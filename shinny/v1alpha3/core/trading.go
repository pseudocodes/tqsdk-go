package core

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"

	"go.uber.org/zap"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/auth"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/model"
)

// TradingConfig 配置交易组件。URL 为账户 URL 的默认值，不在创建时拨号。
type TradingConfig struct {
	Auth            auth.Provider
	URL             string
	Logger          *zap.Logger
	MaxMessageBytes int64
	QueueSize       int
	MailboxSize     int
}

// AccountConfig 配置一个交易账户。三种身份分别命名：
// ID 是本地身份，UserName 是券商资金账号（编码到 req_login.user_name），天勤用户名只在 auth.Client 内。
type AccountConfig struct {
	ID         string // Trading 内唯一的本地账户身份；为空时取 BrokerID+":"+UserName
	BrokerID   string
	UserName   string // 券商资金账号，不是天勤鉴权用户名
	Password   string
	URL        string                                // 覆盖交易服务地址
	Source     Source                                // 注入的连接（回放/测试）
	Redial     func(context.Context) (Source, error) // 注入连接断开后的重建函数；nil 表示不重连
	ClientInfo LoginClientInfo
	NewOrderID func() string // 报单未指定 ClientOrderID 时的生成器；nil 使用随机 ID
}

// AccountSpec 是账户声明：在指定 Trading 上登记一个未启动的账户句柄，不拨号、不推进、不报单。
type AccountSpec interface {
	Prepare(t *Trading) (*AccountHandle, error)
}

// Prepare 让网络账户配置实现 AccountSpec，等价于 PrepareAccount。
func (cfg AccountConfig) Prepare(t *Trading) (*AccountHandle, error) { return t.PrepareAccount(cfg) }

// LoginClientInfo 是穿透式监管信息；AppID/SystemInfo 为空时不发送，不伪造。
type LoginClientInfo struct {
	MACAddress string // 为空时取本机第一个有效网卡地址
	AppID      string
	SystemInfo string
}

// Trading 是交易组件，管理多个账户；每个账户一条交易连接。
type Trading struct {
	rt  *Runtime
	id  uint64
	cfg TradingConfig
	log *zap.Logger

	// 受 rt.mu 保护。
	accounts map[string]*AccountHandle
	closed   bool
}

var (
	ErrDuplicateAccount = errors.New("tqsdk: duplicate account id")
	ErrStarted          = errors.New("tqsdk: account already started")
	ErrUnsupported      = errors.New("tqsdk: unsupported")
)

// NewTrading 创建交易组件；不拨号，网络账户 Start 时才鉴权与接入（此时要求 Auth）。
func NewTrading(ctx context.Context, rt *Runtime, cfg TradingConfig) (*Trading, error) {
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	t := &Trading{rt: rt, id: rt.newID(), cfg: cfg, log: cfg.Logger, accounts: map[string]*AccountHandle{}}
	rt.onClose(func() { t.Close() })
	return t, nil
}

// Changes 提取提交中属于本组件的变化。
func (t *Trading) Changes(c Commit) TradingChanges {
	if c.runtimeID != t.rt.id {
		panic("tqsdk: commit from another runtime")
	}
	p, _ := c.changesFor(t.id).(*tradingChanges)
	return TradingChanges{p: p}
}

// Close 关闭全部账户；不关闭借用的 Runtime。幂等。
func (t *Trading) Close() error {
	t.rt.mu.Lock()
	if t.closed {
		t.rt.mu.Unlock()
		return nil
	}
	t.closed = true
	accounts := make([]*AccountHandle, 0, len(t.accounts))
	for _, a := range t.accounts {
		accounts = append(accounts, a)
	}
	t.rt.mu.Unlock()
	for _, a := range accounts {
		a.Close()
	}
	return nil
}

// PrepareAccount 本地登记一个 Pending 账户句柄；不接管 Source、不拨号。
func (t *Trading) PrepareAccount(cfg AccountConfig) (*AccountHandle, error) {
	if cfg.BrokerID == "" || cfg.UserName == "" {
		return nil, invalid("BrokerID and UserName are required")
	}
	if cfg.ID == "" {
		cfg.ID = cfg.BrokerID + ":" + cfg.UserName
	}
	t.rt.mu.Lock()
	defer t.rt.mu.Unlock()
	return t.register(cfg)
}

