package domain

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/infra"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/infra/graphqlx"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/store"
)

type startStop interface {
	Start(ctx context.Context) error
	Close() error
}

type marketService struct {
	auth       api.AuthService
	store      *store.MarketStore
	httpClient *http.Client
	wsURL      string

	mu       sync.RWMutex
	started  bool
	closed   bool
	runCtx   context.Context
	cancel   context.CancelFunc
	ws       *infra.WSManager
	upSignal chan struct{}
	recMu    sync.Mutex
	recov    bool
	hadConn  bool
	shadow   *store.MarketStore
	pending  []map[string]any

	nextSubID int64

	quoteSubs map[int64]*quoteSub
	tickSubs  map[string]*tickSub
	klineSubs map[string]*klineSub

	queryMu      sync.Mutex
	queryWaiters map[string]chan struct{}
	queryFlights map[string]*insQueryFlight

	connSubsMu sync.Mutex
	connSubs   map[chan api.ConnEvent]struct{}
}

type quoteSub struct {
	svc      *marketService
	id       int64
	symbols  map[string]struct{}
	ch       chan api.QuoteEvent
	done     chan struct{}
	once     sync.Once
	doneOnce sync.Once
}

type tickSub struct {
	svc       *marketService
	chartID   string
	symbol    string
	width     int
	emitFrame bool
	clientTag string
	ch        chan api.TickEvent
	done      chan struct{}
	reqState  map[string]any

	mu          sync.RWMutex
	doneOnce    sync.Once
	serialReady bool
	lastID      int64
	closed      bool
}

type klineSub struct {
	svc       *marketService
	chartID   string
	symbols   []string
	durationS int
	width     int
	align     api.KlineAlignMode
	emitFrame bool
	clientTag string
	ch        chan api.KlineEvent
	done      chan struct{}
	reqState  map[string]any

	mu          sync.RWMutex
	doneOnce    sync.Once
	serialReady bool
	lastID      int64
	closed      bool
}

type klineDownloader struct {
	svc     *marketService
	req     api.KlineDownloadReq
	begin   api.KlineBegin
	started bool
	done    bool
	err     error
}

type tickDownloader struct {
	svc     *marketService
	req     api.TickDownloadReq
	begin   api.KlineBegin
	started bool
	done    bool
	err     error
}

type insQueryFlight struct {
	done    chan struct{}
	waiters int
	result  map[string]any
	err     error
}

func NewMarketService(auth api.AuthService, st *store.MarketStore, wsURL string, httpClient *http.Client) api.MarketService {
	if st == nil {
		st = store.NewMarketStore()
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &marketService{
		auth:         auth,
		store:        st,
		httpClient:   httpClient,
		wsURL:        strings.TrimSpace(wsURL),
		upSignal:     make(chan struct{}, 1),
		quoteSubs:    map[int64]*quoteSub{},
		tickSubs:     map[string]*tickSub{},
		klineSubs:    map[string]*klineSub{},
		queryWaiters: map[string]chan struct{}{},
		queryFlights: map[string]*insQueryFlight{},
		connSubs:     map[chan api.ConnEvent]struct{}{},
	}
}

func (m *marketService) Start(ctx context.Context) error {
	if err := m.ensureStarted(ctx); err != nil {
		return err
	}
	return m.waitConnected(ctx)
}

func (m *marketService) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	cancel := m.cancel
	quoteSubs := make([]*quoteSub, 0, len(m.quoteSubs))
	for _, sub := range m.quoteSubs {
		quoteSubs = append(quoteSubs, sub)
	}
	tickSubs := make([]*tickSub, 0, len(m.tickSubs))
	for _, sub := range m.tickSubs {
		sub.mu.Lock()
		sub.closed = true
		sub.mu.Unlock()
		tickSubs = append(tickSubs, sub)
	}
	klineSubs := make([]*klineSub, 0, len(m.klineSubs))
	for _, sub := range m.klineSubs {
		sub.mu.Lock()
		sub.closed = true
		sub.mu.Unlock()
		klineSubs = append(klineSubs, sub)
	}
	m.quoteSubs = map[int64]*quoteSub{}
	m.tickSubs = map[string]*tickSub{}
	m.klineSubs = map[string]*klineSub{}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, sub := range quoteSubs {
		sub.signalDone()
	}
	for _, sub := range tickSubs {
		sub.signalDone()
	}
	for _, sub := range klineSubs {
		sub.signalDone()
	}
	if m.ws != nil {
		_ = m.ws.Close()
	}
	m.connSubsMu.Lock()
	for ch := range m.connSubs {
		close(ch)
	}
	m.connSubs = map[chan api.ConnEvent]struct{}{}
	m.connSubsMu.Unlock()
	return nil
}

func (m *marketService) ensureStarted(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return api.NewError(api.ErrDisconnected, "market service closed", nil)
	}
	if m.started {
		return nil
	}
	if m.wsURL == "" {
		mdURL, err := m.auth.ResolveMDURL(ctx, false, false)
		if err == nil && strings.TrimSpace(mdURL) != "" {
			m.wsURL = mdURL
		}
		if m.wsURL == "" {
			m.wsURL = "wss://api.shinnytech.com/t/nfmd/front/mobile"
		}
	}
	m.runCtx, m.cancel = context.WithCancel(context.Background())
	m.ws = infra.NewWSManager(
		m.wsURL,
		func() http.Header { return m.auth.Header() },
		infra.WSCallbacks{
			OnEvent: func(ev infra.WSEvent) {
				switch ev.State {
				case infra.WSStateConnecting, infra.WSStateReconnecting:
					if ev.State == infra.WSStateReconnecting {
						m.enterRecovery()
					}
					m.emitConnEvent(api.ConnEvent{State: "connecting", Err: ev.Err, At: ev.At})
				case infra.WSStateConnected:
					m.markConnected()
					m.emitConnEvent(api.ConnEvent{State: "ready", At: ev.At})
				case infra.WSStateDisconnected:
					m.emitConnEvent(api.ConnEvent{State: "disconnected", Err: ev.Err, At: ev.At})
				}
			},
			OnConnect: func(ctx context.Context) error {
				m.replayAll()
				return m.sendPack(map[string]any{"aid": "peek_message"})
			},
			OnMessage: func(ctx context.Context, msg map[string]any) error {
				_ = ctx
				if m.isRecovering() {
					m.handleMessageRecover(msg)
				} else {
					m.handleMessage(msg)
				}
				return m.sendPack(map[string]any{"aid": "peek_message"})
			},
		},
		nil,
	)
	m.started = true
	if err := m.ws.Start(m.runCtx); err != nil {
		m.started = false
		return api.NewError(api.ErrDisconnected, "start market ws manager failed", err)
	}
	return nil
}

func (m *marketService) requireStarted() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return api.NewError(api.ErrDisconnected, "market service closed", nil)
	}
	if !m.started {
		return api.NewError(api.ErrDisconnected, "market service not started, call Start first", nil)
	}
	return nil
}

func (m *marketService) notifyUpdateSignal() {
	select {
	case m.upSignal <- struct{}{}:
	default:
	}
}

func (m *marketService) waitConnected(ctx context.Context) error {
	if m.ws == nil {
		return api.NewError(api.ErrDisconnected, "market ws manager not started", nil)
	}
	if err := m.ws.WaitConnected(ctx); err != nil {
		return api.NewError(api.ErrTimeout, "wait market ws ready timeout", err)
	}
	return nil
}

func (m *marketService) sendPack(pack map[string]any) error {
	if m.ws == nil {
		return api.NewError(api.ErrDisconnected, "market ws disconnected", nil)
	}
	if err := m.ws.Send(pack); err != nil {
		return api.NewError(api.ErrDisconnected, "market ws write failed", err)
	}
	return nil
}

func (m *marketService) replayAll() {
	m.mu.RLock()
	quoteSymbols := map[string]struct{}{}
	for _, sub := range m.quoteSubs {
		for s := range sub.symbols {
			quoteSymbols[s] = struct{}{}
		}
	}
	ticks := make([]*tickSub, 0, len(m.tickSubs))
	for _, sub := range m.tickSubs {
		ticks = append(ticks, sub)
	}
	klines := make([]*klineSub, 0, len(m.klineSubs))
	for _, sub := range m.klineSubs {
		klines = append(klines, sub)
	}
	m.mu.RUnlock()

	if len(quoteSymbols) > 0 {
		list := mapKeys(quoteSymbols)
		sort.Strings(list)
		_ = m.sendPack(map[string]any{"aid": "subscribe_quote", "ins_list": strings.Join(list, ",")})
	}
	for _, sub := range ticks {
		if sub.isClosed() {
			continue
		}
		_ = m.sendPack(sub.requestPack())
	}
	for _, sub := range klines {
		if sub.isClosed() {
			continue
		}
		_ = m.sendPack(sub.requestPack())
	}
}

func (m *marketService) handleMessage(msg map[string]any) {
	diffs := extractRtnDiffs(msg)
	if len(diffs) == 0 {
		return
	}
	s := m.applyDiffs(m.store, diffs)
	m.notifyUpdateSignal()
	m.dispatchSummary(s)
}

func (m *marketService) handleMessageRecover(msg map[string]any) {
	diffs := extractRtnDiffs(msg)
	if len(diffs) == 0 {
		return
	}
	m.recMu.Lock()
	if !m.recov {
		m.recMu.Unlock()
		m.handleMessage(msg)
		return
	}
	if m.shadow == nil {
		m.shadow = store.NewMarketStore()
	}
	m.pending = append(m.pending, diffs...)
	_ = m.applyDiffs(m.shadow, diffs)
	if !m.recoveryReadyLocked() {
		m.recMu.Unlock()
		return
	}
	pending := m.pending
	m.pending = nil
	m.shadow = nil
	m.recov = false
	m.recMu.Unlock()

	s := m.applyDiffs(m.store, pending)
	m.notifyUpdateSignal()
	m.dispatchSummary(s)
}

type marketApplySummary struct {
	changedQuotes map[string]struct{}
	queryIDs      map[string]struct{}
	chartTouched  bool
	serialTouched bool
}

func (m *marketService) applyDiffs(st *store.MarketStore, diffs []map[string]any) marketApplySummary {
	out := marketApplySummary{
		changedQuotes: map[string]struct{}{},
		queryIDs:      map[string]struct{}{},
	}
	for _, diff := range diffs {
		if qd, ok := diff["quotes"].(map[string]any); ok {
			for k := range qd {
				out.changedQuotes[k] = struct{}{}
			}
		}
		if _, ok := diff["charts"]; ok {
			out.chartTouched = true
		}
		if _, ok := diff["ticks"]; ok {
			out.serialTouched = true
		}
		if _, ok := diff["klines"]; ok {
			out.serialTouched = true
		}
		if syms, ok := diff["symbols"].(map[string]any); ok {
			for qid := range syms {
				out.queryIDs[qid] = struct{}{}
			}
		}
		_ = st.ApplyDiff(diff)
	}
	return out
}

func (m *marketService) dispatchSummary(s marketApplySummary) {
	if len(s.changedQuotes) > 0 {
		m.dispatchQuoteEvents(s.changedQuotes)
	}
	if s.chartTouched || s.serialTouched {
		m.dispatchTickEvents()
		m.dispatchKlineEvents()
	}
	if len(s.queryIDs) > 0 {
		m.notifyQueryWaiters(s.queryIDs)
	}
}

func extractRtnDiffs(msg map[string]any) []map[string]any {
	aid, _ := msg["aid"].(string)
	if aid != "rtn_data" {
		return nil
	}
	items, ok := msg["data"].([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		diff, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, diff)
	}
	return out
}

func (m *marketService) markConnected() {
	m.recMu.Lock()
	m.hadConn = true
	m.recMu.Unlock()
}

func (m *marketService) enterRecovery() {
	m.recMu.Lock()
	defer m.recMu.Unlock()
	if !m.hadConn {
		return
	}
	m.recov = true
	m.shadow = store.NewMarketStore()
	m.pending = nil
}

func (m *marketService) isRecovering() bool {
	m.recMu.Lock()
	defer m.recMu.Unlock()
	return m.recov
}

