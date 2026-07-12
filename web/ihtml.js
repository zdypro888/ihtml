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
 *   auth(() => token 每次连接求值), onReady(cfg), onAuthError(), onConnection(state)
 * 实例 API 额外提供: destroy(), ready(Promise), fmt/util/chart/poll/listen/panel。 */
(() => {
  'use strict';
  const doc = document;
  const lib = window.__ihtmlLib || { makeT: () => (k) => k, fmt: {}, util: {}, chart: {} };

  function el(tag, cls, text) {
    const node = doc.createElement(tag);
    if (cls) node.className = cls;
    if (text != null) node.textContent = text;
    return node;
  }
  const clip = (s, n) => (s && s.length > n ? s.slice(0, n) : s || '');

  function createInstance(hostEl, opts) {
    const standalone = !!opts.standalone;
    const T = lib.makeT(opts.locale || navigator.language, opts.strings);

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
    const appEl = el('div', 'ihtml-app');
    rootEl.appendChild(appEl);
    hostEl.appendChild(rootEl);
    const themedRoots = [rootEl];

    /* ---------- 参数与模式 ---------- */
    const baseURL = opts.base ? new URL(String(opts.base), location.href) : new URL('.', location.href);
    if (!baseURL.pathname.endsWith('/')) baseURL.pathname += '/';
    const pageParams = standalone
      ? new URLSearchParams(location.search)
      : new URLSearchParams(opts.params || {});
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
    // 鉴权提供器: 每次(重)连接求值; REST 走请求头, SSE/WS 走 auth 查询参数
    let authValue = '';
    async function evalAuth() {
      if (typeof opts.auth !== 'function') return;
      try { authValue = String(await opts.auth() || ''); } catch { /* 保留旧值 */ }
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
    function apiURL(path, withAuth) {
      const u = new URL(path, baseURL);
      pageParams.forEach((v, k) => { if (!u.searchParams.has(k)) u.searchParams.append(k, v); });
      if (withAuth && authValue) u.searchParams.set('auth', authValue);
      return u;
    }
    let authExpiredShown = false;
    function handleAuthExpired() {
      bus.emit('ihtml:auth-expired', {});
      if (typeof opts.onAuthError === 'function') { try { opts.onAuthError(); } catch { /* 忽略 */ } }
      if (authExpiredShown) return;
      authExpiredShown = true;
      const bar = el('div', 'ihtml-banner', T('auth_expired'));
      const btn = el('button', '', T('retry'));
      btn.onclick = () => location.reload();
      bar.appendChild(btn);
      rootEl.appendChild(bar);
    }
    async function request(path, reqOpts = {}) {
      const ctrl = new AbortController();
      const timer = setTimeout(() => ctrl.abort(), reqOpts.timeout || 15000);
      try {
        const init = {
          method: reqOpts.method || 'GET',
          credentials: 'same-origin',
          signal: ctrl.signal,
          headers: {},
        };
        if (authValue) init.headers['X-Ihtml-User'] = authValue;
        if (reqOpts.body !== undefined) {
          init.headers['Content-Type'] = 'application/json';
          init.body = typeof reqOpts.body === 'string' ? reqOpts.body : JSON.stringify(reqOpts.body);
        }
        const res = await fetch(apiURL(path), init);
        const text = await res.text();
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
      } finally {
        clearTimeout(timer);
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
      onChange(fn) { envListeners.add(fn); return () => envListeners.delete(fn); },
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
    const themeListeners = new Set();
    let themeValue = { ...themeDefaults };
    let themePushTimer = 0;
    const darkMedia = matchMedia('(prefers-color-scheme: dark)');

    function themeApply() {
      const t = themeValue;
      const dark = t.mode === 'dark' || (t.mode !== 'light' && darkMedia.matches);
      if (standalone) doc.documentElement.classList.toggle('ihtml-dark', dark);
      for (const root of themedRoots) {
        root.classList.toggle('ihtml-dark', dark);
        const st = root.style;
        const setVar = (k, v) => (v ? st.setProperty(k, v) : st.removeProperty(k));
        for (const key in themeVarMap) setVar(themeVarMap[key], t[key]);
        setVar('--ui-radius', t.radius ? t.radius + 'px' : '');
        setVar('--ui-gap', t.gap ? t.gap + 'px' : '');
      }
      if (lib.chart.redrawAll) setTimeout(() => lib.chart.redrawAll(), 30);
      themeListeners.forEach((fn) => { try { fn(theme.get()); } catch { /* 忽略 */ } });
    }
    const theme = {
      get: () => ({ ...themeValue, dark: rootEl.classList.contains('ihtml-dark') }),
      set(patch, o = {}) {
        themeValue = { ...themeValue, ...(patch || {}) };
        themeApply();
        if (hostTheme) return; // 宿主控制主题时不持久化
        try { localStorage.setItem(THEME_KEY, JSON.stringify(themeValue)); } catch { /* 忽略 */ }
        if (o.persist === false) return;
        clearTimeout(themePushTimer);
        themePushTimer = setTimeout(() => {
          kv.set('theme', themeValue).catch(() => {});
        }, 400);
      },
      onChange(fn) { themeListeners.add(fn); return () => themeListeners.delete(fn); },
    };
    function themeLoadLocal() {
      if (hostTheme) {
        themeValue = { ...themeDefaults, ...hostTheme };
        themeApply();
        return;
      }
      try {
        const saved = JSON.parse(localStorage.getItem(THEME_KEY) || 'null');
        if (saved) themeValue = { ...themeDefaults, ...saved };
      } catch { /* 忽略 */ }
      themeApply();
    }
    async function themePullRemote() {
      try {
        const remote = await kv.get('theme');
        if (remote && typeof remote === 'object') {
          themeValue = { ...themeDefaults, ...remote };
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
      async get(key, def = null) {
        try {
          const out = await http.get('api/kv/' + encodeURIComponent(key));
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
          const handle = ui.modal({
            title,
            render(body) { if (text) body.appendChild(el('p', 'ui-muted', text)); },
            actions: [
              { label: cancelText, onClick(close) { close(); resolve(false); } },
              { label: okText, primary: true, onClick(close) { close(); resolve(true); } },
            ],
            onClose() { resolve(false); },
          });
          void handle;
        });
      },
      // modal: AI/宿主的标准弹窗, 不要手搓遮罩。render(bodyEl) 填内容。
      modal({ title = '', width = 0, render, actions = [], onClose } = {}) {
        const mask = el('div', 'ihtml-modal-mask');
        const modal = el('div', 'ihtml-modal');
        if (width) modal.style.width = 'min(' + width + 'px, 94vw)';
        if (title) modal.appendChild(el('h3', '', title));
        const body = el('div', 'body');
        modal.appendChild(body);
        if (typeof render === 'function') { try { render(body); } catch (e) { body.textContent = String(e); } }
        let closed = false;
        const close = () => {
          if (closed) return;
          closed = true;
          mask.remove();
          doc.removeEventListener('keydown', onKey);
          if (typeof onClose === 'function') { try { onClose(); } catch { /* 忽略 */ } }
        };
        const onKey = (ev) => { if (ev.key === 'Escape') close(); };
        if (actions.length) {
          const bar = el('div', 'actions');
          for (const a of actions) {
            const btn = el('button', 'ui-btn' + (a.primary ? ' primary' : ''), a.label);
            btn.onclick = () => (typeof a.onClick === 'function' ? a.onClick(close) : close());
            bar.appendChild(btn);
          }
          modal.appendChild(bar);
        }
        mask.onclick = (ev) => { if (ev.target === mask) close(); };
        doc.addEventListener('keydown', onKey);
        mask.appendChild(modal);
        rootEl.appendChild(mask);
        const focusable = modal.querySelector('button, input, select, textarea');
        if (focusable) focusable.focus();
        return { close, body };
      },
      // paginate: 分页条。onChange(page) 回调; 返回 {update}
      paginate(elOrSel, { page = 1, size = 20, total = 0, onChange } = {}) {
        const box = typeof elOrSel === 'string' ? doc.querySelector(elOrSel) : elOrSel;
        if (!box) return { update() {} };
        const state = { page, size, total };
        function renderBar() {
          const pages = Math.max(1, Math.ceil(state.total / state.size));
          state.page = Math.min(Math.max(1, state.page), pages);
          box.innerHTML = '';
          box.classList.add('ui-pagination');
          const mk = (label, target, disabled, current) => {
            const b = el('button', current ? 'cur' : '', label);
            b.disabled = !!disabled;
            if (!disabled && !current) b.onclick = () => { state.page = target; renderBar(); if (onChange) onChange(target); };
            box.appendChild(b);
          };
          mk('‹', state.page - 1, state.page <= 1);
          const win = [];
          for (let p = 1; p <= pages; p++) {
            if (p === 1 || p === pages || Math.abs(p - state.page) <= 2) win.push(p);
          }
          let last = 0;
          for (const p of win) {
            if (last && p - last > 1) box.appendChild(el('span', 'gap', '…'));
            mk(String(p), p, false, p === state.page);
            last = p;
          }
          mk('›', state.page + 1, state.page >= pages);
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
      ensure() {
        if (this.active) return;
        this.active = true;
        rootEl.classList.add('ihtml-has-shell');
        const header = el('header', 'ihtml-slot-header');
        const sidebar = el('aside', 'ihtml-slot-sidebar');
        const main = el('div', 'ihtml-slot-main');
        const footer = el('footer', 'ihtml-slot-footer');
        const existing = Array.from(appEl.childNodes);
        appEl.classList.add('ihtml-shell');
        appEl.append(sidebar, header, main, footer);
        existing.forEach((n) => main.appendChild(n));
        this.slots = { header, sidebar, main, footer };

        this.mask = el('div', 'ihtml-sidebar-mask');
        this.mask.onclick = () => this.sidebar(false);
        const burger = el('button', 'ihtml-hamburger', '☰');
        burger.type = 'button';
        burger.setAttribute('aria-label', 'menu');
        burger.onclick = () => this.sidebar(!appEl.classList.contains('ihtml-sidebar-open'));
        rootEl.append(this.mask, burger);
      },
      parentFor(slot) {
        if (!this.active) return appEl;
        return this.slots[slot] || this.slots.main;
      },
      sidebar(open) {
        if (!this.active) return;
        appEl.classList.toggle('ihtml-sidebar-open', !!open);
        if (this.mask) this.mask.style.display = open && env.mode === 'mobile' ? 'block' : 'none';
      },
    };

    /* ---------- Item 执行引擎 ---------- */
    const registry = new Map(); // id → { item, page, nodes, teardowns }

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
      rec.nodes.splice(0).forEach((n) => n.remove());
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
    async function activateScripts(container, itemId) {
      for (const old of Array.from(container.querySelectorAll('script'))) {
        const script = doc.createElement('script');
        for (const attr of old.attributes) script.setAttribute(attr.name, attr.value);
        script.text = old.text || '';
        const wait = old.src
          ? new Promise((res) => {
              script.onload = () => res();
              script.onerror = () => { errors.report(itemId, 'script load failed: ' + old.src); res(); };
              setTimeout(res, 10000);
            })
          : null;
        runningItem = itemId;
        old.replaceWith(script);
        runningItem = '';
        if (wait) await wait;
      }
    }

    async function applyItem(item) {
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
          style.textContent = item.content;
          rootEl.appendChild(style);
          rec.nodes.push(style);
        } else if (item.type === 'html') {
          const slot = (item.meta && item.meta.slot) || '';
          if (slot === 'sidebar' || slot === 'header' || slot === 'footer') shell.ensure();
          const container = el('section', 'ihtml-enter');
          container.dataset.ihtmlId = id;
          container.innerHTML = item.content;
          insertContainer(container, item.order || 0, shell.parentFor(slot));
          rec.nodes.push(container);
          setTimeout(() => container.classList.remove('ihtml-enter'), 300);
          await activateScripts(container, id);
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
      } catch (e) {
        errors.report(id, e && e.message || e, e && e.stack);
        const broken = el('div', 'ihtml-item-broken', T('item_broken', { id, msg: (e && e.message) || e }));
        shell.parentFor('').appendChild(broken);
        rec.nodes.push(broken);
      }
    }

    function removeItem(id) {
      if (!registry.has(id)) return;
      runTeardowns(id);
      removeItemNodes(id);
      registry.delete(id);
      bus.emit('ihtml:item-removed', { id });
    }
    function sortedItems(list) {
      return (list || []).slice().sort((a, b) => (a.order || 0) - (b.order || 0) || (a.id < b.id ? -1 : 1));
    }
    async function applyAll(list) {
      for (const id of Array.from(registry.keys())) removeItem(id);
      const sorted = sortedItems(list);
      for (const item of sorted) await applyItem(item);
      if (!registry.size) emptyState.show();
      return sorted.length;
    }
    async function refreshItems() {
      const out = await http.get('api/items?page=' + encodeURIComponent(nav.currentPage));
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
      try { fn(); } catch (e) { errors.report(itemId || '', 'poll: ' + e, e && e.stack); }
      return () => clearInterval(timer);
    }
    function listen(target, ev, fn, itemId) {
      target.addEventListener(ev, fn);
      const off = () => target.removeEventListener(ev, fn);
      const rec = currentItemRec(itemId);
      if (rec) rec.teardowns.push(off);
      return off;
    }

    const items = {
      list: () => Array.from(registry.values()).map((r) => ({ ...r.item })),
      apply: async (one) => { for (const it of Array.isArray(one) ? one : [one]) await applyItem(it); },
      remove: removeItem,
      refresh: refreshItems,
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
      },
      hide() { if (this.node) { this.node.remove(); this.node = null; } },
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
      },
      done() {
        if (!this.node) return;
        this.node.classList.remove('on');
        clearTimeout(this.timer);
        this.timer = setTimeout(() => { if (this.node) { this.node.remove(); this.node = null; } }, 400);
      },
    };
    const errorState = {
      node: null,
      show(message, retry) {
        this.hide();
        const box = el('div', 'ihtml-empty ihtml-error-state');
        box.appendChild(el('div', 'mark', '⚠️'));
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

      pages() { return this.pagesList.map((p) => ({ ...p })); },
      current() { return { page: this.currentPage, params: { ...this.currentParams } }; },
      onChange(fn) { this.listeners.add(fn); return () => this.listeners.delete(fn); },
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
          location.hash = this.hashFor(page, params);
          return;
        }
        this.activate(String(page || ''), params);
      },
      async loadPages() {
        try {
          const out = await http.get('api/pages');
          this.pagesList = out.pages || [];
        } catch { this.pagesList = []; }
        this.emitChange();
      },
      // 切页: stale-while-load —— 旧页保留到新页数据就绪, 顶部进度条指示
      async activate(page, params) {
        params = params || {};
        if (page === this.currentPage) {
          this.currentParams = params;
          this.emitChange();
          return;
        }
        const token = ++this.navToken;
        progress.start();
        let out = null;
        try {
          if (page) out = await http.get('api/items?page=' + encodeURIComponent(page) + '&global=0');
        } catch (e) {
          if (token !== this.navToken) return;
          progress.done();
          ui.toast(T('page_load_failed') + ': ' + e.message, 'error');
          errorState.show(e.message, () => this.activate(page, params));
          return;
        }
        if (token !== this.navToken) return;
        for (const [id, rec] of Array.from(registry)) {
          if (rec.page) removeItem(id);
        }
        this.currentPage = page;
        this.currentParams = params;
        if (out) {
          for (const it of sortedItems(out.items || [])) await applyItem(it);
        }
        await grid.load();
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

    /* ---------- 拖拽排版(grid): 用户直接拖块换序/拉宽高, 布局存 KV(独立于 Item 内容) ----------
     * 数据: kv "layout.<页面名|root>" = { order: [itemId...], items: { id: {w(1-12列), h(px, 可选)} } }
     * 只作用于主区域的 html Item 容器; 移动端强制单列且不可编辑。 */
    const grid = {
      cfgs: {},
      editing: false,
      saveTimer: 0,
      doneBtn: null,

      key() { return 'layout.' + (nav.currentPage || 'root'); },
      cfg() {
        const k = this.key();
        if (!this.cfgs[k]) this.cfgs[k] = { order: [], items: {} };
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

      async load() {
        const k = this.key();
        try {
          const v = await kv.get(k);
          this.cfgs[k] = v && typeof v === 'object' ? { order: v.order || [], items: v.items || {} } : { order: [], items: {} };
        } catch { this.cfgs[k] = { order: [], items: {} }; }
        this.apply();
      },
      saveSoon() {
        clearTimeout(this.saveTimer);
        this.saveTimer = setTimeout(() => {
          if (destroyed) return;
          kv.set(this.key(), this.cfg()).catch((e) => ui.toast(String(e.message || e), 'error'));
        }, 600);
      },

      apply() {
        if (destroyed) return;
        const box = this.container();
        const isOn = this.active() && env.mode !== 'mobile';
        box.classList.toggle('ihtml-grid', this.active());
        const c = this.cfg();
        const pos = {};
        c.order.forEach((id, i) => { pos[id] = i; });
        const ws = this.widgets().sort((a, b) => {
          const ia = pos[a.dataset.ihtmlId] !== undefined ? pos[a.dataset.ihtmlId] : 1000 + Number(a.dataset.ihtmlOrder || 0);
          const ib = pos[b.dataset.ihtmlId] !== undefined ? pos[b.dataset.ihtmlId] : 1000 + Number(b.dataset.ihtmlOrder || 0);
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
        this.editing = on;
        rootEl.classList.toggle('ihtml-layout-editing', on);
        if (on && !this.doneBtn) {
          const btn = el('button', 'ihtml-layout-done ui-btn primary', T('layout_done'));
          btn.onclick = () => this.edit(false);
          rootEl.appendChild(btn);
          this.doneBtn = btn;
        } else if (!on && this.doneBtn) {
          this.doneBtn.remove();
          this.doneBtn = null;
        }
        this.apply();
        if (!on) ui.toast(T('layout_saved'), 'success');
      },
      async reset() {
        const k = this.key();
        this.cfgs[k] = { order: [], items: {} };
        try { await kv.del(k); } catch { /* 忽略 */ }
        this.apply();
      },
      setSize(id, size) {
        const c = this.cfg();
        c.items[id] = Object.assign({}, c.items[id], size || {});
        if (c.items[id].h == null) delete c.items[id].h;
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
          const move = el('div', 'ihtml-w-move', '⠿ ' + (registry.get(w.dataset.ihtmlId)?.item.title || w.dataset.ihtmlId));
          const size = el('div', 'ihtml-w-size', '↘');
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
          const onUp = (ev) => {
            handle.removeEventListener('pointermove', onMove);
            handle.removeEventListener('pointerup', onUp);
            handle.removeEventListener('pointercancel', onUp);
            const size = { w: lastW };
            if (heightTouched) size.h = Math.max(64, Math.round(rect.height + ev.clientY - startY));
            else if (startSize.h) size.h = startSize.h;
            this.setSize(w.dataset.ihtmlId, size);
          };
          handle.addEventListener('pointermove', onMove);
          handle.addEventListener('pointerup', onUp);
          handle.addEventListener('pointercancel', onUp);
        });
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
            b.append(el('span', 'ico', p.icon || '•'), el('span', '', p.title));
            b.onclick = () => nav.go(p.name);
            menuNav.appendChild(b);
          });
          menuRoot.style.display = nav.pagesList.length ? '' : 'none';
        };
        nav.onChange(renderMenu);
        renderMenu();
      }
    }

    /* ---------- 空状态 ---------- */
    const emptyState = {
      node: null,
      show() {
        if (this.node || SAFE_MODE) return;
        const box = el('div', 'ihtml-empty');
        box.appendChild(el('div', 'mark', '✦'));
        box.appendChild(el('h1', '', T('empty_title')));
        if (chat.enabled) {
          box.appendChild(el('p', '', T('empty_chat_hint')));
          const btn = el('button', 'ui-btn primary', T('empty_start'));
          btn.onclick = () => chat.open();
          box.appendChild(btn);
        } else {
          box.appendChild(el('p', '', T('empty_api_hint')));
          const hint = el('div', 'hint');
          hint.textContent = "curl -X PUT '" + http.url('api/items') + "' \\\n  -H 'Content-Type: application/json' \\\n  -d '{\"items\":[{\"id\":\"hello\",\"type\":\"html\",\"order\":1,\n       \"content\":\"<div class=\\\"ui-card\\\" style=\\\"margin:24px\\\">Hello ihtml</div>\"}]}'";
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
    async function renderSafeMode() {
      const box = el('div', 'ihtml-empty');
      box.appendChild(el('div', 'mark', '🛟'));
      box.appendChild(el('h1', '', T('safe_title')));
      box.appendChild(el('p', '', T('safe_hint')));
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
    async function renderHistoryList(list, afterRollback) {
      list.textContent = '';
      try {
        const out = await http.get('api/revisions');
        for (const rev of (out.revisions || []).slice(0, 20)) {
          const row = el('div', 'ui-card');
          row.style.cssText = 'display:flex;align-items:center;gap:10px;padding:10px 14px;box-shadow:none';
          const label = el('div');
          label.appendChild(el('div', '', (rev.note || rev.id) + ' · ' + T('items_label', { n: rev.item_count })));
          label.appendChild(el('div', 'ui-muted', new Date(rev.created_at).toLocaleString()));
          label.style.flex = '1';
          const btn = el('button', 'ui-btn', T('rollback'));
          btn.onclick = async () => {
            if (!(await ui.confirm({ title: T('rollback_confirm_title'), text: T('rollback_confirm') }))) return;
            await http.post('api/revisions/' + encodeURIComponent(rev.id) + '/rollback');
            ui.toast(T('rolled_back'), 'success');
            if (afterRollback) afterRollback();
          };
          row.append(label, btn);
          list.appendChild(row);
        }
        if (!(out.revisions || []).length) list.appendChild(el('p', 'ui-muted', T('history_empty')));
      } catch (e) {
        list.appendChild(el('p', 'ui-muted', String(e.message || e)));
      }
    }

    /* ---------- 管理面板(齿轮): 主题/历史/错误 —— 无 AI 也能自助管理与救砖 ---------- */
    const panel = {
      dom: null,
      open(tab) {
        if (readOnly && tab !== 'errors') tab = tab || 'theme';
        if (!this.dom) this.build();
        this.dom.mask.style.display = '';
        this.dom.box.style.display = '';
        this.switch(tab || 'theme');
      },
      close() {
        if (!this.dom) return;
        this.dom.mask.style.display = 'none';
        this.dom.box.style.display = 'none';
      },
      switch(tab) {
        const d = this.dom;
        d.tabs.forEach((b) => b.classList.toggle('active', b.dataset.tab === tab));
        d.body.textContent = '';
        if (tab === 'theme') this.renderTheme(d.body);
        else if (tab === 'layout') this.renderLayout(d.body);
        else if (tab === 'history') renderHistoryList(d.body, () => { this.close(); });
        else this.renderErrors(d.body);
      },
      renderLayout(body) {
        body.appendChild(el('p', 'ui-muted', T('layout_hint')));
        const row = el('div', 'prow');
        const btn = el('button', 'ui-btn primary', grid.editing ? T('layout_done') : T('layout_edit'));
        btn.onclick = () => { this.close(); grid.edit(!grid.editing); };
        const reset = el('button', 'ui-btn', T('layout_reset'));
        reset.onclick = async () => { await grid.reset(); ui.toast(T('layout_saved'), 'success'); };
        row.append(btn, reset);
        body.appendChild(row);
      },
      build() {
        const mask = el('div', 'ihtml-chat-mask');
        mask.onclick = () => this.close();
        const box = el('div', 'ihtml-panel');
        const head = el('div', 'head');
        head.appendChild(el('span', 'title', T('panel_title')));
        const closeBtn = el('button', '', '✕');
        closeBtn.onclick = () => this.close();
        head.appendChild(closeBtn);
        const tabBar = el('div', 'tabs');
        const tabs = [];
        const defs = [['theme', T('panel_theme')], ['layout', T('panel_layout')], ['history', T('panel_history')], ['errors', T('panel_errors')]];
        for (const [key, label] of defs) {
          if (readOnly && (key === 'history' || key === 'layout')) continue;
          const b = el('button', '', label);
          b.dataset.tab = key;
          b.onclick = () => this.switch(key);
          tabBar.appendChild(b);
          tabs.push(b);
        }
        const body = el('div', 'pbody');
        box.append(head, tabBar, body);
        rootEl.append(mask, box);
        this.dom = { mask, box, body, tabs };
        mask.style.display = 'none';
        box.style.display = 'none';
      },
      renderTheme(body) {
        const cur = theme.get();
        const row1 = el('div', 'prow');
        row1.appendChild(el('span', 'plabel', T('theme_mode')));
        for (const [mode, label] of [['auto', T('theme_auto')], ['light', T('theme_light')], ['dark', T('theme_dark')]]) {
          const b = el('button', 'ui-btn' + (cur.mode === mode ? ' primary' : ''), label);
          b.onclick = () => { theme.set({ mode }); this.renderThemeAgain(body); };
          row1.appendChild(b);
        }
        body.appendChild(row1);
        const row2 = el('div', 'prow');
        row2.appendChild(el('span', 'plabel', T('theme_primary')));
        for (const c of ['#6366f1', '#0ea5e9', '#059669', '#d97706', '#dc2626', '#8b5cf6', '#c99a45']) {
          const dot = el('button', 'pswatch');
          dot.style.background = c;
          if (cur.primary === c) dot.classList.add('cur');
          dot.onclick = () => { theme.set({ primary: c }); this.renderThemeAgain(body); };
          row2.appendChild(dot);
        }
        body.appendChild(row2);
        const row3 = el('div', 'prow');
        row3.appendChild(el('span', 'plabel', T('theme_radius')));
        for (const rVal of [6, 10, 14]) {
          const b = el('button', 'ui-btn' + (Number(cur.radius) === rVal ? ' primary' : ''), rVal + 'px');
          b.onclick = () => { theme.set({ radius: rVal }); this.renderThemeAgain(body); };
          row3.appendChild(b);
        }
        body.appendChild(row3);
      },
      renderThemeAgain(body) { body.textContent = ''; this.renderTheme(body); },
      renderErrors(body) {
        const list = errors.list().slice(-20).reverse();
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
          rootEl.appendChild(offlineDot);
        }
      } else if (offlineDot) {
        offlineDot.remove();
        offlineDot = null;
      }
    }
    function connectSSE() {
      if (SAFE_MODE || destroyed) return;
      if (sse) { sse.close(); sse = null; }
      const source = new EventSource(apiURL('api/events', true));
      sse = source;
      source.onopen = () => {
        const wasDown = sseRetry > 0;
        sseRetry = 0;
        setConnState('online');
        if (wasDown) {
          refreshItems().catch(() => {});
          nav.loadPages();
        }
      };
      source.onerror = () => {
        // 不依赖 EventSource 内建重试(HTTP 错误会永久停止): 主动关闭指数退避重建
        source.close();
        if (sse !== source || destroyed) return;
        sse = null;
        sseRetry = Math.min(sseRetry + 1, 6);
        if (sseRetry >= 2) setConnState('offline');
        clearTimeout(sseTimer);
        sseTimer = setTimeout(connectSSE, Math.min(30000, 1000 * Math.pow(2, sseRetry)));
      };
      source.onmessage = async (ev) => {
        let data = null;
        try { data = JSON.parse(ev.data); } catch { return; }
        if (data.type === 'items') {
          if (data.op === 'put') {
            for (const it of data.items || []) {
              if (!it.page || it.page === nav.currentPage) await applyItem(it);
            }
          } else if (data.op === 'delete') {
            (data.ids || []).forEach(removeItem);
            if (!registry.size) emptyState.show();
          } else {
            await refreshItems();
          }
          grid.apply();
          bus.emit('ihtml:items-changed', data);
        } else if (data.type === 'pages') {
          nav.loadPages();
        } else if (data.type === 'kv') {
          if (data.key === 'theme' && !hostTheme) themePullRemote();
          if (data.key && data.key.indexOf('layout.') === 0 && data.key === grid.key() && !grid.editing) grid.load();
          bus.emit('ihtml:kv-changed', data);
        }
      };
    }
    addDisposer(() => { clearTimeout(sseTimer); if (sse) sse.close(); });

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
    const TOOL_STATUS_LABEL = { running: T('tool_running'), pending: T('tool_pending'), success: T('tool_success'), error: T('tool_error') };
    const RISK_LABEL = { low: T('risk_low'), medium: T('risk_medium'), high: T('risk_high'), blocked: T('risk_blocked') };

    function pretty(v) {
      if (v === undefined || v === null) return '';
      try {
        if (typeof v === 'string') return JSON.stringify(JSON.parse(v), null, 2);
        return JSON.stringify(v, null, 2);
      } catch { return String(v); }
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
      dom: null,
      log: null,
      ws: null,
      active: false,
      follow: true,
      model: '',
      modelInfo: null,
      turn: null,
      everConnected: false,

      init(cfg) {
        this.enabled = !!(cfg && cfg.chat) && !readOnly;
        if (!this.enabled || !chatEntryEnabled) return;
        const entry = el('button', 'ihtml-chat-entry');
        entry.type = 'button';
        entry.append(el('span', 'spark', '✦'), doc.createTextNode(T('chat_entry')));
        entry.onclick = () => this.open();
        rootEl.appendChild(entry);
      },
      open() {
        if (!this.enabled) return;
        if (!this.dom) this.build();
        this.dom.mask.style.display = '';
        this.dom.panel.style.display = '';
        this.dom.input.focus();
        if (!this.modelInfo) this.loadModels();
      },
      close() {
        if (!this.dom) return;
        this.dom.mask.style.display = 'none';
        this.dom.panel.style.display = 'none';
      },

      build() {
        const mask = el('div', 'ihtml-chat-mask');
        mask.onclick = () => this.close();
        const panelEl = el('div', 'ihtml-chat');

        const head = el('div', 'ihtml-chat-head');
        const title = el('div', 'title');
        title.append(el('span', 'spark', '✦'), doc.createTextNode(T('chat_title')));
        const newBtn = el('button', '', T('new_session'));
        newBtn.onclick = () => this.reset();
        const closeBtn = el('button', '', '✕');
        closeBtn.setAttribute('aria-label', 'close');
        closeBtn.onclick = () => this.close();
        head.append(title, newBtn, closeBtn);

        const toolbar = el('div', 'ihtml-chat-toolbar');
        toolbar.style.display = 'none'; // 模型选择器默认隐藏(model_select 下发才显示)
        const modelInput = el('input', 'model');
        modelInput.name = 'ihtml-model';
        modelInput.placeholder = T('model_placeholder');
        modelInput.setAttribute('list', 'ihtml-model-list');
        modelInput.onchange = () => this.saveModel(modelInput.value.trim());
        const dataList = el('datalist');
        dataList.id = 'ihtml-model-list';
        const freshBtn = el('button', 'fresh', T('refresh_models'));
        freshBtn.onclick = () => this.loadModels(true);
        toolbar.append(modelInput, freshBtn, dataList);

        const log = el('div', 'ihtml-chat-log');
        log.addEventListener('scroll', () => {
          this.follow = log.scrollTop + log.clientHeight >= log.scrollHeight - 48;
        });

        const foot = el('div', 'ihtml-chat-foot');
        const input = el('textarea');
        input.name = 'ihtml-composer';
        input.placeholder = T('composer_placeholder');
        input.rows = 1;
        input.addEventListener('input', () => {
          input.style.height = 'auto';
          input.style.height = Math.min(input.scrollHeight, 140) + 'px';
        });
        let composing = false;
        let composedAt = 0;
        input.addEventListener('compositionstart', () => { composing = true; });
        input.addEventListener('compositionend', () => { composing = false; composedAt = Date.now(); });
        input.addEventListener('keydown', (ev) => {
          if (ev.key === 'Escape') { this.close(); return; }
          if (ev.key !== 'Enter') return;
          if (ev.shiftKey || ev.altKey || ev.ctrlKey || ev.metaKey) return;
          if (ev.isComposing || composing || Date.now() - composedAt < 80) return;
          ev.preventDefault();
          this.send();
        });
        const sendBtn = el('button', '', T('send'));
        sendBtn.onclick = () => (this.active ? this.cancel() : this.send());
        foot.append(input, sendBtn);

        panelEl.addEventListener('keydown', (ev) => { if (ev.key === 'Escape') this.close(); });
        panelEl.append(head, toolbar, log, foot);
        rootEl.append(mask, panelEl);
        this.dom = { mask, panel: panelEl, log, input, sendBtn, modelInput, dataList, freshBtn, toolbar };
        this.log = log;
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
        this.scroll();
      },

      async loadModels(force) {
        const { modelInput, dataList, freshBtn, toolbar } = this.dom;
        freshBtn.disabled = true;
        try {
          this.modelInfo = await http.get('api/chat/models');
        } catch {
          this.modelInfo = { model_ready: false, models: [] };
        } finally {
          freshBtn.disabled = false;
        }
        const models = this.modelInfo.models || [];
        toolbar.style.display = this.modelInfo.model_select ? '' : 'none';
        dataList.innerHTML = '';
        for (const m of models) {
          const opt = el('option');
          opt.value = m;
          dataList.appendChild(opt);
        }
        const saved = this.modelInfo.model_select ? (localStorage.getItem('ihtml.model') || '') : '';
        const preferred = saved && (!models.length || models.includes(saved))
          ? saved
          : (this.modelInfo.current_model || models[0] || saved);
        modelInput.value = preferred || '';
        this.saveModel(modelInput.value.trim());
        if (this.modelInfo.model_ready === false) {
          this.notice(T('ai_unavailable'));
        }
      },
      saveModel(m) {
        this.model = m || '';
        try { localStorage.setItem('ihtml.model', this.model); } catch { /* 忽略 */ }
      },

      scroll(force) {
        if (!this.follow && !force) return;
        requestAnimationFrame(() => { this.log.scrollTop = this.log.scrollHeight; });
      },

      ensureWS() {
        if (this.ws && this.ws.readyState === WebSocket.OPEN) return Promise.resolve(this.ws);
        const wsURL = apiURL('api/chat/ws', true);
        wsURL.searchParams.set('csid', chatSessionID());
        wsURL.protocol = wsURL.protocol === 'https:' ? 'wss:' : 'ws:';
        return new Promise((resolve, reject) => {
          const ws = new WebSocket(wsURL);
          ws.onopen = () => {
            this.ws = ws;
            if (this.everConnected) this.systemNote(T('chat_reconnected'));
            this.everConnected = true;
            resolve(ws);
          };
          ws.onerror = () => reject(new Error(T('chat_broken')));
          ws.onclose = () => {
            if (this.ws === ws) {
              this.ws = null;
              if (this.dom && !destroyed) this.systemNote(T('chat_disconnected'));
            }
            this.setActive(false);
          };
          ws.onmessage = (ev) => {
            let data = null;
            try { data = JSON.parse(ev.data); } catch { return; }
            this.onEvent(data);
          };
        });
      },
      reset() {
        if (this.ws) { this.ws.close(); this.ws = null; }
        chatSessionID(true); // 换会话号 = 服务端上下文重开
        this.log.innerHTML = '';
        this.turn = null;
        this.everConnected = false;
        this.setActive(false);
        this.notice(T('session_reset'));
      },
      setActive(v) {
        this.active = v;
        if (!this.dom) return;
        this.dom.sendBtn.textContent = v ? T('stop') : T('send');
        this.dom.sendBtn.classList.toggle('stop', v);
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
            path: (standalone ? location.pathname + location.search : '(embedded)') + '#/' + nav.currentPage,
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
        if (!text || this.active) return;
        if (!this.modelInfo) await this.loadModels();
        if (!this.model) { ui.toast(T('model_needed'), 'warn'); return; }
        this.dom.input.value = '';
        this.dom.input.style.height = 'auto';
        const um = el('article', 'ihtml-msg user');
        um.appendChild(el('div', 'author', T('author_user')));
        um.appendChild(el('div', 'content', text));
        this.log.appendChild(um);
        this.follow = true;
        this.setActive(true);
        this.beginTurn();
        try {
          const ws = await this.ensureWS();
          ws.send(JSON.stringify({
            type: 'user_message',
            messages: [{ role: 'user', content: text }],
            model: this.model,
            tools_enabled: true,
            client_context: this.clientContext(),
          }));
        } catch (e) {
          this.turnError(e.message);
        }
      },
      cancel() {
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
          this.ws.send(JSON.stringify({ type: 'cancel' }));
        }
        this.setActive(false);
      },

      beginTurn() {
        const card = el('article', 'ihtml-msg assistant');
        card.appendChild(el('div', 'author', 'AI'));
        const process = el('details', 'ihtml-process active');
        process.open = true;
        const summary = el('summary');
        const label = el('span', '', T('process'));
        const note = el('small', '', T('analyzing'));
        summary.append(label, note);
        const pBody = el('div', 'pbody');
        process.append(summary, pBody);
        const answer = el('div', 'answer');
        answer.style.display = 'none';
        card.append(process, answer);
        this.log.appendChild(card);
        const turn = {
          card, process, note, pBody, answer,
          text: '', thinkingPre: null, lastKind: '', lastStatusNode: null,
          tools: new Map(), toolCount: 0, pendingCount: 0, errorCount: 0, done: false,
        };
        card._turn = turn;
        this.turn = turn;
        this.scroll(true);
      },
      processSummaryText(t) {
        if (!t.done) {
          for (const rec of t.tools.values()) {
            if (rec.status === 'running') return T('running_tool', { name: rec.title.textContent });
          }
          return T('analyzing');
        }
        if (t.errorCount > 0) return T('tools_failed', { n: t.errorCount });
        if (t.pendingCount > 0) return T('tools_pending', { n: t.pendingCount });
        if (t.toolCount > 0) return T('tools_called', { n: t.toolCount });
        return T('done');
      },
      updateProcess(t) {
        t = t || this.turn;
        if (!t) return;
        t.process.classList.toggle('active', !t.done);
        t.process.classList.toggle('pending', t.pendingCount > 0);
        t.process.classList.toggle('error', t.errorCount > 0);
        t.note.textContent = this.processSummaryText(t);
        if (t.done) {
          if (!t.pBody.childNodes.length) t.process.remove();
          else if (t.pendingCount === 0 && t.errorCount === 0) t.process.open = false;
          else t.process.open = true;
        }
      },
      appendStatus(text, isError) {
        const t = this.turn;
        text = (text || '').trim();
        if (!t || !text) return;
        if (t.lastKind === 'status' && t.lastStatusNode) {
          t.lastStatusNode.textContent = text;
        } else {
          const node = el('div', 'ihtml-pline', text);
          t.pBody.appendChild(node);
          t.lastStatusNode = node;
          t.lastKind = 'status';
        }
        if (isError) t.lastStatusNode.style.color = 'var(--ui-err)';
        this.updateProcess(t);
        this.scroll();
      },
      appendThinking(delta) {
        const t = this.turn;
        if (!t || !delta) return;
        if (!t.thinkingPre) {
          const wrap = el('div', 'ihtml-think');
          wrap.appendChild(el('div', 'subtitle', T('thinking')));
          t.thinkingPre = el('pre');
          wrap.appendChild(t.thinkingPre);
          t.pBody.appendChild(wrap);
        }
        t.thinkingPre.textContent += delta;
        t.thinkingPre.scrollTop = t.thinkingPre.scrollHeight;
        t.lastKind = 'thinking';
        this.scroll();
      },
      appendDelta(delta) {
        const t = this.turn;
        if (!t || !delta) return;
        if (!t.text && !t.tools.size && t.lastKind === 'status' && t.lastStatusNode) {
          t.lastStatusNode.remove();
          t.lastStatusNode = null;
          t.lastKind = '';
        }
        t.text += delta;
        t.answer.style.display = '';
        t.answer.replaceChildren(renderAssistant(t.text));
        this.updateProcess(t);
        this.scroll();
      },

      upsertTool(ev, status) {
        const t = this.turn;
        if (!t) return;
        const call = ev.tool_call || {};
        const id = call.id || ev.tool_name || 't_' + (ev.seq || Date.now());
        let rec = t.tools.get(id);
        if (!rec) {
          const card = el('details', 'ihtml-tool');
          const summary = el('summary');
          const chip = el('span', 'ihtml-chip');
          const title = el('span', 't-title');
          const note = el('small');
          summary.append(chip, title, note);
          const body = el('div', 'tbody');
          card.append(summary, body);
          t.pBody.appendChild(card);
          rec = { card, chip, title, note, body, call, parts: {}, status: '', ownerTurn: t, countedPending: false, countdown: 0 };
          t.tools.set(id, rec);
          t.toolCount++;
        }
        if (ev.tool_call) rec.call = ev.tool_call;
        rec.status = status;
        rec.title.textContent = ev.tool_title || call.title || ev.tool_name || call.name || 'tool';
        rec.chip.textContent = TOOL_STATUS_LABEL[status] || status;
        rec.chip.className = 'ihtml-chip ' + status;
        if (ev.tool_summary) rec.note.textContent = ev.tool_summary;

        if (ev.tool_input && !rec.parts.input) {
          rec.parts.input = el('div', 't-input', ev.tool_input);
          rec.body.appendChild(rec.parts.input);
        }
        if (status === 'pending') {
          rec.card.open = true;
          if (!rec.countedPending) { t.pendingCount++; rec.countedPending = true; }
          if (!rec.parts.meta) {
            const meta = el('div', 't-meta');
            if (rec.call.risk) meta.appendChild(el('span', 'ihtml-chip risk-' + rec.call.risk, RISK_LABEL[rec.call.risk] || rec.call.risk));
            if (rec.call.path) meta.appendChild(el('span', 't-endpoint', (rec.call.method || '') + ' ' + rec.call.path));
            // 确认卡有效期倒计时
            if (rec.call.confirmation_expires) {
              const cd = el('span', 't-endpoint');
              meta.appendChild(cd);
              const tick = () => {
                const left = rec.call.confirmation_expires * 1000 - Date.now();
                if (left <= 0) {
                  cd.textContent = T('confirm_expired');
                  clearInterval(rec.countdown);
                  if (rec.parts.actions) rec.parts.actions.querySelectorAll('button').forEach((b) => { b.disabled = true; });
                  return;
                }
                cd.textContent = T('expires_in') + ' ' + Math.ceil(left / 60000) + 'min';
              };
              rec.countdown = setInterval(tick, 15000);
              tick();
            }
            rec.body.appendChild(meta);
            rec.parts.meta = meta;
          }
          if (!rec.parts.params && rec.call.body !== undefined) {
            const box = el('div');
            box.appendChild(el('div', 't-params-title', T('params_title')));
            const pre = el('pre');
            pre.textContent = pretty(rec.call.body);
            box.appendChild(pre);
            rec.body.appendChild(box);
            rec.parts.params = box;
          }
          if (!rec.parts.actions) {
            const bar = el('div', 't-actions');
            const okBtn = el('button', 'ok', T('confirm_run'));
            const noBtn = el('button', '', T('cancel'));
            okBtn.onclick = (e) => { e.preventDefault(); this.confirmTool(rec); };
            noBtn.onclick = (e) => { e.preventDefault(); this.cancelTool(rec); };
            bar.append(okBtn, noBtn);
            rec.body.appendChild(bar);
            rec.parts.actions = bar;
          }
        }
        if (status === 'success' || status === 'error') {
          if (rec.parts.actions) { rec.parts.actions.remove(); delete rec.parts.actions; }
          if (rec.countedPending) { t.pendingCount--; rec.countedPending = false; }
          if (rec.countdown) { clearInterval(rec.countdown); rec.countdown = 0; }
          if (status === 'error') { t.errorCount++; rec.card.open = true; }
        }
        if ((ev.tool_result !== undefined || ev.tool_debug !== undefined) && !rec.parts.debug) {
          const dbg = el('details', 't-debug');
          dbg.appendChild(el('summary', '', T('debug_title')));
          const pre = el('pre');
          pre.textContent = [pretty(ev.tool_debug), pretty(ev.tool_result)].filter(Boolean).join('\n\n');
          dbg.appendChild(pre);
          rec.body.appendChild(dbg);
          rec.parts.debug = dbg;
        }
        t.lastKind = 'tool';
        this.updateProcess(t);
        this.scroll();
      },
      confirmTool(rec) {
        if (!this.ws || this.ws.readyState !== WebSocket.OPEN) { ui.toast(T('ws_lost'), 'error'); return; }
        const t = rec.ownerTurn;
        this.turn = t;
        t.done = false;
        rec.status = 'running';
        rec.chip.textContent = TOOL_STATUS_LABEL.running;
        rec.chip.className = 'ihtml-chip running';
        rec.note.textContent = T('confirmed_running');
        if (rec.parts.actions) { rec.parts.actions.remove(); delete rec.parts.actions; }
        if (rec.countedPending) { t.pendingCount--; rec.countedPending = false; }
        if (rec.countdown) { clearInterval(rec.countdown); rec.countdown = 0; }
        this.setActive(true);
        this.updateProcess(t);
        this.ws.send(JSON.stringify({ type: 'confirm_tool', tool_call: rec.call }));
      },
      cancelTool(rec) {
        const t = rec.ownerTurn;
        rec.status = 'error';
        rec.chip.textContent = T('tool_cancelled');
        rec.chip.className = 'ihtml-chip error';
        rec.note.textContent = T('cancelled_not_run');
        if (rec.parts.actions) { rec.parts.actions.remove(); delete rec.parts.actions; }
        if (rec.countedPending) { t.pendingCount--; rec.countedPending = false; }
        if (rec.countdown) { clearInterval(rec.countdown); rec.countdown = 0; }
        this.updateProcess(t);
      },
      onEvent(ev) {
        if (!this.turn) return;
        switch (ev.type) {
          case 'turn_start': this.appendStatus(T('analyzing')); break;
          case 'assistant_status': this.appendStatus(ev.status); break;
          case 'assistant_thinking': this.appendThinking(ev.thinking || ''); break;
          case 'assistant_delta': this.appendDelta(ev.delta || ''); break;
          case 'tool_start': this.upsertTool(ev, 'running'); break;
          case 'tool_result': this.upsertTool(ev, ev.tool_status || 'success'); break;
          case 'confirmation_required': this.upsertTool(ev, 'pending'); break;
          case 'assistant_done': this.finishTurn(ev); break;
          case 'turn_error': this.turnError(ev.error || 'error'); break;
        }
      },
      finishTurn(ev) {
        const t = this.turn;
        if (!t || t.done) { this.active && this.setActive(false); return; }
        if (!t.text && ev && ev.message) {
          t.answer.style.display = '';
          t.text = ev.message;
          t.answer.replaceChildren(renderAssistant(ev.message));
        }
        t.done = true;
        this.updateProcess(t);
        if (this.active) this.setActive(false);
        this.scroll();
      },
      turnError(msg) {
        const t = this.turn;
        if (t) {
          t.errorCount++;
          this.appendStatus(msg, true);
          t.done = true;
          this.updateProcess(t);
        } else {
          ui.toast(msg, 'error');
        }
        if (this.active) this.setActive(false);
      },
    };

    /* ---------- 鉴权失败提示(启动时) ---------- */
    function renderAuthHint() {
      const box = el('div', 'ihtml-empty');
      box.appendChild(el('div', 'mark', '🔑'));
      box.appendChild(el('h1', '', T('auth_missing')));
      box.appendChild(el('p', '', T('auth_missing_hint')));
      appEl.appendChild(box);
    }

    /* ---------- 宿主 postMessage 控制桥(iframe 场景) ---------- */
    if (standalone) {
      on(window, 'message', (ev) => {
        const d = ev.data;
        if (!d || typeof d !== 'object' || typeof d.type !== 'string' || !d.type.startsWith('ihtml:')) return;
        switch (d.type) {
          case 'ihtml:theme': theme.set(d.theme || {}, { persist: d.persist === true }); break;
          case 'ihtml:nav': nav.go(String(d.page || ''), d.params); break;
          case 'ihtml:chat': if (d.open === false) chat.close(); else chat.open(); break;
          case 'ihtml:refresh': refreshItems().catch(() => {}); break;
        }
      });
    }

    /* ---------- 启动 ---------- */
    let booted = false;
    let readOnly = false;
    async function loadConfig() {
      const cfg = await http.get('api/config');
      api.config = (cfg && cfg.params) || {};
      readOnly = !!(cfg && cfg.read_only);
      rootEl.classList.toggle('ihtml-readonly', readOnly);
      bus.emit('ihtml:config', api.config);
      return cfg;
    }
    async function boot() {
      installErrorHandlers();
      await evalAuth();
      themeLoadLocal();
      applyEnv();
      skeleton.show();
      let cfg = { chat: false };
      let authed = true;
      try {
        cfg = await loadConfig();
      } catch (e) {
        if (e.status === 401) { authed = false; skeleton.hide(); renderAuthHint(); }
      }
      chat.init(cfg);
      if (authed) {
        // 齿轮管理面板(主题/历史/错误)——嵌入模式的救砖与自助入口
        if (opts.panel !== false) {
          const gear = el('button', 'ihtml-gear', '⚙');
          gear.type = 'button';
          gear.setAttribute('aria-label', 'settings');
          gear.onclick = () => panel.open();
          rootEl.appendChild(gear);
        }
        if (!hostTheme) themePullRemote();
        env.onChange(() => grid.apply()); // 移动/PC 切换时重算网格
        if (SAFE_MODE) {
          skeleton.hide();
          renderSafeMode();
        } else {
          try {
            await nav.loadPages();
            let page = '';
            let params = {};
            if (routing === 'hash') {
              ({ page, params } = nav.parseHash());
              if (!page && nav.pagesList.length) {
                page = nav.pagesList[0].name;
                history.replaceState(null, '', nav.hashFor(page, params));
              }
            } else {
              page = opts.page || (nav.pagesList.length ? nav.pagesList[0].name : '');
            }
            nav.currentPage = page;
            nav.currentParams = params;
            const out = await http.get('api/items?page=' + encodeURIComponent(page));
            skeleton.hide();
            await applyAll(out.items || []);
            await grid.load();
            nav.emitChange();
          } catch (e) {
            skeleton.hide();
            errors.report('', 'boot: ' + e.message, e.stack);
            errorState.show(e.message, () => {
              errorState.hide();
              boot2();
            });
          }
          connectSSE();
        }
      }
      booted = true;
      readyResolve(cfg);
      bus.emit('ihtml:ready', cfg);
      if (typeof opts.onReady === 'function') { try { opts.onReady(cfg); } catch { /* 忽略 */ } }
    }
    // 错误态重试: 只重拉数据不重放全部初始化
    async function boot2() {
      progress.start();
      try {
        await nav.loadPages();
        await refreshItems();
        nav.emitChange();
      } catch (e) {
        errorState.show(e.message, () => { errorState.hide(); boot2(); });
      }
      progress.done();
    }

    /* ---------- 实例 API ---------- */
    const api = {
      version: '0.5.0',
      root: rootEl,
      params: pageParams,
      config: {},
      ready: () => readyPromise,
      refreshConfig: () => loadConfig(),
      refreshAuth: async () => { await evalAuth(); connectSSE(); },
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
        // 拖拽排版
        edit: (on) => grid.edit(on),
        reset: () => grid.reset(),
        setSize: (id, size) => grid.setSize(id, size),
        setOrder: (ids) => { grid.cfg().order = ids.slice(); grid.apply(); grid.saveSoon(); },
        config: () => JSON.parse(JSON.stringify(grid.cfg())),
        get editing() { return grid.editing; },
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
        try { if (chat.ws) chat.ws.close(); } catch { /* 忽略 */ }
        for (const id of Array.from(registry.keys())) removeItem(id);
        disposers.splice(0).forEach((fn) => { try { fn(); } catch { /* 忽略 */ } });
        rootEl.remove();
        bus.emit('ihtml:destroyed', {});
        busMap.clear();
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
    for (let n = ref; n && n !== doc.documentElement; n = n.parentElement) {
      const c = getComputedStyle(n).backgroundColor;
      if (c && c !== 'transparent' && !/rgba\(\s*\d+\s*,\s*\d+\s*,\s*\d+\s*,\s*0\s*\)/.test(c)) { bg = c; break; }
    }
    if (!bg) bg = getComputedStyle(doc.body).backgroundColor || 'rgb(255,255,255)';
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
    version: '0.5.0',
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