// PrepareLocalAccount 登记一个由本地后端（模拟账户）驱动的账户：不鉴权、不拨号，立即接入本地路由。
// 账户以 ID 作为交易报文中的用户键；报撤单经 TakeCommands 交给本地后端，
// 被接受即为 CommandWritten。Start 之前可 SetCommandGuard；账户在本地后端交付初始基线后 Ready。
func (t *Trading) PrepareLocalAccount(cfg AccountConfig) (*AccountHandle, error) {
	if cfg.ID == "" {
		return nil, invalid("local account ID is required")
	}
	cfg.UserName = cfg.ID
	t.rt.mu.Lock()
	a, err := t.register(cfg)
	t.rt.mu.Unlock()
	if err != nil {
		return nil, err
	}
	r, err := t.rt.attachLocalRoute(t.id, a.decodeTrading, a)
	if err != nil {
		a.Close()
		return nil, err
	}
	t.rt.mu.Lock()
	a.route, a.state = r, Pending
	t.rt.mu.Unlock()
	return a, nil
}

// TakeCommands 取走本地账户已接受的报撤单报文（insert_order / cancel_order），按接受顺序。
func (a *AccountHandle) TakeCommands() [][]byte {
	a.t.rt.mu.Lock()
	defer a.t.rt.mu.Unlock()
	if a.route == nil {
		return nil
	}
	return a.route.takeOutbox()
}

// register 登记账户句柄；调用方持有 rt.mu。
func (t *Trading) register(cfg AccountConfig) (*AccountHandle, error) {
	if t.closed {
		return nil, ErrClosed
	}
	if _, ok := t.accounts[cfg.ID]; ok {
		return nil, ErrDuplicateAccount
	}
	a := &AccountHandle{
		t: t, id: t.rt.newID(), cfg: cfg,
		accounts:  map[string]*objNode[model.Account]{},
		positions: map[string]*objNode[model.Position]{},
		orders:    map[string]*objNode[model.Order]{},
		riskRules: map[string]*objNode[model.RemoteRiskRule]{},
		riskData:  map[string]*objNode[model.RemoteRiskData]{},
		trades:    map[string]model.Trade{},
		partials:  map[string]*model.Trade{},
		seen:      map[string]bool{},
		subs:      map[*TradeEventSub]struct{}{},
		commands:  map[string]*orderCmd{},
		riskReqs:  map[string]*RemoteRiskRequest{},
	}
	t.accounts[cfg.ID] = a
	return a, nil
}

// OpenAccount 是 PrepareAccount + Start。返回后才订阅事件不保证收到早期通知。
func (t *Trading) OpenAccount(ctx context.Context, cfg AccountConfig) (*AccountHandle, error) {
	a, err := t.PrepareAccount(cfg)
	if err != nil {
		return nil, err
	}
	if err := a.Start(ctx); err != nil {
		a.Close()
		return nil, err
	}
	return a, nil
}

// Account 返回本地身份为 id 的账户句柄。
func (t *Trading) Account(id string) (*AccountHandle, bool) {
	t.rt.mu.RLock()
	defer t.rt.mu.RUnlock()
	a, ok := t.accounts[id]
	return a, ok
}

// objNode 是交易对象的稳定身份。gen 为数据所属连接代次，当前集合只包含当前代次内容。
type objNode[T any] struct {
	id     uint64
	key    string
	value  T
	exists bool
	gen    uint64
}

// AccountHandle 是一个交易账户：登录、状态同步、事件与命令入口。
type AccountHandle struct {
	t      *Trading
	id     uint64
	cfg    AccountConfig
	access atomic.Pointer[auth.Access]

	// 以下字段受 rt.mu 保护。
	route     *route
	started   bool
	closed    bool
	state     DataState
	moreKnown bool
	more      bool
	posReady  bool
	posKnown  bool
	session   model.TradingSessionInfo
	scope     string
	accounts  map[string]*objNode[model.Account]
	positions map[string]*objNode[model.Position]
	orders    map[string]*objNode[model.Order]
	riskRules map[string]*objNode[model.RemoteRiskRule]
	riskData  map[string]*objNode[model.RemoteRiskData]
	trades    map[string]model.Trade  // 当前作用域的完整成交事实
	partials  map[string]*model.Trade // 尚未完整的成交记录
	seen      map[string]bool         // 本同步周期出现过的持仓
	subs      map[*TradeEventSub]struct{}
	guard     CommandGuard
	commands  map[string]*orderCmd // 本地已接受报单，按 order_id
	riskReqs  map[string]*RemoteRiskRequest
	cmdSeq    uint64
}