func (m *marketService) recoveryReadyLocked() bool {
	if m.shadow == nil {
		return false
	}
	if m.shadow.MDHisMoreData() {
		return false
	}
	expectedQuotes := m.expectedQuoteInsList()
	if expectedQuotes != "" && m.shadow.InsList() != expectedQuotes {
		return false
	}
	ticks, klines := m.activeCharts()
	for _, sub := range ticks {
		st, ok := m.shadow.Chart(sub.chartID)
		if !ok || !st.Ready || (st.LeftID < 0 && st.RightID < 0) {
			return false
		}
		if !stateSubset(sub.reqState, st.State) {
			return false
		}
		if !m.shadow.HasTickData(sub.symbol) {
			return false
		}
	}
	for _, sub := range klines {
		st, ok := m.shadow.Chart(sub.chartID)
		if !ok || !st.Ready || (st.LeftID < 0 && st.RightID < 0) {
			return false
		}
		if !stateSubset(sub.reqState, st.State) {
			return false
		}
		dur := int64(sub.durationS) * int64(time.Second)
		for _, sym := range sub.symbols {
			if !m.shadow.HasKlineData(sym, dur) {
				return false
			}
		}
	}
	return true
}

func (m *marketService) expectedQuoteInsList() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	qs := map[string]struct{}{}
	for _, sub := range m.quoteSubs {
		for sym := range sub.symbols {
			qs[sym] = struct{}{}
		}
	}
	if len(qs) == 0 {
		return ""
	}
	list := mapKeys(qs)
	sort.Strings(list)
	return strings.Join(list, ",")
}

func (m *marketService) activeCharts() ([]*tickSub, []*klineSub) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ticks := make([]*tickSub, 0, len(m.tickSubs))
	for _, sub := range m.tickSubs {
		if sub.isClosed() {
			continue
		}
		ticks = append(ticks, sub)
	}
	klines := make([]*klineSub, 0, len(m.klineSubs))
	for _, sub := range m.klineSubs {
		if sub.isClosed() {
			continue
		}
		klines = append(klines, sub)
	}
	return ticks, klines
}

func (m *marketService) dispatchQuoteEvents(changed map[string]struct{}) {
	m.mu.RLock()
	subs := make([]*quoteSub, 0, len(m.quoteSubs))
	for _, s := range m.quoteSubs {
		subs = append(subs, s)
	}
	m.mu.RUnlock()
	for _, sub := range subs {
		for symbol := range sub.symbols {
			if _, ok := changed[symbol]; !ok {
				continue
			}
			q, ok := m.store.Quote(symbol)
			if !ok {
				continue
			}
			ev := api.QuoteEvent{Quote: q, ChangedAt: time.Now()}
			sub.enqueue(ev)
		}
	}
}

func (m *marketService) dispatchTickEvents() {
	m.mu.RLock()
	subs := make([]*tickSub, 0, len(m.tickSubs))
	for _, s := range m.tickSubs {
		subs = append(subs, s)
	}
	m.mu.RUnlock()
	for _, sub := range subs {
		if sub.isClosed() {
			continue
		}
		st, ok := m.store.Chart(sub.chartID)
		if !ok {
			continue
		}
		ready := st.Ready && (st.LeftID >= 0 || st.RightID >= 0)
		if !ready {
			continue
		}
		frame := m.store.TickFrameByRange(sub.symbol, st.LeftID, st.RightID, sub.width)
		if frame == nil {
			frame = make([]*api.Tick, sub.width)
		}
		valid := 0
		var last *api.Tick
		for i := range frame {
			if frame[i] != nil {
				valid++
				last = frame[i]
			}
		}
		kind := api.EventSnapshot
		lastID := int64(-1)
		if last != nil {
			lastID = last.ID
		}
		sub.mu.Lock()
		if sub.serialReady {
			if lastID > sub.lastID {
				kind = api.EventAppend
			} else {
				kind = api.EventUpdate
			}
		}
		sub.serialReady = true
		sub.lastID = lastID
		sub.mu.Unlock()

		ev := api.TickEvent{
			Kind:        kind,
			Symbol:      sub.symbol,
			SerialReady: true,
			FrameFull:   valid == sub.width,
			ValidCount:  valid,
			ChangedAt:   time.Now(),
		}
		if last != nil {
			ev.Tick = *last
		}
		if sub.emitFrame {
			ev.Frame = frame
		}
		sub.enqueue(ev)
	}
}

func (m *marketService) dispatchKlineEvents() {
	m.mu.RLock()
	subs := make([]*klineSub, 0, len(m.klineSubs))
	for _, s := range m.klineSubs {
		subs = append(subs, s)
	}
	m.mu.RUnlock()
	for _, sub := range subs {
		if sub.isClosed() {
			continue
		}
		st, ok := m.store.Chart(sub.chartID)
		if !ok {
			continue
		}
		ready := st.Ready && (st.LeftID >= 0 || st.RightID >= 0)
		if !ready {
			continue
		}
		durN := int64(sub.durationS) * int64(time.Second)
		var frame1D []*api.Kline
		var frame2D [][]*api.Kline
		valid := 0
		lastID := int64(-1)
		row := api.AlignedKlineRow{}
		if len(sub.symbols) == 1 {
			frame1D = m.store.KlineFrameByRange(sub.symbols[0], durN, st.LeftID, st.RightID, sub.width)
			if frame1D == nil {
				frame1D = make([]*api.Kline, sub.width)
			}
			for _, it := range frame1D {
				if it == nil {
					continue
				}
				valid++
				lastID = it.ID
				row = api.AlignedKlineRow{MainID: it.ID, Datetime: time.Unix(0, it.Datetime), Main: *it, Others: map[string]*api.Kline{}, Binding: map[string]int64{}}
			}
		} else {
			frame2D, valid, row, lastID = m.buildAlignedFrame(sub, durN, st.LeftID, st.RightID)
		}

		kind := api.EventSnapshot
		sub.mu.Lock()
		if sub.serialReady {
			if lastID > sub.lastID {
				kind = api.EventAppend
			} else {
				kind = api.EventUpdate
			}
		}
		sub.serialReady = true
		sub.lastID = lastID
		sub.mu.Unlock()

		ev := api.KlineEvent{
			Kind:        kind,
			Key:         api.KlineKey{MainSymbol: sub.symbols[0], Duration: time.Duration(durN)},
			Row:         row,
			SerialReady: true,
			FrameFull:   valid == sub.width,
			ValidCount:  valid,
			ChangedAt:   time.Now(),
		}
		if sub.emitFrame {
			ev.Frame1D = frame1D
			ev.Frame2D = frame2D
		}
		sub.enqueue(ev)
	}
}

func (m *marketService) buildAlignedFrame(sub *klineSub, durN int64, leftID int64, rightID int64) ([][]*api.Kline, int, api.AlignedKlineRow, int64) {
	frame := make([][]*api.Kline, sub.width)
	rows := make([][]*api.Kline, 0, sub.width)
	valid := 0
	lastID := int64(-1)
	lastRow := api.AlignedKlineRow{Others: map[string]*api.Kline{}, Binding: map[string]int64{}}
	latestCaptured := false
	main := sub.symbols[0]
	for id := rightID; id >= leftID && len(rows) < sub.width; id-- {
		mainK, ok := m.store.KlineByID(main, durN, id)
		if !ok {
			continue
		}
		row := make([]*api.Kline, len(sub.symbols))
		mainCopy := mainK
		row[0] = &mainCopy
		others := map[string]*api.Kline{}
		binding := map[string]int64{}
		missing := false
		for i := 1; i < len(sub.symbols); i++ {
			sym := sub.symbols[i]
			subID, ok := m.store.ResolveBinding(main, durN, sym, id)
			if !ok || subID < 0 {
				if sub.align == api.AlignStrictByBinding {
					missing = true
					break
				}
				row[i] = nil
				continue
			}
			sk, ok := m.store.KlineByID(sym, durN, subID)
			if !ok {
				if sub.align == api.AlignStrictByBinding {
					missing = true
					break
				}
				row[i] = nil
				continue
			}
			skCopy := sk
			row[i] = &skCopy
			others[sym] = &skCopy
			binding[sym] = subID
		}
		if missing {
			continue
		}
		rows = append(rows, row)
		valid++
		if !latestCaptured {
			// Iterate from right->left, so the first accepted row is the newest bar.
			lastID = id
			lastRow = api.AlignedKlineRow{MainID: id, Datetime: time.Unix(0, mainK.Datetime), Main: mainK, Others: others, Binding: binding}
			latestCaptured = true
		}
	}
	reverseRows(rows)
	offset := sub.width - len(rows)
	for i := 0; i < len(rows); i++ {
		frame[offset+i] = rows[i]
	}
	return frame, valid, lastRow, lastID
}

