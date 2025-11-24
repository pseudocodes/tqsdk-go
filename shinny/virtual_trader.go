package shinny

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// VirtualTrader 虚拟交易者（用于模拟交易和回测）
//
// 特点：
//   - 不连接真实交易服务器
//   - 模拟资金、持仓、订单、成交
//   - 可以对接行情数据进行撮合
//   - 适用于策略回测和模拟交易
type VirtualTrader struct {
	// 配置
	initialBalance float64
	commission     float64 // 手续费率

	// 状态
	account   *Account
	positions map[string]*Position
	orders    map[string]*Order
	trades    map[string]*Trade

	// 流式 Channels
	accountCh      chan *Account
	positionCh     chan *PositionUpdate
	positionsCh    chan map[string]*Position
	orderCh        chan *Order
	tradeCh        chan *Trade
	notificationCh chan *Notification

	// 回调函数
	onAccount      func(*Account)
	onPosition     func(string, *Position)
	onPositions    func(map[string]*Position)
	onOrder        func(*Order)
	onTrade        func(*Trade)
	onNotification func(*Notification)
	onError        func(error)

	// 状态管理
	connected bool // 是否已连接/初始化
	running   bool
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.RWMutex
}

// NewVirtualTrader 创建虚拟交易者
//
// 参数：
//   - ctx: 上下文
//   - initialBalance: 初始资金
//   - commission: 手续费率（例如 0.0001 表示万分之一）
//
// 示例：
//
//	trader := NewVirtualTrader(ctx, 1000000.0, 0.0001)
//	trader.OnOrder(func(order *Order) {
//	    fmt.Printf("订单更新: %+v\n", order)
//	})
func NewVirtualTrader(ctx context.Context, initialBalance float64, commission float64) *VirtualTrader {
	subCtx, cancel := context.WithCancel(ctx)

	trader := &VirtualTrader{
		initialBalance: initialBalance,
		commission:     commission,
		account: &Account{
			Balance:      initialBalance,
			Available:    initialBalance,
			PreBalance:   initialBalance,
			Margin:       0,
			FrozenMargin: 0,
		},
		positions:      make(map[string]*Position),
		orders:         make(map[string]*Order),
		trades:         make(map[string]*Trade),
		accountCh:      make(chan *Account, 10),
		positionCh:     make(chan *PositionUpdate, 10),
		positionsCh:    make(chan map[string]*Position, 10),
		orderCh:        make(chan *Order, 100),
		tradeCh:        make(chan *Trade, 100),
		notificationCh: make(chan *Notification, 10),
		connected:      false,
		running:        true,
		ctx:            subCtx,
		cancel:         cancel,
	}

	// 自动连接（保持向后兼容）
	trader.Connect(ctx)

	return trader
}

// ==================== 生命周期管理 ====================

// Connect 初始化虚拟交易环境
//
// 此方法是幂等的，可以重复调用
func (vt *VirtualTrader) Connect(ctx context.Context) error {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	// 如果已连接，直接返回
	if vt.connected {
		return nil
	}

	// 初始化虚拟交易环境（这里可以添加更多初始化逻辑）
	// 例如：加载配置、初始化撮合引擎等
	vt.connected = true

	return nil
}

// ==================== 交易操作 ====================

// InsertOrder 下单（模拟）
func (vt *VirtualTrader) InsertOrder(ctx context.Context, req *InsertOrderRequest) (*Order, error) {
	if !vt.IsReady() {
		return nil, fmt.Errorf("virtual trader not ready, please call Connect() first")
	}

	vt.mu.Lock()
	defer vt.mu.Unlock()

	// 生成订单ID
	orderID := fmt.Sprintf("VIRTUAL_%s_%d", RandomStr(6), time.Now().UnixNano())

	// 创建订单
	order := &Order{
		OrderID:        orderID,
		ExchangeID:     req.Symbol[:strings.Index(req.Symbol, ".")],
		InstrumentID:   req.Symbol[strings.Index(req.Symbol, ".")+1:],
		Direction:      req.Direction,
		Offset:         req.Offset,
		VolumeOrign:    req.Volume,
		VolumeLeft:     req.Volume,
		PriceType:      req.PriceType,
		LimitPrice:     req.LimitPrice,
		Status:         OrderStatusAlive,
		InsertDateTime: time.Now().UnixNano(),
	}

	vt.orders[orderID] = order

	// 异步触发回调
	go vt.emitOrder(order)

	// TODO: 这里可以添加更复杂的撮合逻辑
	// 例如：监听行情数据，当价格满足条件时自动成交

	return order, nil
}

// CancelOrder 撤单（模拟）
func (vt *VirtualTrader) CancelOrder(ctx context.Context, orderID string) error {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	order, exists := vt.orders[orderID]
	if !exists {
		return fmt.Errorf("order not found: %s", orderID)
	}

	if order.Status != OrderStatusAlive {
		return fmt.Errorf("order cannot be cancelled, status: %s", order.Status)
	}

	// 更新订单状态为已完成（撤单也算完成）
	order.Status = OrderStatusFinished
	order.VolumeLeft = 0

	// 异步触发回调
	go vt.emitOrder(order)

	return nil
}

