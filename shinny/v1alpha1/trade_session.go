package shinny

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"go.uber.org/zap"
)

// TradeSession 交易会话
type TradeSession struct {
	client   *Client
	broker   string
	userID   string
	password string // 保存密码用于重连
	ctx      context.Context
	cancel   context.CancelFunc
	ws       *TqTradeWebsocket
	dm       *DataManager

	// 流式 Channels（用于 Channel-based API）
	accountCh      chan *Account
	positionCh     chan *PositionUpdate
	positionsCh    chan map[string]*Position
	orderCh        chan *Order
	tradeCh        chan *Trade
	notificationCh chan *Notification

	// 回调函数（用于 Callback-based API）
	onAccount      func(*Account)
	onPosition     func(string, *Position)
	onPositions    func(map[string]*Position)
	onOrder        func(*Order)
	onTrade        func(*Trade)
	onNotification func(*Notification)
	onError        func(error)

	// 状态
	loggedIn bool
	running  bool
	wg       sync.WaitGroup
	mu       sync.RWMutex
}

// NewTradeSession 创建交易会话
func NewTradeSession(ctx context.Context, client *Client, broker, userID, password string) (*TradeSession, error) {
	if broker == "" || userID == "" || password == "" {
		return nil, NewError("NewTradeSession", fmt.Errorf("broker, userID, password cannot be empty"))
	}

	// 快期模拟特殊处理
	if broker == "快期模拟" {
		userID = client.Auth.(*TqAuth).AuthID
		password = client.Auth.(*TqAuth).AuthID
	}

	sessionCtx, cancel := context.WithCancel(ctx)

	// 创建独立的 DataManager
	initialData := map[string]interface{}{
		"trade": map[string]interface{}{
			userID: map[string]interface{}{
				"accounts": map[string]interface{}{
					"CNY": make(map[string]interface{}),
				},
				"trades":          make(map[string]interface{}),
				"positions":       make(map[string]interface{}),
				"orders":          make(map[string]interface{}),
				"his_settlements": make(map[string]interface{}),
			},
		},
	}
	dm := NewDataManager(initialData)

	// 获取交易服务器 URL
	brokerInfo, err := client.Auth.GetTdUrl(broker, userID)
	if err != nil {
		cancel()
		return nil, NewError("NewTradeSession.GetTdUrl", err)
	}

	urls := []string{brokerInfo.URL}

	// 创建交易 WebSocket
	ws := NewTqTradeWebsocket(urls, dm, client.config.WsConfig)

	session := &TradeSession{
		client:         client,
		broker:         broker,
		userID:         userID,
		password:       password,
		ctx:            sessionCtx,
		cancel:         cancel,
		ws:             ws,
		dm:             dm,
		accountCh:      make(chan *Account, 10),
		positionCh:     make(chan *PositionUpdate, 10),
		positionsCh:    make(chan map[string]*Position, 10),
		orderCh:        make(chan *Order, 100),
		tradeCh:        make(chan *Trade, 100),
		notificationCh: make(chan *Notification, 10),
		running:        false,
	}

	// 注册 WebSocket 通知回调
	ws.OnNotify(func(notify NotifyEvent) {
		notification := &Notification{
			Code:    notify.Code,
			Level:   notify.Level,
			Type:    notify.Type,
			Content: notify.Content,
			BID:     broker,
			UserID:  userID,
		}

		session.emitNotification(notification)
	})

	// 自动连接（保持向后兼容）
	if err := session.Connect(ctx); err != nil {
		cancel()
		return nil, err
	}

	return session, nil
}

