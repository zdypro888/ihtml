package chat

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/zdypro888/ihtml"
)

type collectResp struct {
	Message   string
	Streamed  bool
	ToolCalls []ihtml.ProposedToolCall
}

type streamCollectResult struct {
	Message  string
	Streamed bool
}

// collectEvents 消费一轮 agent 的事件流: 思考/正文增量转成 WS 事件,
// 工具事件由工具自身经 timeline 发出, 这里只管文本。
func collectEvents(ctx context.Context, iter *adk.AsyncIterator[*adk.AgentEvent], cfg Config, tl *timeline, emit func(ihtml.ChatStreamEvent) error) (collectResp, error) {
	var content strings.Builder
	streamed := false
	sawThinking := false
	for {
		select {
		case <-ctx.Done():
			return collectResp{}, ctx.Err()
		default:
		}
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			if _, ok := errors.AsType[*adk.WillRetryError](event.Err); ok {
				if emit != nil {
					_ = emit(ihtml.ChatStreamEvent{Type: "assistant_status", Status: "模型连接中断, 正在重试…", ModelReady: true})
				}
				continue
			}
			return collectResp{}, event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		result, err := collectMessageOutput(ctx, event.Output.MessageOutput, cfg, emit, &sawThinking)
		if err != nil {
			return collectResp{}, err
		}
		if result.Message != "" {
			content.WriteString(result.Message)
		}
		if result.Streamed {
			streamed = true
		}
	}
	message := strings.TrimSpace(content.String())
	if message == "" && tl.completedSummary() != "" {
		message = tl.completedSummary()
		if emit != nil {
			if err := emit(ihtml.ChatStreamEvent{Type: "assistant_delta", Delta: message, ModelReady: true}); err != nil {
				return collectResp{}, err
			}
		}
		streamed = true
	}
	return collectResp{Message: message, Streamed: streamed, ToolCalls: tl.pendingCalls()}, nil
}

func collectMessageOutput(ctx context.Context, mv *adk.MessageVariant, cfg Config, emit func(ihtml.ChatStreamEvent) error, sawThinking *bool) (streamCollectResult, error) {
	if mv == nil || mv.Role == schema.Tool {
		return streamCollectResult{}, nil
	}
	if mv.IsStreaming {
		return streamModelMessage(ctx, mv.MessageStream, cfg, emit, sawThinking)
	}
	if mv.Message == nil || mv.Message.Role == schema.Tool || mv.Message.ToolCallID != "" || len(mv.Message.ToolCalls) > 0 {
		return streamCollectResult{}, nil
	}
	if cfg.Thinking && strings.TrimSpace(mv.Message.ReasoningContent) != "" && emit != nil {
		if err := emitThinking(emit, mv.Message.ReasoningContent, sawThinking); err != nil {
			return streamCollectResult{}, err
		}
	}
	content := strings.TrimSpace(mv.Message.Content)
	if content == "" {
		return streamCollectResult{}, nil
	}
	if emit != nil {
		if err := emit(ihtml.ChatStreamEvent{Type: "assistant_delta", Delta: mv.Message.Content, ModelReady: true}); err != nil {
			return streamCollectResult{}, err
		}
	}
	return streamCollectResult{Message: mv.Message.Content, Streamed: emit != nil}, nil
}

func streamModelMessage(ctx context.Context, stream *schema.StreamReader[*schema.Message], cfg Config, emit func(ihtml.ChatStreamEvent) error, sawThinking *bool) (streamCollectResult, error) {
	if stream == nil {
		return streamCollectResult{}, nil
	}
	defer stream.Close()
	var out streamCollectResult
	var content strings.Builder
	streamRole := schema.RoleType("")
	for {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		default:
		}
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return out, recvErr
		}
		if chunk == nil {
			continue
		}
		if chunk.Role != "" {
			streamRole = chunk.Role
		}
		if streamRole == schema.Tool || chunk.ToolCallID != "" {
			continue
		}
		if cfg.Thinking && strings.TrimSpace(chunk.ReasoningContent) != "" && emit != nil {
			if err := emitThinking(emit, chunk.ReasoningContent, sawThinking); err != nil {
				return out, err
			}
		}
		if len(chunk.ToolCalls) > 0 {
			continue
		}
		if chunk.Content == "" {
			continue
		}
		content.WriteString(chunk.Content)
		if emit != nil {
			if err := emit(ihtml.ChatStreamEvent{Type: "assistant_delta", Delta: chunk.Content, ModelReady: true}); err != nil {
				return out, err
			}
		}
		out.Streamed = emit != nil
	}
	out.Message = strings.TrimSpace(content.String())
	return out, nil
}

func emitThinking(emit func(ihtml.ChatStreamEvent) error, thinking string, sawThinking *bool) error {
	if !*sawThinking {
		if err := emit(ihtml.ChatStreamEvent{Type: "assistant_status", Status: "模型正在分析。", ModelReady: true}); err != nil {
			return err
		}
		*sawThinking = true
	}
	return emit(ihtml.ChatStreamEvent{Type: "assistant_thinking", Thinking: thinking, ModelReady: true})
}
