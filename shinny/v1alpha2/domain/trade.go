package domain

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/infra"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/store"
)

const defaultTradingStatusURL = "wss://trading-status.shinnytech.com/status"

type tradeService struct {
	auth    api.AuthService
	store   *store.TradeStore
	tsStore *store.TradingStatusStore
	autoAdd bool
	market  api.MarketService

	mu               sync.RWMutex
	sessions         map[string]*tradeSession
	sessionsByAccID  map[string]*tradeSession
	tradingStatusHub *tradingStatusHub
}

func NewTradeService(auth api.AuthService, st *store.TradeStore, autoAdd bool, market ...api.MarketService) api.TradeService {
	if st == nil {
		st = store.NewTradeStore()
	}
	tsStore := store.NewTradingStatusStore()
	var mkt api.MarketService
	if len(market) > 0 {
		mkt = market[0]
	}
	svc := &tradeService{
		auth:            auth,
		store:           st,
		tsStore:         tsStore,
		autoAdd:         autoAdd,
		market:          mkt,
		sessions:        map[string]*tradeSession{},
		sessionsByAccID: map[string]*tradeSession{},
	}
	svc.tradingStatusHub = newTradingStatusHub(auth, tsStore, defaultTradingStatusURL, buildOptionResolver(mkt))
	return svc
}

func (s *tradeService) Start(ctx context.Context) error {
	_ = ctx
	return nil
}

func (s *tradeService) Login(ctx context.Context, req api.TradeLoginReq) (api.TradeSession, error) {
	req.AccountID = strings.TrimSpace(req.AccountID)
	req.BrokerID = strings.TrimSpace(req.BrokerID)
	req.UserName = strings.TrimSpace(req.UserName)
	req.Password = strings.TrimSpace(req.Password)
	if req.FrontURL != nil {
		v := strings.TrimSpace(*req.FrontURL)
		if v == "" {
			req.FrontURL = nil
		} else {
			req.FrontURL = &v
		}
	}

	if req.AccountID == "" {
		return nil, api.NewError(api.ErrInvalidAccount, "empty account_id", nil)
	}
	if req.BrokerID == "" {
		return nil, api.NewError(api.ErrInvalidArgument, "empty broker_id", nil)
	}
	if req.UserName == "" {
		return nil, api.NewError(api.ErrInvalidArgument, "empty user_name", nil)
	}
	if req.Password == "" {
		return nil, api.NewError(api.ErrInvalidArgument, "empty password", nil)
	}

	if _, ok := s.auth.Session(); !ok {
		return nil, api.NewError(api.ErrAuthRequired, "auth session required before trade login", nil)
	}
	if err := s.auth.EnsureAccountGrant(ctx, req.AccountID, s.autoAdd); err != nil {
		return nil, err
	}

	s.mu.Lock()
	if old, ok := s.sessionsByAccID[req.AccountID]; ok {
		s.mu.Unlock()
		if old.Ready() {
			return old, nil
		}
		_ = old.Close(context.Background())
		s.mu.Lock()
	}

	wsURL := ""
	if req.FrontURL != nil {
		wsURL = strings.TrimSpace(*req.FrontURL)
	}
	if wsURL == "" {
		route, err := s.auth.ResolveTDURL(ctx, req.BrokerID, req.AccountID)
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		wsURL = strings.TrimSpace(route.URL)
	}
	if wsURL == "" {
		s.mu.Unlock()
		return nil, api.NewError(api.ErrInvalidArgument, "empty td websocket url", nil)
	}

	sessionID := genTradeID("GO_td")
	ts := newTradeSession(s, sessionID, req, wsURL)
	s.sessions[sessionID] = ts
	s.sessionsByAccID[req.AccountID] = ts
	s.store.TouchSession(sessionID, req.AccountID)
	s.mu.Unlock()

	ts.start()
	if err := ts.waitReady(ctx); err != nil {
		_ = ts.Close(context.Background())
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, api.NewError(api.ErrLoginTimeout, "trade login timeout", err)
		}
		var se *api.SDKError
		if errors.As(err, &se) && se.Code == api.ErrLoginRejected {
			return nil, err
		}
		return nil, api.NewError(api.ErrLoginTimeout, "trade login failed before ready", err)
	}
	return ts, nil
}

func (s *tradeService) Session(sessionID string) (api.TradeSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ts, ok := s.sessions[sessionID]
	if !ok || ts.isClosed() {
		return nil, false
	}
	return ts, true
}

func (s *tradeService) GetTradingStatus(ctx context.Context, symbol string) (api.TradingStatus, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return api.TradingStatus{}, api.NewError(api.ErrInvalidSymbol, "empty symbol", nil)
	}
	if err := s.auth.EnsureFeature("tq_trading_status"); err != nil {
		return api.TradingStatus{}, err
	}
	if err := s.tradingStatusHub.ensureStarted(ctx); err != nil {
		return api.TradingStatus{}, err
	}
	s.tradingStatusHub.addSymbols(ctx, []string{symbol})

	for {
		if st, ok := s.tsStore.Get(symbol); ok && st.TradeStatus != "" {
			return st, nil
		}
		select {
		case <-ctx.Done():
			return api.TradingStatus{}, api.NewError(api.ErrTradingStatusTimeout, "get trading status timeout", ctx.Err())
		case <-s.tradingStatusHub.upSignal:
		}
	}
}

func (s *tradeService) TradingStatus(symbol string) (api.TradingStatus, bool) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return api.TradingStatus{}, false
	}
	return s.tsStore.Get(symbol)
}

func (s *tradeService) SubscribeTradingStatuses(ctx context.Context, symbols ...string) (api.TradingStatusSub, error) {
	if err := s.auth.EnsureFeature("tq_trading_status"); err != nil {
		return nil, err
	}
	if err := s.tradingStatusHub.ensureStarted(ctx); err != nil {
		return nil, err
	}
	clean := normalizeSymbols(symbols)
	if len(clean) == 0 {
		return nil, api.NewError(api.ErrInvalidSymbol, "empty symbols", nil)
	}
	return s.tradingStatusHub.subscribe(ctx, clean)
}

func (s *tradeService) Close() error {
	s.mu.RLock()
	sessions := make([]*tradeSession, 0, len(s.sessions))
	for _, ss := range s.sessions {
		sessions = append(sessions, ss)
	}
	hub := s.tradingStatusHub
	s.mu.RUnlock()
	for _, ss := range sessions {
		_ = ss.Close(context.Background())
	}
	if hub != nil {
		_ = hub.Close()
	}
	return nil
}

func (s *tradeService) dropSession(sessionID string, accountID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
	if cur, ok := s.sessionsByAccID[accountID]; ok && cur.id == sessionID {
		delete(s.sessionsByAccID, accountID)
	}
	s.store.DropSession(sessionID)
}

