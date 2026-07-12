# Item editor design QA

## Evidence

- Source visual truth: `/Users/zdypro/Documents/src/ihtml/design/item-editor-target.png`
- Browser-rendered implementation: `/Users/zdypro/Documents/src/ihtml/design/item-editor-implementation.png`
- Full-view comparison: `/Users/zdypro/Documents/src/ihtml/design/item-editor-comparison-full.png`
- Focused controls comparison: `/Users/zdypro/Documents/src/ihtml/design/item-editor-comparison-focus.png`
- Responsive evidence: `/Users/zdypro/Documents/src/ihtml/design/item-editor-mobile.png`
- Collapsed AI-process spacing evidence: `/Users/zdypro/Documents/src/ihtml/design/chat-collapsed-spacing.png`
- Viewport: `1487 × 1058` for the source/implementation comparison; `390 × 844` for responsive QA.
- State: dark theme, root page, Free Position mode, an existing Item selected. The source contains illustrative mock content, while the implementation intentionally renders the real dynamic Items supplied by the current page.

## Findings

No actionable P0, P1, or P2 finding remains.

- Fonts and typography: the implementation uses the product's existing `--ui-font` system stack and preserves the source's compact hierarchy, muted metadata, and stronger selected/property labels. Long Item titles truncate in the layer list instead of changing column width.
- Spacing and layout rhythm: the desktop three-region frame, toolbar, canvas grid, selected outline, contextual actions, and properties rail follow the source proportions. The central content geometry intentionally follows the live page rather than the illustrative mock. The collapsed AI process summary sits directly above the formal answer without the earlier dead space.
- Colors and tokens: editor surfaces, borders, focus/selection purple, muted copy, destructive red, and disabled states all use existing product tokens and remain legible in dark mode.
- Image and asset fidelity: neither design needs photographic or illustrative assets. The implementation does not substitute fake SVG/CSS art. Text layer controls are retained because the project has no established icon library; this is an accessibility-safe, non-blocking deviation from the mock's glyph controls.
- Copy and content: editor chrome says it edits existing page content; Template editing is explicitly secondary and described as empty structure. Dynamic Item content is intentionally not hard-coded from the mock.
- Accessibility and behavior: the editor is a labelled modal with semantic radio modes, buttons, labelled numeric/color fields, keyboard move/resize, undo/redo, focus containment, and restored Item tab stops after exit. Editing controls are an external overlay and add no children to Item content DOM.
- Responsiveness: at `390 × 844`, the layer rail is removed, the inspector becomes a scrollable bottom panel, all persistent controls remain reachable, Item spans are normalized against Template breakpoint columns, and document/editor widths both remain exactly `390px` with zero horizontal overflow.

Accepted P3 differences:

- The mock centers a fabricated hero Item; the implementation preserves each real Item's existing geometry when entering Free Position mode.
- Context actions use short text labels until the product adopts an icon set; no fake glyph or handcrafted icon was introduced.

## Comparison history

### Iteration 1 — blocked

- [P1] A four-column published Template was initially interpreted as four columns of the editor's twelve-column grid on mobile, compressing the selected Item.
- [P2] The selection context bar could clip past the left edge for a narrow selected Item.
- Fixes: normalized Template placement spans from the active breakpoint column count into twelve editor columns; anchored the mobile context bar from the selected Item's left edge.
- Post-fix evidence: `/Users/zdypro/Documents/src/ihtml/design/item-editor-mobile.png` shows full-width Items, all three context actions, the bottom inspector, and no horizontal overflow.

### Iteration 2 — blocked

- [P2] Live Item controls remained keyboard-focusable while layout editing, creating a risk of changing business data instead of layout.
- [P2] A late SSE resync could replace the current selection or overwrite unsaved local layout state.
- Fixes: disabled Item descendant pointer/tab interaction only while editing and restored original tab stops on close; preserved selection across clean reloads and converted dirty remote updates into an explicit conflict state.
- Post-fix evidence: browser checks found zero editor nodes inside Item DOM, all Item controls received `tabindex=-1` only during editing, original values returned after exit, and dirty remote updates displayed the conflict message without overwrite.

### Iteration 3 — passed

- Full and focused comparisons were repeated at the source's exact `1487 × 1058` dimensions.
- The final mobile pass reported `innerWidth=390`, `document.scrollWidth=390`, and `editor.scrollWidth=390`.
- Browser console/developer log check returned no warnings or errors.

## Primary interactions tested

- Select an existing Item from the live canvas and layer rail.
- Switch Auto Layout / Free Position.
- Real pointer drag and resize; keyboard move and resize alternatives.
- Numeric position/size edit, background color edit, clear, undo, and redo.
- Save, reload, and verify persisted inline geometry and mode.
- Delete cancellation, successful deletion of a page-scoped QA Item, and layout cleanup.
- External KV conflict while dirty.
- Published Template Slot and fallback Items.
- Narrow-screen numeric editing and overflow behavior.
- AI process collapsed spacing.

## Implementation checklist

- [x] Existing Items only in the default workflow.
- [x] Position, size, colors, order/z-index, delete, undo/redo, save, and exit.
- [x] Separate `item-layout.*` persistence and AI-write protection.
- [x] Template structure remains an advanced, content-free entry.
- [x] Desktop and narrow-screen behavior.
- [x] Browser interaction, persistence, deletion, conflict, Template, accessibility, and console checks.

## Follow-up polish

- If an official icon library is added later, the three contextual layer/delete text controls can adopt matching product icons without changing layout or behavior.

final result: passed
