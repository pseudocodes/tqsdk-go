// Package symbols 提供回测使用的合约资料：Python 报价风格的资料 JSON 与已下市合约表。
//
// expired_quotes.json.gz 由 gen.py 从 tqsdk-python 3.10.4 的 expired_quotes.json.lzma 转换而来
// （Apache-2.0，来源与校验和见 SOURCE、NOTICE），包含 2016-01 至 2020-09 下市的 14,246 个合约。
package symbols

// 重新生成：python3 gen.py <tqsdk-python>/tqsdk/expired_quotes.json.lzma

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json/v2"
	"sync"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// Info 是一个合约的资料，键名与 Python Quote 的合约资料字段相同；指针为 nil 表示缺省。
type Info struct {
	InstrumentID         string   `json:"instrument_id"` // 完整代码，如 SHFE.rb2001
	ExchangeID           string   `json:"exchange_id,omitempty"`
	ProductID            string   `json:"product_id,omitempty"`
	InsClass             string   `json:"ins_class,omitempty"`
	PriceTick            *float64 `json:"price_tick,omitempty"`
	PriceDecs            *int64   `json:"price_decs,omitempty"`
	VolumeMultiple       *float64 `json:"volume_multiple,omitempty"`
	Margin               *float64 `json:"margin,omitempty"`
	Commission           *float64 `json:"commission,omitempty"`
	MaxLimitOrderVolume  *int64   `json:"max_limit_order_volume,omitempty"`
	MaxMarketOrderVolume *int64   `json:"max_market_order_volume,omitempty"`
	MinLimitOrderVolume  *int64   `json:"min_limit_order_volume,omitempty"`
	MinMarketOrderVolume *int64   `json:"min_market_order_volume,omitempty"`
	ExpireDatetime       *float64 `json:"expire_datetime,omitempty"` // Unix 秒
	DeliveryYear         *int64   `json:"delivery_year,omitempty"`
	DeliveryMonth        *int64   `json:"delivery_month,omitempty"`
	UnderlyingSymbol     string   `json:"underlying_symbol,omitempty"`
	OptionClass          string   `json:"option_class,omitempty"`
	TradingTime          *struct {
		Day   [][]string `json:"day,omitempty"`
		Night [][]string `json:"night,omitempty"`
	} `json:"trading_time,omitempty"`
}

// Instrument 转换为 model.Instrument，只为出现的字段设置有效位。
func (s Info) Instrument() model.Instrument {
	var ins model.Instrument
	v := &ins.Valid
	ins.Symbol, ins.InstrumentID = s.InstrumentID, s.InstrumentID
	v.Add(model.InstrumentInstrumentID)
	str := func(src string, dst *string, f model.InstrumentField) {
		if src != "" {
			*dst = src
			v.Add(f)
		}
	}
	f64 := func(src *float64, dst *float64, f model.InstrumentField) {
		if src != nil {
			*dst = *src
			v.Add(f)
		}
	}
	i64 := func(src *int64, dst *int64, f model.InstrumentField) {
		if src != nil {
			*dst = *src
			v.Add(f)
		}
	}
	str(s.ExchangeID, &ins.ExchangeID, model.InstrumentExchangeID)
	str(s.ProductID, &ins.ProductID, model.InstrumentProductID)
	str(s.InsClass, &ins.InsClass, model.InstrumentInsClass)
	str(s.UnderlyingSymbol, &ins.UnderlyingSymbol, model.InstrumentUnderlyingSymbol)
	str(s.OptionClass, &ins.OptionClass, model.InstrumentOptionClass)
	f64(s.PriceTick, &ins.PriceTick, model.InstrumentPriceTick)
	f64(s.Margin, &ins.Margin, model.InstrumentMargin)
	f64(s.Commission, &ins.Commission, model.InstrumentCommission)
	f64(s.ExpireDatetime, &ins.ExpireDatetime, model.InstrumentExpireDatetime)
	i64(s.PriceDecs, &ins.PriceDecs, model.InstrumentPriceDecs)
	i64(s.MaxLimitOrderVolume, &ins.MaxLimitOrderVolume, model.InstrumentMaxLimitOrderVolume)
	i64(s.MaxMarketOrderVolume, &ins.MaxMarketOrderVolume, model.InstrumentMaxMarketOrderVolume)
	i64(s.MinLimitOrderVolume, &ins.MinLimitOrderVolume, model.InstrumentMinLimitOrderVolume)
	i64(s.MinMarketOrderVolume, &ins.MinMarketOrderVolume, model.InstrumentMinMarketOrderVolume)
	i64(s.DeliveryYear, &ins.DeliveryYear, model.InstrumentDeliveryYear)
	i64(s.DeliveryMonth, &ins.DeliveryMonth, model.InstrumentDeliveryMonth)
	if s.VolumeMultiple != nil {
		ins.VolumeMultiple = int64(*s.VolumeMultiple)
		v.Add(model.InstrumentVolumeMultiple)
	}
	if s.TradingTime != nil {
		ins.TradingTime = model.TradingTime{Day: s.TradingTime.Day, Night: s.TradingTime.Night}
	}
	return ins
}

