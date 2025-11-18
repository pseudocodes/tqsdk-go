package tqsdk

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// SeriesAPI 序列数据 API
type SeriesAPI struct {
	client        *Client
	dm            *DataManager
	ws            *TqQuoteWebsocket
	subscriptions map[string]*SeriesSubscription
	mu            sync.RWMutex
}

// NewSeriesAPI 创建序列数据 API
func NewSeriesAPI(client *Client, dm *DataManager, ws *TqQuoteWebsocket) *SeriesAPI {
	return &SeriesAPI{
		client:        client,
		dm:            dm,
		ws:            ws,
		subscriptions: make(map[string]*SeriesSubscription),
	}
}

// Kline 订阅单个合约的 K线
func (sa *SeriesAPI) Kline(ctx context.Context, symbol string, duration time.Duration, viewWidth int) (*SeriesSubscription, error) {
	return sa.Subscribe(ctx, SeriesOptions{
		Symbols:   []string{symbol},
		Duration:  duration,
		ViewWidth: viewWidth,
	})
}

// KlineMulti 订阅多个合约的 K线（同一个 Chart）
func (sa *SeriesAPI) KlineMulti(ctx context.Context, symbols []string, duration time.Duration, viewWidth int) (*SeriesSubscription, error) {
	if len(symbols) == 0 {
		return nil, NewError("KlineMulti", ErrInvalidSymbol)
	}

	return sa.Subscribe(ctx, SeriesOptions{
		Symbols:   symbols,
		Duration:  duration,
		ViewWidth: viewWidth,
	})
}

// Tick 订阅单个合约的 Tick
func (sa *SeriesAPI) Tick(ctx context.Context, symbol string, viewWidth int) (*SeriesSubscription, error) {
	return sa.Subscribe(ctx, SeriesOptions{
		Symbols:   []string{symbol},
		Duration:  0, // 0 表示 Tick
		ViewWidth: viewWidth,
	})
}

// KlineHistory 订阅单个合约的历史 K线（使用 left_kline_id）
func (sa *SeriesAPI) KlineHistory(ctx context.Context, symbol string, duration time.Duration, viewWidth int, leftKlineID int64) (*SeriesSubscription, error) {
	return sa.Subscribe(ctx, SeriesOptions{
		Symbols:     []string{symbol},
		Duration:    duration,
		ViewWidth:   viewWidth,
		LeftKlineID: &leftKlineID,
	})
}

// KlineHistoryWithFocus 订阅单个合约的历史 K线（使用 focus_datetime + focus_position）
// focusPosition: 1=从焦点时间向右扩展，-1=从焦点时间向左扩展
func (sa *SeriesAPI) KlineHistoryWithFocus(ctx context.Context, symbol string, duration time.Duration, viewWidth int, focusTime time.Time, focusPosition int) (*SeriesSubscription, error) {
	return sa.Subscribe(ctx, SeriesOptions{
		Symbols:       []string{symbol},
		Duration:      duration,
		ViewWidth:     viewWidth,
		FocusDatetime: &focusTime,
		FocusPosition: &focusPosition,
	})
}

// TickHistory 订阅单个合约的历史 Tick（使用 left_kline_id）
func (sa *SeriesAPI) TickHistory(ctx context.Context, symbol string, viewWidth int, leftKlineID int64) (*SeriesSubscription, error) {
	return sa.Subscribe(ctx, SeriesOptions{
		Symbols:     []string{symbol},
		Duration:    0,
		ViewWidth:   viewWidth,
		LeftKlineID: &leftKlineID,
	})
}

// Subscribe 通用订阅方法
func (sa *SeriesAPI) Subscribe(ctx context.Context, options SeriesOptions) (*SeriesSubscription, error) {
	if len(options.Symbols) == 0 {
		return nil, NewError("Subscribe", ErrInvalidSymbol)
	}

	if options.ViewWidth <= 0 {
		options.ViewWidth = sa.client.config.DataConfig.DefaultViewWidth
	}

	// 生成 Chart ID
	if options.ChartID == "" {
		options.ChartID = generateChartID(options)
	}

	sa.mu.Lock()
	defer sa.mu.Unlock()

	// 检查是否已存在
	if sub, exists := sa.subscriptions[options.ChartID]; exists {
		return sub, nil
	}

	// 创建新订阅
	sub, err := NewSeriesSubscription(ctx, sa.client, sa.dm, sa.ws, options)
	if err != nil {
		return nil, err
	}

	sa.subscriptions[options.ChartID] = sub
	return sub, nil
}

