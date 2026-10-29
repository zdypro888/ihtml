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

Hosts that already own trusted output and business confirmations can reuse
`assistant.SessionRunner` without adopting the default projection:

```go
runner, err := assistant.NewSessionRunner(ctx, assistant.SessionRunnerConfig{
    MaxPending: 16,          // default 16; maximum 1024 waiting frames
    MaxEventBytes: 2 << 20,  // default 2 MiB; maximum 16 MiB of encoded JSON
    Run: handleTrustedTurn, // func(context.Context, *ihtml.ChatClientEvent)
    OnQueueFull: reportQueueFull,
    OnPanic: reportFatalPanic,
})
```

`Submit(parent, event)` JSON-clones the complete frame and never waits for the
active turn. Handle `ErrSessionQueueFull` and `ErrSessionEventTooLarge` explicitly;
neither means accepted work. `Stop()` atomically drops pending frames and cancels
the active turn with `ErrTurnStopped` as `context.Cause`, while keeping the
connection usable. Parent cancellations retain their own causes.
`RunInline(parent, callback)` immediately runs a tracked control callback outside
the FIFO, for cancellation acknowledgements or rejection replies. `Abort(err)`
is safe from hooks and cancels the entire connection. `Close()` is idempotent and
waits for actual host unwind, including accepted control work continued with
`context.WithoutCancel`. Never call Close reentrantly from Run or hooks; use
Abort instead. Hooks run outside scheduler locks. A hook panic aborts the
connection, clears queued work and reports `ErrSessionRunnerPanic`; even a second
panic in the reporter cannot strand Close. The ordinary `assistant.Backend`
shares this runner. Its `assistant.Config.MaxEventBytes` setting applies only to
that ordinary projection bridge. Confirmation-enabled backends retain their
existing WebSocket input and confirmation-card limits, approval/rejection and
asynchronous cancellation lanes while sharing the execution-context primitive.

### SDK 包边界

| 包 | 第三方用途 |
|---|---|
| `ihtml` | 稳定核心：HTTP Handler、Store/Service、模板、事件与 ChatBackend 协议；AI 完全可选 |
| `ihtml/chat` | 内置 Eino AI 后端；程序化入口是 `chat.Config` + `chat.NewChecked` |
| `ihtml/agent` | 与引擎无关的 Tool、TurnRequest、流式 Step、Usage 与 Engine 契约 |
| `ihtml/agent/eino` | 通用 Eino 执行循环、原生 Responses/Chat Completions/Claude 模型适配、动态工具与持久会话接口 |
| `ihtml/assistant` | 将宿主的 `agent.Engine` 接到对话协议，宿主通过 PrepareTurn/CompleteTurn/ToolExecutionGuard 保留业务治理 |
| `ihtml/uitools` | 受信 ScopedService 上的通用界面工具元数据、只读执行、写入准备与执行；宿主保留授权、确认、代码政策与审计 |
| `ihtml/confirmation` | 可复用确认卡签名与一次性核销；绑定用户、会话和能力边界，确认与撤销共用原子边界，不代替业务授权 |
| `ihtml/chat/config` | 严格、版本化、可嵌入宿主配置的 AI JSON loader；SecretResolver 显式注入 |
| `ihtml/sqlstore` | `database/sql` 持久化实现；驱动与 `*sql.DB` 生命周期由宿主拥有 |
| `ihtml/redisbroadcast` | 多副本实时事件：非阻塞 Redis Pub/Sub + 用户级全局单调版本；`*redis.Client` 与 Broadcaster 生命周期均由宿主关闭 |
| `ihtml/storetest` | 第三方 `Store` 实现的官方一致性验收套件（`storetest.Run`），内置 Store 同样由它验收 |

**AI 所有权只有一种。** 宿主已有自己的 Agent、会话、记忆或权限系统时，应实现 `ihtml.ChatBackend` 并通过 `WithChatBackend` 注入；此时不要再创建 `ihtml/chat.Backend`。`ihtml/chat` 只服务没有既有 AI 中枢的独立部署，负责 ihtml 领域的 Fast/Deep Agent、UI 工具、确认边界与会话治理。两种模式共享同一套前端协议，但不会在一个进程里叠加两套模型循环。

