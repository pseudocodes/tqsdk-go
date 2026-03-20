package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/app"
)

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

var (
	symbol       = flag.String("symbol", "KQ.m@SHFE.rb", "main symbol for tick/kline/get")
	quoteSymbols = flag.String("quote-symbols", "KQ.m@SHFE.rb,KQ.m@SHFE.ag", "quote subscription symbols")
	klineSymbols = flag.String("kline-symbols", "KQ.m@SHFE.rb,KQ.m@SHFE.ag", "kline subscription symbols")
	durationSec  = flag.Int("duration-sec", 60, "kline duration seconds")
	dataLength   = flag.Int("data-length", 2000, "subscription data length")
	anchorNS     = flag.Int64("anchor-ns", 1770598800000000000, "data fetch anchor unix ns (default: 2026-02-09 09:00:00+08:00)")
	runCase      = flag.String("case", "all", "which case to run: all,quote,tick,kline,get-ticks,get-tick-ds,get-klines,downloader,get-kline-ds")
)

func main() {
	flag.Parse()

	user := os.Getenv("SHINNYTECH_ID")
	password := os.Getenv("SHINNYTECH_PW")
	if user == "" || password == "" {
		log.Fatal("please set SHINNYTECH_ID and SHINNYTECH_PW environment variables")
	}

	cli, err := app.NewDefaultClient(app.Config{
		User:     user,
		Password: password,
	})
	if err != nil {
		panic(err)
	}
	defer func() { _ = cli.Close() }()

	runCtx, runCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer runCancel()
	if err := cli.Run(runCtx); err != nil {
		panic(err)
	}
	m := cli.Market()

	fmt.Println("== Market Demo Start ==")
	fmt.Printf("symbol=%s duration=%ds dataLength=%d case=%s\n", *symbol, *durationSec, *dataLength, *runCase)

	start := time.Date(2025, 01, 02, 9, 0, 0, 0, time.Local)
	end := time.Date(2025, 12, 31, 15, 0, 0, 0, time.Local)

	// cases := parseCases(*runCase)

	// if cases["quote"] {
	// 	caseSubscribeQuotes(m)
	// }
	// if cases["tick"] {
	// 	caseSubscribeTicks(m)
	// }
	// if cases["kline"] {
	// 	caseSubscribeKlines(m)
	// }

	// anchor := time.Unix(0, *anchorNS)
	// start := anchor.Add(-90 * time.Minute)
	// end := anchor.Add(30 * time.Minute)

	// if cases["get-ticks"] {
	// caseGetTicks(m, start, end)
	// }
	// if cases["get-tick-ds"] {
	// caseGetTickDataSeries(m, start, end)
	// }
	// if cases["get-klines"] {
	// caseGetKlines(m, start, end)
	// }
	// if cases["downloader"] {
	// 	caseKlineDownloader(m, start, end)
	// }
	// if cases["get-kline-ds"] {
	// 	caseGetKlineDataSeries(m, start, end)
	// }

	// caseKlineDownloader(m, start, end)
	caseTickDownloader(m, start, end)

	fmt.Println("== Market Demo Done ==")
}

// ---------------------------------------------------------------------------
// Individual cases
// ---------------------------------------------------------------------------

func caseSubscribeQuotes(m api.MarketService) {
	qs := splitCSV(*quoteSymbols)
	if len(qs) == 0 {
		qs = []string{*symbol}
	}
	quoteSub, err := m.SubscribeQuotes(context.Background(), qs...)
	if err != nil {
		panic(err)
	}
	defer quoteSub.Close()
	waitQuoteEvent(quoteSub.C(), 8*time.Second)
	if q, ok := m.Quote(qs[0]); ok {
		fmt.Printf("[Quote] symbol=%s last=%.3f bid1=%.3f ask1=%.3f datetime=%s\n", qs[0], q.LastPrice, q.BidPrice1, q.AskPrice1, q.Datetime)
	}
}

func caseSubscribeTicks(m api.MarketService) {
	tickSub, err := m.SubscribeTicks(context.Background(), *symbol, *dataLength)
	if err != nil {
		panic(err)
	}
	defer tickSub.Close()
	waitTickEvent(tickSub.C(), 10*time.Second)
	if frame, ok := m.TickFrame(*symbol); ok {
		fmt.Printf("[TickFrame] symbol=%s len=%d lastID=%d\n", *symbol, len(frame), lastTickID(frame))
	}
}