// generateChartID 生成 Chart ID（使用 UUID 后缀保证唯一性）
func generateChartID(options SeriesOptions) string {
	uid := uuid.New().String()
	if options.Duration == 0 {
		return fmt.Sprintf("TQGO_tick_%s", uid)
	}
	return fmt.Sprintf("TQGO_kline_%s", uid)
}

// ==================== SeriesSubscription ====================

// SeriesSubscription 序列订阅
type SeriesSubscription struct {
	client  *Client
	dm      *DataManager
	ws      *TqQuoteWebsocket
	ctx     context.Context
	cancel  context.CancelFunc
	options SeriesOptions

	// 回调函数
	onUpdate    func(*SeriesData, *UpdateInfo)
	onNewBar    func(*SeriesData) // 新 K线/Tick 回调，传递完整序列数据用于计算指标
	onBarUpdate func(*SeriesData) // K线/Tick 更新回调，传递完整序列数据用于计算指标
	onError     func(error)

	// 状态跟踪
	lastIDs     map[string]int64 // symbol -> lastID
	lastLeftID  int64
	lastRightID int64
	chartReady  bool

	mu      sync.RWMutex
	running bool
	wg      sync.WaitGroup
}

// NewSeriesSubscription 创建序列订阅
func NewSeriesSubscription(ctx context.Context, client *Client, dm *DataManager, ws *TqQuoteWebsocket, options SeriesOptions) (*SeriesSubscription, error) {
	subCtx, cancel := context.WithCancel(ctx)

	sub := &SeriesSubscription{
		client:      client,
		dm:          dm,
		ws:          ws,
		ctx:         subCtx,
		cancel:      cancel,
		options:     options,
		lastIDs:     make(map[string]int64),
		lastLeftID:  -1,
		lastRightID: -1,
		running:     true,
	}

	// 初始化 lastIDs
	for _, symbol := range options.Symbols {
		sub.lastIDs[symbol] = -1
	}

	// 发送 set_chart 请求
	if err := sub.sendSetChart(); err != nil {
		cancel()
		return nil, err
	}

	// 启动监听
	sub.wg.Add(1)
	go sub.watch()

	return sub, nil
}

// sendSetChart 发送 set_chart 请求
func (sub *SeriesSubscription) sendSetChart() error {
	// 检查 view_width 限制
	viewWidth := sub.options.ViewWidth
	if viewWidth > 10000 {
		viewWidth = 10000
		if sub.client.logger != nil {
			sub.client.logger.Warn("ViewWidth exceeds maximum limit, adjusted to 10000",
				zap.Int("requested", sub.options.ViewWidth))
		}
	}

	chartReq := map[string]interface{}{
		"aid":        "set_chart",
		"chart_id":   sub.options.ChartID,
		"ins_list":   strings.Join(sub.options.Symbols, ","),
		"duration":   int64(sub.options.Duration),
		"view_width": viewWidth,
	}

	// 添加历史数据订阅参数（优先级：LeftKlineID > FocusDatetime+FocusPosition）
	if sub.options.LeftKlineID != nil {
		chartReq["left_kline_id"] = *sub.options.LeftKlineID
	} else if sub.options.FocusDatetime != nil && sub.options.FocusPosition != nil {
		chartReq["focus_datetime"] = sub.options.FocusDatetime.UnixNano()
		chartReq["focus_position"] = *sub.options.FocusPosition
	}

	if sub.ws != nil {
		sub.ws.Send(chartReq)

		if sub.client.logger != nil {
			sub.client.logger.Info("Sent set_chart request",
				zap.String("chart_id", sub.options.ChartID),
				zap.Strings("symbols", sub.options.Symbols),
				zap.Int("view_width", viewWidth))
		}
	}

	return nil
}

// OnUpdate 注册通用更新回调（包含详细的更新信息）
func (sub *SeriesSubscription) OnUpdate(handler func(*SeriesData, *UpdateInfo)) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	sub.onUpdate = handler
}

// OnNewBar 注册新 K线/Tick 回调
// 回调函数接收完整的序列数据，便于计算技术指标
func (sub *SeriesSubscription) OnNewBar(handler func(*SeriesData)) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	sub.onNewBar = handler
}

