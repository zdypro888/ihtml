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
- **安全网**：每次变更自动快照可回滚；`?safe=1` 安全模式救砖；Item 报错自动收集，AI 可读取并修复
- **AI 对话**：eino ADK 多轮会话 + 语义工具 + 高危操作 HMAC 确认卡（协议与 casino admin AI 一致）

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
- 样式全部收在 `.ihtml-root` 作用域内，不污染宿主；跨域部署时服务端加 `ihtml.WithCORS("https://host.example.com")`
- 等价的编程式：`ihtml.mount(el, {base, params:{uid}, theme, routing, menu, chatEntry})`；宿主随时可调 `ihtml.theme.set/nav.go/chat.open`
- 也支持 iframe 嵌入独立页（`?embed=1&chat=0&theme={...}` + postMessage `ihtml:theme/nav/chat` 控制）

## 快速开始

```go
package main

import (
    "net/http"
    "github.com/zdypro888/ihtml"
    "github.com/zdypro888/ihtml/chat"
)

func main() {
    store, _ := ihtml.NewFileStore("./uidata") // 或 NewMemoryStore() / 自己实现 Store

    handler := ihtml.New(store,
        // 服务端"根据参数找到当前用户", 对接你自己的鉴权
        ihtml.WithUserResolver(func(r *http.Request) (string, error) {
            return r.URL.Query().Get("uid"), nil
        }),
        // AI 对话(可选)
        ihtml.WithChatBackend(chat.New(chat.Config{
            Provider: chat.ProviderOpenAI,          // openai(一切兼容网关/Ollama) 或 claude
            BaseURL:  "https://api.openai.com/v1",
            APIKey:   "sk-...",
            Model:    "gpt-4o-mini",
            Stream:   true,
        })),
    )

    http.Handle("/ui/", http.StripPrefix("/ui", handler)) // 挂任意子路径或根路径
    http.ListenAndServe(":8080", nil)
}
```

体验演示（含预置数据；未配置真实模型时自动挂载"演示助手"，完整走一遍对话事件流）：

```bash
go run ./example -addr :8899     # http://localhost:8899/?uid=demo
go run ./example -data ./uidata  # 文件持久化

# 接真实模型(任意 OpenAI 兼容网关 / Ollama / exo):
export IHTML_AI_BASE_URL=https://api.openai.com/v1
export IHTML_AI_API_KEY=sk-...
export IHTML_AI_MODEL=gpt-4o-mini   # 可省: 会自动选网关上第一个已加载模型
go run ./example -addr :8899
```

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
  "meta": {"slot": "sidebar"} // 可选; html 可设 slot: sidebar|header|footer; js 可设 module
}
```

- `html`：渲染进 `<section data-ihtml-id="…">`，按 order 排列，内嵌 `<script>` 会执行
- `css`：注入 `<style>`；`js`：注入 `<script>` 执行（宽容处理误带的 `<script>` 标签包裹）
- 单 Item ≤ 512KB，单用户 ≤ 512 个

### 页面与路由（多页应用）

**默认是单页形态**（Item 全部 `page:""`，无菜单无 shell）——适合作为宿主产品里的一个页面；AI 也被教导只有用户明确要"多页/菜单"时才升级。多页时：注册页面 `{name, title, icon, order}`（页面列表=菜单数据源），路由切页卸载当前页 Item（触发 teardown）→ 加载目标页；独立页面用 hash 路由（`#/bills`），嵌入模式用内存路由。菜单可以是 AI 生成的 sidebar Item（独立页面），也可以渲染进宿主容器（`data-menu` 菜单槽）。

### 插槽布局与 PC/Mobile

html Item 声明 `meta.slot: sidebar|header|footer` 即挂入对应区域；存在这类 Item 时内核自动启用 app-shell 网格布局。**移动端（<768px）sidebar 自动变成左侧抽屉 + 汉堡按钮**，内核 CSS 保证响应式骨架；`html.ihtml-mobile` 类与 `ihtml.env` 供 Item 侧适配。

### 业务 API 目录（AI 取数）

宿主用 `WithAPIs(...)` 声明业务接口（路径+参数/返回说明），目录进入 AI 系统提示；AI 生成取数 UI 前可用 `api_probe` 工具（只读、目录白名单）实调一次看真实字段。Item 的 JS 直接 `fetch` 这些同源接口。配合 `WithPageConfig` 可再注入每用户动态参数（`ihtml.config`）。

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

`--ui-primary / --ui-primary-contrast / --ui-primary-soft / --ui-primary-border / --ui-bg / --ui-surface / --ui-surface-muted / --ui-surface-hover / --ui-text / --ui-text-muted / --ui-border / --ui-shadow / --ui-ok / --ui-err / --ui-warn / --ui-radius / --ui-gap / --ui-font`

基础形态类：`.ui-card .ui-btn .ui-btn.primary .ui-input .ui-select .ui-badge .ui-title .ui-muted`

## HTTP API

挂载点相对路径（全部要求 UserResolver 能解析出用户）：

| 方法与路径 | 说明 |
|---|---|
| `GET /` | 空壳页面（`?safe=1` 安全模式：不执行 Item，可回滚） |
| `GET/PUT/POST /api/items` | 列出 / 增改 Item（`?page=x` 过滤：全局+该页；`&global=0` 仅该页） |
| `DELETE /api/items?id=a&id=b` | 删除 |
| `GET/PUT /api/pages` · `DELETE /api/pages/{name}` | 页面注册表（删除页面连带删除页内 Item） |
| `POST /api/items/replace` | 整体替换 |
| `GET /api/revisions` · `GET /api/revisions/{id}` | 快照列表 / 内容 |
| `POST /api/revisions/{id}/rollback` | 回滚（回滚前自动再存一份快照） |
| `GET/PUT/DELETE /api/kv/{key}` | 用户级 KV（体：`{"value":…}`；`__ihtml` 前缀保留） |
| `GET /api/events` | SSE：`{"type":"items","op":"put",…}` / `{"type":"kv","key":"theme"}` |
| `GET/POST /api/errors` | 页面错误查看 / 上报 |
| `GET /api/config` | `{"user":"…","chat":true}` |
| `GET /api/chat/models` · `GET /api/chat/ws` | AI 模型列表 / 对话 WebSocket |

