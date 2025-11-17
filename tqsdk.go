package tqsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"go.uber.org/zap"
)

// TQSDKConfig TQSDK 配置
type TQSDKConfig struct {
	SymbolsServerURL string          // 合约服务地址
	WsQuoteURL       string          // 行情连接地址
	WsTdURL          string          // 交易连接地址（从期货公司列表获取）
	AutoInit         bool            // 自动初始化
	ClientSystemInfo string          // 客户端系统信息
	ClientAppID      string          // 客户端应用ID
	AccessToken      string          // 访问令牌
	LogConfig        LogConfig       // 日志配置
	WsConfig         WebSocketConfig // WebSocket 配置
}

// DefaultTQSDKConfig 默认配置
func DefaultTQSDKConfig() TQSDKConfig {
	return TQSDKConfig{
		SymbolsServerURL: "https://openmd.shinnytech.com/t/md/symbols/latest.json",
		// WsQuoteURL:       "wss://openmd.shinnytech.com/t/md/front/mobile",
		AutoInit: true,
		LogConfig: LogConfig{
			Level:       "info",
			OutputPath:  "stdout",
			Development: false,
		},
		WsConfig: DefaultWebSocketConfig(),
	}
}

// TQSDK 天勤 SDK 主类
type TQSDK struct {
	*EventEmitter // 嵌入事件发射器

	config TQSDKConfig
	logger *zap.Logger

	auth *TqAuth

	// 数据管理
	dm         *DataManager
	quotesInfo map[string]map[string]interface{} // 合约信息
	quotesWs   *TqQuoteWebsocket

	// 交易相关
	brokersList     map[string]interface{}
	brokers         []string
	tradeAccounts   map[string]*TradeAccount
	tradeAccountsMu sync.RWMutex

	// 订阅管理
	subscribeQuotesSet map[string]bool
	subscribeQuotesMu  sync.RWMutex

	// 控制
	ctx    context.Context
	cancel context.CancelFunc
	prefix string
}

// TradeAccount 交易账户
type TradeAccount struct {
	BID          string
	UserID       string
	Password     string
	Ws           *TqTradeWebsocket
	Dm           *DataManager
	hasConfirmed bool
}

// NewTQSDK 创建新的 TQSDK 实例
func NewTQSDK(tquser, tqpassword string, config TQSDKConfig) *TQSDK {
	// 创建 logger
	logger, err := NewLogger(config.LogConfig)
	if err != nil {
		logger = NewDefaultLogger()
	}

	config.WsConfig.Logger = logger

	tqauth := NewTqAuth(tquser, tqpassword)
	if err := tqauth.Login(); err != nil {
		logger.Error("login error", zap.Error(err))
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())

	// 初始化数据
	initialData := map[string]interface{}{
		"klines": make(map[string]interface{}),
		"quotes": make(map[string]interface{}),
		"charts": make(map[string]interface{}),
		"ticks":  make(map[string]interface{}),
		"trade":  make(map[string]interface{}),
	}

	config.AccessToken = tqauth.accessToken
	config.WsConfig.Headers = tqauth.BaseHeader()

	tqsdk := &TQSDK{
		EventEmitter:       NewEventEmitter(),
		auth:               tqauth,
		config:             config,
		logger:             logger,
		dm:                 NewDataManager(initialData),
		quotesInfo:         make(map[string]map[string]interface{}),
		tradeAccounts:      make(map[string]*TradeAccount),
		subscribeQuotesSet: make(map[string]bool),
		ctx:                ctx,
		cancel:             cancel,
		prefix:             "TQGO_",
	}

	// 注册数据更新回调
	tqsdk.dm.OnData(func() {
		tqsdk.Emit(EventRtnData, nil)
	})

	if config.AutoInit {
		tqsdk.InitMdWebsocket()
		tqsdk.InitTdWebsocket()
	}

	return tqsdk
}

