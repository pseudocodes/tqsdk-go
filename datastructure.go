package tqsdk

import (
	"math"
	"time"
)

// Quote 行情报价数据
type Quote struct {
	// 基本信息
	InstrumentID string  `json:"instrument_id"` // 合约代码
	Datetime     string  `json:"datetime"`      // 行情时间
	LastPrice    float64 `json:"last_price"`    // 最新价

	// 买卖盘口
	AskPrice1  float64 `json:"ask_price1"`  // 卖一价
	AskVolume1 int64   `json:"ask_volume1"` // 卖一量
	AskPrice2  float64 `json:"ask_price2"`  // 卖二价
	AskVolume2 int64   `json:"ask_volume2"` // 卖二量
	AskPrice3  float64 `json:"ask_price3"`  // 卖三价
	AskVolume3 int64   `json:"ask_volume3"` // 卖三量
	AskPrice4  float64 `json:"ask_price4"`  // 卖四价
	AskVolume4 int64   `json:"ask_volume4"` // 卖四量
	AskPrice5  float64 `json:"ask_price5"`  // 卖五价
	AskVolume5 int64   `json:"ask_volume5"` // 卖五量

	BidPrice1  float64 `json:"bid_price1"`  // 买一价
	BidVolume1 int64   `json:"bid_volume1"` // 买一量
	BidPrice2  float64 `json:"bid_price2"`  // 买二价
	BidVolume2 int64   `json:"bid_volume2"` // 买二量
	BidPrice3  float64 `json:"bid_price3"`  // 买三价
	BidVolume3 int64   `json:"bid_volume3"` // 买三量
	BidPrice4  float64 `json:"bid_price4"`  // 买四价
	BidVolume4 int64   `json:"bid_volume4"` // 买四量
	BidPrice5  float64 `json:"bid_price5"`  // 买五价
	BidVolume5 int64   `json:"bid_volume5"` // 买五量

	// 当日统计
	Highest      float64 `json:"highest"`       // 最高价
	Lowest       float64 `json:"lowest"`        // 最低价
	Open         float64 `json:"open"`          // 开盘价
	Close        float64 `json:"close"`         // 收盘价
	Average      float64 `json:"average"`       // 均价
	Volume       int64   `json:"volume"`        // 成交量
	Amount       float64 `json:"amount"`        // 成交额
	OpenInterest int64   `json:"open_interest"` // 持仓量

	// 涨跌停
	LowerLimit float64 `json:"lower_limit"` // 跌停价
	UpperLimit float64 `json:"upper_limit"` // 涨停价

	// 结算价
	Settlement    float64 `json:"settlement"`     // 结算价
	PreSettlement float64 `json:"pre_settlement"` // 昨结算价

	// 涨跌
	Change        float64 `json:"change"`         // 涨跌
	ChangePercent float64 `json:"change_percent"` // 涨跌幅

	// 期权相关
	StrikePrice float64 `json:"strike_price"` // 行权价

	// 昨日数据
	PreOpenInterest int64   `json:"pre_open_interest"` // 昨持仓量
	PreClose        float64 `json:"pre_close"`         // 昨收盘价
	PreVolume       int64   `json:"pre_volume"`        // 昨成交量

	// 保证金和手续费
	Margin     float64 `json:"margin"`     // 每手保证金
	Commission float64 `json:"commission"` // 每手手续费

	// 合约信息（从合约服务获取）
	Class             string  `json:"class"`                // 合约类型
	ExchangeID        string  `json:"exchange_id"`          // 交易所代码
	ProductID         string  `json:"product_id"`           // 品种代码
	ProductShortName  string  `json:"product_short_name"`   // 品种简称
	UnderlyingProduct string  `json:"underlying_product"`   // 标的产品
	UnderlyingSymbol  string  `json:"underlying_symbol"`    // 标的合约
	DeliveryYear      int     `json:"delivery_year"`        // 交割年份
	DeliveryMonth     int     `json:"delivery_month"`       // 交割月份
	ExpireDatetime    int64   `json:"expire_datetime"`      // 到期时间
	VolumeMultiple    int     `json:"volume_multiple"`      // 合约乘数
	PriceTick         float64 `json:"price_tick"`           // 最小变动价位
	PriceDecs         int     `json:"price_decs"`           // 价格小数位数
	MaxMarketOrderVol int     `json:"max_market_order_vol"` // 市价单最大下单量
	MinMarketOrderVol int     `json:"min_market_order_vol"` // 市价单最小下单量
	MaxLimitOrderVol  int     `json:"max_limit_order_vol"`  // 限价单最大下单量
	MinLimitOrderVol  int     `json:"min_limit_order_vol"`  // 限价单最小下单量
	Expired           bool    `json:"expired"`              // 是否已下市
	Py                string  `json:"py"`                   // 拼音

	// 内部字段
	epoch int64       // 数据版本
	root  interface{} // 指向根数据对象
}