type tradeSession struct {
	svc *tradeService
	id  string
	req api.TradeLoginReq
	url string

	risk *RiskManager

	mu       sync.RWMutex
	state    api.TradeSessionState
	ready    bool
	closed   bool
	loginErr error

	runCtx context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	ws *infra.WSManager

	upSignal chan struct{}
	recMu    sync.Mutex
	recov    bool
	shadow   *store.TradeStore
	pending  []map[string]any

	eventCh  chan api.TradeSessionEvent
	notifyCh chan api.NotifyEvent

	subsMu       sync.Mutex
	connSubs     map[chan api.ConnEvent]struct{}
	accountSubs  map[chan api.AccountEvent]struct{}
	positionSubs map[chan api.PositionEvent]struct{}
	orderSubs    map[chan api.OrderEvent]struct{}
	tradeSubs    map[chan api.TradeFillEvent]struct{}
	notifySubs   map[chan api.NotifyEvent]struct{}

	refsMu     sync.Mutex
	orderRefs  map[string]map[*orderRef]struct{}
	firstReady bool

	riskBootstrapped bool
}

func newTradeSession(svc *tradeService, id string, req api.TradeLoginReq, wsURL string) *tradeSession {
	runCtx, cancel := context.WithCancel(context.Background())
	return &tradeSession{
		svc:          svc,
		id:           id,
		req:          req,
		url:          wsURL,
		risk:         NewRiskManager(),
		state:        api.TradeSessionConnecting,
		runCtx:       runCtx,
		cancel:       cancel,
		upSignal:     make(chan struct{}, 1),
		eventCh:      make(chan api.TradeSessionEvent, 64),
		notifyCh:     make(chan api.NotifyEvent, 128),
		connSubs:     map[chan api.ConnEvent]struct{}{},
		accountSubs:  map[chan api.AccountEvent]struct{}{},
		positionSubs: map[chan api.PositionEvent]struct{}{},
		orderSubs:    map[chan api.OrderEvent]struct{}{},
		tradeSubs:    map[chan api.TradeFillEvent]struct{}{},
		notifySubs:   map[chan api.NotifyEvent]struct{}{},
		orderRefs:    map[string]map[*orderRef]struct{}{},
	}
}

func (s *tradeSession) start() {
	s.wg.Add(1)
	go s.run()
}

func (s *tradeSession) run() {
	defer s.wg.Done()
	defer func() {
		if s.ws != nil {
			_ = s.ws.Close()
		}
		s.setState(api.TradeSessionClosed, s.loginErr)
		s.svc.dropSession(s.id, s.req.AccountID)
		s.closeAllChannels()
	}()

	first := true
	s.ws = infra.NewWSManager(
		s.url,
		func() http.Header { return s.svc.auth.Header() },
		infra.WSCallbacks{
			OnEvent: func(ev infra.WSEvent) {
				switch ev.State {
				case infra.WSStateConnecting, infra.WSStateReconnecting:
					s.emitConnEvent(api.ConnEvent{State: "connecting", Err: ev.Err, At: ev.At})
					if !first {
						s.enterRecovery()
						s.setState(api.TradeSessionRecovering, nil)
						s.markReady(false)
					} else {
						s.setState(api.TradeSessionConnecting, nil)
					}
				case infra.WSStateConnected:
					s.emitConnEvent(api.ConnEvent{State: "ready", At: ev.At})
					if first {
						s.setState(api.TradeSessionWsReady, nil)
						s.setState(api.TradeSessionLoggingIn, nil)
					} else {
						s.setState(api.TradeSessionRecovering, nil)
						s.markReady(false)
					}
				case infra.WSStateDisconnected:
					s.emitConnEvent(api.ConnEvent{State: "disconnected", Err: ev.Err, At: ev.At})
					if first {
						s.setLoginErr(api.NewError(api.ErrLoginRejected, "trade ws dial failed", ev.Err))
						s.cancel()
					}
				}
			},
			OnConnect: func(ctx context.Context) error {
				if err := s.sendLoginPacks(); err != nil {
					return err
				}
				first = false
				return s.sendPack(map[string]any{"aid": "peek_message"})
			},
			OnMessage: func(ctx context.Context, pack map[string]any) error {
				_ = ctx
				if s.isRecovering() {
					s.handleMessageRecover(pack)
					return s.sendPack(map[string]any{"aid": "peek_message"})
				}
				if err := s.handleMessage(pack); err != nil {
					if isSDKCode(err, api.ErrLoginRejected) {
						s.setLoginErr(err)
						s.cancel()
						return nil
					}
					return err
				}
				return s.sendPack(map[string]any{"aid": "peek_message"})
			},
		},
		nil,
	)
	if err := s.ws.Start(s.runCtx); err != nil {
		s.setLoginErr(api.NewError(api.ErrDisconnected, "start trade ws manager failed", err))
		return
	}
	<-s.runCtx.Done()
}

func (s *tradeSession) handleMessage(pack map[string]any) error {
	aid, _ := pack["aid"].(string)
	switch aid {
	case "rsp_login":
		if err := parseLoginReject(pack); err != nil {
			wrapped := api.NewError(api.ErrLoginRejected, "trade login rejected", err)
			s.setLoginErr(wrapped)
			return wrapped
		}
		return nil
	case "rtn_data":
		items, _ := pack["data"].([]any)
		if len(items) == 0 {
			return nil
		}
		for _, one := range items {
			dm, ok := one.(map[string]any)
			if !ok {
				continue
			}
			s.handleNotifyDiff(dm)
			s.handleTradeDiff(dm)
		}
		return nil
	default:
		return nil
	}
}

func (s *tradeSession) handleNotifyDiff(diff map[string]any) {
	node, ok := diff["notify"].(map[string]any)
	if !ok {
		return
	}
	for _, raw := range node {
		nm, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ev := api.NotifyEvent{
			AccountID: s.req.AccountID,
			ConnID:    asStringLoose(nm["conn_id"]),
			Type:      asStringLoose(nm["type"]),
			Code:      asInt64Loose(nm["code"]),
			Content:   asStringLoose(nm["content"]),
			URL:       asStringLoose(nm["url"]),
			ArrivedAt: time.Now(),
		}
		switch strings.ToUpper(asStringLoose(nm["level"])) {
		case "ERROR":
			ev.Level = api.NotifyError
		case "WARNING":
			ev.Level = api.NotifyWarning
		default:
			ev.Level = api.NotifyInfo
		}
		s.notifyCh <- ev
		s.dispatchNotifyEvent(ev)
	}
}

func (s *tradeSession) handleTradeDiff(diff map[string]any) {
	justReady := s.applyTradeDiff(s.svc.store, diff, true, true, true)
	if justReady {
		s.bootstrapRiskFromStore()
		s.setState(api.TradeSessionReady, nil)
		s.emitSnapshotEvents()
	}
}

