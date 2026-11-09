// Command targetpos 演示目标持仓任务（TargetPosTask）：策略只给出目标净仓，任务负责平今、平昨、开仓与追价，
// 随 WaitUpdate 自动推进。同一份策略代码可在回测与 paper trade 中运行。
//
// 回测（默认生成合成数据集，无需网络）：双均线在 1 分钟线上产生多空目标，打印每笔成交与日结报告。
//
//	go run ./examples/v1alpha3/targetpos
//	TQ_AUTH_USER=... TQ_AUTH_PASS=... go run ./examples/v1alpha3/targetpos -online -symbol SHFE.rb2701 -start 2026-11-03 -end 2026-11-05
//
// paper trade：实时行情驱动本地模拟账户，持有目标 -hold 后平仓并收尾（非交易时段任务会等待开盘）。
//
//	TQ_AUTH_USER=... TQ_AUTH_PASS=... go run ./examples/v1alpha3/targetpos -paper -symbol SHFE.rb2701 -volume 2 -hold 30s
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/auth"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/backtest"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/sim"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/targetpos"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/tqapi"
)

var cst = time.FixedZone("CST", 8*3600)

func main() {
	symbol := flag.String("symbol", "SHFE.rb2601", "合约代码")
	volume := flag.Int64("volume", 3, "目标持仓手数")
	paper := flag.Bool("paper", false, "paper trade：实时行情 + 本地模拟账户")
	hold := flag.Duration("hold", 30*time.Second, "paper：达到目标后持有多久再平仓")
	timeout := flag.Duration("timeout", 10*time.Minute, "paper：最长运行时间")
	online := flag.Bool("online", false, "回测使用天勤历史行情服务")
	first := flag.String("start", "2026-09-14", "回测首个交易日")
	last := flag.String("end", "2026-09-16", "回测最后交易日")
	flag.Parse()

	ctx := context.Background()
	cfg := tqapi.Config{Market: &core.MarketConfig{}, Account: sim.New(sim.Config{InitBalance: 1_000_000})}
	if *paper || *online {
		cfg.Auth = auth.NewClient(os.Getenv("TQ_AUTH_USER"), os.Getenv("TQ_AUTH_PASS"))
	}
	if !*paper {
		bt, err := backtestConfig(*symbol, *first, *last, *online, cfg.Auth)
		if err != nil {
			log.Fatal(err)
		}
		cfg.Backtest = bt
	}
	api, err := tqapi.New(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer api.Close() // 异常兜底；正常路径用 Shutdown 撤单收尾

	account, _ := api.Account()
	task, err := targetpos.New(ctx, api, account, targetpos.Config{Symbol: *symbol}) // 默认对价、"今昨,开"
	if err != nil {
		log.Fatal(err)
	}
	if *paper {
		pctx, cancel := context.WithTimeout(ctx, *timeout)
		err = runPaper(pctx, api, task, *volume, *hold)
		cancel()
	} else {
		err = runBacktest(ctx, api, task, *symbol, *volume)
	}
	if err != nil {
		log.Print(err)
	}
	// 收尾：取消全部任务、撤销在途单并继续推进直到结清，最后关闭 API。
	sctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	if err := api.Shutdown(sctx); err != nil && !errors.Is(err, backtest.ErrFinished) {
		log.Print("shutdown: ", err)
	}
	if c, err := task.CloseResult(); err == nil && !c.Confirmed {
		log.Printf("未确认的委托：%+v", c.Unresolved)
	}
	if api.Backtest != nil {
		report(api.Backtest.Result().Accounts)
	} else {
		report(api.Paper.Reports())
	}
}

// runBacktest：1 分钟线 5/20 均线，快线上穿目标 +volume，下穿目标 −volume。
func runBacktest(ctx context.Context, api *tqapi.API, task *targetpos.Task, symbol string, volume int64) error {
	bars, err := api.Market.Klines(ctx, core.KlineRequest{Symbol: symbol, Period: time.Minute, Width: 21})
	if err != nil {
		return err
	}
	printed := 0
	var target int64
	for {
		pulse, err := api.WaitUpdate(ctx)
		if errors.Is(err, backtest.ErrFinished) {
			return nil
		}
		if err != nil {
			return err
		}
		if e := pulse.TaskError(); e != nil {
			return e // 致命错误（如外部撤单）：任务已在收尾
		}
		printed = printFills(api, task, printed)
		if !pulse.IsChanging(bars.LatestRow(), "datetime") { // 只在新柱出现时判断
			continue
		}
		closes := closes(bars.Snapshot())
		if len(closes) < 21 {
			continue
		}
		done := closes[:len(closes)-1] // 不含刚开盘的新柱
		want := -volume
		if ma(done, 5) > ma(done, 20) {
			want = volume
		}
		if want != target { // 信号变化时才设置新目标；执行中的目标由任务继续追价完成
			target = want
			if _, err := task.SetTarget(target); err != nil {
				return err
			}
		}
	}
}

// runPaper：设置目标并等待达到，持有 hold 后平仓。
func runPaper(ctx context.Context, api *tqapi.API, task *targetpos.Task, volume int64, hold time.Duration) error {
	printed := 0
	for i, target := range []int64{volume, 0} {
		q, err := task.SetTarget(target)
		if err != nil {
			return err
		}
		fmt.Printf("目标 %d 手\n", target)
		var deadline time.Time
		for !q.Done() || time.Now().Before(deadline) {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("运行超时，%s", task.Snapshot().WaitReason)
			}
			wctx, cancel := context.WithTimeout(ctx, time.Second) // 短等待：持有期在没有行情时也能结束
			pulse, err := api.WaitUpdate(wctx)
			cancel()
			if err != nil && !errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			if e := pulse.TaskError(); e != nil {
				return e
			}
			if pulse.TasksChanged() {
				fmt.Println("任务状态:", task.Snapshot().Phase, task.Snapshot().WaitReason)
			}
			printed = printFills(api, task, printed)
			if q.Done() && deadline.IsZero() && i == 0 {
				r, _ := q.Result()
				fmt.Printf("目标结果 %s：净仓 %d，持有 %v\n", r.Outcome, r.ObservedNet, hold)
				deadline = time.Now().Add(hold)
			}
		}
		r, _ := q.Result()
		fmt.Printf("目标 %d 结果 %s：成交 %d 手\n", target, r.Outcome, r.Traded)
	}
	return nil
}