func (m *marketService) notifyQueryWaiters(ids map[string]struct{}) {
	m.queryMu.Lock()
	defer m.queryMu.Unlock()
	for qid := range ids {
		ch, ok := m.queryWaiters[qid]
		if !ok {
			continue
		}
		entry, ok := m.store.QuerySymbol(qid)
		if !ok || len(entry) == 0 {
			continue
		}
		if _, ok := entry["result"]; !ok {
			if _, ok := entry["error"]; !ok {
				if _, ok := entry["errors"]; !ok {
					continue
				}
			}
		}
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (m *marketService) ConnEvents(ctx context.Context) (<-chan api.ConnEvent, error) {
	if err := m.requireStarted(); err != nil {
		return nil, err
	}
	ch := make(chan api.ConnEvent, 16)
	m.connSubsMu.Lock()
	m.connSubs[ch] = struct{}{}
	m.connSubsMu.Unlock()
	go func() {
		<-ctx.Done()
		m.connSubsMu.Lock()
		delete(m.connSubs, ch)
		close(ch)
		m.connSubsMu.Unlock()
	}()
	return ch, nil
}

func (m *marketService) emitConnEvent(ev api.ConnEvent) {
	m.connSubsMu.Lock()
	defer m.connSubsMu.Unlock()
	for ch := range m.connSubs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (m *marketService) SubscribeQuotes(ctx context.Context, symbols ...string) (api.QuoteSub, error) {
	if err := m.requireStarted(); err != nil {
		return nil, err
	}
	if len(symbols) == 0 {
		return nil, api.NewError(api.ErrInvalidSymbol, "empty symbols", nil)
	}
	sm := map[string]struct{}{}
	list := make([]string, 0, len(symbols))
	for _, s := range symbols {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, api.NewError(api.ErrInvalidSymbol, "empty symbol", nil)
		}
		sm[s] = struct{}{}
		list = append(list, s)
	}
	if err := m.auth.EnsureMDGrants(list); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.nextSubID++
	sub := &quoteSub{
		svc:     m,
		id:      m.nextSubID,
		symbols: sm,
		ch:      make(chan api.QuoteEvent, 64),
		done:    make(chan struct{}),
	}
	m.quoteSubs[sub.id] = sub
	m.mu.Unlock()
	m.resubscribeQuotes()
	for sym := range sm {
		if q, ok := m.store.Quote(sym); ok {
			sub.enqueue(api.QuoteEvent{Quote: q, ChangedAt: time.Now()})
		}
	}
	return sub, nil
}

func (m *marketService) resubscribeQuotes() {
	m.mu.RLock()
	ss := map[string]struct{}{}
	for _, sub := range m.quoteSubs {
		for sym := range sub.symbols {
			ss[sym] = struct{}{}
		}
	}
	m.mu.RUnlock()
	list := mapKeys(ss)
	sort.Strings(list)
	_ = m.sendPack(map[string]any{"aid": "subscribe_quote", "ins_list": strings.Join(list, ",")})
}

func (s *quoteSub) C() <-chan api.QuoteEvent { return s.ch }

func (s *quoteSub) Close() error {
	s.once.Do(func() {
		s.svc.mu.Lock()
		delete(s.svc.quoteSubs, s.id)
		s.svc.mu.Unlock()
		s.svc.resubscribeQuotes()
		s.signalDone()
	})
	return nil
}

func (s *quoteSub) signalDone() {
	s.doneOnce.Do(func() {
		if s.done != nil {
			close(s.done)
		}
		if s.ch != nil {
			close(s.ch)
		}
	})
}

func (s *quoteSub) enqueue(ev api.QuoteEvent) bool {
	return sendToSub(s.done, s.ch, ev)
}

func sendToSub[T any](done <-chan struct{}, out chan<- T, ev T) bool {
	if out == nil {
		return false
	}
	if done == nil {
		out <- ev
		return true
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

func (m *marketService) SubscribeTicks(ctx context.Context, symbol string, dataLength int, opts ...api.TickSubOption) (api.TickSub, error) {
	if err := m.requireStarted(); err != nil {
		return nil, err
	}
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return nil, api.NewError(api.ErrInvalidSymbol, "empty symbol", nil)
	}
	if err := m.auth.EnsureMDGrants([]string{symbol}); err != nil {
		return nil, err
	}
	if dataLength <= 0 {
		dataLength = 200
	}
	if dataLength > api.MarketMaxDataLength {
		dataLength = api.MarketMaxDataLength
	}
	op := api.ResolveTickSubOptions(opts...)
	chartID := genID("GO_tick")
	sub := &tickSub{
		svc:       m,
		chartID:   chartID,
		symbol:    symbol,
		width:     dataLength,
		emitFrame: op.EmitFrame,
		clientTag: op.ClientTag,
		ch:        make(chan api.TickEvent, 64),
		done:      make(chan struct{}),
		reqState: map[string]any{
			"ins_list":   symbol,
			"duration":   int64(0),
			"view_width": dataLength,
		},
		lastID: -1,
	}
	m.mu.Lock()
	m.tickSubs[chartID] = sub
	m.mu.Unlock()
	_ = m.sendPack(sub.requestPack())
	return sub, nil
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

func (s *tickSub) signalDone() {
	s.doneOnce.Do(func() {
		if s.done != nil {
			close(s.done)
		}
		if s.ch != nil {
			close(s.ch)
		}
	})
}

func (s *tickSub) enqueue(ev api.TickEvent) bool {
	return sendToSub(s.done, s.ch, ev)
}

func (s *tickSub) requestPack() map[string]any {
	return map[string]any{
		"aid":        "set_chart",
		"chart_id":   s.chartID,
		"ins_list":   s.symbol,
		"duration":   int64(0),
		"view_width": s.width,
	}
}

func (s *tickSub) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.svc.mu.Lock()
	delete(s.svc.tickSubs, s.chartID)
	s.svc.mu.Unlock()
	_ = s.svc.sendPack(map[string]any{
		"aid":        "set_chart",
		"chart_id":   s.chartID,
		"ins_list":   "",
		"duration":   int64(0),
		"view_width": s.width,
	})
	s.signalDone()
	return nil
}

func (m *marketService) Quote(symbol string) (api.Quote, bool) {
	return m.store.Quote(symbol)
}

func (m *marketService) TickFrame(symbol string) ([]*api.Tick, bool) {
	m.mu.RLock()
	width := 0
	for _, sub := range m.tickSubs {
		if sub.symbol == symbol && !sub.isClosed() {
			width = sub.width
			break
		}
	}
	m.mu.RUnlock()
	if width == 0 {
		return nil, false
	}
	return m.store.TickFrame(symbol, width), true
}

func (m *marketService) SubscribeKlines(ctx context.Context, symbols []string, durationSeconds int, dataLength int, opts ...api.KlineSubOption) (api.KlineSub, error) {
	if err := m.requireStarted(); err != nil {
		return nil, err
	}
	if len(symbols) == 0 {
		return nil, api.NewError(api.ErrInvalidSymbol, "empty symbols", nil)
	}
	for _, s := range symbols {
		if strings.TrimSpace(s) == "" {
			return nil, api.NewError(api.ErrInvalidSymbol, "empty symbol", nil)
		}
	}
	if err := m.auth.EnsureMDGrants(symbols); err != nil {
		return nil, err
	}
	if durationSeconds <= 0 || (durationSeconds > 86400 && durationSeconds%86400 != 0) {
		return nil, api.NewError(api.ErrInvalidDuration, "invalid duration_seconds", nil)
	}
	if dataLength <= 0 {
		dataLength = 200
	}
	if dataLength > api.MarketMaxDataLength {
		dataLength = api.MarketMaxDataLength
	}
	op := api.ResolveKlineSubOptions(opts...)
	if op.AlignMode != api.AlignStrictByBinding && op.AlignMode != api.AlignMainWithNA {
		return nil, api.NewError(api.ErrInvalidAlignMode, "invalid align mode", nil)
	}
	chartID := genID("GO_kline")
	insList := strings.Join(symbols, ",")
	durN := int64(durationSeconds) * int64(time.Second)
	sub := &klineSub{
		svc:       m,
		chartID:   chartID,
		symbols:   append([]string(nil), symbols...),
		durationS: durationSeconds,
		width:     dataLength,
		align:     op.AlignMode,
		emitFrame: op.EmitFrame,
		clientTag: op.ClientTag,
		ch:        make(chan api.KlineEvent, 64),
		done:      make(chan struct{}),
		reqState: map[string]any{
			"ins_list":   insList,
			"duration":   durN,
			"view_width": dataLength,
		},
		lastID: -1,
	}
	m.mu.Lock()
	m.klineSubs[chartID] = sub
	m.mu.Unlock()
	_ = m.sendPack(sub.requestPack())
	return sub, nil
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

func (s *klineSub) signalDone() {
	s.doneOnce.Do(func() {
		if s.done != nil {
			close(s.done)
		}
		if s.ch != nil {
			close(s.ch)
		}
	})
}

func (s *klineSub) enqueue(ev api.KlineEvent) bool {
	return sendToSub(s.done, s.ch, ev)
}

func (s *klineSub) requestPack() map[string]any {
	return map[string]any{
		"aid":        "set_chart",
		"chart_id":   s.chartID,
		"ins_list":   strings.Join(s.symbols, ","),
		"duration":   int64(s.durationS) * int64(time.Second),
		"view_width": s.width,
	}
}

func (s *klineSub) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.svc.mu.Lock()
	delete(s.svc.klineSubs, s.chartID)
	s.svc.mu.Unlock()
	_ = s.svc.sendPack(map[string]any{
		"aid":        "set_chart",
		"chart_id":   s.chartID,
		"ins_list":   "",
		"duration":   int64(s.durationS) * int64(time.Second),
		"view_width": s.width,
	})
	s.signalDone()
	return nil
}

func (m *marketService) QueryTicksPage(ctx context.Context, req api.TickQueryReq) (api.TickBatch, error) {
	if err := m.requireStarted(); err != nil {
		return api.TickBatch{}, err
	}
	if strings.TrimSpace(req.Symbol) == "" {
		return api.TickBatch{}, api.NewError(api.ErrInvalidSymbol, "empty symbol", nil)
	}
	if err := m.auth.EnsureMDGrants([]string{req.Symbol}); err != nil {
		return api.TickBatch{}, err
	}
	if err := validateBegin(req.Begin); err != nil {
		return api.TickBatch{}, err
	}
	view := req.ViewWidth
	if view <= 0 {
		return api.TickBatch{}, api.NewError(api.ErrInvalidViewWidth, "invalid view width", nil)
	}
	if view > api.MarketMaxViewWidth {
		view = api.MarketMaxViewWidth
	}
	chartID := genID("GO_tickq")
	pack := map[string]any{
		"aid":        "set_chart",
		"chart_id":   chartID,
		"ins_list":   req.Symbol,
		"duration":   int64(0),
		"view_width": view,
	}
	stateReq := map[string]any{"ins_list": req.Symbol, "duration": int64(0), "view_width": view}
	if req.Begin.FocusDatetime != nil {
		pack["focus_datetime"] = req.Begin.FocusDatetime.UnixNano()
		pack["focus_position"] = *req.Begin.FocusPosition
		stateReq["focus_datetime"] = req.Begin.FocusDatetime.UnixNano()
		stateReq["focus_position"] = *req.Begin.FocusPosition
	}
	if req.Begin.LeftKlineID != nil {
		pack["left_kline_id"] = *req.Begin.LeftKlineID
		stateReq["left_kline_id"] = *req.Begin.LeftKlineID
	}
	if err := m.sendPack(pack); err != nil {
		return api.TickBatch{}, err
	}
	defer func() {
		_ = m.sendPack(map[string]any{"aid": "set_chart", "chart_id": chartID, "ins_list": "", "duration": int64(0), "view_width": view})
	}()
	st, err := m.waitChartReady(ctx, chartID, stateReq)
	if err != nil {
		return api.TickBatch{}, err
	}
	frame := m.store.TickFrameByRange(req.Symbol, st.LeftID, st.RightID, view)
	next := nextRight(st.RightID)
	return api.TickBatch{
		Frame:           frame,
		Ready:           st.Ready,
		MoreData:        st.MoreData || m.store.MDHisMoreData(),
		LeftID:          st.LeftID,
		RightID:         st.RightID,
		NextLeftKlineID: next,
		ChartID:         chartID,
	}, nil
}

func (m *marketService) GetTicks(ctx context.Context, req api.TickGetReq) (api.TickGetResult, error) {
	if strings.TrimSpace(req.Symbol) == "" {
		return api.TickGetResult{}, api.NewError(api.ErrInvalidSymbol, "empty symbol", nil)
	}
	if req.ViewWidth <= 0 {
		req.ViewWidth = 10000
	}
	if req.ViewWidth > api.MarketMaxViewWidth {
		req.ViewWidth = api.MarketMaxViewWidth
	}
	if req.Begin.FocusDatetime == nil && req.Begin.LeftKlineID == nil && req.Start != nil {
		pos := 0
		start := *req.Start
		req.Begin.FocusDatetime = &start
		req.Begin.FocusPosition = &pos
	}
	if err := validateBegin(req.Begin); err != nil {
		return api.TickGetResult{}, err
	}

	var data []*api.Tick
	var lastFrame []*api.Tick
	begin := req.Begin
	for {
		batch, err := m.QueryTicksPage(ctx, api.TickQueryReq{Symbol: req.Symbol, Begin: begin, ViewWidth: req.ViewWidth})
		if err != nil {
			if ctx.Err() != nil {
				return api.TickGetResult{Complete: false, Reason: "timeout", Frame: lastFrame}, api.NewError(api.ErrTimeout, "get ticks timeout", err)
			}
			return api.TickGetResult{}, err
		}
		lastFrame = batch.Frame
		reachedEnd := false
		for _, t := range batch.Frame {
			if t == nil {
				continue
			}
			if req.Start != nil && t.Datetime < req.Start.UnixNano() {
				continue
			}
			if req.End != nil && t.Datetime > req.End.UnixNano() {
				reachedEnd = true
				break
			}
			data = append(data, t)
		}
		if reachedEnd {
			break
		}
		if req.Count > 0 && len(data) >= req.Count {
			break
		}
		if batch.NextLeftKlineID == nil {
			break
		}
		begin = api.KlineBegin{LeftKlineID: batch.NextLeftKlineID}
	}

	if req.Count > 0 && len(data) > req.Count {
		data = data[:req.Count]
	}

	complete := req.Count <= 0 || len(data) >= req.Count
	reason := "enough_data"
	if !complete {
		reason = "no_more_data"
	}
	return api.TickGetResult{Data: data, Frame: lastFrame, Complete: complete, Reason: reason}, nil
}

func (m *marketService) GetTickDataSeries(ctx context.Context, symbol string, startDT time.Time, endDT time.Time) (api.TickDataSeriesResult, error) {
	if strings.TrimSpace(symbol) == "" {
		return api.TickDataSeriesResult{}, api.NewError(api.ErrInvalidSymbol, "empty symbol", nil)
	}
	if startDT.IsZero() || endDT.IsZero() || !startDT.Before(endDT) {
		return api.TickDataSeriesResult{}, api.NewError(api.ErrInvalidTimeRange, "invalid time range", nil)
	}
	const viewWidth = 10000

	// Forward pagination: start from startDT, focus_position=0 places the
	// focus_datetime at the left edge so we receive ticks *after* startDT.
	pos := 0
	begin := api.KlineBegin{FocusDatetime: &startDT, FocusPosition: &pos}

	endNS := endDT.UnixNano()

	var data []*api.Tick
	for {
		batch, err := m.QueryTicksPage(ctx, api.TickQueryReq{
			Symbol:    symbol,
			Begin:     begin,
			ViewWidth: viewWidth,
		})
		if err != nil {
			if ctx.Err() != nil {
				return api.TickDataSeriesResult{Data: data, Complete: false, Reason: "timeout"}, api.NewError(api.ErrTimeout, "get tick data series timeout", err)
			}
			return api.TickDataSeriesResult{Data: data, Complete: false, Reason: "timeout"}, err
		}

		hasAny := false
		reachedEnd := false
		for _, t := range batch.Frame {
			if t == nil {
				continue
			}
			hasAny = true
			if t.Datetime > endNS {
				reachedEnd = true
				break
			}
			data = append(data, t)
		}
		if reachedEnd {
			break
		}
		if !hasAny {
			break
		}
		if batch.NextLeftKlineID == nil {
			break
		}
		begin = api.KlineBegin{LeftKlineID: batch.NextLeftKlineID}
	}

	complete := len(data) > 0
	reason := "enough_data"
	if !complete {
		reason = "no_more_data"
	}
	return api.TickDataSeriesResult{Data: data, Complete: complete, Reason: reason}, nil
}

func (m *marketService) NewTickDownloader(ctx context.Context, req api.TickDownloadReq) (api.TickBatchIter, error) {
	if strings.TrimSpace(req.Symbol) == "" {
		return nil, api.NewError(api.ErrInvalidSymbol, "empty symbol", nil)
	}
	if req.ViewWidth <= 0 {
		req.ViewWidth = 10000
	}
	if req.ViewWidth > api.MarketMaxViewWidth {
		req.ViewWidth = api.MarketMaxViewWidth
	}
	if err := m.requireStarted(); err != nil {
		return nil, err
	}
	return &tickDownloader{svc: m, req: req}, nil
}

func (it *tickDownloader) Next(ctx context.Context) (api.TickBatch, bool, error) {
	if it.done {
		if it.err != nil {
			return api.TickBatch{}, false, it.err
		}
		return api.TickBatch{}, false, nil
	}
	if !it.started {
		pos := 0
		it.begin = api.KlineBegin{FocusDatetime: &it.req.Start, FocusPosition: &pos}
		it.started = true
	}
	batch, err := it.svc.QueryTicksPage(ctx, api.TickQueryReq{
		Symbol:    it.req.Symbol,
		Begin:     it.begin,
		ViewWidth: it.req.ViewWidth,
	})
	if err != nil {
		it.err = err
		it.done = true
		return api.TickBatch{}, false, err
	}
	if batch.NextLeftKlineID == nil {
		it.done = true
	} else {
		it.begin = api.KlineBegin{LeftKlineID: batch.NextLeftKlineID}
	}

	// Truncate ticks beyond req.End.
	endNS := it.req.End.UnixNano()
	origLen := len(batch.Frame)
	for i := len(batch.Frame) - 1; i >= 0; i-- {
		if batch.Frame[i] != nil && batch.Frame[i].Datetime > endNS {
			batch.Frame = batch.Frame[:i]
		} else if batch.Frame[i] != nil {
			break
		}
	}
	if len(batch.Frame) < origLen {
		it.done = true
	}
	if len(batch.Frame) == 0 {
		it.done = true
		return api.TickBatch{}, false, nil
	}
	return batch, true, nil
}

func (it *tickDownloader) Err() error {
	return it.err
}

func (it *tickDownloader) Close() error {
	it.done = true
	return nil
}

func (m *marketService) QueryKlinesPage(ctx context.Context, req api.KlineQueryReq) (api.KlineBatch, error) {
	if err := m.requireStarted(); err != nil {
		return api.KlineBatch{}, err
	}
	if len(req.Symbols) == 0 {
		return api.KlineBatch{}, api.NewError(api.ErrInvalidSymbol, "empty symbols", nil)
	}
	for _, s := range req.Symbols {
		if strings.TrimSpace(s) == "" {
			return api.KlineBatch{}, api.NewError(api.ErrInvalidSymbol, "empty symbol", nil)
		}
	}
	if err := m.auth.EnsureMDGrants(req.Symbols); err != nil {
		return api.KlineBatch{}, err
	}
	if req.DurationSeconds <= 0 || (req.DurationSeconds > 86400 && req.DurationSeconds%86400 != 0) {
		return api.KlineBatch{}, api.NewError(api.ErrInvalidDuration, "invalid duration_seconds", nil)
	}
	if err := validateBegin(req.Begin); err != nil {
		return api.KlineBatch{}, err
	}
	view := req.ViewWidth
	if view <= 0 {
		return api.KlineBatch{}, api.NewError(api.ErrInvalidViewWidth, "invalid view width", nil)
	}
	if view > api.MarketMaxViewWidth {
		view = api.MarketMaxViewWidth
	}
	if req.AlignMode == "" {
		req.AlignMode = api.AlignStrictByBinding
	}
	if req.AlignMode != api.AlignStrictByBinding && req.AlignMode != api.AlignMainWithNA {
		return api.KlineBatch{}, api.NewError(api.ErrInvalidAlignMode, "invalid align mode", nil)
	}

	chartID := genID("GO_klineq")
	durN := int64(req.DurationSeconds) * int64(time.Second)
	insList := strings.Join(req.Symbols, ",")
	pack := map[string]any{"aid": "set_chart", "chart_id": chartID, "ins_list": insList, "duration": durN, "view_width": view}
	stateReq := map[string]any{"ins_list": insList, "duration": durN, "view_width": view}
	if req.Begin.FocusDatetime != nil {
		pack["focus_datetime"] = req.Begin.FocusDatetime.UnixNano()
		pack["focus_position"] = *req.Begin.FocusPosition
		stateReq["focus_datetime"] = req.Begin.FocusDatetime.UnixNano()
		stateReq["focus_position"] = *req.Begin.FocusPosition
	}
	if req.Begin.LeftKlineID != nil {
		pack["left_kline_id"] = *req.Begin.LeftKlineID
		stateReq["left_kline_id"] = *req.Begin.LeftKlineID
	}
	if err := m.sendPack(pack); err != nil {
		return api.KlineBatch{}, err
	}
	defer func() {
		_ = m.sendPack(map[string]any{"aid": "set_chart", "chart_id": chartID, "ins_list": "", "duration": durN, "view_width": view})
	}()
	st, err := m.waitChartReady(ctx, chartID, stateReq)
	if err != nil {
		return api.KlineBatch{}, err
	}

	batch := api.KlineBatch{
		Key:      api.KlineKey{MainSymbol: req.Symbols[0], Duration: time.Duration(durN)},
		Ready:    st.Ready,
		MoreData: st.MoreData || m.store.MDHisMoreData(),
		LeftID:   st.LeftID,
		RightID:  st.RightID,
		ChartID:  chartID,
	}
	batch.NextLeftKlineID = nextRight(st.RightID)
	if len(req.Symbols) == 1 {
		batch.Frame1D = m.store.KlineFrameByRange(req.Symbols[0], durN, st.LeftID, st.RightID, view)
		for _, k := range batch.Frame1D {
			if k == nil {
				continue
			}
			batch.Rows = append(batch.Rows, api.AlignedKlineRow{MainID: k.ID, Datetime: time.Unix(0, k.Datetime), Main: *k, Others: map[string]*api.Kline{}, Binding: map[string]int64{}})
		}
	} else {
		sub := &klineSub{symbols: req.Symbols, width: view, align: req.AlignMode}
		frame2D, _, _, _ := m.buildAlignedFrame(sub, durN, st.LeftID, st.RightID)
		batch.Frame2D = frame2D
		for _, row := range frame2D {
			if len(row) == 0 || row[0] == nil {
				continue
			}
			ar := api.AlignedKlineRow{MainID: row[0].ID, Datetime: time.Unix(0, row[0].Datetime), Main: *row[0], Others: map[string]*api.Kline{}, Binding: map[string]int64{}}
			for i := 1; i < len(req.Symbols); i++ {
				if row[i] != nil {
					ar.Others[req.Symbols[i]] = row[i]
					ar.Binding[req.Symbols[i]] = row[i].ID
				}
			}
			batch.Rows = append(batch.Rows, ar)
		}
	}
	return batch, nil
}

func (m *marketService) GetKlines(ctx context.Context, req api.KlineGetReq) (api.KlineGetResult, error) {
	if len(req.Symbols) == 0 {
		return api.KlineGetResult{}, api.NewError(api.ErrInvalidSymbol, "empty symbols", nil)
	}
	if req.DurationSeconds <= 0 || (req.DurationSeconds > 86400 && req.DurationSeconds%86400 != 0) {
		return api.KlineGetResult{}, api.NewError(api.ErrInvalidDuration, "invalid duration_seconds", nil)
	}
	if req.AlignMode == "" {
		req.AlignMode = api.AlignStrictByBinding
	}
	if req.ViewWidth <= 0 {
		req.ViewWidth = 3000
	}
	if req.ViewWidth > api.MarketMaxViewWidth {
		req.ViewWidth = api.MarketMaxViewWidth
	}
	if req.Begin.FocusDatetime == nil && req.Begin.LeftKlineID == nil && req.Start != nil {
		st := *req.Start
		pos := 0
		req.Begin.FocusDatetime = &st
		req.Begin.FocusPosition = &pos
	}
	if err := validateBegin(req.Begin); err != nil {
		return api.KlineGetResult{}, err
	}

	var rows []api.AlignedKlineRow
	var data []*api.Kline
	var last2D [][]*api.Kline
	begin := req.Begin

	for {
		batch, err := m.QueryKlinesPage(ctx, api.KlineQueryReq{Symbols: req.Symbols, DurationSeconds: req.DurationSeconds, Begin: begin, ViewWidth: req.ViewWidth, AlignMode: req.AlignMode})
		if err != nil {
			if ctx.Err() != nil {
				return api.KlineGetResult{Complete: false, Reason: "timeout", Frame1D: batch.Frame1D, Frame2D: batch.Frame2D}, api.NewError(api.ErrTimeout, "get klines timeout", err)
			}
			return api.KlineGetResult{}, err
		}
		last2D = batch.Frame2D
		reachedEnd := false
		for _, r := range batch.Rows {
			if req.Start != nil && r.Main.Datetime < req.Start.UnixNano() {
				continue
			}
			if req.End != nil && r.Main.Datetime > req.End.UnixNano() {
				reachedEnd = true
				break
			}
			rows = append(rows, r)
			if len(req.Symbols) == 1 {
				k := r.Main
				data = append(data, &k)
			}
		}
		if reachedEnd {
			break
		}
		if req.Count > 0 && len(rows) >= req.Count {
			break
		}
		if batch.NextLeftKlineID == nil {
			break
		}
		begin = api.KlineBegin{LeftKlineID: batch.NextLeftKlineID}
	}

	if req.Count > 0 && len(rows) > req.Count {
		rows = rows[:req.Count]
		data = data[:req.Count]
	}

	frame1D := data
	if len(req.Symbols) != 1 {
		frame1D = nil
	}

	complete := req.Count <= 0 || len(rows) >= req.Count
	reason := "enough_data"
	if !complete {
		reason = "no_more_data"
	}
	return api.KlineGetResult{Rows: rows, Frame1D: frame1D, Frame2D: last2D, Complete: complete, Reason: reason}, nil
}

func (m *marketService) NewKlineDownloader(ctx context.Context, req api.KlineDownloadReq) (api.KlineBatchIter, error) {
	if len(req.Symbols) == 0 {
		return nil, api.NewError(api.ErrInvalidSymbol, "empty symbols", nil)
	}
	if req.DurationSeconds <= 0 || (req.DurationSeconds > 86400 && req.DurationSeconds%86400 != 0) {
		return nil, api.NewError(api.ErrInvalidDuration, "invalid duration_seconds", nil)
	}
	if req.ViewWidth <= 0 {
		req.ViewWidth = 2000
	}
	if req.ViewWidth > api.MarketMaxViewWidth {
		req.ViewWidth = api.MarketMaxViewWidth
	}
	if req.AlignMode == "" {
		req.AlignMode = api.AlignStrictByBinding
	}
	if err := m.requireStarted(); err != nil {
		return nil, err
	}
	return &klineDownloader{svc: m, req: req}, nil
}

func (it *klineDownloader) Next(ctx context.Context) (api.KlineBatch, bool, error) {
	if it.done {
		if it.err != nil {
			return api.KlineBatch{}, false, it.err
		}
		return api.KlineBatch{}, false, nil
	}
	if !it.started {
		pos := 0
		it.begin = api.KlineBegin{FocusDatetime: &it.req.Start, FocusPosition: &pos}
		it.started = true
	}
	batch, err := it.svc.QueryKlinesPage(ctx, api.KlineQueryReq{
		Symbols:         it.req.Symbols,
		DurationSeconds: it.req.DurationSeconds,
		Begin:           it.begin,
		ViewWidth:       it.req.ViewWidth,
		AlignMode:       it.req.AlignMode,
	})
	if err != nil {
		it.err = err
		it.done = true
		return api.KlineBatch{}, false, err
	}
	if batch.NextLeftKlineID == nil {
		it.done = true
	} else {
		it.begin = api.KlineBegin{LeftKlineID: batch.NextLeftKlineID}
	}

	// Truncate data beyond req.End and mark done.
	endNS := it.req.End.UnixNano()
	origLen1D := len(batch.Frame1D)
	origLen2D := len(batch.Frame2D)
	origLenRows := len(batch.Rows)
	batch.Frame1D = truncFrame1D(batch.Frame1D, endNS)
	batch.Frame2D = truncFrame2D(batch.Frame2D, endNS)
	batch.Rows = truncRows(batch.Rows, endNS)
	if len(batch.Frame1D) < origLen1D || len(batch.Frame2D) < origLen2D || len(batch.Rows) < origLenRows {
		it.done = true
	}
	// If truncation removed everything, treat as no more data.
	if len(batch.Frame1D) == 0 && len(batch.Frame2D) == 0 && len(batch.Rows) == 0 {
		it.done = true
		return api.KlineBatch{}, false, nil
	}
	return batch, true, nil
}

func (it *klineDownloader) Err() error {
	return it.err
}

func (it *klineDownloader) Close() error {
	it.done = true
	return nil
}

func (m *marketService) GetKlineDataSeries(ctx context.Context, symbol string, durationSeconds int, startDT time.Time, endDT time.Time) (api.KlineDataSeriesResult, error) {
	if strings.TrimSpace(symbol) == "" {
		return api.KlineDataSeriesResult{}, api.NewError(api.ErrInvalidSymbol, "empty symbol", nil)
	}
	if durationSeconds <= 0 || (durationSeconds > 86400 && durationSeconds%86400 != 0) {
		return api.KlineDataSeriesResult{}, api.NewError(api.ErrInvalidDuration, "invalid duration_seconds", nil)
	}
	if startDT.IsZero() || endDT.IsZero() || !startDT.Before(endDT) {
		return api.KlineDataSeriesResult{}, api.NewError(api.ErrInvalidTimeRange, "invalid time range", nil)
	}
	const viewWidth = 10000

	// Forward pagination: start from startDT, focus_position=0 places the
	// focus_datetime at the left edge so we receive klines *after* startDT.
	pos := 0
	begin := api.KlineBegin{FocusDatetime: &startDT, FocusPosition: &pos}

	endNS := endDT.UnixNano()
	durN := int64(durationSeconds) * int64(time.Second)

	var data []*api.Kline
	for {
		batch, err := m.QueryKlinesPage(ctx, api.KlineQueryReq{
			Symbols:         []string{symbol},
			DurationSeconds: durationSeconds,
			Begin:           begin,
			ViewWidth:       viewWidth,
		})
		if err != nil {
			if ctx.Err() != nil {
				return api.KlineDataSeriesResult{Data: data, Complete: false, Reason: "timeout"}, api.NewError(api.ErrTimeout, "get kline data series timeout", err)
			}
			return api.KlineDataSeriesResult{Data: data, Complete: false, Reason: "timeout"}, err
		}

		frame := batch.Frame1D
		if frame == nil {
			frame = m.store.KlineFrameByRange(symbol, durN, batch.LeftID, batch.RightID, viewWidth)
		}
		hasAny := false
		reachedEnd := false
		for _, k := range frame {
			if k == nil {
				continue
			}
			hasAny = true
			if k.Datetime > endNS {
				reachedEnd = true
				break
			}
			data = append(data, k)
		}
		if reachedEnd {
			break
		}
		if !hasAny {
			break
		}
		if batch.NextLeftKlineID == nil {
			break
		}
		begin = api.KlineBegin{LeftKlineID: batch.NextLeftKlineID}
	}

	complete := len(data) > 0
	reason := "enough_data"
	if !complete {
		reason = "no_more_data"
	}
	return api.KlineDataSeriesResult{Data: data, Complete: complete, Reason: reason}, nil
}

func (m *marketService) waitChartReady(ctx context.Context, chartID string, reqState map[string]any) (store.ChartState, error) {
	for {
		st, ok := m.store.Chart(chartID)
		if ok && st.Ready && (st.LeftID >= 0 || st.RightID >= 0) && stateSubset(reqState, st.State) {
			if !m.store.MDHisMoreData() {
				return st, nil
			}
		}
		select {
		case <-ctx.Done():
			return store.ChartState{}, api.NewError(api.ErrTimeout, "wait chart ready timeout", ctx.Err())
		case <-m.upSignal:
		}
	}
}

func validateBegin(begin api.KlineBegin) error {
	if begin.FocusDatetime != nil && begin.LeftKlineID != nil {
		return api.NewError(api.ErrInvalidBegin, "focus_datetime and left_kline_id are mutually exclusive", nil)
	}
	if (begin.FocusDatetime != nil && begin.FocusPosition == nil) || (begin.FocusDatetime == nil && begin.FocusPosition != nil) {
		return api.NewError(api.ErrInvalidBegin, "focus_datetime and focus_position must both exist", nil)
	}
	if begin.FocusDatetime == nil && begin.LeftKlineID == nil {
		return api.NewError(api.ErrInvalidBegin, "begin is required", nil)
	}
	return nil
}

func nextRight(rightID int64) *int64 {
	if rightID < 0 {
		return nil
	}
	v := rightID + 1
	return &v
}

// truncFrame1D removes klines with Datetime > endNS from the tail.
func truncFrame1D(frame []*api.Kline, endNS int64) []*api.Kline {
	for i := len(frame) - 1; i >= 0; i-- {
		if frame[i] != nil && frame[i].Datetime > endNS {
			frame = frame[:i]
		} else if frame[i] != nil {
			break
		}
	}
	return frame
}

// truncFrame2D removes rows whose main kline (index 0) Datetime > endNS.
func truncFrame2D(frame [][]*api.Kline, endNS int64) [][]*api.Kline {
	for i := len(frame) - 1; i >= 0; i-- {
		if len(frame[i]) > 0 && frame[i][0] != nil && frame[i][0].Datetime > endNS {
			frame = frame[:i]
		} else if len(frame[i]) > 0 && frame[i][0] != nil {
			break
		}
	}
	return frame
}

// truncRows removes AlignedKlineRow entries with Main.Datetime > endNS.
func truncRows(rows []api.AlignedKlineRow, endNS int64) []api.AlignedKlineRow {
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Main.Datetime > endNS {
			rows = rows[:i]
		} else {
			break
		}
	}
	return rows
}

func stateSubset(expect map[string]any, state map[string]any) bool {
	for k, v := range expect {
		if !equalLoose(state[k], v) {
			return false
		}
	}
	return true
}

func equalLoose(a any, b any) bool {
	switch x := a.(type) {
	case float64:
		y, ok := toFloat64(b)
		if !ok {
			return false
		}
		return x == y
	case int64:
		y, ok := toInt64(b)
		if !ok {
			return false
		}
		return x == y
	case int:
		y, ok := toInt64(b)
		if !ok {
			return false
		}
		return int64(x) == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	default:
		return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
	}
}

func mapKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func reverseRows(rows [][]*api.Kline) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

func genID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int64:
		return x, true
	case float64:
		return int64(x), true
	case json.Number:
		i, err := x.Int64()
		return i, err == nil
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return i, err == nil
	default:
		return 0, false
	}
}

func toFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// ------------------------- Query -------------------------

func (m *marketService) QueryGraphQL(ctx context.Context, query string, variables map[string]any) (api.GraphQLResult, error) {
	if strings.TrimSpace(query) == "" {
		return api.GraphQLResult{}, api.NewError(api.ErrInvalidArgument, "empty graphql query", nil)
	}
	qid := genID("GO_q")
	start := time.Now()
	entry, err := m.sendInsQuery(ctx, qid, query, variables)
	if err != nil {
		return api.GraphQLResult{}, err
	}
	res := api.GraphQLResult{Meta: api.QueryMeta{Source: api.QuerySourceWSInsQuery, QueryID: qid, Cost: time.Since(start)}, QueryID: qid, Query: query, Variables: variables}
	if r, ok := entry["result"].(map[string]any); ok {
		res.Result = r
	}
	if errs, ok := entry["errors"].([]any); ok {
		res.Errors = parseGraphQLErrors(errs)
	}
	if errNode, ok := entry["error"].(map[string]any); ok {
		res.Errors = append(res.Errors, api.GraphQLError{Message: fmt.Sprintf("%v", errNode)})
	}
	if raw, e := json.Marshal(entry); e == nil {
		res.Raw = raw
	}
	return res, nil
}

func (m *marketService) QueryQuotes(ctx context.Context, insClass []string, exchangeID []string, productID []string, expired *bool, hasNight *bool) ([]api.InstrumentID, error) {
	normClass := normalizeQueryEnums(insClass)
	normExchange := normalizeQueryStrings(exchangeID)
	normProduct := normalizeQueryStrings(productID)

	pythonClientSideExchangeFilter := ""
	applyExchangeInRequest := len(normExchange) > 0
	if len(normClass) == 1 && len(normExchange) == 1 {
		cls := normClass[0]
		ex := strings.ToUpper(normExchange[0])
		if (cls == "INDEX" || cls == "CONT") && isFutureExchange(ex) {
			// Python: for INDEX/CONT + futures exchange, do not pass exchange_id to GraphQL;
			// request all and filter instrument_id client side.
			applyExchangeInRequest = false
			pythonClientSideExchangeFilter = ex
		}
	}

	q := buildQueryQuotes(normClass, normExchange, normProduct, expired, hasNight, applyExchangeInRequest)
	ret, err := m.QueryGraphQL(ctx, q, nil)
	if err != nil {
		return nil, err
	}
	out := parseInstrumentIDs(ret.Result)
	if pythonClientSideExchangeFilter == "" {
		return out, nil
	}
	filtered := make([]api.InstrumentID, 0, len(out))
	for _, id := range out {
		if strings.Contains(strings.ToUpper(string(id)), pythonClientSideExchangeFilter) {
			filtered = append(filtered, id)
		}
	}
	return filtered, nil
}

