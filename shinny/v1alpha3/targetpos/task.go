package targetpos

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/internal/tday"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/tqapi"
)

// Task 是一个账户合约的目标持仓任务。全部方法须在驱动 WaitUpdate 的 goroutine 调用。
type Task struct {
	api      *tqapi.API
	acc      *core.AccountHandle
	cfg      Config
	key      regKey
	exchange string
	groups   []string
	h        *tqapi.TaskHandle
	rng      *rand.Rand

	quote *core.QuoteRef
	req   *core.Request[[]model.Instrument]
	ins   *model.Instrument // 合约资料，取得并校验后非 nil

	version  uint64
	active   *round
	pending  *Ticket // 等待执行的最新目标
	orders   map[string]*child
	seen     map[string]bool // 已计入的成交 ID
	fills    []Fill
	stopping bool  // 已接受取消或致命错误，不再接收目标、不再下新单
	cause    error // 致命错误
	reported bool  // cause 已经通过 Poll 报告
	drainAt  time.Time
	depAt    time.Time // 当前依赖等待的期限；零值表示未在等待
	retryAt  time.Time // 报单可重试错误后的下次尝试时刻
	waitWhy  string
	close    *CloseResult
	closed   bool // 已确认结清并正常收尾
	detached bool // 被 tqapi 强制分离

	// 实盘与 paper 的交易所时间估计（同 Python _local_time_record）：最近行情时间 + 观察到它之后的本机流逝。
	lastDT   string
	lastDTNs int64
	lastSeen time.Time
}

// round 是一个目标版本的一次执行：按优先级逐组规划、报单、对账。
type round struct {
	t            *Ticket
	gi           int // 下一个待规划的组
	legs         []*leg
	long, short  int64 // 按自有成交推算的多空持仓
	startNet     int64
	traded       int64
	fills        []Fill
	reason       string
	planned      bool // 当前组已规划
	supersededBy uint64
}

// leg 是一组中的一项（Python InsertOrderUntilAllTradedTask）：同一时刻至多一笔在场子单，追价直到全部成交。
type leg struct {
	dir    model.Direction
	offset model.Offset
	left   int64 // 尚未成交的计划手数
	child  *child
	ended  bool // 因最小开仓手数尾差提前结束
}

// child 是一笔自有委托。
type child struct {
	ref       *core.OrderRef
	leg       *leg
	volume    int64
	price     float64
	filled    int64
	cancelled bool      // 本任务已请求撤单
	cancelAt  time.Time // 撤单失败后的重试时刻
	settled   bool
}

// SetTarget 设置目标净持仓（正为多头、负为空头、0 为空仓），返回该目标的票据。
// 与最新的未完成目标相同时返回同一票据。实际报撤单在之后的 WaitUpdate 中进行。
func (t *Task) SetTarget(volume int64) (*Ticket, error) {
	switch {
	case t.closed || t.detached:
		return nil, ErrTaskClosed
	case t.close != nil && !t.close.Confirmed:
		return nil, ErrUnconfirmed
	case t.stopping:
		return nil, ErrTaskStopping
	}
	if p := t.pending; p != nil {
		if p.target == volume {
			return p, nil
		}
	} else if r := t.active; r != nil && r.t.target == volume {
		return r.t, nil
	}
	t.version++
	q := newTicket(t.version, volume)
	if p := t.pending; p != nil {
		p.finish(Result{Outcome: Superseded, Settled: true, SupersededBy: q.version})
	}
	t.pending = q
	t.h.Wake()
	return q, nil
}

// Cancel 请求停止：不再接收目标、不再下新单，之后的 WaitUpdate 撤销自有在途单、对账并收尾。重复调用无效果。
func (t *Task) Cancel() {
	if t.closed || t.stopping {
		return
	}
	t.stopping = true
	t.h.Cancel()
}

// Done 报告任务是否已确认结清并正常收尾。
func (t *Task) Done() bool { return t.closed }

