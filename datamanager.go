package tqsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DataManagerConfig 数据管理器配置
type DataManagerConfig struct {
	DefaultViewWidth  int           // 默认视图宽度
	MaxDataRetention  time.Duration // 最大数据保留时间
	EnableAutoCleanup bool          // 启用自动清理
}

// PathWatcher 路径监听器
type PathWatcher struct {
	path   []string
	ch     chan interface{}
	ctx    context.Context
	cancel context.CancelFunc
}

// DataManager 数据管理器，实现 DIFF 协议数据合并
type DataManager struct {
	mu     sync.RWMutex
	epoch  int64                    // 数据版本号
	data   map[string]interface{}   // 存储的数据
	diffs  []map[string]interface{} // 最近的差异数据
	config DataManagerConfig        // 配置

	// 事件回调
	onDataCallbacks []func()

	// 路径监听
	watchers   map[string]*PathWatcher // path key -> watcher
	watchersMu sync.RWMutex
}

// NewDataManager 创建新的数据管理器
func NewDataManager(initialData map[string]interface{}, config ...DataManagerConfig) *DataManager {
	if initialData == nil {
		initialData = make(map[string]interface{})
	}

	var cfg DataManagerConfig
	if len(config) > 0 {
		cfg = config[0]
	} else {
		cfg = DataManagerConfig{
			DefaultViewWidth:  102400,
			EnableAutoCleanup: true,
		}
	}

	dm := &DataManager{
		epoch:           0,
		data:            initialData,
		diffs:           make([]map[string]interface{}, 0),
		config:          cfg,
		onDataCallbacks: make([]func(), 0),
		watchers:        make(map[string]*PathWatcher),
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
	shouldNotifyWatchers := false

	func() {
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
			shouldNotifyWatchers = true
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
	}()

	// 在释放锁后触发 watchers
	if shouldNotifyWatchers {
		dm.notifyWatchers()
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
			if s, ok := value.(string); ok && (s == "NaN" || s == "-") {
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
func (dm *DataManager) GetKlinesData(symbol string, duration int64, viewWidth ...int) (*KlineSeriesData, error) {
	data := dm.GetByPath([]string{"klines", symbol, fmt.Sprintf("%d", duration)})
	if data == nil {
		return nil, fmt.Errorf("klines not found: %s/%d", symbol, duration)
	}

	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid klines data format")
	}

	klineSeriesData := &KlineSeriesData{
		Symbol:   symbol,
		Duration: time.Duration(duration),
		Data:     make([]*Kline, 0),
	}

	// 提取基本信息
	if tradingDayEndID, ok := dataMap["trading_day_end_id"]; ok {
		klineSeriesData.TradingDayEndID = toInt64(tradingDayEndID)
	}

	if tradingDayStartID, ok := dataMap["trading_day_start_id"]; ok {
		klineSeriesData.TradingDayStartID = toInt64(tradingDayStartID)
	}

	if lastID, ok := dataMap["last_id"]; ok {
		klineSeriesData.LastID = toInt64(lastID)
	}

	// 转换 Data map 为数组
	if dataField, ok := dataMap["data"]; ok {
		if klineMap, ok := dataField.(map[string]interface{}); ok {
			allKlines := make([]*Kline, 0, len(klineMap))

			for id, klineData := range klineMap {
				var kline Kline
				if err := dm.ConvertToStruct(klineData, &kline); err != nil {
					continue
				}
				// 将 string 类型的 id 转换为 int64
				if idInt, err := strconv.ParseInt(id, 10, 64); err == nil {
					kline.ID = idInt
				}
				allKlines = append(allKlines, &kline)
			}

			// 按照 ID 大小排序
			sort.Slice(allKlines, func(i, j int) bool {
				return allKlines[i].ID < allKlines[j].ID
			})

			// 【关键修复】获取 Chart 信息以过滤超出范围的数据
			// TQ 服务端会在数据末尾包含一个 last_id 的 K线用于实时更新
			// 在历史数据订阅时，需要过滤掉 ID > right_id 的数据
			var rightID int64 = -1
			charts := dm.GetByPath([]string{"charts"})
			if chartsMap, ok := charts.(map[string]interface{}); ok {
				for _, chartData := range chartsMap {
					if chartMap, ok := chartData.(map[string]interface{}); ok {
						if state, ok := chartMap["state"].(map[string]interface{}); ok {
							// 检查是否匹配当前订阅
							if insList, ok := state["ins_list"].(string); ok && strings.Contains(insList, symbol) {
								if dur, ok := state["duration"]; ok && toInt64(dur) == duration {
									if rid, ok := chartMap["right_id"]; ok {
										rightID = toInt64(rid)
									}
									break
								}
							}
						}
					}
				}
			}

			// 过滤掉超出 Chart 范围的数据（ID > right_id）
			// 使用二分查找优化：O(log n) 代替 O(n)
			if rightID > 0 {
				// 使用二分查找找到第一个 ID > rightID 的位置
				idx := sort.Search(len(allKlines), func(i int) bool {
					return allKlines[i].ID > rightID
				})
				// 截取 [0, idx) 范围的数据
				allKlines = allKlines[:idx]
			}

			// 应用 ViewWidth 限制（只保留最新的 viewWidth 条）
			vw := dm.config.DefaultViewWidth
			if len(viewWidth) > 0 && viewWidth[0] > 0 {
				vw = viewWidth[0]
			}

			if vw > 0 && len(allKlines) > vw {
				klineSeriesData.Data = allKlines[len(allKlines)-vw:]
			} else {
				klineSeriesData.Data = allKlines
			}
		}
	}

	return klineSeriesData, nil
}

// GetMultiKlinesData 获取多合约对齐的K线数据
func (dm *DataManager) GetMultiKlinesData(symbols []string, duration time.Duration, chartID string, viewWidth int) (*MultiKlineSeriesData, error) {
	if len(symbols) == 0 {
		return nil, fmt.Errorf("no symbols provided")
	}

	mainSymbol := symbols[0]

	// 获取 Chart 信息
	chartData := dm.GetByPath([]string{"charts", chartID})
	var leftID, rightID int64 = -1, -1

	if chartData != nil {
		if chartMap, ok := chartData.(map[string]interface{}); ok {
			if lid, ok := chartMap["left_id"]; ok {
				leftID = toInt64(lid)
			}
			if rid, ok := chartMap["right_id"]; ok {
				rightID = toInt64(rid)
			}
		}
	}

	result := &MultiKlineSeriesData{
		ChartID:    chartID,
		Duration:   duration,
		MainSymbol: mainSymbol,
		Symbols:    symbols,
		LeftID:     leftID,
		RightID:    rightID,
		ViewWidth:  viewWidth,
		Data:       make([]AlignedKlineSet, 0),
		Metadata:   make(map[string]*KlineMetadata),
	}

	// 获取主合约的K线数据
	mainKlinesData := dm.GetByPath([]string{"klines", mainSymbol, fmt.Sprintf("%d", duration)})
	if mainKlinesData == nil {
		return result, nil
	}

	mainKlinesMap, ok := mainKlinesData.(map[string]interface{})
	if !ok {
		return result, nil
	}

	// 获取主合约元数据
	for _, symbol := range symbols {
		klineData := dm.GetByPath([]string{"klines", symbol, fmt.Sprintf("%d", duration)})
		if klineData != nil {
			if klineMap, ok := klineData.(map[string]interface{}); ok {
				meta := &KlineMetadata{Symbol: symbol}
				if lastID, ok := klineMap["last_id"]; ok {
					meta.LastID = toInt64(lastID)
				}
				if startID, ok := klineMap["trading_day_start_id"]; ok {
					meta.TradingDayStartID = toInt64(startID)
				}
				if endID, ok := klineMap["trading_day_end_id"]; ok {
					meta.TradingDayEndID = toInt64(endID)
				}
				result.Metadata[symbol] = meta
			}
		}
	}

	// 获取主合约的 K线 map
	mainDataField, ok := mainKlinesMap["data"]
	if !ok {
		return result, nil
	}

	mainKlineMap, ok := mainDataField.(map[string]interface{})
	if !ok {
		return result, nil
	}

	// 获取 binding 信息
	bindings := make(map[string]map[int64]int64) // symbol -> (mainID -> otherID)
	if bindingData, ok := mainKlinesMap["binding"]; ok {
		if bindingMap, ok := bindingData.(map[string]interface{}); ok {
			for symbol, bindingInfo := range bindingMap {
				bindings[symbol] = make(map[int64]int64)
				if bindingIDMap, ok := bindingInfo.(map[string]interface{}); ok {
					for mainIDStr, otherIDVal := range bindingIDMap {
						if mainIDInt, err := strconv.ParseInt(mainIDStr, 10, 64); err == nil {
							bindings[symbol][mainIDInt] = toInt64(otherIDVal)
						}
					}
				}
			}
		}
	}

	// 收集所有主合约 ID 并排序
	mainIDs := make([]int64, 0, len(mainKlineMap))
	for idStr := range mainKlineMap {
		if idInt, err := strconv.ParseInt(idStr, 10, 64); err == nil {
			mainIDs = append(mainIDs, idInt)
		}
	}
	sort.Slice(mainIDs, func(i, j int) bool {
		return mainIDs[i] < mainIDs[j]
	})

	// 【关键修复】过滤掉超出 Chart 范围的数据（ID > right_id）
	// TQ 服务端会在数据末尾包含一个 last_id 的 K线用于实时更新
	// 使用二分查找优化：O(log n) 代替 O(n)
	if rightID > 0 {
		// 使用二分查找找到第一个 ID > rightID 的位置
		idx := sort.Search(len(mainIDs), func(i int) bool {
			return mainIDs[i] > rightID
		})
		// 截取 [0, idx) 范围的数据
		mainIDs = mainIDs[:idx]
	}

	// 应用 ViewWidth 限制
	if viewWidth > 0 && len(mainIDs) > viewWidth {
		mainIDs = mainIDs[len(mainIDs)-viewWidth:]
		result.LeftID = mainIDs[0]
		result.RightID = mainIDs[len(mainIDs)-1]
	}

	// 对齐所有合约的K线
	for _, mainID := range mainIDs {
		set := AlignedKlineSet{
			MainID: mainID,
			Klines: make(map[string]*Kline),
		}

		// 添加主合约K线
		if klineData, ok := mainKlineMap[fmt.Sprintf("%d", mainID)]; ok {
			var kline Kline
			if err := dm.ConvertToStruct(klineData, &kline); err == nil {
				kline.ID = mainID
				set.Klines[mainSymbol] = &kline
				set.Timestamp = time.Unix(0, kline.Datetime)
			}
		}

		// 添加其他合约的对齐K线
		for _, symbol := range symbols[1:] {
			if binding, ok := bindings[symbol]; ok {
				if mappedID, ok := binding[mainID]; ok {
					otherKlineData := dm.GetByPath([]string{"klines", symbol, fmt.Sprintf("%d", duration), "data", fmt.Sprintf("%d", mappedID)})
					if otherKlineData != nil {
						var kline Kline
						if err := dm.ConvertToStruct(otherKlineData, &kline); err == nil {
							kline.ID = mappedID
							set.Klines[symbol] = &kline
						}
					}
				}
			}
		}

		result.Data = append(result.Data, set)
	}

	return result, nil
}

// GetTicksData 获取Tick数据（数组形式）
func (dm *DataManager) GetTicksData(symbol string, viewWidth ...int) (*TickSeriesData, error) {
	data := dm.GetByPath([]string{"ticks", symbol})
	if data == nil {
		return nil, fmt.Errorf("ticks not found: %s", symbol)
	}

	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid ticks data format")
	}

	tickSeriesData := &TickSeriesData{
		Symbol: symbol,
		Data:   make([]*Tick, 0),
	}

	// 提取基本信息
	if lastID, ok := dataMap["last_id"]; ok {
		tickSeriesData.LastID = toInt64(lastID)
	}

	// 转换 Data map 为数组
	if dataField, ok := dataMap["data"]; ok {
		if tickMap, ok := dataField.(map[string]interface{}); ok {
			allTicks := make([]*Tick, 0, len(tickMap))

			for id, tickData := range tickMap {
				var tick Tick
				if err := dm.ConvertToStruct(tickData, &tick); err != nil {
					continue
				}
				// 将 string 类型的 id 转换为 int64
				if idInt, err := strconv.ParseInt(id, 10, 64); err == nil {
					tick.ID = idInt
				}
				allTicks = append(allTicks, &tick)
			}

			// 按照 ID 大小排序
			sort.Slice(allTicks, func(i, j int) bool {
				return allTicks[i].ID < allTicks[j].ID
			})

			// 【关键修复】获取 Chart 信息以过滤超出范围的数据
			// TQ 服务端会在数据末尾包含一个 last_id 的 Tick 用于实时更新
			// 在历史数据订阅时，需要过滤掉 ID > right_id 的数据
			var rightID int64 = -1
			charts := dm.GetByPath([]string{"charts"})
			if chartsMap, ok := charts.(map[string]interface{}); ok {
				for _, chartData := range chartsMap {
					if chartMap, ok := chartData.(map[string]interface{}); ok {
						if state, ok := chartMap["state"].(map[string]interface{}); ok {
							// 检查是否匹配当前订阅（Tick 的 duration 为 0）
							if insList, ok := state["ins_list"].(string); ok && strings.Contains(insList, symbol) {
								if dur, ok := state["duration"]; ok && toInt64(dur) == 0 {
									if rid, ok := chartMap["right_id"]; ok {
										rightID = toInt64(rid)
									}
									break
								}
							}
						}
					}
				}
			}

			// 过滤掉超出 Chart 范围的数据（ID > right_id）
			// 使用二分查找优化：O(log n) 代替 O(n)
			if rightID > 0 {
				// 使用二分查找找到第一个 ID > rightID 的位置
				idx := sort.Search(len(allTicks), func(i int) bool {
					return allTicks[i].ID > rightID
				})
				// 截取 [0, idx) 范围的数据
				allTicks = allTicks[:idx]
			}

			// 应用 ViewWidth 限制（只保留最新的 viewWidth 条）
			vw := dm.config.DefaultViewWidth
			if len(viewWidth) > 0 && viewWidth[0] > 0 {
				vw = viewWidth[0]
			}

			if vw > 0 && len(allTicks) > vw {
				tickSeriesData.Data = allTicks[len(allTicks)-vw:]
			} else {
				tickSeriesData.Data = allTicks
			}
		}
	}

	return tickSeriesData, nil
}

// toInt64 辅助函数：转换为 int64
func toInt64(val interface{}) int64 {
	switch v := val.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case float32:
		return int64(v)
	default:
		return 0
	}
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

// ==================== Watch/UnWatch 路径监听 ====================

// Watch 监听指定路径的数据变化
func (dm *DataManager) Watch(ctx context.Context, path []string) (<-chan interface{}, error) {
	pathKey := strings.Join(path, ".")

	dm.watchersMu.Lock()
	defer dm.watchersMu.Unlock()

	// 检查是否已存在
	if _, exists := dm.watchers[pathKey]; exists {
		return nil, fmt.Errorf("path already watched: %v", path)
	}

	watcherCtx, cancel := context.WithCancel(ctx)
	ch := make(chan interface{}, 10)

	watcher := &PathWatcher{
		path:   path,
		ch:     ch,
		ctx:    watcherCtx,
		cancel: cancel,
	}

	dm.watchers[pathKey] = watcher

	// 启动监听 goroutine
	go dm.watchPath(watcher)

	return ch, nil
}

// UnWatch 取消路径监听
func (dm *DataManager) UnWatch(path []string) error {
	pathKey := strings.Join(path, ".")

	dm.watchersMu.Lock()
	watcher, exists := dm.watchers[pathKey]
	if !exists {
		dm.watchersMu.Unlock()
		return fmt.Errorf("path not watched: %v", path)
	}
	delete(dm.watchers, pathKey)
	dm.watchersMu.Unlock()

	watcher.cancel()
	close(watcher.ch)

	return nil
}

// watchPath 监听路径变化的内部方法
func (dm *DataManager) watchPath(watcher *PathWatcher) {
	// Watch 不使用轮询，而是通过 notifyWatchers 推送
	// 这个 goroutine 只是等待 context 取消
	<-watcher.ctx.Done()
}

// notifyWatchers 通知所有 watchers
func (dm *DataManager) notifyWatchers() {
	dm.watchersMu.RLock()
	defer dm.watchersMu.RUnlock()

	for _, watcher := range dm.watchers {
		if dm.IsChanging(watcher.path) {
			data := dm.GetByPath(watcher.path)
			if data != nil {
				select {
				case watcher.ch <- data:
				default:
					// Channel 满了，跳过
				}
			}
		}
	}
}

// ==================== 动态配置方法 ====================

// SetViewWidth 设置默认视图宽度
func (dm *DataManager) SetViewWidth(width int) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if width <= 0 {
		width = 500 // 默认值
	}
	dm.config.DefaultViewWidth = width
}

// GetViewWidth 获取当前视图宽度
func (dm *DataManager) GetViewWidth() int {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	return dm.config.DefaultViewWidth
}

// SetDataRetention 设置数据保留时间
func (dm *DataManager) SetDataRetention(duration time.Duration) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	dm.config.MaxDataRetention = duration
}