func (m *marketService) QueryContQuotes(ctx context.Context, exchangeID string, productID string, hasNight *bool) ([]api.InstrumentID, error) {
	q := buildQueryContQuotes(hasNight)
	ret, err := m.QueryGraphQL(ctx, q, nil)
	if err != nil {
		return nil, err
	}
	return parseUnderlyingIDs(ret.Result, exchangeID, productID), nil
}

func (m *marketService) QueryOptions(ctx context.Context, underlyingSymbol string, optionClass string, exerciseYear int, exerciseMonth int, strikePrice *float64, expired *bool, hasA *bool) ([]api.InstrumentID, error) {
	nodes, err := m.queryOptionNodes(ctx, underlyingSymbol)
	if err != nil {
		return nil, err
	}
	filtered := filterOptionNodes(nodes, optionClass, exerciseYear, exerciseMonth, strikePrice, expired, hasA)
	out := make([]api.InstrumentID, 0, len(filtered))
	for _, n := range filtered {
		out = append(out, api.InstrumentID(n.InstrumentID))
	}
	return out, nil
}

func (m *marketService) QueryATMOptions(ctx context.Context, underlyingSymbol string, underlyingPrice float64, priceLevel []int, optionClass string, exerciseYear int, exerciseMonth int, hasA *bool) ([]*api.InstrumentID, error) {
	nodes, err := m.queryOptionNodes(ctx, underlyingSymbol)
	if err != nil {
		return nil, err
	}
	filtered := filterOptionNodes(nodes, optionClass, exerciseYear, exerciseMonth, nil, ptrBool(false), hasA)
	if len(filtered) == 0 {
		return nil, nil
	}
	strikes := uniqueSortedStrikes(filtered)
	if len(strikes) == 0 {
		return nil, nil
	}
	atmStrike := nearestStrike(strikes, underlyingPrice)
	if len(priceLevel) == 0 {
		priceLevel = []int{0}
	}
	res := make([]*api.InstrumentID, 0, len(priceLevel))
	for _, lv := range priceLevel {
		target := atmStrike + float64(lv)*strikeStep(strikes)
		node := findOptionByStrike(filtered, target, optionClass)
		if node == nil {
			res = append(res, nil)
			continue
		}
		v := api.InstrumentID(node.InstrumentID)
		res = append(res, &v)
	}
	return res, nil
}

func (m *marketService) QuerySymbolInfo(ctx context.Context, symbols []string) ([]api.Quote, error) {
	if len(symbols) == 0 {
		return nil, api.NewError(api.ErrInvalidSymbol, "empty symbols", nil)
	}
	normSymbols := make([]string, 0, len(symbols))
	for _, s := range symbols {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, api.NewError(api.ErrInvalidSymbol, "symbols contains empty string", nil)
		}
		normSymbols = append(normSymbols, s)
	}
	q := buildQuerySymbolInfo(normSymbols)
	ret, err := m.QueryGraphQL(ctx, q, nil)
	if err != nil {
		return nil, err
	}
	info := parseSymbolInfo(ret.Result, normSymbols)

	// For symbols where the server returned an empty stub (expired / delisted
	// contracts), fill in metadata from the embedded expired-quotes cache.
	for i := range info {
		if isEmptyQuote(info[i]) {
			if cached, ok := lookupEmbeddedQuote(info[i].Symbol); ok {
				info[i] = cached
			}
		}
	}

	return info, nil
}

