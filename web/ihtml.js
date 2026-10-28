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
 *   scopeStyles(嵌入时默认 true), storageKey(稳定实例缓存键),
 *   scriptNonce(宿主 CSP script-src nonce，自动附加到所有动态 Item script),
 *   allowExternalAssets(默认 false),
 *   captureHostErrors(嵌入时默认 false),
 *   onReady(cfg), onAuthError(), onConnection(state)
 * 实例 API 额外提供: destroy(), ready(Promise), fmt/util/chart/poll/listen/panel。 */
/* mountChat(el, options) uses the same chat renderer without workspace boot.
 * Options: base/params/theme/locale/strings/storageKey/auth/connectionAuth,
 * Optional history: {list({cursor,signal})->{items,cursor?}, select({id,signal}),
 * remove?({id,signal})->boolean, entry?:false, onOpenChange?(opened)}.
 * Host owns storage, resume binding, authorization and native deletion confirmation.
 * chat.history.open/close/refresh and .opened control the same optional renderer.
 * onReady/onAuthError, initialMessages: [{role, content, contentFormat?}],
 * contextProvider({context, user, signal}) -> partial ChatClientContext | Promise,
 * onChatEvent(event) -> void (detached snapshot, confirmation secrets omitted).
 * beforeConfirm({toolCall, signal}) -> true | Promise<true> adds a host-owned
 * UI check (e.g. retyping an amount). Only literal true proceeds. The detached
 * toolCall omits secrets; the hook cannot rewrite the executable card. Backend
 * authorization and one-time consumption remain required. Timeout: 2 minutes.
 * reviewFieldLabel(path) -> string | null translates host-specific fields;
 * null omits a technical metadata field from the business preview.
 * verificationLinks(toolCall) -> [{label, url}] supplies same-origin records
 * to check when a write has no authoritative execution result.
 * API: ready(), refreshAuth(), refreshConfig(), destroy(), root/user/config,
 * chat.send(text?) -> Promise<boolean>; chat.draft(text) sets input without
 * sending; chat.open()/close()/cancel()/clear(),
 * theme.get()/set(patch, {persist?})/onChange(callback) shares the workspace
 * theme contract; host theme options keep updates local to the instance.
 * Signed tool_call.presentation may supply confirm_text/cancel_text/reason_label;
 * labels are bounded, redacted plain text with localized defaults.
 * chat.getMessages() -> [{role, content, contentFormat}] (detached, bounded
 * display-only snapshot, not canonical history or model input),
 * chat.enabled/busy/readOnly. clear() rotates csid; initialMessages are display
 * only. Hosts MUST await refreshAuth() when changing identity. Context remains
 * untrusted browser input; auth uses headers, connectionAuth uses short tickets.
 * Include ihtml.css and the bundled ihtml-lib.js + ihtml.js as for mount(). */
