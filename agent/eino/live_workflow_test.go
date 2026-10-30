package einoengine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/schema"
	ai "github.com/zdypro888/ihtml/agent"
)

// 诊断只记录工具名与选择模式；不记录请求消息、密钥、工具参数或模型响应。
type liveFixtureTransport struct{ t *testing.T }

func (p liveFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(raw))
		var body struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
			ToolChoice json.RawMessage `json:"tool_choice"`
		}
		_ = json.Unmarshal(raw, &body)
		names := []string{}
		for _, tool := range body.Tools {
			names = append(names, tool.Name)
		}
		p.t.Logf("request tools=%v choice=%s", names, body.ToolChoice)
	}
	return http.DefaultTransport.RoundTrip(req)
}

// 显式选择才访问模型；工具只读取合成数据及写本机计数，不接触真实租户、Apple 或云盘。
// 凭据仍只从统一 JSON 文件读取，测试报告不保存密钥、响应正文或模型推理。
func TestLiveResponsesAgentWorkflow(t *testing.T) {
	if os.Getenv("IHTML_LIVE_AGENT_TEST") != "1" {
		t.Skip("explicit live model test opt-in required")
	}
	key := liveModelAPIKey(t)

	for _, model := range []string{"gpt-6.1-sol", "gpt-6.1-terra"} {
		t.Run(model, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
			defer cancel()
			memory := session.NewInMemoryStore[*schema.Message](nil)
			cfg := Config{Provider: ProviderOpenAI, BaseURL: "https://api.openai.com/v1", APIKey: key, Model: model, OpenAIAPI: "responses", MaxTokens: 8192, MaxCompletionTokens: 8192, ReasoningEffort: "medium", TimeoutMS: 180000, MaxTurns: 64, SummarizeAfterMessages: 6, SessionNamespace: "ihtml-live-fixture"}
			cfg.HTTPClient = &http.Client{Transport: liveFixtureTransport{t: t}, Timeout: 180 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			engine, err := New(ctx, cfg, WithRuntimeStore(memory))
			if err != nil {
				t.Fatalf("engine initialization failed (%T)", err)
			}
			var nonce [12]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				t.Fatal("fixture nonce unavailable")
			}
			digest := hex.EncodeToString(nonce[:])
			var found, inspected, committed, verified atomic.Int32
			empty := map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false}
			proof := map[string]any{"type": "object", "properties": map[string]any{"fingerprint": map[string]any{"type": "string"}}, "required": []string{"fingerprint"}, "additionalProperties": false}
			tools := []ai.Tool{
				{Name: "lookup_candidates", Description: "Read ten synthetic candidate identity names. Call this before inspect_plan. Read-only probes may be repeated to recover context.", Effect: ai.ToolEffectRead, LoadMode: ai.ToolLoadImmediate, InputSchema: empty, Handler: func(context.Context, json.RawMessage) (string, error) {
					found.Add(1)
					return `{"usernames":["fixture-1","fixture-2","fixture-3","fixture-4","fixture-5","fixture-6","fixture-7","fixture-8","fixture-9","fixture-10"],"count":10}`, nil
				}},
				{Name: "inspect_plan", Description: "Read a synthetic plan after lookup_candidates; returns an exact fingerprint required by the receipt tool.", Effect: ai.ToolEffectRead, LoadMode: ai.ToolLoadDeferred, InputSchema: empty, Handler: func(context.Context, json.RawMessage) (string, error) {
					if found.Load() < 1 {
						return "", errors.New("lookup must occur first")
					}
					inspected.Add(1)
					return fmt.Sprintf(`{"fingerprint":%q,"count":10,"scope":"synthetic-only"}`, digest), nil
				}},
				{Name: "record_fixture_receipt", Description: "Record only an in-memory test receipt after inspecting the synthetic plan. Requires exact fingerprint and human approval. Does not touch any real data.", Effect: ai.ToolEffectWrite, ApprovalRequired: true, LoadMode: ai.ToolLoadDeferred, InputSchema: proof, Handler: func(_ context.Context, raw json.RawMessage) (string, error) {
					var p struct {
						Fingerprint string `json:"fingerprint"`
					}
					if json.Unmarshal(raw, &p) != nil || p.Fingerprint != digest || inspected.Load() < 1 {
						return "", errors.New("invalid test receipt proof")
					}
					if !committed.CompareAndSwap(0, 1) {
						return "", errors.New("fixture receipt already exists")
					}
					return `{"state":"completed","count":10}`, nil
				}},
				{Name: "verify_fixture_receipt", Description: "Read the receipt after it is recorded, verifying the synthetic operation count. Call after record_fixture_receipt.", Effect: ai.ToolEffectRead, LoadMode: ai.ToolLoadDeferred, InputSchema: empty, Handler: func(context.Context, json.RawMessage) (string, error) {
					if committed.Load() != 1 {
						return "", errors.New("receipt not recorded exactly once")
					}
					verified.Add(1)
					return `{"verified":true,"count":10}`, nil
				}},
			}
			req := &ai.TurnRequest{Mode: ai.TurnModeDeep, SessionID: digest, InvocationScope: digest, SessionCapability: "fixture-capability", RequireApproval: true, System: "You are validating a synthetic local workflow. Complete the user's ordered tool sequence using exact tool facts. Use tool_search to discover deferred tools. Never invent a fingerprint or claim an unexecuted action succeeded. This fixture has no real Apple account, cloud file, or remote business action.", UserText: "Lookup the ten candidates, inspect the plan, record the fixture receipt using its exact fingerprint, and verify the recorded receipt. Perform the stages in that order; record the receipt exactly once. Read-only probes may be repeated if needed to recover context. Then briefly report the verified count.", Tools: tools, MaxIterations: 64, MaxOutputTokens: 8192}
			req.OnEvent = func(step ai.Step) {
				if step.Kind == ai.StepStatus || step.Kind == ai.StepToolCall {
					t.Logf("model=%s event=%s tool=%s", model, step.Kind, step.ToolName)
					if step.ToolName == "tool_search" {
						var result struct {
							Matches []string `json:"matches"`
						}
						_ = json.Unmarshal([]byte(step.Result), &result)
						t.Logf("search loaded=%v", result.Matches)
					}
				}
			}
			first, err := engine.RunTurn(ctx, req)
			if err != nil || first == nil || first.PendingApproval == nil || len(first.PendingApproval.Tools) != 1 || committed.Load() != 0 {
				t.Fatalf("checkpoint stage failed (%T); found=%d inspected=%d committed=%d", err, found.Load(), inspected.Load(), committed.Load())
			}
			pending := first.PendingApproval.Tools[0]
			if pending.ToolName != "record_fixture_receipt" {
				t.Fatal("wrong tool interrupted")
			}
			req.EngineSession = first.EngineSession
			req.Resume = &ai.ResumeRequest{CheckPointID: first.PendingApproval.CheckPointID, Decisions: map[string]bool{pending.InterruptID: true}, ToolCallID: pending.ToolCallID}
			// 重建 Engine，工作记忆和 checkpoint 必须从注入的原 Store 恢复；不是重新执行整个计划。
			rebuilt, err := New(ctx, cfg, WithRuntimeStore(memory))
			if err != nil {
				t.Fatal("rebuilt engine unavailable")
			}
			second, err := rebuilt.RunTurn(ctx, req)
			if err != nil || second == nil || second.PendingApproval != nil || committed.Load() != 1 || verified.Load() < 1 || found.Load() < 1 || inspected.Load() < 1 {
				t.Fatalf("resume stage failed (%T); counts=%d/%d/%d/%d", err, found.Load(), inspected.Load(), committed.Load(), verified.Load())
			}
			if second.FinishReason == "agent_error" || strings.TrimSpace(second.Text) == "" {
				t.Fatal("resume did not finish")
			}
			if _, err := rebuilt.RunTurn(ctx, req); err == nil || committed.Load() != 1 {
				t.Fatal("consumed checkpoint replayed")
			}
			follow := *req
			follow.Resume = nil
			follow.RequireApproval = false
			follow.InvocationScope = digest + "-followup"
			follow.EngineSession = second.EngineSession
			follow.UserText = "Without calling tools again, state the already verified candidate count from this session."
			follow.History = []ai.Message{{Role: ai.RoleUser, Content: "Deliberately invalid seed: count was 999."}}
			third, err := rebuilt.RunTurn(ctx, &follow)
			if err != nil || third == nil || !strings.Contains(third.Text, "10") || strings.Contains(third.Text, "999") || found.Load() < 1 || committed.Load() != 1 {
				t.Fatalf("managed follow-up failed (%T)", err)
			}
			compactions := first.ToolExposure.ContextCompactions + second.ToolExposure.ContextCompactions + third.ToolExposure.ContextCompactions
			if compactions == 0 {
				t.Fatal("native summarization was not exercised")
			}
			t.Logf("model=%s: deferred search, ordered tools, native compaction, checkpoint pause/resume, replay rejection, managed follow-up passed; model_calls=%d/%d/%d compactions=%d", model, first.ToolExposure.ModelCalls, second.ToolExposure.ModelCalls, third.ToolExposure.ModelCalls, compactions)
		})
	}
}

// 实际模型验收共用同一凭据入口；从不把密钥写到测试文件或日志。
func liveModelAPIKey(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal("home directory unavailable")
	}
	raw, err := os.ReadFile(filepath.Join(home, ".config/credentials"))
	if err != nil {
		t.Fatal("unified credentials unavailable")
	}
	var credentials struct {
		Services map[string]json.RawMessage `json:"services"`
	}
	if json.Unmarshal(raw, &credentials) != nil {
		t.Fatal("unified credentials invalid")
	}
	var service struct {
		Credentials struct {
			APIKey string `json:"api_key"`
		} `json:"credentials"`
	}
	if json.Unmarshal(credentials.Services["iclouder_ai"], &service) != nil {
		t.Fatal("iclouder_ai configuration invalid")
	}
	key := service.Credentials.APIKey
	if key == "" {
		t.Fatal("iclouder_ai credential unavailable")
	}
	return key
}