// FromInstrument 把合约资料转为 Info（写数据集时使用），只保留有效字段。
func FromInstrument(ins model.Instrument) Info {
	s := Info{InstrumentID: ins.Symbol}
	has := ins.Valid.Has
	if has(model.InstrumentExchangeID) {
		s.ExchangeID = ins.ExchangeID
	}
	if has(model.InstrumentProductID) {
		s.ProductID = ins.ProductID
	}
	if has(model.InstrumentInsClass) {
		s.InsClass = ins.InsClass
	}
	if has(model.InstrumentUnderlyingSymbol) {
		s.UnderlyingSymbol = ins.UnderlyingSymbol
	}
	if has(model.InstrumentOptionClass) {
		s.OptionClass = ins.OptionClass
	}
	f := func(ok bool, x float64) *float64 {
		if !ok {
			return nil
		}
		return &x
	}
	i := func(ok bool, x int64) *int64 {
		if !ok {
			return nil
		}
		return &x
	}
	s.PriceTick = f(has(model.InstrumentPriceTick), ins.PriceTick)
	s.PriceDecs = i(has(model.InstrumentPriceDecs), ins.PriceDecs)
	s.VolumeMultiple = f(has(model.InstrumentVolumeMultiple), float64(ins.VolumeMultiple))
	s.Margin = f(has(model.InstrumentMargin), ins.Margin)
	s.Commission = f(has(model.InstrumentCommission), ins.Commission)
	s.ExpireDatetime = f(has(model.InstrumentExpireDatetime), ins.ExpireDatetime)
	s.DeliveryYear = i(has(model.InstrumentDeliveryYear), ins.DeliveryYear)
	s.DeliveryMonth = i(has(model.InstrumentDeliveryMonth), ins.DeliveryMonth)
	s.MaxLimitOrderVolume = i(has(model.InstrumentMaxLimitOrderVolume), ins.MaxLimitOrderVolume)
	s.MaxMarketOrderVolume = i(has(model.InstrumentMaxMarketOrderVolume), ins.MaxMarketOrderVolume)
	s.MinLimitOrderVolume = i(has(model.InstrumentMinLimitOrderVolume), ins.MinLimitOrderVolume)
	s.MinMarketOrderVolume = i(has(model.InstrumentMinMarketOrderVolume), ins.MinMarketOrderVolume)
	if len(ins.TradingTime.Day)+len(ins.TradingTime.Night) > 0 {
		s.TradingTime = &struct {
			Day   [][]string `json:"day,omitempty"`
			Night [][]string `json:"night,omitempty"`
		}{ins.TradingTime.Day, ins.TradingTime.Night}
	}
	return s
}

//go:embed expired_quotes.json.gz
var expiredGz []byte

