# TQSDK-GO V2

天勤 [DIFF 协议](https://www.shinnytech.com/diff/) 的 Go 语言封装 - 全新重构版本

TQSDK-GO V2 提供了类型安全、并发安全、易用的 Go 语言 API，支持期货行情订阅、K线数据获取、交易操作等功能。

## 特性

- **类型安全**：强类型 API 设计，消除 `map[string]interface{}` 的使用
- **并发安全**：完善的并发控制和数据竞争保护
- **易用性**：符合 Go 语言习惯的 API 设计，支持 Context、Channel、Callback 等多种模式
- **高性能**：优化的数据管理和内存使用，支持 ViewWidth 控制
- **多合约对齐**：基于 TQSDK 协议的 binding 机制，自动对齐不同合约的 K线数据
- **零竞态条件**：延迟启动设计，确保回调注册后再接收数据

## 安装

```bash
go get github.com/pseudocodes/tqsdk-go@latest
```

## 快速开始

### 基础示例

```go
package main

import (
    "context"
    "fmt"
    "time"

    tqsdk "github.com/pseudocodes/tqsdk-go/shinny"
)

func main() {
    ctx := context.Background()
    
    // 1. 创建客户端
    client, err := tqsdk.NewClient(ctx, "username", "password",
        tqsdk.WithLogLevel("info"),
        tqsdk.WithViewWidth(500),
    )
    if err != nil {
        panic(err)
    }
    defer client.Close()

    // 2. 初始化行情功能 (必须调用)
    if err := client.InitMarket(); err != nil {
        panic(err)
    }
    
    // 3. 订阅 Quote
    quoteSub, _ := client.SubscribeQuote(ctx, "SHFE.au2602")
    defer quoteSub.Close()
    
    quoteSub.OnQuote(func(quote *tqsdk.Quote) {
        fmt.Printf("黄金: %.2f\n", quote.LastPrice)
    })
    
    // 4. 订阅 K线（延迟启动，避免竞态条件）
    klineSub, _ := client.Series().Kline(ctx, "SHFE.au2602", time.Minute, 100)
    defer klineSub.Close()
    
    // 先注册回调
    klineSub.OnNewBar(func(data *tqsdk.SeriesData) {
        klines := data.GetSymbolKlines("SHFE.au2602")
        if len(klines.Data) > 0 {
            latest := klines.Data[len(klines.Data)-1]
            fmt.Printf("新 K线: %.2f\n", latest.Close)
        }
    })
    
    // 再启动监听（确保不会错过数据）
    klineSub.Start()
    
    // 保持运行
    time.Sleep(30 * time.Second)
}
```

## 核心概念

### Client - 客户端

`Client` 是 TQSDK-GO 的核心入口，负责管理连接、认证、数据订阅等。

#### 创建客户端

```go
client, err := tqsdk.NewClient(ctx, username, password, options...)
```

#### 初始化行情

在使用行情相关功能（Quote, Series）之前，**必须**先调用 `InitMarket()`：

```go
if err := client.InitMarket(); err != nil {
    // 处理错误
}
```

#### 配置选项

```go
// 设置日志级别
tqsdk.WithLogLevel("debug")  // debug, info, warn, error

// 设置默认视图宽度
tqsdk.WithViewWidth(1000)

// 设置客户端信息
tqsdk.WithClientInfo("MyApp", "v1.0.0")

// 启用开发模式（使用测试服务器）
tqsdk.WithDevelopment(true)
```

### Quote 订阅

Quote 订阅用于获取实时行情数据，支持多合约订阅。

#### 基础用法

```go
// 订阅单个或多个合约
quoteSub, err := client.SubscribeQuote(ctx, "SHFE.au2602", "SHFE.ag2512")
defer quoteSub.Close()

// 方式 1: 使用 Channel（流式处理）
go func() {
    for quote := range quoteSub.QuoteChannel() {
        fmt.Printf("%s: %.2f\n", quote.InstrumentID, quote.LastPrice)
    }
}()

// 方式 2: 使用回调（事件驱动）
quoteSub.OnQuote(func(quote *tqsdk.Quote) {
    fmt.Printf("%s: %.2f\n", quote.InstrumentID, quote.LastPrice)
})
```

#### 动态添加/移除合约

```go
// 添加合约
quoteSub.AddSymbols("DCE.m2512", "CZCE.CF512")

// 移除合约
quoteSub.RemoveSymbols("SHFE.ag2512")
```

### Series API - 序列数据

Series API 提供 K线、Tick 等序列数据的订阅功能。

#### 延迟启动模式（推荐，零竞态条件）

```go
// 1. 创建订阅（不立即启动）
sub, err := client.Series().Kline(ctx, "SHFE.au2602", time.Minute, 100)
defer sub.Close()

// 2. 注册所有回调函数
sub.OnNewBar(func(data *tqsdk.SeriesData) {
    klines := data.GetSymbolKlines("SHFE.au2602")
    if len(klines.Data) > 0 {
        latest := klines.Data[len(klines.Data)-1]
        fmt.Printf("新 K线: O=%.2f H=%.2f L=%.2f C=%.2f\n",
            latest.Open, latest.High, latest.Low, latest.Close)
    }
})

sub.OnBarUpdate(func(data *tqsdk.SeriesData) {
    klines := data.GetSymbolKlines("SHFE.au2602")
    if len(klines.Data) > 0 {
        latest := klines.Data[len(klines.Data)-1]
        fmt.Printf("K线更新: C=%.2f\n", latest.Close)
    }
})

// 3. 启动监听（不会错过任何数据）
if err := sub.Start(); err != nil {
    panic(err)
}
```

#### 自动启动模式（兼容模式）

如果需要向后兼容或快速原型开发，可以使用 `AndStart` 后缀的方法：

```go
// 创建并立即启动（注意：可能错过早期数据）
sub, err := client.Series().KlineAndStart(ctx, "SHFE.au2602", time.Minute, 100)
defer sub.Close()

// 注册回调（可能已经错过了一些数据）
sub.OnNewBar(func(data *tqsdk.SeriesData) {
    // ...
})
```

#### 详细更新信息

使用 `OnUpdate` 可以获取详细的更新信息：

```go
sub.OnUpdate(func(data *tqsdk.SeriesData, info *tqsdk.UpdateInfo) {
    if info.HasNewBar {
        // 新增了一根 K线
        fmt.Printf("新 K线! ID=%d\n", info.NewBarIDs["SHFE.au2602"])
    }
    
    if info.HasBarUpdate && !info.HasNewBar {
        // 更新了最后一根 K线（盘中实时更新）
        fmt.Printf("K线更新\n")
    }
    
    if info.ChartRangeChanged {
        // Chart 范围发生变化
        fmt.Printf("Chart 范围: [%d,%d] -> [%d,%d]\n",
            info.OldLeftID, info.OldRightID,
            info.NewLeftID, info.NewRightID)
    }
    
    if info.HasChartSync {
        // Chart 初次同步完成
        fmt.Printf("Chart 同步完成!\n")
    }
})
```

#### 多合约 K线订阅（自动对齐）

```go
// 订阅多个合约的 K线，自动对齐到相同时间点
sub, err := client.Series().KlineMulti(ctx,
    []string{"SHFE.au2602", "SHFE.ag2512", "INE.sc2601"},
    time.Minute, 100)
defer sub.Close()

// 注册回调
sub.OnUpdate(func(data *tqsdk.SeriesData, info *tqsdk.UpdateInfo) {
    if info.HasNewBar {
        // 获取最新的对齐数据集
        if len(data.Multi.Data) > 0 {
            latest := data.Multi.Data[len(data.Multi.Data)-1]
            fmt.Printf("时间: %s\n", latest.Timestamp.Format("15:04:05"))
            
            // 遍历所有合约的 K线
            for symbol, kline := range latest.Klines {
                fmt.Printf("  %s: C=%.2f V=%d\n", symbol, kline.Close, kline.Volume)
            }
        }
    }
})

// 启动监听
sub.Start()
```

#### Tick 订阅

```go
// 创建 Tick 订阅
sub, err := client.Series().Tick(ctx, "SHFE.au2602", 100)
defer sub.Close()

// 注册回调
sub.OnNewBar(func(data *tqsdk.SeriesData) {
    if data.TickData != nil && len(data.TickData.Data) > 0 {
        tick := data.TickData.Data[len(data.TickData.Data)-1]
        fmt.Printf("新 Tick: 价格=%.2f 买一=%.2f 卖一=%.2f\n",
            tick.LastPrice, tick.BidPrice1, tick.AskPrice1)
    }
})

// 启动监听
sub.Start()
```

### 历史数据订阅

TQSDK 支持订阅历史数据，适用于回测、数据分析等场景。

#### 使用 left_kline_id 订阅

```go
// 从指定 K线 ID 开始订阅历史数据
leftKlineID := int64(105761)
sub, err := client.Series().KlineHistory(ctx, "SHFE.au2602", time.Minute, 8000, leftKlineID)
defer sub.Close()

sub.OnUpdate(func(data *tqsdk.SeriesData, info *tqsdk.UpdateInfo) {
    if info.ChartReady {
        // 历史数据传输完成
        fmt.Printf("历史数据传输完成！数据量: %d\n", len(data.Single.Data))
    }
})

sub.Start()
```

#### 使用 focus_datetime 订阅

```go
// 从指定时间点开始订阅
focusTime := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)
// focusPosition > 0: 起始时间从焦点时间向左扩展
sub, err := client.Series().KlineHistoryWithFocus(ctx, 
    "SHFE.au2602", time.Minute, 8000, focusTime, 100)
defer sub.Close()

sub.OnUpdate(func(data *tqsdk.SeriesData, info *tqsdk.UpdateInfo) {
    if info.ChartReady {
        fmt.Printf("历史数据加载完成\n")
    }
})

sub.Start()
```

### TradeSession - 交易会话

TradeSession 提供交易相关功能，支持多账户管理。

#### 登录交易账户

```go
// 登录交易账户（快期模拟、SimNow 等）
session, err := client.LoginTrade(ctx, "快期模拟", "userID", "password")
if err != nil {
    panic(err)
}
defer session.Close()
```

#### 下单

```go
// 构建下单请求
req := &tqsdk.InsertOrderRequest{
    Symbol:     "SHFE.au2602",
    Direction:  tqsdk.DirectionBuy,
    Offset:     tqsdk.OffsetOpen,
    PriceType:  tqsdk.PriceTypeLimit,
    LimitPrice: 500.00,
    Volume:     1,
}

// 下单
order, err := session.InsertOrder(ctx, req)
if err != nil {
    fmt.Printf("下单失败: %v\n", err)
} else {
    fmt.Printf("下单成功，订单ID: %s\n", order.OrderID)
}
```

#### 撤单

```go
err := session.CancelOrder(ctx, orderID)
```

#### 查询账户信息

```go
// 同步查询
account, err := session.GetAccount(ctx)
if err == nil {
    fmt.Printf("账户权益: %.2f\n", account.Balance)
    fmt.Printf("可用资金: %.2f\n", account.Available)
    fmt.Printf("风险度: %.2f%%\n", account.RiskRatio*100)
}
```

#### 查询持仓

```go
// 查询所有持仓
positions, err := session.GetPositions(ctx)
for symbol, pos := range positions {
    fmt.Printf("%s: 多头=%d 空头=%d 浮盈=%.2f\n",
        symbol, pos.VolumeLongToday, pos.VolumeShortToday, pos.FloatProfit)
}

// 查询指定合约持仓
pos, err := session.GetPosition(ctx, "SHFE.au2602")
```

#### 实时监听（Callback 模式）

```go
// 注册账户更新回调
session.OnAccount(func(account *tqsdk.Account) {
    fmt.Printf("账户权益: %.2f\n", account.Balance)
})

// 注册持仓更新回调
session.OnPosition(func(symbol string, pos *tqsdk.Position) {
    fmt.Printf("持仓更新: %s 浮盈=%.2f\n", symbol, pos.FloatProfit)
})

// 注册订单更新回调
session.OnOrder(func(order *tqsdk.Order) {
    fmt.Printf("订单 %s: %s\n", order.OrderID, order.Status)
})

// 注册成交回调
session.OnTrade(func(trade *tqsdk.Trade) {
    fmt.Printf("成交: %s %.2f x %d\n", trade.InstrumentID, trade.Price, trade.Volume)
})

// 注册通知回调
session.OnNotification(func(notify *tqsdk.Notification) {
    fmt.Printf("[%s] %s\n", notify.Level, notify.Content)
})
```

#### 实时监听（Channel 模式）

```go
// 监听账户更新
go func() {
    for account := range session.AccountChannel() {
        fmt.Printf("账户更新: %.2f\n", account.Balance)
    }
}()

// 监听持仓更新
go func() {
    for update := range session.PositionChannel() {
        fmt.Printf("持仓更新: %s\n", update.Symbol)
    }
}()

// 监听订单更新
go func() {
    for order := range session.OrderChannel() {
        fmt.Printf("订单更新: %s %s\n", order.OrderID, order.Status)
    }
}()

// 监听成交更新
go func() {
    for trade := range session.TradeChannel() {
        fmt.Printf("成交: %s\n", trade.TradeID)
    }
}()
```

## API 参考

### 订阅方法对照表

| 功能 | 延迟启动（推荐） | 自动启动（兼容） |
|------|-----------------|-----------------|
| 通用订阅 | `Subscribe()` | `SubscribeAndStart()` |
| K线订阅 | `Kline()` | `KlineAndStart()` |
| 多K线订阅 | `KlineMulti()` | `KlineMultiAndStart()` |
| Tick订阅 | `Tick()` | `TickAndStart()` |
| 历史K线 | `KlineHistory()` | `KlineHistoryAndStart()` |
| 焦点历史K线 | `KlineHistoryWithFocus()` | `KlineHistoryWithFocusAndStart()` |
| 历史Tick | `TickHistory()` | `TickHistoryAndStart()` |

**推荐使用延迟启动方法**，确保所有回调注册完成后再启动监听，避免竞态条件。

### 核心数据结构

#### Quote - 行情报价

```go
type Quote struct {
    InstrumentID     string  // 合约代码
    Datetime         string  // 行情时间
    LastPrice        float64 // 最新价
    AskPrice1        float64 // 卖一价
    AskVolume1       int64   // 卖一量
    BidPrice1        float64 // 买一价
    BidVolume1       int64   // 买一量
    Highest          float64 // 最高价
    Lowest           float64 // 最低价
    Open             float64 // 开盘价
    Close            float64 // 收盘价
    Volume           int64   // 成交量
    OpenInterest     int64   // 持仓量
    Change           float64 // 涨跌
    ChangePercent    float64 // 涨跌幅
    // ... 更多字段
}
```

#### Kline - K线数据

```go
type Kline struct {
    ID       int64   // K线ID
    Datetime int64   // K线起点时间(纳秒)
    Open     float64 // 开盘价
    Close    float64 // 收盘价
    High     float64 // 最高价
    Low      float64 // 最低价
    OpenOI   int64   // 起始持仓量
    CloseOI  int64   // 结束持仓量
    Volume   int64   // 成交量
}
```

#### Tick - Tick数据

```go
type Tick struct {
    ID           int64   // Tick ID
    Datetime     int64   // tick时间(纳秒)
    LastPrice    float64 // 最新价
    Average      float64 // 均价
    Volume       int64   // 成交量
    OpenInterest int64   // 持仓量
    AskPrice1    float64 // 卖一价
    AskVolume1   int64   // 卖一量
    BidPrice1    float64 // 买一价
    BidVolume1   int64   // 买一量
    // ... 更多盘口数据
}
```

## 高级功能

### ViewWidth 控制

ViewWidth 用于控制返回的数据量，只保留最新的 N 条数据，优化内存使用。

```go
// 全局设置
client, _ := tqsdk.NewClient(ctx, username, password,
    tqsdk.WithViewWidth(1000))

// 订阅时指定
sub, _ := client.Series().Kline(ctx, "SHFE.au2602", time.Minute, 500)
```

### 多合约对齐

基于 TQSDK 协议的 binding 机制，自动对齐不同合约的 K线数据到相同的时间点。

```go
sub, _ := client.Series().KlineMulti(ctx,
    []string{"SHFE.au2602", "SHFE.ag2512"},
    time.Minute, 100)

sub.OnNewBar(func(data *tqsdk.SeriesData) {
    // data.Multi.Data 中的每个 AlignedKlineSet 包含同一时间点的多个合约 K线
    for _, set := range data.Multi.Data {
        fmt.Printf("时间: %s\n", set.Timestamp.Format("15:04:05"))
        for symbol, kline := range set.Klines {
            fmt.Printf("  %s: %.2f\n", symbol, kline.Close)
        }
    }
})

sub.Start()
```

## 完整示例

完整示例代码请参考 `examples/` 目录：

- **[examples/quote/](examples/quote/)** - 行情订阅示例
  - Quote 订阅（Channel 和 Callback 模式）
  - 单合约 K线订阅
  - 多合约 K线订阅（自动对齐）
  - Tick 订阅
  
- **[examples/history/](examples/history/)** - 历史数据订阅示例
  - 使用 `left_kline_id` 订阅
  - 使用 `focus_datetime` 订阅
  - 大数据量分片传输
  
- **[examples/trade/](examples/trade/)** - 交易操作示例
  - 账户登录和会话管理
  - 下单、撤单操作
  - 账户、持仓、订单查询
  - 实时监听（Callback 和 Channel 模式）
  
- **[examples/datamanager/](examples/datamanager/)** - DataManager 高级功能
  - 路径监听（Watch/UnWatch）
  - 动态配置管理
  - 多路径同时监听

## 常量定义

### 方向

```go
const (
    DirectionBuy  = "BUY"   // 买入
    DirectionSell = "SELL"  // 卖出
)
```

### 开平

```go
const (
    OffsetOpen       = "OPEN"       // 开仓
    OffsetClose      = "CLOSE"      // 平仓
    OffsetCloseToday = "CLOSETODAY" // 平今
)
```

### 价格类型

```go
const (
    PriceTypeLimit = "LIMIT"  // 限价单
    PriceTypeAny   = "ANY"    // 市价单
)
```

### 订单状态

```go
const (
    OrderStatusAlive    = "ALIVE"    // 活动（未成交或部分成交）
    OrderStatusFinished = "FINISHED" // 已完成（全部成交或已撤销）
)
```

## 环境变量

运行示例程序需要设置以下环境变量：

```bash
# 天勤账号（必需）
export SHINNYTECH_ID="your_username"
export SHINNYTECH_PW="your_password"

# SimNow 账号（仅交易示例需要）
export SIMNOW_USER_0="your_simnow_user"
export SIMNOW_PASS_0="your_simnow_pass"
```

## 许可证

Apache License 2.0

## 更新日志

查看 [CHANGELOG.md](CHANGELOG.md) 了解版本更新历史和最新变更。

## 相关链接

- [天勤量化官网](https://www.shinnytech.com/)
- [DIFF 协议文档](https://www.shinnytech.com/diff/)
- [GitHub 仓库](https://github.com/pseudocodes/tqsdk-go)
- [更新日志](CHANGELOG.md) / [中文版](CHANGELOG_CN.md)

## 免责声明

本项目明确拒绝对产品做任何明示或暗示的担保。由于软件系统开发本身的复杂性，无法保证本项目完全没有错误。如选择使用本项目表示您同意错误和/或遗漏的存在，在任何情况下本项目对于直接、间接、特殊的、偶然的、或间接产生的、使用或无法使用本项目进行交易和投资造成的盈亏、直接或间接引起的赔偿、损失、债务或是任何交易中止均不承担责任和义务。

## 致谢

感谢 [天勤量化](https://www.shinnytech.com/) 提供优秀的 DIFF 协议和服务支持。
