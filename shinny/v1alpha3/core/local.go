package core

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Driver 是本地推进源（回测）。装入 Runtime 后，Step/StepUntil 不再读取网络 inbox，
// 而是在推进者 goroutine 中调用 Next 取得下一个逻辑包；Next 之外不得修改策略可见状态。
type Driver interface {
	// Next 返回下一个逻辑包。wake 为调度器给出的虚拟期限（零值表示无）：驱动在下一事件晚于 wake 时
	// 把虚拟时间推进到 wake 并返回 ErrWakeReached。等待数据时在 ctx 内阻塞，返回 ctx 错误时不得丢失状态。
	Next(ctx context.Context, wake time.Time) (Packet, error)
	// Now 返回已提交的虚拟时间。
	Now() time.Time
}

// Packet 是一个逻辑包：同一业务时刻的行情与各账户结果，作为一个 Commit 原子安装。
type Packet struct {
	Time  time.Time
	Parts []PacketPart
}

// PacketPart 是逻辑包中发往一个本地组件的 rtn_data 报文；Market 与 Account 二选一。
type PacketPart struct {
	Market  *Market
	Account *AccountHandle
	Frame   []byte
}

// Backend 是与网络行情同一 Runtime 推进的本地账户后端（实时模拟交易）。两个方法都在推进者
// goroutine 中、写锁外调用；返回的账户报文与触发它的输入作为一个 Commit 原子安装。
// 返回错误表示本地执行失败：该输入不安装，Runtime 此后的 Step 都返回该错误。
type Backend interface {
	// OnMarket 在一帧行情 rtn_data 安装前调用，recv 为 reader 收到该帧的本机时间；
	// 返回因该帧行情产生的账户报文（撮合、估值、日结）。
	OnMarket(frame []byte, recv time.Time) ([]PacketPart, error)
	// Poll 在每次等待输入前调用：处理一个与网络输入无关的本地事件（已接受的命令、定时日结），
	// 没有时返回空 parts。next 为下一次需要 Poll 的本机时间，零值表示只在新输入或命令唤醒时再调用。
	Poll(now time.Time) (parts []PacketPart, next time.Time, err error)
}

// SetDriver 装入本地驱动；必须在任何 Run/Step 之前调用。
func (rt *Runtime) SetDriver(d Driver) error {
	if rt.driver.Load() != driverNone || rt.backend != nil {
		return ErrDriverConflict
	}
	rt.local = d
	return nil
}

// SetBackend 装入与行情组件 m 协同推进的本地账户后端；必须在任何 Run/Step 之前调用。
// 本地账户接受命令时唤醒阻塞中的 Step。
func (rt *Runtime) SetBackend(m *Market, b Backend) error {
	if rt.driver.Load() != driverNone || rt.local != nil || rt.backend != nil {
		return ErrDriverConflict
	}
	rt.backend, rt.backendMD = b, m.id
	return nil
}

// signal 唤醒等待输入的推进者（本地命令已接受）；不阻塞。
func (rt *Runtime) signal() {
	select {
	case rt.wake <- struct{}{}:
	default:
	}
}

// pollBackend 让本地后端处理一个本地事件。
func (rt *Runtime) pollBackend() (Commit, bool, time.Time, error) {
	parts, next, err := rt.backend.Poll(time.Now())
	if err != nil {
		rt.localErr = fmt.Errorf("tqsdk: local backend: %w", err)
		return Commit{}, false, next, rt.localErr
	}
	if len(parts) == 0 {
		return Commit{}, false, next, nil
	}
	c, ok, err := rt.applyPacket(Packet{Time: time.Now(), Parts: parts})
	if err != nil {
		rt.localErr = err
	}
	return c, ok, next, err
}

// coupleMarket 把一帧已解码的行情与本地后端据此产生的账户报文组成一个逻辑包安装。
func (rt *Runtime) coupleMarket(r *route, in inbound, p Prepared) (Commit, bool, error) {
	parts, err := rt.backend.OnMarket(in.data, in.at)
	if err == nil {
		var items []packetItem
		if items, err = decodeParts(parts); err == nil {
			if p.Apply != nil {
				items = append([]packetItem{{comp: r.comp, prep: p}}, items...)
			}
			var c Commit
			var ok bool
			if c, ok, err = rt.install(items, time.Now()); err == nil {
				return c, ok, nil
			}
		}
	}
	if !errors.Is(err, ErrClosed) {
		rt.localErr = fmt.Errorf("tqsdk: local backend: %w", err)
		err = rt.localErr
	}
	return Commit{}, false, err
}

// Now 返回业务时钟：装有本地驱动时为其虚拟时间，否则为本机时间。
func (rt *Runtime) Now() time.Time {
	if rt.local != nil {
		return rt.local.Now()
	}
	return time.Now()
}

