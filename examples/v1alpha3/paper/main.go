// Command paper 演示实时模拟交易（paper trade）：实时行情驱动本地 sim 账户撮合，
// 策略写法与回测、实盘相同（WaitUpdate → IsChanging → Snapshot / Submit）。
//
// 示例以对手价开多 1 手，持有 -hold 秒后以对手价平仓，打印委托、成交与权益变化。
// 不在交易时段时委托会被拒绝（"下单失败, 不在可交易时间段内"），与 Python TqSim 一致。
//
//	TQ_AUTH_USER=... TQ_AUTH_PASS=... go run ./examples/v1alpha3/paper -symbol SHFE.rb2701 -hold 10
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
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/sim"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/tqapi"
)

func main() {
	symbol := flag.String("symbol", "SHFE.rb2701", "合约代码")
	hold := flag.Duration("hold", 10*time.Second, "开仓成交后的持有时长")
	timeout := flag.Duration("timeout", 2*time.Minute, "最长运行时间")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	api, err := tqapi.New(ctx, tqapi.Config{
		Auth:    auth.NewClient(os.Getenv("TQ_AUTH_USER"), os.Getenv("TQ_AUTH_PASS")),
		Market:  &core.MarketConfig{},
		Account: sim.New(sim.Config{InitBalance: 1_000_000}),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer api.Close()
	if err := run(ctx, api, *symbol, *hold); err != nil {
		log.Print(err)
	}
	for _, r := range api.Paper.Reports() {
		for _, d := range r.Days {
			fmt.Printf("日结 %s 权益 %.2f 手续费 %.2f 成交 %d 笔\n", d.Date, d.Account.Balance, d.Account.Commission, len(d.Trades))
		}
	}
}

func run(ctx context.Context, api *tqapi.API, symbol string, hold time.Duration) error {
	account, err := api.Account()
	if err != nil {
		return err
	}
	// 账户在第一笔行情确定交易日后 Ready，因此先订阅行情。
	quote, err := api.Market.Quote(ctx, symbol)
	if err != nil {
		return err
	}
	accRef := account.AccountRef("")
	var order *core.OrderRef
	var closeAt time.Time // 开仓成交后计划平仓的时间
	closing, waiting := false, false
	for {
		pulse, err := api.WaitUpdate(ctx)
		if err != nil {
			return err
		}
		for _, n := range pulse.Notifies(account) {
			fmt.Println("通知:", n.Content)
		}
		if order != nil && pulse.IsChanging(order) {
			o, _, _ := order.Snapshot()
			fmt.Printf("委托 %s %s %s %d @ %.0f：%s %s\n", o.OrderID, o.Direction, o.Offset, o.VolumeOriginal, o.LimitPrice, o.Status, o.LastMsg)
		}
		if a, ok := account.Account(); ok && pulse.IsChanging(accRef, "balance") {
			fmt.Printf("%s 权益 %.2f 可用 %.2f 持仓盈亏 %.2f\n", quote.Snapshot().Value.Datetime, a.Balance, a.Available, a.PositionProfit)
		}

		q := quote.Snapshot()
		if account.State() != core.Ready || q.State != core.Ready {
			continue
		}
		if !q.Valid.Has(model.QuoteLastPrice) { // 收盘后到下一时段开盘前，行情可能没有有效价格
			if !waiting {
				fmt.Printf("%s 行情暂无有效价格，等待开盘\n", q.Value.Datetime)
				waiting = true
			}
			continue
		}
		// 对手价；非交易时段盘口为空时用最新价（sim 会以不在交易时段拒单）。
		ask, bid := q.Value.AskPrice1, q.Value.BidPrice1
		if !q.Valid.Has(model.QuoteAskPrice1) || !q.Valid.Has(model.QuoteBidPrice1) {
			ask, bid = q.Value.LastPrice, q.Value.LastPrice
		}
		if order != nil {
			if o, _, _ := order.Snapshot(); o.Status != model.OrderFinished {
				continue // 等待委托结束
			}
		}
		pos, _ := account.Position(symbol)
		switch {
		case order == nil: // 开仓
			order, err = account.Submit(ctx, core.OrderRequest{Symbol: symbol, Direction: model.DirectionBuy, Offset: model.OffsetOpen,
				Volume: 1, Price: core.LimitPrice(ask)})
		case closing: // 平仓委托已结束
			a, _ := account.Account()
			fmt.Printf("结束：平仓盈亏 %.2f 手续费 %.2f 权益 %.2f\n", a.CloseProfit, a.Commission, a.Balance)
			return nil
		case pos.PosLong() == 0: // 开仓未成交（如不在交易时段）
			return nil
		case closeAt.IsZero():
			closeAt = time.Now().Add(hold)
			fmt.Printf("开仓成交，%v 后平仓\n", hold)
		case time.Now().After(closeAt):
			offset := model.OffsetClose // 上期所、上期能源区分平今与平昨
			if pos.PosLongToday > 0 {
				offset = model.OffsetCloseToday
			}
			order, err = account.Submit(ctx, core.OrderRequest{Symbol: symbol, Direction: model.DirectionSell, Offset: offset,
				Volume: pos.PosLong(), Price: core.LimitPrice(bid)})
			closing = true
		}
		if err != nil {
			return err
		}
	}
}
