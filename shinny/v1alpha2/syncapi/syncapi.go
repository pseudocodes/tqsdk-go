package syncapi

import (
	"context"
	"fmt"
	"sync"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
)

// syncEvent is the internal unified event.
type syncEvent struct {
	key           refKey
	changedFields ChangedFields // which fields changed (nil = unknown, degrade to object-level)
}

type closeable interface {
	Close() error
}

// SyncApi provides a synchronous wait_update / is_changing programming
// model on top of tqsdk-go's channel-based async API.
//
// Counterpart: Python TqApi
//
//	sa := syncapi.New(market, trade)
//	quote, _ := sa.GetQuote(ctx, "SHFE.rb2501")
//	klines, _ := sa.GetKlineSeries(ctx, "SHFE.rb2501", 60, 200)
//	for {
//	    sa.WaitUpdate()
//	    if sa.IsChanging(quote)  { fmt.Println(quote.Get().LastPrice) }
//	    if sa.IsChanging(klines) { /* ... */ }
//	}
type SyncApi struct {
	market api.MarketService
	trade  api.TradeService

	eventCh chan syncEvent

	mu      sync.RWMutex
	changed map[refKey]ChangedFields // value: field-level info (nil = object-level only)

	subsMu sync.Mutex
	subs   []closeable

	// KlineRef registry for KlineBar lookups in IsChanging.
	klineRefsMu sync.RWMutex
	klineRefs   map[refKey]*KlineRef

	session api.TradeSession
}

// New creates a SyncApi from existing services.
func New(market api.MarketService, trade api.TradeService) *SyncApi {
	return &SyncApi{
		market:    market,
		trade:     trade,
		eventCh:   make(chan syncEvent, 256),
		changed:   make(map[refKey]ChangedFields),
		klineRefs: make(map[refKey]*KlineRef),
	}
}

// NewFromClient creates a SyncApi from a Client.
func NewFromClient(cli api.Client) *SyncApi {
	return New(cli.Market(), cli.Trade())
}

// Market returns the underlying MarketService.
func (s *SyncApi) Market() api.MarketService { return s.market }

// Trade returns the underlying TradeService.
func (s *SyncApi) Trade() api.TradeService { return s.trade }

// Session returns the trade session created by Login.
func (s *SyncApi) Session() api.TradeSession { return s.session }

// ---------------------------------------------------------------
// GetXxx — create Ref and auto-subscribe
// ---------------------------------------------------------------

// GetQuote subscribes to a symbol's quote and returns a QuoteRef.
// Counterpart: Python quote = api.get_quote("SHFE.rb2501")
func (s *SyncApi) GetQuote(ctx context.Context, symbol string) (*QuoteRef, error) {
	sub, err := s.market.SubscribeQuotes(ctx, symbol)
	if err != nil {
		return nil, err
	}
	s.trackSub(sub)
	ref := &QuoteRef{symbol: symbol, market: s.market}
	go s.forwardQuote(ctx, sub, ref.refKey())
	return ref, nil
}

// GetKlineSeries subscribes to klines and returns a KlineRef.
// Counterpart: Python klines = api.get_kline_serial("SHFE.rb2501", 60, 200)
func (s *SyncApi) GetKlineSeries(
	ctx context.Context,
	symbol string,
	durationSec int,
	dataLength int,
	opts ...api.KlineSubOption,
) (*KlineRef, error) {
	sub, err := s.market.SubscribeKlines(ctx, []string{symbol}, durationSec, dataLength, opts...)
	if err != nil {
		return nil, err
	}
	s.trackSub(sub)
	ref := &KlineRef{symbol: symbol, duration: durationSec}

	key := ref.refKey()
	s.klineRefsMu.Lock()
	s.klineRefs[key] = ref
	s.klineRefsMu.Unlock()

	go s.forwardKline(ctx, sub, ref)
	return ref, nil
}

// GetTicks subscribes to ticks and returns a TickRef.
// Counterpart: Python ticks = api.get_tick_serial("SHFE.rb2501", 200)
func (s *SyncApi) GetTicks(
	ctx context.Context,
	symbol string,
	dataLength int,
	opts ...api.TickSubOption,
) (*TickRef, error) {
	sub, err := s.market.SubscribeTicks(ctx, symbol, dataLength, opts...)
	if err != nil {
		return nil, err
	}
	s.trackSub(sub)
	ref := &TickRef{symbol: symbol, market: s.market}
	go s.forwardTick(ctx, sub, ref)
	return ref, nil
}

