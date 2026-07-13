package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/zdypro888/ihtml"
)

type collectResp struct {
	Message       string
	Streamed      bool
	ContentFormat string
	ToolCalls     []ihtml.ProposedToolCall
	FinishReason  string
	Usage         *ihtml.ChatUsage
	NeedsRepair   bool
	Repaired      bool
}

type streamCollectResult struct {
	Message           string
	Streamed          bool
	Retry             bool
	HadVisibleContent bool
	FinishReason      string
	Usage             *ihtml.ChatUsage
}

const (
	assistantMessageSeparator             = "\n\n"
	assistantTruncationMarker             = "\n\n[回答过长，已在服务端安全截断。]"
	reasoningTruncationMarker             = "\n[分析过程过长，已截断。]"
	serverOutputLimitFinishReason         = "server_output_limit"
	maxAssistantEscapedMessageBytes int64 = 700 << 10
	modelStreamBatchBytes                 = 4 << 10
	modelStreamBatchInterval              = 40 * time.Millisecond
)

// thinkingState tracks reasoning segments within one model turn. A segment is
// allocated lazily for each MessageOutput that actually contains reasoning, so
// empty provider frames do not create phantom steps in the client timeline.
type thinkingState struct {
	sawThinking bool
	segments    int
}

func (s *thinkingState) nextSegmentID() string {
	s.segments++
	return fmt.Sprintf("reasoning-%d", s.segments)
}

type assistantProjectionCheckpoint struct {
	length       int
	escapedBytes int64
	truncated    bool
}

// assistantProjection is the single authoritative answer buffer used by both
// streamed deltas and assistant_done. It enforces a raw UTF-8 limit and a
// conservative encoding/json string limit, so HTML escaping cannot turn a
// bounded answer into an oversized WebSocket frame.
type assistantProjection struct {
	content      []byte
	maxContent   int64
	maxEscaped   int64
	escapedBytes int64
	truncated    bool
}

func newAssistantProjection(cfg Config) *assistantProjection {
	limit := cfg.MaxAssistantMessageBytes
	if limit < minMaxAssistantMessageBytes || limit > maxMaxAssistantMessageBytes {
		limit = defaultMaxAssistantMessageBytes
	}
	return &assistantProjection{
		maxContent: limit - int64(len(assistantTruncationMarker)),
		maxEscaped: maxAssistantEscapedMessageBytes - jsonEscapedStringBytes(assistantTruncationMarker),
	}
}

func (p *assistantProjection) checkpoint() assistantProjectionCheckpoint {
	if p == nil {
		return assistantProjectionCheckpoint{}
	}
	return assistantProjectionCheckpoint{length: len(p.content), escapedBytes: p.escapedBytes, truncated: p.truncated}
}

func (p *assistantProjection) rollback(checkpoint assistantProjectionCheckpoint) {
	if p == nil {
		return
	}
	if checkpoint.length < 0 || checkpoint.length > len(p.content) {
		checkpoint = assistantProjectionCheckpoint{}
	}
	p.content = p.content[:checkpoint.length]
	p.escapedBytes = checkpoint.escapedBytes
	p.truncated = checkpoint.truncated
}

func (p *assistantProjection) reset() {
	if p == nil {
		return
	}
	p.content = p.content[:0]
	p.escapedBytes = 0
	p.truncated = false
}

func (p *assistantProjection) message() string {
	if p == nil {
		return ""
	}
	return string(p.content)
}

func (p *assistantProjection) hasContent() bool { return p != nil && len(p.content) > 0 }

