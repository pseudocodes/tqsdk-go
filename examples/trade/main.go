package main

import (
	"os"
	"time"

	tqsdk "github.com/pseudocodes/tqsdk-go"
	"go.uber.org/zap"
)

func sample1() {
	// 创建 Development Logger（带颜色输出）
	zapConfig := zap.NewDevelopmentConfig()
	// zapConfig.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	logger, _ := zapConfig.Build()
	defer logger.Sync()

	// 创建配置
	config := tqsdk.DefaultTQSDKConfig()
	config.LogConfig.Level = "info"
	config.LogConfig.Development = true
	config.AutoInit = true

	// 创建 TQSDK 实例
	// bid := "快期模拟"
	bid := "simnow"

	tqUserID := os.Getenv("SHINNYTECH_ID")
	tqPassword := os.Getenv("SHINNYTECH_PW")

	userID := os.Getenv("SIMNOW_USER_0")
	password := os.Getenv("SIMNOW_PASS_0")

	sdk := tqsdk.NewTQSDK(tqUserID, tqPassword, config)
	defer sdk.Close()

	logger.Info("TQSDK Go 示例 - 交易操作")
	logger.Info("======================")

	// 账户信息

	// 注册期货公司列表事件
	sdk.On(tqsdk.EventRtnBrokers, func(data interface{}) {
		brokers := data.([]string)
		logger.Info("收到期货公司列表", zap.Int("count", len(brokers)))
	})

	// 注册数据更新事件
	loginChecked := false

	count := 0
	sdk.On(tqsdk.EventRtnData, func(data interface{}) {
		count++
		logger.Warn("收到数据", zap.Int("count", count))

		if !loginChecked {
			if !sdk.IsLogined(bid, userID) {
				return
			}
			loginChecked = true
			logger.Info("登录成功", zap.String("bid", bid), zap.String("user_id", userID))
		}

		// logger.Debug("收到数据", zap.Any("data", data))

		// 查询账户信息
		logger.Info("【账户信息】")
		account := sdk.GetAccount(bid, tqUserID)
		if account != nil {
			if balance, ok := account["balance"].(float64); ok {
				logger.Info("账户权益", zap.Float64("balance", balance))
			}
			if available, ok := account["available"].(float64); ok {
				logger.Info("可用资金", zap.Float64("available", available))
			}
			if currMargin, ok := account["curr_margin"].(float64); ok {
				logger.Info("当前保证金", zap.Float64("curr_margin", currMargin))
			}
			if riskRatio, ok := account["risk_ratio"].(float64); ok {
				logger.Info("风险度", zap.Float64("risk_ratio_percent", riskRatio*100))
			}
		}

		// 查询持仓
		logger.Info("【持仓信息】")
		positions := sdk.GetPositions(bid, userID)
		if len(positions) > 0 {
			for symbol, posData := range positions {
				if posMap, ok := posData.(map[string]interface{}); ok {
					fields := []zap.Field{zap.String("symbol", symbol)}
					if volumeLong, ok := posMap["volume_long_today"].(float64); ok {
						fields = append(fields, zap.Float64("volume_long_today", volumeLong))
					}
					if volumeShort, ok := posMap["volume_short_today"].(float64); ok {
						fields = append(fields, zap.Float64("volume_short_today", volumeShort))
					}
					if floatProfit, ok := posMap["float_profit"].(float64); ok {
						fields = append(fields, zap.Float64("float_profit", floatProfit))
					}
					logger.Info("持仓详情", fields...)
				}
			}
		} else {
			logger.Info("无持仓")
		}

		// 查询委托单
		logger.Info("【委托单】")
		orders := sdk.GetOrders(bid, userID)
		if len(orders) > 0 {
			count := 0
			for orderID, orderData := range orders {
				if orderMap, ok := orderData.(map[string]interface{}); ok {
					if status, ok := orderMap["status"].(string); ok && status == "ALIVE" {
						count++
						fields := []zap.Field{zap.String("order_id", orderID)}
						if symbol, ok := orderMap["exchange_id"].(string); ok {
							if inst, ok := orderMap["instrument_id"].(string); ok {
								fields = append(fields, zap.String("symbol", symbol+"."+inst))
							}
						}
						if direction, ok := orderMap["direction"].(string); ok {
							fields = append(fields, zap.String("direction", direction))
						}
						if limitPrice, ok := orderMap["limit_price"].(float64); ok {
							fields = append(fields, zap.Float64("limit_price", limitPrice))
						}
						if volumeLeft, ok := orderMap["volume_left"].(float64); ok {
							fields = append(fields, zap.Float64("volume_left", volumeLeft))
						}
						logger.Info("活动委托单", fields...)
					}
				}
			}
			if count == 0 {
				logger.Info("无活动委托单")
			}
		} else {
			logger.Info("无委托单")
		}
		// 	// 下单示例（注释掉，避免实际下单）
		// 	/*
		// 		fmt.Println("\n【下单示例】")
		// 		fmt.Println("  准备下单...")
		// 		order, err := sdk.InsertOrder(
		// 			bid, userID,
		// 			"SHFE", "au2406",
		// 			"BUY", "OPEN", "LIMIT",
		// 			500.00, 1,
		// 		)
		// 		if err != nil {
		// 			fmt.Printf("  ✗ 下单失败: %v\n", err)
		// 		} else {
		// 			fmt.Printf("  ✓ 下单成功！订单ID: %v\n", order["order_id"])

		// 			// 等待一下
		// 			time.Sleep(2 * time.Second)

		// 			// 撤单
		// 			if orderID, ok := order["order_id"].(string); ok {
		// 				fmt.Printf("  准备撤单 %s...\n", orderID)
		// 				err = sdk.CancelOrder(bid, userID, orderID)
		// 				if err != nil {
		// 					fmt.Printf("  ✗ 撤单失败: %v\n", err)
		// 				} else {
		// 					fmt.Println("  ✓ 撤单成功！")
		// 				}
		// 			}
		// 		}
		// 	*/

		logger.Info("交易示例完成")
	})

	// 注册通知事件
	sdk.On(tqsdk.EventNotify, func(data interface{}) {
		if notify, ok := data.(tqsdk.NotifyEvent); ok {
			logger.Warn("收到通知",
				zap.String("level", notify.Level),
				zap.String("content", notify.Content))
		}
	})

	// 注册错误事件
	sdk.On(tqsdk.EventError, func(data interface{}) {
		if err, ok := data.(error); ok {
			logger.Error("发生错误", zap.Error(err))
		}
	})

	// 登录账户
	logger.Info("正在登录账户", zap.String("bid", bid), zap.String("user_id", userID))
	err := sdk.Login(bid, userID, password)
	if err != nil {
		logger.Error("登录失败", zap.Error(err))
		return
	}

	// 保持程序运行
	logger.Info("程序运行中，按 Ctrl+C 退出...")
	time.Sleep(30 * time.Second)
	logger.Info("程序即将退出...")
}

func main() {
	sample1()
}
