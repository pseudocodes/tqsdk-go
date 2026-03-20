# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] - 2026-03-21

### Added

- **v1alpha2 架构**: 全新设计的 v1alpha2 版本，采用分层架构
  - `api/` — 核心接口定义层：`Client`、`MarketService`、`TradeService`、`AuthService` 等接口及类型
  - `domain/` — 业务实现层：认证 (`AuthService`)、行情 (`MarketService`)、交易 (`TradeService`)、风控 (`RiskManager`)
  - `infra/` — 基础设施层：WebSocket 连接管理 (`WSManager`)、JSON 编解码 (`codec`)、GraphQL 查询构建器 (`graphqlx`)
  - `store/` — 数据存储层：行情存储 (`MarketStore`)、交易存储 (`TradeStore`)、交易状态存储 (`TradingStatusStore`)
  - `app/` — 应用组装层：`NewWiring` / `NewDefaultClient` 一键构建完整客户端

- **回测引擎** (`backtest/`): 完整的本地回测框架
  - `runtime/` — 事件驱动内核 (`EventKernel`)、模拟时钟 (`Clock`)
  - `data/` — 历史数据下载器、内存数据提供者 (`MemoryProvider`)、LRU 缓存
  - `market/` — 模拟行情服务 (`SimMarketService`)，支持懒加载数据下载
  - `trade/` — 模拟交易服务 (`SimTradeService`)、期货/期权撮合与结算
  - `report/` — 回测报告：收益率、最大回撤、夏普率、索提诺比率、卡玛比率、胜率、盈亏比等指标计算与图表生成
  - 支持 `app.NewBacktestWiring` 一键构建回测环境

- **SyncApi** (`syncapi/`): 同步编程模型，对标 Python tqsdk 的 `wait_update` / `is_changing` 模式
  - `QuoteRef` / `KlineRef` / `AccountRef` / `PositionRef` 等引用类型
  - `WaitUpdate()` 阻塞等待数据更新
  - `IsChanging()` 检测数据变化，支持字段级粒度

- **WebAdapter** (`webadapter/`): 浏览器可视化适配层
  - `Adapter` — 将 Client 事件转换为 tqsdk web UI 协议
  - `Gateway` — HTTP/WebSocket 网关，提供 `/ws`、`/snapshot`、`/url` 端点
  - `IndicatorOverlay` — 自定义指标叠加层，支持在 K 线图上绘制 MA 等指标

- **v1alpha2 示例程序** (`examples/v1alpha2/`):
  - `backtest_demo` — MA 均线交叉回测策略
  - `indicator_demo` — 回测 + 自定义指标 Web 可视化
  - `market_demo` — 行情订阅、Tick/K线下载器
  - `market_query_demo` — GraphQL 查询、合约信息、期权链、历史主力合约、结算价、排名等
  - `syncapi_demo` — 同步编程模型演示
  - `webadapter_demo` — 回测 + Web UI 可视化
  - 所有示例统一使用环境变量 `SHINNYTECH_ID` / `SHINNYTECH_PW` 进行认证

- **新增依赖**: `bytedance/sonic` (高性能 JSON)、`tidwall/gjson` (JSON 路径查询)

- **合约信息缓存机制**: 支持本地缓存合约信息，提高启动速度
  - 新增三种缓存策略：`CacheStrategyAlwaysNetwork`、`CacheStrategyPreferLocal`、`CacheStrategyAutoRefresh`（默认）
  - 缓存目录可配置，默认为 `$HOME/.tqsdk/latest.json`
  - 缓存有效期可配置，默认为 1 天（86400 秒）
  - 自动检测缓存过期并刷新
  - 新增配置选项：`WithSymbolsCacheDir()`、`WithSymbolsCacheStrategy()`、`WithSymbolsCacheMaxAge()`
  - 添加详细文档和示例程序 `examples/symbols_cache/`

- **延迟启动模式**: Series API 订阅支持延迟启动，避免竞态条件
  - 默认接口（`Kline()`, `Tick()` 等）采用延迟启动模式
  - 新增 `Start()` 方法手动启动监听
  - 新增 `AndStart` 后缀接口（`KlineAndStart()` 等）提供向后兼容
  - 确保所有回调注册完成后再接收数据，不会错过早期数据

