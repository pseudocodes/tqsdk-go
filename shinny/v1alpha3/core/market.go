package core

import (
	"context"
	"encoding/json/v2"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/auth"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// MarketConfig 配置行情组件。URL 选择顺序：URL → Access.Endpoint；注入 Source 时不拨号。
type MarketConfig struct {
	Auth            auth.Provider
	Source          Source                                // 注入的连接（回放/测试）；成功返回后由组件接管
	Redial          func(context.Context) (Source, error) // 注入连接断开后的重建函数；nil 表示不重连
	URL             string
	Logger          *zap.Logger
	MaxMessageBytes int64 // 单条解压后消息上限，默认 DefaultMaxMessageBytes
	QueueSize       int   // 发送队列长度，默认 1024
	MailboxSize     int   // 订阅邮箱默认容量，默认 DefaultMailboxSize
	Backtest        bool  // 接入回测行情服务（名称服务 backtest=true），用于下载历史数据
}

// Market 是行情组件：一条行情连接上的报价、序列、查询与订阅。
type Market struct {
	rt     *Runtime
	id     uint64
	cfg    MarketConfig
	log    *zap.Logger
	route  *route
	access atomic.Pointer[auth.Access]

	// 以下字段受 rt.mu 保护。
	quotes      map[string]*quoteNode
	klines      map[seriesKey]*series[model.Kline, model.KlineField]
	ticks       map[seriesKey]*series[model.Tick, model.TickField]
	charts      map[string]*chartState
	windows     map[uint64]marketWindow
	queries     map[string]*queryCall
	quoteSubs   map[*QuoteSub]struct{}
	sentInsList string
	echoInsList string
	mdhis       bool
	mdhisSeen   bool
	recovering  bool // 重连后尚未满足恢复屏障
	closed      bool
}

// quoteNode 是一个合约报价的稳定身份；值在提交时改变。
type quoteNode struct {
	id      uint64
	symbol  string
	value   model.Quote
	valid   model.QuoteFields
	exists  bool
	deleted bool // 被当前代次显式删除
	state   DataState
	gen     uint64
	leases  int
}

// marketWindow 是行情组件内的一个视图（单合约或多合约窗口）。
type marketWindow interface {
	affected(j *mdJournal) bool
	evaluate(j *mdJournal, ch *marketChanges, pub *[]func())
	drop(err error) // 连接失败时终结一次性请求；返回后窗口可能已关闭
	closeLocked()
}

// NewMarket 鉴权并绑定行情来源，不启动驱动。
func NewMarket(ctx context.Context, rt *Runtime, cfg MarketConfig) (*Market, error) {
	if cfg.Auth == nil {
		return nil, errors.New("tqsdk: MarketConfig.Auth is required")
	}
	m := newMarket(rt, cfg)
	req := auth.Request{Service: auth.Market, Backtest: cfg.Backtest}
	access, err := cfg.Auth.Authorize(ctx, req)
	if err != nil {
		return nil, err
	}
	m.access.Store(&access)
	src, dial := cfg.Source, cfg.Redial
	if src == nil {
		url := cfg.URL
		if url == "" {
			url = access.Endpoint
		}
		if url == "" {
			return nil, errors.New("tqsdk: market URL unknown")
		}
		dial = func(ctx context.Context) (Source, error) {
			a, err := cfg.Auth.Authorize(ctx, req)
			if err != nil {
				return nil, err
			}
			m.access.Store(&a)
			return DialWebsocket(ctx, url, a.Header, cfg.MaxMessageBytes)
		}
		if src, err = DialWebsocket(ctx, url, access.Header, cfg.MaxMessageBytes); err != nil {
			return nil, err
		}
	}
	r, err := rt.attachRoute(m.id, src, m.decodeMarket, m, dial, cfg.QueueSize, nil)
	if err != nil {
		if cfg.Source == nil {
			src.Close()
		}
		return nil, err
	}
	m.route = r
	rt.onClose(func() { m.Close() })
	return m, nil
}

func newMarket(rt *Runtime, cfg MarketConfig) *Market {
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	return &Market{
		rt: rt, id: rt.newID(), cfg: cfg, log: cfg.Logger,
		quotes:    map[string]*quoteNode{},
		klines:    map[seriesKey]*series[model.Kline, model.KlineField]{},
		ticks:     map[seriesKey]*series[model.Tick, model.TickField]{},
		charts:    map[string]*chartState{},
		windows:   map[uint64]marketWindow{},
		queries:   map[string]*queryCall{},
		quoteSubs: map[*QuoteSub]struct{}{},
	}
}

// NewLocalMarket 创建由本地驱动（回测）提供数据的行情组件：不鉴权、不拨号。
// 订阅、窗口与查询请求经 TakeRequests 交给驱动；功能权限同 Python 回测模式放开 futr/sec/lmt_idx。
func NewLocalMarket(rt *Runtime, cfg MarketConfig) (*Market, error) {
	m := newMarket(rt, cfg)
	m.access.Store(&auth.Access{Features: auth.Granted("futr", "sec", "lmt_idx")})
	r, err := rt.attachLocalRoute(m.id, m.decodeMarket, m)
	if err != nil {
		return nil, err
	}
	m.route = r
	rt.onClose(func() { m.Close() })
	return m, nil
}

// TakeRequests 取走本地行情组件已发出的请求报文（subscribe_quote / set_chart / ins_query），按发送顺序。
func (m *Market) TakeRequests() [][]byte {
	m.rt.mu.Lock()
	defer m.rt.mu.Unlock()
	return m.route.takeOutbox()
}

// ReleaseKlines 释放 K 线序列中 ID 小于 beforeID 的行，供后台下载器控制内存。
// 仅在该序列没有打开的窗口时生效（服务端不会重发已下发的行，调用方不得再请求被释放的行）。
func (m *Market) ReleaseKlines(symbol string, period time.Duration, beforeID int64) {
	m.rt.mu.Lock()
	defer m.rt.mu.Unlock()
	s := m.klines[seriesKey{symbol, int64(period)}]
	if s == nil {
		return
	}
	for _, w := range m.windows {
		if aw, ok := w.(*AlignedKlines); ok && slices.Contains(aw.legs, s) {
			return
		}
	}
	releaseRows(m, s, beforeID)
}

// ReleaseTicks 同 ReleaseKlines，作用于 Tick 序列。
func (m *Market) ReleaseTicks(symbol string, beforeID int64) {
	m.rt.mu.Lock()
	defer m.rt.mu.Unlock()
	if s := m.ticks[seriesKey{symbol, 0}]; s != nil {
		releaseRows(m, s, beforeID)
	}
}

// releaseRows 在序列没有打开的窗口时删除 ID 小于 beforeID 的行；调用方持有 rt.mu。
func releaseRows[T any, F ~uint16](m *Market, s *series[T, F], beforeID int64) {
	for _, w := range m.windows {
		if sw, ok := w.(*seriesWindow[T, F]); ok && sw.s == s {
			return
		}
	}
	for id := range s.rows {
		if id < beforeID {
			delete(s.rows, id)
		}
	}
}

// HasFeature 查询最近一次成功接入授权提供的功能权限。
// core 发起重连授权时更新权限；注入 Redial 时沿用初始权限视图。
func (m *Market) HasFeature(feature string) bool {
	a := m.access.Load()
	return a != nil && a.HasFeature(feature)
}

// Close 释放组件的路由、窗口、等待者和订阅；不关闭借用的 Runtime。幂等。
func (m *Market) Close() error {
	m.rt.mu.Lock()
	if m.closed {
		m.rt.mu.Unlock()
		return nil
	}
	m.closed = true
	for _, w := range m.windows {
		w.drop(ErrClosed)
		w.closeLocked()
	}
	for _, c := range m.queries {
		c.finish(nil, ErrClosed)
	}
	subs := m.quoteSubs
	m.quoteSubs = map[*QuoteSub]struct{}{}
	m.rt.mu.Unlock()
	m.route.close()
	for s := range subs {
		s.mb.close(ErrClosed)
	}
	return nil
}

// Changes 提取提交中属于本组件的变化；其他组件的提交返回空值。
func (m *Market) Changes(c Commit) MarketChanges {
	if c.runtimeID != m.rt.id {
		panic("tqsdk: commit from another runtime")
	}
	p, _ := c.changesFor(m.id).(*marketChanges)
	return MarketChanges{p: p}
}

func (m *Market) usable() error {
	if m.closed {
		return ErrClosed
	}
	return nil
}

func (m *Market) quoteNode(symbol string) *quoteNode {
	n := m.quotes[symbol]
	if n == nil {
		n = &quoteNode{id: m.rt.newID(), symbol: symbol}
		quoteSchema.reset(&n.value)
		n.value.Symbol = symbol
		m.quotes[symbol] = n
	}
	return n
}

// lease / release 维护报价订阅；集合变化时发送全量 subscribe_quote（同 Python）。
func (m *Market) lease(symbols []string) {
	for _, s := range symbols {
		m.quoteNode(s).leases++
	}
	m.syncSubscription()
}

func (m *Market) release(symbols []string) {
	for _, s := range symbols {
		if n := m.quotes[s]; n != nil && n.leases > 0 {
			n.leases--
		}
	}
	m.syncSubscription()
}

func (m *Market) insList() string {
	var list []string
	for s, n := range m.quotes {
		if n.leases > 0 {
			list = append(list, s)
		}
	}
	slices.Sort(list)
	return strings.Join(list, ",")
}

func (m *Market) syncSubscription() {
	list := m.insList()
	if list == m.sentInsList || m.closed {
		return
	}
	if err := m.route.send(subscribeQuote(list), nil); err == nil {
		m.sentInsList = list
	}
}

func subscribeQuote(list string) []byte {
	b, _ := json.Marshal(struct {
		Aid     string `json:"aid"`
		InsList string `json:"ins_list"`
	}{"subscribe_quote", list})
	return b
}

// down 实现 routeHandler：状态置 Stale，未完成的一次性请求失败。
func (m *Market) down(r *route, st Stamp, err error) Applied {
	m.log.Warn("market route down", zap.Uint32("route", r.id), zap.Uint64("generation", r.gen), zap.Error(err))
	ch := &marketChanges{errors: []RouteError{{Route: r.id, Generation: r.gen, Err: err}}}
	for id, c := range m.queries {
		c.finish(nil, ErrNotConnected)
		delete(m.queries, id)
	}
	for _, w := range m.windows {
		w.drop(ErrNotConnected)
	}
	m.sentInsList = ""
	j := m.newJournal(st)
	j.forced = true
	return m.finish(j, ch)
}

// up 实现 routeHandler：重放 subscribe_quote 与活动 set_chart，进入恢复屏障。
func (m *Market) up(r *route, st Stamp) Applied {
	m.log.Info("market route up", zap.Uint32("route", r.id), zap.Uint64("generation", r.gen))
	m.recovering = true
	m.mdhisSeen, m.echoInsList = false, ""
	for _, c := range m.charts {
		c.resetEcho()
	}
	m.syncSubscription()
	for _, w := range m.windows {
		if sw, ok := w.(interface{ resend() }); ok {
			sw.resend()
		}
	}
	j := m.newJournal(st)
	j.forced = true
	return m.finish(j, &marketChanges{})
}

// barrierReached 实现 Python MdReconnectHandler._is_all_received 的恢复条件。
func (m *Market) barrierReached() bool {
	if len(m.charts) > 0 && (!m.mdhisSeen || m.mdhis) {
		return false
	}
	for _, c := range m.charts {
		if !c.acked() || (c.left == -1 && c.right == -1) {
			return false
		}
		syms, dur := chartSeries(c)
		for _, s := range syms {
			if dur == 0 {
				if t := m.ticks[seriesKey{s, 0}]; t == nil || t.lastID == -1 || t.lastGen != m.route.gen {
					return false
				}
			} else if k := m.klines[seriesKey{s, dur}]; k == nil || k.lastID == -1 || k.lastGen != m.route.gen {
				return false
			}
		}
	}
	return m.echoInsList == m.sentInsList
}

func chartSeries(c *chartState) ([]string, int64) {
	list := strings.TrimPrefix(c.desired["ins_list"], "s:")
	var dur int64
	for _, ch := range strings.TrimPrefix(c.desired["duration"], "n:") {
		dur = dur*10 + int64(ch-'0')
	}
	var syms []string
	for _, s := range strings.Split(list, ",") {
		if s != "" {
			syms = append(syms, s)
		}
	}
	return syms, dur
}

// ---- 报价 ----

// QuoteRef 是一个合约报价的稳定句柄，持有独立的订阅 lease。
type QuoteRef struct {
	m      *Market
	node   *quoteNode
	closed bool
}

// QuoteRef 建立稳定报价句柄并订阅该合约；立即返回 Pending 句柄，不等待数据。
func (m *Market) QuoteRef(ctx context.Context, symbol string) (*QuoteRef, error) {
	if err := validateSymbol(symbol); err != nil {
		return nil, err
	}
	m.rt.mu.Lock()
	defer m.rt.mu.Unlock()
	if err := m.usable(); err != nil {
		return nil, err
	}
	m.lease([]string{symbol})
	return &QuoteRef{m: m, node: m.quotes[symbol]}, nil
}

// Symbol 返回合约代码。
func (q *QuoteRef) Symbol() string { return q.node.symbol }

// Snapshot 读取当前报价副本。
func (q *QuoteRef) Snapshot() QuoteSnapshot {
	q.m.rt.mu.RLock()
	defer q.m.rt.mu.RUnlock()
	return q.m.snapshotQuote(q.node)
}

// State 返回当前可用状态。
func (q *QuoteRef) State() DataState {
	q.m.rt.mu.RLock()
	defer q.m.rt.mu.RUnlock()
	return q.node.state
}

// Close 释放 lease；最后一个 lease 释放后退订。
func (q *QuoteRef) Close() error {
	q.m.rt.mu.Lock()
	defer q.m.rt.mu.Unlock()
	if q.closed {
		return nil
	}
	q.closed = true
	if !q.m.closed {
		q.m.release([]string{q.node.symbol})
	}
	return nil
}

func (m *Market) snapshotQuote(n *quoteNode) QuoteSnapshot {
	return QuoteSnapshot{Value: n.value, Valid: n.valid, State: n.state, Stamp: Stamp{Epoch: m.rt.epoch}}
}

// LookupQuote 读取本地已提交的报价，不发请求。
func (m *Market) LookupQuote(symbol string) (QuoteSnapshot, bool) {
	m.rt.mu.RLock()
	defer m.rt.mu.RUnlock()
	n := m.quotes[symbol]
	if n == nil || !n.exists {
		return QuoteSnapshot{}, false
	}
	return m.snapshotQuote(n), true
}

// QuoteSub 是一组合约的报价订阅。
type QuoteSub struct {
	m       *Market
	symbols map[string]bool
	list    []string
	mb      *mailbox[QuoteEvent]
}

// SubscribeQuotes 订阅报价；已就绪的合约立即排入一次基线事件。
func (m *Market) SubscribeQuotes(ctx context.Context, symbols ...string) (*QuoteSub, error) {
	if len(symbols) == 0 {
		return nil, invalid("no symbols")
	}
	set := map[string]bool{}
	for _, s := range symbols {
		if err := validateSymbol(s); err != nil {
			return nil, err
		}
		set[s] = true
	}
	list := make([]string, 0, len(set))
	for s := range set {
		list = append(list, s)
	}
	m.rt.mu.Lock()
	defer m.rt.mu.Unlock()
	if err := m.usable(); err != nil {
		return nil, err
	}
	sub := &QuoteSub{m: m, symbols: set, list: list, mb: newMailbox[QuoteEvent](m.cfg.MailboxSize)}
	m.lease(list)
	for _, s := range list {
		if n := m.quotes[s]; n.state == Ready {
			sub.mb.push(quoteEvent(n, Stamp{Epoch: m.rt.epoch}))
		}
	}
	m.quoteSubs[sub] = struct{}{}
	return sub, nil
}

func quoteEvent(n *quoteNode, st Stamp) QuoteEvent {
	return QuoteEvent{Quote: n.value, Valid: n.valid, State: n.state, Stamp: st}
}

// C 返回事件 channel；订阅终止后关闭。
func (s *QuoteSub) C() <-chan QuoteEvent { return s.mb.ch }

// Err 返回终止原因；正常 Close 为 nil。
func (s *QuoteSub) Err() error { return s.mb.error() }

// Close 结束订阅并释放 lease。
func (s *QuoteSub) Close() error {
	s.m.rt.mu.Lock()
	if _, ok := s.m.quoteSubs[s]; ok {
		delete(s.m.quoteSubs, s)
		s.m.release(s.list)
	}
	s.m.rt.mu.Unlock()
	s.mb.close(nil)
	return nil
}
