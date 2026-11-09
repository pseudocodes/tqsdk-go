// Package tqapi 提供显式 WaitUpdate 的策略编程方式：状态只在 WaitUpdate 时推进，
// 每次返回一个可观察提交（Pulse），可按对象与字段查询本轮净变化。
// tqapi 直接组合 core，不依赖 market/trade 门面。
package tqapi

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/auth"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/backtest"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/sim"
)

// Config 配置 API。Market/Trading 为 nil 时不创建对应组件；其中 Auth/Logger 为空时继承顶层配置。
type Config struct {
	Auth    auth.Provider
	Logger  *zap.Logger
	Market  *core.MarketConfig
	Trading *core.TradingConfig

	// 账户声明，Account 与 Accounts 二选一：网络账户用 core.AccountConfig，模拟账户用 sim.New。
	// 声明了账户时自动创建 Trading 组件；New 返回前全部账户已启动（不保证 Ready）。
	Account  core.AccountSpec
	Accounts []core.AccountSpec
	// BeforeAccountStart 在任何账户启动前、写锁外对每个账户调用，可挂载 CommandGuard 或订阅早期事件；
	// 不得报撤单或驱动 Runtime。
	BeforeAccountStart func(*core.AccountHandle) error

	// Backtest 非空时进入回测：行情由回测驱动按虚拟时间提供（须同时配置 Market），账户须为 sim 账户，
	// 任务调度使用虚拟时钟。为 nil 时 sim 账户由实时行情驱动撮合（paper trade，须配置 Market）。
	Backtest *backtest.Config
}

// API 拥有一个手动推进的 Runtime。
type API struct {
	runtime *core.Runtime
	md      *core.Market
	td      *core.Trading

	Market   *MarketAPI
	Trade    *TradeAPI
	Backtest *backtest.Driver // 回测时非 nil：Status / Result
	Paper    *sim.Paper       // 实时模拟交易时非 nil：Reports

	log       *zap.Logger
	lastPulse Pulse
	accounts  []*core.AccountHandle
	virtual   bool // 回测：调度器使用虚拟时钟

	// 任务调度。tasksOn/driving 可被其他 goroutine 读取；其余字段只在 owner 访问。
	tasksOn atomic.Bool // 首次 RegisterTask 后为 true，此后 WaitUpdate 走调度路径
	driving atomic.Bool // 驱动排他标记，覆盖任务执行与 Step
	closing bool
	tasks   []*TaskHandle
	cursor  int
	batch   []int
}

// New 创建 API 与所需组件；不等待任何业务数据（初始基线由 WaitUpdate 交付）。
// 内部顺序：校验配置 → 创建组件 → 登记全部账户 → BeforeAccountStart → 启动账户 → 装入回测驱动。
// 任一步失败时关闭本次创建的全部资源。
func New(ctx context.Context, cfg Config) (*API, error) {
	specs := cfg.Accounts
	switch {
	case cfg.Account != nil && len(cfg.Accounts) > 0:
		return nil, errors.New("tqapi: Account and Accounts are mutually exclusive")
	case cfg.Account != nil:
		specs = []core.AccountSpec{cfg.Account}
	}
	switch {
	case cfg.Market == nil && cfg.Trading == nil && len(specs) == 0:
		return nil, errors.New("tqapi: neither Market nor Trading configured")
	case cfg.Backtest != nil && cfg.Market == nil:
		return nil, errors.New("tqapi: Backtest requires Market")
	}
	a := &API{runtime: core.NewRuntime(), log: cfg.Logger}
	if a.log == nil {
		a.log = zap.NewNop()
	}
	if err := a.build(ctx, cfg, specs); err != nil {
		a.Close()
		return nil, err
	}
	return a, nil
}

