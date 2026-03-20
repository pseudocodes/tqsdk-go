package data

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
)

type DownloadConfig struct {
	StartDT              time.Time
	EndDT                time.Time
	Symbols              []string
	KlineDurations       []int
	StrictMode           bool
	EnableAuto1mForQuote bool
	SkipTicks            bool                // skip tick data download (only klines)
	OnProgress           func(stage string)  // optional progress callback
	SymbolInfoTimeout    time.Duration       // per-symbol-info query timeout (default 15s)
	PerSeriesTimeout     time.Duration       // per-kline/tick series download timeout (default 60s)
}

// BuildMemoryProviderFromMarket downloads historical data from an existing market service
// and materializes an in-memory provider for deterministic backtest runtime usage.
func BuildMemoryProviderFromMarket(ctx context.Context, market api.MarketService, cfg DownloadConfig) (*MemoryProvider, error) {
	if market == nil {
		return nil, fmt.Errorf("market service is required")
	}
	mp := NewMemoryProvider()
	symbols := normalizeSymbols(cfg.Symbols)
	if len(symbols) == 0 {
		return mp, nil
	}

	progress := cfg.OnProgress
	if progress == nil {
		progress = func(string) {}
	}

	symbolInfoTimeout := cfg.SymbolInfoTimeout
	if symbolInfoTimeout <= 0 {
		symbolInfoTimeout = 15 * time.Second
	}
	perSeriesTimeout := cfg.PerSeriesTimeout
	if perSeriesTimeout <= 0 {
		perSeriesTimeout = 60 * time.Second
	}

	progress("querying symbol info")
	// Use a dedicated shorter timeout for symbol info — expired contracts
	// may not be served by the server.
	siCtx, siCancel := context.WithTimeout(ctx, symbolInfoTimeout)
	quotes, qErr := market.QuerySymbolInfo(siCtx, symbols)
	siCancel()
	if qErr == nil {
		for _, q := range quotes {
			s := q.Symbol
			if s == "" {
				s = joinSymbol(q.ExchangeID, q.InstrumentID)
			}
			if strings.TrimSpace(s) == "" {
				continue
			}
			mp.SetMeta(s, InstrumentMeta{
				Symbol:           s,
				PriceTick:        nonZeroFloat(q.PriceTick, 1),
				VolumeMultiple:   nonZeroInt64(q.VolumeMultiple, 1),
				InsClass:         q.InsClass,
				StrikePrice:      q.StrikePrice,
				OptionClass:      q.OptionClass,
				UnderlyingSymbol: q.UnderlyingSymbol,
				ExpireDatetime:   q.ExpireDatetime,
				TradingTime:      q.TradingTime,
			})
		}
	} else if cfg.StrictMode && !isUnsupported(qErr) {
		return nil, qErr
	}

	durs := normalizeDurations(cfg.KlineDurations)
	if cfg.EnableAuto1mForQuote {
		durs = appendIfMissingInt(durs, 60)
	}

	for _, symbol := range symbols {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if !cfg.SkipTicks {
			progress(fmt.Sprintf("downloading ticks: %s", symbol))
			tickCtx, tickCancel := context.WithTimeout(ctx, perSeriesTimeout)
			if ticks, err := market.GetTickDataSeries(tickCtx, symbol, cfg.StartDT, cfg.EndDT); err == nil {
				rows := make([]api.Tick, 0, len(ticks.Data))
				for _, t := range ticks.Data {
					if t == nil {
						continue
					}
					rows = append(rows, *t)
				}
				if len(rows) > 0 {
					mp.SetTickSeries(symbol, rows)
				}
			} else if cfg.StrictMode && !isUnsupported(err) {
				tickCancel()
				return nil, err
			}
			tickCancel()
		}

		for _, dur := range durs {
			progress(fmt.Sprintf("downloading klines: %s dur=%d", symbol, dur))
			kCtx, kCancel := context.WithTimeout(ctx, perSeriesTimeout)
			series, err := market.GetKlineDataSeries(kCtx, symbol, dur, cfg.StartDT, cfg.EndDT)
			kCancel()
			if err != nil {
				progress(fmt.Sprintf("  kline download error: %s dur=%d: %v", symbol, dur, err))
				if cfg.StrictMode && !isUnsupported(err) {
					return nil, err
				}
				continue
			}
			rows := make([]api.Kline, 0, len(series.Data))
			for _, k := range series.Data {
				if k == nil {
					continue
				}
				rows = append(rows, *k)
			}
			progress(fmt.Sprintf("  kline stored: %s dur=%d rows=%d complete=%v", symbol, dur, len(rows), series.Complete))
			if len(rows) > 0 {
				mp.SetKlineSeries(symbol, dur, rows)
			}
		}
	}
	return mp, nil
}

func normalizeSymbols(symbols []string) []string {
	set := map[string]struct{}{}
	out := make([]string, 0, len(symbols))
	for _, s := range symbols {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := set[s]; ok {
			continue
		}
		set[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func normalizeDurations(in []int) []int {
	set := map[int]struct{}{}
	out := make([]int, 0, len(in))
	for _, d := range in {
		if d <= 0 {
			continue
		}
		if _, ok := set[d]; ok {
			continue
		}
		set[d] = struct{}{}
		out = append(out, d)
	}
	sort.Ints(out)
	if len(out) == 0 {
		out = []int{60}
	}
	return out
}

func appendIfMissingInt(in []int, v int) []int {
	for _, x := range in {
		if x == v {
			return in
		}
	}
	return append(in, v)
}

func joinSymbol(exchangeID, instrumentID string) string {
	if strings.TrimSpace(exchangeID) == "" {
		return instrumentID
	}
	return exchangeID + "." + instrumentID
}

func nonZeroFloat(v, fallback float64) float64 {
	if v == 0 {
		return fallback
	}
	return v
}

func nonZeroInt64(v, fallback int64) int64 {
	if v == 0 {
		return fallback
	}
	return v
}

func isUnsupported(err error) bool {
	if err == nil {
		return false
	}
	var se *api.SDKError
	if errors.As(err, &se) {
		return se.Code == api.ErrUnsupported || se.Code == api.ErrUnsupportedBacktest
	}
	return false
}

// Ensure interface compliance.
var _ DataProvider = (*MemoryProvider)(nil)
