# 受管调用账本与事件挂起

`agent.TaskRuntime` 为宿主提供同一套同步/后台执行、持久调用账本、结果回收和事件等待原语。调度步骤由 AI 选择，运行时不识别任何业务接口名称。显式提供Tasks时，控制原语独立于业务工具目录是否为空；TurnRequest.DisableTools仍可关闭所有工具及可选内置，桥接层按用户工具开关设置，不能用空业务目录猜测开关。

## AI 可用原语

| 工具 | 作用 |
| --- | --- |
| `task_start` | 提交已发现工具的冻结参数，立即取得调用句柄；同步接口也可后台执行 |
| `task_budget` | 查询宿主绑定的共享资源剩余额度；快照不代表预留 |
| `task_result` | 按受权句柄和字节偏移读取完整结果片段，不重新调用原工具 |
| `task_event_sources` / `task_event_read` | 发现授权事件源、按持久游标读取外部事实；模型无发布权限 |
| `task_context_save` / `task_context_read` / `task_context_list` / `task_context_delete` | AI维护长期目标、约束、待办及证据引用，按版本CAS更新 |
| `task_trace` / `task_metrics` | 查询脱敏追踪和有界统计，明确分页与观测损失 |
| `task_read` | 查询原调用/等待的状态、授权范围内的原参数和结果；调用与活跃等待分别分页 |
| `task_wait` | 短时间等待任意/全部调用终态；超时不取消工作 |
| `task_suspend` | AI 主动声明句柄条件、任意/全部、期限和事实检查点，结束当前推理；期限也可作为独立计时事件 |
| `task_cancel` | 请求协作取消调用，或取消等待登记以阻止后续唤醒；不撤回已发生业务 |

`task_suspend` 必须是该次模型输出唯一的工具调用，避免挂起和未批准写入在同一步互相覆盖。它仅在宿主明确声明具备唤醒消费者时暴露；`checkpoint` 保存用户目标、范围、待办和证据引用；也可通过 `contexts` 指定长期上下文ID与版本，两者至少一项。不保存隐藏推理或新增授权。

AI 决定直接、同轮并行、后台、短等或挂起，以及事件后使用哪些当前工具。`completed` 只表示 Handler 返回；原回执中的 pending、accepted 和业务后台作业状态仍需按原契约解释。

## 账本与执行边界

`TaskStore` 提供原子创建、作用域查询、版本 CAS、有序分页、等待交付和回收协议。默认 `MemoryTaskStore` 适用于会话内使用/测试；生产宿主注入持久适配器。iClouder 使用原有 MongoDB 连接，未另设凭据或独立业务执行器。

1. 接纳前保存调用键、参数指纹、冻结参数、调用身份和租约。存储不可用时不执行 Handler。
2. 相同作用域、逻辑轮次和调用键只返回原记录。同步与后台调用共用原副作用边界，切换方式不能绕过去重。
3. Handler 开始前 CAS 标记执行，执行期间续租。失联/过期租约的未开始调用转为 cancelled；已开始且可能有副作用的调用转为 unknown，只查询原业务回执，不重建 Go 栈或自动重放。
4. 执行者更新必须匹配租约与版本，旧进程不能覆盖恢复后的终态。
5. 真正返回并保存的结果可跨运行时重建查询。超过模型内联额度的正文返回完整结果引用，执行状态保持原事实；正文传输、保存可用性与执行状态分别报告。

`Scope` 绑定可信用户会话与权限代次，不由模型选择。宿主适配器还须绑定租户/用户/会话命名空间。读取历史账本不代表对资源的当前状态作出新查询。

## 生命周期

默认每运行时最多 8 个 Handler 并发、128 个在途调用，**已完成历史不占执行名额**。原参数最多 64 KiB，每结果内联默认 64 KiB、完整正文默认 16 MiB（最高 256 MiB），调用期限默认 10 分钟、最长 24 小时；这些是资源边界，不是业务编排规则。

