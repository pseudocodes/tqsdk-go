// Package core 实现协议语义：连接路由、diff 解码、类型化状态、提交与变化记录。
//
// 一个 Runtime 串行推进所有路由的输入：每个 rtn_data envelope 是一个提交单位，
// 整包解码成功后在写锁内安装，释放锁后按序发布独立副本。驱动方式二选一：
// 后台 Run（独立 market/trade 客户端）或手动 Step（tqapi.WaitUpdate）。
// 本地逻辑包有两种来源：Driver 取代网络输入（回测）；Backend 与网络行情协同，
// 把一帧行情与其触发的本地账户结果作为一个提交（实时模拟交易）。
package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// DataState 是对象、窗口或账户的可用状态。
type DataState uint8

const (
	Pending DataState = iota // 已登记，尚未收到当前连接代次的数据
	Ready                    // 当前代次数据可用
	Stale                    // 连接断开，内容为旧代次
	Deleted                  // 服务端删除
)

func (s DataState) String() string {
	switch s {
	case Pending:
		return "Pending"
	case Ready:
		return "Ready"
	case Stale:
		return "Stale"
	case Deleted:
		return "Deleted"
	}
	return fmt.Sprintf("DataState(%d)", uint8(s))
}

// Stamp 标识一次提交。
type Stamp struct {
	Epoch      uint64 // Runtime 内提交序号
	Route      uint32 // 物理来源；本地控制提交为 0
	Generation uint64 // 该来源的连接代次
	Sequence   uint64 // 该代次内的本地接收顺序
}

// Source 是一条物理连接。Read 返回的字节归调用方所有。
type Source interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
	Close() error
}

// Prepared 是一帧解码后的结果；Check、Apply 在 Runtime 写锁内调用。
type Prepared struct {
	RtnData bool                         // 是 rtn_data，解除该连接的 pending peek
	Apply   func(Stamp) (Applied, error) // nil 表示无需安装（如 rsp_login、rtn_brokers）
	Check   func() error                 // 可选预检：不修改状态；本地逻辑包在安装任何部分前调用
}

// Applied 是一次安装的结果。Publish 在锁外、下一次提交前调用。
type Applied struct {
	Changed bool // 有需要交付的结果
	Changes any  // 领域变化批次（*MarketChanges / *TradingChanges）
	Publish func()
}

// Decode 解码一帧；失败时整帧不安装。
type Decode func([]byte) (Prepared, error)

// Commit 是 Step 返回的一个可观察提交。网络提交只含一个组件；本地逻辑包（回测）可同时含
// 行情与交易组件，其余组件经不可变的 extra 链保存，Commit 本身保持可比较。
type Commit struct {
	Stamp       Stamp
	runtimeID   uint64
	componentID uint64
	changes     any
	extra       *commitPart
	at          int64 // 业务时间，Unix 纳秒
}

type commitPart struct {
	comp    uint64
	changes any
	next    *commitPart
}

// Time 返回提交的业务时间：网络提交为安装时的本机时间，本地逻辑包为其业务（虚拟）时间。
func (c Commit) Time() time.Time {
	if c.at == 0 {
		return time.Time{}
	}
	return time.Unix(0, c.at)
}

// changesFor 返回提交中属于组件 comp 的变化；没有则为 nil。
func (c Commit) changesFor(comp uint64) any {
	if c.componentID == comp {
		return c.changes
	}
	for p := c.extra; p != nil; p = p.next {
		if p.comp == comp {
			return p.changes
		}
	}
	return nil
}

var (
	ErrClosed         = errors.New("tqsdk: closed")
	ErrDriverConflict = errors.New("tqsdk: runtime already driven by Run or Step")
	ErrConcurrentStep = errors.New("tqsdk: concurrent Step call")
	ErrQueueFull      = errors.New("tqsdk: command queue full")
	ErrNotConnected   = errors.New("tqsdk: route not connected")
	ErrNotSent        = errors.New("tqsdk: command not sent")
	ErrSlowConsumer   = errors.New("tqsdk: slow consumer, subscription terminated")
	ErrNotReady       = errors.New("tqsdk: result not ready")
	ErrWakeReached    = errors.New("tqsdk: wake deadline reached")
)

