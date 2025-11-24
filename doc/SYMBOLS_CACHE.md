# 合约信息缓存机制

TQSDK-GO V2 提供了灵活的合约信息缓存机制，支持本地缓存和多种缓存策略。

## 缓存策略

### 1. CacheStrategyAlwaysNetwork - 总是从网络获取

每次启动都从网络获取最新的合约信息，并保存到本地缓存。

```go
client, _ := tqsdk.NewClient(ctx, username, password,
    tqsdk.WithSymbolsCacheStrategy(tqsdk.CacheStrategyAlwaysNetwork),
)
```

**适用场景**：
- 需要确保使用最新合约信息
- 网络环境良好
- 不关心启动速度

### 2. CacheStrategyPreferLocal - 优先使用本地缓存

优先使用本地缓存，只有在缓存不存在或加载失败时才从网络获取。

```go
client, _ := tqsdk.NewClient(ctx, username, password,
    tqsdk.WithSymbolsCacheStrategy(tqsdk.CacheStrategyPreferLocal),
)
```

**适用场景**：
- 离线或网络环境不佳
- 追求快速启动
- 合约信息更新不频繁

### 3. CacheStrategyAutoRefresh - 自动刷新（默认）

如果本地缓存超过指定时间（默认 1 天），则自动从网络刷新。

```go
client, _ := tqsdk.NewClient(ctx, username, password,
    tqsdk.WithSymbolsCacheStrategy(tqsdk.CacheStrategyAutoRefresh),
    tqsdk.WithSymbolsCacheMaxAge(86400), // 1天，默认值
)
```

**适用场景**：
- 平衡数据新鲜度和启动速度
- 推荐的默认策略

## 缓存配置

### 设置缓存目录

默认缓存目录为 `$HOME/.tqsdk/latest.json`，可以自定义：

```go
client, _ := tqsdk.NewClient(ctx, username, password,
    tqsdk.WithSymbolsCacheDir("/path/to/custom/cache"),
)
```

### 设置缓存有效期

自定义缓存最大有效期（仅在 `CacheStrategyAutoRefresh` 策略下生效）：

```go
client, _ := tqsdk.NewClient(ctx, username, password,
    tqsdk.WithSymbolsCacheStrategy(tqsdk.CacheStrategyAutoRefresh),
    tqsdk.WithSymbolsCacheMaxAge(3600), // 1小时
)
```

## 完整示例

### 示例 1：生产环境配置（推荐）

```go
package main

import (
    "context"
    "fmt"
    
    tqsdk "github.com/pseudocodes/tqsdk-go/shinny"
)

func main() {
    ctx := context.Background()
    
    // 使用自动刷新策略，缓存有效期 1 天
    client, err := tqsdk.NewClient(ctx, "username", "password",
        tqsdk.WithSymbolsCacheStrategy(tqsdk.CacheStrategyAutoRefresh),
        tqsdk.WithSymbolsCacheMaxAge(86400), // 1天
        tqsdk.WithLogLevel("info"),
    )
    if err != nil {
        panic(err)
    }
    defer client.Close()
    
    // 初始化行情（会自动加载合约信息）
    if err := client.InitMarket(); err != nil {
        panic(err)
    }
    
    fmt.Println("客户端初始化完成")
}
```

### 示例 2：开发测试环境

```go
// 开发测试时总是获取最新数据
client, _ := tqsdk.NewClient(ctx, username, password,
    tqsdk.WithSymbolsCacheStrategy(tqsdk.CacheStrategyAlwaysNetwork),
    tqsdk.WithDevelopment(true),
)
```

### 示例 3：离线或网络不佳环境

```go
// 优先使用本地缓存，避免网络延迟
client, _ := tqsdk.NewClient(ctx, username, password,
    tqsdk.WithSymbolsCacheStrategy(tqsdk.CacheStrategyPreferLocal),
)
```

### 示例 4：自定义缓存目录

```go
// 使用自定义缓存目录
client, _ := tqsdk.NewClient(ctx, username, password,
    tqsdk.WithSymbolsCacheDir("/opt/myapp/cache"),
    tqsdk.WithSymbolsCacheStrategy(tqsdk.CacheStrategyAutoRefresh),
    tqsdk.WithSymbolsCacheMaxAge(7200), // 2小时
)
```

## 缓存文件格式

缓存文件为标准的 JSON 格式，与天勤服务器返回的格式一致：

```json
{
  "SHFE.au2602": {
    "ins_class": "FUTURE",
    "class": "FUTURE",
    "exchange_id": "SHFE",
    "instrument_id": "au2602",
    ...
  },
  "SHFE.ag2512": {
    ...
  }
}
```

## 注意事项

1. **缓存目录权限**：确保应用程序有权限在缓存目录中创建和写入文件
2. **缓存一致性**：如果手动修改了缓存文件，建议删除后重新生成
3. **磁盘空间**：缓存文件通常在 1-5 MB 左右，请确保有足够的磁盘空间
4. **并发安全**：缓存加载是在后台 goroutine 中进行的，不会阻塞客户端初始化

## 日志输出

通过日志可以观察缓存行为：

```
INFO    Symbols loaded    {"source": "cache", "count": 5432}  // 从缓存加载
INFO    Symbols loaded    {"source": "network", "count": 5432}  // 从网络加载
INFO    Cache expired, fetching from network  // 缓存过期
INFO    Local cache not available, fetching from network  // 缓存不存在
```

## 常见问题

### Q: 如何强制刷新缓存？

A: 使用 `CacheStrategyAlwaysNetwork` 策略，或删除缓存文件后重启应用。

### Q: 如何禁用缓存？

A: 使用 `CacheStrategyAlwaysNetwork` 策略，虽然仍会保存缓存，但每次都从网络获取。

### Q: 缓存过期后会立即从网络刷新吗？

A: 是的，在 `CacheStrategyAutoRefresh` 策略下，如果缓存过期，会自动从网络获取最新数据。

### Q: 多个应用实例可以共享缓存吗？

A: 可以，只要配置相同的 `SymbolsCacheDir`。但注意并发写入的问题。

