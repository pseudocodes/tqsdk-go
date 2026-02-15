# Trade 交易示例

本示例展示了如何使用 TQSDK Go V2 的新 `Trader` 接口进行交易操作。

## 核心特性

### Trader 接口
所有交易示例都使用统一的 `Trader` 接口，支持：
- ✅ **实盘交易**（TradeSession）- 连接真实账户
- ✅ **虚拟交易**（VirtualTrader）- 模拟交易，无需真实账户
- ✅ **回测交易**（自定义实现）- 历史数据回放

### 生命周期管理
- `Connect(ctx)` - 连接并初始化交易者
- `IsReady()` - 检查是否就绪可以交易
- `Close()` - 关闭连接并释放资源

## 示例说明

### 1. VirtualTraderExample - 虚拟交易者 ⭐ 推荐入门
展示如何使用虚拟交易者进行模拟交易：
- ✅ **无需真实账户**，直接运行
- 创建 `VirtualTrader` 实例（100万初始资金）
- 虚拟下单和成交
- 手动触发成交模拟（`SimulateTrade`）
- 查询虚拟账户状态
- **适合策略开发和调试**

```go
// 创建虚拟交易者
trader := tqsdk.NewVirtualTrader(ctx, 1000000.0, 0.0001)

// 虚拟下单
order, _ := trader.InsertOrder(ctx, &tqsdk.InsertOrderRequest{
    Symbol:     "SHFE.au2512",
    Direction:  tqsdk.DirectionBuy,
    Volume:     1,
})

// 模拟成交
vt := trader.(*tqsdk.VirtualTrader)
vt.SimulateTrade(order.OrderID, 500.0, 1)
```

### 2. TradeCallbackExample - 回调模式（实盘）
展示如何使用回调函数处理实盘交易数据：
- 使用 `Trader` 接口（返回实盘 TradeSession）
- 账户更新回调
- 持仓更新回调
- 订单更新回调
- 成交回调
- 通知回调
- 使用 `IsReady()` 检查就绪状态

### 3. TradeChannelExample - 流式模式
展示如何使用 Channel 处理交易数据：
- 通过 Channel 接收账户更新
- 通过 Channel 接收持仓更新
- 通过 Channel 接收订单更新
- 通过 Channel 接收成交
- 通过 Channel 接收通知

### 4. TradeMixedExample - 混合模式
展示如何混合使用回调和流式模式：
- 重要事件用回调（实时响应）
- 批量处理用流式（异步处理）
- 同步查询当前状态

### 5. TraderSwitchExample - 接口切换示例 ⭐ 重要
展示如何编写统一的策略代码，在不同交易模式间切换：
- 定义接受 `Trader` 接口的策略函数
- 使用虚拟交易者运行策略
- 使用实盘交易运行相同策略
- **同一份代码，多种运行模式**

```go
// 定义策略函数
func MyStrategy(ctx context.Context, trader tqsdk.Trader) {
    // 等待就绪
    for !trader.IsReady() {
        time.Sleep(100 * time.Millisecond)
    }
    
    // 查询账户
    account, _ := trader.GetAccount(ctx)
    
    // 下单...
}

// 虚拟交易
virtualTrader := tqsdk.NewVirtualTrader(ctx, 1000000.0, 0.0001)
MyStrategy(ctx, virtualTrader)

// 实盘交易（同样的策略代码）
realTrader, _ := client.LoginTrade(ctx, "simnow", "user", "pass")
MyStrategy(ctx, realTrader)
```

## 运行示例

### 快速开始（无需账户）

```bash
# 运行虚拟交易示例（推荐入门）
go run main.go
```

默认运行 `VirtualTraderExample()`，无需任何配置！

### 运行实盘交易示例（需要账户）

```bash
# 1. 设置环境变量
export SHINNYTECH_ID="your_username"
export SHINNYTECH_PW="your_password"
export SIMNOW_USER_0="your_simnow_account"
export SIMNOW_PASS_0="your_simnow_password"

# 2. 修改 main.go，取消注释相应示例
# TradeCallbackExample()    // 实盘 - 回调模式
# TradeChannelExample()     // 实盘 - 流式模式
# TradeMixedExample()       // 实盘 - 混合模式

# 3. 运行
go run main.go
```

## 功能演示

### 1. 回调模式（推荐用于实时响应）
```go
trader.OnAccount(func(account *tqsdk.Account) {
    fmt.Printf("账户更新: %.2f\n", account.Balance)
})

trader.OnOrder(func(order *tqsdk.Order) {
    fmt.Printf("订单: %s\n", order.OrderID)
})

trader.OnTrade(func(trade *tqsdk.Trade) {
    fmt.Printf("成交: %.2f x%d\n", trade.Price, trade.Volume)
})
```

### 2. 流式模式（适合批量处理）
```go
go func() {
    for account := range trader.AccountChannel() {
        // 处理账户更新
    }
}()

go func() {
    for order := range trader.OrderChannel() {
        // 处理订单更新
    }
}()
```

### 3. 同步查询
```go
account, _ := trader.GetAccount(ctx)
positions, _ := trader.GetPositions(ctx)
orders, _ := trader.GetOrders(ctx)
trades, _ := trader.GetTrades(ctx)
```

