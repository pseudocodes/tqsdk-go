// indicator_demo — Backtest with MA indicator overlay on the web UI.
//
// Demonstrates the IndicatorOverlay API: computes MA5 and MA20 on each
// kline event and pushes them to the WebAdapter for browser rendering.
//
// Usage:
//
//	SHINNYTECH_PW=xxx go run ./examples/indicator_demo
//	# open http://127.0.0.1:9876 in browser
package main

import (
	"context"
	"flag"
	"log"
	"math"
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
	maShort  = 5
	maLong   = 12
	dataLen  = 200
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
	// 1. Backtest config — lazy loading (no upfront symbols)
	// ---------------------------------------------------------------
	btCfg := backtest.Config{
		StartDT:          time.Date(2018, 5, 1, 0, 0, 0, 0, time.Local),
		EndDT:            time.Date(2018, 5, 6, 0, 0, 0, 0, time.Local),
		InitialBalance:   10_000_000,
		QuoteBasisPolicy: runtime.QuoteBasisNoAuto1m,
	}
	appCfg := app.Config{User: user, Password: password}

	log.Println("[1] 创建回测环境 ...")
	wiring, err := app.NewBacktestWiring(appCfg, btCfg,
		app.WithBacktestSkipTicks(),
		app.WithBacktestProgress(func(s string) { log.Println(s) }),
		app.WithBacktestLoadTimeout(5*time.Minute),
		app.WithBacktestAutoRun(false),
	)
	if err != nil {
		log.Fatalf("NewBacktestWiring: %v", err)
	}

	// ---------------------------------------------------------------
	// 2. api.Client
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
	// 3. Subscribe klines (triggers lazy download)
	// ---------------------------------------------------------------
	log.Println("[2] 订阅 K 线 ...")
	klineSub, err := wiring.Market.SubscribeKlines(ctx, []string{symbol}, duration, dataLen,
		api.WithKlineEmitFrame(true))
	if err != nil {
		log.Fatalf("SubscribeKlines: %v", err)
	}
	defer klineSub.Close()
	log.Println("[2] 订阅完成")

	// ---------------------------------------------------------------
	// 4. Login to sim trade
	// ---------------------------------------------------------------
	session, err := wiring.Trade.Login(ctx, api.TradeLoginReq{
		AccountID: "backtest", BrokerID: "backtest",
		UserName: "backtest", Password: "backtest",
	})
	if err != nil {
		log.Fatalf("Trade.Login: %v", err)
	}

	// ---------------------------------------------------------------
	// 5. WebAdapter + Gateway
	// ---------------------------------------------------------------
	adapter := webadapter.New(webadapter.Config{
		Mode:            "backtest",
		Symbols:         []string{symbol},
		KlineDurations:  []int{duration},
		KlineDataLength: dataLen,
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

	gw := webadapter.NewGateway(adapter, webadapter.GatewayConfig{
		Addr:        *addr,
		WebDir:      *webDir,
		URLResponse: urlResp,
	})
	go func() {
		log.Printf("网页预览: http://%s", *addr)
		log.Printf("WebSocket: ws://%s/ws", *addr)
		if err := gw.Start(ctx); err != nil {
			log.Printf("Gateway: %v", err)
		}
	}()

	// ---------------------------------------------------------------
	// 6. Create IndicatorOverlay — MA5 (orange) + MA20 (blue)
	// ---------------------------------------------------------------
	overlay := webadapter.NewIndicatorOverlay(adapter, symbol, duration)

	ma5Series := overlay.AddSeries("MA5", webadapter.SeriesOpts{
		Style: webadapter.StyleLine,
		Color: "#FF6600",
		Width: 1,
		Board: webadapter.BoardMain,
	})
	ma20Series := overlay.AddSeries("MA20", webadapter.SeriesOpts{
		Style: webadapter.StyleLine,
		Color: "#0066FF",
		Width: 2,
		Board: webadapter.BoardMain,
	})

	// ---------------------------------------------------------------
	// 7. Start kernel + event loop
	// ---------------------------------------------------------------
	log.Println("[3] 回测运行中 ...")
	kernelDone := make(chan error, 1)
	go func() {
		kernelDone <- wiring.Runtime.Run(ctx)
	}()

	events := 0
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case err := <-kernelDone:
			if err != nil {
				log.Printf("Kernel error: %v", err)
			}
			// Drain remaining events
			for {
				select {
				case ev, ok := <-klineSub.C():
					if !ok {
						break loop
					}
					processKline(ev, ma5Series, ma20Series, overlay)
					events++
				default:
					break loop
				}
			}
		case ev, ok := <-klineSub.C():
			if !ok {
				break loop
			}
			processKline(ev, ma5Series, ma20Series, overlay)
			events++
		}
	}

	log.Printf("[3] 回测完成, 处理 %d 个 K 线事件", events)

	// ---------------------------------------------------------------
	// 8. Push report
	// ---------------------------------------------------------------
	rpt := wiring.Collector.Finalize()
	stat := map[string]any{
		"init_balance": rpt.InitialBalance,
		"balance":      rpt.FinalBalance,
		"ror":          rpt.TotalReturn,
		"annual_yield": rpt.AnnualReturn,
		"max_drawdown": rpt.MaxDrawdown,
		"sharpe_ratio": rpt.SharpeRatio,
		"trading_days": rpt.TradingDays,
	}
	adapter.PushReportData(map[string]any{
		"backtest": map[string]any{"metrics": stat},
	})
	adapter.PublishDiff(map[string]any{
		"trade": map[string]any{
			"backtest": map[string]any{
				"accounts": map[string]any{
					"CNY": map[string]any{"_tqsdk_stat": stat},
				},
			},
		},
	})

	log.Println("[4] 报告已推送, Ctrl+C 退出")
	<-ctx.Done()
	_ = gw.Close(context.Background())
}

// processKline computes MA5 and MA20 from the frame and pushes to overlay.
func processKline(ev api.KlineEvent, ma5s, ma20s *webadapter.IndicatorSeries, overlay *webadapter.IndicatorOverlay) {
	if !ev.SerialReady {
		return
	}
	frame := ev.Frame1D
	if len(frame) == 0 {
		return
	}
	id := ev.Row.MainID

	if v := calcMA(frame, maShort); !math.IsNaN(v) {
		ma5s.Set(id, v)
	}
	if v := calcMA(frame, maLong); !math.IsNaN(v) {
		ma20s.Set(id, v)
	}
	overlay.Flush()
}

// calcMA computes a simple moving average over the last n non-nil klines.
func calcMA(frame []*api.Kline, n int) float64 {
	count := 0
	sum := 0.0
	for i := len(frame) - 1; i >= 0 && count < n; i-- {
		if frame[i] != nil {
			sum += frame[i].Close
			count++
		}
	}
	if count < n {
		return math.NaN()
	}
	return sum / float64(n)
}