// OnBarUpdate 注册 K线/Tick 更新回调（盘中实时更新）
// 回调函数接收完整的序列数据，便于计算技术指标
func (sub *SeriesSubscription) OnBarUpdate(handler func(*SeriesData)) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	sub.onBarUpdate = handler
}

// OnError 注册错误回调
func (sub *SeriesSubscription) OnError(handler func(error)) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	sub.onError = handler
}

// watch 监听数据更新
func (sub *SeriesSubscription) watch() {
	defer sub.wg.Done()

	// 注册数据更新回调
	sub.dm.OnData(func() {
		sub.processUpdate()
	})

	// 等待上下文取消
	<-sub.ctx.Done()
}

// processUpdate 处理数据更新
func (sub *SeriesSubscription) processUpdate() {
	sub.mu.Lock()
	onUpdate := sub.onUpdate
	onNewBar := sub.onNewBar
	onBarUpdate := sub.onBarUpdate
	sub.mu.Unlock()

	// 检查 Chart 或数据是否有更新
	isMulti := len(sub.options.Symbols) > 1
	isTick := sub.options.Duration == 0

	var chartPath, dataPath []string
	if isTick {
		dataPath = []string{"ticks", sub.options.Symbols[0]}
		chartPath = []string{"charts", sub.options.ChartID}
	} else {
		if isMulti {
			dataPath = []string{"klines", sub.options.Symbols[0], fmt.Sprintf("%d", sub.options.Duration)}
		} else {
			dataPath = []string{"klines", sub.options.Symbols[0], fmt.Sprintf("%d", sub.options.Duration)}
		}
		chartPath = []string{"charts", sub.options.ChartID}
	}

	// 检查是否有更新
	hasDataChange := sub.dm.IsChanging(dataPath)
	hasChartChange := sub.dm.IsChanging(chartPath)

	if !hasDataChange && !hasChartChange {
		return
	}

	// 构建 UpdateInfo
	updateInfo := &UpdateInfo{
		NewBarIDs: make(map[string]int64),
	}

	// 获取数据
	var seriesData *SeriesData
	var err error

	if isTick {
		seriesData, err = sub.getTickData()
	} else if isMulti {
		seriesData, err = sub.getMultiKlineData()
	} else {
		seriesData, err = sub.getSingleKlineData()
	}

	if err != nil {
		if sub.onError != nil {
			go sub.onError(err)
		}
		return
	}

	// 检测新 K线
	sub.detectNewBars(seriesData, updateInfo)

	// 检测 Chart 范围变化
	sub.detectChartRangeChange(seriesData, updateInfo)

	// 调用回调
	if onUpdate != nil {
		go onUpdate(seriesData, updateInfo)
	}

	// 调用 OnNewBar 回调（传递完整序列数据）
	if onNewBar != nil && updateInfo.HasNewBar {
		go onNewBar(seriesData)
	}

	// 调用 OnBarUpdate 回调（传递完整序列数据）
	if onBarUpdate != nil && updateInfo.HasBarUpdate && !updateInfo.HasNewBar {
		go onBarUpdate(seriesData)
	}
}

// detectNewBars 检测新 K线
func (sub *SeriesSubscription) detectNewBars(data *SeriesData, info *UpdateInfo) {
	for _, symbol := range sub.options.Symbols {
		var currentID int64

		if data.IsTick && data.TickData != nil {
			currentID = data.TickData.LastID
		} else if data.IsMulti && data.Multi != nil {
			if meta, ok := data.Multi.Metadata[symbol]; ok {
				currentID = meta.LastID
			}
		} else if data.Single != nil {
			currentID = data.Single.LastID
		}

		lastID := sub.lastIDs[symbol]
		if currentID > lastID && lastID != -1 {
			info.HasNewBar = true
			info.NewBarIDs[symbol] = currentID
		}

		sub.lastIDs[symbol] = currentID
	}

	// 检测是否有 K线数据更新
	if !info.HasNewBar {
		for _, symbol := range sub.options.Symbols {
			if sub.dm.IsChanging([]string{"klines", symbol, fmt.Sprintf("%d", sub.options.Duration)}) {
				info.HasBarUpdate = true
				break
			}
		}
	}
}

