package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

// 续跑会生成新的确认卡，绝不能借用上一张卡执行第二个写动作。
func TestConfirmedToolContinuesThroughFreshApproval(t *testing.T) {
	var writes atomic.Int32
	cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) {
		writes.Add(1)
		return `{"completed":true}`, nil
	})
	cfg.ContinueAfterTool = func(_ context.Context, _ *ihtml.ChatSessionContext, evidence *ConfirmedToolResult) (*ihtml.ChatClientEvent, error) {
		if !evidence.Started || evidence.Err != nil {
			t.Error("invalid continuation evidence")
		}
		return &ihtml.ChatClientEvent{Type: ihtml.ChatClientUserMessage, Messages: []ihtml.ChatMessage{{Role: "user", Content: "propose"}}}, nil
	}
	b, err := NewChecked(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := newRecorder()
	s, err := b.NewSession(context.Background(), &ihtml.ChatSessionContext{User: "u", Emit: r.emit})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := proposedCard(t, s, r)
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTool, ToolCall: &first})
	events := r.wait(t, ihtml.ChatStreamConfirmationRequired, 2)
	if writes.Load() != 1 {
		t.Fatalf("continuation executed unconfirmed writes: %d", writes.Load())
	}
	var second *ihtml.ProposedToolCall
	for _, ev := range events {
		if ev.Type == ihtml.ChatStreamConfirmationRequired {
			second = ev.ToolCall
		}
	}
	if second == nil || second.ConfirmationNonce == first.ConfirmationNonce {
		t.Fatal("approval was reused")
	}
}

func TestPublicTurnErrorNeverUsesProviderText(t *testing.T) {
	cfg := configFor(&fakeEngine{run: func(context.Context, *agent.TurnRequest) (*agent.TurnResult, error) {
		return nil, errors.New("PRIVATE_PROVIDER_SECRET")
	}})
	cfg.PublicTurnError = func(error) string { return "公开超时提示；查询原任务" }
	r := newRecorder()
	s := openSession(t, cfg, "user", r)
	send(s, "fixture")
	events := r.wait(t, ihtml.ChatStreamTurnError, 1)
	for _, event := range events {
		if strings.Contains(event.Error, "PRIVATE_PROVIDER_SECRET") {
			t.Fatal("provider error exposed")
		}
		if event.Type == ihtml.ChatStreamTurnError && event.Error != "公开超时提示；查询原任务" {
			t.Fatal("host formatter ignored")
		}
	}
}

func TestUnknownConfirmedToolDoesNotContinue(t *testing.T) {
	var continuations atomic.Int32
	cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) { return "", errors.New("unknown fixture") })
	cfg.ContinueAfterTool = func(context.Context, *ihtml.ChatSessionContext, *ConfirmedToolResult) (*ihtml.ChatClientEvent, error) {
		continuations.Add(1)
		return nil, nil
	}
	b, err := NewChecked(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r := newRecorder()
	s, err := b.NewSession(context.Background(), &ihtml.ChatSessionContext{User: "u", Emit: r.emit})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	card := proposedCard(t, s, r)
	s.HandleEvent(context.Background(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTool, ToolCall: &card})
	r.wait(t, ihtml.ChatStreamTurnError, 1)
	if continuations.Load() != 0 {
		t.Fatal("unknown result resumed plan")
	}
}

// 能力描述必须取自桥接配置，不得把宿主列表中的字符串当成已接通续办。
func TestModelsDescribeManagedContinuation(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) { return `{}`, nil })
		cfg.Models.DeepApprovalResume = "managed_turn"
		if enabled {
			cfg.ContinueAfterTool = func(context.Context, *ihtml.ChatSessionContext, *ConfirmedToolResult) (*ihtml.ChatClientEvent, error) {
				return nil, nil
			}
		}
		backend, err := NewChecked(cfg)
		if err != nil {
			t.Fatal(err)
		}
		models, err := backend.Models(context.Background())
		want := "manual_tool_only"
		if enabled {
			want = "managed_turn"
		}
		if err != nil || models.DeepApprovalResume != want {
			t.Fatalf("enabled=%v resume=%q err=%v", enabled, models.DeepApprovalResume, err)
		}
	}
}

// 确定的业务拒绝回执可交回宿主续办；执行异常仍由 TestUnknownConfirmedToolDoesNotContinue 阻止。
func TestKnownRejectedReceiptCanContinueAfterErrorStatus(t *testing.T) {
	cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) {
		return `{"state":"rejected","execution":"not_started"}`, nil
	})
	cfg.PresentToolResult = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage, string) ToolResultView {
		return ToolResultView{Status: ihtml.ChatToolError, Summary: "业务已明确拒绝，未执行"}
	}
	cfg.ContinueAfterTool = func(ctx context.Context, _ *ihtml.ChatSessionContext, evidence *ConfirmedToolResult) (*ihtml.ChatClientEvent, error) {
		return NewToolContinuation(ctx, evidence), nil
	}
	r := newRecorder()
	s := openSession(t, cfg, "u", r)
	defer s.Close()
	card := proposedCard(t, s, r)
	confirmCard(s, card)
	events := r.wait(t, ihtml.ChatStreamAssistantDone, 3)
	for _, ev := range events {
		if ev.Type == ihtml.ChatStreamTurnError {
			t.Fatal("known rejection ended continuation", ev)
		}
	}
}
