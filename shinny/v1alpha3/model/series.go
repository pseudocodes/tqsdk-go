package model

// Kline 是一根 K 线，字段与 Python tqsdk.objs.Kline 对应。
type Kline struct {
	ID       int64   // 服务端序列号
	Datetime int64   `tq:"datetime"` // K 线起点时间，Unix 纳秒（北京时间）
	Open     float64 `tq:"open"`
	High     float64 `tq:"high"`
	Low      float64 `tq:"low"`
	Close    float64 `tq:"close"`
	Volume   int64   `tq:"volume"`
	OpenOI   int64   `tq:"open_oi"`
	CloseOI  int64   `tq:"close_oi"`
}

// Tick 是一笔 Tick，字段与 Python tqsdk.objs.Tick 对应，五档盘口逐字段保存。
type Tick struct {
	ID           int64   // 服务端序列号
	Datetime     int64   `tq:"datetime"` // 交易所时间，Unix 纳秒（北京时间）
	LastPrice    float64 `tq:"last_price"`
	Average      float64 `tq:"average"`
	Highest      float64 `tq:"highest"`
	Lowest       float64 `tq:"lowest"`
	AskPrice1    float64 `tq:"ask_price1"`
	AskVolume1   int64   `tq:"ask_volume1"`
	BidPrice1    float64 `tq:"bid_price1"`
	BidVolume1   int64   `tq:"bid_volume1"`
	AskPrice2    float64 `tq:"ask_price2"`
	AskVolume2   int64   `tq:"ask_volume2"`
	BidPrice2    float64 `tq:"bid_price2"`
	BidVolume2   int64   `tq:"bid_volume2"`
	AskPrice3    float64 `tq:"ask_price3"`
	AskVolume3   int64   `tq:"ask_volume3"`
	BidPrice3    float64 `tq:"bid_price3"`
	BidVolume3   int64   `tq:"bid_volume3"`
	AskPrice4    float64 `tq:"ask_price4"`
	AskVolume4   int64   `tq:"ask_volume4"`
	BidPrice4    float64 `tq:"bid_price4"`
	BidVolume4   int64   `tq:"bid_volume4"`
	AskPrice5    float64 `tq:"ask_price5"`
	AskVolume5   int64   `tq:"ask_volume5"`
	BidPrice5    float64 `tq:"bid_price5"`
	BidVolume5   int64   `tq:"bid_volume5"`
	Volume       int64   `tq:"volume"`
	Amount       float64 `tq:"amount"`
	OpenInterest int64   `tq:"open_interest"`
}
