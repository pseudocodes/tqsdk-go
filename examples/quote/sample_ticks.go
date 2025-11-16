package main

import (
	"fmt"
	"os"
	"time"

	tqsdk "github.com/pseudocodes/tqsdk-go"
)

// SampleTicksData 演示如何使用 GetTicksData 获取结构化 Tick 数据
func SampleTicksData() {
	config := tqsdk.DefaultTQSDKConfig()
	config.LogConfig.Level = "info"
	config.AutoInit = true

	userID := os.Getenv("SHINNYTECH_ID")
	password := os.Getenv("SHINNYTECH_PW")
	// 创建 TQSDK 实例
	sdk := tqsdk.NewTQSDK(userID, password, config)
	defer sdk.Close()

	fmt.Println("TQSDK Go 示例 - Tick数据结构化访问")
	fmt.Println("=====================================")

	// 注册 ready 事件
	sdk.On(tqsdk.EventReady, func(data interface{}) {
		fmt.Println("\n✓ SDK 已就绪")

		// 订阅合约
		symbol := "SHFE.au2512"
		sdk.SubscribeQuote([]string{symbol})

		// 请求 Tick 数据
		chartPayload := map[string]interface{}{
			"symbol":     symbol,
			"duration":   0, // 0 表示 tick
			"view_width": 100,
		}
		sdk.SetChart(chartPayload)
		fmt.Printf("✓ 已请求 %s 的 Tick 数据\n", symbol)
	})

	// 监听数据更新
	dataReceived := false
	sdk.On(tqsdk.EventRtnData, func(data interface{}) {
		if dataReceived {
			return
		}

		// 检查 Tick 数据是否有更新
		if sdk.IsChanging([]string{"ticks", "SHFE.au2512"}) {
			dataReceived = true

			fmt.Println("\n【Tick数据更新】")

			// 使用新的 GetTicksData 方法获取结构化数据
			tickSeriesData, err := sdk.GetTicksData("SHFE.au2512")
			if err != nil {
				fmt.Printf("✗ 获取 Tick 数据失败: %v\n", err)
				return
			}

			fmt.Printf("\n最新 ID: %d\n", tickSeriesData.LastID)
			fmt.Printf("Tick总数: %d\n", len(tickSeriesData.Data))

			// 显示最近的 5 个 Tick
			if len(tickSeriesData.Data) > 0 {
				fmt.Println("\n【最近的 Tick】")
				start := 0
				if len(tickSeriesData.Data) > 5 {
					start = len(tickSeriesData.Data) - 5
				}

				for i := start; i < len(tickSeriesData.Data); i++ {
					tick := tickSeriesData.Data[i]
					fmt.Printf("\nTick ID: %d\n", tick.ID)
					fmt.Printf("  时间: %d\n", tick.Datetime)
					fmt.Printf("  最新价: %.2f\n", tick.LastPrice)
					fmt.Printf("  买一: %.2f (%d), 卖一: %.2f (%d)\n",
						tick.BidPrice1, tick.BidVolume1, tick.AskPrice1, tick.AskVolume1)
					fmt.Printf("  成交量: %d, 持仓量: %d\n", tick.Volume, tick.OpenInterest)
				}
			}

			// 对比：使用原始 GetTicks 方法
			fmt.Println("\n\n【对比：使用原始 GetTicks 方法】")
			ticksMap := sdk.GetTicks("SHFE.au2512")
			fmt.Printf("原始 map 格式的 last_id: %v\n", ticksMap["last_id"])
			if dataMap, ok := ticksMap["data"].(map[string]interface{}); ok {
				fmt.Printf("原始 map 格式的数据条数: %d\n", len(dataMap))
			}
		}
	})

	// 注册错误事件
	sdk.On(tqsdk.EventError, func(data interface{}) {
		if err, ok := data.(error); ok {
			fmt.Printf("\n✗ 错误: %v\n", err)
		}
	})

	// 运行 30 秒后退出
	fmt.Println("\n程序运行中，30秒后自动退出...")
	time.Sleep(30 * time.Second)
	fmt.Println("\n程序退出")
}
