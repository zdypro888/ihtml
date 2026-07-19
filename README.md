# ihtml — AI 动态前端 UI 功能库

一个 Go 库：内嵌一张"空壳页面"（微内核），按用户加载服务端记录的一组 **UI Item**（HTML/JS/CSS 片段）并在浏览器里执行，拼装出该用户的完整界面。内置 AI 助手既能在对话中直接回答和分析业务数据，也能在用户明确要求时生成并持久化 Item——用户说"加一张行情卡片"，页面实时出现，下次打开依然在。

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
| `ihtml/redisbroadcast` | 多副本实时事件：非阻塞 Redis Pub/Sub + 用户级全局单调版本；`*redis.Client` 与 Broadcaster 生命周期均由宿主关闭 |
| `ihtml/storetest` | 第三方 `Store` 实现的官方一致性验收套件（`storetest.Run`），内置 Store 同样由它验收 |

旧的 `ihtml.New` / `chat.New` 为兼容保留；新集成推荐 `NewHandler` / `NewChecked`，让所有配置错误在开始监听前返回。公共协议字符串均有导出常量，模型能力响应带 `protocol_version`。当前仍处于 pre-v1 阶段；发布 v1 前会继续保持新增优先、破坏性调整集中并提供迁移说明。

完整兼容约定见 [COMPATIBILITY.md](COMPATIBILITY.md)，未发布变更见 [CHANGELOG.md](CHANGELOG.md)。许可证为 [MIT](LICENSE)，最低 Go 版本 1.25（CI 同时验证最低版与最新稳定版）；正式发版前仍需由项目所有者打首个 semver tag。

## 嵌入宿主应用（SDK 形态）

宿主页面只需引入内核 js/css 并声明一个挂载点——**零 JS 代码**：

```html
<link rel="stylesheet" href="https://ui.example.com/ihtml.css">
<script src="https://ui.example.com/ihtml.js"></script>

<div id="their-nav"><!-- 宿主自己的侧栏里留给 ihtml 菜单的位置 --></div>
<div id="customer-ui" data-ihtml
     data-base="https://ui.example.com/"  
     data-uid="alice"
     data-ihtml-storage-key="customer-ui"
     data-theme="inherit"
     data-menu="#their-nav"></div>
```

- `data-theme="inherit"`：挂载时探测宿主计算样式（背景亮度→明暗、正文色、字体、链接色→主色）自动融入宿主观感；也可显式 `data-theme='{"primary":"var(--brand-600)","mode":"dark"}'` 直接映射宿主设计变量
- `data-menu`：多页时菜单渲染进宿主指定容器（单页/无页面时自动隐藏）；嵌入默认**内存路由**，不占宿主 `location.hash`
- 内核样式全部收在 `.ihtml-root` 作用域内；嵌入时 Item 的普通选择器默认精确限定到所属实例的 `data-ihtml-instance`。`@keyframes` / `@property` 等文档级命名仍需 Item 自行使用唯一前缀；跨域部署时服务端加 `ihtml.WithCORS("https://host.example.com")`
- 等价的编程式：`ihtml.mount(el, {base, auth, connectionAuth, theme, routing, menu, chatEntry, storageKey})`；宿主随时可调 `ihtml.theme.set/nav.go/chat.open`
- `base` 在挂载前严格解析，只接受不含用户名/密码的 HTTP(S) 目录 URL；其中 query/hash 属于页面状态，会被丢弃，身份参数请放入 `params`，凭据请放入 `auth` / `connectionAuth`
- **同页多实例**：`mount` 可多次调用（或写多个 `data-ihtml` 声明），内核生成的 CSS 选择器边界、Item 词法 `ihtml` API、主题/模型/Agent 模式/对话会话缓存和内核 ARIA ID 均按实例隔离，各自 `destroy()`。Item 正文自带的 `id` 不会被重写，操作 Item DOM 必须从返回 API 的 `root` 开始查询。兼容层的 `window.ihtml` 动态指向当前主实例，主实例销毁后自动提升仍存活的第一个实例；多实例宿主仍必须保留每次 `mount()` 的返回值，并保持默认内存路由（hash 是页面级单资源）
- `storageKey` / `data-ihtml-storage-key` 为实例提供跨刷新稳定的本地缓存身份；宿主元素有稳定 `id` 时可省略，否则建议显式设置。键还会按 `base` 和已解析用户隔离，切换身份不会复用上一用户的对话。升级时，首个成功解析身份的实例会一次性复制旧的无作用域主题、模型、Agent 模式和会话键；旧键保留供回滚，但不会再被第二个实例认领
- 也支持 iframe 嵌入独立页。跨域父页面必须用 `WithMessageOrigins("https://host.example.com")` 显式放行 postMessage 与 CSP `frame-ancestors`
- Item 中声明式的外部 `<script src>` / `<link rel=stylesheet>` 默认拒绝；只有受控宿主可显式设置 `allowExternalAssets:true`。启用严格 `script-src` CSP 的嵌入宿主应把当前响应的随机 nonce 传给 `mount({scriptNonce})`，内核会把它附加到所有动态 Item script，无需放开 `unsafe-inline`。外链 stylesheet 是文档级资源，不能被内核 `@scope` 重写；外链 classic Item 脚本若需异步使用实例 API，必须在脚本同步顶层先捕获 `const instance = window.ihtml`，回调只使用 `instance`。外链 ES Module 没有可靠的 `document.currentScript`；多实例宿主应由自己的初始化函数显式传入 `mount()` 返回 API，或使用能注入词法 `ihtml` 的内联 module Item。这不是沙箱：Item 是可信可执行代码，仍可通过 JS、图片或 CSS 发起网络请求。兼容旧 CSS 时可临时设置 `scopeStyles:false`

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

