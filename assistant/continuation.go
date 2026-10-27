package assistant

import (
	"context"

	"github.com/zdypro888/ihtml"
)

// NewToolContinuation 为已保存回执的动作构造通用续办事件；宿主调用前仍须重查实时权限。
// 事件只要求核查原请求的剩余步骤，不扩大范围，也不赋予下一步写操作任何批准。
func NewToolContinuation(ctx context.Context, result *ConfirmedToolResult) *ihtml.ChatClientEvent {
	if ctx == nil || ctx.Err() != nil || result == nil || result.Err != nil || !result.Started {
		return nil
	}
	return &ihtml.ChatClientEvent{Type: ihtml.ChatClientUserMessage, Messages: []ihtml.ChatMessage{{Role: "user", Content: "请根据本段对话中我原先明确提出的请求和刚保存的服务端回执继续处理剩余步骤。先查询原任务状态；仍在执行时查询或等待原任务。只处理原名单和范围，不重复已完成或结果未知的操作；后续写操作重新生成确认卡，没有剩余步骤则简短汇报。"}}}
}
