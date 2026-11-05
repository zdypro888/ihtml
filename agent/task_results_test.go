package agent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 大结果只改变传输形式；执行状态、完整正文、权限与字节游标在运行时重建后仍一致。
func TestTaskLargeResultUsesDurableReferenceAndUTF8Pages(t *testing.T) {
	store := NewMemoryTaskStore()
	results := NewMemoryTaskResultStore()
	r := taskRuntimeFixture(t, TaskConfig{Store: store, Results: results, MaxResultBytes: 8})
	body := strings.Repeat("回执ab🙂", 40)
	var calls atomic.Int32
	tool := Tool{Name: "arbitrary_write", Effect: ToolEffectWrite, Handler: func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return body, nil }}
	response, err := r.Call(t.Context(), "owner", "turn", "key", tool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var reference struct {
		Status, TaskID string
		Result         *TaskResultRef
	}
	if json.Unmarshal([]byte(response), &reference) != nil {
		t.Fatal(response)
	}
	// JSON task_id 不是 Go 默认字段名，按冻结调用键读取原账本身份。
	record, err := store.FindTask(t.Context(), "owner", taskHash([]string{"owner", "turn", "key"}))
	if err != nil || record.Info.State != "completed" || record.Info.Result != "" || record.Info.ResultRef == nil || calls.Load() != 1 {
		t.Fatal(record, err, calls.Load())
	}
	if reference.Status != "result_reference" {
		t.Fatal(response)
	}
	r.Close()
	restored := taskRuntimeFixture(t, TaskConfig{Store: store, Results: results, MaxResultBytes: 8})
	var joined strings.Builder
	var offset int64
	for {
		page, err := restored.ReadResult(t.Context(), "owner", record.ID, offset, 7, map[string]bool{tool.Name: true})
		if err != nil || page.Offset != offset || page.NextOffset <= offset {
			t.Fatal(page, err)
		}
		joined.WriteString(page.Text)
		offset = page.NextOffset
		if page.EOF {
			break
		}
	}
	if joined.String() != body {
		t.Fatal("result pages lost or duplicated bytes")
	}
	sum := sha256.Sum256([]byte(body))
	if record.Info.ResultRef.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal(record.Info.ResultRef)
	}
	if _, err = restored.ReadResult(t.Context(), "owner", record.ID, 1, 7, map[string]bool{tool.Name: true}); !errors.Is(err, ErrTaskResultRange) {
		t.Fatal("accepted offset inside UTF-8 character", err)
	}
	if _, err = restored.ReadResult(t.Context(), "foreign", record.ID, 0, 7, map[string]bool{tool.Name: true}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatal("scope leaked", err)
	}
	if _, err = restored.ReadResult(t.Context(), "owner", record.ID, 0, 7, map[string]bool{}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatal("tool permission leaked", err)
	}
	if _, err = restored.Call(t.Context(), "owner", "turn", "key", tool, json.RawMessage(`{}`)); err != nil || calls.Load() != 1 {
		t.Fatal(err, calls.Load())
	}
}

type retryResultStore struct {
	TaskResultStore
	failures atomic.Int32
}

func (s *retryResultStore) PutResult(ctx context.Context, record TaskResultRecord) (TaskResultRecord, error) {
	if s.failures.Add(-1) >= 0 {
		return TaskResultRecord{}, errors.New("temporary result storage outage")
	}
	return s.TaskResultStore.PutResult(ctx, record)
}

type retryFinalLedger struct {
	TaskStore
	failures atomic.Int32
}

func (s *retryFinalLedger) ReplaceTask(ctx context.Context, record TaskRecord, revision int64) (bool, error) {
	if record.Info.ResultRef != nil && s.failures.Add(-1) >= 0 {
		return false, errors.New("temporary ledger outage")
	}
	return s.TaskStore.ReplaceTask(ctx, record, revision)
}

