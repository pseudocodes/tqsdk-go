// Package config 用 Viper 装配文件配置；SDK 本身也接受直接构造的 Config。
// Source、Provider、Logger、CommandGuard 等运行时对象不从文件反序列化。
package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
	"go.uber.org/zap"

	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/auth"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/core"
	"github.com/pseudocodes/tqsdk-go/shinny/v1alpha3/risk"
)

// File 是配置文件结构；环境变量前缀 TQ_，如 TQ_AUTH_USER_NAME。
type File struct {
	Auth struct {
		UserName string `mapstructure:"user_name"`
		Password string `mapstructure:"password"`
	} `mapstructure:"auth"`
	Market struct {
		URL             string `mapstructure:"url"`
		MaxMessageBytes int64  `mapstructure:"max_message_bytes"`
	} `mapstructure:"market"`
	Trade struct {
		URL      string    `mapstructure:"url"`
		Accounts []Account `mapstructure:"accounts"`
	} `mapstructure:"trade"`
	Log struct {
		Level string `mapstructure:"level"`
	} `mapstructure:"log"`
}

// Account 是一个交易账户及其本地风控规则。
type Account struct {
	ID       string `mapstructure:"id"`
	BrokerID string `mapstructure:"broker_id"`
	UserName string `mapstructure:"user_name"`
	Password string `mapstructure:"password"`
	URL      string `mapstructure:"url"`
	Risk     []Rule `mapstructure:"risk"`
}

// Rule 是一条本地风控规则配置；Type 为 open_count / open_volume / order_rate。
type Rule struct {
	Type      string   `mapstructure:"type"`
	ID        string   `mapstructure:"id"`
	Symbols   []string `mapstructure:"symbols"`
	Exchanges []string `mapstructure:"exchanges"`
	Limit     int64    `mapstructure:"limit"`
	Combined  bool     `mapstructure:"combined"`
	PerSecond int      `mapstructure:"per_second"`
}

// Load 读取配置文件（yaml/toml/json），未知键报错。
func Load(path string) (File, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetDefault("log.level", "info")
	v.SetEnvPrefix("TQ")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	for _, k := range []string{"auth.user_name", "auth.password", "market.url", "trade.url", "log.level"} {
		if err := v.BindEnv(k); err != nil {
			return File{}, err
		}
	}
	if err := v.ReadInConfig(); err != nil {
		return File{}, err
	}
	var f File
	if err := v.UnmarshalExact(&f); err != nil {
		return File{}, err
	}
	return f, nil
}

// Logger 按 log.level 创建生产 Zap 日志。
func (f File) Logger() (*zap.Logger, error) {
	cfg := zap.NewProductionConfig()
	if err := cfg.Level.UnmarshalText([]byte(f.Log.Level)); err != nil {
		return nil, err
	}
	return cfg.Build()
}

// Provider 返回天勤账户鉴权客户端。
func (f File) Provider() *auth.Client { return auth.NewClient(f.Auth.UserName, f.Auth.Password) }

// MarketConfig 返回行情组件配置。
func (f File) MarketConfig(p auth.Provider, log *zap.Logger) core.MarketConfig {
	return core.MarketConfig{Auth: p, URL: f.Market.URL, Logger: log, MaxMessageBytes: f.Market.MaxMessageBytes}
}

// TradingConfig 返回交易组件配置。
func (f File) TradingConfig(p auth.Provider, log *zap.Logger) core.TradingConfig {
	return core.TradingConfig{Auth: p, URL: f.Trade.URL, Logger: log}
}

// AccountConfig 返回账户配置。
func (a Account) AccountConfig() core.AccountConfig {
	return core.AccountConfig{ID: a.ID, BrokerID: a.BrokerID, UserName: a.UserName, Password: a.Password, URL: a.URL}
}

// Guard 按配置创建该账户的风控 Engine；没有规则时返回 nil。
func (a Account) Guard() (*risk.Engine, error) {
	if len(a.Risk) == 0 {
		return nil, nil
	}
	rules := make([]risk.Rule, 0, len(a.Risk))
	for _, r := range a.Risk {
		switch r.Type {
		case "open_count":
			rules = append(rules, &risk.OpenCountLimit{Name: r.ID, Symbols: r.Symbols, Limit: r.Limit})
		case "open_volume":
			rules = append(rules, &risk.OpenVolumeLimit{Name: r.ID, Symbols: r.Symbols, Limit: r.Limit, Combined: r.Combined})
		case "order_rate":
			rules = append(rules, &risk.OrderRateLimit{Name: r.ID, Exchanges: r.Exchanges, PerSecond: r.PerSecond})
		default:
			return nil, fmt.Errorf("config: unknown risk rule type %q", r.Type)
		}
	}
	return risk.New(rules...)
}
