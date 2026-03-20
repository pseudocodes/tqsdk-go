# TQSDK-GO

天勤量化 [DIFF 协议](https://www.shinnytech.com/diff/) 的 Go 语言 SDK，提供期货/期权行情订阅、交易操作、历史数据查询、本地回测等功能。

本项目是 [tqsdk-python](https://github.com/shinnytech/tqsdk-python) 的 Go 语言非官方实现，基于天勤 DIFF 协议与 shinnytech 后端服务通信。

## 版本说明

项目包含两个 API 版本，均可使用：

| 版本     | 包路径            | 状态     | 说明                                    |
| -------- | ----------------- | -------- | --------------------------------------- |
| v1alpha1 | `shinny/v1alpha1` | 稳定     | 基于 DataManager 的回调/Channel 模型    |
| v1alpha2 | `shinny/v1alpha2` | 开发维护 | 分层架构，支持回测、SyncApi、WebAdapter |

v1alpha2 是推荐的新开发方向，提供更完整的功能集和更清晰的架构设计。

## 安装

```bash
go get github.com/pseudocodes/tqsdk-go@latest
```

要求 Go 1.21+。

## 环境变量

所有示例和客户端初始化均通过环境变量传入天勤账号：

```bash
export SHINNYTECH_ID="your_username"
export SHINNYTECH_PW="your_password"
```

## 快速开始 (v1alpha2)

### 行情订阅

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/app"
)

func main() {
	cli, err := app.NewDefaultClient(app.Config{
		User:     os.Getenv("SHINNYTECH_ID"),
		Password: os.Getenv("SHINNYTECH_PW"),
	})
	if err != nil {
		log.Fatal(err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := cli.Run(ctx); err != nil {
		log.Fatal(err)
	}

	// 订阅实时行情
	quoteSub, err := cli.Market().SubscribeQuotes(ctx, "SHFE.rb2510", "SHFE.ag2512")
	if err != nil {
		log.Fatal(err)
	}
	defer quoteSub.Close()

	for ev := range quoteSub.C() {
		fmt.Printf("%s last=%.1f bid1=%.1f ask1=%.1f\n",
			ev.Quote.InstrumentID, ev.Quote.LastPrice,
			ev.Quote.BidPrice1, ev.Quote.AskPrice1)
	}
}
```

### SyncApi 模式 (对标 Python tqsdk)

```go
// 对标 Python:
//   api = TqApi(auth=TqAuth(user, password))
//   quote = api.get_quote("SHFE.rb2510")
//   while True:
//       api.wait_update()
//       if api.is_changing(quote): print(quote.last_price)

sa := syncapi.NewFromClient(cli)
defer sa.Close()

quote, _ := sa.GetQuote(ctx, "SHFE.rb2510")
klines, _ := sa.GetKlineSeries(ctx, "SHFE.rb2510", 60, 200)

for {
	if err := sa.WaitUpdateContext(ctx); err != nil {
		break
	}
	if sa.IsChanging(quote) {
		q := quote.Get()
		fmt.Printf("last=%.1f\n", q.LastPrice)
	}
	if sa.IsChanging(klines.Bar(-1), "datetime") {
		fmt.Println("new bar!")
	}
}
```

### 本地回测

```go
btCfg := backtest.Config{
	StartDT:        time.Date(2018, 5, 1, 0, 0, 0, 0, time.Local),
	EndDT:          time.Date(2018, 10, 1, 0, 0, 0, 0, time.Local),
	InitialBalance: 10_000_000,
}
appCfg := app.Config{User: user, Password: password}

wiring, err := app.NewBacktestWiring(appCfg, btCfg,
	app.WithBacktestSkipTicks(),
	app.WithBacktestAutoRun(false),
)
// wiring.Market — 模拟行情服务
// wiring.Trade  — 模拟交易服务
// wiring.Runtime.Run(ctx) — 启动事件内核
// wiring.Collector.Finalize() — 获取回测报告
```

## v1alpha2 架构

```
shinny/v1alpha2/
├── api/          接口定义层 (Client, MarketService, TradeService, AuthService)
├── domain/       业务实现层 (认证、行情、交易、风控)
├── infra/        基础设施层 (WebSocket 管理、JSON 编解码、GraphQL 构建器)
├── store/        数据存储层 (行情存储、交易存储)
├── app/          应用组装层 (NewDefaultClient, NewBacktestWiring)
├── backtest/     回测引擎
│   ├── runtime/    事件驱动内核 (EventKernel)
│   ├── data/       历史数据下载与缓存
│   ├── market/     模拟行情服务
│   ├── trade/      模拟交易撮合与结算
│   └── report/     报告生成 (收益率、夏普率、最大回撤等)
├── syncapi/      同步编程模型 (WaitUpdate/IsChanging)
└── webadapter/   浏览器可视化 (WebSocket 网关、指标叠加层)
```

### 核心接口

- `api.Client` — 统一入口，组合 Auth + Market + Trade 三个服务
- `api.MarketService` — 行情服务：Quote/Tick/Kline 订阅、历史数据查询与下载、GraphQL 查询
- `api.TradeService` — 交易服务：登录、下单、撤单、账户/持仓/订单查询
- `api.TradeSession` — 交易会话：单账户的完整交易操作与事件订阅
- `app.NewDefaultClient(cfg)` — 一键构建完整的实盘客户端
- `app.NewBacktestWiring(cfg, btCfg)` — 一键构建回测环境

## 示例程序

### v1alpha2 示例

| 示例                                                      | 说明                                           |
| --------------------------------------------------------- | ---------------------------------------------- |
| [backtest_demo](examples/v1alpha2/backtest_demo/)         | MA 均线交叉回测策略，含完整报告输出            |
| [indicator_demo](examples/v1alpha2/indicator_demo/)       | 回测 + 自定义 MA 指标 Web 可视化               |
| [market_demo](examples/v1alpha2/market_demo/)             | 行情订阅、Tick/K线下载器                       |
| [market_query_demo](examples/v1alpha2/market_query_demo/) | GraphQL 查询、合约信息、期权链、结算价、排名等 |
| [syncapi_demo](examples/v1alpha2/syncapi_demo/)           | 同步编程模型 (wait_update/is_changing)         |
| [webadapter_demo](examples/v1alpha2/webadapter_demo/)     | 回测 + tqsdk Web UI 浏览器可视化               |

运行示例：

```bash
export SHINNYTECH_ID="your_username"
export SHINNYTECH_PW="your_password"

go run ./examples/v1alpha2/syncapi_demo
go run ./examples/v1alpha2/market_query_demo
go run ./examples/v1alpha2/backtest_demo
```




## 相关链接

- [天勤量化官网](https://www.shinnytech.com/)
- [DIFF 协议文档](https://www.shinnytech.com/diff/)
- [tqsdk-python](https://github.com/shinnytech/tqsdk-python) — Python 官方 SDK
- [更新日志](CHANGELOG.md)


## 免责声明

本项目为非官方的第三方开源实现，与天勤量化 (shinnytech) 无直接关联。

本项目按"原样"提供，不做任何明示或暗示的担保，包括但不限于适销性、特定用途适用性的担保。由于软件系统开发本身的复杂性，无法保证本项目完全没有错误。

在任何情况下，本项目的作者和贡献者均不对因使用或无法使用本项目而导致的任何直接、间接、偶然、特殊或后果性损害承担责任，包括但不限于交易损失、数据丢失、业务中断等。

使用本项目进行实盘交易的风险由用户自行承担。建议在生产环境使用前进行充分的测试和验证。

## 致谢

感谢 [天勤量化](https://www.shinnytech.com/) 提供的 DIFF 协议和后端服务。
