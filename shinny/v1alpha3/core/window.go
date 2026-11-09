package core

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// seriesReq 是规范化后的单合约序列请求。
type seriesReq struct {
	key   seriesKey
	width int
	pos   WindowPosition
}

// view 是窗口最近一次 Ready 时的视图，用于判断行是否以新身份进入窗口。
type view[T any, F ~uint16] struct {
	s     *series[T, F]
	known bool
	r     IDRange
}

// seriesWindow 是单合约 K 线/Tick 的逻辑窗口：序列保存数据，chart 保存线上请求，窗口保存视图。
type seriesWindow[T any, F ~uint16] struct {
	m    *Market
	id   uint64
	kind *seriesKind[T, F]
	tick bool

	// 以下字段受 rt.mu 保护。
	req    seriesReq
	strict bool // 需要请求回显确认（历史定位或一次性页）
	chart  *chartState
	s      *series[T, F]
	next   *seriesReq // Reconfigure 登记、尚未切换的请求
	state  DataState
	inited bool
	rebase bool // 下次就绪时发布新基线（重连恢复）
	info   WindowInfo
	infoS  *series[T, F] // info 所基于的序列
	view   view[T, F]
	subs   map[*seriesSub[T, F]]struct{}
	page   *queryCall
	closed bool
}

// Klines 是单合约 K 线窗口的稳定句柄。
type Klines struct {
	*seriesWindow[model.Kline, model.KlineField]
}

// Ticks 是单合约 Tick 窗口的稳定句柄。
type Ticks struct {
	*seriesWindow[model.Tick, model.TickField]
}

func klineReq(r KlineRequest) (seriesReq, error) {
	if err := validateSymbol(r.Symbol); err != nil {
		return seriesReq{}, err
	}
	if err := validatePeriod(r.Period); err != nil {
		return seriesReq{}, err
	}
	if err := validateWindow(r.Width, r.Position); err != nil {
		return seriesReq{}, err
	}
	return seriesReq{key: seriesKey{r.Symbol, int64(r.Period)}, width: r.Width, pos: r.Position}, nil
}

func tickReq(r TickRequest) (seriesReq, error) {
	if err := validateSymbol(r.Symbol); err != nil {
		return seriesReq{}, err
	}
	if err := validateWindow(r.Width, r.Position); err != nil {
		return seriesReq{}, err
	}
	return seriesReq{key: seriesKey{r.Symbol, 0}, width: r.Width, pos: r.Position}, nil
}

// Klines 建立 K 线窗口并发送 set_chart；立即返回 Pending 句柄。
func (m *Market) Klines(ctx context.Context, r KlineRequest) (*Klines, error) {
	req, err := klineReq(r)
	if err != nil {
		return nil, err
	}
	w, err := openWindow(m, klineKind, false, req, req.pos.Mode != Latest, nil)
	if err != nil {
		return nil, err
	}
	return &Klines{w}, nil
}

// Ticks 建立 Tick 窗口并发送 set_chart；立即返回 Pending 句柄。
func (m *Market) Ticks(ctx context.Context, r TickRequest) (*Ticks, error) {
	req, err := tickReq(r)
	if err != nil {
		return nil, err
	}
	w, err := openWindow(m, tickKind, true, req, req.pos.Mode != Latest, nil)
	if err != nil {
		return nil, err
	}
	return &Ticks{w}, nil
}

// Reconfigure 登记新请求（新 chart_id），窗口进入 Pending，完成后交付新 ViewVersion 基线。
func (w *Klines) Reconfigure(ctx context.Context, r KlineRequest) error {
	req, err := klineReq(r)
	if err != nil {
		return err
	}
	return w.reconfigure(req)
}

// Reconfigure 登记新请求（新 chart_id），窗口进入 Pending，完成后交付新 ViewVersion 基线。
func (w *Ticks) Reconfigure(ctx context.Context, r TickRequest) error {
	req, err := tickReq(r)
	if err != nil {
		return err
	}
	return w.reconfigure(req)
}

