package ihtml

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

const templatesKVKey = "__ihtml.templates"

type templateCatalog struct {
	RootTemplateID string     `json:"root_template_id,omitempty"`
	NextRevision   uint64     `json:"next_revision,omitempty"`
	Templates      []Template `json:"templates"`
}

func cloneTemplateCatalog(c templateCatalog) templateCatalog {
	return templateCatalog{RootTemplateID: c.RootTemplateID, NextRevision: c.NextRevision, Templates: cloneTemplates(c.Templates)}
}

func normalizeTemplateCatalog(c *templateCatalog) {
	var highest uint64
	for i := range c.Templates {
		normalizeTemplateCollections(&c.Templates[i])
		if c.Templates[i].Revision > highest {
			highest = c.Templates[i].Revision
		}
	}
	if c.NextRevision < highest {
		c.NextRevision = highest
	}
	if c.Templates == nil {
		c.Templates = []Template{}
	}
}

func allocateTemplateRevision(c *templateCatalog) (uint64, error) {
	if c.NextRevision >= maxTemplateRevision {
		return 0, fmt.Errorf("template revision counter is exhausted")
	}
	c.NextRevision++
	return c.NextRevision, nil
}

func rebaseRestoredTemplateCatalog(target *templateCatalog, current templateCatalog) error {
	normalizeTemplateCatalog(target)
	normalizeTemplateCatalog(&current)
	if target.NextRevision < current.NextRevision {
		target.NextRevision = current.NextRevision
	}
	for i := range target.Templates {
		revision, err := allocateTemplateRevision(target)
		if err != nil {
			return err
		}
		target.Templates[i].Revision = revision
	}
	return nil
}

func normalizeTemplateCollections(t *Template) {
	if t.Draft.Nodes == nil {
		t.Draft.Nodes = []TemplateNode{}
	}
	if t.Draft.Slots == nil {
		t.Draft.Slots = []Slot{}
	}
	if t.Draft.Placements == nil {
		t.Draft.Placements = []Placement{}
	}
	if t.Draft.Breakpoints == nil {
		t.Draft.Breakpoints = []Breakpoint{}
	}
	if t.Published != nil {
		if t.Published.Nodes == nil {
			t.Published.Nodes = []TemplateNode{}
		}
		if t.Published.Slots == nil {
			t.Published.Slots = []Slot{}
		}
		if t.Published.Placements == nil {
			t.Published.Placements = []Placement{}
		}
		if t.Published.Breakpoints == nil {
			t.Published.Breakpoints = []Breakpoint{}
		}
	}
}

func templateIndex(templates []Template, id string) int {
	for i := range templates {
		if templates[i].ID == id {
			return i
		}
	}
	return -1
}

func validateTemplateCatalog(c templateCatalog, pages []Page) error {
	if len(c.Templates) > maxTemplatesPerUser {
		return fmt.Errorf("too many templates: %d > %d", len(c.Templates), maxTemplatesPerUser)
	}
	ids := make(map[string]bool, len(c.Templates))
	var highestRevision uint64
	for i := range c.Templates {
		t := &c.Templates[i]
		if err := t.Validate(); err != nil {
			return err
		}
		if t.Revision == 0 || t.Draft.Version == 0 {
			return fmt.Errorf("persisted template %q must have non-zero revision and draft version", t.ID)
		}
		if t.Revision > maxTemplateRevision || t.Draft.Version > maxTemplateRevision ||
			t.Published != nil && t.Published.Version > maxTemplateRevision {
			return fmt.Errorf("template %q revision exceeds the JSON-safe integer range", t.ID)
		}
		if t.Revision < t.Draft.Version {
			return fmt.Errorf("template %q revision cannot be older than its draft", t.ID)
		}
		if t.Published != nil {
			if t.Published.Version == 0 || t.Published.Version > t.Draft.Version || t.PublishedAt == nil {
				return fmt.Errorf("template %q has invalid published version metadata", t.ID)
			}
		} else if t.PublishedAt != nil {
			return fmt.Errorf("template %q has published_at without a published version", t.ID)
		}
		if ids[t.ID] {
			return fmt.Errorf("duplicate template id %q", t.ID)
		}
		ids[t.ID] = true
		if t.Revision > highestRevision {
			highestRevision = t.Revision
		}
	}
	if c.NextRevision < highestRevision {
		return fmt.Errorf("template revision clock %d is behind template revision %d", c.NextRevision, highestRevision)
	}
	if c.NextRevision > maxTemplateRevision {
		return fmt.Errorf("template revision clock exceeds the JSON-safe integer range")
	}
	if c.RootTemplateID != "" && !ids[c.RootTemplateID] {
		return fmt.Errorf("root template %q does not exist", c.RootTemplateID)
	}
	for _, page := range pages {
		if page.TemplateID != "" && !ids[page.TemplateID] {
			return fmt.Errorf("page %q references missing template %q", page.Name, page.TemplateID)
		}
	}
	return nil
}

