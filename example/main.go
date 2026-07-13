// 演示程序: 起一个带演示数据的多页 ihtml 服务。
//
//	go run ./example                       # 内存存储，仅监听本机
//	go run ./example -data ./uidata        # 文件存储
//	go run ./example -config ./example/ihtml.example.json
//
// 配置真实 AI(可选, 不配置则挂内置"演示助手"):
//
//	export IHTML_AI_BASE_URL=https://ai.im.app/v1        # 任意 OpenAI 兼容网关
//	export IHTML_AI_API_KEY=sk-...
//	export IHTML_AI_THINKING=1
//	export IHTML_AI_ENABLE_DEEP=1                        # 可选；默认仅开放 fast
//
// Anthropic 原生协议也可直接复用 ANTHROPIC_BASE_URL、ANTHROPIC_API_KEY /
// ANTHROPIC_AUTH_TOKEN 与 ANTHROPIC_MODEL 环境变量。
//
// 绑定 casino 真实数据(可选): 服务端持有 casino admin 会话,
// 把 /capi/* 只读转发到 casino, 并把 API 目录告知 AI:
//
//	export IHTML_CASINO_BASE=http://192.168.139.2:9090
//	export IHTML_CASINO_USER=admin IHTML_CASINO_PASS=admin
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/chat"
)

func main() {
	configPath := flag.String("config", "", "服务端 JSON 配置文件（也可用 IHTML_CONFIG 指定）")
	addr := flag.String("addr", "127.0.0.1:8899", "监听地址")
	data := flag.String("data", "", "数据目录, 留空用内存存储")
	flag.Parse()
	explicitFlags := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { explicitFlags[f.Name] = true })

	selectedConfigPath := strings.TrimSpace(*configPath)
	if selectedConfigPath == "" {
		selectedConfigPath = strings.TrimSpace(os.Getenv("IHTML_CONFIG"))
	}
	var fileConfig *exampleFileConfig
	if selectedConfigPath != "" {
		var err error
		fileConfig, err = loadExampleFileConfig(selectedConfigPath)
		if err != nil {
			log.Fatalf("加载服务端配置失败: %v", err)
		}
		configuredAddr, configuredData, err := fileConfig.serverValues()
		if err != nil {
			log.Fatalf("服务端配置无效: %v", err)
		}
		if !explicitFlags["addr"] && configuredAddr != "" {
			*addr = configuredAddr
		}
		if !explicitFlags["data"] && configuredData != "" {
			*data = configuredData
		}
	}

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
		// 仅演示环境允许 ?uid=；生产必须换成验证过会话/令牌的 UserResolver。
		ihtml.WithUserResolver(ihtml.DevelopmentUserResolver),
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

	probeBase := ""
	var probeQuery []string
	if os.Getenv("IHTML_CASINO_BASE") != "" {
		// api_probe 只回连本演示服务；显式转发 demo 的 uid，模型不能覆盖。
		probeBase = loopbackOrigin(*addr)
		probeQuery = []string{"uid"}
	}
	var configuredChat *chat.Config
	if !envBool("IHTML_AI_DISABLED") {
		if fileConfig != nil {
			cfg, enabled, err := fileConfig.chatConfig(probeBase, probeQuery)
			if err != nil {
				log.Fatalf("AI 配置无效: %v", err)
			}
			if enabled {
				configuredChat = &cfg
			}
		} else if base := envFirst("IHTML_AI_BASE_URL", "ANTHROPIC_BASE_URL"); base != "" {
			cfg := exampleChatConfig(base, probeBase, probeQuery)
			configuredChat = &cfg
			log.Printf("未指定 -config，使用兼容环境变量加载 AI；建议迁移到版本化服务端配置文件")
		}
	}
	if configuredChat != nil {
		cfg := *configuredChat
		backend, err := chat.NewChecked(cfg)
		if err != nil {
			log.Fatalf("AI 配置无法启动: %v", err)
		}
		opts = append(opts, ihtml.WithChatBackend(backend))
		modes := chat.AgentModeFast
		if cfg.EnableDeepAgent {
			modes += "," + chat.AgentModeDeep
		}
		log.Printf("AI 对话已启用(真实模型): provider=%s model=%s endpoint=%s agent_modes=%s", cfg.Provider, cfg.Model, safeEndpointLabel(cfg.BaseURL), modes)
	} else {
		opts = append(opts, ihtml.WithChatBackend(&demoBackend{}))
		log.Printf("未启用真实 AI，已挂载内置演示助手(不调用真实模型)")
	}

	mux := http.NewServeMux()

	// casino 数据绑定: 只读代理 + API 目录
	if casinoBase := os.Getenv("IHTML_CASINO_BASE"); casinoBase != "" {
		if !isLoopbackBind(*addr) {
			log.Fatal("启用 IHTML_CASINO_BASE 时 -addr 必须绑定 loopback，避免暴露管理端代理")
		}
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

	// 跨域演示必须显式指定精确 Origin；默认仅同源。
	if raw := strings.TrimSpace(os.Getenv("IHTML_DEMO_CORS_ORIGINS")); raw != "" {
		origins := strings.Split(raw, ",")
		for i := range origins {
			origins[i] = strings.TrimSpace(origins[i])
		}
		opts = append(opts, ihtml.WithCORS(origins...), ihtml.WithMessageOrigins(origins...))
	}

	handler, err := ihtml.NewHandler(store, opts...)
	if err != nil {
		log.Fatalf("ihtml Handler 配置无效: %v", err)
	}
	defer handler.Close()
	seedDemo(handler)
	seedHostDemo(handler)
	mux.Handle("/", handler)
	mux.HandleFunc("GET /host-demo", hostDemoPage)

	log.Printf("演示页面: %s/?uid=demo (空白用户: ?uid=blank)", loopbackOrigin(*addr))
	log.Printf("宿主嵌入演示: %s/host-demo (mount 进宿主自己的布局)", loopbackOrigin(*addr))
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Printf("HTTP 服务已停止: %v", err)
	}
}

