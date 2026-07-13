package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	maxInspectionItems    = 200
	maxInspectionText     = 200
	maxInspectionError    = 300
	maxInspectionErrors   = 8
	maxInspectionFullText = 2000
)

// sanitizeInspection 把内核回传的载荷(不可信数据)裁剪为固定上限的视图,
// 防止恶意/异常页面撑爆模型上下文。
func sanitizeInspection(in *ihtml.ChatClientInspection) map[string]any {
	items := make([]map[string]any, 0, min(len(in.Items), maxInspectionItems))
	missing := 0
	for i, it := range in.Items {
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
	out := map[string]any{
		"page":  clipRunes(in.Page, 64),
		"items": items,
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
	out["notice"] = untrustedDataNotice
	return out
}

// executeInspect 是 ui_page_inspect 的执行体: 经本会话 WS 发 inspect_request,
// 阻塞等待内核 inspect_result(或超时)。
func executeInspect(ctx context.Context, rt *runtime) (any, string, error) {
	if rt.inspect == nil {
		return nil, "", fmt.Errorf("页面检查不可用: 会话未接通页面内核")
	}
	id, ch := rt.inspect.register()
	defer rt.inspect.drop(id)
	rt.timeline.write(ihtml.ChatStreamEvent{Type: ihtml.ChatStreamInspectRequest, InspectID: id, ModelReady: true})

	timer := time.NewTimer(inspectTimeout)
	defer timer.Stop()
	select {
	case payload := <-ch:
		view := sanitizeInspection(payload)
		rendered := 0
		for _, item := range payload.Items {
			if item.Present {
				rendered++
			}
		}
		summary := fmt.Sprintf("读取实时页面: %d 个 Item 在渲染", rendered)
		if missing := len(payload.Items) - rendered; missing > 0 {
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
