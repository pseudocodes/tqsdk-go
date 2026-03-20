package domain

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"
)

var (
	futureExchanges = map[string]struct{}{"CFFEX": {}, "SHFE": {}, "DCE": {}, "CZCE": {}, "INE": {}, "GFEX": {}, "KQ": {}, "KQD": {}}
	stockExchanges  = map[string]struct{}{"SSE": {}, "SZSE": {}, "CSI": {}}
)

type authService struct {
	mu sync.RWMutex

	httpClient *http.Client
	authURL    string

	userName     string
	password     string
	refreshToken string
	session      api.AuthSession
	ok           bool
}

func NewAuthService(httpClient *http.Client, authURL string) api.AuthService {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if strings.TrimSpace(authURL) == "" {
		authURL = "https://auth.shinnytech.com"
	}
	return &authService{
		httpClient: httpClient,
		authURL:    strings.TrimRight(authURL, "/"),
	}
}

func (s *authService) Login(ctx context.Context, user, password string) (api.AuthSession, error) {
	user = strings.TrimSpace(user)
	password = strings.TrimSpace(password)
	if user == "" || password == "" {
		return api.AuthSession{}, api.NewError(api.ErrAuthFailed, "empty user/password", nil)
	}

	// Fast path: if already logged in with the same credentials, return cached session.
	s.mu.RLock()
	if s.ok && s.userName == user && s.password == password {
		sess := s.session
		s.mu.RUnlock()
		return sess, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	s.userName = user
	s.password = password
	s.mu.Unlock()

	accessToken, refreshToken, err := s.requestToken(ctx, map[string]string{
		"grant_type": "password",
		"username":   user,
		"password":   password,
	})
	if err != nil {
		return api.AuthSession{}, err
	}
	sess, err := parseAccessToken(user, accessToken)
	if err != nil {
		return api.AuthSession{}, api.NewError(api.ErrAuthFailed, "decode access token failed", err)
	}

	s.mu.Lock()
	s.refreshToken = refreshToken
	s.session = sess
	s.ok = true
	s.mu.Unlock()
	return sess, nil
}

func (s *authService) Session() (api.AuthSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.session, s.ok
}

func (s *authService) Header() http.Header {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := make(http.Header)
	h.Set("User-Agent", "tqsdk-go")
	h.Set("Accept", "application/json")
	if s.ok && s.session.AccessToken != "" {
		h.Set("Authorization", "Bearer "+s.session.AccessToken)
	}
	return h
}

func (s *authService) EnsureFeature(feature string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.ok {
		return api.NewError(api.ErrAuthRequired, "auth session not initialized", nil)
	}
	if !s.session.Features[feature] {
		return api.NewError(api.ErrPermissionDenied, fmt.Sprintf("feature %s not granted", feature), nil)
	}
	return nil
}

func (s *authService) EnsureMDGrants(symbols []string) error {
	if len(symbols) == 0 {
		return api.NewError(api.ErrInvalidSymbol, "empty symbols", nil)
	}
	for _, symbol := range symbols {
		symbol = strings.TrimSpace(symbol)
		if symbol == "" {
			return api.NewError(api.ErrInvalidSymbol, "empty symbol", nil)
		}
		ex := strings.ToUpper(strings.TrimSpace(strings.Split(symbol, ".")[0]))
		if _, ok := futureExchanges[ex]; ok {
			if err := s.EnsureFeature("futr"); err != nil {
				return err
			}
			continue
		}
		if _, ok := stockExchanges[ex]; ok {
			if err := s.EnsureFeature("sec"); err != nil {
				return err
			}
			continue
		}
		if ex == "SSE" && (symbol == "SSE.000016" || symbol == "SSE.000300" || symbol == "SSE.000905" || symbol == "SSE.000852") {
			if err := s.EnsureFeature("lmt_idx"); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *authService) EnsureTDGrants(symbol string) error {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return api.NewError(api.ErrInvalidSymbol, "empty symbol", nil)
	}
	ex := strings.ToUpper(strings.TrimSpace(strings.Split(symbol, ".")[0]))
	if _, ok := stockExchanges[ex]; ok {
		return s.EnsureFeature("sec")
	}
	if _, ok := futureExchanges[ex]; ok {
		return s.EnsureFeature("futr")
	}
	return api.NewError(api.ErrPermissionDenied, "symbol has no TD grant", nil)
}

func (s *authService) EnsureAccountGrant(ctx context.Context, accountID string, autoAdd bool) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return api.NewError(api.ErrInvalidAccount, "empty account id", nil)
	}

	s.mu.RLock()
	ok := s.ok
	hasAccount := s.session.Accounts[accountID]
	s.mu.RUnlock()
	if !ok {
		return api.NewError(api.ErrAuthRequired, "auth session not initialized", nil)
	}
	if hasAccount {
		return nil
	}
	if !autoAdd {
		return api.NewError(api.ErrPermissionDenied, "account not granted", nil)
	}

	if err := s.addAccount(ctx, accountID); err != nil {
		return err
	}
	// add_account 后重新登录刷新 grants
	s.mu.RLock()
	user := s.userName
	pass := s.password
	s.mu.RUnlock()
	_, err := s.Login(ctx, user, pass)
	return err
}

