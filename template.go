package ihtml

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

// LayoutMode describes how a placement participates in layout. Flow placements
// are responsive grid children; absolute placements use canvas coordinates.
type LayoutMode string

const (
	LayoutFlow     LayoutMode = "flow"
	LayoutAbsolute LayoutMode = "absolute"
)

// TemplateNodeKind is the structural role of a template node. Nodes contain no
// page content: reusable Item bindings remain on Item.Meta, with Placement only
// as an optional instance override. A completely empty template is valid.
type TemplateNodeKind string

const (
	NodeCanvas TemplateNodeKind = "canvas"
	NodeGrid   TemplateNodeKind = "grid"
	NodeStack  TemplateNodeKind = "stack"
	NodeGroup  TemplateNodeKind = "group"
)

// TemplateNode forms a tree through ParentID. Props is reserved for
// renderer-specific JSON hints; the built-in layouts/frames shapes receive the
// same finite/range validation as typed Placement and Breakpoint geometry.
type TemplateNode struct {
	ID       string                     `json:"id"`
	ParentID string                     `json:"parent_id,omitempty"`
	Kind     TemplateNodeKind           `json:"kind"`
	Name     string                     `json:"name,omitempty"`
	Order    int                        `json:"order,omitempty"`
	Props    map[string]json.RawMessage `json:"props,omitempty"`
}

// Slot is a named drop target owned by a node. Removing a slot never removes
// an Item; DeleteTemplateSlot moves its placements to the unassigned tray.
type Slot struct {
	ID      string     `json:"id"`
	NodeID  string     `json:"node_id"`
	Name    string     `json:"name,omitempty"`
	Accepts []ItemType `json:"accepts,omitempty"`
	Locked  bool       `json:"locked,omitempty"`
}

// Placement is an optional page-instance/legacy override that binds one concrete
// Item at one breakpoint. Because ItemID makes a document non-portable, new
// reusable templates should normally leave Placements empty and bind Items with
// Item.Meta["slot"] == Slot.ID. An empty SlotID with Unassigned=true represents
// the compatibility/editor tray; runtimes append it to Root rather than hiding it.
type Placement struct {
	ItemID     string     `json:"item_id"`
	SlotID     string     `json:"slot_id,omitempty"`
	Unassigned bool       `json:"unassigned,omitempty"`
	Breakpoint string     `json:"breakpoint,omitempty"`
	Mode       LayoutMode `json:"mode"`
	Order      int        `json:"order,omitempty"`
	Column     int        `json:"column,omitempty"`
	ColumnSpan int        `json:"column_span,omitempty"`
	Row        int        `json:"row,omitempty"`
	RowSpan    int        `json:"row_span,omitempty"`
	X          float64    `json:"x,omitempty"`
	Y          float64    `json:"y,omitempty"`
	Width      float64    `json:"width,omitempty"`
	Height     float64    `json:"height,omitempty"`
	ZIndex     int        `json:"z_index,omitempty"`
	Locked     bool       `json:"locked,omitempty"`
	Hidden     bool       `json:"hidden,omitempty"`
}

// Breakpoint defines a responsive editing surface. A zero MinWidth/MaxWidth is
// open-ended; Columns and Gap apply to flow layout.
type Breakpoint struct {
	ID       string  `json:"id"`
	Name     string  `json:"name,omitempty"`
	MinWidth int     `json:"min_width,omitempty"`
	MaxWidth int     `json:"max_width,omitempty"`
	Columns  int     `json:"columns,omitempty"`
	Gap      float64 `json:"gap,omitempty"`
}

// TemplateVersion is a complete immutable-by-value template document. Draft is
// edited in place by creating a new Version; Published is a copy of a draft.
type TemplateVersion struct {
	Version     uint64         `json:"version"`
	RootID      string         `json:"root_id,omitempty"`
	Nodes       []TemplateNode `json:"nodes"`
	Slots       []Slot         `json:"slots"`
	Placements  []Placement    `json:"placements"`
	Breakpoints []Breakpoint   `json:"breakpoints"`
}

