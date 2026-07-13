package chatconfig_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zdypro888/ihtml/chat"
	chatconfig "github.com/zdypro888/ihtml/chat/config"
)

const secretSentinel = "sdk-config-secret-sentinel"

func claudeConfig(auth string) string {
	return fmt.Sprintf(`{
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
}`, auth)
}

func staticSecrets(value string) chatconfig.SecretResolver {
	return chatconfig.SecretResolverFunc(func(_ context.Context, _ chatconfig.SecretRef) ([]byte, error) {
		return []byte(value), nil
	})
}

type typedNilSecretResolver struct{ value string }

func (r *typedNilSecretResolver) ResolveSecret(context.Context, chatconfig.SecretRef) ([]byte, error) {
	return []byte(r.value), nil // dereference proves the loader must reject/normalize first.
}

type typedNilReader struct{ data []byte }

func (r *typedNilReader) Read(p []byte) (int, error) {
	return copy(p, r.data), nil
}

func ExampleLoader() {
	loader := chatconfig.NewLoader(chatconfig.Options{
		Secrets: chatconfig.SecretResolverFunc(func(_ context.Context, ref chatconfig.SecretRef) ([]byte, error) {
			if ref.Kind != chatconfig.SecretRefName || ref.Name != "vault://ihtml/model" {
				return nil, errors.New("unknown secret")
			}
			return []byte("example-secret"), nil
		}),
	})
	result, err := loader.Decode(context.Background(), strings.NewReader(claudeConfig(`{"type":"auth_token","ref":"vault://ihtml/model"}`)))
	fmt.Println(result.Enabled, result.Config.Provider, err)
	// Output: true claude <nil>
}

func TestExternalConsumerResolvesClaudeDefaults(t *testing.T) {
	var got chatconfig.SecretRef
	loader := chatconfig.NewLoader(chatconfig.Options{Secrets: chatconfig.SecretResolverFunc(func(_ context.Context, ref chatconfig.SecretRef) ([]byte, error) {
		got = ref
		return []byte(secretSentinel), nil
	})})
	result, err := loader.Decode(context.Background(), strings.NewReader(claudeConfig(`{"type":"auth_token","ref":"vault://team/ihtml"}`)))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Enabled || result.Config.Provider != chat.ProviderClaude || result.Config.Model != "glm-test" {
		t.Fatalf("result = %#v", result)
	}
	if got != (chatconfig.SecretRef{Kind: chatconfig.SecretRefName, Name: "vault://team/ihtml"}) {
		t.Fatalf("secret ref = %#v", got)
	}
	if result.Config.AuthToken != secretSentinel || result.Config.APIKey != "" {
		t.Fatal("credential was not isolated to AuthToken")
	}
	if !result.Config.Stream || result.Config.MaxTokens != 4096 || result.Config.RequestTimeout != 3*time.Minute || result.Config.MaxTurnDuration != 2*time.Minute {
		t.Fatalf("defaults = stream:%v tokens:%d request:%s turn:%s", result.Config.Stream, result.Config.MaxTokens, result.Config.RequestTimeout, result.Config.MaxTurnDuration)
	}
	if result.Config.MaxHistorySessions != 128 || result.Config.MaxHistoryEvents != 256 || result.Config.HistoryTTL != 12*time.Hour {
		t.Fatal("history defaults were not applied")
	}
}

