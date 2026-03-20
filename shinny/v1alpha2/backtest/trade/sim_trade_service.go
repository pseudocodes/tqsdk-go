package trade

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest/data"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest/runtime"
)

type Option func(*simTradeService)

func WithInitialBalance(v float64) Option {
	return func(s *simTradeService) {
		if v > 0 {
			s.initialBalance = v
		}
	}
}

// WithSettlementObserver sets the observer that receives daily snapshots.
func WithSettlementObserver(obs SettlementObserver) Option {
	return func(s *simTradeService) {
		s.settlementObserver = obs
	}
}

type simTradeService struct {
	rt *runtime.EventKernel

	mu             sync.RWMutex
	started        bool
	closed         bool
	nextSessionSeq int64
	nextTradeSeq   int64
	initialBalance float64
	sessions       map[string]*simSession

	latestQuotes map[string]api.Quote

	settlementObserver SettlementObserver
}

func NewSimTradeService(rt *runtime.EventKernel, opts ...Option) api.TradeService {
	s := &simTradeService{
		rt:             rt,
		initialBalance: 10000000,
		sessions:       map[string]*simSession{},
		latestQuotes:   map[string]api.Quote{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

func (s *simTradeService) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return api.NewError(api.ErrDisconnected, "sim trade service closed", nil)
	}
	if s.started {
		return nil
	}
	s.started = true
	s.rt.RegisterTradeSink("sim_trade_service", s.onBatch)
	go func() {
		<-ctx.Done()
		_ = s.Close()
	}()
	return nil
}

func (s *simTradeService) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	sessions := make([]*simSession, 0, len(s.sessions))
	for _, ss := range s.sessions {
		sessions = append(sessions, ss)
	}
	s.sessions = map[string]*simSession{}
	s.mu.Unlock()
	for _, ss := range sessions {
		_ = ss.Close(context.Background())
	}
	s.rt.RegisterTradeSink("sim_trade_service", nil)
	return nil
}

func (s *simTradeService) Login(ctx context.Context, req api.TradeLoginReq) (api.TradeSession, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, api.NewError(api.ErrDisconnected, "sim trade service closed", nil)
	}
	if !s.started {
		return nil, api.NewError(api.ErrDisconnected, "sim trade service not started", nil)
	}
	accountID := strings.TrimSpace(req.AccountID)
	if accountID == "" {
		accountID = "SIM"
	}
	sessionID := fmt.Sprintf("BT_session_%d", atomic.AddInt64(&s.nextSessionSeq, 1))
	ss := newSimSession(s, sessionID, accountID)
	for sym, q := range s.latestQuotes {
		ss.latestQuotes[sym] = q
	}
	s.sessions[sessionID] = ss
	ss.emitSessionEvent(api.TradeSessionReady, nil)
	return ss, nil
}

func (s *simTradeService) Session(sessionID string) (api.TradeSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ss, ok := s.sessions[sessionID]
	if !ok || ss.isClosed() {
		return nil, false
	}
	return ss, true
}

func (s *simTradeService) onBatch(batch runtime.MarketEventBatch) {
	s.mu.RLock()
	sessions := make([]*simSession, 0, len(s.sessions))
	for _, ss := range s.sessions {
		sessions = append(sessions, ss)
	}
	s.mu.RUnlock()

	for _, ev := range batch.Events {
		if ev.Quote == nil {
			continue
		}
		s.mu.Lock()
		s.latestQuotes[ev.Symbol] = *ev.Quote
		s.mu.Unlock()
		for _, ss := range sessions {
			ss.onQuoteUpdate(ev.Symbol, *ev.Quote)
		}
	}
	if batch.DaySwitched {
		for _, ss := range sessions {
			ss.Settle(batch.TradingDay)
		}
	}
}

func (s *simTradeService) GetTradingStatus(ctx context.Context, symbol string) (api.TradingStatus, error) {
	_ = ctx
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return api.TradingStatus{}, api.NewError(api.ErrInvalidSymbol, "empty symbol", nil)
	}
	return api.TradingStatus{Symbol: symbol, TradeStatus: api.TradeStatusContinous}, nil
}

func (s *simTradeService) TradingStatus(symbol string) (api.TradingStatus, bool) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return api.TradingStatus{}, false
	}
	return api.TradingStatus{Symbol: symbol, TradeStatus: api.TradeStatusContinous}, true
}

func (s *simTradeService) SubscribeTradingStatuses(ctx context.Context, symbols ...string) (api.TradingStatusSub, error) {
	if len(symbols) == 0 {
		return nil, api.NewError(api.ErrInvalidSymbol, "empty symbols", nil)
	}
	ch := make(chan api.TradingStatusEvent, 64)
	sub := &tradingStatusSub{ch: ch}
	now := time.Now()
	for _, sym := range symbols {
		sym = strings.TrimSpace(sym)
		if sym == "" {
			continue
		}
		ch <- api.TradingStatusEvent{Kind: api.TradeEventSnapshot, Symbol: sym, Status: api.TradingStatus{Symbol: sym, TradeStatus: api.TradeStatusContinous}, ChangedAt: now}
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = sub.Close()
		}
	}()
	return sub, nil
}

type simSession struct {
	svc       *simTradeService
	sessionID string
	accountID string

	mu      sync.RWMutex
	state   api.TradeSessionState
	ready   bool
	closed  bool
	account api.Account

	positions map[string]api.Position
	orders       map[string]api.Order
	trades       map[string]api.Trade
	orderFrozens map[string]*orderFrozen
	dailyTrades  []api.Trade // trades accumulated during the current trading day
	latestQuotes map[string]api.Quote

	eventCh  chan api.TradeSessionEvent
	notifyCh chan api.NotifyEvent

	subsMu       sync.Mutex
	connSubs     map[chan api.ConnEvent]struct{}
	accountSubs  map[chan api.AccountEvent]struct{}
	positionSubs map[chan api.PositionEvent]struct{}
	orderSubs    map[chan api.OrderEvent]struct{}
	tradeSubs    map[chan api.TradeFillEvent]struct{}
	notifySubs   map[chan api.NotifyEvent]struct{}

	refsMu    sync.Mutex
	orderRefs map[string]map[*orderRef]struct{}

	orderSeq int64
}