// GetAccount returns a live reference to the account balance.
// Requires Login to have been called first.
// Counterpart: Python account = api.get_account()
func (s *SyncApi) GetAccount() (*AccountRef, error) {
	if s.session == nil {
		return nil, fmt.Errorf("syncapi: not logged in, call Login first")
	}
	return &AccountRef{session: s.session}, nil
}

// GetPosition returns a live reference to a position.
// Counterpart: Python pos = api.get_position("SHFE.rb2501")
func (s *SyncApi) GetPosition(symbol string) (*PositionRef, error) {
	if s.session == nil {
		return nil, fmt.Errorf("syncapi: not logged in, call Login first")
	}
	return &PositionRef{symbol: symbol, session: s.session}, nil
}

// GetOrder returns a live reference to an order.
func (s *SyncApi) GetOrder(orderID string) (*SyncOrderRef, error) {
	if s.session == nil {
		return nil, fmt.Errorf("syncapi: not logged in, call Login first")
	}
	return &SyncOrderRef{orderID: orderID, session: s.session}, nil
}

// ---------------------------------------------------------------
// Login — trade login + auto-subscribe trade events
// ---------------------------------------------------------------

// Login logs in to a trade account and auto-subscribes to all trade
// events (account, position, order, fill), forwarding them into the
// unified event channel so WaitUpdate + IsChanging covers trade data.
func (s *SyncApi) Login(ctx context.Context, req api.TradeLoginReq) (api.TradeSession, error) {
	session, err := s.trade.Login(ctx, req)
	if err != nil {
		return nil, err
	}
	s.session = session
	accountID := session.AccountID()

	// Account updates
	if sub, err := session.SubscribeAccounts(ctx); err == nil {
		s.trackSub(sub)
		go func() {
			for range sub.C() {
				s.eventCh <- syncEvent{key: refKey{kind: refAccount, accountID: accountID}}
			}
		}()
	}

	// Position updates
	if sub, err := session.SubscribePositions(ctx); err == nil {
		s.trackSub(sub)
		go func() {
			for ev := range sub.C() {
				s.eventCh <- syncEvent{key: refKey{kind: refPosition, accountID: accountID, symbol: ev.Symbol}}
			}
		}()
	}

	// Order updates
	if sub, err := session.SubscribeOrders(ctx); err == nil {
		s.trackSub(sub)
		go func() {
			for ev := range sub.C() {
				s.eventCh <- syncEvent{key: refKey{kind: refOrder, orderID: ev.OrderID}}
			}
		}()
	}

	// Trade fill updates
	if sub, err := session.SubscribeTrades(ctx); err == nil {
		s.trackSub(sub)
		go func() {
			for ev := range sub.C() {
				s.eventCh <- syncEvent{key: refKey{kind: refTradeFill, orderID: ev.TradeID}}
			}
		}()
	}

	return session, nil
}

// ---------------------------------------------------------------
// WaitUpdate + IsChanging — core methods
// ---------------------------------------------------------------

// WaitUpdate blocks until the next data change event.
// Semantics match Python api.wait_update():
//  1. Reset change markers
//  2. Block until next event
//  3. Mark changed refKeys (drains buffered events in same batch)
//
// Counterpart: Python api.wait_update()
func (s *SyncApi) WaitUpdate() error {
	return s.WaitUpdateContext(context.Background())
}

