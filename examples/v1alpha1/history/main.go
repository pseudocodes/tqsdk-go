package main

import (
	"context"
	"fmt"
	"os"
	"time"

	tqsdk "github.com/pseudocodes/tqsdk-go/shinny/v1alpha1"
)

// HistoryKlineWithLeftIDExample 使用 left_kline_id 订阅历史 K线
func HistoryKlineWithLeftIDExample() {
	ctx := context.Background()

	username := os.Getenv("SHINNYTECH_ID")
	password := os.Getenv("SHINNYTECH_PW")

	client, err := tqsdk.NewClient(ctx, username, password,
		tqsdk.WithLogLevel("info"),
		tqsdk.WithViewWidth(100000),
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
	targetSymbol := "SHFE.au2512"

	fmt.Println("==================== 历史 K线订阅示例（使用 left_kline_id） ====================")

	// 从指定的 K线 ID 开始订阅 8000 根历史 K线
	// 注意：数据会分片返回（每片最多 3000 根）
	// leftKlineID := int64(105761)
	leftKlineID := int64(10000)

	sub, err := client.Series().KlineHistory(ctx, targetSymbol, 60*time.Second, 8010, leftKlineID)
	if err != nil {
		fmt.Printf("订阅失败: %v\n", err)
		return
	}
	sub.Start()
	defer sub.Close()

	// 监听数据更新
	sub.OnUpdate(func(data *tqsdk.SeriesData, info *tqsdk.UpdateInfo) {
		symData := data.GetSymbolKlines(targetSymbol)

		if info.HasChartSync {
			fmt.Printf("✅ Chart 初次同步完成\n")
			fmt.Printf("   范围: [%d, %d]\n", data.Single.Chart.LeftID, data.Single.Chart.RightID)
			fmt.Printf("   数据量: %d 根K线\n", len(symData.Data))
			fmt.Printf("   第一根 Bar: %+v\n", symData.Data[0])
		}

		if info.ChartRangeChanged {
			fmt.Printf("📊 Chart 范围变化: [%d,%d] -> [%d,%d]\n",
				info.OldLeftID, info.OldRightID,
				info.NewLeftID, info.NewRightID)
			fmt.Printf("   当前数据量: %d 根K线\n", len(symData.Data))
		}

		// 检测分片数据传输完成
		if info.ChartReady {
			fmt.Printf("\n🎉 所有历史数据传输完成！\n")
			fmt.Printf("   最终范围: [%d, %d]\n", data.Single.Chart.LeftID, data.Single.Chart.RightID)
			fmt.Printf("   总数据量: %d 根K线\n", len(symData.Data))
			fmt.Printf("   Chart More Data: %v, Ready: %v\n", data.Single.Chart.MoreData, data.Single.Chart.Ready)

			// 验证数据范围是否正确
			if len(symData.Data) > 0 {
				first := symData.Data[0]
				last := symData.Data[len(symData.Data)-1]

				fmt.Printf("\n数据范围验证:\n")
				fmt.Printf("   首根 K线 ID: %d (应该 >= left_id: %d)\n", first.ID, data.Single.Chart.LeftID)
				fmt.Printf("   末根 K线 ID: %d (应该 <= right_id: %d)\n", last.ID, data.Single.Chart.RightID)

				if last.ID <= data.Single.Chart.RightID && first.ID >= data.Single.Chart.LeftID {
					fmt.Printf("   ✓ 数据范围正确！\n")
				} else {
					fmt.Printf("   ❌ 数据范围异常！\n")
				}
			}

			// 显示前几根和后几根K线
			if len(symData.Data) > 0 {
				fmt.Printf("\n前3根K线:\n")
				for i := 0; i < 3 && i < len(symData.Data); i++ {
					k := symData.Data[i]
					fmt.Printf("  [%d] %s O:%.2f H:%.2f L:%.2f C:%.2f V:%d\n",
						k.ID,
						time.Unix(0, k.Datetime).Format("2006-01-02 15:04:05"),
						k.Open, k.High, k.Low, k.Close, k.Volume)
				}

				fmt.Printf("\n后3根K线:\n")
				start := len(symData.Data) - 3
				if start < 0 {
					start = 0
				}
				for i := start; i < len(symData.Data); i++ {
					k := symData.Data[i]
					fmt.Printf("  [%d] %s O:%.2f H:%.2f L:%.2f C:%.2f V:%d\n",
						k.ID,
						time.Unix(0, k.Datetime).Format("2006-01-02 15:04:05"),
						k.Open, k.High, k.Low, k.Close, k.Volume)
				}
			}
		}
	})

	// 等待数据传输完成
	time.Sleep(30 * time.Second)
	fmt.Println("\n历史 K线订阅示例结束")
}

// HistoryKlineWithFocusExample 使用 focus_datetime + focus_position 订阅历史 K线
func HistoryKlineWithFocusExample() {
	ctx := context.Background()

	username := os.Getenv("SHINNYTECH_ID")
	password := os.Getenv("SHINNYTECH_PW")

	client, err := tqsdk.NewClient(ctx, username, password,
		tqsdk.WithLogLevel("info"),
		tqsdk.WithViewWidth(500),
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

	fmt.Println("==================== 历史 K线订阅示例（使用 focus_datetime） ====================")

	// 从指定时间点开始订阅（focus_position=1 表示从该时间向右扩展）
	focusTime := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)
	sub, err := client.Series().KlineHistoryWithFocus(ctx, "SHFE.au2512", 60*time.Second, 8000, focusTime, 1)
	if err != nil {
		fmt.Printf("订阅失败: %v\n", err)
		return
	}
	defer sub.Close()

	sub.OnUpdate(func(data *tqsdk.SeriesData, info *tqsdk.UpdateInfo) {
		if info.ChartReady {
			fmt.Printf("\n🎉 历史数据传输完成！\n")
			fmt.Printf("   焦点时间: %s\n", focusTime.Format("2006-01-02 15:04:05"))

			symData := data.GetSymbolKlines("SHFE.au2512")
			fmt.Printf("   范围: [%d, %d]\n", data.Single.Chart.LeftID, data.Single.Chart.RightID)
			fmt.Printf("   数据量: %d 根K线\n", len(symData.Data))
		}
	})

	time.Sleep(30 * time.Second)
	fmt.Println("\n历史 K线订阅示例结束")
}

func main() {
	// 运行历史数据订阅示例
	HistoryKlineWithLeftIDExample()
	// HistoryKlineWithFocusExample()

	fmt.Println("\n所有示例运行完成!")
}
