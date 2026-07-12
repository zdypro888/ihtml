# ihtml — AI 动态前端 UI 功能库

一个 Go 库：内嵌一张"空壳页面"（微内核），按用户加载服务端记录的一组 **UI Item**（HTML/JS/CSS 片段）并在浏览器里执行，拼装出该用户的完整界面。Item 由 **AI 助手通过对话生成并持久化** —— 用户说"加一张行情卡片"，AI 写入 Item，页面实时出现，下次打开依然在。

```
浏览器空壳页(内核)  ──参数──▶  Go 服务端  ──UserResolver──▶  用户
      ▲                        │
      │  Items(html/js/css)    │ Store(内存/文件/自定义数据库)
      │◀── REST + SSE 实时 ────┤
      │                        │
      └── WebSocket 对话 ──▶ AI 后端(eino: OpenAI 兼容/Claude) ──工具──▶ 改 Item/主题/KV/回滚
```

## 特性

- **微内核空壳页**：无构建、无框架、内嵌二进制；Item 层想用什么技术都行（JS 可自由 `fetch`）
- **按用户持久化**：同一个 URL，不同用户是完全不同的应用
- **实时上屏**：SSE 推送，AI 改完不用刷新
- **主题系统**：CSS 变量 tokens + 明暗模式 + 任意主色（`color-mix` 派生），一处换肤全局生效
- **安全网**：默认拒绝未接入鉴权的请求；每次变更自动快照可回滚；`?safe=1` 安全模式救砖；Item 报错自动收集
- **AI 对话**：eino ADK 多轮会话，默认低延迟 Fast、可显式开放 DeepAgent 多 Agent 编排；语义工具 + 源码写入/高危操作 HMAC 确认卡 + 有界历史与独立写限流

### SDK 包边界

| 包 | 第三方用途 |
|---|---|
| `ihtml` | 稳定核心：HTTP Handler、Store/Service、模板、事件与 ChatBackend 协议；AI 完全可选 |
| `ihtml/chat` | 内置 Eino AI 后端；程序化入口是 `chat.Config` + `chat.NewChecked` |
| `ihtml/chat/config` | 严格、版本化、可嵌入宿主配置的 AI JSON loader；SecretResolver 显式注入 |
| `ihtml/sqlstore` | `database/sql` 持久化实现；驱动与 `*sql.DB` 生命周期由宿主拥有 |

旧的 `ihtml.New` / `chat.New` 为兼容保留；新集成推荐 `NewHandler` / `NewChecked`，让所有配置错误在开始监听前返回。公共协议字符串均有导出常量，模型能力响应带 `protocol_version`。当前仍处于 pre-v1 阶段；发布 v1 前会继续保持新增优先、破坏性调整集中并提供迁移说明。

完整兼容约定见 [COMPATIBILITY.md](COMPATIBILITY.md)，未发布变更见 [CHANGELOG.md](CHANGELOG.md)。正式发版前仍需由项目所有者确认 License、最低 Go 版本与首个 semver tag。

## 嵌入宿主应用（SDK 形态）

宿主页面只需引入内核 js/css 并声明一个挂载点——**零 JS 代码**：

```html
<link rel="stylesheet" href="https://ui.example.com/ihtml.css">
<script src="https://ui.example.com/ihtml.js"></script>

<div id="their-nav"><!-- 宿主自己的侧栏里留给 ihtml 菜单的位置 --></div>
<div data-ihtml
     data-base="https://ui.example.com/"  
     data-uid="alice"
     data-theme="inherit"
     data-menu="#their-nav"></div>
```

