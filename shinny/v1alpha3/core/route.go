package core

import (
	"context"
	"math/rand/v2"
	"sync/atomic"
	"time"
)

// routeHandler 是一条路由所属组件对连接生命周期的响应，均在 Runtime 写锁内调用。
type routeHandler interface {
	// down 在连接断开后调用：状态置 Stale、失败等待者。
	down(r *route, st Stamp, err error) Applied
	// up 在新连接代次建立后调用：重放持续意图，状态置 Pending。
	up(r *route, st Stamp) Applied
}

// route 是一条物理连接及其代次。一个 reader、一个有序 writer、一个 pending peek。
type route struct {
	rt      *Runtime
	id      uint32
	comp    uint64
	decode  Decode
	handler routeHandler
	dial    func(context.Context) (Source, error) // nil 表示注入的 Source，不支持重连
	queue   int
	recvSeq atomic.Uint64 // reader 已接收的消息序号（跨代次单调）

	// 以下字段受 rt.mu 保护；gen/peek 只由 Runtime 推进者修改。
	gen    uint64
	src    Source
	out    chan outMsg
	cancel context.CancelFunc
	up     bool
	closed bool
	peek   bool

	// 本地路由（回测）：没有连接与 goroutine；send 同步进入 outbox，由本地驱动在 Step 中取走。
	local  bool
	outbox [][]byte
}

// attachLocalRoute 绑定一条本地路由（第 1 代，始终在线）。
func (rt *Runtime) attachLocalRoute(comp uint64, decode Decode, h routeHandler) (*route, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.closed {
		return nil, ErrClosed
	}
	rt.nextRID++
	r := &route{rt: rt, id: rt.nextRID, comp: comp, decode: decode, handler: h, local: true, gen: 1, up: true}
	rt.routes = append(rt.routes, r)
	return r, nil
}

// takeOutbox 取走本地路由已发送的报文；调用方持有 rt.mu。
func (r *route) takeOutbox() [][]byte {
	out := r.outbox
	r.outbox = nil
	return out
}

type outMsg struct {
	data []byte
	done func(error) // writer 完成写入（或确定未发送）后调用，可为 nil
}

