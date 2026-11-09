package model

// Quote 是实时盘口行情，字段与 Python tqsdk.objs.Quote 的行情部分一一对应。
// 合约资料类字段（最小变动价位、合约乘数等）见 Instrument。
// Quote 的有效位位于外层 QuoteSnapshot/QuoteEvent。
type Quote struct {
	Symbol string // 合约代码，来自订阅键

	Datetime string `tq:"datetime"` // 行情时间，如 "2017-07-26 23:04:21.000001"

	AskPrice1  float64 `tq:"ask_price1"`
	AskVolume1 int64   `tq:"ask_volume1"`
	BidPrice1  float64 `tq:"bid_price1"`
	BidVolume1 int64   `tq:"bid_volume1"`
	AskPrice2  float64 `tq:"ask_price2"`
	AskVolume2 int64   `tq:"ask_volume2"`
	BidPrice2  float64 `tq:"bid_price2"`
	BidVolume2 int64   `tq:"bid_volume2"`
	AskPrice3  float64 `tq:"ask_price3"`
	AskVolume3 int64   `tq:"ask_volume3"`
	BidPrice3  float64 `tq:"bid_price3"`
	BidVolume3 int64   `tq:"bid_volume3"`
	AskPrice4  float64 `tq:"ask_price4"`
	AskVolume4 int64   `tq:"ask_volume4"`
	BidPrice4  float64 `tq:"bid_price4"`
	BidVolume4 int64   `tq:"bid_volume4"`
	AskPrice5  float64 `tq:"ask_price5"`
	AskVolume5 int64   `tq:"ask_volume5"`
	BidPrice5  float64 `tq:"bid_price5"`
	BidVolume5 int64   `tq:"bid_volume5"`

	LastPrice       float64 `tq:"last_price"`
	Highest         float64 `tq:"highest"`
	Lowest          float64 `tq:"lowest"`
	Open            float64 `tq:"open"`
	Close           float64 `tq:"close"`
	Average         float64 `tq:"average"`
	Volume          int64   `tq:"volume"`
	Amount          float64 `tq:"amount"`
	OpenInterest    int64   `tq:"open_interest"`
	Settlement      float64 `tq:"settlement"`
	UpperLimit      float64 `tq:"upper_limit"`
	LowerLimit      float64 `tq:"lower_limit"`
	PreOpenInterest int64   `tq:"pre_open_interest"`
	PreSettlement   float64 `tq:"pre_settlement"`
	PreClose        float64 `tq:"pre_close"`
	IOPV            float64 `tq:"iopv"` // ETF 实时单位基金净值
}

// TradingTime 是合约交易时段，每段为 [开始, 结束]，如 ["21:00:00", "25:00:00"]。
type TradingTime struct {
	Day   [][]string
	Night [][]string
}

// Instrument 是合约资料，来自合约服务查询（ins_query）。
// 字段与 Python Quote 的合约资料部分对应；缺省字段通过 Valid 表示。
type Instrument struct {
	Symbol string // 合约代码，如 "SHFE.rb2701"

	InstrumentID             string  `tq:"instrument_id"`
	ExchangeID               string  `tq:"exchange_id"`
	Name                     string  `tq:"instrument_name"`
	InsClass                 string  `tq:"ins_class"` // FUTURE / OPTION / COMBINE / INDEX / CONT / STOCK / FUND ...
	PriceTick                float64 `tq:"price_tick"`
	PriceDecs                int64   `tq:"price_decs"`
	VolumeMultiple           int64   `tq:"volume_multiple"`
	MaxLimitOrderVolume      int64   `tq:"max_limit_order_volume"`
	MaxMarketOrderVolume     int64   `tq:"max_market_order_volume"`
	MinLimitOrderVolume      int64   `tq:"min_limit_order_volume"`
	MinMarketOrderVolume     int64   `tq:"min_market_order_volume"`
	OpenLimit                int64   `tq:"open_limit"`
	OpenMaxMarketOrderVolume int64   `tq:"open_max_market_order_volume"`
	OpenMaxLimitOrderVolume  int64   `tq:"open_max_limit_order_volume"`
	OpenMinMarketOrderVolume int64   `tq:"open_min_market_order_volume"`
	OpenMinLimitOrderVolume  int64   `tq:"open_min_limit_order_volume"`
	UnderlyingSymbol         string  `tq:"underlying_symbol"`
	StrikePrice              float64 `tq:"strike_price"`
	Expired                  bool    `tq:"expired"`
	ExpireDatetime           float64 `tq:"expire_datetime"` // 到期时间，Unix 秒（与 Python 一致）
	DeliveryYear             int64   `tq:"delivery_year"`
	DeliveryMonth            int64   `tq:"delivery_month"`
	LastExerciseDatetime     float64 `tq:"last_exercise_datetime"` // 最后行权日，Unix 秒
	ExerciseYear             int64   `tq:"exercise_year"`
	ExerciseMonth            int64   `tq:"exercise_month"`
	OptionClass              string  `tq:"option_class"` // CALL / PUT
	ExerciseType             string  `tq:"exercise_type"`
	ProductID                string  `tq:"product_id"`
	PublicFloatShareQuantity int64   `tq:"public_float_share_quantity"`
	PositionLimit            int64   `tq:"position_limit"`
	Margin                   float64 `tq:"margin"`     // 每手保证金（合约服务快照，模拟交易使用）
	Commission               float64 `tq:"commission"` // 每手手续费（合约服务快照，模拟交易使用）
	// 以下为资料查询时刻的快照值；实时值以 Quote 为准（与 Python 的来源划分一致）。
	PreSettlement   float64 `tq:"pre_settlement"` // 合约服务的 settlement_price
	PreOpenInterest int64   `tq:"pre_open_interest"`
	PreClose        float64 `tq:"pre_close"`
	UpperLimit      float64 `tq:"upper_limit"`
	LowerLimit      float64 `tq:"lower_limit"`

	Leg1Symbol string `tq:"leg1_symbol"` // 组合合约第一腿
	Leg2Symbol string `tq:"leg2_symbol"` // 组合合约第二腿

	TradingTime        TradingTime // 交易时段；为空表示合约服务未返回
	StockDividendRatio []string    // 送股信息
	CashDividendRatio  []string    // 分红信息

	Valid InstrumentFields
}