func (p *assistantProjection) append(value string) string {
	if p == nil || value == "" || p.truncated {
		return ""
	}
	rawRemaining := p.maxContent - int64(len(p.content))
	escapedRemaining := p.maxEscaped - p.escapedBytes
	valueEscaped := jsonEscapedStringBytes(value)
	if int64(len(value)) <= rawRemaining && valueEscaped <= escapedRemaining {
		p.content = append(p.content, value...)
		p.escapedBytes += valueEscaped
		return value
	}
	prefix := jsonStringPrefix(value, rawRemaining, escapedRemaining)
	p.content = append(p.content, prefix...)
	p.escapedBytes += jsonEscapedStringBytes(prefix)
	p.content = append(p.content, assistantTruncationMarker...)
	p.escapedBytes += jsonEscapedStringBytes(assistantTruncationMarker)
	p.truncated = true
	return prefix + assistantTruncationMarker
}

func jsonEscapedStringBytes(value string) int64 {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) < 2 {
		return math.MaxInt64
	}
	return int64(len(raw) - 2)
}

func jsonStringPrefix(value string, rawLimit, escapedLimit int64) string {
	if value == "" || rawLimit <= 0 || escapedLimit <= 0 {
		return ""
	}
	low, high := 0, len(value)
	for low < high {
		mid := low + (high-low+1)/2
		for mid > 0 && mid < len(value) && !utf8.RuneStart(value[mid]) {
			mid--
		}
		if mid == low && mid < high {
			_, size := utf8.DecodeRuneInString(value[mid:])
			mid += size
			if mid > high {
				mid = high
			}
		}
		if int64(mid) <= rawLimit && jsonEscapedStringBytes(value[:mid]) <= escapedLimit {
			low = mid
		} else {
			high = mid - 1
			for high > 0 && high < len(value) && !utf8.RuneStart(value[high]) {
				high--
			}
		}
	}
	for low > 0 && low < len(value) && !utf8.RuneStart(value[low]) {
		low--
	}
	return value[:low]
}

type reasoningProjection struct {
	maxTotal   int64
	maxSegment int64
	total      int64
	segments   map[string]int64
	truncated  map[string]bool
}

func newReasoningProjection(cfg Config) *reasoningProjection {
	limit := cfg.MaxReasoningBytesPerTurn
	if limit < minMaxReasoningBytesPerTurn || limit > maxMaxReasoningBytesPerTurn {
		limit = defaultMaxReasoningBytesPerTurn
	}
	segment := min(limit, maxReasoningBytesPerSegment)
	return &reasoningProjection{maxTotal: limit, maxSegment: segment, segments: make(map[string]int64), truncated: make(map[string]bool)}
}

func (p *reasoningProjection) append(segmentID, value string) string {
	if p == nil || value == "" || p.truncated[segmentID] {
		return ""
	}
	remaining := min(p.maxTotal-p.total, p.maxSegment-p.segments[segmentID])
	if remaining <= 0 {
		p.truncated[segmentID] = true
		return ""
	}
	if int64(len(value)) <= remaining {
		p.total += int64(len(value))
		p.segments[segmentID] += int64(len(value))
		return value
	}
	prefixLimit := remaining - int64(len(reasoningTruncationMarker))
	prefix := utf8Prefix(value, prefixLimit)
	accepted := prefix
	if int64(len(reasoningTruncationMarker)) <= remaining-int64(len(prefix)) {
		accepted += reasoningTruncationMarker
	}
	p.total += int64(len(accepted))
	p.segments[segmentID] += int64(len(accepted))
	p.truncated[segmentID] = true
	return accepted
}

func utf8Prefix(value string, limit int64) string {
	if limit <= 0 || value == "" {
		return ""
	}
	if int64(len(value)) <= limit {
		return value
	}
	end := int(limit)
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}

type modelStreamBatcher struct {
	emit          func(ihtml.ChatStreamEvent) error
	thinking      *thinkingState
	contentFormat string
	toolCalls     []ihtml.ProposedToolCall
	toolCallsSent bool
	kind          string
	segmentID     string
	pending       strings.Builder
	lastFlush     time.Time
	emittedAny    bool
}

func (b *modelStreamBatcher) appendContent(value string) error {
	return b.append(ihtml.ChatStreamAssistantDelta, "", value)
}

