package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// 真实运行独立宿主，确保示例可以执行而非仅是文档片段。
func TestIndependentHostLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var out bytes.Buffer
	if err := run(ctx, &out); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"后台执行", "唤醒原等待", "分段读取", "保留取消回执"} {
		if !strings.Contains(out.String(), text) {
			t.Fatal(out.String())
		}
	}
}