// CloseResult 返回收尾报告；尚未收尾时返回 ErrNotReady。
func (t *Task) CloseResult() (CloseResult, error) {
	if t.close == nil {
		return CloseResult{}, ErrNotReady
	}
	return *t.close, nil
}

// Trades 返回本任务自有成交的副本，按首次观察顺序只追加。
func (t *Task) Trades() []Fill { return slices.Clone(t.fills) }

// Snapshot 返回任务状态。
func (t *Task) Snapshot() Snapshot {
	s := Snapshot{StopRequested: t.stopping, WaitReason: t.waitWhy, OwnedOrders: t.unresolved()}
	if t.active != nil {
		s.ActiveVersion = t.active.t.version
	}
	if t.pending != nil {
		s.PendingVersion = t.pending.version
	}
	switch {
	case t.closed:
		s.Phase = PhaseClosed
	case t.close != nil:
		s.Phase = PhaseUnconfirmed
	case t.stopping:
		s.Phase = PhaseStopping
	case t.active != nil && t.replacing():
		s.Phase = PhaseReplacing
	case t.active != nil:
		s.Phase = PhaseRunning
	case t.pending != nil:
		s.Phase = PhaseWaiting
	default:
		s.Phase = PhaseIdle
	}
	return s
}

func (t *Task) replacing() bool { return t.cfg.ReplaceMode == Latest && t.pending != nil }

// runner 是注册到 tqapi 的私有任务，避免把 Poll 暴露为 Task 的公开方法。
type runner struct{ t *Task }

func (r *runner) Poll(in tqapi.TaskInput) (tqapi.TaskStep, error) {
	t := r.t
	if in.Stopping {
		t.stopping = true // Shutdown：与 Cancel 同一路径
	}
	changed := t.advance(in.Now)
	var err error
	if t.cause != nil && !t.reported {
		t.reported, err = true, t.cause
	}
	if t.closed {
		return tqapi.TaskStep{Done: true, Changed: true}, err
	}
	return tqapi.TaskStep{WatchData: true, WakeAt: t.nextCheck(), Changed: changed}, err
}

// OnDetach 实现 tqapi.TaskDetachHandler：强制结束时终结全部票据并释放资源，不报撤单。
func (r *runner) OnDetach(cause error) {
	t := r.t
	t.detached = true
	unresolved := t.unresolved()
	outcome := Failed
	if len(unresolved) > 0 {
		outcome = Unconfirmed
	}
	t.finishAll(outcome, errors.Join(t.cause, cause))
	t.close = &CloseResult{Confirmed: len(unresolved) == 0, Unresolved: unresolved, Cause: errors.Join(t.cause, cause)}
	t.release(len(unresolved) == 0)
}

// advance 是一次推进：先跟踪自有委托，再处理停止、替换，最后规划与报单。返回是否有可观察变化。
func (t *Task) advance(now time.Time) bool {
	changed := t.track(now)
	if t.stopping {
		return t.drain(now) || changed
	}
	if t.cause == nil && t.ins == nil {
		t.loadInstrument()
	}
	if t.cause != nil { // 致命错误：转入收尾
		t.stopping = true
		return t.drain(now) || changed
	}
	if r := t.active; r != nil && t.replacing() { // Latest：撤回当前轮，结清并对账后从实际持仓开始新目标
		if t.halt(r, now) && t.reconciled(r, now) {
			r.supersededBy = t.pending.version
			t.finishRound(Superseded, nil)
			t.h.Wake()
			changed = true
		}
		return changed
	}
	if t.active == nil && t.pending != nil {
		changed = t.start(now) || changed
	}
	if t.active != nil {
		changed = t.run(now) || changed
	}
	if t.cause != nil { // 本次推进中发现的致命错误：立即开始收尾
		t.stopping = true
		changed = t.drain(now) || changed
	}
	return changed
}