func (s *tradeSession) applyTradeDiff(dst *store.TradeStore, diff map[string]any, allowReadyTransition bool, dispatch bool, applyRisk bool) bool {
	tradeNode, ok := diff["trade"].(map[string]any)
	if !ok {
		return false
	}

	justReady := false
	var tradingDay string
	for _, rawAcc := range tradeNode {
		accNode, ok := rawAcc.(map[string]any)
		if !ok {
			continue
		}
		if td := strings.TrimSpace(asStringLoose(accNode["trading_day"])); td != "" {
			tradingDay = td
		}

		if v, ok := asBoolLooseOk(accNode["trade_more_data"]); ok {
			dst.SetTradeMoreData(s.id, s.req.AccountID, v)
			if allowReadyTransition && !v && !s.Ready() {
				s.markReady(true)
				justReady = true
			}
		}

		if accounts, ok := accNode["accounts"].(map[string]any); ok {
			for _, raw := range accounts {
				m, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if td := strings.TrimSpace(asStringLoose(m["trading_day"])); td != "" {
					tradingDay = td
				}
				var a api.Account
				decodeInto(m, &a)
				dst.UpsertAccount(s.id, s.req.AccountID, a)
				if dispatch {
					s.dispatchAccountEvent(api.AccountEvent{Kind: api.TradeEventUpsert, AccountID: s.req.AccountID, Account: a, ChangedAt: time.Now()})
				}
				break
			}
		}

		if positions, ok := accNode["positions"].(map[string]any); ok {
			for symbol, raw := range positions {
				if raw == nil {
					dst.DeletePosition(s.id, s.req.AccountID, symbol)
					if dispatch {
						s.dispatchPositionEvent(api.PositionEvent{Kind: api.TradeEventDelete, AccountID: s.req.AccountID, Symbol: symbol, ChangedAt: time.Now()})
					}
					continue
				}
				m, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				var p api.Position
				decodeInto(m, &p)
				dst.UpsertPosition(s.id, s.req.AccountID, symbol, p)
				if dispatch {
					s.dispatchPositionEvent(api.PositionEvent{Kind: api.TradeEventUpsert, AccountID: s.req.AccountID, Symbol: symbol, Position: p, ChangedAt: time.Now()})
				}
			}
		}

		if orders, ok := accNode["orders"].(map[string]any); ok {
			for orderID, raw := range orders {
				if raw == nil {
					dst.DeleteOrder(s.id, s.req.AccountID, orderID)
					if dispatch {
						ev := api.OrderEvent{Kind: api.TradeEventDelete, AccountID: s.req.AccountID, OrderID: orderID, ChangedAt: time.Now()}
						s.dispatchOrderEvent(ev)
						s.dispatchOrderRef(ev)
					}
					continue
				}
				m, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				var o api.Order
				decodeInto(m, &o)
				if o.OrderID == "" {
					o.OrderID = orderID
				}
				s.risk.BindOrderExchange(orderID, o.ExchangeID)
				dst.UpsertOrder(s.id, s.req.AccountID, orderID, o)
				if dispatch {
					ev := api.OrderEvent{Kind: api.TradeEventUpsert, AccountID: s.req.AccountID, OrderID: orderID, Order: o, ChangedAt: time.Now()}
					s.dispatchOrderEvent(ev)
					s.dispatchOrderRef(ev)
				}
			}
		}

		if trades, ok := accNode["trades"].(map[string]any); ok {
			for tradeID, raw := range trades {
				if raw == nil {
					dst.DeleteTrade(s.id, s.req.AccountID, tradeID)
					if dispatch {
						s.dispatchTradeFillEvent(api.TradeFillEvent{Kind: api.TradeEventDelete, AccountID: s.req.AccountID, TradeID: tradeID, ChangedAt: time.Now()})
					}
					continue
				}
				m, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				var t api.Trade
				decodeInto(m, &t)
				if t.TradeID == "" {
					t.TradeID = tradeID
				}
				dst.UpsertTrade(s.id, s.req.AccountID, tradeID, t)
				if dispatch {
					s.dispatchTradeFillEvent(api.TradeFillEvent{Kind: api.TradeEventUpsert, AccountID: s.req.AccountID, TradeID: tradeID, Trade: t, ChangedAt: time.Now()})
				}
			}
		}

		if rules, ok := accNode["risk_management_rule"].(map[string]any); ok {
			for exchangeID, raw := range rules {
				m, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				var rr api.RiskManagementRule
				decodeInto(m, &rr)
				if rr.ExchangeID == "" {
					rr.ExchangeID = exchangeID
				}
				dst.UpsertRiskRule(s.id, s.req.AccountID, exchangeID, rr)
			}
		}

		if rds, ok := accNode["risk_management_data"].(map[string]any); ok {
			for symbol, raw := range rds {
				m, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				var rd api.RiskManagementData
				decodeInto(m, &rd)
				if rd.InstrumentID == "" {
					rd.InstrumentID = symbol
				}
				dst.UpsertRiskData(s.id, s.req.AccountID, symbol, rd)
			}
		}
	}
	if applyRisk && tradingDay != "" {
		s.risk.OnRecvDiff(api.TradeDiffView{
			SessionID:  s.id,
			AccountID:  s.req.AccountID,
			TradingDay: tradingDay,
			Diff:       diff,
		})
	}
	return justReady
}

func (s *tradeSession) handleMessageRecover(pack map[string]any) {
	aid, _ := pack["aid"].(string)
	if aid != "rtn_data" {
		return
	}
	items, _ := pack["data"].([]any)
	if len(items) == 0 {
		return
	}
	s.recMu.Lock()
	if !s.recov {
		s.recMu.Unlock()
		_ = s.handleMessage(pack)
		return
	}
	if s.shadow == nil {
		s.shadow = store.NewTradeStore()
	}
	for _, one := range items {
		dm, ok := one.(map[string]any)
		if !ok {
			continue
		}
		s.handleNotifyDiff(dm)
		s.pending = append(s.pending, dm)
		_ = s.applyTradeDiff(s.shadow, dm, false, false, false)
	}
	if !s.recoveryReadyLocked() {
		s.recMu.Unlock()
		return
	}
	pending := s.pending
	s.pending = nil
	s.shadow = nil
	s.recov = false
	s.recMu.Unlock()

	for _, diff := range pending {
		_ = s.applyTradeDiff(s.svc.store, diff, false, false, true)
	}
	s.markReady(true)
	s.bootstrapRiskFromStore()
	s.setState(api.TradeSessionReady, nil)
	s.emitSnapshotEvents()
}

func (s *tradeSession) bootstrapRiskFromStore() {
	s.mu.Lock()
	if s.riskBootstrapped {
		s.mu.Unlock()
		return
	}
	s.riskBootstrapped = true
	s.mu.Unlock()

	ss, ok := s.svc.store.SessionSnapshot(s.id)
	if !ok {
		return
	}
	s.risk.Bootstrap(ss.Orders, ss.Trades)
}

func (s *tradeSession) enterRecovery() {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	s.recov = true
	s.shadow = store.NewTradeStore()
	s.pending = nil
}

func (s *tradeSession) isRecovering() bool {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	return s.recov
}

func (s *tradeSession) recoveryReadyLocked() bool {
	if s.shadow == nil {
		return false
	}
	ss, ok := s.shadow.SessionSnapshot(s.id)
	if !ok {
		return false
	}
	return !ss.TradeMoreData
}

func (s *tradeSession) sendLoginPacks() error {
	pack := map[string]any{
		"aid":       "req_login",
		"bid":       s.req.BrokerID,
		"user_name": s.req.UserName,
		"password":  s.req.Password,
	}
	if s.req.FrontURL != nil && strings.TrimSpace(*s.req.FrontURL) != "" {
		pack["front"] = strings.TrimSpace(*s.req.FrontURL)
		pack["broker_id"] = s.req.BrokerID
	}
	if err := s.sendPack(pack); err != nil {
		return err
	}
	if err := s.sendPack(map[string]any{"aid": "confirm_settlement"}); err != nil {
		return err
	}
	return nil
}

