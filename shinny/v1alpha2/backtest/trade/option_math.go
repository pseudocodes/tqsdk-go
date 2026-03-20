package trade

import (
	"math"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest/data"
)

// OptionMarginPerLot calculates margin for a SHORT option position (per lot).
// Long options require no margin — only premium is paid.
// Uses the simplified Chinese futures-option margin formula.
func OptionMarginPerLot(optionLast, underlyingLast, strikePrice float64, vm int64, optionClass string, meta data.InstrumentMeta) float64 {
	vmf := float64(vm)
	if vmf == 0 {
		vmf = 1
	}
	ratio := meta.MarginRatio
	if ratio <= 0 {
		ratio = 0.1
	}

	// Out-of-the-money value
	var otm float64
	if optionClass == "CALL" {
		otm = math.Max(0, strikePrice-underlyingLast)
	} else {
		otm = math.Max(0, underlyingLast-strikePrice)
	}

	// Simplified option margin formula (common for Chinese futures options):
	// margin = optionLast * vm + max(underlying * vm * ratio - OTM * vm,
	//                                 0.5 * underlying * vm * ratio)
	component1 := underlyingLast*vmf*ratio - otm*vmf
	component2 := 0.5 * underlyingLast * vmf * ratio
	margin := optionLast*vmf + math.Max(component1, component2)

	// For PUT options, cap at strike_price * vm * ratio
	if optionClass == "PUT" {
		capVal := strikePrice * vmf * ratio
		if margin > capVal {
			margin = capVal
		}
	}

	if margin < 0 {
		margin = 0
	}
	return margin
}

// OptionPremium calculates the premium flow for an option trade.
// BUY → negative (pay premium), SELL → positive (receive premium).
func OptionPremium(direction string, tradePrice float64, volume, vm int64) float64 {
	vmf := float64(vm)
	if vmf == 0 {
		vmf = 1
	}
	vol := float64(volume)
	premium := tradePrice * vol * vmf
	if direction == "BUY" {
		return -premium
	}
	return premium
}

// OptionCloseProfit calculates realized P&L from closing an option position.
func OptionCloseProfit(posDir string, tradePrice, positionPrice float64, volume, vm int64) float64 {
	return FutureCloseProfit(posDir, tradePrice, positionPrice, volume, vm)
}
