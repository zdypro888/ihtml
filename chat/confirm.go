package chat

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/zdypro888/ihtml"
)

const confirmationTTL = 15 * time.Minute

// confirmSigner 给"需人工确认"的工具卡做 HMAC 签名与一次性核销:
// 前端确认时原样带回卡片, 内容被改/过期/重放都拒绝执行。
type confirmSigner struct {
	key []byte

	mu     sync.Mutex
	nonces map[string]int64 // nonce → 过期时间(unix 秒)
}

// newConfirmSigner 创建签名器。secret 非空时派生固定密钥(滚动发布不失效),
// 否则每进程随机。
func newConfirmSigner(secret []byte) *confirmSigner {
	var key []byte
	if len(secret) > 0 {
		sum := sha256.Sum256(secret)
		key = sum[:]
	} else {
		key = make([]byte, 32)
		_, _ = rand.Read(key)
	}
	return &confirmSigner{key: key, nonces: make(map[string]int64)}
}

// canonicalCall 是参与签名的规范化字段。Body 经 map 反序列化再序列化,
// Go 对 map 键排序, 两端得到确定字节序。
type canonicalCall struct {
	User    string          `json:"user"`
	Name    string          `json:"name"`
	Method  string          `json:"method"`
	Path    string          `json:"path"`
	Body    json.RawMessage `json:"body"`
	Nonce   string          `json:"nonce"`
	Expires int64           `json:"expires"`
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

func (s *confirmSigner) sign(user string, call ihtml.ProposedToolCall) string {
	payload, _ := json.Marshal(canonicalCall{
		User:    user,
		Name:    call.Name,
		Method:  call.Method,
		Path:    call.Path,
		Body:    canonicalBody(call.Body),
		Nonce:   call.ConfirmationNonce,
		Expires: call.ConfirmationExpires,
	})
	mac := hmac.New(sha256.New, s.key)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// prepare 签发确认卡: 填 nonce/expires/token 并登记 nonce。
func (s *confirmSigner) prepare(user string, call ihtml.ProposedToolCall) ihtml.ProposedToolCall {
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	call.ConfirmationNonce = hex.EncodeToString(nonce)
	call.ConfirmationExpires = time.Now().Add(confirmationTTL).Unix()
	call.ConfirmationToken = s.sign(user, call)

	s.mu.Lock()
	now := time.Now().Unix()
	for n, exp := range s.nonces { // 顺手清理过期
		if exp < now {
			delete(s.nonces, n)
		}
	}
	s.nonces[call.ConfirmationNonce] = call.ConfirmationExpires
	s.mu.Unlock()
	return call
}

// consume 验签并一次性核销。返回 false 表示卡片无效(改动/过期/重放)。
func (s *confirmSigner) consume(user string, call ihtml.ProposedToolCall) bool {
	if call.ConfirmationToken == "" || call.ConfirmationNonce == "" {
		return false
	}
	if call.ConfirmationExpires < time.Now().Unix() {
		return false
	}
	expected := s.sign(user, call)
	if !hmac.Equal([]byte(expected), []byte(call.ConfirmationToken)) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.nonces[call.ConfirmationNonce]
	if !ok || exp < time.Now().Unix() {
		return false
	}
	delete(s.nonces, call.ConfirmationNonce)
	return true
}
