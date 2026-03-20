package trade

import (
	"math"
	"strings"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
)

// MatchOrder evaluates whether an order can be filled against the current quote.
// Returns updated (status, message, fillPrice).
// Aligns with Python tqsdk baseline: limit orders fill at the declared limit price
// (standard Chinese futures exchange behaviour).
func MatchOrder(order api.Order, quote api.Quote) (status, lastMsg string, price float64) {
	status, lastMsg = "ALIVE", ""
	ask, bid := GetPriceRange(quote)
	isIOC := strings.EqualFold(order.TimeCondition, "IOC")
	isBuy := strings.EqualFold(order.Direction, string(api.DirectionBuy))

	if strings.EqualFold(order.PriceType, "ANY") {
		// Market order — always IOC semantics
		if isBuy {
			price = ask
		} else {
			price = bid
		}
		if math.IsNaN(price) || price <= 0 {
			return "FINISHED", "市价指令剩余撤销", math.NaN()
		}
		return "FINISHED", "全部成交", price
	}

	// LIMIT order
	price = order.LimitPrice

	if isBuy {
		if math.IsNaN(ask) || ask <= 0 {
			// No valid ask — cannot determine if fillable
			if isIOC {
				return "FINISHED", "已撤单报单已提交", price
			}
			return "ALIVE", "", price
		}
		if price >= ask {
			return "FINISHED", "全部成交", price
		}
		// Price below ask, cannot fill now
		if isIOC {
			return "FINISHED", "已撤单报单已提交", price
		}
		return "ALIVE", "", price
	}

	// SELL
	if math.IsNaN(bid) || bid <= 0 {
		if isIOC {
			return "FINISHED", "已撤单报单已提交", price
		}
		return "ALIVE", "", price
	}
	if price <= bid {
		return "FINISHED", "全部成交", price
	}
	if isIOC {
		return "FINISHED", "已撤单报单已提交", price
	}
	return "ALIVE", "", price
}

// GetPriceRange extracts ask and bid from a quote.
// Falls back to last_price ± price_tick for instruments without an order book (e.g. index).
func GetPriceRange(q api.Quote) (ask, bid float64) {
	ask = q.AskPrice1
	bid = q.BidPrice1
	pt := q.PriceTick
	if pt <= 0 {
		pt = 1
	}
	if ask <= 0 || math.IsNaN(ask) {
		if q.LastPrice > 0 {
			ask = q.LastPrice + pt
		} else {
			ask = math.NaN()
		}
	}
	if bid <= 0 || math.IsNaN(bid) {
		if q.LastPrice > 0 {
			bid = q.LastPrice - pt
		} else {
			bid = math.NaN()
		}
	}
	return ask, bid
}

// CanTradeNow checks whether the current time falls within the instrument's trading hours.
// Returns true when TradingTime is empty (permissive default for sim environments).
func CanTradeNow(tt api.TradingTime, currentTime time.Time) bool {
	sessions := append(append([][]string(nil), tt.Day...), tt.Night...)
	if len(sessions) == 0 {
		return true // permissive default
	}
	nowStr := currentTime.Format("15:04:05")
	for _, seg := range sessions {
		if len(seg) < 2 || seg[0] == "" || seg[1] == "" {
			continue
		}
		start, end := seg[0], seg[1]
		if crossesMidnight(start, end) {
			if nowStr >= start || nowStr <= end {
				return true
			}
		} else {
			if nowStr >= start && nowStr <= end {
				return true
			}
		}
	}
	return false
}

func crossesMidnight(start, end string) bool {
	return start > end
}
