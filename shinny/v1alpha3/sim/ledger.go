package sim

import (
	"math"
	"slices"
	"strconv"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/internal/wire"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// Quote 是撮合使用的一档盘口。
type Quote struct {
	AskPrice1 float64
	BidPrice1 float64
	LastPrice float64
}

// InsertOrder 是一笔已被 core 接受的报单（insert_order 报文）。
type InsertOrder struct {
	UserID          string  `json:"user_id"`
	OrderID         string  `json:"order_id"`
	ExchangeID      string  `json:"exchange_id"`
	InstrumentID    string  `json:"instrument_id"`
	Direction       string  `json:"direction"`
	Offset          string  `json:"offset"`
	Volume          int64   `json:"volume"`
	PriceType       string  `json:"price_type"`
	LimitPrice      float64 `json:"limit_price"`
	VolumeCondition string  `json:"volume_condition"`
	TimeCondition   string  `json:"time_condition"`
}

type order struct {
	model.Order
	frozenMargin float64
}

type position struct {
	model.Position
	futureMargin float64 // 计算保证金使用的每手保证金
}

// Ledger 是一个模拟账户的账本。所有方法在回测驱动的推进 goroutine 中调用；
// 变化以 Flush 返回的交易 diff 交给 core 安装，策略只读 core 中已提交的镜像。
type Ledger struct {
	cfg    Config
	acc    model.Account
	pos    map[string]*position
	book   map[string][]*order // 按合约，接受顺序
	infos  map[string]model.Instrument
	quotes map[string]Quote
	trades []model.Trade // 当前交易日的成交
	days   []DayRecord

	dirtyAcc    bool
	dirtyPos    []string
	dirtyOrders []*order
	newTrades   []model.Trade
	notifies    *wire.Object
	notifySeq   int
	elements    []any
}

// NewLedger 创建账本。
func NewLedger(cfg Config) *Ledger {
	b := cfg.InitBalance
	return &Ledger{
		cfg: cfg,
		acc: model.Account{Currency: "CNY", PreBalance: b, StaticBalance: b, Balance: b, Available: b,
			CtpBalance: math.NaN(), CtpAvailable: math.NaN()},
		pos: map[string]*position{}, book: map[string][]*order{},
		infos: map[string]model.Instrument{}, quotes: map[string]Quote{},
	}
}

// ID 返回账户身份（交易报文中的用户键）。
func (l *Ledger) ID() string { return l.cfg.ID }

// Pending 报告是否有尚未 Flush 的变化。
func (l *Ledger) Pending() bool {
	return l.dirtyAcc || len(l.dirtyPos) > 0 || len(l.dirtyOrders) > 0 || len(l.newTrades) > 0 ||
		l.notifies != nil || len(l.elements) > 0
}

// Init 记录初始截面与交易日，交付后账户 Ready。
func (l *Ledger) Init(tradingDay string) {
	node := new(wire.Object).
		Set("session", new(wire.Object).Set("trading_day", tradingDay)).
		Set("accounts", new(wire.Object).Set("CNY", wire.Fields(&l.acc, accountFields))).
		Set("positions", new(wire.Object)).Set("orders", new(wire.Object)).Set("trades", new(wire.Object)).
		Set("trade_more_data", false)
	l.elements = append(l.elements, l.wrap(node))
}

// SetInstrument 登记合约资料；用户覆盖的保证金与手续费优先。
func (l *Ledger) SetInstrument(symbol string, info model.Instrument) {
	if v, ok := l.cfg.Margins[symbol]; ok {
		info.Margin = v
		info.Valid.Add(model.InstrumentMargin)
	}
	if v, ok := l.cfg.Commissions[symbol]; ok {
		info.Commission = v
		info.Valid.Add(model.InstrumentCommission)
	}
	l.infos[symbol] = info
}

// Active 报告账户是否已跟踪该合约（报过单）；只有跟踪的合约需要逐笔行情撮合与估值。
func (l *Ledger) Active(symbol string) bool { return l.pos[symbol] != nil }

// OnQuote 处理一次盘口更新：先撮合活动委托，再重估持仓（Python update_quotes）。
func (l *Ledger) OnQuote(now int64, symbol string, q Quote) {
	info := l.infos[symbol]
	if info.InsClass == "INDEX" { // 指数行情没有盘口：以最新价 ± price_tick 撮合（Python _get_price_range）
		if math.IsNaN(q.AskPrice1) {
			q.AskPrice1 = q.LastPrice + info.PriceTick
		}
		if math.IsNaN(q.BidPrice1) {
			q.BidPrice1 = q.LastPrice - info.PriceTick
		}
	}
	l.quotes[symbol] = q
	p := l.pos[symbol]
	if p == nil || math.IsNaN(q.LastPrice) { // 无效行情直接跳过
		return
	}
	for _, o := range slices.Clone(l.book[symbol]) {
		l.match(now, o, symbol, p, info, q)
	}
	fm := futureMargin(info)
	if p.VolumeLong > 0 || p.VolumeShort > 0 {
		if p.LastPrice != q.LastPrice || math.IsNaN(fm) || fm != p.futureMargin {
			l.adjustPositionAccount(symbol, info, p.LastPrice, q.LastPrice, 0, 0, 0, 0)
			p.futureMargin, p.LastPrice = fm, q.LastPrice
			l.markPos(symbol)
		}
	} else if p.LastPrice != q.LastPrice || p.futureMargin != fm {
		p.futureMargin, p.LastPrice = fm, q.LastPrice
		l.markPos(symbol)
	}
}

// Insert 处理报单。调用前该合约须已有合约资料与有效行情（Python _ensure_quote）。
func (l *Ledger) Insert(now int64, in InsertOrder, inTradingTime bool) {
	symbol := in.ExchangeID + "." + in.InstrumentID
	info, q := l.infos[symbol], l.quotes[symbol]
	p := l.ensurePosition(symbol, q)
	o := &order{Order: model.Order{UserID: in.UserID, OrderID: in.OrderID, ExchangeOrderID: in.OrderID,
		ExchangeID: in.ExchangeID, InstrumentID: in.InstrumentID, Direction: model.Direction(in.Direction),
		Offset: model.Offset(in.Offset), VolumeOriginal: in.Volume, VolumeLeft: in.Volume, LimitPrice: in.LimitPrice,
		PriceType: in.PriceType, VolumeCondition: in.VolumeCondition, TimeCondition: in.TimeCondition,
		InsertDateTime: now, LastMsg: "报单成功", Status: model.OrderAlive}}
	l.markOrder(o)
	if msg := l.check(o, p, info, inTradingTime); msg != "" {
		o.LastMsg, o.Status, o.frozenMargin = msg, model.OrderFinished, 0
		return
	}
	l.book[symbol] = append(l.book[symbol], o)
	l.onInsert(o, symbol, p)
	l.match(now, o, symbol, p, info, q)
}

// Cancel 处理撤单；未知或已结束的委托只发布一条通知。
func (l *Ledger) Cancel(orderID string) {
	for symbol, list := range l.book {
		for _, o := range list {
			if o.OrderID == orderID {
				o.LastMsg, o.Status = "已撤单", model.OrderFinished
				l.onFailed(o, symbol)
				l.markOrder(o)
				l.removeOrder(symbol, o)
				return
			}
		}
	}
	l.notifySeq++
	if l.notifies == nil {
		l.notifies = new(wire.Object)
	}
	l.notifies.Set("TQGO_sim_"+strconv.Itoa(l.notifySeq), new(wire.Object).
		Set("type", "MESSAGE").Set("level", "WARNING").Set("code", int64(0)).
		Set("content", "撤单失败: 委托单 "+orderID+" 不存在或已结束"))
}

// Reject 直接以 FINISHED 结束一笔无法处理的报单（如回测已结束）。
func (l *Ledger) Reject(now int64, in InsertOrder, msg string) {
	l.markOrder(&order{Order: model.Order{UserID: in.UserID, OrderID: in.OrderID, ExchangeID: in.ExchangeID,
		InstrumentID: in.InstrumentID, Direction: model.Direction(in.Direction), Offset: model.Offset(in.Offset),
		VolumeOriginal: in.Volume, VolumeLeft: in.Volume, LimitPrice: in.LimitPrice, PriceType: in.PriceType,
		VolumeCondition: in.VolumeCondition, TimeCondition: in.TimeCondition, InsertDateTime: now,
		LastMsg: msg, Status: model.OrderFinished}})
}

// check 返回拒单原因；空串表示可以进入委托簿（Python _check_insert_order，期货部分）。
func (l *Ledger) check(o *order, p *position, info model.Instrument, inTradingTime bool) string {
	switch {
	case o.PriceType != "LIMIT" || o.TimeCondition != "GFD" || o.VolumeCondition != "ANY":
		return "模拟交易暂不支持 FAK/FOK/市价单"
	case !tradable(info):
		return "模拟交易暂不支持该合约类型"
	case math.IsNaN(futureMargin(info)) || math.IsNaN(commission(info)):
		return "不支持的合约类型，TqSim 目前不支持组合，股票，etf期权模拟交易"
	case !inTradingTime:
		return "下单失败, 不在可交易时间段内"
	}
	v := o.VolumeOriginal
	buy := o.Direction == model.DirectionBuy
	if o.Offset != model.OffsetOpen {
		var avail int64
		msg := "平仓手数不足"
		switch {
		case shfeLike(o.ExchangeID) && o.Offset == model.OffsetCloseToday:
			msg = "平今仓手数不足"
			avail = pick(buy, p.VolumeShortToday-p.VolumeShortFrozenToday, p.VolumeLongToday-p.VolumeLongFrozenToday)
		case shfeLike(o.ExchangeID):
			msg = "平昨仓手数不足"
			avail = pick(buy, p.VolumeShortHis-p.VolumeShortFrozenHis, p.VolumeLongHis-p.VolumeLongFrozenHis)
		default:
			avail = pick(buy, p.VolumeShort-p.VolumeShortFrozen, p.VolumeLong-p.VolumeLongFrozen)
		}
		if avail < v {
			return msg
		}
		return ""
	}
	o.frozenMargin = float64(v) * futureMargin(info)
	if o.frozenMargin > l.acc.Available {
		return "开仓资金不足"
	}
	return ""
}

// onInsert 冻结开仓保证金或平仓手数（Python _on_insert_order）。
func (l *Ledger) onInsert(o *order, symbol string, p *position) {
	if o.Offset == model.OffsetOpen {
		l.acc.FrozenMargin += o.frozenMargin
		l.acc.Available -= o.frozenMargin
		l.dirtyAcc = true
		return
	}
	v := o.VolumeOriginal
	long := o.Direction == model.DirectionSell // 卖平平多头
	today, his, hisAvail := &p.VolumeShortFrozenToday, &p.VolumeShortFrozenHis, p.VolumeShortHis-p.VolumeShortFrozenHis
	if long {
		today, his, hisAvail = &p.VolumeLongFrozenToday, &p.VolumeLongFrozenHis, p.VolumeLongHis-p.VolumeLongFrozenHis
	}
	switch {
	case shfeLike(o.ExchangeID) && o.Offset == model.OffsetCloseToday:
		*today += v
	case shfeLike(o.ExchangeID):
		*his += v
	case hisAvail < v:
		*his += hisAvail
		*today += v - hisAvail
	default:
		*his += v
	}
	p.VolumeLongFrozen = p.VolumeLongFrozenToday + p.VolumeLongFrozenHis
	p.VolumeShortFrozen = p.VolumeShortFrozenToday + p.VolumeShortFrozenHis
	l.markPos(symbol)
}

// match 按 PythonCompatible 规则撮合一笔活动委托（Python match_order）。
func (l *Ledger) match(now int64, o *order, symbol string, p *position, info model.Instrument, q Quote) {
	price := o.LimitPrice
	if !(o.Direction == model.DirectionBuy && price >= q.AskPrice1 || o.Direction == model.DirectionSell && price <= q.BidPrice1) {
		return
	}
	o.LastMsg, o.Status = "全部成交", model.OrderFinished
	t := model.Trade{UserID: o.UserID, OrderID: o.OrderID, TradeID: o.OrderID + "|" + strconv.FormatInt(o.VolumeLeft, 10),
		ExchangeTradeID: o.OrderID + "|" + strconv.FormatInt(o.VolumeLeft, 10), ExchangeID: o.ExchangeID,
		InstrumentID: o.InstrumentID, Direction: o.Direction, Offset: o.Offset, Price: price, Volume: o.VolumeLeft,
		TradeDateTime: now, Commission: float64(o.VolumeLeft) * commission(info)}
	l.trades = append(l.trades, t)
	l.newTrades = append(l.newTrades, t)
	if math.IsNaN(p.LastPrice) {
		p.LastPrice = price
	}
	l.onTraded(o, t, symbol, p, info)
	l.markOrder(o)
	l.removeOrder(symbol, o)
}

// onTraded 是 Python _on_order_traded 的期货部分。
func (l *Ledger) onTraded(o *order, t model.Trade, symbol string, p *position, info model.Instrument) {
	frozen := o.frozenMargin
	o.frozenMargin, o.VolumeLeft = 0, 0
	v, mult := o.VolumeOriginal, float64(info.VolumeMultiple)
	amount := t.Price * float64(v) * mult
	if o.Offset == model.OffsetOpen {
		if o.Direction == model.DirectionBuy {
			p.VolumeLongToday += v
			p.OpenCostLong += amount
			p.PositionCostLong += amount
		} else {
			p.VolumeShortToday += v
			p.OpenCostShort += amount
			p.PositionCostShort += amount
		}
		l.acc.FrozenMargin -= frozen
		l.acc.Available += frozen
		l.adjustAccountByTrade(t.Commission, 0)
		buyOpen, sellOpen := pick(o.Direction == model.DirectionBuy, v, 0), pick(o.Direction == model.DirectionBuy, 0, v)
		l.adjustPositionAccount(symbol, info, t.Price, p.LastPrice, buyOpen, 0, sellOpen, 0)
	} else {
		long := o.Direction == model.DirectionSell // 卖平平多头
		volToday, volHis := &p.VolumeShortToday, &p.VolumeShortHis
		fToday, fHis := &p.VolumeShortFrozenToday, &p.VolumeShortFrozenHis
		if long {
			volToday, volHis = &p.VolumeLongToday, &p.VolumeLongHis
			fToday, fHis = &p.VolumeLongFrozenToday, &p.VolumeLongFrozenHis
		}
		switch {
		case shfeLike(o.ExchangeID) && o.Offset == model.OffsetCloseToday:
			*fToday -= v
			*volToday -= v
		case shfeLike(o.ExchangeID):
			*fHis -= v
			*volHis -= v
		case *fHis >= v:
			*fHis -= v
			*volHis -= v
		default:
			*fToday -= v - *fHis
			*volToday -= v - *fHis
			*volHis -= *fHis
			*fHis = 0
		}
		var closeProfit float64
		if long {
			p.OpenCostLong -= p.OpenPriceLong * float64(v) * mult
			p.PositionCostLong -= p.PositionPriceLong * float64(v) * mult
			closeProfit = (t.Price - p.PositionPriceLong) * float64(v) * mult
		} else {
			p.OpenCostShort -= p.OpenPriceShort * float64(v) * mult
			p.PositionCostShort -= p.PositionPriceShort * float64(v) * mult
			closeProfit = (p.PositionPriceShort - t.Price) * float64(v) * mult
		}
		l.adjustAccountByTrade(t.Commission, closeProfit)
		buyClose, sellClose := pick(long, 0, v), pick(long, v, 0)
		l.adjustPositionAccount(symbol, info, p.LastPrice, 0, 0, buyClose, 0, sellClose)
	}
	l.markPos(symbol)
	l.dirtyAcc = true
}

// onFailed 释放未成交委托的冻结（Python _on_order_failed）。
func (l *Ledger) onFailed(o *order, symbol string) {
	frozen := o.frozenMargin
	o.frozenMargin = 0
	if o.Offset == model.OffsetOpen {
		l.acc.FrozenMargin -= frozen
		l.acc.Available += frozen
		l.dirtyAcc = true
		return
	}
	p := l.pos[symbol]
	v := o.VolumeOriginal
	today, his := &p.VolumeShortFrozenToday, &p.VolumeShortFrozenHis
	if o.Direction == model.DirectionSell {
		today, his = &p.VolumeLongFrozenToday, &p.VolumeLongFrozenHis
	}
	switch {
	case shfeLike(o.ExchangeID) && o.Offset == model.OffsetCloseToday:
		*today -= v
	case shfeLike(o.ExchangeID):
		*his -= v
	case *today >= v:
		*today -= v
	default:
		*his -= v - *today
		*today = 0
	}
	p.VolumeLongFrozen = p.VolumeLongFrozenToday + p.VolumeLongFrozenHis
	p.VolumeShortFrozen = p.VolumeShortFrozenToday + p.VolumeShortFrozenHis
	l.markPos(symbol)
}

// adjustPositionAccount 按价格变化或一次开平计算持仓与账户的差值（Python _adjust_position_account，期货部分）。
func (l *Ledger) adjustPositionAccount(symbol string, info model.Instrument, pre, last float64, buyOpen, buyClose, sellOpen, sellClose int64) {
	p := l.pos[symbol]
	mult, m := float64(info.VolumeMultiple), futureMargin(info)
	var fpl, fps, ppl, pps, ml, ms float64
	switch {
	case buyOpen > 0:
		fpl = (last - pre) * float64(buyOpen) * mult
		ml, ppl = float64(buyOpen)*m, fpl
	case sellClose > 0:
		fpl = -p.FloatProfitLong / float64(p.VolumeLong) * float64(sellClose)
		ml = -float64(sellClose) * m
		ppl = -p.PositionProfitLong / float64(p.VolumeLong) * float64(sellClose)
	case sellOpen > 0:
		fps = (pre - last) * float64(sellOpen) * mult
		ms, pps = float64(sellOpen)*m, fps
	case buyClose > 0:
		fps = -p.FloatProfitShort / float64(p.VolumeShort) * float64(buyClose)
		ms = -float64(buyClose) * m
		pps = -p.PositionProfitShort / float64(p.VolumeShort) * float64(buyClose)
	default:
		fpl = (last - pre) * float64(p.VolumeLong) * mult
		fps = (pre - last) * float64(p.VolumeShort) * mult
		ppl, pps = fpl, fps
		ml = m*float64(p.VolumeLong) - p.MarginLong
		ms = m*float64(p.VolumeShort) - p.MarginShort
	}
	if buyOpen+buyClose+sellOpen+sellClose > 0 {
		p.PosLongToday, p.PosLongHis = p.VolumeLongToday, p.VolumeLongHis
		p.PosShortToday, p.PosShortHis = p.VolumeShortToday, p.VolumeShortHis
		p.VolumeLong = p.VolumeLongToday + p.VolumeLongHis
		p.VolumeShort = p.VolumeShortToday + p.VolumeShortHis
		p.VolumeLongFrozen = p.VolumeLongFrozenToday + p.VolumeLongFrozenHis
		p.VolumeShortFrozen = p.VolumeShortFrozenToday + p.VolumeShortFrozenHis
	}
	p.FloatProfitLong += fpl
	p.FloatProfitShort += fps
	p.PositionProfitLong += ppl
	p.PositionProfitShort += pps
	p.MarginLong += ml
	p.MarginShort += ms
	p.OpenPriceLong, p.PositionPriceLong = avgPrice(p.OpenCostLong, p.VolumeLong, mult), avgPrice(p.PositionCostLong, p.VolumeLong, mult)
	p.OpenPriceShort, p.PositionPriceShort = avgPrice(p.OpenCostShort, p.VolumeShort, mult), avgPrice(p.PositionCostShort, p.VolumeShort, mult)
	p.FloatProfit = p.FloatProfitLong + p.FloatProfitShort
	p.PositionProfit = p.PositionProfitLong + p.PositionProfitShort
	p.Margin = p.MarginLong + p.MarginShort

	a := &l.acc
	a.FloatProfit += fpl + fps
	a.PositionProfit += ppl + pps
	a.Balance += ppl + pps
	a.Margin = l.accountMargin()
	a.Available = a.Balance - a.Margin - a.FrozenMargin - a.FrozenPremium - a.MarketValue
	a.RiskRatio = riskRatio(a)
	l.dirtyAcc = true
}

// accountMargin 按 交易所.品种 取多空较大一边汇总期货保证金（Python _adjust_account_by_position）。
func (l *Ledger) accountMargin() float64 {
	type side struct{ long, short float64 }
	var keys []string
	products := map[string]*side{}
	var other float64
	for _, symbol := range l.symbols() { // 固定求和顺序，保证结果逐位确定
		p, info := l.pos[symbol], l.infos[symbol]
		if info.InsClass != "FUTURE" || info.ProductID == "" {
			other += p.Margin
			continue
		}
		key := p.ExchangeID + "." + info.ProductID
		s := products[key]
		if s == nil {
			s = &side{}
			products[key] = s
			keys = append(keys, key)
		}
		s.long += p.MarginLong
		s.short += p.MarginShort
	}
	for _, k := range keys {
		other += max(products[k].long, products[k].short)
	}
	return other
}

// symbols 返回按代码排序的持仓合约。
func (l *Ledger) symbols() []string {
	out := make([]string, 0, len(l.pos))
	for s := range l.pos {
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

func (l *Ledger) adjustAccountByTrade(commission, closeProfit float64) {
	a := &l.acc
	a.CloseProfit += closeProfit
	a.Commission += commission
	a.Balance += closeProfit - commission
	a.Available += closeProfit - commission
	a.RiskRatio = riskRatio(a)
	l.dirtyAcc = true
}

// Settle 结算当前交易日并切换到 nextDay：记录当日账户截面，撤销全部活动委托，按最新价逐日盯市，
// 今仓并入昨仓（Python settle / _on_settle）。date 为结算日记录的日期（YYYY-MM-DD）。
func (l *Ledger) Settle(date, nextDay string) {
	rec := DayRecord{Date: date, Account: l.acc, Positions: map[string]model.Position{}, Trades: l.trades}
	for symbol, p := range l.pos {
		rec.Positions[symbol] = p.Position
	}
	l.days = append(l.days, rec)
	l.trades = nil

	for _, symbol := range l.symbols() {
		for _, o := range l.book[symbol] {
			o.frozenMargin = 0
			o.LastMsg, o.Status = "交易日结束，自动撤销当日有效的委托单（GFD）", model.OrderFinished
			l.markOrder(o)
		}
		delete(l.book, symbol)
	}
	a := &l.acc
	a.PreBalance = a.Balance - a.MarketValue
	a.CloseProfit, a.Commission, a.Premium, a.FrozenMargin, a.FrozenPremium = 0, 0, 0, 0, 0
	a.StaticBalance, a.PositionProfit = a.PreBalance, 0
	a.RiskRatio = riskRatio(a)
	a.Available = a.StaticBalance - a.Margin
	l.dirtyAcc = true
	for _, symbol := range l.symbols() {
		p, mult := l.pos[symbol], float64(l.infos[symbol].VolumeMultiple)
		p.VolumeLongFrozenToday, p.VolumeLongFrozenHis, p.VolumeShortFrozenToday, p.VolumeShortFrozenHis = 0, 0, 0, 0
		p.VolumeLongToday, p.VolumeLongHis = 0, p.VolumeLong
		p.VolumeShortToday, p.VolumeShortHis = 0, p.VolumeShort
		p.PosLongHis, p.PosLongToday, p.PosShortHis, p.PosShortToday = p.VolumeLongHis, 0, p.VolumeShortHis, 0
		p.VolumeLongFrozen, p.VolumeShortFrozen = 0, 0
		p.PositionPriceLong, p.PositionPriceShort = p.LastPrice, p.LastPrice
		p.PositionCostLong = p.LastPrice * float64(p.VolumeLong) * mult
		p.PositionCostShort = p.LastPrice * float64(p.VolumeShort) * mult
		p.PositionProfitLong, p.PositionProfitShort, p.PositionProfit = 0, 0, 0
		l.markPos(symbol)
	}
	l.flushTo(nextDay)
}

// Flush 返回自上次 Flush 以来的交易 diff（按产生顺序），没有变化时为 nil。
func (l *Ledger) Flush() []any {
	l.flushTo("")
	out := l.elements
	l.elements = nil
	return out
}

// flushTo 把脏对象收为一个 diff 元素；tradingDay 非空时同一元素发布新交易日。
func (l *Ledger) flushTo(tradingDay string) {
	node := new(wire.Object)
	if tradingDay != "" {
		node.Set("session", new(wire.Object).Set("trading_day", tradingDay))
	}
	if l.dirtyAcc {
		node.Set("accounts", new(wire.Object).Set("CNY", wire.Fields(&l.acc, accountFields)))
	}
	if len(l.dirtyPos) > 0 {
		ps := new(wire.Object)
		for _, s := range l.dirtyPos {
			ps.Set(s, wire.Fields(&l.pos[s].Position, positionFields))
		}
		node.Set("positions", ps)
	}
	if len(l.dirtyOrders) > 0 {
		os := new(wire.Object)
		for _, o := range l.dirtyOrders {
			os.Set(o.OrderID, wire.Fields(&o.Order, orderFields))
		}
		node.Set("orders", os)
	}
	if len(l.newTrades) > 0 {
		ts := new(wire.Object)
		for i := range l.newTrades {
			ts.Set(l.newTrades[i].TradeID, wire.Fields(&l.newTrades[i], tradeFields))
		}
		node.Set("trades", ts)
	}
	l.dirtyAcc, l.dirtyPos, l.dirtyOrders, l.newTrades = false, nil, nil, nil
	if node.Len() > 0 {
		l.elements = append(l.elements, l.wrap(node))
	}
	if l.notifies != nil {
		l.elements = append(l.elements, new(wire.Object).Set("notify", l.notifies))
		l.notifies = nil
	}
}

func (l *Ledger) wrap(node *wire.Object) *wire.Object {
	return new(wire.Object).Set("trade", new(wire.Object).Set(l.cfg.ID, node))
}

func (l *Ledger) ensurePosition(symbol string, q Quote) *position {
	p := l.pos[symbol]
	if p == nil {
		nan := math.NaN()
		ex, ins := splitSymbol(symbol)
		p = &position{Position: model.Position{ExchangeID: ex, InstrumentID: ins,
			OpenPriceLong: nan, OpenPriceShort: nan, PositionPriceLong: nan, PositionPriceShort: nan,
			LastPrice: q.LastPrice}, futureMargin: futureMargin(l.infos[symbol])}
		l.pos[symbol] = p
	}
	return p
}

func (l *Ledger) markPos(symbol string) {
	if !slices.Contains(l.dirtyPos, symbol) {
		l.dirtyPos = append(l.dirtyPos, symbol)
	}
}

func (l *Ledger) markOrder(o *order) {
	if !slices.Contains(l.dirtyOrders, o) {
		l.dirtyOrders = append(l.dirtyOrders, o)
	}
}

func (l *Ledger) removeOrder(symbol string, o *order) {
	l.book[symbol] = slices.DeleteFunc(l.book[symbol], func(x *order) bool { return x == o })
	if len(l.book[symbol]) == 0 {
		delete(l.book, symbol)
	}
}

func futureMargin(info model.Instrument) float64 {
	if !info.Valid.Has(model.InstrumentMargin) {
		return math.NaN()
	}
	return info.Margin
}

func commission(info model.Instrument) float64 {
	if !info.Valid.Has(model.InstrumentCommission) {
		return math.NaN()
	}
	return info.Commission
}

// shfeLike 报告交易所是否区分平今与平昨（上期所、上期能源）。
func shfeLike(exchange string) bool { return exchange == "SHFE" || exchange == "INE" }

func avgPrice(cost float64, volume int64, mult float64) float64 {
	if volume <= 0 {
		return math.NaN()
	}
	return cost / float64(volume) / mult
}

// riskRatio = 保证金 / 权益；权益 ≤ 0 时为 +Inf（协议中以不可用值传递）。
func riskRatio(a *model.Account) float64 {
	if a.Balance <= 0 {
		return math.Inf(1)
	}
	return a.Margin / a.Balance
}

func pick[T any](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}

func splitSymbol(s string) (string, string) {
	for i := range s {
		if s[i] == '.' {
			return s[:i], s[i+1:]
		}
	}
	return "", s
}

var (
	accountFields = []string{"currency", "pre_balance", "static_balance", "balance", "available", "float_profit",
		"position_profit", "close_profit", "frozen_margin", "margin", "frozen_commission", "commission",
		"frozen_premium", "premium", "deposit", "withdraw", "risk_ratio", "market_value", "ctp_balance", "ctp_available"}
	positionFields = []string{"exchange_id", "instrument_id", "pos_long_his", "pos_long_today", "pos_short_his",
		"pos_short_today", "volume_long_today", "volume_long_his", "volume_long", "volume_long_frozen_today",
		"volume_long_frozen_his", "volume_long_frozen", "volume_short_today", "volume_short_his", "volume_short",
		"volume_short_frozen_today", "volume_short_frozen_his", "volume_short_frozen", "open_price_long",
		"open_price_short", "open_cost_long", "open_cost_short", "position_price_long", "position_price_short",
		"position_cost_long", "position_cost_short", "float_profit_long", "float_profit_short", "float_profit",
		"position_profit_long", "position_profit_short", "position_profit", "margin_long", "margin_short", "margin",
		"last_price", "market_value_long", "market_value_short", "market_value"}
	orderFields = []string{"user_id", "order_id", "exchange_order_id", "exchange_id", "instrument_id", "direction",
		"offset", "volume_orign", "volume_left", "limit_price", "price_type", "volume_condition", "time_condition",
		"insert_date_time", "last_msg", "status"}
	tradeFields = []string{"user_id", "order_id", "trade_id", "exchange_trade_id", "exchange_id", "instrument_id",
		"direction", "offset", "price", "volume", "trade_date_time", "commission"}
)
