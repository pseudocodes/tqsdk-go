// webadapter_demo — Starts a backtest with WebAdapter + WebSocket gateway,
// serving the tqsdk web UI for browser-based K-line visualization.
//
// Usage:
//
//	SHINNYTECH_PW=xxx go run ./cmd/webadapter_demo
//
// Then open http://127.0.0.1:9876 in your browser.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/app"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest/runtime"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/webadapter"
)

const (
	symbol   = "KQ.m@DCE.m"
	duration = 5 * 60 // 5-minute klines
)

func main() {
	log.SetFlags(log.Lshortfile | log.LstdFlags)

	addr := flag.String("addr", "127.0.0.1:9876", "HTTP listen address")
	webDir := flag.String("web-dir", "", "path to web UI directory (empty = use embedded)")
	flag.Parse()

	user := os.Getenv("SHINNYTECH_ID")
	if user == "" {
		user = "neuron"
	}
	password := os.Getenv("SHINNYTECH_PW")
	if password == "" {
		log.Fatal("please set SHINNYTECH_PW environment variable")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// ---------------------------------------------------------------
	// 1. Backtest configuration
	// ---------------------------------------------------------------
	btCfg := backtest.Config{
		StartDT:          time.Date(2018, 5, 1, 0, 0, 0, 0, time.Local),
		EndDT:            time.Date(2018, 10, 1, 0, 0, 0, 0, time.Local),
		Symbols:          []string{symbol},
		KlineDurations:   []int{duration},
		InitialBalance:   10_000_000,
		QuoteBasisPolicy: runtime.QuoteBasisCompatAuto1m,
	}

	appCfg := app.Config{User: user, Password: password}

	// ---------------------------------------------------------------
	// 2. Create BacktestWiring
	// ---------------------------------------------------------------
	log.Println("[1] 下载历史数据 ...")
	wiring, err := app.NewBacktestWiring(appCfg, btCfg,
		app.WithBacktestSkipTicks(),
		app.WithBacktestProgress(func(s string) { log.Println(s) }),
		app.WithBacktestLoadTimeout(5*time.Minute),
		app.WithBacktestAutoRun(false),
	)
	if err != nil {
		log.Fatalf("NewBacktestWiring: %v", err)
	}
	log.Println("[1] 下载完成")

	// ---------------------------------------------------------------
	// 3. Create api.Client
	// ---------------------------------------------------------------
	cli, err := api.NewClient(api.Config{
		AuthService:   wiring.Auth,
		MarketService: wiring.Market,
		TradeService:  wiring.Trade,
	})
	if err != nil {
		log.Fatalf("NewClient: %v", err)
	}
	if err := cli.Run(ctx); err != nil {
		log.Fatalf("Client.Run: %v", err)
	}
	defer func() { _ = cli.Close() }()

	// ---------------------------------------------------------------
	// 4. Subscribe klines (before kernel starts)
	// ---------------------------------------------------------------
	klineSub, err := wiring.Market.SubscribeKlines(ctx, []string{symbol}, duration, 200,
		api.WithKlineEmitFrame(true))
	if err != nil {
		log.Fatalf("SubscribeKlines: %v", err)
	}
	defer klineSub.Close()

	// ---------------------------------------------------------------
	// 5. Login to sim trade
	// ---------------------------------------------------------------
	session, err := wiring.Trade.Login(ctx, api.TradeLoginReq{
		AccountID: "backtest",
		BrokerID:  "backtest",
		UserName:  "backtest",
		Password:  "backtest",
	})
	if err != nil {
		log.Fatalf("Trade.Login: %v", err)
	}

	// ---------------------------------------------------------------
	// 6. Create WebAdapter + Gateway
	// ---------------------------------------------------------------
	adapter := webadapter.New(webadapter.Config{
		Mode:            "backtest",
		Symbols:         []string{symbol},
		KlineDurations:  []int{duration},
		KlineDataLength: 200,
		PeekCompatible:  true,
	})
	if err := adapter.Attach(cli); err != nil {
		log.Fatalf("Adapter.Attach: %v", err)
	}
	if err := adapter.AttachSession(session); err != nil {
		log.Fatalf("Adapter.AttachSession: %v", err)
	}
	if err := adapter.Start(ctx); err != nil {
		log.Fatalf("Adapter.Start: %v", err)
	}
	defer func() { _ = adapter.Close() }()

	// Build /url response for the frontend.
	urlResp := map[string]any{
		"ins_url": "https://openmd.shinnytech.com/t/md/symbols/latest.json",
	}
	if mdURL, err := wiring.Auth.ResolveMDURL(ctx, false, false); err == nil && mdURL != "" {
		urlResp["md_url"] = mdURL
	}

	gwCfg := webadapter.GatewayConfig{
		Addr:        *addr,
		WebDir:      *webDir,
		URLResponse: urlResp,
	}

	gw := webadapter.NewGateway(adapter, gwCfg)

	// Start gateway in background
	go func() {
		log.Printf("[2] 网页预览: http://%s", *addr)
		log.Printf("[2] WebSocket: ws://%s/ws", *addr)
		log.Printf("[2] Snapshot:  http://%s/snapshot", *addr)
		if err := gw.Start(ctx); err != nil {
			log.Printf("Gateway stopped: %v", err)
		}
	}()

	// ---------------------------------------------------------------
	// 7. Start EventKernel — run backtest
	// ---------------------------------------------------------------
	log.Println("[3] 回测运行中 ...")
	kernelDone := make(chan error, 1)
	go func() {
		kernelDone <- wiring.Runtime.Run(ctx)
	}()

	// Drain kline events (keep the pipeline flowing)
	go func() {
		for range klineSub.C() {
		}
	}()

	// Wait for kernel to finish or signal
	select {
	case err := <-kernelDone:
		if err != nil {
			log.Printf("Kernel error: %v", err)
		} else {
			log.Println("[3] 回测完成")
		}
	case <-ctx.Done():
		log.Println("收到退出信号")
	}

	// Push final report to webadapter.
	// The frontend reads metrics from trade.{account}.accounts.CNY._tqsdk_stat
	// (not from draw_report_datas — that's for the detailed report page charts).
	rpt := wiring.Collector.Finalize()
	stat := map[string]any{
		"init_balance":      rpt.InitialBalance,
		"balance":           rpt.FinalBalance,
		"start_balance":     rpt.InitialBalance,
		"end_balance":       rpt.FinalBalance,
		"ror":               rpt.TotalReturn,
		"annual_yield":      rpt.AnnualReturn,
		"max_drawdown":      rpt.MaxDrawdown,
		"sharpe_ratio":      rpt.SharpeRatio,
		"sortino_ratio":     rpt.SortinoRatio,
		"calmar_ratio":      rpt.CalmarRatio,
		"commission":        rpt.DayStats.TotalCommission,
		"trading_days":      rpt.TradingDays,
		"winning_rate":      rpt.TradeStats.WinningRate,
		"profit_loss_ratio": rpt.TradeStats.ProfitLossRatio,
		"open_times":        rpt.TradeStats.OpenTimes,
		"close_times":       rpt.TradeStats.CloseTimes,
		"tqsdk_punchline":   rpt.Punchline,
	}

	// 1. Push draw_report_datas (for the detail report page)
	reportID := "backtest"
	adapter.PushReportData(map[string]any{
		reportID: map[string]any{"metrics": stat},
	})

	// 2. The critical part: push _tqsdk_stat into trade.backtest.accounts.CNY
	//    This is what the frontend's main backtest panel reads.
	adapter.PublishDiff(map[string]any{
		"trade": map[string]any{
			"backtest": map[string]any{
				"accounts": map[string]any{
					"CNY": map[string]any{
						"_tqsdk_stat": stat,
					},
				},
			},
		},
	})

	log.Println("[4] 回测报告已推送")
	log.Println("    保持运行以供浏览器查看，Ctrl+C 退出")

	// Keep alive until signal
	<-ctx.Done()
	_ = gw.Close(context.Background())
}
