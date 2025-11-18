package main

import (
	"context"
	"fmt"
	"os"
	"time"

	tqsdk "github.com/pseudocodes/tqsdk-go"
)

// QuoteSubscriptionExample Quote 订阅示例
func QuoteSubscriptionExample() {
	ctx := context.Background()

	username := os.Getenv("SHINNYTECH_ID")
	password := os.Getenv("SHINNYTECH_PW")

	// 创建客户端
	client, err := tqsdk.NewClient(ctx, username, password,
		tqsdk.WithLogLevel("info"), // 启用调试日志
		tqsdk.WithViewWidth(500),
		tqsdk.WithDevelopment(true),
	)
	if err != nil {
		fmt.Printf("创建客户端失败: %v\n", err)
		return
	}
	defer client.Close()

	// 初始化行情功能
	if err := client.InitMarket(); err != nil {
		fmt.Printf("初始化行情功能失败: %v\n", err)
		return
	}

	fmt.Println("==================== Quote 订阅示例 ====================")

	fmt.Println("等待客户端初始化完成...")
	time.Sleep(2 * time.Second)

	// 示例 1: 使用流式接口
	fmt.Println("开始订阅合约...")
	quoteSub, err := client.SubscribeQuote(ctx, "SHFE.au2512", "SHFE.ag2512", "DCE.m2512")
	if err != nil {
		fmt.Printf("订阅失败: %v\n", err)
		return
	}
	defer quoteSub.Close()

	// 方式 1: 使用 Channel
	fmt.Println("开始监听 Quote 数据...")
	go func() {
		count := 0
		for quote := range quoteSub.QuoteChannel() {
			count++
			fmt.Printf("收到 Quote 更新 #%d: %s\n", count, quote.InstrumentID)

			// 用户自行过滤合约（注意：需要使用完整的合约代码）
			if quote.InstrumentID == "SHFE.au2512" {
				fmt.Printf("📊 黄金: 最新价=%.2f, 涨跌=%.2f, 买一=%.2f, 卖一=%.2f\n",
					quote.LastPrice, quote.Change, quote.BidPrice1, quote.AskPrice1)
			}
		}
		fmt.Println("Quote Channel 已关闭")
	}()

	// 方式 2: 使用回调接口（输出所有合约以便调试）
	quoteSub.OnQuote(func(quote *tqsdk.Quote) {
		if quote.InstrumentID == "SHFE.ag2512" {
			fmt.Printf("📊 白银: 最新价=%.2f, 涨跌=%.2f, 买一=%.2f, 卖一=%.2f\n", quote.LastPrice, quote.Change, quote.BidPrice1, quote.AskPrice1)
		}
		fmt.Printf("[回调] %s: 最新价=%.2f\n", quote.InstrumentID, quote.LastPrice)
	})

	// 运行 30 秒
	time.Sleep(30 * time.Second)
	fmt.Println("Quote 订阅示例结束")
}