func (s *authService) ResolveMDURL(ctx context.Context, stock bool, backtest bool) (string, error) {
	u := "https://api.shinnytech.com/ns"
	q := url.Values{}
	q.Set("stock", fmt.Sprintf("%v", stock))
	q.Set("backtest", fmt.Sprintf("%v", backtest))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?"+q.Encode(), nil)
	if err != nil {
		return "", api.NewError(api.ErrAuthFailed, "build md url request failed", err)
	}
	req.Header = s.Header()
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", api.NewError(api.ErrAuthFailed, "request md url failed", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", api.NewError(api.ErrAuthFailed, "request md url failed", fmt.Errorf("status=%d body=%s", resp.StatusCode, string(body)))
	}
	var out struct {
		MDURL string `json:"mdurl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", api.NewError(api.ErrAuthFailed, "decode md url failed", err)
	}
	if strings.TrimSpace(out.MDURL) == "" {
		return "", api.NewError(api.ErrAuthFailed, "empty mdurl response", nil)
	}
	return out.MDURL, nil
}

func (s *authService) ResolveTDURL(ctx context.Context, brokerID, accountID string) (api.TDRoute, error) {
	brokerID = strings.TrimSpace(brokerID)
	accountID = strings.TrimSpace(accountID)
	if brokerID == "" || accountID == "" {
		return api.TDRoute{}, api.NewError(api.ErrInvalidArgument, "empty broker/account", nil)
	}
	u := fmt.Sprintf("https://files.shinnytech.com/%s.json", brokerID)
	q := url.Values{}
	q.Set("account_id", accountID)
	s.mu.RLock()
	q.Set("auth", s.userName)
	s.mu.RUnlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u+"?"+q.Encode(), nil)
	if err != nil {
		return api.TDRoute{}, api.NewError(api.ErrAuthFailed, "build td url request failed", err)
	}
	req.Header = s.Header()
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return api.TDRoute{}, api.NewError(api.ErrAuthFailed, "request td url failed", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return api.TDRoute{}, api.NewError(api.ErrAuthFailed, "request td url failed", fmt.Errorf("status=%d body=%s", resp.StatusCode, string(body)))
	}
	var all map[string]struct {
		URL        string   `json:"url"`
		Category   []string `json:"category"`
		BrokerType string   `json:"broker_type"`
		SMType     string   `json:"smtype"`
		SMConfig   string   `json:"smconfig"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&all); err != nil {
		return api.TDRoute{}, api.NewError(api.ErrAuthFailed, "decode td url response failed", err)
	}
	entry, ok := all[brokerID]
	if !ok {
		return api.TDRoute{}, api.NewError(api.ErrAuthFailed, "broker not found in td route", nil)
	}
	return api.TDRoute{URL: entry.URL, BrokerType: entry.BrokerType, SMType: entry.SMType, SMConfig: entry.SMConfig}, nil
}

func (s *authService) requestToken(ctx context.Context, payload map[string]string) (string, string, error) {
	form := url.Values{}
	form.Set("client_id", "shinny_tq")
	form.Set("client_secret", "be30b9f4-6862-488a-99ad-21bde0400081")
	for k, v := range payload {
		form.Set(k, v)
	}
	u := s.authURL + "/auth/realms/shinnytech/protocol/openid-connect/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", api.NewError(api.ErrAuthFailed, "build token request failed", err)
	}
	req.Header = s.Header()
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", "", api.NewError(api.ErrAuthFailed, "request token failed", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", api.NewError(api.ErrAuthFailed, "request token failed", fmt.Errorf("status=%d body=%s", resp.StatusCode, string(body)))
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", api.NewError(api.ErrAuthFailed, "decode token response failed", err)
	}
	if out.AccessToken == "" {
		return "", "", api.NewError(api.ErrAuthFailed, "empty access token", nil)
	}
	return out.AccessToken, out.RefreshToken, nil
}

func (s *authService) addAccount(ctx context.Context, accountID string) error {
	u := s.authURL + "/auth/realms/shinnytech/rest/update-grant-accounts/" + path.Clean("/"+accountID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, nil)
	if err != nil {
		return api.NewError(api.ErrAuthFailed, "build add account request failed", err)
	}
	req.Header = s.Header()
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return api.NewError(api.ErrAuthFailed, "add account request failed", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return api.NewError(api.ErrAuthFailed, "add account failed", fmt.Errorf("status=%d body=%s", resp.StatusCode, string(body)))
	}
	return nil
}

func parseAccessToken(userName string, token string) (api.AuthSession, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return api.AuthSession{}, fmt.Errorf("invalid jwt token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return api.AuthSession{}, err
	}
	var claims struct {
		Sub    string `json:"sub"`
		Exp    int64  `json:"exp"`
		Grants struct {
			Features []string `json:"features"`
			Accounts []string `json:"accounts"`
		} `json:"grants"`
		AccountType string `json:"account_type"`
		DaysLeft    int    `json:"daysleft"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return api.AuthSession{}, err
	}
	features := map[string]bool{}
	for _, f := range claims.Grants.Features {
		features[f] = true
	}
	accounts := map[string]bool{}
	for _, a := range claims.Grants.Accounts {
		accounts[a] = true
	}
	return api.AuthSession{
		UserName:    userName,
		AuthID:      claims.Sub,
		AccessToken: token,
		Features:    features,
		Accounts:    accounts,
		ExpireAt:    time.Unix(claims.Exp, 0),
		ProductType: claims.AccountType,
		ExpireDays:  claims.DaysLeft,
	}, nil
}