func decodeTemplateCatalog(raw json.RawMessage, pages []Page) (templateCatalog, error) {
	var catalog templateCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return templateCatalog{}, fmt.Errorf("corrupt template catalog: %w", err)
	}
	normalizeTemplateCatalog(&catalog)
	if err := validateTemplateCatalog(catalog, pages); err != nil {
		return templateCatalog{}, fmt.Errorf("invalid template catalog: %w", err)
	}
	return catalog, nil
}

func stateTemplateCatalog(state UserState, pages []Page) (templateCatalog, error) {
	raw, ok := state.KV[templatesKVKey]
	if !ok {
		return templateCatalog{Templates: []Template{}}, nil
	}
	return decodeTemplateCatalog(raw, pages)
}

func setStateTemplateCatalog(state *UserState, catalog templateCatalog, pages []Page) error {
	next := cloneTemplateCatalog(catalog)
	normalizeTemplateCatalog(&next)
	if err := validateTemplateCatalog(next, pages); err != nil {
		return err
	}
	sortTemplates(next.Templates)
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(raw) > maxInternalKVValueSize {
		return fmt.Errorf("template catalog exceeds %d bytes", maxInternalKVValueSize)
	}
	if state.KV == nil {
		state.KV = make(map[string]json.RawMessage)
	}
	state.KV[templatesKVKey] = raw
	return nil
}

func (h *Handler) loadTemplateCatalog(ctx context.Context, user string, pages []Page) (templateCatalog, error) {
	raw, err := h.store.KVGet(ctx, user, templatesKVKey)
	if errors.Is(err, ErrNotFound) {
		return templateCatalog{Templates: []Template{}}, nil
	}
	if err != nil {
		return templateCatalog{}, err
	}
	return decodeTemplateCatalog(raw, pages)
}

func blankCurrentTemplateID(page string) string {
	if page == "" {
		return "tpl-root"
	}
	return "tpl-page-" + page
}

func blankCurrentTemplateName(page string, pages []Page) string {
	if page == "" {
		return "根模板"
	}
	for _, p := range pages {
		if p.Name == page {
			return p.Title + " 模板"
		}
	}
	return page + " 模板"
}

func availableCurrentTemplateID(catalog templateCatalog, base string) string {
	if templateIndex(catalog.Templates, base) < 0 {
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s-%d", base, suffix)
		if templateIndex(catalog.Templates, candidate) < 0 {
			return candidate
		}
	}
}

func pageByName(pages []Page, name string) (int, bool) {
	for i := range pages {
		if pages[i].Name == name {
			return i, true
		}
	}
	return -1, false
}

type legacyLayout struct {
	Order []string `json:"order"`
	Items map[string]struct {
		W int `json:"w"`
		H int `json:"h"`
	} `json:"items"`
}

func safeLegacyID(prefix, value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	b.WriteString(prefix)
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == ':' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	if b.Len() == len(prefix) {
		b.WriteString("main")
	}
	if b.Len() > 128 {
		return b.String()[:128]
	}
	return b.String()
}

