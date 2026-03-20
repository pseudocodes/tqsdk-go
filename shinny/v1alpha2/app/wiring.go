package app

import (
	"net/http"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/domain"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/store"
)

// Config holds the build-time parameters for constructing services.
// This is the only struct end-users need to fill in.
type Config struct {
	User      string // auth user
	Password  string // auth password
	AuthURL   string // optional, defaults to https://auth.shinnytech.com
	MarketURL string // optional, auto-resolved if empty
	AutoAdd   bool   // auto-add account grants
}

type Wiring struct {
	Auth   api.AuthService
	Market api.MarketService
	Trade  api.TradeService
}

func NewWiring(cfg Config) *Wiring {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	authSvc := domain.NewAuthService(httpClient, cfg.AuthURL)
	marketStore := store.NewMarketStore()
	tradeStore := store.NewTradeStore()
	marketSvc := domain.NewMarketService(authSvc, marketStore, cfg.MarketURL, httpClient)
	tradeSvc := domain.NewTradeService(authSvc, tradeStore, cfg.AutoAdd, marketSvc)
	return &Wiring{Auth: authSvc, Market: marketSvc, Trade: tradeSvc}
}

// NewDefaultClient creates a fully-wired live-trading Client.
// This is the primary entry point for end-users.
func NewDefaultClient(cfg Config) (api.Client, error) {
	w := NewWiring(cfg)
	apiCfg := api.Config{
		AuthService:   w.Auth,
		MarketService: w.Market,
		TradeService:  w.Trade,
	}
	if cfg.User != "" && cfg.Password != "" {
		apiCfg.AutoLogin = &api.AuthCredential{User: cfg.User, Password: cfg.Password}
	}
	return api.NewClient(apiCfg)
}
