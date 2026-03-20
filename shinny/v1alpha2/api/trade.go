package api

import "context"

type TradeService interface {
	Login(ctx context.Context, req TradeLoginReq) (TradeSession, error)
	Session(sessionID string) (TradeSession, bool)

	GetTradingStatus(ctx context.Context, symbol string) (TradingStatus, error)
	TradingStatus(symbol string) (TradingStatus, bool)
	SubscribeTradingStatuses(ctx context.Context, symbols ...string) (TradingStatusSub, error)
}

type TradeLoginReq struct {
	AccountID string
	BrokerID  string
	UserName  string
	Password  string
	// FrontURL: nil -> resolve by AuthService.ResolveTDURL; non-nil -> direct use.
	FrontURL *string
}

type TradeSession interface {
	SessionID() string
	AccountID() string
	State() TradeSessionState
	Ready() bool
	Events() <-chan TradeSessionEvent

	InsertOrder(ctx context.Context, symbol string, direction Direction, volume int, opts ...InsertOrderOption) (OrderRef, error)
	CancelOrder(ctx context.Context, orderID string) error

	Account() (Account, bool)
	Position(symbol string) (Position, bool)
	Positions() map[string]Position
	Order(orderID string) (Order, bool)
	Orders() map[string]Order
	Trade(tradeID string) (Trade, bool)
	Trades() map[string]Trade

	SubscribeAccounts(ctx context.Context, opts ...TradeSubOption) (AccountSub, error)
	SubscribePositions(ctx context.Context, opts ...TradeSubOption) (PositionSub, error)
	SubscribeOrders(ctx context.Context, opts ...TradeSubOption) (OrderSub, error)
	SubscribeTrades(ctx context.Context, opts ...TradeSubOption) (TradeSub, error)
	SubscribeNotifies(ctx context.Context, opts ...TradeSubOption) (NotifySub, error)
	NotifyEvents() <-chan NotifyEvent

	GetRiskManagementRule(exchangeID string) (RiskManagementRule, bool)
	SetRiskManagementRule(ctx context.Context, exchangeID string, enable bool, opts ...RiskRuleOption) (RiskManagementRule, error)
	GetRiskManagementData(symbol string) (RiskManagementData, bool)
	RiskManagementDataAll() map[string]RiskManagementData
	AddRiskRule(rule RiskRule) error
	RemoveRiskRule(ruleID string) error

	ConnEvents(ctx context.Context) (<-chan ConnEvent, error)
	Close(ctx context.Context) error
}

type OrderRef interface {
	AccountID() string
	OrderID() string
	Snapshot() (Order, bool)
	Events() <-chan OrderEvent
	WaitDone(ctx context.Context) (Order, error)
}

type TradingStatusSub interface {
	C() <-chan TradingStatusEvent
	Close() error
}

type AccountSub interface {
	C() <-chan AccountEvent
	Close() error
}

type PositionSub interface {
	C() <-chan PositionEvent
	Close() error
}

type OrderSub interface {
	C() <-chan OrderEvent
	Close() error
}

type TradeSub interface {
	C() <-chan TradeFillEvent
	Close() error
}

type NotifySub interface {
	C() <-chan NotifyEvent
	Close() error
}

type RiskRule interface {
	ID() string
	CouldInsertOrder(req InsertOrderCheckReq) *RiskReject
	OnInsertOrder(req InsertOrderApplyReq)
	CouldCancelOrder(req CancelOrderCheckReq) *RiskReject
	OnCancelOrder(req CancelOrderApplyReq)
	OnSettle()
}

type RiskReject struct {
	RuleID  string
	Reason  string
	Details map[string]any
}

type InsertOrderCheckReq struct {
	AccountID  string
	ExchangeID string
	Symbol     string
	Direction  Direction
	Offset     Offset
	Volume     int
}

type InsertOrderApplyReq = InsertOrderCheckReq

type CancelOrderCheckReq struct {
	AccountID  string
	OrderID    string
	ExchangeID string
}

type CancelOrderApplyReq = CancelOrderCheckReq

type TradeDiffView struct {
	SessionID  string
	AccountID  string
	TradingDay string
	Diff       map[string]any
}

type RiskDiffObserver interface {
	OnRecvDiff(view TradeDiffView)
}

type RiskManager interface {
	Add(rule RiskRule) error
	Remove(ruleID string) error

	BeforeInsert(req InsertOrderCheckReq) error
	AfterInsert(req InsertOrderApplyReq)
	BeforeCancel(req CancelOrderCheckReq) error
	AfterCancel(req CancelOrderApplyReq)

	OnRecvDiff(view TradeDiffView)
}
