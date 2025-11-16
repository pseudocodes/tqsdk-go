package tqsdk

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"sync"
)

// DataManager 数据管理器，实现 DIFF 协议数据合并
type DataManager struct {
	mu    sync.RWMutex
	epoch int64                    // 数据版本号
	data  map[string]interface{}   // 存储的数据
	diffs []map[string]interface{} // 最近的差异数据

	// 事件回调
	onDataCallbacks []func()
}

// NewDataManager 创建新的数据管理器
func NewDataManager(initialData map[string]interface{}) *DataManager {
	if initialData == nil {
		initialData = make(map[string]interface{})
	}

	dm := &DataManager{
		epoch:           0,
		data:            initialData,
		diffs:           make([]map[string]interface{}, 0),
		onDataCallbacks: make([]func(), 0),
	}

	return dm
}

// OnData 注册数据更新回调
func (dm *DataManager) OnData(callback func()) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	dm.onDataCallbacks = append(dm.onDataCallbacks, callback)
}

// MergeData 合并数据，实现 DIFF 协议
func (dm *DataManager) MergeData(source interface{}, epochIncrease bool, deleteNullObj bool) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	// 将 source 转换为数组
	var sourceArr []map[string]interface{}
	switch v := source.(type) {
	case []map[string]interface{}:
		sourceArr = v
	case map[string]interface{}:
		sourceArr = []map[string]interface{}{v}
	case []interface{}:
		sourceArr = make([]map[string]interface{}, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				sourceArr = append(sourceArr, m)
			}
		}
	default:
		// 尝试通过 JSON 转换
		data, err := json.Marshal(source)
		if err != nil {
			return
		}
		var m map[string]interface{}
		if err := json.Unmarshal(data, &m); err != nil {
			return
		}
		sourceArr = []map[string]interface{}{m}
	}

	if epochIncrease {
		dm.epoch++
		dm.diffs = sourceArr
	}

	for _, item := range sourceArr {
		if len(item) == 0 {
			continue
		}
		dm.mergeObject(dm.data, item, dm.epoch, deleteNullObj)
	}

	// 如果数据有更新，触发回调
	if epochIncrease {
		if dataEpoch, ok := dm.getEpoch(dm.data); ok && dataEpoch == dm.epoch {
			for _, callback := range dm.onDataCallbacks {
				go callback() // 异步调用回调
			}
		}
	}
}

// mergeObject 递归合并对象
func (dm *DataManager) mergeObject(target map[string]interface{}, source map[string]interface{}, epoch int64, deleteNullObj bool) {
	for property, value := range source {

		if value == nil {
			if deleteNullObj {
				delete(target, property)
			}
			continue
		}

		switch v := value.(type) {
		case string, bool, int, int32, int64, float32, float64:
			// 基本类型直接赋值
			if s, ok := value.(string); ok && s == "NaN" {
				target[property] = nil // 或者使用 math.NaN()
			} else {
				target[property] = value
			}

		case []interface{}:
			// 数组类型直接替换
			target[property] = value
			dm.setEpoch(value, epoch)

		case map[string]interface{}:
			// 对象类型递归合并
			if _, exists := target[property]; !exists {
				target[property] = make(map[string]interface{})
			}
			// quotes 对象特殊处理
			if property == "quotes" {
				dm.mergeQuotes(target, v, epoch, deleteNullObj)
			} else {
				if targetMap, ok := target[property].(map[string]interface{}); ok {
					dm.mergeObject(targetMap, v, epoch, deleteNullObj)
				}
			}
		}
	}

	// 设置 epoch
	dm.setEpoch(target, epoch)
}

// mergeQuotes 特殊处理 quotes 对象
func (dm *DataManager) mergeQuotes(target map[string]interface{}, quotes map[string]interface{}, epoch int64, deleteNullObj bool) {
	quotesMap, ok := target["quotes"].(map[string]interface{})
	if !ok {
		quotesMap = make(map[string]interface{})
		target["quotes"] = quotesMap
	}

	for symbol, quoteData := range quotes {
		if quoteData == nil {
			if deleteNullObj {
				delete(quotesMap, symbol)
			}
			continue
		}

		if quoteMap, ok := quoteData.(map[string]interface{}); ok {
			if _, exists := quotesMap[symbol]; !exists {
				// 创建新的 Quote 对象
				quotesMap[symbol] = make(map[string]interface{})
			}

			if targetQuote, ok := quotesMap[symbol].(map[string]interface{}); ok {
				dm.mergeObject(targetQuote, quoteMap, epoch, deleteNullObj)
			}
		}
	}
}

