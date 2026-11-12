package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
	"github.com/zdypro888/ihtml/authorization"
)

type scopeFixture struct {
	cfg                Config
	store              *authorization.MemoryStore
	calls, previews    atomic.Int32
	previewUnavailable atomic.Bool
	tool               agent.Tool
}

func newScopeFixture(t *testing.T) *scopeFixture {
	t.Helper()
	f := &scopeFixture{store: authorization.NewMemoryStore()}
	f.tool = agent.Tool{Name: "modify", Effect: agent.ToolEffectWrite, ApprovalRequired: true, ScopeTargets: []string{"/path/id"}, InputSchema: map[string]any{"type": "object"}, Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
		if agent.ToolInvocationKey(ctx) == "" {
			t.Error("missing stable invocation")
		}
		f.calls.Add(1)
		return `{"saved":true}`, nil
	}}
	f.cfg = approvalConfig(t, f.tool.Handler)
	f.cfg.ScopeApproval = &ScopeApprovalConfig{StoreProvider: func(context.Context, *ihtml.ChatSessionContext) (authorization.Store, error) { return f.store, nil }}
	f.cfg.Confirmation.Preview = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage) (ihtml.ProposedToolCall, error) {
		f.previews.Add(1)
		if f.previewUnavailable.Load() {
			return ihtml.ProposedToolCall{}, ErrPreviewRefreshRequired
		}
		return ihtml.ProposedToolCall{Title: "Modify fixed value", Risk: "medium", Body: map[string]any{"fixed": "live"}}, nil
	}
	f.cfg.PrepareTurn = func(_ context.Context, _ *ihtml.ChatSessionContext, e *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
		text := ""
		if len(e.Messages) > 0 {
			text = e.Messages[0].Content
		}
		return &agent.TurnRequest{SessionID: "conversation", SessionCapability: "cap", InvocationScope: "run", UserText: text, Tools: []agent.Tool{f.tool}}, nil
	}
	f.cfg.Engine = &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		if req.UserText != "propose" {
			return &agent.TurnResult{Text: "ready"}, nil
		}
		for _, tool := range req.Tools {
			if tool.Name == authorizationRequest {
				args := scopeArgs("/path/id")
				result, err := tool.Handler(ctx, args)
				req.OnEvent(agent.Step{Kind: agent.StepToolCall, ToolName: tool.Name, Args: args, Result: result})
				return &agent.TurnResult{Text: "Approve once"}, err
			}
		}
		return nil, errors.New("scope tool missing")
	}}
	return f
}
func scopeArgs(pointer string) json.RawMessage {
	raw, _ := json.Marshal(scopeProposal{Tool: "modify", Arguments: json.RawMessage(`{"path":{"id":"template"},"body":{"fixed":"live"}}`), TargetPointer: pointer, Targets: []string{"one", "two", "three"}})
	return raw
}
func (f *scopeFixture) session(t *testing.T) (*session, *recorder) {
	r := newRecorder()
	return openSession(t, f.cfg, "owner", r).(*session), r
}
func scopeReq() *agent.TurnRequest {
	return &agent.TurnRequest{SessionID: "conversation", SessionCapability: "cap", InvocationScope: "later"}
}
func scopeFindTool(t *testing.T, s *session, original agent.Tool, req *agent.TurnRequest, name string) agent.Tool {
	t.Helper()
	tools, err := s.appendScopeTools([]agent.Tool{original}, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatal("missing scoped capability", name)
	return agent.Tool{}
}
func scopePrepared(t *testing.T, f *scopeFixture, s *session, targets ...string) (string, json.RawMessage) {
	t.Helper()
	p := newProjection(s, t.Context(), func() {})
	p.tools[f.tool.Name] = f.tool
	args := scopeArgs("/path/id")
	if len(targets) > 0 {
		var proposal scopeProposal
		_ = json.Unmarshal(args, &proposal)
		proposal.Targets = targets
		args, _ = json.Marshal(proposal)
	}
	call, ref, err := s.prepareScopeProposal(t.Context(), args, p, scopeReq())
	if err != nil {
		t.Fatal(err)
	}
	id := call.Body.(map[string]string)["id"]
	return id, ref
}
func scopeReferenceJSON(id, target string) json.RawMessage {
	return json.RawMessage(scopeJSON(scopeReference{ID: id, Target: target}))
}

func TestScopeApprovalUsesOneSignedCardBeforeAnyExecution(t *testing.T) {
	f := newScopeFixture(t)
	s, r := f.session(t)
	card := proposedCard(t, s, r)
	if card.Name != authorizationRequest || f.calls.Load() != 0 || f.previews.Load() != 1 {
		t.Fatal("per-target work occurred before approval", card.Name, f.calls.Load(), f.previews.Load())
	}
	var ref struct {
		ID string `json:"id"`
	}
	raw, _ := json.Marshal(card.Body)
	_ = json.Unmarshal(raw, &ref)
	execute := scopeFindTool(t, s, f.tool, scopeReq(), authorizationExecute)
	if _, err := execute.Handler(t.Context(), scopeReferenceJSON(ref.ID, "one")); err == nil || f.calls.Load() != 0 {
		t.Fatal("executed without signed approval", err)
	}
	confirmCard(s, card)
	r.wait(t, ihtml.ChatStreamAssistantDone, 2)
	for _, target := range []string{"one", "two", "three"} {
		if _, err := execute.Handler(t.Context(), scopeReferenceJSON(ref.ID, target)); err != nil {
			t.Fatal(err)
		}
	}
	if f.calls.Load() != 3 {
		t.Fatal("approved targets did not execute", f.calls.Load())
	}
}
func TestScopeApprovalOnlyAllowsTrustedTargetFields(t *testing.T) {
	f := newScopeFixture(t)
	s, _ := f.session(t)
	p := newProjection(s, t.Context(), func() {})
	p.tools[f.tool.Name] = f.tool
	if _, _, err := s.prepareScopeProposal(t.Context(), scopeArgs("/body/fixed"), p, scopeReq()); err == nil {
		t.Fatal("AI changed fixed modification into a target")
	}
	if f.previews.Load() != 0 || f.calls.Load() != 0 {
		t.Fatal("untrusted pointer reached host")
	}
}
func TestScopeApprovalSurvivesConnectionAndReadsOriginalReceiptAfterPreviewChanges(t *testing.T) {
	f := newScopeFixture(t)
	s, _ := f.session(t)
	id, ref := scopePrepared(t, f, s)
	request := scopeFindTool(t, s, f.tool, scopeReq(), authorizationRequest)
	if _, err := request.Handler(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	execute := scopeFindTool(t, s, f.tool, scopeReq(), authorizationExecute)
	first, err := execute.Handler(t.Context(), scopeReferenceJSON(id, "one"))
	if err != nil {
		t.Fatal(err)
	}
	f.previewUnavailable.Store(true)
	s.Close()
	restored, _ := f.session(t)
	again := scopeFindTool(t, restored, f.tool, scopeReq(), authorizationExecute)
	cached, err := again.Handler(t.Context(), scopeReferenceJSON(id, "one"))
	if err != nil || cached != first || f.calls.Load() != 1 {
		t.Fatal("cached receipt lost or replayed", err, f.calls.Load())
	}
	for _, change := range []func(*agent.TurnRequest){func(r *agent.TurnRequest) { r.SessionID = "other" }, func(r *agent.TurnRequest) { r.SessionCapability = "other" }} {
		req := scopeReq()
		change(req)
		tool := scopeFindTool(t, restored, f.tool, req, authorizationExecute)
		if _, err := tool.Handler(t.Context(), scopeReferenceJSON(id, "one")); err == nil {
			t.Fatal("cross binding read permitted")
		}
	}
}
func TestScopeApprovalStopRevokesPendingAndActiveScopes(t *testing.T) {
	f := newScopeFixture(t)
	s, _ := f.session(t)
	id, ref := scopePrepared(t, f, s)
	request := scopeFindTool(t, s, f.tool, scopeReq(), authorizationRequest)
	if _, err := request.Handler(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	pending, _ := scopePrepared(t, f, s, "distinct-pending")
	s.revokeScopeApprovals(t.Context())
	m := authorization.New(f.store)
	for _, grant := range []string{id, pending} {
		r, err := m.Get(t.Context(), authorization.Binding{Owner: "owner", Conversation: "conversation", Capability: "cap"}, grant)
		if err != nil || r.State != authorization.Revoked {
			t.Fatal("stop failed to revoke", grant, err)
		}
	}
	tool := scopeFindTool(t, s, f.tool, scopeReq(), authorizationExecute)
	if _, err := tool.Handler(t.Context(), scopeReferenceJSON(id, "two")); err == nil || f.calls.Load() != 0 {
		t.Fatal("stopped scope executed", err)
	}
}
func TestScopeAsyncRejectionRemainsError(t *testing.T) {
	f := newScopeFixture(t)
	f.tool.Completion = agent.ToolCompletionAsynchronous
	f.cfg.PresentToolResult = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage, string) ToolResultView {
		return ToolResultView{Title: "Rejected", Summary: "Original service refused", Status: ihtml.ChatToolError}
	}
	s, _ := f.session(t)
	id, ref := scopePrepared(t, f, s)
	request := scopeFindTool(t, s, f.tool, scopeReq(), authorizationRequest)
	if _, err := request.Handler(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	execute := scopeFindTool(t, s, f.tool, scopeReq(), authorizationExecute)
	raw, err := execute.Handler(t.Context(), scopeReferenceJSON(id, "one"))
	if err != nil {
		t.Fatal(err)
	}
	var result scopeResult
	if json.Unmarshal([]byte(raw), &result) != nil || result.View.Status != ihtml.ChatToolError {
		t.Fatal(fmt.Sprintf("asynchronous refusal reported as accepted: %s", raw))
	}
}

// 即使授权列表撤销失败，原执行代次失效仍阻止新连接使用已批准或待批准范围。
func TestScopeApprovalStopEpochSurvivesReconnectWithoutGrantRevocation(t *testing.T) {
	f := newScopeFixture(t)
	f.cfg.Tasks = &agent.TaskConfig{Store: agent.NewMemoryTaskStore(), Scope: agent.TaskScope(scopeReq())}
	s, _ := f.session(t)
	active, ref := scopePrepared(t, f, s)
	request := scopeFindTool(t, s, f.tool, scopeReq(), authorizationRequest)
	if _, err := request.Handler(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	_, pendingRef := scopePrepared(t, f, s, "distinct-pending")
	if err := s.tasks.CancelAll(); err != nil {
		t.Fatal(err)
	}
	// 直接调用运行时停止模拟持久授权服务暂时不可写；授权记录故意仍保留 active。
	record, err := authorization.New(f.store).Get(t.Context(), authorization.Binding{Owner: "owner", Conversation: "conversation", Capability: "cap"}, active)
	if err != nil || record.State != authorization.Active {
		t.Fatal(record.State, err)
	}
	restored, _ := f.session(t)
	execute := scopeFindTool(t, restored, f.tool, scopeReq(), authorizationExecute)
	if _, err = execute.Handler(t.Context(), scopeReferenceJSON(active, "two")); err == nil {
		t.Fatal("old execution epoch reused after stop")
	}
	activate := scopeFindTool(t, restored, f.tool, scopeReq(), authorizationRequest)
	if _, err = activate.Handler(t.Context(), pendingRef); err == nil {
		t.Fatal("pre-stop proposal activated after stop")
	}
	if f.calls.Load() != 0 {
		t.Fatal("stopped grant reached business handler")
	}
}

func TestScopePendingRequestCancellationAndConnectionCleanup(t *testing.T) {
	for _, closeConnection := range []bool{false, true} {
		t.Run(fmt.Sprint(closeConnection), func(t *testing.T) {
			f := newScopeFixture(t)
			s, events := f.session(t)
			card := proposedCard(t, s, events)
			var ref scopeReference
			raw, _ := json.Marshal(card.Body)
			_ = json.Unmarshal(raw, &ref)
			if closeConnection {
				s.Close()
			} else {
				dismissCard(s, card)
				events.wait(t, ihtml.ChatStreamCancelToolResult, 1)
			}
			record, err := authorization.New(f.store).Get(t.Context(), authorization.Binding{Owner: "owner", Conversation: "conversation", Capability: "cap"}, ref.ID)
			if err != nil || record.State != authorization.Revoked {
				t.Fatal("pending scope not reclaimed", record.State, err)
			}
			if f.calls.Load() != 0 {
				t.Fatal("cancellation executed business action")
			}
		})
	}
}

func TestScopePendingReviewReusesStoredRange(t *testing.T) {
	f := newScopeFixture(t)
	s, _ := f.session(t)
	id, _ := scopePrepared(t, f, s)
	p := newProjection(s, t.Context(), func() {})
	p.tools[f.tool.Name] = f.tool
	call, _, err := s.prepareScopeProposal(t.Context(), json.RawMessage(scopeJSON(map[string]string{"id": id})), p, scopeReq())
	if err != nil || call.Body.(map[string]string)["id"] != id || f.calls.Load() != 0 {
		t.Fatal("review lost frozen range", err)
	}
	_, _, err = s.prepareScopeProposal(t.Context(), json.RawMessage(scopeJSON(map[string]any{"id": id, "targets": []string{"outside"}})), p, scopeReq())
	if err == nil {
		t.Fatal("review changed approved scope")
	}
}

func TestScopeOldProposalCleanupCannotRevokeActivatedRange(t *testing.T) {
	f := newScopeFixture(t)
	s, _ := f.session(t)
	id, ref := scopePrepared(t, f, s)
	request := scopeFindTool(t, s, f.tool, scopeReq(), authorizationRequest)
	if _, err := request.Handler(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if err := authorization.New(f.store).RevokePending(t.Context(), scopeBinding(t.Context(), s, scopeReq()), id); err != nil {
		t.Fatal(err)
	}
	r, err := authorization.New(f.store).Get(t.Context(), scopeBinding(t.Context(), s, scopeReq()), id)
	if err != nil || r.State != authorization.Active {
		t.Fatal("old proposal revoked active scope", err, r.State)
	}
}

func TestScopeEquivalentProposalReusesOriginalAuthorization(t *testing.T) {
	f := newScopeFixture(t)
	s, _ := f.session(t)
	id, ref := scopePrepared(t, f, s)
	duplicate, _ := scopePrepared(t, f, s)
	if duplicate != id {
		t.Fatal("pending proposal duplicated")
	}
	request := scopeFindTool(t, s, f.tool, scopeReq(), authorizationRequest)
	if _, err := request.Handler(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	p := newProjection(s, t.Context(), func() {})
	p.tools[f.tool.Name] = f.tool
	_, _, err := s.prepareScopeProposal(t.Context(), scopeArgs("/path/id"), p, scopeReq())
	var reuse *scopeReuseError
	if !errors.As(err, &reuse) || reuse.record.ID != id {
		t.Fatal("active grant was replaced", err)
	}
	records, e := authorization.New(f.store).List(t.Context(), reuse.record.Binding)
	if e != nil || len(records) != 1 || f.calls.Load() != 0 {
		t.Fatal("duplicate grant or premature execution", len(records), e)
	}
	// 即使业务不再允许生成新预览，也必须交回原有效授权；实际执行仍逐项重新检查。
	f.previewUnavailable.Store(true)
	reused, prepareErr := s.prepareTool(t.Context(), request, scopeArgs("/path/id"), p, scopeReq())
	if prepareErr != nil || reused != activeScopeResult(reuse.record) {
		t.Fatal("confirmation bridge did not return original grant", prepareErr)
	}
	var result map[string]any
	_ = json.Unmarshal([]byte(activeScopeResult(reuse.record)), &result)
	if result["next_action"] != authorizationExecute || result["operation_completion"] != "unconfirmed" {
		t.Fatal(result)
	}
}

func TestScopeStatusPresentationRetainsStructuredFacts(t *testing.T) {
	f := newScopeFixture(t)
	s, _ := f.session(t)
	view, ok := s.scopeResultView(agent.Tool{Name: authorizationStatus, Title: "查询任务授权状态"}, `{"id":"original","status":"active","targets":["one"],"total":1,"private":"hidden"}`)
	if !ok || len(view.Result) == 0 || view.Title == "" {
		t.Fatal(view)
	}
	var data map[string]any
	_ = json.Unmarshal(view.Result, &data)
	if data["status"] != "active" || data["private"] != nil {
		t.Fatal(data)
	}
}

func TestConcurrentEquivalentProposalsShareOnePendingGrant(t *testing.T) {
	f := newScopeFixture(t)
	s, _ := f.session(t)
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := newProjection(s, t.Context(), func() {})
			p.tools[f.tool.Name] = f.tool
			call, _, err := s.prepareScopeProposal(t.Context(), scopeArgs("/path/id"), p, scopeReq())
			if err != nil {
				errs <- err
				return
			}
			ids <- call.Body.(map[string]string)["id"]
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	first := ""
	for id := range ids {
		if first != "" && id != first {
			t.Fatal("parallel duplicate grants")
		}
		first = id
	}
}
