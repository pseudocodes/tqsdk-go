package main

import (
	"fmt"
	"os"
	"time"

	tqsdk "github.com/pseudocodes/tqsdk-go"
)

func main() {
	// 创建配置
	config := tqsdk.DefaultTQSDKConfig()
	config.LogConfig.Level = "info"
	config.AutoInit = true

	// 创建 TQSDK 实例
	bid := "快期模拟"
	userID := os.Getenv("SHINNYTECH_ID")
	password := os.Getenv("SHINNYTECH_PW")
	sdk := tqsdk.NewTQSDK(userID, password, config)
	defer sdk.Close()

	fmt.Println("TQSDK Go 示例 - 交易操作")
	fmt.Println("======================")

	// 账户信息

	// 注册期货公司列表事件
	sdk.On(tqsdk.EventRtnBrokers, func(data interface{}) {
		brokers := data.([]string)
		fmt.Printf("\n✓ 收到期货公司列表，共 %d 家\n", len(brokers))

	})

	// 注册数据更新事件
	loginChecked := false
	sdk.On(tqsdk.EventRtnData, func(data interface{}) {
		if loginChecked {
			return
		}

		// 检查是否登录成功
		if sdk.IsLogined(bid, userID) {
			loginChecked = true
			fmt.Println("✓ 登录成功！")

			// 确认结算单
			err := sdk.ConfirmSettlement(bid, userID)
			if err != nil {
				fmt.Printf("✗ 确认结算单失败: %v\n", err)
			} else {
				fmt.Println("✓ 已确认结算单")
			}

			// 等待一下让数据同步
			time.Sleep(2 * time.Second)

			// 查询账户信息
			fmt.Println("\n【账户信息】")
			account := sdk.GetAccount(bid, userID)
			if account != nil {
				if balance, ok := account["balance"].(float64); ok {
					fmt.Printf("  账户权益: %.2f\n", balance)
				}
				if available, ok := account["available"].(float64); ok {
					fmt.Printf("  可用资金: %.2f\n", available)
				}
				if currMargin, ok := account["curr_margin"].(float64); ok {
					fmt.Printf("  当前保证金: %.2f\n", currMargin)
				}
				if riskRatio, ok := account["risk_ratio"].(float64); ok {
					fmt.Printf("  风险度: %.2f%%\n", riskRatio*100)
				}
			}

			// 查询持仓
			fmt.Println("\n【持仓信息】")
			positions := sdk.GetPositions(bid, userID)
			if len(positions) > 0 {
				for symbol, posData := range positions {
					if posMap, ok := posData.(map[string]interface{}); ok {
						fmt.Printf("\n  合约: %s\n", symbol)
						if volumeLong, ok := posMap["volume_long_today"].(float64); ok {
							fmt.Printf("    今多: %.0f\n", volumeLong)
						}
						if volumeShort, ok := posMap["volume_short_today"].(float64); ok {
							fmt.Printf("    今空: %.0f\n", volumeShort)
						}
						if floatProfit, ok := posMap["float_profit"].(float64); ok {
							fmt.Printf("    浮动盈亏: %.2f\n", floatProfit)
						}
					}
				}
			} else {
				fmt.Println("  无持仓")
			}

			// 查询委托单
			fmt.Println("\n【委托单】")
			orders := sdk.GetOrders(bid, userID)
			if len(orders) > 0 {
				count := 0
				for orderID, orderData := range orders {
					if orderMap, ok := orderData.(map[string]interface{}); ok {
						if status, ok := orderMap["status"].(string); ok && status == "ALIVE" {
							count++
							fmt.Printf("\n  订单ID: %s\n", orderID)
							if symbol, ok := orderMap["exchange_id"].(string); ok {
								if inst, ok := orderMap["instrument_id"].(string); ok {
									fmt.Printf("    合约: %s.%s\n", symbol, inst)
								}
							}
							if direction, ok := orderMap["direction"].(string); ok {
								fmt.Printf("    方向: %s\n", direction)
							}
							if limitPrice, ok := orderMap["limit_price"].(float64); ok {
								fmt.Printf("    价格: %.2f\n", limitPrice)
							}
							if volumeLeft, ok := orderMap["volume_left"].(float64); ok {
								fmt.Printf("    剩余手数: %.0f\n", volumeLeft)
							}
						}
					}
				}
				if count == 0 {
					fmt.Println("  无活动委托单")
				}
			} else {
				fmt.Println("  无委托单")
			}

			// 下单示例（注释掉，避免实际下单）
			/*
				fmt.Println("\n【下单示例】")
				fmt.Println("  准备下单...")
				order, err := sdk.InsertOrder(
					bid, userID,
					"SHFE", "au2406",
					"BUY", "OPEN", "LIMIT",
					500.00, 1,
				)
				if err != nil {
					fmt.Printf("  ✗ 下单失败: %v\n", err)
				} else {
					fmt.Printf("  ✓ 下单成功！订单ID: %v\n", order["order_id"])

					// 等待一下
					time.Sleep(2 * time.Second)

					// 撤单
					if orderID, ok := order["order_id"].(string); ok {
						fmt.Printf("  准备撤单 %s...\n", orderID)
						err = sdk.CancelOrder(bid, userID, orderID)
						if err != nil {
							fmt.Printf("  ✗ 撤单失败: %v\n", err)
						} else {
							fmt.Println("  ✓ 撤单成功！")
						}
					}
				}
			*/

			fmt.Println("\n✓ 交易示例完成")
		}
	})

	// 注册通知事件
	sdk.On(tqsdk.EventNotify, func(data interface{}) {
		if notify, ok := data.(tqsdk.NotifyEvent); ok {
			fmt.Printf("\n[通知] %s: %s\n", notify.Level, notify.Content)
		}
	})

	// 注册错误事件
	sdk.On(tqsdk.EventError, func(data interface{}) {
		if err, ok := data.(error); ok {
			fmt.Printf("\n✗ 错误: %v\n", err)
		}
	})

	// 登录账户
	fmt.Printf("\n正在登录账户 %s/%s ...\n", bid, userID)
	err := sdk.Login(bid, userID, password)
	if err != nil {
		fmt.Printf("✗ 登录失败: %v\n", err)
		return
	}

	// 保持程序运行
	fmt.Println("\n程序运行中，按 Ctrl+C 退出...")
	time.Sleep(30 * time.Second)
	fmt.Println("\n程序即将退出...")
}
