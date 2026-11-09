package backtest

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/internal/tday"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/internal/wire"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

const day = int64(24 * time.Hour)

type eventKind uint8

const (
	evOpen  eventKind = iota // K 线开盘：open=high=low=close，成交量 0
	evClose                  // K 线收盘：最终 OHLC
	evTick                   // 一条 Tick
)

type event struct {
	time int64
	kind eventKind
	bar  model.Kline
	tick *model.Tick
}

// serie 是一条 (合约, 周期) 序列，按 Python _gen_serial 产生事件：K 线每根产生开盘、收盘两个事件；
// 周期为 0 时是 Tick 序列，每条 Tick 一个事件。
type serie struct {
	symbol string
	dur    int64
	cur    cursor

	row     row
	have    bool // row 尚有未产生的事件
	closing bool // K 线的下一个事件是收盘
	started bool
	done    bool

	// 本逻辑包内的累积变化
	touched bool
	rows    map[int64]any // 行 ID → 行对象；nil 表示删除
	ids     []int64
	lastID  *int64
	last    event
	hasLast bool
	empty   bool // 序列没有任何数据：发送 last_id=-1
}

// head 返回下一事件的时间；ok=false 表示序列已结束（越过 end 或数据耗尽）。
func (s *serie) head(ctx context.Context, end int64) (int64, bool, error) {
	if s.done {
		return 0, false, nil
	}
	if !s.have {
		r, ok, err := s.cur.next(ctx)
		if err != nil {
			return 0, false, err
		}
		if !ok {
			s.done = true
			if !s.started {
				s.empty = true
				s.touch()
			}
			return 0, false, nil
		}
		s.row, s.have, s.closing, s.started = r, true, false, true
	}
	t := s.eventTime()
	if t > end {
		s.done = true
		return 0, false, nil
	}
	return t, true, nil
}

func (s *serie) eventTime() int64 {
	dt := s.row.dt
	if s.dur == 0 {
		return dt
	}
	if s.dur < day {
		if s.closing {
			return dt + s.dur - 1000
		}
		return dt
	}
	if s.closing {
		return tday.Start(dt+s.dur) - 1000
	}
	return tday.Start(dt)
}

// pop 产生下一事件并记入本包的序列变化；调用前 head 必须返回 ok。
func (s *serie) pop() event {
	e := event{time: s.eventTime(), bar: s.row.bar, tick: s.row.tick}
	id := s.row.id
	switch {
	case s.dur == 0: // 同 Python：发布该条并删除 10000 条之前的行，控制下游内存
		e.kind = evTick
		s.have = false
		s.setRow(id, wire.Fields(e.tick, tickFields))
		if id >= warmupBars {
			s.setRow(id-warmupBars, nil)
		}
		s.lastID = &id
	case s.closing:
		e.kind = evClose
		s.have = false
		s.setRow(id, wire.Fields(&e.bar, klineFields))
	default:
		s.closing = true
		open := e.bar
		open.High, open.Low, open.Close, open.Volume, open.CloseOI = open.Open, open.Open, open.Open, 0, open.OpenOI
		s.setRow(id, wire.Fields(&open, klineFields))
		s.lastID = &id
	}
	s.last, s.hasLast = e, true
	s.touch()
	return e
}

func (s *serie) touch() { s.touched = true }

func (s *serie) setRow(id int64, v *wire.Object) {
	if s.rows == nil {
		s.rows = map[int64]any{}
	}
	if _, ok := s.rows[id]; !ok {
		s.ids = append(s.ids, id)
	}
	if v == nil {
		s.rows[id] = nil // 不存 nil *Object：编码为 null（删除）
	} else {
		s.rows[id] = v
	}
}

