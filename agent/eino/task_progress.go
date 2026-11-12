package einoengine

import (
	"fmt"
	"strings"

	ai "github.com/zdypro888/ihtml/agent"
	"github.com/zdypro888/ihtml/agent/internal/textfmt"
	"github.com/zdypro888/ihtml/progress"
)

// reportReadTaskProgress 将原调用已经保存的公开快照投影到当前读取轮次；
// 不依赖已经结束的原模型投影，也不把重新读取时间伪装为任务刚有进展。
func reportReadTaskProgress(req *ai.TurnRequest, rows []ai.TaskInfo, scope string) {
	if req == nil || req.OnEvent == nil || len(rows) == 0 {
		return
	}
	snapshots := make([]progress.Snapshot, 0, len(rows))
	for _, row := range rows {
		// 终态仅保留最后阶段供详情核对，不能把旧“正在处理”重新作为当前进度。
		if row.State != "queued" && row.State != "running" && row.State != "cancelling" {
			continue
		}
		if row.Progress != nil {
			if snapshot, ok := progress.Normalize(*row.Progress); ok && !snapshot.UpdatedAt.IsZero() {
				snapshots = append(snapshots, snapshot)
			}
		}
	}
	if len(snapshots) == 0 {
		return
	}
	if len(rows) == 1 {
		snapshot := snapshots[0]
		title := "后台调用"
		for _, tool := range req.Tools {
			if tool.Name == rows[0].Tool {
				if metadata := ai.ResolveToolMetadata(tool, nil); metadata.Title != "" {
					title = metadata.Title
				}
				break
			}
		}
		if !strings.HasPrefix(snapshot.Message, title) {
			snapshot.Message = textfmt.TruncateRunes(title+"："+snapshot.Message, 240)
		}
		req.OnEvent(ai.Step{Kind: ai.StepStatus, Result: snapshot.Message, Progress: &snapshot})
		return
	}
	// 多个分项不能合并文件数、百分比或“完成”语义；只汇总本次读取范围内的调用状态。
	snapshot := progress.Snapshot{UpdatedAt: snapshots[0].UpdatedAt}
	for _, current := range snapshots[1:] {
		if current.UpdatedAt.After(snapshot.UpdatedAt) {
			snapshot.UpdatedAt = current.UpdatedAt
		}
	}
	var running, queued, returned, other int
	for _, row := range rows {
		switch row.State {
		case "running", "cancelling":
			running++
		case "queued":
			queued++
		case "completed":
			returned++
		default:
			other++
		}
	}
	snapshot.Message = fmt.Sprintf("%s %d 个后台调用：处理中 %d，排队 %d，已返回 %d，其他状态 %d。调用返回不代表业务完成。", scope, len(rows), running, queued, returned, other)
	if running > 0 {
		snapshot.State = "running"
	} else if queued > 0 {
		snapshot.State = "queued"
	}
	req.OnEvent(ai.Step{Kind: ai.StepStatus, Result: snapshot.Message, Progress: &snapshot})
}
