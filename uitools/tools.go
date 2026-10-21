// Package uitools exposes workspace tools over an already scoped ihtml service.
// Tools do not authenticate, authorize or confirm writes. The host must perform
// live authorization and verify approval immediately before Apply. CheckItems
// is required for card writes, including rollback, and must enforce host policy.
// Adapted in part from github.com/zdypro888/nbco gateway/httpapi/ihtml_tools.go
// (MIT License), via HJGAS. Host business templates and billing are not included.
package uitools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zdypro888/ihtml"
)

// Runtime contains request-bound capabilities. Never construct Service from client identity.
type Runtime struct {
	Service    ihtml.ScopedService
	CheckItems func([]ihtml.Item) error
	FormatTime func(time.Time) string
	ChangedBy  string
}

func (r *Runtime) checkItems(items []ihtml.Item) error {
	if r.CheckItems == nil {
		return ErrCodePolicyRequired
	}
	return r.CheckItems(items)
}
func (r *Runtime) changedBy() string {
	if r.ChangedBy == "" {
		return "AI 助手"
	}
	return r.ChangedBy
}
func (r *Runtime) formatTime(at time.Time) string {
	if r.FormatTime != nil {
		return r.FormatTime(at)
	}
	return at.Format(time.RFC3339)
}

func invalidService(s ihtml.ScopedService) bool {
	if s == nil {
		return true
	}
	v := reflect.ValueOf(s)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

var ErrCodePolicyRequired = errors.New("uitools: host code policy required")
var ErrInvalidRuntime = errors.New("uitools: invalid runtime or operation")

// Tool metadata can be converted to any agent framework. Prepare never mutates
// the workspace; Apply must only run after host-owned approval and live access.
type Tool struct {
	Name, Label, Description string
	Parameters               map[string]any
	Write                    bool
	read                     func(context.Context, *Runtime, json.RawMessage) (Result, error)
	prepare                  func(context.Context, *Runtime, json.RawMessage) (Draft, error)
	apply                    func(context.Context, *Runtime, json.RawMessage) (string, error)
}
type Result struct {
	ForModel, Summary string
	Failed            bool
}
type Draft struct {
	Title, Reason string
	Risk          string
	Args          json.RawMessage
}

func (t Tool) Read(ctx context.Context, r *Runtime, raw json.RawMessage) (Result, error) {
	if ctx == nil || r == nil || invalidService(r.Service) || t.read == nil {
		return Result{}, ErrInvalidRuntime
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return t.read(ctx, r, raw)
}
func (t Tool) Prepare(ctx context.Context, r *Runtime, raw json.RawMessage) (Draft, error) {
	if ctx == nil || r == nil || invalidService(r.Service) || t.prepare == nil {
		return Draft{}, ErrInvalidRuntime
	}
	if err := ctx.Err(); err != nil {
		return Draft{}, err
	}
	return t.prepare(ctx, r, raw)
}
func (t Tool) Apply(ctx context.Context, r *Runtime, raw json.RawMessage) (string, error) {
	if ctx == nil || r == nil || invalidService(r.Service) || t.apply == nil {
		return "", ErrInvalidRuntime
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return t.apply(ctx, r, raw)
}

// InputError is safe display text; unexpected service errors are not rewritten.
type InputError struct{ Text string }

func (e *InputError) Error() string              { return e.Text }
func plainError(text string) error               { return &InputError{Text: text} }
func decodeArgs(raw json.RawMessage, v any) bool { return json.Unmarshal(raw, v) == nil }
func objectSchema(properties map[string]any, required ...string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	out := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}
func property(kind, description string) map[string]any {
	return map[string]any{"type": kind, "description": description}
}
func marshalToolResult(value any) string {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if enc.Encode(value) != nil {
		return "结果无法整理。"
	}
	return strings.TrimSuffix(out.String(), "\n")
}
func canonicalJSON(raw []byte) (json.RawMessage, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
func clipRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

type itemInput struct {
	ID      string            `json:"id"`
	Type    ihtml.ItemType    `json:"type"`
	Title   string            `json:"title,omitempty"`
	Page    string            `json:"page,omitempty"`
	Order   int               `json:"order"`
	Content string            `json:"content"`
	Meta    map[string]string `json:"meta,omitempty"`
}

func (in itemInput) item() ihtml.Item {
	return ihtml.Item{ID: strings.TrimSpace(in.ID), Type: in.Type, Title: strings.TrimSpace(in.Title), Page: strings.TrimSpace(in.Page),
		Order: in.Order, Content: in.Content, Meta: in.Meta}
}

func toItems(in []itemInput) []ihtml.Item {
	out := make([]ihtml.Item, 0, len(in))
	for _, it := range in {
		out = append(out, it.item())
	}
	return out
}

type pageInput struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Icon  string `json:"icon,omitempty"`
	Order int    `json:"order,omitempty"`
}

// itemSchema describes one card for the model.
//
// Adapted from github.com/zdypro888/nbco gateway/httpapi/ihtml_tools.go (MIT).
func itemSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":      property("string", "稳定的英文小写名称（字母数字和连字符），修改同一张卡片时沿用原名称"),
			"type":    map[string]any{"type": "string", "enum": []string{"html", "css", "js"}},
			"title":   property("string", "卡片的中文名称，用户会看到"),
			"page":    property("string", "所属页面名；留空表示放在默认页"),
			"order":   property("integer", "排列顺序，小的在前"),
			"content": property("string", "HTML、CSS 或 JavaScript 源码"),
			"meta":    map[string]any{"type": "object", "description": "可选，如 slot", "additionalProperties": map[string]any{"type": "string"}},
		},
		"required": []string{"id", "type", "title", "content"},
	}
}

func pageSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":  property("string", "页面英文小写名称（字母数字和 -_）"),
			"title": property("string", "页面中文标题，菜单上显示"),
			"icon":  property("string", "可选的短图标文字"),
			"order": property("integer", "菜单顺序"),
		},
		"required": []string{"name", "title"},
	}
}

func reasonSchema() map[string]any {
	return property("string", "一句中文大白话，告诉用户为什么这样改（不写英文、编号）")
}

func textResult(forModel, summary string) Result {
	return Result{ForModel: forModel, Summary: summary}
}

func badArgs(message string) Result {
	return Result{ForModel: message, Summary: "参数不对", Failed: true}
}

func ReadTools() []Tool {
	tools := []Tool{
		// Adapted from github.com/zdypro888/nbco gateway/httpapi/ihtml_tools.go (MIT): ui_list_state.
		{
			Name: "ui_list_state", Label: "看看我的界面",
			Description: "分页列出“我的界面”的页面、卡片（不含源码）和最近的运行错误。改界面前先调用。",
			Parameters:  objectSchema(map[string]any{"offset": property("integer", "卡片分页起点，默认 0"), "limit": property("integer", "每页卡片数，默认 20，最多 50")}),
			read: func(ctx context.Context, t *Runtime, raw json.RawMessage) (Result, error) {
				var args struct {
					Offset int `json:"offset"`
					Limit  int `json:"limit"`
				}
				if !decodeArgs(raw, &args) || args.Offset < 0 {
					return badArgs("参数格式不对。"), nil
				}
				svc := t.Service
				items, err := svc.ListItems(ctx)
				if err != nil {
					return Result{}, err
				}
				pages, err := svc.ListPages(ctx)
				if err != nil {
					return Result{}, err
				}
				errs, err := svc.PageErrors(ctx)
				if err != nil {
					return Result{}, err
				}
				if args.Limit <= 0 {
					args.Limit = 20
				}
				args.Limit = min(args.Limit, 50)
				args.Offset = min(args.Offset, len(items))
				end := min(args.Offset+args.Limit, len(items))
				type itemView struct {
					ID         string            `json:"id"`
					Type       ihtml.ItemType    `json:"type"`
					Title      string            `json:"title,omitempty"`
					Page       string            `json:"page,omitempty"`
					Order      int               `json:"order"`
					Meta       map[string]string `json:"meta,omitempty"`
					ContentLen int               `json:"content_bytes"`
				}
				views := make([]itemView, 0, end-args.Offset)
				for _, item := range items[args.Offset:end] {
					views = append(views, itemView{ID: item.ID, Type: item.Type, Title: item.Title, Page: item.Page, Order: item.Order,
						Meta: item.Meta, ContentLen: len(item.Content)})
				}
				if len(errs) > 20 {
					errs = errs[:20]
				}
				return textResult(marshalToolResult(map[string]any{
					"item_count": len(items), "offset": args.Offset, "next_offset": end, "truncated": end < len(items),
					"pages": pages, "items": views, "recent_errors": errs,
				}), fmt.Sprintf("看了我的界面：%d 页、%d 张卡片", len(pages), len(items))), nil
			},
		},
		// Adapted from github.com/zdypro888/nbco gateway/httpapi/ihtml_tools.go (MIT): ui_get_item.
		{
			Name: "ui_get_item", Label: "看卡片内容",
			Description: "按卡片名称分段读取卡片源码，局部修改前核对；按 next_offset 继续读，直到 truncated=false。",
			Parameters: objectSchema(map[string]any{
				"id": property("string", "卡片名称（id）"), "offset": property("integer", "从第几个字符开始，默认 0"),
				"max_chars": property("integer", "最多返回字符数，默认 8000，最多 10000"),
			}, "id"),
			read: func(ctx context.Context, t *Runtime, raw json.RawMessage) (Result, error) {
				var args struct {
					ID       string `json:"id"`
					Offset   int    `json:"offset"`
					MaxChars int    `json:"max_chars"`
				}
				if !decodeArgs(raw, &args) || strings.TrimSpace(args.ID) == "" || args.Offset < 0 {
					return badArgs("id 必填，offset 不能小于 0。"), nil
				}
				if args.MaxChars <= 0 {
					args.MaxChars = 8000
				}
				args.MaxChars = min(args.MaxChars, 10000)
				items, err := t.Service.ListItems(ctx)
				if err != nil {
					return Result{}, err
				}
				for _, item := range items {
					if item.ID != args.ID {
						continue
					}
					runes := []rune(item.Content)
					args.Offset = min(args.Offset, len(runes))
					end := min(args.Offset+args.MaxChars, len(runes))
					return textResult(marshalToolResult(map[string]any{
						"id": item.ID, "type": item.Type, "title": item.Title, "page": item.Page, "order": item.Order, "meta": item.Meta,
						"content": string(runes[args.Offset:end]), "content_chars": len(runes), "offset": args.Offset,
						"next_offset": end, "truncated": end < len(runes),
					}), "看了卡片“"+cardTitle(item)+"”"), nil
				}
				return textResult("没有这张卡片。", "没有找到这张卡片"), nil
			},
		},
		{
			Name: "ui_list_revisions", Label: "看看以前的样子",
			Description: "列出“我的界面”自动保存的以前的样子（时间、说明、卡片数），用于恢复。回答用户时只说时间，不说编号。",
			Parameters:  objectSchema(nil),
			read: func(ctx context.Context, t *Runtime, _ json.RawMessage) (Result, error) {
				revisions, err := t.Service.ListRevisions(ctx)
				if err != nil {
					return Result{}, err
				}
				type view struct {
					ID    string `json:"revision_id"`
					When  string `json:"when"`
					Note  string `json:"note,omitempty"`
					By    string `json:"by,omitempty"`
					Items int    `json:"item_count"`
				}
				out := make([]view, 0, len(revisions))
				for _, r := range revisions[:min(len(revisions), 50)] {
					out = append(out, view{ID: r.ID, When: t.formatTime(r.CreatedAt), Note: r.Note, By: r.By, Items: r.ItemCount})
				}
				return textResult(marshalToolResult(out), fmt.Sprintf("看了以前的样子：共 %d 份", len(revisions))), nil
			},
		},
		// Adapted from github.com/zdypro888/nbco gateway/httpapi/ihtml_tools.go (MIT): ui_inspect_page.
		{
			Name: "ui_inspect_page", Label: "核对一页",
			Description: "核对一个页面保存后的实际情况：是否存在、卡片数量和名称、最近运行错误。不代表用户屏幕上的实时画面。",
			Parameters:  objectSchema(map[string]any{"name": property("string", "页面名称")}, "name"),
			read: func(ctx context.Context, t *Runtime, raw json.RawMessage) (Result, error) {
				var args struct {
					Name string `json:"name"`
				}
				if !decodeArgs(raw, &args) || !validPageName(args.Name) {
					return badArgs("name 必须是有效的页面名称。"), nil
				}
				inspection, err := inspectPage(ctx, t, t.Service, strings.TrimSpace(args.Name))
				if err != nil {
					return Result{}, err
				}
				title := args.Name
				if inspection.Page != nil {
					title = inspection.Page.Title
				}
				return textResult(marshalToolResult(inspection), fmt.Sprintf("核对了“%s”这一页：%d 张卡片", title, inspection.ItemCount)), nil
			},
		},
		// Adapted from github.com/zdypro888/nbco gateway/httpapi/ihtml_tools.go (MIT): ui_get_data.
		{
			Name: "ui_get_data", Label: "看卡片用的数据",
			Description: "读取“我的界面”保存的一份结构化数据（卡片可用 ihtml.kv.get(key) 读取）。",
			Parameters:  objectSchema(map[string]any{"key": property("string", "数据名称，字母数字及 ._-")}, "key"),
			read: func(ctx context.Context, t *Runtime, raw json.RawMessage) (Result, error) {
				var args struct {
					Key string `json:"key"`
				}
				if !decodeArgs(raw, &args) || strings.TrimSpace(args.Key) == "" {
					return badArgs("key 必填。"), nil
				}
				key := strings.TrimSpace(args.Key)
				value, err := t.Service.KVGet(ctx, key)
				if errors.Is(err, ihtml.ErrNotFound) || errors.Is(err, ihtml.ErrReservedKVKey) {
					return textResult(marshalToolResult(map[string]any{"exists": false, "key": key}), "这份数据还没有"), nil
				}
				if err != nil {
					return Result{}, err
				}
				return textResult(marshalToolResult(map[string]any{"exists": true, "key": key, "value": value, "bytes": len(value)}), "看了卡片用的数据"), nil
			},
		},
	}
	return tools
}
func validPageName(name string) bool { return ihtml.ValidatePageName(strings.TrimSpace(name)) == nil }

