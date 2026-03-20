package runtime

import (
	"container/heap"
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest/data"
)

type heapEvent struct {
	ts  int64
	seq int64
	ev  MarketEvent
}

type eventMinHeap []heapEvent

func (h eventMinHeap) Len() int { return len(h) }
func (h eventMinHeap) Less(i, j int) bool {
	if h[i].ts == h[j].ts {
		return h[i].seq < h[j].seq
	}
	return h[i].ts < h[j].ts
}
func (h eventMinHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *eventMinHeap) Push(x any)   { *h = append(*h, x.(heapEvent)) }
func (h *eventMinHeap) Pop() any {
	old := *h
	n := len(old)
	v := old[n-1]
	*h = old[:n-1]
	return v
}

type EventKernel struct {
	cfg      RuntimeConfig
	provider data.DataProvider
	clock    *Clock
	meta     map[string]data.InstrumentMeta

	mu      sync.Mutex
	state   RuntimeState
	heap    eventMinHeap
	nextSeq int64

	marketSinks map[string]func(MarketEventBatch)
	tradeSinks  map[string]func(MarketEventBatch)

	statusSubs map[chan map[string]any]struct{}
	currentNS  int64

	injected1m   map[string]bool
	loadedKlines map[string]map[int]bool // symbol -> durationSeconds -> loaded
	loadedTicks  map[string]bool         // symbol -> loaded
}

func NewEventKernel(cfg RuntimeConfig, provider data.DataProvider) (*EventKernel, error) {
	if provider == nil {
		return nil, fmt.Errorf("data provider is required")
	}
	cfg = NormalizeConfig(cfg)
	clock, err := NewClock(cfg.StartDT, cfg.EndDT, time.Local)
	if err != nil {
		return nil, err
	}
	k := &EventKernel{
		cfg:          cfg,
		provider:     provider,
		clock:        clock,
		state:        RuntimeStateInit,
		marketSinks:  map[string]func(MarketEventBatch){},
		tradeSinks:   map[string]func(MarketEventBatch){},
		statusSubs:   map[chan map[string]any]struct{}{},
		currentNS:    cfg.StartDT.UnixNano(),
		injected1m:   map[string]bool{},
		loadedKlines: map[string]map[int]bool{},
		loadedTicks:  map[string]bool{},
	}
	if err := k.loadInitialEvents(context.Background()); err != nil {
		return nil, err
	}
	k.state = RuntimeStateReady
	k.emitStatusLocked()
	return k, nil
}

func (k *EventKernel) loadInitialEvents(ctx context.Context) error {
	k.state = RuntimeStateLoading
	meta, err := k.provider.LoadInstrumentMeta(ctx, k.cfg.Symbols)
	if err != nil && k.cfg.StrictMode {
		return err
	}
	k.meta = meta

	durs := uniquePositiveDurations(k.cfg.KlineDurations)
	if k.cfg.QuoteBasisPolicy == QuoteBasisCompatAuto1m {
		durs = appendIfMissing(durs, 60)
	}

	for _, symbol := range k.cfg.Symbols {
		if ticks, err := k.provider.LoadTickSeries(ctx, symbol, k.cfg.StartDT, k.cfg.EndDT); err == nil {
			k.addTickEvents(symbol, ticks)
			k.loadedTicks[symbol] = true
		}
		for _, dur := range durs {
			rows, err := k.provider.LoadKlineSeries(ctx, symbol, dur, k.cfg.StartDT, k.cfg.EndDT)
			if err != nil {
				if k.cfg.StrictMode {
					return err
				}
				continue
			}
			k.addKlineEvents(symbol, dur, rows)
			if k.loadedKlines[symbol] == nil {
				k.loadedKlines[symbol] = map[int]bool{}
			}
			k.loadedKlines[symbol][dur] = true
			if dur == 60 {
				k.injected1m[symbol] = true
			}
		}
	}
	return nil
}

