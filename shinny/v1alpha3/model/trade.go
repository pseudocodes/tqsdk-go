package model

// Direction 是买卖方向。
type Direction string

// Offset 是开平标志。
type Offset string

// OrderState 是委托单状态（线上 status 字段）。
type OrderState string

const (
	DirectionBuy  Direction = "BUY"
	DirectionSell Direction = "SELL"

	OffsetOpen       Offset = "OPEN"
	OffsetClose      Offset = "CLOSE"
	OffsetCloseToday Offset = "CLOSETODAY"

	OrderAlive    OrderState = "ALIVE"
	OrderFinished OrderState = "FINISHED"
)

// TradingSessionInfo 是交易服务端 session 元数据（trade.<user>.session）。
type TradingSessionInfo struct {
	UserID     string
	TradingDay string
}

// Account 是某币种的资金记录，字段与 Python tqsdk.objs.Account 及线上 accounts 分支对应。
type Account struct {
	UserID               string  `tq:"user_id"`
	AccountID            string  `tq:"account_id"`
	Currency             string  `tq:"currency"`
	PreBalance           float64 `tq:"pre_balance"`
	StaticBalance        float64 `tq:"static_balance"`
	Balance              float64 `tq:"balance"`
	Available            float64 `tq:"available"`
	CtpBalance           float64 `tq:"ctp_balance"`
	CtpAvailable         float64 `tq:"ctp_available"`
	FloatProfit          float64 `tq:"float_profit"`
	PositionProfit       float64 `tq:"position_profit"`
	CloseProfit          float64 `tq:"close_profit"`
	FrozenMargin         float64 `tq:"frozen_margin"`
	Margin               float64 `tq:"margin"`
	FrozenCommission     float64 `tq:"frozen_commission"`
	Commission           float64 `tq:"commission"`
	FrozenPremium        float64 `tq:"frozen_premium"`
	Premium              float64 `tq:"premium"`
	Deposit              float64 `tq:"deposit"`
	Withdraw             float64 `tq:"withdraw"`
	RiskRatio            float64 `tq:"risk_ratio"`
	MarketValue          float64 `tq:"market_value"`
	PreOptionMarketValue float64 `tq:"pre_option_market_value"`
	Valid                AccountFields
}

// Position 是单合约持仓。pos_* 与 volume_* 分别映射，不互相推导。
type Position struct {
	UserID       string `tq:"user_id"`
	ExchangeID   string `tq:"exchange_id"`
	InstrumentID string `tq:"instrument_id"`

	PosLongHis    int64 `tq:"pos_long_his"`
	PosLongToday  int64 `tq:"pos_long_today"`
	PosShortHis   int64 `tq:"pos_short_his"`
	PosShortToday int64 `tq:"pos_short_today"`

	VolumeLongToday        int64 `tq:"volume_long_today"`
	VolumeLongHis          int64 `tq:"volume_long_his"`
	VolumeLong             int64 `tq:"volume_long"`
	VolumeLongYd           int64 `tq:"volume_long_yd"`
	VolumeLongFrozenToday  int64 `tq:"volume_long_frozen_today"`
	VolumeLongFrozenHis    int64 `tq:"volume_long_frozen_his"`
	VolumeLongFrozen       int64 `tq:"volume_long_frozen"`
	VolumeShortToday       int64 `tq:"volume_short_today"`
	VolumeShortHis         int64 `tq:"volume_short_his"`
	VolumeShort            int64 `tq:"volume_short"`
	VolumeShortYd          int64 `tq:"volume_short_yd"`
	VolumeShortFrozenToday int64 `tq:"volume_short_frozen_today"`
	VolumeShortFrozenHis   int64 `tq:"volume_short_frozen_his"`
	VolumeShortFrozen      int64 `tq:"volume_short_frozen"`

	OpenPriceLong          float64 `tq:"open_price_long"`
	OpenPriceShort         float64 `tq:"open_price_short"`
	OpenCostLong           float64 `tq:"open_cost_long"`
	OpenCostLongToday      float64 `tq:"open_cost_long_today"`
	OpenCostLongHis        float64 `tq:"open_cost_long_his"`
	OpenCostShort          float64 `tq:"open_cost_short"`
	OpenCostShortToday     float64 `tq:"open_cost_short_today"`
	OpenCostShortHis       float64 `tq:"open_cost_short_his"`
	PositionPriceLong      float64 `tq:"position_price_long"`
	PositionPriceShort     float64 `tq:"position_price_short"`
	PositionCostLong       float64 `tq:"position_cost_long"`
	PositionCostLongToday  float64 `tq:"position_cost_long_today"`
	PositionCostLongHis    float64 `tq:"position_cost_long_his"`
	PositionCostShort      float64 `tq:"position_cost_short"`
	PositionCostShortToday float64 `tq:"position_cost_short_today"`
	PositionCostShortHis   float64 `tq:"position_cost_short_his"`
	FloatProfitLong        float64 `tq:"float_profit_long"`
	FloatProfitShort       float64 `tq:"float_profit_short"`
	FloatProfit            float64 `tq:"float_profit"`
	PositionProfitLong     float64 `tq:"position_profit_long"`
	PositionProfitShort    float64 `tq:"position_profit_short"`
	PositionProfit         float64 `tq:"position_profit"`
	MarginLong             float64 `tq:"margin_long"`
	MarginLongToday        float64 `tq:"margin_long_today"`
	MarginLongHis          float64 `tq:"margin_long_his"`
	MarginShort            float64 `tq:"margin_short"`
	MarginShortToday       float64 `tq:"margin_short_today"`
	MarginShortHis         float64 `tq:"margin_short_his"`
	Margin                 float64 `tq:"margin"`
	MarketValueLong        float64 `tq:"market_value_long"`
	MarketValueShort       float64 `tq:"market_value_short"`
	MarketValue            float64 `tq:"market_value"`
	LastPrice              float64 `tq:"last_price"`
	MarketStatus           int64   `tq:"market_status"`

	Valid PositionFields
}

