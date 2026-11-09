// Command tick 演示单合约 Tick 订阅：基线交付整窗，之后每个事件交付新到的 Tick。
// 五档盘口是独立字段（AskPrice1..5 / BidVolume1..5），价格无效时为 NaN。
//
//	TQ_AUTH_USER=... TQ_AUTH_PASS=... go run ./examples/v1alpha3/tick -symbol SHFE.rb2701 -width 5
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/auth"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/market"
)

var cst = time.FixedZone("CST", 8*3600)

func main() {
	symbol := flag.String("symbol", "SHFE.rb2701", "合约代码")
	width := flag.Int("width", 5, "窗口宽度（1..10000）")
	url := flag.String("url", "", "行情地址覆盖；仍使用 TQ_AUTH_USER / TQ_AUTH_PASS 鉴权")
	duration := flag.Duration("duration", time.Minute, "运行时长")
	flag.Parse()

	provider := auth.NewClient(os.Getenv("TQ_AUTH_USER"), os.Getenv("TQ_AUTH_PASS"))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *duration)
	defer cancel()

	md, err := market.Dial(ctx, market.Config{Auth: provider, URL: *url})
	if err != nil {
		log.Fatal(err)
	}
	defer md.Close()

	sub, err := md.SubscribeTicks(ctx, market.TickRequest{Symbol: *symbol, Width: *width})
	if err != nil {
		log.Fatal(err)
	}
	defer sub.Close()

	for {
		select {
		case e, ok := <-sub.C():
			if !ok {
				log.Fatal(sub.Err())
			}
			if e.State != core.Ready {
				fmt.Println("窗口状态:", e.State)
				continue
			}
			for _, r := range e.Rows {
				if !r.Present {
					continue
				}
				t := r.Value
				fmt.Printf("id=%d %s 最新 %v 量 %d | 买一 %v/%d 卖一 %v/%d | 买五 %v/%d 卖五 %v/%d\n", t.ID,
					time.Unix(0, t.Datetime).In(cst).Format("15:04:05.000"), t.LastPrice, t.Volume,
					t.BidPrice1, t.BidVolume1, t.AskPrice1, t.AskVolume1, t.BidPrice5, t.BidVolume5, t.AskPrice5, t.AskVolume5)
			}
		case <-ctx.Done():
			return
		}
	}
}
