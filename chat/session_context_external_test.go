package chat_test

import (
	"context"
	"testing"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/chat"
)

// Embedding the SDK interface makes this pointer implement every Service
// method while preserving the classic typed-nil panic hazard.
type typedNilService struct{ ihtml.Service }

type typedNilScopedService struct{ ihtml.ScopedService }

func TestNewSessionRejectsTypedNilServiceDependencies(t *testing.T) {
	backend := chat.New(chat.Config{})
	var service *typedNilService
	if session, err := backend.NewSession(context.Background(), &ihtml.ChatSessionContext{
		User: "alice", Service: service, Emit: func(ihtml.ChatStreamEvent) error { return nil },
	}); err == nil || session != nil {
		t.Fatalf("typed-nil Service created session=%T err=%v", session, err)
	}

	h := ihtml.New(ihtml.NewMemoryStore())
	defer h.Close()
	var scoped *typedNilScopedService
	if session, err := backend.NewSession(context.Background(), &ihtml.ChatSessionContext{
		User: "alice", Service: h, ScopedService: scoped,
		Emit: func(ihtml.ChatStreamEvent) error { return nil },
	}); err == nil || session != nil {
		t.Fatalf("typed-nil ScopedService created session=%T err=%v", session, err)
	}
}
