package market

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest/data"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest/runtime"
)

type Option func(*simMarketService)

func WithAutoRun(v bool) Option {
	return func(s *simMarketService) { s.autoRun = v }
}

// WithLazyDownloader sets a live market service used to download data on-demand
// when SubscribeKlines or SubscribeTicks is called before the kernel starts.
func WithLazyDownloader(downloader api.MarketService) Option {
	return func(s *simMarketService) { s.downloader = downloader }
}

// tickRingBuffer is a fixed-capacity circular buffer for api.Tick values.
// Push is O(1) with zero allocation; Tail returns the most recent n entries in O(n).
type tickRingBuffer struct {
	buf   []api.Tick // fixed length = cap
	cap   int
	head  int // next write position (0..cap-1)
	count int // total items ever written (used to derive actual size)
}

func newTickRingBuffer(capacity int) *tickRingBuffer {
	if capacity <= 0 {
		capacity = 1
	}
	return &tickRingBuffer{
		buf: make([]api.Tick, capacity),
		cap: capacity,
	}
}

// Push appends a tick, overwriting the oldest entry when full. O(1), no allocation.
func (r *tickRingBuffer) Push(t api.Tick) {
	r.buf[r.head] = t
	r.head = (r.head + 1) % r.cap
	r.count++
}

// Len returns the number of items currently in the buffer (<= cap).
func (r *tickRingBuffer) Len() int {
	if r.count < r.cap {
		return r.count
	}
	return r.cap
}

// Tail returns the most recent `width` ticks as []*api.Tick.
// The slice is left-padded with nil when fewer items are available,
// matching the original buildTickFrame semantics.
func (r *tickRingBuffer) Tail(width int) []*api.Tick {
	out := make([]*api.Tick, width)
	n := r.Len()
	if n == 0 || width <= 0 {
		return out
	}
	take := n
	if take > width {
		take = width
	}
	// start is the buf index of the oldest entry within the take window.
	start := (r.head - take + r.cap) % r.cap
	offset := width - take
	for i := 0; i < take; i++ {
		idx := (start + i) % r.cap
		cp := r.buf[idx]
		out[offset+i] = &cp
	}
	return out
}

// klineRingBuffer is a fixed-capacity circular buffer for api.Kline values.
// Unlike tickRingBuffer, it provides PushOrUpdate semantics: if the incoming
// kline has the same ID as the last entry, it updates in-place (EventKlineClose)
// instead of appending (EventKlineOpen). Both operations are O(1).
type klineRingBuffer struct {
	buf   []api.Kline // fixed length = cap
	cap   int
	head  int // next write position (0..cap-1)
	count int // total items appended (update does not increment)
}

func newKlineRingBuffer(capacity int) *klineRingBuffer {
	if capacity <= 0 {
		capacity = 1
	}
	return &klineRingBuffer{
		buf: make([]api.Kline, capacity),
		cap: capacity,
	}
}

// PushOrUpdate appends k if its ID differs from the last entry, or updates
// the last entry in-place if the ID matches. O(1), no allocation.
func (r *klineRingBuffer) PushOrUpdate(k api.Kline) {
	if r.Len() > 0 {
		lastIdx := (r.head - 1 + r.cap) % r.cap
		if r.buf[lastIdx].ID == k.ID {
			r.buf[lastIdx] = k
			return
		}
	}
	r.buf[r.head] = k
	r.head = (r.head + 1) % r.cap
	r.count++
}

// Len returns the number of items currently in the buffer (<= cap).
func (r *klineRingBuffer) Len() int {
	if r.count < r.cap {
		return r.count
	}
	return r.cap
}

// Tail returns the most recent `width` klines as []*api.Kline.
// The slice is left-padded with nil when fewer items are available,
// matching the original buildKlineFrame semantics.
func (r *klineRingBuffer) Tail(width int) []*api.Kline {
	out := make([]*api.Kline, width)
	n := r.Len()
	if n == 0 || width <= 0 {
		return out
	}
	take := n
	if take > width {
		take = width
	}
	start := (r.head - take + r.cap) % r.cap
	offset := width - take
	for i := 0; i < take; i++ {
		idx := (start + i) % r.cap
		cp := r.buf[idx]
		out[offset+i] = &cp
	}
	return out
}