// track 读取自有在场委托：计入新成交，结清已终结的委托，并按 Python 规则追价。
func (t *Task) track(now time.Time) bool {
	changed := false
	q := t.quote.Snapshot().Value
	for _, c := range t.live() {
		trades := t.acc.TradesByOrder(c.ref.ID())
		slices.SortFunc(trades, func(a, b model.Trade) int {
			return cmp.Or(cmp.Compare(a.TradeDateTime, b.TradeDateTime), cmp.Compare(a.TradeID, b.TradeID))
		})
		for _, tr := range trades {
			if !t.seen[tr.TradeID] {
				t.seen[tr.TradeID] = true
				t.addFill(c, tr)
				changed = true
			}
		}
		o, _, reported := c.ref.Snapshot()
		if st, _ := c.ref.SendState(); st == core.CommandNotSent && !reported {
			t.fail(fmt.Errorf("targetpos: 报单 %s 未发送", c.ref.ID()))
			continue
		}
		if !reported {
			continue
		}
		if o.Status == model.OrderFinished && c.filled == o.VolumeOriginal-o.VolumeLeft {
			c.settled = true
			c.leg.child = nil
			if o.VolumeLeft != 0 && !c.cancelled {
				t.fail(fmt.Errorf("targetpos: 遇到错单: %s %s %s %d手 %v %s",
					t.cfg.Symbol, c.leg.dir, c.leg.offset, c.volume, c.price, o.LastMsg))
			}
			changed = true
			continue
		}
		if o.Status != model.OrderAlive {
			continue
		}
		if c.cancelled { // 已撤单；上次撤单失败的到期重试
			if !c.cancelAt.IsZero() && !now.Before(c.cancelAt) {
				t.cancelChild(c, now)
			}
			continue
		}
		p := t.cfg.Price.Price(c.leg.dir, q)
		worse := c.leg.dir == model.DirectionBuy && p > c.price || c.leg.dir == model.DirectionSell && p < c.price
		if worse && o.ExchangeOrderID != "" {
			t.cancelChild(c, now)
		}
	}
	return changed
}

func (t *Task) addFill(c *child, tr model.Trade) {
	signed := tr.Volume
	if tr.Direction == model.DirectionSell {
		signed = -signed
	}
	c.filled += tr.Volume
	c.leg.left -= tr.Volume
	r := t.active
	f := Fill{Version: r.t.version, Trade: tr, Signed: signed}
	t.fills = append(t.fills, f)
	r.fills = append(r.fills, f)
	r.traded += signed
	switch {
	case tr.Offset == model.OffsetOpen && signed > 0:
		r.long += tr.Volume
	case tr.Offset == model.OffsetOpen:
		r.short += tr.Volume
	case signed > 0: // 买平平空头
		r.short -= tr.Volume
	default:
		r.long -= tr.Volume
	}
}

// cancelChild 撤销一笔自有委托；可重试错误时在 retryInterval 后再试。
func (t *Task) cancelChild(c *child, now time.Time) {
	c.cancelled = true
	c.cancelAt = time.Time{}
	if err := t.acc.Cancel(context.Background(), c.ref.ID()); err != nil {
		c.cancelAt = now.Add(retryInterval)
	}
}

// halt 撤销本轮全部在场子单；全部结清时返回 true。
func (t *Task) halt(r *round, now time.Time) bool {
	done := true
	for _, c := range t.live() {
		if !c.cancelled {
			t.cancelChild(c, now)
		}
		done = false
	}
	return done
}

// live 返回尚未结清的自有委托（按报单顺序）。
func (t *Task) live() []*child {
	if t.active == nil {
		return nil
	}
	var out []*child
	for _, l := range t.active.legs {
		if l.child != nil {
			out = append(out, l.child)
		}
	}
	return out
}

func (t *Task) unresolved() []OrderSummary {
	var out []OrderSummary
	for _, c := range t.live() {
		o, _, _ := c.ref.Snapshot()
		out = append(out, OrderSummary{OrderID: c.ref.ID(), Direction: c.leg.dir, Offset: c.leg.offset,
			Volume: c.volume, Price: c.price, Filled: c.filled, Status: o.Status})
	}
	return out
}

// fail 记录第一个致命错误。
func (t *Task) fail(err error) {
	if t.cause == nil {
		t.cause = err
	}
}

