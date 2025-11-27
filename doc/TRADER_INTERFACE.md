# Trader 接口使用指南

## 概述

`Trader` 接口抽象了交易会话的核心功能，使得你可以轻松地在以下场景之间切换：
- **实盘交易**：连接真实期货账户
- **模拟交易**：使用虚拟资金进行交易
- **策略回测**：在历史数据上测试策略

## 接口定义

```go
type Trader interface {
    // 交易操作
    InsertOrder(ctx context.Context, req *InsertOrderRequest) (*Order, error)
    CancelOrder(ctx context.Context, orderID string) error
    
    // 同步查询
    GetAccount(ctx context.Context) (*Account, error)
    GetPosition(ctx context.Context, symbol string) (*Position, error)
    GetPositions(ctx context.Context) (map[string]*Position, error)
    GetOrders(ctx context.Context) (map[string]*Order, error)
    GetTrades(ctx context.Context) (map[string]*Trade, error)
    
    // 流式接口（Channel）
    AccountChannel() <-chan *Account
    PositionChannel() <-chan *PositionUpdate
    PositionsChannel() <-chan map[string]*Position
    OrderChannel() <-chan *Order
    TradeChannel() <-chan *Trade
    NotificationChannel() <-chan *Notification
    
    // 回调接口
    OnAccount(handler func(*Account))
    OnPosition(handler func(string, *Position))
    OnPositions(handler func(map[string]*Position))
    OnOrder(handler func(*Order))
    OnTrade(handler func(*Trade))
    OnNotification(handler func(*Notification))
    OnError(handler func(error))
    
    // 状态管理
    IsLoggedIn() bool
    Close() error
}
```

## 使用场景

### 1. 实盘交易（TradeSession）

连接真实的期货账户进行交易：

```go
package main

import (
    "context"
    "fmt"
    "github.com/yourusername/tqsdk-go/shinny"
)

func main() {
    ctx := context.Background()
    
    // 创建客户端
    client, err := shinny.NewClient(ctx, shinny.WithMdUrl("实盘行情"))
    if err != nil {
        panic(err)
    }
    defer client.Close()
    
    // 方式1: LoginTrade 自动连接（推荐）
    var trader shinny.Trader
    trader, err = client.LoginTrade(ctx, "simnow", "username", "password")
    if err != nil {
        panic(err)
    }
    defer trader.Close()
    
    // 方式2: 显式调用 Connect（用于重连场景）
    // trader.Connect(ctx) // LoginTrade 内部已自动调用
    
    // 检查是否已就绪
    if !trader.IsReady() {
        panic("trader not ready")
    }
    
    // 注册回调
    trader.OnOrder(func(order *shinny.Order) {
        fmt.Printf("订单更新: ID=%s, 状态=%s\n", order.OrderID, order.Status)
    })
    
    trader.OnTrade(func(trade *shinny.Trade) {
        fmt.Printf("成交: 合约=%s.%s, 价格=%.2f, 数量=%d\n",
            trade.ExchangeID, trade.InstrumentID, trade.Price, trade.Volume)
    })
    
    // 下单
    order, err := trader.InsertOrder(ctx, &shinny.InsertOrderRequest{
        Symbol:     "SHFE.cu2401",
        Direction:  shinny.DirectionBuy,
        Offset:     shinny.OffsetOpen,
        Volume:     1,
        PriceType:  shinny.PriceTypeLimit,
        LimitPrice: 70000.0,
    })
    if err != nil {
        panic(err)
    }
    
    fmt.Printf("下单成功: %s\n", order.OrderID)
    
    // 查询账户
    account, _ := trader.GetAccount(ctx)
    fmt.Printf("账户权益: %.2f\n", account.Balance)
    
    // 保持运行
    select {}
}
```

### 2. 模拟交易（VirtualTrader）

使用虚拟资金进行交易，适合策略开发和测试：

