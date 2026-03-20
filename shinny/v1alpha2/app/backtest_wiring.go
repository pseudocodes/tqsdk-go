package app

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest/data"
	btmarket "github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest/market"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest/report"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest/runtime"
	bttrade "github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest/trade"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/domain"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/store"
)

type BacktestWiring struct {
	Auth       api.AuthService
	Market     api.MarketService
	Trade      api.TradeService
	Runtime    *runtime.EventKernel
	Provider   data.DataProvider
	Collector  *report.Collector
	Downloader api.MarketService // live market service used for lazy data loading (may be nil)
}

type backtestBuildOptions struct {
	provider       data.DataProvider
	downloader     api.MarketService
	autoRun        *bool
	initialBalance *float64
	loadTimeout    time.Duration
	skipTicks      bool
	onProgress     func(stage string)
}

type BacktestOption interface {
	applyBacktest(*backtestBuildOptions)
}

type backtestOptionFunc func(*backtestBuildOptions)

func (f backtestOptionFunc) applyBacktest(o *backtestBuildOptions) {
	f(o)
}

func WithBacktestDataProvider(provider data.DataProvider) BacktestOption {
	return backtestOptionFunc(func(o *backtestBuildOptions) {
		o.provider = provider
	})
}

// WithBacktestDownloader sets the market service used only for historical data download.
func WithBacktestDownloader(market api.MarketService) BacktestOption {
	return backtestOptionFunc(func(o *backtestBuildOptions) {
		o.downloader = market
	})
}

func WithBacktestAutoRun(v bool) BacktestOption {
	return backtestOptionFunc(func(o *backtestBuildOptions) {
		o.autoRun = &v
	})
}

func WithBacktestInitialBalance(v float64) BacktestOption {
	return backtestOptionFunc(func(o *backtestBuildOptions) {
		o.initialBalance = &v
	})
}

func WithBacktestLoadTimeout(timeout time.Duration) BacktestOption {
	return backtestOptionFunc(func(o *backtestBuildOptions) {
		if timeout > 0 {
			o.loadTimeout = timeout
		}
	})
}

// WithBacktestSkipTicks skips tick data download — useful when the strategy
// only uses kline data, significantly reducing download time.
func WithBacktestSkipTicks() BacktestOption {
	return backtestOptionFunc(func(o *backtestBuildOptions) {
		o.skipTicks = true
	})
}

// WithBacktestProgress sets a callback that receives progress stage descriptions
// during data download.
func WithBacktestProgress(fn func(stage string)) BacktestOption {
	return backtestOptionFunc(func(o *backtestBuildOptions) {
		o.onProgress = fn
	})
}

func NewBacktestWiring(cfg Config, btCfg backtest.Config, opts ...BacktestOption) (*BacktestWiring, error) {
	build := backtestBuildOptions{loadTimeout: 120 * time.Second}
	for _, opt := range opts {
		if opt != nil {
			opt.applyBacktest(&build)
		}
	}

	// Validate config early
	if err := runtime.ValidateConfig(btCfg); err != nil {
		return nil, fmt.Errorf("backtest config validation: %w", err)
	}

	provider, lazyDownloader, err := resolveBacktestProvider(cfg, btCfg, build)
	if err != nil {
		return nil, err
	}

	kernel, err := runtime.NewEventKernel(btCfg, provider)
	if err != nil {
		return nil, err
	}

	marketOpts := []btmarket.Option{}
	if build.autoRun != nil {
		marketOpts = append(marketOpts, btmarket.WithAutoRun(*build.autoRun))
	}
	if lazyDownloader != nil {
		marketOpts = append(marketOpts, btmarket.WithLazyDownloader(lazyDownloader))
	}
	marketSvc := btmarket.NewSimMarketService(kernel, provider, marketOpts...)

	// Resolve initial balance for trade service and report collector
	initialBalance := btCfg.InitialBalance
	if build.initialBalance != nil {
		initialBalance = *build.initialBalance
	}
	if initialBalance <= 0 {
		initialBalance = 10_000_000
	}

	collector := report.NewCollector(initialBalance)

	tradeOpts := []bttrade.Option{
		bttrade.WithInitialBalance(initialBalance),
		bttrade.WithSettlementObserver(collector),
	}
	tradeSvc := bttrade.NewSimTradeService(kernel, tradeOpts...)

	return &BacktestWiring{
		Auth:       backtest.NewNoopAuthService(),
		Market:     marketSvc,
		Trade:      tradeSvc,
		Runtime:    kernel,
		Provider:   provider,
		Collector:  collector,
		Downloader: lazyDownloader,
	}, nil
}

// NewBacktestClient creates a fully-wired backtest Client.
// This is the primary entry point for backtest users.
func NewBacktestClient(cfg Config, btCfg backtest.Config, opts ...BacktestOption) (api.Client, error) {
	w, err := NewBacktestWiring(cfg, btCfg, opts...)
	if err != nil {
		return nil, err
	}
	return api.NewClient(api.Config{
		AuthService:   w.Auth,
		MarketService: w.Market,
		TradeService:  w.Trade,
	})
}

