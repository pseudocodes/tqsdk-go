// Command quote 演示独立行情客户端：订阅报价与 K 线、查询合约资料。
//
//	TQ_AUTH_USER=... TQ_AUTH_PASS=... go run ./examples/v1alpha3/quote -symbol SHFE.rb2701
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/auth"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/market"
)

func init() {
	log.SetFlags(log.Lshortfile | log.LstdFlags)
}

func main() {
	symbol := flag.String("symbol", "SHFE.rb2701", "合约代码")
	url := flag.String("url", "", "行情地址覆盖；仍使用 TQ_AUTH_USER / TQ_AUTH_PASS 鉴权")
	duration := flag.Int("duration", 10, "运行时长(s)")
	flag.Parse()

	provider := auth.NewClient(os.Getenv("TQ_AUTH_USER"), os.Getenv("TQ_AUTH_PASS"))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(*duration)*time.Second)
	defer cancel()

	md, err := market.Dial(ctx, market.Config{Auth: provider, URL: *url})
	if err != nil {
		log.Fatal(err)
	}
	defer md.Close()

	quotes, err := md.SubscribeQuotes(ctx, *symbol)
	if err != nil {
		log.Fatal(err)
	}
	defer quotes.Close()
	bars, err := md.SubscribeKlines(ctx, market.KlineRequest{Symbol: *symbol, Period: time.Minute, Width: 3})
	if err != nil {
		log.Fatal(err)
	}
	defer bars.Close()

	go func() {
		info, err := md.QuerySymbolInfo(ctx, []string{*symbol})
		if err != nil {
			log.Printf("query symbol info: %v", err)
			return
		}
		for _, ins := range info {
			log.Printf("合约资料 %s %s 最小变动 %v 乘数 %d\n", ins.Symbol, ins.Name, ins.PriceTick, ins.VolumeMultiple)
		}
	}()

	for {
		select {
		case e, ok := <-quotes.C():
			if !ok {
				log.Fatal(quotes.Err())
			}
			_ = e
			// q := e.Quote
			// log.Printf("报价 %s %s 最新 %v 买一 %v/%d 卖一 %v/%d\n", q.Symbol, q.Datetime, q.LastPrice, q.BidPrice1, q.BidVolume1, q.AskPrice1, q.AskVolume1)
		case e, ok := <-bars.C():
			if !ok {
				log.Fatal(bars.Err())
			}
			// 订阅默认附带完整窗口 e.Frame.Rows；e.Rows 仍是本批增量。
			if e.Frame == nil { // 未就绪或断线等状态事件不附带 Frame。
				log.Printf("K线窗口状态: %v\n", e.State)
				continue
			}
			log.Printf("K线窗口 rows=%d changed=%d shifted=%v\n", len(e.Frame.Rows), len(e.Rows), e.Shifted)
			for i, r := range e.Frame.Rows {
				if !r.Present {
					log.Printf("[%d] K线 id=%d 暂无数据\n", i, r.Value.ID)
					continue
				}
				log.Printf("[%d] K线 id=%d %s close=%v\n", i, r.Value.ID, time.Unix(0, r.Value.Datetime).Format("15:04"), r.Value.Close)
			}
		case <-ctx.Done():
			return
		}
	}
}