// IsChanging 判断指定路径的数据是否在最近一次更新中发生了变化
func (dm *DataManager) IsChanging(pathArray []string) bool {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	d := dm.data
	for i, key := range pathArray {
		val, exists := d[key]
		if !exists {
			return false
		}

		// 检查当前层级的 epoch
		if epoch, ok := dm.getEpoch(val); ok && epoch == dm.epoch {
			return true
		}

		// 继续往下查找
		if i < len(pathArray)-1 {
			if m, ok := val.(map[string]interface{}); ok {
				d = m
			} else {
				return false
			}
		}
	}

	return false
}

// GetByPath 根据路径获取数据
func (dm *DataManager) GetByPath(pathArray []string) interface{} {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	d := dm.data
	for i, key := range pathArray {
		val, exists := d[key]
		if !exists {
			return nil
		}

		if i == len(pathArray)-1 {
			return val
		}

		if m, ok := val.(map[string]interface{}); ok {
			d = m
		} else {
			return nil
		}
	}

	return d
}

// SetDefault 设置默认值，如果路径不存在则创建
func (dm *DataManager) SetDefault(pathArray []string, defaultValue interface{}) interface{} {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	node := dm.data
	for i, key := range pathArray {
		if i == len(pathArray)-1 {
			// 最后一个key
			if _, exists := node[key]; !exists {
				node[key] = defaultValue
			}
			return node[key]
		}

		// 中间节点
		if _, exists := node[key]; !exists {
			node[key] = make(map[string]interface{})
		}

		if nextNode, ok := node[key].(map[string]interface{}); ok {
			node = nextNode
		} else {
			// 类型不匹配，无法继续
			return nil
		}
	}

	return node
}

// GetEpoch 获取当前数据版本号
func (dm *DataManager) GetEpoch() int64 {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	return dm.epoch
}

// GetDiffs 获取最近的差异数据
func (dm *DataManager) GetDiffs() []map[string]interface{} {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	return dm.diffs
}

// setEpoch 设置对象的 epoch（通过特殊字段 _epoch）
func (dm *DataManager) setEpoch(obj interface{}, epoch int64) {
	switch v := obj.(type) {
	case map[string]interface{}:
		v["_epoch"] = epoch
	case []interface{}:
		// 对于数组，可以尝试设置，但通常不直接支持
		// 在实际使用中可能需要包装
	}
}

// getEpoch 获取对象的 epoch
func (dm *DataManager) getEpoch(obj interface{}) (int64, bool) {
	if m, ok := obj.(map[string]interface{}); ok {
		if epoch, exists := m["_epoch"]; exists {
			switch e := epoch.(type) {
			case int64:
				return e, true
			case int:
				return int64(e), true
			case float64:
				return int64(e), true
			}
		}
	}
	return 0, false
}

// ConvertToStruct 将 map 转换为结构体
func (dm *DataManager) ConvertToStruct(data interface{}, target interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal error: %w", err)
	}

	if err := json.Unmarshal(jsonData, target); err != nil {
		return fmt.Errorf("unmarshal error: %w", err)
	}

	return nil
}

// Clone 深拷贝数据
func (dm *DataManager) Clone(src interface{}) (interface{}, error) {
	data, err := json.Marshal(src)
	if err != nil {
		return nil, err
	}

	var dst interface{}
	if err := json.Unmarshal(data, &dst); err != nil {
		return nil, err
	}

	return dst, nil
}

// Dump 导出所有数据（用于调试）
func (dm *DataManager) Dump() map[string]interface{} {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	result, _ := dm.Clone(dm.data)
	if m, ok := result.(map[string]interface{}); ok {
		return m
	}
	return make(map[string]interface{})
}

// GetData 获取原始数据（注意：返回的是引用，使用时需要加锁）
func (dm *DataManager) GetData() map[string]interface{} {
	return dm.data
}

// GetQuoteData 获取行情数据并转换为 Quote 结构体
func (dm *DataManager) GetQuoteData(symbol string) (*Quote, error) {
	data := dm.GetByPath([]string{"quotes", symbol})
	if data == nil {
		return nil, fmt.Errorf("quote not found: %s", symbol)
	}

	var quote Quote
	if err := dm.ConvertToStruct(data, &quote); err != nil {
		return nil, err
	}

	return &quote, nil
}

