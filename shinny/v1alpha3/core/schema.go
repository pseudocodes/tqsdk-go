package core

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"unsafe"

	"encoding/json/jsontext"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// schema 是一个模型的字段表：由结构体 `tq` 标签反射得到，编号与 model 生成代码一致。
// 解码与安装直接按字段偏移读写，不经过通用 map 树。
type schema[T any, F ~uint16] struct {
	fields []field
	index  map[string]F   // 线上叶字段名 -> 编号
	groups map[string]int // 嵌套对象名（如 self_trade）-> 所含叶字段在 fields 中的起始下标
	// persist 表示成员值为 null 时不修改已有值，对应 Python 原型中的 "#"（持久对象，_merge_diff 丢弃 null）。
	// 目前只有 Quote；K 线/Tick 行与交易对象的原型为 "@"，null 仍清除字段。
	persist bool
}

type fieldKind uint8

const (
	kindFloat fieldKind = iota + 1
	kindInt
	kindString
	kindBool
)

type field struct {
	name  string
	kind  fieldKind
	off   uintptr
	group string // 所属嵌套对象名，顶层为空
}

// newSchema 反射 T 的 `tq` 标签；nameOf 为生成代码的名称函数，用于校验编号一致。
func newSchema[T any, F ~uint16](count int, nameOf func(F) string) *schema[T, F] {
	s := &schema[T, F]{index: map[string]F{}, groups: map[string]int{}}
	var walk func(t reflect.Type, base uintptr, prefix, group string)
	walk = func(t reflect.Type, base uintptr, prefix, group string) {
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			tag := sf.Tag.Get("tq")
			if tag == "" || tag == "-" {
				continue
			}
			if sf.Type.Kind() == reflect.Struct {
				s.groups[prefix+tag] = len(s.fields)
				walk(sf.Type, base+sf.Offset, prefix+tag+".", prefix+tag)
				continue
			}
			f := field{name: prefix + tag, off: base + sf.Offset, group: group}
			switch sf.Type.Kind() {
			case reflect.Float64:
				f.kind = kindFloat
			case reflect.Int64:
				f.kind = kindInt
			case reflect.String:
				f.kind = kindString
			case reflect.Bool:
				f.kind = kindBool
			default:
				panic(fmt.Sprintf("tqsdk: unsupported field kind %s.%s", t.Name(), sf.Name))
			}
			s.index[f.name] = F(len(s.fields))
			s.fields = append(s.fields, f)
		}
	}
	walk(reflect.TypeFor[T](), 0, "", "")
	if len(s.fields) != count {
		panic(fmt.Sprintf("tqsdk: schema %T has %d fields, generated %d; run go generate", *new(T), len(s.fields), count))
	}
	for i, f := range s.fields {
		if nameOf(F(i)) != f.name {
			panic(fmt.Sprintf("tqsdk: schema %T field %d is %q, generated %q; run go generate", *new(T), i, f.name, nameOf(F(i))))
		}
	}
	return s
}

var (
	quoteSchema    = persistent(newSchema[model.Quote](model.QuoteFieldCount, model.QuoteField.String))
	klineSchema    = newSchema[model.Kline](model.KlineFieldCount, model.KlineField.String)
	tickSchema     = newSchema[model.Tick](model.TickFieldCount, model.TickField.String)
	accountSchema  = newSchema[model.Account](model.AccountFieldCount, model.AccountField.String)
	positionSchema = newSchema[model.Position](model.PositionFieldCount, model.PositionField.String)
	orderSchema    = newSchema[model.Order](model.OrderFieldCount, model.OrderField.String)
	tradeSchema    = newSchema[model.Trade](model.TradeFieldCount, model.TradeField.String)
	notifySchema   = newSchema[model.Notify](model.NotifyFieldCount, model.NotifyField.String)
	riskRuleSchema = newSchema[model.RemoteRiskRule](model.RemoteRiskRuleFieldCount, model.RemoteRiskRuleField.String)
	riskDataSchema = newSchema[model.RemoteRiskData](model.RemoteRiskDataFieldCount, model.RemoteRiskDataField.String)
)

