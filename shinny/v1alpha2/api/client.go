package api

import (
	"context"
	"sync"
)

// Config holds the injected services and optional auto-login credentials
// needed by Client. Build-time parameters (URLs, AutoAdd, etc.) belong in
// app.Config, not here.
type Config struct {
	AuthService   AuthService
	MarketService MarketService
	TradeService  TradeService

	// AutoLogin optionally triggers an auth login during Client.Run.
	AutoLogin *AuthCredential
}

// AuthCredential holds user/password for auto-login during Client.Run.
type AuthCredential struct {
	User     string
	Password string
}

type Client interface {
	Run(ctx context.Context) error
	Close() error
	Market() MarketService
	Trade() TradeService
	Auth() AuthService
}

type client struct {
	cfg Config

	authSvc   AuthService
	marketSvc MarketService
	tradeSvc  TradeService

	mu      sync.RWMutex
	started bool
	closed  bool
}

func NewClient(cfg Config) (Client, error) {
	if cfg.AuthService == nil {
		return nil, NewError(ErrInvalidArgument, "AuthService is required", nil)
	}
	if cfg.MarketService == nil {
		return nil, NewError(ErrInvalidArgument, "MarketService is required", nil)
	}
	if cfg.TradeService == nil {
		return nil, NewError(ErrInvalidArgument, "TradeService is required", nil)
	}
	return &client{
		cfg:       cfg,
		authSvc:   cfg.AuthService,
		marketSvc: cfg.MarketService,
		tradeSvc:  cfg.TradeService,
	}, nil
}

func (c *client) Run(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return NewError(ErrDisconnected, "client already closed", nil)
	}
	if c.started {
		c.mu.Unlock()
		return nil
	}
	c.started = true
	c.mu.Unlock()

	if cred := c.cfg.AutoLogin; cred != nil {
		if cred.User == "" || cred.Password == "" {
			return NewError(ErrAuthFailed, "both auth user/password are required", nil)
		}
		if _, err := c.authSvc.Login(ctx, cred.User, cred.Password); err != nil {
			return err
		}
	}

	if c.marketSvc != nil {
		if err := c.marketSvc.Start(ctx); err != nil {
			return err
		}
	}
	type starter interface {
		Start(ctx context.Context) error
	}
	if s, ok := c.tradeSvc.(starter); ok {
		if err := s.Start(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (c *client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	type closer interface {
		Close() error
	}
	if cl, ok := c.marketSvc.(closer); ok {
		_ = cl.Close()
	}
	if cl, ok := c.tradeSvc.(closer); ok {
		_ = cl.Close()
	}
	return nil
}

func (c *client) Market() MarketService { return c.marketSvc }

func (c *client) Trade() TradeService { return c.tradeSvc }

func (c *client) Auth() AuthService { return c.authSvc }
