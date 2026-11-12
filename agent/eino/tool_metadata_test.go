package einoengine

import (
	"testing"

	ai "github.com/zdypro888/ihtml/agent"
)

func TestBuiltinToolMetadataComesFromDefinitions(t *testing.T) {
	tools, err := taskTools(&ai.TurnRequest{AllowTaskSuspend: true})
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{}
	for _, tool := range tools {
		metadata, ok := engine.LookupToolMetadata(tool.Name)
		if !ok || metadata.Name != tool.Name || metadata.Title != tool.Title || ai.PublicToolTitle(tool.Name, tool.Title) == "" {
			t.Fatalf("missing or changed display metadata for %q: %+v", tool.Name, metadata)
		}
	}
	for _, name := range []string{"tool_search", "task_result_file", "skill", "write_todos", "task"} {
		metadata, ok := engine.LookupToolMetadata(name)
		if !ok || metadata.Name != name || ai.PublicToolTitle(name, metadata.Title) == "" {
			t.Fatal(name, metadata)
		}
	}
	if _, ok := engine.LookupToolMetadata("arbitrary_host_tool"); ok {
		t.Fatal("engine must not guess metadata for host tools")
	}
}