func resolveBacktestProvider(cfg Config, btCfg backtest.Config, build backtestBuildOptions) (data.DataProvider, api.MarketService, error) {
	if build.provider != nil {
		// User supplied a custom provider — no lazy downloader.
		return build.provider, build.downloader, nil
	}

	downloader := build.downloader
	var cliToClose api.Client

	if downloader == nil {
		// Create a live market service for historical data download.
		httpClient := &http.Client{Timeout: 30 * time.Second}
		authSvc := domain.NewAuthService(httpClient, cfg.AuthURL)

		if cfg.User != "" && cfg.Password != "" {
			loginCtx, loginCancel := context.WithTimeout(context.Background(), 15*time.Second)
			_, _ = authSvc.Login(loginCtx, cfg.User, cfg.Password)
			loginCancel()
		}

		btURL := cfg.MarketURL
		if btURL == "" {
			urlCtx, urlCancel := context.WithTimeout(context.Background(), 15*time.Second)
			resolved, err := authSvc.ResolveMDURL(urlCtx, true, true)
			urlCancel()
			if err == nil && resolved != "" {
				btURL = resolved
			}
		}

		marketStore := store.NewMarketStore()
		tradeStore := store.NewTradeStore()
		marketSvc := domain.NewMarketService(authSvc, marketStore, btURL, httpClient)
		tradeSvc := domain.NewTradeService(authSvc, tradeStore, cfg.AutoAdd, marketSvc)
		downloader = marketSvc

		apiCfg := api.Config{
			AuthService:   authSvc,
			MarketService: marketSvc,
			TradeService:  tradeSvc,
		}
		if cfg.User != "" && cfg.Password != "" {
			apiCfg.AutoLogin = &api.AuthCredential{User: cfg.User, Password: cfg.Password}
		}
		cli, err := api.NewClient(apiCfg)
		if err != nil {
			return nil, nil, err
		}
		cliToClose = cli

		loadCtx, cancel := context.WithTimeout(context.Background(), build.loadTimeout)
		defer cancel()
		if err := cli.Run(loadCtx); err != nil {
			return nil, nil, err
		}
	}

	rtCfg := runtime.NormalizeConfig(btCfg)

	// If no symbols are specified, skip upfront download — data will be loaded
	// lazily when SubscribeKlines/SubscribeTicks is called.
	if len(rtCfg.Symbols) == 0 {
		if cliToClose != nil {
			// Keep the client alive for lazy loading — caller is responsible for
			// closing it via BacktestWiring.Downloader.
			// We do NOT defer close here.
		}
		return data.NewMemoryProvider(), downloader, nil
	}

	// Upfront download for the declared symbols.
	loadCtx, cancel := context.WithTimeout(context.Background(), build.loadTimeout)
	defer cancel()

	if cliToClose == nil {
		// External downloader — ensure it's started.
		started, stopFn, err := ensureMarketStarted(loadCtx, downloader)
		if err != nil {
			return nil, nil, err
		}
		if started {
			defer stopFn()
		}
	}

	provider, err := data.BuildMemoryProviderFromMarket(loadCtx, downloader, data.DownloadConfig{
		StartDT:              rtCfg.StartDT,
		EndDT:                rtCfg.EndDT,
		Symbols:              append([]string(nil), rtCfg.Symbols...),
		KlineDurations:       append([]int(nil), rtCfg.KlineDurations...),
		StrictMode:           rtCfg.StrictMode,
		EnableAuto1mForQuote: rtCfg.QuoteBasisPolicy == runtime.QuoteBasisCompatAuto1m || rtCfg.QuoteBasisPolicy == runtime.QuoteBasisOnOrderAuto1m,
		SkipTicks:            build.skipTicks,
		OnProgress:           build.onProgress,
	})
	if err != nil {
		return nil, nil, err
	}

	if cliToClose != nil {
		// Close the temporary download client — lazy loading will reuse downloader directly.
		_ = cliToClose.Close()
	}

	return provider, downloader, nil
}

func ensureMarketStarted(ctx context.Context, market api.MarketService) (bool, func(), error) {
	if market == nil {
		return false, func() {}, fmt.Errorf("market service is nil")
	}

	// If this market service is already running externally, ConnEvents should work.
	if _, err := market.ConnEvents(ctx); err == nil {
		return false, func() {}, nil
	}

	noopAuth := backtest.NewNoopAuthService()
	cli, err := api.NewClient(api.Config{
		AuthService:   noopAuth,
		MarketService: market,
		TradeService:  &noopTradeService{},
	})
	if err != nil {
		return false, func() {}, err
	}
	if err := cli.Run(ctx); err != nil {
		return false, func() {}, err
	}
	return true, func() { _ = cli.Close() }, nil
}

type noopTradeService struct{}

func (s *noopTradeService) Login(_ context.Context, _ api.TradeLoginReq) (api.TradeSession, error) {
	return nil, api.NewError(api.ErrUnsupportedBacktest, "noop trade service does not support login", nil)
}

func (s *noopTradeService) Session(_ string) (api.TradeSession, bool) {
	return nil, false
}

func (s *noopTradeService) GetTradingStatus(_ context.Context, _ string) (api.TradingStatus, error) {
	return api.TradingStatus{}, api.NewError(api.ErrUnsupportedBacktest, "noop trade service does not support trading status", nil)
}

func (s *noopTradeService) TradingStatus(_ string) (api.TradingStatus, bool) {
	return api.TradingStatus{}, false
}

func (s *noopTradeService) SubscribeTradingStatuses(_ context.Context, _ ...string) (api.TradingStatusSub, error) {
	return nil, api.NewError(api.ErrUnsupportedBacktest, "noop trade service does not support trading status subscriptions", nil)
}