func (s *tradeSession) sendPack(pack map[string]any) error {
	if s.ws == nil {
		return api.NewError(api.ErrDisconnected, "trade ws disconnected", nil)
	}
	if err := s.ws.Send(pack); err != nil {
		return api.NewError(api.ErrDisconnected, "trade ws write failed", err)
	}
	return nil
}

func (s *tradeSession) waitReady(ctx context.Context) error {
	for {
		s.mu.RLock()
		ready := s.ready
		closed := s.closed
		err := s.loginErr
		s.mu.RUnlock()

		if ready {
			return nil
		}
		if err != nil {
			return err
		}
		if closed {
			return api.NewError(api.ErrDisconnected, "trade session closed", nil)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.upSignal:
		}
	}
}

func (s *tradeSession) setState(state api.TradeSessionState, cause error) {
	s.mu.Lock()
	if s.state == state {
		s.mu.Unlock()
		return
	}
	s.state = state
	s.mu.Unlock()

	ev := api.TradeSessionEvent{SessionID: s.id, AccountID: s.req.AccountID, State: state, Err: cause, ChangedAt: time.Now()}
	select {
	case s.eventCh <- ev:
	default:
	}
	s.wakeWaiters()
}

func (s *tradeSession) setLoginErr(err error) {
	s.mu.Lock()
	if s.loginErr == nil {
		s.loginErr = err
	}
	s.mu.Unlock()
	s.wakeWaiters()
}

func (s *tradeSession) markReady(v bool) {
	s.mu.Lock()
	s.ready = v
	if v {
		s.firstReady = true
	}
	s.mu.Unlock()
	s.wakeWaiters()
}

func (s *tradeSession) wakeWaiters() {
	select {
	case s.upSignal <- struct{}{}:
	default:
	}
}

func (s *tradeSession) emitConnEvent(ev api.ConnEvent) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for ch := range s.connSubs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (s *tradeSession) dispatchAccountEvent(ev api.AccountEvent) {
	if !s.Ready() {
		return
	}
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for ch := range s.accountSubs {
		ch <- ev
	}
}

func (s *tradeSession) dispatchPositionEvent(ev api.PositionEvent) {
	if !s.Ready() {
		return
	}
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for ch := range s.positionSubs {
		ch <- ev
	}
}

func (s *tradeSession) dispatchOrderEvent(ev api.OrderEvent) {
	if !s.Ready() {
		return
	}
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for ch := range s.orderSubs {
		ch <- ev
	}
}

func (s *tradeSession) dispatchTradeFillEvent(ev api.TradeFillEvent) {
	if !s.Ready() {
		return
	}
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for ch := range s.tradeSubs {
		ch <- ev
	}
}

func (s *tradeSession) dispatchNotifyEvent(ev api.NotifyEvent) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for ch := range s.notifySubs {
		ch <- ev
	}
}

func (s *tradeSession) emitSnapshotEvents() {
	snap, ok := s.svc.store.SessionSnapshot(s.id)
	if !ok {
		return
	}
	if snap.AccountOK {
		s.dispatchAccountEvent(api.AccountEvent{Kind: api.TradeEventSnapshot, AccountID: s.req.AccountID, Account: snap.Account, ChangedAt: time.Now()})
	}
	for symbol, p := range snap.Positions {
		s.dispatchPositionEvent(api.PositionEvent{Kind: api.TradeEventSnapshot, AccountID: s.req.AccountID, Symbol: symbol, Position: p, ChangedAt: time.Now()})
	}
	for orderID, o := range snap.Orders {
		ev := api.OrderEvent{Kind: api.TradeEventSnapshot, AccountID: s.req.AccountID, OrderID: orderID, Order: o, ChangedAt: time.Now()}
		s.dispatchOrderEvent(ev)
		s.dispatchOrderRef(ev)
	}
	for tradeID, t := range snap.Trades {
		s.dispatchTradeFillEvent(api.TradeFillEvent{Kind: api.TradeEventSnapshot, AccountID: s.req.AccountID, TradeID: tradeID, Trade: t, ChangedAt: time.Now()})
	}
}

func (s *tradeSession) dispatchOrderRef(ev api.OrderEvent) {
	s.refsMu.Lock()
	defer s.refsMu.Unlock()
	for ref := range s.orderRefs[ev.OrderID] {
		ref.push(ev)
	}
}

func (s *tradeSession) SessionID() string { return s.id }
func (s *tradeSession) AccountID() string { return s.req.AccountID }

func (s *tradeSession) State() api.TradeSessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (s *tradeSession) Ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready
}

func (s *tradeSession) Events() <-chan api.TradeSessionEvent { return s.eventCh }

func (s *tradeSession) NotifyEvents() <-chan api.NotifyEvent { return s.notifyCh }

func (s *tradeSession) InsertOrder(ctx context.Context, symbol string, direction api.Direction, volume int, opts ...api.InsertOrderOption) (api.OrderRef, error) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return nil, api.NewError(api.ErrInvalidSymbol, "empty symbol", nil)
	}
	parts := strings.SplitN(symbol, ".", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return nil, api.NewError(api.ErrInvalidSymbol, "invalid symbol format", nil)
	}
	exchangeID := strings.ToUpper(strings.TrimSpace(parts[0]))
	instrumentID := strings.TrimSpace(parts[1])
	if direction != api.DirectionBuy && direction != api.DirectionSell {
		return nil, api.NewError(api.ErrInvalidDirection, "invalid direction", nil)
	}
	if volume <= 0 {
		return nil, api.NewError(api.ErrInvalidVolume, "volume must be > 0", nil)
	}
	if err := s.svc.auth.EnsureTDGrants(symbol); err != nil {
		return nil, err
	}

	op := api.ResolveInsertOrderOptions(opts...)
	if op.Offset != api.OffsetOpen && op.Offset != api.OffsetClose && op.Offset != api.OffsetCloseToday {
		return nil, api.NewError(api.ErrInvalidOffset, "invalid offset", nil)
	}
	if op.Advanced != api.AdvancedNone && op.Advanced != api.AdvancedFAK && op.Advanced != api.AdvancedFOK {
		return nil, api.NewError(api.ErrUnsupportedAdvanced, "invalid advanced type", nil)
	}
	if op.PriceMode != "" && op.PriceMode != "LIMIT" && op.PriceMode != "BEST" && op.PriceMode != "FIVELEVEL" {
		return nil, api.NewError(api.ErrUnsupportedPriceMode, "unsupported price mode", nil)
	}
	if (op.PriceMode == "BEST" || op.PriceMode == "FIVELEVEL") && exchangeID != "CFFEX" {
		return nil, api.NewError(api.ErrUnsupportedPriceMode, "BEST/FIVELEVEL only supported on CFFEX", nil)
	}
	if (op.PriceMode == "BEST" || op.PriceMode == "FIVELEVEL") && op.Advanced == api.AdvancedFOK {
		return nil, api.NewError(api.ErrUnsupportedAdvanced, "FOK cannot be used with BEST/FIVELEVEL", nil)
	}

	checkReq := api.InsertOrderCheckReq{
		AccountID:  s.req.AccountID,
		ExchangeID: exchangeID,
		Symbol:     symbol,
		Direction:  direction,
		Offset:     op.Offset,
		Volume:     volume,
	}
	if err := s.risk.BeforeInsert(checkReq); err != nil {
		return nil, err
	}

	orderID := strings.TrimSpace(op.OrderID)
	if orderID == "" {
		orderID = genTradeID("GO_ord")
	}
	pack := map[string]any{
		"aid":           "insert_order",
		"user_id":       s.req.AccountID,
		"order_id":      orderID,
		"exchange_id":   exchangeID,
		"instrument_id": instrumentID,
		"direction":     string(direction),
		"offset":        string(op.Offset),
		"volume":        volume,
	}
	if op.PriceMode == "BEST" || op.PriceMode == "FIVELEVEL" {
		pack["price_type"] = op.PriceMode
		pack["time_condition"] = "IOC"
	} else if op.LimitPrice == nil {
		pack["price_type"] = "ANY"
		pack["time_condition"] = "IOC"
	} else {
		pack["price_type"] = "LIMIT"
		pack["limit_price"] = *op.LimitPrice
		if op.Advanced == api.AdvancedNone {
			pack["time_condition"] = "GFD"
		} else {
			pack["time_condition"] = "IOC"
		}
	}
	if op.Advanced == api.AdvancedFOK {
		pack["volume_condition"] = "ALL"
	} else {
		pack["volume_condition"] = "ANY"
	}

	if err := s.sendPack(pack); err != nil {
		return nil, err
	}
	s.risk.AfterInsert(checkReq)

	ref := &orderRef{
		session: s,
		orderID: orderID,
		evCh:    make(chan api.OrderEvent, 64),
	}
	s.refsMu.Lock()
	if s.orderRefs[orderID] == nil {
		s.orderRefs[orderID] = map[*orderRef]struct{}{}
	}
	s.orderRefs[orderID][ref] = struct{}{}
	s.refsMu.Unlock()

	return ref, nil
}