// AddKlineData loads kline data for a symbol+duration into the kernel heap.
// Must be called before the kernel starts running (state == RuntimeStateReady).
// Safe to call multiple times — duplicate symbol+duration pairs are skipped.
func (k *EventKernel) AddKlineData(ctx context.Context, symbol string, durationSeconds int) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.state == RuntimeStateRunning || k.state == RuntimeStateFinished {
		return fmt.Errorf("cannot add data after kernel has started")
	}
	if k.loadedKlines[symbol] != nil && k.loadedKlines[symbol][durationSeconds] {
		return nil // already loaded
	}
	if k.meta == nil {
		k.meta = map[string]data.InstrumentMeta{}
	}
	if _, ok := k.meta[symbol]; !ok {
		if meta, err := k.provider.LoadInstrumentMeta(ctx, []string{symbol}); err == nil {
			for s, m := range meta {
				k.meta[s] = m
			}
		}
	}
	rows, err := k.provider.LoadKlineSeries(ctx, symbol, durationSeconds, k.cfg.StartDT, k.cfg.EndDT)
	if err != nil {
		return err
	}
	k.addKlineEvents(symbol, durationSeconds, rows)
	if k.loadedKlines[symbol] == nil {
		k.loadedKlines[symbol] = map[int]bool{}
	}
	k.loadedKlines[symbol][durationSeconds] = true
	if durationSeconds == 60 {
		k.injected1m[symbol] = true
	}
	// Auto-inject 1m for quote basis if policy requires it.
	if (k.cfg.QuoteBasisPolicy == QuoteBasisCompatAuto1m || k.cfg.QuoteBasisPolicy == QuoteBasisOnOrderAuto1m) &&
		!k.injected1m[symbol] {
		if rows1m, err := k.provider.LoadKlineSeries(ctx, symbol, 60, k.cfg.StartDT, k.cfg.EndDT); err == nil {
			k.addKlineEvents(symbol, 60, rows1m)
			k.injected1m[symbol] = true
			if k.loadedKlines[symbol] == nil {
				k.loadedKlines[symbol] = map[int]bool{}
			}
			k.loadedKlines[symbol][60] = true
		}
	}
	return nil
}

// AddTickData loads tick data for a symbol into the kernel heap.
// Must be called before the kernel starts running (state == RuntimeStateReady).
// Safe to call multiple times — duplicate symbols are skipped.
func (k *EventKernel) AddTickData(ctx context.Context, symbol string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.state == RuntimeStateRunning || k.state == RuntimeStateFinished {
		return fmt.Errorf("cannot add data after kernel has started")
	}
	if k.loadedTicks[symbol] {
		return nil // already loaded
	}
	if k.meta == nil {
		k.meta = map[string]data.InstrumentMeta{}
	}
	if _, ok := k.meta[symbol]; !ok {
		if meta, err := k.provider.LoadInstrumentMeta(ctx, []string{symbol}); err == nil {
			for s, m := range meta {
				k.meta[s] = m
			}
		}
	}
	ticks, err := k.provider.LoadTickSeries(ctx, symbol, k.cfg.StartDT, k.cfg.EndDT)
	if err != nil {
		return err
	}
	k.addTickEvents(symbol, ticks)
	k.loadedTicks[symbol] = true
	return nil
}

func (k *EventKernel) addTickEvents(symbol string, ticks []api.Tick) {
	if len(ticks) == 0 {
		return
	}
	sort.Slice(ticks, func(i, j int) bool { return ticks[i].Datetime < ticks[j].Datetime })
	for _, t := range ticks {
		quote := k.quoteFromTick(symbol, t)
		heap.Push(&k.heap, heapEvent{
			ts:  t.Datetime,
			seq: k.nextSeq,
			ev:  MarketEvent{EventTimeNS: t.Datetime, Symbol: symbol, EventType: EventTick, DurationNS: 0, Tick: cloneTick(&t), Quote: &quote},
		})
		k.nextSeq++
	}
}

