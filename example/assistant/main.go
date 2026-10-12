// Run with go run ./example/assistant. Real API calls require -live and explicit
// IHTML_ASSISTANT_API_KEY and IHTML_ASSISTANT_MODEL environment variables.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/schema"
	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/agent"
	native "github.com/zdypro888/ihtml/agent/eino"
	"github.com/zdypro888/ihtml/assistant"
	"github.com/zdypro888/ihtml/confirmation"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8901", "loopback listen address")
	live := flag.Bool("live", false, "explicitly enable billable API calls")
	flag.Parse()
	if !loopback(*addr) {
		log.Fatal("this unauthenticated demo must bind to loopback")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h, err := newDemo(ctx, *live, os.Getenv)
	if err != nil {
		// Do not log SDK errors or configuration that may contain credentials.
		log.Fatal("assistant demo could not initialize; check explicit server-side settings")
	}
	defer h.Close()
	server := &http.Server{Addr: *addr, Handler: demoRoutes(h), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		// Shutdown does not close hijacked WebSockets; Handler owns those sessions.
		h.Close()
	}()
	mode := "preview (no model API calls)"
	if *live {
		mode = "live (billable model API calls enabled)"
	}
	log.Printf("assistant demo http://%s - %s", *addr, mode)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal("assistant demo listener failed")
	}
}

func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type previewEngine struct{}