// Start 鉴权、接入并登记 req_login；不等待数据 Ready。同一句柄只能 Start 一次。
func (a *AccountHandle) Start(ctx context.Context) error {
	t := a.t
	t.rt.mu.Lock()
	if a.closed || t.closed {
		t.rt.mu.Unlock()
		return ErrClosed
	}
	if a.started {
		t.rt.mu.Unlock()
		return ErrStarted
	}
	if a.route != nil && a.route.local { // 本地账户已接入，启动只封住 SetCommandGuard
		a.started = true
		t.rt.mu.Unlock()
		return nil
	}
	if t.cfg.Auth == nil {
		t.rt.mu.Unlock()
		return errors.New("tqsdk: TradingConfig.Auth is required for network accounts")
	}
	a.started = true
	t.rt.mu.Unlock()

	req := auth.Request{Service: auth.Trade, BrokerID: a.cfg.BrokerID, AccountID: a.cfg.UserName}
	access, err := t.cfg.Auth.Authorize(ctx, req)
	if err != nil {
		a.unstart()
		return err
	}
	a.access.Store(&access)
	src, dial := a.cfg.Source, a.cfg.Redial
	if src == nil {
		url := a.cfg.URL
		if url == "" {
			url = t.cfg.URL
		}
		if url == "" {
			url = access.Endpoint
		}
		if url == "" {
			a.unstart()
			return errors.New("tqsdk: trade URL unknown")
		}
		dial = func(ctx context.Context) (Source, error) {
			acc, err := t.cfg.Auth.Authorize(ctx, req)
			if err != nil {
				return nil, err
			}
			a.access.Store(&acc)
			return DialWebsocket(ctx, url, acc.Header, t.cfg.MaxMessageBytes)
		}
		if src, err = DialWebsocket(ctx, url, access.Header, t.cfg.MaxMessageBytes); err != nil {
			a.unstart()
			return err
		}
	}
	// 路由登记与 req_login 在同一临界区完成，保证 req_login 先于任何 peek_message。
	_, err = t.rt.attachRoute(t.id, src, a.decodeTrading, a, dial, t.cfg.QueueSize, func(r *route) error {
		if a.closed || t.closed {
			return ErrClosed
		}
		a.route, a.state = r, Pending
		return r.send(a.loginPack(), nil)
	})
	if err != nil {
		if a.cfg.Source == nil {
			go src.Close()
		}
		a.unstart()
	}
	return err
}

func (a *AccountHandle) unstart() {
	a.t.rt.mu.Lock()
	a.access.Store(nil)
	a.started = false
	a.t.rt.mu.Unlock()
}

// HasFeature 查询最近一次成功接入授权提供的功能权限。
// Start 尚未完成授权或已失败时返回 false；重连时的更新规则同 Market.HasFeature。
func (a *AccountHandle) HasFeature(feature string) bool {
	access := a.access.Load()
	return access != nil && access.HasFeature(feature)
}

type loginPack struct {
	Aid              string `json:"aid"`
	Bid              string `json:"bid"`
	UserName         string `json:"user_name"`
	Password         string `json:"password"`
	ClientMACAddress string `json:"client_mac_address,omitempty"`
	ClientAppID      string `json:"client_app_id,omitempty"`
	ClientSystemInfo string `json:"client_system_info,omitempty"`
}

func (a *AccountHandle) loginPack() []byte {
	info := a.cfg.ClientInfo
	mac := info.MACAddress
	if mac == "" {
		mac = localMAC()
	}
	return mustJSON(loginPack{Aid: "req_login", Bid: a.cfg.BrokerID, UserName: a.cfg.UserName, Password: a.cfg.Password,
		ClientMACAddress: mac, ClientAppID: info.AppID, ClientSystemInfo: info.SystemInfo})
}

// localMAC 返回本机第一个有效网卡地址，格式同 Python：XX-XX-XX-XX-XX-XX。
func localMAC() string {
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback == 0 && len(i.HardwareAddr) == 6 {
			return strings.ToUpper(strings.ReplaceAll(i.HardwareAddr.String(), ":", "-"))
		}
	}
	return ""
}

// Close 关闭账户连接，完成全部等待者与订阅。幂等。
func (a *AccountHandle) Close() error {
	t := a.t
	t.rt.mu.Lock()
	if a.closed {
		t.rt.mu.Unlock()
		return nil
	}
	a.closed = true
	delete(t.accounts, a.cfg.ID)
	for _, q := range a.riskReqs {
		q.c.finish(nil, ErrClosed)
	}
	subs := a.subs
	a.subs = map[*TradeEventSub]struct{}{}
	r := a.route
	t.rt.mu.Unlock()
	if r != nil {
		r.close()
	}
	for s := range subs {
		s.mb.close(ErrClosed)
	}
	return nil
}

