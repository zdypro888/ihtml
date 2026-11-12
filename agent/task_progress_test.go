package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zdypro888/ihtml/progress"
)

type countingProgressStore struct {
	*MemoryTaskStore
	progressWrites atomic.Int64
}

func (s *countingProgressStore) ReplaceTask(ctx context.Context, row TaskRecord, revision int64) (bool, error) {
	old, _ := s.GetTask(ctx, row.Scope, row.ID)
	ok, err := s.MemoryTaskStore.ReplaceTask(ctx, row, revision)
	if ok && row.Info.Progress != nil && (old.Info.Progress == nil || row.Info.Progress.Message != old.Info.Progress.Message) {
		s.progressWrites.Add(1)
	}
	return ok, err
}

func TestTaskProgressPersistsBoundedLatestSnapshotAcrossReaders(t *testing.T) {
	store := &countingProgressStore{MemoryTaskStore: NewMemoryTaskStore()}
	r := taskRuntimeFixture(t, TaskConfig{Store: store})
	reader := taskRuntimeFixture(t, TaskConfig{Store: store})
	release := make(chan struct{})
	var closed atomic.Bool
	t.Cleanup(func() {
		if closed.CompareAndSwap(false, true) {
			close(release)
		}
	})
	reported := make(chan struct{})
	observed := make(chan progress.Snapshot, 1)
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	total := int64(1000)
	tool := Tool{Name: "arbitrary_long_tool", Effect: ToolEffectRead, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
		// 模拟宿主在原 Handler 外包裹自己的显示 reporter，不得覆盖运行时记录通道。
		ctx = WithProgressReporter(ctx, func(s progress.Snapshot) {
			select {
			case observed <- s:
			default:
			}
		})
		for i := int64(1); i <= 200; i++ {
			ReportProgress(ctx, progress.Snapshot{Message: "正在核对范围", State: "running", Counters: []progress.Counter{{Label: "已核对", Current: i, Total: &total}}, UpdatedAt: at.Add(time.Duration(i) * time.Millisecond)})
		}
		close(reported)
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		ReportProgress(ctx, progress.Snapshot{Message: "核对回执已返回", State: "running", Counters: []progress.Counter{{Label: "已核对", Current: 201, Total: &total}}, UpdatedAt: at.Add(201 * time.Millisecond)})
		return "receipt", nil
	}}
	info, err := r.Start(t.Context(), "scope", "turn", "call", tool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	<-reported
	select {
	case <-observed:
	default:
		t.Fatal("host reporter was replaced")
	}
	deadline := time.Now().Add(2 * time.Second)
	var rows []TaskInfo
	for {
		rows, err = reader.Read("scope", []string{info.ID}, map[string]bool{tool.Name: true})
		if err != nil {
			t.Fatal(err)
		}
		if rows[0].Progress != nil && rows[0].Progress.Counters[0].Current == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("last running progress was not persisted", rows)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rows[0].State != "running" || !rows[0].Progress.UpdatedAt.Equal(at.Add(200*time.Millisecond)) || store.progressWrites.Load() > 2 {
		t.Fatal("progress changed task state/time or caused unbounded writes", rows[0], store.progressWrites.Load())
	}
	rows[0].Progress.Counters[0].Current = 999
	*rows[0].Progress.Counters[0].Total = 9999
	closed.Store(true)
	close(release)
	rows, err = r.Wait(t.Context(), "scope", []string{info.ID}, map[string]bool{tool.Name: true}, time.Second, true)
	if err != nil || rows[0].State != "completed" || rows[0].Result != "receipt" || rows[0].Progress.Counters[0].Current != 201 || *rows[0].Progress.Counters[0].Total != 1000 || !rows[0].Progress.UpdatedAt.Equal(at.Add(201*time.Millisecond)) {
		t.Fatal("final flush or snapshot isolation failed", rows, err)
	}
	if store.progressWrites.Load() > 3 {
		t.Fatal("each progress update was written separately", store.progressWrites.Load())
	}
	rows, err = reader.Read("scope", []string{info.ID}, map[string]bool{tool.Name: true})
	if err != nil || rows[0].Progress.Message != "核对回执已返回" {
		t.Fatal(rows, err)
	}
	if _, err = reader.Read("other", []string{info.ID}, map[string]bool{tool.Name: true}); err == nil {
		t.Fatal("progress crossed task scope")
	}
}

func TestTaskProgressNeverOverridesTerminalOrForeignOwner(t *testing.T) {
	for _, state := range []string{"completed", "running"} {
		t.Run(state, func(t *testing.T) {
			r := taskRuntimeFixture(t, TaskConfig{})
			owner := r.owner
			if state == "running" {
				owner = "different-runner"
			}
			rec := TaskRecord{StorageVersion: TaskStorageVersion, ID: "original", Scope: "scope", Key: "call", Owner: owner, Revision: 1, LeaseUntil: time.Now().Add(time.Minute), Info: TaskInfo{ID: "original", Tool: "read", State: state}}
			if _, _, err := r.Store().CreateTask(t.Context(), rec); err != nil {
				t.Fatal(err)
			}
			task := &toolTask{scope: "scope", id: "original", progress: newTaskProgress()}
			task.progress.report(progress.Snapshot{Message: "late progress", UpdatedAt: time.Now()})
			r.flushTaskProgress(task)
			saved, err := r.Store().GetTask(t.Context(), "scope", "original")
			if err != nil || saved.Info.State != state || saved.Revision != 1 || saved.Info.Progress != nil {
				t.Fatal(saved, err)
			}
		})
	}
}

func TestTaskProgressObserverPreservesStatusAndRejectsLateFacts(t *testing.T) {
	p := newTaskProgress()
	ctx := context.WithValue(t.Context(), progressObserverKey{}, p.report)
	var plain int
	ctx = WithStatusReporter(ctx, func(string) { plain++ })
	if !ReportStatus(ctx, "读取原任务") || plain != 1 || p.latest == nil || p.latest.Message != "读取原任务" {
		t.Fatal(plain, p.latest)
	}
	at := p.latest.UpdatedAt
	ctx = WithProgressReporter(ctx, func(progress.Snapshot) { panic("private display error") })
	if !ReportProgress(ctx, progress.Snapshot{Message: "已保存进度", UpdatedAt: at.Add(time.Second)}) || p.latest.Message != "已保存进度" {
		t.Fatal(p.latest)
	}
	ReportProgress(ctx, progress.Snapshot{Message: "迟到旧快照", UpdatedAt: at})
	if p.latest.Message != "已保存进度" {
		t.Fatal("late progress replaced latest", p.latest)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if ReportProgress(cancelled, progress.Snapshot{Message: "cancelled"}) {
		t.Fatal("cancelled handler reported progress")
	}
}

func TestTaskProgressCallbackFollowsPersistenceAndCannotBlockCompletion(t *testing.T) {
	store := NewMemoryTaskStore()
	progressEntered, releaseProgress := make(chan TaskInfo, 1), make(chan struct{})
	completed := make(chan TaskInfo, 1)
	r := taskRuntimeFixture(t, TaskConfig{Store: store, OnProgress: func(info TaskInfo) {
		saved, err := store.GetTask(t.Context(), "scope", info.ID)
		if err != nil || saved.Info.Progress == nil || info.Progress == nil || !saved.Info.Progress.UpdatedAt.Equal(info.Progress.UpdatedAt) {
			t.Errorf("callback preceded durable progress: %+v %v", saved, err)
		}
		progressEntered <- info
		<-releaseProgress
	}, OnComplete: func(info TaskInfo) { completed <- info }})
	defer close(releaseProgress)
	releaseHandler := make(chan struct{})
	var handlerReleased atomic.Bool
	t.Cleanup(func() {
		if handlerReleased.CompareAndSwap(false, true) {
			close(releaseHandler)
		}
	})
	info, err := r.Start(t.Context(), "scope", "turn", "call", Tool{Name: "slow_read", Title: "读取大范围目录", Effect: ToolEffectRead, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
		ReportProgress(ctx, progress.Snapshot{Message: "正在读取第一页", State: "running"})
		select {
		case <-releaseHandler:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		ReportProgress(ctx, progress.Snapshot{Message: "原读取已返回", State: "running"})
		return "PRIVATE_RESULT", nil
	}}, json.RawMessage(`{"credential":"PRIVATE_ARGS"}`))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case progressInfo := <-progressEntered:
		if progressInfo.ID != info.ID || progressInfo.GoalID == "" || progressInfo.Title != "读取大范围目录" || progressInfo.Input != nil || progressInfo.Result != "" || progressInfo.Error != "" {
			t.Fatal(progressInfo)
		}
	case <-time.After(time.Second):
		t.Fatal("first progress callback was not timely")
	}
	handlerReleased.Store(true)
	close(releaseHandler)
	select {
	case final := <-completed:
		if final.State != "completed" || final.Progress == nil || final.Progress.Message != "原读取已返回" {
			t.Fatal(final)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked progress callback blocked completion")
	}
}

func TestTaskProgressCallbackReconcilesCompletedTask(t *testing.T) {
	called := make(chan TaskInfo, 1)
	r := taskRuntimeFixture(t, TaskConfig{OnProgress: func(info TaskInfo) { called <- info }})
	old := TaskRecord{StorageVersion: TaskStorageVersion, ID: "original", Scope: "scope", Key: "call", Revision: 1, Info: TaskInfo{ID: "original", Tool: "read", State: "running", Progress: &progress.Snapshot{Message: "earlier", UpdatedAt: time.Now()}}}
	current := old
	current.Info.State = "completed"
	if _, _, err := r.Store().CreateTask(t.Context(), current); err != nil {
		t.Fatal(err)
	}
	r.enqueueProgressCallback(old)
	select {
	case info := <-called:
		if info.State != "completed" {
			t.Fatal("late progress revived completed task", info)
		}
	case <-time.After(time.Second):
		t.Fatal("authoritative final state was not delivered")
	}
}

func TestTaskProgressMailboxRecoversOverflowedFinalStates(t *testing.T) {
	for _, scope := range []string{"", "scope"} {
		t.Run("configured_scope="+scope, func(t *testing.T) {
			// 先保存旧终态，再建立新运行时；恢复不能依赖本实例执行过 Start。
			store := NewMemoryTaskStore()
			var records []TaskRecord
			for i := 0; i < 20; i++ {
				id := fmt.Sprintf("task-%02d", i)
				record := TaskRecord{StorageVersion: TaskStorageVersion, ID: id, Scope: "scope", Key: id, Revision: 1, Info: TaskInfo{ID: id, Tool: "read", GoalID: "goal", State: "completed", Progress: &progress.Snapshot{Message: "saved", UpdatedAt: time.Now()}}}
				if _, _, err := store.CreateTask(t.Context(), record); err != nil {
					t.Fatal(err)
				}
				records = append(records, record)
			}
			blocked, release := make(chan struct{}), make(chan struct{})
			var first atomic.Bool
			states := make(chan TaskInfo, 64)
			r := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: scope, MaxTasks: 2, OnProgress: func(info TaskInfo) {
				if first.CompareAndSwap(false, true) {
					close(blocked)
					<-release
				}
				states <- info
			}})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			for i, record := range records {
				r.enqueueProgressCallback(record)
				if i == 0 {
					select {
					case <-blocked:
					case <-time.After(time.Second):
						t.Fatal("dispatcher did not start")
					}
				}
			}
			r.progressMailbox.mu.Lock()
			queued, rescan := len(r.progressMailbox.queue), r.progressMailbox.rescan
			r.progressMailbox.mu.Unlock()
			if queued > 2 || !rescan {
				t.Fatal("mailbox was not bounded", queued, rescan)
			}
			close(release)
			seen := map[string]bool{}
			deadline := time.After(2 * time.Second)
			for len(seen) < 20 {
				select {
				case info := <-states:
					if info.State != "completed" {
						t.Fatal(info)
					}
					seen[info.ID] = true
				case <-deadline:
					t.Fatal("burst terminal states were lost", len(seen))
				}
			}
		})
	}
}

