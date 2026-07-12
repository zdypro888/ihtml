package ihtml

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ItemService is the independently implementable Item capability. New public
// capabilities are added as separate interfaces so SDK consumers can depend on
// the smallest stable contract they need.
type ItemService interface {
	// ListItems 返回按 Order 升序(同序按 ID)排好的 Item 全集。
	ListItems(ctx context.Context, user string) ([]Item, error)
	// PutItems 按 ID 增改 Item(变更前自动快照), note/by 记入快照说明。
	PutItems(ctx context.Context, user string, items []Item, by, note string) error
	// DeleteItems 删除指定 Item, 返回实际删除的 ID。
	DeleteItems(ctx context.Context, user string, ids []string, by, note string) ([]string, error)
	// ReplaceItems 整体替换 Item 全集。
	ReplaceItems(ctx context.Context, user string, items []Item, by, note string) error
}

// RevisionService is the revision history capability.
type RevisionService interface {
	// ListRevisions 返回快照列表(新的在前)。
	ListRevisions(ctx context.Context, user string) ([]RevisionInfo, error)
	// RevisionItems 返回某快照的 Item 全集。
	RevisionItems(ctx context.Context, user, id string) ([]Item, error)
	// Rollback 把 Item 全集回滚到指定快照(回滚前同样自动快照)。
	Rollback(ctx context.Context, user, revisionID, by string) error
}

// KVService is the per-user JSON key/value capability.
type KVService interface {
	// KVGet 读取用户级 KV, 不存在返回 ErrNotFound。
	KVGet(ctx context.Context, user, key string) (json.RawMessage, error)
	// KVSet 写入用户级 KV 并广播变更事件。
	KVSet(ctx context.Context, user, key string, value json.RawMessage) error
	// KVDelete 删除用户级 KV 并广播变更事件。
	KVDelete(ctx context.Context, user, key string) error
}

// ErrorService is the browser runtime error capability.
type ErrorService interface {
	// PageErrors 返回内核上报的最近页面错误(新的在前)。
	PageErrors(ctx context.Context, user string) ([]PageError, error)
	// ReportErrors 追加页面错误记录。
	ReportErrors(ctx context.Context, user string, errs []PageError) error
}

// PageService is the page registry capability.
type PageService interface {
	// ListPages 返回页面注册表(按 Order 排序), 即侧边菜单的数据源。
	ListPages(ctx context.Context, user string) ([]Page, error)
	// SetPage 新建或更新页面(按 Name 合并)。
	SetPage(ctx context.Context, user string, page Page) error
	// DeletePage 删除页面及其名下全部 Item, 返回连带删除的 Item ID。
	DeletePage(ctx context.Context, user, name, by string) ([]string, error)
}

// TemplateService is the reusable layout template capability.
type TemplateService interface {
	// ListTemplates 返回一等布局模板；模板内容与动态页面 Item 分离。
	ListTemplates(ctx context.Context, user string) ([]Template, error)
	GetTemplate(ctx context.Context, user, id string) (Template, error)
	GetCurrentTemplate(ctx context.Context, user, page string) (CurrentTemplate, error)
	PutTemplate(ctx context.Context, user string, template Template, expectedRevision uint64, by string) (Template, error)
	PutCurrentTemplate(ctx context.Context, user, page string, template Template, expectedRevision uint64, by string) (Template, error)
	PublishTemplate(ctx context.Context, user, id string, expectedRevision uint64, by string) (Template, error)
	DeleteTemplateSlot(ctx context.Context, user, id, slotID string, expectedRevision uint64, by string) (Template, error)
	DeleteTemplate(ctx context.Context, user, id string, expectedRevision uint64, by string) error
}

// Service is the complete trusted administration API implemented by *Handler.
// Direct calls intentionally bypass HTTP authentication and AccessPolicy; use
// Handler.ServiceForRequest for request-scoped, policy-enforced access.
//
// The interface embeds smaller capability interfaces so downstream code can
// avoid implementing or mocking unrelated future features.
type Service interface {
	ItemService
	RevisionService
	KVService
	ErrorService
	PageService
	TemplateService
}

const errorsKVKey = "__ihtml.errors"

const pagesKVKey = "__ihtml.pages"

// ItemLayoutKVPrefix identifies editor-owned per-Item layout records. These
// records remain ordinary KV so the browser can persist them, but lifecycle
// operations such as deleting a page must keep them in sync with that page.
const ItemLayoutKVPrefix = "item-layout."