// PosLong 返回多头持仓手数（今仓+昨仓），同 Python Position.pos_long。
func (p Position) PosLong() int64 { return p.PosLongHis + p.PosLongToday }

// PosShort 返回空头持仓手数（今仓+昨仓），同 Python Position.pos_short。
func (p Position) PosShort() int64 { return p.PosShortHis + p.PosShortToday }

// Pos 返回净持仓（多头-空头），同 Python Position.pos。
func (p Position) Pos() int64 { return p.PosLong() - p.PosShort() }

// Order 是委托单，字段与线上 orders 分支对应。
type Order struct {
	SeqNo           int64      `tq:"seqno"`
	UserID          string     `tq:"user_id"`
	OrderID         string     `tq:"order_id"`
	ExchangeOrderID string     `tq:"exchange_order_id"`
	ExchangeID      string     `tq:"exchange_id"`
	InstrumentID    string     `tq:"instrument_id"`
	Direction       Direction  `tq:"direction"`
	Offset          Offset     `tq:"offset"`
	OffsetLeg2      Offset     `tq:"offset_leg2"`
	VolumeOriginal  int64      `tq:"volume_orign"` // 线上拼写 volume_orign
	VolumeLeft      int64      `tq:"volume_left"`
	LimitPrice      float64    `tq:"limit_price"`
	PriceType       string     `tq:"price_type"`
	VolumeCondition string     `tq:"volume_condition"`
	TimeCondition   string     `tq:"time_condition"`
	HedgeFlag       string     `tq:"hedge_flag"`
	InsertDateTime  int64      `tq:"insert_date_time"` // Unix 纳秒
	LastMsg         string     `tq:"last_msg"`
	Status          OrderState `tq:"status"`
	Valid           OrderFields
}

// IsDead 表示委托单已确定不会再有新成交，同 Python Order.is_dead。
func (o Order) IsDead() bool { return o.Status == OrderFinished }

// IsOnline 表示委托单已被交易所接受且仍在排队，同 Python Order.is_online。
func (o Order) IsOnline() bool { return o.ExchangeOrderID != "" && o.Status == OrderAlive }

// IsError 表示委托单未被交易所接受即结束，同 Python Order.is_error。
func (o Order) IsError() bool { return o.ExchangeOrderID == "" && o.Status == OrderFinished }

// Trade 是一笔成交；成为完整事实后不再改变。
type Trade struct {
	SeqNo           int64     `tq:"seqno"`
	UserID          string    `tq:"user_id"`
	TradeID         string    `tq:"trade_id"`
	OrderID         string    `tq:"order_id"`
	ExchangeTradeID string    `tq:"exchange_trade_id"`
	ExchangeID      string    `tq:"exchange_id"`
	InstrumentID    string    `tq:"instrument_id"`
	Direction       Direction `tq:"direction"`
	Offset          Offset    `tq:"offset"`
	HedgeFlag       string    `tq:"hedge_flag"`
	Price           float64   `tq:"price"`
	Volume          int64     `tq:"volume"`
	TradeDateTime   int64     `tq:"trade_date_time"` // Unix 纳秒
	Commission      float64   `tq:"commission"`
	Valid           TradeFields
}

// AveragePrice 返回一组成交的成交均价，总量为 0 时返回 NaN；
// 与 Python Order.trade_price 的计算方式一致。
func AveragePrice(trades []Trade) float64 {
	var volume int64
	var amount float64
	for _, t := range trades {
		volume += t.Volume
		amount += float64(t.Volume) * t.Price
	}
	if volume == 0 {
		return nan()
	}
	return amount / float64(volume)
}

// Notify 是服务端或连接层通知。
type Notify struct {
	ID        string // 线上 notify 键
	Type      string `tq:"type"`  // MESSAGE / TEXT / ...
	Level     string `tq:"level"` // INFO / WARNING / ERROR
	Code      int64  `tq:"code"`
	Content   string `tq:"content"`
	AccountID string `tq:"account_id"`
	BrokerID  string `tq:"broker_id"`
	SessionID int64  `tq:"session_id"`
	Valid     NotifyFields
}