func (k *EventKernel) addKlineEvents(symbol string, durationSeconds int, klines []api.Kline) {
	if len(klines) == 0 {
		return
	}
	durN := int64(durationSeconds) * int64(time.Second)
	sort.Slice(klines, func(i, j int) bool { return klines[i].Datetime < klines[j].Datetime })
	for _, row := range klines {
		openRow := row
		openRow.High = row.Open
		openRow.Low = row.Open
		openRow.Close = row.Open
		openRow.Volume = 0
		openRow.CloseOI = row.OpenOI
		oQuote := k.quoteFromKline(symbol, row.Datetime, row.Open, row.OpenOI)
		heap.Push(&k.heap, heapEvent{
			ts:  row.Datetime,
			seq: k.nextSeq,
			ev:  MarketEvent{EventTimeNS: row.Datetime, Symbol: symbol, EventType: EventKlineOpen, DurationNS: durN, Kline: cloneKline(&openRow), Quote: &oQuote},
		})
		k.nextSeq++

		closeTS := row.Datetime + durN - 1000
		cQuote := k.quoteFromKline(symbol, closeTS, row.Close, row.CloseOI)
		heap.Push(&k.heap, heapEvent{
			ts:  closeTS,
			seq: k.nextSeq,
			ev:  MarketEvent{EventTimeNS: closeTS, Symbol: symbol, EventType: EventKlineClose, DurationNS: durN, Kline: cloneKline(&row), Quote: &cQuote},
		})
		k.nextSeq++

		if k.cfg.MatchPricePolicy == MatchPriceCompatOHLCPath {
			for _, p := range []float64{row.High, row.Low, row.Close} {
				q := k.quoteFromKline(symbol, closeTS, p, row.CloseOI)
				heap.Push(&k.heap, heapEvent{
					ts:  closeTS,
					seq: k.nextSeq,
					ev:  MarketEvent{EventTimeNS: closeTS, Symbol: symbol, EventType: EventQuote, DurationNS: durN, Quote: &q},
				})
				k.nextSeq++
			}
		}
	}
}

func (k *EventKernel) quoteFromTick(symbol string, tick api.Tick) api.Quote {
	meta := k.meta[symbol]
	priceTick := meta.PriceTick
	if priceTick == 0 {
		priceTick = 1
	}
	q := api.Quote{
		Symbol:         symbol,
		LastPrice:      tick.LastPrice,
		AskPrice1:      tick.AskPrice1,
		BidPrice1:      tick.BidPrice1,
		AskVolume1:     tick.AskVolume1,
		BidVolume1:     tick.BidVolume1,
		Datetime:       time.Unix(0, tick.Datetime).Format("2006-01-02 15:04:05.000000"),
		PriceTick:      priceTick,
		VolumeMultiple: meta.VolumeMultiple,
		Highest:        tick.Highest,
		Lowest:         tick.Lowest,
		Average:        tick.Average,
		Volume:         tick.Volume,
		Amount:         tick.Amount,
		OpenInterest:   tick.OpenInterest,
	}
	if q.AskPrice1 == 0 {
		q.AskPrice1 = q.LastPrice + priceTick
	}
	if q.BidPrice1 == 0 {
		q.BidPrice1 = q.LastPrice - priceTick
	}
	if q.AskVolume1 == 0 {
		q.AskVolume1 = 1
	}
	if q.BidVolume1 == 0 {
		q.BidVolume1 = 1
	}
	if q.VolumeMultiple == 0 {
		q.VolumeMultiple = 1
	}
	return q
}

func (k *EventKernel) quoteFromKline(symbol string, ts int64, price float64, oi int64) api.Quote {
	meta := k.meta[symbol]
	priceTick := meta.PriceTick
	if priceTick == 0 {
		priceTick = 1
	}
	vm := meta.VolumeMultiple
	if vm == 0 {
		vm = 1
	}
	return api.Quote{
		Symbol:         symbol,
		Datetime:       time.Unix(0, ts).Format("2006-01-02 15:04:05.000000"),
		LastPrice:      price,
		AskPrice1:      price + priceTick,
		BidPrice1:      price - priceTick,
		AskVolume1:     1,
		BidVolume1:     1,
		PriceTick:      priceTick,
		VolumeMultiple: vm,
		OpenInterest:   oi,
		Highest:        math.NaN(),
		Lowest:         math.NaN(),
		Average:        math.NaN(),
		Amount:         math.NaN(),
	}
}

func (k *EventKernel) RegisterMarketSink(id string, fn func(MarketEventBatch)) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if fn == nil {
		delete(k.marketSinks, id)
		return
	}
	k.marketSinks[id] = fn
}

func (k *EventKernel) RegisterTradeSink(id string, fn func(MarketEventBatch)) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if fn == nil {
		delete(k.tradeSinks, id)
		return
	}
	k.tradeSinks[id] = fn
}