const (
	maxPagesPerUser        = 128
	maxInternalKVValueSize = 1 << 20
	maxRevisionIDBytes     = 128
	maxRevisionNoteBytes   = 1024
	maxRevisionByBytes     = 128
)

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
	items = cloneItems(items)
	sortItems(items)
	return items, nil
}

// snapshotLocked 在持有用户锁的前提下为 current 存一条完整 UI 快照。
// 即使 Items/Pages 都为空也保存，使用户第一次创建的内容同样可回滚。
func (h *Handler) snapshotLocked(ctx context.Context, user string, current []Item, by, note string) error {
	pages, err := h.loadPages(ctx, user)
	if err != nil {
		return err
	}
	catalog, err := h.loadTemplateCatalog(ctx, user, pages)
	if err != nil {
		return err
	}
	return h.addRevisionLocked(ctx, user, newRevision(current, pages, by, note, catalog))
}

func newRevision(items []Item, pages []Page, by, note string, catalogs ...templateCatalog) Revision {
	revision := Revision{
		Version:   CurrentDataVersion,
		ID:        newRevisionID(),
		Note:      note,
		By:        by,
		CreatedAt: time.Now(),
		Items:     cloneItems(items),
		Pages:     append([]Page(nil), pages...),
	}
	if len(catalogs) > 0 {
		revision.Templates = cloneTemplates(catalogs[0].Templates)
		revision.RootTemplateID = catalogs[0].RootTemplateID
		revision.TemplateRevisionClock = catalogs[0].NextRevision
	}
	return revision
}

func (h *Handler) addRevisionLocked(ctx context.Context, user string, rev Revision) error {
	return h.store.AddRevision(ctx, user, rev, h.opts.maxRevisions)
}

func newRevisionID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("r%d-%s", time.Now().UnixMilli(), hex.EncodeToString(b[:]))
}