旧的 `ihtml.New` / `chat.New` 为兼容保留；新集成推荐 `NewHandler` / `NewChecked`，让所有配置错误在开始监听前返回。公共协议字符串均有导出常量，模型能力响应带 `protocol_version`。当前仍处于 pre-v1 阶段；发布 v1 前会继续保持新增优先、破坏性调整集中并提供迁移说明。

完整兼容约定见 [COMPATIBILITY.md](COMPATIBILITY.md)，未发布变更见 [CHANGELOG.md](CHANGELOG.md)。许可证为 [MIT](LICENSE)，最低 Go 版本 1.26.9（官方 OpenAI SDK 要求 Go 1.26，使用已修补版本；CI 同时验证最低系列与最新稳定版）；正式发版前仍需由项目所有者打首个 semver tag。

### 独立 AI 助手与复用指南

只需要对话页面时使用 `ihtml.mountChat`，无需挂载工作台或加载 Items。它和工作台抽屉共用消息渲染、流式事件、取消、模型选择和身份隔离：


先选择接入方式，避免无意中再建一套 Agent 或历史系统：

| 现有项目 | 推荐入口 | 宿主仍然负责 |
|---|---|---|
| 需要完整动态工作台 | `ihtml.mount` / Go `NewHandler` | 身份解析、Store 与业务权限 |
| 已有页面，只需要聊天组件 | `ihtml.mountChat` | 面板高度、入口与页面生命周期 |
| 已有 Agent 和原生业务接口 | `mountChat` + `hostTransport`，或 Go `WithChatBackend` | 模型执行、会话、配额、审计与原生授权 |
| 没有自己的 AI 后端 | `ihtml/chat.NewChecked` | 模型配置与密钥注入；不要与已有 Agent 叠加 |

> `mountChat` 不会自动得到项目的历史列表、删除接口或业务权限。下面的可选能力需要显式接入。浏览器显示消息不等于服务端模型上下文。

```js
const assistant = ihtml.mountChat(document.querySelector('#assistant'), {
  base: '/ui/', theme: 'inherit', storageKey: 'project-assistant',
  auth: getHostToken, connectionAuth: getShortLivedTicket,
  contextProvider: ({context}) => ({page: {...context.page, path: '/orders'}}),
  onChatEvent: event => observeChat(event),
});
await assistant.ready();
await assistant.chat.send('查看本周订单');
const messages = assistant.chat.getMessages(); // 有界显示快照，可用于路由重挂载
assistant.theme.set({mode: 'dark', primary: '#059669'}, {persist: false});
const offTheme = assistant.theme.onChange(theme => observeTheme(theme));
// 页面离开时 assistant.destroy()；换用户时 await assistant.refreshAuth()。
```

`mountChat` 与工作区公开相同的 `theme.get/set/onChange`；主题更新保留当前对话、连接和待确认卡，各实例独立处理系统明暗。`set(patch,{persist:false})` 禁止将改动写回远端主题；挂载时提供宿主 `theme` 配置的实例由宿主管理，不写本地或远端主题。订阅返回取消函数，销毁后不再通知。设置面板手动选择深浅模式时，会清除固定背景／正文配色以恢复自适应调色板，保留主色、字体和圆角；直接调用 `theme.set` 仍保留未指定字段。

#### 可选能力：接入时逐项核对

| 选项 / 公共接口 | 用途与默认行为 |
|---|---|
| `history: {list, select, remove?}` | 历史列表、分页、恢复及可选删除；默认关闭，不产生历史请求。没有 `remove` 就没有删除按钮 |
| `history.entry: false` | 宿主已有历史图标时隐藏 SDK 重复入口，调用 `chat.history.open/close/refresh()`；`onOpenChange` 同步显示状态 |
| `history.description` | 历史保留期限、可见范围等纯文本说明，由宿主按实际存储规则提供 |
| `chatHeader: 'compact'` | 隐藏重复标题，保留紧凑操作行 |
| `chatHeader: 'none'` | 宿主接管整个标题栏；必须自行提供“新对话”等入口，调用原有公共方法 |
| `starters: string[]` | 最多四个空对话示例；在消息区滚动，首条消息提交后隐藏，恢复已有消息时不显示 |
| `initialMessages` / `chat.getMessages()` | 最多 100 条、总计 512 KiB 的显示快照；不是可信历史，不含可继续执行的旧授权 |
| `chat.sessionId` | 当前 SDK 的稳定 `csid`，非认证凭据；连续追问和重连沿用，显式新对话换号 |
| `chat.clear()` / `onNewChat` | 开始新对话并通知宿主解除原生继续绑定；不删除历史、不撤销业务操作 |
| `beforeConfirm` | 接入宿主的原生确认/身份核验；不能替代服务端执行时再次鉴权 |
| `auth` / `connectionAuth` | HTTP 鉴权与连接短票据分离；长期凭据不要放进 URL |
| `contextProvider` / `onChatEvent` | 页面上下文与脱敏事件观察；都不构成服务端授权 |
| `theme` / `theme.set` / `refreshAuth` / `destroy` | 宿主换肤、身份变化及卸载时的标准生命周期；不应为了换肤重挂载 |