func (b *modelStreamBatcher) appendThinking(segmentID, value string) error {
	return b.append(ihtml.ChatStreamAssistantThinking, segmentID, value)
}

func (b *modelStreamBatcher) append(kind, segmentID, value string) error {
	if b == nil || b.emit == nil || value == "" {
		return nil
	}
	if b.kind != "" && (b.kind != kind || b.segmentID != segmentID) {
		if err := b.flush(); err != nil {
			return err
		}
	}
	b.kind, b.segmentID = kind, segmentID
	for value != "" {
		room := modelStreamBatchBytes - b.pending.Len()
		part := utf8Prefix(value, int64(room))
		if part == "" {
			if err := b.flush(); err != nil {
				return err
			}
			continue
		}
		b.pending.WriteString(part)
		value = value[len(part):]
		if b.pending.Len() >= modelStreamBatchBytes || !b.emittedAny || (!b.lastFlush.IsZero() && time.Since(b.lastFlush) >= modelStreamBatchInterval) {
			if err := b.flush(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *modelStreamBatcher) flush() error {
	if b == nil || b.pending.Len() == 0 || b.emit == nil {
		return nil
	}
	value := b.pending.String()
	b.pending.Reset()
	var err error
	if b.kind == ihtml.ChatStreamAssistantThinking {
		err = emitThinking(b.emit, value, b.segmentID, false, b.thinking)
	} else {
		event := ihtml.ChatStreamEvent{Type: ihtml.ChatStreamAssistantDelta, Delta: value, ContentFormat: b.contentFormat, ModelReady: true}
		if !b.toolCallsSent && len(b.toolCalls) > 0 {
			event.ToolCalls = b.toolCalls
			b.toolCallsSent = true
		}
		err = b.emit(event)
	}
	if err == nil {
		b.lastFlush = time.Now()
		b.emittedAny = true
	}
	return err
}

func emitBatchedAssistantDelta(emit func(ihtml.ChatStreamEvent) error, thinking *thinkingState, value string) error {
	return emitBatchedAssistantDeltaFormat(emit, thinking, value, "")
}

func emitBatchedAssistantDeltaFormat(emit func(ihtml.ChatStreamEvent) error, thinking *thinkingState, value, contentFormat string) error {
	return emitBatchedAssistantDeltaEvent(emit, thinking, value, contentFormat, nil)
}

func emitBatchedAssistantDeltaEvent(emit func(ihtml.ChatStreamEvent) error, thinking *thinkingState, value, contentFormat string, toolCalls []ihtml.ProposedToolCall) error {
	if emit == nil || value == "" {
		return nil
	}
	batcher := &modelStreamBatcher{emit: emit, thinking: thinking, contentFormat: contentFormat, toolCalls: toolCalls}
	if err := batcher.appendContent(value); err != nil {
		return err
	}
	return batcher.flush()
}

// collectEvents 消费一轮 agent 的事件流: 思考/正文增量转成 WS 事件,
// 工具事件由工具自身经 timeline 发出, 这里只管文本。
func collectEvents(ctx context.Context, iter *adk.AsyncIterator[*adk.AgentEvent], cfg Config, tl *timeline, emit func(ihtml.ChatStreamEvent) error) (collectResp, error) {
	projection := newAssistantProjection(cfg)
	reasoning := newReasoningProjection(cfg)
	streamed := false
	thinking := &thinkingState{}
	var usage ihtml.ChatUsage
	hasUsage := false
	finishReason := ""
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
			// errors.As 而非 1.26 的 errors.AsType: SDK 最低 Go 版本保持 1.25。
			var willRetry *adk.WillRetryError
			if errors.As(event.Err, &willRetry) {
				// Some providers surface a retry on the enclosing AgentEvent rather
				// than the MessageStream. Any answer projected before that boundary
				// belongs to the rejected attempt and must not enter the final snapshot.
				projection.reset()
				streamed = false
				if err := emitAssistantReset(emit); err != nil {
					return collectResp{}, err
				}
				if err := emitRetryStatus(emit); err != nil {
					return collectResp{}, err
				}
				continue
			}
			return collectResp{}, event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		separate := projection.hasContent()
		checkpoint := projection.checkpoint()
		result, err := collectMessageOutput(ctx, event.Output.MessageOutput, cfg, emit, thinking, reasoning, projection, separate)
		if err != nil {
			return collectResp{}, err
		}
		if result.Retry {
			projection.rollback(checkpoint)
			// streamModelMessage has reset the rejected projection. Preserve and
			// immediately replay any earlier accepted messages in this same turn so
			// the browser projection stays identical to assistant_done.message.
			if emit != nil && projection.hasContent() {
				if err := emitBatchedAssistantDelta(emit, thinking, projection.message()); err != nil {
					return collectResp{}, err
				}
			}
			continue
		}
		if result.Usage != nil {
			addChatUsage(&usage, result.Usage)
			hasUsage = true
		}
		hasVisibleMessage := result.HadVisibleContent
		if hasVisibleMessage {
			// Metadata from an earlier tool-selection call does not describe the
			// later visible answer. An answer without finish metadata therefore
			// clears stale "tool_calls" rather than reporting it to SDK consumers.
			finishReason = result.FinishReason
		} else if result.FinishReason != "" && (!projection.hasContent() || isTruncatedFinishReason(result.FinishReason)) {
			finishReason = result.FinishReason
		}
		if result.Streamed {
			streamed = true
		}
	}
	message := projection.message()
	if strings.TrimSpace(message) == "" {
		message = ""
	}
	if message == "" && tl.completedSummary() != "" {
		accepted := projection.append(tl.completedSummary())
		message = projection.message()
		if emit != nil {
			if err := emitBatchedAssistantDeltaFormat(emit, thinking, accepted, ihtml.ChatContentFormatText); err != nil {
				return collectResp{}, err
			}
		}
		streamed = true
		return collectResp{
			Message: message, Streamed: streamed, ContentFormat: ihtml.ChatContentFormatText,
			ToolCalls: tl.pendingCalls(), FinishReason: finishReason,
			Usage: usagePointer(usage, hasUsage), NeedsRepair: true,
		}, nil
	}
	if projection.truncated {
		finishReason = serverOutputLimitFinishReason
	}
	return collectResp{
		Message: message, Streamed: streamed, ToolCalls: tl.pendingCalls(),
		FinishReason: finishReason, Usage: usagePointer(usage, hasUsage),
		NeedsRepair: !projection.truncated && isTruncatedFinishReason(finishReason),
	}, nil
}

func collectMessageOutput(ctx context.Context, mv *adk.MessageVariant, cfg Config, emit func(ihtml.ChatStreamEvent) error, thinking *thinkingState, reasoning *reasoningProjection, projection *assistantProjection, separate bool) (streamCollectResult, error) {
	if mv == nil || mv.Role == schema.Tool {
		return streamCollectResult{}, nil
	}
	if mv.IsStreaming {
		return streamModelMessage(ctx, mv.MessageStream, cfg, emit, thinking, reasoning, projection, separate)
	}
	if mv.Message == nil || mv.Message.Role == schema.Tool || mv.Message.ToolCallID != "" {
		return streamCollectResult{}, nil
	}
	result := responseMetadata(mv.Message)
	if len(mv.Message.ToolCalls) > 0 {
		return result, nil
	}
	if cfg.Thinking && strings.TrimSpace(mv.Message.ReasoningContent) != "" && emit != nil {
		segmentID := thinking.nextSegmentID()
		accepted := reasoning.append(segmentID, mv.Message.ReasoningContent)
		if accepted != "" {
			if err := emitThinking(emit, accepted, segmentID, true, thinking); err != nil {
				return streamCollectResult{}, err
			}
		}
	}
	if strings.TrimSpace(mv.Message.Content) == "" {
		return result, nil
	}
	result.HadVisibleContent = true
	value := mv.Message.Content
	if separate {
		value = assistantMessageSeparator + value
	}
	accepted := projection.append(value)
	if emit != nil {
		if err := emitBatchedAssistantDelta(emit, thinking, accepted); err != nil {
			return streamCollectResult{}, err
		}
	}
	result.Message = accepted
	result.Streamed = emit != nil
	return result, nil
}

func streamModelMessage(ctx context.Context, stream *schema.StreamReader[*schema.Message], cfg Config, emit func(ihtml.ChatStreamEvent) error, thinking *thinkingState, reasoning *reasoningProjection, projection *assistantProjection, separate bool) (streamCollectResult, error) {
	if stream == nil {
		return streamCollectResult{}, nil
	}
	defer stream.Close()
	var out streamCollectResult
	var content strings.Builder
	batcher := &modelStreamBatcher{emit: emit, thinking: thinking}
	streamRole := schema.RoleType("")
	segmentID := ""
	segmentVisible := false
	separatorEmitted := false
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
			var willRetry *adk.WillRetryError
			if errors.As(recvErr, &willRetry) {
				if err := batcher.flush(); err != nil {
					return out, err
				}
				if segmentID != "" && segmentVisible && emit != nil {
					if err := emitThinking(emit, "", segmentID, true, thinking); err != nil {
						return out, err
					}
				}
				if err := emitAssistantReset(emit); err != nil {
					return out, err
				}
				if err := emitRetryStatus(emit); err != nil {
					return out, err
				}
				// Chunks from a rejected attempt were already projected to the
				// browser, but must never enter the authoritative done snapshot.
				// Existing clients replace that transient text from assistant_done.
				return streamCollectResult{Retry: true}, nil
			}
			return out, recvErr
		}
		if chunk == nil {
			continue
		}
		mergeStreamMetadata(&out, chunk)
		if chunk.Role != "" {
			streamRole = chunk.Role
		}
		if streamRole == schema.Tool || chunk.ToolCallID != "" {
			continue
		}
		if cfg.Thinking && strings.TrimSpace(chunk.ReasoningContent) != "" && emit != nil {
			if segmentID == "" {
				segmentID = thinking.nextSegmentID()
			}
			accepted := reasoning.append(segmentID, chunk.ReasoningContent)
			segmentVisible = segmentVisible || accepted != ""
			if err := batcher.appendThinking(segmentID, accepted); err != nil {
				return out, err
			}
		}
		if len(chunk.ToolCalls) > 0 {
			continue
		}
		if chunk.Content == "" {
			continue
		}
		out.HadVisibleContent = true
		value := chunk.Content
		if separate && !separatorEmitted {
			value = assistantMessageSeparator + value
			separatorEmitted = true
		}
		accepted := projection.append(value)
		content.WriteString(accepted)
		if emit != nil {
			if err := batcher.appendContent(accepted); err != nil {
				return out, err
			}
		}
		out.Streamed = emit != nil
	}
	if err := batcher.flush(); err != nil {
		return out, err
	}
	if segmentID != "" && segmentVisible {
		if err := emitThinking(emit, "", segmentID, true, thinking); err != nil {
			return out, err
		}
	}
	out.Message = content.String()
	if strings.TrimSpace(out.Message) == "" {
		out.Message = ""
	}
	return out, nil
}

