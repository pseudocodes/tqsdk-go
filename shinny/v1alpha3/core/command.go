package core

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

var (
	ErrDuplicateOrder         = errors.New("tqsdk: duplicate order id")
	ErrRiskNotReady           = errors.New("tqsdk: risk trading day or baseline unavailable")
	ErrRiskContextUnavailable = errors.New("tqsdk: risk command context unavailable")
	ErrRequestInProgress      = errors.New("tqsdk: request already in progress")
)

// OrderPrice 区分限价与市价，不用 0/NaN 兼作价格类型。
type OrderPrice struct {
	market bool
	value  float64
}

// LimitPrice 返回限价。
func LimitPrice(v float64) OrderPrice { return OrderPrice{value: v} }

// MarketPrice 返回市价（price_type=ANY）。
func MarketPrice() OrderPrice { return OrderPrice{market: true} }

// IsMarket 报告是否市价。
func (p OrderPrice) IsMarket() bool { return p.market }

// Value 返回限价价格。
func (p OrderPrice) Value() float64 { return p.value }

// OrderRequest 是报单请求（期货/期权）。
type OrderRequest struct {
	ClientOrderID string // 线上 order_id；为空时自动生成
	Symbol        string // 如 "SHFE.rb2701"
	Direction     model.Direction
	Offset        model.Offset
	Volume        int64
	Price         OrderPrice
	Advanced      string // ""（限价 GFD / 市价 IOC）、"FAK"、"FOK"
}

// CommandState 是命令的本地发送状态；服务端结果另看委托单状态。
type CommandState uint8

const (
	CommandQueued  CommandState = iota // 已接受并排队
	CommandWritten                     // 已写入连接
	CommandNotSent                     // 确定未发送（写失败或断线时仍在队列）
)

// orderCmd 是一次已接受报单的本地记录。
type orderCmd struct {
	req      OrderRequest
	exchange string
	mu       sync.Mutex
	state    CommandState
	err      error
}

func (c *orderCmd) done(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.state, c.err = CommandNotSent, err
	} else {
		c.state = CommandWritten
	}
}

// OrderRef 是委托单的稳定引用：本地报单请求与服务端状态入口分开保存。
type OrderRef struct {
	a    *AccountHandle
	node *objNode[model.Order]
	cmd  *orderCmd // 非本地报单时为 nil
}

// OrderRef 返回委托单引用（用于 IsChanging 或读取）。
func (a *AccountHandle) OrderRef(orderID string) *OrderRef {
	a.t.rt.mu.Lock()
	defer a.t.rt.mu.Unlock()
	return &OrderRef{a: a, node: nodeOf(a, a.orders, orderID), cmd: a.commands[orderID]}
}

// ID 返回 order_id。
func (r *OrderRef) ID() string { return r.node.key }

// Snapshot 返回服务端委托单、账户状态以及是否已收到当前代次回报。
func (r *OrderRef) Snapshot() (model.Order, DataState, bool) {
	defer r.a.lock()()
	return r.node.value, r.a.state, current(r.a, r.node)
}

// Request 返回本地报单请求；非本地报单返回 false。
func (r *OrderRef) Request() (OrderRequest, bool) {
	if r.cmd == nil {
		return OrderRequest{}, false
	}
	return r.cmd.req, true
}

// SendState 返回本地发送状态；非本地报单返回 CommandWritten。
func (r *OrderRef) SendState() (CommandState, error) {
	if r.cmd == nil {
		return CommandWritten, nil
	}
	r.cmd.mu.Lock()
	defer r.cmd.mu.Unlock()
	return r.cmd.state, r.cmd.err
}

type insertOrderPack struct {
	Aid             string   `json:"aid"`
	UserID          string   `json:"user_id"`
	OrderID         string   `json:"order_id"`
	ExchangeID      string   `json:"exchange_id"`
	InstrumentID    string   `json:"instrument_id"`
	Direction       string   `json:"direction"`
	Offset          string   `json:"offset"`
	Volume          int64    `json:"volume"`
	PriceType       string   `json:"price_type"`
	LimitPrice      *float64 `json:"limit_price,omitempty"`
	VolumeCondition string   `json:"volume_condition"`
	TimeCondition   string   `json:"time_condition"`
}

var noMarketOrder = map[string]bool{"CFFEX": true, "SHFE": true, "INE": true, "SSE": true, "SZSE": true}

