package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/zdypro888/ihtml"
)

// inspectTimeout 是等待内核回传页面状态的上限。页面不在线(纯 API 调用、
// 内核旧缓存、标签页已关)时工具以明确文案超时返回, 不阻塞整轮。
const inspectTimeout = 5 * time.Second

// inspectBroker 把 ui_page_inspect 工具的等待与 WS 读循环送达的
// inspect_result 帧配对。一个会话一个实例, 跨轮共享。
type inspectBroker struct {
	mu      sync.Mutex
	pending map[string]chan *ihtml.ChatClientInspection
}

func newInspectBroker() *inspectBroker {
	return &inspectBroker{pending: make(map[string]chan *ihtml.ChatClientInspection)}
}

func (b *inspectBroker) register() (string, chan *ihtml.ChatClientInspection) {
	var raw [12]byte
	_, _ = rand.Read(raw[:])
	id := "ins_" + hex.EncodeToString(raw[:])
	ch := make(chan *ihtml.ChatClientInspection, 1)
	b.mu.Lock()
	b.pending[id] = ch
	b.mu.Unlock()
	return id, ch
}

func (b *inspectBroker) drop(id string) {
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
}

// deliver 把内核应答路由给等待中的工具调用; 未知/迟到的 ID 直接丢弃。
func (b *inspectBroker) deliver(id string, payload *ihtml.ChatClientInspection) {
	b.mu.Lock()
	ch, ok := b.pending[id]
	if ok {
		delete(b.pending, id)
	}
	b.mu.Unlock()
	if ok && payload != nil {
		ch <- payload
	}
}

const (
	inspectDefaultLimit   = 20
	inspectMaxLimit       = 20
	maxInspectionTotal    = 512
	maxInspectionItems    = inspectMaxLimit
	maxInspectionText     = 200
	maxInspectionError    = 300
	maxInspectionErrors   = 8
	maxInspectionFullText = 1200
	// semanticTool 的通用返回上限是 20KB。inspect 在这里保证完整
	// JSON 低于更严格的预算, 给外层 status/summary 留出空间。
	maxInspectionResultBytes = 14_000
)

type inspectQuery struct {
	offset int
	limit  int
	ids    []string
}

type inspectionPage struct {
	items     []ihtml.ChatClientInspectionItem
	total     int
	offset    int
	truncated bool
}

func normalizeInspectQuery(args map[string]any) inspectQuery {
	q := inspectQuery{offset: argInt(args, "offset", 0), limit: argInt(args, "limit", inspectDefaultLimit)}
	q.offset = min(maxInspectionTotal, max(0, q.offset))
	q.limit = min(inspectMaxLimit, max(1, q.limit))
	seen := make(map[string]struct{})
	for _, id := range argStrings(args, "ids") {
		id = clipRunes(strings.TrimSpace(id), 128)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		q.ids = append(q.ids, id)
		if len(q.ids) == inspectMaxLimit {
			break
		}
	}
	return q
}

