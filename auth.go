package tqsdk

import (
	"encoding/json"
	"fmt"
	"io"

	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

// Authenticator 认证器接口，定义了认证相关的所有操作
type Authenticator interface {
	// BaseHeader 返回包含认证信息的 HTTP Header
	BaseHeader() http.Header

	// Login 执行登录操作
	Login() error

	// GetTdUrl 获取指定期货公司的交易服务器地址
	GetTdUrl(brokerID, accountID string) (*BrokerInfo, error)

	// GetMdUrl 获取行情服务器地址
	GetMdUrl(stock bool, backtest bool) (string, error)

	// HasFeature 检查是否具有指定的功能权限
	HasFeature(feature string) bool

	// HasAccount 检查是否具有指定的账户权限
	HasAccount(account string) bool

	// HasMdGrants 检查是否有查看指定合约行情数据的权限
	HasMdGrants(symbols ...string) error

	// HasTdGrants 检查是否有交易指定合约的权限
	HasTdGrants(symbol string) error

	// // GetAuthID 获取认证ID
	// GetAuthID() string

	// // GetAccessToken 获取访问令牌
	// GetAccessToken() string
}

var (
	VERSION     = "3.8.1"
	TQ_AUTH_URL = "https://auth.shinnytech.com"

	CLIENT_ID     = "shinny_tq"
	CLIENT_SECRET = "be30b9f4-6862-488a-99ad-21bde0400081"

	DefaultHTTPTimeout = 30 * time.Second
)

type TqAuth struct {
	UserName string
	Password string

	authURL      string
	accessToken  string
	refreshToken string
	AuthID       string
	grands       Grands
}

type Grands struct {
	Features map[string]struct{}
	Accounts map[string]struct{}
}

type AuthResp struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	NotBeforePolicy  int    `json:"not-before-policy"`
	SessionState     string `json:"session_state"`
	Scope            string `json:"scope"`
}

type AccessTokenClaims struct {
	Jti          string `json:"jti"`
	Exp          int    `json:"exp"`
	Nbf          int    `json:"nbf"`
	Iat          int    `json:"iat"`
	Iss          string `json:"iss"`
	Sub          string `json:"sub"`
	Typ          string `json:"typ"`
	Azp          string `json:"azp"`
	AuthTime     int    `json:"auth_time"`
	SessionState string `json:"session_state"`
	Acr          string `json:"acr"`
	Scope        string `json:"scope"`
	Grants       struct {
		Features   []string `json:"features"`
		OtgIds     string   `json:"otg_ids"`
		ExpiryDate string   `json:"expiry_date"`
		Accounts   []string `json:"accounts"`
	} `json:"grants"`
	CreationTime      int64  `json:"creation_time"`
	Setname           bool   `json:"setname"`
	Mobile            string `json:"mobile"`
	MobileVerified    string `json:"mobileVerified"`
	PreferredUsername string `json:"preferred_username"`
	ID                string `json:"id"`
	Username          string `json:"username"`
}

// GetAudience implements jwt.Claims.
func (c *AccessTokenClaims) GetAudience() (jwt.ClaimStrings, error) {
	return nil, nil
}

// GetExpirationTime implements jwt.Claims.
func (c *AccessTokenClaims) GetExpirationTime() (*jwt.NumericDate, error) {
	d := jwt.NewNumericDate(time.Unix(int64(c.Exp), 0))
	return d, nil
}

// GetIssuedAt implements jwt.Claims.
func (c *AccessTokenClaims) GetIssuedAt() (*jwt.NumericDate, error) {
	d := jwt.NewNumericDate(time.Unix(int64(c.Iat), 0))
	return d, nil
}

// GetIssuer implements jwt.Claims.
func (c *AccessTokenClaims) GetIssuer() (string, error) {
	return c.Iss, nil
}

// GetNotBefore implements jwt.Claims.
func (c *AccessTokenClaims) GetNotBefore() (*jwt.NumericDate, error) {
	return nil, nil
}

// GetSubject implements jwt.Claims.
func (c *AccessTokenClaims) GetSubject() (string, error) {
	return c.Sub, nil
}

func (c AccessTokenClaims) Valid() error {
	return nil
}

var _ jwt.Claims = &AccessTokenClaims{}

type BrokerInfo struct {
	Category   []string `json:"category"`
	URL        string   `json:"url"`
	BrokerType string   `json:"broker_type"`
	SmType     string   `json:"smtype,omitempty" csv:"sm_type,omitempty"`
	SmConfig   string   `json:"smconfig,omitempty" csv:"sm_config,omitempty"`
}

type MdURL struct {
	Mdurl string `json:"mdurl"`
}

func NewTqAuth(username, password string) *TqAuth {
	auth := &TqAuth{
		UserName: username,
		Password: password,
		grands: Grands{
			Features: map[string]struct{}{},
			Accounts: map[string]struct{}{},
		},
	}
	if authURL, ok := os.LookupEnv("TQ_AUTH_URL"); ok && authURL != "" {
		auth.authURL = authURL
	} else {
		auth.authURL = "https://auth.shinnytech.com"
	}
	return auth
}

