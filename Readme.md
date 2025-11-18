# TQSDK-GO V2

天勤 [DIFF 协议](https://www.shinnytech.com/diff/) 的 Go 语言封装 - 全新重构版本

TQSDK-GO V2 提供了类型安全、并发安全、易用的 Go 语言 API，支持期货行情订阅、K线数据获取、交易操作等功能。

## 特性

- **类型安全**：强类型 API 设计，消除 `map[string]interface{}` 的使用
- **并发安全**：完善的并发控制和数据竞争保护
- **易用性**：符合 Go 语言习惯的 API 设计，支持 Context、Channel、Callback 等多种模式
- **高性能**：优化的数据管理和内存使用，支持 ViewWidth 控制
- **多合约对齐**：基于 TQSDK 协议的 binding 机制，自动对齐不同合约的 K线数据

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

    tqsdk "github.com/pseudocodes/tqsdk-go"
)

func main() {
    ctx := context.Background()
    
    // 创建客户端
    client, err := tqsdk.NewClient(ctx, "username", "password",
        tqsdk.WithLogLevel("info"),
        tqsdk.WithViewWidth(500),
    )
    if err != nil {
        panic(err)
    }
    defer client.Close()
    
    // 订阅 Quote
    quoteSub, _ := client.SubscribeQuote(ctx, "SHFE.au2512")
    defer quoteSub.Close()
    
    quoteSub.OnQuote(func(quote *tqsdk.Quote) {
        fmt.Printf("黄金: %.2f\n", quote.LastPrice)
    })
    
    // 订阅 K线
    klineSub, _ := client.Series().Kline(ctx, "SHFE.au2512", time.Minute, 100)
    defer klineSub.Close()
    
    klineSub.OnNewBar(func(data *tqsdk.SeriesData) {
        klines := data.GetSymbolKlines("SHFE.au2512")
        if len(klines.Data) > 0 {
            latest := klines.Data[len(klines.Data)-1]
            fmt.Printf("新 K线: %.2f\n", latest.Close)
        }
    })
    
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

#### 配置选项

```go
// 设置日志级别
tqsdk.WithLogLevel("debug")

// 设置默认视图宽度
tqsdk.WithViewWidth(1000)

// 设置客户端信息
tqsdk.WithClientInfo("MyApp", "v1.0.0")

// 启用开发模式
tqsdk.WithDevelopment(true)
```

### Quote 订阅

Quote 订阅用于获取实时行情数据，支持多合约订阅。

#### 基础用法

```go
// 订阅单个或多个合约
quoteSub, err := client.SubscribeQuote(ctx, "SHFE.au2512", "SHFE.ag2512")
defer quoteSub.Close()

// 方式 1: 使用 Channel
go func() {
    for quote := range quoteSub.QuoteChannel() {
        fmt.Printf("%s: %.2f\n", quote.InstrumentID, quote.LastPrice)
    }
}()

// 方式 2: 使用回调
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

#### 单合约 K线订阅

```go
// 订阅 1分钟 K线，保留最新 100 根
sub, err := client.Series().Kline(ctx, "SHFE.au2512", time.Minute, 100)
defer sub.Close()

// 监听新 K线
sub.OnNewBar(func(data *tqsdk.SeriesData) {
    klines := data.GetSymbolKlines("SHFE.au2512")
    if len(klines.Data) > 0 {
        latest := klines.Data[len(klines.Data)-1]
        fmt.Printf("新 K线: O=%.2f H=%.2f L=%.2f C=%.2f V=%d\n",
            latest.Open, latest.High, latest.Low, latest.Close, latest.Volume)
    }
})

// 监听 K线更新（盘中实时）
sub.OnBarUpdate(func(data *tqsdk.SeriesData) {
    klines := data.GetSymbolKlines("SHFE.au2512")
    if len(klines.Data) > 0 {
        latest := klines.Data[len(klines.Data)-1]
        fmt.Printf("K线更新: C=%.2f\n", latest.Close)
    }
})
```

#### 多合约 K线订阅（自动对齐）

```go
// 订阅多个合约的 K线，自动对齐到相同时间点
sub, err := client.Series().KlineMulti(ctx,
    []string{"SHFE.au2512", "SHFE.ag2512", "INE.sc2601"},
    time.Minute, 100)
defer sub.Close()

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
```

#### Tick 订阅

```go
// 订阅 Tick 数据
sub, err := client.Series().Tick(ctx, "SHFE.au2512", 100)
defer sub.Close()

sub.OnNewBar(func(data *tqsdk.SeriesData) {
    if data.TickData != nil && len(data.TickData.Data) > 0 {
        tick := data.TickData.Data[len(data.TickData.Data)-1]
        fmt.Printf("新 Tick: 价格=%.2f 买一=%.2f 卖一=%.2f\n",
            tick.LastPrice, tick.BidPrice1, tick.AskPrice1)
    }
})
```

### 历史数据订阅

#### 使用 left_kline_id 订阅

```go
// 从指定 K线 ID 开始订阅历史数据
leftKlineID := int64(105761)
sub, err := client.Series().KlineHistory(ctx, "SHFE.au2512", time.Minute, 8000, leftKlineID)
defer sub.Close()

sub.OnUpdate(func(data *tqsdk.SeriesData, info *tqsdk.UpdateInfo) {
    if info.ChartReady {
        fmt.Printf("历史数据传输完成！数据量: %d\n", len(data.Single.Data))
    }
})
```

#### 使用 focus_datetime 订阅

```go
// 从指定时间点开始订阅
focusTime := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)
sub, err := client.Series().KlineHistoryWithFocus(ctx, 
    "SHFE.au2512", time.Minute, 8000, focusTime, 1)