// legacyTemplateVersion derives, but never writes, a draft from the former
// Item.Meta["slot"] and layout.<page|root> conventions. Saving through the new
// API persists the strong model while old readers may keep using the legacy data.
func legacyTemplateVersion(state UserState, page string) (TemplateVersion, bool, error) {
	key := "layout.root"
	if page != "" {
		key = "layout." + page
	}
	var layout legacyLayout
	hasLayout := false
	if raw, ok := state.KV[key]; ok {
		if err := json.Unmarshal(raw, &layout); err != nil {
			return TemplateVersion{}, false, fmt.Errorf("invalid legacy layout %q: %w", key, err)
		}
		hasLayout = true
	}
	items := make(map[string]Item)
	hasSlots := false
	for _, item := range state.Items {
		if item.Page != page || item.Type != ItemHTML {
			continue
		}
		items[item.ID] = item
		if strings.TrimSpace(item.Meta["slot"]) != "" {
			hasSlots = true
		}
	}
	if !hasLayout && !hasSlots {
		return TemplateVersion{Nodes: []TemplateNode{}, Slots: []Slot{}, Placements: []Placement{}, Breakpoints: []Breakpoint{}}, false, nil
	}
	version := TemplateVersion{
		RootID: "root",
		Nodes:  []TemplateNode{{ID: "root", Kind: NodeGrid, Name: "根容器"}},
		Slots:  []Slot{}, Placements: []Placement{},
		Breakpoints: []Breakpoint{{ID: "desktop", Name: "桌面", Columns: 12, Gap: 16}},
	}
	slotIDs := make(map[string]string)
	ensureSlot := func(name string) string {
		name = strings.TrimSpace(name)
		if name == "" {
			name = "main"
		}
		if id, ok := slotIDs[name]; ok {
			return id
		}
		id := name
		if !templateIDPattern.MatchString(id) {
			id = safeLegacyID("slot-", name)
		}
		for suffix := 2; ; suffix++ {
			collision := false
			for _, existing := range slotIDs {
				if existing == id {
					collision = true
					break
				}
			}
			if !collision {
				break
			}
			id = fmt.Sprintf("%s-%d", safeLegacyID("slot-", name), suffix)
		}
		slotIDs[name] = id
		version.Slots = append(version.Slots, Slot{ID: id, NodeID: "root", Name: name, Accepts: []ItemType{ItemHTML}})
		return id
	}
	// meta.slot is already the canonical reusable binding. Derive the Slot
	// structure, but do not copy Item IDs into Placements unless layout.* carried
	// an explicit page-instance order/geometry override.
	itemsByOrder := make([]Item, 0, len(items))
	for _, item := range items {
		itemsByOrder = append(itemsByOrder, item)
	}
	sortItems(itemsByOrder)
	for _, item := range itemsByOrder {
		if strings.TrimSpace(item.Meta["slot"]) != "" {
			ensureSlot(item.Meta["slot"])
		}
	}
	ordered := make([]string, 0, len(items))
	seen := make(map[string]bool)
	for _, id := range layout.Order {
		if _, ok := items[id]; ok && !seen[id] {
			ordered = append(ordered, id)
			seen[id] = true
		}
	}
	rest := make([]Item, 0, len(items))
	for id, item := range items {
		_, hasGeometry := layout.Items[id]
		if !seen[id] && hasGeometry {
			rest = append(rest, item)
		}
	}
	sortItems(rest)
	for _, item := range rest {
		ordered = append(ordered, item.ID)
	}
	for order, id := range ordered {
		item := items[id]
		geometry := layout.Items[id]
		span, rows := geometry.W, geometry.H
		if span <= 0 {
			span = 12
		}
		if rows <= 0 {
			rows = 1
		}
		version.Placements = append(version.Placements, Placement{
			ItemID: id, SlotID: ensureSlot(item.Meta["slot"]), Breakpoint: "desktop",
			Mode: LayoutFlow, Order: order, ColumnSpan: span, RowSpan: rows,
		})
	}
	return version, true, version.Validate()
}

func currentTemplateFromState(state UserState, page string) (CurrentTemplate, []Page, templateCatalog, error) {
	if page != "" {
		if err := ValidatePageName(page); err != nil {
			return CurrentTemplate{}, nil, templateCatalog{}, err
		}
	}
	pages, err := statePages(state)
	if err != nil {
		return CurrentTemplate{}, nil, templateCatalog{}, err
	}
	pageIndex := -1
	if page != "" {
		var ok bool
		pageIndex, ok = pageByName(pages, page)
		if !ok {
			return CurrentTemplate{}, nil, templateCatalog{}, ErrNotFound
		}
	}
	catalog, err := stateTemplateCatalog(state, pages)
	if err != nil {
		return CurrentTemplate{}, nil, templateCatalog{}, err
	}
	boundID := catalog.RootTemplateID
	if pageIndex >= 0 {
		boundID = pages[pageIndex].TemplateID
	}
	if boundID != "" {
		if index := templateIndex(catalog.Templates, boundID); index >= 0 {
			return CurrentTemplate{Template: cloneTemplate(catalog.Templates[index]), Persisted: true}, pages, catalog, nil
		}
		return CurrentTemplate{}, nil, templateCatalog{}, fmt.Errorf("template binding %q is missing", boundID)
	}
	id := availableCurrentTemplateID(catalog, blankCurrentTemplateID(page))
	draft, migrated, err := legacyTemplateVersion(state, page)
	if err != nil {
		return CurrentTemplate{}, nil, templateCatalog{}, err
	}
	template := Template{ID: id, Name: blankCurrentTemplateName(page, pages), Draft: draft}
	return CurrentTemplate{Template: template, Migrated: migrated}, pages, catalog, nil
}

