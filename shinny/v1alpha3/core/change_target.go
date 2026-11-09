package core

import (
	"fmt"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// ChangeTarget 是可用于 IsChanging 的对象身份；只由 SDK 的 Ref、窗口和行选择值实现。
type ChangeTarget interface {
	changeTarget() targetKey
}

type targetKind uint8

const (
	targetNone targetKind = iota
	targetQuote
	targetKlines
	targetTicks
	targetAligned
	targetAccount
	targetPosition
	targetOrder
	targetTrades
)

func (k targetKind) String() string {
	return [...]string{"nil", "Quote", "Klines", "Ticks", "AlignedKlines", "Account", "Position", "Order", "Trades"}[k]
}

type rowSelection uint8

const (
	wholeObject rowSelection = iota
	fixedRow
	latestRow
)

type targetKey struct {
	runtime uint64
	comp    uint64
	obj     uint64 // 稳定节点或逻辑窗口身份，不是 Ref 地址
	kind    targetKind
	sel     rowSelection
	row     int64
}

func (q *QuoteRef) changeTarget() targetKey {
	if q == nil {
		return targetKey{}
	}
	return targetKey{runtime: q.m.rt.id, comp: q.m.id, obj: q.node.id, kind: targetQuote}
}

func (w *seriesWindow[T, F]) targetKind() targetKind {
	if w.tick {
		return targetTicks
	}
	return targetKlines
}

func (w *seriesWindow[T, F]) changeTarget() targetKey {
	if w == nil {
		return targetKey{}
	}
	return targetKey{runtime: w.m.rt.id, comp: w.m.id, obj: w.id, kind: w.targetKind()}
}

// SeriesRowTarget 选择窗口中的一行用于变化查询；不持有订阅或状态副本。
type SeriesRowTarget struct{ key targetKey }

func (t SeriesRowTarget) changeTarget() targetKey { return t.key }

// Row 返回固定业务行 ID 的变化查询目标（不是数组下标）。
func (w *seriesWindow[T, F]) Row(id int64) SeriesRowTarget {
	k := w.changeTarget()
	k.sel, k.row = fixedRow, id
	return SeriesRowTarget{k}
}

// LatestRow 返回窗口末行位置的变化查询目标；在被查询的提交中解析，不回读当前窗口。
func (w *seriesWindow[T, F]) LatestRow() SeriesRowTarget {
	k := w.changeTarget()
	k.sel = latestRow
	return SeriesRowTarget{k}
}

func (w *AlignedKlines) changeTarget() targetKey {
	if w == nil {
		return targetKey{}
	}
	return targetKey{runtime: w.m.rt.id, comp: w.m.id, obj: w.id, kind: targetAligned}
}

func (r *AccountRef) changeTarget() targetKey {
	if r == nil {
		return targetKey{}
	}
	return targetKey{runtime: r.a.t.rt.id, comp: r.a.t.id, obj: r.node.id, kind: targetAccount}
}

func (r *PositionRef) changeTarget() targetKey {
	if r == nil {
		return targetKey{}
	}
	return targetKey{runtime: r.a.t.rt.id, comp: r.a.t.id, obj: r.node.id, kind: targetPosition}
}

func (r *OrderRef) changeTarget() targetKey {
	if r == nil {
		return targetKey{}
	}
	return targetKey{runtime: r.a.t.rt.id, comp: r.a.t.id, obj: r.node.id, kind: targetOrder}
}

func (r *TradesRef) changeTarget() targetKey {
	if r == nil {
		return targetKey{}
	}
	return targetKey{runtime: r.a.t.rt.id, comp: r.a.t.id, obj: r.a.id, kind: targetTrades}
}

// IsChanging 报告 target 在提交 c 中是否有业务净变化；fields 为协议 snake_case 名称，多个为 OR，
// 为空表示整个对象。纯状态变化（Pending/Ready/Stale）不算业务变化，可用详细 Change 查询。
//
// 使用错误会 panic：nil 目标、未知或不属于目标类型的字段名、负行 ID、跨 Runtime 混用。
// 全部字段先校验，零值 Commit 或无关组件的提交对合法目标返回 false。
func IsChanging(c Commit, target ChangeTarget, fields ...string) bool {
	if target == nil {
		panic("tqsdk: IsChanging nil target")
	}
	k := target.changeTarget()
	if k.kind == targetNone {
		panic("tqsdk: IsChanging nil or zero target")
	}
	if k.sel == fixedRow && k.row < 0 {
		panic(fmt.Sprintf("tqsdk: IsChanging %s row id %d is negative", k.kind, k.row))
	}
	match := fieldMatcher(k.kind, fields)
	if c.runtimeID == 0 {
		return false
	}
	if c.runtimeID != k.runtime {
		panic(fmt.Sprintf("tqsdk: IsChanging %s target belongs to another runtime", k.kind))
	}
	switch ch := c.changesFor(k.comp).(type) {
	case *marketChanges:
		switch k.kind {
		case targetQuote:
			q := ch.quotes[k.obj]
			return match.object(q.Fields.Any(), q.Created || q.Deleted, func(m any) bool { return q.Fields.And(m.(model.QuoteFields)).Any() })
		case targetKlines:
			sc, _ := ch.series[k.obj].(*KlineChange)
			return seriesChanging(sc, k, match)
		case targetTicks:
			sc, _ := ch.series[k.obj].(*TickChange)
			return seriesChanging(sc, k, match)
		case targetAligned:
			a := ch.aligned[k.obj]
			return a != nil && (len(a.Rows) > 0 || a.KeysChanged)
		}
	case *tradingChanges:
		switch k.kind {
		case targetAccount:
			o := ch.accounts[k.obj]
			return match.object(o.Fields.Any(), o.Created || o.Deleted, func(m any) bool { return o.Fields.And(m.(model.AccountFields)).Any() })
		case targetPosition:
			o := ch.positions[k.obj]
			return match.object(o.Fields.Any(), o.Created || o.Deleted, func(m any) bool { return o.Fields.And(m.(model.PositionFields)).Any() })
		case targetOrder:
			o := ch.orders[k.obj]
			return match.object(o.Fields.Any(), o.Created || o.Deleted, func(m any) bool { return o.Fields.And(m.(model.OrderFields)).Any() })
		case targetTrades:
			t := ch.trades[k.obj]
			return t != nil && (t.Reset || len(t.Added) > 0)
		}
	}
	return false
}

// matcher 是已校验的字段集合；fields 为 nil 表示整个对象。
type matcher struct{ fields any }

func (m matcher) object(fieldChanged, existence bool, has func(any) bool) bool {
	if m.fields == nil {
		return fieldChanged || existence
	}
	return has(m.fields)
}

func fieldMatcher(kind targetKind, names []string) matcher {
	if len(names) == 0 {
		return matcher{}
	}
	switch kind {
	case targetQuote:
		return matcher{parseFields(kind, names, model.QuoteFieldByName)}
	case targetKlines:
		return matcher{parseFields(kind, names, model.KlineFieldByName)}
	case targetTicks:
		return matcher{parseFields(kind, names, model.TickFieldByName)}
	case targetAccount:
		return matcher{parseFields(kind, names, model.AccountFieldByName)}
	case targetPosition:
		return matcher{parseFields(kind, names, model.PositionFieldByName)}
	case targetOrder:
		return matcher{parseFields(kind, names, model.OrderFieldByName)}
	}
	panic(fmt.Sprintf("tqsdk: IsChanging %s does not accept field names %q", kind, names))
}

func parseFields[F ~uint16](kind targetKind, names []string, byName func(string) (F, bool)) model.Fields[F] {
	var set model.Fields[F]
	for _, n := range names {
		f, ok := byName(n)
		if !ok {
			panic(fmt.Sprintf("tqsdk: IsChanging unknown %s field %q", kind, n))
		}
		set.Add(f)
	}
	return set
}

func seriesChanging[T any, F ~uint16](c *SeriesChange[T, F], k targetKey, m matcher) bool {
	if c == nil {
		return false
	}
	rowHas := func(r RowChange[F]) bool {
		if m.fields == nil {
			return r.Fields.Any() || r.Created || r.Deleted
		}
		return r.Fields.And(m.fields.(model.Fields[F])).Any()
	}
	find := func(id int64) (RowChange[F], bool) {
		for _, r := range c.Rows {
			if r.ID == id {
				return r, true
			}
		}
		return RowChange[F]{}, false
	}
	switch k.sel {
	case fixedRow:
		r, ok := find(k.row)
		return ok && rowHas(r)
	case latestRow:
		bID, bOK := c.lastBefore.id, c.lastBefore.known && c.lastBefore.present
		aID, aOK := c.lastAfter.id, c.lastAfter.known && c.lastAfter.present
		switch {
		case !bOK && !aOK:
			return false
		case bOK != aOK || bID != aID: // 末行出现、消失或身份切换
			return true
		}
		r, ok := find(aID)
		return ok && rowHas(r)
	}
	for _, r := range c.Rows {
		if rowHas(r) {
			return true
		}
	}
	return m.fields == nil && c.WindowMoved
}