func TestTaskProgressBlockedSubscriberCannotBlockShutdown(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	r := taskRuntimeFixture(t, TaskConfig{OnProgress: func(TaskInfo) { close(entered); <-release }})
	defer close(release)
	record := TaskRecord{StorageVersion: TaskStorageVersion, ID: "one", Scope: "scope", Key: "one", Revision: 1, Info: TaskInfo{ID: "one", State: "completed", Progress: &progress.Snapshot{Message: "finished", UpdatedAt: time.Now()}}}
	if _, _, err := r.Store().CreateTask(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	r.enqueueProgressCallback(record)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("subscriber was not invoked")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := r.Shutdown(ctx); err != nil {
		t.Fatal("display subscriber blocked runtime shutdown", err)
	}
}

// 不依赖 Handler 返回：取消、失租和崩溃补账都必须把持久状态送入同一展示通道。
func TestTaskProgressPublishesPersistedRecoveryAndCancellation(t *testing.T) {
	for _, kind := range []string{"cancel", "lease_expired", "receipt_recovery"} {
		t.Run(kind, func(t *testing.T) {
			states := make(chan TaskInfo, 8)
			r := taskRuntimeFixture(t, TaskConfig{OnProgress: func(info TaskInfo) { states <- info }})
			at := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
			record := TaskRecord{StorageVersion: TaskStorageVersion, ID: "original", Scope: "scope", Key: "original", Digest: "digest", Effect: ToolEffectWrite, Started: true, Revision: 1, LeaseUntil: time.Now().Add(time.Minute), Info: TaskInfo{ID: "original", Tool: "write", GoalID: "goal", Background: true, State: "running", Progress: &progress.Snapshot{Message: "原处理进度", UpdatedAt: at}}}
			if kind == "lease_expired" {
				record.LeaseUntil = at
			}
			if _, _, err := r.Store().CreateTask(t.Context(), record); err != nil {
				t.Fatal(err)
			}
			want := ""
			switch kind {
			case "cancel":
				want = "cancelling"
				if _, err := r.Cancel(record.Scope, record.ID, map[string]bool{"write": true}); err != nil {
					t.Fatal(err)
				}
			case "lease_expired":
				want = "unknown"
				if _, err := r.refresh(t.Context(), record); err != nil {
					t.Fatal(err)
				}
			case "receipt_recovery":
				want = "completed"
				outcome := r.makeResult(&toolTask{id: record.ID, scope: record.Scope}, Tool{Effect: ToolEffectWrite}, "saved receipt", nil, true, false, record.Digest)
				if _, err := r.cfg.Results.PutResult(t.Context(), outcome); err != nil {
					t.Fatal(err)
				}
				if _, err := r.refresh(t.Context(), record); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case info := <-states:
				if info.State != want || info.ID != record.ID || info.GoalID != "goal" || info.Progress == nil || !info.Progress.UpdatedAt.Equal(at) {
					t.Fatal("persistent state or original progress identity/time lost", info)
				}
			case <-time.After(time.Second):
				t.Fatal("persistent state change did not reach display subscriber", want)
			}
		})
	}
}

// 没有业务进度也要可靠告知终态，宿主才能撤下原作用域的停止入口；不伪造快照。
func TestTaskProgressTerminalWithoutSnapshotIsDelivered(t *testing.T) {
	states := make(chan TaskInfo, 2)
	r := taskRuntimeFixture(t, TaskConfig{OnProgress: func(info TaskInfo) { states <- info }})
	tool := Tool{Name: "silent_read", Effect: ToolEffectRead, Handler: func(context.Context, json.RawMessage) (string, error) { return "receipt", nil }}
	info, err := r.Start(t.Context(), "scope", "turn", "call", tool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case final := <-states:
		if final.ID != info.ID || final.State != "completed" || final.Progress != nil || final.Result != "" {
			t.Fatal("terminal notification fabricated progress or exposed result", final)
		}
	case <-time.After(time.Second):
		t.Fatal("silent task final state was lost")
	}
}
