package chat

import (
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProviderHTTPClientEnforcesResponseBodyLimit(t *testing.T) {
	const limit = int64(1 << 20)
	for name, size := range map[string]int64{"exact": limit, "oversized": limit + 1} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.CopyN(w, strings.NewReader(strings.Repeat("x", int(size))), size)
			}))
			defer server.Close()

			resp, err := newProviderHTTPClient(server.URL, 0, nil, limit).Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, readErr := io.ReadAll(resp.Body)
			if name == "exact" {
				if readErr != nil || int64(len(body)) != limit {
					t.Fatalf("exact body bytes=%d err=%v", len(body), readErr)
				}
				return
			}
			if int64(len(body)) != limit {
				t.Fatalf("oversized body retained %d bytes, want %d", len(body), limit)
			}
			if !errors.Is(readErr, ErrModelOutputLimit) {
				t.Fatalf("oversized body error = %v", readErr)
			}
			var typed *ModelOutputLimitError
			if !errors.As(readErr, &typed) || typed.Scope != ModelOutputLimitProviderResponseBytes || typed.Limit != limit {
				t.Fatalf("typed error = %#v", typed)
			}
		})
	}
}

func TestProviderClientsDoNotInheritAmbientCookieJar(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultClient.Jar
	http.DefaultClient.Jar = jar
	t.Cleanup(func() { http.DefaultClient.Jar = previous })

	if client := newProviderHTTPClient("https://provider.example/v1", 0, nil); client.Jar != nil {
		t.Fatal("generation client inherited http.DefaultClient cookie jar")
	}
	origin, err := url.Parse("https://provider.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	if client := modelDiscoveryClient(origin); client.Jar != nil {
		t.Fatal("model discovery client inherited http.DefaultClient cookie jar")
	}
}

func TestProviderHTTPClientRejectsCrossOriginRedirect(t *testing.T) {
	var targetRequests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests.Add(1)
		if got := r.Header.Get("X-Api-Key"); got != "" {
			t.Errorf("redirect target received provider credential %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	req, err := http.NewRequest(http.MethodPost, source.URL+"/v1/messages", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Api-Key", "provider-secret")
	resp, err := newProviderHTTPClient(source.URL+"/v1", 0, nil).Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("credential-bearing provider request followed a cross-origin redirect")
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("redirect target requests = %d", targetRequests.Load())
	}
}

func TestProviderHTTPClientAllowsSameOriginSameMethodRedirect(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/messages":
			http.Redirect(w, r, server.URL+"/v1/region/messages", http.StatusTemporaryRedirect)
		case "/v1/region/messages":
			if r.Method != http.MethodPost {
				t.Errorf("redirect method = %q", r.Method)
			}
			if got := r.Header.Get("X-Api-Key"); got != "provider-secret" {
				t.Errorf("same-origin credential = %q", got)
			}
			_, _ = io.WriteString(w, "ok")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/messages", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Api-Key", "provider-secret")
	resp, err := newProviderHTTPClient(server.URL+"/v1", 0, nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q", body)
	}
}

func TestProviderHTTPClientRejectsMethodChangingRedirect(t *testing.T) {
	var changedMethodRequests atomic.Int64
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/changed" {
			changedMethodRequests.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, server.URL+"/changed", http.StatusFound)
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := newProviderHTTPClient(server.URL+"/v1", 0, nil).Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("provider request accepted a method-changing redirect")
	}
	if changedMethodRequests.Load() != 0 {
		t.Fatalf("method-changing target requests = %d", changedMethodRequests.Load())
	}
}
