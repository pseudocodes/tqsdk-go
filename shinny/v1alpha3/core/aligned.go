package core

import (
	"context"
	"slices"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// AlignedKlines 是多合约对齐 K 线窗口。对齐关系只来自服务端 binding，
// 不用相同 ID 或 datetime 相等替代。
type AlignedKlines struct {
	m  *Market
	id uint64

	// 以下字段受 rt.mu 保护。
	req    AlignedKlineRequest
	dur    int64
	legs   []*series[model.Kline, model.KlineField]
	chart  *chartState
	state  DataState
	inited bool
	rebase bool
	shrunk bool
	info   WindowInfo
	rows   []AlignedRow
	keys   []int64
	subs   map[*AlignedKlineSub]struct{}
	page   *queryCall
	closed bool
}

func validateAligned(r AlignedKlineRequest, page bool) error {
	if len(r.Symbols) < 2 {
		return invalid("aligned klines need at least two symbols")
	}
	seen := map[string]bool{}
	for _, s := range r.Symbols {
		if err := validateSymbol(s); err != nil {
			return err
		}
		if seen[s] {
			return invalid("duplicate symbol %s", s)
		}
		seen[s] = true
	}
	if err := validatePeriod(r.Period); err != nil {
		return err
	}
	if err := validateWindow(r.Width, r.Position); err != nil {
		return err
	}
	if r.Mode == MatchedRows && (r.Position.Mode != Latest || page) {
		return invalid("MatchedRows supports only a persistent Latest window; use PrimarySlots for history")
	}
	return nil
}

// AlignedKlines 建立多合约对齐窗口；立即返回 Pending 句柄。
func (m *Market) AlignedKlines(ctx context.Context, r AlignedKlineRequest) (*AlignedKlines, error) {
	if err := validateAligned(r, false); err != nil {
		return nil, err
	}
	return m.openAligned(r, nil)
}

// RequestAlignedKlinesPage 请求一次性多合约历史页（仅 PrimarySlots）。
func (m *Market) RequestAlignedKlinesPage(ctx context.Context, r AlignedKlineRequest) (*Request[AlignedKlineFrame], error) {
	if err := validateAligned(r, true); err != nil {
		return nil, err
	}
	c := newCall("page")
	if _, err := m.openAligned(r, c); err != nil {
		return nil, err
	}
	return &Request[AlignedKlineFrame]{m, c}, nil
}

func (m *Market) openAligned(r AlignedKlineRequest, page *queryCall) (*AlignedKlines, error) {
	r.Symbols = slices.Clone(r.Symbols)
	w := &AlignedKlines{m: m, id: m.rt.newID(), req: r, dur: int64(r.Period), page: page,
		subs: map[*AlignedKlineSub]struct{}{}}
	width := r.Width
	if r.Mode == MatchedRows {
		width = maxWidth // 初始化扫描最近 10000 根寻找完整匹配（同 Python）
	}
	pack := newSetChart(newChartID("aligned"), r.Symbols, w.dur, width, r.Position)
	data := pack.encode()
	m.rt.mu.Lock()
	defer m.rt.mu.Unlock()
	if err := m.usable(); err != nil {
		return nil, err
	}
	if err := m.route.send(data, nil); err != nil && err != ErrNotConnected {
		return nil, err
	}
	for _, s := range r.Symbols {
		w.legs = append(w.legs, klineKind.get(m, seriesKey{s, w.dur}, true))
	}
	w.chart = newChart(pack.ChartID, pack.desired())
	w.chart.pack = data
	m.charts[pack.ChartID] = w.chart
	if page != nil {
		page.id = pack.ChartID
	}
	m.windows[w.id] = w
	return w, nil
}

func (w *AlignedKlines) affected(j *mdJournal) bool {
	if j.charts[w.chart] {
		return true
	}
	for _, s := range w.legs {
		if _, ok := j.klines[s]; ok {
			return true
		}
	}
	return false
}

func (w *AlignedKlines) resend() {
	w.m.route.send(w.chart.pack, nil)
	w.rebase = true
}

func (w *AlignedKlines) drop(err error) {
	if w.page != nil {
		w.page.finish(nil, err)
		w.closeLocked()
	}
}

func (w *AlignedKlines) pageCall() *queryCall { return w.page }

func (w *AlignedKlines) closeLocked() {
	if w.closed {
		return
	}
	w.closed = true
	m := w.m
	delete(m.windows, w.id)
	if !m.closed {
		m.route.send(cancelChart(w.chart.id, w.dur, w.req.Width), nil)
		delete(m.charts, w.chart.id)
	}
	for s := range w.subs {
		s.mb.close(ErrClosed)
	}
	w.subs = nil
}

// Close 关闭窗口并撤销 chart。
func (w *AlignedKlines) Close() error {
	w.m.rt.mu.Lock()
	defer w.m.rt.mu.Unlock()
	w.closeLocked()
	return nil
}

// project 按当前状态重新计算输出行；返回主序列是否为空。
func (w *AlignedKlines) project() (rows []AlignedRow, keys []int64, info WindowInfo) {
	primary := w.legs[0]
	width := int64(w.req.Width)
	info = WindowInfo{RequestedWidth: w.req.Width, ViewVersion: w.info.ViewVersion}
	legsOf := func(pid int64) ([]KlineSlot, bool) {
		legs := make([]KlineSlot, len(w.legs))
		legs[0] = klineKind.slot(primary, pid)
		full := legs[0].Present
		for i := 1; i < len(w.legs); i++ {
			sid, ok := primary.binding[w.req.Symbols[i]][pid]
			if ok && sid >= 0 {
				legs[i] = klineKind.slot(w.legs[i], sid)
			} else {
				legs[i] = klineKind.slot(nil, -1)
			}
			full = full && legs[i].Present
		}
		return legs, full
	}
	if w.req.Mode == PrimarySlots {
		if w.req.Position.Mode == Latest {
			if primary.lastID >= 0 {
				info.Range, info.RangeKnown = IDRange{primary.lastID - width + 1, primary.lastID}, true
			}
		} else if w.chart.leftSeen && w.chart.left >= 0 {
			info.Range, info.RangeKnown = IDRange{w.chart.left, w.chart.left + width - 1}, true
		}
		if info.RangeKnown {
			for pid := info.Range.LeftID; pid <= info.Range.RightID; pid++ {
				legs, _ := legsOf(pid)
				if legs[0].Present {
					info.ValidCount++
				}
				rows = append(rows, AlignedRow{PrimaryID: pid, Legs: legs})
				keys = append(keys, pid)
			}
		}
		return rows, keys, info
	}
	// MatchedRows：从主序列末尾向前扫描完整匹配行，取最近 Width 个。
	if primary.lastID < 0 {
		return nil, nil, info
	}
	low := primary.lastID - maxWidth + 1
	if w.chart.leftSeen && w.chart.left >= 0 {
		low = w.chart.left
	}
	for _, k := range w.keys { // 源范围缩小后仍保留已选行
		if k >= 0 {
			low = min(low, k)
			break
		}
	}
	info.Range, info.RangeKnown = IDRange{max(low, 0), primary.lastID}, true
	var matched []AlignedRow
	for pid := primary.lastID; pid >= max(low, 0) && int64(len(matched)) < width; pid-- {
		if legs, full := legsOf(pid); full {
			matched = append(matched, AlignedRow{PrimaryID: pid, Legs: legs})
		}
	}
	info.ValidCount = len(matched)
	if len(matched) == 0 {
		return nil, nil, info
	}
	rows = make([]AlignedRow, 0, width)
	for i := int64(len(matched)); i < width; i++ { // 前置占位
		legs := make([]KlineSlot, len(w.legs))
		for j := range legs {
			legs[j] = klineKind.slot(nil, -1)
		}
		rows = append(rows, AlignedRow{PrimaryID: -1, Legs: legs})
	}
	for i := len(matched) - 1; i >= 0; i-- {
		rows = append(rows, matched[i])
	}
	keys = make([]int64, len(rows))
	for i, r := range rows {
		keys[i] = r.PrimaryID
	}
	return rows, keys, info
}

func (w *AlignedKlines) evaluate(j *mdJournal, ch *marketChanges, pub *[]func()) {
	if w.closed {
		return
	}
	m := w.m
	oldState := w.state
	rows, keys, info := w.project()
	baseline := false
	if !w.inited {
		if w.req.Mode == MatchedRows {
			w.inited = w.chart.signalReady() && (w.legs[0].lastID < 0 || info.ValidCount > 0)
		} else {
			w.inited = w.chart.acked() && w.chart.signalReady()
		}
		baseline = w.inited
	}
	state := oldState
	switch {
	case !m.route.up:
		if oldState == Ready {
			state = Stale
		}
	case w.inited && !m.recovering:
		state = Ready
	default:
		state = Pending
	}
	if state == Ready && w.rebase {
		baseline, w.rebase = true, false
	}
	if baseline {
		info.ViewVersion++
	}
	info.SerialReady = state == Ready
	info.FrameFull = state == Ready && info.ValidCount == w.req.Width
	w.state = state

	c := &AlignedKlineChange{StatusChanged: oldState != state}
	if state == Ready {
		// 位置变化只看输出 RowKeys：MatchedRows 源范围收缩不改变已输出位置，不算 Shifted。
		c.KeysChanged = baseline || !slices.Equal(keys, w.keys)
		c.Rows = changedRows(w.rows, rows, baseline)
		w.rows, w.keys = rows, keys
	}
	w.info = info
	if c.Any() {
		if ch.aligned == nil {
			ch.aligned = map[uint64]*AlignedKlineChange{}
		}
		ch.aligned[w.id] = c
	}
	if w.req.Mode == MatchedRows && state == Ready && !w.shrunk {
		w.shrink()
	}
	if w.page != nil {
		if state == Ready {
			w.page.finish(w.frame(j.st), nil)
			ch.queries = append(ch.queries, w.page.id)
			w.closeLocked()
		}
		return
	}
	if len(w.subs) == 0 || !(c.Any()) {
		return
	}
	var e AlignedKlineEvent
	e = AlignedKlineEvent{Symbols: w.req.Symbols, State: state, Window: info, Stamp: j.st}
	if state == Ready {
		e.Shifted = c.KeysChanged
		e.Rows = c.Rows
		if c.KeysChanged {
			e.RowKeys = keys
		}
	}
	items := make([]func(), 0, len(w.subs))
	for s := range w.subs {
		ev := e
		ev.Rows = cloneRows(e.Rows)
		ev.RowKeys = slices.Clone(e.RowKeys)
		ev.Symbols = slices.Clone(e.Symbols)
		if s.frame && state == Ready {
			f := w.frame(j.st)
			ev.Frame = &f
		}
		items = append(items, func() { s.mb.push(ev) })
	}
	*pub = append(*pub, items...)
}

// shrink 在初始化扫描后把源宽度收缩到 max(Width,30)（同 Python _update_serial_multi）。
func (w *AlignedKlines) shrink() {
	w.shrunk = true
	pack := newSetChart(w.chart.id, w.req.Symbols, w.dur, max(w.req.Width, 30), w.req.Position)
	w.chart.desired = pack.desired()
	w.chart.pack = pack.encode()
	w.m.route.send(w.chart.pack, nil)
}

func changedRows(old, cur []AlignedRow, all bool) []AlignedRow {
	if all {
		return cur
	}
	prev := make(map[int64]AlignedRow, len(old))
	for _, r := range old {
		if r.PrimaryID >= 0 {
			prev[r.PrimaryID] = r
		}
	}
	var out []AlignedRow
	for _, r := range cur {
		if r.PrimaryID < 0 {
			continue
		}
		p, ok := prev[r.PrimaryID]
		if !ok || !sameLegs(p.Legs, r.Legs) {
			out = append(out, r)
		}
	}
	return out
}

func sameLegs(a, b []KlineSlot) bool {
	for i := range a {
		if a[i].Present != b[i].Present || a[i].Value.ID != b[i].Value.ID ||
			klineSchema.changed(&a[i].Value, a[i].Valid, &b[i].Value, b[i].Valid).Any() {
			return false
		}
	}
	return true
}

func cloneRows(rows []AlignedRow) []AlignedRow {
	out := make([]AlignedRow, len(rows))
	for i, r := range rows {
		out[i] = AlignedRow{PrimaryID: r.PrimaryID, Legs: slices.Clone(r.Legs)}
	}
	return out
}

func (w *AlignedKlines) frame(st Stamp) AlignedKlineFrame {
	return AlignedKlineFrame{
		Symbols: slices.Clone(w.req.Symbols), Rows: cloneRows(w.rows), RowKeys: slices.Clone(w.keys),
		Window: w.info, SeriesLastID: w.legs[0].lastID, State: w.state, Stamp: st,
	}
}

// Snapshot 复制完整对齐窗口。
func (w *AlignedKlines) Snapshot() AlignedKlineFrame {
	w.m.rt.mu.RLock()
	defer w.m.rt.mu.RUnlock()
	return w.frame(Stamp{Epoch: w.m.rt.epoch})
}

// State 返回窗口状态。
func (w *AlignedKlines) State() DataState {
	w.m.rt.mu.RLock()
	defer w.m.rt.mu.RUnlock()
	return w.state
}

// AlignedKlineSub 是多合约对齐订阅。
type AlignedKlineSub struct {
	w     *AlignedKlines
	mb    *mailbox[AlignedKlineEvent]
	frame bool
	owns  bool
}

// Subscribe 为窗口建立事件订阅；已就绪时先交付基线。
func (w *AlignedKlines) Subscribe(opts ...SeriesOption) (*AlignedKlineSub, error) {
	return w.subscribe(opts, false)
}

func (w *AlignedKlines) subscribe(opts []SeriesOption, owns bool) (*AlignedKlineSub, error) {
	o := applyOptions(opts)
	if o.mailbox == 0 {
		o.mailbox = w.m.cfg.MailboxSize
	}
	w.m.rt.mu.Lock()
	defer w.m.rt.mu.Unlock()
	if w.closed {
		return nil, ErrClosed
	}
	s := &AlignedKlineSub{w: w, mb: newMailbox[AlignedKlineEvent](o.mailbox), frame: o.withFrame, owns: owns}
	if w.state == Ready {
		st := Stamp{Epoch: w.m.rt.epoch}
		f := w.frame(st)
		e := AlignedKlineEvent{Symbols: f.Symbols, Shifted: true, Window: w.info, State: Ready,
			Rows: cloneRows(f.Rows), RowKeys: slices.Clone(f.RowKeys), Stamp: st}
		if s.frame {
			e.Frame = &f
		}
		s.mb.push(e)
	}
	w.subs[s] = struct{}{}
	return s, nil
}

// SubscribeAlignedKlines 建立多合约窗口及其订阅。
func (m *Market) SubscribeAlignedKlines(ctx context.Context, r AlignedKlineRequest, opts ...SeriesOption) (*AlignedKlineSub, error) {
	w, err := m.AlignedKlines(ctx, r)
	if err != nil {
		return nil, err
	}
	s, err := w.subscribe(opts, true)
	if err != nil {
		w.Close()
		return nil, err
	}
	return s, nil
}

// C 返回事件 channel；订阅终止后关闭。
func (s *AlignedKlineSub) C() <-chan AlignedKlineEvent { return s.mb.ch }

// Err 返回终止原因；正常 Close 为 nil。
func (s *AlignedKlineSub) Err() error { return s.mb.error() }

// Window 返回订阅的窗口。
func (s *AlignedKlineSub) Window() *AlignedKlines { return s.w }

// Close 结束订阅；由 SubscribeAlignedKlines 创建的窗口一并关闭。
func (s *AlignedKlineSub) Close() error {
	s.w.m.rt.mu.Lock()
	delete(s.w.subs, s)
	if s.owns {
		s.w.closeLocked()
	}
	s.w.m.rt.mu.Unlock()
	s.mb.close(nil)
	return nil
}