// WaitUpdateContext is like WaitUpdate but respects context cancellation.
func (s *SyncApi) WaitUpdateContext(ctx context.Context) error {
	// Reset
	s.mu.Lock()
	for k := range s.changed {
		delete(s.changed, k)
	}
	s.mu.Unlock()

	// Block
	select {
	case ev, ok := <-s.eventCh:
		if !ok {
			return fmt.Errorf("syncapi: event channel closed")
		}
		s.mu.Lock()
		s.mergeEvent(ev)
		// Drain any additional buffered events in the same batch
	drain:
		for {
			select {
			case ev2, ok2 := <-s.eventCh:
				if !ok2 {
					break drain
				}
				s.mergeEvent(ev2)
			default:
				break drain
			}
		}
		s.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// mergeEvent merges a syncEvent into the changed map.
// Must be called with s.mu held.
func (s *SyncApi) mergeEvent(ev syncEvent) {
	existing, ok := s.changed[ev.key]
	if !ok || existing == nil {
		// First event for this key, or previous had nil fields — just set.
		s.changed[ev.key] = ev.changedFields
		return
	}
	if ev.changedFields == nil {
		// New event has no field info — degrade to nil (object-level).
		s.changed[ev.key] = nil
		return
	}
	// Merge field sets.
	for k, v := range ev.changedFields {
		existing[k] = v
	}
}

// IsChanging reports whether a Ref changed in the latest WaitUpdate round.
//
// Without keys: object-level check (any field changed → true).
//
//	sa.IsChanging(quote)
//
// With keys: field-level check (only returns true if specified field(s) changed).
//
//	sa.IsChanging(quote, "last_price")
//	sa.IsChanging(quote, "bid_price1", "ask_price1")
//
// For KlineBar refs, checks whether the specific bar was the one that changed.
//
//	sa.IsChanging(klines.Bar(-1), "datetime")  // new bar produced?
//
// Counterpart: Python api.is_changing(quote), api.is_changing(quote, "last_price")
func (s *SyncApi) IsChanging(ref Ref, keys ...string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := ref.refKey()

	// ── KlineBar: bar-level check (§8.8.5) ──
	if key.kind == refKlineBar {
		return s.isChangingKlineBar(key, keys)
	}

	// ── Normal Ref (Quote, Kline series, Position, etc.) ──
	fields, exists := s.changed[key]
	if !exists {
		return false
	}
	if len(keys) == 0 {
		return true
	}
	// changedFields nil → degrade to object-level (always true)
	if fields == nil {
		return true
	}
	for _, k := range keys {
		if fields[k] {
			return true
		}
	}
	return false
}

// isChangingKlineBar handles IsChanging for KlineBar refs.
// Must be called with s.mu held for reading.
func (s *SyncApi) isChangingKlineBar(key refKey, keys []string) bool {
	seriesKey := refKey{kind: refKline, symbol: key.symbol, duration: key.duration}
	fields, exists := s.changed[seriesKey]
	if !exists {
		return false // series didn't change at all
	}

	s.klineRefsMu.RLock()
	kRef, ok := s.klineRefs[seriesKey]
	s.klineRefsMu.RUnlock()
	if !ok {
		return false
	}

	kRef.mu.RLock()
	frame := kRef.lastFrame
	changedBarID := kRef.changedBarID
	kRef.mu.RUnlock()

	// Resolve requested index to actual bar ID
	idx := key.barIndex
	if idx < 0 {
		idx = len(frame) + idx
	}
	if idx < 0 || idx >= len(frame) {
		return false
	}
	if frame[idx].ID != changedBarID {
		return false // a different bar changed
	}

	if len(keys) == 0 {
		return true
	}
	if fields == nil {
		return true
	}
	for _, k := range keys {
		if fields[k] {
			return true
		}
	}
	return false
}

// Close releases all subscriptions.
func (s *SyncApi) Close() error {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	for _, sub := range s.subs {
		_ = sub.Close()
	}
	s.subs = nil
	return nil
}

// ---------------------------------------------------------------
// internal helpers
// ---------------------------------------------------------------

func (s *SyncApi) trackSub(sub closeable) {
	s.subsMu.Lock()
	s.subs = append(s.subs, sub)
	s.subsMu.Unlock()
}

func (s *SyncApi) forwardQuote(ctx context.Context, sub api.QuoteSub, key refKey) {
	ch := sub.C()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			s.eventCh <- syncEvent{key: key}
		}
	}
}

func (s *SyncApi) forwardKline(ctx context.Context, sub api.KlineSub, ref *KlineRef) {
	key := ref.refKey()
	ch := sub.C()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			// Update frame cache
			ref.updateFrame(ev.Frame1D)

			// Record which bar changed and the event kind (§8.8.6)
			ref.mu.Lock()
			ref.lastEventKind = ev.Kind
			ref.changedBarID = ev.Row.MainID
			ref.mu.Unlock()

			// Build ChangedFields based on event kind
			var cf ChangedFields
			switch ev.Kind {
			case api.EventAppend:
				// New bar: all fields are considered changed (matches Python behavior)
				cf = ChangedFields{
					"datetime": true, "open": true, "high": true,
					"low": true, "close": true, "volume": true,
					"open_oi": true, "close_oi": true,
				}
			case api.EventUpdate:
				// Update existing bar: typically high/low/close/volume change
				cf = ChangedFields{
					"high": true, "low": true, "close": true,
					"volume": true, "close_oi": true,
				}
			}

			s.eventCh <- syncEvent{key: key, changedFields: cf}
		}
	}
}

func (s *SyncApi) forwardTick(ctx context.Context, sub api.TickSub, ref *TickRef) {
	key := ref.refKey()
	ch := sub.C()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			ref.updateFrame(ev.Frame)
			s.eventCh <- syncEvent{key: key}
		}
	}
}
