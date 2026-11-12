package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
	"github.com/zdypro888/ihtml/authorization"
)

// ScopeApprovalConfig 将任务授权保存到宿主受控存储；AI 的任务上下文不参与授权判断。
type ScopeApprovalConfig struct {
	StoreProvider func(context.Context, *ihtml.ChatSessionContext) (authorization.Store, error)
	Lifetime      time.Duration
}

const authorizationRequest = "authorization_request"
const authorizationExecute = "authorization_execute"
const authorizationStatus = "authorization_status"
const authorizationRevoke = "authorization_revoke"

func scopeTool(name string) bool {
	return name == authorizationRequest || name == authorizationExecute || name == authorizationStatus || name == authorizationRevoke
}

type scopeProposal struct {
	ID            string          `json:"id,omitempty"`
	Tool          string          `json:"tool"`
	Arguments     json.RawMessage `json:"arguments"`
	TargetPointer string          `json:"target_pointer"`
	Targets       []string        `json:"targets"`
}
type scopeReference struct {
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	ID     string `json:"id"`
	Target string `json:"target,omitempty"`
}
type scopeResult struct {
	Status  string         `json:"status"`
	ID      string         `json:"id"`
	Targets int            `json:"targets,omitempty"`
	Result  string         `json:"result,omitempty"`
	View    ToolResultView `json:"view"`
}

func scopeJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func scopeContract(t agent.Tool) string {
	b, _ := json.Marshal([]any{t.Name, t.Contract, t.InputSchema, t.Effect, t.Completion, t.RequiredAction, t.ScopeTargets})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func scopeBinding(ctx context.Context, s *session, req *agent.TurnRequest) authorization.Binding {
	return authorization.Binding{Owner: s.sctx.User, Conversation: req.SessionID, Capability: req.SessionCapability, Goal: agent.TaskGoalID(ctx)}
}
func (s *session) scopeManager(ctx context.Context) (*authorization.Manager, error) {
	h := s.sctx
	store, err := s.backend.cfg.ScopeApproval.StoreProvider(ctx, &h)
	if err != nil {
		return nil, err
	}
	return authorization.New(store), nil
}

// appendScopeTools 将通用授权能力置于原业务工具之上；内部工具不得冒充宿主接口。
func (s *session) appendScopeTools(tools []agent.Tool, req *agent.TurnRequest) ([]agent.Tool, error) {
	if s.backend.cfg.ScopeApproval == nil || s.sctx.Access.ReadOnly {
		return tools, nil
	}
	originals := map[string]agent.Tool{}
	for _, t := range tools {
		if scopeTool(t.Name) {
			return nil, errors.New("assistant: reserved authorization tool name")
		}
		originals[t.Name] = t
	}
	binding := scopeBinding(context.Background(), s, req)
	s.mu.Lock()
	s.scopeApprovalBinding = binding
	s.mu.Unlock()
	schema := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	str := map[string]any{"type": "string"}
	// 请求本身只激活已签名的持久范围；从来不在 Handler 内批量执行业务。
	request := agent.Tool{Name: authorizationRequest, Title: "申请任务授权", Description: "先请求一次任务授权，再逐项执行。适用于同一操作、固定修改内容、已确定的有限目标集合；无需先生成每个目标的确认卡。arguments是一个目标的完整参数模板，target_pointer是模板中允许替换的字符串目标字段RFC6901路径，targets是精确目标名单。其他参数固定不变。批准后用authorization_execute执行，不能把目标指针指向修改值或放宽业务前置条件。", Effect: agent.ToolEffectWrite, ApprovalRequired: true, InputSchema: schema(map[string]any{"tool": str, "arguments": map[string]any{"type": "object"}, "target_pointer": str, "targets": map[string]any{"type": "array", "items": str, "minItems": 1, "maxItems": authorization.MaxTargets}}, "tool", "arguments", "target_pointer", "targets")}
	request.InputSchema["properties"].(map[string]any)["id"] = str
	delete(request.InputSchema, "required")
	request.InputSchema["oneOf"] = []any{map[string]any{"required": []string{"id"}, "not": map[string]any{"anyOf": []any{map[string]any{"required": []string{"tool"}}, map[string]any{"required": []string{"arguments"}}, map[string]any{"required": []string{"target_pointer"}}, map[string]any{"required": []string{"targets"}}}}}, map[string]any{"required": []string{"tool", "arguments", "target_pointer", "targets"}, "not": map[string]any{"required": []string{"id"}}}}
	request.Description += " 恢复历史待确认授权时只传id，由服务端重读原固定范围重新签卡；不能改写名单或参数。"
	var catalog []map[string]any
	for _, tool := range tools {
		if tool.ApprovalRequired && len(tool.ScopeTargets) > 0 {
			catalog = append(catalog, map[string]any{"tool": tool.Name, "target_pointers": tool.ScopeTargets})
		}
	}
	request.Description += " 当前允许的工具和目标字段：" + scopeJSON(catalog)
	request.Handler = func(ctx context.Context, args json.RawMessage) (string, error) {
		var input struct {
			ID     string `json:"id"`
			Digest string `json:"digest"`
		}
		if json.Unmarshal(args, &input) != nil {
			return "", authorization.ErrInvalid
		}
		m, err := s.scopeManager(ctx)
		if err != nil {
			return "", err
		}
		record, err := m.Get(ctx, scopeBinding(ctx, s, req), input.ID)
		if err != nil {
			return "", err
		}
		if s.tasks != nil {
			if err = s.tasks.CheckExecutionEpoch(ctx, agent.TaskScope(req), record.Plan.ExecutionEpoch); err != nil {
				return "", err
			}
		}
		record, err = m.Activate(ctx, scopeBinding(ctx, s, req), input.ID, input.Digest)
		if err != nil {
			return "", err
		}
		return scopeJSON(scopeResult{Status: authorization.Active, ID: record.ID, Targets: len(record.Plan.Targets), View: ToolResultView{Title: "任务已授权", Summary: fmt.Sprintf("已授权 %d 个目标，后续执行将持续显示进度和结果。", len(record.Plan.Targets)), Status: ihtml.ChatToolSuccess}}), nil
	}
	execute := agent.Tool{Name: authorizationExecute, Title: "执行已授权操作", Description: "执行已获用户批准的一个目标。仅传原授权id和名单中的target；工具从冻结模板构造原调用，检查当前权限与条件后执行。按依赖自主串行、并发或后台执行。重复调用只读取原记录，未知结果不可重做。", Effect: agent.ToolEffectWrite, InputSchema: schema(map[string]any{"id": str, "target": str}, "id", "target")}
	execute.Handler = func(ctx context.Context, args json.RawMessage) (string, error) {
		var ref scopeReference
		if json.Unmarshal(args, &ref) != nil {
			return "", authorization.ErrInvalid
		}
		m, err := s.scopeManager(ctx)
		if err != nil {
			return "", err
		}
		b := scopeBinding(ctx, s, req)
		record, err := m.Get(ctx, b, ref.ID)
		if err != nil {
			return "", err
		}
		original, ok := originals[record.Plan.Tool]
		if !ok || !original.ApprovalRequired || scopeContract(original) != record.Plan.ContractVersion {
			return "", ihtml.ErrAccessDenied
		}
		input, err := authorization.BuildArguments(record.Plan, ref.Target)
		if err != nil {
			return "", err
		}
		// 所有真实权限及业务预览条件仍在执行前校验，但不会再签发单项卡。
		h := s.sctx
		if err = s.backend.cfg.ToolExecutionGuard(ctx, &h, original, input); err != nil {
			return "", ihtml.ErrAccessDenied
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if s.tasks != nil {
			if err = s.tasks.CheckExecutionEpoch(ctx, agent.TaskScope(req), record.Plan.ExecutionEpoch); err != nil {
				return "", err
			}
		}
		claim, err := m.Authorize(ctx, record.Binding, record.ID, original.Name, scopeContract(original), input)
		if err != nil {
			return "", err
		}
		if !claim.Fresh {
			if claim.Result != "" {
				return claim.Result, nil
			}
			return scopeJSON(scopeResult{Status: authorization.Unknown, ID: ref.ID, View: ToolResultView{Title: original.Name, Status: ihtml.ChatToolUnknown, Summary: "原操作已接纳，结果尚未可靠保存；请核对原记录，不重复执行。"}}), nil
		}
		h = s.sctx
		preview, previewErr := s.backend.cfg.Confirmation.Preview(ctx, &h, original, input)
		if previewErr != nil || preview.Risk == "blocked" {
			result := scopeJSON(scopeResult{Status: "not_started", ID: ref.ID, View: ToolResultView{Title: original.Name, Status: ihtml.ChatToolError, Summary: "未执行：原业务条件已变化或无法核验，需要重新核对后申请授权。"}})
			finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := m.Finish(finish, record.Binding, claim, authorization.Outcome{State: authorization.Completed, Result: result}); err != nil {
				return "", err
			}
			return result, nil
		}
		callCtx := agent.WithToolInvocationKey(ctx, claim.Invocation)
		// claim先持久化。发生异常也保留墓碑，无法确认结果的副作用不能重做。
		var result string
		var runErr error
		func() {
			defer func() {
				if recover() != nil {
					runErr = errors.New("assistant: authorized tool panicked")
				}
			}()
			if callCtx.Err() != nil {
				runErr = callCtx.Err()
				return
			}
			result, runErr = original.Handler(callCtx, input)
		}()
		view := s.publicToolResult(ctx, original, input, result)
		state := authorization.Completed
		if view.Status == ihtml.ChatToolUnknown {
			state = authorization.Unknown
		}
		if runErr != nil {
			state = authorization.Unknown
			failure := agent.TaskFailureFrom(runErr, "unknown", true, claim.Invocation, nil)
			facts, _ := json.Marshal(failure)
			view = ToolResultView{Title: preview.Title, Status: ihtml.ChatToolUnknown, Summary: failure.Message, Result: facts}
			// 范围回执也只保存审定失败事实，不能将失败 Handler 的原始响应旁路公开。
			result, _ = agent.TaskFailureJSON(failure, "unknown", true, claim.Invocation, nil)
		}
		if original.Completion == agent.ToolCompletionAsynchronous && runErr == nil && (view.Status == "" || view.Status == ihtml.ChatToolSuccess) {
			view.Status = ihtml.ChatToolAccepted
		}
		resultStatus := state
		if view.Status == ihtml.ChatToolAccepted {
			resultStatus = "accepted"
		} else if view.Status == ihtml.ChatToolError {
			resultStatus = "failed"
		}
		envelope := scopeJSON(scopeResult{Status: resultStatus, ID: ref.ID, Result: result, View: view})
		// 取消不丢弃已得到的回执；保存有独立短期限，不能恢复业务执行。
		finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err = m.Finish(finish, record.Binding, claim, authorization.Outcome{State: state, Result: envelope}); err != nil {
			return scopeJSON(scopeResult{Status: authorization.Unknown, ID: ref.ID, View: ToolResultView{Title: preview.Title, Status: ihtml.ChatToolUnknown, Summary: "原操作回执未能可靠保存，请核对原记录，不要重复执行。"}}), nil
		}
		return envelope, nil
	}
	execute.ResolveResources = func(ctx context.Context, args json.RawMessage) ([]agent.TaskResourceRequest, error) {
		var ref scopeReference
		if json.Unmarshal(args, &ref) != nil {
			return nil, authorization.ErrInvalid
		}
		m, err := s.scopeManager(ctx)
		if err != nil {
			return nil, err
		}
		r, err := m.Get(ctx, scopeBinding(ctx, s, req), ref.ID)
		if err != nil {
			return nil, err
		}
		original, ok := originals[r.Plan.Tool]
		if !ok || scopeContract(original) != r.Plan.ContractVersion {
			return nil, ihtml.ErrAccessDenied
		}
		input, err := authorization.BuildArguments(r.Plan, ref.Target)
		if err != nil {
			return nil, err
		}
		if original.ResolveResources != nil {
			return original.ResolveResources(ctx, input)
		}
		return nil, nil
	}

	status := agent.Tool{Name: authorizationStatus, Title: "查询任务授权状态", Description: "分段读取原任务授权的固定目标和状态；offset从0开始，limit默认50最多200。只查询，不执行。", Effect: agent.ToolEffectRead, InputSchema: schema(map[string]any{"id": str, "offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}, "id")}
	status.Handler = func(ctx context.Context, args json.RawMessage) (string, error) {
		var ref scopeReference
		if json.Unmarshal(args, &ref) != nil {
			return "", authorization.ErrInvalid
		}
		m, err := s.scopeManager(ctx)
		if err != nil {
			return "", err
		}
		r, err := m.Get(ctx, scopeBinding(ctx, s, req), ref.ID)
		if err != nil {
			return "", err
		}
		if ref.Offset < 0 || ref.Offset > len(r.Plan.Targets) || ref.Limit < 0 || ref.Limit > 200 {
			return "", authorization.ErrInvalid
		}
		if ref.Limit == 0 {
			ref.Limit = 50
		}
		end := min(len(r.Plan.Targets), ref.Offset+ref.Limit)
		return scopeJSON(map[string]any{"id": r.ID, "status": r.State, "tool": r.Plan.Tool, "targets": r.Plan.Targets[ref.Offset:end], "total": len(r.Plan.Targets), "next_offset": end, "has_more": end < len(r.Plan.Targets), "expires_at": r.ExpiresAt}), nil
	}
	revoke := agent.Tool{Name: authorizationRevoke, Title: "撤销任务授权", Description: "撤销本会话指定任务授权，阻止尚未开始的目标执行；已执行的结果不会撤回。", Effect: agent.ToolEffectWrite, InputSchema: schema(map[string]any{"id": str}, "id")}
	revoke.Handler = func(ctx context.Context, args json.RawMessage) (string, error) {
		var ref scopeReference
		if json.Unmarshal(args, &ref) != nil {
			return "", authorization.ErrInvalid
		}
		m, err := s.scopeManager(ctx)
		if err != nil {
			return "", err
		}
		err = m.Revoke(ctx, scopeBinding(ctx, s, req), ref.ID)
		return scopeJSON(map[string]any{"id": ref.ID, "status": authorization.Revoked}), err
	}
	return append(tools, request, execute, status, revoke), nil
}

// prepareScopeProposal 只核验一次模板以说明操作，不枚举执行业务预览。
func (s *session) prepareScopeProposal(ctx context.Context, args json.RawMessage, p *projection, req *agent.TurnRequest) (ihtml.ProposedToolCall, json.RawMessage, error) {
	var input scopeProposal
	if json.Unmarshal(args, &input) != nil {
		return ihtml.ProposedToolCall{}, nil, ErrInvalidToolArguments
	}
	m, err := s.scopeManager(ctx)
	if err != nil {
		return ihtml.ProposedToolCall{}, nil, err
	}
	var existing *authorization.Record
	if input.ID != "" {
		if input.Tool != "" || len(input.Arguments) > 0 || input.TargetPointer != "" || len(input.Targets) > 0 {
			return ihtml.ProposedToolCall{}, nil, ErrInvalidToolArguments
		}
		r, e := m.Get(ctx, scopeBinding(ctx, s, req), input.ID)
		if e != nil || r.State != authorization.Pending {
			return ihtml.ProposedToolCall{}, nil, ErrPreviewRefreshRequired
		}
		if s.tasks != nil {
			if e = s.tasks.CheckExecutionEpoch(ctx, agent.TaskScope(req), r.Plan.ExecutionEpoch); e != nil {
				return ihtml.ProposedToolCall{}, nil, e
			}
		}
		existing = &r
		input.Tool = r.Plan.Tool
		input.Arguments = r.Plan.Arguments
		input.TargetPointer = r.Plan.TargetPointer
		input.Targets = r.Plan.Targets
	}
	tool, ok := p.tools[input.Tool]
	if !ok || !tool.ApprovalRequired || scopeTool(tool.Name) || !slices.Contains(tool.ScopeTargets, input.TargetPointer) {
		return ihtml.ProposedToolCall{}, nil, ihtml.ErrAccessDenied
	}
	plan := authorization.Plan{Tool: tool.Name, ContractVersion: scopeContract(tool), Arguments: input.Arguments, TargetPointer: input.TargetPointer, Targets: input.Targets}
	if existing != nil && existing.Plan.ContractVersion != plan.ContractVersion {
		return ihtml.ProposedToolCall{}, nil, ErrPreviewRefreshRequired
	}
	// 先由标准协议校验指针和目标，再使用代表性目标取得宿主公开的操作说明。
	if len(input.Targets) == 0 {
		return ihtml.ProposedToolCall{}, nil, ErrInvalidToolArguments
	}
	sample, err := authorization.BuildArguments(plan, input.Targets[0])
	if err != nil {
		return ihtml.ProposedToolCall{}, nil, ErrInvalidToolArguments
	}
	h := s.sctx
	if err = s.backend.cfg.ToolExecutionGuard(ctx, &h, tool, sample); err != nil {
		return ihtml.ProposedToolCall{}, nil, err
	}
	h = s.sctx
	call, err := s.backend.cfg.Confirmation.Preview(ctx, &h, tool, sample)
	if err != nil || call.Risk == "blocked" {
		return ihtml.ProposedToolCall{}, nil, ErrPreviewRefreshRequired
	}
	life := s.backend.cfg.ScopeApproval.Lifetime
	if life == 0 {
		life = 24 * time.Hour
	}
	if s.tasks != nil {
		plan.ExecutionEpoch, err = s.tasks.ExecutionEpoch(ctx, agent.TaskScope(req))
		if err != nil {
			return ihtml.ProposedToolCall{}, nil, err
		}
	}
	var record authorization.Record
	if existing != nil {
		record = *existing
	} else {
		record, err = m.Prepare(ctx, scopeBinding(ctx, s, req), plan, time.Now().Add(life))
	}
	if err != nil {
		return ihtml.ProposedToolCall{}, nil, err
	}
	reference, _ := json.Marshal(map[string]string{"id": record.ID, "digest": record.Digest})
	details := call.Review
	call.Title = "授权任务 · " + call.Title
	call.Reason = "确认后，AI 可在下列固定目标范围内逐项执行相同操作。每个目标最多执行一次；权限和业务条件仍逐项检查，范围或操作内容变化需重新确认。"
	call.Review = &ihtml.OperationReview{Scope: fmt.Sprintf("固定目标 %d 个", len(record.Plan.Targets)), After: map[string]any{"目标清单": record.Plan.Targets, "操作说明": details, "参数示例": call.Body}}
	call.Body = map[string]string{"id": record.ID, "digest": record.Digest}
	call.Path = ""
	call.Method = ""
	call.Presentation = &ihtml.ToolCallPresentation{ConfirmText: "确认并开始任务", Facts: []ihtml.ToolCallFact{{Label: "目标数量", Value: fmt.Sprint(len(record.Plan.Targets))}, {Label: "授权期限", Value: record.ExpiresAt.Format(time.RFC3339)}}}
	return call, reference, nil
}
func (s *session) scopeResultView(tool agent.Tool, result string) (ToolResultView, bool) {
	if s.backend.cfg.ScopeApproval == nil || !scopeTool(tool.Name) {
		return ToolResultView{}, false
	}
	var out scopeResult
	if json.Unmarshal([]byte(result), &out) != nil {
		return ToolResultView{}, true
	}
	return out.View, true
}

// 显式停止撤销当前会话的持久授权，后台断线/关闭仅解绑连接，不撤销。
func (s *session) revokeScopeApprovals(ctx context.Context) {
	if s.backend.cfg.ScopeApproval == nil {
		return
	}
	s.mu.Lock()
	b := s.scopeApprovalBinding
	s.mu.Unlock()
	if !b.Valid() {
		return
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	m, err := s.scopeManager(cleanup)
	if err == nil {
		var records []authorization.Record
		records, err = m.List(cleanup, b)
		for _, r := range records {
			if r.State == authorization.Active || r.State == authorization.Pending {
				if e := m.Revoke(cleanup, b, r.ID); e != nil {
					err = e
				}
			}
		}
	}
	if err != nil {
		_ = s.emit(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamAssistantStatus, Status: "停止已请求，但授权撤销未能保存，请核对原授权状态。"})
	}
}
