package model

import "math"

func nan() float64 { return math.NaN() }

// RemoteRiskRule 是网关回报的远端风控规则（trade.<user>.risk_management_rule.<exchange>）。
// 百分比字段保留线上的百分比单位。
type RemoteRiskRule struct {
	UserID               string                  `tq:"user_id"`
	ExchangeID           string                  `tq:"exchange_id"`
	Enable               bool                    `tq:"enable"`
	SelfTrade            RemoteSelfTradeRule     `tq:"self_trade"`
	FrequentCancellation RemoteCancellationRule  `tq:"frequent_cancellation"`
	TradePositionRatio   RemoteTradePositionRule `tq:"trade_position_ratio"`
	Valid                RemoteRiskRuleFields
}

// RemoteSelfTradeRule 是自成交规则。
type RemoteSelfTradeRule struct {
	CountLimit int64 `tq:"count_limit"`
}

// RemoteCancellationRule 是频繁报撤单规则。
type RemoteCancellationRule struct {
	InsertOrderCountLimit   int64   `tq:"insert_order_count_limit"`
	CancelOrderCountLimit   int64   `tq:"cancel_order_count_limit"`
	CancelOrderPercentLimit float64 `tq:"cancel_order_percent_limit"`
}

// RemoteTradePositionRule 是成交持仓比规则。
type RemoteTradePositionRule struct {
	TradeUnitsLimit         int64   `tq:"trade_units_limit"`
	TradePositionRatioLimit float64 `tq:"trade_position_ratio_limit"`
}

// RemoteRiskData 是网关回报的远端风控统计（trade.<user>.risk_management_data.<symbol>）。
type RemoteRiskData struct {
	UserID               string                  `tq:"user_id"`
	ExchangeID           string                  `tq:"exchange_id"`
	InstrumentID         string                  `tq:"instrument_id"`
	SelfTrade            RemoteSelfTradeData     `tq:"self_trade"`
	FrequentCancellation RemoteCancellationData  `tq:"frequent_cancellation"`
	TradePositionRatio   RemoteTradePositionData `tq:"trade_position_ratio"`
	Valid                RemoteRiskDataFields
}

// RemoteSelfTradeData 是自成交统计。
type RemoteSelfTradeData struct {
	HighestBuyPrice float64 `tq:"highest_buy_price"`
	LowestSellPrice float64 `tq:"lowest_sell_price"`
	SelfTradeCount  int64   `tq:"self_trade_count"`
	RejectedCount   int64   `tq:"rejected_count"`
}

// RemoteCancellationData 是报撤单统计。
type RemoteCancellationData struct {
	InsertOrderCount   int64   `tq:"insert_order_count"`
	CancelOrderCount   int64   `tq:"cancel_order_count"`
	CancelOrderPercent float64 `tq:"cancel_order_percent"`
	RejectedCount      int64   `tq:"rejected_count"`
}

// RemoteTradePositionData 是成交持仓比统计。
type RemoteTradePositionData struct {
	TradeUnits         int64   `tq:"trade_units"`
	NetPositionUnits   int64   `tq:"net_position_units"`
	TradePositionRatio float64 `tq:"trade_position_ratio"`
	RejectedCount      int64   `tq:"rejected_count"`
}
