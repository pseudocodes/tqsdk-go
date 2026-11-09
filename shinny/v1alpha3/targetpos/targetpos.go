// Package targetpos 是目标持仓执行工具（对应 tqsdk-python 3.10.4 的 TargetPosTask）：
// 给定一个账户合约的目标净仓，按开平优先级平今、平昨、开仓，追价直到成交。
//
// 任务在 New 时注册到 tqapi 的协作式调度器，之后只需调用 api.WaitUpdate 即可推进；
// New、SetTarget、Cancel 与 WaitUpdate 须在同一个 goroutine 调用。实盘、paper trade 与回测共用同一实现。
//
// 与 Python 相同，合约被任务控制期间不允许对该账户该合约做任何外部下单或撤单，风险由使用者自担：
// 自有子单被拒或被非本任务撤销（含日终 GFD 撤单）、出现外部在场委托或无法解释的持仓变化，
// 都作为致命错误处理——停止新单、撤掉自有在途单、对账后以 Failed 结束任务。
package targetpos

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/internal/tday"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/tqapi"
)

// ReplaceMode 决定执行中收到新目标时的处理方式。
type ReplaceMode uint8

const (
	FinishCurrent ReplaceMode = iota // 当前轮执行完毕后再处理最新目标（同 Python）
	Latest                           // 撤回当前轮在途单、对账后立即处理最新目标
)

// Config 配置一个目标持仓任务。零值字段使用默认值。
type Config struct {
	Symbol         string
	OffsetPriority string      // 开平优先级，默认 "今昨,开"；今=平今、昨=平昨、开=开仓、逗号=等待之前的组完成
	ReplaceMode    ReplaceMode // 默认 FinishCurrent
	Price          PricePolicy // 默认 Active

	// 大单拆分（Python min_volume / max_volume）：须同时设置且 MaxVolume >= MinVolume > 0。
	// 剩余手数 >= MaxVolume 时每笔取 [MinVolume, MaxVolume] 内的随机手数，否则一次报出剩余手数。
	MinVolume, MaxVolume int64
	Seed                 uint64 // 拆单随机数种子；0 时回测由账户 ID 与合约派生固定种子，其余情况随机

	// SupportOpenMinVolume 允许交易有最小开仓手数限制的合约（Python support_open_min_volume），
	// 此时持仓只能调整到 |当前 − 目标| < 最小开仓手数。
	SupportOpenMinVolume bool

	DependencyTimeout time.Duration // 行情、合约资料、账户与持仓对账的等待上限，默认 30s；非交易时段的等待不计入
	DrainTimeout      time.Duration // 停止后撤单与对账的等待上限，默认 30s；超时报告未确认

	// Session 决定能否开始一轮下单，默认 TradingTime（同 Python）。须为可比较类型，自定义函数请用 SessionFunc 包装。
	Session SessionPolicy
}

// SessionPolicy 判断现在能否开始一轮调仓。每轮开始前调用；返回 false 时任务等待（不计入依赖期限），
// 等待期间只保留最新目标，之后每次数据更新再判断。轮中不调用，已在场的委托继续跟踪。
// now 为估计的交易所时间（北京时间）：回测为虚拟时间，实盘与 paper 为最近行情时间加上之后的本机流逝。
// 实现须短小、不阻塞，不调用 WaitUpdate。
type SessionPolicy interface {
	InSession(now time.Time, ins model.Instrument) bool
}

// TradingTime 是默认时段规则（Python _is_in_trading_time）：now 落在合约当前交易日的交易时段内。
type TradingTime struct{}

// InSession 实现 SessionPolicy。
func (TradingTime) InSession(now time.Time, ins model.Instrument) bool {
	return tday.InTradingTime(ins.TradingTime, now.UnixNano())
}

type sessionFunc struct {
	f func(time.Time, model.Instrument) bool
}

func (s *sessionFunc) InSession(now time.Time, ins model.Instrument) bool { return s.f(now, ins) }

// SessionFunc 把自定义时段函数包装为 SessionPolicy；按指针身份比较，同一合约复用任务时须传入同一个返回值。
func SessionFunc(f func(now time.Time, ins model.Instrument) bool) SessionPolicy {
	return &sessionFunc{f}
}

const (
	defaultTimeout = 30 * time.Second
	retryInterval  = 500 * time.Millisecond // 报撤单遇到可重试错误后的重试间隔
)

