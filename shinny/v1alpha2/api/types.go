package api

import "time"

type Direction string

const (
	DirectionBuy  Direction = "BUY"
	DirectionSell Direction = "SELL"
)

type Offset string

const (
	OffsetOpen       Offset = "OPEN"
	OffsetClose      Offset = "CLOSE"
	OffsetCloseToday Offset = "CLOSETODAY"
)

type AdvancedType string

const (
	AdvancedNone AdvancedType = ""
	AdvancedFAK  AdvancedType = "FAK"
	AdvancedFOK  AdvancedType = "FOK"
)

type KlineAlignMode string

const (
	AlignStrictByBinding KlineAlignMode = "strict_binding"
	AlignMainWithNA      KlineAlignMode = "main_with_na"
)

type EventKind string

const (
	EventSnapshot EventKind = "snapshot"
	EventAppend   EventKind = "append"
	EventUpdate   EventKind = "update"
)

type TradeEventKind string

const (
	TradeEventSnapshot TradeEventKind = "snapshot"
	TradeEventUpsert   TradeEventKind = "upsert"
	TradeEventDelete   TradeEventKind = "delete"
)

type TradeSessionState string

const (
	TradeSessionConnecting TradeSessionState = "connecting"
	TradeSessionWsReady    TradeSessionState = "ws_ready"
	TradeSessionLoggingIn  TradeSessionState = "logging_in"
	TradeSessionRecovering TradeSessionState = "recovering"
	TradeSessionReady      TradeSessionState = "ready"
	TradeSessionClosed     TradeSessionState = "closed"
)

type TradeStatusCode string

const (
	TradeStatusAuctionOrdering TradeStatusCode = "AUCTIONORDERING"
	TradeStatusContinous       TradeStatusCode = "CONTINOUS"
	TradeStatusNoTrading       TradeStatusCode = "NOTRADING"
)

type NotifyLevel string

const (
	NotifyInfo    NotifyLevel = "INFO"
	NotifyWarning NotifyLevel = "WARNING"
	NotifyError   NotifyLevel = "ERROR"
)

type TradingTime struct {
	Day   [][]string `json:"day"`
	Night [][]string `json:"night"`
}

type CategoryInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Quote struct {
	Symbol                   string         `json:"symbol,omitempty"`
	Datetime                 string         `json:"datetime"`
	AskPrice1                float64        `json:"ask_price1"`
	AskVolume1               int64          `json:"ask_volume1"`
	BidPrice1                float64        `json:"bid_price1"`
	BidVolume1               int64          `json:"bid_volume1"`
	AskPrice2                float64        `json:"ask_price2"`
	AskVolume2               int64          `json:"ask_volume2"`
	BidPrice2                float64        `json:"bid_price2"`
	BidVolume2               int64          `json:"bid_volume2"`
	AskPrice3                float64        `json:"ask_price3"`
	AskVolume3               int64          `json:"ask_volume3"`
	BidPrice3                float64        `json:"bid_price3"`
	BidVolume3               int64          `json:"bid_volume3"`
	AskPrice4                float64        `json:"ask_price4"`
	AskVolume4               int64          `json:"ask_volume4"`
	BidPrice4                float64        `json:"bid_price4"`
	BidVolume4               int64          `json:"bid_volume4"`
	AskPrice5                float64        `json:"ask_price5"`
	AskVolume5               int64          `json:"ask_volume5"`
	BidPrice5                float64        `json:"bid_price5"`
	BidVolume5               int64          `json:"bid_volume5"`
	LastPrice                float64        `json:"last_price"`
	Highest                  float64        `json:"highest"`
	Lowest                   float64        `json:"lowest"`
	Open                     float64        `json:"open"`
	Close                    float64        `json:"close"`
	Average                  float64        `json:"average"`
	Volume                   int64          `json:"volume"`
	Amount                   float64        `json:"amount"`
	OpenInterest             int64          `json:"open_interest"`
	Settlement               float64        `json:"settlement"`
	UpperLimit               float64        `json:"upper_limit"`
	LowerLimit               float64        `json:"lower_limit"`
	PreOpenInterest          int64          `json:"pre_open_interest"`
	PreSettlement            float64        `json:"pre_settlement"`
	PreClose                 float64        `json:"pre_close"`
	PriceTick                float64        `json:"price_tick"`
	PriceDecs                int64          `json:"price_decs"`
	VolumeMultiple           int64          `json:"volume_multiple"`
	MaxLimitOrderVolume      int64          `json:"max_limit_order_volume"`
	MaxMarketOrderVolume     int64          `json:"max_market_order_volume"`
	MinLimitOrderVolume      int64          `json:"min_limit_order_volume"`
	MinMarketOrderVolume     int64          `json:"min_market_order_volume"`
	OpenMaxMarketOrderVolume int64          `json:"open_max_market_order_volume"`
	OpenMaxLimitOrderVolume  int64          `json:"open_max_limit_order_volume"`
	OpenMinMarketOrderVolume int64          `json:"open_min_market_order_volume"`
	OpenMinLimitOrderVolume  int64          `json:"open_min_limit_order_volume"`
	UnderlyingSymbol         string         `json:"underlying_symbol"`
	StrikePrice              float64        `json:"strike_price"`
	InsClass                 string         `json:"ins_class"`
	InstrumentID             string         `json:"instrument_id"`
	InstrumentName           string         `json:"instrument_name"`
	ExchangeID               string         `json:"exchange_id"`
	Expired                  bool           `json:"expired"`
	TradingTime              TradingTime    `json:"trading_time"`
	ExpireDatetime           float64        `json:"expire_datetime"`
	DeliveryYear             int64          `json:"delivery_year"`
	DeliveryMonth            int64          `json:"delivery_month"`
	LastExerciseDatetime     float64        `json:"last_exercise_datetime"`
	ExerciseYear             int64          `json:"exercise_year"`
	ExerciseMonth            int64          `json:"exercise_month"`
	OptionClass              string         `json:"option_class"`
	ExerciseType             string         `json:"exercise_type"`
	ProductID                string         `json:"product_id"`
	IOPV                     float64        `json:"iopv"`
	PublicFloatShareQuantity int64          `json:"public_float_share_quantity"`
	StockDividendRatio       []string       `json:"stock_dividend_ratio"`
	CashDividendRatio        []string       `json:"cash_dividend_ratio"`
	ExpireRestDays           int64          `json:"expire_rest_days"`
	Categories               []CategoryInfo `json:"categories"`
	PositionLimit            int64          `json:"position_limit"`
}

