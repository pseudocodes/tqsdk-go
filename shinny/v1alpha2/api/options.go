package api

type TickSubOptions struct {
	ClientTag string
	EmitFrame bool
}

type TickSubOption interface {
	applyTick(*TickSubOptions)
}

type tickSubOptionFunc func(*TickSubOptions)

func (f tickSubOptionFunc) applyTick(o *TickSubOptions) {
	f(o)
}

func WithTickClientTag(tag string) TickSubOption {
	return tickSubOptionFunc(func(o *TickSubOptions) {
		o.ClientTag = tag
	})
}

func WithTickEmitFrame(v bool) TickSubOption {
	return tickSubOptionFunc(func(o *TickSubOptions) {
		o.EmitFrame = v
	})
}

func ResolveTickSubOptions(opts ...TickSubOption) TickSubOptions {
	out := TickSubOptions{EmitFrame: true}
	for _, o := range opts {
		if o == nil {
			continue
		}
		o.applyTick(&out)
	}
	return out
}

type KlineSubOptions struct {
	ClientTag string
	AlignMode KlineAlignMode
	EmitFrame bool
}

type KlineSubOption interface {
	applyKline(*KlineSubOptions)
}

type klineSubOptionFunc func(*KlineSubOptions)

func (f klineSubOptionFunc) applyKline(o *KlineSubOptions) {
	f(o)
}

func WithKlineClientTag(tag string) KlineSubOption {
	return klineSubOptionFunc(func(o *KlineSubOptions) {
		o.ClientTag = tag
	})
}

func WithKlineAlignMode(mode KlineAlignMode) KlineSubOption {
	return klineSubOptionFunc(func(o *KlineSubOptions) {
		o.AlignMode = mode
	})
}

func WithKlineEmitFrame(v bool) KlineSubOption {
	return klineSubOptionFunc(func(o *KlineSubOptions) {
		o.EmitFrame = v
	})
}

func ResolveKlineSubOptions(opts ...KlineSubOption) KlineSubOptions {
	out := KlineSubOptions{
		AlignMode: AlignStrictByBinding,
		EmitFrame: true,
	}
	for _, o := range opts {
		if o == nil {
			continue
		}
		o.applyKline(&out)
	}
	return out
}

type InsertOrderOptions struct {
	Offset     Offset
	LimitPrice *float64
	PriceMode  string
	Advanced   AdvancedType
	OrderID    string
	ClientTag  string
}

type InsertOrderOption interface {
	applyInsert(*InsertOrderOptions)
}

type insertOrderOptionFunc func(*InsertOrderOptions)

func (f insertOrderOptionFunc) applyInsert(o *InsertOrderOptions) {
	f(o)
}

func WithOffset(offset Offset) InsertOrderOption {
	return insertOrderOptionFunc(func(o *InsertOrderOptions) {
		o.Offset = offset
	})
}

func WithLimitPrice(price float64) InsertOrderOption {
	return insertOrderOptionFunc(func(o *InsertOrderOptions) {
		o.LimitPrice = &price
		o.PriceMode = "LIMIT"
	})
}

func WithAdvanced(v AdvancedType) InsertOrderOption {
	return insertOrderOptionFunc(func(o *InsertOrderOptions) {
		o.Advanced = v
	})
}

func ResolveInsertOrderOptions(opts ...InsertOrderOption) InsertOrderOptions {
	out := InsertOrderOptions{Offset: OffsetOpen, Advanced: AdvancedNone}
	for _, o := range opts {
		if o == nil {
			continue
		}
		o.applyInsert(&out)
	}
	return out
}

func WithBestPrice() InsertOrderOption {
	return insertOrderOptionFunc(func(o *InsertOrderOptions) {
		o.PriceMode = "BEST"
		o.LimitPrice = nil
	})
}

func WithFiveLevelPrice() InsertOrderOption {
	return insertOrderOptionFunc(func(o *InsertOrderOptions) {
		o.PriceMode = "FIVELEVEL"
		o.LimitPrice = nil
	})
}

func WithOrderID(orderID string) InsertOrderOption {
	return insertOrderOptionFunc(func(o *InsertOrderOptions) {
		o.OrderID = orderID
	})
}

func WithOrderClientTag(tag string) InsertOrderOption {
	return insertOrderOptionFunc(func(o *InsertOrderOptions) {
		o.ClientTag = tag
	})
}

type TradeSubOptions struct{}

type TradeSubOption interface {
	applyTradeSub(*TradeSubOptions)
}

type tradeSubOptionFunc func(*TradeSubOptions)

func (f tradeSubOptionFunc) applyTradeSub(o *TradeSubOptions) {
	f(o)
}

type RiskRuleOptions struct {
	SelfTradeCountLimit        *int64
	FrequentInsertCountLimit   *int64
	FrequentCancelCountLimit   *int64
	FrequentCancelPercentLimit *float64
	TradePositionUnitsLimit    *int64
	TradePositionRatioLimit    *float64
}

type RiskRuleOption interface {
	applyRisk(*RiskRuleOptions)
}

type riskRuleOptionFunc func(*RiskRuleOptions)

func (f riskRuleOptionFunc) applyRisk(o *RiskRuleOptions) {
	f(o)
}

func WithSelfTradeCountLimit(v int64) RiskRuleOption {
	return riskRuleOptionFunc(func(o *RiskRuleOptions) {
		o.SelfTradeCountLimit = &v
	})
}

func WithFrequentInsertCountLimit(v int64) RiskRuleOption {
	return riskRuleOptionFunc(func(o *RiskRuleOptions) {
		o.FrequentInsertCountLimit = &v
	})
}

func WithFrequentCancelCountLimit(v int64) RiskRuleOption {
	return riskRuleOptionFunc(func(o *RiskRuleOptions) {
		o.FrequentCancelCountLimit = &v
	})
}

func WithFrequentCancelPercentLimit(v float64) RiskRuleOption {
	return riskRuleOptionFunc(func(o *RiskRuleOptions) {
		o.FrequentCancelPercentLimit = &v
	})
}

func WithTradePositionUnitsLimit(v int64) RiskRuleOption {
	return riskRuleOptionFunc(func(o *RiskRuleOptions) {
		o.TradePositionUnitsLimit = &v
	})
}

func WithTradePositionRatioLimit(v float64) RiskRuleOption {
	return riskRuleOptionFunc(func(o *RiskRuleOptions) {
		o.TradePositionRatioLimit = &v
	})
}

func ResolveRiskRuleOptions(opts ...RiskRuleOption) RiskRuleOptions {
	out := RiskRuleOptions{}
	for _, o := range opts {
		if o == nil {
			continue
		}
		o.applyRisk(&out)
	}
	return out
}