func conflictFor(expected uint64, current Template) error {
	copy := cloneTemplate(current)
	return &TemplateConflictError{ID: current.ID, Expected: expected, Actual: current.Revision, Current: &copy}
}

func normalizeTemplateCreate(input Template, fallbackID, fallbackName string, revision uint64, now time.Time) (Template, error) {
	input.ID = strings.TrimSpace(input.ID)
	if input.ID == "" {
		input.ID = fallbackID
	}
	if fallbackID != "" && input.ID != fallbackID {
		return Template{}, fmt.Errorf("current template id must remain %q", fallbackID)
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		input.Name = fallbackName
	}
	input.Revision = revision
	input.Draft.Version = 1
	input.Published = nil
	input.PublishedAt = nil
	input.CreatedAt, input.UpdatedAt = now, now
	normalizeTemplateCollections(&input)
	if err := input.Validate(); err != nil {
		return Template{}, err
	}
	return input, nil
}

func normalizeTemplateUpdate(current, input Template, now time.Time) (Template, bool, error) {
	if input.ID != "" && input.ID != current.ID {
		return Template{}, false, fmt.Errorf("template id is immutable")
	}
	input.ID = current.ID
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return Template{}, false, fmt.Errorf("template name is required")
	}
	normalizeTemplateCollections(&input)
	if sameTemplateDraft(current, input) {
		return cloneTemplate(current), false, nil
	}
	next := cloneTemplate(current)
	next.Name = input.Name
	next.Draft = cloneTemplateVersion(input.Draft)
	normalizeTemplateCollections(&next)
	next.Draft.Version = current.Draft.Version + 1
	next.Revision = current.Revision + 1
	next.UpdatedAt = now
	if err := next.Validate(); err != nil {
		return Template{}, false, err
	}
	return next, true, nil
}

func snapshotState(state *UserState, pages []Page, catalog templateCatalog, by, note string, keep int) {
	prependRevision(state, newRevision(state.Items, pages, by, note, catalog), keep)
}

// ListTemplates returns all persisted templates sorted by stable ID.
func (h *Handler) ListTemplates(ctx context.Context, user string) ([]Template, error) {
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()
	pages, err := h.loadPages(ctx, user)
	if err != nil {
		return nil, err
	}
	catalog, err := h.loadTemplateCatalog(ctx, user, pages)
	if err != nil {
		return nil, err
	}
	out := cloneTemplates(catalog.Templates)
	sortTemplates(out)
	return out, nil
}

// GetTemplate returns one persisted template.
func (h *Handler) GetTemplate(ctx context.Context, user, id string) (Template, error) {
	if err := validateTemplateID(id); err != nil {
		return Template{}, err
	}
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()
	pages, err := h.loadPages(ctx, user)
	if err != nil {
		return Template{}, err
	}
	catalog, err := h.loadTemplateCatalog(ctx, user, pages)
	if err != nil {
		return Template{}, err
	}
	if index := templateIndex(catalog.Templates, id); index >= 0 {
		return cloneTemplate(catalog.Templates[index]), nil
	}
	return Template{}, ErrNotFound
}

// GetCurrentTemplate resolves the root or a registered page's stable binding.
func (h *Handler) GetCurrentTemplate(ctx context.Context, user, page string) (CurrentTemplate, error) {
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()
	items, err := h.store.Items(ctx, user)
	if err != nil {
		return CurrentTemplate{}, err
	}
	state := UserState{Items: items, KV: make(map[string]json.RawMessage)}
	layoutKey := "layout.root"
	if page != "" {
		layoutKey = "layout." + page
	}
	for _, key := range []string{pagesKVKey, templatesKVKey, layoutKey} {
		raw, err := h.store.KVGet(ctx, user, key)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return CurrentTemplate{}, err
		}
		state.KV[key] = raw
	}
	current, _, _, err := currentTemplateFromState(state, page)
	return current, err
}