// GetKlinesData 获取K线数据（数组形式）
func (dm *DataManager) GetKlinesData(symbol string, duration int64) (*KlineSeriesData, error) {
	data := dm.GetByPath([]string{"klines", symbol, fmt.Sprintf("%d", duration)})
	if data == nil {
		return nil, fmt.Errorf("klines not found: %s/%d", symbol, duration)
	}

	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid klines data format")
	}

	klineSeriesData := &KlineSeriesData{
		Data: make([]*Kline, 0),
	}

	// 提取基本信息
	if tradingDayEndID, ok := dataMap["trading_day_end_id"]; ok {
		switch v := tradingDayEndID.(type) {
		case int64:
			klineSeriesData.TradingDayEndID = v
		case float64:
			klineSeriesData.TradingDayEndID = int64(v)
		}
	}

	if tradingDayStartID, ok := dataMap["trading_day_start_id"]; ok {
		switch v := tradingDayStartID.(type) {
		case int64:
			klineSeriesData.TradingDayStartID = v
		case float64:
			klineSeriesData.TradingDayStartID = int64(v)
		}
	}

	if lastID, ok := dataMap["last_id"]; ok {
		switch v := lastID.(type) {
		case int64:
			klineSeriesData.LastID = v
		case float64:
			klineSeriesData.LastID = int64(v)
		}
	}

	// 转换 Data map 为数组
	if dataField, ok := dataMap["data"]; ok {
		if klineMap, ok := dataField.(map[string]interface{}); ok {
			for id, klineData := range klineMap {
				var kline Kline
				if err := dm.ConvertToStruct(klineData, &kline); err != nil {
					continue
				}
				// 将 string 类型的 id 转换为 int64
				if idInt, err := strconv.ParseInt(id, 10, 64); err == nil {
					kline.ID = idInt
				}
				klineSeriesData.Data = append(klineSeriesData.Data, &kline)
			}

			// 按照 ID 大小排序
			sort.Slice(klineSeriesData.Data, func(i, j int) bool {
				return klineSeriesData.Data[i].ID < klineSeriesData.Data[j].ID
			})
		}
	}

	return klineSeriesData, nil
}

// GetTicksData 获取Tick数据（数组形式）
func (dm *DataManager) GetTicksData(symbol string) (*TickSeriesData, error) {
	data := dm.GetByPath([]string{"ticks", symbol})
	if data == nil {
		return nil, fmt.Errorf("ticks not found: %s", symbol)
	}

	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid ticks data format")
	}

	tickSeriesData := &TickSeriesData{
		Data: make([]*Tick, 0),
	}

	// 提取基本信息
	if lastID, ok := dataMap["last_id"]; ok {
		switch v := lastID.(type) {
		case int64:
			tickSeriesData.LastID = v
		case float64:
			tickSeriesData.LastID = int64(v)
		}
	}

	// 转换 Data map 为数组
	if dataField, ok := dataMap["data"]; ok {
		if tickMap, ok := dataField.(map[string]interface{}); ok {
			for id, tickData := range tickMap {
				var tick Tick
				if err := dm.ConvertToStruct(tickData, &tick); err != nil {
					continue
				}
				// 将 string 类型的 id 转换为 int64
				if idInt, err := strconv.ParseInt(id, 10, 64); err == nil {
					tick.ID = idInt
				}
				tickSeriesData.Data = append(tickSeriesData.Data, &tick)
			}

			// 按照 ID 大小排序
			sort.Slice(tickSeriesData.Data, func(i, j int) bool {
				return tickSeriesData.Data[i].ID < tickSeriesData.Data[j].ID
			})
		}
	}

	return tickSeriesData, nil
}

// GetAccountData 获取账户数据
func (dm *DataManager) GetAccountData(userID string, currency string) (*Account, error) {
	data := dm.GetByPath([]string{"trade", userID, "accounts", currency})
	if data == nil {
		return nil, fmt.Errorf("account not found: %s/%s", userID, currency)
	}

	var account Account
	if err := dm.ConvertToStruct(data, &account); err != nil {
		return nil, err
	}

	return &account, nil
}

// GetPositionData 获取持仓数据
func (dm *DataManager) GetPositionData(userID string, symbol string) (*Position, error) {
	data := dm.GetByPath([]string{"trade", userID, "positions", symbol})
	if data == nil {
		return nil, fmt.Errorf("position not found: %s/%s", userID, symbol)
	}

	var position Position
	if err := dm.ConvertToStruct(data, &position); err != nil {
		return nil, err
	}

	return &position, nil
}

// GetOrderData 获取委托单数据
func (dm *DataManager) GetOrderData(userID string, orderID string) (*Order, error) {
	data := dm.GetByPath([]string{"trade", userID, "orders", orderID})
	if data == nil {
		return nil, fmt.Errorf("order not found: %s/%s", userID, orderID)
	}

	var order Order
	if err := dm.ConvertToStruct(data, &order); err != nil {
		return nil, err
	}

	return &order, nil
}

// GetTradeData 获取成交数据
func (dm *DataManager) GetTradeData(userID string, tradeID string) (*Trade, error) {
	data := dm.GetByPath([]string{"trade", userID, "trades", tradeID})
	if data == nil {
		return nil, fmt.Errorf("trade not found: %s/%s", userID, tradeID)
	}

	var trade Trade
	if err := dm.ConvertToStruct(data, &trade); err != nil {
		return nil, err
	}

	return &trade, nil
}

// Helper function to check if value is of type
func isType(value interface{}, kind reflect.Kind) bool {
	return reflect.TypeOf(value).Kind() == kind
}