// UpdateChange 更新涨跌和涨跌幅
func (q *Quote) UpdateChange() {
	if !math.IsNaN(q.LastPrice) && !math.IsNaN(q.PreSettlement) && q.PreSettlement != 0 {
		q.Change = q.LastPrice - q.PreSettlement
		q.ChangePercent = q.Change / q.PreSettlement * 100
	}
}

// Kline K线数据
type Kline struct {
	ID       int64   `json:"id"`       // K线ID
	Datetime int64   `json:"datetime"` // K线起点时间(纳秒)
	Open     float64 `json:"open"`     // 开盘价
	Close    float64 `json:"close"`    // 收盘价
	High     float64 `json:"high"`     // 最高价
	Low      float64 `json:"low"`      // 最低价
	OpenOI   int64   `json:"open_oi"`  // 起始持仓量
	CloseOI  int64   `json:"close_oi"` // 结束持仓量
	Volume   int64   `json:"volume"`   // 成交量

	// 内部字段
	epoch int64 // 数据版本
}

// Tick Tick数据
type Tick struct {
	ID        int64   `json:"id"`         // Tick ID
	Datetime  int64   `json:"datetime"`   // tick时间(纳秒)
	LastPrice float64 `json:"last_price"` // 最新价
	Average   float64 `json:"average"`    // 均价
	Highest   float64 `json:"highest"`    // 最高价
	Lowest    float64 `json:"lowest"`     // 最低价

	AskPrice1  float64 `json:"ask_price1"`  // 卖一价
	AskVolume1 int64   `json:"ask_volume1"` // 卖一量
	AskPrice2  float64 `json:"ask_price2"`  // 卖二价
	AskVolume2 int64   `json:"ask_volume2"` // 卖二量
	AskPrice3  float64 `json:"ask_price3"`  // 卖三价
	AskVolume3 int64   `json:"ask_volume3"` // 卖三量
	AskPrice4  float64 `json:"ask_price4"`  // 卖四价
	AskVolume4 int64   `json:"ask_volume4"` // 卖四量
	AskPrice5  float64 `json:"ask_price5"`  // 卖五价
	AskVolume5 int64   `json:"ask_volume5"` // 卖五量

	BidPrice1  float64 `json:"bid_price1"`  // 买一价
	BidVolume1 int64   `json:"bid_volume1"` // 买一量
	BidPrice2  float64 `json:"bid_price2"`  // 买二价
	BidVolume2 int64   `json:"bid_volume2"` // 买二量
	BidPrice3  float64 `json:"bid_price3"`  // 买三价
	BidVolume3 int64   `json:"bid_volume3"` // 买三量
	BidPrice4  float64 `json:"bid_price4"`  // 买四价
	BidVolume4 int64   `json:"bid_volume4"` // 买四量
	BidPrice5  float64 `json:"bid_price5"`  // 买五价
	BidVolume5 int64   `json:"bid_volume5"` // 买五量

	Volume       int64   `json:"volume"`        // 成交量
	Amount       float64 `json:"amount"`        // 成交额
	OpenInterest int64   `json:"open_interest"` // 持仓量

	// 内部字段
	epoch int64 // 数据版本
}