// InitMdWebsocket 初始化行情连接
func (t *TQSDK) InitMdWebsocket() error {
	if t.quotesWs != nil {
		return nil
	}

	// 下载合约信息
	go func() {
		data, err := t.fetchJSON(t.config.SymbolsServerURL)
		if err != nil {
			t.logger.Error("Failed to fetch symbols", zap.Error(err))
			t.Emit(EventError, err)
			return
		}

		if quotesMap, ok := data.(map[string]interface{}); ok {
			// 处理合约类别
			for symbol, quoteData := range quotesMap {
				if quoteMap, ok := quoteData.(map[string]interface{}); ok {
					if class, ok := quoteMap["class"].(string); ok && class == "FUTURE_OPTION" {
						quoteMap["class"] = "OPTION"
					}
					t.quotesInfo[symbol] = quoteMap
				}
			}
		}

		t.logger.Info("Symbols loaded", zap.Int("count", len(t.quotesInfo)))
		t.Emit(EventReady, nil)
		t.Emit(EventRtnData, nil)
	}()
	wsQuoteURL, err := t.auth.GetMdUrl(true, false)
	if err != nil {
		t.logger.Error("Failed to get md url", zap.Error(err))
		return err
	}
	t.config.WsQuoteURL = wsQuoteURL
	// 创建行情 WebSocket
	t.quotesWs = NewTqQuoteWebsocket(t.config.WsQuoteURL, t.dm, t.config.WsConfig)

	// 设置全局事件发射器，允许 WebSocket 层的事件穿透到 TQSDK 层
	t.quotesWs.SetGlobalEmitter(t.EventEmitter)

	return t.quotesWs.Init(false)
}

// InitTdWebsocket 初始化交易连接信息
func (t *TQSDK) InitTdWebsocket() error {
	if t.brokers != nil {
		return nil
	}

	return nil
}

// fetchJSON 获取 JSON 数据
func (t *TQSDK) fetchJSON(url string) (interface{}, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	return result, nil
}

// GetQuote 获取行情
func (t *TQSDK) GetQuote(symbol string) map[string]interface{} {
	if symbol == "" {
		return make(map[string]interface{})
	}

	quoteInfo, exists := t.quotesInfo[symbol]
	if !exists {
		return make(map[string]interface{})
	}

	// 从 DataManager 获取或创建 quote
	quote := t.dm.SetDefault([]string{"quotes", symbol}, make(map[string]interface{}))

	if quoteMap, ok := quote.(map[string]interface{}); ok {
		// 如果还没有 class 字段，说明是新创建的，需要合并合约信息
		if _, hasClass := quoteMap["class"]; !hasClass {
			// 合并合约信息
			lastPrice := quoteMap["last_price"]
			if lastPrice == nil && quoteInfo["last_price"] != nil {
				lastPrice = quoteInfo["last_price"]
			}

			for k, v := range quoteInfo {
				if _, exists := quoteMap[k]; !exists {
					quoteMap[k] = v
				}
			}
			if lastPrice != nil {
				quoteMap["last_price"] = lastPrice
			}
		}

		// 订阅行情
		t.SubscribeQuote([]string{symbol})

		return quoteMap
	}

	return make(map[string]interface{})
}

// SubscribeQuote 订阅行情
func (t *TQSDK) SubscribeQuote(quotes []string) {
	t.subscribeQuotesMu.Lock()
	defer t.subscribeQuotesMu.Unlock()

	beginSize := len(t.subscribeQuotesSet)

	// 添加所有持仓合约
	t.tradeAccountsMu.RLock()
	for _, account := range t.tradeAccounts {
		positions := account.Dm.GetByPath([]string{"trade", account.UserID, "positions"})
		if posMap, ok := positions.(map[string]interface{}); ok {
			for symbol := range posMap {
				t.subscribeQuotesSet[symbol] = true
			}
		}
	}
	t.tradeAccountsMu.RUnlock()

	// 添加指定合约
	for _, symbol := range quotes {
		t.subscribeQuotesSet[symbol] = true
	}

	// 如果集合大小没变，说明没有新合约需要订阅
	if beginSize == len(t.subscribeQuotesSet) {
		return
	}

	// 构建订阅列表
	insList := make([]string, 0, len(t.subscribeQuotesSet))
	for symbol := range t.subscribeQuotesSet {
		insList = append(insList, symbol)
	}

	if t.quotesWs != nil {
		t.quotesWs.Send(map[string]interface{}{
			"aid":      "subscribe_quote",
			"ins_list": strings.Join(insList, ","),
		})
	}
}

