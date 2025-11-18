# TQSDK Go V2 Examples

本目录包含 TQSDK Go V2 的示例程序。

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
├── datamanager/    # DataManager 高级功能示例
│   ├── main.go
│   ├── go.mod
│   └── README.md
└── history/        # 历史数据订阅示例
    ├── main.go
    ├── go.mod
    └── README.md
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

### 2. 运行行情示例

```bash
cd quote
go run main.go
```

示例包含：
- Quote 订阅（实时行情）
- 单合约 K线订阅
- 多合约 K线订阅（自动对齐）
- Tick 订阅

### 3. 运行交易示例

```bash
cd trade
go run main.go
```

示例包含：
- 回调模式交易
- 流式模式交易
- 混合模式交易

### 4. 运行 DataManager 示例

```bash
cd datamanager
go run main.go
```

示例包含：
- Watch/UnWatch 路径监听
- 动态配置管理
- 多路径同时监听
- 标准数据访问接口

### 5. 运行历史数据订阅示例

```bash
cd history
go run main.go
```

示例包含：
- 使用 left_kline_id 订阅历史数据
- 使用 focus_datetime + focus_position 订阅
- 大量数据自动分片传输（最多 10000 根）
- 数据传输状态监控

## 核心特性

### 行情订阅

#### Quote 订阅
```go
quoteSub, _ := client.SubscribeQuote(ctx, "SHFE.au2512", "SHFE.ag2512")

// 方式 1: Channel
go func() {
    for quote := range quoteSub.QuoteChannel() {
        fmt.Printf("%s: %.2f\n", quote.InstrumentID, quote.LastPrice)
    }
}()

// 方式 2: 回调
quoteSub.OnQuote(func(quote *Quote) {
    fmt.Printf("%s: %.2f\n", quote.InstrumentID, quote.LastPrice)
})
```

#### K线订阅
```go
sub, _ := client.Series().Kline(ctx, "SHFE.cu2501", time.Minute, 500)

// 详细更新信息
sub.OnUpdate(func(data *SeriesData, info *UpdateInfo) {
    if info.HasNewBar {
        fmt.Println("新 K线!")
    }
})

// 新 K线回调
sub.OnNewBar(func(symbol string, bar interface{}) {
    kline := bar.(*Kline)
    fmt.Printf("新 K线: %.2f\n", kline.Close)
})
```

#### 多合约对齐
```go
sub, _ := client.Series().KlineMulti(ctx,
    []string{"SHFE.au2512", "SHFE.ag2512"},
    time.Minute, 10)

sub.OnUpdate(func(data *SeriesData, info *UpdateInfo) {
    // 访问对齐的数据
    for _, set := range data.Multi.Data {
        for symbol, kline := range set.Klines {
            fmt.Printf("%s: %.2f\n", symbol, kline.Close)
        }
    }
})
```

### 交易操作

#### 登录和会话
```go
session, _ := client.LoginTrade(ctx, "simnow", userID, password)
defer session.Close()
```

#### 回调模式
```go
session.OnAccount(func(account *Account) {
    fmt.Printf("权益: %.2f\n", account.Balance)
})

session.OnOrder(func(order *Order) {
    fmt.Printf("订单: %s\n", order.OrderID)
})

session.OnTrade(func(trade *Trade) {
    fmt.Printf("成交: %s\n", trade.TradeID)
})
```

#### 流式模式
```go
go func() {
    for account := range session.AccountChannel() {
        fmt.Printf("账户更新: %.2f\n", account.Balance)
    }
}()
```

#### 下单
```go
order, _ := session.InsertOrder(ctx, &InsertOrderRequest{
    Symbol:     "SHFE.au2512",
    Direction:  DirectionBuy,
    Offset:     OffsetOpen,
    PriceType:  PriceTypeLimit,
    LimitPrice: 500.0,
    Volume:     1,
})
```

## 注意事项

1. **认证信息**: 请妥善保管您的账号密码，建议使用环境变量
2. **模拟账户**: 建议先使用模拟账户测试
3. **风险提示**: 交易有风险，请谨慎操作
4. **下单测试**: 示例中的下单代码已注释，避免误操作

## 更多文档

- [主文档](../README_V2.md) - 完整的 API 文档和迁移指南
- [Quote 示例文档](quote/README.md) - 行情订阅详细说明
- [Trade 示例文档](trade/README.md) - 交易操作详细说明

## 问题反馈

如有问题，请提交 Issue。

