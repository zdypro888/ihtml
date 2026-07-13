package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/zdypro888/ihtml"
)

// sessionCapabilityFingerprint rotates persisted Agent history whenever the
// host changes a boundary that affects available tools, authorization or
// trusted instructions. This prevents evidence produced under an older policy
// from silently crossing into the new capability scope.
func sessionCapabilityFingerprint(cfg Config, access ihtml.Access, apis []ihtml.APISpec) string {
	type apiCapability struct {
		Name        string `json:"name"`
		Method      string `json:"method"`
		Path        string `json:"path"`
		Description string `json:"description"`
	}
	canonicalAPIs := make([]apiCapability, 0, len(apis))
	for _, api := range apis {
		canonicalAPIs = append(canonicalAPIs, apiCapability{
			Name:        strings.TrimSpace(api.Name),
			Method:      strings.ToUpper(strings.TrimSpace(api.Method)),
			Path:        strings.TrimSpace(api.Path),
			Description: strings.TrimSpace(api.Description),
		})
	}
	sort.Slice(canonicalAPIs, func(i, j int) bool {
		if canonicalAPIs[i].Name != canonicalAPIs[j].Name {
			return canonicalAPIs[i].Name < canonicalAPIs[j].Name
		}
		if canonicalAPIs[i].Method != canonicalAPIs[j].Method {
			return canonicalAPIs[i].Method < canonicalAPIs[j].Method
		}
		return canonicalAPIs[i].Path < canonicalAPIs[j].Path
	})
	headers := normalizedSortedStrings(cfg.ProbeForwardHeaders)
	query := normalizedSortedQueryKeys(cfg.ProbeForwardQuery)
	payload := struct {
		SessionNamespace           string          `json:"session_namespace"`
		SessionCapabilityVersion   string          `json:"session_capability_version"`
		ReadOnly                   bool            `json:"read_only"`
		Provider                   string          `json:"provider"`
		SystemPrompt               string          `json:"system_prompt"`
		ConfirmWrites              bool            `json:"confirm_writes"`
		AllowUnconfirmedItemWrites bool            `json:"allow_unconfirmed_item_writes"`
		ProbeBaseURL               string          `json:"probe_base_url"`
		ProbeForwardHeaders        []string        `json:"probe_forward_headers"`
		ProbeForwardQuery          []string        `json:"probe_forward_query"`
		APIs                       []apiCapability `json:"apis"`
	}{
		SessionNamespace:         strings.TrimSpace(cfg.SessionNamespace),
		SessionCapabilityVersion: strings.TrimSpace(cfg.SessionCapabilityVersion),
		ReadOnly:                 access.ReadOnly, Provider: strings.TrimSpace(cfg.Provider),
		SystemPrompt: cfg.SystemPrompt, ConfirmWrites: cfg.ConfirmWrites,
		AllowUnconfirmedItemWrites: cfg.AllowUnconfirmedItemWrites,
		ProbeBaseURL:               strings.TrimSpace(cfg.ProbeBaseURL),
		ProbeForwardHeaders:        headers, ProbeForwardQuery: query, APIs: canonicalAPIs,
	}
	raw, _ := json.Marshal(payload) // the closed struct is always JSON-serializable
	sum := sha256.Sum256(raw)
	// Keep the scope printable and valid UTF-8. It is embedded in the canonical
	// confirmation JSON as well as hashed into the opaque session ID; using raw
	// digest bytes in a Go string would let JSON's invalid-UTF-8 replacement
	// weaken the one-to-one canonical representation.
	return hex.EncodeToString(sum[:])
}

func normalizedSortedStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// HTTP header names are case-insensitive, but URL query keys are not. Keep
// query spelling exact so changing a forwarded identity key rotates history.
func normalizedSortedQueryKeys(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
