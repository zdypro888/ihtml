package agent

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
)

func TestDurableAssemblyRejectsMissingAuthority(t *testing.T) {
	if _, err := NewTaskRuntime(t.Context(), TaskConfig{StorageMode: TaskStorageDurable, Store: NewMemoryTaskStore()}); !errors.Is(err, ErrTaskStorageConfiguration) {
		t.Fatal(err)
	}
	if _, err := CheckTaskReadiness(t.Context(), TaskConfig{StorageMode: "unknown"}); !errors.Is(err, ErrTaskStorageConfiguration) {
		t.Fatal(err)
	}
	if TaskProtocolVersion(0) != nil || TaskProtocolVersion(1) != nil || !errors.Is(TaskProtocolVersion(2), ErrTaskStorageVersion) {
		t.Fatal("version policy")
	}
}

// 输出校验失败不能把已发生调用变成未执行，也不能为修复 schema 重执行业务。
func TestOutputContractPreservesOriginalExecution(t *testing.T) {
	r := taskRuntimeFixture(t, TaskConfig{})
	var calls atomic.Int32
	tool := Tool{Name: "any_write", Effect: ToolEffectWrite, Contract: ToolContract{OutputSchema: map[string]any{"type": "object", "required": []string{"ok"}}}, Handler: func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return `{"other":1}`, nil }}
	one, err := r.Start(t.Context(), "scope", "turn", "same", tool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	row, err := r.AwaitResult(t.Context(), "scope", one.ID, map[string]bool{tool.Name: true})
	if err != nil || row.State != "completed" || row.Failure == nil || row.Failure.Code != "contract_violation" {
		t.Fatal(row, err)
	}
	again, err := r.Start(t.Context(), "scope", "turn", "same", tool, json.RawMessage(`{}`))
	if err != nil || again.ID != one.ID || calls.Load() != 1 {
		t.Fatal(again, err, calls.Load())
	}
	body, err := r.ResolveResultInput(t.Context(), "scope", TaskResultInput{ID: row.ID, SHA256: row.ResultRef.SHA256}, map[string]bool{tool.Name: true})
	if err != nil || string(body) != `{"other":1}` {
		t.Fatal(string(body), err)
	}
	tool.Contract.OutputSchema = map[string]any{"$ref": "https://invalid.example/private"}
	if _, err = r.Start(t.Context(), "scope", "turn", "invalid-schema", tool, json.RawMessage(`{}`)); !errors.Is(err, ErrTaskContract) || calls.Load() != 1 {
		t.Fatal(err, calls.Load())
	}
}