// drain 是停止后的收尾：终结待办目标，撤销在场子单，结清后终结当前轮并关闭任务；超过 DrainTimeout 报告未确认。
func (t *Task) drain(now time.Time) bool {
	changed := false
	outcome := Cancelled
	if t.cause != nil {
		outcome = Failed
	}
	if p := t.pending; p != nil {
		p.finish(Result{Outcome: outcome, Settled: true, Cause: t.cause})
		t.pending, changed = nil, true
	}
	if t.drainAt.IsZero() {
		t.drainAt = now.Add(t.cfg.DrainTimeout)
	}
	if r := t.active; r != nil && !t.halt(r, now) {
		if t.close == nil && !now.Before(t.drainAt) {
			unresolved := t.unresolved()
			t.finishRound(Unconfirmed, t.cause)
			t.close = &CloseResult{Unresolved: unresolved, Cause: errors.Join(t.cause, ErrUnconfirmed)}
			changed = true
		}
		return changed
	}
	if t.active != nil {
		t.finishRound(outcome, t.cause)
	}
	t.close = &CloseResult{Confirmed: true, Cause: t.cause}
	t.release(true)
	t.closed = true
	return true
}

// finishRound 终结当前轮的票据（已因超时终结的保持原结果）。
func (t *Task) finishRound(o Outcome, cause error) {
	r := t.active
	if !r.t.Done() {
		net := r.startNet + r.traded
		r.t.finish(Result{Outcome: o, ObservedNet: net, Remaining: r.t.target - net, Settled: len(t.live()) == 0,
			SupersededBy: r.supersededBy, Traded: r.traded, Fills: r.fills, Reason: r.reason, Cause: cause})
	}
	if len(t.live()) == 0 {
		t.active = nil
	}
}

func (t *Task) finishAll(o Outcome, cause error) {
	if t.pending != nil {
		t.pending.finish(Result{Outcome: o, Settled: true, Cause: cause})
		t.pending = nil
	}
	if t.active != nil && !t.active.t.Done() {
		t.finishRound(o, cause)
	}
}

// release 释放行情引用与资料查询；confirmed 时注销单例登记，否则保留登记隔离该合约。
func (t *Task) release(confirmed bool) {
	t.quote.Close()
	if t.req != nil {
		t.req.Close()
	}
	if confirmed {
		t.unregister()
	}
}

// loadInstrument 推进合约资料查询；取得后按 Python 校验合约类型与最小开仓手数。
func (t *Task) loadInstrument() {
	if t.req == nil {
		t.req, _ = t.api.Market.RequestSymbolInfo(context.Background(), []string{t.cfg.Symbol})
		return
	}
	list, err := t.req.Result()
	if errors.Is(err, core.ErrNotReady) {
		return
	}
	t.req = nil
	if err != nil {
		return // 断线等，下次推进重新查询
	}
	if len(list) == 0 {
		t.fail(fmt.Errorf("targetpos: 合约 %s 不存在", t.cfg.Symbol))
		return
	}
	ins := list[0]
	minLimit, minMarket := ins.OpenMinLimitOrderVolume, ins.OpenMinMarketOrderVolume
	switch {
	case ins.InsClass == "COMBINE":
		t.fail(fmt.Errorf("targetpos: %s 类型为 %s，不能交易", t.cfg.Symbol, ins.InsClass))
	case !t.cfg.SupportOpenMinVolume && (minLimit > 1 || minMarket > 1):
		t.fail(fmt.Errorf("targetpos: 交易所规定 %s 最小市价开仓手数 (%d) 或最小限价开仓手数 (%d) 大于 1，"+
			"target_pos_task 默认不支持有最小开仓手数限制的合约，如果需要支持，请设置 SupportOpenMinVolume", t.cfg.Symbol, minMarket, minLimit))
	case minLimit > 1 && t.cfg.MaxVolume > 0 && (t.cfg.MinVolume < minLimit || t.cfg.MaxVolume < minLimit):
		t.fail(fmt.Errorf("targetpos: 交易所规定 %s 最小限价开仓手数 (%d) 大于 1，当前 MinVolume (%d) 或 MaxVolume (%d) 小于最小限价开仓手数",
			t.cfg.Symbol, minLimit, t.cfg.MinVolume, t.cfg.MaxVolume))
	default:
		t.ins = &ins
	}
}

