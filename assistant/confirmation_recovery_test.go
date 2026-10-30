package assistant

import (
	"context"
	"encoding/json"
	"errors"
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
