/* ihtml 标准库(无实例状态): i18n 字典、格式化、通用工具、零依赖 SVG 图表。
 * 由服务端与内核(ihtml.js)拼接成单个 /ihtml.js 下发; 经 window.__ihtmlLib 交接。 */
(() => {
  'use strict';

  /* ---------- i18n: 内核 chrome 文案(zh/en, 宿主可经 mount strings 覆写) ---------- */
  const I18N = {
    zh: {
      chat_entry: 'AI 助手', chat_title: '页面搭建助手', send: '发送', stop: '停止',
      new_session: '新会话', refresh_models: '刷新模型', model_placeholder: '选择已加载模型, 或直接输入模型名',
      composer_placeholder: '告诉我你想要的界面/修改…  (Enter 发送, Shift+Enter 换行)',
      welcome: '你好, 我是页面搭建助手。直接说你想要的界面或修改, 我会实时改给你看, 改动会永久保留。',
      session_reset: '新会话已开始。之前的对话上下文已清空, 页面 Item 不受影响。',
      chat_reconnected: '连接已重连, 上下文已恢复。', chat_disconnected: '连接已断开, 发送消息时会自动重连。',
      process: '分析过程', analyzing: '正在分析', thinking: '思考过程', done: '已完成',
      tool_running: '执行中', tool_pending: '待确认', tool_success: '完成', tool_error: '失败', tool_cancelled: '已取消',
      risk_low: '低风险', risk_medium: '中风险', risk_high: '高风险', risk_blocked: '已拦截',
      confirm_run: '确认执行', cancel: '取消', confirmed_running: '已确认, 正在执行。', cancelled_not_run: '已取消, 未执行。',
      confirm_expired: '确认卡已过期, 请重新让 AI 发起。', expires_in: '有效期',
      params_title: '操作参数', debug_title: '调试详情', tools_called: '调用 {n} 个工具',
      tools_failed: '{n} 个工具失败', tools_pending: '{n} 个操作待确认', running_tool: '正在执行 {name}',
      empty_title: '这里还是一张白纸', empty_chat_hint: '告诉 AI 助手你想要什么界面, 它会当场为你搭出来, 并且一直记得。',
      empty_start: '开始对话', empty_api_hint: '还没有任何 UI Item。通过 API 写入第一组片段, 页面会立即呈现:',
      load_failed: '界面加载失败', retry: '重试', page_load_failed: '页面加载失败',
      auth_expired: '登录状态已过期, 请刷新页面或重新登录。', auth_missing: '缺少用户标识',
      auth_missing_hint: '服务端无法从请求中解析出用户(默认支持 ?uid= 参数 / X-Ihtml-User 头 / ihtml_uid cookie; 嵌入模式经 mount 的 params 传入)。',
      offline: '实时更新已断开, 重连中…', readonly_note: '当前为只读访问',
      panel_title: '页面管理', panel_theme: '主题', panel_history: '历史', panel_errors: '错误',
      theme_mode: '明暗', theme_auto: '跟随系统', theme_light: '亮色', theme_dark: '暗色',
      theme_primary: '主色', theme_radius: '圆角',
      history_empty: '暂无历史快照。', rollback: '回滚到此', rollback_confirm_title: '回滚确认',
      rollback_confirm: '将恢复到该快照(当前状态也会先存为快照)。', rolled_back: '已回滚',
      errors_empty: '暂无页面错误。', items_label: '{n} 个 Item',
      safe_title: '安全模式', safe_hint: '未执行任何 UI Item。可以回滚到某个历史快照, 或让 AI 助手修复。',
      safe_exit: '退出安全模式', ai_unavailable: 'AI 服务未配置, 请联系管理员。',
      model_needed: '请先选择或输入模型名', chat_broken: 'AI 连接失败', ws_lost: '连接已断开, 请重新发送',
      item_broken: 'Item "{id}" 执行失败: {msg}', loading: '加载中…', author_user: '你',
    },
    en: {
      chat_entry: 'AI Assistant', chat_title: 'Page Builder', send: 'Send', stop: 'Stop',
      new_session: 'New chat', refresh_models: 'Refresh', model_placeholder: 'Pick a loaded model or type a name',
      composer_placeholder: 'Describe the UI you want…  (Enter to send, Shift+Enter for newline)',
      welcome: 'Hi, I build this page for you. Tell me what you want — changes apply live and persist.',
      session_reset: 'New session started. Conversation context cleared; page items are unaffected.',
      chat_reconnected: 'Reconnected; context restored.', chat_disconnected: 'Disconnected. Sending will reconnect automatically.',
      process: 'Working', analyzing: 'Analyzing', thinking: 'Thinking', done: 'Done',
      tool_running: 'Running', tool_pending: 'Confirm', tool_success: 'Done', tool_error: 'Failed', tool_cancelled: 'Cancelled',
      risk_low: 'low risk', risk_medium: 'medium risk', risk_high: 'high risk', risk_blocked: 'blocked',
      confirm_run: 'Confirm', cancel: 'Cancel', confirmed_running: 'Confirmed, running.', cancelled_not_run: 'Cancelled.',
      confirm_expired: 'Confirmation expired; ask the AI again.', expires_in: 'expires in',
      params_title: 'Parameters', debug_title: 'Debug', tools_called: '{n} tool calls',
      tools_failed: '{n} tools failed', tools_pending: '{n} pending confirmations', running_tool: 'Running {name}',
      empty_title: 'A blank canvas', empty_chat_hint: 'Tell the AI assistant what you want — it builds it live and remembers.',
      empty_start: 'Start chatting', empty_api_hint: 'No UI items yet. Put the first items via API:',
      load_failed: 'Failed to load', retry: 'Retry', page_load_failed: 'Failed to load page',
      auth_expired: 'Session expired. Refresh the page or sign in again.', auth_missing: 'Missing user identity',
      auth_missing_hint: 'The server could not resolve a user (defaults: ?uid= query / X-Ihtml-User header / ihtml_uid cookie; pass params in mount for embeds).',
      offline: 'Live updates disconnected, retrying…', readonly_note: 'Read-only access',
      panel_title: 'Page settings', panel_theme: 'Theme', panel_history: 'History', panel_errors: 'Errors',
      theme_mode: 'Mode', theme_auto: 'System', theme_light: 'Light', theme_dark: 'Dark',
      theme_primary: 'Primary', theme_radius: 'Radius',
      history_empty: 'No snapshots yet.', rollback: 'Roll back', rollback_confirm_title: 'Confirm rollback',
      rollback_confirm: 'Restore this snapshot (current state is snapshotted first).', rolled_back: 'Rolled back',
      errors_empty: 'No page errors.', items_label: '{n} items',
      safe_title: 'Safe mode', safe_hint: 'No UI items were executed. Roll back to a snapshot or let the AI fix things.',
      safe_exit: 'Exit safe mode', ai_unavailable: 'AI service is not configured. Contact the administrator.',
      model_needed: 'Pick or type a model name first', chat_broken: 'AI connection failed', ws_lost: 'Connection lost, please resend',
      item_broken: 'Item "{id}" failed: {msg}', loading: 'Loading…', author_user: 'You',
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

  /* ---------- fmt: 数字/金额/时间格式化(AI 生成代码的标准件) ---------- */
  const fmt = {
    num(n, digits) {
      if (n == null || isNaN(n)) return '-';
      return Number(n).toLocaleString('en-US', {
        minimumFractionDigits: digits || 0,
        maximumFractionDigits: digits == null ? 2 : digits,
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
        if (rest <= 0) { last = now; fn.apply(this, args); return; }
        clearTimeout(timer);
        timer = setTimeout(() => { last = Date.now(); fn.apply(this, args); }, rest);
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
    const labels = (data && data.labels) || [];
    const series = ((data && data.series) || []).filter((s) => s && s.data && s.data.length);
    const svg = svgEl('svg', { viewBox: `0 0 ${width} ${height}`, width: '100%', height });
    el.appendChild(svg);
    if (!series.length) {
      const t = svgEl('text', { x: width / 2, y: height / 2, 'text-anchor': 'middle', 'font-size': 12, fill: tokenColor(el, '--ui-text-muted', '#888') });
      t.textContent = opts.emptyText || '暂无数据';
      svg.appendChild(t);
      return;
    }
    const all = series.flatMap((s) => s.data.map(Number)).filter((v) => !isNaN(v));
    let min = Math.min(0, ...all);
    let max = Math.max(...all);
    const { lo, hi, ticks } = niceTicks(min, max, 4);
    const plotW = width - pad.l - pad.r;
    const plotH = height - pad.t - pad.b;
    const y = (v) => pad.t + plotH - ((v - lo) / (hi - lo)) * plotH;
    const n = Math.max(...series.map((s) => s.data.length), labels.length, 1);
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
          if (isNaN(Number(v))) return;
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
        const pts = s.data.map((v, i) => (isNaN(Number(v)) ? null : `${xAt(i)},${y(Number(v))}`)).filter(Boolean);
        if (opts.area !== false && series.length === 1) {
          const area = svgEl('path', {
            d: `M ${pad.l},${y(Math.max(lo, 0))} L ${pts.join(' L ')} L ${xAt(s.data.length - 1)},${y(Math.max(lo, 0))} Z`,
            fill: colors[si], opacity: 0.12,
          });
          svg.appendChild(area);
        }
        svg.appendChild(svgEl('polyline', { points: pts.join(' '), fill: 'none', stroke: colors[si], 'stroke-width': spark ? 1.5 : 2, 'stroke-linejoin': 'round' }));
        if (!spark) {
          s.data.forEach((v, i) => {
            if (isNaN(Number(v))) return;
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

  window.__ihtmlLib = { makeT, fmt, util, chart };
})();
