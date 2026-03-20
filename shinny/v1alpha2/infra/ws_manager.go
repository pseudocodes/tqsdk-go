package infra

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type WSEventState string

const (
	WSStateConnecting   WSEventState = "connecting"
	WSStateReconnecting WSEventState = "reconnecting"
	WSStateConnected    WSEventState = "connected"
	WSStateDisconnected WSEventState = "disconnected"
	WSStateClosed       WSEventState = "closed"
)

type WSEvent struct {
	State     WSEventState
	URL       string
	Attempt   int
	Err       error
	Connected bool
	At        time.Time
}

type WSCallbacks struct {
	OnEvent   func(WSEvent)
	OnConnect func(context.Context) error
	OnMessage func(context.Context, map[string]any) error
}

type WSOptions struct {
	DialTimeout  time.Duration
	WriteTimeout time.Duration
	BaseBackoff  time.Duration
	MaxBackoff   time.Duration
	SendQueue    int
}

func defaultWSOptions() WSOptions {
	return WSOptions{
		DialTimeout:  20 * time.Second,
		WriteTimeout: 10 * time.Second,
		BaseBackoff:  time.Second,
		MaxBackoff:   8 * time.Second,
		SendQueue:    1024,
	}
}

type WSManager struct {
	url      string
	headerFn func() http.Header
	cb       WSCallbacks
	opt      WSOptions

	mu      sync.Mutex
	started bool
	closed  bool
	runCtx  context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	conn    *WSConn

	sendQ   chan map[string]any
	signal  chan struct{}
	forceRC chan struct{}

	connected atomic.Bool
}

func NewWSManager(url string, headerFn func() http.Header, cb WSCallbacks, opt *WSOptions) *WSManager {
	o := defaultWSOptions()
	if opt != nil {
		if opt.DialTimeout > 0 {
			o.DialTimeout = opt.DialTimeout
		}
		if opt.WriteTimeout > 0 {
			o.WriteTimeout = opt.WriteTimeout
		}
		if opt.BaseBackoff > 0 {
			o.BaseBackoff = opt.BaseBackoff
		}
		if opt.MaxBackoff > 0 {
			o.MaxBackoff = opt.MaxBackoff
		}
		if opt.SendQueue > 0 {
			o.SendQueue = opt.SendQueue
		}
	}
	return &WSManager{
		url:      url,
		headerFn: headerFn,
		cb:       cb,
		opt:      o,
		sendQ:    make(chan map[string]any, o.SendQueue),
		signal:   make(chan struct{}, 1),
		forceRC:  make(chan struct{}, 1),
	}
}

func (m *WSManager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return context.Canceled
	}
	if m.started {
		return nil
	}
	m.runCtx, m.cancel = context.WithCancel(ctx)
	m.started = true
	m.wg.Add(1)
	go m.run()
	return nil
}

func (m *WSManager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.closeConn()
	m.wg.Wait()
	m.emitEvent(WSEvent{State: WSStateClosed, URL: m.url, Connected: false, At: time.Now()})
	return nil
}

func (m *WSManager) Connected() bool {
	return m.connected.Load()
}

func (m *WSManager) WaitConnected(ctx context.Context) error {
	for {
		if m.connected.Load() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.signal:
		}
	}
}

func (m *WSManager) ForceReconnect() {
	select {
	case m.forceRC <- struct{}{}:
	default:
	}
	m.closeConn()
}

func (m *WSManager) Send(pack map[string]any) error {
	if pack == nil {
		return nil
	}
	m.mu.Lock()
	runCtx := m.runCtx
	started := m.started
	closed := m.closed
	m.mu.Unlock()
	if !started || closed || runCtx == nil {
		return context.Canceled
	}
	select {
	case <-runCtx.Done():
		return runCtx.Err()
	case m.sendQ <- pack:
		return nil
	}
}

