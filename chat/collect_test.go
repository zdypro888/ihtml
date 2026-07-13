package chat

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/zdypro888/ihtml"
)

func TestCollectEventsSegmentsStreamingAndNonStreamingReasoning(t *testing.T) {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		IsStreaming: true,
		Role:        schema.Assistant,
		MessageStream: schema.StreamReaderFromArray([]*schema.Message{
			{Role: schema.Assistant, ReasoningContent: "先检查"},
			{ReasoningContent: "，再比较", Content: "结论"},
		}),
	}}})
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		Role: schema.Assistant,
		Message: &schema.Message{
			Role:             schema.Assistant,
			ReasoningContent: "第二次分析",
			Content:          "完成",
		},
	}}})
	generator.Close()

	var events []ihtml.ChatStreamEvent
	_, err := collectEvents(context.Background(), iter, Config{Thinking: true}, &timeline{}, func(event ihtml.ChatStreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var statuses, reasoning []ihtml.ChatStreamEvent
	for _, event := range events {
		switch event.Type {
		case ihtml.ChatStreamAssistantStatus:
			statuses = append(statuses, event)
		case ihtml.ChatStreamAssistantThinking:
			reasoning = append(reasoning, event)
		}
	}
	if len(statuses) != 1 || statuses[0].Status != "模型正在分析。" {
		t.Fatalf("thinking statuses = %+v", statuses)
	}
	if len(reasoning) != 4 {
		t.Fatalf("reasoning events = %+v", reasoning)
	}
	if reasoning[0].ThinkingID != "reasoning-1" || reasoning[0].Thinking != "先检查" || reasoning[0].ThinkingDone {
		t.Fatalf("first reasoning chunk = %+v", reasoning[0])
	}
	if reasoning[1].ThinkingID != "reasoning-1" || reasoning[1].Thinking != "，再比较" || reasoning[1].ThinkingDone {
		t.Fatalf("second reasoning chunk = %+v", reasoning[1])
	}
	if reasoning[2].ThinkingID != "reasoning-1" || reasoning[2].Thinking != "" || !reasoning[2].ThinkingDone {
		t.Fatalf("stream completion = %+v", reasoning[2])
	}
	if reasoning[3].ThinkingID != "reasoning-2" || reasoning[3].Thinking != "第二次分析" || !reasoning[3].ThinkingDone {
		t.Fatalf("non-stream reasoning = %+v", reasoning[3])
	}
	if len(events) == 0 || events[0].Type != ihtml.ChatStreamAssistantStatus {
		t.Fatalf("first event must preserve thinking status semantics: %+v", events)
	}
}

func TestCollectEventsIgnoresBlankReasoningFrames(t *testing.T) {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		IsStreaming: true,
		Role:        schema.Assistant,
		MessageStream: schema.StreamReaderFromArray([]*schema.Message{
			{Role: schema.Assistant, ReasoningContent: " \n\t"},
			{Content: "正文"},
		}),
	}}})
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		Role: schema.Assistant,
		Message: &schema.Message{
			Role:             schema.Assistant,
			ReasoningContent: "   ",
		},
	}}})
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		Role: schema.Assistant,
		Message: &schema.Message{
			Role:             schema.Assistant,
			ReasoningContent: "有效分析",
		},
	}}})
	generator.Close()

	var events []ihtml.ChatStreamEvent
	_, err := collectEvents(context.Background(), iter, Config{Thinking: true}, &timeline{}, func(event ihtml.ChatStreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var statuses, reasoning []ihtml.ChatStreamEvent
	for _, event := range events {
		if event.Type == ihtml.ChatStreamAssistantStatus {
			statuses = append(statuses, event)
		}
		if event.Type == ihtml.ChatStreamAssistantThinking {
			reasoning = append(reasoning, event)
		}
	}
	if len(statuses) != 1 {
		t.Fatalf("blank frames emitted statuses: %+v", statuses)
	}
	if len(reasoning) != 1 || reasoning[0].ThinkingID != "reasoning-1" || reasoning[0].Thinking != "有效分析" || !reasoning[0].ThinkingDone {
		t.Fatalf("blank frames created reasoning segments: %+v", reasoning)
	}
}

func TestThinkingFieldsAreBackwardCompatibleJSONAdditions(t *testing.T) {
	legacy, err := json.Marshal(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamAssistantThinking, Thinking: "分析"})
	if err != nil {
		t.Fatal(err)
	}
	var legacyObject map[string]any
	if err := json.Unmarshal(legacy, &legacyObject); err != nil {
		t.Fatal(err)
	}
	if _, ok := legacyObject["thinking_id"]; ok {
		t.Fatalf("zero thinking_id must be omitted: %s", legacy)
	}
	if _, ok := legacyObject["thinking_done"]; ok {
		t.Fatalf("false thinking_done must be omitted: %s", legacy)
	}

	segmented, err := json.Marshal(ihtml.ChatStreamEvent{
		Type:         ihtml.ChatStreamAssistantThinking,
		Thinking:     "分析",
		ThinkingID:   "reasoning-1",
		ThinkingDone: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var segmentedObject map[string]any
	if err := json.Unmarshal(segmented, &segmentedObject); err != nil {
		t.Fatal(err)
	}
	if segmentedObject["thinking_id"] != "reasoning-1" || segmentedObject["thinking_done"] != true {
		t.Fatalf("segmented thinking JSON = %s", segmented)
	}
}

func TestAnswerContentFormatAndResetAreBackwardCompatibleJSONAdditions(t *testing.T) {
	legacy, err := json.Marshal(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamAssistantDelta, Delta: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	var legacyObject map[string]any
	if err := json.Unmarshal(legacy, &legacyObject); err != nil {
		t.Fatal(err)
	}
	if _, ok := legacyObject["content_format"]; ok {
		t.Fatalf("zero content_format must be omitted: %s", legacy)
	}

	rich, err := json.Marshal(ihtml.ChatStreamEvent{
		Type: ihtml.ChatStreamAssistantDone, Message: "<table></table>", ContentFormat: ihtml.ChatContentFormatHTML,
	})
	if err != nil {
		t.Fatal(err)
	}
	var richObject map[string]any
	if err := json.Unmarshal(rich, &richObject); err != nil {
		t.Fatal(err)
	}
	if richObject["content_format"] != ihtml.ChatContentFormatHTML {
		t.Fatalf("rich answer JSON = %s", rich)
	}

	reset, err := json.Marshal(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamAssistantReset})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reset), `"type":"assistant_reset"`) {
		t.Fatalf("reset JSON = %s", reset)
	}
}

