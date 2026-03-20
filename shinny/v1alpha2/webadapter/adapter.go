package webadapter

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
)

type Adapter struct {
	cfg Config

	mu      sync.RWMutex
	client  api.Client
	started bool
	closed  bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	diffCh chan map[string]any

	listenersMu sync.Mutex
	listeners   map[chan Message]struct{}

	sessionMu     sync.Mutex
	sessions      map[string]api.TradeSession
	sessionCancel map[string]context.CancelFunc

	snapshotMu  sync.Mutex
	snapshot    map[string]any
	pendingDiff map[string]any

	subsMu sync.Mutex
	subs   []ioCloser
}

type ioCloser interface {
	Close() error
}

func New(cfg Config) *Adapter {
	cfg = cfg.normalize()
	return &Adapter{
		cfg:           cfg,
		diffCh:        make(chan map[string]any, cfg.OutBuffer),
		listeners:     map[chan Message]struct{}{},
		sessions:      map[string]api.TradeSession{},
		sessionCancel: map[string]context.CancelFunc{},
		snapshot:      map[string]any{},
		pendingDiff:   map[string]any{},
	}
}

func (a *Adapter) Attach(client api.Client) error {
	if client == nil {
		return fmt.Errorf("client is nil")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return fmt.Errorf("adapter already started")
	}
	a.client = client
	return nil
}

func (a *Adapter) AttachSession(session api.TradeSession) error {
	if session == nil {
		return fmt.Errorf("trade session is nil")
	}
	a.sessionMu.Lock()
	a.sessions[session.SessionID()] = session
	running := a.started && !a.closed
	ctx := a.ctx
	a.sessionMu.Unlock()
	if running {
		a.startSessionCollector(ctx, session)
	}
	return nil
}

func (a *Adapter) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return api.NewError(api.ErrDisconnected, "web adapter closed", nil)
	}
	if a.started {
		a.mu.Unlock()
		return nil
	}
	if a.client == nil {
		a.mu.Unlock()
		return fmt.Errorf("client is not attached")
	}
	a.started = true
	a.ctx, a.cancel = context.WithCancel(ctx)
	startCtx := a.ctx
	client := a.client
	a.mu.Unlock()

	initialSubs := a.currentSubscribedList()
	initial := BuildTqWebInitialData(client, a.cfg, initialSubs)
	a.snapshotMu.Lock()
	a.snapshot = cloneAnyMap(initial)
	a.pendingDiff = map[string]any{}
	a.snapshotMu.Unlock()

	a.wg.Add(1)
	go a.runDispatchLoop(startCtx)

	a.emitMessage(Message{Aid: "rtn_data", Data: []map[string]any{cloneAnyMap(initial)}, Mode: a.cfg.Mode, TS: time.Now().UnixNano()})

	a.startMarketCollectors(startCtx, client)
	a.startRuntimeCollector(startCtx, client)
	a.startAttachedSessions(startCtx)
	return nil
}

func (a *Adapter) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	cancel := a.cancel
	a.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	a.sessionMu.Lock()
	for _, fn := range a.sessionCancel {
		fn()
	}
	a.sessionCancel = map[string]context.CancelFunc{}
	a.sessionMu.Unlock()

	a.subsMu.Lock()
	subs := append([]ioCloser(nil), a.subs...)
	a.subs = nil
	a.subsMu.Unlock()
	for _, sub := range subs {
		_ = sub.Close()
	}

	a.wg.Wait()

	a.listenersMu.Lock()
	for ch := range a.listeners {
		close(ch)
	}
	a.listeners = map[chan Message]struct{}{}
	a.listenersMu.Unlock()
	return nil
}