// ==================== 同步查询 ====================

// GetAccount 获取账户信息
func (vt *VirtualTrader) GetAccount(ctx context.Context) (*Account, error) {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	// 返回账户副本
	account := *vt.account
	return &account, nil
}

// GetPosition 获取指定合约的持仓
func (vt *VirtualTrader) GetPosition(ctx context.Context, symbol string) (*Position, error) {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	pos, exists := vt.positions[symbol]
	if !exists {
		return nil, fmt.Errorf("position not found: %s", symbol)
	}

	// 返回持仓副本
	position := *pos
	return &position, nil
}

// GetPositions 获取所有持仓
func (vt *VirtualTrader) GetPositions(ctx context.Context) (map[string]*Position, error) {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	// 返回持仓副本
	positions := make(map[string]*Position)
	for symbol, pos := range vt.positions {
		posCopy := *pos
		positions[symbol] = &posCopy
	}

	return positions, nil
}

// GetOrders 获取所有委托单
func (vt *VirtualTrader) GetOrders(ctx context.Context) (map[string]*Order, error) {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	// 返回订单副本
	orders := make(map[string]*Order)
	for orderID, order := range vt.orders {
		orderCopy := *order
		orders[orderID] = &orderCopy
	}

	return orders, nil
}

// GetTrades 获取所有成交记录
func (vt *VirtualTrader) GetTrades(ctx context.Context) (map[string]*Trade, error) {
	vt.mu.RLock()
	defer vt.mu.RUnlock()

	// 返回成交副本
	trades := make(map[string]*Trade)
	for tradeID, trade := range vt.trades {
		tradeCopy := *trade
		trades[tradeID] = &tradeCopy
	}

	return trades, nil
}

// ==================== 流式接口 ====================

func (vt *VirtualTrader) AccountChannel() <-chan *Account {
	return vt.accountCh
}

func (vt *VirtualTrader) PositionChannel() <-chan *PositionUpdate {
	return vt.positionCh
}

func (vt *VirtualTrader) PositionsChannel() <-chan map[string]*Position {
	return vt.positionsCh
}

func (vt *VirtualTrader) OrderChannel() <-chan *Order {
	return vt.orderCh
}

func (vt *VirtualTrader) TradeChannel() <-chan *Trade {
	return vt.tradeCh
}

func (vt *VirtualTrader) NotificationChannel() <-chan *Notification {
	return vt.notificationCh
}

// ==================== 回调接口 ====================

func (vt *VirtualTrader) OnAccount(handler func(*Account)) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	vt.onAccount = handler
}

func (vt *VirtualTrader) OnPosition(handler func(string, *Position)) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	vt.onPosition = handler
}

func (vt *VirtualTrader) OnPositions(handler func(map[string]*Position)) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	vt.onPositions = handler
}

func (vt *VirtualTrader) OnOrder(handler func(*Order)) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	vt.onOrder = handler
}

func (vt *VirtualTrader) OnTrade(handler func(*Trade)) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	vt.onTrade = handler
}

func (vt *VirtualTrader) OnNotification(handler func(*Notification)) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	vt.onNotification = handler
}

func (vt *VirtualTrader) OnError(handler func(error)) {
	vt.mu.Lock()
	defer vt.mu.Unlock()
	vt.onError = handler
}

// ==================== 状态查询 ====================

// IsReady 检查虚拟交易者是否已就绪
func (vt *VirtualTrader) IsReady() bool {
	vt.mu.RLock()
	defer vt.mu.RUnlock()
	return vt.connected
}

// IsLoggedIn 检查是否已初始化（IsReady 的别名，保持向后兼容）
func (vt *VirtualTrader) IsLoggedIn() bool {
	return vt.IsReady()
}

func (vt *VirtualTrader) Close() error {
	vt.mu.Lock()
	if !vt.running {
		vt.mu.Unlock()
		return nil
	}
	vt.running = false
	vt.mu.Unlock()

	vt.cancel()

	// 关闭所有 Channels
	close(vt.accountCh)
	close(vt.positionCh)
	close(vt.positionsCh)
	close(vt.orderCh)
	close(vt.tradeCh)
	close(vt.notificationCh)

	return nil
}

// ==================== 内部方法 ====================

func (vt *VirtualTrader) emitOrder(order *Order) {
	// 发送到 Channel
	select {
	case vt.orderCh <- order:
	default:
	}

	// 调用回调
	vt.mu.RLock()
	handler := vt.onOrder
	vt.mu.RUnlock()

	if handler != nil {
		handler(order)
	}
}

