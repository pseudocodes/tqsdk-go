package tqsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"go.uber.org/zap"
)

// WebSocketStatus WebSocket 连接状态
type WebSocketStatus int

const (
	StatusConnecting WebSocketStatus = 0
	StatusOpen       WebSocketStatus = 1
	StatusClosing    WebSocketStatus = 2
	StatusClosed     WebSocketStatus = 3
)

// WebSocketConfig WebSocket 配置
type WebSocketConfig struct {
	Headers           http.Header
	ReconnectInterval time.Duration // 重连间隔
	ReconnectMaxTimes int           // 最大重连次数
	Logger            *zap.Logger   // 日志记录器
}

// DefaultWebSocketConfig 默认配置
func DefaultWebSocketConfig() WebSocketConfig {
	return WebSocketConfig{
		ReconnectInterval: 3 * time.Second,
		ReconnectMaxTimes: 2,
		Logger:            NewDefaultLogger(),
	}
}

// TqWebsocket 天勤 WebSocket 基类
type TqWebsocket struct {
	urlList           []string
	conn              *websocket.Conn
	ctx               context.Context
	cancel            context.CancelFunc
	queue             []string
	queueMu           sync.Mutex
	status            WebSocketStatus
	statusMu          sync.RWMutex
	headers           http.Header
	reconnect         bool
	reconnectTimes    int
	reconnectUrlIndex int
	reconnectTask     *time.Timer
	config            WebSocketConfig
	logger            *zap.Logger

	// 事件回调
	onMessage   func(data map[string]interface{})
	onOpen      func()
	onClose     func()
	onError     func(error)
	onReconnect func()
	onDeath     func(msg string)
}

// NewTqWebsocket 创建新的 WebSocket 连接
func NewTqWebsocket(url interface{}, config WebSocketConfig) *TqWebsocket {
	var urlList []string
	switch v := url.(type) {
	case string:
		urlList = []string{v}
	case []string:
		urlList = v
	default:
		urlList = []string{}
	}

	if config.Logger == nil {
		config.Logger = NewDefaultLogger()
	}

	ctx, cancel := context.WithCancel(context.Background())

	ws := &TqWebsocket{
		urlList:           urlList,
		ctx:               ctx,
		cancel:            cancel,
		headers:           config.Headers,
		queue:             make([]string, 0),
		status:            StatusClosed,
		reconnect:         true,
		reconnectTimes:    0,
		reconnectUrlIndex: 0,
		config:            config,
		logger:            config.Logger,
	}

	return ws
}

// Init 初始化 WebSocket 连接
func (ws *TqWebsocket) Init(isReconnection bool) error {
	if len(ws.urlList) == 0 {
		return fmt.Errorf("no URL provided")
	}

	url := ws.urlList[ws.reconnectUrlIndex]

	ws.logger.Info("Connecting to WebSocket",
		zap.String("url", url),
		zap.Bool("reconnection", isReconnection),
		zap.Int("reconnect_times", ws.reconnectTimes))

	if isReconnection && ws.reconnectUrlIndex == len(ws.urlList)-1 {
		ws.reconnectTimes++
	}

	// 配置 WebSocket 选项，启用 deflate 压缩
	opts := &websocket.DialOptions{
		HTTPHeader:      ws.headers,
		CompressionMode: websocket.CompressionContextTakeover,
	}

	conn, _, err := websocket.Dial(ws.ctx, url, opts)
	if err != nil {
		ws.logger.Error("Failed to connect WebSocket",
			zap.String("url", url),
			zap.Error(err))
		return err
	}

	ws.conn = conn
	ws.setStatus(StatusOpen)

	// 触发 onOpen 回调
	if ws.onOpen != nil {
		ws.onOpen()
	}

	// 发送队列中的消息
	ws.flushQueue()

	// 启动消息接收循环
	go ws.receiveLoop()

	return nil
}

// Send 发送消息
func (ws *TqWebsocket) Send(obj interface{}) error {
	var jsonStr string

	switch v := obj.(type) {
	case string:
		jsonStr = v
	default:
		data, err := json.Marshal(obj)
		if err != nil {
			ws.logger.Error("Failed to marshal message", zap.Error(err))
			return err
		}
		jsonStr = string(data)
	}

	if ws.IsReady() {
		ws.logger.Debug("WebSocket sending message",
			zap.String("url", ws.getCurrentURL()),
			zap.String("message", jsonStr))

		err := ws.conn.Write(ws.ctx, websocket.MessageText, []byte(jsonStr))
		if err != nil {
			ws.logger.Error("Failed to send message", zap.Error(err))
			return err
		}
	} else {
		ws.logger.Debug("WebSocket not ready, queueing message",
			zap.String("message", jsonStr))
		ws.queueMu.Lock()
		ws.queue = append(ws.queue, jsonStr)
		ws.queueMu.Unlock()
	}

	return nil
}

