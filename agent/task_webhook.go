package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
)

// TaskWebhook 是默认未挂载的可信输入适配；作用域/源/事件类型及验签实现均由宿主固定。
// 原子事件键/指纹兼作 durable inbox，只有 Publish 确认持久后才返回 202。
type TaskWebhook struct {
	Publisher                  TaskEventPublisher
	Scope, SourceID, EventType string
	Verify                     func(context.Context, *http.Request, []byte) error
}

func (h TaskWebhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.Verify == nil || h.Scope == "" || h.SourceID == "" || h.EventType == "" {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	if err != nil || len(body) > 8192 {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	if h.Verify(r.Context(), r, body) != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 256 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	_, err = h.Publisher.Publish(r.Context(), h.Scope, h.SourceID, TaskExternalEvent{Key: key, Type: h.EventType, Payload: body})
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, ErrTaskConflict) {
			status = http.StatusConflict
		}
		if errors.Is(err, ErrTaskEventClosed) || errors.Is(err, ErrTaskNotFound) {
			status = http.StatusGone
		}
		w.WriteHeader(status)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// VerifyTaskWebhookHMAC 签名 method、escaped path、时间戳、幂等键和原正文（逐项以换行分隔）。
// 幂等键必须被签名，防止窃取原请求后换键重复注入；密钥来自宿主配置，不自行创建密钥文件。
func VerifyTaskWebhookHMAC(secret []byte, maxAge time.Duration) func(context.Context, *http.Request, []byte) error {
	key := append([]byte(nil), secret...)
	return func(ctx context.Context, r *http.Request, body []byte) error {
		if len(key) < 32 || maxAge <= 0 || ctx.Err() != nil {
			return ErrTaskNotFound
		}
		raw := r.Header.Get("X-Event-Timestamp")
		unix, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return ErrTaskNotFound
		}
		delta := time.Since(time.Unix(unix, 0))
		if delta > maxAge || delta < -maxAge {
			return ErrTaskNotFound
		}
		supplied, err := hex.DecodeString(r.Header.Get("X-Event-Signature"))
		if err != nil {
			return ErrTaskNotFound
		}
		mac := hmac.New(sha256.New, key)
		for _, field := range []string{r.Method, r.URL.EscapedPath(), raw, r.Header.Get("Idempotency-Key")} {
			mac.Write([]byte(field))
			mac.Write([]byte("\n"))
		}
		mac.Write(body)
		if !hmac.Equal(supplied, mac.Sum(nil)) {
			return ErrTaskNotFound
		}
		return nil
	}
}