### Changed

- **完善 `.gitignore`**: 添加 Go 构建产物、IDE 配置、OS 临时文件及 v1alpha2 示例二进制文件的忽略规则

- **重构 Series API**: 改进订阅对象的创建和启动机制
  - `NewSeriesSubscription()` 不再自动启动监听
  - 初始状态 `running = false`
  - 用户需要显式调用 `Start()` 启动监听
  
- **优化数据获取流程**: 改进 `GetKlinesData()` 性能
  - 减少重复的数据查找操作
  - 提前传递 `rightID` 避免重复查询

- **文档更新**: 移除所有 README 文档中的 emoji 符号，使文档更加专业简洁

### Fixed

- **修复订阅时机问题**: 解决 `OnNewBar` 和 `OnBarUpdate` 回调的竞态条件
  - 修改 `detectNewBars()` 逻辑，移除 `lastID != -1` 的检查
  - 确保首次数据也能正确触发回调

- **修复 K线更新时机**: 改进 `OnBarUpdate` 的触发条件
  - 调整更新检测逻辑，确保实时更新正确触发

### Documentation

- 新增 `SYMBOLS_CACHE.md` 文档，详细说明缓存机制
- 新增 `examples/symbols_cache/` 示例程序和文档
- 更新主 README，添加缓存配置说明
- 更新所有示例文档，展示延迟启动模式的最佳实践

## [0.1.0] - 2024-11-24

### Added

- **V2 版本发布**: 全新重构的 TQSDK-GO V2
  - 强类型 API 设计，消除 `map[string]interface{}` 的使用
  - 完善的并发安全保护
  - 支持 Context、Channel、Callback 多种编程模式
  - 优化的数据管理和内存使用

- **Series API**: 序列数据订阅功能
  - 单合约 K线订阅：`Kline()`
  - 多合约 K线订阅（自动对齐）：`KlineMulti()`
  - Tick 数据订阅：`Tick()`
  - 历史数据订阅：`KlineHistory()`, `KlineHistoryWithFocus()`, `TickHistory()`
  - ViewWidth 控制，优化内存使用
  - 支持 `OnUpdate()`, `OnNewBar()`, `OnBarUpdate()` 多种回调

- **Quote 订阅**: 实时行情订阅
  - Channel 模式：`QuoteChannel()`
  - Callback 模式：`OnQuote()`
  - 动态添加/移除合约：`AddSymbols()`, `RemoveSymbols()`

- **TradeSession**: 交易会话管理
  - 多账户支持
  - 下单、撤单操作
  - 账户、持仓、订单查询
  - 实时监听（Callback 和 Channel 模式）

- **DataManager**: 数据管理器
  - 路径监听：`Watch()`, `UnWatch()`
  - 动态配置管理
  - 自动清理过期数据

- **WebSocket 连接**: 优化的 WebSocket 实现
  - 自动重连机制
  - 心跳保活
  - 全局事件发射器

### Changed

- **项目结构重组**: 优化代码组织
  - 核心代码移至 `shinny/` 目录
  - 示例代码移至 `examples/` 目录
  - 清晰的模块划分

### Fixed

- 修复认证相关问题
- 修复交易示例代码

### Documentation

- 完整的 README 文档
- 详细的 API 参考文档
- 多个示例程序：
  - `examples/quote/` - 行情订阅示例
  - `examples/trade/` - 交易操作示例
  - `examples/history/` - 历史数据示例
  - `examples/datamanager/` - DataManager 高级功能示例

## [0.0.1] - Earlier versions

### Note

Earlier versions (prior to v0.1.0) were experimental and are not documented in this changelog.
For historical reference, see the git commit history.

---

## Version Naming Convention

- **Major version** (X.0.0): Incompatible API changes
- **Minor version** (0.X.0): New features in a backwards-compatible manner
- **Patch version** (0.0.X): Backwards-compatible bug fixes

## Links

- [GitHub Repository](https://github.com/pseudocodes/tqsdk-go)
- [Documentation](README.md)
- [Issues](https://github.com/pseudocodes/tqsdk-go/issues)


