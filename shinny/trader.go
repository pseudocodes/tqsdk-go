package shinny

import "context"

// Trader 交易接口
//
// 这个接口抽象了交易会话的核心功能，允许实现不同的交易模式：
//   - 实盘交易：使用 TradeSession（真实的期货账户）
//   - 模拟交易：实现 VirtualTrader（虚拟资金和持仓）
//   - 回测交易：实现 BacktestTrader（历史数据回放）
//
// 使用示例：
//
//	// 实盘交易
//	var trader Trader
//	trader, _ = client.Trade(ctx, "快期模拟", "user", "pass")
//
//	// 模拟交易
//	trader = NewVirtualTrader(initialBalance, client)
//
//	// 回测交易
//	trader = NewBacktestTrader(startTime, endTime, initialBalance)
type Trader interface {
	// ==================== 交易操作 ====================

	// InsertOrder 下单
	// 返回订单对象，订单状态会通过回调或 Channel 异步更新
	InsertOrder(ctx context.Context, req *InsertOrderRequest) (*Order, error)

	// CancelOrder 撤销委托单
	CancelOrder(ctx context.Context, orderID string) error

	// ==================== 同步查询 ====================

	// GetAccount 获取账户信息
	// 返回当前的资金账户信息（余额、可用资金、冻结资金等）
	GetAccount(ctx context.Context) (*Account, error)

	// GetPosition 获取指定合约的持仓
	// symbol: 合约代码，例如 "SHFE.cu2401"
	GetPosition(ctx context.Context, symbol string) (*Position, error)

	// GetPositions 获取所有持仓
	// 返回一个 map，key 为合约代码，value 为持仓信息
	GetPositions(ctx context.Context) (map[string]*Position, error)

	// GetOrders 获取所有委托单
	// 返回一个 map，key 为订单ID，value 为订单信息
	GetOrders(ctx context.Context) (map[string]*Order, error)

	// GetTrades 获取所有成交记录
	// 返回一个 map，key 为成交ID，value 为成交信息
	GetTrades(ctx context.Context) (map[string]*Trade, error)

	// ==================== 流式接口（Channel-based）====================

	// AccountChannel 获取账户更新 Channel
	// 每次账户信息变化时（如成交、出入金），会推送到这个 Channel
	AccountChannel() <-chan *Account

	// PositionChannel 获取单个持仓更新 Channel
	// 每次单个持仓变化时，会推送到这个 Channel
	PositionChannel() <-chan *PositionUpdate

	// PositionsChannel 获取全量持仓更新 Channel
	// 每次持仓有任何变化时，会推送所有持仓的快照
	PositionsChannel() <-chan map[string]*Position

	// OrderChannel 获取订单更新 Channel
	// 每次订单状态变化时（如报单、成交、撤单），会推送到这个 Channel
	OrderChannel() <-chan *Order

	// TradeChannel 获取成交更新 Channel
	// 每次成交时，会推送到这个 Channel
	TradeChannel() <-chan *Trade

	// NotificationChannel 获取通知 Channel
	// 系统通知、错误信息等会推送到这个 Channel
	NotificationChannel() <-chan *Notification

	// ==================== 回调接口（Callback-based）====================

	// OnAccount 注册账户更新回调
	// 每次账户信息变化时，会调用此回调函数
	OnAccount(handler func(*Account))

	// OnPosition 注册单个持仓更新回调
	// 每次单个持仓变化时，会调用此回调函数
	// 参数：symbol（合约代码）, position（持仓信息）
	OnPosition(handler func(string, *Position))

	// OnPositions 注册全量持仓更新回调
	// 每次持仓有任何变化时，会调用此回调函数，传递所有持仓
	OnPositions(handler func(map[string]*Position))

	// OnOrder 注册订单更新回调
	// 每次订单状态变化时，会调用此回调函数
	OnOrder(handler func(*Order))

	// OnTrade 注册成交回调
	// 每次成交时，会调用此回调函数
	OnTrade(handler func(*Trade))

	// OnNotification 注册通知回调
	// 系统通知、错误信息等会调用此回调函数
	OnNotification(handler func(*Notification))

	// OnError 注册错误回调
	// 发生错误时，会调用此回调函数
	OnError(handler func(error))

	// ==================== 生命周期管理 ====================

	// Connect 连接并初始化交易者
	//
	// 语义说明：
	//   - 实盘交易：连接交易服务器并登录
	//   - 模拟交易：初始化虚拟交易环境
	//   - 回测交易：初始化回测引擎，加载历史数据
	//
	// 注意：
	//   - 此方法应该是幂等的，重复调用不会产生副作用
	//   - 如果已连接，可以直接返回 nil
	//   - 支持重连场景（断线后重新调用）
	//
	// 示例：
	//
	//	trader := NewVirtualTrader(ctx, 1000000.0, 0.0001)
	//	if err := trader.Connect(ctx); err != nil {
	//	    return err
	//	}
	Connect(ctx context.Context) error

	// IsReady 检查交易者是否已就绪
	//
	// 返回 true 表示可以开始交易操作（下单、查询等）
	//
	// 语义说明：
	//   - 实盘交易：已成功登录交易服务器
	//   - 模拟交易：虚拟环境已初始化
	//   - 回测交易：回测引擎已就绪
	IsReady() bool

	// IsLoggedIn 检查是否已登录（IsReady 的别名，保持向后兼容）
	//
	// 已废弃：建议使用 IsReady() 代替
	IsLoggedIn() bool

	// Close 关闭交易会话
	//
	// 释放资源、关闭连接、停止后台任务等
	// 调用后，需要重新调用 Connect() 才能继续使用
	Close() error
}

// 编译时检查 TradeSession 是否实现了 Trader 接口
var _ Trader = (*TradeSession)(nil)