func (a *Adapter) Subscribe(ctx context.Context) (<-chan Message, error) {
	ch := make(chan Message, 64)
	a.listenersMu.Lock()
	a.listeners[ch] = struct{}{}
	a.listenersMu.Unlock()

	if snap := a.Snapshot(); len(snap.Data) > 0 {
		ch <- snap
	}

	go func() {
		<-ctx.Done()
		a.listenersMu.Lock()
		if _, ok := a.listeners[ch]; ok {
			delete(a.listeners, ch)
			close(ch)
		}
		a.listenersMu.Unlock()
	}()
	return ch, nil
}

func (a *Adapter) Snapshot() Message {
	a.snapshotMu.Lock()
	defer a.snapshotMu.Unlock()
	return Message{Aid: "rtn_data", Data: []map[string]any{cloneAnyMap(a.snapshot)}, Mode: a.cfg.Mode, TS: time.Now().UnixNano()}
}

func (a *Adapter) DrainPendingDiff() map[string]any {
	a.snapshotMu.Lock()
	defer a.snapshotMu.Unlock()
	if len(a.pendingDiff) == 0 {
		return map[string]any{}
	}
	out := cloneAnyMap(a.pendingDiff)
	a.pendingDiff = map[string]any{}
	return out
}

// PushChartData emits tqwebhelper-compatible draw_chart_datas diff.
func (a *Adapter) PushChartData(symbol string, durNano int64, datas map[string]any) {
	if symbol == "" || len(datas) == 0 {
		return
	}
	a.publishDiff(map[string]any{
		"draw_chart_datas": map[string]any{
			symbol: map[string]any{
				strconv.FormatInt(durNano, 10): cloneAnyMap(datas),
			},
		},
	})
}

// PushReportData emits tqwebhelper-compatible draw_report_datas diff.
func (a *Adapter) PushReportData(report map[string]any) {
	if len(report) == 0 {
		return
	}
	a.publishDiff(map[string]any{"draw_report_datas": cloneAnyMap(report)})
}

// PublishDiff sends an arbitrary diff to all connected web UI clients.
func (a *Adapter) PublishDiff(diff map[string]any) {
	if len(diff) == 0 {
		return
	}
	a.publishDiff(cloneAnyMap(diff))
}

// UpdateSubscribed overwrites the subscribed list shown to web UI.
func (a *Adapter) UpdateSubscribed(subscribed []map[string]any) {
	a.publishDiff(map[string]any{"subscribed": cloneSubList(subscribed)})
}

func (a *Adapter) runDispatchLoop(ctx context.Context) {
	defer a.wg.Done()
	ticker := time.NewTicker(a.cfg.MergeWindow)
	defer ticker.Stop()
	var pendingEmit map[string]any
	for {
		select {
		case <-ctx.Done():
			// Flush any remaining pending diffs before exiting
			if pendingEmit != nil && len(pendingEmit) > 0 {
				a.emitMessage(Message{Aid: "rtn_data", Data: []map[string]any{cloneAnyMap(pendingEmit)}, Mode: a.cfg.Mode, TS: time.Now().UnixNano()})
			}
			return
		case diff := <-a.diffCh:
			if len(diff) == 0 {
				continue
			}
			// Always update snapshot immediately (so Snapshot() is fresh)
			a.snapshotMu.Lock()
			mergeDiff(a.snapshot, diff)
			mergeDiff(a.pendingDiff, diff)
			a.snapshotMu.Unlock()
			// Accumulate for batched emission
			if pendingEmit == nil {
				pendingEmit = map[string]any{}
			}
			mergeDiff(pendingEmit, diff)
		case <-ticker.C:
			if pendingEmit != nil && len(pendingEmit) > 0 {
				a.emitMessage(Message{Aid: "rtn_data", Data: []map[string]any{cloneAnyMap(pendingEmit)}, Mode: a.cfg.Mode, TS: time.Now().UnixNano()})
				pendingEmit = nil
			}
		}
	}
}

