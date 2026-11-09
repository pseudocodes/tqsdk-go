// Package backtest 是 K 线回测驱动：历史数据读取、虚拟时间、多序列归并、行情生成与结束控制。
//
// 驱动装入 core.Runtime 后，每次 Step 交付一个逻辑包（行情 + 模拟账户结果 + 虚拟时间），语义对应
// tqsdk-python 3.10.4 的 backtest/backtest.py 与 TqSim：
//   - 当前时刻的请求回执、命令结果优先交付，没有待交付结果时才推进到下一事件时间；
//   - K 线开盘发布 open=high=low=close、收盘发布最终值；最小周期收盘依次以 high、low、close 合成盘口撮合；
//   - 由 K 线合成的盘口为价格 ± price_tick；同一时刻的事件放在同一逻辑包，按订阅顺序排列；
//   - 订阅了 Tick 的合约，报价由 Tick 原样生成（不加价差），每条 Tick 一个事件；
//   - 只订阅 Quote 或模拟账户需要行情、而合约没有 1 分钟及以下的 K 线或 Tick 时，隐式建立 1 分钟序列；
//   - 数据耗尽后执行收尾日结，之后返回 ErrFinished。
//
// 支持 K 线与 Tick 的最新窗口（Latest）；历史定位窗口、历史页与多合约对齐窗口返回 core.ErrUnsupported。
package backtest

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/internal/tday"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/internal/wire"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/sim"
)

var (
	// ErrFinished 是回测正常结束的哨兵；收尾结果已在最后一个成功的 Pulse 交付。
	ErrFinished = errors.New("backtest: finished")
)

// Model 是行情生成与撮合模型的版本，写入回测结果。
const Model = "PythonCompatible (tqsdk-python 3.10.4)"

const minute = int64(time.Minute)

// Config 配置一次回测。Start、End 为闭区间的时刻；按交易日指定时用 TradingDays。
type Config struct {
	Start, End time.Time
	Data       Provider
}

// TradingDays 把交易日区间 [first, last]（取日期部分，北京时间）转为回测起止时刻：
// 从 first 前一工作日 18:00 到 last 当天 18:00 前，与 Python TqBacktest 传入 date 时一致。
func TradingDays(first, last time.Time) (start, end time.Time) {
	midnight := func(t time.Time) int64 {
		y, m, d := t.In(tday.CST).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, tday.CST).UnixNano()
	}
	return time.Unix(0, tday.Start(midnight(first))).In(tday.CST), time.Unix(0, tday.End(midnight(last))).In(tday.CST)
}

// Status 是回测状态。
type Status uint8

const (
	Running Status = iota
	Finished
	Failed
)

func (s Status) String() string { return [...]string{"Running", "Finished", "Failed"}[s] }

// Result 是回测结果快照。
type Result struct {
	Status         Status
	Err            error // Failed 的原因
	Start, End     time.Time
	Last           time.Time // 最后已提交的虚拟时间
	PartialLastDay bool      // 区间在交易日中途结束，最后一日的收尾日结不完整
	Model          string
	Source         string
	Accounts       []sim.Report
}

// Account 是参与回测的一个模拟账户。
type Account struct {
	Handle *core.AccountHandle // core.Trading.PrepareLocalAccount 登记的本地账户
	Config sim.Config
}

type account struct {
	h     *core.AccountHandle
	led   *sim.Ledger
	queue [][]byte // 已接受、尚未处理的命令
}

type quoteState struct {
	minDur int64
	sent   bool // 已发送过初始报价
	q      sim.Quote
}

// Driver 实现 core.Driver。所有方法只在推进 goroutine（WaitUpdate 的调用者）中调用。
type Driver struct {
	cfg        Config
	start, end int64
	md         *core.Market
	accts      []*account

	now    int64
	day    int64 // 当前交易日
	inited bool
	status Status
	err    error

	series []*serie // 订阅顺序
	byKey  map[seriesKey]*serie
	quotes map[string]*quoteState
	infos  map[string]model.Instrument
	reqs   [][]byte

	// 本逻辑包的累积内容
	mdData    []any // 请求回执
	touched   []*serie
	quoteData []any
}