结果正文默认保留 24 小时；到期后保留状态、调用键和指纹，并返回 `result_expired`，不重做操作。已知终态的原参数可随正文回收，unknown 的冻结参数留到核对/账本保留期。轻量账本默认保留 30 天。调用记录的最终 TTL 从终态起算，在途记录不设置删除 TTL。等待记录至少保留至触发期限之后的账本保留期。

`Collect` 恢复过期租约并请求存储执行分层保留；宿主可在无客户端在线时维护已登记命名空间。读取也检查正文有效期，不依赖数据库 TTL 准时运行。

`Done` 对本地句柄表示执行器已退出，对远程句柄表示观察到账本终态；取消意图不会使其提前关闭。本地退出不保证最终回执已经持久成功，需要核对持久终态及观察错误时使用带 context 的 `Wait`。`Completed` 是有界本地通知提示，慢消费者不阻塞收尾；可靠唤醒以账本和等待登记为准。`Close` 取消本地工作并等待实际退出，保留持久账本/等待；Handler 必须响应 context。

## 挂起与可靠唤醒

等待条件保存为 `TaskWaitRecord`，独立于 goroutine 和浏览器连接。已结束调用可以晚注册等待；任意/全部终态、期限、取消和登记丢失都有明确事实。记录丢失不能解释为业务未执行。

消费者先 CAS 认领交付租约，获得模型轮次准入后才扣恢复尝试预算。仅因队列/会话忙碌而重投不会消耗模型调用次数。交付失租时旧推理取消，队列仍保留可信事件来源，不能退化为普通用户消息。

恢复进入新的宿主受管轮次：当前身份、权限、可用工具和预算重新核对，后续写入仍须真实确认。恢复请求使用稳定逻辑身份，消费失败仍引用同一等待记录。默认实际恢复尝试最多 3 次；同一用户目标自动恢复链默认最多 32 层，可由宿主配置，不能由模型扩大。预算耗尽公开报告，保留未完成事实。

宿主通过 `TasksProvider` 返回固定存储与 Scope，通过 `TaskWakeGuard` 做实时授权。`TaskWakeAcknowledged` 核对与宿主历史提交同一 CAS/事务保存的处理标记，覆盖“历史已提交、交付 ACK 丢失”的崩溃窗口。`PrepareTurn` 应用 `agent.TaskWakeID(ctx)` 保持稳定执行身份，并将恢复事件作为服务器事实而非新的用户发言；`TurnRequest.WakeAdmitted` 区分真正准入与仅重投。

启用 `BackgroundConfig.RestoreSessions` 后，宿主可从持久登记解析仍获准的会话，服务重启后无需打开页面即可恢复消费者。恢复钩子只创建可信会话，不重发原工具调用。iClouder 登记只包含账户 ID、租户/会话、作用域和维护时间，不保存 API Key。

## 确认与通知

原生批准仍在任务接纳前挂起；签名确认的准备、一次性 nonce、实时权限、原 Handler、持久业务回执和续办沿用原路径。同步批准保持顺序，模型选择后台的批准沿共享执行池并发；全部逐项回执保存后才放行原来的自动续办。

未开始的队列取消/超时释放原卡预留，显式重新批准产生新的调度尝试，不能复用旧 cancelled 句柄。实际业务 invocation 保留，消费过的 nonce 不能再次执行。

后台确认准备可以晚于模型挂起，仍按原组投递固定清单；迟到正文不能改写已结束答复。聊天后台完成通知只发送显式后台执行的句柄和调用状态，不发原参数/结果；同步调用仍记账并触发宿主通用完成钩子，业务是否完成由真实回执决定。


## 结果提交与分段读取

`TaskResultStore` 独立保存不可变最终事实及正文。先完整提交正文/元数据，再补账本；默认独立补写期限30秒，短暂失败只重试同一结果，绝不重放 Handler。已提交的最终结果可在进程重建后修正账本的失租推断。所有存储持续不可用超过补写期限，或进程在首次持久提交前退出，仍只能保留未知边界，不能声称结果已保存。

