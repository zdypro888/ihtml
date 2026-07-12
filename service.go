package ihtml

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Service 是对某用户 UI 数据的完整操作集, HTTP API 与 AI 工具走同一实现,
// 保证快照、上限校验与 SSE 通知不会被绕过。*Handler 实现该接口。
type Service interface {
	// ListItems 返回按 Order 升序(同序按 ID)排好的 Item 全集。
	ListItems(ctx context.Context, user string) ([]Item, error)
	// PutItems 按 ID 增改 Item(变更前自动快照), note/by 记入快照说明。
	PutItems(ctx context.Context, user string, items []Item, by, note string) error
	// DeleteItems 删除指定 Item, 返回实际删除的 ID。
	DeleteItems(ctx context.Context, user string, ids []string, by, note string) ([]string, error)
	// ReplaceItems 整体替换 Item 全集。
	ReplaceItems(ctx context.Context, user string, items []Item, by, note string) error
	// ListRevisions 返回快照列表(新的在前)。
	ListRevisions(ctx context.Context, user string) ([]RevisionInfo, error)
	// RevisionItems 返回某快照的 Item 全集。
	RevisionItems(ctx context.Context, user, id string) ([]Item, error)
	// Rollback 把 Item 全集回滚到指定快照(回滚前同样自动快照)。
	Rollback(ctx context.Context, user, revisionID, by string) error
	// KVGet 读取用户级 KV, 不存在返回 ErrNotFound。
	KVGet(ctx context.Context, user, key string) (json.RawMessage, error)
	// KVSet 写入用户级 KV 并广播变更事件。
	KVSet(ctx context.Context, user, key string, value json.RawMessage) error
	// KVDelete 删除用户级 KV 并广播变更事件。
	KVDelete(ctx context.Context, user, key string) error
	// PageErrors 返回内核上报的最近页面错误(新的在前)。
	PageErrors(ctx context.Context, user string) ([]PageError, error)
	// ReportErrors 追加页面错误记录。
	ReportErrors(ctx context.Context, user string, errs []PageError) error
	// ListPages 返回页面注册表(按 Order 排序), 即侧边菜单的数据源。
	ListPages(ctx context.Context, user string) ([]Page, error)
	// SetPage 新建或更新页面(按 Name 合并)。
	SetPage(ctx context.Context, user string, page Page) error
	// DeletePage 删除页面及其名下全部 Item, 返回连带删除的 Item ID。
	DeletePage(ctx context.Context, user, name, by string) ([]string, error)
}

const errorsKVKey = "__ihtml.errors"

const pagesKVKey = "__ihtml.pages"

// reservedKVPrefix 下的键由内核/服务端内部使用, 拒绝通过公开 KV API 写入。
const reservedKVPrefix = "__ihtml"

var kvKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._\-]{1,64}$`)

// MaxKVValueBytes 是单个 KV 值的上限。
const MaxKVValueBytes = 64 << 10

// userLock 返回该用户的互斥锁(固定分桶, 无内存泄漏), 用于串行化"读-改-写"。
// 注意: 锁是进程内的, 多副本部署需保证同一用户路由到同一副本(见 README 部署章节)。
func (h *Handler) userLock(user string) *sync.Mutex {
	f := fnv.New32a()
	_, _ = f.Write([]byte(user))
	return &h.lockShards[f.Sum32()%uint32(len(h.lockShards))]
}

// publish 统一出口: 计数 + 交给 Broadcaster。
func (h *Handler) publish(user string, ev Event) {
	h.metric("events_published", 1)
	h.opts.broadcaster.Publish(user, ev)
}

// audit 同步调用宿主审计回调(未注册则忽略)。
func (h *Handler) audit(ctx context.Context, user, action, by, note string, detail any) {
	if h.opts.audit == nil {
		return
	}
	var raw json.RawMessage
	if detail != nil {
		raw, _ = json.Marshal(detail)
	}
	h.opts.audit(ctx, AuditEvent{Time: time.Now(), User: user, Action: action, By: by, Note: note, Detail: raw})
}

func sortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Order != items[j].Order {
			return items[i].Order < items[j].Order
		}
		return items[i].ID < items[j].ID
	})
}

// ListItems 实现 Service。
func (h *Handler) ListItems(ctx context.Context, user string) ([]Item, error) {
	items, err := h.store.Items(ctx, user)
	if err != nil {
		return nil, err
	}
	sortItems(items)
	return items, nil
}

// snapshotLocked 在持有用户锁的前提下为 current 存一条快照。
// 空集不快照(没有可回滚的内容)。
func (h *Handler) snapshotLocked(ctx context.Context, user string, current []Item, by, note string) error {
	if len(current) == 0 {
		return nil
	}
	rev := Revision{
		ID:        newRevisionID(),
		Note:      note,
		By:        by,
		CreatedAt: time.Now(),
		Items:     current,
	}
	return h.store.AddRevision(ctx, user, rev, h.opts.maxRevisions)
}

func newRevisionID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("r%d-%s", time.Now().UnixMilli(), hex.EncodeToString(b[:]))
}

func validateItems(items []Item) error {
	seen := make(map[string]struct{}, len(items))
	for i := range items {
		if err := items[i].Validate(); err != nil {
			return err
		}
		if _, dup := seen[items[i].ID]; dup {
			return fmt.Errorf("duplicate item id %q", items[i].ID)
		}
		seen[items[i].ID] = struct{}{}
	}
	return nil
}

// PutItems 实现 Service。
func (h *Handler) PutItems(ctx context.Context, user string, items []Item, by, note string) error {
	if len(items) == 0 {
		return fmt.Errorf("no items to put")
	}
	if err := validateItems(items); err != nil {
		return err
	}
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()

	existing, err := h.store.Items(ctx, user)
	if err != nil {
		return err
	}
	if err := h.snapshotLocked(ctx, user, existing, by, note); err != nil {
		return err
	}
	now := time.Now()
	index := make(map[string]int, len(existing))
	for i := range existing {
		index[existing[i].ID] = i
	}
	merged := cloneItems(existing)
	written := make([]Item, 0, len(items))
	for _, it := range items {
		it.UpdatedAt = now
		if pos, ok := index[it.ID]; ok {
			merged[pos] = it
		} else {
			index[it.ID] = len(merged)
			merged = append(merged, it)
		}
		written = append(written, it)
	}
	if len(merged) > MaxItemsPerUser {
		return fmt.Errorf("too many items: %d > %d", len(merged), MaxItemsPerUser)
	}
	if err := h.store.ReplaceItems(ctx, user, merged); err != nil {
		return err
	}
	sortItems(written)
	h.metric("writes", 1)
	h.publish(user, Event{Type: "items", Op: "put", Items: written, By: by})
	h.audit(ctx, user, "items.put", by, note, map[string]any{"ids": itemIDList(written)})
	return nil
}

func itemIDList(items []Item) []string {
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	return ids
}

// DeleteItems 实现 Service。
func (h *Handler) DeleteItems(ctx context.Context, user string, ids []string, by, note string) ([]string, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("no item ids to delete")
	}
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()

	existing, err := h.store.Items(ctx, user)
	if err != nil {
		return nil, err
	}
	drop := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		drop[strings.TrimSpace(id)] = struct{}{}
	}
	kept := make([]Item, 0, len(existing))
	removed := make([]string, 0, len(ids))
	for _, it := range existing {
		if _, ok := drop[it.ID]; ok {
			removed = append(removed, it.ID)
			continue
		}
		kept = append(kept, it)
	}
	if len(removed) == 0 {
		return nil, nil
	}
	if err := h.snapshotLocked(ctx, user, existing, by, note); err != nil {
		return nil, err
	}
	if err := h.store.ReplaceItems(ctx, user, kept); err != nil {
		return nil, err
	}
	h.metric("writes", 1)
	h.publish(user, Event{Type: "items", Op: "delete", IDs: removed, By: by})
	h.audit(ctx, user, "items.delete", by, note, map[string]any{"ids": removed})
	return removed, nil
}

// ReplaceItems 实现 Service。
func (h *Handler) ReplaceItems(ctx context.Context, user string, items []Item, by, note string) error {
	if err := validateItems(items); err != nil {
		return err
	}
	if len(items) > MaxItemsPerUser {
		return fmt.Errorf("too many items: %d > %d", len(items), MaxItemsPerUser)
	}
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()

	existing, err := h.store.Items(ctx, user)
	if err != nil {
		return err
	}
	if err := h.snapshotLocked(ctx, user, existing, by, note); err != nil {
		return err
	}
	now := time.Now()
	next := cloneItems(items)
	for i := range next {
		next[i].UpdatedAt = now
	}
	if err := h.store.ReplaceItems(ctx, user, next); err != nil {
		return err
	}
	h.metric("writes", 1)
	h.publish(user, Event{Type: "items", Op: "replace", By: by})
	h.audit(ctx, user, "items.replace", by, note, map[string]any{"count": len(next)})
	return nil
}

// ListRevisions 实现 Service。
func (h *Handler) ListRevisions(ctx context.Context, user string) ([]RevisionInfo, error) {
	return h.store.Revisions(ctx, user)
}

// RevisionItems 实现 Service。
func (h *Handler) RevisionItems(ctx context.Context, user, id string) ([]Item, error) {
	items, err := h.store.RevisionItems(ctx, user, id)
	if err != nil {
		return nil, err
	}
	sortItems(items)
	return items, nil
}

// Rollback 实现 Service。
func (h *Handler) Rollback(ctx context.Context, user, revisionID, by string) error {
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()

	target, err := h.store.RevisionItems(ctx, user, revisionID)
	if err != nil {
		return err
	}
	existing, err := h.store.Items(ctx, user)
	if err != nil {
		return err
	}
	if err := h.snapshotLocked(ctx, user, existing, by, "rollback to "+revisionID); err != nil {
		return err
	}
	if err := h.store.ReplaceItems(ctx, user, target); err != nil {
		return err
	}
	h.metric("writes", 1)
	h.publish(user, Event{Type: "items", Op: "rollback", By: by})
	h.audit(ctx, user, "items.rollback", by, "rollback to "+revisionID, nil)
	return nil
}

func validateKVKey(key string) error {
	if !kvKeyPattern.MatchString(key) {
		return fmt.Errorf("invalid kv key %q", key)
	}
	return nil
}

// KVGet 实现 Service。
func (h *Handler) KVGet(ctx context.Context, user, key string) (json.RawMessage, error) {
	if err := validateKVKey(key); err != nil {
		return nil, err
	}
	return h.store.KVGet(ctx, user, key)
}

// KVSet 实现 Service。
func (h *Handler) KVSet(ctx context.Context, user, key string, value json.RawMessage) error {
	if err := validateKVKey(key); err != nil {
		return err
	}
	if len(value) > MaxKVValueBytes {
		return fmt.Errorf("kv value exceeds %d bytes", MaxKVValueBytes)
	}
	if !json.Valid(value) {
		return fmt.Errorf("kv value must be valid JSON")
	}
	// 新键受数量配额约束(排除内核保留键), 防止 Item 脚本无限膨胀存储
	if !strings.HasPrefix(key, reservedKVPrefix) {
		if _, err := h.store.KVGet(ctx, user, key); errors.Is(err, ErrNotFound) {
			keys, err := h.store.KVKeys(ctx, user)
			if err != nil {
				return err
			}
			n := 0
			for _, k := range keys {
				if !strings.HasPrefix(k, reservedKVPrefix) {
					n++
				}
			}
			if limit := h.opts.limits.MaxKVKeysPerUser; limit > 0 && n >= limit {
				return fmt.Errorf("kv keys quota exceeded (%d); 请清理不再使用的键", limit)
			}
		}
	}
	if err := h.store.KVSet(ctx, user, key, value); err != nil {
		return err
	}
	h.metric("writes", 1)
	h.publish(user, Event{Type: "kv", Key: key})
	h.audit(ctx, user, "kv.set", "", "", map[string]any{"key": key})
	return nil
}

// KVDelete 实现 Service。
func (h *Handler) KVDelete(ctx context.Context, user, key string) error {
	if err := validateKVKey(key); err != nil {
		return err
	}
	if err := h.store.KVDelete(ctx, user, key); err != nil {
		return err
	}
	h.publish(user, Event{Type: "kv", Key: key})
	h.audit(ctx, user, "kv.delete", "", "", map[string]any{"key": key})
	return nil
}

// PageErrors 实现 Service。
func (h *Handler) PageErrors(ctx context.Context, user string) ([]PageError, error) {
	raw, err := h.store.KVGet(ctx, user, errorsKVKey)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []PageError
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, nil // 记录损坏时按空处理, 下次上报会覆盖
	}
	return out, nil
}

// ReportErrors 实现 Service。
func (h *Handler) ReportErrors(ctx context.Context, user string, errs []PageError) error {
	if len(errs) == 0 {
		return nil
	}
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()

	current, _ := h.PageErrors(ctx, user)
	now := time.Now()
	for i := range errs {
		if errs[i].Time.IsZero() {
			errs[i].Time = now
		}
		errs[i].Message = clip(errs[i].Message, 2048)
		errs[i].Stack = clip(errs[i].Stack, 4096)
		errs[i].URL = clip(errs[i].URL, 512)
		errs[i].ItemID = clip(errs[i].ItemID, 128)
	}
	merged := append(errs, current...)
	if len(merged) > MaxPageErrors {
		merged = merged[:MaxPageErrors]
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	return h.store.KVSet(ctx, user, errorsKVKey, raw)
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

/* ---------- 页面注册表 (存于保留 KV, 不进快照; 页面结构性内容在 Item 里) ---------- */

func (h *Handler) loadPages(ctx context.Context, user string) ([]Page, error) {
	raw, err := h.store.KVGet(ctx, user, pagesKVKey)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pages []Page
	if err := json.Unmarshal(raw, &pages); err != nil {
		return nil, nil // 损坏按空处理, 下次写入覆盖
	}
	return pages, nil
}

func (h *Handler) savePagesLocked(ctx context.Context, user string, pages []Page) error {
	sort.SliceStable(pages, func(i, j int) bool {
		if pages[i].Order != pages[j].Order {
			return pages[i].Order < pages[j].Order
		}
		return pages[i].Name < pages[j].Name
	})
	raw, err := json.Marshal(pages)
	if err != nil {
		return err
	}
	if err := h.store.KVSet(ctx, user, pagesKVKey, raw); err != nil {
		return err
	}
	h.publish(user, Event{Type: "pages"})
	return nil
}

// ListPages 实现 Service。
func (h *Handler) ListPages(ctx context.Context, user string) ([]Page, error) {
	pages, err := h.loadPages(ctx, user)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(pages, func(i, j int) bool {
		if pages[i].Order != pages[j].Order {
			return pages[i].Order < pages[j].Order
		}
		return pages[i].Name < pages[j].Name
	})
	return pages, nil
}

// SetPage 实现 Service。
func (h *Handler) SetPage(ctx context.Context, user string, page Page) error {
	if err := page.Validate(); err != nil {
		return err
	}
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()
	pages, err := h.loadPages(ctx, user)
	if err != nil {
		return err
	}
	found := false
	for i := range pages {
		if pages[i].Name == page.Name {
			pages[i] = page
			found = true
			break
		}
	}
	if !found {
		if len(pages) >= 128 {
			return fmt.Errorf("too many pages")
		}
		pages = append(pages, page)
	}
	if err := h.savePagesLocked(ctx, user, pages); err != nil {
		return err
	}
	h.audit(ctx, user, "page.set", "", "", map[string]any{"page": page.Name})
	return nil
}

// DeletePage 实现 Service。
func (h *Handler) DeletePage(ctx context.Context, user, name, by string) ([]string, error) {
	if err := ValidatePageName(name); err != nil {
		return nil, err
	}
	lock := h.userLock(user)
	lock.Lock()
	pages, err := h.loadPages(ctx, user)
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	kept := pages[:0]
	for _, p := range pages {
		if p.Name != name {
			kept = append(kept, p)
		}
	}
	if err := h.savePagesLocked(ctx, user, kept); err != nil {
		lock.Unlock()
		return nil, err
	}
	// 收集该页 Item, 解锁后走 DeleteItems(自带快照与事件, 会重新拿锁)
	items, err := h.store.Items(ctx, user)
	lock.Unlock()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0)
	for _, it := range items {
		if it.Page == name {
			ids = append(ids, it.ID)
		}
	}
	h.audit(ctx, user, "page.delete", by, "", map[string]any{"page": name})
	if len(ids) == 0 {
		return nil, nil
	}
	return h.DeleteItems(ctx, user, ids, by, "delete page "+name)
}

/* ---------- 导出 / 导入 / 清除 (备份、迁移、模板预置、注销) ---------- */

// ExportUser 导出该用户全量数据(UserExport 即备份契约)。
func (h *Handler) ExportUser(ctx context.Context, user string) (*UserExport, error) {
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()

	items, err := h.store.Items(ctx, user)
	if err != nil {
		return nil, err
	}
	sortItems(items)
	pages, err := h.loadPages(ctx, user)
	if err != nil {
		return nil, err
	}
	keys, err := h.store.KVKeys(ctx, user)
	if err != nil {
		return nil, err
	}
	kv := make(map[string]json.RawMessage, len(keys))
	for _, k := range keys {
		if k == pagesKVKey {
			continue // 页面注册表以 Pages 字段导出
		}
		v, err := h.store.KVGet(ctx, user, k)
		if err != nil {
			continue
		}
		kv[k] = v
	}
	infos, err := h.store.Revisions(ctx, user)
	if err != nil {
		return nil, err
	}
	revisions := make([]Revision, 0, len(infos))
	for _, info := range infos {
		revItems, err := h.store.RevisionItems(ctx, user, info.ID)
		if err != nil {
			continue
		}
		revisions = append(revisions, Revision{ID: info.ID, Note: info.Note, By: info.By, CreatedAt: info.CreatedAt, Items: revItems})
	}
	return &UserExport{Items: items, Pages: pages, KV: kv, Revisions: revisions}, nil
}

// ImportUser 用 UserExport 整体替换该用户数据(先清场再写入)。
// 用途: 备份恢复、环境迁移、新用户套模板。
func (h *Handler) ImportUser(ctx context.Context, user string, export *UserExport) error {
	if export == nil {
		return fmt.Errorf("nothing to import")
	}
	if err := validateItems(export.Items); err != nil {
		return err
	}
	for i := range export.Pages {
		if err := export.Pages[i].Validate(); err != nil {
			return err
		}
	}
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()

	if err := h.store.EraseUser(ctx, user); err != nil {
		return err
	}
	if err := h.store.ReplaceItems(ctx, user, export.Items); err != nil {
		return err
	}
	for k, v := range export.KV {
		if err := validateKVKey(k); err != nil {
			continue
		}
		_ = h.store.KVSet(ctx, user, k, v)
	}
	if len(export.Pages) > 0 {
		if err := h.savePagesLocked(ctx, user, export.Pages); err != nil {
			return err
		}
	}
	// 快照按时间正序回放(AddRevision 语义是"新的在前")
	for i := len(export.Revisions) - 1; i >= 0; i-- {
		_ = h.store.AddRevision(ctx, user, export.Revisions[i], h.opts.maxRevisions)
	}
	h.publish(user, Event{Type: "items", Op: "import", By: "import"})
	h.publish(user, Event{Type: "pages"})
	h.audit(ctx, user, "user.import", "import", "", map[string]any{"items": len(export.Items), "pages": len(export.Pages)})
	return nil
}

// PurgeUser 删除该用户全部数据(注销/GDPR)。
func (h *Handler) PurgeUser(ctx context.Context, user string) error {
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()
	if err := h.store.EraseUser(ctx, user); err != nil {
		return err
	}
	h.publish(user, Event{Type: "items", Op: "purge", By: "api"})
	h.publish(user, Event{Type: "pages"})
	h.audit(ctx, user, "user.purge", "api", "", nil)
	return nil
}
