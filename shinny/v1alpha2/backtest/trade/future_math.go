package trade

import (
	"strings"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest/data"
)

// FutureMarginPerLot calculates the margin required per lot for a futures contract.
// Priority: userOverride > meta.MarginRatio > default (10%).
func FutureMarginPerLot(lastPrice float64, vm int64, meta data.InstrumentMeta, userOverride *float64) float64 {
	if userOverride != nil && *userOverride > 0 {
		return *userOverride
	}
	vmf := float64(vm)
	if vmf == 0 {
		vmf = float64(meta.VolumeMultiple)
	}
	if vmf == 0 {
		vmf = 1
	}
	ratio := meta.MarginRatio
	if ratio <= 0 {
		ratio = 0.1
	}
	return lastPrice * vmf * ratio
}

// FutureCommission calculates the commission for a trade.
// Priority: userOverride > meta.CommissionPerLot > meta.CommissionRate > default.
func FutureCommission(tradePrice float64, volume int64, vm int64, meta data.InstrumentMeta, userOverride *float64) float64 {
	vol := float64(volume)
	if userOverride != nil && *userOverride >= 0 {
		return *userOverride * vol
	}
	if meta.CommissionPerLot > 0 {
		return meta.CommissionPerLot * vol
	}
	if meta.CommissionRate > 0 {
		vmf := float64(vm)
		if vmf == 0 {
			vmf = 1
		}
		return tradePrice * vmf * meta.CommissionRate * vol
	}
	// Default: ~2.3 per 10000 of notional (typical Chinese futures)
	vmf := float64(vm)
	if vmf == 0 {
		vmf = 1
	}
	return tradePrice * vmf * 0.000023 * vol
}

// FutureCloseProfit calculates the realized P&L from closing a position.
// posDir is "LONG" or "SHORT" — the direction of the position being closed.
func FutureCloseProfit(posDir string, tradePrice, positionPrice float64, volume, vm int64) float64 {
	vmf := float64(vm)
	if vmf == 0 {
		vmf = 1
	}
	vol := float64(volume)
	if strings.EqualFold(posDir, "LONG") {
		return (tradePrice - positionPrice) * vol * vmf
	}
	return (positionPrice - tradePrice) * vol * vmf
}

// FutureFloatProfit calculates the unrealized P&L using the original open price.
func FutureFloatProfit(posDir string, lastPrice, openPrice float64, volume, vm int64) float64 {
	vmf := float64(vm)
	if vmf == 0 {
		vmf = 1
	}
	vol := float64(volume)
	if vol == 0 {
		return 0
	}
	if strings.EqualFold(posDir, "LONG") {
		return (lastPrice - openPrice) * vol * vmf
	}
	return (openPrice - lastPrice) * vol * vmf
}

// FuturePositionProfit calculates position profit using the settlement/position price.
func FuturePositionProfit(posDir string, lastPrice, positionPrice float64, volume, vm int64) float64 {
	return FutureFloatProfit(posDir, lastPrice, positionPrice, volume, vm)
}

// resolveVM returns a non-zero volume multiple.
func resolveVM(quote api.Quote, meta data.InstrumentMeta) int64 {
	if quote.VolumeMultiple > 0 {
		return quote.VolumeMultiple
	}
	if meta.VolumeMultiple > 0 {
		return meta.VolumeMultiple
	}
	return 1
}