- `data-theme="inherit"`：挂载时探测宿主计算样式（背景亮度→明暗、正文色、字体、链接色→主色）自动融入宿主观感；也可显式 `data-theme='{"primary":"var(--brand-600)","mode":"dark"}'` 直接映射宿主设计变量
- `data-menu`：多页时菜单渲染进宿主指定容器（单页/无页面时自动隐藏）；嵌入默认**内存路由**，不占宿主 `location.hash`
- 内核样式全部收在 `.ihtml-root` 作用域内；嵌入时 Item CSS 也默认用 `@scope (.ihtml-root)` 隔离。跨域部署时服务端加 `ihtml.WithCORS("https://host.example.com")`
- 等价的编程式：`ihtml.mount(el, {base, auth, connectionAuth, theme, routing, menu, chatEntry})`；宿主随时可调 `ihtml.theme.set/nav.go/chat.open`
- 也支持 iframe 嵌入独立页。跨域父页面必须用 `WithMessageOrigins("https://host.example.com")` 显式放行 postMessage 与 CSP `frame-ancestors`
- Item 中声明式的外部 `<script src>` / `<link rel=stylesheet>` 默认拒绝；只有受控宿主可显式设置 `allowExternalAssets:true`。这不是沙箱：Item 是可信可执行代码，仍可通过 JS、图片或 CSS 发起网络请求。兼容旧 CSS 时可临时设置 `scopeStyles:false`

## 快速开始

```go
package main

import (
    "log"
    "net/http"
    "os"

    "github.com/zdypro888/ihtml"
    "github.com/zdypro888/ihtml/chat"
)

func main() {
    store, err := ihtml.NewFileStore("./uidata") // 或 NewMemoryStore() / 自己实现 Store
    if err != nil {
        log.Fatal(err)
    }

    ai, err := chat.NewChecked(chat.Config{
        Provider: chat.ProviderOpenAI,
        BaseURL:  "https://api.openai.com/v1",
        APIKey:   os.Getenv("OPENAI_API_KEY"), // 或由宿主秘密管理系统注入
        Model:    "gpt-4o-mini",
        Stream:   true,
    })
    if err != nil {
        log.Fatal(err) // 在监听端口前发现配置问题
    }

    handler, err := ihtml.NewHandler(store,
        // 仅用于本地体验；生产必须换成验证宿主会话/令牌的 resolver。
        // 不配置 resolver 时 ihtml 会安全地返回 401。
        ihtml.WithUserResolver(ihtml.DevelopmentUserResolver),
        ihtml.WithChatBackend(ai), // AI 对话完全可选
    )
    if err != nil {
        log.Fatal(err)
    }
    defer handler.Close()

    mux := http.NewServeMux()
    mux.Handle("/ui/", http.StripPrefix("/ui", handler)) // 挂任意子路径或根路径
    if err := http.ListenAndServe(":8080", mux); err != nil {
        log.Print(err)
    }
}
```

> `DevelopmentUserResolver` 信任客户端可修改的 `?uid=` / header / cookie，只能用于演示和测试。生产 resolver 应从宿主已经验签的会话或令牌中返回用户，空用户或错误会得到 401。

体验演示（含预置数据；未配置真实模型时自动挂载"演示助手"，完整走一遍对话事件流）：

```bash
go run ./example                 # http://127.0.0.1:8899/?uid=demo，仅监听本机
go run ./example -data ./uidata  # 文件持久化
```

推荐用版本化服务端配置文件管理 Provider、模型、Thinking、Fast/Deep 预算、历史和确认策略。AI section 自带独立的 `schema_version`，不会被宿主自己的配置版本绑死：

```bash
cp example/ihtml.example.json example/ihtml.local.json
# 编辑 ihtml.local.json；该文件已加入 .gitignore。

# 示例文件只引用密钥名称，不保存密钥值。
export ANTHROPIC_AUTH_TOKEN=your-token
go run ./example -config example/ihtml.local.json
```

`-config` 优先于 `IHTML_CONFIG`。这是 demo 启动器的策略，不是核心库的全局行为。只要指定配置文件，JSON 就是全部非密钥 AI 字段的唯一权威，不再被 ambient `IHTML_AI_*` / `ANTHROPIC_*` 标量覆盖；只有 `auth.env` / `confirmation_secret.env` 会读取明确命名的环境变量。也可以完全不使用环境变量，改用只读密钥文件：

```json
"auth": {
  "type": "auth_token",
  "file": "/run/secrets/ihtml-anthropic-token"
}
```

密钥文件必须是绝对路径、普通非符号链接文件，Unix 权限为 `0600` 或 `0400`。配置采用严格 JSON：限制 64 KiB、拒绝未知字段/重复键/尾随文档，并在监听端口前校验 Provider、HTTPS BaseURL、模型、认证、Thinking、Deep 预算与超时；显式配置无效会终止启动，绝不会静默降级到模拟助手。`IHTML_AI_DISABLED=1` 可作为紧急熔断。

