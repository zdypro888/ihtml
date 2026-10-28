// Package push 提供可选浏览器 Web Push 传输；订阅归属、权限、持久回执和重试由宿主负责。
package push

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	webpush "github.com/SherClockHolmes/webpush-go"
)

type Subscription = webpush.Subscription

type Config struct {
	PublicKey  string
	PrivateKey string
	Subject    string
	HTTPClient *http.Client
}
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Tag   string `json:"tag"`
}
type Sender struct {
	cfg    Config
	client *http.Client
}

// GenerateKeys 仅生成调用方显式保存的 VAPID 密钥，不读取凭据文件或自动注册订阅。
func GenerateKeys() (string, string, error) { return webpush.GenerateVAPIDKeys() }

func NewSender(cfg Config) (*Sender, error) {
	private, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(cfg.PrivateKey, "="))
	if err != nil || len(private) != 32 {
		return nil, errors.New("push: invalid private key")
	}
	public, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(cfg.PublicKey, "="))
	if err != nil || len(public) != 65 {
		return nil, errors.New("push: invalid public key")
	}
	x, _ := elliptic.Unmarshal(elliptic.P256(), public)
	if x == nil {
		return nil, errors.New("push: invalid public key")
	}
	key, keyErr := ecdh.P256().NewPrivateKey(private)
	if keyErr != nil || !bytes.Equal(key.PublicKey().Bytes(), public) {
		return nil, errors.New("push: mismatched VAPID keys")
	}
	if !strings.HasPrefix(cfg.Subject, "mailto:") && !strings.HasPrefix(cfg.Subject, "https://") {
		return nil, errors.New("push: a contact subject is required")
	}
	client := http.Client{Timeout: 15 * time.Second}
	if cfg.HTTPClient != nil {
		client = *cfg.HTTPClient
	}
	if client.Timeout <= 0 {
		client.Timeout = 15 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Sender{cfg: cfg, client: &client}, nil
}

// ValidateSubscription 限定主流浏览器的 HTTPS 推送服务，避免用户端点成为任意网络请求入口。
func ValidateSubscription(subscription Subscription) error {
	endpoint, err := url.Parse(subscription.Endpoint)
	if err != nil || len(subscription.Endpoint) > 4096 || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Fragment != "" || (endpoint.Port() != "" && endpoint.Port() != "443") {
		return errors.New("push: invalid endpoint")
	}
	host := strings.ToLower(endpoint.Hostname())
	allowed := host == "fcm.googleapis.com" || host == "fcm.google.com" || host == "web.push.apple.com" || strings.HasSuffix(host, ".push.services.mozilla.com") || host == "updates.push.services.mozilla.com" || strings.HasSuffix(host, ".notify.windows.com") || strings.HasSuffix(host, ".notify.microsoft.com")
	if !allowed {
		return errors.New("push: unsupported browser push service")
	}
	auth, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(subscription.Keys.Auth, "="))
	if err != nil || len(auth) != 16 {
		return errors.New("push: invalid subscription key")
	}
	public, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(subscription.Keys.P256dh, "="))
	if err != nil || len(public) != 65 {
		return errors.New("push: invalid subscription key")
	}
	x, _ := elliptic.Unmarshal(elliptic.P256(), public)
	if x == nil {
		return errors.New("push: invalid subscription key")
	}
	return nil
}

// Send 复用标准 Web Push 加密库；通知只能打开同源相对路径，不能承载自动业务操作。
// 返回 HTTP 状态供宿主持久化投递结果；410/404 应移除订阅，暂时失败由宿主原任务重试。
func (s *Sender) Send(ctx context.Context, subscription Subscription, message Message) (int, error) {
	if err := ValidateSubscription(subscription); err != nil {
		return 0, err
	}
	target, err := url.Parse(message.URL)
	if err != nil || target.IsAbs() || target.Host != "" || !strings.HasPrefix(message.URL, "/") || strings.HasPrefix(message.URL, "//") {
		return 0, errors.New("push: notification target must be same-origin")
	}
	if !utf8.ValidString(message.Title) || !utf8.ValidString(message.Body) || len(message.Title) > 256 || len(message.Body) > 1024 || len(message.Tag) > 128 || len(message.URL) > 2048 {
		return 0, errors.New("push: notification exceeds public display limits")
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return 0, err
	}
	response, err := webpush.SendNotificationWithContext(ctx, payload, &subscription, &webpush.Options{HTTPClient: s.client, Subscriber: s.cfg.Subject, VAPIDPublicKey: s.cfg.PublicKey, VAPIDPrivateKey: s.cfg.PrivateKey, TTL: 3600, Urgency: webpush.UrgencyNormal})
	if err != nil {
		return 0, errors.New("push: delivery unavailable")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return response.StatusCode, nil
}
