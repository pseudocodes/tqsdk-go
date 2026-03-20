package store

import (
	"sync"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
)

type TradeSessionData struct {
	SessionID     string
	AccountID     string
	Account       api.Account
	AccountOK     bool
	Positions     map[string]api.Position
	Orders        map[string]api.Order
	Trades        map[string]api.Trade
	RiskRules     map[string]api.RiskManagementRule
	RiskData      map[string]api.RiskManagementData
	TradeMoreData bool
	Version       uint64
	UpdatedAt     time.Time
}

type TradeStore struct {
	mu sync.RWMutex

	version   uint64
	updatedAt time.Time

	sessions map[string]*TradeSessionData
}

func NewTradeStore() *TradeStore {
	return &TradeStore{
		sessions: map[string]*TradeSessionData{},
	}
}

func (s *TradeStore) ensureSessionLocked(sessionID string, accountID string) *TradeSessionData {
	ss, ok := s.sessions[sessionID]
	if ok {
		if accountID != "" {
			ss.AccountID = accountID
		}
		return ss
	}
	ss = &TradeSessionData{
		SessionID:     sessionID,
		AccountID:     accountID,
		Positions:     map[string]api.Position{},
		Orders:        map[string]api.Order{},
		Trades:        map[string]api.Trade{},
		RiskRules:     map[string]api.RiskManagementRule{},
		RiskData:      map[string]api.RiskManagementData{},
		TradeMoreData: true,
	}
	s.sessions[sessionID] = ss
	return ss
}

func (s *TradeStore) TouchSession(sessionID string, accountID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.ensureSessionLocked(sessionID, accountID)
	s.bumpLocked()
}

func (s *TradeStore) DropSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
	s.bumpLocked()
}

func (s *TradeStore) SetTradeMoreData(sessionID string, accountID string, v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.ensureSessionLocked(sessionID, accountID)
	ss.TradeMoreData = v
	ss.Version++
	ss.UpdatedAt = time.Now()
	s.bumpLocked()
}

func (s *TradeStore) UpsertAccount(sessionID string, accountID string, v api.Account) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.ensureSessionLocked(sessionID, accountID)
	ss.Account = v
	ss.AccountOK = true
	ss.Version++
	ss.UpdatedAt = time.Now()
	s.bumpLocked()
}

func (s *TradeStore) UpsertPosition(sessionID string, accountID string, symbol string, v api.Position) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.ensureSessionLocked(sessionID, accountID)
	ss.Positions[symbol] = v
	ss.Version++
	ss.UpdatedAt = time.Now()
	s.bumpLocked()
}

func (s *TradeStore) DeletePosition(sessionID string, accountID string, symbol string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.ensureSessionLocked(sessionID, accountID)
	delete(ss.Positions, symbol)
	ss.Version++
	ss.UpdatedAt = time.Now()
	s.bumpLocked()
}

func (s *TradeStore) UpsertOrder(sessionID string, accountID string, orderID string, v api.Order) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.ensureSessionLocked(sessionID, accountID)
	ss.Orders[orderID] = v
	ss.Version++
	ss.UpdatedAt = time.Now()
	s.bumpLocked()
}

func (s *TradeStore) DeleteOrder(sessionID string, accountID string, orderID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.ensureSessionLocked(sessionID, accountID)
	delete(ss.Orders, orderID)
	ss.Version++
	ss.UpdatedAt = time.Now()
	s.bumpLocked()
}

func (s *TradeStore) UpsertTrade(sessionID string, accountID string, tradeID string, v api.Trade) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.ensureSessionLocked(sessionID, accountID)
	ss.Trades[tradeID] = v
	ss.Version++
	ss.UpdatedAt = time.Now()
	s.bumpLocked()
}

func (s *TradeStore) DeleteTrade(sessionID string, accountID string, tradeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.ensureSessionLocked(sessionID, accountID)
	delete(ss.Trades, tradeID)
	ss.Version++
	ss.UpdatedAt = time.Now()
	s.bumpLocked()
}

func (s *TradeStore) UpsertRiskRule(sessionID string, accountID string, exchangeID string, v api.RiskManagementRule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.ensureSessionLocked(sessionID, accountID)
	ss.RiskRules[exchangeID] = v
	ss.Version++
	ss.UpdatedAt = time.Now()
	s.bumpLocked()
}

func (s *TradeStore) UpsertRiskData(sessionID string, accountID string, symbol string, v api.RiskManagementData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.ensureSessionLocked(sessionID, accountID)
	ss.RiskData[symbol] = v
	ss.Version++
	ss.UpdatedAt = time.Now()
	s.bumpLocked()
}

func (s *TradeStore) SessionSnapshot(sessionID string) (TradeSessionData, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ss, ok := s.sessions[sessionID]
	if !ok {
		return TradeSessionData{}, false
	}
	cp := TradeSessionData{
		SessionID:     ss.SessionID,
		AccountID:     ss.AccountID,
		Account:       ss.Account,
		AccountOK:     ss.AccountOK,
		Positions:     make(map[string]api.Position, len(ss.Positions)),
		Orders:        make(map[string]api.Order, len(ss.Orders)),
		Trades:        make(map[string]api.Trade, len(ss.Trades)),
		RiskRules:     make(map[string]api.RiskManagementRule, len(ss.RiskRules)),
		RiskData:      make(map[string]api.RiskManagementData, len(ss.RiskData)),
		TradeMoreData: ss.TradeMoreData,
		Version:       ss.Version,
		UpdatedAt:     ss.UpdatedAt,
	}
	for k, v := range ss.Positions {
		cp.Positions[k] = v
	}
	for k, v := range ss.Orders {
		cp.Orders[k] = v
	}
	for k, v := range ss.Trades {
		cp.Trades[k] = v
	}
	for k, v := range ss.RiskRules {
		cp.RiskRules[k] = v
	}
	for k, v := range ss.RiskData {
		cp.RiskData[k] = v
	}
	return cp, true
}

func (s *TradeStore) OrderSnapshot(sessionID string, orderID string) (api.Order, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ss, ok := s.sessions[sessionID]
	if !ok {
		return api.Order{}, false
	}
	v, ok := ss.Orders[orderID]
	return v, ok
}

func (s *TradeStore) bumpLocked() {
	s.version++
	s.updatedAt = time.Now()
}