这套加载能力不是 example 私有代码。第三方宿主可直接导入 `github.com/zdypro888/ihtml/chat/config`：

```go
loader := chatconfig.NewLoader(chatconfig.Options{
    // 必须显式注入；nil 时 loader 绝不会偷偷读取 ANTHROPIC_* / IHTML_*。
    Secrets: chatconfig.NewOSSecretResolver(),
})
result, err := loader.DecodeSection(ctx, appConfig.AI) // appConfig.AI 是 json.RawMessage
if err != nil {
    // errors.Is(err, chatconfig.ErrInvalidConfig / ErrSecretUnavailable)
    // errors.As(err, *chatconfig.Error) 可取得稳定的 Kind 与 Path。
    return err
}
if result.Enabled {
    // SessionStore、拦截器、Probe 身份等代码能力仍由宿主注入，不从 JSON 构造。
    backend, err := chat.NewChecked(result.Config)
    // ... ihtml.WithChatBackend(backend)
}
```

Secret Manager/Vault/KMS 用户实现 `chatconfig.SecretResolver` 即可；JSON 使用 `{"ref":"vault://..."}`，loader 只把该显式引用交给宿主 resolver。公共 loader 没有 provider 环境变量推断、全局注册表或静默 fallback。

不使用配置文件时仍兼容旧环境变量启动方式：

```bash
# 接真实模型(任意 OpenAI 兼容网关 / Ollama / exo):
export IHTML_AI_BASE_URL=https://api.openai.com/v1
export IHTML_AI_API_KEY=sk-...
export IHTML_AI_MODEL=gpt-4o-mini   # 可省: 会自动选网关上第一个已加载模型

# 可选：服务端开放 Deep；不设置时模型列表只公布 fast。
export IHTML_AI_ENABLE_DEEP=1
# 可选：Deep 根 Agent 与只读子 Agent 共享的硬预算（以下为默认值）。
export IHTML_AI_MAX_MODEL_CALLS=24 IHTML_AI_MAX_SUBTASKS=3 IHTML_AI_MAX_TURN_SECONDS=120
go run ./example
```

Anthropic Messages 协议（Eino Claude adapter）也可直接复用 Claude Code 风格环境变量。`BaseURL` 写协议根路径，SDK 会自动追加 `/v1/messages`：

```bash
export ANTHROPIC_BASE_URL=https://gateway.example/api/anthropic
export ANTHROPIC_AUTH_TOKEN=your-token
export ANTHROPIC_DEFAULT_HAIKU_MODEL=your-fast-model

# IHTML_AI_* 始终具有更高优先级；用它明确选择本应用的单一默认模型。
export IHTML_AI_MODEL=your-model
export IHTML_AI_THINKING=1
export IHTML_AI_THINKING_BUDGET_TOKENS=1024
export IHTML_AI_MAX_TOKENS=4096
export IHTML_AI_ENABLE_DEEP=1
go run ./example
```

也可使用 `ANTHROPIC_API_KEY`（`x-api-key`）或 `IHTML_AI_AUTH_TOKEN`（Bearer）。未设置 `IHTML_AI_MODEL` 时，演示程序依次读取 `ANTHROPIC_MODEL`、`ANTHROPIC_DEFAULT_HAIKU_MODEL`、Sonnet、Opus 映射；Fast 是默认模式，因此优先 Haiku 映射。

## 核心概念

### UI Item

```json
{
  "id": "btc-card",          // 语义化唯一标识, 同 id 重写 = 原地覆盖
  "type": "html",            // html | js | css
  "title": "BTC 价格卡片",
  "page": "dash",            // 所属页面; 留空 = 全局(菜单/顶栏/全局样式)
  "order": 20,               // 应用顺序
  "content": "<div class=\"ui-card\">…</div>",
  "meta": {"slot": "sidebar"} // 可选; html 绑定当前 Template Slot.id; js 可设 module
}
```

