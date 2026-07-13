package ihtml

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	// KVGet 读取用户级 KV, 不存在返回 ErrNotFound。ReservedKVPrefix 下的
	// 内核键不属于公共 KV 能力，返回 ErrReservedKVKey。
	KVGet(ctx context.Context, user, key string) (json.RawMessage, error)
	// KVSet 写入用户级 KV 并广播变更事件；保留键返回 ErrReservedKVKey。
	KVSet(ctx context.Context, user, key string, value json.RawMessage) error
	// KVDelete 删除用户级 KV 并广播变更事件；保留键返回 ErrReservedKVKey。
	KVDelete(ctx context.Context, user, key string) error
}

// VersionedKVService 是 KVService 的可选增强能力。它把单个键的读取版本与
// 条件写暴露给 SDK 消费者，适合编辑器、多标签页等不能接受静默覆盖的场景。
// 这是独立接口，避免给已有第三方 KVService 实现增加破坏性方法要求。
type VersionedKVService interface {
	// KVGetWithRevision 返回值及其不透明版本令牌；键不存在仍返回 ErrNotFound。
	KVGetWithRevision(ctx context.Context, user, key string) (json.RawMessage, string, error)
	// KVCompareAndSet 仅在当前版本等于 expectedRevision 时原子写入，并返回
	// 新版本。读取时不存在的键使用 MissingKVRevision 作为 expectedRevision。
	KVCompareAndSet(ctx context.Context, user, key string, value json.RawMessage, expectedRevision string) (string, error)
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

// ReservedKVPrefix identifies kernel-owned keys that are intentionally hidden
// from the public KVService. Use the dedicated Page, Template, Error, and
// layout APIs instead of reading or mutating these records directly.
const ReservedKVPrefix = "__ihtml"

const reservedKVPrefix = ReservedKVPrefix

// ErrReservedKVKey is returned by the public KVService when a caller attempts
// to read, write, or delete a kernel-owned key.
var ErrReservedKVKey = errors.New("ihtml: reserved kv key")

var kvKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._\-]{1,64}$`)

// MaxKVValueBytes 是单个 KV 值的上限。
const MaxKVValueBytes = 64 << 10

const (
	// MaxKVEntriesPerUser is a hard structural ceiling across public and
	// kernel-owned keys. Limits.MaxKVKeysPerUser remains the configurable public
	// quota; this ceiling keeps an explicitly unlimited or malformed Store from
	// creating an unbounded map/list operation.
	MaxKVEntriesPerUser = 4096
	// MaxUserKVBytes bounds aggregate key/value source bytes copied in a complete
	// UserState. It is deliberately above the default 256 x 64 KiB public quota
	// while remaining finite for third-party Store implementations.
	MaxUserKVBytes = 32 << 20
)

// userLock 返回该用户的互斥锁(固定分桶, 无内存泄漏), 用于串行化"读-改-写"。
// 它只覆盖当前 Handler；VersionedUserStateStore 通过 CAS+重试处理跨副本
// 交错。只有 legacy/non-versioned Store 才仍需 sticky routing 或外部互斥。
func (h *Handler) userLock(user string) *sync.Mutex {
	f := fnv.New32a()
	_, _ = f.Write([]byte(user))
	return &h.lockShards[f.Sum32()%uint32(len(h.lockShards))]
}

// publish 统一出口: 计数 + 交给 Broadcaster。
func (h *Handler) publish(user string, ev Event) {
	h.publishEvents(user, ev)
}

// publishEvents 发布同一次权威提交产生的事件。内置 hub（或宿主
// 显式声明为同序的 Broadcaster）保留每个增量；安全默认的外部
// Broadcaster 将同一提交合并为一条 resync，避免客户端对一次变更
// 连续执行多次完整刷新。
func (h *Handler) publishEvents(user string, events ...Event) {
	if len(events) == 0 {
		return
	}
	if _, ordered := h.opts.broadcaster.(orderedIncrementalBroadcaster); ordered {
		for _, ev := range events {
			h.metric("events_published", 1)
			h.opts.broadcaster.Publish(user, ev)
		}
		return
	}

	// Store 提交和外部消息发布不可能在同一个原子边界内。若 A 先提交
	// 却晚于 B 发布，转发 A 的增量载荷会把客户端回滚到旧状态。
	// 外部 Broadcaster 只发权威 resync，由客户端串行重读已提交状态。
	reason := ResyncExternal
	if len(events) == 1 && events[0].Type == EventResync && events[0].Reason != "" {
		reason = events[0].Reason
	}
	h.metric("events_published", 1)
	h.opts.broadcaster.Publish(user, Event{Type: EventResync, Reason: reason})
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
	if err := validateItems(items); err != nil {
		return nil, fmt.Errorf("stored items are invalid: %w", err)
	}
	items = cloneItems(items)
	sortItems(items)
	return items, nil
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
	totalBytes := 0
	for i := range items {
		if err := items[i].Validate(); err != nil {
			return err
		}
		itemBytes := itemSourceBytes(items[i])
		if itemBytes > MaxUserItemBytes-totalBytes {
			return fmt.Errorf("item data exceeds %d bytes per user", MaxUserItemBytes)
		}
		totalBytes += itemBytes
		if _, dup := seen[items[i].ID]; dup {
			return fmt.Errorf("duplicate item id %q", items[i].ID)
		}
		seen[items[i].ID] = struct{}{}
	}
	return nil
}

func itemSourceBytes(item Item) int {
	total := len(item.ID) + len(item.Title) + len(item.Page) + len(item.Content)
	for key, value := range item.Meta {
		total += len(key) + len(value)
	}
	return total
}

func revisionSourceBytes(rev Revision) int {
	total := 256 + len(rev.ID) + len(rev.Note) + len(rev.By) + len(rev.RootTemplateID)
	for _, item := range rev.Items {
		total += itemSourceBytes(item)
	}
	for _, page := range rev.Pages {
		total += 64 + len(page.Name) + len(page.Title) + len(page.Icon) + len(page.TemplateID)
	}
	for i := range rev.Templates {
		template := &rev.Templates[i]
		total += 256 + len(template.ID) + len(template.Name) + templateVersionSize(&template.Draft)
		if template.Published != nil {
			total += templateVersionSize(template.Published)
		}
	}
	return total
}

func validateStoredKV(kv map[string]json.RawMessage, publicLimit int) error {
	if len(kv) > MaxKVEntriesPerUser {
		return fmt.Errorf("too many kv entries: %d > %d", len(kv), MaxKVEntriesPerUser)
	}
	publicKeys := 0
	totalBytes := 0
	for key, value := range kv {
		if err := validateKVKey(key); err != nil {
			return err
		}
		valueLimit := storedKVValueLimit(key)
		if !strings.HasPrefix(key, reservedKVPrefix) {
			publicKeys++
		}
		if len(value) > valueLimit {
			return fmt.Errorf("kv value %q exceeds %d bytes", key, valueLimit)
		}
		size := len(key) + len(value)
		if size > MaxUserKVBytes-totalBytes {
			return fmt.Errorf("kv data exceeds %d bytes per user", MaxUserKVBytes)
		}
		totalBytes += size
	}
	if publicLimit > 0 && publicKeys > publicLimit {
		return fmt.Errorf("kv keys quota exceeded: %d > %d", publicKeys, publicLimit)
	}
	return nil
}

func storedKVValueLimit(key string) int {
	if key == templatesKVKey {
		return MaxTemplateCatalogStorageBytes
	}
	if strings.HasPrefix(key, reservedKVPrefix) {
		return maxInternalKVValueSize
	}
	return MaxKVValueBytes
}

func validateStoredRevisions(revisions []Revision) error {
	if len(revisions) > MaxRevisionsPerUser {
		return fmt.Errorf("too many revisions: %d > %d", len(revisions), MaxRevisionsPerUser)
	}
	seen := make(map[string]struct{}, len(revisions))
	totalBytes := 0
	for i := range revisions {
		// Import validation performs legacy-version normalization. Validate a local
		// header copy so reading a third-party Store never mutates its returned
		// object while still applying the exact public backup contract.
		candidate := revisions[i]
		if err := validateRevisionForImport(&candidate); err != nil {
			return fmt.Errorf("stored revision is invalid: %w", err)
		}
		if _, duplicate := seen[candidate.ID]; duplicate {
			return fmt.Errorf("duplicate revision id %q", candidate.ID)
		}
		seen[candidate.ID] = struct{}{}
		size := revisionSourceBytes(candidate)
		if size > MaxUserRevisionBytes-totalBytes {
			return fmt.Errorf("revisions exceed %d bytes per user", MaxUserRevisionBytes)
		}
		totalBytes += size
	}
	return nil
}

func validateStoredUserState(state UserState, publicKVLimit int) error {
	if err := validateItems(state.Items); err != nil {
		return fmt.Errorf("stored items are invalid: %w", err)
	}
	if err := validateStoredKV(state.KV, publicKVLimit); err != nil {
		return fmt.Errorf("stored kv is invalid: %w", err)
	}
	pages, err := statePages(state)
	if err != nil {
		return err
	}
	if _, err := stateTemplateCatalog(state, pages); err != nil {
		return err
	}
	return validateStoredRevisions(state.Revisions)
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

	var written []Item
	err := h.updateUserStateLocked(ctx, user, func(before UserState) (UserState, error) {
		existing := before.Items
		if err := validateItems(existing); err != nil {
			return UserState{}, fmt.Errorf("stored items are invalid: %w", err)
		}
		now := time.Now()
		index := make(map[string]int, len(existing))
		for i := range existing {
			index[existing[i].ID] = i
		}
		merged := cloneItems(existing)
		written = make([]Item, 0, len(items))
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
		if err := validateItems(merged); err != nil {
			return UserState{}, err
		}
		if len(written) == 0 {
			return UserState{}, errUserStateUnchanged
		}
		next, err := nextStateWithSnapshot(before, by, note, h.opts.maxRevisions)
		if err != nil {
			return UserState{}, err
		}
		next.Items = merged
		return next, nil
	})
	if err != nil || len(written) == 0 {
		return err
	}
	sortItems(written)
	h.metric("writes", 1)
	h.publish(user, Event{Type: "items", Op: "put", Items: written, By: by})
	h.audit(ctx, user, "items.put", by, note, map[string]any{"ids": itemIDList(written)})
	return nil
}

// nextStateWithSnapshot 克隆 before 并前置一条完整快照, 供各写路径把
// "先快照后写入"合并为对 Store 的单次原子替换。
func nextStateWithSnapshot(before UserState, by, note string, keep int) (UserState, error) {
	pages, err := statePages(before)
	if err != nil {
		return UserState{}, err
	}
	catalog, err := stateTemplateCatalog(before, pages)
	if err != nil {
		return UserState{}, err
	}
	next := cloneUserState(before)
	prependRevision(&next, newRevision(before.Items, pages, by, note, catalog), keep)
	return next, nil
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

	var removed []string
	var cleanedLayoutKeys []string
	err := h.updateUserStateLocked(ctx, user, func(before UserState) (UserState, error) {
		existing := before.Items
		if err := validateItems(existing); err != nil {
			return UserState{}, fmt.Errorf("stored items are invalid: %w", err)
		}
		drop := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			drop[strings.TrimSpace(id)] = struct{}{}
		}
		kept := make([]Item, 0, len(existing))
		removed = make([]string, 0, len(ids))
		for _, it := range existing {
			if _, ok := drop[it.ID]; ok {
				removed = append(removed, it.ID)
				continue
			}
			kept = append(kept, it)
		}
		if len(removed) == 0 {
			return UserState{}, errUserStateUnchanged
		}
		removedSet := make(map[string]struct{}, len(removed))
		for _, id := range removed {
			removedSet[id] = struct{}{}
		}
		next, err := nextStateWithSnapshot(before, by, note, h.opts.maxRevisions)
		if err != nil {
			return UserState{}, err
		}
		next.Items = kept
		cleanedLayoutKeys = cleanedLayoutKeys[:0]
		for key, raw := range next.KV {
			if !strings.HasPrefix(strings.ToLower(key), ItemLayoutKVPrefix) {
				continue
			}
			cleaned, changed, err := removeItemIDsFromLayout(raw, removedSet)
			if err != nil {
				return UserState{}, fmt.Errorf("clean item layout %q: %w", key, err)
			}
			if changed {
				next.KV[key] = cleaned
				cleanedLayoutKeys = append(cleanedLayoutKeys, key)
			}
		}
		return next, nil
	})
	if err != nil || len(removed) == 0 {
		return nil, err
	}
	h.metric("writes", 1)
	events := []Event{{Type: "items", Op: "delete", IDs: removed, By: by}}
	sort.Strings(cleanedLayoutKeys)
	for _, key := range cleanedLayoutKeys {
		events = append(events, Event{Type: "kv", Key: key, By: by})
	}
	h.publishEvents(user, events...)
	h.audit(ctx, user, "items.delete", by, note, map[string]any{"ids": removed})
	return removed, nil
}

// removeItemIDsFromLayout removes deleted Item references while preserving all
// unknown top-level and per-item JSON. Malformed/non-object layout values are
// ignored here: deleting a real Item must not be blocked by an unrelated legacy
// value, and the browser loader will continue to fail closed for that value.
func removeItemIDsFromLayout(raw json.RawMessage, drop map[string]struct{}) (json.RawMessage, bool, error) {
	var doc map[string]json.RawMessage
	if len(drop) == 0 || json.Unmarshal(raw, &doc) != nil || doc == nil {
		return raw, false, nil
	}
	changed := false
	if itemsRaw, ok := doc["items"]; ok {
		var items map[string]json.RawMessage
		if json.Unmarshal(itemsRaw, &items) == nil && items != nil {
			for id := range drop {
				if _, exists := items[id]; exists {
					delete(items, id)
					changed = true
				}
			}
			if changed {
				encoded, err := marshalJSONWithoutHTMLEscaping(items)
				if err != nil {
					return nil, false, err
				}
				doc["items"] = encoded
			}
		}
	}
	if orderRaw, ok := doc["orderBySlot"]; ok {
		var order map[string]json.RawMessage
		if json.Unmarshal(orderRaw, &order) == nil && order != nil {
			orderChanged := false
			for slot, idsRaw := range order {
				var ids []string
				if json.Unmarshal(idsRaw, &ids) != nil {
					continue
				}
				kept := ids[:0]
				for _, id := range ids {
					if _, remove := drop[id]; remove {
						orderChanged = true
						continue
					}
					kept = append(kept, id)
				}
				if len(kept) == 0 {
					delete(order, slot)
				} else if len(kept) != len(ids) {
					encoded, err := marshalJSONWithoutHTMLEscaping(kept)
					if err != nil {
						return nil, false, err
					}
					order[slot] = encoded
				}
			}
			if orderChanged {
				encoded, err := marshalJSONWithoutHTMLEscaping(order)
				if err != nil {
					return nil, false, err
				}
				doc["orderBySlot"] = encoded
				changed = true
			}
		}
	}
	if !changed {
		return raw, false, nil
	}
	encoded, err := marshalJSONWithoutHTMLEscaping(doc)
	if err != nil {
		return nil, false, err
	}
	if len(encoded) > MaxKVValueBytes {
		return nil, false, fmt.Errorf("cleaned layout exceeds %d bytes", MaxKVValueBytes)
	}
	return encoded, true, nil
}

// marshalJSONWithoutHTMLEscaping keeps opaque layout fields byte-efficient.
// encoding/json's default HTML escaping can turn each literal '<', '>' or '&'
// into six bytes; re-encoding a valid near-limit layout during cleanup must not
// make an unrelated opaque field exceed the public KV ceiling and block Item
// deletion. Encoder.Encode appends exactly one trailing newline, which is not
// part of the JSON value persisted by KVSet.
func marshalJSONWithoutHTMLEscaping(value any) (json.RawMessage, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	encoded := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
	return append(json.RawMessage(nil), encoded...), nil
}

// ReplaceItems 实现 Service。
func (h *Handler) ReplaceItems(ctx context.Context, user string, items []Item, by, note string) error {
	if err := validateItems(items); err != nil {
		return err
	}
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()

	wrote := false
	err := h.updateUserStateLocked(ctx, user, func(before UserState) (UserState, error) {
		wrote = false
		existing := before.Items
		if err := validateItems(existing); err != nil {
			return UserState{}, fmt.Errorf("stored items are invalid: %w", err)
		}
		existingByID := make(map[string]Item, len(existing))
		for _, it := range existing {
			existingByID[it.ID] = it
		}
		now := time.Now()
		replaced := cloneItems(items)
		changed := len(replaced) != len(existing)
		for i := range replaced {
			if old, ok := existingByID[replaced[i].ID]; ok && sameItemContent(old, replaced[i]) {
				replaced[i].UpdatedAt = old.UpdatedAt
				continue
			}
			replaced[i].UpdatedAt = now
			changed = true
		}
		if !changed {
			return UserState{}, errUserStateUnchanged
		}
		next, err := nextStateWithSnapshot(before, by, note, h.opts.maxRevisions)
		if err != nil {
			return UserState{}, err
		}
		next.Items = replaced
		wrote = true
		return next, nil
	})
	if err != nil || !wrote {
		return err
	}
	h.metric("writes", 1)
	h.publish(user, Event{Type: "items", Op: "replace", By: by})
	h.audit(ctx, user, "items.replace", by, note, map[string]any{"count": len(items)})
	return nil
}

// ListRevisions 实现 Service。
func (h *Handler) ListRevisions(ctx context.Context, user string) ([]RevisionInfo, error) {
	revisions, err := h.store.Revisions(ctx, user)
	if err != nil {
		return nil, err
	}
	if err := validateRevisionInfos(revisions); err != nil {
		return nil, err
	}
	return append([]RevisionInfo(nil), revisions...), nil
}

func validateRevisionInfos(revisions []RevisionInfo) error {
	if len(revisions) > MaxRevisionsPerUser {
		return fmt.Errorf("too many revisions: %d > %d", len(revisions), MaxRevisionsPerUser)
	}
	seen := make(map[string]struct{}, len(revisions))
	for _, revision := range revisions {
		if strings.TrimSpace(revision.ID) == "" || len(revision.ID) > maxRevisionIDBytes ||
			len(revision.Note) > maxRevisionNoteBytes || len(revision.By) > maxRevisionByBytes ||
			revision.ItemCount < 0 || revision.ItemCount > MaxItemsPerUser ||
			revision.TemplateCount < 0 || revision.TemplateCount > maxTemplatesPerUser {
			return fmt.Errorf("stored revision metadata %q is invalid", revision.ID)
		}
		if _, duplicate := seen[revision.ID]; duplicate {
			return fmt.Errorf("duplicate revision id %q", revision.ID)
		}
		seen[revision.ID] = struct{}{}
	}
	return nil
}

// RevisionItems 实现 Service。
func (h *Handler) RevisionItems(ctx context.Context, user, id string) ([]Item, error) {
	items, err := h.store.RevisionItems(ctx, user, id)
	if err != nil {
		return nil, err
	}
	if err := validateItems(items); err != nil {
		return nil, fmt.Errorf("stored revision items are invalid: %w", err)
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

	var restorePages, restoreTemplates bool
	err := h.updateUserStateLocked(ctx, user, func(before UserState) (UserState, error) {
		var target *Revision
		for i := range before.Revisions {
			if before.Revisions[i].ID == revisionID {
				target = &before.Revisions[i]
				break
			}
		}
		if target == nil {
			return UserState{}, ErrNotFound
		}
		if target.Version < 0 || target.Version > CurrentDataVersion {
			return UserState{}, fmt.Errorf("revision %q uses unsupported version %d", revisionID, target.Version)
		}
		if err := validateItems(target.Items); err != nil {
			return UserState{}, fmt.Errorf("invalid revision %q: %w", revisionID, err)
		}
		currentPages, err := statePages(before)
		if err != nil {
			return UserState{}, err
		}
		currentCatalog, err := stateTemplateCatalog(before, currentPages)
		if err != nil {
			return UserState{}, err
		}
		next := cloneUserState(before)
		next.Items = cloneItems(target.Items)
		restorePages = target.Version >= 2
		if restorePages {
			if err := setStatePages(&next, target.Pages); err != nil {
				return UserState{}, fmt.Errorf("invalid revision %q pages: %w", revisionID, err)
			}
		}
		restoreTemplates = target.Version >= 3
		if restoreTemplates {
			targetCatalog := templateCatalog{
				RootTemplateID: target.RootTemplateID, NextRevision: target.TemplateRevisionClock,
				Templates: cloneTemplates(target.Templates),
			}
			if err := rebaseRestoredTemplateCatalog(&targetCatalog, currentCatalog); err != nil {
				return UserState{}, fmt.Errorf("restore revision %q template revisions: %w", revisionID, err)
			}
			targetPages := currentPages
			if restorePages {
				targetPages = target.Pages
			}
			if err := setStateTemplateCatalog(&next, targetCatalog, targetPages); err != nil {
				return UserState{}, fmt.Errorf("invalid revision %q templates: %w", revisionID, err)
			}
		}
		prependRevision(&next, newRevision(before.Items, currentPages, by, "rollback to "+revisionID, currentCatalog), h.opts.maxRevisions)
		return next, nil
	})
	if err != nil {
		return err
	}
	h.metric("writes", 1)
	events := []Event{{Type: "items", Op: "rollback", By: by}}
	if restorePages {
		events = append(events, Event{Type: "pages"})
	}
	if restoreTemplates {
		events = append(events, Event{Type: "templates", Op: "rollback", By: by})
	}
	h.publishEvents(user, events...)
	h.audit(ctx, user, "items.rollback", by, "rollback to "+revisionID, nil)
	return nil
}

func validateKVKey(key string) error {
	if !kvKeyPattern.MatchString(key) {
		return fmt.Errorf("invalid kv key %q", key)
	}
	return nil
}

func validatePublicKVKey(key string) error {
	if err := validateKVKey(key); err != nil {
		return err
	}
	if strings.HasPrefix(key, reservedKVPrefix) {
		return fmt.Errorf("%w: %q", ErrReservedKVKey, key)
	}
	return nil
}

// MissingKVRevision 是 KVGetWithRevision 读取一个不存在的键时，调用方在首次
// KVCompareAndSet 中使用的版本前置条件。它与已有值的 SHA-256 版本空间分离。
const MissingKVRevision = "missing"

var kvRevisionPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func kvValueRevision(value json.RawMessage) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateKVRevision(revision string) error {
	if revision == MissingKVRevision || kvRevisionPattern.MatchString(revision) {
		return nil
	}
	return fmt.Errorf("invalid kv revision")
}

func newKVConflict(key, expected, actual string, current json.RawMessage, exists bool) error {
	return &KVConflictError{
		Key: key, Expected: expected, Actual: actual,
		Current: append(json.RawMessage(nil), current...), Exists: exists,
	}
}

// KVGet 实现 Service。
func (h *Handler) KVGet(ctx context.Context, user, key string) (json.RawMessage, error) {
	if err := validatePublicKVKey(key); err != nil {
		return nil, err
	}
	value, err := h.store.KVGet(ctx, user, key)
	if err != nil {
		return nil, err
	}
	if len(value) > MaxKVValueBytes {
		return nil, fmt.Errorf("stored kv value exceeds %d bytes", MaxKVValueBytes)
	}
	if !json.Valid(value) {
		return nil, fmt.Errorf("stored kv value is invalid JSON")
	}
	return append(json.RawMessage(nil), value...), nil
}

// KVGetWithRevision 原子读取语义由底层 KVGet 提供，并为逐字节值生成一个
// 不透明版本令牌。令牌只用于 KVCompareAndSet，调用方不应解析或自行构造。
func (h *Handler) KVGetWithRevision(ctx context.Context, user, key string) (json.RawMessage, string, error) {
	value, err := h.KVGet(ctx, user, key)
	if err != nil {
		return nil, "", err
	}
	return value, kvValueRevision(value), nil
}

// KVCompareAndSet 仅当 key 当前值仍等于 expectedRevision 所代表的快照时写入。
// VersionedUserStateStore 路径把比较和写入放在同一次状态 CAS 中，因此跨 Handler /
// 跨副本也不会静默覆盖；旧 Store 仍在当前 Handler 的用户锁内提供原子语义。
func (h *Handler) KVCompareAndSet(ctx context.Context, user, key string, value json.RawMessage, expectedRevision string) (string, error) {
	if err := validatePublicKVKey(key); err != nil {
		return "", err
	}
	if err := validateKVRevision(expectedRevision); err != nil {
		return "", err
	}
	if len(value) > MaxKVValueBytes {
		return "", fmt.Errorf("kv value exceeds %d bytes", MaxKVValueBytes)
	}
	if !json.Valid(value) {
		return "", fmt.Errorf("kv value must be valid JSON")
	}

	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()

	wrote := false
	if _, versioned := h.store.(VersionedUserStateStore); versioned {
		err := h.updateUserStateLocked(ctx, user, func(before UserState) (UserState, error) {
			wrote = false
			current, exists := before.KV[key]
			currentRevision := MissingKVRevision
			if exists {
				currentRevision = kvValueRevision(current)
			}
			if currentRevision != expectedRevision {
				return UserState{}, newKVConflict(key, expectedRevision, currentRevision, current, exists)
			}
			if exists && bytes.Equal(current, value) {
				return UserState{}, errUserStateUnchanged
			}
			if err := checkKVQuota(before.KV, key, h.opts.limits.MaxKVKeysPerUser); err != nil {
				return UserState{}, err
			}
			next := cloneUserState(before)
			next.KV[key] = append(json.RawMessage(nil), value...)
			wrote = true
			return next, nil
		})
		if err != nil {
			return "", err
		}
	} else {
		current, err := h.store.KVGet(ctx, user, key)
		exists := err == nil
		if err != nil && !errors.Is(err, ErrNotFound) {
			return "", err
		}
		currentRevision := MissingKVRevision
		if exists {
			if len(current) > MaxKVValueBytes || !json.Valid(current) {
				return "", fmt.Errorf("stored kv value is invalid or exceeds %d bytes", MaxKVValueBytes)
			}
			currentRevision = kvValueRevision(current)
		}
		if currentRevision != expectedRevision {
			return "", newKVConflict(key, expectedRevision, currentRevision, current, exists)
		}
		if !exists || !bytes.Equal(current, value) {
			if err := h.checkLegacyKVQuota(ctx, user, key); err != nil {
				return "", err
			}
			if err := h.store.KVSet(ctx, user, key, value); err != nil {
				return "", err
			}
			wrote = true
		}
	}

	revision := kvValueRevision(value)
	if wrote {
		h.metric("writes", 1)
		h.publish(user, Event{Type: "kv", Key: key})
		h.audit(ctx, user, "kv.compare_and_set", "", "", map[string]any{"key": key})
	}
	return revision, nil
}

// KVSet 实现 Service。
func (h *Handler) KVSet(ctx context.Context, user, key string, value json.RawMessage) error {
	if err := validatePublicKVKey(key); err != nil {
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

	if _, versioned := h.store.(VersionedUserStateStore); versioned {
		// 配额判定与新键写入必须基于同一个版本并条件提交；
		// 否则两个 Handler 都可能在上限前读到相同键数并各自写入。
		if err := h.updateUserStateLocked(ctx, user, func(before UserState) (UserState, error) {
			if err := checkKVQuota(before.KV, key, h.opts.limits.MaxKVKeysPerUser); err != nil {
				return UserState{}, err
			}
			next := cloneUserState(before)
			next.KV[key] = append(json.RawMessage(nil), value...)
			return next, nil
		}); err != nil {
			return err
		}
	} else {
		// 旧 Store 无条件写能力，保留历史路径和进程内 userLock 语义。
		if err := h.checkLegacyKVQuota(ctx, user, key); err != nil {
			return err
		}
		if err := h.store.KVSet(ctx, user, key, value); err != nil {
			return err
		}
	}
	h.metric("writes", 1)
	h.publish(user, Event{Type: "kv", Key: key})
	h.audit(ctx, user, "kv.set", "", "", map[string]any{"key": key})
	return nil
}

// checkKVQuota 在一个已读取的完整状态上判定新键配额。
func checkKVQuota(kv map[string]json.RawMessage, key string, limit int) error {
	if limit <= 0 || strings.HasPrefix(key, reservedKVPrefix) {
		return nil
	}
	if _, exists := kv[key]; exists {
		return nil
	}
	n := 0
	for existing := range kv {
		if !strings.HasPrefix(existing, reservedKVPrefix) {
			n++
		}
	}
	if n >= limit {
		return fmt.Errorf("kv keys quota exceeded (%d); 请清理不再使用的键", limit)
	}
	return nil
}

func (h *Handler) checkLegacyKVQuota(ctx context.Context, user, key string) error {
	if strings.HasPrefix(key, reservedKVPrefix) {
		return nil
	}
	keys, err := h.store.KVKeys(ctx, user)
	if err != nil {
		return err
	}
	if len(keys) > MaxKVEntriesPerUser {
		return fmt.Errorf("too many kv entries: %d > %d", len(keys), MaxKVEntriesPerUser)
	}
	seen := make(map[string]struct{}, len(keys))
	n := 0
	exists := false
	for _, existing := range keys {
		if err := validateKVKey(existing); err != nil {
			return fmt.Errorf("stored kv key is invalid: %w", err)
		}
		if _, duplicate := seen[existing]; duplicate {
			return fmt.Errorf("duplicate stored kv key %q", existing)
		}
		seen[existing] = struct{}{}
		if existing == key {
			exists = true
		}
		if !strings.HasPrefix(existing, reservedKVPrefix) {
			n++
		}
	}
	if exists {
		return nil
	}
	if limit := h.opts.limits.MaxKVKeysPerUser; limit > 0 && n >= limit {
		return fmt.Errorf("kv keys quota exceeded (%d); 请清理不再使用的键", limit)
	}
	return nil
}

// KVDelete 实现 Service。
func (h *Handler) KVDelete(ctx context.Context, user, key string) error {
	if err := validatePublicKVKey(key); err != nil {
		return err
	}
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()
	if err := h.store.KVDelete(ctx, user, key); err != nil {
		return err
	}
	h.metric("writes", 1)
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
	return decodePageErrors(raw)
}

func decodePageErrors(raw json.RawMessage) ([]PageError, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > maxInternalKVValueSize {
		return nil, fmt.Errorf("page error log exceeds %d bytes", maxInternalKVValueSize)
	}
	var out []PageError
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("corrupt page error log: %w", err)
	}
	if len(out) > MaxPageErrors {
		return nil, fmt.Errorf("page error log has too many entries: %d > %d", len(out), MaxPageErrors)
	}
	for i := range out {
		if len(out[i].Message) > 2048 || len(out[i].Stack) > 4096 || len(out[i].URL) > 512 || len(out[i].ItemID) > 128 {
			return nil, fmt.Errorf("page error log entry %d exceeds field limits", i)
		}
	}
	return out, nil
}

func (h *Handler) recordReportedErrors(count int) {
	h.metric("errors_reported", int64(count))
	h.metric("writes", 1)
}

// ReportErrors 实现 Service。
func (h *Handler) ReportErrors(ctx context.Context, user string, errs []PageError) error {
	if len(errs) == 0 {
		return nil
	}
	// 不修改调用方切片；CAS 冲突重试也始终使用同一份规范化输入。
	normalized := append([]PageError(nil), errs...)
	now := time.Now()
	for i := range normalized {
		if normalized[i].Time.IsZero() {
			normalized[i].Time = now
		}
		normalized[i].Message = clip(normalized[i].Message, 2048)
		normalized[i].Stack = clip(normalized[i].Stack, 4096)
		normalized[i].URL = clip(normalized[i].URL, 512)
		normalized[i].ItemID = clip(normalized[i].ItemID, 128)
	}

	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()

	merge := func(current []PageError) (json.RawMessage, error) {
		merged := append(append([]PageError(nil), normalized...), current...)
		if len(merged) > MaxPageErrors {
			merged = merged[:MaxPageErrors]
		}
		return json.Marshal(merged)
	}

	if _, versioned := h.store.(VersionedUserStateStore); versioned {
		if err := h.updateUserStateLocked(ctx, user, func(before UserState) (UserState, error) {
			current, err := decodePageErrors(before.KV[errorsKVKey])
			if err != nil {
				return UserState{}, err
			}
			raw, err := merge(current)
			if err != nil {
				return UserState{}, err
			}
			next := cloneUserState(before)
			next.KV[errorsKVKey] = raw
			return next, nil
		}); err != nil {
			return err
		}
		h.recordReportedErrors(len(normalized))
		return nil
	}

	// 旧 Store 仍用进程内锁保护历史读改写路径。
	current, err := h.PageErrors(ctx, user)
	if err != nil {
		return err
	}
	raw, err := merge(current)
	if err != nil {
		return err
	}
	if err := h.store.KVSet(ctx, user, errorsKVKey, raw); err != nil {
		return err
	}
	h.recordReportedErrors(len(normalized))
	return nil
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
	if len(raw) > maxInternalKVValueSize {
		return nil, fmt.Errorf("page registry exceeds %d bytes", maxInternalKVValueSize)
	}
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
		// Configurable public-key quota is admission control, not stored-data
		// validity: lowering it must still allow reads and cleanup of old keys.
		if err := validateStoredUserState(state, 0); err != nil {
			return UserState{}, err
		}
		return cloneUserState(state), nil
	}
	state := UserState{KV: make(map[string]json.RawMessage)}
	var err error
	state.Items, err = h.store.Items(ctx, user)
	if err != nil {
		return UserState{}, err
	}
	if err := validateItems(state.Items); err != nil {
		return UserState{}, fmt.Errorf("stored items are invalid: %w", err)
	}
	keys, err := h.store.KVKeys(ctx, user)
	if err != nil {
		return UserState{}, err
	}
	if len(keys) > MaxKVEntriesPerUser {
		return UserState{}, fmt.Errorf("too many kv entries: %d > %d", len(keys), MaxKVEntriesPerUser)
	}
	for _, key := range keys {
		if err := validateKVKey(key); err != nil {
			return UserState{}, fmt.Errorf("stored kv key is invalid: %w", err)
		}
	}
	sort.Strings(keys)
	for index := 1; index < len(keys); index++ {
		if keys[index] == keys[index-1] {
			return UserState{}, fmt.Errorf("duplicate stored kv key %q", keys[index])
		}
	}
	kvBytes := 0
	for _, key := range keys {
		value, err := h.store.KVGet(ctx, user, key)
		if err != nil {
			return UserState{}, fmt.Errorf("read kv %q: %w", key, err)
		}
		valueLimit := storedKVValueLimit(key)
		if len(value) > valueLimit {
			return UserState{}, fmt.Errorf("stored kv value %q exceeds %d bytes", key, valueLimit)
		}
		if !json.Valid(value) {
			return UserState{}, fmt.Errorf("stored kv value %q is invalid JSON", key)
		}
		size := len(key) + len(value)
		if size > MaxUserKVBytes-kvBytes {
			return UserState{}, fmt.Errorf("kv data exceeds %d bytes per user", MaxUserKVBytes)
		}
		kvBytes += size
		state.KV[key] = append(json.RawMessage(nil), value...)
	}
	infos, err := h.store.Revisions(ctx, user)
	if err != nil {
		return UserState{}, err
	}
	// Validate metadata before reflecting any identifier back into a third-party
	// Store callback. Besides bounding memory, this prevents a malformed Store
	// from turning an oversized ID into a second untrusted lookup.
	if err := validateRevisionInfos(infos); err != nil {
		return UserState{}, err
	}
	fullStore, canReadFullRevision := h.store.(RevisionStore)
	revisionBytes := 0
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
		candidate := rev
		if err := validateRevisionForImport(&candidate); err != nil {
			return UserState{}, fmt.Errorf("stored revision %q is invalid: %w", info.ID, err)
		}
		if candidate.ID != info.ID {
			return UserState{}, fmt.Errorf("stored revision id mismatch: metadata %q, record %q", info.ID, candidate.ID)
		}
		size := revisionSourceBytes(candidate)
		if size > MaxUserRevisionBytes-revisionBytes {
			return UserState{}, fmt.Errorf("revisions exceed %d bytes per user", MaxUserRevisionBytes)
		}
		revisionBytes += size
		state.Revisions = append(state.Revisions, candidate)
	}
	if err := validateStoredUserState(state, 0); err != nil {
		return UserState{}, err
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
	// 条件写优先: 版本取自本次读到的 before, 任何跨副本交错写都会触发
	// ErrVersionConflict, 由 updateUserStateLocked 重读重试。
	if store, ok := h.store.(VersionedUserStateStore); ok {
		return store.ReplaceUserStateVersioned(ctx, user, next, before.Version)
	}
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

// errUserStateUnchanged 由 updateUserStateLocked 的 mutate 回调返回,
// 表示本次操作无需任何写入(不是错误)。
var errUserStateUnchanged = errors.New("ihtml: user state unchanged")

// userStateConflictAttempts 是条件写冲突的重试熔断上限。每次冲突都意味着
// 另一个副本刚成功提交(系统整体在前进), 因此重试以 context 取消为主界;
// 该上限只用于防御行为异常的 Store 造成的死循环。
const userStateConflictAttempts = 64

// updateUserStateLocked 是所有"读-改-写"变更的统一入口: 读完整状态 → mutate
// 计算下一状态(含前置快照) → 单次原子替换。调用方必须已持有 userLock。
// mutate 可能因条件写冲突被调用多次, 不得在闭包外累积副作用(应先重置再写入)。
func (h *Handler) updateUserStateLocked(ctx context.Context, user string, mutate func(before UserState) (UserState, error)) error {
	for attempt := 1; ; attempt++ {
		before, err := h.readUserStateLocked(ctx, user)
		if err != nil {
			return err
		}
		next, err := mutate(before)
		if errors.Is(err, errUserStateUnchanged) {
			return nil
		}
		if err != nil {
			return err
		}
		err = h.replaceUserStateLocked(ctx, user, before, next)
		if err == nil || !errors.Is(err, ErrVersionConflict) || attempt >= userStateConflictAttempts {
			return err
		}
		h.metric("state_conflicts", 1)
		if err := sleepJittered(ctx, attempt); err != nil {
			return errors.Join(ErrVersionConflict, err)
		}
	}
}

// sleepJittered 在冲突重试间做带抖动的指数退避(上限 ~50ms), 并响应 ctx 取消。
func sleepJittered(ctx context.Context, attempt int) error {
	base := time.Millisecond << min(attempt, 5) // 2ms..64ms 量级
	var b [2]byte
	_, _ = rand.Read(b[:])
	jitter := time.Duration(uint16(b[0])<<8|uint16(b[1])) % base
	timer := time.NewTimer(base/2 + jitter)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func prependRevision(state *UserState, rev Revision, keep int) {
	state.Revisions = append([]Revision{rev}, state.Revisions...)
	countLimit := keep
	if countLimit <= 0 || countLimit > MaxRevisionsPerUser {
		countLimit = MaxRevisionsPerUser
	}
	if len(state.Revisions) > countLimit {
		state.Revisions = state.Revisions[:countLimit]
	}
	total := 0
	kept := 0
	for i := range state.Revisions {
		size := revisionSourceBytes(state.Revisions[i])
		if size > MaxUserRevisionBytes-total {
			break
		}
		total += size
		kept = i + 1
	}
	state.Revisions = state.Revisions[:kept]
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
	by := mutationSourceFromContext(ctx)
	if by == "" {
		by = "api"
	}
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()

	wrote := false
	err := h.updateUserStateLocked(ctx, user, func(before UserState) (UserState, error) {
		wrote = false
		oldPages, err := statePages(before)
		if err != nil {
			return UserState{}, err
		}
		catalog, err := stateTemplateCatalog(before, oldPages)
		if err != nil {
			return UserState{}, err
		}
		if page.TemplateID != "" && templateIndex(catalog.Templates, page.TemplateID) < 0 {
			return UserState{}, fmt.Errorf("page %q references missing template %q", page.Name, page.TemplateID)
		}
		pages := append([]Page(nil), oldPages...)
		found := false
		for i := range pages {
			if pages[i].Name == page.Name {
				if pages[i] == page {
					return UserState{}, errUserStateUnchanged
				}
				pages[i] = page
				found = true
				break
			}
		}
		if !found {
			if len(pages) >= maxPagesPerUser {
				return UserState{}, fmt.Errorf("too many pages")
			}
			pages = append(pages, page)
		}
		if err := validateItems(before.Items); err != nil {
			return UserState{}, fmt.Errorf("stored items are invalid: %w", err)
		}
		next := cloneUserState(before)
		prependRevision(&next, newRevision(before.Items, oldPages, by, "set page "+page.Name, catalog), h.opts.maxRevisions)
		if err := setStatePages(&next, pages); err != nil {
			return UserState{}, err
		}
		wrote = true
		return next, nil
	})
	if err != nil || !wrote {
		return err
	}
	h.metric("writes", 1)
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

	var (
		pageFound  bool
		ids        []string
		layoutKeys []string
	)
	err := h.updateUserStateLocked(ctx, user, func(before UserState) (UserState, error) {
		if err := validateItems(before.Items); err != nil {
			return UserState{}, fmt.Errorf("stored items are invalid: %w", err)
		}
		pages, err := statePages(before)
		if err != nil {
			return UserState{}, err
		}
		catalog, err := stateTemplateCatalog(before, pages)
		if err != nil {
			return UserState{}, err
		}
		keptPages := make([]Page, 0, len(pages))
		pageFound = false
		for _, p := range pages {
			if p.Name != name {
				keptPages = append(keptPages, p)
			} else {
				pageFound = true
			}
		}
		keptItems := make([]Item, 0, len(before.Items))
		ids = make([]string, 0)
		for _, it := range before.Items {
			if it.Page == name {
				ids = append(ids, it.ID)
				continue
			}
			keptItems = append(keptItems, it)
		}
		layoutKeys = itemLayoutKeysForPage(before.KV, name)
		if !pageFound && len(ids) == 0 && len(layoutKeys) == 0 {
			return UserState{}, errUserStateUnchanged
		}
		next := cloneUserState(before)
		next.Items = keptItems
		for _, key := range layoutKeys {
			delete(next.KV, key)
		}
		if err := setStatePages(&next, keptPages); err != nil {
			return UserState{}, err
		}
		prependRevision(&next, newRevision(before.Items, pages, by, "delete page "+name, catalog), h.opts.maxRevisions)
		return next, nil
	})
	if err != nil {
		return nil, err
	}
	if !pageFound && len(ids) == 0 && len(layoutKeys) == 0 {
		return nil, nil
	}
	// 所有广播都在原子提交成功后发生，客户端不会观察到半删除状态。
	events := make([]Event, 0, 2+len(layoutKeys))
	if pageFound {
		events = append(events, Event{Type: "pages"})
	}
	if len(ids) > 0 {
		events = append(events, Event{Type: "items", Op: "delete", IDs: ids, By: by})
	}
	for _, key := range layoutKeys {
		events = append(events, Event{Type: "kv", Key: key})
	}
	h.publishEvents(user, events...)
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
		// Validate the caller-owned structure before cloning it. This keeps an
		// oversized third-party backup/Store payload from doubling memory merely
		// to discover that it violates the catalog limits.
		preflight := templateCatalog{
			RootTemplateID: rev.RootTemplateID, NextRevision: rev.TemplateRevisionClock,
			Templates: rev.Templates,
		}
		for i := range preflight.Templates {
			if preflight.NextRevision < preflight.Templates[i].Revision {
				preflight.NextRevision = preflight.Templates[i].Revision
			}
		}
		if err := validateTemplateCatalog(preflight, rev.Pages); err != nil {
			return fmt.Errorf("revision %q: %w", rev.ID, err)
		}
		catalog := cloneTemplateCatalog(preflight)
		normalizeTemplateCatalog(&catalog)
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
	preflightCatalog := templateCatalog{
		RootTemplateID: export.RootTemplateID, NextRevision: export.TemplateRevisionClock,
		Templates: export.Templates,
	}
	for i := range preflightCatalog.Templates {
		if preflightCatalog.NextRevision < preflightCatalog.Templates[i].Revision {
			preflightCatalog.NextRevision = preflightCatalog.Templates[i].Revision
		}
	}
	if err := validateTemplateCatalog(preflightCatalog, export.Pages); err != nil {
		return UserState{}, err
	}
	catalog := cloneTemplateCatalog(preflightCatalog)
	normalizeTemplateCatalog(&catalog)
	if len(export.Revisions) > MaxRevisionsPerUser {
		return UserState{}, fmt.Errorf("too many revisions: %d > %d", len(export.Revisions), MaxRevisionsPerUser)
	}
	if h.opts.maxRevisions > 0 && len(export.Revisions) > h.opts.maxRevisions {
		return UserState{}, fmt.Errorf("too many revisions: %d > %d", len(export.Revisions), h.opts.maxRevisions)
	}
	if len(export.KV) > MaxKVEntriesPerUser-2 {
		return UserState{}, fmt.Errorf("too many exported kv entries: %d > %d", len(export.KV), MaxKVEntriesPerUser-2)
	}
	if err := validateStoredKV(export.KV, h.opts.limits.MaxKVKeysPerUser); err != nil {
		return UserState{}, err
	}
	seenRevisions := make(map[string]struct{}, len(export.Revisions))
	revisionBytes := 0
	for i := range export.Revisions {
		candidate := export.Revisions[i]
		if err := validateRevisionForImport(&candidate); err != nil {
			return UserState{}, err
		}
		if _, exists := seenRevisions[candidate.ID]; exists {
			return UserState{}, fmt.Errorf("duplicate revision id %q", candidate.ID)
		}
		size := revisionSourceBytes(candidate)
		if size > MaxUserRevisionBytes-revisionBytes {
			return UserState{}, fmt.Errorf("revisions exceed %d bytes per user", MaxUserRevisionBytes)
		}
		revisionBytes += size
		seenRevisions[candidate.ID] = struct{}{}
	}
	state := UserState{
		Items:     cloneItems(export.Items),
		KV:        make(map[string]json.RawMessage, len(export.KV)+2),
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
		limit := storedKVValueLimit(key)
		if !strings.HasPrefix(key, reservedKVPrefix) {
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
	for i := range state.Revisions {
		if err := validateRevisionForImport(&state.Revisions[i]); err != nil {
			return UserState{}, err
		}
	}
	if err := validateStoredKV(state.KV, h.opts.limits.MaxKVKeysPerUser); err != nil {
		return UserState{}, err
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
	imported, err := h.importState(export)
	if err != nil {
		return err
	}
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()

	err = h.updateUserStateLocked(ctx, user, func(before UserState) (UserState, error) {
		next := cloneUserState(imported)
		beforePages, err := statePages(before)
		if err != nil {
			return UserState{}, err
		}
		beforeCatalog, err := stateTemplateCatalog(before, beforePages)
		if err != nil {
			return UserState{}, err
		}
		nextPages, err := statePages(next)
		if err != nil {
			return UserState{}, err
		}
		nextCatalog, err := stateTemplateCatalog(next, nextPages)
		if err != nil {
			return UserState{}, err
		}
		if err := rebaseRestoredTemplateCatalog(&nextCatalog, beforeCatalog); err != nil {
			return UserState{}, err
		}
		if err := setStateTemplateCatalog(&next, nextCatalog, nextPages); err != nil {
			return UserState{}, err
		}
		return next, nil
	})
	if err != nil {
		return err
	}
	h.publishEvents(user,
		Event{Type: "items", Op: "import", By: "import"},
		Event{Type: "pages"},
		Event{Type: "templates", Op: "import", By: "import"},
	)
	h.metric("writes", 1)
	h.audit(ctx, user, "user.import", "import", "", map[string]any{"items": len(export.Items), "pages": len(export.Pages)})
	return nil
}

// PurgeUser 清空该用户的全部业务载荷。VersionedUserStateStore 会保留一个
// 递增版本墓碑以阻止旧 CAS 复活数据；SQL 实现因此仍保留版本表中的用户键。
// 需要物理抹除身份标识时，宿主必须先停止该用户的全部 writer，再按自己的
// 保留策略清理墓碑；不能把本方法单独当作完整的 GDPR 擦除流程。
func (h *Handler) PurgeUser(ctx context.Context, user string) error {
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()
	if err := h.store.EraseUser(ctx, user); err != nil {
		return err
	}
	h.metric("writes", 1)
	h.publishEvents(user,
		Event{Type: "items", Op: "purge", By: "api"},
		Event{Type: "pages"},
		Event{Type: "templates", Op: "purge", By: "api"},
	)
	h.audit(ctx, user, "user.purge", "api", "", nil)
	return nil
}
