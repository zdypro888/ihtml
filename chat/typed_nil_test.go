package chat

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

type nilSessionStore struct{}

func (*nilSessionStore) LoadEvents(context.Context, string, *adk.LoadSessionEventsRequest) (*adk.LoadSessionEventsResult[*schema.Message], error) {
	panic("typed nil session store was not normalized")
}

func (*nilSessionStore) AppendEvents(context.Context, string, []*adk.SessionEvent[*schema.Message]) error {
	panic("typed nil session store was not normalized")
}

type nilNonceStore struct{}

func (*nilNonceStore) Put(context.Context, string, time.Time) error {
	panic("typed nil nonce store was not normalized")
}

func (*nilNonceStore) Consume(context.Context, string, time.Time) (bool, error) {
	panic("typed nil nonce store was not normalized")
}

type nilRoundTripper struct{}

func (*nilRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	panic("typed nil transport was not normalized")
}

type nilCookieJar struct{}

func (*nilCookieJar) SetCookies(*url.URL, []*http.Cookie) {
	panic("typed nil cookie jar was not normalized")
}

func (*nilCookieJar) Cookies(*url.URL) []*http.Cookie {
	panic("typed nil cookie jar was not normalized")
}

func TestUncheckedNewNormalizesTypedNilOptionalDependencies(t *testing.T) {
	var sessions *nilSessionStore
	var nonces *nilNonceStore
	var transport *nilRoundTripper
	var jar *nilCookieJar
	b := New(Config{
		SessionStore:      sessions,
		ConfirmNonceStore: nonces,
		ProbeHTTPClient:   &http.Client{Transport: transport, Jar: jar},
	})
	if b.history == nil || b.store != b.history {
		t.Fatalf("typed-nil SessionStore did not select bounded default: store=%T history=%p", b.store, b.history)
	}
	if _, ok := b.signer.nonces.(*memoryConfirmNonceStore); !ok {
		t.Fatalf("typed-nil ConfirmNonceStore did not select memory default: %T", b.signer.nonces)
	}
	if b.cfg.ProbeHTTPClient == nil || b.cfg.ProbeHTTPClient.Transport != nil || b.cfg.ProbeHTTPClient.Jar != nil {
		t.Fatalf("typed-nil nested HTTP dependencies were not normalized: %#v", b.cfg.ProbeHTTPClient)
	}
}

func TestConfirmSignerNormalizesTypedNilNonceStore(t *testing.T) {
	var nonces *nilNonceStore
	signer := newConfirmSigner([]byte("a sufficiently long fixed secret for tests"), nonces)
	if _, ok := signer.nonces.(*memoryConfirmNonceStore); !ok {
		t.Fatalf("typed-nil nonce store did not select memory default: %T", signer.nonces)
	}
}