结果引用含总字节数、SHA256、编码、正文可用性和期限。`task_result` 使用字节offset/next_offset，单次1至65536字节；UTF-8页尾最多延长3字节保持字符完整，非UTF-8返回base64。二进制即使很小也不内联为JSON文本。范围和当前工具权限逐次校验。完整存储额度超限时保存已知执行事实、大小与摘要，availability=size_exceeded，不因正文未保存将成功执行改为unknown。

默认内存账本复用内存结果目录；生产须显式配置持久Results。Mongo适配使用256 KiB不可变块、逐块摘要校验、完整写入后发布元数据；未发布的部分块不构成可见回执。正文到期回收，最终执行事实保留到LedgerExpiresAt。

## 严格停止代次

`TaskControlStore` 为停止代次和共享预算提供通用原子CAS。`BindContext` 在新的明确消息/批准入队前冻结随机代次，出队使用 `UseContext` 核对原代次，不能刷新旧队列资格。固定Scope宿主通过 `WatchContext` 覆盖整轮推理，尚未调用工具或关闭工具的旧轮次同样响应远程停止；结束时释放并等待观察协程。Start在任何持久受理前捕获代次，后台与嵌套调用继承。CancelAll先持久推进代次，再取消已落账的同作用域调用/等待；执行器在Handler前的作用域CAS线性化准入，并在心跳核对代次。因此停止后才完成入库的旧受理无法进入Handler。

恢复事件通过BindWaitContext继承原等待代次，renew/admit也核对代次；它不能当作新用户指令刷新停止资格。没有代次的升级前等待只能查询或由用户明确继续，不自动恢复。已进入执行的Handler仍须协作响应context，停止不承诺撤销已发生影响或强制杀死不协作的外部进程。停止存储写入失败公开报告未确认，不能假装已全局停止。

## 共享预算与背压

TaskConfig通过ControlStore、ControlNamespace、BudgetPool和Budgets绑定可信资源。全局/租户/用户资源在一个有界共享池文档CAS中同时接纳，不会只占部分资源。MaxActive限制在途接纳，MaxConcurrent限制Handler执行，MaxWaiting限制未结束等待登记；每池总预留最多5000，最多8个资源约束。宿主各实例应使用一致的预算策略，运行时不把模型输入作为配额配置，也不提供模型修改额度的工具。

接纳不足返回budget_exhausted、not_started、retry_after_ms；原Handler未执行。已接纳调用在共享并发不足时受管排队，期间不轮询模型。执行租约续期，真正收尾后释放；失联到期可回收。等待预留直到交付/取消/失败才释放，触发期限已过但尚未处理的事件仍计入积压；最晚随账本保留期结束回收。维护可补偿终态已提交、释放预留时断线的窗口，不依据缺失记录抢回仍在入库的接纳。

`task_budget`只展示当前宿主绑定资源的限制和剩余额度；AI自主选择顺序、并发、查询、短等或挂起。预算限制的是受管工具执行和等待资源，不等于模型token计费额度。租约不能强制终止不响应context的Handler；配置中途变更应由宿主协调，而不是多个实例分别采用不同限额。


## 通用外部事件

`TaskEventPublisher` 是可信宿主Go入口，`OpenSource`用稳定键创建作用域内事件源，`Publish`以事件key和内容指纹原子去重。发布工具不交给模型，也不开放无认证webhook。宿主按当前权限绑定namespace/scope，发布已经脱敏、可向该范围展示的事实；接入具体生产者由宿主适配，不在运行时猜业务字段或写固定步骤。

每源持久分配到达顺序Seq；ProducerSequence和OccurredAt保留为来源事实，乱序到达不会被丢弃。sourceID与after游标组成订阅条件，源丢失/过期明确报告不可用，不默默重置游标为零。正文默认24小时、元数据/去重默认30天；正文失效后仍保留事件发生事实。每源最多256条、每事件JSON最多8KiB，每scope最多256源；满额明确拒绝，不覆盖未读事件。模型只读授权源，不能改变生产者、作用域或发布权限。

