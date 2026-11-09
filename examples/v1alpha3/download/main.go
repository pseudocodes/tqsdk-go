// Command download 下载指定时间段 [start, end) 的 K 线或 Tick 并写成 CSV，流程对应 Python DataDownloader：
// 先按时间定位左端（focus_datetime + focus_position=0），之后按 ID 分页（left_kline_id）直到越过 end
// 或序列尾部。每页是一次性 Chart 请求，完成后自动撤销。
//
//	TQ_AUTH_USER=... TQ_AUTH_PASS=... go run ./examples/v1alpha3/download -symbol SHFE.rb2701 -period 1m \
//	    -start 2026-09-01 -end 2026-09-02 -out rb2701_1m.csv
//
//	TQ_AUTH_USER=... TQ_AUTH_PASS=... go run ./examples/v1alpha3/download -type tick -symbol SHFE.rb2701 \
//	    -start "2026-09-01 09:00:00" -end "2026-09-01 10:00:00" -out rb2701_tick.csv
package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/auth"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/market"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

var cst = time.FixedZone("CST", 8*3600)

const pageWidth = 3000 // 与 Python DataDownloader 的 view_width 一致

var verbose = flag.Bool("v", false, "打印每页的范围")

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

func main() {
	kind := flag.String("type", "kline", "数据类型：kline 或 tick")
	symbol := flag.String("symbol", "SHFE.rb2701", "合约代码")
	period := flag.Duration("period", time.Minute, "K 线周期（仅 kline）")
	start := flag.String("start", time.Now().In(cst).AddDate(0, 0, -1).Format("2006-01-02"), "开始时间（北京时间，含），2006-01-02 或 2006-01-02 15:04:05")
	end := flag.String("end", time.Now().In(cst).Format("2006-01-02"), "结束时间（北京时间，不含）")
	out := flag.String("out", "", "输出 CSV 文件；为空时写到标准输出")
	url := flag.String("url", "", "行情地址覆盖；仍使用 TQ_AUTH_USER / TQ_AUTH_PASS 鉴权")
	timeout := flag.Duration("timeout", 5*time.Minute, "总超时")
	flag.Parse()

	startAt, err := parseTime(*start)
	if err != nil {
		log.Fatal(err)
	}
	endAt, err := parseTime(*end)
	if err != nil {
		log.Fatal(err)
	}
	if !endAt.After(startAt) {
		log.Fatal("end 必须晚于 start")
	}
	if *kind != "kline" && *kind != "tick" {
		log.Fatalf("未知数据类型 %q，可选 kline 或 tick", *kind)
	}

	provider := auth.NewClient(os.Getenv("TQ_AUTH_USER"), os.Getenv("TQ_AUTH_PASS"))
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	md, err := market.Dial(ctx, market.Config{Auth: provider, URL: *url})
	if err != nil {
		log.Fatal(err)
	}
	defer md.Close()

	var w io.Writer = os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		w = f
	}
	buf := bufio.NewWriter(w)
	defer buf.Flush()
	cw := csv.NewWriter(buf)
	defer cw.Flush()

	startNS, endNS := startAt.UnixNano(), endAt.UnixNano()
	span := fmt.Sprintf("[%s, %s)", startAt.Format(time.DateTime), endAt.Format(time.DateTime))
	switch *kind {
	case "tick":
		n, err := downloadTicks(ctx, md, cw, *symbol, startNS, endNS)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("%s tick %s 共 %d 笔", *symbol, span, n)
	default:
		n, err := downloadKlines(ctx, md, cw, *symbol, *period, startNS, endNS)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("%s %v %s 共 %d 根", *symbol, *period, span, n)
	}
}

// downloadKlines 分页读取 K 线；返回写出的行数。
func downloadKlines(ctx context.Context, md *market.Market, cw *csv.Writer, symbol string, period time.Duration, startNS, endNS int64) (int, error) {
	query := func(ctx context.Context, pos core.WindowPosition) (market.KlineFrame, error) {
		return md.QueryKlinesPage(ctx, market.KlineRequest{Symbol: symbol, Period: period, Width: pageWidth, Position: pos})
	}
	header := []string{"id", "datetime", "open", "high", "low", "close", "volume", "open_oi", "close_oi"}
	return download(ctx, cw, header, query, func(k model.Kline) int64 { return k.Datetime }, klineRecord, startNS, endNS)
}

// downloadTicks 分页读取 Tick；返回写出的行数。
func downloadTicks(ctx context.Context, md *market.Market, cw *csv.Writer, symbol string, startNS, endNS int64) (int, error) {
	query := func(ctx context.Context, pos core.WindowPosition) (market.TickFrame, error) {
		return md.QueryTicksPage(ctx, market.TickRequest{Symbol: symbol, Width: pageWidth, Position: pos})
	}
	header := []string{"id"}
	for f := range model.TickField(model.TickFieldCount) { // 列顺序与 tickRecord 一致
		header = append(header, f.String())
	}
	return download(ctx, cw, header, query, func(t model.Tick) int64 { return t.Datetime }, tickRecord, startNS, endNS)
}