func (a *API) build(ctx context.Context, cfg Config, specs []core.AccountSpec) error {
	if cfg.Market != nil {
		mc := *cfg.Market
		if mc.Auth == nil {
			mc.Auth = cfg.Auth
		}
		if mc.Logger == nil {
			mc.Logger = cfg.Logger
		}
		var md *core.Market
		var err error
		if cfg.Backtest != nil {
			md, err = core.NewLocalMarket(a.runtime, mc)
		} else {
			md, err = core.NewMarket(ctx, a.runtime, mc)
		}
		if err != nil {
			return err
		}
		a.md = md
		a.Market = &MarketAPI{market: md}
	}
	if cfg.Trading != nil || len(specs) > 0 {
		var tc core.TradingConfig
		if cfg.Trading != nil {
			tc = *cfg.Trading
		}
		if tc.Auth == nil {
			tc.Auth = cfg.Auth
		}
		if tc.Logger == nil {
			tc.Logger = cfg.Logger
		}
		td, err := core.NewTrading(ctx, a.runtime, tc)
		if err != nil {
			return err
		}
		a.td = td
		a.Trade = &TradeAPI{trading: td}
	}

	var simAccounts []backtest.Account
	for _, spec := range specs {
		s, isSim := spec.(*sim.Account)
		switch {
		case spec == nil:
			return errors.New("tqapi: nil account spec")
		case cfg.Backtest != nil && !isSim:
			return errors.New("tqapi: backtest only accepts sim accounts")
		case isSim && a.md == nil:
			return errors.New("tqapi: sim account requires Market")
		}
		h, err := spec.Prepare(a.td)
		if err != nil {
			return err
		}
		a.accounts = append(a.accounts, h)
		if isSim {
			simAccounts = append(simAccounts, backtest.Account{Handle: h, Config: s.Config()})
		}
	}
	if cfg.BeforeAccountStart != nil {
		for _, h := range a.accounts {
			if err := cfg.BeforeAccountStart(h); err != nil {
				return err
			}
		}
	}
	for _, h := range a.accounts {
		if err := h.Start(ctx); err != nil {
			return err
		}
	}
	if cfg.Backtest != nil {
		drv, err := backtest.NewDriver(*cfg.Backtest, a.md, simAccounts)
		if err != nil {
			return err
		}
		if err := a.runtime.SetDriver(drv); err != nil {
			return err
		}
		a.Backtest, a.virtual = drv, true
	} else if len(simAccounts) > 0 {
		paper := sim.NewPaper(a.md)
		for _, s := range simAccounts {
			paper.Add(s.Handle, s.Config)
		}
		if err := a.runtime.SetBackend(a.md, paper); err != nil {
			return err
		}
		a.Paper = paper
	}
	return nil
}

// Close 快速释放全部资源。启用任务后先强制分离仍活动的任务（调用 OnDetach），
// 有任务被中止时返回 ErrTaskDetached；此时须由 owner 在驱动调用之外调用。
func (a *API) Close() error {
	if !a.tasksOn.Load() {
		return errors.Join(a.runtime.Close(), a.closeBacktest())
	}
	err := a.closeTasks()
	if errors.Is(err, ErrDispatchBusy) {
		return err
	}
	return errors.Join(err, a.runtime.Close(), a.closeBacktest())
}

func (a *API) closeBacktest() error {
	if a.Backtest == nil {
		return nil
	}
	return a.Backtest.Close()
}

// Account 返回唯一的账户；没有账户或有多个账户时返回错误。
func (a *API) Account() (*core.AccountHandle, error) {
	switch len(a.accounts) {
	case 1:
		return a.accounts[0], nil
	case 0:
		return nil, errors.New("tqapi: no account configured")
	}
	return nil, fmt.Errorf("tqapi: %d accounts configured, use Trade.Account(id)", len(a.accounts))
}

// Now 返回业务时钟：回测为已提交的虚拟时间，否则为本机时间。
func (a *API) Now() time.Time { return a.runtime.Now() }

// Runtime 返回底层 Runtime（手动驱动）。启用任务后不要直接调用其 Step/Run/Close。
func (a *API) Runtime() *core.Runtime { return a.runtime }

// WaitUpdate 推进到下一个可观察提交。入口先清空 LastPulse；超时或失败返回空 Pulse。
//
// 启用任务后，同一调用还会执行就绪任务，并可能只因任务反馈或预算让出返回（HasData=false）；
// 驱动错误可与任务反馈同时返回，调用者应先处理 Pulse 再处理 error。
func (a *API) WaitUpdate(ctx context.Context) (Pulse, error) {
	if !a.tasksOn.Load() {
		a.lastPulse = Pulse{}
		commit, err := a.runtime.Step(ctx)
		if err != nil {
			return Pulse{}, err
		}
		a.lastPulse = a.dataPulse(commit)
		return a.lastPulse, nil
	}
	if !a.driving.CompareAndSwap(false, true) {
		return Pulse{}, ErrDispatchBusy
	}
	defer a.driving.Store(false)
	a.lastPulse = Pulse{}
	p, err := a.pump(ctx)
	a.lastPulse = p
	return p, err
}