func newChartID(prefix string) string {
	return fmt.Sprintf("TQGO_%s_%016x%016x", prefix, rand.Uint64(), rand.Uint64())
}

func openWindow[T any, F ~uint16](m *Market, kind *seriesKind[T, F], tick bool, req seriesReq, strict bool, page *queryCall) (*seriesWindow[T, F], error) {
	m.rt.mu.Lock()
	defer m.rt.mu.Unlock()
	if err := m.usable(); err != nil {
		return nil, err
	}
	w := &seriesWindow[T, F]{m: m, id: m.rt.newID(), kind: kind, tick: tick, strict: strict, page: page,
		subs: map[*seriesSub[T, F]]struct{}{}}
	if err := w.attach(req); err != nil {
		w.detach()
		return nil, err
	}
	if page != nil {
		page.id = w.chart.id
	}
	m.windows[w.id] = w
	return w, nil
}

// attach 绑定序列、登记 chart 并发送 set_chart。未连接时只登记，重连后重放。
func (w *seriesWindow[T, F]) attach(req seriesReq) error {
	m := w.m
	prefix := "realtime"
	if w.page != nil {
		prefix = "page"
	}
	pack := newSetChart(newChartID(prefix), []string{req.key.symbol}, req.key.dur, req.width, req.pos)
	w.req = req
	w.s = w.kind.get(m, req.key, true)
	w.chart = newChart(pack.ChartID, pack.desired())
	w.chart.pack = pack.encode()
	m.charts[pack.ChartID] = w.chart
	if err := m.route.send(w.chart.pack, nil); err != nil && err != ErrNotConnected {
		return err
	}
	return nil
}

// detach 撤销 chart；序列数据保留（服务端不会重发已下发的行）。
func (w *seriesWindow[T, F]) detach() {
	m := w.m
	m.route.send(cancelChart(w.chart.id, w.req.key.dur, w.req.width), nil)
	delete(m.charts, w.chart.id)
}

func (w *seriesWindow[T, F]) resend() {
	if w.next == nil {
		w.m.route.send(w.chart.pack, nil)
	}
	w.rebase = true
}

func (w *seriesWindow[T, F]) reconfigure(req seriesReq) error {
	m := w.m
	m.rt.mu.Lock()
	defer m.rt.mu.Unlock()
	if w.closed || m.closed {
		return ErrClosed
	}
	if w.next == nil && req == w.req {
		return nil
	}
	w.next = &req
	m.rt.postControl(m.id, m.controlCommit)
	return nil
}

// controlCommit 处理本地控制更新（Reconfigure 切换等）。
func (m *Market) controlCommit(st Stamp) Applied {
	if m.closed {
		return Applied{}
	}
	return m.finish(m.newJournal(st), &marketChanges{})
}

func (w *seriesWindow[T, F]) switchRequest() {
	req := *w.next
	w.next = nil
	w.detach()
	if err := w.attach(req); err != nil { // 请求已登记，重连时重放
		w.m.log.Warn("reconfigure send failed")
	}
	w.strict = req.pos.Mode != Latest
	w.inited = false
	w.rebase = true
	w.state = Pending
}

func (w *seriesWindow[T, F]) affected(j *mdJournal) bool {
	if w.next != nil || j.charts[w.chart] {
		return true
	}
	_, ok := touchesOf(j, w.kind)[w.s]
	return ok
}

func (w *seriesWindow[T, F]) drop(err error) {
	if w.page != nil {
		w.page.finish(nil, err)
		w.closeLocked()
	}
}

// closeLocked 关闭窗口；调用方持有 rt.mu。
func (w *seriesWindow[T, F]) closeLocked() {
	if w.closed {
		return
	}
	w.closed = true
	delete(w.m.windows, w.id)
	if !w.m.closed {
		w.detach()
	}
	for s := range w.subs {
		s.mb.close(ErrClosed)
	}
	w.subs = nil
}

// Close 关闭窗口、撤销 chart；之后句柄不可再读。
func (w *seriesWindow[T, F]) Close() error {
	w.m.rt.mu.Lock()
	defer w.m.rt.mu.Unlock()
	w.closeLocked()
	return nil
}