func (s *tradeSession) CancelOrder(ctx context.Context, orderID string) error {
	_ = ctx
	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return api.NewError(api.ErrOrderNotFound, "empty order id", nil)
	}
	exchangeID := ""
	if o, ok := s.svc.store.OrderSnapshot(s.id, orderID); ok {
		exchangeID = o.ExchangeID
	}
	checkReq := api.CancelOrderCheckReq{AccountID: s.req.AccountID, OrderID: orderID, ExchangeID: exchangeID}
	if err := s.risk.BeforeCancel(checkReq); err != nil {
		return err
	}
	if err := s.sendPack(map[string]any{
		"aid":      "cancel_order",
		"user_id":  s.req.AccountID,
		"order_id": orderID,
	}); err != nil {
		return err
	}
	s.risk.AfterCancel(checkReq)
	return nil
}

func (s *tradeSession) Account() (api.Account, bool) {
	snap, ok := s.svc.store.SessionSnapshot(s.id)
	if !ok || !snap.AccountOK {
		return api.Account{}, false
	}
	return snap.Account, true
}

func (s *tradeSession) Position(symbol string) (api.Position, bool) {
	snap, ok := s.svc.store.SessionSnapshot(s.id)
	if !ok {
		return api.Position{}, false
	}
	v, ok := snap.Positions[symbol]
	return v, ok
}

func (s *tradeSession) Positions() map[string]api.Position {
	snap, ok := s.svc.store.SessionSnapshot(s.id)
	if !ok {
		return map[string]api.Position{}
	}
	return snap.Positions
}

func (s *tradeSession) Order(orderID string) (api.Order, bool) {
	return s.svc.store.OrderSnapshot(s.id, orderID)
}

func (s *tradeSession) Orders() map[string]api.Order {
	snap, ok := s.svc.store.SessionSnapshot(s.id)
	if !ok {
		return map[string]api.Order{}
	}
	return snap.Orders
}

func (s *tradeSession) Trade(tradeID string) (api.Trade, bool) {
	snap, ok := s.svc.store.SessionSnapshot(s.id)
	if !ok {
		return api.Trade{}, false
	}
	v, ok := snap.Trades[tradeID]
	return v, ok
}

func (s *tradeSession) Trades() map[string]api.Trade {
	snap, ok := s.svc.store.SessionSnapshot(s.id)
	if !ok {
		return map[string]api.Trade{}
	}
	return snap.Trades
}

func (s *tradeSession) SubscribeAccounts(ctx context.Context, opts ...api.TradeSubOption) (api.AccountSub, error) {
	_ = opts
	ch := make(chan api.AccountEvent, 64)
	s.subsMu.Lock()
	s.accountSubs[ch] = struct{}{}
	s.subsMu.Unlock()
	sub := &accountSub{ch: ch}
	sub.closeFn = func() {
		s.subsMu.Lock()
		if _, ok := s.accountSubs[ch]; ok {
			delete(s.accountSubs, ch)
			close(ch)
		}
		s.subsMu.Unlock()
	}
	if acc, ok := s.Account(); ok {
		ch <- api.AccountEvent{Kind: api.TradeEventSnapshot, AccountID: s.req.AccountID, Account: acc, ChangedAt: time.Now()}
	}
	go s.bindSubContext(ctx, func() { _ = sub.Close() })
	return sub, nil
}

func (s *tradeSession) SubscribePositions(ctx context.Context, opts ...api.TradeSubOption) (api.PositionSub, error) {
	_ = opts
	ch := make(chan api.PositionEvent, 128)
	s.subsMu.Lock()
	s.positionSubs[ch] = struct{}{}
	s.subsMu.Unlock()
	sub := &positionSub{ch: ch}
	sub.closeFn = func() {
		s.subsMu.Lock()
		if _, ok := s.positionSubs[ch]; ok {
			delete(s.positionSubs, ch)
			close(ch)
		}
		s.subsMu.Unlock()
	}
	for symbol, p := range s.Positions() {
		ch <- api.PositionEvent{Kind: api.TradeEventSnapshot, AccountID: s.req.AccountID, Symbol: symbol, Position: p, ChangedAt: time.Now()}
	}
	go s.bindSubContext(ctx, func() { _ = sub.Close() })
	return sub, nil
}

func (s *tradeSession) SubscribeOrders(ctx context.Context, opts ...api.TradeSubOption) (api.OrderSub, error) {
	_ = opts
	ch := make(chan api.OrderEvent, 128)
	s.subsMu.Lock()
	s.orderSubs[ch] = struct{}{}
	s.subsMu.Unlock()
	sub := &orderSub{ch: ch}
	sub.closeFn = func() {
		s.subsMu.Lock()
		if _, ok := s.orderSubs[ch]; ok {
			delete(s.orderSubs, ch)
			close(ch)
		}
		s.subsMu.Unlock()
	}
	for orderID, o := range s.Orders() {
		ch <- api.OrderEvent{Kind: api.TradeEventSnapshot, AccountID: s.req.AccountID, OrderID: orderID, Order: o, ChangedAt: time.Now()}
	}
	go s.bindSubContext(ctx, func() { _ = sub.Close() })
	return sub, nil
}

