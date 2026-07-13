// Package chatconfig loads the data-only portion of an ihtml chat backend
// configuration. It is intentionally independent from an application's HTTP
// server, Store, authentication, and other host-owned concerns.
//
// Secrets are resolved through an explicitly supplied SecretResolver. The core
// decoder never searches provider-specific environment variables or silently
// falls back to ambient process configuration.
package chatconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zdypro888/ihtml/chat"
	"github.com/zdypro888/ihtml/internal/nilcheck"
)

const (
	// CurrentSchemaVersion is the latest supported AI configuration schema.
	CurrentSchemaVersion = 1
	// MaxDocumentBytes bounds both standalone and embedded AI documents.
	MaxDocumentBytes = 64 << 10
	maxSecretBytes   = 16 << 10
	maxPromptBytes   = 16 << 10
)

// ErrorKind is a stable, machine-readable configuration error category.
type ErrorKind string

const (
	ErrorSyntax             ErrorKind = "syntax"
	ErrorDuplicateKey       ErrorKind = "duplicate_key"
	ErrorUnknownField       ErrorKind = "unknown_field"
	ErrorUnsupportedVersion ErrorKind = "unsupported_version"
	ErrorValidation         ErrorKind = "validation"
	ErrorSecretUnavailable  ErrorKind = "secret_unavailable"
)

var (
	// ErrInvalidConfig matches every decoder and validation failure.
	ErrInvalidConfig = errors.New("chatconfig: invalid configuration")
	// ErrUnsupportedVersion matches a document with an unknown schema version.
	ErrUnsupportedVersion = errors.New("chatconfig: unsupported schema version")
	// ErrSecretUnavailable matches a missing, invalid, or failed secret lookup.
	ErrSecretUnavailable = errors.New("chatconfig: secret unavailable")
)

// Error reports a typed failure without including secret values. Path uses the
// JSON field spelling so applications can map it back to their configuration UI.
type Error struct {
	Kind    ErrorKind
	Path    string
	Problem string
	cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return ErrInvalidConfig.Error()
	}
	prefix := "chatconfig"
	if e.Path != "" {
		prefix += ": " + e.Path
	}
	if e.Problem != "" {
		return prefix + ": " + e.Problem
	}
	return prefix + ": " + string(e.Kind)
}

func (e *Error) Unwrap() error { return e.cause }

func (e *Error) Is(target error) bool {
	if target == ErrInvalidConfig {
		return true
	}
	if target == ErrUnsupportedVersion {
		return e != nil && e.Kind == ErrorUnsupportedVersion
	}
	if target == ErrSecretUnavailable {
		return e != nil && e.Kind == ErrorSecretUnavailable
	}
	return false
}

func configError(kind ErrorKind, path, problem string, cause error) error {
	// Resolver errors can contain credentials or provider response bodies. Keep
	// them out of both Error() and the unwrap chain; context cancellation is safe
	// and useful to preserve for callers.
	if kind == ErrorSecretUnavailable && !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
		cause = nil
	}
	return &Error{Kind: kind, Path: path, Problem: problem, cause: cause}
}

// SecretRef is the normalized reference passed to a SecretResolver. Name is an
// environment variable name, absolute file path, or host-defined opaque ref.
type SecretRef struct {
	Kind string
	Name string
}

const (
	SecretEnv     = "env"
	SecretFile    = "file"
	SecretRefName = "ref"
)

// SecretResolver resolves only the reference explicitly present in the JSON.
// Implementations must be safe for concurrent use. Returned bytes are copied by
// the loader and may be cleared after Resolve returns.
type SecretResolver interface {
	ResolveSecret(context.Context, SecretRef) ([]byte, error)
}

// SecretResolverFunc adapts a function to SecretResolver.
type SecretResolverFunc func(context.Context, SecretRef) ([]byte, error)

func (f SecretResolverFunc) ResolveSecret(ctx context.Context, ref SecretRef) ([]byte, error) {
	return f(ctx, ref)
}

// Options contains host-provided dependencies. A nil Secrets resolver is valid
// for disabled or auth.type=none configurations, but never triggers ambient env
// lookup when a credential is required.
type Options struct {
	Secrets SecretResolver
}

// Validate checks host-provided loader dependencies without reading a
// configuration document or resolving a secret.
func (o Options) Validate() error {
	if o.Secrets != nil && nilcheck.IsNil(o.Secrets) {
		return configError(ErrorSecretUnavailable, "options.secrets", "must not contain a typed nil implementation", nil)
	}
	return nil
}

