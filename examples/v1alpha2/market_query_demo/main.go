package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/app"
)

func main() {
	var (
		symbols           = flag.String("symbols", "SHFE.rb2605,DCE.i2605", "symbols for QuerySymbolInfo")
		optionUnderlying  = flag.String("option-underlying", "SHFE.rb2605", "underlying symbol for option queries")
		optionPrice       = flag.Float64("option-price", 3500, "reference underlying price for ATM/all-level option queries")
		financeUnderlying = flag.String("finance-underlying", "SSE.510300", "finance option underlying symbol")
		financePrice      = flag.Float64("finance-price", 4.0, "reference underlying price for finance all-level option query")
		contSymbols       = flag.String("cont-symbols", "KQ.m@SHFE.rb,KQ.m@DCE.i", "symbols for QueryHisContQuotes")
		settleSymbols     = flag.String("settlement-symbols", "INE.nr2408", "symbols for QuerySymbolSettlement (aligned with api.py example)")
		settlementDays    = flag.Int("settlement-days", 3, "days for QuerySymbolSettlement (aligned with api.py example)")
		settlementStart   = flag.String("settlement-start", "", "optional start date for QuerySymbolSettlement, format YYYYMMDD")
		edbIDsCSV         = flag.String("edb-ids", "472,497", "ids for QueryEDBData, csv")
		rankingSymbol     = flag.String("ranking-symbol", "SHFE.cu2109", "symbol for QuerySymbolRanking (aligned with api.py example)")
		rankingDays       = flag.Int("ranking-days", 1, "days for QuerySymbolRanking")
		rankingStart      = flag.String("ranking-start", "", "optional start date for QuerySymbolRanking, format YYYYMMDD")
	)
	flag.Parse()

	user := strings.TrimSpace(os.Getenv("SHINNYTECH_ID"))
	password := strings.TrimSpace(os.Getenv("SHINNYTECH_PW"))
	if user == "" || password == "" {
		log.Fatal("please set SHINNYTECH_ID and SHINNYTECH_PW environment variables")
	}

	cli, err := app.NewDefaultClient(app.Config{
		User:     user,
		Password: password,
	})
	if err != nil {
		panic(err)
	}
	defer func() { _ = cli.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := cli.Run(ctx); err != nil {
		panic(err)
	}

	m := cli.Market()
	syms := splitCSV(*symbols)
	if len(syms) == 0 {
		syms = []string{"SHFE.rb2605", "DCE.i2605"}
	}
	contSyms := splitCSV(*contSymbols)
	if len(contSyms) == 0 {
		contSyms = []string{"KQ.m@SHFE.rb"}
	}
	settleSyms := splitCSV(*settleSymbols)
	if len(settleSyms) == 0 {
		settleSyms = []string{"INE.nr2408"}
	}
	edbIDs := parseCSVInts(*edbIDsCSV)
	if len(edbIDs) == 0 {
		edbIDs = []int{472, 497}
	}
	if *settlementDays <= 0 {
		*settlementDays = 3
	}
	if *rankingDays <= 0 {
		*rankingDays = 1
	}
	settleStartDT, err := parseYYYYMMDDPtr(*settlementStart)
	if err != nil {
		panic(fmt.Errorf("invalid -settlement-start: %w", err))
	}
	rankingStartDT, err := parseYYYYMMDDPtr(*rankingStart)
	if err != nil {
		panic(fmt.Errorf("invalid -ranking-start: %w", err))
	}

	fmt.Println("== Query Demo Start ==")
	fmt.Printf("symbols=%v\n", syms)

	// Example 1: QueryGraphQL
	gql := `query {
  multi_symbol_info(class: [FUTURE], product_id: ["rb"], expired: false) {
    __typename
    ... on basic { instrument_id exchange_id class }
    ... on future { product_id expired }
  }
}`
	runQuery("QueryGraphQL", 15*time.Second, func(ctx context.Context) error {
		gqlRes, err := m.QueryGraphQL(ctx, gql, nil)
		if err != nil {
			return err
		}
		n := 0
		if arr, ok := gqlRes.Result["multi_symbol_info"].([]any); ok {
			n = len(arr)
		}
		fmt.Printf("  source=%s keys=%v multi_symbol_info_count=%d errors=%d raw_len=%d\n",
			gqlRes.Meta.Source, mapKeys(gqlRes.Result), n, len(gqlRes.Errors), len(gqlRes.Raw))
		if len(gqlRes.Errors) > 0 {
			fmt.Printf("  first_error=%s\n", gqlRes.Errors[0].Message)
		}
		return nil
	})

	// Example 2: QuerySymbolInfo
	runQuery("QuerySymbolInfo", 15*time.Second, func(ctx context.Context) error {
		info, err := m.QuerySymbolInfo(ctx, syms)
		if err != nil {
			return err
		}
		fmt.Printf("  count=%d\n", len(info))
		for i, q := range info {
			if i >= 5 {
				break
			}
			fmt.Printf("  #%d symbol=%s class=%s exchange=%s product=%s expired=%v strike=%.3f\n",
				i+1, q.InstrumentID, q.InsClass, q.ExchangeID, q.ProductID, q.Expired, q.StrikePrice)
		}
		return nil
	})

	// Example 3: QueryQuotes
	runQuery("QueryQuotes", 15*time.Second, func(ctx context.Context) error {
		expired := false
		ids, err := m.QueryQuotes(ctx, []string{"FUTURE"}, nil, []string{"rb"}, &expired, nil)
		if err != nil {
			return err
		}
		fmt.Printf("  count=%d sample=%v\n", len(ids), headIDs(ids, 8))
		return nil
	})

	// Example 4: QueryContQuotes
	runQuery("QueryContQuotes", 15*time.Second, func(ctx context.Context) error {
		cont, err := m.QueryContQuotes(ctx, "SHFE", "rb", nil)
		if err != nil {
			return err
		}
		fmt.Printf("  count=%d sample=%v\n", len(cont), headIDs(cont, 8))
		return nil
	})

	// Example 5: QueryOptions
	runQuery("QueryOptions", 15*time.Second, func(ctx context.Context) error {
		expired := false
		opts, err := m.QueryOptions(ctx, *optionUnderlying, "CALL", 0, 0, nil, &expired, nil)
		if err != nil {
			return err
		}
		fmt.Printf("  underlying=%s count=%d sample=%v\n", *optionUnderlying, len(opts), headIDs(opts, 8))
		return nil
	})

	// Example 6: QueryATMOptions
	runQuery("QueryATMOptions", 15*time.Second, func(ctx context.Context) error {
		opts, err := m.QueryATMOptions(ctx, *optionUnderlying, *optionPrice, []int{1, 0, -1}, "CALL", 0, 0, nil)
		if err != nil {
			return err
		}
		fmt.Printf("  underlying=%s price=%.4f levels=[1 0 -1] result=%v\n", *optionUnderlying, *optionPrice, headIDPtrs(opts, 8))
		return nil
	})

	// Example 7: QueryAllLevelOptions
	runQuery("QueryAllLevelOptions", 15*time.Second, func(ctx context.Context) error {
		inMoney, atMoney, outMoney, err := m.QueryAllLevelOptions(ctx, *optionUnderlying, *optionPrice, "CALL", 0, 0, nil)
		if err != nil {
			return err
		}
		fmt.Printf("  underlying=%s in=%d at=%d out=%d\n", *optionUnderlying, len(inMoney), len(atMoney), len(outMoney))
		fmt.Printf("  in_sample=%v at=%v out_sample=%v\n", headIDs(inMoney, 5), atMoney, headIDs(outMoney, 5))
		return nil
	})

	// Example 8: QueryAllLevelFinanceOptions
	runQuery("QueryAllLevelFinanceOptions", 15*time.Second, func(ctx context.Context) error {
		inMoney, atMoney, outMoney, err := m.QueryAllLevelFinanceOptions(ctx, *financeUnderlying, *financePrice, "CALL", []int{0, 1}, nil)
		if err != nil {
			return err
		}
		fmt.Printf("  underlying=%s nearbys=[0 1] in=%d at=%d out=%d\n", *financeUnderlying, len(inMoney), len(atMoney), len(outMoney))
		fmt.Printf("  in_sample=%v at=%v out_sample=%v\n", headIDs(inMoney, 5), atMoney, headIDs(outMoney, 5))
		return nil
	})

	// Example 9: QueryHisContQuotes (HTTP)
	runQuery("QueryHisContQuotes", 20*time.Second, func(ctx context.Context) error {
		his, err := m.QueryHisContQuotes(ctx, contSyms, 5)
		if err != nil {
			return err
		}
		fmt.Printf("  symbols=%v rows=%d\n", his.Symbols, len(his.Rows))
		if len(his.Rows) > 0 {
			r := his.Rows[len(his.Rows)-1]
			fmt.Printf("  latest date=%s underlyings=%v\n", r.Date.Format("2006-01-02"), r.Underlyings)
		}
		return nil
	})

	// Example 10: QuerySymbolSettlement (HTTP)
	runQuery("QuerySymbolSettlement", 20*time.Second, func(ctx context.Context) error {
		ret, err := m.QuerySymbolSettlement(ctx, settleSyms, *settlementDays, settleStartDT)
		if err != nil {
			return err
		}
		fmt.Printf("  rows=%d symbols=%v days=%d start=%s\n", len(ret.Rows), settleSyms, *settlementDays, fmtDatePtr(settleStartDT))
		for i, r := range ret.Rows {
			if i >= 3 {
				break
			}
			fmt.Printf("  #%d datetime=%s symbol=%s settlement=%.4f\n", i+1, r.Datetime, r.Symbol, r.Settlement)
		}
		return nil
	})

	// Example 11: QueryEDBData (HTTP)
	runQuery("QueryEDBData", 20*time.Second, func(ctx context.Context) error {
		ret, err := m.QueryEDBData(ctx, edbIDs, 10, "day", "ffill")
		if err != nil {
			return err
		}
		fmt.Printf("  ids=%v dates=%d\n", ret.IDs, len(ret.Dates))
		if len(ret.Dates) > 0 && len(ret.Values) > 0 {
			lastI := len(ret.Dates) - 1
			fmt.Printf("  latest date=%s valid_values=%d\n", ret.Dates[lastI], countValidEDB(ret.Values[lastI]))
		}
		return nil
	})

	// Example 12: QuerySymbolRanking (HTTP)
	runQuery("QuerySymbolRanking", 20*time.Second, func(ctx context.Context) error {
		ret, err := m.QuerySymbolRanking(ctx, *rankingSymbol, api.RankingTypeVolume, *rankingDays, rankingStartDT, "")
		if err != nil {
			return err
		}
		fmt.Printf("  ranking_type=%s rows=%d symbol=%s days=%d start=%s\n", ret.RankingType, len(ret.Rows), *rankingSymbol, *rankingDays, fmtDatePtr(rankingStartDT))
		for i, r := range ret.Rows {
			if i >= 3 {
				break
			}
			fmt.Printf("  #%d datetime=%s broker=%s volume_rank=%s volume=%s\n",
				i+1, r.Datetime, r.Broker, fmtPtrFloat(r.VolumeRanking), fmtPtrFloat(r.Volume))
		}
		return nil
	})

	fmt.Println("== Query Demo Done ==")
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func headIDs(ids []api.InstrumentID, n int) []api.InstrumentID {
	if len(ids) <= n {
		return ids
	}
	return ids[:n]
}

func headIDPtrs(ids []*api.InstrumentID, n int) []string {
	if len(ids) > n {
		ids = ids[:n]
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == nil {
			out = append(out, "<nil>")
			continue
		}
		out = append(out, string(*id))
	}
	return out
}

func runQuery(name string, timeout time.Duration, fn func(ctx context.Context) error) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := fn(ctx); err != nil {
		fmt.Printf("[%s] err=%v cost=%s\n", name, err, time.Since(start).Round(time.Millisecond))
		return
	}
	fmt.Printf("[%s] ok cost=%s\n", name, time.Since(start).Round(time.Millisecond))
}

func parseCSVInts(raw string) []int {
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			continue
		}
		out = append(out, v)
	}
	return out
}

func countValidEDB(row []api.EDBValue) int {
	n := 0
	for _, v := range row {
		if v.Valid {
			n++
		}
	}
	return n
}

func fmtPtrFloat(v *float64) string {
	if v == nil {
		return "nil"
	}
	return fmt.Sprintf("%.4f", *v)
}

func parseYYYYMMDDPtr(v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse("20060102", v)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func fmtDatePtr(v *time.Time) string {
	if v == nil {
		return "<nil>"
	}
	return v.Format("2006-01-02")
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
