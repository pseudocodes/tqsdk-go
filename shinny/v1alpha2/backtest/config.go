package backtest

import (
	"context"
	"net/http"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/backtest/runtime"
)

type Config = runtime.RuntimeConfig

type NoopAuthService struct {
	session api.AuthSession
}

func NewNoopAuthService() *NoopAuthService {
	return &NoopAuthService{
		session: api.AuthSession{
			UserName:    "backtest",
			AuthID:      "backtest",
			AccessToken: "backtest",
			Features:    map[string]bool{"*": true},
			Accounts:    map[string]bool{},
			ExpireAt:    time.Now().Add(365 * 24 * time.Hour),
		},
	}
}

func (s *NoopAuthService) Login(ctx context.Context, user, password string) (api.AuthSession, error) {
	_ = ctx
	s.session.UserName = user
	return s.session, nil
}

func (s *NoopAuthService) Session() (api.AuthSession, bool) {
	return s.session, true
}

func (s *NoopAuthService) Header() http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer backtest")
	return h
}

func (s *NoopAuthService) EnsureFeature(feature string) error {
	_ = feature
	return nil
}

func (s *NoopAuthService) EnsureMDGrants(symbols []string) error {
	_ = symbols
	return nil
}

func (s *NoopAuthService) EnsureTDGrants(symbol string) error {
	_ = symbol
	return nil
}

func (s *NoopAuthService) EnsureAccountGrant(ctx context.Context, accountID string, autoAdd bool) error {
	_, _, _ = ctx, accountID, autoAdd
	return nil
}

func (s *NoopAuthService) ResolveMDURL(ctx context.Context, stock bool, backtest bool) (string, error) {
	_, _, _ = ctx, stock, backtest
	return "", nil
}

func (s *NoopAuthService) ResolveTDURL(ctx context.Context, brokerID, accountID string) (api.TDRoute, error) {
	_, _, _ = ctx, brokerID, accountID
	return api.TDRoute{}, nil
}