type seriesKey struct {
	symbol string
	dur    int64
}

// NewDriver 创建回测驱动；md 须为 core.NewLocalMarket 创建的本地行情组件。
func NewDriver(cfg Config, md *core.Market, accounts []Account) (*Driver, error) {
	if cfg.Data == nil {
		return nil, errors.New("backtest: Config.Data is required")
	}
	if cfg.Start.IsZero() || cfg.End.Before(cfg.Start) {
		return nil, fmt.Errorf("backtest: bad range %v ~ %v", cfg.Start, cfg.End)
	}
	d := &Driver{cfg: cfg, start: cfg.Start.UnixNano(), end: cfg.End.UnixNano(), md: md,
		byKey: map[seriesKey]*serie{}, quotes: map[string]*quoteState{}, infos: map[string]model.Instrument{}}
	d.now, d.day = d.start, tday.Of(d.start)
	for _, a := range accounts {
		d.accts = append(d.accts, &account{h: a.Handle, led: sim.NewLedger(a.Config)})
	}
	return d, nil
}

// Now 返回已提交的虚拟时间。
func (d *Driver) Now() time.Time { return time.Unix(0, d.now).In(tday.CST) }

// Status 返回回测状态。
func (d *Driver) Status() Status { return d.status }

// Result 返回结果快照；各账户报告在收尾日结后完整。
func (d *Driver) Result() Result {
	r := Result{Status: d.status, Err: d.err, Start: d.cfg.Start, End: d.cfg.End, Last: d.Now(),
		PartialLastDay: d.end < tday.End(d.day), Model: Model, Source: d.cfg.Data.source()}
	for _, a := range d.accts {
		r.Accounts = append(r.Accounts, a.led.Report())
	}
	return r
}

// Close 释放数据源（在线下载器）。
func (d *Driver) Close() error { return d.cfg.Data.close() }

// Next 实现 core.Driver：交付当前时刻的待交付结果，否则推进到下一事件或 wake。
func (d *Driver) Next(ctx context.Context, wake time.Time) (core.Packet, error) {
	if d.status == Failed {
		return core.Packet{}, d.err
	}
	if !d.inited {
		d.inited = true
		return d.initPacket(), nil
	}
	d.reqs = append(d.reqs, d.md.TakeRequests()...)
	for _, a := range d.accts {
		a.queue = append(a.queue, a.h.TakeCommands()...)
	}
	if err := d.handleRequests(ctx); err != nil {
		return d.failed(err)
	}
	if err := d.prepareCommands(ctx); err != nil {
		return d.failed(err)
	}
	if err := d.collect(ctx, d.now); err != nil { // 新订阅序列中不晚于当前时刻的事件（预热与补齐）
		return d.failed(err)
	}
	d.emitQuotes()
	d.runCommands()
	if d.pending() {
		return d.flush(), nil
	}
	if d.status == Finished {
		return core.Packet{}, ErrFinished
	}

	t, ok, err := d.nextTime(ctx)
	if err != nil {
		return d.failed(err)
	}
	if !ok {
		if len(d.series) == 0 { // 尚无任何数据需求：等待新请求或 ctx 结束
			<-ctx.Done()
			return core.Packet{}, ctx.Err()
		}
		return d.finish(), nil
	}
	if !wake.IsZero() && wake.UnixNano() < t {
		d.now = max(d.now, wake.UnixNano())
		return core.Packet{}, core.ErrWakeReached
	}
	d.now = t
	if td := tday.Of(t); td != d.day { // 跨交易日：日结排在该时刻全部行情事件之前
		for _, a := range d.accts {
			a.led.Settle(time.Unix(0, d.day).In(tday.CST).Format("2006-01-02"), tday.Format(td))
		}
		d.day = td
	}
	if err := d.collect(ctx, t); err != nil {
		return d.failed(err)
	}
	d.emitQuotes()
	d.runCommands()
	return d.flush(), nil
}