// Chart 图表状态
type Chart struct {
	LeftID   int64                  `json:"left_id"`   // 左边界K线ID
	RightID  int64                  `json:"right_id"`  // 右边界K线ID
	MoreData bool                   `json:"more_data"` // 是否有更多数据
	Ready    bool                   `json:"ready"`     // 数据是否已准备好（分片传输完成）
	State    map[string]interface{} `json:"state"`     // 图表状态

	// 内部字段
	epoch int64 // 数据版本
}

// ChartInfo Chart 信息
type ChartInfo struct {
	ChartID   string `json:"chart_id"`   // 图表ID
	LeftID    int64  `json:"left_id"`    // 左边界K线ID
	RightID   int64  `json:"right_id"`   // 右边界K线ID
	MoreData  bool   `json:"more_data"`  // 是否有更多数据
	Ready     bool   `json:"ready"`      // 数据是否已准备好（分片传输完成）
	ViewWidth int    `json:"view_width"` // 视图宽度
}

// NewChart 创建新的图表对象
func NewChart(state map[string]interface{}) *Chart {
	if state == nil {
		state = make(map[string]interface{})
	}
	return &Chart{
		LeftID:   -1,
		RightID:  -1,
		MoreData: true,
		State:    state,
	}
}

// KlineSeriesData K线序列数据（带Chart信息）
type KlineSeriesData struct {
	Symbol            string        `json:"symbol"`               // 合约代码
	Duration          time.Duration `json:"duration"`             // K线周期
	ChartID           string        `json:"chart_id"`             // 关联的Chart ID
	Chart             *ChartInfo    `json:"chart"`                // Chart 信息
	LastID            int64         `json:"last_id"`              // 最新K线ID
	TradingDayStartID int64         `json:"trading_day_start_id"` // 交易日起始ID
	TradingDayEndID   int64         `json:"trading_day_end_id"`   // 交易日结束ID
	Data              []*Kline      `json:"data"`                 // K线数组（仅保留 ViewWidth 长度）
	HasNewBar         bool          `json:"has_new_bar"`          // 是否有新K线

	// 内部字段
	epoch int64 // 数据版本
}

// TickSeriesData Tick序列数据
type TickSeriesData struct {
	Symbol    string     `json:"symbol"`      // 合约代码
	ChartID   string     `json:"chart_id"`    // 关联的Chart ID
	Chart     *ChartInfo `json:"chart"`       // Chart 信息
	LastID    int64      `json:"last_id"`     // 最新Tick ID
	Data      []*Tick    `json:"data"`        // Tick数组
	HasNewBar bool       `json:"has_new_bar"` // 是否有新Tick

	// 内部字段
	epoch int64 // 数据版本
}

// Account 账户资金信息
type Account struct {
	CurrMargin       float64 `json:"curr_margin"`       // 当前保证金
	FrozenMargin     float64 `json:"frozen_margin"`     // 冻结保证金
	FrozenCommission float64 `json:"frozen_commission"` // 冻结手续费
	FrozenPremium    float64 `json:"frozen_premium"`    // 冻结权利金
	Available        float64 `json:"available"`         // 可用资金
	Balance          float64 `json:"balance"`           // 账户权益
	PreBalance       float64 `json:"pre_balance"`       // 昨日权益
	Deposit          float64 `json:"deposit"`           // 入金
	Withdraw         float64 `json:"withdraw"`          // 出金
	CloseProfit      float64 `json:"close_profit"`      // 平仓盈亏
	PositionProfit   float64 `json:"position_profit"`   // 持仓盈亏
	Commission       float64 `json:"commission"`        // 手续费
	Premium          float64 `json:"premium"`           // 权利金
	StaticBalance    float64 `json:"static_balance"`    // 静态权益
	RiskRatio        float64 `json:"risk_ratio"`        // 风险度
	MarketValue      float64 `json:"market_value"`      // 市值
	CashAssets       float64 `json:"cash_assets"`       // 现金资产

	// 内部字段
	epoch int64 // 数据版本
}

