package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode"

	einoJSONSchema "github.com/eino-contrib/jsonschema"

	"github.com/zdypro888/ihtml"
)

const maxAITemplateRevision = uint64(1<<53 - 1)

// aiTemplateDraftPayload deliberately exposes only the editable identity/name
// and Draft. Revision/Published/timestamps are server-owned and are never
// accepted inside the payload; CAS is carried by expected_revision instead.
type aiTemplateDraftPayload struct {
	ID    string                `json:"id"`
	Name  string                `json:"name"`
	Draft ihtml.TemplateVersion `json:"draft"`
}

func strictObjSchema(required []string, props ...prop) *einoJSONSchema.Schema {
	schema := objSchema(required, props...)
	schema.AdditionalProperties = einoJSONSchema.FalseSchema
	return schema
}

func boolSchema(desc string) *einoJSONSchema.Schema {
	return &einoJSONSchema.Schema{Type: "boolean", Description: desc}
}

func numberSchema(desc string) *einoJSONSchema.Schema {
	return &einoJSONSchema.Schema{Type: "number", Description: desc}
}

func revisionSchema() *einoJSONSchema.Schema {
	return &einoJSONSchema.Schema{
		Type: "integer", Description: "必填 CAS 版本，必须原样使用 ui_template_get 返回的 template.revision；首次空模板为 0。",
		Minimum: json.Number("0"), Maximum: json.Number("9007199254740991"),
	}
}

func templateNodeSchema() *einoJSONSchema.Schema {
	return strictObjSchema([]string{"id", "kind"},
		prop{"id", strSchema("结构节点 ID，不是 Item ID")},
		prop{"parent_id", strSchema("父结构节点 ID；Root 留空")},
		prop{"kind", &einoJSONSchema.Schema{Type: "string", Enum: []any{"canvas", "grid", "stack", "group"}, Description: "结构节点类型"}},
		prop{"name", strSchema("编辑器中的结构名称，不是业务正文")},
		prop{"order", intSchema("同级结构顺序")},
		prop{"props", &einoJSONSchema.Schema{
			Type: "object", Description: "仅允许布局/尺寸/断点等结构提示；服务端拒绝正文、HTML、CSS、JS、URL 与可执行载荷。",
			AdditionalProperties: einoJSONSchema.TrueSchema,
		}},
	)
}

func templateSlotSchema() *einoJSONSchema.Schema {
	itemType := &einoJSONSchema.Schema{Type: "string", Enum: []any{"html", "js", "css"}}
	return strictObjSchema([]string{"id", "node_id"},
		prop{"id", strSchema("Slot ID；HTML Item 用 meta.slot 等于该 ID 绑定")},
		prop{"node_id", strSchema("拥有该 Slot 的结构节点 ID")},
		prop{"name", strSchema("编辑器中的 Slot 名称")},
		prop{"accepts", arrSchema("允许绑定的 Item 类型，仅是类型约束，不包含 Item 内容", itemType)},
		prop{"locked", boolSchema("是否锁定")},
	)
}

func templatePlacementSchema() *einoJSONSchema.Schema {
	return strictObjSchema([]string{"item_id", "mode"},
		prop{"item_id", strSchema("可选页面实例覆盖引用的 Item ID；不要在可复用模板中无故写入")},
		prop{"slot_id", strSchema("目标 Slot ID")},
		prop{"unassigned", boolSchema("true 表示进入 Root 末尾的未分配 fallback，不会隐藏或删除 Item")},
		prop{"breakpoint", strSchema("断点 ID；留空为默认")},
		prop{"mode", &einoJSONSchema.Schema{Type: "string", Enum: []any{"flow", "absolute"}, Description: "Item 实例排版模式"}},
		prop{"order", intSchema("顺序")},
		prop{"column", intSchema("网格列")},
		prop{"column_span", intSchema("跨列数")},
		prop{"row", intSchema("网格行")},
		prop{"row_span", intSchema("跨行数")},
		prop{"x", numberSchema("绝对排版 X")},
		prop{"y", numberSchema("绝对排版 Y")},
		prop{"width", numberSchema("绝对排版宽度")},
		prop{"height", numberSchema("绝对排版高度")},
		prop{"z_index", intSchema("层级")},
		prop{"locked", boolSchema("是否锁定")},
		prop{"hidden", boolSchema("是否在该断点隐藏")},
	)
}

