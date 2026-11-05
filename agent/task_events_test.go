package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// 到达顺序与发布者序号分离；重复发布不能推进游标，正文失效也不能消除已发生事实。
func TestExternalEventsHaveDurableArrivalOrderDedupAndBoundedRetention(t *testing.T) {
	store := NewMemoryTaskStore()
	r := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "scope", MaxEventRecords: 2})
	publisher := r.EventPublisher()
	source, err := publisher.OpenSource(t.Context(), "scope", "stable-host-key", "a source")
	if err != nil {
		t.Fatal(err)
	}
	again, err := publisher.OpenSource(t.Context(), "scope", "stable-host-key", "a source")
	if err != nil || again.ID != source.ID {
		t.Fatal(again, err)
	}
	if _, err = publisher.OpenSource(t.Context(), "scope", "stable-host-key", "changed label"); !errors.Is(err, ErrTaskConflict) {
		t.Fatal("source meaning changed", err)
	}
	input := TaskExternalEvent{Key: "event-two", Type: "changed", ProducerSequence: "2", Payload: json.RawMessage(`{"large":9007199254740993,"value":"first arrival"}`)}
	first, err := publisher.Publish(t.Context(), "scope", source.ID, input)
	if err != nil || first.Seq != 1 {
		t.Fatal(first, err)
	}
	duplicate, err := publisher.Publish(t.Context(), "scope", source.ID, input)
	if err != nil || duplicate.Seq != 1 {
		t.Fatal("duplicate advanced sequence", duplicate, err)
	}
	input.Payload = json.RawMessage(`{"value":"changed"}`)
	if _, err = publisher.Publish(t.Context(), "scope", source.ID, input); !errors.Is(err, ErrTaskConflict) {
		t.Fatal("same event key changed facts", err)
	}
	second, err := publisher.Publish(t.Context(), "scope", source.ID, TaskExternalEvent{Key: "late-one", Type: "changed", ProducerSequence: "1", Payload: json.RawMessage(`{"value":"late arrival"}`)})
	if err != nil || second.Seq != 2 {
		t.Fatal("late producer event lost", second, err)
	}
	page, err := r.ReadEvents(t.Context(), "scope", source.ID, 0, 1)
	if err != nil || page.Next != 1 || !page.More || !strings.Contains(string(page.Events[0].Payload), "9007199254740993") {
		t.Fatal(page, err)
	}
	if _, err = r.ReadEvents(t.Context(), "scope", source.ID, 3, 1); !errors.Is(err, ErrTaskEventCursor) {
		t.Fatal("invented future cursor accepted", err)
	}
	if _, err = r.ReadEvents(t.Context(), "foreign", source.ID, 0, 1); !errors.Is(err, ErrTaskNotFound) {
		t.Fatal("event scope leaked", err)
	}
	if err = store.events.CollectEvents(t.Context(), time.Now().Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	page, err = r.ReadEvents(t.Context(), "scope", source.ID, 0, 8)
	if err != nil || len(page.Events) != 2 || page.Events[0].Availability != "expired" || len(page.Events[0].Payload) != 0 || page.Next != 2 {
		t.Fatal("expired body erased fact/cursor", page, err)
	}
	if _, err = publisher.Publish(t.Context(), "scope", source.ID, TaskExternalEvent{Key: "third", Type: "changed", Payload: json.RawMessage(`{}`)}); !errors.Is(err, ErrTaskEventCapacity) {
		t.Fatal("retained event bound bypassed", err)
	}
	r.Close()
	restored := taskRuntimeFixture(t, TaskConfig{Store: store, Scope: "scope"})
	page, err = restored.ReadEvents(t.Context(), "scope", source.ID, 1, 8)
	if err != nil || len(page.Events) != 1 || page.Events[0].Seq != 2 {
		t.Fatal("recreation lost cursor", page, err)
	}
}

