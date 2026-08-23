package ihtml

// PageAuthoringGuide is the host-independent design contract for AI-authored
// pages. Hosts that supply their own Agent can reuse it without constructing
// ihtml's optional chat backend or a second model runtime.
const PageAuthoringGuide = `
## ihtml 页面设计契约
- 页面面向实际工作，不做营销页。信息层级要紧凑、安静、便于扫描；不要用大号 Hero、装饰性渐变、Emoji 标题或把每个区块都做成卡片。
- 优先组合内核组件：页面骨架使用 ui-page，标题区使用 ui-section，指标组使用 ui-grid + ui-card/ui-stat，工具栏使用 ui-toolbar，输入控件使用 ui-input/ui-select/ui-btn，数据表使用 ui-table-wrap + ui-table，空态使用 ui-empty。颜色、边框、间距和语义状态只使用 --ui-* tokens。
- 同一个页面必须同时适配窄屏和宽屏。整体布局不得固定像素宽度；网格使用 auto-fit/minmax；以 .ihtml-root.ihtml-mobile 作为容器断点。宽表格只能在自己的 ui-table-wrap 内滚动；窄屏若仍难以阅读，应提供卡片或列表视图，不得把桌面表格强塞进手机。
- 卡片只用于指标或重复记录，不得嵌套卡片，也不得把整个页面分区全部包装成浮动卡片。按钮、计数器、图表和表格应有稳定尺寸，动态内容不能推动周边布局跳动。
- CSS 必须限定在 .ihtml-root 或 Item 自身容器，不使用 html/body/:root 全局选择器，不写原始主题色，不覆盖宿主导航。正文不得出现横向页面级溢出；长文本必须换行或省略。
- 页面由多个职责清晰的 Item 组成：结构、样式、行为分离；复杂页面继续按可独立维护的业务区块拆分 HTML Item。不要把整页 HTML、CSS、JS 塞进一个超大 Item。
- 使用 ihtml.env.mode/width 和 env.onChange 响应容器变化；图表使用 ihtml.chart，动态请求使用 ihtml.http，格式化使用 ihtml.fmt，动态文本使用 textContent 或 ihtml.util.esc。交互要包含加载、空结果、失败和重试状态。
- 发布前按手机与桌面两种容器审查：内容是否完整可见、无页面级横向滚动、主操作是否明确、表格是否可读、颜色对比和键盘焦点是否可辨。视觉漂亮不能牺牲数据真实性或权限边界。
`
