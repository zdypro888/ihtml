# AI 接入与扩展

## 选择唯一的 AI 所有者

| 项目形态 | 接入方式 | 谁负责模型循环与业务治理 |
|---|---|---|
| 已有自己的 Agent/Orchestrator | 实现 `ihtml.ChatBackend` | 宿主，ihtml 只负责 UI/协议 |
| 已有 `agent.Engine` 或采用共享 Eino | `assistant.NewChecked` | Engine 负责执行，宿主 hooks 负责权限/历史/审计 |
| 没有 AI 中枢，主要操作动态页面 | `chat.NewChecked` | 内置 UI 领域 Agent、确认卡、预算和历史治理 |

不要同时启动两套 Agent 处理同一轮。核心 `ihtml` 不导入 Eino，AI 完全可选。
通用包不依赖 nbco 的人员、Worker、审批数据库或业务工具。

### 助手能力边界

| 能力 | `chat.Backend`：完整 UI Agent | `assistant.Backend`：宿主 Engine |
|---|---|---|
| 模型循环与工具 | 内置 UI 领域工具与治理 | 宿主 Engine，宿主提供授权后的工具 |
| 动态页面修改 | 内置页面工具与确认卡 | 不自动提供 UI 写工具；宿主显式授予能力 |
| 工具确认 | 内置确认协议 | 可选 manual `ConfirmationConfig` 或原生 `NativeConfirmationConfig`；必须配置执行 guard |
| 确认后的计划 | 不应把 manual-only 描述为 checkpoint 恢复 | manual 仅执行指定工具；native 从持久检查点恢复同一 Agent |
| 浏览器 inspect | 内置页面检查协议 | 不自动实现 `inspect_request/inspect_result`；浏览器 context 始终不可信 |
| 历史与断线恢复 | 遵循内置 chat 的会话治理 | 宿主拥有业务历史；连接内 pending 工具不代表持久恢复能力 |
| 后续扩展入口 | 领域工具、模型工厂与既有 chat 配置 | `PrepareTurn`、`CompleteTurn`、`ToolExecutionGuard`、自定义 `agent.Engine`；完整私有协议可实现 `ChatBackend` |

`manual_tool_only` 不是中断计划的恢复：确认后只执行一条服务端保存的调用。
签名、nonce 与确认卡不授予业务权限，执行前的实时权限 guard 不可省略。
跨连接、进程重启或页面重新挂载的持久恢复，需要宿主另行定义受保护的状态存储、
身份绑定、幂等与审计；`initialMessages/getMessages()` 只能恢复显示，不能恢复待确认授权。

## 原生 Eino 与 Responses

```go
import (
    "context"
    "os"
    "github.com/zdypro888/ihtml/agent"
    einoengine "github.com/zdypro888/ihtml/agent/eino"
)

engine, err := einoengine.New(context.Background(), einoengine.Config{
    Provider: einoengine.ProviderOpenAI,
    OpenAIAPI: "responses",
    APIKey: os.Getenv("OPENAI_API_KEY"), // 仅由宿主显式读取，不写入源码/日志
    Model: "gpt-6.1-sol", // 使用账户可用的 API model ID，不是 UI 显示名
    ReasoningEffort: "medium",
    MaxCompletionTokens: 8192, MaxTurns: 32, TimeoutMS: 120000,
    AgentName: "orders", SessionNamespace: "orders:assistant:v1",
})
if err != nil { /* 在开始监听前处理错误 */ }
result, err := engine.RunTurn(ctx, &agent.TurnRequest{
    System: "你是订单助手。", UserText: "查看订单",
    Tools: authorizedTools, // 宿主按已验证用户选择
    OnDelta: func(snapshot string) { /* 累积快照，不是增量片段 */ },
})
```

`NewChatModel` 单独暴露相同 transport，供已经拥有 Eino Agent 的项目使用。
Chat Completions 用 `OpenAIAPI: "chat_completions"`；Claude 用
`ProviderClaude` 且不设置 OpenAIAPI。模型不存在或账户无权限时返回真实错误，
不静默降级到另一模型。配置和 APIKey 是程序化结构，不是安全的持久配置格式；
不要把含密钥的 Config 编码到日志或数据库。