`task_suspend.events`最多32项，与任务句柄合计最多128项，可按types筛选；all对任务和事件的组合条件生效，期限仍是独立触发事实。事件先到、等待后注册同样可匹配。首次认领后的Reason/EventMatches冻结，交付重投不换成后来另一事件；恢复信封只带SourceID/Seq/State，正文要通过受控工具单独读取。取消、预算和严格停止代次沿用原等待协议。读取事件始终标识为不可信数据，内容不能提升为系统指令或授权。

## 长期工作上下文

`TaskContextStore`独立于聊天压缩，保存AI公开维护的Goal、Constraints、Pending、Evidence、Notes和State。State只是工作声明，不是业务成功回执。保存、读取、分页和删除均受可信scope约束；写入验证停止代次，模型不能选择其他用户或权限范围。

更新必须指定expected_revision；同一可信逻辑轮次+mutation_key幂等，重投返回原应用版本及当前记录，不覆盖后续更新。存储提交应答未知给出admission_state=unknown和可查询ID；只有同一幂等键核对，不靠新写入猜测。删除清正文、留墓碑。默认每scope最多128条、每文档32KiB(最高64KiB)、4096条轻量mutation receipts；目录显示用量和剩余，满额拒绝。默认保留30天，更新延长期限时同步延长该上下文的已有去重回执。Mongo用单个有界bucket CAS完成版本、配额和幂等，不要求跨文档事务。

等待可引用至多16个ID/Revision。认领时记录available/version_changed/deleted/unavailable/unconfirmed的版本事实，重投保留该观察；旧引用不是历史正文快照，恢复后仍应读取当前版本比较。引用已丢失时保留未完成与差异事实，不能把新版本当成原计划或猜测旧目标。运行时不据上下文内容自动执行步骤。

## 结构化错误

`TaskFailure`公开code、execution_state、admission_state、receipt_id、result_ref、retry_after_ms及固定脱敏message。兼容admitted=true也可能表示“可能受理”，应以admission_state的admitted/not_admitted/unknown三态为准。CreateTask/Context提交失去应答不能标作not_admitted，已开始的外层写调用不能因下游资源拒绝被反证成尚未执行。错误不包含原驱动地址、响应原文或凭据；cause只留进程内errors.Is/As。

全部kernel工具和原调用错误使用同一信封；真实挂起interrupt保留原协议，不被包装成普通错误。界面区分未受理、失败和结果未知。结构化错误只提供事实，不授权自动重试或扩大目标。明确存储边界分类storage_unavailable，版本/容量/范围/期限/停止各有稳定code，不按接口名或错误文本猜测。

## 可观测性

`TaskObservationStore`接收白名单关联元数据：trace、调用/父子/等待/恢复/上下文/源/事件ID、状态、稳定code、revision、duration和时间。参数、正文、外部payload、检查点及目标内容均不进入追踪。Invocation/Turn按摘要保存，父子调用与等待恢复继承TraceID，事件/上下文通过引用关联边可追溯。

观测采用128容量有界异步队列，默认每次sink期限100ms、保留30天，失败不改变业务结果、不重放Handler。local_dropped/local_failed/local_pending明确是本运行时指标，会随进程重建重置；它是best effort遥测，不是可靠业务账本或完整审计承诺。关闭限时drain，适配器须响应context。已持久记录可跨重建查询；重复ID只接受同一事实，时间按UTC毫秒规范化以兼容Mongo精度。

`task_trace`分页、`task_metrics`最多汇总1024条快照，next/truncated不能被当作全量统计。时间窗口统一[From,To)，并发写入分页不是完整实时事件流。宿主额外提供本人当前权限代次的只读HTTP观测目录，不接受客户端scope、不返回正文。单独发布者可用PrepareTaskObservation归一化元数据后接同一sink；观测hook失败不改变外部事件的持久结果。

