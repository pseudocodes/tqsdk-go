package shinny

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"go.uber.org/zap"
)

// QuoteSubscription Quote 订阅（全局）
type QuoteSubscription struct {
	client *Client
	ctx    context.Context
	cancel context.CancelFunc

	// 流式 Channel
	quoteCh chan *Quote

	// 回调函数
	onQuote func(*Quote)
	onError func(error)

	// 订阅的合约集合
	symbols map[string]bool
	mu      sync.RWMutex

	// 订阅状态
	running bool
	wg      sync.WaitGroup
}

// NewQuoteSubscription 创建 Quote 订阅
func NewQuoteSubscription(ctx context.Context, client *Client, symbols ...string) (*QuoteSubscription, error) {
	subCtx, cancel := context.WithCancel(ctx)

	qs := &QuoteSubscription{
		client:  client,
		ctx:     subCtx,
		cancel:  cancel,
		quoteCh: make(chan *Quote, 100),
		symbols: make(map[string]bool),
		running: true,
	}

	// 添加初始合约
	if err := qs.AddSymbols(symbols...); err != nil {
		cancel()
		return nil, err
	}

	// 启动监听
	qs.wg.Add(1)
	go qs.watchQuotes()

	return qs, nil
}

// AddSymbols 添加订阅合约
func (qs *QuoteSubscription) AddSymbols(symbols ...string) error {
	if len(symbols) == 0 {
		return nil
	}

	qs.mu.Lock()
	defer qs.mu.Unlock()

	// 添加到集合
	for _, symbol := range symbols {
		qs.symbols[symbol] = true
	}

	// 发送订阅请求
	insList := make([]string, 0, len(qs.symbols))
	for symbol := range qs.symbols {
		insList = append(insList, symbol)
	}

	if qs.client.quotesWs != nil {
		insListStr := strings.Join(insList, ",")
		if qs.client.logger != nil {
			qs.client.logger.Info("Sending quote subscription",
				zap.String("ins_list", insListStr),
				zap.Int("count", len(insList)))
		}

		qs.client.quotesWs.Send(map[string]interface{}{
			"aid":      "subscribe_quote",
			"ins_list": insListStr,
		})
	} else {
		if qs.client.logger != nil {
			qs.client.logger.Warn("quotesWs is nil, cannot send subscription")
		}
	}

	return nil
}

// RemoveSymbols 移除订阅合约
func (qs *QuoteSubscription) RemoveSymbols(symbols ...string) error {
	if len(symbols) == 0 {
		return nil
	}

	qs.mu.Lock()
	defer qs.mu.Unlock()

	// 从集合移除
	for _, symbol := range symbols {
		delete(qs.symbols, symbol)
	}

	// 重新发送订阅请求
	insList := make([]string, 0, len(qs.symbols))
	for symbol := range qs.symbols {
		insList = append(insList, symbol)
	}

	if qs.client.quotesWs != nil {
		qs.client.quotesWs.Send(map[string]interface{}{
			"aid":      "subscribe_quote",
			"ins_list": strings.Join(insList, ","),
		})
	}

	return nil
}

// QuoteChannel 获取 Quote 更新流（不区分合约）
func (qs *QuoteSubscription) QuoteChannel() <-chan *Quote {
	return qs.quoteCh
}

// OnQuote 注册回调（不区分合约，用户自行过滤）
func (qs *QuoteSubscription) OnQuote(handler func(*Quote)) {
	qs.mu.Lock()
	defer qs.mu.Unlock()
	qs.onQuote = handler
}

// OnError 注册错误回调
func (qs *QuoteSubscription) OnError(handler func(error)) {
	qs.mu.Lock()
	defer qs.mu.Unlock()
	qs.onError = handler
}