// GetDataRetention 获取数据保留时间
func (dm *DataManager) GetDataRetention() time.Duration {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	return dm.config.MaxDataRetention
}

// Cleanup 清理过期数据
func (dm *DataManager) Cleanup() {
	if !dm.config.EnableAutoCleanup || dm.config.MaxDataRetention == 0 {
		return
	}

	dm.mu.Lock()
	defer dm.mu.Unlock()

	cutoffTime := time.Now().Add(-dm.config.MaxDataRetention)

	// 清理 klines 数据
	if klines, ok := dm.data["klines"].(map[string]interface{}); ok {
		for _, symbolData := range klines {
			if symMap, ok := symbolData.(map[string]interface{}); ok {
				for _, durData := range symMap {
					if durMap, ok := durData.(map[string]interface{}); ok {
						if dataField, ok := durMap["data"].(map[string]interface{}); ok {
							dm.cleanupKlineData(dataField, cutoffTime)
						}
					}
				}
			}
		}
	}

	// 清理 ticks 数据
	if ticks, ok := dm.data["ticks"].(map[string]interface{}); ok {
		for _, symbolData := range ticks {
			if symMap, ok := symbolData.(map[string]interface{}); ok {
				if dataField, ok := symMap["data"].(map[string]interface{}); ok {
					dm.cleanupTickData(dataField, cutoffTime)
				}
			}
		}
	}
}