type simMarketService struct {
	rt         *runtime.EventKernel
	provider   data.DataProvider
	downloader api.MarketService // optional: used for lazy data download on Subscribe
	autoRun    bool

	mu      sync.RWMutex
	started bool
	closed  bool
	runOnce sync.Once

	nextSubID int64
	quotes    map[string]api.Quote
	ticks     map[string]*tickRingBuffer
	klines    map[string]map[int64]*klineRingBuffer

	quoteSubs map[int64]*quoteSub
	tickSubs  map[string]*tickSub
	klineSubs map[string]*klineSub

	connSubsMu sync.Mutex
	connSubs   map[chan api.ConnEvent]struct{}
}

type quoteSub struct {
	id      int64
	symbols map[string]struct{}
	ch      chan api.QuoteEvent
	done    chan struct{}
	once    sync.Once
}

type tickSub struct {
	chartID string
	symbol  string
	width   int
	opts    api.TickSubOptions
	ch      chan api.TickEvent
	done    chan struct{}

	mu          sync.RWMutex
	serialReady bool
	lastID      int64
	closed      bool
	once        sync.Once
}

type klineSub struct {
	chartID   string
	symbols   []string
	durationN int64
	width     int
	opts      api.KlineSubOptions
	ch        chan api.KlineEvent
	done      chan struct{}

	mu          sync.RWMutex
	serialReady bool
	lastID      int64
	closed      bool
	once        sync.Once
}

func NewSimMarketService(rt *runtime.EventKernel, dp data.DataProvider, opts ...Option) api.MarketService {
	s := &simMarketService{
		rt:        rt,
		provider:  dp,
		autoRun:   true,
		quotes:    map[string]api.Quote{},
		ticks:     map[string]*tickRingBuffer{},
		klines:    map[string]map[int64]*klineRingBuffer{},
		quoteSubs: map[int64]*quoteSub{},
		tickSubs:  map[string]*tickSub{},
		klineSubs: map[string]*klineSub{},
		connSubs:  map[chan api.ConnEvent]struct{}{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

func (m *simMarketService) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return api.NewError(api.ErrDisconnected, "sim market service closed", nil)
	}
	if m.started {
		m.mu.Unlock()
		return nil
	}
	m.started = true
	m.mu.Unlock()

	m.rt.RegisterMarketSink("sim_market_service", m.onBatch)
	m.emitConnEvent(api.ConnEvent{State: "ready", At: time.Now()})

	if m.autoRun {
		m.runOnce.Do(func() {
			go func() {
				if err := m.rt.Run(ctx); err != nil {
					m.emitConnEvent(api.ConnEvent{State: "disconnected", Err: err, At: time.Now()})
				}
			}()
		})
	}
	return nil
}

func (m *simMarketService) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	quoteSubs := make([]*quoteSub, 0, len(m.quoteSubs))
	for _, s := range m.quoteSubs {
		quoteSubs = append(quoteSubs, s)
	}
	tickSubs := make([]*tickSub, 0, len(m.tickSubs))
	for _, s := range m.tickSubs {
		tickSubs = append(tickSubs, s)
	}
	klineSubs := make([]*klineSub, 0, len(m.klineSubs))
	for _, s := range m.klineSubs {
		klineSubs = append(klineSubs, s)
	}
	m.quoteSubs = map[int64]*quoteSub{}
	m.tickSubs = map[string]*tickSub{}
	m.klineSubs = map[string]*klineSub{}
	m.mu.Unlock()

	for _, s := range quoteSubs {
		s.Close()
	}
	for _, s := range tickSubs {
		s.Close()
	}
	for _, s := range klineSubs {
		s.Close()
	}
	m.rt.RegisterMarketSink("sim_market_service", nil)

	m.connSubsMu.Lock()
	for ch := range m.connSubs {
		close(ch)
	}
	m.connSubs = map[chan api.ConnEvent]struct{}{}
	m.connSubsMu.Unlock()
	return nil
}

