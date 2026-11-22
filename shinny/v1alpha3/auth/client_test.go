package auth

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func fakeToken(t *testing.T, sub string, features, accounts []string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"sub": sub,
		"grants": map[string]any{
			"features": features,
			"accounts": accounts,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestDecodeClaims(t *testing.T) {
	token := fakeToken(t, "test-auth-id", []string{"futr", "tq_ma"}, []string{"022631"})
	claims, err := decodeClaims(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Sub != "test-auth-id" {
		t.Fatalf("sub = %q, want test-auth-id", claims.Sub)
	}
	if len(claims.Grants.Features) != 2 || claims.Grants.Features[1] != "tq_ma" {
		t.Fatalf("features = %v", claims.Grants.Features)
	}
	if len(claims.Grants.Accounts) != 1 || claims.Grants.Accounts[0] != "022631" {
		t.Fatalf("accounts = %v", claims.Grants.Accounts)
	}
}

func TestDecodeClaimsRejectsBadToken(t *testing.T) {
	if _, err := decodeClaims("not-a-jwt"); err == nil {
		t.Fatal("expected error")
	}
}

func TestAuthIDBeforeLogin(t *testing.T) {
	if got := NewClient("user", "pass").AuthID(); got != "" {
		t.Fatalf("AuthID = %q, want empty", got)
	}
}

func TestAuthorizeKQSimNoAutoAdd(t *testing.T) {
	token := fakeToken(t, "test-auth-id", []string{"futr"}, []string{"022631"})
	var grantPuts atomic.Int32
	var tokenCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/protocol/openid-connect/token"):
			tokenCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"refresh-2"}`, token)
		case strings.HasPrefix(r.URL.Path, "/auth/realms/shinnytech/rest/update-grant-accounts/"):
			if r.Method != http.MethodPut {
				http.Error(w, "method", http.StatusMethodNotAllowed)
				return
			}
			grantPuts.Add(1)
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, ".json"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"快期模拟":{"url":"wss://kq.example/trade","category":["TQ"]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient("user", "pass")
	c.AuthURL, c.FilesURL = srv.URL, srv.URL

	access, err := c.Authorize(context.Background(), Request{
		Service: Trade, BrokerID: "快期模拟", AccountID: "test-auth-id", NoAutoAdd: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if access.Endpoint != "wss://kq.example/trade" {
		t.Fatalf("endpoint = %q", access.Endpoint)
	}
	if got := c.AuthID(); got != "test-auth-id" {
		t.Fatalf("AuthID = %q, want test-auth-id", got)
	}
	if got := grantPuts.Load(); got != 0 {
		t.Fatalf("NoAutoAdd 仍调用了 update-grant-accounts %d 次", got)
	}
	if got := tokenCalls.Load(); got != 1 {
		t.Fatalf("token calls = %d, want 1", got)
	}

	// auto_add=true 的普通账户仍然要加入授权列表并刷新 token。
	if _, err := c.Authorize(context.Background(), Request{
		Service: Trade, BrokerID: "快期模拟", AccountID: "test-auth-id",
	}); err != nil {
		t.Fatal(err)
	}
	if got := grantPuts.Load(); got != 1 {
		t.Fatalf("auto_add 账户 update-grant-accounts 次数 = %d, want 1", got)
	}
	if got := tokenCalls.Load(); got != 2 {
		t.Fatalf("token calls = %d, want 2", got)
	}
}
