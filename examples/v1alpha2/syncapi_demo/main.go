// syncapi_demo demonstrates the synchronous wait_update / is_changing
// programming model provided by the syncapi package.
//
// This mirrors the Python tqsdk pattern:
//
//	api = TqApi()
//	quote = api.get_quote("SHFE.rb2510")
//	klines = api.get_kline_serial("SHFE.rb2510", 60)
//	while True:
//	    api.wait_update()
//	    if api.is_changing(quote):
//	        print(quote.last_price)
//	    if api.is_changing(klines.iloc[-1], "datetime"):
//	        print("new bar!")
//
// Usage:
//
//	SHINNYTECH_ID=xxx SHINNYTECH_PW=yyy go run ./examples/syncapi_demo
//	SHINNYTECH_ID=xxx SHINNYTECH_PW=yyy go run ./examples/syncapi_demo -symbol SHFE.ag2512 -duration 300
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/app"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/syncapi"
)

var (
	symbol      = flag.String("symbol", "KQ.m@SHFE.ag", "symbol to subscribe")
	durationSec = flag.Int("duration", 60, "kline duration in seconds")
	dataLength  = flag.Int("length", 200, "kline data length")
	maxRounds   = flag.Int("rounds", 50, "max WaitUpdate rounds (0 = unlimited)")
)

func main() {
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	userID := os.Getenv("SHINNYTECH_ID")
	password := os.Getenv("SHINNYTECH_PW")
	if userID == "" || password == "" {
		log.Fatal("set SHINNYTECH_ID and SHINNYTECH_PW environment variables")
	}

	// ── 1. Create client (same as other examples) ──────────────
	cli, err := app.NewDefaultClient(app.Config{
		User:     userID,
		Password: password,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = cli.Close() }()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := cli.Run(ctx); err != nil {
		log.Fatal(err)
	}

	// ── 2. Create SyncApi ──────────────────────────────────────
	//
	// Counterpart: Python api = TqApi()
	sa := syncapi.NewFromClient(cli)
	defer sa.Close()

	// ── 3. Subscribe data — returns live Refs ──────────────────
	//
	// Counterpart:
	//   quote  = api.get_quote("SHFE.rb2510")
	//   klines = api.get_kline_serial("SHFE.rb2510", 60)
	quote, err := sa.GetQuote(ctx, *symbol)
	if err != nil {
		log.Fatalf("GetQuote: %v", err)
	}

	klines, err := sa.GetKlineSeries(ctx, *symbol, *durationSec, *dataLength)
	if err != nil {
		log.Fatalf("GetKlineSeries: %v", err)
	}

	fmt.Printf("subscribed: symbol=%s duration=%ds length=%d\n", *symbol, *durationSec, *dataLength)
	fmt.Println("waiting for market data ...")

	// ── 4. Main loop: WaitUpdate + IsChanging ──────────────────
	//
	// Counterpart:
	//   while True:
	//       api.wait_update()
	//       if api.is_changing(quote): ...
	//       if api.is_changing(klines.iloc[-1], "datetime"): ...
	round := 0
	for {
		if err := sa.WaitUpdateContext(ctx); err != nil {
			log.Printf("WaitUpdate ended: %v", err)
			break
		}
		round++

		// ── Object-level: did the quote change at all? ─────────
		// Counterpart: if api.is_changing(quote):
		if sa.IsChanging(quote) {
			q := quote.Get()
			fmt.Printf("[quote] %s  last=%.1f  bid1=%.1f×%d  ask1=%.1f×%d  vol=%d  time=%s\n",
				*symbol, q.LastPrice,
				q.BidPrice1, q.BidVolume1,
				q.AskPrice1, q.AskVolume1,
				q.Volume, q.Datetime)
		}

		// ── Series-level: did any kline bar update? ────────────
		// Counterpart: if api.is_changing(klines):
		if sa.IsChanging(klines) {
			frame := klines.Get()
			if len(frame) > 0 {
				last := frame[len(frame)-1]
				fmt.Printf("[kline] bars=%d  last: O=%.1f H=%.1f L=%.1f C=%.1f V=%d  dt=%s\n",
					len(frame), last.Open, last.High, last.Low, last.Close, last.Volume,
					time.Unix(0, last.Datetime).Format("2006-01-02 15:04:05"))
			}
		}

		// ── Bar-level: did a NEW kline bar appear? ─────────────
		// Counterpart: if api.is_changing(klines.iloc[-1], "datetime"):
		if sa.IsChanging(klines.Bar(-1), "datetime") {
			bar := klines.Bar(-1).Get()
			if bar != nil {
				fmt.Printf("[new bar!] id=%d  dt=%s  O=%.1f\n",
					bar.ID, time.Unix(0, bar.Datetime).Format("15:04:05"), bar.Open)
			}
		}

		if *maxRounds > 0 && round >= *maxRounds {
			fmt.Printf("reached %d rounds, exiting\n", *maxRounds)
			break
		}
	}
}