func (m *simMarketService) onBatch(batch runtime.MarketEventBatch) {
	m.mu.Lock()
	for _, ev := range batch.Events {
		if ev.Quote != nil {
			m.quotes[ev.Symbol] = *ev.Quote
		}
		switch ev.EventType {
		case runtime.EventTick:
			if ev.Tick != nil {
				ring := m.ticks[ev.Symbol]
				if ring == nil {
					ring = newTickRingBuffer(api.MarketMaxDataLength)
					m.ticks[ev.Symbol] = ring
				}
				ring.Push(*ev.Tick)
			}
		case runtime.EventKlineOpen, runtime.EventKlineClose:
			if ev.Kline != nil {
				if m.klines[ev.Symbol] == nil {
					m.klines[ev.Symbol] = map[int64]*klineRingBuffer{}
				}
				ring := m.klines[ev.Symbol][ev.DurationNS]
				if ring == nil {
					ring = newKlineRingBuffer(api.MarketMaxDataLength)
					m.klines[ev.Symbol][ev.DurationNS] = ring
				}
				ring.PushOrUpdate(*ev.Kline)
			}
		}
	}

	quoteSubs := make([]*quoteSub, 0, len(m.quoteSubs))
	for _, sub := range m.quoteSubs {
		quoteSubs = append(quoteSubs, sub)
	}
	tickSubs := make([]*tickSub, 0, len(m.tickSubs))
	for _, sub := range m.tickSubs {
		tickSubs = append(tickSubs, sub)
	}
	klineSubs := make([]*klineSub, 0, len(m.klineSubs))
	for _, sub := range m.klineSubs {
		klineSubs = append(klineSubs, sub)
	}
	m.mu.Unlock()

	now := time.Now()
	for _, ev := range batch.Events {
		if ev.Quote != nil {
			for _, sub := range quoteSubs {
				if _, ok := sub.symbols[ev.Symbol]; !ok {
					continue
				}
				sub.enqueue(api.QuoteEvent{Quote: *ev.Quote, ChangedAt: now})
			}
		}
		if ev.EventType == runtime.EventTick && ev.Tick != nil {
			for _, sub := range tickSubs {
				if sub.symbol != ev.Symbol || sub.isClosed() {
					continue
				}
				sub.emit(ev.Tick, m.buildTickFrame(ev.Symbol, sub.width), now)
			}
		}
		if (ev.EventType == runtime.EventKlineOpen || ev.EventType == runtime.EventKlineClose) && ev.Kline != nil {
			for _, sub := range klineSubs {
				if sub.isClosed() {
					continue
				}
				if len(sub.symbols) == 0 || sub.symbols[0] != ev.Symbol || sub.durationN != ev.DurationNS {
					continue
				}
				kind := api.EventUpdate
				if ev.EventType == runtime.EventKlineOpen {
					kind = api.EventAppend
				}
				frame := m.buildKlineFrame(ev.Symbol, ev.DurationNS, sub.width)
				row := api.AlignedKlineRow{
					MainID:   ev.Kline.ID,
					Datetime: time.Unix(0, ev.Kline.Datetime),
					Main:     *ev.Kline,
					Others:   map[string]*api.Kline{},
					Binding:  map[string]int64{},
				}
				sub.emit(kind, ev.Kline, row, frame, now)
			}
		}
	}
}

func (m *simMarketService) ensureQuoteBasis(symbol string, reason runtime.QuoteBasisReason) error {
	return m.rt.EnsureQuoteBasis(symbol, reason)
}

func (m *simMarketService) SubscribeQuotes(ctx context.Context, symbols ...string) (api.QuoteSub, error) {
	if len(symbols) == 0 {
		return nil, api.NewError(api.ErrInvalidSymbol, "empty symbols", nil)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started {
		return nil, api.NewError(api.ErrDisconnected, "sim market service not started", nil)
	}
	sm := map[string]struct{}{}
	for _, s := range symbols {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, api.NewError(api.ErrInvalidSymbol, "empty symbol", nil)
		}
		sm[s] = struct{}{}
		_ = m.rt.EnsureQuoteBasis(s, runtime.QuoteBasisReasonQuoteSubscribe)
	}
	id := atomic.AddInt64(&m.nextSubID, 1)
	sub := &quoteSub{id: id, symbols: sm, ch: make(chan api.QuoteEvent, 128), done: make(chan struct{})}
	m.quoteSubs[id] = sub
	go bindCtx(ctx, func() { _ = sub.Close() })
	for sym := range sm {
		if q, ok := m.quotes[sym]; ok {
			sub.enqueue(api.QuoteEvent{Quote: q, ChangedAt: time.Now()})
		}
	}
	return sub, nil
}

