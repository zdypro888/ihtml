package einoengine

import (
	"strings"
	"testing"
	"time"

	ai "github.com/zdypro888/ihtml/agent"
	"github.com/zdypro888/ihtml/progress"
)

func TestReadTaskProgressPreservesSourceTimeAndScope(t *testing.T) {
	at := time.Date(2026, 11, 12, 16, 9, 0, 0, time.UTC)
	total := int64(10)
	snapshot := progress.Snapshot{Message: "正在核对关联资料", Phase: "核对", State: "running", UpdatedAt: at, Counters: []progress.Counter{{Label: "已核对", Current: 3, Total: &total}}}
	var events []ai.Step
	req := &ai.TurnRequest{Tools: []ai.Tool{{Name: "arbitrary_read", Title: "读取资料"}}, OnEvent: func(s ai.Step) { events = append(events, s) }}
	reportReadTaskProgress(req, []ai.TaskInfo{{Tool: "arbitrary_read", State: "running", Progress: &snapshot}}, "本次等待")
	if len(events) != 1 || events[0].Progress == nil || !events[0].Progress.UpdatedAt.Equal(at) || events[0].Progress.Counters[0].Current != 3 || !strings.Contains(events[0].Result, "读取资料") {
		t.Fatal(events)
	}
	if snapshot.Message != "正在核对关联资料" {
		t.Fatal("mutated saved source")
	}
	reportReadTaskProgress(req, []ai.TaskInfo{{Tool: "arbitrary_read", State: "running", Progress: &snapshot}, {Tool: "another", State: "completed", Progress: &snapshot}}, "本次查询")
	if len(events) != 2 || len(events[1].Progress.Counters) != 0 || !events[1].Progress.UpdatedAt.Equal(at) || !strings.Contains(events[1].Result, "本次查询 2") || !strings.Contains(events[1].Result, "不代表业务完成") {
		t.Fatal(events)
	}
	reportReadTaskProgress(req, []ai.TaskInfo{{Tool: "arbitrary_read"}}, "本次等待")
	if len(events) != 2 {
		t.Fatal("invented progress for missing snapshot")
	}
	for _, state := range []string{"completed", "failed", "unknown", "cancelled"} {
		reportReadTaskProgress(req, []ai.TaskInfo{{Tool: "arbitrary_read", State: state, Progress: &snapshot}}, "本次等待")
	}
	reportReadTaskProgress(req, []ai.TaskInfo{{Tool: "a", State: "completed", Progress: &snapshot}, {Tool: "b", State: "failed", Progress: &snapshot}}, "本次等待")
	if len(events) != 2 {
		t.Fatal("terminal task replayed stale running progress", events)
	}
}