#### 历史对话如何接入

SDK 统一渲染列表和交互，宿主提供自己的 API 适配器。数据契约是：

```js
history: {
  // cursor 是宿主的不透明分页游标；只有明确还有下一页时才返回新 cursor。
  list: async ({cursor, signal}) => ({
    items: [{id: 'server-conversation-id', title: '整段对话标题',
             subtitle: '更新时间', selected: false, removable: true}],
    cursor: '',
  }),
  // 读取本人原生记录，重挂载时提供 initialMessages，并绑定服务端认可的继续凭据。
  // 必须等待读取完成；signal 取消或身份变化后，不得恢复迟到的旧对话。
  select: async ({id, signal}) => openNativeConversation(id, signal),
  // 可选：在宿主确认后删除整段对话。只有服务器明确确认删除成功才返回 true。
  // 取消返回 false；失败抛出错误，不能只从浏览器列表移除。
  remove: async ({id, signal}) => deleteNativeConversationAfterConfirmation(id, signal),
}
```

`openNativeConversation`、`deleteNativeConversationAfterConfirmation` 是宿主实现的适配函数，不是 SDK 自带接口。`list` 每页最多 100 条；`select` 负责原生继续绑定，`initialMessages` 只负责显示。读取/选择有 30 秒超时，删除含确认阶段为 120 秒，均提供取消信号；删除失败或结果未知时不会自动重试。权限、归属、并发与幂等必须由宿主 API 校验，不能信任行里的 `selected` 或 `removable`。

**一轮提问（turn）、对话（conversation）与传输/执行 session 要分开。** 历史 ID 应标识整段对话；连续追问追加原记录，重连不能变成新历史。原生后端可把 `csid` 映射到自己的对话 ID，不能把它当访问凭据。点击新对话时，宿主必须在 `onNewChat` 中解除旧的 `params.conversation` 或原生继续令牌并重新绑定，不能只清屏后仍把新问题写回旧对话。

删除当前对话成功后，宿主应通过 `chat.clear()` 并解除继续绑定；删除其他对话不应清空当前对话或草稿。服务端应阻止旧连接/迟到写入复活已删除记录。删除对话消息不等于撤销已经执行的业务操作，相关回执、计费、必要审计及保留策略仍由宿主管理；SDK 不会代为承诺或清除这些数据。

#### 上线前验收

- 连续追问与断线重连仍落到同一条原生历史；新对话独立保存，恢复后继续原对话。
- 历史列表、分页、详情与删除均校验当前用户和租户；切换账号后旧响应不能恢复界面。
- 删除必须有明确确认和服务端成功回执；取消、超时或结果未知不伪报成功，不自动重试写入。
- 恢复文本不会恢复旧确认卡或业务授权；所有执行仍由服务端重新鉴权。
- 手机有足够的消息滚动区域，输入框可见；宿主标题/示例不要与 SDK 重复常驻。
- 卸载调用 `destroy()`，权限/用户变化按实际作用域 `refreshAuth()` 或重建；换肤用 `theme.set()`。

完整选项、原生传输及确认契约见 [AI 接入指南](docs/ai-integration.md)。不要复制某个项目的持久化策略作为 SDK 默认能力。

