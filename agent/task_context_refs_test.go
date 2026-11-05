package agent

import (
	"errors"
	"testing"
	"time"
)

// 等待引用保留原观察版本；目标变更/删除是恢复事实，不由运行时编排下一步或借入新授权。
func TestTaskWaitReportsChangedContextReferenceWithoutReplacingGoal(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed", true: "deleted"}[remove], func(t *testing.T) {
			store := NewMemoryTaskStore()
			r := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "scope"})
			goal, err := r.SaveContext(t.Context(), "scope", "turn", "goal", "", 0, TaskContextDocument{Goal: "original exact goal", State: "active"})
			if err != nil {
				t.Fatal(err)
			}
			spec := TaskWaitSpec{Timeout: time.Hour, Contexts: []TaskContextVersion{{ID: goal.ID, Revision: goal.Revision}}}
			wait, err := r.Suspend(t.Context(), "scope", "turn", "suspend", spec, nil)
			if err != nil {
				t.Fatal(err)
			}
			wrong := spec
			wrong.Contexts = []TaskContextVersion{{ID: goal.ID, Revision: goal.Revision + 100}}
			if _, err = r.Suspend(t.Context(), "scope", "turn", "invalid-reference", wrong, nil); !errors.Is(err, ErrTaskConflict) {
				t.Fatal("unknown version accepted", err)
			}
			var latest TaskContextRecord
			if remove {
				latest, err = r.DeleteContext(t.Context(), "scope", "other", "delete", goal.ID, goal.Revision)
			} else {
				latest, err = r.SaveContext(t.Context(), "scope", "other", "change", goal.ID, goal.Revision, TaskContextDocument{Goal: "new goal", State: "active"})
			}
			if err != nil {
				t.Fatal(err)
			}
			saved, _ := store.GetWait(t.Context(), "scope", wait.ID)
			revision := saved.Revision
			saved.Spec.Deadline = time.Now().Add(-time.Second)
			saved.Revision++
			if ok, err := store.ReplaceWait(t.Context(), saved, revision); err != nil || !ok {
				t.Fatal(ok, err)
			}
			claimed, err := r.ClaimWake(t.Context(), "scope", "consumer")
			if err != nil || claimed == nil || len(claimed.ContextMatches) != 1 {
				t.Fatal(claimed, err)
			}
			state := "version_changed"
			if remove {
				state = "deleted"
			}
			match := claimed.ContextMatches[0]
			if match.State != state || match.ExpectedRevision != goal.Revision || match.CurrentRevision != latest.Revision || claimed.Spec.Checkpoint != "" {
				t.Fatal("reference silently became a new goal", claimed)
			}
			// 返回对象不能绕过存储CAS改写原登记的引用事实。
			claimed.Spec.Contexts[0].Revision = 999
			claimed.ContextMatches[0].State = "available"
			persisted, _ := store.GetWait(t.Context(), "scope", wait.ID)
			if persisted.Spec.Contexts[0].Revision != goal.Revision || persisted.ContextMatches[0].State != state {
				t.Fatal("reference metadata alias escaped CAS", persisted)
			}
		})
	}
}