- `html`：渲染进 `<section data-ihtml-id="…">`，按 order 排列，内嵌 `<script>` 会执行
- `css`：注入 `<style>`；嵌入模式默认限定在 `.ihtml-root`；`js`：注入 `<script>` 执行（宽容处理误带的 `<script>` 标签包裹）
- 单 Item ≤ 512KB，单用户 ≤ 512 个

### 页面与路由（多页应用）

**默认是单页形态**（Item 全部 `page:""`，无菜单无 shell）——适合作为宿主产品里的一个页面；AI 也被教导只有用户明确要"多页/菜单"时才升级。多页时：注册页面 `{name, title, icon, order}`（页面列表=菜单数据源），路由切页卸载当前页 Item（触发 teardown）→ 加载目标页；独立页面用 hash 路由（`#/bills`），嵌入模式用内存路由。菜单可以是 AI 生成的 sidebar Item（独立页面），也可以渲染进宿主容器（`data-menu` 菜单槽）。

### 插槽布局与 PC/Mobile

未启用已发布 Template 时，html Item 仍可用旧兼容名 `meta.slot: sidebar|header|footer` 挂入 app-shell 对应区域；存在这类 Item 时内核自动启用 app-shell 网格布局。**容器宽度 <768px 时** sidebar 自动变成左侧抽屉 + 汉堡按钮，浮动操作也切成底部操作栏；`.ihtml-root.ihtml-mobile` 类与 `ihtml.env` 供 Item 侧适配。

### 业务 API 目录（AI 取数）

宿主用 `WithAPIs(...)` 声明业务接口（路径+参数/返回说明），目录进入 AI 系统提示。AI 的 `api_probe` 默认禁用；只有同时配置 `chat.Config.ProbeBaseURL` 后才能探测目录白名单内的 GET 路径。身份不会隐式转发，可用 `ProbeForwardHeaders`、`ProbeForwardQuery` 或 `ProbeRequest` 显式注入。Item 的 JS 直接 `fetch` 同源接口；配合 `WithPageConfig` 可注入每用户动态参数（`ihtml.config`）。

### 页面运行时 `window.ihtml`

Item 代码的"标准库"（也是 AI system prompt 里的 API 文档）：

| API | 用途 |
|---|---|
| `ihtml.nav.go/current/pages/onChange` | 路由：切页、当前页、页面列表（=菜单数据源）、监听 |
| `ihtml.env` / `env.onChange(fn)` | `{mode:'pc'|'mobile', width, touch}` 设备感知 |
| `ihtml.config` | 宿主经 `WithPageConfig` 注入的动态参数（只读） |
| `ihtml.layout.sidebar(open)` | 移动端抽屉菜单开合 |
| `ihtml.http.get/post/put/del(path)` | 同源请求封装，自动透传用户参数 |
| `ihtml.kv.get/set/del(key)` | 用户级持久化键值（服务端存储） |
| `ihtml.bus.on/emit(name, data)` | Item 间解耦通讯 |
| `ihtml.theme.get/set(patch)/onChange(fn)` | 主题读改，如 `set({mode:'dark', primary:'#059669'})` |
| `ihtml.ui.toast(msg, type)` / `ui.confirm({...})` | 通知与确认 |
| `ihtml.items.onTeardown(id, fn)` | **必须**：定时器/监听器的清理钩子，Item 被覆盖时调用 |
| `ihtml.items.apply/remove/refresh/save/delete` | 运行时增删 Item |
| `ihtml.errors.report/list` | 错误上报与查看 |
| `ihtml.chat.open()` | 唤起 AI 对话抽屉 |

### 主题 tokens

Item 样式引用 CSS 变量即可自动适配换肤与明暗模式：

`--ui-primary / --ui-primary-contrast / --ui-primary-soft / --ui-primary-border / --ui-bg / --ui-surface / --ui-surface-muted / --ui-surface-hover / --ui-text / --ui-text-muted / --ui-border / --ui-control-border / --ui-shadow / --ui-ok / --ui-err / --ui-warn / --ui-radius / --ui-gap / --ui-font`

基础形态类：`.ui-card .ui-btn .ui-btn.primary .ui-input .ui-select .ui-badge .ui-title .ui-muted`

## HTTP API

挂载点相对路径（全部要求 UserResolver 能解析出用户）：

