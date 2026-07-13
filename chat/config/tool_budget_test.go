package chatconfig_test

import (
	"context"
	"strings"
	"testing"

	chatconfig "github.com/zdypro888/ihtml/chat/config"
)

func TestAgentToolBudgetDefaultsAndExplicitLimits(t *testing.T) {
	loader := chatconfig.NewLoader(chatconfig.Options{Secrets: staticSecrets(secretSentinel)})
	base := claudeConfig(`{"type":"auth_token","ref":"tool-budget"}`)
	defaults, err := loader.Decode(context.Background(), strings.NewReader(base))
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Config.MaxToolCallsPerTurn != 32 || defaults.Config.MaxToolCallsPerTool != 8 || defaults.Config.MaxRepeatedToolCalls != 2 {
		t.Fatalf("tool budget defaults = %d/%d/%d", defaults.Config.MaxToolCallsPerTurn, defaults.Config.MaxToolCallsPerTool, defaults.Config.MaxRepeatedToolCalls)
	}

	explicitJSON := strings.Replace(base, `"agent": {}`, `"agent":{"max_tool_calls":7,"max_tool_calls_per_tool":3,"max_repeated_tool_calls":-9}`, 1)
	explicit, err := loader.Decode(context.Background(), strings.NewReader(explicitJSON))
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Config.MaxToolCallsPerTurn != 7 || explicit.Config.MaxToolCallsPerTool != 3 || explicit.Config.MaxRepeatedToolCalls != -1 {
		t.Fatalf("explicit tool budgets = %d/%d/%d", explicit.Config.MaxToolCallsPerTurn, explicit.Config.MaxToolCallsPerTool, explicit.Config.MaxRepeatedToolCalls)
	}
}

func TestAgentToolBudgetRejectsUnboundedPositiveConfig(t *testing.T) {
	loader := chatconfig.NewLoader(chatconfig.Options{Secrets: staticSecrets(secretSentinel)})
	raw := strings.Replace(
		claudeConfig(`{"type":"auth_token","ref":"tool-budget"}`),
		`"agent": {}`, `"agent":{"max_tool_calls":513}`, 1,
	)
	if _, err := loader.Decode(context.Background(), strings.NewReader(raw)); err == nil || !strings.Contains(err.Error(), "ai.agent.max_tool_calls") {
		t.Fatalf("oversized tool budget error = %v", err)
	}
}

func TestAgentContextAndDiscoveryDefaultsAndOverrides(t *testing.T) {
	loader := chatconfig.NewLoader(chatconfig.Options{Secrets: staticSecrets(secretSentinel)})
	base := claudeConfig(`{"type":"auth_token","ref":"context"}`)
	defaults, err := loader.Decode(context.Background(), strings.NewReader(base))
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Config.DisableDynamicToolSearch || defaults.Config.DisableContextSummarization ||
		defaults.Config.SummarizeAfterTokens != 24000 || defaults.Config.SummarizeAfterMessages != 80 ||
		defaults.Config.MaxModelRetries != 1 {
		t.Fatalf("agent defaults = dynamic-disabled:%v summary-disabled:%v summary:%d/%d retries:%d",
			defaults.Config.DisableDynamicToolSearch, defaults.Config.DisableContextSummarization,
			defaults.Config.SummarizeAfterTokens, defaults.Config.SummarizeAfterMessages,
			defaults.Config.MaxModelRetries)
	}

	explicitJSON := strings.Replace(base, `"agent": {}`,
		`"agent":{"dynamic_tool_search":false,"context_summarization":false,"summarize_after_tokens":4096,"summarize_after_messages":12,"max_model_retries":-1}`, 1)
	explicit, err := loader.Decode(context.Background(), strings.NewReader(explicitJSON))
	if err != nil {
		t.Fatal(err)
	}
	if !explicit.Config.DisableDynamicToolSearch || !explicit.Config.DisableContextSummarization ||
		explicit.Config.SummarizeAfterTokens != 4096 || explicit.Config.SummarizeAfterMessages != 12 ||
		explicit.Config.MaxModelRetries != -1 {
		t.Fatalf("agent overrides = %#v", explicit.Config)
	}
}

func TestBackendCapacityLimitsLoadAndValidate(t *testing.T) {
	loader := chatconfig.NewLoader(chatconfig.Options{Secrets: staticSecrets(secretSentinel)})
	base := claudeConfig(`{"type":"auth_token","ref":"capacity"}`)
	raw := strings.Replace(base, `"agent": {}`,
		`"agent":{"max_outstanding_host_callbacks":7,"max_outstanding_observations":9,"max_outstanding_operations":11}`, 1)
	result, err := loader.Decode(context.Background(), strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.MaxOutstandingHostCallbacks != 7 || result.Config.MaxOutstandingObservations != 9 || result.Config.MaxOutstandingOperations != 11 {
		t.Fatalf("capacity overrides = %d/%d/%d", result.Config.MaxOutstandingHostCallbacks, result.Config.MaxOutstandingObservations, result.Config.MaxOutstandingOperations)
	}

	for _, field := range []string{"max_outstanding_host_callbacks", "max_outstanding_observations", "max_outstanding_operations"} {
		for _, value := range []string{"-1", "4097"} {
			invalid := strings.Replace(base, `"agent": {}`, `"agent":{"`+field+`":`+value+`}`, 1)
			if _, err := loader.Decode(context.Background(), strings.NewReader(invalid)); err == nil || !strings.Contains(err.Error(), "ai.agent."+field) {
				t.Fatalf("%s=%s error = %v", field, value, err)
			}
		}
	}
}

func TestAgentContextThresholdsAreBounded(t *testing.T) {
	loader := chatconfig.NewLoader(chatconfig.Options{Secrets: staticSecrets(secretSentinel)})
	base := claudeConfig(`{"type":"auth_token","ref":"context"}`)
	for _, test := range []struct {
		field string
		value string
	}{
		{"summarize_after_tokens", "1023"},
		{"summarize_after_messages", "7"},
	} {
		raw := strings.Replace(base, `"agent": {}`, `"agent":{"`+test.field+`":`+test.value+`}`, 1)
		if _, err := loader.Decode(context.Background(), strings.NewReader(raw)); err == nil || !strings.Contains(err.Error(), "ai.agent."+test.field) {
			t.Fatalf("%s invalid threshold error = %v", test.field, err)
		}
	}
}

func TestHistoryNamespaceAndCapabilityVersionLoad(t *testing.T) {
	loader := chatconfig.NewLoader(chatconfig.Options{Secrets: staticSecrets(secretSentinel)})
	base := claudeConfig(`{"type":"auth_token","ref":"history-scope"}`)
	raw := strings.Replace(base, `"history": {}`, `"history":{"namespace":"billing-app","capability_version":"v3"}`, 1)
	result, err := loader.Decode(context.Background(), strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.SessionNamespace != "billing-app" || result.Config.SessionCapabilityVersion != "v3" {
		t.Fatalf("history scope = %q/%q", result.Config.SessionNamespace, result.Config.SessionCapabilityVersion)
	}
}