func validateItems(items []Item) error {
	if len(items) > MaxItemsPerUser {
		return fmt.Errorf("too many items: %d > %d", len(items), MaxItemsPerUser)
	}
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

func sameItemContent(a, b Item) bool {
	return a.ID == b.ID && a.Type == b.Type && a.Title == b.Title && a.Order == b.Order &&
		a.Page == b.Page && a.Content == b.Content && maps.Equal(a.Meta, b.Meta)
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
	if err := validateItems(existing); err != nil {
		return fmt.Errorf("stored items are invalid: %w", err)
	}
	now := time.Now()
	index := make(map[string]int, len(existing))
	for i := range existing {
		index[existing[i].ID] = i
	}
	merged := cloneItems(existing)
	written := make([]Item, 0, len(items))
	for _, it := range items {
		if pos, ok := index[it.ID]; ok {
			if sameItemContent(existing[pos], it) {
				continue
			}
			it.UpdatedAt = now
			merged[pos] = it
		} else {
			it.UpdatedAt = now
			index[it.ID] = len(merged)
			merged = append(merged, it)
		}
		written = append(written, it)
	}
	// 必须先完成合并和上限校验，再生成快照；失败请求不能污染历史列表。
	if len(merged) > MaxItemsPerUser {
		return fmt.Errorf("too many items: %d > %d", len(merged), MaxItemsPerUser)
	}
	if len(written) == 0 {
		return nil
	}
	if err := h.snapshotLocked(ctx, user, existing, by, note); err != nil {
		return err
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
	if err := validateItems(existing); err != nil {
		return nil, fmt.Errorf("stored items are invalid: %w", err)
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
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()

	existing, err := h.store.Items(ctx, user)
	if err != nil {
		return err
	}
	if err := validateItems(existing); err != nil {
		return fmt.Errorf("stored items are invalid: %w", err)
	}
	existingByID := make(map[string]Item, len(existing))
	for _, it := range existing {
		existingByID[it.ID] = it
	}
	now := time.Now()
	next := cloneItems(items)
	changed := len(next) != len(existing)
	for i := range next {
		if old, ok := existingByID[next[i].ID]; ok && sameItemContent(old, next[i]) {
			next[i].UpdatedAt = old.UpdatedAt
			continue
		}
		next[i].UpdatedAt = now
		changed = true
	}
	if !changed {
		return nil
	}
	if err := h.snapshotLocked(ctx, user, existing, by, note); err != nil {
		return err
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
	revisions, err := h.store.Revisions(ctx, user)
	if err != nil {
		return nil, err
	}
	return append([]RevisionInfo(nil), revisions...), nil
}

// RevisionItems 实现 Service。
func (h *Handler) RevisionItems(ctx context.Context, user, id string) ([]Item, error) {
	items, err := h.store.RevisionItems(ctx, user, id)
	if err != nil {
		return nil, err
	}
	items = cloneItems(items)
	sortItems(items)
	return items, nil
}

// Rollback 实现 Service。
func (h *Handler) Rollback(ctx context.Context, user, revisionID, by string) error {
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()

	before, err := h.readUserStateLocked(ctx, user)
	if err != nil {
		return err
	}
	var target *Revision
	for i := range before.Revisions {
		if before.Revisions[i].ID == revisionID {
			target = &before.Revisions[i]
			break
		}
	}
	if target == nil {
		return ErrNotFound
	}
	if target.Version < 0 || target.Version > CurrentDataVersion {
		return fmt.Errorf("revision %q uses unsupported version %d", revisionID, target.Version)
	}
	if err := validateItems(target.Items); err != nil {
		return fmt.Errorf("invalid revision %q: %w", revisionID, err)
	}
	currentPages, err := statePages(before)
	if err != nil {
		return err
	}
	currentCatalog, err := stateTemplateCatalog(before, currentPages)
	if err != nil {
		return err
	}
	next := cloneUserState(before)
	next.Items = cloneItems(target.Items)
	restorePages := target.Version >= 2
	if restorePages {
		if err := setStatePages(&next, target.Pages); err != nil {
			return fmt.Errorf("invalid revision %q pages: %w", revisionID, err)
		}
	}
	restoreTemplates := target.Version >= 3
	if restoreTemplates {
		targetCatalog := templateCatalog{
			RootTemplateID: target.RootTemplateID, NextRevision: target.TemplateRevisionClock,
			Templates: cloneTemplates(target.Templates),
		}
		if err := rebaseRestoredTemplateCatalog(&targetCatalog, currentCatalog); err != nil {
			return fmt.Errorf("restore revision %q template revisions: %w", revisionID, err)
		}
		targetPages := currentPages
		if restorePages {
			targetPages = target.Pages
		}
		if err := setStateTemplateCatalog(&next, targetCatalog, targetPages); err != nil {
			return fmt.Errorf("invalid revision %q templates: %w", revisionID, err)
		}
	}
	prependRevision(&next, newRevision(before.Items, currentPages, by, "rollback to "+revisionID, currentCatalog), h.opts.maxRevisions)
	if err := h.replaceUserStateLocked(ctx, user, before, next); err != nil {
		return err
	}
	h.metric("writes", 1)
	h.publish(user, Event{Type: "items", Op: "rollback", By: by})
	if restorePages {
		h.publish(user, Event{Type: "pages"})
	}
	if restoreTemplates {
		h.publish(user, Event{Type: "templates", Op: "rollback", By: by})
	}
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
	value, err := h.store.KVGet(ctx, user, key)
	if err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), value...), nil
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
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()
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
		} else if err != nil {
			return err
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
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()
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
		return nil, fmt.Errorf("corrupt page error log: %w", err)
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

	current, err := h.PageErrors(ctx, user)
	if err != nil {
		return err
	}
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
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

/* ---------- 页面注册表 (存于保留 KV, v2 起随 Revision 一起快照) ---------- */

func validatePages(pages []Page) error {
	if len(pages) > maxPagesPerUser {
		return fmt.Errorf("too many pages: %d > %d", len(pages), maxPagesPerUser)
	}
	seen := make(map[string]struct{}, len(pages))
	for i := range pages {
		if err := pages[i].Validate(); err != nil {
			return err
		}
		if _, exists := seen[pages[i].Name]; exists {
			return fmt.Errorf("duplicate page name %q", pages[i].Name)
		}
		seen[pages[i].Name] = struct{}{}
	}
	return nil
}

func sortPages(pages []Page) {
	sort.SliceStable(pages, func(i, j int) bool {
		if pages[i].Order != pages[j].Order {
			return pages[i].Order < pages[j].Order
		}
		return pages[i].Name < pages[j].Name
	})
}

func decodePages(raw json.RawMessage) ([]Page, error) {
	var pages []Page
	if err := json.Unmarshal(raw, &pages); err != nil {
		return nil, fmt.Errorf("corrupt page registry: %w", err)
	}
	if err := validatePages(pages); err != nil {
		return nil, fmt.Errorf("invalid page registry: %w", err)
	}
	return pages, nil
}

func (h *Handler) loadPages(ctx context.Context, user string) ([]Page, error) {
	raw, err := h.store.KVGet(ctx, user, pagesKVKey)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodePages(raw)
}

func (h *Handler) savePagesLocked(ctx context.Context, user string, pages []Page) error {
	if err := validatePages(pages); err != nil {
		return err
	}
	next := append([]Page(nil), pages...)
	sortPages(next)
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	return h.store.KVSet(ctx, user, pagesKVKey, raw)
}

func statePages(state UserState) ([]Page, error) {
	raw, ok := state.KV[pagesKVKey]
	if !ok {
		return nil, nil
	}
	return decodePages(raw)
}

func setStatePages(state *UserState, pages []Page) error {
	if err := validatePages(pages); err != nil {
		return err
	}
	next := append([]Page(nil), pages...)
	sortPages(next)
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if state.KV == nil {
		state.KV = make(map[string]json.RawMessage)
	}
	state.KV[pagesKVKey] = raw
	return nil
}

// readUserStateLocked 返回完整的一致状态。内置 Store 走单事务/单锁快照；
// 第三方旧 Store 在 Handler 用户锁保护下逐项读取，并且任何缺失都返回错误。
func (h *Handler) readUserStateLocked(ctx context.Context, user string) (UserState, error) {
	if store, ok := h.store.(UserStateStore); ok {
		state, err := store.UserState(ctx, user)
		if err != nil {
			return UserState{}, err
		}
		if state.KV == nil {
			state.KV = make(map[string]json.RawMessage)
		}
		return cloneUserState(state), nil
	}
	state := UserState{KV: make(map[string]json.RawMessage)}
	var err error
	state.Items, err = h.store.Items(ctx, user)
	if err != nil {
		return UserState{}, err
	}
	keys, err := h.store.KVKeys(ctx, user)
	if err != nil {
		return UserState{}, err
	}
	sort.Strings(keys)
	for _, key := range keys {
		value, err := h.store.KVGet(ctx, user, key)
		if err != nil {
			return UserState{}, fmt.Errorf("read kv %q: %w", key, err)
		}
		state.KV[key] = append(json.RawMessage(nil), value...)
	}
	infos, err := h.store.Revisions(ctx, user)
	if err != nil {
		return UserState{}, err
	}
	fullStore, canReadFullRevision := h.store.(RevisionStore)
	for _, info := range infos {
		var rev Revision
		if canReadFullRevision {
			rev, err = fullStore.Revision(ctx, user, info.ID)
		} else {
			rev.Items, err = h.store.RevisionItems(ctx, user, info.ID)
			rev.ID, rev.Note, rev.By, rev.CreatedAt = info.ID, info.Note, info.By, info.CreatedAt
		}
		if err != nil {
			return UserState{}, fmt.Errorf("read revision %q: %w", info.ID, err)
		}
		state.Revisions = append(state.Revisions, rev)
	}
	return state, nil
}

func applyLegacyUserState(ctx context.Context, store Store, user string, state UserState) error {
	if err := store.EraseUser(ctx, user); err != nil {
		return err
	}
	if err := store.ReplaceItems(ctx, user, state.Items); err != nil {
		return err
	}
	keys := make([]string, 0, len(state.KV))
	for key := range state.KV {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := store.KVSet(ctx, user, key, state.KV[key]); err != nil {
			return fmt.Errorf("write kv %q: %w", key, err)
		}
	}
	for i := len(state.Revisions) - 1; i >= 0; i-- {
		if err := store.AddRevision(ctx, user, state.Revisions[i], 0); err != nil {
			return fmt.Errorf("write revision %q: %w", state.Revisions[i].ID, err)
		}
	}
	return nil
}

func (h *Handler) replaceUserStateLocked(ctx context.Context, user string, before, next UserState) error {
	if store, ok := h.store.(UserStateStore); ok {
		return store.ReplaceUserState(ctx, user, next)
	}
	if err := applyLegacyUserState(ctx, h.store, user, next); err != nil {
		restoreErr := applyLegacyUserState(ctx, h.store, user, before)
		if restoreErr != nil {
			return errors.Join(fmt.Errorf("replace user state: %w", err), fmt.Errorf("restore previous state: %w", restoreErr))
		}
		return fmt.Errorf("replace user state (previous state restored): %w", err)
	}
	return nil
}

func prependRevision(state *UserState, rev Revision, keep int) {
	state.Revisions = append([]Revision{rev}, state.Revisions...)
	if keep > 0 && len(state.Revisions) > keep {
		state.Revisions = state.Revisions[:keep]
	}
}

// ListPages 实现 Service。
func (h *Handler) ListPages(ctx context.Context, user string) ([]Page, error) {
	pages, err := h.loadPages(ctx, user)
	if err != nil {
		return nil, err
	}
	sortPages(pages)
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
	oldPages := append([]Page(nil), pages...)
	catalog, err := h.loadTemplateCatalog(ctx, user, oldPages)
	if err != nil {
		return err
	}
	if page.TemplateID != "" && templateIndex(catalog.Templates, page.TemplateID) < 0 {
		return fmt.Errorf("page %q references missing template %q", page.Name, page.TemplateID)
	}
	found := false
	for i := range pages {
		if pages[i].Name == page.Name {
			if pages[i] == page {
				return nil
			}
			pages[i] = page
			found = true
			break
		}
	}
	if !found {
		if len(pages) >= maxPagesPerUser {
			return fmt.Errorf("too many pages")
		}
		pages = append(pages, page)
	}
	items, err := h.store.Items(ctx, user)
	if err != nil {
		return err
	}
	if err := validateItems(items); err != nil {
		return fmt.Errorf("stored items are invalid: %w", err)
	}
	by := mutationSourceFromContext(ctx)
	if by == "" {
		by = "api"
	}
	if err := h.addRevisionLocked(ctx, user, newRevision(items, oldPages, by, "set page "+page.Name, catalog)); err != nil {
		return err
	}
	if err := h.savePagesLocked(ctx, user, pages); err != nil {
		return err
	}
	h.publish(user, Event{Type: "pages"})
	h.audit(ctx, user, "page.set", by, "", map[string]any{"page": page.Name})
	return nil
}

// DeletePage 实现 Service。
func (h *Handler) DeletePage(ctx context.Context, user, name, by string) ([]string, error) {
	if err := ValidatePageName(name); err != nil {
		return nil, err
	}
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()
	before, err := h.readUserStateLocked(ctx, user)
	if err != nil {
		return nil, err
	}
	if err := validateItems(before.Items); err != nil {
		return nil, fmt.Errorf("stored items are invalid: %w", err)
	}
	pages, err := statePages(before)
	if err != nil {
		return nil, err
	}
	catalog, err := stateTemplateCatalog(before, pages)
	if err != nil {
		return nil, err
	}
	keptPages := make([]Page, 0, len(pages))
	pageFound := false
	for _, p := range pages {
		if p.Name != name {
			keptPages = append(keptPages, p)
		} else {
			pageFound = true
		}
	}
	keptItems := make([]Item, 0, len(before.Items))
	ids := make([]string, 0)
	for _, it := range before.Items {
		if it.Page == name {
			ids = append(ids, it.ID)
			continue
		}
		keptItems = append(keptItems, it)
	}
	layoutKeys := itemLayoutKeysForPage(before.KV, name)
	if !pageFound && len(ids) == 0 && len(layoutKeys) == 0 {
		return nil, nil
	}
	next := cloneUserState(before)
	next.Items = keptItems
	for _, key := range layoutKeys {
		delete(next.KV, key)
	}
	if err := setStatePages(&next, keptPages); err != nil {
		return nil, err
	}
	prependRevision(&next, newRevision(before.Items, pages, by, "delete page "+name, catalog), h.opts.maxRevisions)
	if err := h.replaceUserStateLocked(ctx, user, before, next); err != nil {
		return nil, err
	}
	// 所有广播都在原子提交成功后发生，客户端不会观察到半删除状态。
	if pageFound {
		h.publish(user, Event{Type: "pages"})
	}
	if len(ids) > 0 {
		h.publish(user, Event{Type: "items", Op: "delete", IDs: ids, By: by})
	}
	for _, key := range layoutKeys {
		h.publish(user, Event{Type: "kv", Key: key})
	}
	h.metric("writes", 1)
	h.audit(ctx, user, "page.delete", by, "", map[string]any{"page": name, "ids": ids, "layout_keys": layoutKeys})
	return ids, nil
}

// itemLayoutKeysForPage returns only well-formed editor layout records that
// explicitly belong to page. Corrupt or unrelated KV must never make page
// deletion fail or cause an unrelated record to be removed.
func itemLayoutKeysForPage(kv map[string]json.RawMessage, page string) []string {
	keys := make([]string, 0)
	for key, raw := range kv {
		if !strings.HasPrefix(strings.ToLower(key), ItemLayoutKVPrefix) {
			continue
		}
		var record struct {
			Page string `json:"page"`
		}
		if err := json.Unmarshal(raw, &record); err != nil || record.Page != page {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

/* ---------- 导出 / 导入 / 清除 (备份、迁移、模板预置、注销) ---------- */

func validateRevisionForImport(rev *Revision) error {
	if strings.TrimSpace(rev.ID) == "" || len(rev.ID) > maxRevisionIDBytes {
		return fmt.Errorf("invalid revision id %q", rev.ID)
	}
	if len(rev.Note) > maxRevisionNoteBytes || len(rev.By) > maxRevisionByBytes {
		return fmt.Errorf("revision %q note/by too long", rev.ID)
	}
	if rev.Version < 0 || rev.Version > CurrentDataVersion {
		return fmt.Errorf("revision %q uses unsupported version %d", rev.ID, rev.Version)
	}
	if err := validateItems(rev.Items); err != nil {
		return fmt.Errorf("revision %q: %w", rev.ID, err)
	}
	// 旧包若显式携带 pages（包括 []），安全地升级为 v2；真正的旧快照
	// Pages 为 nil，继续保持“回滚时不改变当前页面注册表”的兼容语义。
	if rev.Version < 2 && rev.Pages != nil {
		rev.Version = 2
	}
	if rev.Version >= 2 {
		if err := validatePages(rev.Pages); err != nil {
			return fmt.Errorf("revision %q: %w", rev.ID, err)
		}
	}
	if rev.Version < 3 && (rev.Templates != nil || rev.RootTemplateID != "" || rev.TemplateRevisionClock != 0) {
		rev.Version = 3
	}
	if rev.Version >= 3 {
		catalog := templateCatalog{
			RootTemplateID: rev.RootTemplateID, NextRevision: rev.TemplateRevisionClock,
			Templates: cloneTemplates(rev.Templates),
		}
		normalizeTemplateCatalog(&catalog)
		if err := validateTemplateCatalog(catalog, rev.Pages); err != nil {
			return fmt.Errorf("revision %q: %w", rev.ID, err)
		}
		rev.Templates = catalog.Templates
		rev.TemplateRevisionClock = catalog.NextRevision
	}
	return nil
}

func (h *Handler) importState(export *UserExport) (UserState, error) {
	if export == nil {
		return UserState{}, fmt.Errorf("nothing to import")
	}
	if export.Version < 0 || export.Version > CurrentDataVersion {
		return UserState{}, fmt.Errorf("unsupported export version %d", export.Version)
	}
	if err := validateItems(export.Items); err != nil {
		return UserState{}, err
	}
	if err := validatePages(export.Pages); err != nil {
		return UserState{}, err
	}
	catalog := templateCatalog{
		RootTemplateID: export.RootTemplateID, NextRevision: export.TemplateRevisionClock,
		Templates: cloneTemplates(export.Templates),
	}
	normalizeTemplateCatalog(&catalog)
	if err := validateTemplateCatalog(catalog, export.Pages); err != nil {
		return UserState{}, err
	}
	if h.opts.maxRevisions > 0 && len(export.Revisions) > h.opts.maxRevisions {
		return UserState{}, fmt.Errorf("too many revisions: %d > %d", len(export.Revisions), h.opts.maxRevisions)
	}
	state := UserState{
		Items:     cloneItems(export.Items),
		KV:        make(map[string]json.RawMessage, len(export.KV)+1),
		Revisions: cloneRevisions(export.Revisions),
	}
	regularKeys := 0
	for key, value := range export.KV {
		if err := validateKVKey(key); err != nil {
			return UserState{}, err
		}
		if key == pagesKVKey || key == templatesKVKey {
			return UserState{}, fmt.Errorf("kv key %q is reserved for a top-level export field", key)
		}
		limit := MaxKVValueBytes
		if strings.HasPrefix(key, reservedKVPrefix) {
			limit = maxInternalKVValueSize
		} else {
			regularKeys++
		}
		if len(value) > limit {
			return UserState{}, fmt.Errorf("kv value %q exceeds %d bytes", key, limit)
		}
		if !json.Valid(value) {
			return UserState{}, fmt.Errorf("kv value %q must be valid JSON", key)
		}
		state.KV[key] = append(json.RawMessage(nil), value...)
	}
	if limit := h.opts.limits.MaxKVKeysPerUser; limit > 0 && regularKeys > limit {
		return UserState{}, fmt.Errorf("kv keys quota exceeded: %d > %d", regularKeys, limit)
	}
	if err := setStatePages(&state, export.Pages); err != nil {
		return UserState{}, err
	}
	if err := setStateTemplateCatalog(&state, catalog, export.Pages); err != nil {
		return UserState{}, err
	}
	seenRevisions := make(map[string]struct{}, len(state.Revisions))
	for i := range state.Revisions {
		if err := validateRevisionForImport(&state.Revisions[i]); err != nil {
			return UserState{}, err
		}
		if _, exists := seenRevisions[state.Revisions[i].ID]; exists {
			return UserState{}, fmt.Errorf("duplicate revision id %q", state.Revisions[i].ID)
		}
		seenRevisions[state.Revisions[i].ID] = struct{}{}
	}
	return state, nil
}

// ExportUser 导出该用户全量数据(UserExport 即备份契约)。
func (h *Handler) ExportUser(ctx context.Context, user string) (*UserExport, error) {
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()

	state, err := h.readUserStateLocked(ctx, user)
	if err != nil {
		return nil, err
	}
	if err := validateItems(state.Items); err != nil {
		return nil, fmt.Errorf("stored items are invalid: %w", err)
	}
	pages, err := statePages(state)
	if err != nil {
		return nil, err
	}
	catalog, err := stateTemplateCatalog(state, pages)
	if err != nil {
		return nil, err
	}
	delete(state.KV, pagesKVKey)     // 页面注册表以 Pages 字段导出
	delete(state.KV, templatesKVKey) // 模板目录以强类型字段导出
	for key, value := range state.KV {
		if !json.Valid(value) {
			return nil, fmt.Errorf("stored kv %q is not valid JSON", key)
		}
	}
	sortItems(state.Items)
	sortPages(pages)
	sortTemplates(catalog.Templates)
	export := &UserExport{
		Version: CurrentDataVersion, Items: state.Items, Pages: pages,
		Templates: catalog.Templates, RootTemplateID: catalog.RootTemplateID,
		TemplateRevisionClock: catalog.NextRevision,
		KV:                    state.KV, Revisions: state.Revisions,
	}
	if _, err := h.importState(export); err != nil {
		return nil, fmt.Errorf("stored user state is not a valid export: %w", err)
	}
	return export, nil
}

// ImportUser 用 UserExport 整体替换该用户数据(先清场再写入)。
// 用途: 备份恢复、环境迁移、新用户套模板。
func (h *Handler) ImportUser(ctx context.Context, user string, export *UserExport) error {
	next, err := h.importState(export)
	if err != nil {
		return err
	}
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()

	before, err := h.readUserStateLocked(ctx, user)
	if err != nil {
		return err
	}
	beforePages, err := statePages(before)
	if err != nil {
		return err
	}
	beforeCatalog, err := stateTemplateCatalog(before, beforePages)
	if err != nil {
		return err
	}
	nextPages, err := statePages(next)
	if err != nil {
		return err
	}
	nextCatalog, err := stateTemplateCatalog(next, nextPages)
	if err != nil {
		return err
	}
	if err := rebaseRestoredTemplateCatalog(&nextCatalog, beforeCatalog); err != nil {
		return err
	}
	if err := setStateTemplateCatalog(&next, nextCatalog, nextPages); err != nil {
		return err
	}
	if err := h.replaceUserStateLocked(ctx, user, before, next); err != nil {
		return err
	}
	h.publish(user, Event{Type: "items", Op: "import", By: "import"})
	h.publish(user, Event{Type: "pages"})
	h.publish(user, Event{Type: "templates", Op: "import", By: "import"})
	h.metric("writes", 1)
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
	h.publish(user, Event{Type: "templates", Op: "purge", By: "api"})
	h.audit(ctx, user, "user.purge", "api", "", nil)
	return nil
}
