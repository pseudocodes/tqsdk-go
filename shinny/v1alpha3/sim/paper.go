package sim

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/internal/tday"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/internal/wire"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// Paper 是实时模拟交易（paper trade）的本地账户后端，实现 core.Backend：
// 实时行情先经撮合，再与账户结果作为一个 Commit 提交。规则对应 Python TqSim 的实时模式（tradeable/sim/basesim.py）：
//   - 业务时钟以行情时间为锚：全部合约最近一次行情时间的最大值 + 自 reader 收到该行情以来的本机单调时间；
//   - 首笔带时间的行情确定交易日并交付账户初始截面，账户随之 Ready；
//   - 行情时间越过交易日结束（18:00）时先日结、再撮合该帧行情；没有行情时由业务时钟到点日结；
//   - 报单先订阅合约并取得合约资料与有效行情（Python _ensure_quote），就绪前不发布委托；
//     合约服务查无此合约时拒单；行情断线（Stale）期间报单等待恢复，不按旧盘口成交；
//   - 每个已接受的命令单独形成一个逻辑包，无需等待下一笔行情。
type Paper struct {
	md    *core.Market
	accts []*paperAccount
	syms  map[string]*paperSymbol
	infos map[string]*infoState

	anchor  int64     // 最近行情时间（Unix 纳秒）
	anchorS string    // 其原文，与行情 datetime 按文本比较（同 Python）
	recv    time.Time // reader 收到该行情的本机时间（含单调读数）
	day     int64     // 当前交易日；0 表示尚未确定
}

type paperAccount struct {
	h       *core.AccountHandle
	led     *Ledger
	queue   [][]byte      // 已接受、尚未处理的命令
	waiting []InsertOrder // 等待合约资料或行情的报单，按接受顺序
}

type paperSymbol struct {
	ref     *core.QuoteRef // 账户持有的行情需求，与策略的订阅独立计数
	info    model.Instrument
	known   bool  // 已取得合约资料（含主连、指数补齐的费用）
	missing bool  // 合约服务没有该合约
	tracked bool  // q 已按已提交行情初始化，此后随每帧行情更新
	q       Quote // 撮合使用的盘口
}

// infoState 是一个合约的资料查询。
type infoState struct {
	req   *core.Request[[]model.Instrument]
	info  model.Instrument
	found bool
	done  bool
}

// NewPaper 创建与行情组件 md 协同的模拟交易后端；用 Add 登记账户后交给 core.Runtime.SetBackend。
func NewPaper(md *core.Market) *Paper {
	return &Paper{md: md, syms: map[string]*paperSymbol{}, infos: map[string]*infoState{}}
}

// Add 登记一个由 Account.Prepare 创建的本地账户。
func (p *Paper) Add(h *core.AccountHandle, cfg Config) {
	p.accts = append(p.accts, &paperAccount{h: h, led: NewLedger(cfg)})
}

// Reports 返回各账户的日结记录与指标；与 WaitUpdate 在同一 goroutine 调用。
func (p *Paper) Reports() []Report {
	out := make([]Report, 0, len(p.accts))
	for _, a := range p.accts {
		out = append(out, a.led.Report())
	}
	return out
}

