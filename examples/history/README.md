# 历史数据订阅示例

此示例展示如何订阅历史 K线/Tick 数据，支持大量历史数据的分片传输。

## 功能特点

1. **支持大量历史数据订阅**：最多可订阅 10000 根 K线/Tick
2. **自动分片传输**：超过 3000 根数据会自动分片传输（按时间逆序）
3. **传输状态监控**：通过 `ChartReady` 标志判断数据是否传输完成
4. **两种订阅方式**：
   - `left_kline_id`：从指定的 K线 ID 开始订阅
   - `focus_datetime + focus_position`：从指定时间点向左/右扩展订阅

## 运行示例

```bash
# 设置环境变量
export SHINNYTECH_ID="your_username"
export SHINNYTECH_PW="your_password"

# 运行示例
cd examples/history
go run main.go
```

## API 使用

### 1. 使用 left_kline_id 订阅

```go
// 从指定的 K线 ID 开始订阅 8000 根历史数据
leftKlineID := int64(105761)
sub, err := client.Series().KlineHistory(ctx, "SHFE.au2512", 60*time.Second, 8000, leftKlineID)
```

### 2. 使用 focus_datetime + focus_position 订阅

```go
// 从指定时间点开始订阅
// focusPosition: 1=从该时间向右扩展, -1=从该时间向左扩展
focusTime := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)
sub, err := client.Series().KlineHistoryWithFocus(ctx, "SHFE.au2512", 60*time.Second, 8000, focusTime, 1)
```

### 3. 监听数据传输状态

```go
sub.OnUpdate(func(data *tqsdk.SeriesData, info *tqsdk.UpdateInfo) {
    // 初次同步完成
    if info.HasChartSync {
        fmt.Printf("✅ Chart 初次同步完成\n")
    }
    
    // 范围变化（分片到达）
    if info.ChartRangeChanged {
        fmt.Printf("📊 Chart 范围变化: [%d,%d] -> [%d,%d]\n",
            info.OldLeftID, info.OldRightID,
            info.NewLeftID, info.NewRightID)
    }
    
    // 所有分片传输完成
    if info.ChartReady {
        fmt.Printf("🎉 所有历史数据传输完成！\n")
        fmt.Printf("   总数据量: %d 根K线\n", len(data.GetSymbolKlines("SHFE.au2512").Data))
    }
})
```

## 数据传输说明

根据日志文件 `out2.log` 的分析：

1. **分片大小**：每片最多 3000 根数据
2. **传输顺序**：按时间逆序返回（先返回最近的数据）
3. **状态标志**：
   - `more_data=true, ready=false`：还有更多分片未传输
   - `more_data=false, ready=true`：所有分片传输完成
4. **最大订阅量**：10000 根 K线/Tick

## 注意事项

1. 订阅历史数据时，view_width 会被限制为最大 10000
2. 数据分片是按时间逆序返回的，但每片内部按时间正序排列
3. 使用 `ChartReady` 标志判断所有数据是否已接收完毕
4. 历史数据订阅同样支持实时更新（最新一根 K线）

