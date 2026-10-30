package einoengine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ai "github.com/zdypro888/ihtml/agent"
)

// 模型实际读取的工具契约包含影响、授权和完成语义；未知影响不能被美化成可并发只读。
func TestModelToolSchedulingContracts(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool ai.Tool
		want []string
	}{
		{"read", ai.Tool{Effect: ai.ToolEffectRead}, []string{"只读查询", "输入已确定", "等待前一步"}},
		{"write", ai.Tool{Effect: ai.ToolEffectWrite, ApprovalRequired: true}, []string{"会产生外部影响", "同一资源的修改按序", "需要独立确认"}},
		{"dispatch", ai.Tool{Effect: ai.ToolEffectExecute, Completion: ai.ToolCompletionAsynchronous, ApprovalRequired: true}, []string{"需要独立确认", "异步受理", "须等真实完成", "不要重复提交"}},
		{"unknown", ai.Tool{}, []string{"外部影响未声明", "不能假定"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.tool.Name = "custom_capability"
			tc.tool.Description = "前置：版本必须来自上一步结果。"
			info, err := (&einoTool{t: tc.tool}).Info(t.Context())
			if err != nil || !strings.HasPrefix(info.Desc, tc.tool.Description) {
				t.Fatal("host constraint lost", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(info.Desc, want) {
					t.Fatalf("missing %q in %q", want, info.Desc)
				}
			}
		})
	}
}

// 显式真实模型验收：用户只要求查询两项，不提醒并发；检验共享指引能使 Handler 同时运行。
// 工具只读合成状态，不进入生产会话、租户、Apple 或实际迁移；不据此宣称迁移验收完成。
func TestLiveResponsesParallelScheduling(t *testing.T) {
	if os.Getenv("IHTML_LIVE_PARALLEL_TEST") != "1" {
		t.Skip("explicit live parallel test opt-in required")
	}
	key := liveModelAPIKey(t)
	for _, name := range []string{"gpt-6.1-sol", "gpt-6.1-terra"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			engine, err := New(ctx, Config{Provider: ProviderOpenAI, BaseURL: "https://api.openai.com/v1", APIKey: key, Model: name, OpenAIAPI: "responses", MaxCompletionTokens: 4096, ReasoningEffort: "medium", TimeoutMS: 120000, MaxTurns: 8, HTTPClient: &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
			if err != nil {
				t.Fatalf("model init failed (%T)", err)
			}
			var calls, active, seen atomic.Int32
			both := make(chan struct{})
			var overlap atomic.Bool
			result, err := engine.RunTurn(ctx, &ai.TurnRequest{DisableSession: true, System: "你是项目状态助手，使用工具取得当前状态后简短汇总。", UserText: "请查项目 A 和项目 B 当前各有多少待处理任务。", Tools: []ai.Tool{{Name: "project_status", Description: "读取一个指定项目的当前状态，项目标识为 A 或 B。", Effect: ai.ToolEffectRead, LoadMode: ai.ToolLoadImmediate, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"project": map[string]any{"type": "string", "enum": []string{"A", "B"}}}, "required": []string{"project"}, "additionalProperties": false}, Handler: func(c context.Context, raw json.RawMessage) (string, error) {
				var args struct {
					Project string `json:"project"`
				}
				if err := json.Unmarshal(raw, &args); err != nil {
					return "", err
				}
				if args.Project != "A" && args.Project != "B" {
					return "", fmt.Errorf("invalid project")
				}
				if args.Project == "A" {
					seen.Or(1)
				} else {
					seen.Or(2)
				}
				calls.Add(1)
				defer active.Add(-1)
				if active.Add(1) == 2 && overlap.CompareAndSwap(false, true) {
					close(both)
				}
				timer := time.NewTimer(3 * time.Second)
				defer timer.Stop()
				select {
				case <-both:
				case <-timer.C:
				case <-c.Done():
					return "", c.Err()
				}
				out, _ := json.Marshal(map[string]any{"project": args.Project, "pending": map[string]int{"A": 3, "B": 7}[args.Project]})
				return string(out), nil
			}}}})
			if err != nil {
				t.Fatalf("model workflow failed (%T)", err)
			}
			if calls.Load() != 2 || !overlap.Load() || seen.Load() != 3 || result == nil || strings.TrimSpace(result.Text) == "" {
				t.Fatalf("independent queries not completed in parallel: calls=%d overlap=%t projects=%d", calls.Load(), overlap.Load(), seen.Load())
			}

			t.Logf("model=%s parallel calls=%d", name, calls.Load())
		})
	}
}
