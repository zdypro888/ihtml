package textfmt

import (
	"regexp"
	"strings"
)

func TruncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

var (
	secretJSONAssignmentRe = regexp.MustCompile(`(?i)("?(?:api[_-]?key|api[_-]?hash|access[_-]?token|worker[_-]?access[_-]?token|token|secret|password)"?\s*:\s*")([^"]{8,})(")`)
	secretKVAssignmentRe   = regexp.MustCompile(`(?i)\b((?:api[_-]?key|api[_-]?hash|access[_-]?token|worker[_-]?access[_-]?token|token|secret|password)\s*[:=]\s*["']?)([^"'\s<]{8,})(["']?)`)
	secretPatterns         = []*regexp.Regexp{
		regexp.MustCompile(`(?is)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
		regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/=-]{12,}`),
		regexp.MustCompile(`(?i)\bsk-[a-z0-9][a-z0-9._-]{12,}`),
		regexp.MustCompile(`(?i)\b(?:gh[pousr]_[a-z0-9]{20,}|github_pat_[a-z0-9_]{20,})\b`),
		regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
		regexp.MustCompile(`(?i)\b[0-9]{6,14}:[a-z0-9_-]{20,}\b`),
		regexp.MustCompile(`(?i)\b[a-z0-9]{24,}\.[a-z0-9_-]{12,}\b`),
		regexp.MustCompile(`(?i)\b[a-f0-9]{48,}\b`),
	}
	reasoningBlockRe        = regexp.MustCompile(`(?is)<\s*think\b[^>]*>.*?<\s*/\s*think\s*>`)
	reasoningCloseRe        = regexp.MustCompile(`(?is)<\s*/\s*think\s*>`)
	reasoningOpenRe         = regexp.MustCompile(`(?is)<\s*think\b[^>]*>`)
	escapedReasoningBlockRe = regexp.MustCompile(`(?is)&lt;\s*think\b[^&]*?&gt;.*?&lt;\s*/\s*think\s*&gt;`)
	escapedReasoningCloseRe = regexp.MustCompile(`(?is)&lt;\s*/\s*think\s*&gt;`)
	escapedReasoningOpenRe  = regexp.MustCompile(`(?is)&lt;\s*think\b[^&]*?&gt;`)
)

// RedactSecrets removes API keys, Telegram bot tokens, worker access tokens,
// and common credential assignments at derived or diagnostic boundaries such
// as logs, audits, semantic indexes, and learning candidates. Do not apply it
// to an authorized canonical record: destructive redaction there breaks later
// Agent turns and cannot be reversed after permissions have been checked.
func RedactSecrets(s string) string {
	s = secretJSONAssignmentRe.ReplaceAllString(s, `$1[redacted]$3`)
	s = secretKVAssignmentRe.ReplaceAllString(s, `$1[redacted]$3`)
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, "[redacted]")
	}
	return s
}

// StripReasoning removes model-emitted <think> blocks from user-visible text.
// Some OpenAI-compatible reasoning models put chain-of-thought in Content
// instead of a dedicated reasoning field, sometimes even with a missing opening
// tag during streaming. In that dangling-close case, everything before the last
// </think> is treated as hidden reasoning and dropped.
func StripReasoning(s string) string {
	if s == "" {
		return s
	}
	for {
		next := reasoningBlockRe.ReplaceAllString(s, "")
		next = escapedReasoningBlockRe.ReplaceAllString(next, "")
		if next == s {
			break
		}
		s = next
	}
	if locs := reasoningCloseRe.FindAllStringIndex(s, -1); len(locs) > 0 {
		s = s[locs[len(locs)-1][1]:]
	}
	if locs := escapedReasoningCloseRe.FindAllStringIndex(s, -1); len(locs) > 0 {
		s = s[locs[len(locs)-1][1]:]
	}
	if loc := reasoningOpenRe.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	}
	if loc := escapedReasoningOpenRe.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	}
	s = reasoningCloseRe.ReplaceAllString(s, "")
	s = escapedReasoningCloseRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}
