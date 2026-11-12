package agent

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PublicErrorDetails 是宿主明确审定可公开的失败原因，不改变执行状态或重试权限。
// Code 和 Message 必须来自宿主的公开错误映射，不能填入响应原文、凭据或用户输入。
type PublicErrorDetails struct {
	Code    string `json:"code" bson:"code"`
	Message string `json:"message" bson:"message"`
}

type publicError struct {
	details *PublicErrorDetails
	cause   error
}

// NewPublicError 将经过审定的公开说明与私有原因分离；cause 只供服务端 errors.Is/As。
// code 限小写标识符，message 限 2048 字节。无效说明退回通用错误，不透传 cause。
func NewPublicError(code, message string, cause error) error {
	return &publicError{details: normalizedPublicError(&PublicErrorDetails{Code: code, Message: message}), cause: cause}
}

func (e *publicError) Error() string {
	if e != nil && e.details != nil {
		return e.details.Message
	}
	return "操作未能完成；请核对当前状态和原回执。"
}

func (e *publicError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func normalizedPublicError(value *PublicErrorDetails) *PublicErrorDetails {
	if value == nil || len(value.Code) == 0 || len(value.Code) > 96 {
		return nil
	}
	for _, c := range value.Code {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return nil
		}
	}
	message := strings.TrimSpace(value.Message)
	if message == "" || len(message) > 2048 || !utf8.ValidString(message) {
		return nil
	}
	for _, c := range message {
		if unicode.IsControl(c) {
			return nil
		}
	}
	return &PublicErrorDetails{Code: value.Code, Message: message}
}

// ParseTaskFailureJSON 读取框架公开失败信封。任意 message 都会重新规范化，不能借此公开原错误文本。
// 只处理失败协议；普通业务 JSON 与异步受理回执不应被解释为失败。
func ParseTaskFailureJSON(value string) (*TaskFailure, bool) {
	if len(value) > 64<<10 {
		return nil, false
	}
	var envelope struct {
		Status string `json:"status"`
		TaskFailure
	}
	if json.Unmarshal([]byte(value), &envelope) != nil || (envelope.Status != "operation_failed" && envelope.Status != "not_admitted") {
		return nil, false
	}
	return normalizedTaskFailure(&envelope.TaskFailure), true
}
