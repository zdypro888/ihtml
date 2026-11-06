// task-host 演示独立宿主只依赖 ihtml 内核，不需要 iClouder、模型或云账号。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/zdypro888/ihtml/agent"
)

// run 展示宿主主动组合执行原语；示例步骤不是内核自动施加给AI的业务规则。
func run(ctx context.Context, out io.Writer) error {
	const scope = "minimal-host"
	ledger, results := agent.NewMemoryTaskStore(), agent.NewMemoryTaskResultStore()
	cfg := agent.TaskConfig{Scope: scope, Store: ledger, Results: results, MaxResultBytes: 16, PollInterval: time.Millisecond}
	runtime, err := agent.NewTaskRuntime(ctx, cfg)
	if err != nil {
		return err
	}
	defer runtime.Close()
	started, release := make(chan struct{}), make(chan struct{})
	tool := agent.Tool{Name: "local_report", Effect: agent.ToolEffectRead, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
		close(started)
		select {
		case <-release:
			// 示例探针只读本地状态，真实宿主在此读取自己的API并解释完成条件。
			reads := 0
			return agent.PollUntil(ctx, time.Millisecond, func(context.Context) (agent.PollResult[string], error) {
				reads++
				return agent.PollResult[string]{Value: strings.Repeat("report;", 64), Done: reads >= 2, Progress: "宿主提供的进度"}, nil
			}, nil)
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
	task, err := runtime.Start(ctx, scope, "turn", "original-call", tool, json.RawMessage(`{}`))
	if err != nil {
		return err
	}
	select {
	case <-started:
	case <-ctx.Done():
		return ctx.Err()
	}
	fmt.Fprintln(out, "同步Handler已在后台执行，Start无需等待Handler返回")
	publisher := runtime.EventPublisher()
	source, err := publisher.OpenSource(ctx, scope, "local-events", "宿主事件")
	if err != nil {
		return err
	}
	wait, err := runtime.Suspend(ctx, scope, "turn", "original-wait", agent.TaskWaitSpec{IDs: []string{task.ID}, Events: []agent.TaskEventCondition{{SourceID: source.ID, Types: []string{"ready"}}}, All: true, Timeout: time.Minute, Checkpoint: "读取原回执后再判断下一步"}, map[string]bool{tool.Name: true})
	if err != nil {
		return err
	}
	if _, err = publisher.Publish(ctx, scope, source.ID, agent.TaskExternalEvent{Key: "original-event", Type: "ready", Payload: json.RawMessage(`{"ready":true}`)}); err != nil {
		return err
	}
	close(release)
	rows, err := runtime.Wait(ctx, scope, []string{task.ID}, map[string]bool{tool.Name: true}, time.Second, true)
	if err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].State != "completed" {
		return fmt.Errorf("call did not complete")
	}
	wake, err := runtime.ClaimWake(ctx, scope, "host-consumer")
	if err != nil {
		return err
	}
	if wake == nil || wake.ID != wait.ID || len(wake.EventMatches) != 1 {
		return fmt.Errorf("missing original wake facts")
	}
	if err = runtime.UpdateWake(ctx, scope, wake.ID, "host-consumer", "ack"); err != nil {
		return err
	}
	fmt.Fprintln(out, "任务与外部事件共同唤醒原等待，已确认交付")
	var body strings.Builder
	for offset := int64(0); ; {
		page, e := runtime.ReadResult(ctx, scope, task.ID, offset, 32, map[string]bool{tool.Name: true})
		if e != nil {
			return e
		}
		body.WriteString(page.Text)
		if page.EOF {
			break
		}
		offset = page.NextOffset
	}
	if body.String() != strings.Repeat("report;", 64) {
		return fmt.Errorf("result pages changed the receipt")
	}
	fmt.Fprintln(out, "完整回执独立保存，分段读取内容一致")
	cancelStarted := make(chan struct{})
	cancelTool := agent.Tool{Name: "local_wait", Effect: agent.ToolEffectRead, Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
		close(cancelStarted)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	cancelTask, err := runtime.Start(ctx, scope, "turn-2", "cancel-call", cancelTool, json.RawMessage(`{}`))
	if err != nil {
		return err
	}
	select {
	case <-cancelStarted:
	case <-ctx.Done():
		return ctx.Err()
	}
	if _, err = runtime.Cancel(scope, cancelTask.ID, map[string]bool{cancelTool.Name: true}); err != nil {
		return err
	}
	rows, err = runtime.Wait(ctx, scope, []string{cancelTask.ID}, map[string]bool{cancelTool.Name: true}, time.Second, true)
	if err != nil {
		return err
	}
	if rows[0].State != "cancelled" {
		return fmt.Errorf("cancelled handler did not unwind: %s", rows[0].State)
	}
	fmt.Fprintln(out, "取消后等待Handler退出，保留取消回执")
	return nil
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := run(ctx, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
