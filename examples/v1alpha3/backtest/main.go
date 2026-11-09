// Command backtest 演示 K 线回测：双均线策略在 1 分钟线上交易，以 5 分钟线过滤趋势（多周期），
// 并注册一个按虚拟时间每 30 分钟记录权益的任务（回测中任务使用虚拟时钟）。
//
// 默认生成一份合成的离线数据集运行，无需网络：
//
//	go run ./examples/v1alpha3/backtest
//
// 使用天勤历史行情服务（在线预取或滚动加载）：
//
//	TQ_AUTH_USER=... TQ_AUTH_PASS=... go run ./examples/v1alpha3/backtest -online -mode rolling \
//	    -symbol SHFE.rb2601 -start 2026-09-14 -end 2026-09-18
//
// 加 -tick 同时订阅 Tick：报价改由 Tick 原样生成（不再是 K 线价格 ± price_tick），撮合按 Tick 盘口进行。
// 合成数据集中的 Tick 由 1 分钟线派生，每根 4 条。
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
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/tqapi"
)

var cst = time.FixedZone("CST", 8*3600)

func main() {
	symbol := flag.String("symbol", "SHFE.rb2601", "合约代码")
	first := flag.String("start", "2026-09-14", "首个交易日")
	last := flag.String("end", "2026-09-16", "最后交易日")
	data := flag.String("data", "", "离线数据集目录；为空且未指定 -online 时生成合成数据集")
	online := flag.Bool("online", false, "使用天勤历史行情服务（TQ_AUTH_USER / TQ_AUTH_PASS）")
	mode := flag.String("mode", "prefetch", "在线加载方式：prefetch | rolling")
	fast := flag.Int("fast", 5, "快线周期")
	slow := flag.Int("slow", 20, "慢线周期")
	tick := flag.Bool("tick", false, "同时订阅 Tick，报价与撮合由 Tick 驱动")
	flag.Parse()

	d1, err1 := time.ParseInLocation("2006-01-02", *first, cst)
	d2, err2 := time.ParseInLocation("2006-01-02", *last, cst)
	if err := errors.Join(err1, err2); err != nil {
		log.Fatal(err)
	}
	start, end := backtest.TradingDays(d1, d2)

	var provider backtest.Provider
	switch {
	case *online:
		m := backtest.Prefetch
		if *mode == "rolling" {
			m = backtest.Rolling
		}
		provider = backtest.Online(backtest.OnlineConfig{Auth: auth.NewClient(os.Getenv("TQ_AUTH_USER"), os.Getenv("TQ_AUTH_PASS")), Mode: m})
	case *data != "":
		provider = backtest.Dataset(*data)
	default:
		dir := filepath.Join(os.TempDir(), "tqsdk-go-backtest-sample")
		if err := writeSample(dir, *symbol, d1, d2); err != nil {
			log.Fatal(err)
		}
		log.Printf("合成数据集：%s", dir)
		provider = backtest.Dataset(dir)
	}

	ctx := context.Background()
	api, err := tqapi.New(ctx, tqapi.Config{
		Market:   &core.MarketConfig{},
		Account:  sim.New(sim.Config{InitBalance: 1_000_000}),
		Backtest: &backtest.Config{Start: start, End: end, Data: provider},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer api.Close()
	if err := run(ctx, api, *symbol, *fast, *slow, *tick); err != nil {
		log.Fatal(err)
	}
	report(api.Backtest.Result())
}

func run(ctx context.Context, api *tqapi.API, symbol string, fast, slow int, tick bool) error {
	account, err := api.Account()
	if err != nil {
		return err
	}
	quote, err := api.Market.Quote(ctx, symbol)
	if err != nil {
		return err
	}
	if tick { // 订阅 Tick 后，该合约的报价只由 Tick 生成
		if _, err := api.Market.Ticks(ctx, core.TickRequest{Symbol: symbol, Width: 1}); err != nil {
			return err
		}
	}
	m1, err := api.Market.Klines(ctx, core.KlineRequest{Symbol: symbol, Period: time.Minute, Width: slow + 1})
	if err != nil {
		return err
	}
	m5, err := api.Market.Klines(ctx, core.KlineRequest{Symbol: symbol, Period: 5 * time.Minute, Width: slow})
	if err != nil {
		return err
	}
	if _, err := api.RegisterTask(&equityLog{account: account, every: 30 * time.Minute}); err != nil {
		return err
	}

	var working *core.OrderRef // 未结束的委托，避免重复下单
	for {
		pulse, err := api.WaitUpdate(ctx)
		if errors.Is(err, backtest.ErrFinished) {
			return nil
		}
		if err != nil {
			return err
		}
		if working != nil {
			if o, _, _ := working.Snapshot(); o.Status == model.OrderFinished {
				working = nil
			}
		}
		// 只在新 1 分钟柱出现时判断（上一根已收盘）；行情或账户未就绪时跳过。
		if !pulse.IsChanging(m1.LatestRow(), "datetime") || working != nil || account.State() != core.Ready {
			continue
		}
		closes1, closes5 := closes(m1.Snapshot()), closes(m5.Snapshot())
		if len(closes1) < slow+1 || len(closes5) < slow {
			continue
		}
		done := closes1[:len(closes1)-1] // 不含刚开盘的新柱
		up := ma(done, fast) > ma(done, slow)
		trend := closes5[len(closes5)-1] > ma(closes5, slow)
		pos, _ := account.Position(symbol)
		q := quote.Snapshot().Value
		var req *core.OrderRequest
		switch {
		case up && trend && pos.PosLong() == 0:
			req = &core.OrderRequest{Symbol: symbol, Direction: model.DirectionBuy, Offset: model.OffsetOpen, Volume: 1, Price: core.LimitPrice(q.AskPrice1)}
		case !up && pos.PosLong() > 0:
			offset := model.OffsetClose // 上期所：平今与平昨分开
			if pos.PosLongToday > 0 {
				offset = model.OffsetCloseToday
			}
			req = &core.OrderRequest{Symbol: symbol, Direction: model.DirectionSell, Offset: offset, Volume: pos.PosLong(), Price: core.LimitPrice(q.BidPrice1)}
		}
		if req != nil {
			if working, err = account.Submit(ctx, *req); err != nil {
				return err
			}
			fmt.Printf("%s %s %s %d @ %.0f\n", api.Now().Format("2006-01-02 15:04"), req.Direction, req.Offset, req.Volume, req.Price.Value())
		}
	}
}

// equityLog 是一个按虚拟时间运行的任务：每隔 every 检查一次账户，权益变化时打印。
// WakeAt 早于下一根 K 线时，回测把虚拟时间推进到 WakeAt 执行本任务（休市时段同样如此）。
type equityLog struct {
	account *core.AccountHandle
	every   time.Duration
	next    time.Time
	last    float64
}

func (e *equityLog) Poll(in tqapi.TaskInput) (tqapi.TaskStep, error) {
	if in.Stopping {
		return tqapi.TaskStep{Done: true}, nil
	}
	if !e.next.IsZero() && !in.Now.Before(e.next) {
		if a, ok := e.account.Account(); ok && a.Balance != e.last {
			e.last = a.Balance
			fmt.Printf("%s 权益 %.2f 可用 %.2f\n", in.Now.Format("2006-01-02 15:04"), a.Balance, a.Available)
		}
	}
	for e.next.IsZero() || !in.Now.Before(e.next) {
		e.next = in.Now.Truncate(e.every).Add(e.every)
	}
	return tqapi.TaskStep{WakeAt: e.next}, nil
}

func report(r backtest.Result) {
	fmt.Printf("\n回测 %s ~ %s  %s  数据：%s  模型：%s\n", r.Start.Format("2006-01-02 15:04"), r.End.Format("2006-01-02 15:04"), r.Status, r.Source, r.Model)
	for _, a := range r.Accounts {
		for _, d := range a.Days {
			fmt.Printf("  %s 权益 %.2f 手续费 %.2f 成交 %d 笔\n", d.Date, d.Account.Balance, d.Account.Commission, len(d.Trades))
		}
		fmt.Printf("  收益率 %.2f%%  年化 %.2f%%  最大回撤 %.2f%%  夏普 %.4f  索提诺 %.4f\n",
			a.ROR*100, a.AnnualYield*100, a.MaxDrawdown*100, a.SharpeRatio, a.SortinoRatio)
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

// ---- 合成数据集 ----

// writeSample 生成 [first-3 个工作日, last] 的 1 分钟与 5 分钟合成 K 线（随机游走），前几天用于预热。
func writeSample(dir, symbol string, first, last time.Time) error {
	ins := model.Instrument{Symbol: symbol, InstrumentID: symbol, ExchangeID: "SHFE", ProductID: "rb", InsClass: "FUTURE",
		PriceTick: 1, VolumeMultiple: 10, Margin: 3500, Commission: 2,
		TradingTime: model.TradingTime{
			Day:   [][]string{{"09:00:00", "10:15:00"}, {"10:30:00", "11:30:00"}, {"13:30:00", "15:00:00"}},
			Night: [][]string{{"21:00:00", "23:00:00"}}}}
	for _, f := range []model.InstrumentField{model.InstrumentInstrumentID, model.InstrumentExchangeID, model.InstrumentProductID,
		model.InstrumentInsClass, model.InstrumentPriceTick, model.InstrumentVolumeMultiple, model.InstrumentMargin, model.InstrumentCommission} {
		ins.Valid.Add(f)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	price, oi := 3500.0, int64(100000)
	var m1, m5 []model.Kline
	for day := addWeekdays(first, -3); !day.After(last); day = addWeekdays(day, 1) {
		prev := addWeekdays(day, -1)
		sessions := [][2]time.Time{ // 夜盘属于下一交易日
			{clock(prev, 21, 0), clock(prev, 23, 0)},
			{clock(day, 9, 0), clock(day, 10, 15)}, {clock(day, 10, 30), clock(day, 11, 30)}, {clock(day, 13, 30), clock(day, 15, 0)},
		}
		for _, s := range sessions {
			for t := s[0]; t.Before(s[1]); t = t.Add(time.Minute) {
				k := model.Kline{ID: int64(len(m1)), Datetime: t.UnixNano(), Open: price, OpenOI: oi}
				hi, lo := price, price
				for range 4 {
					price = math.Round(price + rng.NormFloat64()*3)
					hi, lo = max(hi, price), min(lo, price)
				}
				oi += int64(rng.IntN(41) - 20)
				k.High, k.Low, k.Close, k.Volume, k.CloseOI = hi, lo, price, int64(50+rng.IntN(200)), oi
				m1 = append(m1, k)
				if t.Minute()%5 == 0 || len(m5) == 0 {
					m5 = append(m5, model.Kline{ID: int64(len(m5)), Datetime: t.Truncate(5 * time.Minute).UnixNano(), Open: k.Open, High: k.High, Low: k.Low, OpenOI: k.OpenOI})
				}
				b := &m5[len(m5)-1]
				b.High, b.Low, b.Close, b.Volume, b.CloseOI = max(b.High, k.High), min(b.Low, k.Low), k.Close, b.Volume+k.Volume, k.CloseOI
			}
		}
	}
	return backtest.WriteDataset(dir, "sample", []model.Instrument{ins}, []backtest.Series{
		{Symbol: symbol, Period: time.Minute, Rows: m1}, {Symbol: symbol, Period: 5 * time.Minute, Rows: m5},
		{Symbol: symbol, Ticks: ticksOf(m1)}})
}

// ticksOf 由 1 分钟线派生 Tick：每根在 0s、15s、30s、59.5s 依次经过开、高、低、收，盘口为价格与价格 + 1。
func ticksOf(bars []model.Kline) []model.Tick {
	nan := math.NaN()
	var out []model.Tick
	for _, k := range bars {
		path := []float64{k.Open, k.High, k.Low, k.Close}
		offsets := []time.Duration{0, 15 * time.Second, 30 * time.Second, 59500 * time.Millisecond}
		for i, p := range path {
			out = append(out, model.Tick{ID: int64(len(out)), Datetime: k.Datetime + int64(offsets[i]), LastPrice: p,
				AskPrice1: p + 1, AskVolume1: 10, BidPrice1: p, BidVolume1: 10, Highest: nan, Lowest: nan, Average: nan,
				AskPrice2: nan, BidPrice2: nan, AskPrice3: nan, BidPrice3: nan, AskPrice4: nan, BidPrice4: nan,
				AskPrice5: nan, BidPrice5: nan, Amount: nan, Volume: k.Volume, OpenInterest: k.CloseOI})
		}
	}
	return out
}

func clock(day time.Time, h, m int) time.Time {
	y, mo, d := day.Date()
	return time.Date(y, mo, d, h, m, 0, 0, cst)
}

func addWeekdays(t time.Time, n int) time.Time {
	step := 1
	if n < 0 {
		step, n = -1, -n
	}
	for n > 0 {
		t = t.AddDate(0, 0, step)
		if t.Weekday() != time.Saturday && t.Weekday() != time.Sunday {
			n--
		}
	}
	return t
}
