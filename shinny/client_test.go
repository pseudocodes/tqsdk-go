package shinny

import (
	"context"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	// 基本的客户端创建测试（不需要真实的认证）
	ctx := context.Background()

	// 测试默认配置
	config := DefaultClientConfig("testuser", "testpass")
	if config.Username != "testuser" {
		t.Errorf("Expected username 'testuser', got '%s'", config.Username)
	}
	if config.Password != "testpass" {
		t.Errorf("Expected password 'testpass', got '%s'", config.Password)
	}
	if config.DataConfig.DefaultViewWidth != 500 {
		t.Errorf("Expected DefaultViewWidth 500, got %d", config.DataConfig.DefaultViewWidth)
	}

	// 测试选项函数
	testConfig := DefaultClientConfig("test", "test")
	WithViewWidth(1000)(&testConfig)
	if testConfig.DataConfig.DefaultViewWidth != 1000 {
		t.Errorf("Expected ViewWidth 1000, got %d", testConfig.DataConfig.DefaultViewWidth)
	}

	WithLogLevel("debug")(&testConfig)
	if testConfig.LogConfig.Level != "debug" {
		t.Errorf("Expected LogLevel 'debug', got '%s'", testConfig.LogConfig.Level)
	}

	// 注意：实际的 NewClient 测试需要真实的认证信息，这里只测试配置
	_ = ctx
}

func TestDataManagerConfig(t *testing.T) {
	config := DataManagerConfig{
		DefaultViewWidth:  100,
		EnableAutoCleanup: true,
	}

	if config.DefaultViewWidth != 100 {
		t.Errorf("Expected DefaultViewWidth 100, got %d", config.DefaultViewWidth)
	}
	if !config.EnableAutoCleanup {
		t.Error("Expected EnableAutoCleanup true")
	}
}

func TestDataManagerV2(t *testing.T) {
	initialData := map[string]interface{}{
		"quotes": make(map[string]interface{}),
		"klines": make(map[string]interface{}),
	}

	dm := NewDataManager(initialData)

	// 测试基本数据存储
	testData := map[string]interface{}{
		"quotes": map[string]interface{}{
			"SHFE.au2512": map[string]interface{}{
				"last_price": 500.0,
				"volume":     1000,
			},
		},
	}

	dm.MergeData(testData, true, false)

	// 测试数据获取
	quoteData := dm.GetByPath([]string{"quotes", "SHFE.au2512"})
	if quoteData == nil {
		t.Error("Expected quote data, got nil")
	}

	if quoteMap, ok := quoteData.(map[string]interface{}); ok {
		if lastPrice, ok := quoteMap["last_price"]; !ok || lastPrice != 500.0 {
			t.Errorf("Expected last_price 500.0, got %v", lastPrice)
		}
	} else {
		t.Error("Quote data is not a map")
	}
}

func TestToInt64(t *testing.T) {
	tests := []struct {
		input    interface{}
		expected int64
	}{
		{int64(100), 100},
		{int(50), 50},
		{float64(75.5), 75},
		{float32(25.3), 25},
		{"invalid", 0},
	}

	for _, test := range tests {
		result := toInt64(test.input)
		if result != test.expected {
			t.Errorf("toInt64(%v) = %d, expected %d", test.input, result, test.expected)
		}
	}
}