// OnMarket 实现 core.Backend：按帧内顺序更新业务时钟、处理交易日切换，再用逐次盘口撮合并估值。
func (p *Paper) OnMarket(frame []byte, recv time.Time) ([]core.PacketPart, error) {
	if !bytes.Contains(frame, []byte(`"quotes"`)) {
		return nil, nil
	}
	var pack struct {
		Data []struct {
			Quotes jsontext.Value `json:"quotes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(frame, &pack); err != nil {
		return nil, err
	}
	for _, d := range pack.Data {
		if len(d.Quotes) == 0 {
			continue
		}
		diffs, err := parseQuotes(d.Quotes)
		if err != nil {
			return nil, err
		}
		for _, q := range diffs {
			if q.datetime > p.anchorS {
				if t, err := time.ParseInLocation("2006-01-02 15:04:05.000000", q.datetime, tday.CST); err == nil {
					p.anchor, p.anchorS, p.recv = t.UnixNano(), q.datetime, recv
				}
			}
		}
		if p.anchorS != "" {
			p.advance(p.anchor)
		}
		now := p.clock(time.Now())
		for _, q := range diffs {
			s := p.syms[q.symbol]
			if s == nil || !s.tracked {
				continue
			}
			q.mergeInto(&s.q)
			for _, a := range p.accts {
				if a.led.Active(q.symbol) {
					a.led.OnQuote(now, q.symbol, s.q)
				}
			}
		}
	}
	return p.flush(), nil
}

// Poll 实现 core.Backend：到点日结优先，其次按接受顺序处理一个命令。
func (p *Paper) Poll(now time.Time) ([]core.PacketPart, time.Time, error) {
	for _, a := range p.accts {
		a.queue = append(a.queue, a.h.TakeCommands()...)
	}
	var next time.Time
	if p.day != 0 {
		if c := p.clock(now); c > tday.End(p.day) {
			p.advance(c)
			if parts := p.flush(); len(parts) > 0 {
				return parts, time.Time{}, nil
			}
		}
		next = p.recv.Add(time.Duration(tday.End(p.day) + 1 - p.anchor))
	}
	for _, a := range p.accts {
		if p.process(a, now) {
			return p.flush(), next, nil
		}
	}
	return nil, next, nil
}

// advance 以业务时间 t 确定交易日：首次交付初始截面，越过交易日结束时日结并切换到 t 所属交易日。
func (p *Paper) advance(t int64) {
	switch {
	case p.day == 0:
		p.day = tday.Of(t)
		for _, a := range p.accts {
			a.led.Init(tday.Format(p.day))
		}
	case t > tday.End(p.day):
		next := tday.Of(t)
		date := time.Unix(0, p.day).In(tday.CST).Format("2006-01-02")
		for _, a := range p.accts {
			a.led.Settle(date, tday.Format(next))
		}
		p.day = next
	}
}

// clock 返回估计的交易所时间（Python _get_trade_timestamp）；尚无行情时为本机时间。
func (p *Paper) clock(now time.Time) int64 {
	if p.recv.IsZero() {
		return now.UnixNano()
	}
	return p.anchor + int64(now.Sub(p.recv))
}

// process 处理账户的下一个可执行命令：先是已就绪的等待报单，再是新命令。处理了命令时返回 true。
func (p *Paper) process(a *paperAccount, now time.Time) bool {
	for i, in := range a.waiting {
		if p.insert(a, in, now) {
			a.waiting = slices.Delete(a.waiting, i, i+1)
			return true
		}
	}
	for len(a.queue) > 0 {
		raw := a.queue[0]
		a.queue = a.queue[1:]
		var c struct {
			InsertOrder `json:",inline"`
			Aid         string `json:"aid"`
		}
		if json.Unmarshal(raw, &c) != nil {
			continue
		}
		switch c.Aid {
		case "insert_order":
			if !p.insert(a, c.InsertOrder, now) {
				a.waiting = append(a.waiting, c.InsertOrder)
				continue
			}
		case "cancel_order":
			if i := slices.IndexFunc(a.waiting, func(in InsertOrder) bool { return in.OrderID == c.OrderID }); i >= 0 {
				a.led.Reject(p.clock(now), a.waiting[i], "已撤单")
				a.waiting = slices.Delete(a.waiting, i, i+1)
			} else {
				a.led.Cancel(c.OrderID)
			}
		default:
			continue
		}
		return true
	}
	return false
}

// insert 在合约资料与行情就绪时处理报单；仍需等待时返回 false。
func (p *Paper) insert(a *paperAccount, in InsertOrder, now time.Time) bool {
	symbol := in.ExchangeID + "." + in.InstrumentID
	t := p.clock(now)
	s, err := p.symbol(symbol)
	if err != nil {
		a.led.Reject(t, in, "下单失败, "+err.Error())
		return true
	}
	if !p.ready(s, symbol) {
		return false
	}
	if s.missing {
		a.led.Reject(t, in, "下单失败, 合约 "+symbol+" 不存在")
		return true
	}
	if !a.led.Active(symbol) {
		a.led.SetInstrument(symbol, s.info)
		a.led.OnQuote(t, symbol, s.q)
	}
	a.led.Insert(t, in, tday.InTradingTime(s.info.TradingTime, t))
	return true
}

// symbol 返回合约的行情需求，首次使用时订阅。
func (p *Paper) symbol(symbol string) (*paperSymbol, error) {
	if s := p.syms[symbol]; s != nil {
		return s, nil
	}
	ref, err := p.md.QuoteRef(context.Background(), symbol)
	if err != nil {
		return nil, err
	}
	s := &paperSymbol{ref: ref}
	p.syms[symbol] = s
	return s, nil
}

// ready 推进合约资料查询并检查行情：资料已知（或确认不存在）且行情 Ready、带行情时间时返回 true。
func (p *Paper) ready(s *paperSymbol, symbol string) bool {
	if !s.known && !s.missing {
		info, found, done := p.instrument(symbol)
		if !done {
			return false
		}
		if !found {
			s.missing = true
			return true
		}
		if s.info, s.known = ResolveCosts(info, func(sym string) (model.Instrument, bool) {
			info, _, done := p.instrument(sym) // 来源合约不存在时以无费用资料继续，报单检查拒单
			return info, done
		}); !s.known {
			return false
		}
	}
	if s.missing {
		return true
	}
	snap := s.ref.Snapshot()
	if snap.State != core.Ready || snap.Value.Datetime == "" {
		return false
	}
	if !s.tracked {
		v := snap.Value
		s.q, s.tracked = Quote{AskPrice1: v.AskPrice1, BidPrice1: v.BidPrice1, LastPrice: v.LastPrice}, true
	}
	return true
}

// instrument 推进一个合约的资料查询；done=false 表示仍在查询，查询失败（如断线）时下次重新发起。
func (p *Paper) instrument(symbol string) (info model.Instrument, found, done bool) {
	s := p.infos[symbol]
	if s == nil {
		s = &infoState{}
		p.infos[symbol] = s
	}
	if !s.done {
		if s.req == nil {
			s.req, _ = p.md.RequestSymbolInfo(context.Background(), []string{symbol})
			return info, false, false
		}
		list, err := s.req.Result()
		if errors.Is(err, core.ErrNotReady) {
			return info, false, false
		}
		s.req = nil
		if err != nil {
			return info, false, false
		}
		s.done = true
		if len(list) > 0 {
			s.info, s.found = list[0], true
		}
	}
	return s.info, s.found, true
}

func (p *Paper) flush() []core.PacketPart {
	var parts []core.PacketPart
	for _, a := range p.accts {
		if el := a.led.Flush(); len(el) > 0 {
			parts = append(parts, core.PacketPart{Account: a.h, Frame: wire.Frame(el)})
		}
	}
	return parts
}

// quoteDiff 是一帧中一个合约的报价增量（只取撮合需要的字段）。
type quoteDiff struct {
	symbol         string
	datetime       string
	ask, bid, last price
}

func (d *quoteDiff) mergeInto(q *Quote) {
	d.ask.mergeInto(&q.AskPrice1)
	d.bid.mergeInto(&q.BidPrice1)
	d.last.mergeInto(&q.LastPrice)
}

// price 是一个可选的价格字段；"-"、"" 与 null 表示当前不可用（NaN）。
type price struct {
	set bool
	v   float64
}

func (p *price) UnmarshalJSON(b []byte) error {
	p.set = true
	if string(b) == "null" || json.Unmarshal(b, &p.v) != nil {
		p.v = math.NaN()
	}
	return nil
}

func (p price) mergeInto(dst *float64) {
	if p.set {
		*dst = p.v
	}
}

// parseQuotes 按帧内顺序解析 quotes 对象；删除（null）的合约跳过。
func parseQuotes(raw jsontext.Value) ([]quoteDiff, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.ReadToken(); err != nil {
		return nil, err
	}
	var out []quoteDiff
	for dec.PeekKind() == '"' {
		name, err := dec.ReadToken()
		if err != nil {
			return nil, err
		}
		symbol := name.String() // Token 在下一次读取后失效
		val, err := dec.ReadValue()
		if err != nil {
			return nil, err
		}
		if val.Kind() != '{' {
			continue
		}
		var d struct {
			Datetime string `json:"datetime"`
			Ask      price  `json:"ask_price1"`
			Bid      price  `json:"bid_price1"`
			Last     price  `json:"last_price"`
		}
		if err := json.Unmarshal(val, &d); err != nil {
			return nil, err
		}
		out = append(out, quoteDiff{symbol: symbol, datetime: d.Datetime, ask: d.Ask, bid: d.Bid, last: d.Last})
	}
	return out, nil
}