const (
	driverNone int32 = iota
	driverRun
	driverStep
)

var runtimeIDs atomic.Uint64

// Runtime 串行推进所有已绑定来源的输入，并用一把读写锁保护已提交状态。
type Runtime struct {
	id     uint64
	mu     sync.RWMutex
	inbox  chan inbound
	done   chan struct{}
	driver atomic.Int32
	step   atomic.Bool
	ids    atomic.Uint64 // 组件、节点、窗口身份
	wake   chan struct{} // 本地命令已接受

	// 以下字段在推进开始前设置，之后只由推进者访问。
	local     Driver  // 本地驱动（回测）
	backend   Backend // 与网络行情协同的本地账户后端（实时模拟）
	backendMD uint64  // backend 协同的行情组件
	localErr  error   // 本地执行失败后锁定的错误

	// 以下字段受 mu 保护。
	epoch   uint64
	routes  []*route
	nextRID uint32
	closers []func()
	closed  bool
}

// NewRuntime 创建一个尚未驱动的 Runtime。
func NewRuntime() *Runtime {
	return &Runtime{
		id:    runtimeIDs.Add(1),
		inbox: make(chan inbound, 256),
		done:  make(chan struct{}),
		wake:  make(chan struct{}, 1),
	}
}

func (rt *Runtime) newID() uint64 { return rt.ids.Add(1) }

// Close 关闭全部路由并使组件不可用；幂等。
func (rt *Runtime) Close() error {
	rt.mu.Lock()
	if rt.closed {
		rt.mu.Unlock()
		return nil
	}
	rt.closed = true
	close(rt.done)
	routes := rt.routes
	rt.routes = nil
	closers := rt.closers
	rt.closers = nil
	for _, r := range routes {
		r.shutdown()
	}
	rt.mu.Unlock()
	for i := len(closers) - 1; i >= 0; i-- {
		closers[i]()
	}
	return nil
}

// onClose 登记 Runtime 关闭时调用的清理函数（在锁外调用）。
func (rt *Runtime) onClose(fn func()) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.closers = append(rt.closers, fn)
}

// Run 持续推进直到 ctx 结束或 Runtime 关闭。与 Step 互斥；装有本地驱动时只能用 Step。
func (rt *Runtime) Run(ctx context.Context) error {
	if rt.local != nil || !rt.driver.CompareAndSwap(driverNone, driverRun) {
		return ErrDriverConflict
	}
	for {
		if _, _, err := rt.once(ctx); err != nil {
			return err
		}
	}
}

// Step 推进直到返回一个可观察提交、ctx 结束或 Runtime 关闭。不可并发调用。
func (rt *Runtime) Step(ctx context.Context) (Commit, error) { return rt.StepUntil(ctx, time.Time{}) }

// StepUntil 推进直到返回一个可观察提交、wake 到达、ctx 结束或 Runtime 关闭；wake 为零值时等同 Step。
// 仅 wake 到达时返回 ErrWakeReached。网络驱动下 wake 是本机时间期限；
// 本地驱动下 wake 是虚拟时间，由驱动决定是否推进到 wake。
func (rt *Runtime) StepUntil(ctx context.Context, wake time.Time) (Commit, error) {
	if !rt.driver.CompareAndSwap(driverNone, driverStep) && rt.driver.Load() != driverStep {
		return Commit{}, ErrDriverConflict
	}
	if !rt.step.CompareAndSwap(false, true) {
		return Commit{}, ErrConcurrentStep
	}
	defer rt.step.Store(false)
	if rt.local != nil {
		return rt.stepLocal(ctx, wake)
	}
	if !wake.IsZero() {
		sctx, cancel := context.WithDeadline(ctx, wake)
		defer cancel()
		c, err := rt.stepNetwork(sctx)
		if err != nil && ctx.Err() == nil && sctx.Err() != nil {
			return Commit{}, ErrWakeReached
		}
		return c, err
	}
	return rt.stepNetwork(ctx)
}

func (rt *Runtime) stepNetwork(ctx context.Context) (Commit, error) {
	for {
		c, ok, err := rt.once(ctx)
		if err != nil {
			return Commit{}, err
		}
		if ok {
			return c, nil
		}
	}
}