func responseMetadata(msg *schema.Message) streamCollectResult {
	if msg == nil || msg.ResponseMeta == nil {
		return streamCollectResult{}
	}
	return streamCollectResult{
		FinishReason: strings.TrimSpace(msg.ResponseMeta.FinishReason),
		Usage:        normalizedUsage(msg.ResponseMeta.Usage),
	}
}

// Streaming providers normally send cumulative usage in the final chunk. Match
// schema.ConcatMessages semantics and retain the maximum observed counter so a
// provider that repeats metadata on multiple chunks is never double-counted.
func mergeStreamMetadata(out *streamCollectResult, msg *schema.Message) {
	if out == nil || msg == nil || msg.ResponseMeta == nil {
		return
	}
	if reason := strings.TrimSpace(msg.ResponseMeta.FinishReason); reason != "" {
		out.FinishReason = reason
	}
	usage := normalizedUsage(msg.ResponseMeta.Usage)
	if usage == nil {
		return
	}
	if out.Usage == nil {
		out.Usage = usage
		return
	}
	out.Usage.InputTokens = max(out.Usage.InputTokens, usage.InputTokens)
	out.Usage.OutputTokens = max(out.Usage.OutputTokens, usage.OutputTokens)
	out.Usage.TotalTokens = max(out.Usage.TotalTokens, usage.TotalTokens)
	out.Usage.CachedInputTokens = max(out.Usage.CachedInputTokens, usage.CachedInputTokens)
	out.Usage.ReasoningTokens = max(out.Usage.ReasoningTokens, usage.ReasoningTokens)
}

