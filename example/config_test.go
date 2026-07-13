package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zdypro888/ihtml/chat"
)

const testSecretSentinel = "config-test-secret-sentinel"

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ihtml.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func minimalClaudeConfig(authJSON string) string {
	return fmt.Sprintf(`{
  "version": 1,
  "server": {},
  "ai": {
    "schema_version": 1,
    "enabled": true,
    "provider": "claude",
    "base_url": "https://gateway.example/api/anthropic",
    "model": "glm-test",
    "auth": %s,
    "generation": {},
    "agent": {},
    "safety": {},
    "history": {},
    "system_prompt": ""
  }
}`, authJSON)
}

func TestFileConfigMapsStrictClaudeDefaults(t *testing.T) {
	t.Setenv("TEST_IHTML_AUTH_TOKEN", testSecretSentinel)
	t.Setenv("IHTML_AI_MODEL", "ambient-must-not-override")
	path := writeConfigFile(t, minimalClaudeConfig(`{"type":"auth_token","env":"TEST_IHTML_AUTH_TOKEN"}`))

	fileConfig, err := loadExampleFileConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, enabled, err := fileConfig.chatConfig("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled || cfg.Provider != chat.ProviderClaude || cfg.BaseURL != "https://gateway.example/api/anthropic" || cfg.Model != "glm-test" {
		t.Fatalf("resolved provider/base/model mismatch: enabled=%v provider=%q base=%q model=%q", enabled, cfg.Provider, cfg.BaseURL, cfg.Model)
	}
	if cfg.AuthToken != testSecretSentinel || cfg.APIKey != "" {
		t.Fatal("configured auth source did not map exclusively to AuthToken")
	}
	if !cfg.Stream || cfg.MaxTokens != 4096 || cfg.Thinking || cfg.ThinkingBudgetTokens != 0 {
		t.Fatalf("generation defaults = stream:%v max:%d thinking:%v/%d", cfg.Stream, cfg.MaxTokens, cfg.Thinking, cfg.ThinkingBudgetTokens)
	}
	if cfg.EnableDeepAgent || cfg.AllowModelSelect || cfg.MaxIterations != 16 || cfg.MaxModelCallsPerTurn != 24 || cfg.MaxSubtasksPerTurn != 3 {
		t.Fatalf("agent defaults = deep:%v select:%v iterations:%d calls:%d subtasks:%d", cfg.EnableDeepAgent, cfg.AllowModelSelect, cfg.MaxIterations, cfg.MaxModelCallsPerTurn, cfg.MaxSubtasksPerTurn)
	}
	if cfg.RequestTimeout != 3*time.Minute || cfg.MaxTurnDuration != 2*time.Minute || !cfg.ConfirmWrites || cfg.WritesPerMinute != 240 {
		t.Fatalf("runtime defaults = timeout:%s turn:%s confirm:%v writes:%d", cfg.RequestTimeout, cfg.MaxTurnDuration, cfg.ConfirmWrites, cfg.WritesPerMinute)
	}
	if cfg.MaxHistorySessions != 128 || cfg.MaxHistoryEvents != 256 || cfg.MaxHistoryBytes != 2<<20 || cfg.HistoryTTL != 12*time.Hour {
		t.Fatalf("history defaults = sessions:%d events:%d bytes:%d ttl:%s", cfg.MaxHistorySessions, cfg.MaxHistoryEvents, cfg.MaxHistoryBytes, cfg.HistoryTTL)
	}
}

func TestCheckedExampleConfigFileLoads(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", testSecretSentinel)
	fileConfig, err := loadExampleFileConfig("ihtml.example.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg, enabled, err := fileConfig.chatConfig("", nil)
	if err != nil || !enabled {
		t.Fatalf("sample config: enabled=%v err=%v", enabled, err)
	}
	if cfg.Provider != chat.ProviderClaude || cfg.Model != "glm-5.2" || !cfg.Thinking || !cfg.EnableDeepAgent {
		t.Fatalf("sample capabilities = provider:%q model:%q thinking:%v deep:%v", cfg.Provider, cfg.Model, cfg.Thinking, cfg.EnableDeepAgent)
	}
	if cfg.MaxOutstandingHostCallbacks != 32 || cfg.MaxOutstandingObservations != 32 || cfg.MaxOutstandingOperations != 64 || cfg.SessionNamespace != "example-app" {
		t.Fatalf("sample runtime bounds/scope = %d/%d/%d namespace:%q", cfg.MaxOutstandingHostCallbacks, cfg.MaxOutstandingObservations, cfg.MaxOutstandingOperations, cfg.SessionNamespace)
	}
}

func TestConfigDecoderRejectsAmbiguityAndUnknownFields(t *testing.T) {
	tests := map[string]string{
		"unknown field":   `{"version":1,"unknown":true}`,
		"duplicate key":   `{"version":1,"version":1}`,
		"trailing json":   `{"version":1} {"version":1}`,
		"wrong version":   `{"version":2}`,
		"top level array": `[]`,
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := loadExampleFileConfig(writeConfigFile(t, content)); err == nil {
				t.Fatal("invalid config was accepted")
			}
		})
	}
	oversized := `{"version":1,"ai":{"system_prompt":"` + strings.Repeat("x", maxExampleConfigBytes) + `"}}`
	if _, err := loadExampleFileConfig(writeConfigFile(t, oversized)); err == nil {
		t.Fatal("oversized config was accepted")
	}
}