func (s *tradeSession) SubscribeTrades(ctx context.Context, opts ...api.TradeSubOption) (api.TradeSub, error) {
	_ = opts
	ch := make(chan api.TradeFillEvent, 128)
	s.subsMu.Lock()
	s.tradeSubs[ch] = struct{}{}
	s.subsMu.Unlock()
	sub := &tradeSub{ch: ch}
	sub.closeFn = func() {
		s.subsMu.Lock()
		if _, ok := s.tradeSubs[ch]; ok {
			delete(s.tradeSubs, ch)
			close(ch)
		}
		s.subsMu.Unlock()
	}
	for tradeID, t := range s.Trades() {
		ch <- api.TradeFillEvent{Kind: api.TradeEventSnapshot, AccountID: s.req.AccountID, TradeID: tradeID, Trade: t, ChangedAt: time.Now()}
	}
	go s.bindSubContext(ctx, func() { _ = sub.Close() })
	return sub, nil
}

func (s *tradeSession) SubscribeNotifies(ctx context.Context, opts ...api.TradeSubOption) (api.NotifySub, error) {
	_ = opts
	ch := make(chan api.NotifyEvent, 128)
	s.subsMu.Lock()
	s.notifySubs[ch] = struct{}{}
	s.subsMu.Unlock()
	sub := &notifySub{ch: ch}
	sub.closeFn = func() {
		s.subsMu.Lock()
		if _, ok := s.notifySubs[ch]; ok {
			delete(s.notifySubs, ch)
			close(ch)
		}
		s.subsMu.Unlock()
	}
	go s.bindSubContext(ctx, func() { _ = sub.Close() })
	return sub, nil
}

func (s *tradeSession) GetRiskManagementRule(exchangeID string) (api.RiskManagementRule, bool) {
	snap, ok := s.svc.store.SessionSnapshot(s.id)
	if !ok {
		return api.RiskManagementRule{}, false
	}
	v, ok := snap.RiskRules[exchangeID]
	return v, ok
}

func (s *tradeSession) SetRiskManagementRule(ctx context.Context, exchangeID string, enable bool, opts ...api.RiskRuleOption) (api.RiskManagementRule, error) {
	_ = ctx
	exchangeID = strings.TrimSpace(exchangeID)
	if exchangeID == "" {
		return api.RiskManagementRule{}, api.NewError(api.ErrInvalidArgument, "empty exchange id", nil)
	}
	op := api.ResolveRiskRuleOptions(opts...)
	pack := map[string]any{
		"aid":                   "set_risk_management_rule",
		"user_id":               s.req.AccountID,
		"exchange_id":           exchangeID,
		"enable":                enable,
		"self_trade":            map[string]any{},
		"frequent_cancellation": map[string]any{},
		"trade_position_ratio":  map[string]any{},
	}
	if op.SelfTradeCountLimit != nil {
		pack["self_trade"].(map[string]any)["count_limit"] = *op.SelfTradeCountLimit
	}
	if op.FrequentInsertCountLimit != nil {
		pack["frequent_cancellation"].(map[string]any)["insert_order_count_limit"] = *op.FrequentInsertCountLimit
	}
	if op.FrequentCancelCountLimit != nil {
		pack["frequent_cancellation"].(map[string]any)["cancel_order_count_limit"] = *op.FrequentCancelCountLimit
	}
	if op.FrequentCancelPercentLimit != nil {
		pack["frequent_cancellation"].(map[string]any)["cancel_order_percent_limit"] = *op.FrequentCancelPercentLimit
	}
	if op.TradePositionUnitsLimit != nil {
		pack["trade_position_ratio"].(map[string]any)["trade_units_limit"] = *op.TradePositionUnitsLimit
	}
	if op.TradePositionRatioLimit != nil {
		pack["trade_position_ratio"].(map[string]any)["trade_position_ratio_limit"] = *op.TradePositionRatioLimit
	}
	if err := s.sendPack(pack); err != nil {
		return api.RiskManagementRule{}, err
	}
	if rr, ok := s.GetRiskManagementRule(exchangeID); ok {
		return rr, nil
	}
	return api.RiskManagementRule{ExchangeID: exchangeID, Enable: enable}, nil
}

func (s *tradeSession) GetRiskManagementData(symbol string) (api.RiskManagementData, bool) {
	snap, ok := s.svc.store.SessionSnapshot(s.id)
	if !ok {
		return api.RiskManagementData{}, false
	}
	v, ok := snap.RiskData[symbol]
	return v, ok
}

func (s *tradeSession) RiskManagementDataAll() map[string]api.RiskManagementData {
	snap, ok := s.svc.store.SessionSnapshot(s.id)
	if !ok {
		return map[string]api.RiskManagementData{}
	}
	return snap.RiskData
}

func (s *tradeSession) AddRiskRule(rule api.RiskRule) error {
	if err := s.risk.Add(rule); err != nil {
		return err
	}
	if b, ok := rule.(api.RiskBootstrapper); ok {
		if ss, ok := s.svc.store.SessionSnapshot(s.id); ok {
			b.BootstrapFromSnapshot(ss.Orders, ss.Trades)
		}
	}
	return nil
}

func (s *tradeSession) RemoveRiskRule(ruleID string) error {
	return s.risk.Remove(ruleID)
}

func (s *tradeSession) ConnEvents(ctx context.Context) (<-chan api.ConnEvent, error) {
	ch := make(chan api.ConnEvent, 32)
	s.subsMu.Lock()
	s.connSubs[ch] = struct{}{}
	s.subsMu.Unlock()
	go s.bindSubContext(ctx, func() {
		s.subsMu.Lock()
		if _, ok := s.connSubs[ch]; ok {
			delete(s.connSubs, ch)
			close(ch)
		}
		s.subsMu.Unlock()
	})
	return ch, nil
}

func (s *tradeSession) Close(ctx context.Context) error {
	_ = ctx
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.ready = false
	s.mu.Unlock()
	s.cancel()
	if s.ws != nil {
		_ = s.ws.Close()
	}
	s.wg.Wait()
	return nil
}

func (s *tradeSession) isClosed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}

func (s *tradeSession) closeAllChannels() {
	s.subsMu.Lock()
	for ch := range s.connSubs {
		close(ch)
	}
	for ch := range s.accountSubs {
		close(ch)
	}
	for ch := range s.positionSubs {
		close(ch)
	}
	for ch := range s.orderSubs {
		close(ch)
	}
	for ch := range s.tradeSubs {
		close(ch)
	}
	for ch := range s.notifySubs {
		close(ch)
	}
	s.connSubs = map[chan api.ConnEvent]struct{}{}
	s.accountSubs = map[chan api.AccountEvent]struct{}{}
	s.positionSubs = map[chan api.PositionEvent]struct{}{}
	s.orderSubs = map[chan api.OrderEvent]struct{}{}
	s.tradeSubs = map[chan api.TradeFillEvent]struct{}{}
	s.notifySubs = map[chan api.NotifyEvent]struct{}{}
	s.subsMu.Unlock()

	s.refsMu.Lock()
	for _, refs := range s.orderRefs {
		for ref := range refs {
			ref.close()
		}
	}
	s.orderRefs = map[string]map[*orderRef]struct{}{}
	s.refsMu.Unlock()

	close(s.notifyCh)
	close(s.eventCh)
}

func (s *tradeSession) bindSubContext(ctx context.Context, closeFn func()) {
	select {
	case <-ctx.Done():
		closeFn()
	case <-s.runCtx.Done():
		closeFn()
	}
}

type orderRef struct {
	session *tradeSession
	orderID string
	evCh    chan api.OrderEvent
	once    sync.Once
}