// Position 持仓信息
type Position struct {
	ExchangeID          string  `json:"exchange_id"`           // 交易所代码
	InstrumentID        string  `json:"instrument_id"`         // 合约代码
	VolumeShortToday    int64   `json:"volume_short_today"`    // 今空头持仓量
	VolumeShortHis      int64   `json:"volume_short_his"`      // 昨空头持仓量
	VolumeLongToday     int64   `json:"volume_long_today"`     // 今多头持仓量
	VolumeLongHis       int64   `json:"volume_long_his"`       // 昨多头持仓量
	VolumeLongFrozen    int64   `json:"volume_long_frozen"`    // 冻结多头持仓量
	VolumeShortFrozen   int64   `json:"volume_short_frozen"`   // 冻结空头持仓量
	OpenPriceLong       float64 `json:"open_price_long"`       // 多头开仓均价
	OpenPriceShort      float64 `json:"open_price_short"`      // 空头开仓均价
	OpenCostLong        float64 `json:"open_cost_long"`        // 多头开仓成本
	OpenCostShort       float64 `json:"open_cost_short"`       // 空头开仓成本
	PositionPriceLong   float64 `json:"position_price_long"`   // 多头持仓均价
	PositionPriceShort  float64 `json:"position_price_short"`  // 空头持仓均价
	PositionCostLong    float64 `json:"position_cost_long"`    // 多头持仓成本
	PositionCostShort   float64 `json:"position_cost_short"`   // 空头持仓成本
	FloatProfitLong     float64 `json:"float_profit_long"`     // 多头浮动盈亏
	FloatProfitShort    float64 `json:"float_profit_short"`    // 空头浮动盈亏
	FloatProfit         float64 `json:"float_profit"`          // 总浮动盈亏
	PositionProfitLong  float64 `json:"position_profit_long"`  // 多头持仓盈亏
	PositionProfitShort float64 `json:"position_profit_short"` // 空头持仓盈亏
	PositionProfit      float64 `json:"position_profit"`       // 总持仓盈亏
	MarginLong          float64 `json:"margin_long"`           // 多头占用保证金
	MarginShort         float64 `json:"margin_short"`          // 空头占用保证金
	Margin              float64 `json:"margin"`                // 占用保证金

	// 内部字段
	epoch int64 // 数据版本
}

// Order 委托单信息
type Order struct {
	OrderID         string  `json:"order_id"`         // 委托单ID
	ExchangeID      string  `json:"exchange_id"`      // 交易所代码
	InstrumentID    string  `json:"instrument_id"`    // 合约代码
	Direction       string  `json:"direction"`        // 下单方向 BUY/SELL
	Offset          string  `json:"offset"`           // 开平标志 OPEN/CLOSE/CLOSETODAY
	VolumeOrign     int64   `json:"volume_orign"`     // 总报单手数
	VolumeLeft      int64   `json:"volume_left"`      // 未成交手数
	PriceType       string  `json:"price_type"`       // 价格类型 LIMIT/ANY
	LimitPrice      float64 `json:"limit_price"`      // 委托价格
	VolumeCondition string  `json:"volume_condition"` // 数量条件 ANY/MIN/ALL
	TimeCondition   string  `json:"time_condition"`   // 时间条件 IOC/GFS/GFD/GTC/GFA
	InsertDateTime  int64   `json:"insert_date_time"` // 下单时间(纳秒)
	Status          string  `json:"status"`           // 委托单状态 ALIVE/FINISHED

	// 内部字段
	epoch int64 // 数据版本
}

