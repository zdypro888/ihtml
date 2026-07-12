/* ihtml 内核: 动态 UI 运行时(SDK 形态)。与 ihtml-lib.js(i18n/fmt/util/chart)
 * 拼接成单个 /ihtml.js 下发。
 *
 * 宿主方式:
 *   1. 独立页面 —— 库自带 index.html 自动挂载(hash 路由);
 *   2. 嵌入宿主 —— 声明式 <div data-ihtml data-uid="alice" data-theme="inherit"
 *      data-menu="#nav"></div> 或编程式 ihtml.mount(el, opts); 默认内存路由。
 *
 * mount(el, opts) 选项:
 *   base, params, theme(对象|'inherit'), routing('memory'|'hash'), page,
 *   chatEntry, menu(元素|选择器), locale, strings(文案覆写), safe(安全模式),
 *   auth(() => REST header token), connectionAuth(() => 短期连接票据),
 *   messageOrigins(postMessage 白名单, 默认同源),
 *   scopeStyles(嵌入时默认 true), allowExternalAssets(默认 false),
 *   captureHostErrors(嵌入时默认 false),
 *   onReady(cfg), onAuthError(), onConnection(state)
 * 实例 API 额外提供: destroy(), ready(Promise), fmt/util/chart/poll/listen/panel。 */
