package core

import (
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// TradeEventKind 标识 TradeEvent 中有效的 payload。
type TradeEventKind uint8

const (
	TradeEventSnapshot TradeEventKind = iota + 1
	TradeEventState
	TradeEventAccount
	TradeEventPosition
	TradeEventOrder
	TradeEventTrade
	TradeEventNotify
	TradeEventRemoteRiskRule
	TradeEventRemoteRiskData
)

func (k TradeEventKind) String() string {
	switch k {
	case TradeEventSnapshot:
		return "Snapshot"
	case TradeEventState:
		return "State"
	case TradeEventAccount:
		return "Account"
	case TradeEventPosition:
		return "Position"
	case TradeEventOrder:
		return "Order"
	case TradeEventTrade:
		return "Trade"
	case TradeEventNotify:
		return "Notify"
	case TradeEventRemoteRiskRule:
		return "RemoteRiskRule"
	case TradeEventRemoteRiskData:
		return "RemoteRiskData"
	}
	return "TradeEventKind(?)"
}

// TradeEvent 是交易账户的一次交付；Kind 对应恰好一个非 nil payload，均为独立副本。
type TradeEvent struct {
	Kind      TradeEventKind
	AccountID string // 本地账户身份 AccountConfig.ID
	Scope     string // 业务作用域（交易日）；未知为空
	Stamp     Stamp

	Snapshot       *AccountSnapshot
	State          *TradeStateEvent
	Account        *AccountEvent
	Position       *PositionEvent
	Order          *OrderEvent
	Trade          *model.Trade
	Notify         *model.Notify
	RemoteRiskRule *RemoteRiskRuleEvent
	RemoteRiskData *RemoteRiskDataEvent
}

type (
	AccountEvent struct {
		Key     string // 币种
		Value   model.Account
		Deleted bool
	}
	PositionEvent struct {
		Key     string // 合约代码 exchange_id.instrument_id
		Value   model.Position
		Deleted bool
	}
	OrderEvent struct {
		Key     string // order_id
		Value   model.Order
		Deleted bool
	}
	RemoteRiskRuleEvent struct {
		Exchange string
		Value    model.RemoteRiskRule
		Deleted  bool
	}
	RemoteRiskDataEvent struct {
		Symbol  string
		Value   model.RemoteRiskData
		Deleted bool
	}
)

// TradeStateEvent 是账户状态或会话元信息的变化。
type TradeStateEvent struct {
	State   DataState
	Session model.TradingSessionInfo
	Scope   string
	Err     error
}

// AccountSnapshot 是一个账户的完整快照（当前代次已确认内容）。
type AccountSnapshot struct {
	State              DataState
	Stamp              Stamp
	Session            model.TradingSessionInfo
	PositionReadyKnown bool
	PositionReady      bool
	Scope              string
	Accounts           map[string]model.Account
	Positions          map[string]model.Position
	Orders             map[string]model.Order
	Trades             map[string]model.Trade
	RemoteRiskRules    map[string]model.RemoteRiskRule
	RemoteRiskData     map[string]model.RemoteRiskData
}

// TradeEventSub 是账户事件订阅。
type TradeEventSub struct {
	a  *AccountHandle
	mb *mailbox[TradeEvent]
}

// C 返回事件 channel；订阅终止后关闭。
func (s *TradeEventSub) C() <-chan TradeEvent { return s.mb.ch }

// Err 返回终止原因；正常 Close 为 nil。
func (s *TradeEventSub) Err() error { return s.mb.error() }

// Close 结束订阅。
func (s *TradeEventSub) Close() error {
	s.a.t.rt.mu.Lock()
	delete(s.a.subs, s)
	s.a.t.rt.mu.Unlock()
	s.mb.close(nil)
	return nil
}

// tradingChanges 是交易组件一次提交的变化记录。
type tradingChanges struct {
	accounts  map[uint64]AccountChange
	positions map[uint64]PositionChange
	orders    map[uint64]OrderChange
	riskRules map[uint64]RemoteRiskRuleChange
	riskData  map[uint64]RemoteRiskDataChange
	trades    map[uint64]*TradesChange // 按 AccountHandle 身份
	states    map[uint64]bool          // 账户状态或会话元信息改变
	notifies  map[uint64][]model.Notify
	riskReqs  []string
	errors    []RouteError
}

func (c *tradingChanges) empty() bool {
	return len(c.accounts) == 0 && len(c.positions) == 0 && len(c.orders) == 0 && len(c.riskRules) == 0 &&
		len(c.riskData) == 0 && len(c.trades) == 0 && len(c.states) == 0 && len(c.notifies) == 0 &&
		len(c.riskReqs) == 0 && len(c.errors) == 0
}

// TradingChanges 是某次提交中交易组件的变化；其他组件的提交返回空值。
type TradingChanges struct{ p *tradingChanges }

// Account 返回资金记录的净变化。
func (c TradingChanges) Account(r *AccountRef) AccountChange {
	if c.p == nil || r == nil {
		return AccountChange{}
	}
	return c.p.accounts[r.node.id]
}

// Position 返回持仓的净变化。
func (c TradingChanges) Position(r *PositionRef) PositionChange {
	if c.p == nil || r == nil {
		return PositionChange{}
	}
	return c.p.positions[r.node.id]
}

// Order 返回委托单的净变化。
func (c TradingChanges) Order(r *OrderRef) OrderChange {
	if c.p == nil || r == nil {
		return OrderChange{}
	}
	return c.p.orders[r.node.id]
}

// Trades 返回成交集合的变化。
func (c TradingChanges) Trades(r *TradesRef) TradesChange {
	if c.p == nil || r == nil {
		return TradesChange{}
	}
	if t := c.p.trades[r.a.id]; t != nil {
		return TradesChange{Reset: t.Reset, Added: append([]model.Trade(nil), t.Added...)}
	}
	return TradesChange{}
}

// RemoteRiskRule 返回账户某交易所远端风控规则的变化。
func (c TradingChanges) RemoteRiskRule(a *AccountHandle, exchange string) RemoteRiskRuleChange {
	if c.p == nil || a == nil {
		return RemoteRiskRuleChange{}
	}
	a.t.rt.mu.RLock()
	n := a.riskRules[exchange]
	a.t.rt.mu.RUnlock()
	if n == nil {
		return RemoteRiskRuleChange{}
	}
	return c.p.riskRules[n.id]
}

// RemoteRiskData 返回账户某合约远端风控统计的变化。
func (c TradingChanges) RemoteRiskData(a *AccountHandle, symbol string) RemoteRiskDataChange {
	if c.p == nil || a == nil {
		return RemoteRiskDataChange{}
	}
	a.t.rt.mu.RLock()
	n := a.riskData[symbol]
	a.t.rt.mu.RUnlock()
	if n == nil {
		return RemoteRiskDataChange{}
	}
	return c.p.riskData[n.id]
}

// StateChanged 报告账户状态或会话元信息是否改变。
func (c TradingChanges) StateChanged(a *AccountHandle) bool {
	return c.p != nil && a != nil && c.p.states[a.id]
}

// Notifies 返回本提交收到的账户通知。
func (c TradingChanges) Notifies(a *AccountHandle) []model.Notify {
	if c.p == nil || a == nil {
		return nil
	}
	return append([]model.Notify(nil), c.p.notifies[a.id]...)
}

// RemoteRiskRequestIDs 返回本提交完成的远端风控设置观察票据。
func (c TradingChanges) RemoteRiskRequestIDs() []string {
	if c.p == nil {
		return nil
	}
	return append([]string(nil), c.p.riskReqs...)
}

// Errors 返回本提交的路由错误。
func (c TradingChanges) Errors() []RouteError {
	if c.p == nil {
		return nil
	}
	return append([]RouteError(nil), c.p.errors...)
}
