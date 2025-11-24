# TQSDK Go V2 Examples

本目录包含 TQSDK Go V2 的完整示例程序，演示各种使用场景和最佳实践。

## 目录结构

```
examples/
├── quote/          # 行情订阅示例
│   ├── main.go
│   ├── go.mod
│   └── README.md
├── trade/          # 交易操作示例
│   ├── main.go
│   ├── go.mod
│   └── README.md
├── history/        # 历史数据订阅示例
│   ├── main.go
│   ├── go.mod
│   └── README.md
├── datamanager/    # DataManager 高级功能示例
│   ├── main.go
│   ├── go.mod
│   └── README.md
└── README.md       # 本文件
```

## 快速开始

### 1. 设置环境变量

```bash
# 天勤账号（必需）
export SHINNYTECH_ID="your_username"
export SHINNYTECH_PW="your_password"

# SimNow 账号（仅交易示例需要）
export SIMNOW_USER_0="your_simnow_user"
export SIMNOW_PASS_0="your_simnow_pass"
```

### 2. 运行示例

```bash
# 行情订阅示例
cd quote && go run main.go

# 历史数据示例
cd history && go run main.go

# 交易操作示例
cd trade && go run main.go

# DataManager 高级功能示例
cd datamanager && go run main.go
```

## 示例说明

### 1. Quote 示例 (`quote/`)

演示行情订阅的各种用法，包括：

#### Quote 订阅
```go
// 订阅实时行情
quoteSub, _ := client.SubscribeQuote(ctx, "SHFE.au2602", "SHFE.ag2512")

// 方式 1: 使用 Channel
go func() {
    for quote := range quoteSub.QuoteChannel() {
        fmt.Printf("%s: %.2f\n", quote.InstrumentID, quote.LastPrice)
    }
}()

// 方式 2: 使用回调
quoteSub.OnQuote(func(quote *Quote) {
    if quote.InstrumentID == "SHFE.au2602" {
        fmt.Printf("黄金: %.2f\n", quote.LastPrice)
    }
})
```

#### 单合约 K线订阅（推荐延迟启动模式）
```go
// 1. 创建订阅（不立即启动）
sub, _ := client.Series().Kline(ctx, "SHFE.au2602", time.Minute, 100)

// 2. 注册所有回调
sub.OnNewBar(func(data *SeriesData) {
    klines := data.GetSymbolKlines("SHFE.au2602")
    if len(klines.Data) > 0 {
        latest := klines.Data[len(klines.Data)-1]
        fmt.Printf("新 K线: %.2f\n", latest.Close)
    }
})

sub.OnBarUpdate(func(data *SeriesData) {
    klines := data.GetSymbolKlines("SHFE.au2602")
    if len(klines.Data) > 0 {
        latest := klines.Data[len(klines.Data)-1]
        fmt.Printf("K线更新: %.2f\n", latest.Close)
    }
})

// 3. 启动监听（确保不会错过数据）
sub.Start()
```

#### 多合约 K线订阅（自动对齐）
```go
sub, _ := client.Series().KlineMulti(ctx,
    []string{"SHFE.au2602", "SHFE.ag2512", "INE.sc2601"},
    time.Minute, 100)

sub.OnUpdate(func(data *SeriesData, info *UpdateInfo) {
    if info.HasNewBar {
        // 访问对齐的数据
        for _, set := range data.Multi.Data {
            fmt.Printf("时间: %s\n", set.Timestamp.Format("15:04:05"))
            for symbol, kline := range set.Klines {
                fmt.Printf("  %s: %.2f\n", symbol, kline.Close)
            }
        }
    }
})

sub.Start()
```

#### Tick 订阅
```go
sub, _ := client.Series().Tick(ctx, "SHFE.au2602", 100)

sub.OnNewBar(func(data *SeriesData) {
    if data.TickData != nil && len(data.TickData.Data) > 0 {
        tick := data.TickData.Data[len(data.TickData.Data)-1]
        fmt.Printf("Tick: %.2f\n", tick.LastPrice)
    }
})

sub.Start()
```

### 2. History 示例 (`history/`)

演示历史数据订阅，支持回测和数据分析：