func newSimSession(svc *simTradeService, sessionID, accountID string) *simSession {
	acc := api.Account{
		Currency:      "CNY",
		PreBalance:    svc.initialBalance,
		StaticBalance: svc.initialBalance,
		Balance:       svc.initialBalance,
		Available:     svc.initialBalance,
	}
	return &simSession{
		svc:         svc,
		sessionID:   sessionID,
		accountID:   accountID,
		state:       api.TradeSessionReady,
		ready:       true,
		account:     acc,
		positions:   map[string]api.Position{},
		orders:       map[string]api.Order{},
		trades:       map[string]api.Trade{},
		orderFrozens: map[string]*orderFrozen{},
		latestQuotes: map[string]api.Quote{},
		eventCh:     make(chan api.TradeSessionEvent, 64),
		notifyCh:    make(chan api.NotifyEvent, 128),
		connSubs:    map[chan api.ConnEvent]struct{}{},
		accountSubs: map[chan api.AccountEvent]struct{}{},
		positionSubs: map[chan api.PositionEvent]struct{}{},
		orderSubs:   map[chan api.OrderEvent]struct{}{},
		tradeSubs:   map[chan api.TradeFillEvent]struct{}{},
		notifySubs:  map[chan api.NotifyEvent]struct{}{},
		orderRefs:   map[string]map[*orderRef]struct{}{},
	}
}

func (s *simSession) SessionID() string { return s.sessionID }
func (s *simSession) AccountID() string { return s.accountID }
func (s *simSession) State() api.TradeSessionState {
	s.mu.RLock(); defer s.mu.RUnlock(); return s.state
}
func (s *simSession) Ready() bool {
	s.mu.RLock(); defer s.mu.RUnlock(); return s.ready && !s.closed
}
func (s *simSession) Events() <-chan api.TradeSessionEvent { return s.eventCh }
func (s *simSession) NotifyEvents() <-chan api.NotifyEvent { return s.notifyCh }

func (s *simSession) emitSessionEvent(state api.TradeSessionState, err error) {
	s.mu.Lock()
	s.state = state
	s.ready = state == api.TradeSessionReady
	s.mu.Unlock()
	ev := api.TradeSessionEvent{SessionID: s.sessionID, AccountID: s.accountID, State: state, Err: err, ChangedAt: time.Now()}
	select {
	case s.eventCh <- ev:
	default:
	}
}

