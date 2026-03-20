package domain

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"sync"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
)

//go:embed expired_quotes.json.gz
var embeddedQuotesGZ []byte

// compactQuoteEntry mirrors the compact JSON schema produced by _gen_embedded_quotes.py.
type compactQuoteEntry struct {
	PT float64    `json:"pt"`           // price_tick
	VM float64    `json:"vm"`           // volume_multiple
	IC string     `json:"ic"`           // ins_class
	SP float64    `json:"sp,omitempty"` // strike_price
	OC string     `json:"oc,omitempty"` // option_class
	US string     `json:"us,omitempty"` // underlying_symbol
	ED float64    `json:"ed,omitempty"` // expire_datetime
	MG float64    `json:"mg,omitempty"` // margin (ratio)
	CM float64    `json:"cm,omitempty"` // commission
	TT *compactTT `json:"tt,omitempty"` // trading_time
}

type compactTT struct {
	Day   [][]string `json:"day,omitempty"`
	Night [][]string `json:"night,omitempty"`
}

var (
	embeddedOnce sync.Once
	embeddedData map[string]compactQuoteEntry
)

func loadEmbeddedQuotes() {
	embeddedOnce.Do(func() {
		r, err := gzip.NewReader(bytes.NewReader(embeddedQuotesGZ))
		if err != nil {
			embeddedData = map[string]compactQuoteEntry{}
			return
		}
		defer r.Close()
		var m map[string]compactQuoteEntry
		if err := json.NewDecoder(r).Decode(&m); err != nil {
			embeddedData = map[string]compactQuoteEntry{}
			return
		}
		embeddedData = m
	})
}

// lookupEmbeddedQuote returns the embedded Quote for a symbol if available.
func lookupEmbeddedQuote(symbol string) (api.Quote, bool) {
	loadEmbeddedQuotes()
	e, ok := embeddedData[symbol]
	if !ok {
		return api.Quote{}, false
	}
	q := api.Quote{
		Symbol:           symbol,
		InstrumentID:     symbol,
		PriceTick:        e.PT,
		VolumeMultiple:   int64(e.VM),
		InsClass:         e.IC,
		StrikePrice:      e.SP,
		OptionClass:      e.OC,
		UnderlyingSymbol: e.US,
		ExpireDatetime:   e.ED,
		Expired:          true,
	}
	if e.TT != nil {
		q.TradingTime = api.TradingTime{
			Day:   e.TT.Day,
			Night: e.TT.Night,
		}
	}
	return q, true
}

// isEmptyQuote returns true if the Quote has no meaningful instrument metadata
// (i.e. the server returned a stub with no price_tick / volume_multiple).
func isEmptyQuote(q api.Quote) bool {
	return q.PriceTick == 0 && q.VolumeMultiple == 0 && q.InsClass == ""
}