### 4. 交易操作
```go
// 下单
order, _ := trader.InsertOrder(ctx, &tqsdk.InsertOrderRequest{
    Symbol:     "SHFE.au2512",
    Direction:  tqsdk.DirectionBuy,
    Offset:     tqsdk.OffsetOpen,
    PriceType:  tqsdk.PriceTypeLimit,
    LimitPrice: 500.0,
    Volume:     1,
})

// 撤单
trader.CancelOrder(ctx, order.OrderID)
```

## Trader 接口优势

### 1. 统一抽象
```go
// 接口定义
type Trader interface {
    Connect(ctx context.Context) error
    IsReady() bool
    InsertOrder(ctx context.Context, req *InsertOrderRequest) (*Order, error)
    CancelOrder(ctx context.Context, orderID string) error
    GetAccount(ctx context.Context) (*Account, error)
    // ... 更多方法
}
```

### 2. 灵活切换
```go
var trader tqsdk.Trader

// 开发阶段：虚拟交易
trader = tqsdk.NewVirtualTrader(ctx, 1000000.0, 0.0001)

// 测试阶段：模拟盘
trader, _ = client.LoginTrade(ctx, "simnow", "user", "pass")

// 生产阶段：实盘
trader, _ = client.LoginTrade(ctx, "real_broker", "user", "pass")

// 策略代码无需修改！
runStrategy(ctx, trader)
```

### 3. 易于测试
```go
// 单元测试中使用 mock trader
type MockTrader struct {
    // ...
}

func TestStrategy(t *testing.T) {
    mockTrader := &MockTrader{}
    runStrategy(ctx, mockTrader)
    // 验证行为...
}
```

## 注意事项

### 1. 虚拟交易者
- ✅ 无需真实账户即可测试
- ✅ 适合策略开发和调试
- ✅ 可以手动触发成交模拟
- ⚠️ 不会连接真实市场数据
- ⚠️ 需要自己实现撮合逻辑

### 2. 生命周期管理
```go
// 创建
trader := tqsdk.NewVirtualTrader(ctx, 1000000.0, 0.0001)

// 连接（构造函数已自动调用）
trader.Connect(ctx)

// 检查就绪
if trader.IsReady() {
    // 可以开始交易
}

// 关闭
defer trader.Close()
```

### 3. 安全性
- ⚠️ 不要在代码中硬编码账号密码
- ✅ 使用环境变量管理敏感信息
- ✅ 生产环境使用配置文件或密钥管理服务

### 4. 下单提醒
- ⚠️ 示例代码中的实盘下单部分已注释
- ✅ 取消注释前请确认合约代码和价格
- ✅ 建议先使用 VirtualTrader 测试策略

### 5. 资源管理
```go
// 正确的资源管理
client, _ := tqsdk.NewClient(ctx, username, password)
defer client.Close() // 关闭客户端

trader, _ := client.LoginTrade(ctx, broker, user, pass)
defer trader.Close() // 关闭交易会话
```

### 6. 错误处理
```go
trader.OnError(func(err error) {
    log.Printf("交易错误: %v", err)
})

// 同步操作的错误处理
order, err := trader.InsertOrder(ctx, req)
if err != nil {
    log.Printf("下单失败: %v", err)
    return
}
```

## 进阶使用

### 自定义 Trader 实现

你可以实现自己的 Trader（例如回测引擎）：

```go
type BacktestTrader struct {
    // 你的字段
}

func (bt *BacktestTrader) Connect(ctx context.Context) error {
    // 初始化回测引擎
    return nil
}

func (bt *BacktestTrader) IsReady() bool {
    return true
}

// ... 实现其他方法

// 编译时检查
var _ tqsdk.Trader = (*BacktestTrader)(nil)
```

## 相关文档

- [Trader 接口详细文档](../../doc/TRADER_INTERFACE.md)
- [Client 使用文档](../../doc/CLIENT_USAGE.md)
- [DataManager 文档](../../doc/DATAMANAGER_ENHANCEMENT.md)

## 常见问题

**Q: 虚拟交易者的数据从哪里来？**
A: 虚拟交易者是完全独立的，不连接市场数据。你可以：
- 手动触发成交（`SimulateTrade`）
- 对接行情数据实现自动撮合（`UpdateMarketPrice`）
- 用于单元测试和策略逻辑验证

**Q: 如何实现回测？**
A: 实现 `Trader` 接口，在 `Connect()` 中加载历史数据，然后模拟时间推进和订单撮合。

**Q: Trader 接口和 TradeSession 什么关系？**
A: `TradeSession` 是 `Trader` 接口的实盘实现，`LoginTrade()` 返回 `Trader` 接口，实际是 `TradeSession`。

**Q: 可以同时使用多个 Trader 吗？**
A: 可以！你可以同时运行多个虚拟交易者测试不同策略，或者同时连接多个实盘账户。

```go
// 多策略测试
strategy1 := tqsdk.NewVirtualTrader(ctx, 1000000.0, 0.0001)
strategy2 := tqsdk.NewVirtualTrader(ctx, 1000000.0, 0.0001)

go runStrategy(ctx, strategy1)
go runStrategy(ctx, strategy2)
```
