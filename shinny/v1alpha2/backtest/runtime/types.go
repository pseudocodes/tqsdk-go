package runtime

import (
	"fmt"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
)

type QuoteBasisPolicy string

const (
	QuoteBasisCompatAuto1m QuoteBasisPolicy = "compat_auto_1m"
	QuoteBasisOnOrderAuto1m QuoteBasisPolicy = "on_order_auto_1m"
	QuoteBasisNoAuto1m      QuoteBasisPolicy = "no_auto_1m"
)

type MatchPricePolicy string

const (
	MatchPriceCompatOHLCPath MatchPricePolicy = "compat_ohlc_path"
	MatchPriceCloseOnlyFast  MatchPricePolicy = "close_only_fast"
)

type QuoteBasisReason string

const (
	QuoteBasisReasonQuoteSubscribe QuoteBasisReason = "quote_subscribe"
	QuoteBasisReasonOrderInsert    QuoteBasisReason = "order_insert"
)

type EventType string

const (
	EventTick      EventType = "TICK"
	EventKlineOpen EventType = "KLINE_OPEN"
	EventKlineClose EventType = "KLINE_CLOSE"
	EventQuote     EventType = "QUOTE"
)

type RuntimeState string

const (
	RuntimeStateInit     RuntimeState = "INIT"
	RuntimeStateLoading  RuntimeState = "LOADING"
	RuntimeStateReady    RuntimeState = "READY"
	RuntimeStateRunning  RuntimeState = "RUNNING"
	RuntimeStateFinished RuntimeState = "FINISHED"
	RuntimeStateClosed   RuntimeState = "CLOSED"
)

type RuntimeConfig struct {
	StartDT time.Time
	EndDT   time.Time

	Symbols        []string
	KlineDurations []int

	InitialBalance float64

	// Optional overrides — nil means "use instrument meta or default".
	CommissionOverride  *float64           // global default
	MarginOverride      *float64           // global default
	CommissionOverrides map[string]float64 // per-symbol commission (GAP-06)
	MarginOverrides     map[string]float64 // per-symbol margin (GAP-06)

	AutoRun  bool
	StepMode bool

	StrictMode bool

	QuoteBasisPolicy QuoteBasisPolicy
	MatchPricePolicy MatchPricePolicy

	FastBacktestPreset bool
}

type MarketEvent struct {
	EventTimeNS int64
	Symbol      string
	EventType   EventType
	DurationNS  int64

	Tick  *api.Tick
	Kline *api.Kline
	Quote *api.Quote
}

type MarketEventBatch struct {
	BatchTimeNS int64
	Events      []MarketEvent
	TradingDay  string
	DaySwitched bool

	// MainContractMap is populated on DaySwitched when a main contract mapping
	// is available. Keys are continuous symbols (e.g. "KQ.m@SHFE.cu"),
	// values are the mapped underlying symbols (e.g. "SHFE.cu2503"). (GAP-01)
	MainContractMap map[string]string
}

type RuntimeStatusEvent struct {
	State RuntimeState
	Diff  map[string]any
	At    time.Time
}

func NormalizeConfig(cfg RuntimeConfig) RuntimeConfig {
	out := cfg
	if out.QuoteBasisPolicy == "" {
		out.QuoteBasisPolicy = QuoteBasisCompatAuto1m
	}
	if out.MatchPricePolicy == "" {
		out.MatchPricePolicy = MatchPriceCompatOHLCPath
	}
	if out.FastBacktestPreset {
		out.QuoteBasisPolicy = QuoteBasisOnOrderAuto1m
		out.MatchPricePolicy = MatchPriceCloseOnlyFast
	}
	if len(out.KlineDurations) == 0 {
		out.KlineDurations = []int{60}
	}
	if out.InitialBalance <= 0 {
		out.InitialBalance = 10_000_000
	}
	return out
}

// ValidateConfig checks that the runtime config is self-consistent.
func ValidateConfig(cfg RuntimeConfig) error {
	if cfg.StartDT.IsZero() {
		return fmt.Errorf("start_dt is required")
	}
	if cfg.EndDT.IsZero() {
		return fmt.Errorf("end_dt is required")
	}
	if !cfg.StartDT.Before(cfg.EndDT) {
		return fmt.Errorf("start_dt must be before end_dt")
	}
	return nil
}