func (s *simSession) InsertOrder(ctx context.Context, symbol string, direction api.Direction, volume int, opts ...api.InsertOrderOption) (api.OrderRef, error) {
	_ = ctx
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return nil, api.NewError(api.ErrInvalidSymbol, "empty symbol", nil)
	}
	if direction != api.DirectionBuy && direction != api.DirectionSell {
		return nil, api.NewError(api.ErrInvalidDirection, "invalid direction", nil)
	}
	if volume <= 0 {
		return nil, api.NewError(api.ErrInvalidVolume, "volume must be > 0", nil)
	}
	if !s.Ready() {
		return nil, api.NewError(api.ErrDisconnected, "session not ready", nil)
	}

	_ = s.svc.rt.EnsureQuoteBasis(symbol, runtime.QuoteBasisReasonOrderInsert)

	// GAP-03: Reject orders on expired contracts
	{
		m := s.svc.getMeta(symbol)
		if m.ExpireDatetime > 0 {
			currentNS := s.svc.rt.CurrentNS()
			expireNS := int64(m.ExpireDatetime * 1e9)
			if currentNS >= expireNS {
				return nil, api.NewError(api.ErrInvalidSymbol, "合约已过期", nil)
			}
		}
	}

	op := api.ResolveInsertOrderOptions(opts...)

	exID, insID := splitSymbol(symbol)
	orderID := strings.TrimSpace(op.OrderID)
	if orderID == "" {
		orderID = fmt.Sprintf("BT_ord_%d_%d", time.Now().UnixNano(), atomic.AddInt64(&s.orderSeq, 1))
	}
	priceType := "LIMIT"
	timeCondition := "GFD"
	volumeCondition := "ANY"
	limitPrice := 0.0
	if op.LimitPrice == nil || op.PriceMode == "BEST" || op.PriceMode == "FIVELEVEL" {
		priceType = "ANY"
		timeCondition = "IOC"
		limitPrice = math.NaN()
	} else {
		limitPrice = *op.LimitPrice
		if op.Advanced != api.AdvancedNone {
			timeCondition = "IOC"
		}
	}
	if op.Advanced == api.AdvancedFOK {
		volumeCondition = "ALL"
	}

	order := api.Order{
		OrderID:        orderID,
		ExchangeID:     exID,
		InstrumentID:   insID,
		Direction:      string(direction),
		Offset:         string(op.Offset),
		VolumeOrign:    int64(volume),
		VolumeLeft:     int64(volume),
		LimitPrice:     limitPrice,
		PriceType:      priceType,
		VolumeCondition: volumeCondition,
		TimeCondition:  timeCondition,
		InsertDateTime: time.Now().UnixNano(),
		Status:         "ALIVE",
		LastMsg:        "报单已提交",
	}

	// --- Frozen margin/premium/position logic (GAP-02 + GAP-12) ---
	meta := s.svc.getMeta(symbol)
	isOption := strings.EqualFold(meta.InsClass, "OPTION")
	frozen := &orderFrozen{symbol: symbol}
	offset := op.Offset

	s.mu.Lock()

	if offset == api.OffsetOpen {
		// Calculate and freeze margin or premium for open orders
		q, hasQ := s.latestQuotes[symbol]
		vm := int64(0)
		if hasQ {
			vm = q.VolumeMultiple
		}
		if vm == 0 {
			vm = meta.VolumeMultiple
		}
		if vm == 0 {
			vm = 1
		}
		if isOption && direction == api.DirectionBuy {
			askPrice := 0.0
			if hasQ {
				askPrice = q.AskPrice1
			}
			frozen.frozenPremium = askPrice * float64(vm) * float64(volume)
		} else if isOption {
			optLast := 0.0
			if hasQ {
				optLast = q.LastPrice
			}
			underlyingLast := 0.0
			if uq, uOK := s.latestQuotes[meta.UnderlyingSymbol]; uOK {
				underlyingLast = uq.LastPrice
			}
			marginPerLot := OptionMarginPerLot(optLast, underlyingLast, meta.StrikePrice, vm, meta.OptionClass, meta)
			frozen.frozenMargin = marginPerLot * float64(volume)
		} else {
			lastPrice := 0.0
			if hasQ {
				lastPrice = q.LastPrice
			}
			marginPerLot := FutureMarginPerLot(lastPrice, vm, meta, s.svc.getMarginOverrideForSymbol(symbol))
			frozen.frozenMargin = marginPerLot * float64(volume)
		}

		if frozen.frozenMargin+frozen.frozenPremium > s.account.Available+1e-6 {
			isErr := true
			order.Status = "FINISHED"
			order.LastMsg = "开仓资金不足"
			order.IsError = &isErr
			s.orders[orderID] = order
			s.mu.Unlock()
			s.emitOrderEvent(api.OrderEvent{Kind: api.TradeEventUpsert, AccountID: s.accountID, OrderID: orderID, Order: order, ChangedAt: time.Now()})
			ref := s.newOrderRef(orderID)
			return ref, nil
		}
		s.account.FrozenMargin += frozen.frozenMargin
		s.account.FrozenPremium += frozen.frozenPremium
		s.account.Available -= frozen.frozenMargin + frozen.frozenPremium
	} else {
		// Close order: freeze position volumes
		pos := s.positions[symbol]
		vol := int64(volume)
		if direction == api.DirectionBuy {
			// Buy to close short
			avail := availableCloseVolumeLocked(pos, "SHORT", offset, exID)
			if vol > avail {
				isErr := true
				order.Status = "FINISHED"
				order.LastMsg = "平仓手数不足"
				order.IsError = &isErr
				s.orders[orderID] = order
				s.mu.Unlock()
				s.emitOrderEvent(api.OrderEvent{Kind: api.TradeEventUpsert, AccountID: s.accountID, OrderID: orderID, Order: order, ChangedAt: time.Now()})
				ref := s.newOrderRef(orderID)
				return ref, nil
			}
			freezePositionVolumeLocked(&pos, frozen, "SHORT", vol, offset, exID)
		} else {
			// Sell to close long
			avail := availableCloseVolumeLocked(pos, "LONG", offset, exID)
			if vol > avail {
				isErr := true
				order.Status = "FINISHED"
				order.LastMsg = "平仓手数不足"
				order.IsError = &isErr
				s.orders[orderID] = order
				s.mu.Unlock()
				s.emitOrderEvent(api.OrderEvent{Kind: api.TradeEventUpsert, AccountID: s.accountID, OrderID: orderID, Order: order, ChangedAt: time.Now()})
				ref := s.newOrderRef(orderID)
				return ref, nil
			}
			freezePositionVolumeLocked(&pos, frozen, "LONG", vol, offset, exID)
		}
		s.positions[symbol] = pos
	}

	s.orderFrozens[orderID] = frozen
	s.orders[orderID] = order
	quote, ok := s.latestQuotes[symbol]
	s.mu.Unlock()

	s.emitOrderEvent(api.OrderEvent{Kind: api.TradeEventUpsert, AccountID: s.accountID, OrderID: orderID, Order: order, ChangedAt: time.Now()})

	if ok {
		s.tryMatch(orderID, quote)
	}
	ref := s.newOrderRef(orderID)
	return ref, nil
}

func (s *simSession) CancelOrder(ctx context.Context, orderID string) error {
	_ = ctx
	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return api.NewError(api.ErrOrderNotFound, "empty order id", nil)
	}
	s.mu.Lock()
	order, ok := s.orders[orderID]
	if !ok {
		s.mu.Unlock()
		return api.NewError(api.ErrOrderNotFound, "order not found", nil)
	}
	if strings.EqualFold(order.Status, "FINISHED") {
		s.mu.Unlock()
		return nil
	}
	order.Status = "FINISHED"
	order.LastMsg = "已撤单"
	order.VolumeLeft = order.VolumeOrign
	s.orders[orderID] = order
	s.releaseFrozenLocked(orderID)
	s.recalcAccountLocked()
	s.mu.Unlock()
	s.emitOrderEvent(api.OrderEvent{Kind: api.TradeEventUpsert, AccountID: s.accountID, OrderID: orderID, Order: order, ChangedAt: time.Now()})
	return nil
}

func (s *simSession) Account() (api.Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return api.Account{}, false
	}
	return s.account, true
}

func (s *simSession) Position(symbol string) (api.Position, bool) {
	s.mu.RLock(); defer s.mu.RUnlock()
	p, ok := s.positions[symbol]
	return p, ok
}

func (s *simSession) Positions() map[string]api.Position {
	s.mu.RLock(); defer s.mu.RUnlock()
	out := make(map[string]api.Position, len(s.positions))
	for k, v := range s.positions { out[k] = v }
	return out
}

func (s *simSession) Order(orderID string) (api.Order, bool) {
	s.mu.RLock(); defer s.mu.RUnlock()
	v, ok := s.orders[orderID]
	return v, ok
}

func (s *simSession) Orders() map[string]api.Order {
	s.mu.RLock(); defer s.mu.RUnlock()
	out := make(map[string]api.Order, len(s.orders))
	for k, v := range s.orders { out[k] = v }
	return out
}

func (s *simSession) Trade(tradeID string) (api.Trade, bool) {
	s.mu.RLock(); defer s.mu.RUnlock()
	v, ok := s.trades[tradeID]
	return v, ok
}

func (s *simSession) Trades() map[string]api.Trade {
	s.mu.RLock(); defer s.mu.RUnlock()
	out := make(map[string]api.Trade, len(s.trades))
	for k, v := range s.trades { out[k] = v }
	return out
}

