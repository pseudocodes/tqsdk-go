package sim

import (
	"strings"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// ResolveCosts 为 KQ 主连与指数补齐每手保证金与手续费：指数取对应主连的标的，主连取其标的
// （Python basesim._quote_handler 对指数的处理，Go 同样用于主连，便于回测）。合约服务给出的是当前值，
// 主连的标的也是当前主力合约。lookup 返回合约资料，ok=false 表示尚未取得、需要稍后重试。
// 来源缺少费用资料时原样返回，由报单检查按"不支持的合约类型"拒绝；也可用 Config.Margins/Commissions 覆盖。
func ResolveCosts(ins model.Instrument, lookup func(symbol string) (model.Instrument, bool)) (model.Instrument, bool) {
	src := ins
	for range 2 { // 指数 → 主连 → 标的
		next := costSource(src)
		if next == "" {
			break
		}
		var ok bool
		if src, ok = lookup(next); !ok {
			return ins, false
		}
	}
	for _, f := range []model.InstrumentField{model.InstrumentMargin, model.InstrumentCommission} {
		if src.Valid.Has(f) {
			ins.Valid.Add(f)
		}
	}
	ins.Margin, ins.Commission = src.Margin, src.Commission
	return ins, true
}

func costSource(ins model.Instrument) string {
	if ins.ExchangeID != "KQ" || ins.Valid.Has(model.InstrumentMargin) && ins.Valid.Has(model.InstrumentCommission) {
		return ""
	}
	switch ins.InsClass {
	case "INDEX":
		return strings.Replace(ins.Symbol, "KQ.i@", "KQ.m@", 1)
	case "CONT":
		return ins.UnderlyingSymbol
	}
	return ""
}

// tradable 报告 sim 能否交易该类合约：期货，以及 KQ 主连与指数。
func tradable(ins model.Instrument) bool {
	return ins.InsClass == "FUTURE" || ins.ExchangeID == "KQ" && (ins.InsClass == "CONT" || ins.InsClass == "INDEX")
}