体验演示（含预置数据；未配置真实模型时自动挂载"演示助手"，只预览富文本对话展示，不会伪造模型思考、业务结果或页面写入）：

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

密钥文件必须是绝对路径、普通非符号链接文件，Unix 权限为 `0600` 或 `0400`。配置采用严格 JSON：限制 64 KiB、拒绝未知字段/重复键/尾随文档，并在监听端口前校验 Provider、HTTPS BaseURL、模型、认证、Thinking、Deep 预算与超时；显式配置无效会终止启动，绝不会静默降级到模拟助手。当前 AI schema 是 v2，loader 仍严格读取旧 v1 文档并补上安全默认值；运行时资源上限、模型重试、工具发现/额度、会话 namespace 等新增字段只属于 v2，使用这些字段时必须把 `schema_version` 升为 `2`，不会在 v1 下被悄悄接受。`IHTML_AI_DISABLED=1` 可作为紧急熔断。

这套加载能力不是 example 私有代码。第三方宿主可直接导入 `github.com/zdypro888/ihtml/chat/config`：

```go
loader, err := chatconfig.NewLoaderChecked(chatconfig.Options{
    // 必须显式注入；nil 时 loader 绝不会偷偷读取 ANTHROPIC_* / IHTML_*。
    Secrets: chatconfig.NewOSSecretResolver(),
})
if err != nil {
    return err // 包括 typed-nil SecretResolver 等宿主依赖错误
}
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
- 单 Item 的 `content` ≤ 512 KiB；单用户最多 512 个 Item，且全部 Item 源字段合计 ≤ 8 MiB。每个 Item 的 `meta` 最多 64 项，key ≤ 128 bytes、value ≤ 1 KiB

### 页面与路由（多页应用）

**默认是单页形态**（Item 全部 `page:""`，无菜单无 shell）——适合作为宿主产品里的一个页面；AI 也被教导只有用户明确要"多页/菜单"时才升级。多页时：注册页面 `{name, title, icon, order}`（页面列表=菜单数据源），路由切页卸载当前页 Item（触发 teardown）→ 加载目标页；独立页面用 hash 路由（`#/bills`），嵌入模式用内存路由。菜单可以是 AI 生成的 sidebar Item（独立页面），也可以渲染进宿主容器（`data-menu` 菜单槽）。

### 插槽布局与 PC/Mobile

