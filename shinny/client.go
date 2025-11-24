package shinny

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Client TQSDK 客户端
type Client struct {
	config ClientConfig
	logger *zap.Logger
	Auth   Authenticator // 认证器接口，对外可见

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

// SymbolsCacheStrategy 合约信息缓存策略
type SymbolsCacheStrategy int

const (
	// CacheStrategyAlwaysNetwork 总是从网络获取
	CacheStrategyAlwaysNetwork SymbolsCacheStrategy = iota
	// CacheStrategyPreferLocal 优先使用本地缓存
	CacheStrategyPreferLocal
	// CacheStrategyAutoRefresh 如果本地缓存超过指定时间则自动刷新
	CacheStrategyAutoRefresh
)

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

	// 合约缓存配置
	SymbolsCacheDir      string               // 缓存目录，默认 $HOME/.tqsdk
	SymbolsCacheStrategy SymbolsCacheStrategy // 缓存策略
	SymbolsCacheMaxAge   int64                // 缓存最大有效期（秒），默认 86400（1天）

	// 日志配置
	LogConfig LogConfig

	// WebSocket 配置
	WsConfig WebSocketConfig

	// 数据管理配置
	DataConfig DataManagerConfig
}

// DefaultClientConfig 默认配置
func DefaultClientConfig(username, password string) ClientConfig {
	homeDir, _ := os.UserHomeDir()
	cacheDir := filepath.Join(homeDir, ".tqsdk")

	return ClientConfig{
		Username:             username,
		Password:             password,
		SymbolsServerURL:     "https://openmd.shinnytech.com/t/md/symbols/latest.json",
		SymbolsCacheDir:      cacheDir,
		SymbolsCacheStrategy: CacheStrategyAutoRefresh,
		SymbolsCacheMaxAge:   86400, // 1天
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
		Auth:          tqauth,
		dm:            NewDataManager(initialData),
		quotesInfo:    make(map[string]map[string]interface{}),
		tradeSessions: make(map[string]*TradeSession),
		ctx:           clientCtx,
		cancel:        cancel,
	}

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

// WithSymbolsCacheDir 设置合约信息缓存目录
func WithSymbolsCacheDir(dir string) ClientOption {
	return func(c *ClientConfig) {
		c.SymbolsCacheDir = dir
	}
}

// WithSymbolsCacheStrategy 设置合约信息缓存策略
func WithSymbolsCacheStrategy(strategy SymbolsCacheStrategy) ClientOption {
	return func(c *ClientConfig) {
		c.SymbolsCacheStrategy = strategy
	}
}

// WithSymbolsCacheMaxAge 设置合约信息缓存最大有效期（秒）
func WithSymbolsCacheMaxAge(maxAge int64) ClientOption {
	return func(c *ClientConfig) {
		c.SymbolsCacheMaxAge = maxAge
	}
}

// InitMarket 初始化行情功能（WebSocket 和 SeriesAPI）
// 此方法是可选的，只有需要使用行情功能时才调用
func (c *Client) InitMarket() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 如果已经初始化，直接返回
	if c.quotesWs != nil {
		c.logger.Warn("Market already initialized")
		return nil
	}

	// 加载合约信息
	go c.loadSymbols()

	// 获取行情服务器地址
	wsQuoteURL, err := c.Auth.GetMdUrl(true, false)
	if err != nil {
		c.logger.Error("Failed to get md url", zap.Error(err))
		return NewError("InitMarket.GetMdUrl", err)
	}
	c.config.WsQuoteURL = wsQuoteURL

	// 创建行情 WebSocket
	c.quotesWs = NewTqQuoteWebsocket(c.config.WsQuoteURL, c.dm, c.config.WsConfig)

	// 初始化连接
	if err := c.quotesWs.Init(false); err != nil {
		return NewError("InitMarket.Init", err)
	}

	// 创建 SeriesAPI
	c.seriesAPI = NewSeriesAPI(c, c.dm, c.quotesWs)

	c.logger.Info("Market initialized successfully")
	return nil
}

// Series 获取序列数据 API
// 注意：使用前需要先调用 InitMarket() 初始化行情功能
func (c *Client) Series() *SeriesAPI {
	if c.seriesAPI == nil {
		c.logger.Warn("SeriesAPI not initialized, call InitMarket() first")
	}
	return c.seriesAPI
}

