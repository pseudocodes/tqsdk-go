# DataManager 示例

本示例展示了 TQSDK Go V2 中 DataManager 的高级功能。

## 功能演示

### 1. Watch/UnWatch - 路径监听

精细化监听特定路径的数据变化：

```go
// 监听特定路径
ch, _ := dm.Watch(ctx, []string{"quotes", "SHFE.au2512"})

// 接收数据
go func() {
    for data := range ch {
        // 处理数据
    }
}()

// 取消监听
dm.UnWatch([]string{"quotes", "SHFE.au2512"})
```

**特点**：
- 基于 Channel 的异步通知
- 支持 Context 取消
- 自动管理 goroutine 生命周期
- 非阻塞发送，避免死锁

### 2. 动态配置

运行时动态调整 DataManager 配置：

```go
// 设置视图宽度
dm.SetViewWidth(1000)
width := dm.GetViewWidth()

// 设置数据保留时间
dm.SetDataRetention(24 * time.Hour)

// 清理过期数据
dm.Cleanup()
```

### 3. 标准数据访问

两种方式访问数据：

```go
// Get - 带错误返回（推荐）
data, err := dm.Get([]string{"quotes", "SHFE.au2512"})
if err != nil {
    // 处理错误
}

// GetByPath - 兼容接口
data := dm.GetByPath([]string{"quotes", "SHFE.au2512"})
```

### 4. 多路径监听

同时监听多个路径的数据变化。

## 运行

```bash
cd examples/datamanager
go run main.go
```

## 核心概念

### Watch vs OnData

| 特性 | Watch | OnData |
|------|-------|--------|
| 粒度 | 特定路径 | 全局 |
| 通知方式 | Channel | 回调函数 |
| 管理 | 手动 UnWatch | 自动 |
| 适用场景 | 精细控制 | 全局监听 |

### 性能考虑

- Watch 使用推送模式，不轮询，性能高效
- 每个 watcher 有独立的 goroutine，开销小
- Channel 缓冲区默认 10，可防止短时拥塞
- 非阻塞发送，避免慢消费者影响系统

### 最佳实践

1. **使用 Context 管理生命周期**
   ```go
   ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
   defer cancel()
   ch, _ := dm.Watch(ctx, path)
   ```

2. **及时 UnWatch**
   ```go
   defer dm.UnWatch(path)
   ```

3. **错误处理**
   ```go
   ch, err := dm.Watch(ctx, path)
   if err != nil {
       log.Printf("Watch failed: %v", err)
       return
   }
   ```

4. **避免重复监听**
   - 同一路径不能重复 Watch
   - 使用前检查或捕获错误

## 注意事项

- Watch 监听是路径级别的，不会收到子路径的变化通知
- Channel 满时会跳过数据，避免阻塞
- Context 取消后，Watch 会自动清理资源
- Cleanup 只清理过期数据，不影响当前活跃数据