func (*previewEngine) Name() string { return "preview" }
func (*previewEngine) RunTurn(ctx context.Context, req *agent.TurnRequest) (*agent.TurnResult, error) {
	answer := "Preview only: no AI model is connected."
	wanted, args := "scratch_status", json.RawMessage(`{}`)
	if strings.EqualFold(strings.TrimSpace(req.UserText), "mark") {
		wanted, args = "set_demo_marker", json.RawMessage(`{"value":"marked"}`)
	}
	for _, tool := range req.Tools {
		if tool.Name != wanted {
			continue
		}
		result, err := tool.Handler(ctx, args)
		step := agent.Step{Kind: agent.StepToolCall, ToolName: tool.Name, Args: args, Result: result}
		if err != nil {
			step.Err = "demo tool failed"
		}
		req.OnEvent(step)
		if err != nil {
			return nil, err
		}
		if wanted == "set_demo_marker" {
			answer += " Scratch marker update proposed; not executed."
		} else {
			answer += " Scratch marker: " + result + "."
		}
		break
	}
	var snapshot string
	for _, word := range strings.Fields(answer) {
		if snapshot != "" {
			snapshot += " "
		}
		snapshot += word
		req.OnDelta(snapshot)
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return &agent.TurnResult{Text: answer, FinishReason: "stop"}, nil
}

type hostState struct {
	user, id, engineSession string
	history                 []agent.Message
	turn                    uint64
	marker                  string
}

type demoHost struct {
	mu     sync.Mutex
	ids    atomic.Uint64
	states map[*http.Request]*hostState
}

func newDemo(ctx context.Context, live bool, getenv func(string) string) (*ihtml.Handler, error) {
	var engine agent.Engine = &previewEngine{}
	models := ihtml.ChatModels{Provider: "preview", PreviewOnly: true, CurrentModel: "preview", Models: []string{"preview"}, AgentModes: []string{"deep"}, RequestTimeoutMS: 120000}
	if live {
		key, model := strings.TrimSpace(getenv("IHTML_ASSISTANT_API_KEY")), strings.TrimSpace(getenv("IHTML_ASSISTANT_MODEL"))
		if key == "" || model == "" {
			return nil, errors.New("explicit API key and model are required")
		}
		var err error
		engine, err = native.New(ctx, native.Config{Provider: native.ProviderOpenAI, OpenAIAPI: "responses",
			APIKey: key, BaseURL: strings.TrimSpace(getenv("IHTML_ASSISTANT_BASE_URL")), Model: model,
			ReasoningEffort: "medium", MaxCompletionTokens: 8192, TimeoutMS: 120000,
			SessionNamespace: "ihtml-assistant-demo"}, native.WithRuntimeStore(session.NewInMemoryStore[*schema.Message](nil)))
		if err != nil {
			return nil, errors.New("native engine initialization failed")
		}
		models.Provider, models.CurrentModel, models.Models = "openai", model, []string{model}
		models.ModelReady, models.PreviewOnly = true, false
	}
	host := &demoHost{states: make(map[*http.Request]*hostState)}
	signer, err := confirmation.NewChecked(confirmation.Config{})
	if err != nil {
		return nil, err
	}
	backend, err := assistant.NewChecked(assistant.Config{Engine: engine, Models: &models,
		PrepareTurn: host.prepare, CompleteTurn: host.complete, ToolExecutionGuard: host.guard,
		Confirmation: &assistant.ConfirmationConfig{Signer: signer,
			Scope: func(ctx context.Context, sc *ihtml.ChatSessionContext, req *agent.TurnRequest) (confirmation.Scope, error) {
				if err := host.guard(ctx, sc, agent.Tool{Name: "scratch_status", Effect: agent.ToolEffectRead}, json.RawMessage(`{}`)); err != nil {
					return confirmation.Scope{}, err
				}
				return confirmation.NewScope(req.SessionID, "demo-scratch-marker"), nil
			}, Preview: host.preview, ToolTimeout: 5 * time.Second},
		CompleteTool: host.completeTool})
	if err != nil {
		return nil, err
	}
	return ihtml.NewHandler(ihtml.NewMemoryStore(),
		ihtml.WithChatBackend(backend),
		ihtml.WithUserResolver(func(*http.Request) (string, error) { return "loopback-demo", nil }),
		// HTTP UI writes remain blocked by demoRoutes. Only the explicitly
		// exposed scratch tool can mutate anything through this backend.
		ihtml.WithAccessPolicy(func(*http.Request, string) ihtml.Access { return ihtml.Access{} }))
}

func (h *demoHost) prepare(ctx context.Context, sc *ihtml.ChatSessionContext, ev *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
	if sc.Request == nil || sc.ScopedService == nil || len(ev.Messages) != 1 || strings.TrimSpace(ev.Messages[0].Content) == "" {
		return nil, errors.New("authenticated session and user text required")
	}
	h.mu.Lock()
	state := h.states[sc.Request]
	if state == nil {
		state = &hostState{user: sc.User, id: fmt.Sprintf("connection-%d", h.ids.Add(1)), marker: "unmarked"}
		h.states[sc.Request] = state
		context.AfterFunc(sc.Request.Context(), func() { h.mu.Lock(); delete(h.states, sc.Request); h.mu.Unlock() })
	}
	if state.user != sc.User {
		h.mu.Unlock()
		return nil, ihtml.ErrAccessDenied
	}
	state.turn++
	req := &agent.TurnRequest{Mode: agent.TurnModeDeep, SessionID: state.id, EngineSession: state.engineSession,
		InvocationScope: fmt.Sprintf("%s-turn-%d", state.id, state.turn), SessionCapability: "demo-scratch-marker",
		History: append([]agent.Message(nil), state.history...), UserText: ev.Messages[0].Content,
		System: "You are a demo assistant. item_count and scratch_status are read-only. set_demo_marker only proposes a connection-local scratch marker update; a pending receipt is not execution. Never claim a proposal changed the marker. No files, business data or UI pages can be modified. Current connection scratch marker: " + state.marker + "."}
	h.mu.Unlock()
	service := sc.ScopedService
	req.Tools = []agent.Tool{{Name: "item_count", Description: "Count the current user's UI items without reading their contents.", Effect: agent.ToolEffectRead, LoadMode: agent.ToolLoadImmediate,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
			items, err := service.ListItems(ctx)
			if err != nil {
				return "", err
			}
			return fmt.Sprint(len(items)), nil
		}}, {Name: "scratch_status", Description: "Read the connection-local demo marker.", Effect: agent.ToolEffectRead, LoadMode: agent.ToolLoadImmediate,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			if err := h.guard(ctx, sc, agent.Tool{Name: "scratch_status", Effect: agent.ToolEffectRead}, args); err != nil {
				return "", err
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			state := h.states[sc.Request]
			if state == nil || ctx.Err() != nil {
				return "", ihtml.ErrAccessDenied
			}
			return state.marker, nil
		}}, {Name: "set_demo_marker", Description: "Propose setting a connection-local scratch marker to marked. Requires explicit confirmation; no business or UI changes.", Effect: agent.ToolEffectWrite,
		RequiredAction: "demo:scratch", ApprovalRequired: true, LoadMode: agent.ToolLoadImmediate,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string", "enum": []string{"marked"}}}, "required": []string{"value"}, "additionalProperties": false},
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			if err := h.guard(ctx, sc, agent.Tool{Name: "set_demo_marker", Effect: agent.ToolEffectWrite, RequiredAction: "demo:scratch", ApprovalRequired: true}, args); err != nil {
				return "", err
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			state := h.states[sc.Request]
			if state == nil || state.user != sc.User || ctx.Err() != nil || sc.Request.Context().Err() != nil {
				return "", ihtml.ErrAccessDenied
			}
			state.marker = "marked"
			return "marked", nil
		}}}
	return req, ctx.Err()
}