(() => {
  'use strict';
  const doc = document;
  const lib = window.__ihtmlLib || { makeT: () => (k) => k, fmt: {}, util: {}, chart: {} };
  let instanceSeq = 0;
  const chatOnlyOption = Symbol('ihtml.mountChat');
  let runtimeSymbolName = 'ihtml.runtime.v1';
  let runtimeSymbol = Symbol.for(runtimeSymbolName);

  // Dynamic Item scripts execute as document scripts rather than inside this
  // closure.  Keep their instance APIs in a shared, reclaimable bridge and
  // mark every script with its owner.  The bridge exposes lookup only; the map
  // itself stays private so destroying an instance can reliably revoke it.
  function createRuntimeBridge() {
    const apis = new Map();
    return Object.freeze({
      register(key, api) { if (key && api) apis.set(String(key), api); },
      unregister(key, api) { if (apis.get(String(key)) === api) apis.delete(String(key)); },
      get(key) { return apis.get(String(key)) || null; },
      current() {
        const script = doc.currentScript;
        return script && script.dataset ? apis.get(script.dataset.ihtmlInstance || '') || null : null;
      },
    });
  }
  function compatibleRuntimeBridge(value) {
    try {
      return !!value &&
        typeof value.register === 'function' &&
        typeof value.unregister === 'function' &&
        typeof value.get === 'function' &&
        typeof value.current === 'function';
    } catch { return false; }
  }
  function readRuntimeBridge(symbol) {
    try { return window[symbol]; } catch { return null; }
  }
  function publishRuntimeBridge(symbol, value) {
    try {
      const descriptor = Object.getOwnPropertyDescriptor(window, symbol);
      if (!descriptor || descriptor.configurable) {
        Object.defineProperty(window, symbol, { value, configurable: true });
      } else if ('value' in descriptor && descriptor.writable) {
        window[symbol] = value;
      } else {
        return false;
      }
      return window[symbol] === value;
    } catch { return false; }
  }
  let runtimeBridge = readRuntimeBridge(runtimeSymbol);
  if (!compatibleRuntimeBridge(runtimeBridge)) {
    runtimeBridge = createRuntimeBridge();
    let published = publishRuntimeBridge(runtimeSymbol, runtimeBridge);
    if (!published) {
      // A host may have reserved the public Symbol with a non-configurable,
      // incompatible value. Use a deterministic fallback slot rather than
      // crashing or later calling missing get/unregister methods.
      for (let suffix = 1; suffix <= 16; suffix++) {
        runtimeSymbolName = 'ihtml.runtime.v1.fallback.' + suffix;
        runtimeSymbol = Symbol.for(runtimeSymbolName);
        const existing = readRuntimeBridge(runtimeSymbol);
        if (compatibleRuntimeBridge(existing)) {
          runtimeBridge = existing;
          published = true;
          break;
        }
        if (publishRuntimeBridge(runtimeSymbol, runtimeBridge)) { published = true; break; }
      }
    }
    if (!published) {
      // All deterministic slots can also be host-reserved. A per-load random
      // Symbol makes collision practically impossible; if window itself is
      // non-extensible, fail here instead of generating scripts that later
      // dereference an unpublished/incompatible bridge.
      runtimeSymbolName = 'ihtml.runtime.v1.fallback.' + Date.now().toString(36) + '.' + Math.random().toString(36).slice(2);
      runtimeSymbol = Symbol.for(runtimeSymbolName);
      published = publishRuntimeBridge(runtimeSymbol, runtimeBridge);
    }
    if (!published) throw new Error('ihtml: unable to publish the runtime bridge');
  }

  function newInstanceKey(seq) {
    let nonce = '';
    try {
      const words = new Uint32Array(2);
      crypto.getRandomValues(words);
      nonce = words[0].toString(36) + words[1].toString(36);
    } catch { nonce = Date.now().toString(36) + Math.random().toString(36).slice(2); }
    return 'i' + seq.toString(36) + '-' + nonce;
  }

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
    const instanceKey = newInstanceKey(instanceID);
    const standalone = !!opts.standalone;
    const chatOnly = opts[chatOnlyOption] === true;
    const compactChatHeader = chatOnly && ['compact', 'none'].includes(opts.chatHeader);
    if (opts.hostTransport != null && (!chatOnly ||
        typeof opts.hostTransport.request !== 'function' || typeof opts.hostTransport.open !== 'function')) {
      throw new TypeError('ihtml: hostTransport requires mountChat and request/open functions');
    }
    const hostTransport = opts.hostTransport || null;
    const T = lib.makeT(opts.locale || navigator.language, opts.strings);
    const templateCore = lib.template;
    const itemLayoutCore = lib.itemLayout;
    const chatTraceCore = lib.chatTrace || null;
    const chatContentCore = lib.chatContent || (() => {
      const formats = new Set(['auto', 'html', 'markdown', 'text']);
      const normalize = (value) => {
        const raw = String(value == null ? '' : value).trim().toLowerCase();
        return !raw ? 'auto' : (formats.has(raw) ? raw : 'text');
      };
      const encoder = typeof TextEncoder === 'function' ? new TextEncoder() : null;
      const linkFilters = (() => {
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
      return {
        normalizeFormat: normalize,
        mergeFormat: (current, incoming, reset) => {
          const base = reset ? 'auto' : normalize(current);
          return base !== 'auto' || incoming == null || String(incoming).trim() === '' ? base : normalize(incoming);
        },
        finalizeFormat: (current, incoming) => {
          const base = normalize(current);
          return incoming == null || String(incoming).trim() === '' ? base : normalize(incoming);
        },
        doneSnapshot: (event) => {
          const present = !!event && Object.prototype.hasOwnProperty.call(event, 'message') && typeof event.message === 'string';
          return { present, message: present ? event.message : '' };
        },
        resolveFormat: (format, value) => {
          const resolved = normalize(format);
          if (resolved !== 'auto') return resolved;
          return /<\s*\/?\s*(?:article|section|div|span|p|h[1-6]|blockquote|ul|ol|li|dl|table|thead|tbody|tr|th|td|pre|code|strong|em|a|details|summary|figure)\b/i.test(String(value || '')) ? 'html' : 'markdown';
        },
        clipUTF8: (value, max) => {
          const source = String(value == null ? '' : value);
          const limit = Math.max(0, Math.floor(Number(max) || 0));
          if (!encoder) {
            const text = source.slice(0, limit);
            return { text, bytes: text.length, truncated: text.length < source.length };
          }
          const encoded = encoder.encode(source);
          if (encoded.length <= limit) return { text: source, bytes: encoded.length, truncated: false };
          let low = 0; let high = Math.min(source.length, limit);
          while (low < high) {
            const middle = Math.ceil((low + high) / 2);
            if (encoder.encode(source.slice(0, middle)).length <= limit) low = middle;
            else high = middle - 1;
          }
          if (low > 0 && /[\uD800-\uDBFF]/.test(source.charAt(low - 1))) low--;
          const text = source.slice(0, low);
          return { text, bytes: encoder.encode(text).length, truncated: true };
        },
        compactLinkLabel: (value, max) => {
          let label = String(value == null ? '' : value);
          try { label = label.normalize('NFKC'); } catch { /* explicit fallback ranges remain active */ }
          label = label.replace(linkFilters.invisible, '').replace(linkFilters.whitespace, ' ').trim();
          const visible = !!label.replace(linkFilters.marks, '').trim();
          const points = Array.from(label);
          const limit = Math.max(2, Math.min(1024, Math.floor(Number(max) || 320)));
          const truncated = points.length > limit;
          if (truncated) label = points.slice(0, limit - 1).join('') + '…';
          return { text: label, visible, truncated };
        },
        markdownToHTML: (value) => '<p>' + String(value || '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/\n/g, '<br>') + '</p>',
      };
    })();
    const chatAgentCore = lib.chatAgent || {
      normalizeModes: () => ['fast'], selectMode: () => 'fast',
      requestSnapshot: (_modes, _preferred, model, modelSelect) => ({ agentMode: 'fast', model: modelSelect ? String(model || '') : '' }),
      cancelToolEvent: (call) => ({ type: 'cancel_tool', tool_call: call && typeof call === 'object' ? call : null }),
      cancelAckMatches: (call, ev) => !!call && !!ev && !!call.confirmation_nonce && ev.confirmation_nonce === call.confirmation_nonce && (!call.id && !ev.tool_id || !!call.id && ev.tool_id === call.id),
      streamRenderDelay: (last, now) => Math.max(0, 80 - Math.max(0, Number(now || 0) - Number(last || 0))),
      initialState: () => ({ mode: 'fast', plan: [], agents: {}, budget: null, manualPaused: false }),
      project: (state) => state, budgetParts: () => ({ modelCalls: 0, maxModelCalls: 0, subtasks: 0, maxSubtasks: 0, toolCalls: 0, maxToolCalls: 0 }),
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
    rootEl.classList.toggle('ihtml-chat-inline', chatOnly);
    rootEl.dataset.ihtmlInstance = instanceKey;
    rootEl.classList.toggle('ihtml-standalone-root', standalone);
    const appEl = el('div', 'ihtml-app');
    if (!chatOnly) rootEl.appendChild(appEl);
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

    // aria-modal only describes modality to assistive technology; it does not
    // make the rest of the application non-interactive. Isolate every root
    // layer and restore its exact previous state so nested dialogs remain safe.
    function isolateModalLayers(layers) {
      const allowed = new Set((Array.isArray(layers) ? layers : [layers]).filter(Boolean));
      const records = [];
      for (const node of Array.from(rootEl.children)) {
        if (allowed.has(node)) continue;
        records.push({
          node,
          inertAttribute: node.hasAttribute('inert'),
          inertProperty: !!node.inert,
          ariaHidden: node.getAttribute('aria-hidden'),
        });
        try { node.inert = true; } catch { node.setAttribute('inert', ''); }
        node.setAttribute('aria-hidden', 'true');
      }
      let restored = false;
      return () => {
        if (restored) return;
        restored = true;
        for (const record of records) {
          if (!record.node.isConnected) continue;
          try { record.node.inert = record.inertProperty; } catch { /* attribute restoration below */ }
          if (record.inertAttribute) record.node.setAttribute('inert', '');
          else record.node.removeAttribute('inert');
          if (record.ariaHidden === null) record.node.removeAttribute('aria-hidden');
          else record.node.setAttribute('aria-hidden', record.ariaHidden);
        }
      };
    }

    /* ---------- 参数与模式 ---------- */
    const baseURL = opts.base ? new URL(String(opts.base), location.href) : new URL('.', location.href);
    if (!baseURL.pathname.endsWith('/')) baseURL.pathname += '/';
    const pageParams = standalone
      ? new URLSearchParams(location.search)
      : new URLSearchParams(opts.params || {});
    pageParams.delete('auth'); // 长期凭据不得从页面 URL 复制到每个 API/连接日志
    function stableHostIdentity() {
      const explicit = String(opts.storageKey || hostEl.getAttribute('data-ihtml-storage-key') || hostEl.id || '').trim();
      if (explicit) return 'named:' + explicit;
      const parts = [];
      for (let node = hostEl; node && node.nodeType === 1 && node !== doc.documentElement; node = node.parentElement) {
        const siblings = node.parentElement ? Array.from(node.parentElement.children) : [];
        parts.push(node.localName + ':' + Math.max(0, siblings.indexOf(node)));
        if (node === doc.body) break;
      }
      return 'path:' + parts.reverse().join('/');
    }
    const storageHost = stableHostIdentity();
    const storageBase = baseURL.origin + baseURL.pathname;
    function storageHash(value) {
      if (lib.runtime && typeof lib.runtime.storageHash === 'function') return lib.runtime.storageHash(value);
      let hash = 0x811c9dc5;
      for (let i = 0; i < value.length; i++) {
        hash ^= value.charCodeAt(i);
        hash = Math.imul(hash, 0x01000193) >>> 0;
      }
      return hash.toString(36);
    }
    function storageNamespace(user) {
      const identity = String(user || pageParams.get('uid') || pageParams.get('user') || 'anonymous');
      return storageHash([storageBase, storageHost, identity].join('\u001f'));
    }
    function storageKey(kind, user) {
      return 'ihtml.' + kind + '.' + storageNamespace(user == null ? resolvedUser : user);
    }
    const legacyLocalMarker = 'ihtml.storage_migration.v1.local';
    const legacySessionMarker = 'ihtml.storage_migration.v1.session';
    // Every instance may attempt after its identity is resolved; the
    // synchronous per-storage marker makes the first successful config the
    // sole claimant, even if an earlier mount was destroyed or stayed offline.
    let legacyLocalMigrationDone = false;
    let legacySessionMigrationDone = false;
    function migrateLegacyStore(storage, marker, pairs) {
      if (lib.runtime && typeof lib.runtime.migrateLegacyStorage === 'function') {
        return lib.runtime.migrateLegacyStorage(storage, marker, pairs);
      }
      try {
        if (storage.getItem(marker) != null) return false;
        for (const pair of pairs) {
          if (storage.getItem(pair.scoped) == null) {
            const value = storage.getItem(pair.legacy);
            if (value != null) storage.setItem(pair.scoped, value);
          }
        }
        storage.setItem(marker, '1');
        return true;
      } catch { return false; }
    }
    function migrateLegacyStorageOnce() {
      let localClaimed = false;
      if (!legacyLocalMigrationDone) {
        try {
          localClaimed = migrateLegacyStore(localStorage, legacyLocalMarker, [
            { legacy: 'ihtml.theme', scoped: storageKey('theme') },
            { legacy: 'ihtml.model', scoped: storageKey('model') },
            { legacy: 'ihtml.agent_mode', scoped: storageKey('agent_mode') },
          ]);
          legacyLocalMigrationDone = localStorage.getItem(legacyLocalMarker) != null;
        } catch { /* storage disabled */ }
      }
      if (!legacySessionMigrationDone) {
        try {
          migrateLegacyStore(sessionStorage, legacySessionMarker, [
            { legacy: 'ihtml.csid', scoped: storageKey('csid') },
          ]);
          legacySessionMigrationDone = sessionStorage.getItem(legacySessionMarker) != null;
        } catch { /* storage disabled */ }
      }
      return localClaimed;
    }
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
    function boundedChatHook(invoke, signal, timeoutMS = 5000) {
      return new Promise((resolve, reject) => {
        let timer; let settled = false;
        const finish = (error, value) => {
          if (settled) return;
          settled = true;
          clearTimeout(timer);
          signal.removeEventListener('abort', abort);
          if (error) reject(error); else resolve(value);
        };
        const abort = () => finish(staleRequestError());
        signal.addEventListener('abort', abort, { once: true });
        timer = setTimeout(() => { finish(new Error('ihtml: host callback unavailable')); }, timeoutMS);
        Promise.resolve().then(() => {
          if (signal.aborted) throw staleRequestError();
          return invoke();
        }).then((value) => finish(null, value), (error) => finish(error));
      });
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
      // Revoke pending context/connection work before the host changes identity.
      chat.resetForAuth();
      chat.enabled = false;
      chat.syncChatControls();
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
        const res = await (hostTransport ? hostTransport.request(path, init) : fetch(apiURL(path), init));
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
    // A callable client is both the shortest safe GET form and a compatibility
    // surface for generated Item code. Named methods remain available for
    // explicit verbs and existing pages.
    const http = lib.runtime.createHTTPClient(request, (path) => apiURL(path).toString());

    /* ---------- 错误收集 ---------- */
    const registry = new Map(); // id → { item, page, nodes, teardowns }
    const errorLog = [];
    // errorLog 是审计历史；currentItemErrors 只表示当前这一版 Item
    // 自上次 apply 以来的错误，inspect 不得把已修复的旧错误当成现状。
    const currentItemErrors = new Map();
    let errQueue = [];
    let errTimer = 0;
    const errors = {
      list: () => errorLog.slice(),
      report(itemId, message, stack, lifecycle) {
        const entry = {
          item_id: String(itemId || ''),
          message: clip(String(message || 'unknown error'), 2000),
          stack: clip(String(stack || ''), 4000),
          url: standalone ? location.pathname + location.hash : '(embedded)#/' + nav.currentPage,
          time: new Date().toISOString(),
        };
        errorLog.push(entry);
        if (errorLog.length > 100) errorLog.shift();
        const current = entry.item_id ? registry.get(entry.item_id) : null;
        if (current && (lifecycle == null || current.lifecycle === lifecycle)) currentItemErrors.set(entry.item_id, entry.message);
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
        try { localStorage.setItem(storageKey('theme'), JSON.stringify(themeValue)); } catch { /* 忽略 */ }
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
      // Namespace changes must never inherit fields from the previous user.
      // A missing/invalid cache is the default theme, not the old snapshot.
      themeValue = { ...themeDefaults };
      try {
        const saved = JSON.parse(localStorage.getItem(storageKey('theme')) || 'null');
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
          try { localStorage.setItem(storageKey('theme'), JSON.stringify(themeValue)); } catch { /* 忽略 */ }
        }
      } catch { /* 未配置或离线 */ }
    }
    const onDarkMedia = () => { if (themeValue.mode !== 'dark' && themeValue.mode !== 'light') themeApply(); };
    darkMedia.addEventListener('change', onDarkMedia);
    addDisposer(() => darkMedia.removeEventListener('change', onDarkMedia));

    /* ---------- KV ---------- */
    const KV_MISSING_REVISION = 'missing';
    const validKVRevision = (value) => value === KV_MISSING_REVISION || /^sha256:[0-9a-f]{64}$/.test(String(value || ''));
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
      async getVersioned(key, def = null, requestOpts) {
        try {
          const out = await http.get('api/kv/' + encodeURIComponent(key), requestOpts);
          const revision = String(out && out.revision || '');
          if (!validKVRevision(revision)) throw new Error('ihtml: server did not return a valid KV revision');
          return {
            value: out && Object.prototype.hasOwnProperty.call(out, 'value') ? out.value : def,
            revision,
            exists: true,
          };
        } catch (e) {
          if (e.status === 404) return { value: def, revision: KV_MISSING_REVISION, exists: false };
          throw e;
        }
      },
      set: (key, value) => http.put('api/kv/' + encodeURIComponent(key), { value }),
      async compareAndSet(key, value, expectedRevision, requestOpts) {
        if (!validKVRevision(expectedRevision)) throw new Error('ihtml: invalid expected KV revision');
        const out = await http.put('api/kv/' + encodeURIComponent(key), { value, expected_revision: expectedRevision }, requestOpts);
        const revision = String(out && out.revision || '');
        if (!validKVRevision(revision) || revision === KV_MISSING_REVISION) throw new Error('ihtml: server did not return the written KV revision');
        return { value, revision, exists: true };
      },
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
        const dialogID = 'ihtml-dialog-' + instanceID + '-' + (++dialogSeq);
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
        let restoreIsolation = null;
        const close = (restoreFocus = true) => {
          if (closed) return;
          closed = true;
          modalClosers.delete(close);
          if (restoreIsolation) { restoreIsolation(); restoreIsolation = null; }
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
        restoreIsolation = isolateModalLayers(mask);
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
        sidebar.id = 'ihtml-sidebar-' + instanceID + '-' + (++dialogSeq);
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
        this.bindGestures();
        queueMicrotask(() => { if (!destroyed) { grid.apply(); if (typeof itemEditor !== 'undefined') itemEditor.apply(); } });
      },
      // 移动端抽屉滑动手势: 左缘右滑打开, 抽屉开启时左滑关闭。
      // 只绑一次(实例级 disposer 回收), shell reset/ensure 循环不叠加监听。
      gesturesBound: false,
      bindGestures() {
        if (this.gesturesBound) return;
        this.gesturesBound = true;
        let track = null;
        on(rootEl, 'touchstart', (ev) => {
          track = null;
          if (!this.active || env.mode !== 'mobile' || ev.touches.length !== 1) return;
          if (!rootEl.classList.contains('ihtml-has-sidebar')) return;
          const t = ev.touches[0];
          const open = appEl.classList.contains('ihtml-sidebar-open');
          if (!open) {
            // 仅容器左缘 24px 内起手才追踪, 不干扰页面内横向滑动组件。
            const rect = rootEl.getBoundingClientRect();
            if (t.clientX - rect.left > 24) return;
          }
          track = { x: t.clientX, y: t.clientY, open };
        }, { passive: true });
        on(rootEl, 'touchmove', (ev) => {
          if (!track || ev.touches.length !== 1) return;
          const t = ev.touches[0];
          const dx = t.clientX - track.x;
          const dy = t.clientY - track.y;
          if (Math.abs(dx) < 40 || Math.abs(dx) < Math.abs(dy) * 1.2) return;
          const { open } = track;
          track = null;
          if (!open && dx > 0) this.sidebar(true);
          else if (open && dx < 0) this.sidebar(false);
        }, { passive: true });
        on(rootEl, 'touchend', () => { track = null; }, { passive: true });
        on(rootEl, 'touchcancel', () => { track = null; }, { passive: true });
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
    let applyGeneration = 0;
    let itemLifecycleSeq = 0;
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
      if (lib.runtime && typeof lib.runtime.scopeCSS === 'function') {
        return lib.runtime.scopeCSS(css, standalone, opts.scopeStyles, instanceKey);
      }
      if (standalone || opts.scopeStyles === false) return css;
      // A stylesheet applies document-wide regardless of which root contains
      // its <style> node.  Scope to this exact root, never every .ihtml-root.
      return '@scope (.ihtml-root[data-ihtml-instance="' + instanceKey + '"]) {\n' + css + '\n}';
    }
    const runtimeAPIExpression = 'window[Symbol.for(' + JSON.stringify(runtimeSymbolName) + ')].get(' + JSON.stringify(instanceKey) + ')';
    const privateRuntimeAPIName = '__ihtmlAPI_' + instanceKey.replace(/\W/g, '_');
    function hasIHTMLBinding(code) {
      if (lib.runtime && typeof lib.runtime.hasIHTMLBinding === 'function') return lib.runtime.hasIHTMLBinding(code);
      // 旧 ihtml-lib 兼容路径：只做保守的冲突判断，永远不改写 Item 源码。
      const source = String(code || '').replace(/(['"`])(?:\\.|(?!\1)[\s\S])*\1|\/\*[\s\S]*?\*\/|\/\/[^\r\n]*/g, ' ');
      return /\b(?:const|let|var)\b[^;]{0,512}\bihtml\b/m.test(source) ||
        /\b(?:function|class)\s+ihtml\b/m.test(source) || /\bimport\b[^;]{0,512}\bihtml\b/m.test(source);
    }
    function classicItemScript(code, itemId) {
      if (lib.runtime && typeof lib.runtime.classicItemScript === 'function') {
        return lib.runtime.classicItemScript(code, runtimeAPIExpression, itemId, privateRuntimeAPIName);
      }
      code = String(code || '');
      return '(function(' + privateRuntimeAPIName + '){try{(function(ihtml){\n{\n' + code +
        '\n}\n})(' + privateRuntimeAPIName + ');}catch(__e){' + privateRuntimeAPIName + '.errors.report(' + JSON.stringify(itemId) +
        ', __e && __e.message || __e, __e && __e.stack);}})(' + runtimeAPIExpression + ');';
    }
    function moduleItemScript(code, itemId, lifecycle) {
      const boundExpression = lib.runtime && typeof lib.runtime.itemFacadeExpression === 'function'
        ? lib.runtime.itemFacadeExpression(runtimeAPIExpression, itemId, lifecycle)
        : '(' + runtimeAPIExpression + ').__itemFacade(' + JSON.stringify(String(itemId || '')) + ',' + JSON.stringify(lifecycle) + ')';
      if (lib.runtime && typeof lib.runtime.moduleItemScript === 'function') {
        return lib.runtime.moduleItemScript(code, boundExpression);
      }
      code = String(code || '');
      return (hasIHTMLBinding(code) ? '' : 'const ihtml = ' + boundExpression + ';\n') + code;
    }
    function reportScriptElementError(itemId, source, ev, lifecycle) {
      const detail = ev && (ev.error || ev.message);
      errors.report(itemId, detail && (detail.message || detail) || 'script execution failed: ' + (source || 'inline'), detail && detail.stack, lifecycle);
    }
    function waitForScript(script, generation, signal) {
      return new Promise((resolve) => {
        let settled = false;
        let timer = 0;
        let watcher = 0;
        const finish = () => {
          if (settled) return;
          settled = true;
          clearTimeout(timer);
          clearInterval(watcher);
          script.removeEventListener('load', finish);
          script.removeEventListener('error', finish);
          if (signal) signal.removeEventListener('abort', finish);
          resolve();
        };
        script.addEventListener('load', finish, { once: true });
        script.addEventListener('error', finish, { once: true });
        timer = setTimeout(finish, 10000);
        watcher = setInterval(() => {
          if (destroyed || generation !== applyGeneration) finish();
        }, 50);
        if (signal && signal.aborted) finish();
        else if (signal) signal.addEventListener('abort', finish, { once: true });
      });
    }
    async function activateScripts(container, itemId, lifecycle, generation, signal) {
      for (const old of Array.from(container.querySelectorAll('script'))) {
        if (destroyed || generation !== applyGeneration || (signal && signal.aborted)) throw staleApplyError();
        if (old.src && opts.allowExternalAssets !== true) {
          errors.report(itemId, 'external script blocked by default: ' + old.src);
          old.remove();
          continue;
        }
        const script = doc.createElement('script');
        for (const attr of old.attributes) script.setAttribute(attr.name, attr.value);
        // The trusted host nonce is authoritative. Apply it after copying Item
        // attributes so stale or model-authored nonce attributes cannot replace
        // the current response nonce and make an otherwise valid Item fail CSP.
        if (lib.runtime && typeof lib.runtime.applyScriptNonce === 'function') lib.runtime.applyScriptNonce(script, opts.scriptNonce);
        else if (opts.scriptNonce) script.nonce = String(opts.scriptNonce).trim();
        script.dataset.ihtmlInstance = instanceKey;
        script.dataset.ihtmlItem = itemId;
        const module = String(script.type || '').trim().toLowerCase() === 'module';
        if (!old.src) {
          script.text = module ? moduleItemScript(old.text || '', itemId, lifecycle) : classicItemScript(old.text || '', itemId);
        }
        script.addEventListener('error', (ev) => reportScriptElementError(itemId, old.src || 'inline', ev, lifecycle), { once: true });
        const wait = old.src || module ? waitForScript(script, generation, signal) : null;
        if (destroyed || generation !== applyGeneration || (signal && signal.aborted)) throw staleApplyError();
        runningItem = itemId;
        try { old.replaceWith(script); } finally { runningItem = ''; }
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
      // teardown 属于旧版 Item；它在清理中报错也只进历史日志，
      // 不能污染即将应用的新版当前状态。
      currentItemErrors.delete(id);
      const rec = { item, page: item.page || '', nodes: [], teardowns: [], lifecycle: ++itemLifecycleSeq };
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
          await activateScripts(container, id, rec.lifecycle, generation, signal);
          if (destroyed || generation !== applyGeneration || (signal && signal.aborted)) {
            if (registry.get(id) === rec) removeItem(id);
            throw staleApplyError();
          }
        } else if (item.type === 'js') {
          const script = el('script');
          if (lib.runtime && typeof lib.runtime.applyScriptNonce === 'function') lib.runtime.applyScriptNonce(script, opts.scriptNonce);
          else if (opts.scriptNonce) script.nonce = String(opts.scriptNonce).trim();
          script.dataset.ihtmlId = id;
          script.dataset.ihtmlInstance = instanceKey;
          script.dataset.ihtmlItem = id;
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
            script.text = moduleItemScript(code, id, rec.lifecycle);
          } else {
            script.text = classicItemScript(code, id);
          }
          script.addEventListener('error', (ev) => reportScriptElementError(id, 'js Item', ev, rec.lifecycle), { once: true });
          const wait = isModule ? waitForScript(script, generation, signal) : null;
          rec.nodes.push(script);
          runningItem = id;
          try { doc.body.appendChild(script); } finally { runningItem = ''; }
          if (registry.get(id) !== rec || destroyed || generation !== applyGeneration || (signal && signal.aborted)) throw staleApplyError();
          if (wait) {
            await wait;
            if (registry.get(id) !== rec || destroyed || generation !== applyGeneration || (signal && signal.aborted)) throw staleApplyError();
          }
        } else {
          throw new Error('unknown item type: ' + item.type);
        }
        // Item code can synchronously remove or replace itself while its
        // script is inserted. Never publish an applied event for that dead
        // lifecycle (or let it reconcile the replacement record).
        if (registry.get(id) !== rec || destroyed || generation !== applyGeneration || (signal && signal.aborted)) {
          throw staleApplyError();
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
      currentItemErrors.delete(id);
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

    function lifecycleItemRec(id, lifecycle) {
      if (lib.runtime && typeof lib.runtime.itemLifecycleRecord === 'function') {
        return lib.runtime.itemLifecycleRecord(registry, id, lifecycle);
      }
      const rec = registry.get(String(id || ''));
      return rec && rec.lifecycle === lifecycle ? rec : null;
    }
    function lifecyclePoll(id, lifecycle, fn, ms) {
      const rec = lifecycleItemRec(id, lifecycle);
      if (!rec || typeof fn !== 'function') return () => {};
      let active = true;
      const off = () => {
        if (!active) return;
        active = false;
        clearInterval(timer);
      };
      const invoke = () => {
        if (!lifecycleItemRec(id, lifecycle)) { off(); return; }
        try { fn(); } catch (e) { errors.report(id, 'poll: ' + e, e && e.stack, lifecycle); }
      };
      const timer = setInterval(invoke, Math.max(250, ms || 30000));
      rec.teardowns.push(off);
      invoke();
      return off;
    }
    function lifecycleListen(id, lifecycle, target, ev, fn) {
      const rec = lifecycleItemRec(id, lifecycle);
      if (!rec || !target || typeof target.addEventListener !== 'function' || typeof fn !== 'function') return () => {};
      let active = true;
      const guarded = (...args) => {
        if (!lifecycleItemRec(id, lifecycle)) { off(); return; }
        return fn.apply(target, args);
      };
      const off = () => {
        if (!active) return;
        active = false;
        target.removeEventListener(ev, guarded);
      };
      target.addEventListener(ev, guarded);
      rec.teardowns.push(off);
      return off;
    }
    function lifecycleTeardown(id, lifecycle, fn) {
      if (typeof fn !== 'function') return false;
      const rec = lifecycleItemRec(id, lifecycle);
      if (!rec) {
        // A top-level-await module can resume after replacement. Its cleanup
        // belongs to the dead record, never the new same-ID Item.
        try { fn(); } catch (e) { console.warn('[ihtml] stale module teardown:', e); }
        return false;
      }
      let active = true;
      const run = () => {
        if (!active) return;
        active = false;
        fn();
      };
      rec.teardowns.push(run);
      return true;
    }
    function createItemFacade(id, lifecycle) {
      id = String(id || '');
      const itemMethods = Object.create(items);
      const errorMethods = Object.create(errors);
      Object.defineProperty(itemMethods, 'onTeardown', {
        configurable: true,
        enumerable: true,
        value: (idOrFn, maybeFn) => lifecycleTeardown(id, lifecycle, typeof idOrFn === 'function' ? idOrFn : maybeFn),
      });
      Object.defineProperties(itemMethods, {
        list: { configurable: true, enumerable: true, value: () => lifecycleItemRec(id, lifecycle) ? items.list() : [] },
        apply: { configurable: true, enumerable: true, value: (...args) => lifecycleItemRec(id, lifecycle) ? items.apply(...args) : Promise.resolve([]) },
        remove: { configurable: true, enumerable: true, value: (...args) => lifecycleItemRec(id, lifecycle) ? items.remove(...args) : false },
        refresh: { configurable: true, enumerable: true, value: (...args) => lifecycleItemRec(id, lifecycle) ? items.refresh(...args) : Promise.resolve([]) },
        save: { configurable: true, enumerable: true, value: (...args) => lifecycleItemRec(id, lifecycle) ? items.save(...args) : Promise.resolve(null) },
        delete: { configurable: true, enumerable: true, value: (...args) => lifecycleItemRec(id, lifecycle) ? items.delete(...args) : Promise.resolve(null) },
      });
      Object.defineProperty(errorMethods, 'report', {
        configurable: true,
        enumerable: true,
        value: (...args) => {
          const explicit = args.length > 1;
          errors.report(id, explicit ? args[1] : args[0], explicit ? args[2] : undefined, lifecycle);
        },
      });
      const facade = Object.create(api);
      Object.defineProperties(facade, {
        poll: { configurable: true, enumerable: true, value: (fn, ms) => lifecyclePoll(id, lifecycle, fn, ms) },
        listen: { configurable: true, enumerable: true, value: (target, ev, fn) => lifecycleListen(id, lifecycle, target, ev, fn) },
        onTeardown: { configurable: true, enumerable: true, value: (fn) => lifecycleTeardown(id, lifecycle, fn) },
        items: { configurable: true, enumerable: true, value: itemMethods },
        errors: { configurable: true, enumerable: true, value: errorMethods },
      });
      return facade;
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
      remoteKnown: false,
      remoteRevision: KV_MISSING_REVISION,
      remoteFingerprint: '',
      remoteSourceFingerprint: 'missing',
      remoteChangePending: false,
      loadError: null,
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
        if (this.page !== (nav.currentPage || '') || !this.remoteKnown) {
          this.restoreBase();
          grid.apply();
          this.updateOverlay();
          return;
        }
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
        if (this.saving) { this.remoteChangePending = true; return false; }
        const seq = ++this.loadSeq;
        const page = nav.currentPage || '';
        const samePage = this.page === page;
        this.loading = true;
        if (this.editing) this.render();
        let remote;
        try {
          remote = await kv.getVersioned(this.key(page), null, requestOpts);
        } catch (err) {
          if (seq !== this.loadSeq || destroyed || page !== (nav.currentPage || '') || guard && !guard() || isStaleError(err) || err && err.name === 'AbortError') return false;
          this.loadError = err;
          if (!samePage) this.remoteKnown = false;
          if (this.editing) {
            ui.toast(T('item_editor_load_failed') + ': ' + String(err && err.message || err), 'error');
            this.render();
          }
          return false;
        } finally {
          if (seq === this.loadSeq) {
            this.loading = false;
            if (this.editing) this.render();
          }
        }
        if (seq !== this.loadSeq || destroyed || guard && !guard() || page !== (nav.currentPage || '')) return false;
        const remoteState = itemLayoutCore.remoteSnapshot(remote.value, page);
        if (remote.exists && !remoteState.valid) {
          const err = new Error('stored Item layout does not match the current page');
          this.loadError = err;
          if (!samePage) this.remoteKnown = false;
          if (this.editing) ui.toast(T('item_editor_load_failed') + ': ' + err.message, 'error');
          return false;
        }
        this.page = page;
        this.remoteKnown = true;
        this.loadError = null;
        if (samePage && this.editing && this.state && this.state.dirty) {
          // resync 会重建 Item 容器；保留本地文档并把样式重新绑定到新节点。
          // revision 按原始 JSON 字节计算；这里进一步区分无害的键重排、同布局
          // 并发保存/响应丢失，以及真正的远端修改。source fingerprint 保留未知
          // 扩展字段，不能仅凭 normalize 后的文档静默覆盖它们。
          const localState = itemLayoutCore.remoteSnapshot(this.state.doc, page);
          const classification = itemLayoutCore.classifyRemoteChange({
            baselineRevision: this.remoteRevision,
            baselineSourceFingerprint: this.remoteSourceFingerprint,
            remoteRevision: remote.revision,
            remoteSourceFingerprint: remoteState.sourceFingerprint,
            localSourceFingerprint: localState.sourceFingerprint,
            remoteExists: remote.exists,
            remoteValid: remoteState.valid,
          });
          this.conflict = classification === 'conflict';
          if (classification === 'equivalent-baseline') {
            this.persisted = remote.exists;
            this.remoteRevision = remote.revision;
            this.remoteFingerprint = remoteState.fingerprint;
            this.remoteSourceFingerprint = remoteState.sourceFingerprint;
          } else if (classification === 'matches-local') {
            this.persisted = true;
            this.remoteRevision = remote.revision;
            this.remoteFingerprint = remoteState.fingerprint;
            this.remoteSourceFingerprint = remoteState.sourceFingerprint;
            this.state = itemLayoutCore.reduce(this.state, { type: 'markSaved', doc: this.state.doc });
          }
          this.syncRecords(); return !this.conflict;
        }
        const selectedId = this.editing && this.state ? this.state.selectedId : '';
        this.persisted = remote.exists;
        this.remoteRevision = remote.revision;
        this.remoteFingerprint = remoteState.fingerprint;
        this.remoteSourceFingerprint = remoteState.sourceFingerprint;
        this.conflict = false;
        this.state = itemLayoutCore.initial(page, this.seed(remoteState.valid ? remoteState.doc : itemLayoutCore.empty(page)));
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
        save.onclick = () => { void (this.conflict ? this.reloadConflict() : this.save()); };
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
        if (!(await this.load()) || this.page !== page || !this.remoteKnown) {
          const message = this.loadError && this.loadError.message || 'layout state is unavailable';
          ui.toast(T('item_editor_load_failed') + ': ' + message, 'error');
          return;
        }
        this.page = page;
        this.ensureEditingState();
        this.previousFocus = doc.activeElement;
        this.originalParent = appEl.parentNode;
        this.originalNext = appEl.nextSibling;
        this.dom.stage.insertBefore(appEl, this.dom.overlay);
        this.dom.shell.hidden = false;
        if (this.restoreIsolation) this.restoreIsolation();
        this.restoreIsolation = isolateModalLayers(this.dom.shell);
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
        if (this.restoreIsolation) { this.restoreIsolation(); this.restoreIsolation = null; }
        this.dom.overlay.hidden = true;
        this.originalParent = null; this.originalNext = null;
        this.apply();
        if (this.previousFocus && this.previousFocus.isConnected && typeof this.previousFocus.focus === 'function') this.previousFocus.focus();
        this.previousFocus = null;
      },
      async requestClose() {
        if (this.interaction) this.cancelInteraction();
        if (this.conflict && (!this.state || !this.state.dirty)) {
          this.close(true);
          await this.load();
          return true;
        }
        if (this.state && this.state.dirty) {
          const leave = await ui.confirm({ title: T('item_editor_unsaved'), text: T('item_editor_exit_hint'), okText: T('item_editor_discard') });
          if (!leave) return false;
          this.close(true);
          await this.load();
          return true;
        }
        this.close(false); return true;
      },
      async finish() {
        if (this.conflict) { ui.toast(T('item_editor_conflict'), 'warn'); return; }
        if (this.state && this.state.dirty && !(await this.save())) return;
        this.close(false);
      },
      async reloadConflict() {
        if (!this.conflict || this.loading || this.saving || this.deleting) return false;
        if (this.state && this.state.dirty) {
          const discard = await ui.confirm({ title: T('item_editor_conflict'), text: T('item_editor_exit_hint'), okText: T('item_editor_reload') });
          if (!discard) return false;
        }
        if (this.state) {
          const selectedId = this.state.selectedId;
          this.state = itemLayoutCore.reduce(this.state, { type: 'replace', page: this.page, doc: this.state.baseline });
          if (selectedId) this.state = itemLayoutCore.reduce(this.state, { type: 'select', id: selectedId });
        }
        return this.load();
      },
      dispatch(action) {
        if (!this.state || this.loading || this.saving || this.deleting || this.interaction) return;
        const next = itemLayoutCore.reduce(this.state, action);
        if (next === this.state) return;
        this.state = next;
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
        if (!this.state || !this.state.dirty || this.loading || this.saving) return !this.state || !this.state.dirty;
        if (!this.remoteKnown) {
          ui.toast(T('item_editor_load_failed'), 'error');
          return false;
        }
        const page = this.page; const key = this.key(page); const snapshot = itemLayoutCore.normalize(this.state.doc, page);
        this.saving = true; this.render();
        try {
          const saved = await kv.compareAndSet(key, snapshot, this.remoteRevision);
          if (page !== this.page || page !== (nav.currentPage || '')) return false;
          this.persisted = true; this.conflict = false;
          this.remoteKnown = true;
          this.remoteRevision = saved.revision;
          const savedState = itemLayoutCore.remoteSnapshot(snapshot, page);
          this.remoteFingerprint = savedState.fingerprint;
          this.remoteSourceFingerprint = savedState.sourceFingerprint;
          this.state = itemLayoutCore.reduce(this.state, { type: 'markSaved', doc: snapshot });
          // Item SSE may have added/removed content or changed its runtime slot
          // while the CAS request was in flight. Reconcile the acknowledged
          // layout with the live topology so an older request snapshot cannot
          // make those Items disappear from the editor until another resync.
          this.syncRecords();
          ui.toast(T('item_editor_saved'), 'success');
          return true;
        } catch (err) {
          if (page !== this.page || page !== (nav.currentPage || '') || isStaleError(err) || err && err.name === 'AbortError') return false;
          if (err && err.status === 409) {
            const body = err.body && typeof err.body === 'object' ? err.body : {};
            const currentRevision = String(body.current_revision || '');
            const hasCurrent = Object.prototype.hasOwnProperty.call(body, 'current');
            const remoteState = itemLayoutCore.remoteSnapshot(hasCurrent ? body.current : null, page);
            const localState = itemLayoutCore.remoteSnapshot(snapshot, page);
            if (body.exists === true && hasCurrent && validKVRevision(currentRevision) && currentRevision !== KV_MISSING_REVISION &&
                remoteState.valid && remoteState.sourceFingerprint === localState.sourceFingerprint) {
              // Another writer committed the exact document we intended to save.
              // Treat it as convergence instead of forcing the user to discard it.
              this.persisted = true;
              this.conflict = false;
              this.remoteKnown = true;
              this.remoteRevision = currentRevision;
              this.remoteFingerprint = remoteState.fingerprint;
              this.remoteSourceFingerprint = remoteState.sourceFingerprint;
              this.state = itemLayoutCore.reduce(this.state, { type: 'markSaved', doc: snapshot });
              this.syncRecords();
              ui.toast(T('item_editor_saved'), 'success');
              return true;
            }
            this.conflict = true;
            ui.toast(T('item_editor_conflict'), 'warn');
            return false;
          }
          if (!err || !err.status || err.status >= 500) this.remoteChangePending = true;
          ui.toast(T('item_editor_save_failed') + ': ' + String(err && err.message || err), 'error');
          return false;
        } finally {
          this.saving = false; this.render();
          if (this.remoteChangePending) {
            this.remoteChangePending = false;
            await this.load();
          }
        }
      },
      async remoteChanged() {
        if (this.saving) { this.remoteChangePending = true; return; }
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
        if (this.interaction || this.loading || !this.state || !this.state.selectedId || this.deleting) return;
        if (this.conflict) { ui.toast(T('item_editor_conflict'), 'warn'); return; }
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
        this.state = pendingState; this.deleting = true; this.render();
        try {
          // Commit any other layout edits first. DeleteItems then removes the
          // Item and all of its persisted layout references in one server-side
          // state transaction, so a layout conflict can never delete content.
          if (this.state.dirty && !(await this.save())) {
            this.state = itemLayoutCore.reduce(this.state, { type: 'cancelRemove' });
            return;
          }
          await items.delete(id);
          removeItem(id);
          await this.load();
          this.apply(); this.render();
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
        const busy = this.loading || this.saving || this.deleting;
        this.dom.undo.disabled = busy || interacting || !this.state.past.length;
        this.dom.redo.disabled = busy || interacting || !this.state.future.length;
        this.dom.save.textContent = this.conflict ? T('item_editor_reload') : (this.saving ? T('item_editor_saving') : T('item_editor_save'));
        this.dom.save.disabled = busy || interacting || (!this.conflict && !this.state.dirty);
        this.dom.done.disabled = busy || interacting;
        for (const button of Object.values(this.dom.modeButtons)) button.disabled = busy || interacting;
        this.dom.up.disabled = busy || interacting; this.dom.down.disabled = busy || interacting;
        this.dom.remove.disabled = this.conflict || busy || interacting;
        this.dom.moveHandle.disabled = busy;
        this.dom.resizeHandle.disabled = busy;
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
        remove.disabled = this.conflict || this.loading || this.deleting; remove.onclick = () => { void this.deleteSelected(); }; danger.appendChild(remove);
      },
      syncRecords() {
        if (!this.state || this.page !== (nav.currentPage || '')) return;
        const liveDoc = this.seed(this.state.doc);
        this.state = itemLayoutCore.reconcileState(this.state, liveDoc);
        if (!this.state.selectedId) this.state = itemLayoutCore.reduce(this.state, { type: 'select', id: Object.keys(this.state.doc.items)[0] || '' });
        this.apply(); if (this.editing) this.render();
      },
      async reset() {
        if (readOnly || !itemLayoutCore) return false;
        if (this.loading || this.saving || this.deleting || this.interaction) return false;
        if (this.page !== (nav.currentPage || '') || !this.state || !this.remoteKnown) {
          if (!(await this.load())) return false;
        }
        if (this.conflict) { ui.toast(T('item_editor_conflict'), 'warn'); return false; }
        const fresh = this.seed(itemLayoutCore.empty(this.page));
        const selectedId = fresh.items[this.state && this.state.selectedId] ? this.state.selectedId : Object.keys(fresh.items)[0] || '';
        this.state = itemLayoutCore.reduce(this.state || itemLayoutCore.initial(this.page), { type: 'replaceDoc', doc: fresh, selectedId });
        this.apply(); if (this.editing) this.render();
        return this.save();
      },
      destroy() {
        if (this.interaction) this.cancelInteraction();
        if (this.resizeObserver) { this.resizeObserver.disconnect(); this.resizeObserver = null; }
        this.restoreContentInteractions();
        this.close(true); this.clearParents();
        if (this.dom) this.dom.shell.remove();
        this.dom = null; this.state = itemLayoutCore ? itemLayoutCore.initial('') : null;
        this.remoteKnown = false;
        this.remoteRevision = KV_MISSING_REVISION;
        this.remoteFingerprint = '';
        this.remoteSourceFingerprint = 'missing';
        this.remoteChangePending = false;
        this.loadError = null;
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
      interaction: null,
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
        if (this.restoreIsolation) this.restoreIsolation();
        this.restoreIsolation = isolateModalLayers(this.dom.shell);
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
        if (this.interaction) this.cancelInteraction();
        this.dom.shell.hidden = true;
        if (this.restoreIsolation) { this.restoreIsolation(); this.restoreIsolation = null; }
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
        if (this.interaction) this.cancelInteraction();
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
        // Remote updates or toolbar actions may render while a pointer is
        // captured. End the preview first so window listeners never outlive
        // the DOM nodes they target.
        if (this.interaction) this.cancelInteraction();
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
        const item = el('div', 'ihtml-template-tree-item');
        item.dataset.selected = String(entry.id === this.state.selectedId);
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
        item.appendChild(row);
        if (entry.id !== 'root') {
          item.classList.add('has-actions');
          const controls = el('div', 'ihtml-template-tree-actions');
          controls.setAttribute('role', 'group');
          controls.setAttribute('aria-label', T('template_reorder_actions', { title: entry.node.name }));
          const actions = templateCore.reorderActions(this.state.doc, entry.id);
          for (const [key, label, glyph] of [
            ['up', T('template_move_up'), '↑'], ['down', T('template_move_down'), '↓'],
            ['outdent', T('template_outdent'), '←'], ['indent', T('template_indent'), '→'],
          ]) {
            const button = el('button', '', glyph);
            button.type = 'button';
            button.title = label;
            button.setAttribute('aria-label', label + ' · ' + entry.node.name);
            button.disabled = !actions[key];
            button.onclick = (ev) => {
              ev.preventDefault(); ev.stopPropagation();
              if (actions[key]) this.dispatch(actions[key], { focus: entry.id, focusKey: 'tree' });
            };
            controls.appendChild(button);
          }
          item.appendChild(controls);
        }
        return item;
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
          const widthOnly = !!parentLayout && parentLayout.mode === 'grid';
          const resize = el('button', 'ihtml-template-resize' + (widthOnly ? ' width-only' : ''), widthOnly ? '↔' : '↘');
          resize.dataset.templateFocus = 'canvas-resize'; resize.dataset.templateId = id;
          resize.setAttribute('aria-label', T(widthOnly ? 'layout_resize_width' : 'layout_resize', { title: node.name }));
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
      beginInteraction(ev, session) {
        if (this.interaction) this.cancelInteraction();
        const handle = ev.currentTarget;
        if (!handle) return;
        const pointerId = ev.pointerId;
        const eventTarget = handle.ownerDocument && handle.ownerDocument.defaultView || window;
        const start = { x: ev.clientX, y: ev.clientY };
        session.pointerId = pointerId;
        session.handle = handle;
        session.latest = start;
        session.active = false;
        session.raf = 0;
        session.threshold = ev.pointerType === 'touch' ? 8 : 4;
        const movedEnough = (point) => templateCore.pointerMoved(
          start,
          session.axis === 'x' ? { x: point.x, y: start.y } : point,
          session.threshold,
        );
        const activate = () => {
          if (session.active || this.interaction !== session) return;
          session.active = true;
          rootEl.classList.add('ihtml-template-interacting', session.kind === 'move' ? 'ihtml-template-moving' : 'ihtml-template-resizing');
          if (session.onActivate) session.onActivate();
        };
        const tick = () => {
          session.raf = 0;
          if (this.interaction !== session) return;
          if (!session.active) {
            if (!movedEnough(session.latest)) return;
            activate();
          }
          if (session.onFrame) session.onFrame(session.latest);
        };
        const schedule = (moveEv) => {
          if (this.interaction !== session || moveEv.pointerId !== pointerId) return;
          if (moveEv.cancelable) moveEv.preventDefault();
          session.latest = { x: moveEv.clientX, y: moveEv.clientY };
          if (!session.raf) session.raf = requestAnimationFrame(tick);
        };
        const cleanup = () => {
          eventTarget.removeEventListener('pointermove', schedule, true);
          eventTarget.removeEventListener('pointerup', onUp, true);
          eventTarget.removeEventListener('pointercancel', onCancel, true);
          eventTarget.removeEventListener('blur', onBlur, true);
          handle.removeEventListener('lostpointercapture', onLostCapture);
        };
        const finish = (commit, finishEv) => {
          if (this.interaction !== session || finishEv && finishEv.pointerId !== pointerId) return;
          if (commit && finishEv && Number.isFinite(finishEv.clientX) && Number.isFinite(finishEv.clientY)) {
            session.latest = { x: finishEv.clientX, y: finishEv.clientY };
          }
          if (session.raf) { cancelAnimationFrame(session.raf); session.raf = 0; }
          if (commit && !session.active && movedEnough(session.latest)) activate();
          if (commit && session.active && session.onFrame) session.onFrame(session.latest);
          const shouldCommit = !!(commit && session.active);
          this.interaction = null;
          cleanup();
          rootEl.classList.remove('ihtml-template-interacting', 'ihtml-template-moving', 'ihtml-template-resizing');
          if (handle.releasePointerCapture) { try { handle.releasePointerCapture(pointerId); } catch { /* capture already gone */ } }
          if (session.onFinish) session.onFinish(shouldCommit);
        };
        const onUp = (upEv) => finish(true, upEv);
        const onCancel = (cancelEv) => finish(false, cancelEv);
        const onBlur = () => finish(false);
        const onLostCapture = (lostEv) => { if (lostEv.pointerId === pointerId) finish(false, lostEv); };
        session.finish = finish;
        this.interaction = session;
        if (handle.setPointerCapture) { try { handle.setPointerCapture(pointerId); } catch { /* window listeners remain the fallback */ } }
        eventTarget.addEventListener('pointermove', schedule, { capture: true, passive: false });
        eventTarget.addEventListener('pointerup', onUp, true);
        eventTarget.addEventListener('pointercancel', onCancel, true);
        eventTarget.addEventListener('blur', onBlur, true);
        handle.addEventListener('lostpointercapture', onLostCapture);
      },
      cancelInteraction() {
        if (this.interaction && this.interaction.finish) this.interaction.finish(false);
      },
      gridGeometry(parent, parentLayout) {
        const styles = parent && parent.ownerDocument && parent.ownerDocument.defaultView
          ? parent.ownerDocument.defaultView.getComputedStyle(parent)
          : null;
        const number = (value, fallback) => Number.isFinite(Number.parseFloat(value)) ? Number.parseFloat(value) : fallback;
        return {
          clientWidth: parent ? parent.clientWidth : 0,
          paddingLeft: number(styles && styles.paddingLeft, Number(parentLayout && parentLayout.padding) || 0),
          paddingRight: number(styles && styles.paddingRight, Number(parentLayout && parentLayout.padding) || 0),
          gap: number(styles && (styles.columnGap || styles.gap), Number(parentLayout && parentLayout.gap) || 0),
          columns: Math.max(1, Number(parentLayout && parentLayout.columns) || 12),
        };
      },
      beginMove(ev, id, box, breakpoint) {
        if (ev.button !== 0 || ev.isPrimary === false || !this.state || !this.state.doc.nodes[id]) return;
        ev.preventDefault(); ev.stopPropagation();
        const frame = templateCore.resolveFrame(this.state.doc.nodes[id], breakpoint);
        const originalStyle = box.getAttribute('style');
        const start = { x: ev.clientX, y: ev.clientY };
        let next = { x: frame.x || 0, y: frame.y || 0 };
        let changed = false;
        const restore = () => {
          if (originalStyle == null) box.removeAttribute('style'); else box.setAttribute('style', originalStyle);
          box.classList.remove('pointer-preview');
        };
        this.beginInteraction(ev, {
          kind: 'move',
          onActivate: () => box.classList.add('pointer-preview'),
          onFrame: (point) => {
            next = templateCore.moveFrame(frame, { x: point.x - start.x, y: point.y - start.y }, 8);
            changed = next.x !== (frame.x || 0) || next.y !== (frame.y || 0);
            box.style.left = next.x + 'px'; box.style.top = next.y + 'px';
          },
          onFinish: (commit) => {
            restore();
            if (commit && changed) this.dispatch({ type: 'patchFrame', id, breakpoint, patch: next });
          },
        });
      },
      beginResize(ev, id, box, breakpoint, parentLayout) {
        if (ev.button !== 0 || ev.isPrimary === false || !parentLayout || !box.parentElement || !this.state || !this.state.doc.nodes[id]) return;
        ev.preventDefault(); ev.stopPropagation();
        const frame = templateCore.resolveFrame(this.state.doc.nodes[id], breakpoint);
        const rect = box.getBoundingClientRect();
        const start = { x: ev.clientX, y: ev.clientY };
        const originalStyle = box.getAttribute('style');
        const mode = parentLayout.mode;
        const current = mode === 'grid'
          ? { span: Math.min(parentLayout.columns || 12, frame.span || parentLayout.columns || 12) }
          : { w: frame.w || Math.round(rect.width), h: frame.h || Math.round(rect.height) };
        const geometry = this.gridGeometry(box.parentElement, parentLayout);
        let next = current;
        let changed = false;
        const restore = () => {
          if (originalStyle == null) box.removeAttribute('style'); else box.setAttribute('style', originalStyle);
          box.classList.remove('pointer-preview');
        };
        this.beginInteraction(ev, {
          kind: 'resize', axis: mode === 'grid' ? 'x' : '',
          onActivate: () => box.classList.add('pointer-preview'),
          onFrame: (point) => {
            next = templateCore.resizeFrame(mode, rect, { x: point.x - start.x, y: point.y - start.y }, geometry, 8);
            changed = Object.keys(next).some((key) => next[key] !== current[key]);
            if (mode === 'grid') box.style.gridColumn = 'span ' + next.span;
            else {
              box.style.width = mode === 'free' ? next.w + 'px' : 'min(100%, ' + next.w + 'px)';
              box.style.minHeight = next.h + 'px';
            }
          },
          onFinish: (commit) => {
            restore();
            if (commit && changed) this.dispatch({ type: 'patchFrame', id, breakpoint, patch: next });
          },
        });
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
        // 菜单是该 mount 的第二个根；与主根共用实例标识，
        // 让精确 @scope 既不泄漏到别的 mount，也不会丢掉本实例菜单样式。
        menuRoot.dataset.ihtmlInstance = instanceKey;
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
        if (this.restoreIsolation) this.restoreIsolation();
        this.restoreIsolation = isolateModalLayers([this.dom.mask, this.dom.box]);
        this.switch(tab || 'theme');
        const selected = this.dom.tabs.find((b) => b.getAttribute('aria-selected') === 'true');
        if (selected) selected.focus();
      },
      close() {
        if (!this.dom) return;
        this.dom.mask.style.display = 'none';
        this.dom.box.style.display = 'none';
        if (this.restoreIsolation) { this.restoreIsolation(); this.restoreIsolation = null; }
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
        const panelID = 'ihtml-panel-' + instanceID + '-' + (++dialogSeq);
        const mask = el('div', 'ihtml-chat-mask');
        mask.onclick = () => this.close();
        const box = el('div', 'ihtml-panel');
        box.setAttribute('role', 'dialog');
        box.setAttribute('aria-modal', 'true');
        box.setAttribute('aria-labelledby', panelID + '-title');
        box.addEventListener('keydown', (ev) => handleDialogKey(ev, box, () => this.close()));
        const head = el('div', 'head');
        const heading = el('h2', 'title', T('panel_title'));
        heading.id = panelID + '-title';
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
          b.id = panelID + '-tab-' + key;
          b.setAttribute('role', 'tab');
          b.setAttribute('aria-controls', panelID + '-body');
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
        body.id = panelID + '-body';
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
          b.onclick = () => {
            // A fixed host palette may describe only the initial appearance.
            // An explicit panel mode choice restores the SDK's adaptive palette;
            // programmatic theme.set still preserves any supplied overrides.
            theme.set({ mode, bg: '', surface: '', text: '', muted: '', border: '' });
            this.renderThemeAgain(body);
          };
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

    /* ---------- 助手回答画布：不可信 HTML 的 DOM 重建边界 ---------- */
    const MAX_ASSISTANT_BYTES = chatContentCore.maxRenderBytes;
    const MAX_ASSISTANT_NODES = 5000;
    const MAX_ASSISTANT_DEPTH = 64;
    const ASSISTANT_TAGS = new Set([
      'P', 'BR', 'WBR', 'HR', 'STRONG', 'EM', 'B', 'I', 'U', 'S', 'DEL', 'INS', 'CODE', 'PRE',
      'BLOCKQUOTE', 'UL', 'OL', 'LI', 'DL', 'DT', 'DD', 'TABLE', 'CAPTION', 'COLGROUP', 'COL',
      'THEAD', 'TBODY', 'TFOOT', 'TR', 'TH', 'TD', 'H1', 'H2', 'H3', 'H4', 'H5', 'H6',
      'A', 'SPAN', 'DIV', 'SMALL', 'SUB', 'SUP', 'ARTICLE', 'SECTION', 'HEADER', 'FOOTER', 'ASIDE',
      'FIGURE', 'FIGCAPTION', 'DETAILS', 'SUMMARY', 'TIME', 'MARK', 'KBD', 'SAMP', 'VAR', 'CITE',
      'ABBR', 'Q', 'DATA', 'PROGRESS', 'METER', 'RUBY', 'RT', 'RP',
    ]);
    const ASSISTANT_DROP_TAGS = new Set([
      'SCRIPT', 'STYLE', 'IFRAME', 'OBJECT', 'EMBED', 'LINK', 'META', 'FORM', 'FIELDSET', 'LEGEND',
      'INPUT', 'BUTTON', 'TEXTAREA', 'SELECT', 'OPTION', 'OPTGROUP', 'SVG', 'MATH', 'TEMPLATE', 'BASE',
      'IMG', 'PICTURE', 'SOURCE', 'VIDEO', 'AUDIO', 'CANVAS', 'NOSCRIPT', 'XMP', 'PLAINTEXT',
    ]);
    const ASSISTANT_STYLE_PROPERTIES = new Set([
      'color', 'background-color', 'text-align', 'vertical-align', 'font-weight', 'font-style',
      'font-size', 'line-height', 'letter-spacing', 'text-decoration-line', 'text-decoration-color',
      'text-decoration-style', 'white-space', 'overflow-wrap', 'word-break', 'border-collapse',
      'caption-side', 'margin', 'margin-top', 'margin-right', 'margin-bottom', 'margin-left', 'padding',
      'padding-top', 'padding-right', 'padding-bottom', 'padding-left', 'border-width',
      'border-top-width', 'border-right-width', 'border-bottom-width', 'border-left-width',
      'border-style', 'border-top-style', 'border-right-style', 'border-bottom-style', 'border-left-style',
      'border-color', 'border-top-color', 'border-right-color', 'border-bottom-color', 'border-left-color',
      'border-radius', 'border-top-left-radius', 'border-top-right-radius', 'border-bottom-left-radius',
      'border-bottom-right-radius',
    ]);
    function safeAssistantLengthToken(raw, limits, minimums) {
      raw = String(raw || '').trim().toLowerCase();
      if (raw === '0') return !minimums || !Object.values(minimums).some((value) => value > 0);
      const match = /^(\d{1,4}(?:\.\d{1,4})?|\.\d{1,4})(px|em|rem|%|ch|ex)$/.exec(raw);
      if (!match || !Object.prototype.hasOwnProperty.call(limits, match[2])) return false;
      const number = Number(match[1]);
      const minimum = minimums && minimums[match[2]] || 0;
      return Number.isFinite(number) && number >= minimum && number <= limits[match[2]];
    }
    function safeAssistantLengthList(value, limits, minimums, maxParts = 4) {
      const parts = String(value || '').trim().split(/\s+/);
      return parts.length > 0 && parts.length <= maxParts && parts.every((part) => safeAssistantLengthToken(part, limits, minimums));
    }
    function supportsCSS(property, value) {
      try { return !window.CSS || typeof window.CSS.supports !== 'function' || window.CSS.supports(property, value); }
      catch { return false; }
    }
    function safeAssistantStyleValue(property, value) {
      value = String(value || '').trim();
      if (!value || value.length > 160 || /url\s*\(|@import|expression\s*\(|javascript:|data:|behavior\s*:|-moz-binding|var\s*\(|attr\s*\(|[{}<>]/i.test(value)) return false;
      if (!supportsCSS(property, value)) return false;
      if (property === 'color' || property === 'background-color' || property.endsWith('-color')) return value.length <= 120;
      if (property === 'text-align') return /^(?:start|end|left|right|center|justify|match-parent)$/i.test(value);
      if (property === 'vertical-align') return /^(?:baseline|sub|super|text-top|text-bottom|middle|top|bottom)$/i.test(value) ||
        safeAssistantLengthList(value, { px: 24, em: 1.5, rem: 1.5 }, null, 1);
      if (property === 'font-weight') return /^(?:normal|bold|bolder|lighter|[1-9]00|[1-9]\d{0,2}|1000)$/i.test(value);
      if (property === 'font-style') return /^(?:normal|italic|oblique)$/i.test(value);
      if (property === 'text-decoration-line') return /^(?:(?:none|underline|overline|line-through)(?:\s+|$)){1,3}$/i.test(value);
      if (property === 'text-decoration-style') return /^(?:solid|double|dotted|dashed|wavy)$/i.test(value);
      if (property === 'white-space') return /^(?:normal|pre|pre-wrap|pre-line|break-spaces)$/i.test(value);
      if (property === 'overflow-wrap') return /^(?:normal|break-word|anywhere)$/i.test(value);
      if (property === 'word-break') return /^(?:normal|break-all|keep-all|break-word)$/i.test(value);
      if (property === 'border-collapse') return /^(?:collapse|separate)$/i.test(value);
      if (property === 'caption-side') return /^(?:top|bottom)$/i.test(value);
      if (property === 'font-size') return safeAssistantLengthList(value,
        { px: 32, rem: 2 }, { px: 10, rem: 0.7 }, 1);
      if (property === 'line-height') {
        if (value === 'normal') return true;
        if (/^(?:\d+(?:\.\d+)?|\.\d+)$/.test(value)) return Number(value) >= 0.8 && Number(value) <= 3;
        return safeAssistantLengthList(value,
          { px: 48, em: 3, rem: 3, '%': 300 }, { px: 10, em: 0.7, rem: 0.7, '%': 70 }, 1);
      }
      if (property === 'letter-spacing') return value === 'normal' || safeAssistantLengthList(value, { px: 4, em: 0.25, rem: 0.25 }, null, 1);
      if (/^border(?:-(?:top|right|bottom|left))?-style$/.test(property)) return /^(?:(?:none|hidden|dotted|dashed|solid|double|groove|ridge|inset|outset)(?:\s+|$)){1,4}$/i.test(value);
      if (/^border(?:-(?:top|right|bottom|left))?-width$/.test(property)) {
        return /^(?:(?:thin|medium|thick)(?:\s+|$)){1,4}$/i.test(value) ||
          safeAssistantLengthList(value, { px: 8, em: 0.5, rem: 0.5 });
      }
      if (/^border(?:-(?:top-left|top-right|bottom-left|bottom-right))?-radius$/.test(property)) {
        return safeAssistantLengthList(value, { px: 64, em: 4, rem: 4, '%': 50 });
      }
      if (/^(?:margin|padding)(?:-(?:top|right|bottom|left))?$/.test(property)) {
        return safeAssistantLengthList(value, { px: 64, em: 4, rem: 4, '%': 10 });
      }
      return false;
    }
    function copyAssistantStyle(source, target, allowed) {
      if (!allowed) return;
      const raw = String(source.getAttribute('style') || '');
      if (!raw || raw.length > 2000 || /url\s*\(|@import|expression\s*\(|javascript:|data:|behavior\s*:|-moz-binding/i.test(raw)) return;
      const style = source.style;
      for (let index = 0; index < style.length; index++) {
        const property = String(style[index] || '').toLowerCase();
        if (!ASSISTANT_STYLE_PROPERTIES.has(property) || style.getPropertyPriority(property)) continue;
        const value = style.getPropertyValue(property);
        if (safeAssistantStyleValue(property, value)) target.style.setProperty(property, value);
      }
    }
    function safeAssistantURL(raw) {
      raw = String(raw || '').trim();
      if (!raw || raw.length > 2048 || /[\u0000-\u001f\u007f]/.test(raw)) return '';
      try {
        const parsed = new URL(raw, location.href);
        if (!['http:', 'https:', 'mailto:'].includes(parsed.protocol) || parsed.username || parsed.password) return '';
        return parsed.href;
      } catch { return ''; }
    }
    function safeInteger(raw, min, max) {
      if (!/^-?\d+$/.test(String(raw || '').trim())) return '';
      const value = Number(raw);
      return Number.isSafeInteger(value) && value >= min && value <= max ? String(value) : '';
    }
    function safeNumber(raw) {
      const value = Number(String(raw || '').trim());
      return Number.isFinite(value) && Math.abs(value) <= 1000000 ? String(value) : '';
    }
    function copyAssistantAttributes(source, target, tag, allowStyle) {
      const label = String(source.getAttribute('aria-label') || '').trim();
      const title = String(source.getAttribute('title') || '').trim();
      const lang = String(source.getAttribute('lang') || '').trim();
      const dir = String(source.getAttribute('dir') || '').trim().toLowerCase();
      if (label) target.setAttribute('aria-label', clip(label, 240));
      if (title) target.setAttribute('title', clip(title, 240));
      if (/^[A-Za-z]{1,8}(?:-[A-Za-z0-9]{1,8})*$/.test(lang)) target.setAttribute('lang', lang);
      if (['ltr', 'rtl', 'auto'].includes(dir)) target.setAttribute('dir', dir);
      copyAssistantStyle(source, target, allowStyle);

      if (tag === 'A') {
        const href = safeAssistantURL(source.getAttribute('href'));
        if (href) {
          target.setAttribute('href', href);
          target.setAttribute('target', '_blank');
          target.setAttribute('rel', 'noopener noreferrer nofollow');
        }
      }
      if (tag === 'BLOCKQUOTE' || tag === 'Q') {
        const cite = safeAssistantURL(source.getAttribute('cite'));
        if (cite) target.setAttribute('cite', cite);
      }
      if (tag === 'TH') {
        const scope = String(source.getAttribute('scope') || '').toLowerCase();
        if (['row', 'col', 'rowgroup', 'colgroup'].includes(scope)) target.setAttribute('scope', scope);
      }
      if (tag === 'TD' || tag === 'TH') {
        for (const attr of ['colspan', 'rowspan']) {
          const value = safeInteger(source.getAttribute(attr), 1, 100);
          if (value) target.setAttribute(attr, value);
        }
      }
      if (tag === 'COL' || tag === 'COLGROUP') {
        const span = safeInteger(source.getAttribute('span'), 1, 100);
        if (span) target.setAttribute('span', span);
      }
      if (tag === 'OL') {
        const start = safeInteger(source.getAttribute('start'), -100000, 100000);
        if (start) target.setAttribute('start', start);
        if (source.hasAttribute('reversed')) target.setAttribute('reversed', '');
      }
      if (tag === 'LI') {
        const value = safeInteger(source.getAttribute('value'), -100000, 100000);
        if (value) target.setAttribute('value', value);
      }
      if (tag === 'DETAILS' && source.hasAttribute('open')) target.setAttribute('open', '');
      if (tag === 'TIME') {
        const datetime = String(source.getAttribute('datetime') || '').trim();
        if (/^[0-9A-Za-z:+.,\-TZ ]{1,80}$/.test(datetime)) target.setAttribute('datetime', datetime);
      }
      if (tag === 'DATA') {
        const value = String(source.getAttribute('value') || '').trim();
        if (value) target.setAttribute('value', clip(value, 240));
      }
      if (tag === 'PROGRESS' || tag === 'METER') {
        for (const attr of ['value', 'min', 'max', 'low', 'high', 'optimum']) {
          if (!source.hasAttribute(attr)) continue;
          const value = safeNumber(source.getAttribute(attr));
          if (value) target.setAttribute(attr, value);
        }
      }
    }
    function appendAssistantTruncation(fragment) {
      const note = el('p', 'ihtml-answer-truncated', T('answer_truncated'));
      note.setAttribute('role', 'note');
      fragment.appendChild(note);
    }
    function postprocessAssistantFragment(fragment) {
      for (const table of Array.from(fragment.querySelectorAll('table'))) {
        for (const heading of table.querySelectorAll('thead th:not([scope])')) heading.setAttribute('scope', 'col');
        for (const row of table.querySelectorAll('tbody tr, tfoot tr')) {
          const heading = row.querySelector(':scope > th:first-child:not([scope])');
          if (heading) heading.setAttribute('scope', 'row');
        }
        const wrapper = el('div', 'ihtml-answer-table-wrap');
        const caption = table.querySelector(':scope > caption');
        wrapper.tabIndex = 0;
        wrapper.setAttribute('role', 'region');
        wrapper.setAttribute('aria-label', clip(table.getAttribute('aria-label') || caption && caption.textContent.trim() || T('answer_table'), 240));
        table.parentNode.insertBefore(wrapper, table);
        wrapper.appendChild(table);
      }
      for (const pre of fragment.querySelectorAll('pre')) {
        pre.tabIndex = 0;
        if (!pre.hasAttribute('aria-label')) pre.setAttribute('aria-label', T('answer_code'));
      }
      for (const link of fragment.querySelectorAll('a[href][target="_blank"]')) {
        // A navigation target is always a compact inline affordance. Flatten
        // block/line-break descendants so model HTML cannot turn one visible
        // label into a large mostly-empty click region.
        const compact = chatContentCore.compactLinkLabel(link.textContent, 320);
        // Links are deliberately plain text. Keeping arbitrary descendants
        // would let thousands of otherwise-empty inline/list boxes expand the
        // hit area even when every copied style is safe.
        link.replaceChildren(doc.createTextNode(compact.text));
        if (!compact.visible) {
          link.removeAttribute('href');
          link.removeAttribute('target');
          link.removeAttribute('rel');
          link.removeAttribute('aria-label');
          continue;
        }
        const label = link.getAttribute('aria-label') || link.textContent.trim();
        if (label) link.setAttribute('aria-label', clip(label + ' · ' + T('opens_new_window'), 240));
      }
    }
    function sanitizeAssistantHTML(html, forceTruncated) {
      // Parsing inside template.content is inert and cannot start image/iframe
      // fetches. DOMParser's detached HTML document is script-inert but may
      // still fetch subresources before the DROP list gets a chance to run.
      const template = doc.createElement('template');
      template.innerHTML = String(html || '');
      const state = { nodes: 0, truncated: !!forceTruncated };
      function walk(source, target, depth, blockedByInteractiveAncestor) {
        if (depth > MAX_ASSISTANT_DEPTH) { state.truncated = true; return; }
        for (const child of Array.from(source.childNodes)) {
          if (state.nodes >= MAX_ASSISTANT_NODES) { state.truncated = true; return; }
          if (child.nodeType === Node.TEXT_NODE) {
            target.appendChild(doc.createTextNode(child.nodeValue || ''));
            state.nodes++;
            continue;
          }
          if (child.nodeType !== Node.ELEMENT_NODE) continue;
          const tag = child.tagName.toUpperCase();
          if (ASSISTANT_DROP_TAGS.has(tag)) continue;
          if (!ASSISTANT_TAGS.has(tag)) { walk(child, target, depth + 1, blockedByInteractiveAncestor); continue; }
          const node = doc.createElement(tag.toLowerCase());
          const blockStyle = blockedByInteractiveAncestor || tag === 'A' || tag === 'SUMMARY';
          copyAssistantAttributes(child, node, tag, !blockStyle);
          target.appendChild(node);
          state.nodes++;
          walk(child, node, depth + 1, blockStyle);
          if (state.nodes >= MAX_ASSISTANT_NODES) { state.truncated = true; return; }
        }
      }
      const fragment = doc.createDocumentFragment();
      walk(template.content, fragment, 0, false);
      postprocessAssistantFragment(fragment);
      if (state.truncated) appendAssistantTruncation(fragment);
      return fragment;
    }

    /* ---------- AI 对话抽屉 ---------- */
    const TOOL_STATUS_LABEL = {
      running: T('tool_running'), pending: T('tool_pending'), success: T('tool_success'), accepted: T('tool_accepted'),
      error: T('tool_error'), cancelled: T('tool_cancelled'), unknown: T('tool_unknown'),
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
    function renderAssistant(text, contentFormat, answer, truncated) {
      const resolved = chatContentCore.resolveFormat(contentFormat, text);
      if (answer) answer.dataset.contentFormat = resolved;
      if (resolved === 'text') {
        const fragment = doc.createDocumentFragment();
        fragment.appendChild(doc.createTextNode(String(text || '')));
        if (truncated) appendAssistantTruncation(fragment);
        return fragment;
      }
      const html = resolved === 'html' ? String(text || '') : chatContentCore.markdownToHTML(text);
      return sanitizeAssistantHTML(html, truncated);
    }

    function assistantNodePath(root, node) {
      if (node === root) return [];
      const path = [];
      for (let current = node; current && current !== root && path.length <= MAX_ASSISTANT_DEPTH; current = current.parentNode) {
        const parent = current.parentNode;
        if (!parent) return null;
        path.unshift(Array.prototype.indexOf.call(parent.childNodes, current));
      }
      return path.length && root.contains(node) ? path : null;
    }

    function assistantNodeAtPath(root, path) {
      let node = root;
      for (const index of Array.isArray(path) ? path : []) {
        if (!node || !node.childNodes || index < 0 || index >= node.childNodes.length) return null;
        node = node.childNodes[index];
      }
      return node;
    }

    function captureAssistantView(answer) {
      const state = { details: [], scroll: [], focus: null };
      if (!answer) return state;
      for (const node of Array.from(answer.querySelectorAll('details')).slice(0, 64)) {
        state.details.push({ path: assistantNodePath(answer, node), open: node.open });
      }
      for (const node of [answer, ...Array.from(answer.querySelectorAll('*')).slice(0, MAX_ASSISTANT_NODES)]) {
        if (state.scroll.length >= 64) break;
        if (node.scrollTop || node.scrollLeft) state.scroll.push({ path: assistantNodePath(answer, node), top: node.scrollTop, left: node.scrollLeft });
      }
      if (doc.activeElement && answer.contains(doc.activeElement)) state.focus = assistantNodePath(answer, doc.activeElement);
      return state;
    }

    function replaceAssistantPreservingView(answer, fragment) {
      const state = captureAssistantView(answer);
      answer.replaceChildren(fragment);
      for (const detail of state.details) {
        const node = assistantNodeAtPath(answer, detail.path);
        if (node && node.nodeType === Node.ELEMENT_NODE && node.tagName === 'DETAILS') node.open = detail.open;
      }
      for (const scroll of state.scroll) {
        const node = assistantNodeAtPath(answer, scroll.path);
        if (node && typeof node.scrollTo === 'function') node.scrollTo(scroll.left, scroll.top);
        else if (node) { node.scrollLeft = scroll.left; node.scrollTop = scroll.top; }
      }
      const focus = assistantNodeAtPath(answer, state.focus);
      if (focus && typeof focus.focus === 'function') focus.focus({ preventScroll: true });
    }

    function cancelAssistantRender(turn) {
      if (!turn || !turn.renderFrame) return;
      clearTimeout(turn.renderFrame);
      turn.renderFrame = 0;
    }

    // 稳定会话号: 存 sessionStorage, 断线/刷新重连恢复上下文; 新会话=换号
    function chatSessionID(renew) {
      let id = '';
      const key = storageKey('csid');
      try { id = sessionStorage.getItem(key) || ''; } catch { /* 忽略 */ }
      if (!id || renew) {
        id = 'c' + Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
        try { sessionStorage.setItem(key, id); } catch { /* 忽略 */ }
      }
      return id;
    }

    // 仅用户向上阅读才暂停跟随；内容增长、折叠和键盘缩放产生的 scroll 不代表用户意图。
    function pauseScrollOnUserIntent(node, pause) {
      let touchY = 0, upward = 0;
      node.addEventListener('wheel', ev => { if (ev.deltaY < 0 && Math.abs(ev.deltaY) >= Math.abs(ev.deltaX)) pause(); }, { passive: true });
      node.addEventListener('touchstart', ev => { touchY = ev.touches[0]?.clientY || 0; upward = 0; }, { passive: true });
      node.addEventListener('touchmove', ev => { const next = ev.touches[0]?.clientY; if (next == null) return; upward = Math.max(0, upward + next - touchY); if (upward > 4) pause(); touchY = next; }, { passive: true });
      node.addEventListener('keydown', ev => {
        if (!ev.defaultPrevented && ['ArrowUp', 'PageUp', 'Home'].includes(ev.key) && !ev.target.closest('input, textarea, select, [contenteditable="true"]')) pause();
      });
      node.addEventListener('pointerdown', ev => {
        // 桌面滚动条拖动也属于主动阅读；普通文本选择不改变自动跟随状态。
        const rect = node.getBoundingClientRect();
        if (ev.target === node && node.offsetWidth > node.clientWidth && ev.clientX >= rect.left + node.clientWidth) pause();
      });
    }

    // 可选历史 UI 只消费宿主回调；持久化、身份/权限及删除确认始终由宿主拥有。
    function createChatHistory(head, log, foot, source = opts.history, kind = 'history') {
      if (!source) return null;
      const entry = el('button', 'ihtml-chat-history-entry', (source.title || T('history_title')));
      entry.type = 'button'; entry.setAttribute('aria-expanded', 'false'); entry.hidden = source.entry === false;
      head.appendChild(entry);
      const pane = el('section', 'ihtml-chat-history'); pane.hidden = true; pane.setAttribute('aria-label', (source.title || T('history_title')));
      pane.tabIndex = -1;
      const heading = el('div', 'ihtml-chat-history-head');
      const title = el('strong', '', (source.title || T('history_title')));
      const back = el('button', '', T('history_back')); back.type = 'button'; heading.append(title, back);
      const clear = source.clear ? el('button', 'ihtml-chat-history-clear', source.clearLabel || T('notifications_clear')) : null;
      if (clear) { clear.type = 'button'; heading.insertBefore(clear, back); clear.onclick = () => { void act('clear', {id: ''}); }; }
      const status = el('div', 'ihtml-chat-history-status'); status.setAttribute('role', 'status');
      const list = el('div', 'ihtml-chat-history-list');
      const retry = el('button', 'ihtml-chat-history-retry', T('history_retry')); retry.type = 'button'; retry.hidden = true;
      const more = el('button', 'ihtml-chat-history-more', T('history_more')); more.type = 'button'; more.hidden = true;
      pane.appendChild(heading);
      let refreshPushState=()=>{};
      if (source.push) {
        const push = source.push;
 const subscriptionIds=new Set(push.subscriptionIds||[]);
 const subscriptionID=async endpoint=>Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',new TextEncoder().encode(endpoint))),n=>n.toString(16).padStart(2,'0')).join('');
        const button = el('button','ihtml-chat-push-enable',T('notifications_push_enable'));button.type='button';
        const supported = typeof Notification!=='undefined' && 'serviceWorker' in navigator && typeof PushManager!=='undefined' && isSecureContext;
        button.disabled=!supported;
        if (!supported) button.textContent=T('notifications_push_unsupported');
        let registration=null,enabled=false;
        button.onclick=async()=>{
          if (button.disabled) return;button.disabled=true;
          try {
            if (!enabled && await Notification.requestPermission()!=='granted') throw new Error('permission');
            const worker=new URL('ihtml-push-sw.js',baseURL);if(worker.origin!==location.origin)throw new Error('origin');
            registration=registration||await navigator.serviceWorker.register(worker.href,{scope:new URL('./',worker).pathname,updateViaCache:'none'});
            if (!registration.active) await new Promise((resolve,reject)=>{const instance=registration.installing||registration.waiting;if(!instance){reject(new Error('worker'));return;}const timer=setTimeout(()=>reject(new Error('worker timeout')),15000);instance.addEventListener('statechange',()=>{if(instance.state==='activated'){clearTimeout(timer);resolve();}});});
            let subscription=await registration.pushManager.getSubscription();
            if (enabled) {if(subscription){await push.remove(subscription.toJSON());await subscription.unsubscribe();subscriptionIds.delete(await subscriptionID(subscription.endpoint));}enabled=false;}
            else {
              if(!subscription){const encoded=String(push.publicKey||'').replace(/-/g,'+').replace(/_/g,'/');const key=Uint8Array.from(atob(encoded),c=>c.charCodeAt(0));subscription=await registration.pushManager.subscribe({userVisibleOnly:true,applicationServerKey:key});}
              const saved=await push.subscribe(subscription.toJSON());if(saved?.enabled!==true)throw new Error('not saved');subscriptionIds.add(await subscriptionID(subscription.endpoint));enabled=true;
            }
            button.textContent=T(enabled?'notifications_push_disable':'notifications_push_enable');
          }catch{status.textContent=T('notifications_push_error');}finally{button.disabled=false;}
        };
        refreshPushState=async()=>{
          if(!supported||Notification.permission!=='granted')return;
          try{const worker=new URL('ihtml-push-sw.js',baseURL);const found=await navigator.serviceWorker.getRegistration(new URL('./',worker).href);
            if(!found?.active||new URL(found.active.scriptURL).pathname!==worker.pathname)return;
            registration=found;const current=await found.pushManager.getSubscription();
            if(current){const id=await subscriptionID(current.endpoint);enabled=subscriptionIds.has(id);button.textContent=T(enabled?'notifications_push_disable':'notifications_push_enable');}
          }catch{}
        };
        pane.appendChild(button);
      }

      if (source.description) pane.appendChild(el('p', 'ihtml-chat-history-description', source.description));
      pane.append(status, list, retry, more); log.before(pane);
      let opened = false, generation = 0, pending = null, mutating = false, cursor = '', items = [];
      const uncertain = new Set();
      const busy = () => (!chat.modelInfo?.background_execution && chat.active) || chat.sending || !!chat.confirmationBusy || authRefreshing || destroyed;
      const notify = () => { if (typeof source.onOpenChange === 'function') { try { source.onOpenChange(opened); } catch { /* presentation only */ } } };
      const invalidate = () => { generation++; if (pending) pending.abort(); pending = null; mutating = false; };
      const sync = () => {
        entry.disabled = destroyed || authRefreshing;
        back.disabled = mutating;
        if (clear) { clear.hidden = !items.length; clear.disabled = !!pending || busy(); }
        for (const button of list.querySelectorAll('button')) button.disabled = !!pending || busy() || (button.dataset.removeId && uncertain.has(button.dataset.removeId));
        more.disabled = !!pending; retry.disabled = !!pending;
      };
      const render = () => {
        list.replaceChildren();
        for (const item of items) {
          const row = el('div', 'ihtml-chat-history-row');
          const open = el('button', 'ihtml-chat-history-select'); open.type = 'button';
          open.title = item.title; open.appendChild(el('strong', '', item.title));
          if (item.subtitle) open.appendChild(el('small', '', item.subtitle));
          if (item.selected) open.setAttribute('aria-current', 'true');
          open.onclick = () => { void act('select', item); }; row.appendChild(open);
          if (source.remove && item.removable !== false) {
            const label = source.removeLabel || T(kind === 'notifications' ? 'notifications_delete' : 'history_delete');
            const remove = el('button', 'ihtml-chat-history-remove', label); remove.type = 'button';
            remove.dataset.removeId = item.id; remove.setAttribute('aria-label', label + ' · ' + item.title);
            remove.onclick = () => { void act('remove', item); }; row.appendChild(remove);
          }
          if (uncertain.has(item.id)) row.appendChild(el('small', 'ihtml-chat-history-unknown', T('history_unknown')));
          list.appendChild(row);
        }
        more.hidden = !cursor; sync();
      };
      const alive = (id, abort) => !destroyed && opened && id === generation && !abort.signal.aborted && !authRefreshing;
      const failure = (error, id, abort) => {
        if (!alive(id, abort)) return;
        // 不输出回调异常原文，避免 HTTP/认证错误携带宿主敏感信息。
        status.textContent = T('history_error'); retry.hidden = false;
      };
      async function load(append = false) {
        if (!opened || pending || destroyed || authRefreshing) return;
        const abort = new AbortController(), id = ++generation, before = append ? cursor : '';
        pending = abort; status.textContent = T('history_loading'); retry.hidden = true; sync();
        try {
          const page = await boundedChatHook(() => source.list({cursor: before, signal: abort.signal}), abort.signal, 30000);
          if (!alive(id, abort)) return;
          if (!page || !Array.isArray(page.items) || page.items.length > 100 || (page.cursor != null && (typeof page.cursor !== 'string' || page.cursor.length > 2048)) || (page.cursor && page.cursor === before)) throw new TypeError('invalid history page');
          const rows = page.items.map(item => {
            if (!item || typeof item.id !== 'string' || !item.id || item.id.length > 512 || typeof item.title !== 'string' || !item.title || item.title.length > 1024 || (item.subtitle != null && (typeof item.subtitle !== 'string' || item.subtitle.length > 1024))) throw new TypeError('invalid history item');
            return {id: item.id, title: item.title, subtitle: item.subtitle || '', selected: item.selected === true, removable: item.removable !== false};
          });
          const seen = new Set(); items = (append ? items : []).concat(rows).filter(item => { if (seen.has(item.id)) return false; seen.add(item.id); return true; });
          cursor = page.cursor || ''; status.textContent = items.length ? '' : (source.emptyMessage || T('chat_history_empty'));
          if (typeof source.onUnreadChange==='function') source.onUnreadChange(Math.max(0,Number(page.unread)||0));
          render();
        } catch (error) { failure(error, id, abort); }
        finally { if (id === generation) { pending = null; abort.abort(); sync(); } }
      }
      async function act(kind, item) {
        if (pending || busy() || !opened || (kind === 'remove' && uncertain.has(item.id))) return;
        const abort = new AbortController(), id = ++generation;
        pending = abort; mutating = true; status.textContent = T('history_loading'); retry.hidden = true; sync(); chat.syncChatControls();
        try {
          // 删除 hook 必须完成宿主原生确认并返回 true；取消返回 false，绝不默认删除。
          const result = await boundedChatHook(() => source[kind]({id: item.id, signal: abort.signal}), abort.signal, kind !== 'select' ? 120000 : 30000);
          if (!alive(id, abort)) return;
          if (kind !== 'select') {
            if (result !== true && result !== false) throw new TypeError('history removal requires explicit receipt');
            if (result) { items = kind === 'clear' ? [] : items.filter(row => row.id !== item.id); if (kind === 'clear') cursor = ''; render(); }
            status.textContent = items.length ? '' : (source.emptyMessage || T('chat_history_empty'));
          } else { mutating = false; close(false); }
        } catch (error) {
          if (kind === 'remove' && alive(id, abort)) { uncertain.add(item.id); render(); }
          failure(error, id, abort);
        } finally { if (id === generation) { pending = null; mutating = false; abort.abort(); sync(); chat.syncChatControls(); if (source.onUnreadChange && retry.hidden) void load(); } }
      }
      function close(focus = true) {
        if (!opened || mutating) return;
        invalidate(); opened = false; pane.hidden = true; log.hidden = false;
        entry.setAttribute('aria-expanded', 'false'); notify(); chat.syncLatest(); chat.syncChatControls();
        if (focus && entry.isConnected && !entry.hidden) entry.focus({preventScroll: true});
      }
      function reset() { invalidate(); opened = false; items = []; cursor = ''; uncertain.clear(); list.replaceChildren(); pane.hidden = true; log.hidden = false; entry.setAttribute('aria-expanded', 'false'); notify(); }
      entry.onclick = () => opened ? close() : open();
      back.onclick = () => close(); more.onclick = () => { void load(true); }; retry.onclick = () => { void load(); };
      pane.addEventListener('keydown', event => { if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close(); } });
      function open() {
        if (destroyed || authRefreshing) return;
        if (opened) return;
        if (kind==='notifications') chat.history?.close(false); else chat.notifications?.close(false);
        opened = true; pane.hidden = false; log.hidden = true; chat.dom.latestBtn.hidden = true;
        entry.setAttribute('aria-expanded', 'true'); notify(); pane.focus({preventScroll: true}); void refreshPushState(); void load();
      }
      let summaryAbort=null;
      const refreshSummary=async()=>{
        if(opened||summaryAbort||destroyed||authRefreshing||document.visibilityState==='hidden')return;
        summaryAbort=new AbortController();const summaryGeneration=generation;
        try{const page=await boundedChatHook(()=>source.list({cursor:'',signal:summaryAbort.signal}),summaryAbort.signal,30000);if(!destroyed&&!opened&&summaryGeneration===generation&&typeof source.onUnreadChange==='function')source.onUnreadChange(Math.max(0,Number(page?.unread)||0));}catch{}finally{summaryAbort?.abort();summaryAbort=null;}
      };
      const poll = kind==='notifications'?setInterval(refreshSummary,30000):0;
      if(poll)void refreshSummary();
      addDisposer(()=>{if(poll)clearInterval(poll);summaryAbort?.abort();reset();});
      return {open, close, reset, sync, refresh: () => opened?load():refreshSummary(), get opened() { return opened; }, get mutating() { return mutating; }};
    }

    const chat = {
      enabled: false,
      entry: null,
      dom: null,
      log: null,
      ws: null,
      wsPromise: null,
      wsGeneration: 0,
      reconnectTimer: 0,
      reconnectAttempt: 0,
      reconnectWanted: false,
      backgroundSession: '',
      backgroundSeq: 0,
      connectionNotice: null,
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
      composerResizeFrame: 0,
      announcementTimer: 0,
      lastAnnouncement: '',
      announcementQueue: [],
      announcementBusy: false,
      everConnected: false,
      previousFocus: null,
      restoreIsolation: null,
      confirmationBusy: null,

      init(cfg) {
        this.enabled = !!(cfg && cfg.chat);
        if (this.dom && this.dom.readonlyBadge) this.dom.readonlyBadge.hidden = !readOnly;
        if (chatOnly) {
          if (!this.dom) this.build();
          this.dom.panel.style.display = '';
          this.syncChatControls();
          return;
        }
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
        if (!this.enabled || destroyed) return;
        if (chatOnly) {
          this.dom.panel.style.display = '';
          this.dom.input.focus();
          return;
        }
        panel.close();
        if (!this.dom) this.build();
        this.previousFocus = doc.activeElement;
        this.dom.mask.style.display = '';
        this.dom.panel.style.display = '';
        if (this.restoreIsolation) this.restoreIsolation();
        this.restoreIsolation = isolateModalLayers([this.dom.mask, this.dom.panel]);
        rootEl.classList.add('ihtml-chat-open');
        this.dom.input.focus();
        if (!this.modelInfo) void this.loadModels().catch((err) => {
          if (!(err && err.stale)) ui.toast(err.message || T('chat_broken'), 'error');
        });
      },
      close() {
        if (!this.dom) return;
        if (chatOnly) { this.dom.panel.style.display = 'none'; return; }
        this.dom.mask.style.display = 'none';
        this.dom.panel.style.display = 'none';
        rootEl.classList.remove('ihtml-chat-open');
        if (this.restoreIsolation) { this.restoreIsolation(); this.restoreIsolation = null; }
        if (this.previousFocus && this.previousFocus.isConnected && typeof this.previousFocus.focus === 'function') this.previousFocus.focus();
        this.previousFocus = null;
      },

      build() {
        const chatID = 'ihtml-chat-' + instanceID + '-' + (++dialogSeq);
        const mask = el('div', 'ihtml-chat-mask');
        mask.onclick = () => this.close();
        const panelEl = el('div', 'ihtml-chat');
        panelEl.setAttribute('role', chatOnly ? 'region' : 'dialog');
        if (!chatOnly) panelEl.setAttribute('aria-modal', 'true');
        if (compactChatHeader) panelEl.setAttribute('aria-label', T('chat_title'));
        else panelEl.setAttribute('aria-labelledby', chatID + '-title');

        const head = el('div', 'ihtml-chat-head');
        if (compactChatHeader) head.classList.add('ihtml-chat-head-compact');
        // 宿主接管操作栏时不再占用第二行；原有公共 clear/history 接口保持一致。
        if (chatOnly && opts.chatHeader === 'none') head.hidden = true;
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
        if (!compactChatHeader) head.appendChild(title);
        head.append(readonlyBadge, newBtn);
        if (!chatOnly) head.appendChild(closeBtn);

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
        log.tabIndex = 0;
        let lastLogTop = log.scrollTop;
        log.addEventListener('scroll', () => {
          if (log.scrollTop + log.clientHeight >= log.scrollHeight - 48 && (this.follow || log.scrollTop > lastLogTop)) this.setFollowing(true);
          lastLogTop = log.scrollTop;
          this.syncLatest();
        });
        pauseScrollOnUserIntent(log, () => {
          lastLogTop = log.scrollTop;
          if (log.scrollHeight > log.clientHeight + 1) this.setFollowing(false);
        });
        const latestBtn = el('button', 'ihtml-chat-latest', T('chat_latest'));
        latestBtn.type = 'button'; latestBtn.hidden = true;
        latestBtn.onclick = () => { const focused = doc.activeElement === latestBtn; this.scroll(true); if (focused) log.focus({ preventScroll: true }); };

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
        const keyboard = lib.chatComposer.create();
        input.addEventListener('compositionstart', () => keyboard.start());
        input.addEventListener('compositionend', () => keyboard.end());
        input.addEventListener('keyup', (ev) => keyboard.release(ev));
        input.addEventListener('blur', () => keyboard.blur());
        input.addEventListener('keydown', (ev) => {
          if (ev.key === 'Escape') {
            if (keyboard.active(ev)) { ev.stopPropagation(); return; }
            if (chatOnly) return;
            ev.preventDefault(); ev.stopPropagation(); this.close(); return;
          }
          // 桌面与移动端一致；选词确认交给输入法，Shift+Enter 保留换行。
          if (ev.key !== 'Enter' || ev.defaultPrevented || ev.shiftKey || ev.altKey || ev.ctrlKey || ev.metaKey || keyboard.active(ev)) return;
          const shouldSend = keyboard.shouldSend(ev);
          // 确认键的尾部事件和长按都不能掉进 textarea 的默认换行；选词仍交给输入法。
          ev.preventDefault();
          if (shouldSend) this.send();
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

        if (!chatOnly) panelEl.addEventListener('keydown', (ev) => handleDialogKey(ev, panelEl, () => this.close()));
        panelEl.append(head, toolbar, modebar, log, latestBtn, foot, live);
        if (!chatOnly) rootEl.appendChild(mask);
        rootEl.appendChild(panelEl);
        this.dom = { mask, panel: panelEl, log, latestBtn, foot, input, sendBtn, providerBadge, modelInput, dataList, freshBtn, newBtn, toolbar, modebar, modeButtons, modeHint, live, readonlyBadge };
        this.log = log;
        // 网络/前台恢复只重建连接，不重放用户消息或确认帧。
        const recoverConnection = () => {
          if (!this.reconnectWanted || document.visibilityState === 'hidden') return;
          this.reconnectAttempt = 0;
          this.scheduleReconnect(true);
        };
        window.addEventListener('online', recoverConnection);
        document.addEventListener('visibilitychange', recoverConnection);
        addDisposer(() => {window.removeEventListener('online', recoverConnection); document.removeEventListener('visibilitychange', recoverConnection);});
        this.history = createChatHistory(head, log, foot);
        this.notifications = opts.notifications ? createChatHistory(head,log,foot,opts.notifications,'notifications') : null;
        // 观察真正渲染后的消息高度；覆盖延迟 Markdown、图片加载、折叠及宿主视口变化。
        if (typeof ResizeObserver === 'function') {
          const resize = new ResizeObserver(() => {
            // An inline host can shrink independently of the browser viewport.
            // Keep the composer reachable by compacting explanatory chrome.
            if (chatOnly) rootEl.classList.toggle('ihtml-chat-compact', panelEl.clientHeight < 320);
            this.syncLatest(); this.scroll();
          });
          resize.observe(log);
          if (chatOnly) resize.observe(panelEl);
          const children = new MutationObserver(records => {
            for (const record of records) {
              for (const node of record.removedNodes) if (node.nodeType === 1) resize.unobserve(node);
              for (const node of record.addedNodes) if (node.nodeType === 1) resize.observe(node);
            }
            this.scroll();
          });
          children.observe(log, { childList: true });
          addDisposer(() => { children.disconnect(); resize.disconnect(); });
        }

        on(window, 'resize', () => {
          if (this.composerResizeFrame) return;
          this.composerResizeFrame = requestAnimationFrame(() => {
            this.composerResizeFrame = 0;
            if (!destroyed && this.dom) this.resizeComposer();
          });
        });
        this.resizeComposer(); this.syncChatControls();
        this.notice(T('welcome'));
        this.appendMessages(opts.initialMessages);
        if (!opts.initialMessages?.length) this.renderStarters();
      },

      // 示例属于空对话内容，随消息区滚动；发送后移除，不永久占用宿主顶部空间。
      renderStarters() {
        this.starters?.remove();
        if (!opts.starters?.length) return;
        const group = el('div', 'ihtml-chat-starters');
        for (const text of opts.starters) {
          const button = el('button', '', text); button.type = 'button';
          button.onclick = () => { void this.send(text); };
          group.appendChild(button);
        }
        this.starters = group; this.log.appendChild(group);
      },

      appendMessages(messages) {
        for (const message of messages || []) {
          const card = el('article', 'ihtml-msg ' + message.role);
          card._displayMessage = { role: message.role, content: message.content, contentFormat: message.contentFormat };
          card.appendChild(el('div', 'author', message.role === 'user' ? T('author_user') : 'AI'));
          const body = el('div', message.role === 'user' ? 'content' : 'answer');
          if (message.role === 'user') body.textContent = message.content;
          else body.replaceChildren(renderAssistant(message.content, message.contentFormat, body, false));
          card.appendChild(body);
          this.log.appendChild(card);
        }
        this.scroll();
      },

      getMessages() {
        if (!this.log || destroyed || authRefreshing) return [];
        const messages = [];
        let remaining = 512 * 1024;
        // Take the newest display text only; notices and process/tool state are
        // deliberately not replayable. Never derive history from rendered HTML.
        for (const card of Array.from(this.log.children).reverse()) {
          const source = card._turn
            ? { role: 'assistant', content: card._turn.text, contentFormat: card._turn.contentFormat }
            : card._displayMessage;
          if (!source || !source.content || !remaining || messages.length === 100) continue;
          const limited = chatContentCore.clipUTF8(source.content, remaining);
          if (!limited.text) continue;
          remaining -= limited.bytes;
          messages.push({ role: source.role, content: limited.text,
            contentFormat: source.role === 'user' ? 'text' : chatContentCore.resolveFormat(source.contentFormat, source.content) });
        }
        return messages.reverse();
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

      setAvailabilityNote(show, preview = false) {
        if (!this.log) return;
        if (!show) {
          if (this.availabilityNote) this.availabilityNote.remove();
          this.availabilityNote = null;
          return;
        }
        const message = T(preview ? 'ai_preview' : 'ai_unavailable');
        if (!this.availabilityNote || !this.availabilityNote.isConnected) {
          this.availabilityNote = el('div', 'ihtml-sysnote ihtml-ai-unavailable', message);
          this.log.appendChild(this.availabilityNote);
        } else this.availabilityNote.textContent = message;
        this.availabilityNote.classList.toggle('ihtml-ai-preview', preview);
        this.scroll();
      },
      setAgentMode(mode, persist) {
        this.agentMode = chatAgentCore.selectMode(this.agentModes, mode);
        // 仅一种模式时无可选项，隐藏整行和预留空间，但仍使用后端声明的模式发消息。
        const multipleModes = this.agentModes.length > 1;
        if (this.dom) {
          this.dom.modebar.hidden = !multipleModes;
          this.dom.panel.classList.toggle('has-agent-modes', multipleModes);
          for (const [key, button] of Object.entries(this.dom.modeButtons || {})) {
            const selected = key === this.agentMode;
            button.hidden = !this.agentModes.includes(key);
            button.setAttribute('aria-checked', String(selected));
            button.tabIndex = selected ? 0 : -1;
            button.disabled = !this.agentModes.includes(key) || this.active || this.sending;
          }
          const manual = this.agentMode === 'deep' && this.deepApprovalResume === 'manual_tool_only';
          this.dom.modeHint.textContent = this.deepApprovalResume === 'managed_turn' ? T('agent_managed_confirm') : manual ? T('agent_deep_manual') : T(this.agentMode === 'deep' ? 'agent_deep_hint' : 'agent_fast_hint');
        }
        if (persist) {
          try { localStorage.setItem(storageKey('agent_mode'), this.agentMode); } catch { /* 忽略 */ }
        }
      },
      resizeComposer() {
        if (!this.dom || !this.dom.input) return;
        const input = this.dom.input; input.style.height = 'auto';
        const style = getComputedStyle(input);
        const minHeight = Number.parseFloat(style.minHeight) || 44;
        const maxHeight = Number.parseFloat(style.maxHeight) || 140;
        const border = (Number.parseFloat(style.borderTopWidth) || 0) + (Number.parseFloat(style.borderBottomWidth) || 0);
        // A wrapping placeholder is a hint, not user content. Empty inputs stay one line.
        const scrollHeight = input.value ? input.scrollHeight + (style.boxSizing === 'border-box' ? border : 0) : minHeight;
        input.style.height = Math.max(minHeight, Math.min(scrollHeight, maxHeight)) + 'px';
        input.style.overflowY = scrollHeight > maxHeight + 1 ? 'auto' : 'hidden';
        if (this.follow) this.scroll();
      },
      syncChatControls() {
        if (!this.dom) return;
        const busy = this.active || this.sending;
        this.history?.sync();
        const unavailable = destroyed || !this.enabled || authRefreshing;
        this.dom.input.readOnly = this.sending || unavailable || !!this.history?.mutating;
        this.dom.input.setAttribute('aria-readonly', String(this.dom.input.readOnly));
        this.dom.foot.setAttribute('aria-busy', String(busy));
        this.dom.sendBtn.textContent = busy ? T('stop') : T('send');
        this.dom.sendBtn.classList.toggle('stop', busy);
        this.dom.sendBtn.disabled = unavailable || !!this.history?.mutating || (!busy && !this.dom.input.value.trim());
        this.dom.newBtn.disabled = authRefreshing || !!this.history?.mutating;
        this.dom.modelInput.disabled = busy;
        this.dom.freshBtn.disabled = busy || !!this.modelPromise;
        for (const [mode, button] of Object.entries(this.dom.modeButtons || {})) {
          button.disabled = busy || !this.agentModes.includes(mode);
        }
        if (this.log) {
          const confirmationBusy = busy || !!this.confirmationBusy;
          for (const message of Array.from(this.log.children)) {
            const owner = message._turn;
            if (!owner) continue;
            for (const rec of owner.tools.values()) {
              if (rec.expired && rec.status === 'cancelled') {
                const refresh = rec.body.querySelector('.t-refresh');
                if (refresh) refresh.disabled = unavailable || readOnly || confirmationBusy;
              }
              if (rec.status !== 'pending') continue;
              const disabled = unavailable || readOnly || confirmationBusy || !!rec.cancelPending || !!rec.confirmPending;
              rec.card.classList.toggle('confirmation-busy', disabled);
              rec.body.setAttribute('aria-busy', String(!!rec.cancelPending || !!rec.confirmPending));
              rec.body.querySelectorAll('.t-actions button').forEach((button) => { button.disabled = disabled || (button.classList.contains('ok') && !!rec.call.review?.blocking_reason); });
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
        if (info?.background_execution) queueMicrotask(()=>{if(!destroyed&&this.enabled&&!authRefreshing)void this.ensureWS().catch(()=>{});});
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
            try { saved = localStorage.getItem(storageKey('model')) || ''; } catch { /* 忽略 */ }
          }
          const preferred = saved && (!models.length || models.includes(saved))
            ? saved
            : (info.current_model || models[0] || saved);
          modelInput.value = preferred || '';
          this.saveModel(modelInput.value.trim());
          let savedMode = '';
          try { savedMode = localStorage.getItem(storageKey('agent_mode')) || ''; } catch { /* 忽略 */ }
          this.setAgentMode(chatAgentCore.selectMode(this.agentModes, savedMode || this.agentMode), true);
          const previewOnly = info.preview_only === true;
          this.setAvailabilityNote(previewOnly || info.model_ready === false, previewOnly);
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
        try { localStorage.setItem(storageKey('model'), this.model); } catch { /* 忽略 */ }
      },

      setFollowing(value) {
        this.follow = value;
        if (this.log) this.log.style.overflowAnchor = 'none';
        this.syncLatest();
      },
      syncLatest() {
        if (!this.log || !this.dom?.latestBtn) return;
        const remaining = Math.max(0, this.log.scrollHeight - this.log.clientHeight - this.log.scrollTop);
        // Intent pauses streaming immediately, but only a real distance from
        // the end warrants a button. Hysteresis avoids flicker near the edge.
        const button = this.dom.latestBtn;
        button.hidden = this.follow || remaining <= (button.hidden ? 96 : 48);
      },
      scroll(force) {
        if (force) this.setFollowing(true);
        if (!this.follow || this.scrollFrame) return;
        const settle = (remaining) => {
          this.scrollFrame = 0;
          if (destroyed || !this.follow || !this.log) return;
          // Auto-follow must not inherit a host's smooth scrolling or compete
          // with browser anchoring after terminal Markdown/trace compaction.
          this.log.style.overflowAnchor = 'none';
          this.log.scrollTo({top: this.log.scrollHeight, behavior: 'instant'});
          this.syncLatest();
          if (remaining) this.scrollFrame = requestAnimationFrame(() => settle(remaining - 1));
        };
        this.scrollFrame = requestAnimationFrame(() => settle(1));
      },

      connectionNote(text) {
        if (!this.dom || destroyed) return;
        if (!this.connectionNotice?.isConnected) {
          this.connectionNotice = el('div', 'ihtml-sysnote'); this.log.appendChild(this.connectionNotice);
        }
        if (this.connectionNotice.textContent === text) return;
        this.connectionNotice.textContent = text; this.announce(text); this.scroll();
      },
      scheduleReconnect(immediate = false) {
        if (destroyed || !this.enabled || authRefreshing || !this.reconnectWanted || navigator.onLine === false || document.visibilityState === 'hidden') return;
        if (this.ws?.readyState === WebSocket.OPEN || this.wsPromise) return;
        if (this.reconnectTimer) {if (!immediate) return; clearTimeout(this.reconnectTimer);}
        const delay = immediate ? 0 : Math.min(30000, 1000 * 2 ** Math.min(this.reconnectAttempt++, 5));
        this.reconnectTimer = setTimeout(() => {
          this.reconnectTimer = 0;
          if (destroyed || !this.reconnectWanted || navigator.onLine === false || document.visibilityState === 'hidden') return;
          this.ensureWS(true).catch(() => {});
        }, delay);
      },
      ensureWS(automatic = false) {
        if (!automatic) {this.reconnectWanted = true; this.reconnectAttempt = 0;}
        if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
        this.reconnectTimer = 0;
        if (destroyed) return Promise.reject(new Error('ihtml: destroyed'));
        if (this.ws && this.ws.readyState === WebSocket.OPEN) return Promise.resolve(this.ws);
        if (this.wsPromise) return this.wsPromise;
        const generation = ++this.wsGeneration;
        const connectionAbort = new AbortController();
        this.connectionAbort = connectionAbort;
        const promise = (async () => {
          let ticket;
          try { ticket = await boundedChatHook(evalConnectionAuth, connectionAbort.signal); }
          finally { if (this.connectionAbort === connectionAbort) this.connectionAbort = null; }
          if (destroyed || generation !== this.wsGeneration) throw new Error(T('chat_disconnected'));
          const wsURL = apiURL('api/chat/ws', true, ticket);
          wsURL.searchParams.set('csid', chatSessionID());
          wsURL.protocol = wsURL.protocol === 'https:' ? 'wss:' : 'ws:';
          const ws = hostTransport ? hostTransport.open() : new WebSocket(wsURL);
          this.ws = ws;
          let settled = false;
          let openedAt = 0;
          let handshakeTimer;
          return new Promise((resolve, reject) => {
            const fail = (err) => {
              if (settled) return;
              settled = true;
              clearTimeout(handshakeTimer);
              reject(err);
            };
            handshakeTimer = setTimeout(() => {fail(new Error(T('chat_broken'))); try {ws.close();} catch {}}, 10000);
            ws.onopen = () => {
              if (destroyed || generation !== this.wsGeneration || this.ws !== ws) {
                try { ws.close(); } catch { /* 忽略 */ }
                fail(new Error(T('chat_disconnected')));
                return;
              }
              settled = true;
              clearTimeout(handshakeTimer);
              openedAt = Date.now();
              this.wsPromise = null;
              if (this.everConnected) this.connectionNote(T('chat_reconnected'));
              this.everConnected = true;
              resolve(ws);
            };
            ws.onerror = () => fail(new Error(T('chat_broken')));
            ws.onclose = (event) => {
              clearTimeout(handshakeTimer);
              fail(new Error(T('chat_disconnected')));
              if (generation !== this.wsGeneration || this.ws !== ws) return;
              this.ws = null;
              this.wsPromise = null;
              this.expireConnectionConfirmations();
              if (this.confirmationBusy && this.confirmationBusy.cancelPending && this.confirmationBusy.cancelSent) {
                this.failCancellation(this.confirmationBusy, T('cancellation_ack_timeout'), true);
              }
              const denied = [1008, 4001, 4003].includes(event?.code);
              const incompatible = [1002, 1003, 1009].includes(event?.code);
              if (denied || incompatible) this.reconnectWanted = false;
              // 短暂握手成功后再次断开也保留退避；稳定连接才恢复首次重试间隔。
              if (openedAt && Date.now() - openedAt >= 10000) this.reconnectAttempt = 0;
              this.connectionNote(T(denied ? 'chat_connection_denied' : incompatible ? 'chat_connection_incompatible' : 'chat_disconnected'));
              if (this.active && this.turn && !this.turn.done) {
                if (this.modelInfo?.background_execution) this.appendStatus(T('chat_background_running'),false,this.turn);
                else this.turnError(T('chat_turn_interrupted'));
              }
              else this.setActive(false, false);
              this.scheduleReconnect();
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
        promise.catch(error => {
          if (generation !== this.wsGeneration || !this.reconnectWanted) return;
          if (this.wsPromise === promise) this.wsPromise = null;
          if (error?.status === 401 || error?.status === 403) {
            this.reconnectWanted = false; this.connectionNote(T('chat_connection_denied')); return;
          }
          this.scheduleReconnect();
        });
        return promise;
      },
      disconnect() {
        // 显式停止、登出、新对话或销毁不得被后台重连计时器重新打开。
        this.reconnectWanted = false;
        this.reconnectAttempt = 0;
        if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
        this.reconnectTimer = 0;
        const ws = this.ws;
        if (this.connectionAbort) this.connectionAbort.abort();
        this.connectionAbort = null;
        this.wsGeneration++;
        this.ws = null;
        this.wsPromise = null;
        this.expireConnectionConfirmations();
        if (ws) { try { ws.close(); } catch { /* 忽略 */ } }
      },
      reset(hostReset = false) {
        // 宿主可能在原生删除回执后主动清屏；公共 clear 保持可撤销本实例显示的语义。
        if (this.history?.mutating && !hostReset) return;
        this.history?.reset();
        this.notifications?.reset();
        this.sendGeneration++;
        this.sending = false;
        if (this.contextAbort) this.contextAbort.abort();
        this.contextAbort = null;
        this.confirmationBusy = null;
        this.disconnect();
        chatSessionID(true); // 换会话号 = 服务端上下文重开
        this.clearTurnTimers();
        this.log.replaceChildren();
        this.setFollowing(true);
        this.availabilityNote = null;
        this.turn = null;
        this.turnsByRun.clear();
        this.everConnected = false;
        this.setActive(false);
        this.notice(T('session_reset'));
        this.renderStarters();
        if (this.modelInfo && (this.modelInfo.preview_only === true || this.modelInfo.model_ready === false)) {
          this.setAvailabilityNote(true, this.modelInfo.preview_only === true);
        }
        if (typeof opts.onNewChat === 'function') { try { opts.onNewChat(); } catch { /* display-only host hook */ } }
      },
      clearTurnTimers() {
        if (this.scrollFrame) cancelAnimationFrame(this.scrollFrame);
        this.scrollFrame = 0;
        if (this.composerResizeFrame) cancelAnimationFrame(this.composerResizeFrame);
        this.composerResizeFrame = 0;
        clearTimeout(this.announcementTimer);
        this.announcementTimer = 0;
        this.lastAnnouncement = '';
        this.announcementQueue = [];
        this.announcementBusy = false;
        if (!this.log) return;
        for (const card of Array.from(this.log.children)) {
          const turn = card._turn;
          if (!turn) continue;
          cancelAssistantRender(turn);
          for (const segment of (turn.thinkingSegments || new Map()).values()) {
            if (segment.frame) cancelAnimationFrame(segment.frame);
            segment.frame = 0;
          }
          if (turn.clock) clearInterval(turn.clock);
          turn.clock = 0;
          for (const rec of turn.tools.values()) {
            if (rec.confirmAbort) rec.confirmAbort.abort();
            rec.confirmAbort = null;
            if (rec.countdown) clearInterval(rec.countdown);
            rec.countdown = 0;
            if (rec.cancelAckTimer) clearTimeout(rec.cancelAckTimer);
            rec.cancelAckTimer = 0;
          }
        }
      },
      expireConnectionConfirmations() {
        if (this.modelInfo?.confirmation_scope !== 'connection' || !this.log) return;
        for (const card of Array.from(this.log.children)) {
          const turn = card._turn;
          if (!turn) continue;
          for (const rec of turn.tools.values()) {
            if (rec.status !== 'pending') continue;
            if (rec.confirmAbort) rec.confirmAbort.abort();
            if (this.confirmationBusy === rec) this.confirmationBusy = null;
            rec.confirmPending = false;
            this.setToolStatus(rec, 'cancelled', turn);
            rec.lastEvent = Object.assign({}, rec.lastEvent, { tool_summary: T('confirm_expired') });
            this.renderToolRecord(rec, rec.lastEvent, turn);
          }
          this.updateProcess(turn);
        }
        this.syncChatControls();
      },
      resetForAuth() {
        this.history?.reset();
        this.notifications?.reset();
        this.sendGeneration++;
        this.modelLoadSeq++;
        this.sending = false;
        if (this.contextAbort) this.contextAbort.abort();
        this.contextAbort = null;
        this.confirmationBusy = null;
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
          this.dom.input.value = '';
          this.dom.modelInput.value = '';
          this.dom.dataList.replaceChildren();
          this.model = '';
          this.setAgentMode('fast', false);
          this.syncChatControls();
        }
        this.close();
        if (this.log) {
          this.log.replaceChildren();
          this.notice(T('welcome'));
        }
      },
      setActive(v, complete = true) {
        this.active = v;
        if (!this.dom) return;
        this.syncChatControls();
        if (!v && complete && this.turn && !this.turn.done) this.finishTurn(null);
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

      async resolveContext(signal) {
        const context = this.clientContext();
        if (chatOnly) context.page.visible_text = '';
        let supplied = context;
        if (typeof opts.contextProvider === 'function') {
          supplied = await boundedChatHook(() => opts.contextProvider({ context, user: resolvedUser, signal }), signal);
        }
        const source = supplied && typeof supplied === 'object' ? supplied : {};
        const viewport = source.viewport || {};
        const page = source.page || {};
        const text = (value, fallback, max) => typeof value === 'string' ? clip(value, max) : fallback;
        const dimension = (value, fallback) => Number.isFinite(value) ? Math.max(0, Math.min(32768, value)) : fallback;
        let path = context.page.path;
        // Even trusted host context is browser input. Never forward URL
        // credentials, search parameters or arbitrary extra fields to a model.
        if (typeof opts.contextProvider === 'function' && typeof page.path === 'string') {
          try {
            const url = new URL(clip(page.path, 1024), location.origin);
            path = url.protocol === 'http:' || url.protocol === 'https:' ? url.pathname : '(embedded)';
          } catch { path = '(embedded)'; }
        }
        return {
          viewport: {
            width: dimension(viewport.width, context.viewport.width),
            height: dimension(viewport.height, context.viewport.height),
            device_pixel_ratio: dimension(viewport.device_pixel_ratio, context.viewport.device_pixel_ratio),
            mode: viewport.mode === 'mobile' || viewport.mode === 'pc' ? viewport.mode : context.viewport.mode,
          },
          page: { path, title: text(page.title, context.page.title, 300), visible_text: text(page.visible_text, context.page.visible_text, 1800),
            query_filters: (Array.isArray(page.query_filters) ? page.query_filters : []).slice(0, 32)
              .filter(filter => filter && typeof filter.name === 'string' && typeof filter.value === 'string' && !isTechnicalSecretKey(filter.name))
              .map(filter => ({ name: clip(filter.name, 96), value: clip(filter.value, 320) })) },
          locale: text(source.locale, context.locale, 64),
          time_zone: text(source.time_zone, context.time_zone, 128),
          user_agent: text(source.user_agent, context.user_agent, 512),
        };
      },

      // 应答服务端 ui_page_inspect 工具的 inspect_request:
      // 回传每个 Item 的实时渲染状态, 让 AI 基于真实屏幕自检而不是猜测。
      sendInspectResult(ev) {
        if (!ev || !ev.inspect_id || !this.ws || this.ws.readyState !== WebSocket.OPEN) return;
        // v1 旧服务端只发 inspect_id。对这种请求保留旧的“一次全量”
        // 应答（服务端旧上限 200），不能默认切成 20 项后让旧端误以为已完整。
        const legacyInspect = ev.inspect_offset == null && ev.inspect_limit == null && ev.inspect_ids == null;
        const requested = !legacyInspect && Array.isArray(ev.inspect_ids)
          ? Array.from(new Set(ev.inspect_ids.map((id) => String(id || '').trim()).filter(Boolean))).slice(0, 20)
          : [];
        const source = requested.length
          ? requested.map((id) => [id, registry.get(id) || null])
          : Array.from(registry.entries());
        const total = source.length;
        const range = lib.runtime && typeof lib.runtime.inspectRange === 'function'
          ? lib.runtime.inspectRange(total, ev.inspect_offset, ev.inspect_limit, legacyInspect)
          : legacyInspect
            ? { offset: 0, limit: 200 }
            : { offset: Math.max(0, Math.min(total, Math.floor(Number(ev.inspect_offset) || 0))), limit: Math.max(1, Math.min(20, Math.floor(Number(ev.inspect_limit) || 20))) };
        const { offset, limit } = range;
        const items = [];
        const lastErrorFor = (id) => clip(currentItemErrors.get(id) || '', 300);
        const visualRect = (node) => {
          let rect = node && node.getBoundingClientRect ? node.getBoundingClientRect() : null;
          if (rect && rect.width > 0 && rect.height > 0) return rect;
          // display:contents 自身没有 box，但其文本/子元素仍可能真正显示。
          // Range 可在不扫描整棵子树的情况下给出内容外接矩形。
          try {
            const range = doc.createRange();
            range.selectNodeContents(node);
            const contentRect = range.getBoundingClientRect();
            if (contentRect && contentRect.width > 0 && contentRect.height > 0) rect = contentRect;
          } catch { /* 无可选内容 */ }
          return rect;
        };
        const actuallyVisible = (node, rect) => {
          const viewport = {
            width: doc.documentElement.clientWidth || innerWidth,
            height: doc.documentElement.clientHeight || innerHeight,
          };
          const clips = [];
          const clipping = new Set(['hidden', 'clip', 'scroll', 'auto']);
          for (let current = node; current && current.nodeType === 1; current = current.parentElement) {
            const style = getComputedStyle(current);
            const opacity = Number.parseFloat(style.opacity);
            if (style.display === 'none' || style.visibility === 'hidden' || style.visibility === 'collapse' ||
                style.contentVisibility === 'hidden' || (Number.isFinite(opacity) && opacity <= 0)) return false;
            if (current !== node) {
              const clipX = clipping.has(style.overflowX);
              const clipY = clipping.has(style.overflowY);
              if ((clipX || clipY) && current.getBoundingClientRect) {
                const outer = current.getBoundingClientRect();
                const left = outer.left + (current.clientLeft || 0);
                const top = outer.top + (current.clientTop || 0);
                const width = current.clientWidth;
                const height = current.clientHeight;
                if (style.display !== 'contents') {
                  clips.push({
                    left, top, right: left + Math.max(0, width), bottom: top + Math.max(0, height),
                    clipX, clipY,
                  });
                }
              }
            }
            if (current === doc.documentElement) break;
          }
          if (lib.runtime && typeof lib.runtime.rectVisible === 'function') return lib.runtime.rectVisible(rect, viewport, clips);
          if (!rect || rect.right <= 0 || rect.bottom <= 0 || rect.left >= viewport.width || rect.top >= viewport.height) return false;
          return clips.every((clipRect) =>
            (!clipRect.clipX || (rect.right > clipRect.left && rect.left < clipRect.right)) &&
            (!clipRect.clipY || (rect.bottom > clipRect.top && rect.top < clipRect.bottom)));
        };
        for (const [id, rec] of source.slice(offset, offset + limit)) {
          const node = ((rec && rec.nodes) || []).find((n) => n && n.isConnected);
          const view = {
            id,
            type: (rec && rec.item && rec.item.type) || '',
            page: (rec && rec.page) || '',
            slot: (rec && rec.item && rec.item.meta && rec.item.meta.slot) || '',
            present: !!node,
          };
          if (node && node.getBoundingClientRect) {
            const rect = visualRect(node);
            view.width = Math.round(rect.width);
            view.height = Math.round(rect.height);
            view.visible = actuallyVisible(node, rect);
            view.text = clip((node.innerText || '').trim(), 200);
          }
          const err = lastErrorFor(id);
          if (err) view.error = err;
          items.push(view);
        }
        const nextOffset = offset + items.length < total ? offset + items.length : 0;
        const recent = errorLog.slice(-8).map((e) => clip((e.item_id ? e.item_id + ': ' : '') + e.message, 300));
        const currentTheme = theme.get();
        try {
          this.ws.send(JSON.stringify({
            type: 'inspect_result',
            inspect_id: ev.inspect_id,
            inspect: {
              page: nav.currentPage,
              viewport: { width: env.width, height: env.height, mode: env.mode },
              theme_mode: currentTheme.dark ? 'dark' : 'light',
              total,
              offset,
              limit,
              next_offset: nextOffset,
              truncated: nextOffset > 0,
              items,
              errors: recent,
              visible_text: offset === 0 ? clip((appEl.innerText || '').trim(), 1200) : '',
            },
          }));
        } catch (e) { /* 连接瞬断: 服务端工具会按离线超时返回 */ }
      },

      async send(value) {
        if (destroyed || !this.enabled || authRefreshing || !this.dom || this.active || this.sending || this.confirmationBusy || this.history?.mutating) return false;
        this.history?.close(false);
        this.notifications?.close(false);
        if (value !== undefined) {
          if (typeof value !== 'string') throw new TypeError('ihtml.chat.send: text must be a string');
          this.dom.input.value = value;
        }
        const text = this.dom.input.value.trim();
        if (!text || text.length > 65536) return false;
        this.sending = true;
        const generation = ++this.sendGeneration;
        const epoch = requestEpoch;
        const user = resolvedUser;
        const contextAbort = new AbortController();
        this.contextAbort = contextAbort;
        this.syncChatControls();
        let pendingTurn = null;
        try {
          if (!this.modelInfo) await this.loadModels();
          if (destroyed || epoch !== requestEpoch || user !== resolvedUser || generation !== this.sendGeneration || !this.enabled || authRefreshing) return false;
          const clientContext = await this.resolveContext(contextAbort.signal);
          if (destroyed || epoch !== requestEpoch || user !== resolvedUser || generation !== this.sendGeneration || contextAbort.signal.aborted || !this.enabled || authRefreshing) return false;
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
          um._displayMessage = { role: 'user', content: text, contentFormat: 'text' };
          um.appendChild(el('div', 'author', T('author_user')));
          um.appendChild(el('div', 'content', text));
          this.starters?.remove();
          this.log.appendChild(um);
          this.setFollowing(true);
          const request = chatAgentCore.requestSnapshot(this.agentModes, this.agentMode, this.model, !!this.modelInfo?.model_select);
          this.setActive(true);
          pendingTurn = this.beginTurn(request.agentMode);
          pendingTurn.requestModel = request.model;
          this.sending = false;
          this.syncChatControls();
          const ws = await this.ensureWS();
          if (destroyed || epoch !== requestEpoch || user !== resolvedUser || generation !== this.sendGeneration || !this.active || this.turn !== pendingTurn || pendingTurn.done || pendingTurn.cancelled) return false;
          ws.send(JSON.stringify({
            type: 'user_message',
            request_id: Array.from(crypto.getRandomValues(new Uint8Array(16)), value => value.toString(16).padStart(2, '0')).join(''),
            messages: [{ role: 'user', content: text }],
            model: pendingTurn.requestModel,
            agent_mode: pendingTurn.agentMode,
            tools_enabled: true,
            client_context: clientContext,
          }));
          return true;
        } catch (e) {
          if (generation === this.sendGeneration && !(e && e.stale) && !(pendingTurn && pendingTurn.cancelled)) {
            if (this.active && this.turn === pendingTurn && !pendingTurn.done) this.turnError(T('chat_broken'), pendingTurn);
            else ui.toast(T('chat_broken'), 'error');
          }
          return false;
        } finally {
          contextAbort.abort();
          if (this.contextAbort === contextAbort) this.contextAbort = null;
          if (generation === this.sendGeneration) {
            this.sending = false;
            if (this.dom) this.syncChatControls();
          }
        }
      },
      cancel() {
        if (this.modelInfo?.background_execution) {void this.stopBackground();return;}
        this.sendGeneration++;
        this.sending = false;
        if (this.contextAbort) this.contextAbort.abort();
        this.contextAbort = null;
        const turn = this.turn;
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
          this.ws.send(JSON.stringify({ type: 'cancel' }));
        }
        this.disconnect();
        if (turn) this.finishCancelled(turn);
        else this.setActive(false);
        if (this.dom) this.syncChatControls();
      },

      async stopBackground() {
        if (destroyed || !this.active || !this.turn || this.turn.done) return;
        const turn=this.turn;
        this.appendStatus(T('chat_background_stopping'),false,turn);
        try {
          const ws=await this.ensureWS();
          if (destroyed || this.turn!==turn || turn.done) return;
          ws.send(JSON.stringify({type:'cancel'}));
          this.finishCancelled(turn);
        } catch {
          this.appendStatus(T('chat_background_stop_unsent'),true,turn);
          this.syncChatControls();
        }
      },
      beginTurn(agentMode) {
        const card = el('article', 'ihtml-msg assistant');
        card.appendChild(el('div', 'author', 'AI'));
        const process = el('details', 'ihtml-process active');
        process.setAttribute('aria-busy', 'true');
        process.open = false;
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
        const actions = el('div', 'ihtml-trace-actions');
        actions.hidden = true;
        // 工具确认和执行回执固定在正文之前；流式总结不得把回执不断推到答复末尾。
        card.append(process, actions, answer);
        this.log.appendChild(card);
        const turn = {
          card, process, processSummary: summary, processLabel: label, processDot, note, pBody, answer, actions,
          processPinned: false, elapsedMS: 0, clockStartedAt: null, clock: 0,
          agentMode: chatAgentCore.selectMode(this.agentModes, agentMode),
          agentState: chatAgentCore.initialState(agentMode), agentOverview: null,
          text: '', textBytes: 0, textTruncated: false, contentFormat: 'auto', lastKind: '', lastStatusNode: null,
          tools: new Map(), toolCount: 0, pendingCount: 0, errorCount: 0, unknownCount: 0, done: false,
          turnFailed: false,
          runSeq: new Map(), acceptNextRun: false, renderFrame: 0, lastRenderAt: 0,
          cancelled: false, confirmingTool: false,
          thinkingSegments: new Map(), activeThinkingID: '', legacyThinkingSeq: 0,
          anonymousToolQueues: new Map(), anonymousToolAliases: new Map(), anonymousToolSeq: 0,
        };
        // 用户主动展开后保持阅读状态，后续流式事件不擅自收起。
        summary.addEventListener('click', event => {
          if (pBody.hidden) { event.preventDefault(); return; }
          turn.processPinned = !process.open;
          if (turn.processPinned) this.setFollowing(false);
        });
        card._turn = turn;
        this.turn = turn;
        this.lastAnnouncement = '';
        this.announce(T('analyzing'));
        this.updateProcess(turn);
        this.scroll(true);
        return turn;
      },
      processStepCount(t) {
        const reasoning = t.thinkingSegments ? t.thinkingSegments.size : 0;
        const deep = t.agentState && t.agentState.mode === 'deep' && t.agentState.plan.length ? 1 : 0;
        return t.toolCount + reasoning + deep;
      },
      processSummaryText(t) {
        if (t.unknownCount > 0) return T('tools_unknown', {n: t.unknownCount});
        if (t.cancelled) return T('turn_stopped');
        if (t.pendingCount > 0) return T('tools_pending', { n: t.pendingCount });
        if (!t.done) {
          const running = Array.from(t.tools.values()).filter((rec) => rec.status === 'running');
          if (running.length === 1) return T('running_tool', { name: running[0].title.textContent });
          if (running.length > 1) return T('tools_running', { n: running.length });
          if (t.agentState && t.agentState.manualPaused && t.agentState.plan.length) return T('agent_plan_paused');
          if (t.text) return T('answering');
          if (t.activeThinkingID) return T('reasoning_streaming');
          return t.lastStatusNode && t.lastStatusNode.dataset.persistent !== 'true' ? t.lastStatusNode.textContent : T('analyzing');
        }
        if (t.turnFailed) return T('turn_failed');
        if (t.errorCount > 0) return T('tools_failed', { n: t.errorCount });
        if (t.agentState && t.agentState.manualPaused && t.agentState.plan.length) return T('agent_plan_paused');
        const steps = this.processStepCount(t);
        if (steps > 0) return T('trace_completed', { n: steps });
        return T('done');
      },
      finishCancelled(t) {
        if (!t || t.done) { this.setActive(false); return; }
        t.cancelled = true;
        cancelAssistantRender(t);
        if (t.text || t.textTruncated) replaceAssistantPreservingView(t.answer, renderAssistant(t.text, t.contentFormat, t.answer, t.textTruncated));
        t.answer.classList.remove('streaming');
        this.clearTransientStatus(t);
        this.finalizeThinking(t);
        for (const rec of t.tools.values()) {
          const mutating = rec.confirmSent || /^(POST|PUT|PATCH|DELETE)$/i.test(rec.call.method || '');
          const nextStatus = chatAgentCore.statusAfterTurnStop(rec.status, mutating);
          if (nextStatus === rec.status) continue;
          this.setToolStatus(rec, nextStatus, t);
          this.releaseAnonymousTool(t, rec.id);
          rec.lastEvent = Object.assign({}, rec.lastEvent, { tool_summary: T(nextStatus === 'unknown' ? 'execution_unknown' : 'turn_stop_unknown') });
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
        if (t.pendingCount > 0 || t.errorCount > 0 || t.unknownCount > 0) return true;
        return Array.from(t.tools.values()).some((rec) => rec.status === 'running');
      },
      renderProcessMeta(t) {
        const elapsed = t.elapsedMS + (t.clockStartedAt == null ? 0 : performance.now() - t.clockStartedAt);
        const parts = [];
        if (t.toolCount) parts.push(T('trace_tool_count', {n: t.toolCount}));
        parts.push(T('trace_elapsed', {n: Math.max(0, Math.floor(elapsed / 1000))}));
        t.note.textContent = parts.join(' · ');
      },
      syncProcessClock(t) {
        // 等待人工确认不计入执行耗时；恢复执行时接着累计，销毁时统一回收。
        const working = !t.done && !t.cancelled && !t.pendingCount;
        if (working && t.clockStartedAt == null) t.clockStartedAt = performance.now();
        if (!working && t.clockStartedAt != null) {
          t.elapsedMS += performance.now() - t.clockStartedAt;
          t.clockStartedAt = null;
        }
        if (working && !t.clock) t.clock = setInterval(() => this.renderProcessMeta(t), 1000);
        if (!working && t.clock) { clearInterval(t.clock); t.clock = 0; }
        this.renderProcessMeta(t);
      },
      updateProcess(t) {
        t = t || this.turn;
        if (!t || !t.process || !t.process.isConnected) return;
        const focusWasInside = t.process.contains(doc.activeElement);
        let collapsed = false;
        t.process.classList.toggle('active', !t.done);
        t.process.classList.toggle('pending', t.pendingCount > 0);
        t.process.classList.toggle('error', t.turnFailed || t.errorCount > 0);
        t.process.classList.toggle('paused', !!(t.agentState && t.agentState.manualPaused && t.agentState.plan.length));
        t.process.dataset.state = t.cancelled ? 'cancelled' : (t.turnFailed || t.errorCount > 0 ? 'error' : (t.pendingCount > 0 ? 'pending' : (t.done ? 'complete' : 'running')));
        t.process.setAttribute('aria-busy', String(!t.done));
        const status = this.processSummaryText(t);
        t.processLabel.textContent = status;
        this.syncProcessClock(t);
        t.actions.hidden = !t.actions.childNodes.length;
        // 无实际计划时，不再用第二张“操作确认”概览重复待确认工具卡。
        if (t.agentOverview) t.agentOverview.hidden = !t.agentOverview._pinned && !t.agentState.plan.length && (t.pendingCount > 0 || t.agentState.manualPaused);
        // 通用等待提示只在摘要显示一次；不同的状态详情、错误和工具卡仍保留。
        for (const node of t.pBody.querySelectorAll('.ihtml-pline[data-persistent="false"]')) {
          node.hidden = node.textContent.trim() === status.trim() || node.textContent.trim() === T('analyzing');
        }
        t.pBody.hidden = !Array.from(t.pBody.children).some((node) => !node.hidden);
        t.process.classList.toggle('is-empty', t.pBody.hidden);
        if (t.pBody.hidden) { collapsed = t.process.open; t.process.open = false; }
        if (t.done) {
          this.finalizeThinking(t);
          if (t.agentOverview && !t.agentOverview._pinned && !t.agentState.manualPaused && !t.errorCount) t.agentOverview.open = false;
          if (!t.pBody.childNodes.length && !t.toolCount && !t.turnFailed) t.process.remove();
          else if (!t.processPinned) { collapsed = t.process.open; t.process.open = false; }
        } else if (t.text && !t.processPinned && !this.hasBlockingProcess(t) && !(t.agentState && t.agentState.manualPaused)) {
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
          (isError ? t.actions : t.pBody).appendChild(node);
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
        this.scroll();
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
          if (!segment.pinned) segment.wrap.open = false;
        }
        if (!segmentID || t.activeThinkingID === segmentID) t.activeThinkingID = '';
      },
      collapseThinking(t) {
        if (!t || !t.thinkingSegments) return;
        for (const segment of t.thinkingSegments.values()) if (segment.state === 'running' && !segment.pinned) segment.wrap.open = false;
      },
      appendThinking(delta, ev, t = this.turn) {
        if (!t) return;
        ev = ev && typeof ev === 'object' ? ev : {};
        if (delta) this.clearTransientStatus(t); // 已有真实分析内容后移除重复的临时“正在处理”。
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
          wrap.open = false;
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
          segment = { id, wrap, pre, meta, text: '', state: 'running', frame: 0, follow: true, pinned: false, truncated: false };
          summary.addEventListener('click', () => { segment.pinned = !wrap.open; if (segment.pinned) this.setFollowing(false); });
          pre.tabIndex = 0;
          let lastThinkingTop = pre.scrollTop;
          pre.addEventListener('scroll', () => {
            if (pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 24 && (segment.follow || pre.scrollTop > lastThinkingTop)) segment.follow = true;
            lastThinkingTop = pre.scrollTop;
          });
          pauseScrollOnUserIntent(pre, () => { segment.follow = false; });
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
      appendDelta(delta, contentFormat, t = this.turn) {
        if (!t) return;
        t.contentFormat = chatContentCore.mergeFormat(t.contentFormat, contentFormat, false);
        if (!delta || t.textTruncated) return;
        this.clearTransientStatus(t);
        // 正文和 reasoning 可能交错到达。正文开始只收起视觉详情，真正的
        // “分析完成”必须等待 thinking_done、工具边界或本轮终态。
        this.collapseThinking(t);
        const limited = chatContentCore.clipUTF8(delta, Math.max(0, MAX_ASSISTANT_BYTES - t.textBytes));
        t.text += limited.text;
        t.textBytes += limited.bytes;
        if (limited.truncated) t.textTruncated = true;
        t.answer.style.display = '';
        t.answer.classList.add('streaming');
        if (!t.renderFrame) {
          const clock = typeof performance !== 'undefined' && typeof performance.now === 'function' ? performance.now() : Date.now();
          const delay = chatAgentCore.streamRenderDelay(t.lastRenderAt, clock, 80);
          t.renderFrame = setTimeout(() => {
            t.renderFrame = 0;
            if (t.done) return;
            t.lastRenderAt = typeof performance !== 'undefined' && typeof performance.now === 'function' ? performance.now() : Date.now();
            // Render at a bounded cadence and restore user-owned disclosure,
            // scroll and focus state after rebuilding the sanitized fragment.
            replaceAssistantPreservingView(t.answer, renderAssistant(t.text, t.contentFormat, t.answer, t.textTruncated));
            this.scroll(); // 追加内容落入 DOM 后再跟随，不能只滚到上一批 token 的末尾。
          }, delay);
        }
        t.lastKind = 'answer';
        this.updateProcess(t);
        this.scroll();
      },
      resetAssistant(ev, t = this.turn) {
        if (!t || t.done) return;
        cancelAssistantRender(t);
        t.text = '';
        t.textBytes = 0;
        t.textTruncated = false;
        t.contentFormat = chatContentCore.mergeFormat('auto', ev && ev.content_format, true);
        t.answer.replaceChildren();
        t.answer.classList.remove('streaming');
        t.answer.removeAttribute('data-content-format');
        t.answer.style.display = 'none';
        // 确认后的结果总结只重置正文，保留用户选择的过程展开状态和真实回执。
        if (!t.confirmingTool) t.process.open = true;
        if (t.lastKind === 'answer') t.lastKind = '';
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
          summary.addEventListener('click', () => { details._pinned = !details.open; if (details._pinned) this.setFollowing(false); });
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
          budgetText.push(T('agent_tool_budget', { used: budget.toolCalls, max: budget.maxToolCalls || '∞' }));
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
          budget.maxSubtasks > 0 && budget.subtasks / budget.maxSubtasks >= 0.8 ||
          budget.maxToolCalls > 0 && budget.toolCalls / budget.maxToolCalls >= 0.8 ||
          !!budget.toolBudgetCode
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
          const manual = el('div', 'ihtml-agent-manual', T(this.deepApprovalResume === 'managed_turn' ? 'agent_managed_confirm' : 'agent_manual_confirm'));
          manual.setAttribute('role', 'note');
          body.appendChild(manual);
        }
        const hasAgentError = agents.some((stage) => stage.status === 'error');
        if (state.manualPaused || hasAgentError) details.open = true;
        else if ((t.text || t.done) && !details._pinned) details.open = false;
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
        const safeVisible = (value, limit) => {
          if (chatTraceCore && typeof chatTraceCore.safeText === 'function') {
            try { return chatTraceCore.safeText(value, limit); } catch { /* use fail-closed fallback */ }
          }
          const redacted = String(value == null ? '' : value)
            .replace(/\bBearer\s+[^\s,;]+/gi, 'Bearer [redacted]')
            .replace(/((?:authorization|confirmation[_-]?token|access[_-]?token|refresh[_-]?token|auth[_-]?token|session[_-]?(?:id|key|token)|private[_-]?key|client[_-]?secret|api[_-]?key|password|secret|cookie)\s*["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;&}]+)/gi, '$1[redacted]')
            .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f]/g, '');
          return clip(redacted, limit);
        };
        const presentationText = (value, fallback) => typeof value === 'string' ? (safeVisible(value.trim(), 80).trim() || fallback) : fallback;
        const fallbackFacts = [];
        if (Array.isArray(body.items)) fallbackFacts.push({ label: 'Items', value: body.items.map((item) => item && item.id).filter(Boolean).join(', ') });
        else if (Array.isArray(body.ids)) fallbackFacts.push({ label: 'Items', value: body.ids.join(', ') });
        else if (body.id) fallbackFacts.push({ label: 'ID', value: body.id });
        return {
          title: safeVisible(source.title || ev.tool_title || call.title || ev.tool_name || call.name || 'tool', 160),
          action: safeVisible(source.action || '', 320),
          summary: safeVisible(source.summary || (!view && ev.tool_summary) || '', 32768),
          reason: safeVisible(source.reason || (!view && call.reason) || '', 500),
          // Display-only metadata is never interpreted as HTML or execution
          // policy. Invalid/blank values keep the localized SDK wording.
          confirmText: presentationText(call.presentation?.confirm_text, T('confirm_this_action')),
          cancelText: presentationText(call.presentation?.cancel_text, T('dismiss_tool')),
          reasonLabel: presentationText(call.presentation?.reason_label, T('tool_reason')),
          facts: (Array.isArray(source.facts) ? source.facts : fallbackFacts).slice(0, 8).map((fact) => ({
            label: safeVisible(fact && fact.label || '', 80), value: safeVisible(fact && fact.value || '', 32768),
          })).filter((fact) => fact.label && fact.value),
          request: source.request !== undefined ? source.request : { method: call.method, path: call.path, query: call.query, body: call.body },
          result: source.result !== undefined ? source.result : ev.tool_result,
          debug: source.debug !== undefined ? source.debug : ev.tool_debug,
          hasTechnical: source.hasTechnical !== undefined ? !!source.hasTechnical : true,
        };
      },
      syncToolSummary(rec) {
        if (!rec || !rec.view) return;
        const summary = rec.note.textContent;
        // 展开的卡片在正文展示结果；收起后才把结果缩略放回标题，避免读两遍。
        rec.note.hidden = rec.card.open || !summary || summary === rec.view.title || summary === rec.view.action;
      },
      setToolStatus(rec, status, t) {
        if (!rec || !t) return;
        const previous = rec.status;
        if (previous === status) return;
        if (status !== 'pending' && rec.confirmAbort) rec.confirmAbort.abort();
        if (rec.countedPending && status !== 'pending') { t.pendingCount = Math.max(0, t.pendingCount - 1); rec.countedPending = false; }
        if (!rec.countedPending && status === 'pending') { t.pendingCount++; rec.countedPending = true; }
        if (rec.countedError && status !== 'error') { t.errorCount = Math.max(0, t.errorCount - 1); rec.countedError = false; }
        if (!rec.countedError && status === 'error') { t.errorCount++; rec.countedError = true; }
        if (previous === 'unknown') t.unknownCount = Math.max(0, (t.unknownCount || 0) - 1);
        if (status === 'unknown') t.unknownCount = (t.unknownCount || 0) + 1;
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
        rec.attention = rec.attention || ['pending', 'error', 'unknown'].includes(rec.status);
        const parent = rec.attention ? t.actions : t.pBody;
        if (rec.card.parentNode !== parent) parent.appendChild(rec.card);
        if (rec.countdown) { clearInterval(rec.countdown); rec.countdown = 0; }
        const view = this.toolView(rec.call, ev || {});
        rec.view = view;
        rec.title.textContent = view.title;
        // toolView is the single redaction boundary for every visible field.
        // Never prefer raw protocol values again in this DOM projection.
        const resultSummary = view.summary || view.action || '';
        rec.note.textContent = resultSummary;
        rec.note.hidden = rec.status === 'pending' || resultSummary === view.title || resultSummary === view.action;
        rec.body.replaceChildren();
        rec.body.removeAttribute('role');
        rec.body.removeAttribute('aria-labelledby');
        rec.body.removeAttribute('aria-describedby');
        rec.body.setAttribute('aria-busy', String(!!rec.cancelPending || !!rec.confirmPending));
        rec.card.classList.toggle('confirmation-busy', !!rec.cancelPending || !!rec.confirmPending);
        if (rec.cancelPending) {
          rec.chip.textContent = T('tool_cancelling');
          rec.chip.className = 'ihtml-chip t-status running';
        }

        const primaryText = rec.cancelPending
          ? T('cancellation_waiting')
          : (rec.confirmPending ? T('confirmation_connecting') : (rec.status === 'pending'
          ? (view.action || resultSummary)
          : (resultSummary || view.action)));
        let primary = null;
        if (primaryText && primaryText !== view.title && primaryText !== view.action) {
          primary = el('div', 't-primary' + (rec.status === 'success' || rec.status === 'accepted' || rec.status === 'error' ? ' t-live-result' : ''), primaryText);
          primary.id = rec.domID + '-primary';
          rec.body.appendChild(primary);
        }
        if (rec.status === 'pending' && view.reason) {
          const reason = el('div', 't-reason');
          reason.append(el('strong', '', view.reasonLabel), doc.createTextNode(view.reason));
          rec.body.appendChild(reason);
        }
        if (rec.interactionError) {
          const failure = el('div', 't-reason t-interaction-error', rec.interactionError);
          failure.setAttribute('role', 'alert');
          rec.body.appendChild(failure);
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
        if (rec.status === 'unknown' && typeof opts.verificationLinks === 'function') {
          try {
            const call = JSON.parse(JSON.stringify(rec.call, (key, value) => isTechnicalSecretKey(key) ? undefined : value));
            const links = opts.verificationLinks(call);
            const nav = el('nav', 't-verification');
            for (const item of (Array.isArray(links) ? links : []).slice(0, 4)) {
              const url = new URL(String(item.url || ''), location.href);
              if (url.origin !== location.origin || !/^https?:$/.test(url.protocol) || url.username || url.password || Array.from(url.searchParams.keys()).some(isTechnicalSecretKey)) continue;
              const link = el('a', '', chatTraceCore.safeText(item.label, 100));
              link.href = url.href; nav.appendChild(link);
            }
            if (nav.childNodes.length) rec.body.appendChild(nav);
          } catch { /* verification UI must never alter execution */ }
        }
        const review = chatTraceCore && typeof chatTraceCore.reviewView === 'function' ? chatTraceCore.reviewView(rec.call.review, T, opts.reviewFieldLabel) : null;
        if (review && (review.rows.length || review.scope || review.blockingReason)) {
          const section = el('section', 't-review');
          if (review.scope) section.appendChild(el('strong', '', review.scope));
          if (review.blockingReason) section.appendChild(el('p', 't-interaction-error', review.blockingReason));
          if (review.rows.length) {
            const table = el('table'); const head = el('tr');
            for (const label of [T('review_field'), T('review_before'), T('review_after')]) head.appendChild(el('th', '', label));
            const thead = el('thead'); thead.appendChild(head);table.appendChild(thead);
            const body = el('tbody');
            for (const row of review.rows) {
              const tr = el('tr');
              for (const text of [row.label, row.before, row.after]) tr.appendChild(el('td', '', text));
              body.appendChild(tr);
            }
            table.appendChild(body);section.appendChild(table);
          }
          if (review.truncated) section.appendChild(el('small', '', T('review_partial')));
          rec.body.appendChild(section);
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
              // An in-flight cancellation needs its server acknowledgement.
              // Expiry is not an execution/cancellation receipt; retain the
              // acknowledgement timer and its honest unknown-state fallback.
              if (rec.cancelPending) {
                countdown.textContent = T('cancellation_waiting');
                return;
              }
              rec.expired = true;
              if (rec.confirmAbort) rec.confirmAbort.abort();
              rec.confirmAbort = null;
              if (this.confirmationBusy === rec) this.confirmationBusy = null;
              rec.confirmPending = false;
              this.setToolStatus(rec, 'cancelled', t);
              this.releaseAnonymousTool(t, rec.id);
              rec.lastEvent = Object.assign({}, rec.lastEvent, { tool_summary: T('confirm_expired') });
              this.renderToolRecord(rec, rec.lastEvent, t);
              this.announce(T('confirmation_expired_announced', { name: rec.title.textContent }));
              this.updateProcess(t);
              this.syncChatControls();
              if (t.done && t.pendingCount === 0) this.releaseTurnRuns(t);
              return;
            }
            const seconds = Math.max(0, Math.ceil(left / 1000));
            countdown.textContent = T('expires_in') + ' ' + Math.floor(seconds / 60) + ':' + String(seconds % 60).padStart(2, '0');
            countdown.classList.toggle('is-expiring', seconds <= 120);
          };
          const deadline = el('time', 't-deadline');
          const expiry = new Date(rec.call.confirmation_expires * 1000);
          deadline.dateTime = expiry.toISOString();
          deadline.textContent = T('expires_at') + ' ' + expiry.toLocaleString(opts.locale || navigator.language);
          meta.appendChild(deadline);
          rec.countdown = setInterval(tick, 1000);
          tick();
          if (rec.status !== 'pending') return;
        }
        if (meta.childNodes.length) rec.body.appendChild(meta);
        // 业务影响说明不能替代续办能力；受管续办另显示一行共享提示，普通单卡避免重复说明。
        if (rec.status === 'pending' && rec.call.approval_resume === 'manual_tool_only' && (!view.reason || this.deepApprovalResume === 'managed_turn')) {
          const manual = el('div', 'ihtml-agent-manual', T(this.deepApprovalResume === 'managed_turn' ? 'agent_managed_confirm' : 'agent_manual_confirm'));
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
          const okBtn = el('button', 'ok', view.confirmText);
          const noBtn = el('button', '', view.cancelText);
          okBtn.setAttribute('aria-label', view.confirmText + ' · ' + view.title);
          noBtn.setAttribute('aria-label', view.cancelText + ' · ' + view.title);
          const interactionBusy = !!this.confirmationBusy || this.active || this.sending || !!rec.cancelPending || !!rec.confirmPending;
          okBtn.disabled = interactionBusy || !!review?.blockingReason;
          noBtn.disabled = interactionBusy;
          okBtn.onclick = (event) => { event.preventDefault(); okBtn.disabled = true; noBtn.disabled = true; this.confirmTool(rec); };
          noBtn.onclick = (event) => { event.preventDefault(); okBtn.disabled = true; noBtn.disabled = true; this.cancelTool(rec); };
          bar.append(okBtn, noBtn);
          rec.body.appendChild(bar);
          rec.body.setAttribute('role', 'group');
          rec.body.setAttribute('aria-labelledby', rec.title.id);
          if (primary) rec.body.setAttribute('aria-describedby', primary.id);
        }
        if (rec.expired && rec.status === 'cancelled') {
          const bar = el('div', 't-actions');
          const refresh = el('button', 't-refresh', T('refresh_confirmation'));
          refresh.disabled = !!this.confirmationBusy || this.active || this.sending;
          refresh.onclick = (event) => {event.preventDefault(); this.send(T('refresh_confirmation_request'));};
          bar.appendChild(refresh); rec.body.appendChild(bar);
        }
        if (rec.status === 'pending' || rec.status === 'error' || rec.status === 'unknown' || rec.expired) rec.card.open = true;
        else if (['success', 'accepted', 'cancelled'].includes(rec.status) && !rec.pinned) rec.card.open = false;
        this.syncToolSummary(rec);
        if (hadFocus) queueMicrotask(() => {
          const owner = rec.ownerTurn;
          const target = owner && rec.card.parentNode === owner.pBody && !owner.process.open ? owner.processSummary : rec.summary;
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
        // 历史回放中的确认执行属于新 run；按签名卡的 id+nonce 回写原卡，
        // 不能仅按工具 id 或标题匹配，已执行状态不能被迟到的待确认事件回退。
        if (nonce) {
          const original = this.findConfirmationRecord({ tool_id: call.id || ev.tool_id, confirmation_nonce: nonce });
          if (original) {
            if (['success', 'accepted', 'error', 'cancelled'].includes(original.status) && ['pending', 'running'].includes(status)) return;
            if (original.status === 'running' && status === 'pending') return;
            if (original.ownerTurn !== t) {
              this.upsertTool(ev, status, original.ownerTurn);
              this.syncChatControls();
              return;
            }
          }
        }
        const queueKey = this.anonymousToolQueueKey(ev, call);
        let id = call.id || ev.tool_id || (nonce && t.anonymousToolAliases.get(nonce)) || '';
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
          rec = {
            id, card, summary, chip, title, note, body, domID, call, status: '', ownerTurn: t,
            countedPending: false, countedError: false, countdown: 0, cancelAckTimer: 0,
            cancelPending: false, cancelSent: false, confirmPending: false, interactionError: '', lastEvent: {},
          };
          summary.addEventListener('click', () => { rec.pinned = !card.open; if (rec.pinned) this.setFollowing(false); });
          card.addEventListener('toggle', () => this.syncToolSummary(rec));
          t.tools.set(id, rec);
          t.toolCount++;
        }
        if (nonce) t.anonymousToolAliases.set(nonce, id);
        // 开始/回执事件通常只有签名卡的引用，不能覆盖原来已验证的目标和标题。
        if (ev.tool_call) rec.call = Object.assign({}, rec.call, ev.tool_call);
        const previous = rec.status;
        rec.lastEvent = Object.assign({}, rec.lastEvent, ev, { tool_call: rec.call });
        if (ev.type === 'tool_start') {
          this.clearTransientStatus(t);
          this.finalizeThinking(t, t.activeThinkingID);
        }
        this.setToolStatus(rec, status, t);
        if (['success', 'accepted', 'error', 'cancelled'].includes(status)) this.releaseAnonymousTool(t, id);
        this.renderToolRecord(rec, rec.lastEvent, t);
        if (status === 'pending' && previous !== 'pending') {
          this.announce(T('confirmation_announced', { name: rec.title.textContent, risk: RISK_LABEL[rec.call.risk] || rec.call.risk || T('risk_low') }));
        } else if (previous !== status && ['success', 'accepted', 'error', 'cancelled'].includes(status)) {
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
      async confirmTool(rec) {
        if (destroyed || !this.enabled || authRefreshing || readOnly) return;
        if (!rec || rec.status !== 'pending') return;
        if (rec.call.confirmation_expires && rec.call.confirmation_expires * 1000 <= Date.now()) {
          this.renderToolRecord(rec, rec.lastEvent, rec.ownerTurn); return;
        }
        if (rec.call.review?.blocking_reason) return;
        if (this.active || this.sending || this.confirmationBusy) {
          this.syncChatControls();
          ui.toast(T('confirmation_busy'), 'warn');
          this.announce(T('confirmation_busy'));
          return;
        }
        this.confirmationBusy = rec;
        rec.confirmPending = true;
        rec.interactionError = '';
        this.renderToolRecord(rec, rec.lastEvent, rec.ownerTurn);
        this.syncChatControls();
        let ws;
        try {
          if (typeof opts.beforeConfirm === 'function') {
            const controller = new AbortController();
            rec.confirmAbort = controller;
            let approved;
            try {
              const toolCall = JSON.parse(JSON.stringify(rec.call, (key, value) => isTechnicalSecretKey(key) ? undefined : value));
              approved = await boundedChatHook(() => opts.beforeConfirm({ toolCall, signal: controller.signal }), controller.signal, 120000);
            } finally {
              controller.abort();
              if (rec.confirmAbort === controller) rec.confirmAbort = null;
            }
            if (this.confirmationBusy !== rec || rec.status !== 'pending') return;
            if (destroyed || authRefreshing || readOnly) throw staleRequestError();
            if (approved !== true) {
              this.confirmationBusy = null;
              rec.confirmPending = false;
              this.renderToolRecord(rec, rec.lastEvent, rec.ownerTurn);
              this.syncChatControls();
              return;
            }
          }
          ws = await this.ensureWS();
          if (this.confirmationBusy !== rec || rec.status !== 'pending') return;
          if (destroyed || authRefreshing || readOnly) throw staleRequestError();
          if (rec.call.confirmation_expires && rec.call.confirmation_expires * 1000 <= Date.now()) {
            this.renderToolRecord(rec, rec.lastEvent, rec.ownerTurn); return;
          }
          ws.send(JSON.stringify({ type: 'confirm_tool', tool_call: rec.call }));
          rec.confirmSent = true;
        } catch {
          if (this.confirmationBusy !== rec) return;
          this.confirmationBusy = null;
          rec.confirmPending = false;
          rec.interactionError = T('confirmation_send_failed');
          this.renderToolRecord(rec, rec.lastEvent, rec.ownerTurn);
          this.syncChatControls();
          ui.toast(T('confirmation_send_failed'), 'error');
          return;
        }
        this.confirmationBusy = null;
        rec.confirmPending = false;
        const t = rec.ownerTurn;
        this.turn = t;
        t.done = false;
        // A preserved confirmation card is an independent, explicit user
        // action. Confirming it after a normal turn stop starts a new tool run
        // and should no longer project the owner turn as merely stopped.
        t.cancelled = false;
        t.acceptNextRun = true;
        t.confirmingTool = true;
        this.setToolStatus(rec, 'running', t);
        rec.lastEvent = Object.assign({}, rec.lastEvent, { tool_summary: T('confirmed_running') });
        this.renderToolRecord(rec, rec.lastEvent, t);
        this.announce(T('tool_confirmed_announced', { name: rec.title.textContent }));
        this.setActive(true);
        this.updateProcess(t);
        queueMicrotask(() => { if (rec.summary.isConnected) rec.summary.focus({ preventScroll: true }); });
      },
      async cancelTool(rec) {
        if (!rec || rec.status !== 'pending') return;
        if (this.active || this.sending || this.confirmationBusy) {
          this.syncChatControls();
          ui.toast(T('confirmation_busy'), 'warn');
          this.announce(T('confirmation_busy'));
          return;
        }
        const t = rec.ownerTurn;
        const cancelEvent = chatAgentCore.cancelToolEvent(rec.call);
        this.confirmationBusy = rec;
        rec.cancelPending = true;
        rec.cancelSent = false;
        rec.interactionError = '';
        this.renderToolRecord(rec, rec.lastEvent, t);
        this.syncChatControls();
        try {
          const ws = await this.ensureWS();
          if (this.confirmationBusy !== rec || rec.status !== 'pending') return;
          ws.send(JSON.stringify(cancelEvent));
          rec.cancelSent = true;
          if (rec.call.approval_resume === 'checkpoint') {
            this.turn = t;
            t.done = false;
            t.cancelled = false;
            t.acceptNextRun = true;
            t.confirmingTool = true;
            this.setActive(true);
          }
          rec.cancelAckTimer = setTimeout(() => {
            if (rec.cancelPending) this.failCancellation(rec, T('cancellation_ack_timeout'), true);
          }, 12000);
        } catch {
          if (this.confirmationBusy === rec) this.failCancellation(rec, T('cancellation_send_failed'), false);
        }
      },
      failCancellation(rec, message, terminal) {
        if (!rec) return;
        terminal = chatAgentCore.cancellationFailureTerminal(rec.cancelSent, terminal);
        if (rec.cancelAckTimer) clearTimeout(rec.cancelAckTimer);
        rec.cancelAckTimer = 0;
        if (this.confirmationBusy === rec) this.confirmationBusy = null;
        rec.cancelPending = false;
        rec.cancelSent = false;
        rec.interactionError = message || T('cancellation_failed');
        const t = rec.ownerTurn;
        if (terminal && t) {
          this.setToolStatus(rec, 'error', t);
          this.releaseAnonymousTool(t, rec.id);
        }
        if (t) {
          rec.lastEvent = Object.assign({}, rec.lastEvent, { tool_summary: rec.interactionError });
          this.renderToolRecord(rec, rec.lastEvent, t);
          this.updateProcess(t);
          if (terminal) this.announce(rec.interactionError);
          if (t.done && t.pendingCount === 0) this.releaseTurnRuns(t);
        }
        this.syncChatControls();
      },
      // 执行与取消回执共用签名卡定位，不依赖当前模型轮次。
      findConfirmationRecord(ev) {
        if (!this.log) return null;
        for (const message of Array.from(this.log.children)) {
          const owner = message._turn;
          if (!owner) continue;
          for (const rec of owner.tools.values()) {
            if (chatAgentCore.cancelAckMatches(rec.call, ev)) return rec;
          }
        }
        return null;
      },
      handleCancellationResult(ev) {
        const rec = this.findConfirmationRecord(ev);
        if (!rec) return;
        if (rec.cancelAckTimer) clearTimeout(rec.cancelAckTimer);
        rec.cancelAckTimer = 0;
        if (String(ev.tool_status || '') !== 'cancelled') {
          const rawError = String(ev.error || '');
          let visibleError = T('cancellation_failed');
          // Error text from third-party ChatBackend implementations is
          // untrusted. Keep credential-bearing URLs/headers fail-closed and use
          // the shared chat redaction boundary for ordinary short messages.
          if (rawError && !/(?:\bBearer\b|\b(?:https?|postgres(?:ql)?|mysql|redis):\/\/|(?:token|secret|password|cookie|authorization)\s*[:=])/i.test(rawError)
            && chatTraceCore && typeof chatTraceCore.safeText === 'function') {
            try { visibleError = chatTraceCore.safeText(rawError, 400) || visibleError; } catch { /* keep generic */ }
          }
          this.failCancellation(rec, visibleError, true);
          return;
        }
        if (this.confirmationBusy === rec) this.confirmationBusy = null;
        rec.cancelPending = false;
        rec.cancelSent = false;
        rec.interactionError = '';
        const t = rec.ownerTurn;
        this.setToolStatus(rec, 'cancelled', t);
        this.releaseAnonymousTool(t, rec.id);
        rec.lastEvent = Object.assign({}, rec.lastEvent, ev, { tool_summary: ev.tool_summary || T('cancelled_not_run') });
        this.renderToolRecord(rec, rec.lastEvent, t);
        this.announce(T('tool_dismissed_announced', { name: rec.title.textContent }));
        this.updateProcess(t);
        this.syncChatControls();
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
          if (!(this.turn?.acceptNextRun) && (ev.restored_approval || ev.background_replay)) {
            const foreground = this.turn;
            t = this.beginTurn('deep');
            this.turn = foreground && !foreground.done ? foreground : t;
            if (ev.background_replay && this.turn===t) this.setActive(true);
            t.runSeq.set(runID, 0);
            this.turnsByRun.set(runID, t);
          }
          const candidate = this.turn;
          if (!t && candidate && (candidate.runSeq.size === 0 || candidate.acceptNextRun)) {
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
        if (!ev || typeof ev !== 'object' || typeof ev.type !== 'string') return;
        if (ev.background_session && Number(ev.background_seq)>0) {
          if (this.backgroundSession!==ev.background_session) {this.backgroundSession=ev.background_session;this.backgroundSeq=0;}
          if (Number(ev.background_seq)<=this.backgroundSeq) return;
          this.backgroundSeq=Number(ev.background_seq);
        }
        if (ev.type==='background_user_message') {
          const existing=(opts.initialMessages||[]).some(message=>message.role==='user'&&message.content===ev.message);
          if (!existing && ev.background_replay && typeof ev.message==='string') {
            const row=el('article','ihtml-msg user');row.append(el('div','author',T('author_user')),el('div','content',ev.message));
            this.log.appendChild(row);
          }
          return;
        }
        // inspect_request 是服务端的带外请求, 不依赖轮次归属与序号去重:
        // 即使本地轮次已收尾也应答, 否则服务端工具只能等到超时。
        if (ev.type === 'inspect_request') { this.sendInspectResult(ev); this.notifyEvent(ev); return; }
        // cancel_tool_result is connection-scoped rather than a model turn. Its
        // id/nonce pair resolves the exact historical confirmation card.
        if (ev.type === 'cancel_tool_result') { this.handleCancellationResult(ev); this.notifyEvent(ev); return; }
        const t = this.eventTurn(ev);
        if (!t) return;
        if (ev.agent || ev.budget || ev.type === 'assistant_plan' || ev.tool_call && ['manual_tool_only', 'checkpoint'].includes(ev.tool_call.approval_resume)) {
          this.projectAgentEvent(ev, t);
        }
        switch (ev.type) {
          case 'turn_start': this.appendStatus(T('analyzing'), false, t); break;
          case 'assistant_status': this.appendStatus(ev.status, false, t); break;
          case 'assistant_thinking': this.appendThinking(ev.thinking || '', ev, t); break;
          case 'assistant_reset': this.resetAssistant(ev, t); break;
          case 'assistant_delta': this.appendDelta(ev.delta || '', ev.content_format, t); break;
          case 'assistant_snapshot': this.resetAssistant(ev,t);this.appendDelta(ev.delta||'',ev.content_format,t);break;
          case 'assistant_plan':
          case 'assistant_agent':
          case 'assistant_budget': break;
          case 'tool_start': this.upsertTool(ev, 'running', t); break;
          case 'tool_result': this.upsertTool(ev, ev.tool_status || 'success', t); break;
          case 'confirmation_required': this.upsertTool(ev, 'pending', t); break;
          case 'turn_paused':
            cancelAssistantRender(t);
            this.clearTransientStatus(t);
            this.finalizeThinking(t);
            t.answer.classList.remove('streaming');
            t.confirmingTool = false;
            t.done = false;
            if (this.turn === t) this.setActive(false, false);
            this.appendStatus(ev.status || T('tool_pending'), false, t);
            this.updateProcess(t);
            break;
          case 'assistant_done': this.finishTurn(ev, t);
            if(ev.background_replay && (opts.initialMessages||[]).some(message=>message.role==='assistant'&&message.content===ev.message)) {
              // 产品历史已有正文，回放只补工具回执/待确认卡，避免同一答复显示两遍。
              t.answer.hidden=true;if(!t.toolCount&&!t.pendingCount)t.card.remove();
            }
            break;
          case 'turn_error': if(ev.finish_reason==='cancelled')this.finishCancelled(t);else this.turnError(ev.error || 'error', t); break;
        }
        this.notifyEvent(ev);
      },
      notifyEvent(ev) {
        if (typeof opts.onChatEvent !== 'function' || destroyed) return;
        try {
          // The host receives a detached snapshot, never the executable card
          // or internal state. Hook failures cannot alter protocol handling.
          const snapshot = JSON.parse(JSON.stringify(ev, (key, value) =>
            isTechnicalSecretKey(key) || key === 'tool_debug' ? undefined : value));
          const result = opts.onChatEvent(snapshot);
          if (result && typeof result.catch === 'function') result.catch(() => {});
        } catch { /* host observation is best effort */ }
      },
      finishTurn(ev, t = this.turn) {
        // 中间确认回执必须保留当前请求的运行状态，下一 run_id 仍归属同一张流程卡。
        const continuing=ev.continuation_pending===true;
        if(continuing&&t){
          cancelAssistantRender(t);this.clearTransientStatus(t);this.finalizeThinking(t);
          t.confirmingTool=false;t.done=false;t.acceptNextRun=true;t.text='';t.textBytes=0;t.answer.replaceChildren();t.answer.style.display='none';
          this.appendStatus(T('agent_continuing'),false,t);this.updateProcess(t);if(this.turn===t)this.setActive(true);return;
        }
        if (!t || t.done) {
          if (this.turn === t && this.active) this.setActive(false);
          return;
        }
        cancelAssistantRender(t);
        this.clearTransientStatus(t);
        this.finalizeThinking(t);
        t.contentFormat = chatContentCore.finalizeFormat(t.contentFormat, ev && ev.content_format);
        const finalSnapshot = chatContentCore.doneSnapshot(ev);
        // assistant_done.message 是本轮的权威快照。Deep 在确认边界会重写
        // 最终文案，不能让此前的流式草稿继续冒充最终结果。
        if (finalSnapshot.present) {
          const limited = chatContentCore.clipUTF8(finalSnapshot.message, MAX_ASSISTANT_BYTES);
          t.text = limited.text;
          t.textBytes = limited.bytes;
          t.textTruncated = limited.truncated;
        }
        t.confirmingTool = false;
        if ((t.text && t.text.trim()) || t.textTruncated) {
          t.answer.style.display = '';
          replaceAssistantPreservingView(t.answer, renderAssistant(t.text, t.contentFormat, t.answer, t.textTruncated));
        } else {
          t.answer.replaceChildren();
          t.answer.style.display = 'none';
        }
        t.answer.classList.remove('streaming');
        t.done = true;
        this.updateProcess(t);
        const answerSummary = clip(String(t.answer.innerText || t.answer.textContent || '').replace(/\s+/g, ' ').trim(), 140);
        this.announce(answerSummary ? T('answer_complete_summary', { summary: answerSummary }) : T('answer_complete'));
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
          cancelAssistantRender(t);
          this.finalizeThinking(t);
          for (const rec of t.tools.values()) {
            if (rec.status !== 'running') continue;
            const mutating = rec.confirmSent || /^(POST|PUT|PATCH|DELETE)$/i.test(rec.call.method || '');
            const status = mutating ? 'unknown' : 'error';
            this.setToolStatus(rec, status, t);
            rec.lastEvent = Object.assign({}, rec.lastEvent, { tool_summary: status === 'unknown' ? T('execution_unknown') : msg });
            this.renderToolRecord(rec, rec.lastEvent, t);
          }
          if (t.text || t.textTruncated) replaceAssistantPreservingView(t.answer, renderAssistant(t.text, t.contentFormat, t.answer, t.textTruncated));
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
    let authRefreshing = false;
    let resolvedUser = '';
    async function loadConfig() {
      const epoch = requestEpoch;
      const cfg = await http.get('api/config');
      if (destroyed || epoch !== requestEpoch) throw staleRequestError();
      const previousStorageNamespace = storageNamespace(resolvedUser);
      resolvedUser = String((cfg && cfg.user) || '');
      api.user = resolvedUser;
      const migratedLegacyLocal = migrateLegacyStorageOnce();
      // Cookie/header based resolvers may not expose a user hint before config.
      // Once identity is known, switch to that user's instance-local cache.
      if (!hostTheme && (migratedLegacyLocal || previousStorageNamespace !== storageNamespace(resolvedUser))) themeLoadLocal();
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
      if (chatOnly) return;
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
      itemEditor.remoteKnown = false;
      itemEditor.remoteRevision = KV_MISSING_REVISION;
      itemEditor.remoteFingerprint = '';
      itemEditor.remoteSourceFingerprint = 'missing';
      itemEditor.remoteChangePending = false;
      itemEditor.loadError = null;
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
      currentItemErrors.clear();
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
      if (!chatOnly && !envGridBound) {
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
      if (chatOnly) {
        themeLoadLocal();
        applyEnv();
        try {
          const cfg = await loadConfig();
          if (destroyed) return;
          configureActions(cfg);
          finishReadyOnce(cfg);
          if (chat.enabled) void chat.loadModels().catch(() => {});
        } catch (error) {
          if (destroyed || isStaleError(error)) return;
          configureActions({ chat: false });
          chat.systemNote(T('chat_broken'));
          finishReadyOnce({ chat: false });
        }
        return;
      }
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
      authRefreshing = true;
      chat.enabled = false;
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
        if (chatOnly) {
          finishReadyOnce(cfg);
          return cfg;
        }
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
        if (chatOnly) {
          chat.enabled = false;
          chat.systemNote(T('chat_broken'));
          finishReadyOnce({ chat: false });
          throw err;
        }
        if (err && err.status === 401) renderAuthHint();
        else errorState.show(err && err.message || err, () => {
          errorState.hide();
          void refreshAuthAndState().catch(() => {});
        });
        throw err;
      } finally {
        if (epoch === requestEpoch) {
          authRefreshing = false;
          chat.syncChatControls();
        }
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
      refreshConfig: async () => {
        const user = resolvedUser;
        const cfg = await loadConfig();
        if (booted && user !== resolvedUser) return refreshAuthAndState();
        return cfg;
      },
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
        draft: (text) => {
          if (typeof text !== 'string') throw new TypeError('ihtml.chat.draft: text must be a string');
          if (destroyed || authRefreshing || !chat.dom) return false;
          chat.dom.input.value = chatContentCore.clipUTF8(text, 64 * 1024).text;
          chat.resizeComposer();
          return true;
        },
        open: () => chat.open(),
        close: () => chat.close(),
        send: async (text) => {
          await readyPromise;
          if (destroyed || !chat.enabled || authRefreshing) return false;
          if (!chat.dom) chat.build();
          return !!(await chat.send(text));
        },
        cancel: () => { if (!destroyed) chat.cancel(); },
        history: {open: () => { if (!destroyed && !authRefreshing) chat.history?.open(); }, close: () => chat.history?.close(), refresh: () => chat.history?.refresh(), get opened() { return !!chat.history?.opened; }},
        // 仅连接会话标识，不是授权凭据；宿主可据此标记当前历史记录。
        get sessionId() { return destroyed || authRefreshing ? '' : chatSessionID(); },
        getMessages: () => chat.getMessages(),
        clear: () => {
          if (destroyed || authRefreshing) return;
          if (!chat.dom) chat.build();
          chat.reset(true);
        },
        get enabled() { return !destroyed && chat.enabled; },
        get busy() { return !destroyed && (chat.active || chat.sending || !!chat.confirmationBusy); },
        notifications: {
          open:()=>{if(chat.notifications){chat.history?.close(false);chat.notifications.open();}},
          close:()=>chat.notifications?.close(false),
          refresh:()=>chat.notifications?.refresh(),
        },
        get readOnly() { return readOnly; },
      },
      destroy() {
        if (destroyed) return;
        destroyed = true;
        chat.sendGeneration++;
        if (chat.contextAbort) chat.contextAbort.abort();
        chat.contextAbort = null;
        if (chat.restoreIsolation) { chat.restoreIsolation(); chat.restoreIsolation = null; }
        runtimeBridge.unregister(instanceKey, publicAPI);
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
    Object.defineProperty(api, '__itemFacade', {
      value: (id, lifecycle) => createItemFacade(id, lifecycle),
      enumerable: false,
    });
    const publicAPI = chatOnly ? {
      version: api.version, root: rootEl,
      ready: api.ready, refreshAuth: api.refreshAuth,
      refreshConfig: api.refreshAuth,
      chat: api.chat, theme: api.theme, destroy: api.destroy,
      get user() { return api.user; },
      get config() { return api.config; },
    } : api;
    runtimeBridge.register(instanceKey, publicAPI);
    boot();
    return publicAPI;
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

  /* ---------- 全局入口 ----------
   * 同一页面支持多个相互隔离的实例(内核状态全部在 createInstance 闭包内)。
   * 兼容层: 当前主实例的 API 由 window.ihtml 动态代理, 既有单实例宿主代码不变;
   * 多实例宿主请使用 mount() 的返回值操作各自实例。 */
  const liveInstances = new Set();
  const mountedHosts = new WeakMap();
  const mountingHosts = new WeakSet();
  const compatibilityProxyKeys = new Set();
  const compatibilityReservedKeys = new Set(['version', 'mounted', 'instances', 'mount', 'mountChat', '__primary']);
  const globalAPI = {
    version: '0.6.0',
    mounted: false,
    __primary: null,
    instances: () => Array.from(liveInstances),
    mountChat(hostEl, options = {}) {
      return globalAPI.mount(hostEl, options, chatOnlyOption);
    },
    mount(hostEl, opts = {}, internalMode) {
      if (typeof hostEl === 'string') hostEl = doc.querySelector(hostEl);
      if (!hostEl || hostEl.nodeType !== 1 || typeof hostEl.getAttribute !== 'function' || typeof hostEl.appendChild !== 'function') {
        throw new Error('ihtml.mount: container element required');
      }
      if (mountedHosts.has(hostEl)) throw new Error('ihtml: this element is already mounted (destroy() it first)');
      if (mountingHosts.has(hostEl)) throw new Error('ihtml: this element is already being mounted');

      // Reserve before reading options. A getter (or future synchronous hook)
      // must not be able to re-enter mount() for the same host and create two
      // roots. All validation remains before createInstance mutates the DOM.
      mountingHosts.add(hostEl);
      let inst = null;
      try {
        if (internalMode === chatOnlyOption) {
          if (opts == null) opts = {};
          if (typeof opts !== 'object') throw new TypeError('ihtml.mountChat: options must be an object');
          opts = { ...opts, [chatOnlyOption]: true, standalone: false, routing: 'memory', menu: null, panel: false };
        }
        if (lib.runtime && typeof lib.runtime.normalizeMountOptions === 'function') {
          opts = lib.runtime.normalizeMountOptions(opts, location.href, !!(opts && opts.standalone));
        } else {
          if (opts == null) opts = {};
          if (typeof opts !== 'object') throw new TypeError('ihtml.mount: options must be an object');
          opts = { ...opts };
          const base = opts.base ? new URL(String(opts.base), location.href) : new URL('.', location.href);
          if (base.protocol !== 'http:' && base.protocol !== 'https:') throw new TypeError('ihtml.mount: base must use http: or https:');
          if (base.username || base.password) throw new TypeError('ihtml.mount: base must not contain credentials');
          base.search = '';
          base.hash = '';
          if (!base.pathname.endsWith('/')) base.pathname += '/';
          opts.base = base.href;
          if (!opts.standalone) opts.params = new URLSearchParams(opts.params || {});
          if (opts.storageKey != null) opts.storageKey = String(opts.storageKey);
          if (opts.scriptNonce != null) opts.scriptNonce = String(opts.scriptNonce).trim();
          if (Array.isArray(opts.messageOrigins)) opts.messageOrigins = opts.messageOrigins.slice();
          opts.routing = opts.routing || (opts.standalone ? 'hash' : 'memory');
          if (opts.routing !== 'memory' && opts.routing !== 'hash') throw new TypeError('ihtml.mount: routing must be "memory" or "hash"');
        }
        for (const name of ['onChatEvent', 'contextProvider', 'beforeConfirm', 'reviewFieldLabel', 'verificationLinks']) {
          if (opts[name] != null && typeof opts[name] !== 'function') throw new TypeError('ihtml: ' + name + ' must be a function');
        }
        if (opts.history != null && opts.history !== false) {
          if (!opts[chatOnlyOption] || typeof opts.history !== 'object' || typeof opts.history.list !== 'function' || typeof opts.history.select !== 'function' || (opts.history.remove != null && typeof opts.history.remove !== 'function') || (opts.history.onOpenChange != null && typeof opts.history.onOpenChange !== 'function') || (opts.history.description != null && (typeof opts.history.description !== 'string' || opts.history.description.length > 2048))) throw new TypeError('ihtml: history requires mountChat and list/select callbacks');
          opts.history = {...opts.history};
        }
        if (opts.starters != null) {
          if (!opts[chatOnlyOption] || !Array.isArray(opts.starters) || opts.starters.length > 4 || opts.starters.some(text => typeof text !== 'string' || !text.trim() || text.length > 160)) throw new TypeError('ihtml: starters requires at most four short nonempty strings in mountChat');
          opts.starters = opts.starters.slice();
        }
        if (opts.initialMessages != null) {
          if (!Array.isArray(opts.initialMessages) || opts.initialMessages.length > 100) throw new TypeError('ihtml: initialMessages must be an array of at most 100 messages');
          let bytes = 0;
          opts.initialMessages = opts.initialMessages.map((message) => {
            if (!message || !['user', 'assistant'].includes(message.role) || typeof message.content !== 'string') throw new TypeError('ihtml: initialMessages require user/assistant role and string content');
            bytes += new TextEncoder().encode(message.content).length;
            if (bytes > 512 * 1024) throw new TypeError('ihtml: initialMessages exceed 512 KiB');
            return { role: message.role, content: message.content, contentFormat: typeof message.contentFormat === 'string' ? message.contentFormat : 'text' };
          });
        }
        if (typeof opts.menu === 'string') doc.querySelector(opts.menu); // validate selector before createInstance mutates DOM
        else if (opts.menu && (opts.menu.nodeType !== 1 || typeof opts.menu.appendChild !== 'function')) {
          throw new TypeError('ihtml.mount: menu must be an element or selector');
        }
        if (opts.theme === 'inherit') opts.theme = probeHostTheme(hostEl);
        if (opts.routing === 'hash' && liveInstances.size > 0) {
          // location.hash 是页面级单资源, 第二个 hash 路由实例会互相踩踏。
          console.warn('[ihtml] multiple instances should use the default memory routing; hash routing is page-global');
        }
        inst = createInstance(hostEl, opts);
        mountedHosts.set(hostEl, inst);
        liveInstances.add(inst);
        const instDestroy = inst.destroy;
        let instanceDestroyed = false;
        inst.destroy = () => {
          if (instanceDestroyed) return;
          instanceDestroyed = true;
          const wasPrimary = globalAPI.__primary === inst;
          try {
            instDestroy();
          } finally {
            liveInstances.delete(inst);
            if (mountedHosts.get(hostEl) === inst) mountedHosts.delete(hostEl);
            if (wasPrimary || !globalAPI.__primary) syncGlobalPrimary(selectLivePrimary());
          }
        };
        syncGlobalPrimary(selectLivePrimary());
        return inst;
      } catch (error) {
        // A failure after createInstance returned still rolls back its DOM and
        // listeners. Preflight failures simply release the reservation so the
        // caller can correct the options and retry the same element.
        if (inst) {
          try { inst.destroy(); } catch { /* preserve the original mount error */ }
          liveInstances.delete(inst);
          if (mountedHosts.get(hostEl) === inst) mountedHosts.delete(hostEl);
        }
        throw error;
      } finally {
        mountingHosts.delete(hostEl);
      }
    },
  };
  function selectLivePrimary() {
    if (lib.runtime && typeof lib.runtime.selectPrimary === 'function') {
      return lib.runtime.selectPrimary(globalAPI.__primary, liveInstances);
    }
    return globalAPI.__primary && liveInstances.has(globalAPI.__primary)
      ? globalAPI.__primary
      : liveInstances.values().next().value || null;
  }
  function syncGlobalPrimary(primary) {
    if (primary) {
      for (const key of Object.keys(primary)) {
        if (compatibilityReservedKeys.has(key) || compatibilityProxyKeys.has(key)) continue;
        Object.defineProperty(globalAPI, key, {
          configurable: true,
          enumerable: true,
          get: () => globalAPI.__primary ? globalAPI.__primary[key] : undefined,
          set: (value) => { if (globalAPI.__primary) globalAPI.__primary[key] = value; },
        });
        compatibilityProxyKeys.add(key);
      }
    }
    globalAPI.__primary = primary || null;
    globalAPI.mounted = !!primary;
  }
  // Outside Item execution this remains the backward-compatible primary API.
  // While a marked classic script is synchronously evaluating, window.ihtml
  // resolves to its owner. Conflict-free inline code additionally gets a
  // lexical ihtml binding; external/explicit window.ihtml users must capture
  // that synchronous value rather than re-reading the global in a callback.
  try {
    Object.defineProperty(window, 'ihtml', {
      configurable: true,
      get: () => runtimeBridge.current() || globalAPI,
    });
  } catch { window.ihtml = globalAPI; }

  function declarativeOptions(d) {
    const o = {};
    if (d.base) o.base = d.base;
    if (d.routing) o.routing = d.routing;
    if (d.page) o.page = d.page;
    if (d.chat === '0') o.chatEntry = false;
    if (d.locale) o.locale = d.locale;
    if (d.safe === '1') o.safe = true;
    if (d.storageKey) o.storageKey = d.storageKey;
    const params = {};
    if (d.uid) params.uid = d.uid;
    if (d.params) { try { Object.assign(params, JSON.parse(d.params)); } catch { /* 忽略 */ } }
    o.params = params;
    if (d.theme === 'inherit') o.theme = 'inherit';
    else if (d.theme) { try { o.theme = JSON.parse(d.theme); } catch { /* 忽略 */ } }
    if (d.strings) { try { o.strings = JSON.parse(d.strings); } catch { /* 忽略 */ } }
    if (d.menu) o.menu = d.menu;
    return o;
  }

  /* 自动挂载: 独立页 #app[data-ihtml-standalone] 或全部声明式 [data-ihtml] */
  function autoMount() {
    const standaloneEl = doc.getElementById('app');
    if (!globalAPI.mounted && standaloneEl && standaloneEl.hasAttribute('data-ihtml-standalone')) {
      globalAPI.mount(standaloneEl, { standalone: true });
      return;
    }
    doc.querySelectorAll('[data-ihtml]').forEach((target) => {
      if (mountedHosts.has(target) || mountingHosts.has(target)) return;
      globalAPI.mount(target, declarativeOptions(target.dataset));
    });
  }
  autoMount();
  doc.addEventListener('DOMContentLoaded', autoMount);
})();