```go
package main

import (
    "context"
    "fmt"
    "github.com/yourusername/tqsdk-go/shinny"
)

func main() {
    ctx := context.Background()
    
    // 方式1: 直接创建虚拟交易者（自动连接）
    var trader shinny.Trader
    trader = shinny.NewVirtualTrader(ctx, 1000000.0, 0.0001) // 100万初始资金，万分之一手续费
    defer trader.Close()
    
    // 方式2: 创建后显式连接
    trader = shinny.NewVirtualTrader(ctx, 1000000.0, 0.0001)
    if err := trader.Connect(ctx); err != nil {
        panic(err)
    }
    
    // 方式3: 通过 Client 注册虚拟交易者（推荐，统一管理）
    client, _ := shinny.NewClient(ctx)
    virtualTrader := shinny.NewVirtualTrader(ctx, 1000000.0, 0.0001)
    client.RegisterTrader("virtual:test1", virtualTrader)
    
    // 获取已注册的交易者
    trader, _ = client.GetTrader("virtual:test1")
    
    // 检查是否已就绪
    if trader.IsReady() {
        fmt.Println("虚拟交易者已就绪")
    }
    
    // 注册回调
    trader.OnOrder(func(order *shinny.Order) {
        fmt.Printf("虚拟订单: %s, 状态=%s\n", order.OrderID, order.Status)
    })
    
    trader.OnTrade(func(trade *shinny.Trade) {
        fmt.Printf("虚拟成交: 价格=%.2f, 数量=%d\n", trade.Price, trade.Volume)
    })
    
    // 下单（虚拟）
    order, err := trader.InsertOrder(ctx, &shinny.InsertOrderRequest{
        Symbol:     "SHFE.cu2401",
        Direction:  shinny.DirectionBuy,
        Offset:     shinny.OffsetOpen,
        Volume:     1,
        PriceType:  shinny.PriceTypeLimit,
        LimitPrice: 70000.0,
    })
    if err != nil {
        panic(err)
    }
    
    fmt.Printf("虚拟下单成功: %s\n", order.OrderID)
    
    // 模拟成交（在实际使用中，应该对接行情进行自动撮合）
    vt := trader.(*shinny.VirtualTrader)
    vt.SimulateTrade(order.OrderID, 70000.0, 1)
    
    // 查询虚拟账户
    account, _ := trader.GetAccount(ctx)
    fmt.Printf("虚拟账户权益: %.2f\n", account.Balance)
}
```

### 3. 策略回测（BacktestTrader - 待实现）

在历史数据上测试策略：

```go
package main

import (
    "context"
    "time"
    "github.com/yourusername/tqsdk-go/shinny"
)

func main() {
    ctx := context.Background()
    
    // 创建回测交易者（示例，需要自己实现）
    var trader shinny.Trader
    trader = NewBacktestTrader(
        time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC),  // 开始时间
        time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC), // 结束时间
        1000000.0,                                      // 初始资金
    )
    defer trader.Close()
    
    // 运行策略
    runStrategy(ctx, trader)
    
    // 输出回测结果
    account, _ := trader.GetAccount(ctx)
    fmt.Printf("最终权益: %.2f\n", account.Balance)
}

func runStrategy(ctx context.Context, trader shinny.Trader) {
    // 你的策略逻辑
    // ...
}
```

## Trader 生命周期管理

### Connect - 连接并初始化

所有 Trader 实现都需要实现 `Connect()` 方法：

```go
// 对于实盘交易：连接交易服务器并登录
trader, _ := client.LoginTrade(ctx, "simnow", "user", "pass")
// LoginTrade 内部自动调用了 Connect()

// 对于模拟交易：初始化虚拟环境
virtualTrader := shinny.NewVirtualTrader(ctx, 1000000.0, 0.0001)
// NewVirtualTrader 内部自动调用了 Connect()

// 显式调用 Connect（用于重连）
if err := trader.Connect(ctx); err != nil {
    log.Fatal(err)
}
```

### IsReady - 检查是否就绪

```go
if trader.IsReady() {
    // 可以开始交易
    trader.InsertOrder(ctx, req)
} else {
    // 需要先连接
    trader.Connect(ctx)
}
```

## Client 的 Trader 管理

`Client` 提供了三个方法来管理 Trader：

### 1. LoginTrade - 创建实盘交易会话

```go
// 返回 Trader 接口（实际是 TradeSession 实现）
// 内部自动调用 Connect() 进行连接和登录
trader, err := client.LoginTrade(ctx, "simnow", "user", "pass")
```

### 2. RegisterTrader - 注册自定义交易者

```go
// 注册虚拟交易者
virtualTrader := shinny.NewVirtualTrader(ctx, 1000000.0, 0.0001)
client.RegisterTrader("virtual:strategy1", virtualTrader)

// 注册回测引擎
backtestTrader := NewBacktestTrader(startTime, endTime, 1000000.0)
client.RegisterTrader("backtest:test1", backtestTrader)
```

### 3. GetTrader - 获取已注册的交易者

```go
trader, exists := client.GetTrader("virtual:strategy1")
if exists {
    trader.InsertOrder(ctx, req)
}
```

## 统一策略开发

使用 `Trader` 接口，你可以编写一次策略代码，在不同环境中运行：

