package assistant

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

func TestOrdinaryBackendReportsInvalidAndOversizedFramesThenContinues(t *testing.T) {
	var calls atomic.Int32
	engine := &fakeEngine{run: func(context.Context, *agent.TurnRequest) (*agent.TurnResult, error) {
		calls.Add(1)
		return &agent.TurnResult{Text: "done", FinishReason: "stop"}, nil
	}}
	cfg := configFor(engine)
	cfg.MaxEventBytes = 1024
	log := newRecorder()
	s := openSession(t, cfg, "user", log)
	if s.(*session).runner == nil {
		t.Fatal("ordinary Backend does not share SessionRunner")
	}
	send(s, strings.Repeat("x", 2048))
	log.wait(t, ihtml.ChatStreamTurnError, 1)
	invalid := runnerEvent("invalid")
	invalid.ToolCall = &ihtml.ProposedToolCall{Body: make(chan int)}
	s.HandleEvent(t.Context(), invalid)
	log.wait(t, ihtml.ChatStreamTurnError, 2)
	if calls.Load() != 0 {
		t.Fatal("rejected frame reached Engine")
	}
	send(s, "valid")
	log.wait(t, ihtml.ChatStreamAssistantDone, 1)
	if calls.Load() != 1 {
		t.Fatal("valid frame did not run exactly once")
	}
	for _, event := range log.snapshot() {
		if event.Type == ihtml.ChatStreamTurnError && event.Error == "" {
			t.Fatal("rejection has no explanation")
		}
	}
}

func TestOrdinaryBackendFailedRejectionDeliveryAbortsRunner(t *testing.T) {
	outputFailure := errors.New("connection delivery failed")
	cfg := configFor(&fakeEngine{run: func(context.Context, *agent.TurnRequest) (*agent.TurnResult, error) {
		t.Error("oversized frame ran")
		return nil, nil
	}})
	cfg.MaxEventBytes = 1024
	backend, err := NewChecked(cfg)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := backend.NewSession(t.Context(), &ihtml.ChatSessionContext{User: "user", Emit: func(ihtml.ChatStreamEvent) error { return outputFailure }})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	send(opened, strings.Repeat("x", 2048))
	r := opened.(*session).runner
	runnerReceive(t, r.Done())
	if !errors.Is(context.Cause(r.Context()), outputFailure) {
		t.Fatal("failed delivery did not retain its abort cause")
	}
}

func TestOrdinaryBackendInvalidFrameReplyCanAbortAndThenClose(t *testing.T) {
	cfg := configFor(&fakeEngine{run: func(context.Context, *agent.TurnRequest) (*agent.TurnResult, error) {
		t.Error("oversized frame ran")
		return nil, nil
	}})
	cfg.MaxEventBytes = 1024
	backend, err := NewChecked(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var opened ihtml.ChatSession
	reported := make(chan struct{})
	opened, err = backend.NewSession(t.Context(), &ihtml.ChatSessionContext{User: "user", Emit: func(ihtml.ChatStreamEvent) error {
		opened.(*session).runner.Abort(errors.New("host closed output"))
		close(reported)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	go send(opened, strings.Repeat("x", 2048))
	runnerReceive(t, reported)
}

func TestOrdinaryBackendRejectsInvalidFrameByteLimit(t *testing.T) {
	cfg := configFor(&fakeEngine{})
	for _, limit := range []int{-1, 17 << 20} {
		cfg.MaxEventBytes = limit
		if _, err := NewChecked(cfg); err == nil {
			t.Fatal("invalid frame byte limit accepted")
		}
	}
}

func TestOrdinaryBackendCloseWaitsForRejectionDelivery(t *testing.T) {
	cfg := configFor(&fakeEngine{})
	cfg.MaxEventBytes = 1024
	backend, err := NewChecked(cfg)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	opened, err := backend.NewSession(t.Context(), &ihtml.ChatSessionContext{User: "user", Emit: func(ihtml.ChatStreamEvent) error { close(entered); <-release; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	go func() { send(opened, strings.Repeat("x", 2048)); close(returned) }()
	runnerReceive(t, entered)
	closed := make(chan struct{})
	go func() { opened.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close skipped rejection callback")
	default:
	}
	close(release)
	runnerReceive(t, returned)
	runnerReceive(t, closed)
}