func (t *TqAuth) BaseHeader() http.Header {
	var headers = http.Header{}
	headers.Add("User-Agent", fmt.Sprintf("tqsdk-python %s", VERSION))
	headers.Add("Accept", "application/json")
	headers.Add("Authorization", fmt.Sprintf("Bearer %s", t.accessToken))
	return headers
}

// Login xxx
func (t *TqAuth) Login() error {
	if err := t.requestToken(); err != nil {
		zap.L().Error("login request token error", zap.Error(err))
		return err
	}
	zap.L().Debug(t.accessToken)
	accTokenClaims := &AccessTokenClaims{}
	token, _, err := new(jwt.Parser).ParseUnverified(t.accessToken, accTokenClaims)
	if err != nil {
		zap.L().Error("jwt valiation error", zap.Error(err))
		return err
	}

	if claims, ok := token.Claims.(*AccessTokenClaims); ok {

		t.AuthID = claims.Sub
		grands := claims.Grants
		// fmt.Printf("%+v\n", claims.Grants)
		for _, feature := range grands.Features {
			t.grands.Features[feature] = struct{}{}
		}
		for _, account := range grands.Accounts {
			t.grands.Accounts[account] = struct{}{}

		}
	}
	return nil
}

func (t *TqAuth) requestToken() error {
	var (
		reqData  = url.Values{}
		authResp = &AuthResp{}
	)
	reqData.Add("client_id", "shinny_tq")
	reqData.Add("client_secret", "be30b9f4-6862-488a-99ad-21bde0400081")
	reqData.Add("username", t.UserName)
	reqData.Add("password", t.Password)
	reqData.Add("grant_type", "password")

	headers := http.Header{}
	headers.Add("User-Agent", fmt.Sprintf("tqsdk-python %s", VERSION))
	headers.Add("Accept", "application/json")

	url := t.authURL + "/auth/realms/shinnytech/protocol/openid-connect/token"

	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(reqData.Encode()))
	if err != nil {
		// log.Println(err)
		zap.L().Error("create http get request error", zap.Error(err), zap.String("url", url))
		return err
	}

	req.Header = headers
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{
		Timeout: 30 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		zap.L().Error("http get request token error", zap.Error(err), zap.String("url", url))

		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s", string(body))
	}

	// log.Printf("Response body : `%v` ", string(body))
	err = json.Unmarshal([]byte(body), &authResp)
	if err != nil {
		// log.Println(err)
		zap.L().Error("unmarshal auth response json error", zap.Error(err))
		return err
	}
	t.accessToken = authResp.AccessToken
	t.refreshToken = authResp.RefreshToken
	return nil
}

func (t *TqAuth) addAccount(accountID string) {
	panic("UnImplement Method")
}

func (t *TqAuth) GetTdUrl(brokerID, accountID string) (*BrokerInfo, error) {
	var (
		requrl  = fmt.Sprintf("https://files.shinnytech.com/%s.json", brokerID)
		headers = t.BaseHeader()
	)
	req, _ := http.NewRequest(http.MethodGet, requrl, nil)
	q := req.URL.Query()
	q.Add("account_id", accountID)
	q.Add("auth", t.UserName)
	req.URL.RawQuery = q.Encode()
	req.Header = headers

	client := &http.Client{
		Timeout: DefaultHTTPTimeout,
	}
	resp, err := client.Do(req)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		zap.L().Error("fetch broker info error", zap.Error(err), zap.String("url", requrl))
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("不支持该期货公司 - %s", brokerID)
	}
	var brokerInfos = map[string]*BrokerInfo{}
	if err := json.Unmarshal(body, &brokerInfos); err != nil {
		zap.L().Error("unmarshal broker_info json error", zap.Error(err))
		return nil, err
	}

	if bi, ok := brokerInfos[brokerID]; ok {
		if bi.BrokerType == "" {
			bi.BrokerType = "FUTURE"
		}
		return bi, nil
	}
	return nil, fmt.Errorf("该期货公司 - %s 暂不支持 TqSdk 登录，请联系期货公司", brokerID)
}

func (t *TqAuth) GetMdUrl(stock bool, backtest bool) (string, error) {
	var (
		requrl  = fmt.Sprintf("https://api.shinnytech.com/ns?stock=%v&backtest=%v", stock, backtest)
		headers = t.BaseHeader()
	)
	req, err := http.NewRequest(http.MethodGet, requrl, nil)
	if err != nil {
		// log.Println(err)
		zap.L().Error("gen http get mdurl request error", zap.Error(err), zap.String("url", requrl))
		return "", err
	}
	req.Header = headers

	client := &http.Client{
		Timeout: DefaultHTTPTimeout,
	}
	resp, err := client.Do(req)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		zap.L().Error("fetch mdurl error", zap.Error(err), zap.String("url", requrl))
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("调用名称服务失败: %d, %s", resp.StatusCode, string(body))
	}
	var mdurl = MdURL{}
	if err := json.Unmarshal(body, &mdurl); err != nil {
		// log.Println(err)
		zap.L().Error("unmarshal mdurl info json error", zap.Error(err))
		return "", err
	}
	return mdurl.Mdurl, nil
}

