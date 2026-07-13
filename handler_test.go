package ihtml

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, opts ...Option) (*httptest.Server, *Handler) {
	t.Helper()
	allOpts := []Option{WithUserResolver(DevelopmentUserResolver)}
	allOpts = append(allOpts, opts...)
	h := New(NewMemoryStore(), allOpts...)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, h
}

func doJSON(t *testing.T, method, url string, body string) (int, map[string]any) {
	t.Helper()
	var req *http.Request
	var err error
	if body != "" {
		req, err = http.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequest(method, url, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestHandlerPageAndAssets(t *testing.T) {
	srv, _ := newTestServer(t, WithPageTitle("我的<页面>"))
	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf := new(strings.Builder)
	if _, err := fmt.Fprint(buf, readBody(t, res)); err != nil {
		t.Fatal(err)
	}
	page := buf.String()
	if !strings.Contains(page, "<title>我的&lt;页面&gt;</title>") {
		t.Fatalf("page title not rendered: %s", page[:200])
	}
	for _, asset := range []string{"/ihtml.js", "/ihtml.css"} {
		res, err := http.Get(srv.URL + asset)
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("asset %s: %v %v", asset, res.StatusCode, err)
		}
		res.Body.Close()
	}
}

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	sb := new(strings.Builder)
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		sb.WriteString(sc.Text())
		sb.WriteString("\n")
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read body: %v", err)
	}
	return sb.String()
}

func TestHandlerAuth(t *testing.T) {
	srv, _ := newTestServer(t)
	status, out := doJSON(t, "GET", srv.URL+"/api/items", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("no uid should be 401, got %d %v", status, out)
	}
}

func TestHandlerItemsFlow(t *testing.T) {
	srv, _ := newTestServer(t)
	base := srv.URL + "/api"

	// PUT 两个 Item
	status, out := doJSON(t, "PUT", base+"/items?uid=alice",
		`{"items":[{"id":"one","type":"html","order":2,"content":"<b>1</b>"},{"id":"two","type":"js","order":1,"content":"1+1"}]}`)
	if status != 200 {
		t.Fatalf("put items: %d %v", status, out)
	}
	// GET 按 order 排序
	status, out = doJSON(t, "GET", base+"/items?uid=alice", "")
	items := out["items"].([]any)
	if status != 200 || len(items) != 2 {
		t.Fatalf("list items: %d %v", status, out)
	}
	if items[0].(map[string]any)["id"] != "two" {
		t.Fatalf("items should sort by order: %v", items)
	}
	// 用户隔离
	status, out = doJSON(t, "GET", base+"/items?uid=bob", "")
	if status != 200 || len(out["items"].([]any)) != 0 {
		t.Fatalf("bob should have no items: %d %v", status, out)
	}
	// 覆盖 one → 产生快照
	status, _ = doJSON(t, "PUT", base+"/items?uid=alice", `[{"id":"one","type":"html","order":2,"content":"<b>2</b>"}]`)
	if status != 200 {
		t.Fatalf("overwrite failed: %d", status)
	}
	status, out = doJSON(t, "GET", base+"/revisions?uid=alice", "")
	revs := out["revisions"].([]any)
	if status != 200 || len(revs) == 0 {
		t.Fatalf("should have revisions: %d %v", status, out)
	}
	revID := revs[0].(map[string]any)["id"].(string)
	// 回滚
	status, out = doJSON(t, "POST", base+"/revisions/"+revID+"/rollback?uid=alice", "")
	if status != 200 {
		t.Fatalf("rollback: %d %v", status, out)
	}
	status, out = doJSON(t, "GET", base+"/items?uid=alice", "")
	if status != 200 {
		t.Fatalf("items after rollback: %d %v", status, out)
	}
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		if m["id"] == "one" && m["content"] != "<b>1</b>" {
			t.Fatalf("rollback content mismatch: %v", m)
		}
	}
	// 删除
	status, out = doJSON(t, "DELETE", base+"/items?uid=alice&id=one&id=two", "")
	if status != 200 {
		t.Fatalf("delete: %d %v", status, out)
	}
	status, out = doJSON(t, "GET", base+"/items?uid=alice", "")
	if status != 200 || len(out["items"].([]any)) != 0 {
		t.Fatalf("items should be empty after delete: %d %v", status, out)
	}
	// 非法 Item 校验
	status, out = doJSON(t, "PUT", base+"/items?uid=alice", `[{"id":"bad id","type":"html","content":""}]`)
	if status != http.StatusBadRequest {
		t.Fatalf("invalid id should 400: %d %v", status, out)
	}
}