// down 实现 routeHandler：账户置 Stale，远端风控观察票据失败。
func (a *AccountHandle) down(r *route, st Stamp, err error) Applied {
	a.t.log.Warn("trade route down", zap.String("account", a.cfg.ID), zap.Uint64("generation", r.gen), zap.Error(err))
	j := newTdJournal()
	j.errs = append(j.errs, RouteError{Route: r.id, Generation: r.gen, Err: err})
	for ex, q := range a.riskReqs {
		q.c.finish(nil, ErrNotConnected)
		delete(a.riskReqs, ex)
	}
	if a.state != Stale {
		j.state = true
		j.stateErr = err
	}
	a.state = Stale
	return a.finish(j, st)
}

// up 实现 routeHandler：新代次重放 req_login（不重放结算确认），等待该代次 trade_more_data=false。
func (a *AccountHandle) up(r *route, st Stamp) Applied {
	a.t.log.Info("trade route up", zap.String("account", a.cfg.ID), zap.Uint64("generation", r.gen))
	r.send(a.loginPack(), nil)
	a.moreKnown, a.more, a.posKnown = false, false, false
	clear(a.seen)
	a.state = Pending
	j := newTdJournal()
	j.state = true
	return a.finish(j, st)
}

// ---- 读取 ----

func (a *AccountHandle) lock() func() {
	a.t.rt.mu.RLock()
	return a.t.rt.mu.RUnlock
}

func (a *AccountHandle) gen() uint64 {
	if a.route == nil {
		return 0
	}
	return a.route.gen
}

func current[T any](a *AccountHandle, n *objNode[T]) bool {
	return n != nil && n.exists && n.gen == a.gen()
}

func collect[T any](a *AccountHandle, m map[string]*objNode[T]) map[string]T {
	out := make(map[string]T, len(m))
	for k, n := range m {
		if current(a, n) {
			out[k] = n.value
		}
	}
	return out
}

// ID 返回本地账户身份。
func (a *AccountHandle) ID() string { return a.cfg.ID }

// UserName 返回券商资金账号。
func (a *AccountHandle) UserName() string { return a.cfg.UserName }

// State 返回账户同步状态。
func (a *AccountHandle) State() DataState { defer a.lock()(); return a.state }

// Session 返回服务端 session 元数据。
func (a *AccountHandle) Session() model.TradingSessionInfo { defer a.lock()(); return a.session }

// Account 返回 CNY 资金记录；缺少时返回 false，不任取其他币种。
func (a *AccountHandle) Account() (model.Account, bool) { return a.AccountByCurrency("CNY") }

// AccountByCurrency 返回指定币种资金记录。
func (a *AccountHandle) AccountByCurrency(currency string) (model.Account, bool) {
	defer a.lock()()
	n := a.accounts[currency]
	if !current(a, n) {
		return model.Account{}, false
	}
	return n.value, true
}

// Accounts 返回全部币种资金记录的副本。
func (a *AccountHandle) Accounts() map[string]model.Account {
	defer a.lock()()
	return collect(a, a.accounts)
}

// Position 返回持仓；未找到不等于已确认不存在，调用者应检查 State。
func (a *AccountHandle) Position(symbol string) (model.Position, bool) {
	defer a.lock()()
	n := a.positions[symbol]
	if !current(a, n) {
		return model.Position{}, false
	}
	return n.value, true
}

// Positions 返回全部持仓的副本。
func (a *AccountHandle) Positions() map[string]model.Position {
	defer a.lock()()
	return collect(a, a.positions)
}

// Order 返回委托单。
func (a *AccountHandle) Order(id string) (model.Order, bool) {
	defer a.lock()()
	n := a.orders[id]
	if !current(a, n) {
		return model.Order{}, false
	}
	return n.value, true
}

// Orders 返回当前代次全部委托单的副本。
func (a *AccountHandle) Orders() map[string]model.Order {
	defer a.lock()()
	return collect(a, a.orders)
}

// Trade 返回一笔完整成交。
func (a *AccountHandle) Trade(id string) (model.Trade, bool) {
	defer a.lock()()
	t, ok := a.trades[id]
	return t, ok
}

// Trades 返回当前作用域全部完整成交的副本。
func (a *AccountHandle) Trades() map[string]model.Trade {
	defer a.lock()()
	out := make(map[string]model.Trade, len(a.trades))
	for k, v := range a.trades {
		out[k] = v
	}
	return out
}

// TradesByOrder 返回某委托单的成交。
func (a *AccountHandle) TradesByOrder(orderID string) []model.Trade {
	defer a.lock()()
	var out []model.Trade
	for _, t := range a.trades {
		if t.OrderID == orderID {
			out = append(out, t)
		}
	}
	return out
}