func TestSeriesData(t *testing.T) {
	// 测试单合约数据
	single := &KlineSeriesData{
		Symbol:   "SHFE.au2512",
		Duration: time.Minute,
		Data: []*Kline{
			{ID: 1, Close: 500.0},
			{ID: 2, Close: 501.0},
		},
	}

	seriesData := &SeriesData{
		IsMulti: false,
		IsTick:  false,
		Symbols: []string{"SHFE.au2512"},
		Single:  single,
	}

	result := seriesData.GetSymbolKlines("SHFE.au2512")
	if result == nil {
		t.Error("Expected kline data, got nil")
	}
	if len(result.Data) != 2 {
		t.Errorf("Expected 2 klines, got %d", len(result.Data))
	}

	// 测试多合约数据
	multi := &MultiKlineSeriesData{
		ChartID:    "test_chart",
		MainSymbol: "SHFE.au2512",
		Symbols:    []string{"SHFE.au2512", "SHFE.ag2512"},
		Data: []AlignedKlineSet{
			{
				MainID: 1,
				Klines: map[string]*Kline{
					"SHFE.au2512": {ID: 1, Close: 500.0},
					"SHFE.ag2512": {ID: 1, Close: 50.0},
				},
			},
		},
		Metadata: map[string]*KlineMetadata{
			"SHFE.au2512": {Symbol: "SHFE.au2512", LastID: 1},
			"SHFE.ag2512": {Symbol: "SHFE.ag2512", LastID: 1},
		},
	}

	multiSeriesData := &SeriesData{
		IsMulti: true,
		IsTick:  false,
		Symbols: []string{"SHFE.au2512", "SHFE.ag2512"},
		Multi:   multi,
	}

	auResult := multiSeriesData.GetSymbolKlines("SHFE.au2512")
	if auResult == nil {
		t.Error("Expected au kline data, got nil")
	}
	if len(auResult.Data) != 1 {
		t.Errorf("Expected 1 kline for au, got %d", len(auResult.Data))
	}

	agResult := multiSeriesData.GetSymbolKlines("SHFE.ag2512")
	if agResult == nil {
		t.Error("Expected ag kline data, got nil")
	}
	if len(agResult.Data) != 1 {
		t.Errorf("Expected 1 kline for ag, got %d", len(agResult.Data))
	}
}

func TestUpdateInfo(t *testing.T) {
	info := &UpdateInfo{
		HasNewBar: true,
		NewBarIDs: map[string]int64{
			"SHFE.au2512": 100,
		},
		HasBarUpdate:      false,
		ChartRangeChanged: true,
		OldLeftID:         50,
		OldRightID:        99,
		NewLeftID:         51,
		NewRightID:        100,
		HasChartSync:      false,
	}

	if !info.HasNewBar {
		t.Error("Expected HasNewBar true")
	}
	if info.NewBarIDs["SHFE.au2512"] != 100 {
		t.Errorf("Expected NewBarID 100, got %d", info.NewBarIDs["SHFE.au2512"])
	}
	if !info.ChartRangeChanged {
		t.Error("Expected ChartRangeChanged true")
	}
}

func TestInsertOrderRequest(t *testing.T) {
	req := &InsertOrderRequest{
		Symbol:     "SHFE.au2512",
		Direction:  DirectionBuy,
		Offset:     OffsetOpen,
		PriceType:  PriceTypeLimit,
		LimitPrice: 500.0,
		Volume:     1,
	}

	if req.Symbol != "SHFE.au2512" {
		t.Errorf("Expected symbol 'SHFE.au2512', got '%s'", req.Symbol)
	}
	if req.Direction != DirectionBuy {
		t.Errorf("Expected direction 'BUY', got '%s'", req.Direction)
	}
	if req.Volume != 1 {
		t.Errorf("Expected volume 1, got %d", req.Volume)
	}
}

func TestRandomStrV2(t *testing.T) {
	// 测试生成的随机字符串长度
	lengths := []int{5, 10, 20}
	for _, length := range lengths {
		str := RandomStr(length)
		if len(str) != length {
			t.Errorf("Expected string length %d, got %d", length, len(str))
		}
	}

	// 测试两次生成的字符串不同（概率极高）
	str1 := RandomStr(10)
	str2 := RandomStr(10)
	if str1 == str2 {
		t.Error("Two random strings should be different")
	}
}

func TestIsEmptyObjectV2(t *testing.T) {
	// 测试 nil
	if !IsEmptyObject(nil) {
		t.Error("nil should be empty")
	}

	// 测试空 map
	emptyMap := make(map[string]interface{})
	if !IsEmptyObject(emptyMap) {
		t.Error("Empty map should be empty")
	}

	// 测试非空 map
	nonEmptyMap := map[string]interface{}{"key": "value"}
	if IsEmptyObject(nonEmptyMap) {
		t.Error("Non-empty map should not be empty")
	}

	// 测试空 slice
	emptySlice := []string{}
	if !IsEmptyObject(emptySlice) {
		t.Error("Empty slice should be empty")
	}

	// 测试非空 slice
	nonEmptySlice := []string{"item"}
	if IsEmptyObject(nonEmptySlice) {
		t.Error("Non-empty slice should not be empty")
	}
}