func TestOutputSchemaKeepsExactJSONNumbers(t *testing.T) {
	schema, err := CompileToolContract(Tool{Contract: ToolContract{OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}}, "required": []string{"id"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = validateToolOutput(schema, `{"id":9007199254740993}`); err != nil {
		t.Fatal(err)
	}
	if err = validateToolOutput(schema, `{"id":"wrong"}`); !errors.Is(err, ErrTaskContract) {
		t.Fatal(err)
	}
}

func TestShutdownDeadlineAndCallbackIsolation(t *testing.T) {
	started, release, callback, unblock := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(unblock)
	r := taskRuntimeFixture(t, TaskConfig{OnComplete: func(TaskInfo) { close(callback); <-unblock }})
	tool := Tool{Name: "read", Effect: ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) {
		close(started)
		<-release
		return "original", nil
	}}
	_, err := r.Start(t.Context(), "scope", "turn", "one", tool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err = r.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err = r.Start(t.Context(), "scope", "turn", "two", tool, json.RawMessage(`{}`)); err == nil {
		t.Fatal("accepted after shutdown")
	}
	close(release)
	if err = r.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	// 新运行时的 callback 阻塞不影响下一项、Drain 或 Shutdown。
	r2 := taskRuntimeFixture(t, TaskConfig{OnComplete: func(TaskInfo) {
		select {
		case <-callback:
		default:
			close(callback)
		}
		<-unblock
	}})
	tool.Handler = func(context.Context, json.RawMessage) (string, error) { return "ok", nil }
	for _, key := range []string{"a", "b"} {
		row, e := r2.Start(t.Context(), "scope", "turn", key, tool, json.RawMessage(`{}`))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = r2.AwaitResult(t.Context(), "scope", row.ID, map[string]bool{"read": true}); e != nil {
			t.Fatal(e)
		}
		if key == "a" {
			<-callback
		}
	}
	if err = r2.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r2.callbackDropped.Load() != 1 {
		t.Fatal("callback queue unbounded or blocked", r2.callbackDropped.Load())
	}
}

func TestReadOnlyReconciliationAddsEvidenceWithoutReplaying(t *testing.T) {
	store := NewMemoryTaskStore()
	r := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "scope"})
	now := time.Now()
	original := TaskRecord{ID: "lost", Scope: "scope", Key: "original", Digest: "frozen", Invocation: "trusted-invocation", Args: []byte(`{"id":"target"}`), Started: true, Effect: ToolEffectWrite, Revision: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour), Info: TaskInfo{ID: "lost", Tool: "write", State: "unknown", ContractVersion: "v1"}}
	if _, _, err := store.CreateTask(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	var calls, probes atomic.Int32
	tool := Tool{Name: "write", Effect: ToolEffectWrite, Contract: ToolContract{Version: "v1"}, Handler: func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return "", nil }, RecoverResult: func(ctx context.Context, args json.RawMessage) (string, bool, error) {
		probes.Add(1)
		if ToolInvocationKey(ctx) != "trusted-invocation" || string(args) != string(original.Args) {
			t.Error("untrusted recovery identity")
		}
		return `{"state":"accepted"}`, true, nil
	}}
	if _, err := r.Reconcile(t.Context(), "scope", "lost", nil); !errors.Is(err, ErrTaskNotFound) {
		t.Fatal(err)
	}
	wrong := tool
	wrong.Contract.Version = "v2"
	if _, err := r.Reconcile(t.Context(), "scope", "lost", []Tool{wrong}); !errors.Is(err, ErrTaskContract) {
		t.Fatal(err)
	}
	row, err := r.Reconcile(t.Context(), "scope", "lost", []Tool{tool})
	if err != nil || row.State != "unknown" || row.ResolutionRef == nil || row.ResultRef != nil || calls.Load() != 0 {
		t.Fatal(row, err, calls.Load())
	}
	body, err := r.ResolveResultInput(t.Context(), "scope", TaskResultInput{ID: row.ResolutionRef.ID, SHA256: row.ResolutionRef.SHA256}, map[string]bool{"write": true})
	if err != nil || !strings.Contains(string(body), `accepted`) {
		t.Fatal(string(body), err)
	}
	if _, err = r.Reconcile(t.Context(), "scope", "lost", []Tool{tool}); err != nil || probes.Load() != 1 {
		t.Fatal(err, probes.Load())
	}
}

type countedResults struct {
	TaskResultStore
	bytes   atomic.Int64
	corrupt bool
}

func (s *countedResults) ReadResult(ctx context.Context, scope, id string, off int64, n int) ([]byte, error) {
	b, err := s.TaskResultStore.ReadResult(ctx, scope, id, off, n)
	s.bytes.Add(int64(len(b)))
	if s.corrupt && len(b) > 0 {
		b = append([]byte(nil), b...)
		b[0] ^= 1
	}
	return b, err
}

