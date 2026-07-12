package chat

import (
	"context"
	"encoding/json"
	"testing"

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