#### 使用 left_kline_id 订阅
```go
leftKlineID := int64(105761)
sub, _ := client.Series().KlineHistory(ctx, "SHFE.au2602", time.Minute, 8000, leftKlineID)

sub.OnUpdate(func(data *SeriesData, info *UpdateInfo) {
    if info.ChartReady {
        // 数据传输完成
        fmt.Printf("历史数据加载完成，共 %d 根 K线\n", len(data.Single.Data))
    }
})

sub.Start()
```

#### 使用 focus_datetime 订阅
```go
focusTime := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)
// focusPosition > 0: 起始时间从焦点时间向左扩展
sub, _ := client.Series().KlineHistoryWithFocus(ctx,
    "SHFE.au2602", time.Minute, 8000, focusTime, 100)

sub.OnUpdate(func(data *SeriesData, info *UpdateInfo) {
    if info.ChartReady {
        fmt.Println("历史数据加载完成")
    }
})

sub.Start()
```

#### 大数据量分片传输
- 支持最多 10000 根 K线的自动分片传输
- 通过 `Chart.MoreData` 和 `ChartReady` 监控传输状态

### 3. Trade 示例 (`trade/`)

演示交易操作和账户管理：

#### 登录和会话管理
```go
session, _ := client.LoginTrade(ctx, "快期模拟", userID, password)
defer session.Close()
```

#### 下单操作
```go
order, _ := session.InsertOrder(ctx, &InsertOrderRequest{
    Symbol:     "SHFE.au2602",
    Direction:  DirectionBuy,
    Offset:     OffsetOpen,
    PriceType:  PriceTypeLimit,
    LimitPrice: 500.0,
    Volume:     1,
})
fmt.Printf("订单ID: %s\n", order.OrderID)
```

#### 撤单操作
```go
err := session.CancelOrder(ctx, orderID)
```

#### 查询操作
```go
// 查询账户
account, _ := session.GetAccount(ctx)
fmt.Printf("权益: %.2f\n", account.Balance)

// 查询持仓
positions, _ := session.GetPositions(ctx)
for symbol, pos := range positions {
    fmt.Printf("%s: 多=%d 空=%d\n", symbol, pos.VolumeLongToday, pos.VolumeShortToday)
}

// 查询委托
orders, _ := session.GetOrders(ctx)
```

#### 实时监听（Callback 模式）
```go
session.OnAccount(func(account *Account) {
    fmt.Printf("账户权益: %.2f\n", account.Balance)
})

session.OnPosition(func(symbol string, pos *Position) {
    fmt.Printf("持仓更新: %s\n", symbol)
})

session.OnOrder(func(order *Order) {
    fmt.Printf("订单: %s %s\n", order.OrderID, order.Status)
})

session.OnTrade(func(trade *Trade) {
    fmt.Printf("成交: %s\n", trade.TradeID)
})
```

#### 实时监听（Channel 模式）
```go
go func() {
    for account := range session.AccountChannel() {
        fmt.Printf("账户更新: %.2f\n", account.Balance)
    }
}()

go func() {
    for order := range session.OrderChannel() {
        fmt.Printf("订单更新: %s\n", order.OrderID)
    }
}()
```

### 4. DataManager 示例 (`datamanager/`)

演示 DataManager 的高级功能：

#### 路径监听
```go
// 监听特定路径的数据变化
ch, _ := client.dm.Watch(ctx, []string{"quotes", "SHFE.au2602"})
go func() {
    for data := range ch {
        fmt.Printf("数据更新: %v\n", data)
    }
}()

// 取消监听
client.dm.UnWatch([]string{"quotes", "SHFE.au2602"})
```

#### 动态配置
```go
// 设置视图宽度
client.dm.SetViewWidth(2000)

// 设置数据保留时间
client.dm.SetDataRetention(24 * time.Hour)

// 手动清理过期数据
client.dm.Cleanup()
```

#### 多路径同时监听
```go
// 监听多个路径
ch1, _ := client.dm.Watch(ctx, []string{"quotes", "SHFE.au2602"})
ch2, _ := client.dm.Watch(ctx, []string{"klines", "SHFE.au2602", "60000000000"})
```

## 核心特性演示

### 1. 延迟启动模式（Zero Race Condition）

**推荐使用延迟启动模式**，确保所有回调注册完成后再启动监听：

