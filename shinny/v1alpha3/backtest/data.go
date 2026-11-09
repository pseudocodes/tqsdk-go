package backtest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/backtest/internal/symbols"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/internal/wire"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// warmupBars 是订阅时刻之前的预热行数（K 线或 Tick），同 Python 回测的 view_width=10000。
const warmupBars = 10000

// Provider 提供历史 K 线、Tick 与合约资料：Dataset（离线数据集）或 Online（天勤历史行情服务）。
type Provider interface {
	// instruments 返回合约资料；资料缺失的合约不出现在结果中。
	instruments(ctx context.Context, symbols []string) (map[string]model.Instrument, error)
	// cursor 打开一条序列（period 为 0 表示 Tick）：先给出 from 之前最多 warmupBars 行预热，
	// 再按 ID 升序直到 end 或序列尾部。不做 I/O；数据在 next 中读取。
	cursor(symbol string, period time.Duration, from, end int64) cursor
	// source 返回结果中记录的数据来源说明。
	source() string
	close() error
}

// row 是序列的一行：K 线序列为 bar，Tick 序列为 tick（非 nil）。
type row struct {
	id, dt int64
	bar    model.Kline
	tick   *model.Tick
}

func barRow(k model.Kline) row { return row{id: k.ID, dt: k.Datetime, bar: k} }

func tickRow(t *model.Tick) row { return row{id: t.ID, dt: t.Datetime, tick: t} }

// cursor 是一条序列的行读取器；next 在数据未到达时于 ctx 内阻塞，ok=false 表示序列结束。
// 返回 ctx 错误时不丢失读取位置。
type cursor interface {
	next(ctx context.Context) (r row, ok bool, err error)
}

// sliceCursor 读取内存中的行。
type sliceCursor struct{ rows []row }

func (c *sliceCursor) next(context.Context) (row, bool, error) {
	if len(c.rows) == 0 {
		return row{}, false, nil
	}
	r := c.rows[0]
	c.rows = c.rows[1:]
	return r, true, nil
}

// window 从按 ID 升序的行中取出 from 之前最多 warmupBars 行及之后到 end 的部分。
func window(rows []row, from, end int64) []row {
	i := sort.Search(len(rows), func(i int) bool { return rows[i].dt >= from })
	j := sort.Search(len(rows), func(i int) bool { return rows[i].dt > end })
	return rows[max(0, i-warmupBars):j]
}

// ---- 数据集格式 ----
//
// 目录下一个 manifest.json 与每条序列一个 JSON Lines 文件。manifest 记录数据集标识、版本、合约资料
// （键名同 Python Quote）与序列清单；序列文件每行一根 K 线或一条 Tick（键名同协议，"-" 表示无效值），
// 按 ID 升序，ID 不要求连续。数据集中没有的序列视为缺失（订阅时报错），序列文件结束即 EOF。

type manifest struct {
	ID          string           `json:"id"`
	Version     string           `json:"version"`
	Source      string           `json:"source,omitempty"`
	Instruments []symbols.Info   `json:"instruments"`
	Series      []manifestSeries `json:"series"`
}

type manifestSeries struct {
	Symbol   string `json:"symbol"`
	PeriodNS int64  `json:"period_ns"`
	File     string `json:"file"`
}

type klineRow struct {
	ID       int64   `json:"id"`
	Datetime int64   `json:"datetime"`
	Open     float64 `json:"open"`
	High     float64 `json:"high"`
	Low      float64 `json:"low"`
	Close    float64 `json:"close"`
	Volume   int64   `json:"volume"`
	OpenOI   int64   `json:"open_oi"`
	CloseOI  int64   `json:"close_oi"`
}

// Series 是写入数据集的一条序列，按 ID 升序：K 线序列填 Rows；Period 为 0 时是 Tick 序列，填 Ticks。
type Series struct {
	Symbol string
	Period time.Duration
	Rows   []model.Kline
	Ticks  []model.Tick
}

func (s Series) rows() []row {
	out := make([]row, 0, len(s.Rows)+len(s.Ticks))
	for _, k := range s.Rows {
		out = append(out, barRow(k))
	}
	for i := range s.Ticks {
		out = append(out, tickRow(&s.Ticks[i]))
	}
	return out
}

// seriesOf 把游标行还原为数据集序列。
func seriesOf(symbol string, period time.Duration, rows []row) Series {
	s := Series{Symbol: symbol, Period: period}
	for _, r := range rows {
		if r.tick != nil {
			s.Ticks = append(s.Ticks, *r.tick)
		} else {
			s.Rows = append(s.Rows, r.bar)
		}
	}
	return s
}