func persistent[T any, F ~uint16](s *schema[T, F]) *schema[T, F] {
	s.persist = true
	return s
}

// patch 是一个 JSON 对象对模型的增量：Set 的字段写入 Value 中的值，Clear 的字段置为无效。
type patch[T any, F ~uint16] struct {
	Set   model.Fields[F]
	Clear model.Fields[F]
	Value T
}

func (p *patch[T, F]) empty() bool { return !p.Set.Any() && !p.Clear.Any() }

// errType 表示字段值类型与 schema 不符，整帧拒绝。
var errType = errors.New("field type mismatch")

// decodeObject 读取一个 JSON 对象并合并进 p。调用前下一个 token 必须是 '{'。
// 未知字段校验后跳过；null 与允许的占位字符串产生 Clear，persist 模型的 null 不修改已有值。
func (s *schema[T, F]) decodeObject(d *jsontext.Decoder, p *patch[T, F]) error {
	return s.decodeMembers(d, p, "")
}

func (s *schema[T, F]) decodeMembers(d *jsontext.Decoder, p *patch[T, F], prefix string) error {
	return readObject(d, func(name string) error {
		full := name
		if prefix != "" {
			full = prefix + "." + name
		}
		if id, ok := s.index[full]; ok {
			return s.decodeValue(d, p, id)
		}
		if _, ok := s.groups[full]; ok {
			if d.PeekKind() == jsontext.KindNull {
				d.ReadToken()
				if s.persist {
					return nil
				}
				for i, f := range s.fields {
					if f.group == full {
						p.Set.Remove(F(i))
						p.Clear.Add(F(i))
					}
				}
				return nil
			}
			return s.decodeMembers(d, p, full)
		}
		return d.SkipValue()
	})
}

func (s *schema[T, F]) decodeValue(d *jsontext.Decoder, p *patch[T, F], id F) error {
	f := &s.fields[id]
	tok, err := d.ReadToken()
	if err != nil {
		return err
	}
	ptr := unsafe.Add(unsafe.Pointer(&p.Value), f.off)
	markClear := func() error { p.Set.Remove(id); p.Clear.Add(id); return nil }
	markSet := func() error { p.Clear.Remove(id); p.Set.Add(id); return nil }
	switch tok.Kind() {
	case jsontext.KindNull:
		if s.persist {
			return nil // 同 Python 持久原型：丢弃 null，保留旧值与有效位
		}
		return markClear()
	case jsontext.KindNumber:
		switch f.kind {
		case kindFloat:
			v, err := tok.Float()
			if err != nil {
				return fmt.Errorf("%s: %w", f.name, err)
			}
			*(*float64)(ptr) = v
			return markSet()
		case kindInt:
			v, err := parseInt(tok)
			if err != nil {
				return fmt.Errorf("%s: %w", f.name, err)
			}
			*(*int64)(ptr) = v
			return markSet()
		}
	case jsontext.KindString:
		str := tok.String()
		switch f.kind {
		case kindString:
			*(*string)(ptr) = str
			return markSet()
		case kindFloat, kindInt:
			// 线上用 "-" 或 "" 表示数值当前不可用（Python 以原型默认值替换）。
			if str == "-" || str == "" {
				return markClear()
			}
		}
	case jsontext.KindTrue, jsontext.KindFalse:
		if f.kind == kindBool {
			*(*bool)(ptr) = tok.Bool()
			return markSet()
		}
	}
	return fmt.Errorf("%s: %w (%v)", f.name, errType, tok.Kind())
}

// parseInt 解析整数字段；允许 3.0 这样的整值小数，拒绝溢出与非整数。
func parseInt(tok jsontext.Token) (int64, error) {
	v, err := tok.Int()
	if err == nil {
		return v, nil
	}
	if errors.Is(err, strconv.ErrRange) {
		return 0, err
	}
	fv, ferr := tok.Float()
	if ferr != nil || fv != math.Trunc(fv) || fv > math.MaxInt64 || fv < math.MinInt64 {
		return 0, fmt.Errorf("not an integer: %s", tok.String())
	}
	return int64(fv), nil
}