defer sub.Close()
```

### 交易会话

TradeSession 提供交易相关功能，支持多账户管理。

#### 登录交易账户

```go
// 登录交易账户
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
    Symbol:     "SHFE.au2512",
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
pos, err := session.GetPosition(ctx, "SHFE.au2512")
```

#### 查询委托单

```go
// 查询所有委托单
orders, err := session.GetOrders(ctx)
for orderID, order := range orders {
    if order.Status == tqsdk.OrderStatusAlive {
        fmt.Printf("活动委托: %s\n", orderID)
    }
}
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
        fmt.Printf("成交: %s %.2f x %d\n", trade.InstrumentID, trade.Price, trade.Volume)
    }
}()

// 监听通知
go func() {
    for notify := range session.NotificationChannel() {
        fmt.Printf("[%s] %s\n", notify.Level, notify.Content)
    }
}()
```

#### 实时监听（Callback 模式）

```go
// 注册账户更新回调
session.OnAccount(func(account *tqsdk.Account) {
    fmt.Printf("账户更新: %.2f\n", account.Balance)
})

// 注册持仓更新回调
session.OnPosition(func(symbol string, pos *tqsdk.Position) {
    fmt.Printf("持仓更新: %s\n", symbol)
})

// 注册订单更新回调
session.OnOrder(func(order *tqsdk.Order) {
    fmt.Printf("订单更新: %s\n", order.OrderID)
})

// 注册成交回调
session.OnTrade(func(trade *tqsdk.Trade) {
    fmt.Printf("成交: %s\n", trade.TradeID)
})

// 注册通知回调
session.OnNotification(func(notify *tqsdk.Notification) {
    fmt.Printf("[%s] %s\n", notify.Level, notify.Content)
})
```

## 数据结构

### Quote - 行情报价

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
    LowerLimit       float64 // 跌停价
    UpperLimit       float64 // 涨停价
    Settlement       float64 // 结算价
    PreSettlement    float64 // 昨结算价
    VolumeMultiple   int     // 合约乘数
    PriceTick        float64 // 最小变动价位
    // ... 更多字段
}
```

### Kline - K线数据

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

### Tick - Tick数据

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

### Account - 账户资金

```go
type Account struct {
    Balance          float64 // 账户权益
    Available        float64 // 可用资金
    CurrMargin       float64 // 当前保证金
    FrozenMargin     float64 // 冻结保证金
    CloseProfit      float64 // 平仓盈亏
    PositionProfit   float64 // 持仓盈亏
    RiskRatio        float64 // 风险度
    // ... 更多字段
}
```

### Position - 持仓信息

