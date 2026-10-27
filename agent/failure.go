package agent

import (
	"context"
	"errors"
	"strings"
)

// ClassifyTurnFailure 只返回稳定分类，不返回供应商错误正文，供宿主日志、历史与公开文案复用。
func ClassifyTurnFailure(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	}
	if err != nil {
		text := strings.ToLower(err.Error())
		for _, word := range []string{"max iteration", "max_iteration", "maxiteration", "iteration limit", "maximum iterations"} {
			if strings.Contains(text, word) {
				return "iteration_budget"
			}
		}
		for _, word := range []string{"timeout", "deadline exceeded"} {
			if strings.Contains(text, word) {
				return "timeout"
			}
		}
	}
	return "model_incomplete"
}
