// Package risk 提供可选的本地报撤单规则，实现 core.CommandGuard。
//
// 计数口径是“本 Engine 在本地接受的申报”：首次绑定账户交易日时从零开始，
// 不从服务端委托/成交推测，远端拒单、撤单、成交都不回退额度；确认新交易日且账户完成基线后清零。
// 每个账户使用独立 Engine，不跨账户共享计数。
package risk

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// Rule 是一条本地规则。
type Rule interface {
	core.CommandGuard
	ID() string
}

// Engine 依次检查全部规则，全部通过后统一记账。
type Engine struct{ rules []Rule }

// New 校验规则配置（唯一 ID、非负额度、正频率、有限作用域）并创建 Engine。
func New(rules ...Rule) (*Engine, error) {
	seen := map[string]bool{}
	for _, r := range rules {
		if r == nil {
			return nil, errors.New("risk: nil rule")
		}
		id := r.ID()
		if id == "" || seen[id] {
			return nil, fmt.Errorf("risk: rule id %q empty or duplicated", id)
		}
		seen[id] = true
		if v, ok := r.(interface{ validate() error }); ok {
			if err := v.validate(); err != nil {
				return nil, fmt.Errorf("risk: rule %s: %w", id, err)
			}
		}
	}
	return &Engine{rules: slices.Clone(rules)}, nil
}

// Check 实现 core.CommandGuard：任一规则拒绝即返回，不记账。
func (e *Engine) Check(v core.RiskView, c core.TradeCommand) error {
	for _, r := range e.rules {
		if err := r.Check(v, c); err != nil {
			return err
		}
	}
	return nil
}

// OnAccepted 实现 core.CommandGuard。
func (e *Engine) OnAccepted(c core.TradeCommand) {
	for _, r := range e.rules {
		r.OnAccepted(c)
	}
}

// OnCommit 实现 core.CommandGuard。
func (e *Engine) OnCommit(v core.RiskView, u core.RiskUpdate) {
	for _, r := range e.rules {
		r.OnCommit(v, u)
	}
}

// dayCounter 按账户交易日维护计数：Ready 且交易日确认后绑定，新交易日清零一次。
type dayCounter struct {
	day    string
	counts map[string]int64
}

func (d *dayCounter) onCommit(u core.RiskUpdate) {
	if u.State != core.Ready || u.TradingDay == "" || u.TradingDay == d.day {
		return
	}
	if d.day != "" {
		clear(d.counts)
	}
	d.day = u.TradingDay
}

func (d *dayCounter) ready(v core.RiskView) error {
	if v.State() != core.Ready || d.day == "" || v.TradingDay() != d.day {
		return core.ErrRiskNotReady
	}
	return nil
}

func isOpen(c core.TradeCommand) bool {
	return c.Kind == core.CommandSubmit && c.Order.Offset == model.OffsetOpen
}

func toSet(list []string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, s := range list {
		m[s] = true
	}
	return m
}

// OpenCountLimit 限制指定合约各自累计本地接受的 OPEN 报单次数。Limit=0 表示禁止开仓。
type OpenCountLimit struct {
	Name    string
	Symbols []string
	Limit   int64

	set map[string]bool
	dayCounter
}

func (r *OpenCountLimit) ID() string { return r.Name }

func (r *OpenCountLimit) validate() error {
	if len(r.Symbols) == 0 || r.Limit < 0 {
		return errors.New("need symbols and non-negative limit")
	}
	r.set, r.counts = toSet(r.Symbols), map[string]int64{}
	return nil
}

func (r *OpenCountLimit) Check(v core.RiskView, c core.TradeCommand) error {
	if !isOpen(c) || !r.set[c.Symbol] {
		return nil
	}
	if err := r.ready(v); err != nil {
		return err
	}
	if r.counts[c.Symbol]+1 > r.Limit {
		return &core.RiskRejectedError{RuleID: r.Name, Reason: fmt.Sprintf("%s 开仓次数 %d 已达上限 %d", c.Symbol, r.counts[c.Symbol], r.Limit)}
	}
	return nil
}

