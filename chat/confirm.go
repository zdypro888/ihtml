package chat

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/zdypro888/ihtml"
	"github.com/zdypro888/ihtml/internal/nilcheck"
)

const (
	confirmationTTL                   = 15 * time.Minute
	defaultMaxMemoryConfirmNonces     = 65_536
	memoryConfirmNonceCleanupInterval = time.Minute
)

// confirmationToolProtocolVersion is intentionally independent from the
// additive websocket protocol version. Bump it whenever the signed semantic
// tool contract or confirmation authorization boundary changes; cards issued
// under an older contract must never become executable under the new one.
const confirmationToolProtocolVersion = "ihtml.semantic-tools.confirmation.v2"

// confirmationScope binds a confirmation card to the logical chat session and
// the exact host capability boundary that issued it. It is never supplied by
// the browser: NewSession derives it from trusted server-side state and copies
// it into every runtime used to issue, confirm, or revoke a card.
type confirmationScope struct {
	SessionID           string `json:"session_id"`
	CapabilityScope     string `json:"capability_scope"`
	ToolProtocolVersion string `json:"tool_protocol_version"`
}

func newConfirmationScope(sessionID, capabilityScope string) confirmationScope {
	return confirmationScope{
		SessionID:           sessionID,
		CapabilityScope:     capabilityScope,
		ToolProtocolVersion: confirmationToolProtocolVersion,
	}
}

func (s confirmationScope) valid() bool {
	return s.SessionID != "" && s.CapabilityScope != "" && s.ToolProtocolVersion != ""
}

// ConfirmNonceStore 持久化确认卡的一次性 nonce。Consume 必须以原子方式做到
// “存在且未过期则删除并返回 true”，从而在多实例并发确认时仍最多执行一次。
// 多副本部署应使用共享数据库/缓存实现；默认实现仅在当前进程内有效。
type ConfirmNonceStore interface {
	Put(ctx context.Context, nonce string, expiresAt time.Time) error
	Consume(ctx context.Context, nonce string, now time.Time) (bool, error)
}

// confirmSigner 给"需人工确认"的工具卡做 HMAC 签名与一次性核销:
// 前端确认时原样带回卡片, 内容被改/过期/重放都拒绝执行。
type confirmSigner struct {
	key    []byte
	keyErr error
	nonces ConfirmNonceStore
}

// newConfirmSigner 创建签名器。secret 非空时派生固定密钥；否则每进程随机。
// nonceStore 省略或为 nil 时使用进程内存实现。
func newConfirmSigner(secret []byte, nonceStores ...ConfirmNonceStore) *confirmSigner {
	var key []byte
	if len(secret) > 0 {
		sum := sha256.Sum256(secret)
		key = sum[:]
	} else {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			key = nil
			return &confirmSigner{keyErr: err, nonces: pickConfirmNonceStore(nonceStores)}
		}
	}
	return &confirmSigner{key: key, nonces: pickConfirmNonceStore(nonceStores)}
}

func pickConfirmNonceStore(nonceStores []ConfirmNonceStore) ConfirmNonceStore {
	var nonceStore ConfirmNonceStore
	if len(nonceStores) > 0 {
		nonceStore = nonceStores[0]
	}
	if nilcheck.IsNil(nonceStore) {
		nonceStore = newMemoryConfirmNonceStore()
	}
	return nonceStore
}

// canonicalCall 是参与签名的规范化字段。Body 经 map 反序列化再序列化,
// Go 对 map 键排序, 两端得到确定字节序。
type canonicalCall struct {
	Scope        confirmationScope `json:"scope"`
	ID           string            `json:"id"`
	User         string            `json:"user"`
	Name         string            `json:"name"`
	Title        string            `json:"title"`
	Method       string            `json:"method"`
	Path         string            `json:"path"`
	Query        json.RawMessage   `json:"query"`
	Body         json.RawMessage   `json:"body"`
	Risk         string            `json:"risk"`
	Confirmation bool              `json:"confirmation"`
	Reason       string            `json:"reason"`
	AgentMode    string            `json:"agent_mode"`
	ResumeMode   string            `json:"resume_mode"`
	Nonce        string            `json:"nonce"`
	Expires      int64             `json:"expires"`
}

func canonicalBody(body any) json.RawMessage {
	raw, err := json.Marshal(body)
	if err != nil {
		return json.RawMessage("null")
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return json.RawMessage("null")
	}
	out, err := json.Marshal(normalized)
	if err != nil {
		return json.RawMessage("null")
	}
	return out
}