```go
// 策略函数，接受 Trader 接口
func MyStrategy(ctx context.Context, trader shinny.Trader) error {
    // 注册回调
    trader.OnOrder(func(order *shinny.Order) {
        log.Printf("订单更新: %+v", order)
    })
    
    trader.OnTrade(func(trade *shinny.Trade) {
        log.Printf("成交: %+v", trade)
    })
    
    // 查询账户
    account, err := trader.GetAccount(ctx)
    if err != nil {
        return err
    }
    log.Printf("账户权益: %.2f", account.Balance)
    
    // 下单逻辑
    _, err = trader.InsertOrder(ctx, &shinny.InsertOrderRequest{
        Symbol:     "SHFE.cu2401",
        Direction:  shinny.DirectionBuy,
        Offset:     shinny.OffsetOpen,
        Volume:     1,
        PriceType:  shinny.PriceTypeLimit,
        LimitPrice: 70000.0,
    })
    
    return err
}

func main() {
    ctx := context.Background()
    
    // 场景1: 实盘运行
    realTrader, _ := client.Trade(ctx, "simnow", "user", "pass")
    MyStrategy(ctx, realTrader)
    
    // 场景2: 模拟交易
    virtualTrader := shinny.NewVirtualTrader(ctx, 1000000.0, 0.0001)
    MyStrategy(ctx, virtualTrader)
    
    // 场景3: 回测
    backtestTrader := NewBacktestTrader(startTime, endTime, 1000000.0)
    MyStrategy(ctx, backtestTrader)
}
```

## VirtualTrader 高级功能

### 对接行情进行自动撮合

```go
package main

import (
    "context"
    "github.com/yourusername/tqsdk-go/shinny"
)

func main() {
    ctx := context.Background()
    
    // 创建客户端和虚拟交易者
    client, _ := shinny.NewClient(ctx)
    vt := shinny.NewVirtualTrader(ctx, 1000000.0, 0.0001)
    
    // 订阅行情
    sub, _ := client.Series.Kline(ctx, "SHFE.cu2401", time.Minute, 100)
    
    // 监听行情更新，自动撮合
    sub.OnBarUpdate(func(data *shinny.SeriesData) {
        if data.Single != nil && len(data.Single.Data) > 0 {
            lastBar := data.Single.Data[len(data.Single.Data)-1]
            
            // 更新市场价格，触发自动撮合
            vt.UpdateMarketPrice("SHFE.cu2401", lastBar.Close)
        }
    })
    
    // 下单
    vt.InsertOrder(ctx, &shinny.InsertOrderRequest{
        Symbol:     "SHFE.cu2401",
        Direction:  shinny.DirectionBuy,
        Offset:     shinny.OffsetOpen,
        Volume:     1,
        PriceType:  shinny.PriceTypeLimit,
        LimitPrice: 70000.0,
    })
    
    // 当行情达到70000时，订单会自动成交
    select {}
}
```

## 实现自己的 Trader

如果你需要实现特殊的交易逻辑（例如回测引擎），只需实现 `Trader` 接口：

```go
type BacktestTrader struct {
    // 你的字段
}

func (bt *BacktestTrader) InsertOrder(ctx context.Context, req *InsertOrderRequest) (*Order, error) {
    // 你的实现
}

func (bt *BacktestTrader) CancelOrder(ctx context.Context, orderID string) error {
    // 你的实现
}

// ... 实现其他方法

// 编译时检查
var _ Trader = (*BacktestTrader)(nil)
```

## 重连场景

`Connect` 方法支持重连：

```go
// 创建实盘交易者
trader, _ := client.LoginTrade(ctx, "simnow", "user", "pass")

// ... 使用一段时间后断线 ...

// 重新连接
if err := trader.Connect(ctx); err != nil {
    log.Printf("重连失败: %v", err)
    // 进行错误处理或重试
}

// 检查连接状态
for !trader.IsReady() {
    time.Sleep(time.Second)
    trader.Connect(ctx)
}
```

## 注意事项

1. **生命周期管理**：
   - 构造函数：创建对象，分配资源
   - `Connect()`：连接/初始化，可以重复调用（幂等）
   - `IsReady()`：检查是否可以开始交易
   - `Close()`：释放资源，关闭连接

2. **线程安全**：所有实现都是线程安全的

3. **资源释放**：使用完毕后务必调用 `Close()`

4. **上下文管理**：所有操作都支持 context，可以设置超时和取消

5. **错误处理**：注册 `OnError` 回调处理异步错误

6. **向后兼容**：
   - `IsLoggedIn()` 仍然可用，是 `IsReady()` 的别名
   - 构造函数自动调用 `Connect()`，保持现有代码正常工作

## 总结

`Trader` 接口提供了统一的交易抽象，让你可以：
- ✅ 编写一次代码，在多个环境运行
- ✅ 轻松切换实盘、模拟、回测
- ✅ 便于单元测试（使用 mock trader）
- ✅ 灵活扩展自定义交易逻辑