每次 Item 变更（含 AI 发起）都会：先快照（默认保留 50 份，`WithMaxRevisions` 调整）→ 落库 → SSE 广播。

## AI 对话

- 传输：WebSocket，事件格式与 casino admin AI 一致（`user_message` / `assistant_delta` / `tool_start` / `confirmation_required` / `assistant_done` …），前后端经验可直接平移
- 模型发现：优先查网关"已加载实例"（Ollama 风格 `/ollama/api/ps` → `/api/ps`），查不到退回 OpenAI 标准 `GET /models` 目录；目录里有 ≠ 能服务，默认模型自动指向已加载的第一个
- 工具：`ui_item_list/get/put/delete`、`ui_items_replace`、`ui_page_list/set/delete`、`ui_theme_get/set`、`ui_kv_get/set`、`ui_revision_list/rollback`、`ui_page_errors`、`api_probe`（只读探测业务 API）
- 风险控制：读操作直接执行；普通写操作直接执行（有快照兜底）；**整体替换与回滚**必须点确认卡（HMAC 签名 + 一次性 nonce + 15 分钟 TTL，改一个字都执行不了）。`Config.ConfirmWrites=true` 可让所有写操作都要确认
- 多轮历史存内存（每条 WS 连接一个 ADK Session）；"新会话"= 重开连接
- 修 bug 闭环：内核收集 Item 报错 → AI 用 `ui_page_errors` 读取 → 重写 Item 修复

## 生产治理（SDK 内建）

| 能力 | 用法 |
|---|---|
| 数据库存储 | `sqlstore.New(db, sqlstore.Postgres/MySQL/SQLite)`（纯 database/sql，`Init()` 建表；已对真实 Postgres 测试） |
| 审计留痕 | `WithAudit(fn)` —— 全部写操作（含 AI 发起）同步回调 `AuditEvent{User,Action,By,Note,Detail}` |
| 访问策略 | `WithAccessPolicy(fn) → Access{ReadOnly, ChatDisabled}` —— 只读视图/按用户禁 AI，HTTP 与 AI 工具双层拦截 |
| 限流配额 | `WithLimits{MaxSSEPerUser:8, MaxWSPerUser:4, ChatTurnsPerMinute:20, WritesPerMinute:240, MaxKVKeysPerUser:256}`（默认即生效；对话轮次=真金白银） |
| 指标 | `WithMetrics(m)` 或零依赖 `Handler.Stats()`：sse_connections/ws_sessions/chat_turns/writes/rate_limited… |
| 备份迁移 | `ExportUser/ImportUser/PurgeUser`（Go API）+ `GET /api/export`；`UserExport` JSON 即备份契约，可做新用户模板 |
| 优雅停机 | `Handler.Close()`（断 SSE/WS）；SSE 55 分钟强制到期配合 LB 排空；资源 ETag 304 协商缓存 |
| 多副本 | `WithBroadcaster` 接口化（默认进程内 hub）；**当前用户锁为进程内，多副本需按用户 sticky 路由**，Redis 广播实现在路线图 |
| 对话治理 | `chat.Config{ConfirmSecret(确认卡跨重启有效), TurnInterceptor(配额/计费钩子), AllowModelSelect(默认对终端用户隐藏模型)}`；会话经 csid 跨断线恢复上下文 |

## 前端内核能力（v0.5）

- **SPA 生命周期**：`ihtml.destroy()` 完整回收（全局监听/SSE/WS/DOM），支持 mount→destroy→mount；`ready()` Promise 与 `onReady/onAuthError/onConnection` 回调
- **鉴权**：`mount({auth: () => token})` 每次连接求值（REST 走请求头），运行中 401 出横幅并通知宿主，`refreshAuth()` 换令牌重连
- **加载体验**：首屏骨架屏、切页顶部进度条 + stale-while-load、失败错误态带重试（与空态严格区分）、SSE 指数退避重连 + 离线指示
- **管理面板**：右下 ⚙（主题/历史回滚/错误三页签）——嵌入模式的救砖与无 AI 自助入口；嵌入救砖还可用 `mount({safe:true})` 或宿主 URL `?ihtml-safe=1`
- **AI 标准库**（prompt 已教，AI 生成质量的地基）：`ihtml.chart.line/bars/spark` 零依赖 SVG 图表（tokens 配色、自动重绘、随 Item 回收）、`ihtml.fmt.num/money/date/ago/pct`、`ihtml.util.esc/debounce/throttle`、`ihtml.poll/listen`（自动绑定 Item 生命周期）、`ihtml.ui.modal/paginate`
- **i18n**：内核文案 zh/en 随 `locale` 自动切换，`mount({strings:{...}})` 逐条覆写
- **无障碍与细节**：toast aria-live、Escape 关闭、prefers-reduced-motion、safe-area、100dvh、移动端触摸目标下限

## 安全模型

- **数据边界在服务端 API**：Item 的 JS 以浏览者的会话身份运行，服务端接口自行鉴权。Item 本身不含敏感信息
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
type Service interface { … }      // *Handler 已实现: 宿主程序可直接用它读改用户 UI
```
