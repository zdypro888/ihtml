// Package progress 定义跨工具、任务及界面共享的公开进度事实，不负责业务调度。
package progress

import (
	"strings"
	"time"
)

// Counter 的总量只在原系统明确提供时填写；不同单位不能相加或据此推算整体百分比。
type Counter struct {
	Label   string `json:"label"`
	Unit    string `json:"unit,omitempty"`
	Current int64  `json:"current"`
	Total   *int64 `json:"total,omitempty"`
}

// Snapshot 仅作展示。State 说明当前等待状态，不代表工具结果、任务完成或操作授权。
// UpdatedAt 是原回执时间；重放保留原时间，不能因重新打开界面伪装成刚更新。
type Snapshot struct {
	Message    string     `json:"message"`
	Phase      string     `json:"phase,omitempty"`
	State      string     `json:"state,omitempty"` // running / queued / waiting / cooldown
	Counters   []Counter  `json:"counters,omitempty"`
	WaitReason string     `json:"wait_reason,omitempty"`
	RetryAt    *time.Time `json:"retry_at,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at,omitempty,omitzero"`
}

// Normalize 校验有界公开事实并脱离调用方切片；无总量时保留未知，不补造“100%”。
func Normalize(in Snapshot) (Snapshot, bool) {
	in.Message = strings.TrimSpace(in.Message)
	in.Phase = strings.TrimSpace(in.Phase)
	in.WaitReason = strings.TrimSpace(in.WaitReason)
	if in.Message == "" || len(in.Message) > 1024 || len(in.Phase) > 128 || len(in.WaitReason) > 512 || len(in.Counters) > 8 {
		return Snapshot{}, false
	}
	switch in.State {
	case "", "running", "queued", "waiting", "cooldown":
	default:
		return Snapshot{}, false
	}
	if !in.UpdatedAt.IsZero() && (in.UpdatedAt.Year() < 1 || in.UpdatedAt.Year() > 9999) || in.RetryAt != nil && (in.RetryAt.Year() < 1 || in.RetryAt.Year() > 9999) {
		return Snapshot{}, false
	}
	out := in
	out.Counters = make([]Counter, len(in.Counters))
	for i, c := range in.Counters {
		c.Label = strings.TrimSpace(c.Label)
		c.Unit = strings.TrimSpace(c.Unit)
		if c.Label == "" || len(c.Label) > 128 || len(c.Unit) > 32 || c.Current < 0 || c.Total != nil && (*c.Total < 0 || c.Current > *c.Total) {
			return Snapshot{}, false
		}
		if c.Total != nil {
			n := *c.Total
			c.Total = &n
		}
		out.Counters[i] = c
	}
	if in.RetryAt != nil {
		at := *in.RetryAt
		out.RetryAt = &at
	}
	return out, true
}
