package backtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/auth"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/backtest/internal/symbols"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/market"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// Mode 是在线数据的加载方式。
type Mode uint8

const (
	Prefetch Mode = iota // 订阅时把整段序列下载完再开始推进
	Rolling              // 运行中按页下载，后台提前请求下一页
)

// OnlineConfig 配置天勤历史行情服务。
type OnlineConfig struct {
	Auth     auth.Provider
	Mode     Mode
	CacheDir string // 仅 Prefetch：非空时把下载结果写成离线数据集格式，下次相同请求直接读取
	URL      string // 覆盖回测行情地址；为空时经名称服务（backtest=true）获取
}

const (
	pageWidth    = 10000
	pageRetries  = 3
	retryBackoff = time.Second
)

// online 使用独立的内部 Runtime/Market 下载历史数据；后台推进只发生在内部 Runtime，
// 策略可见状态仍只在回测 Step 中改变。
type online struct {
	cfg OnlineConfig

	mu  sync.Mutex
	md  *market.Market
	err error
}

// Online 返回使用天勤历史行情服务的 Provider。
func Online(cfg OnlineConfig) Provider { return &online{cfg: cfg} }

func (o *online) market(ctx context.Context) (*market.Market, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.md == nil && o.err == nil {
		o.md, o.err = market.Dial(ctx, market.Config{Auth: o.cfg.Auth, URL: o.cfg.URL, Backtest: true})
	}
	return o.md, o.err
}

func (o *online) instruments(ctx context.Context, syms []string) (map[string]model.Instrument, error) {
	md, err := o.market(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]model.Instrument{}
	list, err := md.QuerySymbolInfo(ctx, syms)
	if err != nil && ctx.Err() == nil {
		// 同 Python symbols.py：合约服务不认识的已下市合约改用内置资料，其余合约重新查询。
		var rest []string
		for _, s := range syms {
			if ins, ok := symbols.Expired(s); ok {
				out[s] = ins
			} else {
				rest = append(rest, s)
			}
		}
		if len(rest) == len(syms) {
			return nil, fmt.Errorf("backtest: query symbol info: %w", err)
		}
		list, err = nil, nil
		if len(rest) > 0 {
			list, err = md.QuerySymbolInfo(ctx, rest)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("backtest: query symbol info: %w", err)
	}
	for _, ins := range list {
		out[ins.Symbol] = ins
	}
	for _, s := range syms { // 同 Python：合约服务没有的已下市合约使用内置资料
		if _, ok := out[s]; !ok {
			if ins, ok := symbols.Expired(s); ok {
				out[s] = ins
			}
		}
	}
	return out, nil
}

func (o *online) cursor(symbol string, period time.Duration, from, end int64) cursor {
	return &onlineCursor{o: o, symbol: symbol, period: period, from: from, end: end, left: -1}
}

func (o *online) source() string {
	if o.cfg.Mode == Rolling {
		return "online rolling"
	}
	return "online prefetch"
}

func (o *online) close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.md != nil {
		return o.md.Close()
	}
	return nil
}

// pageResult 是一页的结果；由推进 goroutine 应用到游标，后台任务不直接修改游标。
type pageResult struct {
	rows []row
	next int64 // 下一页左端 ID
	eof  bool
	err  error
}

// onlineCursor 按页读取一条序列：首页按时间定位 from（含 from 之前的预热），之后按 ID 翻页。
type onlineCursor struct {
	o         *online
	symbol    string
	period    time.Duration
	from, end int64

	buf     []row
	left    int64 // 下一页左端 ID；-1 表示尚未取首页
	eof     bool
	pending chan pageResult // 后台在途页（滚动加载）
	cached  bool            // 预取已完成
}

func (c *onlineCursor) next(ctx context.Context) (row, bool, error) {
	if c.o.cfg.Mode == Prefetch && !c.cached {
		if err := c.prefetch(ctx); err != nil {
			return row{}, false, err
		}
	}
	for len(c.buf) == 0 {
		if c.eof && c.pending == nil {
			return row{}, false, nil
		}
		if err := c.fill(ctx); err != nil {
			return row{}, false, err
		}
	}
	k := c.buf[0]
	c.buf = c.buf[1:]
	if c.o.cfg.Mode == Rolling && !c.eof && c.pending == nil && len(c.buf) < pageWidth/2 {
		ch := make(chan pageResult, 1)
		c.pending = ch
		left := c.left
		go func() { ch <- c.load(context.Background(), left) }()
	}
	return k, true, nil
}