func TestHandlerKVAndErrors(t *testing.T) {
	srv, _ := newTestServer(t)
	base := srv.URL + "/api"

	status, out := doJSON(t, "PUT", base+"/kv/theme?uid=u", `{"value":{"mode":"dark","primary":"#059669"}}`)
	if status != 200 {
		t.Fatalf("kv set: %d %v", status, out)
	}
	status, out = doJSON(t, "GET", base+"/kv/theme?uid=u", "")
	if status != 200 || out["value"].(map[string]any)["mode"] != "dark" {
		t.Fatalf("kv get: %d %v", status, out)
	}
	// 保留前缀拒绝写
	status, _ = doJSON(t, "PUT", base+"/kv/__ihtml.errors?uid=u", `{"value":1}`)
	if status != http.StatusForbidden {
		t.Fatalf("reserved kv should 403, got %d", status)
	}
	// 错误上报与读取
	status, _ = doJSON(t, "POST", base+"/errors?uid=u", `{"errors":[{"item_id":"clock","message":"boom"}]}`)
	if status != 200 {
		t.Fatalf("report errors: %d", status)
	}
	status, out = doJSON(t, "GET", base+"/errors?uid=u", "")
	errs := out["errors"].([]any)
	if status != 200 || len(errs) != 1 || errs[0].(map[string]any)["message"] != "boom" {
		t.Fatalf("list errors: %d %v", status, out)
	}
	// config
	status, out = doJSON(t, "GET", base+"/config?uid=u", "")
	if status != 200 || out["chat"] != false || out["user"] != "u" {
		t.Fatalf("config: %d %v", status, out)
	}
}

func TestHandlerSSE(t *testing.T) {
	srv, h := newTestServer(t)
	req, _ := http.NewRequest("GET", srv.URL+"/api/events?uid=sse", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("sse content type: %s", ct)
	}

	// 订阅建立后写入一个 Item, 应收到 put 事件
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = h.PutItems(context.Background(), "sse", []Item{{ID: "x", Type: ItemCSS, Content: "body{}"}}, "api", "t")
	}()
	deadline := time.After(3 * time.Second)
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for sse event")
		case line := <-lines:
			if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"op":"put"`) && strings.Contains(line, `"x"`) {
				return // 成功
			}
		}
	}
}

func TestHandlerPages(t *testing.T) {
	srv, _ := newTestServer(t)
	base := srv.URL + "/api"

	// 注册两个页面
	status, out := doJSON(t, "PUT", base+"/pages?uid=p1", `{"page":{"name":"dash","title":"仪表盘","icon":"📊","order":10}}`)
	if status != 200 {
		t.Fatalf("set page: %d %v", status, out)
	}
	status, _ = doJSON(t, "PUT", base+"/pages?uid=p1", `{"page":{"name":"bills","title":"账单","order":20}}`)
	if status != 200 {
		t.Fatal("set page 2 failed")
	}
	// 非法页面名
	status, _ = doJSON(t, "PUT", base+"/pages?uid=p1", `{"page":{"name":"Bad Name","title":"x"}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("invalid page name should 400, got %d", status)
	}
	status, out = doJSON(t, "GET", base+"/pages?uid=p1", "")
	pages := out["pages"].([]any)
	if status != 200 || len(pages) != 2 || pages[0].(map[string]any)["name"] != "dash" {
		t.Fatalf("list pages: %d %v", status, out)
	}

	// 页面过滤: 全局 + dash 页 + bills 页各放一个 Item
	status, _ = doJSON(t, "PUT", base+"/items?uid=p1",
		`[{"id":"g","type":"css","content":"body{}"},{"id":"d","type":"html","page":"dash","content":"<i>d</i>"},{"id":"b","type":"html","page":"bills","content":"<i>b</i>"}]`)
	if status != 200 {
		t.Fatal("put items failed")
	}
	ids := func(out map[string]any) []string {
		var got []string
		for _, it := range out["items"].([]any) {
			got = append(got, it.(map[string]any)["id"].(string))
		}
		return got
	}
	_, out = doJSON(t, "GET", base+"/items?uid=p1&page=dash", "")
	if got := ids(out); len(got) != 2 || got[0] != "d" && got[1] != "d" {
		t.Fatalf("dash+global filter: %v", got)
	}
	_, out = doJSON(t, "GET", base+"/items?uid=p1&page=bills&global=0", "")
	if got := ids(out); len(got) != 1 || got[0] != "b" {
		t.Fatalf("bills only filter: %v", got)
	}
	_, out = doJSON(t, "GET", base+"/items?uid=p1", "")
	if got := ids(out); len(got) != 3 {
		t.Fatalf("all items: %v", got)
	}

	// 删除页面: 注册表与页内 Item 一起删, 全局 Item 保留
	status, out = doJSON(t, "DELETE", base+"/pages/bills?uid=p1", "")
	if status != 200 {
		t.Fatalf("delete page: %d %v", status, out)
	}
	_, out = doJSON(t, "GET", base+"/pages?uid=p1", "")
	if len(out["pages"].([]any)) != 1 {
		t.Fatalf("page not removed: %v", out)
	}
	_, out = doJSON(t, "GET", base+"/items?uid=p1", "")
	if got := ids(out); len(got) != 2 {
		t.Fatalf("bills items should be gone: %v", got)
	}
}