// WriteDataset 把合约资料与 K 线序列写成离线数据集，供 Dataset 读取。
func WriteDataset(dir, id string, instruments []model.Instrument, series []Series) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	m := manifest{ID: id, Version: "1"}
	for _, ins := range instruments {
		m.Instruments = append(m.Instruments, symbols.FromInstrument(ins))
	}
	for _, s := range series {
		file := seriesFile(s.Symbol, s.Period)
		if err := writeRows(filepath.Join(dir, file), s.rows()); err != nil {
			return err
		}
		m.Series = append(m.Series, manifestSeries{Symbol: s.Symbol, PeriodNS: int64(s.Period), File: file})
	}
	return writeJSON(filepath.Join(dir, "manifest.json"), m)
}

func seriesFile(symbol string, period time.Duration) string {
	return fmt.Sprintf("%s_%ds.jsonl", symbol, int64(period/time.Second))
}

func writeRows(path string, rows []row) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, r := range rows {
		var v any
		if r.tick != nil {
			v = new(wire.Object).Set("id", r.id).Fields(r.tick, tickFields)
		} else {
			k := r.bar
			v = klineRow{k.ID, k.Datetime, k.Open, k.High, k.Low, k.Close, k.Volume, k.OpenOI, k.CloseOI}
		}
		b, err := json.Marshal(v)
		if err != nil {
			f.Close()
			return err
		}
		w.Write(b)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// readRows 读取序列文件；period 为 0 时按 Tick 解析。
func readRows(path string, period time.Duration) ([]row, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var rows []row
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if period == 0 {
			t := new(model.Tick)
			var id struct {
				ID int64 `json:"id"`
			}
			if err := errors.Join(json.Unmarshal(line, &id), wire.Decode(line, t)); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			t.ID = id.ID
			rows = append(rows, tickRow(t))
			continue
		}
		var r klineRow
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		rows = append(rows, barRow(model.Kline{ID: r.ID, Datetime: r.Datetime, Open: r.Open, High: r.High, Low: r.Low,
			Close: r.Close, Volume: r.Volume, OpenOI: r.OpenOI, CloseOI: r.CloseOI}))
	}
	return rows, sc.Err()
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// ---- 离线数据集 ----

type dataset struct {
	dir  string
	once sync.Once
	man  manifest
	err  error
}

// Dataset 返回读取本地数据集目录的 Provider（格式见 WriteDataset）。数据集未提供的合约资料
// 回退到内置的已下市合约表。
func Dataset(dir string) Provider { return &dataset{dir: dir} }

func (d *dataset) load() error {
	d.once.Do(func() {
		b, err := os.ReadFile(filepath.Join(d.dir, "manifest.json"))
		if err == nil {
			err = json.Unmarshal(b, &d.man)
		}
		if err != nil {
			d.err = fmt.Errorf("backtest: open dataset %s: %w", d.dir, err)
		}
	})
	return d.err
}

func (d *dataset) instruments(_ context.Context, syms []string) (map[string]model.Instrument, error) {
	if err := d.load(); err != nil {
		return nil, err
	}
	out := map[string]model.Instrument{}
	for _, s := range syms {
		for _, info := range d.man.Instruments {
			if info.InstrumentID == s {
				out[s] = info.Instrument()
			}
		}
		if _, ok := out[s]; !ok {
			if ins, ok := symbols.Expired(s); ok {
				out[s] = ins
			}
		}
	}
	return out, nil
}

func (d *dataset) cursor(symbol string, period time.Duration, from, end int64) cursor {
	return &datasetCursor{d: d, symbol: symbol, period: period, from: from, end: end}
}

func (d *dataset) source() string { return "dataset " + d.dir }

func (d *dataset) close() error { return nil }

// datasetCursor 在第一次读取时载入整条序列。
type datasetCursor struct {
	d         *dataset
	symbol    string
	period    time.Duration
	from, end int64
	rows      *sliceCursor
}

func (c *datasetCursor) next(ctx context.Context) (row, bool, error) {
	if c.rows == nil {
		if err := c.d.load(); err != nil {
			return row{}, false, err
		}
		var file string
		for _, s := range c.d.man.Series {
			if s.Symbol == c.symbol && s.PeriodNS == int64(c.period) {
				file = s.File
			}
		}
		if file == "" {
			return row{}, false, fmt.Errorf("backtest: dataset %s has no %s %v series", c.d.dir, c.symbol, c.period)
		}
		rows, err := readRows(filepath.Join(c.d.dir, file), c.period)
		if err != nil {
			return row{}, false, err
		}
		c.rows = &sliceCursor{rows: window(rows, c.from, c.end)}
	}
	return c.rows.next(ctx)
}