// SetChart 请求 K 线图表
func (t *TQSDK) SetChart(payload map[string]interface{}) map[string]interface{} {
	content := make(map[string]interface{})

	if tradingDayStart, ok := payload["trading_day_start"]; ok {
		content["trading_day_start"] = tradingDayStart
		if tradingDayCount, ok := payload["trading_day_count"]; ok {
			content["trading_day_count"] = tradingDayCount
		} else {
			content["trading_day_count"] = 3600 * 24 * 1e9
		}
	} else {
		if viewWidth, ok := payload["view_width"]; ok {
			content["view_width"] = viewWidth
		} else {
			content["view_width"] = 500
		}

		if leftKlineID, ok := payload["left_kline_id"]; ok {
			content["left_kline_id"] = leftKlineID
			if !t.auth.HasFeature("td_dl") {
				t.logger.Error("数据获取方式仅限专业版用户使用，如需购买专业版或者申请试用，请访问 https://www.shinnytech.com/tqsdk-buy/")
				return nil
			}
		} else if focusDatetime, ok := payload["focus_datetime"]; ok {
			if !t.auth.HasFeature("td_dl") {
				t.logger.Error("数据获取方式仅限专业版用户使用，如需购买专业版或者申请试用，请访问 https://www.shinnytech.com/tqsdk-buy/")
				return nil
			}
			content["focus_datetime"] = focusDatetime
			if focusPosition, ok := payload["focus_position"]; ok {
				content["focus_position"] = focusPosition
			} else {
				content["focus_position"] = 0
			}
		}
	}

	chartID := t.prefix + "kline_chart"
	if id, ok := payload["chart_id"].(string); ok {
		chartID = id
	}

	sendChart := map[string]interface{}{
		"aid":      "set_chart",
		"chart_id": chartID,
		"duration": payload["duration"],
	}

	if insList, ok := payload["ins_list"].([]string); ok {
		if err := t.auth.HasMdGrants(insList...); err != nil {
			t.logger.Error("HasMdGrants error", zap.Error(err))
			return nil
		}
		sendChart["ins_list"] = strings.Join(insList, ",")
	} else if symbol, ok := payload["symbol"].(string); ok {
		if err := t.auth.HasMdGrants(symbol); err != nil {
			t.logger.Error("HasMdGrants error", zap.Error(err))
			return nil
		}
		sendChart["ins_list"] = symbol
	}

	for k, v := range content {
		sendChart[k] = v
	}

	if t.quotesWs != nil {
		t.quotesWs.Send(sendChart)
	}

	// 返回图表对象
	chart := t.dm.SetDefault([]string{"charts", chartID}, NewChart(sendChart))
	if chartMap, ok := chart.(map[string]interface{}); ok {
		return chartMap
	}

	return make(map[string]interface{})
}

// GetKlines 获取 K 线序列（原始 map 格式）
func (t *TQSDK) GetKlines(symbol string, duration int64) map[string]interface{} {
	if symbol == "" {
		return nil
	}

	ks := t.dm.GetByPath([]string{"klines", symbol, fmt.Sprintf("%d", duration)})
	// dump.V(ks)
	if ks == nil || (ks != nil && t.getLastID(ks) == -1) {
		// 初始化 K 线数据
		t.dm.MergeData(map[string]interface{}{
			"klines": map[string]interface{}{
				symbol: map[string]interface{}{
					fmt.Sprintf("%d", duration): map[string]interface{}{
						"trading_day_end_id":   -1,
						"trading_day_start_id": -1,
						"last_id":              -1,
						"data":                 make(map[string]interface{}),
					},
				},
			},
		}, false, false)
		ks = t.dm.GetByPath([]string{"klines", symbol, fmt.Sprintf("%d", duration)})
	}

	if ksMap, ok := ks.(map[string]interface{}); ok {
		return ksMap
	}

	return make(map[string]interface{})
}

// GetKlinesData 获取 K 线序列数据（结构化数组格式）
func (t *TQSDK) GetKlinesData(symbol string, duration int64) (*KlineSeriesData, error) {
	if symbol == "" {
		return nil, fmt.Errorf("symbol cannot be empty")
	}
	// 确保数据已初始化
	t.GetKlines(symbol, duration)

	// 从 DataManager 获取结构化数据
	return t.dm.GetKlinesData(symbol, duration)
}

