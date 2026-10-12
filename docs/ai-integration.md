# AI 接入与扩展

## 选择唯一的 AI 所有者

| 项目形态 | 接入方式 | 谁负责模型循环与业务治理 |
|---|---|---|
| 已有自己的 Agent/Orchestrator | 实现 `ihtml.ChatBackend` | 宿主，ihtml 只负责 UI/协议 |
| 已有 `agent.Engine` 或采用共享 Eino | `assistant.NewChecked` | Engine 负责执行，宿主 hooks 负责权限/历史/审计 |
| 没有 AI 中枢，主要操作动态页面 | `chat.NewChecked` | 内置 UI 领域 Agent、确认卡、预算和历史治理 |

不要同时启动两套 Agent 处理同一轮。核心 `ihtml` 不导入 Eino，AI 完全可选。
通用包不依赖 nbco 的人员、Worker、审批数据库或业务工具。

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
桥接层不实现确认卡/浏览器检查/中断恢复；需要这些能力时使用内置 chat 后端，
或像 nbco 一样实现完整 ChatBackend。它不会对外投影原始工具参数/结果或隐藏推理。

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
确定性 Engine。示例源码展示真实 Engine 和桥接层的组合，生产必须更换开发鉴权、
增加规范历史存储、权限校验、执行超时及审计。预览不声称完成任何业务操作。

回归套件：`go test -race ./...`、`node --test web/*.test.js`、`go vet ./...`。
前端测试首次运行先执行 `npm ci` 和 `npx playwright install chromium`。
`agent/eino` 包保留原生工具多轮、Responses 回传/取消/预算测试；assistant 包验证
队列、取消、投影、权限与生命周期；前端验证嵌入、隔离和异步扩展。
