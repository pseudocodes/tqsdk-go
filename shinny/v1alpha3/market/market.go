// Package market 是独立行情客户端：后台自动推进，订阅通过 channel 交付，查询阻塞等待结果。
package market

import (
	"context"
	"sync"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

type (
	Config              = core.MarketConfig
	KlineRequest        = core.KlineRequest
	TickRequest         = core.TickRequest
	AlignedKlineRequest = core.AlignedKlineRequest
	WindowPosition      = core.WindowPosition
	QuoteSnapshot       = core.QuoteSnapshot
	QuoteEvent          = core.QuoteEvent
	QuoteSub            = core.QuoteSub
	KlineEvent          = core.KlineEvent
	KlineFrame          = core.KlineFrame
	KlineSub            = core.KlineSub
	TickEvent           = core.TickEvent
	TickFrame           = core.TickFrame
	TickSub             = core.TickSub
	AlignedKlineEvent   = core.AlignedKlineEvent
	AlignedKlineFrame   = core.AlignedKlineFrame
	AlignedKlineSub     = core.AlignedKlineSub
	SeriesOption        = core.SeriesOption
	QuoteFilter         = core.QuoteFilter
	OptionQuery         = core.OptionQuery
	GraphQLResult       = core.GraphQLResult
)

// WithFrame 让每个 Ready 序列事件附带完整窗口副本，读取方式为 e.Frame.Rows。
// 这是默认行为；e.Rows 仍保留增量语义；非 Ready 事件的 e.Frame 为 nil。
func WithFrame() SeriesOption { return core.WithFrame() }

// WithoutFrame 关闭完整窗口副本，事件只携带增量 e.Rows。
func WithoutFrame() SeriesOption { return core.WithoutFrame() }

// Market 拥有自己的 Runtime，并在后台 Run。
type Market struct {
	rt     *core.Runtime
	md     *core.Market
	cancel context.CancelFunc
	done   chan struct{}

	mu  sync.Mutex
	err error
}

// Dial 连接行情服务并开始后台推进。
func Dial(ctx context.Context, cfg Config) (*Market, error) {
	rt := core.NewRuntime()
	md, err := core.NewMarket(ctx, rt, cfg)
	if err != nil {
		rt.Close()
		return nil, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	m := &Market{rt: rt, md: md, cancel: cancel, done: make(chan struct{})}
	go func() {
		err := rt.Run(runCtx)
		m.mu.Lock()
		m.err = err
		m.mu.Unlock()
		close(m.done)
	}()
	return m, nil
}

// Close 停止推进并释放全部资源。
func (m *Market) Close() error {
	m.cancel()
	err := m.rt.Close()
	<-m.done
	return err
}

// Done 在后台推进结束后关闭。
func (m *Market) Done() <-chan struct{} { return m.done }

// Err 返回后台推进结束的原因。
func (m *Market) Err() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

// Core 返回底层行情组件，供高级组合使用。
func (m *Market) Core() *core.Market { return m.md }

// HasFeature 查询当前授权的功能权限。
func (m *Market) HasFeature(feature string) bool { return m.md.HasFeature(feature) }

// LookupQuote 读取本地已提交的报价，不发请求。
func (m *Market) LookupQuote(symbol string) (QuoteSnapshot, bool) { return m.md.LookupQuote(symbol) }

// SubscribeQuotes 订阅报价。
func (m *Market) SubscribeQuotes(ctx context.Context, symbols ...string) (*QuoteSub, error) {
	return m.md.SubscribeQuotes(ctx, symbols...)
}

// SubscribeKlines 订阅 K 线窗口。
func (m *Market) SubscribeKlines(ctx context.Context, r KlineRequest, opts ...SeriesOption) (*KlineSub, error) {
	return m.md.SubscribeKlines(ctx, r, opts...)
}

// SubscribeTicks 订阅 Tick 窗口。
func (m *Market) SubscribeTicks(ctx context.Context, r TickRequest, opts ...SeriesOption) (*TickSub, error) {
	return m.md.SubscribeTicks(ctx, r, opts...)
}

// SubscribeAlignedKlines 订阅多合约对齐 K 线。
func (m *Market) SubscribeAlignedKlines(ctx context.Context, r AlignedKlineRequest, opts ...SeriesOption) (*AlignedKlineSub, error) {
	return m.md.SubscribeAlignedKlines(ctx, r, opts...)
}

// QueryQuotes 查询合约代码列表。
func (m *Market) QueryQuotes(ctx context.Context, f QuoteFilter) ([]string, error) {
	req, err := m.md.RequestQuotes(ctx, f)
	if err != nil {
		return nil, err
	}
	return req.Wait(ctx)
}

// QuerySymbolInfo 查询合约资料。
func (m *Market) QuerySymbolInfo(ctx context.Context, symbols []string) ([]model.Instrument, error) {
	req, err := m.md.RequestSymbolInfo(ctx, symbols)
	if err != nil {
		return nil, err
	}
	return req.Wait(ctx)
}

// QueryOptions 查询期权代码。
func (m *Market) QueryOptions(ctx context.Context, q OptionQuery) ([]string, error) {
	req, err := m.md.RequestOptions(ctx, q)
	if err != nil {
		return nil, err
	}
	return req.Wait(ctx)
}

// QueryGraphQL 发送任意合约服务 GraphQL 查询。
func (m *Market) QueryGraphQL(ctx context.Context, query string, variables map[string]any) (GraphQLResult, error) {
	req, err := m.md.RequestGraphQL(ctx, query, variables)
	if err != nil {
		return GraphQLResult{}, err
	}
	return req.Wait(ctx)
}

// QueryKlinesPage 查询一页 K 线历史。
func (m *Market) QueryKlinesPage(ctx context.Context, r KlineRequest) (KlineFrame, error) {
	req, err := m.md.RequestKlinesPage(ctx, r)
	if err != nil {
		return KlineFrame{}, err
	}
	return req.Wait(ctx)
}

// QueryTicksPage 查询一页 Tick 历史。
func (m *Market) QueryTicksPage(ctx context.Context, r TickRequest) (TickFrame, error) {
	req, err := m.md.RequestTicksPage(ctx, r)
	if err != nil {
		return TickFrame{}, err
	}
	return req.Wait(ctx)
}

// QueryAlignedKlinesPage 查询一页多合约对齐 K 线历史（PrimarySlots）。
func (m *Market) QueryAlignedKlinesPage(ctx context.Context, r AlignedKlineRequest) (AlignedKlineFrame, error) {
	req, err := m.md.RequestAlignedKlinesPage(ctx, r)
	if err != nil {
		return AlignedKlineFrame{}, err
	}
	return req.Wait(ctx)
}
