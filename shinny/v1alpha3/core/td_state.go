package core

import (
	"errors"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// ErrConflictingTrade 表示同一成交身份出现不同的必需字段，整包拒绝。
var ErrConflictingTrade = errors.New("tqsdk: conflicting trade fact")

// tradeRequired 是形成完整成交事实所需的字段；其余字段可以晚到补齐。
var tradeRequired = func() model.TradeFields {
	var f model.TradeFields
	for _, id := range []model.TradeField{model.TradeOrderID, model.TradeExchangeID, model.TradeInstrumentID,
		model.TradeDirection, model.TradePrice, model.TradeVolume, model.TradeTradeDateTime} {
		f.Add(id)
	}
	return f
}()

func tradeComplete(t *model.Trade) bool { return t.Valid.And(tradeRequired) == tradeRequired }

type objBefore[T any] struct {
	value  T
	exists bool
}

// tdJournal 记录一次交易提交的期初值与控制结果。
type tdJournal struct {
	accounts    map[*objNode[model.Account]]objBefore[model.Account]
	positions   map[*objNode[model.Position]]objBefore[model.Position]
	orders      map[*objNode[model.Order]]objBefore[model.Order]
	riskRules   map[*objNode[model.RemoteRiskRule]]objBefore[model.RemoteRiskRule]
	riskData    map[*objNode[model.RemoteRiskData]]objBefore[model.RemoteRiskData]
	newTrades   []model.Trade
	notifies    []model.Notify
	state       bool // 账户状态改变
	stateErr    error
	meta        bool // 会话元信息改变
	becameReady bool
	reset       bool // 成交集合需要重建基线（作用域切换）
	errs        []RouteError
}

func newTdJournal() *tdJournal {
	return &tdJournal{
		accounts:  map[*objNode[model.Account]]objBefore[model.Account]{},
		positions: map[*objNode[model.Position]]objBefore[model.Position]{},
		orders:    map[*objNode[model.Order]]objBefore[model.Order]{},
		riskRules: map[*objNode[model.RemoteRiskRule]]objBefore[model.RemoteRiskRule]{},
		riskData:  map[*objNode[model.RemoteRiskData]]objBefore[model.RemoteRiskData]{},
	}
}

// apply 先预检成交冲突（失败整包不安装），再按序安装。
func (a *AccountHandle) apply(b *tdBatch, st Stamp) (Applied, error) {
	if err := a.checkTrades(b); err != nil {
		return Applied{}, err
	}
	j := newTdJournal()
	gen := a.route.gen
	for i := range b.ops {
		op := &b.ops[i]
		switch op.kind {
		case tdAccount:
			applyObj(j.accounts, a, a.accounts, accountSchema, func(v *model.Account) *model.AccountFields { return &v.Valid }, op, gen)
		case tdPosition:
			applyObj(j.positions, a, a.positions, positionSchema, func(v *model.Position) *model.PositionFields { return &v.Valid }, op, gen)
			if !op.del {
				a.seen[op.key] = true
			}
		case tdOrder:
			applyObj(j.orders, a, a.orders, orderSchema, func(v *model.Order) *model.OrderFields { return &v.Valid }, op, gen)
		case tdRiskRule:
			applyObj(j.riskRules, a, a.riskRules, riskRuleSchema, func(v *model.RemoteRiskRule) *model.RemoteRiskRuleFields { return &v.Valid }, op, gen)
		case tdRiskData:
			applyObj(j.riskData, a, a.riskData, riskDataSchema, func(v *model.RemoteRiskData) *model.RemoteRiskDataFields { return &v.Valid }, op, gen)
		case tdTrade:
			a.applyTrade(j, op)
		case tdNotify:
			var n model.Notify
			notifySchema.reset(&n)
			n.ID = op.key
			notifySchema.apply(&n, &n.Valid, op.p.(*patch[model.Notify, model.NotifyField]))
			j.notifies = append(j.notifies, n)
		case tdSession:
			if op.s != "" && op.s != a.session.UserID {
				a.session.UserID = op.s
				j.meta = true
			}
			if op.s2 != "" {
				a.setScope(j, op.s2)
			}
		case tdTradingDay:
			a.setScope(j, op.s)
		case tdPositionReady:
			a.posKnown, a.posReady = true, op.b
			j.meta = true
		case tdMoreData:
			a.moreKnown, a.more = true, op.b
			switch {
			case op.b && a.state == Ready: // 已就绪后收到 more=true：新的初始化周期
				a.state = Pending
				clear(a.seen)
				j.state = true
			case !op.b && a.state == Pending:
				a.state = Ready
				j.state, j.becameReady = true, true
			}
		}
	}
	return a.finish(j, st), nil
}

// applyObj 安装一个可变对象的 patch；新建、删除后重建或新代次首次触及时从干净内容开始。
func applyObj[T any, F ~uint16](touched map[*objNode[T]]objBefore[T], a *AccountHandle, m map[string]*objNode[T],
	sc *schema[T, F], valid func(*T) *model.Fields[F], op *tdOp, gen uint64) {
	n := nodeOf(a, m, op.key)
	if _, ok := touched[n]; !ok {
		touched[n] = objBefore[T]{value: n.value, exists: current(a, n)}
	}
	if op.del {
		sc.reset(&n.value)
		*valid(&n.value) = model.Fields[F]{}
		n.exists, n.gen = false, gen
		return
	}
	if !n.exists || n.gen != gen {
		sc.reset(&n.value)
		*valid(&n.value) = model.Fields[F]{}
		n.gen = gen
	}
	sc.apply(&n.value, valid(&n.value), op.p.(*patch[T, F]))
	n.exists = true
}

func (a *AccountHandle) setScope(j *tdJournal, day string) {
	if day == "" || day == a.scope {
		return
	}
	if a.scope != "" { // 业务作用域切换：成交集合以新基线表达，不发布删除
		clear(a.trades)
		clear(a.partials)
		j.reset = true
	}
	a.scope, a.session.TradingDay = day, day
	j.meta = true
}

// applyTrade 合并成交记录；必需字段齐全后成为不可变事实，只允许补齐可选字段。
func (a *AccountHandle) applyTrade(j *tdJournal, op *tdOp) {
	if op.del {
		return // 成交是事实，服务端删除只影响视图，不发布 TradeDelete
	}
	p := op.p.(*patch[model.Trade, model.TradeField])
	if fact, ok := a.trades[op.key]; ok {
		tradeSchema.apply(&fact, &fact.Valid, p)
		a.trades[op.key] = fact // 预检已保证必需字段未变
		return
	}
	t := a.partials[op.key]
	if t == nil {
		t = &model.Trade{}
		tradeSchema.reset(t)
		a.partials[op.key] = t
	}
	tradeSchema.apply(t, &t.Valid, p)
	if tradeComplete(t) {
		if !t.Valid.Has(model.TradeTradeID) {
			t.TradeID = op.key
		}
		a.trades[op.key] = *t
		delete(a.partials, op.key)
		if a.state == Ready {
			j.newTrades = append(j.newTrades, *t)
		}
	}
}

// checkTrades 在工作副本上模拟本包的成交合并，发现必需字段冲突即拒绝整包。
func (a *AccountHandle) checkTrades(b *tdBatch) error {
	facts := map[string]model.Trade{}
	for i := range b.ops {
		op := &b.ops[i]
		if op.kind != tdTrade || op.del {
			continue
		}
		p := op.p.(*patch[model.Trade, model.TradeField])
		fact, ok := facts[op.key]
		if !ok {
			fact, ok = a.trades[op.key]
		}
		if !ok {
			var t model.Trade
			if part := a.partials[op.key]; part != nil {
				t = *part
			} else {
				tradeSchema.reset(&t)
			}
			tradeSchema.apply(&t, &t.Valid, p)
			if tradeComplete(&t) {
				facts[op.key] = t
			}
			continue
		}
		cand := fact
		tradeSchema.apply(&cand, &cand.Valid, p)
		if !tradeComplete(&cand) || !tradeSchema.sameOn(&fact, &cand, tradeRequired) {
			return ErrConflictingTrade
		}
		facts[op.key] = cand
	}
	return nil
}

// finish 计算净变化、就绪基线与事件；在写锁内调用。
func (a *AccountHandle) finish(j *tdJournal, st Stamp) Applied {
	if j.becameReady { // 完整周期后清理本周期未出现的持仓（含旧代次残留）；已删除的不重复删除
		for key, n := range a.positions {
			if n.exists && !a.seen[key] {
				if _, ok := j.positions[n]; !ok {
					j.positions[n] = objBefore[model.Position]{value: n.value, exists: true}
				}
				positionSchema.reset(&n.value)
				n.value.Valid = model.PositionFields{}
				n.exists = false
			}
		}
		j.reset = true
	}
	ch := &tradingChanges{errors: j.errs}
	var events []TradeEvent
	ready := a.state == Ready && !j.becameReady
	ev := func(e TradeEvent) {
		e.AccountID, e.Scope, e.Stamp = a.cfg.ID, a.scope, st
		events = append(events, e)
	}
	for i := range j.notifies {
		n := j.notifies[i]
		ev(TradeEvent{Kind: TradeEventNotify, Notify: &n})
	}
	if len(j.notifies) > 0 {
		ch.notifies = map[uint64][]model.Notify{a.id: j.notifies}
	}
	if j.becameReady {
		snap := a.snapshot(st)
		ev(TradeEvent{Kind: TradeEventSnapshot, Snapshot: &snap})
	}
	if j.state || j.meta {
		ev(TradeEvent{Kind: TradeEventState, State: &TradeStateEvent{State: a.state, Session: a.session, Scope: a.scope, Err: j.stateErr}})
		ch.states = map[uint64]bool{a.id: true}
	}

	gen := a.gen()
	ch.accounts = objChanges(j.accounts, a, accountSchema, func(v *model.Account) model.AccountFields { return v.Valid }, gen, ready,
		func(n *objNode[model.Account], del bool) TradeEvent {
			return TradeEvent{Kind: TradeEventAccount, Account: &AccountEvent{Key: n.key, Value: n.value, Deleted: del}}
		}, ev)
	ch.positions = objChanges(j.positions, a, positionSchema, func(v *model.Position) model.PositionFields { return v.Valid }, gen, ready,
		func(n *objNode[model.Position], del bool) TradeEvent {
			return TradeEvent{Kind: TradeEventPosition, Position: &PositionEvent{Key: n.key, Value: n.value, Deleted: del}}
		}, ev)
	ch.orders = objChanges(j.orders, a, orderSchema, func(v *model.Order) model.OrderFields { return v.Valid }, gen, ready,
		func(n *objNode[model.Order], del bool) TradeEvent {
			return TradeEvent{Kind: TradeEventOrder, Order: &OrderEvent{Key: n.key, Value: n.value, Deleted: del}}
		}, ev)
	ch.riskRules = objChanges(j.riskRules, a, riskRuleSchema, func(v *model.RemoteRiskRule) model.RemoteRiskRuleFields { return v.Valid }, gen, ready,
		func(n *objNode[model.RemoteRiskRule], del bool) TradeEvent {
			return TradeEvent{Kind: TradeEventRemoteRiskRule, RemoteRiskRule: &RemoteRiskRuleEvent{Exchange: n.key, Value: n.value, Deleted: del}}
		}, ev)
	ch.riskData = objChanges(j.riskData, a, riskDataSchema, func(v *model.RemoteRiskData) model.RemoteRiskDataFields { return v.Valid }, gen, ready,
		func(n *objNode[model.RemoteRiskData], del bool) TradeEvent {
			return TradeEvent{Kind: TradeEventRemoteRiskData, RemoteRiskData: &RemoteRiskDataEvent{Symbol: n.key, Value: n.value, Deleted: del}}
		}, ev)

	switch {
	case j.reset:
		ch.trades = map[uint64]*TradesChange{a.id: {Reset: true}}
	case len(j.newTrades) > 0:
		ch.trades = map[uint64]*TradesChange{a.id: {Added: j.newTrades}}
	}
	if ready {
		for i := range j.newTrades {
			t := j.newTrades[i]
			ev(TradeEvent{Kind: TradeEventTrade, Trade: &t})
		}
	}

	ch.riskReqs = a.observeRiskRequests(j, st)
	if a.guard != nil {
		orderIDs := make([]string, 0, len(j.orders))
		for n := range j.orders {
			orderIDs = append(orderIDs, n.key)
		}
		tradeIDs := make([]string, 0, len(j.newTrades))
		for _, t := range j.newTrades {
			tradeIDs = append(tradeIDs, t.TradeID)
		}
		a.guard.OnCommit(RiskView{a}, RiskUpdate{Stamp: st, State: a.state, TradingDay: a.scope,
			Baseline: j.becameReady, OrderIDs: orderIDs, TradeIDs: tradeIDs})
	}

	var subs []*TradeEventSub
	for s := range a.subs {
		subs = append(subs, s)
	}
	return Applied{
		Changed: !ch.empty() || len(events) > 0,
		Changes: ch,
		Publish: func() {
			for _, s := range subs {
				for _, e := range events {
					if !s.mb.push(e) {
						break
					}
				}
			}
		},
	}
}

// objChanges 计算一类对象的净变化；账户已就绪时为有业务变化的对象生成事件。
func objChanges[T any, F ~uint16](touched map[*objNode[T]]objBefore[T], a *AccountHandle, sc *schema[T, F],
	valid func(*T) model.Fields[F], gen uint64, ready bool,
	event func(*objNode[T], bool) TradeEvent, emit func(TradeEvent)) map[uint64]ObjectChange[F] {
	if len(touched) == 0 {
		return nil
	}
	out := map[uint64]ObjectChange[F]{}
	for n, b := range touched {
		now := current(a, n)
		var c ObjectChange[F]
		c.Created = !b.exists && now
		c.Deleted = b.exists && !now
		switch {
		case b.exists && now:
			c.Fields = sc.changed(&b.value, valid(&b.value), &n.value, valid(&n.value))
		case now:
			c.Fields = valid(&n.value)
		}
		if !c.business() {
			continue
		}
		out[n.id] = c
		if ready {
			emit(event(n, c.Deleted))
		}
	}
	return out
}
