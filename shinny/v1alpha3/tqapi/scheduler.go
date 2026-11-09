package tqapi

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime/debug"
	"time"

	"go.uber.org/zap"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
)

const (
	maxTasks   = 4096             // 活动任务容量上限
	pollBudget = 256              // 每次调用的普通就绪回调预算（不含数据分发）
	ioWindow   = time.Millisecond // 仅有本地进展时给网络的短等待窗口
)

// taskFeedback 是一次调用的任务反馈；创建后不可变，Pulse 只持有其指针。
type taskFeedback struct {
	changed bool
	err     error
}

// feedback 在一次调用内累积任务反馈。
type feedback struct {
	changed bool
	errs    []error
}

func (f *feedback) any() bool { return f.changed || len(f.errs) > 0 }

func (f *feedback) result() *taskFeedback {
	if !f.any() {
		return nil
	}
	return &taskFeedback{changed: true, err: errors.Join(f.errs...)}
}

// RegisterTask 登记任务并标记首次就绪，不当场调用 Poll。task 须为非 nil 指针，同一实例不可重复登记。
func (a *API) RegisterTask(task Task) (*TaskHandle, error) {
	if task == nil {
		return nil, errors.New("tqapi: nil task")
	}
	if v := reflect.ValueOf(task); v.Kind() != reflect.Pointer || v.IsNil() {
		return nil, errors.New("tqapi: task must be a non-nil pointer")
	}
	if a.closing {
		return nil, core.ErrClosed
	}
	if len(a.tasks) >= maxTasks {
		return nil, ErrTaskLimit
	}
	for _, h := range a.tasks {
		if h.task == task {
			return nil, ErrTaskRegistered
		}
	}
	h := &TaskHandle{task: task, pending: true}
	a.tasks = append(a.tasks, h)
	a.tasksOn.Store(true)
	return h, nil
}

// pump 是启用任务后的一次 WaitUpdate：返回优先级为 Commit → 任务反馈 → 等待错误/预算让出。
func (a *API) pump(ctx context.Context) (Pulse, error) {
	var fb feedback
	for {
		if err := ctx.Err(); err != nil {
			return Pulse{}, err // 能走到这里时 fb 必为空
		}
		more := a.runReady(&fb)
		if more || fb.any() {
			// 已有本地进展：实时模式给网络一次短机会，进展本次交付。回测中时间只在 Step 中前进，
			// 不存在网络饥饿，且真实时间窗口会让 Pulse 分组随下载速度变化，因此直接返回。
			var c core.Commit
			var ok bool
			var err error
			if dl, can := shortDeadline(ctx); can && !a.virtual {
				c, ok, err = a.step(ctx, dl)
			}
			if ok {
				return a.deliver(c, &fb), nil
			}
			p := Pulse{tasks: fb.result()}
			if err != nil && !isCtxErr(err) {
				return p, err // 驱动错误不被进展掩盖
			}
			return p, nil // 外部 ctx 结束也照常交付已有进展
		}
		c, ok, err := a.step(ctx, a.nextWake())
		if ok {
			return a.deliver(c, &fb), nil
		}
		if err != nil {
			return Pulse{}, err
		}
		// 内部期限到达：执行到期任务后继续等待。
	}
}

// step 调用一次 Runtime.StepUntil，deadline 为内部期限（零值表示无；回测中为虚拟时间，参与虚拟时间推进）。
// ok 表示取得 Commit；err==nil && !ok 表示仅内部期限到达；外部 ctx 结束时返回 ctx.Err()，其余 err 为驱动错误。
func (a *API) step(ctx context.Context, deadline time.Time) (core.Commit, bool, error) {
	c, err := a.runtime.StepUntil(ctx, deadline)
	switch {
	case err == nil:
		return c, true, nil
	case errors.Is(err, core.ErrWakeReached):
		return core.Commit{}, false, nil
	case !isCtxErr(err):
		return core.Commit{}, false, err // 驱动错误
	case ctx.Err() != nil:
		return core.Commit{}, false, ctx.Err()
	}
	return core.Commit{}, false, nil
}

