package main

import (
	"context"
	"fmt"
	"time"

	tqsdk "github.com/pseudocodes/tqsdk-go/shinny/v1alpha1"
)

// DataManager Watch 功能示例
func WatchExample() {
	fmt.Println("==================== DataManager Watch 示例 ====================")

	// 创建 DataManager
	initialData := map[string]interface{}{
		"quotes": make(map[string]interface{}),
	}
	dm := tqsdk.NewDataManager(initialData)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 监听特定路径
	ch, err := dm.Watch(ctx, []string{"quotes", "SHFE.au2512"})
	if err != nil {
		fmt.Printf("Watch 失败: %v\n", err)
		return
	}

	// 启动 goroutine 接收数据
	go func() {
		for data := range ch {
			if quoteMap, ok := data.(map[string]interface{}); ok {
				fmt.Printf("📊 Quote 更新: 最新价=%.2f, 成交量=%v\n",
					quoteMap["last_price"], quoteMap["volume"])
			}
		}
	}()

	// 模拟数据更新
	fmt.Println("模拟数据更新...")
	time.Sleep(500 * time.Millisecond)

	dm.MergeData(map[string]interface{}{
		"quotes": map[string]interface{}{
			"SHFE.au2512": map[string]interface{}{
				"last_price": 500.0,
				"volume":     1000,
			},
		},
	}, true, false)

	time.Sleep(100 * time.Millisecond)

	// 更新数据
	dm.MergeData(map[string]interface{}{
		"quotes": map[string]interface{}{
			"SHFE.au2512": map[string]interface{}{
				"last_price": 501.5,
				"volume":     1200,
			},
		},
	}, true, false)

	time.Sleep(100 * time.Millisecond)

	// 取消监听
	fmt.Println("取消监听...")
	dm.UnWatch([]string{"quotes", "SHFE.au2512"})

	fmt.Println("Watch 示例结束")
}

// DataManager 配置示例
func ConfigExample() {
	fmt.Println("==================== DataManager 配置示例 ====================")

	// 创建带配置的 DataManager
	config := tqsdk.DataManagerConfig{
		DefaultViewWidth:  1000,
		MaxDataRetention:  24 * time.Hour,
		EnableAutoCleanup: true,
	}

	initialData := map[string]interface{}{}
	dm := tqsdk.NewDataManager(initialData, config)

	// 获取配置
	fmt.Printf("默认视图宽度: %d\n", dm.GetViewWidth())
	fmt.Printf("数据保留时间: %v\n", dm.GetDataRetention())

	// 动态修改配置
	dm.SetViewWidth(2000)
	fmt.Printf("新视图宽度: %d\n", dm.GetViewWidth())

	dm.SetDataRetention(48 * time.Hour)
	fmt.Printf("新数据保留时间: %v\n", dm.GetDataRetention())

	fmt.Println("配置示例结束")
}

// 数据访问示例
func DataAccessExample() {
	fmt.Println("==================== 数据访问示例 ====================")

	initialData := map[string]interface{}{
		"quotes": map[string]interface{}{
			"SHFE.au2512": map[string]interface{}{
				"last_price": 500.0,
				"volume":     1000,
			},
		},
	}

	dm := tqsdk.NewDataManager(initialData)

	// 使用 Get 方法（带错误处理）
	data, err := dm.Get([]string{"quotes", "SHFE.au2512"})
	if err != nil {
		fmt.Printf("Get 失败: %v\n", err)
	} else {
		fmt.Printf("Get 成功: %v\n", data)
	}

	// 使用 GetByPath 方法（兼容接口）
	data2 := dm.GetByPath([]string{"quotes", "SHFE.au2512"})
	if data2 != nil {
		fmt.Printf("GetByPath 成功: %v\n", data2)
	}

	// 访问不存在的路径
	_, err = dm.Get([]string{"quotes", "INVALID"})
	if err != nil {
		fmt.Printf("预期的错误: %v\n", err)
	}

	fmt.Println("数据访问示例结束")
}

// 多路径监听示例
func MultiWatchExample() {
	fmt.Println("==================== 多路径监听示例 ====================")

	initialData := map[string]interface{}{
		"quotes": make(map[string]interface{}),
	}
	dm := tqsdk.NewDataManager(initialData)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 监听多个路径
	symbols := []string{"SHFE.au2512", "SHFE.ag2512", "DCE.m2505"}
	channels := make(map[string]<-chan interface{})

	for _, symbol := range symbols {
		ch, err := dm.Watch(ctx, []string{"quotes", symbol})
		if err != nil {
			fmt.Printf("监听 %s 失败: %v\n", symbol, err)
			continue
		}
		channels[symbol] = ch
	}

	// 启动多个 goroutine 接收数据
	for symbol, ch := range channels {
		symbol := symbol // 捕获变量
		go func(s string, c <-chan interface{}) {
			for data := range c {
				if quoteMap, ok := data.(map[string]interface{}); ok {
					fmt.Printf("📊 %s 更新: %.2f\n", s, quoteMap["last_price"])
				}
			}
		}(symbol, ch)
	}

	// 模拟批量更新
	time.Sleep(500 * time.Millisecond)
	dm.MergeData(map[string]interface{}{
		"quotes": map[string]interface{}{
			"SHFE.au2512": map[string]interface{}{"last_price": 500.0},
			"SHFE.ag2512": map[string]interface{}{"last_price": 50.0},
			"DCE.m2505":   map[string]interface{}{"last_price": 3000.0},
		},
	}, true, false)

	time.Sleep(500 * time.Millisecond)

	// 清理
	for _, symbol := range symbols {
		dm.UnWatch([]string{"quotes", symbol})
	}

	fmt.Println("多路径监听示例结束")
}

func main() {
	WatchExample()
	ConfigExample()
	DataAccessExample()
	MultiWatchExample()

	fmt.Println("所有示例运行完成!")
}
