package api

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var builtinRuleSeq uint64

type OrderRateLimitRule struct {
	id             string
	limitPerSecond int
	exchanges      map[string]struct{}

	mu            sync.Mutex
	insertCancel  map[string][]time.Time
	orderExchange map[string]string
}

func NewOrderRateLimitRule(ruleID string, exchanges []string, limitPerSecond int) *OrderRateLimitRule {
	es := map[string]struct{}{}
	for _, ex := range exchanges {
		if ex == "" {
			continue
		}
		es[ex] = struct{}{}
	}
	return &OrderRateLimitRule{
		id:             ruleID,
		limitPerSecond: limitPerSecond,
		exchanges:      es,
		insertCancel:   map[string][]time.Time{},
		orderExchange:  map[string]string{},
	}
}

func OrderRateLimit(exchanges []string, limitPerSecond int) RiskRule {
	return NewOrderRateLimitRule(nextBuiltinRuleID("order_rate_limit"), exchanges, limitPerSecond)
}

func (r *OrderRateLimitRule) ID() string { return r.id }

func (r *OrderRateLimitRule) CouldInsertOrder(req InsertOrderCheckReq) *RiskReject {
	return r.could(req.ExchangeID)
}

func (r *OrderRateLimitRule) OnInsertOrder(req InsertOrderApplyReq) {
	r.on(req.ExchangeID)
}

func (r *OrderRateLimitRule) CouldCancelOrder(req CancelOrderCheckReq) *RiskReject {
	exchangeID := req.ExchangeID
	if exchangeID == "" {
		r.mu.Lock()
		exchangeID = r.orderExchange[req.OrderID]
		r.mu.Unlock()
	}
	return r.could(exchangeID)
}

func (r *OrderRateLimitRule) OnCancelOrder(req CancelOrderApplyReq) {
	exchangeID := req.ExchangeID
	if exchangeID == "" {
		r.mu.Lock()
		exchangeID = r.orderExchange[req.OrderID]
		r.mu.Unlock()
	}
	r.on(exchangeID)
}

func (r *OrderRateLimitRule) OnSettle() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.insertCancel = map[string][]time.Time{}
	r.orderExchange = map[string]string{}
}

func (r *OrderRateLimitRule) BindOrderExchange(orderID string, exchangeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if orderID == "" || exchangeID == "" {
		return
	}
	r.orderExchange[orderID] = exchangeID
}

