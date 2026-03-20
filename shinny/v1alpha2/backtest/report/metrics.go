package report

import (
	"math"
)

// TotalReturn computes the simple total return.
func TotalReturn(initial, final float64) float64 {
	if initial == 0 {
		return 0
	}
	return (final - initial) / initial
}

// AnnualReturn converts a total return to an annualized return.
func AnnualReturn(totalReturn float64, tradingDays int) float64 {
	if tradingDays <= 0 {
		return 0
	}
	years := float64(tradingDays) / 252.0
	if years == 0 {
		return 0
	}
	return math.Pow(1+totalReturn, 1.0/years) - 1
}

// MaxDrawdown computes the maximum drawdown from a series of equity values.
func MaxDrawdown(equity []float64) float64 {
	if len(equity) == 0 {
		return 0
	}
	peak := equity[0]
	maxDD := 0.0
	for _, v := range equity {
		if v > peak {
			peak = v
		}
		if peak > 0 {
			dd := (peak - v) / peak
			if dd > maxDD {
				maxDD = dd
			}
		}
	}
	return maxDD
}

// Sharpe computes the annualized Sharpe ratio.
// returns: daily returns, rf: annualized risk-free rate.
// Returns 0 when there is no actual return volatility (all returns identical).
func Sharpe(returns []float64, rf float64) float64 {
	if len(returns) < 2 {
		return 0
	}
	// If all returns are zero (no trades), ratio is meaningless — return 0.
	hasNonZero := false
	for _, r := range returns {
		if r != 0 {
			hasNonZero = true
			break
		}
	}
	if !hasNonZero {
		return 0
	}
	dailyRF := rf / 252.0
	excess := make([]float64, len(returns))
	for i, r := range returns {
		excess[i] = r - dailyRF
	}
	mean := meanF(excess)
	std := stddevF(excess, mean)
	if std == 0 {
		return 0
	}
	return (mean / std) * math.Sqrt(252)
}

// Sortino computes the annualized Sortino ratio (downside deviation only).
// Returns 0 when there is no actual return volatility (all returns identical).
func Sortino(returns []float64, rf float64) float64 {
	if len(returns) < 2 {
		return 0
	}
	// If all returns are zero (no trades), ratio is meaningless — return 0.
	hasNonZero := false
	for _, r := range returns {
		if r != 0 {
			hasNonZero = true
			break
		}
	}
	if !hasNonZero {
		return 0
	}
	dailyRF := rf / 252.0
	excess := make([]float64, len(returns))
	for i, r := range returns {
		excess[i] = r - dailyRF
	}
	mean := meanF(excess)
	dd := downsideDeviation(excess, 0)
	if dd == 0 {
		return 0
	}
	return (mean / dd) * math.Sqrt(252)
}

// Calmar computes the Calmar ratio = annualized return / max drawdown.
func Calmar(annualReturn, maxDrawdown float64) float64 {
	if maxDrawdown == 0 {
		return 0
	}
	return annualReturn / maxDrawdown
}

// DailyReturns converts an equity curve into a series of daily returns.
func DailyReturns(equity []float64) []float64 {
	if len(equity) < 2 {
		return nil
	}
	out := make([]float64, len(equity)-1)
	for i := 1; i < len(equity); i++ {
		if equity[i-1] == 0 {
			out[i-1] = 0
		} else {
			out[i-1] = (equity[i] - equity[i-1]) / equity[i-1]
		}
	}
	return out
}

func meanF(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range data {
		sum += v
	}
	return sum / float64(len(data))
}

func stddevF(data []float64, mean float64) float64 {
	if len(data) < 2 {
		return 0
	}
	sumSq := 0.0
	for _, v := range data {
		d := v - mean
		sumSq += d * d
	}
	return math.Sqrt(sumSq / float64(len(data)-1))
}

func downsideDeviation(data []float64, threshold float64) float64 {
	if len(data) < 2 {
		return 0
	}
	sumSq := 0.0
	n := 0
	for _, v := range data {
		if v < threshold {
			d := v - threshold
			sumSq += d * d
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return math.Sqrt(sumSq / float64(n))
}
