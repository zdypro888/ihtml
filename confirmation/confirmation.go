// Package confirmation signs and atomically consumes existing ihtml confirmation
// cards. It does not authorize or execute business operations. Hosts must derive
// user and Scope from trusted state and recheck permissions before execution.
package confirmation

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/internal/nilcheck"
)

const (
	ToolProtocolVersion    = "ihtml.semantic-tools.confirmation.v2"
	TTL                    = 15 * time.Minute
	DefaultMaxMemoryNonces = 65_536
)

// Scope is the trusted logical session and capability boundary, not client input.
type Scope struct {
	SessionID           string `json:"session_id"`
	CapabilityScope     string `json:"capability_scope"`
	ToolProtocolVersion string `json:"tool_protocol_version"`
}

func NewScope(sessionID, capabilityScope string) Scope {
	return Scope{sessionID, capabilityScope, ToolProtocolVersion}
}

func (s Scope) Valid() bool {
	return s.SessionID != "" && s.CapabilityScope != "" && s.ToolProtocolVersion != ""
}

// NonceStore must atomically delete and return true only for an existing,
// unexpired nonce. Shared deployments need a shared store. Implementations must
// honor the supplied context; calls are synchronous and no goroutines are hidden.
type NonceStore interface {
	Put(context.Context, string, time.Time) error
	Consume(context.Context, string, time.Time) (bool, error)
}

type Config struct {
	// Secret is SHA-256 derived exactly as in chat. Empty generates a random key.
	Secret []byte
	// NonceStore defaults to bounded process-local memory. Explicit typed nil is invalid.
	NonceStore NonceStore
	// MaxMemoryNonces applies only to the default store; zero uses the default.
	MaxMemoryNonces int
}

type Signer struct {
	key    []byte
	nonces NonceStore
}

// Available reports constructor validity without invoking the external store.
func (s *Signer) Available() bool { return s != nil && len(s.key) > 0 && !nilcheck.IsNil(s.nonces) }

