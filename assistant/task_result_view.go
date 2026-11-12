package assistant

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zdypro888/ihtml/agent"
	"github.com/zdypro888/ihtml/progress"
)

// publicTaskView 是调用账本的展示白名单；原参数、结果正文、错误正文和凭据永不进入浏览器。
type publicTaskView struct {
	ID       string             `json:"id"`
	Tool     string             `json:"tool"`
	Title    string             `json:"title,omitempty"`
	State    string             `json:"state"`
	Progress *progress.Snapshot `json:"progress,omitempty"`
}

// taskResultView 由工具定义声明的结果结构选择，而不是按工具名或业务接口猜测。
// “已返回”只表示 Handler 返回；不会把其中的异步业务宣称为完成。
func taskResultView(metadata agent.ToolMetadata, result string) (ToolResultView, bool) {
	if metadata.ResultKind != agent.ToolResultTask && metadata.ResultKind != agent.ToolResultTaskList {
		return ToolResultView{}, false
	}
	view := ToolResultView{Title: metadata.Title}
	if len(result) == 0 || len(result) > 8<<20 {
		return view, true
	}
	var rows []publicTaskView
	if metadata.ResultKind == agent.ToolResultTask {
		var row publicTaskView
		if json.Unmarshal([]byte(result), &row) != nil || row.ID == "" {
			return view, true
		}
		rows = []publicTaskView{row}
	} else {
		var envelope struct {
			Tasks []publicTaskView `json:"tasks"`
		}
		if json.Unmarshal([]byte(result), &envelope) != nil || envelope.Tasks == nil {
			return view, true
		}
		rows = envelope.Tasks
	}
	counts := map[string]int{}
	labels := []struct{ state, text string }{{"queued", "排队中"}, {"running", "执行中"}, {"cancelling", "正在停止"}, {"waiting", "等待中"}, {"ready", "已就绪"}, {"completed", "已返回"}, {"failed", "失败"}, {"cancelled", "已停止"}, {"unknown", "结果未知"}}
	for i := range rows {
		row := &rows[i]
		if row.ID == "" || len(row.ID) > 256 || len(row.Tool) > 256 {
			return view, true
		}
		known := false
		for _, label := range labels {
			if row.State == label.state {
				known = true
				break
			}
		}
		if !known {
			row.State = "unknown"
		}
		counts[row.State]++
		row.Title = agent.PublicToolTitle(row.Tool, row.Title)
		if row.Progress != nil {
			if snapshot, ok := progress.Normalize(*row.Progress); ok {
				row.Progress = &snapshot
			} else {
				row.Progress = nil
			}
		}
	}
	parts := []string{}
	for _, label := range labels {
		if n := counts[label.state]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d 项", label.text, n))
		}
	}
	view.Summary = fmt.Sprintf("本次读取 %d 项任务", len(rows))
	if len(parts) > 0 {
		view.Summary += "：" + strings.Join(parts, "、")
	}
	// 仅展示有界明细；模型仍持有原始完整调用结果，此处不会替它决定下一页或执行步骤。
	details := struct {
		Tasks []publicTaskView `json:"tasks"`
		Total int              `json:"total"`
		Shown int              `json:"shown"`
		More  bool             `json:"more"`
	}{Tasks: []publicTaskView{}, Total: len(rows)}
	for _, row := range rows {
		if len(details.Tasks) >= 8 {
			break
		}
		details.Tasks = append(details.Tasks, row)
		raw, _ := json.Marshal(details)
		if len(raw) > 30<<10 {
			details.Tasks = details.Tasks[:len(details.Tasks)-1]
			break
		}
	}
	details.Shown = len(details.Tasks)
	details.More = details.Shown < details.Total
	view.Result, _ = json.Marshal(details)
	if details.More {
		view.Summary += fmt.Sprintf("；详情显示前 %d 项", details.Shown)
	}
	return view, true
}