func (a *Adapter) emitMessage(msg Message) {
	a.listenersMu.Lock()
	defer a.listenersMu.Unlock()
	for ch := range a.listeners {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (a *Adapter) publishDiff(diff map[string]any) {
	if len(diff) == 0 {
		return
	}
	select {
	case a.diffCh <- cloneAnyMap(diff):
	default:
	}
}

func (a *Adapter) startMarketCollectors(ctx context.Context, client api.Client) {
	market := client.Market()
	if market == nil {
		return
	}

	connCh, err := market.ConnEvents(ctx)
	if err == nil {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-connCh:
					if !ok {
						return
					}
					diff, ok := EncodeAsTqWebDiff(ev)
					if ok {
						a.publishDiff(diff)
					}
				}
			}
		}()
	}

	if len(a.cfg.Symbols) == 0 {
		return
	}
	quoteSub, err := market.SubscribeQuotes(ctx, a.cfg.Symbols...)
	if err == nil {
		a.trackSub(quoteSub)
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-quoteSub.C():
					if !ok {
						return
					}
					if diff, ok := EncodeAsTqWebDiff(ev); ok {
						a.publishDiff(diff)
					}
				}
			}
		}()
	}

	for _, dur := range a.cfg.KlineDurations {
		if dur <= 0 {
			continue
		}
		sub, err := market.SubscribeKlines(ctx, a.cfg.Symbols, dur, a.cfg.KlineDataLength)
		if err != nil {
			continue
		}
		a.trackSub(sub)
		a.wg.Add(1)
		go func(sub api.KlineSub) {
			defer a.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-sub.C():
					if !ok {
						return
					}
					if diff, ok := EncodeAsTqWebDiff(ev); ok {
						a.publishDiff(diff)
					}
				}
			}
		}(sub)
	}

	a.publishDiff(map[string]any{"subscribed": a.currentSubscribedList()})
}

func (a *Adapter) startRuntimeCollector(ctx context.Context, client api.Client) {
	market := client.Market()
	if market == nil {
		return
	}
	provider, ok := market.(RuntimeStatusProvider)
	if !ok {
		return
	}
	if snap := provider.RuntimeStatusSnapshot(); len(snap) > 0 {
		a.publishDiff(snap)
	}
	ch, err := provider.SubscribeRuntimeStatus(ctx)
	if err != nil {
		return
	}
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case diff, ok := <-ch:
				if !ok {
					return
				}
				a.publishDiff(diff)
			}
		}
	}()
}

func (a *Adapter) startAttachedSessions(ctx context.Context) {
	a.sessionMu.Lock()
	sessions := make([]api.TradeSession, 0, len(a.sessions))
	for _, s := range a.sessions {
		sessions = append(sessions, s)
	}
	a.sessionMu.Unlock()
	for _, s := range sessions {
		a.startSessionCollector(ctx, s)
	}
}

func (a *Adapter) startSessionCollector(ctx context.Context, session api.TradeSession) {
	a.sessionMu.Lock()
	if _, ok := a.sessionCancel[session.SessionID()]; ok {
		a.sessionMu.Unlock()
		return
	}
	sCtx, cancel := context.WithCancel(ctx)
	a.sessionCancel[session.SessionID()] = cancel
	a.sessionMu.Unlock()

	a.publishDiff(map[string]any{
		"action": map[string]any{
			"accounts": map[string]any{session.AccountID(): map[string]any{
				"td_url_status": true,
				"account_type":  "FUTURE",
			}},
		},
	})

	if connCh, err := session.ConnEvents(sCtx); err == nil {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			for {
				select {
				case <-sCtx.Done():
					return
				case ev, ok := <-connCh:
					if !ok {
						return
					}
					a.publishDiff(map[string]any{
						"action": map[string]any{
							"accounts": map[string]any{
								session.AccountID(): map[string]any{"td_url_status": ev.State == "ready"},
							},
						},
					})
				}
			}
		}()
	}

	if sub, err := session.SubscribeAccounts(sCtx); err == nil {
		a.trackSub(sub)
		a.wg.Add(1)
		go func(sub api.AccountSub) {
			defer a.wg.Done()
			for {
				select {
				case <-sCtx.Done():
					return
				case ev, ok := <-sub.C():
					if !ok {
						return
					}
					if diff, ok := EncodeAsTqWebDiff(ev); ok {
						a.publishDiff(diff)
						a.publishDiff(buildSnapshotDiff(session.AccountID(), ev.Account))
					}
				}
			}
		}(sub)
	}

	if sub, err := session.SubscribePositions(sCtx); err == nil {
		a.trackSub(sub)
		a.wg.Add(1)
		go a.runPosCollector(sCtx, sub)
	}
	if sub, err := session.SubscribeOrders(sCtx); err == nil {
		a.trackSub(sub)
		a.wg.Add(1)
		go a.runOrdCollector(sCtx, sub)
	}
	if sub, err := session.SubscribeTrades(sCtx); err == nil {
		a.trackSub(sub)
		a.wg.Add(1)
		go a.runTradeCollector(sCtx, sub)
	}
	if sub, err := session.SubscribeNotifies(sCtx); err == nil {
		a.trackSub(sub)
		a.wg.Add(1)
		go a.runNotifyCollector(sCtx, sub)
	}
}

