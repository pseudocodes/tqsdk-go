package api

import (
	"context"
	"encoding/json"
	"time"
)

const (
	MarketMaxDataLength = 10000
	MarketMaxViewWidth  = 10000
)

type MarketService interface {
	MarketQueryService

	Start(ctx context.Context) error

	SubscribeQuotes(ctx context.Context, symbols ...string) (QuoteSub, error)
	SubscribeTicks(ctx context.Context, symbol string, dataLength int, opts ...TickSubOption) (TickSub, error)
	Quote(symbol string) (Quote, bool)
	TickFrame(symbol string) ([]*Tick, bool)

	GetTicks(ctx context.Context, req TickGetReq) (TickGetResult, error)
	QueryTicksPage(ctx context.Context, req TickQueryReq) (TickBatch, error)
	NewTickDownloader(ctx context.Context, req TickDownloadReq) (TickBatchIter, error)
	GetTickDataSeries(ctx context.Context, symbol string, startDT time.Time, endDT time.Time) (TickDataSeriesResult, error)

	SubscribeKlines(ctx context.Context, symbols []string, durationSeconds int, dataLength int, opts ...KlineSubOption) (KlineSub, error)
	GetKlines(ctx context.Context, req KlineGetReq) (KlineGetResult, error)
	QueryKlinesPage(ctx context.Context, req KlineQueryReq) (KlineBatch, error)
	NewKlineDownloader(ctx context.Context, req KlineDownloadReq) (KlineBatchIter, error)
	GetKlineDataSeries(ctx context.Context, symbol string, durationSeconds int, startDT time.Time, endDT time.Time) (KlineDataSeriesResult, error)

	ConnEvents(ctx context.Context) (<-chan ConnEvent, error)
}

type MarketQueryService interface {
	QueryGraphQL(ctx context.Context, query string, variables map[string]any) (GraphQLResult, error)
	QueryQuotes(ctx context.Context, insClass []string, exchangeID []string, productID []string, expired *bool, hasNight *bool) ([]InstrumentID, error)
	QueryContQuotes(ctx context.Context, exchangeID string, productID string, hasNight *bool) ([]InstrumentID, error)
	QueryOptions(ctx context.Context, underlyingSymbol string, optionClass string, exerciseYear int, exerciseMonth int, strikePrice *float64, expired *bool, hasA *bool) ([]InstrumentID, error)
	QueryATMOptions(ctx context.Context, underlyingSymbol string, underlyingPrice float64, priceLevel []int, optionClass string, exerciseYear int, exerciseMonth int, hasA *bool) ([]*InstrumentID, error)
	QuerySymbolInfo(ctx context.Context, symbols []string) ([]Quote, error)
	QueryAllLevelOptions(ctx context.Context, underlyingSymbol string, underlyingPrice float64, optionClass string, exerciseYear int, exerciseMonth int, hasA *bool) (inMoney []InstrumentID, atMoney []InstrumentID, outMoney []InstrumentID, err error)
	QueryAllLevelFinanceOptions(ctx context.Context, underlyingSymbol string, underlyingPrice float64, optionClass string, nearbys []int, hasA *bool) (inMoney []InstrumentID, atMoney []InstrumentID, outMoney []InstrumentID, err error)
	QueryHisContQuotes(ctx context.Context, symbols []string, n int) (HisContQuoteResult, error)
	QuerySymbolSettlement(ctx context.Context, symbols []string, days int, startDT *time.Time) (SettlementResult, error)
	QueryEDBData(ctx context.Context, ids []int, n int, align string, fill string) (EDBDataResult, error)
	QuerySymbolRanking(ctx context.Context, symbol string, rankingType RankingType, days int, startDT *time.Time, broker string) (SymbolRankingResult, error)
}

type QuoteSub interface {
	C() <-chan QuoteEvent
	Close() error
}

type TickSub interface {
	C() <-chan TickEvent
	IsSerialReady() bool
	ChartID() string
	Close() error
}

type KlineSub interface {
	C() <-chan KlineEvent
	IsSerialReady() bool
	ChartID() string
	Close() error
}

type KlineBegin struct {
	FocusDatetime *time.Time
	LeftKlineID   *int64
	FocusPosition *int
}

type TickGetReq struct {
	Symbol    string
	Count     int
	Start     *time.Time
	End       *time.Time
	Begin     KlineBegin
	ViewWidth int
}

type TickGetResult struct {
	Data     []*Tick
	Frame    []*Tick
	Complete bool
	Reason   string
}

type TickQueryReq struct {
	Symbol    string
	Begin     KlineBegin
	ViewWidth int
}

type TickBatch struct {
	Frame           []*Tick
	Ready           bool
	MoreData        bool
	LeftID          int64
	RightID         int64
	NextLeftKlineID *int64
	ChartID         string
}

type TickDataSeriesResult struct {
	Data     []*Tick
	Complete bool
	Reason   string
}