func cardTitle(item ihtml.Item) string {
	if t := strings.TrimSpace(item.Title); t != "" {
		return clipRunes(t, 40)
	}
	return "未命名卡片"
}

type pageInspection struct {
	Registered    bool              `json:"registered"`
	Page          *ihtml.Page       `json:"page,omitempty"`
	ItemCount     int               `json:"item_count"`
	ItemIDs       []string          `json:"item_ids"`
	ContentBytes  int               `json:"content_bytes"`
	LastUpdatedAt string            `json:"last_updated_at,omitempty"`
	RecentErrors  []ihtml.PageError `json:"recent_errors"`
}

// inspectPage reads the persisted state of one page.
//
// Adapted from github.com/zdypro888/nbco gateway/httpapi/ihtml_tools.go (MIT).
func inspectPage(ctx context.Context, runtime *Runtime, svc ihtml.ScopedService, name string) (pageInspection, error) {
	result := pageInspection{ItemIDs: []string{}, RecentErrors: []ihtml.PageError{}}
	pages, err := svc.ListPages(ctx)
	if err != nil {
		return result, err
	}
	for i := range pages {
		if pages[i].Name == name {
			page := pages[i]
			result.Registered, result.Page = true, &page
			break
		}
	}
	items, err := svc.ListItems(ctx)
	if err != nil {
		return result, err
	}
	updated := map[string]time.Time{}
	var latest time.Time
	for _, item := range items {
		if item.Page != name {
			continue
		}
		result.ItemCount++
		result.ItemIDs = append(result.ItemIDs, item.ID)
		result.ContentBytes += len(item.Content)
		updated[item.ID] = item.UpdatedAt
		if item.UpdatedAt.After(latest) {
			latest = item.UpdatedAt
		}
	}
	if !latest.IsZero() {
		result.LastUpdatedAt = runtime.formatTime(latest)
	}
	slices.Sort(result.ItemIDs)
	errs, err := svc.PageErrors(ctx)
	if err != nil {
		return result, err
	}
	for _, pageErr := range errs {
		at, ok := updated[pageErr.ItemID]
		if !ok || pageErr.Time.Before(at) {
			continue
		}
		result.RecentErrors = append(result.RecentErrors, pageErr)
		if len(result.RecentErrors) == 20 {
			break
		}
	}
	return result, nil
}

var kvKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._\-]{1,64}$`)

func reservedKey(key string) bool {
	return strings.HasPrefix(key, ihtml.ReservedKVPrefix) || strings.HasPrefix(key, "layout.") || strings.HasPrefix(key, "item-layout.")
}

func reasonOr(reason, fallback string) string {
	if r := clipRunes(reason, 200); r != "" {
		return r
	}
	return fallback
}

func marshalArgs(v any) (json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return canonicalJSON(raw)
}

func titlesOf(items []ihtml.Item) string {
	if len(items) == 0 {
		return ""
	}
	first := "“" + cardTitle(items[0]) + "”"
	if len(items) == 1 {
		return first
	}
	return fmt.Sprintf("%s等 %d 张卡片", first, len(items))
}

func WriteTools() []Tool {
	tools := []Tool{
		// Adapted from github.com/zdypro888/nbco gateway/httpapi/ihtml_tools.go (MIT): ui_apply_items.
		{
			Name: "ui_apply_items", Label: "放卡片到我的界面", Write: true,
			Description: "新增或按名称覆盖卡片（html/css/js）。不会马上生效：会先给用户一张确认卡，用户点“放上去”才写入。" +
				"整页新建或整体更新请用 ui_publish_page。",
			Parameters: objectSchema(map[string]any{
				"items":  map[string]any{"type": "array", "description": "要新增或覆盖的卡片", "items": itemSchema()},
				"reason": reasonSchema(),
			}, "items"),
			prepare: func(ctx context.Context, s *Runtime, raw json.RawMessage) (Draft, error) {
				var args struct {
					Items  []itemInput `json:"items"`
					Reason string      `json:"reason"`
				}
				if !decodeArgs(raw, &args) || len(args.Items) == 0 {
					return Draft{}, plainError("没有要放的卡片。")
				}
				items := toItems(args.Items)
				if err := s.checkItems(items); err != nil {
					return Draft{}, err
				}
				existing, err := s.Service.ListItems(ctx)
				if err != nil {
					return Draft{}, err
				}
				replacing := slices.ContainsFunc(items, func(it ihtml.Item) bool {
					return slices.ContainsFunc(existing, func(old ihtml.Item) bool { return old.ID == it.ID })
				})
				title := "把" + titlesOf(items) + "放到我的界面"
				if replacing {
					title = "修改我的界面里的" + titlesOf(items)
				}
				normalized, err := marshalArgs(map[string]any{"items": args.Items, "reason": clipRunes(args.Reason, 200)})
				return Draft{Title: title, Reason: reasonOr(args.Reason, "点“放上去”后会出现在你的界面上；改错了可以恢复成以前的样子。"),
					Risk: ihtml.ChatRiskMedium, Args: normalized}, err
			},
			apply: func(ctx context.Context, s *Runtime, raw json.RawMessage) (string, error) {
				var args struct {
					Items  []itemInput `json:"items"`
					Reason string      `json:"reason"`
				}
				if !decodeArgs(raw, &args) || len(args.Items) == 0 {
					return "", plainError("确认卡里没有卡片。")
				}
				items := toItems(args.Items)
				if err := s.checkItems(items); err != nil {
					return "", err
				}
				if err := s.Service.PutItems(ctx, items, s.changedBy(), clipRunes(args.Reason, 200)); err != nil {
					return "", err
				}
				return "已放到我的界面：" + titlesOf(items), nil
			},
		},
		// Adapted from github.com/zdypro888/nbco gateway/httpapi/ihtml_tools.go (MIT): ui_publish_page.
		{
			Name: "ui_publish_page", Label: "放一整页到我的界面", Write: true,
			Description: "一次放上完整的一页：同时登记页面并整体替换这一页的全部卡片，其他页面不受影响。不会马上生效：用户点“放上去”才写入。",
			Parameters: objectSchema(map[string]any{
				"page":   pageSchema(),
				"items":  map[string]any{"type": "array", "description": "这一页的全部卡片；每张的 page 等于 page.name", "items": itemSchema()},
				"reason": reasonSchema(),
			}, "page", "items"),
			prepare: func(ctx context.Context, s *Runtime, raw json.RawMessage) (Draft, error) {
				page, items, reason, err := decodePagePublish(s, raw)
				if err != nil {
					return Draft{}, err
				}
				pages, err := s.Service.ListPages(ctx)
				if err != nil {
					return Draft{}, err
				}
				title := "把“" + page.Title + "”这一页放到我的界面"
				if slices.ContainsFunc(pages, func(p ihtml.Page) bool { return p.Name == page.Name }) {
					title = "整页更新“" + page.Title + "”（这一页原来的卡片会被替换）"
				}
				args := map[string]any{"page": pageInput{Name: page.Name, Title: page.Title, Icon: page.Icon, Order: page.Order}, "items": items,
					"reason": clipRunes(reason, 200)}
				normalized, err := marshalArgs(args)
				return Draft{Title: title, Reason: reasonOr(reason, fmt.Sprintf("这一页共 %d 张卡片。原来的样子会自动留一份，可以恢复。", len(items))),
					Risk: ihtml.ChatRiskMedium, Args: normalized}, err
			},
			apply: func(ctx context.Context, s *Runtime, raw json.RawMessage) (string, error) {
				page, items, reason, err := decodePagePublish(s, raw)
				if err != nil {
					return "", err
				}
				publisher, ok := s.Service.(ihtml.ScopedPagePublicationService)
				if !ok {
					return "", plainError("现在不支持整页放上去。")
				}
				if err := publisher.PublishPage(ctx, page, toItems(items), s.changedBy(), clipRunes(reason, 200)); err != nil {
					return "", err
				}
				return "已把“" + page.Title + "”这一页放到我的界面", nil
			},
		},
		// Adapted from github.com/zdypro888/nbco gateway/httpapi/ihtml_tools.go (MIT): ui_delete_items.
		{
			Name: "ui_delete_items", Label: "从我的界面删卡片", Write: true,
			Description: "按名称删除卡片；删除前自动留一份以前的样子，可以恢复。用户点确认后才删除。",
			Parameters: objectSchema(map[string]any{
				"ids":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "卡片名称（id）列表"},
				"reason": reasonSchema(),
			}, "ids"),
			prepare: func(ctx context.Context, s *Runtime, raw json.RawMessage) (Draft, error) {
				var args struct {
					IDs    []string `json:"ids"`
					Reason string   `json:"reason"`
				}
				if !decodeArgs(raw, &args) || len(args.IDs) == 0 {
					return Draft{}, plainError("没有说要删哪张卡片。")
				}
				existing, err := s.Service.ListItems(ctx)
				if err != nil {
					return Draft{}, err
				}
				var found []ihtml.Item
				for _, id := range args.IDs {
					i := slices.IndexFunc(existing, func(it ihtml.Item) bool { return it.ID == id })
					if i < 0 {
						return Draft{}, plainError("我的界面里没有要删的这张卡片。")
					}
					found = append(found, existing[i])
				}
				normalized, err := marshalArgs(map[string]any{"ids": args.IDs, "reason": clipRunes(args.Reason, 200)})
				return Draft{Title: "从我的界面删掉" + titlesOf(found), Reason: reasonOr(args.Reason, "删之前会自动留一份以前的样子，可以恢复。"),
					Risk: ihtml.ChatRiskMedium, Args: normalized}, err
			},
			apply: func(ctx context.Context, s *Runtime, raw json.RawMessage) (string, error) {
				var args struct {
					IDs    []string `json:"ids"`
					Reason string   `json:"reason"`
				}
				if !decodeArgs(raw, &args) || len(args.IDs) == 0 {
					return "", plainError("确认卡里没有要删的卡片。")
				}
				deleted, err := s.Service.DeleteItems(ctx, args.IDs, s.changedBy(), clipRunes(args.Reason, 200))
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("已删掉 %d 张卡片", len(deleted)), nil
			},
		},
		// Adapted from github.com/zdypro888/nbco gateway/httpapi/ihtml_tools.go (MIT): ui_delete_page.
		{
			Name: "ui_delete_page", Label: "删掉一页", Write: true,
			Description: "删除一个页面和上面的全部卡片；删除前自动留一份以前的样子。用户点确认后才删除。",
			Parameters:  objectSchema(map[string]any{"name": property("string", "页面名称"), "reason": reasonSchema()}, "name"),
			prepare: func(ctx context.Context, s *Runtime, raw json.RawMessage) (Draft, error) {
				var args struct {
					Name   string `json:"name"`
					Reason string `json:"reason"`
				}
				if !decodeArgs(raw, &args) || strings.TrimSpace(args.Name) == "" {
					return Draft{}, plainError("没有说要删哪一页。")
				}
				pages, err := s.Service.ListPages(ctx)
				if err != nil {
					return Draft{}, err
				}
				i := slices.IndexFunc(pages, func(p ihtml.Page) bool { return p.Name == strings.TrimSpace(args.Name) })
				if i < 0 {
					return Draft{}, plainError("我的界面里没有这一页。")
				}
				normalized, err := marshalArgs(map[string]any{"name": pages[i].Name, "reason": clipRunes(args.Reason, 200)})
				return Draft{Title: "删掉“" + pages[i].Title + "”这一页和上面的卡片", Reason: reasonOr(args.Reason, "删之前会自动留一份以前的样子，可以恢复。"),
					Risk: ihtml.ChatRiskHigh, Args: normalized}, err
			},
			apply: func(ctx context.Context, s *Runtime, raw json.RawMessage) (string, error) {
				var args struct {
					Name string `json:"name"`
				}
				if !decodeArgs(raw, &args) || args.Name == "" {
					return "", plainError("确认卡里没有页面。")
				}
				deleted, err := s.Service.DeletePage(ctx, args.Name, s.changedBy())
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("已删掉这一页和上面的 %d 张卡片", len(deleted)), nil
			},
		},
		// Adapted from github.com/zdypro888/nbco gateway/httpapi/ihtml_tools.go (MIT): ui_rollback.
		{
			Name: "ui_rollback", Label: "恢复以前的样子", Write: true,
			Description: "把“我的界面”整体恢复成以前的某个样子（revision_id 取自 ui_list_revisions）；恢复前会先留一份现在的样子。用户点确认后才恢复。",
			Parameters:  objectSchema(map[string]any{"revision_id": property("string", "ui_list_revisions 返回的 revision_id"), "reason": reasonSchema()}, "revision_id"),
			prepare: func(ctx context.Context, s *Runtime, raw json.RawMessage) (Draft, error) {
				var args struct {
					RevisionID string `json:"revision_id"`
					Reason     string `json:"reason"`
				}
				if !decodeArgs(raw, &args) || strings.TrimSpace(args.RevisionID) == "" {
					return Draft{}, plainError("没有说要恢复成哪一次的样子。")
				}
				revisions, err := s.Service.ListRevisions(ctx)
				if err != nil {
					return Draft{}, err
				}
				i := slices.IndexFunc(revisions, func(r ihtml.RevisionInfo) bool { return r.ID == strings.TrimSpace(args.RevisionID) })
				if i < 0 {
					return Draft{}, plainError("找不到这一次以前的样子，请重新看看以前的样子。")
				}
				items, err := s.Service.RevisionItems(ctx, revisions[i].ID)
				if err != nil {
					return Draft{}, err
				}
				if err = s.checkItems(items); err != nil {
					return Draft{}, err
				}
				normalized, err := marshalArgs(map[string]any{"revision_id": revisions[i].ID, "reason": clipRunes(args.Reason, 200)})
				when := s.formatTime(revisions[i].CreatedAt)
				return Draft{Title: "把我的界面恢复成 " + when + " 的样子", Reason: reasonOr(args.Reason, "恢复前会先留一份现在的样子，想改回来也可以。"),
					Risk: ihtml.ChatRiskMedium, Args: normalized}, err
			},
			apply: func(ctx context.Context, s *Runtime, raw json.RawMessage) (string, error) {
				var args struct {
					RevisionID string `json:"revision_id"`
				}
				if !decodeArgs(raw, &args) || args.RevisionID == "" {
					return "", plainError("确认卡里没有要恢复的样子。")
				}
				items, err := s.Service.RevisionItems(ctx, args.RevisionID)
				if err != nil {
					return "", err
				}
				if err = s.checkItems(items); err != nil {
					return "", err
				}
				if err := s.Service.Rollback(ctx, args.RevisionID, s.changedBy()); err != nil {
					return "", err
				}
				return "已恢复成以前的样子", nil
			},
		},
		// Adapted from github.com/zdypro888/nbco gateway/httpapi/ihtml_tools.go (MIT): ui_put_data.
		{
			Name: "ui_put_data", Label: "保存卡片用的数据", Write: true,
			Description: "保存一份卡片要用的结构化数据（卡片用 ihtml.kv.get(key) 读取）。动态业务数据应从宿主允许的数据入口读取，遵循宿主的数据保存政策。用户确认后才保存。",
			Parameters: objectSchema(map[string]any{
				"key": property("string", "数据名称，字母数字及 ._-"), "value_json": property("string", "完整有效的 JSON 文本"), "reason": reasonSchema(),
			}, "key", "value_json"),
			prepare: func(_ context.Context, _ *Runtime, raw json.RawMessage) (Draft, error) {
				key, value, reason, err := decodePutData(raw)
				if err != nil {
					return Draft{}, err
				}
				normalized, err := marshalArgs(map[string]any{"key": key, "value_json": string(value), "reason": clipRunes(reason, 200)})
				return Draft{Title: "保存一份卡片要用的数据", Reason: reasonOr(reason, "卡片会用到这份数据。"), Risk: ihtml.ChatRiskLow, Args: normalized}, err
			},
			apply: func(ctx context.Context, s *Runtime, raw json.RawMessage) (string, error) {
				key, value, _, err := decodePutData(raw)
				if err != nil {
					return "", err
				}
				if err := s.Service.KVSet(ctx, key, value); err != nil {
					return "", err
				}
				return "已保存卡片要用的数据", nil
			},
		},
		// Adapted from github.com/zdypro888/nbco gateway/httpapi/ihtml_tools.go (MIT): ui_delete_data.
		{
			Name: "ui_delete_data", Label: "删掉卡片用的数据", Write: true,
			Description: "删除一份卡片用的数据；先确认没有卡片还在用它。用户确认后才删除。",
			Parameters:  objectSchema(map[string]any{"key": property("string", "数据名称"), "reason": reasonSchema()}, "key"),
			prepare: func(_ context.Context, _ *Runtime, raw json.RawMessage) (Draft, error) {
				var args struct {
					Key    string `json:"key"`
					Reason string `json:"reason"`
				}
				if !decodeArgs(raw, &args) || !kvKeyPattern.MatchString(strings.TrimSpace(args.Key)) || reservedKey(strings.TrimSpace(args.Key)) {
					return Draft{}, plainError("数据名称不对。")
				}
				normalized, err := marshalArgs(map[string]any{"key": strings.TrimSpace(args.Key), "reason": clipRunes(args.Reason, 200)})
				return Draft{Title: "删掉一份卡片用的数据", Reason: reasonOr(args.Reason, "删掉后用到它的卡片会显示为空。"), Risk: ihtml.ChatRiskMedium,
					Args: normalized}, err
			},
			apply: func(ctx context.Context, s *Runtime, raw json.RawMessage) (string, error) {
				var args struct {
					Key string `json:"key"`
				}
				if !decodeArgs(raw, &args) || !kvKeyPattern.MatchString(args.Key) || reservedKey(args.Key) {
					return "", plainError("确认卡里的数据名称不对。")
				}
				if err := s.Service.KVDelete(ctx, args.Key); err != nil {
					return "", err
				}
				return "已删掉这份数据", nil
			},
		},
	}
	return tools
}
func decodePagePublish(s *Runtime, raw json.RawMessage) (ihtml.Page, []itemInput, string, error) {
	var args struct {
		Page   pageInput   `json:"page"`
		Items  []itemInput `json:"items"`
		Reason string      `json:"reason"`
	}
	if !decodeArgs(raw, &args) || len(args.Items) == 0 {
		return ihtml.Page{}, nil, "", plainError("这一页没有卡片。")
	}
	page := ihtml.Page{Name: strings.TrimSpace(args.Page.Name), Title: strings.TrimSpace(args.Page.Title), Icon: args.Page.Icon, Order: args.Page.Order}
	if page.Validate() != nil {
		return ihtml.Page{}, nil, "", plainError("页面名称或标题不对：名称用英文小写字母数字，标题不超过 20 个字。")
	}
	for i := range args.Items {
		switch strings.TrimSpace(args.Items[i].Page) {
		case "":
			args.Items[i].Page = page.Name
		case page.Name:
		default:
			return ihtml.Page{}, nil, "", plainError("这一页里混进了别的页面的卡片。")
		}
	}
	if err := ValidatePublication(page, toItems(args.Items)); err != nil {
		return ihtml.Page{}, nil, "", err
	}
	if err := s.checkItems(toItems(args.Items)); err != nil {
		return ihtml.Page{}, nil, "", err
	}
	return page, args.Items, args.Reason, nil
}

func decodePutData(raw json.RawMessage) (string, json.RawMessage, string, error) {
	var args struct {
		Key       string `json:"key"`
		ValueJSON string `json:"value_json"`
		Reason    string `json:"reason"`
	}
	if !decodeArgs(raw, &args) {
		return "", nil, "", plainError("参数格式不对。")
	}
	key := strings.TrimSpace(args.Key)
	value := json.RawMessage(strings.TrimSpace(args.ValueJSON))
	if !kvKeyPattern.MatchString(key) || reservedKey(key) {
		return "", nil, "", plainError("数据名称不对：只能用字母、数字和 ._-。")
	}
	if len(value) == 0 || !json.Valid(value) || len(value) > ihtml.MaxKVValueBytes {
		return "", nil, "", plainError("数据格式不对或太大。")
	}
	return key, value, args.Reason, nil
}
