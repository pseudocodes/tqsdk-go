package core

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// PositionMode 是序列窗口的定位方式。
type PositionMode uint8

const (
	Latest PositionMode = iota // 跟随最新数据（零值）
	ByTime                     // 以 DatetimeNS 为焦点，FocusPosition 为其在窗口中的下标
	ByID                       // 从 LeftID 开始
)

// WindowPosition 描述窗口定位；非当前模式的字段必须为零。
type WindowPosition struct {
	Mode          PositionMode
	DatetimeNS    int64 // ByTime：焦点时间，Unix 纳秒（0 是合法时间）
	FocusPosition int   // ByTime：焦点在窗口中的下标，0..Width-1
	LeftID        int64 // ByID：窗口左端 ID
}

// KlineRequest 是单合约 K 线请求。
type KlineRequest struct {
	Symbol   string
	Period   time.Duration // 正整秒；超过一天时为整日倍数；最大 28 天
	Width    int           // 1..10000
	Position WindowPosition
}

// TickRequest 是单合约 Tick 请求。
type TickRequest struct {
	Symbol   string
	Width    int
	Position WindowPosition
}

// AlignmentMode 是多合约 K 线的输出方式。
type AlignmentMode uint8

const (
	PrimarySlots AlignmentMode = iota // 按主合约逻辑 ID 输出，缺腿保留空槽
	MatchedRows                       // 只输出所有腿都存在的主 ID，取最近 Width 个（同 Python 多合约 K 线）
)

// AlignedKlineRequest 是多合约对齐 K 线请求；Symbols[0] 为主合约。
type AlignedKlineRequest struct {
	Symbols  []string
	Period   time.Duration
	Width    int
	Position WindowPosition
	Mode     AlignmentMode
}

// Slot 是窗口中的一个逻辑位置。Present 表示该 ID 有真实行，Valid 是该行的字段有效位。
type Slot[T any, F ~uint16] struct {
	Value   T
	Present bool
	Valid   model.Fields[F]
}

type (
	KlineSlot = Slot[model.Kline, model.KlineField]
	TickSlot  = Slot[model.Tick, model.TickField]
)

// IDRange 是闭区间 [LeftID, RightID]。
type IDRange struct{ LeftID, RightID int64 }

func (r IDRange) contains(id int64) bool { return id >= r.LeftID && id <= r.RightID }

// WindowInfo 描述窗口当前视图。
type WindowInfo struct {
	Range          IDRange // 源主轴范围
	RangeKnown     bool    // 能否定位窗口
	RequestedWidth int
	ValidCount     int    // 窗口内真实行数（MatchedRows 为完整匹配行数）
	SerialReady    bool   // 与 State==Ready 同步
	FrameFull      bool   // Ready 且 Width 个位置都有真实行
	ViewVersion    uint64 // 基线替换版本；普通滚动不变
}

// Frame 是一个窗口的完整快照。
type Frame[T any, F ~uint16] struct {
	Rows         []Slot[T, F]
	SeriesLastID int64
	Window       WindowInfo
	State        DataState
	Stamp        Stamp
}

type (
	KlineFrame = Frame[model.Kline, model.KlineField]
	TickFrame  = Frame[model.Tick, model.TickField]
)

// SeriesEvent 是序列订阅的一次交付。
//   - Shifted=true：首次基线、基线替换或窗口范围移动；
//   - Rows：基线时为整窗，其余为本批相关行（可能包含刚离开窗口的旧柱最终值）。
//   - Frame.Rows：默认在 Ready 事件中提供完整窗口（WithoutFrame 可关闭）；不改变 Rows 的增量语义。
type SeriesEvent[T any, F ~uint16] struct {
	Symbol  string
	Period  time.Duration // Tick 为 0
	Shifted bool
	Window  WindowInfo
	State   DataState
	Rows    []Slot[T, F]
	Frame   *Frame[T, F] // State==Ready 时生成；WithoutFrame 时恒为 nil
	Stamp   Stamp
}

type (
	KlineEvent = SeriesEvent[model.Kline, model.KlineField]
	TickEvent  = SeriesEvent[model.Tick, model.TickField]
)

// AlignedRow 是多合约对齐的一行；Legs 与请求 Symbols 顺序一致。
type AlignedRow struct {
	PrimaryID int64 // MatchedRows 的前置占位为 -1
	Legs      []KlineSlot
}