var expired = sync.OnceValue(func() map[string]Info {
	zr, err := gzip.NewReader(bytes.NewReader(expiredGz))
	if err != nil {
		panic(err) // 嵌入文件由 gen.py 生成，损坏即构建错误
	}
	var m map[string]Info
	if err := json.UnmarshalRead(zr, &m); err != nil {
		panic(err)
	}
	for sym, s := range m { // 同 Python 回测的 _quotes_add_night：补齐文件中缺少的夜盘时段
		if night, ok := nightTable[s.ExchangeID+"."+s.ProductID]; ok && s.TradingTime != nil && len(s.TradingTime.Night) == 0 {
			s.TradingTime.Night = [][]string{night[:]}
			m[sym] = s
		}
	}
	return m
})

// Expired 返回已下市合约（2020-09 之前）的资料。
func Expired(symbol string) (model.Instrument, bool) {
	s, ok := expired()[symbol]
	if !ok {
		return model.Instrument{}, false
	}
	return s.Instrument(), true
}

// nightTable 对应 Python tqsdk/utils.py 的 night_trading_table。
var nightTable = map[string][2]string{
	"DCE.a": {"21:00:00", "23:00:00"}, "DCE.b": {"21:00:00", "23:00:00"}, "DCE.c": {"21:00:00", "23:00:00"},
	"DCE.cs": {"21:00:00", "23:00:00"}, "DCE.m": {"21:00:00", "23:00:00"}, "DCE.y": {"21:00:00", "23:00:00"},
	"DCE.p": {"21:00:00", "23:00:00"}, "DCE.l": {"21:00:00", "23:00:00"}, "DCE.v": {"21:00:00", "23:00:00"},
	"DCE.pp": {"21:00:00", "23:00:00"}, "DCE.j": {"21:00:00", "23:00:00"}, "DCE.jm": {"21:00:00", "23:00:00"},
	"DCE.i": {"21:00:00", "23:00:00"}, "DCE.eg": {"21:00:00", "23:00:00"}, "DCE.eb": {"21:00:00", "23:00:00"},
	"DCE.rr": {"21:00:00", "23:00:00"}, "DCE.pg": {"21:00:00", "23:00:00"},
	"CZCE.CF": {"21:00:00", "23:00:00"}, "CZCE.CY": {"21:00:00", "23:00:00"}, "CZCE.SA": {"21:00:00", "23:00:00"},
	"CZCE.SR": {"21:00:00", "23:00:00"}, "CZCE.TA": {"21:00:00", "23:00:00"}, "CZCE.OI": {"21:00:00", "23:00:00"},
	"CZCE.MA": {"21:00:00", "23:00:00"}, "CZCE.FG": {"21:00:00", "23:00:00"}, "CZCE.RM": {"21:00:00", "23:00:00"},
	"CZCE.ZC": {"21:00:00", "23:00:00"}, "CZCE.TC": {"21:00:00", "23:00:00"},
	"SHFE.rb": {"21:00:00", "23:00:00"}, "SHFE.hc": {"21:00:00", "23:00:00"}, "SHFE.fu": {"21:00:00", "23:00:00"},
	"SHFE.bu": {"21:00:00", "23:00:00"}, "SHFE.ru": {"21:00:00", "23:00:00"}, "SHFE.sp": {"21:00:00", "23:00:00"},
	"INE.nr":  {"21:00:00", "23:00:00"},
	"SHFE.cu": {"21:00:00", "25:00:00"}, "SHFE.al": {"21:00:00", "25:00:00"}, "SHFE.zn": {"21:00:00", "25:00:00"},
	"SHFE.pb": {"21:00:00", "25:00:00"}, "SHFE.ni": {"21:00:00", "25:00:00"}, "SHFE.sn": {"21:00:00", "25:00:00"},
	"SHFE.ss": {"21:00:00", "25:00:00"},
	"SHFE.au": {"21:00:00", "26:30:00"}, "SHFE.ag": {"21:00:00", "26:30:00"}, "INE.sc": {"21:00:00", "26:30:00"},
}