// inspectionPageFor 同时兼容新旧页面内核。旧内核忽略请求中的
// offset/limit/ids，且一次回传全量 Items；此时由服务端先精确过滤、
// 再本地分页。Limit>0 是新内核必定回传的能力标记。
func inspectionPageFor(in *ihtml.ChatClientInspection, q inspectQuery) inspectionPage {
	if in.Limit <= 0 {
		var source []ihtml.ChatClientInspectionItem
		if len(q.ids) > 0 {
			byID := make(map[string]ihtml.ChatClientInspectionItem, len(in.Items))
			for _, item := range in.Items {
				if _, exists := byID[item.ID]; !exists {
					byID[item.ID] = item
				}
			}
			source = make([]ihtml.ChatClientInspectionItem, 0, len(q.ids))
			for _, id := range q.ids {
				item, ok := byID[id]
				if !ok {
					item = ihtml.ChatClientInspectionItem{ID: id}
				}
				source = append(source, item)
			}
		} else {
			source = append([]ihtml.ChatClientInspectionItem(nil), in.Items[:min(len(in.Items), maxInspectionTotal)]...)
		}
		total := len(source)
		offset := min(q.offset, total)
		end := min(total, offset+q.limit)
		return inspectionPage{
			items: append([]ihtml.ChatClientInspectionItem(nil), source[offset:end]...),
			total: total, offset: offset, truncated: end < total,
		}
	}

	if len(q.ids) > 0 {
		// ids 是服务端已规范化的权威顺序，不信任客户端可能伪造的
		// ID/total。缺失项以 present=false 显式回传，也保证游标继续前进。
		total := len(q.ids)
		offset := min(q.offset, total)
		end := min(total, offset+q.limit)
		byID := make(map[string]ihtml.ChatClientInspectionItem, len(in.Items))
		for _, item := range in.Items {
			if _, exists := byID[item.ID]; !exists {
				byID[item.ID] = item
			}
		}
		items := make([]ihtml.ChatClientInspectionItem, 0, end-offset)
		for _, id := range q.ids[offset:end] {
			item, ok := byID[id]
			if !ok {
				item = ihtml.ChatClientInspectionItem{ID: id}
			}
			items = append(items, item)
		}
		return inspectionPage{items: items, total: total, offset: offset, truncated: end < total}
	}

	// inspect_id 已把应答与本次请求配对；分页游标仍以服务端发出的
	// q.offset 为权威，不能让异常/旧客户端用陈旧 Offset 把模型带回
	// 第一页。回传 Items 视为本次请求页，并裁到 512 的硬边界。
	requestedOffset := min(maxInspectionTotal, max(0, q.offset))
	remaining := maxInspectionTotal - requestedOffset
	itemCount := min(len(in.Items), q.limit, remaining)
	items := append([]ihtml.ChatClientInspectionItem(nil), in.Items[:itemCount]...)
	total := min(maxInspectionTotal, max(max(0, in.Total), requestedOffset+len(items)))
	offset := min(requestedOffset, total)
	atHardLimit := offset+len(items) >= maxInspectionTotal
	return inspectionPage{
		items: items, total: total, offset: offset,
		truncated: !atHardLimit && (offset+len(items) < total || len(in.Items) > len(items) || in.Truncated),
	}
}

// sanitizeInspection 把内核回传的载荷(不可信数据)裁剪为固定上限的视图,
// 防止恶意/异常页面撑爆模型上下文。
func sanitizeInspection(in *ihtml.ChatClientInspection, q inspectQuery) map[string]any {
	page := inspectionPageFor(in, q)
	items := make([]map[string]any, 0, len(page.items))
	missing := 0
	for i, it := range page.items {
		if i >= maxInspectionItems {
			break
		}
		view := map[string]any{
			"id":      clipRunes(it.ID, 128),
			"present": it.Present,
		}
		if !it.Present {
			missing++
		}
		if it.Type != "" {
			view["type"] = clipRunes(it.Type, 16)
		}
		if it.Page != "" {
			view["page"] = clipRunes(it.Page, 64)
		}
		if it.Slot != "" {
			view["slot"] = clipRunes(it.Slot, 64)
		}
		if it.Present {
			view["visible"] = it.Visible
			if it.Width > 0 || it.Height > 0 {
				view["size"] = fmt.Sprintf("%dx%d", it.Width, it.Height)
			}
		}
		if text := strings.TrimSpace(it.Text); text != "" {
			view["text"] = clipRunes(text, maxInspectionText)
		}
		if errText := strings.TrimSpace(it.Error); errText != "" {
			view["error"] = clipRunes(errText, maxInspectionError)
		}
		items = append(items, view)
	}
	errs := make([]string, 0, min(len(in.Errors), maxInspectionErrors))
	for i, e := range in.Errors {
		if i >= maxInspectionErrors {
			break
		}
		if e = strings.TrimSpace(e); e != "" {
			errs = append(errs, clipRunes(e, maxInspectionError))
		}
	}
	total := page.total
	actualOffset := page.offset
	out := map[string]any{
		"online": true,
		"page":   clipRunes(in.Page, 64),
		"total":  total,
		"offset": actualOffset,
		"limit":  q.limit,
		"items":  items,
		"viewport": map[string]any{
			"width": in.Viewport.Width, "height": in.Viewport.Height,
			"mode": clipRunes(in.Viewport.Mode, 16),
		},
	}
	if in.ThemeMode != "" {
		out["theme_mode"] = clipRunes(in.ThemeMode, 16)
	}
	if missing > 0 {
		out["items_not_rendered"] = missing
	}
	if len(errs) > 0 {
		out["recent_errors"] = errs
	}
	if text := strings.TrimSpace(in.VisibleText); text != "" {
		out["visible_text"] = clipRunes(text, maxInspectionFullText)
	}
	next := actualOffset + len(items)
	// 客户端的 next_offset 只作为“还有下一页”的提示；服务端始终根据
	// 实际返回条目计算游标，避免错误/陈旧游标造成空翻页或重复页。
	// 异常客户端若声称还有数据却返回空页，不能把同一个 offset 再交给
	// 模型形成死循环；有至少一个实际条目时才发布后续游标。
	if len(items) > 0 && (next < total || page.truncated) {
		out["truncated"] = true
		out["next_offset"] = next
	}
	out["notice"] = untrustedDataNotice
	fitInspectionBudget(out, actualOffset)
	return out
}

