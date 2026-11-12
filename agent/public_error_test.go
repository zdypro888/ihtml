package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestPublicErrorPreservesSafeCauseAndExecutionFacts(t *testing.T) {
	private := errors.New("PRIVATE provider credential response")
	err := NewPublicError("record_not_found", "找不到该记录，请核对原记录。", private)
	if !errors.Is(err, private) || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal(err)
	}
	for _, execution := range []string{"handler_returned", "unknown"} {
		failure := TaskFailureFrom(err, execution, true, "receipt", nil)
		if failure.Public == nil || failure.Public.Code != "record_not_found" || !strings.Contains(failure.Message, "找不到该记录") || failure.ExecutionState != execution {
			t.Fatal(failure)
		}
		// BSON/JSON 恢复会移除私有 cause，公开原因与未知执行事实仍保留。
		data, marshalErr := bson.Marshal(failure)
		if marshalErr != nil || strings.Contains(string(data), "PRIVATE") {
			t.Fatal(marshalErr, string(data))
		}
		var restored TaskFailure
		if bson.Unmarshal(data, &restored) != nil {
			t.Fatal("restore failed")
		}
		wire, _ := TaskFailureJSON(&restored, "not_started", false, "", nil)
		parsed, ok := ParseTaskFailureJSON(wire)
		if !ok || parsed.Message != failure.Message || parsed.Public.Code != "record_not_found" || parsed.ExecutionState != execution || strings.Contains(wire, "PRIVATE") {
			t.Fatal(wire, parsed)
		}
		if execution == "unknown" && !strings.Contains(parsed.Message, "不自动重放") {
			t.Fatal("public cause lost unknown outcome boundary", parsed)
		}
	}
	for _, value := range []*PublicErrorDetails{{"bad code", "text"}, {"valid", strings.Repeat("x", 2049)}, {"valid", "first\nPRIVATE"}} {
		failure := TaskFailureFrom(NewPublicError(value.Code, value.Message, private), "handler_returned", true, "", nil)
		if failure.Public != nil || strings.Contains(failure.Message, "PRIVATE") {
			t.Fatal(failure)
		}
	}
	parsed, ok := ParseTaskFailureJSON(`{"status":"operation_failed","code":"handler_failed","execution_state":"handler_returned","message":"PRIVATE"}`)
	if !ok || strings.Contains(parsed.Message, "PRIVATE") {
		t.Fatal(parsed)
	}
}

func TestPublicErrorSurvivesTaskRuntimeRecoveryWithoutReplay(t *testing.T) {
	for _, effect := range []string{ToolEffectRead, ToolEffectWrite} {
		t.Run(effect, func(t *testing.T) {
			store, results := NewMemoryTaskStore(), NewMemoryTaskResultStore()
			r := taskRuntimeFixture(t, TaskConfig{Store: store, Results: results})
			var calls atomic.Int32
			tool := Tool{Name: "operation", Effect: effect, Handler: func(context.Context, json.RawMessage) (string, error) {
				calls.Add(1)
				return "", NewPublicError("record_not_found", "找不到该记录，请核对原记录。", errors.New("PRIVATE"))
			}}
			_, err := r.Call(t.Context(), "owner", "turn", "key", tool, json.RawMessage(`{}`))
			var failure *TaskFailure
			if !errors.As(err, &failure) || failure.Public == nil || failure.Public.Code != "record_not_found" {
				t.Fatal(err)
			}
			if effect == ToolEffectWrite && failure.ExecutionState != "unknown" {
				t.Fatal("write error incorrectly claimed no effects", failure)
			}
			r.Close()
			restored := taskRuntimeFixture(t, TaskConfig{Store: store, Results: results})
			_, err = restored.Call(t.Context(), "owner", "turn", "key", tool, json.RawMessage(`{}`))
			if !errors.As(err, &failure) || failure.Public == nil || failure.Public.Code != "record_not_found" || calls.Load() != 1 {
				t.Fatal(err, calls.Load())
			}
			wire, _ := TaskFailureJSON(err, "not_started", false, "", nil)
			if strings.Contains(wire, "PRIVATE") || !strings.Contains(wire, "找不到该记录") {
				t.Fatal(wire)
			}
		})
	}
}
