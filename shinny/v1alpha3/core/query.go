package core

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// queryCall 是一次性请求的唯一终态：成功、错误、取消、关闭先到先得。
// 结果在整包安装后写入，随后 close(done)；读取方在 done 之后读取。
type queryCall struct {
	id       string
	values   map[string]jsontext.Value // symbols.<id> 下的 result / error / errors
	convert  func(id string, result jsontext.Value) (any, error)
	finished bool
	value    any
	err      error
	done     chan struct{}
}

func newCall(prefix string) *queryCall {
	return &queryCall{id: fmt.Sprintf("TQGO_%s_%016x%016x", prefix, rand.Uint64(), rand.Uint64()), done: make(chan struct{})}
}

// finish 在 rt.mu 内调用；只有第一次生效。
func (c *queryCall) finish(v any, err error) {
	if c.finished {
		return
	}
	c.value, c.err, c.finished = v, err, true
	close(c.done)
}

// QueryError 是合约服务返回的错误。
type QueryError struct {
	QueryID string
	Detail  string // 服务端 error/errors 原文
}

func (e *QueryError) Error() string { return "tqsdk: query " + e.QueryID + " failed: " + e.Detail }

// completeQuery 按回包判断完成：error/errors 表示失败，result 表示成功；仅有回显不算完成。
func (m *Market) completeQuery(c *queryCall) bool {
	if c.finished {
		return false
	}
	var v any
	var err error
	switch {
	case c.values["error"] != nil:
		err = &QueryError{c.id, string(c.values["error"])}
	case c.values["errors"] != nil:
		err = &QueryError{c.id, string(c.values["errors"])}
	case c.values["result"] != nil:
		v, err = c.convert(c.id, c.values["result"])
	default:
		return false
	}
	c.finish(v, err)
	delete(m.queries, c.id)
	m.route.send(releaseQuery(c.id), nil) // 同 Python TqSymbols：完成后释放服务端请求
	return true
}