// once 处理一个输入。装有本地账户后端时，先让后端处理已接受的本地命令与到期事件，
// 它们优先于下一份尚未发布的网络输入。
func (rt *Runtime) once(ctx context.Context) (Commit, bool, error) {
	if rt.localErr != nil {
		return Commit{}, false, rt.localErr
	}
	rt.requestPeeks()
	var timer <-chan time.Time
	if rt.backend != nil {
		c, ok, next, err := rt.pollBackend()
		if err != nil || ok {
			return c, ok, err
		}
		if !next.IsZero() {
			t := time.NewTimer(time.Until(next))
			defer t.Stop()
			timer = t.C
		}
	}
	select {
	case in := <-rt.inbox:
		return rt.handle(in)
	case <-rt.wake:
		return Commit{}, false, nil
	case <-timer:
		return Commit{}, false, nil
	case <-ctx.Done():
		return Commit{}, false, ctx.Err()
	case <-rt.done:
		return Commit{}, false, ErrClosed
	}
}

// inbound 是进入 Runtime 的一条输入：报文、读失败、新连接或本地控制更新。
type inbound struct {
	r    *route
	gen  uint64
	seq  uint64
	data []byte
	at   time.Time // reader 收到报文的本机时间
	err  error
	src  Source              // 重连得到的新连接
	ctrl func(Stamp) Applied // 本地控制提交
	comp uint64              // ctrl 所属组件
}

// post 把输入放入队列；队列满时阻塞（背压），ctx/Runtime 结束时放弃。
func (rt *Runtime) post(ctx context.Context, in inbound) bool {
	select {
	case rt.inbox <- in:
		return true
	case <-ctx.Done():
		return false
	case <-rt.done:
		return false
	}
}

// postControl 投递一个本地控制提交；只在 Runtime 推进时执行。
func (rt *Runtime) postControl(comp uint64, fn func(Stamp) Applied) {
	go rt.post(context.Background(), inbound{ctrl: fn, comp: comp})
}

func (rt *Runtime) handle(in inbound) (Commit, bool, error) {
	if in.ctrl != nil {
		rt.mu.Lock()
		st := Stamp{Epoch: rt.epoch + 1}
		a := in.ctrl(st)
		return rt.finish(st, in.comp, a)
	}
	r := in.r
	if in.gen != r.gen { // 旧代次的迟到输入
		if in.src != nil {
			go in.src.Close()
		}
		return Commit{}, false, nil
	}
	switch {
	case in.src != nil:
		return rt.routeUp(r, in.src)
	case in.err != nil:
		return rt.routeDown(r, in.err)
	}
	p, err := r.decode(in.data)
	if err != nil {
		return rt.routeDown(r, fmt.Errorf("decode: %w", err))
	}
	if p.RtnData {
		r.peek = false
		if rt.backend != nil && r.comp == rt.backendMD {
			return rt.coupleMarket(r, in, p)
		}
	}
	if p.Apply == nil {
		return Commit{}, false, nil
	}
	rt.mu.Lock()
	st := Stamp{Epoch: rt.epoch + 1, Route: r.id, Generation: r.gen, Sequence: in.seq}
	a, err := p.Apply(st)
	if err != nil {
		rt.mu.Unlock()
		return rt.routeDown(r, fmt.Errorf("apply: %w", err))
	}
	return rt.finish(st, r.comp, a)
}

// finish 在持有写锁时调用：确定 Epoch、解锁、发布。
func (rt *Runtime) finish(st Stamp, comp uint64, a Applied) (Commit, bool, error) {
	if a.Changed {
		rt.epoch = st.Epoch
	}
	rt.mu.Unlock()
	if a.Publish != nil {
		a.Publish()
	}
	if !a.Changed {
		return Commit{}, false, nil
	}
	return Commit{Stamp: st, runtimeID: rt.id, componentID: comp, changes: a.Changes, at: time.Now().UnixNano()}, true, nil
}

// requestPeeks 为每条已连接且没有 pending peek 的路由登记一个 peek_message。
func (rt *Runtime) requestPeeks() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, r := range rt.routes {
		if r.up && !r.local && !r.peek && r.send(peekMessage, nil) == nil {
			r.peek = true
		}
	}
}

var peekMessage = []byte(`{"aid":"peek_message"}`)
