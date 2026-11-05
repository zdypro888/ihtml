package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
)

// 聊天后台通知取决于执行方式，不取决于业务返回是否代表异步受理；同步账本仍完整通知宿主钩子。
func TestTaskChatNotificationUsesExecutionMode(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprint("background=", background), func(t *testing.T) {
			store := agent.NewMemoryTaskStore()
			completed := make(chan agent.TaskInfo, 1)
			cfg := configFor(&fakeEngine{run: func(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
				tool := req.Tools[0]
				if background {
					_, err := req.Tasks.Start(ctx, agent.TaskScope(req), req.InvocationScope, "one", tool, json.RawMessage(`{}`))
					return &agent.TurnResult{Text: "started"}, err
				}
				_, err := req.Tasks.Call(ctx, agent.TaskScope(req), req.InvocationScope, "one", tool, json.RawMessage(`{}`))
				return &agent.TurnResult{Text: "returned"}, err
			}})
			prepare := cfg.PrepareTurn
			cfg.PrepareTurn = func(ctx context.Context, sc *ihtml.ChatSessionContext, ev *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
				req, err := prepare(ctx, sc, ev)
				// 同步调用可受理业务异步作业，后台执行也可使用普通同步接口。
				completion := agent.ToolCompletionAsynchronous
				if background {
					completion = agent.ToolCompletionImmediate
				}
				req.Tools = []agent.Tool{{Name: "generic", Effect: agent.ToolEffectRead, Completion: completion, Handler: func(context.Context, json.RawMessage) (string, error) { return "actual receipt", nil }}}
				return req, err
			}
			cfg.ToolExecutionGuard = func(context.Context, *ihtml.ChatSessionContext, agent.Tool, json.RawMessage) error { return nil }
			cfg.Tasks = &agent.TaskConfig{Store: store, OnComplete: func(info agent.TaskInfo) { completed <- info }}
			rec := newRecorder()
			chat := openSession(t, cfg, "alice", rec)
			send(chat, "call")
			var info agent.TaskInfo
			select {
			case info = <-completed:
			case <-time.After(time.Second):
				t.Fatal("missing general completion hook")
			}
			rec.wait(t, ihtml.ChatStreamAssistantDone, 1)
			count := 0
			for _, ev := range rec.snapshot() {
				if ev.Type == ihtml.ChatStreamTaskComplete {
					count++
					if ev.TaskID != info.ID {
						t.Fatal("notification lost task identity", ev)
					}
				}
			}
			want := 0
			if background {
				want = 1
			}
			if count != want || info.Background != background {
				t.Fatal("wrong chat notification/mode", count, info)
			}
			scope := agent.TaskScope(&agent.TurnRequest{SessionID: "alice"})
			record, err := store.GetTask(t.Context(), scope, info.ID)
			if err != nil || record.Info.State != "completed" || record.Info.Result != "actual receipt" || record.Info.Background != background {
				t.Fatal("ledger must retain mode and result for both execution styles", record, err)
			}
		})
	}
}