// AlignedKlineFrame 是多合约对齐窗口快照。
type AlignedKlineFrame struct {
	Symbols      []string
	Rows         []AlignedRow
	RowKeys      []int64
	Window       WindowInfo
	SeriesLastID int64
	State        DataState
	Stamp        Stamp
}

// AlignedKlineEvent 是多合约对齐订阅的一次交付。
type AlignedKlineEvent struct {
	Symbols []string
	Shifted bool
	Window  WindowInfo
	State   DataState
	Rows    []AlignedRow
	RowKeys []int64 // 基线或位置映射改变时为完整位置表
	Frame   *AlignedKlineFrame
	Stamp   Stamp
}

// SeriesOption 配置序列订阅。
type SeriesOption func(*seriesOptions)

type seriesOptions struct {
	withFrame bool
	mailbox   int
}

// WithFrame 让每个 Ready 事件附带完整窗口副本，读取方式为 e.Frame.Rows。
// 这是默认行为，显式传入仅用于表达意图或覆盖之前的 WithoutFrame。
// e.Rows 仍保留增量语义；非 Ready 事件的 e.Frame 为 nil。
func WithFrame() SeriesOption { return func(o *seriesOptions) { o.withFrame = true } }

// WithoutFrame 关闭完整窗口副本，事件只携带增量 e.Rows，e.Frame 恒为 nil。
// 适合宽窗口、只消费增量的订阅，省去每个 Ready 事件的整窗复制。
func WithoutFrame() SeriesOption { return func(o *seriesOptions) { o.withFrame = false } }

// WithMailboxSize 设置订阅邮箱容量。
func WithMailboxSize(n int) SeriesOption { return func(o *seriesOptions) { o.mailbox = n } }

func applyOptions(opts []SeriesOption) seriesOptions {
	o := seriesOptions{withFrame: true} // 默认附带完整窗口
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// QuoteSnapshot 是一次读取得到的报价副本。Stamp 为读取时的 Runtime 视点。
type QuoteSnapshot struct {
	Value model.Quote
	Valid model.QuoteFields
	State DataState
	Stamp Stamp
}

// QuoteEvent 是报价订阅的一次交付。
type QuoteEvent struct {
	Quote model.Quote
	Valid model.QuoteFields
	State DataState
	Stamp Stamp
}

// RouteError 是某条路由的断线或协议错误。
type RouteError struct {
	Route      uint32
	Generation uint64
	Err        error
}

func (e RouteError) Error() string {
	return fmt.Sprintf("route %d gen %d: %v", e.Route, e.Generation, e.Err)
}

// ErrInvalidRequest 包装全部本地参数校验错误。
var ErrInvalidRequest = errors.New("tqsdk: invalid request")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, a...))
}

const (
	maxWidth  = 10000
	day       = 24 * time.Hour
	maxPeriod = 28 * day
)

func validateSymbol(s string) error {
	if s == "" {
		return invalid("empty symbol")
	}
	return nil
}

func validatePeriod(p time.Duration) error {
	switch {
	case p <= 0 || p%time.Second != 0:
		return invalid("kline period %v must be positive whole seconds", p)
	case p > day && p%day != 0:
		return invalid("kline period %v above one day must be whole days", p)
	case p > maxPeriod:
		return invalid("kline period %v exceeds 28 days", p)
	}
	return nil
}

func validateWindow(width int, pos WindowPosition) error {
	if width < 1 || width > maxWidth {
		return invalid("width %d out of range 1..%d", width, maxWidth)
	}
	switch pos.Mode {
	case Latest:
		if pos.DatetimeNS != 0 || pos.FocusPosition != 0 || pos.LeftID != 0 {
			return invalid("latest position must not set DatetimeNS/FocusPosition/LeftID")
		}
	case ByTime:
		if pos.LeftID != 0 {
			return invalid("by-time position must not set LeftID")
		}
		if pos.FocusPosition < 0 || pos.FocusPosition >= width {
			return invalid("focus position %d out of range 0..%d", pos.FocusPosition, width-1)
		}
	case ByID:
		if pos.DatetimeNS != 0 || pos.FocusPosition != 0 {
			return invalid("by-id position must not set DatetimeNS/FocusPosition")
		}
		if pos.LeftID < 0 || pos.LeftID > math.MaxInt64-int64(width) {
			return invalid("left id %d out of range", pos.LeftID)
		}
	default:
		return invalid("unknown position mode %d", pos.Mode)
	}
	return nil
}