func NewChecked(cfg Config) (*Signer, error) {
	if cfg.NonceStore != nil && nilcheck.IsNil(cfg.NonceStore) {
		return nil, errors.New("confirmation nonce store is typed nil")
	}
	if cfg.MaxMemoryNonces < 0 || (cfg.NonceStore != nil && cfg.MaxMemoryNonces != 0) {
		return nil, errors.New("invalid confirmation memory nonce capacity")
	}
	s := &Signer{nonces: cfg.NonceStore}
	if s.nonces == nil {
		s.nonces = NewMemoryNonceStore(cfg.MaxMemoryNonces)
	}
	if len(cfg.Secret) > 0 {
		sum := sha256.Sum256(cfg.Secret)
		s.key = append([]byte(nil), sum[:]...)
	} else {
		s.key = make([]byte, 32)
		if _, err := rand.Read(s.key); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// CanonicalCall preserves the legacy chat JSON field order and names.
// These fields, the protocol constant, and CanonicalBody define the wire signature.
type CanonicalCall struct {
	Scope        Scope           `json:"scope"`
	ID           string          `json:"id"`
	User         string          `json:"user"`
	Name         string          `json:"name"`
	Title        string          `json:"title"`
	Method       string          `json:"method"`
	Path         string          `json:"path"`
	Query        json.RawMessage `json:"query"`
	Body         json.RawMessage `json:"body"`
	Risk         string          `json:"risk"`
	Confirmation bool            `json:"confirmation"`
	Reason       string          `json:"reason"`
	AgentMode    string          `json:"agent_mode"`
	ResumeMode   string          `json:"resume_mode"`
	Nonce        string          `json:"nonce"`
	Expires      int64           `json:"expires"`
}

// CanonicalBody retains legacy JSON number normalization and null fallback.
// Prepare rejects invalid JSON instead of signing a fallback for an invalid body.
func CanonicalBody(body any) json.RawMessage {
	raw, err := json.Marshal(body)
	if err != nil {
		return json.RawMessage("null")
	}
	var normalized any
	if json.Unmarshal(raw, &normalized) != nil {
		return json.RawMessage("null")
	}
	out, err := json.Marshal(normalized)
	if err != nil {
		return json.RawMessage("null")
	}
	return out
}

// Sign is a low-level compatibility helper. It does not register a nonce or
// check expiry, identity, or scope; use Prepare/Consume/Revoke for cards.
func (s *Signer) Sign(user string, scope Scope, call ihtml.ProposedToolCall) string {
	if s == nil || len(s.key) == 0 {
		return ""
	}
	payload, _ := json.Marshal(CanonicalCall{
		Scope: scope, ID: call.ID, User: user, Name: call.Name, Title: call.Title,
		Method: call.Method, Path: call.Path, Query: CanonicalBody(call.Query),
		Body: CanonicalBody(call.Body), Risk: call.Risk, Confirmation: call.RequiresConfirmation,
		Reason: call.Reason, AgentMode: call.AgentMode, ResumeMode: call.ApprovalResume,
		Nonce: call.ConfirmationNonce, Expires: call.ConfirmationExpires,
	})
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Signer) check(ctx context.Context, user string, scope Scope) error {
	if ctx == nil {
		return errors.New("confirmation context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || len(s.key) == 0 || nilcheck.IsNil(s.nonces) {
		return errors.New("confirmation signer is unavailable")
	}
	if user == "" {
		return errors.New("confirmation user is required")
	}
	if !scope.Valid() {
		return errors.New("confirmation scope is unavailable")
	}
	return nil
}

// Prepare returns an independently owned JSON snapshot. Callers must not mutate
// the input while this synchronous method reads it. No card is returned if Put fails.
func (s *Signer) Prepare(ctx context.Context, user string, scope Scope, call ihtml.ProposedToolCall) (out ihtml.ProposedToolCall, err error) {
	defer func() {
		if recover() != nil {
			out = ihtml.ProposedToolCall{}
			err = errors.New("confirmation preparation callback panicked")
		}
	}()
	if err = s.check(ctx, user, scope); err != nil {
		return out, err
	}
	// Round-trip only mutable payloads so all legacy signed scalar fields survive.
	raw, err := json.Marshal(call.Query)
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(raw, &out.Query); err != nil {
		return ihtml.ProposedToolCall{}, err
	}
	query := out.Query
	raw, err = json.Marshal(call.Body)
	if err != nil {
		return ihtml.ProposedToolCall{}, err
	}
	var body any
	if err = json.Unmarshal(raw, &body); err != nil {
		return ihtml.ProposedToolCall{}, err
	}
	out = call
	out.Query, out.Body = query, body
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return ihtml.ProposedToolCall{}, err
	}
	out.ConfirmationNonce = hex.EncodeToString(nonce)
	out.ConfirmationExpires = time.Now().Add(TTL).Unix()
	out.ConfirmationToken = s.Sign(user, scope, out)
	if err = ctx.Err(); err != nil {
		return ihtml.ProposedToolCall{}, err
	}
	if err = s.nonces.Put(ctx, out.ConfirmationNonce, time.Unix(out.ConfirmationExpires, 0)); err != nil {
		return ihtml.ProposedToolCall{}, err
	}
	if err = ctx.Err(); err != nil {
		return ihtml.ProposedToolCall{}, err
	}
	return out, nil
}

// Consume verifies the exact signed card before the atomic store boundary.
// Invalid, expired, and replayed cards return false without a validation error.
// A store error is fail-closed; it may still have consumed the nonce, so retry
// must never be interpreted as permission to execute without a successful CAS.
func (s *Signer) Consume(ctx context.Context, user string, scope Scope, call ihtml.ProposedToolCall) (ok bool, err error) {
	defer func() {
		if recover() != nil {
			ok = false
			err = errors.New("confirmation consumption callback panicked")
		}
	}()
	if err = s.check(ctx, user, scope); err != nil {
		return false, err
	}
	if call.ConfirmationToken == "" || call.ConfirmationNonce == "" {
		return false, nil
	}
	now := time.Now()
	if call.ConfirmationExpires <= now.Unix() {
		return false, nil
	}
	if !hmac.Equal([]byte(s.Sign(user, scope, call)), []byte(call.ConfirmationToken)) {
		return false, nil
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	ok, err = s.nonces.Consume(ctx, call.ConfirmationNonce, now)
	if err != nil {
		return false, err
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	return ok, nil
}

// Revoke consumes the same nonce without executing anything. Confirmation and
// cancellation race through the same atomic boundary; at most one succeeds.
func (s *Signer) Revoke(ctx context.Context, user string, scope Scope, call ihtml.ProposedToolCall) (bool, error) {
	return s.Consume(ctx, user, scope, call)
}