func (vt *VirtualTrader) emitTrade(trade *Trade) {
	// 发送到 Channel
	select {
	case vt.tradeCh <- trade:
	default:
	}

	// 调用回调
	vt.mu.RLock()
	handler := vt.onTrade
	vt.mu.RUnlock()

	if handler != nil {
		handler(trade)
	}
}

func (vt *VirtualTrader) emitAccount(account *Account) {
	// 发送到 Channel
	select {
	case vt.accountCh <- account:
	default:
	}

	// 调用回调
	vt.mu.RLock()
	handler := vt.onAccount
	vt.mu.RUnlock()

	if handler != nil {
		handler(account)
	}
}

func (vt *VirtualTrader) emitPosition(symbol string, position *Position) {
	// 发送到 Channel
	update := &PositionUpdate{
		Symbol:   symbol,
		Position: position,
	}
	select {
	case vt.positionCh <- update:
	default:
	}

	// 调用回调
	vt.mu.RLock()
	handler := vt.onPosition
	vt.mu.RUnlock()

	if handler != nil {
		handler(symbol, position)
	}
}

func (vt *VirtualTrader) emitPositions(positions map[string]*Position) {
	// 发送到 Channel
	select {
	case vt.positionsCh <- positions:
	default:
	}

	// 调用回调
	vt.mu.RLock()
	handler := vt.onPositions
	vt.mu.RUnlock()

	if handler != nil {
		handler(positions)
	}
}

// ==================== 高级功能（用于回测/模拟） ====================

// SimulateTrade 模拟成交
//
// 此方法用于回测或模拟交易中，手动触发成交
// 参数：
//   - orderID: 订单ID
//   - price: 成交价格
//   - volume: 成交数量
//
// 示例：
//
//	// 在回测中，当价格满足条件时触发成交
//	trader.SimulateTrade(orderID, 3500.0, 1)
func (vt *VirtualTrader) SimulateTrade(orderID string, price float64, volume int64) error {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	order, exists := vt.orders[orderID]
	if !exists {
		return fmt.Errorf("order not found: %s", orderID)
	}

	if order.Status != OrderStatusAlive {
		return fmt.Errorf("order cannot be filled, status: %s", order.Status)
	}

	if volume > order.VolumeLeft {
		return fmt.Errorf("fill volume exceeds order volume left")
	}

	// 生成成交记录
	tradeID := fmt.Sprintf("TRADE_%s_%d", RandomStr(6), time.Now().UnixNano())
	trade := &Trade{
		TradeID:       tradeID,
		OrderID:       orderID,
		ExchangeID:    order.ExchangeID,
		InstrumentID:  order.InstrumentID,
		Direction:     order.Direction,
		Offset:        order.Offset,
		Price:         price,
		Volume:        volume,
		TradeDateTime: time.Now().UnixNano(),
		Commission:    price * float64(volume) * vt.commission,
	}

	vt.trades[tradeID] = trade

	// 更新订单
	order.VolumeLeft -= volume
	if order.VolumeLeft == 0 {
		order.Status = OrderStatusFinished
	}

	// 更新持仓（这里是简化版本，实际需要更复杂的持仓计算）
	// symbol := fmt.Sprintf("%s.%s", order.ExchangeID, order.InstrumentID)
	// TODO: 实现完整的持仓更新逻辑

	// 计算手续费
	commission := trade.Commission

	// 更新账户（简化版本）
	vt.account.Available -= commission
	vt.account.Balance -= commission

	// 触发回调
	go vt.emitTrade(trade)
	go vt.emitOrder(order)
	go vt.emitAccount(vt.account)

	return nil
}

// UpdateMarketPrice 更新市场价格（用于盘口撮合）
//
// 此方法可以接收实时行情，自动撮合未成交订单
// 参数：
//   - symbol: 合约代码
//   - price: 最新价格
func (vt *VirtualTrader) UpdateMarketPrice(symbol string, price float64) {
	vt.mu.Lock()
	defer vt.mu.Unlock()

	// TODO: 实现自动撮合逻辑
	// 遍历所有未成交订单，检查是否满足成交条件
	for _, order := range vt.orders {
		if order.Status != OrderStatusAlive {
			continue
		}

		orderSymbol := fmt.Sprintf("%s.%s", order.ExchangeID, order.InstrumentID)
		if orderSymbol != symbol {
			continue
		}

		// 简化的撮合逻辑（实际需要考虑买卖方向）
		shouldFill := false
		if order.PriceType == PriceTypeAny {
			shouldFill = true
		} else if order.Direction == DirectionBuy && price <= order.LimitPrice {
			shouldFill = true
		} else if order.Direction == DirectionSell && price >= order.LimitPrice {
			shouldFill = true
		}

		if shouldFill {
			// 异步触发成交（避免死锁）
			orderID := order.OrderID
			volume := order.VolumeLeft
			go func() {
				vt.SimulateTrade(orderID, price, volume)
			}()
		}
	}
}

// 编译时检查 VirtualTrader 是否实现了 Trader 接口
var _ Trader = (*VirtualTrader)(nil)