(() => {
  'use strict';
  const doc = document;
  const lib = window.__ihtmlLib || { makeT: () => (k) => k, fmt: {}, util: {}, chart: {} };
  let instanceSeq = 0;

  function el(tag, cls, text) {
    const node = doc.createElement(tag);
    if (tag === 'button') node.type = 'button';
    if (cls) node.className = cls;
    if (text != null) node.textContent = text;
    return node;
  }
  const clip = (s, n) => (s && s.length > n ? s.slice(0, n) : s || '');
  // 仅用于展示可复制的 POSIX shell 命令。URL 来自页面参数，必须把单引号
  // 按 shell 规则转义，避免诱导用户复制执行被篡改的命令。
  const shellQuote = (value) => "'" + String(value).replace(/'/g, "'\"'\"'") + "'";

  function createInstance(hostEl, opts) {
    const instanceID = ++instanceSeq;
    const standalone = !!opts.standalone;
    const T = lib.makeT(opts.locale || navigator.language, opts.strings);
    const templateCore = lib.template;
    const itemLayoutCore = lib.itemLayout;
    const chatTraceCore = lib.chatTrace || null;
    const chatAgentCore = lib.chatAgent || {
      normalizeModes: () => ['fast'], selectMode: () => 'fast',
      requestSnapshot: (_modes, _preferred, model, modelSelect) => ({ agentMode: 'fast', model: modelSelect ? String(model || '') : '' }),
      initialState: () => ({ mode: 'fast', plan: [], agents: {}, budget: null, manualPaused: false }),
      project: (state) => state, budgetParts: () => ({ modelCalls: 0, maxModelCalls: 0, subtasks: 0, maxSubtasks: 0 }),
    };

    /* ---------- 生命周期: 全部全局副作用登记 disposer, destroy() 时回收 ---------- */
    let destroyed = false;
    const disposers = [];
    const addDisposer = (fn) => { disposers.push(fn); return fn; };
    function on(target, ev, fn, opts2) {
      target.addEventListener(ev, fn, opts2);
      addDisposer(() => target.removeEventListener(ev, fn, opts2));
    }
    let readyResolve;
    const readyPromise = new Promise((res) => { readyResolve = res; });

    /* ---------- 容器 ---------- */
    const rootEl = el('div', 'ihtml-root');
    rootEl.classList.toggle('ihtml-standalone-root', standalone);
    const appEl = el('div', 'ihtml-app');
    rootEl.appendChild(appEl);
    hostEl.appendChild(rootEl);
    const themedRoots = [rootEl];
    let dialogSeq = 0;
    const modalClosers = new Set();
    let actionDockEl = null;
    function actionDock() {
      if (!actionDockEl) {
        actionDockEl = el('div', 'ihtml-actions');
        actionDockEl.setAttribute('role', 'group');
        actionDockEl.setAttribute('aria-label', T('page_actions'));
        rootEl.appendChild(actionDockEl);
      }
      return actionDockEl;
    }
    function syncActionDock() {
      if (!actionDockEl) return;
      const visible = Array.from(actionDockEl.children).filter((node) => node.style.display !== 'none');
      actionDockEl.classList.toggle('single', visible.length === 1);
      rootEl.classList.toggle('ihtml-has-actions', visible.length > 0);
    }
    function dialogFocusable(container) {
      return Array.from(container.querySelectorAll('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'))
        .filter((node) => !node.disabled && node.getAttribute('aria-hidden') !== 'true' && node.offsetParent !== null);
    }
    function handleDialogKey(ev, container, close) {
      if (ev.key === 'Escape') { ev.preventDefault(); close(); return; }
      if (ev.key !== 'Tab') return;
      const nodes = dialogFocusable(container);
      if (!nodes.length) { ev.preventDefault(); return; }
      const first = nodes[0];
      const last = nodes[nodes.length - 1];
      if (ev.shiftKey && doc.activeElement === first) { ev.preventDefault(); last.focus(); }
      else if (!ev.shiftKey && doc.activeElement === last) { ev.preventDefault(); first.focus(); }
    }

    /* ---------- 参数与模式 ---------- */
    const baseURL = opts.base ? new URL(String(opts.base), location.href) : new URL('.', location.href);
    if (!baseURL.pathname.endsWith('/')) baseURL.pathname += '/';
    const pageParams = standalone
      ? new URLSearchParams(location.search)
      : new URLSearchParams(opts.params || {});
    pageParams.delete('auth'); // 长期凭据不得从页面 URL 复制到每个 API/连接日志
    const hostParams = new URLSearchParams(location.search);
    const SAFE_MODE = !!opts.safe || (standalone && pageParams.get('safe') === '1') || hostParams.get('ihtml-safe') === '1';
    const routing = opts.routing || (standalone ? 'hash' : 'memory');
    const chatEntryEnabled = opts.chatEntry !== false && pageParams.get('chat') !== '0';
    if (standalone && pageParams.get('embed') === '1') doc.documentElement.classList.add('ihtml-embed');
    // 宿主控制主题: mount({theme}) / 'inherit'(探测宿主样式) / 独立页 ?theme={...}
    let hostTheme = opts.theme || null;
    if (hostTheme === 'inherit') hostTheme = probeHostTheme(hostEl);
    if (!hostTheme && standalone) {
      try { hostTheme = JSON.parse(pageParams.get('theme') || 'null'); } catch { /* 忽略 */ }
    }
    // REST 鉴权每次请求求值并走 header。浏览器原生 WS/EventSource 不能设置任意
    // header，连接凭据由独立 connectionAuth 提供短期票据，避免把长期 token
    // 隐式放进 URL；同源 cookie 会由浏览器正常携带。
    let authValue = '';
    let authEvalSeq = 0;
    async function evalAuth() {
      const seq = ++authEvalSeq;
      if (typeof opts.auth !== 'function') {
        if (seq === authEvalSeq) authValue = '';
        return '';
      }
      try {
        const value = String(await opts.auth() || '');
        if (seq === authEvalSeq) authValue = value;
        return value;
      } catch (err) {
        if (seq === authEvalSeq) authValue = '';
        throw err;
      }
    }
    async function evalConnectionAuth() {
      if (typeof opts.connectionAuth === 'function') return String(await opts.connectionAuth() || '');
      return typeof opts.connectionAuth === 'string' ? opts.connectionAuth : '';
    }

    /* ---------- 事件总线 ---------- */
    const busMap = new Map();
    const bus = {
      on(name, fn) {
        if (!busMap.has(name)) busMap.set(name, new Set());
        busMap.get(name).add(fn);
        return () => bus.off(name, fn);
      },
      off(name, fn) { busMap.get(name)?.delete(fn); },
      emit(name, data) {
        busMap.get(name)?.forEach((fn) => {
          try { fn(data); } catch (e) { errors.report('', 'bus handler for "' + name + '": ' + e, e && e.stack); }
        });
      },
    };

    /* ---------- HTTP ---------- */
    function apiURL(path, withAuth, connectionToken = '') {
      const u = new URL(path, baseURL);
      pageParams.forEach((v, k) => { if (!u.searchParams.has(k)) u.searchParams.append(k, v); });
      if (withAuth) {
        u.searchParams.delete('auth');
        if (connectionToken) u.searchParams.set('auth', connectionToken);
      }
      return u;
    }
    let authExpiredShown = false;
    let authBanner = null;
    function handleAuthExpired() {
      bus.emit('ihtml:auth-expired', {});
      if (typeof opts.onAuthError === 'function') { try { opts.onAuthError(); } catch { /* 忽略 */ } }
      if (authExpiredShown) return;
      authExpiredShown = true;
      const bar = el('div', 'ihtml-banner', T('auth_expired'));
      bar.setAttribute('role', 'alert');
      const btn = el('button', '', T('retry'));
      btn.type = 'button';
      btn.onclick = () => refreshAuthAndState().catch((err) => {
        if (!isStaleError(err)) location.reload();
      });
      bar.appendChild(btn);
      rootEl.appendChild(bar);
      authBanner = bar;
    }
    const requestControllers = new Set();
    let requestEpoch = 0;
    // 页面快照、导航提交和 SSE 增量共享同一条状态队列。这样导航请求期间
    // 到达的增量会排在导航提交之后执行，不会因“仍在旧页”而被丢弃。
    let stateQueue = Promise.resolve();
    function enqueueState(task) {
      const run = stateQueue.then(() => {
        if (destroyed) throw staleRequestError();
        return task();
      });
      stateQueue = run.catch(() => {});
      return run;
    }
    function staleRequestError() {
      const err = new Error('ihtml: stale request');
      err.stale = true;
      return err;
    }
    const isStaleError = (err) => !!(err && (err.stale || err.staleApply));
    function invalidateRequests() {
      requestEpoch++;
      authEvalSeq++;
      authValue = '';
      requestControllers.forEach((ctrl) => ctrl.abort());
      requestControllers.clear();
    }
    async function request(path, reqOpts = {}) {
      if (destroyed) throw new Error('ihtml instance destroyed');
      const epoch = requestEpoch;
      let token = '';
      try {
        token = await evalAuth();
      } catch (err) {
        if (epoch !== requestEpoch) throw staleRequestError();
        throw err;
      }
      if (epoch !== requestEpoch || destroyed) throw staleRequestError();
      const ctrl = new AbortController();
      const externalSignal = reqOpts.signal;
      const abortFromExternal = () => ctrl.abort();
      if (externalSignal) {
        if (externalSignal.aborted) ctrl.abort();
        else externalSignal.addEventListener('abort', abortFromExternal, { once: true });
      }
      requestControllers.add(ctrl);
      const timer = setTimeout(() => ctrl.abort(), reqOpts.timeout || 15000);
      try {
        const init = {
          method: reqOpts.method || 'GET',
          credentials: 'same-origin',
          signal: ctrl.signal,
          headers: {},
        };
        if (token) init.headers['X-Ihtml-User'] = token;
        if (reqOpts.body !== undefined) {
          init.headers['Content-Type'] = 'application/json';
          init.body = typeof reqOpts.body === 'string' ? reqOpts.body : JSON.stringify(reqOpts.body);
        }
        const res = await fetch(apiURL(path), init);
        const text = await res.text();
        if (epoch !== requestEpoch || destroyed) throw staleRequestError();
        let data = null;
        try { data = text ? JSON.parse(text) : null; } catch { data = text; }
        if (!res.ok) {
          if (res.status === 401 && booted) handleAuthExpired();
          const err = new Error((data && data.error) || res.status + ' ' + res.statusText);
          err.status = res.status;
          err.body = data;
          throw err;
        }
        return data;
      } catch (err) {
        if (epoch !== requestEpoch || destroyed) throw staleRequestError();
        throw err;
      } finally {
        clearTimeout(timer);
        if (externalSignal) externalSignal.removeEventListener('abort', abortFromExternal);
        requestControllers.delete(ctrl);
      }
    }
    const http = {
      url: (path) => apiURL(path).toString(),
      request,
      get: (path, o) => request(path, o),
      post: (path, body, o) => request(path, { ...o, method: 'POST', body }),
      put: (path, body, o) => request(path, { ...o, method: 'PUT', body }),
      del: (path, o) => request(path, { ...o, method: 'DELETE' }),
    };

    /* ---------- 错误收集 ---------- */
    const errorLog = [];
    let errQueue = [];
    let errTimer = 0;
    const errors = {
      list: () => errorLog.slice(),
      report(itemId, message, stack) {
        const entry = {
          item_id: String(itemId || ''),
          message: clip(String(message || 'unknown error'), 2000),
          stack: clip(String(stack || ''), 4000),
          url: standalone ? location.pathname + location.hash : '(embedded)#/' + nav.currentPage,
          time: new Date().toISOString(),
        };
        errorLog.push(entry);
        if (errorLog.length > 100) errorLog.shift();
        console.warn('[ihtml]', entry.item_id || '(page)', entry.message);
        errQueue.push(entry);
        clearTimeout(errTimer);
        errTimer = setTimeout(() => {
          const batch = errQueue.splice(0);
          if (!batch.length || destroyed) return;
          http.post('api/errors', { errors: batch }).catch(() => {});
        }, 1500);
        bus.emit('ihtml:error', entry);
      },
    };
    let runningItem = ''; // 当前正在执行的 Item, 用于错误归属与 poll/listen 作用域
    function installErrorHandlers() {
      if (!standalone && opts.captureHostErrors !== true) return;
      on(window, 'error', (ev) => {
        errors.report(runningItem, ev.message, ev.error && ev.error.stack);
      });
      on(window, 'unhandledrejection', (ev) => {
        const r = ev.reason;
        errors.report(runningItem, (r && r.message) || String(r), r && r.stack);
      });
    }

    /* ---------- 环境 ---------- */
    const envListeners = new Set();
    const env = {
      mode: 'pc',
      width: 0,
      height: 0,
      touch: matchMedia('(pointer: coarse)').matches,
      onChange(fn) {
        if (destroyed || typeof fn !== 'function') return () => {};
        envListeners.add(fn);
        return () => envListeners.delete(fn);
      },
    };
    function applyEnv() {
      if (destroyed) return;
      env.width = rootEl.clientWidth || innerWidth;
      env.height = innerHeight;
      const mode = env.width < 768 ? 'mobile' : 'pc';
      const changed = mode !== env.mode;
      env.mode = mode;
      rootEl.classList.toggle('ihtml-mobile', mode === 'mobile');
      if (changed) {
        if (shell.active) shell.sidebar(false);
        envListeners.forEach((fn) => { try { fn({ ...env }); } catch { /* 忽略 */ } });
        bus.emit('ihtml:env', { ...env });
      }
    }
    let envTimer = 0;
    const scheduleEnv = () => { clearTimeout(envTimer); envTimer = setTimeout(applyEnv, 120); };
    on(window, 'resize', scheduleEnv);
    if (typeof ResizeObserver === 'function') {
      const ro = new ResizeObserver(scheduleEnv);
      ro.observe(rootEl);
      addDisposer(() => ro.disconnect());
    }

    /* ---------- 主题 ---------- */
    const THEME_KEY = 'ihtml.theme';
    const themeDefaults = {
      mode: 'auto', primary: '', contrast: '', radius: 0, gap: 0, font: '',
      bg: '', surface: '', text: '', muted: '', border: '',
    };
    const themeVarMap = {
      primary: '--ui-primary', contrast: '--ui-primary-contrast', font: '--ui-font',
      bg: '--ui-bg', surface: '--ui-surface', text: '--ui-text', muted: '--ui-text-muted', border: '--ui-border',
    };
    function normalizeThemePatch(patch) {
      const src = patch && typeof patch === 'object' ? patch : {};
      const out = {};
      if (['auto', 'light', 'dark'].includes(src.mode)) out.mode = src.mode;
      for (const key of ['primary', 'contrast', 'bg', 'surface', 'text', 'muted', 'border']) {
        const value = src[key];
        if (value === '') out[key] = '';
        else if (typeof value === 'string' && value.length <= 160 && CSS.supports('color', value)) out[key] = value;
      }
      if (typeof src.font === 'string' && src.font.length <= 200 && !/[{};]|url\s*\(/i.test(src.font)) out.font = src.font;
      for (const [key, max] of [['radius', 32], ['gap', 48]]) {
        if (src[key] === '' || src[key] == null) continue;
        const value = Number(src[key]);
        if (Number.isFinite(value)) out[key] = Math.max(0, Math.min(max, Math.round(value)));
      }
      return out;
    }
    const themeListeners = new Set();
    let themeValue = { ...themeDefaults };
    let themePushTimer = 0;
    let themeRevision = 0;
    let themePullSeq = 0;
    const darkMedia = matchMedia('(prefers-color-scheme: dark)');

    function contrastForColor(color) {
      if (!color) return '';
      const probe = el('span');
      probe.style.color = color;
      probe.style.display = 'none';
      rootEl.appendChild(probe);
      const computed = getComputedStyle(probe).color;
      probe.remove();
      const match = /rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)/.exec(computed);
      if (!match) return '';
      const channels = match.slice(1, 4).map((value) => {
        const s = Number(value) / 255;
        return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
      });
      const luminance = 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
      const whiteContrast = 1.05 / (luminance + 0.05);
      const darkContrast = (luminance + 0.05) / 0.057;
      return whiteContrast >= darkContrast ? '#ffffff' : '#0b1020';
    }

    function themeApply() {
      const t = themeValue;
      const dark = t.mode === 'dark' || (t.mode !== 'light' && darkMedia.matches);
      if (standalone) doc.documentElement.classList.toggle('ihtml-dark', dark);
      for (const root of themedRoots) {
        root.classList.toggle('ihtml-dark', dark);
        const st = root.style;
        const setVar = (k, v) => (v ? st.setProperty(k, v) : st.removeProperty(k));
        for (const key in themeVarMap) {
          const value = key === 'contrast' && !t.contrast && t.primary ? contrastForColor(t.primary) : t[key];
          setVar(themeVarMap[key], value);
        }
        setVar('--ui-radius', t.radius ? t.radius + 'px' : '');
        setVar('--ui-gap', t.gap ? t.gap + 'px' : '');
      }
      if (lib.chart.redrawAll) setTimeout(() => lib.chart.redrawAll(), 30);
      themeListeners.forEach((fn) => { try { fn(theme.get()); } catch { /* 忽略 */ } });
    }
    const theme = {
      get: () => ({ ...themeValue, dark: rootEl.classList.contains('ihtml-dark') }),
      set(patch, o = {}) {
        if (destroyed) return;
        const normalized = normalizeThemePatch(patch);
        if (normalized.primary && !Object.prototype.hasOwnProperty.call(normalized, 'contrast')) {
          normalized.contrast = contrastForColor(normalized.primary);
        }
        themeValue = { ...themeValue, ...normalized };
        themeRevision++;
        themeApply();
        if (hostTheme) return; // 宿主控制主题时不持久化
        try { localStorage.setItem(THEME_KEY, JSON.stringify(themeValue)); } catch { /* 忽略 */ }
        if (o.persist === false) return;
        clearTimeout(themePushTimer);
        const snapshot = { ...themeValue };
        themePushTimer = setTimeout(() => {
          kv.set('theme', snapshot).catch(() => {});
        }, 400);
      },
      onChange(fn) {
        if (destroyed || typeof fn !== 'function') return () => {};
        themeListeners.add(fn);
        return () => themeListeners.delete(fn);
      },
    };
    function themeLoadLocal() {
      if (hostTheme) {
        themeValue = { ...themeDefaults, ...normalizeThemePatch(hostTheme) };
        themeApply();
        return;
      }
      try {
        const saved = JSON.parse(localStorage.getItem(THEME_KEY) || 'null');
        if (saved) themeValue = { ...themeDefaults, ...normalizeThemePatch(saved) };
      } catch { /* 忽略 */ }
      themeApply();
    }
    async function themePullRemote() {
      const seq = ++themePullSeq;
      const revision = themeRevision;
      try {
        const remote = await kv.get('theme');
        if (seq === themePullSeq && revision === themeRevision && remote && typeof remote === 'object') {
          themeValue = { ...themeDefaults, ...normalizeThemePatch(remote) };
          themeRevision++;
          themeApply();
          try { localStorage.setItem(THEME_KEY, JSON.stringify(themeValue)); } catch { /* 忽略 */ }
        }
      } catch { /* 未配置或离线 */ }
    }
    const onDarkMedia = () => { if (themeValue.mode !== 'dark' && themeValue.mode !== 'light') themeApply(); };
    darkMedia.addEventListener('change', onDarkMedia);
    addDisposer(() => darkMedia.removeEventListener('change', onDarkMedia));

    /* ---------- KV ---------- */
    const kv = {
      async get(key, def = null, requestOpts) {
        try {
          const out = await http.get('api/kv/' + encodeURIComponent(key), requestOpts);
          return out ? out.value : def;
        } catch (e) {
          if (e.status === 404) return def;
          throw e;
        }
      },
      set: (key, value) => http.put('api/kv/' + encodeURIComponent(key), { value }),
      del: (key) => http.del('api/kv/' + encodeURIComponent(key)),
    };

    /* ---------- UI: toast / confirm / modal / paginate ---------- */
    let toastBox = null;
    const ui = {
      toast(message, type = 'info', ms = 3200) {
        if (!toastBox) {
          toastBox = el('div', 'ihtml-toasts');
          toastBox.setAttribute('role', 'status');
          toastBox.setAttribute('aria-live', 'polite');
          rootEl.appendChild(toastBox);
        }
        const t = el('div', 'ihtml-toast ' + type, String(message));
        toastBox.appendChild(t);
        setTimeout(() => t.remove(), type === 'error' ? Math.max(ms, 6000) : ms);
      },
      confirm({ title = T('rollback_confirm_title'), text = '', okText = T('confirm_run'), cancelText = T('cancel') } = {}) {
        return new Promise((resolve) => {
          let decided = false;
          const handle = ui.modal({
            title,
            render(body) { if (text) body.appendChild(el('p', 'ui-muted', text)); },
            actions: [
              { label: cancelText, onClick(close) { decided = true; resolve(false); close(); } },
              { label: okText, primary: true, onClick(close) { decided = true; resolve(true); close(); } },
            ],
            onClose() { if (!decided) resolve(false); },
          });
          void handle;
        });
      },
      // modal: AI/宿主的标准弹窗, 不要手搓遮罩。render(bodyEl) 填内容。
      modal({ title = '', width = 0, render, actions = [], onClose } = {}) {
        const mask = el('div', 'ihtml-modal-mask');
        const modal = el('div', 'ihtml-modal');
        const previousFocus = doc.activeElement;
        const dialogID = 'ihtml-dialog-' + (++dialogSeq);
        modal.setAttribute('role', 'dialog');
        modal.setAttribute('aria-modal', 'true');
        if (width) modal.style.width = 'min(' + width + 'px, 94vw)';
        if (title) {
          const heading = el('h3', '', title);
          heading.id = dialogID + '-title';
          modal.setAttribute('aria-labelledby', heading.id);
          modal.appendChild(heading);
        } else modal.setAttribute('aria-label', T('dialog'));
        const body = el('div', 'body');
        modal.appendChild(body);
        if (typeof render === 'function') { try { render(body); } catch (e) { body.textContent = String(e); } }
        let closed = false;
        const close = (restoreFocus = true) => {
          if (closed) return;
          closed = true;
          modalClosers.delete(close);
          mask.remove();
          doc.removeEventListener('keydown', onKey);
          if (restoreFocus && previousFocus && previousFocus.isConnected && typeof previousFocus.focus === 'function') previousFocus.focus();
          if (typeof onClose === 'function') { try { onClose(); } catch { /* 忽略 */ } }
        };
        const onKey = (ev) => handleDialogKey(ev, modal, close);
        if (actions.length) {
          const bar = el('div', 'actions');
          for (const a of actions) {
            const btn = el('button', 'ui-btn' + (a.primary ? ' primary' : ''), a.label);
            btn.type = 'button';
            btn.onclick = () => (typeof a.onClick === 'function' ? a.onClick(close) : close());
            bar.appendChild(btn);
          }
          modal.appendChild(bar);
        }
        mask.onclick = (ev) => { if (ev.target === mask) close(); };
        doc.addEventListener('keydown', onKey);
        modalClosers.add(close);
        mask.appendChild(modal);
        rootEl.appendChild(mask);
        const focusable = dialogFocusable(modal)[0];
        if (focusable) focusable.focus();
        else { modal.tabIndex = -1; modal.focus(); }
        return { close, body };
      },
      // paginate: 分页条。onChange(page) 回调; 返回 {update}
      paginate(elOrSel, { page = 1, size = 20, total = 0, onChange } = {}) {
        const box = typeof elOrSel === 'string' ? doc.querySelector(elOrSel) : elOrSel;
        if (!box) return { update() {} };
        const state = { page: 1, size: 20, total: 0 };
        const normalizePageState = () => {
          state.size = Number.isFinite(Number(state.size)) ? Math.max(1, Math.floor(Number(state.size))) : 20;
          state.total = Number.isFinite(Number(state.total)) ? Math.max(0, Math.floor(Number(state.total))) : 0;
          state.page = Number.isFinite(Number(state.page)) ? Math.max(1, Math.floor(Number(state.page))) : 1;
        };
        Object.assign(state, { page, size, total });
        function renderBar() {
          normalizePageState();
          const pages = Math.max(1, Math.ceil(state.total / state.size));
          state.page = Math.min(Math.max(1, state.page), pages);
          box.innerHTML = '';
          box.classList.add('ui-pagination');
          const mk = (label, target, disabled, current, ariaLabel) => {
            const b = el('button', current ? 'cur' : '', label);
            b.disabled = !!disabled;
            if (ariaLabel) b.setAttribute('aria-label', ariaLabel);
            if (current) b.setAttribute('aria-current', 'page');
            if (!disabled && !current) b.onclick = () => { state.page = target; renderBar(); if (onChange) onChange(target); };
            box.appendChild(b);
          };
          mk('‹', state.page - 1, state.page <= 1, false, T('previous_page'));
          const win = Array.from(new Set([1, pages, state.page - 2, state.page - 1, state.page, state.page + 1, state.page + 2]
            .filter((p) => p >= 1 && p <= pages))).sort((a, b) => a - b);
          let last = 0;
          for (const p of win) {
            if (last && p - last > 1) box.appendChild(el('span', 'gap', '…'));
            mk(String(p), p, false, p === state.page);
            last = p;
          }
          mk('›', state.page + 1, state.page >= pages, false, T('next_page'));
        }
        renderBar();
        return {
          update(patch) { Object.assign(state, patch || {}); renderBar(); },
        };
      },
    };

    /* ---------- app-shell 插槽布局 ---------- */
    const shell = {
      active: false,
      slots: {},
      mask: null,
      burger: null,
      ensure() {
        if (typeof templateRuntime !== 'undefined' && templateRuntime.active) return;
        if (this.active) return;
        this.active = true;
        rootEl.classList.add('ihtml-has-shell');
        const header = el('header', 'ihtml-slot-header');
        const sidebar = el('aside', 'ihtml-slot-sidebar');
        sidebar.id = 'ihtml-sidebar-' + (++dialogSeq);
        sidebar.tabIndex = -1;
        const main = el('div', 'ihtml-slot-main');
        const footer = el('footer', 'ihtml-slot-footer');
        const existing = Array.from(appEl.childNodes);
        appEl.classList.remove('ihtml-grid');
        appEl.classList.add('ihtml-shell');
        appEl.append(sidebar, header, main, footer);
        existing.forEach((n) => main.appendChild(n));
        this.slots = { header, sidebar, main, footer };

        this.mask = el('div', 'ihtml-sidebar-mask');
        this.mask.onclick = () => this.sidebar(false);
        const burger = el('button', 'ihtml-hamburger', T('menu'));
        burger.type = 'button';
        burger.setAttribute('aria-label', T('menu'));
        burger.setAttribute('aria-controls', sidebar.id);
        burger.setAttribute('aria-expanded', 'false');
        burger.onclick = () => this.sidebar(!appEl.classList.contains('ihtml-sidebar-open'));
        sidebar.addEventListener('keydown', (ev) => {
          if (env.mode === 'mobile' && appEl.classList.contains('ihtml-sidebar-open')) {
            handleDialogKey(ev, sidebar, () => this.sidebar(false));
          }
        });
        this.burger = burger;
        rootEl.append(this.mask, burger);
        queueMicrotask(() => { if (!destroyed) { grid.apply(); if (typeof itemEditor !== 'undefined') itemEditor.apply(); } });
      },
      reset() {
        if (!this.active) return;
        this.sidebar(false);
        const mainChildren = this.slots.main ? Array.from(this.slots.main.childNodes) : [];
        appEl.replaceChildren(...mainChildren);
        appEl.classList.remove('ihtml-shell');
        if (this.mask) this.mask.remove();
        if (this.burger) this.burger.remove();
        this.active = false;
        this.slots = {};
        this.mask = null;
        this.burger = null;
        rootEl.classList.remove('ihtml-has-shell', 'ihtml-has-sidebar');
        queueMicrotask(() => { if (!destroyed) { grid.apply(); if (typeof itemEditor !== 'undefined') itemEditor.apply(); } });
      },
      reconcile() {
        if (typeof templateRuntime !== 'undefined' && templateRuntime.active) return;
        let hasShell = false;
        let hasSidebar = false;
        for (const rec of registry.values()) {
          if (!rec.item || rec.item.type !== 'html') continue;
          const slot = rec.item && rec.item.meta && rec.item.meta.slot;
          if (slot === 'sidebar') hasSidebar = true;
          if (slot === 'sidebar' || slot === 'header' || slot === 'footer') hasShell = true;
        }
        const hadSidebar = rootEl.classList.contains('ihtml-has-sidebar');
        if (hadSidebar && !hasSidebar) this.sidebar(false);
        if (hasShell && !this.active) this.ensure();
        rootEl.classList.toggle('ihtml-has-sidebar', hasSidebar);
        if (!hasShell) this.reset();
      },
      parentFor(slot) {
        if (typeof templateRuntime !== 'undefined' && templateRuntime.active && templateRuntime.fallback) return templateRuntime.fallback;
        if (!this.active) return appEl;
        return this.slots[slot] || this.slots.main;
      },
      sidebar(open) {
        if (!this.active) return;
        const wasOpen = rootEl.classList.contains('ihtml-sidebar-open');
        const expanded = !!open && env.mode === 'mobile' && rootEl.classList.contains('ihtml-has-sidebar');
        appEl.classList.toggle('ihtml-sidebar-open', expanded);
        rootEl.classList.toggle('ihtml-sidebar-open', expanded);
        if (this.mask) this.mask.style.display = expanded ? 'block' : 'none';
        if (this.burger) this.burger.setAttribute('aria-expanded', String(expanded));
        if (expanded) {
          queueMicrotask(() => {
            const first = dialogFocusable(this.slots.sidebar)[0] || this.slots.sidebar;
            if (first && typeof first.focus === 'function') first.focus();
          });
        } else if (wasOpen && env.mode === 'mobile' && this.burger && this.burger.offsetParent !== null) {
          this.burger.focus();
        }
      },
    };

    /* ---------- published Template 运行时 ----------
     * 普通页面严格只消费 published，不把 draft 提前上线。无 published 时完整保留旧
     * shell/grid 行为；未匹配或 unassigned Item 进入 Root 末尾的兼容 tray，不丢内容。 */
    const templateRuntime = {
      active: false,
      page: '',
      record: null,
      version: null,
      model: null,
      root: null,
      fallback: null,
      nodeHosts: new Map(),
      slotHosts: new Map(),
      slotAccepts: new Map(),
      breakpoint: 'desktop',
      loadSeq: 0,
      resizeObserver: null,

      htmlContainers() {
        const out = [];
        for (const rec of registry.values()) {
          if (!rec.item || rec.item.type !== 'html') continue;
          const container = rec.nodes.find((node) => node && node.dataset && node.dataset.ihtmlId !== undefined);
          if (container) out.push({ rec, container });
        }
        return out;
      },
      async load(page, guard, requestOpts) {
        const seq = ++this.loadSeq;
        const targetPage = String(page || '');
        try {
          const out = await http.get('api/templates/current?page=' + encodeURIComponent(targetPage), requestOpts);
          if (seq !== this.loadSeq || destroyed || (guard && !guard())) throw staleRequestError();
          this.page = targetPage;
          this.record = out && out.template || null;
          this.setPublished(this.record && this.record.published || null);
          if (typeof templateStudio !== 'undefined') templateStudio.acceptRemote(this.record, targetPage);
          return true;
        } catch (err) {
          if (isStaleError(err) || err && err.name === 'AbortError' || seq !== this.loadSeq) throw err;
          // 模板服务异常不能让原页面内容消失；首次加载继续兼容布局，已有模板继续保留。
          if (!this.active || targetPage !== this.page) this.setPublished(null);
          errors.report('', 'template runtime: ' + String(err && err.message || err), err && err.stack);
          return false;
        }
      },
      setPublished(version) {
        if (!version || typeof version !== 'object') { this.deactivate(); return; }
        this.version = JSON.parse(JSON.stringify(version));
        this.model = templateCore.fromVersion(this.version);
        this.breakpoint = templateCore.chooseBreakpoint(this.version, rootEl.clientWidth || innerWidth);
        this.rebuild();
      },
      rebuild() {
        if (!this.version || !this.model) return;
        const itemsToPlace = this.htmlContainers();
        itemsToPlace.forEach(({ container }) => container.remove());
        const wasActive = this.active;
        this.active = false;
        if (shell.active) shell.reset();
        if (this.root) this.root.remove();
        appEl.classList.remove('ihtml-grid', 'ihtml-shell');
        this.nodeHosts.clear();
        this.slotHosts.clear();
        this.slotAccepts.clear();
        const root = this.buildNode('root', null);
        root.classList.add('ihtml-template-runtime');
        const fallback = el('div', 'ihtml-runtime-fallback');
        fallback.dataset.ihtmlUnassigned = 'true';
        const rootBody = this.nodeHosts.get('root') || root;
        const rootLayout = templateCore.resolveLayout(this.model.nodes.root, this.breakpoint);
        if (rootLayout.mode === 'free') root.appendChild(fallback);
        else rootBody.appendChild(fallback);
        this.root = root;
        this.fallback = fallback;
        appEl.appendChild(root);
        appEl.classList.add('ihtml-template-runtime-active');
        this.active = true;
        if (!wasActive && grid.editing) grid.edit(false);
        itemsToPlace.sort((a, b) => Number(a.rec.item.order || 0) - Number(b.rec.item.order || 0))
          .forEach(({ rec, container }) => this.place(container, rec.item));
        if (emptyState.node && !emptyState.node.isConnected) emptyState.node = null;
        if (!registry.size) emptyState.show();
        this.ensureResizeObserver();
        queueMicrotask(() => { if (!destroyed && typeof itemEditor !== 'undefined') itemEditor.apply(); });
      },
      deactivate() {
        if (!this.active && !this.root) { this.version = null; this.model = null; return; }
        const itemsToPlace = this.htmlContainers();
        itemsToPlace.forEach(({ container }) => container.remove());
        this.active = false;
        if (this.root) this.root.remove();
        this.root = null;
        this.fallback = null;
        this.version = null;
        this.model = null;
        this.nodeHosts.clear();
        this.slotHosts.clear();
        this.slotAccepts.clear();
        appEl.classList.remove('ihtml-template-runtime-active');
        for (const { rec, container } of itemsToPlace.sort((a, b) => Number(a.rec.item.order || 0) - Number(b.rec.item.order || 0))) {
          const slot = rec.item.meta && rec.item.meta.slot || '';
          if (slot === 'sidebar' || slot === 'header' || slot === 'footer') shell.ensure();
          container.hidden = false;
          delete container.dataset.ihtmlUnassigned;
          this.clearPlacementStyle(container);
          insertContainer(container, rec.item.order || 0, shell.parentFor(slot));
        }
        shell.reconcile();
        if (typeof grid !== 'undefined') grid.apply();
        if (typeof itemEditor !== 'undefined') itemEditor.apply();
        if (emptyState.node && !emptyState.node.isConnected) emptyState.node = null;
        if (!registry.size) emptyState.show();
      },
      buildNode(id, parentLayout) {
        const node = this.model.nodes[id];
        const layout = templateCore.resolveLayout(node, this.breakpoint);
        if (node.type === 'slot') {
          const host = el('div', 'ihtml-runtime-slot');
          const slotMeta = this.model.meta.slotMeta[id] || {};
          const slotID = String(slotMeta.id || id);
          host.dataset.ihtmlSlot = slotID;
          this.applyFrame(host, templateCore.resolveFrame(node, this.breakpoint), parentLayout);
          host._ihtmlFrameMinHeight = host.style.minHeight;
          this.slotHosts.set(slotID, host);
          this.slotAccepts.set(slotID, Array.isArray(slotMeta.accepts) ? slotMeta.accepts.map((type) => String(type)) : []);
          return host;
        }
        const box = el('div', 'ihtml-runtime-node');
        box.dataset.ihtmlTemplateNode = id;
        if (id !== 'root') this.applyFrame(box, templateCore.resolveFrame(node, this.breakpoint), parentLayout);
        const children = el('div', 'ihtml-runtime-children mode-' + layout.mode);
        this.applyLayout(children, layout);
        this.nodeHosts.set(id, children);
        for (const child of this.model.children[id] || []) children.appendChild(this.buildNode(child, layout));
        box.appendChild(children);
        return box;
      },
      applyLayout(container, layout) {
        container.style.gap = Number(layout.gap || 0) + 'px';
        container.style.padding = Number(layout.padding || 0) + 'px';
        if (layout.mode === 'flow') {
          container.style.flexDirection = layout.direction || 'column';
          container.style.flexWrap = layout.wrap ? 'wrap' : 'nowrap';
          container.style.alignItems = layout.align || 'stretch';
        } else if (layout.mode === 'grid') container.style.gridTemplateColumns = 'repeat(' + Number(layout.columns || 12) + ', minmax(0, 1fr))';
        else container.style.minHeight = Number(layout.minHeight || 240) + 'px';
      },
      applyFrame(node, frame, parentLayout) {
        if (!parentLayout) return;
        if (parentLayout.mode === 'free') {
          node.style.left = Number(frame.x || 0) + 'px';
          node.style.top = Number(frame.y || 0) + 'px';
          node.style.width = Number(frame.w || 240) + 'px';
          node.style.minHeight = Number(frame.h || 120) + 'px';
        } else if (parentLayout.mode === 'grid') node.style.gridColumn = 'span ' + Math.min(Number(parentLayout.columns || 12), Number(frame.span || parentLayout.columns || 12));
        else if (frame.w) node.style.width = 'min(100%, ' + Number(frame.w) + 'px)';
        if (parentLayout.mode !== 'free' && frame.h) node.style.minHeight = Number(frame.h) + 'px';
      },
      clearPlacementStyle(container) {
        for (const key of ['position', 'left', 'top', 'width', 'height', 'zIndex', 'gridColumn', 'gridRow', 'order']) container.style[key] = '';
      },
      place(container, item) {
        if (!this.active) return false;
        this.clearPlacementStyle(container);
        const placement = templateCore.resolvePlacement(this.version, item.id, this.breakpoint);
        const slotID = placement ? (!placement.unassigned && placement.slot_id ? String(placement.slot_id) : '') : String(item.meta && item.meta.slot || '');
        const accepts = this.slotAccepts.get(slotID) || [];
        const assigned = !!slotID && this.slotHosts.has(slotID) && (!accepts.length || accepts.includes(String(item.type)));
        const target = assigned ? this.slotHosts.get(slotID) : this.fallback;
        container.hidden = !!(placement && placement.hidden);
        container.dataset.ihtmlUnassigned = String(!assigned);
        container.dataset.ihtmlBreakpoint = this.breakpoint;
        container.style.order = String(Number(placement && placement.order != null ? placement.order : item.order || 0));
        if (placement && placement.mode === 'absolute') {
          target.classList.add('has-absolute');
          container.style.position = 'absolute';
          container.style.left = Number(placement.x || 0) + 'px';
          container.style.top = Number(placement.y || 0) + 'px';
          if (placement.width) container.style.width = Number(placement.width) + 'px';
          if (placement.height) container.style.height = Number(placement.height) + 'px';
          container.style.zIndex = String(Number(placement.z_index || 0));
        } else if (assigned) {
          const bp = (this.version.breakpoints || []).find((entry) => String(entry.id) === this.breakpoint) || {};
          target.style.setProperty('--ihtml-runtime-columns', String(Number(bp.columns || 12)));
          target.style.setProperty('--ihtml-runtime-gap', Number(bp.gap || 0) + 'px');
          const start = Math.max(0, Number(placement && placement.column || 0));
          const span = Math.max(1, Number(placement && placement.column_span || bp.columns || 12));
          container.style.gridColumn = (start + 1) + ' / span ' + span;
          if (placement && (placement.row || placement.row_span)) {
            container.style.gridRow = (Math.max(0, Number(placement.row || 0)) + 1) + ' / span ' + Math.max(1, Number(placement.row_span || 1));
          }
        }
        target.appendChild(container);
        this.syncSlotVisibility(target);
        this.syncAbsoluteHeight(target);
        return true;
      },
      syncSlotVisibility(target) {
        if (!target || !target.classList.contains('ihtml-runtime-slot')) return;
        target.hidden = !Array.from(target.children).some((child) => !child.hidden);
      },
      syncAbsoluteHeight(target) {
        if (!target) return;
        let height = 0;
        let hasAbsolute = false;
        for (const child of target.children) {
          if (child.hidden || child.style.position !== 'absolute') continue;
          hasAbsolute = true;
          height = Math.max(height, Number.parseFloat(child.style.top || 0) + Number.parseFloat(child.style.height || child.offsetHeight || 0));
        }
        target.classList.toggle('has-absolute', hasAbsolute);
        const baseline = Number.parseFloat(target._ihtmlFrameMinHeight || 0) || 0;
        target.style.minHeight = hasAbsolute ? Math.max(baseline, 120, Math.ceil(height)) + 'px' : (target._ihtmlFrameMinHeight || '');
      },
      removeContainer(container) {
        const parent = container && container.parentElement;
        if (container) container.remove();
        this.syncSlotVisibility(parent);
        this.syncAbsoluteHeight(parent);
      },
      ensureResizeObserver() {
        if (this.resizeObserver || typeof ResizeObserver !== 'function') return;
        let scheduled = false;
        this.resizeObserver = new ResizeObserver(() => {
          if (scheduled || !this.active) return;
          scheduled = true;
          requestAnimationFrame(() => {
            scheduled = false;
            if (!this.active) return;
            const next = templateCore.chooseBreakpoint(this.version, rootEl.clientWidth || innerWidth);
            if (next !== this.breakpoint) { this.breakpoint = next; this.rebuild(); }
          });
        });
        this.resizeObserver.observe(rootEl);
      },
      destroy() {
        this.loadSeq++;
        if (this.resizeObserver) this.resizeObserver.disconnect();
        this.resizeObserver = null;
        this.active = false;
        this.root = null;
        this.fallback = null;
        this.nodeHosts.clear();
        this.slotHosts.clear();
        this.slotAccepts.clear();
      },
    };

    /* ---------- Item 执行引擎 ---------- */
    const registry = new Map(); // id → { item, page, nodes, teardowns }
    let applyGeneration = 0;
    function staleApplyError() {
      const err = new Error('ihtml: stale item batch');
      err.staleApply = true;
      return err;
    }

    function runTeardowns(id) {
      const rec = registry.get(id);
      if (!rec) return;
      rec.teardowns.splice(0).forEach((fn) => {
        try { fn(); } catch (e) { errors.report(id, 'teardown: ' + e, e && e.stack); }
      });
    }
    function removeItemNodes(id) {
      const rec = registry.get(id);
      if (!rec) return;
      rec.nodes.splice(0).forEach((n) => {
        if (templateRuntime.active && n && n.dataset && n.dataset.ihtmlId !== undefined) templateRuntime.removeContainer(n);
        else n.remove();
      });
      // 容器移除后回收其中的图表(ResizeObserver 等)
      if (lib.chart.gc) lib.chart.gc();
    }
    function insertContainer(container, order, parent) {
      container.dataset.ihtmlOrder = String(order);
      for (const child of parent.children) {
        if (!child.dataset || child.dataset.ihtmlId === undefined) continue;
        const o = Number(child.dataset.ihtmlOrder || 0);
        if (o > order) { parent.insertBefore(container, child); return; }
      }
      parent.appendChild(container);
    }
    function itemCSS(content) {
      const css = String(content || '');
      if (standalone || opts.scopeStyles === false) return css;
      return '@scope (.ihtml-root) {\n' + css + '\n}';
    }
    async function activateScripts(container, itemId, generation, signal) {
      for (const old of Array.from(container.querySelectorAll('script'))) {
        if (destroyed || generation !== applyGeneration || (signal && signal.aborted)) throw staleApplyError();
        if (old.src && opts.allowExternalAssets !== true) {
          errors.report(itemId, 'external script blocked by default: ' + old.src);
          old.remove();
          continue;
        }
        const script = doc.createElement('script');
        for (const attr of old.attributes) script.setAttribute(attr.name, attr.value);
        script.text = old.text || '';
        const wait = old.src
          ? new Promise((res) => {
              let settled = false;
              let timer = 0;
              let watcher = 0;
              const finish = () => {
                if (settled) return;
                settled = true;
                clearTimeout(timer);
                clearInterval(watcher);
                if (signal) signal.removeEventListener('abort', finish);
                res();
              };
              script.onload = finish;
              script.onerror = () => { errors.report(itemId, 'script load failed: ' + old.src); finish(); };
              timer = setTimeout(finish, 10000);
              watcher = setInterval(() => {
                if (destroyed || generation !== applyGeneration) finish();
              }, 50);
              if (signal && signal.aborted) finish();
              else if (signal) signal.addEventListener('abort', finish, { once: true });
            })
          : null;
        if (destroyed || generation !== applyGeneration || (signal && signal.aborted)) throw staleApplyError();
        runningItem = itemId;
        old.replaceWith(script);
        runningItem = '';
        if (wait) {
          await wait;
          if (destroyed || generation !== applyGeneration || (signal && signal.aborted)) throw staleApplyError();
        }
      }
    }

    async function applyItem(item, generation = applyGeneration, signal) {
      if (destroyed || generation !== applyGeneration || (signal && signal.aborted)) throw staleApplyError();
      const id = item.id;
      emptyState.hide();
      errorState.hide();
      runTeardowns(id);
      removeItemNodes(id);
      const rec = { item, page: item.page || '', nodes: [], teardowns: [] };
      registry.set(id, rec);
      try {
        if (item.type === 'css') {
          const style = el('style');
          style.dataset.ihtmlId = id;
          style.textContent = itemCSS(item.content);
          rootEl.appendChild(style);
          rec.nodes.push(style);
        } else if (item.type === 'html') {
          const slot = (item.meta && item.meta.slot) || '';
          if (!templateRuntime.active && (slot === 'sidebar' || slot === 'header' || slot === 'footer')) shell.ensure();
          const container = el('section', 'ihtml-enter');
          container.dataset.ihtmlId = id;
          container.innerHTML = item.content;
          for (const style of container.querySelectorAll('style')) style.textContent = itemCSS(style.textContent);
          for (const link of container.querySelectorAll('link[rel~="stylesheet"]')) {
            if (opts.allowExternalAssets !== true) {
              errors.report(id, 'external stylesheet blocked by default: ' + (link.href || 'unknown'));
              link.remove();
            }
          }
          if (!templateRuntime.place(container, item)) insertContainer(container, item.order || 0, shell.parentFor(slot));
          rec.nodes.push(container);
          setTimeout(() => container.classList.remove('ihtml-enter'), 300);
          await activateScripts(container, id, generation, signal);
          if (destroyed || generation !== applyGeneration || (signal && signal.aborted)) {
            if (registry.get(id) === rec) removeItem(id);
            throw staleApplyError();
          }
        } else if (item.type === 'js') {
          const script = el('script');
          script.dataset.ihtmlId = id;
          // 容错: 模型习惯性把 <script> 标签写进 js Item, 剥掉标签取内部代码
          let code = item.content;
          if (/^\s*<script[\s>]/i.test(code)) {
            const bodies = [];
            code.replace(/<script[^>]*>([\s\S]*?)<\/script>/gi, (_, body) => { bodies.push(body); return ''; });
            if (bodies.length) code = bodies.join('\n;\n');
          }
          const isModule = item.meta && item.meta.module === 'true';
          if (isModule) {
            script.type = 'module';
            script.text = code;
          } else {
            script.text = 'try{\n' + code + '\n}catch(__e){window.ihtml.errors.report(' + JSON.stringify(id) + ', __e && __e.message || __e, __e && __e.stack);}';
          }
          runningItem = id;
          doc.body.appendChild(script);
          runningItem = '';
          rec.nodes.push(script);
        } else {
          throw new Error('unknown item type: ' + item.type);
        }
        bus.emit('ihtml:item-applied', { id, type: item.type, page: rec.page });
        shell.reconcile();
        if (typeof templateStudio !== 'undefined') templateStudio.updateUnassigned();
      } catch (e) {
        if (e && e.staleApply) {
          if (registry.get(id) === rec) removeItem(id);
          throw e;
        }
        if (registry.get(id) !== rec) return;
        errors.report(id, e && e.message || e, e && e.stack);
        const broken = el('div', 'ihtml-item-broken', T('item_broken', { id, msg: (e && e.message) || e }));
        broken.dataset.ihtmlId = id;
        broken.dataset.ihtmlOrder = String(item.order || 0);
        (templateRuntime.active && templateRuntime.fallback || shell.parentFor('')).appendChild(broken);
        rec.nodes.push(broken);
        if (typeof templateStudio !== 'undefined') templateStudio.updateUnassigned();
      }
    }

    function removeItem(id) {
      if (!registry.has(id)) return;
      runTeardowns(id);
      removeItemNodes(id);
      registry.delete(id);
      shell.reconcile();
      if (typeof templateStudio !== 'undefined') templateStudio.updateUnassigned();
      bus.emit('ihtml:item-removed', { id });
    }
    function sortedItems(list) {
      return (list || []).slice().sort((a, b) => (a.order || 0) - (b.order || 0) || (a.id < b.id ? -1 : 1));
    }
    async function applyAll(list) {
      const generation = ++applyGeneration;
      for (const id of Array.from(registry.keys())) removeItem(id);
      const sorted = sortedItems(list);
      for (const item of sorted) {
        if (destroyed || generation !== applyGeneration) throw staleApplyError();
        await applyItem(item, generation);
      }
      if (!registry.size) emptyState.show();
      return sorted.length;
    }
    async function refreshItems(guard, requestOpts) {
      const page = nav.currentPage;
      const token = nav.navToken;
      const epoch = requestEpoch;
      const out = await http.get('api/items?page=' + encodeURIComponent(page), requestOpts);
      if (guard && !guard()) throw staleRequestError();
      if (page !== nav.currentPage || token !== nav.navToken || epoch !== requestEpoch) throw staleApplyError();
      return applyAll(out.items || []);
    }

    // poll/listen: 绑定"当前执行中的 Item"生命周期(须在 Item 脚本顶层同步调用)
    function currentItemRec(explicitId) {
      const id = explicitId || runningItem;
      return id ? registry.get(id) : null;
    }
    function poll(fn, ms, itemId) {
      const timer = setInterval(() => { try { fn(); } catch (e) { errors.report(itemId || '', 'poll: ' + e, e && e.stack); } }, Math.max(250, ms || 30000));
      const rec = currentItemRec(itemId);
      if (rec) rec.teardowns.push(() => clearInterval(timer));
      else addDisposer(() => clearInterval(timer));
      try { fn(); } catch (e) { errors.report(itemId || '', 'poll: ' + e, e && e.stack); }
      return () => clearInterval(timer);
    }
    function listen(target, ev, fn, itemId) {
      target.addEventListener(ev, fn);
      const off = () => target.removeEventListener(ev, fn);
      const rec = currentItemRec(itemId);
      if (rec) rec.teardowns.push(off);
      else addDisposer(off);
      return off;
    }

    const items = {
      list: () => Array.from(registry.values()).map((r) => ({ ...r.item })),
      apply: async (one) => {
        const generation = ++applyGeneration;
        for (const it of Array.isArray(one) ? one : [one]) await applyItem(it, generation);
        grid.apply();
        itemEditor.syncRecords();
        if (registry.size) emptyState.hide(); else emptyState.show();
      },
      remove: (id) => {
        applyGeneration++;
        removeItem(id);
        grid.apply();
        itemEditor.syncRecords();
        if (registry.size) emptyState.hide(); else emptyState.show();
      },
      refresh: () => enqueueState(() => refreshItems()),
      onTeardown(id, fn) {
        const rec = registry.get(id);
        if (rec) { rec.teardowns.push(fn); return; }
        // 未知 id: 大概率是 AI 把自己的 id 打错了——绝不能立即执行 fn(会当场清资源)
        console.warn('[ihtml] onTeardown: unknown item id "' + id + '"');
        errors.report(id, 'onTeardown called with unknown item id (typo?)');
      },
      save: (list, note) => http.put('api/items', { items: Array.isArray(list) ? list : [list], note: note || '' }),
      delete: (ids) => http.del('api/items?' + (Array.isArray(ids) ? ids : [ids]).map((i) => 'id=' + encodeURIComponent(i)).join('&')),
    };

    /* ---------- 加载反馈: 骨架屏 / 顶部进度条 / 错误态 ---------- */
    const skeleton = {
      node: null,
      show() {
        if (this.node) return;
        const box = el('div', 'ihtml-skeleton');
        for (let i = 0; i < 3; i++) box.appendChild(el('div', 'sk'));
        appEl.appendChild(box);
        this.node = box;
        appEl.setAttribute('aria-busy', 'true');
      },
      hide() {
        if (this.node) { this.node.remove(); this.node = null; }
        if (!progress.node || !progress.node.classList.contains('on')) appEl.setAttribute('aria-busy', 'false');
      },
    };
    const progress = {
      node: null, timer: 0,
      start() {
        clearTimeout(this.timer);
        if (!this.node) {
          this.node = el('div', 'ihtml-progress');
          rootEl.appendChild(this.node);
        }
        this.node.classList.add('on');
        appEl.setAttribute('aria-busy', 'true');
      },
      done() {
        if (!this.node) return;
        this.node.classList.remove('on');
        if (!skeleton.node) appEl.setAttribute('aria-busy', 'false');
        clearTimeout(this.timer);
        this.timer = setTimeout(() => { if (this.node) { this.node.remove(); this.node = null; } }, 400);
      },
    };
    const errorState = {
      node: null,
      show(message, retry) {
        this.hide();
        const box = el('div', 'ihtml-empty ihtml-error-state');
        box.setAttribute('role', 'alert');
        box.appendChild(el('h1', '', T('load_failed')));
        box.appendChild(el('p', '', String(message || '')));
        const btn = el('button', 'ui-btn primary', T('retry'));
        btn.onclick = retry;
        box.appendChild(btn);
        shell.parentFor('').appendChild(box);
        this.node = box;
      },
      hide() { if (this.node) { this.node.remove(); this.node = null; } },
    };

    /* ---------- 路由 ---------- */
    const nav = {
      pagesList: [],
      currentPage: '',
      currentParams: {},
      listeners: new Set(),
      navToken: 0,
      requestController: null,
      settledPage: null,

      pages() { return this.pagesList.map((p) => ({ ...p })); },
      current() { return { page: this.currentPage, params: { ...this.currentParams } }; },
      onChange(fn) {
        if (destroyed || typeof fn !== 'function') return () => {};
        this.listeners.add(fn);
        return () => this.listeners.delete(fn);
      },
      emitChange() {
        const state = { ...this.current(), pages: this.pages() };
        this.listeners.forEach((fn) => { try { fn(state); } catch { /* 忽略 */ } });
        bus.emit('ihtml:nav', state);
        if (typeof opts.onChange === 'function') { try { opts.onChange(state); } catch { /* 忽略 */ } }
      },
      parseHash() {
        const raw = location.hash.replace(/^#\/?/, '');
        const qIndex = raw.indexOf('?');
        const name = (qIndex >= 0 ? raw.slice(0, qIndex) : raw).trim();
        const params = {};
        if (qIndex >= 0) new URLSearchParams(raw.slice(qIndex + 1)).forEach((v, k) => { params[k] = v; });
        return { page: name, params };
      },
      hashFor(page, params) {
        let h = '#/' + (page || '');
        const qs = new URLSearchParams(params || {}).toString();
        if (qs) h += '?' + qs;
        return h;
      },
      go(page, params) {
        if (routing === 'hash') {
          const next = this.hashFor(page, params);
          if (location.hash === next) this.activate(String(page || ''), params);
          else location.hash = next;
          return;
        }
        this.activate(String(page || ''), params);
      },
      async loadPages(emit = true, guard) {
        try {
          const out = await http.get('api/pages');
          if (guard && !guard()) throw staleRequestError();
          this.pagesList = out.pages || [];
        } catch (err) {
          throw err;
        }
        if (emit) this.emitChange();
      },
      reconcile(page = this.currentPage, params = this.currentParams) {
        page = String(page || '');
        const known = this.pagesList.some((entry) => entry.name === page);
        if (this.pagesList.length && !known) page = this.pagesList[0].name;
        if (!this.pagesList.length) page = '';
        const changed = page !== this.currentPage;
        this.currentPage = page;
        this.currentParams = { ...(params || {}) };
        if (routing === 'hash') {
          const next = this.hashFor(this.currentPage, this.currentParams);
          if (location.hash !== next) history.replaceState(null, '', next);
        }
        return changed;
      },
      // 切页: stale-while-load —— 旧页保留到新页数据就绪, 顶部进度条指示
      activate(page, params) {
        if (itemEditor.editing) return itemEditor.requestClose().then((proceed) => proceed ? this.activate(page, params) : undefined);
        params = params || {};
        const token = ++this.navToken;
        if (this.requestController) this.requestController.abort();
        const requestController = new AbortController();
        this.requestController = requestController;
        if (page !== this.currentPage) applyGeneration++;
        return enqueueState(() => this.activateQueued(page, params, token, requestController)).catch((err) => {
          if (isStaleError(err) || destroyed || token !== this.navToken) return;
          progress.done();
          errors.report('', 'navigation: ' + String(err && err.message || err), err && err.stack);
          errorState.show(err && err.message || err, () => this.activate(page, params));
        }).finally(() => {
          if (this.requestController === requestController) this.requestController = null;
        });
      },
      async activateQueued(page, params, token, requestController) {
        if (token !== this.navToken || destroyed || requestController.signal.aborted) return;
        if (page === this.currentPage && page === this.settledPage) {
          progress.done();
          errorState.hide();
          this.currentParams = params;
          this.emitChange();
          return;
        }
        progress.start();
        let out = null;
        try {
          if (page) out = await http.get('api/items?page=' + encodeURIComponent(page) + '&global=0', { signal: requestController.signal });
        } catch (e) {
          if (e && e.stale) return;
          if (token !== this.navToken) return;
          progress.done();
          ui.toast(T('page_load_failed') + ': ' + e.message, 'error');
          errorState.show(e.message, () => this.activate(page, params));
          return;
        }
        if (token !== this.navToken) return;
        try {
          await templateRuntime.load(page, () => token === this.navToken && !requestController.signal.aborted, { signal: requestController.signal });
        } catch (err) {
          if (isStaleError(err) || requestController.signal.aborted || token !== this.navToken) return;
        }
        if (token !== this.navToken) return;
        errorState.hide();
        const generation = ++applyGeneration;
        for (const [id, rec] of Array.from(registry)) {
          if (rec.page) removeItem(id);
        }
        this.currentPage = page;
        this.currentParams = params;
        try {
          if (out) {
            for (const it of sortedItems(out.items || [])) {
              if (token !== this.navToken || generation !== applyGeneration) throw staleApplyError();
              await applyItem(it, generation, requestController.signal);
            }
          }
        } catch (err) {
          if (!err || !err.staleApply || token !== this.navToken) return;
          try {
            await refreshItems(
              () => token === this.navToken && !requestController.signal.aborted,
              { signal: requestController.signal },
            );
          } catch (retryErr) {
            if (isStaleError(retryErr) || token !== this.navToken) {
              if (token === this.navToken) progress.done();
              return;
            }
            progress.done();
            errorState.show(retryErr.message, () => this.activate(page, params));
            return;
          }
        }
        if (token !== this.navToken) return;
        await grid.load(() => token === this.navToken && !requestController.signal.aborted, { signal: requestController.signal });
        if (token !== this.navToken) return;
        await itemEditor.load(() => token === this.navToken && !requestController.signal.aborted, { signal: requestController.signal });
        if (token !== this.navToken) return;
        this.settledPage = page;
        progress.done();
        if (!registry.size) emptyState.show(); else emptyState.hide();
        shell.sidebar(false);
        this.emitChange();
      },
    };
    if (routing === 'hash') {
      on(window, 'hashchange', () => {
        const { page, params } = nav.parseHash();
        nav.activate(page, params);
      });
    }
    function initialRoute() {
      if (routing === 'hash') return nav.parseHash();
      return { page: opts.page || '', params: {} };
    }

    /* ---------- 拖拽排版(grid): 用户直接拖块换序/拉宽高, 布局存 KV(独立于 Item 内容) ----------
     * 数据: kv "layout.<页面名|root>" = { order: [itemId...], items: { id: {w(1-12列), h(px, 可选)} } }
     * 只作用于主区域的 html Item 容器; 移动端强制单列且不可编辑。 */
    const grid = {
      cfgs: {},
      editing: false,
      saveTimers: new Map(),
      doneBtn: null,

      key() { return 'layout.' + (nav.currentPage || 'root'); },
      normalize(value) {
        const order = Array.isArray(value && value.order)
          ? Array.from(new Set(value.order.filter((id) => typeof id === 'string').slice(0, 512)))
          : [];
        const items = Object.create(null);
        if (value && value.items && typeof value.items === 'object' && !Array.isArray(value.items)) {
          for (const [id, raw] of Object.entries(value.items)) {
            if (typeof id !== 'string' || !raw || typeof raw !== 'object' || Array.isArray(raw)) continue;
            const size = {};
            const width = Number(raw.w);
            const height = Number(raw.h);
            if (Number.isFinite(width)) size.w = Math.min(12, Math.max(1, Math.round(width)));
            if (Number.isFinite(height)) size.h = Math.min(4000, Math.max(64, Math.round(height)));
            if (Object.keys(size).length) items[id] = size;
          }
        }
        return { order, items };
      },
      cfg() {
        const k = this.key();
        if (!this.cfgs[k]) this.cfgs[k] = { order: [], items: Object.create(null) };
        return this.cfgs[k];
      },
      active() {
        const c = this.cfg();
        return this.editing || c.order.length > 0 || Object.keys(c.items).length > 0;
      },
      container() { return shell.parentFor(''); },
      widgets() {
        return Array.from(this.container().children)
          .filter((n) => n.tagName === 'SECTION' && n.dataset && n.dataset.ihtmlId !== undefined);
      },

      async load(guard, requestOpts) {
        const k = this.key();
        const token = nav.navToken;
        try {
          const v = await kv.get(k, null, requestOpts);
          if (guard && !guard()) throw staleRequestError();
          if (k !== this.key() || token !== nav.navToken) return false;
          this.cfgs[k] = this.normalize(v);
        } catch (err) {
          if ((guard && !guard()) || (err && (err.stale || err.name === 'AbortError'))) return false;
          this.cfgs[k] = { order: [], items: Object.create(null) };
        }
        this.apply();
        return true;
      },
      saveSoon() {
        const key = this.key();
        const snapshot = this.normalize(this.cfg());
        clearTimeout(this.saveTimers.get(key));
        const timer = setTimeout(() => {
          this.saveTimers.delete(key);
          if (destroyed) return;
          kv.set(key, snapshot).catch((e) => ui.toast(String(e.message || e), 'error'));
        }, 600);
        this.saveTimers.set(key, timer);
      },

      apply() {
        if (destroyed) return;
        if (templateRuntime.active) {
          this.undecorate();
          this.container().classList.remove('ihtml-grid');
          return;
        }
        const box = this.container();
        const isOn = this.active() && env.mode !== 'mobile';
        box.classList.toggle('ihtml-grid', this.active());
        const c = this.cfg();
        const pos = new Map();
        c.order.forEach((id, i) => { pos.set(id, i); });
        const ws = this.widgets().sort((a, b) => {
          const ia = pos.has(a.dataset.ihtmlId) ? pos.get(a.dataset.ihtmlId) : 1000 + Number(a.dataset.ihtmlOrder || 0);
          const ib = pos.has(b.dataset.ihtmlId) ? pos.get(b.dataset.ihtmlId) : 1000 + Number(b.dataset.ihtmlOrder || 0);
          return ia - ib;
        });
        for (const w of ws) {
          box.appendChild(w); // 按布局顺序重排 DOM
          const s = c.items[w.dataset.ihtmlId] || {};
          if (this.active()) {
            w.classList.add('ihtml-widget');
            w.style.gridColumn = 'span ' + Math.min(12, Math.max(1, s.w || 12));
            if (s.h && isOn) { w.style.height = s.h + 'px'; w.style.overflow = 'auto'; }
            else { w.style.height = ''; w.style.overflow = ''; }
          } else {
            w.classList.remove('ihtml-widget');
            w.style.gridColumn = '';
            w.style.height = '';
            w.style.overflow = '';
          }
        }
        if (this.editing) this.decorate(); else this.undecorate();
      },

      edit(on) {
        on = on !== false;
        if (on && env.mode === 'mobile') { ui.toast(T('layout_mobile'), 'warn'); return; }
        const wasEditing = this.editing;
        this.editing = on;
        rootEl.classList.toggle('ihtml-layout-editing', on);
        if (on && !this.doneBtn) {
          const btn = el('button', 'ihtml-layout-done ui-btn primary', T('layout_done'));
          btn.type = 'button';
          btn.onclick = () => this.edit(false);
          rootEl.appendChild(btn);
          this.doneBtn = btn;
        } else if (!on && this.doneBtn) {
          this.doneBtn.remove();
          this.doneBtn = null;
        }
        this.apply();
        if (!on && wasEditing) ui.toast(T('layout_saved'), 'success');
      },
      async reset() {
        const k = this.key();
        clearTimeout(this.saveTimers.get(k));
        this.saveTimers.delete(k);
        this.cfgs[k] = { order: [], items: Object.create(null) };
        try { await kv.del(k); } catch { /* 忽略 */ }
        this.apply();
      },
      setSize(id, size) {
        const c = this.cfg();
        const current = c.items[id] || {};
        const candidate = this.normalize({ items: { safe: { w: size && size.w != null ? size.w : current.w, h: size && size.h != null ? size.h : current.h } } }).items.safe || {};
        if (size && Object.prototype.hasOwnProperty.call(size, 'h') && size.h == null) delete candidate.h;
        c.items[String(id)] = candidate;
        this.apply();
        this.saveSoon();
      },
      captureOrder() {
        this.cfg().order = this.widgets().map((w) => w.dataset.ihtmlId);
        this.saveSoon();
      },

      decorate() {
        for (const w of this.widgets()) {
          if (w.querySelector(':scope > .ihtml-w-move')) continue;
          const title = registry.get(w.dataset.ihtmlId)?.item.title || w.dataset.ihtmlId;
          const move = el('button', 'ihtml-w-move', title);
          move.type = 'button';
          move.setAttribute('aria-label', T('layout_move', { title }));
          const size = el('button', 'ihtml-w-size', T('layout_resize_short'));
          size.type = 'button';
          size.setAttribute('aria-label', T('layout_resize', { title }));
          w.append(move, size);
          this.bindMove(w, move);
          this.bindSize(w, size);
        }
      },
      undecorate() {
        for (const w of this.widgets()) {
          w.querySelectorAll(':scope > .ihtml-w-move, :scope > .ihtml-w-size').forEach((n) => n.remove());
        }
      },

      bindMove(w, handle) {
        handle.addEventListener('keydown', (e) => {
          if (!['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight'].includes(e.key)) return;
          e.preventDefault();
          const widgets = this.widgets();
          const index = widgets.indexOf(w);
          const delta = e.key === 'ArrowUp' || e.key === 'ArrowLeft' ? -1 : 1;
          const targetIndex = index + delta;
          if (targetIndex < 0 || targetIndex >= widgets.length) return;
          if (delta < 0) this.container().insertBefore(w, widgets[targetIndex]);
          else this.container().insertBefore(widgets[targetIndex], w);
          this.captureOrder();
          handle.focus();
        });
        handle.addEventListener('pointerdown', (e) => {
          e.preventDefault();
          try { handle.setPointerCapture(e.pointerId); } catch { /* 合成事件无捕获 */ }
          w.classList.add('ihtml-w-dragging');
          const onMove = (ev) => {
            const siblings = this.widgets().filter((s) => s !== w);
            let target = null;
            for (const s of siblings) {
              const r = s.getBoundingClientRect();
              if (ev.clientY < r.top + r.height / 2 || (ev.clientY < r.bottom && ev.clientX < r.left + r.width / 2)) {
                target = s;
                break;
              }
            }
            if (target) this.container().insertBefore(w, target);
            else this.container().appendChild(w);
          };
          const onUp = () => {
            handle.removeEventListener('pointermove', onMove);
            handle.removeEventListener('pointerup', onUp);
            handle.removeEventListener('pointercancel', onUp);
            w.classList.remove('ihtml-w-dragging');
            this.captureOrder();
          };
          handle.addEventListener('pointermove', onMove);
          handle.addEventListener('pointerup', onUp);
          handle.addEventListener('pointercancel', onUp);
        });
      },
      bindSize(w, handle) {
        handle.addEventListener('keydown', (e) => {
          const c = this.cfg();
          const cur = c.items[w.dataset.ihtmlId] || {};
          if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
            e.preventDefault();
            cur.w = Math.min(12, Math.max(1, Number(cur.w || 12) + (e.key === 'ArrowLeft' ? -1 : 1)));
          } else if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
            e.preventDefault();
            cur.h = Math.max(64, Number(cur.h || w.getBoundingClientRect().height) + (e.key === 'ArrowUp' ? -16 : 16));
          } else if (e.key === 'Home') {
            e.preventDefault();
            delete cur.h;
          } else return;
          c.items[w.dataset.ihtmlId] = cur;
          this.apply();
          this.saveSoon();
          handle.focus();
        });
        handle.addEventListener('dblclick', () => {
          const c = this.cfg();
          if (c.items[w.dataset.ihtmlId]) { delete c.items[w.dataset.ihtmlId].h; this.apply(); this.saveSoon(); }
        });
        handle.addEventListener('pointerdown', (e) => {
          e.preventDefault();
          try { handle.setPointerCapture(e.pointerId); } catch { /* 合成事件无捕获 */ }
          const startX = e.clientX;
          const startY = e.clientY;
          const rect = w.getBoundingClientRect();
          const colW = this.container().clientWidth / 12;
          const startSize = this.cfg().items[w.dataset.ihtmlId] || {};
          let lastW = startSize.w || Math.round(rect.width / colW) || 12;
          let heightTouched = false;
          const onMove = (ev) => {
            const wCols = Math.min(12, Math.max(1, Math.round((rect.width + ev.clientX - startX) / colW)));
            if (wCols !== lastW) {
              lastW = wCols;
              w.style.gridColumn = 'span ' + wCols;
            }
            const dy = ev.clientY - startY;
            if (Math.abs(dy) > 12 || heightTouched) {
              heightTouched = true;
              const hPx = Math.max(64, Math.round(rect.height + dy));
              w.style.height = hPx + 'px';
              w.style.overflow = 'auto';
            }
          };
          const cleanup = () => {
            handle.removeEventListener('pointermove', onMove);
            handle.removeEventListener('pointerup', onUp);
            handle.removeEventListener('pointercancel', onCancel);
          };
          const onUp = (ev) => {
            cleanup();
            const size = { w: lastW };
            if (heightTouched) size.h = Math.max(64, Math.round(rect.height + ev.clientY - startY));
            else if (startSize.h) size.h = startSize.h;
            this.setSize(w.dataset.ihtmlId, size);
          };
          const onCancel = () => {
            cleanup();
            this.apply();
          };
          handle.addEventListener('pointermove', onMove);
          handle.addEventListener('pointerup', onUp);
          handle.addEventListener('pointercancel', onCancel);
        });
      },
    };

    /* ---------- 已有 Item 直接编辑器 ----------
     * 模板只负责空结构；这里在真实页面 DOM 上叠加页面级位置/尺寸/外观，不把业务
     * Item ID 写回 Template。编辑手柄全部放在独立 overlay，绝不插进 Item 内容 DOM。 */
    const itemEditor = {
      dom: null,
      state: itemLayoutCore ? itemLayoutCore.initial('') : null,
      page: '',
      persisted: false,
      editing: false,
      loading: false,
      saving: false,
      deleting: false,
      conflict: false,
      loadSeq: 0,
      originalParent: null,
      originalNext: null,
      previousFocus: null,
      managedParents: new Set(),
      contentTabStops: new Map(),
      interaction: null,
      resizeObserver: null,

      key(page = this.page) { return itemLayoutCore ? itemLayoutCore.key(page || '') : 'item-layout.root'; },
      records() {
        const out = [];
        for (const rec of registry.values()) {
          if (!rec.item || rec.item.type !== 'html') continue;
          const container = rec.nodes.find((node) => node && node.tagName === 'SECTION' && node.dataset && node.dataset.ihtmlId !== undefined);
          if (container) out.push({ id: rec.item.id, rec, container });
        }
        return out;
      },
      slotFor(entry) {
        const parent = entry.container.parentElement;
        const runtimeSlot = parent && parent.dataset && parent.dataset.ihtmlSlot;
        if (runtimeSlot) return String(runtimeSlot);
        const declared = entry.rec.item.meta && entry.rec.item.meta.slot;
        if (declared) return String(declared);
        if (parent && parent.dataset && parent.dataset.ihtmlUnassigned === 'true') return 'fallback';
        return 'main';
      },
      entry(id) { return this.records().find((entry) => entry.id === id) || null; },
      seed(raw) {
        const source = itemLayoutCore.normalize(raw, this.page);
        const doc2 = itemLayoutCore.empty(this.page);
        doc2.mode = source.mode;
        const bySlot = new Map();
        for (const entry of this.records()) {
          const id = entry.id;
          const slot = this.slotFor(entry);
          const rect = entry.container.getBoundingClientRect();
          const parentRect = entry.container.parentElement ? entry.container.parentElement.getBoundingClientRect() : { left: 0, top: 0, width: rect.width };
          const inherited = source.items[id];
          let span = 12;
          const match = /span\s+(\d+)/.exec(entry.container.style.gridColumn || '');
          if (match) {
            const runtimeColumns = Math.max(1, Number(entry.container.parentElement && entry.container.parentElement.style.getPropertyValue('--ihtml-runtime-columns')) || 12);
            span = Math.max(1, Math.min(12, Math.round((Number(match[1]) || runtimeColumns) / runtimeColumns * 12)));
          }
          else if (entry.container.classList.contains('ihtml-widget') && parentRect.width > 0) span = Math.max(1, Math.min(12, Math.round(rect.width / parentRect.width * 12) || 12));
          doc2.items[id] = inherited || {
            slot,
            flow: { span },
            free: {
              x: Math.max(0, Math.round(rect.left - parentRect.left)),
              y: Math.max(0, Math.round(rect.top - parentRect.top)),
              width: Math.max(120, Math.round(rect.width || 320)),
              height: Math.max(64, Math.round(rect.height || 180)),
              z: Math.max(0, Number(entry.container.style.zIndex || 0) || 0),
            },
            appearance: {},
          };
          doc2.items[id].slot = slot;
          if (!bySlot.has(slot)) bySlot.set(slot, []);
          bySlot.get(slot).push(entry);
        }
        for (const [slot, entries] of bySlot) {
          const wanted = source.orderBySlot[slot] || [];
          const rank = new Map(wanted.map((id, index) => [id, index]));
          entries.sort((a, b) => {
            const ai = rank.has(a.id) ? rank.get(a.id) : 1000 + Array.from(a.container.parentElement.children).indexOf(a.container);
            const bi = rank.has(b.id) ? rank.get(b.id) : 1000 + Array.from(b.container.parentElement.children).indexOf(b.container);
            return ai - bi;
          });
          doc2.orderBySlot[slot] = entries.map((entry) => entry.id);
        }
        return itemLayoutCore.normalize(doc2, this.page);
      },
      clearContainerStyle(container) {
        const styles = itemLayoutCore.styleFor(null, 'grid');
        for (const [key, value] of Object.entries(styles)) container.style[key] = value;
        delete container.dataset.ihtmlItemLayout;
      },
      clearParents() {
        for (const parent of this.managedParents) {
          parent.classList.remove('ihtml-item-layout-grid', 'ihtml-item-layout-free');
          if (Object.prototype.hasOwnProperty.call(parent, '_ihtmlItemLayoutMinHeight')) {
            parent.style.minHeight = parent._ihtmlItemLayoutMinHeight || '';
            delete parent._ihtmlItemLayoutMinHeight;
          }
          delete parent._ihtmlItemLayoutBaselineHeight;
        }
        this.managedParents.clear();
      },
      disableContentInteractions() {
        if (!this.editing) return;
        for (const node of appEl.querySelectorAll('a[href], button, input, select, textarea, [tabindex], [contenteditable]')) {
          if (!this.contentTabStops.has(node)) this.contentTabStops.set(node, node.hasAttribute('tabindex') ? node.getAttribute('tabindex') : null);
          node.setAttribute('tabindex', '-1');
        }
      },
      restoreContentInteractions() {
        for (const [node, value] of this.contentTabStops) {
          if (node.getAttribute('tabindex') !== '-1') continue;
          if (value == null) node.removeAttribute('tabindex'); else node.setAttribute('tabindex', value);
        }
        this.contentTabStops.clear();
      },
      restoreBase() {
        this.clearParents();
        const entries = this.records();
        appEl.classList.remove('ihtml-grid');
        for (const slot of Object.values(shell.slots || {})) if (slot && slot.classList) slot.classList.remove('ihtml-grid');
        for (const entry of entries) {
          this.clearContainerStyle(entry.container);
          entry.container.classList.remove('ihtml-widget');
        }
        if (templateRuntime.active) {
          entries.sort((a, b) => Number(a.rec.item.order || 0) - Number(b.rec.item.order || 0))
            .forEach((entry) => templateRuntime.place(entry.container, entry.rec.item));
        } else {
          for (const entry of entries.sort((a, b) => Number(a.rec.item.order || 0) - Number(b.rec.item.order || 0))) {
            const slot = entry.rec.item.meta && entry.rec.item.meta.slot || '';
            insertContainer(entry.container, entry.rec.item.order || 0, shell.parentFor(slot));
          }
          shell.reconcile();
        }
      },
      apply() {
        if (!itemLayoutCore || !this.state) return;
        if (this.interaction) this.cancelInteraction();
        const shouldApply = this.persisted || this.editing || this.state.dirty;
        this.restoreBase();
        if (!shouldApply) { grid.apply(); this.updateOverlay(); return; }
        const groups = new Map();
        for (const entry of this.records()) {
          if (!this.state.doc.items[entry.id] || !entry.container.parentElement) continue;
          const parent = entry.container.parentElement;
          if (!groups.has(parent)) groups.set(parent, []);
          groups.get(parent).push(entry);
        }
        for (const [parent, entries] of groups) {
          if (!Object.prototype.hasOwnProperty.call(parent, '_ihtmlItemLayoutMinHeight')) {
            parent._ihtmlItemLayoutMinHeight = parent.style.minHeight || '';
            parent._ihtmlItemLayoutBaselineHeight = Math.max(120, Math.ceil(parent.getBoundingClientRect().height || 0));
          }
          this.managedParents.add(parent);
          const slot = this.slotFor(entries[0]);
          const order = this.state.doc.orderBySlot[slot] || [];
          const rank = new Map(order.map((id, index) => [id, index]));
          entries.sort((a, b) => (rank.has(a.id) ? rank.get(a.id) : 1000) - (rank.has(b.id) ? rank.get(b.id) : 1000));
          parent.classList.add(this.state.doc.mode === 'free' ? 'ihtml-item-layout-free' : 'ihtml-item-layout-grid');
          for (const entry of entries) parent.appendChild(entry.container);
          let freeHeight = 0;
          for (const entry of entries) {
            const styles = itemLayoutCore.styleFor(this.state.doc, entry.id);
            for (const [key, value] of Object.entries(styles)) entry.container.style[key] = value;
            entry.container.dataset.ihtmlItemLayout = this.state.doc.mode;
            if (this.state.doc.mode === 'free') {
              const frame = this.state.doc.items[entry.id].free;
              freeHeight = Math.max(freeHeight, frame.y + frame.height);
            }
          }
          if (this.state.doc.mode === 'free') parent.style.minHeight = Math.max(parent._ihtmlItemLayoutBaselineHeight || 120, freeHeight + 16) + 'px';
          if (templateRuntime.active) {
            templateRuntime.syncSlotVisibility(parent);
            templateRuntime.syncAbsoluteHeight(parent);
          }
        }
        this.disableContentInteractions();
        this.updateOverlay();
      },
      async load(guard, requestOpts) {
        if (!itemLayoutCore) return false;
        const seq = ++this.loadSeq;
        const page = nav.currentPage || '';
        this.page = page;
        let remote = null;
        try { remote = await kv.get(this.key(page), null, requestOpts); } catch (err) {
          if (guard && !guard() || isStaleError(err) || err && err.name === 'AbortError') return false;
        }
        if (seq !== this.loadSeq || destroyed || guard && !guard() || page !== (nav.currentPage || '')) return false;
        if (this.editing && this.state && this.state.dirty) {
          // resync 会重建 Item 容器；保留本地文档，但必须把样式重新绑定到新节点。
          this.conflict = true; this.syncRecords(); return false;
        }
        const valid = remote && typeof remote === 'object' && String(remote.page || '') === page;
        const selectedId = this.editing && this.state ? this.state.selectedId : '';
        this.persisted = !!valid;
        this.conflict = false;
        this.state = itemLayoutCore.initial(page, this.seed(valid ? remote : itemLayoutCore.empty(page)));
        if (selectedId && this.state.doc.items[selectedId]) this.state = itemLayoutCore.reduce(this.state, { type: 'select', id: selectedId });
        this.apply();
        if (this.editing) this.render();
        return true;
      },
      build() {
        const shellEl = el('section', 'ihtml-item-editor');
        shellEl.hidden = true;
        shellEl.tabIndex = -1;
        shellEl.setAttribute('role', 'dialog');
        shellEl.setAttribute('aria-modal', 'true');
        shellEl.setAttribute('aria-label', T('item_editor_title'));

        const toolbar = el('header', 'ihtml-item-editor-toolbar');
        const title = el('strong', 'title', T('item_editor_title'));
        const modeGroup = el('div', 'ihtml-item-editor-modes');
        modeGroup.setAttribute('role', 'radiogroup');
        modeGroup.setAttribute('aria-label', T('item_editor_layout_mode'));
        const modeButtons = {};
        for (const [mode, label] of [['grid', T('item_editor_auto')], ['free', T('item_editor_free')]]) {
          const button = el('button', '', label);
          button.setAttribute('role', 'radio');
          button.onclick = () => this.setMode(mode);
          modeButtons[mode] = button;
          modeGroup.appendChild(button);
        }
        const history = el('div', 'ihtml-item-editor-history');
        const undo = el('button', '', T('template_undo'));
        const redo = el('button', '', T('template_redo'));
        undo.onclick = () => this.dispatch({ type: 'undo' });
        redo.onclick = () => this.dispatch({ type: 'redo' });
        history.append(undo, redo);
        const status = el('span', 'ihtml-item-editor-status');
        status.setAttribute('role', 'status');
        const save = el('button', 'primary', T('item_editor_save'));
        save.onclick = () => { void this.save(); };
        const done = el('button', '', T('item_editor_done'));
        done.onclick = () => { void this.finish(); };
        toolbar.append(title, modeGroup, history, status, save, done);

        const body = el('div', 'ihtml-item-editor-body');
        const layers = el('aside', 'ihtml-item-editor-layers');
        layers.setAttribute('aria-label', T('item_editor_current_items'));
        const layersHead = el('strong', '', T('item_editor_current_items'));
        const layerList = el('div', 'ihtml-item-editor-layer-list');
        layers.append(layersHead, layerList);
        const stage = el('main', 'ihtml-item-editor-stage');
        stage.setAttribute('aria-label', T('item_editor_canvas'));
        const overlay = el('div', 'ihtml-item-editor-selection');
        overlay.hidden = true;
        const chip = el('span', 'chip');
        const contextBar = el('div', 'context');
        const up = el('button', '', T('item_editor_layer_up'));
        const down = el('button', '', T('item_editor_layer_down'));
        const remove = el('button', 'danger', T('template_delete'));
        up.onclick = (ev) => { ev.stopPropagation(); this.moveLayer(-1); };
        down.onclick = (ev) => { ev.stopPropagation(); this.moveLayer(1); };
        remove.onclick = (ev) => { ev.stopPropagation(); void this.deleteSelected(); };
        contextBar.append(up, down, remove);
        const moveHandle = el('button', 'move', T('item_editor_move'));
        moveHandle.setAttribute('aria-label', T('layout_move', { title: T('item_editor_selected') }));
        const resizeHandle = el('button', 'resize', T('layout_resize_short'));
        resizeHandle.setAttribute('aria-label', T('layout_resize', { title: T('item_editor_selected') }));
        overlay.append(chip, contextBar, moveHandle, resizeHandle);
        const coordinates = el('div', 'ihtml-item-editor-coordinates');
        stage.append(overlay, coordinates);
        const inspector = el('aside', 'ihtml-item-editor-inspector');
        inspector.setAttribute('aria-label', T('item_editor_properties'));
        body.append(layers, stage, inspector);
        const live = el('div', 'ihtml-sr-only');
        live.setAttribute('aria-live', 'polite');
        shellEl.append(toolbar, body, live);
        rootEl.appendChild(shellEl);
        this.dom = { shell: shellEl, toolbar, modeButtons, undo, redo, status, save, done, layers, layerList, stage, overlay, chip, contextBar, up, down, remove, moveHandle, resizeHandle, coordinates, inspector, live };

        stage.addEventListener('pointerdown', (ev) => {
          if (overlay.contains(ev.target)) return;
          const container = ev.target && ev.target.closest && ev.target.closest('section[data-ihtml-id]');
          if (!container || !appEl.contains(container)) return;
          ev.preventDefault(); ev.stopPropagation();
          this.select(container.dataset.ihtmlId);
        }, true);
        stage.addEventListener('click', (ev) => {
          const container = ev.target && ev.target.closest && ev.target.closest('section[data-ihtml-id]');
          if (container) { ev.preventDefault(); ev.stopPropagation(); }
        }, true);
        stage.addEventListener('scroll', () => this.updateOverlay(), { passive: true });
        moveHandle.addEventListener('pointerdown', (ev) => this.startMove(ev));
        resizeHandle.addEventListener('pointerdown', (ev) => this.startResize(ev));
        moveHandle.addEventListener('keydown', (ev) => this.moveByKeyboard(ev));
        resizeHandle.addEventListener('keydown', (ev) => this.resizeByKeyboard(ev));
        shellEl.addEventListener('keydown', (ev) => {
          if ((ev.metaKey || ev.ctrlKey) && ev.key.toLowerCase() === 'z') {
            ev.preventDefault(); this.dispatch({ type: ev.shiftKey ? 'redo' : 'undo' }); return;
          }
          if (ev.key === 'Escape') {
            ev.preventDefault();
            if (this.interaction) this.cancelInteraction(); else void this.requestClose();
          }
          else if (ev.key === 'Tab') handleDialogKey(ev, shellEl, () => {});
        });
      },
      ensureEditingState() {
        const seeded = this.seed(this.persisted && this.state ? this.state.doc : itemLayoutCore.empty(this.page));
        const selected = this.state && seeded.items[this.state.selectedId] ? this.state.selectedId : Object.keys(seeded.items)[0] || '';
        this.state = itemLayoutCore.initial(this.page, seeded);
        this.state = itemLayoutCore.reduce(this.state, { type: 'select', id: selected });
      },
      async open() {
        if (!itemLayoutCore || readOnly) return;
        if (this.editing) { this.render(); if (this.dom) this.dom.shell.focus(); return; }
        if (!this.records().length) { ui.toast(T('item_editor_empty'), 'warn'); return; }
        panel.close(); chat.close(); grid.edit(false); templateStudio.close();
        if (!this.dom) this.build();
        const page = nav.currentPage || '';
        if (this.page !== page || !this.state) await this.load();
        this.page = page;
        this.ensureEditingState();
        this.previousFocus = doc.activeElement;
        this.originalParent = appEl.parentNode;
        this.originalNext = appEl.nextSibling;
        this.dom.stage.insertBefore(appEl, this.dom.overlay);
        this.dom.shell.hidden = false;
        this.editing = true;
        rootEl.classList.add('ihtml-item-editing');
        this.apply();
        this.render();
        if (!this.resizeObserver && typeof ResizeObserver === 'function') {
          this.resizeObserver = new ResizeObserver(() => this.updateOverlay());
          this.resizeObserver.observe(appEl);
        }
        this.dom.shell.focus();
      },
      close(discard) {
        if (!this.dom || this.dom.shell.hidden) return;
        if (this.interaction) this.cancelInteraction();
        if (discard && this.state) this.state = itemLayoutCore.reduce(this.state, { type: 'replace', page: this.page, doc: this.state.baseline });
        this.restoreContentInteractions();
        this.editing = false;
        rootEl.classList.remove('ihtml-item-editing');
        if (this.originalParent) {
          if (this.originalNext && this.originalNext.parentNode === this.originalParent) this.originalParent.insertBefore(appEl, this.originalNext);
          else this.originalParent.appendChild(appEl);
        }
        this.dom.shell.hidden = true;
        this.dom.overlay.hidden = true;
        this.originalParent = null; this.originalNext = null;
        this.apply();
        if (this.previousFocus && this.previousFocus.isConnected && typeof this.previousFocus.focus === 'function') this.previousFocus.focus();
        this.previousFocus = null;
      },
      async requestClose() {
        if (this.state && this.state.dirty) {
          const leave = await ui.confirm({ title: T('item_editor_unsaved'), text: T('item_editor_exit_hint'), okText: T('item_editor_discard') });
          if (!leave) return false;
          this.close(true); return true;
        }
        this.close(false); return true;
      },
      async finish() {
        if (this.state && this.state.dirty && !(await this.save())) return;
        this.close(false);
      },
      dispatch(action) {
        if (!this.state || this.loading || this.saving || this.deleting || this.interaction) return;
        const next = itemLayoutCore.reduce(this.state, action);
        if (next === this.state) return;
        this.state = next;
        this.conflict = false;
        this.apply(); this.render();
      },
      select(id) {
        if (this.interaction || !this.state || !this.state.doc.items[id]) return;
        this.state = itemLayoutCore.reduce(this.state, { type: 'select', id });
        this.render(); this.updateOverlay();
      },
      setMode(mode) {
        if (this.interaction) this.cancelInteraction();
        if (!this.state || this.state.doc.mode === mode) return;
        const next = itemLayoutCore.normalize(this.state.doc, this.page);
        if (mode === 'free') {
          for (const entry of this.records()) {
            if (!next.items[entry.id] || !entry.container.parentElement) continue;
            const rect = entry.container.getBoundingClientRect();
            const parentRect = entry.container.parentElement.getBoundingClientRect();
            next.items[entry.id].free = {
              x: Math.max(0, Math.round(rect.left - parentRect.left)),
              y: Math.max(0, Math.round(rect.top - parentRect.top)),
              width: Math.max(120, Math.round(rect.width)),
              height: Math.max(64, Math.round(rect.height)),
              z: next.items[entry.id].free.z || 0,
            };
          }
        }
        next.mode = mode;
        this.dispatch({ type: 'replaceDoc', doc: next, selectedId: this.state.selectedId });
      },
      async save() {
        if (!this.state || !this.state.dirty || this.saving) return !this.state || !this.state.dirty;
        const page = this.page; const key = this.key(page); const snapshot = itemLayoutCore.normalize(this.state.doc, page);
        this.saving = true; this.render();
        try {
          const latest = await kv.get(key, null);
          if (page !== this.page) return false;
          const remote = latest && typeof latest === 'object' && String(latest.page || '') === page ? itemLayoutCore.normalize(latest, page) : null;
          const expected = this.persisted ? itemLayoutCore.normalize(this.state.baseline, page) : null;
          if (JSON.stringify(remote) !== JSON.stringify(expected)) {
            this.conflict = true; ui.toast(T('item_editor_conflict'), 'warn'); return false;
          }
          await kv.set(key, snapshot);
          if (page !== this.page) return false;
          this.persisted = true; this.conflict = false;
          this.state = itemLayoutCore.reduce(this.state, { type: 'markSaved', doc: snapshot });
          ui.toast(T('item_editor_saved'), 'success');
          return true;
        } catch (err) {
          ui.toast(T('item_editor_save_failed') + ': ' + String(err && err.message || err), 'error');
          return false;
        } finally {
          this.saving = false; this.render();
        }
      },
      async remoteChanged() {
        if (this.saving) return;
        if (this.state && this.state.dirty) { this.conflict = true; this.render(); return; }
        await this.load();
      },
      moveLayer(delta) {
        if (this.interaction || !this.state || !this.state.selectedId) return;
        const id = this.state.selectedId; const item = this.state.doc.items[id];
        if (!item) return;
        if (this.state.doc.mode === 'free') {
          this.dispatch({ type: 'patch', id, section: 'free', patch: { z: Math.max(0, Math.min(999, item.free.z + (delta < 0 ? 1 : -1))) } });
          return;
        }
        const list = (this.state.doc.orderBySlot[item.slot] || []).slice();
        const index = list.indexOf(id); const next = index + (delta < 0 ? -1 : 1);
        if (index < 0 || next < 0 || next >= list.length) return;
        list.splice(index, 1); list.splice(next, 0, id);
        this.dispatch({ type: 'reorder', slot: item.slot, ids: list });
      },
      async deleteSelected() {
        if (this.interaction || !this.state || !this.state.selectedId || this.deleting) return;
        const id = this.state.selectedId; const entry = this.entry(id);
        if (!entry) return;
        const global = !entry.rec.item.page;
        const ok = await ui.confirm({
          title: T('item_editor_delete_title'),
          text: global ? T('item_editor_delete_global') : T('item_editor_delete_hint'),
          okText: T('item_editor_delete'),
        });
        if (!ok) return;
        const pendingState = itemLayoutCore.reduce(this.state, { type: 'requestRemove', id });
        const removedState = itemLayoutCore.reduce(pendingState, { type: 'confirmRemove', id });
        this.state = pendingState; this.deleting = true; this.render();
        try {
          await items.delete(id);
          this.state = removedState;
          removeItem(id);
          this.apply(); this.render();
          const saved = !this.state.dirty || await this.save();
          if (saved) {
            this.state = itemLayoutCore.initial(this.page, this.state.doc);
            this.apply(); this.render();
          } else this.state = Object.assign({}, this.state, { past: [], future: [], selectedId: '' });
          ui.toast(T('item_editor_deleted'), 'success');
        } catch (err) {
          this.state = itemLayoutCore.reduce(this.state, { type: 'cancelRemove' });
          ui.toast(T('item_editor_delete_failed') + ': ' + String(err && err.message || err), 'error');
          this.render();
        } finally {
          this.deleting = false; this.render();
        }
      },
      restoreInlineStyle(node, value) {
        if (!node) return;
        if (value == null) node.removeAttribute('style'); else node.setAttribute('style', value);
      },
      reducedMotion() { return !!(window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches); },
      settleEntry(id) {
        const entry = this.entry(id);
        if (!entry || this.reducedMotion() || typeof entry.container.animate !== 'function') return;
        entry.container.animate([
          { transform: 'scale(.992)', filter: 'brightness(1.08)' },
          { transform: 'scale(1)', filter: 'brightness(1)' },
        ], { duration: 170, easing: 'cubic-bezier(.2,.8,.2,1)' });
      },
      animateGridShift(entries, before) {
        const animations = [];
        if (this.reducedMotion()) return animations;
        for (const entry of entries) {
          const first = before.get(entry.id); if (!first || typeof entry.container.animate !== 'function') continue;
          const last = entry.container.getBoundingClientRect();
          const dx = first.left - last.left; const dy = first.top - last.top;
          if (Math.abs(dx) < 1 && Math.abs(dy) < 1) continue;
          animations.push(entry.container.animate([
            { transform: 'translate3d(' + dx + 'px,' + dy + 'px,0)' },
            { transform: 'translate3d(0,0,0)' },
          ], { duration: 170, easing: 'cubic-bezier(.2,.8,.2,1)' }));
        }
        return animations;
      },
      dragRows(entries) {
        const measured = entries.map((entry, index) => ({ entry, index, rect: entry.container.getBoundingClientRect() }))
          .sort((a, b) => a.rect.top - b.rect.top || a.rect.left - b.rect.left);
        const rows = [];
        for (const measuredEntry of measured) {
          let row = rows.find((candidate) => measuredEntry.rect.top < candidate.bottom - 4 && measuredEntry.rect.bottom > candidate.top + 4);
          if (!row) { row = { top: measuredEntry.rect.top, bottom: measuredEntry.rect.bottom, items: [] }; rows.push(row); }
          row.top = Math.min(row.top, measuredEntry.rect.top); row.bottom = Math.max(row.bottom, measuredEntry.rect.bottom);
          row.items.push(measuredEntry);
        }
        rows.sort((a, b) => a.top - b.top);
        for (const row of rows) row.items.sort((a, b) => a.rect.left - b.rect.left);
        return rows;
      },
      dragTargetIndex(rows, clientX, clientY, total) {
        if (!rows.length) return 0;
        for (let rowIndex = 0; rowIndex < rows.length; rowIndex++) {
          const row = rows[rowIndex];
          if (clientY < row.top) return row.items[0].index;
          if (clientY <= row.bottom) {
            for (const item of row.items) if (clientX < item.rect.left + item.rect.width / 2) return item.index;
            return Math.min(total, row.items[row.items.length - 1].index + 1);
          }
          const next = rows[rowIndex + 1];
          if (next && clientY < (row.bottom + next.top) / 2) return Math.min(total, row.items[row.items.length - 1].index + 1);
        }
        return total;
      },
      gridTrackMetrics(parent) {
        if (!parent) return { gap: 0, trackWidth: 1, paddingLeft: 0 };
        const parentStyle = getComputedStyle(parent);
        const gap = Number.parseFloat(parentStyle.columnGap || parentStyle.gap || 0) || 0;
        const paddingLeft = Number.parseFloat(parentStyle.paddingLeft) || 0;
        const usableWidth = Math.max(1, parent.clientWidth - paddingLeft - (Number.parseFloat(parentStyle.paddingRight) || 0));
        return { gap, paddingLeft, trackWidth: Math.max(1, (usableWidth - gap * 11) / 12) };
      },
      contentSafeGridSpan(entry, requested, metrics) {
        let span = Math.max(1, Math.min(12, Math.round(Number(requested) || 1)));
        if (!entry || !entry.container || !entry.container.isConnected || !entry.container.parentElement) return span;
        const container = entry.container; const originalGridColumn = container.style.gridColumn;
        const sizing = metrics || this.gridTrackMetrics(container.parentElement);
        try {
          for (let guard = 0; guard < 12; guard++) {
            container.style.gridColumn = 'span ' + span;
            const clientWidth = container.clientWidth; const scrollWidth = container.scrollWidth;
            if (!clientWidth || scrollWidth <= clientWidth + 2 || span >= 12) break;
            const required = Math.ceil((scrollWidth + sizing.gap) / (sizing.trackWidth + sizing.gap));
            span = Math.min(12, Math.max(span + 1, required));
          }
        } finally {
          container.style.gridColumn = originalGridColumn;
        }
        return span;
      },
      beginInteraction(ev, session) {
        if (this.interaction) this.cancelInteraction();
        const handle = ev.currentTarget; const pointerId = ev.pointerId; const stage = this.dom.stage;
        session.handle = handle; session.pointerId = pointerId; session.raf = 0; session.active = false;
        session.startClientX = ev.clientX; session.startClientY = ev.clientY;
        session.threshold = ev.pointerType === 'touch' ? 8 : 4;
        session.latest = { clientX: ev.clientX, clientY: ev.clientY, altKey: ev.altKey };
        session.startScrollLeft = stage.scrollLeft; session.startScrollTop = stage.scrollTop;
        session.stageRect = stage.getBoundingClientRect();
        const movedEnough = (point) => Math.hypot(point.clientX - session.startClientX, point.clientY - session.startClientY) >= session.threshold;
        const activate = () => {
          if (session.active || this.interaction !== session) return;
          session.active = true;
          rootEl.classList.add('ihtml-item-dragging', session.kind === 'move' ? 'ihtml-item-moving' : 'ihtml-item-resizing');
          if (session.onActivate) session.onActivate();
        };
        const tick = () => {
          if (this.interaction !== session) return;
          session.raf = 0;
          const point = session.latest; const edge = 36; const rect = session.stageRect;
          if (!session.active) {
            if (!movedEnough(point)) return;
            activate();
          }
          let scrollX = 0; let scrollY = 0;
          if (session.autoScroll !== false) {
            if (point.clientX < rect.left + edge) scrollX = -Math.ceil((rect.left + edge - point.clientX) / 3);
            else if (point.clientX > rect.right - edge) scrollX = Math.ceil((point.clientX - (rect.right - edge)) / 3);
            if (point.clientY < rect.top + edge) scrollY = -Math.ceil((rect.top + edge - point.clientY) / 3);
            else if (point.clientY > rect.bottom - edge) scrollY = Math.ceil((point.clientY - (rect.bottom - edge)) / 3);
          }
          scrollX = Math.max(-18, Math.min(18, scrollX)); scrollY = Math.max(-18, Math.min(18, scrollY));
          const oldLeft = stage.scrollLeft; const oldTop = stage.scrollTop;
          if (scrollX || scrollY) stage.scrollBy(scrollX, scrollY);
          session.onFrame(point);
          if ((stage.scrollLeft !== oldLeft || stage.scrollTop !== oldTop) && this.interaction === session) session.raf = requestAnimationFrame(tick);
        };
        const schedule = (moveEv) => {
          if (moveEv.pointerId !== pointerId || this.interaction !== session) return;
          moveEv.preventDefault();
          session.latest = { clientX: moveEv.clientX, clientY: moveEv.clientY, altKey: moveEv.altKey };
          if (!session.raf) session.raf = requestAnimationFrame(tick);
        };
        const finish = (commit, finishEv) => {
          if (this.interaction !== session || finishEv && finishEv.pointerId !== pointerId) return;
          if (commit && finishEv && Number.isFinite(finishEv.clientX) && Number.isFinite(finishEv.clientY)) {
            session.latest = { clientX: finishEv.clientX, clientY: finishEv.clientY, altKey: finishEv.altKey };
          }
          if (session.raf) { cancelAnimationFrame(session.raf); session.raf = 0; }
          if (commit && !session.active && movedEnough(session.latest)) activate();
          if (commit && session.active) session.onFrame(session.latest);
          const shouldCommit = !!(commit && session.active);
          if (!shouldCommit && session.active) {
            stage.scrollLeft = session.startScrollLeft; stage.scrollTop = session.startScrollTop;
          }
          this.interaction = null;
          window.removeEventListener('pointermove', schedule, true);
          window.removeEventListener('pointerup', onUp, true);
          window.removeEventListener('pointercancel', onCancel, true);
          window.removeEventListener('blur', onBlur, true);
          handle.removeEventListener('lostpointercapture', onLostCapture);
          rootEl.classList.remove('ihtml-item-dragging', 'ihtml-item-moving', 'ihtml-item-resizing');
          if (handle.releasePointerCapture) { try { handle.releasePointerCapture(pointerId); } catch { /* capture already gone */ } }
          session.onFinish(shouldCommit);
          this.renderToolbar();
        };
        const onUp = (upEv) => finish(true, upEv);
        const onCancel = (cancelEv) => finish(false, cancelEv);
        const onBlur = () => finish(false);
        const onLostCapture = () => { if (this.interaction === session) finish(false); };
        session.finish = finish;
        this.interaction = session;
        if (handle.setPointerCapture) { try { handle.setPointerCapture(pointerId); } catch { /* window listeners remain the fallback */ } }
        window.addEventListener('pointermove', schedule, { capture: true, passive: false });
        window.addEventListener('pointerup', onUp, true);
        window.addEventListener('pointercancel', onCancel, true);
        window.addEventListener('blur', onBlur, true);
        handle.addEventListener('lostpointercapture', onLostCapture);
        this.renderToolbar();
        schedule(ev);
      },
      cancelInteraction() {
        if (this.interaction && this.interaction.finish) this.interaction.finish(false);
      },
      startMove(ev) {
        if (!this.editing || !this.state || !this.state.selectedId || this.loading || this.saving || this.deleting || ev.button !== 0 || ev.isPrimary === false || this.interaction) return;
        ev.preventDefault(); ev.stopPropagation();
        if (window.innerWidth < 768) { ui.toast(T('item_editor_mobile_drag'), 'warn'); return; }
        const id = this.state.selectedId; const entry = this.entry(id); const item = this.state.doc.items[id];
        if (!entry || !item) return;
        const stage = this.dom.stage; const parent = entry.container.parentElement;
        const originalStyle = entry.container.getAttribute('style'); const originalParentHeight = parent.style.minHeight;
        const startX = ev.clientX; const startY = ev.clientY; const startRect = entry.container.getBoundingClientRect();
        if (this.state.doc.mode === 'free') {
          const original = { ...item.free }; let nextX = original.x; let nextY = original.y; let moved = false;
          this.beginInteraction(ev, {
            kind: 'move',
            onActivate: () => {
              entry.container.classList.add('ihtml-item-drag-ghost'); entry.container.style.zIndex = '10002';
              this.dom.overlay.classList.add('is-moving');
            },
            onFrame: (point) => {
              const scrollDX = stage.scrollLeft - this.interaction.startScrollLeft;
              const scrollDY = stage.scrollTop - this.interaction.startScrollTop;
              const deltaX = point.clientX - startX + scrollDX; const deltaY = point.clientY - startY + scrollDY;
              const snapDelta = (value) => point.altKey ? Math.round(value) : Math.round(value / 8) * 8;
              nextX = Math.max(0, Math.min(10000, original.x + snapDelta(deltaX)));
              nextY = Math.max(0, Math.min(10000, original.y + snapDelta(deltaY)));
              moved = nextX !== original.x || nextY !== original.y;
              const tx = nextX - original.x; const ty = nextY - original.y;
              entry.container.style.transform = 'translate3d(' + tx + 'px,' + ty + 'px,0)';
              this.dom.overlay.style.transform = 'translate3d(' + tx + 'px,' + ty + 'px,0)';
              parent.style.minHeight = Math.max(Number.parseFloat(originalParentHeight || 0) || 0, nextY + original.height + 16) + 'px';
              this.dom.coordinates.textContent = 'X ' + nextX + '  Y ' + nextY + '  W ' + original.width + '  H ' + original.height + (point.altKey ? '' : '  ·  ' + T('item_editor_snap', { value: 8 }));
            },
            onFinish: (commit) => {
              this.restoreInlineStyle(entry.container, originalStyle); parent.style.minHeight = originalParentHeight;
              entry.container.classList.remove('ihtml-item-drag-ghost'); this.dom.overlay.classList.remove('is-moving'); this.dom.overlay.style.transform = '';
              if (commit && moved) { this.dispatch({ type: 'patch', id, section: 'free', patch: { x: nextX, y: nextY } }); this.settleEntry(id); }
              else this.updateOverlay();
            },
          });
          return;
        }

        const slot = item.slot; const originalOrder = (this.state.doc.orderBySlot[slot] || []).slice();
        const parentOrder = originalOrder.filter((itemID) => {
          const candidate = this.entry(itemID); return candidate && candidate.container.parentElement === parent;
        });
        const parentIDs = new Set(parentOrder);
        const ordered = parentOrder.map((itemID) => this.entry(itemID)).filter((candidate) => candidate && candidate.id !== id);
        const placeholder = el('div', 'ihtml-item-drop-placeholder');
        placeholder.setAttribute('aria-hidden', 'true'); placeholder.dataset.label = T('item_editor_drop_here');
        placeholder.style.gridColumn = 'span ' + item.flow.span; placeholder.style.height = Math.max(64, Math.round(startRect.height)) + 'px';
        let rows = []; let targetIndex = ordered.findIndex((candidate) => parentOrder.indexOf(candidate.id) > parentOrder.indexOf(id));
        if (targetIndex < 0) targetIndex = ordered.length;
        let measuredScrollLeft = stage.scrollLeft; let measuredScrollTop = stage.scrollTop;
        let previewOrder = originalOrder.slice(); let moved = false; let shiftAnimations = [];
        const cancelShiftAnimations = () => {
          for (const animation of shiftAnimations) { try { animation.cancel(); } catch { /* already finished */ } }
          shiftAnimations = [];
        };
        const orderWithSelectedAt = (index) => {
          const local = ordered.map((candidate) => candidate.id); local.splice(index, 0, id);
          let cursor = 0;
          return originalOrder.map((itemID) => parentIDs.has(itemID) ? local[cursor++] : itemID);
        };
        const placeAt = (nextIndex) => {
          nextIndex = Math.max(0, Math.min(ordered.length, nextIndex));
          if (nextIndex === targetIndex && previewOrder.length) return;
          cancelShiftAnimations();
          const before = new Map(ordered.map((candidate) => [candidate.id, candidate.container.getBoundingClientRect()]));
          const anchor = ordered[nextIndex] && ordered[nextIndex].container;
          if (anchor) parent.insertBefore(placeholder, anchor); else parent.appendChild(placeholder);
          targetIndex = nextIndex;
          previewOrder = orderWithSelectedAt(targetIndex);
          moved = JSON.stringify(previewOrder) !== JSON.stringify(originalOrder);
          rows = this.dragRows(ordered); shiftAnimations = this.animateGridShift(ordered, before);
        };
        previewOrder = orderWithSelectedAt(targetIndex);
        this.beginInteraction(ev, {
          kind: 'move',
          onActivate: () => {
            parent.insertBefore(placeholder, entry.container);
            entry.container.classList.add('ihtml-item-drag-ghost', 'grid');
            Object.assign(entry.container.style, {
              position: 'fixed', left: Math.round(startRect.left) + 'px', top: Math.round(startRect.top) + 'px',
              width: Math.round(startRect.width) + 'px', height: Math.round(startRect.height) + 'px',
              zIndex: '10002', margin: '0', pointerEvents: 'none', transform: 'translate3d(0,0,0)',
            });
            this.dom.overlay.classList.add('is-grid-moving'); rows = this.dragRows(ordered);
          },
          onFrame: (point) => {
            const dx = point.clientX - startX; const dy = point.clientY - startY;
            entry.container.style.transform = 'translate3d(' + Math.round(dx) + 'px,' + Math.round(dy) + 'px,0) scale(1.012)';
            if (stage.scrollLeft !== measuredScrollLeft || stage.scrollTop !== measuredScrollTop) {
              measuredScrollLeft = stage.scrollLeft; measuredScrollTop = stage.scrollTop;
              cancelShiftAnimations(); rows = this.dragRows(ordered);
            }
            const nextIndex = this.dragTargetIndex(rows, point.clientX, point.clientY, ordered.length);
            placeAt(nextIndex);
            this.dom.coordinates.textContent = T('item_editor_drop_position', { value: targetIndex + 1, total: ordered.length + 1 });
          },
          onFinish: (commit) => {
            cancelShiftAnimations(); placeholder.remove(); this.restoreInlineStyle(entry.container, originalStyle);
            entry.container.classList.remove('ihtml-item-drag-ghost', 'grid'); this.dom.overlay.classList.remove('is-grid-moving');
            if (commit && moved) { this.dispatch({ type: 'reorder', slot, ids: previewOrder }); this.settleEntry(id); }
            else this.updateOverlay();
          },
        });
      },
      startResize(ev) {
        if (!this.editing || !this.state || !this.state.selectedId || this.loading || this.saving || this.deleting || ev.button !== 0 || ev.isPrimary === false || this.interaction) return;
        ev.preventDefault(); ev.stopPropagation();
        if (window.innerWidth < 768) { ui.toast(T('item_editor_mobile_drag'), 'warn'); return; }
        const id = this.state.selectedId; const entry = this.entry(id); const item = this.state.doc.items[id];
        if (!entry || !item || !entry.container.parentElement) return;
        const stage = this.dom.stage; const parent = entry.container.parentElement;
        const originalStyle = entry.container.getAttribute('style'); const originalParentHeight = parent.style.minHeight;
        const startX = ev.clientX; const startY = ev.clientY; const rect = entry.container.getBoundingClientRect();
        const mode = this.state.doc.mode; let width = item.free.width; let height = mode === 'free' ? item.free.height : (item.flow.height || Math.round(rect.height));
        let span = item.flow.span; let moved = false; let contentLimited = false;
        const metrics = this.gridTrackMetrics(parent); const { gap, trackWidth } = metrics;
        const parentRect = parent.getBoundingClientRect();
        const startColumn = mode === 'grid'
          ? Math.max(1, Math.min(12, Math.round((rect.left - parentRect.left - metrics.paddingLeft + parent.scrollLeft) / (trackWidth + gap)) + 1))
          : 1;
        const maxGridSpan = Math.min(12, Math.max(item.flow.span, 13 - startColumn));
        const minGridSpan = mode === 'grid' ? this.contentSafeGridSpan(entry, 1, metrics) : 1;
        this.beginInteraction(ev, {
          kind: 'resize',
          autoScroll: false,
          onActivate: () => {
            entry.container.classList.add('ihtml-item-resize-ghost'); entry.container.style.zIndex = '10002';
            this.dom.overlay.classList.add('is-resizing');
          },
          onFrame: (point) => {
            const dx = point.clientX - startX + stage.scrollLeft - this.interaction.startScrollLeft;
            const dy = point.clientY - startY + stage.scrollTop - this.interaction.startScrollTop;
            const snapDelta = (value) => point.altKey ? Math.round(value) : Math.round(value / 8) * 8;
            if (mode === 'free') {
              width = Math.max(120, Math.min(4000, item.free.width + snapDelta(dx)));
              height = Math.max(64, Math.min(4000, item.free.height + snapDelta(dy)));
              moved = width !== item.free.width || height !== item.free.height;
              entry.container.style.width = width + 'px'; entry.container.style.height = height + 'px';
              this.dom.overlay.style.width = width + 'px'; this.dom.overlay.style.height = height + 'px';
              parent.style.minHeight = Math.max(Number.parseFloat(originalParentHeight || 0) || 0, item.free.y + height + 16) + 'px';
              this.dom.coordinates.textContent = 'X ' + item.free.x + '  Y ' + item.free.y + '  W ' + width + '  H ' + height + (point.altKey ? '' : '  ·  ' + T('item_editor_snap', { value: 8 }));
              return;
            }
            const requestedSpan = Math.max(1, Math.min(maxGridSpan, Math.round((Math.max(trackWidth, rect.width + dx) + gap) / (trackWidth + gap))));
            span = Math.min(maxGridSpan, Math.max(minGridSpan, requestedSpan));
            contentLimited = span > requestedSpan; moved = span !== item.flow.span;
            entry.container.style.gridColumn = 'span ' + span;
            const previewRect = entry.container.getBoundingClientRect(); const stageRect = stage.getBoundingClientRect();
            Object.assign(this.dom.overlay.style, {
              left: Math.round(previewRect.left - stageRect.left + stage.scrollLeft) + 'px',
              top: Math.round(previewRect.top - stageRect.top + stage.scrollTop) + 'px',
              width: Math.round(previewRect.width) + 'px', height: Math.round(previewRect.height) + 'px',
            });
            this.dom.coordinates.textContent = T('item_editor_span_value', { value: span }) + ' · ' + (item.flow.height ? 'H ' + item.flow.height : T('item_editor_auto_height'))
              + (contentLimited ? ' · ' + T('item_editor_content_min_span', { value: span }) : (point.altKey ? '' : ' · ' + T('item_editor_snap', { value: 8 })));
          },
          onFinish: (commit) => {
            this.restoreInlineStyle(entry.container, originalStyle); parent.style.minHeight = originalParentHeight;
            entry.container.classList.remove('ihtml-item-resize-ghost'); this.dom.overlay.classList.remove('is-resizing'); this.dom.overlay.style.width = ''; this.dom.overlay.style.height = '';
            if (commit && moved) {
              if (mode === 'free') this.dispatch({ type: 'patch', id, section: 'free', patch: { width, height } });
              else this.dispatch({ type: 'patch', id, section: 'flow', patch: { span } });
              this.settleEntry(id);
            } else this.updateOverlay();
          },
        });
      },
      moveByKeyboard(ev) {
        if (!this.state || !this.state.selectedId || !['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(ev.key)) return;
        ev.preventDefault();
        if (this.state.doc.mode === 'grid') { this.moveLayer(ev.key === 'ArrowLeft' || ev.key === 'ArrowUp' ? -1 : 1); return; }
        const item = this.state.doc.items[this.state.selectedId]; if (!item) return;
        const step = ev.shiftKey ? 8 : 1; const patch = {};
        if (ev.key === 'ArrowLeft') patch.x = item.free.x - step;
        if (ev.key === 'ArrowRight') patch.x = item.free.x + step;
        if (ev.key === 'ArrowUp') patch.y = item.free.y - step;
        if (ev.key === 'ArrowDown') patch.y = item.free.y + step;
        this.dispatch({ type: 'patch', section: 'free', patch });
      },
      resizeByKeyboard(ev) {
        if (!this.state || !this.state.selectedId || !['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(ev.key)) return;
        ev.preventDefault(); const id = this.state.selectedId; const item = this.state.doc.items[id]; if (!item) return;
        if (this.state.doc.mode === 'grid') {
          if (ev.key === 'ArrowLeft' || ev.key === 'ArrowRight') {
            const requested = Math.max(1, Math.min(12, item.flow.span + (ev.key === 'ArrowLeft' ? -1 : 1)));
            const span = this.contentSafeGridSpan(this.entry(id), requested);
            if (span > requested) ui.toast(T('item_editor_content_min_span', { value: span }), 'warn');
            this.dispatch({ type: 'patch', id, section: 'flow', patch: { span } });
          }
          else {
            const current = item.flow.height || Math.round((this.entry(id) && this.entry(id).container.getBoundingClientRect().height) || 180);
            this.dispatch({ type: 'patch', id, section: 'flow', patch: { height: current + (ev.key === 'ArrowUp' ? -8 : 8) } });
          }
          return;
        }
        const step = ev.shiftKey ? 16 : 4; const patch = {};
        if (ev.key === 'ArrowLeft') patch.width = item.free.width - step;
        if (ev.key === 'ArrowRight') patch.width = item.free.width + step;
        if (ev.key === 'ArrowUp') patch.height = item.free.height - step;
        if (ev.key === 'ArrowDown') patch.height = item.free.height + step;
        this.dispatch({ type: 'patch', id, section: 'free', patch });
      },
      updateOverlay() {
        if (this.interaction) return;
        if (!this.dom || !this.editing || !this.state || !this.state.selectedId) {
          if (this.dom) { this.dom.overlay.hidden = true; this.dom.coordinates.textContent = ''; }
          return;
        }
        const entry = this.entry(this.state.selectedId);
        if (!entry || !entry.container.isConnected || !this.dom.stage.contains(entry.container)) {
          this.dom.overlay.hidden = true; this.dom.coordinates.textContent = ''; return;
        }
        const itemRect = entry.container.getBoundingClientRect(); const stageRect = this.dom.stage.getBoundingClientRect();
        const left = itemRect.left - stageRect.left + this.dom.stage.scrollLeft;
        const top = itemRect.top - stageRect.top + this.dom.stage.scrollTop;
        Object.assign(this.dom.overlay.style, {
          left: Math.round(left) + 'px', top: Math.round(top) + 'px',
          width: Math.max(0, Math.round(itemRect.width)) + 'px', height: Math.max(0, Math.round(itemRect.height)) + 'px',
        });
        const title = String(entry.rec.item.title || entry.rec.item.id || 'Item');
        this.dom.chip.textContent = title;
        this.dom.overlay.hidden = false;
        const widthOnly = this.state.doc.mode === 'grid';
        this.dom.resizeHandle.classList.toggle('width-only', widthOnly);
        this.dom.resizeHandle.textContent = T(widthOnly ? 'layout_resize_width_short' : 'layout_resize_short');
        this.dom.resizeHandle.setAttribute('aria-label', T(widthOnly ? 'layout_resize_width' : 'layout_resize', { title }));
        const contextWidth = this.dom.contextBar.offsetWidth;
        this.dom.contextBar.classList.toggle('align-left', left + itemRect.width - contextWidth < this.dom.stage.scrollLeft + 8);
        const layout = this.state.doc.items[entry.id];
        this.dom.coordinates.textContent = this.state.doc.mode === 'free'
          ? 'X ' + layout.free.x + '  Y ' + layout.free.y + '  W ' + layout.free.width + '  H ' + layout.free.height
          : T('item_editor_span_value', { value: layout.flow.span }) + ' · ' + (layout.flow.height ? 'H ' + layout.flow.height : T('item_editor_auto_height'));
      },
      render() {
        if (!this.dom || !this.state) return;
        this.renderToolbar(); this.renderLayers(); this.renderInspector();
        window.requestAnimationFrame(() => this.updateOverlay());
      },
      renderToolbar() {
        const mode = this.state.doc.mode;
        for (const [key, button] of Object.entries(this.dom.modeButtons)) {
          const active = key === mode; button.classList.toggle('active', active);
          button.setAttribute('aria-checked', String(active)); button.tabIndex = active ? 0 : -1;
        }
        const interacting = !!this.interaction;
        this.dom.undo.disabled = this.saving || this.deleting || interacting || !this.state.past.length;
        this.dom.redo.disabled = this.saving || this.deleting || interacting || !this.state.future.length;
        this.dom.save.disabled = this.saving || this.deleting || interacting || !this.state.dirty;
        this.dom.done.disabled = this.saving || this.deleting || interacting;
        for (const button of Object.values(this.dom.modeButtons)) button.disabled = this.saving || this.deleting || interacting;
        this.dom.up.disabled = this.deleting || interacting; this.dom.down.disabled = this.deleting || interacting;
        this.dom.remove.disabled = this.deleting || interacting;
        this.dom.moveHandle.disabled = this.loading || this.saving || this.deleting;
        this.dom.resizeHandle.disabled = this.loading || this.saving || this.deleting;
        this.dom.status.classList.toggle('warn', this.conflict);
        this.dom.status.classList.toggle('dirty', !this.conflict && this.state.dirty);
        this.dom.status.textContent = this.conflict ? T('item_editor_conflict') : (this.saving ? T('item_editor_saving') : (this.state.dirty ? T('item_editor_unsaved') : T('item_editor_saved')));
      },
      renderLayers() {
        this.dom.layerList.textContent = '';
        const records = new Map(this.records().map((entry) => [entry.id, entry]));
        const slots = Object.keys(this.state.doc.orderBySlot);
        for (const slot of slots) {
          const group = el('section', 'ihtml-item-editor-layer-group');
          group.appendChild(el('div', 'slot', slot === 'main' ? T('item_editor_main_area') : slot));
          for (const id of this.state.doc.orderBySlot[slot] || []) {
            const entry = records.get(id); if (!entry) continue;
            const button = el('button', 'ihtml-item-editor-layer');
            button.type = 'button'; button.classList.toggle('active', id === this.state.selectedId);
            button.setAttribute('aria-pressed', String(id === this.state.selectedId));
            button.append(el('strong', '', String(entry.rec.item.title || id)), el('small', '', id));
            button.onclick = () => this.select(id); group.appendChild(button);
          }
          if (group.children.length > 1) this.dom.layerList.appendChild(group);
        }
        if (!this.dom.layerList.children.length) this.dom.layerList.appendChild(el('p', 'ui-muted', T('item_editor_empty')));
      },
      renderInspector() {
        const host = this.dom.inspector; host.textContent = '';
        host.appendChild(el('strong', 'ihtml-item-editor-inspector-title', T('item_editor_properties')));
        const id = this.state.selectedId; const entry = this.entry(id); const item = this.state.doc.items[id];
        if (!entry || !item) { host.appendChild(el('p', 'ui-muted', T('item_editor_select_hint'))); return; }
        host.append(el('div', 'ihtml-item-editor-inspector-item', String(entry.rec.item.title || id)), el('small', 'ihtml-item-editor-inspector-id', id));
        const numberField = (label, section, key, value, min, max, suffix) => {
          const wrap = el('label', 'ihtml-item-editor-field'); const name = el('span', '', label);
          const control = el('div', 'control'); const input = el('input'); input.type = 'number'; input.inputMode = 'numeric';
          input.value = value; input.min = String(min); input.max = String(max); input.setAttribute('aria-label', label);
          let touched = false;
          const commit = () => {
            if (!touched) return;
            touched = false;
            const requested = Math.max(min, Math.min(max, Number(input.value)));
            const nextValue = section === 'flow' && key === 'span' ? this.contentSafeGridSpan(entry, requested) : requested;
            if (nextValue !== requested) ui.toast(T('item_editor_content_min_span', { value: nextValue }), 'warn');
            this.dispatch({ type: 'patch', id, section, patch: { [key]: nextValue } });
          };
          input.oninput = () => { touched = true; }; input.onchange = () => { touched = true; commit(); }; input.onblur = commit;
          input.onkeydown = (ev) => { if (ev.key === 'Enter') { ev.preventDefault(); touched = true; commit(); } };
          control.append(input); if (suffix) control.appendChild(el('span', 'suffix', suffix)); wrap.append(name, control); return wrap;
        };
        const section = (title) => { const box = el('section', 'ihtml-item-editor-inspector-section'); box.appendChild(el('h3', '', title)); host.appendChild(box); return box; };
        if (this.state.doc.mode === 'free') {
          const position = section(T('item_editor_position')); const grid2 = el('div', 'field-grid');
          grid2.append(numberField('X', 'free', 'x', item.free.x, 0, 10000, 'px'), numberField('Y', 'free', 'y', item.free.y, 0, 10000, 'px')); position.appendChild(grid2);
          const size = section(T('item_editor_size')); const sizeGrid = el('div', 'field-grid');
          sizeGrid.append(numberField(T('item_editor_width'), 'free', 'width', item.free.width, 120, 4000, 'px'), numberField(T('item_editor_height'), 'free', 'height', item.free.height, 64, 4000, 'px')); size.appendChild(sizeGrid);
        } else {
          const size = section(T('item_editor_size')); const sizeGrid = el('div', 'field-grid');
          sizeGrid.appendChild(numberField(T('item_editor_width'), 'flow', 'span', item.flow.span, 1, 12, T('item_editor_columns')));
          const height = numberField(T('item_editor_height'), 'flow', 'height', item.flow.height || Math.round(entry.container.getBoundingClientRect().height), 64, 4000, 'px');
          const auto = el('button', 'text', T('item_editor_auto_height')); auto.type = 'button'; auto.onclick = () => this.dispatch({ type: 'patch', id, section: 'flow', patch: { height: '' } });
          height.appendChild(auto); sizeGrid.appendChild(height); size.appendChild(sizeGrid);
        }
        const appearance = section(T('item_editor_style'));
        const colorField = (label, key) => {
          const wrap = el('label', 'ihtml-item-editor-field color'); wrap.appendChild(el('span', '', label));
          const control = el('div', 'control'); const swatch = el('input'); swatch.type = 'color';
          const current = item.appearance[key] || ''; swatch.value = /^#[0-9a-f]{6}$/i.test(current) ? current : '#ffffff'; swatch.setAttribute('aria-label', label);
          const input = el('input'); input.type = 'text'; input.value = current; input.placeholder = T('item_editor_color_placeholder'); input.setAttribute('aria-label', label + ' CSS');
          const commit = (value) => {
            const safe = itemLayoutCore.sanitizeColor(value);
            if (value && !safe) { ui.toast(T('item_editor_invalid_color'), 'warn'); this.renderInspector(); return; }
            this.dispatch({ type: 'patch', id, section: 'appearance', patch: { [key]: safe } });
          };
          swatch.onchange = () => commit(swatch.value);
          input.onchange = () => commit(input.value.trim()); input.onblur = () => commit(input.value.trim());
          input.onkeydown = (ev) => { if (ev.key === 'Enter') { ev.preventDefault(); commit(input.value.trim()); } };
          const clear = el('button', 'text', T('item_editor_clear')); clear.type = 'button'; clear.onclick = () => commit('');
          control.append(swatch, input, clear); wrap.appendChild(control); return wrap;
        };
        appearance.append(colorField(T('item_editor_background'), 'background'), colorField(T('item_editor_text_color'), 'color'));
        const layer = section(T('item_editor_layer')); const layerRow = el('div', 'ihtml-item-editor-layer-actions');
        const up = el('button', '', T('item_editor_layer_up')); const down = el('button', '', T('item_editor_layer_down'));
        up.onclick = () => this.moveLayer(-1); down.onclick = () => this.moveLayer(1); layerRow.append(up, down);
        if (this.state.doc.mode === 'free') layerRow.appendChild(numberField(T('item_editor_z'), 'free', 'z', item.free.z, 0, 999, ''));
        layer.appendChild(layerRow);
        const danger = section(T('item_editor_delete_title')); const remove = el('button', 'danger wide', T('item_editor_delete'));
        remove.disabled = this.deleting; remove.onclick = () => { void this.deleteSelected(); }; danger.appendChild(remove);
      },
      syncRecords() {
        if (!this.state || this.page !== (nav.currentPage || '')) return;
        const doc2 = this.seed(this.state.doc); const baseline = this.seed(this.state.baseline);
        const selectedId = doc2.items[this.state.selectedId] ? this.state.selectedId : Object.keys(doc2.items)[0] || '';
        this.state = Object.assign({}, this.state, {
          doc: doc2, baseline, selectedId,
          dirty: JSON.stringify(doc2) !== JSON.stringify(baseline),
        });
        this.apply(); if (this.editing) this.render();
      },
      async reset() {
        if (readOnly || !itemLayoutCore) return false;
        if (this.page !== (nav.currentPage || '') || !this.state) await this.load();
        const fresh = this.seed(itemLayoutCore.empty(this.page));
        const selectedId = fresh.items[this.state && this.state.selectedId] ? this.state.selectedId : Object.keys(fresh.items)[0] || '';
        this.state = itemLayoutCore.reduce(this.state || itemLayoutCore.initial(this.page), { type: 'replaceDoc', doc: fresh, selectedId });
        this.conflict = false; this.apply(); if (this.editing) this.render();
        return this.save();
      },
      destroy() {
        if (this.interaction) this.cancelInteraction();
        if (this.resizeObserver) { this.resizeObserver.disconnect(); this.resizeObserver = null; }
        this.restoreContentInteractions();
        this.close(true); this.clearParents();
        if (this.dom) this.dom.shell.remove();
        this.dom = null; this.state = itemLayoutCore ? itemLayoutCore.initial('') : null;
      },
    };

    /* ---------- 空模板结构工作室 ----------
     * 模板只编辑 Root / Container / Slot 以及断点布局，不把动态业务 Item 搬进编辑器。
     * draft 使用服务端 revision CAS；所有本地动作经过纯 reducer，支持可靠撤销/重做。 */
    const templateStudio = {
      dom: null,
      state: templateCore ? templateCore.initial() : null,
      record: null,
      remoteRecord: null,
      saving: false,
      publishing: false,
      loading: false,
      loadError: null,
      loadSeq: 0,
      idSeq: 0,
      draggedId: '',
      previousFocus: null,
      page: '',

      async open() {
        if (!templateCore || readOnly) return;
        if (itemEditor.editing && !(await itemEditor.requestClose())) return;
        panel.close();
        grid.edit(false);
        if (!this.dom) this.build();
        this.previousFocus = doc.activeElement;
        this.dom.shell.hidden = false;
        rootEl.classList.add('ihtml-template-open');
        this.dom.shell.focus();
        const nextPage = nav.currentPage || '';
        if (this.state && this.state.dirty && this.page === nextPage) this.render();
        else await this.load(nextPage);
      },
      async requestClose() {
        if (this.state && this.state.dirty) {
          const leave = await ui.confirm({ title: T('template_unsaved'), text: T('template_exit_hint'), okText: T('template_exit') });
          if (!leave) return;
          this.record = null;
          this.remoteRecord = null;
          this.state = templateCore.initial();
        }
        this.close();
      },
      close() {
        if (!this.dom || this.dom.shell.hidden) return;
        this.dom.shell.hidden = true;
        rootEl.classList.remove('ihtml-template-open');
        if (this.previousFocus && this.previousFocus.isConnected && typeof this.previousFocus.focus === 'function') this.previousFocus.focus();
        this.previousFocus = null;
      },
      newID(kind) {
        const prefix = kind === 'slot' ? 'slot' : 'container';
        do { this.idSeq++; } while (this.state.doc.nodes[prefix + '-' + this.idSeq]);
        return prefix + '-' + this.idSeq;
      },
      async load(page) {
        const seq = ++this.loadSeq;
        this.loading = true;
        this.loadError = null;
        this.page = page || '';
        this.render();
        try {
          const out = await http.get('api/templates/current?page=' + encodeURIComponent(this.page));
          if (seq !== this.loadSeq || destroyed) return;
          this.record = out && out.template || null;
          this.remoteRecord = null;
          const version = this.record && (this.record.draft || this.record.version_data || this.record.version);
          this.state = templateCore.initial(templateCore.fromVersion(version));
          this.idSeq = templateCore.flatten(this.state.doc).length;
        } catch (err) {
          if (seq !== this.loadSeq || destroyed || isStaleError(err) || err && err.name === 'AbortError') return;
          this.record = null;
          this.remoteRecord = null;
          this.state = templateCore.initial();
          this.loadError = err;
          ui.toast(T('template_load_failed'), 'warn');
        } finally {
          if (seq !== this.loadSeq || destroyed) return;
          this.loading = false;
          this.render();
        }
      },
      acceptRemote(record, page) {
        if (!record || String(page || '') !== this.page) return;
        if (this.state && this.state.dirty) {
          if (!this.record || Number(record.revision || 0) !== Number(this.record.revision || 0)) this.remoteRecord = record;
          this.render();
          return;
        }
        this.record = record;
        this.remoteRecord = null;
        this.loadError = null;
        this.state = templateCore.initial(templateCore.fromVersion(record.draft));
        this.render();
      },
      dispatch(action, opts2) {
        if (!this.state || this.loading || this.saving) return;
        const active = doc.activeElement;
        const remembered = active && active.dataset ? { key: active.dataset.templateFocus || '', id: active.dataset.templateId || '' } : { key: '', id: '' };
        const next = templateCore.reduce(this.state, action);
        if (next === this.state) return;
        this.state = next;
        this.render();
        queueMicrotask(() => {
          const focusID = opts2 && opts2.focus || remembered.id;
          const focusKey = opts2 && opts2.focusKey || remembered.key || (focusID ? 'tree' : '');
          const target = Array.from(this.dom.shell.querySelectorAll('[data-template-focus]')).find((node) => node.dataset.templateFocus === focusKey && (!focusID || node.dataset.templateId === focusID));
          if (target) target.focus();
        });
      },
      add(kind) {
        const selected = this.state.doc.nodes[this.state.selectedId];
        const parentId = selected && selected.type !== 'slot' ? selected.id : (selected && selected.parent || 'root');
        const id = this.newID(kind);
        this.dispatch({ type: 'add', id, nodeType: kind, parentId }, { focus: id });
      },
      async removeSelected() {
        const id = this.state.selectedId;
        if (id === 'root') { ui.toast(T('template_locked_root'), 'warn'); return; }
        const parentID = this.state.doc.nodes[id] && this.state.doc.nodes[id].parent || 'root';
        if (templateCore.descendants(this.state.doc, id).length) {
          const ok = await ui.confirm({ title: T('template_delete_title'), text: T('template_delete_hint'), okText: T('template_delete') });
          if (!ok) return;
        }
        this.dispatch({ type: 'remove', id }, { focus: parentID, focusKey: 'tree' });
      },
      async save() {
        if (!this.state || !this.state.dirty || this.saving || this.remoteRecord || this.loadError || !this.record) return;
        this.saving = true;
        this.renderToolbar();
        const operationPage = this.page;
        const operationSeq = this.loadSeq;
        const payload = templateCore.saveRequest(this.record, this.page, this.state.doc);
        try {
          const out = await http.put('api/templates/current?page=' + encodeURIComponent(this.page), payload);
          if (operationPage !== this.page || operationSeq !== this.loadSeq || destroyed) return;
          this.record = out && out.template || payload.template;
          this.remoteRecord = null;
          this.state = templateCore.reduce(this.state, { type: 'markSaved', version: this.record.draft && this.record.draft.version });
          ui.toast(T('template_saved'), 'success');
        } catch (err) {
          if (err && err.status === 409 && err.body && err.body.current) {
            this.remoteRecord = err.body.current;
            ui.toast(T('template_conflict'), 'warn');
          } else ui.toast(T('template_save_failed') + ': ' + String(err && err.message || err), 'error');
        } finally {
          this.saving = false;
          this.render();
        }
      },
      reloadRemote() {
        if (!this.remoteRecord) return;
        this.record = this.remoteRecord;
        this.remoteRecord = null;
        this.state = templateCore.initial(templateCore.fromVersion(this.record.draft));
        this.render();
      },
      async publish() {
        const publishedVersion = Number(this.record && this.record.published && this.record.published.version || 0);
        const draftVersion = Number(this.record && this.record.draft && this.record.draft.version || 0);
        if (!this.record || !this.record.id || !this.record.revision || this.state.dirty || this.saving || this.publishing || this.remoteRecord || this.loadError || publishedVersion && draftVersion <= publishedVersion) return;
        this.publishing = true;
        this.renderToolbar();
        const operationPage = this.page;
        const operationSeq = this.loadSeq;
        try {
          const out = await http.post('api/templates/' + encodeURIComponent(this.record.id) + '/publish', { expected_revision: Number(this.record.revision) });
          if (operationPage !== this.page || operationSeq !== this.loadSeq || destroyed) return;
          this.record = out && out.template || this.record;
          this.remoteRecord = null;
          ui.toast(T('template_published', { n: this.record.published && this.record.published.version || '' }), 'success');
          await templateRuntime.load(this.page);
        } catch (err) {
          if (err && err.status === 409 && err.body && err.body.current) {
            this.remoteRecord = err.body.current;
            ui.toast(T('template_conflict'), 'warn');
          } else ui.toast(T('template_publish_failed') + ': ' + String(err && err.message || err), 'error');
        } finally {
          this.publishing = false;
          this.render();
        }
      },
      build() {
        const shellEl = el('section', 'ihtml-template-studio');
        shellEl.hidden = true;
        shellEl.tabIndex = -1;
        shellEl.setAttribute('role', 'dialog');
        shellEl.setAttribute('aria-modal', 'true');
        shellEl.setAttribute('aria-label', T('template_title'));
        const toolbar = el('header', 'ihtml-template-toolbar');
        const body = el('div', 'ihtml-template-body');
        const layers = el('aside', 'ihtml-template-layers');
        const canvas = el('main', 'ihtml-template-main');
        const inspector = el('aside', 'ihtml-template-inspector');
        body.append(layers, canvas, inspector);
        const assistant = el('button', 'ihtml-template-assistant', T('chat_entry'));
        assistant.hidden = !chat.enabled;
        assistant.onclick = () => chat.open();
        shellEl.append(toolbar, body);
        shellEl.addEventListener('keydown', (ev) => {
          const editingText = /^(INPUT|TEXTAREA|SELECT)$/.test(ev.target && ev.target.tagName);
          if ((ev.metaKey || ev.ctrlKey) && ev.key.toLowerCase() === 'z') {
            ev.preventDefault();
            this.dispatch({ type: ev.shiftKey ? 'redo' : 'undo' });
          } else if ((ev.metaKey || ev.ctrlKey) && ev.key.toLowerCase() === 's') {
            ev.preventDefault();
            void this.save();
          } else if (!editingText && (ev.key === 'Delete' || ev.key === 'Backspace')) {
            ev.preventDefault();
            void this.removeSelected();
          } else if (ev.key === 'Escape' && !chat.dom?.panel?.offsetParent) {
            ev.preventDefault();
            void this.requestClose();
          } else if (ev.key === 'Tab') {
            const focusable = dialogFocusable(shellEl);
            if (!focusable.length) { ev.preventDefault(); shellEl.focus(); }
            else if (ev.shiftKey && doc.activeElement === focusable[0]) { ev.preventDefault(); focusable[focusable.length - 1].focus(); }
            else if (!ev.shiftKey && doc.activeElement === focusable[focusable.length - 1]) { ev.preventDefault(); focusable[0].focus(); }
          }
        });
        rootEl.appendChild(shellEl);
        this.dom = { shell: shellEl, toolbar, body, layers, canvas, inspector, assistant };
      },
      render() {
        if (!this.dom || this.dom.shell.hidden) return;
        this.renderToolbar();
        this.dom.layers.textContent = '';
        this.dom.canvas.textContent = '';
        this.dom.inspector.textContent = '';
        if (this.loading || !this.state) {
          const busy = el('div', 'ihtml-template-loading', T('loading'));
          busy.setAttribute('role', 'status');
          this.dom.canvas.appendChild(busy);
          return;
        }
        if (this.loadError) {
          const failed = el('div', 'ihtml-template-loading');
          failed.setAttribute('role', 'alert');
          failed.append(el('span', '', T('template_load_failed')));
          const retry = el('button', 'ui-btn primary', T('retry'));
          retry.onclick = () => void this.load(this.page);
          failed.appendChild(retry);
          this.dom.canvas.appendChild(failed);
          return;
        }
        this.renderLayers();
        if (this.state.view === 'compare') this.renderCompare();
        else this.renderCanvas();
        this.renderInspector();
      },
      renderToolbar() {
        if (!this.dom) return;
        const bar = this.dom.toolbar;
        bar.textContent = '';
        const identity = el('div', 'ihtml-template-identity');
        identity.append(el('strong', '', T('template_title')), el('span', 'ihtml-template-page', this.page || T('template_root')));
        const views = el('div', 'ihtml-template-segment');
        for (const [key, label] of [['studio', T('template_studio')], ['compare', T('template_compare')]]) {
          const button = el('button', '', label);
          button.dataset.templateFocus = 'view-' + key;
          button.setAttribute('aria-pressed', String(this.state && this.state.view === key));
          button.onclick = () => this.dispatch({ type: 'view', view: key });
          views.appendChild(button);
        }
        const breakpoints = el('div', 'ihtml-template-segment ihtml-template-breakpoints');
        for (const [key, label] of [['desktop', T('template_desktop')], ['tablet', T('template_tablet')], ['mobile', T('template_mobile')]]) {
          const button = el('button', '', label);
          button.dataset.templateFocus = 'breakpoint-' + key;
          button.setAttribute('aria-pressed', String(this.state && this.state.breakpoint === key));
          button.disabled = !!this.state && this.state.view === 'compare';
          button.onclick = () => this.dispatch({ type: 'breakpoint', breakpoint: key });
          breakpoints.appendChild(button);
        }
        const actions = el('div', 'ihtml-template-toolbar-actions');
        const undo = el('button', '', T('template_undo'));
        undo.dataset.templateFocus = 'undo';
        undo.disabled = !this.state || !this.state.past.length || this.saving;
        undo.onclick = () => this.dispatch({ type: 'undo' });
        const redo = el('button', '', T('template_redo'));
        redo.dataset.templateFocus = 'redo';
        redo.disabled = !this.state || !this.state.future.length || this.saving;
        redo.onclick = () => this.dispatch({ type: 'redo' });
        const status = el('span', 'ihtml-template-status', this.remoteRecord ? T('template_conflict') : (this.saving ? T('template_saving') : (this.state && this.state.dirty ? T('template_unsaved') : T('template_saved'))));
        status.setAttribute('role', 'status');
        const save = el('button', 'primary', this.saving ? T('template_saving') : T('template_save'));
        save.dataset.templateFocus = 'save';
        save.disabled = !this.state || !this.state.dirty || this.saving || !!this.remoteRecord || !!this.loadError || !this.record;
        save.onclick = () => void this.save();
        const exit = el('button', '', T('template_exit'));
        exit.dataset.templateFocus = 'exit';
        exit.onclick = () => void this.requestClose();
        actions.append(undo, redo, status);
        if (this.remoteRecord) {
          const reload = el('button', '', T('template_reload'));
          reload.dataset.templateFocus = 'reload';
          reload.onclick = () => this.reloadRemote();
          actions.appendChild(reload);
        }
        const publishedVersion = Number(this.record && this.record.published && this.record.published.version || 0);
        const draftVersion = Number(this.record && this.record.draft && this.record.draft.version || 0);
        const publishState = el('span', 'ihtml-template-publish-state', !publishedVersion ? T('template_unpublished') : (draftVersion > publishedVersion ? T('template_draft_newer') : T('template_published', { n: publishedVersion })));
        const publish = el('button', '', this.publishing ? T('template_publishing') : T('template_publish'));
        publish.dataset.templateFocus = 'publish';
        publish.disabled = !this.record || !this.record.id || !this.record.revision || !!(this.state && this.state.dirty) || this.saving || this.publishing || !!this.remoteRecord || !!this.loadError || !!(publishedVersion && draftVersion <= publishedVersion);
        publish.onclick = () => void this.publish();
        this.dom.assistant.hidden = !chat.enabled;
        actions.append(publishState, save, publish, this.dom.assistant, exit);
        bar.append(identity, views, breakpoints, actions);
      },
      renderLayers() {
        const heading = el('div', 'ihtml-template-sidehead');
        heading.appendChild(el('strong', '', T('template_layers')));
        const adds = el('div', 'ihtml-template-adds');
        const addContainer = el('button', '', '+ ' + T('template_container'));
        addContainer.dataset.templateFocus = 'add-container';
        addContainer.onclick = () => this.add('container');
        const addSlot = el('button', '', '+ ' + T('template_slot'));
        addSlot.dataset.templateFocus = 'add-slot';
        addSlot.onclick = () => this.add('slot');
        adds.append(addContainer, addSlot);
        this.dom.layers.append(heading, adds);
        const tree = el('div', 'ihtml-template-tree');
        tree.setAttribute('role', 'tree');
        tree.setAttribute('aria-label', T('template_layers'));
        for (const entry of templateCore.flatten(this.state.doc)) tree.appendChild(this.renderTreeRow(entry));
        this.dom.layers.appendChild(tree);
        const tray = el('section', 'ihtml-template-unassigned');
        this.dom.layers.appendChild(tray);
        this.dom.unassigned = tray;
        this.updateUnassigned();
      },
      updateUnassigned() {
        const tray = this.dom && this.dom.unassigned;
        if (!tray || !tray.isConnected || !this.state || !this.state.doc) return;
        const slotRules = new Map(Object.values(this.state.doc.meta.slotMeta || {}).map((slot) => [
          String(slot && slot.id || ''),
          Array.isArray(slot && slot.accepts) ? slot.accepts.map((type) => String(type)) : [],
        ]));
        const placements = this.state.doc.meta.placements || [];
        let unassigned = 0;
        for (const rec of registry.values()) {
          if (!rec.item || rec.item.type !== 'html' || rec.page && rec.page !== this.page) continue;
          const placement = templateCore.resolvePlacement({ placements }, rec.item.id, this.state.breakpoint);
          const metaSlot = String(rec.item.meta && rec.item.meta.slot || '');
          const slotID = placement ? (!placement.unassigned && placement.slot_id ? String(placement.slot_id) : '') : metaSlot;
          const accepts = slotRules.get(slotID) || [];
          const assigned = !!slotID && slotRules.has(slotID) && (!accepts.length || accepts.includes(String(rec.item.type)));
          if (!assigned) unassigned++;
        }
        tray.replaceChildren(
          el('strong', '', T('template_unassigned') + ' · ' + unassigned),
          el('small', '', T('template_preview_off')),
        );
      },
      renderTreeRow(entry) {
        const row = el('button', 'ihtml-template-tree-row');
        row.type = 'button';
        row.dataset.templateId = entry.id;
        row.dataset.templateFocus = 'tree';
        row.style.setProperty('--template-depth', entry.depth);
        row.setAttribute('role', 'treeitem');
        row.setAttribute('aria-level', String(entry.depth + 1));
        row.setAttribute('aria-selected', String(entry.id === this.state.selectedId));
        row.draggable = entry.id !== 'root';
        const glyph = entry.node.type === 'root' ? '⌗' : (entry.node.type === 'slot' ? '◇' : '▣');
        row.append(el('span', 'glyph', glyph), el('span', 'name', entry.node.name), el('small', '', entry.node.type === 'root' ? 'Root' : (entry.node.type === 'slot' ? 'Slot' : 'Container')));
        row.onclick = () => this.dispatch({ type: 'select', id: entry.id });
        row.addEventListener('dragstart', (ev) => { this.draggedId = entry.id; ev.dataTransfer.effectAllowed = 'move'; ev.dataTransfer.setData('text/plain', entry.id); });
        row.addEventListener('dragend', () => { this.draggedId = ''; });
        row.addEventListener('dragover', (ev) => { if (this.draggedId && this.draggedId !== entry.id) { ev.preventDefault(); ev.dataTransfer.dropEffect = 'move'; } });
        row.addEventListener('drop', (ev) => this.dropOnTree(ev, entry.id));
        row.addEventListener('keydown', (ev) => this.treeKey(ev, entry.id));
        return row;
      },
      dropOnTree(ev, targetId) {
        ev.preventDefault();
        const id = this.draggedId || ev.dataTransfer.getData('text/plain');
        if (!id || id === targetId || id === 'root') return;
        const target = this.state.doc.nodes[targetId];
        const rect = ev.currentTarget.getBoundingClientRect();
        const ratio = (ev.clientY - rect.top) / Math.max(1, rect.height);
        let parentId = targetId;
        let index;
        if (target.type === 'slot' || ratio < 0.28 || ratio > 0.72) {
          parentId = target.parent || 'root';
          const siblings = (this.state.doc.children[parentId] || []).filter((child) => child !== id);
          index = siblings.indexOf(targetId) + (ratio > 0.72 ? 1 : 0);
        }
        this.draggedId = '';
        this.dispatch({ type: 'move', id, parentId, index }, { focus: id });
      },
      treeKey(ev, id) {
        const rows = templateCore.flatten(this.state.doc);
        const rowIndex = rows.findIndex((entry) => entry.id === id);
        if (!ev.altKey && ['ArrowUp', 'ArrowDown', 'Home', 'End'].includes(ev.key)) {
          ev.preventDefault();
          const nextIndex = ev.key === 'Home' ? 0 : (ev.key === 'End' ? rows.length - 1 : Math.max(0, Math.min(rows.length - 1, rowIndex + (ev.key === 'ArrowUp' ? -1 : 1))));
          const next = Array.from(this.dom.layers.querySelectorAll('[data-template-focus="tree"]')).find((node) => node.dataset.templateId === rows[nextIndex].id);
          if (next) next.focus();
          return;
        }
        if (!ev.altKey || !['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight'].includes(ev.key) || id === 'root') return;
        ev.preventDefault();
        const node = this.state.doc.nodes[id];
        const parent = node.parent || 'root';
        const siblings = this.state.doc.children[parent] || [];
        const index = siblings.indexOf(id);
        if (ev.key === 'ArrowUp' || ev.key === 'ArrowDown') {
          const next = index + (ev.key === 'ArrowUp' ? -1 : 1);
          if (next >= 0 && next < siblings.length) this.dispatch({ type: 'move', id, parentId: parent, index: next }, { focus: id });
        } else if (ev.key === 'ArrowLeft' && parent !== 'root') {
          const grand = this.state.doc.nodes[parent].parent || 'root';
          const at = (this.state.doc.children[grand] || []).indexOf(parent) + 1;
          this.dispatch({ type: 'move', id, parentId: grand, index: at }, { focus: id });
        } else if (ev.key === 'ArrowRight' && index > 0) {
          const target = this.state.doc.nodes[siblings[index - 1]];
          if (target && target.type !== 'slot') this.dispatch({ type: 'move', id, parentId: target.id }, { focus: id });
        }
      },
      renderCanvas() {
        const head = el('div', 'ihtml-template-canvas-head');
        head.append(el('strong', '', T('template_canvas')), el('span', '', T('template_preview_off')));
        const viewport = el('div', 'ihtml-template-viewport bp-' + this.state.breakpoint);
        viewport.appendChild(this.renderCanvasNode('root', this.state.breakpoint, true, null));
        this.dom.canvas.append(head, viewport);
      },
      renderCompare() {
        const head = el('div', 'ihtml-template-canvas-head');
        head.append(el('strong', '', T('template_compare')), el('span', '', T('template_preview_off')));
        const compare = el('div', 'ihtml-template-compare');
        for (const [bp, label] of [['desktop', T('template_desktop')], ['tablet', T('template_tablet')], ['mobile', T('template_mobile')]]) {
          const column = el('section', 'ihtml-template-compare-column bp-' + bp);
          const button = el('button', 'ihtml-template-compare-title', label);
          button.onclick = () => { this.dispatch({ type: 'breakpoint', breakpoint: bp }); this.dispatch({ type: 'view', view: 'studio' }); };
          column.append(button, this.renderCanvasNode('root', bp, false, null));
          compare.appendChild(column);
        }
        this.dom.canvas.append(head, compare);
      },
      renderCanvasNode(id, breakpoint, interactive, parentLayout) {
        const node = this.state.doc.nodes[id];
        const layout = templateCore.resolveLayout(node, breakpoint);
        const frame = templateCore.resolveFrame(node, breakpoint);
        const box = el('section', 'ihtml-template-node type-' + node.type);
        box.dataset.templateCanvasId = id;
        box.classList.toggle('selected', interactive && id === this.state.selectedId);
        if (parentLayout) this.applyFrameStyle(box, frame, parentLayout);
        const nodeHead = el('div', 'ihtml-template-node-head');
        const drag = el('button', 'drag', '⋮⋮');
        drag.setAttribute('aria-label', T('layout_move', { title: node.name }));
        // 自由排版使用 Pointer Events 修改坐标。若同时开启原生 HTML DnD，
        // dragstart 会抢走 pointermove，手柄看似可拖但 frame 不会更新。
        drag.draggable = interactive && id !== 'root' && (!parentLayout || parentLayout.mode !== 'free');
        const label = el(interactive ? 'button' : 'div', 'label');
        if (interactive) { label.dataset.templateFocus = 'canvas-select'; label.dataset.templateId = id; }
        label.append(el('strong', '', node.name), el('small', '', node.type === 'root' ? 'Root' : (node.type === 'slot' ? 'Slot' : layout.mode)));
        label.onclick = (ev) => { ev.stopPropagation(); if (interactive) this.dispatch({ type: 'select', id }); };
        nodeHead.append(drag, label);
        box.appendChild(nodeHead);
        if (interactive && id !== 'root') {
          drag.addEventListener('dragstart', (ev) => { this.draggedId = id; ev.dataTransfer.effectAllowed = 'move'; ev.dataTransfer.setData('text/plain', id); });
          drag.dataset.templateFocus = 'canvas-move'; drag.dataset.templateId = id;
          drag.addEventListener('dragend', () => { this.draggedId = ''; });
          if (parentLayout && parentLayout.mode === 'free') drag.addEventListener('pointerdown', (ev) => this.beginMove(ev, id, box, breakpoint));
          drag.addEventListener('keydown', (ev) => {
            if (!['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight'].includes(ev.key)) return;
            ev.preventDefault();
            if (parentLayout && parentLayout.mode === 'free') {
              const current = templateCore.resolveFrame(node, breakpoint);
              const patch = {};
              if (ev.key === 'ArrowLeft' || ev.key === 'ArrowRight') patch.x = Math.max(0, (current.x || 0) + (ev.key === 'ArrowLeft' ? -8 : 8));
              else patch.y = Math.max(0, (current.y || 0) + (ev.key === 'ArrowUp' ? -8 : 8));
              this.dispatch({ type: 'patchFrame', id, breakpoint, patch });
            } else {
              const siblings = this.state.doc.children[node.parent] || [];
              const index = siblings.indexOf(id);
              const delta = ev.key === 'ArrowUp' || ev.key === 'ArrowLeft' ? -1 : 1;
              if (index + delta >= 0 && index + delta < siblings.length) this.dispatch({ type: 'move', id, parentId: node.parent, index: index + delta });
            }
          });
        } else drag.hidden = true;
        if (interactive && id !== 'root') {
          nodeHead.addEventListener('dragover', (ev) => { if (this.draggedId && this.draggedId !== id) ev.preventDefault(); });
          nodeHead.addEventListener('drop', (ev) => {
            ev.preventDefault(); ev.stopPropagation();
            const moving = this.draggedId || ev.dataTransfer.getData('text/plain');
            this.draggedId = '';
            const siblings = (this.state.doc.children[node.parent] || []).filter((child) => child !== moving);
            if (moving && moving !== id) this.dispatch({ type: 'move', id: moving, parentId: node.parent, index: siblings.indexOf(id) }, { focus: moving });
          });
        }
        if (node.type === 'slot') {
          box.appendChild(el('div', 'ihtml-template-slot-empty', T('template_drop_here')));
        } else {
          const children = el('div', 'ihtml-template-node-children mode-' + layout.mode);
          this.applyLayoutStyle(children, layout);
          const ids = this.state.doc.children[id] || [];
          for (const childId of ids) children.appendChild(this.renderCanvasNode(childId, breakpoint, interactive, layout));
          if (!ids.length) {
            const emptyBox = el('div', 'ihtml-template-structure-empty');
            if (id === 'root') emptyBox.append(el('strong', '', T('template_empty_title')), el('span', '', T('template_empty_hint')));
            else emptyBox.appendChild(el('span', '', T('template_drop_here')));
            children.appendChild(emptyBox);
          }
          box.appendChild(children);
          if (interactive) {
            children.addEventListener('dragover', (ev) => { if (this.draggedId && this.draggedId !== id) ev.preventDefault(); });
            children.addEventListener('drop', (ev) => {
              ev.preventDefault(); ev.stopPropagation();
              const moving = this.draggedId || ev.dataTransfer.getData('text/plain');
              this.draggedId = '';
              if (moving && moving !== id) this.dispatch({ type: 'move', id: moving, parentId: id }, { focus: moving });
            });
          }
        }
        if (interactive && id === this.state.selectedId && id !== 'root') {
          const resize = el('button', 'ihtml-template-resize', '↘');
          resize.dataset.templateFocus = 'canvas-resize'; resize.dataset.templateId = id;
          resize.setAttribute('aria-label', T('layout_resize', { title: node.name }));
          resize.addEventListener('pointerdown', (ev) => this.beginResize(ev, id, box, breakpoint, parentLayout));
          resize.addEventListener('keydown', (ev) => {
            if (!['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight', 'Home'].includes(ev.key) || !parentLayout) return;
            ev.preventDefault();
            const current = templateCore.resolveFrame(node, breakpoint);
            let patch;
            if (ev.key === 'Home') patch = { w: null, h: null, span: null };
            else if (parentLayout.mode === 'grid' && (ev.key === 'ArrowLeft' || ev.key === 'ArrowRight')) patch = { span: Math.max(1, Math.min(parentLayout.columns || 12, (current.span || parentLayout.columns || 12) + (ev.key === 'ArrowLeft' ? -1 : 1))) };
            else if (ev.key === 'ArrowLeft' || ev.key === 'ArrowRight') patch = { w: Math.max(48, (current.w || box.getBoundingClientRect().width) + (ev.key === 'ArrowLeft' ? -8 : 8)) };
            else patch = { h: Math.max(48, (current.h || box.getBoundingClientRect().height) + (ev.key === 'ArrowUp' ? -8 : 8)) };
            this.dispatch({ type: 'patchFrame', id, breakpoint, patch });
          });
          box.appendChild(resize);
        }
        return box;
      },
      applyLayoutStyle(container, layout) {
        container.style.gap = layout.gap + 'px';
        container.style.padding = layout.padding + 'px';
        if (layout.mode === 'flow') {
          container.style.flexDirection = layout.direction || 'column';
          container.style.flexWrap = layout.wrap ? 'wrap' : 'nowrap';
          container.style.alignItems = layout.align || 'stretch';
        } else if (layout.mode === 'grid') container.style.gridTemplateColumns = 'repeat(' + (layout.columns || 12) + ', minmax(0, 1fr))';
        else container.style.minHeight = (layout.minHeight || 240) + 'px';
      },
      applyFrameStyle(box, frame, parentLayout) {
        if (parentLayout.mode === 'free') {
          box.style.left = (frame.x || 0) + 'px';
          box.style.top = (frame.y || 0) + 'px';
          box.style.width = (frame.w || 240) + 'px';
          box.style.minHeight = (frame.h || 120) + 'px';
        } else if (parentLayout.mode === 'grid') {
          box.style.gridColumn = 'span ' + Math.min(parentLayout.columns || 12, frame.span || parentLayout.columns || 12);
          if (frame.h) box.style.minHeight = frame.h + 'px';
        } else {
          if (frame.w) box.style.width = 'min(100%, ' + frame.w + 'px)';
          if (frame.h) box.style.minHeight = frame.h + 'px';
        }
      },
      beginMove(ev, id, box, breakpoint) {
        if (ev.button !== 0) return;
        ev.preventDefault();
        const frame = templateCore.resolveFrame(this.state.doc.nodes[id], breakpoint);
        const start = { x: ev.clientX, y: ev.clientY, left: frame.x || 0, top: frame.y || 0 };
        const pointer = ev.pointerId;
        const target = ev.currentTarget;
        // Pointer capture may be unavailable for synthetic input or embedded
        // webviews. Listen on the owning window as well so movement continues
        // after the small handle itself is left.
        const eventTarget = target.ownerDocument && target.ownerDocument.defaultView || target;
        try { target.setPointerCapture(pointer); } catch { /* window fallback below */ }
        const move = (event) => {
          const x = Math.max(0, Math.round((start.left + event.clientX - start.x) / 8) * 8);
          const y = Math.max(0, Math.round((start.top + event.clientY - start.y) / 8) * 8);
          box.style.left = x + 'px'; box.style.top = y + 'px';
        };
        const cleanup = () => {
          eventTarget.removeEventListener('pointermove', move);
          eventTarget.removeEventListener('pointerup', done);
          eventTarget.removeEventListener('pointercancel', cancel);
        };
        const done = (event) => {
          cleanup();
          const x = Math.max(0, Math.round((start.left + event.clientX - start.x) / 8) * 8);
          const y = Math.max(0, Math.round((start.top + event.clientY - start.y) / 8) * 8);
          this.dispatch({ type: 'patchFrame', id, breakpoint, patch: { x, y } });
        };
        const cancel = () => { cleanup(); this.render(); };
        eventTarget.addEventListener('pointermove', move);
        eventTarget.addEventListener('pointerup', done);
        eventTarget.addEventListener('pointercancel', cancel);
      },
      beginResize(ev, id, box, breakpoint, parentLayout) {
        if (ev.button !== 0 || !parentLayout) return;
        ev.preventDefault(); ev.stopPropagation();
        const frame = templateCore.resolveFrame(this.state.doc.nodes[id], breakpoint);
        const rect = box.getBoundingClientRect();
        const startX = ev.clientX; const startY = ev.clientY;
        const target = ev.currentTarget;
        const eventTarget = target.ownerDocument && target.ownerDocument.defaultView || target;
        try { target.setPointerCapture(ev.pointerId); } catch { /* synthetic pointer */ }
        let patch = {};
        const move = (event) => {
          const width = Math.max(48, Math.round((rect.width + event.clientX - startX) / 8) * 8);
          const height = Math.max(48, Math.round((rect.height + event.clientY - startY) / 8) * 8);
          if (parentLayout.mode === 'grid') {
            const parent = box.parentElement;
            const colWidth = parent.clientWidth / Math.max(1, parentLayout.columns || 12);
            patch = { span: Math.min(parentLayout.columns || 12, Math.max(1, Math.round(width / colWidth))), h: height };
            box.style.gridColumn = 'span ' + patch.span;
          } else patch = { w: width, h: height };
          box.style.width = parentLayout.mode === 'grid' ? '' : width + 'px';
          box.style.minHeight = height + 'px';
        };
        const cleanup = () => { eventTarget.removeEventListener('pointermove', move); eventTarget.removeEventListener('pointerup', done); eventTarget.removeEventListener('pointercancel', cancel); };
        const done = () => { cleanup(); this.dispatch({ type: 'patchFrame', id, breakpoint, patch: Object.keys(patch).length ? patch : { w: frame.w || rect.width, h: frame.h || rect.height } }); };
        const cancel = () => { cleanup(); this.render(); };
        eventTarget.addEventListener('pointermove', move); eventTarget.addEventListener('pointerup', done); eventTarget.addEventListener('pointercancel', cancel);
      },
      renderInspector() {
        const node = this.state.doc.nodes[this.state.selectedId] || this.state.doc.nodes.root;
        const breakpoint = this.state.breakpoint;
        const layout = templateCore.resolveLayout(node, breakpoint);
        const ownLayout = node.layouts && node.layouts[breakpoint];
        const heading = el('div', 'ihtml-template-sidehead');
        heading.append(el('strong', '', T('template_properties')), el('small', '', breakpoint));
        this.dom.inspector.appendChild(heading);
        const field = (label, value, onChange, type = 'number', focusKey = label) => {
          const wrap = el('label', 'ihtml-template-field');
          wrap.appendChild(el('span', '', label));
          const input = el('input'); input.type = type; input.value = value == null ? '' : String(value);
          input.dataset.templateFocus = 'field-' + focusKey;
          input.dataset.templateId = node.id;
          if (type === 'number') { input.min = '0'; input.step = '1'; }
          input.onchange = () => onChange(type === 'number' ? (input.value === '' ? null : Number(input.value)) : input.value);
          wrap.appendChild(input); this.dom.inspector.appendChild(wrap);
        };
        field(T('template_name'), node.name, (name) => this.dispatch({ type: 'rename', id: node.id, name }), 'text');
        if (node.type !== 'slot') {
          const section = el('section', 'ihtml-template-prop-section');
          const title = el('div', 'ihtml-template-prop-title');
          title.appendChild(el('strong', '', T('template_mode')));
          if (breakpoint !== 'desktop' && !ownLayout) title.appendChild(el('small', '', T('template_inherited')));
          const modes = el('div', 'ihtml-template-segment');
          for (const [mode, label] of [['flow', T('template_flow')], ['grid', T('template_grid')], ['free', T('template_free')]]) {
            const button = el('button', '', label); button.setAttribute('aria-pressed', String(layout.mode === mode));
            button.dataset.templateFocus = 'mode-' + mode; button.dataset.templateId = node.id;
            button.onclick = () => this.dispatch({ type: 'patchLayout', id: node.id, breakpoint, patch: { mode } });
            modes.appendChild(button);
          }
          section.append(title, modes);
          this.dom.inspector.appendChild(section);
          if (layout.mode === 'flow') {
            const directionLabel = el('div', 'ihtml-template-field-label', T('template_direction'));
            const directions = el('div', 'ihtml-template-segment');
            for (const [direction, label] of [['column', T('template_column')], ['row', T('template_row')]]) {
              const button = el('button', '', label);
              button.dataset.templateFocus = 'direction-' + direction; button.dataset.templateId = node.id;
              button.setAttribute('aria-pressed', String((layout.direction || 'column') === direction));
              button.onclick = () => this.dispatch({ type: 'patchLayout', id: node.id, breakpoint, patch: { direction } });
              directions.appendChild(button);
            }
            this.dom.inspector.append(directionLabel, directions);
            const wrap = el('label', 'ihtml-template-check');
            const check = el('input'); check.type = 'checkbox'; check.checked = !!layout.wrap;
            check.dataset.templateFocus = 'wrap'; check.dataset.templateId = node.id;
            check.onchange = () => this.dispatch({ type: 'patchLayout', id: node.id, breakpoint, patch: { wrap: check.checked } });
            wrap.append(check, el('span', '', T('template_wrap')));
            this.dom.inspector.appendChild(wrap);
            const align = el('label', 'ihtml-template-field');
            align.appendChild(el('span', '', T('template_align')));
            const select = el('select');
            select.dataset.templateFocus = 'align'; select.dataset.templateId = node.id;
            for (const value of ['stretch', 'start', 'center', 'end']) {
              const option = el('option', '', value); option.value = value; option.selected = (layout.align || 'stretch') === value; select.appendChild(option);
            }
            select.onchange = () => this.dispatch({ type: 'patchLayout', id: node.id, breakpoint, patch: { align: select.value } });
            align.appendChild(select); this.dom.inspector.appendChild(align);
          }
          if (layout.mode === 'grid') field(T('template_columns'), layout.columns || 12, (columns) => this.dispatch({ type: 'patchLayout', id: node.id, breakpoint, patch: { columns } }));
          if (layout.mode === 'free') field(T('template_min_height'), layout.minHeight || 240, (minHeight) => this.dispatch({ type: 'patchLayout', id: node.id, breakpoint, patch: { minHeight } }));
          field(T('template_gap'), layout.gap, (gap) => this.dispatch({ type: 'patchLayout', id: node.id, breakpoint, patch: { gap } }));
          field(T('template_padding'), layout.padding, (padding) => this.dispatch({ type: 'patchLayout', id: node.id, breakpoint, patch: { padding } }));
        }
        if (node.id !== 'root') {
          const parent = this.state.doc.nodes[node.parent];
          const parentLayout = templateCore.resolveLayout(parent, breakpoint);
          const frame = templateCore.resolveFrame(node, breakpoint);
          const frameSection = el('div', 'ihtml-template-frame-grid');
          const appendNumber = (label, key, value) => {
            const wrap = el('label', 'ihtml-template-field'); wrap.appendChild(el('span', '', label));
            const input = el('input'); input.type = 'number'; input.min = '0'; input.value = value == null ? '' : String(value);
            input.dataset.templateFocus = 'frame-' + key; input.dataset.templateId = node.id;
            input.onchange = () => this.dispatch({ type: 'patchFrame', id: node.id, breakpoint, patch: { [key]: input.value === '' ? null : Number(input.value) } });
            wrap.appendChild(input); frameSection.appendChild(wrap);
          };
          if (parentLayout.mode === 'free') { appendNumber(T('template_x'), 'x', frame.x || 0); appendNumber(T('template_y'), 'y', frame.y || 0); }
          appendNumber(parentLayout.mode === 'grid' ? T('template_columns') : T('template_width'), parentLayout.mode === 'grid' ? 'span' : 'w', parentLayout.mode === 'grid' ? (frame.span || parentLayout.columns || 12) : frame.w);
          appendNumber(T('template_height'), 'h', frame.h);
          this.dom.inspector.appendChild(frameSection);
          const itemActions = el('div', 'ihtml-template-item-actions');
          const duplicate = el('button', '', T('template_duplicate'));
          duplicate.dataset.templateFocus = 'duplicate'; duplicate.dataset.templateId = node.id;
          duplicate.onclick = () => this.dispatch({ type: 'duplicate', id: node.id, newId: this.newID(node.type) });
          const remove = el('button', 'danger', T('template_delete'));
          remove.dataset.templateFocus = 'delete'; remove.dataset.templateId = node.id;
          remove.onclick = () => void this.removeSelected();
          itemActions.append(duplicate, remove); this.dom.inspector.appendChild(itemActions);
        }
      },
    };

    /* ---------- 宿主菜单槽 ---------- */
    if (opts.menu) {
      const menuHost = typeof opts.menu === 'string' ? doc.querySelector(opts.menu) : opts.menu;
      if (menuHost) {
        const menuRoot = el('div', 'ihtml-root ihtml-menu-root');
        const menuNav = el('nav', 'ihtml-hostmenu');
        menuRoot.appendChild(menuNav);
        menuHost.appendChild(menuRoot);
        themedRoots.push(menuRoot);
        addDisposer(() => menuRoot.remove());
        const renderMenu = () => {
          menuNav.innerHTML = '';
          const cur = nav.currentPage;
          nav.pagesList.forEach((p) => {
            const b = el('button', 'mi' + (p.name === cur ? ' active' : ''));
            b.type = 'button';
            if (p.name === cur) b.setAttribute('aria-current', 'page');
            if (p.icon) b.appendChild(el('span', 'ico', p.icon));
            b.appendChild(el('span', '', p.title));
            b.onclick = () => nav.go(p.name);
            menuNav.appendChild(b);
          });
          menuRoot.style.display = nav.pagesList.length ? '' : 'none';
        };
        addDisposer(nav.onChange(renderMenu));
        renderMenu();
      }
    }

    /* ---------- 空状态 ---------- */
    const emptyState = {
      node: null,
      show() {
        if (this.node || SAFE_MODE) return;
        const box = el('div', 'ihtml-empty');
        box.appendChild(el('h1', '', T('empty_title')));
        if (readOnly) {
          box.appendChild(el('p', '', T('readonly_empty')));
        } else if (chat.enabled) {
          box.appendChild(el('p', '', T('empty_chat_hint')));
          const btn = el('button', 'ui-btn primary', T('empty_start'));
          btn.onclick = () => chat.open();
          box.appendChild(btn);
        } else {
          box.appendChild(el('p', '', T('empty_api_hint')));
          const hint = el('div', 'hint');
          hint.textContent = 'curl -X PUT ' + shellQuote(http.url('api/items')) + " \\\n  -H 'Content-Type: application/json' \\\n  -d '{\"items\":[{\"id\":\"hello\",\"type\":\"html\",\"order\":1,\n       \"content\":\"<div class=\\\"ui-card\\\" style=\\\"margin:24px\\\">Hello ihtml</div>\"}]}'";
          box.appendChild(hint);
        }
        shell.parentFor('').appendChild(box);
        this.node = box;
      },
      hide() {
        if (this.node) { this.node.remove(); this.node = null; }
      },
    };

    /* ---------- 安全模式 ---------- */
    let safeModeNode = null;
    async function renderSafeMode() {
      if (safeModeNode) safeModeNode.remove();
      const box = el('div', 'ihtml-empty');
      safeModeNode = box;
      box.appendChild(el('h1', '', T('safe_title')));
      box.appendChild(el('p', '', T(readOnly ? 'safe_readonly_hint' : 'safe_hint')));
      const list = el('div');
      list.style.cssText = 'display:flex;flex-direction:column;gap:8px;width:min(560px,92vw)';
      box.appendChild(list);
      if (standalone) {
        const exit = el('button', 'ui-btn primary', T('safe_exit'));
        exit.onclick = () => {
          const u = new URL(location.href);
          u.searchParams.delete('safe');
          u.searchParams.delete('ihtml-safe');
          location.href = u.toString();
        };
        box.appendChild(exit);
      }
      appEl.appendChild(box);
      renderHistoryList(list, () => location.reload());
    }

    // 历史快照列表(安全模式与管理面板共用)
    async function renderHistoryList(list, afterRollback, isCurrent) {
      list.textContent = '';
      try {
        const out = await http.get('api/revisions');
        if (isCurrent && !isCurrent()) return;
        for (const rev of (out.revisions || []).slice(0, 20)) {
          const row = el('div', 'ui-card');
          row.style.cssText = 'display:flex;align-items:center;gap:10px;padding:10px 14px;box-shadow:none';
          const label = el('div');
          label.appendChild(el('div', '', (rev.note || rev.id) + ' · ' + T('items_label', { n: rev.item_count })));
          label.appendChild(el('div', 'ui-muted', new Date(rev.created_at).toLocaleString()));
          label.style.flex = '1';
          row.appendChild(label);
          if (!readOnly) {
            const btn = el('button', 'ui-btn', T('rollback'));
            btn.onclick = async () => {
              if (!(await ui.confirm({ title: T('rollback_confirm_title'), text: T('rollback_confirm') }))) return;
              btn.disabled = true;
              try {
                await http.post('api/revisions/' + encodeURIComponent(rev.id) + '/rollback');
                if (!SAFE_MODE) await enqueueState(() => loadCurrentState());
                ui.toast(T('rolled_back'), 'success');
                if (afterRollback) afterRollback();
              } catch (err) {
                ui.toast(String(err && err.message || err), 'error');
              } finally {
                if (btn.isConnected) btn.disabled = false;
              }
            };
            row.appendChild(btn);
          }
          list.appendChild(row);
        }
        if (!(out.revisions || []).length) list.appendChild(el('p', 'ui-muted', T('history_empty')));
      } catch (e) {
        if (isCurrent && !isCurrent()) return;
        list.appendChild(el('p', 'ui-muted', String(e.message || e)));
      }
    }

    /* ---------- 管理面板(齿轮): 主题/历史/错误 —— 无 AI 也能自助管理与救砖 ---------- */
    const panel = {
      dom: null,
      previousFocus: null,
      renderToken: 0,
      open(tab) {
        chat.close();
        if (readOnly && (tab === 'history' || tab === 'layout')) tab = 'theme';
        if (!this.dom) this.build();
        this.previousFocus = doc.activeElement;
        this.dom.mask.style.display = '';
        this.dom.box.style.display = '';
        this.switch(tab || 'theme');
        const selected = this.dom.tabs.find((b) => b.getAttribute('aria-selected') === 'true');
        if (selected) selected.focus();
      },
      close() {
        if (!this.dom) return;
        this.dom.mask.style.display = 'none';
        this.dom.box.style.display = 'none';
        if (this.previousFocus && this.previousFocus.isConnected && typeof this.previousFocus.focus === 'function') this.previousFocus.focus();
        this.previousFocus = null;
      },
      reset() {
        this.renderToken++;
        if (!this.dom) return;
        this.close();
        this.dom.mask.remove();
        this.dom.box.remove();
        this.dom = null;
      },
      switch(tab) {
        const d = this.dom;
        if (!d.tabs.some((button) => button.dataset.tab === tab)) tab = 'theme';
        const token = ++this.renderToken;
        d.tabs.forEach((b) => {
          const active = b.dataset.tab === tab;
          b.classList.toggle('active', active);
          b.setAttribute('aria-selected', String(active));
          b.tabIndex = active ? 0 : -1;
          if (active) d.body.setAttribute('aria-labelledby', b.id);
        });
        d.body.dataset.tab = tab;
        d.body.textContent = '';
        if (tab === 'theme') this.renderTheme(d.body);
        else if (tab === 'layout') this.renderLayout(d.body);
        else if (tab === 'history') renderHistoryList(d.body, () => { this.close(); }, () => this.renderToken === token);
        else this.renderErrors(d.body, token);
      },
      renderLayout(body) {
        body.appendChild(el('p', 'ui-muted', T('item_editor_panel_hint')));
        const row = el('div', 'prow ihtml-layout-entry-actions');
        const editItems = el('button', 'ui-btn primary', T('item_editor_edit_items'));
        editItems.onclick = () => { this.close(); void itemEditor.open(); };
        const editTemplate = el('button', 'ui-btn', T('item_editor_advanced_template'));
        editTemplate.onclick = () => { this.close(); void templateStudio.open(); };
        row.append(editItems, editTemplate);
        body.append(row, el('p', 'ui-muted ihtml-layout-entry-note', T('item_editor_template_hint')));
      },
      build() {
        const mask = el('div', 'ihtml-chat-mask');
        mask.onclick = () => this.close();
        const box = el('div', 'ihtml-panel');
        box.setAttribute('role', 'dialog');
        box.setAttribute('aria-modal', 'true');
        box.setAttribute('aria-labelledby', 'ihtml-panel-title');
        box.addEventListener('keydown', (ev) => handleDialogKey(ev, box, () => this.close()));
        const head = el('div', 'head');
        const heading = el('h2', 'title', T('panel_title'));
        heading.id = 'ihtml-panel-title';
        head.appendChild(heading);
        const closeBtn = el('button', 'close', T('close'));
        closeBtn.type = 'button';
        closeBtn.onclick = () => this.close();
        head.appendChild(closeBtn);
        const tabBar = el('div', 'tabs');
        tabBar.setAttribute('role', 'tablist');
        tabBar.setAttribute('aria-label', T('panel_title'));
        const tabs = [];
        const defs = [['theme', T('panel_theme')], ['layout', T('panel_layout')], ['history', T('panel_history')], ['errors', T('panel_errors')]];
        for (const [key, label] of defs) {
          if (readOnly && (key === 'history' || key === 'layout')) continue;
          const b = el('button', '', label);
          b.type = 'button';
          b.dataset.tab = key;
          b.id = 'ihtml-panel-tab-' + key;
          b.setAttribute('role', 'tab');
          b.setAttribute('aria-controls', 'ihtml-panel-body');
          b.onclick = () => this.switch(key);
          tabBar.appendChild(b);
          tabs.push(b);
        }
        tabBar.addEventListener('keydown', (ev) => {
          if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(ev.key)) return;
          ev.preventDefault();
          const current = tabs.indexOf(doc.activeElement);
          let next = current;
          if (ev.key === 'Home') next = 0;
          else if (ev.key === 'End') next = tabs.length - 1;
          else next = (current + (ev.key === 'ArrowLeft' ? -1 : 1) + tabs.length) % tabs.length;
          if (tabs[next]) { this.switch(tabs[next].dataset.tab); tabs[next].focus(); }
        });
        const body = el('div', 'pbody');
        body.id = 'ihtml-panel-body';
        body.setAttribute('role', 'tabpanel');
        box.append(head, tabBar, body);
        rootEl.append(mask, box);
        this.dom = { mask, box, body, tabs };
        mask.style.display = 'none';
        box.style.display = 'none';
      },
      renderTheme(body) {
        const cur = theme.get();
        const effectivePrimary = cur.primary || '#4f46e5';
        const effectiveRadius = Number(cur.radius) || 10;
        const row1 = el('div', 'prow');
        row1.appendChild(el('span', 'plabel', T('theme_mode')));
        for (const [mode, label] of [['auto', T('theme_auto')], ['light', T('theme_light')], ['dark', T('theme_dark')]]) {
          const b = el('button', 'ui-btn' + (cur.mode === mode ? ' primary' : ''), label);
          b.type = 'button';
          b.dataset.themeControl = 'mode';
          b.dataset.themeValue = mode;
          b.setAttribute('aria-pressed', String(cur.mode === mode));
          b.onclick = () => { theme.set({ mode }); this.renderThemeAgain(body); };
          row1.appendChild(b);
        }
        body.appendChild(row1);
        const row2 = el('div', 'prow');
        row2.appendChild(el('span', 'plabel', T('theme_primary')));
        for (const c of ['#4f46e5', '#0e7490', '#047857', '#b45309', '#b91c1c', '#7c3aed', '#c99a45']) {
          const dot = el('button', 'pswatch');
          dot.type = 'button';
          dot.dataset.themeControl = 'primary';
          dot.dataset.themeValue = c;
          dot.setAttribute('aria-label', T('theme_color', { color: c }));
          dot.setAttribute('aria-pressed', String(effectivePrimary === c));
          dot.style.background = c;
          if (effectivePrimary === c) dot.classList.add('cur');
          dot.onclick = () => { theme.set({ primary: c }); this.renderThemeAgain(body); };
          row2.appendChild(dot);
        }
        body.appendChild(row2);
        const row3 = el('div', 'prow');
        row3.appendChild(el('span', 'plabel', T('theme_radius')));
        for (const rVal of [6, 10, 14]) {
          const b = el('button', 'ui-btn' + (effectiveRadius === rVal ? ' primary' : ''), rVal + 'px');
          b.type = 'button';
          b.dataset.themeControl = 'radius';
          b.dataset.themeValue = String(rVal);
          b.setAttribute('aria-pressed', String(effectiveRadius === rVal));
          b.onclick = () => { theme.set({ radius: rVal }); this.renderThemeAgain(body); };
          row3.appendChild(b);
        }
        body.appendChild(row3);
      },
      renderThemeAgain(body) {
        const active = doc.activeElement;
        const control = active && active.dataset && active.dataset.themeControl;
        const value = active && active.dataset && active.dataset.themeValue;
        body.textContent = '';
        this.renderTheme(body);
        if (control && value != null) {
          const next = Array.from(body.querySelectorAll('[data-theme-control]'))
            .find((node) => node.dataset.themeControl === control && node.dataset.themeValue === value);
          if (next) next.focus();
        }
      },
      async renderErrors(body, token) {
        body.appendChild(el('p', 'ui-muted', T('loading')));
        let list = errors.list();
        try {
          const out = await http.get('api/errors');
          const remote = (out && out.errors) || [];
          const seen = new Set(list.map((e) => [e.time, e.item_id, e.message].join('|')));
          for (const e of remote) {
            const key = [e.time, e.item_id, e.message].join('|');
            if (!seen.has(key)) { seen.add(key); list.push(e); }
          }
        } catch { /* 本地错误仍可展示 */ }
        if (this.renderToken !== token || body.dataset.tab !== 'errors') return;
        body.replaceChildren();
        list = list.sort((a, b) => String(b.time || '').localeCompare(String(a.time || ''))).slice(0, 20);
        if (!list.length) { body.appendChild(el('p', 'ui-muted', T('errors_empty'))); return; }
        for (const e of list) {
          const row = el('div', 'perr');
          row.appendChild(el('div', 'pid', (e.item_id || '(page)') + ' · ' + new Date(e.time).toLocaleTimeString()));
          row.appendChild(el('div', 'pmsg', e.message));
          body.appendChild(row);
        }
      },
    };

    /* ---------- SSE: 手动指数退避重连 + 离线指示 ---------- */
    let sse = null;
    let sseRetry = 0;
    let sseTimer = 0;
    let sseVersion = 0;
    let sseQueue = Promise.resolve();
    let sseGeneration = 0;
    let sseSynced = false;
    const sseWaiters = new Set();
    let offlineDot = null;
    let connState = 'idle';
    function setConnState(state) {
      if (connState === state) return;
      connState = state;
      bus.emit('ihtml:connection', { state });
      if (typeof opts.onConnection === 'function') { try { opts.onConnection(state); } catch { /* 忽略 */ } }
      if (state === 'offline') {
        if (!offlineDot) {
          offlineDot = el('div', 'ihtml-offline', T('offline'));
          offlineDot.setAttribute('role', 'status');
          offlineDot.setAttribute('aria-live', 'polite');
          rootEl.appendChild(offlineDot);
        }
      } else if (offlineDot) {
        offlineDot.remove();
        offlineDot = null;
      }
    }
    async function resyncState(reason, version, generation = sseGeneration) {
      const current = () => generation === sseGeneration && !destroyed;
      await nav.loadPages(false, current);
      if (generation !== sseGeneration) throw staleRequestError();
      nav.reconcile(nav.currentPage, nav.currentParams);
      await templateRuntime.load(nav.currentPage, current);
      if (generation !== sseGeneration) throw staleRequestError();
      await refreshItems(current);
      if (generation !== sseGeneration) throw staleRequestError();
      await grid.load(current);
      if (generation !== sseGeneration) throw staleRequestError();
      await itemEditor.load(current);
      if (generation !== sseGeneration) throw staleRequestError();
      nav.settledPage = nav.currentPage;
      nav.emitChange();
      if (version > 0) sseVersion = version;
      bus.emit('ihtml:resynced', { reason: reason || 'server', version: sseVersion });
    }
    function markSSESynced() {
      sseSynced = true;
      for (const waiter of sseWaiters) {
        clearTimeout(waiter.timer);
        waiter.resolve(true);
      }
      sseWaiters.clear();
    }
    function waitForSSESync(timeout = 1200) {
      if (sseSynced) return Promise.resolve(true);
      return new Promise((resolve) => {
        const waiter = { resolve, timer: 0 };
        waiter.timer = setTimeout(() => {
          sseWaiters.delete(waiter);
          resolve(false);
        }, timeout);
        sseWaiters.add(waiter);
      });
    }
    function stopSSE() {
      sseGeneration++;
      clearTimeout(sseTimer);
      if (sse) sse.close();
      sse = null;
      sseSynced = false;
      for (const waiter of sseWaiters) {
        clearTimeout(waiter.timer);
        waiter.resolve(false);
      }
      sseWaiters.clear();
      sseRetry = 0;
      connState = 'idle';
      if (offlineDot) { offlineDot.remove(); offlineDot = null; }
    }
    async function handleSSEData(data, generation = sseGeneration) {
      if (generation !== sseGeneration || destroyed) throw staleRequestError();
      const version = Number(data && data.version) || 0;
      if (data.type === 'resync') {
        await resyncState(data.reason, version, generation);
        if (generation !== sseGeneration || destroyed) throw staleRequestError();
        markSSESynced();
        return;
      }
      if (version && sseVersion && version <= sseVersion) return;
      if (version && sseVersion && version !== sseVersion + 1) {
        await resyncState('version-gap', version, generation);
        return;
      }
      if (data.type === 'items') {
        if (data.op === 'put') {
          const applyBatch = ++applyGeneration;
          for (const it of data.items || []) {
            if (!it.page || it.page === nav.currentPage) await applyItem(it, applyBatch);
            else removeItem(it.id);
            if (generation !== sseGeneration || destroyed) throw staleRequestError();
          }
          if (!registry.size) emptyState.show(); else emptyState.hide();
        } else if (data.op === 'delete') {
          applyGeneration++;
          (data.ids || []).forEach(removeItem);
          if (!registry.size) emptyState.show();
        } else {
          await refreshItems(() => generation === sseGeneration && !destroyed);
        }
        grid.apply();
        itemEditor.syncRecords();
        bus.emit('ihtml:items-changed', data);
      } else if (data.type === 'pages') {
        await resyncState('pages-changed', version, generation);
        return;
      } else if (data.type === 'templates') {
        await templateRuntime.load(nav.currentPage, () => generation === sseGeneration && !destroyed);
        itemEditor.apply();
        bus.emit('ihtml:templates-changed', data);
      } else if (data.type === 'kv') {
        if (data.key === 'theme' && !hostTheme) await themePullRemote();
        if (generation !== sseGeneration || destroyed) throw staleRequestError();
        if (data.key && data.key.indexOf('layout.') === 0 && data.key === grid.key() && !grid.editing) {
          await grid.load(() => generation === sseGeneration && !destroyed);
        }
        if (data.key === itemEditor.key()) await itemEditor.remoteChanged();
        bus.emit('ihtml:kv-changed', data);
      }
      if (generation !== sseGeneration || destroyed) throw staleRequestError();
      if (version > 0) sseVersion = version;
    }
    async function connectSSE() {
      if (SAFE_MODE || destroyed) return;
      if (sse) { sse.close(); sse = null; }
      const generation = ++sseGeneration;
      applyGeneration++;
      sseSynced = false;
      sseVersion = 0;
      sseQueue = Promise.resolve();
      let ticket = '';
      try {
        ticket = await evalConnectionAuth();
      } catch (err) {
        if (generation !== sseGeneration || destroyed) return;
        setConnState('offline');
        clearTimeout(sseTimer);
        sseTimer = setTimeout(connectSSE, 3000);
        return;
      }
      if (generation !== sseGeneration || destroyed) return;
      const source = new EventSource(apiURL('api/events', true, ticket));
      sse = source;
      source.onopen = () => {
        if (sse !== source || generation !== sseGeneration || destroyed) return;
        sseRetry = 0;
        setConnState('online');
      };
      source.onerror = () => {
        // 不依赖 EventSource 内建重试(HTTP 错误会永久停止): 主动关闭指数退避重建
        source.close();
        if (sse !== source || generation !== sseGeneration || destroyed) return;
        sse = null;
        sseRetry = Math.min(sseRetry + 1, 6);
        if (sseRetry >= 2) setConnState('offline');
        clearTimeout(sseTimer);
        sseTimer = setTimeout(connectSSE, Math.min(30000, 1000 * Math.pow(2, sseRetry)));
      };
      source.onmessage = (ev) => {
        let data = null;
        try { data = JSON.parse(ev.data); } catch { return; }
        sseQueue = enqueueState(() => {
          if (destroyed || sse !== source || generation !== sseGeneration) return;
          return handleSSEData(data, generation);
        }).catch((err) => {
          if (!destroyed && !isStaleError(err)) {
            errors.report('', 'live update: ' + (err && err.message || err), err && err.stack);
          }
        });
      };
    }
    addDisposer(stopSSE);

    /* ---------- 助手回复 HTML 白名单清洗 ---------- */
    function sanitizeAssistantHTML(html) {
      const ALLOW = new Set(['P', 'BR', 'HR', 'STRONG', 'EM', 'B', 'I', 'U', 'S', 'DEL', 'CODE', 'PRE',
        'BLOCKQUOTE', 'UL', 'OL', 'LI', 'TABLE', 'THEAD', 'TBODY', 'TFOOT', 'TR', 'TH', 'TD',
        'H1', 'H2', 'H3', 'H4', 'H5', 'H6', 'A', 'SPAN', 'DIV', 'SMALL', 'SUB', 'SUP']);
      const DROP = new Set(['SCRIPT', 'STYLE', 'IFRAME', 'OBJECT', 'EMBED', 'LINK', 'META', 'FORM',
        'INPUT', 'BUTTON', 'TEXTAREA', 'SELECT', 'SVG', 'MATH', 'TEMPLATE', 'BASE']);
      const parsed = new DOMParser().parseFromString(String(html || ''), 'text/html');
      function walk(srcNode, dst) {
        for (const child of Array.from(srcNode.childNodes)) {
          if (child.nodeType === Node.TEXT_NODE) { dst.appendChild(doc.createTextNode(child.nodeValue)); continue; }
          if (child.nodeType !== Node.ELEMENT_NODE) continue;
          const tag = child.tagName.toUpperCase();
          if (DROP.has(tag)) continue;
          if (!ALLOW.has(tag)) { walk(child, dst); continue; }
          const node = doc.createElement(tag.toLowerCase());
          if (tag === 'A') {
            const href = String(child.getAttribute('href') || '');
            if (/^(https?:|mailto:)/i.test(href)) {
              node.setAttribute('href', href);
              node.setAttribute('target', '_blank');
              node.setAttribute('rel', 'noopener noreferrer');
            }
          }
          dst.appendChild(node);
          walk(child, node);
        }
      }
      const frag = doc.createDocumentFragment();
      walk(parsed.body, frag);
      return frag;
    }

    /* ---------- AI 对话抽屉 ---------- */
    const TOOL_STATUS_LABEL = {
      running: T('tool_running'), pending: T('tool_pending'), success: T('tool_success'),
      error: T('tool_error'), cancelled: T('tool_cancelled'),
    };
    const RISK_LABEL = { low: T('risk_low'), medium: T('risk_medium'), high: T('risk_high'), blocked: T('risk_blocked') };

    function pretty(v) {
      if (v === undefined || v === null) return '';
      try {
        if (typeof v === 'string') return JSON.stringify(JSON.parse(v), null, 2);
        return JSON.stringify(v, null, 2);
      } catch { return String(v); }
    }
    function isTechnicalSecretKey(key) {
      const normalized = String(key == null ? '' : key).toLowerCase().replace(/[^a-z0-9]/g, '');
      return !!normalized && /(?:authorization|auth|token|secret|password|passwd|cookie|credential|apikey|session|sessionid|sessionkey|nonce|signature|privatekey)$/.test(normalized);
    }
    function redactTechnical(value, depth = 0, seen = new WeakSet(), keyName = '') {
      if (value == null || typeof value === 'number' || typeof value === 'boolean') return value;
      if (typeof value === 'string') {
        if (isTechnicalSecretKey(keyName)) return '[redacted]';
        if (/content|template|source|html|code|value/i.test(keyName) && value.length > 240) {
          return { omitted: true, characters: value.length, preview: value.slice(0, 160) + '…' };
        }
        return clip(value, 4000);
      }
      if (depth >= 6) return '[depth limited]';
      if (typeof value !== 'object') return clip(String(value), 4000);
      if (seen.has(value)) return '[circular]';
      seen.add(value);
      if (Array.isArray(value)) return value.slice(0, 50).map((item) => redactTechnical(item, depth + 1, seen, keyName));
      const out = {};
      for (const [key, item] of Object.entries(value).slice(0, 100)) {
        if (isTechnicalSecretKey(key)) continue;
        out[key] = redactTechnical(item, depth + 1, seen, key);
      }
      return out;
    }
    function technicalText(value, max = 16000) {
      const text = pretty(redactTechnical(value));
      return text.length > max ? text.slice(0, max) + '\n…' : text;
    }
    function renderAssistant(text) {
      let html = String(text || '');
      if (!/<[a-z][^>]*>/i.test(html)) {
        html = html
          .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
          .replace(/```([\s\S]*?)```/g, (_, code) => '<pre>' + code.replace(/^\w*\n/, '') + '</pre>')
          .replace(/\*\*([^*\n]+)\*\*/g, '<b>$1</b>')
          .replace(/`([^`\n]+)`/g, '<code>$1</code>')
          .replace(/^[-•] (.+)$/gm, '· $1')
          .replace(/\n/g, '<br>');
      }
      return sanitizeAssistantHTML(html);
    }

    // 稳定会话号: 存 sessionStorage, 断线/刷新重连恢复上下文; 新会话=换号
    function chatSessionID(renew) {
      let id = '';
      try { id = sessionStorage.getItem('ihtml.csid') || ''; } catch { /* 忽略 */ }
      if (!id || renew) {
        id = 'c' + Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
        try { sessionStorage.setItem('ihtml.csid', id); } catch { /* 忽略 */ }
      }
      return id;
    }

    const chat = {
      enabled: false,
      entry: null,
      dom: null,
      log: null,
      ws: null,
      wsPromise: null,
      wsGeneration: 0,
      active: false,
      sending: false,
      sendGeneration: 0,
      follow: true,
      model: '',
      modelInfo: null,
      modelPromise: null,
      modelLoadSeq: 0,
      agentMode: 'fast',
      agentModes: ['fast'],
      deepApprovalResume: '',
      availabilityNote: null,
      turn: null,
      turnsByRun: new Map(),
      toolDomSeq: 0,
      scrollFrame: 0,
      announcementTimer: 0,
      lastAnnouncement: '',
      announcementQueue: [],
      announcementBusy: false,
      everConnected: false,
      previousFocus: null,
      obscuredStudio: null,

      init(cfg) {
        this.enabled = !!(cfg && cfg.chat);
        if (this.dom && this.dom.readonlyBadge) this.dom.readonlyBadge.hidden = !readOnly;
        if (this.entry) {
          this.entry.style.display = this.enabled && chatEntryEnabled ? '' : 'none';
          syncActionDock();
          return;
        }
        if (!this.enabled || !chatEntryEnabled) return;
        const entry = el('button', 'ihtml-chat-entry');
        entry.type = 'button';
        entry.textContent = T('chat_entry');
        entry.setAttribute('aria-haspopup', 'dialog');
        entry.onclick = () => this.open();
        this.entry = entry;
        actionDock().appendChild(entry);
        syncActionDock();
      },
      open() {
        if (!this.enabled) return;
        panel.close();
        if (!this.dom) this.build();
        this.previousFocus = doc.activeElement;
        const studioShell = templateStudio.dom && templateStudio.dom.shell;
        if (studioShell && !studioShell.hidden) {
          studioShell.inert = true;
          studioShell.setAttribute('aria-hidden', 'true');
          this.obscuredStudio = studioShell;
        }
        this.dom.mask.style.display = '';
        this.dom.panel.style.display = '';
        rootEl.classList.add('ihtml-chat-open');
        this.dom.input.focus();
        if (!this.modelInfo) void this.loadModels().catch((err) => {
          if (!(err && err.stale)) ui.toast(err.message || T('chat_broken'), 'error');
        });
      },
      close() {
        if (!this.dom) return;
        this.dom.mask.style.display = 'none';
        this.dom.panel.style.display = 'none';
        rootEl.classList.remove('ihtml-chat-open');
        if (this.obscuredStudio) {
          this.obscuredStudio.inert = false;
          this.obscuredStudio.removeAttribute('aria-hidden');
          this.obscuredStudio = null;
        }
        if (this.previousFocus && this.previousFocus.isConnected && typeof this.previousFocus.focus === 'function') this.previousFocus.focus();
        this.previousFocus = null;
      },

      build() {
        const chatID = 'ihtml-chat-' + instanceID + '-' + (++dialogSeq);
        const mask = el('div', 'ihtml-chat-mask');
        mask.onclick = () => this.close();
        const panelEl = el('div', 'ihtml-chat');
        panelEl.setAttribute('role', 'dialog');
        panelEl.setAttribute('aria-modal', 'true');
        panelEl.setAttribute('aria-labelledby', chatID + '-title');

        const head = el('div', 'ihtml-chat-head');
        const title = el('h2', 'title', T('chat_title'));
        title.id = chatID + '-title';
        const readonlyBadge = el('span', 'ihtml-readonly-badge', T('readonly_note'));
        readonlyBadge.hidden = !readOnly;
        readonlyBadge.dataset.short = T('readonly_short');
        const newBtn = el('button', '', T('new_session'));
        newBtn.type = 'button';
        newBtn.onclick = () => this.reset();
        const closeBtn = el('button', 'close', T('close'));
        closeBtn.type = 'button';
        closeBtn.onclick = () => this.close();
        head.append(title, readonlyBadge, newBtn, closeBtn);

        const toolbar = el('div', 'ihtml-chat-toolbar');
        toolbar.style.display = 'none'; // 模型选择器默认隐藏(model_select 下发才显示)
        const providerBadge = el('span', 'provider');
        providerBadge.hidden = true;
        const modelInput = el('input', 'model');
        modelInput.name = 'ihtml-model';
        modelInput.placeholder = T('model_placeholder');
        modelInput.setAttribute('list', chatID + '-model-list');
        const syncModelInput = () => this.saveModel(modelInput.value.trim());
        modelInput.oninput = syncModelInput;
        modelInput.onchange = syncModelInput;
        const dataList = el('datalist');
        dataList.id = chatID + '-model-list';
        const freshBtn = el('button', 'fresh', T('refresh_models'));
        freshBtn.type = 'button';
        freshBtn.onclick = () => { void this.loadModels(true).catch((err) => {
          if (!(err && err.stale)) ui.toast(err.message || T('chat_broken'), 'error');
        }); };
        toolbar.append(providerBadge, modelInput, freshBtn, dataList);

        const modebar = el('div', 'ihtml-agent-modebar');
        modebar.hidden = true;
        const modeLabel = el('span', 'label', T('agent_mode_label'));
        const modeGroup = el('div', 'ihtml-agent-mode-group');
        modeGroup.setAttribute('role', 'radiogroup');
        modeGroup.setAttribute('aria-label', T('agent_mode_label'));
        const modeButtons = {};
        for (const [mode, label] of [['fast', T('agent_fast')], ['deep', T('agent_deep')]]) {
          const button = el('button', '', label);
          button.type = 'button';
          button.dataset.agentMode = mode;
          button.setAttribute('role', 'radio');
          button.setAttribute('aria-checked', String(mode === 'fast'));
          button.onclick = () => this.setAgentMode(mode, true);
          modeButtons[mode] = button;
          modeGroup.appendChild(button);
        }
        modeGroup.addEventListener('keydown', (ev) => {
          if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(ev.key) || this.active || this.sending) return;
          ev.preventDefault();
          const available = this.agentModes.filter((mode) => modeButtons[mode] && !modeButtons[mode].disabled);
          if (!available.length) return;
          const current = Math.max(0, available.indexOf(this.agentMode));
          const delta = ev.key === 'ArrowLeft' || ev.key === 'ArrowUp' ? -1 : 1;
          const next = available[(current + delta + available.length) % available.length];
          if (next) { this.setAgentMode(next, true); modeButtons[next].focus(); }
        });
        const modeHint = el('small', 'hint', T('agent_fast_hint'));
        modebar.append(modeLabel, modeGroup, modeHint);

        const log = el('div', 'ihtml-chat-log');
        log.setAttribute('role', 'log');
        log.setAttribute('aria-label', T('conversation'));
        // 流式正文和推理只负责视觉呈现。读屏播报走下面独立的、简短的
        // status 通道，避免每个 token 与最终全文被重复朗读。
        log.setAttribute('aria-live', 'off');
        log.addEventListener('scroll', () => {
          this.follow = log.scrollTop + log.clientHeight >= log.scrollHeight - 48;
        });

        const foot = el('div', 'ihtml-chat-foot');
        const compose = el('div', 'compose');
        const input = el('textarea');
        input.name = 'ihtml-composer';
        input.placeholder = T('composer_placeholder');
        input.setAttribute('aria-label', T('composer_placeholder'));
        input.setAttribute('aria-describedby', chatID + '-composer-hint');
        input.rows = 1;
        input.addEventListener('input', () => {
          this.resizeComposer(); this.syncChatControls();
        });
        let composing = false;
        let composedAt = 0;
        input.addEventListener('compositionstart', () => { composing = true; });
        input.addEventListener('compositionend', () => { composing = false; composedAt = Date.now(); });
        input.addEventListener('blur', () => { composing = false; });
        input.addEventListener('keydown', (ev) => {
          const imeActive = ev.isComposing || composing || ev.keyCode === 229;
          if (ev.key === 'Escape') {
            if (imeActive) { ev.stopPropagation(); return; }
            ev.preventDefault(); ev.stopPropagation(); this.close(); return;
          }
          if (ev.key !== 'Enter') return;
          if (rootEl.classList.contains('ihtml-mobile')) return;
          if (ev.shiftKey || ev.altKey || ev.ctrlKey || ev.metaKey) return;
          if (imeActive || Date.now() - composedAt < 80) return;
          ev.preventDefault();
          this.send();
        });
        const sendBtn = el('button', '', T('send'));
        sendBtn.type = 'button';
        sendBtn.onclick = () => (this.active || this.sending ? this.cancel() : this.send());
        const hint = el('div', 'hint', T('composer_hint'));
        hint.id = chatID + '-composer-hint';
        compose.append(input, hint);
        foot.append(compose, sendBtn);

        const live = el('div', 'ihtml-sr-only ihtml-sr-status');
        live.setAttribute('role', 'status');
        live.setAttribute('aria-live', 'polite');
        live.setAttribute('aria-atomic', 'true');

        panelEl.addEventListener('keydown', (ev) => handleDialogKey(ev, panelEl, () => this.close()));
        panelEl.append(head, toolbar, modebar, log, foot, live);
        rootEl.append(mask, panelEl);
        this.dom = { mask, panel: panelEl, log, foot, input, sendBtn, providerBadge, modelInput, dataList, freshBtn, toolbar, modebar, modeButtons, modeHint, live, readonlyBadge };
        this.log = log;
        this.resizeComposer(); this.syncChatControls();
        this.notice(T('welcome'));
      },

      notice(html) {
        const card = el('article', 'ihtml-msg assistant');
        card.appendChild(el('div', 'author', 'AI'));
        const answer = el('div', 'answer');
        answer.replaceChildren(sanitizeAssistantHTML('<p>' + html + '</p>'));
        card.appendChild(answer);
        this.log.appendChild(card);
        this.scroll();
      },
      systemNote(text) {
        this.log.appendChild(el('div', 'ihtml-sysnote', text));
        this.announce(text);
        this.scroll();
      },
      announce(text) {
        text = clip(String(text || '').trim(), 180);
        if (!text || !this.dom || !this.dom.live) return;
        if (text === this.lastAnnouncement || this.announcementQueue.includes(text)) return;
        this.announcementQueue.push(text);
        if (this.announcementQueue.length > 6) this.announcementQueue.splice(0, this.announcementQueue.length - 6);
        this.flushAnnouncements();
      },
      flushAnnouncements() {
        if (this.announcementBusy || !this.announcementQueue.length || !this.dom || !this.dom.live) return;
        const text = this.announcementQueue.shift();
        this.announcementBusy = true;
        this.lastAnnouncement = text;
        this.dom.live.textContent = '';
        this.announcementTimer = setTimeout(() => {
          if (this.dom && this.dom.live) this.dom.live.textContent = text;
          this.announcementTimer = setTimeout(() => {
            this.announcementTimer = 0;
            this.announcementBusy = false;
            this.flushAnnouncements();
          }, 450);
        }, 20);
      },

      setAvailabilityNote(show) {
        if (!this.log) return;
        if (!show) {
          if (this.availabilityNote) this.availabilityNote.remove();
          this.availabilityNote = null;
          return;
        }
        if (!this.availabilityNote || !this.availabilityNote.isConnected) {
          this.availabilityNote = el('div', 'ihtml-sysnote ihtml-ai-unavailable', T('ai_unavailable'));
          this.log.appendChild(this.availabilityNote);
        } else this.availabilityNote.textContent = T('ai_unavailable');
        this.scroll();
      },
      setAgentMode(mode, persist) {
        this.agentMode = chatAgentCore.selectMode(this.agentModes, mode);
        const deepAvailable = this.agentModes.includes('deep');
        if (this.dom) {
          this.dom.modebar.hidden = !deepAvailable;
          this.dom.panel.classList.toggle('has-agent-modes', deepAvailable);
          for (const [key, button] of Object.entries(this.dom.modeButtons || {})) {
            const selected = key === this.agentMode;
            button.setAttribute('aria-checked', String(selected));
            button.tabIndex = selected ? 0 : -1;
            button.disabled = !this.agentModes.includes(key) || this.active || this.sending;
          }
          const manual = this.agentMode === 'deep' && this.deepApprovalResume === 'manual_tool_only';
          this.dom.modeHint.textContent = manual ? T('agent_deep_manual') : T(this.agentMode === 'deep' ? 'agent_deep_hint' : 'agent_fast_hint');
        }
        if (persist) {
          try { localStorage.setItem('ihtml.agent_mode', this.agentMode); } catch { /* 忽略 */ }
        }
      },
      resizeComposer() {
        if (!this.dom || !this.dom.input) return;
        const input = this.dom.input; input.style.height = 'auto';
        const style = getComputedStyle(input);
        const minHeight = Number.parseFloat(style.minHeight) || 44;
        const maxHeight = Number.parseFloat(style.maxHeight) || 140;
        const scrollHeight = input.scrollHeight;
        input.style.height = Math.max(minHeight, Math.min(scrollHeight, maxHeight)) + 'px';
        input.style.overflowY = scrollHeight > maxHeight + 1 ? 'auto' : 'hidden';
        if (this.follow) this.scroll();
      },
      syncChatControls() {
        if (!this.dom) return;
        const busy = this.active || this.sending;
        this.dom.input.readOnly = this.sending;
        this.dom.input.setAttribute('aria-readonly', String(this.sending));
        this.dom.foot.setAttribute('aria-busy', String(busy));
        this.dom.sendBtn.textContent = busy ? T('stop') : T('send');
        this.dom.sendBtn.classList.toggle('stop', busy);
        this.dom.sendBtn.disabled = !busy && !this.dom.input.value.trim();
        this.dom.modelInput.disabled = busy;
        this.dom.freshBtn.disabled = busy || !!this.modelPromise;
        for (const [mode, button] of Object.entries(this.dom.modeButtons || {})) {
          button.disabled = busy || !this.agentModes.includes(mode);
        }
        if (this.log) {
          for (const message of Array.from(this.log.children)) {
            const owner = message._turn;
            if (!owner) continue;
            for (const rec of owner.tools.values()) {
              if (rec.status !== 'pending') continue;
              const disabled = busy && owner !== this.turn;
              rec.body.querySelectorAll('.t-actions button').forEach((button) => { button.disabled = disabled; });
            }
          }
        }
      },

      async loadModels(force) {
        if (this.modelPromise && !force) return this.modelPromise;
        const { providerBadge, modelInput, dataList, freshBtn, toolbar } = this.dom;
        const seq = ++this.modelLoadSeq;
        const promise = (async () => {
          freshBtn.disabled = true;
          let info;
          try {
            info = await http.get('api/chat/models');
          } catch (err) {
            if (err && err.stale) throw err;
            info = { model_ready: false, models: [], agent_modes: ['fast'] };
          }
          if (seq !== this.modelLoadSeq || destroyed) throw staleRequestError();
          info = info && typeof info === 'object' ? info : { model_ready: false, models: [] };
          this.modelInfo = info;
          const models = (Array.isArray(info.models) ? info.models : []).map((model) => String(model)).slice(0, 256);
          this.agentModes = chatAgentCore.normalizeModes(info.agent_modes);
          this.deepApprovalResume = String(info.deep_approval_resume || '');
          toolbar.style.display = info.model_select ? '' : 'none';
          const provider = String(info.provider || '').trim().slice(0, 32);
          providerBadge.textContent = provider ? provider.charAt(0).toUpperCase() + provider.slice(1) : '';
          providerBadge.hidden = !provider;
          toolbar.classList.toggle('has-provider', !!provider);
          dataList.innerHTML = '';
          for (const m of models) {
            const opt = el('option');
            opt.value = m;
            dataList.appendChild(opt);
          }
          let saved = '';
          if (info.model_select) {
            try { saved = localStorage.getItem('ihtml.model') || ''; } catch { /* 忽略 */ }
          }
          const preferred = saved && (!models.length || models.includes(saved))
            ? saved
            : (info.current_model || models[0] || saved);
          modelInput.value = preferred || '';
          this.saveModel(modelInput.value.trim());
          let savedMode = '';
          try { savedMode = localStorage.getItem('ihtml.agent_mode') || ''; } catch { /* 忽略 */ }
          this.setAgentMode(chatAgentCore.selectMode(this.agentModes, savedMode || this.agentMode), true);
          this.setAvailabilityNote(info.model_ready === false);
          this.syncChatControls();
          return info;
        })();
        this.modelPromise = promise;
        try {
          return await promise;
        } finally {
          if (this.modelPromise === promise) {
            this.modelPromise = null;
            this.syncChatControls();
          }
        }
      },
      saveModel(m) {
        this.model = m || '';
        try { localStorage.setItem('ihtml.model', this.model); } catch { /* 忽略 */ }
      },

      scroll(force) {
        if (!this.follow && !force) return;
        if (this.scrollFrame) return;
        this.scrollFrame = requestAnimationFrame(() => {
          this.scrollFrame = 0;
          if (this.log) this.log.scrollTop = this.log.scrollHeight;
        });
      },

      ensureWS() {
        if (destroyed) return Promise.reject(new Error('ihtml: destroyed'));
        if (this.ws && this.ws.readyState === WebSocket.OPEN) return Promise.resolve(this.ws);
        if (this.wsPromise) return this.wsPromise;
        const generation = ++this.wsGeneration;
        const promise = (async () => {
          const ticket = await evalConnectionAuth();
          if (destroyed || generation !== this.wsGeneration) throw new Error(T('chat_disconnected'));
          const wsURL = apiURL('api/chat/ws', true, ticket);
          wsURL.searchParams.set('csid', chatSessionID());
          wsURL.protocol = wsURL.protocol === 'https:' ? 'wss:' : 'ws:';
          const ws = new WebSocket(wsURL);
          this.ws = ws;
          let settled = false;
          return new Promise((resolve, reject) => {
            const fail = (err) => {
              if (settled) return;
              settled = true;
              reject(err);
            };
            ws.onopen = () => {
              if (destroyed || generation !== this.wsGeneration || this.ws !== ws) {
                try { ws.close(); } catch { /* 忽略 */ }
                fail(new Error(T('chat_disconnected')));
                return;
              }
              settled = true;
              this.wsPromise = null;
              if (this.everConnected) this.systemNote(T('chat_reconnected'));
              this.everConnected = true;
              resolve(ws);
            };
            ws.onerror = () => fail(new Error(T('chat_broken')));
            ws.onclose = () => {
              fail(new Error(T('chat_disconnected')));
              if (generation !== this.wsGeneration || this.ws !== ws) return;
              this.ws = null;
              this.wsPromise = null;
              if (this.dom && !destroyed) this.systemNote(T('chat_disconnected'));
              if (this.active && this.turn && !this.turn.done) this.turnError(T('chat_disconnected'));
              else this.setActive(false);
            };
            ws.onmessage = (ev) => {
              if (destroyed || generation !== this.wsGeneration || this.ws !== ws) return;
              let data = null;
              try { data = JSON.parse(ev.data); } catch { return; }
              this.onEvent(data);
            };
          });
        })();
        this.wsPromise = promise;
        promise.catch(() => {
          if (generation === this.wsGeneration && this.wsPromise === promise) this.wsPromise = null;
        });
        return promise;
      },
      disconnect() {
        const ws = this.ws;
        this.wsGeneration++;
        this.ws = null;
        this.wsPromise = null;
        if (ws) { try { ws.close(); } catch { /* 忽略 */ } }
      },
      reset() {
        this.sendGeneration++;
        this.sending = false;
        this.disconnect();
        chatSessionID(true); // 换会话号 = 服务端上下文重开
        this.clearTurnTimers();
        this.log.replaceChildren();
        this.availabilityNote = null;
        this.turn = null;
        this.turnsByRun.clear();
        this.everConnected = false;
        this.setActive(false);
        this.notice(T('session_reset'));
        if (this.modelInfo && this.modelInfo.model_ready === false) this.setAvailabilityNote(true);
      },
      clearTurnTimers() {
        if (this.scrollFrame) cancelAnimationFrame(this.scrollFrame);
        this.scrollFrame = 0;
        clearTimeout(this.announcementTimer);
        this.announcementTimer = 0;
        this.lastAnnouncement = '';
        this.announcementQueue = [];
        this.announcementBusy = false;
        if (!this.log) return;
        for (const card of Array.from(this.log.children)) {
          const turn = card._turn;
          if (!turn) continue;
          if (turn.renderFrame) cancelAnimationFrame(turn.renderFrame);
          turn.renderFrame = 0;
          for (const segment of (turn.thinkingSegments || new Map()).values()) {
            if (segment.frame) cancelAnimationFrame(segment.frame);
            segment.frame = 0;
          }
          for (const rec of turn.tools.values()) {
            if (rec.countdown) clearInterval(rec.countdown);
            rec.countdown = 0;
          }
        }
      },
      resetForAuth() {
        this.sendGeneration++;
        this.modelLoadSeq++;
        this.sending = false;
        this.clearTurnTimers();
        this.disconnect();
        this.modelInfo = null;
        this.modelPromise = null;
        this.agentModes = ['fast'];
        this.deepApprovalResume = '';
        this.agentMode = 'fast';
        this.availabilityNote = null;
        this.turn = null;
        this.turnsByRun.clear();
        this.everConnected = false;
        this.setActive(false);
        if (this.dom) {
          this.setAgentMode('fast', false);
          this.syncChatControls();
        }
        this.close();
        if (this.log) {
          this.log.replaceChildren();
          this.notice(T('welcome'));
        }
      },
      setActive(v) {
        this.active = v;
        if (!this.dom) return;
        this.syncChatControls();
        if (!v && this.turn && !this.turn.done) this.finishTurn(null);
      },

      clientContext() {
        return {
          viewport: {
            width: env.width,
            height: env.height,
            device_pixel_ratio: devicePixelRatio || 1,
            mode: env.mode,
          },
          page: {
            // 页面 query 常含 OAuth code、票据或业务筛选条件，不能转交给外部模型。
            path: (standalone ? location.pathname : '(embedded)') + '#/' + nav.currentPage,
            title: doc.title,
            visible_text: clip(appEl.innerText || '', 1800),
          },
          locale: navigator.language,
          time_zone: (Intl.DateTimeFormat().resolvedOptions() || {}).timeZone || '',
          user_agent: navigator.userAgent,
        };
      },

      async send() {
        const text = this.dom.input.value.trim();
        if (!text || this.active || this.sending) return;
        this.sending = true;
        const generation = ++this.sendGeneration;
        this.syncChatControls();
        let pendingTurn = null;
        try {
          if (!this.modelInfo) await this.loadModels();
          if (generation !== this.sendGeneration) return;
          // The input is the source of truth. `change` is not guaranteed to fire
          // before Enter submits (and programmatic fills only emit `input`), so a
          // stale cached model must never be sent while another value is visible.
          if (this.modelInfo?.model_select) this.saveModel(this.dom.modelInput.value.trim());
          if (this.modelInfo?.model_select && !this.model) {
            ui.toast(T('model_needed'), 'warn');
            return;
          }
          if (this.dom.input.value.trim() === text) this.dom.input.value = '';
          this.resizeComposer();
          const um = el('article', 'ihtml-msg user');
          um.appendChild(el('div', 'author', T('author_user')));
          um.appendChild(el('div', 'content', text));
          this.log.appendChild(um);
          this.follow = true;
          const request = chatAgentCore.requestSnapshot(this.agentModes, this.agentMode, this.model, !!this.modelInfo?.model_select);
          this.setActive(true);
          pendingTurn = this.beginTurn(request.agentMode);
          pendingTurn.requestModel = request.model;
          this.sending = false;
          this.syncChatControls();
          const ws = await this.ensureWS();
          if (generation !== this.sendGeneration || !this.active || this.turn !== pendingTurn || pendingTurn.done || pendingTurn.cancelled) return;
          ws.send(JSON.stringify({
            type: 'user_message',
            messages: [{ role: 'user', content: text }],
            model: pendingTurn.requestModel,
            agent_mode: pendingTurn.agentMode,
            tools_enabled: true,
            client_context: this.clientContext(),
          }));
        } catch (e) {
          if (generation === this.sendGeneration && !(e && e.stale) && !(pendingTurn && pendingTurn.cancelled)) {
            if (this.active && this.turn === pendingTurn && !pendingTurn.done) this.turnError(e.message, pendingTurn);
            else ui.toast(e.message || T('chat_broken'), 'error');
          }
        } finally {
          if (generation === this.sendGeneration) {
            this.sending = false;
            if (this.dom) this.syncChatControls();
          }
        }
      },
      cancel() {
        this.sendGeneration++;
        this.sending = false;
        const turn = this.turn;
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
          this.ws.send(JSON.stringify({ type: 'cancel' }));
        }
        this.disconnect();
        if (turn) this.finishCancelled(turn);
        else this.setActive(false);
        if (this.dom) this.syncChatControls();
      },

      beginTurn(agentMode) {
        const card = el('article', 'ihtml-msg assistant');
        card.appendChild(el('div', 'author', 'AI'));
        const process = el('details', 'ihtml-process active');
        process.setAttribute('aria-busy', 'true');
        process.open = true;
        const summary = el('summary', 'ihtml-process-head');
        const processDot = el('span', 'p-dot');
        processDot.setAttribute('aria-hidden', 'true');
        const label = el('span', 'p-title', T('process_steps'));
        const note = el('small', 'p-meta', T('analyzing'));
        summary.append(processDot, label, note);
        const pBody = el('div', 'pbody');
        process.append(summary, pBody);
        const answer = el('div', 'answer');
        answer.style.display = 'none';
        answer.setAttribute('aria-live', 'off');
        card.append(process, answer);
        this.log.appendChild(card);
        const turn = {
          card, process, processSummary: summary, processDot, note, pBody, answer,
          agentMode: chatAgentCore.selectMode(this.agentModes, agentMode),
          agentState: chatAgentCore.initialState(agentMode), agentOverview: null,
          text: '', lastKind: '', lastStatusNode: null,
          tools: new Map(), toolCount: 0, pendingCount: 0, errorCount: 0, done: false,
          turnFailed: false,
          runSeq: new Map(), acceptNextRun: false, renderFrame: 0,
          cancelled: false, confirmingTool: false,
          thinkingSegments: new Map(), activeThinkingID: '', legacyThinkingSeq: 0,
          anonymousToolQueues: new Map(), anonymousToolAliases: new Map(), anonymousToolSeq: 0,
        };
        card._turn = turn;
        this.turn = turn;
        this.lastAnnouncement = '';
        this.announce(T('analyzing'));
        this.scroll(true);
        return turn;
      },
      processStepCount(t) {
        const reasoning = t.thinkingSegments ? t.thinkingSegments.size : 0;
        const deep = t.agentState && t.agentState.mode === 'deep' && t.agentState.plan.length ? 1 : 0;
        return t.toolCount + reasoning + deep;
      },
      processSummaryText(t) {
        if (t.cancelled) return T('turn_stopped');
        if (t.pendingCount > 0) return T('tools_pending', { n: t.pendingCount });
        if (!t.done) {
          const running = Array.from(t.tools.values()).filter((rec) => rec.status === 'running');
          if (running.length === 1) return T('running_tool', { name: running[0].title.textContent });
          if (running.length > 1) return T('tools_running', { n: running.length });
          if (t.agentState && t.agentState.manualPaused) return T('agent_plan_paused');
          if (t.text) return T('answering');
          return T('analyzing');
        }
        if (t.turnFailed) return T('turn_failed');
        if (t.errorCount > 0) return T('tools_failed', { n: t.errorCount });
        if (t.agentState && t.agentState.manualPaused) return T('agent_plan_paused');
        const steps = this.processStepCount(t);
        if (steps > 0) return T('steps_completed', { n: steps });
        return T('done');
      },
      finishCancelled(t) {
        if (!t || t.done) { this.setActive(false); return; }
        t.cancelled = true;
        if (t.renderFrame) cancelAnimationFrame(t.renderFrame);
        t.renderFrame = 0;
        if (t.text) t.answer.replaceChildren(renderAssistant(t.text));
        t.answer.classList.remove('streaming');
        this.clearTransientStatus(t);
        this.finalizeThinking(t);
        for (const rec of t.tools.values()) {
          if (rec.status !== 'running' && rec.status !== 'pending') continue;
          this.setToolStatus(rec, 'cancelled', t);
          this.releaseAnonymousTool(t, rec.id);
          rec.lastEvent = Object.assign({}, rec.lastEvent, { tool_summary: T('turn_stopped') });
          this.renderToolRecord(rec, rec.lastEvent, t);
        }
        if (!t.pBody.childNodes.length) this.appendStatus(T('turn_stopped'), false, t);
        t.done = true;
        this.updateProcess(t);
        this.announce(T('turn_stopped'));
        this.releaseTurnRuns(t);
        if (this.turn === t) this.setActive(false);
      },
      hasBlockingProcess(t) {
        if (!t) return false;
        if (t.pendingCount > 0 || t.errorCount > 0) return true;
        return Array.from(t.tools.values()).some((rec) => rec.status === 'running');
      },
      updateProcess(t) {
        t = t || this.turn;
        if (!t || !t.process || !t.process.isConnected) return;
        const focusWasInside = t.process.contains(doc.activeElement);
        let collapsed = false;
        t.process.classList.toggle('active', !t.done);
        t.process.classList.toggle('pending', t.pendingCount > 0);
        t.process.classList.toggle('error', t.turnFailed || t.errorCount > 0);
        t.process.classList.toggle('paused', !!(t.agentState && t.agentState.manualPaused));
        t.process.dataset.state = t.cancelled ? 'cancelled' : (t.turnFailed || t.errorCount > 0 ? 'error' : (t.pendingCount > 0 ? 'pending' : (t.done ? 'complete' : 'running')));
        t.process.setAttribute('aria-busy', String(!t.done));
        t.note.textContent = this.processSummaryText(t);
        if (t.done) {
          this.finalizeThinking(t);
          if (t.agentOverview && !t.agentState.manualPaused && !t.errorCount) t.agentOverview.open = false;
          if (!t.pBody.childNodes.length) t.process.remove();
          else if (t.pendingCount === 0 && t.errorCount === 0) { collapsed = t.process.open; t.process.open = false; }
          else t.process.open = true;
        } else if (t.text && !this.hasBlockingProcess(t) && !(t.agentState && t.agentState.manualPaused)) {
          collapsed = t.process.open;
          t.process.open = false;
          if (t.agentOverview) t.agentOverview.open = false;
        }
        if (collapsed && focusWasInside && t.processSummary) {
          queueMicrotask(() => { if (t.processSummary.isConnected) t.processSummary.focus({ preventScroll: true }); });
        }
      },
      clearTransientStatus(t) {
        if (!t || !t.pBody) return;
        for (const node of t.pBody.querySelectorAll('.ihtml-pline[data-persistent="false"]')) node.remove();
        if (t.lastStatusNode && t.lastStatusNode.dataset.persistent !== 'true') t.lastStatusNode = null;
        if (t.lastKind === 'status') t.lastKind = '';
      },
      appendStatus(text, isError, t = this.turn) {
        text = (text || '').trim();
        if (!t || !text) return;
        let node = null;
        if (!isError) node = t.pBody.querySelector('.ihtml-pline[data-persistent="false"]');
        else if (t.lastStatusNode && t.lastStatusNode.dataset.persistent === 'true') node = t.lastStatusNode;
        if (node) node.textContent = text;
        else {
          node = el('div', 'ihtml-pline', text);
          t.pBody.appendChild(node);
        }
        t.lastStatusNode = node;
        t.lastKind = 'status';
        t.lastStatusNode.dataset.persistent = String(!!isError);
        t.lastStatusNode.setAttribute('aria-live', 'off');
        t.lastStatusNode.style.color = isError ? 'var(--ui-err)' : '';
        if (isError) this.announce(text);
        this.updateProcess(t);
        this.scroll();
      },
      flushThinkingSegment(segment) {
        if (!segment || !segment.pre) return;
        if (segment.frame) cancelAnimationFrame(segment.frame);
        segment.frame = 0;
        const follow = segment.follow || segment.pre.scrollHeight <= segment.pre.clientHeight + 24;
        segment.pre.textContent = segment.text;
        if (follow) segment.pre.scrollTop = segment.pre.scrollHeight;
      },
      finalizeThinking(t, segmentID) {
        if (!t || !t.thinkingSegments) return;
        const segments = segmentID ? [t.thinkingSegments.get(segmentID)] : Array.from(t.thinkingSegments.values());
        for (const segment of segments) {
          if (!segment || segment.state === 'complete') continue;
          this.flushThinkingSegment(segment);
          segment.state = 'complete';
          segment.wrap.dataset.state = 'complete';
          segment.meta.textContent = T('reasoning_complete');
          segment.wrap.open = false;
        }
        if (!segmentID || t.activeThinkingID === segmentID) t.activeThinkingID = '';
      },
      collapseThinking(t) {
        if (!t || !t.thinkingSegments) return;
        for (const segment of t.thinkingSegments.values()) if (segment.state === 'running') segment.wrap.open = false;
      },
      appendThinking(delta, ev, t = this.turn) {
        if (!t) return;
        ev = ev && typeof ev === 'object' ? ev : {};
        const rawID = String(ev.thinking_id || '');
        const runID = String(ev.run_id || 'legacy');
        let id = rawID ? runID + ':' + rawID : t.activeThinkingID;
        if (!id || !t.thinkingSegments.has(id) || t.thinkingSegments.get(id).state === 'complete') {
          if (!rawID) id = runID + ':legacy-' + (++t.legacyThinkingSeq);
          else id = runID + ':' + rawID;
        }
        let segment = t.thinkingSegments.get(id);
        if (!segment && (delta || !ev.thinking_done)) {
          if (t.activeThinkingID && t.activeThinkingID !== id) this.finalizeThinking(t, t.activeThinkingID);
          const wrap = el('details', 'ihtml-think');
          wrap.dataset.state = 'running';
          wrap.open = !t.text;
          const summary = el('summary', 'think-head');
          const dot = el('span', 'p-dot');
          dot.setAttribute('aria-hidden', 'true');
          const title = el('span', 'think-title', T('reasoning_step', { n: t.thinkingSegments.size + 1 }));
          const meta = el('small', 'think-meta', T('reasoning_streaming'));
          summary.append(dot, title, meta);
          const pre = el('pre', 'think-body');
          pre.setAttribute('aria-live', 'off');
          wrap.append(summary, pre);
          t.pBody.appendChild(wrap);
          segment = { id, wrap, pre, meta, text: '', state: 'running', frame: 0, follow: true, truncated: false };
          pre.addEventListener('scroll', () => {
            segment.follow = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 24;
          });
          t.thinkingSegments.set(id, segment);
          t.activeThinkingID = id;
        }
        if (segment && delta) {
          const max = 65536;
          if (!segment.truncated) {
            const next = segment.text + String(delta);
            if (next.length > max) {
              segment.text = next.slice(0, max) + '\n\n' + T('content_truncated');
              segment.truncated = true;
            } else segment.text = next;
          }
          if (!segment.frame) {
            segment.frame = requestAnimationFrame(() => {
              segment.frame = 0;
              this.flushThinkingSegment(segment);
            });
          }
          t.lastKind = 'thinking';
        }
        if (ev.thinking_done && segment) this.finalizeThinking(t, id);
        this.updateProcess(t);
        this.scroll();
      },
      appendDelta(delta, t = this.turn) {
        if (!t || !delta) return;
        this.clearTransientStatus(t);
        // 正文和 reasoning 可能交错到达。正文开始只收起视觉详情，真正的
        // “分析完成”必须等待 thinking_done、工具边界或本轮终态。
        this.collapseThinking(t);
        t.text += delta;
        t.answer.style.display = '';
        t.answer.classList.add('streaming');
        if (!t.renderFrame) {
          t.renderFrame = requestAnimationFrame(() => {
            t.renderFrame = 0;
            if (!t.done) t.answer.textContent = t.text;
          });
        }
        this.updateProcess(t);
        this.scroll();
      },

      projectAgentEvent(ev, t = this.turn) {
        if (!t) return;
        t.agentState = chatAgentCore.project(t.agentState, ev);
        if (t.agentState.mode === 'deep') {
          t.agentMode = 'deep';
          this.renderAgentState(t);
        }
      },
      renderAgentState(t) {
        if (!t || !t.agentState || t.agentState.mode !== 'deep') return;
        let details = t.agentOverview;
        if (!details) {
          details = el('details', 'ihtml-agent-overview');
          details.open = false;
          const summary = el('summary');
          const body = el('div', 'body');
          details.append(summary, body);
          t.pBody.insertBefore(details, t.pBody.firstChild);
          details._summary = summary;
          details._body = body;
          t.agentOverview = details;
        }
        const state = t.agentState;
        const budget = chatAgentCore.budgetParts(state.budget);
        const budgetText = [];
        if (state.budget) {
          budgetText.push(T('agent_model_budget', { used: budget.modelCalls, max: budget.maxModelCalls || '∞' }));
          budgetText.push(T('agent_subtask_budget', { used: budget.subtasks, max: budget.maxSubtasks || '∞' }));
        }
        const completed = state.plan.filter((item) => item.status === 'completed').length;
        const summaryBits = [];
        const stagesNow = Object.values(state.agents || {});
        const activeStage = stagesNow.find((stage) => ['error', 'paused', 'running'].includes(stage.status) && stage.summary);
        const activePlan = state.plan.find((item) => item.status === 'in_progress');
        if (activeStage && activeStage.summary) summaryBits.push(clip(activeStage.summary, 120));
        else if (activePlan && activePlan.content) summaryBits.push(clip(activePlan.content, 120));
        if (state.plan.length) summaryBits.push(T('agent_plan') + ' ' + completed + '/' + state.plan.length);
        const budgetNearLimit = state.budget && (
          budget.maxModelCalls > 0 && budget.modelCalls / budget.maxModelCalls >= 0.8 ||
          budget.maxSubtasks > 0 && budget.subtasks / budget.maxSubtasks >= 0.8
        );
        if (budgetNearLimit && budgetText.length) summaryBits.push(budgetText.join(' · '));
        if (state.manualPaused) summaryBits.push(T('agent_plan_paused'));
        details._summary.replaceChildren(
          el('strong', '', T('agent_deep')),
          el('small', '', summaryBits.join(' · ') || T('agent_deep_hint')),
        );

        const body = details._body;
        body.replaceChildren();
        if (budgetText.length) {
          const budgetLine = el('div', 'ihtml-agent-budget');
          for (const text of budgetText) budgetLine.appendChild(el('span', '', text));
          body.appendChild(budgetLine);
        }
        if (state.plan.length) {
          const list = el('ol', 'ihtml-agent-plan');
          for (const item of state.plan) {
            const row = el('li', 'status-' + item.status);
            const statusKey = item.status === 'in_progress' ? 'running' : item.status;
            row.append(
              el('span', 'status', T('agent_status_' + statusKey)),
              el('span', 'content', item.content),
            );
            list.appendChild(row);
          }
          body.appendChild(list);
        }
        const agents = stagesNow;
        if (agents.length) {
          const stages = el('div', 'ihtml-agent-stages');
          for (const stage of agents) {
            const row = el('div', 'ihtml-agent-stage' + (stage.parent_task_id && stage.agent !== 'root' ? ' child' : ''));
            const agentKey = stage.agent === 'ui_inspector' ? 'agent_inspector' : (stage.agent === 'ui_reviewer' ? 'agent_reviewer' : 'agent_root');
            const status = ['pending', 'running', 'completed', 'paused', 'error'].includes(stage.status) ? stage.status : 'running';
            row.append(
              el('strong', '', T(agentKey)),
              el('span', 'status status-' + status, T('agent_status_' + status)),
            );
            if (stage.summary) row.appendChild(el('span', 'summary', stage.summary));
            stages.appendChild(row);
          }
          body.appendChild(stages);
        }
        if (state.manualPaused) {
          const manual = el('div', 'ihtml-agent-manual', T('agent_manual_confirm'));
          manual.setAttribute('role', 'note');
          body.appendChild(manual);
        }
        const hasAgentError = agents.some((stage) => stage.status === 'error');
        if (state.manualPaused || hasAgentError) details.open = true;
        else if (t.text || t.done) details.open = false;
        this.updateProcess(t);
        this.scroll();
      },

      toolView(call, ev) {
        let view = null;
        if (chatTraceCore && typeof chatTraceCore.toolView === 'function') {
          try { view = chatTraceCore.toolView(call, ev, T); } catch { view = null; }
        }
        const source = view && typeof view === 'object' ? view : {};
        const body = call && call.body && typeof call.body === 'object' ? call.body : {};
        const fallbackFacts = [];
        if (Array.isArray(body.items)) fallbackFacts.push({ label: 'Items', value: body.items.map((item) => item && item.id).filter(Boolean).join(', ') });
        else if (Array.isArray(body.ids)) fallbackFacts.push({ label: 'Items', value: body.ids.join(', ') });
        else if (body.id) fallbackFacts.push({ label: 'ID', value: body.id });
        return {
          title: clip(String(source.title || ev.tool_title || call.title || ev.tool_name || call.name || 'tool'), 160),
          action: clip(String(source.action || ''), 320),
          summary: clip(String(source.summary || ev.tool_summary || ''), 500),
          facts: (Array.isArray(source.facts) ? source.facts : fallbackFacts).slice(0, 8).map((fact) => ({
            label: clip(String(fact && fact.label || ''), 80), value: clip(String(fact && fact.value || ''), 320),
          })).filter((fact) => fact.label && fact.value),
          request: source.request !== undefined ? source.request : { method: call.method, path: call.path, query: call.query, body: call.body },
          result: source.result !== undefined ? source.result : ev.tool_result,
          debug: source.debug !== undefined ? source.debug : ev.tool_debug,
          hasTechnical: source.hasTechnical !== undefined ? !!source.hasTechnical : true,
        };
      },
      setToolStatus(rec, status, t) {
        if (!rec || !t) return;
        const previous = rec.status;
        if (previous === status) return;
        if (rec.countedPending && status !== 'pending') { t.pendingCount = Math.max(0, t.pendingCount - 1); rec.countedPending = false; }
        if (!rec.countedPending && status === 'pending') { t.pendingCount++; rec.countedPending = true; }
        if (rec.countedError && status !== 'error') { t.errorCount = Math.max(0, t.errorCount - 1); rec.countedError = false; }
        if (!rec.countedError && status === 'error') { t.errorCount++; rec.countedError = true; }
        rec.status = status;
        rec.card.dataset.status = status;
        rec.chip.textContent = TOOL_STATUS_LABEL[status] || status;
        rec.chip.className = 'ihtml-chip t-status ' + status;
      },
      appendTechnicalSection(parent, title, value, rec, suffix) {
        if (value === undefined || value === null || value === '') return false;
        const text = typeof value === 'string' ? clip(value, 16000) : technicalText(value);
        if (!text) return false;
        const section = el('section', 't-tech-section');
        const heading = el('div', 't-tech-title', title);
        heading.id = rec.domID + '-' + suffix;
        const pre = el('pre');
        pre.setAttribute('aria-labelledby', heading.id);
        pre.textContent = text;
        section.append(heading, pre);
        parent.appendChild(section);
        return true;
      },
      renderToolRecord(rec, ev, t) {
        if (!rec || !t) return;
        const hadFocus = rec.body.contains(doc.activeElement);
        if (rec.countdown) { clearInterval(rec.countdown); rec.countdown = 0; }
        const view = this.toolView(rec.call, ev || {});
        rec.view = view;
        rec.title.textContent = view.title;
        const resultSummary = clip(String(ev && ev.tool_summary || view.summary || view.action || ''), 500);
        rec.note.textContent = resultSummary;
        rec.body.replaceChildren();
        rec.body.removeAttribute('role');
        rec.body.removeAttribute('aria-labelledby');
        rec.body.removeAttribute('aria-describedby');

        const primaryText = rec.status === 'pending'
          ? (view.action || resultSummary)
          : (resultSummary || view.action);
        let primary = null;
        if (primaryText) {
          primary = el('div', 't-primary' + (rec.status === 'success' || rec.status === 'error' ? ' t-live-result' : ''), primaryText);
          primary.id = rec.domID + '-primary';
          rec.body.appendChild(primary);
        }
        if (rec.status === 'pending' && rec.call.reason) {
          const reason = el('div', 't-reason');
          reason.append(el('strong', '', T('tool_reason')), doc.createTextNode(String(rec.call.reason)));
          rec.body.appendChild(reason);
        }
        if (view.facts.length) {
          const facts = el('dl', 't-facts');
          for (const fact of view.facts) {
            const row = el('div', 't-fact');
            row.append(el('dt', '', fact.label), el('dd', '', fact.value));
            facts.appendChild(row);
          }
          rec.body.appendChild(facts);
        }
        const meta = el('div', 't-meta');
        if (rec.call.risk) meta.appendChild(el('span', 'ihtml-chip risk-' + rec.call.risk, RISK_LABEL[rec.call.risk] || rec.call.risk));
        if (rec.status === 'pending' && rec.call.confirmation_expires) {
          const countdown = el('span', 't-countdown');
          countdown.setAttribute('aria-live', 'off');
          meta.appendChild(countdown);
          const tick = () => {
            const left = rec.call.confirmation_expires * 1000 - Date.now();
            if (left <= 0) {
              if (rec.countdown) clearInterval(rec.countdown);
              rec.countdown = 0;
              this.setToolStatus(rec, 'cancelled', t);
              this.releaseAnonymousTool(t, rec.id);
              rec.lastEvent = Object.assign({}, rec.lastEvent, { tool_summary: T('confirm_expired') });
              this.renderToolRecord(rec, rec.lastEvent, t);
              this.announce(T('confirmation_expired_announced', { name: rec.title.textContent }));
              this.updateProcess(t);
              if (t.done && t.pendingCount === 0) this.releaseTurnRuns(t);
              return;
            }
            const minutes = Math.max(1, Math.ceil(left / 60000));
            let relative = minutes + ' min';
            try { relative = new Intl.RelativeTimeFormat(opts.locale || navigator.language, { numeric: 'always' }).format(minutes, 'minute'); } catch { /* 兼容旧浏览器 */ }
            countdown.textContent = T('expires_in') + ' ' + relative;
          };
          rec.countdown = setInterval(tick, 15000);
          tick();
        }
        if (meta.childNodes.length) rec.body.appendChild(meta);
        if (rec.call.approval_resume === 'manual_tool_only') {
          const manual = el('div', 'ihtml-agent-manual', T('agent_manual_confirm'));
          manual.setAttribute('role', 'note');
          rec.body.appendChild(manual);
        }

        const techBody = el('div', 't-tech-body');
        const hasRequest = this.appendTechnicalSection(techBody, T('request_summary'), view.request, rec, 'request');
        const hasResult = this.appendTechnicalSection(techBody, T('result_data'), view.result, rec, 'result');
        const hasDebug = this.appendTechnicalSection(techBody, T('diagnostic_data'), view.debug, rec, 'debug');
        if (view.hasTechnical && (hasRequest || hasResult || hasDebug)) {
          const tech = el('details', 't-tech');
          const techSummary = el('summary', '', T('technical_details'));
          techSummary.setAttribute('aria-label', T('technical_details') + ' · ' + view.title);
          tech.append(techSummary, techBody);
          rec.body.appendChild(tech);
        }
        if (rec.status === 'pending') {
          const bar = el('div', 't-actions');
          const okBtn = el('button', 'ok', T('confirm_this_action'));
          const noBtn = el('button', '', T('dismiss_tool'));
          okBtn.setAttribute('aria-label', T('confirm_this_action') + ' · ' + view.title);
          noBtn.setAttribute('aria-label', T('dismiss_tool') + ' · ' + view.title);
          okBtn.onclick = (event) => { event.preventDefault(); okBtn.disabled = true; noBtn.disabled = true; this.confirmTool(rec); };
          noBtn.onclick = (event) => { event.preventDefault(); okBtn.disabled = true; noBtn.disabled = true; this.cancelTool(rec); };
          bar.append(okBtn, noBtn);
          rec.body.appendChild(bar);
          rec.body.setAttribute('role', 'group');
          rec.body.setAttribute('aria-labelledby', rec.title.id);
          if (primary) rec.body.setAttribute('aria-describedby', primary.id);
        }
        if (rec.status === 'pending' || rec.status === 'error') rec.card.open = true;
        else if (['success', 'cancelled'].includes(rec.status)) rec.card.open = false;
        if (hadFocus) queueMicrotask(() => {
          const owner = rec.ownerTurn;
          const target = owner && owner.process && !owner.process.open ? owner.processSummary : rec.summary;
          if (target && target.isConnected) target.focus({ preventScroll: true });
        });
      },
      anonymousToolQueueKey(ev, call) {
        const body = call && call.body !== undefined ? technicalText(call.body, 1200) : '';
        return [ev.run_id || 'legacy', ev.tool_name || call.name || 'tool', call.method || '', call.path || '', body].join('|');
      },
      releaseAnonymousTool(t, id) {
        if (!t || !t.anonymousToolQueues) return;
        for (const [key, queue] of t.anonymousToolQueues) {
          const next = queue.filter((value) => value !== id);
          if (next.length) t.anonymousToolQueues.set(key, next);
          else t.anonymousToolQueues.delete(key);
        }
      },
      upsertTool(ev, status, t = this.turn) {
        if (!t) return;
        const call = ev.tool_call && typeof ev.tool_call === 'object' ? ev.tool_call : {};
        const nonce = String(call.confirmation_nonce || '');
        const queueKey = this.anonymousToolQueueKey(ev, call);
        let id = call.id || (nonce && t.anonymousToolAliases.get(nonce)) || '';
        if (!id) {
          const queue = t.anonymousToolQueues.get(queueKey) || [];
          if (ev.type === 'tool_start') {
            id = queueKey + '#' + (++t.anonymousToolSeq);
            queue.push(id);
            t.anonymousToolQueues.set(queueKey, queue);
          } else {
            id = queue.find((candidate) => {
              const existing = t.tools.get(candidate);
              return existing && (existing.status === 'running' || existing.status === 'pending');
            }) || queueKey + '#' + (++t.anonymousToolSeq);
          }
        }
        let rec = t.tools.get(id);
        if (!rec) {
          const card = el('details', 'ihtml-tool');
          const summary = el('summary', 't-head');
          const chip = el('span', 'ihtml-chip t-status');
          const row = el('span', 't-summary-row');
          const title = el('span', 't-title');
          const note = el('small', 't-summary');
          const domID = 'ihtml-tool-' + instanceID + '-' + (++this.toolDomSeq);
          title.id = domID + '-title';
          row.append(title, note);
          summary.append(chip, row);
          const body = el('div', 'tbody');
          card.append(summary, body);
          t.pBody.appendChild(card);
          rec = { id, card, summary, chip, title, note, body, domID, call, status: '', ownerTurn: t, countedPending: false, countedError: false, countdown: 0, lastEvent: {} };
          t.tools.set(id, rec);
          t.toolCount++;
        }
        if (nonce) t.anonymousToolAliases.set(nonce, id);
        if (ev.tool_call) rec.call = ev.tool_call;
        const previous = rec.status;
        rec.lastEvent = Object.assign({}, rec.lastEvent, ev, { tool_call: rec.call });
        if (ev.type === 'tool_start') {
          this.clearTransientStatus(t);
          this.finalizeThinking(t, t.activeThinkingID);
        }
        this.setToolStatus(rec, status, t);
        if (['success', 'error', 'cancelled'].includes(status)) this.releaseAnonymousTool(t, id);
        this.renderToolRecord(rec, rec.lastEvent, t);
        if (status === 'pending' && previous !== 'pending') {
          this.announce(T('confirmation_announced', { name: rec.title.textContent, risk: RISK_LABEL[rec.call.risk] || rec.call.risk || T('risk_low') }));
        } else if (previous !== status && ['success', 'error', 'cancelled'].includes(status)) {
          this.announce(T('tool_result_announced', {
            name: rec.title.textContent,
            status: TOOL_STATUS_LABEL[status] || status,
            summary: rec.note.textContent || '',
          }));
        }
        t.lastKind = 'tool';
        this.updateProcess(t);
        this.scroll();
      },
      confirmTool(rec) {
        if (!rec || rec.status !== 'pending') return;
        if ((this.active || this.sending) && this.turn !== rec.ownerTurn) {
          rec.body.querySelectorAll('.t-actions button').forEach((button) => { button.disabled = true; });
          ui.toast(T('confirmation_busy'), 'warn');
          this.announce(T('confirmation_busy'));
          return;
        }
        if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
          rec.body.querySelectorAll('.t-actions button').forEach((button) => { button.disabled = false; });
          ui.toast(T('ws_lost'), 'error');
          return;
        }
        const t = rec.ownerTurn;
        this.turn = t;
        t.done = false;
        t.acceptNextRun = true;
        t.confirmingTool = true;
        this.setToolStatus(rec, 'running', t);
        rec.lastEvent = Object.assign({}, rec.lastEvent, { tool_summary: T('confirmed_running') });
        this.renderToolRecord(rec, rec.lastEvent, t);
        this.announce(T('tool_confirmed_announced', { name: rec.title.textContent }));
        this.setActive(true);
        this.updateProcess(t);
        queueMicrotask(() => { if (rec.summary.isConnected) rec.summary.focus({ preventScroll: true }); });
        this.ws.send(JSON.stringify({ type: 'confirm_tool', tool_call: rec.call }));
      },
      cancelTool(rec) {
        if (!rec || rec.status !== 'pending') return;
        const t = rec.ownerTurn;
        this.setToolStatus(rec, 'cancelled', t);
        this.releaseAnonymousTool(t, rec.id);
        rec.lastEvent = Object.assign({}, rec.lastEvent, { tool_summary: T('cancelled_not_run') });
        this.renderToolRecord(rec, rec.lastEvent, t);
        this.announce(T('tool_dismissed_announced', { name: rec.title.textContent }));
        this.updateProcess(t);
        queueMicrotask(() => {
          const target = t.process.open ? rec.summary : t.processSummary;
          if (target && target.isConnected) target.focus({ preventScroll: true });
        });
        if (t.done && t.pendingCount === 0) this.releaseTurnRuns(t);
      },
      eventTurn(ev) {
        const runID = String(ev && ev.run_id || '');
        if (!runID) return this.turn;
        let t = this.turnsByRun.get(runID);
        if (!t && ev.type === 'turn_start') {
          const candidate = this.turn;
          if (candidate && (candidate.runSeq.size === 0 || candidate.acceptNextRun)) {
            candidate.acceptNextRun = false;
            candidate.runSeq.set(runID, 0);
            this.turnsByRun.set(runID, candidate);
            t = candidate;
          }
        }
        if (!t) return null;
        const seq = Number(ev.seq) || 0;
        const previous = t.runSeq.get(runID) || 0;
        if (seq && seq <= previous) return null;
        if (seq) t.runSeq.set(runID, seq);
        return t;
      },
      releaseTurnRuns(t) {
        if (!t || t.pendingCount > 0) return;
        for (const [runID, owner] of this.turnsByRun) if (owner === t) this.turnsByRun.delete(runID);
        while (this.turnsByRun.size > 128) this.turnsByRun.delete(this.turnsByRun.keys().next().value);
      },
      onEvent(ev) {
        const t = this.eventTurn(ev);
        if (!t) return;
        if (ev.agent || ev.budget || ev.type === 'assistant_plan' || ev.tool_call && ev.tool_call.approval_resume === 'manual_tool_only') {
          this.projectAgentEvent(ev, t);
        }
        switch (ev.type) {
          case 'turn_start': this.appendStatus(T('analyzing'), false, t); break;
          case 'assistant_status': this.appendStatus(ev.status, false, t); break;
          case 'assistant_thinking': this.appendThinking(ev.thinking || '', ev, t); break;
          case 'assistant_delta': this.appendDelta(ev.delta || '', t); break;
          case 'assistant_plan':
          case 'assistant_agent':
          case 'assistant_budget': break;
          case 'tool_start': this.upsertTool(ev, 'running', t); break;
          case 'tool_result': this.upsertTool(ev, ev.tool_status || 'success', t); break;
          case 'confirmation_required': this.upsertTool(ev, 'pending', t); break;
          case 'assistant_done': this.finishTurn(ev, t); break;
          case 'turn_error': this.turnError(ev.error || 'error', t); break;
        }
      },
      finishTurn(ev, t = this.turn) {
        if (!t || t.done) {
          if (this.turn === t && this.active) this.setActive(false);
          return;
        }
        if (t.renderFrame) cancelAnimationFrame(t.renderFrame);
        t.renderFrame = 0;
        this.clearTransientStatus(t);
        this.finalizeThinking(t);
        const finalMessage = ev && typeof ev.message === 'string' && ev.message.trim() ? ev.message : '';
        // assistant_done.message 是本轮的权威快照。Deep 在确认边界会重写
        // 最终文案，不能让此前的流式草稿继续冒充最终结果。
        if (finalMessage) {
          t.answer.style.display = '';
          t.text = finalMessage;
        }
        t.confirmingTool = false;
        if (t.text) t.answer.replaceChildren(renderAssistant(t.text));
        t.answer.classList.remove('streaming');
        t.done = true;
        this.updateProcess(t);
        this.announce(T('answer_complete'));
        this.releaseTurnRuns(t);
        if (this.turn === t && this.active) this.setActive(false);
        this.scroll();
      },
      turnError(msg, t = this.turn) {
        if (t) {
          t.turnFailed = true;
          this.clearTransientStatus(t);
          const agentAlreadyShowsError = t.agentState && t.agentState.mode === 'deep' && Object.values(t.agentState.agents || {})
            .some((stage) => stage.status === 'error' && stage.summary === msg);
          if (agentAlreadyShowsError) {
            // Deep 的代理行已经给出失败原因，避免重复一条错误文本。
          } else this.appendStatus(msg, true, t);
          if (t.renderFrame) cancelAnimationFrame(t.renderFrame);
          t.renderFrame = 0;
          this.finalizeThinking(t);
          for (const rec of t.tools.values()) {
            if (rec.status !== 'running') continue;
            this.setToolStatus(rec, 'error', t);
            rec.lastEvent = Object.assign({}, rec.lastEvent, { tool_summary: msg });
            this.renderToolRecord(rec, rec.lastEvent, t);
          }
          if (t.text) t.answer.replaceChildren(renderAssistant(t.text));
          t.answer.classList.remove('streaming');
          t.done = true;
          this.updateProcess(t);
          this.announce(msg);
          this.releaseTurnRuns(t);
        } else {
          ui.toast(msg, 'error');
        }
        if (this.turn === t && this.active) this.setActive(false);
      },
    };

    /* ---------- 鉴权失败提示(启动时) ---------- */
    let authHintNode = null;
    function renderAuthHint() {
      if (authHintNode && authHintNode.isConnected) return;
      const box = el('div', 'ihtml-empty');
      box.setAttribute('role', 'alert');
      box.appendChild(el('h1', '', T('auth_missing')));
      box.appendChild(el('p', '', T('auth_missing_hint')));
      appEl.appendChild(box);
      authHintNode = box;
    }

    /* ---------- 宿主 postMessage 控制桥(iframe 场景) ---------- */
    let messageOrigins = Array.isArray(opts.messageOrigins)
      ? opts.messageOrigins.slice()
      : (typeof opts.messageOrigins === 'string' ? [opts.messageOrigins] : [location.origin]);
    if (standalone) {
      on(window, 'message', (ev) => {
        const sourceAllowed = ev.source === window.parent || ev.source === window.opener || ev.source === window;
        const originAllowed = messageOrigins.includes('*') || messageOrigins.includes(ev.origin);
        if (!sourceAllowed || !originAllowed) return;
        const d = ev.data;
        if (!d || typeof d !== 'object' || typeof d.type !== 'string' || !d.type.startsWith('ihtml:')) return;
        switch (d.type) {
          case 'ihtml:theme': theme.set(d.theme || {}, { persist: d.persist === true }); break;
          case 'ihtml:nav': nav.go(String(d.page || ''), d.params); break;
          case 'ihtml:chat': if (d.open === false) chat.close(); else chat.open(); break;
          case 'ihtml:refresh': items.refresh().catch(() => {}); break;
        }
      });
    }

    /* ---------- 启动 ---------- */
    let booted = false;
    let readyFinished = false;
    let envGridBound = false;
    let readOnly = false;
    let resolvedUser = '';
    async function loadConfig() {
      const cfg = await http.get('api/config');
      resolvedUser = String((cfg && cfg.user) || '');
      api.user = resolvedUser;
      if (Array.isArray(cfg && cfg.message_origins)) messageOrigins = cfg.message_origins.slice();
      api.config = (cfg && cfg.params) || {};
      readOnly = !!(cfg && cfg.read_only);
      rootEl.classList.toggle('ihtml-readonly', readOnly);
      bus.emit('ihtml:config', api.config);
      return cfg;
    }
    let settingsEntry = null;
    function configureActions(cfg) {
      chat.init(cfg);
      if (opts.panel !== false && !settingsEntry) {
        settingsEntry = el('button', 'ihtml-settings-entry', T('settings'));
        settingsEntry.type = 'button';
        settingsEntry.setAttribute('aria-haspopup', 'dialog');
        settingsEntry.onclick = () => panel.open();
        actionDock().insertBefore(settingsEntry, chat.entry || null);
      }
      if (actionDockEl) actionDockEl.style.display = '';
      syncActionDock();
    }
    function clearUserRuntime() {
      nav.navToken++;
      if (nav.requestController) nav.requestController.abort();
      nav.requestController = null;
      applyGeneration++;
      progress.done();
      for (const id of Array.from(registry.keys())) removeItem(id);
      templateRuntime.loadSeq++;
      templateRuntime.setPublished(null);
      nav.pagesList = [];
      nav.settledPage = null;
      const route = initialRoute();
      nav.currentPage = route.page;
      nav.currentParams = route.params;
      grid.saveTimers.forEach((timer) => clearTimeout(timer));
      grid.saveTimers.clear();
      grid.cfgs = {};
      grid.editing = false;
      itemEditor.loadSeq++;
      itemEditor.close(true);
      itemEditor.clearParents();
      itemEditor.page = '';
      itemEditor.persisted = false;
      itemEditor.conflict = false;
      itemEditor.loading = false;
      itemEditor.saving = false;
      itemEditor.deleting = false;
      itemEditor.state = itemLayoutCore ? itemLayoutCore.initial('') : null;
      templateStudio.close();
      templateStudio.record = null;
      templateStudio.remoteRecord = null;
      templateStudio.page = '';
      templateStudio.loadSeq++;
      templateStudio.loading = false;
      templateStudio.saving = false;
      templateStudio.publishing = false;
      templateStudio.loadError = null;
      templateStudio.state = templateCore ? templateCore.initial() : null;
      rootEl.classList.remove('ihtml-layout-editing', 'ihtml-item-editing', 'ihtml-item-dragging', 'ihtml-readonly');
      if (grid.doneBtn) { grid.doneBtn.remove(); grid.doneBtn = null; }
      clearTimeout(themePushTimer);
      themePushTimer = 0;
      clearTimeout(errTimer);
      errQueue = [];
      errorLog.length = 0;
      emptyState.hide();
      errorState.hide();
      if (safeModeNode) { safeModeNode.remove(); safeModeNode = null; }
      api.user = '';
      api.config = {};
      resolvedUser = '';
      readOnly = false;
      if (!hostTheme) {
        themeRevision++;
        themePullSeq++;
        themeValue = { ...themeDefaults };
        themeApply();
      }
    }
    async function loadCurrentState() {
      const epoch = requestEpoch;
      await nav.loadPages(false);
      if (epoch !== requestEpoch) throw staleRequestError();
      nav.reconcile(nav.currentPage, nav.currentParams);
      await templateRuntime.load(nav.currentPage);
      if (epoch !== requestEpoch) throw staleRequestError();
      await refreshItems();
      if (epoch !== requestEpoch) throw staleRequestError();
      await grid.load();
      if (epoch !== requestEpoch) throw staleRequestError();
      await itemEditor.load();
      if (epoch !== requestEpoch) throw staleRequestError();
      nav.settledPage = nav.currentPage;
      nav.emitChange();
    }
    function finishReadyOnce(cfg) {
      if (readyFinished || destroyed) return;
      readyFinished = true;
      booted = true;
      skeleton.hide();
      if (!envGridBound) {
        envGridBound = true;
        addDisposer(env.onChange(() => {
          if (env.mode === 'mobile' && grid.editing) grid.edit(false);
          else grid.apply();
          itemEditor.apply();
        }));
      }
      readyResolve(cfg);
      bus.emit('ihtml:ready', cfg);
      if (typeof opts.onReady === 'function') { try { opts.onReady(cfg); } catch { /* 忽略 */ } }
    }
    async function boot() {
      const epoch = requestEpoch;
      installErrorHandlers();
      if (destroyed) return;
      themeLoadLocal();
      applyEnv();
      skeleton.show();
      let cfg = { chat: false };
      let authed = true;
      try {
        cfg = await loadConfig();
      } catch (e) {
        if (destroyed || epoch !== requestEpoch || isStaleError(e)) return;
        authed = false;
        skeleton.hide();
        if (e.status === 401) renderAuthHint();
        else errorState.show(e.message, () => { errorState.hide(); void refreshAuthAndState(); });
      }
      if (destroyed || epoch !== requestEpoch) return;
      if (authed) configureActions(cfg);
      if (authed) {
        if (!hostTheme) await themePullRemote();
        if (destroyed || epoch !== requestEpoch) return;
        if (SAFE_MODE) {
          skeleton.hide();
          renderSafeMode();
        } else {
          try {
            const route = initialRoute();
            nav.currentPage = route.page;
            nav.currentParams = route.params;
            void connectSSE();
            const synced = await waitForSSESync();
            if (destroyed || epoch !== requestEpoch) return;
            if (!synced) {
              stopSSE();
              await loadCurrentState();
              void connectSSE();
            }
            if (destroyed || epoch !== requestEpoch) return;
            skeleton.hide();
          } catch (e) {
            if (destroyed || epoch !== requestEpoch || isStaleError(e)) return;
            skeleton.hide();
            errors.report('', 'boot: ' + e.message, e.stack);
            errorState.show(e.message, () => {
              errorState.hide();
              boot2();
            });
          }
        }
      }
      if (destroyed || epoch !== requestEpoch) return;
      finishReadyOnce(cfg);
    }

    async function refreshAuthAndState() {
      invalidateRequests();
      const epoch = requestEpoch;
      stopSSE();
      modalClosers.forEach((close) => close(false));
      modalClosers.clear();
      chat.resetForAuth();
      panel.reset();
      if (actionDockEl) actionDockEl.style.display = 'none';
      rootEl.classList.remove('ihtml-has-actions');
      clearUserRuntime();
      try {
        const cfg = await loadConfig();
        if (destroyed) return cfg;
        if (epoch !== requestEpoch) throw staleRequestError();
        authExpiredShown = false;
        if (authBanner) { authBanner.remove(); authBanner = null; }
        if (authHintNode) { authHintNode.remove(); authHintNode = null; }
        configureActions(cfg);
        if (!hostTheme) await themePullRemote();
        if (epoch !== requestEpoch) throw staleRequestError();
        if (SAFE_MODE) {
          await renderSafeMode();
        } else {
          void connectSSE();
          const synced = await waitForSSESync();
          if (epoch !== requestEpoch) throw staleRequestError();
          if (!synced) {
            stopSSE();
            await loadCurrentState();
            void connectSSE();
          }
        }
        finishReadyOnce(cfg);
        return cfg;
      } catch (err) {
        if (epoch !== requestEpoch || isStaleError(err)) throw err;
        stopSSE();
        clearUserRuntime();
        if (err && err.status === 401) renderAuthHint();
        else errorState.show(err && err.message || err, () => {
          errorState.hide();
          void refreshAuthAndState().catch(() => {});
        });
        throw err;
      }
    }
    // 错误态重试: 只重拉数据不重放全部初始化
    async function boot2() {
      progress.start();
      try {
        await enqueueState(() => loadCurrentState());
        if (!sse) void connectSSE();
      } catch (e) {
        errorState.show(e.message, () => { errorState.hide(); boot2(); });
      }
      progress.done();
    }

    /* ---------- 实例 API ---------- */
    const api = {
      version: '0.6.0',
      root: rootEl,
      params: pageParams,
      user: '',
      config: {},
      ready: () => readyPromise,
      refreshConfig: () => loadConfig(),
      refreshAuth: refreshAuthAndState,
      http, kv, bus, theme, ui, env, items, errors,
      fmt: lib.fmt,
      util: lib.util,
      chart: lib.chart,
      poll,
      listen,
      nav: {
        go: (page, params) => nav.go(page, params),
        current: () => nav.current(),
        pages: () => nav.pages(),
        onChange: (fn) => nav.onChange(fn),
      },
      layout: {
        sidebar: (open) => shell.sidebar(open),
        get shell() { return shell.active; },
        // 当前页面已有 Item 的直接排版；Template 结构编辑保留在 api.templates。
        edit: (on = true) => on === false ? itemEditor.requestClose() : itemEditor.open(),
        reset: () => itemEditor.reset(),
        setSize: (id, size) => {
          const state = itemEditor.state; const item = state && state.doc.items[id]; if (!item) return false;
          size = size && typeof size === 'object' ? size : {};
          if (state.doc.mode === 'free') itemEditor.dispatch({ type: 'patch', id, section: 'free', patch: { width: size.width || size.w, height: size.height || size.h } });
          else itemEditor.dispatch({ type: 'patch', id, section: 'flow', patch: { span: size.span || size.w, height: size.height || size.h } });
          return true;
        },
        setOrder: (ids) => {
          if (!itemEditor.state || !Array.isArray(ids)) return false;
          const first = itemEditor.state.doc.items[ids[0]]; const slot = first ? first.slot : 'main';
          itemEditor.dispatch({ type: 'reorder', slot, ids }); return true;
        },
        config: () => itemEditor.state ? JSON.parse(JSON.stringify(itemEditor.state.doc)) : null,
        get editing() { return itemEditor.editing; },
      },
      templates: {
        open: () => templateStudio.open(),
        close: () => templateStudio.requestClose(),
        save: () => templateStudio.save(),
        publish: () => templateStudio.publish(),
        config: () => templateStudio.state ? JSON.parse(JSON.stringify(templateStudio.state.doc)) : null,
        get editing() { return !!(templateStudio.dom && !templateStudio.dom.shell.hidden); },
      },
      panel: {
        open: (tab) => panel.open(tab),
        close: () => panel.close(),
      },
      chat: {
        open: () => chat.open(),
        close: () => chat.close(),
        get enabled() { return chat.enabled; },
      },
      destroy() {
        if (destroyed) return;
        destroyed = true;
        invalidateRequests();
        clearTimeout(errTimer);
        clearTimeout(envTimer);
        clearTimeout(themePushTimer);
        clearTimeout(progress.timer);
        grid.saveTimers.forEach((timer) => clearTimeout(timer));
        grid.saveTimers.clear();
        errQueue = [];
        modalClosers.forEach((close) => close(false));
        modalClosers.clear();
        chat.clearTurnTimers();
        chat.disconnect();
        itemEditor.destroy();
        templateStudio.close();
        templateRuntime.destroy();
        stopSSE();
        for (const id of Array.from(registry.keys())) removeItem(id);
        nav.listeners.clear();
        envListeners.clear();
        themeListeners.clear();
        disposers.splice(0).forEach((fn) => { try { fn(); } catch { /* 忽略 */ } });
        rootEl.remove();
        bus.emit('ihtml:destroyed', {});
        busMap.clear();
        readyResolve({ destroyed: true });
      },
    };
    boot();
    return api;
  }

  /* 从宿主页面推断主题(theme:'inherit') */
  function probeHostTheme(hostEl) {
    const ref = hostEl.parentElement || doc.body;
    const cs = getComputedStyle(ref);
    let bg = '';
    const transparent = (value) => !value || value === 'transparent' ||
      /rgba\(\s*[\d.]+\s*[,\s]+[\d.]+\s*[,\s]+[\d.]+\s*[,/]\s*0(?:\.0+)?\s*\)/.test(value);
    for (let n = ref; n; n = n.parentElement) {
      const c = getComputedStyle(n).backgroundColor;
      if (!transparent(c)) { bg = c; break; }
      if (n === doc.documentElement) break;
    }
    if (!bg) bg = 'rgb(255, 255, 255)';
    const probe = doc.createElement('a');
    probe.href = '#';
    probe.style.cssText = 'position:absolute;opacity:0;pointer-events:none';
    ref.appendChild(probe);
    const primary = getComputedStyle(probe).color;
    probe.remove();
    const m = /rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)/.exec(bg);
    const lum = m ? (0.2126 * m[1] + 0.7152 * m[2] + 0.0722 * m[3]) / 255 : 1;
    return {
      mode: lum < 0.45 ? 'dark' : 'light',
      bg,
      text: cs.color || '',
      font: cs.fontFamily || '',
      primary: primary || '',
    };
  }

  /* ---------- 全局入口 ---------- */
  const globalAPI = {
    version: '0.6.0',
    mounted: false,
    mount(hostEl, opts = {}) {
      if (globalAPI.mounted) throw new Error('ihtml: already mounted (call ihtml.destroy() first)');
      if (typeof hostEl === 'string') hostEl = doc.querySelector(hostEl);
      if (!hostEl || !hostEl.appendChild) throw new Error('ihtml.mount: container element required');
      const inst = createInstance(hostEl, opts);
      globalAPI.mounted = true;
      const instDestroy = inst.destroy;
      inst.destroy = () => { instDestroy(); globalAPI.mounted = false; };
      Object.assign(globalAPI, inst);
      return globalAPI;
    },
  };
  window.ihtml = globalAPI;

  /* 自动挂载: 独立页 #app[data-ihtml-standalone] 或声明式 [data-ihtml] */
  function autoMount() {
    if (globalAPI.mounted) return;
    const standaloneEl = doc.getElementById('app');
    if (standaloneEl && standaloneEl.hasAttribute('data-ihtml-standalone')) {
      globalAPI.mount(standaloneEl, { standalone: true });
      return;
    }
    const target = doc.querySelector('[data-ihtml]');
    if (!target) return;
    const d = target.dataset;
    const o = {};
    if (d.base) o.base = d.base;
    if (d.routing) o.routing = d.routing;
    if (d.page) o.page = d.page;
    if (d.chat === '0') o.chatEntry = false;
    if (d.locale) o.locale = d.locale;
    if (d.safe === '1') o.safe = true;
    const params = {};
    if (d.uid) params.uid = d.uid;
    if (d.params) { try { Object.assign(params, JSON.parse(d.params)); } catch { /* 忽略 */ } }
    o.params = params;
    if (d.theme === 'inherit') o.theme = 'inherit';
    else if (d.theme) { try { o.theme = JSON.parse(d.theme); } catch { /* 忽略 */ } }
    if (d.strings) { try { o.strings = JSON.parse(d.strings); } catch { /* 忽略 */ } }
    if (d.menu) o.menu = d.menu;
    globalAPI.mount(target, o);
  }
  autoMount();
  if (!globalAPI.mounted) doc.addEventListener('DOMContentLoaded', autoMount);
})();