// fill 取得下一页：优先等待后台在途页，否则同步请求。ctx 结束时保留在途页。
func (c *onlineCursor) fill(ctx context.Context) error {
	var res pageResult
	if c.pending != nil {
		select {
		case res = <-c.pending:
			c.pending = nil
		case <-ctx.Done():
			return ctx.Err()
		}
	} else {
		res = c.load(ctx, c.left)
	}
	return c.apply(res)
}

func (c *onlineCursor) apply(res pageResult) error {
	if res.err != nil {
		return res.err
	}
	c.buf = append(c.buf, res.rows...)
	c.left, c.eof = res.next, res.eof
	return nil
}

// load 请求一页；失败时有限次退避重试（附录 A20）。只读游标的不可变字段，可在后台调用。
func (c *onlineCursor) load(ctx context.Context, left int64) pageResult {
	md, err := c.o.market(ctx)
	if err != nil {
		return pageResult{err: err}
	}
	pos := core.WindowPosition{Mode: core.ByID, LeftID: left}
	if left < 0 {
		pos = core.WindowPosition{Mode: core.ByTime, DatetimeNS: c.from, FocusPosition: pageWidth - 1}
	}
	var p page
	for attempt := 0; ; attempt++ {
		p, err = c.fetch(ctx, md, pos)
		if err == nil || ctx.Err() != nil || attempt == pageRetries {
			break
		}
		select {
		case <-time.After(retryBackoff << attempt):
		case <-ctx.Done():
			return pageResult{err: ctx.Err()}
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return pageResult{err: ctx.Err()}
		}
		return pageResult{err: fmt.Errorf("backtest: download %s %v: %w", c.symbol, c.period, err)}
	}
	r := p.window.Range
	if !p.window.RangeKnown || p.window.ValidCount == 0 {
		return pageResult{next: left, eof: true}
	}
	res := pageResult{next: r.RightID + 1}
	for _, x := range p.rows {
		if x.dt > c.end {
			res.eof = true
			break
		}
		res.rows = append(res.rows, x)
	}
	if r.RightID >= p.lastID || res.next <= left {
		res.eof = true
	}
	// 已转为游标行的部分不再保留在下载器中
	if c.period == 0 {
		md.Core().ReleaseTicks(c.symbol, res.next)
	} else {
		md.Core().ReleaseKlines(c.symbol, c.period, res.next)
	}
	return res
}

// page 是一页历史数据中的真实行及窗口信息。
type page struct {
	rows   []row
	window core.WindowInfo
	lastID int64
}

// fetch 请求一页 K 线或 Tick（周期 0）。
func (c *onlineCursor) fetch(ctx context.Context, md *market.Market, pos core.WindowPosition) (page, error) {
	if c.period == 0 {
		f, err := md.QueryTicksPage(ctx, core.TickRequest{Symbol: c.symbol, Width: pageWidth, Position: pos})
		p := page{window: f.Window, lastID: f.SeriesLastID}
		for i := range f.Rows {
			if f.Rows[i].Present {
				p.rows = append(p.rows, tickRow(&f.Rows[i].Value))
			}
		}
		return p, err
	}
	f, err := md.QueryKlinesPage(ctx, core.KlineRequest{Symbol: c.symbol, Period: c.period, Width: pageWidth, Position: pos})
	p := page{window: f.Window, lastID: f.SeriesLastID}
	for _, s := range f.Rows {
		if s.Present {
			p.rows = append(p.rows, barRow(s.Value))
		}
	}
	return p, err
}

// prefetch 读完整条序列；配置了 CacheDir 时读写离线数据集格式的缓存。
func (c *onlineCursor) prefetch(ctx context.Context) error {
	dir := ""
	if c.o.cfg.CacheDir != "" {
		dir = filepath.Join(c.o.cfg.CacheDir, fmt.Sprintf("%s_%ds_%d_%d", c.symbol, int64(c.period/time.Second), c.from, c.end))
		if rows, err := readRows(filepath.Join(dir, seriesFile(c.symbol, c.period)), c.period); err == nil {
			c.buf, c.eof, c.cached = rows, true, true
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for !c.eof {
		if err := c.apply(c.load(ctx, c.left)); err != nil {
			return err
		}
	}
	c.cached = true
	if dir != "" {
		if err := WriteDataset(dir, "cache", nil, []Series{seriesOf(c.symbol, c.period, c.buf)}); err != nil {
			return fmt.Errorf("backtest: write cache: %w", err)
		}
	}
	return nil
}