func (s *simSession) SubscribeAccounts(ctx context.Context, opts ...api.TradeSubOption) (api.AccountSub, error) {
	_ = opts
	ch := make(chan api.AccountEvent, 64)
	s.subsMu.Lock(); s.accountSubs[ch] = struct{}{}; s.subsMu.Unlock()
	sub := &accountSub{ch: ch, closeFn: func() { s.removeAccountSub(ch) }}
	if acc, ok := s.Account(); ok {
		ch <- api.AccountEvent{Kind: api.TradeEventSnapshot, AccountID: s.accountID, Account: acc, ChangedAt: time.Now()}
	}
	go bindSubContext(ctx, func() { _ = sub.Close() })
	return sub, nil
}

func (s *simSession) SubscribePositions(ctx context.Context, opts ...api.TradeSubOption) (api.PositionSub, error) {
	_ = opts
	ch := make(chan api.PositionEvent, 128)
	s.subsMu.Lock(); s.positionSubs[ch] = struct{}{}; s.subsMu.Unlock()
	sub := &positionSub{ch: ch, closeFn: func() { s.removePositionSub(ch) }}
	for sym, p := range s.Positions() {
		ch <- api.PositionEvent{Kind: api.TradeEventSnapshot, AccountID: s.accountID, Symbol: sym, Position: p, ChangedAt: time.Now()}
	}
	go bindSubContext(ctx, func() { _ = sub.Close() })
	return sub, nil
}

func (s *simSession) SubscribeOrders(ctx context.Context, opts ...api.TradeSubOption) (api.OrderSub, error) {
	_ = opts
	ch := make(chan api.OrderEvent, 128)
	s.subsMu.Lock(); s.orderSubs[ch] = struct{}{}; s.subsMu.Unlock()
	sub := &orderSub{ch: ch, closeFn: func() { s.removeOrderSub(ch) }}
	for id, o := range s.Orders() {
		ch <- api.OrderEvent{Kind: api.TradeEventSnapshot, AccountID: s.accountID, OrderID: id, Order: o, ChangedAt: time.Now()}
	}
	go bindSubContext(ctx, func() { _ = sub.Close() })
	return sub, nil
}

func (s *simSession) SubscribeTrades(ctx context.Context, opts ...api.TradeSubOption) (api.TradeSub, error) {
	_ = opts
	ch := make(chan api.TradeFillEvent, 128)
	s.subsMu.Lock(); s.tradeSubs[ch] = struct{}{}; s.subsMu.Unlock()
	sub := &tradeSub{ch: ch, closeFn: func() { s.removeTradeSub(ch) }}
	for id, t := range s.Trades() {
		ch <- api.TradeFillEvent{Kind: api.TradeEventSnapshot, AccountID: s.accountID, TradeID: id, Trade: t, ChangedAt: time.Now()}
	}
	go bindSubContext(ctx, func() { _ = sub.Close() })
	return sub, nil
}

func (s *simSession) SubscribeNotifies(ctx context.Context, opts ...api.TradeSubOption) (api.NotifySub, error) {
	_ = opts
	ch := make(chan api.NotifyEvent, 128)
	s.subsMu.Lock(); s.notifySubs[ch] = struct{}{}; s.subsMu.Unlock()
	sub := &notifySub{ch: ch, closeFn: func() { s.removeNotifySub(ch) }}
	go bindSubContext(ctx, func() { _ = sub.Close() })
	return sub, nil
}

func (s *simSession) GetRiskManagementRule(exchangeID string) (api.RiskManagementRule, bool) {
	_ = exchangeID
	return api.RiskManagementRule{}, false
}

func (s *simSession) SetRiskManagementRule(ctx context.Context, exchangeID string, enable bool, opts ...api.RiskRuleOption) (api.RiskManagementRule, error) {
	_, _, _, _ = ctx, exchangeID, enable, opts
	return api.RiskManagementRule{}, api.NewError(api.ErrUnsupportedBacktest, "risk rules are unsupported in sim session", nil)
}

func (s *simSession) GetRiskManagementData(symbol string) (api.RiskManagementData, bool) {
	_ = symbol
	return api.RiskManagementData{}, false
}

func (s *simSession) RiskManagementDataAll() map[string]api.RiskManagementData {
	return map[string]api.RiskManagementData{}
}

func (s *simSession) AddRiskRule(rule api.RiskRule) error {
	_ = rule
	return api.NewError(api.ErrUnsupportedBacktest, "risk rules are unsupported in sim session", nil)
}

func (s *simSession) RemoveRiskRule(ruleID string) error {
	_ = ruleID
	return api.NewError(api.ErrUnsupportedBacktest, "risk rules are unsupported in sim session", nil)
}

func (s *simSession) ConnEvents(ctx context.Context) (<-chan api.ConnEvent, error) {
	ch := make(chan api.ConnEvent, 32)
	s.subsMu.Lock(); s.connSubs[ch] = struct{}{}; s.subsMu.Unlock()
	ch <- api.ConnEvent{State: "ready", At: time.Now()}
	go bindSubContext(ctx, func() { s.removeConnSub(ch) })
	return ch, nil
}

func (s *simSession) Close(ctx context.Context) error {
	_ = ctx
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.ready = false
	s.state = api.TradeSessionClosed
	s.mu.Unlock()
	s.emitSessionEvent(api.TradeSessionClosed, nil)
	s.closeAllChannels()
	return nil
}

func (s *simSession) isClosed() bool {
	s.mu.RLock(); defer s.mu.RUnlock(); return s.closed
}

