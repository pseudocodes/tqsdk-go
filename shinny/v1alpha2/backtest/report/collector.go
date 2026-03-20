package report

import (
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest/trade"
)

// BacktestReport is the final output of backtesting.
type BacktestReport struct {
	TradingDays    int
	InitialBalance float64
	FinalBalance   float64
	TotalReturn    float64
	AnnualReturn   float64
	MaxDrawdown    float64
	SharpeRatio    float64
	SortinoRatio   float64
	CalmarRatio    float64
	DailyEquity    []float64
	Snapshots      []trade.DailySnapshot
	GeneratedAt    time.Time

	// GAP-07: Extended metrics
	TradeStats TradeStats
	DayStats   DayStats
	Punchline  string
}

// Collector accumulates daily settlement snapshots and produces a report.
// It implements trade.SettlementObserver.
type Collector struct {
	initialBalance float64
	snapshots      []trade.DailySnapshot
}

// NewCollector creates a report collector with the given initial balance.
func NewCollector(initialBalance float64) *Collector {
	return &Collector{initialBalance: initialBalance}
}

// OnSettlement implements trade.SettlementObserver.
func (c *Collector) OnSettlement(snap trade.DailySnapshot) {
	c.snapshots = append(c.snapshots, snap)
}

// Finalize computes the backtest report from collected snapshots.
func (c *Collector) Finalize() BacktestReport {
	n := len(c.snapshots)
	if n == 0 {
		return BacktestReport{
			InitialBalance: c.initialBalance,
			FinalBalance:   c.initialBalance,
			GeneratedAt:    time.Now(),
		}
	}

	equity := make([]float64, n)
	for i, s := range c.snapshots {
		equity[i] = s.Balance
	}

	finalBalance := equity[n-1]
	totalRet := TotalReturn(c.initialBalance, finalBalance)
	annualRet := AnnualReturn(totalRet, n)
	maxDD := MaxDrawdown(equity)
	dailyRet := DailyReturns(append([]float64{c.initialBalance}, equity...))
	sharpe := Sharpe(dailyRet, 0.03)
	sortino := Sortino(dailyRet, 0.03)
	calmar := Calmar(annualRet, maxDD)

	return BacktestReport{
		TradingDays:    n,
		InitialBalance: c.initialBalance,
		FinalBalance:   finalBalance,
		TotalReturn:    totalRet,
		AnnualReturn:   annualRet,
		MaxDrawdown:    maxDD,
		SharpeRatio:    sharpe,
		SortinoRatio:   sortino,
		CalmarRatio:    calmar,
		DailyEquity:    equity,
		Snapshots:      c.snapshots,
		GeneratedAt:    time.Now(),
		TradeStats:     ComputeTradeStatsFromSnapshots(c.snapshots),
		DayStats:       ComputeDayStats(c.snapshots, c.initialBalance),
		Punchline:      GetPunchline(totalRet),
	}
}