```go
// 推荐：延迟启动
sub, _ := client.Series().Kline(ctx, "SHFE.au2602", time.Minute, 100)

// 注册所有回调
sub.OnNewBar(func(data *SeriesData) { /* ... */ })
sub.OnBarUpdate(func(data *SeriesData) { /* ... */ })

// 启动监听（不会错过任何数据）
sub.Start()
```

```go
// ⚠️ 兼容模式：自动启动（可能错过早期数据）
sub, _ := client.Series().KlineAndStart(ctx, "SHFE.au2602", time.Minute, 100)

// 注册回调（可能已经错过了一些数据）
sub.OnNewBar(func(data *SeriesData) { /* ... */ })
```

### 2. 多合约对齐（Binding）

基于 TQSDK 协议的 binding 机制，自动对齐不同合约的 K线数据：

```go
sub, _ := client.Series().KlineMulti(ctx,
    []string{"SHFE.au2602", "SHFE.ag2512"},
    time.Minute, 100)

sub.OnNewBar(func(data *SeriesData) {
    // data.Multi.Data 中的每个 AlignedKlineSet 
    // 包含同一时间点的多个合约 K线
    for _, set := range data.Multi.Data {
        fmt.Printf("时间: %s\n", set.Timestamp)
        for symbol, kline := range set.Klines {
            fmt.Printf("  %s: %.2f\n", symbol, kline.Close)
        }
    }
})

sub.Start()
```

### 3. ViewWidth 控制

优化内存使用，只保留最新的 N 条数据：

```go
// 全局设置
client, _ := tqsdk.NewClient(ctx, username, password,
    tqsdk.WithViewWidth(1000))

// 订阅时指定
sub, _ := client.Series().Kline(ctx, "SHFE.au2602", time.Minute, 500)
```

### 4. 详细更新信息

使用 `OnUpdate` 获取详细的更新信息：

```go
sub.OnUpdate(func(data *SeriesData, info *UpdateInfo) {
    if info.HasNewBar {
        fmt.Println("新 K线")
    }
    
    if info.HasBarUpdate && !info.HasNewBar {
        fmt.Println("K线更新")
    }
    
    if info.ChartRangeChanged {
        fmt.Printf("Chart 范围变化: [%d,%d] -> [%d,%d]\n",
            info.OldLeftID, info.OldRightID,
            info.NewLeftID, info.NewRightID)
    }
    
    if info.HasChartSync {
        fmt.Println("Chart 同步完成")
    }
})
```

## API 方法对照表

### Series API

| 功能 | 延迟启动（推荐） | 自动启动（兼容） |
|------|-----------------|-----------------|
| 通用订阅 | `Subscribe()` | `SubscribeAndStart()` |
| K线订阅 | `Kline()` | `KlineAndStart()` |
| 多K线订阅 | `KlineMulti()` | `KlineMultiAndStart()` |
| Tick订阅 | `Tick()` | `TickAndStart()` |
| 历史K线 | `KlineHistory()` | `KlineHistoryAndStart()` |
| 焦点历史K线 | `KlineHistoryWithFocus()` | `KlineHistoryWithFocusAndStart()` |
| 历史Tick | `TickHistory()` | `TickHistoryAndStart()` |

## 注意事项

1. **环境变量**：请妥善保管您的账号密码，建议使用环境变量
2. **模拟账户**：建议先使用模拟账户测试
3. **风险提示**：交易有风险，请谨慎操作
4. **下单测试**：示例中的下单代码已注释，避免误操作
5. **延迟启动**：推荐使用延迟启动模式，避免竞态条件
6. **合约代码**：请使用完整的合约代码格式，如 `SHFE.au2602`

## 更多文档

- [主文档](../README.md) - 完整的 API 文档
- [Quote 示例](quote/README.md) - 行情订阅详细说明
- [Trade 示例](trade/README.md) - 交易操作详细说明
- [History 示例](history/README.md) - 历史数据详细说明
- [DataManager 示例](datamanager/README.md) - 高级功能详细说明

## 问题反馈

如有问题，请提交 [GitHub Issue](https://github.com/pseudocodes/tqsdk-go/issues)。

## 许可证

Apache License 2.0
