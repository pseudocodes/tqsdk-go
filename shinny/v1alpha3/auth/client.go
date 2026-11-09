package auth

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Client 是生产 Provider：天勤账户登录、授权与路由发现，对应 Python TqAuth。
// 并发安全；首次 Authorize 时登录，之后复用 token。
type Client struct {
	UserName string // 天勤账户（邮箱、用户名或手机号）
	Password string

	AuthURL    string       // 默认 $TQ_AUTH_URL 或 https://auth.shinnytech.com
	NSURL      string       // 行情名称服务，默认 https://api.shinnytech.com/ns
	FilesURL   string       // 交易网关列表，默认 https://files.shinnytech.com
	Stock      bool         // 行情名称服务的 stock 参数；NewClient 设为 true（同 Python 默认）
	HTTPClient *http.Client // 默认 30 秒超时
	UserAgent  string

	mu           sync.Mutex
	accessToken  string
	refreshToken string
	grants       atomic.Pointer[Grants] // 不可变快照；读取不等待登录或路由发现。
	mdURL        string
	btURL        string // 回测行情地址，与实盘地址分开缓存
	tdURLs       map[string]string
}

// Grants 是天勤账户的授权内容。
type Grants struct {
	Features []string `json:"features"`
	Accounts []string `json:"accounts"`
}

// NewClient 创建生产 Provider。
func NewClient(userName, password string) *Client {
	return &Client{UserName: userName, Password: password, Stock: true}
}

const (
	clientID     = "shinny_tq"
	clientSecret = "be30b9f4-6862-488a-99ad-21bde0400081"
)