// start 在依赖就绪且处于交易时段时开始执行最新目标（Python 每轮开始前等待交易时段）。
func (t *Task) start(now time.Time) bool {
	q := t.quote.Snapshot()
	switch {
	case t.ins == nil:
		t.wait(now, "等待合约资料")
		return false
	case t.acc.State() != core.Ready:
		t.wait(now, "等待账户就绪")
		return false
	case q.State != core.Ready || q.Value.Datetime == "":
		t.wait(now, "等待行情")
		return false
	case !t.cfg.Session.InSession(time.Unix(0, t.exchangeNow(now, q.Value)).In(tday.CST), *t.ins):
		t.depAt, t.waitWhy = time.Time{}, "不在可交易时间段内"
		return false
	}
	pos, _ := t.acc.Position(t.cfg.Symbol)
	t.active = &round{t: t.pending, long: pos.PosLong(), short: pos.PosShort(), startNet: pos.Pos()}
	t.pending, t.depAt, t.waitWhy = nil, time.Time{}, ""
	return true
}

// wait 进入或保持一次依赖等待；超过 DependencyTimeout 为致命错误。
func (t *Task) wait(now time.Time, why string) {
	t.waitWhy = why
	if t.depAt.IsZero() {
		t.depAt = now.Add(t.cfg.DependencyTimeout)
	} else if !now.Before(t.depAt) {
		t.fail(fmt.Errorf("targetpos: %s 超时 (%v)", why, t.cfg.DependencyTimeout))
	}
}

// exchangeNow 估计交易所时间：回测为虚拟时间；否则为最近行情时间加上观察到该行情以来的本机流逝。
func (t *Task) exchangeNow(now time.Time, q model.Quote) int64 {
	if t.api.Backtest != nil {
		return now.UnixNano()
	}
	if q.Datetime != t.lastDT {
		ts, err := time.ParseInLocation("2006-01-02 15:04:05.000000", q.Datetime, tday.CST)
		if err != nil {
			return now.UnixNano()
		}
		t.lastDT, t.lastDTNs, t.lastSeen = q.Datetime, ts.UnixNano(), now
	}
	return t.lastDTNs + int64(now.Sub(t.lastSeen))
}

// run 推进当前轮：本组全部结束且持仓与自有成交对上后规划下一组，没有下一组时终结票据；为无在场单的项报单。
func (t *Task) run(now time.Time) bool {
	r := t.active
	if r.planned && slices.ContainsFunc(r.legs, func(l *leg) bool { return !l.ended && (l.left > 0 || l.child != nil) }) {
		t.submit(r, now)
		return false
	}
	if !t.reconciled(r, now) {
		return false
	}
	pos, _ := t.acc.Position(t.cfg.Symbol)
	for r.gi < len(t.groups) {
		if o, ok := t.foreignOrder(); ok {
			t.fail(fmt.Errorf("targetpos: %s 存在非本任务的在场委托 %s，TargetPosTask 运行期间不允许外部下单", t.cfg.Symbol, o))
			return false
		}
		delta := r.t.target - (r.startNet + r.traded)
		group := t.groups[r.gi]
		r.gi++
		r.legs, r.planned = nil, true
		for _, o := range planGroup(group, delta, t.exchange, pos) {
			r.legs = append(r.legs, &leg{dir: o.dir, offset: o.offset, left: o.volume})
		}
		if len(r.legs) > 0 {
			t.submit(r, now)
			return true
		}
	}
	net := r.startNet + r.traded
	outcome := Reached
	if net != r.t.target {
		outcome = Partial
		if r.reason == "" {
			r.reason = "可平仓手数不足"
		}
	}
	t.finishRound(outcome, nil)
	if t.pending != nil {
		t.h.Wake() // FinishCurrent：继续执行最新目标
	}
	return true
}

