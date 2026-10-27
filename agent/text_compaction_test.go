package agent

import (
	"context"
	"strings"
	"testing"
)

func TestCompactTextHistoryKeepsPairsAndRequiresBoundedSummary(t *testing.T) {
	history := []Message{}
	for i := 0; i < 10; i++ {
		history = append(history, Message{Role: RoleUser, Content: strings.Repeat("u", 1000)}, Message{Role: RoleAssistant, Content: "receipt pending, not completed"})
	}
	calls := 0
	r, e := CompactTextHistory(context.Background(), "", history, func(_ context.Context, old string, part []Message) (string, error) {
		calls++
		if len(part)%2 != 0 {
			t.Fatal("split pair")
		}
		return "untrusted summary: pending receipt", nil
	})
	if e != nil || calls == 0 || r.Removed == 0 || len(r.History) > 14 || r.Summary == "" {
		t.Fatal(r, e, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = CompactTextHistory(ctx, "", history, func(context.Context, string, []Message) (string, error) {
		t.Fatal("called after cancellation")
		return "", nil
	}); e == nil {
		t.Fatal("cancel ignored")
	}
	if _, e = CompactTextHistory(context.Background(), "", history, func(context.Context, string, []Message) (string, error) { return strings.Repeat("x", 6001), nil }); e == nil {
		t.Fatal("unbounded summary")
	}
}