func templateBreakpointSchema() *einoJSONSchema.Schema {
	return strictObjSchema([]string{"id"},
		prop{"id", strSchema("断点 ID")},
		prop{"name", strSchema("断点名称")},
		prop{"min_width", intSchema("最小容器宽度；0 为开放")},
		prop{"max_width", intSchema("最大容器宽度；0 为开放")},
		prop{"columns", intSchema("flow 网格列数")},
		prop{"gap", numberSchema("flow 间距")},
	)
}

func templateVersionSchema() *einoJSONSchema.Schema {
	return strictObjSchema([]string{"version", "root_id", "nodes", "slots", "placements", "breakpoints"},
		prop{"version", intSchema("从 ui_template_get 原样带回；服务端保存时自行递增")},
		prop{"root_id", strSchema("Root 节点 ID；完全空模板必须为空字符串")},
		prop{"nodes", arrSchema("只含 Root/Container 结构节点，不含业务正文", templateNodeSchema())},
		prop{"slots", arrSchema("命名挂载点", templateSlotSchema())},
		prop{"placements", arrSchema("可选 Item 实例引用；通常留空，优先使用 Item.meta.slot", templatePlacementSchema())},
		prop{"breakpoints", arrSchema("响应式断点", templateBreakpointSchema())},
	)
}

func templateDraftPayloadSchema() *einoJSONSchema.Schema {
	return strictObjSchema([]string{"id", "name", "draft"},
		prop{"id", strSchema("当前模板稳定 ID，必须使用 ui_template_get 返回值")},
		prop{"name", strSchema("模板名称")},
		prop{"draft", templateVersionSchema()},
	)
}

func requireOnlyArgs(args map[string]any, allowed ...string) error {
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key := range args {
		if _, ok := set[key]; !ok {
			return fmt.Errorf("未知参数 %q", key)
		}
	}
	return nil
}

func optionalTemplatePage(args map[string]any) (string, error) {
	raw, exists := args["page"]
	if !exists || raw == nil {
		return "", nil
	}
	page, ok := raw.(string)
	if !ok {
		return "", errors.New("page 必须是字符串")
	}
	return strings.TrimSpace(page), nil
}

func requiredTemplateString(args map[string]any, key string) (string, error) {
	raw, exists := args[key]
	if !exists {
		return "", fmt.Errorf("缺少 %s", key)
	}
	value, ok := raw.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s 必须是非空字符串", key)
	}
	return strings.TrimSpace(value), nil
}

func requiredTemplateRevision(args map[string]any) (uint64, error) {
	raw, exists := args["expected_revision"]
	if !exists {
		return 0, errors.New("缺少 expected_revision；先调用 ui_template_get 获取当前 CAS 版本")
	}
	var value float64
	switch v := raw.(type) {
	case float64:
		value = v
	case float32:
		value = float64(v)
	case int:
		value = float64(v)
	case int64:
		value = float64(v)
	case uint64:
		if v > maxAITemplateRevision {
			return 0, errors.New("expected_revision 超出 JSON 安全整数范围")
		}
		return v, nil
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, errors.New("expected_revision 必须是整数")
		}
		value = parsed
	default:
		return 0, errors.New("expected_revision 必须是整数，不能是字符串")
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > float64(maxAITemplateRevision) || math.Trunc(value) != value {
		return 0, errors.New("expected_revision 必须是 JSON 安全范围内的非负整数")
	}
	return uint64(value), nil
}

