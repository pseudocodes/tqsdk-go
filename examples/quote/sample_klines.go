package main

import (
	"fmt"
	"os"
	"time"

	tqsdk "github.com/pseudocodes/tqsdk-go"
)

// SampleKlinesData 演示如何使用 GetKlinesData 获取结构化 K 线数据
func SampleKlinesData() {
	config := tqsdk.DefaultTQSDKConfig()
	config.LogConfig.Level = "debug"
	config.AutoInit = true

	userID := os.Getenv("SHINNYTECH_ID")
	password := os.Getenv("SHINNYTECH_PW")
	// 创建 TQSDK 实例
	sdk := tqsdk.NewTQSDK(userID, password, config)
	defer sdk.Close()

	fmt.Println("TQSDK Go 示例 - K线数据结构化访问")
	fmt.Println("=====================================")

	// 注册 ready 事件
	sdk.On(tqsdk.EventReady, func(data interface{}) {
		fmt.Println("\n✓ SDK 已就绪")

		// 订阅合约
		symbol := "SHFE.au2512"
		sdk.SubscribeQuote([]string{symbol})

		// 请求 K 线数据
		chartPayload := map[string]interface{}{
			"symbol":     symbol,
			"duration":   60 * 1e9, // 1分钟
			"view_width": 10,
		}
		sdk.SetChart(chartPayload)
		fmt.Printf("✓ 已请求 %s 的 1 分钟 K 线\n", symbol)
	})

	// 监听数据更新
	// dataReceived := false
	sdk.On(tqsdk.EventRtnData, func(data interface{}) {

		// 检查 K 线数据是否有更新
		if sdk.IsChanging([]string{"klines", "SHFE.au2512", "60000000000"}) {

			fmt.Println("\n【K线数据更新】")

			// 使用新的 GetKlinesData 方法获取结构化数据
			klineSeriesData, err := sdk.GetKlinesData("SHFE.au2512", 60*1e9)
			if err != nil {
				fmt.Printf("✗ 获取 K 线数据失败: %v\n", err)
				return
			}

			fmt.Printf("\n交易日范围: %d - %d\n", klineSeriesData.TradingDayStartID, klineSeriesData.TradingDayEndID)
			fmt.Printf("最新 ID: %d\n", klineSeriesData.LastID)
			fmt.Printf("K线总数: %d\n", len(klineSeriesData.Data))

			// 显示最近的 5 根 K 线
			if len(klineSeriesData.Data) > 0 {
				fmt.Println("\n【最近的 K 线】")
				start := 0
				if len(klineSeriesData.Data) > 5 {
					start = len(klineSeriesData.Data) - 5
				}

				for i := start; i < len(klineSeriesData.Data); i++ {
					kline := klineSeriesData.Data[i]
					fmt.Printf("\nK线ID: %d\n", kline.ID)
					fmt.Printf("  时间: %d\n", kline.Datetime)
					fmt.Printf("  开: %.2f, 高: %.2f, 低: %.2f, 收: %.2f\n",
						kline.Open, kline.High, kline.Low, kline.Close)
					fmt.Printf("  成交量: %d\n", kline.Volume)
				}
			}

			// 对比：使用原始 GetKlines 方法
			fmt.Println("\n\n【对比: 使用原始 GetKlines 方法】")
			klinesMap := sdk.GetKlines("SHFE.au2512", 60*1e9)
			fmt.Printf("原始 map 格式的 last_id: %v\n", klinesMap["last_id"])
			if dataMap, ok := klinesMap["data"].(map[string]interface{}); ok {
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
