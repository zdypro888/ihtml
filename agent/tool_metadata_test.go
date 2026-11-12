package agent

import "testing"

type metadataFixture struct{ metadata ToolMetadata }

func (m metadataFixture) LookupToolMetadata(string) (ToolMetadata, bool) { return m.metadata, true }

func TestToolMetadataNeverInfersTechnicalTitleOrPermission(t *testing.T) {
	tool := Tool{Name: "private_operation", Title: "查询公开记录"}
	provider := metadataFixture{ToolMetadata{Name: tool.Name, Title: "读取后台任务状态"}}
	if got := ResolveToolMetadata(tool, provider); got.Title != tool.Title || got.Name != tool.Name {
		t.Fatal(got)
	}
	tool.Title = ""
	if got := ResolveToolMetadata(tool, provider); got.Title != provider.metadata.Title {
		t.Fatal(got)
	}
	provider.metadata.Name = "different_tool"
	if got := ResolveToolMetadata(tool, provider); got.Title != "" || got.Name != tool.Name {
		t.Fatal("another tool's title must not label this invocation", got)
	}
	for _, title := range []string{"", "private_operation", "  private_operation  ", "unsafe\nlabel", "unsafe\x00label"} {
		if got := PublicToolTitle(tool.Name, title); got != "" {
			t.Fatal(title, got)
		}
	}
}
