// Package auth 定义授权边界：core 通过 Provider 取得连接地址、请求头与功能授权，
// 生产实现 Client 负责天勤账户登录、授权与路由发现（对应 Python tqsdk/auth.py）。
package auth

import (
	"context"
	"net/http"
)

// Service 是需要接入的服务。
type Service uint8

const (
	Market Service = iota // 行情与合约服务
	Trade                 // 交易服务
)

// Request 是一次接入授权请求。
type Request struct {
	Service  Service
	BrokerID string // 交易：期货公司
	// AccountID 在交易时是券商资金账号（core.AccountConfig.UserName），不是天勤用户名。
	AccountID string
	// Backtest 表示接入回测行情服务（名称服务 backtest=true）；同 Python 回测模式，
	// 授权结果对 futr / sec / lmt_idx 视为已授权。
	Backtest bool
}

// Access 是授权结果。Endpoint 为空时由调用方的 URL 配置决定地址。
// Features 为 nil 表示未授予任何功能权限；放行必须显式提供 FeatureSet。
type Access struct {
	Endpoint string
	Header   http.Header
	Features FeatureSet
}

// HasFeature 查询授权后的功能权限，不发起接入授权；Features 为 nil 时返回 false。
func (a Access) HasFeature(feature string) bool {
	return a.Features != nil && a.Features.HasFeature(feature)
}

// Provider 为 core 提供接入授权。成功返回表示允许接入服务；后续功能权限通过 Access.HasFeature 查询。
type Provider interface {
	Authorize(context.Context, Request) (Access, error)
}

// ProviderFunc 让普通函数实现 Provider，便于测试注入。
type ProviderFunc func(context.Context, Request) (Access, error)

// Authorize 实现 Provider。
func (f ProviderFunc) Authorize(ctx context.Context, r Request) (Access, error) { return f(ctx, r) }

// None 返回显式允许接入及全部功能的 Provider，用于回放或已知无需鉴权的地址。
func None() Provider {
	return ProviderFunc(func(context.Context, Request) (Access, error) {
		return Access{Features: FeatureFunc(func(string) bool { return true })}, nil
	})
}
