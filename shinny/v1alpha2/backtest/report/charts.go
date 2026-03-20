package report

import (
	"math"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest/trade"
)

// ChartData represents an ECharts-compatible chart configuration.
type ChartData struct {
	Title  ChartTitle  `json:"title"`
	XAxis  ChartAxis   `json:"xAxis"`
	YAxis  ChartAxis   `json:"yAxis"`
	Series []ChartLine `json:"series"`
}

// ChartTitle holds chart title configuration.
type ChartTitle struct {
	Text string `json:"text"`
}

// ChartAxis holds axis configuration.
type ChartAxis struct {
	Type string   `json:"type"`
	Data []string `json:"data,omitempty"`
}

// ChartLine holds a single data series.
type ChartLine struct {
	Name string    `json:"name"`
	Type string    `json:"type"`
	Data []float64 `json:"data"`
}

// DailyBalanceChart generates an ECharts config for the daily balance curve.
func DailyBalanceChart(snapshots []trade.DailySnapshot, initialBalance float64) ChartData {
	dates := make([]string, 0, len(snapshots)+1)
	values := make([]float64, 0, len(snapshots)+1)

	dates = append(dates, "初始")
	values = append(values, initialBalance)

	for _, s := range snapshots {
		dates = append(dates, s.TradingDay)
		values = append(values, s.Balance)
	}

	return ChartData{
		Title: ChartTitle{Text: "每日资金"},
		XAxis: ChartAxis{Type: "category", Data: dates},
		YAxis: ChartAxis{Type: "value"},
		Series: []ChartLine{
			{Name: "账户权益", Type: "line", Data: values},
		},
	}
}

// DailyProfitChart generates an ECharts config for daily profit/loss bars.
func DailyProfitChart(snapshots []trade.DailySnapshot, initialBalance float64) ChartData {
	dates := make([]string, 0, len(snapshots))
	profits := make([]float64, 0, len(snapshots))

	prev := initialBalance
	for _, s := range snapshots {
		dates = append(dates, s.TradingDay)
		profits = append(profits, s.Balance-prev)
		prev = s.Balance
	}

	return ChartData{
		Title: ChartTitle{Text: "每日盈亏"},
		XAxis: ChartAxis{Type: "category", Data: dates},
		YAxis: ChartAxis{Type: "value"},
		Series: []ChartLine{
			{Name: "每日盈亏", Type: "bar", Data: profits},
		},
	}
}

// DrawdownChart generates an ECharts config for the drawdown curve.
func DrawdownChart(snapshots []trade.DailySnapshot) ChartData {
	dates := make([]string, 0, len(snapshots))
	dd := make([]float64, 0, len(snapshots))

	peak := 0.0
	for _, s := range snapshots {
		dates = append(dates, s.TradingDay)
		if s.Balance > peak {
			peak = s.Balance
		}
		drawdown := 0.0
		if peak > 0 {
			drawdown = (peak - s.Balance) / peak
		}
		dd = append(dd, drawdown)
	}

	return ChartData{
		Title: ChartTitle{Text: "最大回撤"},
		XAxis: ChartAxis{Type: "category", Data: dates},
		YAxis: ChartAxis{Type: "value"},
		Series: []ChartLine{
			{Name: "回撤", Type: "line", Data: dd},
		},
	}
}

// SharpeRollingChart generates an ECharts config for the 21-day rolling Sharpe ratio.
func SharpeRollingChart(snapshots []trade.DailySnapshot, initialBalance float64, window int) ChartData {
	if window <= 0 {
		window = 21
	}

	equity := make([]float64, 0, len(snapshots)+1)
	equity = append(equity, initialBalance)
	for _, s := range snapshots {
		equity = append(equity, s.Balance)
	}

	returns := DailyReturns(equity)
	dates := make([]string, 0, len(snapshots))
	values := make([]float64, 0, len(snapshots))

	for i, s := range snapshots {
		dates = append(dates, s.TradingDay)
		if i < window-1 {
			values = append(values, 0)
			continue
		}
		windowReturns := returns[i-window+1 : i+1]
		sharpe := Sharpe(windowReturns, 0.03)
		if math.IsNaN(sharpe) || math.IsInf(sharpe, 0) {
			sharpe = 0
		}
		values = append(values, sharpe)
	}

	return ChartData{
		Title: ChartTitle{Text: "滚动夏普率 (21日)"},
		XAxis: ChartAxis{Type: "category", Data: dates},
		YAxis: ChartAxis{Type: "value"},
		Series: []ChartLine{
			{Name: "夏普率", Type: "line", Data: values},
		},
	}
}

// SortinoRollingChart generates an ECharts config for the 21-day rolling Sortino ratio.
func SortinoRollingChart(snapshots []trade.DailySnapshot, initialBalance float64, window int) ChartData {
	if window <= 0 {
		window = 21
	}

	equity := make([]float64, 0, len(snapshots)+1)
	equity = append(equity, initialBalance)
	for _, s := range snapshots {
		equity = append(equity, s.Balance)
	}

	returns := DailyReturns(equity)
	dates := make([]string, 0, len(snapshots))
	values := make([]float64, 0, len(snapshots))

	for i, s := range snapshots {
		dates = append(dates, s.TradingDay)
		if i < window-1 {
			values = append(values, 0)
			continue
		}
		windowReturns := returns[i-window+1 : i+1]
		sortino := Sortino(windowReturns, 0.03)
		if math.IsNaN(sortino) || math.IsInf(sortino, 0) {
			sortino = 0
		}
		values = append(values, sortino)
	}

	return ChartData{
		Title: ChartTitle{Text: "滚动索提诺比率 (21日)"},
		XAxis: ChartAxis{Type: "category", Data: dates},
		YAxis: ChartAxis{Type: "value"},
		Series: []ChartLine{
			{Name: "索提诺比率", Type: "line", Data: values},
		},
	}
}

// GenerateAllCharts produces all 5 standard backtest charts.
func GenerateAllCharts(report BacktestReport) []ChartData {
	return []ChartData{
		DailyBalanceChart(report.Snapshots, report.InitialBalance),
		DailyProfitChart(report.Snapshots, report.InitialBalance),
		DrawdownChart(report.Snapshots),
		SharpeRollingChart(report.Snapshots, report.InitialBalance, 21),
		SortinoRollingChart(report.Snapshots, report.InitialBalance, 21),
	}
}
