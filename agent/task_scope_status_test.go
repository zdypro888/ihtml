package agent

import (
	"fmt"
	"testing"
	"time"
)

func TestTaskScopeStatusReadsOnlyCurrentEpochAndScope(t *testing.T) {
	store := NewMemoryTaskStore()
	r := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "owner"})
	epoch, err := r.scopeEpoch(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 132 {
		row := TaskRecord{ID: fmt.Sprintf("task-%03d", i), Scope: "owner", Epoch: epoch, Key: fmt.Sprint(i), Revision: 1, Info: TaskInfo{State: "queued"}}
		if i == 130 {
			row.Epoch = "old"
		}
		if i == 131 {
			row.Scope = "foreign"
		}
		if _, _, err := store.CreateTask(t.Context(), row); err != nil {
			t.Fatal(err)
		}
	}
	for i, state := range []string{"waiting", "delivering", "delivered"} {
		row := TaskWaitRecord{ID: fmt.Sprint(i), Scope: "owner", Epoch: epoch, Key: fmt.Sprint(i), Revision: 1, State: state}
		if _, _, err := store.CreateWait(t.Context(), row); err != nil {
			t.Fatal(err)
		}
	}
	before := time.Now().UTC()
	status, err := r.ScopeStatus(t.Context())
	if err != nil || status.ActiveTasks != 130 || status.ActiveWaits != 2 || status.CheckedAt.Before(before) {
		t.Fatal(status, err)
	}
	if err := r.stopScope(t.Context(), "owner"); err != nil {
		t.Fatal(err)
	}
	status, err = r.ScopeStatus(t.Context())
	if err != nil || status.ActiveTasks != 0 || status.ActiveWaits != 0 {
		t.Fatal("old work restored stop state", status, err)
	}
}

func TestTaskScopeStatusFailureDoesNotInventEmptyState(t *testing.T) {
	store := failingTaskScanStore{NewMemoryTaskStore()}
	r := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "owner"})
	if _, err := r.scopeEpoch(t.Context(), "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ScopeStatus(t.Context()); err == nil {
		t.Fatal("unavailable storage reported no active tasks")
	}
}