// RemoteRiskRule 返回当前代次已收到的交易所远端风控规则；存在不代表字段完整，看 Valid。
func (a *AccountHandle) RemoteRiskRule(exchange string) (model.RemoteRiskRule, bool) {
	defer a.lock()()
	n := a.riskRules[exchange]
	if !current(a, n) {
		return model.RemoteRiskRule{}, false
	}
	return n.value, true
}

// RemoteRiskData 返回当前代次已收到的合约远端风控统计。
func (a *AccountHandle) RemoteRiskData(symbol string) (model.RemoteRiskData, bool) {
	defer a.lock()()
	n := a.riskData[symbol]
	if !current(a, n) {
		return model.RemoteRiskData{}, false
	}
	return n.value, true
}

// Snapshot 在同一视点复制整个账户。
func (a *AccountHandle) Snapshot() AccountSnapshot {
	defer a.lock()()
	return a.snapshot(Stamp{Epoch: a.t.rt.epoch})
}

func (a *AccountHandle) snapshot(st Stamp) AccountSnapshot {
	trades := make(map[string]model.Trade, len(a.trades))
	for k, v := range a.trades {
		trades[k] = v
	}
	return AccountSnapshot{
		State: a.state, Stamp: st, Session: a.session, Scope: a.scope,
		PositionReadyKnown: a.posKnown, PositionReady: a.posReady,
		Accounts: collect(a, a.accounts), Positions: collect(a, a.positions), Orders: collect(a, a.orders),
		Trades: trades, RemoteRiskRules: collect(a, a.riskRules), RemoteRiskData: collect(a, a.riskData),
	}
}

// Events 注册账户事件订阅。Ready 账户在同一视点先交付 Snapshot，再接后续增量；
// 未 Ready 时先交付状态与通知，完成同步后交付 Snapshot。
func (a *AccountHandle) Events(ctx context.Context) (*TradeEventSub, error) {
	t := a.t
	t.rt.mu.Lock()
	defer t.rt.mu.Unlock()
	if a.closed {
		return nil, ErrClosed
	}
	s := &TradeEventSub{a: a, mb: newMailbox[TradeEvent](t.cfg.MailboxSize)}
	if a.state == Ready {
		snap := a.snapshot(Stamp{Epoch: t.rt.epoch})
		s.mb.push(TradeEvent{Kind: TradeEventSnapshot, AccountID: a.cfg.ID, Scope: a.scope, Stamp: snap.Stamp, Snapshot: &snap})
	}
	a.subs[s] = struct{}{}
	return s, nil
}

// ---- 变化查询目标 ----

// AccountRef 是某币种资金记录的稳定引用。
type AccountRef struct {
	a    *AccountHandle
	node *objNode[model.Account]
}

// PositionRef 是某合约持仓的稳定引用。
type PositionRef struct {
	a    *AccountHandle
	node *objNode[model.Position]
}

// TradesRef 是账户成交集合的引用。
type TradesRef struct{ a *AccountHandle }

func nodeOf[T any](a *AccountHandle, m map[string]*objNode[T], key string) *objNode[T] {
	n := m[key]
	if n == nil {
		n = &objNode[T]{id: a.t.rt.newID(), key: key}
		m[key] = n
	}
	return n
}

// AccountRef 返回币种（默认 CNY）资金记录的引用。
func (a *AccountHandle) AccountRef(currency string) *AccountRef {
	if currency == "" {
		currency = "CNY"
	}
	a.t.rt.mu.Lock()
	defer a.t.rt.mu.Unlock()
	return &AccountRef{a, nodeOf(a, a.accounts, currency)}
}

// PositionRef 返回合约持仓的引用。
func (a *AccountHandle) PositionRef(symbol string) *PositionRef {
	a.t.rt.mu.Lock()
	defer a.t.rt.mu.Unlock()
	return &PositionRef{a, nodeOf(a, a.positions, symbol)}
}

// TradesRef 返回成交集合的引用。
func (a *AccountHandle) TradesRef() *TradesRef { return &TradesRef{a} }

// Snapshot 返回资金记录与账户状态。
func (r *AccountRef) Snapshot() (model.Account, DataState, bool) {
	defer r.a.lock()()
	return r.node.value, r.a.state, current(r.a, r.node)
}

// Snapshot 返回持仓与账户状态。
func (r *PositionRef) Snapshot() (model.Position, DataState, bool) {
	defer r.a.lock()()
	return r.node.value, r.a.state, current(r.a, r.node)
}

// Snapshot 返回成交集合副本。
func (r *TradesRef) Snapshot() map[string]model.Trade { return r.a.Trades() }
