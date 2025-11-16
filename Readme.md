# TQSDK-GO

天勤 [DIFF 协议](https://www.shinnytech.com/diff/) 的 Go 语言封装。

提供核心协议 API 数据交互 

`TQSDK-GO` 支持以下功能：

* 查询合约行情
* 查询合约 K线图、Tick图、盘口报价
* 登录期货交易账户
* 查看账户资金、持仓记录、委托单记录

## 安装

```bash
go get -u github.com/pseudocodes/tqsdk-go
```

## 使用

### 初始化

```go
package main

import (
    "fmt"
    tqsdk "github.com/pseudocodes/tqsdk-go"
)

func main() {
    // 使用默认配置
    config := tqsdk.DefaultTQSDKConfig()
    
    // 创建 TQSDK 实例
    sdk := tqsdk.NewTQSDK("your_user_id", "your_password", config)
    defer sdk.Close()
    
    // ... 业务逻辑
}
```

### 自定义配置初始化

```go
// 自定义配置
config := tqsdk.TQSDKConfig{
    SymbolsServerURL: "https://openmd.shinnytech.com/t/md/symbols/latest.json",
    AutoInit:         true,
    LogConfig: tqsdk.LogConfig{
        Level:       "info",
        OutputPath:  "stdout",
        Development: false,
    },
}

sdk := tqsdk.NewTQSDK("your_user_id", "your_password", config)
defer sdk.Close()
```

### 事件监听

```go
// 添加事件监听
sdk.On(eventName, callback)

// 取消事件监听
sdk.Off(eventName, callback)
```

支持的事件：

| 事件名称 | 回调函数参数 | 事件触发说明 |
|---------|-------------|-------------|
| ready | nil | 收到合约基础数据 |
| rtn_brokers | []string 期货公司列表 | 收到期货公司列表 |
| notify | NotifyEvent 通知对象 | 收到通知对象 |
| rtn_data | nil | 数据更新（每一次数据更新触发） |
| error | error | 发生错误 |

注意：监听 `rtn_data` 事件可以实时对行情数据变化作出响应。

## API 参考

### 创建实例

#### NewTQSDK

创建新的 TQSDK 实例。

**函数签名：**
```go
func NewTQSDK(tquser, tqpassword string, config TQSDKConfig) *TQSDK
```

**参数：**
- `tquser` - 天勤账户用户名
- `tqpassword` - 天勤账户密码
- `config` - TQSDK 配置对象

**配置选项：**
- `SymbolsServerURL` - 合约服务地址，默认：`"https://openmd.shinnytech.com/t/md/symbols/latest.json"`
- `AutoInit` - 自动初始化，默认：`true`
- `ClientSystemInfo` - 客户端系统信息（可选）
- `ClientAppID` - 客户端应用ID（可选）
- `LogConfig` - 日志配置
- `WsConfig` - WebSocket 配置

**示例：**
```go
config := tqsdk.DefaultTQSDKConfig()
config.LogConfig.Level = "debug"

sdk := tqsdk.NewTQSDK("username", "password", config)
defer sdk.Close()

sdk.On(tqsdk.EventReady, func(data interface{}) {
    fmt.Println("SDK 已就绪")
    quote := sdk.GetQuote("SHFE.au2512")
    if lastPrice, ok := quote["last_price"].(float64); ok {
        fmt.Printf("最新价: %.2f\n", lastPrice)
    }
})
```

---

### 行情相关

#### GetQuote

根据合约代码获取合约行情对象。

**函数签名：**
```go
func (t *TQSDK) GetQuote(symbol string) map[string]interface{}
```

**参数：**
- `symbol` - 合约代码，格式：`"交易所.合约代码"`，例如：`"SHFE.au2512"`

**返回：**
返回合约行情数据 map，包含以下字段：
- `last_price` - 最新价
- `bid_price1` - 买一价
- `ask_price1` - 卖一价
- `volume` - 成交量
- `open_interest` - 持仓量
- `datetime` - 行情时间
- `pre_settlement` - 昨结算价
- 更多字段参见数据结构文档

**示例：**
```go
sdk.On(tqsdk.EventReady, func(data interface{}) {
    quote := sdk.GetQuote("SHFE.au2512")
    if lastPrice, ok := quote["last_price"].(float64); ok {
        fmt.Printf("最新价: %.2f\n", lastPrice)
    }
})

sdk.On(tqsdk.EventRtnData, func(data interface{}) {
    if sdk.IsChanging([]string{"quotes", "SHFE.au2512"}) {
        quote := sdk.GetQuote("SHFE.au2512")
        fmt.Println("行情更新:", quote["last_price"])
    }
})
```

#### SubscribeQuote

手动订阅合约行情。

**函数签名：**
```go
func (t *TQSDK) SubscribeQuote(quotes []string)
```

**参数：**
- `quotes` - 合约代码列表

**示例：**
```go
sdk.SubscribeQuote([]string{"SHFE.au2512"})
sdk.SubscribeQuote([]string{"SHFE.au2512", "DCE.m2512", "CZCE.CF512"})
```

#### GetQuotesByInput

根据输入字符串查询合约列表。

**函数签名：**
```go
func (t *TQSDK) GetQuotesByInput(input string, filterOption map[string]bool) []string
```

**参数：**
- `input` - 搜索关键词
- `filterOption` - 查询条件，支持以下选项：
  - `symbol` - 是否根据合约ID匹配，默认：`true`
  - `pinyin` - 是否根据拼音匹配，默认：`true`
  - `include_expired` - 匹配结果是否包含已下市合约，默认：`false`
  - `future` - 匹配结果是否包含期货合约，默认：`true`
  - `future_index` - 匹配结果是否包含期货指数，默认：`false`
  - `future_cont` - 匹配结果是否包含期货主连，默认：`false`
  - `option` - 匹配结果是否包含期权，默认：`false`
  - `combine` - 匹配结果是否包含组合，默认：`false`

**返回：**
返回符合条件的合约代码列表。

**示例：**
```go
sdk.On(tqsdk.EventReady, func(data interface{}) {
    // 搜索黄金合约
    filterOption := map[string]bool{
        "future": true,
    }
    results := sdk.GetQuotesByInput("au", filterOption)
    fmt.Printf("找到 %d 个合约\n", len(results))
    
    // 搜索期货指数和主连
    filterOption2 := map[string]bool{
        "future":       false,
        "future_index": true,
        "future_cont":  true,
    }
    results2 := sdk.GetQuotesByInput("au", filterOption2)
    fmt.Println("指数和主连:", results2)
})
```

---

### K线和Tick数据

#### SetChart

请求 K 线图表数据。

**函数签名：**
```go
func (t *TQSDK) SetChart(payload map[string]interface{}) map[string]interface{}
```

**参数：**
- `chart_id` - 图表 ID（可选）
- `symbol` - 合约代码
- `duration` - 图表周期，单位：纳秒。例如：`60 * 1e9` 表示 1 分钟
- `view_width` - 图表柱子宽度（可选）
- `left_kline_id` - 指定 K 线 ID，向右请求 view_width 个数据（可选）
- `trading_day_start` - 指定交易日，返回对应的数据（可选）
- `trading_day_count` - 请求交易日天数（可选）
- `focus_datetime` - 使得指定日期的 K 线位于屏幕第 M 个柱子的位置（可选）
- `focus_position` - 使得指定日期的 K 线位于屏幕第 M 个柱子的位置（可选）

**返回：**
返回图表对象，包含 `left_id`、`right_id`、`more_data` 等字段。

**示例：**
```go
chartPayload := map[string]interface{}{
    "symbol":     "SHFE.au2512",
    "duration":   60 * 1e9, // 1分钟
    "view_width": 100,
}
chart := sdk.SetChart(chartPayload)
fmt.Printf("图表ID: %v\n", chart["chart_id"])

sdk.On(tqsdk.EventRtnData, func(data interface{}) {
    if chart != nil {
        fmt.Println("right_id:", chart["right_id"])
    }
})
```

#### GetKlines

获取 K 线序列数据（原始 map 格式）。

**函数签名：**
```go
func (t *TQSDK) GetKlines(symbol string, duration int64) map[string]interface{}
```

**参数：**
- `symbol` - 合约代码
- `duration` - K 线周期（纳秒）

**返回：**
返回 K 线数据 map，包含 `data`、`last_id` 等字段。

#### GetKlinesData

获取 K 线序列数据（结构化数组格式，推荐使用）。

**函数签名：**
```go
func (t *TQSDK) GetKlinesData(symbol string, duration int64) (*KlineSeriesData, error)
```

**参数：**
- `symbol` - 合约代码
- `duration` - K 线周期（纳秒）

**返回：**
返回结构化的 K 线数据对象，包含：
- `LastID` - 最后一根 K 线的 ID
- `Data` - K 线数组，每个元素包含：
  - `ID` - K 线 ID
  - `Datetime` - K 线时间
  - `Open` - 开盘价
  - `High` - 最高价
  - `Low` - 最低价
  - `Close` - 收盘价
  - `Volume` - 成交量
  - `OpenOI` - 起始持仓量
  - `CloseOI` - 结束持仓量

**示例：**
```go
sdk.On(tqsdk.EventRtnData, func(data interface{}) {
    if sdk.IsChanging([]string{"klines", "SHFE.au2512", "60000000000"}) {
        klinesData, err := sdk.GetKlinesData("SHFE.au2512", 60*1e9)
        if err != nil {
            fmt.Println("获取K线失败:", err)
            return
        }
        if len(klinesData.Data) > 0 {
            lastKline := klinesData.Data[len(klinesData.Data)-1]
            fmt.Printf("最新K线 - 收盘价: %.2f\n", lastKline.Close)
        }
    }
})
```

#### GetTicks

获取 Tick 序列数据（原始 map 格式）。

**函数签名：**
```go
func (t *TQSDK) GetTicks(symbol string) map[string]interface{}
```

**参数：**
- `symbol` - 合约代码

**返回：**
返回 Tick 数据 map，包含 `data`、`last_id` 等字段。

#### GetTicksData

获取 Tick 序列数据（结构化数组格式，推荐使用）。

**函数签名：**
```go
func (t *TQSDK) GetTicksData(symbol string) (*TickSeriesData, error)
```

**参数：**
- `symbol` - 合约代码

**返回：**
返回结构化的 Tick 数据对象，包含：
- `LastID` - 最后一个 Tick 的 ID
- `Data` - Tick 数组，每个元素包含：
  - `ID` - Tick ID
  - `Datetime` - Tick 时间
  - `LastPrice` - 最新价
  - `Average` - 均价
  - `Volume` - 成交量
  - `BidPrice1`、`AskPrice1` - 买一价、卖一价
  - 更多字段参见 Tick 结构体定义

**示例：**
```go
sdk.On(tqsdk.EventRtnData, func(data interface{}) {
    if sdk.IsChanging([]string{"ticks", "SHFE.au2512"}) {
        ticksData, err := sdk.GetTicksData("SHFE.au2512")
        if err != nil {
            fmt.Println("获取Tick失败:", err)
            return
        }
        if len(ticksData.Data) > 0 {
            lastTick := ticksData.Data[len(ticksData.Data)-1]
            fmt.Printf("最新Tick - 价格: %.2f\n", lastTick.LastPrice)
        }
    }
})
```

---

### 交易相关

#### Login

登录期货账户。

**函数签名：**
```go
func (t *TQSDK) Login(bid, userID, password string) error
```

**参数：**
- `bid` - 期货公司名称，例如：`"快期模拟"`
- `userID` - 账户名
- `password` - 密码

**返回：**
返回错误信息，成功返回 nil。

**示例：**
```go
bid := "快期模拟"
userID := "your_account"
password := "your_password"

err := sdk.Login(bid, userID, password)
if err != nil {
    fmt.Println("登录失败:", err)
    return
}

sdk.On(tqsdk.EventRtnData, func(data interface{}) {
    if sdk.IsLogined(bid, userID) {
        fmt.Println("登录成功")
    }
})
```

#### IsLogined

判断账户是否已登录。

**函数签名：**
```go
func (t *TQSDK) IsLogined(bid, userID string) bool
```

**参数：**
- `bid` - 期货公司名称
- `userID` - 账户名

**返回：**
返回 true 表示已登录，false 表示未登录。

#### GetAccount

获取账户资金信息。

**函数签名：**
```go
func (t *TQSDK) GetAccount(bid, userID string) map[string]interface{}
```

**参数：**
- `bid` - 期货公司名称
- `userID` - 账户名

**返回：**
返回账户资金信息 map，包含：
- `balance` - 账户权益
- `available` - 可用资金
- `curr_margin` - 当前保证金
- `risk_ratio` - 风险度
- 更多字段参见 Account 结构体定义

**示例：**
```go
sdk.On(tqsdk.EventRtnData, func(data interface{}) {
    if sdk.IsLogined(bid, userID) {
        account := sdk.GetAccount(bid, userID)
        if balance, ok := account["balance"].(float64); ok {
            fmt.Printf("账户权益: %.2f\n", balance)
        }
        if available, ok := account["available"].(float64); ok {
            fmt.Printf("可用资金: %.2f\n", available)
        }
    }
})
```

#### GetPositions

获取账户全部持仓信息。

**函数签名：**
```go
func (t *TQSDK) GetPositions(bid, userID string) map[string]interface{}
```

**参数：**
- `bid` - 期货公司名称
- `userID` - 账户名

**返回：**
返回持仓信息 map，key 为合约代码，value 为持仓详情。

**示例：**
```go
positions := sdk.GetPositions(bid, userID)
for symbol, posData := range positions {
    if posMap, ok := posData.(map[string]interface{}); ok {
        fmt.Printf("合约: %s\n", symbol)
        if volumeLong, ok := posMap["volume_long_today"].(float64); ok {
            fmt.Printf("  今多: %.0f\n", volumeLong)
        }
        if floatProfit, ok := posMap["float_profit"].(float64); ok {
            fmt.Printf("  浮动盈亏: %.2f\n", floatProfit)
        }
    }
}
```

#### GetPosition

获取指定合约的持仓信息。

**函数签名：**
```go
func (t *TQSDK) GetPosition(bid, userID, symbol string) map[string]interface{}
```

**参数：**
- `bid` - 期货公司名称
- `userID` - 账户名
- `symbol` - 合约代码

**返回：**
返回该合约的持仓信息。

**示例：**
```go
pos := sdk.GetPosition(bid, userID, "SHFE.au2512")
if pos != nil {
    if volumeLong, ok := pos["volume_long_today"].(float64); ok {
        fmt.Printf("多头持仓: %.0f\n", volumeLong)
    }
}
```

#### InsertOrder

下单。

**函数签名：**
```go
func (t *TQSDK) InsertOrder(bid, userID, exchangeID, instrumentID, direction, offset, priceType string, limitPrice float64, volume int64) (map[string]interface{}, error)
```

**参数：**
- `bid` - 期货公司名称
- `userID` - 账户名
- `exchangeID` - 交易所代码，例如：`"SHFE"`
- `instrumentID` - 合约代码，例如：`"au2512"`
- `direction` - 方向，`"BUY"` 或 `"SELL"`
- `offset` - 开平标志，`"OPEN"`、`"CLOSE"` 或 `"CLOSETODAY"`
- `priceType` - 价格类型，`"LIMIT"` 限价单或 `"ANY"` 市价单
- `limitPrice` - 委托价格
- `volume` - 委托手数

**返回：**
返回委托单信息和错误信息。

**示例：**
```go
order, err := sdk.InsertOrder(
    bid, userID,
    "SHFE", "au2512",
    "BUY", "OPEN", "LIMIT",
    500.00, 1,
)
if err != nil {
    fmt.Println("下单失败:", err)
} else {
    fmt.Printf("下单成功，订单ID: %v\n", order["order_id"])
}
```

#### CancelOrder

撤销委托单。

**函数签名：**
```go
func (t *TQSDK) CancelOrder(bid, userID, orderID string) error
```

**参数：**
- `bid` - 期货公司名称
- `userID` - 账户名
- `orderID` - 委托单 ID

**返回：**
返回错误信息，成功返回 nil。

**示例：**
```go
err := sdk.CancelOrder(bid, userID, "order_id_here")
if err != nil {
    fmt.Println("撤单失败:", err)
} else {
    fmt.Println("撤单成功")
}
```

#### GetOrders

获取账户全部委托单信息。

**函数签名：**
```go
func (t *TQSDK) GetOrders(bid, userID string) map[string]interface{}
```

**参数：**
- `bid` - 期货公司名称
- `userID` - 账户名

**返回：**
返回委托单信息 map，key 为委托单 ID，value 为委托单详情。

**示例：**
```go
orders := sdk.GetOrders(bid, userID)
for orderID, orderData := range orders {
    if orderMap, ok := orderData.(map[string]interface{}); ok {
        if status, ok := orderMap["status"].(string); ok && status == "ALIVE" {
            fmt.Printf("活动委托单: %s\n", orderID)
        }
    }
}
```

#### GetOrder

获取指定委托单信息。

**函数签名：**
```go
func (t *TQSDK) GetOrder(bid, userID, orderID string) map[string]interface{}
```

#### GetOrdersBySymbol

获取指定合约的所有委托单。

**函数签名：**
```go
func (t *TQSDK) GetOrdersBySymbol(bid, userID, symbol string) map[string]interface{}
```

#### GetTrades

获取账户全部成交记录。

**函数签名：**
```go
func (t *TQSDK) GetTrades(bid, userID string) map[string]interface{}
```

#### GetTrade

获取指定成交记录。

**函数签名：**
```go
func (t *TQSDK) GetTrade(bid, userID, tradeID string) map[string]interface{}
```

#### GetTradesByOrder

获取指定委托单的所有成交记录。

**函数签名：**
```go
func (t *TQSDK) GetTradesByOrder(bid, userID, orderID string) map[string]interface{}
```

#### GetTradesBySymbol

获取指定合约的所有成交记录。

**函数签名：**
```go
func (t *TQSDK) GetTradesBySymbol(bid, userID, symbol string) map[string]interface{}
```

#### ConfirmSettlement

确认结算单。每个交易日需要确认一次结算单后才能进行交易。

**函数签名：**
```go
func (t *TQSDK) ConfirmSettlement(bid, userID string) error
```

**参数：**
- `bid` - 期货公司名称
- `userID` - 账户名

**示例：**
```go
sdk.On(tqsdk.EventRtnData, func(data interface{}) {
    if sdk.IsLogined(bid, userID) {
        err := sdk.ConfirmSettlement(bid, userID)
        if err != nil {
            fmt.Println("确认结算单失败:", err)
        } else {
            fmt.Println("已确认结算单")
        }
    }
})
```

#### GetHisSettlements

获取历史结算单列表。

**函数签名：**
```go
func (t *TQSDK) GetHisSettlements(bid, userID string) map[string]interface{}
```

#### GetHisSettlement

获取指定日期的历史结算单。

**函数签名：**
```go
func (t *TQSDK) GetHisSettlement(bid, userID, tradingDay string) map[string]interface{}
```

**参数：**
- `bid` - 期货公司名称
- `userID` - 账户名
- `tradingDay` - 交易日期，格式：`"20231201"`

#### QueryHisSettlement

查询历史结算单。

**函数签名：**
```go
func (t *TQSDK) QueryHisSettlement(bid, userID, tradingDay string) error
```

---

### 工具方法

#### IsChanging

判断某个数据路径是否在最近一次更新中有变动。

**函数签名：**
```go
func (t *TQSDK) IsChanging(pathArray []string) bool
```

**参数：**
- `pathArray` - 数据路径数组，例如：`[]string{"quotes", "SHFE.au2512"}`

**返回：**
返回 true 表示有变动，false 表示无变动。

**示例：**
```go
sdk.On(tqsdk.EventRtnData, func(data interface{}) {
    if sdk.IsChanging([]string{"quotes", "SHFE.au2512"}) {
        quote := sdk.GetQuote("SHFE.au2512")
        fmt.Println("行情更新:", quote["last_price"])
    }
    
    if sdk.IsChanging([]string{"trade", userID, "accounts", "CNY"}) {
        account := sdk.GetAccount(bid, userID)
        fmt.Println("账户资金更新:", account["balance"])
    }
})
```

#### GetByPath

根据路径数组获取数据。

**函数签名：**
```go
func (t *TQSDK) GetByPath(pathArray []string) interface{}
```

**参数：**
- `pathArray` - 数据路径数组

**返回：**
返回路径对应的数据对象。

**示例：**
```go
// 获取某个合约的最新价
lastPrice := sdk.GetByPath([]string{"quotes", "SHFE.au2512", "last_price"})

// 推荐使用 GetQuote 方法
quote := sdk.GetQuote("SHFE.au2512")
lastPrice := quote["last_price"]
```

---

## 完整示例

### 行情查询示例

```go
package main

import (
    "fmt"
    "os"
    tqsdk "github.com/pseudocodes/tqsdk-go"
)

func main() {
    // 创建配置
    config := tqsdk.DefaultTQSDKConfig()
    config.LogConfig.Level = "info"
    config.AutoInit = true

    // 从环境变量获取账号信息
    userID := os.Getenv("SHINNYTECH_ID")
    password := os.Getenv("SHINNYTECH_PW")

    // 创建 TQSDK 实例
    sdk := tqsdk.NewTQSDK(userID, password, config)
    defer sdk.Close()

    fmt.Println("TQSDK Go 示例 - 行情查询")

    // 注册 ready 事件
    sdk.On(tqsdk.EventReady, func(data interface{}) {
        fmt.Println("SDK 已就绪，合约信息已加载")

        // 获取合约行情
        symbol := "SHFE.au2512"
        quote := sdk.GetQuote(symbol)

        fmt.Printf("\n【合约信息】%s\n", symbol)
        if productName, ok := quote["product_short_name"].(string); ok {
            fmt.Printf("  品种名称: %s\n", productName)
        }
        if volumeMultiple, ok := quote["volume_multiple"].(float64); ok {
            fmt.Printf("  合约乘数: %.0f\n", volumeMultiple)
        }
        if priceTick, ok := quote["price_tick"].(float64); ok {
            fmt.Printf("  最小变动价位: %.2f\n", priceTick)
        }

        fmt.Printf("\n【实时行情】\n")
        if lastPrice, ok := quote["last_price"].(float64); ok {
            fmt.Printf("  最新价: %.2f\n", lastPrice)
        }
        if bidPrice1, ok := quote["bid_price1"].(float64); ok {
            fmt.Printf("  买一价: %.2f\n", bidPrice1)
        }
        if askPrice1, ok := quote["ask_price1"].(float64); ok {
            fmt.Printf("  卖一价: %.2f\n", askPrice1)
        }

        // 订阅多个合约
        symbols := []string{"SHFE.au2512", "DCE.m2512", "CZCE.CF512"}
        sdk.SubscribeQuote(symbols)
        fmt.Printf("\n已订阅合约: %v\n", symbols)

        // 请求 K 线数据
        chartPayload := map[string]interface{}{
            "symbol":     "SHFE.au2512",
            "duration":   60 * 1e9, // 1分钟
            "view_width": 100,
        }
        chart := sdk.SetChart(chartPayload)
        fmt.Printf("\n已请求 K 线图表，chart_id: %v\n", chart["chart_id"])
    })

    // 注册数据更新事件
    sdk.On(tqsdk.EventRtnData, func(data interface{}) {
        // 检查特定合约是否有更新
        if sdk.IsChanging([]string{"quotes", "SHFE.au2512"}) {
            quote := sdk.GetQuote("SHFE.au2512")
            if lastPrice, ok := quote["last_price"].(float64); ok {
                if datetime, ok := quote["datetime"].(string); ok {
                    fmt.Printf("[%s] SHFE.au2512 更新 - 最新价: %.2f\n", datetime, lastPrice)
                }
            }
        }

        // 检查 K 线更新
        if sdk.IsChanging([]string{"klines", "SHFE.au2512", "60000000000"}) {
            klinesData, err := sdk.GetKlinesData("SHFE.au2512", 60*1e9)
            if err != nil {
                fmt.Printf("获取 K 线数据失败: %v\n", err)
                return
            }
            if len(klinesData.Data) > 0 {
                lastKline := klinesData.Data[len(klinesData.Data)-1]
                fmt.Printf("K线更新 - 收盘价: %.2f\n", lastKline.Close)
            }
        }
    })

    // 注册错误事件
    sdk.On(tqsdk.EventError, func(data interface{}) {
        if err, ok := data.(error); ok {
            fmt.Printf("错误: %v\n", err)
        }
    })

    // 保持程序运行
    fmt.Println("\n程序运行中，按 Ctrl+C 退出...")
    select {}
}
```

### 交易操作示例

```go
package main

import (
    "fmt"
    "os"
    "time"
    tqsdk "github.com/pseudocodes/tqsdk-go"
)

func main() {
    // 创建配置
    config := tqsdk.DefaultTQSDKConfig()
    config.LogConfig.Level = "info"
    config.AutoInit = true

    // 从环境变量获取账号信息
    userID := os.Getenv("SHINNYTECH_ID")
    password := os.Getenv("SHINNYTECH_PW")

    // 创建 TQSDK 实例
    sdk := tqsdk.NewTQSDK(userID, password, config)
    defer sdk.Close()

    fmt.Println("TQSDK Go 示例 - 交易操作")

    // 账户信息
    bid := "快期模拟"

    // 注册数据更新事件
    loginChecked := false
    sdk.On(tqsdk.EventRtnData, func(data interface{}) {
        if loginChecked {
            return
        }

        // 检查是否登录成功
        if sdk.IsLogined(bid, userID) {
            loginChecked = true
            fmt.Println("登录成功！")

            // 确认结算单
            err := sdk.ConfirmSettlement(bid, userID)
            if err != nil {
                fmt.Printf("确认结算单失败: %v\n", err)
            } else {
                fmt.Println("已确认结算单")
            }

            // 等待一下让数据同步
            time.Sleep(2 * time.Second)

            // 查询账户信息
            fmt.Println("\n【账户信息】")
            account := sdk.GetAccount(bid, userID)
            if account != nil {
                if balance, ok := account["balance"].(float64); ok {
                    fmt.Printf("  账户权益: %.2f\n", balance)
                }
                if available, ok := account["available"].(float64); ok {
                    fmt.Printf("  可用资金: %.2f\n", available)
                }
                if currMargin, ok := account["curr_margin"].(float64); ok {
                    fmt.Printf("  当前保证金: %.2f\n", currMargin)
                }
                if riskRatio, ok := account["risk_ratio"].(float64); ok {
                    fmt.Printf("  风险度: %.2f%%\n", riskRatio*100)
                }
            }

            // 查询持仓
            fmt.Println("\n【持仓信息】")
            positions := sdk.GetPositions(bid, userID)
            if len(positions) > 0 {
                for symbol, posData := range positions {
                    if posMap, ok := posData.(map[string]interface{}); ok {
                        fmt.Printf("\n  合约: %s\n", symbol)
                        if volumeLong, ok := posMap["volume_long_today"].(float64); ok {
                            fmt.Printf("    今多: %.0f\n", volumeLong)
                        }
                        if volumeShort, ok := posMap["volume_short_today"].(float64); ok {
                            fmt.Printf("    今空: %.0f\n", volumeShort)
                        }
                        if floatProfit, ok := posMap["float_profit"].(float64); ok {
                            fmt.Printf("    浮动盈亏: %.2f\n", floatProfit)
                        }
                    }
                }
            } else {
                fmt.Println("  无持仓")
            }

            // 查询委托单
            fmt.Println("\n【委托单】")
            orders := sdk.GetOrders(bid, userID)
            activeCount := 0
            for orderID, orderData := range orders {
                if orderMap, ok := orderData.(map[string]interface{}); ok {
                    if status, ok := orderMap["status"].(string); ok && status == "ALIVE" {
                        activeCount++
                        fmt.Printf("\n  订单ID: %s\n", orderID)
                        if symbol, ok := orderMap["exchange_id"].(string); ok {
                            if inst, ok := orderMap["instrument_id"].(string); ok {
                                fmt.Printf("    合约: %s.%s\n", symbol, inst)
                            }
                        }
                        if direction, ok := orderMap["direction"].(string); ok {
                            fmt.Printf("    方向: %s\n", direction)
                        }
                        if limitPrice, ok := orderMap["limit_price"].(float64); ok {
                            fmt.Printf("    价格: %.2f\n", limitPrice)
                        }
                        if volumeLeft, ok := orderMap["volume_left"].(float64); ok {
                            fmt.Printf("    剩余手数: %.0f\n", volumeLeft)
                        }
                    }
                }
            }
            if activeCount == 0 {
                fmt.Println("  无活动委托单")
            }

            fmt.Println("\n交易示例完成")
        }
    })

    // 注册通知事件
    sdk.On(tqsdk.EventNotify, func(data interface{}) {
        if notify, ok := data.(tqsdk.NotifyEvent); ok {
            fmt.Printf("\n[通知] %s: %s\n", notify.Level, notify.Content)
        }
    })

    // 注册错误事件
    sdk.On(tqsdk.EventError, func(data interface{}) {
        if err, ok := data.(error); ok {
            fmt.Printf("\n错误: %v\n", err)
        }
    })

    // 登录账户
    fmt.Printf("\n正在登录账户 %s/%s ...\n", bid, userID)
    err := sdk.Login(bid, userID, password)
    if err != nil {
        fmt.Printf("登录失败: %v\n", err)
        return
    }

    // 保持程序运行
    fmt.Println("\n程序运行中，按 Ctrl+C 退出...")
    time.Sleep(30 * time.Second)
    fmt.Println("\n程序即将退出...")
}
```

## 数据结构

### Quote - 行情报价

```go
type Quote struct {
    InstrumentID     string  // 合约代码
    Datetime         string  // 行情时间
    LastPrice        float64 // 最新价
    AskPrice1        float64 // 卖一价
    AskVolume1       int64   // 卖一量
    BidPrice1        float64 // 买一价
    BidVolume1       int64   // 买一量
    Highest          float64 // 最高价
    Lowest           float64 // 最低价
    Open             float64 // 开盘价
    Close            float64 // 收盘价
    Volume           int64   // 成交量
    OpenInterest     int64   // 持仓量
    LowerLimit       float64 // 跌停价
    UpperLimit       float64 // 涨停价
    Settlement       float64 // 结算价
    PreSettlement    float64 // 昨结算价
    VolumeMultiple   int     // 合约乘数
    PriceTick        float64 // 最小变动价位
    // ... 更多字段
}
```

### Kline - K线数据

```go
type Kline struct {
    ID       int64   // K线ID
    Datetime int64   // K线起点时间(纳秒)
    Open     float64 // 开盘价
    Close    float64 // 收盘价
    High     float64 // 最高价
    Low      float64 // 最低价
    OpenOI   int64   // 起始持仓量
    CloseOI  int64   // 结束持仓量
    Volume   int64   // 成交量
}
```

### Tick - Tick数据

```go
type Tick struct {
    ID           int64   // Tick ID
    Datetime     int64   // tick时间(纳秒)
    LastPrice    float64 // 最新价
    Average      float64 // 均价
    Volume       int64   // 成交量
    OpenInterest int64   // 持仓量
    // ... 盘口数据
}
```

## 事件常量

```go
const (
    EventReady      = "ready"       // SDK就绪事件
    EventRtnData    = "rtn_data"    // 数据更新事件
    EventRtnBrokers = "rtn_brokers" // 期货公司列表事件
    EventNotify     = "notify"      // 通知事件
    EventError      = "error"       // 错误事件
)
```

## 许可证

Apache License 2.0

## 相关链接

- [天勤量化官网](https://www.shinnytech.com/)
- [DIFF 协议文档](https://www.shinnytech.com/diff/)
- [GitHub 仓库](https://github.com/pseudocodes/tqsdk-go)

## 免责声明
本项目明确拒绝对产品做任何明示或暗示的担保。由于软件系统开发本身的复杂性，无法保证本项目完全没有错误。如选择使用本项目表示您同意错误和/或遗漏的存在，在任何情况下本项目对于直接、间接、特殊的、偶然的、或间接产生的、使用或无法使用本项目进行交易和投资造成的盈亏、直接或间接引起的赔偿、损失、债务或是任何交易中止均不承担责任和义务。