// Loader is immutable after construction and safe for concurrent use.
type Loader struct {
	secrets SecretResolver
}

// NewLoader constructs an isolated loader. It does not read files, environment
// variables, or global provider settings.
func NewLoader(opts Options) *Loader {
	// Preserve the original no-error constructor while making a typed nil behave
	// like plain nil rather than a delayed method-call panic. NewLoaderChecked is
	// available to reject the programming error immediately.
	if nilcheck.IsNil(opts.Secrets) {
		opts.Secrets = nil
	}
	return &Loader{secrets: opts.Secrets}
}

// NewLoaderChecked validates host dependencies before constructing a Loader.
func NewLoaderChecked(opts Options) (*Loader, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	return NewLoader(opts), nil
}

// Result is the resolved SDK configuration. Config is zero when Enabled is false.
type Result struct {
	Enabled bool
	Config  chat.Config
}

// Decode reads a standalone AI configuration object. The same object can be
// embedded under an application's own "ai" field and passed to DecodeSection.
func (l *Loader) Decode(ctx context.Context, r io.Reader) (Result, error) {
	if nilcheck.IsNil(r) {
		return Result{}, configError(ErrorSyntax, "$", "reader is nil", nil)
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxDocumentBytes+1))
	if err != nil {
		return Result{}, configError(ErrorSyntax, "$", "cannot read JSON", err)
	}
	if len(data) > MaxDocumentBytes {
		return Result{}, configError(ErrorValidation, "$", fmt.Sprintf("document exceeds %d bytes", MaxDocumentBytes), nil)
	}
	return l.DecodeSection(ctx, data)
}

// DecodeSection resolves an embedded raw AI configuration object.
func (l *Loader) DecodeSection(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return Result{}, nil
	}
	if len(trimmed) > MaxDocumentBytes {
		return Result{}, configError(ErrorValidation, "ai", fmt.Sprintf("section exceeds %d bytes", MaxDocumentBytes), nil)
	}
	if !utf8.Valid(trimmed) {
		return Result{}, configError(ErrorSyntax, "ai", "must be UTF-8 JSON", nil)
	}
	if err := validateUniqueJSONObject(trimmed); err != nil {
		var duplicate *duplicateKeyError
		if errors.As(err, &duplicate) {
			return Result{}, configError(ErrorDuplicateKey, duplicate.Path, "duplicate object key", nil)
		}
		return Result{}, configError(ErrorSyntax, "ai", "must be one JSON object", err)
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var rawConfig aiFileConfig
	if err := dec.Decode(&rawConfig); err != nil {
		kind := ErrorSyntax
		problem := "invalid JSON field"
		if strings.Contains(err.Error(), "unknown field") {
			kind = ErrorUnknownField
			problem = err.Error()
		}
		return Result{}, configError(kind, "ai", problem, err)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return Result{}, configError(ErrorSyntax, "ai", "trailing JSON is not allowed", err)
	}
	if rawConfig.SchemaVersion != CurrentSchemaVersion {
		return Result{}, configError(ErrorUnsupportedVersion, "ai.schema_version", fmt.Sprintf("version %d is not supported; expected %d", rawConfig.SchemaVersion, CurrentSchemaVersion), nil)
	}
	return l.resolve(ctx, rawConfig)
}

// LoadFile securely reads and resolves a standalone AI configuration file. The
// file must be regular, non-symlink, and not writable by group/other on Unix.
func (l *Loader) LoadFile(ctx context.Context, path string) (Result, error) {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil || strings.TrimSpace(path) == "" {
		return Result{}, configError(ErrorValidation, "$", "configuration path is invalid", err)
	}
	data, _, err := readCheckedRegularFile(abs, MaxDocumentBytes, false)
	if err != nil {
		return Result{}, configError(ErrorValidation, "$", "cannot read configuration file", err)
	}
	return l.Decode(ctx, bytes.NewReader(data))
}

type aiFileConfig struct {
	SchemaVersion int                  `json:"schema_version"`
	Enabled       *bool                `json:"enabled"`
	Provider      string               `json:"provider"`
	BaseURL       string               `json:"base_url"`
	Model         string               `json:"model"`
	Auth          authFileConfig       `json:"auth"`
	Generation    generationFileConfig `json:"generation"`
	Agent         agentFileConfig      `json:"agent"`
	Safety        safetyFileConfig     `json:"safety"`
	History       historyFileConfig    `json:"history"`
	SystemPrompt  string               `json:"system_prompt"`
}

