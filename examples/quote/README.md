# Quote 行情订阅示例

本示例展示了如何使用 TQSDK Go V2 订阅行情数据。

## 功能演示

### 1. Quote 订阅（全局实时行情）
- 流式接口（Channel）
- 回调接口

### 2. 单合约 K线订阅
- `OnUpdate()` - 通用更新回调，包含详细的 UpdateInfo
- `OnNewBar()` - 新 K线回调
- `OnBarUpdate()` - K线更新回调（盘中实时）

### 3. 多合约 K线订阅
- 自动对齐多个合约的 K线数据
- 基于 binding 机制

### 4. Tick 订阅
- 单个合约的 Tick 数据流

## 运行

```bash
# 设置环境变量
export SHINNYTECH_ID="your_username"
export SHINNYTECH_PW="your_password"

# 运行示例
go run main.go
```

## 核心概念

### UpdateInfo

`UpdateInfo` 提供详细的数据更新信息：
- `HasNewBar` - 是否有新 K线/Tick
- `NewBarIDs` - 新 K线的 ID 映射
- `HasBarUpdate` - 是否有 K线更新
- `ChartRangeChanged` - Chart 范围是否变化
- `HasChartSync` - Chart 是否同步完成

### ViewWidth

控制返回的数据量，只保留最新的 N 条数据，优化内存使用。

### 多合约对齐

基于 TQSDK 协议的 binding 机制，自动对齐不同合约的 K线数据到相同的时间点。

