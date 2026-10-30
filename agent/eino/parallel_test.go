package einoengine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	ai "github.com/zdypro888/ihtml/agent"
)

// 两个真实 Handler 必须相互遇见；第二个先完成也必须按 CallID 正确反馈模型。
func TestDeepAgentRunsIndependentToolsConcurrently(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := make(chan struct{}, 2)
	secondDone := make(chan struct{})
	var calls atomic.Int32
	state := &scriptedModelState{fn: func(input []*schema.Message, _ []*schema.ToolInfo) (*schema.Message, error) {
		results := map[string]string{}
		for _, msg := range input {
			if msg.Role == schema.Tool {
				results[msg.ToolCallID] = msg.Content
			}
		}
		if len(results) > 0 {
			if results["read-a"] != "result-a" || results["read-b"] != "result-b" {
				return nil, fmt.Errorf("parallel results lost or mismatched: %v", results)
			}
			return schema.AssistantMessage("两项查询完成", nil), nil
		}
		return schema.AssistantMessage("", []schema.ToolCall{
			{ID: "read-a", Type: "function", Function: schema.FunctionCall{Name: "read_item", Arguments: `{"item":"a"}`}},
			{ID: "read-b", Type: "function", Function: schema.FunctionCall{Name: "read_item", Arguments: `{"item":"b"}`}},
		}), nil
	}}
	// 屏障只由两个 Handler 都启动后释放，串行执行会超时而不是误通过。
	ready := make(chan struct{})
	go func() {
		for range 2 {
			select {
			case <-started:
			case <-ctx.Done():
				return
			}
		}
		close(ready)
	}()
	result, err := newNativeTestEngine(&scriptedModel{state: state}, nil).RunTurn(ctx, &ai.TurnRequest{
		System: "查询两项", UserText: "同时查询 a 和 b",
		Tools: []ai.Tool{{Name: "read_item", Effect: ai.ToolEffectRead, LoadMode: ai.ToolLoadImmediate, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"item": map[string]any{"type": "string"}}}, Handler: func(c context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Item string `json:"item"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", err
			}
			calls.Add(1)
			started <- struct{}{}
			select {
			case <-ready:
			case <-c.Done():
				return "", c.Err()
			}
			if args.Item == "a" {
				select {
				case <-secondDone:
				case <-c.Done():
					return "", c.Err()
				}
			} else {
				close(secondDone)
			}
			return "result-" + args.Item, nil
		}}},
	})
	if err != nil || result == nil || result.Text != "两项查询完成" || calls.Load() != 2 {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls.Load(), err)
	}
}