// reconciled 报告持仓是否已反映本轮全部自有成交；持仓晚到时等待，长期不一致（外部成交）在 DependencyTimeout 后致命。
func (t *Task) reconciled(r *round, now time.Time) bool {
	pos, _ := t.acc.Position(t.cfg.Symbol)
	if pos.PosLong() != r.long || pos.PosShort() != r.short {
		t.wait(now, "等待持仓与成交对账")
		return false
	}
	t.depAt, t.waitWhy = time.Time{}, ""
	return true
}

// foreignOrder 返回一笔非本任务的在场委托。
func (t *Task) foreignOrder() (string, bool) {
	ex, ins, _ := strings.Cut(t.cfg.Symbol, ".")
	for id, o := range t.acc.Orders() {
		if o.Status == model.OrderAlive && o.ExchangeID == ex && o.InstrumentID == ins && t.orders[id] == nil {
			return id, true
		}
	}
	return "", false
}

// submit 为本组每个没有在场子单的项报出下一笔子单（Python InsertOrderUntilAllTradedTask._run）。
func (t *Task) submit(r *round, now time.Time) {
	if now.Before(t.retryAt) {
		return
	}
	t.retryAt = time.Time{}
	q := t.quote.Snapshot().Value
	for _, l := range r.legs {
		if l.child != nil || l.left <= 0 || l.ended {
			continue
		}
		if m := t.ins.OpenMinLimitOrderVolume; l.offset == model.OffsetOpen && m > 1 && l.left < m {
			l.ended = true
			r.reason = fmt.Sprintf("剩余开仓手数 %d 小于最小开仓手数 %d", l.left, m)
			continue
		}
		price := t.cfg.Price.Price(l.dir, q)
		if math.IsNaN(price) || math.IsInf(price, 0) {
			t.fail(errors.New("targetpos: 设置价格函数返回 nan，无法处理。请检查后重试。"))
			return
		}
		volume := t.childVolume(l.left)
		ref, err := t.acc.Submit(context.Background(), core.OrderRequest{Symbol: t.cfg.Symbol, Direction: l.dir,
			Offset: l.offset, Volume: volume, Price: core.LimitPrice(price)})
		if errors.Is(err, core.ErrQueueFull) || errors.Is(err, core.ErrNotConnected) || errors.Is(err, core.ErrRiskNotReady) {
			t.retryAt = now.Add(retryInterval)
			return
		}
		if err != nil {
			t.fail(fmt.Errorf("targetpos: 报单失败: %w", err))
			return
		}
		c := &child{ref: ref, leg: l, volume: volume, price: price}
		l.child = c
		t.orders[ref.ID()] = c
	}
}

// childVolume 返回下一笔子单手数：大单拆分取随机手数，并遵守交易所单笔限价单手数上下限。
func (t *Task) childVolume(left int64) int64 {
	v := left
	if mn, mx := t.cfg.MinVolume, t.cfg.MaxVolume; mx > 0 && left >= mx {
		v = mn + t.rng.Int64N(mx-mn+1)
	}
	ins := t.ins
	if mx := ins.MaxLimitOrderVolume; ins.Valid.Has(model.InstrumentMaxLimitOrderVolume) && mx > 0 && v > mx {
		v = mx
		if mn := ins.MinLimitOrderVolume; ins.Valid.Has(model.InstrumentMinLimitOrderVolume) && mn > 0 && left-v > 0 && left-v < mn {
			v = max(1, left-mn)
		}
	}
	return v
}

// nextCheck 返回最早的有效期限：撤单重试，以及运行中的依赖等待与报单重试或停止后的收尾期限。
func (t *Task) nextCheck() time.Time {
	var next time.Time
	add := func(at time.Time) {
		if !at.IsZero() && (next.IsZero() || at.Before(next)) {
			next = at
		}
	}
	for _, c := range t.live() {
		add(c.cancelAt)
	}
	switch {
	case !t.stopping:
		add(t.depAt)
		add(t.retryAt)
	case t.close == nil:
		add(t.drainAt)
	}
	return next
}