// download 是 K 线与 Tick 共用的分页循环：先按时间定位左端，再按 ID 翻页，直到越过 endNS 或序列尾部。
func download[T any, F ~uint16](
	ctx context.Context, cw *csv.Writer, header []string,
	query func(context.Context, core.WindowPosition) (core.Frame[T, F], error),
	datetime func(T) int64, record func(core.Slot[T, F]) []string,
	startNS, endNS int64,
) (int, error) {
	if err := cw.Write(header); err != nil {
		return 0, err
	}
	pos := core.WindowPosition{Mode: core.ByTime, DatetimeNS: startNS}
	written := 0
	lastLeft := int64(-1)
	for {
		page, err := query(ctx, pos)
		if err != nil {
			return written, err
		}
		if *verbose {
			log.Printf("页 %+v 范围 %+v 真实行 %d 序列尾 %d", pos, page.Window.Range, page.Window.ValidCount, page.SeriesLastID)
		}
		if !page.Window.RangeKnown || page.Window.ValidCount == 0 {
			return written, nil // 该时间段没有数据
		}
		for _, r := range page.Rows {
			if !r.Present {
				return written, nil // 超出序列尾部
			}
			dt := datetime(r.Value)
			if dt >= endNS {
				return written, nil
			}
			if dt < startNS {
				continue
			}
			if err := cw.Write(record(r)); err != nil {
				return written, err
			}
			written++
		}
		next := page.Window.Range.RightID + 1
		if page.Window.Range.RightID >= page.SeriesLastID || next <= lastLeft {
			return written, nil // 已到最新一根，或服务端范围没有前进
		}
		lastLeft = next
		pos = core.WindowPosition{Mode: core.ByID, LeftID: next}
	}
}

const datetimeLayout = "2006-01-02 15:04:05.000000000"

func klineRecord(r core.KlineSlot) []string {
	k := r.Value
	f := func(v float64, id model.KlineField) string {
		if !r.Valid.Has(id) {
			return "" // 无效值（NaN）
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return []string{
		strconv.FormatInt(k.ID, 10),
		time.Unix(0, k.Datetime).In(cst).Format(datetimeLayout),
		f(k.Open, model.KlineOpen), f(k.High, model.KlineHigh), f(k.Low, model.KlineLow), f(k.Close, model.KlineClose),
		strconv.FormatInt(k.Volume, 10), strconv.FormatInt(k.OpenOI, 10), strconv.FormatInt(k.CloseOI, 10),
	}
}

// tickRecord 的列顺序与 model.TickField 编号一致（id 之后依次为 datetime、last_price ...）。
func tickRecord(r core.TickSlot) []string {
	t := r.Value
	fl := func(v float64, id model.TickField) string {
		if !r.Valid.Has(id) {
			return "" // 无效值（NaN），如交易所不提供的深档盘口
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	in := func(v int64, id model.TickField) string {
		if !r.Valid.Has(id) {
			return ""
		}
		return strconv.FormatInt(v, 10)
	}
	return []string{
		strconv.FormatInt(t.ID, 10),
		time.Unix(0, t.Datetime).In(cst).Format(datetimeLayout),
		fl(t.LastPrice, model.TickLastPrice), fl(t.Average, model.TickAverage),
		fl(t.Highest, model.TickHighest), fl(t.Lowest, model.TickLowest),
		fl(t.AskPrice1, model.TickAskPrice1), in(t.AskVolume1, model.TickAskVolume1),
		fl(t.BidPrice1, model.TickBidPrice1), in(t.BidVolume1, model.TickBidVolume1),
		fl(t.AskPrice2, model.TickAskPrice2), in(t.AskVolume2, model.TickAskVolume2),
		fl(t.BidPrice2, model.TickBidPrice2), in(t.BidVolume2, model.TickBidVolume2),
		fl(t.AskPrice3, model.TickAskPrice3), in(t.AskVolume3, model.TickAskVolume3),
		fl(t.BidPrice3, model.TickBidPrice3), in(t.BidVolume3, model.TickBidVolume3),
		fl(t.AskPrice4, model.TickAskPrice4), in(t.AskVolume4, model.TickAskVolume4),
		fl(t.BidPrice4, model.TickBidPrice4), in(t.BidVolume4, model.TickBidVolume4),
		fl(t.AskPrice5, model.TickAskPrice5), in(t.AskVolume5, model.TickAskVolume5),
		fl(t.BidPrice5, model.TickBidPrice5), in(t.BidVolume5, model.TickBidVolume5),
		in(t.Volume, model.TickVolume), fl(t.Amount, model.TickAmount),
		in(t.OpenInterest, model.TickOpenInterest),
	}
}

func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{time.DateTime, time.DateOnly} {
		if t, err := time.ParseInLocation(layout, s, cst); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析时间 %q", s)
}
