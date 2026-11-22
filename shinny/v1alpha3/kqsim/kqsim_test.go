package kqsim

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
)

const testAuthID = "test-auth-id"

type fakeAuth struct {
	id       string
	features map[string]bool
}

func (f fakeAuth) AuthID() string { return f.id }

func (f fakeAuth) HasFeature(feature string) bool { return f.features[feature] }

func TestAccountConfigMain(t *testing.T) {
	a := &Account{cfg: Config{}}
	cfg, err := a.accountConfig(testAuthID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ID != "KQ" || cfg.BrokerID != BrokerID {
		t.Fatalf("id/broker = %q/%q", cfg.ID, cfg.BrokerID)
	}
	if cfg.UserName != testAuthID || cfg.Password != testAuthID {
		t.Fatalf("credentials = %q/%q", cfg.UserName, cfg.Password)
	}
	if !cfg.NoAutoAdd {
		t.Fatal("NoAutoAdd should be true")
	}
}

func TestAccountConfigExplicitID(t *testing.T) {
	a := &Account{cfg: Config{ID: "futures-kq"}}
	cfg, err := a.accountConfig(testAuthID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ID != "futures-kq" {
		t.Fatalf("id = %q", cfg.ID)
	}
}

func TestAccountConfigAux(t *testing.T) {
	a := &Account{cfg: Config{Number: 7}}
	cfg, err := a.accountConfig(testAuthID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ID != "KQ007" {
		t.Fatalf("id = %q", cfg.ID)
	}
	if got, want := cfg.UserName, testAuthID+"007"; got != want {
		t.Fatalf("user = %q, want %q", got, want)
	}
	if want := "shinnytech%03d" + fmt.Sprintf("%03d", 7); cfg.Password != want {
		t.Fatalf("password = %q, want %q", cfg.Password, want)
	}
	if !cfg.NoAutoAdd {
		t.Fatal("NoAutoAdd should be true")
	}
}

func TestAccountConfigRejectsBadInput(t *testing.T) {
	cases := []struct {
		name   string
		cfg    Config
		authID string
	}{
		{name: "empty auth id", cfg: Config{}, authID: ""},
		{name: "negative number", cfg: Config{Number: -1}, authID: testAuthID},
		{name: "number too large", cfg: Config{Number: 100}, authID: testAuthID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Account{cfg: tc.cfg}
			if _, err := a.accountConfig(tc.authID); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestPrepareAuxRequiresFeature(t *testing.T) {
	a := New(fakeAuth{id: testAuthID}, Config{Number: 1})
	// 权限检查在访问 Trading 之前完成，因此这里可以传 nil。
	_, err := a.Prepare(nil)
	if err == nil || !strings.Contains(err.Error(), "tq_ma") {
		t.Fatalf("err = %v, want tq_ma error", err)
	}
}

func TestPrepareRegistersAccount(t *testing.T) {
	rt := core.NewRuntime()
	td, err := core.NewTrading(context.Background(), rt, core.TradingConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer td.Close()
	defer rt.Close()

	a := New(fakeAuth{id: testAuthID}, Config{})
	h, err := a.Prepare(td)
	if err != nil {
		t.Fatal(err)
	}
	if h.ID() != "KQ" || h.UserName() != testAuthID {
		t.Fatalf("handle = %q/%q", h.ID(), h.UserName())
	}
}

func TestPrepareAuxRegistersAccount(t *testing.T) {
	rt := core.NewRuntime()
	td, err := core.NewTrading(context.Background(), rt, core.TradingConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer td.Close()
	defer rt.Close()

	a := New(fakeAuth{id: testAuthID, features: map[string]bool{"tq_ma": true}}, Config{Number: 3})
	h, err := a.Prepare(td)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := h.UserName(), testAuthID+"003"; h.ID() != "KQ003" || got != want {
		t.Fatalf("handle = %q/%q, want KQ003/%q", h.ID(), got, want)
	}
}
