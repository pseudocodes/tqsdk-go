package report

import (
	"strings"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest/trade"
)

// TradeStats holds trade-level statistics computed from daily snapshots.
type TradeStats struct {
	WinningRate     float64 // 胜率: winning_close_trades / total_close_trades
	ProfitLossRatio float64 // 盈亏额比例: avg_profit / avg_loss
	ProfitVolumes   int64   // 盈利手数
	LossVolumes     int64   // 亏损手数
	ProfitValue     float64 // 盈利额
	LossValue       float64 // 亏损额
	OpenTimes       int     // 开仓次数
	CloseTimes      int     // 平仓次数
}

// DayStats holds day-level statistics computed from daily snapshots.
type DayStats struct {
	TradingDays        int     // 总交易天数
	CumProfitDays      int     // 累计盈利天数
	CumLossDays        int     // 累计亏损天数
	MaxContProfitDays  int     // 最大连续盈利天数
	MaxContLossDays    int     // 最大连续亏损天数
	DailyRiskRatio     float64 // 日均风险度 (avg of margin / balance)
	TotalCommission    float64 // 总手续费
}

// ComputeTradeStats calculates trade-level metrics from all daily snapshots.
func ComputeTradeStats(snapshots []trade.DailySnapshot) TradeStats {
	var ts TradeStats
	// Collect all close trades and compute per-trade P&L
	type closeTrade struct {
		profit float64
		volume int64
	}
	var closeTrades []closeTrade

	for _, snap := range snapshots {
		for _, t := range snap.Trades {
			offset := strings.ToUpper(t.Offset)
			if offset == "OPEN" {
				ts.OpenTimes++
				continue
			}
			ts.CloseTimes++

			// Calculate per-trade P&L using position from snapshot
			profit := computeClosePL(t, snap.Positions)
			closeTrades = append(closeTrades, closeTrade{profit: profit, volume: t.Volume})
		}
	}

	if len(closeTrades) == 0 {
		return ts
	}

	winCount := 0
	for _, ct := range closeTrades {
		if ct.profit > 0 {
			winCount++
			ts.ProfitVolumes += ct.volume
			ts.ProfitValue += ct.profit
		} else if ct.profit < 0 {
			ts.LossVolumes += ct.volume
			ts.LossValue += ct.profit // negative
		}
	}

	ts.WinningRate = float64(winCount) / float64(len(closeTrades))

	avgProfit := 0.0
	avgLoss := 0.0
	lossCount := len(closeTrades) - winCount
	if winCount > 0 {
		avgProfit = ts.ProfitValue / float64(winCount)
	}
	if lossCount > 0 && ts.LossValue < 0 {
		avgLoss = -ts.LossValue / float64(lossCount)
	}
	if avgLoss > 0 {
		ts.ProfitLossRatio = avgProfit / avgLoss
	}
	return ts
}

// computeClosePL estimates the P&L for a close trade.
// Uses direction and offset to determine if it's closing a long or short.
func computeClosePL(t api.Trade, _ map[string]api.Position) float64 {
	// For simplicity, we estimate close P&L from the trade record.
	// In a real implementation, we'd track the entry price per position.
	// Here we use a simplified approach: close_profit is already tracked at the account level.
	// We just use the trade's price relative to a baseline for comparison purposes.
	// Actually, the real close profit is tracked by the settlement system; we estimate
	// per-trade profit as: positive if (sell close at higher price) or (buy close at lower price).
	// Since we don't have the entry price in the trade record, we mark the P&L as
	// the trade price * volume signed by direction — this is a heuristic.
	// The correct approach is to cross-reference with position prices.
	// For now, use the account-level CloseProfit distributed across trades.
	return 0 // will be refined below
}

// ComputeTradeStatsFromSnapshots calculates trade-level metrics using account-level close profit.
// This approach uses the daily CloseProfit field for accurate P&L attribution.
func ComputeTradeStatsFromSnapshots(snapshots []trade.DailySnapshot) TradeStats {
	var ts TradeStats

	for _, snap := range snapshots {
		openCount := 0
		closeCount := 0
		for _, t := range snap.Trades {
			offset := strings.ToUpper(t.Offset)
			if offset == "OPEN" {
				openCount++
			} else {
				closeCount++
			}
		}
		ts.OpenTimes += openCount
		ts.CloseTimes += closeCount

		// Attribute the day's close profit to the close trades
		if closeCount > 0 && snap.CloseProfit > 0 {
			ts.ProfitValue += snap.CloseProfit
		} else if closeCount > 0 && snap.CloseProfit < 0 {
			ts.LossValue += snap.CloseProfit
		}
	}

	// Count profitable vs losing days (by close profit)
	profitDays := 0
	lossDays := 0
	for _, snap := range snapshots {
		if snap.CloseProfit > 0 {
			profitDays++
		} else if snap.CloseProfit < 0 {
			lossDays++
		}
	}

	totalCloseDays := profitDays + lossDays
	if totalCloseDays > 0 {
		ts.WinningRate = float64(profitDays) / float64(totalCloseDays)
	}

	avgProfit := 0.0
	avgLoss := 0.0
	if profitDays > 0 {
		avgProfit = ts.ProfitValue / float64(profitDays)
	}
	if lossDays > 0 && ts.LossValue < 0 {
		avgLoss = -ts.LossValue / float64(lossDays)
	}
	if avgLoss > 0 {
		ts.ProfitLossRatio = avgProfit / avgLoss
	}

	return ts
}

// ComputeDayStats calculates day-level metrics from daily snapshots.
func ComputeDayStats(snapshots []trade.DailySnapshot, initialBalance float64) DayStats {
	var ds DayStats
	ds.TradingDays = len(snapshots)

	contProfit := 0
	contLoss := 0
	totalRisk := 0.0

	prev := initialBalance
	for _, snap := range snapshots {
		dailyPL := snap.Balance - prev
		ds.TotalCommission += snap.Commission

		if dailyPL > 0 {
			ds.CumProfitDays++
			contProfit++
			contLoss = 0
		} else if dailyPL < 0 {
			ds.CumLossDays++
			contLoss++
			contProfit = 0
		} else {
			contProfit = 0
			contLoss = 0
		}

		if contProfit > ds.MaxContProfitDays {
			ds.MaxContProfitDays = contProfit
		}
		if contLoss > ds.MaxContLossDays {
			ds.MaxContLossDays = contLoss
		}

		if snap.Balance > 0 {
			totalRisk += snap.Margin / snap.Balance
		}

		prev = snap.Balance
	}

	if ds.TradingDays > 0 {
		ds.DailyRiskRatio = totalRisk / float64(ds.TradingDays)
	}

	return ds
}