Responses 保存加密 reasoning 与原始有序输出项，下一次工具迭代原样回传；
不能将 reasoning、message、function_call 任意压平成旧 Chat 消息。
`store=false`，Eino 仍是本地工具循环的所有者。原始模型工作记忆需要受保护，
对外错误/工具状态另做最小投影，不能先脱敏 JSON 再拿来恢复执行。
默认 HTTP transport 不跟随重定向，避免凭据转移到其他地址；显式 HTTPClient
覆盖后由宿主负责相同约束。

已有 `chat.Backend` 只想改为原生 Responses 时，设置 `chat.Config.ModelFactory`：

```go
ModelFactory: func(ctx context.Context, cfg chat.Config) (model.ToolCallingChatModel, error) {
    return einoengine.NewChatModel(ctx, einoengine.Config{
        Provider: cfg.Provider, APIKey: cfg.APIKey, BaseURL: cfg.BaseURL,
        Model: cfg.Model, OpenAIAPI: "responses", ReasoningEffort: "medium",
        MaxCompletionTokens: cfg.MaxTokens,
    })
},
```

这里 `model` 是 `github.com/cloudwego/eino/components/model`。ModelFactory
只改变模型构造，现有 chat 预算、UI 工具、确认与历史仍由同一个 Backend 掌控。
它是显式程序化扩展，不是 `chat/config` JSON 字段。

## 通用助手桥接

```go
backend, err := assistant.NewChecked(assistant.Config{
    Engine: engine,
    Models: &ihtml.ChatModels{
        ModelReady: true, CurrentModel: "gpt-6.1-sol", Models: []string{"gpt-6.1-sol"},
    },
    PrepareTurn: func(ctx context.Context, session *ihtml.ChatSessionContext,
        event *ihtml.ChatClientEvent) (*agent.TurnRequest, error) {
        // session.User 来自服务端 UserResolver；不能使用 event 的客户端身份。
        // event 只保留当前用户消息，浏览器历史不能充当可信模型历史。
        return prepareAuthorizedRequest(ctx, session.User, event)
    },
    CompleteTurn: persistTurnAndAudit,
    ToolExecutionGuard: verifyLiveAccessAndHostApproval,
})
handler, err := ihtml.NewHandler(store,
    ihtml.WithUserResolver(resolveVerifiedHostSession),
    ihtml.WithChatBackend(backend),
)
```

`prepareAuthorizedRequest`、`persistTurnAndAudit` 等是宿主函数，不是库函数。
PrepareTurn 负责可信 System/History、允许的 Model/Tools、租户会话和预算。
客户端 page/selection/context 都只能作为非可信信息附加，不能覆盖系统指令或权限。
只读模式移除非读工具，客户端关闭工具时移除全部工具；需要审批的工具没有
ToolExecutionGuard 时不可执行。已返回工具仍应在 Handler 内检查实时权限，
并用 `agent.ToolInvocationKey(ctx)` 实现宿主的幂等回执。

同一连接串行排队；取消绕过队列并清除待执行轮次。Close 取消并等待真实工作
退出，因此宿主 Engine/hooks 必须响应 context，不能在回调里递归 Close。
CompleteTurn 收到部分结果与错误，错误会抑制成功消息；若取消后仍需保存审计，
宿主应使用自己管理的有界清理上下文，不能无限后台执行。
桥接层可显式启用下述 manual-only 或原生 checkpoint 确认卡，不实现浏览器检查。
需要完整 UI 领域工具/inspect 时使用内置 chat 后端，或像 nbco 一样实现自己的
ChatBackend。默认投影不暴露原始工具参数/结果或隐藏推理；确认卡的公开内容由宿主控制。

### 可选的手动工具确认

```go
signer, err := confirmation.NewChecked(confirmation.Config{})
if err != nil { /* 在开始监听前处理错误 */ }
backend, err := assistant.NewChecked(assistant.Config{
    Engine: engine, Models: models,
    PrepareTurn: prepareAuthorizedTurn,
    CompleteTurn: persistModelTurn,
    ToolExecutionGuard: verifyCurrentToolPermission,
    Confirmation: &assistant.ConfirmationConfig{
        Signer: signer,
        Scope: func(ctx context.Context, session *ihtml.ChatSessionContext,
            req *agent.TurnRequest) (confirmation.Scope, error) {
            // SessionID/SessionCapability 必须来自宿主可信绑定，不能使用浏览器字段。
            return confirmation.NewScope(req.SessionID, req.SessionCapability), nil
        },
        Preview: publicRedactedToolPreview,
        MaxPendingTools: 16,
        ToolTimeout: 30 * time.Second,
    },
    CompleteTool: persistConfirmedExecution,
})
```