## 持久装配、格式与适配器验收

`StorageMode` 默认 `memory`，不会因为注入一个 Store 就宣称完整持久化。显式 `durable` 要求同时提供 Store、Results、ControlStore、Contexts、Events、Observations，缺失即拒绝启动；可选 Outbox 仍由宿主按实际事务边界接入。`Readiness` / `CheckTaskReadiness` 检查实际装配与适配器健康，不启动维护、模型或业务。durable 表示禁止内存回退，实际介质的持久承诺由被注入的提供者负责。

`agent/mongostore` 提供 TaskStore、ResultStore、ControlStore、ContextStore、EventStore、ObservationStore、OutboxStore，复用 Mongo 原生 CAS、不可变分块和事务。宿主固定连接、集合、Subject/Conversation/Scope；`EnsureIndexes(ctx)` 创建协议索引，保留原数据布局。BeforeCreate 可登记可信维护命名空间；它必须响应 context，不能执行业务。该包不创建新数据库服务、不另读凭据、不识别业务 API。

独立 `storageVersion` 与 CAS Revision、工具 Contract.Version 分开。当前写版本1，读取兼容旧版本0/缺失字段；未来版本拒绝读取、领取与覆盖，主动回收也不修改未来格式。`MigrateStorage(ctx, collections, strict)` 是显式、可重复入口：先停止旧写入者，再升级专用集合的版本；strict 保留既有 validator 并增加版本准入，阻止不带版本的旧二进制重新写入。**未执行严格迁移时不能承诺任意新旧二进制混写安全**。不要将业务共享集合随意交给此迁移入口。Eino 的 session/checkpoint 继续使用其原适配和兼容检查，不建立第二套恢复引擎。

`agent/storetest.Run` 提供 Task/Result/Control/Context/Event 的可复用一致性验收，`RunOutbox` 提供领取、所有者、修订、应答丢失重投的协议验收。Factory 必须使用隔离命名空间；真实业务与 outbox 的事务原子性仍需提供者的集成测试证明。

## 有期限退出、只读核对与工具契约

`Drain(ctx)` 冻结新受理，等待已受理调用及登记真正退出；已在入库的调用也计入屏障，Drain 本身不取消它。`Shutdown(ctx)` 取消本地工作并在期限内等待；返回超时表示仍未退出。`Close` 兼容原无期限真实收尾语义。后台会话提供相同 Shutdown 能力，先取消全部会话，再并行等待收尾。不能强杀忽略 context 的 Go Handler。

`AwaitResult(ctx, scope, id, allowed)` 直接报告存储错误和取消，替代仅依赖无错误 Done channel 的远端等待。Completed 和旧 OnComplete 都是本地提示：回调最多允许一个受阻执行者，繁忙时丢弃提示并在 `Status().CallbackDropped` / `task_budget.runtime` 明确报告，不能用于可靠业务交付；可靠通知使用等待记录/outbox。

`task_reconcile` / `Reconcile` 仅使用当前获准、版本一致的原工具 RecoverResult，传入原可信 invocation key 和冻结参数。它创建只读证据调用，保存独立不可变结果并以 CAS 绑定原记录的 ResolutionRef；原执行状态与原结果不被改写，原 Handler 不再执行。没有可核对回执时保留原未知事实，需人工/AI依据真实证据继续判断，不能强制改为成功。

OutputSchema 由标准 JSON Schema 编译器执行校验，精确支持 json.Number，禁止引用读取网络或宿主文件。非法 schema 在接纳前拒绝；Handler 返回但输出不合约时仍保存原文和 completed 执行事实，并单独标记 contract_violation。ResourceFencing 声明要求真实 ResolveResources；运行时授予 fence 不等于外部提供者已经检查 fence。

## 结果引用、流式读取与可信外部输入

