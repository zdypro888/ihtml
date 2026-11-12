package agent

import (
	"context"
	"github.com/zdypro888/ihtml/progress"
	"strings"
	"time"
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
	observed := observeProgress(ctx, progress.Snapshot{Message: text, UpdatedAt: time.Now().UTC()})
	return reportStatus(ctx, text) || observed
}

func reportStatus(ctx context.Context, text string) (sent bool) {
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

type progressReporterKey struct{}

// 执行账本观察器独立于界面 reporter，宿主替换显示通道时不会丢失任务最后进度。
// 嵌套任务只替换自身观察器，避免把子任务的分项快照写到父调用账本。
type progressObserverKey struct{}

func observeProgress(ctx context.Context, snapshot progress.Snapshot) (sent bool) {
	observer, _ := ctx.Value(progressObserverKey{}).(func(progress.Snapshot))
	if observer == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			sent = false
		}
	}()
	observer(snapshot)
	return true
}

// WithProgressReporter 为当前执行绑定结构化进度；与纯文本通道共存，不创建额外线程。
func WithProgressReporter(ctx context.Context, report func(progress.Snapshot)) context.Context {
	if ctx == nil || report == nil {
		return ctx
	}
	return context.WithValue(ctx, progressReporterKey{}, report)
}

// ReportProgress 仅报告经校验的公开事实；旧宿主继续收到 Message，取消后不再报告。
func ReportProgress(ctx context.Context, snapshot progress.Snapshot) (sent bool) {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	snapshot, ok := progress.Normalize(snapshot)
	if !ok {
		return false
	}
	if snapshot.UpdatedAt.IsZero() {
		snapshot.UpdatedAt = time.Now().UTC()
	}
	sent = observeProgress(ctx, snapshot)
	report, _ := ctx.Value(progressReporterKey{}).(func(progress.Snapshot))
	if report == nil {
		// 结构化快照已观察一次；纯文本回退不再重新生成时间或重复保存。
		return reportStatus(ctx, snapshot.Message) || sent
	}
	defer func() {
		if recover() != nil {
			// 界面回调异常不否定独立观察器已经接收的事实。
		}
	}()
	report(snapshot)
	return true
}
