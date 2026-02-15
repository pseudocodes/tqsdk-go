# Quote 行情订阅示例

本示例展示了如何使用 TQSDK Go V2 订阅行情数据，包括 Quote、K线、Tick 等。

## 功能演示

### 1. Quote 订阅（全局实时行情）

演示两种订阅模式：

#### 流式接口（Channel）
```go
quoteSub, _ := client.SubscribeQuote(ctx, "SHFE.au2602", "SHFE.ag2512")

go func() {
    for quote := range quoteSub.QuoteChannel() {
        fmt.Printf("%s: %.2f\n", quote.InstrumentID, quote.LastPrice)
    }
}()
```

#### 回调接口
```go
quoteSub.OnQuote(func(quote *Quote) {
    if quote.InstrumentID == "SHFE.au2602" {
        fmt.Printf("黄金: %.2f\n", quote.LastPrice)
    }
})
```

### 2. 单合约 K线订阅（推荐延迟启动模式）

**延迟启动模式**确保所有回调注册完成后再开始接收数据，避免竞态条件。

```go
// 1. 创建订阅（不立即启动）
sub, _ := client.Series().Kline(ctx, "SHFE.au2602", time.Minute, 100)
defer sub.Close()

// 2. 注册回调函数
sub.OnUpdate(func(data *SeriesData, info *UpdateInfo) {
    if info.HasNewBar {
        fmt.Println("新 K线!")
    }
    if info.HasBarUpdate && !info.HasNewBar {
        fmt.Println("K线更新")
    }
})

sub.OnNewBar(func(data *SeriesData) {
    klines := data.GetSymbolKlines("SHFE.au2602")
    if len(klines.Data) > 0 {
        latest := klines.Data[len(klines.Data)-1]
        fmt.Printf("新 K线: C=%.2f\n", latest.Close)
    }
})

sub.OnBarUpdate(func(data *SeriesData) {
    klines := data.GetSymbolKlines("SHFE.au2602")
    if len(klines.Data) > 0 {
        latest := klines.Data[len(klines.Data)-1]
        fmt.Printf("K线更新: C=%.2f\n", latest.Close)
    }
})

// 3. 启动监听（确保不会错过数据）
sub.Start()
```

**回调类型说明**：
- `OnUpdate()` - 通用更新回调，包含详细的 UpdateInfo
- `OnNewBar()` - 新 K线回调，传递完整序列数据便于计算指标
- `OnBarUpdate()` - K线更新回调（盘中实时更新）

### 3. 多合约 K线订阅（自动对齐）

基于 TQSDK 协议的 binding 机制，自动对齐不同合约的 K线数据到相同的时间点。

```go
// 创建多合约订阅
sub, _ := client.Series().KlineMulti(ctx,
    []string{"SHFE.au2602", "SHFE.ag2512", "INE.sc2601"},
    time.Minute, 100)
defer sub.Close()

// 注册回调
sub.OnUpdate(func(data *SeriesData, info *UpdateInfo) {
    if info.HasNewBar {
        // 访问对齐的数据集
        for _, set := range data.Multi.Data {
            fmt.Printf("时间: %s\n", set.Timestamp.Format("15:04:05"))
            
            // 遍历所有合约的 K线
            for symbol, kline := range set.Klines {
                fmt.Printf("  %s: C=%.2f V=%d\n", symbol, kline.Close, kline.Volume)
            }
        }
    }
})

// 启动监听
sub.Start()
```

### 4. Tick 订阅

订阅单个合约的 Tick 数据流：

```go
// 创建订阅
sub, _ := client.Series().Tick(ctx, "SHFE.au2602", 100)
defer sub.Close()

// 注册回调
sub.OnNewBar(func(data *SeriesData) {
    if data.TickData != nil && len(data.TickData.Data) > 0 {
        tick := data.TickData.Data[len(data.TickData.Data)-1]
        fmt.Printf("新 Tick: 价格=%.2f 买一=%.2f(%d) 卖一=%.2f(%d)\n",
            tick.LastPrice,
            tick.BidPrice1, tick.BidVolume1,
            tick.AskPrice1, tick.AskVolume1)
    }
})

sub.OnBarUpdate(func(data *SeriesData) {
    if data.TickData != nil && len(data.TickData.Data) > 0 {
        tick := data.TickData.Data[len(data.TickData.Data)-1]
        fmt.Printf("Tick 更新: %.2f\n", tick.LastPrice)
    }
})

// 启动监听
sub.Start()
```

### 5. 自动启动模式（兼容模式）

如果需要向后兼容或快速原型开发，可以使用 `AndStart` 后缀的方法：

```go
// 创建并自动启动（注意：可能错过早期数据）
sub, _ := client.Series().TickAndStart(ctx, "SHFE.au2602", 100)
defer sub.Close()

// 注册回调（可能已经错过了一些数据）
sub.OnNewBar(func(data *SeriesData) {
    // ...
})
```

⚠️ **不推荐使用自动启动模式**，因为可能存在竞态条件导致错过早期数据。

## 运行示例

```bash
# 设置环境变量
export SHINNYTECH_ID="your_username"
export SHINNYTECH_PW="your_password"

# 运行示例
cd examples/quote
go run main.go
```

## 核心概念

### UpdateInfo

`UpdateInfo` 提供详细的数据更新信息：

```go
type UpdateInfo struct {
    HasNewBar         bool              // 是否有新 K线/Tick
    NewBarIDs         map[string]int64  // 新 K线的 ID 映射 (symbol -> id)
    HasBarUpdate      bool              // 是否有 K线更新
    ChartRangeChanged bool              // Chart 范围是否变化
    OldLeftID         int64             // 旧的左边界 ID
    OldRightID        int64             // 旧的右边界 ID
    NewLeftID         int64             // 新的左边界 ID
    NewRightID        int64             // 新的右边界 ID
    HasChartSync      bool              // Chart 是否初次同步完成
    ChartReady        bool              // Chart 数据是否传输完成
}
```