func TestStreamedResultsAndRestrictedReferences(t *testing.T) {
	results := &countedResults{TaskResultStore: NewMemoryTaskResultStore()}
	r := taskRuntimeFixture(t, TaskConfig{Results: results})
	body := `{"ignored":[` + strings.Repeat(`"data",`, 50000) + `null],"selected":{"id":9007199254740993}}`
	tool := Tool{Name: "source", Effect: ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) { return body, nil }}
	row, err := r.Start(t.Context(), "scope", "turn", "key", tool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	row, err = r.AwaitResult(t.Context(), "scope", row.ID, map[string]bool{"source": true})
	if err != nil {
		t.Fatal(err)
	}
	ref := TaskResultInput{ID: row.ID, SHA256: row.ResultRef.SHA256, Pointer: "/selected"}
	b, err := r.ResolveResultInput(t.Context(), "scope", ref, map[string]bool{"source": true})
	if err != nil || string(b) != `{"id":9007199254740993}` {
		t.Fatal(string(b), err)
	}
	ctx := r.ResultInputContext(t.Context(), "scope", []Tool{tool})
	raw, _ := json.Marshal(map[string]any{"body": map[string]any{}, "result_inputs": map[string]any{"/body/value": ref}})
	b, err = ResolveToolInputReferences(ctx, raw, []string{"/body"}, 65536)
	if err != nil || !bytes.Contains(b, []byte("9007199254740993")) {
		t.Fatal(string(b), err)
	}
	for _, refs := range []map[string]TaskResultInput{{"/path/id": ref}, {"/body": ref, "/body/id": ref}} {
		raw, _ = json.Marshal(map[string]any{"body": map[string]any{}, "result_inputs": refs})
		if _, err = ResolveToolInputReferences(ctx, raw, []string{"/body"}, 65536); !errors.Is(err, ErrTaskContract) {
			t.Fatal(err)
		}
	}
	results.bytes.Store(0)
	ref.Pointer = ""
	reader, err := r.OpenResultInput(t.Context(), "scope", ref, map[string]bool{"source": true})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 32)
	if _, err = io.ReadFull(reader, buf); err != nil {
		t.Fatal(err)
	}
	reader.Close()
	if results.bytes.Load() != 32 {
		t.Fatal("partial read loaded entire body", results.bytes.Load())
	}
	results.corrupt = true
	reader, err = r.OpenResultInput(t.Context(), "scope", ref, map[string]bool{"source": true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, reader)
	reader.Close()
	if !errors.Is(err, ErrTaskResultUnavailable) {
		t.Fatal("corruption accepted", err)
	}
	for _, invalid := range []string{`{"x":1} {}`, `{"x":[1,2`, `{"x":1,"tail":[}`} {
		if _, err = selectJSONReader(strings.NewReader(invalid), "/x"); err == nil {
			t.Fatal("malformed tail accepted", invalid)
		}
	}
}

