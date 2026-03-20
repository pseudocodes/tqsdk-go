package infra

import (
	"github.com/bytedance/sonic"
	"github.com/tidwall/gjson"
)

func RouteAID(payload []byte) string {
	return gjson.GetBytes(payload, "aid").String()
}

func RouteChartID(payload []byte) string {
	return gjson.GetBytes(payload, "chart_id").String()
}

func DecodeJSON(payload []byte, out any) error {
	return sonic.Unmarshal(payload, out)
}

func EncodeJSON(v any) ([]byte, error) {
	return sonic.Marshal(v)
}