func (m *marketService) QueryAllLevelOptions(ctx context.Context, underlyingSymbol string, underlyingPrice float64, optionClass string, exerciseYear int, exerciseMonth int, hasA *bool) (inMoney []api.InstrumentID, atMoney []api.InstrumentID, outMoney []api.InstrumentID, err error) {
	nodes, err := m.queryOptionNodes(ctx, underlyingSymbol)
	if err != nil {
		return nil, nil, nil, err
	}
	filtered := filterOptionNodes(nodes, optionClass, exerciseYear, exerciseMonth, nil, ptrBool(false), hasA)
	if len(filtered) == 0 {
		return nil, nil, nil, nil
	}
	atm := nearestStrike(uniqueSortedStrikes(filtered), underlyingPrice)
	for _, n := range filtered {
		id := api.InstrumentID(n.InstrumentID)
		if math.Abs(n.StrikePrice-atm) < 1e-9 {
			atMoney = append(atMoney, id)
			continue
		}
		if strings.EqualFold(optionClass, "CALL") {
			if n.StrikePrice < atm {
				inMoney = append(inMoney, id)
			} else {
				outMoney = append(outMoney, id)
			}
		} else {
			if n.StrikePrice > atm {
				inMoney = append(inMoney, id)
			} else {
				outMoney = append(outMoney, id)
			}
		}
	}
	return
}

func (m *marketService) QueryAllLevelFinanceOptions(ctx context.Context, underlyingSymbol string, underlyingPrice float64, optionClass string, nearbys []int, hasA *bool) (inMoney []api.InstrumentID, atMoney []api.InstrumentID, outMoney []api.InstrumentID, err error) {
	nodes, err := m.queryOptionNodes(ctx, underlyingSymbol)
	if err != nil {
		return nil, nil, nil, err
	}
	filtered := filterOptionNodes(nodes, optionClass, 0, 0, nil, ptrBool(false), hasA)
	filtered = filterOptionNodesByNearbys(filtered, nearbys)
	if len(filtered) == 0 {
		return nil, nil, nil, nil
	}
	atm := nearestStrike(uniqueSortedStrikes(filtered), underlyingPrice)
	for _, n := range filtered {
		id := api.InstrumentID(n.InstrumentID)
		if math.Abs(n.StrikePrice-atm) < 1e-9 {
			atMoney = append(atMoney, id)
			continue
		}
		cls := strings.ToUpper(strings.TrimSpace(optionClass))
		if cls == "" {
			cls = strings.ToUpper(strings.TrimSpace(n.CallOrPut))
		}
		if cls == "CALL" {
			if n.StrikePrice < atm {
				inMoney = append(inMoney, id)
			} else {
				outMoney = append(outMoney, id)
			}
		} else {
			if n.StrikePrice > atm {
				inMoney = append(inMoney, id)
			} else {
				outMoney = append(outMoney, id)
			}
		}
	}
	return
}

func (m *marketService) QueryHisContQuotes(ctx context.Context, symbols []string, n int) (api.HisContQuoteResult, error) {
	if len(symbols) == 0 {
		return api.HisContQuoteResult{}, api.NewError(api.ErrInvalidSymbol, "empty symbols", nil)
	}
	if n <= 0 {
		return api.HisContQuoteResult{}, api.NewError(api.ErrInvalidArgument, "n must be >= 1", nil)
	}
	start := time.Now()
	u := "https://files.shinnytech.com/continuous_table.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return api.HisContQuoteResult{}, err
	}
	req.Header = m.auth.Header()
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return api.HisContQuoteResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return api.HisContQuoteResult{}, fmt.Errorf("status=%d body=%s", resp.StatusCode, string(body))
	}
	var tbl map[string][][]any
	if err := json.NewDecoder(resp.Body).Decode(&tbl); err != nil {
		return api.HisContQuoteResult{}, err
	}

	normSymbols := make([]string, 0, len(symbols))
	for _, s := range symbols {
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "KQ.m@") {
			s = strings.TrimPrefix(s, "KQ.m@")
		}
		normSymbols = append(normSymbols, s)
	}
	dateSet := map[string]struct{}{}
	series := map[string][][2]string{}
	for _, s := range normSymbols {
		rows := tbl[s]
		arr := make([][2]string, 0, len(rows))
		for _, r := range rows {
			if len(r) < 2 {
				continue
			}
			d := fmt.Sprintf("%v", r[0])
			u := fmt.Sprintf("%v", r[1])
			arr = append(arr, [2]string{d, u})
			dateSet[d] = struct{}{}
		}
		sort.Slice(arr, func(i, j int) bool { return arr[i][0] < arr[j][0] })
		series[s] = arr
	}
	dates := make([]string, 0, len(dateSet))
	for d := range dateSet {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	if len(dates) > n {
		dates = dates[len(dates)-n:]
	}

	rows := make([]api.HisContQuoteRow, 0, len(dates))
	for _, d := range dates {
		under := make([]string, len(normSymbols))
		for i, s := range normSymbols {
			under[i] = contAtDate(series[s], d)
		}
		t, _ := time.Parse("20060102", d)
		rows = append(rows, api.HisContQuoteRow{Date: t, Underlyings: under})
	}
	ids := make([]api.InstrumentID, 0, len(symbols))
	for _, s := range symbols {
		ids = append(ids, api.InstrumentID(s))
	}
	return api.HisContQuoteResult{Meta: api.QueryMeta{Source: api.QuerySourceHTTP, Cost: time.Since(start)}, Symbols: ids, Rows: rows}, nil
}

func (m *marketService) QuerySymbolSettlement(ctx context.Context, symbols []string, days int, startDT *time.Time) (api.SettlementResult, error) {
	if len(symbols) == 0 {
		return api.SettlementResult{}, api.NewError(api.ErrInvalidSymbol, "empty symbols", nil)
	}
	if days <= 0 {
		return api.SettlementResult{}, api.NewError(api.ErrInvalidArgument, "days must be >= 1", nil)
	}
	start := time.Now()
	q := url.Values{}
	q.Set("days", strconv.Itoa(days))
	q.Set("symbols", strings.Join(symbols, ","))
	if startDT != nil {
		q.Set("start_date", startDT.Format("20060102"))
	}
	u := "https://md-settlement-system-fc-api.shinnytech.com/mss?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return api.SettlementResult{}, err
	}
	req.Header = m.auth.Header()
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return api.SettlementResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return api.SettlementResult{}, fmt.Errorf("status=%d body=%s", resp.StatusCode, string(body))
	}
	var content map[string]map[string]float64
	if err := json.NewDecoder(resp.Body).Decode(&content); err != nil {
		return api.SettlementResult{}, err
	}
	rows := make([]api.SettlementRow, 0)
	for dt, bySym := range content {
		for sym, val := range bySym {
			rows = append(rows, api.SettlementRow{Datetime: dt, Symbol: sym, Settlement: val})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Datetime == rows[j].Datetime {
			return rows[i].Symbol < rows[j].Symbol
		}
		return rows[i].Datetime < rows[j].Datetime
	})
	return api.SettlementResult{Meta: api.QueryMeta{Source: api.QuerySourceHTTP, Cost: time.Since(start)}, Rows: rows}, nil
}

func (m *marketService) QueryEDBData(ctx context.Context, ids []int, n int, align string, fill string) (api.EDBDataResult, error) {
	if len(ids) == 0 {
		return api.EDBDataResult{}, api.NewError(api.ErrInvalidArgument, "empty ids", nil)
	}
	if n <= 0 {
		return api.EDBDataResult{}, api.NewError(api.ErrInvalidArgument, "n must be >= 1", nil)
	}
	startTime := time.Now()
	end := time.Now().Format("2006-01-02")
	begin := time.Now().AddDate(0, 0, -(n - 1)).Format("2006-01-02")
	payload := map[string]any{"ids": ids, "start": begin, "end": end}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://edb.shinnytech.com/data/index_data", strings.NewReader(string(b)))
	if err != nil {
		return api.EDBDataResult{}, err
	}
	req.Header = m.auth.Header()
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return api.EDBDataResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return api.EDBDataResult{}, fmt.Errorf("status=%d body=%s", resp.StatusCode, string(body))
	}
	var out struct {
		ErrorCode int    `json:"error_code"`
		ErrorMsg  string `json:"error_msg"`
		Data      struct {
			IDs    []int                `json:"ids"`
			Values map[string][]float64 `json:"values"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return api.EDBDataResult{}, err
	}
	if out.ErrorCode != 0 {
		return api.EDBDataResult{}, fmt.Errorf("edb error: %s", out.ErrorMsg)
	}
	dates := make([]string, 0, len(out.Data.Values))
	for d := range out.Data.Values {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	values := make([][]api.EDBValue, 0, len(dates))
	for _, d := range dates {
		row := out.Data.Values[d]
		cells := make([]api.EDBValue, len(out.Data.IDs))
		for i := range out.Data.IDs {
			if i >= len(row) {
				cells[i] = api.EDBValue{Valid: false}
				continue
			}
			cells[i] = api.EDBValue{Valid: true, Value: row[i]}
		}
		values = append(values, cells)
	}
	if align == "day" {
		dates, values = alignEDBDay(begin, end, dates, values)
		if fill == "ffill" {
			fillForward(values)
		} else if fill == "bfill" {
			fillBackward(values)
		}
	}
	return api.EDBDataResult{Meta: api.QueryMeta{Source: api.QuerySourceHTTP, Cost: time.Since(startTime)}, Dates: dates, IDs: out.Data.IDs, Values: values}, nil
}

func (m *marketService) QuerySymbolRanking(ctx context.Context, symbol string, rankingType api.RankingType, days int, startDT *time.Time, broker string) (api.SymbolRankingResult, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return api.SymbolRankingResult{}, api.NewError(api.ErrInvalidSymbol, "empty symbol", nil)
	}
	if rankingType != api.RankingTypeVolume && rankingType != api.RankingTypeLong && rankingType != api.RankingTypeShort {
		return api.SymbolRankingResult{}, api.NewError(api.ErrInvalidArgument, "invalid ranking type", nil)
	}
	if days <= 0 {
		return api.SymbolRankingResult{}, api.NewError(api.ErrInvalidArgument, "days must be >= 1", nil)
	}
	start := time.Now()
	q := url.Values{}
	q.Set("symbol", symbol)
	q.Set("days", strconv.Itoa(days))
	if startDT != nil {
		q.Set("start_date", startDT.Format("20060102"))
	}
	if strings.TrimSpace(broker) != "" {
		q.Set("broker", broker)
	}
	u := "https://symbol-ranking-system-fc-api.shinnytech.com/srs?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return api.SymbolRankingResult{}, err
	}
	req.Header = m.auth.Header()
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return api.SymbolRankingResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return api.SymbolRankingResult{}, fmt.Errorf("status=%d body=%s", resp.StatusCode, string(body))
	}
	var content map[string]map[string]map[string]map[string]struct {
		Volume    float64 `json:"volume"`
		VarVolume float64 `json:"varvolume"`
		Ranking   float64 `json:"ranking"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&content); err != nil {
		return api.SymbolRankingResult{}, err
	}
	tmp := map[string]api.SymbolRankingRow{}
	for dt, bySym := range content {
		for sym, byType := range bySym {
			for dataType, byBroker := range byType {
				for brk, it := range byBroker {
					k := dt + "|" + sym + "|" + brk
					row, ok := tmp[k]
					if !ok {
						parts := strings.SplitN(sym, ".", 2)
						row = api.SymbolRankingRow{Datetime: dt, Symbol: sym, Broker: brk}
						if len(parts) == 2 {
							row.ExchangeID = parts[0]
							row.InstrumentID = parts[1]
						}
					}
					switch dataType {
					case "volume_ranking":
						row.Volume = ptrFloat(it.Volume)
						row.VolumeChange = ptrFloat(it.VarVolume)
						row.VolumeRanking = ptrFloat(it.Ranking)
					case "long_ranking":
						row.LongOI = ptrFloat(it.Volume)
						row.LongChange = ptrFloat(it.VarVolume)
						row.LongRanking = ptrFloat(it.Ranking)
					case "short_ranking":
						row.ShortOI = ptrFloat(it.Volume)
						row.ShortChange = ptrFloat(it.VarVolume)
						row.ShortRanking = ptrFloat(it.Ranking)
					}
					tmp[k] = row
				}
			}
		}
	}
	rows := make([]api.SymbolRankingRow, 0, len(tmp))
	for _, r := range tmp {
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Datetime == rows[j].Datetime {
			return rankVal(rows[i], rankingType) < rankVal(rows[j], rankingType)
		}
		return rows[i].Datetime < rows[j].Datetime
	})
	return api.SymbolRankingResult{Meta: api.QueryMeta{Source: api.QuerySourceHTTP, Cost: time.Since(start)}, RankingType: rankingType, Rows: rows}, nil
}

