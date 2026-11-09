# TQSDK-GO

天勤（Shinny）行情与交易协议的 Go SDK，提供行情订阅、历史数据查询、账户交易、本地模拟交易、回测和目标持仓任务。本项目是第三方实现，与天勤官方 Python SDK 的 API 并不完全相同。

本文以本仓库 `shinny/v1alpha3` 的代码为准，完整可运行程序见 [examples/v1alpha3](examples/v1alpha3)。

## 版本与运行要求

| 版本 | 导入路径前缀 | 编程入口 |
| --- | --- | --- |
| v1alpha3 | `github.com/pseudocodes/tqsdk-go/shinny/v1alpha3` | `market`、`trade`、`tqapi`；本文介绍的版本 |
| v1alpha2 | `github.com/pseudocodes/tqsdk-go/shinny/v1alpha2` | `app`、`syncapi`、`webadapter` 等旧接口 |
| v1alpha1 | `github.com/pseudocodes/tqsdk-go/shinny/v1alpha1` | DataManager、回调与 Channel 接口 |

v1alpha3 是独立的 alpha API，与旧版不是直接替换关系；迁移时需要调整初始化、订阅、账户与策略循环。

- 当前模块要求 **Go 1.27 或更新版本**。v1alpha3 使用 `encoding/json/v2` 和 `encoding/json/jsontext`，不支持 `GOEXPERIMENT=nojsonv2`。
- 三个版本共用根目录 `go.mod`，因此 Go 版本要求作用于整个模块。
- 网络行情、历史行情和交易接入需要对应服务的账户与权限。离线回测示例无需账户或网络服务。

在仓库根目录验证并运行离线示例：

```sh
go version
go test -race ./shinny/v1alpha3/... ./examples/v1alpha3/...
go run ./examples/v1alpha3/backtest
```

在自己的 Go 模块中，选择包含 `shinny/v1alpha3` 的版本或提交：

```sh
go get github.com/pseudocodes/tqsdk-go@<版本或提交>
```

使用尚未发布的本地代码时，在使用方模块中配置本地替换：

```sh
go mod edit -replace=github.com/pseudocodes/tqsdk-go=/本机路径/tqsdk-go
go get github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/tqapi
```

不要假定远端 `@latest` 已包含当前工作区的 v1alpha3。

## 选择使用方式

| 场景 | 入口 | 状态推进方式 |
| --- | --- | --- |
| 行情订阅、历史分页、合约查询 | `market.Dial` | 后台自动推进；订阅通过 Channel 交付，查询阻塞等待结果 |
| 独立交易服务 | `trade.Dial` → `Session` | 后台自动推进；通过 `Events`、`RunEvents` 或 `Serve` 消费事件 |
| 策略、目标持仓、模拟交易、回测 | `tqapi.New` | 调用 `WaitUpdate` 推进，通过 `Pulse` / `IsChanging` 判断变化 |
| 自定义服务组合 | `core.Runtime` | 自行管理 `Run` 或 `Step` 与组件生命周期 |

`tqapi` 直接组合 `core`。独立的 `market` / `trade` 客户端分别拥有自己的 Runtime；它们不是同一个 `tqapi` 实例的共享状态入口。

## 快速开始：策略循环

所有 v1alpha3 在线示例仅从以下环境变量读取天勤凭证，统一通过 `auth.NewClient` 登录：

```sh
export TQ_AUTH_USER="your_username"
export TQ_AUTH_PASS="your_password"
go run ./examples/v1alpha3/strategy -symbol SHFE.rb2701
```

`TQ_AUTH_USER` 是天勤用户名，`TQ_AUTH_PASS` 是天勤密码；缺失或为空时，在线接入会报错。支持 `-url` 的示例仅覆盖行情地址，仍然使用这两个环境变量认证。离线回测无需设置凭证。

