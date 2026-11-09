// Package model 定义 SDK 对外的纯数据类型：行情、序列、交易与远端风控记录。
//
// 约定：
//   - float64 字段当前不可用时为 math.NaN()，同时对应有效位被清除；
//   - int64/string/bool 字段不可用时保持类型零值，是否有效只看有效位；
//   - 字段编号与有效位类型由 `go generate` 根据结构体的 `tq` 标签生成（fields_gen.go）。
//
// model 只依赖标准库，不包含锁、连接或协议 patch。
package model

//go:generate go run ../internal/schema/gen

import (
	"math/bits"
	"strconv"
)

func itoa(n int) string { return strconv.Itoa(n) }

// fieldWords 决定单个模型最多支持的字段数（64*fieldWords）。
const fieldWords = 2

// Fields 是某个模型的字段位图，F 为该模型的字段编号类型。
// 零值表示空集合。
type Fields[F ~uint16] struct {
	bits [fieldWords]uint64
}

// Has 报告字段 f 是否在集合中。
func (s Fields[F]) Has(f F) bool { return s.bits[f>>6]&(1<<(f&63)) != 0 }

// Add 将字段 f 加入集合。
func (s *Fields[F]) Add(f F) { s.bits[f>>6] |= 1 << (f & 63) }

// Remove 将字段 f 移出集合。
func (s *Fields[F]) Remove(f F) { s.bits[f>>6] &^= 1 << (f & 63) }

// Any 报告集合是否非空。
func (s Fields[F]) Any() bool {
	for _, w := range s.bits {
		if w != 0 {
			return true
		}
	}
	return false
}

// Or 返回两个集合的并集。
func (s Fields[F]) Or(o Fields[F]) Fields[F] {
	for i := range s.bits {
		s.bits[i] |= o.bits[i]
	}
	return s
}

// And 返回两个集合的交集。
func (s Fields[F]) And(o Fields[F]) Fields[F] {
	for i := range s.bits {
		s.bits[i] &= o.bits[i]
	}
	return s
}

// AndNot 返回在 s 中但不在 o 中的字段。
func (s Fields[F]) AndNot(o Fields[F]) Fields[F] {
	for i := range s.bits {
		s.bits[i] &^= o.bits[i]
	}
	return s
}

// Len 返回集合中的字段数。
func (s Fields[F]) Len() int {
	n := 0
	for _, w := range s.bits {
		n += bits.OnesCount64(w)
	}
	return n
}

// Each 按编号升序遍历集合中的字段。
func (s Fields[F]) Each(fn func(F)) {
	for i, w := range s.bits {
		for w != 0 {
			b := bits.TrailingZeros64(w)
			fn(F(i*64 + b))
			w &= w - 1
		}
	}
}

// AllFields 返回包含前 n 个字段的集合；由生成代码为各模型提供便捷封装。
func AllFields[F ~uint16](n int) Fields[F] {
	var s Fields[F]
	for i := 0; i < n; i++ {
		s.Add(F(i))
	}
	return s
}
