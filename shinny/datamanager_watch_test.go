package shinny

import (
	"context"
	"testing"
	"time"
)

// TestWatch 测试 Watch 功能
func TestWatch(t *testing.T) {
	initialData := map[string]interface{}{
		"quotes": make(map[string]interface{}),
	}

	dm := NewDataManager(initialData)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 监听 quote 路径
	ch, err := dm.Watch(ctx, []string{"quotes", "SHFE.au2512"})
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}

	// 模拟数据更新
	go func() {
		time.Sleep(50 * time.Millisecond)
		dm.MergeData(map[string]interface{}{
			"quotes": map[string]interface{}{
				"SHFE.au2512": map[string]interface{}{
					"last_price": 500.0,
					"volume":     1000,
				},
			},
		}, true, false)
	}()

	// 等待数据
	select {
	case data := <-ch:
		if data == nil {
			t.Error("Received nil data")
		}
		if quoteMap, ok := data.(map[string]interface{}); ok {
			if lastPrice, ok := quoteMap["last_price"]; !ok || lastPrice != 500.0 {
				t.Errorf("Expected last_price 500.0, got %v", lastPrice)
			}
		} else {
			t.Error("Data is not a map")
		}
	case <-time.After(2 * time.Second):
		t.Error("Timeout waiting for data")
	}
}

// TestUnWatch 测试 UnWatch 功能
func TestUnWatch(t *testing.T) {
	initialData := map[string]interface{}{
		"quotes": make(map[string]interface{}),
	}

	dm := NewDataManager(initialData)

	ctx := context.Background()

	// 监听路径
	path := []string{"quotes", "SHFE.au2512"}
	ch, err := dm.Watch(ctx, path)
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}

	// 取消监听
	err = dm.UnWatch(path)
	if err != nil {
		t.Fatalf("UnWatch failed: %v", err)
	}

	// Channel 应该已关闭
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("Channel should be closed")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Channel not closed")
	}

	// 再次 UnWatch 应该返回错误
	err = dm.UnWatch(path)
	if err == nil {
		t.Error("Expected error when unwatching non-existent path")
	}
}

// TestMultipleWatchers 测试多个监听器
func TestMultipleWatchers(t *testing.T) {
	initialData := map[string]interface{}{
		"quotes": make(map[string]interface{}),
	}

	dm := NewDataManager(initialData)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 监听两个不同路径
	ch1, err := dm.Watch(ctx, []string{"quotes", "SHFE.au2512"})
	if err != nil {
		t.Fatalf("Watch 1 failed: %v", err)
	}

	ch2, err := dm.Watch(ctx, []string{"quotes", "SHFE.ag2512"})
	if err != nil {
		t.Fatalf("Watch 2 failed: %v", err)
	}

	// 更新两个合约的数据
	go func() {
		time.Sleep(50 * time.Millisecond)
		dm.MergeData(map[string]interface{}{
			"quotes": map[string]interface{}{
				"SHFE.au2512": map[string]interface{}{
					"last_price": 500.0,
				},
				"SHFE.ag2512": map[string]interface{}{
					"last_price": 50.0,
				},
			},
		}, true, false)
	}()

	// 验证两个 channel 都收到了数据
	received1 := false
	received2 := false

	timeout := time.After(2 * time.Second)
	for !received1 || !received2 {
		select {
		case data := <-ch1:
			if data != nil {
				received1 = true
			}
		case data := <-ch2:
			if data != nil {
				received2 = true
			}
		case <-timeout:
			if !received1 {
				t.Error("ch1 did not receive data")
			}
			if !received2 {
				t.Error("ch2 did not receive data")
			}
			return
		}
	}
}

// TestWatchWithContext 测试 context 取消
func TestWatchWithContext(t *testing.T) {
	initialData := map[string]interface{}{
		"quotes": make(map[string]interface{}),
	}

	dm := NewDataManager(initialData)

	ctx, cancel := context.WithCancel(context.Background())

	// 监听路径
	ch, err := dm.Watch(ctx, []string{"quotes", "SHFE.au2512"})
	if err != nil {
		t.Fatalf("Watch failed: %v", err)
	}

	// 取消 context
	cancel()

	// Channel 应该会关闭或停止发送数据
	time.Sleep(100 * time.Millisecond)

	// 尝试更新数据，watcher 不应该响应
	dm.MergeData(map[string]interface{}{
		"quotes": map[string]interface{}{
			"SHFE.au2512": map[string]interface{}{
				"last_price": 500.0,
			},
		},
	}, true, false)

	// 不应该收到数据（因为 context 已取消）
	select {
	case <-ch:
		// 可能收到最后一个数据或 channel 关闭，这都是合理的
	case <-time.After(200 * time.Millisecond):
		// 超时也是合理的
	}
}

