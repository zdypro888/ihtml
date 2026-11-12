package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

func TestPublicToolResultTitleFallsBackToDeclaredMetadata(t *testing.T) {
	for _, variant := range []string{"missing", "technical", "panic", "explicit"} {
		t.Run(variant, func(t *testing.T) {
			cfg := configFor(&fakeEngine{})
			cfg.PresentToolResult = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage, string) ToolResultView {
				switch variant {
				case "technical":
					return ToolResultView{Title: "custom_command"}
				case "explicit":
					return ToolResultView{Title: "已核对的特定范围"}
				case "panic":
					panic("PRIVATE")
				default:
					return ToolResultView{}
				}
			}
			s := openSession(t, cfg, "owner", newRecorder()).(*session)
			view := s.publicToolResult(t.Context(), agent.Tool{Name: "custom_command", Title: "核对执行结果"}, nil, "PRIVATE")
			want := "核对执行结果"
			if variant == "explicit" {
				want = "已核对的特定范围"
			}
			if view.Title != want || view.Result != nil {
				t.Fatal(view)
			}
		})
	}
}

func TestRestoreToolHistoryMetadataPreservesExecutionFacts(t *testing.T) {
	for _, previousTitle := range []string{"", "task_read", "特定调用的公开名称"} {
		record := ToolHistoryRecord{ID: "original", Call: ihtml.ProposedToolCall{ID: "original", Name: "task_read", Title: previousTitle, Body: map[string]any{"fixed": "range"}}, Status: ihtml.ChatToolUnknown, Summary: "结果未知", Result: json.RawMessage(`{"execution_state":"unknown"}`)}
		updated := RestoreToolHistoryMetadata(record, agent.ToolMetadata{Name: "task_read", Title: "读取后台任务状态"})
		if previousTitle == "特定调用的公开名称" {
			if !reflect.DeepEqual(record, updated) {
				t.Fatal("explicit title changed", updated)
			}
			continue
		}
		if updated.Call.Title != "读取后台任务状态" {
			t.Fatal(updated)
		}
		updated.Call.Title = previousTitle
		if !reflect.DeepEqual(record, updated) {
			t.Fatal("restoring display metadata changed execution facts", updated)
		}
		if got := RestoreToolHistoryMetadata(record, agent.ToolMetadata{Name: "other", Title: "错误范围"}); !reflect.DeepEqual(record, got) {
			t.Fatal("metadata for another capability was accepted", got)
		}
	}
}
