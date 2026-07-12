package chat

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/zdypro888/ihtml"
)

type eventSink struct {
	mu     sync.Mutex
	events []ihtml.ChatStreamEvent
}

func (s *eventSink) emit(ev ihtml.ChatStreamEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
	return nil
}

func (s *eventSink) types() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.events))
	for _, ev := range s.events {
		out = append(out, ev.Type+":"+ev.ToolStatus)
	}
	return out
}

func newTestRuntime(t *testing.T) (*runtime, *eventSink, ihtml.Service) {
	t.Helper()
	svc := ihtml.New(ihtml.NewMemoryStore())
	sink := &eventSink{}
	rt := &runtime{
		svc:      svc,
		user:     "t1",
		cfg:      Config{Provider: ProviderOpenAI, BaseURL: "http://gateway"},
		signer:   newConfirmSigner(nil),
		timeline: &timeline{emit: sink.emit},
	}
	return rt, sink, svc
}

func findTool(t *testing.T, rt *runtime, name string) *semanticTool {
	t.Helper()
	def, ok := findToolDef(name)
	if !ok {
		t.Fatalf("tool %s not found", name)
	}
	return &semanticTool{rt: rt, def: def}
}

func TestToolPutAndList(t *testing.T) {
	rt, sink, svc := newTestRuntime(t)
	ctx := context.Background()

	put := findTool(t, rt, "ui_item_put")
	out, err := put.InvokableRun(ctx, `{"items":[{"id":"hello","type":"html","order":10,"content":"<b>hi</b>"}],"note":"首个"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"status":"ok"`) {
		t.Fatalf("put result: %s", out)
	}
	items, err := svc.ListItems(ctx, "t1")
	if err != nil || len(items) != 1 || items[0].ID != "hello" {
		t.Fatalf("item not written: %v %v", items, err)
	}
	// 事件顺序: tool_start(running) → tool_result(success)
	types := sink.types()
	if types[0] != "tool_start:running" || types[len(types)-1] != "tool_result:success" {
		t.Fatalf("events: %v", types)
	}

	list := findTool(t, rt, "ui_item_list")
	out, err = list.InvokableRun(ctx, `{}`)
	if err != nil || !strings.Contains(out, `"count":1`) || !strings.Contains(out, "hello") {
		t.Fatalf("list result: %s %v", out, err)
	}
	// 不可信数据声明必须带
	if !strings.Contains(out, "untrusted_data_notice") {
		t.Fatalf("missing untrusted notice: %s", out)
	}
}

func TestToolThemeSet(t *testing.T) {
	rt, _, svc := newTestRuntime(t)
	ctx := context.Background()
	set := findTool(t, rt, "ui_theme_set")
	if _, err := set.InvokableRun(ctx, `{"mode":"dark","primary":"#059669"}`); err != nil {
		t.Fatal(err)
	}
	raw, err := svc.KVGet(ctx, "t1", "theme")
	if err != nil {
		t.Fatal(err)
	}
	theme := map[string]any{}
	_ = json.Unmarshal(raw, &theme)
	if theme["mode"] != "dark" || theme["primary"] != "#059669" {
		t.Fatalf("theme kv: %v", theme)
	}
}

func TestToolConfirmationFlow(t *testing.T) {
	rt, _, svc := newTestRuntime(t)
	ctx := context.Background()

	// 造出一条历史快照: 写两次
	_ = svc.PutItems(ctx, "t1", []ihtml.Item{{ID: "a", Type: ihtml.ItemHTML, Content: "v1"}}, "chat", "1")
	_ = svc.PutItems(ctx, "t1", []ihtml.Item{{ID: "a", Type: ihtml.ItemHTML, Content: "v2"}}, "chat", "2")
	revs, _ := svc.ListRevisions(ctx, "t1")
	if len(revs) == 0 {
		t.Fatal("need a revision")
	}

	// rollback 是高危工具: 只发确认卡, 不执行
	rollback := findTool(t, rt, "ui_revision_rollback")
	out, err := rollback.InvokableRun(ctx, `{"id":"`+revs[0].ID+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pending_confirmation") {
		t.Fatalf("rollback should be pending: %s", out)
	}
	items, _ := svc.ListItems(ctx, "t1")
	if items[0].Content != "v2" {
		t.Fatal("rollback should not run before confirmation")
	}
	// timeline 里应有待确认卡
	pending := rt.timeline.pendingCalls()
	if len(pending) != 1 || pending[0].ConfirmationToken == "" {
		t.Fatalf("pending call: %+v", pending)
	}

	// 篡改内容 → 核销失败
	tampered := pending[0]
	tampered.Body = map[string]any{"id": "别的快照"}
	if rt.signer.consume(rt.user, tampered) {
		t.Fatal("tampered call must be rejected")
	}

	// 原样确认 → 执行成功, 内容回滚
	sink2 := &eventSink{}
	call := pending[0]
	if err := executeConfirmedTool(ctx, rt, &call, sink2.emit); err != nil {
		t.Fatal(err)
	}
	items, _ = svc.ListItems(ctx, "t1")
	if items[0].Content != "v1" {
		t.Fatalf("rollback did not apply: %v", items)
	}
	types := sink2.types()
	last := types[len(types)-1]
	if !strings.HasPrefix(last, "assistant_done") {
		t.Fatalf("confirm events: %v", types)
	}

	// 重放 → 拒绝
	sink3 := &eventSink{}
	if err := executeConfirmedTool(ctx, rt, &call, sink3.emit); err != nil {
		t.Fatal(err)
	}
	replay := sink3.types()
	if replay[0] != "tool_result:error" {
		t.Fatalf("replay must fail: %v", replay)
	}
}

func TestToolKVReserved(t *testing.T) {
	rt, _, _ := newTestRuntime(t)
	set := findTool(t, rt, "ui_kv_set")
	out, err := set.InvokableRun(context.Background(), `{"key":"__ihtml.errors","value":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"status":"error"`) {
		t.Fatalf("reserved key must be rejected: %s", out)
	}
}

func TestSystemPromptBraces(t *testing.T) {
	// 系统提示走自定义 GenModelInput 不做 f-string 模板, 但保底确认内容完整
	prompt := buildSystemPrompt(Config{SystemPrompt: "扩展指令"}, ihtml.ChatClientContext{
		Viewport: ihtml.ChatClientViewport{Width: 400, Height: 800, Mode: "mobile"},
		Locale:   "zh-CN",
		Page:     ihtml.ChatClientPage{VisibleText: "现有页面文本"},
	}, []ihtml.APISpec{{Name: "player_search", Method: "GET", Path: "/capi/players", Description: "按昵称搜玩家; 参数 q/limit"}})
	for _, want := range []string{"ui_item_list", "ui_page_set", "--ui-primary", "扩展指令", "mobile", "现有页面文本", "不可信", "/capi/players", "api_probe", "ihtml-mobile"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
}
