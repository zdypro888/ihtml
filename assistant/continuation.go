package assistant

import (
	"context"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

// NewToolContinuation 为已保存回执的动作构造通用续办事件；宿主调用前仍须重查实时权限。
// 事件只要求核查原请求的剩余步骤，不扩大范围，也不赋予下一步写操作任何批准。
func NewToolContinuation(ctx context.Context, result *ConfirmedToolResult) *ihtml.ChatClientEvent {
	if ctx == nil || ctx.Err() != nil || result == nil || result.Err != nil || !result.Started {
		return nil
	}
	return &ihtml.ChatClientEvent{Type: ihtml.ChatClientUserMessage, Messages: []ihtml.ChatMessage{{Role: "user", Content: agent.ToolContinuationText()}}}
}
