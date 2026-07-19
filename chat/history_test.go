package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/schema"
)

func historyEvent(id, content string) *adk.SessionEvent[*schema.Message] {
	return &adk.SessionEvent[*schema.Message]{
		EventID: id,
		Kind:    adk.SessionEventMessage,
		Message: schema.UserMessage(content),
	}
}

func TestBoundedHistoryEinoConformance(t *testing.T) {
	adksession.RunConformanceTests(t, func(testing.TB) adk.SessionEventStore[*schema.Message] {
		return newBoundedSessionStore(historyLimits{
			maxSessions: -1,
			maxEvents:   -1,
			maxBytes:    -1,
			idleTTL:     -1,
		})
	}, schema.UserMessage)
}

func TestBoundedHistoryPrunesOldestEvents(t *testing.T) {
	store := newBoundedSessionStore(historyLimits{maxSessions: 10, maxEvents: 2, maxBytes: 1 << 20, idleTTL: time.Hour})
	ctx := context.Background()
	if err := store.AppendEvents(ctx, "s", []*adk.SessionEvent[*schema.Message]{
		historyEvent("1", "one"), historyEvent("2", "two"), historyEvent("3", "three"),
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadEvents(ctx, "s", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Events) != 2 || loaded.Events[0].EventID != "2" || loaded.Events[1].EventID != "3" {
		t.Fatalf("unexpected retained events: %+v", loaded.Events)
	}
	_, err = store.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{After: "1"})
	if !errors.Is(err, adk.ErrEventIDOutOfRange) {
		t.Fatalf("pruned cursor should be out of range: %v", err)
	}
}

func TestBoundedHistoryPrunesBySerializedBytes(t *testing.T) {
	const limit = int64(1800)
	store := newBoundedSessionStore(historyLimits{maxSessions: 10, maxEvents: 100, maxBytes: limit, idleTTL: time.Hour})
	events := make([]*adk.SessionEvent[*schema.Message], 0, 8)
	for i := 0; i < 8; i++ {
		events = append(events, historyEvent(string(rune('a'+i)), strings.Repeat("x", 300)))
	}
	if err := store.AppendEvents(context.Background(), "s", events); err != nil {
		t.Fatal(err)
	}
	entry := store.sessions["s"]
	if entry == nil || entry.bytes > limit || len(entry.events) >= len(events) || len(entry.events) == 0 {
		t.Fatalf("byte retention failed: bytes=%d events=%d", entry.bytes, len(entry.events))
	}
}

func TestBoundedHistoryLRUEvictsInactiveSessions(t *testing.T) {
	store := newBoundedSessionStore(historyLimits{maxSessions: 2, maxEvents: 10, maxBytes: 1 << 20, idleTTL: time.Hour})
	for _, id := range []string{"s1", "s2", "s3"} {
		store.acquire(id)
		if err := store.AppendEvents(context.Background(), id, []*adk.SessionEvent[*schema.Message]{historyEvent(id, id)}); err != nil {
			t.Fatal(err)
		}
		store.release(id)
	}
	if len(store.sessions) != 2 || store.sessions["s1"] != nil || store.sessions["s2"] == nil || store.sessions["s3"] == nil {
		t.Fatalf("unexpected LRU sessions: %#v", store.sessions)
	}
}

func TestBoundedHistoryPinsActiveSession(t *testing.T) {
	store := newBoundedSessionStore(historyLimits{maxSessions: 1, maxEvents: 10, maxBytes: 1 << 20, idleTTL: time.Hour})
	store.acquire("active")
	store.acquire("new")
	if len(store.sessions) != 2 {
		t.Fatalf("active sessions should be temporarily retained: %d", len(store.sessions))
	}
	store.release("new")
	if len(store.sessions) != 1 || store.sessions["active"] == nil {
		t.Fatalf("inactive session should be evicted before active: %#v", store.sessions)
	}
	store.release("active")
}

func TestBoundedHistoryExpiresIdleSessions(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newBoundedSessionStore(historyLimits{maxSessions: 10, maxEvents: 10, maxBytes: 1 << 20, idleTTL: time.Minute})
	store.now = func() time.Time { return now }
	store.acquire("old")
	store.release("old")
	now = now.Add(time.Minute)
	store.acquire("new")
	if store.sessions["old"] != nil {
		t.Fatal("idle session was not expired")
	}
	store.release("new")
}

func TestSecureSessionIDIsStableAndUnambiguous(t *testing.T) {
	a := secureSessionID("a-b", "c")
	b := secureSessionID("a", "b-c")
	if a == b {
		t.Fatal("ambiguous user/csid pairs collided")
	}
	if a != secureSessionID("a-b", "c") {
		t.Fatal("stable csid did not produce stable session ID")
	}
	if strings.Contains(a, "a-b") {
		t.Fatalf("session ID leaks source identifiers: %q", a)
	}
}