func TestCollectEventsPreservesAnswerWhitespaceAndSeparatesMessages(t *testing.T) {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		Role:    schema.Assistant,
		Message: &schema.Message{Role: schema.Assistant, Content: "  first"},
	}}})
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		Role:    schema.Assistant,
		Message: &schema.Message{Role: schema.Assistant, Content: "second  "},
	}}})
	generator.Close()

	var streamed strings.Builder
	resp, err := collectEvents(context.Background(), iter, Config{}, &timeline{}, func(event ihtml.ChatStreamEvent) error {
		if event.Type == ihtml.ChatStreamAssistantDelta {
			streamed.WriteString(event.Delta)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "  first\n\nsecond  "
	if resp.Message != want || streamed.String() != want {
		t.Fatalf("message=%q streamed=%q, want %q", resp.Message, streamed.String(), want)
	}
}

func TestCollectEventsDoesNotProjectStaleToolCallFinishReasonOntoFinalAnswer(t *testing.T) {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		Role: schema.Assistant,
		Message: &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "lookup"}}},
			ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"}},
	}}})
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		Role: schema.Assistant, Message: &schema.Message{Role: schema.Assistant, Content: "<p>final</p>"},
	}}})
	generator.Close()
	resp, err := collectEvents(context.Background(), iter, Config{}, &timeline{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message != "<p>final</p>" || resp.FinishReason != "" || resp.NeedsRepair {
		t.Fatalf("final response kept stale metadata: %#v", resp)
	}
}

func TestCollectEventsAggregatesUsageAcrossModelCallsAndKeepsFinalReason(t *testing.T) {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		Role: schema.Assistant,
		Message: &schema.Message{
			Role:      schema.Assistant,
			ToolCalls: []schema.ToolCall{{ID: "lookup"}},
			ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls", Usage: &schema.TokenUsage{
				PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7,
			}},
		},
	}}})
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		IsStreaming: true,
		Role:        schema.Assistant,
		MessageStream: schema.StreamReaderFromArray([]*schema.Message{
			{Role: schema.Assistant, Content: "完成"},
			{ResponseMeta: &schema.ResponseMeta{FinishReason: "stop", Usage: &schema.TokenUsage{
				PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7,
				PromptTokenDetails:      schema.PromptTokenDetails{CachedTokens: 1},
				CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 2},
			}}},
		}),
	}}})
	generator.Close()

	resp, err := collectEvents(context.Background(), iter, Config{}, &timeline{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message != "完成" || resp.FinishReason != "stop" || resp.NeedsRepair {
		t.Fatalf("response = %#v", resp)
	}
	if resp.Usage == nil || resp.Usage.InputTokens != 8 || resp.Usage.OutputTokens != 6 ||
		resp.Usage.TotalTokens != 14 || resp.Usage.CachedInputTokens != 1 || resp.Usage.ReasoningTokens != 2 {
		t.Fatalf("usage = %#v", resp.Usage)
	}
}

func TestCollectEventsMarksOnlyExplicitTruncationForRepair(t *testing.T) {
	for reason, want := range map[string]bool{
		"length": true, "max_tokens": true, "content_filter": false, "stop": false, "": false,
	} {
		t.Run(orDefault(reason, "empty"), func(t *testing.T) {
			iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
			generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
				Role:    schema.Assistant,
				Message: &schema.Message{Role: schema.Assistant, Content: "partial", ResponseMeta: &schema.ResponseMeta{FinishReason: reason}},
			}}})
			generator.Close()
			resp, err := collectEvents(context.Background(), iter, Config{}, &timeline{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp.NeedsRepair != want {
				t.Fatalf("reason %q NeedsRepair=%v, want %v", reason, resp.NeedsRepair, want)
			}
		})
	}
}

