package assistant

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

// 模拟执行标记已落库，但存储应答与运行时关闭交错；不靠延迟猜测排队状态。
type confirmationAdmissionStore struct {
	agent.TaskStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *confirmationAdmissionStore) ReplaceTask(ctx context.Context, row agent.TaskRecord, revision int64) (bool, error) {
	ok, err := s.TaskStore.ReplaceTask(ctx, row, revision)
	if ok && err == nil && row.Info.State == "running" {
		s.once.Do(func() {
			close(s.entered)
			<-s.release
		})
	}
	return ok, err
}

// 同步批准刚取得执行名额即停止时，前台可以先返回；原授权不能因此被消费或重复执行。
func TestSynchronousConfirmationStopAtAdmissionKeepsOriginalApproval(t *testing.T) {
	store := &confirmationAdmissionStore{TaskStore: agent.NewMemoryTaskStore(), entered: make(chan struct{}), release: make(chan struct{})}
	var writes atomic.Int32
	cfg := approvalConfig(t, func(context.Context, json.RawMessage) (string, error) {
		writes.Add(1)
		return "saved once", nil
	})
	cfg.Tasks = &agent.TaskConfig{Store: store, MaxConcurrent: 1}
	recorder := newRecorder()
	s := openSession(t, cfg, "alice", recorder).(*session)
	card := proposedCard(t, s, recorder)
	select {
	case <-store.entered:
		t.Fatal("proposal unexpectedly executed a managed write")
	default:
	}
	event := &ihtml.ChatClientEvent{Type: ihtml.ChatClientConfirmTool, ToolCall: &card}
	s.pendingMu.Lock()
	s.pendingTools[card.ConfirmationNonce].queued = true
	s.pendingMu.Unlock()
	scheduled := make(chan bool, 1)
	go func() { scheduled <- s.scheduleConfirmedTask(t.Context(), event, false) }()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("confirmation never reached admission")
	}
	closed := make(chan struct{})
	go func() { s.tasks.Close(); close(closed) }()
	select {
	case handled := <-scheduled:
		if !handled {
			t.Fatal("confirmation not handled")
		}
	case <-time.After(time.Second):
		t.Fatal("stopped synchronous caller did not return")
	}
	close(store.release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("stopped handler did not unwind")
	}
	if writes.Load() != 0 {
		t.Fatal("cancelled admission executed a write", writes.Load())
	}
	s.pendingMu.Lock()
	pending := s.pendingTools[card.ConfirmationNonce]
	available := pending != nil && !pending.queued
	s.pendingMu.Unlock()
	if !available {
		t.Fatal("stopped admission consumed or reserved the original approval")
	}
}