// Template is a reusable layout document. Revision is the CAS token for every
// mutation, independent from Draft.Version and Published.Version.
type Template struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Revision    uint64           `json:"revision"`
	Draft       TemplateVersion  `json:"draft"`
	Published   *TemplateVersion `json:"published,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	PublishedAt *time.Time       `json:"published_at,omitempty"`
}

// CurrentTemplate is returned by the page-scoped convenience API. Persisted is
// false for a deterministic empty/legacy-derived draft that has not been saved.
type CurrentTemplate struct {
	Template  Template `json:"template"`
	Persisted bool     `json:"persisted"`
	Migrated  bool     `json:"migrated,omitempty"`
}

var (
	// ErrConflict indicates a failed optimistic-concurrency (CAS) check.
	ErrConflict       = errors.New("ihtml: revision conflict")
	templateIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// TemplateConflictError reports both revisions and includes the latest value so
// an editor can offer reload/merge without another round trip.
type TemplateConflictError struct {
	ID       string
	Expected uint64
	Actual   uint64
	Current  *Template
}

func (e *TemplateConflictError) Error() string {
	return fmt.Sprintf("template %q revision conflict: expected %d, current %d", e.ID, e.Expected, e.Actual)
}

func (e *TemplateConflictError) Unwrap() error { return ErrConflict }

const (
	maxTemplatesPerUser    = 128
	maxTemplateNodes       = 512
	maxTemplateSlots       = 512
	maxTemplatePlacements  = 2048
	maxTemplateBreakpoints = 32
	maxTemplateProps       = 64
	maxTemplatePropBytes   = 16 << 10
	maxTemplateOrder       = 1_000_000
	maxTemplateGridLine    = 1_000_000
	maxTemplateCoordinate  = 1_000_000
	maxTemplateZIndex      = 1_000_000
	maxBreakpointWidth     = 1_000_000
	// JSON clients represent numbers as IEEE-754 doubles. Revisions stay inside
	// the exact integer range so every valid CAS token can be sent back losslessly.
	maxTemplateRevision = uint64(1<<53 - 1)
)

func finiteRange(value, min, max float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= min && value <= max
}

func decodePropertyObject(owner string, raw json.RawMessage) (map[string]json.RawMessage, error) {
	if string(raw) == "null" {
		return nil, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object: %w", owner, err)
	}
	return object, nil
}

func validateNumericProperty(owner string, object map[string]json.RawMessage, key string, min, max float64) error {
	raw, exists := object[key]
	if !exists {
		return nil
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil || !finiteRange(value, min, max) {
		return fmt.Errorf("%s.%s must be a finite number between %g and %g", owner, key, min, max)
	}
	return nil
}

func validateLayoutProperty(owner string, raw json.RawMessage) error {
	layout, err := decodePropertyObject(owner, raw)
	if err != nil {
		return err
	}
	for _, field := range []struct {
		name     string
		min, max float64
	}{
		{"gap", 0, 4096}, {"padding", 0, 4096}, {"columns", 0, 64},
		{"minHeight", 0, maxTemplateCoordinate},
	} {
		if err := validateNumericProperty(owner, layout, field.name, field.min, field.max); err != nil {
			return err
		}
	}
	return nil
}

func validateBreakpointGeometryProperty(owner, kind string, raw json.RawMessage) error {
	byBreakpoint, err := decodePropertyObject(owner, raw)
	if err != nil {
		return err
	}
	if len(byBreakpoint) > maxTemplateBreakpoints {
		return fmt.Errorf("%s has too many breakpoints", owner)
	}
	for breakpoint, value := range byBreakpoint {
		if err := validateStructuralID("property breakpoint", breakpoint); err != nil {
			return err
		}
		entryOwner := owner + "." + breakpoint
		if kind == "layouts" {
			if err := validateLayoutProperty(entryOwner, value); err != nil {
				return err
			}
			continue
		}
		frame, err := decodePropertyObject(entryOwner, value)
		if err != nil {
			return err
		}
		for _, field := range []struct {
			name     string
			min, max float64
		}{
			{"x", 0, maxTemplateCoordinate}, {"y", 0, maxTemplateCoordinate},
			{"w", 0, maxTemplateCoordinate}, {"h", 0, maxTemplateCoordinate},
			{"span", 0, 64},
		} {
			if err := validateNumericProperty(entryOwner, frame, field.name, field.min, field.max); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateKnownNodeProperty(nodeID, key string, raw json.RawMessage) error {
	switch key {
	case "layout":
		return validateLayoutProperty("node "+nodeID+" props.layout", raw)
	case "layouts", "frames":
		return validateBreakpointGeometryProperty("node "+nodeID+" props."+key, key, raw)
	case "slot_layouts":
		owner := "node " + nodeID + " props.slot_layouts"
		bySlot, err := decodePropertyObject(owner, raw)
		if err != nil {
			return err
		}
		if len(bySlot) > maxTemplateSlots {
			return fmt.Errorf("%s has too many slots", owner)
		}
		for slotID, value := range bySlot {
			if err := validateStructuralID("slot layout", slotID); err != nil {
				return err
			}
			slotOwner := owner + "." + slotID
			slotLayout, err := decodePropertyObject(slotOwner, value)
			if err != nil {
				return err
			}
			for _, nestedKey := range []string{"layouts", "frames"} {
				if nested, exists := slotLayout[nestedKey]; exists {
					if err := validateBreakpointGeometryProperty(slotOwner+"."+nestedKey, nestedKey, nested); err != nil {
						return err
					}
				}
			}
			if len(slotLayout) > maxTemplateProps {
				return fmt.Errorf("%s has too many properties", slotOwner)
			}
			for nestedKey, nested := range slotLayout {
				if len(nested) > maxTemplatePropBytes || !json.Valid(nested) {
					return fmt.Errorf("%s.%s must be valid JSON up to %d bytes", slotOwner, nestedKey, maxTemplatePropBytes)
				}
				if err := validateStructuralID("slot layout property", nestedKey); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateTemplateID(id string) error {
	if !templateIDPattern.MatchString(id) {
		return fmt.Errorf("invalid template id %q", id)
	}
	return nil
}

func validateStructuralID(kind, id string) error {
	if !templateIDPattern.MatchString(id) {
		return fmt.Errorf("invalid %s id %q", kind, id)
	}
	return nil
}

// Validate checks the complete template document, including tree acyclicity and
// all node/slot/breakpoint references. Empty Draft arrays are intentionally valid.
func (t *Template) Validate() error {
	if err := validateTemplateID(t.ID); err != nil {
		return err
	}
	if strings.TrimSpace(t.Name) == "" || len(t.Name) > 128 {
		return fmt.Errorf("template %q: name is required and must be at most 128 bytes", t.ID)
	}
	if t.Revision > maxTemplateRevision {
		return fmt.Errorf("template %q revision exceeds the JSON-safe integer range", t.ID)
	}
	if err := t.Draft.Validate(); err != nil {
		return fmt.Errorf("template %q draft: %w", t.ID, err)
	}
	if t.Published != nil {
		if err := t.Published.Validate(); err != nil {
			return fmt.Errorf("template %q published: %w", t.ID, err)
		}
	}
	return nil
}

// Validate checks a template version. Version zero is accepted only so a
// virtual, not-yet-persisted current template can be round-tripped by the API.
func (v *TemplateVersion) Validate() error {
	if v.Version > maxTemplateRevision {
		return fmt.Errorf("template version exceeds the JSON-safe integer range")
	}
	if len(v.Nodes) > maxTemplateNodes || len(v.Slots) > maxTemplateSlots ||
		len(v.Placements) > maxTemplatePlacements || len(v.Breakpoints) > maxTemplateBreakpoints {
		return fmt.Errorf("template document exceeds limits (nodes=%d slots=%d placements=%d breakpoints=%d)",
			len(v.Nodes), len(v.Slots), len(v.Placements), len(v.Breakpoints))
	}
	nodes := make(map[string]TemplateNode, len(v.Nodes))
	for _, node := range v.Nodes {
		if err := validateStructuralID("node", node.ID); err != nil {
			return err
		}
		switch node.Kind {
		case NodeCanvas, NodeGrid, NodeStack, NodeGroup:
		default:
			return fmt.Errorf("node %q: unsupported kind %q", node.ID, node.Kind)
		}
		if _, exists := nodes[node.ID]; exists {
			return fmt.Errorf("duplicate node id %q", node.ID)
		}
		if len(node.Name) > 128 || len(node.Props) > maxTemplateProps {
			return fmt.Errorf("node %q: name or properties exceed limits", node.ID)
		}
		if node.Order < -maxTemplateOrder || node.Order > maxTemplateOrder {
			return fmt.Errorf("node %q order is out of range", node.ID)
		}
		for key, value := range node.Props {
			if err := validateStructuralID("property", key); err != nil {
				return err
			}
			if len(value) > maxTemplatePropBytes || !json.Valid(value) {
				return fmt.Errorf("node %q property %q must be valid JSON up to %d bytes", node.ID, key, maxTemplatePropBytes)
			}
			if err := validateKnownNodeProperty(node.ID, key, value); err != nil {
				return err
			}
		}
		nodes[node.ID] = node
	}
	if len(nodes) == 0 {
		if v.RootID != "" {
			return fmt.Errorf("root_id must be empty when nodes is empty")
		}
	} else {
		if _, ok := nodes[v.RootID]; !ok {
			return fmt.Errorf("root node %q does not exist", v.RootID)
		}
		if nodes[v.RootID].ParentID != "" {
			return fmt.Errorf("root node %q must not have a parent", v.RootID)
		}
		for id, node := range nodes {
			if id == v.RootID {
				continue
			}
			if _, ok := nodes[node.ParentID]; !ok {
				return fmt.Errorf("node %q references missing parent %q", id, node.ParentID)
			}
			seen := map[string]bool{id: true}
			for parent := node.ParentID; parent != ""; parent = nodes[parent].ParentID {
				if seen[parent] {
					return fmt.Errorf("node tree contains a cycle at %q", parent)
				}
				seen[parent] = true
			}
			if !seen[v.RootID] {
				return fmt.Errorf("node %q is disconnected from root %q", id, v.RootID)
			}
		}
	}

	breakpoints := make(map[string]Breakpoint, len(v.Breakpoints))
	for _, bp := range v.Breakpoints {
		if err := validateStructuralID("breakpoint", bp.ID); err != nil {
			return err
		}
		if _, exists := breakpoints[bp.ID]; exists {
			return fmt.Errorf("duplicate breakpoint id %q", bp.ID)
		}
		if len(bp.Name) > 128 || bp.MinWidth < 0 || bp.MinWidth > maxBreakpointWidth ||
			bp.MaxWidth < 0 || bp.MaxWidth > maxBreakpointWidth ||
			bp.MaxWidth > 0 && bp.MinWidth > bp.MaxWidth ||
			bp.Columns < 0 || bp.Columns > 64 || !finiteRange(bp.Gap, 0, 4096) {
			return fmt.Errorf("breakpoint %q has invalid bounds/grid values", bp.ID)
		}
		breakpoints[bp.ID] = bp
	}

	slots := make(map[string]Slot, len(v.Slots))
	for _, slot := range v.Slots {
		if err := validateStructuralID("slot", slot.ID); err != nil {
			return err
		}
		if _, exists := slots[slot.ID]; exists {
			return fmt.Errorf("duplicate slot id %q", slot.ID)
		}
		if _, ok := nodes[slot.NodeID]; !ok {
			return fmt.Errorf("slot %q references missing node %q", slot.ID, slot.NodeID)
		}
		if len(slot.Name) > 128 {
			return fmt.Errorf("slot %q name is too long", slot.ID)
		}
		accepted := make(map[ItemType]bool, len(slot.Accepts))
		for _, typ := range slot.Accepts {
			if typ != ItemHTML && typ != ItemJS && typ != ItemCSS {
				return fmt.Errorf("slot %q accepts unsupported item type %q", slot.ID, typ)
			}
			if accepted[typ] {
				return fmt.Errorf("slot %q repeats accepted item type %q", slot.ID, typ)
			}
			accepted[typ] = true
		}
		slots[slot.ID] = slot
	}

	placementKeys := make(map[string]bool, len(v.Placements))
	for _, p := range v.Placements {
		if strings.TrimSpace(p.ItemID) == "" || len(p.ItemID) > 128 || strings.ContainsAny(p.ItemID, "\"'<>`&\\/\r\n\t ") {
			return fmt.Errorf("invalid placement item id %q", p.ItemID)
		}
		if p.Breakpoint != "" {
			if _, ok := breakpoints[p.Breakpoint]; !ok {
				return fmt.Errorf("placement for %q references missing breakpoint %q", p.ItemID, p.Breakpoint)
			}
		}
		key := p.ItemID + "\x00" + p.Breakpoint
		if placementKeys[key] {
			return fmt.Errorf("duplicate placement for item %q at breakpoint %q", p.ItemID, p.Breakpoint)
		}
		placementKeys[key] = true
		if p.Order < -maxTemplateOrder || p.Order > maxTemplateOrder ||
			p.ZIndex < -maxTemplateZIndex || p.ZIndex > maxTemplateZIndex {
			return fmt.Errorf("placement for %q has order or z-index out of range", p.ItemID)
		}
		if p.Column < 0 || p.Column > 64 || p.ColumnSpan < 0 || p.ColumnSpan > 64 ||
			p.Row < 0 || p.Row > maxTemplateGridLine || p.RowSpan < 0 || p.RowSpan > maxTemplateGridLine {
			return fmt.Errorf("placement for %q has grid geometry out of range", p.ItemID)
		}
		if !finiteRange(p.X, 0, maxTemplateCoordinate) || !finiteRange(p.Y, 0, maxTemplateCoordinate) ||
			!finiteRange(p.Width, 0, maxTemplateCoordinate) || !finiteRange(p.Height, 0, maxTemplateCoordinate) {
			return fmt.Errorf("placement for %q has canvas geometry out of range", p.ItemID)
		}
		if p.SlotID == "" {
			if !p.Unassigned {
				return fmt.Errorf("placement for %q without slot must be unassigned", p.ItemID)
			}
		} else {
			if p.Unassigned {
				return fmt.Errorf("placement for %q cannot be assigned and unassigned", p.ItemID)
			}
			if _, ok := slots[p.SlotID]; !ok {
				return fmt.Errorf("placement for %q references missing slot %q", p.ItemID, p.SlotID)
			}
		}
		switch p.Mode {
		case LayoutFlow:
			if p.Breakpoint != "" && breakpoints[p.Breakpoint].Columns > 0 &&
				p.ColumnSpan > breakpoints[p.Breakpoint].Columns {
				return fmt.Errorf("flow placement for %q spans more columns than breakpoint %q", p.ItemID, p.Breakpoint)
			}
		case LayoutAbsolute:
		default:
			return fmt.Errorf("placement for %q has unsupported mode %q", p.ItemID, p.Mode)
		}
	}
	return nil
}