func inspectionSize(out map[string]any) int {
	raw, _ := json.Marshal(out)
	return len(raw)
}

func updateInspectionDerived(out map[string]any) {
	items, _ := out["items"].([]map[string]any)
	missing := 0
	for _, item := range items {
		if present, _ := item["present"].(bool); !present {
			missing++
		}
	}
	if missing > 0 {
		out["items_not_rendered"] = missing
	} else {
		delete(out, "items_not_rendered")
	}
}

// fitInspectionBudget 只移除完整字段或完整 Item, 永远不切开 JSON。
// 如果页尾 Item 被预算裁掉, next_offset 指回第一个未返回项。
func fitInspectionBudget(out map[string]any, offset int) {
	if inspectionSize(out) <= maxInspectionResultBytes {
		return
	}
	delete(out, "visible_text")
	if inspectionSize(out) <= maxInspectionResultBytes {
		return
	}
	delete(out, "recent_errors")
	items, _ := out["items"].([]map[string]any)
	for len(items) > 1 && inspectionSize(out) > maxInspectionResultBytes {
		items = items[:len(items)-1]
		out["items"] = items
		out["truncated"] = true
		out["next_offset"] = offset + len(items)
	}
	updateInspectionDerived(out)
	if inspectionSize(out) <= maxInspectionResultBytes || len(items) == 0 {
		return
	}
	delete(items[0], "text")
	delete(items[0], "error")
}

// executeInspect 是 ui_page_inspect 的执行体: 经本会话 WS 发 inspect_request,
// 阻塞等待内核 inspect_result(或超时)。
func executeInspect(ctx context.Context, rt *runtime, args map[string]any) (any, string, error) {
	if rt.inspect == nil {
		return nil, "", fmt.Errorf("页面检查不可用: 会话未接通页面内核")
	}
	q := normalizeInspectQuery(args)
	id, ch := rt.inspect.register()
	defer rt.inspect.drop(id)
	rt.timeline.write(ihtml.ChatStreamEvent{
		Type: ihtml.ChatStreamInspectRequest, InspectID: id, InspectOffset: q.offset,
		InspectLimit: q.limit, InspectIDs: append([]string(nil), q.ids...), ModelReady: true,
	})

	timer := time.NewTimer(inspectTimeout)
	defer timer.Stop()
	select {
	case payload := <-ch:
		view := sanitizeInspection(payload, q)
		rendered := 0
		visible := 0
		items, _ := view["items"].([]map[string]any)
		for _, item := range items {
			if present, _ := item["present"].(bool); present {
				rendered++
			}
			if shown, _ := item["visible"].(bool); shown {
				visible++
			}
		}
		actualOffset, _ := view["offset"].(int)
		summary := fmt.Sprintf("读取实时页面: 第 %d-%d 项/共 %d 项, %d 个已渲染, %d 个可见", actualOffset+1, actualOffset+len(items), view["total"], rendered, visible)
		if len(items) == 0 {
			summary = fmt.Sprintf("读取实时页面: 共 %d 项, offset=%d 的本页没有 Item", view["total"], actualOffset)
		}
		if missing := len(items) - rendered; missing > 0 {
			summary += fmt.Sprintf(", %d 个未渲染", missing)
		}
		if n := len(payload.Errors); n > 0 {
			summary += fmt.Sprintf(", %d 条近期报错", n)
		}
		return view, summary, nil
	case <-ctx.Done():
		return nil, "", ctx.Err()
	case <-timer.C:
		return map[string]any{
			"online": false,
			"notice": "页面当前不在线或未应答(可能是纯 API 会话、页面已关闭)。可改用 ui_item_list 与 ui_page_errors 了解持久化状态。",
		}, "页面不在线, 未获取实时状态", nil
	}
}
