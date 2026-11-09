// Command multikline 演示多合约对齐 K 线，对齐关系来自服务端 binding。
//
//   - matched（默认，同 Python get_kline_serial 传入合约列表）：只输出所有腿都有数据的主 ID，取最近 Width 个；
//
//   - primary：按主合约逻辑 ID 输出，副合约缺失时该腿 Present=false。
//
//     TQ_AUTH_USER=... TQ_AUTH_PASS=... go run ./examples/v1alpha3/multikline -symbols SHFE.rb2701,SHFE.hc2701 -period 1m
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/auth"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/market"
)

var cst = time.FixedZone("CST", 8*3600)

func main() {
	symbols := flag.String("symbols", "SHFE.rb2701,SHFE.hc2701", "合约列表，第一个为主合约")
	period := flag.Duration("period", time.Minute, "K 线周期")
	width := flag.Int("width", 10, "输出行数")
	mode := flag.String("mode", "matched", "matched | primary")
	url := flag.String("url", "", "行情地址覆盖；仍使用 TQ_AUTH_USER / TQ_AUTH_PASS 鉴权")
	duration := flag.Duration("duration", time.Minute, "运行时长")
	flag.Parse()

	provider := auth.NewClient(os.Getenv("TQ_AUTH_USER"), os.Getenv("TQ_AUTH_PASS"))
	align := core.MatchedRows
	if *mode == "primary" {
		align = core.PrimarySlots
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *duration)
	defer cancel()

	md, err := market.Dial(ctx, market.Config{Auth: provider, URL: *url})
	if err != nil {
		log.Fatal(err)
	}
	defer md.Close()

	list := strings.Split(*symbols, ",")
	sub, err := md.SubscribeAlignedKlines(ctx, market.AlignedKlineRequest{Symbols: list, Period: *period, Width: *width, Mode: align})
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
			if e.Shifted {
				fmt.Printf("位置表变化，完整匹配 %d 行，RowKeys=%v\n", e.Window.ValidCount, e.RowKeys)
			}
			for _, r := range e.Rows {
				if r.PrimaryID < 0 { // MatchedRows 的前置占位
					continue
				}
				primary := r.Legs[0]
				line := fmt.Sprintf("主ID=%d %s", r.PrimaryID, time.Unix(0, primary.Value.Datetime).In(cst).Format("01-02 15:04"))
				for i, leg := range r.Legs {
					if leg.Present {
						line += fmt.Sprintf(" | %s id=%d close=%v", list[i], leg.Value.ID, leg.Value.Close)
					} else {
						line += fmt.Sprintf(" | %s 缺失", list[i])
					}
				}
				if len(r.Legs) == 2 && r.Legs[0].Present && r.Legs[1].Present {
					line += fmt.Sprintf(" | 价差 %.2f", r.Legs[0].Value.Close-r.Legs[1].Value.Close)
				}
				fmt.Println(line)
			}
		case <-ctx.Done():
			return
		}
	}
}
