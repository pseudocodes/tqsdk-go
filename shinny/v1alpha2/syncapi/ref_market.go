package syncapi

import (
	"sync"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
)

// QuoteRef is a live reference to a symbol's quote.
// Counterpart: Python quote = api.get_quote("SHFE.rb2501")
type QuoteRef struct {
	symbol string
	market api.MarketService
}

func (r *QuoteRef) refKey() refKey { return refKey{kind: refQuote, symbol: r.symbol} }

// Get returns the latest quote snapshot from the store.
func (r *QuoteRef) Get() api.Quote {
	q, _ := r.market.Quote(r.symbol)
	return q
}

// Symbol returns the instrument code.
func (r *QuoteRef) Symbol() string { return r.symbol }

// KlineRef is a live reference to a kline series.
// Counterpart: Python klines = api.get_kline_serial("SHFE.rb2501", 60)
type KlineRef struct {
	symbol   string
	duration int

	mu            sync.RWMutex
	lastFrame     []*api.Kline
	changedBarID  int64         // bar ID that changed in the latest event (§8.8.6)
	lastEventKind api.EventKind // "append" or "update" (§8.8.6)
}

func (r *KlineRef) refKey() refKey {
	return refKey{kind: refKline, symbol: r.symbol, duration: r.duration}
}

// Get returns the latest kline frame (index 0 = oldest).
func (r *KlineRef) Get() []*api.Kline {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastFrame
}

// Bar returns a KlineBar reference for the bar at the given index.
// Supports negative indexing: Bar(-1) = last bar, Bar(-2) = second to last.
//
// Counterpart: Python klines.iloc[-1]
//
// The returned KlineBar implements Ref and can be passed to sa.IsChanging().
func (r *KlineRef) Bar(index int) *KlineBar {
	return &KlineBar{series: r, index: index}
}

// Symbol returns the instrument code.
func (r *KlineRef) Symbol() string { return r.symbol }

// Duration returns the kline period in seconds.
func (r *KlineRef) Duration() int { return r.duration }

func (r *KlineRef) updateFrame(frame []*api.Kline) {
	r.mu.Lock()
	r.lastFrame = frame
	r.mu.Unlock()
}

// KlineBar is a reference to a single bar within a kline series.
//
// Counterpart: Python klines.iloc[-1] (pandas.Series)
//
// Implements Ref so it can be passed to sa.IsChanging():
//
//	sa.IsChanging(klines.Bar(-1))              // did this bar update?
//	sa.IsChanging(klines.Bar(-1), "datetime")  // new bar produced?
type KlineBar struct {
	series *KlineRef
	index  int // supports negative index
}

func (b *KlineBar) refKey() refKey {
	return refKey{
		kind:     refKlineBar,
		symbol:   b.series.symbol,
		duration: b.series.duration,
		barIndex: b.index,
	}
}

// Get returns the Kline data for this bar, or nil if out of range.
func (b *KlineBar) Get() *api.Kline {
	b.series.mu.RLock()
	defer b.series.mu.RUnlock()
	frame := b.series.lastFrame
	idx := b.index
	if idx < 0 {
		idx = len(frame) + idx
	}
	if idx < 0 || idx >= len(frame) {
		return nil
	}
	return frame[idx]
}

// TickRef is a live reference to a tick series.
type TickRef struct {
	symbol string
	market api.MarketService

	mu        sync.RWMutex
	lastFrame []*api.Tick
}

func (r *TickRef) refKey() refKey { return refKey{kind: refTick, symbol: r.symbol} }

// Get returns the latest tick frame.
func (r *TickRef) Get() []*api.Tick {
	r.mu.RLock()
	defer r.mu.RUnlock()
	// Prefer cached frame; fall back to store query.
	if r.lastFrame != nil {
		return r.lastFrame
	}
	frame, _ := r.market.TickFrame(r.symbol)
	return frame
}

// Symbol returns the instrument code.
func (r *TickRef) Symbol() string { return r.symbol }

func (r *TickRef) updateFrame(frame []*api.Tick) {
	r.mu.Lock()
	r.lastFrame = frame
	r.mu.Unlock()
}