// GetTicks 获取 Tick 序列（原始 map 格式）
func (t *TQSDK) GetTicks(symbol string) map[string]interface{} {
	if symbol == "" {
		return nil
	}

	ts := t.dm.GetByPath([]string{"ticks", symbol})
	if ts == nil {
		// 初始化 Tick 数据
		t.dm.MergeData(map[string]interface{}{
			"ticks": map[string]interface{}{
				symbol: map[string]interface{}{
					"last_id": -1,
					"data":    make(map[string]interface{}),
				},
			},
		}, false, false)
		ts = t.dm.GetByPath([]string{"ticks", symbol})
	}

	if tsMap, ok := ts.(map[string]interface{}); ok {
		return tsMap
	}

	return make(map[string]interface{})
}

// GetTicksData 获取 Tick 序列数据（结构化数组格式）
func (t *TQSDK) GetTicksData(symbol string) (*TickSeriesData, error) {
	if symbol == "" {
		return nil, fmt.Errorf("symbol cannot be empty")
	}

	// 确保数据已初始化
	t.GetTicks(symbol)

	// 从 DataManager 获取结构化数据
	return t.dm.GetTicksData(symbol)
}

// IsChanging 判断数据是否变化
func (t *TQSDK) IsChanging(pathArray []string) bool {
	return t.dm.IsChanging(pathArray)
}

// GetByPath 根据路径获取数据
func (t *TQSDK) GetByPath(pathArray []string) interface{} {
	return t.dm.GetByPath(pathArray)
}

// GetQuotesByInput 根据输入查询合约列表
func (t *TQSDK) GetQuotesByInput(input string, filterOption map[string]bool) []string {
	if input == "" {
		return []string{}
	}

	// 默认过滤选项
	option := map[string]interface{}{
		"input":           strings.ToLower(input),
		"symbol":          true,
		"pinyin":          true,
		"include_expired": false,
		"FUTURE":          true,
		"FUTURE_INDEX":    false,
		"FUTURE_CONT":     false,
		"OPTION":          false,
		"COMBINE":         false,
	}

	// 应用用户提供的过滤选项
	for k, v := range filterOption {
		option[k] = v
	}

	result := make([]string, 0)
	for symbol, quoteInfo := range t.quotesInfo {
		if t.filterSymbol(option, quoteInfo) {
			result = append(result, symbol)
		}
	}

	return result
}

// filterSymbol 过滤合约
func (t *TQSDK) filterSymbol(option map[string]interface{}, quote map[string]interface{}) bool {
	class, _ := quote["class"].(string)
	classFilter, _ := option[class].(bool)

	expired, _ := quote["expired"].(bool)
	includeExpired, _ := option["include_expired"].(bool)

	if !classFilter {
		return false
	}

	if !includeExpired && expired {
		return false
	}

	input, _ := option["input"].(string)
	symbolFilter, _ := option["symbol"].(bool)
	pinyinFilter, _ := option["pinyin"].(bool)

	if symbolFilter {
		if underlyingProduct, ok := quote["underlying_product"].(string); ok {
			parts := strings.Split(strings.ToLower(underlyingProduct), ".")
			if len(parts) >= 2 && (parts[0] == input || parts[1] == input) {
				return true
			}
		} else if productID, ok := quote["product_id"].(string); ok {
			if strings.ToLower(productID) == input {
				return true
			}
		} else if instrumentID, ok := quote["instrument_id"].(string); ok {
			if len(input) > 2 && strings.Contains(strings.ToLower(instrumentID), input) {
				return true
			}
		}
	}

	if pinyinFilter {
		if py, ok := quote["py"].(string); ok {
			pyArray := strings.Split(py, ",")
			for _, p := range pyArray {
				if strings.Contains(p, input) {
					return true
				}
			}
		}
	}

	return false
}

// Close 关闭 SDK
func (t *TQSDK) Close() error {
	t.cancel()

	if t.quotesWs != nil {
		t.quotesWs.Close()
	}

	t.tradeAccountsMu.Lock()
	for _, account := range t.tradeAccounts {
		if account.Ws != nil {
			account.Ws.Close()
		}
	}
	t.tradeAccountsMu.Unlock()

	return nil
}

// getLastID 辅助函数：从数据中获取 last_id
func (t *TQSDK) getLastID(data interface{}) int64 {
	if m, ok := data.(map[string]interface{}); ok {
		if lastID, ok := m["last_id"]; ok {
			switch v := lastID.(type) {
			case int64:
				return v
			case int:
				return int64(v)
			case float64:
				return int64(v)
			}
		}
	}
	return 0
}