// Connect 连接并登录交易服务器
//
// 此方法是幂等的，如果已连接则直接返回 nil
// 可以用于重连场景
func (ts *TradeSession) Connect(ctx context.Context) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	// 如果已登录，直接返回
	if ts.loggedIn {
		return nil
	}

	// 初始化 WebSocket 连接
	if err := ts.ws.Init(false); err != nil {
		return NewError("TradeSession.Connect.Init", err)
	}

	// 发送登录请求
	loginContent := map[string]interface{}{
		"aid":       "req_login",
		"bid":       ts.broker,
		"user_name": ts.userID,
		"password":  ts.password,
	}

	if ts.client.config.ClientAppID != "" {
		loginContent["client_app_id"] = ts.client.config.ClientAppID
		loginContent["client_system_info"] = ts.client.config.ClientSystemInfo
	}

	ts.ws.Send(loginContent)

	// 启动数据监听（仅第一次连接时启动）
	if !ts.running {
		ts.running = true
		ts.wg.Add(1)
		go ts.watchData()
	}

	return nil
}

// watchData 监听数据更新
func (ts *TradeSession) watchData() {
	defer ts.wg.Done()

	// 注册数据更新回调
	ts.dm.OnData(func() {
		ts.processUpdate()
	})

	// 等待上下文取消
	<-ts.ctx.Done()
}

// processUpdate 处理数据更新
func (ts *TradeSession) processUpdate() {
	// 检查是否已登录
	session := ts.dm.GetByPath([]string{"trade", ts.userID, "session"})
	if session != nil {
		if sessionMap, ok := session.(map[string]interface{}); ok {
			if tradingDay, ok := sessionMap["trading_day"]; ok && tradingDay != nil && tradingDay != "" {
				ts.mu.Lock()
				wasLoggedIn := ts.loggedIn
				ts.loggedIn = true
				ts.mu.Unlock()

				if !wasLoggedIn {
					ts.client.logger.Info("Trade session logged in",
						zap.String("broker", ts.broker),
						zap.String("userID", ts.userID))
				}
			}
		}
	}

	// 处理账户更新
	if ts.dm.IsChanging([]string{"trade", ts.userID, "accounts", "CNY"}) {
		ts.processAccountUpdate()
	}

	// 处理持仓更新
	if ts.dm.IsChanging([]string{"trade", ts.userID, "positions"}) {
		ts.processPositionUpdate()
	}

	// 处理订单更新
	if ts.dm.IsChanging([]string{"trade", ts.userID, "orders"}) {
		ts.processOrderUpdate()
	}

	// 处理成交更新
	if ts.dm.IsChanging([]string{"trade", ts.userID, "trades"}) {
		ts.processTradeUpdate()
	}
}

// processAccountUpdate 处理账户更新
func (ts *TradeSession) processAccountUpdate() {
	accountData := ts.dm.GetByPath([]string{"trade", ts.userID, "accounts", "CNY"})
	if accountData == nil {
		return
	}

	var account Account
	if err := ts.dm.ConvertToStruct(accountData, &account); err != nil {
		return
	}

	ts.emitAccount(&account)
}

// processPositionUpdate 处理持仓更新
func (ts *TradeSession) processPositionUpdate() {
	positionsData := ts.dm.GetByPath([]string{"trade", ts.userID, "positions"})
	if positionsData == nil {
		return
	}

	positionsMap, ok := positionsData.(map[string]interface{})
	if !ok {
		return
	}

	positions := make(map[string]*Position)
	for symbol, posData := range positionsMap {
		var pos Position
		if err := ts.dm.ConvertToStruct(posData, &pos); err == nil {
			positions[symbol] = &pos

			// 发送单个持仓更新
			update := &PositionUpdate{
				Symbol:   symbol,
				Position: &pos,
			}
			ts.emitPosition(update)
		}
	}

	// 发送全量持仓
	ts.emitPositions(positions)
}

// processOrderUpdate 处理订单更新
func (ts *TradeSession) processOrderUpdate() {
	ordersData := ts.dm.GetByPath([]string{"trade", ts.userID, "orders"})
	if ordersData == nil {
		return
	}

	ordersMap, ok := ordersData.(map[string]interface{})
	if !ok {
		return
	}

	for orderID, orderData := range ordersMap {
		// 检查该订单是否有更新
		if ts.dm.IsChanging([]string{"trade", ts.userID, "orders", orderID}) {
			var order Order
			if err := ts.dm.ConvertToStruct(orderData, &order); err == nil {
				ts.emitOrder(&order)
			}
		}
	}
}

