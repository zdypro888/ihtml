package assistant

import (
	"encoding/json"
	"time"

	"github.com/zdypro888/ihtml"
)

// ToolHistoryRecord 是公开操作显示快照，不包含签名、nonce 或任何可恢复执行的授权。
// Call 只来自宿主已脱敏的提案，Result 只来自 PresentToolResult 或规范化失败事实；不能传入原始 Handler 输出。
type ToolHistoryRecord struct {
	FlowID           string                 `json:"flowId,omitempty"`
	ID               string                 `json:"id"`
	AfterMessageID   string                 `json:"afterMessageId,omitempty"`
	Call             ihtml.ProposedToolCall `json:"call"`
	Status           string                 `json:"status"`
	Summary          string                 `json:"summary,omitempty"`
	Result           json.RawMessage        `json:"result,omitempty"`
	ProposalRecorded bool                   `json:"proposalRecorded"`
	ReceiptOnly      bool                   `json:"receiptOnly,omitempty"`
	UpdatedAt        *time.Time             `json:"updatedAt,omitempty"`
	RunID            string                 `json:"runId,omitempty"`
	Sequence         int64                  `json:"sequence,omitempty"`
}

// trackedToolHistoryEvent 区分“不属于历史”与“历史快照无效”，不能把保存失败当成可忽略事件。
func trackedToolHistoryEvent(event ihtml.ChatStreamEvent) bool {
	switch event.Type {
	case ihtml.ChatStreamConfirmationRequired, ihtml.ChatStreamToolStart, ihtml.ChatStreamToolResult:
		return true
	case ihtml.ChatStreamCancelToolResult:
		return event.ToolStatus == ihtml.ChatToolCancelled
	default:
		return false
	}
}

// ToolHistoryEvent 仅接收框架已投影的公开事件；宿主可按 ID 合并并持久化，不依赖工具名称。
// 执行引用不会覆盖原提案；返回的增量不包含授权，读取历史也不能代替重新校验待确认卡。
func ToolHistoryEvent(event ihtml.ChatStreamEvent) (ToolHistoryRecord, bool) {
	var out ToolHistoryRecord
	if !trackedToolHistoryEvent(event) {
		return out, false
	}
	if event.ToolCall != nil {
		out.Call = *event.ToolCall
	}
	out.ID = out.Call.ID
	if out.ID == "" {
		out.ID = event.ToolID
	}
	if out.ID == "" {
		return out, false
	}
	out.Call.ID = out.ID
	if out.Call.Name == "" {
		out.Call.Name = event.ToolName
	}
	if out.Call.Title == "" {
		out.Call.Title = event.ToolTitle
	}
	out.Call.ConfirmationToken, out.Call.ConfirmationNonce = "", ""
	out.Call.RequiresConfirmation, out.Call.ApprovalResume, out.Call.AgentMode = false, "", ""
	out.ProposalRecorded = event.Type == ihtml.ChatStreamConfirmationRequired
	out.Status, out.Summary = event.ToolStatus, event.ToolSummary
	if out.Status == "" {
		if out.ProposalRecorded {
			out.Status = ihtml.ChatToolPending
		} else if event.Type == ihtml.ChatStreamToolStart {
			out.Status = ihtml.ChatToolRunning
		} else {
			out.Status = ihtml.ChatToolUnknown
		}
	}
	out.Result = append(json.RawMessage(nil), event.ToolResult...)
	out.RunID, out.Sequence = event.RunID, event.Seq
	out.FlowID = event.FlowID
	out.UpdatedAt = event.ToolUpdatedAt
	if out.UpdatedAt == nil {
		updatedAt := time.Now().UTC()
		out.UpdatedAt = &updatedAt
	}
	// JSON 往返脱离事件中的 map/slice，持久化回调不能修改仍将发送的签名提案。
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > 512<<10 {
		return ToolHistoryRecord{}, false
	}
	if json.Unmarshal(raw, &out) != nil {
		return ToolHistoryRecord{}, false
	}
	return out, true
}

// MergeToolHistory 保留原确认范围，只让后续执行证据更新状态与公开结果。
func MergeToolHistory(previous, next ToolHistoryRecord) ToolHistoryRecord {
	if previous.ID == "" {
		return next
	}
	if previous.ID != next.ID {
		return previous
	}
	// 原提案可迟于终态到达；只补齐公开范围，不能借补齐过程回退执行事实。
	if !previous.ProposalRecorded && next.ProposalRecorded {
		previous.Call, previous.ProposalRecorded, previous.ReceiptOnly = next.Call, true, false
	}
	if previous.RunID != "" && previous.RunID == next.RunID && previous.Sequence > 0 && next.Sequence > 0 && next.Sequence <= previous.Sequence {
		return previous
	}
	if previous.UpdatedAt != nil && next.UpdatedAt != nil && next.UpdatedAt.Before(*previous.UpdatedAt) {
		return previous
	}
	// 重连的迟到提案或开始事件不是新执行证据；未知/已受理仍可由真实结果纠正。
	if (previous.Status == ihtml.ChatToolSuccess || previous.Status == ihtml.ChatToolError || previous.Status == ihtml.ChatToolCancelled) && next.Status != previous.Status {
		return previous
	}
	if (previous.Status == ihtml.ChatToolRunning && next.Status == ihtml.ChatToolPending) || ((previous.Status == ihtml.ChatToolAccepted || previous.Status == ihtml.ChatToolUnknown) && (next.Status == ihtml.ChatToolPending || next.Status == ihtml.ChatToolRunning)) {
		return previous
	}
	// 同一终态的无正文重复通知只能补充信息，不能清掉已经保存的结果。
	if previous.Status == next.Status {
		if len(next.Result) == 0 {
			next.Result = append(json.RawMessage(nil), previous.Result...)
		}
		if next.Summary == "" {
			next.Summary = previous.Summary
		}
	}
	if previous.ProposalRecorded {
		next.Call, next.ProposalRecorded = previous.Call, true
		next.ReceiptOnly = false
	} else {
		// 非确认工具也有完整请求描述；结果引用只补缺字段，不清空或替换原调用范围。
		oldJSON, _ := json.Marshal(previous.Call)
		newJSON, _ := json.Marshal(next.Call)
		var fields, additions map[string]json.RawMessage
		_ = json.Unmarshal(oldJSON, &fields)
		_ = json.Unmarshal(newJSON, &additions)
		for key, value := range additions {
			if prior, exists := fields[key]; !exists || string(prior) == `""` || string(prior) == "null" {
				fields[key] = value
			}
		}
		merged, _ := json.Marshal(fields)
		_ = json.Unmarshal(merged, &next.Call)
	}
	// 缺少元数据的旧回执不能清掉排序依据，否则后续迟到帧可能重新覆盖已保存结果。
	if next.UpdatedAt == nil {
		next.UpdatedAt = previous.UpdatedAt
	}
	if next.RunID == "" {
		next.RunID = previous.RunID
	}
	if next.RunID == previous.RunID && next.Sequence == 0 {
		next.Sequence = previous.Sequence
	}
	if previous.FlowID != "" {
		next.FlowID = previous.FlowID
	}
	next.AfterMessageID = previous.AfterMessageID
	return next
}