func (r *orderRef) AccountID() string { return r.session.req.AccountID }

func (r *orderRef) OrderID() string { return r.orderID }

func (r *orderRef) Snapshot() (api.Order, bool) {
	return r.session.Order(r.orderID)
}

func (r *orderRef) Events() <-chan api.OrderEvent { return r.evCh }

func (r *orderRef) WaitDone(ctx context.Context) (api.Order, error) {
	defer r.detach()
	for {
		if o, ok := r.Snapshot(); ok {
			if strings.EqualFold(o.Status, "FINISHED") {
				if o.IsError != nil && *o.IsError {
					return o, api.NewError(api.ErrExchangeReject, "order rejected", nil)
				}
				return o, nil
			}
		}
		select {
		case <-ctx.Done():
			return api.Order{}, api.NewError(api.ErrTimeout, "wait order done timeout", ctx.Err())
		case _, ok := <-r.evCh:
			if !ok {
				if o, ok := r.Snapshot(); ok {
					return o, nil
				}
				return api.Order{}, api.NewError(api.ErrDisconnected, "session closed", nil)
			}
		}
	}
}

func (r *orderRef) detach() {
	r.session.refsMu.Lock()
	defer r.session.refsMu.Unlock()
	refs := r.session.orderRefs[r.orderID]
	delete(refs, r)
	if len(refs) == 0 {
		delete(r.session.orderRefs, r.orderID)
	}
}

func (r *orderRef) push(ev api.OrderEvent) {
	select {
	case r.evCh <- ev:
	default:
		r.evCh <- ev
	}
}

func (r *orderRef) close() {
	r.once.Do(func() {
		close(r.evCh)
	})
}

type accountSub struct {
	ch      chan api.AccountEvent
	closeFn func()
	once    sync.Once
}

type positionSub struct {
	ch      chan api.PositionEvent
	closeFn func()
	once    sync.Once
}

type orderSub struct {
	ch      chan api.OrderEvent
	closeFn func()
	once    sync.Once
}

type tradeSub struct {
	ch      chan api.TradeFillEvent
	closeFn func()
	once    sync.Once
}

type notifySub struct {
	ch      chan api.NotifyEvent
	closeFn func()
	once    sync.Once
}

func (s *accountSub) C() <-chan api.AccountEvent   { return s.ch }
func (s *positionSub) C() <-chan api.PositionEvent { return s.ch }
func (s *orderSub) C() <-chan api.OrderEvent       { return s.ch }
func (s *tradeSub) C() <-chan api.TradeFillEvent   { return s.ch }
func (s *notifySub) C() <-chan api.NotifyEvent     { return s.ch }
func (s *accountSub) Close() error {
	s.once.Do(func() {
		if s.closeFn != nil {
			s.closeFn()
		}
	})
	return nil
}
func (s *positionSub) Close() error {
	s.once.Do(func() {
		if s.closeFn != nil {
			s.closeFn()
		}
	})
	return nil
}
func (s *orderSub) Close() error {
	s.once.Do(func() {
		if s.closeFn != nil {
			s.closeFn()
		}
	})
	return nil
}
func (s *tradeSub) Close() error {
	s.once.Do(func() {
		if s.closeFn != nil {
			s.closeFn()
		}
	})
	return nil
}
func (s *notifySub) Close() error {
	s.once.Do(func() {
		if s.closeFn != nil {
			s.closeFn()
		}
	})
	return nil
}

type tradingStatusHub struct {
	auth  api.AuthService
	store *store.TradingStatusStore
	url   string
	rsv   func(context.Context, []string) (map[string]string, error)

	mu      sync.RWMutex
	started bool
	closed  bool
	runCtx  context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	ws *infra.WSManager

	upSignal chan struct{}

	nextSubID int64
	symbols   map[string]struct{}
	seen      map[string]bool
	subs      map[int64]*tradingStatusSub
	opt2und   map[string]string
	und2opt   map[string]map[string]struct{}
}

type tradingStatusSub struct {
	hub     *tradingStatusHub
	id      int64
	symbols map[string]struct{}
	ch      chan api.TradingStatusEvent
	once    sync.Once
}

func newTradingStatusHub(auth api.AuthService, st *store.TradingStatusStore, url string, resolver func(context.Context, []string) (map[string]string, error)) *tradingStatusHub {
	if st == nil {
		st = store.NewTradingStatusStore()
	}
	return &tradingStatusHub{
		auth:     auth,
		store:    st,
		url:      strings.TrimSpace(url),
		rsv:      resolver,
		upSignal: make(chan struct{}, 1),
		symbols:  map[string]struct{}{},
		seen:     map[string]bool{},
		subs:     map[int64]*tradingStatusSub{},
		opt2und:  map[string]string{},
		und2opt:  map[string]map[string]struct{}{},
	}
}

func (h *tradingStatusHub) ensureStarted(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return api.NewError(api.ErrDisconnected, "trading_status hub closed", nil)
	}
	if h.started {
		return nil
	}
	if h.url == "" {
		h.url = defaultTradingStatusURL
	}
	h.runCtx, h.cancel = context.WithCancel(context.Background())
	h.started = true
	h.wg.Add(1)
	go h.run()
	_ = ctx
	return nil
}

func (h *tradingStatusHub) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	cancel := h.cancel
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if h.ws != nil {
		_ = h.ws.Close()
	}
	h.wg.Wait()
	h.mu.Lock()
	for _, sub := range h.subs {
		sub.once.Do(func() { close(sub.ch) })
	}
	h.subs = map[int64]*tradingStatusSub{}
	h.mu.Unlock()
	return nil
}

func (h *tradingStatusHub) run() {
	defer h.wg.Done()
	h.ws = infra.NewWSManager(
		h.url,
		func() http.Header { return h.auth.Header() },
		infra.WSCallbacks{
			OnConnect: func(ctx context.Context) error {
				h.replaySubscribe()
				return h.sendPack(map[string]any{"aid": "peek_message"})
			},
			OnMessage: func(ctx context.Context, pack map[string]any) error {
				_ = ctx
				h.handleMessage(pack)
				return h.sendPack(map[string]any{"aid": "peek_message"})
			},
		},
		nil,
	)
	if err := h.ws.Start(h.runCtx); err != nil {
		return
	}
	<-h.runCtx.Done()
}

func (h *tradingStatusHub) handleMessage(pack map[string]any) {
	if aid, _ := pack["aid"].(string); aid != "rtn_data" {
		return
	}
	items, _ := pack["data"].([]any)
	for _, item := range items {
		dm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tsNode, ok := dm["trading_status"].(map[string]any)
		if !ok {
			continue
		}
		for symbol, raw := range tsNode {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			st := api.TradingStatus{Symbol: symbol, TradeStatus: normalizeTradeStatus(asStringLoose(m["trade_status"]))}
			h.upsertAndDispatch(symbol, st)
			for _, ext := range h.extendOptionStatuses(symbol, st) {
				h.upsertAndDispatch(ext.Symbol, ext)
			}
		}
	}
}

func (h *tradingStatusHub) TradingStatus(symbol string) (api.TradingStatus, bool) {
	return h.store.Get(symbol)
}

