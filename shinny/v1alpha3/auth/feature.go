package auth

import "slices"

// FeatureSet 查询功能授权。实现必须并发安全，且不做阻塞 I/O。
type FeatureSet interface {
	HasFeature(feature string) bool
}

// FeatureFunc 让普通函数实现 FeatureSet，便于动态替换功能授权。
type FeatureFunc func(feature string) bool

// HasFeature 实现 FeatureSet。
func (f FeatureFunc) HasFeature(feature string) bool { return f(feature) }

// Granted 返回不可变的功能集合；Granted() 拒绝所有功能。
// 功能名按字面匹配，例如 "futr"、"sec"、"tq_dl"。
func Granted(features ...string) FeatureSet {
	features = slices.Clone(features)
	return FeatureFunc(func(feature string) bool { return slices.Contains(features, feature) })
}
