// Package sim 是本地模拟账户（对应 Python TqSim）：PythonCompatible 期货撮合与账务。
//
// 规则逐条对应 tqsdk-python 3.10.4 的 tradeable/sim/trade_base.py 与 trade_future.py：
// 买限价 ≥ 卖一、卖限价 ≤ 买一时按委托价整笔成交；只冻结保证金；账户保证金按品种取单向大边；
// 日结按最新价逐日盯市。首版只支持期货与 KQ 主连、指数的限价 GFD 单，FAK/FOK、市价、期权等在本地拒绝。
// 主连与指数没有费用资料时取标的（指数经主连取标的）的每手保证金与手续费，见 ResolveCosts；
// 指数行情没有盘口时以最新价 ± price_tick 撮合（同 Python）。
//
// 同一账本（Ledger）有两种驱动方式：backtest 包按历史数据与虚拟时间驱动；Paper 由实时行情驱动（paper trade）。
package sim

import (
	"fmt"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
)

// DefaultInitBalance 是 Python TqSim 的默认初始资金。
const DefaultInitBalance = 10_000_000

// Config 是模拟账户配置。
type Config struct {
	ID          string  // 本地账户身份；为空时为 "TQSIM"
	InitBalance float64 // 初始资金；0 表示默认 10,000,000，负值拒绝

	// 用户覆盖的每手保证金、每手手续费（对应 Python set_margin / set_commission），按合约代码。
	Margins     map[string]float64
	Commissions map[string]float64
}

// Account 是模拟账户声明，实现 core.AccountSpec。运行态（账本）在装配时由回测驱动或实时模拟后端（Paper）
// 创建，重复使用同一声明不共享资金、订单或成交。
type Account struct{ cfg Config }

// New 复制配置并创建账户声明；配置错误在装配时返回。
func New(cfg Config) *Account {
	if cfg.ID == "" {
		cfg.ID = "TQSIM"
	}
	if cfg.InitBalance == 0 {
		cfg.InitBalance = DefaultInitBalance
	}
	cfg.Margins = clone(cfg.Margins)
	cfg.Commissions = clone(cfg.Commissions)
	return &Account{cfg: cfg}
}

// Config 返回声明的配置副本。
func (s *Account) Config() Config {
	c := s.cfg
	c.Margins, c.Commissions = clone(c.Margins), clone(c.Commissions)
	return c
}

// Prepare 实现 core.AccountSpec：登记本地账户，订单 ID 按接受顺序确定性生成（TQGO_bt_<ID>_<序号>）。
func (s *Account) Prepare(t *core.Trading) (*core.AccountHandle, error) {
	if s.cfg.InitBalance < 0 {
		return nil, fmt.Errorf("sim: 初始资金 %v 错误", s.cfg.InitBalance)
	}
	seq := 0
	id := s.cfg.ID
	return t.PrepareLocalAccount(core.AccountConfig{ID: id, NewOrderID: func() string {
		seq++
		return fmt.Sprintf("TQGO_bt_%s_%d", id, seq)
	}})
}

func clone(m map[string]float64) map[string]float64 {
	if m == nil {
		return nil
	}
	c := make(map[string]float64, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}