// 临时存储故障只重试结果提交与补账，不能把已返回的写操作重新执行。
func TestTaskResultFinalizationRetriesWithoutHandlerReplay(t *testing.T) {
	store := &retryFinalLedger{TaskStore: NewMemoryTaskStore()}
	store.failures.Store(2)
	results := &retryResultStore{TaskResultStore: NewMemoryTaskResultStore()}
	results.failures.Store(2)
	r := taskRuntimeFixture(t, TaskConfig{Store: store, Results: results, ResultSaveTimeout: time.Second})
	var calls atomic.Int32
	tool := Tool{Name: "write", Effect: ToolEffectWrite, Handler: func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return "actual receipt", nil }}
	actual, err := r.Call(t.Context(), "owner", "turn", "key", tool, json.RawMessage(`{}`))
	if err != nil || actual != "actual receipt" || calls.Load() != 1 {
		t.Fatal(actual, err, calls.Load())
	}
}

// 模拟正文和事实已提交、账本仍为崩溃前running；下一实例以原回执补账，不推断unknown或重放。
func TestTaskCommittedResultRepairsLedgerAfterCrash(t *testing.T) {
	store := NewMemoryTaskStore()
	results := NewMemoryTaskResultStore()
	args := json.RawMessage(`{}`)
	digest := taskHash([]any{"write", args})
	record := TaskRecord{ID: "committed", Scope: "owner", Key: taskHash([]string{"owner", "turn", "key"}), Digest: digest, Turn: "turn", Started: true, Effect: ToolEffectWrite, Info: TaskInfo{ID: "committed", Tool: "write", State: "running"}, Revision: 1, LeaseUntil: time.Now().Add(-time.Second)}
	if _, _, err := store.CreateTask(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	r := taskRuntimeFixture(t, TaskConfig{Store: store, Results: results})
	outcome := r.makeResult(&toolTask{scope: "owner", id: record.ID}, Tool{Effect: ToolEffectWrite}, "durable receipt", nil, true, false, digest)
	if _, err := results.PutResult(t.Context(), outcome); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	row, err := r.Start(t.Context(), "owner", "turn", "key", Tool{Name: "write", Effect: ToolEffectWrite, Handler: func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return "replayed", nil }}, args)
	if err != nil || row.State != "completed" || row.Result != "durable receipt" || calls.Load() != 0 {
		t.Fatal(row, err, calls.Load())
	}
}

// 完整正文的容量上限不能抹掉执行事实；二进制结果的片段编码也必须明确。
func TestTaskResultStorageLimitAndBinaryEncoding(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", 17), string([]byte{0xff, 0x01, 0xfe})} {
		r := taskRuntimeFixture(t, TaskConfig{MaxResultBytes: 2, MaxStoredResultBytes: 16})
		tool := Tool{Name: "write", Effect: ToolEffectWrite, Handler: func(context.Context, json.RawMessage) (string, error) { return body, nil }}
		row, err := r.Start(t.Context(), "owner", "turn", "key", tool, json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		rows, err := r.Wait(t.Context(), "owner", []string{row.ID}, map[string]bool{"write": true}, time.Second, true)
		if err != nil || rows[0].State != "completed" || rows[0].ResultRef == nil {
			t.Fatal(rows, err)
		}
		page, err := r.ReadResult(t.Context(), "owner", row.ID, 0, 16, map[string]bool{"write": true})
		if len(body) > 16 {
			if !errors.Is(err, ErrTaskResultUnavailable) || rows[0].ResultRef.Availability != "size_exceeded" {
				t.Fatal(page, rows, err)
			}
		} else {
			actual, e := base64.StdEncoding.DecodeString(page.Data)
			if err != nil || e != nil || string(actual) != body || page.Result.Encoding != "base64" {
				t.Fatal(page, err, e)
			}
		}
	}
}

