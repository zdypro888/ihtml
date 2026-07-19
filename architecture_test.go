package ihtml_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCoreDoesNotLinkEinoRuntime(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("list core dependencies: %v", err)
	}
	for _, dependency := range strings.Fields(string(output)) {
		if strings.HasPrefix(dependency, "github.com/cloudwego/eino") ||
			dependency == "github.com/zdypro888/ihtml/chat" ||
			strings.HasPrefix(dependency, "github.com/zdypro888/ihtml/chat/") {
			t.Fatalf("ihtml core must remain AI-runtime neutral; linked %q", dependency)
		}
	}
}
