// backtest_demo — Go port of the Python tqsdk backtest tutorial.
//
// Python original:
//
//	api = TqApi(backtest=TqBacktest(start_dt=date(2018,5,1), end_dt=date(2018,10,1)),
//	            auth=TqAuth(user, password))
//	klines = api.get_kline_serial("DCE.m1901", 5*60, data_length=15)
//	target_pos = TargetPosTask(api, "DCE.m1901")
//	while True:
//	    api.wait_update()
//	    if api.is_changing(klines):
//	        ma = sum(klines.close[-15:]) / 15
//	        if klines.close[-1] > ma:  target_pos.set_target_volume(5)
//	        elif klines.close[-1] < ma: target_pos.set_target_volume(0)
//
// This Go demo reproduces the same MA crossover logic with the backtest engine.
// Historical kline data is downloaded automatically from the live tq server.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/app"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest/runtime"
)

// lotSize is the target long volume for the MA crossover strategy (currently commented out).
var _ = lotSize

const (
	// symbol   = "DCE.m1901"
	symbol   = "KQ.m@DCE.m"
	duration = 5 * 60 // 5-minute klines (seconds)
	maLen    = 15     // MA period
	lotSize  = 5      // target long volume
)

func init() {
	log.SetFlags(log.Lshortfile | log.LstdFlags)
}

