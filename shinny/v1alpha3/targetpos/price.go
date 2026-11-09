package targetpos

import (
	"math"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// PricePolicy 决定子单的限价。返回 NaN 或 Inf 表示无可用价格，任务按 Python 报错并进入失败收尾。
// 实现须为可比较类型（单例按 == 比较配置），自定义函数请用 PriceFunc 包装。
type PricePolicy interface {
	Price(dir model.Direction, q model.Quote) float64
}

// PriceMode 是内置定价方式。
type PriceMode uint8

const (
	Active  PriceMode = iota // 对价：买取卖一、卖取买一
	Passive                  // 排队价：买取买一、卖取卖一
)

// Price 实现 PricePolicy（Python _get_price）：本方向盘口无效时依次回退对侧盘口、最新价、昨收盘。
func (m PriceMode) Price(dir model.Direction, q model.Quote) float64 {
	first, second := q.AskPrice1, q.BidPrice1
	if (dir == model.DirectionSell) != (m == Passive) {
		first, second = second, first
	}
	for _, p := range []float64{first, second, q.LastPrice, q.PreClose} {
		if !math.IsNaN(p) {
			return p
		}
	}
	return math.NaN()
}

func (m PriceMode) String() string { return [...]string{"ACTIVE", "PASSIVE"}[m] }

// Fixed 以固定价格下单。
type Fixed float64

// Price 实现 PricePolicy。
func (f Fixed) Price(model.Direction, model.Quote) float64 { return float64(f) }

type priceFunc struct {
	f func(model.Direction, model.Quote) float64
}

func (p *priceFunc) Price(dir model.Direction, q model.Quote) float64 { return p.f(dir, q) }

// PriceFunc 把自定义定价函数包装为 PricePolicy（Python price 为函数时的用法）。
// 返回值按指针身份比较：同一合约复用任务时须传入同一个返回值。
func PriceFunc(f func(dir model.Direction, q model.Quote) float64) PricePolicy { return &priceFunc{f} }
