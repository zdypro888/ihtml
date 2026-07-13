package chat

import (
	"fmt"
	"strings"
	"time"

	"github.com/zdypro888/ihtml"
)

// confirmationReferenceValid deliberately accepts either stable identifier.
// Built-in cards carry both, while third-party protocol clients may omit an ID
// and use the signed nonce as their only correlation key.
func confirmationReferenceValid(call *ihtml.ProposedToolCall) bool {
	return call != nil && confirmationCardStructurallySafe(call) &&
		(strings.TrimSpace(call.ID) != "" || strings.TrimSpace(call.ConfirmationNonce) != "")
}

func cloneConfirmationCall(call *ihtml.ProposedToolCall) *ihtml.ProposedToolCall {
	if call == nil {
		return nil
	}
	cloned := *call
	if call.Query != nil {
		cloned.Query = make(map[string]any, len(call.Query))
		for key, value := range call.Query {
			cloned.Query[key] = value
		}
	}
	return &cloned
}

// emitConfirmationFailure terminates one already-started confirmation run and
// marks the exact existing tool card as failed. Returning false means the
// request had no usable card reference, so callers should fall back to a turn
// error rather than creating an unrelated tool card.
func (s *session) emitConfirmationFailure(
	emit func(ihtml.ChatStreamEvent) error,
	ev *ihtml.ChatClientEvent,
	message string,
) bool {
	if emit == nil || ev == nil || !confirmationReferenceValid(ev.ToolCall) {
		return false
	}
	message = sanitizeUserFacingMessage(message, 400)
	if message == "" {
		message = "当前无法执行此操作，操作未执行，请稍后重试。"
	}
	call := cloneConfirmationCall(ev.ToolCall)
	if err := emit(ihtml.ChatStreamEvent{
		Type:        ihtml.ChatStreamToolResult,
		ModelReady:  true,
		ToolCall:    toolEventCallReference(call),
		ToolName:    call.Name,
		ToolTitle:   call.Title,
		ToolStatus:  ihtml.ChatToolError,
		ToolSummary: message,
	}); err != nil {
		return true
	}
	if call.AgentMode == AgentModeDeep {
		_ = emit(ihtml.ChatStreamEvent{
			Type: ihtml.ChatStreamAssistantAgent, ModelReady: true,
			Agent: &ihtml.ChatAgentStage{
				Mode: AgentModeDeep, Agent: "root", TaskID: "root", Phase: "approval", Status: "error",
				Summary: message, ApprovalResume: "manual_tool_only",
			},
		})
	}
	_ = emit(ihtml.ChatStreamEvent{
		Type: ihtml.ChatStreamAssistantDone, ModelReady: true,
		Message: message, ContentFormat: ihtml.ChatContentFormatText,
	})
	return true
}

// rejectConfirmation creates the same short confirmation run used by normal
// manual execution. This lets the browser attach the terminal result to the
// preserved card even when rejection happens before the approval queue starts.
func (s *session) rejectConfirmation(ev *ihtml.ChatClientEvent, message string) {
	if s == nil || s.emit == nil {
		return
	}
	if ev == nil || !confirmationReferenceValid(ev.ToolCall) {
		_ = s.emit(ihtml.ChatStreamEvent{
			Type: ihtml.ChatStreamTurnError, Error: message, ModelReady: true,
		})
		return
	}
	runID := fmt.Sprintf("ai_%d", time.Now().UnixNano())
	emit := s.newEmitter(runID, runID+"_confirm")
	if err := emit(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamTurnStart, ModelReady: true}); err != nil {
		return
	}
	_ = s.emitConfirmationFailure(emit, ev, message)
}
