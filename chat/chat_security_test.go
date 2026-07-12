package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSelectTurnModelIsServerAuthoritativeWhenSelectorDisabled(t *testing.T) {
	cfg := Config{Model: "server-model"}
	selected, err := selectTurnModel(cfg, "server-model")
	if err != nil || selected.Model != "server-model" {
		t.Fatalf("matching UI echo rejected: model=%q err=%v", selected.Model, err)
	}
	selected, err = selectTurnModel(cfg, "attacker-model")
	if err == nil || selected.Model != "server-model" {
		t.Fatalf("client overrode hidden model: model=%q err=%v", selected.Model, err)
	}
	selected, err = selectTurnModel(Config{}, "attacker-model")
	if err != nil || selected.Model != "" {
		t.Fatalf("client supplied missing server default: model=%q err=%v", selected.Model, err)
	}
}

func TestSelectTurnModelAllowsExplicitOptIn(t *testing.T) {
	selected, err := selectTurnModel(Config{Model: "default", AllowModelSelect: true}, "chosen")
	if err != nil || selected.Model != "chosen" {
		t.Fatalf("explicit model selection failed: model=%q err=%v", selected.Model, err)
	}
}

func TestUserFacingErrorHidesRuntimeAndNetworkInternals(t *testing.T) {
	raw := errors.New(`[NodeRunError] create new streaming message fail: Post "http://127.0.0.1:9/v1/messages": dial tcp 127.0.0.1:9: connect: connection refused
------------------------ node path: [node_1, ChatModel]`)
	got := userFacingError(raw)
	if got != "无法连接模型服务，请检查服务端配置或稍后重试。" {
		t.Fatalf("network error = %q", got)
	}
	for _, secret := range []string{"127.0.0.1", "node path", "NodeRunError"} {
		if strings.Contains(got, secret) {
			t.Fatalf("network diagnostic leaked %q in %q", secret, got)
		}
	}
	if got := userFacingError(context.DeadlineExceeded); got != "模型响应超时，请稍后重试。" {
		t.Fatalf("deadline error = %q", got)
	}
	if got := userFacingError(errors.New("Deep 模式需要启用工具")); got != "Deep 模式需要启用工具" {
		t.Fatalf("safe validation error changed = %q", got)
	}
	if got := userFacingError(errors.New(`create new streaming message fail: 400 Bad Request {"error":{"type":"invalid_request_error","message":"模型不存在"}}`)); got != "模型不存在或当前账号无权使用，请检查模型名称。" {
		t.Fatalf("missing model error = %q", got)
	}
	if got := userFacingError(errors.New(`create new streaming message fail: 400 Bad Request {"error":{"type":"invalid_request_error","message":"bad tools"}}`)); got != "模型服务拒绝了请求，请检查模型名称与参数配置。" {
		t.Fatalf("invalid request error = %q", got)
	}
}
