// Command strategy 演示显式 WaitUpdate 的策略写法（tqapi）：只在 WaitUpdate 时推进状态，
// 用 IsChanging 判断本轮净变化。示例只读行情，不下单。
//
//	TQ_AUTH_USER=... TQ_AUTH_PASS=... go run ./examples/v1alpha3/strategy -symbol SHFE.rb2701
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/auth"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/tqapi"
)

func main() {
	symbol := flag.String("symbol", "SHFE.ag2612", "合约代码")
	url := flag.String("url", "", "行情地址覆盖；仍使用 TQ_AUTH_USER / TQ_AUTH_PASS 鉴权")
	duration := flag.Int64("duration", 10, "运行时长(s)")
	flag.Parse()

	provider := auth.NewClient(os.Getenv("TQ_AUTH_USER"), os.Getenv("TQ_AUTH_PASS"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*duration)*time.Second)
	defer cancel()

	api, err := tqapi.New(ctx, tqapi.Config{Auth: provider, Market: &core.MarketConfig{URL: *url}})
	if err != nil {
		log.Fatal(err)
	}
	defer api.Close()
	q, err := api.Market.Quote(ctx, *symbol)
	if err != nil {
		log.Fatal(err)
	}
	bars, err := api.Market.Klines(ctx, core.KlineRequest{Symbol: *symbol, Period: time.Minute, Width: 20})
	if err != nil {
		log.Fatal(err)
	}

	for {
		if _, err := api.WaitUpdate(ctx); err != nil {
			return // 超时或关闭
		}
		if api.IsChanging(q, "datetime") {
			if s := q.Snapshot(); s.State == core.Ready && s.Valid.Has(model.QuoteLastPrice) {
				fmt.Printf("%s 最新价 %v\n", s.Value.Datetime, s.Value.LastPrice)
			}
		}
		if api.IsChanging(bars.LatestRow(), "datetime") { // 首个真实末行或新柱
			f := bars.Snapshot()
			var sum float64
			n := 0
			for _, r := range f.Rows {
				if r.Present && r.Valid.Has(model.KlineClose) {
					sum += r.Value.Close
					n++
				}
			}
			if n > 0 {
				fmt.Printf("新 K 线，最近 %d 根收盘均价 %.2f\n", n, sum/float64(n))
			}
		}
	}
}