// evaluate 在整包安装后更新窗口的就绪、范围与变化，并准备事件。
func (w *seriesWindow[T, F]) evaluate(j *mdJournal, ch *marketChanges, pub *[]func()) {
	if w.closed {
		return
	}
	m := w.m
	if w.next != nil {
		w.switchRequest()
	}
	t := touchesOf(j, w.kind)[w.s]
	oldState, old := w.state, w.view

	baseline := false
	if !w.inited {
		if w.strict {
			w.inited = w.chart.acked() && w.chart.signalReady()
		} else {
			w.inited = w.chart.signalReady()
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
	w.info = w.computeInfo(t, state, baseline)
	w.state = state

	var c *SeriesChange[T, F]
	var ids []int64
	moved := false
	if state == Ready {
		c, ids, moved = w.diff(t, old, baseline)
		w.view = view[T, F]{s: w.s, known: w.info.RangeKnown, r: w.info.Range}
	}
	if oldState != state {
		if c == nil {
			c = &SeriesChange[T, F]{lastBefore: w.lastIdent(old, t), lastAfter: w.lastIdent(old, t)}
		}
		c.StatusChanged = true
	}
	if c.Any() || (c != nil && c.lastBefore != c.lastAfter) {
		if ch.series == nil {
			ch.series = map[uint64]any{}
		}
		ch.series[w.id] = c
	}

	if w.page != nil {
		if state == Ready {
			w.page.finish(w.frame(j.st), nil)
			ch.queries = append(ch.queries, w.page.id)
			w.closeLocked()
		}
		return
	}
	w.publish(j.st, state, oldState, baseline, moved, ids, pub)
}

// computeInfo 计算窗口范围与真实行数；ValidCount 按范围移动增量维护。
func (w *seriesWindow[T, F]) computeInfo(t *seriesTouch[T, F], state DataState, baseline bool) WindowInfo {
	prev, prevS := w.info, w.infoS
	info := WindowInfo{RequestedWidth: w.req.width, ViewVersion: prev.ViewVersion}
	if baseline {
		info.ViewVersion++
	}
	width := int64(w.req.width)
	if w.req.pos.Mode == Latest {
		if w.s.lastID >= 0 {
			info.Range, info.RangeKnown = IDRange{w.s.lastID - width + 1, w.s.lastID}, true
		}
	} else if w.chart.leftSeen && w.chart.left >= 0 {
		info.Range, info.RangeKnown = IDRange{w.chart.left, w.chart.left + width - 1}, true
	}
	w.infoS = w.s
	if info.RangeKnown {
		pr, nr := prev.Range, info.Range
		if prevS != w.s || !prev.RangeKnown || abs(nr.RightID-pr.RightID) >= width || abs(nr.LeftID-pr.LeftID) >= width {
			for id := max(nr.LeftID, 0); id <= nr.RightID; id++ {
				if w.s.rows[id] != nil {
					info.ValidCount++
				}
			}
		} else {
			n := prev.ValidCount
			eachOutside(pr, nr, func(id int64) { // 离开窗口的行
				if t.presentBefore(w.s, id) {
					n--
				}
			})
			eachOutside(nr, pr, func(id int64) { // 进入窗口的行
				if w.s.rows[id] != nil {
					n++
				}
			})
			if t != nil { // 窗口内被触及的行
				for id, b := range t.rows {
					if pr.contains(id) && nr.contains(id) {
						if b.present && w.s.rows[id] == nil {
							n--
						} else if !b.present && w.s.rows[id] != nil {
							n++
						}
					}
				}
			}
			info.ValidCount = n
		}
	}
	info.SerialReady = state == Ready
	info.FrameFull = state == Ready && info.RangeKnown && info.ValidCount == w.req.width
	return info
}

// eachOutside 遍历在 a 中但不在 b 中的 ID。
func eachOutside(a, b IDRange, fn func(int64)) {
	for id := a.LeftID; id <= min(a.RightID, b.LeftID-1); id++ {
		fn(id)
	}
	for id := max(a.LeftID, b.RightID+1); id <= a.RightID; id++ {
		fn(id)
	}
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// diff 计算相对上一个 Ready 视图的行变化；返回事件行 ID 与窗口是否移动。
func (w *seriesWindow[T, F]) diff(t *seriesTouch[T, F], old view[T, F], baseline bool) (*SeriesChange[T, F], []int64, bool) {
	nr, nk := w.info.Range, w.info.RangeKnown
	same := old.s == w.s
	inOld := func(id int64) bool { return same && old.known && old.r.contains(id) }
	inNew := func(id int64) bool { return nk && nr.contains(id) }

	set := map[int64]struct{}{}
	if nk {
		if baseline || !same || !old.known {
			for id := nr.LeftID; id <= nr.RightID; id++ {
				set[id] = struct{}{}
			}
		} else {
			for id := nr.LeftID; id <= min(nr.RightID, old.r.LeftID-1); id++ {
				set[id] = struct{}{}
			}
			for id := max(nr.LeftID, old.r.RightID+1); id <= nr.RightID; id++ {
				set[id] = struct{}{}
			}
		}
	}
	if t != nil {
		for id := range t.rows {
			if inNew(id) || inOld(id) {
				set[id] = struct{}{}
			}
		}
	}
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	c := &SeriesChange[T, F]{lastBefore: w.lastIdent(old, t)}
	all := model.AllFields[F](len(w.kind.schema.fields))
	for _, id := range ids {
		after := w.s.rows[id]
		rc := RowChange[F]{ID: id}
		b, touched := rowBefore[T, F]{}, false
		if t != nil {
			b, touched = t.rows[id]
		}
		switch {
		case inNew(id) && !inOld(id): // 以新身份进入窗口
			if after != nil {
				rc.Fields = all
				rc.Created = touched && !b.present
			}
		case touched:
			rc.Created = !b.present && after != nil
			rc.Deleted = b.present && after == nil
			if rc.Created {
				rc.Fields = all
			} else if b.present && after != nil {
				rc.Fields = w.kind.schema.changed(&b.value, b.valid, &after.value, after.valid)
			}
		}
		if rc.Fields.Any() || rc.Created || rc.Deleted {
			c.Rows = append(c.Rows, rc)
			c.FinalRows = append(c.FinalRows, w.kind.slot(w.s, id))
		}
	}
	c.lastAfter = rowIdent{known: nk, id: nr.RightID, present: nk && w.s.rows[nr.RightID] != nil}
	c.WindowMoved = !same || old.known != nk || old.r != nr
	return c, ids, c.WindowMoved
}

func (w *seriesWindow[T, F]) lastIdent(v view[T, F], t *seriesTouch[T, F]) rowIdent {
	if !v.known {
		return rowIdent{}
	}
	id := v.r.RightID
	if v.s == w.s {
		return rowIdent{known: true, id: id, present: t.presentBefore(v.s, id)}
	}
	return rowIdent{known: true, id: id, present: v.s.rows[id] != nil}
}

func (w *seriesWindow[T, F]) publish(st Stamp, state, oldState DataState, baseline, moved bool, ids []int64, pub *[]func()) {
	if len(w.subs) == 0 {
		return
	}
	ready := state == Ready && (baseline || moved || len(ids) > 0)
	if !ready && (state == oldState || state == Ready) {
		return
	}
	var slots []Slot[T, F]
	if ready {
		slots = make([]Slot[T, F], len(ids))
		for i, id := range ids {
			slots[i] = w.kind.slot(w.s, id)
		}
	}
	var frame *Frame[T, F]
	type item struct {
		s *seriesSub[T, F]
		e SeriesEvent[T, F]
	}
	items := make([]item, 0, len(w.subs))
	for s := range w.subs {
		e := w.event(st, state, ready && (baseline || moved), slices.Clone(slots))
		if s.frame && state == Ready {
			if frame == nil {
				f := w.frame(st)
				frame = &f
			}
			f := *frame
			f.Rows = slices.Clone(frame.Rows)
			e.Frame = &f
		}
		items = append(items, item{s, e})
	}
	*pub = append(*pub, func() {
		for _, it := range items {
			it.s.mb.push(it.e)
		}
	})
}

func (w *seriesWindow[T, F]) event(st Stamp, state DataState, shifted bool, rows []Slot[T, F]) SeriesEvent[T, F] {
	return SeriesEvent[T, F]{
		Symbol: w.req.key.symbol, Period: periodOf(w.req.key.dur), Shifted: shifted,
		Window: w.info, State: state, Rows: rows, Stamp: st,
	}
}

// frame 复制完整窗口；调用方持有 rt.mu。
func (w *seriesWindow[T, F]) frame(st Stamp) Frame[T, F] {
	f := Frame[T, F]{SeriesLastID: w.s.lastID, Window: w.info, State: w.state, Stamp: st}
	if w.info.RangeKnown {
		f.Rows = make([]Slot[T, F], 0, w.req.width)
		for id := w.info.Range.LeftID; id <= w.info.Range.RightID; id++ {
			f.Rows = append(f.Rows, w.kind.slot(w.s, id))
		}
	}
	return f
}

// ---- 读取 ----

// At 返回窗口第 index 个逻辑位置；bool 表示该位置在已知窗口内（不等于 Present）。
func (w *seriesWindow[T, F]) At(index int) (Slot[T, F], bool) {
	w.m.rt.mu.RLock()
	defer w.m.rt.mu.RUnlock()
	if !w.info.RangeKnown || index < 0 || index >= w.req.width {
		return Slot[T, F]{}, false
	}
	return w.kind.slot(w.s, w.info.Range.LeftID+int64(index)), true
}

// Last 返回窗口末位置（可能是空槽）。
func (w *seriesWindow[T, F]) Last() (Slot[T, F], bool) { return w.At(w.req.width - 1) }

// ByID 按业务行 ID 读取窗口内的行。
func (w *seriesWindow[T, F]) ByID(id int64) (Slot[T, F], bool) {
	w.m.rt.mu.RLock()
	defer w.m.rt.mu.RUnlock()
	if !w.info.RangeKnown || !w.info.Range.contains(id) {
		return Slot[T, F]{}, false
	}
	return w.kind.slot(w.s, id), true
}

// Len 返回已知窗口的位置数；范围未知时为 0。
func (w *seriesWindow[T, F]) Len() int {
	w.m.rt.mu.RLock()
	defer w.m.rt.mu.RUnlock()
	if !w.info.RangeKnown {
		return 0
	}
	return w.req.width
}

// State 返回窗口状态。
func (w *seriesWindow[T, F]) State() DataState {
	w.m.rt.mu.RLock()
	defer w.m.rt.mu.RUnlock()
	return w.state
}

// Info 返回窗口信息。
func (w *seriesWindow[T, F]) Info() WindowInfo {
	w.m.rt.mu.RLock()
	defer w.m.rt.mu.RUnlock()
	return w.info
}

// Snapshot 在一次读锁内复制完整窗口。
func (w *seriesWindow[T, F]) Snapshot() Frame[T, F] {
	w.m.rt.mu.RLock()
	defer w.m.rt.mu.RUnlock()
	return w.frame(Stamp{Epoch: w.m.rt.epoch})
}

// AppendTo 把窗口追加到 dst（复用调用者缓冲）。
func (w *seriesWindow[T, F]) AppendTo(dst []Slot[T, F]) ([]Slot[T, F], WindowInfo, Stamp) {
	w.m.rt.mu.RLock()
	defer w.m.rt.mu.RUnlock()
	if w.info.RangeKnown {
		for id := w.info.Range.LeftID; id <= w.info.Range.RightID; id++ {
			dst = append(dst, w.kind.slot(w.s, id))
		}
	}
	return dst, w.info, Stamp{Epoch: w.m.rt.epoch}
}

// ---- 订阅 ----

// seriesSub 是一个窗口的有界事件订阅。
type seriesSub[T any, F ~uint16] struct {
	w     *seriesWindow[T, F]
	mb    *mailbox[SeriesEvent[T, F]]
	frame bool
	owns  bool // 关闭订阅时一并关闭窗口
}

func (w *seriesWindow[T, F]) subscribe(opts []SeriesOption, owns bool) (*seriesSub[T, F], error) {
	o := applyOptions(opts)
	if o.mailbox == 0 {
		o.mailbox = w.m.cfg.MailboxSize
	}
	w.m.rt.mu.Lock()
	defer w.m.rt.mu.Unlock()
	if w.closed {
		return nil, ErrClosed
	}
	s := &seriesSub[T, F]{w: w, mb: newMailbox[SeriesEvent[T, F]](o.mailbox), frame: o.withFrame, owns: owns}
	if w.state == Ready { // 同一视点的基线先入邮箱
		st := Stamp{Epoch: w.m.rt.epoch}
		f := w.frame(st)
		e := w.event(st, Ready, true, slices.Clone(f.Rows))
		if s.frame {
			e.Frame = &f
		}
		s.mb.push(e)
	}
	w.subs[s] = struct{}{}
	return s, nil
}

// C 返回事件 channel；订阅终止后关闭。
func (s *seriesSub[T, F]) C() <-chan SeriesEvent[T, F] { return s.mb.ch }

// Err 返回终止原因；正常 Close 为 nil。
func (s *seriesSub[T, F]) Err() error { return s.mb.error() }

// Close 结束订阅；由 Subscribe 创建的窗口一并关闭。
func (s *seriesSub[T, F]) Close() error {
	s.w.m.rt.mu.Lock()
	delete(s.w.subs, s)
	if s.owns {
		s.w.closeLocked()
	}
	s.w.m.rt.mu.Unlock()
	s.mb.close(nil)
	return nil
}

// KlineSub 是 K 线订阅；关闭时撤销其窗口。
type KlineSub struct {
	*seriesSub[model.Kline, model.KlineField]
	Window *Klines
}

// TickSub 是 Tick 订阅；关闭时撤销其窗口。
type TickSub struct {
	*seriesSub[model.Tick, model.TickField]
	Window *Ticks
}

// Subscribe 为窗口建立一个事件订阅；窗口已就绪时先交付基线。
func (w *Klines) Subscribe(opts ...SeriesOption) (*KlineSub, error) {
	s, err := w.subscribe(opts, false)
	if err != nil {
		return nil, err
	}
	return &KlineSub{s, w}, nil
}

// Subscribe 为窗口建立一个事件订阅；窗口已就绪时先交付基线。
func (w *Ticks) Subscribe(opts ...SeriesOption) (*TickSub, error) {
	s, err := w.subscribe(opts, false)
	if err != nil {
		return nil, err
	}
	return &TickSub{s, w}, nil
}

// SubscribeKlines 建立 K 线窗口及其订阅。
func (m *Market) SubscribeKlines(ctx context.Context, r KlineRequest, opts ...SeriesOption) (*KlineSub, error) {
	w, err := m.Klines(ctx, r)
	if err != nil {
		return nil, err
	}
	s, err := w.subscribe(opts, true)
	if err != nil {
		w.Close()
		return nil, err
	}
	return &KlineSub{s, w}, nil
}

// SubscribeTicks 建立 Tick 窗口及其订阅。
func (m *Market) SubscribeTicks(ctx context.Context, r TickRequest, opts ...SeriesOption) (*TickSub, error) {
	w, err := m.Ticks(ctx, r)
	if err != nil {
		return nil, err
	}
	s, err := w.subscribe(opts, true)
	if err != nil {
		w.Close()
		return nil, err
	}
	return &TickSub{s, w}, nil
}

// Reconfigure 更换订阅窗口的请求。
func (s *KlineSub) Reconfigure(ctx context.Context, r KlineRequest) error {
	return s.Window.Reconfigure(ctx, r)
}

// Reconfigure 更换订阅窗口的请求。
func (s *TickSub) Reconfigure(ctx context.Context, r TickRequest) error {
	return s.Window.Reconfigure(ctx, r)
}
