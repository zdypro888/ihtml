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
	count := "0"
	if len(req.Tools) > 0 {
		result, err := req.Tools[0].Handler(ctx, json.RawMessage(`{}`))
		step := agent.Step{Kind: agent.StepToolCall, ToolName: req.Tools[0].Name, Result: result}
		if err != nil {
			step.Err = "read failed"
		}
		req.OnEvent(step)
		if err != nil {
			return nil, err
		}
		count = result
	}
	answer := "Preview only: no AI model is connected. Read-only item count: " + count + "."
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
	backend, err := assistant.NewChecked(assistant.Config{Engine: engine, Models: &models, PrepareTurn: host.prepare, CompleteTurn: host.complete})
	if err != nil {
		return nil, err
	}
	return ihtml.NewHandler(ihtml.NewMemoryStore(),
		ihtml.WithChatBackend(backend),
		ihtml.WithUserResolver(func(*http.Request) (string, error) { return "loopback-demo", nil }),
		ihtml.WithAccessPolicy(func(*http.Request, string) ihtml.Access { return ihtml.Access{ReadOnly: true} }))
}

func (h *demoHost) prepare(ctx context.Context, sc *ihtml.ChatSessionContext, ev *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
	if sc.Request == nil || sc.ScopedService == nil || len(ev.Messages) != 1 || strings.TrimSpace(ev.Messages[0].Content) == "" {
		return nil, errors.New("authenticated session and user text required")
	}
	h.mu.Lock()
	state := h.states[sc.Request]
	if state == nil {
		state = &hostState{user: sc.User, id: fmt.Sprintf("connection-%d", h.ids.Add(1))}
		h.states[sc.Request] = state
		context.AfterFunc(sc.Request.Context(), func() { h.mu.Lock(); delete(h.states, sc.Request); h.mu.Unlock() })
	}
	if state.user != sc.User {
		h.mu.Unlock()
		return nil, ihtml.ErrAccessDenied
	}
	state.turn++
	req := &agent.TurnRequest{Mode: agent.TurnModeDeep, SessionID: state.id, EngineSession: state.engineSession,
		InvocationScope: fmt.Sprintf("%s-turn-%d", state.id, state.turn), SessionCapability: "demo-readonly",
		History: append([]agent.Message(nil), state.history...), UserText: ev.Messages[0].Content,
		System: "You are a read-only assistant. Answer the user's question concisely. The item_count tool counts UI items; it cannot modify data."}
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
		}}}
	return req, ctx.Err()
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
	mux.Handle("/", h)
	return mux
}

const demoHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>ihtml Assistant</title><link rel="stylesheet" href="/ihtml.css">
<style>body{margin:0;font-family:system-ui;background:#fff;color:#172522}header{padding:16px 24px;border-bottom:1px solid #dce3e0}h1{font-size:20px;margin:0}main{padding:16px;max-width:960px;margin:auto}#assistant{min-height:70vh}</style></head>
<body><header><h1>ihtml Assistant</h1></header><main><div id="assistant"></div></main>
<script src="/ihtml.js"></script><script>const assistant=ihtml.mountChat(document.getElementById('assistant'),{base:'/',theme:{mode:'light'}});assistant.ready().then(()=>assistant.chat.open());</script>
</body></html>`