func (s *simSession) closeAllChannels() {
	s.subsMu.Lock()
	for ch := range s.connSubs { close(ch) }
	for ch := range s.accountSubs { close(ch) }
	for ch := range s.positionSubs { close(ch) }
	for ch := range s.orderSubs { close(ch) }
	for ch := range s.tradeSubs { close(ch) }
	for ch := range s.notifySubs { close(ch) }
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

func (s *simSession) onQuoteUpdate(symbol string, quote api.Quote) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.latestQuotes[symbol] = quote
	aliveIDs := make([]string, 0)
	for id, o := range s.orders {
		if strings.EqualFold(o.Status, "ALIVE") && joinSymbol(o.ExchangeID, o.InstrumentID) == symbol {
			aliveIDs = append(aliveIDs, id)
		}
	}
	hasPosition := false
	if p, ok := s.positions[symbol]; ok {
		if p.VolumeLong > 0 || p.VolumeShort > 0 {
			hasPosition = true
		}
	}
	s.mu.Unlock()
	for _, id := range aliveIDs {
		s.tryMatch(id, quote)
	}
	// Recalculate float profit / margin when a position exists for this symbol
	if hasPosition {
		s.mu.Lock()
		s.recalcAccountLocked()
		acc := s.account
		pos := s.positions[symbol]
		s.mu.Unlock()
		s.emitAccountEvent(api.AccountEvent{Kind: api.TradeEventUpsert, AccountID: s.accountID, Account: acc, ChangedAt: time.Now()})
		s.emitPositionEvent(api.PositionEvent{Kind: api.TradeEventUpsert, AccountID: s.accountID, Symbol: symbol, Position: pos, ChangedAt: time.Now()})
	}
}

func (s *simSession) tryMatch(orderID string, quote api.Quote) {
	s.mu.Lock()
	order, ok := s.orders[orderID]
	if !ok || !strings.EqualFold(order.Status, "ALIVE") {
		s.mu.Unlock()
		return
	}
	status, msg, price := MatchOrder(order, quote)
	if status == "ALIVE" {
		s.mu.Unlock()
		return
	}
	order.Status = status
	order.LastMsg = msg
	if msg == "全部成交" {
		order.VolumeLeft = 0
		order.TradePrice = price
	} else {
		order.VolumeLeft = order.VolumeOrign
	}
	s.orders[orderID] = order
	var trade api.Trade
	tradeID := ""
	s.releaseFrozenLocked(orderID)
	if msg == "全部成交" {
		tradeID = fmt.Sprintf("BT_trade_%d", atomic.AddInt64(&s.svc.nextTradeSeq, 1))
		trade = api.Trade{
			OrderID:       order.OrderID,
			TradeID:       tradeID,
			ExchangeID:    order.ExchangeID,
			InstrumentID:  order.InstrumentID,
			Direction:     order.Direction,
			Offset:        order.Offset,
			Price:         price,
			Volume:        order.VolumeOrign,
			TradeDateTime: s.svc.rt.CurrentNS(),
		}
		s.trades[tradeID] = trade
		s.dailyTrades = append(s.dailyTrades, trade)
		s.applyTradeLocked(order, quote, trade)
	} else {
		s.recalcAccountLocked()
	}
	acc := s.account
	pos, _ := s.positions[joinSymbol(order.ExchangeID, order.InstrumentID)]
	s.mu.Unlock()

	s.emitOrderEvent(api.OrderEvent{Kind: api.TradeEventUpsert, AccountID: s.accountID, OrderID: orderID, Order: order, ChangedAt: time.Now()})
	if msg == "全部成交" {
		s.emitTradeEvent(api.TradeFillEvent{Kind: api.TradeEventUpsert, AccountID: s.accountID, TradeID: tradeID, Trade: trade, ChangedAt: time.Now()})
		s.emitAccountEvent(api.AccountEvent{Kind: api.TradeEventUpsert, AccountID: s.accountID, Account: acc, ChangedAt: time.Now()})
		s.emitPositionEvent(api.PositionEvent{Kind: api.TradeEventUpsert, AccountID: s.accountID, Symbol: joinSymbol(order.ExchangeID, order.InstrumentID), Position: pos, ChangedAt: time.Now()})
	}
}

func (s *simSession) applyTradeLocked(order api.Order, quote api.Quote, trade api.Trade) {
	symbol := joinSymbol(order.ExchangeID, order.InstrumentID)
	pos := s.positions[symbol]
	meta := s.svc.getMeta(symbol)
	vm := quote.VolumeMultiple
	if vm == 0 {
		vm = meta.VolumeMultiple
	}
	if vm == 0 {
		vm = 1
	}
	isOption := strings.EqualFold(meta.InsClass, "OPTION")
	offset := api.Offset(order.Offset)
	isBuy := strings.EqualFold(order.Direction, string(api.DirectionBuy))

	// Commission (GAP-06: per-symbol override)
	commission := FutureCommission(trade.Price, trade.Volume, vm, meta, s.svc.getCommissionOverrideForSymbol(symbol))
	s.account.Commission += commission

	if offset == api.OffsetOpen {
		if isBuy {
			// Open long
			oldVol := pos.VolumeLong
			newVol := oldVol + trade.Volume
			vmf := float64(vm)
			// GAP-13: accumulate costs, derive prices
			pos.OpenCostLong += trade.Price * float64(trade.Volume) * vmf
			pos.PositionCostLong += trade.Price * float64(trade.Volume) * vmf
			if newVol > 0 && vmf > 0 {
				pos.OpenPriceLong = pos.OpenCostLong / float64(newVol) / vmf
				pos.PositionPriceLong = pos.PositionCostLong / float64(newVol) / vmf
			}

			pos.VolumeLongToday += trade.Volume
			pos.PosLongToday += trade.Volume
			pos.VolumeLong = newVol
			pos.PosLong = newVol
			pos.Pos += trade.Volume
		} else {
			// Open short
			oldVol := pos.VolumeShort
			newVol := oldVol + trade.Volume
			vmf := float64(vm)
			// GAP-13: accumulate costs, derive prices
			pos.OpenCostShort += trade.Price * float64(trade.Volume) * vmf
			pos.PositionCostShort += trade.Price * float64(trade.Volume) * vmf
			if newVol > 0 && vmf > 0 {
				pos.OpenPriceShort = pos.OpenCostShort / float64(newVol) / vmf
				pos.PositionPriceShort = pos.PositionCostShort / float64(newVol) / vmf
			}

			pos.VolumeShortToday += trade.Volume
			pos.PosShortToday += trade.Volume
			pos.VolumeShort = newVol
			pos.PosShort = newVol
			pos.Pos -= trade.Volume
		}
		if isOption {
			premium := OptionPremium(order.Direction, trade.Price, trade.Volume, vm)
			s.account.Premium += premium
		}
	} else {
		// Close position
		if isBuy {
			// Buy to close short
			closeProfit := FutureCloseProfit("SHORT", trade.Price, pos.PositionPriceShort, trade.Volume, vm)
			s.account.CloseProfit += closeProfit
			// GAP-13: reduce costs before reducing volume
			pos.OpenCostShort -= pos.OpenPriceShort * float64(trade.Volume) * float64(vm)
			pos.PositionCostShort -= pos.PositionPriceShort * float64(trade.Volume) * float64(vm)
			s.reduceShortVolumeLocked(&pos, trade.Volume, offset)
		} else {
			// Sell to close long
			closeProfit := FutureCloseProfit("LONG", trade.Price, pos.PositionPriceLong, trade.Volume, vm)
			s.account.CloseProfit += closeProfit
			// GAP-13: reduce costs before reducing volume
			pos.OpenCostLong -= pos.OpenPriceLong * float64(trade.Volume) * float64(vm)
			pos.PositionCostLong -= pos.PositionPriceLong * float64(trade.Volume) * float64(vm)
			s.reduceLongVolumeLocked(&pos, trade.Volume, offset)
		}
		if isOption {
			premium := OptionPremium(order.Direction, trade.Price, trade.Volume, vm)
			s.account.Premium += premium
		}
	}

	pos.ExchangeID = order.ExchangeID
	pos.InstrumentID = order.InstrumentID
	s.positions[symbol] = pos

	// Full account recalculation
	s.recalcAccountLocked()
}