func (t *TqAuth) HasFeature(feature string) bool {
	_, ok := t.grands.Features[feature]
	return ok
}

func (t *TqAuth) HasAccount(account string) bool {
	_, ok := t.grands.Accounts[account]
	return ok
}

var (
	// FUTURE_EXCHANGES 期货交易所
	futureExchanges = map[string]struct{}{
		"CFFEX": {},
		"SHFE":  {},
		"DCE":   {},
		"CZCE":  {},
		"INE":   {},
		"GFEX":  {},
	}

	// STOCK_EXCHANGES 股票交易所
	stockExchanges = map[string]struct{}{
		"SSE":  {},
		"SZSE": {},
	}

	// SPOT_EXCHANGES 现货交易所
	spotExchanges = map[string]struct{}{
		"SSWE": {},
	}

	// KQ_EXCHANGES KQ交易所
	kqExchanges = map[string]struct{}{
		"KQ": {},
	}

	// KQD_EXCHANGES KQD交易所
	kqdExchanges = map[string]struct{}{
		"KQD": {},
	}

	// 限制指数列表
	limitedIndexes = map[string]struct{}{
		"SSE.000016": {},
		"SSE.000300": {},
		"SSE.000905": {},
		"SSE.000852": {},
	}
)

// HasMdGrants 检查是否有查看指定合约行情数据的权限
func (t *TqAuth) HasMdGrants(symbols ...string) error {
	for _, symbol := range symbols {
		prefix := strings.Split(symbol, ".")[0]

		// 检查是否为期货、现货、KQ、KQD交易所，需要 futr 权限
		if t.isInExchangeGroup(prefix, futureExchanges, spotExchanges, kqExchanges, kqdExchanges) {
			if t.HasFeature("futr") {
				continue
			}
			zap.L().Error(fmt.Sprintf("您的账户不支持查看 %s 的行情数据, 需要购买后才能使用。升级网址: https://www.shinnytech.com/tqsdk-buy/", symbol))
			return ErrPermissionDenied
		}

		// 检查是否为股票交易所（包括 CSI），需要 sec 权限
		if prefix == "CSI" || t.isInExchangeGroup(prefix, stockExchanges) {
			if t.HasFeature("sec") {
				continue
			}
			zap.L().Error(fmt.Sprintf("您的账户不支持查看 %s 的行情数据，需要购买后才能使用。升级网址: https://www.shinnytech.com/tqsdk-buy/", symbol))
			return ErrPermissionDenied
		}

		// 检查是否为限制指数，需要 lmt_idx 权限
		if _, ok := limitedIndexes[symbol]; ok {
			if t.HasFeature("lmt_idx") {
				continue
			}
			zap.L().Error(fmt.Sprintf("您的账户不支持查看 %s 的行情数据，需要购买后才能使用。升级网址: https://www.shinnytech.com/tqsdk-buy/", symbol))
			return ErrPermissionDenied
		}

		// 不在任何已知交易所列表中
		return ErrPermissionDenied

	}
	return nil
}

// isInExchangeGroup 检查交易所前缀是否在给定的交易所组中
func (t *TqAuth) isInExchangeGroup(prefix string, groups ...map[string]struct{}) bool {
	for _, group := range groups {
		if _, ok := group[prefix]; ok {
			return true
		}
	}
	return false
}

// HasTdGrants 检查是否有交易指定合约的权限
// 对于 opt / cmb / adv 权限的检查由 OTG 做
func (t *TqAuth) HasTdGrants(symbol string) error {
	prefix := strings.Split(symbol, ".")[0]

	// 检查是否为期货、现货、KQ、KQD交易所，需要 futr 权限
	if t.isInExchangeGroup(prefix, futureExchanges, spotExchanges, kqExchanges, kqdExchanges) {
		if t.HasFeature("futr") {
			return nil
		}
		zap.L().Error(fmt.Sprintf("您的账户不支持交易 %s，需要购买后才能使用。升级网址：https://www.shinnytech.com/tqsdk-buy/", symbol))
		return ErrPermissionDenied
	}

	// 检查是否为股票交易所（包括 CSI），需要 sec 权限
	if prefix == "CSI" || t.isInExchangeGroup(prefix, stockExchanges) {
		if t.HasFeature("sec") {
			return nil
		}
		zap.L().Error(fmt.Sprintf("您的账户不支持交易 %s，需要购买后才能使用。升级网址：https://www.shinnytech.com/tqsdk-buy/", symbol))
		return ErrPermissionDenied
	}

	// 不在任何已知交易所列表中
	zap.L().Error(fmt.Sprintf("您的账户不支持交易 %s，需要购买后才能使用。升级网址：https://www.shinnytech.com/tqsdk-buy/", symbol))
	return ErrPermissionDenied
}

// GetAuthID 获取认证ID
func (t *TqAuth) GetAuthID() string {
	return t.AuthID
}

// GetAccessToken 获取访问令牌
func (t *TqAuth) GetAccessToken() string {
	return t.accessToken
}
