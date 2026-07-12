// Package ihtml 提供"AI 动态前端 UI"功能库: 一个内嵌的空壳页面(微内核)
// 按用户加载服务端记录的一组 UI Item(HTML/JS/CSS 片段)并在浏览器里执行,
// 拼装出该用户的完整界面。Item 由 AI 助手通过对话生成并持久化, 页面通过
// SSE 实时应用变更, 无需刷新。
//
// 库对外的三个扩展点:
//   - Store: 持久化接口, 内置 Memory/File 两个实现, 生产可换数据库;
//   - UserResolver: 从宿主已验证的会话或令牌解析当前用户;
//   - ChatBackend: AI 对话后端, 子包 chat 提供基于 eino 的实现。
package ihtml

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ItemType 是 UI Item 的种类。
type ItemType string

const (
	// ItemHTML 是 HTML 片段, 内核按 order 顺序挂载到页面 #app 容器,
	// 片段内嵌的 <script> 会被重新创建以保证真正执行。
	ItemHTML ItemType = "html"
	// ItemJS 是 JS 脚本, 内核以 <script> 注入执行, 可自由 fetch。
	ItemJS ItemType = "js"
	// ItemCSS 是样式片段, 内核以 <style> 注入 head。
	ItemCSS ItemType = "css"
)

// Item 是一段由 AI 生成(或手工写入)的前端代码片段。
// 同一用户名下 ID 唯一, 以相同 ID 写入即覆盖(内核会原地替换并触发 teardown)。
type Item struct {
	ID    string   `json:"id"`
	Type  ItemType `json:"type"`
	Title string   `json:"title,omitempty"` // 人类/AI 可读的用途说明
	Order int      `json:"order"`           // 应用顺序, 小的先执行
	// Page 是所属页面名。空 = 全局 Item(菜单/顶栏/全局样式, 始终加载);
	// 非空 = 仅在内核路由切到该页时加载执行, 离开时 teardown。
	Page string `json:"page,omitempty"`
	// Content 是片段源码。html 类型可内嵌 <script>/<style>。
	Content string `json:"content"`
	// Meta 是附加元数据。约定:
	//   - js 类型 meta["module"]=="true" 时以 <script type="module"> 执行;
	//   - html 类型 meta["slot"] 是 Item 对当前 Template Slot.id 的规范绑定;
	//     sidebar/header/footer/main 是旧内核兼容名称。绑定缺失或 Slot 已删除时,
	//     模板运行时必须把 Item 放到 Root 末尾的未分配区，而不是丢弃内容。
	Meta      map[string]string `json:"meta,omitempty"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// Page 是页面注册表条目: 应用的一个"页", 也是侧边菜单的数据源。
// 页面下的 Item 通过 Item.Page 归属。
type Page struct {
	Name       string `json:"name"`                  // 路由名, 如 bills; 出现在 #/bills
	Title      string `json:"title"`                 // 菜单显示名
	Icon       string `json:"icon,omitempty"`        // 菜单图标(emoji 或短文本)
	Order      int    `json:"order,omitempty"`       // 菜单排序
	TemplateID string `json:"template_id,omitempty"` // 稳定引用的布局模板
}

var pageNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidatePageName 校验页面名(小写字母数字开头, 允许 -_)。
func ValidatePageName(name string) error {
	if !pageNamePattern.MatchString(name) {
		return fmt.Errorf("invalid page name %q (需要小写字母/数字/-_, 以字母数字开头)", name)
	}
	return nil
}

// Validate 校验 Page。
func (p *Page) Validate() error {
	if err := ValidatePageName(p.Name); err != nil {
		return err
	}
	if strings.TrimSpace(p.Title) == "" {
		return fmt.Errorf("page %q: title is required", p.Name)
	}
	if len(p.Title) > 64 || len(p.Icon) > 16 {
		return fmt.Errorf("page %q: title/icon too long", p.Name)
	}
	if p.TemplateID != "" {
		if err := validateTemplateID(p.TemplateID); err != nil {
			return fmt.Errorf("page %q: %w", p.Name, err)
		}
	}
	return nil
}

// Validate 校验 Item 的基本合法性。
func (it *Item) Validate() error {
	if strings.TrimSpace(it.ID) == "" {
		return fmt.Errorf("item id is required")
	}
	if len(it.ID) > 128 {
		return fmt.Errorf("item %q: id is too long", it.ID)
	}
	if strings.ContainsAny(it.ID, "\"'<>`&\\/\r\n\t ") {
		return fmt.Errorf("item %q: id contains invalid characters", it.ID)
	}
	switch it.Type {
	case ItemHTML, ItemJS, ItemCSS:
	default:
		return fmt.Errorf("item %q: unsupported type %q", it.ID, it.Type)
	}
	if it.Page != "" {
		if err := ValidatePageName(it.Page); err != nil {
			return fmt.Errorf("item %q: %w", it.ID, err)
		}
	}
	if len(it.Content) > MaxItemContentBytes {
		return fmt.Errorf("item %q: content exceeds %d bytes", it.ID, MaxItemContentBytes)
	}
	return nil
}