func TestGovernance(t *testing.T) {
	h := New(NewMemoryStore(),
		WithUserResolver(DevelopmentUserResolver),
		WithLimits(Limits{WritesPerMinute: 2, MaxKVKeysPerUser: 2}),
		WithAccessPolicy(func(_ *http.Request, user string) Access {
			return Access{ReadOnly: user == "viewer", ChatDisabled: user == "viewer"}
		}))
	srv := httptest.NewServer(h)
	defer srv.Close()

	// 只读用户: 写 403, config 下发 read_only
	status, _ := doJSON(t, "PUT", srv.URL+"/api/items?uid=viewer", `[{"id":"a","type":"css","content":"x"}]`)
	if status != http.StatusForbidden {
		t.Fatalf("read-only write should 403, got %d", status)
	}
	_, out := doJSON(t, "GET", srv.URL+"/api/config?uid=viewer", "")
	if out["read_only"] != true {
		t.Fatalf("config read_only: %v", out)
	}

	// 写频率限制: 前 2 次过, 第 3 次 429
	for i := range 2 {
		body := fmt.Sprintf(`[{"id":"a","type":"css","content":"x%d"}]`, i)
		status, _ = doJSON(t, "PUT", srv.URL+"/api/items?uid=w", body)
		if status != 200 {
			t.Fatalf("write %d should pass: %d", i, status)
		}
	}
	status, _ = doJSON(t, "PUT", srv.URL+"/api/items?uid=w", `[{"id":"a","type":"css","content":"x"}]`)
	if status != http.StatusTooManyRequests {
		t.Fatalf("3rd write should 429, got %d", status)
	}
	if h.Stats()["writes"] < 2 || h.Stats()["rate_limited"] < 1 {
		t.Fatalf("stats: %v", h.Stats())
	}

	// KV 键数量配额(排除保留键): 2 个之后拒绝新键, 覆盖旧键不受限
	ctx := context.Background()
	if err := h.KVSet(ctx, "q", "k1", json.RawMessage(`1`)); err != nil {
		t.Fatal(err)
	}
	if err := h.KVSet(ctx, "q", "k2", json.RawMessage(`1`)); err != nil {
		t.Fatal(err)
	}
	if err := h.KVSet(ctx, "q", "k3", json.RawMessage(`1`)); err == nil {
		t.Fatal("kv quota should reject 3rd key")
	}
	if err := h.KVSet(ctx, "q", "k1", json.RawMessage(`2`)); err != nil {
		t.Fatalf("overwrite existing key should pass: %v", err)
	}
}

