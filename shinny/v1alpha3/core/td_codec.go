package core

import (
	"encoding/json/jsontext"
	"fmt"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

type tdKind uint8

const (
	tdAccount tdKind = iota + 1
	tdPosition
	tdOrder
	tdTrade
	tdNotify
	tdRiskRule
	tdRiskData
	tdSession    // session.trading_day / session.user_id
	tdTradingDay // trade.<user>.trading_day
	tdMoreData   // trade_more_data
	tdPositionReady
)

// tdOp 是交易 envelope 中的一个有序操作；控制操作与业务操作保持原始顺序。
type tdOp struct {
	kind tdKind
	key  string
	del  bool
	b    bool
	s    string // 交易日 / session.user_id
	s2   string // session.trading_day
	p    any    // *patch[...]
}

type tdBatch struct {
	ops []tdOp
}

// decodeTrading 解码一帧交易报文；只接受本账户（trade.<UserName>）的数据。
func (a *AccountHandle) decodeTrading(frame []byte) (Prepared, error) {
	d := newDecoder(frame)
	b := &tdBatch{}
	var aid string
	err := readObject(d, func(name string) error {
		switch name {
		case "aid":
			s, _, err := readString(d)
			aid = s
			return err
		case "data":
			return readArray(d, func() error { return a.decodeTradeDiff(d, b) })
		}
		return d.SkipValue()
	})
	if err == nil {
		err = finishFrame(d)
	}
	if err != nil {
		return Prepared{}, err
	}
	if aid != "rtn_data" {
		return Prepared{}, nil // rsp_login / rtn_brokers 等不改变业务状态
	}
	return Prepared{RtnData: true, Apply: func(st Stamp) (Applied, error) { return a.apply(b, st) },
		Check: func() error { return a.checkTrades(b) }}, nil
}

func (a *AccountHandle) decodeTradeDiff(d *jsontext.Decoder, b *tdBatch) error {
	return readObjectOrNull(d, func() {}, func(root string) error {
		switch root {
		case "notify":
			return readObjectOrNull(d, func() {}, func(id string) error {
				if d.PeekKind() == jsontext.KindNull {
					d.ReadToken()
					return nil
				}
				p := &patch[model.Notify, model.NotifyField]{}
				if err := notifySchema.decodeObject(d, p); err != nil {
					return fmt.Errorf("notify.%s.%w", id, err)
				}
				b.ops = append(b.ops, tdOp{kind: tdNotify, key: id, p: p})
				return nil
			})
		case "trade":
			return readObjectOrNull(d, func() {}, func(user string) error {
				if user != a.cfg.UserName {
					return d.SkipValue() // 其他账户的数据不复制到本账户
				}
				return readObjectOrNull(d, func() {}, func(name string) error { return decodeTradeNode(d, b, name) })
			})
		}
		return d.SkipValue()
	})
}

func decodeTradeNode(d *jsontext.Decoder, b *tdBatch, name string) error {
	switch name {
	case "accounts":
		return decodeEntries(d, b, tdAccount, accountSchema)
	case "positions":
		return decodeEntries(d, b, tdPosition, positionSchema)
	case "orders":
		return decodeEntries(d, b, tdOrder, orderSchema)
	case "trades":
		return decodeEntries(d, b, tdTrade, tradeSchema)
	case "risk_management_rule":
		return decodeEntries(d, b, tdRiskRule, riskRuleSchema)
	case "risk_management_data":
		return decodeEntries(d, b, tdRiskData, riskDataSchema)
	case "session":
		op := tdOp{kind: tdSession}
		err := readObjectOrNull(d, func() {}, func(k string) error {
			var err error
			switch k {
			case "user_id":
				op.s, _, err = readString(d)
			case "trading_day":
				op.s2, _, err = readString(d)
			default:
				err = d.SkipValue()
			}
			return err
		})
		b.ops = append(b.ops, op)
		return err
	case "trading_day":
		s, ok, err := readString(d)
		if ok {
			b.ops = append(b.ops, tdOp{kind: tdTradingDay, s: s})
		}
		return err
	case "trade_more_data":
		v, ok, err := readBool(d)
		if !ok {
			v = true // 缺省按仍未完成处理
		}
		b.ops = append(b.ops, tdOp{kind: tdMoreData, b: v})
		return err
	case "position_ready":
		v, ok, err := readBool(d)
		if ok {
			b.ops = append(b.ops, tdOp{kind: tdPositionReady, b: v})
		}
		return err
	}
	// banks / transfers / pre_insert_orders 等首版不暴露的分支：校验后跳过。
	return d.SkipValue()
}

func decodeEntries[T any, F ~uint16](d *jsontext.Decoder, b *tdBatch, kind tdKind, sc *schema[T, F]) error {
	return readObjectOrNull(d, func() {}, func(key string) error {
		if d.PeekKind() == jsontext.KindNull {
			d.ReadToken()
			b.ops = append(b.ops, tdOp{kind: kind, key: key, del: true})
			return nil
		}
		p := &patch[T, F]{}
		if err := sc.decodeObject(d, p); err != nil {
			return fmt.Errorf("%s.%w", key, err)
		}
		b.ops = append(b.ops, tdOp{kind: kind, key: key, p: p})
		return nil
	})
}
