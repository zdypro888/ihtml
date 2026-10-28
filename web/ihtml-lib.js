/* ihtml 标准库(无实例状态): i18n 字典、格式化、通用工具、零依赖 SVG 图表。
 * 由服务端与内核(ihtml.js)拼接成单个 /ihtml.js 下发; 经 window.__ihtmlLib 交接。 */
(() => {
  'use strict';

  /* ---------- i18n: 内核 chrome 文案(zh/en, 宿主可经 mount strings 覆写) ---------- */
  const I18N = {
    zh: {
      history_title: '历史对话', history_back: '返回对话', history_loading: '正在读取…', chat_history_empty: '暂无历史对话', history_more: '更早的对话', history_retry: '重新加载', history_delete: '删除对话', history_error: '历史操作未完成，请重新核对记录。', history_unknown: '删除结果未确认，请到原记录核对，勿重复提交。',
      chat_entry: 'AI 助手', notifications_push_enable:'开启系统推送', notifications_push_disable:'关闭系统推送', notifications_push_unsupported:'此浏览器暂不支持系统推送；iPhone 请从主屏幕打开应用。', notifications_push_error:'系统推送尚未设置成功，请检查通知权限后重试。', chat_title: 'AI 助手', send: '发送', stop: '停止',
      chat_latest: '回到最新', new_session: '新对话', close: '关闭', settings: '页面设置', menu: '菜单', dialog: '对话框', page_actions: '页面操作', conversation: '与 AI 助手的对话',
      refresh_models: '刷新模型', model_placeholder: '选择已加载模型, 或直接输入模型名',
      agent_mode_label: '执行模式', agent_fast: 'Fast', agent_deep: 'Deep',
      agent_fast_hint: '低延迟单代理，适合直接问答和明确修改。',
      agent_deep_hint: '先规划并委派只读检查，适合复杂界面任务。',
      agent_deep_manual: '需确认的变更会暂停；确认只执行当前卡片，计划不会自动继续。',
      agent_plan: '计划', agent_plan_paused: '计划已暂停', agent_budget: '预算',
      agent_model_budget: '模型 {used}/{max}', agent_subtask_budget: '子任务 {used}/{max}', agent_tool_budget: '工具 {used}/{max}',
      agent_root: '主代理', agent_inspector: '页面检查', agent_reviewer: 'UI 验收',
      agent_status_pending: '待处理', agent_status_running: '进行中', agent_status_completed: '完成', agent_status_paused: '暂停', agent_status_error: '失败',
      agent_manual_confirm: '确认只执行此操作，不会自动继续其他操作；以实际结果为准。',
      agent_managed_confirm: '确认仅授权当前操作；完成后助手会继续处理原请求，后续变更会再次请求确认。',
      composer_placeholder: '输入问题…', composer_hint: 'Enter 发送 · Shift+Enter 换行',
      welcome: '你可以直接提问、分析业务数据，也可以明确要求创建或修改页面。回答会以合适的 HTML 结构展示在这里。',
      session_reset: '新会话已开始。之前的对话上下文已清空, 页面 Item 不受影响。',
      chat_background_running: '连接暂时断开，后台仍在继续；恢复后会接回原进度。', chat_background_stopping: '正在发送停止请求…', chat_background_stop_unsent: '停止请求未送达，后台可能仍在执行；请恢复网络后重试停止。', chat_reconnected: '连接已恢复，未重复提交原请求或操作。', chat_disconnected: '连接已断开，网络可用时将自动重连。', chat_connection_incompatible: '连接协议或响应异常，自动重连已停止，请刷新页面后核对原任务。', chat_connection_denied: '连接授权已失效，请重新登录后继续。', chat_turn_interrupted: '连接中断，本轮尚未完成；恢复连接后请继续核对原任务。',
      process: '处理记录', analyzing: '正在处理', thinking: '模型思考', done: '已完成', turn_stopped: '已请求停止', turn_failed: '处理失败',
      turn_stop_unknown: '已请求停止当前轮次。已开始的操作可能仍在收尾，请核对最终结果。',
      tool_running: '执行中', tool_pending: '待确认', tool_success: '完成', tool_accepted: '已接受', tool_error: '失败', tool_unknown: '结果未知', tools_unknown: '{n} 个操作待核对', execution_unknown: '未收到执行结果，操作可能已在服务端生效。请勿重复执行，先核对操作记录。', tool_cancelled: '已取消', tool_cancelling: '撤销中',
      risk_low: '低风险', risk_medium: '中风险', risk_high: '高风险', risk_blocked: '已拦截',
      confirm_run: '确认执行', cancel: '取消', confirmed_running: '已确认, 正在执行。', cancelled_not_run: '已撤销，操作未执行。',
      confirmation_connecting: '正在连接并提交确认…', cancellation_waiting: '正在向服务器撤销，等待确认…',
      confirmation_send_failed: '未能提交确认，操作尚未开始。', cancellation_send_failed: '未能发送撤销请求，操作仍待确认。',
      cancellation_ack_timeout: '未收到服务器撤销回执，执行状态未知。请重新连接后再确认。', cancellation_failed: '撤销失败，执行状态未确认。',
      confirm_expired: '确认卡已过期，本次未执行。请重新核对范围并生成新卡。', expires_in: '剩余', expires_at: '到期', refresh_confirmation: '刷新预览', refresh_confirmation_request: '请继续原请求：先核对原任务，刷新过期预览并生成新的确认卡；不要重复提交已执行或结果未知的操作。',
      params_title: '操作参数', debug_title: '调试详情', tools_called: '调用 {n} 个工具',
      process_steps: '处理过程', trace_completed: '本轮处理结束', trace_tool_count: '{n} 条工具记录', trace_elapsed: '{n}秒', steps_completed: '已完成 {n} 个步骤', tools_running: '正在执行 {n} 项', answering: '正在整理回答',
      reasoning_step: '分析摘要 {n}', reasoning_streaming: '正在分析', reasoning_complete: '分析完成', technical_details: '技术详情',
      request_summary: '请求摘要', result_data: '返回结果', diagnostic_data: '诊断信息', tool_reason: '为什么需要确认', review_field: '设置项', review_before: '修改前', review_after: '修改后', review_missing: '未设置', review_partial: '这里只展示部分变更，请核对完整操作内容。',
      confirm_this_action: '确认此操作', dismiss_tool: '暂不执行', answer_complete: '回答已完成',
      answer_complete_summary: '回答已完成：{summary}', answer_truncated: '回答过长，已仅展示前面部分。',
      answer_table: '数据表格', answer_code: '代码内容', opens_new_window: '在新窗口打开',
      confirmation_announced: '{name} 待确认，{risk}', tool_confirmed_announced: '已确认 {name}，正在执行', tool_dismissed_announced: '已暂不执行 {name}',
      confirmation_expired_announced: '{name} 的确认已过期', content_chars: '包含 {n} 个字符的内容', content_truncated: '内容过长，已截断。',
      confirmation_busy: '请先等待当前请求完成，再处理之前的确认卡。',
      tool_result_announced: '{name}：{status}。{summary}',
      tool_title_item_list: '查看页面模块', tool_title_item_get: '读取模块源码', tool_title_item_put: '创建或更新模块',
      tool_title_item_delete: '删除模块', tool_title_items_replace: '整体重建页面', tool_title_theme_get: '查看主题', tool_title_theme_set: '调整主题',
      tool_title_kv_get: '读取持久数据', tool_title_kv_set: '写入持久数据', tool_title_page_list: '查看页面列表', tool_title_page_set: '创建或更新页面',
      tool_title_page_delete: '删除页面', tool_title_api_probe: '检查业务接口', tool_title_revision_list: '查看历史快照', tool_title_revision_rollback: '回滚到快照',
      tool_title_page_errors: '查看页面报错', tool_title_template_get: '读取当前模板结构', tool_title_template_put: '保存当前模板草稿',
      tool_title_template_publish: '发布模板', tool_title_template_slot_delete: '删除模板区域', tool_title_unknown: '工具操作',
      tool_action_item_list: '查看当前页面模块', tool_action_item_get: '读取模块 {id}', tool_action_item_put: '创建或更新 {n} 个界面模块',
      tool_action_item_delete: '删除 {n} 个界面模块', tool_action_items_replace: '用 {n} 个模块整体重建页面', tool_action_theme_get: '读取页面主题',
      tool_action_theme_set: '更新 {n} 项主题设置', tool_action_kv_get: '读取数据 {key}', tool_action_kv_set: '写入数据 {key}',
      tool_action_page_list: '查看页面列表', tool_action_page_set: '创建或更新页面 {name}', tool_action_page_delete: '删除页面 {name}',
      tool_action_api_probe: '检查接口 {path}', tool_action_revision_list: '查看历史快照', tool_action_revision_rollback: '回滚到快照 {id}',
      tool_action_page_errors: '检查页面运行错误', tool_action_template_get: '读取 {page} 模板结构', tool_action_template_put: '保存 {page} 模板草稿',
      tool_action_template_publish: '发布模板 {id}', tool_action_template_slot_delete: '删除模板区域 {id}', tool_action_unknown: '执行 {name}',
      tool_fact_items: '模块数', tool_fact_item_id: '模块 ID', tool_fact_item_ids: '模块 ID', tool_fact_page: '页面', tool_fact_pages: '页面', tool_fact_scope: '作用范围',
      tool_fact_note: '变更说明', tool_fact_fields: '更改项', tool_fact_key: '数据键', tool_fact_route: '路由', tool_fact_title: '显示名称', tool_fact_path: '接口路径',
      tool_fact_query: '查询条件', tool_fact_revision: '版本', tool_fact_slot: '区域', tool_fact_limit: '数量上限', tool_fact_type: '类型', tool_fact_operation: '操作',
      tool_fact_content: '完整内容', tool_fact_value: '数据概要', tool_fact_template: '模板 ID',
      tool_scope_global: '全局', tool_scope_current: '当前页面', tool_page_root: '根页面', tool_yes: '是', tool_no: '否',
      tool_omitted_chars: '{n} 个字符，已折叠', tool_omitted_structure: '结构数据（约 {n} 个字符），已折叠', tool_omitted_entries: '{n} 项数据，已折叠',
      tool_redacted: '[已隐藏]', tool_more_entries: '…还有 {n} 项', tool_technical_truncated: '…（技术详情已截断）',
      tools_failed: '{n} 个工具失败', tools_pending: '{n} 个操作待确认', running_tool: '正在执行 {name}',
      empty_title: '这里还是一张白纸', empty_chat_hint: '告诉 AI 助手你想要什么界面, 它会当场为你搭出来, 并且一直记得。',
      empty_start: '开始对话', empty_api_hint: '还没有任何 UI Item。通过 API 写入第一组片段, 页面会立即呈现:',
      load_failed: '界面加载失败', retry: '重试', page_load_failed: '页面加载失败',
      auth_expired: '登录状态已过期, 请刷新页面或重新登录。', auth_missing: '缺少用户标识',
      auth_missing_hint: '服务端没有配置可信的用户认证。生产环境必须通过 WithUserResolver 对接已验证的会话或令牌。',
      offline: '实时更新已断开, 重连中…', readonly_note: '当前为只读访问', readonly_short: '只读',
      readonly_empty: '当前为只读访问，暂无可显示的内容。', previous_page: '上一页', next_page: '下一页',
      panel_title: '页面设置', panel_theme: '主题', panel_history: '历史', panel_errors: '错误',
      theme_mode: '明暗', theme_auto: '跟随系统', theme_light: '亮色', theme_dark: '暗色',
      theme_primary: '主色', theme_radius: '圆角', theme_color: '主题色 {color}',
      history_empty: '暂无历史快照。', rollback: '回滚到此', rollback_confirm_title: '回滚确认',
      rollback_confirm: '将恢复到该快照(当前状态也会先存为快照)。', rolled_back: '已回滚',
      errors_empty: '暂无页面错误。', items_label: '{n} 个 Item',
      safe_title: '安全模式', safe_hint: '未执行任何 UI Item。可以回滚到某个历史快照, 或让 AI 助手修复。',
      safe_readonly_hint: '未执行任何 UI Item。当前账号没有修改权限，请联系有权限的管理员处理。',
      safe_exit: '退出安全模式', ai_unavailable: 'AI 服务未配置, 请联系管理员。',
      ai_preview: '演示预览：当前未连接真实 AI 模型，不会执行真实问答或页面修改。',
      model_needed: '请先选择或输入模型名', chat_broken: 'AI 连接失败', ws_lost: '连接已断开, 请重新发送',
      item_broken: 'Item "{id}" 执行失败: {msg}', loading: '加载中…', author_user: '你',
      panel_layout: '布局', layout_edit: '开启拖拽排版', layout_editing: '正在编辑布局',
      layout_done: '完成排版', layout_reset: '重置本页布局', layout_hint: '拖动顶部手柄换位置，拉右下角调宽高；也可聚焦手柄后用方向键调整，Home 恢复自动高度。',
      layout_mobile: '手机上不支持编辑布局', layout_saved: '布局已保存', layout_move: '移动 {title}', layout_resize: '调整 {title} 大小', layout_resize_short: '大小',
      layout_resize_width: '调整 {title} 宽度', layout_resize_width_short: '宽度',
      item_editor_title: '内容排版', item_editor_layout_mode: '排版模式', item_editor_auto: '自动排版', item_editor_free: '自由定位',
      item_editor_auto_hint: '拖动内容调整顺序，拉动边角调整大小。', item_editor_free_hint: '自由移动、缩放内容，并调整层级。',
      item_editor_save: '保存', item_editor_reload: '重新加载', item_editor_saving: '保存中…', item_editor_saved: '已保存', item_editor_done: '完成',
      item_editor_unsaved: '有未保存修改', item_editor_clean: '修改已保存', item_editor_conflict: '布局已在其他位置更新，请放弃本地修改并重新加载。', item_editor_load_failed: '读取布局失败', item_editor_save_failed: '保存失败',
      item_editor_current_items: '页面内容', item_editor_canvas: '编辑画布', item_editor_properties: '属性', item_editor_selected: '所选内容',
      item_editor_select_hint: '选择画布中的内容后编辑属性。', item_editor_no_selection: '请选择内容', item_editor_empty: '当前页面没有可编辑内容',
      item_editor_move: '拖动', item_editor_layer: '层级', item_editor_layer_up: '上移一层', item_editor_layer_down: '下移一层',
      item_editor_layout: '大小与位置', item_editor_span: '所占列数', item_editor_grid_span: '所占列数', item_editor_auto_height: '自动高度',
      item_editor_position: '位置', item_editor_size: '大小', item_editor_x: 'X', item_editor_y: 'Y', item_editor_width: '宽', item_editor_height: '高', item_editor_z: '层级', item_editor_z_index: '层级',
      item_editor_appearance: '外观', item_editor_background: '背景色', item_editor_background_color: '背景色', item_editor_color: '文字色', item_editor_text_color: '文字色',
      item_editor_radius: '圆角', item_editor_opacity: '透明度', item_editor_reset_size: '恢复默认大小', item_editor_reset_appearance: '恢复默认外观',
      item_editor_global: '全局内容', item_editor_page_item: '当前页内容',
      item_editor_discard: '放弃修改', item_editor_exit_hint: '退出会丢失尚未保存的排版和外观修改。',
      item_editor_delete_title: '删除内容', item_editor_delete_hint: '删除后无法撤销，确定删除这项内容吗？',
      item_editor_delete_global: '这是全局内容，删除后所有页面都会移除。确定删除吗？', item_editor_delete: '删除', item_editor_deleted: '已删除', item_editor_delete_failed: '删除失败',
      item_editor_style: '颜色', item_editor_clear: '清除', item_editor_color_placeholder: '#RRGGBB / rgb() / var()', item_editor_invalid_color: '请输入有效的纯色值',
      item_editor_columns: '列', item_editor_span_value: '宽 {value} 列', item_editor_main_area: '主内容区', item_editor_mobile_drag: '窄屏下请使用右侧数值精确调整',
      item_editor_snap: '吸附 {value}px', item_editor_drop_here: '放到这里', item_editor_drop_position: '放置位置 {value}/{total}',
      item_editor_content_min_span: '内容至少需要 {value} 列',
      item_editor_panel_hint: '直接调整当前页面已有内容的位置、大小、颜色和层级。', item_editor_edit_items: '编辑当前页面内容',
      item_editor_advanced_template: '高级：编辑空模板结构', item_editor_template_hint: '模板只定义空的容器和区域，不会预填或绑定页面内容。',
      template_edit: '编辑模板', template_title: '模板结构工作室', template_layers: '结构', template_properties: '属性',
      template_studio: '结构工作室', template_compare: '断点对比', template_preview_off: '内容预览已关闭',
      template_root: 'Root', template_container: 'Container', template_slot: 'Slot', template_add_container: '添加容器', template_add_slot: '添加插槽',
      template_empty_title: '空模板', template_empty_hint: '从 Container 或 Slot 开始搭建结构。业务内容不会出现在模板编辑器中。',
      template_mode: '排版', template_flow: '流式', template_grid: '网格', template_free: '自由', template_columns: '列数',
      template_direction: '方向', template_column: '纵向', template_row: '横向', template_wrap: '允许换行', template_align: '对齐',
      template_gap: '间距', template_padding: '内边距', template_width: '宽', template_height: '高', template_x: 'X', template_y: 'Y',
      template_name: '名称', template_delete: '删除', template_duplicate: '复制', template_undo: '撤销', template_redo: '重做',
      template_save: '保存草稿', template_saving: '保存中…', template_saved: '草稿已保存', template_unsaved: '有未保存修改',
      template_publish: '发布', template_publishing: '发布中…', template_unpublished: '未发布', template_published: '已发布 v{n}', template_draft_newer: '草稿有新改动', template_publish_failed: '发布失败',
      template_exit: '退出编辑', template_delete_title: '删除结构', template_delete_hint: '将同时删除其全部子层。此操作可以撤销。', template_exit_hint: '退出后将丢失尚未保存的模板修改。',
      template_desktop: '桌面', template_tablet: '平板', template_mobile: '手机', template_inherited: '继承',
      template_canvas: '模板画布', template_drop_here: '拖到这里', template_locked_root: 'Root 不能删除或移动',
      template_reorder_actions: '调整 {title} 的层级', template_move_up: '上移', template_move_down: '下移', template_indent: '缩进', template_outdent: '减少缩进',
      template_load_failed: '模板加载失败，请重试；远端模板未被覆盖', template_save_failed: '模板保存失败', template_min_height: '最小高度',
      template_conflict: '模板已在其他位置更新', template_reload: '载入远端版本',
      template_unassigned: '未放置内容', template_unassigned_empty: '没有未放置内容',
    },
    en: {
      history_title: 'History', history_back: 'Back to chat', history_loading: 'Loading…', chat_history_empty: 'No saved conversations', history_more: 'Earlier conversations', history_retry: 'Reload', history_delete: 'Delete conversation', history_error: 'History request did not complete. Check the original record.', history_unknown: 'Deletion is unconfirmed. Check the original record; do not resubmit.',
      chat_entry: 'AI assistant', notifications_push_enable:'Enable system notifications', notifications_push_disable:'Disable system notifications', notifications_push_unsupported:'System notifications unavailable; on iPhone use the installed Home Screen app.', notifications_push_error:'Push setup failed. Check notification permission and retry.', chat_title: 'AI assistant', send: 'Send', stop: 'Stop',
      chat_latest: 'Jump to latest', new_session: 'New chat', close: 'Close', settings: 'Page settings', menu: 'Menu', dialog: 'Dialog', page_actions: 'Page actions', conversation: 'Conversation with the AI assistant',
      refresh_models: 'Refresh', model_placeholder: 'Pick a loaded model or type a name',
      agent_mode_label: 'Execution mode', agent_fast: 'Fast', agent_deep: 'Deep',
      agent_fast_hint: 'Low-latency single agent for direct questions and well-scoped changes.',
      agent_deep_hint: 'Plans first and delegates read-only checks for complex interface work.',
      agent_deep_manual: 'Changes requiring confirmation pause the plan. Confirming runs only that card and does not resume the plan.',
      agent_plan: 'Plan', agent_plan_paused: 'Plan paused', agent_budget: 'Budget',
      agent_model_budget: 'Model {used}/{max}', agent_subtask_budget: 'Subtasks {used}/{max}', agent_tool_budget: 'Tools {used}/{max}',
      agent_root: 'Main agent', agent_inspector: 'Page inspector', agent_reviewer: 'UI reviewer',
      agent_status_pending: 'Pending', agent_status_running: 'Running', agent_status_completed: 'Done', agent_status_paused: 'Paused', agent_status_error: 'Failed',
      agent_manual_confirm: 'Confirmation runs only this operation. Other operations do not resume automatically; verify the actual result.',
      agent_managed_confirm: 'Confirmation authorizes only this operation. The assistant then continues your request and asks again before further changes.',
      composer_placeholder: 'Ask a question…', composer_hint: 'Enter to send · Shift+Enter for a new line',
      welcome: 'Ask a question, analyze business data, or explicitly ask me to create or change the page. Answers appear here using an appropriate HTML structure.',
      session_reset: 'New session started. Conversation context cleared; page items are unaffected.',
      chat_background_running: 'Connection lost; work continues in the background and progress will resume.', chat_background_stopping: 'Sending stop request…', chat_background_stop_unsent: 'Stop request was not delivered; work may still be running. Reconnect and try stopping again.', chat_reconnected: 'Connection restored; no request or operation was resubmitted.', chat_disconnected: 'Disconnected. Reconnecting automatically when the network is available.', chat_connection_incompatible: 'Connection protocol or response error. Automatic reconnect stopped; refresh and verify the original task.', chat_connection_denied: 'Connection authorization expired. Sign in again to continue.', chat_turn_interrupted: 'Connection interrupted; this turn is incomplete. Verify the original task after reconnecting.',
      process: 'Work log', analyzing: 'Working', thinking: 'Model reasoning', done: 'Done', turn_stopped: 'Stop requested', turn_failed: 'Failed',
      turn_stop_unknown: 'Stop was requested. An operation that already started may still be finishing; verify its final result.',
      tool_running: 'Running', tool_pending: 'Confirm', tool_success: 'Done', tool_accepted: 'Accepted', tool_error: 'Failed', tool_unknown: 'Outcome unknown', tools_unknown: '{n} operations need verification', execution_unknown: 'No execution result was received. The operation may have committed. Do not repeat it; check the records first.', tool_cancelled: 'Cancelled', tool_cancelling: 'Cancelling',
      risk_low: 'low risk', risk_medium: 'medium risk', risk_high: 'high risk', risk_blocked: 'blocked',
      confirm_run: 'Confirm', cancel: 'Cancel', confirmed_running: 'Confirmed, running.', cancelled_not_run: 'Revoked; the operation was not run.',
      confirmation_connecting: 'Connecting and submitting confirmation…', cancellation_waiting: 'Revoking on the server; waiting for confirmation…',
      confirmation_send_failed: 'Confirmation could not be submitted; the operation was not started.', cancellation_send_failed: 'The cancellation request could not be sent; the operation is still awaiting confirmation.',
      cancellation_ack_timeout: 'No cancellation acknowledgement was received; execution status is unknown. Reconnect before taking further action.', cancellation_failed: 'Cancellation failed; execution status is not confirmed.',
      confirm_expired: 'Confirmation expired; nothing was executed. Review the scope and request a new card.', expires_in: 'Time left', expires_at: 'Expires', refresh_confirmation: 'Refresh preview', refresh_confirmation_request: 'Continue the original request: verify the original task, refresh the expired preview and issue a new confirmation card. Do not repeat completed or unknown operations.',
      params_title: 'Parameters', debug_title: 'Debug', tools_called: '{n} tool calls',
      process_steps: 'Process', trace_completed: 'Turn finished', trace_tool_count: '{n} tool records', trace_elapsed: '{n}s', steps_completed: '{n} steps completed', tools_running: '{n} running', answering: 'Preparing the answer',
      reasoning_step: 'Reasoning step {n}', reasoning_streaming: 'Analyzing', reasoning_complete: 'Analysis complete', technical_details: 'Technical details',
      request_summary: 'Request summary', result_data: 'Result', diagnostic_data: 'Diagnostics', tool_reason: 'Why confirmation is required', review_field: 'Setting', review_before: 'Before', review_after: 'After', review_missing: 'Not set', review_partial: 'Only part of the changes is shown. Review the complete operation.',
      confirm_this_action: 'Confirm this action', dismiss_tool: 'Not now', answer_complete: 'Answer complete',
      answer_complete_summary: 'Answer complete: {summary}', answer_truncated: 'This answer was too long, so only the first part is shown.',
      answer_table: 'Data table', answer_code: 'Code content', opens_new_window: 'opens in a new window',
      confirmation_announced: '{name} awaits confirmation, {risk}', tool_confirmed_announced: 'Confirmed {name}; running now', tool_dismissed_announced: 'Skipped {name}',
      confirmation_expired_announced: 'Confirmation for {name} expired', content_chars: 'Contains {n} characters of content', content_truncated: 'Long content was truncated.',
      confirmation_busy: 'Wait for the current request to finish before using an earlier confirmation card.',
      tool_result_announced: '{name}: {status}. {summary}',
      tool_title_item_list: 'View page modules', tool_title_item_get: 'Read module source', tool_title_item_put: 'Create or update modules',
      tool_title_item_delete: 'Delete modules', tool_title_items_replace: 'Rebuild the page', tool_title_theme_get: 'View theme', tool_title_theme_set: 'Adjust theme',
      tool_title_kv_get: 'Read persistent data', tool_title_kv_set: 'Write persistent data', tool_title_page_list: 'View pages', tool_title_page_set: 'Create or update a page',
      tool_title_page_delete: 'Delete page', tool_title_api_probe: 'Check business API', tool_title_revision_list: 'View history snapshots', tool_title_revision_rollback: 'Roll back to snapshot',
      tool_title_page_errors: 'View page errors', tool_title_template_get: 'Read current template structure', tool_title_template_put: 'Save current template draft',
      tool_title_template_publish: 'Publish template', tool_title_template_slot_delete: 'Delete template region', tool_title_unknown: 'Tool operation',
      tool_action_item_list: 'View the current page modules', tool_action_item_get: 'Read module {id}', tool_action_item_put: 'Create or update {n} interface modules',
      tool_action_item_delete: 'Delete {n} interface modules', tool_action_items_replace: 'Rebuild the page with {n} modules', tool_action_theme_get: 'Read the page theme',
      tool_action_theme_set: 'Update {n} theme settings', tool_action_kv_get: 'Read data {key}', tool_action_kv_set: 'Write data {key}',
      tool_action_page_list: 'View the page list', tool_action_page_set: 'Create or update page {name}', tool_action_page_delete: 'Delete page {name}',
      tool_action_api_probe: 'Check API {path}', tool_action_revision_list: 'View history snapshots', tool_action_revision_rollback: 'Roll back to snapshot {id}',
      tool_action_page_errors: 'Check page runtime errors', tool_action_template_get: 'Read the {page} template structure', tool_action_template_put: 'Save the {page} template draft',
      tool_action_template_publish: 'Publish template {id}', tool_action_template_slot_delete: 'Delete template region {id}', tool_action_unknown: 'Run {name}',
      tool_fact_items: 'Modules', tool_fact_item_id: 'Module ID', tool_fact_item_ids: 'Module IDs', tool_fact_page: 'Page', tool_fact_pages: 'Pages', tool_fact_scope: 'Scope',
      tool_fact_note: 'Change note', tool_fact_fields: 'Changes', tool_fact_key: 'Data key', tool_fact_route: 'Route', tool_fact_title: 'Display name', tool_fact_path: 'API path',
      tool_fact_query: 'Query', tool_fact_revision: 'Revision', tool_fact_slot: 'Region', tool_fact_limit: 'Limit', tool_fact_type: 'Type', tool_fact_operation: 'Operation',
      tool_fact_content: 'Full content', tool_fact_value: 'Value summary', tool_fact_template: 'Template ID',
      tool_scope_global: 'Global', tool_scope_current: 'Current page', tool_page_root: 'root page', tool_yes: 'Yes', tool_no: 'No',
      tool_omitted_chars: '{n} characters, collapsed', tool_omitted_structure: 'Structure data (about {n} characters), collapsed', tool_omitted_entries: '{n} entries, collapsed',
      tool_redacted: '[hidden]', tool_more_entries: '…{n} more', tool_technical_truncated: '…(technical details truncated)',
      tools_failed: '{n} tools failed', tools_pending: '{n} pending confirmations', running_tool: 'Running {name}',
      empty_title: 'A blank canvas', empty_chat_hint: 'Tell the AI assistant what you want — it builds it live and remembers.',
      empty_start: 'Start chatting', empty_api_hint: 'No UI items yet. Put the first items via API:',
      load_failed: 'Failed to load', retry: 'Retry', page_load_failed: 'Failed to load page',
      auth_expired: 'Session expired. Refresh the page or sign in again.', auth_missing: 'Missing user identity',
      auth_missing_hint: 'The server has no trusted user authentication configured. Production hosts must connect WithUserResolver to a verified session or token.',
      offline: 'Live updates disconnected, retrying…', readonly_note: 'Read-only access', readonly_short: 'Read only',
      readonly_empty: 'This view is read-only and has no content to display.', previous_page: 'Previous page', next_page: 'Next page',
      panel_title: 'Page settings', panel_theme: 'Theme', panel_history: 'History', panel_errors: 'Errors',
      theme_mode: 'Mode', theme_auto: 'System', theme_light: 'Light', theme_dark: 'Dark',
      theme_primary: 'Primary', theme_radius: 'Radius', theme_color: 'Theme color {color}',
      history_empty: 'No snapshots yet.', rollback: 'Roll back', rollback_confirm_title: 'Confirm rollback',
      rollback_confirm: 'Restore this snapshot (current state is snapshotted first).', rolled_back: 'Rolled back',
      errors_empty: 'No page errors.', items_label: '{n} items',
      safe_title: 'Safe mode', safe_hint: 'No UI items were executed. Roll back to a snapshot or let the AI fix things.',
      safe_readonly_hint: 'No UI items were executed. This account cannot make changes; contact an administrator with write access.',
      safe_exit: 'Exit safe mode', ai_unavailable: 'AI service is not configured. Contact the administrator.',
      ai_preview: 'Demo preview: no real AI model is connected, so answers and page changes are not executed.',
      model_needed: 'Pick or type a model name first', chat_broken: 'AI connection failed', ws_lost: 'Connection lost, please resend',
      item_broken: 'Item "{id}" failed: {msg}', loading: 'Loading…', author_user: 'You',
      panel_layout: 'Layout', layout_edit: 'Enable drag layout', layout_editing: 'Editing layout',
      layout_done: 'Done', layout_reset: 'Reset page layout', layout_hint: 'Drag the top handle to reorder and the corner to resize. You can also focus a handle and use arrow keys; Home restores automatic height.',
      layout_mobile: 'Layout editing is unavailable on mobile', layout_saved: 'Layout saved', layout_move: 'Move {title}', layout_resize: 'Resize {title}', layout_resize_short: 'Size',
      layout_resize_width: 'Resize {title} width', layout_resize_width_short: 'Width',
      item_editor_title: 'Content layout', item_editor_layout_mode: 'Layout mode', item_editor_auto: 'Auto layout', item_editor_free: 'Free position',
      item_editor_auto_hint: 'Drag content to reorder it, and drag a corner to resize it.', item_editor_free_hint: 'Move, resize, and layer content freely.',
      item_editor_save: 'Save', item_editor_reload: 'Reload', item_editor_saving: 'Saving…', item_editor_saved: 'Saved', item_editor_done: 'Done',
      item_editor_unsaved: 'Unsaved changes', item_editor_clean: 'Changes saved', item_editor_conflict: 'The layout changed elsewhere. Discard local changes to reload it.', item_editor_load_failed: 'Could not load layout', item_editor_save_failed: 'Save failed',
      item_editor_current_items: 'Page content', item_editor_canvas: 'Editing canvas', item_editor_properties: 'Properties', item_editor_selected: 'Selected content',
      item_editor_select_hint: 'Select content on the canvas to edit its properties.', item_editor_no_selection: 'Select some content', item_editor_empty: 'This page has no editable content',
      item_editor_move: 'Drag', item_editor_layer: 'Layer', item_editor_layer_up: 'Bring forward', item_editor_layer_down: 'Send backward',
      item_editor_layout: 'Size and position', item_editor_span: 'Column span', item_editor_grid_span: 'Column span', item_editor_auto_height: 'Auto height',
      item_editor_position: 'Position', item_editor_size: 'Size', item_editor_x: 'X', item_editor_y: 'Y', item_editor_width: 'Width', item_editor_height: 'Height', item_editor_z: 'Layer', item_editor_z_index: 'Layer',
      item_editor_appearance: 'Appearance', item_editor_background: 'Background', item_editor_background_color: 'Background', item_editor_color: 'Text color', item_editor_text_color: 'Text color',
      item_editor_radius: 'Corner radius', item_editor_opacity: 'Opacity', item_editor_reset_size: 'Reset size', item_editor_reset_appearance: 'Reset appearance',
      item_editor_global: 'Global content', item_editor_page_item: 'Current-page content',
      item_editor_discard: 'Discard changes', item_editor_exit_hint: 'Exiting discards unsaved layout and appearance changes.',
      item_editor_delete_title: 'Delete content', item_editor_delete_hint: 'This cannot be undone. Delete this content?',
      item_editor_delete_global: 'This is global content. Deleting it removes it from every page. Continue?', item_editor_delete: 'Delete', item_editor_deleted: 'Deleted', item_editor_delete_failed: 'Delete failed',
      item_editor_style: 'Colors', item_editor_clear: 'Clear', item_editor_color_placeholder: '#RRGGBB / rgb() / var()', item_editor_invalid_color: 'Enter a valid solid color',
      item_editor_columns: 'cols', item_editor_span_value: '{value} columns wide', item_editor_main_area: 'Main content', item_editor_mobile_drag: 'Use the numeric controls for precise adjustments on narrow screens',
      item_editor_snap: 'Snap {value}px', item_editor_drop_here: 'Drop here', item_editor_drop_position: 'Drop position {value}/{total}',
      item_editor_content_min_span: 'Content needs at least {value} columns',
      item_editor_panel_hint: 'Adjust the position, size, colors, and layering of content already on this page.', item_editor_edit_items: 'Edit current page content',
      item_editor_advanced_template: 'Advanced: edit empty template', item_editor_template_hint: 'Templates define empty containers and regions; they do not prefill or bind page content.',
      template_edit: 'Edit template', template_title: 'Template structure studio', template_layers: 'Structure', template_properties: 'Properties',
      template_studio: 'Structure studio', template_compare: 'Breakpoint comparison', template_preview_off: 'Content preview is off',
      template_root: 'Root', template_container: 'Container', template_slot: 'Slot', template_add_container: 'Add container', template_add_slot: 'Add slot',
      template_empty_title: 'Empty template', template_empty_hint: 'Start with a Container or Slot. Business content is never shown in the template editor.',
      template_mode: 'Layout', template_flow: 'Flow', template_grid: 'Grid', template_free: 'Free', template_columns: 'Columns',
      template_direction: 'Direction', template_column: 'Column', template_row: 'Row', template_wrap: 'Wrap', template_align: 'Align',
      template_gap: 'Gap', template_padding: 'Padding', template_width: 'Width', template_height: 'Height', template_x: 'X', template_y: 'Y',
      template_name: 'Name', template_delete: 'Delete', template_duplicate: 'Duplicate', template_undo: 'Undo', template_redo: 'Redo',
      template_save: 'Save draft', template_saving: 'Saving…', template_saved: 'Draft saved', template_unsaved: 'Unsaved changes',
      template_publish: 'Publish', template_publishing: 'Publishing…', template_unpublished: 'Unpublished', template_published: 'Published v{n}', template_draft_newer: 'Draft has newer changes', template_publish_failed: 'Publish failed',
      template_exit: 'Exit editor', template_delete_title: 'Delete structure', template_delete_hint: 'All nested layers will also be removed. You can undo this action.', template_exit_hint: 'Exiting will discard unsaved template changes.',
      template_desktop: 'Desktop', template_tablet: 'Tablet', template_mobile: 'Mobile', template_inherited: 'Inherited',
      template_canvas: 'Template canvas', template_drop_here: 'Drop here', template_locked_root: 'Root cannot be deleted or moved',
      template_reorder_actions: 'Reorder {title}', template_move_up: 'Move up', template_move_down: 'Move down', template_indent: 'Indent', template_outdent: 'Outdent',
      template_load_failed: 'Template failed to load. Retry; the remote template was not changed', template_save_failed: 'Failed to save template', template_min_height: 'Min height',
      template_conflict: 'Template changed elsewhere', template_reload: 'Load remote version',
      template_unassigned: 'Unassigned content', template_unassigned_empty: 'No unassigned content',
    },
  };

  function makeT(locale, overrides) {
    const lang = String(locale || '').toLowerCase().startsWith('en') ? 'en' : 'zh';
    const table = Object.assign({}, I18N[lang], overrides || {});
    return (key, vars) => {
      let s = table[key] != null ? table[key] : (I18N.zh[key] || key);
      if (vars) for (const k in vars) s = s.replace('{' + k + '}', vars[k]);
      return s;
    };
  }

  /* ---------- chatContent: 通用回答格式、Markdown 与传输预算 ----------
   * 这里只把 Markdown 转成 HTML 字符串，绝不把它视为可信 DOM。内核会把
   * Markdown 和显式 HTML 一并送入同一个 DOM 白名单清洗器。 */
  const chatContent = (() => {
    const KNOWN_FORMATS = new Set(['auto', 'html', 'markdown', 'text']);
    // Matches chat.Config's public maximum. The server default remains 256 KiB;
    // this browser ceiling merely avoids truncating a valid host override a
    // second time while retaining a non-disableable DOM/rendering boundary.
    const MAX_RENDER_BYTES = 512 * 1024;
    const HTML_TAGS = new Set(('article section header footer aside div span p br hr h1 h2 h3 h4 h5 h6 blockquote ' +
      'ul ol li dl dt dd table caption colgroup col thead tbody tfoot tr th td pre code strong em b i u s del ins a ' +
      'details summary figure figcaption time mark kbd samp var cite abbr q data progress meter').split(' '));
    const encoder = typeof TextEncoder === 'function' ? new TextEncoder() : null;
    const linkTextFilters = (() => {
      try {
        return {
          whitespace: new RegExp('\\p{White_Space}+', 'gu'),
          invisible: new RegExp('[\\p{Default_Ignorable_Code_Point}\\u2800]+', 'gu'),
          marks: new RegExp('\\p{Mark}+', 'gu'),
        };
      } catch {
        return {
          whitespace: /\s+/g,
          invisible: /[\u00ad\u034f\u061c\u115f\u1160\u17b4\u17b5\u180b-\u180f\u200b-\u200f\u202a-\u202e\u2060-\u206f\u2800\u3164\ufe00-\ufe0f\ufeff\uffa0]+/g,
          marks: /[\u0300-\u036f\u0483-\u0489\u0591-\u05bd\u05bf\u05c1-\u05c2\u0610-\u061a\u064b-\u065f\u0670]+/g,
        };
      }
    })();

    function escapeHTML(value) {
      return String(value == null ? '' : value)
        .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
    }

    function normalizeFormat(value) {
      const raw = String(value == null ? '' : value).trim().toLowerCase();
      if (!raw) return 'auto';
      return KNOWN_FORMATS.has(raw) ? raw : 'text';
    }

    // The first explicit stream format locks that projection. assistant_reset
    // starts a fresh rendering stream; assistant_done is handled separately.
    function mergeFormat(current, incoming, reset) {
      const base = reset ? 'auto' : normalizeFormat(current);
      if (base !== 'auto') return base;
      if (incoming == null || String(incoming).trim() === '') return base;
      return normalizeFormat(incoming);
    }

    // A non-empty assistant_done format describes its authoritative snapshot.
    // Unknown values deliberately normalize to text; omission preserves the
    // format established by streaming deltas or legacy auto detection.
    function finalizeFormat(current, incoming) {
      const base = normalizeFormat(current);
      if (incoming == null || String(incoming).trim() === '') return base;
      return normalizeFormat(incoming);
    }

    function doneSnapshot(event) {
      const present = !!event && Object.prototype.hasOwnProperty.call(event, 'message') && typeof event.message === 'string';
      return { present, message: present ? event.message : '' };
    }

    function stripInlineCode(line) {
      const source = String(line || '');
      let output = '';
      let cursor = 0;
      while (cursor < source.length) {
        const start = source.indexOf('`', cursor);
        if (start < 0) { output += source.slice(cursor); break; }
        output += source.slice(cursor, start);
        let end = start + 1;
        while (source[end] === '`') end++;
        const marker = source.slice(start, end);
        const close = source.indexOf(marker, end);
        if (close < 0) break;
        cursor = close + marker.length;
      }
      return output;
    }

    function withoutCode(source) {
      const lines = String(source || '').replace(/\r\n?/g, '\n').split('\n');
      let fence = '';
      return lines.map((line) => {
        const match = /^ {0,3}(`{3,}|~{3,})/.exec(line);
        if (match) {
          if (!fence) fence = match[1][0];
          else if (match[1][0] === fence) fence = '';
          return '';
        }
        if (fence) return '';
        return stripInlineCode(line);
      }).join('\n');
    }

    // The auto detector is deliberately a single forward scan. A regex ending in
    // `[^>]*>` turns many unterminated tags into quadratic work on model output.
    function looksLikeHTML(source) {
      const clean = withoutCode(source);
      let open = -1;
      for (let index = 0; index < clean.length; index++) {
        const char = clean.charCodeAt(index);
        if (char === 60) { open = index; continue; } // <
        if (char !== 62 || open < 0) continue; // >
        if (index - open <= 512) {
          const match = /^<\s*\/?\s*([a-z][a-z0-9]*)\b/i.exec(clean.slice(open, index + 1));
          if (match && HTML_TAGS.has(match[1].toLowerCase())) return true;
        }
        open = -1;
      }
      return false;
    }

    function resolveFormat(format, source) {
      const normalized = normalizeFormat(format);
      if (normalized !== 'auto') return normalized;
      return looksLikeHTML(source) ? 'html' : 'markdown';
    }

    function clipUTF8(value, maxBytes) {
      const text = String(value == null ? '' : value);
      const limit = Math.max(0, Math.floor(Number(maxBytes) || 0));
      if (!encoder) {
        const clipped = text.slice(0, limit);
        return { text: clipped, bytes: clipped.length, truncated: clipped.length < text.length };
      }
      const encoded = encoder.encode(text);
      if (encoded.length <= limit) return { text, bytes: encoded.length, truncated: false };
      let low = 0;
      let high = Math.min(text.length, limit);
      while (low < high) {
        const mid = Math.ceil((low + high) / 2);
        if (encoder.encode(text.slice(0, mid)).length <= limit) low = mid;
        else high = mid - 1;
      }
      if (low > 0 && /[\uD800-\uDBFF]/.test(text.charAt(low - 1))) low--;
      const clipped = text.slice(0, low);
      return { text: clipped, bytes: encoder.encode(clipped).length, truncated: true };
    }

    function compactLinkLabel(value, maxCodePoints = 320) {
      const raw = String(value == null ? '' : value);
      let normalized = raw;
      try { normalized = normalized.normalize('NFKC'); } catch { /* Older engines use the explicit fallback ranges. */ }
      let label = normalized.replace(linkTextFilters.invisible, '');
      label = label.replace(linkTextFilters.whitespace, ' ').trim();
      const visible = !!label.replace(linkTextFilters.marks, '').trim();
      const points = Array.from(label);
      const limit = Math.max(2, Math.min(1024, Math.floor(Number(maxCodePoints) || 320)));
      const truncated = points.length > limit;
      if (truncated) label = points.slice(0, limit - 1).join('') + '…';
      return { text: label, visible, truncated };
    }

    let tokenSequence = 0;
    function tokenStore(source) {
      const values = [];
      let nonce = (++tokenSequence).toString(36) + Math.random().toString(36).slice(2, 12);
      let prefix = '\uE000IHTML_' + nonce + '_';
      // Bounded collision handling avoids an attacker-controlled O(n²) search.
      for (let attempt = 0; attempt < 3 && source.includes(prefix); attempt++) {
        nonce = (++tokenSequence).toString(36) + Math.random().toString(36).slice(2, 12);
        prefix = '\uE000IHTML_' + nonce + '_';
      }
      const suffix = '\uE001';
      return {
        put(value) { const index = values.push(String(value)) - 1; return prefix + index + suffix; },
        restore(value) {
          const pattern = new RegExp(prefix + '(\\d+)' + suffix, 'g');
          return String(value).replace(pattern, (_, index) => values[Number(index)] || '');
        },
      };
    }

    function protectCodeSpans(source, tokens) {
      let output = '';
      let cursor = 0;
      while (cursor < source.length) {
        const start = source.indexOf('`', cursor);
        if (start < 0) { output += source.slice(cursor); break; }
        output += source.slice(cursor, start);
        let end = start + 1;
        while (source[end] === '`') end++;
        const marker = source.slice(start, end);
        const close = source.indexOf(marker, end);
        if (close < 0) { output += source.slice(start); break; }
        output += tokens.put('<code>' + escapeHTML(source.slice(end, close).trim()) + '</code>');
        cursor = close + marker.length;
      }
      return output;
    }

    function protectLinks(source, tokens, depth) {
      let output = '';
      let cursor = 0;
      while (cursor < source.length) {
        const open = source.indexOf('[', cursor);
        if (open < 0) { output += source.slice(cursor); break; }
        const image = open > cursor && source[open - 1] === '!';
        const start = image ? open - 1 : open;
        output += source.slice(cursor, start);
        const closeLabel = source.indexOf(']', open + 1);
        if (closeLabel < 0) { output += source.slice(start); break; }
        if (source[closeLabel + 1] !== '(') {
          output += source.slice(start, closeLabel + 1);
          cursor = closeLabel + 1;
          continue;
        }
        const closeTarget = source.indexOf(')', closeLabel + 2);
        if (closeTarget < 0) { output += source.slice(start); break; }
        const label = source.slice(open + 1, closeLabel);
        const target = source.slice(closeLabel + 2, closeTarget).trim();
        const href = target.split(/\s+/, 1)[0] || '';
        if (!label || !href || label.length > 4096 || href.length > 4096) {
          output += source.slice(start, closeTarget + 1);
        } else if (image) {
          // Remote images are deliberately not part of the answer surface; retain
          // useful alternative text without creating a tracking request.
          output += tokens.put(inlineMarkdown(label, depth + 1));
        } else {
          output += tokens.put('<a href="' + escapeHTML(href) + '">' + inlineMarkdown(label, depth + 1) + '</a>');
        }
        cursor = closeTarget + 1;
      }
      return output;
    }

    function inlineMarkdown(source, depth = 0) {
      let text = String(source == null ? '' : source);
      if (!text || depth > 4) return escapeHTML(text);
      const tokens = tokenStore(text);

      // Code and links use forward-only scanners. Unclosed openers turn the
      // remaining input into plain text instead of repeatedly rescanning it.
      text = protectCodeSpans(text, tokens);
      text = text.replace(/\\([\\`*{}\[\]()#+\-.!_>~|])/g, (_, value) => tokens.put(escapeHTML(value)));
      text = protectLinks(text, tokens, depth);
      text = text.replace(/<((?:https?:\/\/|mailto:)[^>\s]+)>/gi,
        (_, href) => tokens.put('<a href="' + escapeHTML(href) + '">' + escapeHTML(href) + '</a>'));

      text = escapeHTML(text)
        .replace(/~~([^~\n]+)~~/g, '<del>$1</del>')
        .replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>')
        .replace(/__([^_\n]+)__/g, '<strong>$1</strong>')
        .replace(/(^|[^\w])\*([^*\n]+)\*(?!\w)/g, '$1<em>$2</em>')
        .replace(/(^|[^\w])_([^_\n]+)_(?!\w)/g, '$1<em>$2</em>')
        .replace(/ {2,}\n/g, '<br>\n')
        .replace(/\n/g, ' ');
      return tokens.restore(text);
    }

    function splitTableRow(line) {
      let source = String(line || '').trim();
      if (source.startsWith('|')) source = source.slice(1);
      if (source.endsWith('|') && !source.endsWith('\\|')) source = source.slice(0, -1);
      const cells = [];
      let cell = '';
      let codeFence = 0;
      for (let index = 0; index < source.length; index++) {
        const ch = source[index];
        if (ch === '\\' && index + 1 < source.length) {
          const next = source[index + 1];
          if (next === '|' || next === '\\') { cell += next; index++; continue; }
        }
        if (ch === '`') {
          let end = index;
          while (source[end] === '`') end++;
          const width = end - index;
          codeFence = codeFence === width ? 0 : (codeFence || width);
          cell += source.slice(index, end);
          index = end - 1;
          continue;
        }
        if (ch === '|' && !codeFence) { cells.push(cell.trim()); cell = ''; continue; }
        cell += ch;
      }
      cells.push(cell.trim());
      return cells;
    }

    function tableDelimiter(line) {
      if (!String(line || '').includes('|')) return null;
      const cells = splitTableRow(line);
      if (!cells.length || cells.some((cell) => !/^:?-{3,}:?$/.test(cell.replace(/\s/g, '')))) return null;
      return cells.map((cell) => {
        const clean = cell.replace(/\s/g, '');
        return clean.startsWith(':') && clean.endsWith(':') ? 'center' : (clean.endsWith(':') ? 'right' : (clean.startsWith(':') ? 'left' : ''));
      });
    }

    function markdownToHTML(source, depth = 0) {
      const lines = String(source == null ? '' : source).replace(/\r\n?/g, '\n').split('\n');
      const output = [];
      let index = 0;
      const isFence = (line) => /^ {0,3}(?:`{3,}|~{3,})/.test(line || '');
      const isList = (line) => /^ {0,3}(?:[-+*]|\d+[.)])\s+\S/.test(line || '');
      const isHeading = (line) => /^ {0,3}#{1,6}\s+\S/.test(line || '');
      const isQuote = (line) => /^ {0,3}>/.test(line || '');
      const isRule = (line) => /^ {0,3}(?:(?:\*\s*){3,}|(?:-\s*){3,}|(?:_\s*){3,})$/.test(line || '');

      while (index < lines.length) {
        const line = lines[index];
        if (!line.trim()) { index++; continue; }

        const fence = /^ {0,3}(`{3,}|~{3,})\s*([^\s`]*)?.*$/.exec(line);
        if (fence) {
          const marker = fence[1];
          const language = /^[A-Za-z0-9_-]{1,40}$/.test(fence[2] || '') ? fence[2] : '';
          const body = [];
          index++;
          const closing = new RegExp('^ {0,3}' + (marker[0] === '`' ? '`' : '~') + '{' + marker.length + ',}\\s*$');
          while (index < lines.length && !closing.test(lines[index])) body.push(lines[index++]);
          if (index < lines.length) index++;
          output.push('<pre><code' + (language ? ' class="language-' + language + '"' : '') + '>' + escapeHTML(body.join('\n')) + '</code></pre>');
          continue;
        }

        const heading = /^ {0,3}(#{1,6})\s+(.+?)\s*#*\s*$/.exec(line);
        if (heading) {
          const level = heading[1].length;
          output.push('<h' + level + '>' + inlineMarkdown(heading[2]) + '</h' + level + '>');
          index++;
          continue;
        }
        if (index + 1 < lines.length && line.trim() && /^ {0,3}(?:=+|-+)\s*$/.test(lines[index + 1]) && !line.includes('|')) {
          const level = lines[index + 1].trim().startsWith('=') ? 1 : 2;
          output.push('<h' + level + '>' + inlineMarkdown(line.trim()) + '</h' + level + '>');
          index += 2;
          continue;
        }
        if (isRule(line)) { output.push('<hr>'); index++; continue; }

        if (isQuote(line)) {
          const quote = [];
          while (index < lines.length && isQuote(lines[index])) quote.push(lines[index++].replace(/^ {0,3}>\s?/, ''));
          output.push('<blockquote>' + (depth < 8 ? markdownToHTML(quote.join('\n'), depth + 1) : '<p>' + inlineMarkdown(quote.join(' ')) + '</p>') + '</blockquote>');
          continue;
        }

        const alignments = index + 1 < lines.length && line.includes('|') ? tableDelimiter(lines[index + 1]) : null;
        if (alignments) {
          const headers = splitTableRow(line);
          if (headers.length === alignments.length) {
            index += 2;
            const rows = [];
            while (index < lines.length && lines[index].trim() && lines[index].includes('|')) {
              const cells = splitTableRow(lines[index++]);
              while (cells.length < headers.length) cells.push('');
              rows.push(cells.slice(0, headers.length));
            }
            const style = (value) => value ? ' style="text-align: ' + value + '"' : '';
            output.push('<table><thead><tr>' + headers.map((cell, column) => '<th scope="col"' + style(alignments[column]) + '>' + inlineMarkdown(cell) + '</th>').join('') + '</tr></thead>' +
              (rows.length ? '<tbody>' + rows.map((row) => '<tr>' + row.map((cell, column) => '<td' + style(alignments[column]) + '>' + inlineMarkdown(cell) + '</td>').join('') + '</tr>').join('') + '</tbody>' : '') + '</table>');
            continue;
          }
        }

        const firstList = /^ {0,3}([-+*]|\d+[.)])\s+(.+)$/.exec(line);
        if (firstList) {
          const ordered = /^\d/.test(firstList[1]);
          const tag = ordered ? 'ol' : 'ul';
          const start = ordered ? Math.max(1, Number.parseInt(firstList[1], 10) || 1) : 1;
          const items = [];
          while (index < lines.length) {
            const item = /^ {0,3}([-+*]|\d+[.)])\s+(.+)$/.exec(lines[index]);
            if (!item || /^\d/.test(item[1]) !== ordered) break;
            const parts = [item[2]];
            index++;
            while (index < lines.length && /^ {2,}\S/.test(lines[index]) && !isList(lines[index])) parts.push(lines[index++].trim());
            items.push(parts.join(' '));
          }
          output.push('<' + tag + (ordered && start !== 1 ? ' start="' + start + '"' : '') + '>' + items.map((item) => '<li>' + inlineMarkdown(item) + '</li>').join('') + '</' + tag + '>');
          continue;
        }

        const paragraph = [line.trim()];
        index++;
        while (index < lines.length && lines[index].trim()) {
          if (isFence(lines[index]) || isHeading(lines[index]) || isList(lines[index]) || isQuote(lines[index]) || isRule(lines[index])) break;
          if (index + 1 < lines.length && lines[index].includes('|') && tableDelimiter(lines[index + 1])) break;
          paragraph.push(lines[index++].trim());
        }
        output.push('<p>' + inlineMarkdown(paragraph.join('\n')) + '</p>');
      }
      return output.join('\n');
    }

    return Object.freeze({ maxRenderBytes: MAX_RENDER_BYTES, escapeHTML, normalizeFormat, mergeFormat, finalizeFormat, doneSnapshot, looksLikeHTML, resolveFormat, clipUTF8, compactLinkLabel, inlineMarkdown, splitTableRow, markdownToHTML });
  })();

  /* ---------- chatAgent: Fast/Deep 能力选择与事件投影纯函数 ---------- */
  const chatAgent = (() => {
    const KNOWN_MODES = ['fast', 'deep'];
    const clone = (value) => JSON.parse(JSON.stringify(value));
    const count = (value) => {
      const parsed = Number(value);
      return Number.isFinite(parsed) ? Math.max(0, Math.floor(parsed)) : 0;
    };

    function normalizeModes(value) {
      const seen = new Set();
      for (const raw of Array.isArray(value) ? value : []) {
        const mode = String(raw || '').trim().toLowerCase();
        if (KNOWN_MODES.includes(mode)) seen.add(mode);
      }
      // Missing/empty legacy capabilities fall back to Fast; an explicit
      // supported list must not gain capabilities the server did not declare.
      return seen.size ? Array.from(seen) : ['fast'];
    }

    function selectMode(modes, preferred) {
      const available = normalizeModes(modes);
      const wanted = String(preferred || '').trim().toLowerCase();
      return available.includes(wanted) ? wanted : available[0];
    }

    function requestSnapshot(modes, preferred, model, modelSelect) {
      return {
        agentMode: selectMode(modes, preferred),
        model: modelSelect ? String(model || '').trim() : '',
      };
    }

    function cancelToolEvent(call) {
      return {
        type: 'cancel_tool',
        tool_call: call && typeof call === 'object' ? call : null,
      };
    }

    function cancelAckMatches(call, event) {
      const card = call && typeof call === 'object' ? call : {};
      const ack = event && typeof event === 'object' ? event : {};
      const cardID = String(card.id || '');
      const cardNonce = String(card.confirmation_nonce || '');
      const ackID = String(ack.tool_id || '');
      const ackNonce = String(ack.confirmation_nonce || '');
      // Every signed card has a unique nonce and the new acknowledgement
      // protocol always echoes it. A tool id alone is ambiguous across turns.
      if (!cardNonce || !ackNonce || cardNonce !== ackNonce) return false;
      // ID is optional for provider compatibility, but if either side supplies
      // one both sides must agree; never silently downgrade a two-part key.
      if (cardID || ackID) return !!cardID && !!ackID && cardID === ackID;
      return true;
    }

    // A normal turn stop only tells us that cancellation was requested. It
    // must never revoke a separate, signed confirmation card, and a tool that
    // already started may ignore context cancellation. Project that ambiguity
    // to an unknown card for writes, never claiming the effect was cancelled.
    function statusAfterTurnStop(status, mutating = false) {
      return String(status || '') === 'running' ? (mutating ? 'unknown' : 'error') : String(status || '');
    }

    // Once cancel_tool was put on the wire, losing the acknowledgement makes
    // execution state unknown. Callers may request terminal handling earlier,
    // but may never downgrade a sent request back to an actionable card.
    function cancellationFailureTerminal(cancelSent, requestedTerminal) {
      return !!cancelSent || !!requestedTerminal;
    }

    function streamRenderDelay(lastRenderedAt, now, interval) {
      const last = Number(lastRenderedAt) || 0;
      const current = Math.max(0, Number(now) || 0);
      const minimum = Math.max(16, Math.min(250, Number(interval) || 80));
      return Math.max(0, Math.min(minimum, minimum - Math.max(0, current - last)));
    }

    function initialState(mode) {
      return { mode: selectMode(['fast', 'deep'], mode), plan: [], agents: {}, budget: null, manualPaused: false };
    }

    function agentKey(agent) {
      if (!agent || typeof agent !== 'object') return '';
      if (String(agent.agent || '') === 'root') return 'root';
      return String(agent.task_id || (String(agent.agent || 'agent') + ':' + String(agent.phase || 'turn')));
    }

    // Deep 事件是完整状态快照而不是日志：Plan 整体替换，Agent 原位更新，
    // Budget 只保留最新一份。这避免长任务把助手布局不断撑高。
    function project(state, event) {
      const previous = state && typeof state === 'object' ? state : initialState('fast');
      const out = {
        mode: previous.mode === 'deep' ? 'deep' : 'fast',
        plan: Array.isArray(previous.plan) ? clone(previous.plan) : [],
        agents: previous.agents && typeof previous.agents === 'object' ? clone(previous.agents) : {},
        budget: previous.budget && typeof previous.budget === 'object' ? clone(previous.budget) : null,
        manualPaused: !!previous.manualPaused,
      };
      const ev = event && typeof event === 'object' ? event : {};
      if (ev.type === 'assistant_plan') {
        out.plan = (Array.isArray(ev.plan) ? ev.plan : []).slice(0, 12).map((item, index) => ({
          id: String(item && item.id || 'step-' + (index + 1)),
          content: String(item && item.content || '').slice(0, 400),
          status: ['pending', 'in_progress', 'completed'].includes(String(item && item.status)) ? String(item.status) : 'pending',
        })).filter((item) => item.content);
        out.mode = 'deep';
      }
      const agent = ev.agent && typeof ev.agent === 'object' ? ev.agent : null;
      if (agent) {
        if (String(agent.mode || '').toLowerCase() === 'deep') out.mode = 'deep';
        if (String(agent.phase || '') !== 'budget') {
          const key = agentKey(agent);
          if (key) out.agents[key] = {
            mode: String(agent.mode || out.mode), agent: String(agent.agent || 'root'),
            task_id: String(agent.task_id || ''), parent_task_id: String(agent.parent_task_id || ''),
            phase: String(agent.phase || ''), status: String(agent.status || 'running'),
            summary: String(agent.summary || '').slice(0, 500), approval_resume: String(agent.approval_resume || ''),
          };
          const keys = Object.keys(out.agents);
          while (keys.length > 32) {
            const oldest = keys.shift();
            if (oldest === 'root') keys.push(oldest);
            else delete out.agents[oldest];
          }
        }
        if (String(agent.approval_resume || '') === 'manual_tool_only' && String(agent.phase || '') === 'approval') out.manualPaused = true;
      }
      if (ev.budget && typeof ev.budget === 'object') {
        out.budget = {
          model_calls: count(ev.budget.model_calls), max_model_calls: count(ev.budget.max_model_calls),
          subtasks: count(ev.budget.subtasks), max_subtasks: count(ev.budget.max_subtasks),
          tool_calls: count(ev.budget.tool_calls), max_tool_calls: count(ev.budget.max_tool_calls),
          max_tool_calls_per_tool: count(ev.budget.max_tool_calls_per_tool),
          max_repeated_tool_calls: count(ev.budget.max_repeated_tool_calls),
          tool_budget_code: String(ev.budget.tool_budget_code || '').slice(0, 120),
          tool_budget_limit: String(ev.budget.tool_budget_limit || '').slice(0, 120),
          tool_budget_tool: String(ev.budget.tool_budget_tool || '').slice(0, 120),
          deadline_unix_ms: count(ev.budget.deadline_unix_ms),
        };
      }
      const call = ev.tool_call && typeof ev.tool_call === 'object' ? ev.tool_call : null;
      if (call && String(call.approval_resume || '') === 'manual_tool_only') {
        out.mode = 'deep';
        out.manualPaused = true;
      }
      return out;
    }

    function budgetParts(value) {
      const budget = value && typeof value === 'object' ? value : {};
      return {
        modelCalls: count(budget.model_calls), maxModelCalls: count(budget.max_model_calls),
        subtasks: count(budget.subtasks), maxSubtasks: count(budget.max_subtasks),
        toolCalls: count(budget.tool_calls), maxToolCalls: count(budget.max_tool_calls),
        maxToolCallsPerTool: count(budget.max_tool_calls_per_tool),
        maxRepeatedToolCalls: count(budget.max_repeated_tool_calls),
        toolBudgetCode: String(budget.tool_budget_code || ''),
        toolBudgetLimit: String(budget.tool_budget_limit || ''),
        toolBudgetTool: String(budget.tool_budget_tool || ''),
      };
    }

    return {
      normalizeModes, selectMode, requestSnapshot, cancelToolEvent, cancelAckMatches,
      statusAfterTurnStop, cancellationFailureTerminal, streamRenderDelay,
      initialState, project, budgetParts,
    };
  })();

  /* ---------- chatTrace: 将工具协议事件投影为可读、脱敏的界面数据 ---------- */
  const chatTrace = (() => {
    const TITLE_LIMIT = 120;
    const ACTION_LIMIT = 320;
    const SUMMARY_LIMIT = 32768;
    const FACT_LIMIT = 32768;
    const TECHNICAL_LIMIT = 5000;
    const COLLECTION_LIMIT = 24;
    const SENSITIVE_KEY = /(?:authorization|auth|token|secret|password|passwd|cookie|credential|apikey|session|sessionid|sessionkey|nonce|signature|privatekey)$/;
    const HEAVY_KEY = /^(?:content|html|css|js|javascript|code|script|template|value)$/i;
    const TOOL_META = {
      ui_item_list: ['tool_title_item_list', 'tool_action_item_list'],
      ui_item_get: ['tool_title_item_get', 'tool_action_item_get'],
      ui_item_put: ['tool_title_item_put', 'tool_action_item_put'],
      ui_item_delete: ['tool_title_item_delete', 'tool_action_item_delete'],
      ui_items_replace: ['tool_title_items_replace', 'tool_action_items_replace'],
      ui_theme_get: ['tool_title_theme_get', 'tool_action_theme_get'],
      ui_theme_set: ['tool_title_theme_set', 'tool_action_theme_set'],
      ui_kv_get: ['tool_title_kv_get', 'tool_action_kv_get'],
      ui_kv_set: ['tool_title_kv_set', 'tool_action_kv_set'],
      ui_page_list: ['tool_title_page_list', 'tool_action_page_list'],
      ui_page_set: ['tool_title_page_set', 'tool_action_page_set'],
      ui_page_delete: ['tool_title_page_delete', 'tool_action_page_delete'],
      api_probe: ['tool_title_api_probe', 'tool_action_api_probe'],
      ui_revision_list: ['tool_title_revision_list', 'tool_action_revision_list'],
      ui_revision_rollback: ['tool_title_revision_rollback', 'tool_action_revision_rollback'],
      ui_page_errors: ['tool_title_page_errors', 'tool_action_page_errors'],
      ui_template_get: ['tool_title_template_get', 'tool_action_template_get'],
      ui_template_put_current: ['tool_title_template_put', 'tool_action_template_put'],
      ui_template_publish: ['tool_title_template_publish', 'tool_action_template_publish'],
      ui_template_slot_delete: ['tool_title_template_slot_delete', 'tool_action_template_slot_delete'],
    };

    const own = (value, key) => Object.prototype.hasOwnProperty.call(value || {}, key);

    function isSensitiveKey(key) {
      const normalized = String(key == null ? '' : key).toLowerCase().replace(/[^a-z0-9]/g, '');
      return !!normalized && SENSITIVE_KEY.test(normalized);
    }

    function clip(value, limit) {
      const text = String(value == null ? '' : value);
      if (text.length <= limit) return text;
      return text.slice(0, Math.max(0, limit - 1)).trimEnd() + '…';
    }

    // 技术字符串也可能来自旧协议的 tool_input；即使无法解析 JSON，也不回显凭据。
    function redactText(value) {
      return String(value == null ? '' : value)
        .replace(/\bBearer\s+[^\s,;]+/gi, 'Bearer [redacted]')
        .replace(/((?:authorization|confirmation[_-]?token|access[_-]?token|refresh[_-]?token|auth[_-]?token|session[_-]?(?:id|key|token)|private[_-]?key|client[_-]?secret|api[_-]?key|password|secret|cookie)\s*["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;&}]+)/gi, '$1[redacted]');
    }

    function safeText(value, limit) {
      return clip(redactText(value).replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f]/g, ''), limit);
    }

    function parseMaybeJSON(value) {
      if (typeof value !== 'string') return value;
      const trimmed = value.trim();
      if (!trimmed || !['{', '[', '"'].includes(trimmed[0])) return value;
      try { return JSON.parse(trimmed); } catch (_) { return value; }
    }

    function measuredLength(value) {
      try { return JSON.stringify(value).length; } catch (_) { return String(value == null ? '' : value).length; }
    }

    function heavySummary(value, key, t) {
      const length = measuredLength(value);
      if (typeof value === 'string') return t('tool_omitted_chars', { n: value.length });
      if (/^template$/i.test(key)) return t('tool_omitted_structure', { n: length });
      const count = Array.isArray(value) ? value.length : (value && typeof value === 'object' ? Object.keys(value).length : 1);
      return t('tool_omitted_entries', { n: count });
    }

    function sanitizeValue(value, key, t, depth, seen) {
      if (isSensitiveKey(key)) return undefined;
      if (value == null || typeof value === 'boolean') return value;
      if (typeof value === 'number') return Number.isFinite(value) ? value : String(value);
      if (typeof value === 'bigint') return String(value);
      if (typeof value === 'string') {
        if (HEAVY_KEY.test(String(key || '')) && value.length > 160) return heavySummary(value, key, t);
        return safeText(value, 800);
      }
      if (typeof value !== 'object') return safeText(value, 200);
      if (HEAVY_KEY.test(String(key || '')) && measuredLength(value) > 240) return heavySummary(value, key, t);
      if (depth >= 6) return t('tool_omitted_structure', { n: measuredLength(value) });
      if (seen.has(value)) return '[circular]';
      seen.add(value);
      if (Array.isArray(value)) {
        const output = value.slice(0, COLLECTION_LIMIT).map((entry) => sanitizeValue(entry, '', t, depth + 1, seen));
        if (value.length > COLLECTION_LIMIT) output.push(t('tool_more_entries', { n: value.length - COLLECTION_LIMIT }));
        seen.delete(value);
        return output;
      }
      const output = {};
      const entries = Object.entries(value);
      let used = 0;
      for (const [rawKey, entry] of entries) {
        if (used >= COLLECTION_LIMIT || isSensitiveKey(rawKey)) continue;
        const cleanKey = safeText(rawKey, 80);
        const cleanValue = sanitizeValue(entry, rawKey, t, depth + 1, seen);
        if (cleanKey && cleanValue !== undefined) {
          output[cleanKey] = cleanValue;
          used++;
        }
      }
      if (entries.length > used) output['…'] = t('tool_more_entries', { n: entries.length - used });
      seen.delete(value);
      return output;
    }

    function technicalText(value, t) {
      if (value == null || value === '') return '';
      const parsed = parseMaybeJSON(value);
      const sanitized = sanitizeValue(parsed, '', t, 0, new WeakSet());
      if (sanitized === undefined) return '';
      let output;
      if (typeof sanitized === 'string') output = sanitized;
      else {
        try { output = JSON.stringify(sanitized, null, 2); } catch (_) { output = safeText(sanitized, TECHNICAL_LIMIT); }
      }
      output = safeText(output, TECHNICAL_LIMIT);
      if (output.length >= TECHNICAL_LIMIT) output = clip(output, TECHNICAL_LIMIT - t('tool_technical_truncated').length - 1) + '\n' + t('tool_technical_truncated');
      return output;
    }

    function asObject(value) {
      const parsed = parseMaybeJSON(value);
      return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : {};
    }

    function factValue(value, t) {
      if (value == null || value === '') return '';
      if (Array.isArray(value)) {
        const visible = value.slice(0, 8).map((entry) => safeText(entry, 60)).filter(Boolean);
        if (value.length > 8) visible.push(t('tool_more_entries', { n: value.length - 8 }));
        return clip(visible.join(', '), FACT_LIMIT);
      }
      if (typeof value === 'object') return clip(technicalText(value, t).replace(/\s+/g, ' '), FACT_LIMIT);
      return safeText(value, FACT_LIMIT);
    }

    function pushFact(facts, label, value, t) {
      const cleanLabel = safeText(label, 80);
      const cleanValue = factValue(value, t);
      if (!cleanLabel || !cleanValue) return;
      if (!facts.some((fact) => fact.label === cleanLabel && fact.value === cleanValue)) facts.push({ label: cleanLabel, value: cleanValue });
    }

    function pageLabel(page, t) {
      const value = safeText(page, FACT_LIMIT);
      return value || t('tool_page_root');
    }

    function itemFacts(items, facts, t) {
      const safeItems = Array.isArray(items) ? items.filter((item) => item && typeof item === 'object') : [];
      pushFact(facts, t('tool_fact_items'), safeItems.length, t);
      const ids = safeItems.map((item) => item.id).filter(Boolean);
      if (ids.length) pushFact(facts, t('tool_fact_item_ids'), ids, t);
      const pages = Array.from(new Set(safeItems.map((item) => item.page ? safeText(item.page, 80) : t('tool_scope_global'))));
      if (pages.length) pushFact(facts, t('tool_fact_pages'), pages, t);
      const types = Array.from(new Set(safeItems.map((item) => item.type).filter(Boolean)));
      if (types.length) pushFact(facts, t('tool_fact_type'), types, t);
      return safeItems.length;
    }

    function actionAndFacts(name, body, t) {
      const facts = [];
      const meta = TOOL_META[name];
      let vars = { name: safeText(name, 100) || t('tool_title_unknown'), n: 0, id: '', key: '', path: '', page: t('tool_page_root') };
      switch (name) {
        case 'ui_item_list':
          if (body.include_content === true) pushFact(facts, t('tool_fact_content'), t('tool_yes'), t);
          break;
        case 'ui_item_get':
          vars.id = safeText(body.id, 120);
          pushFact(facts, t('tool_fact_item_id'), body.id, t);
          break;
        case 'ui_item_put':
        case 'ui_items_replace':
          vars.n = itemFacts(body.items, facts, t);
          pushFact(facts, t('tool_fact_note'), body.note, t);
          break;
        case 'ui_item_delete': {
          const ids = Array.isArray(body.ids) ? body.ids : [];
          vars.n = ids.length;
          pushFact(facts, t('tool_fact_item_ids'), ids, t);
          break;
        }
        case 'ui_theme_set': {
          const fields = ['mode', 'primary', 'contrast', 'radius', 'gap', 'font'].filter((key) => own(body, key));
          vars.n = fields.length;
          pushFact(facts, t('tool_fact_fields'), fields, t);
          break;
        }
        case 'ui_kv_get':
        case 'ui_kv_set':
          vars.key = safeText(body.key, 120);
          pushFact(facts, t('tool_fact_key'), body.key, t);
          if (name === 'ui_kv_set' && own(body, 'value')) pushFact(facts, t('tool_fact_value'), heavySummary(body.value, 'value', t), t);
          break;
        case 'ui_page_set':
          vars.name = safeText(body.name, 120);
          pushFact(facts, t('tool_fact_route'), body.name, t);
          pushFact(facts, t('tool_fact_title'), body.title, t);
          break;
        case 'ui_page_delete':
          vars.name = safeText(body.name, 120);
          pushFact(facts, t('tool_fact_route'), body.name, t);
          break;
        case 'api_probe':
          vars.path = safeText(body.path, 180);
          pushFact(facts, t('tool_fact_path'), body.path, t);
          if (body.query && typeof body.query === 'object') pushFact(facts, t('tool_fact_query'), body.query, t);
          break;
        case 'ui_revision_list':
        case 'ui_page_errors':
          if (own(body, 'limit')) pushFact(facts, t('tool_fact_limit'), body.limit, t);
          break;
        case 'ui_revision_rollback':
          vars.id = safeText(body.id, 120);
          pushFact(facts, t('tool_fact_revision'), body.id, t);
          break;
        case 'ui_template_get':
          vars.page = pageLabel(body.page, t);
          pushFact(facts, t('tool_fact_page'), vars.page, t);
          break;
        case 'ui_template_put_current':
          vars.page = pageLabel(body.page, t);
          pushFact(facts, t('tool_fact_page'), vars.page, t);
          pushFact(facts, t('tool_fact_revision'), body.expected_revision, t);
          if (own(body, 'template')) pushFact(facts, t('tool_fact_value'), heavySummary(body.template, 'template', t), t);
          break;
        case 'ui_template_publish':
          vars.id = safeText(body.id, 120);
          pushFact(facts, t('tool_fact_template'), body.id, t);
          pushFact(facts, t('tool_fact_revision'), body.expected_revision, t);
          break;
        case 'ui_template_slot_delete':
          vars.id = safeText(body.slot_id, 120);
          pushFact(facts, t('tool_fact_template'), body.id, t);
          pushFact(facts, t('tool_fact_slot'), body.slot_id, t);
          pushFact(facts, t('tool_fact_revision'), body.expected_revision, t);
          break;
        default:
          pushFact(facts, t('tool_fact_operation'), name || t('tool_title_unknown'), t);
          for (const [key, value] of Object.entries(body).slice(0, 12)) {
            if (facts.length >= 5) break;
            if (isSensitiveKey(key) || HEAVY_KEY.test(key) || (value && typeof value === 'object')) continue;
            pushFact(facts, safeText(key, 60), value, t);
          }
      }
      const actionKey = meta ? meta[1] : 'tool_action_unknown';
      return { action: safeText(t(actionKey, vars), ACTION_LIMIT), facts: facts.slice(0, 8) };
    }

    // Business previews pass through the same redaction boundary as facts.
    function reviewView(review, translator, fieldLabel) {
      if (!review || typeof review !== 'object') return null;
      const t = typeof translator === 'function' ? translator : makeT('en');
      const rows = []; let truncated = false;
      const display = value => {
        if (value === undefined) return t('review_missing');
        const text = technicalText(value, t);
        if (text.length > 1200) truncated = true;
        return safeText(text, 1200);
      };
      const object = value => value && typeof value === 'object' && !Array.isArray(value);
      const visit = (before, after, path, depth) => {
        if (rows.length >= 24) { truncated = true; return; }
        if ((object(before) || object(after)) && (before == null || object(before)) && (after == null || object(after)) && depth < 4) {
          const keys = new Set([...Object.keys(object(before) ? before : {}), ...Object.keys(object(after) ? after : {})]);
          for (const key of keys) {
            if (rows.length >= 24) { truncated = true; break; }
            if (isSensitiveKey(key)) continue;
            const child = path ? path + ' / ' + safeText(key, 120) : safeText(key, 120);
            visit(object(before) ? before[key] : undefined, object(after) ? after[key] : undefined, child, depth + 1);
          }
          return;
        }
        const left = display(before), right = display(after);
        let label = path || t('review_field');
        if (typeof fieldLabel === 'function') {
          try {
            const supplied = fieldLabel(path.split(' / ').join('.'));
            if (supplied === null) return;
            if (typeof supplied === 'string' && supplied) label = safeText(supplied, 320);
          } catch { /* keep the original field path */ }
        }
        if (left !== right || (truncated && JSON.stringify(before) !== JSON.stringify(after))) rows.push({label, before: left, after: right});
        if (depth >= 4 && (object(before) || object(after))) truncated = true;
      };
      visit(review.before, review.after, '', 0);
      return {scope: safeText(review.scope, 320), blockingReason: safeText(review.blocking_reason, 600), rows, truncated};
    }

    function toolView(call, event, translator) {
      const t = typeof translator === 'function' ? translator : makeT('zh');
      const ev = event && typeof event === 'object' ? event : {};
      const embedded = ev.tool_call && typeof ev.tool_call === 'object' ? ev.tool_call : {};
      const supplied = call && typeof call === 'object' ? call : {};
      const source = Object.assign({}, embedded, supplied);
      const name = safeText(source.name || ev.tool_name, 120);
      const meta = TOOL_META[name];
      const title = safeText(meta ? t(meta[0]) : (ev.tool_title || source.title || name || t('tool_title_unknown')), TITLE_LIMIT);
      const body = asObject(source.body);
      const projected = actionAndFacts(name, body, t);
      // 宿主已提供可读标题时，通用卡片不用内部工具标识重复充当操作说明。
      if (!meta && (ev.tool_title || source.title)) {
        projected.action = title;
        projected.facts = projected.facts.filter(fact => fact.label !== t('tool_fact_operation') || fact.value !== name);
      }
      const pending = ev.type === 'confirmation_required' || ev.tool_status === 'pending';
      const summarySource = pending && source.reason ? source.reason : (ev.tool_summary || projected.action);
      const summary = safeText(summarySource, SUMMARY_LIMIT);
      const reason = pending && source.reason ? safeText(source.reason, SUMMARY_LIMIT) : '';
      const requestPayload = {};
      if (source.method) requestPayload.method = source.method;
      if (source.path) requestPayload.path = source.path;
      if (source.query && typeof source.query === 'object' && Object.keys(source.query).length) requestPayload.query = source.query;
      if (own(source, 'body') && source.body != null) requestPayload.body = source.body;
      const request = Object.keys(requestPayload).length ? technicalText(requestPayload, t) : '';
      const result = own(ev, 'tool_result') ? technicalText(ev.tool_result, t) : '';
      const debug = own(ev, 'tool_debug') ? technicalText(ev.tool_debug, t) : '';
      return {
        title,
        action: projected.action,
        summary,
        reason,
        facts: projected.facts,
        request,
        result,
        debug,
        hasTechnical: !!(request || result || debug),
      };
    }

    return { toolView, safeText, reviewView };
  })();

  /* ---------- fmt: 数字/金额/时间格式化(AI 生成代码的标准件) ---------- */
  const fmt = {
    num(n, digits) {
      const value = Number(n);
      if (n == null || !Number.isFinite(value)) return '-';
      const parsed = digits == null ? null : Number(digits);
      const precision = parsed == null || !Number.isFinite(parsed) ? null : Math.min(20, Math.max(0, Math.floor(parsed)));
      return value.toLocaleString('en-US', {
        minimumFractionDigits: precision == null ? 0 : precision,
        maximumFractionDigits: precision == null ? 2 : precision,
      });
    },
    // money(12345) → "¥12,345.00"; money(12345, {cents:true}) 表示入参是"分"
    money(n, opts) {
      if (n == null || isNaN(n)) return '-';
      opts = opts || {};
      let v = Number(n);
      if (opts.cents) v /= 100;
      const symbol = opts.symbol != null ? opts.symbol : '¥';
      return symbol + fmt.num(v, opts.digits == null ? 2 : opts.digits);
    },
    // date(ts, 'YYYY-MM-DD HH:mm'); ts 支持 Date/时间戳/ISO 字符串
    date(ts, pattern) {
      const d = ts instanceof Date ? ts : new Date(ts);
      if (isNaN(d.getTime())) return '-';
      const p2 = (x) => String(x).padStart(2, '0');
      const map = {
        YYYY: d.getFullYear(), MM: p2(d.getMonth() + 1), DD: p2(d.getDate()),
        HH: p2(d.getHours()), mm: p2(d.getMinutes()), ss: p2(d.getSeconds()),
      };
      return String(pattern || 'YYYY-MM-DD HH:mm').replace(/YYYY|MM|DD|HH|mm|ss/g, (k) => map[k]);
    },
    ago(ts) {
      const d = ts instanceof Date ? ts : new Date(ts);
      if (isNaN(d.getTime())) return '-';
      const s = Math.max(0, (Date.now() - d.getTime()) / 1000);
      if (s < 60) return Math.floor(s) + ' 秒前';
      if (s < 3600) return Math.floor(s / 60) + ' 分钟前';
      if (s < 86400) return Math.floor(s / 3600) + ' 小时前';
      return Math.floor(s / 86400) + ' 天前';
    },
    pct(x, digits) {
      if (x == null || isNaN(x)) return '-';
      return fmt.num(Number(x) * 100, digits == null ? 1 : digits) + '%';
    },
  };

  /* ---------- util: 转义与节流(安全与性能标准件) ---------- */
  const util = {
    esc(s) {
      return String(s == null ? '' : s)
        .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
    },
    escAttr(s) { return util.esc(s); },
    debounce(fn, ms) {
      let timer = 0;
      return function (...args) {
        clearTimeout(timer);
        timer = setTimeout(() => fn.apply(this, args), ms || 200);
      };
    },
    throttle(fn, ms) {
      let last = 0;
      let timer = 0;
      return function (...args) {
        const now = Date.now();
        const rest = (ms || 200) - (now - last);
        if (rest <= 0) {
          clearTimeout(timer);
          timer = 0;
          last = now;
          fn.apply(this, args);
          return;
        }
        clearTimeout(timer);
        timer = setTimeout(() => { timer = 0; last = Date.now(); fn.apply(this, args); }, rest);
      };
    },
  };

  /* ---------- chart: 零依赖 SVG 图表(折线/柱状/迷你走势) ----------
   * 颜色读容器的 --ui-* tokens → 明暗/换肤自适应; ResizeObserver 自动重绘;
   * 返回 {redraw, destroy}; 内核会把活动图表挂到 Item teardown 自动清理。 */
  const liveCharts = new Set();

  function tokenColor(el, name, fallback) {
    const v = getComputedStyle(el).getPropertyValue(name).trim();
    return v || fallback;
  }
  const SERIES_FALLBACK = ['#6366f1', '#16a34a', '#d97706', '#dc2626', '#0ea5e9', '#8b5cf6'];
  function seriesColors(el, n) {
    const base = [tokenColor(el, '--ui-primary', SERIES_FALLBACK[0]),
      tokenColor(el, '--ui-ok', SERIES_FALLBACK[1]),
      tokenColor(el, '--ui-warn', SERIES_FALLBACK[2]),
      tokenColor(el, '--ui-err', SERIES_FALLBACK[3])];
    const out = [];
    for (let i = 0; i < n; i++) out.push(base[i] || SERIES_FALLBACK[i % SERIES_FALLBACK.length]);
    return out;
  }
  function niceTicks(min, max, count) {
    if (min === max) { max = min + 1; }
    const span = max - min;
    const step0 = span / Math.max(1, count);
    const mag = Math.pow(10, Math.floor(Math.log10(step0)));
    let step = mag;
    for (const m of [1, 2, 5, 10]) { if (mag * m >= step0) { step = mag * m; break; } }
    const lo = Math.floor(min / step) * step;
    const hi = Math.ceil(max / step) * step;
    const ticks = [];
    for (let v = lo; v <= hi + step / 2; v += step) ticks.push(v);
    return { lo, hi, ticks };
  }
  const svgNS = 'http://www.w3.org/2000/svg';
  function svgEl(tag, attrs) {
    const n = document.createElementNS(svgNS, tag);
    for (const k in attrs) n.setAttribute(k, attrs[k]);
    return n;
  }
  function shortNum(v) {
    const a = Math.abs(v);
    if (a >= 1e8) return (v / 1e8).toFixed(1).replace(/\.0$/, '') + '亿';
    if (a >= 1e4) return (v / 1e4).toFixed(1).replace(/\.0$/, '') + '万';
    if (a >= 1000) return (v / 1000).toFixed(1).replace(/\.0$/, '') + 'k';
    return String(Math.round(v * 100) / 100);
  }

  // kind: 'line' | 'bars'; data: {labels:[], series:[{name,data:[]}]}
  function drawChart(el, kind, data, opts) {
    opts = opts || {};
    el.textContent = '';
    const spark = !!opts.spark;
    const width = Math.max(el.clientWidth || 320, spark ? 60 : 120);
    const height = opts.height || 220;
    const pad = spark ? { l: 2, r: 2, t: 3, b: 3 } : { l: 44, r: 10, t: 10, b: 22 };
    const rawLabels = Array.isArray(data && data.labels) ? data.labels : [];
    const rawSeries = ((data && data.series) || []).filter((s) => s && Array.isArray(s.data) && s.data.length);
    let sourceLength = rawLabels.length;
    for (const item of rawSeries) sourceLength = Math.max(sourceLength, item.data.length);
    const maxPoints = Math.max(100, Math.min(5000, Number(opts.maxPoints) || 1200));
    const sampleCount = Math.min(sourceLength, maxPoints);
    const indices = [];
    for (let i = 0; i < sampleCount; i++) {
      indices.push(sampleCount === sourceLength ? i : Math.round((i / Math.max(1, sampleCount - 1)) * (sourceLength - 1)));
    }
    const labels = indices.map((index) => rawLabels[index]);
    const series = rawSeries.map((item) => ({ ...item, data: indices.map((index) => item.data[index]) }));
    const chartLabel = opts.ariaLabel || `${kind === 'bars' ? '柱状图' : '折线图'}，${series.length} 个序列，${sourceLength} 个数据点`;
    const svg = svgEl('svg', { viewBox: `0 0 ${width} ${height}`, width: '100%', height, role: 'img', 'aria-label': chartLabel });
    const svgTitle = svgEl('title', {});
    svgTitle.textContent = chartLabel;
    svg.appendChild(svgTitle);
    el.appendChild(svg);
    if (!series.length) {
      const t = svgEl('text', { x: width / 2, y: height / 2, 'text-anchor': 'middle', 'font-size': 12, fill: tokenColor(el, '--ui-text-muted', '#888') });
      t.textContent = opts.emptyText || '暂无数据';
      svg.appendChild(t);
      return;
    }
    let min = 0;
    let max = 0;
    let finiteCount = 0;
    for (const item of series) {
      for (const raw of item.data) {
        const value = Number(raw);
        if (!Number.isFinite(value)) continue;
        min = Math.min(min, value);
        max = Math.max(max, value);
        finiteCount++;
      }
    }
    if (!finiteCount) {
      const t = svgEl('text', { x: width / 2, y: height / 2, 'text-anchor': 'middle', 'font-size': 12, fill: tokenColor(el, '--ui-text-muted', '#888') });
      t.textContent = opts.emptyText || '暂无数据';
      svg.appendChild(t);
      return;
    }
    const { lo, hi, ticks } = niceTicks(min, max, 4);
    const plotW = width - pad.l - pad.r;
    const plotH = height - pad.t - pad.b;
    const y = (v) => pad.t + plotH - ((v - lo) / (hi - lo)) * plotH;
    let n = Math.max(labels.length, 1);
    for (const item of series) n = Math.max(n, item.data.length);
    const xAt = (i) => pad.l + (n === 1 ? plotW / 2 : (i / (n - 1)) * plotW);
    const border = tokenColor(el, '--ui-border', 'rgba(128,128,128,.25)');
    const muted = tokenColor(el, '--ui-text-muted', '#888');
    // 网格与 Y 轴刻度(迷你走势不画)
    for (const tv of spark ? [] : ticks) {
      const ty = y(tv);
      svg.appendChild(svgEl('line', { x1: pad.l, y1: ty, x2: width - pad.r, y2: ty, stroke: border, 'stroke-width': 1 }));
      const label = svgEl('text', { x: pad.l - 6, y: ty + 4, 'text-anchor': 'end', 'font-size': 10.5, fill: muted });
      label.textContent = shortNum(tv);
      svg.appendChild(label);
    }
    // X 轴标签(最多 8 个, 均匀抽样; 迷你走势不画)
    const maxXLabels = spark ? 0 : Math.min(8, labels.length);
    for (let k = 0; k < maxXLabels; k++) {
      const i = maxXLabels === 1 ? 0 : Math.round((k / (maxXLabels - 1)) * (labels.length - 1));
      const label = svgEl('text', { x: xAt(i), y: height - 6, 'text-anchor': 'middle', 'font-size': 10.5, fill: muted });
      label.textContent = String(labels[i] == null ? '' : labels[i]);
      svg.appendChild(label);
    }
    const colors = seriesColors(el, series.length);
    if (kind === 'bars') {
      const groupW = plotW / n;
      const barW = Math.max(3, Math.min(28, (groupW * 0.7) / series.length));
      series.forEach((s, si) => {
        s.data.forEach((v, i) => {
          if (!Number.isFinite(Number(v))) return;
          const cx = pad.l + groupW * i + groupW / 2;
          const x0 = cx - (barW * series.length) / 2 + si * barW;
          const y1 = y(Math.max(0, Number(v)));
          const y0 = y(Math.min(0, Number(v)));
          const bar = svgEl('rect', { x: x0, y: y1, width: barW - 1, height: Math.max(1, y0 - y1), fill: colors[si], rx: 2 });
          const title = svgEl('title', {});
          title.textContent = `${s.name || ''} ${labels[i] != null ? labels[i] : i}: ${fmt.num(v)}`;
          bar.appendChild(title);
          svg.appendChild(bar);
        });
      });
    } else {
      series.forEach((s, si) => {
        const pts = s.data.map((v, i) => (!Number.isFinite(Number(v)) ? null : `${xAt(i)},${y(Number(v))}`)).filter(Boolean);
        if (pts.length && opts.area !== false && series.length === 1) {
          const area = svgEl('path', {
            d: `M ${pad.l},${y(Math.max(lo, 0))} L ${pts.join(' L ')} L ${xAt(s.data.length - 1)},${y(Math.max(lo, 0))} Z`,
            fill: colors[si], opacity: 0.12,
          });
          svg.appendChild(area);
        }
        svg.appendChild(svgEl('polyline', { points: pts.join(' '), fill: 'none', stroke: colors[si], 'stroke-width': spark ? 1.5 : 2, 'stroke-linejoin': 'round' }));
        if (!spark) {
          s.data.forEach((v, i) => {
            if (!Number.isFinite(Number(v))) return;
            const dot = svgEl('circle', { cx: xAt(i), cy: y(Number(v)), r: 2.5, fill: colors[si] });
            const title = svgEl('title', {});
            title.textContent = `${s.name || ''} ${labels[i] != null ? labels[i] : i}: ${fmt.num(v)}`;
            dot.appendChild(title);
            svg.appendChild(dot);
          });
        }
      });
    }
    // 多序列图例(迷你走势不画)
    if (series.length > 1 && !spark) {
      let lx = pad.l;
      series.forEach((s, si) => {
        svg.appendChild(svgEl('rect', { x: lx, y: 2, width: 10, height: 10, fill: colors[si], rx: 2 }));
        const label = svgEl('text', { x: lx + 14, y: 11, 'font-size': 11, fill: muted });
        label.textContent = s.name || 'S' + (si + 1);
        svg.appendChild(label);
        lx += 22 + (s.name || 'S1').length * 11;
      });
    }
  }

  function mountChart(el, kind, data, opts) {
    if (typeof el === 'string') el = document.querySelector(el);
    if (!el) return { redraw() {}, destroy() {} };
    let cur = { data, opts };
    const redraw = (next) => {
      if (next) cur = { data: next.labels || next.series ? next : cur.data, opts: cur.opts };
      try { drawChart(el, kind, cur.data, cur.opts); } catch { /* 忽略绘制异常 */ }
    };
    let ro = null;
    if (typeof ResizeObserver === 'function') {
      let w = el.clientWidth;
      ro = new ResizeObserver(() => {
        if (Math.abs(el.clientWidth - w) > 8) { w = el.clientWidth; redraw(); }
      });
      ro.observe(el);
    }
    const handle = {
      el,
      redraw,
      update(nextData) { cur.data = nextData; redraw(); },
      destroy() {
        if (ro) ro.disconnect();
        liveCharts.delete(handle);
        el.textContent = '';
      },
    };
    liveCharts.add(handle);
    redraw();
    return handle;
  }

  const chart = {
    line: (el, data, opts) => mountChart(el, 'line', data, opts),
    bars: (el, data, opts) => mountChart(el, 'bars', data, opts),
    spark(el, values, opts) {
      return mountChart(el, 'line', { labels: values.map((_, i) => i), series: [{ name: '', data: values }] },
        Object.assign({ height: 36 }, opts, { spark: true }));
    },
    redrawAll() { liveCharts.forEach((c) => c.redraw()); },
    // gc: 容器已从文档移除的图表(Item 被卸载)回收其 ResizeObserver
    gc() { liveCharts.forEach((c) => { if (c.el && !c.el.isConnected) c.destroy(); }); },
  };

  /* ---------- itemLayout: 已有 Item 的页面级排版与外观状态机 ----------
   * 模板编辑器只管结构；这里单独保存业务 Item 的顺序、大小、绝对坐标和少量安全外观。
   * 纯函数状态机不接触 DOM/HTTP，持久化键使用页面名的 UTF-8 FNV-1a，避免把路由原文
   * 直接拼进 KV key。styleFor 总是返回完整的受管样式集合，空字符串用于清掉模式切换后
   * 遗留的 inline style。 */
  const itemLayout = (() => {
    const MODES = new Set(['grid', 'free']);
    const MAX_ITEMS = 512;
    const HISTORY_LIMIT = 80;
    const own = (value, key) => Object.prototype.hasOwnProperty.call(value, key);
    const clone = (value) => JSON.parse(JSON.stringify(value));

    function clipBytes(value, maxBytes) {
      let out = ''; let bytes = 0;
      for (const char of String(value == null ? '' : value)) {
        const cp = char.codePointAt(0);
        const size = cp <= 0x7f ? 1 : (cp <= 0x7ff ? 2 : (cp <= 0xffff ? 3 : 4));
        if (bytes + size > maxBytes) break;
        out += char; bytes += size;
      }
      return out;
    }

    function utf8Bytes(value) {
      if (typeof TextEncoder !== 'undefined') return new TextEncoder().encode(String(value));
      const bytes = [];
      for (const char of String(value)) {
        let cp = char.codePointAt(0);
        if (cp >= 0xd800 && cp <= 0xdfff) cp = 0xfffd;
        if (cp <= 0x7f) bytes.push(cp);
        else if (cp <= 0x7ff) bytes.push(0xc0 | (cp >> 6), 0x80 | (cp & 0x3f));
        else if (cp <= 0xffff) bytes.push(0xe0 | (cp >> 12), 0x80 | ((cp >> 6) & 0x3f), 0x80 | (cp & 0x3f));
        else bytes.push(0xf0 | (cp >> 18), 0x80 | ((cp >> 12) & 0x3f), 0x80 | ((cp >> 6) & 0x3f), 0x80 | (cp & 0x3f));
      }
      return bytes;
    }

    function fnv1a(value) {
      let hash = 0x811c9dc5;
      for (const byte of utf8Bytes(value)) hash = Math.imul((hash ^ byte) >>> 0, 0x01000193) >>> 0;
      return hash.toString(16).padStart(8, '0');
    }

    function key(page) {
      const value = String(page == null ? '' : page);
      return !value || value === 'root' ? 'item-layout.root' : 'item-layout.' + fnv1a(value);
    }

    function finite(value, fallback, min, max, integer) {
      const number = Number(value);
      if (!Number.isFinite(number)) return fallback;
      const bounded = Math.min(max, Math.max(min, number));
      return integer === false ? Math.round(bounded * 1000) / 1000 : Math.round(bounded);
    }

    function safeItemID(value) {
      const id = clipBytes(String(value == null ? '' : value).trim(), 128);
      if (!id || /["'<>`&\\/\r\n\t ]/.test(id)) return '';
      return id;
    }

    function safeSlot(value) {
      const slot = clipBytes(String(value == null ? '' : value).trim(), 128);
      return !slot || /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(slot) ? slot : '';
    }

    // 这里只接受“颜色”，不接受 background-image/gradient 等可携带 URL 的完整 CSS。
    function sanitizeColor(value) {
      const color = String(value == null ? '' : value).trim();
      if (!color || color.length > 96) return '';
      if (/^#[0-9a-f]{3,4}$/i.test(color) || /^#[0-9a-f]{6}([0-9a-f]{2})?$/i.test(color)) return color.toLowerCase();
      if (/^transparent$/i.test(color)) return 'transparent';
      if (/^currentcolor$/i.test(color)) return 'currentColor';
      const variable = color.match(/^var\((--[A-Za-z0-9_-]{1,64})\)$/);
      if (variable) return 'var(' + variable[1] + ')';

      const rgb = color.match(/^rgba?\(\s*(\d{1,3})(%?)\s*,\s*(\d{1,3})(%?)\s*,\s*(\d{1,3})(%?)(?:\s*,\s*(0|1|0?\.\d{1,3}))?\s*\)$/i);
      if (rgb) {
        const percent = rgb[2] || rgb[4] || rgb[6];
        if (!!rgb[2] !== !!percent || !!rgb[4] !== !!percent || !!rgb[6] !== !!percent) return '';
        const max = percent ? 100 : 255;
        const channels = [Number(rgb[1]), Number(rgb[3]), Number(rgb[5])];
        if (channels.some((part) => part > max)) return '';
        const prefix = rgb[7] == null ? 'rgb' : 'rgba';
        const suffix = percent ? '%' : '';
        const parts = channels.map((part) => part + suffix);
        if (rgb[7] != null) parts.push(String(Math.round(Number(rgb[7]) * 1000) / 1000));
        return prefix + '(' + parts.join(', ') + ')';
      }

      const hsl = color.match(/^hsla?\(\s*(-?\d{1,3}(?:\.\d+)?)\s*,\s*(\d{1,3}(?:\.\d+)?)%\s*,\s*(\d{1,3}(?:\.\d+)?)%(?:\s*,\s*(0|1|0?\.\d{1,3}))?\s*\)$/i);
      if (hsl) {
        const saturation = Number(hsl[2]); const lightness = Number(hsl[3]);
        if (Math.abs(Number(hsl[1])) > 360 || saturation > 100 || lightness > 100) return '';
        const prefix = hsl[4] == null ? 'hsl' : 'hsla';
        const parts = [String(Number(hsl[1])), saturation + '%', lightness + '%'];
        if (hsl[4] != null) parts.push(String(Math.round(Number(hsl[4]) * 1000) / 1000));
        return prefix + '(' + parts.join(', ') + ')';
      }
      return '';
    }

    function empty(page) {
      return { version: 1, page: clipBytes(String(page == null ? '' : page), 256), mode: 'grid', orderBySlot: Object.create(null), items: Object.create(null) };
    }

    function sanitizeItem(id, raw) {
      raw = raw && typeof raw === 'object' && !Array.isArray(raw) ? raw : {};
      const flowRaw = raw.flow && typeof raw.flow === 'object' && !Array.isArray(raw.flow) ? raw.flow : {};
      const freeRaw = raw.free && typeof raw.free === 'object' && !Array.isArray(raw.free) ? raw.free : {};
      const appearanceRaw = raw.appearance && typeof raw.appearance === 'object' && !Array.isArray(raw.appearance) ? raw.appearance : {};
      const flow = { span: finite(flowRaw.span, 12, 1, 12) };
      if (own(flowRaw, 'height') && flowRaw.height !== '' && flowRaw.height != null && Number.isFinite(Number(flowRaw.height))) {
        flow.height = finite(flowRaw.height, 64, 64, 4000);
      }
      const appearance = {};
      const background = sanitizeColor(appearanceRaw.background);
      const color = sanitizeColor(appearanceRaw.color);
      if (background) appearance.background = background;
      if (color) appearance.color = color;
      if (own(appearanceRaw, 'radius') && appearanceRaw.radius !== '' && appearanceRaw.radius != null && Number.isFinite(Number(appearanceRaw.radius))) {
        appearance.radius = finite(appearanceRaw.radius, 0, 0, 96);
      }
      if (own(appearanceRaw, 'opacity') && appearanceRaw.opacity !== '' && appearanceRaw.opacity != null && Number.isFinite(Number(appearanceRaw.opacity))) {
        appearance.opacity = finite(appearanceRaw.opacity, 1, 0, 1, false);
      }
      return {
        slot: safeSlot(raw.slot),
        flow,
        free: {
          x: finite(freeRaw.x, 0, 0, 10000),
          y: finite(freeRaw.y, 0, 0, 10000),
          width: finite(freeRaw.width, 320, 120, 4000),
          height: finite(freeRaw.height, 180, 64, 4000),
          z: finite(freeRaw.z, 0, 0, 999),
        },
        appearance,
      };
    }

    function normalize(input, pageOverride) {
      const source = input && typeof input === 'object' && !Array.isArray(input) ? input : {};
      const page = arguments.length > 1 ? pageOverride : source.page;
      const out = empty(page);
      out.mode = MODES.has(source.mode) ? source.mode : 'grid';
      const rawItems = source.items && typeof source.items === 'object' && !Array.isArray(source.items) ? source.items : {};
      for (const [rawID, raw] of Object.entries(rawItems).slice(0, MAX_ITEMS)) {
        const id = safeItemID(rawID);
        if (!id || own(out.items, id) || !raw || typeof raw !== 'object' || Array.isArray(raw)) continue;
        out.items[id] = sanitizeItem(id, raw);
      }

      const seen = new Set();
      const rawOrder = source.orderBySlot && typeof source.orderBySlot === 'object' && !Array.isArray(source.orderBySlot) ? source.orderBySlot : {};
      for (const [rawSlot, rawIDs] of Object.entries(rawOrder).slice(0, MAX_ITEMS)) {
        const slot = safeSlot(rawSlot);
        if (slot !== rawSlot || !Array.isArray(rawIDs)) continue;
        const list = [];
        for (const rawID of rawIDs.slice(0, MAX_ITEMS)) {
          const id = safeItemID(rawID);
          if (!id || seen.has(id) || !own(out.items, id) || out.items[id].slot !== slot) continue;
          seen.add(id); list.push(id);
        }
        if (list.length) out.orderBySlot[slot] = list;
      }
      for (const id of Object.keys(out.items)) {
        if (seen.has(id)) continue;
        const slot = out.items[id].slot;
        if (!out.orderBySlot[slot]) out.orderBySlot[slot] = [];
        out.orderBySlot[slot].push(id);
      }
      return out;
    }

    // JSON.stringify preserves object insertion order, which is not semantic for
    // item/slot maps. Sort object keys recursively while retaining array order so
    // equivalent documents produce the same dirty/fingerprint result.
    function canonical(value) {
      if (Array.isArray(value)) return '[' + value.map(canonical).join(',') + ']';
      if (value && typeof value === 'object') {
        return '{' + Object.keys(value).sort().map((key) => JSON.stringify(key) + ':' + canonical(value[key])).join(',') + '}';
      }
      return JSON.stringify(value);
    }

    function sourceFingerprint(value) {
      if (value == null) return 'missing';
      const budget = { nodes: 0 };
      function walk(input, depth) {
        budget.nodes += 1;
        if (depth > 64 || budget.nodes > 5000) throw new Error('layout fingerprint budget exceeded');
        if (Array.isArray(input)) return '[' + input.map((entry) => walk(entry, depth + 1)).join(',') + ']';
        if (input && typeof input === 'object') {
          return '{' + Object.keys(input).sort().map((key) => JSON.stringify(key) + ':' + walk(input[key], depth + 1)).join(',') + '}';
        }
        return JSON.stringify(input);
      }
      try { return 'json:' + walk(value, 0); }
      catch (_) {
        // Public KV values are already byte-bounded. For unusually deep/wide
        // documents stay conservative: insertion-order changes may conflict,
        // but unknown data is never silently treated as equivalent.
        try { return 'opaque:' + JSON.stringify(value); } catch (_) { return 'opaque:unavailable'; }
      }
    }

    // Keep a canonical fingerprint of the last server-owned layout document.
    // The editor's seeded baseline also contains live Item geometry, so it is
    // intentionally unsuitable for deciding whether an SSE resync represents
    // a real concurrent layout write.
    function remoteSnapshot(input, page) {
      const valid = !!input && typeof input === 'object' && !Array.isArray(input) && String(input.page || '') === String(page || '');
      const doc = valid ? normalize(input, page) : null;
      return { valid, doc, fingerprint: valid ? canonical(doc) : '', sourceFingerprint: sourceFingerprint(input) };
    }

    // KV revisions intentionally represent the exact JSON bytes, whereas the
    // editor works with semantic documents. Classify a changed revision before
    // surfacing a conflict so harmless object-key reordering, an identical save
    // from a peer, and a committed write whose response was lost all converge.
    // Source fingerprints include unknown fields; normalized fingerprints alone
    // must never make a forward-compatible extension disappear silently.
    function classifyRemoteChange(input) {
      const value = input && typeof input === 'object' ? input : {};
      if (String(value.remoteRevision || '') === String(value.baselineRevision || '')) return 'unchanged';
      if (String(value.remoteSourceFingerprint || '') === String(value.baselineSourceFingerprint || '')) return 'equivalent-baseline';
      if (value.remoteExists && value.remoteValid && String(value.remoteSourceFingerprint || '') === String(value.localSourceFingerprint || '')) return 'matches-local';
      return 'conflict';
    }

    function cloneDoc(doc) { return normalize(doc, doc && doc.page); }
    function differs(doc, baseline) { return canonical(doc) !== canonical(baseline); }

    // Reconcile a historical layout snapshot with the current live Item
    // topology. Existing geometry/appearance stays historical; removed Items
    // disappear and newly arrived Items are seeded from liveDoc. This keeps
    // undo/redo from resurrecting deleted Items or dropping newly added ones.
    function reconcileDoc(snapshot, liveInput) {
      const live = normalize(liveInput, liveInput && liveInput.page);
      const source = normalize(snapshot, live.page);
      const out = empty(live.page);
      out.mode = source.mode;
      for (const id of Object.keys(live.items)) {
        out.items[id] = clone(source.items[id] || live.items[id]);
        out.items[id].slot = live.items[id].slot;
      }
      const seen = new Set();
      for (const slot of Object.keys(live.orderBySlot)) {
        const current = live.orderBySlot[slot] || [];
        const preferred = source.orderBySlot[slot] || [];
        const ids = [];
        for (const id of preferred.concat(current)) {
          if (seen.has(id) || !own(out.items, id) || out.items[id].slot !== slot) continue;
          seen.add(id); ids.push(id);
        }
        if (ids.length) out.orderBySlot[slot] = ids;
      }
      for (const id of Object.keys(out.items)) {
        if (seen.has(id)) continue;
        const slot = out.items[id].slot;
        if (!out.orderBySlot[slot]) out.orderBySlot[slot] = [];
        out.orderBySlot[slot].push(id);
      }
      return normalize(out, live.page);
    }

    function reconcileState(state, liveDoc) {
      state = state && state.doc ? state : initial(liveDoc);
      const doc = reconcileDoc(state.doc, liveDoc);
      const baseline = reconcileDoc(state.baseline, liveDoc);
      const past = state.past.map((entry) => reconcileDoc(entry, liveDoc));
      const future = state.future.map((entry) => reconcileDoc(entry, liveDoc));
      return Object.assign({}, state, {
        doc, baseline, past, future,
        selectedId: own(doc.items, state.selectedId) ? state.selectedId : '',
        pendingRemoveId: own(doc.items, state.pendingRemoveId) ? state.pendingRemoveId : '',
        dirty: differs(doc, baseline),
      });
    }

    function initial(page, input) {
      if (page && typeof page === 'object' && input == null) { input = page; page = input.page; }
      const doc = normalize(input, page);
      return { doc, baseline: cloneDoc(doc), selectedId: '', pendingRemoveId: '', past: [], future: [], dirty: false };
    }

    function commit(state, candidate, selectedId) {
      const doc = normalize(candidate, state.doc.page);
      if (!differs(doc, state.doc)) return Object.assign({}, state, { pendingRemoveId: '' });
      return Object.assign({}, state, {
        doc,
        selectedId: selectedId == null ? state.selectedId : selectedId,
        pendingRemoveId: '',
        past: state.past.concat([cloneDoc(state.doc)]).slice(-HISTORY_LIMIT),
        future: [],
        dirty: differs(doc, state.baseline),
      });
    }

    function removeFromOrders(doc, id) {
      for (const slot of Object.keys(doc.orderBySlot)) {
        doc.orderBySlot[slot] = doc.orderBySlot[slot].filter((entry) => entry !== id);
        if (!doc.orderBySlot[slot].length) delete doc.orderBySlot[slot];
      }
    }

    function mergePatch(target, patch) {
      if (!patch || typeof patch !== 'object' || Array.isArray(patch)) return;
      for (const [name, value] of Object.entries(patch)) {
        if (value == null || value === '') delete target[name];
        else target[name] = value;
      }
    }

    function reduce(state, action) {
      state = state && state.doc ? state : initial();
      action = action && typeof action === 'object' ? action : {};
      if (action.type === 'select') {
        const id = safeItemID(action.id);
        return !id || own(state.doc.items, id) ? Object.assign({}, state, { selectedId: id, pendingRemoveId: '' }) : state;
      }
      if (action.type === 'mode') {
        if (!MODES.has(action.mode) || action.mode === state.doc.mode) return state;
        const next = cloneDoc(state.doc); next.mode = action.mode;
        return commit(state, next);
      }
      if (action.type === 'patch') {
        const id = safeItemID(action.id || state.selectedId);
        if (!id || !own(state.doc.items, id)) return state;
        const next = cloneDoc(state.doc); const item = next.items[id];
        const patch = action.patch && typeof action.patch === 'object' && !Array.isArray(action.patch) ? action.patch : {};
        if (action.section === 'flow' || action.section === 'free' || action.section === 'appearance') mergePatch(item[action.section], patch);
        else {
          if (own(patch, 'slot')) item.slot = patch.slot;
          for (const section of ['flow', 'free', 'appearance']) if (own(patch, section)) mergePatch(item[section], patch[section]);
        }
        if (safeSlot(item.slot) !== state.doc.items[id].slot) {
          removeFromOrders(next, id);
          const slot = safeSlot(item.slot);
          item.slot = slot;
          if (!next.orderBySlot[slot]) next.orderBySlot[slot] = [];
          next.orderBySlot[slot].push(id);
        }
        return commit(state, next, id);
      }
      if (action.type === 'reorder') {
        const next = cloneDoc(state.doc);
        const id = safeItemID(action.id);
        if (id && own(next.items, id)) {
          const slot = own(action, 'slot') ? safeSlot(action.slot) : next.items[id].slot;
          removeFromOrders(next, id);
          next.items[id].slot = slot;
          const list = next.orderBySlot[slot] || (next.orderBySlot[slot] = []);
          const index = finite(action.index, list.length, 0, list.length);
          list.splice(index, 0, id);
          return commit(state, next, id);
        }
        const slot = safeSlot(action.slot);
        const supplied = Array.isArray(action.ids) ? action.ids : (Array.isArray(action.order) ? action.order : null);
        if (!supplied) return state;
        const current = Object.keys(next.items).filter((itemID) => next.items[itemID].slot === slot);
        const wanted = []; const seen = new Set();
        for (const rawID of supplied.slice(0, MAX_ITEMS)) {
          const itemID = safeItemID(rawID);
          if (!itemID || seen.has(itemID) || !current.includes(itemID)) continue;
          seen.add(itemID); wanted.push(itemID);
        }
        next.orderBySlot[slot] = wanted.concat(current.filter((itemID) => !seen.has(itemID)));
        return commit(state, next);
      }
      if (['remove', 'requestRemove', 'removePending'].includes(action.type)) {
        const id = safeItemID(action.id || state.selectedId);
        return id && own(state.doc.items, id) ? Object.assign({}, state, { pendingRemoveId: id }) : state;
      }
      if (['cancelRemove', 'clearPendingRemove'].includes(action.type)) return state.pendingRemoveId ? Object.assign({}, state, { pendingRemoveId: '' }) : state;
      if (['confirmRemove', 'removeConfirmed'].includes(action.type)) {
        const id = safeItemID(action.id || state.pendingRemoveId);
        if (!id || !own(state.doc.items, id) || (state.pendingRemoveId && id !== state.pendingRemoveId)) return state;
        const next = cloneDoc(state.doc); delete next.items[id]; removeFromOrders(next, id);
        return commit(state, next, state.selectedId === id ? '' : state.selectedId);
      }
      if (action.type === 'undo') {
        if (!state.past.length) return state;
        const doc = cloneDoc(state.past[state.past.length - 1]);
        return Object.assign({}, state, {
          doc, selectedId: own(doc.items, state.selectedId) ? state.selectedId : '', pendingRemoveId: '',
          past: state.past.slice(0, -1), future: [cloneDoc(state.doc)].concat(state.future).slice(0, HISTORY_LIMIT),
          dirty: differs(doc, state.baseline),
        });
      }
      if (action.type === 'redo') {
        if (!state.future.length) return state;
        const doc = cloneDoc(state.future[0]);
        return Object.assign({}, state, {
          doc, selectedId: own(doc.items, state.selectedId) ? state.selectedId : '', pendingRemoveId: '',
          past: state.past.concat([cloneDoc(state.doc)]).slice(-HISTORY_LIMIT), future: state.future.slice(1),
          dirty: differs(doc, state.baseline),
        });
      }
      if (action.type === 'markSaved') {
        const doc = action.doc ? normalize(action.doc, state.doc.page) : cloneDoc(state.doc);
        return Object.assign({}, state, { doc, baseline: cloneDoc(doc), pendingRemoveId: '', dirty: false });
      }
      if (action.type === 'replaceDoc') return commit(state, action.doc, action.selectedId);
      if (action.type === 'replace') return initial(own(action, 'page') ? action.page : state.doc.page, action.doc);
      return state;
    }

    const clearStyle = () => ({
      position: '', left: '', top: '', right: '', bottom: '', width: '', height: '', zIndex: '',
      gridColumn: '', gridRow: '', order: '', overflow: '', background: '', color: '', borderRadius: '', opacity: '', boxSizing: '',
    });

    function styleFor(source, idOrMode, modeOverride) {
      let item = source; let mode = idOrMode;
      if (source && source.items && typeof source.items === 'object') {
        const id = safeItemID(idOrMode);
        item = id && own(source.items, id) ? source.items[id] : null;
        mode = modeOverride || source.mode;
      }
      const styles = clearStyle();
      if (!item || typeof item !== 'object') return styles;
      const normalized = sanitizeItem('', item);
      styles.boxSizing = 'border-box';
      styles.background = normalized.appearance.background || '';
      styles.color = normalized.appearance.color || '';
      styles.borderRadius = own(normalized.appearance, 'radius') ? normalized.appearance.radius + 'px' : '';
      styles.opacity = own(normalized.appearance, 'opacity') ? String(normalized.appearance.opacity) : '';
      if (mode === 'free') {
        styles.position = 'absolute';
        styles.left = normalized.free.x + 'px'; styles.top = normalized.free.y + 'px';
        styles.width = normalized.free.width + 'px'; styles.height = normalized.free.height + 'px';
        styles.zIndex = String(normalized.free.z); styles.overflow = 'auto';
      } else {
        styles.gridColumn = 'span ' + normalized.flow.span;
        if (own(normalized.flow, 'height')) { styles.height = normalized.flow.height + 'px'; styles.overflow = 'auto'; }
      }
      return styles;
    }

    return { key, fnv1a, empty, normalize, remoteSnapshot, classifyRemoteChange, reconcileDoc, reconcileState, initial, reduce, styleFor, sanitizeColor };
  })();

  /* ---------- template: 空模板结构编辑器的纯状态机 ----------
   * 与 DOM/HTTP 解耦，既供内核编辑器使用，也能独立测试。模板只描述 Root / Container /
   * Slot 的结构和排版，不保存或渲染业务 Item 内容。tablet/mobile 只保存覆写值，读取时
   * 按 desktop -> tablet -> mobile 继承。 */
  const template = (() => {
    const BREAKPOINTS = ['desktop', 'tablet', 'mobile'];
    const TYPES = new Set(['root', 'container', 'slot']);
    const MODES = new Set(['flow', 'grid', 'free']);
    const clone = (value) => JSON.parse(JSON.stringify(value));
    const clipBytes = (value, maxBytes) => {
      let out = ''; let bytes = 0;
      for (const char of String(value == null ? '' : value)) {
        const cp = char.codePointAt(0);
        const size = cp <= 0x7f ? 1 : (cp <= 0x7ff ? 2 : (cp <= 0xffff ? 3 : 4));
        if (bytes + size > maxBytes) break;
        out += char; bytes += size;
      }
      return out;
    };
    const finite = (value, fallback, min, max) => {
      const n = Number(value);
      if (!Number.isFinite(n)) return fallback;
      return Math.min(max, Math.max(min, Math.round(n)));
    };
    const baseLayout = () => ({ mode: 'flow', direction: 'column', gap: 12, padding: 16 });

    function empty() {
      return {
        version: 1,
        rootId: 'root',
        nodes: {
          root: { id: 'root', type: 'root', name: 'Root', parent: null, layouts: { desktop: baseLayout() }, frames: {} },
        },
        children: { root: [] },
        meta: { version: 0, placements: [], breakpoints: [], slotMeta: {}, nodeKinds: {} },
      };
    }

    function sanitizeLayout(raw, partial) {
      raw = raw && typeof raw === 'object' && !Array.isArray(raw) ? raw : {};
      const out = {};
      if (MODES.has(raw.mode)) out.mode = raw.mode;
      else if (!partial) out.mode = 'flow';
      if (raw.direction === 'row' || raw.direction === 'column') out.direction = raw.direction;
      if (raw.wrap === true || raw.wrap === false) out.wrap = raw.wrap;
      if (['start', 'center', 'end', 'stretch'].includes(raw.align)) out.align = raw.align;
      if (raw.gap != null) out.gap = finite(raw.gap, partial ? undefined : 12, 0, 128);
      if (raw.padding != null) out.padding = finite(raw.padding, partial ? undefined : 16, 0, 160);
      if (raw.columns != null) out.columns = finite(raw.columns, partial ? undefined : 12, 1, 24);
      if (raw.minHeight != null) out.minHeight = finite(raw.minHeight, partial ? undefined : 160, 48, 4000);
      if (!partial) return Object.assign(baseLayout(), out);
      return out;
    }

    function sanitizeFrame(raw) {
      raw = raw && typeof raw === 'object' && !Array.isArray(raw) ? raw : {};
      const out = {};
      if (raw.x != null) out.x = finite(raw.x, 0, 0, 4000);
      if (raw.y != null) out.y = finite(raw.y, 0, 0, 4000);
      if (raw.w != null) out.w = finite(raw.w, 240, 48, 10000);
      if (raw.h != null) out.h = finite(raw.h, 120, 48, 4000);
      if (raw.span != null) out.span = finite(raw.span, 1, 1, 24);
      return out;
    }

    function normalize(input) {
      const src = input && typeof input === 'object' && !Array.isArray(input) ? input : {};
      const out = empty();
      const rawNodes = src.nodes && typeof src.nodes === 'object' && !Array.isArray(src.nodes) ? src.nodes : {};
      const accepted = new Set(['root']);
      for (const [key, raw] of Object.entries(rawNodes).slice(0, 1000)) {
        if (!raw || typeof raw !== 'object' || Array.isArray(raw)) continue;
        const id = clipBytes(String(raw.id || key).trim(), 128);
        if (!id || id === 'root' || accepted.has(id) || !TYPES.has(raw.type) || raw.type === 'root') continue;
        accepted.add(id);
        const layouts = {};
        const frames = {};
        for (const [bp, value] of Object.entries(raw.layouts || {}).slice(0, 32)) {
          if (/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(bp)) layouts[bp] = sanitizeLayout(value, bp !== 'desktop');
        }
        for (const [bp, value] of Object.entries(raw.frames || {}).slice(0, 32)) {
          if (/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(bp)) frames[bp] = sanitizeFrame(value);
        }
        if (!layouts.desktop) layouts.desktop = baseLayout();
        out.nodes[id] = {
          id,
          type: raw.type,
          name: clipBytes(String(raw.name || (raw.type === 'slot' ? 'Slot' : 'Container')).trim(), 128),
          parent: null,
          layouts,
          frames,
        };
        out.children[id] = [];
      }
      const rawRoot = rawNodes.root;
      if (rawRoot && typeof rawRoot === 'object') {
        out.nodes.root.name = clipBytes(String(rawRoot.name || 'Root').trim(), 128) || 'Root';
        out.nodes.root.layouts = {};
        out.nodes.root.frames = {};
        for (const [bp, value] of Object.entries(rawRoot.layouts || {}).slice(0, 32)) {
          if (/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(bp)) out.nodes.root.layouts[bp] = sanitizeLayout(value, bp !== 'desktop');
        }
        if (!out.nodes.root.layouts.desktop) out.nodes.root.layouts.desktop = baseLayout();
      }
      const rawChildren = src.children && typeof src.children === 'object' && !Array.isArray(src.children) ? src.children : {};
      const visited = new Set(['root']);
      function attach(parent) {
        const list = Array.isArray(rawChildren[parent]) ? rawChildren[parent] : [];
        for (const rawId of list.slice(0, 1000)) {
          const id = String(rawId);
          if (!accepted.has(id) || visited.has(id) || id === parent) continue;
          visited.add(id);
          out.nodes[id].parent = parent;
          out.children[parent].push(id);
          attach(id);
        }
      }
      attach('root');
      // 损坏/旧数据中的孤儿节点仍可编辑，安全地挂回 Root。
      for (const id of accepted) {
        if (id === 'root' || visited.has(id)) continue;
        out.nodes[id].parent = 'root';
        out.children.root.push(id);
        visited.add(id);
        attach(id);
      }
      const meta = src.meta && typeof src.meta === 'object' && !Array.isArray(src.meta) ? src.meta : {};
      out.meta = {
        version: Number.isFinite(Number(meta.version)) ? Math.max(0, Math.floor(Number(meta.version))) : 0,
        placements: Array.isArray(meta.placements) ? clone(meta.placements.slice(0, 5000)) : [],
        breakpoints: Array.isArray(meta.breakpoints) ? clone(meta.breakpoints.slice(0, 32)) : [],
        slotMeta: meta.slotMeta && typeof meta.slotMeta === 'object' && !Array.isArray(meta.slotMeta) ? clone(meta.slotMeta) : {},
        nodeKinds: meta.nodeKinds && typeof meta.nodeKinds === 'object' && !Array.isArray(meta.nodeKinds) ? clone(meta.nodeKinds) : {},
        externalIDs: meta.externalIDs && typeof meta.externalIDs === 'object' && !Array.isArray(meta.externalIDs) ? clone(meta.externalIDs) : {},
        extraProps: meta.extraProps && typeof meta.extraProps === 'object' && !Array.isArray(meta.extraProps) ? clone(meta.extraProps) : {},
      };
      return out;
    }

    function fromVersion(version) {
      version = version && typeof version === 'object' && !Array.isArray(version) ? version : {};
      const rootID = String(version.root_id || 'root');
      const nodeList = Array.isArray(version.nodes) ? version.nodes : [];
      const slotList = Array.isArray(version.slots) ? version.slots : [];
      const raw = { nodes: {}, children: {}, meta: { version: version.version || 0, placements: version.placements || [], breakpoints: version.breakpoints || [], slotMeta: {}, nodeKinds: {}, externalIDs: { root: rootID }, extraProps: {} } };
      raw.nodes.root = { id: 'root', type: 'root', name: 'Root', layouts: { desktop: baseLayout() }, frames: {} };
      raw.children.root = [];
      const idMap = new Map([[rootID, 'root']]);
      for (const item of nodeList) {
        if (!item || typeof item !== 'object') continue;
        const sourceID = String(item.id || '');
        if (!sourceID || sourceID === rootID) continue;
        let targetID = sourceID === 'root' ? 'node-root' : sourceID;
        let suffix = 2;
        while (raw.nodes[targetID]) targetID = sourceID + '-' + suffix++;
        idMap.set(sourceID, targetID);
        raw.meta.externalIDs[targetID] = sourceID;
        const props = item.props && typeof item.props === 'object' && !Array.isArray(item.props) ? item.props : {};
        const inferredMode = item.kind === 'grid' ? 'grid' : (item.kind === 'group' ? 'free' : 'flow');
        raw.nodes[targetID] = {
          id: targetID,
          type: 'container',
          name: String(item.name || 'Container'),
          layouts: props.layouts || { desktop: props.layout || Object.assign(baseLayout(), { mode: inferredMode }) },
          frames: props.frames || {},
        };
        raw.children[targetID] = [];
        raw.meta.nodeKinds[targetID] = item.kind;
        raw.meta.extraProps[targetID] = Object.fromEntries(Object.entries(props).filter(([key]) => !['layouts', 'layout', 'frames', 'slot_layouts'].includes(key)));
      }
      const rootRecord = nodeList.find((node) => node && String(node.id) === rootID);
      if (rootRecord) {
        const props = rootRecord.props && typeof rootRecord.props === 'object' && !Array.isArray(rootRecord.props) ? rootRecord.props : {};
        const inferredMode = rootRecord.kind === 'grid' ? 'grid' : (rootRecord.kind === 'group' ? 'free' : 'flow');
        raw.nodes.root.name = String(rootRecord.name || 'Root');
        raw.nodes.root.layouts = props.layouts || { desktop: props.layout || Object.assign(baseLayout(), { mode: inferredMode }) };
        raw.meta.nodeKinds.root = rootRecord.kind;
        raw.meta.extraProps.root = Object.fromEntries(Object.entries(props).filter(([key]) => !['layouts', 'layout', 'frames', 'slot_layouts'].includes(key)));
      }
      const ordered = nodeList.slice().sort((a, b) => Number(a && a.order || 0) - Number(b && b.order || 0));
      for (const item of ordered) {
        if (!item || String(item.id) === rootID) continue;
        const id = idMap.get(String(item.id));
        if (!id || !raw.nodes[id]) continue;
        const parent = idMap.get(String(item.parent_id || rootID)) || 'root';
        if (!raw.children[parent]) raw.children[parent] = [];
        raw.children[parent].push(id);
      }
      // Slot 是独立于 Node 的命名挂载点。同一 Node 可以拥有多个 Slot，Root 也可以有 Slot；
      // 编辑器用虚拟 Slot 图层表达它们，保存时不会伪造额外 TemplateNode。
      for (const slot of slotList) {
        if (!slot || typeof slot !== 'object') continue;
        const owner = idMap.get(String(slot.node_id || ''));
        if (!owner || !raw.nodes[owner]) continue;
        const sourceID = String(slot.id || 'slot');
        let layerID = sourceID;
        let suffix = 2;
        if (raw.nodes[layerID]) layerID = 'slot-layer-' + sourceID;
        while (raw.nodes[layerID]) layerID = 'slot-layer-' + sourceID + '-' + suffix++;
        const ownerSource = nodeList.find((item) => item && String(item.id) === String(slot.node_id));
        const ownerProps = ownerSource && ownerSource.props && typeof ownerSource.props === 'object' ? ownerSource.props : {};
        const slotLayout = ownerProps.slot_layouts && ownerProps.slot_layouts[sourceID] || {};
        raw.nodes[layerID] = { id: layerID, type: 'slot', name: String(slot.name || 'Slot'), layouts: slotLayout.layouts || { desktop: baseLayout() }, frames: slotLayout.frames || {} };
        raw.children[layerID] = [];
        raw.children[owner].push(layerID);
        raw.meta.slotMeta[layerID] = clone(slot);
      }
      return normalize(raw);
    }

    function toVersion(input) {
      const doc = normalize(input);
      const nodes = [];
      const slots = [];
      const externalID = (id) => String(doc.meta.externalIDs[id] || id);
      const slotLayouts = {};
      for (const { id, node } of flatten(doc)) {
        if (node.type !== 'slot') continue;
        const owner = node.parent || 'root';
        const slotID = String((doc.meta.slotMeta[id] || {}).id || id);
        if (!slotLayouts[owner]) slotLayouts[owner] = {};
        slotLayouts[owner][slotID] = { layouts: clone(node.layouts || {}), frames: clone(node.frames || {}) };
      }
      for (const { id, node } of flatten(doc)) {
        const siblings = (doc.children[node.parent || 'root'] || []).filter((childID) => doc.nodes[childID] && doc.nodes[childID].type !== 'slot');
        const desktop = resolveLayout(node, 'desktop');
        let kind = 'group';
        if (node.type === 'slot') {
          const previous = doc.meta.slotMeta[id] || {};
          slots.push({ id: String(previous.id || id), node_id: externalID(node.parent || 'root'), name: node.name, accepts: Array.isArray(previous.accepts) ? previous.accepts : ['html'], locked: !!previous.locked });
          continue;
        }
        const storedKind = doc.meta.nodeKinds[id];
        const kindMatches = desktop.mode === 'grid' ? storedKind === 'grid' : (desktop.mode === 'free' ? storedKind === 'group' : (storedKind === 'stack' || storedKind === 'canvas'));
        if (kindMatches) kind = storedKind;
        else if (desktop.mode === 'grid') kind = 'grid';
        else if (desktop.mode === 'free') kind = 'group';
        else kind = id === 'root' ? 'canvas' : 'stack';
        const props = Object.assign({}, clone(doc.meta.extraProps[id] || {}), { layouts: clone(node.layouts || {}), frames: clone(node.frames || {}) });
        if (slotLayouts[id] && Object.keys(slotLayouts[id]).length) props.slot_layouts = slotLayouts[id];
        nodes.push({
          id: externalID(id),
          parent_id: id === 'root' ? '' : externalID(node.parent || 'root'),
          kind,
          name: node.name,
          order: id === 'root' ? 0 : Math.max(0, siblings.indexOf(id)),
          props,
        });
      }
      const validSlots = new Set(slots.map((slot) => slot.id));
      const placements = (doc.meta.placements || []).map((placement) => {
        const next = clone(placement);
        if (next.slot_id && !validSlots.has(String(next.slot_id))) { next.slot_id = ''; next.unassigned = true; }
        return next;
      });
      const breakpoints = doc.meta.breakpoints && doc.meta.breakpoints.length
        ? clone(doc.meta.breakpoints)
        : [
            { id: 'desktop', name: 'Desktop', min_width: 1024, columns: 12, gap: 12 },
            { id: 'tablet', name: 'Tablet', min_width: 768, max_width: 1023, columns: 8, gap: 12 },
            { id: 'mobile', name: 'Mobile', max_width: 767, columns: 4, gap: 10 },
          ];
      return { version: doc.meta.version || 0, root_id: externalID('root'), nodes, slots, placements, breakpoints };
    }

    function saveRequest(record, page, doc) {
      const current = record && typeof record === 'object' && !Array.isArray(record) ? clone(record) : {};
      // page/persisted/migrated 属于 current endpoint 的绑定或响应元数据，不能混入 Template 实体。
      delete current.page;
      delete current.persisted;
      delete current.migrated;
      current.draft = toVersion(doc);
      return { page: String(page || ''), expected_revision: Math.max(0, Number(current.revision) || 0), template: current };
    }

    function resolveMap(map, breakpoint, defaults) {
      const result = Object.assign({}, defaults || {});
      const index = BREAKPOINTS.indexOf(breakpoint);
      if (index < 0) {
        Object.assign(result, map && map.desktop || {}, map && map[breakpoint] || {});
        return result;
      }
      for (let i = 0; i <= index; i++) Object.assign(result, map && map[BREAKPOINTS[i]] || {});
      return result;
    }
    function resolveLayout(node, breakpoint) { return sanitizeLayout(resolveMap(node && node.layouts, breakpoint, baseLayout()), false); }
    function resolveFrame(node, breakpoint) { return sanitizeFrame(resolveMap(node && node.frames, breakpoint, {})); }
    function chooseBreakpoint(version, width) {
      width = Math.max(0, Number(width) || 0);
      const list = Array.isArray(version && version.breakpoints) ? version.breakpoints : [];
      const matches = list.filter((bp) => bp && (!Number(bp.min_width) || width >= Number(bp.min_width)) && (!Number(bp.max_width) || width <= Number(bp.max_width)));
      if (matches.length) return String(matches.sort((a, b) => Number(b.min_width || 0) - Number(a.min_width || 0))[0].id || 'desktop');
      if (!list.length) return width < 768 ? 'mobile' : (width < 1024 ? 'tablet' : 'desktop');
      return String(list.slice().sort((a, b) => Math.abs(width - Number(a.min_width || 0)) - Math.abs(width - Number(b.min_width || 0)))[0].id || 'desktop');
    }
    function resolvePlacement(version, itemID, breakpoint) {
      const list = (Array.isArray(version && version.placements) ? version.placements : []).filter((placement) => placement && String(placement.item_id) === String(itemID));
      if (!list.length) return null;
      const chain = breakpoint === 'mobile' ? ['', 'desktop', 'tablet', 'mobile']
        : (breakpoint === 'tablet' ? ['', 'desktop', 'tablet'] : ['', 'desktop', breakpoint]);
      let found = null;
      for (const key of chain) {
        const candidate = list.find((placement) => String(placement.breakpoint || '') === key);
        if (candidate) found = candidate;
      }
      return found || list.find((placement) => String(placement.breakpoint || '') === String(breakpoint)) || list.find((placement) => !placement.breakpoint) || null;
    }
    function descendants(doc, id) {
      const out = [];
      const visit = (key) => (doc.children[key] || []).forEach((child) => { out.push(child); visit(child); });
      visit(id);
      return out;
    }
    function flatten(doc) {
      const out = [];
      const visit = (id, depth) => {
        if (!doc.nodes[id]) return;
        out.push({ id, depth, node: doc.nodes[id] });
        (doc.children[id] || []).forEach((child) => visit(child, depth + 1));
      };
      visit('root', 0);
      return out;
    }

    function pointerMoved(start, point, threshold) {
      start = start && typeof start === 'object' ? start : {};
      point = point && typeof point === 'object' ? point : {};
      const dx = Number(point.x) - Number(start.x);
      const dy = Number(point.y) - Number(start.y);
      const limit = Math.max(1, Math.min(32, Number(threshold) || 4));
      return Number.isFinite(dx) && Number.isFinite(dy) && Math.hypot(dx, dy) >= limit;
    }

    function moveFrame(frame, delta, snap) {
      frame = sanitizeFrame(frame);
      delta = delta && typeof delta === 'object' ? delta : {};
      const step = Math.max(1, Math.min(64, Number(snap) || 8));
      const dx = Number.isFinite(Number(delta.x)) ? Number(delta.x) : 0;
      const dy = Number.isFinite(Number(delta.y)) ? Number(delta.y) : 0;
      const x = Math.max(0, Math.min(4000, Math.round(((frame.x || 0) + dx) / step) * step));
      const y = Math.max(0, Math.min(4000, Math.round(((frame.y || 0) + dy) / step) * step));
      return { x, y };
    }

    function gridSpanForWidth(width, geometry) {
      geometry = geometry && typeof geometry === 'object' ? geometry : {};
      const columns = finite(geometry.columns, 12, 1, 24);
      const clientWidth = Math.max(1, Number(geometry.clientWidth) || 1);
      const paddingLeft = Math.max(0, Number(geometry.paddingLeft) || 0);
      const paddingRight = Math.max(0, Number(geometry.paddingRight) || 0);
      const gap = Math.max(0, Number(geometry.gap) || 0);
      const contentWidth = Math.max(1, clientWidth - paddingLeft - paddingRight);
      const trackWidth = Math.max(1, (contentWidth - gap * (columns - 1)) / columns);
      const requested = Math.round((Math.max(48, Number(width) || 48) + gap) / (trackWidth + gap));
      return Math.max(1, Math.min(columns, requested));
    }

    function resizeFrame(mode, rect, delta, geometry, snap) {
      rect = rect && typeof rect === 'object' ? rect : {};
      delta = delta && typeof delta === 'object' ? delta : {};
      const step = Math.max(1, Math.min(64, Number(snap) || 8));
      const dx = Number.isFinite(Number(delta.x)) ? Number(delta.x) : 0;
      const dy = Number.isFinite(Number(delta.y)) ? Number(delta.y) : 0;
      const width = Math.max(48, Math.min(10000, Math.round((Math.max(0, Number(rect.width) || 0) + dx) / step) * step));
      if (mode === 'grid') return { span: gridSpanForWidth(width, geometry) };
      const height = Math.max(48, Math.min(4000, Math.round((Math.max(0, Number(rect.height) || 0) + dy) / step) * step));
      return { w: width, h: height };
    }

    // Touch UIs expose these actions as buttons; the keyboard tree uses the
    // same move semantics. Returning reducer actions keeps both paths aligned
    // and makes disabled states independently testable without a DOM.
    function reorderActions(doc, id) {
      const none = { up: null, down: null, indent: null, outdent: null };
      if (!doc || !doc.nodes || !doc.children || id === 'root' || !doc.nodes[id]) return none;
      const node = doc.nodes[id];
      const parent = node.parent || 'root';
      const siblings = Array.isArray(doc.children[parent]) ? doc.children[parent] : [];
      const index = siblings.indexOf(id);
      if (index < 0) return none;
      const actions = { up: null, down: null, indent: null, outdent: null };
      if (index > 0) actions.up = { type: 'move', id, parentId: parent, index: index - 1 };
      if (index < siblings.length - 1) actions.down = { type: 'move', id, parentId: parent, index: index + 1 };
      const previous = index > 0 ? doc.nodes[siblings[index - 1]] : null;
      if (previous && previous.type !== 'slot') actions.indent = { type: 'move', id, parentId: previous.id };
      if (parent !== 'root' && doc.nodes[parent]) {
        const grand = doc.nodes[parent].parent || 'root';
        const grandSiblings = Array.isArray(doc.children[grand]) ? doc.children[grand] : [];
        actions.outdent = { type: 'move', id, parentId: grand, index: Math.max(0, grandSiblings.indexOf(parent) + 1) };
      }
      return actions;
    }

    function initial(doc) {
      const normalized = normalize(doc);
      return { doc: normalized, baseline: clone(normalized), selectedId: 'root', breakpoint: 'desktop', view: 'studio', past: [], future: [], dirty: false };
    }
    function differsFromBaseline(doc, baseline) {
      return JSON.stringify(doc) !== JSON.stringify(baseline);
    }
    function commit(state, nextDoc, selectedId) {
      const previous = clone(state.doc);
      const past = state.past.concat([previous]).slice(-80);
      const doc = normalize(nextDoc);
      return Object.assign({}, state, { doc, selectedId: selectedId || state.selectedId, past, future: [], dirty: differsFromBaseline(doc, state.baseline) });
    }
    function move(doc, id, parent, index) {
      if (id === 'root' || !doc.nodes[id] || !doc.nodes[parent] || doc.nodes[parent].type === 'slot' || descendants(doc, id).includes(parent)) return doc;
      const next = clone(doc);
      const oldParent = next.nodes[id].parent;
      next.children[oldParent] = (next.children[oldParent] || []).filter((child) => child !== id);
      const list = (next.children[parent] || []).filter((child) => child !== id);
      const at = Math.min(list.length, Math.max(0, Number.isFinite(Number(index)) ? Math.floor(Number(index)) : list.length));
      list.splice(at, 0, id);
      next.children[parent] = list;
      next.nodes[id].parent = parent;
      return next;
    }

    function reduce(state, action) {
      state = state || initial();
      action = action || {};
      if (action.type === 'select') return state.doc.nodes[action.id] ? Object.assign({}, state, { selectedId: action.id }) : state;
      if (action.type === 'breakpoint' && BREAKPOINTS.includes(action.breakpoint)) return Object.assign({}, state, { breakpoint: action.breakpoint });
      if (action.type === 'view' && (action.view === 'studio' || action.view === 'compare')) return Object.assign({}, state, { view: action.view });
      if (action.type === 'markSaved') {
        const doc = clone(state.doc);
        if (action.version != null && Number.isFinite(Number(action.version))) doc.meta.version = Math.max(0, Math.floor(Number(action.version)));
        return Object.assign({}, state, { doc, baseline: clone(doc), dirty: false });
      }
      if (action.type === 'replace') return initial(action.doc);
      if (action.type === 'undo') {
        if (!state.past.length) return state;
        const doc = state.past[state.past.length - 1];
        const selectedId = doc.nodes[state.selectedId] ? state.selectedId : 'root';
        return Object.assign({}, state, { doc, selectedId, past: state.past.slice(0, -1), future: [clone(state.doc)].concat(state.future).slice(0, 80), dirty: differsFromBaseline(doc, state.baseline) });
      }
      if (action.type === 'redo') {
        if (!state.future.length) return state;
        const doc = state.future[0];
        const selectedId = doc.nodes[state.selectedId] ? state.selectedId : 'root';
        return Object.assign({}, state, { doc, selectedId, past: state.past.concat([clone(state.doc)]).slice(-80), future: state.future.slice(1), dirty: differsFromBaseline(doc, state.baseline) });
      }
      if (action.type === 'add') {
        if (Object.keys(state.doc.nodes).length >= 512) return state;
        const type = action.nodeType === 'slot' ? 'slot' : 'container';
        const id = clipBytes(String(action.id || '').trim(), 128);
        const parent = state.doc.nodes[action.parentId] ? action.parentId : state.selectedId;
        if (!id || state.doc.nodes[id] || !state.doc.nodes[parent]) return state;
        const next = clone(state.doc);
        next.nodes[id] = { id, type, name: clipBytes(action.name || (type === 'slot' ? 'Slot' : 'Container'), 128), parent, layouts: { desktop: baseLayout() }, frames: {} };
        next.children[id] = [];
        const list = next.children[parent] || (next.children[parent] = []);
        const at = Math.min(list.length, Math.max(0, Number.isFinite(Number(action.index)) ? Math.floor(Number(action.index)) : list.length));
        list.splice(at, 0, id);
        const parentLayout = resolveLayout(next.nodes[parent], state.breakpoint);
        if (parentLayout.mode === 'free') {
          next.nodes[id].frames[state.breakpoint] = { x: 0, y: at * 136, w: 240, h: 120 };
        }
        return commit(state, next, id);
      }
      if (action.type === 'remove') {
        const id = action.id || state.selectedId;
        if (id === 'root' || !state.doc.nodes[id]) return state;
        const next = clone(state.doc);
        const removeIds = [id].concat(descendants(next, id));
        const parent = next.nodes[id].parent || 'root';
        next.children[parent] = (next.children[parent] || []).filter((child) => child !== id);
        removeIds.forEach((key) => { delete next.nodes[key]; delete next.children[key]; });
        return commit(state, next, parent);
      }
      if (action.type === 'move') {
        const next = move(state.doc, action.id || state.selectedId, action.parentId, action.index);
        return next === state.doc ? state : commit(state, next, action.id || state.selectedId);
      }
      if (action.type === 'duplicate') {
        const id = action.id || state.selectedId;
        if (id === 'root' || !state.doc.nodes[id] || !action.newId) return state;
        const source = state.doc.nodes[id];
        const newID = clipBytes(String(action.newId).trim(), 128);
        if (!newID || state.doc.nodes[newID]) return state;
        const next = clone(state.doc);
        next.nodes[newID] = clone(source);
        next.nodes[newID].id = newID;
        next.nodes[newID].name = clipBytes(source.name + ' copy', 128);
        next.children[newID] = [];
        const siblings = next.children[source.parent] || (next.children[source.parent] = []);
        siblings.splice(siblings.indexOf(id) + 1, 0, newID);
        const parentLayout = resolveLayout(next.nodes[source.parent], state.breakpoint);
        if (parentLayout.mode === 'free') {
          const frame = resolveFrame(source, state.breakpoint);
          next.nodes[newID].frames[state.breakpoint] = Object.assign({}, frame, { x: (frame.x || 0) + 16, y: (frame.y || 0) + 16 });
        }
        if (source.type === 'slot' && next.meta.slotMeta[id]) {
          next.meta.slotMeta[newID] = clone(next.meta.slotMeta[id]);
          next.meta.slotMeta[newID].id = newID;
          next.meta.slotMeta[newID].node_id = newID;
        }
        if (source.type !== 'slot') {
          if (next.meta.extraProps[id]) next.meta.extraProps[newID] = clone(next.meta.extraProps[id]);
          if (next.meta.nodeKinds[id]) next.meta.nodeKinds[newID] = next.meta.nodeKinds[id];
        }
        return commit(state, next, newID);
      }
      if (['rename', 'patchLayout', 'patchFrame'].includes(action.type)) {
        const id = action.id || state.selectedId;
        if (!state.doc.nodes[id]) return state;
        const next = clone(state.doc);
        if (action.type === 'rename') next.nodes[id].name = clipBytes(String(action.name || '').trim(), 128) || next.nodes[id].name;
        else {
          const breakpoint = BREAKPOINTS.includes(action.breakpoint) ? action.breakpoint : state.breakpoint;
          const field = action.type === 'patchLayout' ? 'layouts' : 'frames';
          next.nodes[id][field] = next.nodes[id][field] || {};
          const current = next.nodes[id][field][breakpoint] || {};
          const patch = action.patch && typeof action.patch === 'object' ? action.patch : {};
          const merged = Object.assign({}, current, patch);
          for (const [key, value] of Object.entries(merged)) if (value == null || value === '') delete merged[key];
          next.nodes[id][field][breakpoint] = action.type === 'patchLayout' ? sanitizeLayout(merged, breakpoint !== 'desktop') : sanitizeFrame(merged);
          if (action.type === 'patchLayout' && patch.mode === 'free') {
            (next.children[id] || []).forEach((childID, index) => {
              const child = next.nodes[childID];
              child.frames = child.frames || {};
              const own = child.frames[breakpoint] || {};
              if (own.x == null || own.y == null) child.frames[breakpoint] = Object.assign({ x: 0, y: index * 136, w: 240, h: 120 }, own);
            });
          }
        }
        return commit(state, next, id);
      }
      return state;
    }

    return {
      BREAKPOINTS, empty, normalize, fromVersion, toVersion, saveRequest, initial, reduce,
      resolveLayout, resolveFrame, chooseBreakpoint, resolvePlacement, descendants, flatten,
      pointerMoved, moveFrame, gridSpanForWidth, resizeFrame, reorderActions,
    };
  })();

  /* ---------- runtime 纯函数: 多实例命名/样式与 inspect 分页 ---------- */
  const runtime = (() => {
    function createHTTPClient(request, resolveURL) {
      if (typeof request !== 'function') throw new TypeError('ihtml: HTTP request function required');
      const url = typeof resolveURL === 'function' ? resolveURL : (path) => String(path == null ? '' : path);
      const client = (path, options) => request(path, options);
      return Object.assign(client, {
        url,
        request,
        get: (path, options) => request(path, options),
        post: (path, body, options) => request(path, { ...options, method: 'POST', body }),
        put: (path, body, options) => request(path, { ...options, method: 'PUT', body }),
        del: (path, options) => request(path, { ...options, method: 'DELETE' }),
      });
    }
    function storageHash(value) {
      value = String(value || '');
      let hash = 0x811c9dc5;
      for (let i = 0; i < value.length; i++) {
        hash ^= value.charCodeAt(i);
        hash = Math.imul(hash, 0x01000193) >>> 0;
      }
      return hash.toString(36);
    }
    function storageKey(kind, base, host, user) {
      return 'ihtml.' + String(kind || '') + '.' + storageHash([base, host, user || 'anonymous'].join('\u001f'));
    }
    function migrateLegacyStorage(storage, markerKey, pairs) {
      if (!storage || typeof storage.getItem !== 'function' || typeof storage.setItem !== 'function') return false;
      try {
        if (storage.getItem(String(markerKey || '')) != null) return false;
        for (const pair of pairs || []) {
          const legacy = pair && String(pair.legacy || '');
          const scoped = pair && String(pair.scoped || '');
          if (!legacy || !scoped || storage.getItem(scoped) != null) continue;
          const value = storage.getItem(legacy);
          if (value != null) storage.setItem(scoped, value);
        }
        // Keep the old keys for rollback; the marker only prevents another
        // mount/user from claiming the same process-global legacy values.
        storage.setItem(String(markerKey || ''), '1');
        return true;
      } catch { return false; }
    }
    function selectPrimary(current, candidates) {
      const list = Array.from(candidates || []).filter(Boolean);
      return current && list.includes(current) ? current : list[0] || null;
    }
    function itemLifecycleRecord(registry, id, lifecycle) {
      if (!registry || typeof registry.get !== 'function') return null;
      const record = registry.get(String(id || ''));
      return record && record.lifecycle === lifecycle ? record : null;
    }
    function itemFacadeExpression(apiExpression, id, lifecycle) {
      return '(' + String(apiExpression || 'null') + ').__itemFacade(' +
        JSON.stringify(String(id || '')) + ',' + JSON.stringify(lifecycle) + ')';
    }
    function normalizeMountOptions(opts, href, standalone) {
      if (opts == null) opts = {};
      if (typeof opts !== 'object') throw new TypeError('ihtml.mount: options must be an object');
      const out = { ...opts };
      const base = out.base ? new URL(String(out.base), String(href || '')) : new URL('.', String(href || ''));
      if (base.protocol !== 'http:' && base.protocol !== 'https:') {
        throw new TypeError('ihtml.mount: base must use http: or https:');
      }
      if (base.username || base.password) {
        throw new TypeError('ihtml.mount: base must not contain credentials');
      }
      // API paths are always resolved below a directory. A query/hash on the
      // configured base is page state, not part of that directory.
      base.search = '';
      base.hash = '';
      if (!base.pathname.endsWith('/')) base.pathname += '/';
      out.base = base.href;
      if (!standalone) out.params = new URLSearchParams(out.params || {});
      if (out.storageKey != null) out.storageKey = String(out.storageKey);
      if (out.scriptNonce != null) out.scriptNonce = String(out.scriptNonce).trim();
      if (Array.isArray(out.messageOrigins)) out.messageOrigins = out.messageOrigins.slice();
      out.routing = out.routing || (standalone ? 'hash' : 'memory');
      if (out.routing !== 'memory' && out.routing !== 'hash') {
        throw new TypeError('ihtml.mount: routing must be "memory" or "hash"');
      }
      return out;
    }
    function applyScriptNonce(script, nonce) {
      if (!script) return script;
      const value = String(nonce || '').trim();
      if (value) script.nonce = value;
      return script;
    }
    function scopeCSS(css, standalone, scopeStyles, instanceKey) {
      css = String(css || '');
      if (standalone || scopeStyles === false) return css;
      return '@scope (.ihtml-root[data-ihtml-instance="' + String(instanceKey || '') + '"]) {\n' + css + '\n}';
    }
    function inspectRange(total, offset, limit, legacy) {
      total = Math.max(0, Math.floor(Number(total) || 0));
      if (legacy === true) {
        const legacyLimit = 200;
        const end = Math.min(total, legacyLimit);
        return { total, offset: 0, limit: legacyLimit, end, nextOffset: end < total ? end : 0, truncated: end < total };
      }
      offset = Math.max(0, Math.min(total, Math.floor(Number(offset) || 0)));
      limit = Math.max(1, Math.min(20, Math.floor(Number(limit) || 20)));
      const end = Math.min(total, offset + limit);
      return { total, offset, limit, end, nextOffset: end < total ? end : 0, truncated: end < total };
    }

    // 仅用于判断能否安全地在 ES module 顶层注入词法 ihtml 变量。
    // 它不改写源码；字符串/模板/注释先被遮掉，块/函数内部的词法
    // shadow 不会误伤模块顶层绑定。var 在裸块中仍会提升到模块作用域，
    // 因此对嵌套 var 保持保守判断。
    function unicodeIdentifierEscape(code, index) {
      if (code[index] !== '\\' || code[index + 1] !== 'u') return null;
      if (code[index + 2] === '{') {
        let end = index + 3;
        while (end < code.length && end < index + 9 && /[0-9A-Fa-f]/.test(code[end])) end++;
        if (end === index + 3 || code[end] !== '}') return null;
        const point = Number.parseInt(code.slice(index + 3, end), 16);
        if (!Number.isFinite(point) || point > 0x10ffff) return null;
        return { end: end + 1, value: String.fromCodePoint(point) };
      }
      const hex = code.slice(index + 2, index + 6);
      if (!/^[0-9A-Fa-f]{4}$/.test(hex)) return null;
      return { end: index + 6, value: String.fromCharCode(Number.parseInt(hex, 16)) };
    }
    function identifierAt(code, start) {
      let index = start;
      let decoded = '';
      let first = true;
      while (index < code.length) {
        const escape = unicodeIdentifierEscape(code, index);
        if (escape) {
          const valid = first ? /[A-Za-z_$]/.test(escape.value) : /[A-Za-z0-9_$]/.test(escape.value);
          if (!valid) break;
          decoded += escape.value;
          index = escape.end;
          first = false;
          continue;
        }
        const ch = code[index];
        const valid = first ? /[A-Za-z_$]/.test(ch) : /[A-Za-z0-9_$]/.test(ch);
        if (!valid) break;
        decoded += ch;
        index++;
        first = false;
      }
      return index > start ? { end: index, decoded } : null;
    }
    function maskJSLiterals(code) {
      code = String(code || '');
      let out = '';
      let state = 'code';
      let quote = '';
      let escaped = false;
      let regexClass = false;
      let canStartRegex = true;
      let pendingControl = '';
      const parenKinds = [];
      const braceKinds = [];
      let lastToken = 'start';
      let pendingClass = false;
      let labelCandidate = false;
      const regexPrefixWords = new Set([
        'await', 'break', 'case', 'continue', 'debugger', 'default', 'delete', 'do', 'else', 'in', 'instanceof', 'new',
        'of', 'return', 'throw', 'typeof', 'void', 'yield',
      ]);
      const controlWords = new Set(['catch', 'for', 'if', 'switch', 'while', 'with']);
      for (let i = 0; i < code.length; i++) {
        const ch = code[i];
        const next = code[i + 1] || '';
        if (state === 'line') {
          if (ch === '\n' || ch === '\r') { state = 'code'; out += ch; }
          else out += ' ';
          continue;
        }
        if (state === 'block') {
          if (ch === '*' && next === '/') { out += '  '; i++; state = 'code'; }
          else out += ch === '\n' || ch === '\r' ? ch : ' ';
          continue;
        }
        if (state === 'literal') {
          if (escaped) { escaped = false; out += ch === '\n' || ch === '\r' ? ch : ' '; continue; }
          if (ch === '\\') { escaped = true; out += ' '; continue; }
          if (ch === quote) { state = 'code'; quote = ''; canStartRegex = false; lastToken = 'operand'; out += ' '; }
          else out += ch === '\n' || ch === '\r' ? ch : ' ';
          continue;
        }
        if (state === 'regex') {
          if (ch === '\n' || ch === '\r') {
            state = 'code';
            regexClass = false;
            canStartRegex = true;
            out += ch;
            continue;
          }
          if (escaped) { escaped = false; out += ' '; continue; }
          if (ch === '\\') { escaped = true; out += ' '; continue; }
          if (ch === '[') { regexClass = true; out += ' '; continue; }
          if (ch === ']' && regexClass) { regexClass = false; out += ' '; continue; }
          if (ch === '/' && !regexClass) {
            state = 'code';
            canStartRegex = false;
            lastToken = 'operand';
            out += ' ';
            while (i + 1 < code.length && /[A-Za-z]/.test(code[i + 1])) { out += ' '; i++; }
            continue;
          }
          out += ' ';
          continue;
        }
        if (ch === '/' && next === '/') { out += '  '; i++; state = 'line'; continue; }
        if (ch === '/' && next === '*') { out += '  '; i++; state = 'block'; continue; }
        if (ch === '/' && canStartRegex) {
          state = 'regex';
          regexClass = false;
          escaped = false;
          pendingControl = '';
          out += ' ';
          continue;
        }
        if (ch === '/') {
          out += ch;
          if (next === '=') { out += next; i++; }
          canStartRegex = true;
          pendingControl = '';
          lastToken = '/';
          continue;
        }
        if (ch === '\'' || ch === '"' || ch === '`') {
          state = 'literal'; quote = ch; escaped = false; pendingControl = ''; labelCandidate = false; out += '__ihtmlString__'; continue;
        }
        if (/\s/.test(ch)) { out += ch; continue; }
        const identifier = identifierAt(code, i);
        if (identifier) {
          out += identifier.decoded;
          i = identifier.end - 1;
          labelCandidate = lastToken === 'start' || lastToken === ';' || lastToken === '}block' ||
            lastToken === 'control)' || lastToken === 'label:' ||
            (lastToken === '{' && braceKinds[braceKinds.length - 1] === 'block');
          if (controlWords.has(identifier.decoded)) pendingControl = identifier.decoded;
          else pendingControl = '';
          if (identifier.decoded === 'class') pendingClass = true;
          canStartRegex = regexPrefixWords.has(identifier.decoded) || controlWords.has(identifier.decoded);
          lastToken = identifier.decoded;
          continue;
        }
        if (/[0-9]/.test(ch)) {
          let end = i + 1;
          while (end < code.length && /[A-Za-z0-9_.]/.test(code[end])) end++;
          out += code.slice(i, end);
          i = end - 1;
          canStartRegex = false;
          pendingControl = '';
          labelCandidate = false;
          lastToken = 'operand';
          continue;
        }
        if (ch === '(') {
          labelCandidate = false;
          parenKinds.push(pendingControl ? 'control' : 'normal');
          pendingControl = '';
          canStartRegex = true;
          lastToken = '(';
        } else if (ch === ')') {
          labelCandidate = false;
          const kind = parenKinds.pop();
          canStartRegex = kind === 'control';
          pendingControl = '';
          lastToken = kind === 'control' ? 'control)' : ')';
        } else if (ch === '{') {
          labelCandidate = false;
          const classBody = pendingClass && parenKinds.length === 0;
          const block = classBody || lastToken === 'start' || lastToken === ';' || lastToken === 'control)' || lastToken === ')' ||
            lastToken === '=>' || lastToken === 'else' || lastToken === 'do' || lastToken === 'try' || lastToken === 'finally' ||
            lastToken === '}block' || lastToken === 'label:';
          if (classBody) pendingClass = false;
          braceKinds.push(block ? 'block' : 'object');
          pendingControl = '';
          canStartRegex = true;
          lastToken = '{';
        } else if (ch === '}') {
          labelCandidate = false;
          const kind = braceKinds.pop();
          pendingControl = '';
          canStartRegex = kind === 'block';
          lastToken = kind === 'block' ? '}block' : '}object';
        } else if (ch === '=' && next === '>') {
          labelCandidate = false;
          out += ch;
          i++;
          pendingControl = '';
          canStartRegex = true;
          lastToken = '=>';
          out += next;
          continue;
        } else {
          pendingControl = '';
          const labelColon = ch === ':' && labelCandidate;
          labelCandidate = false;
          if (ch === ']' || ch === '.') canStartRegex = false;
          else if ((ch === '+' && next === '+') || (ch === '-' && next === '-')) {
            out += ch;
            i++;
            canStartRegex = false;
          } else canStartRegex = true;
          lastToken = labelColon ? 'label:' : ch;
        }
        out += ch;
      }
      return out;
    }
    function hasIHTMLBinding(code) {
      const masked = maskJSLiterals(code);
      const tokens = masked.match(/[A-Za-z_$][\w$]*|=>|\.\.\.|[{}()[\],;=:.\-*]/g) || [];

      const closeForOpen = new Map();
      const controlParens = new Set();
      const parenStack = [];
      const controls = new Set(['catch', 'for', 'if', 'switch', 'while', 'with']);
      for (let i = 0; i < tokens.length; i++) {
        if (tokens[i] === '(') {
          const previous = tokens[i - 1];
          const control = controls.has(previous) || (previous === 'await' && tokens[i - 2] === 'for');
          parenStack.push({ index: i, control });
        } else if (tokens[i] === ')') {
          const open = parenStack.pop();
          if (!open) continue;
          closeForOpen.set(open.index, i);
          if (open.control) controlParens.add(i);
        }
      }

      // A var conflicts with the injected module binding only in the module's
      // own var-scope. Mark function/method/class bodies as barriers while
      // leaving ordinary blocks transparent (var inside them still hoists).
      const barrierBraces = new Set();
      const keywordActsAsProperty = (index) => {
        const previous = tokens[index - 1];
        const next = tokens[index + 1];
        // Reserved words are valid IdentifierName property keys. Treating
        // `obj.class`, `{ class: 1 }`, or `{ function() {} }` as declaration
        // keywords can incorrectly turn a later ordinary block into a
        // function/class barrier and miss its module-scoped `var ihtml`.
        if (previous === '.' || next === ':') return true;
        if (next !== '(' && next !== '=' && next !== ';') return false;
        if (previous === '{' || previous === '}' || previous === ';') return true;
        if (previous === ',') {
          let depth = 0;
          for (let i = index - 1; i >= 0; i--) {
            if (tokens[i] === '}' || tokens[i] === ')' || tokens[i] === ']') depth++;
            else if (tokens[i] === '{' || tokens[i] === '(' || tokens[i] === '[') {
              if (depth) depth--;
              else return tokens[i] === '{';
            }
          }
        }
        if (previous === 'static' || previous === 'get' || previous === 'set') return true;
        if (previous === 'async') {
          const beforeAsync = tokens[index - 2];
          return beforeAsync === '{' || beforeAsync === '}' || beforeAsync === ';' || beforeAsync === ',';
        }
        return false;
      };
      const nextBodyAfterFunction = (start) => {
        let open = start + 1;
        if (tokens[open] === '*') open++;
        if (tokens[open] && !['('].includes(tokens[open])) open++;
        while (open < tokens.length && tokens[open] !== '(') open++;
        const close = closeForOpen.get(open);
        if (close != null && tokens[close + 1] === '{') barrierBraces.add(close + 1);
      };
      for (let i = 0; i < tokens.length; i++) {
        if (tokens[i] === 'function' && !keywordActsAsProperty(i)) nextBodyAfterFunction(i);
        if (tokens[i] === '=>' && tokens[i + 1] === '{') barrierBraces.add(i + 1);
        if (tokens[i] === 'class' && !keywordActsAsProperty(i)) {
          let parens = 0;
          let brackets = 0;
          for (let j = i + 1; j < tokens.length; j++) {
            if (tokens[j] === '(') parens++;
            else if (tokens[j] === ')') parens = Math.max(0, parens - 1);
            else if (tokens[j] === '[') brackets++;
            else if (tokens[j] === ']') brackets = Math.max(0, brackets - 1);
            else if (tokens[j] === '{' && parens === 0 && brackets === 0) { barrierBraces.add(j); break; }
          }
        }
      }
      const preliminaryBraces = [];
      for (let i = 0; i < tokens.length; i++) {
        if (tokens[i] === ')' && tokens[i + 1] === '{' && !controlParens.has(i) &&
            preliminaryBraces[preliminaryBraces.length - 1] === 'object') {
          // Method syntax is only valid while the nearest enclosing grammar
          // context is an object literal. `foo()\n{ var ihtml }` is instead a
          // call followed by an ordinary block, whose var still hoists.
          barrierBraces.add(i + 1);
        }
        if (tokens[i] === '{') {
          let kind = 'object';
          const previous = tokens[i - 1];
          if (barrierBraces.has(i)) kind = 'barrier';
          else if (i === 0 || controlParens.has(i - 1) ||
              [';', 'else', 'do', 'try', 'finally'].includes(previous) || previous === '}') kind = 'block';
          preliminaryBraces.push(kind);
        } else if (tokens[i] === '}') preliminaryBraces.pop();
      }

      const scopes = [];
      let braces = 0;
      let parens = 0;
      let barriers = 0;
      const braceStack = [];
      for (let i = 0; i < tokens.length; i++) {
        scopes.push({ braces, parens, barriers });
        if (tokens[i] === '{') {
          const barrier = barrierBraces.has(i);
          braceStack.push(barrier);
          braces++;
          if (barrier) barriers++;
        } else if (tokens[i] === '}') {
          const barrier = braceStack.pop();
          braces = Math.max(0, braces - 1);
          if (barrier) barriers = Math.max(0, barriers - 1);
        }
        else if (tokens[i] === '(') parens++;
        else if (tokens[i] === ')') parens = Math.max(0, parens - 1);
      }

      const matching = { '(': ')', '[': ']', '{': '}' };
      const skipBalanced = (start) => {
        const close = matching[tokens[start]];
        if (!close) return start + 1;
        const stack = [close];
        let index = start + 1;
        while (index < tokens.length && stack.length) {
          const token = tokens[index++];
          if (matching[token]) stack.push(matching[token]);
          else if (token === stack[stack.length - 1]) stack.pop();
        }
        return index;
      };
      const skipExpression = (start, stops) => {
        const stack = [];
        let index = start;
        while (index < tokens.length) {
          const token = tokens[index];
          if (!stack.length && stops.has(token)) break;
          if (matching[token]) stack.push(matching[token]);
          else if (stack.length && token === stack[stack.length - 1]) stack.pop();
          index++;
        }
        return index;
      };
      const parseBindingPattern = (start) => {
        let index = start;
        if (tokens[index] === '...') index++;
        const first = tokens[index];
        if (first !== '{' && first !== '[') {
          return { binds: first === 'ihtml', end: first ? index + 1 : index };
        }
        const close = first === '{' ? '}' : ']';
        index++;
        while (index < tokens.length && tokens[index] !== close) {
          if (tokens[index] === ',') { index++; continue; }
          if (tokens[index] === '...') {
            const rest = parseBindingPattern(index + 1);
            if (rest.binds) return rest;
            index = rest.end;
          } else if (first === '[') {
            const item = parseBindingPattern(index);
            if (item.binds) return item;
            index = item.end;
          } else if (tokens[index] === '[') {
            index = skipBalanced(index);
            if (tokens[index] === ':') index++;
            const value = parseBindingPattern(index);
            if (value.binds) return value;
            index = value.end;
          } else if (tokens[index] === ':') {
            // A string/numeric property key is masked/omitted by the lexer.
            const value = parseBindingPattern(index + 1);
            if (value.binds) return value;
            index = value.end;
          } else {
            const key = tokens[index++];
            if (tokens[index] === ':') {
              const value = parseBindingPattern(index + 1);
              if (value.binds) return value;
              index = value.end;
            } else if (key === 'ihtml') return { binds: true, end: index };
          }
          if (tokens[index] === '=') index = skipExpression(index + 1, new Set([',', close]));
          while (index < tokens.length && tokens[index] !== ',' && tokens[index] !== close) index++;
        }
        return { binds: false, end: tokens[index] === close ? index + 1 : index };
      };
      const variableDeclarationBinds = (start) => {
        let index = start + 1;
        while (index < tokens.length) {
          const pattern = parseBindingPattern(index);
          if (pattern.binds) return true;
          if (pattern.end <= index) return false;
          index = pattern.end;
          if (tokens[index] === '=') {
            index = skipExpression(index + 1, new Set([',', ';', 'in', 'of', ')']));
          }
          if (tokens[index] !== ',') return false;
          index++;
        }
        return false;
      };
      const importBinds = (start) => {
        let index = start + 1;
        if (tokens[index] === '(' || tokens[index] === '.' || tokens[index] === ';' || tokens[index] == null) return false;
        if (tokens[index] !== '{' && tokens[index] !== '*') {
          if (tokens[index] === 'ihtml') return true;
          index++;
          if (tokens[index] === ',') index++;
          else return false;
        }
        if (tokens[index] === '*') return tokens[index + 1] === 'as' && tokens[index + 2] === 'ihtml';
        if (tokens[index] !== '{') return false;
        index++;
        while (index < tokens.length && tokens[index] !== '}') {
          if (tokens[index] === ',') { index++; continue; }
          const imported = tokens[index++];
          let local = imported;
          if (tokens[index] === 'as') { local = tokens[index + 1]; index += 2; }
          if (local === 'ihtml') return true;
          while (index < tokens.length && tokens[index] !== ',' && tokens[index] !== '}') index++;
        }
        return false;
      };
      const expressionDeclarationContext = (index) => {
        let previous = tokens[index - 1];
        let before = tokens[index - 2];
        if (previous === 'async') { previous = before; before = tokens[index - 3]; }
        return new Set(['=', '(', '[', ',', ':', '=>', 'return', 'throw', 'case', 'new', 'void', 'typeof', 'delete', 'yield', 'await', '+', '-', '*', '/']).has(previous);
      };

      for (let i = 0; i < tokens.length; i++) {
        const token = tokens[i];
        const topLevel = scopes[i].braces === 0 && scopes[i].parens === 0;
        if (token === 'function' || token === 'class') {
          if (!topLevel || expressionDeclarationContext(i)) continue;
          let j = i + 1;
          if (token === 'function' && tokens[j] === '*') j++;
          if (tokens[j] === 'ihtml') return true;
          continue;
        }
        if (token === 'import' && tokens[i + 1] !== '(' && tokens[i + 1] !== '.') {
          if (!topLevel) continue;
          if (importBinds(i)) return true;
          continue;
        }
        if (token !== 'const' && token !== 'let' && token !== 'var') continue;
        // const/let 只有模块顶层才与注入声明冲突；var 即使位于裸块中
        // 也可能提升到模块作用域，所以保持保守。for(let ...) 有自己的
        // 词法作用域，不应被当作顶层声明。
        if (token === 'var' ? scopes[i].barriers > 0 : !topLevel) continue;
        if (variableDeclarationBinds(i)) return true;
      }
      return false;
    }
    function classicItemScript(code, apiExpression, itemId, privateName) {
      code = String(code || '');
      apiExpression = String(apiExpression || 'null');
      privateName = /^[A-Za-z_$][\w$]*$/.test(String(privateName || '')) ? String(privateName) : '__ihtmlInstanceAPI';
      if (privateName === 'ihtml') privateName = '__ihtmlInstanceAPI';
      // Classic Item 源码位于内部块中，因此自己的 const/let/class/function
      // ihtml 可以自然 shadow；外层始终把当前实例作为 ihtml 形参注入。
      // 报错捕获留在外层，避免源码里的 var ihtml 覆盖错误上报对象。
      return '(function(' + privateName + '){try{(function(ihtml){\n{\n' + code +
        '\n}\n})(' + privateName + ');}catch(__e){' + privateName + '.errors.report(' + JSON.stringify(String(itemId || '')) +
        ', __e && __e.message || __e, __e && __e.stack);}})(' + apiExpression + ');';
    }
    function moduleItemScript(code, apiExpression) {
      code = String(code || '');
      if (hasIHTMLBinding(code)) return code;
      return 'const ihtml = ' + String(apiExpression || 'null') + ';\n' + code;
    }
    function rectVisible(rect, viewport, clips) {
      if (!rect || !(rect.right > rect.left) || !(rect.bottom > rect.top)) return false;
      let left = rect.left; let right = rect.right; let top = rect.top; let bottom = rect.bottom;
      const apply = (clip) => {
        if (!clip) return;
        if (clip.clipX !== false) { left = Math.max(left, clip.left); right = Math.min(right, clip.right); }
        if (clip.clipY !== false) { top = Math.max(top, clip.top); bottom = Math.min(bottom, clip.bottom); }
      };
      apply({ left: 0, top: 0, right: Number(viewport && viewport.width) || 0, bottom: Number(viewport && viewport.height) || 0 });
      for (const clip of clips || []) apply(clip);
      return right > left && bottom > top;
    }
    return Object.freeze({
      createHTTPClient, storageHash, storageKey, migrateLegacyStorage, selectPrimary, itemLifecycleRecord, itemFacadeExpression, normalizeMountOptions, applyScriptNonce, scopeCSS, inspectRange,
      hasIHTMLBinding, classicItemScript, moduleItemScript, rectVisible,
    });
  })();

  // 只保护输入法确认所在的按键周期，不能用固定延时吞掉用户下一次独立回车。
  const chatComposer = {
    create() {
      let composing = false, committing = false, clearCommit = null;
      const releaseCommit = () => {
        committing = false;
        clearTimeout(clearCommit); clearCommit = null;
      };
      return {
        start() { releaseCommit(); composing = true; },
        end() {
          composing = false; committing = true;
          // Safari 可能先结束组词再派发确认键；释放按键或本轮事件结束即解除保护。
          clearTimeout(clearCommit);
          clearCommit = setTimeout(releaseCommit, 0);
        },
        release(ev) { if (ev.key === 'Enter' || ev.keyCode === 229) releaseCommit(); },
        blur() { composing = false; releaseCommit(); },
        active(ev) { return composing || ev.isComposing || ev.keyCode === 229; },
        shouldSend(ev) {
          return ev.key === 'Enter' && !ev.defaultPrevented && !ev.repeat &&
            !ev.shiftKey && !ev.altKey && !ev.ctrlKey && !ev.metaKey &&
            !this.active(ev) && !committing;
        },
      };
    },
  };

  window.__ihtmlLib = { makeT, fmt, util, chart, template, chatAgent, chatTrace, chatContent, chatComposer, itemLayout, runtime };
})();
