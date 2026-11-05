package einoengine

import (
	"context"
	"encoding/json"
	ai "github.com/zdypro888/ihtml/agent"
	"time"
)

func observationKernelName(name string) bool { return name == "task_trace" || name == "task_metrics" }
func observationTools(req *ai.TurnRequest) []ai.Tool {
	scope := ai.TaskScope(req)
	properties := map[string]any{"task_id": map[string]any{"type": "string", "maxLength": 256}, "wait_id": map[string]any{"type": "string", "maxLength": 256}, "trace_id": map[string]any{"type": "string", "maxLength": 128}, "context_id": map[string]any{"type": "string", "maxLength": 256}, "source_id": map[string]any{"type": "string", "maxLength": 256}, "subscription_id": map[string]any{"type": "string", "maxLength": 256}, "event_id": map[string]any{"type": "string", "maxLength": 256}, "kind": map[string]any{"type": "string", "maxLength": 64}, "after": map[string]any{"type": "string", "maxLength": 128}, "from": map[string]any{"type": "string", "format": "date-time"}, "to": map[string]any{"type": "string", "format": "date-time"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 1024}}
	handler := func(metrics bool) func(context.Context, json.RawMessage) (string, error) {
		return func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in struct {
				TaskID         string    `json:"task_id"`
				WaitID         string    `json:"wait_id"`
				TraceID        string    `json:"trace_id"`
				ContextID      string    `json:"context_id"`
				SourceID       string    `json:"source_id"`
				SubscriptionID string    `json:"subscription_id"`
				EventID        string    `json:"event_id"`
				Kind           string    `json:"kind"`
				After          string    `json:"after"`
				From           time.Time `json:"from"`
				To             time.Time `json:"to"`
				Limit          int       `json:"limit"`
			}
			if err := decodeTaskInput(string(raw), &in); err != nil {
				return "", err
			}
			if in.Limit == 0 {
				if metrics {
					in.Limit = 1024
				} else {
					in.Limit = 64
				}
			}
			q := ai.TaskObservationQuery{TaskID: in.TaskID, WaitID: in.WaitID, TraceID: in.TraceID, ContextID: in.ContextID, SourceID: in.SourceID, SubscriptionID: in.SubscriptionID, EventID: in.EventID, Kind: in.Kind, After: in.After, From: in.From, To: in.To, Limit: in.Limit}
			var result any
			var err error
			if metrics {
				result, err = req.Tasks.ObservationStats(ctx, scope, q)
			} else {
				result, err = req.Tasks.Observations(ctx, scope, q)
			}
			if err != nil {
				if text, ok := ai.TaskFailureJSON(err, "not_started", false, "", nil); ok {
					return text, nil
				}
				return "", err
			}
			return taskJSON(result)
		}
	}
	return []ai.Tool{{Name: "task_trace", Effect: ai.ToolEffectRead, Description: "查询当前授权作用域的调用、等待、上下文和事件追踪。只含关联ID与状态，不含参数/结果/事件正文。按after分页；并发写入期间这是有界快照，不是完整实时事件流。local_dropped/failed/pending公开当前执行器的观测背压，不证明业务失败。", InputSchema: taskSchema(properties), Handler: handler(false)}, {Name: "task_metrics", Effect: ai.ToolEffectRead, Description: "对当前授权作用域最多1024条追踪快照按kind/state/code汇总。truncated/next表示仍有后续数据，count不是全量业务成功次数；观测可能丢弃，真实执行以原账本/回执为准。你决定是否读取后续页或原记录。", InputSchema: taskSchema(properties), Handler: handler(true)}}
}