func normalizedUsage(usage *schema.TokenUsage) *ihtml.ChatUsage {
	if usage == nil {
		return nil
	}
	return &ihtml.ChatUsage{
		InputTokens:       nonNegativeTokenCount(usage.PromptTokens),
		OutputTokens:      nonNegativeTokenCount(usage.CompletionTokens),
		TotalTokens:       nonNegativeTokenCount(usage.TotalTokens),
		CachedInputTokens: nonNegativeTokenCount(usage.PromptTokenDetails.CachedTokens),
		ReasoningTokens:   nonNegativeTokenCount(usage.CompletionTokensDetails.ReasoningTokens),
	}
}

func nonNegativeTokenCount(value int) int64 {
	if value <= 0 {
		return 0
	}
	return int64(value)
}

func saturatingTokenAdd(left, right int64) int64 {
	if left < 0 {
		left = 0
	}
	if right <= 0 {
		return left
	}
	if left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}

func addChatUsage(dst, src *ihtml.ChatUsage) {
	if dst == nil || src == nil {
		return
	}
	dst.InputTokens = saturatingTokenAdd(dst.InputTokens, src.InputTokens)
	dst.OutputTokens = saturatingTokenAdd(dst.OutputTokens, src.OutputTokens)
	dst.TotalTokens = saturatingTokenAdd(dst.TotalTokens, src.TotalTokens)
	dst.CachedInputTokens = saturatingTokenAdd(dst.CachedInputTokens, src.CachedInputTokens)
	dst.ReasoningTokens = saturatingTokenAdd(dst.ReasoningTokens, src.ReasoningTokens)
}