// diff 返回本包的序列 diff（klines 或 ticks）并清空累积。
func (s *serie) diff() *wire.Object {
	node := new(wire.Object)
	if s.empty {
		node.Set("last_id", int64(-1))
		s.empty = false
	}
	if s.lastID != nil {
		node.Set("last_id", *s.lastID)
	}
	if len(s.ids) > 0 {
		data := new(wire.Object)
		for _, id := range s.ids {
			data.Set(strconv.FormatInt(id, 10), s.rows[id])
		}
		node.Set("data", data)
	}
	s.rows, s.ids, s.lastID, s.touched = nil, nil, nil, false
	if s.dur == 0 {
		return new(wire.Object).Set("ticks", new(wire.Object).Set(s.symbol, node))
	}
	return new(wire.Object).Set("klines", new(wire.Object).Set(s.symbol,
		new(wire.Object).Set(strconv.FormatInt(s.dur, 10), node)))
}

var (
	klineFields = []string{"datetime", "open", "high", "low", "close", "volume", "open_oi", "close_oi"}
	// tickQuoteFields 是 Tick 中同时属于报价的字段（除 datetime）。
	tickQuoteFields = []string{"last_price", "average", "highest", "lowest",
		"ask_price1", "ask_volume1", "bid_price1", "bid_volume1", "ask_price2", "ask_volume2", "bid_price2", "bid_volume2",
		"ask_price3", "ask_volume3", "bid_price3", "bid_volume3", "ask_price4", "ask_volume4", "bid_price4", "bid_volume4",
		"ask_price5", "ask_volume5", "bid_price5", "bid_volume5", "volume", "amount", "open_interest"}
	tickFields = append([]string{"datetime"}, tickQuoteFields...)
)

// tickQuote 按 Python _get_quote_diffs_from_tick 由 Tick 生成报价：字段原样复制，不加价差，只有一步撮合。
func tickQuote(symbol string, t *model.Tick) ([]any, []quoteStep) {
	q := new(wire.Object).Set("datetime", tday.Datetime(t.Datetime)).Fields(t, tickQuoteFields)
	return []any{new(wire.Object).Set("quotes", new(wire.Object).Set(symbol, q))},
		[]quoteStep{{ask: t.AskPrice1, bid: t.BidPrice1, last: t.LastPrice}}
}

// quoteDiffs 按 Python _get_quote_diffs_from_kline 由 K 线事件生成报价：盘口为价格 ± price_tick，
// 第一包带最新价等字段，其后各包只改变买卖价。返回 diff 与对应的撮合盘口序列。
func quoteDiffs(symbol string, tick float64, e event, minDur, dur int64) ([]any, []quoteStep) {
	froms := []string{"open"}
	if e.kind == evClose {
		froms = []string{"close"}
		if dur == minDur {
			froms = []string{"high", "low", "close"}
		}
	}
	last := price(e.bar, froms[len(froms)-1])
	oi := e.bar.OpenOI
	if e.kind == evClose {
		oi = e.bar.CloseOI
	}
	nan := math.NaN()
	var diffs []any
	var steps []quoteStep
	for i, f := range froms {
		p := price(e.bar, f)
		q := new(wire.Object)
		if i == 0 {
			q.Set("datetime", tday.Datetime(e.time)).
				Set("ask_price1", p+tick).Set("ask_volume1", int64(1)).
				Set("bid_price1", p-tick).Set("bid_volume1", int64(1)).
				Set("last_price", last).Set("highest", nan).Set("lowest", nan).Set("average", nan).
				Set("volume", int64(0)).Set("amount", nan).Set("open_interest", oi)
		} else {
			q.Set("ask_price1", p+tick).Set("bid_price1", p-tick)
		}
		diffs = append(diffs, new(wire.Object).Set("quotes", new(wire.Object).Set(symbol, q)))
		steps = append(steps, quoteStep{ask: p + tick, bid: p - tick, last: last})
	}
	return diffs, steps
}

type quoteStep struct{ ask, bid, last float64 }

func price(k model.Kline, f string) float64 {
	switch f {
	case "open":
		return k.Open
	case "high":
		return k.High
	case "low":
		return k.Low
	}
	return k.Close
}
