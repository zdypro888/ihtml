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

// 从真实执行保存追踪，敏感输入/响应/原调用身份不会进入观测目录。
func TestTaskObservationSurvivesRuntimeRecreationAndContainsOnlyMetadata(t *testing.T) {
	store := NewMemoryTaskStore()
	runtime := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "scope"})
	var calls atomic.Int32
	row, err := runtime.Start(WithToolInvocationKey(t.Context(), "secret-invocation"), "scope", "secret-turn", "key", Tool{Name: "write", Effect: ToolEffectWrite, Handler: func(context.Context, json.RawMessage) (string, error) {
		calls.Add(1)
		return "secret-response", errors.New("secret-error")
	}}, json.RawMessage(`{"token":"secret-argument"}`))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := runtime.Wait(t.Context(), "scope", []string{row.ID}, map[string]bool{"write": true}, time.Second, true)
	if err != nil || rows[0].Failure == nil || rows[0].Failure.ExecutionState != "unknown" || !rows[0].Failure.Admitted {
		t.Fatal(rows, err)
	}
	runtime.Close()
	next := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "scope"})
	page, err := next.Observations(t.Context(), "scope", TaskObservationQuery{TaskID: row.ID, Limit: 20})
	if err != nil || len(page.Records) != 3 || page.Next != "" {
		t.Fatal(page, err)
	}
	encoded, _ := json.Marshal(page)
	for _, secret := range []string{"secret-invocation", "secret-turn", "secret-response", "secret-error", "secret-argument"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("observation leaked private data", string(encoded))
		}
	}
	if page.Records[0].TraceID != rows[0].TraceID || page.Records[0].TaskID != row.ID || calls.Load() != 1 {
		t.Fatal(page, rows, calls.Load())
	}
	if _, err = next.Observations(t.Context(), "foreign", TaskObservationQuery{}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatal("scope bypass", err)
	}
}

// 计数是有界快照，不把一页或者重复上报伪装成全量业务结果。
func TestTaskObservationDedupAndBoundedSummary(t *testing.T) {
	store := NewMemoryTaskObservationStore()
	at := time.Now().UTC()
	for i := range 4 {
		row, err := PrepareTaskObservation("scope", TaskObservation{Kind: "test_state", TaskID: string(rune('a' + i)), State: "queued", Revision: 1, At: at.Add(time.Duration(i))}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		created, err := store.AppendObservation(t.Context(), row)
		if err != nil || !created {
			t.Fatal(created, err)
		}
		if created, err = store.AppendObservation(t.Context(), row); err != nil || created {
			t.Fatal("duplicate counted", created, err)
		}
		row.Code = "changed"
		if _, err = store.AppendObservation(t.Context(), row); !errors.Is(err, ErrTaskConflict) {
			t.Fatal("sameID overwritten", err)
		}
	}
	runtime := taskRuntimeFixture(t, TaskConfig{Observations: store, Scope: "scope"})
	summary, err := runtime.ObservationStats(t.Context(), "scope", TaskObservationQuery{Limit: 2})
	if err != nil || summary.Count != 2 || !summary.Truncated || summary.Next == "" || summary.Counts["test_state/queued/"] != 2 {
		t.Fatal(summary, err)
	}
	page, err := runtime.Observations(t.Context(), "scope", TaskObservationQuery{After: summary.Next, Limit: 3})
	if err != nil || len(page.Records) != 2 {
		t.Fatal(page, err)
	}
}

type blockedObservationStore struct {
	TaskObservationStore
	entered chan struct{}
	release chan struct{}
	once    atomic.Bool
}

func (s *blockedObservationStore) AppendObservation(context.Context, TaskObservation) (bool, error) {
	if s.once.CompareAndSwap(false, true) {
		close(s.entered)
	}
	<-s.release
	return false, errors.New("secret-observation-backend")
}

// 即使非协作sink阻塞，受管业务和Close也有上界；观测损失与真实执行分别报告。
func TestTaskObservationBackpressureDoesNotBlockExecutionOrShutdown(t *testing.T) {
	sink := &blockedObservationStore{TaskObservationStore: NewMemoryTaskObservationStore(), entered: make(chan struct{}), release: make(chan struct{})}
	runtime := taskRuntimeFixture(t, TaskConfig{Observations: sink, Scope: "scope", ObservationTimeout: 5 * time.Millisecond})
	if err := runtime.RecordObservation(t.Context(), "scope", TaskObservation{Kind: "probe"}); err != nil {
		t.Fatal(err)
	}
	<-sink.entered
	for range 160 {
		_ = runtime.RecordObservation(t.Context(), "scope", TaskObservation{Kind: "probe"})
	}
	result, err := runtime.Call(t.Context(), "scope", "turn", "call", Tool{Name: "read", Effect: ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) { return "actual", nil }}, json.RawMessage(`{}`))
	if err != nil || result != "actual" {
		t.Fatal(result, err)
	}
	deadline := time.Now()
	runtime.Close()
	if time.Since(deadline) > time.Second {
		t.Fatal("Close blocked on telemetry")
	}
	if runtime.observer.dropped.Load() == 0 {
		t.Fatal("queue losses hidden")
	}
	close(sink.release)
	select {
	case <-runtime.observer.done:
	case <-time.After(time.Second):
		t.Fatal("observer did not retire after sink returned")
	}
	if runtime.observer.failed.Load() == 0 {
		t.Fatal("sink failure hidden")
	}
}

// 父子调用和后续恢复共享追踪身份，但每个调用/等待仍保留自己的授权与停止校验。
func TestTaskTraceLinksNestedCallsAndWaitResume(t *testing.T) {
	r := taskRuntimeFixture(t, TaskConfig{Scope: "scope"})
	childID := make(chan string, 1)
	parent, err := r.Start(t.Context(), "scope", "goal-turn", "parent", Tool{Name: "parent", Effect: ToolEffectRead, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
		child, err := r.Start(ctx, "scope", "nested-turn", "child", Tool{Name: "child", Effect: ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) { return "child", nil }}, json.RawMessage(`{}`))
		if err != nil {
			return "", err
		}
		childID <- child.ID
		_, err = r.Wait(ctx, "scope", []string{child.ID}, map[string]bool{"child": true}, time.Second, true)
		return "parent", err
	}}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	parents, err := r.Wait(t.Context(), "scope", []string{parent.ID}, map[string]bool{"parent": true}, time.Second, true)
	if err != nil || parents[0].State != "completed" {
		t.Fatal(parents, err)
	}
	child := <-childID
	children, err := r.Read("scope", []string{child}, map[string]bool{"child": true})
	if err != nil || children[0].ParentID != parent.ID || children[0].TraceID != parents[0].TraceID {
		t.Fatal(children, parents, err)
	}
	ctx := r.TraceContext(t.Context(), "scope", "goal-turn")
	wait, err := r.Suspend(ctx, "scope", "waiting-turn", "wait", TaskWaitSpec{IDs: []string{parent.ID}, Timeout: time.Minute, Checkpoint: "public work facts"}, map[string]bool{"parent": true})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := r.BindWaitContext(t.Context(), "scope", wait.ID)
	if err != nil || TaskTraceID(resumed) != parents[0].TraceID {
		t.Fatal("resume split trace", TaskTraceID(resumed), parents, err)
	}
	if TaskTraceID(r.TraceContext(resumed, "scope", "fresh-recovery-turn")) != parents[0].TraceID {
		t.Fatal("continuation replaced original trace")
	}
}