// reduceLongVolumeLocked removes volume from the long side.
// CLOSETODAY targets today's volume; CLOSE hits historical first (SHFE-style).
func (s *simSession) reduceLongVolumeLocked(pos *api.Position, volume int64, offset api.Offset) {
	remaining := volume
	if offset == api.OffsetCloseToday {
		close := remaining
		if close > pos.VolumeLongToday {
			close = pos.VolumeLongToday
		}
		pos.VolumeLongToday -= close
		pos.PosLongToday -= close
		remaining -= close
	} else {
		hisClose := remaining
		if hisClose > pos.VolumeLongHis {
			hisClose = pos.VolumeLongHis
		}
		pos.VolumeLongHis -= hisClose
		pos.PosLongHis -= hisClose
		remaining -= hisClose
		if remaining > 0 {
			todayClose := remaining
			if todayClose > pos.VolumeLongToday {
				todayClose = pos.VolumeLongToday
			}
			pos.VolumeLongToday -= todayClose
			pos.PosLongToday -= todayClose
		}
	}
	pos.VolumeLong -= volume
	pos.PosLong -= volume
	pos.Pos -= volume
}

// reduceShortVolumeLocked mirrors reduceLongVolumeLocked for the short side.
func (s *simSession) reduceShortVolumeLocked(pos *api.Position, volume int64, offset api.Offset) {
	remaining := volume
	if offset == api.OffsetCloseToday {
		close := remaining
		if close > pos.VolumeShortToday {
			close = pos.VolumeShortToday
		}
		pos.VolumeShortToday -= close
		pos.PosShortToday -= close
		remaining -= close
	} else {
		hisClose := remaining
		if hisClose > pos.VolumeShortHis {
			hisClose = pos.VolumeShortHis
		}
		pos.VolumeShortHis -= hisClose
		pos.PosShortHis -= hisClose
		remaining -= hisClose
		if remaining > 0 {
			todayClose := remaining
			if todayClose > pos.VolumeShortToday {
				todayClose = pos.VolumeShortToday
			}
			pos.VolumeShortToday -= todayClose
			pos.PosShortToday -= todayClose
		}
	}
	pos.VolumeShort -= volume
	pos.PosShort -= volume
	pos.Pos += volume
}

// recalcAccountLocked recomputes margin, float profit, position profit and balance
// from scratch based on current quotes and positions.
func (s *simSession) recalcAccountLocked() {
	totalMargin := 0.0
	totalFloatProfit := 0.0
	totalPositionProfit := 0.0

	for sym, pos := range s.positions {
		q, ok := s.latestQuotes[sym]
		if !ok || q.LastPrice <= 0 {
			continue
		}
		meta := s.svc.getMeta(sym)
		vm := q.VolumeMultiple
		if vm == 0 {
			vm = meta.VolumeMultiple
		}
		if vm == 0 {
			vm = 1
		}
		isOption := strings.EqualFold(meta.InsClass, "OPTION")

		// --- Long side ---
		if pos.VolumeLong > 0 {
			if isOption {
				// Long options: no margin, still has float profit
			} else {
				marginPerLot := FutureMarginPerLot(q.LastPrice, vm, meta, s.svc.getMarginOverrideForSymbol(sym))
				pos.MarginLong = marginPerLot * float64(pos.VolumeLong)
				totalMargin += pos.MarginLong
			}
			pos.FloatProfitLong = FutureFloatProfit("LONG", q.LastPrice, pos.OpenPriceLong, pos.VolumeLong, vm)
			pos.PositionProfitLong = FuturePositionProfit("LONG", q.LastPrice, pos.PositionPriceLong, pos.VolumeLong, vm)
			totalFloatProfit += pos.FloatProfitLong
			totalPositionProfit += pos.PositionProfitLong
		} else {
			pos.FloatProfitLong = 0
			pos.PositionProfitLong = 0
			pos.MarginLong = 0
		}

		// --- Short side ---
		if pos.VolumeShort > 0 {
			if isOption {
				underlyingQ, _ := s.latestQuotes[meta.UnderlyingSymbol]
				marginPerLot := OptionMarginPerLot(q.LastPrice, underlyingQ.LastPrice, meta.StrikePrice, vm, meta.OptionClass, meta)
				pos.MarginShort = marginPerLot * float64(pos.VolumeShort)
				totalMargin += pos.MarginShort
			} else {
				marginPerLot := FutureMarginPerLot(q.LastPrice, vm, meta, s.svc.getMarginOverrideForSymbol(sym))
				pos.MarginShort = marginPerLot * float64(pos.VolumeShort)
				totalMargin += pos.MarginShort
			}
			pos.FloatProfitShort = FutureFloatProfit("SHORT", q.LastPrice, pos.OpenPriceShort, pos.VolumeShort, vm)
			pos.PositionProfitShort = FuturePositionProfit("SHORT", q.LastPrice, pos.PositionPriceShort, pos.VolumeShort, vm)
			totalFloatProfit += pos.FloatProfitShort
			totalPositionProfit += pos.PositionProfitShort
		} else {
			pos.FloatProfitShort = 0
			pos.PositionProfitShort = 0
			pos.MarginShort = 0
		}

		pos.FloatProfit = pos.FloatProfitLong + pos.FloatProfitShort
		pos.PositionProfit = pos.PositionProfitLong + pos.PositionProfitShort
		pos.Margin = pos.MarginLong + pos.MarginShort
		s.positions[sym] = pos
	}

	s.account.FloatProfit = totalFloatProfit
	s.account.PositionProfit = totalPositionProfit
	s.account.Margin = totalMargin
	s.account.Balance = s.account.StaticBalance + totalPositionProfit + s.account.CloseProfit - s.account.Commission + s.account.Premium
	s.account.Available = s.account.Balance - totalMargin - s.account.FrozenMargin - s.account.FrozenPremium
	if s.account.Balance != 0 {
		s.account.RiskRatio = totalMargin / s.account.Balance
	}
}