// printFills 增量打印任务的自有成交（Python trade_chan 的用法）。
func printFills(api *tqapi.API, task *targetpos.Task, printed int) int {
	fills := task.Trades()
	for _, f := range fills[printed:] {
		t := f.Trade
		fmt.Printf("%s 成交 %s %s %d @ %.0f（净仓变化 %+d，目标版本 %d）\n",
			time.Unix(0, t.TradeDateTime).In(cst).Format("2006-01-02 15:04:05"), t.Direction, t.Offset, t.Volume, t.Price, f.Signed, f.Version)
	}
	return len(fills)
}

func report(accounts []sim.Report) {
	for _, a := range accounts {
		for _, d := range a.Days {
			fmt.Printf("日结 %s 权益 %.2f 手续费 %.2f 成交 %d 笔\n", d.Date, d.Account.Balance, d.Account.Commission, len(d.Trades))
		}
	}
}

func closes(f core.KlineFrame) []float64 {
	var out []float64
	for _, r := range f.Rows {
		if r.Present {
			out = append(out, r.Value.Close)
		}
	}
	return out
}

func ma(xs []float64, n int) float64 {
	var s float64
	for _, x := range xs[len(xs)-n:] {
		s += x
	}
	return s / float64(n)
}

// ---- 回测数据 ----

func backtestConfig(symbol, first, last string, online bool, a auth.Provider) (*backtest.Config, error) {
	d1, err1 := time.ParseInLocation("2006-01-02", first, cst)
	d2, err2 := time.ParseInLocation("2006-01-02", last, cst)
	if err := errors.Join(err1, err2); err != nil {
		return nil, err
	}
	start, end := backtest.TradingDays(d1, d2)
	if online {
		return &backtest.Config{Start: start, End: end, Data: backtest.Online(backtest.OnlineConfig{Auth: a})}, nil
	}
	dir := filepath.Join(os.TempDir(), "tqsdk-go-targetpos-sample")
	if err := writeSample(dir, symbol, start, end); err != nil {
		return nil, err
	}
	return &backtest.Config{Start: start, End: end, Data: backtest.Dataset(dir)}, nil
}

// writeSample 生成上期所螺纹钢日盘的合成 1 分钟线（随机游走），覆盖回测区间及之前一天的预热。
func writeSample(dir, symbol string, start, end time.Time) error {
	ins := model.Instrument{Symbol: symbol, InstrumentID: symbol, ExchangeID: "SHFE", ProductID: "rb", InsClass: "FUTURE",
		PriceTick: 1, VolumeMultiple: 10, Margin: 3500, Commission: 2,
		TradingTime: model.TradingTime{Day: [][]string{{"09:00:00", "10:15:00"}, {"10:30:00", "11:30:00"}, {"13:30:00", "15:00:00"}}}}
	for _, f := range []model.InstrumentField{model.InstrumentInstrumentID, model.InstrumentExchangeID, model.InstrumentProductID,
		model.InstrumentInsClass, model.InstrumentPriceTick, model.InstrumentVolumeMultiple, model.InstrumentMargin, model.InstrumentCommission} {
		ins.Valid.Add(f)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	price := 3500.0
	var bars []model.Kline
	for day := start.Add(-24 * time.Hour).Truncate(24 * time.Hour); day.Before(end); day = day.Add(24 * time.Hour) {
		if wd := day.In(cst).Weekday(); wd == time.Saturday || wd == time.Sunday {
			continue
		}
		y, m, d := day.In(cst).Date()
		for _, s := range ins.TradingTime.Day {
			from, _ := time.ParseInLocation("15:04:05", s[0], cst)
			to, _ := time.ParseInLocation("15:04:05", s[1], cst)
			for t := time.Date(y, m, d, from.Hour(), from.Minute(), 0, 0, cst); t.Before(time.Date(y, m, d, to.Hour(), to.Minute(), 0, 0, cst)); t = t.Add(time.Minute) {
				k := model.Kline{ID: int64(len(bars)), Datetime: t.UnixNano(), Open: price, Volume: 100, OpenOI: 1000, CloseOI: 1000}
				hi, lo := price, price
				for range 4 {
					price = math.Round(price + rng.NormFloat64()*3)
					hi, lo = max(hi, price), min(lo, price)
				}
				k.High, k.Low, k.Close = hi, lo, price
				bars = append(bars, k)
			}
		}
	}
	return backtest.WriteDataset(dir, "targetpos-sample", []model.Instrument{ins},
		[]backtest.Series{{Symbol: symbol, Period: time.Minute, Rows: bars}})
}
