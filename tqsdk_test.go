package tqsdk

import (
	"os"
	"testing"
	"time"
)

// TestNewTQSDK 测试创建 TQSDK 实例
func TestNewTQSDK(t *testing.T) {
	config := DefaultTQSDKConfig()
	config.AutoInit = false // 不自动初始化以避免网络请求

	userID := os.Getenv("SHINNYTECH_ID")
	password := os.Getenv("SHINNYTECH_PW")
	sdk := NewTQSDK(userID, password, config)
	if sdk == nil {
		t.Fatal("Failed to create TQSDK instance")
	}
	defer sdk.Close()

	if sdk.dm == nil {
		t.Error("DataManager not initialized")
	}

	if sdk.EventEmitter == nil {
		t.Error("EventEmitter not initialized")
	}
}

// TestEventEmitter 测试事件发射器
func TestEventEmitter(t *testing.T) {
	emitter := NewEventEmitter()

	called := false
	emitter.On(EventReady, func(data interface{}) {
		called = true
	})

	emitter.Emit(EventReady, nil)

	// 等待异步回调
	time.Sleep(100 * time.Millisecond)

	if !called {
		t.Error("Event handler not called")
	}
}

// TestDataManager 测试数据管理器
func TestDataManager(t *testing.T) {
	dm := NewDataManager(nil)

	// 测试 SetDefault
	dm.SetDefault([]string{"test", "key"}, "value")

	// 测试 GetByPath
	val := dm.GetByPath([]string{"test", "key"})
	if val != "value" {
		t.Errorf("Expected 'value', got %v", val)
	}

	// 测试 MergeData
	dm.MergeData(map[string]interface{}{
		"quotes": map[string]interface{}{
			"SHFE.au2406": map[string]interface{}{
				"last_price": 500.0,
			},
		},
	}, true, true)

	quote := dm.GetByPath([]string{"quotes", "SHFE.au2406"})
	if quote == nil {
		t.Error("Quote not found after merge")
	}

	// 测试 IsChanging
	if !dm.IsChanging([]string{"quotes", "SHFE.au2406"}) {
		t.Error("Quote should be marked as changed")
	}
}

// TestRandomStr 测试随机字符串生成
func TestRandomStr(t *testing.T) {
	str := RandomStr(8)
	if len(str) != 8 {
		t.Errorf("Expected length 8, got %d", len(str))
	}

	// 测试两次生成的字符串不同
	str2 := RandomStr(8)
	if str == str2 {
		t.Error("Two random strings should be different")
	}
}

// TestIsEmptyObject 测试空对象检查
func TestIsEmptyObject(t *testing.T) {
	if !IsEmptyObject(nil) {
		t.Error("nil should be empty")
	}

	if !IsEmptyObject(map[string]interface{}{}) {
		t.Error("Empty map should be empty")
	}

	if IsEmptyObject(map[string]interface{}{"key": "value"}) {
		t.Error("Non-empty map should not be empty")
	}
}

// TestLogger 测试日志系统
func TestLogger(t *testing.T) {
	config := LogConfig{
		Level:       "debug",
		OutputPath:  "stdout",
		Development: true,
	}

	logger, err := NewLogger(config)
	if err != nil {
		t.Fatalf("Failed to create logger: %v", err)
	}

	if logger == nil {
		t.Fatal("Logger is nil")
	}

	// 测试日志记录（不检查输出，只确保不崩溃）
	logger.Debug("Test debug message")
	logger.Info("Test info message")
	logger.Warn("Test warn message")
	logger.Error("Test error message")
}

// TestWebSocketConfig 测试 WebSocket 配置
func TestWebSocketConfig(t *testing.T) {
	config := DefaultWebSocketConfig()

	if config.ReconnectInterval <= 0 {
		t.Error("ReconnectInterval should be positive")
	}

	if config.ReconnectMaxTimes <= 0 {
		t.Error("ReconnectMaxTimes should be positive")
	}

	if config.Logger == nil {
		t.Error("Logger should not be nil")
	}
}

// BenchmarkRandomStr 基准测试随机字符串生成
func BenchmarkRandomStr(b *testing.B) {
	for i := 0; i < b.N; i++ {
		RandomStr(8)
	}
}

// BenchmarkDataManagerMerge 基准测试数据合并
func BenchmarkDataManagerMerge(b *testing.B) {
	dm := NewDataManager(nil)
	data := map[string]interface{}{
		"quotes": map[string]interface{}{
			"SHFE.au2406": map[string]interface{}{
				"last_price": 500.0,
				"bid_price1": 499.5,
				"ask_price1": 500.5,
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dm.MergeData(data, true, true)
	}
}