宿主签发确认卡时，可设置 `ProposedToolCall.Presentation`（JSON `presentation`）：`confirm_text`、`cancel_text`、`reason_label`。字段只控制纯文本显示，空值或无效类型使用原本地化文案，最长显示80字符；不授予执行权限，也不改变 `beforeConfirm` 或确认协议。确认与取消文案应明确描述操作；签名器覆盖完整 presentation，宿主必须先设置文案再签名。未设置 presentation 的旧卡保持原签名。

挂载点由宿主设置高度；上下文是非可信浏览器输入，不能用于授权。显示快照也不是可信模型历史。扩展接口、原生 Responses 模型、宿主 Agent 接入、历史与工具权限边界，以及 nbco 实践总结见 [AI 接入指南](docs/ai-integration.md)。可运行的独立助手示例：`go run ./example/assistant`，默认只在本机运行确定性预览，不调用付费 API。

### 通用界面工具

已有业务 Agent 的宿主可使用 `uitools.ReadTools()`／`WriteTools()`，将 `Name/Label/Description/Parameters/Write` 转成自身工具注册；`Read(ctx,runtime,args)` 查询，`Prepare` 返回不修改数据的 `Draft`，`Apply` 执行已批准的参数。`Runtime.Service` 必须来自受信用户绑定的 `ScopedService`；`CheckItems` 是卡片写入和恢复历史版本的必填代码政策，准备和实际写入均检查；整页发布要求 `ScopedPagePublicationService` 原子能力，缺失时拒绝，不以多步写入代替。`FormatTime`／`ChangedBy` 可由宿主配置。

