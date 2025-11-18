# Trade 交易示例

本示例展示了如何使用 TQSDK Go V2 进行交易操作。

## 功能演示

### 1. 回调模式（推荐用于实时响应）
- `OnAccount()` - 账户更新回调
- `OnPosition()` - 单个持仓更新回调
- `OnPositions()` - 全量持仓更新回调
- `OnOrder()` - 订单更新回调
- `OnTrade()` - 成交回调
- `OnNotification()` - 通知回调

### 2. 流式模式（适合批量处理）
- `AccountChannel()` - 账户更新流
- `PositionChannel()` - 持仓更新流
- `OrderChannel()` - 订单更新流
- `TradeChannel()` - 成交流
- `NotificationChannel()` - 通知流

### 3. 混合模式（推荐）
- 重要的用回调（实时响应）
- 批量处理用流式

### 4. 同步查询
- `GetAccount()` - 获取账户信息
- `GetPositions()` - 获取所有持仓
- `GetOrders()` - 获取所有订单
- `GetTrades()` - 获取所有成交

### 5. 交易操作
- `InsertOrder()` - 下单
- `CancelOrder()` - 撤单

## 运行

```bash
# 设置环境变量
export SHINNYTECH_ID="your_username"
export SHINNYTECH_PW="your_password"
export SIMNOW_USER_0="your_simnow_user"
export SIMNOW_PASS_0="your_simnow_pass"

# 运行示例
go run main.go
```

## 核心概念

### 会话设计

交易采用会话设计，每个交易账户对应一个 `TradeSession`：

```go
session, _ := client.LoginTrade(ctx, "simnow", userID, password)
defer session.Close()
```

### 双模式支持

同时支持流式（Channel）和回调两种模式，可以混合使用：

```go
// 重要的用回调（实时响应）
session.OnTrade(func(trade *Trade) {
    // 立即处理成交
})

// 批量处理用流式
go func() {
    for update := range session.PositionChannel() {
        // 批量更新UI
    }
}()
```

### 注意事项

示例中的下单代码已注释，避免实际下单。如需测试下单功能，请取消注释并谨慎操作。