func (a *Adapter) runPosCollector(ctx context.Context, sub api.PositionSub) {
	defer a.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.C():
			if !ok {
				return
			}
			if diff, ok := EncodeAsTqWebDiff(ev); ok {
				a.publishDiff(diff)
			}
		}
	}
}

func (a *Adapter) runOrdCollector(ctx context.Context, sub api.OrderSub) {
	defer a.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.C():
			if !ok {
				return
			}
			if diff, ok := EncodeAsTqWebDiff(ev); ok {
				a.publishDiff(diff)
			}
		}
	}
}

func (a *Adapter) runTradeCollector(ctx context.Context, sub api.TradeSub) {
	defer a.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.C():
			if !ok {
				return
			}
			if diff, ok := EncodeAsTqWebDiff(ev); ok {
				a.publishDiff(diff)
			}
		}
	}
}

func (a *Adapter) runNotifyCollector(ctx context.Context, sub api.NotifySub) {
	defer a.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.C():
			if !ok {
				return
			}
			if diff, ok := EncodeAsTqWebDiff(ev); ok {
				a.publishDiff(diff)
			}
		}
	}
}

func (a *Adapter) trackSub(sub ioCloser) {
	a.subsMu.Lock()
	a.subs = append(a.subs, sub)
	a.subsMu.Unlock()
}

func (a *Adapter) currentSubscribedList() []map[string]any {
	items := make([]map[string]any, 0, len(a.cfg.Symbols)*(len(a.cfg.KlineDurations)+1))
	// Kline subscriptions first — the frontend reads subscribed[0].dur_nano
	// to determine the initial chart duration.
	for _, s := range a.cfg.Symbols {
		for _, d := range a.cfg.KlineDurations {
			items = append(items, map[string]any{"symbol": s, "dur_nano": int64(d) * int64(time.Second)})
		}
	}
	for _, s := range a.cfg.Symbols {
		items = append(items, map[string]any{"symbol": s})
	}
	return items
}

func buildSnapshotDiff(accountID string, acc api.Account) map[string]any {
	ts := strconv.FormatInt(time.Now().UnixNano(), 10)
	return map[string]any{
		"snapshots": map[string]any{
			ts: map[string]any{
				"account_id": accountID,
				"accounts":   map[string]any{"CNY": structToMap(acc)},
			},
		},
	}
}

func mergeDiff(dst, src map[string]any) {
	for k, v := range src {
		if vMap, ok := v.(map[string]any); ok {
			cur, ok := dst[k].(map[string]any)
			if !ok {
				cur = map[string]any{}
				dst[k] = cur
			}
			mergeDiff(cur, vMap)
			continue
		}
		dst[k] = v
	}
}

func cloneSubList(in []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, item := range in {
		out = append(out, cloneAnyMap(item))
	}
	return out
}