// IsReady 检查连接是否就绪
func (ws *TqWebsocket) IsReady() bool {
	ws.statusMu.RLock()
	defer ws.statusMu.RUnlock()
	return ws.status == StatusOpen && ws.conn != nil
}

// Close 关闭连接
func (ws *TqWebsocket) Close() error {
	ws.reconnect = false

	if ws.reconnectTask != nil {
		ws.reconnectTask.Stop()
	}

	ws.cancel()

	if ws.conn != nil {
		err := ws.conn.Close(websocket.StatusNormalClosure, "closing")
		ws.setStatus(StatusClosed)
		return err
	}

	return nil
}

// receiveLoop 消息接收循环
func (ws *TqWebsocket) receiveLoop() {
	defer func() {
		ws.setStatus(StatusClosed)
		ws.handleClose()
	}()

	for {
		select {
		case <-ws.ctx.Done():
			return
		default:
		}

		_, message, err := ws.conn.Read(ws.ctx)
		if err != nil {
			if ws.ctx.Err() != nil {
				// Context 已取消，正常退出
				return
			}

			ws.logger.Error("Failed to read message", zap.Error(err))
			if ws.onError != nil {
				ws.onError(err)
			}
			return
		}

		ws.logger.Debug("WebSocket received message",
			zap.String("url", ws.getCurrentURL()),
			zap.String("message", string(message)),
			zap.Int("length", len(message)))

		// 解析 JSON
		var data map[string]interface{}
		if err := json.Unmarshal(message, &data); err != nil {
			ws.logger.Error("Failed to unmarshal message",
				zap.Error(err),
				zap.String("message", string(message)))
			continue
		}

		// 触发消息回调
		if ws.onMessage != nil {
			ws.onMessage(data)
		}

		// 发送 peek_message
		ws.Send(map[string]string{"aid": "peek_message"})
	}
}

// handleClose 处理连接关闭
func (ws *TqWebsocket) handleClose() {
	ws.logger.Info("WebSocket connection closed",
		zap.String("url", ws.getCurrentURL()))

	// 触发 onClose 回调
	if ws.onClose != nil {
		ws.onClose()
	}

	// 清空队列
	ws.queueMu.Lock()
	ws.queue = make([]string, 0)
	ws.queueMu.Unlock()

	// 自动重连
	if ws.reconnect {
		if ws.reconnectTimes >= ws.config.ReconnectMaxTimes {
			ws.logger.Error("Max reconnect times reached",
				zap.Int("max_times", ws.config.ReconnectMaxTimes))
			if ws.onDeath != nil {
				ws.onDeath(fmt.Sprintf("超过最大重连次数 %d", ws.config.ReconnectMaxTimes))
			}
		} else {
			ws.logger.Info("Scheduling reconnect",
				zap.Duration("interval", ws.config.ReconnectInterval),
				zap.Int("times", ws.reconnectTimes))

			ws.reconnectTask = time.AfterFunc(ws.config.ReconnectInterval, func() {
				// 更新 URL 索引
				ws.reconnectUrlIndex = (ws.reconnectUrlIndex + 1) % len(ws.urlList)

				if ws.onReconnect != nil {
					ws.onReconnect()
				}

				// 重新初始化连接
				ws.Init(true)
			})
		}
	}
}

// flushQueue 发送队列中的所有消息
func (ws *TqWebsocket) flushQueue() {
	ws.queueMu.Lock()
	defer ws.queueMu.Unlock()

	for len(ws.queue) > 0 {
		if !ws.IsReady() {
			break
		}

		msg := ws.queue[0]
		ws.queue = ws.queue[1:]

		ws.logger.Debug("Flushing queued message", zap.String("message", msg))
		ws.conn.Write(ws.ctx, websocket.MessageText, []byte(msg))
	}
}

// setStatus 设置连接状态
func (ws *TqWebsocket) setStatus(status WebSocketStatus) {
	ws.statusMu.Lock()
	defer ws.statusMu.Unlock()
	ws.status = status
}

// GetStatus 获取连接状态
func (ws *TqWebsocket) GetStatus() WebSocketStatus {
	ws.statusMu.RLock()
	defer ws.statusMu.RUnlock()
	return ws.status
}