// processTradeUpdate 处理成交更新
func (ts *TradeSession) processTradeUpdate() {
	tradesData := ts.dm.GetByPath([]string{"trade", ts.userID, "trades"})
	if tradesData == nil {
		return
	}

	tradesMap, ok := tradesData.(map[string]interface{})
	if !ok {
		return
	}

	for tradeID, tradeData := range tradesMap {
		// 检查该成交是否有更新
		if ts.dm.IsChanging([]string{"trade", ts.userID, "trades", tradeID}) {
			var trade Trade
			if err := ts.dm.ConvertToStruct(tradeData, &trade); err == nil {
				ts.emitTrade(&trade)
			}
		}
	}
}

// ==================== 交易操作方法 ====================

// InsertOrder 下单
func (ts *TradeSession) InsertOrder(ctx context.Context, req *InsertOrderRequest) (*Order, error) {
	if !ts.IsLoggedIn() {
		return nil, NewError("InsertOrder", ErrNotLoggedIn)
	}

	// 解析合约代码
	parts := strings.Split(req.Symbol, ".")
	if len(parts) != 2 {
		return nil, NewError("InsertOrder", fmt.Errorf("invalid symbol format: %s", req.Symbol))
	}
	exchangeID, instrumentID := parts[0], parts[1]

	// 生成订单ID
	orderID := fmt.Sprintf("TQGO_%s", RandomStr(8))

	timeCondition := "GFD"
	if req.PriceType == PriceTypeAny {
		timeCondition = "IOC"
	}

	orderCommon := map[string]interface{}{
		"user_id":          ts.userID,
		"order_id":         orderID,
		"exchange_id":      exchangeID,
		"instrument_id":    instrumentID,
		"direction":        req.Direction,
		"offset":           req.Offset,
		"price_type":       req.PriceType,
		"limit_price":      req.LimitPrice,
		"volume_condition": "ANY",
		"time_condition":   timeCondition,
	}

	orderInsert := map[string]interface{}{
		"aid":    "insert_order",
		"volume": req.Volume,
	}
	for k, v := range orderCommon {
		orderInsert[k] = v
	}

	// 发送下单请求
	ts.ws.Send(orderInsert)

	// 初始化订单状态
	orderInit := map[string]interface{}{
		"volume_orign": req.Volume,
		"status":       OrderStatusAlive,
		"volume_left":  req.Volume,
	}
	for k, v := range orderCommon {
		orderInit[k] = v
	}

	ts.dm.MergeData(map[string]interface{}{
		"trade": map[string]interface{}{
			ts.userID: map[string]interface{}{
				"orders": map[string]interface{}{
					orderID: orderInit,
				},
			},
		},
	}, false, false)

	// 获取订单对象
	order := &Order{
		OrderID:      orderID,
		ExchangeID:   exchangeID,
		InstrumentID: instrumentID,
		Direction:    req.Direction,
		Offset:       req.Offset,
		VolumeOrign:  req.Volume,
		VolumeLeft:   req.Volume,
		PriceType:    req.PriceType,
		LimitPrice:   req.LimitPrice,
		Status:       OrderStatusAlive,
	}

	return order, nil
}

// CancelOrder 撤销委托单
func (ts *TradeSession) CancelOrder(ctx context.Context, orderID string) error {
	if !ts.IsLoggedIn() {
		return NewError("CancelOrder", ErrNotLoggedIn)
	}

	ts.ws.Send(map[string]interface{}{
		"aid":      "cancel_order",
		"user_id":  ts.userID,
		"order_id": orderID,
	})

	return nil
}

// ==================== 同步查询方法 ====================

