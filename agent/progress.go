package agent

import (
	"context"
	"strings"
)

type statusReporterKey struct{}

// WithStatusReporter 为宿主工具绑定现有轮次的状态通道；保留原上下文和取消条件。
// 进度只作展示，不代表执行回执、用户授权或模型新增轮次。
func WithStatusReporter(ctx context.Context, report func(string)) context.Context {
	if ctx == nil || report == nil {
		return ctx
	}
	return context.WithValue(ctx, statusReporterKey{}, report)
}

// ReportStatus 上报有界纯文本进度；没有通道、已取消或回调异常时返回 false，不创建后台工作。
func ReportStatus(ctx context.Context, text string) (sent bool) {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 1024 {
		return false
	}
	report, _ := ctx.Value(statusReporterKey{}).(func(string))
	if report == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			sent = false
		}
	}()
	report(text)
	return true
}