var (
	ErrInvalidBinding = errors.New("targetpos: account is not bound to this api")
	ErrConfigMismatch = errors.New("targetpos: task exists with a different config")
	ErrTaskClosed     = errors.New("targetpos: task closed")
	ErrTaskStopping   = errors.New("targetpos: task stopping")
	ErrUnconfirmed    = errors.New("targetpos: own orders unconfirmed")
	ErrNotReady       = core.ErrNotReady
)

// futureExchanges 是支持的交易所；KQ 为主连与指数，sim 可交易，便于回测。
var futureExchanges = map[string]bool{"SHFE": true, "INE": true, "DCE": true, "CZCE": true, "CFFEX": true, "GFEX": true, "KQ": true}

func (c *Config) normalize() ([]string, error) {
	if c.OffsetPriority == "" {
		c.OffsetPriority = "今昨,开"
	}
	if c.Price == nil {
		c.Price = Active
	}
	if c.Session == nil {
		c.Session = TradingTime{}
	}
	if c.DependencyTimeout == 0 {
		c.DependencyTimeout = defaultTimeout
	}
	if c.DrainTimeout == 0 {
		c.DrainTimeout = defaultTimeout
	}
	exchange, _, _ := strings.Cut(c.Symbol, ".")
	switch {
	case !futureExchanges[exchange]:
		return nil, fmt.Errorf("%w: targetpos supports futures exchanges only (%s)", core.ErrUnsupported, c.Symbol)
	case !reflect.TypeOf(c.Price).Comparable():
		return nil, errors.New("targetpos: Price must be comparable, wrap functions with PriceFunc")
	case !reflect.TypeOf(c.Session).Comparable():
		return nil, errors.New("targetpos: Session must be comparable, wrap functions with SessionFunc")
	case c.DependencyTimeout < 0 || c.DrainTimeout < 0:
		return nil, errors.New("targetpos: negative timeout")
	case c.MinVolume < 0 || c.MaxVolume < 0 || (c.MinVolume == 0) != (c.MaxVolume == 0) || c.MinVolume > c.MaxVolume:
		return nil, fmt.Errorf("targetpos: 最小下单手数(min_volume) %d 和 最大下单手数(max_volume) %d 错误, 须同时填写且 max_volume >= min_volume > 0",
			c.MinVolume, c.MaxVolume)
	}
	return parsePriority(c.OffsetPriority)
}

// mismatch 返回两份已规范化配置中第一个不同的字段说明；相同时为空。
func mismatch(a, b Config) string {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	for i := range va.NumField() {
		if x, y := va.Field(i).Interface(), vb.Field(i).Interface(); x != y {
			return fmt.Sprintf("您试图用不同的 %s 参数创建两个 %s 调仓任务, 原为 %v, 现为 %v", va.Type().Field(i).Name, a.Symbol, x, y)
		}
	}
	return ""
}

type regKey struct {
	acc    *core.AccountHandle
	symbol string
}

// registry 保证每个账户句柄的每个合约只有一个任务（Python TargetPosTaskSingleton）。
var registry = struct {
	sync.Mutex
	m map[regKey]*Task
}{m: map[regKey]*Task{}}

// New 创建目标持仓任务并注册到 api 的任务调度器；不等待数据、不下单。
// 同一账户同一合约已有未结束的任务时：配置相同则返回该任务，不同则返回 ErrConfigMismatch。
// ctx 只用于构造。
func New(ctx context.Context, api *tqapi.API, account *core.AccountHandle, cfg Config) (*Task, error) {
	if api == nil || api.Market == nil || api.Trade == nil || account == nil {
		return nil, ErrInvalidBinding
	}
	if bound, ok := api.Trade.Core().Account(account.ID()); !ok || bound != account {
		return nil, ErrInvalidBinding
	}
	groups, err := cfg.normalize()
	if err != nil {
		return nil, err
	}
	key := regKey{account, cfg.Symbol}
	registry.Lock()
	defer registry.Unlock()
	if t := registry.m[key]; t != nil {
		if msg := mismatch(t.cfg, cfg); msg != "" {
			return nil, fmt.Errorf("%w: %s", ErrConfigMismatch, msg)
		}
		return t, nil
	}
	t := &Task{api: api, acc: account, cfg: cfg, key: key, groups: groups, orders: map[string]*child{}, seen: map[string]bool{}}
	t.exchange, _, _ = strings.Cut(cfg.Symbol, ".")
	seed := cfg.Seed
	if seed == 0 {
		if api.Backtest != nil {
			h := fnv.New64a()
			h.Write([]byte(account.ID() + "#" + cfg.Symbol))
			seed = h.Sum64()
		} else {
			seed = rand.Uint64()
		}
	}
	t.rng = rand.New(rand.NewPCG(seed, seed>>1|1))
	if t.quote, err = api.Market.Quote(ctx, cfg.Symbol); err != nil {
		return nil, err
	}
	if t.h, err = api.RegisterTask(&runner{t}); err != nil {
		t.quote.Close()
		return nil, err
	}
	registry.m[key] = t
	return t, nil
}