type Kline struct {
	ID       int64   `json:"id"`
	Datetime int64   `json:"datetime"`
	Open     float64 `json:"open"`
	High     float64 `json:"high"`
	Low      float64 `json:"low"`
	Close    float64 `json:"close"`
	Volume   int64   `json:"volume"`
	OpenOI   int64   `json:"open_oi"`
	CloseOI  int64   `json:"close_oi"`
}

type Tick struct {
	ID           int64   `json:"id"`
	Datetime     int64   `json:"datetime"`
	LastPrice    float64 `json:"last_price"`
	Average      float64 `json:"average"`
	Highest      float64 `json:"highest"`
	Lowest       float64 `json:"lowest"`
	AskPrice1    float64 `json:"ask_price1"`
	AskVolume1   int64   `json:"ask_volume1"`
	BidPrice1    float64 `json:"bid_price1"`
	BidVolume1   int64   `json:"bid_volume1"`
	AskPrice2    float64 `json:"ask_price2"`
	AskVolume2   int64   `json:"ask_volume2"`
	BidPrice2    float64 `json:"bid_price2"`
	BidVolume2   int64   `json:"bid_volume2"`
	AskPrice3    float64 `json:"ask_price3"`
	AskVolume3   int64   `json:"ask_volume3"`
	BidPrice3    float64 `json:"bid_price3"`
	BidVolume3   int64   `json:"bid_volume3"`
	AskPrice4    float64 `json:"ask_price4"`
	AskVolume4   int64   `json:"ask_volume4"`
	BidPrice4    float64 `json:"bid_price4"`
	BidVolume4   int64   `json:"bid_volume4"`
	AskPrice5    float64 `json:"ask_price5"`
	AskVolume5   int64   `json:"ask_volume5"`
	BidPrice5    float64 `json:"bid_price5"`
	BidVolume5   int64   `json:"bid_volume5"`
	Volume       int64   `json:"volume"`
	Amount       float64 `json:"amount"`
	OpenInterest int64   `json:"open_interest"`
}

type TradingStatus struct {
	Symbol      string          `json:"symbol"`
	TradeStatus TradeStatusCode `json:"trade_status"`
}