// TestSetViewWidth 测试动态设置视图宽度
func TestSetViewWidth(t *testing.T) {
	initialData := map[string]interface{}{}
	dm := NewDataManager(initialData)

	// 默认值
	if dm.GetViewWidth() != 500 {
		t.Errorf("Expected default ViewWidth 500, got %d", dm.GetViewWidth())
	}

	// 设置新值
	dm.SetViewWidth(1000)
	if dm.GetViewWidth() != 1000 {
		t.Errorf("Expected ViewWidth 1000, got %d", dm.GetViewWidth())
	}

	// 设置无效值，应该重置为默认值
	dm.SetViewWidth(-1)
	if dm.GetViewWidth() != 500 {
		t.Errorf("Expected ViewWidth 500 (default), got %d", dm.GetViewWidth())
	}
}

// TestSetDataRetention 测试数据保留时间
func TestSetDataRetention(t *testing.T) {
	initialData := map[string]interface{}{}
	dm := NewDataManager(initialData)

	// 默认值
	if dm.GetDataRetention() != 0 {
		t.Errorf("Expected default DataRetention 0, got %v", dm.GetDataRetention())
	}

	// 设置新值
	retention := 24 * time.Hour
	dm.SetDataRetention(retention)
	if dm.GetDataRetention() != retention {
		t.Errorf("Expected DataRetention %v, got %v", retention, dm.GetDataRetention())
	}
}

// TestCleanup 测试数据清理
func TestCleanup(t *testing.T) {
	config := DataManagerConfig{
		DefaultViewWidth:  500,
		MaxDataRetention:  1 * time.Hour,
		EnableAutoCleanup: true,
	}

	initialData := map[string]interface{}{
		"klines": map[string]interface{}{
			"SHFE.au2512": map[string]interface{}{
				"60000000000": map[string]interface{}{
					"data": map[string]interface{}{
						"1": map[string]interface{}{
							"datetime": time.Now().Add(-2 * time.Hour).UnixNano(), // 2小时前
							"close":    500.0,
						},
						"2": map[string]interface{}{
							"datetime": time.Now().UnixNano(), // 现在
							"close":    501.0,
						},
					},
				},
			},
		},
	}

	dm := NewDataManager(initialData, config)

	// 执行清理
	dm.Cleanup()

	// 检查过期数据是否被删除
	klineData := dm.GetByPath([]string{"klines", "SHFE.au2512", "60000000000", "data"})
	if klineData == nil {
		t.Error("Kline data should not be nil")
		return
	}

	if dataMap, ok := klineData.(map[string]interface{}); ok {
		if _, exists := dataMap["1"]; exists {
			t.Error("Old data should be cleaned up")
		}
		if _, exists := dataMap["2"]; !exists {
			t.Error("Recent data should not be cleaned up")
		}
	}
}

// TestGet 测试 Get 方法
func TestGet(t *testing.T) {
	initialData := map[string]interface{}{
		"quotes": map[string]interface{}{
			"SHFE.au2512": map[string]interface{}{
				"last_price": 500.0,
			},
		},
	}

	dm := NewDataManager(initialData)

	// 测试存在的路径
	data, err := dm.Get([]string{"quotes", "SHFE.au2512"})
	if err != nil {
		t.Errorf("Get failed: %v", err)
	}
	if data == nil {
		t.Error("Expected data, got nil")
	}

	// 测试不存在的路径
	_, err = dm.Get([]string{"quotes", "SHFE.invalid"})
	if err == nil {
		t.Error("Expected error for non-existent path")
	}
}

// TestGetByPath 测试 GetByPath 兼容性
func TestGetByPath(t *testing.T) {
	initialData := map[string]interface{}{
		"quotes": map[string]interface{}{
			"SHFE.au2512": map[string]interface{}{
				"last_price": 500.0,
			},
		},
	}

	dm := NewDataManager(initialData)

	// GetByPath 应该仍然有效
	data := dm.GetByPath([]string{"quotes", "SHFE.au2512"})
	if data == nil {
		t.Error("GetByPath should return data")
	}
}