func (m *simMarketService) SubscribeTicks(ctx context.Context, symbol string, dataLength int, opts ...api.TickSubOption) (api.TickSub, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return nil, api.NewError(api.ErrInvalidSymbol, "empty symbol", nil)
	}
	if dataLength <= 0 {
		dataLength = 200
	}
	if dataLength > api.MarketMaxDataLength {
		dataLength = api.MarketMaxDataLength
	}
	op := api.ResolveTickSubOptions(opts...)
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started {
		return nil, api.NewError(api.ErrDisconnected, "sim market service not started", nil)
	}

	// Lazy-load tick data if not yet in the kernel.
	if m.downloader != nil {
		if ll, ok := m.provider.(data.LazyLoader); ok {
			cfg := m.rt.Config()
			if err := ll.EnsureTickSeries(ctx, symbol, cfg.StartDT, cfg.EndDT, m.downloader); err != nil {
				return nil, fmt.Errorf("lazy load ticks %s: %w", symbol, err)
			}
			if err := m.rt.AddTickData(ctx, symbol); err != nil {
				return nil, fmt.Errorf("add tick data %s: %w", symbol, err)
			}
		}
	}

	// Pre-fill: fetch historical ticks before startDT so the first emitted
	// frame is fully populated, matching Python TqBacktest behavior where
	// set_chart uses focus_position=view_width to place focus_datetime at
	// the right edge of the window.
	m.prefillTicks(ctx, symbol, dataLength)

	chartID := genID("BT_tick")
	sub := &tickSub{chartID: chartID, symbol: symbol, width: dataLength, opts: op, ch: make(chan api.TickEvent, 128), done: make(chan struct{}), lastID: -1}
	m.tickSubs[chartID] = sub
	go bindCtx(ctx, func() { _ = sub.Close() })
	return sub, nil
}

func (m *simMarketService) Quote(symbol string) (api.Quote, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	q, ok := m.quotes[symbol]
	return q, ok
}

func (m *simMarketService) TickFrame(symbol string) ([]*api.Tick, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	width := 0
	for _, s := range m.tickSubs {
		if s.symbol == symbol {
			width = s.width
			break
		}
	}
	if width == 0 {
		return nil, false
	}
	return m.buildTickFrame(symbol, width), true
}

func (m *simMarketService) GetTicks(ctx context.Context, req api.TickGetReq) (api.TickGetResult, error) {
	_ = ctx
	frame, _ := m.TickFrame(req.Symbol)
	if frame == nil {
		frame = []*api.Tick{}
	}
	return api.TickGetResult{Data: frame, Frame: frame, Complete: true}, nil
}

func (m *simMarketService) QueryTicksPage(ctx context.Context, req api.TickQueryReq) (api.TickBatch, error) {
	_ = ctx
	frame, _ := m.TickFrame(req.Symbol)
	if frame == nil {
		frame = []*api.Tick{}
	}
	left, right := int64(-1), int64(-1)
	for _, t := range frame {
		if t == nil {
			continue
		}
		if left < 0 || t.ID < left {
			left = t.ID
		}
		if t.ID > right {
			right = t.ID
		}
	}
	return api.TickBatch{Frame: frame, Ready: true, MoreData: false, LeftID: left, RightID: right}, nil
}

func (m *simMarketService) GetTickDataSeries(ctx context.Context, symbol string, startDT time.Time, endDT time.Time) (api.TickDataSeriesResult, error) {
	rows, err := m.provider.LoadTickSeries(ctx, symbol, startDT, endDT)
	if err != nil {
		return api.TickDataSeriesResult{}, api.NewError(api.ErrUnsupportedBacktest, "tick data unavailable", err)
	}
	out := make([]*api.Tick, 0, len(rows))
	for i := range rows {
		cp := rows[i]
		out = append(out, &cp)
	}
	return api.TickDataSeriesResult{Data: out, Complete: true}, nil
}

func (m *simMarketService) SubscribeKlines(ctx context.Context, symbols []string, durationSeconds int, dataLength int, opts ...api.KlineSubOption) (api.KlineSub, error) {
	if len(symbols) == 0 {
		return nil, api.NewError(api.ErrInvalidSymbol, "empty symbols", nil)
	}
	if durationSeconds <= 0 {
		return nil, api.NewError(api.ErrInvalidDuration, "invalid duration", nil)
	}
	if dataLength <= 0 {
		dataLength = 200
	}
	if dataLength > api.MarketMaxDataLength {
		dataLength = api.MarketMaxDataLength
	}
	op := api.ResolveKlineSubOptions(opts...)
	durN := int64(durationSeconds) * int64(time.Second)
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started {
		return nil, api.NewError(api.ErrDisconnected, "sim market service not started", nil)
	}

	// Lazy-load data for any symbol not yet in the kernel.
	if m.downloader != nil {
		if ll, ok := m.provider.(data.LazyLoader); ok {
			cfg := m.rt.Config()
			for _, sym := range symbols {
				if err := ll.EnsureKlineSeries(ctx, sym, durationSeconds, cfg.StartDT, cfg.EndDT, m.downloader); err != nil {
					return nil, fmt.Errorf("lazy load klines %s@%ds: %w", sym, durationSeconds, err)
				}
				if err := m.rt.AddKlineData(ctx, sym, durationSeconds); err != nil {
					return nil, fmt.Errorf("add kline data %s@%ds: %w", sym, durationSeconds, err)
				}
			}
		}
	}

	// Pre-fill ring buffer with historical klines before startDT so the first
	// emitted frame is fully populated (matches Python TqBacktest behavior).
	for _, sym := range symbols {
		m.prefillKlines(ctx, sym, durationSeconds, dataLength)
	}

	chartID := genID("BT_kline")
	sub := &klineSub{chartID: chartID, symbols: append([]string(nil), symbols...), durationN: durN, width: dataLength, opts: op, ch: make(chan api.KlineEvent, 128), done: make(chan struct{}), lastID: -1}
	m.klineSubs[chartID] = sub
	go bindCtx(ctx, func() { _ = sub.Close() })
	return sub, nil
}

