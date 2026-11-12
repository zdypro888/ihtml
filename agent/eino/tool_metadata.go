package einoengine

import (
	"sync"

	ai "github.com/zdypro888/ihtml/agent"
)

// 引擎适配器为框架注入的工具声明展示元数据，与 SDK 自有工具定义一并对外查询。
// 这里只保存名称和标题，不注册 handler，也不把当前未开放的能力变成可调用工具。
var builtinToolMetadata = sync.OnceValue(func() map[string]ai.ToolMetadata {
	metadata := map[string]ai.ToolMetadata{}
	tools, _ := taskTools(&ai.TurnRequest{AllowTaskSuspend: true})
	for _, tool := range tools {
		metadata[tool.Name] = ai.ToolMetadata{Name: tool.Name, Title: tool.Title, ResultKind: tool.ResultKind}
	}
	for _, item := range []ai.ToolMetadata{
		{Name: toolSearchToolName, Title: "查找可用工具"},
		{Name: "task_result_file", Title: "读取已保存的任务结果"},
		{Name: "skill", Title: "读取操作指引"},
		{Name: "write_todos", Title: "更新任务计划"},
		{Name: "task", Title: "委派子任务"},
	} {
		metadata[item.Name] = item
	}
	return metadata
})

// LookupToolMetadata 支持实时执行和旧历史读取共用同一份工具标题；不推断任务状态或业务结果。
func (e *Engine) LookupToolMetadata(name string) (ai.ToolMetadata, bool) {
	metadata, ok := builtinToolMetadata()[name]
	return metadata, ok
}

var _ ai.ToolMetadataProvider = (*Engine)(nil)