func rankVal(r api.SymbolRankingRow, t api.RankingType) float64 {
	switch t {
	case api.RankingTypeVolume:
		if r.VolumeRanking != nil {
			return *r.VolumeRanking
		}
	case api.RankingTypeLong:
		if r.LongRanking != nil {
			return *r.LongRanking
		}
	case api.RankingTypeShort:
		if r.ShortRanking != nil {
			return *r.ShortRanking
		}
	}
	return math.MaxFloat64
}

func (m *marketService) sendInsQuery(ctx context.Context, queryID string, query string, variables map[string]any) (map[string]any, error) {
	flightKey := insQueryFlightKey(query, variables)
	flight, leader := m.acquireInsQueryFlight(flightKey)
	defer m.releaseInsQueryFlight(flightKey, flight)
	if !leader {
		select {
		case <-ctx.Done():
			return nil, api.NewError(api.ErrTimeout, "ins_query timeout", ctx.Err())
		case <-flight.done:
			return cloneAnyMap(flight.result), flight.err
		}
	}

	ret, err := m.sendInsQueryLeader(ctx, queryID, query, variables)
	m.completeInsQueryFlight(flight, ret, err)
	return ret, err
}

func (m *marketService) sendInsQueryLeader(ctx context.Context, queryID string, query string, variables map[string]any) (map[string]any, error) {
	if err := m.requireStarted(); err != nil {
		return nil, err
	}
	waiter := make(chan struct{}, 1)
	m.queryMu.Lock()
	m.queryWaiters[queryID] = waiter
	m.queryMu.Unlock()
	defer func() {
		m.queryMu.Lock()
		delete(m.queryWaiters, queryID)
		m.queryMu.Unlock()
	}()

	pack := map[string]any{"aid": "ins_query", "query_id": queryID, "query": query}
	if variables != nil {
		pack["variables"] = variables
	}
	if err := m.sendPack(pack); err != nil {
		return nil, err
	}
	_ = m.sendPack(map[string]any{"aid": "peek_message"})
	for {
		entry, ok := m.store.QuerySymbol(queryID)
		if ok && len(entry) > 0 {
			if _, ok := entry["result"]; ok {
				return entry, nil
			}
			if _, ok := entry["error"]; ok {
				return entry, nil
			}
			if _, ok := entry["errors"]; ok {
				return entry, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, api.NewError(api.ErrTimeout, "ins_query timeout", ctx.Err())
		case <-waiter:
		}
	}
}

func (m *marketService) acquireInsQueryFlight(key string) (*insQueryFlight, bool) {
	if key == "" {
		return &insQueryFlight{done: make(chan struct{}), waiters: 1}, true
	}
	m.queryMu.Lock()
	defer m.queryMu.Unlock()
	if old, ok := m.queryFlights[key]; ok {
		old.waiters++
		return old, false
	}
	f := &insQueryFlight{done: make(chan struct{}), waiters: 1}
	m.queryFlights[key] = f
	return f, true
}

func (m *marketService) releaseInsQueryFlight(key string, f *insQueryFlight) {
	if key == "" || f == nil {
		return
	}
	m.queryMu.Lock()
	defer m.queryMu.Unlock()
	cur, ok := m.queryFlights[key]
	if !ok || cur != f {
		return
	}
	cur.waiters--
	if cur.waiters <= 0 {
		delete(m.queryFlights, key)
	}
}

func (m *marketService) completeInsQueryFlight(f *insQueryFlight, ret map[string]any, err error) {
	if f == nil {
		return
	}
	f.result = cloneAnyMap(ret)
	f.err = err
	select {
	case <-f.done:
	default:
		close(f.done)
	}
}

func insQueryFlightKey(query string, variables map[string]any) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}
	if len(variables) == 0 {
		return query + "\n{}"
	}
	b, err := json.Marshal(variables)
	if err != nil {
		return query + "\n{invalid}"
	}
	return query + "\n" + string(b)
}

func cloneAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	b, err := json.Marshal(src)
	if err != nil {
		cp := map[string]any{}
		for k, v := range src {
			cp[k] = v
		}
		return cp
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		cp := map[string]any{}
		for k, v := range src {
			cp[k] = v
		}
		return cp
	}
	return out
}

func parseGraphQLErrors(arr []any) []api.GraphQLError {
	out := make([]api.GraphQLError, 0, len(arr))
	for _, x := range arr {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		e := api.GraphQLError{}
		e.Message = fmt.Sprintf("%v", m["message"])
		out = append(out, e)
	}
	return out
}

func parseInstrumentIDs(result map[string]any) []api.InstrumentID {
	rows, _ := result["multi_symbol_info"].([]any)
	out := make([]api.InstrumentID, 0, len(rows))
	for _, x := range rows {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["instrument_id"].(string)
		if id == "" {
			continue
		}
		out = append(out, api.InstrumentID(id))
	}
	return out
}

func parseUnderlyingIDs(result map[string]any, exchangeID string, productID string) []api.InstrumentID {
	rows, _ := result["multi_symbol_info"].([]any)
	exFilter := strings.ToUpper(strings.TrimSpace(exchangeID))
	productFilter := strings.ToLower(strings.TrimSpace(productID))
	out := make([]api.InstrumentID, 0, len(rows))
	for _, x := range rows {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		der, _ := m["underlying"].(map[string]any)
		edges, _ := der["edges"].([]any)
		for _, e := range edges {
			em, ok := e.(map[string]any)
			if !ok {
				continue
			}
			node, _ := em["node"].(map[string]any)
			nodeExchange, _ := node["exchange_id"].(string)
			nodeProduct, _ := node["product_id"].(string)
			if exFilter != "" && strings.ToUpper(strings.TrimSpace(nodeExchange)) != exFilter {
				continue
			}
			if productFilter != "" && strings.ToLower(strings.TrimSpace(nodeProduct)) != productFilter {
				continue
			}
			id, _ := node["instrument_id"].(string)
			if id == "" {
				continue
			}
			out = append(out, api.InstrumentID(id))
		}
	}
	return out
}

type optionNode struct {
	InstrumentID     string
	ExchangeID       string
	EnglishName      string
	CallOrPut        string
	StrikePrice      float64
	ExpireDatetime   int64
	LastExerciseTime int64
	Expired          bool
	OptionClassField string
}

func (m *marketService) queryOptionNodes(ctx context.Context, underlyingSymbol string) ([]optionNode, error) {
	underlyingSymbol = strings.TrimSpace(underlyingSymbol)
	if underlyingSymbol == "" {
		return nil, api.NewError(api.ErrInvalidSymbol, "empty underlying symbol", nil)
	}
	q := buildQueryOptionsByUnderlying(underlyingSymbol)
	ret, err := m.QueryGraphQL(ctx, q, nil)
	if err != nil {
		return nil, err
	}
	rows, _ := ret.Result["multi_symbol_info"].([]any)
	out := make([]optionNode, 0)
	for _, x := range rows {
		m1, ok := x.(map[string]any)
		if !ok {
			continue
		}
		der, _ := m1["derivatives"].(map[string]any)
		edges, _ := der["edges"].([]any)
		for _, e := range edges {
			em, ok := e.(map[string]any)
			if !ok {
				continue
			}
			node, _ := em["node"].(map[string]any)
			id, _ := node["instrument_id"].(string)
			if id == "" {
				continue
			}
			cp, _ := node["call_or_put"].(string)
			ex, _ := node["exchange_id"].(string)
			en, _ := node["english_name"].(string)
			strike, _ := toFloat64(node["strike_price"])
			exp, _ := toInt64(node["expire_datetime"])
			lastEx, _ := toInt64(node["last_exercise_datetime"])
			expired, _ := node["expired"].(bool)
			cls, _ := node["class"].(string)
			out = append(out, optionNode{
				InstrumentID:     id,
				ExchangeID:       strings.ToUpper(strings.TrimSpace(ex)),
				EnglishName:      en,
				CallOrPut:        strings.ToUpper(cp),
				StrikePrice:      strike,
				ExpireDatetime:   exp,
				LastExerciseTime: lastEx,
				Expired:          expired,
				OptionClassField: cls,
			})
		}
	}
	return out, nil
}

func filterOptionNodes(nodes []optionNode, optionClass string, year int, month int, strike *float64, expired *bool, hasA *bool) []optionNode {
	out := make([]optionNode, 0, len(nodes))
	oc := strings.ToUpper(strings.TrimSpace(optionClass))
	for _, n := range nodes {
		if oc != "" && n.CallOrPut != oc {
			continue
		}
		if strike != nil && math.Abs(n.StrikePrice-*strike) > 1e-9 {
			continue
		}
		if expired != nil && n.Expired != *expired {
			continue
		}
		if hasA != nil {
			has := strings.Count(strings.ToUpper(n.EnglishName), "A") > 0
			if *hasA && !has {
				continue
			}
			if !*hasA && has {
				continue
			}
		}
		if year > 0 || month > 0 {
			ns := n.LastExerciseTime
			if ns <= 0 {
				ns = n.ExpireDatetime
			}
			t := time.Unix(0, ns).In(cstTimeZone())
			if year > 0 && t.Year() != year {
				continue
			}
			if month > 0 && int(t.Month()) != month {
				continue
			}
		}
		out = append(out, n)
	}
	return out
}

