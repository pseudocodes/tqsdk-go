package api

import (
	"context"
	"net/http"
	"time"
)

// NoopAuthService is an AuthService that permits everything without
// contacting any remote server. Useful for demos and tests.
type NoopAuthService struct {
	session AuthSession
}

func NewNoopAuthService() *NoopAuthService {
	return &NoopAuthService{
		session: AuthSession{
			UserName:    "noop",
			AuthID:      "noop",
			AccessToken: "noop",
			Features:    map[string]bool{"*": true},
			Accounts:    map[string]bool{},
			ExpireAt:    time.Now().Add(365 * 24 * time.Hour),
		},
	}
}

func (s *NoopAuthService) Login(_ context.Context, user, _ string) (AuthSession, error) {
	s.session.UserName = user
	return s.session, nil
}

func (s *NoopAuthService) Session() (AuthSession, bool) {
	return s.session, true
}

func (s *NoopAuthService) Header() http.Header {
	h := http.Header{}
	h.Set("User-Agent", "tqsdk-go-noop")
	return h
}

func (s *NoopAuthService) EnsureFeature(_ string) error                                  { return nil }
func (s *NoopAuthService) EnsureMDGrants(_ []string) error                               { return nil }
func (s *NoopAuthService) EnsureTDGrants(_ string) error                                 { return nil }
func (s *NoopAuthService) EnsureAccountGrant(_ context.Context, _ string, _ bool) error  { return nil }
func (s *NoopAuthService) ResolveMDURL(_ context.Context, _ bool, _ bool) (string, error) { return "", nil }
func (s *NoopAuthService) ResolveTDURL(_ context.Context, _, _ string) (TDRoute, error)  { return TDRoute{}, nil }
