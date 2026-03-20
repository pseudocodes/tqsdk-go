package webadapter

import (
	"math"
	"strconv"
	"sync"
	"time"
)

// SeriesStyle controls how the indicator line is rendered.
type SeriesStyle string

const (
	StyleLine    SeriesStyle = "LINE"
	StyleDot     SeriesStyle = "DOT"
	StyleDash    SeriesStyle = "DASH"
	StyleBar     SeriesStyle = "BAR"
	StyleKSerial SeriesStyle = "KSERIAL"
)

// BoardTarget controls which panel the series is drawn on.
type BoardTarget string

const (
	BoardMain BoardTarget = "MAIN"
	BoardB0   BoardTarget = "B0"
	BoardB1   BoardTarget = "B1"
	BoardB2   BoardTarget = "B2"
)

// SeriesOpts configures a registered indicator series.
type SeriesOpts struct {
	Style SeriesStyle // default LINE
	Color string      // default "#FF0000"
	Width int         // default 1
	Board BoardTarget // default MAIN
}

func (o SeriesOpts) normalize() SeriesOpts {
	if o.Style == "" {
		o.Style = StyleLine
	}
	if o.Color == "" {
		o.Color = "#FF0000"
	}
	if o.Width <= 0 {
		o.Width = 1
	}
	if o.Board == "" {
		o.Board = BoardMain
	}
	return o
}

// IndicatorOverlay manages multiple indicator series for a single
// symbol + duration pair and pushes updates to the WebAdapter.
type IndicatorOverlay struct {
	adapter *Adapter
	symbol  string
	durNano int64

	mu     sync.Mutex
	series map[string]*IndicatorSeries
}

// NewIndicatorOverlay creates an overlay bound to a symbol and kline duration.
func NewIndicatorOverlay(adapter *Adapter, symbol string, durationSeconds int) *IndicatorOverlay {
	return &IndicatorOverlay{
		adapter: adapter,
		symbol:  symbol,
		durNano: int64(durationSeconds) * int64(time.Second),
		series:  map[string]*IndicatorSeries{},
	}
}

// AddSeries registers a named indicator series and returns a write handle.
func (o *IndicatorOverlay) AddSeries(id string, opts SeriesOpts) *IndicatorSeries {
	opts = opts.normalize()
	s := &IndicatorSeries{
		id:      id,
		opts:    opts,
		overlay: o,
		dirty:   map[int64]any{},
	}
	o.mu.Lock()
	o.series[id] = s
	o.mu.Unlock()
	return s
}

// RemoveSeries removes a previously registered series.
func (o *IndicatorOverlay) RemoveSeries(id string) {
	o.mu.Lock()
	delete(o.series, id)
	o.mu.Unlock()
}

// Flush pushes all pending indicator data to the WebAdapter and clears
// the dirty buffers. Call this once per event-loop iteration.
func (o *IndicatorOverlay) Flush() {
	o.mu.Lock()
	defer o.mu.Unlock()

	datas := map[string]any{}
	for _, s := range o.series {
		d := s.drain()
		if d != nil {
			datas[s.id] = d
		}
	}
	if len(datas) == 0 {
		return
	}
	o.adapter.PushChartData(o.symbol, o.durNano, datas)
}

// IndicatorSeries is a write handle for a single named indicator.
type IndicatorSeries struct {
	id      string
	opts    SeriesOpts
	overlay *IndicatorOverlay

	mu    sync.Mutex
	dirty map[int64]any // klineID → value (float64 or ohlc map)
}

// Set records a scalar indicator value for the given kline ID.
// The value is buffered until Flush is called.
func (s *IndicatorSeries) Set(klineID int64, value float64) {
	if math.IsNaN(value) {
		return
	}
	s.mu.Lock()
	s.dirty[klineID] = value
	s.mu.Unlock()
}

// SetOHLC records a KSERIAL-style value (candlestick overlay).
func (s *IndicatorSeries) SetOHLC(klineID int64, o, h, l, c float64) {
	s.mu.Lock()
	s.dirty[klineID] = [4]float64{o, h, l, c}
	s.mu.Unlock()
}

// drain collects dirty data into a draw_chart_datas-compatible map and
// resets the buffer. Returns nil if nothing is dirty.
func (s *IndicatorSeries) drain() map[string]any {
	s.mu.Lock()
	if len(s.dirty) == 0 {
		s.mu.Unlock()
		return nil
	}
	dirty := s.dirty
	s.dirty = map[int64]any{}
	s.mu.Unlock()

	minID := int64(math.MaxInt64)
	maxID := int64(math.MinInt64)
	for id := range dirty {
		if id < minID {
			minID = id
		}
		if id > maxID {
			maxID = id
		}
	}

	data := map[string]any{}
	isKSerial := s.opts.Style == StyleKSerial
	for id, v := range dirty {
		key := strconv.FormatInt(id, 10)
		if isKSerial {
			ohlc := v.([4]float64)
			data[key] = map[string]any{
				"open": ohlc[0], "high": ohlc[1],
				"low": ohlc[2], "close": ohlc[3],
			}
		} else {
			data[key] = map[string]any{"value": v}
		}
	}

	out := map[string]any{
		"type":        "SERIAL",
		"range_left":  minID,
		"range_right": maxID,
		"data":        data,
	}
	if isKSerial {
		out["type"] = "KSERIAL"
	} else {
		out["style"] = string(s.opts.Style)
		out["color"] = s.opts.Color
		out["width"] = s.opts.Width
	}
	out["board"] = string(s.opts.Board)
	return out
}