| 方法与路径 | 说明 |
|---|---|
| `GET /` | 空壳页面（`?safe=1` 安全模式：不执行 Item，可回滚） |
| `GET/PUT/POST /api/items` | 列出 / 增改 Item（`?page=x` 过滤：全局+该页；`&global=0` 仅该页） |
| `DELETE /api/items?id=a&id=b` | 删除 |
| `GET/PUT /api/pages` · `DELETE /api/pages/{name}` | 页面注册表（删除页面连带删除页内 Item） |
| `GET/POST /api/templates` · `GET/PUT/DELETE /api/templates/{id}` | 一等布局模板 CRUD；写入使用 `expected_revision` 做 CAS |
| `GET/PUT /api/templates/current?page=x` | 读取/保存 root 或页面当前模板；空模板合法，旧 `meta.slot/layout.*` 只读渐进迁移 |
| `POST /api/templates/{id}/publish` | 把当前 Draft 发布为不可变值快照（体含 `expected_revision`） |
| `DELETE /api/templates/{id}/slots/{slot}?expected_revision=N` | 删除 Slot；Item 保留，关联 Placement 自动迁入未分配区 |
| `POST /api/items/replace` | 整体替换 |
| `GET /api/revisions` · `GET /api/revisions/{id}` | 快照列表 / 内容 |
| `POST /api/revisions/{id}/rollback` | 回滚（回滚前自动再存一份快照） |
| `GET/PUT/DELETE /api/kv/{key}` | 用户级 KV（体：`{"value":…}`；`__ihtml` 前缀保留） |
| `GET /api/events` | SSE：带单调 `version` 的变更事件；订阅、溢出或周期校准会发送 `resync` |
| `GET/POST /api/errors` | 页面错误查看 / 上报 |
| `GET /api/config` | `{"user":"…","chat":true}` |
| `GET /api/chat/models` · `GET /api/chat/ws` | AI 模型列表 / 对话 WebSocket |

每次 Item、Page 或 Template 变更（含 AI 发起）都会：先快照（默认保留 50 份，`WithMaxRevisions` 调整）→ 落库 → SSE 广播。

Template 只定义可复用结构。新业务 Item 通过 `Item.Meta["slot"] == Slot.id` 绑定；`TemplateVersion.Placements` 仅用于旧 `layout.*` 迁移或明确的页面实例覆盖，模板编辑器不应因创建 Container/Slot 而写入业务 ItemID。Slot 缺失、已删除或 Placement 标记 `unassigned` 时，运行时把 Item 追加到 Root 末尾，不能静默隐藏。

`POST /api/templates` 创建的是未绑定模板，不会仅因 ID 恰好为 `tpl-root` 或 `tpl-page-*` 就自动上线；必须通过 `PUT /api/templates/current` 或 `Page.template_id` 显式绑定。普通运行时只应用 `template.published`，绝不回退到 Draft；没有 Published 时继续走旧兼容渲染。

## AI 对话