工具不实现业务身份、实时授权、额度、确认签名或审计；这些由宿主在调用 `Apply` 前核验并保留。公司模板审批和经营数据政策也留在宿主。内置聊天工具保留原生确认及浏览器实时自检，和该包共享可兼容的解析及页面归属校验。

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
| `ihtml.http(path, options)` / `http.get(path, options)` / `http.post/put(path, body, options)` / `http.del(path, options)` | 同源请求封装；直接调用等价于 GET，自动透传用户参数 |
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
| `PUT /api/pages/{name}/publish` | 原子发布完整页面：一次提交同时更新页面注册项并替换该页全部 Item；不影响全局及其他页面 Item |
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
- 确认后续办由实际桥接配置声明：`manual_tool_only` 只执行当前签名卡；同时接入 `Confirmation` 和 `ContinueAfterTool` 时声明 `managed_turn`，回执保存后复用普通受管轮次继续核对原请求，后续写操作仍需分别生成并确认新卡片。`checkpoint` 仅用于已接通原生中断恢复的 `NativeConfirmation`。能力不能由模型列表伪造，续办失败或结果未知不能重复提交原操作。
- 共享聊天组件根据该能力展示确认说明，宿主不应覆盖 `agent_manual_confirm` 或把 Deep 改名为“操作确认”。只有一种后端模式时隐藏整条模式栏及预留空间，实际发送仍沿用该模式；存在多个模式时才显示选择器。
- 工具：`ui_item_list/get/put/delete`、`ui_items_replace`、`ui_page_list/set/publish/delete`（`publish` 原子替换一个页面的注册项与完整 Item 集合）、`ui_theme_get/set`、`ui_kv_get/set`、`ui_revision_list/rollback`、`ui_page_errors`、`ui_page_inspect`（经会话 WS 请求内核回传实时渲染状态：按视口交集、祖先计算样式与 overflow 裁剪估算每个 Item 的可见性，并返回尺寸、文本摘要与报错；支持 `ids` 精确过滤及 `offset`/`limit` 分页，每页最多 20 项，按 `next_offset` 继续；页面离线时数秒超时返回）、`api_probe`（只读探测业务 API）、`ui_plan_update`（Deep 模式的结构化计划工具），以及结构专用的 `ui_template_get/put_current/publish/slot_delete`
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
type PagePublicationService interface { … } // 可选: 页面注册项 + 页内完整 Item 原子发布
type Service interface { … }      // 完整受信任管理 API；*Handler 实现
type ScopedService interface { … } // 已绑定一个用户；请求路由优先用 ServiceForRequest
type ScopedVersionedKVService interface { … } // 对 ScopedService 做类型断言取得
type ScopedPagePublicationService interface { … } // 对 ScopedService 做类型断言取得
```

`VersionedUserStateStore` 的版本 0 只表示“从未写入”。每个成功写操作（含删除不存在的 KV）都必须推进版本；`EraseUser` 也必须留下更新后的空状态墓碑，不能重置或复用版本。公共 KV 遇到 `ReservedKVPrefix` 会返回可用 `errors.Is(err, ErrReservedKVKey)` 判断的错误。第三方 Store 应运行 `storetest.Run`，它会同时验证并发 CAS、RawMessage 字节保真、输入/输出所有权和删除后的版本语义。`FileStore` 用可响应 `context` 取消的进程级 gate 与系统文件锁支持同目录多句柄/协作进程 CAS，新数据文件为 `0600`，v2 文件同时写精确 `kv_bytes` 和旧版可读的语义 `kv`；它适合小规模单版本部署，升级/回滚应停写切换，不能让不识别锁协议的旧二进制与新版并行写。生产数据库仍优先使用 `sqlstore`。

编辑器或多标签页应使用 `KVGetWithRevision` + `KVCompareAndSet`，不要自行实现“先 GET、再无条件 PUT”。版本令牌是服务端基于 RawMessage 完整字节生成的不透明 SHA-256 表示；首次创建使用 `MissingKVRevision`。冲突返回 `KVConflictError`（同时匹配 `ErrKVConflict` / `ErrConflict`），其中包含最新版本和值。HTTP `GET /api/kv/{key}` 会增量返回 `revision`，`PUT` 可选传 `expected_revision`；字段缺失仍保持旧版无条件写语义。内置 Versioned Store 可跨 Handler/副本原子比较并写入；第三方 legacy Store 仅保证单 Handler 锁内语义。`DeleteItems` 会在同一用户状态事务中清理所有 `item-layout.*` 引用。

### 确认后继续多步业务（可选）

已签发确认卡的排队容量使用 `Confirmation.MaxPendingTools` 的独立有界额度，不与 `MaxPendingTurns` 的普通消息队列争用；普通消息满载时，已展示给用户的合法确认仍可排队。重复点击同一 nonce 只执行一次，取消会释放原卡排队状态。已确认卡也不占用普通消息或续办的额度；排队中的相同宿主续办事件会合并为一次重新读取权威回执，避免批量确认引起重复模型规划。不同事件不合并，续办未受理时不向页面标记 continuation_pending。此规则不延长卡片期限、不扩大工具权限，也不会自动重放执行结果未知的工具。

`assistant.Config.ContinueAfterTool` 可在已确认动作执行并经 `CompleteTool` 保存后，返回一个新的 `ChatClientUserMessage` 事件。框架将其送回普通轮次队列，重新调用宿主 `PrepareTurn`、实时鉴权和工具治理；新的写动作仍生成新确认卡，不继承上一张卡的权限。错误、unknown、公开回执标为 error 时不续跑。未配置时保持原有无工具结果总结；配置并返回事件时跳过重复总结。返回 nil 可结束原计划。宿主应从服务端对话和原任务回执决定剩余范围，不能扩展原名单或重放未知动作；后台作业仍由宿主持久化。

`PublicTurnError` 可把内部错误映射为固定公开文案，切勿返回供应商错误原文、令牌或调用参数。历史、错误分类和后台进度仍由宿主存储。

宿主通过 `ContinueAfterTool` 重查权限后，可直接返回 `assistant.NewToolContinuation(ctx, result)`，复用通用续办事件，不自行复制提示词与执行状态判断。`agent.ClassifyTurnFailure(err)` 提供不包含错误原文的稳定分类；具体业务文案由宿主决定。

如宿主持有会话租约，可通过 `assistant.Config.TurnLifetime` 返回已领取轮次的生命周期。桥接层统一合并租约、用户停止和连接关闭的取消信号；宿主只负责续租与存储。成功收尾时释放租约不会把已完成轮次误报为取消。后台业务作业的寿命仍由宿主管理，不依附本轮模型上下文。

### Agent 宿主接入核对

`agent/eino` 使用 Eino `deep.New` 与 `adk.Runner` 执行多轮工具调用，不在宿主重新实现推理循环。工具目录较大时，只把常用入口标为 `ToolLoadImmediate`，其余保留 `ToolLoadDeferred`，交给原生 `tool_search`；延迟加载不会赋予新权限。工具作用域在压缩前记录真实检索结果，避免已加载工具被摘要隐藏。摘要使用原生 summarization middleware 的任务摘要配置，禁止摘要请求调用工具，Responses 摘要降低推理开销；Finalize 保留最近两组完整工具往返（合计不超过64 KiB，不截断 JSON 或加密元数据），准确保留任务编号及预览指纹。公开进度只发送压缩阶段名称，`TurnResult.ToolExposure.ContextCompactions` 记录实际压缩次数。

需要保留工具轨迹时，通过 `einoengine.WithRuntimeStore` 注入宿主存储，并保存 `EngineSession`；`DisableSession:true` 明确关闭此能力。产品历史与引擎工作记忆分开，不能把原始 checkpoint 或推理轨迹当作历史对话返回。`agent/eino/sqlstore` 提供 PostgreSQL 适配器；数据库迁移、租户隔离、执行锁及保留策略仍由宿主负责。

需要原生确认中断时，使用 `assistant.Config.NativeConfirmation` 的 Pause/PrepareResume/Pending 与 `RequireApproval`、`ResumeRequest`，让 Eino checkpoint 继续原轨迹；它与手动 Confirmation 互斥。已有业务确认宿主可以选择 `ContinueAfterTool`，确认后进入新的受管轮次，不应把这种方式宣传为跨进程恢复原模型轨迹。独立业务作业的恢复以宿主原任务回执为准。工具未知结果不重放，人工取消不能被后台续跑重新排队覆盖。

GPT-6.1 Terra 的工具调用必须使用 `OpenAIAPI:"responses"`；SDK 在发送错误协议请求前拒绝，普通无工具文本请求不受该规则影响。模型可用性以实际账户的模型列表和代表性流程验收为准。

显式真实模型验收：`IHTML_LIVE_AGENT_TEST=1 GOWORK=off go test ./agent/eino -run TestLiveResponsesAgentWorkflow -v`。只读取统一 `~/.config/credentials` 内 `iclouder_ai` 凭据；Sol/Terra 对同一组十个合成身份执行只读工具、本机计数回执、原生压缩、checkpoint 暂停/恢复、重复恢复拒绝和后续会话核对。默认测试跳过网络调用；测试不清空真实云盘、不转移真实身份。测试 Store 为内存存储，只证明重建 Engine 后使用同一 Store 恢复，不能据此声称某个宿主已经接好持久数据库。

实现依据：[Eino Runner](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_extension/)、[Eino 中断恢复](https://www.cloudwego.io/docs/eino/quick_start/chapter_07_interrupt_resume/)、[Codex harness](https://developers.openai.com/blog/codex-as-a-platform)、[Claude tool runner](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-runner)、[Terra 模型](https://developers.openai.com/api/docs/models/gpt-6.1-terra)。

工具等待进度：宿主工具可调用 `agent.ReportStatus(ctx, "正在等待原任务…")`，桥接器将其投影为当前受管轮次的 `assistant_status`；不新增模型调用或工具记录，不依赖 `PrepareTurn` 返回指针（请求会被复制）。原确认工具继承可信执行上下文，但进度重新绑定本次确认轮次。自行接入引擎的宿主可用 `agent.WithStatusReporter` 绑定已有状态通道。报告有长度与取消校验，只是展示进度，不是成功回执或新增批准。后台任务等待／归属仍由宿主业务 API 处理。

### 业务预览与确认卡共用期限

宿主 `Confirmation.Preview` 可将可信业务预览的 Unix 秒期限写入返回卡的 `ConfirmationExpires`。桥接层用 `Signer.PrepareUntil` 取业务期限与默认15分钟的较早值；不能延长原预览，签名和 nonce 仍由 SDK 生成。直接使用签名器的宿主也应调用 `PrepareUntil`，不要用模型或浏览器输入当期限。

宿主判定预览已过期或留给操作者的时间不足时，返回 `assistant.ErrPreviewRefreshRequired`。共享桥接层返回固定的 `preview_refresh_required / not_started` 工具回执，不签发旧卡、不执行操作，模型可在当前轮次按原范围刷新预览。业务扫描、名单和刷新窗口由宿主原接口负责，SDK 不创建另一套业务执行器。

共享聊天界面显示实际签名期限、分秒倒计时和临期提示；到期撤下确认按钮，提供“刷新预览”。该按钮只发送新的普通对话请求，不重放旧卡或继承批准。点击确认后等待宿主检查或重连期间跨过期限，也不会发送确认帧。服务器仍是最终执行准入依据。

已获得且保存的明确业务拒绝回执（Handler 无执行异常）可由宿主 `ContinueAfterTool` 接回普通轮次，重新核查并生成新卡；执行异常、未知结果或取消不会自动续办。后续写动作仍须重新确认。

### 聊天连接自动恢复

共享聊天组件在异常断开后按1、2、4秒逐步退避重连，最长30秒；每次重连重新调用 `connectionAuth` 取得新票据，保留同一个 `csid`。断网或页面在后台时暂停重试，网络恢复及回到前台时立即尝试。握手10秒无响应会关闭该次连接后重试；短暂握手成功又断开不会重置退避。

重连仅恢复传输，不重发用户消息、确认卡或取消帧，也不把中断轮次冒充完成。后台作业和未知操作按宿主保存的原编号继续核对。连接范围内的旧确认仍按原规则失效。主动停止、新对话、登出、销毁会取消重连；明确撤权、认证拒绝或不可恢复的协议错误停止自动重试并给出提示。

### 可选后台执行与完成通知

原生宿主可用 `assistant.NewBackgroundBackend(assistant.BackgroundConfig{Backend: existingBackend, Lifetime: serviceContext, Key: trustedKey, Retain: retainOwner, Observe: persistProgress})` 包装**原有**执行器，再传入 `WithChatBackend`。`Key` 必须绑定服务端认证用户、当前权限代次和整段对话号；不能接受客户端声称的 owner。`Retain` 保持宿主会话资源，返回释放函数。`Observe` 保存脱敏进度、最终状态和通知意图，正文仍复用原历史回调；接纳时即保存用户请求，不能等到回复结束才保存。

后台能力会声明 `background_execution:true`：浏览器关闭/断线只解绑订阅；重新打开主动取新票据、接回原执行，**不重发用户消息或确认帧**。显式停止才取消原模型轮次，已被业务系统接纳的任务仍需按原编号核对。重连保留原签名域，未过期确认卡可以恢复；它不延长业务预览期限，也不允许自动批准。进度使用全局序号去重和全文快照，避免丢线前缀重复。 产品历史与回放正文去重时，仍在续办的轮次保留进度容器，只隐藏重复文字；只有已结束且没有工具记录的重复轮次才移除。 确认操作的开始/执行回执必须保留原卡的 `tool_call.id` 与 `confirmation_nonce`；前端据此跨轮次更新原卡，历史重开后不会再次显示已执行操作为待确认。不同 nonce 的操作互不影响，迟到的待确认事件不覆盖已受理或已完成回执。 即使同轮次复用操作编号，不同 nonce 的卡片也各自保留；跨轮次缺少 nonce 的回执不能更新旧签名卡。撤销失败只代表撤销请求失败，不是原操作的执行结果；真实执行回执会清理旧的撤销等待与错误提示。 本地授权过期独立显示为 `expired`，不能伪称服务端已取消或未执行；后续真实回执可纠正该显示。包含 `accepted` 回执的旧轮次摘要显示“操作请求已受理”，不把异步业务误报为结束。

包装器在单进程保留执行和有界展示回放；这不是跨服务重启的模型栈恢复器。多副本需要粘性路由或宿主分布式执行协调。宿主必须持久保存产品历史、任务编号、回执与通知，服务退出后的未知业务状态查询原记录，不能自动重新执行写操作。旧展示轨迹的回放预算不限制模型工具上下文。

`mountChat` 可选 `notifications` 沿用历史面板合同：`{title, entry, description, list({cursor,signal}), select({id,signal}), onOpenChange, onUnreadChange, remove?, clear?, removeLabel?, clearLabel?}`。`list` 返回 `{items,cursor,unread}`，`chat.notifications.open/close/refresh()` 是公开入口；同一时刻只展示历史或通知面板。宿主负责通知归属、版本已读、持久队列和逐设备投递回执。

可选 `notifications.push={publicKey,subscriptionIds,subscribe(subscriptionJSON),remove(subscriptionJSON)}` 增加系统推送设置。只有用户点击才请求浏览器权限，`subscribe` 必须在服务端保存后返回 `{enabled:true}`。`subscriptionIds` 为当前用户已登记端点的 SHA-256；不能仅凭浏览器已有订阅就宣称当前用户已开启。`push` 包复用标准 Web Push 加密传输；VAPID 私钥由宿主凭据配置保管，不返回前端。固定同源资源 `ihtml-push-sw.js` 只显示通知和打开同源页面，不执行业务。宿主需放行该 SDK 静态资源；`/assistant/` 集成使用同路径 scope，不覆盖站点已有根 Service Worker。iOS 可能要求将站点添加到主屏幕；不支持或拒绝权限时站内通知照常可用。

### Responses 原生上下文压缩

`agent/eino.Config{Provider:ProviderOpenAI,OpenAIAPI:"responses",ContextCompaction:"provider"}` 使用 `context_management:[{type:"compaction"}]`。未显式设置 `SummarizeAfterTokens` 时**省略** `compact_threshold`，由服务端决定；不套用 Eino 的 24k 默认值，也不再按 80 条消息提前触发。供应商没有给所有模型推荐同一个固定阈值，不能把文档示例阈值或整个模型窗口当作推荐值。参见 [官方压缩指南](https://developers.openai.com/api/docs/guides/compaction)。

加密 compaction item 只在协议工作记忆中原样传递，不展示其正文；回放保留当前 system/developer 约束及新的工具调用/回执。`ContextCompaction:"eino"` 保留 Eino summarization middleware，`SummarizeAfterMessages:0` 不启用消息数量触发。工具列表先返回筛选所需的摘要与分页，详情按需读取；压缩不能掩盖丢失的任务编号、冻结指纹或授权范围。

受管确认的中间回执使用 `assistant_done.continuation_pending:true`：仅当前确认调用已经收尾，原请求仍会进入下一轮。客户端保持流程运行、下一 `run_id` 归入同一流程卡，不把“已受理”当作整个请求完成，也不重复把回执摘要当正文。普通终态和停止不设置该字段；业务完成仍须核查原任务。实时消息和断线回放使用同一归属逻辑。

通知条目可额外提供 `subtitle`（进展）、`meta`（时间等短信息）、`unread`（未读标记），布局和行操作由共享组件统一渲染。可选 `seen({ids,signal})` 在通知面板打开、当前页渲染后调用，后台未读摘要轮询不会调用；宿主按已展示版本提交已读，返回 `{readIds,unread}`，仅在服务器确认后去掉未读标记。`unread` 应重新查询服务器总数，避免清掉并发产生的新通知。已读失败保留原未读状态，不影响查看通知。

通知可选 `remove({id,signal})` 和 `clear({signal})` 复用历史面板的删除交互，均须由宿主完成确认并返回 `true`（取消返回 `false`）。未提供回调时不显示相应按钮；`removeLabel`、`clearLabel` 可定制文案。清空后重新读取服务端列表与未读数，避免吞掉并发新通知。宿主应只清除操作者确认的通知快照；删除对话还应关联移除通知并阻止迟到事件复活，通知删除不代表停止任务或删除历史。

### 确认卡的业务说明与实时进度

宿主在 `ProposedToolCall.Presentation` 中提供 `ConfirmText`、`ReasonLabel` 和 `Facts: []ihtml.ToolCallFact{{Label: "处理对象", Value: "完整名单"}}`。Facts 是明确的展示投影，会替代从 Body 猜测生成的信息；原始 Body 仍在技术详情中。应提供准确对象、范围、操作影响，不能用内部参数名或模糊的“确认此操作”代替说明。展示内容与请求一起签名，修改后必须重新预览确认。不要把密码、令牌等敏感数据放进展示文本。

`assistant_status` 表示当前执行进度，折叠摘要直接显示。流式正文和思考不会覆盖它；下一工具阶段、暂停、完成或失败会结束原进度阶段。业务无需自行展开工具面板或解析自然语言来控制进度展示。

窄聊天区域会把工具数和耗时放到进度下方，桌面侧栏与手机使用同一容器宽度规则。受理回执折叠时只显示状态与标题，展开仍保留原始说明，不把提交已受理等同于业务完成。实时耗时按秒、分、小时显示；原生历史回放尚无权威耗时字段时不显示重新连接后的计时，避免把新连接的 0 秒冒充原流程耗时。