func (r *OrderRateLimitRule) could(exchangeID string) *RiskReject {
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
	window := trimOneSecond(r.insertCancel[exchangeID], now)
	if len(window)+1 > r.limitPerSecond {
		return &RiskReject{
			RuleID: r.id,
			Reason: "order rate exceeded",
			Details: map[string]any{
				"exchange_id": exchangeID,
				"limit":       r.limitPerSecond,
				"current":     len(window),
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
	window := trimOneSecond(r.insertCancel[exchangeID], now)
	r.insertCancel[exchangeID] = append(window, now)
}

func trimOneSecond(arr []time.Time, now time.Time) []time.Time {
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

type RiskBootstrapper interface {
	BootstrapFromSnapshot(orders map[string]Order, trades map[string]Trade)
}

type OpenCountsLimitRule struct {
	id      string
	limit   int64
	symbols map[string]struct{}

	mu    sync.Mutex
	count int64
}

func NewOpenCountsLimitRule(ruleID string, symbols []string, limit int64) *OpenCountsLimitRule {
	return &OpenCountsLimitRule{
		id:      strings.TrimSpace(ruleID),
		limit:   limit,
		symbols: buildSymbolSet(symbols),
	}
}

func OpenCountsLimit(symbols []string, limit int64) RiskRule {
	return NewOpenCountsLimitRule(nextBuiltinRuleID("open_counts_limit"), symbols, limit)
}

func (r *OpenCountsLimitRule) ID() string { return r.id }

func (r *OpenCountsLimitRule) CouldInsertOrder(req InsertOrderCheckReq) *RiskReject {
	if req.Offset != OffsetOpen || !r.match(req.Symbol) || r.limit <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count+1 > r.limit {
		return &RiskReject{
			RuleID: r.id,
			Reason: "open counts limit exceeded",
			Details: map[string]any{
				"symbol":  req.Symbol,
				"limit":   r.limit,
				"current": r.count,
			},
		}
	}
	return nil
}

func (r *OpenCountsLimitRule) OnInsertOrder(req InsertOrderApplyReq) {
	if req.Offset != OffsetOpen || !r.match(req.Symbol) {
		return
	}
	r.mu.Lock()
	r.count++
	r.mu.Unlock()
}

func (r *OpenCountsLimitRule) CouldCancelOrder(req CancelOrderCheckReq) *RiskReject {
	_ = req
	return nil
}

func (r *OpenCountsLimitRule) OnCancelOrder(req CancelOrderApplyReq) {
	_ = req
}

func (r *OpenCountsLimitRule) OnSettle() {
	r.mu.Lock()
	r.count = 0
	r.mu.Unlock()
}

func (r *OpenCountsLimitRule) BootstrapFromSnapshot(orders map[string]Order, trades map[string]Trade) {
	_ = trades
	if r.limit <= 0 {
		return
	}
	var c int64
	for _, o := range orders {
		if strings.EqualFold(strings.TrimSpace(o.Offset), string(OffsetOpen)) && r.match(joinSymbol(o.ExchangeID, o.InstrumentID)) {
			c++
		}
	}
	r.mu.Lock()
	r.count = c
	r.mu.Unlock()
}

type OpenVolumesLimitRule struct {
	id      string
	limit   int64
	symbols map[string]struct{}

	mu     sync.Mutex
	bySym  map[string]int64
	total  int64
	bySeen map[string]struct{}
}

func NewOpenVolumesLimitRule(ruleID string, symbols []string, limit int64) *OpenVolumesLimitRule {
	return &OpenVolumesLimitRule{
		id:      strings.TrimSpace(ruleID),
		limit:   limit,
		symbols: buildSymbolSet(symbols),
		bySym:   map[string]int64{},
		bySeen:  map[string]struct{}{},
	}
}

func OpenVolumesLimit(symbols []string, limit int64) RiskRule {
	return NewOpenVolumesLimitRule(nextBuiltinRuleID("open_volumes_limit"), symbols, limit)
}

func (r *OpenVolumesLimitRule) ID() string { return r.id }

func (r *OpenVolumesLimitRule) CouldInsertOrder(req InsertOrderCheckReq) *RiskReject {
	if req.Offset != OffsetOpen || !r.match(req.Symbol) || r.limit <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.bySym[req.Symbol]
	if cur+int64(req.Volume) > r.limit {
		return &RiskReject{
			RuleID: r.id,
			Reason: "open volumes limit exceeded",
			Details: map[string]any{
				"symbol":  req.Symbol,
				"limit":   r.limit,
				"current": cur,
				"volume":  req.Volume,
			},
		}
	}
	return nil
}

func (r *OpenVolumesLimitRule) OnInsertOrder(req InsertOrderApplyReq) {
	if req.Offset != OffsetOpen || !r.match(req.Symbol) {
		return
	}
	r.mu.Lock()
	r.bySym[req.Symbol] += int64(req.Volume)
	r.total += int64(req.Volume)
	r.mu.Unlock()
}

func (r *OpenVolumesLimitRule) CouldCancelOrder(req CancelOrderCheckReq) *RiskReject {
	_ = req
	return nil
}

func (r *OpenVolumesLimitRule) OnCancelOrder(req CancelOrderApplyReq) {
	_ = req
}

func (r *OpenVolumesLimitRule) OnSettle() {
	r.mu.Lock()
	r.bySym = map[string]int64{}
	r.bySeen = map[string]struct{}{}
	r.total = 0
	r.mu.Unlock()
}

func (r *OpenVolumesLimitRule) BootstrapFromSnapshot(orders map[string]Order, trades map[string]Trade) {
	_ = orders
	if r.limit <= 0 {
		return
	}
	bySym := map[string]int64{}
	total := int64(0)
	seen := map[string]struct{}{}
	for tradeID, tr := range trades {
		if tradeID == "" {
			tradeID = tr.TradeID
		}
		if tradeID != "" {
			if _, ok := seen[tradeID]; ok {
				continue
			}
			seen[tradeID] = struct{}{}
		}
		if !strings.EqualFold(strings.TrimSpace(tr.Offset), string(OffsetOpen)) {
			continue
		}
		sym := joinSymbol(tr.ExchangeID, tr.InstrumentID)
		if !r.match(sym) {
			continue
		}
		bySym[sym] += tr.Volume
		total += tr.Volume
	}
	r.mu.Lock()
	r.bySym = bySym
	r.total = total
	r.bySeen = seen
	r.mu.Unlock()
}

type AccOpenVolumesLimitRule struct {
	id      string
	limit   int64
	symbols map[string]struct{}

	mu        sync.Mutex
	total     int64
	seenTrade map[string]struct{}
}

func NewAccOpenVolumesLimitRule(ruleID string, symbols []string, limit int64) *AccOpenVolumesLimitRule {
	return &AccOpenVolumesLimitRule{
		id:        strings.TrimSpace(ruleID),
		limit:     limit,
		symbols:   buildSymbolSet(symbols),
		seenTrade: map[string]struct{}{},
	}
}

func AccOpenVolumesLimit(symbols []string, limit int64) RiskRule {
	return NewAccOpenVolumesLimitRule(nextBuiltinRuleID("acc_open_volumes_limit"), symbols, limit)
}

func (r *AccOpenVolumesLimitRule) ID() string { return r.id }

func (r *AccOpenVolumesLimitRule) CouldInsertOrder(req InsertOrderCheckReq) *RiskReject {
	if req.Offset != OffsetOpen || !r.match(req.Symbol) || r.limit <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.total+int64(req.Volume) > r.limit {
		return &RiskReject{
			RuleID: r.id,
			Reason: "acc open volumes limit exceeded",
			Details: map[string]any{
				"limit":   r.limit,
				"current": r.total,
				"volume":  req.Volume,
			},
		}
	}
	return nil
}

func (r *AccOpenVolumesLimitRule) OnInsertOrder(req InsertOrderApplyReq) {
	if req.Offset != OffsetOpen || !r.match(req.Symbol) {
		return
	}
	r.mu.Lock()
	r.total += int64(req.Volume)
	r.mu.Unlock()
}

func (r *AccOpenVolumesLimitRule) CouldCancelOrder(req CancelOrderCheckReq) *RiskReject {
	_ = req
	return nil
}

func (r *AccOpenVolumesLimitRule) OnCancelOrder(req CancelOrderApplyReq) {
	_ = req
}

func (r *AccOpenVolumesLimitRule) OnSettle() {
	r.mu.Lock()
	r.total = 0
	r.seenTrade = map[string]struct{}{}
	r.mu.Unlock()
}

func (r *AccOpenVolumesLimitRule) BootstrapFromSnapshot(orders map[string]Order, trades map[string]Trade) {
	_ = orders
	if r.limit <= 0 {
		return
	}
	total := int64(0)
	seen := map[string]struct{}{}
	for tradeID, tr := range trades {
		if tradeID == "" {
			tradeID = tr.TradeID
		}
		if tradeID != "" {
			if _, ok := seen[tradeID]; ok {
				continue
			}
			seen[tradeID] = struct{}{}
		}
		if !strings.EqualFold(strings.TrimSpace(tr.Offset), string(OffsetOpen)) {
			continue
		}
		if !r.match(joinSymbol(tr.ExchangeID, tr.InstrumentID)) {
			continue
		}
		total += tr.Volume
	}
	r.mu.Lock()
	r.total = total
	r.seenTrade = seen
	r.mu.Unlock()
}

func (r *OpenCountsLimitRule) match(symbol string) bool {
	if len(r.symbols) == 0 {
		return symbol != ""
	}
	_, ok := r.symbols[symbol]
	return ok
}

func (r *OpenVolumesLimitRule) match(symbol string) bool {
	if len(r.symbols) == 0 {
		return symbol != ""
	}
	_, ok := r.symbols[symbol]
	return ok
}

func (r *AccOpenVolumesLimitRule) match(symbol string) bool {
	if len(r.symbols) == 0 {
		return symbol != ""
	}
	_, ok := r.symbols[symbol]
	return ok
}

func buildSymbolSet(symbols []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, s := range symbols {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out[s] = struct{}{}
	}
	return out
}

func joinSymbol(exchangeID string, instrumentID string) string {
	exchangeID = strings.TrimSpace(exchangeID)
	instrumentID = strings.TrimSpace(instrumentID)
	if exchangeID == "" || instrumentID == "" {
		return ""
	}
	return exchangeID + "." + instrumentID
}

func nextBuiltinRuleID(prefix string) string {
	id := atomic.AddUint64(&builtinRuleSeq, 1)
	return fmt.Sprintf("%s_%d", prefix, id)
}