未启用已发布 Template 时，html Item 仍可用旧兼容名 `meta.slot: sidebar|header|footer` 挂入 app-shell 对应区域；存在这类 Item 时内核自动启用 app-shell 网格布局。**容器宽度 <768px 时** sidebar 自动变成左侧抽屉 + 汉堡按钮（支持左缘右滑打开、左滑关闭的触摸手势），浮动操作也切成底部操作栏；`.ihtml-root.ihtml-mobile` 类与 `ihtml.env` 供 Item 侧适配。

### 业务 API 目录（AI 取数）

宿主用 `WithAPIs(...)` 声明业务接口（路径+参数/返回说明），目录进入 AI 系统提示。AI 的 `api_probe` 默认禁用；只有同时配置 `chat.Config.ProbeBaseURL` 后才能探测目录白名单内的 GET 路径。身份不会隐式转发，可用 `ProbeForwardHeaders`、`ProbeForwardQuery` 或 `ProbeRequest` 显式注入。Item 的 JS 直接 `fetch` 同源接口；配合 `WithPageConfig` 可注入每用户动态参数（`ihtml.config`）。

### 页面运行时 `ihtml`

Item 内联代码会在无同名声明冲突时获得指向所属实例的词法 `ihtml`；内核不会用正则改写 Item 源码。内联 classic 脚本在 Item 局部函数作用域执行，若确实要导出全局能力，须显式写成 `window.foo = foo`，不要依赖顶层 `var`/函数声明。下表是 Item 的"标准库"（也是 AI system prompt 里的 API 文档）：

| API | 用途 |
|---|---|
| `ihtml.root` | 当前实例根元素；用 `ihtml.root.querySelector(...)` 查 Item，不要从全局 `document` 查同名 Item |
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
| `GET /api/export` | 导出该用户全量数据（v3 备份契约，与 `ExportUser` 一致） |
| `GET/PUT/DELETE /api/kv/{key}` | 用户级 KV（GET 返回 `value` + `revision`；PUT 可传 `expected_revision` 做原子条件写，冲突 409；缺失读取 404；`__ihtml` 前缀统一拒绝） |
| `GET /api/events` | SSE：带单调 `version` 的变更事件；订阅、溢出或周期校准会发送 `resync` |
| `GET/POST /api/errors` | 页面错误查看 / 上报 |
| `GET /api/config` | `{"user":"…","chat":true}` |
| `GET /api/chat/models` · `GET /api/chat/ws` | AI 模型列表 / 对话 WebSocket |

每次 Item、Page 或 Template 变更（含 AI 发起）都会：先快照（默认保留 50 份，`WithMaxRevisions` 调整）→ 落库 → SSE 广播。`WithMaxRevisions(0)` 只取消默认的 50 份保留目标，不能关闭 4096 份 / 64 MiB 的 revision 硬上限。

### 持久化资源边界

这些限制由服务端强制执行，不能由 Store、导入文件或配置关闭；名称对应可供第三方宿主引用的公开 Go 常量。

| 数据 | 每对象限制 | 单用户硬上限 |
|---|---|---|
| Item | `content` ≤ 512 KiB (`MaxItemContentBytes`)；每个 `meta` ≤ 64 项、key ≤ 128 bytes、value ≤ 1 KiB (`MaxItemMetaEntries` / `MaxItemMetaKeyBytes` / `MaxItemMetaValueBytes`) | ≤ 512 项 (`MaxItemsPerUser`)；所有 ID/title/page/content/meta 源字节合计 ≤ 8 MiB (`MaxUserItemBytes`) |
| Template | 每个 Draft 或 Published `TemplateVersion` 的保守结构估算 ≤ 1 MiB (`MaxTemplateVersionBytes`) | 全部模板 Draft/Published 逻辑结构合计 ≤ 8 MiB (`MaxTemplateCatalogBytes`)；内部目录 JSON 编码 envelope ≤ 16 MiB (`MaxTemplateCatalogStorageBytes`) |
| KV | 公共 KV value ≤ 64 KiB (`MaxKVValueBytes`) | 公共与内核 key 合计 ≤ 4096 项 (`MaxKVEntriesPerUser`)；key + 原始 JSON value 合计 ≤ 32 MiB (`MaxUserKVBytes`)；`Limits.MaxKVKeysPerUser` 仍是更低的可配置公共 KV 准入配额 |
| Revision | 每份快照仍逐项满足 Item/Page/Template 契约 | ≤ 4096 份 (`MaxRevisionsPerUser`)；全部快照的 Item/Page/Template 源数据保守估算合计 ≤ 64 MiB (`MaxUserRevisionBytes`) |

