package store

import (
	"sync"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
)

type TradingStatusStore struct {
	mu    sync.RWMutex
	items map[string]api.TradingStatus
}

func NewTradingStatusStore() *TradingStatusStore {
	return &TradingStatusStore{items: map[string]api.TradingStatus{}}
}

func (s *TradingStatusStore) Upsert(v api.TradingStatus) {
	if v.Symbol == "" {
		return
	}
	s.mu.Lock()
	s.items[v.Symbol] = v
	s.mu.Unlock()
}

func (s *TradingStatusStore) Get(symbol string) (api.TradingStatus, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.items[symbol]
	return v, ok
}