// PutTemplate creates at expectedRevision=0 or updates a persisted template with CAS.
func (h *Handler) PutTemplate(ctx context.Context, user string, input Template, expectedRevision uint64, by string) (Template, error) {
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()
	before, err := h.readUserStateLocked(ctx, user)
	if err != nil {
		return Template{}, err
	}
	pages, err := statePages(before)
	if err != nil {
		return Template{}, err
	}
	catalog, err := stateTemplateCatalog(before, pages)
	if err != nil {
		return Template{}, err
	}
	index := templateIndex(catalog.Templates, input.ID)
	now := time.Now()
	var nextTemplate Template
	changed := true
	if index < 0 {
		if expectedRevision != 0 {
			return Template{}, &TemplateConflictError{ID: input.ID, Expected: expectedRevision, Actual: 0}
		}
		if len(catalog.Templates) >= maxTemplatesPerUser {
			return Template{}, fmt.Errorf("too many templates")
		}
		revision, allocErr := allocateTemplateRevision(&catalog)
		if allocErr != nil {
			return Template{}, allocErr
		}
		nextTemplate, err = normalizeTemplateCreate(input, "", "未命名模板", revision, now)
		if err != nil {
			return Template{}, err
		}
		catalog.Templates = append(catalog.Templates, nextTemplate)
	} else {
		current := catalog.Templates[index]
		if expectedRevision != current.Revision {
			return Template{}, conflictFor(expectedRevision, current)
		}
		nextTemplate, changed, err = normalizeTemplateUpdate(current, input, now)
		if err != nil {
			return Template{}, err
		}
		if !changed {
			return nextTemplate, nil
		}
		nextTemplate.Revision, err = allocateTemplateRevision(&catalog)
		if err != nil {
			return Template{}, err
		}
		catalog.Templates[index] = nextTemplate
	}
	next := cloneUserState(before)
	snapshotState(&next, pages, mustStateTemplateCatalog(before, pages), by, "put template "+nextTemplate.ID, h.opts.maxRevisions)
	if err := setStateTemplateCatalog(&next, catalog, pages); err != nil {
		return Template{}, err
	}
	if err := h.replaceUserStateLocked(ctx, user, before, next); err != nil {
		return Template{}, err
	}
	h.publish(user, Event{Type: "templates", Op: "put", Templates: []Template{cloneTemplate(nextTemplate)}, By: by})
	h.metric("writes", 1)
	h.audit(ctx, user, "template.put", by, "", map[string]any{"template": nextTemplate.ID, "revision": nextTemplate.Revision})
	return cloneTemplate(nextTemplate), nil
}

func mustStateTemplateCatalog(state UserState, pages []Page) templateCatalog {
	catalog, err := stateTemplateCatalog(state, pages)
	if err != nil {
		panic(err)
	}
	return catalog
}

