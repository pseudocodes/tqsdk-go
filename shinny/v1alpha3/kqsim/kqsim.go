// Package kqsim 声明天勤快期模拟账户（对应 Python TqKq）。
//
// 快期模拟是服务端模拟账户：资金、持仓、委托和成交由天勤服务端保存，可在快期
// APP、快期专业版、快期 v2/v3 中查看。它走网络交易网关，与 sim 包的本地
// TqSim 账户不是同一套账本。
//
// 主模拟账号使用天勤登录返回的 auth_id 同时作为资金账号和密码，并且不把账号
// 加入授权列表（Python TqKq 的 auto_add=false）。auth_id 是凭证的一部分，
// 不要写入日志。辅模拟账号（Number 1~99）使用 auth_id+三位编号和独立的固定密码，
// 需要专业版 tq_ma 权限。
//
// 用法（账户在行情授权完成后 Prepare，因此 tqapi.Config 必须配置 Market）：
//
//	client := auth.NewClient(os.Getenv("TQ_AUTH_USER"), os.Getenv("TQ_AUTH_PASS"))
//	api, err := tqapi.New(ctx, tqapi.Config{
//		Auth:    client,
//		Market:  &core.MarketConfig{},
//		Account: kqsim.New(client, kqsim.Config{}),
//	})
package kqsim

import (
	"errors"
	"fmt"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
)

// BrokerID 是快期模拟在天勤交易网关列表中的期货公司名。
const BrokerID = "快期模拟"

// Auth 提供天勤登录身份；*auth.Client 实现该接口。
type Auth interface {
	// AuthID 返回登录 JWT 的 sub（同 Python TqAuth._auth_id）。
	AuthID() string
	// HasFeature 查询登录账户的功能授权。
	HasFeature(feature string) bool
}

// Config 是快期模拟账户配置。
type Config struct {
	// ID 是本地账户身份；为空时主账号为 "KQ"，辅账号为 "KQ<三位编号>"。
	ID string
	// Number 为 0 表示主模拟账号；1~99 为专业版辅模拟账号。
	Number int
}

// Account 是快期模拟账户声明，实现 core.AccountSpec。
type Account struct {
	auth Auth
	cfg  Config
}

// New 创建快期模拟账户声明。auth_id 在 Prepare 时读取，所以同一个 Auth 必须
// 已完成天勤登录；tqapi 配置 Market 时会先完成行情授权，再 Prepare 账户。
func New(auth Auth, cfg Config) *Account {
	return &Account{auth: auth, cfg: cfg}
}

// Prepare 实现 core.AccountSpec。
func (a *Account) Prepare(t *core.Trading) (*core.AccountHandle, error) {
	if a.auth == nil {
		return nil, errors.New("kqsim: auth is nil")
	}
	if a.cfg.Number > 0 && !a.auth.HasFeature("tq_ma") {
		return nil, fmt.Errorf("kqsim: 辅模拟账号 %d 需要专业版 tq_ma 权限", a.cfg.Number)
	}
	cfg, err := a.accountConfig(a.auth.AuthID())
	if err != nil {
		return nil, err
	}
	return t.PrepareAccount(cfg)
}

// accountConfig 把登录身份转换为交易账户声明；与 core.Trading 解耦，便于测试。
func (a *Account) accountConfig(authID string) (core.AccountConfig, error) {
	if authID == "" {
		return core.AccountConfig{}, errors.New("kqsim: 天勤登录尚未完成（auth_id 为空）；请先完成行情授权")
	}
	if a.cfg.Number < 0 || a.cfg.Number > 99 {
		return core.AccountConfig{}, fmt.Errorf("kqsim: 辅模拟账号编号 %d 超出 0~99", a.cfg.Number)
	}

	id := a.cfg.ID
	if id == "" {
		if a.cfg.Number == 0 {
			id = "KQ"
		} else {
			id = fmt.Sprintf("KQ%03d", a.cfg.Number)
		}
	}

	// Python TqKq._update_otg_info：主账号用 auth_id 同时作为账号和密码；
	// 辅账号用 auth_id+三位编号，密码为固定前缀加三位编号。
	accountID, password := authID, authID
	if a.cfg.Number > 0 {
		accountID = fmt.Sprintf("%s%03d", authID, a.cfg.Number)
		password = "shinnytech%03d" + fmt.Sprintf("%03d", a.cfg.Number)
	}

	return core.AccountConfig{
		ID:        id,
		BrokerID:  BrokerID,
		UserName:  accountID,
		Password:  password,
		NoAutoAdd: true, // 快期模拟 auto_add=false，不做 update-grant-accounts
	}, nil
}
