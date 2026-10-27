package einoengine

import (
	"context"
	"encoding/json"

	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/schema"
)

// 沿用 Eino 的摘要与持久事件替换，只在 Finalize 保留最近两组完整工具往返。
// 指纹、任务编号等不能只靠有损摘要重建；工具调用和结果须成对保留，避免虚构未知结果或重放动作。
func finalizeAgentSummary(ctx context.Context, original []*schema.Message, summary *schema.Message) ([]*schema.Message, error) {
	result, err := summarization.DefaultFinalize(ctx, original, summary)
	if err != nil {
		return nil, err
	}
	return append(result, recentToolExchanges(original, 2, 64<<10)...), nil
}

func recentToolExchanges(messages []*schema.Message, maxGroups, maxBytes int) []*schema.Message {
	boundary := -1
	for i, msg := range messages {
		if msg != nil && msg.Role == schema.User && !isToolSearchReminder(msg) {
			boundary = i
		}
	}
	groups := [][]*schema.Message{}
	for i := boundary + 1; i < len(messages); i++ {
		msg := messages[i]
		if msg == nil || msg.Role != schema.Assistant || len(msg.ToolCalls) == 0 {
			continue
		}
		pending := map[string]bool{}
		valid := true
		for _, call := range msg.ToolCalls {
			if call.ID == "" || pending[call.ID] {
				valid = false
			}
			pending[call.ID] = true
		}
		end := i + 1
		for end < len(messages) && messages[end] != nil && messages[end].Role == schema.Tool {
			id := messages[end].ToolCallID
			if !pending[id] {
				valid = false
			} else {
				delete(pending, id)
			}
			end++
		}
		if valid && len(pending) == 0 {
			groups = append(groups, messages[i:end])
		}
		i = end - 1
	}
	start := len(groups)
	used := 0
	for start > 0 && len(groups)-start < maxGroups {
		raw, err := json.Marshal(groups[start-1])
		// 不截断原始 JSON 或加密推理；超过预算时由摘要和只读重查恢复事实。
		if err != nil || used+len(raw) > maxBytes {
			break
		}
		used += len(raw)
		start--
	}
	var result []*schema.Message
	for _, group := range groups[start:] {
		result = append(result, group...)
	}
	return result
}
