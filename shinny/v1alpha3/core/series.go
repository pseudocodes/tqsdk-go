package core

import (
	"encoding/json/v2"
	"strconv"
	"strings"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// seriesKey 标识一条服务端序列：Kline 为 (symbol, duration ns)，Tick 的 dur 为 0。
type seriesKey struct {
	symbol string
	dur    int64
}

type row[T any, F ~uint16] struct {
	value T
	valid model.Fields[F]
}

// series 集中保存一条序列的行；窗口只持有范围视图。
// 服务端按连接维护已下发状态、只发送增量（语料 L01/H04：撤销后重新请求不会重发已有行与 last_id），
// 因此本地不能自行删除或裁剪已收到的数据，只在服务端下发 null 时删除（同 Python 的 _data）。
type series[T any, F ~uint16] struct {
	key     seriesKey
	lastID  int64 // -1 表示缺省或无数据
	lastGen uint64
	rows    map[int64]*row[T, F]
	binding map[string]map[int64]int64 // 副合约 -> 主 ID -> 副 ID（仅 K 线主序列）
}

func newSeries[T any, F ~uint16](key seriesKey) *series[T, F] {
	return &series[T, F]{key: key, lastID: -1, rows: map[int64]*row[T, F]{}}
}

// seriesKind 汇集一种序列（K 线或 Tick）的类型相关操作。
type seriesKind[T any, F ~uint16] struct {
	schema *schema[T, F]
	setID  func(*T, int64)
	empty  T // 所有浮点为 NaN 的空值
	store  func(m *Market) map[seriesKey]*series[T, F]
}

func newKind[T any, F ~uint16](sc *schema[T, F], setID func(*T, int64), store func(*Market) map[seriesKey]*series[T, F]) *seriesKind[T, F] {
	k := &seriesKind[T, F]{schema: sc, setID: setID, store: store}
	sc.reset(&k.empty)
	return k
}

var (
	klineKind = newKind(klineSchema, func(k *model.Kline, id int64) { k.ID = id },
		func(m *Market) map[seriesKey]*series[model.Kline, model.KlineField] { return m.klines })
	tickKind = newKind(tickSchema, func(t *model.Tick, id int64) { t.ID = id },
		func(m *Market) map[seriesKey]*series[model.Tick, model.TickField] { return m.ticks })
)

func (k *seriesKind[T, F]) get(m *Market, key seriesKey, create bool) *series[T, F] {
	store := k.store(m)
	s := store[key]
	if s == nil && create {
		s = newSeries[T, F](key)
		store[key] = s
	}
	return s
}

// slot 返回序列在 id 处的逻辑槽；s 可为 nil。
func (k *seriesKind[T, F]) slot(s *series[T, F], id int64) Slot[T, F] {
	if s != nil {
		if r := s.rows[id]; r != nil {
			v := r.value
			k.setID(&v, id)
			return Slot[T, F]{Value: v, Present: true, Valid: r.valid}
		}
	}
	v := k.empty
	k.setID(&v, id)
	return Slot[T, F]{Value: v}
}

// chartState 是一个 chart_id 的线上请求与回显状态。
type chartState struct {
	id      string
	pack    []byte            // 已编码的 set_chart，重连时重放
	desired map[string]string // 实际发送的键值（规范文本）
	echo    map[string]string // 服务端 state 合并结果
	left    int64
	right   int64
	ready   bool
	more    bool

	leftSeen, rightSeen, readySeen bool
}

func newChart(id string, desired map[string]string) *chartState {
	return &chartState{id: id, desired: desired, echo: map[string]string{}, left: -1, right: -1}
}

// acked 报告回显 state 是否包含本次请求的全部键值（Python: request.items() <= state.items()）。
func (c *chartState) acked() bool {
	for k, v := range c.desired {
		if c.echo[k] != v {
			return false
		}
	}
	return true
}

func (c *chartState) signalReady() bool { return c.readySeen && c.ready }

// resetEcho 在新连接代次开始时清除回显与控制状态。
func (c *chartState) resetEcho() {
	c.echo = map[string]string{}
	c.left, c.right = -1, -1
	c.ready, c.more = false, false
	c.leftSeen, c.rightSeen, c.readySeen = false, false, false
}

func (c *chartState) applyOp(op *chartOp) {
	if op.hasLeft {
		c.left, c.leftSeen = op.left, true
	}
	if op.hasRight {
		c.right, c.rightSeen = op.right, true
	}
	if op.hasReady {
		c.ready, c.readySeen = op.ready, true
	}
	if op.hasMore {
		c.more = op.more
	}
	if op.stateNull {
		c.echo = map[string]string{}
	}
	for k, v := range op.state {
		if v == "" {
			delete(c.echo, k)
		} else {
			c.echo[k] = v
		}
	}
}

// setChartPack 是 set_chart 请求；定位参数按模式二选一。
type setChartPack struct {
	Aid           string `json:"aid"`
	ChartID       string `json:"chart_id"`
	InsList       string `json:"ins_list"`
	Duration      int64  `json:"duration"`
	ViewWidth     int    `json:"view_width"`
	FocusDatetime *int64 `json:"focus_datetime,omitempty"`
	FocusPosition *int   `json:"focus_position,omitempty"`
	LeftKlineID   *int64 `json:"left_kline_id,omitempty"`
}

func newSetChart(id string, symbols []string, dur int64, width int, pos WindowPosition) setChartPack {
	p := setChartPack{Aid: "set_chart", ChartID: id, InsList: strings.Join(symbols, ","), Duration: dur, ViewWidth: width}
	switch pos.Mode {
	case ByTime:
		dt, fp := pos.DatetimeNS, pos.FocusPosition
		p.FocusDatetime, p.FocusPosition = &dt, &fp
	case ByID:
		left := pos.LeftID
		p.LeftKlineID = &left
	}
	return p
}

func (p setChartPack) encode() []byte {
	b, _ := json.Marshal(p)
	return b
}

// desired 返回请求键值的规范文本，与 readScalarText 的回显格式一致。
func (p setChartPack) desired() map[string]string {
	d := map[string]string{
		"aid":        "s:set_chart",
		"chart_id":   "s:" + p.ChartID,
		"ins_list":   "s:" + p.InsList,
		"duration":   "n:" + strconv.FormatInt(p.Duration, 10),
		"view_width": "n:" + strconv.Itoa(p.ViewWidth),
	}
	if p.FocusDatetime != nil {
		d["focus_datetime"] = "n:" + strconv.FormatInt(*p.FocusDatetime, 10)
		d["focus_position"] = "n:" + strconv.Itoa(*p.FocusPosition)
	}
	if p.LeftKlineID != nil {
		d["left_kline_id"] = "n:" + strconv.FormatInt(*p.LeftKlineID, 10)
	}
	return d
}

// cancelChart 是撤销请求：保留必要参数，ins_list 为空。
func cancelChart(id string, dur int64, width int) []byte {
	return setChartPack{Aid: "set_chart", ChartID: id, Duration: dur, ViewWidth: width}.encode()
}

func periodOf(dur int64) time.Duration { return time.Duration(dur) }
