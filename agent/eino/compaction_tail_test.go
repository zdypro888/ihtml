package einoengine

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestCompactionTailKeepsOnlyCompleteCurrentToolExchanges(t *testing.T) {
	call := func(id string) *schema.Message {
		return schema.AssistantMessage("", []schema.ToolCall{{ID: id, Type: "function", Function: schema.FunctionCall{Name: "probe", Arguments: `{}`}}})
	}
	result := func(id string) *schema.Message {
		return &schema.Message{Role: schema.Tool, ToolCallID: id, ToolName: "probe", Content: `{"fingerprint":"exact-plan"}`}
	}
	messages := []*schema.Message{schema.UserMessage("old task"), call("old"), result("old"), schema.UserMessage("current task"), call("first"), result("first"), call("second"), result("second"), call("pending")}
	tail := recentToolExchanges(messages, 2, 64<<10)
	if len(tail) != 4 || tail[0].ToolCalls[0].ID != "first" || tail[2].ToolCalls[0].ID != "second" || tail[3].Content != `{"fingerprint":"exact-plan"}` {
		t.Fatal("complete current exchanges not preserved")
	}
	if got := recentToolExchanges(messages, 2, 1); len(got) != 0 {
		t.Fatal("oversize exchange was truncated")
	}
	for _, bad := range [][]*schema.Message{{schema.UserMessage("task"), call("a"), result("wrong")}, {schema.UserMessage("task"), call("a"), result("a"), result("a")}} {
		if len(recentToolExchanges(bad, 2, 64<<10)) != 0 {
			t.Fatal("unpaired/duplicate tool result retained")
		}
	}
	large := result("large")
	large.Content = strings.Repeat("x", 70<<10)
	if len(recentToolExchanges([]*schema.Message{schema.UserMessage("task"), call("large"), large}, 2, 64<<10)) != 0 {
		t.Fatal("large result leaked past tail budget")
	}
}
