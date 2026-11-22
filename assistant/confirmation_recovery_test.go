package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

// 写请求只执行一次；超时后的只读核对沿用原 invocation，可信回执才允许续办。
func TestConfirmedToolRecoversDurableReceipt(t *testing.T) {
	for _, scenario := range []string{"returned_error", "timeout", "missing", "lookup_error", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			var writes, reads, saved atomic.Int32
			cfg := approvalConfig(t, func(ctx context.Context, _ json.RawMessage) (string, error) {
				writes.Add(1)
				if scenario == "timeout" {
					<-ctx.Done()
				}
				return "", errors.New("PRIVATE_TRANSPORT_ERROR")
			})
			cfg.Confirmation.ToolTimeout = 100 * time.Millisecond
			prepare := cfg.PrepareTurn
			cfg.PrepareTurn = func(ctx context.Context, sc *ihtml.ChatSessionContext, ev *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
				req, err := prepare(ctx, sc, ev)
				req.Tools[0].RecoverResult = func(ctx context.Context, args json.RawMessage) (string, bool, error) {
					reads.Add(1)
					if ctx.Err() != nil || agent.ToolInvocationKey(ctx) != "invocation-secret" || string(args) != `{"secret":"original"}` {
						t.Error("recovery lost trusted invocation or live context")
					}
					if scenario == "missing" {
						return "", false, nil
					}
					if scenario == "lookup_error" {
						return "", false, errors.New("PRIVATE_DB_ERROR")
					}
					return `{"accepted":true,"id":"original-job"}`, true, nil
				}
				return req, err
			}
			cfg.ToolExecutionGuard = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage) error {
				if scenario == "revoked" && writes.Load() > 0 {
					return ihtml.ErrAccessDenied
				}
				return nil
			}
			cfg.CompleteTool = func(ctx context.Context, _ *ihtml.ChatSessionContext, evidence *ConfirmedToolResult) error {
				if evidence.Err == nil {
					if ctx.Err() != nil || evidence.Result != `{"accepted":true,"id":"original-job"}` {
						t.Error("receipt not preserved with live persistence context")
					}
					saved.Add(1)
				}
				return nil
			}
			cfg.ContinueAfterTool = func(ctx context.Context, _ *ihtml.ChatSessionContext, evidence *ConfirmedToolResult) (*ihtml.ChatClientEvent, error) {
				return NewToolContinuation(ctx, evidence), nil
			}
			r := newRecorder()
			s := openSession(t, cfg, "u", r)
			defer s.Close()
			confirmCard(s, proposedCard(t, s, r))
			ok := scenario == "returned_error" || scenario == "timeout"
			if ok {
				r.wait(t, ihtml.ChatStreamAssistantDone, 3)
			} else {
				r.wait(t, ihtml.ChatStreamTurnError, 1)
			}
			wantReads := int32(1)
			if scenario == "revoked" {
				wantReads = 0
			}
			if writes.Load() != 1 || reads.Load() != wantReads || (saved.Load() == 1) != ok {
				t.Fatalf("writes=%d reads=%d saved=%d", writes.Load(), reads.Load(), saved.Load())
			}
		})
	}
}