func (m *simMarketService) GetKlines(ctx context.Context, req api.KlineGetReq) (api.KlineGetResult, error) {
	_ = ctx
	if len(req.Symbols) == 0 {
		return api.KlineGetResult{}, api.NewError(api.ErrInvalidSymbol, "empty symbols", nil)
	}
	durN := int64(req.DurationSeconds) * int64(time.Second)
	frame := m.buildKlineFrame(req.Symbols[0], durN, req.Count)
	return api.KlineGetResult{Frame1D: frame, Complete: true}, nil
}

func (m *simMarketService) QueryKlinesPage(ctx context.Context, req api.KlineQueryReq) (api.KlineBatch, error) {
	_ = ctx
	if len(req.Symbols) == 0 {
		return api.KlineBatch{}, api.NewError(api.ErrInvalidSymbol, "empty symbols", nil)
	}
	durN := int64(req.DurationSeconds) * int64(time.Second)
	frame := m.buildKlineFrame(req.Symbols[0], durN, req.ViewWidth)
	left, right := int64(-1), int64(-1)
	for _, k := range frame {
		if k == nil {
			continue
		}
		if left < 0 || k.ID < left {
			left = k.ID
		}
		if k.ID > right {
			right = k.ID
		}
	}
	return api.KlineBatch{
		Key:      api.KlineKey{MainSymbol: req.Symbols[0], Duration: time.Duration(durN)},
		Frame1D:  frame,
		Ready:    true,
		MoreData: false,
		LeftID:   left,
		RightID:  right,
	}, nil
}

func (m *simMarketService) NewKlineDownloader(ctx context.Context, req api.KlineDownloadReq) (api.KlineBatchIter, error) {
	_ = ctx
	_ = req
	return nil, api.NewError(api.ErrUnsupportedBacktest, "kline downloader not implemented in sim market", nil)
}

func (m *simMarketService) NewTickDownloader(ctx context.Context, req api.TickDownloadReq) (api.TickBatchIter, error) {
	_ = ctx
	_ = req
	return nil, api.NewError(api.ErrUnsupportedBacktest, "tick downloader not implemented in sim market", nil)
}

func (m *simMarketService) GetKlineDataSeries(ctx context.Context, symbol string, durationSeconds int, startDT time.Time, endDT time.Time) (api.KlineDataSeriesResult, error) {
	rows, err := m.provider.LoadKlineSeries(ctx, symbol, durationSeconds, startDT, endDT)
	if err != nil {
		return api.KlineDataSeriesResult{}, api.NewError(api.ErrUnsupportedBacktest, "kline data unavailable", err)
	}
	out := make([]*api.Kline, 0, len(rows))
	for i := range rows {
		cp := rows[i]
		out = append(out, &cp)
	}
	return api.KlineDataSeriesResult{Data: out, Complete: true}, nil
}

func (m *simMarketService) ConnEvents(ctx context.Context) (<-chan api.ConnEvent, error) {
	ch := make(chan api.ConnEvent, 16)
	m.connSubsMu.Lock()
	m.connSubs[ch] = struct{}{}
	m.connSubsMu.Unlock()
	ch <- api.ConnEvent{State: "ready", At: time.Now()}
	go func() {
		<-ctx.Done()
		m.connSubsMu.Lock()
		if _, ok := m.connSubs[ch]; ok {
			delete(m.connSubs, ch)
			close(ch)
		}
		m.connSubsMu.Unlock()
	}()
	return ch, nil
}

