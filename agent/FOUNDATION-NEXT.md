# 通用执行基础能力与复用边界

能力属于运行时和存储协议；AI 决定发现、顺序、并行、后台、等待和依赖，不按工具名、URL 或业务字段编写编排规则。未声明的工具语义保持未知，不能据此授予权限或自动重放。

## 复用结论

| 能力 | 复用实现 | 本项目适配边界 |
| --- | --- | --- |
| 模型与工具编排 | Eino v0.10.0-alpha.12 Runner/session、compose interrupt/resume、ToolSearch、Summarization | 原权限、确认和持久调用账本 |
| 大结果文件读取 | Eino filesystem.Backend 与原生文件中间件 | 只读适配现有 TaskResultStore；不开磁盘、shell、写文件、目录枚举 |
| 结构化选择 | go-openapi/jsonpointer v1.0.0 | 先做作用域、源工具权限、SHA256、保留期校验；JSON UseNumber 保留整数精度 |
| 速率算法 | golang.org/x/time/rate | 现有 TaskControlStore 的共享 CAS 持久化桶状态、策略、冷却和租约 |
| 数据库原子提交 | 宿主 Mongo 原生 Session/事务 | 与原业务使用同一事务，投递器只发布已提交事实 |

Eino 原生 backgroundtask.Manager 管理内存生命周期，不能替代当前跨实例账本和停止代次，因此不并排注册第二套任务 ID、停止工具或执行器。plantask/automemory 可用于 AI 规划/记忆，但不替代现有带 CAS 的工作上下文或权限权威。原生 reduction 的 offload/clear 是对话压缩策略，不能替换不可变业务回执，也不额外建立第二个正文存储。

Watermill Forwarder 的事务 outbox 以 SQL 适配为主；宿主当前为 Mongo，使用原生 Session 更贴合已有事务。没有为了这些能力新增 Redis、SQL、Temporal 服务或消息代理。

## 已实现

- [x] ToolContract：版本、幂等范围、取消约定、输出 schema、引用输入和资源 fencing 声明；同时进入模型工具说明、会话指纹与显式契约调用指纹。零契约保留原调用指纹兼容既有账本。
- [x] TaskResultInput / ReadTaskResultInput：引用原样进入原参数确认和身份计算，显式支持的 Handler 在当前受控上下文按需读取。没有隐式替换任意 JSON 字段、展开大参数或跳过目标鉴权。
- [x] Eino 原生 `task_result_file`：路径 `/results/<task_id>/<sha256>`，行范围读取、输出 64KiB 上限。更大内容继续用现有 `task_result` 字节分页。
- [x] TaskGoalLimits：模型调用、工具调用、输入/输出 token、费用微单位及总时间；可信 InvocationScope 持久关联账户，跨 checkpoint、挂起恢复、显式批准和确认后总结沿用原额度。新明确指令取得新账户，AI context ID 不能重置预算。
- [x] TaskResourcePolicy：跨实例令牌桶、可信提供者冷却、先到且当前可运行的相交资源公平排队；容量和速率策略有持久单调版本，旧实例的新接纳拒绝降级。已有在途调用不因调整额度被猜成未执行。
- [x] ResolveResources / TaskResourceGrants：共享/独占资源租约、单调 fence、IfVersion 传递，租约随调用续期、结束释放，不解释业务字段。
- [x] TaskOutboxStore / DeliverTaskOutbox：稳定事件键、投递租约、Owner/Revision ACK、失败重投事实。宿主 Mongo 实现强制使用正在运行的原生事务，发布成功但 ACK 丢失不会重新执行业务。
- [x] 本地内存、跨实例 Mongo、Eino 模型边界、故障恢复和 race 验收；Mongo 副本集验证真实原子提交及回滚。

## 宿主使用约束

频率限流和累计调用/token/费用额度默认关闭；只在宿主显式提供正数时启用。连接/并发/存储容量和单次执行期限是独立的运行边界，仍然保留。