// getMeta returns instrument metadata for a symbol via the EventKernel.
func (s *simTradeService) getMeta(symbol string) data.InstrumentMeta {
	return s.rt.Meta(symbol)
}

func (s *simTradeService) getMarginOverride() *float64 {
	cfg := s.rt.Config()
	return cfg.MarginOverride
}

// getMarginOverrideForSymbol returns the per-symbol margin override, falling back to global.
func (s *simTradeService) getMarginOverrideForSymbol(symbol string) *float64 {
	cfg := s.rt.Config()
	if cfg.MarginOverrides != nil {
		if v, ok := cfg.MarginOverrides[symbol]; ok {
			return &v
		}
	}
	return cfg.MarginOverride
}

func (s *simTradeService) getCommissionOverride() *float64 {
	cfg := s.rt.Config()
	return cfg.CommissionOverride
}

// getCommissionOverrideForSymbol returns the per-symbol commission override, falling back to global.
func (s *simTradeService) getCommissionOverrideForSymbol(symbol string) *float64 {
	cfg := s.rt.Config()
	if cfg.CommissionOverrides != nil {
		if v, ok := cfg.CommissionOverrides[symbol]; ok {
			return &v
		}
	}
	return cfg.CommissionOverride
}



func (s *simSession) newOrderRef(orderID string) api.OrderRef {
	ref := &orderRef{session: s, orderID: orderID, evCh: make(chan api.OrderEvent, 64)}
	s.refsMu.Lock()
	if s.orderRefs[orderID] == nil {
		s.orderRefs[orderID] = map[*orderRef]struct{}{}
	}
	s.orderRefs[orderID][ref] = struct{}{}
	s.refsMu.Unlock()
	return ref
}

func (s *simSession) emitOrderEvent(ev api.OrderEvent) {
	s.subsMu.Lock()
	for ch := range s.orderSubs {
		select { case ch <- ev: default: }
	}
	s.subsMu.Unlock()
	s.refsMu.Lock()
	for ref := range s.orderRefs[ev.OrderID] {
		ref.push(ev)
	}
	s.refsMu.Unlock()
}

func (s *simSession) emitTradeEvent(ev api.TradeFillEvent) {
	s.subsMu.Lock(); defer s.subsMu.Unlock()
	for ch := range s.tradeSubs {
		select { case ch <- ev: default: }
	}
}

func (s *simSession) emitPositionEvent(ev api.PositionEvent) {
	s.subsMu.Lock(); defer s.subsMu.Unlock()
	for ch := range s.positionSubs {
		select { case ch <- ev: default: }
	}
}

func (s *simSession) emitAccountEvent(ev api.AccountEvent) {
	s.subsMu.Lock(); defer s.subsMu.Unlock()
	for ch := range s.accountSubs {
		select { case ch <- ev: default: }
	}
}

func (s *simSession) removeConnSub(ch chan api.ConnEvent) {
	s.subsMu.Lock()
	if _, ok := s.connSubs[ch]; ok {
		delete(s.connSubs, ch)
		close(ch)
	}
	s.subsMu.Unlock()
}

func (s *simSession) removeAccountSub(ch chan api.AccountEvent) {
	s.subsMu.Lock(); if _, ok := s.accountSubs[ch]; ok { delete(s.accountSubs, ch); close(ch) }; s.subsMu.Unlock()
}
func (s *simSession) removePositionSub(ch chan api.PositionEvent) {
	s.subsMu.Lock(); if _, ok := s.positionSubs[ch]; ok { delete(s.positionSubs, ch); close(ch) }; s.subsMu.Unlock()
}
func (s *simSession) removeOrderSub(ch chan api.OrderEvent) {
	s.subsMu.Lock(); if _, ok := s.orderSubs[ch]; ok { delete(s.orderSubs, ch); close(ch) }; s.subsMu.Unlock()
}
func (s *simSession) removeTradeSub(ch chan api.TradeFillEvent) {
	s.subsMu.Lock(); if _, ok := s.tradeSubs[ch]; ok { delete(s.tradeSubs, ch); close(ch) }; s.subsMu.Unlock()
}
func (s *simSession) removeNotifySub(ch chan api.NotifyEvent) {
	s.subsMu.Lock(); if _, ok := s.notifySubs[ch]; ok { delete(s.notifySubs, ch); close(ch) }; s.subsMu.Unlock()
}



// --- Frozen order tracking ---

// orderFrozen tracks the margin/premium/position volumes frozen for a single pending order.
type orderFrozen struct {
	symbol           string
	frozenMargin     float64
	frozenPremium    float64
	longFrozenToday  int64
	longFrozenHis    int64
	shortFrozenToday int64
	shortFrozenHis   int64
}

