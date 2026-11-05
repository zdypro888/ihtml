package assistant

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

type taskWakeQueueKey struct{}

type taskWakeState struct {
	record   agent.TaskWaitRecord
	consumer string
	cancel   context.CancelFunc
}

// startTaskWake 的轮询只检查存储事件，不调用模型。恢复计划仍来自 AI 保存的 checkpoint。
func (s *session) startTaskWake() {
	if s.tasks == nil || s.tasks.Scope() == "" {
		return
	}
	s.taskWakeDone = make(chan struct{})
	go func() {
		defer close(s.taskWakeDone)
		ticker := time.NewTicker(s.tasks.PollInterval())
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.pollTaskWake()
			}
		}
	}()
}
func (s *session) pollTaskWake() {
	s.mu.Lock()
	stopped := s.taskWakeStopped
	s.mu.Unlock()
	if stopped {
		return
	}
	ctx, stop := context.WithTimeout(s.ctx, 5*time.Second)
	defer stop()
	if guard := s.backend.cfg.TaskWakeGuard; guard != nil {
		host := s.sctx
		if guard(ctx, &host) != nil {
			// 撤权也必须中止已经准入的本地推理；保留来源元数据，直到队列退休再释放。
			s.mu.Lock()
			for _, w := range s.taskWakes {
				if w.cancel != nil {
					w.cancel()
				}
			}
			s.mu.Unlock()
			_ = s.tasks.CancelWaits(ctx, s.tasks.Scope())
			return
		}
	}
	s.mu.Lock()
	live := make([]taskWakeState, 0, len(s.taskWakes))
	for _, w := range s.taskWakes {
		live = append(live, w)
	}
	s.mu.Unlock()
	for _, w := range live {
		if s.tasks.UpdateWake(ctx, s.tasks.Scope(), w.record.ID, w.consumer, "renew") != nil {
			s.mu.Lock()
			current, exists := s.taskWakes["task-wake/"+w.record.ID]
			s.mu.Unlock()
			// 元数据保留到队列退休，避免失租的事件被误当成新用户消息；旧消费者不能继续推理。
			if exists && current.consumer == w.consumer && current.cancel != nil {
				current.cancel()
			}
		}
	}
	// 同一会话只预取一个恢复事件，避免挤占普通用户消息队列。
	if len(live) > 0 {
		return
	}
	consumer := rand.Text()
	wait, err := s.tasks.ClaimWake(ctx, s.tasks.Scope(), consumer)
	if err != nil || wait == nil {
		return
	}
	if ack := s.backend.cfg.TaskWakeAcknowledged; ack != nil {
		host := s.sctx
		done, e := ack(ctx, &host, wait.ID)
		if e != nil {
			_ = s.tasks.UpdateWake(ctx, s.tasks.Scope(), wait.ID, consumer, "release")
			return
		}
		if done {
			_ = s.tasks.UpdateWake(ctx, s.tasks.Scope(), wait.ID, consumer, "ack")
			return
		}
	}
	data, _ := json.Marshal(map[string]any{"wait_id": wait.ID, "reason": wait.Reason, "task_ids": wait.Spec.IDs, "checkpoint": wait.Spec.Checkpoint})
	event := ihtml.ChatClientEvent{Type: ihtml.ChatClientUserMessage, RequestID: "task-wake/" + wait.ID, Messages: []ihtml.ChatMessage{{Role: "user", Content: "已登记的后台等待条件已触发。以下为原等待上下文与事件事实，请核对回执并继续原请求；不继承额外授权：\n" + string(data)}}}
	s.mu.Lock()
	if s.closed || s.ctx.Err() != nil || s.queuedOrdinaryTurnsLocked() >= s.backend.cfg.MaxPendingTurns {
		s.mu.Unlock()
		_ = s.tasks.UpdateWake(ctx, s.tasks.Scope(), wait.ID, consumer, "release")
		return
	}
	if s.taskWakes == nil {
		s.taskWakes = map[string]taskWakeState{}
	}
	state := taskWakeState{record: *wait, consumer: consumer}
	s.taskWakes[event.RequestID] = state
	parent := context.WithValue(s.ctx, taskWakeQueueKey{}, state)
	if s.runner == nil {
		s.queue = append(s.queue, pendingTurn{parent: parent, event: event})
		select {
		case s.wake <- struct{}{}:
		default:
		}
		s.mu.Unlock()
	} else {
		s.mu.Unlock()
		if s.runner.Submit(parent, &event) != nil {
			s.finishTaskWake(&event, false)
		}
	}
}
func (s *session) finishTaskWake(event *ihtml.ChatClientEvent, ok bool) {
	s.mu.Lock()
	w, exists := s.taskWakes[event.RequestID]
	delete(s.taskWakes, event.RequestID)
	s.mu.Unlock()
	if !exists {
		return
	}
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	action := "release"
	if ok {
		action = "ack"
	}
	_ = s.tasks.UpdateWake(ctx, s.tasks.Scope(), w.record.ID, w.consumer, action)
}

func (s *session) stopTaskWakes() {
	if s.tasks == nil {
		return
	}
	s.mu.Lock()
	s.taskWakeStopped = true
	for _, w := range s.taskWakes {
		if w.cancel != nil {
			w.cancel()
		}
	}
	s.taskWakes = map[string]taskWakeState{}
	s.mu.Unlock()
	if err := s.tasks.CancelAll(); err != nil {
		_ = s.emit(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnError, Error: "本地停止已请求，但等待登记的取消尚未持久确认；请核对原记录。"})
	}
}
func (s *session) restartTaskWakes(ctx context.Context) {
	if s.tasks == nil {
		return
	}
	s.mu.Lock()
	stopped := s.taskWakeStopped
	s.mu.Unlock()
	if !stopped {
		return
	}
	if s.tasks.Scope() == "" || s.tasks.CancelWaits(ctx, s.tasks.Scope()) == nil {
		s.mu.Lock()
		s.taskWakeStopped = false
		s.mu.Unlock()
	}
}