第三方 `Store` / `UserStateStore` 的返回值也会在复制、返回或继续写入前重新校验。任一结构损坏或超过上述硬上限时，Handler 会 fail-closed 返回错误，不会静默截断或暴露部分状态。升级已有部署前应离线扫描并迁移、裁剪或清理超限记录；超限后不能依赖普通 Handler API 自行“读出来再清理”。

Template 只定义可复用结构。新业务 Item 通过 `Item.Meta["slot"] == Slot.id` 绑定；`TemplateVersion.Placements` 仅用于旧 `layout.*` 迁移或明确的页面实例覆盖，模板编辑器不应因创建 Container/Slot 而写入业务 ItemID。Slot 缺失、已删除或 Placement 标记 `unassigned` 时，运行时把 Item 追加到 Root 末尾，不能静默隐藏。

`POST /api/templates` 创建的是未绑定模板，不会仅因 ID 恰好为 `tpl-root` 或 `tpl-page-*` 就自动上线；必须通过 `PUT /api/templates/current` 或 `Page.template_id` 显式绑定。普通运行时只应用 `template.published`，绝不回退到 Draft；没有 Published 时继续走旧兼容渲染。

## AI 对话

- 传输：WebSocket 上的可扩展流式事件协议（`user_message` / `confirm_tool` / `cancel_tool` / `cancel_tool_result` / `assistant_delta` / `tool_start` / `confirmation_required` / `assistant_done` …），第三方 `ChatBackend` 可以沿用同一套通用前端。所有服务端输出（包括第三方 Backend）在写线前统一受限：单个编码后事件 ≤ 1 MiB - 1 byte (`MaxChatOutboundFrameBytes`)，每个已接受用户轮次 ≤ 4096 个事件且 ≤ 4 MiB，每条连接累计 ≤ 65536 个事件且 ≤ 64 MiB；越界会发送固定错误并以 WebSocket 1009 关闭连接。`confirm_tool` 与 `cancel_tool` 进入同一有界 FIFO，按帧顺序争用一次性 nonce；前端以携带工具 ID + nonce 的真实回执为准，连接断开时会先重连，只有成功回执才显示“已撤销、未执行”，失败或状态未知绝不伪装成取消成功
- 通用助手：普通问答、解释和业务数据查询直接在对话中回答；只有用户明确要求创建或修改界面时才调用 UI 写工具。多记录、时间序列和横向对比优先使用语义表格，但不硬编码业务字段或固定模板
- 执行模式：未指定 `agent_mode` 或指定 `fast` 时走单 Agent 低延迟路径；只有服务端设置 `EnableDeepAgent=true` 后，客户端才可对复杂任务发送 `agent_mode:"deep"`。因此开放 Deep 不会改变普通对话的默认行为
- DeepAgent 边界：使用 eino ADK DeepAgent 做规划与委派，内置 `ui_inspector` / `ui_reviewer` 子 Agent 只能读取（含 `ui_template_get`，不会获得模板写工具）；所有 UI/Template 写入仍集中在根 Agent，且没有文件系统、Shell 或任意网络工具
- 上下文与工具目录：Deep 默认用 Eino `tool_search` 按需装载语义工具，Fast 仍直接绑定完整目录；长会话任一超过 24000 tokens 或 80 条消息时自动压缩。摘要模型被禁止调用工具，旧系统消息不会进入其输入，工具结果按不可信数据处理；压缩记忆也带显式不可信边界。宿主可分别通过 `DisableDynamicToolSearch`、`DisableContextSummarization` 或严格 JSON 配置调整
- 能力发现：`GET /api/chat/models` 的 `agent_modes` 是服务端实际开放的模式（默认 `["fast"]`，显式开放后为 `["fast","deep"]`）；`deep_approval_resume` 明示确认卡后的恢复能力。`AllowModelSelect=true` 时才可查网关模型目录并由用户选择；生产默认隐藏模型元数据并锁定服务端 `Config.Model`，客户端不能覆盖
- 有界执行：`MaxModelCallsPerTurn`（默认 24）、`MaxSubtasksPerTurn`（默认 3）与 `MaxTurnDuration`（默认 2 分钟）由根/子 Agent 共享；每次模型调用只对网络、限流和明确上游瞬时错误重试 1 次（`MaxModelRetries`，认证/参数/策略/主动取消不重试，且重试继续计入模型额度）。不信任 provider 的 token/usage 声明：每次 HTTP 响应体默认硬限 8 MiB，模型输出按单次/整轮默认限制为 1 MiB / 4 MiB，整轮最多 4096 个模型输出事件（流 chunk 或非流 response；`MaxProviderResponseBytes` / `MaxModelOutputBytesPerCall` / `MaxModelOutputBytesPerTurn` / `MaxModelOutputEventsPerTurn`）；正文、reasoning、tool-call 与扩展载荷都计入，Deep 子 Agent、摘要与答复修复共享同一 turn 额度。可见最终回答和整轮 reasoning 另默认硬限 256 KiB（`MaxAssistantMessageBytes` / `MaxReasoningBytesPerTurn`，reasoning 单 segment 最多 64 KiB），超限会给出截断标记，不会把巨型快照交给浏览器。严格 JSON 与 `NewChecked` 接受的范围依次为：provider 响应 1..64 MiB、单次模型输出 64 KiB..16 MiB、整轮模型输出 64 KiB..64 MiB（且不得小于单次）、整轮模型事件 16..65536、最终回答 16..512 KiB、整轮 reasoning 16..768 KiB；0 选择上述默认值，边界不能关闭。语义工具另受整轮 32 次、单工具 8 次、相同规范化参数 2 次的默认三层共享限额（`MaxToolCallsPerTurn` / `MaxToolCallsPerTool` / `MaxRepeatedToolCalls`，负数均表示不限）；相同读调用可按配置重试，相同规范化写调用每轮最多执行一次，避免模型重放产生重复副作用。耗尽后 `assistant_budget` 快照带稳定 code/limit/tool 且不执行超额工具；`ui_plan_update`、动态 `tool_search` 与子任务委派是内部编排，分别受目录、计划校验与子任务额度约束，不重复计入语义工具额度。Backend 还分别限制真实未返回的关键宿主回调、观察回调和模型/Service 操作，默认 `32/32/64`，可用 `MaxOutstandingHostCallbacks` / `MaxOutstandingObservations` / `MaxOutstandingOperations` 配置；0 使用默认值，合法显式值为 1..4096
- 回答格式：`assistant_delta.delta` 与 `assistant_done.message` 可通过可选 `content_format` 声明 `text` / `markdown` / `html`。内置 Eino 助手输出语义 HTML 片段，由前端统一清洗后展示；非空的 `assistant_done.content_format` 对最终快照具有权威性，空字段保留流式格式或旧版自动检测，未知值必须按纯文本处理。`assistant_done` 还可附加 provider 原始 `finish_reason`、规范化 `usage` 与 `repaired` 标记；这些是可选诊断元数据，不应被客户端当作授权或最终计费依据。若模型在工具执行后返回空答复或因输出上限截断，服务端只用已完成工具证据整理一次最终 HTML，不重新执行工具
- 演示能力：`GET /api/chat/models` 可用 `preview_only:true` 表示仅提供确定性界面预览；它不等同于 `model_ready`，前端会保持预览可操作，同时明确提示回答并非来自真实 AI
- 过程事件：`assistant_thinking` 是增量流，不是一次性快照；同一次模型输出共享 `thinking_id`，最后一条带 `thinking_done:true`。客户端应按 ID 追加并在 done 后折叠。中途流式重试前服务端发送 `assistant_reset`，新客户端应清除本轮已投影草稿；旧客户端可忽略。`assistant_done.message` 始终是权威最终快照，可覆盖此前的流式草稿；工具按 `tool_call.id` 原位更新，`tool_result.tool_summary` 是面向用户的结果，`tool_result` / `tool_debug` 只适合放在折叠技术详情中
- 确认后恢复：当前 Deep 能力值为 `manual_tool_only`——确认卡只执行卡片中的单个工具调用，整体计划保持暂停，不会在用户不知情时自动续跑；需要继续时由客户端发起新的用户轮次。未来接入 checkpoint 后再通过能力字段升级
- 工具：`ui_item_list/get/put/delete`、`ui_items_replace`、`ui_page_list/set/delete`、`ui_theme_get/set`、`ui_kv_get/set`、`ui_revision_list/rollback`、`ui_page_errors`、`ui_page_inspect`（经会话 WS 请求内核回传实时渲染状态：按视口交集、祖先计算样式与 overflow 裁剪估算每个 Item 的可见性，并返回尺寸、文本摘要与报错；支持 `ids` 精确过滤及 `offset`/`limit` 分页，每页最多 20 项，按 `next_offset` 继续；页面离线时数秒超时返回）、`api_probe`（只读探测业务 API）、`ui_plan_update`（Deep 模式的结构化计划工具），以及结构专用的 `ui_template_get/put_current/publish/slot_delete`
- Template 工具契约：`ui_template_get(page)` 只返回 Draft/Published 结构和 CAS revision，不读取 Item content；`put_current` 只接受完整结构 Draft（服务端严格拒绝未知字段、业务正文、HTML/CSS/JS/URL），保存后不会改变运行时；`publish` 才把 Draft 上线；`slot_delete` 保留 Item 并把引用迁入 Root fallback。所有 Template 写入都必须带 `expected_revision` 并确认
- 风险控制：读操作直接执行；安装 HTML/JS/CSS 的 Item 写入、全部 Template 写入及所有高风险操作默认必须点确认卡（HMAC 签名 + 原子一次性 nonce + 15 分钟 TTL）。签名同时绑定用户、逻辑 SessionID、应用/租户能力指纹及语义工具协议版本，同密钥下也不能跨会话或跨应用执行/撤销。只有完全受控环境可用 `AllowUnconfirmedItemWrites=true` 放开普通 Item 写；`ConfirmWrites=true` 会进一步确认其余写操作
- 多轮历史默认在 Backend 级内存中按 csid 恢复，并受会话数、每会话事件数、字节数与 TTL 四重上限约束；同一 Backend 内相同作用域 SessionID 同时只允许一个 WS owner，新连接会非阻塞接管，并在旧连接的模型、工具和确认操作真实返回后才开始执行。生产可注入持久化 `SessionStore` 或共享 `ConfirmNonceStore`；任一外部 Store 存在时，`NewChecked` 都强制要求稳定的 `SessionNamespace`（应用/租户命名空间），避免同一密钥/Store 被多个应用复用时确认卡串域，非结构化授权或回调语义变化时递增 `SessionCapabilityVersion`。兼容构造器 `New` 遇到未命名的任一外部 Store 会生成实例级临时 namespace，安全隔离但不会跨重启/副本续接。内部 SessionID 同时绑定这些值、只读策略、工具/API 目录、确认策略、探测边界与宿主提示词的能力指纹，权限或能力变化会自动切换历史作用域，且不会把 user/csid 明文暴露给外部 Store。该 owner 协调只保证单进程；多副本必须由宿主提供 sticky routing 或分布式 session lease/锁，单独共享 `SessionStore` 不能防止两个 Backend 并发执行同一会话
- 修 bug 闭环：内核收集 Item 报错 → AI 用 `ui_page_errors` 读取、`ui_page_inspect` 核对真实屏幕 → 重写 Item 修复

