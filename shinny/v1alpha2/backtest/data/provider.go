package data

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
)

type InstrumentMeta struct {
	Symbol         string
	PriceTick      float64
	VolumeMultiple int64

	// Extended fields for margin/commission/option calculations.
	InsClass         string  // "FUTURE", "OPTION", "STOCK", etc.
	MarginRatio      float64 // e.g. 0.1 for 10%
	CommissionPerLot float64 // fixed commission per lot (0 → use rate)
	CommissionRate   float64 // commission as ratio of notional (0 → use per-lot or default)
	StrikePrice      float64 // for options
	OptionClass      string  // "CALL" or "PUT"
	UnderlyingSymbol string  // underlying for options
	ExpireDatetime   float64 // expire datetime as epoch seconds (0 = never expires)
	TradingTime      api.TradingTime
}

type DataProvider interface {
	LoadInstrumentMeta(ctx context.Context, symbols []string) (map[string]InstrumentMeta, error)
	LoadTickSeries(ctx context.Context, symbol string, start, end time.Time) ([]api.Tick, error)
	LoadKlineSeries(ctx context.Context, symbol string, durationSeconds int, start, end time.Time) ([]api.Kline, error)

	// LoadMainContractMap returns the main contract mapping for a given trading day.
	// Keys are continuous contract symbols (e.g. "KQ.m@SHFE.cu"),
	// values are the corresponding underlying symbols (e.g. "SHFE.cu2503").
	// Returns nil/empty if no mapping is available (GAP-01).
	LoadMainContractMap(ctx context.Context, tradingDay string) (map[string]string, error)
}

type MemoryProvider struct {
	mu              sync.RWMutex
	meta            map[string]InstrumentMeta
	ticks           map[string][]api.Tick
	klines          map[string]map[int][]api.Kline
	mainContractMap map[string]map[string]string // tradingDay -> (contSymbol -> underlying)
}

func NewMemoryProvider() *MemoryProvider {
	return &MemoryProvider{
		meta:            map[string]InstrumentMeta{},
		ticks:           map[string][]api.Tick{},
		klines:          map[string]map[int][]api.Kline{},
		mainContractMap: map[string]map[string]string{},
	}
}

func (p *MemoryProvider) SetMeta(symbol string, m InstrumentMeta) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if m.Symbol == "" {
		m.Symbol = symbol
	}
	p.meta[symbol] = m
}

func (p *MemoryProvider) SetTickSeries(symbol string, rows []api.Tick) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := append([]api.Tick(nil), rows...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Datetime < cp[j].Datetime })
	p.ticks[symbol] = cp
}

func (p *MemoryProvider) SetKlineSeries(symbol string, durationSeconds int, rows []api.Kline) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.klines[symbol] == nil {
		p.klines[symbol] = map[int][]api.Kline{}
	}
	cp := append([]api.Kline(nil), rows...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Datetime < cp[j].Datetime })
	p.klines[symbol][durationSeconds] = cp
}

func (p *MemoryProvider) LoadInstrumentMeta(ctx context.Context, symbols []string) (map[string]InstrumentMeta, error) {
	_ = ctx
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]InstrumentMeta, len(symbols))
	for _, s := range symbols {
		if v, ok := p.meta[s]; ok {
			out[s] = v
			continue
		}
		out[s] = InstrumentMeta{Symbol: s, PriceTick: 1, VolumeMultiple: 1}
	}
	return out, nil
}

func (p *MemoryProvider) LoadTickSeries(ctx context.Context, symbol string, start, end time.Time) ([]api.Tick, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	rows, ok := p.ticks[symbol]
	if !ok {
		return nil, fmt.Errorf("tick series not found: %s", symbol)
	}
	return filterTicks(rows, start.UnixNano(), end.UnixNano()), nil
}

func (p *MemoryProvider) LoadKlineSeries(ctx context.Context, symbol string, durationSeconds int, start, end time.Time) ([]api.Kline, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	byDur, ok := p.klines[symbol]
	if !ok {
		return nil, fmt.Errorf("kline series not found: %s", symbol)
	}
	rows, ok := byDur[durationSeconds]
	if !ok {
		return nil, fmt.Errorf("kline series not found: %s@%ds", symbol, durationSeconds)
	}
	return filterKlines(rows, start.UnixNano(), end.UnixNano()), nil
}

// SetMainContractMap stores the main contract mapping for a given trading day.
func (p *MemoryProvider) SetMainContractMap(tradingDay string, mapping map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make(map[string]string, len(mapping))
	for k, v := range mapping {
		cp[k] = v
	}
	p.mainContractMap[tradingDay] = cp
}

// LoadMainContractMap implements DataProvider.
func (p *MemoryProvider) LoadMainContractMap(ctx context.Context, tradingDay string) (map[string]string, error) {
	_ = ctx
	p.mu.RLock()
	defer p.mu.RUnlock()
	m, ok := p.mainContractMap[tradingDay]
	if !ok {
		return nil, nil
	}
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp, nil
}

// LazyLoader is an optional interface that a DataProvider may implement to
// support on-demand data loading from a live market service.
type LazyLoader interface {
	EnsureKlineSeries(ctx context.Context, symbol string, durationSeconds int, start, end time.Time, market api.MarketService) error
	EnsureTickSeries(ctx context.Context, symbol string, start, end time.Time, market api.MarketService) error
}

// EnsureKlineSeries downloads kline data for symbol+duration if not already present.
// Implements LazyLoader.
func (p *MemoryProvider) EnsureKlineSeries(ctx context.Context, symbol string, durationSeconds int, start, end time.Time, market api.MarketService) error {
	p.mu.RLock()
	_, hasSymbol := p.klines[symbol]
	if hasSymbol {
		_, hasDur := p.klines[symbol][durationSeconds]
		if hasDur {
			p.mu.RUnlock()
			return nil
		}
	}
	p.mu.RUnlock()

	// Download from live market service.
	res, err := market.GetKlineDataSeries(ctx, symbol, durationSeconds, start, end)
	if err != nil {
		return err
	}
	rows := make([]api.Kline, 0, len(res.Data))
	for _, k := range res.Data {
		if k != nil {
			rows = append(rows, *k)
		}
	}
	p.SetKlineSeries(symbol, durationSeconds, rows)
	return nil
}

// EnsureTickSeries downloads tick data for symbol if not already present.
// Implements LazyLoader.
func (p *MemoryProvider) EnsureTickSeries(ctx context.Context, symbol string, start, end time.Time, market api.MarketService) error {
	p.mu.RLock()
	_, has := p.ticks[symbol]
	p.mu.RUnlock()
	if has {
		return nil
	}

	// Download from live market service.
	res, err := market.GetTickDataSeries(ctx, symbol, start, end)
	if err != nil {
		return err
	}
	rows := make([]api.Tick, 0, len(res.Data))
	for _, t := range res.Data {
		if t != nil {
			rows = append(rows, *t)
		}
	}
	p.SetTickSeries(symbol, rows)
	return nil
}

func filterTicks(in []api.Tick, startNS, endNS int64) []api.Tick {
	out := make([]api.Tick, 0, len(in))
	for _, r := range in {
		if r.Datetime < startNS || r.Datetime > endNS {
			continue
		}
		out = append(out, r)
	}
	return out
}

func filterKlines(in []api.Kline, startNS, endNS int64) []api.Kline {
	out := make([]api.Kline, 0, len(in))
	for _, r := range in {
		if r.Datetime < startNS || r.Datetime > endNS {
			continue
		}
		out = append(out, r)
	}
	return out
}