// getCurrentURL 获取当前连接的 URL
func (ws *TqWebsocket) getCurrentURL() string {
	if ws.reconnectUrlIndex < len(ws.urlList) {
		return ws.urlList[ws.reconnectUrlIndex]
	}
	return ""
}

// OnMessage 注册消息回调
func (ws *TqWebsocket) OnMessage(callback func(data map[string]interface{})) {
	ws.onMessage = callback
}

// OnOpen 注册连接打开回调
func (ws *TqWebsocket) OnOpen(callback func()) {
	ws.onOpen = callback
}

// OnClose 注册连接关闭回调
func (ws *TqWebsocket) OnClose(callback func()) {
	ws.onClose = callback
}

// OnError 注册错误回调
func (ws *TqWebsocket) OnError(callback func(error)) {
	ws.onError = callback
}

// OnReconnect 注册重连回调
func (ws *TqWebsocket) OnReconnect(callback func()) {
	ws.onReconnect = callback
}

// OnDeath 注册死亡回调（不再重连）
func (ws *TqWebsocket) OnDeath(callback func(msg string)) {
	ws.onDeath = callback
}

// TqTradeWebsocket 交易 WebSocket
type TqTradeWebsocket struct {
	*TqWebsocket
	dm       *DataManager
	reqLogin map[string]interface{}
	onNotify func(NotifyEvent)
}

// NewTqTradeWebsocket 创建交易 WebSocket
func NewTqTradeWebsocket(url interface{}, dm *DataManager, config WebSocketConfig) *TqTradeWebsocket {
	base := NewTqWebsocket(url, config)

	tw := &TqTradeWebsocket{
		TqWebsocket: base,
		dm:          dm,
	}

	tw.initHandlers()
	return tw
}

// initHandlers 初始化处理器
func (tw *TqTradeWebsocket) initHandlers() {
	tw.OnMessage(func(data map[string]interface{}) {
		aid, ok := data["aid"].(string)
		if !ok {
			return
		}

		switch aid {
		case "rtn_data":
			// 提取通知
			if payload, ok := data["data"].([]interface{}); ok {
				notifies := tw.separateNotifies(payload)
				for _, notify := range notifies {
					if tw.onNotify != nil {
						tw.onNotify(notify)
					}
				}

				// 合并数据
				tw.dm.MergeData(data["data"], true, true)
			}

		case "rtn_brokers":
			// 期货公司列表
			// dump.V(data)
			if brokers, ok := data["brokers"].([]interface{}); ok {
				for _, broker := range brokers {
					if brokerMap, ok := broker.(map[string]interface{}); ok {
						tw.dm.MergeData(brokerMap, true, true)
					}
				}
			}

		case "qry_settlement_info":
			// 历史结算单
			if settlementInfo, ok := data["settlement_info"].(string); ok {
				settlement := ParseSettlementContent(settlementInfo)
				if userName, ok := data["user_name"].(string); ok {
					if tradingDay, ok := data["trading_day"].(string); ok {
						settlement.TradingDay = tradingDay
						tw.dm.MergeData(map[string]interface{}{
							"trade": map[string]interface{}{
								userName: map[string]interface{}{
									"his_settlements": map[string]interface{}{
										tradingDay: settlement,
									},
								},
							},
						}, true, true)
					}
				}
			}
		}
	})

	tw.OnReconnect(func() {
		if tw.reqLogin != nil {
			tw.Send(tw.reqLogin)
		}
	})
}

// Send 重写 Send 方法，记录登录请求
func (tw *TqTradeWebsocket) Send(obj interface{}) error {
	if m, ok := obj.(map[string]interface{}); ok {
		if aid, ok := m["aid"].(string); ok && aid == "req_login" {
			tw.reqLogin = m
		}
	}
	return tw.TqWebsocket.Send(obj)
}

// OnNotify 注册通知回调
func (tw *TqTradeWebsocket) OnNotify(callback func(NotifyEvent)) {
	tw.onNotify = callback
}