// isSHFEOrINE returns true for exchanges that distinguish CLOSETODAY from CLOSE.
func isSHFEOrINE(exchangeID string) bool {
	e := strings.ToUpper(exchangeID)
	return e == "SHFE" || e == "INE"
}

// availableCloseVolumeLocked returns the position volume available for closing (total - frozen).
func availableCloseVolumeLocked(pos api.Position, side string, offset api.Offset, exchangeID string) int64 {
	isSHFE := isSHFEOrINE(exchangeID)
	if strings.EqualFold(side, "LONG") {
		if isSHFE && offset == api.OffsetCloseToday {
			return pos.VolumeLongToday - pos.VolumeLongFrozenToday
		}
		return pos.VolumeLong - pos.VolumeLongFrozen
	}
	if isSHFE && offset == api.OffsetCloseToday {
		return pos.VolumeShortToday - pos.VolumeShortFrozenToday
	}
	return pos.VolumeShort - pos.VolumeShortFrozen
}

// freezePositionVolumeLocked freezes position volumes for a close order.
// For SHFE/INE CLOSETODAY: freezes today only.
// For CLOSE or non-SHFE: freezes history first, then today.
func freezePositionVolumeLocked(pos *api.Position, frozen *orderFrozen, side string, vol int64, offset api.Offset, exchangeID string) {
	isSHFE := isSHFEOrINE(exchangeID)
	if strings.EqualFold(side, "LONG") {
		if isSHFE && offset == api.OffsetCloseToday {
			frozen.longFrozenToday = vol
			pos.VolumeLongFrozenToday += vol
		} else {
			his := vol
			avHis := pos.VolumeLongHis - pos.VolumeLongFrozenHis
			if his > avHis {
				his = avHis
			}
			today := vol - his
			frozen.longFrozenHis = his
			frozen.longFrozenToday = today
			pos.VolumeLongFrozenHis += his
			pos.VolumeLongFrozenToday += today
		}
		pos.VolumeLongFrozen += vol
	} else {
		if isSHFE && offset == api.OffsetCloseToday {
			frozen.shortFrozenToday = vol
			pos.VolumeShortFrozenToday += vol
		} else {
			his := vol
			avHis := pos.VolumeShortHis - pos.VolumeShortFrozenHis
			if his > avHis {
				his = avHis
			}
			today := vol - his
			frozen.shortFrozenHis = his
			frozen.shortFrozenToday = today
			pos.VolumeShortFrozenHis += his
			pos.VolumeShortFrozenToday += today
		}
		pos.VolumeShortFrozen += vol
	}
}

// releaseFrozenLocked releases frozen margin/premium/position volumes for an order.
// Must be called with s.mu held.
func (s *simSession) releaseFrozenLocked(orderID string) {
	frozen, ok := s.orderFrozens[orderID]
	if !ok {
		return
	}
	delete(s.orderFrozens, orderID)
	s.account.FrozenMargin -= frozen.frozenMargin
	s.account.FrozenPremium -= frozen.frozenPremium
	if frozen.longFrozenToday > 0 || frozen.longFrozenHis > 0 ||
		frozen.shortFrozenToday > 0 || frozen.shortFrozenHis > 0 {
		pos := s.positions[frozen.symbol]
		pos.VolumeLongFrozenToday -= frozen.longFrozenToday
		pos.VolumeLongFrozenHis -= frozen.longFrozenHis
		pos.VolumeLongFrozen -= (frozen.longFrozenToday + frozen.longFrozenHis)
		pos.VolumeShortFrozenToday -= frozen.shortFrozenToday
		pos.VolumeShortFrozenHis -= frozen.shortFrozenHis
		pos.VolumeShortFrozen -= (frozen.shortFrozenToday + frozen.shortFrozenHis)
		s.positions[frozen.symbol] = pos
	}
}

func splitSymbol(symbol string) (exchangeID, instrumentID string) {
	parts := strings.SplitN(symbol, ".", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "SIM", symbol
}

func joinSymbol(exchangeID, instrumentID string) string {
	if exchangeID == "" {
		return instrumentID
	}
	return exchangeID + "." + instrumentID
}

func bindSubContext(ctx context.Context, closeFn func()) {
	select {
	case <-ctx.Done():
		closeFn()
	}
}

type orderRef struct {
	session *simSession
	orderID string
	evCh    chan api.OrderEvent
	once    sync.Once
}

func (r *orderRef) AccountID() string { return r.session.accountID }
func (r *orderRef) OrderID() string   { return r.orderID }
func (r *orderRef) Snapshot() (api.Order, bool) { return r.session.Order(r.orderID) }
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
	r.once.Do(func() { close(r.evCh) })
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

type tradingStatusSub struct {
	ch      chan api.TradingStatusEvent
	once    sync.Once
}

func (s *accountSub) C() <-chan api.AccountEvent   { return s.ch }
func (s *positionSub) C() <-chan api.PositionEvent { return s.ch }
func (s *orderSub) C() <-chan api.OrderEvent       { return s.ch }
func (s *tradeSub) C() <-chan api.TradeFillEvent   { return s.ch }
func (s *notifySub) C() <-chan api.NotifyEvent     { return s.ch }
func (s *tradingStatusSub) C() <-chan api.TradingStatusEvent { return s.ch }

func (s *accountSub) Close() error   { s.once.Do(func() { if s.closeFn != nil { s.closeFn() } }); return nil }
func (s *positionSub) Close() error  { s.once.Do(func() { if s.closeFn != nil { s.closeFn() } }); return nil }
func (s *orderSub) Close() error     { s.once.Do(func() { if s.closeFn != nil { s.closeFn() } }); return nil }
func (s *tradeSub) Close() error     { s.once.Do(func() { if s.closeFn != nil { s.closeFn() } }); return nil }
func (s *notifySub) Close() error    { s.once.Do(func() { if s.closeFn != nil { s.closeFn() } }); return nil }
func (s *tradingStatusSub) Close() error { s.once.Do(func() { close(s.ch) }); return nil }