func decodeAITemplateDraft(args map[string]any) (ihtml.Template, error) {
	raw, exists := args["template"]
	if !exists {
		return ihtml.Template{}, errors.New("缺少 template")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return ihtml.Template{}, fmt.Errorf("template 无法编码: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var payload aiTemplateDraftPayload
	if err := decoder.Decode(&payload); err != nil {
		return ihtml.Template{}, fmt.Errorf("template 只能包含 id/name/draft 与已声明的结构字段: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return ihtml.Template{}, err
	}
	template := ihtml.Template{ID: payload.ID, Name: payload.Name, Draft: payload.Draft}
	if err := validateAITemplateStructuralProps(template.Draft); err != nil {
		return ihtml.Template{}, err
	}
	if err := template.Validate(); err != nil {
		return ihtml.Template{}, fmt.Errorf("模板结构无效: %w", err)
	}
	return template, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("template 后存在多余 JSON 值")
		}
		return fmt.Errorf("template JSON 结尾无效: %w", err)
	}
	return nil
}

var forbiddenTemplatePropKeys = map[string]struct{}{
	"body": {}, "businessdata": {}, "code": {}, "content": {}, "css": {}, "dataurl": {},
	"href": {}, "html": {}, "innerhtml": {}, "item": {}, "itemcontent": {}, "itemid": {}, "items": {},
	"javascript": {}, "js": {}, "markdown": {}, "outerhtml": {}, "script": {}, "src": {}, "srcdoc": {},
	"style": {}, "styles": {}, "stylesheet": {}, "text": {}, "url": {},
}

func normalizedTemplatePropKey(key string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, key)
}

func validateAITemplateStructuralProps(version ihtml.TemplateVersion) error {
	for _, node := range version.Nodes {
		for key, raw := range node.Props {
			if _, blocked := forbiddenTemplatePropKeys[normalizedTemplatePropKey(key)]; blocked {
				return fmt.Errorf("node %q props.%s 是业务内容/可执行字段；Template 只允许结构，正文必须写入 Item", node.ID, key)
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			var value any
			if err := decoder.Decode(&value); err != nil {
				return fmt.Errorf("node %q props.%s 不是有效 JSON: %w", node.ID, key, err)
			}
			count := 0
			if err := walkAITemplateProp(node.ID+".props."+key, value, 0, &count); err != nil {
				return err
			}
		}
	}
	return nil
}

func walkAITemplateProp(path string, value any, depth int, count *int) error {
	if depth > 32 {
		return fmt.Errorf("%s 嵌套过深", path)
	}
	*count++
	if *count > 4096 {
		return fmt.Errorf("%s 结构过大", path)
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if _, blocked := forbiddenTemplatePropKeys[normalizedTemplatePropKey(key)]; blocked {
				return fmt.Errorf("%s.%s 是业务内容/可执行字段；正文必须写入 Item", path, key)
			}
			if err := walkAITemplateProp(path+"."+key, child, depth+1, count); err != nil {
				return err
			}
		}
	case []any:
		for index, child := range typed {
			if err := walkAITemplateProp(fmt.Sprintf("%s[%d]", path, index), child, depth+1, count); err != nil {
				return err
			}
		}
	case string:
		lower := strings.ToLower(typed)
		if len(typed) > 512 || strings.ContainsAny(typed, "<>\r\n") ||
			strings.Contains(lower, "javascript:") || strings.Contains(lower, "data:text/html") ||
			strings.Contains(lower, "http://") || strings.Contains(lower, "https://") ||
			strings.Contains(lower, "@import") || strings.Contains(lower, "<script") ||
			strings.Contains(lower, "function(") || strings.Contains(lower, "function (") ||
			strings.Contains(lower, "=>") || strings.Contains(lower, "document.") ||
			strings.Contains(lower, "window.") || strings.Contains(lower, "fetch(") {
			return fmt.Errorf("%s 含业务正文、URL 或可执行内容；Template 只能保存结构", path)
		}
	}
	return nil
}