func (m *WSManager) run() {
	defer m.wg.Done()

	attempt := 0
	for {
		if m.runCtx.Err() != nil {
			return
		}

		state := WSStateConnecting
		if attempt > 0 {
			state = WSStateReconnecting
		}
		m.emitEvent(WSEvent{State: state, URL: m.url, Attempt: attempt, Connected: false, At: time.Now()})

		dialCtx, cancel := context.WithTimeout(m.runCtx, m.opt.DialTimeout)
		header := http.Header(nil)
		if m.headerFn != nil {
			header = m.headerFn()
		}
		conn, _, err := DialWS(dialCtx, m.url, header)
		cancel()
		if err != nil {
			m.emitEvent(WSEvent{State: WSStateDisconnected, URL: m.url, Attempt: attempt, Err: err, Connected: false, At: time.Now()})
			if !m.sleepBackoff(attempt) {
				return
			}
			attempt++
			continue
		}

		attempt = 0
		m.setConn(conn)
		m.connected.Store(true)
		m.notifySignal()
		m.emitEvent(WSEvent{State: WSStateConnected, URL: m.url, Connected: true, At: time.Now()})

		if m.cb.OnConnect != nil {
			if err := m.cb.OnConnect(m.runCtx); err != nil {
				m.connected.Store(false)
				m.notifySignal()
				m.emitEvent(WSEvent{State: WSStateDisconnected, URL: m.url, Err: err, Connected: false, At: time.Now()})
				m.closeConn()
				if !m.sleepBackoff(attempt) {
					return
				}
				attempt++
				continue
			}
		}

		if err := m.pump(conn); err != nil && m.runCtx.Err() == nil {
			m.emitEvent(WSEvent{State: WSStateDisconnected, URL: m.url, Err: err, Connected: false, At: time.Now()})
		}
		m.connected.Store(false)
		m.notifySignal()
		m.closeConn()
		if !m.sleepBackoff(attempt) {
			return
		}
		attempt++
	}
}

func (m *WSManager) pump(conn *WSConn) error {
	msgCh := make(chan map[string]any, 16)
	errCh := make(chan error, 1)
	go func() {
		defer close(msgCh)
		for {
			var msg map[string]any
			if err := conn.ReadJSON(m.runCtx, &msg); err != nil {
				errCh <- err
				return
			}
			select {
			case <-m.runCtx.Done():
				return
			case msgCh <- msg:
			}
		}
	}()

	for {
		select {
		case <-m.runCtx.Done():
			return m.runCtx.Err()
		case <-m.forceRC:
			return context.Canceled
		case err := <-errCh:
			return err
		case pack := <-m.sendQ:
			wctx, cancel := context.WithTimeout(m.runCtx, m.opt.WriteTimeout)
			err := conn.WriteJSON(wctx, pack)
			cancel()
			if err != nil {
				return err
			}
		case msg, ok := <-msgCh:
			if !ok {
				return context.Canceled
			}
			if m.cb.OnMessage != nil {
				if err := m.cb.OnMessage(m.runCtx, msg); err != nil {
					return err
				}
			}
		}
	}
}

func (m *WSManager) sleepBackoff(attempt int) bool {
	d := m.opt.BaseBackoff
	for i := 0; i < attempt; i++ {
		d *= 2
		if d >= m.opt.MaxBackoff {
			d = m.opt.MaxBackoff
			break
		}
	}
	if d <= 0 {
		d = time.Second
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-m.runCtx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (m *WSManager) setConn(conn *WSConn) {
	m.mu.Lock()
	m.conn = conn
	m.mu.Unlock()
}

func (m *WSManager) closeConn() {
	m.mu.Lock()
	conn := m.conn
	m.conn = nil
	m.mu.Unlock()
	if conn != nil {
		_ = conn.Close(1000, "")
	}
}

func (m *WSManager) emitEvent(ev WSEvent) {
	if m.cb.OnEvent != nil {
		m.cb.OnEvent(ev)
	}
}

func (m *WSManager) notifySignal() {
	select {
	case m.signal <- struct{}{}:
	default:
	}
}
