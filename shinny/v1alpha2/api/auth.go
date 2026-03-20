package api

import (
	"context"
	"net/http"
	"time"
)

type AuthSession struct {
	UserName    string
	AuthID      string
	AccessToken string
	Features    map[string]bool
	Accounts    map[string]bool
	ExpireAt    time.Time
	ProductType string
	ExpireDays  int
}

type TDRoute struct {
	URL        string
	BrokerType string
	SMType     string
	SMConfig   string
}

type AuthService interface {
	Login(ctx context.Context, user, password string) (AuthSession, error)
	Session() (AuthSession, bool)
	Header() http.Header

	EnsureFeature(feature string) error
	EnsureMDGrants(symbols []string) error
	EnsureTDGrants(symbol string) error
	EnsureAccountGrant(ctx context.Context, accountID string, autoAdd bool) error

	ResolveMDURL(ctx context.Context, stock bool, backtest bool) (string, error)
	ResolveTDURL(ctx context.Context, brokerID, accountID string) (TDRoute, error)
}
