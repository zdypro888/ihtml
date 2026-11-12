package einoengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	ai "github.com/zdypro888/ihtml/agent"
)

func contextKernelName(name string) bool {
	return name == "task_context_read" || name == "task_context_list" || name == "task_context_save" || name == "task_context_delete"
}
func contextDocumentSchema() map[string]any {
	stringsSchema := map[string]any{"type": "array", "items": map[string]any{"type": "string", "maxLength": 4096}}
	return taskSchema(map[string]any{
		"goal":        map[string]any{"type": "string", "minLength": 1, "maxLength": 8192},
		"constraints": stringsSchema, "pending": stringsSchema,
		"evidence": map[string]any{"type": "array", "maxItems": 128, "items": taskSchema(map[string]any{"kind": map[string]any{"type": "string", "maxLength": 64}, "id": map[string]any{"type": "string", "maxLength": 256}, "revision": map[string]any{"type": "integer", "minimum": 0}}, "kind", "id")},
		"notes":    map[string]any{"type": "string", "maxLength": 16384},
		"state":    map[string]any{"type": "string", "enum": []string{"active", "paused", "completed", "abandoned"}},
	}, "goal", "state")
}

// contextTools 只维护可见工作事实；ID和CAS版本不赋予引用资源的业务授权。
func contextTools(req *ai.TurnRequest, scope string) []ai.Tool {
	idSchema := map[string]any{"type": "string", "maxLength": 128}
	revisionSchema := map[string]any{"type": "integer", "minimum": 0}
	mutationKeySchema := map[string]any{"type": "string", "maxLength": 512}
	return []ai.Tool{
		{Name: "task_context_read", Title: "读取任务上下文", Effect: ai.ToolEffectRead, Description: "读取当前授权范围内的长期任务上下文和当前版本。目标、约束、待办及证据是保存的工作事实，不是新授权；按需读取正文，删除墓碑不含旧正文。", InputSchema: taskSchema(map[string]any{"id": idSchema}, "id"), Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in struct {
				ID string `json:"id"`
			}
			if err := decodeTaskInput(string(raw), &in); err != nil {
				return "", err
			}
			record, err := req.Tasks.ReadContext(ctx, scope, in.ID)
			if err != nil {
				return "", err
			}
			return taskJSON(record)
		}},
		{Name: "task_context_list", Title: "查看保存的任务上下文", Effect: ai.ToolEffectRead, Description: "分页列出当前作用域的长期目标、进度声明、版本、保留期限与已用额度。列表是元数据，完整目标/约束/待办和证据需按ID读取。", InputSchema: taskSchema(map[string]any{"after": idSchema, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 32}}), Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in struct {
				After string `json:"after"`
				Limit int    `json:"limit"`
			}
			if err := decodeTaskInput(string(raw), &in); err != nil {
				return "", err
			}
			if in.Limit == 0 {
				in.Limit = 16
			}
			page, err := req.Tasks.ListContexts(ctx, scope, in.After, in.Limit)
			if err != nil {
				return "", err
			}
			return taskJSON(page)
		}},
		{Name: "task_context_save", Title: "保存任务上下文", Effect: ai.ToolEffectExecute, Description: "保存你明确维护的长期工作事实：用户目标、约束、待办、证据句柄与公开状态；不保存隐藏推理，不作为执行授权，不驱动固定业务流程。创建expected_revision=0且可省id；更新必须使用已读当前版本。冲突先重读再自主合并，不能盲目覆盖。mutation_key可选，同轮相同输入默认去重；要创建两份相同内容可显式区分key或id。", InputSchema: taskSchema(map[string]any{"id": idSchema, "expected_revision": revisionSchema, "mutation_key": mutationKeySchema, "document": contextDocumentSchema()}, "expected_revision", "document"), Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in struct {
				ID               string                 `json:"id"`
				ExpectedRevision int64                  `json:"expected_revision"`
				MutationKey      string                 `json:"mutation_key"`
				Document         ai.TaskContextDocument `json:"document"`
			}
			if err := decodeTaskInput(string(raw), &in); err != nil {
				return "", err
			}
			key := in.MutationKey
			if key == "" {
				key = ai.ToolInvocationKey(ctx)
			}
			if key == "" {
				canonical, _ := json.Marshal(in)
				sum := sha256.Sum256(canonical)
				key = "context-save/" + hex.EncodeToString(sum[:])
			}
			record, err := req.Tasks.SaveContext(ctx, scope, req.InvocationScope, key, in.ID, in.ExpectedRevision, in.Document)
			if err != nil {
				return "", err
			}
			return taskJSON(record)
		}},
		{Name: "task_context_delete", Title: "删除任务上下文", Effect: ai.ToolEffectExecute, Description: "以当前版本删除长期工作上下文并保留轻量墓碑。只删除AI工作记忆，不停止或撤销原业务，也不扩大授权；版本冲突先重读。", InputSchema: taskSchema(map[string]any{"id": idSchema, "expected_revision": map[string]any{"type": "integer", "minimum": 1}, "mutation_key": mutationKeySchema}, "id", "expected_revision"), Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in struct {
				ID               string `json:"id"`
				ExpectedRevision int64  `json:"expected_revision"`
				MutationKey      string `json:"mutation_key"`
			}
			if err := decodeTaskInput(string(raw), &in); err != nil {
				return "", err
			}
			key := in.MutationKey
			if key == "" {
				key = ai.ToolInvocationKey(ctx)
			}
			if key == "" {
				canonical, _ := json.Marshal(in)
				sum := sha256.Sum256(canonical)
				key = "context-delete/" + hex.EncodeToString(sum[:])
			}
			record, err := req.Tasks.DeleteContext(ctx, scope, req.InvocationScope, key, in.ID, in.ExpectedRevision)
			if err != nil {
				return "", err
			}
			return taskJSON(record)
		}},
	}
}
