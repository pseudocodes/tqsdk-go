package main

import (
	"fmt"
	"os"
	"time"

	tqsdk "github.com/pseudocodes/tqsdk-go"
)

func sample1() {
	// 创建配置
	config := tqsdk.DefaultTQSDKConfig()
	config.LogConfig.Level = "debug" // 启用 debug 日志以查看 WebSocket 消息
	config.AutoInit = true
	userID := os.Getenv("SHINNYTECH_ID")
	password := os.Getenv("SHINNYTECH_PW")
	// 创建 TQSDK 实例
	sdk := tqsdk.NewTQSDK(userID, password, config)
	defer sdk.Close()

	fmt.Println("TQSDK Go 示例 - 行情查询")
	fmt.Println("======================")

	// 注册 ready 事件
	sdk.On(tqsdk.EventReady, func(data interface{}) {
		fmt.Println("\n✓ SDK 已就绪，合约信息已加载")

		// 获取合约行情
		symbol := "SHFE.au2512"
		quote := sdk.GetQuote(symbol)

		fmt.Printf("\n【合约信息】%s\n", symbol)
		if productName, ok := quote["product_short_name"].(string); ok {
			fmt.Printf("  品种名称: %s\n", productName)
		}
		if volumeMultiple, ok := quote["volume_multiple"].(float64); ok {
			fmt.Printf("  合约乘数: %.0f\n", volumeMultiple)
		}
		if priceTick, ok := quote["price_tick"].(float64); ok {
			fmt.Printf("  最小变动价位: %.2f\n", priceTick)
		}

		fmt.Printf("\n【实时行情】\n")
		if lastPrice, ok := quote["last_price"].(float64); ok {
			fmt.Printf("  最新价: %.2f\n", lastPrice)
		}
		if bidPrice1, ok := quote["bid_price1"].(float64); ok {
			fmt.Printf("  买一价: %.2f\n", bidPrice1)
		}
		if askPrice1, ok := quote["ask_price1"].(float64); ok {
			fmt.Printf("  卖一价: %.2f\n", askPrice1)
		}
		if volume, ok := quote["volume"].(float64); ok {
			fmt.Printf("  成交量: %.0f\n", volume)
		}

		// 订阅多个合约
		symbols := []string{"SHFE.au2512", "DCE.m2512", "CZCE.CF512"}
		sdk.SubscribeQuote(symbols)
		fmt.Printf("\n✓ 已订阅合约: %v\n", symbols)

		// 请求 K 线数据
		chartPayload := map[string]interface{}{
			"symbol":     "SHFE.au2512",
			"duration":   60 * 1e9, // 1分钟
			"view_width": 10,
		}
		chart := sdk.SetChart(chartPayload)
		fmt.Printf("\n✓ 已请求 K 线图表，chart_id: %v\n", chart["chart_id"])
	})

	// 注册数据更新事件
	updateCount := 0
	sdk.On(tqsdk.EventRtnData, func(data interface{}) {
		updateCount++
		if updateCount <= 5 {
			fmt.Printf(".")
		}

		// 检查特定合约是否有更新
		if sdk.IsChanging([]string{"quotes", "SHFE.au2512"}) {
			quote := sdk.GetQuote("SHFE.au2512")
			if lastPrice, ok := quote["last_price"].(float64); ok {
				if datetime, ok := quote["datetime"].(string); ok {
					fmt.Printf("\n[%s] SHFE.au2512 更新 - 最新价: %.2f\n", datetime, lastPrice)
				}
			}
		}

		if sdk.IsChanging([]string{"klines", "SHFE.au2512", "60000000000"}) {
			klinesData, err := sdk.GetKlinesData("SHFE.au2512", 60000000000)
			if err != nil {
				fmt.Printf("✗ 获取 K 线数据失败: %v\n", err)
				return
			}
			// dump.V(klinesData)
			if len(klinesData.Data) > 0 {
				fmt.Printf("\n[%d] SHFE.au2512 更新 - 最新价: %.2f\n", klinesData.Data[len(klinesData.Data)-1].ID, klinesData.Data[len(klinesData.Data)-1].Close)
			}
		}
	})

	// 注册错误事件
	sdk.On(tqsdk.EventError, func(data interface{}) {
		if err, ok := data.(error); ok {
			fmt.Printf("\n✗ 错误: %v\n", err)
		}
	})

	// 搜索合约示例
	sdk.On(tqsdk.EventReady, func(data interface{}) {
		// 等待一下让第一次 ready 处理完成
		time.Sleep(1 * time.Second)

		fmt.Println("\n\n【合约搜索示例】")

		// 搜索黄金合约
		filterOption := map[string]bool{
			"future": true,
		}
		results := sdk.GetQuotesByInput("au", filterOption)
		fmt.Printf("搜索 'au' 找到 %d 个合约\n", len(results))
		if len(results) > 0 && len(results) <= 5 {
			for _, symbol := range results {
				fmt.Printf("  - %s\n", symbol)
			}
		}

		// 搜索期货指数和主连
		filterOption2 := map[string]bool{
			"future":       false,
			"future_index": true,
			"future_cont":  true,
		}
		results2 := sdk.GetQuotesByInput("au", filterOption2)
		fmt.Printf("\n搜索黄金指数和主连找到 %d 个合约\n", len(results2))
		if len(results2) > 0 && len(results2) <= 5 {
			for _, symbol := range results2 {
				fmt.Printf("  - %s\n", symbol)
			}
		}
	})

	// 保持程序运行
	fmt.Println("\n程序运行中，按 Ctrl+C 退出...")
	select {}
}