// SingleKlineSubscriptionExample 单合约 K线订阅示例
func SingleKlineSubscriptionExample() {
	ctx := context.Background()

	username := os.Getenv("SHINNYTECH_ID")
	password := os.Getenv("SHINNYTECH_PW")

	client, err := tqsdk.NewClient(ctx, username, password,
		tqsdk.WithLogLevel("info"),
	)
	if err != nil {
		fmt.Printf("创建客户端失败: %v\n", err)
		return
	}
	defer client.Close()

	// 初始化行情功能
	if err := client.InitMarket(); err != nil {
		fmt.Printf("初始化行情功能失败: %v\n", err)
		return
	}

	fmt.Println("==================== 单合约 K线订阅示例 ====================")

	// 订阅 1分钟 K线
	sub, err := client.Series().Kline(ctx, "SHFE.au2512", 60*time.Second, 5)
	if err != nil {
		fmt.Printf("订阅失败: %v\n", err)
		return
	}
	defer sub.Close()

	// 方式 1: 使用通用更新回调（包含更新信息）
	// sub.OnUpdate(func(data *tqsdk.SeriesData, info *tqsdk.UpdateInfo) {
	// 	symData := data.GetSymbolKlines("SHFE.cu2501")

	// 	if info.HasNewBar {
	// 		// 新增了一根 K线
	// 		fmt.Printf("🆕 新 K线! ID=%d, 数据量=%d\n",
	// 			info.NewBarIDs["SHFE.cu2501"],
	// 			len(symData.Data))

	// 		if len(symData.Data) > 0 {
	// 			latest := symData.Data[len(symData.Data)-1]
	// 			fmt.Printf("   时间=%s O:%.2f H:%.2f L:%.2f C:%.2f V:%d\n",
	// 				time.Unix(0, latest.Datetime).Format("15:04:05"),
	// 				latest.Open, latest.High, latest.Low, latest.Close, latest.Volume)
	// 		}
	// 	}

	// 	if info.HasBarUpdate && !info.HasNewBar {
	// 		// 更新了最后一根 K线（盘中实时更新）
	// 		fmt.Printf("🔄 K线更新 (LastID=%d)\n", symData.LastID)

	// 		if len(symData.Data) > 0 {
	// 			latest := symData.Data[len(symData.Data)-1]
	// 			fmt.Printf("   当前价:%.2f (L:%.2f H:%.2f V:%d)\n",
	// 				latest.Close, latest.Low, latest.High, latest.Volume)
	// 		}
	// 	}

	// 	if info.ChartRangeChanged {
	// 		fmt.Printf("📊 Chart 范围变化: [%d,%d] -> [%d,%d]\n",
	// 			info.OldLeftID, info.OldRightID,
	// 			info.NewLeftID, info.NewRightID)
	// 	}

	// 	if info.HasChartSync {
	// 		fmt.Printf("✅ Chart 同步完成! 范围: [%d,%d]\n",
	// 			data.Single.Chart.LeftID, data.Single.Chart.RightID)
	// 	}
	// })

	// 方式 2: 使用专门的新 K线回调（传递完整序列数据，便于计算指标）
	sub.OnNewBar(func(data *tqsdk.SeriesData) {
		symData := data.GetSymbolKlines("SHFE.au2512")
		if len(symData.Data) > 0 {
			latest := symData.Data[len(symData.Data)-1]
			fmt.Printf("🎯 新 K线: [%d] C=%.2f V=%d (序列长度=%d)\n",
				latest.ID, latest.Close, latest.Volume, len(symData.Data))

			// 示例：可以用完整序列数据计算技术指标
			// 如：计算最近5根K线的平均价格
			if len(symData.Data) >= 5 {
				sum := 0.0
				for i := len(symData.Data) - 5; i < len(symData.Data); i++ {
					sum += symData.Data[i].Close
				}
				ma5 := sum / 5
				fmt.Printf("   MA5=%.2f\n", ma5)
			}
		}
	})

	// 方式 3: 使用 K线更新回调（盘中实时）
	sub.OnBarUpdate(func(data *tqsdk.SeriesData) {
		symData := data.GetSymbolKlines("SHFE.cu2501")
		if len(symData.Data) > 0 {
			latest := symData.Data[len(symData.Data)-1]
			fmt.Printf("⏰ K线更新: [%d] C=%.2f (实时)\n",
				latest.ID, latest.Close)
		}
	})

	// 运行 50 秒
	time.Sleep(50 * time.Second)
	fmt.Println("单合约 K线订阅示例结束")
}

