package tqsdk

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// LogConfig 日志配置
type LogConfig struct {
	Level       string // "debug", "info", "warn", "error"
	OutputPath  string // 输出路径，默认 "stdout"
	Development bool   // 开发模式
}

// NewLogger 创建新的 logger 实例
func NewLogger(config LogConfig) (*zap.Logger, error) {
	// 解析日志级别
	var level zapcore.Level
	switch config.Level {
	case "debug":
		level = zapcore.DebugLevel
	case "info":
		level = zapcore.InfoLevel
	case "warn":
		level = zapcore.WarnLevel
	case "error":
		level = zapcore.ErrorLevel
	default:
		level = zapcore.InfoLevel
	}

	// 配置
	zapConfig := zap.Config{
		Level:            zap.NewAtomicLevelAt(level),
		Development:      config.Development,
		Encoding:         "console",
		EncoderConfig:    zap.NewDevelopmentEncoderConfig(),
		OutputPaths:      []string{config.OutputPath},
		ErrorOutputPaths: []string{"stderr"},
	}

	if config.OutputPath == "" {
		zapConfig.OutputPaths = []string{"stdout"}
	}
	zapConfig.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder

	// JSON 格式更适合生产环境
	if !config.Development {
		zapConfig.Encoding = "json"
		zapConfig.EncoderConfig = zap.NewProductionEncoderConfig()
		zapConfig.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	}

	return zapConfig.Build()
}

// NewDefaultLogger 创建默认 logger
func NewDefaultLogger() *zap.Logger {
	logger, err := NewLogger(LogConfig{
		Level:       "info",
		OutputPath:  "stdout",
		Development: false,
	})
	if err != nil {
		// 如果创建失败，返回 nop logger
		return zap.NewNop()
	}
	return logger
}