type Account struct {
	Currency         string  `json:"currency"`
	PreBalance       float64 `json:"pre_balance"`
	StaticBalance    float64 `json:"static_balance"`
	Balance          float64 `json:"balance"`
	Available        float64 `json:"available"`
	CTPBalance       float64 `json:"ctp_balance"`
	CTPAvailable     float64 `json:"ctp_available"`
	FloatProfit      float64 `json:"float_profit"`
	PositionProfit   float64 `json:"position_profit"`
	CloseProfit      float64 `json:"close_profit"`
	FrozenMargin     float64 `json:"frozen_margin"`
	Margin           float64 `json:"margin"`
	FrozenCommission float64 `json:"frozen_commission"`
	Commission       float64 `json:"commission"`
	FrozenPremium    float64 `json:"frozen_premium"`
	Premium          float64 `json:"premium"`
	Deposit          float64 `json:"deposit"`
	Withdraw         float64 `json:"withdraw"`
	RiskRatio        float64 `json:"risk_ratio"`
	MarketValue      float64 `json:"market_value"`
}

type Position struct {
	ExchangeID             string  `json:"exchange_id"`
	InstrumentID           string  `json:"instrument_id"`
	PosLongHis             int64   `json:"pos_long_his"`
	PosLongToday           int64   `json:"pos_long_today"`
	PosShortHis            int64   `json:"pos_short_his"`
	PosShortToday          int64   `json:"pos_short_today"`
	VolumeLongToday        int64   `json:"volume_long_today"`
	VolumeLongHis          int64   `json:"volume_long_his"`
	VolumeLong             int64   `json:"volume_long"`
	VolumeLongFrozenToday  int64   `json:"volume_long_frozen_today"`
	VolumeLongFrozenHis    int64   `json:"volume_long_frozen_his"`
	VolumeLongFrozen       int64   `json:"volume_long_frozen"`
	VolumeShortToday       int64   `json:"volume_short_today"`
	VolumeShortHis         int64   `json:"volume_short_his"`
	VolumeShort            int64   `json:"volume_short"`
	VolumeShortFrozenToday int64   `json:"volume_short_frozen_today"`
	VolumeShortFrozenHis   int64   `json:"volume_short_frozen_his"`
	VolumeShortFrozen      int64   `json:"volume_short_frozen"`
	OpenPriceLong          float64 `json:"open_price_long"`
	OpenPriceShort         float64 `json:"open_price_short"`
	OpenCostLong           float64 `json:"open_cost_long"`
	OpenCostShort          float64 `json:"open_cost_short"`
	PositionPriceLong      float64 `json:"position_price_long"`
	PositionPriceShort     float64 `json:"position_price_short"`
	PositionCostLong       float64 `json:"position_cost_long"`
	PositionCostShort      float64 `json:"position_cost_short"`
	FloatProfitLong        float64 `json:"float_profit_long"`
	FloatProfitShort       float64 `json:"float_profit_short"`
	FloatProfit            float64 `json:"float_profit"`
	PositionProfitLong     float64 `json:"position_profit_long"`
	PositionProfitShort    float64 `json:"position_profit_short"`
	PositionProfit         float64 `json:"position_profit"`
	MarginLong             float64 `json:"margin_long"`
	MarginShort            float64 `json:"margin_short"`
	Margin                 float64 `json:"margin"`
	MarketValueLong        float64 `json:"market_value_long"`
	MarketValueShort       float64 `json:"market_value_short"`
	MarketValue            float64 `json:"market_value"`
	Pos                    int64   `json:"pos"`
	PosLong                int64   `json:"pos_long"`
	PosShort               int64   `json:"pos_short"`
}

type Order struct {
	OrderID         string  `json:"order_id"`
	ExchangeOrderID string  `json:"exchange_order_id"`
	ExchangeID      string  `json:"exchange_id"`
	InstrumentID    string  `json:"instrument_id"`
	Direction       string  `json:"direction"`
	Offset          string  `json:"offset"`
	VolumeOrign     int64   `json:"volume_orign"`
	VolumeLeft      int64   `json:"volume_left"`
	LimitPrice      float64 `json:"limit_price"`
	PriceType       string  `json:"price_type"`
	VolumeCondition string  `json:"volume_condition"`
	TimeCondition   string  `json:"time_condition"`
	InsertDateTime  int64   `json:"insert_date_time"`
	LastMsg         string  `json:"last_msg"`
	Status          string  `json:"status"`
	IsDead          *bool   `json:"is_dead"`
	IsOnline        *bool   `json:"is_online"`
	IsError         *bool   `json:"is_error"`
	TradePrice      float64 `json:"trade_price"`
}

type Trade struct {
	OrderID         string  `json:"order_id"`
	TradeID         string  `json:"trade_id"`
	ExchangeTradeID string  `json:"exchange_trade_id"`
	ExchangeID      string  `json:"exchange_id"`
	InstrumentID    string  `json:"instrument_id"`
	Direction       string  `json:"direction"`
	Offset          string  `json:"offset"`
	Price           float64 `json:"price"`
	Volume          int64   `json:"volume"`
	TradeDateTime   int64   `json:"trade_date_time"`
}

