package tqsdk

import (
	"context"
	"fmt"
	"sync"

	"go.uber.org/zap"
)

// Client TQSDK 客户端
type Client struct {
	config ClientConfig
	logger *zap.Logger
	auth   *TqAuth

	// WebSocket 连接
	quotesWs *TqQuoteWebsocket

	// 数据管理
	dm         *DataManager
	quotesInfo map[string]map[string]interface{} // 合约信息

	// Quote 订阅
	quoteSubscription *QuoteSubscription
	quoteSubMu        sync.RWMutex

	// Series API
	seriesAPI *SeriesAPI

	// 交易会话
	tradeSessions   map[string]*TradeSession
	tradeSessionsMu sync.RWMutex

	// 控制
	ctx    context.Context
	cancel context.CancelFunc

	mu sync.RWMutex
}

// ClientConfig 客户端配置
type ClientConfig struct {
	// 认证信息
	Username string
	Password string

	// 服务器地址
	SymbolsServerURL string // 合约服务地址
	WsQuoteURL       string // 行情连接地址

	// 客户端信息
	ClientSystemInfo string // 客户端系统信息
	ClientAppID      string // 客户端应用ID

	// 日志配置
	LogConfig LogConfig

	// WebSocket 配置
	WsConfig WebSocketConfig

	// 数据管理配置
	DataConfig DataManagerConfig
}

// DefaultClientConfig 默认配置
func DefaultClientConfig(username, password string) ClientConfig {
	return ClientConfig{
		Username:         username,
		Password:         password,
		SymbolsServerURL: "https://openmd.shinnytech.com/t/md/symbols/latest.json",
		LogConfig: LogConfig{
			Level:       "info",
			OutputPath:  "stdout",
			Development: false,
		},
		WsConfig: DefaultWebSocketConfig(),
		DataConfig: DataManagerConfig{
			DefaultViewWidth:  10000,
			EnableAutoCleanup: true,
		},
	}
}

// NewClient 创建新的客户端
func NewClient(ctx context.Context, username, password string, opts ...ClientOption) (*Client, error) {
	config := DefaultClientConfig(username, password)

	// 应用选项
	for _, opt := range opts {
		opt(&config)
	}

	// 创建 logger
	logger, err := NewLogger(config.LogConfig)
	if err != nil {
		logger = NewDefaultLogger()
	}

	config.WsConfig.Logger = logger

	// 认证
	tqauth := NewTqAuth(username, password)
	if err := tqauth.Login(); err != nil {
		return nil, NewError("NewClient.Login", err)
	}

	// 创建上下文
	clientCtx, cancel := context.WithCancel(ctx)

	// 初始化数据
	initialData := map[string]interface{}{
		"klines": make(map[string]interface{}),
		"quotes": make(map[string]interface{}),
		"charts": make(map[string]interface{}),
		"ticks":  make(map[string]interface{}),
		"trade":  make(map[string]interface{}),
	}

	config.WsConfig.Headers = tqauth.BaseHeader()

	client := &Client{
		config:        config,
		logger:        logger,
		auth:          tqauth,
		dm:            NewDataManager(initialData),
		quotesInfo:    make(map[string]map[string]interface{}),
		tradeSessions: make(map[string]*TradeSession),
		ctx:           clientCtx,
		cancel:        cancel,
	}

	// 初始化 行情WebSocket
	if err := client.initMarketData(); err != nil {
		cancel()
		return nil, err
	}

	// 创建 SeriesAPI
	client.seriesAPI = NewSeriesAPI(client, client.dm, client.quotesWs)

	return client, nil
}

// ClientOption 客户端选项函数
type ClientOption func(*ClientConfig)

// WithLogLevel 设置日志级别
func WithLogLevel(level string) ClientOption {
	return func(c *ClientConfig) {
		c.LogConfig.Level = level
	}
}

// WithViewWidth 设置默认视图宽度
func WithViewWidth(width int) ClientOption {
	return func(c *ClientConfig) {
		c.DataConfig.DefaultViewWidth = width
	}
}

// WithClientInfo 设置客户端信息
func WithClientInfo(appID, systemInfo string) ClientOption {
	return func(c *ClientConfig) {
		c.ClientAppID = appID
		c.ClientSystemInfo = systemInfo
	}
}