// SubscribeQuote 订阅 Quote（全局订阅）
// 注意：使用前需要先调用 InitMarket() 初始化行情功能
func (c *Client) SubscribeQuote(ctx context.Context, symbols ...string) (*QuoteSubscription, error) {
	if c.quotesWs == nil {
		return nil, NewError("SubscribeQuote", fmt.Errorf("market not initialized, call InitMarket() first"))
	}
	if c.Auth.HasMdGrants(symbols...) != nil {
		return nil, NewError("SubscribeQuote", ErrPermissionDenied)
	}

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

// loadSymbols 加载合约信息
func (c *Client) loadSymbols() {
	var data interface{}
	var err error
	var source string

	switch c.config.SymbolsCacheStrategy {
	case CacheStrategyAlwaysNetwork:
		// 总是从网络获取
		data, err = c.fetchSymbolsFromNetwork()
		source = "network"
		if err == nil && data != nil {
			// 保存到缓存
			c.saveSymbolsCache(data)
		}

	case CacheStrategyPreferLocal:
		// 优先使用本地缓存
		data, err = c.loadSymbolsFromCache()
		if err != nil || data == nil {
			// 缓存不存在或加载失败，从网络获取
			c.logger.Info("Local cache not available, fetching from network")
			data, err = c.fetchSymbolsFromNetwork()
			source = "network"
			if err == nil && data != nil {
				c.saveSymbolsCache(data)
			}
		} else {
			source = "cache"
		}

	case CacheStrategyAutoRefresh:
		// 检查缓存是否过期
		needRefresh := c.isCacheExpired()
		if needRefresh {
			// 缓存过期，从网络获取
			c.logger.Info("Cache expired, fetching from network")
			data, err = c.fetchSymbolsFromNetwork()
			source = "network"
			if err == nil && data != nil {
				c.saveSymbolsCache(data)
			}
		} else {
			// 使用缓存
			data, err = c.loadSymbolsFromCache()
			if err != nil || data == nil {
				// 缓存加载失败，从网络获取
				c.logger.Info("Failed to load cache, fetching from network")
				data, err = c.fetchSymbolsFromNetwork()
				source = "network"
				if err == nil && data != nil {
					c.saveSymbolsCache(data)
				}
			} else {
				source = "cache"
			}
		}
	}

	if err != nil {
		c.logger.Error("Failed to load symbols", zap.Error(err))
		return
	}

	// 处理合约数据
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

	c.logger.Info("Symbols loaded",
		zap.String("source", source),
		zap.Int("count", len(c.quotesInfo)))
}

// fetchSymbolsFromNetwork 从网络获取合约信息
func (c *Client) fetchSymbolsFromNetwork() (interface{}, error) {
	data, err := FetchJSON(c.config.SymbolsServerURL)
	if err != nil {
		return nil, fmt.Errorf("fetch from network: %w", err)
	}
	return data, nil
}

// loadSymbolsFromCache 从本地缓存加载合约信息
func (c *Client) loadSymbolsFromCache() (interface{}, error) {
	cachePath := c.getSymbolsCachePath()

	// 检查文件是否存在
	if _, err := os.Stat(cachePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("cache file not exists: %s", cachePath)
	}

	// 读取缓存文件
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, fmt.Errorf("read cache file: %w", err)
	}

	// 解析 JSON
	var result interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("unmarshal cache: %w", err)
	}

	return result, nil
}

// saveSymbolsCache 保存合约信息到本地缓存
func (c *Client) saveSymbolsCache(data interface{}) error {
	cachePath := c.getSymbolsCachePath()

	// 确保缓存目录存在
	cacheDir := filepath.Dir(cachePath)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		c.logger.Error("Failed to create cache directory",
			zap.String("dir", cacheDir),
			zap.Error(err))
		return fmt.Errorf("create cache dir: %w", err)
	}

	// 序列化为 JSON
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		c.logger.Error("Failed to marshal symbols", zap.Error(err))
		return fmt.Errorf("marshal json: %w", err)
	}

	// 写入文件
	if err := os.WriteFile(cachePath, jsonData, 0644); err != nil {
		c.logger.Error("Failed to write cache file",
			zap.String("path", cachePath),
			zap.Error(err))
		return fmt.Errorf("write cache file: %w", err)
	}

	c.logger.Debug("Symbols cache saved", zap.String("path", cachePath))
	return nil
}

// isCacheExpired 检查缓存是否过期
func (c *Client) isCacheExpired() bool {
	cachePath := c.getSymbolsCachePath()

	// 获取文件信息
	fileInfo, err := os.Stat(cachePath)
	if err != nil {
		// 文件不存在视为过期
		return true
	}

	// 检查文件修改时间
	modTime := fileInfo.ModTime()
	elapsed := time.Since(modTime).Seconds()

	return elapsed > float64(c.config.SymbolsCacheMaxAge)
}

// getSymbolsCachePath 获取缓存文件路径
func (c *Client) getSymbolsCachePath() string {
	return filepath.Join(c.config.SymbolsCacheDir, "latest.json")
}

// Context 获取上下文
func (c *Client) Context() context.Context {
	return c.ctx
}