预算、资源键、策略版本和价格来自可信宿主，不是 AI 可修改的参数。`task_budget` 返回当前账户预留以及容量、限流、冷却的只读快照，不构成预留。

费用与 token 上限采用保守预留：Eino Config.QuoteModel 必须提供真实上界，包括供应商隐藏输入、输出设置、缓存计费和客户端重试。未提供可靠报价而配置了 token/费用硬额度时，拒绝模型调用；不把 Eino 默认字符估算当成账单上界。超时、无 usage 或进程退出不退款，避免不确定费用反复透支。模型调用次数是运行时模型接口调用次数，不冒充供应商传输重试次数。

工具实际执行前扣一次额度；通用调度/上下文等控制调用也计费，预算查询和取消保留可用。模型或工具因额度拒绝，不表示原业务失败；已提交结果继续保留。目标到期会取消整轮上下文并约束新调用期限，不能强行终止忽略 context 的外部执行者。

`ResultInputs=true` 的工具在自己的 InputSchema 中复用 `TaskResultInputSchema()`，在 Handler 内调用 `ReadTaskResultInput(ctx, ref)`。原参数中的确切引用参与权限、确认、去重，不允许引用工具偷偷扩大目标。已过期/跨作用域/撤权/摘要不符均拒绝读取。

资源键必须包含正确的租户/提供者资源身份。协调租约只约束使用同一存储协议的执行者；真正阻止失租者陈旧写入，提供者必须校验 `TaskResourceGrants(ctx)` 中的 fence 和 IfVersion。声明 ResourceFencing 不会让不支持条件写入的远端接口突然具备该能力。运行时不替业务实现幂等，不自动重试 Handler。

策略改动必须提高 BudgetPolicyVersion（容量）或 TaskResourcePolicy.Version（速率）。同版本不同配置拒绝接纳。关闭已有共享限流时递增 version，并将 rate_per_second、burst 同设为 0；保留关闭版本并清除旧桶和冷却，防止旧实例复活限流。新资源默认不配置限流。新速率策略从空桶开始，避免重新部署反复领取 burst。共享池上限 5000 租约、每类最多 4096 个持久策略资源，协调文档总序列化上限 8MiB；容量不足显式拒绝，不静默删除策略或未交付事实。

outbox 需要业务在实际提交边界显式接入。宿主用 `NewTaskOutboxEntry` 在事务重试循环外固定事实，再在原业务 Mongo 事务中调用 AppendInTransaction。普通 HTTP 返回、远端 API 或单独写一次 outbox 不构成原子提交。未支持事务的 Mongo 部署明确拒绝该写入；不会自动包裹所有旧接口、重放业务或引入隐式事务。

outbox ACK 前保留正文；未交付且过期的记录保留供核对，不通过 TTL 删除。只有已交付记录可在保留期后清理。事件源已满/过期或发布不可用时保留待处理事实。现有 TaskEventPublisher 的源内持久指纹负责去重，不能通过覆盖旧事件腾空间。

## 验证

- `go test ./... -timeout 120s`
- `go test -race ./agent ./agent/eino ./assistant -timeout 120s`
- 宿主：`TestAssistant*`（含 Mongo 持久恢复/停止/权限与旧功能回归）
- `TestAssistantMongoOutboxAtomicCommitAndRollback` 在隔离临时副本集真实执行；单机测试自动跳过此项，不降低事务约束。
- 业务只使用合成 fixture，没有调用真实账户或外部业务接口。

## 官方资料

- https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_hitl/
- https://www.cloudwego.io/docs/eino/core_modules/eino_adk/eino_adk_chatmodelagentmiddleware/middleware_filesystem/
- https://www.cloudwego.io/docs/eino/core_modules/eino_adk/eino_adk_chatmodelagentmiddleware/middleware_toolreduction/
- https://github.com/cloudwego/eino/discussions/1159
- https://go.dev/wiki/RateLimiting
- https://github.com/go-openapi/jsonpointer
- https://watermill.io/advanced/forwarder/
- https://www.mongodb.com/docs/manual/core/transactions/