// Trade 成交记录
type Trade struct {
	TradeID       string  `json:"trade_id"`        // 成交ID
	OrderID       string  `json:"order_id"`        // 委托单ID
	ExchangeID    string  `json:"exchange_id"`     // 交易所代码
	InstrumentID  string  `json:"instrument_id"`   // 合约代码
	Direction     string  `json:"direction"`       // 成交方向 BUY/SELL
	Offset        string  `json:"offset"`          // 开平标志 OPEN/CLOSE/CLOSETODAY
	Price         float64 `json:"price"`           // 成交价格
	Volume        int64   `json:"volume"`          // 成交手数
	TradeDateTime int64   `json:"trade_date_time"` // 成交时间(纳秒)
	Commission    float64 `json:"commission"`      // 手续费

	// 内部字段
	epoch int64 // 数据版本
}

// Session 会话信息
type Session struct {
	TradingDay string `json:"trading_day"` // 交易日

	// 内部字段
	epoch int64 // 数据版本
}

// HisSettlement 历史结算单
type HisSettlement struct {
	TradingDay         string              `json:"trading_day"`         // 交易日
	Account            map[string]string   `json:"account"`             // 账户信息
	PositionClosed     []map[string]string `json:"position_closed"`     // 平仓明细
	TransactionRecords []map[string]string `json:"transaction_records"` // 成交记录

	// 内部字段
	epoch int64 // 数据版本
}

// NotifyEvent 通知事件
type NotifyEvent struct {
	Code    string `json:"code"`    // 通知代码
	Level   string `json:"level"`   // 通知级别
	Type    string `json:"type"`    // 通知类型
	Content string `json:"content"` // 通知内容
	BID     string `json:"bid"`     // 期货公司
	UserID  string `json:"user_id"` // 用户ID
}

// Notification 通知
type Notification struct {
	Code    string `json:"code"`    // 通知代码
	Level   string `json:"level"`   // 通知级别
	Type    string `json:"type"`    // 通知类型
	Content string `json:"content"` // 通知内容
	BID     string `json:"bid"`     // 期货公司
	UserID  string `json:"user_id"` // 用户ID
}

// PositionUpdate 持仓更新
type PositionUpdate struct {
	Symbol   string    `json:"symbol"`   // 合约代码
	Position *Position `json:"position"` // 持仓信息
}

// MultiKlineSeriesData 多合约K线序列数据（已对齐）
type MultiKlineSeriesData struct {
	ChartID    string                    `json:"chart_id"`    // 图表ID
	Duration   time.Duration             `json:"duration"`    // K线周期
	MainSymbol string                    `json:"main_symbol"` // 主合约（第一个合约）
	Symbols    []string                  `json:"symbols"`     // 所有合约列表
	LeftID     int64                     `json:"left_id"`     // 左边界ID
	RightID    int64                     `json:"right_id"`    // 右边界ID
	ViewWidth  int                       `json:"view_width"`  // 视图宽度
	Data       []AlignedKlineSet         `json:"data"`        // 对齐的K线数据集
	HasNewBar  bool                      `json:"has_new_bar"` // 是否有新K线产生
	Metadata   map[string]*KlineMetadata `json:"metadata"`    // 每个合约的元数据
}

// AlignedKlineSet 对齐的K线集合（一个时间点的多个合约）
type AlignedKlineSet struct {
	MainID    int64             `json:"main_id"`   // 主合约的K线ID
	Timestamp time.Time         `json:"timestamp"` // 时间戳
	Klines    map[string]*Kline `json:"klines"`    // symbol -> Kline
}

// KlineMetadata K线元数据
type KlineMetadata struct {
	Symbol            string `json:"symbol"`
	LastID            int64  `json:"last_id"`
	TradingDayStartID int64  `json:"trading_day_start_id"`
	TradingDayEndID   int64  `json:"trading_day_end_id"`
}