// apply 将 p 安装到 dst，同时维护 valid。
func (s *schema[T, F]) apply(dst *T, valid *model.Fields[F], p *patch[T, F]) {
	src := unsafe.Pointer(&p.Value)
	base := unsafe.Pointer(dst)
	p.Set.Each(func(id F) {
		f := &s.fields[id]
		copyField(unsafe.Add(base, f.off), unsafe.Add(src, f.off), f.kind)
		valid.Add(id)
	})
	p.Clear.Each(func(id F) {
		f := &s.fields[id]
		clearField(unsafe.Add(base, f.off), f.kind)
		valid.Remove(id)
	})
}

// reset 把 dst 的全部 schema 字段置为无效值：浮点为 NaN，其余为零值。
func (s *schema[T, F]) reset(dst *T) {
	base := unsafe.Pointer(dst)
	for i := range s.fields {
		clearField(unsafe.Add(base, s.fields[i].off), s.fields[i].kind)
	}
}

// changed 返回 a、b 之间有效性或值不同的字段。两侧均无效视为相同（包括 NaN）。
func (s *schema[T, F]) changed(a *T, va model.Fields[F], b *T, vb model.Fields[F]) model.Fields[F] {
	var out model.Fields[F]
	pa, pb := unsafe.Pointer(a), unsafe.Pointer(b)
	for i := range s.fields {
		id := F(i)
		ha, hb := va.Has(id), vb.Has(id)
		if ha != hb {
			out.Add(id)
			continue
		}
		if !ha {
			continue
		}
		f := &s.fields[i]
		if !sameField(unsafe.Add(pa, f.off), unsafe.Add(pb, f.off), f.kind) {
			out.Add(id)
		}
	}
	return out
}

// sameOn 报告 a、b 在字段集合 fs 上是否取值相同（只比较值，不看有效位）。
func (s *schema[T, F]) sameOn(a, b *T, fs model.Fields[F]) bool {
	same := true
	pa, pb := unsafe.Pointer(a), unsafe.Pointer(b)
	fs.Each(func(id F) {
		f := &s.fields[id]
		if !sameField(unsafe.Add(pa, f.off), unsafe.Add(pb, f.off), f.kind) {
			same = false
		}
	})
	return same
}

func (s *schema[T, F]) fieldID(name string) (F, bool) {
	id, ok := s.index[name]
	return id, ok
}

func copyField(dst, src unsafe.Pointer, k fieldKind) {
	switch k {
	case kindFloat:
		*(*float64)(dst) = *(*float64)(src)
	case kindInt:
		*(*int64)(dst) = *(*int64)(src)
	case kindString:
		*(*string)(dst) = *(*string)(src)
	case kindBool:
		*(*bool)(dst) = *(*bool)(src)
	}
}

func clearField(dst unsafe.Pointer, k fieldKind) {
	switch k {
	case kindFloat:
		*(*float64)(dst) = math.NaN()
	case kindInt:
		*(*int64)(dst) = 0
	case kindString:
		*(*string)(dst) = ""
	case kindBool:
		*(*bool)(dst) = false
	}
}

func sameField(a, b unsafe.Pointer, k fieldKind) bool {
	switch k {
	case kindFloat:
		x, y := *(*float64)(a), *(*float64)(b)
		return x == y || (x != x && y != y)
	case kindInt:
		return *(*int64)(a) == *(*int64)(b)
	case kindString:
		return *(*string)(a) == *(*string)(b)
	case kindBool:
		return *(*bool)(a) == *(*bool)(b)
	}
	return true
}

// readObject 读取一个 JSON 对象，对每个成员名调用 fn；fn 必须恰好消费该成员的值。
func readObject(d *jsontext.Decoder, fn func(name string) error) error {
	tok, err := d.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != jsontext.KindBeginObject {
		return fmt.Errorf("expect object, got %v", tok.Kind())
	}
	for d.PeekKind() != jsontext.KindEndObject {
		name, err := d.ReadToken()
		if err != nil {
			return err
		}
		if err := fn(name.String()); err != nil {
			return err
		}
	}
	_, err = d.ReadToken()
	return err
}