// 正文过期只影响读取，不抹掉已提交的执行事实，也不允许按同一逻辑调用重新执行。
func TestTaskResultExpiredBodyRetainsCommittedOutcome(t *testing.T) {
	store := NewMemoryTaskStore()
	results := NewMemoryTaskResultStore()
	args := json.RawMessage(`{}`)
	digest := taskHash([]any{"write", args})
	record := TaskRecord{ID: "expired-body", Scope: "owner", Key: taskHash([]string{"owner", "turn", "key"}), Digest: digest, Turn: "turn", Started: true, Effect: ToolEffectWrite, Info: TaskInfo{ID: "expired-body", Tool: "write", State: "running"}, Revision: 1, LeaseUntil: time.Now().Add(-time.Second)}
	if _, _, err := store.CreateTask(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	r := taskRuntimeFixture(t, TaskConfig{Store: store, Results: results})
	outcome := r.makeResult(&toolTask{scope: "owner", id: record.ID}, Tool{Effect: ToolEffectWrite}, "once", nil, true, false, digest)
	outcome.CreatedAt = time.Now().Add(-time.Hour)
	outcome.ExpiresAt = time.Now().Add(-time.Minute)
	if _, err := results.PutResult(t.Context(), outcome); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	tool := Tool{Name: "write", Effect: ToolEffectWrite, Handler: func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return "replayed", nil }}
	response, err := r.Call(t.Context(), "owner", "turn", "key", tool, args)
	if err != nil || !strings.Contains(response, "result_expired") || calls.Load() != 0 {
		t.Fatal(response, err, calls.Load())
	}
	if _, err = r.ReadResult(t.Context(), "owner", record.ID, 0, 4, map[string]bool{"write": true}); !errors.Is(err, ErrTaskResultExpired) {
		t.Fatal(err)
	}
	if err = r.Collect(t.Context()); err != nil {
		t.Fatal(err)
	}
	meta, err := results.GetResult(t.Context(), "owner", record.ID)
	if err != nil || meta.State != "completed" {
		t.Fatal(meta, err)
	}
}

// 二进制即使很小也必须给引用，不能经 JSON 字符串替换无效 UTF-8 而损坏原回执。
func TestSmallBinaryResultCallReturnsReference(t *testing.T) {
	r := taskRuntimeFixture(t, TaskConfig{})
	result, err := r.Call(t.Context(), "scope", "turn", "binary", Tool{Name: "binary", Effect: ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) { return string([]byte{0xff, 0x00}), nil }}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var reference struct {
		Status string        `json:"status"`
		Result TaskResultRef `json:"result"`
	}
	if json.Unmarshal([]byte(result), &reference) != nil || reference.Status != "result_reference" || reference.Result.Encoding != "base64" {
		t.Fatal("binary result was inlined", result)
	}
}

type taskPublishedButUnacknowledgedStore struct {
	TaskStore
	published chan struct{}
	release   chan struct{}
}

func (s *taskPublishedButUnacknowledgedStore) ReplaceTask(ctx context.Context, row TaskRecord, revision int64) (bool, error) {
	ok, err := s.TaskStore.ReplaceTask(ctx, row, revision)
	if ok && err == nil && row.Info.State == "completed" {
		close(s.published)
		select {
		case <-s.release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return ok, err
}

// 已持久结果不应让短等被执行器迟到的存储应答无限延长；超时只结束观察，不重放调用。
func TestTaskShortWaitBoundsFinalizationJoin(t *testing.T) {
	store := &taskPublishedButUnacknowledgedStore{TaskStore: NewMemoryTaskStore(), published: make(chan struct{}), release: make(chan struct{})}
	defer close(store.release)
	r := taskRuntimeFixture(t, TaskConfig{Store: store})
	row, err := r.Start(t.Context(), "scope", "turn", "call", Tool{Name: "read", Effect: ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) { return "actual", nil }}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	<-store.published
	observed := make(chan error, 1)
	go func() {
		_, err := r.Wait(t.Context(), "scope", []string{row.ID}, map[string]bool{"read": true}, time.Millisecond, true)
		observed <- err
	}()
	select {
	case err := <-observed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("short Wait ignored its deadline while joining local finalization")
	}
}