func uniqueSortedStrikes(nodes []optionNode) []float64 {
	set := map[float64]struct{}{}
	for _, n := range nodes {
		set[n.StrikePrice] = struct{}{}
	}
	out := make([]float64, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Float64s(out)
	return out
}

func filterOptionNodesByNearbys(nodes []optionNode, nearbys []int) []optionNode {
	if len(nodes) == 0 {
		return nil
	}
	if len(nearbys) == 0 {
		nearbys = []int{0}
	}
	expiries := uniqueSortedExpiries(nodes)
	if len(expiries) == 0 {
		return nil
	}
	allow := map[int64]struct{}{}
	for _, idx := range nearbys {
		if idx < 0 || idx >= len(expiries) {
			continue
		}
		allow[expiries[idx]] = struct{}{}
	}
	if len(allow) == 0 {
		return nil
	}
	out := make([]optionNode, 0, len(nodes))
	for _, n := range nodes {
		if _, ok := allow[n.ExpireDatetime]; ok {
			out = append(out, n)
		}
	}
	return out
}

func uniqueSortedExpiries(nodes []optionNode) []int64 {
	set := map[int64]struct{}{}
	for _, n := range nodes {
		ns := n.LastExerciseTime
		if ns <= 0 {
			ns = n.ExpireDatetime
		}
		if ns <= 0 {
			continue
		}
		set[ns] = struct{}{}
	}
	out := make([]int64, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func nearestStrike(strikes []float64, p float64) float64 {
	if len(strikes) == 0 {
		return 0
	}
	best := strikes[0]
	bestDist := math.Abs(strikes[0] - p)
	for i := 1; i < len(strikes); i++ {
		d := math.Abs(strikes[i] - p)
		if d < bestDist {
			best = strikes[i]
			bestDist = d
		}
	}
	return best
}

func strikeStep(strikes []float64) float64 {
	if len(strikes) < 2 {
		return 0
	}
	step := math.Abs(strikes[1] - strikes[0])
	for i := 2; i < len(strikes); i++ {
		d := math.Abs(strikes[i] - strikes[i-1])
		if d > 0 && (step == 0 || d < step) {
			step = d
		}
	}
	if step == 0 {
		step = 1
	}
	return step
}

func findOptionByStrike(nodes []optionNode, strike float64, optionClass string) *optionNode {
	oc := strings.ToUpper(strings.TrimSpace(optionClass))
	for i := range nodes {
		if oc != "" && nodes[i].CallOrPut != oc {
			continue
		}
		if math.Abs(nodes[i].StrikePrice-strike) < 1e-9 {
			return &nodes[i]
		}
	}
	return nil
}

func parseSymbolInfo(result map[string]any, requested []string) []api.Quote {
	all := collectSymbolNodes(result)
	out := make([]api.Quote, 0, len(requested))
	for _, sym := range requested {
		sym = strings.TrimSpace(sym)
		if sym == "" {
			continue
		}
		node, ok := all[sym]
		if !ok {
			out = append(out, api.Quote{Symbol: sym, InstrumentID: sym})
			continue
		}
		q := convertSymbolNodeToQuote(node, all)
		if q.InstrumentID == "" {
			q.InstrumentID = sym
		}
		q.Symbol = q.InstrumentID
		out = append(out, q)
	}
	return out
}

func quoteStrings(arr []string) string {
	parts := make([]string, 0, len(arr))
	for _, s := range arr {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		parts = append(parts, "\""+s+"\"")
	}
	return strings.Join(parts, ",")
}

func normalizeQueryStrings(arr []string) []string {
	out := make([]string, 0, len(arr))
	for _, s := range arr {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

func normalizeQueryEnums(arr []string) []string {
	out := make([]string, 0, len(arr))
	for _, s := range arr {
		s = strings.ToUpper(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

func isFutureExchange(ex string) bool {
	_, ok := futureExchanges[strings.ToUpper(strings.TrimSpace(ex))]
	return ok
}

func buildQueryQuotes(normClass []string, normExchange []string, normProduct []string, expired *bool, hasNight *bool, applyExchangeInRequest bool) string {
	args := make([]graphqlx.Arg, 0, 5)
	if len(normClass) > 0 {
		args = append(args, graphqlx.EnumList("class", normClass))
	}
	if applyExchangeInRequest && len(normExchange) > 0 {
		args = append(args, graphqlx.StringList("exchange_id", normExchange))
	}
	if len(normProduct) > 0 {
		args = append(args, graphqlx.StringList("product_id", normProduct))
	}
	args = append(args, graphqlx.OptBool("expired", expired))
	args = append(args, graphqlx.OptBool("has_night", hasNight))
	field := graphqlx.Field("multi_symbol_info", args...)
	return graphqlx.QueryField(field, "__typename ... on basic { instrument_id }")
}

func buildQueryContQuotes(hasNight *bool) string {
	field := graphqlx.Field(
		"multi_symbol_info",
		graphqlx.EnumList("class", []string{"CONT"}),
		graphqlx.OptBool("has_night", hasNight),
	)
	return graphqlx.QueryField(
		field,
		"__typename ... on basic { instrument_id } ... on derivative { underlying { edges { node { __typename ... on basic { instrument_id exchange_id } ... on future { product_id } } } } }",
	)
}

func buildQueryOptionsByUnderlying(underlyingSymbol string) string {
	field := graphqlx.Field("multi_symbol_info", graphqlx.StringList("instrument_id", []string{underlyingSymbol}))
	return graphqlx.QueryField(
		field,
		"__typename ... on basic { instrument_id derivatives(class: [OPTION]) { edges { node { __typename ... on basic { class instrument_id exchange_id english_name } ... on option { expired expire_datetime last_exercise_datetime strike_price call_or_put } } } } }",
	)
}

func cstTimeZone() *time.Location {
	return time.FixedZone("CST", 8*3600)
}

func buildQuerySymbolInfo(symbols []string) string {
	return fmt.Sprintf(`query {
  multi_symbol_info(instrument_id: [%s]) {
    __typename
    ...basic
    ...stock
    ...fund
    ...bond
    ...tradeable
    ...index
    ...securities
    ...future
    ...option
    ...combine
    ...derivative
  }
}
fragment basic on basic {
  instrument_id
  exchange_id
  instrument_name
  english_name
  class
  price_tick
  price_decs
  trading_day
  trading_time {
    day
    night
  }
}
fragment stock on stock {
  stock_dividend_ratio
  cash_dividend_ratio
}
fragment fund on fund {
  cash_dividend_ratio
}
fragment bond on bond {
  maturity_datetime
}
fragment tradeable on tradeable {
  pre_close
  volume_multiple
  quote_multiple
  upper_limit
  lower_limit
}
fragment index on index {
  index_multiple
}
fragment securities on securities {
  currency
  face_value
  first_trading_datetime
  buy_volume_unit
  sell_volume_unit
  status
  public_float_share_quantity
}
fragment future on future {
  pre_open_interest
  expired
  product_id
  product_short_name
  delivery_year
  delivery_month
  expire_datetime
  settlement_price
  max_market_order_volume
  max_limit_order_volume
  min_market_order_volume
  min_limit_order_volume
  open_max_market_order_volume
  open_max_limit_order_volume
  open_min_market_order_volume
  open_min_limit_order_volume
  margin
  commission
  mmsa
  categories {
    id
    name
  }
  position_limit
}
fragment option on option {
  pre_open_interest
  expired
  product_short_name
  expire_datetime
  last_exercise_datetime
  settlement_price
  max_market_order_volume
  max_limit_order_volume
  min_market_order_volume
  min_limit_order_volume
  open_max_market_order_volume
  open_max_limit_order_volume
  open_min_market_order_volume
  open_min_limit_order_volume
  strike_price
  call_or_put
  exercise_type
  position_limit
}
fragment combine on combine {
  expired
  product_id
  expire_datetime
  max_market_order_volume
  max_limit_order_volume
  min_market_order_volume
  min_limit_order_volume
  open_max_market_order_volume
  open_max_limit_order_volume
  open_min_market_order_volume
  open_min_limit_order_volume
  leg1 {
    __typename
    ... on basic {
      instrument_id
    }
  }
  leg2 {
    __typename
    ... on basic {
      instrument_id
    }
  }
}
fragment derivative on derivative {
  underlying {
    count
    edges {
      underlying_multiple
      node {
        __typename
        ...basic
        ...stock
        ...fund
        ...bond
        ...tradeable
        ...index
        ...securities
        ...future
      }
    }
  }
}`, quoteStrings(symbols))
}

func collectSymbolNodes(result map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	rows, _ := result["multi_symbol_info"].([]any)
	var walk func(node map[string]any)
	walk = func(node map[string]any) {
		if len(node) == 0 {
			return
		}
		id, _ := node["instrument_id"].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		base := out[id]
		if base == nil {
			base = map[string]any{}
			out[id] = base
		}
		for k, v := range node {
			if v != nil {
				base[k] = v
			}
		}
		under, _ := node["underlying"].(map[string]any)
		edges, _ := under["edges"].([]any)
		for _, e := range edges {
			em, ok := e.(map[string]any)
			if !ok {
				continue
			}
			child, _ := em["node"].(map[string]any)
			if len(child) == 0 {
				continue
			}
			walk(child)
		}
	}
	for _, x := range rows {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		walk(m)
	}
	return out
}

func convertSymbolNodeToQuote(node map[string]any, all map[string]map[string]any) api.Quote {
	m := cloneAnyMap(node)
	if m == nil {
		m = map[string]any{}
	}
	if cls, ok := m["class"].(string); ok {
		m["ins_class"] = cls
	}
	if cp, ok := m["call_or_put"].(string); ok {
		m["option_class"] = cp
	}
	if _, ok := m["volume_multiple"]; !ok {
		if idxMul, ok := toInt64(m["index_multiple"]); ok {
			m["volume_multiple"] = idxMul
		} else if idxMulF, ok := toFloat64(m["index_multiple"]); ok {
			m["volume_multiple"] = int64(idxMulF)
		}
	}
	if settlement, ok := m["settlement_price"]; ok {
		m["pre_settlement"] = settlement
	}
	if ns, ok := toInt64(m["expire_datetime"]); ok && ns > 0 {
		m["expire_datetime"] = float64(ns) / 1e9
	}
	if ns, ok := toInt64(m["last_exercise_datetime"]); ok && ns > 0 {
		m["last_exercise_datetime"] = float64(ns) / 1e9
		tm := time.Unix(0, ns).In(cstTimeZone())
		m["exercise_year"] = int64(tm.Year())
		m["exercise_month"] = int64(tm.Month())
	}
	if under, ok := extractFirstUnderlyingNode(node); ok {
		if uid, ok := under["instrument_id"].(string); ok && strings.TrimSpace(uid) != "" {
			m["underlying_symbol"] = uid
		}
	}

	exchangeID, _ := m["exchange_id"].(string)
	exchangeID = strings.ToUpper(strings.TrimSpace(exchangeID))
	if strings.EqualFold(fmt.Sprintf("%v", m["class"]), "OPTION") {
		if exchangeID == "DCE" || exchangeID == "CZCE" || exchangeID == "SHFE" || exchangeID == "GFEX" {
			if us, ok := m["underlying_symbol"].(string); ok && us != "" {
				if underlying, ok := all[us]; ok {
					if dy, ok := toInt64(underlying["delivery_year"]); ok {
						m["delivery_year"] = dy
					}
					if dm, ok := toInt64(underlying["delivery_month"]); ok {
						m["delivery_month"] = dm
					}
				}
			}
		}
		if exchangeID == "CFFEX" {
			if ns, ok := toInt64(node["last_exercise_datetime"]); ok && ns > 0 {
				tm := time.Unix(0, ns).In(cstTimeZone())
				m["delivery_year"] = int64(tm.Year())
				m["delivery_month"] = int64(tm.Month())
			}
		}
	}

	var q api.Quote
	if b, err := json.Marshal(m); err == nil {
		_ = json.Unmarshal(b, &q)
	}
	if q.InsClass == "COMBINE" && q.VolumeMultiple == 0 {
		if leg, ok := node["leg1"].(map[string]any); ok {
			if legID, ok := leg["instrument_id"].(string); ok && legID != "" {
				if under, ok := all[legID]; ok {
					if v, ok := toInt64(under["volume_multiple"]); ok {
						q.VolumeMultiple = v
					}
				}
			}
		}
	}
	return q
}

func extractFirstUnderlyingNode(node map[string]any) (map[string]any, bool) {
	under, ok := node["underlying"].(map[string]any)
	if !ok {
		return nil, false
	}
	edges, ok := under["edges"].([]any)
	if !ok || len(edges) == 0 {
		return nil, false
	}
	for _, e := range edges {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		child, ok := em["node"].(map[string]any)
		if ok && len(child) > 0 {
			return child, true
		}
	}
	return nil, false
}

func contAtDate(rows [][2]string, date string) string {
	if len(rows) == 0 {
		return ""
	}
	cur := ""
	for _, r := range rows {
		if r[0] > date {
			break
		}
		cur = r[1]
	}
	return cur
}

func ptrBool(v bool) *bool { return &v }

func ptrFloat(v float64) *float64 { return &v }

func alignEDBDay(start, end string, dates []string, values [][]api.EDBValue) ([]string, [][]api.EDBValue) {
	m := map[string][]api.EDBValue{}
	for i, d := range dates {
		m[d] = values[i]
	}
	st, _ := time.Parse("2006-01-02", start)
	ed, _ := time.Parse("2006-01-02", end)
	allDates := make([]string, 0)
	allVals := make([][]api.EDBValue, 0)
	for d := st; !d.After(ed); d = d.AddDate(0, 0, 1) {
		s := d.Format("2006-01-02")
		allDates = append(allDates, s)
		if row, ok := m[s]; ok {
			allVals = append(allVals, row)
			continue
		}
		allVals = append(allVals, nil)
	}
	return allDates, allVals
}

func fillForward(values [][]api.EDBValue) {
	if len(values) == 0 {
		return
	}
	cols := 0
	for _, r := range values {
		if len(r) > cols {
			cols = len(r)
		}
	}
	for c := 0; c < cols; c++ {
		var last *api.EDBValue
		for r := 0; r < len(values); r++ {
			if c >= len(values[r]) || !values[r][c].Valid {
				if last != nil {
					if c >= len(values[r]) {
						row := make([]api.EDBValue, cols)
						copy(row, values[r])
						values[r] = row
					}
					values[r][c] = *last
				}
				continue
			}
			v := values[r][c]
			last = &v
		}
	}
}

func fillBackward(values [][]api.EDBValue) {
	if len(values) == 0 {
		return
	}
	cols := 0
	for _, r := range values {
		if len(r) > cols {
			cols = len(r)
		}
	}
	for c := 0; c < cols; c++ {
		var next *api.EDBValue
		for r := len(values) - 1; r >= 0; r-- {
			if c >= len(values[r]) || !values[r][c].Valid {
				if next != nil {
					if c >= len(values[r]) {
						row := make([]api.EDBValue, cols)
						copy(row, values[r])
						values[r] = row
					}
					values[r][c] = *next
				}
				continue
			}
			v := values[r][c]
			next = &v
		}
	}
}