func caseSubscribeKlines(m api.MarketService) {
	ks := splitCSV(*klineSymbols)
	log.Printf("ks len %d\n", len(ks))
	if len(ks) == 0 {
		ks = []string{*symbol}
	}
	klineSub, err := m.SubscribeKlines(
		context.Background(), ks, *durationSec, *dataLength,
		api.WithKlineAlignMode(api.AlignMainWithNA),
	)
	if err != nil {
		panic(err)
	}
	defer klineSub.Close()
	waitKlineEvent(klineSub.C(), 10*time.Second)
}

func caseGetTicks(m api.MarketService, start, end time.Time) {
	res, err := m.GetTicks(context.Background(), api.TickGetReq{
		Symbol: *symbol, Count: 1200, Start: &start, End: &end, ViewWidth: 2000,
	})
	if err != nil {
		log.Printf("[GetTicks] err=%v complete=%v reason=%s data_len=%d\n", err, res.Complete, res.Reason, len(res.Data))
	} else {
		log.Printf("[GetTicks] complete=%v reason=%s data_len=%d frame_len=%d\n", res.Complete, res.Reason, len(res.Data), len(res.Frame))
		log.Printf("[GetTicks] symbol=%s left_tt=[%v]:%d, right_tt=[%v]:%d\n", *symbol,
			time.Unix(0, res.Data[0].Datetime).String(), res.Data[0].ID,
			time.Unix(0, res.Data[len(res.Data)-1].Datetime).String(), res.Data[len(res.Data)-1].ID)
	}
}

func caseGetTickDataSeries(m api.MarketService, start, end time.Time) {
	res, err := m.GetTickDataSeries(context.Background(), *symbol, start, end)
	if err != nil {
		log.Printf("[GetTickDataSeries] err=%v complete=%v reason=%s data_len=%d\n", err, res.Complete, res.Reason, len(res.Data))
	} else {
		log.Printf("[GetTickDataSeries] complete=%v reason=%s data_len=%d\n", res.Complete, res.Reason, len(res.Data))
		log.Printf("[GetTickDataSeries] symbol=%s left_tt=[%v], right_tt=[%v]\n", *symbol,
			time.Unix(0, res.Data[0].Datetime).String(), time.Unix(0, res.Data[len(res.Data)-1].Datetime).String())
	}
}

func caseGetKlines(m api.MarketService, start, end time.Time) {
	res, err := m.GetKlines(context.Background(), api.KlineGetReq{
		Symbols: []string{*symbol}, DurationSeconds: *durationSec,
		Count: 600, Start: &start, End: &end, ViewWidth: 2000,
	})
	if err != nil {
		log.Printf("[GetKlines] err=%v complete=%v reason=%s frame1d_len=%d rows=%d\n", err, res.Complete, res.Reason, len(res.Frame1D), len(res.Rows))
	} else {
		log.Printf("[GetKlines] complete=%v reason=%s frame1d_len=%d rows=%d\n", res.Complete, res.Reason, len(res.Frame1D), len(res.Rows))
		log.Printf("[GetKlines] symbol=%s left_tt=[%v]:%d, right_tt=[%v]:%d\n", *symbol,
			time.Unix(0, res.Frame1D[0].Datetime).String(), res.Frame1D[0].ID,
			time.Unix(0, res.Frame1D[len(res.Frame1D)-1].Datetime).String(), res.Frame1D[len(res.Frame1D)-1].ID)
	}
}

func caseKlineDownloader(m api.MarketService, start, end time.Time) {
	iter, err := m.NewKlineDownloader(context.Background(), api.KlineDownloadReq{
		Symbols: []string{*symbol}, DurationSeconds: *durationSec,
		Start: start, End: end, ViewWidth: 10000, AlignMode: api.AlignStrictByBinding,
	})
	if err != nil {
		fmt.Printf("[NewKlineDownloader] err=%v\n", err)
		return
	}
	defer iter.Close()
	for i := 1; ; i++ {
		batch, ok, err := iter.Next(context.Background())
		if err != nil {
			fmt.Printf("[KlineDownloader] page=%d err=%v\n", i, err)
			break
		}
		if !ok {
			fmt.Printf("[KlineDownloader] page=%d no_more\n", i)
			break
		}
		fmt.Printf("[KlineDownloader] page=%d frame1d_len=%d left=%d right=%d more=%v\n", i, len(batch.Frame1D), batch.LeftID, batch.RightID, batch.MoreData)
		log.Printf("[KlineDownloader] symbol=%s left=[%v], right=[%v]\n", *symbol, time.Unix(0, batch.Frame1D[0].Datetime).String(), time.Unix(0, batch.Frame1D[len(batch.Frame1D)-1].Datetime).String())
	}
}

