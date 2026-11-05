package ihtml

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 默认和仅配置容量的宿主均能连续处理正常写入，不会隐式获得分钟级限流。
func TestDefaultTrafficRateLimitsAreOptIn(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "default"
		if partial {
			name = "capacity-only"
		}
		t.Run(name, func(t *testing.T) {
			opts := []Option{WithUserResolver(DevelopmentUserResolver)}
			if partial {
				opts = append(opts, WithLimits(Limits{MaxWSPerUser: 2}))
			}
			h := New(NewMemoryStore(), opts...)
			t.Cleanup(func() { h.Close() })
			server := httptest.NewServer(h)
			defer server.Close()
			for i := 0; i < 250; i++ {
				status, _ := doJSON(t, http.MethodPut, server.URL+"/api/items?uid=owner", `[{"id":"same","type":"css","content":"body{}"}]`)
				if status != http.StatusOK {
					t.Fatalf("default burst blocked at %d: %d", i, status)
				}
			}
			if h.opts.limits.ChatTurnsPerMinute != 0 || h.opts.limits.WritesPerMinute != 0 || h.opts.limits.MaxWSPerUser <= 0 {
				t.Fatal("frequency must be opt-in while capacity stays bounded", h.opts.limits)
			}
		})
	}
}
