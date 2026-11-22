package core

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/auth"
)

type blockingSource struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingSource() *blockingSource {
	return &blockingSource{closed: make(chan struct{})}
}

func (s *blockingSource) Read(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closed:
		return nil, io.EOF
	}
}

func (s *blockingSource) Write(context.Context, []byte) error { return nil }

func (s *blockingSource) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func TestAccountStartPassesNoAutoAdd(t *testing.T) {
	rt := NewRuntime()
	defer rt.Close()

	var got auth.Request
	provider := auth.ProviderFunc(func(_ context.Context, r auth.Request) (auth.Access, error) {
		got = r
		return auth.Access{Endpoint: "wss://example", Features: auth.Granted("futr")}, nil
	})
	td, err := NewTrading(context.Background(), rt, TradingConfig{Auth: provider})
	if err != nil {
		t.Fatal(err)
	}
	defer td.Close()

	h, err := td.PrepareAccount(AccountConfig{
		ID:        "kq",
		BrokerID:  "快期模拟",
		UserName:  "test-auth-id",
		Password:  "test-auth-id",
		Source:    newBlockingSource(),
		NoAutoAdd: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	if !got.NoAutoAdd {
		t.Fatal("Start 未把 NoAutoAdd 传给授权请求")
	}
	if got.BrokerID != "快期模拟" || got.AccountID != "test-auth-id" {
		t.Fatalf("授权请求 = %+v", got)
	}
}