新增 import 为 `github.com/zdypro888/ihtml/confirmation` 与 `time`；命名 hooks 是
宿主函数。`Preview(ctx, session, tool, args)` 返回 `ihtml.ProposedToolCall`，应只包含
可以公开的标题、风险、原因与参数摘要，不能暴露凭据或敏感原始参数。
桥接层生成确认 id/nonce/签名并绑定服务端 Scope。`ApprovalRequired` 工具被调用时
保存原始 handler/args，发 `confirmation_required`，返回 pending receipt；此时**没有执行**。
普通 Engine 轮次必须收尾，UI 才开放确认/撤销操作。

确认后验证签名并消费 nonce，只执行 stored 调用，执行前再运行
`ToolExecutionGuard`。签名表示调用完整性而非业务授权，guard 不可省略。
撤销消费同一 nonce 并发送 `cancel_tool_result`，不执行 handler。
`Access.ReadOnly` 会移除非读工具；不能为了让确认卡可点击而绕过真实写权限。

`CompleteTool(ctx, session, *assistant.ConfirmedToolResult)` 接收实际执行证据，包括
`Call/Tool/Args/Result/Err/Started/InvocationScope`。`Started` 区分执行前拒绝和已经进入 handler 的未知结果。
宿主在此合并权威审计和产品历史；有错误时
不能单凭 Result 宣称成功，更不能盲目重试未知副作用。它不自动改写或恢复 Eino 的
pending receipt，不重新运行模型或恢复原计划。复用 `EngineSession` 时，新增 History
可能不会重新注入已有原生工作记忆；后续轮次应在 `PrepareTurn` 中明确提供经过验证的
当前状态/执行事实，例如可信 System 中的当前 scratch marker，而不是声称历史已同步 runtime。

默认签名 nonce 与 pending 调用是内存状态。即使使用共享 nonce store，也不等于
持久化 pending handler/args；连接断开不支持持久恢复确认卡，页面显示缓存不是授权恢复。
原生 checkpoint 使用下述独立接口，不能伪称现有 manual-only 已支持恢复。

### 原生 Eino 暂停与恢复

原生 DeepAgent 配置 `WithRuntimeStore` 后，宿主设置 `TurnRequest.RequireApproval=true`。
`ApprovalRequired` 工具在进入副作用回执或业务 handler **之前**抛出 Eino 原生中断。
`TurnResult.PendingApproval` 包含检查点与各工具的原始参数、interrupt ID；
这是私有执行状态，不是可直接发送给浏览器的数据，也不是业务授权。
暂停结果的 `FinishReason` 为 `awaiting_approval`，不能作为成功轮次归档。
`Step.ApprovalPending/ApprovalRejected` 分别表示未执行与拒绝；跨恢复片段按
`ToolCallID` 合并调用/结果证据，而不是把暂停的空结果当作成功或重复记一次调用。
同一 EngineSession 尚有待确认检查点时，runtime 拒绝普通新 Run，避免覆盖旧计划。

```go
NativeConfirmation: &assistant.NativeConfirmationConfig{
    Pause: persistCheckpointAndIssueSignedCards,
    PrepareResume: claimOriginalTurnAndBuildAuthorizedResume,
    Pending: loadAuthenticatedPendingCards,
},
CompleteTurn: persistFinalResultAndReleaseClaim,
ToolExecutionGuard: verifyLiveAccessAndBusinessApproval,
```

三个 native hooks 与 manual `Confirmation` 互斥。`Pause` 必须在返回卡片前将
原轮次、EngineSession、InvocationScope、工具参数、检查点、进度及签名卡绑定持久化。
`PrepareResume` 必须验证当前用户与渠道、签名及到期时间，原子消费决策并领取原轮次，
从服务器记录构造 `ResumeRequest{CheckPointID, Decisions, ToolCallID}`，其中 `ToolCallID`
必须对应这张卡的原始调用，用于精确关联同名并行工具结果；不能接受客户端指定检查点、
工具参数或权限。恢复保持原 session 和幂等 scope，使用当前授权工具与实时 handler；
能力契约改变时 runtime 拒绝恢复。拒绝只返回未执行结果；未指定的中断继续等待。

