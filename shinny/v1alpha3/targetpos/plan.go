package targetpos

import (
	"fmt"
	"strings"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// parsePriority 校验开平优先级并按逗号分组（Python _check_offset_priority）：
// 今、昨、开各至多出现一次，逗号表示等待之前的组完成。
func parsePriority(s string) ([]string, error) {
	rest := strings.ReplaceAll(s, ",", "")
	for _, c := range []string{"今", "昨", "开"} {
		rest = strings.Replace(rest, c, "", 1)
	}
	if rest != "" {
		return nil, fmt.Errorf("targetpos: 开平仓顺序(offset_priority) %s 错误, 请检查 offset_priority 参数是否填写正确", s)
	}
	return strings.Split(s, ","), nil
}

// order 是一组中的一项计划：方向、开平与手数。
type order struct {
	dir    model.Direction
	offset model.Offset
	volume int64
}

// planGroup 按 Python _get_order 为一组优先级分配平今、平昨与开仓手数；delta 为待调整的净仓（正为买）。
// 不区分平今的交易所共用可平预算：本组已分配的平仓量计为冻结，仍有未冻结今仓时跳过“昨”。
// 调用时本任务没有在场委托，外部在场委托由调用方按独占约束拒绝，因此挂单冻结为 0。
func planGroup(group string, delta int64, exchange string, p model.Position) []order {
	shfe := exchange == "SHFE" || exchange == "INE"
	var out []order
	var pending int64
	for _, c := range group {
		if delta == 0 {
			break
		}
		dir, need := model.DirectionBuy, delta
		all, today, his := p.PosShort(), p.PosShortToday, p.PosShortHis
		if delta < 0 {
			dir, need = model.DirectionSell, -delta
			all, today, his = p.PosLong(), p.PosLongToday, p.PosLongHis
		}
		o := order{dir: dir}
		switch c {
		case '昨':
			o.offset = model.OffsetClose
			switch {
			case shfe:
				o.volume = min(need, max(0, his))
			case today-pending <= 0:
				o.volume = min(need, max(0, all-pending))
			}
		case '今':
			if shfe {
				o.offset, o.volume = model.OffsetCloseToday, min(need, max(0, today))
			} else {
				o.offset, o.volume = model.OffsetClose, min(need, max(0, today-pending))
			}
		case '开':
			o.offset, o.volume = model.OffsetOpen, need
		}
		if o.volume == 0 {
			continue
		}
		if o.offset != model.OffsetOpen {
			pending += o.volume
		}
		out = append(out, o)
		if dir == model.DirectionBuy {
			delta -= o.volume
		} else {
			delta += o.volume
		}
	}
	return out
}