func validateTemplateGetArgs(args map[string]any) error {
	if err := requireOnlyArgs(args, "page"); err != nil {
		return err
	}
	_, err := optionalTemplatePage(args)
	return err
}

func validateTemplatePutArgs(args map[string]any) error {
	if err := requireOnlyArgs(args, "page", "expected_revision", "template"); err != nil {
		return err
	}
	if _, err := optionalTemplatePage(args); err != nil {
		return err
	}
	if _, err := requiredTemplateRevision(args); err != nil {
		return err
	}
	_, err := decodeAITemplateDraft(args)
	return err
}

func validateTemplatePublishArgs(args map[string]any) error {
	if err := requireOnlyArgs(args, "id", "expected_revision"); err != nil {
		return err
	}
	if _, err := requiredTemplateString(args, "id"); err != nil {
		return err
	}
	_, err := requiredTemplateRevision(args)
	return err
}

func validateTemplateSlotDeleteArgs(args map[string]any) error {
	if err := requireOnlyArgs(args, "id", "slot_id", "expected_revision"); err != nil {
		return err
	}
	if _, err := requiredTemplateString(args, "id"); err != nil {
		return err
	}
	if _, err := requiredTemplateString(args, "slot_id"); err != nil {
		return err
	}
	_, err := requiredTemplateRevision(args)
	return err
}

