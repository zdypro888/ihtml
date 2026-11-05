package agent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"time"
)

// Reconcile 使用当前获准的只读回执恢复钩子核对原调用；新增证据，不篡改原执行事实或重放 Handler。
func (r *TaskRuntime) Reconcile(ctx context.Context, scope, id string, tools []Tool) (TaskInfo, error) {
	if scope == "" || r.cfg.Scope != "" && scope != r.cfg.Scope {
		return TaskInfo{}, ErrTaskNotFound
	}
	row, err := r.cfg.Store.GetTask(ctx, scope, id)
	if err != nil {
		return TaskInfo{}, WrapTaskStorage(err)
	}
	if !row.ExpiresAt.IsZero() && !row.ExpiresAt.After(time.Now()) {
		return TaskInfo{}, ErrTaskNotFound
	}
	var selected Tool
	for _, tool := range tools {
		if tool.Name == row.Info.Tool {
			selected = tool
			break
		}
	}
	if selected.Name == "" || selected.RecoverResult == nil {
		return TaskInfo{}, ErrTaskNotFound
	}
	if selected.Contract.Version != row.Info.ContractVersion {
		return TaskInfo{}, ErrTaskContract
	}
	if row.Info.ResolutionRef != nil {
		return r.visible(row), nil
	}
	type recovered struct {
		Found  bool   `json:"found"`
		Result string `json:"result"`
	}
	probe := Tool{Name: selected.Name, Effect: ToolEffectRead, Contract: ToolContract{Version: "receipt/" + selected.Contract.Version}, Handler: func(call context.Context, _ json.RawMessage) (string, error) {
		value, ok, e := selected.RecoverResult(WithToolInvocationKey(call, row.Invocation), append(json.RawMessage(nil), row.Args...))
		if e != nil {
			return "", e
		}
		b, _ := json.Marshal(recovered{ok, value})
		return string(b), nil
	}}
	info, err := r.Start(context.WithValue(ctx, synchronousInvocationKey{}, true), scope, "reconcile/"+id, rand.Text(), probe, json.RawMessage(`{}`))
	if err != nil {
		return TaskInfo{}, err
	}
	evidence, err := r.AwaitResult(ctx, scope, info.ID, map[string]bool{selected.Name: true})
	if err != nil {
		return TaskInfo{}, err
	}
	if evidence.State != "completed" {
		return evidence, ErrTaskResultUnavailable
	}
	body, err := r.ResolveResultInput(ctx, scope, TaskResultInput{ID: evidence.ID, SHA256: evidence.ResultRef.SHA256}, map[string]bool{selected.Name: true})
	if err != nil {
		return evidence, err
	}
	var receipt recovered
	if json.Unmarshal(body, &receipt) != nil {
		return evidence, ErrTaskResultUnavailable
	}
	if !receipt.Found {
		return r.visible(row), nil
	}
	for {
		current, err := r.cfg.Store.GetTask(ctx, scope, id)
		if err != nil {
			return TaskInfo{}, err
		}
		if current.Info.ResolutionRef != nil {
			return r.visible(current), nil
		}
		revision := current.Revision
		current.Info.ResolutionRef = evidence.ResultRef
		current.Revision++
		current.UpdatedAt = time.Now().UTC()
		ok, err := r.cfg.Store.ReplaceTask(ctx, current, revision)
		if err != nil {
			return TaskInfo{}, err
		}
		if ok {
			r.observeRecord(ctx, current, "task_receipt_verified")
			return r.visible(current), nil
		}
	}
}