// cleanupKlineData 清理 K线数据
func (dm *DataManager) cleanupKlineData(data map[string]interface{}, cutoff time.Time) {
	for id, klineData := range data {
		if kMap, ok := klineData.(map[string]interface{}); ok {
			if datetime, ok := kMap["datetime"]; ok {
				dt := toInt64(datetime)
				t := time.Unix(0, dt)
				if t.Before(cutoff) {
					delete(data, id)
				}
			}
		}
	}
}

// cleanupTickData 清理 Tick 数据
func (dm *DataManager) cleanupTickData(data map[string]interface{}, cutoff time.Time) {
	for id, tickData := range data {
		if tMap, ok := tickData.(map[string]interface{}); ok {
			if datetime, ok := tMap["datetime"]; ok {
				dt := toInt64(datetime)
				t := time.Unix(0, dt)
				if t.Before(cutoff) {
					delete(data, id)
				}
			}
		}
	}
}

// ==================== Get 方法标准化 ====================

// Get 根据路径获取数据（标准接口）
func (dm *DataManager) Get(path []string) (interface{}, error) {
	data := dm.GetByPath(path)
	if data == nil {
		return nil, fmt.Errorf("data not found at path: %v", path)
	}
	return data, nil
}

// Helper function to check if value is of type
func isType(value interface{}, kind reflect.Kind) bool {
	return reflect.TypeOf(value).Kind() == kind
}
