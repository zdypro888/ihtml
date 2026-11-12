package assistant

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zdypro888/ihtml/agent"
)

func TestTaskResultViewUsesDeclaredShapeAndRedactsPrivateBodies(t *testing.T) {
	raw := `{"tasks":[{"id":"one","tool":"arbitrary_business","title":"读取文件索引","state":"running","arguments":{"secret":"PRIVATE_ARGS"},"result":"PRIVATE_RESULT","error":"PRIVATE_ERROR","failure":{"message":"PRIVATE_FAILURE"},"progress":{"message":"已读取一批索引","state":"running","updated_at":"2026-11-01T00:00:00Z"}},{"id":"two","tool":"other","title":"查询设置","state":"completed"}]}`
	metadata := agent.ToolMetadata{Name: "arbitrary_task_reader", Title: "读取后台任务状态", ResultKind: agent.ToolResultTaskList}
	view, ok := taskResultView(metadata, raw)
	if !ok || !strings.Contains(view.Summary, "执行中 1 项") || !strings.Contains(view.Summary, "已返回 1 项") || strings.Contains(string(view.Result)+view.Summary, "PRIVATE") || view.Status != "" {
		t.Fatal(view, ok)
	}
	if !strings.Contains(string(view.Result), "读取文件索引") || !strings.Contains(string(view.Result), "2026-11-01T00:00:00Z") || !strings.Contains(string(view.Result), "arbitrary_business") {
		t.Fatal(string(view.Result))
	}
	metadata.ResultKind = ""
	if _, ok := taskResultView(metadata, raw); ok {
		t.Fatal("shape inferred from body/name")
	}
}

func TestTaskResultViewBoundsDetailsWithoutInventingAggregateProgress(t *testing.T) {
	rows := []map[string]any{}
	for range 128 {
		rows = append(rows, map[string]any{"id": "one", "tool": "reader", "state": "running", "progress": map[string]any{"message": "本项范围已读取", "counters": []any{map[string]any{"label": "文件", "current": 7}}}})
	}
	raw, _ := json.Marshal(map[string]any{"tasks": rows})
	view, ok := taskResultView(agent.ToolMetadata{ResultKind: agent.ToolResultTaskList}, string(raw))
	var details struct {
		Tasks        []publicTaskView
		Total, Shown int
		More         bool
	}
	if !ok || json.Unmarshal(view.Result, &details) != nil || details.Total != 128 || details.Shown != 8 || !details.More || len(details.Tasks) != 8 || strings.Contains(view.Summary, "896") || len(view.Result) > 32768 {
		t.Fatal(view, string(view.Result))
	}
}
