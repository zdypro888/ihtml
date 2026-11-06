# 不依赖 iClouder 的最小任务宿主

运行：

```sh
go run ./examples/task-host
go test ./examples/task-host
```

不需要API凭据、模型或云账号。示例只导入 `ihtml/agent`，实际执行以下能力：

1. 用 `TaskRuntime.Start` 把普通同步Handler放到后台，返回句柄后Handler仍在执行。
2. 用 `PollUntil` 只读探测原结果；宿主返回 `PollResult{Value, Done, Progress}`，内核负责取消与最小探测间隔，不理解业务字段或重试错误。
3. 将任务完成和宿主外部事件组合成 `Suspend` 等待，通过 `ClaimWake/UpdateWake` 认领并确认同一份唤醒事实。
4. 保存完整回执，通过 `ReadResult` 分段读取，即使模型的内联结果窗口较小也不会丢失正文。
5. 取消另一项任务，等待Handler退出，读取取消回执。

示例显式组合这些调用只是为了展示API，**不是**内核替AI决定串行、并行或业务步骤。接入Eino的AI助手已提供 `task_start/read/result/wait/cancel/suspend` 等工具，无须再做一套异步工具。

这里使用内存Store，只演示当前进程内生命周期，不声称进程重启后持久恢复。生产宿主应选择 `TaskStorageDurable`，注入共享的 `agent/mongostore` 存储（账本、结果、控制、上下文、事件、观测及outbox），由内核启动检查拒绝持久能力缺失。宿主负责从真实认证信息绑定scope、租户集合和工具授权；不能接受模型任意选择这些值。

`PollUntil` 的 `Done` 只表示不再等待。业务失败、未知或部分完成的原始回执同样可以成为最终Value；内核不会把这些状态改写为成功。context结束只停止本次等待，不向远端业务发送取消请求。中断后的重试/核对由已有TaskRuntime和回执恢复协议处理，不由轮询助手重放写操作。
