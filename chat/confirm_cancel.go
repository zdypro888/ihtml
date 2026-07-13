package chat

import (
	"context"
	"errors"
	"strings"

	"github.com/zdypro888/ihtml"
)

// cancelConfirmation revokes one signed card without entering the manual tool
// execution lane. It is deliberately synchronous: websocket frames are
// handled in order, so once a cancel_tool frame returns, a later confirm_tool
// replay on the same connection can no longer win the nonce CAS.
//
// Every request receives an explicit cancel_tool_result acknowledgement. The
// UI must keep the card pending until that acknowledgement succeeds: a failed
// revocation can mean confirmation already won the nonce race.
func (s *session) cancelConfirmation(ctx context.Context, ev *ihtml.ChatClientEvent) (bool, error) {
	if s == nil || s.backend == nil || s.backend.signer == nil {
		return false, errors.New("confirmation signer is unavailable")
	}
	if ev == nil || ev.ToolCall == nil {
		return false, nil
	}
	if !confirmationCardStructurallySafe(ev.ToolCall) {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	scope := newConfirmationScope(s.sessionID, s.capabilityScope)
	return s.backend.signer.revokeContext(ctx, s.user, scope, *ev.ToolCall)
}

const maxConfirmationReferenceBytes = 256

func boundedConfirmationReference(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > maxConfirmationReferenceBytes {
		value = value[:maxConfirmationReferenceBytes]
	}
	return value
}

func cancellationReference(ev *ihtml.ChatClientEvent) (toolID, nonce string) {
	if ev == nil || ev.ToolCall == nil {
		return "", ""
	}
	return boundedConfirmationReference(ev.ToolCall.ID), boundedConfirmationReference(ev.ToolCall.ConfirmationNonce)
}

func (s *session) emitCancellationFailure(ev *ihtml.ChatClientEvent, message string) {
	toolID, nonce := cancellationReference(ev)
	if strings.TrimSpace(message) == "" {
		message = "暂时无法撤销此操作，执行状态未确认。"
	}
	if s != nil && s.emit != nil {
		_ = s.emit(ihtml.ChatStreamEvent{
			Type: ihtml.ChatStreamCancelToolResult, ToolID: toolID, ConfirmationNonce: nonce,
			ToolStatus: ihtml.ChatToolError, Error: message, ModelReady: true,
		})
	}
}

// cancelConfirmationWithAck performs the nonce CAS and emits a correlated,
// user-safe result. false is intentionally reported as a failure rather than
// "cancelled": the card may already have been confirmed on another connection.
func (s *session) cancelConfirmationWithAck(ctx context.Context, ev *ihtml.ChatClientEvent) {
	toolID, nonce := cancellationReference(ev)
	result := ihtml.ChatStreamEvent{
		Type:              ihtml.ChatStreamCancelToolResult,
		ToolID:            toolID,
		ConfirmationNonce: nonce,
		ModelReady:        true,
	}
	ok, err := runHostValueUntilReturn(ctx, s.backend.hostCallbacks, hostInterceptorTimeout, func(callCtx context.Context) (bool, error) {
		return s.cancelConfirmation(callCtx, ev)
	})
	switch {
	case err != nil:
		result.ToolStatus = ihtml.ChatToolError
		result.Error = "暂时无法撤销此操作，执行状态未确认。"
	case !ok:
		result.ToolStatus = ihtml.ChatToolError
		result.Error = "此操作已失效、已取消或已开始执行，无法确认撤销。"
	default:
		result.ToolStatus = ihtml.ChatToolCancelled
		result.ToolSummary = "已撤销，操作未执行。"
	}
	if s != nil && s.emit != nil {
		_ = s.emit(result)
	}
}
