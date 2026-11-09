// Command kline 演示单合约 K 线订阅。
//
// 事件语义：
//
//   - Window.ViewVersion 变化：新基线（首次就绪、重连恢复），Rows 为整窗；
//
//   - Shifted=true 且 Rows 含多行：出现新柱，ID 小于窗口右端的行是旧柱的最终值；
//
//   - 其余事件只更新本批相关行（通常是最后一根）。
//
//     TQ_AUTH_USER=... TQ_AUTH_PASS=... go run ./examples/v1alpha3/kline -symbol SHFE.rb2701 -period 1m -width 10
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
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

var cst = time.FixedZone("CST", 8*3600)

func main() {
	symbol := flag.String("symbol", "SHFE.rb2701", "合约代码")
	period := flag.Duration("period", time.Minute, "K 线周期")
	width := flag.Int("width", 10, "窗口宽度（1..10000）")
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

	sub, err := md.SubscribeKlines(ctx, market.KlineRequest{Symbol: *symbol, Period: *period, Width: *width})
	if err != nil {
		log.Fatal(err)
	}
	defer sub.Close()

	var version uint64
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
			right := e.Window.Range.RightID
			if e.Window.ViewVersion != version {
				version = e.Window.ViewVersion
				fmt.Printf("基线 v%d 范围 %+v 真实行 %d/%d\n", version, e.Window.Range, e.Window.ValidCount, *width)
				for _, r := range e.Rows {
					printBar("  ", r)
				}
				continue
			}
			for _, r := range e.Rows {
				switch {
				case !r.Present:
				case r.Value.ID < right:
					printBar("定稿", r)
				case e.Shifted:
					printBar("新柱", r)
				default:
					printBar("更新", r)
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func printBar(tag string, r core.KlineSlot) {
	if !r.Present {
		return
	}
	k := r.Value
	fmt.Printf("%s id=%d %s O=%s H=%s L=%s C=%s V=%d OI=%d\n", tag, k.ID,
		time.Unix(0, k.Datetime).In(cst).Format("2006-01-02 15:04:05"),
		price(k.Open, r.Valid.Has(model.KlineOpen)), price(k.High, r.Valid.Has(model.KlineHigh)),
		price(k.Low, r.Valid.Has(model.KlineLow)), price(k.Close, r.Valid.Has(model.KlineClose)), k.Volume, k.CloseOI)
}

func price(v float64, valid bool) string {
	if !valid {
		return "-"
	}
	return fmt.Sprint(v)
}