// attachRoute 绑定一条已建立的连接为第 1 代并启动 reader/writer。
// onAttach 在同一临界区内执行（如排入 req_login），保证先于任何 peek_message。
func (rt *Runtime) attachRoute(comp uint64, src Source, decode Decode, h routeHandler,
	dial func(context.Context) (Source, error), queue int, onAttach func(*route) error) (*route, error) {
	if queue <= 0 {
		queue = 1024
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.closed {
		return nil, ErrClosed
	}
	rt.nextRID++
	r := &route{rt: rt, id: rt.nextRID, comp: comp, decode: decode, handler: h, dial: dial, queue: queue}
	r.start(src)
	if onAttach != nil {
		if err := onAttach(r); err != nil {
			r.cancel() // 失败时不关闭 src：注入的 Source 仍归调用者
			r.up, r.closed = false, true
			return nil, err
		}
	}
	rt.routes = append(rt.routes, r)
	return r, nil
}

// start 以 src 开始新代次；调用方持有 rt.mu。
func (r *route) start(src Source) {
	r.gen++
	r.src = src
	r.out = make(chan outMsg, r.queue)
	r.up = true
	r.peek = false
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go r.readLoop(ctx, src, r.gen)
	go r.writeLoop(ctx, src, r.out)
}

// send 把一帧放入 writer FIFO；调用方持有 rt.mu。队列满立即失败，不等待网络。
func (r *route) send(data []byte, done func(error)) error {
	switch {
	case r.closed:
		return ErrClosed
	case !r.up:
		return ErrNotConnected
	case r.local:
		r.outbox = append(r.outbox, data)
		if done != nil {
			done(nil)
		}
		r.rt.signal()
		return nil
	}
	select {
	case r.out <- outMsg{data: data, done: done}:
		return nil
	default:
		return ErrQueueFull
	}
}

// canSend 报告 send 是否会成功；调用方持有写锁（此时只有 writer 会减少队列长度）。
func (r *route) canSend() error {
	switch {
	case r.closed:
		return ErrClosed
	case !r.up:
		return ErrNotConnected
	case r.local:
		return nil
	case len(r.out) >= cap(r.out):
		return ErrQueueFull
	}
	return nil
}

func (r *route) readLoop(ctx context.Context, src Source, gen uint64) {
	var seq uint64
	for {
		data, err := src.Read(ctx)
		if err != nil {
			r.rt.post(ctx, inbound{r: r, gen: gen, err: err})
			return
		}
		seq = r.recvSeq.Add(1)
		if !r.rt.post(ctx, inbound{r: r, gen: gen, seq: seq, data: data, at: time.Now()}) {
			return
		}
	}
}

func (r *route) writeLoop(ctx context.Context, src Source, out chan outMsg) {
	failed := false
	for {
		select {
		case m := <-out:
			var err error
			if failed {
				err = ErrNotSent
			} else if err = src.Write(ctx, m.data); err != nil {
				failed = true
				go src.Close() // 让 reader 观察到失败并交给 Runtime 处理
			}
			if m.done != nil {
				m.done(err)
			}
		case <-ctx.Done():
			for {
				select {
				case m := <-out:
					if m.done != nil {
						m.done(ErrNotSent)
					}
				default:
					return
				}
			}
		}
	}
}

// stop 结束当前代次；调用方持有 rt.mu。
func (r *route) stop() {
	if !r.up {
		return
	}
	r.up = false
	if r.local {
		return
	}
	r.cancel()
	go r.src.Close()
}

// shutdown 永久关闭路由；调用方持有 rt.mu。
func (r *route) shutdown() {
	r.stop()
	r.closed = true
}

// close 由组件调用，永久关闭并从 Runtime 移除路由。
func (r *route) close() {
	rt := r.rt
	rt.mu.Lock()
	defer rt.mu.Unlock()
	r.shutdown()
	for i, x := range rt.routes {
		if x == r {
			rt.routes = append(rt.routes[:i], rt.routes[i+1:]...)
			break
		}
	}
}

// routeDown 处理断线或协议错误：整包不安装，提交 Stale 并在可重连时后台重连。
func (rt *Runtime) routeDown(r *route, err error) (Commit, bool, error) {
	rt.mu.Lock()
	if !r.up || r.closed {
		rt.mu.Unlock()
		return Commit{}, false, nil
	}
	r.stop()
	st := Stamp{Epoch: rt.epoch + 1, Route: r.id, Generation: r.gen}
	a := r.handler.down(r, st, err)
	if r.dial != nil {
		go r.redial(r.gen)
	}
	return rt.finish(st, r.comp, a)
}

// routeUp 安装重连得到的新连接代次。
func (rt *Runtime) routeUp(r *route, src Source) (Commit, bool, error) {
	rt.mu.Lock()
	if r.closed || rt.closed {
		rt.mu.Unlock()
		go src.Close()
		return Commit{}, false, nil
	}
	r.start(src)
	st := Stamp{Epoch: rt.epoch + 1, Route: r.id, Generation: r.gen}
	a := r.handler.up(r, st)
	return rt.finish(st, r.comp, a)
}

// redial 按 Python TqConnect 的退避节奏重连：首次立即，第 k 次等待 [s, 2s)，s=min(2^k,64)*10 秒。
func (r *route) redial(gen uint64) {
	for attempt := 0; ; attempt++ {
		var delay time.Duration
		if attempt > 0 {
			s := time.Duration(min(1<<attempt, 64)) * 10 * time.Second
			delay = s + time.Duration(rand.Int64N(int64(s)))
		}
		select {
		case <-time.After(delay):
		case <-r.rt.done:
			return
		}
		r.rt.mu.RLock()
		closed := r.closed
		r.rt.mu.RUnlock()
		if closed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		src, err := r.dial(ctx)
		cancel()
		if err == nil {
			if !r.rt.post(context.Background(), inbound{r: r, gen: gen, src: src}) {
				src.Close()
			}
			return
		}
	}
}