func (h *tradingStatusHub) addSymbols(ctx context.Context, symbols []string) {
	resolve := map[string]string{}
	if h.rsv != nil {
		if m, err := h.rsv(ctx, symbols); err == nil {
			resolve = m
		}
	}
	h.mu.Lock()
	changed := false
	for _, s := range symbols {
		underlying := s
		if u := strings.TrimSpace(resolve[s]); u != "" {
			underlying = u
		}
		if underlying != s {
			h.opt2und[s] = underlying
			if h.und2opt[underlying] == nil {
				h.und2opt[underlying] = map[string]struct{}{}
			}
			h.und2opt[underlying][s] = struct{}{}
		}
		if _, ok := h.symbols[underlying]; ok {
			continue
		}
		h.symbols[underlying] = struct{}{}
		changed = true
	}
	h.mu.Unlock()
	if changed {
		h.replaySubscribe()
	}
}

func (h *tradingStatusHub) replaySubscribe() {
	h.mu.RLock()
	symbols := make([]string, 0, len(h.symbols))
	for s := range h.symbols {
		symbols = append(symbols, s)
	}
	h.mu.RUnlock()
	if len(symbols) == 0 {
		return
	}
	sort.Strings(symbols)
	_ = h.sendPack(map[string]any{"aid": "subscribe_trading_status", "ins_list": strings.Join(symbols, ",")})
}

func (h *tradingStatusHub) subscribe(ctx context.Context, symbols []string) (api.TradingStatusSub, error) {
	h.addSymbols(ctx, symbols)
	ss := map[string]struct{}{}
	for _, s := range symbols {
		ss[s] = struct{}{}
	}
	h.mu.Lock()
	h.nextSubID++
	sub := &tradingStatusSub{hub: h, id: h.nextSubID, symbols: ss, ch: make(chan api.TradingStatusEvent, 128)}
	h.subs[sub.id] = sub
	h.mu.Unlock()

	for _, sym := range symbols {
		if st, ok := h.store.Get(sym); ok {
			sub.ch <- api.TradingStatusEvent{Kind: api.TradeEventSnapshot, Symbol: sym, Status: st, ChangedAt: time.Now()}
		}
	}

	go func() {
		select {
		case <-ctx.Done():
			_ = sub.Close()
		case <-h.runCtx.Done():
			_ = sub.Close()
		}
	}()
	return sub, nil
}

func (h *tradingStatusHub) dispatch(ev api.TradingStatusEvent) {
	h.mu.RLock()
	subs := make([]*tradingStatusSub, 0, len(h.subs))
	for _, sub := range h.subs {
		subs = append(subs, sub)
	}
	h.mu.RUnlock()
	for _, sub := range subs {
		if _, ok := sub.symbols[ev.Symbol]; !ok {
			continue
		}
		sub.ch <- ev
	}
	select {
	case h.upSignal <- struct{}{}:
	default:
	}
}

func (h *tradingStatusHub) upsertAndDispatch(symbol string, st api.TradingStatus) {
	h.store.Upsert(st)
	kind := api.TradeEventUpsert
	h.mu.Lock()
	if !h.seen[symbol] {
		h.seen[symbol] = true
		kind = api.TradeEventSnapshot
	}
	h.mu.Unlock()
	h.dispatch(api.TradingStatusEvent{Kind: kind, Symbol: symbol, Status: st, ChangedAt: time.Now()})
}

func (h *tradingStatusHub) extendOptionStatuses(underlying string, st api.TradingStatus) []api.TradingStatus {
	h.mu.RLock()
	opts := h.und2opt[underlying]
	h.mu.RUnlock()
	if len(opts) == 0 {
		return nil
	}
	out := make([]api.TradingStatus, 0, len(opts))
	for sym := range opts {
		out = append(out, api.TradingStatus{Symbol: sym, TradeStatus: st.TradeStatus})
	}
	return out
}

func (h *tradingStatusHub) sendPack(pack map[string]any) error {
	if h.ws == nil {
		return api.NewError(api.ErrDisconnected, "trading_status ws disconnected", nil)
	}
	if err := h.ws.Send(pack); err != nil {
		return api.NewError(api.ErrDisconnected, "trading_status ws write failed", err)
	}
	return nil
}

func (s *tradingStatusSub) C() <-chan api.TradingStatusEvent { return s.ch }

func (s *tradingStatusSub) Close() error {
	s.once.Do(func() {
		s.hub.mu.Lock()
		delete(s.hub.subs, s.id)
		close(s.ch)
		s.hub.mu.Unlock()
	})
	return nil
}

func parseLoginReject(pack map[string]any) error {
	if v := asInt64Loose(pack["error_id"]); v > 0 {
		return fmt.Errorf("error_id=%d", v)
	}
	if v := strings.TrimSpace(asStringLoose(pack["error"])); v != "" {
		return errors.New(v)
	}
	if v := strings.TrimSpace(asStringLoose(pack["message"])); v != "" {
		return errors.New(v)
	}
	if v := strings.TrimSpace(asStringLoose(pack["msg"])); v != "" {
		return errors.New(v)
	}
	if ok, exists := asBoolLooseOk(pack["ok"]); exists && !ok {
		return errors.New("ok=false")
	}
	status := strings.ToUpper(strings.TrimSpace(asStringLoose(pack["status"])))
	if status == "FAILED" || status == "ERROR" {
		return fmt.Errorf("status=%s", status)
	}
	return nil
}

func decodeInto(m map[string]any, out any) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, out)
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
	return out
}

func normalizeTradeStatus(v string) api.TradeStatusCode {
	s := strings.ToUpper(strings.TrimSpace(v))
	if s == string(api.TradeStatusAuctionOrdering) {
		return api.TradeStatusAuctionOrdering
	}
	if s == string(api.TradeStatusContinous) {
		return api.TradeStatusContinous
	}
	return api.TradeStatusNoTrading
}

func asStringLoose(v any) string {
	s, _ := v.(string)
	return s
}

func asBoolLooseOk(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		x = strings.TrimSpace(strings.ToLower(x))
		if x == "true" {
			return true, true
		}
		if x == "false" {
			return false, true
		}
		return false, false
	default:
		return false, false
	}
}

func asInt64Loose(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	case json.Number:
		i, _ := x.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return i
	default:
		return 0
	}
}

func isSDKCode(err error, code api.ErrCode) bool {
	if err == nil {
		return false
	}
	var se *api.SDKError
	if errors.As(err, &se) {
		return se.Code == code
	}
	return false
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		d = time.Second
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func genTradeID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

func buildOptionResolver(mkt api.MarketService) func(context.Context, []string) (map[string]string, error) {
	if mkt == nil {
		return nil
	}
	return func(ctx context.Context, symbols []string) (map[string]string, error) {
		out := map[string]string{}
		for _, s := range symbols {
			out[s] = s
		}
		info, err := mkt.QuerySymbolInfo(ctx, symbols)
		if err != nil {
			return out, err
		}
		for _, q := range info {
			symbol := strings.TrimSpace(q.InstrumentID)
			if symbol == "" {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(q.InsClass), "OPTION") && strings.TrimSpace(q.UnderlyingSymbol) != "" {
				out[symbol] = strings.TrimSpace(q.UnderlyingSymbol)
				continue
			}
			out[symbol] = symbol
		}
		return out, nil
	}
}
