package core

import "github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"

// ObjectChange 是一个对象在一次提交内的净变化：起点与终点的存在性、有效性或值不同。
// 同一包内 10→11→10 不算变化；两侧均无效（NaN）视为相同。
type ObjectChange[F ~uint16] struct {
	Fields  model.Fields[F]
	Created bool
	Deleted bool
	Status  bool // DataState 改变
}

// Has 报告字段 f 是否净变化。
func (c ObjectChange[F]) Has(f F) bool { return c.Fields.Has(f) }

// Any 报告是否有任何变化（包括仅状态变化）。
func (c ObjectChange[F]) Any() bool { return c.Created || c.Deleted || c.Status || c.Fields.Any() }

func (c ObjectChange[F]) business() bool { return c.Created || c.Deleted || c.Fields.Any() }

type (
	QuoteChange          = ObjectChange[model.QuoteField]
	AccountChange        = ObjectChange[model.AccountField]
	PositionChange       = ObjectChange[model.PositionField]
	OrderChange          = ObjectChange[model.OrderField]
	RemoteRiskRuleChange = ObjectChange[model.RemoteRiskRuleField]
	RemoteRiskDataChange = ObjectChange[model.RemoteRiskDataField]
)

// RowChange 是序列中一行的变化。新行或以新身份进入窗口的行，Fields 为字段全集。
type RowChange[F ~uint16] struct {
	ID      int64
	Fields  model.Fields[F]
	Created bool
	Deleted bool
}

// Has 报告字段 f 是否变化。
func (c RowChange[F]) Has(f F) bool { return c.Fields.Has(f) }

type (
	KlineRowChange = RowChange[model.KlineField]
	TickRowChange  = RowChange[model.TickField]
)

// rowIdent 是某一时刻窗口末行的身份。
type rowIdent struct {
	known   bool
	id      int64
	present bool
}

// SeriesChange 是一个单合约窗口在一次提交内的变化。
type SeriesChange[T any, F ~uint16] struct {
	Rows          []RowChange[F] // 本批相关行的变化，按 ID 升序
	FinalRows     []Slot[T, F]   // 与 Rows 对应的本批最终值（离窗后仍可读）
	WindowMoved   bool
	StatusChanged bool

	lastBefore, lastAfter rowIdent
}

// Any 报告是否有任何变化（包括仅状态变化）。
func (c *SeriesChange[T, F]) Any() bool {
	return c != nil && (len(c.Rows) != 0 || c.WindowMoved || c.StatusChanged)
}

type (
	KlineChange = SeriesChange[model.Kline, model.KlineField]
	TickChange  = SeriesChange[model.Tick, model.TickField]
)

// AlignedKlineChange 是多合约窗口的变化。
type AlignedKlineChange struct {
	Rows          []AlignedRow // 内容或存在性改变的输出行
	KeysChanged   bool         // 输出位置表（RowKeys / 范围）改变
	StatusChanged bool
}

// Any 报告是否有任何变化。
func (c *AlignedKlineChange) Any() bool {
	return c != nil && (len(c.Rows) != 0 || c.KeysChanged || c.StatusChanged)
}

// TradesChange 是成交集合的变化。Reset=true 时 Added 为空，应以当前快照重建基线。
type TradesChange struct {
	Reset bool
	Added []model.Trade
}

// marketChanges 是行情组件一次提交的变化记录；提交后只读。
type marketChanges struct {
	quotes  map[uint64]QuoteChange
	series  map[uint64]any // *KlineChange / *TickChange
	aligned map[uint64]*AlignedKlineChange
	queries []string
	errors  []RouteError
}

// MarketChanges 是某次提交中行情组件的变化；其他组件的提交返回空值。
type MarketChanges struct{ p *marketChanges }

// Quote 返回报价的净变化。
func (c MarketChanges) Quote(q *QuoteRef) QuoteChange {
	if c.p == nil || q == nil {
		return QuoteChange{}
	}
	return c.p.quotes[q.node.id]
}

// Klines 返回 K 线窗口的变化；无变化返回 nil。
func (c MarketChanges) Klines(w *Klines) *KlineChange {
	if c.p == nil || w == nil {
		return nil
	}
	ch, _ := c.p.series[w.id].(*KlineChange)
	return ch
}

// Ticks 返回 Tick 窗口的变化；无变化返回 nil。
func (c MarketChanges) Ticks(w *Ticks) *TickChange {
	if c.p == nil || w == nil {
		return nil
	}
	ch, _ := c.p.series[w.id].(*TickChange)
	return ch
}

// AlignedKlines 返回多合约窗口的变化；无变化返回 nil。
func (c MarketChanges) AlignedKlines(w *AlignedKlines) *AlignedKlineChange {
	if c.p == nil || w == nil {
		return nil
	}
	return c.p.aligned[w.id]
}

// QueryIDs 返回本提交完成的请求 ID。
func (c MarketChanges) QueryIDs() []string {
	if c.p == nil {
		return nil
	}
	return append([]string(nil), c.p.queries...)
}

// Errors 返回本提交的路由错误。
func (c MarketChanges) Errors() []RouteError {
	if c.p == nil {
		return nil
	}
	return append([]RouteError(nil), c.p.errors...)
}

func (c *marketChanges) empty() bool {
	return len(c.quotes) == 0 && len(c.series) == 0 && len(c.aligned) == 0 &&
		len(c.queries) == 0 && len(c.errors) == 0
}
