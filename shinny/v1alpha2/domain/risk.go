package domain

import (
	"sync"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
)

type RiskManager struct {
	mu         sync.RWMutex
	rules      []api.RiskRule
	tradingDay string
}

type orderExchangeBinder interface {
	BindOrderExchange(orderID string, exchangeID string)
}

func NewRiskManager() *RiskManager {
	return &RiskManager{}
}

func (m *RiskManager) Add(rule api.RiskRule) error {
	if rule == nil {
		return api.NewError(api.ErrInvalidArgument, "nil risk rule", nil)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rules {
		if r.ID() == rule.ID() {
			return api.NewError(api.ErrInvalidArgument, "duplicated risk rule id", nil)
		}
	}
	m.rules = append(m.rules, rule)
	return nil
}

func (m *RiskManager) Remove(ruleID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range m.rules {
		if r.ID() != ruleID {
			continue
		}
		m.rules = append(m.rules[:i], m.rules[i+1:]...)
		return nil
	}
	return api.NewError(api.ErrInvalidArgument, "risk rule not found", nil)
}

func (m *RiskManager) BeforeInsert(req api.InsertOrderCheckReq) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, r := range m.rules {
		rej := r.CouldInsertOrder(req)
		if rej == nil {
			continue
		}
		return &api.RiskError{Code: api.ErrRiskRejected, RuleID: rej.RuleID, Reason: rej.Reason, Details: rej.Details}
	}
	return nil
}

func (m *RiskManager) AfterInsert(req api.InsertOrderApplyReq) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, r := range m.rules {
		r.OnInsertOrder(req)
	}
}

func (m *RiskManager) BeforeCancel(req api.CancelOrderCheckReq) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, r := range m.rules {
		rej := r.CouldCancelOrder(req)
		if rej == nil {
			continue
		}
		return &api.RiskError{Code: api.ErrRiskRejected, RuleID: rej.RuleID, Reason: rej.Reason, Details: rej.Details}
	}
	return nil
}

func (m *RiskManager) AfterCancel(req api.CancelOrderApplyReq) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, r := range m.rules {
		r.OnCancelOrder(req)
	}
}

func (m *RiskManager) OnSettle() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, r := range m.rules {
		r.OnSettle()
	}
}

func (m *RiskManager) OnRecvDiff(view api.TradeDiffView) {
	m.mu.Lock()
	prev := m.tradingDay
	td := view.TradingDay
	if td != "" {
		m.tradingDay = td
	}
	rules := append([]api.RiskRule(nil), m.rules...)
	m.mu.Unlock()

	for _, r := range rules {
		if obs, ok := r.(api.RiskDiffObserver); ok {
			obs.OnRecvDiff(view)
		}
	}
	if td != "" && prev != "" && prev != td {
		for _, r := range rules {
			r.OnSettle()
		}
	}
}

func (m *RiskManager) Bootstrap(orders map[string]api.Order, trades map[string]api.Trade) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, r := range m.rules {
		if b, ok := r.(api.RiskBootstrapper); ok {
			b.BootstrapFromSnapshot(orders, trades)
		}
	}
}

func (m *RiskManager) BindOrderExchange(orderID string, exchangeID string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, r := range m.rules {
		if b, ok := r.(orderExchangeBinder); ok {
			b.BindOrderExchange(orderID, exchangeID)
		}
	}
}

type OrderRateLimitRule struct {
	id             string
	limitPerSecond int
	exchanges      map[string]struct{}
	mu             sync.Mutex
	insertCancelTS map[string][]time.Time
	orderExchanges map[string]string
}

func NewOrderRateLimitRule(ruleID string, exchanges []string, limitPerSecond int) *OrderRateLimitRule {
	es := map[string]struct{}{}
	for _, e := range exchanges {
		es[e] = struct{}{}
	}
	return &OrderRateLimitRule{
		id:             ruleID,
		limitPerSecond: limitPerSecond,
		exchanges:      es,
		insertCancelTS: map[string][]time.Time{},
		orderExchanges: map[string]string{},
	}
}

func (r *OrderRateLimitRule) ID() string {
	return r.id
}

func (r *OrderRateLimitRule) CouldInsertOrder(req api.InsertOrderCheckReq) *api.RiskReject {
	return r.could(req.ExchangeID)
}

func (r *OrderRateLimitRule) OnInsertOrder(req api.InsertOrderApplyReq) {
	r.on(req.ExchangeID)
}

func (r *OrderRateLimitRule) CouldCancelOrder(req api.CancelOrderCheckReq) *api.RiskReject {
	ex := req.ExchangeID
	if ex == "" {
		r.mu.Lock()
		ex = r.orderExchanges[req.OrderID]
		r.mu.Unlock()
	}
	if ex == "" {
		return nil
	}
	return r.could(ex)
}

func (r *OrderRateLimitRule) OnCancelOrder(req api.CancelOrderApplyReq) {
	ex := req.ExchangeID
	if ex == "" {
		r.mu.Lock()
		ex = r.orderExchanges[req.OrderID]
		r.mu.Unlock()
	}
	if ex == "" {
		return
	}
	r.on(ex)
}

func (r *OrderRateLimitRule) OnSettle() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.insertCancelTS = map[string][]time.Time{}
	r.orderExchanges = map[string]string{}
}

func (r *OrderRateLimitRule) BindOrderExchange(orderID string, exchangeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if orderID != "" && exchangeID != "" {
		r.orderExchanges[orderID] = exchangeID
	}
}

func (r *OrderRateLimitRule) could(exchangeID string) *api.RiskReject {
	if r.limitPerSecond <= 0 || exchangeID == "" {
		return nil
	}
	if len(r.exchanges) > 0 {
		if _, ok := r.exchanges[exchangeID]; !ok {
			return nil
		}
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	arr := trimRecent(r.insertCancelTS[exchangeID], now)
	if len(arr)+1 > r.limitPerSecond {
		return &api.RiskReject{
			RuleID: r.id,
			Reason: "order rate exceeded",
			Details: map[string]any{
				"exchange_id": exchangeID,
				"limit":       r.limitPerSecond,
				"current":     len(arr),
			},
		}
	}
	return nil
}

func (r *OrderRateLimitRule) on(exchangeID string) {
	if r.limitPerSecond <= 0 || exchangeID == "" {
		return
	}
	if len(r.exchanges) > 0 {
		if _, ok := r.exchanges[exchangeID]; !ok {
			return
		}
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	arr := trimRecent(r.insertCancelTS[exchangeID], now)
	arr = append(arr, now)
	r.insertCancelTS[exchangeID] = arr
}

func trimRecent(arr []time.Time, now time.Time) []time.Time {
	if len(arr) == 0 {
		return arr
	}
	cut := now.Add(-1 * time.Second)
	idx := 0
	for idx < len(arr) && arr[idx].Before(cut) {
		idx++
	}
	if idx == 0 {
		return arr
	}
	out := make([]time.Time, len(arr)-idx)
	copy(out, arr[idx:])
	return out
}
