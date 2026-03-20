package trade

import (
	"strings"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
)

// DailySnapshot holds end-of-day state for report collection.
type DailySnapshot struct {
	TradingDay     string
	Balance        float64
	Available      float64
	Margin         float64
	Commission     float64
	CloseProfit    float64
	FloatProfit    float64
	PositionProfit float64
	StaticBalance  float64
	Trades         []api.Trade              // all trades executed this day
	Positions      map[string]api.Position  // end-of-day position snapshot
}

// SettlementObserver receives a callback after each daily settlement.
type SettlementObserver interface {
	OnSettlement(snap DailySnapshot)
}

// Settle performs end-of-day settlement for the sim session.
//  1. Cancel all GFD alive orders
//  2. Migrate today's positions to history
//  3. Update account pre_balance/static_balance and reset daily counters
func (ss *simSession) Settle(tradingDay string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	ss.settleOrdersLocked()

	// GAP-09: collect daily trades and position snapshot before settlement resets
	dayTrades := append([]api.Trade(nil), ss.dailyTrades...)
	posSnap := make(map[string]api.Position, len(ss.positions))
	for sym, p := range ss.positions {
		posSnap[sym] = p
	}

	ss.settlePositionsLocked()
	ss.recalcAccountLocked()
	ss.settleAccountLocked()

	snap := DailySnapshot{
		TradingDay:     tradingDay,
		Balance:        ss.account.Balance,
		Available:      ss.account.Available,
		Margin:         ss.account.Margin,
		Commission:     ss.account.Commission,
		CloseProfit:    ss.account.CloseProfit,
		FloatProfit:    ss.account.FloatProfit,
		PositionProfit: ss.account.PositionProfit,
		StaticBalance:  ss.account.StaticBalance,
		Trades:         dayTrades,
		Positions:      posSnap,
	}

	// Reset daily trades for next trading day
	ss.dailyTrades = ss.dailyTrades[:0]

	if ss.svc.settlementObserver != nil {
		ss.svc.settlementObserver.OnSettlement(snap)
	}
}

// settleOrdersLocked cancels all GFD alive orders.
func (ss *simSession) settleOrdersLocked() {
	for id, ord := range ss.orders {
		if strings.EqualFold(ord.Status, "ALIVE") && strings.EqualFold(ord.TimeCondition, "GFD") {
			ord.Status = "FINISHED"
			ord.LastMsg = "已撤单"
			ss.orders[id] = ord
			ss.releaseFrozenLocked(id)
			go ss.emitOrderEvent(api.OrderEvent{
				Kind: api.TradeEventUpsert, AccountID: ss.accountID,
				OrderID: id, Order: ord, ChangedAt: time.Now(),
			})
		}
	}
	// Clear remaining frozen tracking
	ss.orderFrozens = map[string]*orderFrozen{}
	ss.account.FrozenMargin = 0
	ss.account.FrozenPremium = 0
}

// settlePositionsLocked migrates today volumes to history and updates position prices.
func (ss *simSession) settlePositionsLocked() {
	for sym, pos := range ss.positions {
		q, hasQuote := ss.latestQuotes[sym]
		settlementPrice := q.LastPrice
		if !hasQuote || settlementPrice <= 0 {
			if pos.PositionPriceLong > 0 {
				settlementPrice = pos.PositionPriceLong
			} else {
				settlementPrice = pos.OpenPriceLong
			}
		}

		// Migrate today → history
		pos.VolumeLongHis += pos.VolumeLongToday
		pos.VolumeLongToday = 0
		pos.PosLongHis = pos.VolumeLongHis
		pos.PosLongToday = 0

		pos.VolumeShortHis += pos.VolumeShortToday
		pos.VolumeShortToday = 0
		pos.PosShortHis = pos.VolumeShortHis
		pos.PosShortToday = 0

		// Clear frozen volumes
		pos.VolumeLongFrozenToday = 0
		pos.VolumeLongFrozenHis = 0
		pos.VolumeLongFrozen = 0
		pos.VolumeShortFrozenToday = 0
		pos.VolumeShortFrozenHis = 0
		pos.VolumeShortFrozen = 0

		// Update position price to settlement price
		vm := q.VolumeMultiple
		if vm == 0 {
			vm = 1
		}
		if pos.VolumeLong > 0 {
			pos.PositionPriceLong = settlementPrice
			pos.PositionCostLong = settlementPrice * float64(pos.VolumeLong) * float64(vm)
		}
		if pos.VolumeShort > 0 {
			pos.PositionPriceShort = settlementPrice
			pos.PositionCostShort = settlementPrice * float64(pos.VolumeShort) * float64(vm)
		}

		ss.positions[sym] = pos
	}
}

// settleAccountLocked updates pre_balance/static_balance and resets daily counters.
func (ss *simSession) settleAccountLocked() {
	ss.account.PreBalance = ss.account.Balance
	ss.account.StaticBalance = ss.account.Balance
	// Reset daily cumulative fields
	ss.account.Commission = 0
	ss.account.CloseProfit = 0
	ss.account.Premium = 0
	ss.account.Deposit = 0
	ss.account.Withdraw = 0
}