// now 返回调度器的时钟：回测为虚拟时间，否则为本机单调时钟。
func (a *API) now() time.Time {
	if a.virtual {
		return a.runtime.Now()
	}
	return time.Now()
}

func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// shortDeadline 返回短 I/O 窗口的截止时间；有外部截止时不超过剩余时间的一半。
func shortDeadline(ctx context.Context) (time.Time, bool) {
	w := ioWindow
	if dl, ok := ctx.Deadline(); ok {
		w = min(w, time.Until(dl)/2)
	}
	if w <= 0 || ctx.Err() != nil {
		return time.Time{}, false
	}
	return time.Now().Add(w), true
}

func (a *API) dataPulse(c core.Commit) Pulse {
	return Pulse{Stamp: c.Stamp, commit: c, md: a.md, td: a.td}
}

// deliver 把 Commit 分发给订阅数据的任务，并合并任务反馈。
func (a *API) deliver(c core.Commit, fb *feedback) Pulse {
	p := a.dataPulse(c)
	a.dispatchData(p, fb)
	p.tasks = fb.result()
	return p
}

// dispatchData 对分发前已订阅数据的任务各调用一次 Poll；回调中新注册的任务不补发。
func (a *API) dispatchData(p Pulse, fb *feedback) {
	for _, h := range a.tasks {
		if !h.done && h.watch {
			a.poll(h, p, fb)
		}
	}
	a.compact()
}

// runReady 先执行首次停止回调，再轮转执行首次就绪 / Wake / 到期任务，直到无就绪或预算用尽。
// 返回 true 表示预算用尽时仍有就绪任务。
func (a *API) runReady(fb *feedback) bool {
	budget := pollBudget
	defer a.compact()
	for {
		batch := a.readyBatch(a.now())
		if len(batch) == 0 {
			return false
		}
		for _, i := range batch {
			if budget == 0 {
				return true
			}
			h := a.tasks[i]
			if h.done {
				continue
			}
			if !h.firstStop() {
				a.cursor = i + 1
			}
			budget--
			a.poll(h, Pulse{}, fb)
		}
	}
}

// readyBatch 返回本轮就绪任务的下标：首次停止在前，其余从轮转游标开始。
func (a *API) readyBatch(now time.Time) []int {
	b := a.batch[:0]
	n := len(a.tasks)
	for i, h := range a.tasks {
		if h.firstStop() {
			b = append(b, i)
		}
	}
	for k := range n {
		i := (a.cursor + k) % n
		if h := a.tasks[i]; !h.firstStop() && h.ready(now) {
			b = append(b, i)
		}
	}
	a.batch = b
	return b
}

// poll 执行一次回调；Poll panic 时分离该任务。
func (a *API) poll(h *TaskHandle, p Pulse, fb *feedback) {
	h.pending = false // 执行中重新 Wake 的标志保留
	if h.stopping {
		h.stopNotified = true
	}
	step, err, perr := callPoll(h.task, TaskInput{Now: a.now(), Pulse: p, Stopping: h.stopping})
	if perr != nil {
		a.log.Error("tqapi: task panic", zap.Error(perr), zap.ByteString("stack", perr.Stack))
		a.detach(h, perr, fb)
		return
	}
	if err != nil && h.err == nil {
		h.err = err
		fb.errs = append(fb.errs, err)
		a.log.Warn("tqapi: task error", zap.Error(err))
	}
	if step.Done {
		h.finish()
		fb.changed = true
		return
	}
	fb.changed = fb.changed || step.Changed
	h.watch, h.wakeAt = step.WatchData, step.WakeAt
}

func callPoll(t Task, in TaskInput) (step TaskStep, err error, perr *TaskPanicError) {
	defer func() {
		if r := recover(); r != nil {
			perr = &TaskPanicError{Value: r, Stack: debug.Stack()}
		}
	}()
	step, err = t.Poll(in)
	return
}

