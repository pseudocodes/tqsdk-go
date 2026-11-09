package sim

import (
	"math"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// DayRecord 是一个交易日日结前的账户截面（对应 Python TqSim.trade_log 的一项）。
type DayRecord struct {
	Date      string // 交易日 YYYY-MM-DD
	Account   model.Account
	Positions map[string]model.Position
	Trades    []model.Trade
}

// Report 是账户的回测报告，指标口径同 Python TqReport：年交易日 250，年化无风险利率 2.5%。
type Report struct {
	AccountID    string
	Days         []DayRecord
	InitBalance  float64
	Balance      float64
	ROR          float64 // 收益率
	AnnualYield  float64 // 年化收益率
	MaxDrawdown  float64 // 最大回撤
	SharpeRatio  float64 // 年化夏普率
	SortinoRatio float64 // 年化索提诺比率
	Commission   float64 // 总手续费
}

const (
	tradingDaysOfYear = 250
	riskFreeRate      = 0.025
)

// Report 计算账户报告；没有日结记录时指标为 NaN。
func (l *Ledger) Report() Report {
	r := Report{AccountID: l.cfg.ID, Days: append([]DayRecord(nil), l.days...)}
	nan := math.NaN()
	r.ROR, r.AnnualYield, r.MaxDrawdown, r.SharpeRatio, r.SortinoRatio = nan, nan, nan, nan, nan
	n := len(l.days)
	if n == 0 {
		r.InitBalance, r.Balance = l.cfg.InitBalance, l.acc.Balance
		return r
	}
	r.InitBalance, r.Balance = l.days[0].Account.PreBalance, l.days[n-1].Account.Balance
	ror := r.Balance / r.InitBalance
	r.ROR = ror - 1
	r.AnnualYield = math.Pow(ror, float64(tradingDaysOfYear)/float64(n)) - 1

	yields := make([]float64, n)
	prev, peak := r.InitBalance, math.Inf(-1)
	r.MaxDrawdown = 0
	for i, d := range l.days {
		b := d.Account.Balance
		yields[i] = b/prev - 1
		prev = b
		peak = max(peak, b)
		r.MaxDrawdown = max(r.MaxDrawdown, (peak-b)/peak)
		r.Commission += d.Account.Commission
	}
	r.SharpeRatio, r.SortinoRatio = sharpe(yields), sortino(yields)
	return r
}

func dailyRiskFree() float64 { return math.Pow(1+riskFreeRate, 1.0/tradingDaysOfYear) - 1 }

func mean(xs []float64) float64 {
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func pstdev(xs []float64, mu float64) float64 {
	var s float64
	for _, x := range xs {
		s += (x - mu) * (x - mu)
	}
	return math.Sqrt(s / float64(len(xs)))
}

// sharpe 对应 Python tafunc.get_sharp。
func sharpe(yields []float64) float64 {
	rf, mu := dailyRiskFree(), mean(yields)
	sd := pstdev(yields, mu)
	if sd == 0 {
		return math.Inf(1)
	}
	return math.Sqrt(tradingDaysOfYear) * (mu - rf) / sd
}

// sortino 对应 Python tafunc.get_sortino。
func sortino(yields []float64) float64 {
	rf, mu := dailyRiskFree(), mean(yields)
	var left []float64
	for _, y := range yields {
		if y < rf {
			left = append(left, y)
		}
	}
	if len(left) == 0 {
		return math.Inf(1)
	}
	sd := pstdev(left, rf)
	if sd == 0 {
		return math.Inf(1)
	}
	return math.Sqrt(tradingDaysOfYear*float64(len(left))/float64(len(yields))) * (mu - rf) / sd
}
