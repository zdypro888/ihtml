package assistant

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

// 唤醒已开始后撤权仍须取消当前推理，不能只取消存储登记再跳过本地消费者。
func TestTaskWakeRevocationCancelsAlreadyRunningConsumer(t *testing.T) {
	store := agent.NewMemoryTaskStore()
	runtime, err := agent.NewTaskRuntime(t.Context(), agent.TaskConfig{Store: store, Scope: "scope"})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	wait, err := runtime.Suspend(t.Context(), "scope", "turn", "timer", agent.TaskWaitSpec{Timeout: time.Millisecond, Checkpoint: "continue original goal"}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	started, cancelled := make(chan struct{}), make(chan struct{})
	cfg := configFor(&fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}})
	var revoked atomic.Bool
	cfg.TaskWakeGuard = func(context.Context, *ihtml.ChatSessionContext) error {
		if revoked.Load() {
			return ihtml.ErrAccessDenied
		}
		return nil
	}
	cfg.TasksProvider = func(context.Context, *ihtml.ChatSessionContext) (agent.TaskConfig, error) {
		return agent.TaskConfig{Store: store, Scope: "scope", PollInterval: time.Millisecond}, nil
	}
	openSession(t, cfg, "alice", newRecorder())
	waitSignal(t, started)
	revoked.Store(true)
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("revoked wake consumer continued model execution")
	}
	deadline := time.Now().Add(time.Second)
	for {
		row, err := store.GetWait(t.Context(), "scope", wait.ID)
		if err == nil && row.State == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("revoked wait was not durably cancelled", row, err)
		}
		time.Sleep(time.Millisecond)
	}
}