func (s *confirmSigner) sign(user string, scope confirmationScope, call ihtml.ProposedToolCall) string {
	payload, _ := json.Marshal(canonicalCall{
		Scope:        scope,
		ID:           call.ID,
		User:         user,
		Name:         call.Name,
		Title:        call.Title,
		Method:       call.Method,
		Path:         call.Path,
		Query:        canonicalBody(call.Query),
		Body:         canonicalBody(call.Body),
		Risk:         call.Risk,
		Confirmation: call.RequiresConfirmation,
		Reason:       call.Reason,
		AgentMode:    call.AgentMode,
		ResumeMode:   call.ApprovalResume,
		Nonce:        call.ConfirmationNonce,
		Expires:      call.ConfirmationExpires,
	})
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// prepareContext 签发确认卡: 填 nonce/expires/token 并持久登记 nonce。
func (s *confirmSigner) prepareContext(ctx context.Context, user string, scope confirmationScope, call ihtml.ProposedToolCall) (ihtml.ProposedToolCall, error) {
	if s == nil || s.keyErr != nil || len(s.key) == 0 {
		if s != nil && s.keyErr != nil {
			return call, s.keyErr
		}
		return call, errors.New("confirmation signer is unavailable")
	}
	if !scope.valid() {
		return call, errors.New("confirmation scope is unavailable")
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return call, err
	}
	call.ConfirmationNonce = hex.EncodeToString(nonce)
	call.ConfirmationExpires = time.Now().Add(confirmationTTL).Unix()
	call.ConfirmationToken = s.sign(user, scope, call)
	if err := s.nonces.Put(ctx, call.ConfirmationNonce, time.Unix(call.ConfirmationExpires, 0)); err != nil {
		return ihtml.ProposedToolCall{}, err
	}
	return call, nil
}

// consumeContext 验签并原子核销。false 表示卡片无效(改动/过期/重放)。
func (s *confirmSigner) consumeContext(ctx context.Context, user string, scope confirmationScope, call ihtml.ProposedToolCall) (bool, error) {
	if s == nil || s.keyErr != nil || len(s.key) == 0 {
		if s != nil && s.keyErr != nil {
			return false, s.keyErr
		}
		return false, errors.New("confirmation signer is unavailable")
	}
	if !scope.valid() {
		return false, errors.New("confirmation scope is unavailable")
	}
	if call.ConfirmationToken == "" || call.ConfirmationNonce == "" {
		return false, nil
	}
	now := time.Now()
	if call.ConfirmationExpires <= now.Unix() {
		return false, nil
	}
	expected := s.sign(user, scope, call)
	if !hmac.Equal([]byte(expected), []byte(call.ConfirmationToken)) {
		return false, nil
	}
	return s.nonces.Consume(ctx, call.ConfirmationNonce, now)
}

// consume 保留给包内兼容调用。
func (s *confirmSigner) consume(user string, scope confirmationScope, call ihtml.ProposedToolCall) bool {
	ok, _ := s.consumeContext(context.Background(), user, scope, call)
	return ok
}

// revokeContext validates the exact same signed card as confirmation and then
// atomically consumes its nonce without executing the tool. Confirm and cancel
// therefore race through one CAS boundary and at most one can succeed.
func (s *confirmSigner) revokeContext(ctx context.Context, user string, scope confirmationScope, call ihtml.ProposedToolCall) (bool, error) {
	return s.consumeContext(ctx, user, scope, call)
}

type memoryConfirmNonceStore struct {
	mu          sync.Mutex
	nonces      map[string]time.Time
	maxNonces   int
	nextCleanup time.Time
}

func newMemoryConfirmNonceStore(maxNonces ...int) *memoryConfirmNonceStore {
	limit := defaultMaxMemoryConfirmNonces
	if len(maxNonces) > 0 && maxNonces[0] > 0 {
		limit = maxNonces[0]
	}
	return &memoryConfirmNonceStore{nonces: make(map[string]time.Time), maxNonces: limit}
}

func (s *memoryConfirmNonceStore) cleanupExpiredLocked(now time.Time) {
	for key, expiry := range s.nonces {
		if !expiry.After(now) {
			delete(s.nonces, key)
		}
	}
	s.nextCleanup = now.Add(memoryConfirmNonceCleanupInterval)
}

func (s *memoryConfirmNonceStore) Put(ctx context.Context, nonce string, expiresAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if nonce == "" {
		return errors.New("confirmation nonce is required")
	}
	now := time.Now()
	if !expiresAt.After(now) {
		return errors.New("confirmation nonce expiry must be in the future")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, exists := s.nonces[nonce]; exists {
		return errors.New("confirmation nonce already exists")
	}
	// Avoid an O(n) sweep on every issuance, including repeated attempts while
	// the store is full. At most one sweep runs per interval; a full store fails
	// closed until that scheduled pass can reclaim expired cards rather than
	// evicting a still-valid card or growing forever.
	if s.nextCleanup.IsZero() || !now.Before(s.nextCleanup) {
		s.cleanupExpiredLocked(now)
	}
	if len(s.nonces) >= s.maxNonces {
		return errors.New("confirmation nonce store capacity exceeded")
	}
	s.nonces[nonce] = expiresAt
	return nil
}

func (s *memoryConfirmNonceStore) Consume(ctx context.Context, nonce string, now time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expiresAt, ok := s.nonces[nonce]
	if !ok || !expiresAt.After(now) {
		delete(s.nonces, nonce)
		return false, nil
	}
	delete(s.nonces, nonce)
	return true, nil
}
