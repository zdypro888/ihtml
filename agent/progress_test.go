package agent

import (
	"context"
	"github.com/zdypro888/ihtml/progress"
	"strings"
	"testing"
)

func TestStatusReporterPreservesCancellationAndBounds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	messages := []string{}
	ctx = WithStatusReporter(ctx, func(text string) { messages = append(messages, text) })
	if !ReportStatus(ctx, "  waiting for existing job  ") || len(messages) != 1 || messages[0] != "waiting for existing job" {
		t.Fatal(messages)
	}
	if ReportStatus(ctx, "") || ReportStatus(ctx, strings.Repeat("x", 1025)) || ReportStatus(nil, "status") || ReportStatus(t.Context(), "status") {
		t.Fatal("unavailable or oversized status accepted")
	}
	cancel()
	if ReportStatus(ctx, "late") || len(messages) != 1 {
		t.Fatal("cancelled tool reported status")
	}
}

func TestStatusReporterCallbackIsolation(t *testing.T) {
	outer, inner := 0, 0
	ctx := WithStatusReporter(t.Context(), func(string) { outer++ })
	ctx = WithStatusReporter(ctx, func(string) { inner++ })
	if !ReportStatus(ctx, "progress") || outer != 0 || inner != 1 {
		t.Fatal("reporters were duplicated")
	}
	ctx = WithStatusReporter(ctx, func(string) { panic("private") })
	if ReportStatus(ctx, "progress") {
		t.Fatal("panic not isolated")
	}
	if WithStatusReporter(nil, func(string) {}) != nil {
		t.Fatal("missing context replaced with background")
	}
}

func TestStructuredProgressUsesCurrentContextAndLegacyFallback(t *testing.T) {
	total := int64(19)
	snapshot := progress.Snapshot{Message: "waiting", State: "waiting", Counters: []progress.Counter{{Label: "processed", Unit: "items", Current: 3, Total: &total}}}
	var typed progress.Snapshot
	plain := 0
	ctx := WithStatusReporter(t.Context(), func(string) { plain++ })
	ctx = WithProgressReporter(ctx, func(s progress.Snapshot) { typed = s })
	if !ReportProgress(ctx, snapshot) || plain != 0 || typed.UpdatedAt.IsZero() || typed.Counters[0].Current != 3 {
		t.Fatal("typed channel missing")
	}
	if !ReportProgress(WithStatusReporter(t.Context(), func(string) { plain++ }), snapshot) || plain != 1 {
		t.Fatal("legacy channel lost")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if ReportProgress(canceled, snapshot) {
		t.Fatal("cancelled report accepted")
	}
}