// GetAccount 获取账户信息
func (ts *TradeSession) GetAccount(ctx context.Context) (*Account, error) {
	data := ts.dm.GetByPath([]string{"trade", ts.userID, "accounts", "CNY"})
	if data == nil {
		return nil, fmt.Errorf("account not found")
	}

	var account Account
	if err := ts.dm.ConvertToStruct(data, &account); err != nil {
		return nil, err
	}

	return &account, nil
}

// GetPosition 获取指定合约的持仓
func (ts *TradeSession) GetPosition(ctx context.Context, symbol string) (*Position, error) {
	data := ts.dm.GetByPath([]string{"trade", ts.userID, "positions", symbol})
	if data == nil {
		return nil, fmt.Errorf("position not found: %s", symbol)
	}

	var position Position
	if err := ts.dm.ConvertToStruct(data, &position); err != nil {
		return nil, err
	}

	return &position, nil
}

// GetPositions 获取所有持仓
func (ts *TradeSession) GetPositions(ctx context.Context) (map[string]*Position, error) {
	data := ts.dm.GetByPath([]string{"trade", ts.userID, "positions"})
	if data == nil {
		return make(map[string]*Position), nil
	}

	positionsMap, ok := data.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid positions data format")
	}

	positions := make(map[string]*Position)
	for symbol, posData := range positionsMap {
		var pos Position
		if err := ts.dm.ConvertToStruct(posData, &pos); err == nil {
			positions[symbol] = &pos
		}
	}

	return positions, nil
}

// GetOrders 获取所有委托单
func (ts *TradeSession) GetOrders(ctx context.Context) (map[string]*Order, error) {
	data := ts.dm.GetByPath([]string{"trade", ts.userID, "orders"})
	if data == nil {
		return make(map[string]*Order), nil
	}

	ordersMap, ok := data.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid orders data format")
	}

	orders := make(map[string]*Order)
	for orderID, orderData := range ordersMap {
		var order Order
		if err := ts.dm.ConvertToStruct(orderData, &order); err == nil {
			orders[orderID] = &order
		}
	}

	return orders, nil
}

// GetTrades 获取所有成交记录
func (ts *TradeSession) GetTrades(ctx context.Context) (map[string]*Trade, error) {
	data := ts.dm.GetByPath([]string{"trade", ts.userID, "trades"})
	if data == nil {
		return make(map[string]*Trade), nil
	}

	tradesMap, ok := data.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid trades data format")
	}

	trades := make(map[string]*Trade)
	for tradeID, tradeData := range tradesMap {
		var trade Trade
		if err := ts.dm.ConvertToStruct(tradeData, &trade); err == nil {
			trades[tradeID] = &trade
		}
	}

	return trades, nil
}

// ==================== 流式接口（Channel-based） ====================

// AccountChannel 获取账户更新 Channel
func (ts *TradeSession) AccountChannel() <-chan *Account {
	return ts.accountCh
}

// PositionChannel 获取单个持仓更新 Channel
func (ts *TradeSession) PositionChannel() <-chan *PositionUpdate {
	return ts.positionCh
}

// PositionsChannel 获取全量持仓更新 Channel
func (ts *TradeSession) PositionsChannel() <-chan map[string]*Position {
	return ts.positionsCh
}

// OrderChannel 获取订单更新 Channel
func (ts *TradeSession) OrderChannel() <-chan *Order {
	return ts.orderCh
}

// TradeChannel 获取成交更新 Channel
func (ts *TradeSession) TradeChannel() <-chan *Trade {
	return ts.tradeCh
}

// NotificationChannel 获取通知 Channel
func (ts *TradeSession) NotificationChannel() <-chan *Notification {
	return ts.notificationCh
}

// ==================== 回调接口（Callback-based） ====================

// OnAccount 注册账户更新回调
func (ts *TradeSession) OnAccount(handler func(*Account)) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.onAccount = handler
}