func (rt *Runtime) stepLocal(ctx context.Context, wake time.Time) (Commit, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Commit{}, err
		}
		select {
		case <-rt.done:
			return Commit{}, ErrClosed
		default:
		}
		p, err := rt.local.Next(ctx, wake)
		if err != nil {
			return Commit{}, err
		}
		c, ok, err := rt.applyPacket(p)
		if err != nil {
			return Commit{}, err
		}
		if ok {
			return c, nil
		}
	}
}

type packetItem struct {
	comp uint64
	prep Prepared
}

// applyPacket 解码并安装一个逻辑包。
func (rt *Runtime) applyPacket(p Packet) (Commit, bool, error) {
	items, err := decodeParts(p.Parts)
	if err != nil {
		return Commit{}, false, err
	}
	return rt.install(items, p.Time)
}

func decodeParts(parts []PacketPart) ([]packetItem, error) {
	items := make([]packetItem, 0, len(parts))
	for _, part := range parts {
		var it packetItem
		var err error
		switch {
		case part.Market != nil:
			it.comp = part.Market.id
			it.prep, err = part.Market.decodeMarket(part.Frame)
		case part.Account != nil:
			it.comp = part.Account.t.id
			it.prep, err = part.Account.decodeTrading(part.Frame)
		}
		if err != nil {
			return nil, fmt.Errorf("tqsdk: decode local packet: %w", err)
		}
		if it.prep.Apply != nil {
			items = append(items, it)
		}
	}
	return items, nil
}

// install 在同一写锁内先预检全部交易部分，再依次安装，分配一个 Epoch。
// MD 安装不会失败，TD 只在成交冲突时失败且已由预检排除，因此不会出现半提交。
func (rt *Runtime) install(items []packetItem, at time.Time) (Commit, bool, error) {
	rt.mu.Lock()
	if rt.closed {
		rt.mu.Unlock()
		return Commit{}, false, ErrClosed
	}
	for _, it := range items {
		if it.prep.Check != nil {
			if err := it.prep.Check(); err != nil {
				rt.mu.Unlock()
				return Commit{}, false, fmt.Errorf("tqsdk: local packet rejected: %w", err)
			}
		}
	}
	st := Stamp{Epoch: rt.epoch + 1}
	var parts []commitPart
	var pubs []func()
	for _, it := range items {
		a, err := it.prep.Apply(st)
		if err != nil { // 预检已排除，出现即为实现错误
			rt.mu.Unlock()
			return Commit{}, false, fmt.Errorf("tqsdk: local packet apply: %w", err)
		}
		if a.Publish != nil {
			pubs = append(pubs, a.Publish)
		}
		if a.Changed {
			parts = addPart(parts, it.comp, a.Changes)
		}
	}
	if len(parts) > 0 {
		rt.epoch = st.Epoch
	}
	rt.mu.Unlock()
	for _, fn := range pubs {
		fn()
	}
	if len(parts) == 0 {
		return Commit{}, false, nil
	}
	c := Commit{Stamp: st, runtimeID: rt.id, componentID: parts[0].comp, changes: parts[0].changes, at: at.UnixNano()}
	for i := len(parts) - 1; i >= 1; i-- {
		c.extra = &commitPart{comp: parts[i].comp, changes: parts[i].changes, next: c.extra}
	}
	return c, true, nil
}

// addPart 按组件归并变化：同一 Trading 下多个账户的变化合并为一份 tradingChanges。
func addPart(parts []commitPart, comp uint64, ch any) []commitPart {
	for i := range parts {
		if parts[i].comp == comp {
			if dst, ok := parts[i].changes.(*tradingChanges); ok {
				dst.merge(ch.(*tradingChanges))
			}
			return parts
		}
	}
	return append(parts, commitPart{comp: comp, changes: ch})
}

func (c *tradingChanges) merge(o *tradingChanges) {
	c.accounts = mergeMap(c.accounts, o.accounts)
	c.positions = mergeMap(c.positions, o.positions)
	c.orders = mergeMap(c.orders, o.orders)
	c.riskRules = mergeMap(c.riskRules, o.riskRules)
	c.riskData = mergeMap(c.riskData, o.riskData)
	c.trades = mergeMap(c.trades, o.trades)
	c.states = mergeMap(c.states, o.states)
	c.notifies = mergeMap(c.notifies, o.notifies)
	c.riskReqs = append(c.riskReqs, o.riskReqs...)
	c.errors = append(c.errors, o.errors...)
}

func mergeMap[K comparable, V any](dst, src map[K]V) map[K]V {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = make(map[K]V, len(src))
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