const (
	// MaxItemContentBytes 是单个 Item 内容的上限。
	MaxItemContentBytes = 512 << 10
	// MaxItemsPerUser 是单个用户 Item 数量上限。
	MaxItemsPerUser = 512
	// DefaultMaxRevisions 是默认保留的历史快照条数。
	DefaultMaxRevisions = 50
	// MaxPageErrors 是每个用户保留的页面错误条数。
	MaxPageErrors = 50
	// CurrentDataVersion 是 Revision 与 UserExport 当前使用的数据契约版本。
	// v1/零值快照只包含 Items；v2 增加页面注册表；v3 增加模板目录。
	CurrentDataVersion = 3
)

// Revision 是某用户 UI 状态的一次历史快照, 每次变更前自动保存, 用于回滚。
// Version 为 0/1 的旧快照只有 Items；Version >= 2 同时恢复 Pages；
// Version >= 3 同时恢复模板目录与 root 模板绑定。
type Revision struct {
	Version               int        `json:"version,omitempty"`
	ID                    string     `json:"id"`
	Note                  string     `json:"note,omitempty"` // 变更说明, 如 "put 2 items"
	By                    string     `json:"by,omitempty"`   // 变更来源: api / chat
	CreatedAt             time.Time  `json:"created_at"`
	Items                 []Item     `json:"items"`
	Pages                 []Page     `json:"pages,omitempty"`
	Templates             []Template `json:"templates,omitempty"`
	RootTemplateID        string     `json:"root_template_id,omitempty"`
	TemplateRevisionClock uint64     `json:"template_revision_clock,omitempty"`
}

// RevisionInfo 是快照的元信息(不含 Item 内容), 用于列表展示。
type RevisionInfo struct {
	ID            string    `json:"id"`
	Note          string    `json:"note,omitempty"`
	By            string    `json:"by,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	ItemCount     int       `json:"item_count"`
	TemplateCount int       `json:"template_count,omitempty"`
}

// Info 返回快照的元信息。
func (r *Revision) Info() RevisionInfo {
	return RevisionInfo{ID: r.ID, Note: r.Note, By: r.By, CreatedAt: r.CreatedAt, ItemCount: len(r.Items), TemplateCount: len(r.Templates)}
}

// PageError 是内核上报的一条前端运行错误, 保留下来供 AI 修复参考。
type PageError struct {
	ItemID  string    `json:"item_id,omitempty"` // 归属的 Item, 无法归属时为空
	Message string    `json:"message"`
	Stack   string    `json:"stack,omitempty"`
	URL     string    `json:"url,omitempty"`
	Time    time.Time `json:"time"`
}