func TestCollectEventsResetsRejectedMidStreamRetry(t *testing.T) {
	rejected, writer := schema.Pipe[*schema.Message](1)
	go func() {
		_ = writer.Send(&schema.Message{Role: schema.Assistant, Content: "partial"}, nil)
		_ = writer.Send(nil, &adk.WillRetryError{ErrStr: "temporary", RetryAttempt: 1})
	}()

	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		IsStreaming: true, Role: schema.Assistant, MessageStream: rejected,
	}}})
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		IsStreaming: true, Role: schema.Assistant,
		MessageStream: schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "good"}}),
	}}})
	generator.Close()

	var events []ihtml.ChatStreamEvent
	resp, err := collectEvents(context.Background(), iter, Config{}, &timeline{}, func(event ihtml.ChatStreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message != "good" {
		t.Fatalf("authoritative message = %q, want successful retry only", resp.Message)
	}
	var sawPartial, sawReset, sawRetryStatus, sawGood bool
	for _, event := range events {
		switch event.Type {
		case ihtml.ChatStreamAssistantDelta:
			sawPartial = sawPartial || event.Delta == "partial"
			sawGood = sawGood || event.Delta == "good"
		case ihtml.ChatStreamAssistantReset:
			sawReset = true
		case ihtml.ChatStreamAssistantStatus:
			sawRetryStatus = strings.Contains(event.Status, "重试")
		}
	}
	if !sawPartial || !sawReset || !sawRetryStatus || !sawGood {
		t.Fatalf("retry projection incomplete: %+v", events)
	}
}

func TestCollectEventsReplaysAcceptedMessagesAfterMidStreamRetry(t *testing.T) {
	rejected, writer := schema.Pipe[*schema.Message](1)
	go func() {
		_ = writer.Send(&schema.Message{Role: schema.Assistant, Content: "partial"}, nil)
		_ = writer.Send(nil, &adk.WillRetryError{ErrStr: "temporary", RetryAttempt: 1})
	}()

	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		Role: schema.Assistant, Message: &schema.Message{Role: schema.Assistant, Content: "first"},
	}}})
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		IsStreaming: true, Role: schema.Assistant, MessageStream: rejected,
	}}})
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		IsStreaming: true, Role: schema.Assistant,
		MessageStream: schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "second"}}),
	}}})
	generator.Close()

	var projected strings.Builder
	resp, err := collectEvents(context.Background(), iter, Config{}, &timeline{}, func(event ihtml.ChatStreamEvent) error {
		switch event.Type {
		case ihtml.ChatStreamAssistantReset:
			projected.Reset()
		case ihtml.ChatStreamAssistantDelta:
			projected.WriteString(event.Delta)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "first\n\nsecond"
	if resp.Message != want || projected.String() != want {
		t.Fatalf("message=%q projected=%q, want %q", resp.Message, projected.String(), want)
	}
	if strings.Contains(resp.Message, "partial") || strings.Contains(projected.String(), "partial") {
		t.Fatalf("rejected content leaked: message=%q projected=%q", resp.Message, projected.String())
	}
}

func TestCollectEventsResetsRejectedAgentEventRetry(t *testing.T) {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		Role: schema.Assistant, Message: &schema.Message{Role: schema.Assistant, Content: "rejected"},
	}}})
	generator.Send(&adk.AgentEvent{Err: &adk.WillRetryError{ErrStr: "temporary", RetryAttempt: 1}})
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		Role: schema.Assistant, Message: &schema.Message{Role: schema.Assistant, Content: "accepted"},
	}}})
	generator.Close()

	var events []ihtml.ChatStreamEvent
	resp, err := collectEvents(context.Background(), iter, Config{}, &timeline{}, func(event ihtml.ChatStreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message != "accepted" {
		t.Fatalf("authoritative message = %q, want accepted retry only", resp.Message)
	}
	var resets int
	for _, event := range events {
		if event.Type == ihtml.ChatStreamAssistantReset {
			resets++
		}
	}
	if resets != 1 {
		t.Fatalf("reset events = %d, events=%+v", resets, events)
	}
}

func TestGenInputRespectsConfiguredStreamingWithTools(t *testing.T) {
	s := &session{}
	result, err := s.genInput(context.Background(), nil, []*turnItem{{
		cfg: Config{Stream: false}, current: schema.UserMessage("hello"), toolsEnabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Input.EnableStreaming {
		t.Fatal("tools_enabled must not override Config.Stream=false")
	}

	result, err = s.genInput(context.Background(), nil, []*turnItem{{
		cfg: Config{Stream: true}, current: schema.UserMessage("hello"), toolsEnabled: false,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Input.EnableStreaming {
		t.Fatal("Config.Stream=true must enable streaming")
	}
}

func TestBuiltInEmitterDefaultsModelAnswersToHTMLAndPreservesExplicitText(t *testing.T) {
	var events []ihtml.ChatStreamEvent
	s := &session{emit: func(event ihtml.ChatStreamEvent) error {
		events = append(events, event)
		return nil
	}}
	emit := s.newEmitter("run", "turn")
	for _, event := range []ihtml.ChatStreamEvent{
		{Type: ihtml.ChatStreamAssistantStatus, Status: "working", ContentFormat: ihtml.ChatContentFormatMarkdown},
		{Type: ihtml.ChatStreamAssistantDelta, Delta: "<p>answer</p>"},
		{Type: ihtml.ChatStreamAssistantDone, Message: "<p>answer</p>", ContentFormat: ihtml.ChatContentFormatText},
	} {
		if err := emit(event); err != nil {
			t.Fatal(err)
		}
	}
	if events[0].ContentFormat != "" || events[1].ContentFormat != ihtml.ChatContentFormatHTML || events[2].ContentFormat != ihtml.ChatContentFormatText {
		t.Fatalf("content formats = %q, %q, %q", events[0].ContentFormat, events[1].ContentFormat, events[2].ContentFormat)
	}
}

func TestCollectorCoalescesTinyChunksAndBoundsDeltaFrames(t *testing.T) {
	chunks := make([]*schema.Message, 4096)
	for index := range chunks {
		chunks[index] = schema.AssistantMessage("x", nil)
	}
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		IsStreaming: true, Role: schema.Assistant, MessageStream: schema.StreamReaderFromArray(chunks),
	}}})
	generator.Close()

	var projected strings.Builder
	deltaEvents := 0
	resp, err := collectEvents(context.Background(), iter, Config{}, &timeline{}, func(event ihtml.ChatStreamEvent) error {
		if event.Type == ihtml.ChatStreamAssistantDelta {
			deltaEvents++
			if len(event.Delta) > modelStreamBatchBytes {
				t.Fatalf("delta bytes = %d, max %d", len(event.Delta), modelStreamBatchBytes)
			}
			projected.WriteString(event.Delta)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("x", len(chunks))
	if resp.Message != want || projected.String() != want {
		t.Fatalf("message/projected lengths = %d/%d, want %d", len(resp.Message), projected.Len(), len(want))
	}
	if deltaEvents > 128 {
		t.Fatalf("4096 one-byte chunks produced %d websocket deltas", deltaEvents)
	}
}

func TestCollectorSplitsOneLargeProviderChunkAtUTF8Boundaries(t *testing.T) {
	want := strings.Repeat("天气好", 4000)
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		IsStreaming: true, Role: schema.Assistant,
		MessageStream: schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage(want, nil)}),
	}}})
	generator.Close()
	var projected strings.Builder
	resp, err := collectEvents(context.Background(), iter, Config{}, &timeline{}, func(event ihtml.ChatStreamEvent) error {
		if event.Type == ihtml.ChatStreamAssistantDelta {
			if len(event.Delta) > modelStreamBatchBytes || !json.Valid([]byte(`"`+event.Delta+`"`)) && !strings.ContainsAny(event.Delta, "\"\\\n\r\t") {
				t.Fatalf("invalid bounded delta: bytes=%d", len(event.Delta))
			}
			if !utf8.ValidString(event.Delta) {
				t.Fatal("delta split a UTF-8 rune")
			}
			projected.WriteString(event.Delta)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message != want || projected.String() != want {
		t.Fatalf("large chunk projection mismatch: %d/%d/%d", len(resp.Message), projected.Len(), len(want))
	}
}

func TestCollectorTruncatesAuthoritativeAnswerOnceAndKeepsProjectionEqual(t *testing.T) {
	want := strings.Repeat("<section>answer & detail</section>", 20000)
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		IsStreaming: true, Role: schema.Assistant,
		MessageStream: schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage(want, nil)}),
	}}})
	generator.Close()
	var projected strings.Builder
	resp, err := collectEvents(context.Background(), iter, Config{MaxAssistantMessageBytes: minMaxAssistantMessageBytes}, &timeline{}, func(event ihtml.ChatStreamEvent) error {
		if event.Type == ihtml.ChatStreamAssistantDelta {
			projected.WriteString(event.Delta)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message != projected.String() {
		t.Fatalf("authoritative/projected mismatch: %d/%d", len(resp.Message), projected.Len())
	}
	if int64(len(resp.Message)) > minMaxAssistantMessageBytes || !strings.HasSuffix(resp.Message, assistantTruncationMarker) {
		t.Fatalf("truncated message bytes=%d suffix=%q", len(resp.Message), resp.Message[max(0, len(resp.Message)-80):])
	}
	if resp.FinishReason != serverOutputLimitFinishReason || resp.NeedsRepair {
		t.Fatalf("truncation metadata = %#v", resp)
	}
	if escaped := jsonEscapedStringBytes(resp.Message); escaped > maxAssistantEscapedMessageBytes {
		t.Fatalf("escaped snapshot bytes = %d", escaped)
	}
}

func TestCollectorBoundsStreamingReasoningPerSegment(t *testing.T) {
	reasoning := strings.Repeat("分析", 20000)
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
		IsStreaming: true, Role: schema.Assistant,
		MessageStream: schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant, ReasoningContent: reasoning, Content: "done",
		}}),
	}}})
	generator.Close()
	var visible strings.Builder
	_, err := collectEvents(context.Background(), iter, Config{Thinking: true}, &timeline{}, func(event ihtml.ChatStreamEvent) error {
		if event.Type == ihtml.ChatStreamAssistantThinking {
			if len(event.Thinking) > modelStreamBatchBytes {
				t.Fatalf("reasoning frame bytes = %d", len(event.Thinking))
			}
			visible.WriteString(event.Thinking)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if int64(visible.Len()) > maxReasoningBytesPerSegment || !strings.HasSuffix(visible.String(), reasoningTruncationMarker) {
		t.Fatalf("reasoning projection bytes=%d suffix=%q", visible.Len(), visible.String()[max(0, visible.Len()-80):])
	}
}

func TestUsageAggregationRejectsNegativeAndSaturatesOverflow(t *testing.T) {
	usage := &ihtml.ChatUsage{TotalTokens: math.MaxInt64 - 1, InputTokens: -9}
	addChatUsage(usage, &ihtml.ChatUsage{TotalTokens: 10, InputTokens: 3, OutputTokens: -2})
	if usage.TotalTokens != math.MaxInt64 || usage.InputTokens != 3 || usage.OutputTokens != 0 {
		t.Fatalf("safe usage aggregation = %#v", usage)
	}
	if got := normalizedUsage(&schema.TokenUsage{PromptTokens: -1, CompletionTokens: -2, TotalTokens: -3}); got == nil || got.InputTokens != 0 || got.OutputTokens != 0 || got.TotalTokens != 0 {
		t.Fatalf("normalized negative usage = %#v", got)
	}
}