`OpenResultInput` 返回受当前 scope、工具权限和摘要约束的 io.ReadCloser，支持二进制，每次范围至多64KiB；完整读取在 EOF 核验 SHA256，提前关闭由存储适配器保证块摘要。`ResolveResultInput` 的 JSON Pointer 使用标准路径解码和流式 Decoder，只物化选中值；为验证完整 JSON 和摘要仍需读完整正文，单个大标量和选中子树仍占相应内存。Eino ResultBackend 按行读取所需范围并限制输出64KiB，不能用它访问宿主文件。

`ResolveToolInputReferences` 允许宿主显式指定可填充参数前缀，最多16个不重叠指针；不支持表达式或修改路由/权限。不可变引用参加原确认和幂等绑定，解析结果仍须经过原 schema、权限及目标确认。旧 HTTP 工具展开正文限制64KiB；真正的大正文消费者应直接使用流式读取能力。

事件源可 CloseSource、RenewSource 和 ArchiveEventSource，关闭后仍接受同键同事实的重投。续期不复活关闭或过期源。归档先持久设为 archiving 冻结新订阅，再通过共享预留屏障等待已有订阅交付/退出；失败时保留正文和新修订，宿主读取当前修订后可继续归档。归档只清正文，保留序号、键、指纹，直到完整保留期结束。关闭/归档会向等待返回 source_closed 和可选 successor_id，AI自行决定后续。

`AllocateEventSegment` 用共享 CAS 分配源内位置，满段后使用新源；并发预留可留下空洞，不能超填或将原 outbox 换到另一源。每源/作用域存储容量仍明确有界，不新增默认 QPS 或累计调用额度。

可选 `TaskWebhook` 不会自动挂载。宿主固定 Publisher、scope、源和类型并提供 Verify；内置 VerifyTaskWebhookHMAC 将 method、escaped path、时间戳、Idempotency-Key、原正文按换行连接后校验 HMAC；签名覆盖幂等键和入口，不能捕获签名后换键或换入口重放。事件键/指纹复用原子事件存储作为 durable inbox，只有持久成功才返回202，重用键改变事实返回409。密钥由宿主现有凭据配置注入；不接受模型指定 URL、凭据或发布源。

## Outbox 与标准遥测

投递记录保存 Attempts、RetryAt、FailureCode、Dead。暂时失败指数退避，过期、冲突、源关闭/满额等永久阻塞进入死信；失败项离开就绪队列。TaskOutboxFailureStore 记录失败，TaskOutboxAdminStore 提供脱敏分页与按修订显式重投；过期事实不能通过重投延长寿命，重投不能改载荷或重做业务。DeliverTaskOutboxBatch 有界处理多个已提交事实，TaskOutboxGroup 复用同一投递器覆盖多个真实原子边界。

Mongo OutboxStore 支持实际事务 AppendInTransaction，也支持可信 EmbeddedPath 的同文档 AtomicFields。事务方法拒绝非事务 context；嵌入模式要求宿主在 pending 时移除父记录 TTL，ACK 后恢复期限，避免丢弃未交付事实。根模式只对 delivered 记录设 TTL。它们保证本地事实与事件原子提交，**不把 HTTP/远端副作用宣称为本地事务或通用 exactly-once**。

标准 OpenTelemetry 使用宿主 provider，默认 no-op，不私建 exporter。模型、工具、等待认领及 outbox 投递产生 span；事件次数、耗时和供应商实际返回的 token 用量产生指标。等待和 outbox 可携带 W3C traceparent/tracestate 跨实例续接，不保存 baggage、参数或结果正文。遥测与权威账本仍分离；未配置 exporter 不代表线上已具备集中观测。

实现继续复用 Eino ADK/Runner、中断恢复、原生 session/checkpoint、文件/stream 协议、现有 Mongo 与 OTel。顺序、并行、等待、恢复和业务补偿均由 AI 依据事实和授权判断；没有新增 API 名称专用的调度规则。QPS、累计模型/工具/token/费用额度仍默认关闭；宿主显式开启硬费用/token额度时才必须提供可靠报价。