## 生产治理（SDK 内建）

| 能力 | 用法 |
|---|---|
| 数据库存储 | `sqlstore.New(db, sqlstore.Postgres/MySQL/SQLite)`（纯 database/sql，`Init()` 建表；PostgreSQL/MySQL 的整状态读取使用一致性快照。旧库迁移前仍可读，写入会返回明确错误，升级后须先运行一次 `Init()`） |
| 审计留痕 | `WithAudit(fn)` —— 全部写操作（含 AI 发起）同步回调 `AuditEvent{User,Action,By,Note,Detail}` |
| 访问策略 | `WithAccessPolicy(fn) → Access{ReadOnly, ChatDisabled}` —— 只读视图/按用户禁 AI，HTTP 与 AI 工具双层拦截 |
| 宿主直调 | `Handler.ServiceForRequest(r)` 返回已绑定用户的 `ScopedService`，复用身份重验、访问策略和写限流；`Handler` 自身的 Service 方法是受信任管理 API |
| 限流配额 | `WithLimits{MaxSSEPerUser:8, MaxWSPerUser:4, ChatTurnsPerMinute:20, WritesPerMinute:240, MaxKVKeysPerUser:256}`（默认即生效；对话轮次=真金白银） |
| 指标 | `WithMetrics(m)` 或零依赖 `Handler.Stats()`：sse_connections/ws_sessions/chat_turns/writes/rate_limited… |
| 备份迁移 | `ExportUser/ImportUser/PurgeUser`（Go API）+ `GET /api/export`；v3 契约完整包含 Items/Pages/Templates/KV/Revisions，导入先按当前硬资源边界全量校验并原子替换。升级前应先离线清理旧 Store 中的超限/无效状态，否则新版会 fail-closed，而非静默截断。`PurgeUser` 清空业务载荷但为防旧 CAS 复活会保留版本墓碑；SQL 版本表仍含用户键，不等同于完整物理擦除/GDPR 流程 |
| 优雅停机 | `Handler.Close()`（断 SSE/WS）；SSE 55 分钟强制到期配合 LB 排空；资源 ETag 304 协商缓存 |
| 多副本 | 写一致性：`VersionedUserStateStore` 条件写（内置三种 Store 均实现，含配额、错误日志和 Template `expected_revision` 的跨副本真 CAS），冲突自动重读重试，无需 sticky 路由；`EraseUser` 清空数据但保留递增版本墓碑。实时事件：外部 Broadcaster 默认只发送权威 `resync`，避免提交与发布反序回滚页面；`ihtml/redisbroadcast` 使用非阻塞有界发布队列、Redis Cluster 同槽键和哈希用户标识。它的新 Redis namespace 与旧版不互通；滚动升级时仅在过渡窗口显式开 `redisbroadcast.Options{LegacyNamespaceMigration:true}` 双发/双订阅，旧实例退完立即关闭并清理旧 `<prefix>:ver:<user>` 键。只有宿主把同一用户的完整写调用跨副本全局串行化时才可用 `WithOrderedBroadcaster` 保留增量；Broadcaster 自身排序不够 |
| 对话治理 | `chat.Config{ConfirmSecret, ConfirmNonceStore, SessionStore, SessionNamespace, SessionCapabilityVersion, TurnInterceptor, WriteInterceptor, WritesPerMinute, AllowModelSelect, EnableDeepAgent, MaxModelCallsPerTurn, MaxModelRetries, MaxSubtasksPerTurn, MaxToolCallsPerTurn, MaxToolCallsPerTool, MaxRepeatedToolCalls, MaxProviderResponseBytes, MaxModelOutputBytesPerCall, MaxModelOutputBytesPerTurn, MaxModelOutputEventsPerTurn, MaxAssistantMessageBytes, MaxReasoningBytesPerTurn, MaxTurnDuration, MaxOutstandingHostCallbacks, MaxOutstandingObservations, MaxOutstandingOperations}`；Fast 默认启用，Deep 显式开放；会话经能力作用域化的 csid 跨断线恢复上下文；宿主拦截器受 context 与 5 秒上限保护，只应通过 `chat.NewUserFacingError` 标记已经审查、允许发给终端用户的错误文案，其他内部错误会被泛化 |
| AI 运行时扩展 | `RuntimeConfigProvider` 在每轮模型创建前只覆盖 generation 字段，不能改变凭据、URL、提示词、工具权限或预算，等待时间受 5 秒和本轮 deadline 的较短者约束；`TurnObserver` 以 best-effort 异步接收不含正文、工具参数和原始错误的 started/completed/failed 指标快照，同一轮内已接收的快照保持顺序，但过载或不遵守 context 的回调可导致后续快照丢弃。它使用独立观察额度，回调错误、阻塞与 panic 不影响对话，也不会挤占授权/确认等关键宿主回调 |

