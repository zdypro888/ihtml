package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
)

// 多个无关工具走同一快照协议，零值执行引用不能丢失提案，也不能恢复授权。
func TestToolHistoryRetainsProposalAndPublicResult(t *testing.T) {
	for _, name := range []string{"rename_resource", "set_options", "submit_job"} {
		t.Run(name, func(t *testing.T) {
			call := ihtml.ProposedToolCall{ID: "original", Name: name, Title: "原范围", ConfirmationGroup: "group", Method: "PUT", Path: "/original", Reason: "原确认说明", ConfirmationToken: "PRIVATE_TOKEN", ConfirmationNonce: "PRIVATE_NONCE", RequiresConfirmation: true, Body: map[string]any{"target": "one"}}
			original, ok := ToolHistoryEvent(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamConfirmationRequired, ToolCall: &call, ToolStatus: ihtml.ChatToolPending})
			if !ok {
				t.Fatal("proposal missing")
			}
			original.AfterMessageID = "message-one"
			original.Call.Body.(map[string]any)["target"] = "detached"
			if call.Body.(map[string]any)["target"] != "one" {
				t.Fatal("snapshot aliased signed call")
			}
			next, ok := ToolHistoryEvent(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamToolResult, ToolCall: &ihtml.ProposedToolCall{ID: "original", ConfirmationNonce: "PRIVATE_NONCE"}, ToolStatus: ihtml.ChatToolAccepted, ToolSummary: "accepted only", ToolResult: json.RawMessage(`{"jobId":"original-job"}`)})
			if !ok {
				t.Fatal("receipt missing")
			}
			merged := MergeToolHistory(original, next)
			raw, _ := json.Marshal(merged)
			if !merged.ProposalRecorded || merged.Call.Path != "/original" || merged.AfterMessageID != "message-one" || merged.Status != ihtml.ChatToolAccepted || !strings.Contains(string(raw), "original-job") || strings.Contains(string(raw), "PRIVATE_") || merged.Call.RequiresConfirmation {
				t.Fatal(string(raw))
			}
			merged.Status = ihtml.ChatToolSuccess
			next.Status = ihtml.ChatToolPending
			if MergeToolHistory(merged, next).Status != ihtml.ChatToolSuccess {
				t.Fatal("late proposal regressed terminal state")
			}
		})
	}
}

func TestToolHistoryPersistsBeforeDeliveryAndFailurePreventsIssuedCard(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "stored", true: "storage-failed"}[failure], func(t *testing.T) {
			r := newRecorder()
			cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) {
				t.Fatal("history must not execute tool")
				return "", nil
			})
			var mu sync.Mutex
			records := map[string]ToolHistoryRecord{}
			attempted := make(chan struct{})
			cfg.PersistToolHistory = func(_ context.Context, _ *ihtml.ChatSessionContext, record ToolHistoryRecord) error {
				if failure {
					close(attempted)
					return errors.New("store unavailable")
				}
				mu.Lock()
				records[record.ID] = record
				mu.Unlock()
				record.Call.Body.(map[string]any)["record"] = "cannot mutate signed call"
				return nil
			}
			b, err := NewChecked(cfg)
			if err != nil {
				t.Fatal(err)
			}
			s, err := b.NewSession(context.Background(), &ihtml.ChatSessionContext{User: "owner", Emit: func(ev ihtml.ChatStreamEvent) error {
				if ev.Type == ihtml.ChatStreamConfirmationRequired {
					mu.Lock()
					_, ok := records[ev.ToolCall.ID]
					mu.Unlock()
					if !ok {
						t.Error("card emitted before durable history")
					}
					if ev.ToolCall.Body.(map[string]any)["record"] != "public" {
						t.Error("callback mutated signed call")
					}
				}
				return r.emit(ev)
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if !failure {
				proposedCard(t, s, r)
				return
			}
			send(s, "propose")
			select {
			case <-attempted:
			case <-time.After(time.Second):
				t.Fatal("persistence callback not reached")
			}
			s.Close()
			for _, ev := range r.snapshot() {
				if ev.Type == ihtml.ChatStreamConfirmationRequired {
					t.Fatal("unsaved card delivered")
				}
			}
		})
	}
}

// 浏览器断开或请求取消都不能吞掉已产生的终态，持久化使用独立且有期限的清理上下文。
func TestToolHistoryTerminalRecordSurvivesCancelledTransport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	s := &session{ctx: ctx, cancel: cancel, backend: &Backend{cfg: Config{PersistToolHistory: func(ctx context.Context, _ *ihtml.ChatSessionContext, record ToolHistoryRecord) error {
		if ctx.Err() != nil {
			t.Fatal("result persistence inherited cancelled transport")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("cleanup has no deadline")
		}
		if record.Status != ihtml.ChatToolUnknown {
			t.Fatal(record)
		}
		calls++
		return nil
	}}}, sctx: ihtml.ChatSessionContext{Emit: func(ihtml.ChatStreamEvent) error { t.Fatal("cancelled transport emitted"); return nil }}}
	event := ihtml.ChatStreamEvent{Type: ihtml.ChatStreamToolResult, ToolID: "original", ToolStatus: ihtml.ChatToolUnknown}
	if !errors.Is(s.emit(event), context.Canceled) {
		t.Fatal("cancelled emit succeeded")
	}
	p := newProjection(s, ctx, cancel)
	p.emitFailed = true
	p.send(event)
	if calls != 2 {
		t.Fatal("terminal record lost after delivery stopped", calls)
	}
}

func TestToolHistoryIgnoresRejectedCancellationAndOmitsMissingTime(t *testing.T) {
	if _, ok := ToolHistoryEvent(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamCancelToolResult, ToolID: "unverified-client-reference", ToolStatus: "rejected"}); ok {
		t.Fatal("rejected interaction became execution evidence")
	}
	record := ToolHistoryRecord{ID: "legacy", ReceiptOnly: true}
	raw, err := json.Marshal(record)
	if err != nil || strings.Contains(string(raw), "updatedAt") || strings.Contains(string(raw), "0001-") {
		t.Fatal("missing timestamp was invented", string(raw), err)
	}
}