// OnPosition 注册单个持仓更新回调
func (ts *TradeSession) OnPosition(handler func(string, *Position)) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.onPosition = handler
}

// OnPositions 注册全量持仓更新回调
func (ts *TradeSession) OnPositions(handler func(map[string]*Position)) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.onPositions = handler
}

// OnOrder 注册订单更新回调
func (ts *TradeSession) OnOrder(handler func(*Order)) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.onOrder = handler
}

// OnTrade 注册成交回调
func (ts *TradeSession) OnTrade(handler func(*Trade)) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.onTrade = handler
}

// OnNotification 注册通知回调
func (ts *TradeSession) OnNotification(handler func(*Notification)) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.onNotification = handler
}

// OnError 注册错误回调
func (ts *TradeSession) OnError(handler func(error)) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.onError = handler
}

// ==================== 内部方法 ====================

// emit methods 发送数据到 Channel 和回调
func (ts *TradeSession) emitAccount(account *Account) {
	// 发送到 Channel（非阻塞）
	select {
	case ts.accountCh <- account:
	default:
	}

	// 调用回调
	ts.mu.RLock()
	handler := ts.onAccount
	ts.mu.RUnlock()

	if handler != nil {
		go handler(account)
	}
}

func (ts *TradeSession) emitPosition(update *PositionUpdate) {
	// 发送到 Channel（非阻塞）
	select {
	case ts.positionCh <- update:
	default:
	}

	// 调用回调
	ts.mu.RLock()
	handler := ts.onPosition
	ts.mu.RUnlock()

	if handler != nil {
		go handler(update.Symbol, update.Position)
	}
}

func (ts *TradeSession) emitPositions(positions map[string]*Position) {
	// 发送到 Channel（非阻塞）
	select {
	case ts.positionsCh <- positions:
	default:
	}

	// 调用回调
	ts.mu.RLock()
	handler := ts.onPositions
	ts.mu.RUnlock()

	if handler != nil {
		go handler(positions)
	}
}

func (ts *TradeSession) emitOrder(order *Order) {
	// 发送到 Channel（非阻塞）
	select {
	case ts.orderCh <- order:
	default:
	}

	// 调用回调
	ts.mu.RLock()
	handler := ts.onOrder
	ts.mu.RUnlock()

	if handler != nil {
		go handler(order)
	}
}

func (ts *TradeSession) emitTrade(trade *Trade) {
	// 发送到 Channel（非阻塞）
	select {
	case ts.tradeCh <- trade:
	default:
	}

	// 调用回调
	ts.mu.RLock()
	handler := ts.onTrade
	ts.mu.RUnlock()

	if handler != nil {
		go handler(trade)
	}
}

func (ts *TradeSession) emitNotification(notification *Notification) {
	// 发送到 Channel（非阻塞）
	select {
	case ts.notificationCh <- notification:
	default:
	}

	// 调用回调
	ts.mu.RLock()
	handler := ts.onNotification
	ts.mu.RUnlock()

	if handler != nil {
		go handler(notification)
	}
}

// IsReady 检查交易会话是否已就绪（已登录）
func (ts *TradeSession) IsReady() bool {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.loggedIn
}

// IsLoggedIn 检查是否已登录（IsReady 的别名，保持向后兼容）
func (ts *TradeSession) IsLoggedIn() bool {
	return ts.IsReady()
}

// Close 关闭会话
func (ts *TradeSession) Close() error {
	ts.mu.Lock()
	if !ts.running {
		ts.mu.Unlock()
		return nil
	}
	ts.running = false
	ts.mu.Unlock()

	ts.cancel()

	if ts.ws != nil {
		ts.ws.Close()
	}

	ts.wg.Wait()

	// 关闭所有 Channels
	close(ts.accountCh)
	close(ts.positionCh)
	close(ts.positionsCh)
	close(ts.orderCh)
	close(ts.tradeCh)
	close(ts.notificationCh)

	return nil
}