`Pending` 负责重连加载仍然有效的签名卡。`Pause` 失败不发成功，已保存状态但发送失败
仍可重连恢复；不能把 socket 关闭等同撤销 durable approval。宿主应实现确认有效期、
数据库原子领取、执行代次 fencing、暂停记录清理与审计聚合。崩溃后的未知副作用
不可盲目重放，即使持久检查点存在，也不意味着外部调用恰好执行一次。

协议发送 `confirmation_required` 后发送 `turn_paused`，释放 UI 的忙碌状态而不宣称完成。
重连恢复以 `turn_start.restored_approval=true` 建立显示轮次。用户确认或拒绝启动新的
传输片段，但恢复的是同一 durable Agent，不是另建模型循环。最终结束才调用
`CompleteTurn` 并发送 `assistant_done`。直接使用 Engine 的宿主需自行处理这些契约。

## 可扩展对话页面

引入同一个 Handler 提供的 `/ihtml.css` 和 `/ihtml.js`，宿主设置容器高度：

```html
<div id="assistant" style="height:640px;max-width:100%"></div>
```

```js
const view = ihtml.mountChat(document.querySelector('#assistant'), {
  base: '/ui/', storageKey: 'orders-assistant', theme: 'inherit',
  auth: getHostToken, connectionAuth: getShortLivedTicket,
  initialMessages: [{role: 'assistant', content: '订单助手'}],
  contextProvider: async ({context, user, signal}) => ({
    page: {...context.page, path: '/orders'},
  }),
  onChatEvent: event => recordSafeTelemetry(event),
});
await view.ready();
await view.chat.send('查看订单');
view.chat.cancel();
view.chat.clear(); // 清空显示并轮换本地 csid，不删除宿主数据库历史
const messages = view.chat.getMessages(); // 可作下次挂载的 initialMessages
view.destroy();
```

事件 hook 是独立快照、尽力通知，不携带确认凭据，但文本仍可能含业务数据；
telemetry 必须遵守项目的数据策略。contextProvider 带 AbortSignal，换身份或销毁
后旧异步结果不会发出。初始消息只用于显示，不能用来注入可信模型历史。
每个实例使用不同稳定 storageKey，保留返回对象，不依赖 window.ihtml 的主实例。
getMessages 返回至多 100 条、合计 512 KiB 的独立显示快照，不含欢迎提示、工具或推理，
不代表数据库历史；宿主必须按已验证用户隔离缓存，登出时丢弃。
换用户前等待 `refreshAuth()`；路由销毁时调用 destroy 清理连接和监听。
模型 HTML 只走内核 allowlist 渲染，宿主不要把模型回答直接写入 innerHTML。

## 从 nbco 提炼的契约

- 模型执行、产品对话历史、私有 Eino 工作记忆是三件事：共享 runtime 不拥有业务数据库。
- 权限筛选在模型前，实时权限与审批在执行前；前端上下文从来不是授权。
- 持久 invocation key 连接工具执行与回执，迁移不能改变旧命名空间或静默丢失上下文键。
- 输出是累积快照；发生重试/回滚时必须发 assistant_reset，最终 done 是权威快照。
- 原始工具证据留在受保护工作记忆；网页只显示通用名称与状态，不泄露参数、结果或密钥。
- nbco 的工作台和独立助手共用 `/ui/` 协议及短期票据，后端仍然只有原有 Orchestrator。
- `eino:chat` 及旧 ID 校验只在 nbco 适配层保留；新项目显式定义自己的租户/能力命名空间。

## 运行与验证

`go run ./example/assistant` 提供本机独立助手；没有显式密钥时仅运行标明预览的
确定性 Engine，完全不读取 API 凭据、不发模型请求。点击 `Mark scratch` 提议更新
当前连接的内存标记，确认才修改；撤销无副作用，`Check marker` 读取真实标记。
确认卡只影响 scratch，不写文件、业务数据或 UI 页面；HTTP UI 写入口全部拒绝。
断开后的新连接从 `unmarked` 开始，不恢复待确认工具。
示例源码展示真实 Engine 和桥接层的组合，生产必须更换开发鉴权、
增加规范历史存储、权限校验、执行超时及审计。预览不声称完成任何真实业务操作。