// Authorize 实现 Provider。行情返回名称服务的 mdurl；交易确保账户授权（自动添加，同 Python TqAccount）
// 并返回期货公司的交易网关地址。
func (c *Client) Authorize(ctx context.Context, r Request) (Access, error) {
	if strings.TrimSpace(c.UserName) == "" || strings.TrimSpace(c.Password) == "" {
		return Access{}, errors.New("auth: 快期账户与密码不能为空")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accessToken == "" {
		if err := c.requestToken(ctx, url.Values{"grant_type": {"password"}, "username": {c.UserName}, "password": {c.Password}}); err != nil {
			return Access{}, err
		}
	}
	switch r.Service {
	case Market:
		cached := &c.mdURL
		var features FeatureSet = c
		if r.Backtest {
			cached = &c.btURL
			features = FeatureFunc(func(f string) bool { return f == "futr" || f == "sec" || f == "lmt_idx" || c.HasFeature(f) })
		}
		if *cached == "" {
			u, err := c.resolveMD(ctx, r.Backtest)
			if err != nil {
				return Access{}, err
			}
			*cached = u
		}
		return Access{Endpoint: *cached, Header: c.header(), Features: features}, nil
	case Trade:
		if r.BrokerID == "" || r.AccountID == "" {
			return Access{}, errors.New("auth: trade authorization needs BrokerID and AccountID")
		}
		if err := c.ensureAccount(ctx, r.AccountID); err != nil {
			return Access{}, err
		}
		key := r.BrokerID + "\x00" + r.AccountID
		if c.tdURLs[key] == "" {
			u, err := c.resolveTD(ctx, r.BrokerID, r.AccountID)
			if err != nil {
				return Access{}, err
			}
			if c.tdURLs == nil {
				c.tdURLs = map[string]string{}
			}
			c.tdURLs[key] = u
		}
		return Access{Endpoint: c.tdURLs[key], Header: c.header(), Features: c}, nil
	}
	return Access{}, fmt.Errorf("auth: unknown service %d", r.Service)
}

// Grants 返回已登录账户授权内容的独立副本；未登录时为空，不触发登录。
func (c *Client) Grants() Grants {
	if g := c.grants.Load(); g != nil {
		return Grants{Features: slices.Clone(g.Features), Accounts: slices.Clone(g.Accounts)}
	}
	return Grants{}
}

// HasFeature 报告账户是否有某功能授权（futr / sec / lmt_idx / tq_trading_status ...）。
// 只读取当前授权快照；未登录时返回 false，不触发登录或等待 HTTP。
func (c *Client) HasFeature(f string) bool {
	if c == nil {
		return false
	}
	g := c.grants.Load()
	return g != nil && slices.Contains(g.Features, f)
}

func (c *Client) authURL() string {
	if c.AuthURL != "" {
		return strings.TrimRight(c.AuthURL, "/")
	}
	if u := os.Getenv("TQ_AUTH_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "https://auth.shinnytech.com"
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// header 返回独立副本。调用方持有 c.mu。
func (c *Client) header() http.Header {
	h := http.Header{}
	ua := c.UserAgent
	if ua == "" {
		ua = "tqsdk-go"
	}
	h.Set("User-Agent", ua)
	h.Set("Accept", "application/json")
	if c.accessToken != "" {
		h.Set("Authorization", "Bearer "+c.accessToken)
	}
	return h
}

func (c *Client) do(ctx context.Context, method, u string, body io.Reader, contentType string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header = c.header()
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return data, resp.StatusCode, err
}

// requestToken 调用 openid-connect token 接口并解析 JWT 中的 grants。调用方持有 c.mu。
func (c *Client) requestToken(ctx context.Context, form url.Values) error {
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	u := c.authURL() + "/auth/realms/shinnytech/protocol/openid-connect/token"
	data, status, err := c.do(ctx, http.MethodPost, u, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return fmt.Errorf("auth: request token: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("auth: 用户权限认证失败 (%d, %s)", status, truncate(data))
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(data, &tok); err != nil || tok.AccessToken == "" {
		return fmt.Errorf("auth: bad token response: %v", err)
	}
	grants, err := decodeGrants(tok.AccessToken)
	if err != nil {
		return fmt.Errorf("auth: decode access token: %w", err)
	}
	c.accessToken, c.refreshToken = tok.AccessToken, tok.RefreshToken
	c.grants.Store(&grants)
	return nil
}

// decodeGrants 读取 JWT payload 的 grants（不校验签名，同 Python）。
func decodeGrants(token string) (Grants, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return Grants{}, errors.New("invalid jwt")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Grants{}, err
	}
	var claims struct {
		Grants Grants `json:"grants"`
	}
	err = json.Unmarshal(payload, &claims)
	return claims.Grants, err
}

// ensureAccount 确保期货账户已在授权列表中，否则添加后刷新 token。调用方持有 c.mu。
func (c *Client) ensureAccount(ctx context.Context, account string) error {
	if g := c.grants.Load(); g != nil && slices.Contains(g.Accounts, account) {
		return nil
	}
	u := c.authURL() + "/auth/realms/shinnytech/rest/update-grant-accounts/" + url.PathEscape(account)
	data, status, err := c.do(ctx, http.MethodPut, u, nil, "")
	if err != nil {
		return fmt.Errorf("auth: add account: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("auth: 添加期货账户失败 (%d, %s)", status, truncate(data))
	}
	return c.requestToken(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c.refreshToken}})
}

func (c *Client) resolveMD(ctx context.Context, backtest bool) (string, error) {
	ns := c.NSURL
	if ns == "" {
		ns = "https://api.shinnytech.com/ns"
	}
	q := url.Values{"stock": {fmt.Sprint(c.Stock)}, "backtest": {fmt.Sprint(backtest)}}
	data, status, err := c.do(ctx, http.MethodGet, ns+"?"+q.Encode(), nil, "")
	if err != nil {
		return "", fmt.Errorf("auth: 调用名称服务失败: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("auth: 调用名称服务失败 (%d, %s)", status, truncate(data))
	}
	var out struct {
		MDURL string `json:"mdurl"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.MDURL == "" {
		return "", fmt.Errorf("auth: 调用名称服务失败: %s", truncate(data))
	}
	return out.MDURL, nil
}

// resolveTD 按 Python _get_td_url：account_id 为券商账号，auth 为天勤用户名。
func (c *Client) resolveTD(ctx context.Context, broker, account string) (string, error) {
	files := c.FilesURL
	if files == "" {
		files = "https://files.shinnytech.com"
	}
	q := url.Values{"account_id": {account}, "auth": {c.UserName}}
	data, status, err := c.do(ctx, http.MethodGet, strings.TrimRight(files, "/")+"/"+url.PathEscape(broker)+".json?"+q.Encode(), nil, "")
	if err != nil {
		return "", fmt.Errorf("auth: request td url: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("auth: 不支持该期货公司 - %s", broker)
	}
	var list map[string]struct {
		URL      string   `json:"url"`
		Category []string `json:"category"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return "", fmt.Errorf("auth: bad broker list: %w", err)
	}
	b, ok := list[broker]
	if !ok || b.URL == "" {
		return "", fmt.Errorf("auth: 不支持该期货公司 - %s", broker)
	}
	for _, cat := range b.Category {
		if cat == "TQ" {
			return b.URL, nil
		}
	}
	return "", fmt.Errorf("auth: 该期货公司 - %s 暂不支持 TqSdk 登录", broker)
}

func truncate(b []byte) string {
	if len(b) > 512 {
		b = b[:512]
	}
	return string(b)
}