// buildInsertOrder 按 Python _get_insert_order_future_pack 生成报单。
func (a *AccountHandle) buildInsertOrder(r *OrderRequest) (insertOrderPack, error) {
	exchange, instrument, ok := strings.Cut(r.Symbol, ".")
	if !ok || exchange == "" || instrument == "" {
		return insertOrderPack{}, invalid("symbol %q must be EXCHANGE.INSTRUMENT", r.Symbol)
	}
	if r.Direction != model.DirectionBuy && r.Direction != model.DirectionSell {
		return insertOrderPack{}, invalid("direction %q", r.Direction)
	}
	if r.Offset != model.OffsetOpen && r.Offset != model.OffsetClose && r.Offset != model.OffsetCloseToday {
		return insertOrderPack{}, invalid("offset %q", r.Offset)
	}
	if r.Volume <= 0 {
		return insertOrderPack{}, invalid("volume %d", r.Volume)
	}
	if r.Advanced != "" && r.Advanced != "FAK" && r.Advanced != "FOK" {
		return insertOrderPack{}, invalid("advanced %q", r.Advanced)
	}
	if r.ClientOrderID == "" {
		if a.cfg.NewOrderID != nil {
			r.ClientOrderID = a.cfg.NewOrderID()
		} else {
			r.ClientOrderID = fmt.Sprintf("TQGO_insert_%016x%016x", rand.Uint64(), rand.Uint64())
		}
	}
	p := insertOrderPack{Aid: "insert_order", UserID: a.cfg.UserName, OrderID: r.ClientOrderID,
		ExchangeID: exchange, InstrumentID: instrument, Direction: string(r.Direction), Offset: string(r.Offset),
		Volume: r.Volume, VolumeCondition: "ANY"}
	if r.Advanced == "FOK" {
		p.VolumeCondition = "ALL"
	}
	if r.Price.market {
		if noMarketOrder[exchange] {
			return insertOrderPack{}, invalid("%s does not support market orders", exchange)
		}
		p.PriceType, p.TimeCondition = "ANY", "IOC"
	} else {
		v := r.Price.value
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return insertOrderPack{}, invalid("limit price %v", v)
		}
		p.PriceType, p.LimitPrice, p.TimeCondition = "LIMIT", &v, "GFD"
		if r.Advanced != "" {
			p.TimeCondition = "IOC"
		}
	}
	return p, nil
}

// commandRoute 检查账户生命周期与发送队列；调用方持有写锁。
func (a *AccountHandle) commandRoute() error {
	switch {
	case a.closed:
		return ErrClosed
	case a.route == nil:
		return ErrNotConnected
	}
	return a.route.canSend()
}

// Submit 报单。成功只表示本地已接受并排队发送，服务端结果看 OrderRef 的委托单状态。
// 已挂载的 CommandGuard 在同一临界区内检查、记账与入队。
func (a *AccountHandle) Submit(ctx context.Context, r OrderRequest) (*OrderRef, error) {
	pack, err := a.buildInsertOrder(&r)
	if err != nil {
		return nil, err
	}
	data := mustJSON(pack)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.t.rt.mu.Lock()
	defer a.t.rt.mu.Unlock()
	if err := a.commandRoute(); err != nil {
		return nil, err
	}
	if _, ok := a.commands[r.ClientOrderID]; ok || current(a, a.orders[r.ClientOrderID]) {
		return nil, ErrDuplicateOrder
	}
	cmd := a.newCommand(CommandSubmit, r.ClientOrderID, r.Symbol, pack.ExchangeID)
	cmd.Order = r
	if err := a.guardCheck(cmd); err != nil {
		return nil, err
	}
	oc := &orderCmd{req: r, exchange: pack.ExchangeID}
	a.route.send(data, oc.done) // canSend 已在写锁内确认容量
	a.commands[r.ClientOrderID] = oc
	return &OrderRef{a: a, node: nodeOf(a, a.orders, r.ClientOrderID), cmd: oc}, nil
}