// LastPulse 返回最近一次 WaitUpdate 的结果。
func (a *API) LastPulse() Pulse { return a.lastPulse }

// IsChanging 查询最近一次 WaitUpdate 的变化，等价于 LastPulse().IsChanging。
func (a *API) IsChanging(target core.ChangeTarget, fields ...string) bool {
	return a.lastPulse.IsChanging(target, fields...)
}

// Pulse 是一次 WaitUpdate 返回的独立结果；保存后不受后续推进影响。
// 对象引用不冻结当前值，历史值需在当轮复制 Snapshot。
// 纯任务反馈或预算让出的 Pulse 不带 Commit，Stamp 为零。
type Pulse struct {
	Stamp  core.Stamp
	commit core.Commit
	md     *core.Market
	td     *core.Trading
	tasks  *taskFeedback
}

// HasData 报告本轮是否带有一个 core 数据提交。
func (p Pulse) HasData() bool { return p.Stamp.Epoch != 0 }

// Time 返回本轮提交的业务时间（回测为虚拟时间）；不带 Commit 的 Pulse 返回零值，此时以 API.Now 为准。
func (p Pulse) Time() time.Time { return p.commit.Time() }

// TasksChanged 报告本轮是否至少有一个任务报告 Changed、首次错误或结束调度。
func (p Pulse) TasksChanged() bool { return p.tasks != nil && p.tasks.changed }

// TaskError 返回本轮首次报告的任务错误及新发生的强制分离错误。
func (p Pulse) TaskError() error {
	if p.tasks == nil {
		return nil
	}
	return p.tasks.err
}

// IsChanging 报告目标在本轮是否有业务净变化；fields 为 snake_case 协议名，多个为 OR。
func (p Pulse) IsChanging(target core.ChangeTarget, fields ...string) bool {
	return core.IsChanging(p.commit, target, fields...)
}

func (p Pulse) market() core.MarketChanges {
	if p.md == nil || p.commit == (core.Commit{}) {
		return core.MarketChanges{}
	}
	return p.md.Changes(p.commit)
}

func (p Pulse) trading() core.TradingChanges {
	if p.td == nil || p.commit == (core.Commit{}) {
		return core.TradingChanges{}
	}
	return p.td.Changes(p.commit)
}

func (p Pulse) Quote(q *core.QuoteRef) core.QuoteChange          { return p.market().Quote(q) }
func (p Pulse) Klines(w *core.Klines) *core.KlineChange          { return p.market().Klines(w) }
func (p Pulse) Ticks(w *core.Ticks) *core.TickChange             { return p.market().Ticks(w) }
func (p Pulse) QueryIDs() []string                               { return p.market().QueryIDs() }
func (p Pulse) Account(r *core.AccountRef) core.AccountChange    { return p.trading().Account(r) }
func (p Pulse) Position(r *core.PositionRef) core.PositionChange { return p.trading().Position(r) }
func (p Pulse) Order(r *core.OrderRef) core.OrderChange          { return p.trading().Order(r) }
func (p Pulse) Trades(r *core.TradesRef) core.TradesChange       { return p.trading().Trades(r) }
func (p Pulse) RemoteRiskRequestIDs() []string                   { return p.trading().RemoteRiskRequestIDs() }

// AlignedKlines 返回多合约窗口的变化。
func (p Pulse) AlignedKlines(w *core.AlignedKlines) *core.AlignedKlineChange {
	return p.market().AlignedKlines(w)
}

// RemoteRiskRule 返回账户某交易所远端风控规则的变化。
func (p Pulse) RemoteRiskRule(a *core.AccountHandle, exchange string) core.RemoteRiskRuleChange {
	return p.trading().RemoteRiskRule(a, exchange)
}

// RemoteRiskData 返回账户某合约远端风控统计的变化。
func (p Pulse) RemoteRiskData(a *core.AccountHandle, symbol string) core.RemoteRiskDataChange {
	return p.trading().RemoteRiskData(a, symbol)
}