func usagePointer(usage ihtml.ChatUsage, present bool) *ihtml.ChatUsage {
	if !present {
		return nil
	}
	copy := usage
	return &copy
}

func isTruncatedFinishReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max_output_tokens", "max_completion_tokens":
		return true
	default:
		return false
	}
}

func emitRetryStatus(emit func(ihtml.ChatStreamEvent) error) error {
	if emit == nil {
		return nil
	}
	return emit(ihtml.ChatStreamEvent{Type: "assistant_status", Status: "模型连接中断, 正在重试…", ModelReady: true})
}

func emitAssistantReset(emit func(ihtml.ChatStreamEvent) error) error {
	if emit == nil {
		return nil
	}
	return emit(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamAssistantReset, ModelReady: true})
}

func emitThinking(emit func(ihtml.ChatStreamEvent) error, content, segmentID string, done bool, thinking *thinkingState) error {
	if !thinking.sawThinking {
		if err := emit(ihtml.ChatStreamEvent{Type: "assistant_status", Status: "模型正在分析。", ModelReady: true}); err != nil {
			return err
		}
		thinking.sawThinking = true
	}
	return emit(ihtml.ChatStreamEvent{
		Type:         "assistant_thinking",
		Thinking:     content,
		ThinkingID:   segmentID,
		ThinkingDone: done,
		ModelReady:   true,
	})
}
