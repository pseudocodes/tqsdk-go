package tqapi

import (
	"errors"
	"fmt"
	"time"
)

// Task 是随 WaitUpdate 推进的协作式任务。Poll 必须短小、非阻塞：可读取快照、调用普通报撤单接口，
// 不等待成交、channel 或票据，不 sleep，不重入 WaitUpdate/Shutdown/Close。
type Task interface {
	Poll(input TaskInput) (TaskStep, error)
}

// TaskInput 是一次 Poll 的输入值，不应保存为历史快照。
type TaskInput struct {
	Now      time.Time
	Pulse    Pulse // 数据回调时为本次 core 提交（不含任务反馈）；首次执行、Wake、期限到达时为空
	Stopping bool  // 请求取消后，此后每次 Poll 都为 true
}

// TaskStep 是 Poll 返回的完整等待声明，覆盖上一次的声明；Done=true 时忽略等待字段。
type TaskStep struct {
	WatchData bool      // 后续数据提交是否需要回调
	WakeAt    time.Time // 下次检查期限；零值表示无定时需求
	Changed   bool      // 工具状态或结果发生可观察变化
	Done      bool      // 已完成自身收尾，可以注销
}

// TaskDetachHandler 是可选接口：强制结束调度时只做本地收尾（保存未确认事实、释放本地资源），
// 不报撤单、不等待、不重入驱动。
type TaskDetachHandler interface {
	OnDetach(cause error)
}

var (
	ErrDispatchBusy   = errors.New("tqapi: dispatch in progress")
	ErrTaskRegistered = errors.New("tqapi: task already registered")
	ErrTaskLimit      = errors.New("tqapi: too many tasks")
	ErrTaskDetached   = errors.New("tqapi: task detached before done")
)

// TaskPanicError 是 Poll panic 后的分离原因。
type TaskPanicError struct {
	Value any
	Stack []byte
}

func (e *TaskPanicError) Error() string { return fmt.Sprintf("tqapi: task panic: %v", e.Value) }

// TaskHandle 是已登记任务的句柄。全部方法只能在 owner goroutine 调用。
type TaskHandle struct {
	task         Task // 结束后置 nil
	pending      bool // 需要执行一次 Poll
	stopping     bool
	stopNotified bool // 已交付过 Stopping=true 的回调
	watch        bool
	wakeAt       time.Time
	done         bool
	err          error
}

// Wake 请求下一次调度执行一次 Poll；多次调用合并。已结束时无操作。
func (h *TaskHandle) Wake() {
	if !h.done {
		h.pending = true
	}
}

// Cancel 请求任务收尾：此后 Poll 均带 Stopping=true，并保证一次清理回调。不立即注销任务。
func (h *TaskHandle) Cancel() {
	if h.done || h.stopping {
		return
	}
	h.stopping, h.pending = true, true
}

// Done 报告任务是否已结束调度；不表示委托已结清。
func (h *TaskHandle) Done() bool { return h.done }

// Err 返回首个任务错误，以及强制分离原因；Done=false 时也可能非 nil。
func (h *TaskHandle) Err() error { return h.err }

func (h *TaskHandle) firstStop() bool {
	return !h.done && h.pending && h.stopping && !h.stopNotified
}

func (h *TaskHandle) ready(now time.Time) bool {
	return !h.done && (h.pending || (!h.wakeAt.IsZero() && !now.Before(h.wakeAt)))
}

func (h *TaskHandle) finish() {
	h.task, h.done, h.pending = nil, true, false
}