- 传输：WebSocket，事件格式与 casino admin AI 一致（`user_message` / `assistant_delta` / `tool_start` / `confirmation_required` / `assistant_done` …），前后端经验可直接平移
- 执行模式：未指定 `agent_mode` 或指定 `fast` 时走单 Agent 低延迟路径；只有服务端设置 `EnableDeepAgent=true` 后，客户端才可对复杂任务发送 `agent_mode:"deep"`。因此开放 Deep 不会改变普通对话的默认行为
- DeepAgent 边界：使用 eino ADK DeepAgent 做规划与委派，内置 `ui_inspector` / `ui_reviewer` 子 Agent 只能读取（含 `ui_template_get`，不会获得模板写工具）；所有 UI/Template 写入仍集中在根 Agent，且没有文件系统、Shell 或任意网络工具
- 能力发现：`GET /api/chat/models` 的 `agent_modes` 是服务端实际开放的模式（默认 `["fast"]`，显式开放后为 `["fast","deep"]`）；`deep_approval_resume` 明示确认卡后的恢复能力。`AllowModelSelect=true` 时才可查网关模型目录并由用户选择；生产默认隐藏模型元数据并锁定服务端 `Config.Model`，客户端不能覆盖
- 有界执行：`MaxModelCallsPerTurn`（默认 24）、`MaxSubtasksPerTurn`（默认 3）与 `MaxTurnDuration`（默认 2 分钟）由根/子 Agent 共享；前端可消费结构化 `assistant_plan`、`assistant_agent`、`assistant_budget` 事件展示计划、子任务和预算，而不必解析自然语言
- 过程事件：`assistant_thinking` 是增量流，不是一次性快照；同一次模型输出共享 `thinking_id`，最后一条带 `thinking_done:true`。客户端应按 ID 追加并在 done 后折叠。`assistant_done.message` 是权威最终快照，可覆盖此前的流式草稿；工具按 `tool_call.id` 原位更新，`tool_result.tool_summary` 是面向用户的结果，`tool_result` / `tool_debug` 只适合放在折叠技术详情中
- 确认后恢复：当前 Deep 能力值为 `manual_tool_only`——确认卡只执行卡片中的单个工具调用，整体计划保持暂停，不会在用户不知情时自动续跑；需要继续时由客户端发起新的用户轮次。未来接入 checkpoint 后再通过能力字段升级
- 工具：`ui_item_list/get/put/delete`、`ui_items_replace`、`ui_page_list/set/delete`、`ui_theme_get/set`、`ui_kv_get/set`、`ui_revision_list/rollback`、`ui_page_errors`、`api_probe`（只读探测业务 API），以及结构专用的 `ui_template_get/put_current/publish/slot_delete`
- Template 工具契约：`ui_template_get(page)` 只返回 Draft/Published 结构和 CAS revision，不读取 Item content；`put_current` 只接受完整结构 Draft（服务端严格拒绝未知字段、业务正文、HTML/CSS/JS/URL），保存后不会改变运行时；`publish` 才把 Draft 上线；`slot_delete` 保留 Item 并把引用迁入 Root fallback。所有 Template 写入都必须带 `expected_revision` 并确认
- 风险控制：读操作直接执行；安装 HTML/JS/CSS 的 Item 写入、全部 Template 写入及所有高风险操作默认必须点确认卡（HMAC 签名 + 原子一次性 nonce + 15 分钟 TTL）。只有完全受控环境可用 `AllowUnconfirmedItemWrites=true` 放开普通 Item 写；`ConfirmWrites=true` 会进一步确认其余写操作
- 多轮历史默认在 Backend 级内存中按 csid 恢复，并受会话数、每会话事件数、字节数与 TTL 四重上限约束；生产可注入持久化 `SessionStore`
- 修 bug 闭环：内核收集 Item 报错 → AI 用 `ui_page_errors` 读取 → 重写 Item 修复

## 生产治理（SDK 内建）

| 能力 | 用法 |
|---|---|
| 数据库存储 | `sqlstore.New(db, sqlstore.Postgres/MySQL/SQLite)`（纯 database/sql，`Init()` 建表；已对真实 Postgres 测试） |
| 审计留痕 | `WithAudit(fn)` —— 全部写操作（含 AI 发起）同步回调 `AuditEvent{User,Action,By,Note,Detail}` |
| 访问策略 | `WithAccessPolicy(fn) → Access{ReadOnly, ChatDisabled}` —— 只读视图/按用户禁 AI，HTTP 与 AI 工具双层拦截 |
| 宿主直调 | `Handler.ServiceForRequest(r)` 返回已绑定用户的 `ScopedService`，复用身份重验、访问策略和写限流；`Handler` 自身的 Service 方法是受信任管理 API |
| 限流配额 | `WithLimits{MaxSSEPerUser:8, MaxWSPerUser:4, ChatTurnsPerMinute:20, WritesPerMinute:240, MaxKVKeysPerUser:256}`（默认即生效；对话轮次=真金白银） |
| 指标 | `WithMetrics(m)` 或零依赖 `Handler.Stats()`：sse_connections/ws_sessions/chat_turns/writes/rate_limited… |
| 备份迁移 | `ExportUser/ImportUser/PurgeUser`（Go API）+ `GET /api/export`；v3 契约完整包含 Items/Pages/Templates/KV/Revisions，导入先全量校验并原子替换 |
| 优雅停机 | `Handler.Close()`（断 SSE/WS）；SSE 55 分钟强制到期配合 LB 排空；资源 ETag 304 协商缓存 |
| 多副本 | `WithBroadcaster` 接口化（默认进程内 hub）；**当前用户锁为进程内，多副本需按用户 sticky 路由**，Redis 广播实现在路线图 |
| 对话治理 | `chat.Config{ConfirmSecret, ConfirmNonceStore, SessionStore, TurnInterceptor, WriteInterceptor, WritesPerMinute, AllowModelSelect, EnableDeepAgent, MaxModelCallsPerTurn, MaxSubtasksPerTurn, MaxTurnDuration}`；Fast 默认启用，Deep 显式开放；会话经 csid 跨断线恢复上下文 |

