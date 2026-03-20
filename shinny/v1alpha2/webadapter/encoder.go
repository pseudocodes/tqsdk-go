package webadapter

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
)

func EncodeAsTqWebDiff(event any) (map[string]any, bool) {
	switch ev := event.(type) {
	case api.QuoteEvent:
		q := structToMap(ev.Quote)
		symbol := ev.Quote.Symbol
		if symbol == "" {
			symbol = joinSymbol(ev.Quote.ExchangeID, ev.Quote.InstrumentID)
		}
		if symbol == "" {
			return nil, false
		}
		return map[string]any{"quotes": map[string]any{symbol: q}}, true
	case api.TickEvent:
		tick := structToMap(ev.Tick)
		return map[string]any{"ticks": map[string]any{ev.Symbol: map[string]any{"last_id": ev.Tick.ID, "serial_ready": ev.SerialReady, "data": tick}}}, true
	case api.KlineEvent:
		sym := ev.Key.MainSymbol
		dur := strconv.FormatInt(ev.Key.Duration.Nanoseconds(), 10)
		kline := structToMap(ev.Row.Main)
		return map[string]any{"klines": map[string]any{sym: map[string]any{dur: map[string]any{"last_id": ev.Row.Main.ID, "serial_ready": ev.SerialReady, "data": kline}}}}, true
	case api.AccountEvent:
		acc := structToMap(ev.Account)
		return map[string]any{"trade": map[string]any{ev.AccountID: map[string]any{"accounts": map[string]any{"CNY": acc}}}}, true
	case api.PositionEvent:
		pos := structToMap(ev.Position)
		return map[string]any{"trade": map[string]any{ev.AccountID: map[string]any{"positions": map[string]any{ev.Symbol: pos}}}}, true
	case api.OrderEvent:
		ord := structToMap(ev.Order)
		return map[string]any{"trade": map[string]any{ev.AccountID: map[string]any{"orders": map[string]any{ev.OrderID: ord}}}}, true
	case api.TradeFillEvent:
		trd := structToMap(ev.Trade)
		return map[string]any{"trade": map[string]any{ev.AccountID: map[string]any{"trades": map[string]any{ev.TradeID: trd}}}}, true
	case api.NotifyEvent:
		n := structToMap(ev)
		id := fmt.Sprintf("%d", ev.ArrivedAt.UnixNano())
		if ev.ArrivedAt.IsZero() {
			id = fmt.Sprintf("%d", time.Now().UnixNano())
		}
		return map[string]any{"notify": map[string]any{id: n}}, true
	case api.ConnEvent:
		status := false
		if ev.State == "ready" {
			status = true
		}
		return map[string]any{"action": map[string]any{"md_url_status": status}}, true
	case map[string]any:
		return cloneAnyMap(ev), true
	default:
		return nil, false
	}
}

func Encode(event any) (Message, bool) {
	diff, ok := EncodeAsTqWebDiff(event)
	if !ok {
		return Message{}, false
	}
	return Message{Aid: "rtn_data", Data: []map[string]any{diff}, TS: time.Now().UnixNano()}, true
}

func structToMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func joinSymbol(exchangeID, instrumentID string) string {
	if exchangeID == "" {
		return instrumentID
	}
	if instrumentID == "" {
		return exchangeID
	}
	return exchangeID + "." + instrumentID
}

func cloneAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		if nested, ok := v.(map[string]any); ok {
			out[k] = cloneAnyMap(nested)
			continue
		}
		out[k] = v
	}
	return out
}
