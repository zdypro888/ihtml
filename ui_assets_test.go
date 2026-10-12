package ihtml

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUIAssetsHTTP(t *testing.T) {
	h := UIAssets()
	for _, path := range []string{"/ihtml.js", "/ihtml.css"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 200 || w.Header().Get("ETag") == "" || w.Header().Get("Cache-Control") != "no-cache" || w.Body.Len() == 0 {
			t.Fatalf("asset %s: %d", path, w.Code)
		}
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("If-None-Match", w.Header().Get("ETag"))
		cached := httptest.NewRecorder()
		h.ServeHTTP(cached, r)
		if cached.Code != 304 || cached.Body.Len() != 0 {
			t.Fatal("asset cache contract")
		}
	}
	for _, path := range []string{"/", "/api/config", "/api/items", "/api/chat/ws"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{}`)))
		if w.Code != 404 {
			t.Fatalf("assets exposed service API %s: %d", path, w.Code)
		}
	}
}