// MultiKlineSubscriptionExample 多合约 K线订阅示例
func MultiKlineSubscriptionExample() {
	ctx := context.Background()

	username := os.Getenv("SHINNYTECH_ID")
	password := os.Getenv("SHINNYTECH_PW")

	client, err := tqsdk.NewClient(ctx, username, password,
		tqsdk.WithLogLevel("info"),
	)
	if err != nil {
		fmt.Printf("创建客户端失败: %v\n", err)
		return
	}
	defer client.Close()

	// 初始化行情功能
	if err := client.InitMarket(); err != nil {
		fmt.Printf("初始化行情功能失败: %v\n", err)
		return
	}

	fmt.Println("==================== 多合约 K线订阅示例 ====================")

	// 订阅多个合约的 1分钟 K线
	sub, err := client.Series().KlineMulti(ctx,
		[]string{"SHFE.au2512", "SHFE.ag2512", "INE.sc2601"},
		time.Minute, 10)
	if err != nil {
		fmt.Printf("订阅失败: %v\n", err)
		return
	}
	defer sub.Close()

	sub.OnUpdate(func(data *tqsdk.SeriesData, info *tqsdk.UpdateInfo) {
		if info.HasNewBar {
			fmt.Printf("\n🆕 新 K线产生!\n")
			for symbol, barID := range info.NewBarIDs {
				fmt.Printf("  - %s: ID=%d\n", symbol, barID)
			}

			// 显示对齐的数据
			if len(data.Multi.Data) > 0 {
				latest := data.Multi.Data[len(data.Multi.Data)-1]
				fmt.Printf("\n时间: %s (MainID=%d)\n",
					latest.Timestamp.Format("15:04:05"), latest.MainID)

				for symbol, kline := range latest.Klines {
					fmt.Printf("  %s: O=%.2f C=%.2f H=%.2f L=%.2f V=%d\n",
						symbol,
						kline.Open, kline.Close,
						kline.High, kline.Low,
						kline.Volume)
				}
			}
		}

		if info.HasChartSync {
			fmt.Printf("✅ 多合约 Chart 同步完成!\n")
			fmt.Printf("主合约: %s, 合约数: %d\n",
				data.Multi.MainSymbol, len(data.Multi.Symbols))
			fmt.Printf("数据范围: [%d, %d], 总共 %d 根K线\n",
				data.Multi.LeftID, data.Multi.RightID, len(data.Multi.Data))
		}
	})

	// 运行 30 秒
	time.Sleep(30 * time.Second)
	fmt.Println("多合约 K线订阅示例结束")
}

// TickSubscriptionExample Tick 订阅示例
func TickSubscriptionExample() {
	ctx := context.Background()

	username := os.Getenv("SHINNYTECH_ID")
	password := os.Getenv("SHINNYTECH_PW")

	client, err := tqsdk.NewClient(ctx, username, password,
		tqsdk.WithLogLevel("info"),
		tqsdk.WithDevelopment(true),
	)
	if err != nil {
		fmt.Printf("创建客户端失败: %v\n", err)
		return
	}
	defer client.Close()

	// 初始化行情功能
	if err := client.InitMarket(); err != nil {
		fmt.Printf("初始化行情功能失败: %v\n", err)
		return
	}

	fmt.Println("==================== Tick 订阅示例 ====================")

	// 订阅 Tick
	sub, err := client.Series().Tick(ctx, "SHFE.au2512", 5)
	if err != nil {
		fmt.Printf("订阅失败: %v\n", err)
		return
	}
	defer sub.Close()

	sub.OnNewBar(func(data *tqsdk.SeriesData) {
		if data.TickData != nil && len(data.TickData.Data) > 0 {
			tick := data.TickData.Data[len(data.TickData.Data)-1]
			fmt.Printf("📈 新 Tick: [%d] 最新价=%.2f 买一=%.2f(%d) 卖一=%.2f(%d) 成交量=%d (序列长度=%d)\n",
				tick.ID,
				tick.LastPrice,
				tick.BidPrice1, tick.BidVolume1,
				tick.AskPrice1, tick.AskVolume1,
				tick.Volume,
				len(data.TickData.Data))
		}
	})

	// 运行 20 秒
	time.Sleep(20 * time.Second)
	fmt.Println("Tick 订阅示例结束")
}

func main() {
	// 运行各个示例
	// QuoteSubscriptionExample()
	SingleKlineSubscriptionExample()
	// MultiKlineSubscriptionExample()
	// TickSubscriptionExample()

	fmt.Println("所有示例运行完成!")
}