func (m *simMarketService) emitConnEvent(ev api.ConnEvent) {
	m.connSubsMu.Lock()
	defer m.connSubsMu.Unlock()
	for ch := range m.connSubs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (m *simMarketService) buildTickFrame(symbol string, width int) []*api.Tick {
	m.mu.RLock()
	ring := m.ticks[symbol]
	m.mu.RUnlock()
	if ring == nil || width <= 0 {
		return nil
	}
	return ring.Tail(width)
}

// prefillTicks downloads up to `width` ticks before startDT from the live
// downloader and seeds the ring buffer, so the first emitted frame is fully
// populated. This mirrors Python's _gen_serial which uses
// focus_position=view_width to place focus_datetime at the window's right
// edge, loading historical ticks before the backtest start.
//
// Must be called while m.mu is held. Best-effort: errors are silently ignored.
func (m *simMarketService) prefillTicks(ctx context.Context, symbol string, width int) {
	if m.downloader == nil || width <= 0 {
		return
	}
	// Skip if ring buffer already has data (kernel already replayed events).
	if ring := m.ticks[symbol]; ring != nil && ring.Len() > 0 {
		return
	}
	cfg := m.rt.Config()
	startDT := cfg.StartDT
	pos := width
	batch, err := m.downloader.QueryTicksPage(ctx, api.TickQueryReq{
		Symbol:    symbol,
		Begin:     api.KlineBegin{FocusDatetime: &startDT, FocusPosition: &pos},
		ViewWidth: width,
	})
	if err != nil {
		return
	}
	ring := m.ticks[symbol]
	if ring == nil {
		ring = newTickRingBuffer(api.MarketMaxDataLength)
		m.ticks[symbol] = ring
	}
	startNS := startDT.UnixNano()
	for _, t := range batch.Frame {
		if t != nil && t.Datetime < startNS {
			ring.Push(*t)
		}
	}
}

func (m *simMarketService) buildKlineFrame(symbol string, durN int64, width int) []*api.Kline {
	m.mu.RLock()
	ring := m.klines[symbol][durN]
	m.mu.RUnlock()
	if ring == nil || width <= 0 {
		return nil
	}
	return ring.Tail(width)
}

// prefillKlines downloads up to `width` klines before startDT from the live
// downloader and seeds the ring buffer, so the first emitted frame is fully
// populated. This mirrors Python's _gen_serial which uses
// focus_position=view_width to place focus_datetime at the window's right
// edge, loading historical klines before the backtest start.
//
// Must be called while m.mu is held. Best-effort: errors are silently ignored.
func (m *simMarketService) prefillKlines(ctx context.Context, symbol string, durationSeconds int, width int) {
	if m.downloader == nil || width <= 0 {
		return
	}
	durN := int64(durationSeconds) * int64(time.Second)
	// Skip if ring buffer already has data (kernel already replayed events).
	if durMap := m.klines[symbol]; durMap != nil {
		if ring := durMap[durN]; ring != nil && ring.Len() > 0 {
			return
		}
	}
	cfg := m.rt.Config()
	startDT := cfg.StartDT
	pos := width
	batch, err := m.downloader.QueryKlinesPage(ctx, api.KlineQueryReq{
		Symbols:         []string{symbol},
		DurationSeconds: durationSeconds,
		Begin:           api.KlineBegin{FocusDatetime: &startDT, FocusPosition: &pos},
		ViewWidth:       width,
	})
	if err != nil {
		return
	}
	if m.klines[symbol] == nil {
		m.klines[symbol] = map[int64]*klineRingBuffer{}
	}
	ring := m.klines[symbol][durN]
	if ring == nil {
		ring = newKlineRingBuffer(api.MarketMaxDataLength)
		m.klines[symbol][durN] = ring
	}
	startNS := startDT.UnixNano()
	for _, k := range batch.Frame1D {
		if k != nil && k.Datetime < startNS {
			ring.PushOrUpdate(*k)
		}
	}
}

func (m *simMarketService) QueryGraphQL(ctx context.Context, query string, variables map[string]any) (api.GraphQLResult, error) {
	_, _, _ = ctx, query, variables
	return api.GraphQLResult{}, api.NewError(api.ErrUnsupportedBacktest, "graphql query is unsupported in sim market", nil)
}

func (m *simMarketService) QueryQuotes(ctx context.Context, insClass []string, exchangeID []string, productID []string, expired *bool, hasNight *bool) ([]api.InstrumentID, error) {
	_, _, _, _, _, _ = ctx, insClass, exchangeID, productID, expired, hasNight
	return nil, api.NewError(api.ErrUnsupportedBacktest, "query quotes is unsupported in sim market", nil)
}

func (m *simMarketService) QueryContQuotes(ctx context.Context, exchangeID string, productID string, hasNight *bool) ([]api.InstrumentID, error) {
	_, _, _, _ = ctx, exchangeID, productID, hasNight
	return nil, api.NewError(api.ErrUnsupportedBacktest, "query cont quotes is unsupported in sim market", nil)
}

func (m *simMarketService) QueryOptions(ctx context.Context, underlyingSymbol string, optionClass string, exerciseYear int, exerciseMonth int, strikePrice *float64, expired *bool, hasA *bool) ([]api.InstrumentID, error) {
	_, _, _, _, _, _, _, _ = ctx, underlyingSymbol, optionClass, exerciseYear, exerciseMonth, strikePrice, expired, hasA
	return nil, api.NewError(api.ErrUnsupportedBacktest, "query options is unsupported in sim market", nil)
}

func (m *simMarketService) QueryATMOptions(ctx context.Context, underlyingSymbol string, underlyingPrice float64, priceLevel []int, optionClass string, exerciseYear int, exerciseMonth int, hasA *bool) ([]*api.InstrumentID, error) {
	_, _, _, _, _, _, _, _ = ctx, underlyingSymbol, underlyingPrice, priceLevel, optionClass, exerciseYear, exerciseMonth, hasA
	return nil, api.NewError(api.ErrUnsupportedBacktest, "query atm options is unsupported in sim market", nil)
}

func (m *simMarketService) QuerySymbolInfo(ctx context.Context, symbols []string) ([]api.Quote, error) {
	_ = ctx
	out := make([]api.Quote, 0, len(symbols))
	for _, s := range symbols {
		if q, ok := m.Quote(s); ok {
			out = append(out, q)
		}
	}
	if len(out) == 0 {
		return nil, api.NewError(api.ErrUnsupportedBacktest, "symbol info unavailable in sim market", nil)
	}
	return out, nil
}

func (m *simMarketService) QueryAllLevelOptions(ctx context.Context, underlyingSymbol string, underlyingPrice float64, optionClass string, exerciseYear int, exerciseMonth int, hasA *bool) (inMoney []api.InstrumentID, atMoney []api.InstrumentID, outMoney []api.InstrumentID, err error) {
	_, _, _, _, _, _, _ = ctx, underlyingSymbol, underlyingPrice, optionClass, exerciseYear, exerciseMonth, hasA
	return nil, nil, nil, api.NewError(api.ErrUnsupportedBacktest, "query all level options is unsupported", nil)
}

func (m *simMarketService) QueryAllLevelFinanceOptions(ctx context.Context, underlyingSymbol string, underlyingPrice float64, optionClass string, nearbys []int, hasA *bool) (inMoney []api.InstrumentID, atMoney []api.InstrumentID, outMoney []api.InstrumentID, err error) {
	_, _, _, _, _, _ = ctx, underlyingSymbol, underlyingPrice, optionClass, nearbys, hasA
	return nil, nil, nil, api.NewError(api.ErrUnsupportedBacktest, "query finance options is unsupported", nil)
}

func (m *simMarketService) QueryHisContQuotes(ctx context.Context, symbols []string, n int) (api.HisContQuoteResult, error) {
	_, _, _ = ctx, symbols, n
	return api.HisContQuoteResult{}, api.NewError(api.ErrUnsupportedBacktest, "query his cont quotes is unsupported", nil)
}

func (m *simMarketService) QuerySymbolSettlement(ctx context.Context, symbols []string, days int, startDT *time.Time) (api.SettlementResult, error) {
	_, _, _, _ = ctx, symbols, days, startDT
	return api.SettlementResult{}, api.NewError(api.ErrUnsupportedBacktest, "query symbol settlement is unsupported", nil)
}

func (m *simMarketService) QueryEDBData(ctx context.Context, ids []int, n int, align string, fill string) (api.EDBDataResult, error) {
	_, _, _, _, _ = ctx, ids, n, align, fill
	return api.EDBDataResult{}, api.NewError(api.ErrUnsupportedBacktest, "query edb data is unsupported", nil)
}

func (m *simMarketService) QuerySymbolRanking(ctx context.Context, symbol string, rankingType api.RankingType, days int, startDT *time.Time, broker string) (api.SymbolRankingResult, error) {
	_, _, _, _, _, _ = ctx, symbol, rankingType, days, startDT, broker
	return api.SymbolRankingResult{}, api.NewError(api.ErrUnsupportedBacktest, "query symbol ranking is unsupported", nil)
}

func genID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func bindCtx(ctx context.Context, fn func()) {
	select {
	case <-ctx.Done():
		fn()
	}
}

func sendToSub[T any](done <-chan struct{}, out chan<- T, ev T) bool {
	if out == nil {
		return false
	}
	select {
	case <-done:
		return false
	default:
	}
	select {
	case <-done:
		return false
	case out <- ev:
		return true
	}
}

func (s *quoteSub) C() <-chan api.QuoteEvent { return s.ch }

func (s *quoteSub) Close() error {
	s.once.Do(func() {
		close(s.done)
		close(s.ch)
	})
	return nil
}

func (s *quoteSub) enqueue(ev api.QuoteEvent) bool {
	return sendToSub(s.done, s.ch, ev)
}

func (s *tickSub) C() <-chan api.TickEvent { return s.ch }
func (s *tickSub) ChartID() string         { return s.chartID }
func (s *tickSub) IsSerialReady() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.serialReady
}

