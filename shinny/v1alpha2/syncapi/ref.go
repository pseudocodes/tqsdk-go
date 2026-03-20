// Package syncapi provides a synchronous wait_update / is_changing
// programming model on top of tqsdk-go's channel-based async API,
// mirroring Python tqsdk's TqApi pattern.
//
// Usage:
//
//	sa := syncapi.New(market, trade)
//	quote, _ := sa.GetQuote(ctx, "SHFE.rb2501")
//	for {
//	    sa.WaitUpdate()
//	    if sa.IsChanging(quote) { fmt.Println(quote.Get().LastPrice) }
//	}
package syncapi

// Ref is the unified interface for all trackable data objects.
// Users pass Ref values to SyncApi.IsChanging to check whether
// the object changed in the latest WaitUpdate round.
type Ref interface {
	refKey() refKey
}

type refKind int

const (
	refQuote    refKind = iota
	refKline            // series-level
	refKlineBar         // bar-level (§8.8)
	refTick
	refAccount
	refPosition
	refOrder
	refTradeFill
)

type refKey struct {
	kind      refKind
	symbol    string
	duration  int    // kline only
	barIndex  int    // KlineBar only (supports negative index)
	accountID string // trade side
	orderID   string // order/trade fill
}

// ChangedFields records which fields changed in an event.
// Key is the field name (matching JSON tag), value is always true.
// A nil value means "unknown which fields changed" (degrades to object-level).
type ChangedFields map[string]bool