## 前端内核能力（v0.6）

- **SPA 生命周期**：`ihtml.destroy()` 回收监听、请求、定时器、SSE、WS 与 DOM，支持 mount→destroy→mount 与**同页多实例并存**（`ihtml.instances()` 枚举）；`ready()` Promise 与 `onReady/onAuthError/onConnection` 回调
- **鉴权刷新**：`auth()` 只在每次 REST 请求时求值并写入 `X-Ihtml-User` header；原生 EventSource/WebSocket 无法设置自定义 header，可选 `connectionAuth()` 只返回一次性或短期连接票据并放入连接 query（同源 cookie 场景无需提供）。不要返回会进入 URL 的长期 bearer；票据的签发与校验由宿主负责。`refreshAuth()` 会清理旧用户 UI 与对话状态，再按新身份完整重载
- **实时一致性**：首帧 `resync` 建立基线，事件版本缺口、队列溢出、外部副本通知与页面注册表变化自动触发全量同步；单进程内置 hub 仍保留低延迟增量更新
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
- 对话回复的语义 HTML 在前端经白名单清洗后渲染，模型输出本身不被视为可信代码；工具返回的数据在 prompt 里声明为不可信数据
- 内核资源刻意 `no-cache`：升级库后旧内核不会与新 API 脱节