type RiskManagementRule struct {
	UserID               string                   `json:"user_id"`
	ExchangeID           string                   `json:"exchange_id"`
	Enable               bool                     `json:"enable"`
	SelfTrade            SelfTradeRule            `json:"self_trade"`
	FrequentCancellation FrequentCancellationRule `json:"frequent_cancellation"`
	TradePositionRatio   TradePositionRatioRule   `json:"trade_position_ratio"`
}

type SelfTradeRule struct {
	CountLimit int64 `json:"count_limit"`
}

type FrequentCancellationRule struct {
	InsertOrderCountLimit   int64   `json:"insert_order_count_limit"`
	CancelOrderCountLimit   int64   `json:"cancel_order_count_limit"`
	CancelOrderPercentLimit float64 `json:"cancel_order_percent_limit"`
}

type TradePositionRatioRule struct {
	TradeUnitsLimit         int64   `json:"trade_units_limit"`
	TradePositionRatioLimit float64 `json:"trade_position_ratio_limit"`
}

type RiskManagementData struct {
	UserID               string                   `json:"user_id"`
	ExchangeID           string                   `json:"exchange_id"`
	InstrumentID         string                   `json:"instrument_id"`
	SelfTrade            SelfTradeData            `json:"self_trade"`
	FrequentCancellation FrequentCancellationData `json:"frequent_cancellation"`
	TradePositionRatio   TradePositionRatioData   `json:"trade_position_ratio"`
}

type SelfTradeData struct {
	HighestBuyPrice float64 `json:"highest_buy_price"`
	LowestSellPrice float64 `json:"lowest_sell_price"`
	SelfTradeCount  int64   `json:"self_trade_count"`
	RejectedCount   int64   `json:"rejected_count"`
}

type FrequentCancellationData struct {
	InsertOrderCount   int64   `json:"insert_order_count"`
	CancelOrderCount   int64   `json:"cancel_order_count"`
	CancelOrderPercent float64 `json:"cancel_order_percent"`
	RejectedCount      int64   `json:"rejected_count"`
}

type TradePositionRatioData struct {
	TradeUnits         int64   `json:"trade_units"`
	NetPositionUnits   int64   `json:"net_position_units"`
	TradePositionRatio float64 `json:"trade_position_ratio"`
	RejectedCount      int64   `json:"rejected_count"`
}

type ConnEvent struct {
	State string
	Err   error
	At    time.Time
}

type QuoteEvent struct {
	Quote     Quote
	ChangedAt time.Time
}

type TickEvent struct {
	Kind        EventKind
	Symbol      string
	Tick        Tick
	Frame       []*Tick
	SerialReady bool
	FrameFull   bool
	ValidCount  int
	ChangedAt   time.Time
}

type KlineKey struct {
	MainSymbol string
	Duration   time.Duration
}

type AlignedKlineRow struct {
	MainID   int64
	Datetime time.Time
	Main     Kline
	Others   map[string]*Kline
	Binding  map[string]int64
}

type KlineEvent struct {
	Kind        EventKind
	Key         KlineKey
	Row         AlignedKlineRow
	Frame1D     []*Kline
	Frame2D     [][]*Kline
	SerialReady bool
	FrameFull   bool
	ValidCount  int
	ChangedAt   time.Time
}

type TradeSessionEvent struct {
	SessionID string
	AccountID string
	State     TradeSessionState
	Err       error
	ChangedAt time.Time
}

type OrderEvent struct {
	Kind      TradeEventKind
	AccountID string
	OrderID   string
	Order     Order
	ChangedAt time.Time
}

type TradeFillEvent struct {
	Kind      TradeEventKind
	AccountID string
	TradeID   string
	Trade     Trade
	ChangedAt time.Time
}

type PositionEvent struct {
	Kind      TradeEventKind
	AccountID string
	Symbol    string
	Position  Position
	ChangedAt time.Time
}

type AccountEvent struct {
	Kind      TradeEventKind
	AccountID string
	Account   Account
	ChangedAt time.Time
}

type TradingStatusEvent struct {
	Kind      TradeEventKind
	Symbol    string
	Status    TradingStatus
	ChangedAt time.Time
}

type NotifyEvent struct {
	AccountID string
	ConnID    string
	Type      string
	Level     NotifyLevel
	Code      int64
	Content   string
	URL       string
	ArrivedAt time.Time
}
