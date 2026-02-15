package main

import (
	"context"
	"fmt"
	"os"
	"time"

	tqsdk "github.com/pseudocodes/tqsdk-go/shinny/v1alpha1"
)

func main() {
	ctx := context.Background()

	username := os.Getenv("SHINNYTECH_ID")
	password := os.Getenv("SHINNYTECH_PW")

	if username == "" || password == "" {
		fmt.Println("请设置环境变量 SHINNYTECH_ID 和 SHINNYTECH_PW")
		return
	}

	fmt.Println("==================== 合约信息缓存示例 ====================")

	// 示例 1: 使用默认配置（自动刷新策略，1天过期）
	fmt.Println("\n=== 示例 1: 默认配置（自动刷新） ===")
	example1(ctx, username, password)

	time.Sleep(2 * time.Second)

	// 示例 2: 总是从网络获取
	fmt.Println("\n=== 示例 2: 总是从网络获取 ===")
	example2(ctx, username, password)

	time.Sleep(2 * time.Second)

	// 示例 3: 优先使用本地缓存
	fmt.Println("\n=== 示例 3: 优先使用本地缓存 ===")
	example3(ctx, username, password)

	time.Sleep(2 * time.Second)

	// 示例 4: 自定义缓存配置
	fmt.Println("\n=== 示例 4: 自定义缓存配置 ===")
	example4(ctx, username, password)
}

// 示例 1: 使用默认配置
func example1(ctx context.Context, username, password string) {
	client, err := tqsdk.NewClient(ctx, username, password,
		tqsdk.WithLogLevel("info"),
	)
	if err != nil {
		fmt.Printf("创建客户端失败: %v\n", err)
		return
	}
	defer client.Close()

	// 初始化行情（会自动加载合约信息）
	if err := client.InitMarket(); err != nil {
		fmt.Printf("初始化行情失败: %v\n", err)
		return
	}

	// 等待合约信息加载完成
	time.Sleep(3 * time.Second)

	// 查询合约信息
	if info, exists := client.GetQuoteInfo("SHFE.au2602"); exists {
		fmt.Printf("✓ 成功获取合约信息: SHFE.au2602\n")
		if class, ok := info["class"].(string); ok {
			fmt.Printf("  合约类型: %s\n", class)
		}
	} else {
		fmt.Println("✗ 未找到合约信息")
	}
}

// 示例 2: 总是从网络获取
func example2(ctx context.Context, username, password string) {
	client, err := tqsdk.NewClient(ctx, username, password,
		tqsdk.WithSymbolsCacheStrategy(tqsdk.CacheStrategyAlwaysNetwork),
		tqsdk.WithLogLevel("info"),
	)
	if err != nil {
		fmt.Printf("创建客户端失败: %v\n", err)
		return
	}
	defer client.Close()

	if err := client.InitMarket(); err != nil {
		fmt.Printf("初始化行情失败: %v\n", err)
		return
	}

	time.Sleep(3 * time.Second)
	fmt.Println("✓ 使用 CacheStrategyAlwaysNetwork 策略")
}

// 示例 3: 优先使用本地缓存
func example3(ctx context.Context, username, password string) {
	client, err := tqsdk.NewClient(ctx, username, password,
		tqsdk.WithSymbolsCacheStrategy(tqsdk.CacheStrategyPreferLocal),
		tqsdk.WithLogLevel("info"),
	)
	if err != nil {
		fmt.Printf("创建客户端失败: %v\n", err)
		return
	}
	defer client.Close()

	if err := client.InitMarket(); err != nil {
		fmt.Printf("初始化行情失败: %v\n", err)
		return
	}

	time.Sleep(3 * time.Second)
	fmt.Println("✓ 使用 CacheStrategyPreferLocal 策略")
}

// 示例 4: 自定义缓存配置
func example4(ctx context.Context, username, password string) {
	// 使用临时目录作为缓存目录
	tmpDir := os.TempDir()
	cacheDir := tmpDir + "/tqsdk_cache_example"

	client, err := tqsdk.NewClient(ctx, username, password,
		tqsdk.WithSymbolsCacheDir(cacheDir),
		tqsdk.WithSymbolsCacheStrategy(tqsdk.CacheStrategyAutoRefresh),
		tqsdk.WithSymbolsCacheMaxAge(3600), // 1小时
		tqsdk.WithLogLevel("info"),
	)
	if err != nil {
		fmt.Printf("创建客户端失败: %v\n", err)
		return
	}
	defer client.Close()

	if err := client.InitMarket(); err != nil {
		fmt.Printf("初始化行情失败: %v\n", err)
		return
	}

	time.Sleep(3 * time.Second)
	fmt.Printf("✓ 使用自定义缓存目录: %s\n", cacheDir)
	fmt.Println("✓ 缓存有效期: 1小时")
}