使用示例：

```go
sub.OnUpdate(func(data *SeriesData, info *UpdateInfo) {
    if info.HasNewBar {
        fmt.Println("新 K线!")
    }
    
    if info.HasBarUpdate && !info.HasNewBar {
        fmt.Println("K线更新（盘中实时）")
    }
    
    if info.ChartRangeChanged {
        fmt.Printf("Chart 范围变化: [%d,%d] -> [%d,%d]\n",
            info.OldLeftID, info.OldRightID,
            info.NewLeftID, info.NewRightID)
    }
    
    if info.HasChartSync {
        fmt.Println("Chart 初次同步完成")
    }
})
```

### ViewWidth

ViewWidth 控制返回的数据量，只保留最新的 N 条数据，优化内存使用。

```go
// 全局设置
client, _ := tqsdk.NewClient(ctx, username, password,
    tqsdk.WithViewWidth(1000))

// 订阅时指定
sub, _ := client.Series().Kline(ctx, "SHFE.au2602", time.Minute, 500)
```

### 多合约对齐（Binding）

基于 TQSDK 协议的 binding 机制，自动对齐不同合约的 K线数据到相同的时间点。

**数据结构**：

```go
type AlignedKlineSet struct {
    ID        int64              // 对齐的 K线 ID
    Timestamp time.Time          // K线时间
    Klines    map[string]*Kline  // symbol -> Kline
}
```

**使用场景**：
- 套利策略（需要同一时间点的多个合约数据）
- 相关性分析
- 跨品种对比

### SeriesData

SeriesData 是数据回调函数的参数，包含完整的序列数据：

```go
type SeriesData struct {
    IsMulti  bool              // 是否多合约订阅
    IsTick   bool              // 是否 Tick 数据
    Symbols  []string          // 订阅的合约列表
    
    // 单合约数据
    Single   *KlinesData       // 单合约 K线数据
    
    // 多合约数据
    Multi    *MultiKlinesData  // 多合约 K线数据（对齐）
    
    // Tick 数据
    TickData *TicksData        // Tick 数据
}

// 辅助方法
func (sd *SeriesData) GetSymbolKlines(symbol string) *KlinesData
```

## API 方法对照表

| 功能 | 延迟启动（推荐） | 自动启动（兼容） |
|------|-----------------|-----------------|
| K线订阅 | `Kline()` | `KlineAndStart()` |
| 多K线订阅 | `KlineMulti()` | `KlineMultiAndStart()` |
| Tick订阅 | `Tick()` | `TickAndStart()` |

**推荐使用延迟启动方法**，避免竞态条件。

## 最佳实践

### 1. 使用延迟启动模式

```go
// 推荐
sub, _ := client.Series().Kline(ctx, "SHFE.au2602", time.Minute, 100)
sub.OnNewBar(func(data *SeriesData) { /* ... */ })
sub.Start()

// ⚠️ 不推荐（可能错过数据）
sub, _ := client.Series().KlineAndStart(ctx, "SHFE.au2602", time.Minute, 100)
sub.OnNewBar(func(data *SeriesData) { /* ... */ })
```

### 2. 利用完整序列数据计算指标

`OnNewBar` 和 `OnBarUpdate` 回调传递完整的序列数据，便于计算技术指标：

```go
sub.OnNewBar(func(data *SeriesData) {
    klines := data.GetSymbolKlines("SHFE.au2602")
    
    // 计算最近 5 根 K线的平均价格（MA5）
    if len(klines.Data) >= 5 {
        sum := 0.0
        for i := len(klines.Data) - 5; i < len(klines.Data); i++ {
            sum += klines.Data[i].Close
        }
        ma5 := sum / 5
        fmt.Printf("MA5=%.2f\n", ma5)
    }
})
```

### 3. 正确处理多合约对齐数据

```go
sub.OnNewBar(func(data *SeriesData) {
    // 获取最新的对齐数据集
    if len(data.Multi.Data) > 0 {
        latest := data.Multi.Data[len(data.Multi.Data)-1]
        
        // 检查所有合约是否都有数据
        if len(latest.Klines) == len(data.Symbols) {
            // 所有合约都有数据，可以进行套利计算
            gold := latest.Klines["SHFE.au2602"]
            silver := latest.Klines["SHFE.ag2512"]
            ratio := gold.Close / silver.Close
            fmt.Printf("金银比: %.2f\n", ratio)
        }
    }
})
```

### 4. 合理设置 ViewWidth

```go
// 短期策略：小 ViewWidth
sub, _ := client.Series().Kline(ctx, "SHFE.au2602", time.Minute, 100)

// 长期策略：大 ViewWidth
sub, _ := client.Series().Kline(ctx, "SHFE.au2602", time.Hour, 1000)

// 历史回测：视情况设置
sub, _ := client.Series().KlineHistory(ctx, "SHFE.au2602", time.Minute, 8000, leftID)
```

## 注意事项

1. **延迟启动**：推荐使用延迟启动模式，避免竞态条件
2. **合约代码**：使用完整的合约代码格式，如 `SHFE.au2602`
3. **资源管理**：使用 `defer sub.Close()` 确保资源释放
4. **错误处理**：检查所有返回的 error
5. **Context 管理**：使用 Context 控制订阅的生命周期

## 相关文档

- [主文档](../../README.md) - 完整的 API 文档
- [History 示例](../history/README.md) - 历史数据订阅
- [Trade 示例](../trade/README.md) - 交易操作

## 许可证

Apache License 2.0