func TestExplicitZeroTemperatureIsPreserved(t *testing.T) {
	raw := strings.Replace(claudeConfig(`{"type":"auth_token","ref":"temperature"}`), `"generation": {}`, `"generation":{"temperature":0}`, 1)
	result, err := chatconfig.NewLoader(chatconfig.Options{Secrets: staticSecrets(secretSentinel)}).Decode(context.Background(), strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.TemperatureOverride == nil || *result.Config.TemperatureOverride != 0 {
		t.Fatalf("temperature override = %v", result.Config.TemperatureOverride)
	}
}

func TestNoImplicitEnvironmentOrResolverFallback(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", secretSentinel)
	t.Setenv("IHTML_AI_AUTH_TOKEN", secretSentinel)
	loader := chatconfig.NewLoader(chatconfig.Options{})
	_, err := loader.Decode(context.Background(), strings.NewReader(claudeConfig(`{"type":"auth_token","env":"ANTHROPIC_AUTH_TOKEN"}`)))
	if !errors.Is(err, chatconfig.ErrSecretUnavailable) || !errors.Is(err, chatconfig.ErrInvalidConfig) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(fmt.Sprint(err), secretSentinel) {
		t.Fatal("ambient secret leaked into error")
	}
	var typed *chatconfig.Error
	if !errors.As(err, &typed) || typed.Kind != chatconfig.ErrorSecretUnavailable || typed.Path != "ai.auth" {
		t.Fatalf("typed error = %#v", typed)
	}
}

func TestTypedNilSecretResolverIsRejectedWithoutPanic(t *testing.T) {
	var pointerResolver *typedNilSecretResolver
	var functionResolver chatconfig.SecretResolverFunc
	for name, resolver := range map[string]chatconfig.SecretResolver{
		"pointer":  pointerResolver,
		"function": functionResolver,
	} {
		t.Run(name, func(t *testing.T) {
			opts := chatconfig.Options{Secrets: resolver}
			err := opts.Validate()
			if !errors.Is(err, chatconfig.ErrInvalidConfig) || !errors.Is(err, chatconfig.ErrSecretUnavailable) {
				t.Fatalf("Options.Validate error = %v", err)
			}
			var typed *chatconfig.Error
			if !errors.As(err, &typed) || typed.Kind != chatconfig.ErrorSecretUnavailable || typed.Path != "options.secrets" {
				t.Fatalf("typed options error = %#v", typed)
			}
			if loader, err := chatconfig.NewLoaderChecked(opts); err == nil || loader != nil {
				t.Fatalf("NewLoaderChecked loader=%v err=%v", loader, err)
			}

			// Source-compatible NewLoader treats typed nil exactly like plain nil;
			// a credential-bearing document receives the established typed error.
			loader := chatconfig.NewLoader(opts)
			_, err = loader.Decode(context.Background(), strings.NewReader(
				claudeConfig(`{"type":"auth_token","ref":"typed-nil"}`),
			))
			if !errors.Is(err, chatconfig.ErrSecretUnavailable) {
				t.Fatalf("legacy NewLoader Decode error = %v", err)
			}
		})
	}
}

func TestDecodeRejectsTypedNilReaderWithoutPanic(t *testing.T) {
	var reader *typedNilReader
	_, err := chatconfig.NewLoader(chatconfig.Options{}).Decode(context.Background(), reader)
	var typed *chatconfig.Error
	if !errors.As(err, &typed) || typed.Kind != chatconfig.ErrorSyntax || typed.Path != "$" {
		t.Fatalf("typed-nil reader error = %#v (%v)", typed, err)
	}
}

func TestDisabledConfigDoesNotResolveSecret(t *testing.T) {
	var calls atomic.Int64
	loader := chatconfig.NewLoader(chatconfig.Options{Secrets: chatconfig.SecretResolverFunc(func(context.Context, chatconfig.SecretRef) ([]byte, error) {
		calls.Add(1)
		return nil, errors.New("must not run")
	})})
	raw := strings.Replace(claudeConfig(`{"type":"auth_token","ref":"missing"}`), `"enabled": true`, `"enabled": false`, 1)
	result, err := loader.Decode(context.Background(), strings.NewReader(raw))
	if err != nil || result.Enabled || calls.Load() != 0 {
		t.Fatalf("result=%#v calls=%d err=%v", result, calls.Load(), err)
	}
}

func TestStrictJSONAndTypedErrors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		kind chatconfig.ErrorKind
	}{
		{"top array", `[]`, chatconfig.ErrorSyntax},
		{"duplicate root", `{"schema_version":1,"schema_version":1}`, chatconfig.ErrorDuplicateKey},
		{"duplicate nested", `{"schema_version":1,"enabled":false,"auth":{"type":"none","type":"none"}}`, chatconfig.ErrorDuplicateKey},
		{"unknown root", `{"schema_version":1,"enabled":false,"future":true}`, chatconfig.ErrorUnknownField},
		{"unknown nested", `{"schema_version":1,"enabled":false,"generation":{"future":true}}`, chatconfig.ErrorUnknownField},
		{"trailing", `{"schema_version":1,"enabled":false} {}`, chatconfig.ErrorSyntax},
		{"wrong version", `{"schema_version":2,"enabled":false}`, chatconfig.ErrorUnsupportedVersion},
		{"missing enabled", `{"schema_version":1}`, chatconfig.ErrorValidation},
	}
	loader := chatconfig.NewLoader(chatconfig.Options{})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loader.Decode(context.Background(), strings.NewReader(tc.raw))
			var typed *chatconfig.Error
			if !errors.As(err, &typed) || typed.Kind != tc.kind {
				t.Fatalf("error = %#v (%v), want kind %s", typed, err, tc.kind)
			}
			if !errors.Is(err, chatconfig.ErrInvalidConfig) {
				t.Fatal("all config errors must match ErrInvalidConfig")
			}
		})
	}
	oversized := `{"schema_version":1,"enabled":false,"system_prompt":"` + strings.Repeat("x", chatconfig.MaxDocumentBytes) + `"}`
	if _, err := loader.Decode(context.Background(), strings.NewReader(oversized)); err == nil {
		t.Fatal("oversized document was accepted")
	}
	invalidUTF8 := json.RawMessage([]byte{'{', '"', 0xff, '"', ':', '1', '}'})
	if _, err := loader.DecodeSection(context.Background(), invalidUTF8); err == nil {
		t.Fatal("invalid UTF-8 was accepted")
	}
}