// InsertOrderRequest 下单请求
type InsertOrderRequest struct {
	Symbol     string  // 合约代码（格式：EXCHANGE.INSTRUMENT，如 SHFE.au2512）
	Direction  string  // 下单方向 BUY/SELL
	Offset     string  // 开平标志 OPEN/CLOSE/CLOSETODAY
	PriceType  string  // 价格类型 LIMIT/ANY
	LimitPrice float64 // 委托价格
	Volume     int64   // 下单手数
}

// 常量定义
const (
	// 方向
	DirectionBuy  = "BUY"
	DirectionSell = "SELL"

	// 开平
	OffsetOpen       = "OPEN"
	OffsetClose      = "CLOSE"
	OffsetCloseToday = "CLOSETODAY"

	// 价格类型
	PriceTypeLimit = "LIMIT"
	PriceTypeAny   = "ANY"

	// 订单状态
	OrderStatusAlive    = "ALIVE"
	OrderStatusFinished = "FINISHED"
)

// SeriesOptions 序列订阅选项
type SeriesOptions struct {
	Symbols   []string      // 合约列表
	Duration  time.Duration // K线周期（0表示Tick）
	ViewWidth int           // 视图宽度（最大 10000）
	ChartID   string        // 图表ID（可选）

	// 历史数据订阅参数（可选）
	LeftKlineID   *int64     // 左边界 K线 ID（优先级最高）
	FocusDatetime *time.Time // 焦点时间（需配合 FocusPosition 使用）
	FocusPosition *int       // 焦点位置（需配合 FocusDatetime 使用，1=右侧，-1=左侧）
}

// SeriesData 序列数据（统一接口）
type SeriesData struct {
	IsMulti  bool                  // 是否为多合约
	IsTick   bool                  // 是否为Tick数据
	Symbols  []string              // 合约列表
	Single   *KlineSeriesData      // 单合约K线数据
	Multi    *MultiKlineSeriesData // 多合约K线数据
	TickData *TickSeriesData       // Tick数据
}

// GetSymbolKlines 获取指定合约的K线数据
func (sd *SeriesData) GetSymbolKlines(symbol string) *KlineSeriesData {
	if sd.IsMulti && sd.Multi != nil {
		// 从多合约数据中提取单个合约
		result := &KlineSeriesData{
			Symbol:    symbol,
			Duration:  sd.Multi.Duration,
			ChartID:   sd.Multi.ChartID,
			HasNewBar: sd.Multi.HasNewBar,
			Data:      make([]*Kline, 0),
		}

		if meta, ok := sd.Multi.Metadata[symbol]; ok {
			result.LastID = meta.LastID
			result.TradingDayStartID = meta.TradingDayStartID
			result.TradingDayEndID = meta.TradingDayEndID
		}

		// 提取该合约的K线
		for _, set := range sd.Multi.Data {
			if kline, ok := set.Klines[symbol]; ok {
				result.Data = append(result.Data, kline)
			}
		}

		return result
	} else if !sd.IsMulti && sd.Single != nil {
		return sd.Single
	}
	return nil
}

// UpdateInfo 数据更新信息
type UpdateInfo struct {
	HasNewBar         bool             // 是否有新 K线/Tick
	NewBarIDs         map[string]int64 // 新 K线的 ID（symbol -> id）
	HasBarUpdate      bool             // 是否有 K线更新（最后一根）
	ChartRangeChanged bool             // Chart 范围是否变化
	OldLeftID         int64            // 旧左边界
	OldRightID        int64            // 旧右边界
	NewLeftID         int64            // 新左边界
	NewRightID        int64            // 新右边界
	HasChartSync      bool             // Chart 是否同步完成
	ChartReady        bool             // Chart数据传输是否完成（分片传输场景）
}