func TestAuditAndExportImportPurge(t *testing.T) {
	var audits []AuditEvent
	h := New(NewMemoryStore(), WithUserResolver(DevelopmentUserResolver), WithAudit(func(_ context.Context, e AuditEvent) {
		audits = append(audits, e)
	}))
	ctx := context.Background()
	if err := h.PutItems(ctx, "u", []Item{{ID: "a", Type: ItemHTML, Content: "<i/>", Page: "p1"}}, "chat", "初建"); err != nil {
		t.Fatal(err)
	}
	if err := h.SetPage(ctx, "u", Page{Name: "p1", Title: "页一"}); err != nil {
		t.Fatal(err)
	}
	if err := h.KVSet(ctx, "u", "cnt", json.RawMessage(`7`)); err != nil {
		t.Fatal(err)
	}

	export, err := h.ExportUser(ctx, "u")
	if err != nil {
		t.Fatal(err)
	}
	if len(export.Items) != 1 || len(export.Pages) != 1 || string(export.KV["cnt"]) != `7` {
		t.Fatalf("export content: %+v", export)
	}

	if err := h.PurgeUser(ctx, "u"); err != nil {
		t.Fatal(err)
	}
	items, _ := h.ListItems(ctx, "u")
	pages, _ := h.ListPages(ctx, "u")
	if len(items) != 0 || len(pages) != 0 {
		t.Fatalf("purge incomplete: %v %v", items, pages)
	}

	if err := h.ImportUser(ctx, "u", export); err != nil {
		t.Fatal(err)
	}
	items, _ = h.ListItems(ctx, "u")
	pages, _ = h.ListPages(ctx, "u")
	v, _ := h.KVGet(ctx, "u", "cnt")
	if len(items) != 1 || len(pages) != 1 || string(v) != `7` {
		t.Fatalf("import incomplete: %v %v %s", items, pages, v)
	}

	// 审计流覆盖 put/page.set/kv.set/purge/import
	got := map[string]bool{}
	for _, a := range audits {
		got[a.Action] = true
	}
	for _, want := range []string{"items.put", "page.set", "kv.set", "user.purge", "user.import"} {
		if !got[want] {
			t.Fatalf("audit missing %s: %v", want, got)
		}
	}

	// HTTP 导出端点
	srv := httptest.NewServer(h)
	defer srv.Close()
	status, out := doJSON(t, "GET", srv.URL+"/api/export?uid=u", "")
	if status != 200 || len(out["items"].([]any)) != 1 {
		t.Fatalf("http export: %d %v", status, out)
	}
}

func TestAssetETag(t *testing.T) {
	srv, _ := newTestServer(t)
	res, err := http.Get(srv.URL + "/ihtml.js")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	etag := res.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing etag")
	}
	req, _ := http.NewRequest("GET", srv.URL+"/ihtml.js", nil)
	req.Header.Set("If-None-Match", etag)
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusNotModified {
		t.Fatalf("etag revalidation should 304, got %d", res2.StatusCode)
	}
}

func TestHandlerClose(t *testing.T) {
	srv, h := newTestServer(t)
	// 打开一条 SSE, Close 后应迅速结束
	res, err := http.Get(srv.URL + "/api/events?uid=c1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 1024)
		for {
			if _, err := res.Body.Read(buf); err != nil {
				close(done)
				return
			}
		}
	}()
	time.Sleep(100 * time.Millisecond)
	_ = h.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("sse should terminate after Close")
	}
}

func TestHandlerStripPrefixRedirect(t *testing.T) {
	h := New(NewMemoryStore(), WithUserResolver(DevelopmentUserResolver))
	mux := http.NewServeMux()
	mux.Handle("/ui/", http.StripPrefix("/ui", h))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Get(srv.URL + "/ui?uid=a")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	// 外层 mux 或本库的兜底重定向都可以, 只要求跳到带斜杠且保留参数
	if res.StatusCode < 300 || res.StatusCode >= 400 {
		t.Fatalf("bare mount path should redirect, got %d", res.StatusCode)
	}
	if loc := res.Header.Get("Location"); loc != "/ui/?uid=a" {
		t.Fatalf("redirect location: %s", loc)
	}
	// 带斜杠正常出页面
	res2, err := http.Get(srv.URL + "/ui/?uid=a")
	if err != nil || res2.StatusCode != 200 {
		t.Fatalf("mounted page: %v %v", res2.StatusCode, err)
	}
	res2.Body.Close()
}
