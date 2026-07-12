// 演示程序: 起一个带演示数据的多页 ihtml 服务。
//
//	go run ./example -addr :8899           # 内存存储
//	go run ./example -data ./uidata        # 文件存储
//
// 配置真实 AI(可选, 不配置则挂内置"演示助手"):
//
//	export IHTML_AI_BASE_URL=https://ai.im.app/v1        # 任意 OpenAI 兼容网关
//	export IHTML_AI_API_KEY=sk-...
//	export IHTML_AI_THINKING=1
//
// 绑定 casino 真实数据(可选): 服务端持有 casino admin 会话,
// 把 /capi/* 只读转发到 casino, 并把 API 目录告知 AI:
//
//	export IHTML_CASINO_BASE=http://192.168.139.2:9090
//	export IHTML_CASINO_USER=admin IHTML_CASINO_PASS=admin
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/chat"
)

func main() {
	addr := flag.String("addr", ":8899", "监听地址")
	data := flag.String("data", "", "数据目录, 留空用内存存储")
	flag.Parse()

	var store ihtml.Store
	if *data != "" {
		fileStore, err := ihtml.NewFileStore(*data)
		if err != nil {
			log.Fatal(err)
		}
		store = fileStore
	} else {
		store = ihtml.NewMemoryStore()
	}

	opts := []ihtml.Option{
		ihtml.WithPageTitle("ihtml demo"),
		// 宿主动态参数注入演示: Item 里可用 ihtml.config.nickname
		ihtml.WithPageConfig(func(_ *http.Request, user string) map[string]any {
			return map[string]any{"nickname": user + " 同学", "env": "demo"}
		}),
		// 审计流演示: 全部写操作(含 AI 发起)留痕
		ihtml.WithAudit(func(_ context.Context, e ihtml.AuditEvent) {
			log.Printf("[audit] user=%s action=%s by=%s note=%s detail=%s", e.User, e.Action, e.By, e.Note, e.Detail)
		}),
		// 访问策略演示: uid=viewer 为只读且禁用 AI
		ihtml.WithAccessPolicy(func(_ *http.Request, user string) ihtml.Access {
			if user == "viewer" {
				return ihtml.Access{ReadOnly: true, ChatDisabled: true}
			}
			return ihtml.Access{}
		}),
	}

	if base := os.Getenv("IHTML_AI_BASE_URL"); base != "" {
		backend := chat.New(chat.Config{
			Provider:         envOr("IHTML_AI_PROVIDER", "openai"),
			BaseURL:          base,
			APIKey:           os.Getenv("IHTML_AI_API_KEY"),
			Model:            os.Getenv("IHTML_AI_MODEL"),
			Stream:           true,
			Thinking:         os.Getenv("IHTML_AI_THINKING") == "1",
			RequestTimeout:   180 * time.Second,
			AllowModelSelect: true,                         // 演示环境放开模型选择器(生产默认隐藏)
			ConfirmSecret:    []byte("ihtml-demo-confirm"), // 固定确认卡密钥: 重启不失效
		})
		opts = append(opts, ihtml.WithChatBackend(backend))
		log.Printf("AI 对话已启用(真实模型): %s", base)
	} else {
		opts = append(opts, ihtml.WithChatBackend(&demoBackend{}))
		log.Printf("未配置 IHTML_AI_BASE_URL, 已挂载内置演示助手(不调用真实模型)")
	}

	mux := http.NewServeMux()

	// casino 数据绑定: 只读代理 + API 目录
	if casinoBase := os.Getenv("IHTML_CASINO_BASE"); casinoBase != "" {
		proxy := newCasinoProxy(casinoBase, envOr("IHTML_CASINO_USER", "admin"), envOr("IHTML_CASINO_PASS", "admin"))
		mux.Handle("/capi/", proxy)
		opts = append(opts, ihtml.WithAPIs(
			ihtml.APISpec{
				Name: "player_search", Title: "玩家搜索", Method: "GET", Path: "/capi/players",
				Description: "查询玩家列表。参数: q(昵称/登录名模糊搜索, 可空), limit(条数, 默认20最大200)。返回 {players:[{id, display_name, login_name, balance(分), currency, banned, ban_reason, merchant_name, created_at}]}",
			},
			ihtml.APISpec{
				Name: "player_bets", Title: "玩家下注明细", Method: "GET", Path: "/capi/players/{id}/bets",
				Description: "查询某玩家的下注记录, {id} 换成玩家数字 id。参数: limit(默认20)。返回 {bets:[{game, bet_amount, payout, created_at, ...}]}",
			},
			ihtml.APISpec{
				Name: "report_dashboard", Title: "运营日报", Method: "GET", Path: "/capi/reports/dashboard",
				Description: "近 7 日运营汇总。返回 {from, to, days:[{day, currency, total_bet, total_payout, net, bet_count}]} (金额单位: 元)",
			},
			ihtml.APISpec{
				Name: "node_status", Title: "服务节点状态", Method: "GET", Path: "/capi/nodes",
				Description: "游戏服务节点列表与在线状态。返回 {nodes:[{id, name, status, ...}]}",
			},
		))
		log.Printf("casino 数据代理已启用: /capi/* → %s/admin/api/* (只读)", casinoBase)
	}

	// 演示跨域嵌入: 生产按需收紧 origin 列表
	opts = append(opts, ihtml.WithCORS("*"))

	handler := ihtml.New(store, opts...)
	seedDemo(handler)
	seedHostDemo(handler)
	mux.Handle("/", handler)
	mux.HandleFunc("GET /host-demo", hostDemoPage)

	log.Printf("演示页面: http://localhost%s/?uid=demo (空白用户: ?uid=blank)", *addr)
	log.Printf("宿主嵌入演示: http://localhost%s/host-demo (mount 进宿主自己的布局)", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

// hostDemoPage 模拟"别人的产品"嵌入 ihtml 内核: 宿主只写一个
// <div data-ihtml ...> 声明式挂载(零 JS), 主题 inherit 自动探测宿主样式,
// 多页菜单渲染进宿主自己的侧栏容器(data-menu)。
func hostDemoPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>宿主应用 · 嵌入 ihtml 演示</title>
<link rel="stylesheet" href="/ihtml.css">
<style>
  /* 宿主自己的样式体系(ihtml 的 inherit 主题会探测这些: 背景亮度/正文色/字体/链接色) */
  body { margin:0; font-family:-apple-system,"PingFang SC",sans-serif; background:#0b1220; color:#dbe4f5; }
  a { color:#38bdf8; }
  .host-top { display:flex; align-items:center; gap:18px; padding:14px 22px; background:#111c33; border-bottom:1px solid #22304d; }
  .host-top .logo { font-weight:800; color:#38bdf8; }
  .host-top nav { display:flex; gap:14px; font-size:14px; color:#93a6c8; }
  .host-top nav b { color:#e2ecff; }
  .host-wrap { display:grid; grid-template-columns:200px 1fr; min-height:calc(100vh - 53px); }
  .host-side { background:#0e1728; border-right:1px solid #22304d; padding:14px 10px; font-size:14px; color:#93a6c8; }
  .host-side .host-group { padding:0 8px 8px; font-size:12px; color:#647694; }
  .host-main { padding:18px; min-width:0; }
  .host-note { font-size:12.5px; color:#647694; margin:0 0 12px; }
</style>
</head>
<body>
<div class="host-top"><span class="logo">◆ HostApp</span><nav><span>首页</span><b>动态面板(ihtml)</b><span>设置</span></nav></div>
<div class="host-wrap">
  <aside class="host-side">
    <div class="host-group">宿主自己的菜单</div>
    <div class="host-group" style="color:#e2ecff">▸ 动态面板</div>
    <div class="host-group" style="margin-top:14px">ihtml 页面菜单(菜单槽)</div>
    <div id="host-menu"></div>
  </aside>
  <main class="host-main">
    <p class="host-note">宿主只写了一个 &lt;div data-ihtml…&gt;: 主题 inherit 自动融入宿主观感, 多页菜单渲染进左侧宿主容器, 内存路由不占 hash:</p>
    <div data-ihtml data-base="/" data-uid="hostdemo" data-theme="inherit" data-menu="#host-menu"></div>
  </main>
</div>
<script src="/ihtml.js"></script>
</body>
</html>`))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

/* ---------- casino 只读代理 ----------
 * 服务端持有 admin 会话(cookie + CSRF), 把 /capi/<path> 转发到
 * <base>/admin/api/<path>。只放行 GET, 避免演示环境误写 casino。 */

type casinoProxy struct {
	base, user, pass string

	mu     sync.Mutex
	client *http.Client
	authed bool
}

func newCasinoProxy(base, user, pass string) *casinoProxy {
	jar, _ := cookiejar.New(nil)
	return &casinoProxy{
		base:   strings.TrimRight(base, "/"),
		user:   user,
		pass:   pass,
		client: &http.Client{Jar: jar, Timeout: 15 * time.Second},
	}
}

func (p *casinoProxy) login(ctx context.Context) error {
	// CSRF 双提交: 先领 token, 再带 header 登录
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/admin/api/csrf-token", nil)
	if err != nil {
		return err
	}
	res, err := p.client.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	res.Body.Close()
	token := extractJSONField(string(body), "csrf_token")
	if token == "" {
		return fmt.Errorf("casino csrf-token 获取失败: %s", body)
	}
	loginBody := fmt.Sprintf(`{"username":%q,"password":%q}`, p.user, p.pass)
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/admin/api/login", strings.NewReader(loginBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", token)
	res, err = p.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		out, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("casino 登录失败(%d): %s", res.StatusCode, out)
	}
	p.authed = true
	return nil
}

// extractJSONField 从平铺 JSON 里取字符串字段(避免为演示引入结构体)。
func extractJSONField(raw, field string) string {
	marker := `"` + field + `":"`
	i := strings.Index(raw, marker)
	if i < 0 {
		return ""
	}
	rest := raw[i+len(marker):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func (p *casinoProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"casino 代理只读, 仅支持 GET"}`, http.StatusMethodNotAllowed)
		return
	}
	target := p.base + "/admin/api/" + strings.TrimPrefix(r.URL.Path, "/capi/")
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if !p.authed {
			if err := p.login(r.Context()); err != nil {
				http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadGateway)
				return
			}
		}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadGateway)
			return
		}
		res, err := p.client.Do(req)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadGateway)
			return
		}
		if res.StatusCode == http.StatusUnauthorized && attempt == 0 {
			res.Body.Close()
			p.authed = false // 会话过期, 重登一次
			continue
		}
		defer res.Body.Close()
		w.Header().Set("Content-Type", res.Header.Get("Content-Type"))
		w.WriteHeader(res.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(res.Body, 4<<20))
		return
	}
}

/* ---------- 内置演示助手(无真实模型时) ---------- */

type demoBackend struct{}

func (b *demoBackend) NewSession(_ context.Context, sctx *ihtml.ChatSessionContext) (ihtml.ChatSession, error) {
	return &demoSession{sctx: sctx}, nil
}

// Models 实现 ihtml.ModelLister。
func (b *demoBackend) Models(_ context.Context) (ihtml.ChatModels, error) {
	return ihtml.ChatModels{ModelReady: true, CurrentModel: "demo(演示模式)", Models: []string{"demo(演示模式)"}}, nil
}

type demoSession struct {
	sctx *ihtml.ChatSessionContext
}

func (s *demoSession) Close() {}

func demoDelay() { time.Sleep(300 * time.Millisecond) }

func (s *demoSession) HandleEvent(ctx context.Context, ev *ihtml.ChatClientEvent) {
	if ev.Type != "user_message" {
		return
	}
	emit := s.sctx.Emit
	text := ""
	for i := len(ev.Messages) - 1; i >= 0; i-- {
		if ev.Messages[i].Role == "user" {
			text = strings.TrimSpace(ev.Messages[i].Content)
			break
		}
	}
	_ = emit(ihtml.ChatStreamEvent{Type: "turn_start", ModelReady: true})
	_ = emit(ihtml.ChatStreamEvent{Type: "assistant_status", Status: "正在理解需求。", ModelReady: true})
	demoDelay()
	_ = emit(ihtml.ChatStreamEvent{Type: "assistant_thinking", Thinking: "演示模式: 把用户的话写成一张卡片放到当前页面。", ModelReady: true})
	call := &ihtml.ProposedToolCall{
		ID: fmt.Sprintf("demo_%d", time.Now().UnixNano()), Name: "ui_item_put", Title: "写入/更新 Item",
		Method: "TOOL", Path: "ui://ui_item_put", Risk: "medium",
	}
	_ = emit(ihtml.ChatStreamEvent{Type: "tool_start", ToolCall: call, ToolName: call.Name, ToolTitle: call.Title, ToolStatus: "running", ToolInput: `{"items":[{"id":"ai-note",…}]}`, ModelReady: true})
	demoDelay()
	note := text
	if note == "" {
		note = "(空消息)"
	}
	err := s.sctx.Service.PutItems(ctx, s.sctx.User, []ihtml.Item{{
		ID: "ai-note", Type: ihtml.ItemHTML, Order: 26, Title: "AI 回应卡片",
		Content: `<div></div><script>(function(){var g=document.getElementById('demo-grid');var c=document.createElement('div');c.className='ui-card';c.innerHTML='<div class="ui-stat"><span class="label">AI 刚写入的卡片</span><span class="value" style="font-size:17px">🤖 ' + ` + jsString("你说: "+note) + ` + '</span></div>';(g||document.body).appendChild(c);ihtml.items.onTeardown('ai-note',function(){c.remove()});})();</script>`,
	}}, "chat", "演示助手写入回应卡片")
	if err != nil {
		_ = emit(ihtml.ChatStreamEvent{Type: "tool_result", ToolCall: call, ToolName: call.Name, ToolTitle: call.Title, ToolStatus: "error", ToolSummary: err.Error(), ModelReady: true})
		_ = emit(ihtml.ChatStreamEvent{Type: "turn_error", Error: err.Error(), ModelReady: true})
		return
	}
	_ = emit(ihtml.ChatStreamEvent{Type: "tool_result", ToolCall: call, ToolName: call.Name, ToolTitle: call.Title, ToolStatus: "success", ToolSummary: "已写入 1 个 Item: ai-note(dash 页)", ModelReady: true})
	for _, chunk := range []string{"<p>已把你的话写成一张卡片放进页面。", "接真实模型后我能真正理解需求、取业务数据、改主题、修报错。</p>"} {
		_ = emit(ihtml.ChatStreamEvent{Type: "assistant_delta", Delta: chunk, ModelReady: true})
		time.Sleep(120 * time.Millisecond)
	}
	_ = emit(ihtml.ChatStreamEvent{Type: "assistant_done", ModelReady: true})
}

// jsString 把文本安全地嵌进单引号 JS 字符串字面量。
func jsString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`, "\r", ``, "<", `\x3c`)
	return "'" + r.Replace(s) + "'"
}

/* ---------- 预置演示数据: 单页默认形态(不建页面/菜单, 用内核组件类) ---------- */

func seedDemo(h *ihtml.Handler) {
	ctx := context.Background()
	if items, err := h.ListItems(ctx, "demo"); err != nil || len(items) > 0 {
		return
	}
	demo := []ihtml.Item{
		{
			ID: "demo-head", Type: ihtml.ItemHTML, Title: "页头(标题+主题切换)", Order: 10,
			Content: `<div class="ui-page" style="padding-bottom:0">
  <div class="ui-toolbar">
    <div style="flex:1;min-width:220px">
      <span class="ui-badge">ihtml · 动态页面</span>
      <h1 class="ui-title" style="font-size:20px;margin:8px 0 2px" id="demo-hello">工作台</h1>
      <div class="ui-muted" style="font-size:13px">默认单页形态 · 每个区块都是一个 Item · AI 可增删改并持久化</div>
    </div>
    <button class="ui-btn" onclick="ihtml.theme.set({mode:'light'})">☀️ 亮色</button>
    <button class="ui-btn" onclick="ihtml.theme.set({mode:'dark'})">🌙 暗色</button>
  </div>
</div>
<script>
(function(){
  var t = document.getElementById('demo-hello');
  if (ihtml.config.nickname) t.textContent = ihtml.config.nickname + ' 的工作台';
})();
</script>`,
		},
		{
			ID: "demo-stats", Type: ihtml.ItemHTML, Title: "统计卡片区", Order: 20,
			Content: `<div class="ui-page" style="padding-top:var(--ui-gap)">
  <div class="ui-grid" id="demo-grid">
    <div class="ui-card"><div class="ui-stat">
      <span class="label">现在时刻</span>
      <span class="value primary" id="demo-clock">--:--:--</span>
      <span class="sub">interval 由 onTeardown 管理</span>
    </div></div>
    <div class="ui-card"><div class="ui-stat">
      <span class="label">点击次数(服务端 KV, 刷新不丢)</span>
      <span class="value" id="demo-count">…</span>
      <span class="sub"><button class="ui-btn primary" id="demo-count-btn" style="margin-top:4px">+1</button></span>
    </div></div>
    <div class="ui-card"><div class="ui-stat">
      <span class="label">当前设备(ihtml.env)</span>
      <span class="value" id="demo-env">…</span>
      <span class="sub">缩放窗口试试, 断点 768px</span>
    </div></div>
  </div>
</div>
<script>
(function(){
  var timer = setInterval(function(){
    var n = document.getElementById('demo-clock');
    if (n) n.textContent = new Date().toLocaleTimeString();
  }, 1000);
  var count = 0;
  var numEl = document.getElementById('demo-count');
  ihtml.kv.get('demo.counter', 0).then(function(v){ count = v || 0; numEl.textContent = count; });
  document.getElementById('demo-count-btn').onclick = function(){
    count++; numEl.textContent = count;
    ihtml.kv.set('demo.counter', count).catch(function(e){ ihtml.ui.toast('保存失败: ' + e.message, 'error'); });
  };
  var envEl = document.getElementById('demo-env');
  function renderEnv(){ envEl.textContent = ihtml.env.mode === 'mobile' ? '📱 Mobile' : '🖥 PC'; }
  renderEnv();
  var offEnv = ihtml.env.onChange(renderEnv);
  ihtml.items.onTeardown('demo-stats', function(){ clearInterval(timer); offEnv(); });
})();
</script>`,
		},
		{
			ID: "demo-tips", Type: ihtml.ItemHTML, Title: "使用提示", Order: 90,
			Content: `<div class="ui-page" style="padding-top:0">
  <div class="ui-card">
    <div class="ui-title" style="font-size:15px">试试对右下角的 AI 助手说</div>
    <ul class="ui-muted" style="margin:6px 0 0;padding-left:18px;line-height:2">
      <li>"做一个运营看板: 三张统计卡+近7日明细表格, 用运营日报接口" —— AI 会先探测真实 API 再生成取数 UI</li>
      <li>"我需要多个页面和左侧菜单, 把看板和玩家管理分开" —— 才会升级成多页布局</li>
      <li>"把主色换成翡翠绿, 暗色模式" / "页面有报错吗? 修一下"</li>
    </ul>
    <p class="ui-muted" style="margin:10px 0 0;font-size:12.5px">另: <a href="/host-demo">宿主嵌入演示</a> —— 本内核被 mount 进"别人的产品"里的样子。</p>
  </div>
</div>`,
		},
	}
	if err := h.PutItems(ctx, "demo", demo, "api", "预置单页演示数据"); err != nil {
		log.Printf("预置演示数据失败: %v", err)
	}
}

// seedHostDemo 为宿主嵌入演示预置一个多页用户: 菜单会出现在宿主的菜单槽里。
func seedHostDemo(h *ihtml.Handler) {
	ctx := context.Background()
	if items, err := h.ListItems(ctx, "hostdemo"); err != nil || len(items) > 0 {
		return
	}
	_ = h.SetPage(ctx, "hostdemo", ihtml.Page{Name: "board", Title: "工作台", Icon: "📊", Order: 10})
	_ = h.SetPage(ctx, "hostdemo", ihtml.Page{Name: "reports", Title: "报表", Icon: "📈", Order: 20})
	items := []ihtml.Item{
		{
			ID: "board-main", Type: ihtml.ItemHTML, Title: "工作台", Order: 10, Page: "board",
			Content: `<div class="ui-page" style="padding:var(--ui-gap) 0">
  <div class="ui-grid">
    <div class="ui-card"><div class="ui-stat"><span class="label">今日访问</span><span class="value primary">1,286</span><span class="sub up">+12% 较昨日</span></div></div>
    <div class="ui-card"><div class="ui-stat"><span class="label">处理中工单</span><span class="value">7</span><span class="sub">2 件超时</span></div></div>
    <div class="ui-card"><div class="ui-stat"><span class="label">系统状态</span><span class="value" style="font-size:17px"><span class="ui-tag ok">全部正常</span></span></div></div>
  </div>
</div>`,
		},
		{
			ID: "reports-main", Type: ihtml.ItemHTML, Title: "报表", Order: 10, Page: "reports",
			Content: `<div class="ui-page" style="padding:var(--ui-gap) 0">
  <div class="ui-table-wrap"><table class="ui-table">
    <thead><tr><th>日期</th><th class="num">收入</th><th class="num">支出</th><th>状态</th></tr></thead>
    <tbody>
      <tr><td>2026-07-10</td><td class="num">¥12,340</td><td class="num">¥3,210</td><td><span class="ui-tag ok">已对账</span></td></tr>
      <tr><td>2026-07-11</td><td class="num">¥9,870</td><td class="num">¥2,455</td><td><span class="ui-tag warn">待复核</span></td></tr>
    </tbody>
  </table></div>
</div>`,
		},
	}
	if err := h.PutItems(ctx, "hostdemo", items, "api", "预置宿主嵌入演示数据"); err != nil {
		log.Printf("预置 hostdemo 数据失败: %v", err)
	}
}
