package main

import (
	"context"
	"fmt"
	"os"
	"time"

	tqsdk "github.com/pseudocodes/tqsdk-go/shinny"
)

// TradeCallbackExample 使用回调模式的交易示例
func TradeCallbackExample() {
	ctx := context.Background()

	username := os.Getenv("SHINNYTECH_ID")
	password := os.Getenv("SHINNYTECH_PW")

	simUserID := os.Getenv("SIMNOW_USER_0")
	simPassword := os.Getenv("SIMNOW_PASS_0")

	// 创建客户端
	client, err := tqsdk.NewClient(ctx, username, password,
		tqsdk.WithLogLevel("info"),
	)
	if err != nil {
		fmt.Printf("创建客户端失败: %v\n", err)
		return
	}
	defer client.Close()

	fmt.Println("==================== 交易回调模式示例 ====================")

	// 登录交易账户
	session, err := client.LoginTrade(ctx, "simnow", simUserID, simPassword)
	if err != nil {
		fmt.Printf("登录失败: %v\n", err)
		return
	}
	defer session.Close()

	// 注册账户更新回调
	session.OnAccount(func(account *tqsdk.Account) {
		fmt.Printf("💰 账户更新: 权益=%.2f, 可用=%.2f, 风险度=%.2f%%\n",
			account.Balance, account.Available, account.RiskRatio*100)
	})

	// 注册持仓更新回调（单个持仓）
	session.OnPosition(func(symbol string, pos *tqsdk.Position) {
		totalLong := pos.VolumeLongToday + pos.VolumeLongHis
		totalShort := pos.VolumeShortToday + pos.VolumeShortHis

		if totalLong > 0 || totalShort > 0 {
			fmt.Printf("📊 %s 持仓更新: 多头=%d, 空头=%d, 浮动盈亏=%.2f\n",
				symbol, totalLong, totalShort, pos.FloatProfit)
		}
	})

	// 注册持仓更新回调（全量）
	session.OnPositions(func(positions map[string]*tqsdk.Position) {
		if len(positions) > 0 {
			fmt.Printf("📊 持仓总数: %d\n", len(positions))
			totalProfit := 0.0
			for symbol, pos := range positions {
				fmt.Printf("  - %s: 浮盈=%.2f\n", symbol, pos.FloatProfit)
				totalProfit += pos.FloatProfit
			}
			fmt.Printf("  总浮盈: %.2f\n", totalProfit)
		}
	})

	// 注册委托单更新回调
	session.OnOrder(func(order *tqsdk.Order) {
		fmt.Printf("📝 订单 %s: %s.%s %s %s@%.2f, 状态=%s, 剩余=%d\n",
			order.OrderID,
			order.ExchangeID,
			order.InstrumentID,
			order.Direction,
			order.Offset,
			order.LimitPrice,
			order.Status,
			order.VolumeLeft)
	})

	// 注册成交回调
	session.OnTrade(func(trade *tqsdk.Trade) {
		fmt.Printf("✅ 成交 %s: %s.%s %s %s@%.2f x%d, 手续费=%.2f\n",
			trade.TradeID,
			trade.ExchangeID,
			trade.InstrumentID,
			trade.Direction,
			trade.Offset,
			trade.Price,
			trade.Volume,
			trade.Commission)
	})

	// 注册通知回调
	session.OnNotification(func(notify *tqsdk.Notification) {
		fmt.Printf("🔔 [%s] %s: %s\n", notify.Level, notify.Code, notify.Content)
	})

	// 等待登录完成
	fmt.Println("等待登录...")
	for !session.IsLoggedIn() {
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Println("✅ 登录成功!")

	// 查询账户（同步方式）
	account, err := session.GetAccount(ctx)
	if err == nil {
		fmt.Printf("\n当前账户信息:\n")
		fmt.Printf("  权益: %.2f\n", account.Balance)
		fmt.Printf("  可用: %.2f\n", account.Available)
		fmt.Printf("  保证金: %.2f\n", account.CurrMargin)
		fmt.Printf("  风险度: %.2f%%\n", account.RiskRatio*100)
	}

	// 查询持仓
	positions, err := session.GetPositions(ctx)
	if err == nil && len(positions) > 0 {
		fmt.Printf("\n当前持仓:\n")
		for symbol, pos := range positions {
			totalLong := pos.VolumeLongToday + pos.VolumeLongHis
			totalShort := pos.VolumeShortToday + pos.VolumeShortHis
			if totalLong > 0 || totalShort > 0 {
				fmt.Printf("  %s: 多=%d 空=%d 浮盈=%.2f\n",
					symbol, totalLong, totalShort, pos.FloatProfit)
			}
		}
	}

	// 下单示例（注释掉，避免实际下单）
	/*
		fmt.Println("\n准备下单...")
		order, err := session.InsertOrder(ctx, &tqsdk.InsertOrderRequest{
			Symbol:     "SHFE.au2512",
			Direction:  tqsdk.DirectionBuy,
			Offset:     tqsdk.OffsetOpen,
			PriceType:  tqsdk.PriceTypeLimit,
			LimitPrice: 500.0,
			Volume:     1,
		})
		if err != nil {
			fmt.Printf("下单失败: %v\n", err)
		} else {
			fmt.Printf("下单成功: %s\n", order.OrderID)

			// 等待一会儿
			time.Sleep(2 * time.Second)

			// 撤单
			fmt.Printf("准备撤单 %s...\n", order.OrderID)
			err = session.CancelOrder(ctx, order.OrderID)
			if err != nil {
				fmt.Printf("撤单失败: %v\n", err)
			} else {
				fmt.Println("撤单成功!")
			}
		}
	*/

	// 运行 30 秒
	fmt.Println("\n监听交易数据更新...")
	time.Sleep(30 * time.Second)
	select {}
	fmt.Println("回调模式示例结束\n")
}

// TradeChannelExample 使用流式模式的交易示例
func TradeChannelExample() {
	ctx := context.Background()

	username := os.Getenv("SHINNYTECH_ID")
	password := os.Getenv("SHINNYTECH_PW")

	simUserID := os.Getenv("SIMNOW_USER_0")
	simPassword := os.Getenv("SIMNOW_PASS_0")

	// 创建客户端
	client, err := tqsdk.NewClient(ctx, username, password,
		tqsdk.WithLogLevel("info"),
	)
	if err != nil {
		fmt.Printf("创建客户端失败: %v\n", err)
		return
	}
	defer client.Close()

	fmt.Println("==================== 交易流式模式示例 ====================")

	// 登录交易账户
	session, err := client.LoginTrade(ctx, "simnow", simUserID, simPassword)
	if err != nil {
		fmt.Printf("登录失败: %v\n", err)
		return
	}
	defer session.Close()

	// 启动 goroutine 监听账户更新
	go func() {
		for account := range session.AccountChannel() {
			fmt.Printf("💰 账户更新: 权益=%.2f, 可用=%.2f\n",
				account.Balance, account.Available)
		}
	}()

	// 监听持仓更新
	go func() {
		for update := range session.PositionChannel() {
			fmt.Printf("📊 %s 持仓更新: 浮盈=%.2f\n",
				update.Symbol, update.Position.FloatProfit)
		}
	}()

	// 监听订单更新
	go func() {
		for order := range session.OrderChannel() {
			fmt.Printf("📝 订单 %s: 状态=%s, 剩余=%d\n",
				order.OrderID, order.Status, order.VolumeLeft)
		}
	}()

	// 监听成交
	go func() {
		for trade := range session.TradeChannel() {
			fmt.Printf("✅ 成交: %s.%s@%.2f x%d\n",
				trade.ExchangeID, trade.InstrumentID,
				trade.Price, trade.Volume)
		}
	}()

	// 监听通知
	go func() {
		for notify := range session.NotificationChannel() {
			fmt.Printf("🔔 [%s] %s\n", notify.Level, notify.Content)
		}
	}()

	// 等待登录完成
	fmt.Println("等待登录...")
	for !session.IsLoggedIn() {
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Println("✅ 登录成功!")

	// 运行 30 秒
	fmt.Println("\n监听交易数据更新...")
	time.Sleep(30 * time.Second)
	fmt.Println("流式模式示例结束\n")
}

// TradeMixedExample 混合使用回调和流式的示例
func TradeMixedExample() {
	ctx := context.Background()

	username := os.Getenv("SHINNYTECH_ID")
	password := os.Getenv("SHINNYTECH_PW")

	simUserID := os.Getenv("SIMNOW_USER_0")
	simPassword := os.Getenv("SIMNOW_PASS_0")

	// 创建客户端
	client, err := tqsdk.NewClient(ctx, username, password,
		tqsdk.WithLogLevel("info"),
	)
	if err != nil {
		fmt.Printf("创建客户端失败: %v\n", err)
		return
	}
	defer client.Close()

	fmt.Println("==================== 交易混合模式示例 ====================")

	// 登录交易账户
	session, err := client.LoginTrade(ctx, "simnow", simUserID, simPassword)
	if err != nil {
		fmt.Printf("登录失败: %v\n", err)
		return
	}
	defer session.Close()

	// 重要的用回调（实时响应）
	session.OnTrade(func(trade *tqsdk.Trade) {
		fmt.Printf("⚡ 成交通知: %s %s@%.2f x%d\n",
			trade.TradeID, trade.InstrumentID,
			trade.Price, trade.Volume)
	})

	session.OnNotification(func(notify *tqsdk.Notification) {
		fmt.Printf("🔔 通知: [%s] %s\n", notify.Level, notify.Content)
	})

	// 批量处理用流式
	go func() {
		for update := range session.PositionChannel() {
			fmt.Printf("📊 持仓更新: %s 浮盈=%.2f\n",
				update.Symbol, update.Position.FloatProfit)
		}
	}()

	go func() {
		for order := range session.OrderChannel() {
			fmt.Printf("📝 订单更新: %s 状态=%s\n",
				order.OrderID, order.Status)
		}
	}()

	// 等待登录完成
	fmt.Println("等待登录...")
	for !session.IsLoggedIn() {
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Println("✅ 登录成功!")

	// 同步查询当前状态
	account, err := session.GetAccount(ctx)
	if err == nil {
		fmt.Printf("\n当前权益: %.2f, 可用: %.2f\n",
			account.Balance, account.Available)
	}

	// 运行 30 秒
	fmt.Println("\n监听交易数据更新...")
	time.Sleep(30 * time.Second)
	fmt.Println("混合模式示例结束\n")
}

func main() {
	// 运行各个示例
	TradeCallbackExample()
	// TradeChannelExample()
	// TradeMixedExample()

	fmt.Println("所有交易示例运行完成!")
}