func TestEventLifecycleAndSegmentAllocation(t *testing.T) {
	r := taskRuntimeFixture(t, TaskConfig{Scope: "scope", MaxEventRecords: 2})
	p := r.EventPublisher()
	src, err := p.OpenSource(t.Context(), "scope", "one", "source")
	if err != nil {
		t.Fatal(err)
	}
	event := TaskExternalEvent{Key: "key", Type: "test", Payload: json.RawMessage(`{"ok":true}`)}
	if _, err = p.Publish(t.Context(), "scope", src.ID, event); err != nil {
		t.Fatal(err)
	}
	src, _ = p.Store.GetEventSource(t.Context(), "scope", src.ID)
	closed, err := p.CloseSource(t.Context(), "scope", src.ID, src.Revision, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Publish(t.Context(), "scope", src.ID, event); err != nil {
		t.Fatal("dedup lost at close", err)
	}
	event.Key = "new"
	if _, err = p.Publish(t.Context(), "scope", src.ID, event); !errors.Is(err, ErrTaskEventClosed) {
		t.Fatal(err)
	}
	w, err := r.Suspend(t.Context(), "scope", "turn", "wait", TaskWaitSpec{Timeout: time.Hour, Checkpoint: "facts", Events: []TaskEventCondition{{SourceID: src.ID, After: 1}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	freezing, err := r.ArchiveEventSource(t.Context(), "scope", src.ID, closed.Revision)
	if err == nil {
		t.Fatal("archived active subscription")
	}
	wake, err := r.ClaimWake(t.Context(), "scope", "consumer")
	if err != nil || wake == nil || wake.ID != w.ID || wake.Reason != "event_source_closed" {
		t.Fatal(wake, err)
	}
	if err = r.UpdateWake(t.Context(), "scope", wake.ID, "consumer", "ack"); err != nil {
		t.Fatal(err)
	}
	archived, err := r.ArchiveEventSource(t.Context(), "scope", src.ID, freezing.Revision)
	if err != nil || archived.State != "archived" || len(archived.Records[0].Payload) != 0 {
		t.Fatal(archived, err)
	}
	control := NewMemoryTaskControlStore()
	var wg sync.WaitGroup
	var mu sync.Mutex
	counts := map[string]int{}
	for range 40 {
		wg.Go(func() {
			s, e := AllocateEventSegment(t.Context(), control, "namespace", "scope", "segments", "facts", p)
			if e != nil {
				t.Error(e)
				return
			}
			mu.Lock()
			counts[s.ID]++
			mu.Unlock()
		})
	}
	wg.Wait()
	if len(counts) != 20 {
		t.Fatal("segment over/under allocation", counts)
	}
	for _, n := range counts {
		if n != 2 {
			t.Fatal(counts)
		}
	}
}

func TestWebhookAuthenticationAndDurableDedup(t *testing.T) {
	p := TaskEventPublisher{Store: NewMemoryTaskEventStore()}
	src, err := p.OpenSource(t.Context(), "scope", "webhook", "facts")
	if err != nil {
		t.Fatal(err)
	}
	secret := bytes.Repeat([]byte("x"), 32)
	h := TaskWebhook{Publisher: p, Scope: "scope", SourceID: src.ID, EventType: "fact", Verify: VerifyTaskWebhookHMAC(secret, time.Minute)}
	post := func(body string, signed bool) int {
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "same")
		if signed {
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			req.Header.Set("X-Event-Timestamp", ts)
			mac := hmac.New(sha256.New, secret)
			fmt.Fprintf(mac, "POST\n/\n%s\nsame\n%s", ts, body)
			req.Header.Set("X-Event-Signature", hex.EncodeToString(mac.Sum(nil)))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if post(`{}`, false) != 401 || post(`{"id":1}`, true) != 202 || post(`{"id":1}`, true) != 202 || post(strings.Repeat("x", 8193), true) != 413 {
		t.Fatal("webhook admission")
	}
	if post(`{"id":2}`, true) == 202 {
		t.Fatal("same key changed fact")
	}
	source, _ := p.Store.GetEventSource(t.Context(), "scope", src.ID)
	if source.Head != 1 {
		t.Fatal(source.Head)
	}
	// 同一已签名正文不能换幂等键或入口路径，绕过 durable inbox 重放。
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "POST\n/\n%s\nsame\n%s", ts, `{"id":1}`)
	signature := hex.EncodeToString(mac.Sum(nil))
	for _, change := range []struct{ path, key string }{{"/", "changed"}, {"/another", "same"}} {
		req := httptest.NewRequest("POST", change.path, strings.NewReader(`{"id":1}`))
		req.Header.Set("Idempotency-Key", change.key)
		req.Header.Set("X-Event-Timestamp", ts)
		req.Header.Set("X-Event-Signature", signature)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatal("signed request was rebound", w.Code)
		}
	}
}

func TestWakeRestoresStandardTraceWithoutBaggage(t *testing.T) {
	r := taskRuntimeFixture(t, TaskConfig{Scope: "scope"})
	parent := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1, 2, 3}, SpanID: trace.SpanID{4, 5}, TraceFlags: trace.FlagsSampled})
	ctx := trace.ContextWithSpanContext(t.Context(), parent)
	w, err := r.Suspend(ctx, "scope", "turn", "wait", TaskWaitSpec{Timeout: time.Hour, Checkpoint: "facts"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := r.BindWaitContext(t.Context(), "scope", w.ID)
	if err != nil || trace.SpanContextFromContext(resumed).TraceID() != parent.TraceID() {
		t.Fatal(err)
	}
}

func BenchmarkJSONResultSelection(b *testing.B) {
	body := []byte(`{"ignored":[` + strings.Repeat(`{"value":1},`, 10000) + `null],"selected":9007199254740993}`)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := selectJSONReader(bytes.NewReader(body), "/selected"); err != nil {
			b.Fatal(err)
		}
	}
}

type blockedRegistration struct {
	TaskStore
	entered, release chan struct{}
}

func (s blockedRegistration) CreateWait(ctx context.Context, row TaskWaitRecord) (TaskWaitRecord, bool, error) {
	close(s.entered)
	<-s.release
	return s.TaskStore.CreateWait(ctx, row)
}

// 归档先冻结登记、再等待已有预留；等待写入尚未返回也不能跨越回收屏障。
func TestArchiveProtectsInFlightSubscription(t *testing.T) {
	store := blockedRegistration{NewMemoryTaskStore(), make(chan struct{}), make(chan struct{})}
	r := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "scope"})
	p := r.EventPublisher()
	src, err := p.OpenSource(t.Context(), "scope", "source", "facts")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Publish(t.Context(), "scope", src.ID, TaskExternalEvent{Key: "fact", Type: "fact", Payload: json.RawMessage(`{"id":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	src, _ = p.Store.GetEventSource(t.Context(), "scope", src.ID)
	src, err = p.CloseSource(t.Context(), "scope", src.ID, src.Revision, "")
	if err != nil {
		t.Fatal(err)
	}
	registered := make(chan error, 1)
	go func() {
		_, err := r.Suspend(t.Context(), "scope", "turn", "wait", TaskWaitSpec{Timeout: time.Hour, Checkpoint: "facts", Events: []TaskEventCondition{{SourceID: src.ID, After: 1}}}, nil)
		registered <- err
	}()
	<-store.entered
	frozen, err := r.ArchiveEventSource(t.Context(), "scope", src.ID, src.Revision)
	if err == nil || frozen.State != "archiving" || len(frozen.Records[0].Payload) == 0 {
		t.Fatal(frozen, err)
	}
	if _, err = r.Suspend(t.Context(), "scope", "turn", "new-wait", TaskWaitSpec{Timeout: time.Hour, Checkpoint: "facts", Events: []TaskEventCondition{{SourceID: src.ID, After: 1}}}, nil); !errors.Is(err, ErrTaskEventClosed) {
		t.Fatal("registration crossed archive gate", err)
	}
	close(store.release)
	if err = <-registered; err != nil {
		t.Fatal(err)
	}
	wake, err := r.ClaimWake(t.Context(), "scope", "consumer")
	if err != nil || wake == nil {
		t.Fatal(wake, err)
	}
	if err = r.UpdateWake(t.Context(), "scope", wake.ID, "consumer", "ack"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.ArchiveEventSource(t.Context(), "scope", src.ID, frozen.Revision); err != nil {
		t.Fatal(err)
	}
}

type blockedAdmission struct {
	TaskStore
	entered, release chan struct{}
}

func (s blockedAdmission) CreateTask(ctx context.Context, row TaskRecord) (TaskRecord, bool, error) {
	close(s.entered)
	<-s.release
	return s.TaskStore.CreateTask(ctx, row)
}

func TestDrainWaitsForAcceptedAdmissionWithoutCancelingIt(t *testing.T) {
	store := blockedAdmission{NewMemoryTaskStore(), make(chan struct{}), make(chan struct{})}
	r := taskRuntimeFixture(t, TaskConfig{Store: store})
	done := make(chan error, 1)
	var calls atomic.Int32
	go func() {
		_, err := r.Start(t.Context(), "scope", "turn", "key", Tool{Name: "read", Effect: ToolEffectRead, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) { calls.Add(1); return "result", ctx.Err() }}, json.RawMessage(`{}`))
		done <- err
	}()
	<-store.entered
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := r.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	close(store.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := r.Drain(t.Context()); err != nil || calls.Load() != 1 {
		t.Fatal("drain canceled admitted work", err, calls.Load())
	}
}
