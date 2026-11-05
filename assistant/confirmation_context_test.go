package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

// 实际确认执行及只读核对必须使用新调用句柄，原提案仅提供父句柄与冻结授权身份。
func TestConfirmedExecutionContextUsesCurrentTaskAndOriginalAuthority(t *testing.T) {
	for _, tc := range []struct {
		background bool
		invocation string
	}{{false, "frozen-authority"}, {true, "frozen-authority"}, {false, ""}, {true, ""}} {
		background, invocation := tc.background, tc.invocation
		t.Run(fmt.Sprint("background=", background, "/invocation=", invocation), func(t *testing.T) {
			type identityKey struct{}
			type observed struct {
				id, invocation, identity string
				background               bool
			}
			capture := func(ctx context.Context) observed {
				identity, _ := ctx.Value(identityKey{}).(string)
				return observed{agent.TaskID(ctx), agent.ToolInvocationKey(ctx), identity, agent.IsBackgroundTask(ctx)}
			}
			seen := make(chan observed, 2)
			rec := newRecorder()
			cfg := approvalConfig(t, func(ctx context.Context, _ json.RawMessage) (string, error) {
				seen <- capture(ctx)
				return "", errors.New("read saved receipt")
			})
			cfg.Tasks = &agent.TaskConfig{}
			prepare := cfg.PrepareTurn
			cfg.PrepareTurn = func(ctx context.Context, sc *ihtml.ChatSessionContext, ev *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
				req, err := prepare(ctx, sc, ev)
				req.Tools[0].RecoverResult = func(ctx context.Context, _ json.RawMessage) (string, bool, error) {
					seen <- capture(ctx)
					return "saved receipt", true, nil
				}
				return req, err
			}
			cfg.Engine = &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
				parent := agent.WithToolInvocationKey(ctx, invocation)
				args := json.RawMessage(`{"target":"exact"}`)
				if background {
					row, err := req.Tasks.Start(parent, agent.TaskScope(req), req.InvocationScope, "proposal", req.Tools[0], args)
					if err != nil {
						return nil, err
					}
					_, err = req.Tasks.Wait(ctx, agent.TaskScope(req), []string{row.ID}, map[string]bool{req.Tools[0].Name: true}, time.Second, true)
					return &agent.TurnResult{Text: "proposal"}, err
				}
				_, err := req.Tasks.Call(parent, agent.TaskScope(req), req.InvocationScope, "proposal", req.Tools[0], args)
				return &agent.TurnResult{Text: "proposal"}, err
			}}
			backend, err := NewChecked(cfg)
			if err != nil {
				t.Fatal(err)
			}
			chat, err := backend.NewSession(context.WithValue(t.Context(), identityKey{}, "trusted-session"), &ihtml.ChatSessionContext{User: "alice", Emit: rec.emit})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(chat.Close)
			s := chat.(*session)
			card := proposedCard(t, chat, rec)
			// 确认请求自己的值不能替换冻结的业务 invocation。
			chat.HandleEvent(agent.WithToolInvocationKey(t.Context(), "unrelated-request"), &ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTool, ToolCall: &card})
			var execution, recovered observed
			for _, dst := range []*observed{&execution, &recovered} {
				select {
				case *dst = <-seen:
				case <-time.After(time.Second):
					t.Fatal("confirmed handler/receipt did not run")
				}
			}
			if execution != recovered || execution.id == "" || execution.invocation != invocation || execution.identity != "trusted-session" || execution.background != background {
				t.Fatal("execution/recovery context mismatch", execution, recovered)
			}
			scope := agent.TaskScope(&agent.TurnRequest{SessionID: "alice"})
			rows, err := s.tasks.Wait(t.Context(), scope, []string{execution.id}, map[string]bool{"destroy": true}, time.Second, true)
			if err != nil || len(rows) != 1 || rows[0].ParentID == "" || rows[0].ParentID == execution.id || rows[0].State != "completed" {
				t.Fatal("actual execution must reference its own child ledger record", rows, err)
			}
		})
	}
}

// 通用身份 setter 的空值必须屏蔽旧值，而不是保持另一个逻辑调用的身份。
func TestInvocationKeyExplicitlyClearsInheritedIdentity(t *testing.T) {
	ctx := agent.WithToolInvocationKey(t.Context(), "old")
	ctx = agent.WithToolInvocationKey(ctx, "")
	if got := agent.ToolInvocationKey(ctx); got != "" {
		t.Fatal("inherited invocation was not cleared", got)
	}
}