func (k *EventKernel) NextBatch(ctx context.Context) (MarketEventBatch, bool, error) {
	select {
	case <-ctx.Done():
		return MarketEventBatch{}, false, ctx.Err()
	default:
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.state == RuntimeStateFinished || len(k.heap) == 0 {
		k.state = RuntimeStateFinished
		// Set current time to end_dt so observers (e.g. web UI) know the backtest is complete.
		k.currentNS = k.cfg.EndDT.UnixNano()
		k.emitStatusLocked()
		return MarketEventBatch{}, false, nil
	}
	k.state = RuntimeStateRunning
	head := heap.Pop(&k.heap).(heapEvent)
	events := []MarketEvent{head.ev}
	for len(k.heap) > 0 {
		peek := k.heap[0]
		if peek.ts != head.ts {
			break
		}
		e := heap.Pop(&k.heap).(heapEvent)
		events = append(events, e.ev)
	}
	daySwitched := k.clock.AdvanceTo(head.ts)
	k.currentNS = head.ts
	k.emitStatusLocked()
	batch := MarketEventBatch{
		BatchTimeNS: head.ts,
		Events:      events,
		TradingDay:  k.clock.TradingDay(),
		DaySwitched: daySwitched,
	}
	// GAP-01: On day switch, load main contract mapping from provider.
	if daySwitched && k.provider != nil {
		if mcm, err := k.provider.LoadMainContractMap(ctx, batch.TradingDay); err == nil && len(mcm) > 0 {
			batch.MainContractMap = mcm
		}
	}
	return batch, true, nil
}

func (k *EventKernel) Step(ctx context.Context) error {
	batch, ok, err := k.NextBatch(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	k.dispatch(batch)
	return nil
}

func (k *EventKernel) Run(ctx context.Context) error {
	for {
		batch, ok, err := k.NextBatch(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		k.dispatch(batch)
	}
}

// HeapLen returns the number of scheduled events remaining on the kernel heap.
func (k *EventKernel) HeapLen() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.heap.Len()
}

func (k *EventKernel) dispatch(batch MarketEventBatch) {
	k.mu.Lock()
	marketSinks := make([]func(MarketEventBatch), 0, len(k.marketSinks))
	for _, fn := range k.marketSinks {
		marketSinks = append(marketSinks, fn)
	}
	tradeSinks := make([]func(MarketEventBatch), 0, len(k.tradeSinks))
	for _, fn := range k.tradeSinks {
		tradeSinks = append(tradeSinks, fn)
	}
	k.mu.Unlock()

	// GAP-10: Filter future data fields from quotes for market sinks.
	// Trade sinks receive the raw batch (they need last_price etc.).
	filteredBatch := sanitizeBatchForBacktest(batch)

	for _, fn := range marketSinks {
		fn(filteredBatch)
	}
	for _, fn := range tradeSinks {
		fn(batch)
	}
}

func (k *EventKernel) EnsureQuoteBasis(symbol string, reason QuoteBasisReason) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.cfg.QuoteBasisPolicy == QuoteBasisNoAuto1m {
		return nil
	}
	if k.cfg.QuoteBasisPolicy == QuoteBasisOnOrderAuto1m && reason != QuoteBasisReasonOrderInsert {
		return nil
	}
	if k.injected1m[symbol] {
		return nil
	}
	rows, err := k.provider.LoadKlineSeries(context.Background(), symbol, 60, k.cfg.StartDT, k.cfg.EndDT)
	if err != nil {
		if k.cfg.StrictMode {
			return err
		}
		return nil
	}
	k.addKlineEvents(symbol, 60, rows)
	k.injected1m[symbol] = true
	return nil
}

func (k *EventKernel) MatchPolicy(symbol string) MatchPricePolicy {
	_ = symbol
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.cfg.MatchPricePolicy
}

// Config returns the runtime configuration.
func (k *EventKernel) Config() RuntimeConfig {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.cfg
}

// Meta returns the instrument metadata for a symbol.
// Falls back to a minimal default if the symbol was not pre-loaded.
func (k *EventKernel) Meta(symbol string) data.InstrumentMeta {
	k.mu.Lock()
	defer k.mu.Unlock()
	if m, ok := k.meta[symbol]; ok {
		return m
	}
	return data.InstrumentMeta{Symbol: symbol, PriceTick: 1, VolumeMultiple: 1}
}

// CurrentNS returns the current simulation time in nanoseconds.
func (k *EventKernel) CurrentNS() int64 {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.currentNS
}

// AllMeta returns a copy of all instrument metadata.
func (k *EventKernel) AllMeta() map[string]data.InstrumentMeta {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make(map[string]data.InstrumentMeta, len(k.meta))
	for k2, v := range k.meta {
		out[k2] = v
	}
	return out
}

func (k *EventKernel) RuntimeStatusSnapshot() map[string]any {
	k.mu.Lock()
	defer k.mu.Unlock()
	return map[string]any{
		"_tqsdk_backtest": map[string]any{
			"start_dt":   k.cfg.StartDT.UnixNano(),
			"current_dt": k.currentNS,
			"end_dt":     k.cfg.EndDT.UnixNano(),
		},
	}
}

func (k *EventKernel) SubscribeRuntimeStatus(ctx context.Context) (<-chan map[string]any, error) {
	ch := make(chan map[string]any, 32)
	k.mu.Lock()
	k.statusSubs[ch] = struct{}{}
	snap := map[string]any{
		"_tqsdk_backtest": map[string]any{
			"start_dt":   k.cfg.StartDT.UnixNano(),
			"current_dt": k.currentNS,
			"end_dt":     k.cfg.EndDT.UnixNano(),
		},
	}
	k.mu.Unlock()
	ch <- snap
	go func() {
		<-ctx.Done()
		k.mu.Lock()
		if _, ok := k.statusSubs[ch]; ok {
			delete(k.statusSubs, ch)
			close(ch)
		}
		k.mu.Unlock()
	}()
	return ch, nil
}

func (k *EventKernel) emitStatusLocked() {
	diff := map[string]any{
		"_tqsdk_backtest": map[string]any{
			"start_dt":   k.cfg.StartDT.UnixNano(),
			"current_dt": k.currentNS,
			"end_dt":     k.cfg.EndDT.UnixNano(),
		},
	}
	for ch := range k.statusSubs {
		select {
		case ch <- cloneAnyMap(diff):
		default:
		}
	}
}

func uniquePositiveDurations(d []int) []int {
	m := map[int]struct{}{}
	out := make([]int, 0, len(d))
	for _, v := range d {
		if v <= 0 {
			continue
		}
		if _, ok := m[v]; ok {
			continue
		}
		m[v] = struct{}{}
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}

func appendIfMissing(in []int, v int) []int {
	for _, x := range in {
		if x == v {
			return in
		}
	}
	return append(in, v)
}

func cloneTick(v *api.Tick) *api.Tick {
	if v == nil {
		return nil
	}
	cp := *v
	return &cp
}

func cloneKline(v *api.Kline) *api.Kline {
	if v == nil {
		return nil
	}
	cp := *v
	return &cp
}

func cloneAnyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		vm, ok := v.(map[string]any)
		if ok {
			out[k] = cloneAnyMap(vm)
			continue
		}
		out[k] = v
	}
	return out
}

// sanitizeBatchForBacktest creates a copy of the batch with future data fields
// removed from Quote objects. This implements GAP-10: Python's _update_valid_quotes.
// Fields removed: Open, Close, Settlement, UpperLimit, LowerLimit,
// PreSettlement, PreClose, PreOpenInterest.
// For KQ.m* (continuous) symbols, UnderlyingSymbol is also removed.
func sanitizeBatchForBacktest(batch MarketEventBatch) MarketEventBatch {
	cp := batch
	cp.Events = make([]MarketEvent, len(batch.Events))
	for i, ev := range batch.Events {
		cp.Events[i] = ev
		if ev.Quote != nil {
			q := *ev.Quote
			sanitizeQuote(&q)
			cp.Events[i].Quote = &q
		}
	}
	return cp
}

func sanitizeQuote(q *api.Quote) {
	q.Open = 0
	q.Close = 0
	q.Settlement = 0
	q.UpperLimit = 0
	q.LowerLimit = 0
	q.PreSettlement = 0
	q.PreClose = 0
	q.PreOpenInterest = 0
	// For continuous contract symbols (KQ.m@*), remove underlying_symbol.
	if strings.HasPrefix(q.Symbol, "KQ.m") {
		q.UnderlyingSymbol = ""
	}
}
