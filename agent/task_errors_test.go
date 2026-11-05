package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 稳定码和执行阶段不能靠底层错误文本猜测，也不能把未知写入变成可安全重试。
func TestTaskFailureIsPublicAndPreservesExecutionFacts(t *testing.T) {
	failure := TaskFailureFrom(errors.New("https://secret-user:secret-token@backend"), "unknown", true, "receipt", nil)
	text, ok := TaskFailureJSON(failure, "not_started", false, "", nil)
	if !ok || strings.Contains(text, "secret") || !strings.Contains(text, `"admission_state":"admitted"`) || !strings.Contains(text, `"execution_state":"unknown"`) {
		t.Fatal(text)
	}
	var wire map[string]any
	if err := json.Unmarshal([]byte(text), &wire); err != nil || wire["status"] == "not_admitted" || wire["code"] != "execution_unknown" {
		t.Fatal(wire, err)
	}
	metadata := &TaskFailure{Code: "handler_failed", Message: "secret-host-response", ExecutionState: "unknown", Admitted: true}
	text, _ = TaskFailureJSON(metadata, "not_started", false, "", nil)
	if strings.Contains(text, "secret") {
		t.Fatal("typed private message bypassed public protocol", text)
	}
	cancelled := TaskFailureFrom(context.Canceled, "unknown", true, "id", nil)
	if !errors.Is(cancelled, context.Canceled) || cancelled.Code != "cancelled" || cancelled.ExecutionState != "unknown" {
		t.Fatal(cancelled)
	}
	readStorage := TaskFailureFrom(WrapTaskStorage(errors.New("secret-database-driver")), "not_started", false, "", nil)
	if readStorage.Code != "storage_unavailable" || readStorage.AdmissionState != "not_admitted" {
		t.Fatal(readStorage)
	}
}

type createTaskAckLostStore struct{ TaskStore }

func (s *createTaskAckLostStore) CreateTask(ctx context.Context, row TaskRecord) (TaskRecord, bool, error) {
	saved, created, err := s.TaskStore.CreateTask(ctx, row)
	if err != nil {
		return saved, created, err
	}
	return saved, created, errors.New("secret-ack-lost")
}

// 入库应答丢失时原记录可能已存在；公开错误必须给查询句柄与unknown，而非not_admitted。
func TestTaskCreationAckLossReportsUnknownAdmissionWithoutHandlerReplay(t *testing.T) {
	store := &createTaskAckLostStore{TaskStore: NewMemoryTaskStore()}
	r := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "scope"})
	var calls atomic.Int32
	_, err := r.Start(t.Context(), "scope", "turn", "key", Tool{Name: "read", Effect: ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return "", nil }}, json.RawMessage(`{}`))
	var failure *TaskFailure
	if !errors.As(err, &failure) || failure.Code != "storage_unavailable" || failure.AdmissionState != "unknown" || failure.ReceiptID == "" || failure.ExecutionState != "not_started" {
		t.Fatal(failure, err)
	}
	text, _ := TaskFailureJSON(err, "not_started", false, "", nil)
	if strings.Contains(text, "secret") || strings.Contains(text, `"status":"not_admitted"`) {
		t.Fatal(text)
	}
	row, err := store.GetTask(t.Context(), "scope", failure.ReceiptID)
	if err != nil || row.Info.State != "queued" || calls.Load() != 0 {
		t.Fatal(row, err, calls.Load())
	}
	// 仅模拟原租约失效；恢复状态不调用旧Handler。
	revision := row.Revision
	row.LeaseUntil = time.Now().Add(-time.Second)
	row.Revision++
	store.ReplaceTask(t.Context(), row, revision)
	records, err := r.Read("scope", []string{row.ID}, map[string]bool{"read": true})
	if err != nil || records[0].State != "cancelled" || calls.Load() != 0 {
		t.Fatal(records, err, calls.Load())
	}
}

// 外层已经写入时，下游资源拒绝只解释失败原因，不能反证外层从未执行。
func TestOuterExecutionFactsDominateNestedAdmissionFailure(t *testing.T) {
	for _, nested := range []error{&TaskBudgetError{Kind: "task", RetryAfterMS: 100}, TaskFailureFrom(&TaskBudgetError{Kind: "task"}, "not_started", false, "inner", nil)} {
		outer := TaskFailureFrom(nested, "unknown", true, "outer", nil)
		if !outer.Admitted || outer.AdmissionState != "admitted" || outer.ExecutionState != "unknown" || outer.Code != "budget_exhausted" || outer.ReceiptID != "outer" || strings.Contains(outer.Message, "尚未受理") {
			t.Fatal(outer)
		}
		wire, _ := TaskFailureJSON(outer, "not_started", false, "", nil)
		if strings.Contains(wire, `"status":"not_admitted"`) || strings.Contains(wire, "尚未受理") {
			t.Fatal(wire)
		}
	}
}