## 前端内核能力（v0.6）

- **SPA 生命周期**：`ihtml.destroy()` 回收监听、请求、定时器、SSE、WS 与 DOM，支持 mount→destroy→mount；`ready()` Promise 与 `onReady/onAuthError/onConnection` 回调
- **鉴权刷新**：`auth()` 只在每次 REST 请求时求值并写入 `X-Ihtml-User` header；原生 EventSource/WebSocket 无法设置自定义 header，可选 `connectionAuth()` 只返回一次性或短期连接票据并放入连接 query（同源 cookie 场景无需提供）。不要返回会进入 URL 的长期 bearer；票据的签发与校验由宿主负责。`refreshAuth()` 会清理旧用户 UI 与对话状态，再按新身份完整重载
- **实时一致性**：首帧 `resync` 建立基线，事件版本缺口、队列溢出与页面注册表变化自动触发全量同步
- **管理与 AI**：桌面为紧凑浮动操作组，移动端为安全区底部操作栏；AI 处理记录采用原生可折叠时间线，折叠后与正式回复保持紧凑间距
- **AI 标准库**（prompt 已教，AI 生成质量的地基）：`ihtml.chart.line/bars/spark` 零依赖 SVG 图表（tokens 配色、自动重绘、随 Item 回收）、`ihtml.fmt.num/money/date/ago/pct`、`ihtml.util.esc/debounce/throttle`、`ihtml.poll/listen`（自动绑定 Item 生命周期）、`ihtml.ui.modal/paginate`
- **i18n**：内核文案 zh/en 随 `locale` 自动切换，`mount({strings:{...}})` 逐条覆写
- **无障碍与细节**：toast aria-live、Escape 关闭、prefers-reduced-motion、safe-area、100dvh、移动端触摸目标下限

## 安全模型

- **鉴权默认拒绝**：没有显式 `WithUserResolver` 时所有用户 API 返回 401；开发 resolver 不应进入生产
- **数据边界在服务端 API**：Item 是与宿主同源、可执行的可信代码，它的 JS 以浏览者身份运行；服务端接口必须自行鉴权，Item 本身不应包含敏感信息。若内容来自不受信任的作者，应改用独立 origin + sandbox iframe，而不是依赖 `allowExternalAssets:false`
- 非安全 HTTP 方法只接受同源或 CORS 白名单 Origin，并要求 JSON Content-Type；WebSocket 同样执行 Origin 校验
- `postMessage` 默认只接受同源父页面；跨域嵌入必须同时显式配置允许来源
- Item 默认只来自用户自己的 AI 会话与 API 写入；如果将来做"分享给他人"，请对分享内容走 iframe 沙箱或审核后再放行
- 对话回复在前端经白名单 HTML 清洗后渲染；工具返回的数据在 prompt 里声明为不可信数据
- 内核资源刻意 `no-cache`：升级库后旧内核不会与新 API 脱节

## 扩展点

```go
type Store interface { … }        // 换数据库: 实现 Items/ReplaceItems/KV*/Revision*
type UserResolver func(*http.Request) (string, error)
type ChatBackend interface {      // 换 AI 实现(自研/别的编排框架)
    NewSession(ctx, *ChatSessionContext) (ChatSession, error)
}
type ItemService / KVService / PageService / TemplateService interface { … }
type Service interface { … }      // 完整受信任管理 API；*Handler 实现
type ScopedService interface { … } // 已绑定一个用户；请求路由优先用 ServiceForRequest
```