type insQuery struct {
	Aid       string         `json:"aid"`
	QueryID   string         `json:"query_id"`
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

func releaseQuery(id string) []byte {
	b, _ := json.Marshal(insQuery{Aid: "ins_query", QueryID: id})
	return b
}

// startQuery 先登记等待者再发送；发送失败撤销登记。返回前应答可能已被安装，
// 因此 convert 只能通过参数取得请求身份，不能引用返回值。
func (m *Market) startQuery(prefix, query string, vars map[string]any, convert func(id string, result jsontext.Value) (any, error)) (*queryCall, error) {
	c := newCall(prefix)
	c.convert = convert
	data, err := json.Marshal(insQuery{Aid: "ins_query", QueryID: c.id, Query: query, Variables: vars})
	if err != nil {
		return nil, invalid("encode query: %v", err)
	}
	m.rt.mu.Lock()
	defer m.rt.mu.Unlock()
	if err := m.usable(); err != nil {
		return nil, err
	}
	if err := m.route.send(data, nil); err != nil {
		return nil, err
	}
	m.queries[c.id] = c
	return c, nil
}

// Request 是一次性请求的票据；手动模式下由 WaitUpdate 推进，自动模式可 Wait。
type Request[R any] struct {
	m *Market
	c *queryCall
}

// ID 返回本地请求身份（query_id 或 chart_id）。
func (r *Request[R]) ID() string { return r.c.id }

// Done 报告请求是否已终结。
func (r *Request[R]) Done() bool {
	select {
	case <-r.c.done:
		return true
	default:
		return false
	}
}

// DoneC 返回终结时关闭的 channel。
func (r *Request[R]) DoneC() <-chan struct{} { return r.c.done }

// Result 返回结果；未终结时返回 ErrNotReady。
func (r *Request[R]) Result() (R, error) {
	var zero R
	select {
	case <-r.c.done:
	default:
		return zero, ErrNotReady
	}
	if r.c.err != nil {
		return zero, r.c.err
	}
	v, _ := r.c.value.(R)
	return v, nil
}

// Wait 等待请求终结。仅适用于由 Run 在后台推进的 Runtime；ctx 结束只终结本地等待。
func (r *Request[R]) Wait(ctx context.Context) (R, error) {
	select {
	case <-r.c.done:
		return r.Result()
	case <-ctx.Done():
		r.Close()
		var zero R
		return zero, ctx.Err()
	}
}

// Close 终结本地等待并释放服务端请求；已完成时无操作。
func (r *Request[R]) Close() error {
	m := r.m
	m.rt.mu.Lock()
	defer m.rt.mu.Unlock()
	if r.c.finished {
		return nil
	}
	r.c.finish(nil, ErrClosed)
	if _, ok := m.queries[r.c.id]; ok {
		delete(m.queries, r.c.id)
		m.route.send(releaseQuery(r.c.id), nil)
	}
	for _, w := range m.windows {
		if p, ok := w.(interface{ pageCall() *queryCall }); ok && p.pageCall() == r.c {
			w.closeLocked()
		}
	}
	return nil
}

func (w *seriesWindow[T, F]) pageCall() *queryCall { return w.page }

// ---- 合约服务查询 ----

// QuoteFilter 是合约列表查询条件（对应 Python query_quotes）；nil/空表示不限制。
type QuoteFilter struct {
	InsClass   []string // FUTURE / OPTION / COMBINE / INDEX / CONT / STOCK / FUND ...
	ExchangeID []string
	ProductID  []string
	Expired    *bool
	HasNight   *bool
}

// OptionQuery 是期权查询条件（对应 Python query_options 的常用参数）。
type OptionQuery struct {
	Underlying    string
	OptionClass   string // CALL / PUT，空表示不限
	ExerciseYear  int
	ExerciseMonth int
	StrikePrice   *float64
	Expired       *bool
}

// GraphQLResult 是原始 GraphQL 查询结果（result 对象的 JSON 副本）。
type GraphQLResult struct {
	QueryID string
	Result  []byte
}

var enumPattern = regexp.MustCompile(`^[A-Z_]+$`)

var futureExchanges = map[string]bool{"CFFEX": true, "SHFE": true, "DCE": true, "CZCE": true, "INE": true, "GFEX": true}

func gqlStrings(v []string) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// RequestQuotes 查询合约代码列表。
func (m *Market) RequestQuotes(ctx context.Context, f QuoteFilter) (*Request[[]string], error) {
	var args []string
	if len(f.InsClass) > 0 {
		for _, c := range f.InsClass {
			if !enumPattern.MatchString(c) {
				return nil, invalid("bad ins class %q", c)
			}
		}
		args = append(args, "class: ["+strings.Join(f.InsClass, ", ")+"]")
	}
	// 主连和指数请求全部交易所，在本地按交易所过滤（同 Python）。
	localExchange := ""
	if len(f.ExchangeID) > 0 {
		contOrIndex := len(f.InsClass) > 0 && allIn(f.InsClass, "INDEX", "CONT")
		if contOrIndex && len(f.ExchangeID) == 1 && futureExchanges[f.ExchangeID[0]] {
			localExchange = f.ExchangeID[0]
		} else {
			args = append(args, "exchange_id: "+gqlStrings(f.ExchangeID))
		}
	}
	if len(f.ProductID) > 0 {
		args = append(args, "product_id: "+gqlStrings(f.ProductID))
	}
	if f.Expired != nil {
		args = append(args, fmt.Sprintf("expired: %t", *f.Expired))
	}
	if f.HasNight != nil {
		args = append(args, fmt.Sprintf("has_night: %t", *f.HasNight))
	}
	q := "query { multi_symbol_info"
	if len(args) > 0 {
		q += "(" + strings.Join(args, ", ") + ")"
	}
	q += " { ... on basic { instrument_id } } }"
	c, err := m.startQuery("quotes", q, nil, func(_ string, raw jsontext.Value) (any, error) {
		var res struct {
			List []struct {
				InstrumentID string `json:"instrument_id"`
			} `json:"multi_symbol_info"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, err
		}
		out := make([]string, 0, len(res.List))
		for _, s := range res.List {
			if localExchange == "" || strings.Contains(s.InstrumentID, localExchange) {
				out = append(out, s.InstrumentID)
			}
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return &Request[[]string]{m, c}, nil
}

func allIn(list []string, allowed ...string) bool {
	for _, v := range list {
		ok := false
		for _, a := range allowed {
			ok = ok || v == a
		}
		if !ok {
			return false
		}
	}
	return true
}

// symbolInfoQuery 是 Python _query_for_quote 生成的合约资料查询。
const symbolInfoQuery = `query rootQuery($instrument_id: [String]) {
  multi_symbol_info(instrument_id: $instrument_id) {
    __typename ...basic ...stock ...fund ...bond ...tradeable ...index ...securities ...future ...option ...combine ...derivative
  }
}
fragment basic on basic { instrument_id exchange_id instrument_name english_name class price_tick price_decs trading_day trading_time { day night } }
fragment stock on stock { stock_dividend_ratio cash_dividend_ratio }
fragment fund on fund { cash_dividend_ratio }
fragment bond on bond { maturity_datetime }
fragment tradeable on tradeable { pre_close volume_multiple quote_multiple upper_limit lower_limit }
fragment index on index { index_multiple }
fragment securities on securities { currency face_value first_trading_datetime buy_volume_unit sell_volume_unit status public_float_share_quantity }
fragment future on future { pre_open_interest expired product_id product_short_name delivery_year delivery_month expire_datetime settlement_price max_market_order_volume max_limit_order_volume min_market_order_volume min_limit_order_volume open_limit open_max_market_order_volume open_max_limit_order_volume open_min_market_order_volume open_min_limit_order_volume margin commission mmsa categories { id name } position_limit }
fragment option on option { pre_open_interest expired product_short_name expire_datetime last_exercise_datetime settlement_price max_market_order_volume max_limit_order_volume min_market_order_volume min_limit_order_volume open_max_market_order_volume open_max_limit_order_volume open_min_market_order_volume open_min_limit_order_volume strike_price call_or_put exercise_type position_limit }
fragment combine on combine { expired product_id expire_datetime max_market_order_volume max_limit_order_volume min_market_order_volume min_limit_order_volume open_max_market_order_volume open_max_limit_order_volume open_min_market_order_volume open_min_limit_order_volume leg1 { __typename ... on basic { instrument_id } } leg2 { __typename ... on basic { instrument_id } } }
fragment derivative on derivative { underlying { count edges { underlying_multiple node { __typename ...basic ...stock ...fund ...bond ...tradeable ...index ...securities ...future } } } }`

// RequestSymbolInfo 查询合约资料；查询结果中缺失的合约不伪造记录。
func (m *Market) RequestSymbolInfo(ctx context.Context, symbols []string) (*Request[[]model.Instrument], error) {
	if len(symbols) == 0 {
		return nil, invalid("no symbols")
	}
	for _, s := range symbols {
		if err := validateSymbol(s); err != nil {
			return nil, err
		}
	}
	want := append([]string(nil), symbols...)
	c, err := m.startQuery("quote", symbolInfoQuery, map[string]any{"instrument_id": want}, func(_ string, raw jsontext.Value) (any, error) {
		byID, err := parseInstruments(raw)
		if err != nil {
			return nil, err
		}
		out := make([]model.Instrument, 0, len(want))
		for _, s := range want {
			if ins, ok := byID[s]; ok {
				out = append(out, ins)
			}
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return &Request[[]model.Instrument]{m, c}, nil
}

// RequestOptions 查询标的的期权代码并按条件过滤（对应 Python query_options）。
func (m *Market) RequestOptions(ctx context.Context, q OptionQuery) (*Request[[]string], error) {
	if err := validateSymbol(q.Underlying); err != nil {
		return nil, err
	}
	if q.OptionClass != "" && q.OptionClass != "CALL" && q.OptionClass != "PUT" {
		return nil, invalid("option class %q", q.OptionClass)
	}
	query := "query { multi_symbol_info(instrument_id: " + gqlStrings([]string{q.Underlying}) + ") { ... on basic { instrument_id " +
		"derivatives(class: [OPTION]) { edges { node { ... on basic { class instrument_id exchange_id english_name } " +
		"... on option { expired expire_datetime last_exercise_datetime strike_price call_or_put } } } } } } }"
	c, err := m.startQuery("options", query, nil, func(_ string, raw jsontext.Value) (any, error) {
		var res struct {
			List []struct {
				Derivatives struct {
					Edges []struct {
						Node struct {
							InstrumentID         string  `json:"instrument_id"`
							Expired              bool    `json:"expired"`
							LastExerciseDatetime int64   `json:"last_exercise_datetime"`
							StrikePrice          float64 `json:"strike_price"`
							CallOrPut            string  `json:"call_or_put"`
						} `json:"node"`
					} `json:"edges"`
				} `json:"derivatives"`
			} `json:"multi_symbol_info"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, err
		}
		var out []string
		for _, s := range res.List {
			for _, e := range s.Derivatives.Edges {
				o := e.Node
				t := time.Unix(0, o.LastExerciseDatetime).In(cst)
				if (q.OptionClass == "" || o.CallOrPut == q.OptionClass) &&
					(q.StrikePrice == nil || o.StrikePrice == *q.StrikePrice) &&
					(q.Expired == nil || o.Expired == *q.Expired) &&
					(q.ExerciseYear == 0 || t.Year() == q.ExerciseYear) &&
					(q.ExerciseMonth == 0 || int(t.Month()) == q.ExerciseMonth) {
					out = append(out, o.InstrumentID)
				}
			}
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return &Request[[]string]{m, c}, nil
}

// RequestGraphQL 发送任意合约服务 GraphQL 查询。
func (m *Market) RequestGraphQL(ctx context.Context, query string, variables map[string]any) (*Request[GraphQLResult], error) {
	if strings.TrimSpace(query) == "" {
		return nil, invalid("empty query")
	}
	c, err := m.startQuery("api", query, variables, func(id string, raw jsontext.Value) (any, error) {
		return GraphQLResult{QueryID: id, Result: append([]byte(nil), raw...)}, nil
	})
	if err != nil {
		return nil, err
	}
	return &Request[GraphQLResult]{m, c}, nil
}

// RequestKlinesPage 请求一次性 K 线历史页：请求回显确认且 chart.ready 后完成，随后撤销 chart。
func (m *Market) RequestKlinesPage(ctx context.Context, r KlineRequest) (*Request[KlineFrame], error) {
	req, err := klineReq(r)
	if err != nil {
		return nil, err
	}
	c := newCall("page")
	if _, err := openWindow(m, klineKind, false, req, true, c); err != nil {
		return nil, err
	}
	return &Request[KlineFrame]{m, c}, nil
}

// RequestTicksPage 请求一次性 Tick 历史页。
func (m *Market) RequestTicksPage(ctx context.Context, r TickRequest) (*Request[TickFrame], error) {
	req, err := tickReq(r)
	if err != nil {
		return nil, err
	}
	c := newCall("page")
	if _, err := openWindow(m, tickKind, true, req, true, c); err != nil {
		return nil, err
	}
	return &Request[TickFrame]{m, c}, nil
}

var cst = time.FixedZone("CST", 8*3600)

// parseInstruments 把 multi_symbol_info 结果转换为合约资料（对应 Python _symbols_to_quotes）。
func parseInstruments(raw jsontext.Value) (map[string]model.Instrument, error) {
	var res struct {
		List []gqlSymbol `json:"multi_symbol_info"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	out := map[string]model.Instrument{}
	for i := range res.List {
		s := &res.List[i]
		ins := s.instrument()
		for _, e := range s.Underlying.Edges {
			u := e.Node.instrument()
			if u.Symbol != "" {
				ins.UnderlyingSymbol = u.Symbol
				ins.Valid.Add(model.InstrumentUnderlyingSymbol)
				if _, ok := out[u.Symbol]; !ok {
					out[u.Symbol] = u
				}
				if ins.InsClass == "OPTION" { // 商品期权的交割年月取标的（同 Python）
					switch ins.ExchangeID {
					case "DCE", "CZCE", "SHFE", "GFEX":
						ins.DeliveryYear, ins.DeliveryMonth = u.DeliveryYear, u.DeliveryMonth
						ins.Valid.Add(model.InstrumentDeliveryYear)
						ins.Valid.Add(model.InstrumentDeliveryMonth)
					case "CFFEX":
						if s.LastExerciseDatetime != nil {
							t := time.Unix(0, *s.LastExerciseDatetime).In(cst)
							ins.DeliveryYear, ins.DeliveryMonth = int64(t.Year()), int64(t.Month())
							ins.Valid.Add(model.InstrumentDeliveryYear)
							ins.Valid.Add(model.InstrumentDeliveryMonth)
						}
					}
				}
			}
		}
		out[ins.Symbol] = ins
	}
	for sym, ins := range out { // 组合合约的合约乘数取第一腿
		if ins.InsClass == "COMBINE" && !ins.Valid.Has(model.InstrumentVolumeMultiple) {
			if leg, ok := out[ins.Leg1Symbol]; ok && leg.Valid.Has(model.InstrumentVolumeMultiple) {
				ins.VolumeMultiple = leg.VolumeMultiple
				ins.Valid.Add(model.InstrumentVolumeMultiple)
				out[sym] = ins
			}
		}
	}
	return out, nil
}

// gqlSymbol 是合约服务返回的一个节点；指针字段表达是否出现。
type gqlSymbol struct {
	InstrumentID             *string  `json:"instrument_id"`
	ExchangeID               *string  `json:"exchange_id"`
	InstrumentName           *string  `json:"instrument_name"`
	Class                    *string  `json:"class"`
	PriceTick                *float64 `json:"price_tick"`
	PriceDecs                *int64   `json:"price_decs"`
	VolumeMultiple           *float64 `json:"volume_multiple"`
	IndexMultiple            *float64 `json:"index_multiple"`
	MaxLimitOrderVolume      *int64   `json:"max_limit_order_volume"`
	MaxMarketOrderVolume     *int64   `json:"max_market_order_volume"`
	MinLimitOrderVolume      *int64   `json:"min_limit_order_volume"`
	MinMarketOrderVolume     *int64   `json:"min_market_order_volume"`
	OpenLimit                *int64   `json:"open_limit"`
	OpenMaxMarketOrderVolume *int64   `json:"open_max_market_order_volume"`
	OpenMaxLimitOrderVolume  *int64   `json:"open_max_limit_order_volume"`
	OpenMinMarketOrderVolume *int64   `json:"open_min_market_order_volume"`
	OpenMinLimitOrderVolume  *int64   `json:"open_min_limit_order_volume"`
	StrikePrice              *float64 `json:"strike_price"`
	Expired                  *bool    `json:"expired"`
	ExpireDatetime           *int64   `json:"expire_datetime"`
	DeliveryYear             *int64   `json:"delivery_year"`
	DeliveryMonth            *int64   `json:"delivery_month"`
	LastExerciseDatetime     *int64   `json:"last_exercise_datetime"`
	CallOrPut                *string  `json:"call_or_put"`
	ExerciseType             *string  `json:"exercise_type"`
	ProductID                *string  `json:"product_id"`
	PublicFloatShareQuantity *int64   `json:"public_float_share_quantity"`
	PositionLimit            *int64   `json:"position_limit"`
	Margin                   *float64 `json:"margin"`
	Commission               *float64 `json:"commission"`
	SettlementPrice          *float64 `json:"settlement_price"`
	PreOpenInterest          *int64   `json:"pre_open_interest"`
	PreClose                 *float64 `json:"pre_close"`
	UpperLimit               *float64 `json:"upper_limit"`
	LowerLimit               *float64 `json:"lower_limit"`
	StockDividendRatio       []string `json:"stock_dividend_ratio"`
	CashDividendRatio        []string `json:"cash_dividend_ratio"`
	TradingTime              *struct {
		Day   [][]string `json:"day"`
		Night [][]string `json:"night"`
	} `json:"trading_time"`
	Leg1 *struct {
		InstrumentID string `json:"instrument_id"`
	} `json:"leg1"`
	Leg2 *struct {
		InstrumentID string `json:"instrument_id"`
	} `json:"leg2"`
	Underlying struct {
		Edges []struct {
			Node gqlSymbol `json:"node"`
		} `json:"edges"`
	} `json:"underlying"`
}

func (s *gqlSymbol) instrument() model.Instrument {
	var ins model.Instrument
	instrumentSchema.reset(&ins)
	v := &ins.Valid
	str := func(p *string, dst *string, f model.InstrumentField) {
		if p != nil {
			*dst = *p
			v.Add(f)
		}
	}
	i64 := func(p *int64, dst *int64, f model.InstrumentField) {
		if p != nil {
			*dst = *p
			v.Add(f)
		}
	}
	f64 := func(p *float64, dst *float64, f model.InstrumentField) {
		if p != nil {
			*dst = *p
			v.Add(f)
		}
	}
	str(s.InstrumentID, &ins.InstrumentID, model.InstrumentInstrumentID)
	ins.Symbol = ins.InstrumentID
	str(s.ExchangeID, &ins.ExchangeID, model.InstrumentExchangeID)
	str(s.InstrumentName, &ins.Name, model.InstrumentName)
	str(s.Class, &ins.InsClass, model.InstrumentInsClass)
	f64(s.PriceTick, &ins.PriceTick, model.InstrumentPriceTick)
	i64(s.PriceDecs, &ins.PriceDecs, model.InstrumentPriceDecs)
	if p := s.VolumeMultiple; p != nil {
		ins.VolumeMultiple = int64(*p)
		v.Add(model.InstrumentVolumeMultiple)
	}
	if p := s.IndexMultiple; p != nil {
		ins.VolumeMultiple = int64(*p)
		v.Add(model.InstrumentVolumeMultiple)
	}
	i64(s.MaxLimitOrderVolume, &ins.MaxLimitOrderVolume, model.InstrumentMaxLimitOrderVolume)
	i64(s.MaxMarketOrderVolume, &ins.MaxMarketOrderVolume, model.InstrumentMaxMarketOrderVolume)
	i64(s.MinLimitOrderVolume, &ins.MinLimitOrderVolume, model.InstrumentMinLimitOrderVolume)
	i64(s.MinMarketOrderVolume, &ins.MinMarketOrderVolume, model.InstrumentMinMarketOrderVolume)
	i64(s.OpenLimit, &ins.OpenLimit, model.InstrumentOpenLimit)
	i64(s.OpenMaxMarketOrderVolume, &ins.OpenMaxMarketOrderVolume, model.InstrumentOpenMaxMarketOrderVolume)
	i64(s.OpenMaxLimitOrderVolume, &ins.OpenMaxLimitOrderVolume, model.InstrumentOpenMaxLimitOrderVolume)
	i64(s.OpenMinMarketOrderVolume, &ins.OpenMinMarketOrderVolume, model.InstrumentOpenMinMarketOrderVolume)
	i64(s.OpenMinLimitOrderVolume, &ins.OpenMinLimitOrderVolume, model.InstrumentOpenMinLimitOrderVolume)
	f64(s.StrikePrice, &ins.StrikePrice, model.InstrumentStrikePrice)
	if s.Expired != nil {
		ins.Expired = *s.Expired
		v.Add(model.InstrumentExpired)
	}
	if p := s.ExpireDatetime; p != nil && *p != 0 {
		ins.ExpireDatetime = float64(*p) / 1e9
		v.Add(model.InstrumentExpireDatetime)
	}
	i64(s.DeliveryYear, &ins.DeliveryYear, model.InstrumentDeliveryYear)
	i64(s.DeliveryMonth, &ins.DeliveryMonth, model.InstrumentDeliveryMonth)
	if p := s.LastExerciseDatetime; p != nil && *p != 0 {
		ins.LastExerciseDatetime = float64(*p) / 1e9
		t := time.Unix(0, *p).In(cst)
		ins.ExerciseYear, ins.ExerciseMonth = int64(t.Year()), int64(t.Month())
		v.Add(model.InstrumentLastExerciseDatetime)
		v.Add(model.InstrumentExerciseYear)
		v.Add(model.InstrumentExerciseMonth)
	}
	str(s.CallOrPut, &ins.OptionClass, model.InstrumentOptionClass)
	str(s.ExerciseType, &ins.ExerciseType, model.InstrumentExerciseType)
	str(s.ProductID, &ins.ProductID, model.InstrumentProductID)
	i64(s.PublicFloatShareQuantity, &ins.PublicFloatShareQuantity, model.InstrumentPublicFloatShareQuantity)
	i64(s.PositionLimit, &ins.PositionLimit, model.InstrumentPositionLimit)
	f64(s.Margin, &ins.Margin, model.InstrumentMargin)
	f64(s.Commission, &ins.Commission, model.InstrumentCommission)
	f64(s.SettlementPrice, &ins.PreSettlement, model.InstrumentPreSettlement)
	i64(s.PreOpenInterest, &ins.PreOpenInterest, model.InstrumentPreOpenInterest)
	f64(s.PreClose, &ins.PreClose, model.InstrumentPreClose)
	f64(s.UpperLimit, &ins.UpperLimit, model.InstrumentUpperLimit)
	f64(s.LowerLimit, &ins.LowerLimit, model.InstrumentLowerLimit)
	if s.Leg1 != nil {
		ins.Leg1Symbol = s.Leg1.InstrumentID
		v.Add(model.InstrumentLeg1Symbol)
	}
	if s.Leg2 != nil {
		ins.Leg2Symbol = s.Leg2.InstrumentID
		v.Add(model.InstrumentLeg2Symbol)
	}
	if s.TradingTime != nil {
		ins.TradingTime = model.TradingTime{Day: s.TradingTime.Day, Night: s.TradingTime.Night}
	}
	ins.StockDividendRatio, ins.CashDividendRatio = s.StockDividendRatio, s.CashDividendRatio
	return ins
}

var instrumentSchema = newSchema[model.Instrument](model.InstrumentFieldCount, model.InstrumentField.String)