func (r *OpenCountLimit) OnAccepted(c core.TradeCommand) {
	if isOpen(c) && r.set[c.Symbol] {
		r.counts[c.Symbol]++
	}
}

func (r *OpenCountLimit) OnCommit(_ core.RiskView, u core.RiskUpdate) { r.onCommit(u) }

// OpenVolumeLimit 限制指定合约累计本地接受的 OPEN 报单数量；Combined 时按合约组合计。
type OpenVolumeLimit struct {
	Name     string
	Symbols  []string
	Limit    int64
	Combined bool

	set map[string]bool
	dayCounter
}

func (r *OpenVolumeLimit) ID() string { return r.Name }

func (r *OpenVolumeLimit) validate() error {
	if len(r.Symbols) == 0 || r.Limit < 0 {
		return errors.New("need symbols and non-negative limit")
	}
	r.set, r.counts = toSet(r.Symbols), map[string]int64{}
	return nil
}

func (r *OpenVolumeLimit) key(symbol string) string {
	if r.Combined {
		return ""
	}
	return symbol
}

func (r *OpenVolumeLimit) Check(v core.RiskView, c core.TradeCommand) error {
	if !isOpen(c) || !r.set[c.Symbol] {
		return nil
	}
	if err := r.ready(v); err != nil {
		return err
	}
	used := r.counts[r.key(c.Symbol)]
	if used+c.Order.Volume > r.Limit {
		return &core.RiskRejectedError{RuleID: r.Name, Reason: fmt.Sprintf("开仓数量 %d+%d 超过上限 %d", used, c.Order.Volume, r.Limit)}
	}
	return nil
}

func (r *OpenVolumeLimit) OnAccepted(c core.TradeCommand) {
	if isOpen(c) && r.set[c.Symbol] {
		r.counts[r.key(c.Symbol)] += c.Order.Volume
	}
}

func (r *OpenVolumeLimit) OnCommit(_ core.RiskView, u core.RiskUpdate) { r.onCommit(u) }

// OrderRateLimit 限制指定交易所最近一秒本地接受的报单+撤单数量。时间窗不按交易日清空。
// 撤单无法确认交易所时返回 core.ErrRiskContextUnavailable，不跳过检查。
// 约束的是本地接受频率，不是线上发送频率。
type OrderRateLimit struct {
	Name      string
	Exchanges []string
	PerSecond int

	set   map[string]bool
	times map[string][]time.Time // 每个交易所按接受顺序保存的时间样本（最多 PerSecond 个）
}

func (r *OrderRateLimit) ID() string { return r.Name }

func (r *OrderRateLimit) validate() error {
	if len(r.Exchanges) == 0 || r.PerSecond <= 0 {
		return errors.New("need exchanges and positive PerSecond")
	}
	r.set, r.times = toSet(r.Exchanges), map[string][]time.Time{}
	return nil
}

func (r *OrderRateLimit) live(exchange string, now time.Time) []time.Time {
	ts := r.times[exchange]
	i := 0
	for i < len(ts) && now.Sub(ts[i]) >= time.Second {
		i++
	}
	return ts[i:]
}

func (r *OrderRateLimit) Check(_ core.RiskView, c core.TradeCommand) error {
	if c.Exchange == "" {
		return core.ErrRiskContextUnavailable
	}
	if !r.set[c.Exchange] {
		return nil
	}
	if n := len(r.live(c.Exchange, c.At)); n >= r.PerSecond {
		return &core.RiskRejectedError{RuleID: r.Name, Reason: fmt.Sprintf("%s 一秒内报撤单 %d 次已达上限 %d", c.Exchange, n, r.PerSecond)}
	}
	return nil
}

func (r *OrderRateLimit) OnAccepted(c core.TradeCommand) {
	if !r.set[c.Exchange] {
		return
	}
	ts := append(r.live(c.Exchange, c.At), c.At)
	if len(ts) > r.PerSecond {
		ts = ts[len(ts)-r.PerSecond:]
	}
	r.times[c.Exchange] = slices.Clip(ts)
}

func (r *OrderRateLimit) OnCommit(core.RiskView, core.RiskUpdate) {}