回归套件：`go test -race ./...`、`node --test web/*.test.js`、`go vet ./...`。
前端测试首次运行先执行 `npm ci` 和 `npx playwright install chromium`。
`agent/eino` 包保留原生工具多轮、Responses 回传/取消/预算测试；assistant 包验证
队列、取消、投影、权限与生命周期；前端验证嵌入、隔离和异步扩展。

### 仅复用 UI、模型或 PostgreSQL 存储

宿主已有业务 Agent 时，可分别采用 `UIAssets()`、`ihtml.mountChat()` 与
`agent/eino.NewChatModel()`，无需创建第二个 Agent，也无需开放工作区的 Item
写接口。`UIAssets()` 仅提供 `/ihtml.js` 和 `/ihtml.css`，支持 ETag。
模型配置中的 `ReasoningSummary` 可选 `auto`、`concise`、`detailed`；不显示推理
不等于删除下一轮所需的加密推理状态。Responses 原始输出仍仅保存在服务端。

宿主操作卡可附 `OperationReview{Scope, Before, After, BlockingReason}`。SDK
以纯文本呈现变化并隐藏敏感键，长预览明确标为部分内容。阻塞原因会禁用确认；
宿主仍须在实际执行时重新验证规则，UI 禁用不是授权边界。
`mountChat` 的 `beforeConfirm({toolCall, signal})` 可以完成宿主的金额复核等
额外交互：只接受字面量 `true`，异常、超时、取消均不发送确认。工具参数为脱离
执行对象且移除秘密字段的副本，不能通过钩子修改将要执行的原始卡片。
钩子最长等待两分钟，宿主应监听 `signal` 关闭对话框。它不是服务端审批证明。

对于确认卡绑定原 WebSocket 的宿主，模型能力响应应声明
`confirmation_scope: "connection"`。SDK 在断线时废弃这些未执行的卡片，不能
通过重连执行。默认会话级卡片的历史确认和原生 checkpoint 恢复行为不变。
`client_context.page.query_filters` 可提供最多 32 条命名查询条件；上下文和过滤
条件均为不可信浏览器输入，宿主仍必须重新执行权限与查询校验。

`agent/eino/sqlstore.New[*schema.Message](db, Config{Namespace: "my-host"})`
提供 PostgreSQL 事件和 checkpoint 存储；显式 `Init(ctx)` 安装三张
`ihtml_agent_*` 表。宿主也可以在自己的版本迁移中安装同一 schema。
批次追加与序号分配在一个事务中完成，重复事件导致整批回滚。事件、游标和
checkpoint 按命名空间隔离，默认序列化器保留 typed Eino 消息。
`DeleteSession` 清理事件及对应 runner / approval checkpoint。

**存储不提供模型执行锁或宿主审批事实。** 宿主必须确保同一会话只有一个执行
者，并自行负责会话归属、能力变更、审批持久化、失效和保留周期。只有接通这些
边界后才能声明跨进程恢复；不可把模型私有工作记忆直接当作用户可读取的聊天
记录。事件与 checkpoint 单条上限 16 MiB。
宿主可配置 `Config.BeforeWrite(ctx, tx)`，在每次事件或 checkpoint 修改的同一事务中锁定并验证执行 owner；拒绝时整笔事务回滚。读取授权仍由宿主负责，`Init` 不调用该钩子。
测试：设置 `IHTML_TEST_POSTGRES_DSN` 后执行 `go test -race ./agent/eino/sqlstore`。

确认帧发出后若连接中断，或用户停止尚未回报结果的写操作，工具卡显示 `unknown`
而非“失败”或“已取消”，且没有重新执行按钮。已收到的成功/失败结果不会改写。
宿主可以通过 `verificationLinks(toolCall)` 提供核对记录的链接，仅允许同源 HTTP(S)
地址，回调得到的是移除秘密字段的副本。`reviewFieldLabel(path)` 可将业务字段
翻译成可读名称，返回 `null` 隐藏版本号等技术元数据。