```go
type Position struct {
    ExchangeID          string  // 交易所代码
    InstrumentID        string  // 合约代码
    VolumeLongToday     int64   // 今多头持仓量
    VolumeLongHis       int64   // 昨多头持仓量
    VolumeShortToday    int64   // 今空头持仓量
    VolumeShortHis      int64   // 昨空头持仓量
    OpenPriceLong       float64 // 多头开仓均价
    OpenPriceShort      float64 // 空头开仓均价
    FloatProfitLong     float64 // 多头浮动盈亏
    FloatProfitShort    float64 // 空头浮动盈亏
    FloatProfit         float64 // 总浮动盈亏
    Margin              float64 // 占用保证金
    // ... 更多字段
}
```

### Order - 委托单

```go
type Order struct {
    OrderID         string  // 委托单ID
    ExchangeID      string  // 交易所代码
    InstrumentID    string  // 合约代码
    Direction       string  // 下单方向 BUY/SELL
    Offset          string  // 开平标志 OPEN/CLOSE/CLOSETODAY
    VolumeOrign     int64   // 总报单手数
    VolumeLeft      int64   // 未成交手数
    PriceType       string  // 价格类型 LIMIT/ANY
    LimitPrice      float64 // 委托价格
    Status          string  // 委托单状态 ALIVE/FINISHED
    InsertDateTime  int64   // 下单时间(纳秒)
}
```

### Trade - 成交记录

```go
type Trade struct {
    TradeID       string  // 成交ID
    OrderID       string  // 委托单ID
    ExchangeID    string  // 交易所代码
    InstrumentID  string  // 合约代码
    Direction     string  // 成交方向 BUY/SELL
    Offset        string  // 开平标志 OPEN/CLOSE/CLOSETODAY
    Price         float64 // 成交价格
    Volume        int64   // 成交手数
    TradeDateTime int64   // 成交时间(纳秒)
    Commission    float64 // 手续费
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
sub, _ := client.Series().Kline(ctx, "SHFE.au2512", time.Minute, 500)
```

### 多合约对齐

基于 TQSDK 协议的 binding 机制，自动对齐不同合约的 K线数据到相同的时间点。

```go
sub, _ := client.Series().KlineMulti(ctx,
    []string{"SHFE.au2512", "SHFE.ag2512"},
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
```

### DataManager 高级功能

DataManager 提供底层数据管理功能，支持路径监听、数据清理等。

#### 路径监听

```go
// 监听特定路径的数据变化
ch, err := client.dm.Watch(ctx, []string{"quotes", "SHFE.au2512"})
go func() {
    for data := range ch {
        fmt.Printf("数据更新: %v\n", data)
    }
}()

// 取消监听
client.dm.UnWatch([]string{"quotes", "SHFE.au2512"})
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

## 完整示例

完整示例代码请参考：
- [examples/quote/main.go](examples/quote/main.go) - 行情订阅示例
- [examples/history/main.go](examples/history/main.go) - 历史数据订阅示例
- [examples/datamanager/main.go](examples/datamanager/main.go) - DataManager 高级功能示例

## 常量定义

### 方向

```go
const (
    DirectionBuy  = "BUY"
    DirectionSell = "SELL"
)
```

### 开平

```go
const (
    OffsetOpen       = "OPEN"
    OffsetClose      = "CLOSE"
    OffsetCloseToday = "CLOSETODAY"
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
    OrderStatusAlive    = "ALIVE"    // 活动
    OrderStatusFinished = "FINISHED" // 已完成
)
```

## 错误处理

```go
var (
    ErrInvalidSymbol     = errors.New("tqsdk: invalid symbol")
    ErrSessionClosed     = errors.New("tqsdk: session closed")
    ErrNotLoggedIn       = errors.New("tqsdk: not logged in")
)

// 错误包装
type Error struct {
    Op   string // 操作名
    Err  error  // 原始错误
    Code string // 错误码
}
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

## 相关链接

- [天勤量化官网](https://www.shinnytech.com/)
- [DIFF 协议文档](https://www.shinnytech.com/diff/)
- [GitHub 仓库](https://github.com/pseudocodes/tqsdk-go)

## 免责声明

本项目明确拒绝对产品做任何明示或暗示的担保。由于软件系统开发本身的复杂性，无法保证本项目完全没有错误。如选择使用本项目表示您同意错误和/或遗漏的存在，在任何情况下本项目对于直接、间接、特殊的、偶然的、或间接产生的、使用或无法使用本项目进行交易和投资造成的盈亏、直接或间接引起的赔偿、损失、债务或是任何交易中止均不承担责任和义务。
