package core

import (
	"encoding/json/jsontext"
	"fmt"
	"strconv"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// mdBatch 是一个行情 envelope 解码后的有序 patch。
// 同类操作按 data[] 与成员顺序保存；跨类操作的先后不影响结果（就绪判断在整包安装后进行）。
type mdBatch struct {
	quotes  []quoteOp
	klines  []seriesOp[model.Kline, model.KlineField]
	ticks   []seriesOp[model.Tick, model.TickField]
	charts  []chartOp
	symbols []symbolOp
	insList *string
	mdhis   *bool
}

// quoteOp 是一个报价补丁。按 Python 的持久原型（"#"），quotes.<sym>: null 与字段 null 不产生修改；
// 只有根上的 quotes: null 删除全部报价（Python 在根层级 pop 整个 quotes）。
type quoteOp struct {
	symbol string // 空表示 quotes:null（删除全部）
	p      patch[model.Quote, model.QuoteField]
}

type seriesOpKind uint8

const (
	opSeriesDelete  seriesOpKind = iota + 1 // 删除序列（key.symbol 为空表示全部；allDur 表示该合约全部周期）
	opLastID                                // last_id
	opRow                                   // data.<id> 对象
	opRowDelete                             // data.<id>: null
	opRowsDelete                            // data: null
	opBinding                               // binding.<sym>.<from> = to（to<0 表示删除映射）
	opBindingDelete                         // binding.<sym>: null（sym 为空表示 binding: null）
)

type seriesOp[T any, F ~uint16] struct {
	kind   seriesOpKind
	key    seriesKey
	allDur bool
	id     int64 // 行 ID / binding from / last_id
	to     int64 // binding to
	sym    string
	p      patch[T, F]
}

type chartOp struct {
	id                                   string
	del                                  bool
	left, right                          int64
	hasLeft, hasRight, hasReady, hasMore bool
	ready, more                          bool
	state                                map[string]string // 规范化标量；值为空串表示删除该键
	stateNull                            bool
}

type symbolOp struct {
	id     string
	del    bool
	values map[string]jsontext.Value // result / error / errors；nil 值表示删除
}

// decodeMarket 解码一帧行情报文。非 rtn_data 报文（rsp_login 等）不需要安装。
func (m *Market) decodeMarket(frame []byte) (Prepared, error) {
	d := newDecoder(frame)
	b := &mdBatch{}
	var aid string
	err := readObject(d, func(name string) error {
		switch name {
		case "aid":
			s, _, err := readString(d)
			aid = s
			return err
		case "data":
			return readArray(d, func() error { return decodeMarketDiff(d, b) })
		}
		return d.SkipValue()
	})
	if err == nil {
		err = finishFrame(d)
	}
	if err != nil {
		return Prepared{}, err
	}
	if aid != "rtn_data" {
		return Prepared{}, nil
	}
	return Prepared{RtnData: true, Apply: func(st Stamp) (Applied, error) { return m.apply(b, st), nil }}, nil
}

func decodeMarketDiff(d *jsontext.Decoder, b *mdBatch) error {
	return readObjectOrNull(d, func() {}, func(root string) error {
		switch root {
		case "quotes":
			return readObjectOrNull(d, func() { b.quotes = append(b.quotes, quoteOp{}) }, func(sym string) error {
				if d.PeekKind() == jsontext.KindNull { // 持久原型：不删除报价（如退订后服务端下发的 null）
					_, err := d.ReadToken()
					return err
				}
				op := quoteOp{symbol: sym}
				if err := quoteSchema.decodeObject(d, &op.p); err != nil {
					return fmt.Errorf("quotes.%s.%w", sym, err)
				}
				b.quotes = append(b.quotes, op)
				return nil
			})
		case "klines":
			return readObjectOrNull(d, func() {
				b.klines = append(b.klines, seriesOp[model.Kline, model.KlineField]{kind: opSeriesDelete})
			}, func(sym string) error {
				return readObjectOrNull(d, func() {
					b.klines = append(b.klines, seriesOp[model.Kline, model.KlineField]{kind: opSeriesDelete, key: seriesKey{sym, 0}, allDur: true})
				}, func(durText string) error {
					dur, err := strconv.ParseInt(durText, 10, 64)
					if err != nil || dur <= 0 {
						return fmt.Errorf("klines.%s: bad duration %q", sym, durText)
					}
					return decodeSeries(d, &b.klines, klineSchema, seriesKey{sym, dur}, true)
				})
			})
		case "ticks":
			return readObjectOrNull(d, func() {
				b.ticks = append(b.ticks, seriesOp[model.Tick, model.TickField]{kind: opSeriesDelete})
			}, func(sym string) error {
				return decodeSeries(d, &b.ticks, tickSchema, seriesKey{sym, 0}, false)
			})
		case "charts":
			return readObjectOrNull(d, func() {}, func(id string) error { return decodeChart(d, b, id) })
		case "symbols":
			return readObjectOrNull(d, func() {}, func(id string) error { return decodeSymbol(d, b, id) })
		case "ins_list":
			s, ok, err := readString(d)
			if ok {
				b.insList = &s
			}
			return err
		case "mdhis_more_data":
			v, ok, err := readBool(d)
			if ok {
				b.mdhis = &v
			}
			return err
		}
		return d.SkipValue()
	})
}

func decodeSeries[T any, F ~uint16](d *jsontext.Decoder, ops *[]seriesOp[T, F], sc *schema[T, F], key seriesKey, binding bool) error {
	add := func(op seriesOp[T, F]) { op.key = key; *ops = append(*ops, op) }
	return readObjectOrNull(d, func() { add(seriesOp[T, F]{kind: opSeriesDelete}) }, func(name string) error {
		switch name {
		case "last_id":
			v, ok, err := readInt(d)
			if !ok {
				v = -1
			}
			add(seriesOp[T, F]{kind: opLastID, id: v})
			return err
		case "data":
			return readObjectOrNull(d, func() { add(seriesOp[T, F]{kind: opRowsDelete}) }, func(idText string) error {
				id, err := strconv.ParseInt(idText, 10, 64)
				if err != nil || id < 0 {
					return fmt.Errorf("%s: bad row id %q", key.symbol, idText)
				}
				if d.PeekKind() == jsontext.KindNull {
					d.ReadToken()
					add(seriesOp[T, F]{kind: opRowDelete, id: id})
					return nil
				}
				op := seriesOp[T, F]{kind: opRow, id: id}
				if err := sc.decodeObject(d, &op.p); err != nil {
					return fmt.Errorf("%s.data.%d.%w", key.symbol, id, err)
				}
				add(op)
				return nil
			})
		case "binding":
			if !binding {
				return d.SkipValue()
			}
			return readObjectOrNull(d, func() { add(seriesOp[T, F]{kind: opBindingDelete}) }, func(sym string) error {
				return readObjectOrNull(d, func() { add(seriesOp[T, F]{kind: opBindingDelete, sym: sym}) }, func(fromText string) error {
					from, err := strconv.ParseInt(fromText, 10, 64)
					if err != nil {
						return fmt.Errorf("%s.binding.%s: bad id %q", key.symbol, sym, fromText)
					}
					to, ok, err := readInt(d)
					if !ok {
						to = -1
					}
					add(seriesOp[T, F]{kind: opBinding, sym: sym, id: from, to: to})
					return err
				})
			})
		}
		return d.SkipValue()
	})
}

func decodeChart(d *jsontext.Decoder, b *mdBatch, id string) error {
	op := chartOp{id: id}
	err := readObjectOrNull(d, func() { op.del = true }, func(name string) error {
		var err error
		var ok bool
		switch name {
		case "left_id":
			op.left, ok, err = readInt(d)
			op.hasLeft = true
			if !ok {
				op.left = -1
			}
		case "right_id":
			op.right, ok, err = readInt(d)
			op.hasRight = true
			if !ok {
				op.right = -1
			}
		case "ready":
			op.ready, op.hasReady, err = readBool(d)
			op.hasReady = true
		case "more_data":
			op.more, op.hasMore, err = readBool(d)
			op.hasMore = true
		case "state":
			if op.state == nil {
				op.state = map[string]string{}
			}
			return readObjectOrNull(d, func() { op.stateNull = true }, func(key string) error {
				v, _, err := readScalarText(d)
				op.state[key] = v
				return err
			})
		default:
			err = d.SkipValue()
		}
		return err
	})
	b.charts = append(b.charts, op)
	return err
}

func decodeSymbol(d *jsontext.Decoder, b *mdBatch, id string) error {
	op := symbolOp{id: id}
	err := readObjectOrNull(d, func() { op.del = true }, func(name string) error {
		switch name {
		case "result", "error", "errors":
			v, err := d.ReadValue()
			if err != nil {
				return err
			}
			if op.values == nil {
				op.values = map[string]jsontext.Value{}
			}
			if v.Kind() == jsontext.KindNull {
				op.values[name] = nil
			} else {
				op.values[name] = v.Clone()
			}
			return nil
		}
		return d.SkipValue()
	})
	b.symbols = append(b.symbols, op)
	return err
}