func (t *Task) unregister() {
	registry.Lock()
	if registry.m[t.key] == t {
		delete(registry.m, t.key)
	}
	registry.Unlock()
}

// Outcome 是一个目标版本的结果。
type Outcome uint8

const (
	Reached     Outcome = iota + 1 // 达到目标
	Partial                        // 本轮结束但未达目标（可平量不足、最小开仓手数尾差）
	Superseded                     // 被更新的目标取代
	Cancelled                      // 任务被取消
	Failed                         // 致命错误
	Unconfirmed                    // 停止后自有委托未能在期限内确认结清
)

func (o Outcome) String() string {
	return [...]string{"", "Reached", "Partial", "Superseded", "Cancelled", "Failed", "Unconfirmed"}[o]
}

// Fill 是一笔自有成交；Signed 为带符号手数，BUY 为正、SELL 为负（同 Python trade_chan，表示净仓变化方向）。
type Fill struct {
	Version uint64 // 所属目标版本
	Trade   model.Trade
	Signed  int64
}

// Result 是一个目标版本的终结结果。
type Result struct {
	Version      uint64
	Target       int64
	Outcome      Outcome
	ObservedNet  int64 // 按自有成交推算的结束时净仓
	Remaining    int64 // Target − ObservedNet
	Settled      bool  // 本版本的自有委托均已结清
	SupersededBy uint64
	Traded       int64 // 本版本自有成交的带符号手数合计
	Fills        []Fill
	Reason       string
	Cause        error
}

// Ticket 是一次 SetTarget 的结果票据。Done/DoneC/Result/Wait 可在任意 goroutine 调用；
// 不要在驱动 WaitUpdate 的 goroutine 上阻塞 Wait。
type Ticket struct {
	version uint64
	target  int64
	done    chan struct{}
	res     Result
}

func newTicket(version uint64, target int64) *Ticket {
	return &Ticket{version: version, target: target, done: make(chan struct{})}
}

func (q *Ticket) finish(r Result) {
	r.Version, r.Target = q.version, q.target
	q.res = r
	close(q.done)
}

// Version 返回目标版本号。
func (q *Ticket) Version() uint64 { return q.version }

// Done 报告票据是否已终结。
func (q *Ticket) Done() bool {
	select {
	case <-q.done:
		return true
	default:
		return false
	}
}

// DoneC 返回终结时关闭的 channel。
func (q *Ticket) DoneC() <-chan struct{} { return q.done }

// Result 返回结果；未终结时返回 ErrNotReady。
func (q *Ticket) Result() (Result, error) {
	if !q.Done() {
		return Result{}, ErrNotReady
	}
	return q.res, nil
}

// Wait 等待票据终结；ctx 只影响本次等待。
func (q *Ticket) Wait(ctx context.Context) (Result, error) {
	select {
	case <-q.done:
		return q.res, nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

// Phase 是任务阶段。
type Phase string

const (
	PhaseIdle        Phase = "Idle"        // 等待目标
	PhaseWaiting     Phase = "Waiting"     // 有目标，等待交易时段或依赖
	PhaseRunning     Phase = "Running"     // 执行中
	PhaseReplacing   Phase = "Replacing"   // Latest：撤回当前轮以执行新目标
	PhaseStopping    Phase = "Stopping"    // 撤单与对账收尾中
	PhaseUnconfirmed Phase = "Unconfirmed" // 收尾超时，仍在跟踪
	PhaseClosed      Phase = "Closed"
)

// OrderSummary 是一笔自有委托的摘要。
type OrderSummary struct {
	OrderID   string
	Direction model.Direction
	Offset    model.Offset
	Volume    int64
	Price     float64
	Filled    int64
	Status    model.OrderState
}

// Snapshot 是任务状态的副本。
type Snapshot struct {
	Phase          Phase
	ActiveVersion  uint64
	PendingVersion uint64
	StopRequested  bool
	OwnedOrders    []OrderSummary // 尚未结清的自有委托
	WaitReason     string
}

// CloseResult 是任务收尾报告。读取成功不代表结清，须检查 Confirmed。
type CloseResult struct {
	Confirmed  bool
	Unresolved []OrderSummary
	Cause      error
}
