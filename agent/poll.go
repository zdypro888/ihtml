package agent

import (
	"context"
	"errors"
	"github.com/zdypro888/ihtml/progress"
	"time"
)

// PollResult 由宿主解释原回执后提供；Done 表示停止等待，不等同于业务成功。
// Value 保留原始结果，Progress 是宿主提供的公开进度，内核不解析业务字段。
type PollResult[T any] struct {
	Value    T
	Done     bool
	Progress string
	// ProgressSnapshot 优先报告给当前结构化通道，notify 仅作纯文本兼容。
	ProgressSnapshot *progress.Snapshot
}

// PollUntil 只重复调用只读探针，不创建任务、不重试错误或重放写操作。
// interval 是相邻探针开始时刻的最小间隔（0默认1秒），长轮询不会再额外等待。
// 截止时间、授权及是否后台执行由调用方的上下文和已有 TaskRuntime 决定。
// probe 必须响应取消，notify 必须快速返回；停止等待不会取消原远端业务。
func PollUntil[T any](ctx context.Context, interval time.Duration, probe func(context.Context) (PollResult[T], error), notify func(string)) (T, error) {
	var zero T
	if ctx == nil || probe == nil || interval < 0 {
		return zero, errors.New("agent: invalid polling configuration")
	}
	if interval == 0 {
		interval = time.Second
	}
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		started := time.Now()
		result, err := probe(ctx)
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		if err != nil {
			return zero, err
		}
		if result.Done {
			return result.Value, nil
		}
		reported := false
		text := result.Progress
		if result.ProgressSnapshot != nil {
			if snapshot, ok := progress.Normalize(*result.ProgressSnapshot); ok {
				reported = ReportProgress(ctx, snapshot)
				if text == "" {
					text = snapshot.Message
				}
			}
		}
		if !reported && notify != nil && text != "" {
			notify(text)
		}
		if pause := interval - time.Since(started); pause > 0 {
			timer := time.NewTimer(pause)
			select {
			case <-ctx.Done():
				timer.Stop()
				return zero, ctx.Err()
			case <-timer.C:
			}
		}
	}
}
