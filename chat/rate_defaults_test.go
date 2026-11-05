package chat

import (
	"context"
	"strings"
	"testing"
)

// 默认工具写入不因整分钟计数中断长流程；显式正数仍由 TestToolWriteRateLimit 验证。
func TestDefaultWriteRateDoesNotInterruptToolBurst(t *testing.T) {
	rt, _, _ := newTestRuntime(t)
	rt.writeLimiter = newUserWriteLimiter(0)
	set := findTool(t, rt, "ui_theme_set")
	for i := 0; i < 250; i++ {
		text, err := set.InvokableRun(context.Background(), `{"mode":"dark"}`)
		if err != nil || !strings.Contains(text, `"status":"ok"`) {
			t.Fatal(i, text, err)
		}
	}
}