func TestSecretResolverCalledOnceAndErrorIsRedacted(t *testing.T) {
	var calls atomic.Int64
	loader := chatconfig.NewLoader(chatconfig.Options{Secrets: chatconfig.SecretResolverFunc(func(_ context.Context, _ chatconfig.SecretRef) ([]byte, error) {
		calls.Add(1)
		return nil, errors.New("provider exposed " + secretSentinel)
	})})
	_, err := loader.Decode(context.Background(), strings.NewReader(claudeConfig(`{"type":"auth_token","ref":"vault-key"}`)))
	if calls.Load() != 1 || !errors.Is(err, chatconfig.ErrSecretUnavailable) {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
	if strings.Contains(fmt.Sprint(err), secretSentinel) {
		t.Fatal("resolver error leaked secret material")
	}
}

func TestExplicitOSSecretResolver(t *testing.T) {
	t.Setenv("SDK_CONFIG_TOKEN", secretSentinel)
	loader := chatconfig.NewLoader(chatconfig.Options{Secrets: chatconfig.NewOSSecretResolver()})
	result, err := loader.Decode(context.Background(), strings.NewReader(claudeConfig(`{"type":"auth_token","env":"SDK_CONFIG_TOKEN"}`)))
	if err != nil || result.Config.AuthToken != secretSentinel {
		t.Fatalf("env result=%#v err=%v", result, err)
	}

	secretPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(secretPath, []byte(secretSentinel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = loader.Decode(context.Background(), strings.NewReader(claudeConfig(fmt.Sprintf(`{"type":"auth_token","file":%q}`, secretPath))))
	if err != nil || result.Config.AuthToken != secretSentinel {
		t.Fatalf("file result=%#v err=%v", result, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(secretPath, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loader.Decode(context.Background(), strings.NewReader(claudeConfig(fmt.Sprintf(`{"type":"auth_token","file":%q}`, secretPath)))); !errors.Is(err, chatconfig.ErrSecretUnavailable) {
			t.Fatalf("public secret file accepted: %v", err)
		}
	}
}

func TestClaudeNoneIsRejectedBeforeAmbientSDKFallback(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", secretSentinel)
	raw := strings.Replace(claudeConfig(`{"type":"none"}`), "https://gateway.example/api/anthropic", "http://127.0.0.1:9999", 1)
	_, err := chatconfig.NewLoader(chatconfig.Options{}).Decode(context.Background(), strings.NewReader(raw))
	if err == nil || !errors.Is(err, chatconfig.ErrInvalidConfig) {
		t.Fatalf("Claude auth=none accepted: %v", err)
	}
}

func TestOpenAILoopbackMayExplicitlyUseNoAuthentication(t *testing.T) {
	raw := strings.Replace(claudeConfig(`{"type":"none"}`), `"provider": "claude"`, `"provider": "openai"`, 1)
	raw = strings.Replace(raw, "https://gateway.example/api/anthropic", "http://127.0.0.1:11434/v1", 1)
	result, err := chatconfig.NewLoader(chatconfig.Options{}).Decode(context.Background(), strings.NewReader(raw))
	if err != nil || !result.Enabled || result.Config.APIKey != "" || result.Config.AuthToken != "" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestLoaderIsConcurrentAndContextAware(t *testing.T) {
	loader := chatconfig.NewLoader(chatconfig.Options{Secrets: staticSecrets(secretSentinel)})
	raw := claudeConfig(`{"type":"auth_token","ref":"shared"}`)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := loader.Decode(context.Background(), strings.NewReader(raw))
			if err != nil || result.Config.AuthToken != secretSentinel {
				t.Errorf("result=%#v err=%v", result, err)
			}
		}()
	}
	wg.Wait()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := loader.Decode(canceled, strings.NewReader(raw)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestLoadFileRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions differ on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "ai.json")
	if err := os.WriteFile(target, []byte(strings.Replace(claudeConfig(`{"type":"none"}`), "claude", "openai", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := chatconfig.NewLoader(chatconfig.Options{}).LoadFile(context.Background(), link); err == nil {
		t.Fatal("symlink configuration was accepted")
	}
}

func FuzzLoaderDecodeNeverPanics(f *testing.F) {
	for _, seed := range []string{
		`{}`,
		`{"schema_version":1,"enabled":false}`,
		claudeConfig(`{"type":"auth_token","ref":"seed"}`),
		`{"schema_version":1,"schema_version":1}`,
	} {
		f.Add(seed)
	}
	loader := chatconfig.NewLoader(chatconfig.Options{Secrets: staticSecrets(secretSentinel)})
	f.Fuzz(func(t *testing.T, raw string) {
		_, _ = loader.Decode(context.Background(), strings.NewReader(raw))
	})
}