// 超时或只读核对未得出结果时，可以补充公开原因，但仍保留原失败判定。
func TestConfirmedToolKeepsPublicFailureWithoutConclusiveReceipt(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		timeout, publicHandler  bool
		publicRecovery, revoked bool
		wantCode, wantPublic    string
	}{
		{"timeout keeps handler reason", true, true, true, false, "deadline_exceeded", "handler_source"},
		{"timeout gets reason from recovery", true, false, true, false, "deadline_exceeded", "receipt_source"},
		{"non-timeout preserves its failure", false, false, true, false, "execution_unknown", "receipt_source"},
		{"private recovery error", true, false, false, false, "deadline_exceeded", ""},
		{"recovery access revoked", true, false, true, true, "deadline_exceeded", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var writes, reads atomic.Int32
			cfg := approvalConfig(t, func(ctx context.Context, _ json.RawMessage) (string, error) {
				writes.Add(1)
				if tc.timeout {
					<-ctx.Done()
				}
				err := errors.New("PRIVATE_HANDLER_CAUSE")
				if tc.publicHandler {
					err = agent.NewPublicError("handler_source", "请核对已知的原请求编号 fixture-original。", err)
				}
				return "PRIVATE_HANDLER_RESULT", err
			})
			cfg.Confirmation.ToolTimeout = 100 * time.Millisecond
			prepare := cfg.PrepareTurn
			cfg.PrepareTurn = func(ctx context.Context, sc *ihtml.ChatSessionContext, ev *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
				req, err := prepare(ctx, sc, ev)
				req.Tools[0].RecoverResult = func(ctx context.Context, args json.RawMessage) (string, bool, error) {
					reads.Add(1)
					if ctx.Err() != nil || agent.ToolInvocationKey(ctx) != "invocation-secret" || string(args) != `{"secret":"original"}` {
						t.Error("recovery lost the original invocation scope")
					}
					// 核对超时不能被误判为原工具超时；结果字段同样不能当作已核实的回执。
					err := errors.Join(context.DeadlineExceeded, errors.New("PRIVATE_RECOVERY_CAUSE"))
					if tc.publicRecovery {
						err = agent.NewPublicError("receipt_source", "请核对已知的原请求编号 fixture-original。", err)
					}
					return "PRIVATE_RECOVERY_RESULT", false, err
				}
				return req, err
			}
			cfg.ToolExecutionGuard = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage) error {
				if tc.revoked && writes.Load() != 0 {
					return ihtml.ErrAccessDenied
				}
				return nil
			}
			cfg.CompleteTool = func(_ context.Context, _ *ihtml.ChatSessionContext, result *ConfirmedToolResult) error {
				if !result.Started || result.Err == nil || errors.Is(result.Err, context.DeadlineExceeded) != tc.timeout {
					t.Error("completion lost the original classified failure")
				}
				if tc.timeout {
					return errors.New("PRIVATE_COMPLETION_CAUSE")
				}
				return nil
			}
			cfg.ContinueAfterTool = func(context.Context, *ihtml.ChatSessionContext, *ConfirmedToolResult) (*ihtml.ChatClientEvent, error) {
				t.Error("an inconclusive receipt cannot continue execution")
				return nil, nil
			}

			r := newRecorder()
			s := openSession(t, cfg, "owner", r)
			defer s.Close()
			card := proposedCard(t, s, r)
			confirmCard(s, card)
			events := r.wait(t, ihtml.ChatStreamTurnError, 1)
			var checked bool
			for _, ev := range events {
				if strings.Contains(ev.Error+ev.ToolSummary+string(ev.ToolResult), "PRIVATE") {
					t.Fatal("confirmation exposed a private failure detail")
				}
				if ev.Type != ihtml.ChatStreamToolResult {
					continue
				}
				checked = true
				var failure agent.TaskFailure
				if err := json.Unmarshal(ev.ToolResult, &failure); err != nil || ev.ToolStatus != ihtml.ChatToolUnknown ||
					failure.Code != tc.wantCode || failure.ExecutionState != "unknown" || !strings.Contains(ev.ToolSummary, "不自动重放") {
					t.Fatal("confirmation changed the execution facts", string(ev.ToolResult), err)
				}
				if tc.wantPublic == "" {
					if failure.Public != nil {
						t.Fatal("unapproved public failure information", failure.Public)
					}
				} else if failure.Public == nil || failure.Public.Code != tc.wantPublic || !strings.Contains(ev.ToolSummary, "fixture-original") {
					t.Fatal("confirmation lost approved failure information", string(ev.ToolResult))
				}
			}
			if !checked {
				t.Fatal("missing tool result for confirmed failure")
			}

			confirmCard(s, card)
			r.wait(t, ihtml.ChatStreamTurnError, 2)
			wantReads := int32(1)
			if tc.revoked {
				wantReads = 0
			}
			if writes.Load() != 1 || reads.Load() != wantReads {
				t.Fatalf("write or recovery ran again: writes=%d reads=%d", writes.Load(), reads.Load())
			}
		})
	}
}

// 明确的参数错误属于可修正的提案；不能签卡、调用写工具或把敏感底层错误塞给模型。
func TestInvalidProposalReturnsCorrectionWithoutApproval(t *testing.T) {
	var calls atomic.Int32
	cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return "", nil })
	cfg.Confirmation.Preview = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage) (ihtml.ProposedToolCall, error) {
		return ihtml.ProposedToolCall{}, errors.Join(ErrInvalidToolArguments, errors.New("PRIVATE_ARGUMENT_DETAIL"))
	}
	cfg.Engine = &fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		value, err := req.Tools[0].Handler(ctx, json.RawMessage(`{}`))
		if err != nil || value != invalidToolArgumentsResult {
			t.Fatalf("invalid proposal ended tool loop: %q %v", value, err)
		}
		return &agent.TurnResult{Text: "correcting arguments"}, nil
	}}
	r := newRecorder()
	s := openSession(t, cfg, "u", r)
	defer s.Close()
	send(s, "propose")
	for _, ev := range r.wait(t, ihtml.ChatStreamAssistantDone, 1) {
		if ev.Type == ihtml.ChatStreamConfirmationRequired || ev.Type == ihtml.ChatStreamTurnError {
			t.Fatal("invalid input produced approval or terminal failure", ev)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid proposal executed")
	}
}