// PutCurrentTemplate saves and binds the root/page-scoped template atomically.
func (h *Handler) PutCurrentTemplate(ctx context.Context, user, page string, input Template, expectedRevision uint64, by string) (Template, error) {
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()
	before, err := h.readUserStateLocked(ctx, user)
	if err != nil {
		return Template{}, err
	}
	currentView, pages, catalog, err := currentTemplateFromState(before, page)
	if err != nil {
		return Template{}, err
	}
	current := currentView.Template
	index := templateIndex(catalog.Templates, current.ID)
	now := time.Now()
	var saved Template
	templateChanged := true
	if index < 0 {
		if expectedRevision != 0 {
			return Template{}, &TemplateConflictError{ID: current.ID, Expected: expectedRevision, Actual: 0}
		}
		revision, allocErr := allocateTemplateRevision(&catalog)
		if allocErr != nil {
			return Template{}, allocErr
		}
		saved, err = normalizeTemplateCreate(input, current.ID, current.Name, revision, now)
		if err != nil {
			return Template{}, err
		}
		catalog.Templates = append(catalog.Templates, saved)
	} else {
		stored := catalog.Templates[index]
		if expectedRevision != stored.Revision {
			return Template{}, conflictFor(expectedRevision, stored)
		}
		saved, templateChanged, err = normalizeTemplateUpdate(stored, input, now)
		if err != nil {
			return Template{}, err
		}
		if templateChanged {
			saved.Revision, err = allocateTemplateRevision(&catalog)
			if err != nil {
				return Template{}, err
			}
		}
		catalog.Templates[index] = saved
	}
	bindingChanged := false
	if page == "" {
		bindingChanged = catalog.RootTemplateID != saved.ID
		catalog.RootTemplateID = saved.ID
	} else {
		pageIndex, _ := pageByName(pages, page)
		bindingChanged = pages[pageIndex].TemplateID != saved.ID
		pages[pageIndex].TemplateID = saved.ID
	}
	if !templateChanged && !bindingChanged {
		return cloneTemplate(saved), nil
	}
	next := cloneUserState(before)
	oldPages, err := statePages(before)
	if err != nil {
		return Template{}, err
	}
	oldCatalog, err := stateTemplateCatalog(before, oldPages)
	if err != nil {
		return Template{}, err
	}
	snapshotState(&next, oldPages, oldCatalog, by, "put current template "+saved.ID, h.opts.maxRevisions)
	if err := setStatePages(&next, pages); err != nil {
		return Template{}, err
	}
	if err := setStateTemplateCatalog(&next, catalog, pages); err != nil {
		return Template{}, err
	}
	if err := h.replaceUserStateLocked(ctx, user, before, next); err != nil {
		return Template{}, err
	}
	h.publish(user, Event{Type: "templates", Op: "put", Templates: []Template{cloneTemplate(saved)}, By: by})
	if bindingChanged && page != "" {
		h.publish(user, Event{Type: "pages"})
	}
	h.metric("writes", 1)
	h.audit(ctx, user, "template.current.put", by, "", map[string]any{"template": saved.ID, "page": page, "revision": saved.Revision})
	return cloneTemplate(saved), nil
}

// PublishTemplate copies the current draft into Published with CAS.
func (h *Handler) PublishTemplate(ctx context.Context, user, id string, expectedRevision uint64, by string) (Template, error) {
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()
	before, err := h.readUserStateLocked(ctx, user)
	if err != nil {
		return Template{}, err
	}
	pages, err := statePages(before)
	if err != nil {
		return Template{}, err
	}
	catalog, err := stateTemplateCatalog(before, pages)
	if err != nil {
		return Template{}, err
	}
	index := templateIndex(catalog.Templates, id)
	if index < 0 {
		return Template{}, ErrNotFound
	}
	current := catalog.Templates[index]
	if expectedRevision != current.Revision {
		return Template{}, conflictFor(expectedRevision, current)
	}
	if current.Published != nil && reflect.DeepEqual(*current.Published, current.Draft) {
		return cloneTemplate(current), nil
	}
	now := time.Now()
	nextTemplate := cloneTemplate(current)
	published := cloneTemplateVersion(current.Draft)
	nextTemplate.Published, nextTemplate.PublishedAt = &published, &now
	nextTemplate.Revision, err = allocateTemplateRevision(&catalog)
	if err != nil {
		return Template{}, err
	}
	nextTemplate.UpdatedAt = now
	catalog.Templates[index] = nextTemplate
	next := cloneUserState(before)
	snapshotState(&next, pages, mustStateTemplateCatalog(before, pages), by, "publish template "+id, h.opts.maxRevisions)
	if err := setStateTemplateCatalog(&next, catalog, pages); err != nil {
		return Template{}, err
	}
	if err := h.replaceUserStateLocked(ctx, user, before, next); err != nil {
		return Template{}, err
	}
	h.publish(user, Event{Type: "templates", Op: "publish", Templates: []Template{cloneTemplate(nextTemplate)}, By: by})
	h.metric("writes", 1)
	h.audit(ctx, user, "template.publish", by, "", map[string]any{"template": id, "revision": nextTemplate.Revision})
	return cloneTemplate(nextTemplate), nil
}

