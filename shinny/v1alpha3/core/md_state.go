package core

import (
	"encoding/json/jsontext"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// mdJournal 记录一次提交中首次触及的对象与行的期初值，用于计算净变化。
type mdJournal struct {
	st      Stamp
	gen     uint64
	quotes  map[*quoteNode]quoteBefore
	klines  map[*series[model.Kline, model.KlineField]]*seriesTouch[model.Kline, model.KlineField]
	ticks   map[*series[model.Tick, model.TickField]]*seriesTouch[model.Tick, model.TickField]
	charts  map[*chartState]bool
	queries map[*queryCall]bool
	forced  bool // 状态或屏障改变：评估全部窗口
}

type quoteBefore struct {
	value  model.Quote
	valid  model.QuoteFields
	exists bool
	state  DataState
}

type seriesTouch[T any, F ~uint16] struct {
	rows    map[int64]rowBefore[T, F]
	binding bool
}

type rowBefore[T any, F ~uint16] struct {
	present bool
	value   T
	valid   model.Fields[F]
}

func (t *seriesTouch[T, F]) touchRow(s *series[T, F], id int64) {
	if _, ok := t.rows[id]; ok {
		return
	}
	if r := s.rows[id]; r != nil {
		t.rows[id] = rowBefore[T, F]{present: true, value: r.value, valid: r.valid}
	} else {
		t.rows[id] = rowBefore[T, F]{}
	}
}

// presentBefore 返回 id 在提交前是否存在。
func (t *seriesTouch[T, F]) presentBefore(s *series[T, F], id int64) bool {
	if t != nil {
		if b, ok := t.rows[id]; ok {
			return b.present
		}
	}
	return s.rows[id] != nil
}

func (m *Market) newJournal(st Stamp) *mdJournal {
	return &mdJournal{
		st: st, gen: m.route.gen,
		quotes:  map[*quoteNode]quoteBefore{},
		klines:  map[*series[model.Kline, model.KlineField]]*seriesTouch[model.Kline, model.KlineField]{},
		ticks:   map[*series[model.Tick, model.TickField]]*seriesTouch[model.Tick, model.TickField]{},
		charts:  map[*chartState]bool{},
		queries: map[*queryCall]bool{},
	}
}

func touchesOf[T any, F ~uint16](j *mdJournal, k *seriesKind[T, F]) map[*series[T, F]]*seriesTouch[T, F] {
	switch any(k).(type) {
	case *seriesKind[model.Kline, model.KlineField]:
		return any(j.klines).(map[*series[T, F]]*seriesTouch[T, F])
	default:
		return any(j.ticks).(map[*series[T, F]]*seriesTouch[T, F])
	}
}

func touchSeries[T any, F ~uint16](j *mdJournal, k *seriesKind[T, F], s *series[T, F]) *seriesTouch[T, F] {
	m := touchesOf(j, k)
	t := m[s]
	if t == nil {
		t = &seriesTouch[T, F]{rows: map[int64]rowBefore[T, F]{}}
		m[s] = t
	}
	return t
}

// apply 在写锁内安装一个已解码的 envelope。解码已完成全部类型校验，安装不会失败。
func (m *Market) apply(b *mdBatch, st Stamp) Applied {
	j := m.newJournal(st)
	for i := range b.quotes {
		m.applyQuote(j, &b.quotes[i])
	}
	for i := range b.klines {
		applySeriesOp(m, j, klineKind, &b.klines[i])
	}
	for i := range b.ticks {
		applySeriesOp(m, j, tickKind, &b.ticks[i])
	}
	for i := range b.charts {
		op := &b.charts[i]
		if c := m.charts[op.id]; c != nil {
			if op.del {
				c.resetEcho()
			} else {
				c.applyOp(op)
			}
			j.charts[c] = true
		}
	}
	for i := range b.symbols {
		m.applySymbol(j, &b.symbols[i])
	}
	if b.insList != nil {
		m.echoInsList = *b.insList
	}
	if b.mdhis != nil {
		m.mdhis, m.mdhisSeen = *b.mdhis, true
	}
	return m.finish(j, &marketChanges{})
}

func (m *Market) applyQuote(j *mdJournal, op *quoteOp) {
	touch := func(n *quoteNode) {
		if _, ok := j.quotes[n]; !ok {
			j.quotes[n] = quoteBefore{value: n.value, valid: n.valid, exists: n.exists, state: n.state}
		}
	}
	clearNode := func(n *quoteNode) {
		quoteSchema.reset(&n.value)
		n.valid = model.QuoteFields{}
		n.exists = false
	}
	remove := func(n *quoteNode) {
		clearNode(n)
		n.deleted, n.gen = true, j.gen
	}
	if op.symbol == "" { // 根上的 quotes: null 删除全部报价；单个报价与字段的 null 在解码时已丢弃
		for _, n := range m.quotes {
			touch(n)
			remove(n)
		}
		return
	}
	n := m.quoteNode(op.symbol)
	touch(n)
	if !n.exists || n.gen != j.gen { // 新建或新连接代次首次触及：从干净内容开始
		clearNode(n)
		n.gen = j.gen
	}
	quoteSchema.apply(&n.value, &n.valid, &op.p)
	n.exists, n.deleted = true, false
}

func applySeriesOp[T any, F ~uint16](m *Market, j *mdJournal, k *seriesKind[T, F], op *seriesOp[T, F]) {
	wipe := func(s *series[T, F]) {
		t := touchSeries(j, k, s)
		for id := range s.rows {
			t.touchRow(s, id)
		}
		clear(s.rows)
		s.lastID = -1
		if s.binding != nil {
			s.binding = nil
			t.binding = true
		}
	}
	if op.kind == opSeriesDelete {
		for key, s := range k.store(m) {
			if op.key.symbol == "" || (key.symbol == op.key.symbol && (op.allDur || key.dur == op.key.dur)) {
				wipe(s)
			}
		}
		return
	}
	s := k.get(m, op.key, true)
	t := touchSeries(j, k, s)
	switch op.kind {
	case opLastID:
		s.lastID, s.lastGen = op.id, j.gen
	case opRow:
		t.touchRow(s, op.id)
		r := s.rows[op.id]
		if r == nil {
			r = &row[T, F]{value: k.empty}
			s.rows[op.id] = r
		}
		k.schema.apply(&r.value, &r.valid, &op.p)
	case opRowDelete:
		t.touchRow(s, op.id)
		delete(s.rows, op.id)
	case opRowsDelete:
		for id := range s.rows {
			t.touchRow(s, id)
		}
		clear(s.rows)
	case opBinding:
		t.binding = true
		if s.binding == nil {
			s.binding = map[string]map[int64]int64{}
		}
		b := s.binding[op.sym]
		if b == nil {
			b = map[int64]int64{}
			s.binding[op.sym] = b
		}
		if op.to < 0 {
			delete(b, op.id)
		} else {
			b[op.id] = op.to
		}
	case opBindingDelete:
		t.binding = true
		if op.sym == "" {
			s.binding = nil
		} else {
			delete(s.binding, op.sym)
		}
	}
}

func (m *Market) applySymbol(j *mdJournal, op *symbolOp) {
	c := m.queries[op.id]
	if c == nil || c.finished {
		return
	}
	j.queries[c] = true
	if op.del {
		c.values = nil
		return
	}
	for k, v := range op.values {
		if c.values == nil {
			c.values = map[string]jsontext.Value{}
		}
		if v == nil {
			delete(c.values, k)
		} else {
			c.values[k] = v
		}
	}
}

// finish 在整包安装后计算就绪、净变化与待发布事件。
func (m *Market) finish(j *mdJournal, ch *marketChanges) Applied {
	if m.recovering && m.route.up && m.barrierReached() {
		m.recovering = false
		j.forced = true
	}
	var pub []func()

	// 报价：状态与净变化
	for n, before := range j.quotes {
		m.settleQuote(n)
		c := QuoteChange{
			Fields:  quoteSchema.changed(&before.value, before.valid, &n.value, n.valid),
			Created: !before.exists && n.exists,
			Deleted: before.exists && !n.exists,
			Status:  before.state != n.state,
		}
		if c.Any() {
			ch.quote(n.id, c)
		}
		m.publishQuote(n, j.st, &pub)
	}
	if j.forced {
		for _, n := range m.quotes {
			if _, done := j.quotes[n]; done {
				continue
			}
			old := n.state
			m.settleQuote(n)
			if n.state != old {
				ch.quote(n.id, QuoteChange{Status: true})
				m.publishQuote(n, j.st, &pub)
			}
		}
	}

	// 窗口
	for _, w := range m.windows {
		if j.forced || w.affected(j) {
			w.evaluate(j, ch, &pub)
		}
	}

	// 查询
	for c := range j.queries {
		if m.completeQuery(c) {
			ch.queries = append(ch.queries, c.id)
		}
	}

	return Applied{
		Changed: !ch.empty() || len(pub) > 0,
		Changes: ch,
		Publish: func() {
			for _, fn := range pub {
				fn()
			}
		},
	}
}

func (ch *marketChanges) quote(id uint64, c QuoteChange) {
	if ch.quotes == nil {
		ch.quotes = map[uint64]QuoteChange{}
	}
	ch.quotes[id] = c
}

// settleQuote 根据存在性、连接代次与恢复屏障确定报价状态。
func (m *Market) settleQuote(n *quoteNode) {
	if !m.route.up {
		if n.state == Ready {
			n.state = Stale
		}
		return
	}
	switch {
	case n.exists && n.gen == m.route.gen && !m.recovering:
		n.state = Ready
	case n.deleted && n.gen == m.route.gen:
		n.state = Deleted
	default:
		n.state = Pending // 尚无当前代次数据，或未满足恢复屏障
	}
}

func (m *Market) publishQuote(n *quoteNode, st Stamp, pub *[]func()) {
	var subs []*QuoteSub
	for s := range m.quoteSubs {
		if s.symbols[n.symbol] {
			subs = append(subs, s)
		}
	}
	if len(subs) == 0 {
		return
	}
	e := quoteEvent(n, st)
	*pub = append(*pub, func() {
		for _, s := range subs {
			if !s.mb.push(e) && s.mb.closed() {
				s.m.dropQuoteSub(s)
			}
		}
	})
}

func (m *Market) dropQuoteSub(s *QuoteSub) {
	m.rt.mu.Lock()
	defer m.rt.mu.Unlock()
	if _, ok := m.quoteSubs[s]; ok {
		delete(m.quoteSubs, s)
		m.release(s.list)
	}
}
