package providerstream

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDecodeProtocolsAndTextOnly(t *testing.T) {
	for _, tc := range []struct{ protocol, wire, contains string }{
		{"responses", "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"private\"}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n", `"status":"completed"`},
		{"chat", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", `"content":"Hello"`},
		{"messages", "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"Hello\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n", `"stop_reason":"end_turn"`},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			last := ""
			checked := 0
			raw, err := Decode(context.Background(), tc.protocol, strings.NewReader(tc.wire), Options{Validate: func([]byte) error { checked++; return nil }, OnDelta: func(s string) error { last = s; return nil }})
			if err != nil || !strings.Contains(string(raw), tc.contains) || last != "Hello" || checked == 0 {
				t.Fatalf("%s %v text=%q checks=%d", raw, err, last, checked)
			}
		})
	}
}
func TestSSEMultilineAndCRLF(t *testing.T) {
	wire := ": comment\r\nevent: response.completed\r\ndata: {\"type\":\"response.completed\",\r\ndata: \"response\":{\"status\":\"completed\"}}\r\n\r\n"
	raw, err := Decode(context.Background(), "responses", strings.NewReader(wire), Options{})
	if err != nil || string(raw) != `{"status":"completed"}` {
		t.Fatal(string(raw), err)
	}
}
func TestTruncationLimitsFailuresAndHooks(t *testing.T) {
	for _, wire := range []string{
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n",
		"data: {\"type\":\"response.completed\",\"response\":{}}",
		"data: [DONE]\n\n",
		"data: {\"type\":\"response.failed\"}\n\n",
		"data: " + strings.Repeat("x", 256*1024) + "\n\n",
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + strings.Repeat("x", 24001) + "\"}\n\n",
	} {
		if _, err := Decode(context.Background(), "responses", strings.NewReader(wire), Options{}); err == nil {
			t.Fatal("invalid stream succeeded")
		}
	}
	denied := errors.New("revoked")
	wire := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"
	calls := 0
	_, err := Decode(context.Background(), "responses", strings.NewReader(wire), Options{Validate: func([]byte) error { return denied }, OnDelta: func(string) error { calls++; return nil }})
	if !errors.Is(err, denied) || calls != 0 {
		t.Fatal(err, calls)
	}
	_, err = Decode(context.Background(), "responses", strings.NewReader(wire), Options{OnDelta: func(string) error { return denied }})
	if !errors.Is(err, denied) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = Decode(ctx, "responses", strings.NewReader(wire), Options{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestToolsRemainDataAndArgumentsNeverText(t *testing.T) {
	wire := "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"read\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
	text := ""
	raw, err := Decode(context.Background(), "chat", strings.NewReader(wire), Options{OnDelta: func(v string) error { text += v; return nil }})
	if err != nil || text != "" || !strings.Contains(string(raw), `"arguments":"{}"`) {
		t.Fatal(string(raw), err, text)
	}
}