// detectChartRangeChange 检测 Chart 范围变化
func (sub *SeriesSubscription) detectChartRangeChange(data *SeriesData, info *UpdateInfo) {
	var chart *ChartInfo

	if data.Single != nil {
		chart = data.Single.Chart
	} else if data.Multi != nil {
		// 从 dm 获取 chart
		chartData := sub.dm.GetByPath([]string{"charts", sub.options.ChartID})
		if chartData != nil {
			var c ChartInfo
			if err := sub.dm.ConvertToStruct(chartData, &c); err == nil {
				chart = &c
			}
		}
	}

	if chart != nil {
		if chart.LeftID != sub.lastLeftID || chart.RightID != sub.lastRightID {
			if sub.lastLeftID != -1 || sub.lastRightID != -1 {
				info.ChartRangeChanged = true
				info.OldLeftID = sub.lastLeftID
				info.OldRightID = sub.lastRightID
				info.NewLeftID = chart.LeftID
				info.NewRightID = chart.RightID
			}
			sub.lastLeftID = chart.LeftID
			sub.lastRightID = chart.RightID
		}

		if chart.Ready && !sub.chartReady {
			info.HasChartSync = true
			sub.chartReady = true
		}

		// 检测 Chart 数据传输是否完成（分片传输场景）
		// 当 more_data = false 且 ready = true 时，表示所有分片数据已接收完毕
		if chart.Ready && !chart.MoreData {
			info.ChartReady = true

			if sub.client.logger != nil {
				sub.client.logger.Info("Chart data transfer completed",
					zap.String("chart_id", sub.options.ChartID),
					zap.Int64("left_id", chart.LeftID),
					zap.Int64("right_id", chart.RightID),
					zap.Int("data_count", int(chart.RightID-chart.LeftID+1)))
			}
		}
	}
}

// getSingleKlineData 获取单合约 K线数据
func (sub *SeriesSubscription) getSingleKlineData() (*SeriesData, error) {
	symbol := sub.options.Symbols[0]
	klineData, err := sub.dm.GetKlinesData(symbol, sub.options.Duration.Nanoseconds(), sub.options.ViewWidth)
	if err != nil {
		return nil, err
	}

	// 获取 Chart 信息
	chartData := sub.dm.GetByPath([]string{"charts", sub.options.ChartID})
	if chartData != nil {
		var chart ChartInfo
		if err := sub.dm.ConvertToStruct(chartData, &chart); err == nil {
			klineData.Chart = &chart
			klineData.Chart.ChartID = sub.options.ChartID
		}
	}

	return &SeriesData{
		IsMulti: false,
		IsTick:  false,
		Symbols: []string{symbol},
		Single:  klineData,
	}, nil
}

// getMultiKlineData 获取多合约 K线数据
func (sub *SeriesSubscription) getMultiKlineData() (*SeriesData, error) {
	multiData, err := sub.dm.GetMultiKlinesData(sub.options.Symbols, sub.options.Duration, sub.options.ChartID, sub.options.ViewWidth)
	if err != nil {
		return nil, err
	}

	return &SeriesData{
		IsMulti: true,
		IsTick:  false,
		Symbols: sub.options.Symbols,
		Multi:   multiData,
	}, nil
}

// getTickData 获取 Tick 数据
func (sub *SeriesSubscription) getTickData() (*SeriesData, error) {
	symbol := sub.options.Symbols[0]
	tickData, err := sub.dm.GetTicksData(symbol, sub.options.ViewWidth)
	if err != nil {
		return nil, err
	}

	// 获取 Chart 信息
	chartData := sub.dm.GetByPath([]string{"charts", sub.options.ChartID})
	if chartData != nil {
		var chart ChartInfo
		if err := sub.dm.ConvertToStruct(chartData, &chart); err == nil {
			tickData.Chart = &chart
			tickData.Chart.ChartID = sub.options.ChartID
		}
	}

	return &SeriesData{
		IsMulti:  false,
		IsTick:   true,
		Symbols:  []string{symbol},
		TickData: tickData,
	}, nil
}

// Close 关闭订阅
func (sub *SeriesSubscription) Close() error {
	sub.mu.Lock()
	if !sub.running {
		sub.mu.Unlock()
		return nil
	}
	sub.running = false
	sub.mu.Unlock()

	// 发送取消请求
	if sub.ws != nil {
		sub.ws.Send(map[string]interface{}{
			"aid":        "set_chart",
			"chart_id":   sub.options.ChartID,
			"ins_list":   "",
			"duration":   int64(sub.options.Duration),
			"view_width": 0,
		})
	}

	sub.cancel()
	sub.wg.Wait()

	return nil
}