func cloneTemplateVersion(v TemplateVersion) TemplateVersion {
	out := v
	if v.Nodes != nil {
		out.Nodes = append([]TemplateNode{}, v.Nodes...)
	}
	for i := range out.Nodes {
		if v.Nodes[i].Props != nil {
			out.Nodes[i].Props = make(map[string]json.RawMessage, len(v.Nodes[i].Props))
			for key, value := range v.Nodes[i].Props {
				out.Nodes[i].Props[key] = append(json.RawMessage(nil), value...)
			}
		}
	}
	if v.Slots != nil {
		out.Slots = append([]Slot{}, v.Slots...)
	}
	for i := range out.Slots {
		out.Slots[i].Accepts = append([]ItemType(nil), v.Slots[i].Accepts...)
	}
	if v.Placements != nil {
		out.Placements = append([]Placement{}, v.Placements...)
	}
	if v.Breakpoints != nil {
		out.Breakpoints = append([]Breakpoint{}, v.Breakpoints...)
	}
	return out
}

func cloneTemplate(t Template) Template {
	out := t
	out.Draft = cloneTemplateVersion(t.Draft)
	if t.Published != nil {
		published := cloneTemplateVersion(*t.Published)
		out.Published = &published
	}
	if t.PublishedAt != nil {
		at := *t.PublishedAt
		out.PublishedAt = &at
	}
	return out
}

func cloneTemplates(templates []Template) []Template {
	if templates == nil {
		return nil
	}
	out := make([]Template, len(templates))
	for i := range templates {
		out[i] = cloneTemplate(templates[i])
	}
	return out
}

func sameTemplateDraft(a, b Template) bool {
	left, right := cloneTemplateVersion(a.Draft), cloneTemplateVersion(b.Draft)
	right.Version = left.Version
	return a.Name == b.Name && reflect.DeepEqual(left, right)
}

func sortTemplates(templates []Template) {
	sort.SliceStable(templates, func(i, j int) bool { return templates[i].ID < templates[j].ID })
}