// watchQuotes 监听 Quote 更新
func (qs *QuoteSubscription) watchQuotes() {
	defer qs.wg.Done()

	if qs.client.logger != nil {
		qs.client.logger.Info("QuoteSubscription watchQuotes started")
	}

	callbackCount := 0

	// 注册数据更新回调
	qs.client.dm.OnData(func() {
		callbackCount++
		qs.client.logger.Debug("QuoteSubscription OnData callback triggered",
			zap.Int("call_count", callbackCount))

		qs.mu.RLock()
		symbols := make([]string, 0, len(qs.symbols))
		for symbol := range qs.symbols {
			symbols = append(symbols, symbol)
		}
		onQuote := qs.onQuote
		qs.mu.RUnlock()

		// 调试：打印 DataManager 中的顶层数据结构
		if callbackCount == 1 && qs.client.logger != nil {
			quotesData := qs.client.dm.GetByPath([]string{"quotes"})
			if quotesData != nil {
				if quotesMap, ok := quotesData.(map[string]interface{}); ok {
					keys := make([]string, 0, len(quotesMap))
					for k := range quotesMap {
						keys = append(keys, k)
					}
					qs.client.logger.Info("DataManager quotes keys",
						zap.Strings("keys", keys),
						zap.Int("count", len(keys)))
				}
			} else {
				qs.client.logger.Warn("DataManager quotes path is nil")
			}
		}

		if qs.client.logger != nil {
			qs.client.logger.Debug("Checking symbols for updates",
				zap.Int("count", len(symbols)),
				zap.Strings("symbols", symbols))
		}

		// 检查每个订阅的合约
		for _, symbol := range symbols {
			// 检查是否有更新
			isChanging := qs.client.dm.IsChanging([]string{"quotes", symbol})

			qs.client.logger.Debug("Symbol change check",
				zap.String("symbol", symbol),
				zap.Bool("isChanging", isChanging))

			if isChanging {
				quote := qs.getQuote(symbol)
				if quote != nil {

					qs.client.logger.Debug("Got quote data",
						zap.String("symbol", symbol),
						zap.Float64("last_price", quote.LastPrice))

					// 发送到 Channel（非阻塞）
					select {
					case qs.quoteCh <- quote:

						qs.client.logger.Debug("Quote sent to channel",
							zap.String("symbol", symbol))

					case <-qs.ctx.Done():
						return
					default:
						// Channel 满了，跳过

						qs.client.logger.Warn("Quote channel full, skipping update",
							zap.String("symbol", symbol))

					}

					// 调用回调
					if onQuote != nil {
						qs.client.logger.Debug("Calling quote callback",
							zap.String("symbol", symbol))
						go onQuote(quote)
					}
				} else {

					qs.client.logger.Debug("Quote data is nil",
						zap.String("symbol", symbol))

				}
			}
		}
	})

	if qs.client.logger != nil {
		qs.client.logger.Info("QuoteSubscription OnData callback registered, waiting for context")
	}

	// 等待上下文取消
	<-qs.ctx.Done()

	if qs.client.logger != nil {
		qs.client.logger.Info("QuoteSubscription watchQuotes stopped")
	}
}

// getQuote 获取 Quote 对象
func (qs *QuoteSubscription) getQuote(symbol string) *Quote {
	data := qs.client.dm.GetByPath([]string{"quotes", symbol})
	if data == nil {
		if qs.client.logger != nil {
			qs.client.logger.Warn("GetByPath returned nil",
				zap.String("symbol", symbol),
				zap.Strings("path", []string{"quotes", symbol}))
		}
		return nil
	}

	if qs.client.logger != nil {
		qs.client.logger.Debug("GetByPath returned data",
			zap.String("symbol", symbol),
			zap.Any("data_type", fmt.Sprintf("%T", data)))
	}

	var quote Quote
	if err := qs.client.dm.ConvertToStruct(data, &quote); err != nil {
		if qs.client.logger != nil {
			qs.client.logger.Error("ConvertToStruct failed",
				zap.String("symbol", symbol),
				zap.Error(err),
				zap.Any("data", data))
		}
		return nil
	}

	return &quote
}

// Close 关闭订阅
func (qs *QuoteSubscription) Close() error {
	qs.mu.Lock()
	if !qs.running {
		qs.mu.Unlock()
		return nil
	}
	qs.running = false
	qs.mu.Unlock()

	qs.cancel()
	qs.wg.Wait()
	close(qs.quoteCh)

	return nil
}
