// Package wire 生成本地后端（回测、模拟账户）发给 core 的 rtn_data 报文。
// 对象按插入顺序编码，保证同一输入产生逐字节相同的报文；非有限浮点编码为 "-"（协议的不可用值）。
package wire

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"math"
	"reflect"
	"sync"
)

// Object 是按插入顺序编码的 JSON 对象。
type Object struct {
	keys []string
	vals []any
}

// Set 追加一个成员；值为 nil 时编码为 null。
func (o *Object) Set(k string, v any) *Object {
	o.keys = append(o.keys, k)
	o.vals = append(o.vals, v)
	return o
}

// Len 返回成员数。
func (o *Object) Len() int { return len(o.keys) }

// MarshalJSON 实现 json.Marshaler。
func (o *Object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		vb, err := json.Marshal(value(o.vals[i]))
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func value(v any) any {
	if f, ok := v.(float64); ok && (math.IsNaN(f) || math.IsInf(f, 0)) {
		return "-"
	}
	return v
}

// Frame 编码 {"aid":"rtn_data","data":[...]}。
func Frame(data []any) []byte {
	b, err := json.Marshal(new(Object).Set("aid", "rtn_data").Set("data", data))
	if err != nil {
		panic(err) // 只编码本包构造的值，失败即实现错误
	}
	return b
}

// Fields 按 tq 标签取结构体 v（指针或值）的指定字段，按 names 顺序组成对象。
func Fields(v any, names []string) *Object {
	return (&Object{keys: make([]string, 0, len(names)), vals: make([]any, 0, len(names))}).Fields(v, names)
}

// Fields 把结构体 v 中 names 对应的 tq 字段依次追加到 o。
func (o *Object) Fields(v any, names []string) *Object {
	rv := reflect.Indirect(reflect.ValueOf(v))
	idx := tagIndex(rv.Type())
	for _, n := range names {
		i, ok := idx[n]
		if !ok {
			panic("wire: unknown field " + n)
		}
		o.Set(n, rv.Field(i).Interface())
	}
	return o
}

// Decode 是 Fields 的逆操作：把 JSON 对象的成员按 tq 标签写入结构体指针 v。
// 浮点字段的 "-"、"" 与 null 写为 NaN；未知成员忽略。
func Decode(data []byte, v any) error {
	var m map[string]jsontext.Value
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	rv := reflect.ValueOf(v).Elem()
	idx := tagIndex(rv.Type())
	for k, raw := range m {
		i, ok := idx[k]
		if !ok {
			continue
		}
		f := rv.Field(i)
		if f.Kind() == reflect.Float64 && raw.Kind() != '0' {
			f.SetFloat(math.NaN())
			continue
		}
		if err := json.Unmarshal(raw, f.Addr().Interface()); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}
	return nil
}

var tagCache sync.Map // reflect.Type -> map[string]int

func tagIndex(t reflect.Type) map[string]int {
	if m, ok := tagCache.Load(t); ok {
		return m.(map[string]int)
	}
	m := map[string]int{}
	for i := range t.NumField() {
		if tag := t.Field(i).Tag.Get("tq"); tag != "" && tag != "-" {
			m[tag] = i
		}
	}
	tagCache.Store(t, m)
	return m
}