// failed 记录不可继续的错误；等待被 ctx 中断不改变状态，下次调用从同一时刻继续。
func (d *Driver) failed(err error) (core.Packet, error) {
	if !isCtx(err) {
		d.status, d.err = Failed, err
	}
	return core.Packet{}, err
}

func isCtx(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// initPacket 交付行情初始化完成与各账户的初始截面（含交易日），账户随之 Ready。
func (d *Driver) initPacket() core.Packet {
	d.mdData = append(d.mdData, new(wire.Object).Set("mdhis_more_data", false))
	for _, a := range d.accts {
		a.led.Init(tday.Format(d.day))
	}
	return d.flush()
}

// handleRequests 处理行情组件发出的订阅、窗口与查询请求；ctx 中断时保留未处理的请求。
func (d *Driver) handleRequests(ctx context.Context) error {
	for len(d.reqs) > 0 {
		var req struct {
			Aid         string         `json:"aid"`
			InsList     string         `json:"ins_list"`
			ChartID     string         `json:"chart_id"`
			Duration    int64          `json:"duration"`
			FocusDT     *int64         `json:"focus_datetime"`
			LeftKlineID *int64         `json:"left_kline_id"`
			QueryID     string         `json:"query_id"`
			Query       string         `json:"query"`
			Variables   map[string]any `json:"variables"`
		}
		raw := d.reqs[0]
		if err := json.Unmarshal(raw, &req); err != nil {
			return fmt.Errorf("backtest: bad request %s: %w", raw, err)
		}
		switch req.Aid {
		case "subscribe_quote":
			for _, s := range strings.Split(req.InsList, ",") {
				if s == "" {
					continue
				}
				if err := d.ensureQuote(ctx, s); err != nil {
					return err
				}
			}
			d.mdData = append(d.mdData, new(wire.Object).Set("ins_list", req.InsList))
		case "set_chart":
			if req.InsList == "" {
				d.mdData = append(d.mdData, new(wire.Object).Set("charts", new(wire.Object).Set(req.ChartID, nil)))
				break
			}
			if strings.Contains(req.InsList, ",") || req.Duration < 0 || req.FocusDT != nil || req.LeftKlineID != nil {
				return fmt.Errorf("%w: backtest supports Latest K-line and tick windows only (chart %s %s %d)",
					core.ErrUnsupported, req.ChartID, req.InsList, req.Duration)
			}
			if err := d.ensureSeries(ctx, req.InsList, req.Duration); err != nil {
				return err
			}
			d.mdData = append(d.mdData, new(wire.Object).Set("charts", new(wire.Object).Set(req.ChartID,
				new(wire.Object).Set("left_id", int64(-1)).Set("right_id", int64(-1)).Set("ready", true).
					Set("more_data", false).Set("state", jsontext.Value(raw)))))
		case "ins_query":
			if req.Query == "" {
				break // 释放请求
			}
			res, err := d.answerQuery(ctx, req.Query, req.Variables)
			if err != nil {
				return err
			}
			d.mdData = append(d.mdData, new(wire.Object).Set("symbols", new(wire.Object).Set(req.QueryID, res)))
		}
		d.reqs = d.reqs[1:]
	}
	return nil
}

// answerQuery 只回答合约资料查询（multi_symbol_info）；其他 GraphQL 无法确定历史时点语义，返回错误回执。
func (d *Driver) answerQuery(ctx context.Context, query string, vars map[string]any) (*wire.Object, error) {
	list, _ := vars["instrument_id"].([]any)
	if !strings.Contains(query, "multi_symbol_info") || len(list) == 0 {
		return new(wire.Object).Set("error", "backtest: unsupported query"), nil
	}
	var syms []string
	for _, v := range list {
		if s, ok := v.(string); ok {
			syms = append(syms, s)
		}
	}
	if err := d.loadInstruments(ctx, syms); err != nil {
		return nil, err
	}
	var out []any
	for _, s := range syms {
		if ins, ok := d.infos[s]; ok {
			out = append(out, d.gqlSymbol(ins))
		}
	}
	return new(wire.Object).Set("result", new(wire.Object).Set("multi_symbol_info", out)), nil
}

// gqlSymbol 把合约资料编码为合约服务的节点格式；expired 按当前交易日开始时刻计算（Python _update_valid_quotes）。
func (d *Driver) gqlSymbol(ins model.Instrument) *wire.Object {
	o := new(wire.Object).Set("instrument_id", ins.Symbol)
	has := ins.Valid.Has
	add := func(ok bool, k string, v any) {
		if ok {
			o.Set(k, v)
		}
	}
	add(has(model.InstrumentExchangeID), "exchange_id", ins.ExchangeID)
	add(has(model.InstrumentInsClass), "class", ins.InsClass)
	add(has(model.InstrumentProductID), "product_id", ins.ProductID)
	add(has(model.InstrumentPriceTick), "price_tick", ins.PriceTick)
	add(has(model.InstrumentPriceDecs), "price_decs", ins.PriceDecs)
	add(has(model.InstrumentVolumeMultiple), "volume_multiple", float64(ins.VolumeMultiple))
	add(has(model.InstrumentMargin), "margin", ins.Margin)
	add(has(model.InstrumentCommission), "commission", ins.Commission)
	add(has(model.InstrumentMaxLimitOrderVolume), "max_limit_order_volume", ins.MaxLimitOrderVolume)
	add(has(model.InstrumentMaxMarketOrderVolume), "max_market_order_volume", ins.MaxMarketOrderVolume)
	add(has(model.InstrumentMinLimitOrderVolume), "min_limit_order_volume", ins.MinLimitOrderVolume)
	add(has(model.InstrumentMinMarketOrderVolume), "min_market_order_volume", ins.MinMarketOrderVolume)
	add(has(model.InstrumentDeliveryYear), "delivery_year", ins.DeliveryYear)
	add(has(model.InstrumentDeliveryMonth), "delivery_month", ins.DeliveryMonth)
	if has(model.InstrumentExpireDatetime) {
		exp := int64(ins.ExpireDatetime * 1e9)
		o.Set("expire_datetime", exp).Set("expired", exp <= tday.Start(d.day))
	}
	if len(ins.TradingTime.Day)+len(ins.TradingTime.Night) > 0 {
		o.Set("trading_time", new(wire.Object).Set("day", ins.TradingTime.Day).Set("night", ins.TradingTime.Night))
	}
	return o
}

func (d *Driver) loadInstruments(ctx context.Context, syms []string) error {
	var missing []string
	for _, s := range syms {
		if _, ok := d.infos[s]; !ok {
			missing = append(missing, s)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	got, err := d.cfg.Data.instruments(ctx, missing)
	if err != nil {
		return err
	}
	for s, ins := range got {
		d.infos[s] = ins
	}
	return nil
}

// ensureSeries 登记一条序列（已登记则复用）；同一合约记录最小驱动周期。
func (d *Driver) ensureSeries(ctx context.Context, symbol string, dur int64) error {
	key := seriesKey{symbol, dur}
	if d.byKey[key] != nil {
		return nil
	}
	if err := d.loadInstruments(ctx, []string{symbol}); err != nil {
		return err
	}
	ins, ok := d.infos[symbol]
	if !ok || !ins.Valid.Has(model.InstrumentPriceTick) {
		return fmt.Errorf("backtest: no instrument info for %s", symbol)
	}
	s := &serie{symbol: symbol, dur: dur, cur: d.cfg.Data.cursor(symbol, time.Duration(dur), d.now, d.end)}
	d.series = append(d.series, s)
	d.byKey[key] = s
	q := d.quotes[symbol]
	if q == nil {
		q = &quoteState{minDur: dur}
		d.quotes[symbol] = q
	}
	q.minDur = min(q.minDur, dur)
	return nil
}

// ensureQuote 保证合约有驱动报价的序列：没有序列或最小周期大于 1 分钟时隐式订阅 1 分钟线。
func (d *Driver) ensureQuote(ctx context.Context, symbol string) error {
	if q := d.quotes[symbol]; q == nil || q.minDur > minute {
		return d.ensureSeries(ctx, symbol, minute)
	}
	return nil
}

// collect 依次取出时间不晚于 t 的事件：每次选下一事件时间最早的序列，相等时按订阅顺序；
// 随后为本包触及的序列生成报价并送入模拟账户撮合。
func (d *Driver) collect(ctx context.Context, t int64) error {
	for {
		var best *serie
		var bt int64
		for _, s := range d.series {
			ht, ok, err := s.head(ctx, d.end)
			if err != nil {
				return err
			}
			d.markTouched(s) // 无数据的序列在 head 中被触及，需发送 last_id=-1
			if ok && ht <= t && (best == nil || ht < bt) {
				best, bt = s, ht
			}
		}
		if best == nil {
			return nil
		}
		best.pop()
		d.markTouched(best)
	}
}

// markTouched 按首次触及顺序记录本包变化的序列。
func (d *Driver) markTouched(s *serie) {
	if s.touched && !slices.Contains(d.touched, s) {
		d.touched = append(d.touched, s)
	}
}

// nextTime 返回所有活动序列中最早的下一事件时间；全部序列都须给出下一事件或结束（阻塞归并）。
func (d *Driver) nextTime(ctx context.Context) (int64, bool, error) {
	var best int64
	found := false
	for _, s := range d.series {
		t, ok, err := s.head(ctx, d.end)
		if err != nil {
			return 0, false, err
		}
		if ok && (!found || t < best) {
			best, found = t, true
		}
	}
	return best, found, nil
}

// prepareCommands 为等待行情的报单建立行情需求（隐式订阅），使其预热在本包内完成。
func (d *Driver) prepareCommands(ctx context.Context) error {
	for _, a := range d.accts {
		for _, raw := range a.queue {
			var c struct {
				Aid          string `json:"aid"`
				ExchangeID   string `json:"exchange_id"`
				InstrumentID string `json:"instrument_id"`
			}
			if json.Unmarshal(raw, &c) == nil && c.Aid == "insert_order" && d.status != Finished {
				symbol := c.ExchangeID + "." + c.InstrumentID
				if err := d.ensureQuote(ctx, symbol); err != nil {
					return err
				}
				if err := d.ensureCosts(ctx, symbol); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ensureCosts 为 KQ 主连与指数补齐每手保证金与手续费（来源合约的资料按需加载，见 sim.ResolveCosts）。
func (d *Driver) ensureCosts(ctx context.Context, symbol string) error {
	for {
		var missing string
		ins, ok := sim.ResolveCosts(d.infos[symbol], func(s string) (model.Instrument, bool) {
			info, ok := d.infos[s]
			if !ok {
				missing = s
			}
			return info, ok
		})
		if ok {
			d.infos[symbol] = ins
			return nil
		}
		if err := d.loadInstruments(ctx, []string{missing}); err != nil {
			return err
		}
		if _, ok := d.infos[missing]; !ok { // 来源合约没有资料：按缺少费用拒单
			return nil
		}
	}
}

// runCommands 按接受顺序处理各账户的命令；报单所需的行情尚未就绪时，该账户后续命令一并等待（同 Python）。
func (d *Driver) runCommands() {
	for _, a := range d.accts {
		for len(a.queue) > 0 {
			var c struct {
				Aid     string `json:"aid"`
				OrderID string `json:"order_id"`
			}
			raw := a.queue[0]
			if json.Unmarshal(raw, &c) != nil {
				a.queue = a.queue[1:]
				continue
			}
			switch c.Aid {
			case "insert_order":
				var in sim.InsertOrder
				json.Unmarshal(raw, &in)
				if d.status == Finished {
					a.led.Reject(d.now, in, "回测已结束")
					break
				}
				symbol := in.ExchangeID + "." + in.InstrumentID
				q := d.quotes[symbol]
				if q == nil || !q.sent {
					return // 等待行情
				}
				info := d.infos[symbol]
				if !a.led.Active(symbol) {
					a.led.SetInstrument(symbol, info)
					a.led.OnQuote(d.now, symbol, q.q)
				}
				a.led.Insert(d.now, in, tday.InTradingTime(info.TradingTime, d.now))
			case "cancel_order":
				a.led.Cancel(c.OrderID)
			}
			a.queue = a.queue[1:]
		}
	}
}

// emitQuotes 为本包新触及的序列生成报价并依次送入模拟账户撮合（Python _generator_quotes_diffs）：
//   - 每条序列只用本包最后一个事件生成报价，同一时刻的多条 Tick 只有最后一条参与撮合；
//   - 早于当前时刻且已发过初始报价的跳过（补齐的历史不再改变报价）；
//   - 合约订阅了 Tick 后报价只由 Tick 生成，K 线不再生成报价。
func (d *Driver) emitQuotes() {
	for _, s := range d.touched {
		if !s.hasLast {
			continue
		}
		q, e := d.quotes[s.symbol], s.last
		s.hasLast = false
		if e.time < d.now && q.sent || q.minDur == 0 && s.dur != 0 {
			continue
		}
		var diffs []any
		var steps []quoteStep
		if s.dur == 0 {
			diffs, steps = tickQuote(s.symbol, e.tick)
		} else {
			diffs, steps = quoteDiffs(s.symbol, d.infos[s.symbol].PriceTick, e, q.minDur, s.dur)
		}
		d.quoteData = append(d.quoteData, diffs...)
		for _, st := range steps {
			q.q = sim.Quote{AskPrice1: st.ask, BidPrice1: st.bid, LastPrice: st.last}
			for _, a := range d.accts {
				a.led.OnQuote(d.now, s.symbol, q.q)
			}
		}
		q.sent = true
	}
}

func (d *Driver) pending() bool {
	if len(d.mdData) > 0 || len(d.touched) > 0 || len(d.quoteData) > 0 {
		return true
	}
	for _, a := range d.accts {
		if a.led.Pending() {
			return true
		}
	}
	return false
}

// flush 把本包的行情与各账户结果组成一个逻辑包；行情 diff 顺序同 Python：请求回执、K 线、报价。
func (d *Driver) flush() core.Packet {
	all := d.mdData
	for _, s := range d.touched {
		all = append(all, s.diff())
	}
	all = append(all, d.quoteData...)
	d.mdData, d.touched, d.quoteData = nil, nil, nil
	p := core.Packet{Time: time.Unix(0, d.now)}
	if len(all) > 0 {
		p.Parts = append(p.Parts, core.PacketPart{Market: d.md, Frame: wire.Frame(all)})
	}
	for _, a := range d.accts {
		if el := a.led.Flush(); len(el) > 0 {
			p.Parts = append(p.Parts, core.PacketPart{Account: a.h, Frame: wire.Frame(el)})
		}
	}
	return p
}

// finish 在数据耗尽时执行收尾日结（同 Python _handle_stat_report），交付后进入 Finished。
func (d *Driver) finish() core.Packet {
	for _, a := range d.accts {
		a.led.Settle(time.Unix(0, d.day).In(tday.CST).Format("2006-01-02"), "")
	}
	d.status = Finished
	return d.flush()
}