func (h *demoHost) guard(ctx context.Context, sc *ihtml.ChatSessionContext, tool agent.Tool, args json.RawMessage) error {
	if ctx.Err() != nil || sc == nil || sc.Request == nil || sc.User != "loopback-demo" || sc.Request.Context().Err() != nil {
		return ihtml.ErrAccessDenied
	}
	h.mu.Lock()
	state := h.states[sc.Request]
	valid := state != nil && state.user == sc.User
	h.mu.Unlock()
	if !valid {
		return ihtml.ErrAccessDenied
	}
	switch tool.Name {
	case "item_count", "scratch_status":
		if tool.Effect == agent.ToolEffectRead && string(args) == "{}" {
			return nil
		}
	case "set_demo_marker":
		var input map[string]string
		if !sc.Access.ReadOnly && tool.Effect == agent.ToolEffectWrite && tool.RequiredAction == "demo:scratch" && tool.ApprovalRequired && json.Unmarshal(args, &input) == nil && len(input) == 1 && input["value"] == "marked" {
			return nil
		}
	}
	return ihtml.ErrAccessDenied
}

func (h *demoHost) preview(ctx context.Context, sc *ihtml.ChatSessionContext, tool agent.Tool, args json.RawMessage) (ihtml.ProposedToolCall, error) {
	if err := h.guard(ctx, sc, tool, args); err != nil {
		return ihtml.ProposedToolCall{}, err
	}
	if tool.Name != "set_demo_marker" {
		return ihtml.ProposedToolCall{}, ihtml.ErrAccessDenied
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.states[sc.Request]
	if state == nil {
		return ihtml.ProposedToolCall{}, ihtml.ErrAccessDenied
	}
	return ihtml.ProposedToolCall{Title: "Update scratch marker", Method: "TOOL", Path: "demo://scratch/marker", Risk: "medium",
		Reason: "Only this connection's in-memory marker changes; no files, business data or UI pages are modified.",
		Body:   map[string]any{"before": state.marker, "after": "marked"}}, nil
}

func (h *demoHost) completeTool(_ context.Context, sc *ihtml.ChatSessionContext, evidence *assistant.ConfirmedToolResult) error {
	if evidence.Err != nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.states[sc.Request]
	if state == nil || state.user != sc.User {
		return ihtml.ErrAccessDenied
	}
	// Authoritative display history only: never rewrite the Engine's pending
	// receipt or imply a checkpoint/plan resumed.
	state.history = append(state.history, agent.Message{Role: agent.RoleAssistant, Content: "Confirmed scratch marker update executed: " + evidence.Result})
	if len(state.history) > 40 {
		state.history = append([]agent.Message(nil), state.history[len(state.history)-40:]...)
	}
	return nil
}

func (h *demoHost) complete(_ context.Context, sc *ihtml.ChatSessionContext, req *agent.TurnRequest, result *agent.TurnResult, turnErr error) error {
	if turnErr != nil || result == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.states[sc.Request]
	if state == nil || state.user != sc.User || state.id != req.SessionID {
		return ihtml.ErrAccessDenied
	}
	state.engineSession = result.EngineSession
	state.history = append(state.history, agent.Message{Role: agent.RoleUser, Content: req.UserText}, agent.Message{Role: agent.RoleAssistant, Content: result.Text})
	if len(state.history) > 40 {
		state.history = append([]agent.Message(nil), state.history[len(state.history)-40:]...)
	}
	return nil
}

func demoRoutes(h *ihtml.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(demoHTML))
	})
	// Chat confirmation may enable scratch writes, never the UI store's HTTP
	// mutation endpoints. The WebSocket upgrade itself is a GET request.
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "UI mutations are disabled in this demo", http.StatusMethodNotAllowed)
			return
		}
		h.ServeHTTP(w, r)
	}))
	return mux
}

const demoHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>ihtml Assistant</title><link rel="stylesheet" href="/ihtml.css">
<style>body{margin:0;font-family:system-ui;background:#fff;color:#172522}header{padding:16px 24px;border-bottom:1px solid #dce3e0;display:flex;gap:12px;flex-wrap:wrap;align-items:center}h1{font-size:20px;margin:0}button{font:inherit;padding:8px 12px;background:#fff;border:1px solid #9ba8a2;border-radius:4px}main{padding:16px;max-width:960px;margin:auto}#assistant{height:70vh;min-height:440px}</style></head>
<body><header><h1>ihtml Assistant</h1><button id="mark" disabled>Mark scratch</button><button id="status" disabled>Check marker</button></header><main><div id="assistant"></div></main>
<script src="/ihtml.js"></script><script>const assistant=ihtml.mountChat(document.getElementById('assistant'),{base:'/',theme:{mode:'light'},locale:'en',strings:{welcome:'Scratch marker assistant.',composer_placeholder:'Check the scratch marker...',agent_manual_confirm:'Approval executes only this scratch update; no plan resumes.',agent_deep_manual:'Only the approved scratch update executes.',agent_plan_paused:'Manual approval'}});assistant.ready().then(()=>{document.getElementById('mark').disabled=false;document.getElementById('status').disabled=false;});document.getElementById('mark').onclick=()=>assistant.chat.send('mark');document.getElementById('status').onclick=()=>assistant.chat.send('status');</script>
</body></html>`