// separateNotifies 提取通知
func (tw *TqTradeWebsocket) separateNotifies(data []interface{}) []NotifyEvent {
	notifies := make([]NotifyEvent, 0)

	for i := 0; i < len(data); i++ {
		if item, ok := data[i].(map[string]interface{}); ok {
			if notifyData, exists := item["notify"]; exists {
				// 移除 notify 字段
				delete(item, "notify")

				// 提取通知
				if notifyMap, ok := notifyData.(map[string]interface{}); ok {
					for _, v := range notifyMap {
						if n, ok := v.(map[string]interface{}); ok {
							notify := NotifyEvent{}
							if code, ok := n["code"].(string); ok {
								notify.Code = code
							}
							if level, ok := n["level"].(string); ok {
								notify.Level = level
							}
							if typ, ok := n["type"].(string); ok {
								notify.Type = typ
							}
							if content, ok := n["content"].(string); ok {
								notify.Content = content
							}
							notifies = append(notifies, notify)
						}
					}
				}
			}
		}
	}

	return notifies
}

// TqQuoteWebsocket 行情 WebSocket
type TqQuoteWebsocket struct {
	*TqWebsocket
	dm             *DataManager
	subscribeQuote map[string]interface{}
	charts         map[string]map[string]interface{}
	chartsMu       sync.RWMutex
}

// NewTqQuoteWebsocket 创建行情 WebSocket
func NewTqQuoteWebsocket(url string, dm *DataManager, config WebSocketConfig) *TqQuoteWebsocket {
	base := NewTqWebsocket(url, config)

	qw := &TqQuoteWebsocket{
		TqWebsocket: base,
		dm:          dm,
		charts:      make(map[string]map[string]interface{}),
	}

	qw.initHandlers()
	return qw
}

// initHandlers 初始化处理器
func (qw *TqQuoteWebsocket) initHandlers() {
	qw.OnMessage(func(data map[string]interface{}) {
		aid, ok := data["aid"].(string)
		if !ok {
			return
		}

		if aid == "rtn_data" {
			if payload, ok := data["data"]; ok {
				qw.dm.MergeData(payload, true, true)
			}
		}
	})

	qw.OnReconnect(func() {
		if qw.subscribeQuote != nil {
			qw.Send(qw.subscribeQuote)
		}

		qw.chartsMu.RLock()
		for _, chart := range qw.charts {
			if viewWidth, ok := chart["view_width"].(float64); ok && viewWidth > 0 {
				qw.Send(chart)
			}
		}
		qw.chartsMu.RUnlock()
	})
}

// Send 重写 Send 方法，记录订阅和图表请求
func (qw *TqQuoteWebsocket) Send(obj interface{}) error {
	if m, ok := obj.(map[string]interface{}); ok {
		aid, ok := m["aid"].(string)
		if !ok {
			return qw.TqWebsocket.Send(obj)
		}

		if aid == "subscribe_quote" {
			// 检查是否需要更新订阅
			if qw.subscribeQuote == nil {
				qw.subscribeQuote = m
				return qw.TqWebsocket.Send(obj)
			}

			// 比较 ins_list
			oldList, _ := json.Marshal(qw.subscribeQuote["ins_list"])
			newList, _ := json.Marshal(m["ins_list"])
			if string(oldList) != string(newList) {
				qw.subscribeQuote = m
				return qw.TqWebsocket.Send(obj)
			}
			return nil

		} else if aid == "set_chart" {
			chartID, ok := m["chart_id"].(string)
			if !ok {
				return qw.TqWebsocket.Send(obj)
			}

			qw.chartsMu.Lock()
			if viewWidth, ok := m["view_width"].(float64); ok && viewWidth == 0 {
				delete(qw.charts, chartID)
			} else {
				qw.charts[chartID] = m
			}
			qw.chartsMu.Unlock()

			return qw.TqWebsocket.Send(obj)
		}
	}

	return qw.TqWebsocket.Send(obj)
}

// TqRecvOnlyWebsocket 只读 WebSocket
type TqRecvOnlyWebsocket struct {
	*TqWebsocket
	dm *DataManager
}

// NewTqRecvOnlyWebsocket 创建只读 WebSocket
func NewTqRecvOnlyWebsocket(url string, dm *DataManager, config WebSocketConfig) *TqRecvOnlyWebsocket {
	base := NewTqWebsocket(url, config)

	rw := &TqRecvOnlyWebsocket{
		TqWebsocket: base,
		dm:          dm,
	}

	rw.initHandlers()
	return rw
}

// initHandlers 初始化处理器
func (rw *TqRecvOnlyWebsocket) initHandlers() {
	rw.OnMessage(func(data map[string]interface{}) {
		aid, ok := data["aid"].(string)
		if !ok {
			return
		}

		if aid == "rtn_data" {
			if payload, ok := data["data"]; ok {
				rw.dm.MergeData(payload, true, true)
			}
		}
	})
}