// DeleteTemplateSlot removes a slot from the draft and moves all its Item
// placements to the unassigned tray. It never mutates the Item collection.
func (h *Handler) DeleteTemplateSlot(ctx context.Context, user, id, slotID string, expectedRevision uint64, by string) (Template, error) {
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()
	before, err := h.readUserStateLocked(ctx, user)
	if err != nil {
		return Template{}, err
	}
	pages, err := statePages(before)
	if err != nil {
		return Template{}, err
	}
	catalog, err := stateTemplateCatalog(before, pages)
	if err != nil {
		return Template{}, err
	}
	index := templateIndex(catalog.Templates, id)
	if index < 0 {
		return Template{}, ErrNotFound
	}
	current := catalog.Templates[index]
	if expectedRevision != current.Revision {
		return Template{}, conflictFor(expectedRevision, current)
	}
	nextTemplate := cloneTemplate(current)
	found := false
	kept := nextTemplate.Draft.Slots[:0]
	for _, slot := range nextTemplate.Draft.Slots {
		if slot.ID == slotID {
			found = true
			continue
		}
		kept = append(kept, slot)
	}
	if !found {
		return Template{}, ErrNotFound
	}
	nextTemplate.Draft.Slots = kept
	for i := range nextTemplate.Draft.Placements {
		if nextTemplate.Draft.Placements[i].SlotID == slotID {
			nextTemplate.Draft.Placements[i].SlotID = ""
			nextTemplate.Draft.Placements[i].Unassigned = true
		}
	}
	nextTemplate.Draft.Version++
	nextTemplate.Revision, err = allocateTemplateRevision(&catalog)
	if err != nil {
		return Template{}, err
	}
	nextTemplate.UpdatedAt = time.Now()
	if err := nextTemplate.Validate(); err != nil {
		return Template{}, err
	}
	catalog.Templates[index] = nextTemplate
	next := cloneUserState(before)
	snapshotState(&next, pages, mustStateTemplateCatalog(before, pages), by, "delete template slot "+slotID, h.opts.maxRevisions)
	if err := setStateTemplateCatalog(&next, catalog, pages); err != nil {
		return Template{}, err
	}
	if err := h.replaceUserStateLocked(ctx, user, before, next); err != nil {
		return Template{}, err
	}
	h.publish(user, Event{Type: "templates", Op: "put", Templates: []Template{cloneTemplate(nextTemplate)}, By: by})
	h.metric("writes", 1)
	h.audit(ctx, user, "template.slot.delete", by, "", map[string]any{"template": id, "slot": slotID})
	return cloneTemplate(nextTemplate), nil
}

// DeleteTemplate deletes only the template and clears its page/root bindings.
// Items and their source code remain untouched.
func (h *Handler) DeleteTemplate(ctx context.Context, user, id string, expectedRevision uint64, by string) error {
	lock := h.userLock(user)
	lock.Lock()
	defer lock.Unlock()
	before, err := h.readUserStateLocked(ctx, user)
	if err != nil {
		return err
	}
	pages, err := statePages(before)
	if err != nil {
		return err
	}
	catalog, err := stateTemplateCatalog(before, pages)
	if err != nil {
		return err
	}
	index := templateIndex(catalog.Templates, id)
	if index < 0 {
		return ErrNotFound
	}
	current := catalog.Templates[index]
	if expectedRevision != current.Revision {
		return conflictFor(expectedRevision, current)
	}
	if _, err := allocateTemplateRevision(&catalog); err != nil {
		return err
	}
	catalog.Templates = append(catalog.Templates[:index], catalog.Templates[index+1:]...)
	bindingsChanged := false
	if catalog.RootTemplateID == id {
		catalog.RootTemplateID = ""
		bindingsChanged = true
	}
	for i := range pages {
		if pages[i].TemplateID == id {
			pages[i].TemplateID = ""
			bindingsChanged = true
		}
	}
	next := cloneUserState(before)
	oldPages, err := statePages(before)
	if err != nil {
		return err
	}
	oldCatalog, err := stateTemplateCatalog(before, oldPages)
	if err != nil {
		return err
	}
	snapshotState(&next, oldPages, oldCatalog, by, "delete template "+id, h.opts.maxRevisions)
	if err := setStatePages(&next, pages); err != nil {
		return err
	}
	if err := setStateTemplateCatalog(&next, catalog, pages); err != nil {
		return err
	}
	if err := h.replaceUserStateLocked(ctx, user, before, next); err != nil {
		return err
	}
	h.publish(user, Event{Type: "templates", Op: "delete", IDs: []string{id}, By: by})
	if bindingsChanged {
		h.publish(user, Event{Type: "pages"})
	}
	h.metric("writes", 1)
	h.audit(ctx, user, "template.delete", by, "", map[string]any{"template": id})
	return nil
}
