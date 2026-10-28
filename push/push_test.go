package push

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testSubscription(t *testing.T) Subscription {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := Subscription{Endpoint: "https://web.push.apple.com/test"}
	s.Keys.Auth = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 16))
	s.Keys.P256dh = base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	return s
}
func TestSenderEncryptsAndValidatesWithoutNetwork(t *testing.T) {
	private, public, err := GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	sender, err := NewSender(Config{PublicKey: public, PrivateKey: private, Subject: "mailto:push@example.com", HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("private test body")) || r.Header.Get("Content-Encoding") != "aes128gcm" || !strings.Contains(r.Header.Get("Authorization"), "vapid") {
			t.Fatal("plaintext or missing standard encryption")
		}
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	code, err := sender.Send(context.Background(), testSubscription(t), Message{Title: "Assistant", Body: "private test body", URL: "/?assistant_conversation=test", Tag: "test"})
	if err != nil || code != 201 || calls != 1 {
		t.Fatal(code, err, calls)
	}
	for _, endpoint := range []string{"http://web.push.apple.com/test", "https://127.0.0.1/test", "https://web.push.apple.com.evil.test/", "https://evil.test/", "https://web.push.apple.com:444/test", "https://u:p@web.push.apple.com/test"} {
		s := testSubscription(t)
		s.Endpoint = endpoint
		if ValidateSubscription(s) == nil {
			t.Fatal("unsafe endpoint accepted", endpoint)
		}
	}
	for _, target := range []string{"https://evil.test", "//evil.test", "javascript:alert(1)"} {
		if _, err := sender.Send(context.Background(), testSubscription(t), Message{URL: target}); err == nil {
			t.Fatal("unsafe navigation accepted")
		}
	}
	_, other, _ := GenerateKeys()
	if _, err := NewSender(Config{PublicKey: other, PrivateKey: private, Subject: "mailto:push@example.com"}); err == nil {
		t.Fatal("mismatched pair accepted")
	}
	if calls != 1 {
		t.Fatal("validation issued network calls", calls)
	}
}