func templateToolDefs() []toolDef {
	return []toolDef{
		{
			Name:  "ui_template_get",
			Title: "读取当前模板结构",
			Desc:  "按 page 读取当前绑定的结构模板及 CAS revision。返回 Draft/Published 结构但绝不返回 Item content；运行时只使用 Published，没有 Published 时保持旧兼容渲染。page 留空读取 Root 模板。",
			Params: strictObjSchema(nil,
				prop{"page", strSchema("页面路由名；留空读取 Root 当前模板")},
			),
			Risk:     "low",
			Validate: validateTemplateGetArgs,
			Execute: func(ctx context.Context, rt *runtime, args map[string]any) (any, string, error) {
				page, err := optionalTemplatePage(args)
				if err != nil {
					return nil, "", err
				}
				current, err := rt.svc.GetCurrentTemplate(ctx, rt.user, page)
				if err != nil {
					return nil, "", err
				}
				runtimeMode := "legacy_compat"
				publishedVersion := uint64(0)
				if current.Template.Published != nil {
					runtimeMode = "published_template"
					publishedVersion = current.Template.Published.Version
				}
				result := map[string]any{
					"page": page, "persisted": current.Persisted, "migrated": current.Migrated,
					"template": current.Template,
					"runtime": map[string]any{
						"mode": runtimeMode, "published_version": publishedVersion,
						"published_only": true, "unassigned_fallback": "root_end",
					},
					"item_content_included": false,
				}
				if current.Template.Published == nil {
					return result, fmt.Sprintf("已读取 %s 模板 revision %d；尚未发布，运行时保持旧兼容渲染", templatePageLabel(page), current.Template.Revision), nil
				}
				return result, fmt.Sprintf("已读取 %s 模板 revision %d；运行时使用 Published v%d", templatePageLabel(page), current.Template.Revision, publishedVersion), nil
			},
		},
		{
			Name:  "ui_template_put_current",
			Title: "保存当前模板 Draft",
			Desc:  "以 expected_revision CAS 保存并绑定 page 当前模板的完整 Draft。只允许 Root/Container/Slot、断点和结构几何；严禁 HTML/JS/CSS/正文/URL。保存 Draft 不改变运行时，必须另行 ui_template_publish。",
			Params: strictObjSchema([]string{"expected_revision", "template"},
				prop{"page", strSchema("页面路由名；留空保存 Root 当前模板")},
				prop{"expected_revision", revisionSchema()},
				prop{"template", templateDraftPayloadSchema()},
			),
			Risk: "medium", Confirm: true, Write: true, Validate: validateTemplatePutArgs,
			Execute: func(ctx context.Context, rt *runtime, args map[string]any) (any, string, error) {
				page, _ := optionalTemplatePage(args)
				revision, _ := requiredTemplateRevision(args)
				template, _ := decodeAITemplateDraft(args)
				saved, err := rt.svc.PutCurrentTemplate(ctx, rt.user, page, template, revision, "chat")
				if err != nil {
					return nil, "", err
				}
				return map[string]any{
					"ok": true, "template": saved, "draft_saved": true,
					"runtime_changed": false, "publish_required": true,
				}, fmt.Sprintf("已保存 %s 模板 Draft，revision %d；尚未发布，运行时未改变", templatePageLabel(page), saved.Revision), nil
			},
		},
		{
			Name:  "ui_template_publish",
			Title: "发布模板",
			Desc:  "以 expected_revision CAS 把指定模板当前 Draft 复制为 Published。只有 Published 会驱动运行时；没有 Published 时继续旧兼容渲染。",
			Params: strictObjSchema([]string{"id", "expected_revision"},
				prop{"id", strSchema("模板 ID，来自 ui_template_get")},
				prop{"expected_revision", revisionSchema()},
			),
			Risk: "high", Confirm: true, Write: true, Validate: validateTemplatePublishArgs,
			Execute: func(ctx context.Context, rt *runtime, args map[string]any) (any, string, error) {
				id, _ := requiredTemplateString(args, "id")
				revision, _ := requiredTemplateRevision(args)
				published, err := rt.svc.PublishTemplate(ctx, rt.user, id, revision, "chat")
				if err != nil {
					return nil, "", err
				}
				version := uint64(0)
				if published.Published != nil {
					version = published.Published.Version
				}
				return map[string]any{"ok": true, "template": published, "runtime_version": version},
					fmt.Sprintf("已发布模板 %s（Published v%d，revision %d）", id, version, published.Revision), nil
			},
		},
		{
			Name:  "ui_template_slot_delete",
			Title: "删除模板 Slot",
			Desc:  "以 expected_revision CAS 删除 Draft 中的 Slot。绝不删除 Item；相关 Placement 转入 unassigned，运行时/编辑器应在 Root 末尾显示 fallback。需再次发布才影响运行时。",
			Params: strictObjSchema([]string{"id", "slot_id", "expected_revision"},
				prop{"id", strSchema("模板 ID")},
				prop{"slot_id", strSchema("要删除的 Slot ID")},
				prop{"expected_revision", revisionSchema()},
			),
			Risk: "high", Confirm: true, Write: true, Validate: validateTemplateSlotDeleteArgs,
			Execute: func(ctx context.Context, rt *runtime, args map[string]any) (any, string, error) {
				id, _ := requiredTemplateString(args, "id")
				slotID, _ := requiredTemplateString(args, "slot_id")
				revision, _ := requiredTemplateRevision(args)
				updated, err := rt.svc.DeleteTemplateSlot(ctx, rt.user, id, slotID, revision, "chat")
				if err != nil {
					return nil, "", err
				}
				unassigned := 0
				for _, placement := range updated.Draft.Placements {
					if placement.Unassigned && placement.SlotID == "" {
						unassigned++
					}
				}
				return map[string]any{
					"ok": true, "template": updated, "items_deleted": 0,
					"unassigned_placements": unassigned, "publish_required": true,
				}, fmt.Sprintf("已从 Draft 删除 Slot %s，Item 未删除；%d 个 Placement 位于 Root fallback，需发布后生效", slotID, unassigned), nil
			},
		},
	}
}

func templatePageLabel(page string) string {
	if page == "" {
		return "Root"
	}
	return "页面 " + page
}
