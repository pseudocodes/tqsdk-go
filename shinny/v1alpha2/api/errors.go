package api

import "fmt"

type ErrCode string

const (
	ErrInvalidArgument      ErrCode = "E_INVALID_ARGUMENT"
	ErrInvalidAccount       ErrCode = "E_INVALID_ACCOUNT"
	ErrInvalidSymbol        ErrCode = "E_INVALID_SYMBOL"
	ErrInvalidDirection     ErrCode = "E_INVALID_DIRECTION"
	ErrInvalidOffset        ErrCode = "E_INVALID_OFFSET"
	ErrInvalidVolume        ErrCode = "E_INVALID_VOLUME"
	ErrInvalidPrice         ErrCode = "E_INVALID_PRICE"
	ErrInvalidBegin         ErrCode = "E_INVALID_BEGIN"
	ErrInvalidDuration      ErrCode = "E_INVALID_DURATION"
	ErrInvalidViewWidth     ErrCode = "E_INVALID_VIEW_WIDTH"
	ErrInvalidAlignMode     ErrCode = "E_INVALID_ALIGN_MODE"
	ErrInvalidTimeRange     ErrCode = "E_INVALID_TIME_RANGE"
	ErrInvalidTradingStatus ErrCode = "E_INVALID_TRADING_STATUS"

	ErrUnsupported          ErrCode = "E_UNSUPPORTED"
	ErrUnsupportedAdvanced  ErrCode = "E_UNSUPPORTED_ADVANCED"
	ErrUnsupportedPriceMode ErrCode = "E_UNSUPPORTED_PRICE_MODE"
	ErrUnsupportedBacktest  ErrCode = "E_UNSUPPORTED_BACKTEST"

	ErrPermissionDenied ErrCode = "E_PERMISSION_DENIED"
	ErrAuthRequired     ErrCode = "E_AUTH_REQUIRED"
	ErrAuthFailed       ErrCode = "E_AUTH_FAILED"
	ErrTokenExpired     ErrCode = "E_TOKEN_EXPIRED"

	ErrLoginRejected        ErrCode = "E_LOGIN_REJECTED"
	ErrLoginTimeout         ErrCode = "E_LOGIN_TIMEOUT"
	ErrTradingStatusTimeout ErrCode = "E_TRADING_STATUS_TIMEOUT"

	ErrRiskRejected   ErrCode = "E_RISK_REJECTED"
	ErrOrderNotFound  ErrCode = "E_ORDER_NOT_FOUND"
	ErrExchangeReject ErrCode = "E_EXCHANGE_REJECT"

	ErrTimeout    ErrCode = "E_TIMEOUT"
	ErrNoMoreData ErrCode = "E_NO_MORE_DATA"
	ErrDisconnected ErrCode = "E_DISCONNECTED"
)

type SDKError struct {
	Code    ErrCode
	Message string
	Cause   error
}

func (e *SDKError) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s (%v)", e.Code, e.Message, e.Cause)
}

func (e *SDKError) Unwrap() error {
	return e.Cause
}

func NewError(code ErrCode, msg string, cause error) error {
	return &SDKError{Code: code, Message: msg, Cause: cause}
}

type RiskError struct {
	Code    ErrCode
	RuleID  string
	Reason  string
	Details map[string]any
	Cause   error
}

func (e *RiskError) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("%s: %s", e.Code, e.Reason)
	}
	return fmt.Sprintf("%s: %s (%v)", e.Code, e.Reason, e.Cause)
}

func (e *RiskError) Unwrap() error {
	return e.Cause
}