func safeEndpointLabel(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return "invalid"
	}
	return parsed.Scheme + "://" + parsed.Host + parsed.EscapedPath()
}

func loopbackOrigin(addr string) string {
	addr = strings.TrimSpace(addr)
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func isLoopbackBind(addr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isLoopbackRequestHost(hostport string) bool {
	host := strings.TrimSpace(hostport)
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	} else {
		host = strings.Trim(host, "[]")
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isLocalSameOriginRequest(r *http.Request) bool {
	if !isLoopbackRequestHost(r.Host) || strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	rawOrigin := strings.TrimSpace(r.Header.Get("Origin"))
	if rawOrigin == "" {
		return true // 服务端 api_probe/curl；浏览器跨站请求会带 Fetch Metadata。
	}
	origin, err := url.Parse(rawOrigin)
	if err != nil || origin.Host == "" || !strings.EqualFold(origin.Host, r.Host) {
		return false
	}
	wantScheme := "http"
	if r.TLS != nil {
		wantScheme = "https"
	}
	return strings.EqualFold(origin.Scheme, wantScheme)
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
  @media (max-width:720px) {
    .host-top { align-items:flex-start; padding:12px 14px; }
    .host-top nav { overflow-x:auto; white-space:nowrap; }
    .host-wrap { display:block; min-height:0; }
    .host-side { border-right:0; border-bottom:1px solid #22304d; padding:10px; }
    .host-main { padding:12px; }
  }
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
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envFirst(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

const (
	defaultClaudeMaxTokensForExample      = 4096
	defaultClaudeThinkingBudgetForExample = 1024
)

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envPositiveInt(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		log.Printf("忽略无效 %s=%q（必须是正整数），使用默认值 %d", key, raw, def)
		return def
	}
	return value
}

func envDurationMS(def time.Duration, keys ...string) time.Duration {
	for _, key := range keys {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			log.Printf("忽略无效 %s=%q（必须是正整数毫秒），使用默认值 %s", key, raw, def)
			return def
		}
		return time.Duration(value) * time.Millisecond
	}
	return def
}

// exampleChatConfig 集中声明演示程序的 AI 安全边界。Deep 必须通过环境变量
// 显式开放；即使开放，未携带 agent_mode=deep 的对话仍然走 fast。
func exampleChatConfig(base, probeBase string, probeQuery []string) chat.Config {
	defaultProvider := chat.ProviderOpenAI
	if strings.TrimSpace(os.Getenv("IHTML_AI_BASE_URL")) == "" && strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL")) != "" {
		defaultProvider = chat.ProviderClaude
	}
	provider := envOr("IHTML_AI_PROVIDER", defaultProvider)
	model := strings.TrimSpace(os.Getenv("IHTML_AI_MODEL"))
	apiKey := strings.TrimSpace(os.Getenv("IHTML_AI_API_KEY"))
	authToken := strings.TrimSpace(os.Getenv("IHTML_AI_AUTH_TOKEN"))
	hasIHTMLCredential := apiKey != "" || authToken != ""
	maxTokens := 0
	thinkingBudget := 0
	if provider == chat.ProviderClaude {
		// Do not combine credentials from two independently configured gateways.
		// Presence of either IHTML credential makes that source authoritative.
		if !hasIHTMLCredential {
			apiKey = strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
			authToken = strings.TrimSpace(os.Getenv("ANTHROPIC_AUTH_TOKEN"))
		}
		if model == "" {
			// Fast 是默认执行模式，因此 Claude Code 风格配置优先采用 Haiku
			// 映射；需要其他模型时用 IHTML_AI_MODEL 明确覆盖。
			model = envFirst("ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL")
		}
		maxTokens = envPositiveInt("IHTML_AI_MAX_TOKENS", defaultClaudeMaxTokensForExample)
		thinkingBudget = envPositiveInt("IHTML_AI_THINKING_BUDGET_TOKENS", defaultClaudeThinkingBudgetForExample)
	}
	return chat.Config{
		Provider:             provider,
		BaseURL:              strings.TrimSpace(base),
		APIKey:               apiKey,
		AuthToken:            authToken,
		Model:                model,
		Stream:               true,
		Thinking:             envBool("IHTML_AI_THINKING"),
		ThinkingBudgetTokens: thinkingBudget,
		RequestTimeout:       envDurationMS(180*time.Second, "IHTML_AI_REQUEST_TIMEOUT_MS", "API_TIMEOUT_MS"),
		MaxTokens:            maxTokens,
		EnableDeepAgent:      envBool("IHTML_AI_ENABLE_DEEP"),
		MaxModelCallsPerTurn: envPositiveInt("IHTML_AI_MAX_MODEL_CALLS", 24),
		MaxSubtasksPerTurn:   envPositiveInt("IHTML_AI_MAX_SUBTASKS", 3),
		MaxTurnDuration:      time.Duration(envPositiveInt("IHTML_AI_MAX_TURN_SECONDS", 120)) * time.Second,
		AllowModelSelect:     true, // 演示环境放开模型选择器(生产默认隐藏)
		ProbeBaseURL:         probeBase,
		ProbeForwardQuery:    append([]string(nil), probeQuery...),
	}
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

// extractJSONField 从 JSON 对象读取字符串字段。
func extractJSONField(raw, field string) string {
	var object map[string]any
	if json.Unmarshal([]byte(raw), &object) != nil {
		return ""
	}
	value, _ := object[field].(string)
	return value
}

func casinoTarget(path string, query url.Values) (string, url.Values, error) {
	allowedQuery := map[string]bool{}
	upstream := ""
	switch {
	case path == "/capi/players":
		upstream = "players"
		allowedQuery["q"], allowedQuery["limit"] = true, true
	case path == "/capi/reports/dashboard":
		upstream = "reports/dashboard"
	case path == "/capi/nodes":
		upstream = "nodes"
	case strings.HasPrefix(path, "/capi/players/") && strings.HasSuffix(path, "/bets"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/capi/players/"), "/bets")
		if id == "" || strings.Contains(id, "/") {
			return "", nil, fmt.Errorf("未声明的 casino API 路径")
		}
		if _, err := strconv.ParseUint(id, 10, 64); err != nil {
			return "", nil, fmt.Errorf("玩家 id 无效")
		}
		upstream = "players/" + id + "/bets"
		allowedQuery["limit"] = true
	default:
		return "", nil, fmt.Errorf("未声明的 casino API 路径")
	}
	forward := make(url.Values)
	for key, values := range query {
		if key == "uid" || key == "auth" {
			continue // ihtml 演示身份只用于本地路由，不转发给 casino
		}
		if !allowedQuery[key] || len(values) != 1 {
			return "", nil, fmt.Errorf("不允许的查询参数 %q", key)
		}
		value := values[0]
		if key == "limit" {
			limit, err := strconv.Atoi(value)
			if err != nil || limit < 1 || limit > 200 {
				return "", nil, fmt.Errorf("limit 必须在 1..200")
			}
		}
		if key == "q" && len(value) > 100 {
			return "", nil, fmt.Errorf("q 过长")
		}
		forward[key] = append([]string(nil), values...)
	}
	return upstream, forward, nil
}

func (p *casinoProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if !isLocalSameOriginRequest(r) {
		http.Error(w, `{"error":"casino 代理仅接受本机同源请求"}`, http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"casino 代理只读, 仅支持 GET"}`, http.StatusMethodNotAllowed)
		return
	}
	upstream, query, err := casinoTarget(r.URL.Path, r.URL.Query())
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}
	target := p.base + "/admin/api/" + upstream
	if rawQuery := query.Encode(); rawQuery != "" {
		target += "?" + rawQuery
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
	return ihtml.ChatModels{
		ProtocolVersion: ihtml.ChatProtocolVersion,
		Provider:        "demo",
		ModelReady:      true,
		CurrentModel:    "demo(演示模式)",
		Models:          []string{"demo(演示模式)"},
		AgentModes:      []string{chat.AgentModeFast},
	}, nil
}

type demoSession struct {
	sctx *ihtml.ChatSessionContext

	mu         sync.Mutex
	cancel     context.CancelFunc
	generation uint64
	closed     bool
	wg         sync.WaitGroup

	inspectMu sync.Mutex
	inspects  map[string]chan *ihtml.ChatClientInspection
}

func (s *demoSession) Close() {
	s.mu.Lock()
	s.closed = true
	s.generation++
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

func (s *demoSession) HandleEvent(ctx context.Context, ev *ihtml.ChatClientEvent) {
	if ev == nil {
		return
	}
	if ev.Type == "cancel" {
		s.stopRun()
		return
	}
	if ev.Type == ihtml.ChatClientInspectResult {
		// 内核对 inspect_request 的应答: 与真实后端相同的配对方式。
		s.inspectMu.Lock()
		ch := s.inspects[ev.InspectID]
		delete(s.inspects, ev.InspectID)
		s.inspectMu.Unlock()
		if ch != nil && ev.Inspect != nil {
			ch <- ev.Inspect
		}
		return
	}
	if ev.Type != "user_message" {
		return
	}
	text := ""
	for i := len(ev.Messages) - 1; i >= 0; i-- {
		if ev.Messages[i].Role == "user" {
			text = strings.TrimSpace(ev.Messages[i].Content)
			break
		}
	}
	s.startRun(ctx, text)
}

func (s *demoSession) stopRun() {
	s.mu.Lock()
	s.generation++
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *demoSession) startRun(parent context.Context, text string) {
	runCtx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return
	}
	previous := s.cancel
	s.generation++
	generation := s.generation
	s.cancel = cancel
	s.wg.Add(1)
	s.mu.Unlock()
	if previous != nil {
		previous()
	}
	go func() {
		defer s.wg.Done()
		defer cancel()
		defer func() {
			s.mu.Lock()
			if s.generation == generation {
				s.cancel = nil
			}
			s.mu.Unlock()
		}()
		s.runDemo(runCtx, generation, text)
	}()
}

func (s *demoSession) currentRun(ctx context.Context, generation uint64) bool {
	if ctx.Err() != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.generation == generation
}

func (s *demoSession) emitRun(ctx context.Context, generation uint64, event ihtml.ChatStreamEvent) bool {
	return s.currentRun(ctx, generation) && s.sctx.Emit(event) == nil
}

func demoWait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *demoSession) runDemo(ctx context.Context, generation uint64, text string) {
	if !s.emitRun(ctx, generation, ihtml.ChatStreamEvent{Type: "turn_start", ModelReady: true}) ||
		!s.emitRun(ctx, generation, ihtml.ChatStreamEvent{Type: "assistant_status", Status: "正在理解需求。", ModelReady: true}) ||
		!demoWait(ctx, 300*time.Millisecond) ||
		!s.emitRun(ctx, generation, ihtml.ChatStreamEvent{Type: "assistant_thinking", Thinking: "演示模式: 把用户的话写成一张卡片放到当前页面。", ModelReady: true}) {
		return
	}
	call := &ihtml.ProposedToolCall{
		ID: fmt.Sprintf("demo_%d", time.Now().UnixNano()), Name: "ui_item_put", Title: "写入/更新 Item",
		Method: "TOOL", Path: "ui://ui_item_put", Risk: "medium",
	}
	if !s.emitRun(ctx, generation, ihtml.ChatStreamEvent{Type: "tool_start", ToolCall: call, ToolName: call.Name, ToolTitle: call.Title, ToolStatus: "running", ToolInput: `{"items":[{"id":"ai-note",…}]}`, ModelReady: true}) ||
		!demoWait(ctx, 300*time.Millisecond) {
		return
	}
	note := text
	if note == "" {
		note = "(空消息)"
	}
	// 与 cancel/new-turn 在线性化点互斥；取消若先到达，这里不会写入。
	s.mu.Lock()
	if s.closed || s.generation != generation || ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	err := s.sctx.Service.PutItems(ctx, s.sctx.User, []ihtml.Item{{
		ID: "ai-note", Type: ihtml.ItemHTML, Order: 26, Title: "AI 回应卡片",
		Content: `<div class="ui-page" style="padding-top:0;padding-bottom:0"><div class="ui-card"><div class="ui-stat"><span class="label">AI 刚写入的卡片</span><span class="value" style="font-size:17px">` + html.EscapeString("你说: "+note) + `</span></div></div></div>`,
	}}, "chat", "演示助手写入回应卡片")
	s.mu.Unlock()
	if err != nil {
		_ = s.emitRun(ctx, generation, ihtml.ChatStreamEvent{Type: "tool_result", ToolCall: call, ToolName: call.Name, ToolTitle: call.Title, ToolStatus: "error", ToolSummary: err.Error(), ModelReady: true})
		_ = s.emitRun(ctx, generation, ihtml.ChatStreamEvent{Type: "turn_error", Error: err.Error(), ModelReady: true})
		return
	}
	if !s.emitRun(ctx, generation, ihtml.ChatStreamEvent{Type: "tool_result", ToolCall: call, ToolName: call.Name, ToolTitle: call.Title, ToolStatus: "success", ToolSummary: "已写入 1 个 Item: ai-note(当前页)", ModelReady: true}) {
		return
	}
	// 写后自检演示: 走与真实后端一致的 inspect_request/inspect_result 往返,
	// 读取内核回传的实时渲染状态。先等一拍, 让 SSE put 事件先在内核应用。
	if !demoWait(ctx, 400*time.Millisecond) {
		return
	}
	inspectNote := s.demoInspect(ctx, generation)
	for _, chunk := range []string{"<p>已把你的话写成一张卡片放进页面。", inspectNote, "接真实模型后我能真正理解需求、取业务数据、改主题、修报错。</p>"} {
		if chunk == "" {
			continue
		}
		if !s.emitRun(ctx, generation, ihtml.ChatStreamEvent{Type: "assistant_delta", Delta: chunk, ModelReady: true}) || !demoWait(ctx, 120*time.Millisecond) {
			return
		}
	}
	_ = s.emitRun(ctx, generation, ihtml.ChatStreamEvent{Type: "assistant_done", ModelReady: true})
}

// demoInspect 发出 inspect_request 并等待内核应答, 返回一句自检结论。
// 未应答(超时)时返回空串, 不影响演示流程。
func (s *demoSession) demoInspect(ctx context.Context, generation uint64) string {
	inspectID := fmt.Sprintf("demo_ins_%d", time.Now().UnixNano())
	ch := make(chan *ihtml.ChatClientInspection, 1)
	s.inspectMu.Lock()
	if s.inspects == nil {
		s.inspects = make(map[string]chan *ihtml.ChatClientInspection)
	}
	s.inspects[inspectID] = ch
	s.inspectMu.Unlock()
	defer func() {
		s.inspectMu.Lock()
		delete(s.inspects, inspectID)
		s.inspectMu.Unlock()
	}()
	if !s.emitRun(ctx, generation, ihtml.ChatStreamEvent{
		Type: ihtml.ChatStreamInspectRequest, InspectID: inspectID,
		InspectLimit: 1, InspectIDs: []string{"ai-note"}, ModelReady: true,
	}) {
		return ""
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case inspect := <-ch:
		return demoInspectionNote(inspect)
	case <-ctx.Done():
		return ""
	case <-timer.C:
		return ""
	}
}

func demoInspectionNote(inspect *ihtml.ChatClientInspection) string {
	if inspect != nil {
		for _, item := range inspect.Items {
			if item.ID != "ai-note" {
				continue
			}
			if !item.Present {
				return "我又用 ui_page_inspect 自检了一次: 还没有找到 ai-note 的已连接 DOM。"
			}
			if strings.TrimSpace(item.Error) != "" {
				return "我又用 ui_page_inspect 自检了一次: ai-note 已加载, 但检测到运行错误。"
			}
			if item.Visible {
				return "我又用 ui_page_inspect 自检了一次: ai-note 已渲染、在当前视口可见且没有运行错误。"
			}
			return "我又用 ui_page_inspect 自检了一次: ai-note 已加载, 但当前视口中不可见。"
		}
	}
	return "我又用 ui_page_inspect 自检了一次: 内核未返回 ai-note, 暂时不能声称它已正常显示。"
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
	  <div class="ui-toolbar demo-header">
	    <div style="flex:1;min-width:220px">
	      <span class="ui-badge">ihtml · 动态页面</span>
	      <h1 class="ui-title" style="font-size:20px;margin:8px 0 2px" id="demo-hello">工作台</h1>
	      <div class="ui-muted" style="font-size:13px">默认单页形态 · 每个区块都是一个 Item · AI 可增删改并持久化</div>
	    </div>
	    <div class="ui-segment" role="group" aria-label="主题模式" style="margin-left:auto">
	      <button class="ui-btn" type="button" data-demo-theme="light">亮色</button>
	      <button class="ui-btn" type="button" data-demo-theme="dark">暗色</button>
	    </div>
	  </div>
	</div>
	<script>
	(function(){
	  var t = document.getElementById('demo-hello');
	  if (ihtml.config.nickname) t.textContent = ihtml.config.nickname + ' 的工作台';
	  var buttons = Array.from(document.querySelectorAll('[data-demo-theme]'));
	  function renderTheme(theme){
	    buttons.forEach(function(btn){ btn.setAttribute('aria-pressed', String(theme.mode === btn.dataset.demoTheme)); });
	  }
	  buttons.forEach(function(btn){ btn.onclick = function(){ ihtml.theme.set({mode:btn.dataset.demoTheme}); }; });
	  renderTheme(ihtml.theme.get());
	  var offTheme = ihtml.theme.onChange(renderTheme);
	  ihtml.items.onTeardown('demo-head', offTheme);
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
	  function renderEnv(){ envEl.textContent = ihtml.env.mode === 'mobile' ? 'Mobile' : 'Desktop'; }
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