func main() {
	user := os.Getenv("SHINNYTECH_ID")
	password := os.Getenv("SHINNYTECH_PW")
	if user == "" || password == "" {
		log.Fatal("please set SHINNYTECH_ID and SHINNYTECH_PW environment variables")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	totalStart := time.Now()

	// ---------------------------------------------------------------
	// 1. Backtest configuration
	// ---------------------------------------------------------------
	btCfg := backtest.Config{
		StartDT:          time.Date(2018, 1, 1, 0, 0, 0, 0, time.Local),
		EndDT:            time.Date(2018, 12, 31, 0, 0, 0, 0, time.Local),
		InitialBalance:   10_000_000,
		QuoteBasisPolicy: runtime.QuoteBasisNoAuto1m,
		// Symbols and KlineDurations are intentionally left empty —
		// data is downloaded lazily when SubscribeKlines is called,
		// matching the Python TqApi behavior.
	}

	appCfg := app.Config{User: user, Password: password}

	// ---------------------------------------------------------------
	// 2. Create BacktestWiring — sets up EventKernel, SimMarket,
	//    SimTrade, ReportCollector. No data is downloaded yet —
	//    that happens lazily when SubscribeKlines is called.
	//    autoRun=false: kernel will NOT start until we subscribe.
	// ---------------------------------------------------------------
	log.Println("[阶段1] 创建回测环境 ...")
	downloadStart := time.Now()
	wiring, err := app.NewBacktestWiring(appCfg, btCfg,
		app.WithBacktestSkipTicks(),                                 // kline-only strategy, skip tick download
		app.WithBacktestProgress(func(s string) { log.Println(s) }), // print download progress
		app.WithBacktestLoadTimeout(5*time.Minute),                  // allow enough time for kline download
		app.WithBacktestAutoRun(false),                              // do NOT auto-start kernel — subscribe first
	)
	if err != nil {
		log.Fatalf("NewBacktestWiring: %v", err)
	}
	downloadElapsed := time.Since(downloadStart)
	log.Printf("[阶段1] 创建完成, 耗时: %s", downloadElapsed)

	// Use api.Client to start market + trade services (but kernel stays paused).
	setupStart := time.Now()
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
	// 3. Subscribe to 5-minute klines (BEFORE starting the kernel).
	//    This triggers lazy data download from the live server.
	// ---------------------------------------------------------------
	log.Println("[阶段2] 订阅 K 线 (触发数据下载) ...")
	subStart := time.Now()
	klineSub, err := wiring.Market.SubscribeKlines(ctx, []string{symbol}, duration, maLen)
	if err != nil {
		log.Fatalf("SubscribeKlines: %v", err)
	}
	defer klineSub.Close()
	log.Printf("[阶段2] 订阅完成 (含数据下载), 耗时: %s", time.Since(subStart))

	// ---------------------------------------------------------------
	// 4. Login to sim trade session
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
	setupElapsed := time.Since(setupStart)
	log.Printf("[阶段2] 初始化完成 (Client + Subscribe + Login), 耗时: %s", setupElapsed)

	// ---------------------------------------------------------------
	// 5. Target-position helper (simplified TargetPosTask)
	// ---------------------------------------------------------------
	currentPos := 0 // net long position tracked by the demo

	setTargetVolume := func(target int) {
		diff := target - currentPos
		if diff == 0 {
			return
		}
		var dir api.Direction
		var offset api.Offset
		vol := diff
		if diff > 0 {
			dir, offset = api.DirectionBuy, api.OffsetOpen
		} else {
			dir, offset = api.DirectionSell, api.OffsetClose
			vol = -diff
		}
		ref, err := session.InsertOrder(ctx, symbol, dir, vol, api.WithOffset(offset))
		if err != nil {
			log.Printf("  InsertOrder %s %s %d: %v", dir, offset, vol, err)
			return
		}
		order, err := ref.WaitDone(ctx)
		if err != nil {
			log.Printf("  WaitDone: %v", err)
			return
		}
		if order.Status == "FINISHED" {
			currentPos = target
		} else {
			log.Printf("  Order %s: status=%s msg=%s", order.OrderID, order.Status, order.LastMsg)
		}
	}

	// ---------------------------------------------------------------
	// 6. Start the EventKernel NOW — all subscriptions are in place
	// ---------------------------------------------------------------
	kernelDone := make(chan error, 1)
	go func() {
		kernelDone <- wiring.Runtime.Run(ctx)
	}()

	// ---------------------------------------------------------------
	// 7. Event loop — consume kline events until backtest ends
	// ---------------------------------------------------------------
	log.Println("[阶段3] 回测运行中 ...")
	backtestStart := time.Now()
	tradeDecisions := 0

	processKline := func(ev api.KlineEvent) {
		if !klineSub.IsSerialReady() {
			return
		}
		frame := ev.Frame1D
		if len(frame) < maLen {
			return
		}
		// lastBar := frame[len(frame)-1]
		// ts := time.Unix(0, lastBar.Datetime)
		// log.Printf("ID[%d] len(%d) time: %v\n", lastBar.ID, len(frame), ts)

		closes := make([]float64, 0, maLen)
		for i := len(frame) - maLen; i < len(frame); i++ {
			if frame[i] != nil {
				closes = append(closes, frame[i].Close)
			}
		}
		if len(closes) < maLen {
			return
		}
		var sum float64
		for _, c := range closes {
			sum += c
		}
		ma := sum / float64(maLen)
		last := closes[maLen-1]

		if last > ma && currentPos != lotSize {
			setTargetVolume(lotSize)
			tradeDecisions++
		} else if last < ma && currentPos != 0 {
			setTargetVolume(0)
			tradeDecisions++
		}
	}
	_ = setTargetVolume
loop:
	for {
		select {
		case <-ctx.Done():
			log.Println("Context cancelled")
			break loop

		case err := <-kernelDone:
			// EventKernel.Run() returned — all events processed
			if err != nil {
				log.Printf("Kernel finished with error: %v", err)
			}
			// Drain any remaining kline events in the channel
			for {
				select {
				case ev, ok := <-klineSub.C():
					if !ok {
						break loop
					}
					processKline(ev)
				default:
					break loop
				}
			}

		case ev, ok := <-klineSub.C():
			if !ok {
				break loop
			}
			processKline(ev)
		}
	}

	backtestElapsed := time.Since(backtestStart)
	log.Printf("[阶段3] 回测完成, 耗时: %s", backtestElapsed)

	// ---------------------------------------------------------------
	// 8. Print the final report
	// ---------------------------------------------------------------
	reportStart := time.Now()
	rpt := wiring.Collector.Finalize()

	fmt.Println()
	fmt.Println("========== 回测报告 ==========")
	fmt.Printf("交易天数      : %d\n", rpt.TradingDays)
	fmt.Printf("初始资金      : %.2f\n", rpt.InitialBalance)
	fmt.Printf("最终权益      : %.2f\n", rpt.FinalBalance)
	fmt.Printf("总收益率      : %.4f (%.2f%%)\n", rpt.TotalReturn, rpt.TotalReturn*100)
	fmt.Printf("年化收益率    : %.4f (%.2f%%)\n", rpt.AnnualReturn, rpt.AnnualReturn*100)
	fmt.Printf("最大回撤      : %.4f (%.2f%%)\n", rpt.MaxDrawdown, rpt.MaxDrawdown*100)
	fmt.Printf("夏普率        : %.4f\n", rpt.SharpeRatio)
	fmt.Printf("索提诺比率    : %.4f\n", rpt.SortinoRatio)
	fmt.Printf("卡玛比率      : %.4f\n", rpt.CalmarRatio)
	fmt.Printf("胜率          : %.4f\n", rpt.TradeStats.WinningRate)
	fmt.Printf("盈亏比        : %.4f\n", rpt.TradeStats.ProfitLossRatio)
	fmt.Printf("开仓次数      : %d\n", rpt.TradeStats.OpenTimes)
	fmt.Printf("平仓次数      : %d\n", rpt.TradeStats.CloseTimes)
	fmt.Printf("日均风险度    : %.4f\n", rpt.DayStats.DailyRiskRatio)
	fmt.Printf("总手续费      : %.2f\n", rpt.DayStats.TotalCommission)
	fmt.Printf("策略评语      : %s\n", rpt.Punchline)
	fmt.Printf("交易决策次数  : %d\n", tradeDecisions)
	fmt.Println("===============================")

	// ---------------------------------------------------------------
	// 9. Print detailed trade log
	// ---------------------------------------------------------------
	fmt.Println()
	fmt.Println("========== 成交明细 ==========")
	fmt.Printf("%-12s %-6s %-6s %-6s %10s %6s  %s\n",
		"交易日", "方向", "开平", "合约", "成交价", "手数", "成交时间")
	fmt.Println("---------------------------------------------------------------")

	totalTrades := 0
	for _, snap := range rpt.Snapshots {
		// for _, t := range snap.Trades {
		for range snap.Trades {

			totalTrades++
			// tradeTime := time.Unix(0, t.TradeDateTime).Format("15:04:05")
			// dirStr := "买"
			// if t.Direction == "SELL" {
			// 	dirStr = "卖"
			// }
			// offStr := "开"
			// switch t.Offset {
			// case "CLOSE":
			// 	offStr = "平"
			// case "CLOSETODAY":
			// 	offStr = "平今"
			// case "CLOSEYESTERDAY":
			// 	offStr = "平昨"
			// }
			// fmt.Printf("%-12s %-6s %-6s %-6s %10.1f %6d  %s\n",
			// 	snap.TradingDay, dirStr, offStr, t.InstrumentID,
			// 	t.Price, t.Volume, tradeTime)
		}
	}
	fmt.Println("---------------------------------------------------------------")
	fmt.Printf("合计成交笔数  : %d\n", totalTrades)
	fmt.Println("===============================")

	// ---------------------------------------------------------------
	// 10. Print daily P&L summary
	// ---------------------------------------------------------------
	fmt.Println()
	fmt.Println("========== 每日盈亏 ==========")
	fmt.Printf("%-12s %14s %12s %12s %12s %12s\n",
		"交易日", "权益", "平仓盈亏", "浮动盈亏", "手续费", "日盈亏")
	fmt.Println("-----------------------------------------------------------------------")

	prevBalance := rpt.InitialBalance
	for _, snap := range rpt.Snapshots {
		dayPnL := snap.Balance - prevBalance
		fmt.Printf("%-12s %14.2f %12.2f %12.2f %12.2f %12.2f\n",
			snap.TradingDay, snap.Balance, snap.CloseProfit,
			snap.FloatProfit, snap.Commission, dayPnL)
		prevBalance = snap.Balance
	}
	fmt.Println("-----------------------------------------------------------------------")
	fmt.Printf("最终权益      : %.2f  总盈亏: %.2f\n",
		rpt.FinalBalance, rpt.FinalBalance-rpt.InitialBalance)
	fmt.Println("===============================")

	reportElapsed := time.Since(reportStart)
	totalElapsed := time.Since(totalStart)

	// ---------------------------------------------------------------
	// 11. Phase timing summary
	// ---------------------------------------------------------------
	fmt.Println()
	fmt.Println("========== 耗时统计 ==========")
	fmt.Printf("阶段1 创建环境      : %s\n", downloadElapsed)
	fmt.Printf("阶段2 订阅+下载     : %s\n", setupElapsed)
	fmt.Printf("阶段3 回测运行      : %s\n", backtestElapsed)
	fmt.Printf("阶段4 报告生成      : %s\n", reportElapsed)
	fmt.Printf("总耗时            : %s\n", totalElapsed)
	fmt.Println("===============================")
}
