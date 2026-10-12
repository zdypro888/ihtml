package einoengine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/compose"
	ai "github.com/zdypro888/ihtml/agent"
)

func approvalCheckpointID(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return fmt.Sprintf("ihtml:approval:%x", sum[:])
}

// Strings keep approval state portable through Eino's serializer without
// registering host-defined concrete types globally.
func approvalMiddleware(tools []ai.Tool, enabled bool, scope *turnToolMiddleware) compose.ToolMiddleware {
	byName := make(map[string]ai.Tool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	return compose.ToolMiddleware{Name: "ihtml_native_approval", Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
		return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
			if !enabled || input == nil || !byName[input.Name].ApprovalRequired {
				return next(ctx, input)
			}
			args := strings.TrimSpace(input.Arguments)
			if args == "" {
				args = "{}"
			}
			if len(args) > 64<<10 || !json.Valid([]byte(args)) {
				return nil, errors.New("invalid approval arguments")
			}
			frozen, err := json.Marshal(ai.ApprovalTool{ToolCallID: input.CallID, ToolName: input.Name, Args: json.RawMessage(args)})
			if err != nil {
				return nil, err
			}
			wasInterrupted, hasState, state := compose.GetInterruptState[string](ctx)
			if !wasInterrupted && !scope.callable(input.Name) {
				return next(ctx, input)
			}
			if wasInterrupted && (!hasState || state != string(frozen)) {
				return nil, errors.New("approval invocation changed")
			}
			targeted, hasData, approved := compose.GetResumeContext[bool](ctx)
			if wasInterrupted && targeted && hasData {
				scope.mu.Lock()
				scope.approvalArgs[toolCallKey(input.Name, input.CallID)] = json.RawMessage(args)
				scope.mu.Unlock()
				if !approved {
					scope.mu.Lock()
					scope.rejectedCalls[toolCallKey(input.Name, input.CallID)] = struct{}{}
					scope.mu.Unlock()
					return &compose.ToolOutput{Result: "用户拒绝了本次工具操作，工具未执行。"}, nil
				}
				// A rebuilt middleware has no local tool-search selections. The exact
				// persisted interrupt proves this tool was selected in this same turn.
				scope.mu.Lock()
				scope.selected[input.Name] = struct{}{}
				scope.mu.Unlock()
				return next(ctx, input)
			}
			return nil, compose.StatefulInterrupt(ctx, string(frozen), string(frozen))
		}
	}}
}