func (s *tickSub) isClosed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}

func (s *tickSub) emit(tick *api.Tick, frame []*api.Tick, now time.Time) {
	kind := api.EventSnapshot
	s.mu.Lock()
	if s.serialReady {
		if tick.ID > s.lastID {
			kind = api.EventAppend
		} else {
			kind = api.EventUpdate
		}
	}
	s.serialReady = true
	s.lastID = tick.ID
	s.mu.Unlock()
	ev := api.TickEvent{
		Kind:        kind,
		Symbol:      s.symbol,
		Tick:        *tick,
		SerialReady: true,
		ChangedAt:   now,
	}
	if s.opts.EmitFrame {
		ev.Frame = frame
		valid := 0
		for _, t := range frame {
			if t != nil {
				valid++
			}
		}
		ev.ValidCount = valid
		ev.FrameFull = valid == s.width
	}
	_ = sendToSub(s.done, s.ch, ev)
}

func (s *tickSub) Close() error {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.done)
		close(s.ch)
	})
	return nil
}

func (s *klineSub) C() <-chan api.KlineEvent { return s.ch }
func (s *klineSub) ChartID() string          { return s.chartID }
func (s *klineSub) IsSerialReady() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.serialReady
}

func (s *klineSub) isClosed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}

func (s *klineSub) emit(kind api.EventKind, kl *api.Kline, row api.AlignedKlineRow, frame []*api.Kline, now time.Time) {
	s.mu.Lock()
	if !s.serialReady {
		kind = api.EventSnapshot
	}
	s.serialReady = true
	s.lastID = kl.ID
	s.mu.Unlock()
	ev := api.KlineEvent{
		Kind:        kind,
		Key:         api.KlineKey{MainSymbol: s.symbols[0], Duration: time.Duration(s.durationN)},
		Row:         row,
		SerialReady: true,
		ChangedAt:   now,
	}
	if s.opts.EmitFrame {
		ev.Frame1D = frame
		valid := 0
		for _, k := range frame {
			if k != nil {
				valid++
			}
		}
		ev.ValidCount = valid
		ev.FrameFull = valid == s.width
	}
	_ = sendToSub(s.done, s.ch, ev)
}

func (s *klineSub) Close() error {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.done)
		close(s.ch)
	})
	return nil
}

func (m *simMarketService) RuntimeStatusSnapshot() map[string]any {
	if p, ok := any(m.rt).(interface{ RuntimeStatusSnapshot() map[string]any }); ok {
		return p.RuntimeStatusSnapshot()
	}
	return map[string]any{}
}

func (m *simMarketService) SubscribeRuntimeStatus(ctx context.Context) (<-chan map[string]any, error) {
	if p, ok := any(m.rt).(interface {
		SubscribeRuntimeStatus(context.Context) (<-chan map[string]any, error)
	}); ok {
		return p.SubscribeRuntimeStatus(ctx)
	}
	return nil, api.NewError(api.ErrUnsupportedBacktest, "runtime status not available", nil)
}

func normalizeSymbols(symbols []string) []string {
	set := map[string]struct{}{}
	out := make([]string, 0, len(symbols))
	for _, s := range symbols {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := set[s]; ok {
			continue
		}
		set[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