func caseTickDownloader(m api.MarketService, start, end time.Time) {
	iter, err := m.NewTickDownloader(context.Background(), api.TickDownloadReq{
		Symbol: *symbol, Start: start, End: end, ViewWidth: 3000,
	})
	if err != nil {
		fmt.Printf("[NewTickDownloader] err=%v\n", err)
		return
	}
	defer iter.Close()
	for i := 1; ; i++ {
		batch, ok, err := iter.Next(context.Background())
		if err != nil {
			fmt.Printf("[TickDownloader] page=%d err=%v\n", i, err)
			break
		}
		if !ok {
			fmt.Printf("[TickDownloader] page=%d no_more\n", i)
			break
		}
		fmt.Printf("[TickDownloader] page=%d frame_len=%d left=%d right=%d more=%v\n", i, len(batch.Frame), batch.LeftID, batch.RightID, batch.MoreData)
		log.Printf("[TickDownloader] symbol=%s left=[%v], right=[%v]\n", *symbol, time.Unix(0, batch.Frame[0].Datetime).String(), time.Unix(0, batch.Frame[len(batch.Frame)-1].Datetime).String())
	}
}

func caseGetKlineDataSeries(m api.MarketService, start, end time.Time) {
	res, err := m.GetKlineDataSeries(context.Background(), *symbol, *durationSec, start, end)
	if err != nil {
		fmt.Printf("[GetKlineDataSeries] err=%v complete=%v reason=%s data_len=%d\n", err, res.Complete, res.Reason, len(res.Data))
	} else {
		fmt.Printf("[GetKlineDataSeries] complete=%v reason=%s data_len=%d\n", res.Complete, res.Reason, len(res.Data))
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func parseCases(raw string) map[string]bool {
	all := []string{"quote", "tick", "kline", "get-ticks", "get-tick-ds", "get-klines", "downloader", "get-kline-ds"}
	m := make(map[string]bool, len(all))
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "all" {
		for _, c := range all {
			m[c] = true
		}
		return m
	}
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			m[p] = true
		}
	}
	return m
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func waitQuoteEvent(ch <-chan api.QuoteEvent, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	select {
	case ev, ok := <-ch:
		if !ok {
			fmt.Println("[SubscribeQuotes] channel closed")
			return
		}
		fmt.Printf("[SubscribeQuotes] quote=%s last=%.3f changed_at=%s\n", ev.Quote.InstrumentID, ev.Quote.LastPrice, ev.ChangedAt.Format(time.RFC3339))
	case <-ctx.Done():
		fmt.Printf("[SubscribeQuotes] timeout=%v\n", ctx.Err())
	}
}

func waitTickEvent(ch <-chan api.TickEvent, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	select {
	case ev, ok := <-ch:
		if !ok {
			fmt.Println("[SubscribeTicks] channel closed")
			return
		}
		fmt.Printf("[SubscribeTicks] kind=%s serial_ready=%v frame_full=%v valid=%d frame_len=%d last_id=%d\n",
			ev.Kind, ev.SerialReady, ev.FrameFull, ev.ValidCount, len(ev.Frame), ev.Tick.ID)
	case <-ctx.Done():
		fmt.Printf("[SubscribeTicks] timeout=%v\n", ctx.Err())
	}
}

func waitKlineEvent(ch <-chan api.KlineEvent, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	select {
	case ev, ok := <-ch:
		if !ok {
			fmt.Println("[SubscribeKlines] channel closed")
			return
		}
		log.Printf("[SubscribeKlines] kind=%s serial_ready=%v frame_full=%v valid=%d frame1d=%d frame2d=%d main_id=%d\n",
			ev.Kind, ev.SerialReady, ev.FrameFull, ev.ValidCount, len(ev.Frame1D), len(ev.Frame2D), ev.Row.MainID)
	case <-ctx.Done():
		log.Printf("[SubscribeKlines] timeout=%v\n", ctx.Err())
	}
}

func lastTickID(frame []*api.Tick) int64 {
	var last int64
	for _, t := range frame {
		if t != nil {
			last = t.ID
		}
	}
	return last
}