type TickDownloadReq struct {
	Symbol    string
	Start     time.Time
	End       time.Time
	ViewWidth int
}

type TickBatchIter interface {
	Next(ctx context.Context) (TickBatch, bool, error)
	Err() error
	Close() error
}

type KlineGetReq struct {
	Symbols         []string
	DurationSeconds int
	Count           int
	Start           *time.Time
	End             *time.Time
	Begin           KlineBegin
	ViewWidth       int
	AlignMode       KlineAlignMode
}

type KlineGetResult struct {
	Rows     []AlignedKlineRow
	Frame1D  []*Kline
	Frame2D  [][]*Kline
	Complete bool
	Reason   string
}

type KlineQueryReq struct {
	Symbols         []string
	DurationSeconds int
	Begin           KlineBegin
	ViewWidth       int
	AlignMode       KlineAlignMode
}

type KlineBatch struct {
	Key             KlineKey
	Rows            []AlignedKlineRow
	Frame1D         []*Kline
	Frame2D         [][]*Kline
	Ready           bool
	MoreData        bool
	LeftID          int64
	RightID         int64
	NextLeftKlineID *int64
	ChartID         string
}

type KlineDownloadReq struct {
	Symbols         []string
	DurationSeconds int
	Start           time.Time
	End             time.Time
	ViewWidth        int
	AlignMode       KlineAlignMode
}

type KlineBatchIter interface {
	Next(ctx context.Context) (KlineBatch, bool, error)
	Err() error
	Close() error
}

type KlineDataSeriesResult struct {
	Data     []*Kline
	Complete bool
	Reason   string
}

type QuerySource string

const (
	QuerySourceWSInsQuery QuerySource = "ws_ins_query"
	QuerySourceHTTP       QuerySource = "http"
)

type QueryMeta struct {
	Source  QuerySource   `json:"source"`
	QueryID string        `json:"query_id,omitempty"`
	Cost    time.Duration `json:"cost"`
}

type InstrumentID string

type GraphQLError struct {
	Message    string           `json:"message"`
	Locations  []map[string]int `json:"locations,omitempty"`
	Path       []any            `json:"path,omitempty"`
	Extensions map[string]any   `json:"extensions,omitempty"`
}

type GraphQLResult struct {
	Meta      QueryMeta       `json:"meta"`
	QueryID   string          `json:"query_id"`
	Query     string          `json:"query"`
	Variables map[string]any  `json:"variables"`
	Result    map[string]any  `json:"result,omitempty"`
	Errors    []GraphQLError  `json:"errors,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

type HisContQuoteResult struct {
	Meta    QueryMeta         `json:"meta"`
	Symbols []InstrumentID    `json:"symbols"`
	Rows    []HisContQuoteRow `json:"rows"`
}

type HisContQuoteRow struct {
	Date        time.Time `json:"date"`
	Underlyings []string  `json:"underlyings"`
}

type SettlementResult struct {
	Meta QueryMeta       `json:"meta"`
	Rows []SettlementRow `json:"rows"`
}

type SettlementRow struct {
	Datetime   string  `json:"datetime"`
	Symbol     string  `json:"symbol"`
	Settlement float64 `json:"settlement"`
}

type EDBValue struct {
	Valid bool    `json:"valid"`
	Value float64 `json:"value,omitempty"`
}

type EDBDataResult struct {
	Meta   QueryMeta    `json:"meta"`
	Dates  []string     `json:"dates"`
	IDs    []int        `json:"ids"`
	Values [][]EDBValue `json:"values"`
}

type RankingType string

const (
	RankingTypeVolume RankingType = "VOLUME"
	RankingTypeLong   RankingType = "LONG"
	RankingTypeShort  RankingType = "SHORT"
)

type SymbolRankingResult struct {
	Meta        QueryMeta          `json:"meta"`
	RankingType RankingType        `json:"ranking_type"`
	Rows        []SymbolRankingRow `json:"rows"`
}

type SymbolRankingRow struct {
	Datetime      string   `json:"datetime"`
	Symbol        string   `json:"symbol"`
	ExchangeID    string   `json:"exchange_id"`
	InstrumentID  string   `json:"instrument_id"`
	Broker        string   `json:"broker"`
	Volume        *float64 `json:"volume,omitempty"`
	VolumeChange  *float64 `json:"volume_change,omitempty"`
	VolumeRanking *float64 `json:"volume_ranking,omitempty"`
	LongOI        *float64 `json:"long_oi,omitempty"`
	LongChange    *float64 `json:"long_change,omitempty"`
	LongRanking   *float64 `json:"long_ranking,omitempty"`
	ShortOI       *float64 `json:"short_oi,omitempty"`
	ShortChange   *float64 `json:"short_change,omitempty"`
	ShortRanking  *float64 `json:"short_ranking,omitempty"`
}