将合约代码替换为需要订阅且服务支持的合约。下面是一个只读取行情的完整程序：

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/auth"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/tqapi"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	api, err := tqapi.New(ctx, tqapi.Config{
		Auth:   auth.NewClient(os.Getenv("TQ_AUTH_USER"), os.Getenv("TQ_AUTH_PASS")),
		Market: &core.MarketConfig{},
	})
	if err != nil {
		return err
	}
	defer api.Close()
	quote, err := api.Market.Quote(ctx, "SHFE.rb2701")
	if err != nil {
		return err
	}
	bars, err := api.Market.Klines(ctx, core.KlineRequest{
		Symbol: "SHFE.rb2701", Period: time.Minute, Width: 200,
	})
	if err != nil {
		return err
	}
	for {
		pulse, err := api.WaitUpdate(ctx)
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil
		}
		if err != nil {
			return err
		}
		if pulse.IsChanging(quote, "last_price") {
			s := quote.Snapshot()
			if s.State == core.Ready && s.Valid.Has(model.QuoteLastPrice) {
				fmt.Println("最新价:", s.Value.LastPrice)
			}
		}
		if pulse.IsChanging(bars.LatestRow(), "datetime") {
			fmt.Println("首个真实末行或新 K 线，窗口行数:", len(bars.Snapshot().Rows))
		}
	}
}
```

`New`、`Quote`、`Klines` 返回时不保证业务数据就绪，初始数据由后续 `WaitUpdate` 交付。读取时检查 `State`、行的 `Present` 和字段 `Valid`；不要用数值零来表示字段缺失。

`IsChanging` 判断本轮提交后的净变化，字段名使用 `last_price`、`datetime` 等协议名称，多个字段按 OR 判断。`api.IsChanging(...)` 等价于最近一次 `Pulse.IsChanging(...)`。`LatestRow()` 的 `datetime` 首次有效时也会触发；策略若只处理新柱，需要区分初始基线。

窗口 `Ready` 不等于已经填满请求的行数；可通过 `Window.FrameFull`、`Window.ValidCount` 判断。读取历史行时逐行检查 `Present`，计算指标时再检查所需字段的有效位。

## 独立行情与历史查询

除上面的完整程序外，下文代码块为可复用函数或函数内片段；按代码使用情况导入标准库和 `github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/<包名>`。参数中的 `ctx` 应设置取消或超时，`md` 表示已连接的 `*market.Market`，`api` 表示已创建的 `*tqapi.API`。

以下片段放在返回 `error` 的函数中，创建后台推进的客户端并订阅 K 线：

```go
provider := auth.NewClient(os.Getenv("TQ_AUTH_USER"), os.Getenv("TQ_AUTH_PASS"))
md, err := market.Dial(ctx, market.Config{Auth: provider})
if err != nil {
	return err
}
defer md.Close()
sub, err := md.SubscribeKlines(ctx, market.KlineRequest{
	Symbol: "SHFE.rb2701", Period: time.Minute, Width: 200,
})
if err != nil {
	return err
}
defer sub.Close()
for {
	select {
	case event, ok := <-sub.C():
		if !ok {
			return sub.Err()
		}
		if event.Frame != nil {
			fmt.Printf("完整窗口 %d 行，本次增量 %d 行\n", len(event.Frame.Rows), len(event.Rows))
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}
```

### 实时报价

```go
func watchQuotes(ctx context.Context, md *market.Market) error {
	sub, err := md.SubscribeQuotes(ctx, "SHFE.rb2701", "SHFE.hc2701")
	if err != nil {
		return err
	}
	defer sub.Close()
	for {
		select {
		case event, ok := <-sub.C():
			if !ok {
				return sub.Err()
			}
			if event.State == core.Ready && event.Valid.Has(model.QuoteLastPrice) {
				fmt.Printf("%s 最新价 %.2f\n", event.Quote.Symbol, event.Quote.LastPrice)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
```

### Tick 与盘口

```go
func watchTicks(ctx context.Context, md *market.Market) error {
	sub, err := md.SubscribeTicks(ctx, market.TickRequest{
		Symbol: "SHFE.rb2701", Width: 5,
	}, market.WithoutFrame())
	if err != nil {
		return err
	}
	defer sub.Close()
	for {
		select {
		case event, ok := <-sub.C():
			if !ok {
				return sub.Err()
			}
			if event.State != core.Ready {
				continue
			}
			for _, row := range event.Rows {
				if row.Present && row.Valid.Has(model.TickBidPrice1) && row.Valid.Has(model.TickAskPrice1) {
					fmt.Printf("%s bid=%.2f ask=%.2f\n",
						time.Unix(0, row.Value.Datetime), row.Value.BidPrice1, row.Value.AskPrice1)
				}
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
```

### 多合约对齐 K 线

下面使用 `tqapi` 的窗口句柄。独立行情客户端的对应接口是 `md.SubscribeAlignedKlines`，通过订阅 Channel 获取事件。

```go
func watchSpread(ctx context.Context, api *tqapi.API) error {
	bars, err := api.Market.AlignedKlines(ctx, core.AlignedKlineRequest{
		Symbols: []string{"SHFE.rb2701", "SHFE.hc2701"},
		Period: time.Minute, Width: 20, Mode: core.MatchedRows,
	})
	if err != nil {
		return err
	}
	defer bars.Close()
	for {
		pulse, err := api.WaitUpdate(ctx)
		if err := errors.Join(pulse.TaskError(), err); err != nil {
			return err
		}
		if !pulse.IsChanging(bars) {
			continue
		}
		frame := bars.Snapshot()
		if frame.State != core.Ready {
			continue
		}
		for _, row := range frame.Rows {
			a, b := row.Legs[0], row.Legs[1]
			if a.Present && b.Present && a.Valid.Has(model.KlineClose) && b.Valid.Has(model.KlineClose) {
				fmt.Printf("主轴 ID=%d 收盘价差=%.2f\n", row.PrimaryID, a.Value.Close-b.Value.Close)
			}
		}
	}
}
```

K 线和 Tick 序列订阅默认附带完整窗口 `event.Frame.Rows`；`event.Rows` 在首次或替换基线时为整窗，其余为本批相关行（可能包括刚离开窗口的旧柱最终值）；未就绪等状态事件的 `Frame` 为 nil。只需增量时传入 `market.WithoutFrame()`。

| API | 用途 |
| --- | --- |
| `SubscribeQuotes` / `SubscribeTicks` / `SubscribeKlines` | 实时报价、Tick 和单合约 K 线 |
| `SubscribeAlignedKlines` | 多合约 K 线对齐；`PrimarySlots` 保留主轴空槽，`MatchedRows` 只保留所有合约都有数据的行 |
| `QueryQuotes` / `QuerySymbolInfo` / `QueryOptions` | 合约代码、合约资料、期权代码查询 |
| `QueryGraphQL` | 通过合约服务执行 GraphQL 查询 |
| `QueryKlinesPage` / `QueryTicksPage` / `QueryAlignedKlinesPage` | 历史分页查询 |

窗口通过 `core.WindowPosition` 选择 `Latest`、`ByTime` 或 `ByID`。K 线周期使用 `time.Duration`，必须为正整秒，超过一天时为整日倍数，最大 28 天；单次窗口宽度为 1–10000。

`tqapi.Market` 提供对应的 `Request*` 查询接口。请求发出后必须继续调用 `WaitUpdate`，再检查请求结果；不要在唯一推进循环中阻塞等待一个尚未推进完成的请求。按时间段下载 K 线并输出 CSV 的完整流程见 [download 示例](examples/v1alpha3/download/main.go)。

### 合约列表、合约资料与期权查询

```go
func queryInstruments(ctx context.Context, md *market.Market) error {
	expired := false
	symbols, err := md.QueryQuotes(ctx, market.QuoteFilter{
		InsClass: []string{"FUTURE"}, ExchangeID: []string{"SHFE"},
		ProductID: []string{"rb"}, Expired: &expired,
	})
	if err != nil {
		return err
	}
	fmt.Println("螺纹钢合约:", symbols)
	info, err := md.QuerySymbolInfo(ctx, []string{"SHFE.rb2701"})
	if err != nil {
		return err
	}
	for _, ins := range info {
		fmt.Printf("%s %s 最小变动=%g 合约乘数=%d\n", ins.Symbol, ins.Name, ins.PriceTick, ins.VolumeMultiple)
	}
	options, err := md.QueryOptions(ctx, market.OptionQuery{
		Underlying: "SHFE.rb2701", OptionClass: "CALL", Expired: &expired,
	})
	if err != nil {
		return err
	}
	fmt.Println("看涨期权:", options)
	return nil
}
```

### 历史数据按时间定位、按 ID 翻页

`DatetimeNS` 和 K 线/Tick 的 `Datetime` 都使用 Unix 纳秒。下面只演示两页；完整下载器负责时间区间过滤、尾部判断和 CSV 输出。

```go
func readHistory(ctx context.Context, md *market.Market, from time.Time) error {
	request := market.KlineRequest{
		Symbol: "SHFE.rb2701", Period: time.Minute, Width: 1000,
		Position: core.WindowPosition{Mode: core.ByTime, DatetimeNS: from.UnixNano()},
	}
	page, err := md.QueryKlinesPage(ctx, request)
	if err != nil {
		return err
	}
	if !page.Window.RangeKnown || page.Window.ValidCount == 0 {
		return nil
	}
	for _, row := range page.Rows {
		if row.Present && row.Valid.Has(model.KlineClose) {
			fmt.Println(row.Value.ID, time.Unix(0, row.Value.Datetime), row.Value.Close)
		}
	}
	if page.Window.Range.RightID < page.SeriesLastID {
		request.Position = core.WindowPosition{Mode: core.ByID, LeftID: page.Window.Range.RightID + 1}
		next, err := md.QueryKlinesPage(ctx, request)
		if err != nil {
			return err
		}
		fmt.Println("下一页真实行数:", next.Window.ValidCount)
	}
	return nil
}
```

`QueryTicksPage` 使用相同的窗口定位方式。下载器的 `-start` 包含起点，`-end` 不包含终点，时间按北京时间解析：

```sh
go run ./examples/v1alpha3/download -symbol SHFE.rb2701 -period 1m \
  -start 2026-09-01 -end 2026-09-02 -out rb2701_1m.csv
go run ./examples/v1alpha3/download -type tick -symbol SHFE.rb2701 \
  -start "2026-09-01 09:00:00" -end "2026-09-01 10:00:00" -out rb2701_tick.csv
```

### 在策略循环中执行一次性查询

`tqapi` 不在后台推进查询，需要主动调用 `WaitUpdate`，完成后再读取 `Result`：

```go
func requestInfo(ctx context.Context, api *tqapi.API) error {
	request, err := api.Market.RequestSymbolInfo(ctx, []string{"SHFE.rb2701"})
	if err != nil {
		return err
	}
	for !request.Done() {
		pulse, err := api.WaitUpdate(ctx)
		if err := errors.Join(pulse.TaskError(), err); err != nil {
			return err
		}
	}
	info, err := request.Result()
	if err != nil {
		return err
	}
	fmt.Println(info)
	return nil
}
```

## 账户与交易

`auth.Client` 的天勤账户用于服务鉴权。交易账户另用 `core.AccountConfig` 声明 `BrokerID`、`UserName`、`Password` 等字段，两种凭证用途不同。

- `trade.Dial` 创建交易客户端，`OpenAccount` 登记并启动一个账户；需要在启动前注册事件或风控时，使用 `PrepareAccount` → `Events` / `SetCommandGuard` → `Start`。
- `tqapi.Config.Account` 接收一个账户声明；多账户使用 `Accounts`，二者不能同时设置。网络账户用 `core.AccountConfig`，本地模拟账户用 `sim.New(...)`。
- 声明账户后 `tqapi.New` 自动创建交易组件并启动账户，但不保证 `Ready`。单账户通过 `api.Account()` 获取句柄，多账户用 `api.Trade.Account(id)`。
- `Account()`、`Position()`、`Orders()`、`Trades()` 等方法读取已提交状态；`Submit` 和 `Cancel` 发出报撤单命令。
- `Submit` 成功只表示本地接受并排队，不表示网关接受或成交。后续通过订单状态、交易事件和成交记录确认结果。

独立交易支持 Channel `Events`、串行消费 `RunEvents` 和回调分派 `Serve`。本地 `risk` 包提供开仓次数、开仓数量、报撤单频率规则，通过账户的 `CommandGuard` 挂载。使用 `tqapi.Config.BeforeAccountStart` 可在账户启动前统一注册风控或早期事件订阅。

### 独立交易客户端与本地风控

下面只连接账户并打印事件。天勤凭证与期货公司资金账号分别从环境变量读取；`TQ_BROKER_ID`、`TQ_TRADE_USER`、`TQ_TRADE_PASS` 是此片段自行读取的变量，SDK 不会自动绑定它们。

```go
func watchAccount(ctx context.Context) error {
	client, err := trade.Dial(ctx, trade.Config{
		Auth: auth.NewClient(os.Getenv("TQ_AUTH_USER"), os.Getenv("TQ_AUTH_PASS")),
	})
	if err != nil {
		return err
	}
	defer client.Close()
	session, err := client.PrepareAccount(trade.AccountConfig{
		ID: "futures", BrokerID: os.Getenv("TQ_BROKER_ID"),
		UserName: os.Getenv("TQ_TRADE_USER"), Password: os.Getenv("TQ_TRADE_PASS"),
	})
	if err != nil {
		return err
	}
	guard, err := risk.New(
		&risk.OpenCountLimit{Name: "rb-open-count", Symbols: []string{"SHFE.rb2701"}, Limit: 10},
		&risk.OrderRateLimit{Name: "shfe-rate", Exchanges: []string{"SHFE"}, PerSecond: 5},
	)
	if err != nil {
		return err
	}
	if err := session.SetCommandGuard(guard); err != nil {
		return err
	}
	events, err := session.Events(ctx) // 先订阅，再启动，保留登录阶段的事件
	if err != nil {
		return err
	}
	defer events.Close()
	if err := session.Start(ctx); err != nil {
		return err
	}
	for {
		select {
		case event, ok := <-events.C():
			if !ok {
				return events.Err()
			}
			fmt.Printf("事件 %v: %+v\n", event.Kind, event)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
```

`OpenCountLimit` 统计本地接受的开仓报单次数，`OpenVolumeLimit` 统计开仓数量，`OrderRateLimit` 统计指定交易所最近一秒本地接受的报单和撤单数量。它们通过本地守卫执行，不等同于交易所或网关风控。

## 本地模拟交易与回测

二者共享 `sim` 账户和交易接口，行情来源不同：

| 模式 | `tqapi.Config` 配置 | 时间与结果 |
| --- | --- | --- |
| 实时模拟交易（paper trade） | `Market` + `Account: sim.New(...)`，不设置 `Backtest` | 实时行情驱动；`api.Paper.Reports()` 读取报告 |
| 历史回测 | `Market` + 模拟账户 + `Backtest` | 虚拟时间驱动；`api.Now()` 读取时间，`api.Backtest.Result()` 读取结果 |

模拟账户默认初始资金为 10,000,000，可用 `sim.Config.InitBalance` 设置；`Margins`、`Commissions` 按合约覆盖每手保证金和手续费。实时模拟配置示意：

```go
cfg := tqapi.Config{
	Auth:    auth.NewClient(os.Getenv("TQ_AUTH_USER"), os.Getenv("TQ_AUTH_PASS")),
	Market:  &core.MarketConfig{},
	Account: sim.New(sim.Config{InitBalance: 1_000_000}),
}
```

### 报单与回报

以下函数用于已经配置 `Market` 和唯一账户的 `api`，例如上面的本地模拟账户。它只报一笔限价开仓单，并等待订单结束；订单结束可能是成交、撤单或拒单，需要检查成交量与提示信息。

```go
func buyOne(ctx context.Context, api *tqapi.API, symbol string) error {
	account, err := api.Account()
	if err != nil {
		return err
	}
	quote, err := api.Market.Quote(ctx, symbol)
	if err != nil {
		return err
	}
	var order *core.OrderRef
	for {
		pulse, err := api.WaitUpdate(ctx)
		if err := errors.Join(pulse.TaskError(), err); err != nil {
			return err
		}
		if order != nil {
			o, state, present := order.Snapshot()
			if state == core.Ready && present && o.Status == model.OrderFinished {
				fmt.Printf("订单 %s 成交=%d 剩余=%d 提示=%s\n",
					o.OrderID, o.VolumeOriginal-o.VolumeLeft, o.VolumeLeft, o.LastMsg)
				return nil
			}
			continue
		}
		q := quote.Snapshot()
		if account.State() != core.Ready || q.State != core.Ready || !q.Valid.Has(model.QuoteAskPrice1) {
			continue
		}
		order, err = account.Submit(ctx, core.OrderRequest{
			Symbol: symbol, Direction: model.DirectionBuy, Offset: model.OffsetOpen,
			Volume: 1, Price: core.LimitPrice(q.Value.AskPrice1),
		})
		if err != nil {
			return err
		}
	}
}
```

账户的 `Position(symbol)`、`Orders()`、`Trades()` 可用于读取持仓、订单和成交。手工撤销一笔已知订单可使用以下片段，然后继续 `WaitUpdate`，直到订单回报确认结束：

```go
if err := account.Cancel(ctx, order.ID()); err != nil {
	return err
}
```

`Close`、超时或退出程序不会自动把手工订单撤掉；报撤单返回成功也不代表已成交或已撤销。完整开仓、持有、平仓流程见 [paper 示例](examples/v1alpha3/paper/main.go)。

### 离线数据集回测

下面的函数读取已有数据集，不需要天勤认证。数据集应包含该合约及请求周期的数据；`backtest.TradingDays` 按北京时间交易日生成起止时刻，包含首尾交易日及首日对应的夜盘区间。

```go
func runDataset(ctx context.Context, dir, symbol string, first, last time.Time) error {
	start, end := backtest.TradingDays(first, last)
	api, err := tqapi.New(ctx, tqapi.Config{
		Market: &core.MarketConfig{},
		Account: sim.New(sim.Config{InitBalance: 1_000_000}),
		Backtest: &backtest.Config{Start: start, End: end, Data: backtest.Dataset(dir)},
	})
	if err != nil {
		return err
	}
	defer api.Close()
	bars, err := api.Market.Klines(ctx, core.KlineRequest{
		Symbol: symbol, Period: time.Minute, Width: 20,
	})
	if err != nil {
		return err
	}
	for {
		pulse, err := api.WaitUpdate(ctx)
		if taskErr := pulse.TaskError(); taskErr != nil {
			return errors.Join(taskErr, err)
		}
		if errors.Is(err, backtest.ErrFinished) {
			break
		}
		if err != nil {
			return err
		}
		if pulse.IsChanging(bars.LatestRow(), "datetime") {
			fmt.Println("虚拟时间:", api.Now(), "窗口行数:", bars.Snapshot().Window.ValidCount)
			// 在这里执行策略；报单接口与实时模拟交易相同。
		}
	}
	result := api.Backtest.Result()
	for _, report := range result.Accounts {
		fmt.Printf("%s 权益=%.2f 收益率=%.4f 最大回撤=%.4f\n",
			report.AccountID, report.Balance, report.ROR, report.MaxDrawdown)
	}
	return result.Err
}
```

### 在线历史数据回测

创建 `tqapi.API` 前，将 `cfg.Backtest` 设置为在线数据源；回测仍使用本地模拟账户：

```go
start, end := backtest.TradingDays(firstTradingDay, lastTradingDay)
cfg.Backtest = &backtest.Config{
	Start: start, End: end,
	Data: backtest.Online(backtest.OnlineConfig{
		Auth: auth.NewClient(os.Getenv("TQ_AUTH_USER"), os.Getenv("TQ_AUTH_PASS")),
		Mode: backtest.Prefetch, CacheDir: "./backtest-cache",
	}),
}
```

`backtest.WriteDataset` 可生成 SDK 的离线数据集。在线数据源使用天勤历史行情服务：默认 `Prefetch` 预取整段序列，也可用 `Rolling` 分页滚动加载；`CacheDir` 只用于 Prefetch 模式。在线模式仍依赖服务可用性及账户授权。

`WaitUpdate` 返回 `backtest.ErrFinished` 表示回测正常结束，最后一个成功的 Pulse 已交付收尾结果。回测报告包含日结、成交、收益率、年化收益、最大回撤、夏普率和索提诺比率。

模拟撮合当前支持期货及 KQ 主连、指数的**限价 GFD 单**，不支持期权、市价、FAK/FOK。买限价达到卖一、卖限价达到买一时按委托价整笔成交。K 线回测使用合成盘口；订阅 Tick 后该合约报价及撮合由 Tick 驱动，不能把 K 线合成路径当作真实逐笔成交。

## 目标持仓与任务调度

`targetpos` 将目标净仓转换为平今、平昨、开仓与追价操作，在实盘、实时模拟和回测中随 `WaitUpdate` 推进。下面的片段假设 `api` 已配置行情和唯一账户，所在函数返回 `error`：

```go
account, err := api.Account()
if err != nil {
	return err
}
task, err := targetpos.New(ctx, api, account, targetpos.Config{
	Symbol: "SHFE.rb2701",
})
if err != nil {
	return err
}
ticket, err := task.SetTarget(2) // 目标净多 2 手；负数为净空，0 为平仓
if err != nil {
	return err
}
for !ticket.Done() {
	pulse, err := api.WaitUpdate(ctx)
	if taskErr := pulse.TaskError(); taskErr != nil {
		return errors.Join(taskErr, err)
	}
	if err != nil {
		return err
	}
}
result, err := ticket.Result()
if err != nil {
	return err
}
if result.Outcome != targetpos.Reached {
	return fmt.Errorf("目标未达到: %s，剩余 %d 手，原因 %s", result.Outcome, result.Remaining, result.Reason)
}
fmt.Printf("目标已达到，当前净仓 %d\n", result.ObservedNet)
```

默认使用对价、`"今昨,开"` 开平优先级和 `FinishCurrent` 目标替换策略；`Latest` 可先撤销当前在途单，再处理最新目标。支持 `MinVolume` / `MaxVolume` 拆单及自定义价格、交易时段策略。

`New`、`SetTarget`、`Cancel` 和 `WaitUpdate` 必须在同一 goroutine 调用。同一账户同一合约由任务控制期间，不应再从外部报撤单；外部干预、子单被拒等会导致任务进入失败收尾。票据 `Done()` 后仍要读取 `Result()` 判断目标结果。

退出时使用带超时的 `api.Shutdown(ctx)` 撤单并继续推进收尾，再检查 `task.CloseResult()` 中的 `Confirmed` / `Unresolved`。`api.Close()` 只快速释放资源，不能据此认定在途单已撤销。完整流程见 [targetpos 示例](examples/v1alpha3/targetpos/main.go)。

收尾使用独立的超时上下文，避免复用已经取消的策略上下文；应放在负责 API 生命周期的函数退出路径中：

```go
shutdownCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
defer cancel()
shutdownErr := api.Shutdown(shutdownCtx)
closed, closeErr := task.CloseResult()
if closeErr == nil && !closed.Confirmed {
	fmt.Printf("仍需确认的委托: %+v\n", closed.Unresolved)
}
if err := errors.Join(shutdownErr, closeErr); err != nil {
	return err
}
```

### 自定义定时任务

任务在实时模式使用本机时钟，在回测中使用虚拟时钟。例如下面的心跳任务无需 `time.Sleep`：

```go
type heartbeat struct {
	Every time.Duration // 调用者设置为正数，例如 time.Minute
}

func (h *heartbeat) Poll(in tqapi.TaskInput) (tqapi.TaskStep, error) {
	if in.Stopping {
		return tqapi.TaskStep{Done: true}, nil
	}
	fmt.Println("策略时间:", in.Now)
	return tqapi.TaskStep{WakeAt: in.Now.Add(h.Every), Changed: true}, nil
}
```

在策略启动时调用 `api.RegisterTask(&heartbeat{Every: time.Minute})`，检查返回的错误后继续 `WaitUpdate`。句柄的 `Cancel()` 请求收尾，后续 `Poll` 收到 `Stopping=true`。

自定义任务实现 `tqapi.Task.Poll`，通过 `RegisterTask` 注册，可声明 `WatchData`、`WakeAt`。`Poll` 必须短小、非阻塞，不能等待成交或重入 `WaitUpdate`。启用任务后，`WaitUpdate` 可能只交付任务反馈（`HasData() == false`），且任务反馈可以与驱动错误同时返回，应先检查 Pulse 再处理 error。

## 环境变量与文件配置

示例中的 `os.Getenv` 负责读取凭证；SDK 的 `auth.NewClient` 接收调用方传入的值，首次连接时完成登录、授权和路由发现。`HasFeature` 查询当前已取得的授权快照，不会自行触发登录；权限查询不会自动拦截订阅、下载或交易，也不代表数据已经就绪。

`config.Load(path)` 可读取 YAML、TOML 或 JSON 配置，未知键报错。配置包的环境变量名与示例不同：

| 入口 | 用户名 | 密码 | 其他环境变量 |
| --- | --- | --- | --- |
| `examples/v1alpha3` 与本文代码 | `TQ_AUTH_USER` | `TQ_AUTH_PASS` | 地址通过支持的 `-url` 参数覆盖 |
| `config.Load` | `TQ_AUTH_USER_NAME` | `TQ_AUTH_PASSWORD` | `TQ_MARKET_URL`、`TQ_TRADE_URL`、`TQ_LOG_LEVEL` |

若使用配置文件，可只在文件中保留非凭证字段，例如 `log.level`，再通过环境变量注入认证；不需要把密码写入文件。`File.Provider()`、`MarketConfig()`、`TradingConfig()`、`Logger()` 和账户 `Guard()` 用于装配组件，运行时对象不会从文件反序列化。交易账户列表没有自动绑定上述示例的 `TQ_TRADE_USER` / `TQ_TRADE_PASS`；自行装配账户时显式读取环境变量。

## 示例

以下路径均相对仓库根目录。在线模式必须设置 `TQ_AUTH_USER` / `TQ_AUTH_PASS`；`-url` 只覆盖服务地址，不跳过认证。具体参数用 `go run ./examples/v1alpha3/<示例> -h` 查看。

| 示例 | 内容 | 是否需要行情服务 |
| --- | --- | --- |
| [quote](examples/v1alpha3/quote/main.go) | 报价、K 线订阅与合约资料查询 | 是 |
| [kline](examples/v1alpha3/kline/main.go) | 单合约 K 线窗口与增量 | 是 |
| [tick](examples/v1alpha3/tick/main.go) | Tick 与五档盘口 | 是 |
| [multikline](examples/v1alpha3/multikline/main.go) | 多合约对齐 K 线 | 是 |
| [download](examples/v1alpha3/download/main.go) | 历史 K 线 / Tick 分页下载到 CSV | 是 |
| [strategy](examples/v1alpha3/strategy/main.go) | `WaitUpdate` / `IsChanging`，只读行情 | 是 |
| [paper](examples/v1alpha3/paper/main.go) | 实时行情驱动本地模拟交易 | 是 |
| [backtest](examples/v1alpha3/backtest/main.go) | 多周期策略、虚拟时钟任务、回测报告 | 默认使用离线合成数据 |
| [targetpos](examples/v1alpha3/targetpos/main.go) | 目标持仓、追价和撤单收尾 | 默认离线；`-paper` / `-online` 需在线服务 |

```sh
# 不连接网络服务
go run ./examples/v1alpha3/backtest
go run ./examples/v1alpha3/backtest -tick
go run ./examples/v1alpha3/targetpos

# 设置天勤账号后运行
go run ./examples/v1alpha3/quote -symbol SHFE.rb2701
go run ./examples/v1alpha3/kline -symbol SHFE.rb2701 -period 1m -width 200
go run ./examples/v1alpha3/tick -symbol SHFE.rb2701 -width 5
go run ./examples/v1alpha3/multikline -symbols SHFE.rb2701,SHFE.hc2701 -mode matched
go run ./examples/v1alpha3/strategy -symbol SHFE.rb2701
go run ./examples/v1alpha3/paper -symbol SHFE.rb2701
go run ./examples/v1alpha3/targetpos -paper -symbol SHFE.rb2701 -volume 2 -hold 30s
```

默认回测示例中的历史合约代码用于生成合成数据，不表示这些合约在默认日期存在可下载的真实行情。使用 `-online` 时需自行指定匹配的合约与历史日期。

## 包结构

| 包 | 职责 |
| --- | --- |
| `auth` | 天勤鉴权、Provider、路由与功能权限查询 |
| `model` | 行情与交易数据类型、字段编号和有效位 |
| `core` | Runtime、连接重连、diff 合并、窗口、查询、交易和变化追踪 |
| `market` | 后台推进的独立行情客户端 |
| `trade` | 后台推进的交易客户端与 Session |
| `tqapi` | 显式推进、Pulse、账户装配与协作式任务调度 |
| `sim` | 模拟账户、撮合账本、实时模拟与报告 |
| `backtest` | 离线/在线历史数据、虚拟时间和回测驱动 |
| `targetpos` | 目标持仓执行、拆单、追价与收尾 |
| `risk` | 可选本地报撤单规则 |
| `config` | 文件配置与运行时组件装配 |

## 验证与维护

```sh
go test -race ./shinny/v1alpha3/... ./examples/v1alpha3/...
go vet ./shinny/v1alpha3/... ./examples/v1alpha3/...
go generate ./shinny/v1alpha3/model
go test ./shinny/v1alpha3/core -run '^$' -bench Diff -benchmem -cpu=1
```

部分 core 测试依赖开发机上的 sniffer 语料与 diff 对照证据，缺失时跳过；这些外部语料不在仓库中。其余单元测试使用本地假服务或回放数据，不需要天勤账号。

`backtest/internal/symbols/expired_quotes.json.gz` 是运行时内嵌资料，需与 Go 源码一起保留；其来源、生成脚本与授权说明位于同目录的 `SOURCE`、`gen.py`、`NOTICE`。

## 当前边界

- v1alpha3 尚无旧版 `webadapter` 对应的浏览器可视化接口。
- 未实现股票账户、上期所组合合约代码转换、TradingStatus、HTTP 行情/交易查询、预委托与复权。
- 远端风控设置当前按实现限定 SSE/SZSE，尚未完成目标网关验收；它与本地 `risk` 规则是不同机制。
- 回测支持 K 线/Tick 的最新窗口；历史定位窗口、历史分页请求和多合约对齐窗口在回测运行时返回 `core.ErrUnsupported`。
- 序列数据按连接保留收到的历史行，不按窗口宽度裁剪，内存可能随请求过的历史增长；每个窗口使用独立 chart_id。
- 命令发送状态为 `Queued` / `Written` / `NotSent`，未单独表达“可能已写出”的不确定状态；业务结果仍需根据服务端状态确认。

## 项目关系

本项目为非官方实现。协议行为与部分模拟交易、回测语义参考 Python tqsdk；具体能力与边界以本仓库代码和测试为准，不承诺与官方 SDK 全量兼容。

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