## 扩展点

```go
type Store interface { … }        // 换数据库: 实现 Items/ReplaceItems/KV*/Revision*
type UserStateStore interface { … }          // 可选: 整状态原子读写(强烈建议实现)
type VersionedUserStateStore interface { … } // 可选: 版本条件写, 多副本部署必需
type Broadcaster interface { … }  // 换事件通道: redisbroadcast 提供跨副本实现
type UserResolver func(*http.Request) (string, error)
type ChatBackend interface {      // 换 AI 实现(自研/别的编排框架)
    NewSession(ctx, *ChatSessionContext) (ChatSession, error)
}
type ItemService / KVService / PageService / TemplateService interface { … }
type VersionedKVService interface { … } // 可选: 单键读取版本 + 原子 CompareAndSet
type Service interface { … }      // 完整受信任管理 API；*Handler 实现
type ScopedService interface { … } // 已绑定一个用户；请求路由优先用 ServiceForRequest
type ScopedVersionedKVService interface { … } // 对 ScopedService 做类型断言取得
```

`VersionedUserStateStore` 的版本 0 只表示“从未写入”。每个成功写操作（含删除不存在的 KV）都必须推进版本；`EraseUser` 也必须留下更新后的空状态墓碑，不能重置或复用版本。公共 KV 遇到 `ReservedKVPrefix` 会返回可用 `errors.Is(err, ErrReservedKVKey)` 判断的错误。第三方 Store 应运行 `storetest.Run`，它会同时验证并发 CAS、RawMessage 字节保真、输入/输出所有权和删除后的版本语义。`FileStore` 用可响应 `context` 取消的进程级 gate 与系统文件锁支持同目录多句柄/协作进程 CAS，新数据文件为 `0600`，v2 文件同时写精确 `kv_bytes` 和旧版可读的语义 `kv`；它适合小规模单版本部署，升级/回滚应停写切换，不能让不识别锁协议的旧二进制与新版并行写。生产数据库仍优先使用 `sqlstore`。

编辑器或多标签页应使用 `KVGetWithRevision` + `KVCompareAndSet`，不要自行实现“先 GET、再无条件 PUT”。版本令牌是服务端基于 RawMessage 完整字节生成的不透明 SHA-256 表示；首次创建使用 `MissingKVRevision`。冲突返回 `KVConflictError`（同时匹配 `ErrKVConflict` / `ErrConflict`），其中包含最新版本和值。HTTP `GET /api/kv/{key}` 会增量返回 `revision`，`PUT` 可选传 `expected_revision`；字段缺失仍保持旧版无条件写语义。内置 Versioned Store 可跨 Handler/副本原子比较并写入；第三方 legacy Store 仅保证单 Handler 锁内语义。`DeleteItems` 会在同一用户状态事务中清理所有 `item-layout.*` 引用。
