// Package trade 是独立交易客户端：后台自动推进，账户事件集中在 Session 上，
// 可用 Events、RunEvents 或 CTP 风格的 Serve 消费。
package trade

import (
	"context"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

type (
	Config                = core.TradingConfig
	AccountConfig         = core.AccountConfig
	LoginClientInfo       = core.LoginClientInfo
	Event                 = core.TradeEvent
	EventKind             = core.TradeEventKind
	EventSub              = core.TradeEventSub
	SessionSnapshot       = core.AccountSnapshot
	OrderRequest          = core.OrderRequest
	OrderRef              = core.OrderRef
	CommandGuard          = core.CommandGuard
	RemoteRiskRuleRequest = core.RemoteRiskRuleRequest
	RemoteRiskRequest     = core.RemoteRiskRequest
)

const (
	EventSnapshot       = core.TradeEventSnapshot
	EventState          = core.TradeEventState
	EventAccount        = core.TradeEventAccount
	EventPosition       = core.TradeEventPosition
	EventOrder          = core.TradeEventOrder
	EventTrade          = core.TradeEventTrade
	EventNotify         = core.TradeEventNotify
	EventRemoteRiskRule = core.TradeEventRemoteRiskRule
	EventRemoteRiskData = core.TradeEventRemoteRiskData
)

// Client 拥有自己的 Runtime 并在后台 Run。
type Client struct {
	rt      *core.Runtime
	trading *core.Trading
	cancel  context.CancelFunc
	done    chan struct{}
}

// Dial 创建交易客户端；只创建账户管理组件，账户 Start 时才接入。
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	rt := core.NewRuntime()
	td, err := core.NewTrading(ctx, rt, cfg)
	if err != nil {
		rt.Close()
		return nil, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	c := &Client{rt: rt, trading: td, cancel: cancel, done: make(chan struct{})}
	go func() {
		rt.Run(runCtx)
		close(c.done)
	}()
	return c, nil
}

// Close 关闭全部账户并停止推进。
func (c *Client) Close() error {
	c.cancel()
	err := c.rt.Close()
	<-c.done
	return err
}

// Core 返回底层交易组件。
func (c *Client) Core() *core.Trading { return c.trading }

// PrepareAccount 登记账户；可在 Start 前注册 Events 与 CommandGuard。
func (c *Client) PrepareAccount(cfg AccountConfig) (*Session, error) {
	a, err := c.trading.PrepareAccount(cfg)
	if err != nil {
		return nil, err
	}
	return &Session{a}, nil
}

// OpenAccount 登记并启动账户。
func (c *Client) OpenAccount(ctx context.Context, cfg AccountConfig) (*Session, error) {
	a, err := c.trading.OpenAccount(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Session{a}, nil
}

// Session 是一个交易账户的操作入口；方法委托给 core.AccountHandle。
type Session struct {
	account *core.AccountHandle
}

// Handle 返回底层账户句柄。
func (s *Session) Handle() *core.AccountHandle { return s.account }

// HasFeature 查询当前授权的功能权限；Start 尚未完成授权或已失败时返回 false。
func (s *Session) HasFeature(feature string) bool { return s.account.HasFeature(feature) }

func (s *Session) Start(ctx context.Context) error { return s.account.Start(ctx) }
func (s *Session) Close() error                    { return s.account.Close() }
func (s *Session) State() core.DataState           { return s.account.State() }
func (s *Session) AccountID() string               { return s.account.ID() }
func (s *Session) Snapshot() SessionSnapshot       { return s.account.Snapshot() }
func (s *Session) Account() (model.Account, bool)  { return s.account.Account() }
func (s *Session) AccountByCurrency(c string) (model.Account, bool) {
	return s.account.AccountByCurrency(c)
}
func (s *Session) Accounts() map[string]model.Account            { return s.account.Accounts() }
func (s *Session) Position(symbol string) (model.Position, bool) { return s.account.Position(symbol) }
func (s *Session) Positions() map[string]model.Position          { return s.account.Positions() }
func (s *Session) Order(id string) (model.Order, bool)           { return s.account.Order(id) }
func (s *Session) Orders() map[string]model.Order                { return s.account.Orders() }
func (s *Session) Trade(id string) (model.Trade, bool)           { return s.account.Trade(id) }
func (s *Session) Trades() map[string]model.Trade                { return s.account.Trades() }
func (s *Session) TradesByOrder(id string) []model.Trade         { return s.account.TradesByOrder(id) }
func (s *Session) SetCommandGuard(g CommandGuard) error          { return s.account.SetCommandGuard(g) }
func (s *Session) Events(ctx context.Context) (*EventSub, error) { return s.account.Events(ctx) }
func (s *Session) Cancel(ctx context.Context, orderID string) error {
	return s.account.Cancel(ctx, orderID)
}
func (s *Session) ConfirmSettlement(ctx context.Context) error {
	return s.account.ConfirmSettlement(ctx)
}

// Submit 报单；成功只表示本地已接受并排队。
func (s *Session) Submit(ctx context.Context, r OrderRequest) (*OrderRef, error) {
	return s.account.Submit(ctx, r)
}

// RemoteRiskRule 返回远端风控规则。
func (s *Session) RemoteRiskRule(exchange string) (model.RemoteRiskRule, bool) {
	return s.account.RemoteRiskRule(exchange)
}

// RemoteRiskData 返回远端风控统计。
func (s *Session) RemoteRiskData(symbol string) (model.RemoteRiskData, bool) {
	return s.account.RemoteRiskData(symbol)
}

// RequestRemoteRiskRule 发送远端风控设置并返回观察票据。
func (s *Session) RequestRemoteRiskRule(ctx context.Context, r RemoteRiskRuleRequest) (*RemoteRiskRequest, error) {
	return s.account.RequestRemoteRiskRule(ctx, r)
}

// SetRemoteRiskRule 发送设置并阻塞到观察到目标配置；不是网关逐请求 ACK。
func (s *Session) SetRemoteRiskRule(ctx context.Context, r RemoteRiskRuleRequest) (model.RemoteRiskRule, error) {
	q, err := s.account.RequestRemoteRiskRule(ctx, r)
	if err != nil {
		return model.RemoteRiskRule{}, err
	}
	return q.Wait(ctx)
}

// RunEvents 在调用者 goroutine 串行消费事件，直到订阅结束。
func (s *Session) RunEvents(ctx context.Context, fn func(Event)) error {
	sub, err := s.Events(ctx)
	if err != nil {
		return err
	}
	defer sub.Close()
	for {
		select {
		case e, ok := <-sub.C():
			if !ok {
				return sub.Err()
			}
			fn(e)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Serve 以 CTP 风格回调消费事件；回调在调用者 goroutine 串行执行。
func (s *Session) Serve(ctx context.Context, h Handler) error { return s.RunEvents(ctx, h.Handle) }

// Handler 是按事件种类分派的回调集合；nil 回调忽略对应事件。
type Handler struct {
	OnSnapshot       func(SessionSnapshot)
	OnState          func(core.TradeStateEvent)
	OnAccount        func(core.AccountEvent)
	OnPosition       func(core.PositionEvent)
	OnOrder          func(core.OrderEvent)
	OnTrade          func(model.Trade)
	OnNotify         func(model.Notify)
	OnRemoteRiskRule func(core.RemoteRiskRuleEvent)
	OnRemoteRiskData func(core.RemoteRiskDataEvent)
}

// Handle 把一个事件分派给对应回调。
func (h Handler) Handle(e Event) {
	switch e.Kind {
	case EventSnapshot:
		call(h.OnSnapshot, e.Snapshot)
	case EventState:
		call(h.OnState, e.State)
	case EventAccount:
		call(h.OnAccount, e.Account)
	case EventPosition:
		call(h.OnPosition, e.Position)
	case EventOrder:
		call(h.OnOrder, e.Order)
	case EventTrade:
		call(h.OnTrade, e.Trade)
	case EventNotify:
		call(h.OnNotify, e.Notify)
	case EventRemoteRiskRule:
		call(h.OnRemoteRiskRule, e.RemoteRiskRule)
	case EventRemoteRiskData:
		call(h.OnRemoteRiskData, e.RemoteRiskData)
	}
}

func call[T any](fn func(T), v *T) {
	if fn != nil && v != nil {
		fn(*v)
	}
}