func sample2() {
	config := tqsdk.DefaultTQSDKConfig()
	config.LogConfig.Level = "debug" // 启用 debug 日志以查看 WebSocket 消息
	config.AutoInit = true

	userID := os.Getenv("SHINNYTECH_ID")
	password := os.Getenv("SHINNYTECH_PW")
	// 创建 TQSDK 实例
	sdk := tqsdk.NewTQSDK(userID, password, config)
	defer sdk.Close()

	fmt.Println("TQSDK Go 示例 - 行情查询")
	fmt.Println("======================")

	// 注册 ready 事件
	sdk.On(tqsdk.EventReady, func(data interface{}) {
		fmt.Println("\n✓ SDK 已就绪，合约信息已加载")

		// 获取合约行情
		symbol := "SHFE.au2512"
		quote := sdk.GetQuote(symbol)

		fmt.Printf("\n【合约信息】%s\n", symbol)
		if productName, ok := quote["product_short_name"].(string); ok {
			fmt.Printf("  品种名称: %s\n", productName)
		}
		if volumeMultiple, ok := quote["volume_multiple"].(float64); ok {
			fmt.Printf("  合约乘数: %.0f\n", volumeMultiple)
		}
		if priceTick, ok := quote["price_tick"].(float64); ok {
			fmt.Printf("  最小变动价位: %.2f\n", priceTick)
		}

		fmt.Printf("\n【实时行情】\n")
		if lastPrice, ok := quote["last_price"].(float64); ok {
			fmt.Printf("  最新价: %.2f\n", lastPrice)
		}
		if bidPrice1, ok := quote["bid_price1"].(float64); ok {
			fmt.Printf("  买一价: %.2f\n", bidPrice1)
		}
		if askPrice1, ok := quote["ask_price1"].(float64); ok {
			fmt.Printf("  卖一价: %.2f\n", askPrice1)
		}
		if volume, ok := quote["volume"].(float64); ok {
			fmt.Printf("  成交量: %.0f\n", volume)
		}

		// 订阅多个合约
		symbols := []string{"SHFE.au2512", "DCE.m2512", "CZCE.CF512"}
		sdk.SubscribeQuote(symbols)
		fmt.Printf("\n✓ 已订阅合约: %v\n", symbols)
	})
	// 保持程序运行
	fmt.Println("\n程序运行中, 按 Ctrl+C 退出...")
	select {}
}

func main() {
	// 选择要运行的示例
	sample1() // 基础行情示例
	// sample2()          // 简化行情示例
	// SampleKlinesData() // K线数据结构化访问示例（推荐）
	// SampleTicksData()  // Tick数据结构化访问示例
}
