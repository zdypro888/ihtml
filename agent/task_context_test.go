package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func contextFixtureDoc(goal string) TaskContextDocument {
	return TaskContextDocument{Goal: goal, Constraints: []string{"fixed user scope"}, Pending: []string{"inspect saved evidence"}, Evidence: []TaskContextReference{{Kind: "task", ID: "original-receipt", Revision: 1}}, State: "active"}
}

// 长期工作事实跨运行时恢复，重投保留当前版本但不覆盖后续修改，墓碑不泄露旧正文。
func TestTaskContextRecreationCASDedupAndDeletion(t *testing.T) {
	store := NewMemoryTaskStore()
	r := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "owner"})
	first, err := r.SaveContext(t.Context(), "owner", "turn", "create", "", 0, contextFixtureDoc("original goal"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || first.ID == "" {
		t.Fatal(first)
	}
	updated, err := r.SaveContext(t.Context(), "owner", "turn2", "update", first.ID, 1, contextFixtureDoc("publicly revised goal"))
	if err != nil || updated.Revision != 2 {
		t.Fatal(updated, err)
	}
	same, err := r.SaveContext(t.Context(), "owner", "turn", "create", "", 0, contextFixtureDoc("original goal"))
	if err != nil || !same.Replayed || same.ID != first.ID || same.Revision != 2 || same.AppliedRevision != 1 || same.Document.Goal != "publicly revised goal" {
		t.Fatal("old mutation replay overwrote new facts", same, err)
	}
	if _, err = r.SaveContext(t.Context(), "owner", "turn3", "stale", first.ID, 1, contextFixtureDoc("stale overwrite")); !errors.Is(err, ErrTaskConflict) {
		t.Fatal(err)
	}
	if _, err = r.SaveContext(t.Context(), "owner", "turn", "create", "", 0, contextFixtureDoc("changed logical mutation")); !errors.Is(err, ErrTaskConflict) {
		t.Fatal(err)
	}
	r.Close()
	r = taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "owner"})
	read, err := r.ReadContext(t.Context(), "owner", first.ID)
	if err != nil || read.Document.Goal != "publicly revised goal" {
		t.Fatal(read, err)
	}
	read.Document.Pending[0] = "mutated outside store"
	again, _ := r.ReadContext(t.Context(), "owner", first.ID)
	if again.Document.Pending[0] != "inspect saved evidence" {
		t.Fatal("read bypassed CAS")
	}
	if _, err = r.ReadContext(t.Context(), "foreign", first.ID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatal("foreign scope leaked", err)
	}
	deleted, err := r.DeleteContext(t.Context(), "owner", "turn4", "delete", first.ID, 2)
	if err != nil || !deleted.Deleted || deleted.Document.Goal != "" {
		t.Fatal(deleted, err)
	}
	same, err = r.SaveContext(t.Context(), "owner", "turn", "create", "", 0, contextFixtureDoc("original goal"))
	if err != nil || !same.Deleted || !same.Replayed || same.Document.Goal != "" {
		t.Fatal("old create revived tombstone", same, err)
	}
	page, err := r.ListContexts(t.Context(), "owner", "", 16)
	if err != nil || len(page.Contexts) != 0 || page.RecordsUsed != 1 {
		t.Fatal(page, err)
	}
}

