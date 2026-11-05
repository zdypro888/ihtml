package einoengine

import (
	"encoding/json"
	"strings"

	ai "github.com/zdypro888/ihtml/agent"
)

// 调度指引与工具影响声明共用现有契约；不另造并行工具，也不把提示当作授权或资源锁。
const parallelToolInstruction = `执行方式由你根据用户目标、数据依赖、资源冲突和当前契约判断：可以顺序调用、同轮并行，或使用已提供的后台调用工具先启动再取结果。后台执行也可包装同步接口，不改变原工具的权限、确认、参数和业务完成含义。
参数或结果尚未取得时不猜造依赖；并发额度只是运行资源限制，不代表业务允许并发。需要确认的动作仍须覆盖确切调用，已有副作用或结果未知时核对原记录，不能因为改用同步/后台方式再次执行。
若本轮提供后台调用能力，句柄表示调用已接纳，读取/等待能力提供原 Handler 的真实结果；原结果若是待确认或业务受理，还需依据其契约判断后续。根据当前目标自行选择何时等待、读取或请求取消，不把示例接口的调用顺序当作固定流程。`

// modelToolDescription 同时服务立即工具与 tool_search 延迟工具，避免发现阶段丢失调度语义。
// 只添加执行约定，保留宿主原说明及更具体的依赖限制；不改参数、权限或 Handler。
func modelToolDescription(t ai.Tool) string {
	parts := []string{strings.TrimSpace(t.Description)}
	switch t.Effect {
	case ai.ToolEffectRead:
		parts = append(parts, "执行约定：只读查询。输入已确定且互不依赖时，可与其他独立查询在同一轮并行；依赖返回值或分页游标时须等待前一步。")
	case ai.ToolEffectWrite, ai.ToolEffectExecute:
		parts = append(parts, "执行约定：会产生外部影响。只有逐项满足授权且资源互不冲突、没有先后依赖时才可并行；同一资源的修改按序执行。")
	default:
		parts = append(parts, "执行约定：外部影响未声明，按工具具体说明处理，不能假定是只读或可安全并发。")
	}
	if t.ApprovalRequired {
		parts = append(parts, "此调用需要明确确认；可以纳入展示了确切调用与参数的固定批准清单，不能借用未覆盖本调用的确认。")
	}
	if t.Completion == ai.ToolCompletionAsynchronous {
		parts = append(parts, "成功返回表示任务已持久化并被异步受理，不表示任务已经执行完成；后续进度和最终结果以任务状态为准。沿原任务查询或等待，依赖结果的步骤须等真实完成；不要重复提交。")
	}
	if contract, err := json.Marshal(t.Contract); err == nil && string(contract) != "{}" {
		parts = append(parts, "机器能力契约（未声明不代表支持）："+string(contract))
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
