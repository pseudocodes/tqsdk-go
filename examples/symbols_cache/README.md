# 合约信息缓存示例

本示例展示了如何使用 TQSDK Go V2 的合约信息缓存功能。

## 功能演示

### 1. 默认配置（自动刷新策略）

使用默认的自动刷新策略，缓存有效期 1 天：

```go
client, _ := tqsdk.NewClient(ctx, username, password,
    tqsdk.WithLogLevel("info"),
)
```

### 2. 总是从网络获取

每次启动都从网络获取最新合约信息：

```go
client, _ := tqsdk.NewClient(ctx, username, password,
    tqsdk.WithSymbolsCacheStrategy(tqsdk.CacheStrategyAlwaysNetwork),
)
```

### 3. 优先使用本地缓存

优先使用本地缓存，提高启动速度：

```go
client, _ := tqsdk.NewClient(ctx, username, password,
    tqsdk.WithSymbolsCacheStrategy(tqsdk.CacheStrategyPreferLocal),
)
```

### 4. 自定义缓存配置

自定义缓存目录和有效期：

```go
client, _ := tqsdk.NewClient(ctx, username, password,
    tqsdk.WithSymbolsCacheDir("/custom/cache/dir"),
    tqsdk.WithSymbolsCacheStrategy(tqsdk.CacheStrategyAutoRefresh),
    tqsdk.WithSymbolsCacheMaxAge(3600), // 1小时
)
```

## 运行示例

```bash
# 设置环境变量
export SHINNYTECH_ID="your_username"
export SHINNYTECH_PW="your_password"

# 运行示例
go run main.go
```

## 缓存策略

| 策略 | 说明 | 适用场景 |
|------|------|---------|
| `CacheStrategyAlwaysNetwork` | 总是从网络获取 | 需要最新数据 |
| `CacheStrategyPreferLocal` | 优先使用本地缓存 | 离线环境、快速启动 |
| `CacheStrategyAutoRefresh` | 自动刷新（默认） | 平衡新鲜度和速度 |

## 注意事项

1. 缓存文件默认保存在 `$HOME/.tqsdk/latest.json`
2. 可以通过 `WithSymbolsCacheDir()` 自定义缓存目录
3. 缓存加载是异步的，不会阻塞客户端初始化
4. 查看日志可以了解缓存的加载来源（cache 或 network）

## 相关文档

- [主文档](../../README.md)
- [缓存机制详解](../../SYMBOLS_CACHE.md)