type authFileConfig struct {
	Type string `json:"type"`
	Env  string `json:"env"`
	File string `json:"file"`
	Ref  string `json:"ref"`
}

type generationFileConfig struct {
	Stream      *bool              `json:"stream"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float32           `json:"temperature"`
	Thinking    thinkingFileConfig `json:"thinking"`
}

type thinkingFileConfig struct {
	Enabled      bool `json:"enabled"`
	BudgetTokens int  `json:"budget_tokens"`
}

type agentFileConfig struct {
	DeepEnabled      bool   `json:"deep_enabled"`
	AllowModelSelect bool   `json:"allow_model_select"`
	MaxIterations    int    `json:"max_iterations"`
	MaxModelCalls    int    `json:"max_model_calls"`
	MaxSubtasks      int    `json:"max_subtasks"`
	RequestTimeout   string `json:"request_timeout"`
	MaxTurnDuration  string `json:"max_turn_duration"`
}

type safetyFileConfig struct {
	ConfirmWrites      *bool          `json:"confirm_writes"`
	WritesPerMinute    int            `json:"writes_per_minute"`
	ConfirmationSecret *secretFileRef `json:"confirmation_secret"`
}

type secretFileRef struct {
	Env  string `json:"env"`
	File string `json:"file"`
	Ref  string `json:"ref"`
}

type historyFileConfig struct {
	MaxSessions int    `json:"max_sessions"`
	MaxEvents   int    `json:"max_events_per_session"`
	MaxBytes    int64  `json:"max_bytes_per_session"`
	TTL         string `json:"ttl"`
}

func (l *Loader) resolve(ctx context.Context, raw aiFileConfig) (Result, error) {
	if raw.Enabled == nil {
		return Result{}, validation("ai.enabled", "must be explicitly set")
	}
	if !*raw.Enabled {
		return Result{}, nil
	}

	provider := strings.ToLower(strings.TrimSpace(raw.Provider))
	if provider != chat.ProviderOpenAI && provider != chat.ProviderClaude {
		return Result{}, validation("ai.provider", "must be openai or claude")
	}
	baseURL, loopback, err := validateBaseURL(raw.BaseURL)
	if err != nil {
		return Result{}, validation("ai.base_url", err.Error())
	}
	if provider == chat.ProviderClaude && strings.HasSuffix(strings.TrimRight(baseURL.Path, "/"), "/v1") {
		return Result{}, validation("ai.base_url", "Claude endpoints must use the protocol root; the adapter appends /v1/messages")
	}
	model := strings.TrimSpace(raw.Model)
	if model == "" || len(model) > 256 || strings.ContainsFunc(model, unicode.IsControl) {
		return Result{}, validation("ai.model", "must contain 1..256 non-control characters")
	}

	apiKey, authToken, err := l.resolveAuth(ctx, provider, loopback, raw.Auth)
	if err != nil {
		return Result{}, err
	}
	stream := true
	if raw.Generation.Stream != nil {
		stream = *raw.Generation.Stream
	}
	maxTokens := raw.Generation.MaxTokens
	if maxTokens == 0 && provider == chat.ProviderClaude {
		maxTokens = 4096
	}
	if maxTokens < 0 || maxTokens > 1<<20 {
		return Result{}, validation("ai.generation.max_tokens", "must be 0..1048576")
	}
	temperature := float32(0)
	var temperatureOverride *float32
	if raw.Generation.Temperature != nil {
		temperature = *raw.Generation.Temperature
		temperatureOverride = &temperature
		if temperature < 0 || temperature > 2 {
			return Result{}, validation("ai.generation.temperature", "must be between 0 and 2")
		}
	}
	thinking := raw.Generation.Thinking.Enabled
	thinkingBudget := raw.Generation.Thinking.BudgetTokens
	if provider != chat.ProviderClaude && (thinking || thinkingBudget != 0) {
		return Result{}, validation("ai.generation.thinking", "is only supported by the Claude provider")
	}
	if !thinking && thinkingBudget != 0 {
		return Result{}, validation("ai.generation.thinking.budget_tokens", "requires thinking.enabled=true")
	}
	if thinking {
		if thinkingBudget == 0 {
			thinkingBudget = 1024
		}
		if thinkingBudget < 1024 || maxTokens <= thinkingBudget {
			return Result{}, validation("ai.generation.thinking.budget_tokens", "must be at least 1024 and smaller than max_tokens")
		}
		if temperature != 0 && temperature != 1 {
			return Result{}, validation("ai.generation.temperature", "Claude extended thinking supports only 1 or the zero-value default")
		}
	}

	requestTimeout, err := configuredDuration(raw.Agent.RequestTimeout, 3*time.Minute, time.Hour, "ai.agent.request_timeout")
	if err != nil {
		return Result{}, err
	}
	turnDuration, err := configuredDuration(raw.Agent.MaxTurnDuration, 2*time.Minute, time.Hour, "ai.agent.max_turn_duration")
	if err != nil {
		return Result{}, err
	}
	if turnDuration > requestTimeout {
		return Result{}, validation("ai.agent.max_turn_duration", "cannot exceed request_timeout")
	}
	maxIterations, err := boundedInt(raw.Agent.MaxIterations, 16, 1, 64, "ai.agent.max_iterations")
	if err != nil {
		return Result{}, err
	}
	maxCalls, err := boundedInt(raw.Agent.MaxModelCalls, 24, 1, 128, "ai.agent.max_model_calls")
	if err != nil {
		return Result{}, err
	}
	maxSubtasks, err := boundedInt(raw.Agent.MaxSubtasks, 3, 1, 16, "ai.agent.max_subtasks")
	if err != nil {
		return Result{}, err
	}

	confirmWrites := true
	if raw.Safety.ConfirmWrites != nil {
		confirmWrites = *raw.Safety.ConfirmWrites
	}
	writesPerMinute, err := boundedInt(raw.Safety.WritesPerMinute, 240, 1, 10000, "ai.safety.writes_per_minute")
	if err != nil {
		return Result{}, err
	}
	var confirmSecret []byte
	if raw.Safety.ConfirmationSecret != nil {
		secret, err := l.resolveSecret(ctx, *raw.Safety.ConfirmationSecret, "ai.safety.confirmation_secret")
		if err != nil {
			return Result{}, err
		}
		if len(secret) < 32 {
			return Result{}, validation("ai.safety.confirmation_secret", "must contain at least 32 bytes")
		}
		confirmSecret = []byte(secret)
	}

	maxSessions, err := boundedInt(raw.History.MaxSessions, 128, 1, 100000, "ai.history.max_sessions")
	if err != nil {
		return Result{}, err
	}
	maxEvents, err := boundedInt(raw.History.MaxEvents, 256, 1, 100000, "ai.history.max_events_per_session")
	if err != nil {
		return Result{}, err
	}
	maxBytes := raw.History.MaxBytes
	if maxBytes == 0 {
		maxBytes = 2 << 20
	}
	if maxBytes < 1024 || maxBytes > 1<<30 {
		return Result{}, validation("ai.history.max_bytes_per_session", "must be between 1024 and 1073741824")
	}
	historyTTL, err := configuredDuration(raw.History.TTL, 12*time.Hour, 365*24*time.Hour, "ai.history.ttl")
	if err != nil {
		return Result{}, err
	}
	if len(raw.SystemPrompt) > maxPromptBytes || strings.ContainsRune(raw.SystemPrompt, '\x00') {
		return Result{}, validation("ai.system_prompt", fmt.Sprintf("must not contain NUL or exceed %d bytes", maxPromptBytes))
	}

	cfg := chat.Config{
		Provider:             provider,
		BaseURL:              strings.TrimRight(baseURL.String(), "/"),
		APIKey:               apiKey,
		AuthToken:            authToken,
		Model:                model,
		Stream:               stream,
		Thinking:             thinking,
		ThinkingBudgetTokens: thinkingBudget,
		RequestTimeout:       requestTimeout,
		MaxTokens:            maxTokens,
		TemperatureOverride:  temperatureOverride,
		SystemPrompt:         raw.SystemPrompt,
		ConfirmWrites:        confirmWrites,
		MaxIterations:        maxIterations,
		EnableDeepAgent:      raw.Agent.DeepEnabled,
		MaxModelCallsPerTurn: maxCalls,
		MaxSubtasksPerTurn:   maxSubtasks,
		MaxTurnDuration:      turnDuration,
		ConfirmSecret:        confirmSecret,
		AllowModelSelect:     raw.Agent.AllowModelSelect,
		MaxHistorySessions:   maxSessions,
		MaxHistoryEvents:     maxEvents,
		MaxHistoryBytes:      maxBytes,
		HistoryTTL:           historyTTL,
		WritesPerMinute:      writesPerMinute,
	}
	if err := cfg.Validate(); err != nil {
		var fieldErr *chat.ConfigError
		if errors.As(err, &fieldErr) {
			return Result{}, validation("ai."+lowerSnake(fieldErr.Field), fieldErr.Problem)
		}
		return Result{}, validation("ai", "resolved configuration is invalid")
	}
	return Result{Enabled: true, Config: cfg}, nil
}

func (l *Loader) resolveAuth(ctx context.Context, provider string, loopback bool, raw authFileConfig) (string, string, error) {
	authType := strings.ToLower(strings.TrimSpace(raw.Type))
	ref := secretFileRef{Env: raw.Env, File: raw.File, Ref: raw.Ref}
	switch authType {
	case "none":
		if raw.Env != "" || raw.File != "" || raw.Ref != "" {
			return "", "", validation("ai.auth", "type=none cannot include env, file, or ref")
		}
		if !loopback {
			return "", "", validation("ai.auth.type", "none is allowed only for loopback endpoints")
		}
		// The upstream Claude adapter may consult ambient ANTHROPIC_* variables
		// when no explicit credential is supplied. Refuse this combination so a
		// local endpoint can never receive an unrelated process credential.
		if provider == chat.ProviderClaude {
			return "", "", validation("ai.auth.type", "Claude requires explicit api_key or auth_token credentials")
		}
		return "", "", nil
	case "api_key":
		secret, err := l.resolveSecret(ctx, ref, "ai.auth")
		return secret, "", err
	case "auth_token":
		if provider != chat.ProviderClaude {
			return "", "", validation("ai.auth.type", "auth_token is only supported by the Claude provider")
		}
		secret, err := l.resolveSecret(ctx, ref, "ai.auth")
		return "", secret, err
	default:
		return "", "", validation("ai.auth.type", "must be api_key, auth_token, or none")
	}
}

var envNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)

func (l *Loader) resolveSecret(ctx context.Context, raw secretFileRef, path string) (string, error) {
	ref, err := normalizeSecretRef(raw, path)
	if err != nil {
		return "", err
	}
	if l == nil || nilcheck.IsNil(l.secrets) {
		return "", configError(ErrorSecretUnavailable, path, "no SecretResolver was supplied", nil)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	secret, err := l.secrets.ResolveSecret(ctx, ref)
	if err != nil {
		return "", configError(ErrorSecretUnavailable, path, "the referenced secret could not be resolved", err)
	}
	defer clear(secret)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value := string(secret)
	if strings.HasSuffix(value, "\r\n") {
		value = strings.TrimSuffix(value, "\r\n")
	} else if strings.HasSuffix(value, "\n") {
		value = strings.TrimSuffix(value, "\n")
	}
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsFunc(value, unicode.IsControl) {
		return "", configError(ErrorSecretUnavailable, path, "the referenced secret is empty or contains whitespace/control characters", nil)
	}
	return value, nil
}

func normalizeSecretRef(raw secretFileRef, path string) (SecretRef, error) {
	values := []struct {
		kind string
		name string
	}{
		{SecretEnv, strings.TrimSpace(raw.Env)},
		{SecretFile, strings.TrimSpace(raw.File)},
		{SecretRefName, strings.TrimSpace(raw.Ref)},
	}
	var selected SecretRef
	count := 0
	for _, value := range values {
		if value.name != "" {
			selected = SecretRef{Kind: value.kind, Name: value.name}
			count++
		}
	}
	if count != 1 {
		return SecretRef{}, validation(path, "must set exactly one of env, file, or ref")
	}
	switch selected.Kind {
	case SecretEnv:
		if !envNamePattern.MatchString(selected.Name) {
			return SecretRef{}, validation(path+".env", "has an invalid environment variable name")
		}
	case SecretFile:
		if !filepath.IsAbs(selected.Name) {
			return SecretRef{}, validation(path+".file", "must be an absolute path")
		}
		selected.Name = filepath.Clean(selected.Name)
	case SecretRefName:
		if len(selected.Name) > 512 || strings.ContainsFunc(selected.Name, unicode.IsControl) {
			return SecretRef{}, validation(path+".ref", "must be at most 512 non-control characters")
		}
	}
	return selected, nil
}

func validation(path, problem string) error {
	return configError(ErrorValidation, path, problem, nil)
}

func validateBaseURL(raw string) (*url.URL, bool, error) {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	if value == "" || len(value) > 2048 {
		return nil, false, errors.New("must be non-empty and at most 2048 bytes")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, false, errors.New("must be an absolute HTTP or HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, false, errors.New("must not contain userinfo, query, or fragment")
	}
	loopback := strings.EqualFold(parsed.Hostname(), "localhost")
	if ip := net.ParseIP(parsed.Hostname()); ip != nil && ip.IsLoopback() {
		loopback = true
	}
	if parsed.Scheme != "https" && !loopback {
		return nil, false, errors.New("must use HTTPS unless the host is loopback")
	}
	return parsed, loopback, nil
}

func configuredDuration(raw string, def, max time.Duration, path string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return def, nil
	}
	value, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || value <= 0 || value > max {
		return 0, validation(path, fmt.Sprintf("must be a positive Go duration no greater than %s", max))
	}
	return value, nil
}

func boundedInt(value, def, min, max int, path string) (int, error) {
	if value == 0 {
		return def, nil
	}
	if value < min || value > max {
		return 0, validation(path, fmt.Sprintf("must be between %d and %d", min, max))
	}
	return value, nil
}

func lowerSnake(value string) string {
	var out strings.Builder
	for i, r := range value {
		if unicode.IsUpper(r) && i > 0 {
			out.WriteByte('_')
		}
		out.WriteRune(unicode.ToLower(r))
	}
	return out.String()
}

type duplicateKeyError struct{ Path string }

func (e *duplicateKeyError) Error() string { return e.Path + " is duplicated" }

func validateUniqueJSONObject(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') {
		return errors.New("top level must be an object")
	}
	if err := consumeJSONObject(dec, "ai"); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON")
		}
		return err
	}
	return nil
}

func consumeJSONObject(dec *json.Decoder, path string) error {
	seen := make(map[string]struct{})
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("object key is not a string")
		}
		keyPath := path + "." + key
		if _, exists := seen[key]; exists {
			return &duplicateKeyError{Path: keyPath}
		}
		seen[key] = struct{}{}
		if err := consumeJSONValue(dec, keyPath); err != nil {
			return err
		}
	}
	end, err := dec.Token()
	if err != nil {
		return err
	}
	if end != json.Delim('}') {
		return errors.New("object is not terminated")
	}
	return nil
}

func consumeJSONValue(dec *json.Decoder, path string) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		return consumeJSONObject(dec, path)
	case '[':
		for index := 0; dec.More(); index++ {
			if err := consumeJSONValue(dec, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("array is not terminated")
		}
		return nil
	default:
		return errors.New("invalid JSON delimiter")
	}
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func readCheckedRegularFile(path string, maxBytes int64, secret bool) ([]byte, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, nil, errors.New("file must be regular and not a symlink")
	}
	if before.Size() > maxBytes {
		return nil, nil, errors.New("file exceeds size limit")
	}
	if runtime.GOOS != "windows" {
		perm := before.Mode().Perm()
		if secret && perm != 0o600 && perm != 0o400 {
			return nil, nil, errors.New("secret file permissions must be 0600 or 0400")
		}
		if !secret && perm&0o022 != 0 {
			return nil, nil, errors.New("configuration file cannot be writable by group or other")
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return nil, nil, errors.New("file changed while being opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, nil, errors.New("file exceeds size limit")
	}
	return data, after, nil
}

type osSecretResolver struct{}

// NewOSSecretResolver returns an explicit env/file resolver for conventional
// server processes. It is never installed automatically by NewLoader.
func NewOSSecretResolver() SecretResolver { return osSecretResolver{} }

func (osSecretResolver) ResolveSecret(ctx context.Context, ref SecretRef) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch ref.Kind {
	case SecretEnv:
		value, ok := os.LookupEnv(ref.Name)
		if !ok || value == "" {
			return nil, ErrSecretUnavailable
		}
		return []byte(value), nil
	case SecretFile:
		data, _, err := readCheckedRegularFile(ref.Name, maxSecretBytes, true)
		return data, err
	default:
		return nil, ErrSecretUnavailable
	}
}