// Notifies 返回本轮收到的账户通知。
func (p Pulse) Notifies(a *core.AccountHandle) []model.Notify { return p.trading().Notifies(a) }

// AccountStateChanged 报告账户状态或会话元信息在本轮是否改变。
func (p Pulse) AccountStateChanged(a *core.AccountHandle) bool { return p.trading().StateChanged(a) }

// Errors 返回本轮的路由错误。
func (p Pulse) Errors() []core.RouteError {
	return append(p.market().Errors(), p.trading().Errors()...)
}

// MarketAPI 是策略侧行情入口；句柄立即以 Pending 返回，不隐藏等待。
type MarketAPI struct{ market *core.Market }

// Core 返回底层行情组件。
func (m *MarketAPI) Core() *core.Market { return m.market }

// HasFeature 查询当前授权的功能权限。
func (m *MarketAPI) HasFeature(feature string) bool { return m.market.HasFeature(feature) }

func (m *MarketAPI) Quote(ctx context.Context, symbol string) (*core.QuoteRef, error) {
	return m.market.QuoteRef(ctx, symbol)
}

func (m *MarketAPI) Klines(ctx context.Context, r core.KlineRequest) (*core.Klines, error) {
	return m.market.Klines(ctx, r)
}

func (m *MarketAPI) Ticks(ctx context.Context, r core.TickRequest) (*core.Ticks, error) {
	return m.market.Ticks(ctx, r)
}

func (m *MarketAPI) AlignedKlines(ctx context.Context, r core.AlignedKlineRequest) (*core.AlignedKlines, error) {
	return m.market.AlignedKlines(ctx, r)
}

// RequestQuotes 等请求登记后立即返回；由 WaitUpdate 推进到 Done。
func (m *MarketAPI) RequestQuotes(ctx context.Context, f core.QuoteFilter) (*core.Request[[]string], error) {
	return m.market.RequestQuotes(ctx, f)
}

func (m *MarketAPI) RequestSymbolInfo(ctx context.Context, symbols []string) (*core.Request[[]model.Instrument], error) {
	return m.market.RequestSymbolInfo(ctx, symbols)
}

func (m *MarketAPI) RequestOptions(ctx context.Context, q core.OptionQuery) (*core.Request[[]string], error) {
	return m.market.RequestOptions(ctx, q)
}

func (m *MarketAPI) RequestGraphQL(ctx context.Context, query string, variables map[string]any) (*core.Request[core.GraphQLResult], error) {
	return m.market.RequestGraphQL(ctx, query, variables)
}

func (m *MarketAPI) RequestKlinesPage(ctx context.Context, r core.KlineRequest) (*core.Request[core.KlineFrame], error) {
	return m.market.RequestKlinesPage(ctx, r)
}

func (m *MarketAPI) RequestTicksPage(ctx context.Context, r core.TickRequest) (*core.Request[core.TickFrame], error) {
	return m.market.RequestTicksPage(ctx, r)
}

func (m *MarketAPI) RequestAlignedKlinesPage(ctx context.Context, r core.AlignedKlineRequest) (*core.Request[core.AlignedKlineFrame], error) {
	return m.market.RequestAlignedKlinesPage(ctx, r)
}

// TradeAPI 是策略侧交易入口，返回 core.AccountHandle。
type TradeAPI struct{ trading *core.Trading }

// Account 按本地账户 ID 查找账户。
func (t *TradeAPI) Account(id string) (*core.AccountHandle, error) {
	if a, ok := t.trading.Account(id); ok {
		return a, nil
	}
	return nil, fmt.Errorf("tqapi: account %q not found", id)
}

// Core 返回底层交易组件。
func (t *TradeAPI) Core() *core.Trading { return t.trading }

// PrepareAccount 登记账户；可在 Start 前 SetCommandGuard。
func (t *TradeAPI) PrepareAccount(cfg core.AccountConfig) (*core.AccountHandle, error) {
	return t.trading.PrepareAccount(cfg)
}

// OpenAccount 登记并启动账户（接入并发送登录），不等待同步完成。
func (t *TradeAPI) OpenAccount(ctx context.Context, cfg core.AccountConfig) (*core.AccountHandle, error) {
	return t.trading.OpenAccount(ctx, cfg)
}