// WithDevelopment 设置开发模式
func WithDevelopment(development bool) ClientOption {
	return func(c *ClientConfig) {
		c.LogConfig.Development = development
	}
}

// initMarketData 初始化行情连接
func (c *Client) initMarketData() error {
	// 下载合约信息
	go func() {
		data, err := FetchJSON(c.config.SymbolsServerURL)
		if err != nil {
			c.logger.Error("Failed to fetch symbols", zap.Error(err))
			return
		}

		if quotesMap, ok := data.(map[string]interface{}); ok {
			c.mu.Lock()
			for symbol, quoteData := range quotesMap {
				if quoteMap, ok := quoteData.(map[string]interface{}); ok {
					if class, ok := quoteMap["class"].(string); ok && class == "FUTURE_OPTION" {
						quoteMap["class"] = "OPTION"
					}
					c.quotesInfo[symbol] = quoteMap
				}
			}
			c.mu.Unlock()
		}

		c.logger.Info("Symbols loaded", zap.Int("count", len(c.quotesInfo)))
	}()

	// 获取行情服务器地址
	wsQuoteURL, err := c.auth.GetMdUrl(true, false)
	if err != nil {
		c.logger.Error("Failed to get md url", zap.Error(err))
		return NewError("initMarketData.GetMdUrl", err)
	}
	c.config.WsQuoteURL = wsQuoteURL

	// 创建行情 WebSocket
	c.quotesWs = NewTqQuoteWebsocket(c.config.WsQuoteURL, c.dm, c.config.WsConfig)

	// 初始化连接
	if err := c.quotesWs.Init(false); err != nil {
		return NewError("initMarketData.Init", err)
	}

	return nil
}

// Series 获取序列数据 API
func (c *Client) Series() *SeriesAPI {
	return c.seriesAPI
}

// SubscribeQuote 订阅 Quote（全局订阅）
func (c *Client) SubscribeQuote(ctx context.Context, symbols ...string) (*QuoteSubscription, error) {
	c.quoteSubMu.Lock()
	defer c.quoteSubMu.Unlock()

	// 如果已存在，添加合约
	if c.quoteSubscription != nil {
		return c.quoteSubscription, c.quoteSubscription.AddSymbols(symbols...)
	}

	// 创建新订阅
	sub, err := NewQuoteSubscription(ctx, c, symbols...)
	if err != nil {
		return nil, err
	}

	c.quoteSubscription = sub
	return sub, nil
}

// LoginTrade 登录交易账户，创建交易会话
func (c *Client) LoginTrade(ctx context.Context, broker, userID, password string) (*TradeSession, error) {
	c.tradeSessionsMu.Lock()
	defer c.tradeSessionsMu.Unlock()

	key := fmt.Sprintf("%s:%s", broker, userID)

	// 如果会话已存在，返回现有会话
	if session, exists := c.tradeSessions[key]; exists {
		return session, nil
	}

	// 创建新会话
	session, err := NewTradeSession(ctx, c, broker, userID, password)
	if err != nil {
		return nil, err
	}

	c.tradeSessions[key] = session
	return session, nil
}

// GetQuoteInfo 获取合约信息
func (c *Client) GetQuoteInfo(symbol string) (map[string]interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	info, exists := c.quotesInfo[symbol]
	return info, exists
}

// Close 关闭客户端
func (c *Client) Close() error {
	c.cancel()

	// 关闭 Quote 订阅
	c.quoteSubMu.Lock()
	if c.quoteSubscription != nil {
		c.quoteSubscription.Close()
	}
	c.quoteSubMu.Unlock()

	// 关闭行情 WebSocket
	if c.quotesWs != nil {
		c.quotesWs.Close()
	}

	// 关闭所有交易会话
	c.tradeSessionsMu.Lock()
	for _, session := range c.tradeSessions {
		session.Close()
	}
	c.tradeSessionsMu.Unlock()

	return nil
}

// Logger 获取logger
func (c *Client) Logger() *zap.Logger {
	return c.logger
}

// Context 获取上下文
func (c *Client) Context() context.Context {
	return c.ctx
}