// 任务与外部条件可组合；事件早于订阅仍触发，交付重投保存同一份事实且不泄露正文。
func TestExternalEventSubscriptionsCombineWithTasksAndFreezeWakeFacts(t *testing.T) {
	r := taskRuntimeFixture(t, TaskConfig{Scope: "scope", PollInterval: time.Millisecond})
	publisher := r.EventPublisher()
	source, err := publisher.OpenSource(t.Context(), "scope", "external", "arbitrary source")
	if err != nil {
		t.Fatal(err)
	}
	_, err = publisher.Publish(t.Context(), "scope", source.ID, TaskExternalEvent{Key: "before-subscription", Type: "wanted", Payload: json.RawMessage(`{"private":"not-in-wake-envelope"}`)})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	defer close(release)
	row, err := r.Start(t.Context(), "scope", "turn", "read", Tool{Name: "arbitrary", Effect: ToolEffectRead, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-release:
			return "actual", nil
		}
	}}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	wait, err := r.Suspend(t.Context(), "scope", "turn", "wait", TaskWaitSpec{IDs: []string{row.ID}, Events: []TaskEventCondition{{SourceID: source.ID, After: 0, Types: []string{"wanted"}}}, All: true, Timeout: time.Hour, Checkpoint: "inspect actual facts"}, map[string]bool{"arbitrary": true})
	if err != nil {
		t.Fatal(err)
	}
	if claim, err := r.ClaimWake(t.Context(), "scope", "early"); err != nil || claim != nil {
		t.Fatal("all condition woke before task", claim, err)
	}
	if _, err = r.Cancel("scope", row.ID, map[string]bool{"arbitrary": true}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Wait(t.Context(), "scope", []string{row.ID}, map[string]bool{"arbitrary": true}, time.Second, true); err != nil {
		t.Fatal(err)
	}
	claim, err := r.ClaimWake(t.Context(), "scope", "a")
	if err != nil || claim == nil || claim.ID != wait.ID || len(claim.EventMatches) != 1 || claim.EventMatches[0].Seq != 1 {
		t.Fatal(claim, err)
	}
	encoded, _ := json.Marshal(claim)
	if strings.Contains(string(encoded), "not-in-wake-envelope") {
		t.Fatal("event payload leaked into wake facts")
	}
	if err = r.UpdateWake(t.Context(), "scope", wait.ID, "a", "release"); err != nil {
		t.Fatal(err)
	}
	_, err = publisher.Publish(t.Context(), "scope", source.ID, TaskExternalEvent{Key: "later", Type: "wanted", Payload: json.RawMessage(`{"different":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	// 过期源不能改变已经被认领过的事实；真实正文读取仍会拒绝。
	memory := r.cfg.Events.(*MemoryTaskEventStore)
	memory.mu.Lock()
	record := memory.sources[source.ID]
	record.ExpiresAt = time.Now().Add(-time.Second)
	memory.sources[source.ID] = record
	memory.mu.Unlock()
	stored, _ := r.cfg.Store.GetWait(t.Context(), "scope", wait.ID)
	revision := stored.Revision
	stored.RetryAt = time.Time{}
	stored.Revision++
	r.cfg.Store.ReplaceWait(t.Context(), stored, revision)
	retry, err := r.ClaimWake(t.Context(), "scope", "b")
	if err != nil || retry == nil || retry.EventMatches[0].Seq != 1 || retry.Reason != claim.Reason {
		t.Fatal("wake facts changed during redelivery", retry, err)
	}
	if _, err = r.ReadEvents(t.Context(), "scope", source.ID, 0, 1); !errors.Is(err, ErrTaskNotFound) {
		t.Fatal("expired source payload remained readable", err)
	}
}

// 多发布者同时重投同一key仍只追加一次；不同事件的序号不存在重号或空洞。
func TestExternalEventsConcurrentPublishersShareAtomicSequence(t *testing.T) {
	store := NewMemoryTaskEventStore()
	p := TaskEventPublisher{Store: store}
	source, err := p.OpenSource(t.Context(), "scope", "stable", "source")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if _, err := p.Publish(t.Context(), "scope", source.ID, TaskExternalEvent{Key: "same", Type: "fact", Payload: json.RawMessage(`{"a":1}`)}); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	row, err := store.GetEventSource(t.Context(), "scope", source.ID)
	if err != nil || row.Head != 1 || len(row.Records) != 1 {
		t.Fatal(row, err)
	}
}

// 发布观测只接收白名单关联元数据，重复事件不重复计数，观测异常也不能篡改已持久发布结果。
func TestExternalEventObservationsExcludePayloadAndCannotBreakPublication(t *testing.T) {
	store := NewMemoryTaskEventStore()
	var records []TaskObservation
	publisher := TaskEventPublisher{Store: store, Observe: func(_ context.Context, _ string, row TaskObservation) error {
		records = append(records, row)
		return errors.New("observer unavailable")
	}}
	source, err := publisher.OpenSource(t.Context(), "scope", "private-source-key", "private source label")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = publisher.OpenSource(t.Context(), "scope", "private-source-key", "private source label"); err != nil {
		t.Fatal(err)
	}
	event := TaskExternalEvent{Key: "private-event-key", Type: "private-type", ProducerSequence: "private-seq", Payload: json.RawMessage(`{"private":"private-payload"}`)}
	if _, err = publisher.Publish(t.Context(), "scope", source.ID, event); err != nil {
		t.Fatal(err)
	}
	if _, err = publisher.Publish(t.Context(), "scope", source.ID, event); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Kind != "event_source_created" || records[1].Kind != "event_published" {
		t.Fatal("duplicate observations or absent metadata", records)
	}
	encoded, _ := json.Marshal(records)
	if strings.Contains(string(encoded), "private") {
		t.Fatal("business data leaked into observation", string(encoded))
	}
	publisher.Observe = func(context.Context, string, TaskObservation) error { panic("observer panic") }
	if _, err = publisher.Publish(t.Context(), "scope", source.ID, TaskExternalEvent{Key: "another", Type: "fact", Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal("observer altered publish success", err)
	}
}