// readObjectOrNull 与 readObject 相同，但值为 null 时调用 onNull。
func readObjectOrNull(d *jsontext.Decoder, onNull func(), fn func(name string) error) error {
	if d.PeekKind() == jsontext.KindNull {
		d.ReadToken()
		onNull()
		return nil
	}
	return readObject(d, fn)
}

// readArray 读取一个 JSON 数组，对每个元素调用 fn；fn 必须恰好消费该元素。
func readArray(d *jsontext.Decoder, fn func() error) error {
	tok, err := d.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != jsontext.KindBeginArray {
		return fmt.Errorf("expect array, got %v", tok.Kind())
	}
	for d.PeekKind() != jsontext.KindEndArray {
		if err := fn(); err != nil {
			return err
		}
	}
	_, err = d.ReadToken()
	return err
}

// readInt / readBool / readString 读取一个标量；null 返回 ok=false。
func readInt(d *jsontext.Decoder) (int64, bool, error) {
	tok, err := d.ReadToken()
	if err != nil {
		return 0, false, err
	}
	switch tok.Kind() {
	case jsontext.KindNull:
		return 0, false, nil
	case jsontext.KindNumber:
		v, err := parseInt(tok)
		return v, err == nil, err
	}
	return 0, false, fmt.Errorf("%w: expect integer, got %v", errType, tok.Kind())
}

func readBool(d *jsontext.Decoder) (bool, bool, error) {
	tok, err := d.ReadToken()
	if err != nil {
		return false, false, err
	}
	switch tok.Kind() {
	case jsontext.KindNull:
		return false, false, nil
	case jsontext.KindTrue, jsontext.KindFalse:
		return tok.Bool(), true, nil
	}
	return false, false, fmt.Errorf("%w: expect bool, got %v", errType, tok.Kind())
}

func readString(d *jsontext.Decoder) (string, bool, error) {
	tok, err := d.ReadToken()
	if err != nil {
		return "", false, err
	}
	switch tok.Kind() {
	case jsontext.KindNull:
		return "", false, nil
	case jsontext.KindString:
		return tok.String(), true, nil
	}
	return "", false, fmt.Errorf("%w: expect string, got %v", errType, tok.Kind())
}

// readScalarText 读取一个标量并返回规范文本（用于 chart state 回显比较）；对象/数组返回错误。
func readScalarText(d *jsontext.Decoder) (string, bool, error) {
	tok, err := d.ReadToken()
	if err != nil {
		return "", false, err
	}
	switch tok.Kind() {
	case jsontext.KindNull:
		return "", false, nil
	case jsontext.KindString:
		return "s:" + tok.String(), true, nil
	case jsontext.KindNumber:
		if v, err := parseInt(tok); err == nil {
			return "n:" + strconv.FormatInt(v, 10), true, nil
		}
		return "n:" + tok.String(), true, nil
	case jsontext.KindTrue, jsontext.KindFalse:
		return "b:" + strconv.FormatBool(tok.Bool()), true, nil
	case jsontext.KindBeginObject, jsontext.KindBeginArray:
		return "", false, fmt.Errorf("%w: unexpected composite value", errType)
	}
	return "", false, fmt.Errorf("%w: %v", errType, tok.Kind())
}

// newDecoder 创建按帧解码的 Decoder；默认拒绝重复成员与非法 UTF-8。
func newDecoder(frame []byte) *jsontext.Decoder {
	return jsontext.NewDecoder(bytes.NewReader(frame))
}

// finishFrame 确认帧尾没有多余内容。
func finishFrame(d *jsontext.Decoder) error {
	if _, err := d.ReadToken(); err != io.EOF {
		return fmt.Errorf("trailing data after frame: %v", err)
	}
	return nil
}
