package assistant

import (
	"context"
	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
	"testing"
)

// 宿主没有业务接口时仍可提供控制能力；用户关闭工具时不向引擎传入可用运行时。
func TestAssistantKernelInjectionHonorsExplicitToolSwitch(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			cfg := configFor(&fakeEngine{run: func(_ context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
				if enabled && req.Tasks == nil {
					t.Error("empty business catalog lost managed capabilities")
				}
				if !enabled && (req.Tasks != nil || len(req.Tools) != 0 || len(req.Skills) != 0 || !req.DisableTools) {
					t.Error("disabled turn retained tools")
				}
				return &agent.TurnResult{Text: "actual"}, nil
			}})
			cfg.Tasks = &agent.TaskConfig{}
			rec := newRecorder()
			session := openSession(t, cfg, "alice", rec)
			session.HandleEvent(t.Context(), &ihtml.ChatClientEvent{Type: ihtml.ChatClientUserMessage, ToolsEnabled: &enabled, Messages: []ihtml.ChatMessage{{Role: "user", Content: "plain request"}}})
			rec.wait(t, ihtml.ChatStreamAssistantDone, 1)
		})
	}
}