// 共享存储中的并发保存必须一位获胜，不把不同AI轮次最后写入当作正确合并。
func TestTaskContextConcurrentWritersAndStoppedEpoch(t *testing.T) {
	store := NewMemoryTaskStore()
	r := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "owner"})
	other := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "owner"})
	bound, err := r.BindContext(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.SaveContext(bound, "owner", "turn", "create", "", 0, contextFixtureDoc("goal"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := other.SaveContext(t.Context(), "owner", "update", string(rune('a'+i)), first.ID, 1, contextFixtureDoc("concurrent"))
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrTaskConflict) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatal("CAS winners", wins)
	}
	if err = other.CancelAll(); err != nil {
		t.Fatal(err)
	}
	if _, err = r.SaveContext(bound, "owner", "old-plan", "late-write", first.ID, 2, contextFixtureDoc("stale stopped plan")); !errors.Is(err, ErrTaskStopped) {
		t.Fatal("stopped plan wrote memory", err)
	}
	current, err := other.ReadContext(t.Context(), "owner", first.ID)
	if err != nil || current.Revision != 2 {
		t.Fatal(current, err)
	}
	fresh, err := other.BindContext(bound, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.SaveContext(fresh, "owner", "fresh-plan", "explicit-update", first.ID, 2, contextFixtureDoc("fresh explicit goal")); err != nil {
		t.Fatal(err)
	}
}

// 配额、过期与显式游标都属于存储协议；更新额度满时仍允许删除正文，墓碑不绕过记录上限。
func TestTaskContextCapacityExpiryAndPaging(t *testing.T) {
	store := NewMemoryTaskStore()
	r := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "owner", MaxContexts: 2, MaxContextMutations: 2})
	a, err := r.SaveContext(t.Context(), "owner", "turn", "a", "a", 0, contextFixtureDoc("goal A"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.SaveContext(t.Context(), "owner", "turn", "b", "b", 0, contextFixtureDoc("goal B")); err != nil {
		t.Fatal(err)
	}
	if _, err = r.SaveContext(t.Context(), "owner", "turn", "c", "c", 0, contextFixtureDoc("goal C")); !errors.Is(err, ErrTaskContextCapacity) {
		t.Fatal(err)
	}
	page, err := r.ListContexts(t.Context(), "owner", "", 1)
	if err != nil || len(page.Contexts) != 1 || page.Contexts[0].ID != "a" || page.Next != "a" || page.RecordCapacity != 2 || page.MutationsUsed != 2 {
		t.Fatal(page, err)
	}
	next, err := r.ListContexts(t.Context(), "owner", page.Next, 1)
	if err != nil || len(next.Contexts) != 1 || next.Contexts[0].ID != "b" {
		t.Fatal(next, err)
	}
	if _, err = r.DeleteContext(t.Context(), "owner", "turn", "delete", a.ID, a.Revision); err != nil {
		t.Fatal("full mutation log blocked deleting body", err)
	}
	if err = r.cfg.Contexts.CollectContexts(t.Context(), time.Now().Add(31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = r.ReadContext(t.Context(), "owner", "a"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatal(err)
	}
	page, err = r.ListContexts(t.Context(), "owner", "", 1)
	if err != nil || page.RecordsUsed != 0 || page.MutationsUsed != 0 {
		t.Fatal(page, err)
	}
}

type acknowledgedContextStore struct {
	TaskContextStore
	lost bool
}

func (s *acknowledgedContextStore) CommitContext(ctx context.Context, m TaskContextMutation) (TaskContextRecord, error) {
	row, err := s.TaskContextStore.CommitContext(ctx, m)
	if err == nil && s.lost {
		return TaskContextRecord{}, errors.New("private storage address and credential")
	}
	return row, err
}

// CAS成功但应答丢失的写入不能声称未执行；同一逻辑重投只核对原记录，不创建第二份目标。
func TestTaskContextLostAcknowledgementKeepsUnknownAndDeduplicates(t *testing.T) {
	store := &acknowledgedContextStore{TaskContextStore: NewMemoryTaskContextStore(), lost: true}
	r := taskRuntimeFixture(t, TaskConfig{Contexts: store, Scope: "owner"})
	_, err := r.SaveContext(t.Context(), "owner", "turn", "write", "", 0, contextFixtureDoc("goal"))
	var failure *TaskFailure
	if !errors.As(err, &failure) || failure.Code != "storage_unavailable" || failure.ExecutionState != "unknown" || failure.AdmissionState != "unknown" || !failure.Admitted || failure.ReceiptID == "" || strings.Contains(failure.Error(), "credential") {
		t.Fatal("ambiguous context commit misreported", failure, err)
	}
	store.lost = false
	replay, err := r.SaveContext(t.Context(), "owner", "turn", "write", "", 0, contextFixtureDoc("goal"))
	if err != nil || !replay.Replayed || replay.ID != failure.ReceiptID || replay.Revision != 1 {
		t.Fatal(replay, err)
	}
	page, err := r.ListContexts(t.Context(), "owner", "", 16)
	if err != nil || page.RecordsUsed != 1 || page.MutationsUsed != 1 {
		t.Fatal("lost ACK duplicated context", page, err)
	}
}

// 活跃目标延长保存后，旧创建去重也须同寿命，不能在旧回执期限到达时静默生成目标副本。
func TestTaskContextRetentionKeepsOriginalMutationWhileGoalLives(t *testing.T) {
	now := time.Now().UTC()
	first := TaskContextMutation{ID: "original", Scope: "scope", Key: taskHash("create"), Digest: taskHash("original input"), Document: contextFixtureDoc("original goal"), Now: now, ExpiresAt: now.Add(30 * 24 * time.Hour), MaxRecords: 2, MaxMutations: 16, MaxBytes: 32 << 10}
	bucket, _, err := ApplyTaskContextMutation(TaskContextBucket{}, first)
	if err != nil {
		t.Fatal(err)
	}
	update := first
	update.Key = taskHash("update")
	update.Digest = taskHash("updated input")
	update.ExpectedRevision = 1
	update.Now = now.Add(29 * 24 * time.Hour)
	update.ExpiresAt = now.Add(59 * 24 * time.Hour)
	update.Document = contextFixtureDoc("current goal")
	bucket, _, err = ApplyTaskContextMutation(bucket, update)
	if err != nil {
		t.Fatal(err)
	}
	retry := first
	retry.ID = "new_random_on_retry"
	retry.Now = now.Add(31 * 24 * time.Hour)
	retry.ExpiresAt = retry.Now.Add(30 * 24 * time.Hour)
	bucket, replayed, err := ApplyTaskContextMutation(bucket, retry)
	if err != nil || !replayed.Replayed || replayed.ID != "original" || replayed.Document.Goal != "current goal" || len(bucket.Records) != 1 {
		t.Fatal("old creation duplicated active goal", replayed, err)
	}
}