// Cancel 撤单。成功只表示本地已接受并排队；订单消失不等于撤单成功。
func (a *AccountHandle) Cancel(ctx context.Context, orderID string) error {
	if orderID == "" {
		return invalid("empty order id")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	data := mustJSON(struct {
		Aid     string `json:"aid"`
		UserID  string `json:"user_id"`
		OrderID string `json:"order_id"`
	}{"cancel_order", a.cfg.UserName, orderID})
	a.t.rt.mu.Lock()
	defer a.t.rt.mu.Unlock()
	if err := a.commandRoute(); err != nil {
		return err
	}
	symbol, exchange := "", ""
	if n := a.orders[orderID]; current(a, n) {
		exchange, symbol = n.value.ExchangeID, n.value.ExchangeID+"."+n.value.InstrumentID
	} else if oc := a.commands[orderID]; oc != nil { // 刚报单尚无回报
		exchange, symbol = oc.exchange, oc.req.Symbol
	}
	if err := a.guardCheck(a.newCommand(CommandCancel, orderID, symbol, exchange)); err != nil {
		return err
	}
	return a.route.send(data, nil)
}

// ConfirmSettlement 确认当前连接代次的结算单；不跨代次自动重放，不经过报撤单风控。
func (a *AccountHandle) ConfirmSettlement(ctx context.Context) error {
	a.t.rt.mu.Lock()
	defer a.t.rt.mu.Unlock()
	if err := a.commandRoute(); err != nil {
		return err
	}
	return a.route.send([]byte(`{"aid":"confirm_settlement"}`), nil)
}

func (a *AccountHandle) newCommand(kind TradeCommandKind, orderID, symbol, exchange string) TradeCommand {
	a.cmdSeq++
	return TradeCommand{ID: a.cmdSeq, Kind: kind, AccountID: a.cfg.ID, TradingDay: a.scope,
		OrderID: orderID, Symbol: symbol, Exchange: exchange, At: a.t.rt.Now()}
}

// guardCheck 依次检查全部规则，全部通过才统一记账。调用方持有写锁且已确认可入队。
func (a *AccountHandle) guardCheck(cmd TradeCommand) error {
	if a.guard == nil {
		return nil
	}
	if err := a.guard.Check(RiskView{a}, cmd); err != nil {
		return err
	}
	a.guard.OnAccepted(cmd)
	return nil
}

// ---- 本地风控扩展点 ----

// CommandGuard 是报撤单的本地检查扩展点，在 Runtime 临界区内同步调用：
// 不得发网络请求、等待 channel、调用公开 getter 或重入 Submit/Cancel/WaitUpdate。
type CommandGuard interface {
	Check(view RiskView, cmd TradeCommand) error // 只读判定，不扣额度
	OnAccepted(cmd TradeCommand)                 // 全部规则通过后的记账
	OnCommit(view RiskView, update RiskUpdate)   // 每次账户提交安装后调用
}

// TradeCommandKind 是命令种类。
type TradeCommandKind uint8

const (
	CommandSubmit TradeCommandKind = iota + 1
	CommandCancel
)

// TradeCommand 是交给 CommandGuard 的命令描述。
type TradeCommand struct {
	ID         uint64 // 本地命令身份
	Kind       TradeCommandKind
	AccountID  string
	TradingDay string // 当前已确认的账户交易日；未知为空
	OrderID    string
	Symbol     string
	Exchange   string // Cancel 时由订单或本地报单登记补齐；无法确认为空
	Order      OrderRequest
	At         time.Time // 本次接受尝试的时钟样本（含单调时钟），检查与记账共用
}

// RiskView 是 Guard 调用期间有效的只读视图；不得保存到调用之外。
type RiskView struct{ a *AccountHandle }

// State 返回账户状态。
func (v RiskView) State() DataState { return v.a.state }

// TradingDay 返回已确认的交易日。
func (v RiskView) TradingDay() string { return v.a.scope }

// Account 返回资金记录副本。
func (v RiskView) Account(currency string) (model.Account, bool) {
	n := v.a.accounts[currency]
	if !current(v.a, n) {
		return model.Account{}, false
	}
	return n.value, true
}

// Position 返回持仓副本。
func (v RiskView) Position(symbol string) (model.Position, bool) {
	n := v.a.positions[symbol]
	if !current(v.a, n) {
		return model.Position{}, false
	}
	return n.value, true
}

// Order 返回委托单副本。
func (v RiskView) Order(id string) (model.Order, bool) {
	n := v.a.orders[id]
	if !current(v.a, n) {
		return model.Order{}, false
	}
	return n.value, true
}

// Trade 返回成交副本。
func (v RiskView) Trade(id string) (model.Trade, bool) {
	t, ok := v.a.trades[id]
	return t, ok
}

// Orders 复制全部委托单；仅在规则确需基线时调用。
func (v RiskView) Orders() []model.Order {
	var out []model.Order
	for _, n := range v.a.orders {
		if current(v.a, n) {
			out = append(out, n.value)
		}
	}
	return out
}

// Trades 复制全部成交；仅在规则确需基线时调用。
func (v RiskView) Trades() []model.Trade {
	out := make([]model.Trade, 0, len(v.a.trades))
	for _, t := range v.a.trades {
		out = append(out, t)
	}
	return out
}

// RiskUpdate 描述一次账户提交；ID 列表仅在调用期间有效。
type RiskUpdate struct {
	Stamp      Stamp
	State      DataState
	TradingDay string
	Baseline   bool     // 完成一次完整账户基线
	OrderIDs   []string // 本次触及的委托单
	TradeIDs   []string // 本次新完整成交
}

// RiskRejectedError 是本地规则拒绝。
type RiskRejectedError struct{ RuleID, Reason string }

func (e *RiskRejectedError) Error() string { return e.RuleID + ": " + e.Reason }

// SetCommandGuard 挂载本地风控；仅在第一次 Start 前允许，nil 表示不启用。
func (a *AccountHandle) SetCommandGuard(g CommandGuard) error {
	a.t.rt.mu.Lock()
	defer a.t.rt.mu.Unlock()
	if a.started {
		return ErrStarted
	}
	a.guard = g
	return nil
}

// ---- 远端风控设置 ----

// RemoteRiskRuleRequest 是远端风控规则设置；指针为 nil 表示不发送该叶字段（由服务器取默认值）。
type RemoteRiskRuleRequest struct {
	Exchange                string // SSE / SZSE（同 Python）
	Enable                  bool
	SelfTradeCountLimit     *int64
	InsertOrderCountLimit   *int64
	CancelOrderCountLimit   *int64
	CancelOrderPercentLimit *float64
	TradeUnitsLimit         *int64
	TradePositionRatioLimit *float64
}

// RemoteRiskRequest 是远端风控设置的观察票据：成功仅表示观察到目标字段匹配的规则快照，
// 不是网关逐请求 ACK。
type RemoteRiskRequest struct {
	a    *AccountHandle
	c    *queryCall
	req  RemoteRiskRuleRequest
	mark uint64 // 登记时该路由已接收的序号；只评估更晚的回报
}

// RequestRemoteRiskRule 发送 set_risk_management_rule 并登记观察票据；同一交易所最多一个在途。
func (a *AccountHandle) RequestRemoteRiskRule(ctx context.Context, r RemoteRiskRuleRequest) (*RemoteRiskRequest, error) {
	if r.Exchange != "SSE" && r.Exchange != "SZSE" {
		return nil, fmt.Errorf("%w: remote risk rule on %q (only SSE/SZSE)", ErrUnsupported, r.Exchange)
	}
	for _, f := range []*float64{r.CancelOrderPercentLimit, r.TradePositionRatioLimit} {
		if f != nil && (math.IsNaN(*f) || math.IsInf(*f, 0)) {
			return nil, invalid("non-finite remote risk value")
		}
	}
	r = cloneRiskRequest(r)
	type group map[string]any
	st, fc, tp := group{}, group{}, group{}
	if r.SelfTradeCountLimit != nil {
		st["count_limit"] = *r.SelfTradeCountLimit
	}
	if r.InsertOrderCountLimit != nil {
		fc["insert_order_count_limit"] = *r.InsertOrderCountLimit
	}
	if r.CancelOrderCountLimit != nil {
		fc["cancel_order_count_limit"] = *r.CancelOrderCountLimit
	}
	if r.CancelOrderPercentLimit != nil {
		fc["cancel_order_percent_limit"] = *r.CancelOrderPercentLimit
	}
	if r.TradeUnitsLimit != nil {
		tp["trade_units_limit"] = *r.TradeUnitsLimit
	}
	if r.TradePositionRatioLimit != nil {
		tp["trade_position_ratio_limit"] = *r.TradePositionRatioLimit
	}
	data := mustJSON(map[string]any{"aid": "set_risk_management_rule", "user_id": a.cfg.UserName,
		"exchange_id": r.Exchange, "enable": r.Enable, "self_trade": st, "frequent_cancellation": fc, "trade_position_ratio": tp})
	a.t.rt.mu.Lock()
	defer a.t.rt.mu.Unlock()
	if err := a.commandRoute(); err != nil {
		return nil, err
	}
	if _, ok := a.riskReqs[r.Exchange]; ok {
		return nil, ErrRequestInProgress
	}
	q := &RemoteRiskRequest{a: a, c: newCall("risk"), req: r, mark: a.route.recvSeq.Load()}
	a.route.send(data, nil)
	a.riskReqs[r.Exchange] = q
	return q, nil
}

func cloneRiskRequest(r RemoteRiskRuleRequest) RemoteRiskRuleRequest {
	ci := func(p *int64) *int64 {
		if p == nil {
			return nil
		}
		v := *p
		return &v
	}
	cf := func(p *float64) *float64 {
		if p == nil {
			return nil
		}
		v := *p
		return &v
	}
	r.SelfTradeCountLimit, r.InsertOrderCountLimit, r.CancelOrderCountLimit = ci(r.SelfTradeCountLimit), ci(r.InsertOrderCountLimit), ci(r.CancelOrderCountLimit)
	r.TradeUnitsLimit = ci(r.TradeUnitsLimit)
	r.CancelOrderPercentLimit, r.TradePositionRatioLimit = cf(r.CancelOrderPercentLimit), cf(r.TradePositionRatioLimit)
	return r
}

// observeRiskRequests 在规则回报安装后检查在途票据；调用方持有写锁。
func (a *AccountHandle) observeRiskRequests(j *tdJournal, st Stamp) []string {
	var done []string
	for n := range j.riskRules {
		q := a.riskReqs[n.key]
		if q == nil || st.Sequence <= q.mark || !current(a, n) || !q.matches(&n.value) {
			continue
		}
		q.c.finish(n.value, nil)
		delete(a.riskReqs, n.key)
		done = append(done, q.c.id)
	}
	return done
}

func (q *RemoteRiskRequest) matches(v *model.RemoteRiskRule) bool {
	has := v.Valid.Has
	i64 := func(want *int64, f model.RemoteRiskRuleField, got int64) bool {
		return want == nil || (has(f) && got == *want)
	}
	f64 := func(want *float64, f model.RemoteRiskRuleField, got float64) bool {
		return want == nil || (has(f) && got == *want)
	}
	r := q.req
	return has(model.RemoteRiskRuleEnable) && v.Enable == r.Enable &&
		i64(r.SelfTradeCountLimit, model.RemoteRiskRuleSelfTradeCountLimit, v.SelfTrade.CountLimit) &&
		i64(r.InsertOrderCountLimit, model.RemoteRiskRuleFrequentCancellationInsertOrderCountLimit, v.FrequentCancellation.InsertOrderCountLimit) &&
		i64(r.CancelOrderCountLimit, model.RemoteRiskRuleFrequentCancellationCancelOrderCountLimit, v.FrequentCancellation.CancelOrderCountLimit) &&
		f64(r.CancelOrderPercentLimit, model.RemoteRiskRuleFrequentCancellationCancelOrderPercentLimit, v.FrequentCancellation.CancelOrderPercentLimit) &&
		i64(r.TradeUnitsLimit, model.RemoteRiskRuleTradePositionRatioTradeUnitsLimit, v.TradePositionRatio.TradeUnitsLimit) &&
		f64(r.TradePositionRatioLimit, model.RemoteRiskRuleTradePositionRatioTradePositionRatioLimit, v.TradePositionRatio.TradePositionRatioLimit)
}

// ID 返回本地观察身份（不编码到线上）。
func (q *RemoteRiskRequest) ID() string { return q.c.id }

// Done 报告票据是否已终结。
func (q *RemoteRiskRequest) Done() bool {
	select {
	case <-q.c.done:
		return true
	default:
		return false
	}
}

// Result 返回观察到的规则；未终结时返回 ErrNotReady。
func (q *RemoteRiskRequest) Result() (model.RemoteRiskRule, error) {
	select {
	case <-q.c.done:
	default:
		return model.RemoteRiskRule{}, ErrNotReady
	}
	if q.c.err != nil {
		return model.RemoteRiskRule{}, q.c.err
	}
	return q.c.value.(model.RemoteRiskRule), nil
}

// Wait 等待票据终结；仅适用于后台 Run 推进的 Runtime。
func (q *RemoteRiskRequest) Wait(ctx context.Context) (model.RemoteRiskRule, error) {
	select {
	case <-q.c.done:
		return q.Result()
	case <-ctx.Done():
		q.Close()
		return model.RemoteRiskRule{}, ctx.Err()
	}
}

// Close 取消本地观察，不撤销已发送的设置。
func (q *RemoteRiskRequest) Close() error {
	q.a.t.rt.mu.Lock()
	defer q.a.t.rt.mu.Unlock()
	q.c.finish(nil, ErrClosed)
	if q.a.riskReqs[q.req.Exchange] == q {
		delete(q.a.riskReqs, q.req.Exchange)
	}
	return nil
}