// detach 强制结束调度：调用可选 OnDetach（其 panic 不影响其他任务），记录原因。fb 可为 nil。
func (a *API) detach(h *TaskHandle, cause error, fb *feedback) {
	t := h.task
	h.finish()
	h.err = errors.Join(h.err, cause)
	if fb != nil {
		fb.errs = append(fb.errs, cause)
	}
	if d, ok := t.(TaskDetachHandler); ok {
		func() {
			defer func() {
				if r := recover(); r != nil {
					a.log.Error("tqapi: OnDetach panic", zap.Any("panic", r))
				}
			}()
			d.OnDetach(cause)
		}()
	}
}

// nextWake 返回活动任务中最早的 WakeAt；没有则为零值。
func (a *API) nextWake() time.Time {
	var next time.Time
	for _, h := range a.tasks {
		if !h.done && !h.wakeAt.IsZero() && (next.IsZero() || h.wakeAt.Before(next)) {
			next = h.wakeAt
		}
	}
	return next
}

// compact 移除已结束任务，并保持轮转游标指向同一位置。
func (a *API) compact() {
	live, shift := a.tasks[:0], 0
	for i, h := range a.tasks {
		if h.done {
			if i < a.cursor {
				shift++
			}
			continue
		}
		live = append(live, h)
	}
	clear(a.tasks[len(live):])
	a.tasks = live
	a.cursor -= shift
	if a.cursor >= len(live) {
		a.cursor = 0
	}
}

// Shutdown 拒绝新注册，取消全部任务并继续驱动其清理，直到全部 Done、ctx 结束或驱动无法继续；
// 剩余任务强制分离，最后关闭 Runtime。返回进入时活动任务的错误、分离原因与关闭错误的合并。
// 与 Python close 一致，即使 ctx 已结束也先执行一次就绪回调（含首次停止回调）。
func (a *API) Shutdown(ctx context.Context) error {
	if !a.driving.CompareAndSwap(false, true) {
		return ErrDispatchBusy
	}
	defer a.driving.Store(false)
	a.closing = true
	active := append([]*TaskHandle(nil), a.tasks...)
	for _, h := range a.tasks {
		h.Cancel()
	}
	var fb feedback // Shutdown 不交付 Pulse；错误保存在各 handle
	var stopErr error
	for len(a.tasks) > 0 {
		more := a.runReady(&fb)
		if len(a.tasks) == 0 {
			break
		}
		if err := ctx.Err(); err != nil {
			stopErr = err
			break
		}
		dl := a.nextWake()
		if more { // 仍有就绪任务：只给驱动一次处理当前时刻输入的机会（回测中不推进虚拟时间）
			dl = a.now()
			if !a.virtual {
				dl = dl.Add(ioWindow)
			}
		}
		c, ok, err := a.step(ctx, dl)
		if err != nil {
			stopErr = err
			break
		}
		if ok {
			a.dispatchData(a.dataPulse(c), &fb)
		}
	}
	cause := fmt.Errorf("%w: %w", ErrTaskDetached, stopErr)
	for _, h := range a.tasks {
		a.detach(h, cause, nil)
	}
	a.tasks = nil
	errs := make([]error, 0, len(active)+1)
	for _, h := range active {
		errs = append(errs, h.err)
	}
	return errors.Join(append(errs, a.runtime.Close())...)
}

// closeTasks 是 Close 的任务部分：分离全部活动任务；有任务被中止时返回 ErrTaskDetached。
func (a *API) closeTasks() error {
	if !a.driving.CompareAndSwap(false, true) {
		return ErrDispatchBusy
	}
	defer a.driving.Store(false)
	a.closing = true
	var err error
	for _, h := range a.tasks {
		a.detach(h, ErrTaskDetached, nil)
		err = ErrTaskDetached
	}
	a.tasks = nil
	return err
}
