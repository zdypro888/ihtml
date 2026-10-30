package einoengine

import (
	"strings"

	ai "github.com/zdypro888/ihtml/agent"
)

// 调度指引与工具影响声明共用现有契约；不另造并行工具，也不把提示当作授权或资源锁。
const parallelToolInstruction = `工具调度：先按依赖关系拆分下一步。参数已知、互不依赖的查询，在同一轮发出多个工具调用并行执行；仅当需要前一步返回的标识、游标、版本、预览指纹或完成结果时，才等待后再进入下一轮。只读不等于没有依赖，不能猜造缺失参数来凑并行。
工具说明中的“执行约定”来自宿主的只读/写入、确认和异步声明。写入须逐项满足授权，操作相同资源或存在顺序依赖时串行；声明不明时不能假定并发安全，遵守工具的具体约束。
已有批量接口时优先提交一份确切范围，由业务服务调度内部并发，不把一个逻辑作业拆成多份重复提交。后台作业受理不等于完成：先完成其他独立查询，再等待原任务；同一个原任务只保留一个等待调用。依赖该作业结果的步骤必须等真实完成，不以并行调用重复创建、重试或扩大范围。`

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
		parts = append(parts, "此调用需要独立确认，不能借用同轮其他调用的确认。")
	}
	if t.Completion == ai.ToolCompletionAsynchronous {
		parts = append(parts, "成功返回表示任务已持久化并被异步受理，不表示任务已经执行完成；后续进度和最终结果以任务状态为准。沿原任务查询或等待，依赖结果的步骤须等真实完成；不要重复提交。")
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