func TestConfigAuthFileIsExplicitPrivateAndNeverLeaked(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "token")
	if err := os.WriteFile(secretPath, []byte(testSecretSentinel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := writeConfigFile(t, minimalClaudeConfig(fmt.Sprintf(`{"type":"auth_token","file":%q}`, secretPath)))
	fileConfig, err := loadExampleFileConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := fileConfig.chatConfig("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthToken != testSecretSentinel {
		t.Fatal("secret file was not resolved")
	}

	badCases := []string{
		`{"type":"auth_token","env":"TEST_IHTML_AUTH_TOKEN","file":"/tmp/token"}`,
		`{"type":"auth_token"}`,
		`{"type":"auth_token","file":"relative-token"}`,
		`{"type":"auth_token","env":"bad-name"}`,
	}
	for _, authJSON := range badCases {
		path := writeConfigFile(t, minimalClaudeConfig(authJSON))
		loaded, loadErr := loadExampleFileConfig(path)
		if loadErr != nil {
			continue
		}
		_, _, resolveErr := loaded.chatConfig("", nil)
		if resolveErr == nil {
			t.Fatalf("invalid secret source accepted: %s", authJSON)
		}
		if strings.Contains(resolveErr.Error(), testSecretSentinel) {
			t.Fatal("secret leaked through validation error")
		}
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(secretPath, 0o644); err != nil {
			t.Fatal(err)
		}
		loaded, err := loadExampleFileConfig(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := loaded.chatConfig("", nil); err == nil {
			t.Fatal("group/world-readable secret file was accepted")
		}
	}
}

func TestConfigProviderURLThinkingAndBudgetValidation(t *testing.T) {
	t.Setenv("TEST_IHTML_AUTH_TOKEN", testSecretSentinel)
	validAuth := `{"type":"auth_token","env":"TEST_IHTML_AUTH_TOKEN"}`
	tests := map[string]string{
		"public http":          strings.Replace(minimalClaudeConfig(validAuth), "https://gateway.example", "http://gateway.example", 1),
		"base userinfo":        strings.Replace(minimalClaudeConfig(validAuth), "https://gateway.example", "https://user:pass@gateway.example", 1),
		"double version":       strings.Replace(minimalClaudeConfig(validAuth), "/api/anthropic", "/api/anthropic/v1", 1),
		"budget too small":     strings.Replace(minimalClaudeConfig(validAuth), `"generation": {}`, `"generation":{"max_tokens":2048,"thinking":{"enabled":true,"budget_tokens":512}}`, 1),
		"turn exceeds request": strings.Replace(minimalClaudeConfig(validAuth), `"agent": {}`, `"agent":{"request_timeout":"30s","max_turn_duration":"1m"}`, 1),
		"openai auth token":    strings.Replace(strings.Replace(minimalClaudeConfig(validAuth), `"provider": "claude"`, `"provider":"openai"`, 1), "/api/anthropic", "/v1", 1),
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			loaded, err := loadExampleFileConfig(writeConfigFile(t, content))
			if err != nil {
				return
			}
			_, _, err = loaded.chatConfig("", nil)
			if err == nil {
				t.Fatal("invalid provider configuration was accepted")
			}
			if strings.Contains(err.Error(), testSecretSentinel) {
				t.Fatal("secret leaked through validation error")
			}
		})
	}
}

func TestConfigDisabledDoesNotResolveMissingSecret(t *testing.T) {
	content := strings.Replace(minimalClaudeConfig(`{"type":"auth_token","env":"MISSING_SECRET"}`), `"enabled": true`, `"enabled":false`, 1)
	loaded, err := loadExampleFileConfig(writeConfigFile(t, content))
	if err != nil {
		t.Fatal(err)
	}
	_, enabled, err := loaded.chatConfig("", nil)
	if err != nil || enabled {
		t.Fatalf("disabled config resolved secret: enabled=%v err=%v", enabled, err)
	}
}
