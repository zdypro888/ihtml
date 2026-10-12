package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/schema"
	_ "github.com/lib/pq"
)

func testStore(t testing.TB, serializer schema.Serializer) *Store[*schema.Message] {
	t.Helper()
	dsn := os.Getenv("IHTML_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set IHTML_TEST_POSTGRES_DSN to run PostgreSQL contracts")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := New[*schema.Message](db, Config{Namespace: fmt.Sprintf("test-%d", time.Now().UnixNano()), EventSerializer: serializer})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM ihtml_agent_sessions WHERE namespace=$1`, s.namespace)
		db.Exec(`DELETE FROM ihtml_agent_checkpoints WHERE namespace=$1`, s.namespace)
	})
	return s
}

func event(id, text string) *adk.SessionEvent[*schema.Message] {
	return &adk.SessionEvent[*schema.Message]{EventID: id, Message: schema.UserMessage(text)}
}

func TestPostgresConformance(t *testing.T) {
	session.RunConformanceTests[*schema.Message](t, func(t testing.TB) adk.SessionEventStore[*schema.Message] { return testStore(t, nil) }, schema.UserMessage)
	session.RunSerializerConformanceTests[*schema.Message](t, func(t testing.TB, serializer schema.Serializer) adk.SessionEventStore[*schema.Message] {
		return testStore(t, serializer)
	}, schema.UserMessage)
}

func TestPostgresIsolationAtomicityAndRestart(t *testing.T) {
	s := testStore(t, nil)
	ctx := context.Background()
	if err := s.AppendEvents(ctx, "session", []*adk.SessionEvent[*schema.Message]{event("first", "original")}); err != nil {
		t.Fatal(err)
	}
	// A late duplicate must roll back the entire batch, including its earlier insert.
	err := s.AppendEvents(ctx, "session", []*adk.SessionEvent[*schema.Message]{event("new", "uncommitted"), event("first", "duplicate")})
	if !errors.Is(err, adk.ErrDuplicateEventID) {
		t.Fatalf("duplicate: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.AppendEvents(ctx, "session", []*adk.SessionEvent[*schema.Message]{event(fmt.Sprint(i), "concurrent")}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	// Reopen the DB connection as a restarted process would; no in-memory state is shared.
	reopened, err := sql.Open("postgres", os.Getenv("IHTML_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := New[*schema.Message](reopened, Config{Namespace: s.namespace})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := restored.LoadEvents(ctx, "session", nil)
	if err != nil || len(loaded.Events) != 9 || loaded.Events[0].Message.Content != "original" {
		t.Fatalf("restart: %#v %v", loaded, err)
	}
	other, _ := New[*schema.Message](reopened, Config{Namespace: s.namespace + "-other"})
	isolated, err := other.LoadEvents(ctx, "session", nil)
	if err != nil || len(isolated.Events) != 0 {
		t.Fatalf("namespace leak: %v", err)
	}
	for _, id := range []string{"session/runner_checkpoint", fmt.Sprintf("ihtml:approval:%x", sha256.Sum256([]byte("session")))} {
		if err := s.Set(ctx, id, []byte{0, 1, 255}); err != nil {
			t.Fatal(err)
		}
		b, ok, err := restored.Get(ctx, id)
		if err != nil || !ok || string(b) != string([]byte{0, 1, 255}) {
			t.Fatalf("checkpoint: %v", err)
		}
		if _, ok, err := other.Get(ctx, id); err != nil || ok {
			t.Fatalf("checkpoint namespace leak: %v", err)
		}
	}
	if err := restored.DeleteSession(ctx, "session"); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.LoadEvents(ctx, "session", nil)
	if err != nil || len(loaded.Events) != 0 {
		t.Fatalf("session not deleted: %v", err)
	}
	if _, ok, err := s.Get(ctx, "session/runner_checkpoint"); err != nil || ok {
		t.Fatalf("checkpoint not deleted: %v", err)
	}
	if err := s.Set(ctx, "empty", nil); err != nil {
		t.Fatal(err)
	}
	if b, ok, err := s.Get(ctx, "empty"); err != nil || !ok || len(b) != 0 {
		t.Fatalf("empty checkpoint: %v", err)
	}
}
