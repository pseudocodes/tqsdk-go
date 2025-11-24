package shinny

import (
	"errors"
	"fmt"
)

// 预定义错误
var (
	ErrNotConnected         = errors.New("tqsdk: not connected")
	ErrAlreadySubscribed    = errors.New("tqsdk: already subscribed")
	ErrInvalidSymbol        = errors.New("tqsdk: invalid symbol")
	ErrSessionClosed        = errors.New("tqsdk: session closed")
	ErrOrderFailed          = errors.New("tqsdk: order failed")
	ErrNotLoggedIn          = errors.New("tqsdk: not logged in")
	ErrInvalidDuration      = errors.New("tqsdk: invalid duration")
	ErrInvalidViewWidth     = errors.New("tqsdk: invalid view width")
	ErrInvalidLeftKlineId   = errors.New("tqsdk: invalid left_kline_id")
	ErrInvalidFocusPosition = errors.New("tqsdk: invalid focus_position")
	ErrInvalidFocusDatetime = errors.New("tqsdk: invalid focus_datetime")
	ErrSubscriptionClosed   = errors.New("tqsdk: subscription closed")
	ErrContextCanceled      = errors.New("tqsdk: context canceled")
	ErrPermissionDenied     = errors.New("tqsdk: permission denied")
)

// Error SDK错误类型
type Error struct {
	Op   string // 操作名
	Err  error  // 原始错误
	Code string // 错误码
}

// Error 实现 error 接口
func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("tqsdk: %s failed [%s]: %v", e.Op, e.Code, e.Err)
	}
	return fmt.Sprintf("tqsdk: %s failed: %v", e.Op, e.Err)
}

// Unwrap 实现 errors.Unwrap
func (e *Error) Unwrap() error {
	return e.Err
}

// NewError 创建新的错误
func NewError(op string, err error) *Error {
	return &Error{
		Op:  op,
		Err: err,
	}
}

// NewErrorWithCode 创建带错误码的错误
func NewErrorWithCode(op string, code string, err error) *Error {
	return &Error{
		Op:   op,
		Code: code,
		Err:  err,
	}
}
