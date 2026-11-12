package agent

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ToolMetadata 只描述公开展示，不携带 handler、Schema、参数或任何调用权限。
type ToolMetadata struct {
	Name       string
	Title      string
	ResultKind ToolResultKind
}

// ToolResultKind 是可信定义显式选择的公开结果结构；零值不公开原始 Handler 返回值。
type ToolResultKind string

const (
	ToolResultTask     ToolResultKind = "task"
	ToolResultTaskList ToolResultKind = "task_list"
)

// ToolMetadataProvider 允许引擎公开其内部工具的声明式标题；查到标题不意味着该工具本轮可执行。
// 普通宿主工具仍以本轮已授权的 Tool 定义为准，历史补齐也不得据此恢复授权。
type ToolMetadataProvider interface {
	LookupToolMetadata(name string) (ToolMetadata, bool)
}

// PublicToolTitle 校验宿主或引擎声明的展示名称；缺失时返回空，由界面使用本地化通用标题。
// 不把工具名或长篇模型说明猜成用户文案，原技术标识保留在调用详情中。
func PublicToolTitle(name, title string) string {
	title = strings.TrimSpace(title)
	if title == "" || title == strings.TrimSpace(name) || len(title) > 1024 || !utf8.ValidString(title) {
		return ""
	}
	for _, r := range title {
		if unicode.IsControl(r) {
			return ""
		}
	}
	return title
}

// ResolveToolMetadata 优先使用调用方本次已绑定的定义，再查询引擎补充元数据；不会扩大工具集合。
func ResolveToolMetadata(tool Tool, provider any) ToolMetadata {
	metadata := ToolMetadata{Name: tool.Name, Title: PublicToolTitle(tool.Name, tool.Title), ResultKind: tool.ResultKind}
	if source, ok := provider.(ToolMetadataProvider); ok {
		if declared, found := source.LookupToolMetadata(tool.Name); found && declared.Name == tool.Name {
			if metadata.Title == "" {
				metadata.Title = PublicToolTitle(declared.Name, declared.Title)
			}
			if metadata.ResultKind == "" {
				metadata.ResultKind = declared.ResultKind
			}
		}
	}
	return metadata
}
