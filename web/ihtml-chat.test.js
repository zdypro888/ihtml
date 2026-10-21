'use strict';

// Setup: npm ci && npx playwright install chromium.
// No live backend/provider is required; network and WebSocket traffic is fake.
const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const { chromium } = require('playwright');
let browser;
test.before(async () => { browser = await chromium.launch({ headless: true }); });
test.after(async () => { if (browser) await browser.close(); });

async function fixture(t, viewport = { width: 1100, height: 800 }, device = {}) {
  const page = await browser.newPage({ viewport, ...device });
  t.after(() => page.close());
  const failures = [];
  page.on('pageerror', (error) => failures.push(error.message));
  t.after(() => assert.deepEqual(failures, [], 'no uncaught browser errors'));
  await page.route('**/*', (route) => route.fulfill({ contentType: 'text/html', body: '<!doctype html><meta name="viewport" content="width=device-width, initial-scale=1"><title>Host</title><div id="a" style="height:640px"></div><div id="b" style="height:640px"></div><button id="outside">Outside</button>' }));
  await page.goto('https://host.test/host?token=URL_SECRET');
  await page.evaluate(() => {
    window.calls = []; window.sockets = []; window.events = [];
    window.identity = 'alice'; window.readOnly = false; window.configStatus = 200;
    window.agentModes = ['fast', 'deep'];
    window.confirmationScope = 'session';
    window.fetch = async (url, init) => {
      const pathname = new URL(url).pathname;
      calls.push({ url: String(url), headers: init.headers });
      const data = pathname.endsWith('/api/config')
        ? { user: identity, chat: true, read_only: readOnly, params: {} }
        : pathname.endsWith('/api/chat/models')
          ? { model_ready: true, model_select: false, models: [], agent_modes: agentModes, deep_approval_resume: 'manual_tool_only', confirmation_scope: confirmationScope }
          : {};
      const status = pathname.endsWith('/api/config') ? configStatus : 200;
      return { ok: status === 200, status, statusText: 'Unavailable', text: async () => JSON.stringify(data) };
    };
    class FakeSocket {
      static OPEN = 1;
      constructor(url) {
        this.url = String(url); this.sent = []; this.readyState = 0;
        sockets.push(this);
        queueMicrotask(() => { if (this.readyState !== 3) { this.readyState = 1; this.onopen?.(); } });
      }
      send(value) { if (this.readyState !== 1) throw new Error('closed'); this.sent.push(JSON.parse(value)); }
      close() { this.readyState = 3; this.onclose?.(); }
      emit(value) { this.onmessage?.({ data: JSON.stringify(value) }); }
    }
    window.WebSocket = FakeSocket;
    window.EventSource = class { constructor() { throw new Error('workspace SSE must not start'); } };
  });
  await page.addStyleTag({ path: path.join(__dirname, 'ihtml.css') });
  await page.addScriptTag({ path: path.join(__dirname, 'ihtml-lib.js') });
  await page.addScriptTag({ path: path.join(__dirname, 'ihtml.js') });
  return page;
}

test('deep-only capabilities hide Fast, replace stale saved Fast and send only Deep', async (t) => {
  const page = await fixture(t);
  await page.evaluate(async () => {
    window.a = ihtml.mountChat('#a', { storageKey: 'mode-contract' }); await a.ready();
    a.root.querySelector('[data-agent-mode="fast"]').click();
    a.destroy(); window.agentModes = ['deep'];
    window.a = ihtml.mountChat('#a', { storageKey: 'mode-contract' }); await a.ready();
  });
  for (const viewport of [{ width: 1100, height: 800 }, { width: 390, height: 844 }]) {
    await page.setViewportSize(viewport);
    assert.equal(await page.locator('#a [data-agent-mode="fast"]').isVisible(), false);
    assert.equal(await page.locator('#a [data-agent-mode="deep"]').isVisible(), true);
    assert.equal(await page.locator('#a [data-agent-mode="fast"]').evaluate(button => getComputedStyle(button).display), 'none');
  }
  assert.equal(await page.locator('#a [data-agent-mode="deep"]').getAttribute('aria-checked'), 'true');
  await page.locator('#a [data-agent-mode="deep"]').focus();
  await page.keyboard.press('ArrowRight');
  assert.equal(await page.locator('#a [data-agent-mode="deep"]').getAttribute('aria-checked'), 'true');
  const result = await page.evaluate(async () => {
    // Even a stale programmatic click cannot select an undeclared mode.
    a.root.querySelector('[data-agent-mode="fast"]').click();
    await a.chat.send('Deep only');
    return { mode: sockets[0].sent[0].agent_mode, selected: a.root.querySelector('[aria-checked="true"]').dataset.agentMode };
  });
  assert.deepEqual(result, { mode: 'deep', selected: 'deep' });
});

test('host review and confirmation check are safe, detached and preserve the executable card', async (t) => {
  const page = await fixture(t, { width: 390, height: 844 });
  await page.evaluate(async () => {
    window.a = ihtml.mountChat('#a', { reviewFieldLabel: path => path === 'amount' ? 'Reviewed amount' : path, beforeConfirm: ({toolCall, signal}) => {
      window.checked = toolCall; toolCall.body.amount = 999;
      window.checkSignal = signal;
      return new Promise(resolve => { window.resolveCheck = resolve; });
    } });
    await a.ready(); a.chat.draft('Draft before sending');
    if (sockets.length || !a.root.querySelector('textarea').value.includes('Draft before sending')) throw new Error('draft sent a request or lost text');
    await a.chat.send('Adjust');
    sockets[0].emit({type:'turn_start',run_id:'review',seq:1});
    sockets[0].emit({type:'confirmation_required',run_id:'review',seq:2,tool_call:{id:'review',name:'write',title:'Adjust',method:'POST',path:'/adjust',body:{amount:100},confirmation_token:'PRIVATE_TOKEN',confirmation_nonce:'n',review:{scope:'Player',before:{amount:20,api_key:'OLD_SECRET'},after:{amount:100,api_key:'NEW_SECRET',name:'<img src=x onerror=alert(1)>'}}}});
    sockets[0].emit({type:'assistant_done',run_id:'review',seq:3,message:'Ready'});
  });
  const review = page.locator('#a .t-review');
  await review.waitFor();
  assert.match(await review.innerText(), /20/);
  assert.match(await review.innerText(), /100/);
  assert.match(await review.innerText(), /Reviewed amount/);
  assert.doesNotMatch(await review.innerText(), /OLD_SECRET|NEW_SECRET|PRIVATE_TOKEN/);
  assert.equal(await review.locator('img').count(), 0);
  const bounds = await review.boundingBox();
  assert.ok(bounds.width <= 390, 'review stays within the mobile viewport');
  await page.locator('#a .t-actions .ok').click();
  await page.waitForFunction(() => !!window.resolveCheck);
  assert.equal(await page.evaluate(() => checked.confirmation_token), undefined);
  assert.equal(await page.evaluate(() => sockets[0].sent.length), 1);
  await page.evaluate(() => resolveCheck(false));
  await page.waitForFunction(() => !a.root.querySelector('.t-actions .ok').disabled);
  await page.locator('#a .t-actions .ok').click();
  await page.waitForFunction(() => !checkSignal.aborted);
  await page.evaluate(() => resolveCheck(true));
  await page.waitForFunction(() => sockets[0].sent.length === 2);
  const sent = await page.evaluate(() => sockets[0].sent[1].tool_call);
  assert.equal(sent.body.amount, 100);
  assert.equal(sent.confirmation_token, 'PRIVATE_TOKEN');
});

test('connection-scoped confirmation aborts the host check and expires on disconnect', async (t) => {
  const page = await fixture(t);
  await page.evaluate(async () => {
    confirmationScope = 'connection';
    window.a = ihtml.mountChat('#a', {beforeConfirm:({signal}) => {window.checkSignal = signal; return new Promise(resolve => {window.resolveCheck = resolve;});}});
    await a.ready();await a.chat.send('Propose');
    sockets[0].emit({type:'turn_start',run_id:'r',seq:1});
    sockets[0].emit({type:'confirmation_required',run_id:'r',seq:2,tool_call:{id:'c',name:'write',confirmation_nonce:'n',confirmation_token:'SIGNED'}});
    sockets[0].emit({type:'assistant_done',run_id:'r',seq:3,message:'Ready'});
  });
  await page.locator('#a .t-actions .ok').click();
  await page.waitForFunction(() => !!window.checkSignal);
  await page.evaluate(() => {sockets[0].close();resolveCheck(true);});
  await page.waitForFunction(() => checkSignal.aborted);
  assert.equal(await page.locator('#a .ihtml-tool[data-status="pending"]').count(), 0);
  assert.equal(await page.evaluate(() => sockets.length), 1);
  assert.equal(await page.evaluate(() => sockets[0].sent.length), 1);
});

for (const ending of ['disconnect', 'stop']) test('confirmed write stays unknown after ' + ending + ', with safe verification links', async t => {
  const page = await fixture(t);
  await page.evaluate(async () => {
    window.a = ihtml.mountChat('#a', {verificationLinks: call => {
      if (call.confirmation_token) throw new Error('hook must not receive signature');
      return [{label:'Check records',url:'/audit?player=42'}, {label:'Unsafe',url:'javascript:alert(1)'}, {label:'External',url:'https://elsewhere.test/'}];
    }});
    await a.ready();await a.chat.send('Write');
    sockets[0].emit({type:'turn_start',run_id:'r',seq:1});
    sockets[0].emit({type:'confirmation_required',run_id:'r',seq:2,tool_call:{id:'c',name:'write',method:'POST',path:'/adjust',confirmation_nonce:'n',confirmation_token:'SIGNED'}});
    sockets[0].emit({type:'assistant_done',run_id:'r',seq:3,message:'Ready'});
  });
  await page.locator('#a .t-actions .ok').click();
  await page.waitForFunction(() => sockets[0].sent.length === 2);
  await page.evaluate(ending => { if (ending === 'stop') a.chat.cancel(); else sockets[0].close(); }, ending);
  const card = page.locator('#a .ihtml-tool[data-status="unknown"]');
  await card.waitFor();
  assert.match(await card.innerText(), /Do not repeat/);
  assert.equal(await card.locator('.t-actions button').count(), 0);
  assert.equal(await card.locator('.t-verification a').count(), 1);
  assert.match(await card.locator('.t-verification a').getAttribute('href'), /host.test\/audit/);
  assert.equal(await page.evaluate(() => sockets.length), 1);
});

test('legacy missing or empty modes still select and send Fast', async (t) => {
  const page = await fixture(t);
  for (const modes of [undefined, null, []]) {
    const result = await page.evaluate(async (modes) => {
      window.agentModes = modes;
      window.a = ihtml.mountChat('#a'); await a.ready();
      await a.chat.send('Legacy');
      const result = { mode: sockets.at(-1).sent[0].agent_mode, modebarHidden: a.root.querySelector('.ihtml-agent-modebar').hidden };
      a.destroy(); return result;
    }, modes);
    assert.deepEqual(result, { mode: 'fast', modebarHidden: true });
  }
});

test('inline assistant shares protocol, isolates instances, sanitizes content and observes detached events', async (t) => {
  const page = await fixture(t);
  const result = await page.evaluate(async () => {
    window.a = ihtml.mountChat('#a', {
      base: '/ui/', auth: () => 'LONG_SECRET', connectionAuth: () => 'SHORT_TICKET',
      params: { auth: 'PARAM_SECRET' },
      initialMessages: [
        { role: 'user', content: '<img src=x onerror="window.pwned=1">' },
        { role: 'assistant', content: '<p>Initial</p><script>window.pwned=1</script><img src=x onerror="window.pwned=1">', contentFormat: 'html' },
      ],
      contextProvider: ({ context }) => ({ ...context, page: { path: 'https://user:PASS@host.test/data?token=SECRET#private', title: 'Business', visible_text: 'Evidence' }, auth: 'HIDDEN' }),
      onChatEvent: (event) => { events.push(event); event.message = 'MUTATED'; },
    });
    window.b = ihtml.mountChat('#b', { base: '/ui/' });
    await Promise.all([a.ready(), b.ready()]);
    const sent = await a.chat.send('Hello');
    const busyRejected = await a.chat.send('Duplicate');
    sockets[0].emit({ type: 'turn_start', run_id: 'r1', seq: 1 });
    sockets[0].emit({ type: 'assistant_delta', run_id: 'r1', seq: 2, content_format: 'html', delta: '<p>Draft</p>' });
    sockets[0].emit({ type: 'assistant_reset', run_id: 'r1', seq: 3 });
    sockets[0].emit({ type: 'assistant_done', run_id: 'r1', seq: 4, content_format: 'html', message: '<p>Final</p><script>window.pwned=1</script><a href="javascript:alert(1)" onclick="alert(1)">bad</a>', tool_debug: { authorization: 'SECRET' }, tool_call: { confirmation_token: 'SIGNED_SECRET' } });
    return {
      sent, busyRejected, busy: a.chat.busy, frame: sockets[0].sent[0], wsURL: sockets[0].url,
      endpoints: calls.map((call) => new URL(call.url).pathname), calls,
      events: JSON.stringify(events), html: a.root.innerHTML, text: a.root.innerText,
      otherText: b.root.innerText, pwned: window.pwned || 0,
      mask: !!a.root.querySelector('.ihtml-chat-mask'), app: !!a.root.querySelector('.ihtml-app'),
      role: a.root.querySelector('.ihtml-chat').getAttribute('role'), modal: a.root.querySelector('[aria-modal]') !== null,
    };
  });
  assert.equal(result.sent, true);
  assert.equal(result.busyRejected, false);
  assert.equal(result.busy, false);
  assert.deepEqual(result.frame.messages, [{ role: 'user', content: 'Hello' }]);
  assert.equal(result.frame.tools_enabled, true);
  assert.equal(result.frame.client_context.page.path, '/data');
  assert.equal(result.frame.client_context.page.visible_text, 'Evidence');
  assert.equal(result.frame.client_context.auth, undefined);
  assert.match(result.wsURL, /auth=SHORT_TICKET/);
  assert.doesNotMatch(result.wsURL, /LONG_SECRET|PARAM_SECRET|URL_SECRET/);
  assert.ok(result.calls.some((call) => call.headers['X-Ihtml-User'] === 'LONG_SECRET'));
  assert.ok(result.endpoints.every((endpoint) => ['/ui/api/config', '/ui/api/chat/models'].includes(endpoint)));
  assert.doesNotMatch(result.events, /SIGNED_SECRET|SECRET|tool_debug/);
  assert.match(result.text, /Final/);
  assert.doesNotMatch(result.text, /MUTATED|Draft/);
  assert.doesNotMatch(result.otherText, /Hello|Final|Initial/);
  assert.equal(result.pwned, 0);
  assert.equal(result.mask, false);
  assert.equal(result.app, false);
  assert.equal(result.role, 'region');
  assert.equal(result.modal, false);
  assert.equal(await page.locator('#a .answer script, #a .answer img, #a .answer [onclick], #a .answer a[href^="javascript:"]').count(), 0);
  if (process.env.IHTML_CHAT_SCREENSHOT) {
    await page.locator('#a').screenshot({ path: process.env.IHTML_CHAT_SCREENSHOT + '-desktop.png' });
    await page.setViewportSize({ width: 390, height: 800 });
    await page.waitForTimeout(150);
    assert.equal(await page.evaluate(() => {
      const hint = a.root.querySelector('.ihtml-agent-modebar .hint').getBoundingClientRect();
      const log = a.root.querySelector('.ihtml-chat-log').getBoundingClientRect();
      return hint.bottom <= log.top;
    }), true, 'wrapped mode hint must not overlap messages');
    await page.locator('#a').screenshot({ path: process.env.IHTML_CHAT_SCREENSHOT + '-mobile.png' });
  }
  await page.evaluate(() => { a.destroy(); b.destroy(); });
});

test('pending async context cannot cross identity, cancel, clear or destroy boundaries', async (t) => {
  const page = await fixture(t);
  await page.evaluate(async () => {
    window.waiters = [];
    window.a = ihtml.mountChat('#a', {
      contextProvider: ({ signal, user }) => new Promise((resolve) => waiters.push({ resolve, signal, user })),
    });
    await a.ready();
    window.pending = a.chat.send('old identity');
  });
  await page.waitForFunction(() => waiters.length === 1);
  const identity = await page.evaluate(async () => {
    window.identity = 'bob';
    await a.refreshAuth();
    waiters[0].resolve({ page: { visible_text: 'ALICE_PRIVATE' } });
    return { result: await pending, aborted: waiters[0].signal.aborted, sockets: sockets.length, user: a.user, text: a.root.innerText };
  });
  assert.equal(identity.result, false);
  assert.equal(identity.aborted, true);
  assert.equal(identity.sockets, 0);
  assert.equal(identity.user, 'bob');
  assert.doesNotMatch(identity.text, /old identity|ALICE_PRIVATE/);
  for (const operation of ['cancel', 'clear', 'destroy']) {
    await page.evaluate(() => { window.pending = a.chat.send('pending'); });
    await page.waitForFunction((count) => waiters.length === count, operation === 'cancel' ? 2 : operation === 'clear' ? 3 : 4);
    const result = await page.evaluate(async (operation) => {
      if (operation === 'destroy') a.destroy(); else a.chat[operation]();
      return { sent: await pending, aborted: waiters.at(-1).signal.aborted, sockets: sockets.length };
    }, operation);
    assert.equal(result.sent, false);
    assert.equal(result.aborted, true);
    assert.equal(result.sockets, 0);
  }
  assert.equal(await page.evaluate(() => ihtml.instances().length), 0);
});

test('getMessages exports detached display history only, replays without model history and clears on identity refresh', async (t) => {
  const page = await fixture(t);
  const result = await page.evaluate(async () => {
    window.a = ihtml.mountChat('#a', { initialMessages: [{ role: 'assistant', content: '**Earlier**', contentFormat: 'markdown' }] });
    await a.ready();
    const initial = a.chat.getMessages();
    await a.chat.send('Question');
    sockets[0].emit({ type: 'turn_start', run_id: 'r', seq: 1 });
    sockets[0].emit({ type: 'thinking_delta', run_id: 'r', seq: 2, delta: 'PRIVATE_REASONING' });
    sockets[0].emit({ type: 'assistant_delta', run_id: 'r', seq: 3, delta: 'Draft', content_format: 'text' });
    const draft = a.chat.getMessages();
    sockets[0].emit({ type: 'assistant_reset', run_id: 'r', seq: 4 });
    const reset = a.chat.getMessages();
    sockets[0].emit({ type: 'assistant_done', run_id: 'r', seq: 5, message: '<p>Final</p>', content_format: 'html', nonce: 'SECRET_NONCE' });
    const saved = a.chat.getMessages();
    const detached = a.chat.getMessages(); detached[0].content = 'MUTATED'; detached.push({ role: 'user', content: 'Injected' });
    const unaffected = a.chat.getMessages();
    a.destroy();
    window.b = ihtml.mountChat('#b', { initialMessages: saved }); await b.ready();
    const replayed = b.chat.getMessages();
    const notices = b.root.querySelectorAll('.ihtml-msg').length - replayed.length;
    await b.chat.send('Next');
    const frame = sockets[1].sent[0];
    b.chat.clear(); const cleared = b.chat.getMessages();
    b.destroy();
    window.a = ihtml.mountChat('#a', { initialMessages: saved }); await a.ready();
    window.identity = 'bob';
    const refreshing = a.refreshAuth(); const duringRefresh = a.chat.getMessages();
    await refreshing;
    const afterRefresh = a.chat.getMessages(); a.destroy();
    return { initial, draft, reset, saved, unaffected, replayed, notices, frame, cleared, duringRefresh, afterRefresh, destroyed: a.chat.getMessages() };
  });
  assert.deepEqual(result.initial, [{ role: 'assistant', content: '**Earlier**', contentFormat: 'markdown' }]);
  assert.equal(result.draft.at(-1).content, 'Draft');
  assert.equal(result.reset.length, 2);
  assert.deepEqual(result.saved.at(-1), { role: 'assistant', content: '<p>Final</p>', contentFormat: 'html' });
  assert.doesNotMatch(JSON.stringify(result.saved), /PRIVATE_REASONING|SECRET_NONCE/);
  assert.deepEqual(result.unaffected, result.saved);
  assert.deepEqual(result.replayed, result.saved);
  assert.equal(result.notices, 1);
  assert.deepEqual(result.frame.messages, [{ role: 'user', content: 'Next' }]);
  for (const key of ['cleared', 'duringRefresh', 'afterRefresh', 'destroyed']) assert.deepEqual(result[key], []);
});

test('getMessages is UTF-8 bounded to 100 messages and 512 KiB and can be remounted', async (t) => {
  const page = await fixture(t);
  const result = await page.evaluate(async () => {
    window.a = ihtml.mountChat('#a', { initialMessages: Array.from({ length: 100 }, (_, i) => ({ role: 'user', content: 'history-' + i })) });
    await a.ready(); await a.chat.send('Newest');
    const countBounded = a.chat.getMessages();
    sockets[0].emit({ type: 'assistant_done', message: '界'.repeat(200000), content_format: 'text' });
    const byteBounded = a.chat.getMessages();
    const bytes = byteBounded.reduce((sum, message) => sum + new TextEncoder().encode(message.content).length, 0);
    a.destroy(); window.b = ihtml.mountChat('#b', { initialMessages: byteBounded }); await b.ready();
    return { count: countBounded.length, first: countBounded[0].content, last: countBounded.at(-1).content, bytes, replay: b.chat.getMessages(), byteBounded };
  });
  assert.equal(result.count, 100);
  assert.equal(result.first, 'history-1');
  assert.equal(result.last, 'Newest');
  assert.ok(result.bytes <= 512 * 1024);
  assert.deepEqual(result.replay, result.byteBounded);
  assert.doesNotMatch(JSON.stringify(result.byteBounded), /\ufffd/);
});

test('checkpoint pause enables confirmation without false completion and restores cards', async (t) => {
  const page = await fixture(t);
  await page.evaluate(async () => {
    window.a = ihtml.mountChat('#a'); await a.ready(); await a.chat.send('Write');
    window.nativeCall = { id: 'native', name: 'write', risk: 'high', approval_resume: 'checkpoint', confirmation_nonce: 'n', confirmation_token: 'SIGNED', confirmation_expires: Math.floor(Date.now() / 1000) + 600, body: { value: 1 } };
    sockets[0].emit({ type: 'turn_start', run_id: 'pause', seq: 1 });
    sockets[0].emit({ type: 'confirmation_required', run_id: 'pause', seq: 2, tool_call: nativeCall });
    sockets[0].emit({ type: 'turn_paused', run_id: 'pause', seq: 3, status: 'Awaiting confirmation' });
  });
  assert.equal(await page.evaluate(() => a.chat.busy), false);
  assert.equal(await page.locator('#a .t-actions button:enabled').count(), 2);
  assert.equal(await page.locator('#a .ihtml-msg.assistant').last().evaluate(card => card._turn.done), false);
  await page.locator('#a .t-actions .ok').click();
  await page.waitForFunction(() => sockets[0].sent.some(frame => frame.type === 'confirm_tool'));
  assert.equal(await page.evaluate(() => a.chat.busy), true);
  await page.evaluate(() => {
    sockets[0].emit({ type: 'turn_start', run_id: 'resume', seq: 1 });
    sockets[0].emit({ type: 'tool_result', run_id: 'resume', seq: 2, tool_call: nativeCall, tool_status: 'success', tool_summary: 'Stored' });
    sockets[0].emit({ type: 'assistant_done', run_id: 'resume', seq: 3, message: 'Continued', content_format: 'text' });
    sockets[0].emit({ type: 'turn_start', run_id: 'restore', seq: 1, restored_approval: true });
    sockets[0].emit({ type: 'confirmation_required', run_id: 'restore', seq: 2, tool_call: { ...nativeCall, id: 'restored', confirmation_nonce: 'restored-nonce' } });
    sockets[0].emit({ type: 'turn_paused', run_id: 'restore', seq: 3 });
  });
  assert.equal(await page.locator('#a .ihtml-tool[data-status="success"]').count(), 1);
  assert.equal(await page.locator('#a .ihtml-tool[data-status="pending"]').count(), 1);
  assert.equal(await page.evaluate(() => a.chat.busy), false);
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(await page.locator('#a .t-actions button:enabled').count(), 2);
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.locator('#a').screenshot({ path: '/tmp/ihtml-native-approval-mobile.png' });
});

test('disconnect preserves suspended approval without false completion', async (t) => {
  const page = await fixture(t);
  await page.evaluate(async () => {
    window.a = ihtml.mountChat('#a'); await a.ready(); await a.chat.send('Write');
    sockets[0].emit({ type: 'turn_start', run_id: 'pause', seq: 1 });
    sockets[0].emit({ type: 'confirmation_required', run_id: 'pause', seq: 2, tool_call: { id: 'native', name: 'write', approval_resume: 'checkpoint', confirmation_nonce: 'n', confirmation_token: 'SIGNED', confirmation_expires: Math.floor(Date.now() / 1000) + 600 } });
    sockets[0].emit({ type: 'turn_paused', run_id: 'pause', seq: 3 });
    sockets[0].close();
  });
  assert.equal(await page.evaluate(() => a.chat.busy), false);
  assert.equal(await page.locator('#a .ihtml-msg.assistant').last().evaluate(card => card._turn.done), false);
  assert.equal(await page.locator('#a .t-actions button:enabled').count(), 2);
});

test('restored approval does not displace a foreground user request', async (t) => {
  const page = await fixture(t);
  await page.evaluate(async () => {
    window.a = ihtml.mountChat('#a'); await a.ready(); await a.chat.send('New request');
    sockets[0].emit({ type: 'turn_start', run_id: 'old', seq: 1, restored_approval: true });
    sockets[0].emit({ type: 'confirmation_required', run_id: 'old', seq: 2, tool_call: { id: 'old-card', name: 'write', approval_resume: 'checkpoint', confirmation_nonce: 'n', confirmation_token: 'SIGNED', confirmation_expires: Math.floor(Date.now() / 1000) + 600 } });
    sockets[0].emit({ type: 'turn_paused', run_id: 'old', seq: 3 });
    sockets[0].emit({ type: 'turn_start', run_id: 'new', seq: 1 });
    sockets[0].emit({ type: 'assistant_done', run_id: 'new', seq: 2, message: 'Foreground response', content_format: 'text' });
  });
  assert.equal(await page.locator('#a .answer').filter({ hasText: 'Foreground response' }).count(), 1);
  assert.equal(await page.locator('#a .ihtml-tool[data-status="pending"]').count(), 1);
  assert.equal(await page.evaluate(() => a.chat.busy), false);
});

test('manual_tool_only confirmation requires turn completion, reconnects and executes one historical card', async (t) => {
  const page = await fixture(t);
  await page.evaluate(async () => {
    window.a = ihtml.mountChat('#a'); await a.ready(); await a.chat.send('Two actions');
    window.manualCalls = [1, 2].map(id => ({ id: 'c' + id, name: 'write', risk: 'high', approval_resume: 'manual_tool_only', confirmation_nonce: 'n' + id, confirmation_token: 'SIGNED' + id, confirmation_expires: Math.floor(Date.now() / 1000) + 600, body: { value: id } }));
    sockets[0].emit({ type: 'turn_start', run_id: 'proposal', seq: 1 });
    manualCalls.forEach((tool_call, i) => sockets[0].emit({ type: 'confirmation_required', run_id: 'proposal', seq: i + 2, tool_call }));
  });
  assert.equal(await page.locator('#a .t-actions button:disabled').count(), 4);
  await page.evaluate(() => sockets[0].emit({ type: 'assistant_done', run_id: 'proposal', seq: 4, message: 'Awaiting explicit approval', content_format: 'text' }));
  assert.equal(await page.locator('#a .t-actions button:enabled').count(), 4);
  await page.evaluate(() => sockets[0].close());
  await page.locator('#a .t-actions .ok').first().click();
  await page.waitForFunction(() => sockets.length === 2 && sockets[1].sent.length === 1);
  const confirmed = await page.evaluate(() => ({ frame: sockets[1].sent[0], busy: a.chat.busy, statuses: Array.from(a.root.querySelectorAll('.ihtml-tool')).map(n => n.dataset.status), disabled: a.root.querySelectorAll('.t-actions button:disabled').length }));
  assert.equal(confirmed.frame.type, 'confirm_tool');
  assert.equal(confirmed.frame.tool_call.id, 'c1');
  assert.equal(confirmed.frame.tool_call.confirmation_token, 'SIGNED1');
  assert.equal(confirmed.frame.messages, undefined);
  assert.deepEqual(confirmed.statuses, ['running', 'pending']);
  assert.equal(confirmed.busy, true);
  assert.equal(confirmed.disabled, 2);
  await page.evaluate(() => {
    sockets[1].emit({ type: 'turn_start', run_id: 'confirmed', seq: 1 });
    sockets[1].emit({ type: 'tool_start', run_id: 'confirmed', seq: 2, tool_call: manualCalls[0] });
    sockets[1].emit({ type: 'tool_result', run_id: 'confirmed', seq: 3, tool_call: manualCalls[0], tool_status: 'success', tool_summary: 'Stored call executed' });
    sockets[1].emit({ type: 'assistant_done', run_id: 'confirmed', seq: 4, message: 'Only the approved tool executed', content_format: 'text' });
  });
  assert.deepEqual(await page.evaluate(() => ({ busy: a.chat.busy, statuses: Array.from(a.root.querySelectorAll('.ihtml-tool')).map(n => n.dataset.status) })), { busy: false, statuses: ['success', 'pending'] });
  await page.locator('#a .t-actions .ok').click();
  await page.waitForFunction(() => sockets[1].sent.length === 2);
  await page.evaluate(() => {
    sockets[1].emit({ type: 'turn_start', run_id: 'rejected', seq: 1 });
    sockets[1].emit({ type: 'tool_result', run_id: 'rejected', seq: 2, tool_call: manualCalls[1], tool_status: 'error', tool_summary: 'Approval rejected' });
    sockets[1].emit({ type: 'turn_error', run_id: 'rejected', seq: 3, error: 'Approval rejected' });
  });
  assert.equal(await page.evaluate(() => a.chat.busy), false);
  assert.equal(await page.locator('#a .ihtml-tool[data-status="error"]').count(), 1);
});

test('manual_tool_only cancellation matches id and nonce, waits for ACK, fails closed on timeout and expires locally', async (t) => {
  const page = await fixture(t);
  await page.evaluate(async () => {
    window.a = ihtml.mountChat('#a'); await a.ready(); await a.chat.send('Actions');
    window.manualCalls = [1, 2, 3, 4].map(id => ({ id: 'c' + id, name: 'write', approval_resume: 'manual_tool_only', confirmation_nonce: 'n' + id, confirmation_token: 'SIGNED' + id, confirmation_expires: Math.floor(Date.now() / 1000) + (id === 4 ? -1 : 600) }));
    sockets[0].emit({ type: 'turn_start', run_id: 'proposal', seq: 1 });
    manualCalls.forEach((tool_call, i) => sockets[0].emit({ type: 'confirmation_required', run_id: 'proposal', seq: i + 2, tool_call }));
    sockets[0].emit({ type: 'assistant_done', run_id: 'proposal', seq: 6, message: 'Approve or dismiss' });
  });
  assert.equal(await page.locator('#a .ihtml-tool[data-status="cancelled"]').count(), 1);
  await page.locator('#a .t-actions button:not(.ok)').first().click();
  await page.waitForFunction(() => sockets[0].sent.length === 2);
  assert.equal(await page.evaluate(() => sockets[0].sent[1].type), 'cancel_tool');
  assert.equal(await page.evaluate(() => a.chat.busy), true);
  await page.evaluate(() => sockets[0].emit({ type: 'cancel_tool_result', tool_id: 'c1', confirmation_nonce: 'WRONG', tool_status: 'cancelled' }));
  assert.equal(await page.evaluate(() => a.chat.busy), true);
  await page.evaluate(() => sockets[0].emit({ type: 'cancel_tool_result', tool_id: 'c1', confirmation_nonce: 'n1', tool_status: 'cancelled', tool_summary: 'Not executed' }));
  assert.equal(await page.evaluate(() => a.chat.busy), false);
  assert.equal(await page.locator('#a .ihtml-tool[data-status="cancelled"]').count(), 2);
  await page.locator('#a .t-actions button:not(.ok)').first().click();
  await page.waitForFunction(() => sockets[0].sent.length === 3);
  await page.evaluate(() => sockets[0].emit({ type: 'cancel_tool_result', tool_id: 'c2', confirmation_nonce: 'n2', tool_status: 'error', error: 'Authorization: Bearer PRIVATE_SECRET' }));
  assert.equal(await page.evaluate(() => a.chat.busy), false);
  assert.doesNotMatch(await page.locator('#a').innerText(), /PRIVATE_SECRET/);
  await page.evaluate(() => {
    const original = window.setTimeout;
    window.setTimeout = (fn, delay, ...args) => original(fn, delay === 12000 ? 50 : delay, ...args);
  });
  await page.locator('#a .t-actions button:not(.ok)').click();
  await page.waitForFunction(() => !a.chat.busy);
  assert.equal(await page.locator('#a .ihtml-tool[data-status="error"]').count(), 2);
  assert.equal(await page.locator('#a .t-actions').count(), 0);
});

test('connectionAuth in flight is revoked; clear renews session and destroy permits remount', async (t) => {
  const page = await fixture(t);
  await page.evaluate(async () => {
    window.a = ihtml.mountChat('#a', { connectionAuth: () => new Promise((resolve) => { window.ticketResolve = resolve; }) });
    await a.ready();
    window.pending = a.chat.send('Hello');
  });
  await page.waitForFunction(() => !!window.ticketResolve);
  assert.equal(await page.evaluate(async () => { a.destroy(); const sent = await pending; ticketResolve('TICKET'); return sent; }), false);
  const result = await page.evaluate(async () => {
    window.a = ihtml.mountChat('#a'); await a.ready();
    await a.chat.send('First');
    const first = new URL(sockets[0].url).searchParams.get('csid');
    a.chat.clear();
    await a.chat.send('Second');
    const second = new URL(sockets[1].url).searchParams.get('csid');
    a.destroy(); a.destroy();
    return { first, second, count: sockets.length, closed: sockets.every((socket) => socket.readyState === 3), roots: document.querySelectorAll('.ihtml-root').length, bridge: window[Symbol.for('ihtml.runtime.v1')].get(a.root.dataset.ihtmlInstance) };
  });
  assert.notEqual(result.first, result.second);
  assert.equal(result.count, 2);
  assert.equal(result.closed, true);
  assert.equal(result.roots, 0);
  assert.equal(result.bridge, null);
});

test('host context failures and timeouts release busy without exposing errors or sending credentials', async (t) => {
  const page = await fixture(t);
  const result = await page.evaluate(async () => {
    const originalTimeout = window.setTimeout;
    window.setTimeout = (fn, delay, ...args) => originalTimeout(fn, delay === 5000 ? 20 : delay, ...args);
    window.a = ihtml.mountChat('#a', { contextProvider: () => new Promise(() => {}) });
    await a.ready();
    const timeout = await a.chat.send('Timeout');
    const busy = a.chat.busy;
    a.destroy();
    window.a = ihtml.mountChat('#a', { contextProvider: () => { throw new Error('Bearer PRIVATE_CREDENTIAL'); } });
    await a.ready();
    const failure = await a.chat.send('Failure');
    let invalid = false;
    try { await a.chat.send({ type: 'confirm_tool' }); } catch (error) { invalid = error instanceof TypeError; }
    const text = a.root.innerText;
    a.destroy();
    return { timeout, busy, failure, invalid, text, sockets: sockets.length, enabled: a.chat.enabled, afterDestroy: await a.chat.send('Gone') };
  });
  assert.equal(result.timeout, false);
  assert.equal(result.busy, false);
  assert.equal(result.failure, false);
  assert.equal(result.invalid, true);
  assert.equal(result.sockets, 0);
  assert.equal(result.enabled, false);
  assert.equal(result.afterDestroy, false);
  assert.doesNotMatch(result.text, /PRIVATE_CREDENTIAL|Bearer/);
});

test('read-only still allows conversation, but confirmation is disabled; failed auth refresh fails closed', async (t) => {
  const page = await fixture(t);
  const result = await page.evaluate(async () => {
    readOnly = true;
    window.a = ihtml.mountChat('#a', { onChatEvent: () => { throw new Error('hook failure'); } });
    await a.ready();
    const sent = await a.chat.send('Read');
    sockets[0].emit({ type: 'turn_start', run_id: 'r', seq: 1 });
    const call = { id: 'tool1', name: 'ui_item_put', title: 'Write', method: 'PUT', path: '/api/items', body: {}, risk: 'high', requires_confirmation: true, confirmation_nonce: 'n', confirmation_token: 'TOKEN', confirmation_expires: Date.now() + 60000 };
    sockets[0].emit({ type: 'confirmation_required', run_id: 'r', seq: 2, tool_call: call });
    sockets[0].emit({ type: 'assistant_done', run_id: 'r', seq: 3, message: 'Waiting', content_format: 'text' });
    const buttonsDisabled = [...a.root.querySelectorAll('.t-actions button')].every((button) => button.disabled);
    configStatus = 401;
    try { await a.refreshAuth(); } catch { /* expected */ }
    return { sent, readOnly: a.chat.readOnly, buttonsDisabled, after: await a.chat.send('Should reject'), enabled: a.chat.enabled, text: a.root.innerText };
  });
  assert.equal(result.sent, true);
  assert.equal(result.buttonsDisabled, true);
  assert.equal(result.after, false);
  assert.equal(result.enabled, false);
  assert.doesNotMatch(result.text, /Waiting|TOKEN/);
  await page.evaluate(() => a.destroy());
});

test('legacy mount keeps the drawer and shares public send/context APIs', async (t) => {
  const page = await fixture(t);
  const result = await page.evaluate(async () => {
    window.contextCalls = 0;
    window.a = ihtml.mount('#a', { safe: true, theme: { mode: 'light' }, initialMessages: [{ role: 'assistant', content: 'Legacy initial' }], contextProvider: ({ context }) => {
      if (++contextCalls === 2) return new Promise((resolve) => { window.contextResolve = resolve; });
      return context;
    } });
    await a.ready(); a.chat.open();
    const role = a.root.querySelector('.ihtml-chat').getAttribute('role');
    const modal = a.root.querySelector('.ihtml-chat').getAttribute('aria-modal');
    const mask = a.root.querySelector('.ihtml-chat-mask');
    const sent = await a.chat.send('Legacy');
    a.chat.cancel(); a.chat.close();
    return { role, modal, mask: !!mask, display: mask.style.display, sent, app: !!a.root.querySelector('.ihtml-app'), initial: a.root.innerText };
  });
  assert.equal(result.role, 'dialog');
  assert.equal(result.modal, 'true');
  assert.equal(result.mask, true);
  assert.equal(result.display, 'none');
  assert.equal(result.sent, true);
  assert.equal(result.app, true);
  await page.evaluate(() => { window.pending = a.chat.send('Old legacy identity'); });
  await page.waitForFunction(() => !!window.contextResolve);
  const switched = await page.evaluate(async () => {
    identity = 'bob'; await a.refreshConfig();
    contextResolve({});
    return { sent: await pending, user: a.user, sockets: sockets.length };
  });
  assert.equal(switched.sent, false);
  assert.equal(switched.user, 'bob');
  assert.equal(switched.sockets, 1);
  await page.evaluate(() => a.destroy());
});

test('preflight rejects invalid input without DOM mutation and inline layout fits desktop/mobile', async (t) => {
  const page = await fixture(t);
  const result = await page.evaluate(async () => {
    const errors = [];
    for (const options of [{ base: 'https://user:pass@host.test' }, { contextProvider: true }, { onChatEvent: 'bad' }, { initialMessages: [{ role: 'system', content: 'bad' }] }]) {
      try { ihtml.mountChat('#a', options); } catch (error) { errors.push(error.message); }
    }
    const clean = document.querySelectorAll('.ihtml-root').length === 0;
    let reentrantRejected = false;
    const options = { get base() {
      try { ihtml.mountChat('#a'); } catch { reentrantRejected = true; }
      return '/';
    } };
    window.a = ihtml.mountChat('#a', options); await a.ready();
    const duplicate = (() => { try { ihtml.mount('#a'); return false; } catch { return true; } })();
    return { errors, clean, duplicate, reentrantRejected };
  });
  assert.equal(result.errors.length, 4);
  assert.equal(result.clean, true);
  assert.equal(result.duplicate, true);
  assert.equal(result.reentrantRejected, true);
  for (const width of [1100, 390]) {
    await page.setViewportSize({ width, height: 800 });
    await page.waitForTimeout(150);
    const layout = await page.evaluate(() => {
      const panel = a.root.querySelector('.ihtml-chat');
      const foot = panel.querySelector('.ihtml-chat-foot');
      const bounds = panel.getBoundingClientRect(); const footer = foot.getBoundingClientRect();
      const hint = panel.querySelector('.ihtml-agent-modebar .hint').getBoundingClientRect();
      const log = panel.querySelector('.ihtml-chat-log').getBoundingClientRect();
      return { width: bounds.width, height: bounds.height, bottom: bounds.bottom, footerBottom: footer.bottom, overflow: panel.scrollWidth > panel.clientWidth, position: getComputedStyle(panel).position, overlaps: hint.bottom > log.top };
    });
    assert.ok(layout.width <= width);
    assert.equal(layout.height, 640);
    assert.ok(layout.footerBottom <= layout.bottom + 1);
    assert.equal(layout.overflow, false);
    assert.equal(layout.position, 'relative');
    assert.equal(layout.overlaps, false);
  }
  await page.evaluate(() => a.destroy());
});

for (const mode of ['desktop', 'phone', 'narrow-embed']) {
  test('browser interaction and rich-content layout: ' + mode, async (t) => {
    const phone = mode === 'phone';
    const page = await fixture(t, phone ? { width: 390, height: 844 } : { width: 1280, height: 900 },
      phone ? { isMobile: true, hasTouch: true, deviceScaleFactor: 3 } : {});
    await page.evaluate(async (mode) => {
      document.body.style.margin = '0';
      document.querySelector('#b').remove();
      const host = document.querySelector('#a');
      host.style.width = mode === 'narrow-embed' ? '320px' : '100%';
      host.style.height = mode === 'phone' ? '100dvh' : '720px';
      window.a = ihtml.mountChat(host, { locale: 'zh-CN', storageKey: mode });
      await a.ready();
    }, mode);
    const input = page.locator('#a textarea[name="ihtml-composer"]');
    await input.fill('检查当前业务');
    await input.press('Enter');
    await page.waitForFunction(() => sockets.length === 1 && sockets[0].sent.length === 1);
    assert.equal(await page.evaluate(() => a.chat.busy), true);
    await page.evaluate(() => {
      const socket = sockets[0];
      socket.emit({ type: 'turn_start', run_id: 'browser-run', seq: 1 });
      socket.emit({ type: 'assistant_thinking', run_id: 'browser-run', seq: 2, thinking_id: 'think', thinking: '正在核对数据。', thinking_done: true });
      socket.emit({ type: 'tool_start', run_id: 'browser-run', seq: 3, tool_name: 'business_summary', tool_title: '查询业务摘要', tool_call: { id: 'read-1', name: 'business_summary', method: 'GET', path: '/api/summary', risk: 'low' } });
      socket.emit({ type: 'tool_result', run_id: 'browser-run', seq: 4, tool_name: 'business_summary', tool_title: '查询业务摘要', tool_status: 'success', tool_summary: '已核对 2 项数据', tool_call: { id: 'read-1', name: 'business_summary' }, tool_result: { count: 2 } });
      socket.emit({ type: 'assistant_done', run_id: 'browser-run', seq: 5, content_format: 'html', message: '<h2>业务检查结果</h2><p>两项数据已核对，当前没有待处理异常。</p><table><thead><tr><th>项目</th><th>状态</th></tr></thead><tbody><tr><td>用户会话</td><td>正常</td></tr><tr><td>数据同步</td><td>已完成</td></tr></tbody></table><pre><code>' + 'very_long_field_name_'.repeat(24) + '</code></pre>' });
    });
    assert.equal(await page.evaluate(() => a.chat.busy), false);
    await page.waitForTimeout(200);
    const layout = await page.evaluate(() => {
      const panel = a.root.querySelector('.ihtml-chat');
      const log = panel.querySelector('.ihtml-chat-log');
      const head = panel.querySelector('.ihtml-chat-head');
      const modebar = panel.querySelector('.ihtml-agent-modebar');
      const foot = panel.querySelector('.ihtml-chat-foot');
      const box = (node) => node.getBoundingClientRect();
      return {
        width: box(panel).width, viewport: innerWidth,
        overflow: a.root.scrollWidth > a.root.clientWidth || document.documentElement.scrollWidth > innerWidth,
        headerOverlap: box(head).bottom > box(modebar).top + 1,
        modeOverlap: box(modebar).bottom > box(log).top + 1,
        footerOverlap: box(log).bottom > box(foot).top + 1,
        footerOutside: box(foot).bottom > box(panel).bottom + 1,
        height: box(panel).height,
        frame: sockets[0].sent[0], answer: panel.querySelector('.answer:last-child')?.textContent,
      };
    });
    assert.equal(layout.overflow, false);
    assert.equal(layout.headerOverlap, false);
    assert.equal(layout.modeOverlap, false);
    assert.equal(layout.footerOverlap, false);
    assert.equal(layout.footerOutside, false);
    assert.ok(layout.width <= layout.viewport);
    assert.equal(layout.frame.messages[0].content, '检查当前业务');
    assert.equal(layout.frame.client_context.viewport.mode, mode === 'desktop' ? 'pc' : 'mobile');
    if (process.env.IHTML_CHAT_SCREENSHOT) {
      await page.locator('#a').screenshot({ path: process.env.IHTML_CHAT_SCREENSHOT + '-verified-' + mode + '.png' });
    }
    // Public open/close is non-modal in inline mode and must not trap host focus.
    await page.evaluate(() => { a.chat.close(); document.querySelector('#outside').focus(); });
    assert.equal(await page.evaluate(() => document.activeElement.id), 'outside');
    await page.evaluate(() => a.chat.open());
    assert.equal(await input.evaluate((node) => document.activeElement === node), true);
    await page.evaluate(() => a.destroy());
  });
}

test('latest button ignores short conversations and tiny scrolling, then clears on return', async (t) => {
  const page = await fixture(t);
  await page.evaluate(async () => {
    window.a = ihtml.mountChat('#a', { locale: 'zh-CN' }); await a.ready();
    const log = a.root.querySelector('.ihtml-chat-log');
    log.dispatchEvent(new WheelEvent('wheel', { deltaY: -20 }));
  });
  const latest = page.locator('#a .ihtml-chat-latest');
  assert.equal(await latest.isVisible(), false);
  await page.evaluate(async () => {
    await a.chat.send('scroll test');
    sockets[0].emit({ type: 'turn_start', run_id: 'latest', seq: 1 });
    sockets[0].emit({ type: 'assistant_delta', run_id: 'latest', seq: 2, delta: 'Long conversation.\n\n'.repeat(100) });
  });
  await page.waitForFunction(() => { const log=a.root.querySelector('.ihtml-chat-log'); return log.scrollHeight>log.clientHeight+400 && log.scrollHeight-log.clientHeight-log.scrollTop<2; });
  const log = page.locator('#a .ihtml-chat-log');
  await log.evaluate(node => { node.dispatchEvent(new WheelEvent('wheel', { deltaY: -20 })); node.scrollTop -= 20; node.dispatchEvent(new Event('scroll')); });
  assert.equal(await latest.isVisible(), false);
  await log.evaluate(node => { node.scrollTop -= 200; node.dispatchEvent(new Event('scroll')); });
  await latest.waitFor({ state: 'visible' });
  await log.evaluate(node => { node.scrollTop = node.scrollHeight - node.clientHeight - 70; node.dispatchEvent(new Event('scroll')); });
  assert.equal(await latest.isVisible(), true, 'hysteresis avoids threshold flicker');
  await log.evaluate(node => { node.scrollTop = node.scrollHeight; node.dispatchEvent(new Event('scroll')); });
  await latest.waitFor({ state: 'hidden' });
  await page.evaluate(() => a.destroy());
});

test('stream follows rendered growth, pauses for readers and resumes explicitly across host resize', async (t) => {
  const page = await fixture(t, { width: 390, height: 740 });
  await page.evaluate(async () => {
    document.querySelector('#b').remove();
    const host = document.querySelector('#a'); host.style.height = '540px';
    window.a = ihtml.mountChat(host, { locale: 'zh-CN', storageKey: 'scroll-regression' }); await a.ready();
    await a.chat.send('本机滚动测试');
    sockets[0].emit({ type: 'turn_start', run_id: 'scroll-run', seq: 1 });
    sockets[0].emit({ type: 'assistant_delta', run_id: 'scroll-run', seq: 2, content_format: 'markdown', delta: '第一批段落。\n\n'.repeat(50) });
  });
  await page.waitForFunction(() => {
    const log = a.root.querySelector('.ihtml-chat-log');
    return log.scrollHeight > log.clientHeight + 300 && log.scrollHeight - log.clientHeight - log.scrollTop < 2;
  });
  // 最后一批 token 延迟渲染后没有更多事件，仍必须滚到真正的新末尾。
  await page.evaluate(() => sockets[0].emit({ type: 'assistant_delta', run_id: 'scroll-run', seq: 3, delta: '最后一批段落。\n\n'.repeat(40) }));
  await page.waitForFunction(() => a.root.textContent.includes('最后一批段落') && (() => { const log=a.root.querySelector('.ihtml-chat-log'); return log.scrollHeight-log.clientHeight-log.scrollTop<2; })());
  const log = page.locator('#a .ihtml-chat-log');
  await log.evaluate(node => node.dispatchEvent(new WheelEvent('wheel', { deltaY: -1 })));
  assert.equal(await page.locator('#a .ihtml-chat-latest').isVisible(), false, 'intent at the bottom is not unread distance');
  await log.evaluate(node => { node.scrollTop -= 20; node.dispatchEvent(new Event('scroll')); });
  assert.equal(await page.locator('#a .ihtml-chat-latest').isVisible(), false, 'tiny scroll stays quiet');
  await log.hover(); await page.mouse.wheel(0, -600);
  await page.locator('#a .ihtml-chat-latest').waitFor({ state: 'visible' });
  const before = await log.evaluate(node => node.scrollTop);
  await page.evaluate(() => sockets[0].emit({ type: 'assistant_delta', run_id: 'scroll-run', seq: 4, delta: '阅读期间新增。\n\n'.repeat(20) }));
  await page.waitForFunction(() => a.root.textContent.includes('阅读期间新增'));
  assert.ok(Math.abs(await log.evaluate(node => node.scrollTop) - before) < 2, 'upward reading is not pulled to bottom');
  await log.evaluate(node => { node.scrollTop = node.scrollHeight; node.dispatchEvent(new Event('scroll')); });
  await page.locator('#a .ihtml-chat-latest').waitFor({ state: 'hidden' });
  await log.hover(); await page.mouse.wheel(0, -600);
  await page.locator('#a .ihtml-chat-latest').waitFor({ state: 'visible' });
  await page.locator('#a .ihtml-chat-latest').click();
  await page.waitForFunction(() => { const log=a.root.querySelector('.ihtml-chat-log');return log.scrollHeight-log.clientHeight-log.scrollTop<2; });
  await page.evaluate(() => { document.querySelector('#a').style.height='220px'; });
  await page.waitForFunction(() => {
    const panel=a.root.querySelector('.ihtml-chat').getBoundingClientRect(), foot=a.root.querySelector('.ihtml-chat-foot').getBoundingClientRect();
    const log=a.root.querySelector('.ihtml-chat-log');return foot.bottom<=panel.bottom+1 && log.scrollHeight-log.clientHeight-log.scrollTop<2;
  });
  assert.equal(await page.locator('#a .ihtml-chat-foot textarea').isVisible(), true);
  await page.evaluate(() => a.destroy());
});


test('reasoning replaces the transient working placeholder instead of displaying it twice', async (t) => {
  const page = await fixture(t);
  await page.evaluate(async () => {
    window.a=ihtml.mountChat('#a',{locale:'zh-CN'});await a.ready();await a.chat.send('本机分析展示测试');
    sockets[0].emit({type:'turn_start',run_id:'reason-layout',seq:1});
    sockets[0].emit({type:'assistant_thinking',run_id:'reason-layout',seq:2,thinking_id:'one',thinking:'正在检查本机模拟状态。'});
  });
  await page.waitForFunction(()=>a.root.querySelector('.think-body')?.textContent.includes('本机模拟状态'));
  assert.equal(await page.locator('#a .pbody .ihtml-pline[data-persistent="false"]').count(),0);
  assert.equal(await page.locator('#a .ihtml-think').count(),1);
  await page.evaluate(()=>a.destroy());
});


test('generic working status appears only in summary and distinct details remain visible', async (t) => {
 const page=await fixture(t);
 await page.evaluate(async()=>{window.a=ihtml.mountChat('#a',{locale:'zh-CN'});await a.ready();await a.chat.send('本机状态测试');sockets[0].emit({type:'turn_start',run_id:'status-once',seq:1});});
 assert.equal(await page.locator('#a .pbody').isVisible(),false);
 assert.equal(await page.locator('#a .ihtml-pline:visible').count(),0);
 await page.evaluate(()=>sockets[0].emit({type:'assistant_status',run_id:'status-once',seq:2,status:'正在读取本机模拟数据'}));
 assert.equal(await page.locator('#a .p-title').textContent(),'正在读取本机模拟数据');
 assert.equal(await page.locator('#a .ihtml-pline:visible').count(),0);
 await page.evaluate(()=>a.destroy());
});


test('trace defaults to compact history while signed confirmations stay outside and visible', async(t)=>{
 const page=await fixture(t,{width:393,height:740});
 await page.evaluate(async()=>{window.a=ihtml.mountChat('#a',{locale:'zh-CN'});await a.ready();await a.chat.send('本机流程测试');sockets[0].emit({type:'turn_start',run_id:'compact',seq:1});sockets[0].emit({type:'assistant_thinking',run_id:'compact',seq:2,thinking_id:'summary',thinking:'只显示服务端提供的分析摘要'});sockets[0].emit({type:'confirmation_required',run_id:'compact',seq:3,tool_call:{id:'test-write',name:'fixture_save',title:'保存模拟设置',method:'PUT',path:'/fixture',body:{设置:'本机模拟'},risk:'low',confirmation_token:'fixture',confirmation_nonce:'fixture'}});sockets[0].emit({type:'assistant_done',run_id:'compact',seq:4,message:'等待确认。'});});
 assert.equal(await page.locator('#a .ihtml-process').getAttribute('open'),null);
 assert.equal(await page.locator('#a .ihtml-process .ihtml-tool').count(),0);
 assert.equal(await page.locator('#a .ihtml-trace-actions .ihtml-tool[data-status="pending"]').isVisible(),true);
 assert.equal(await page.getByRole('button',{name:'确认此操作 · 保存模拟设置',exact:true}).isEnabled(),true);
 assert.equal(await page.locator('#a .think-body').isVisible(),false);
 await page.evaluate(()=>a.destroy());
});

test('explicit trace and reasoning expansion survive streamed answers without being folded',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{window.a=ihtml.mountChat('#a',{locale:'zh-CN'});await a.ready();await a.chat.send('本机摘要');sockets[0].emit({type:'turn_start',run_id:'pinned',seq:1});sockets[0].emit({type:'assistant_thinking',run_id:'pinned',seq:2,thinking_id:'one',thinking:'已提供的分析摘要。'});});
 await page.locator('#a .ihtml-process > summary').click();
 await page.locator('#a .ihtml-think > summary').click();
 await page.evaluate(()=>{sockets[0].emit({type:'assistant_delta',run_id:'pinned',seq:3,delta:'正式回答开始。'});sockets[0].emit({type:'assistant_done',run_id:'pinned',seq:4,message:'正式回答完成。'});});
 assert.notEqual(await page.locator('#a .ihtml-process').getAttribute('open'),null);
 assert.notEqual(await page.locator('#a .ihtml-think').getAttribute('open'),null);
 assert.equal(await page.locator('#a .think-body').isVisible(),true);
 assert.equal(await page.locator('#a .ihtml-chat-latest').isVisible(),false,'expanded short trace does not imply hidden messages');
 const frozen=await page.locator('#a .p-meta').textContent();await page.waitForTimeout(1200);assert.equal(await page.locator('#a .p-meta').textContent(),frozen);
 await page.evaluate(()=>a.destroy());
});

test('reference-only tool events retain exact target and tool_id deduplicates within its own turn', async (t) => {
  const page = await fixture(t);
  await page.evaluate(async () => {
    window.a = ihtml.mountChat('#a', {locale:'zh-CN'}); await a.ready();
    await a.chat.send('first');
    const s = sockets[0];
    s.emit({type:'turn_start',run_id:'first',seq:1});
    s.emit({type:'confirmation_required',run_id:'first',seq:2,tool_call:{id:'one',name:'api_test',title:'更新 fixture-one',method:'PUT',path:'/fixture',body:{目标:'fixture-one'},confirmation_nonce:'n1'}});
    s.emit({type:'tool_start',run_id:'first',seq:3,tool_call:{id:'one',confirmation_nonce:'n1'},tool_name:'api_test'});
    s.emit({type:'tool_result',run_id:'first',seq:4,tool_id:'one',tool_name:'api_test',tool_status:'success',tool_summary:'fixture-one 已完成'});
    s.emit({type:'assistant_done',run_id:'first',seq:5,message:'first done'});
    await a.chat.send('second');
    s.emit({type:'turn_start',run_id:'second',seq:1});
    s.emit({type:'tool_start',run_id:'second',seq:2,tool_id:'two',tool_name:'api_test',tool_title:'更新 fixture-two'});
    s.emit({type:'tool_result',run_id:'second',seq:3,tool_id:'two',tool_name:'api_test',tool_status:'success',tool_summary:'fixture-two 已完成'});
    s.emit({type:'assistant_done',run_id:'second',seq:4,message:'second done'});
  });
  assert.equal(await page.locator('#a .ihtml-tool').count(), 2);
  assert.equal(await page.locator('#a .t-title').first().textContent(), '更新 fixture-one');
  const turns = await page.locator('#a .ihtml-process').allTextContents();
  assert.equal(turns.length, 2);
  assert.ok(turns.every(text => text.includes('1 条工具记录')));
  assert.ok(!turns[1].includes('fixture-one'));
});

test('result explanation resets only the draft and preserves the confirmed receipt and collapsed history', async (t) => {
  const page = await fixture(t);
  await page.evaluate(async () => {
    window.a = ihtml.mountChat('#a', {locale:'zh-CN'}); await a.ready(); await a.chat.send('update virtual settings');
    const s=sockets[0];
    s.emit({type:'turn_start',run_id:'proposal',seq:1});
    s.emit({type:'confirmation_required',run_id:'proposal',seq:2,tool_call:{id:'update',name:'api_update',title:'更新虚拟设置',method:'PUT',path:'/fixture',body:{目标:'fixture'},confirmation_nonce:'virtual',approval_resume:'manual_tool_only'}});
    s.emit({type:'assistant_done',run_id:'proposal',seq:3,message:'等待确认，尚未执行。'});
  });
  await page.getByRole('button',{name:'确认此操作 · 更新虚拟设置',exact:true}).click();
  await page.evaluate(() => {
    const s=sockets[0];
    s.emit({type:'turn_start',run_id:'receipt',seq:1});
    s.emit({type:'tool_result',run_id:'receipt',seq:2,tool_id:'update',tool_status:'success',tool_summary:'fixture 已保存'});
    s.emit({type:'assistant_reset',run_id:'receipt',seq:3});
    s.emit({type:'assistant_status',run_id:'receipt',seq:4,status:'正在整理执行结果'});
    s.emit({type:'assistant_delta',run_id:'receipt',seq:5,delta:'已更新 fixture 的虚拟设置。',content_format:'text'});
    s.emit({type:'assistant_done',run_id:'receipt',seq:6,message:'已更新 fixture 的虚拟设置。没有执行其他操作。',finish_reason:'tool_result_explained'});
  });
  assert.equal(await page.locator('#a .ihtml-tool').count(),1);
  assert.equal(await page.locator('#a .t-title').textContent(),'更新虚拟设置');
  assert.equal(await page.locator('#a .ihtml-process').evaluate(el=>el.open),false);
  const text=await page.locator('#a .ihtml-chat-log').innerText();
  assert.ok(text.includes('已更新 fixture 的虚拟设置。没有执行其他操作。'));
  assert.ok(text.includes('fixture 已保存'));
  assert.ok(!text.includes('等待确认，尚未执行。'));
  assert.equal(await page.locator('#a .ihtml-trace-actions').evaluate(node =>
    !!(node.compareDocumentPosition(node.parentElement.querySelector('.answer')) & Node.DOCUMENT_POSITION_FOLLOWING)), true,
    'confirmation and receipt remain before the streamed final explanation');
});

for (const order of ['keydown-first', 'compositionend-first']) {
  test('IME commit does not send or leave an extra newline; next Enter sends: ' + order, async (t) => {
    const page = await fixture(t);
    await page.evaluate(async () => {
      window.a = ihtml.mountChat('#a', { storageKey: 'ime-enter-regression' }); await a.ready();
    });
    const input = page.locator('#a textarea[name="ihtml-composer"]');
    await input.fill('中文消息');
    const state = await input.evaluate((input, order) => {
      input.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true }));
      const end = () => input.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true, data: '消息' }));
      if (order === 'compositionend-first') end();
      const commit = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true, isComposing: order === 'keydown-first' });
      input.dispatchEvent(commit);
      if (order === 'keydown-first') end();
      input.dispatchEvent(new KeyboardEvent('keyup', { key: 'Enter', bubbles: true }));
      return { value: input.value, sent: sockets.reduce((n, socket) => n + socket.sent.length, 0), prevented: commit.defaultPrevented };
    }, order);
    assert.equal(state.sent, 0, 'candidate confirmation never sends');
    assert.equal(state.value, '中文消息');
    assert.equal(state.prevented, order === 'compositionend-first', 'ended composition consumes the trailing Enter instead of inserting a newline');
    await input.press('Shift+Enter');
    assert.equal(await input.inputValue(), '中文消息\n');
    await input.press('Backspace');
    await input.press('Enter');
    await page.waitForFunction(() => sockets.length === 1 && sockets[0].sent.length === 1);
    assert.equal(await input.inputValue(), '');
  });
}

test('mountChat host transport owns requests/socket without fetch or native WebSocket fallback', async (t) => {
  const page = await fixture(t);
  const out = await page.evaluate(async () => {
    const request = window.fetch, NativeFake = window.WebSocket;
    window.fetch = () => { throw new Error('native fetch forbidden'); };
    window.WebSocket = class { static OPEN = 1; constructor() { throw new Error('native WS forbidden'); } };
    const paths = [];
    window.a = ihtml.mountChat('#a', { storageKey: 'native-host', hostTransport: {
      request(path, init) { paths.push(path); return request(new URL(path, 'https://host.test/'), init); },
      open() { return new NativeFake('host-owned'); },
    } });
    await a.ready(); await a.chat.send('one');
    sockets[0].emit({ type: 'assistant_delta', text: 'partial' });
    sockets[0].emit({ type: 'assistant', text: 'final', format: 'text' });
    sockets[0].emit({ type: 'done' });
    const first = sockets[0];
    a.chat.clear();
    await a.chat.send('two');
    const result = { paths, firstClosed: first.readyState === 3, sockets: sockets.length, text: sockets[1].sent[0].text };
    a.destroy(); result.closed = sockets.every(s => s.readyState === 3);
    return result;
  });
  assert.ok(out.paths.includes('api/config') && out.paths.includes('api/chat/models'));
  assert.equal(out.firstClosed, true); assert.equal(out.sockets, 2); assert.equal(out.closed, true);
});
test('host transport is complete and chat-only; invalid hooks never fall back to network', async (t) => {
 const page=await fixture(t);
 const out=await page.evaluate(()=>{
  let rejected=0;
  for(const run of [()=>ihtml.mountChat('#a',{hostTransport:{request(){}}}),()=>ihtml.mount('#a',{hostTransport:{request(){},open(){}}})]) {
   try{run()}catch(e){if(e instanceof TypeError)rejected++;}
  }
  return {rejected,requests:calls.length,sockets:sockets.length};
 });
 assert.deepEqual(out,{rejected:2,requests:0,sockets:0});
});
test('failed host request cannot fall back to default network transport', async(t)=>{
 const page=await fixture(t);
 const result=await page.evaluate(async()=>{
  window.a=ihtml.mountChat('#a',{hostTransport:{request(){throw new Error('host unavailable')},open(){throw new Error('unexpected open')}}});
  await a.ready();const sent=await a.chat.send('blocked');a.destroy();
  return {requests:calls.length,sockets:sockets.length,sent};
 });
 assert.deepEqual(result,{requests:0,sockets:0,sent:false});
});

test('signed card presentation is bounded plain text, falls back safely and preserves confirmation frames', async (t) => {
  const page = await fixture(t);
  await page.evaluate(async () => {
    window.a = ihtml.mountChat('#a'); await a.ready(); await a.chat.send('Prepare');
    window.presentedCall = {id:'presented',name:'write',title:'Business action',method:'POST',path:'/write',body:{amount:100},reason:'Reviewed outcome',confirmation_nonce:'nonce',confirmation_token:'SIGNED',presentation:{confirm_text:'Confirm reviewed action',cancel_text:'<img src=x onerror=alert(1)>',reason_label:'Expected outcome:'}};
    sockets[0].emit({type:'turn_start',run_id:'presentation',seq:1});
    sockets[0].emit({type:'confirmation_required',run_id:'presentation',seq:2,tool_call:presentedCall});
    sockets[0].emit({type:'assistant_done',run_id:'presentation',seq:3,message:'Ready'});
  });
  assert.equal(await page.locator('#a .t-actions .ok').textContent(), 'Confirm reviewed action');
  assert.equal(await page.locator('#a .t-actions button:not(.ok)').textContent(), '<img src=x onerror=alert(1)>');
  assert.equal(await page.locator('#a .t-reason strong').textContent(), 'Expected outcome:');
  assert.equal(await page.locator('#a .t-actions img').count(), 0);
  assert.equal(await page.locator('#a .t-actions .ok').getAttribute('aria-label'), 'Confirm reviewed action · Business action');
  await page.locator('#a .t-actions .ok').click();
  await page.waitForFunction(() => sockets[0].sent.length === 2);
  assert.deepEqual(await page.evaluate(() => sockets[0].sent[1].tool_call), await page.evaluate(() => presentedCall));

  await page.evaluate(() => {
    sockets[0].emit({type:'tool_result',run_id:'presentation',seq:4,tool_call:presentedCall,tool_status:'success'});
    sockets[0].emit({type:'confirmation_required',run_id:'presentation',seq:5,tool_call:{...presentedCall,id:'fallback',confirmation_nonce:'other',presentation:{confirm_text:'\u0001 ',cancel_text:{unsafe:true},reason_label:'x'.repeat(1000)}}});
  });
  const second = page.locator('#a .ihtml-tool').last();
  assert.notEqual(await second.locator('.t-actions .ok').textContent(), '');
  assert.notEqual(await second.locator('.t-actions button:not(.ok)').textContent(), '[object Object]');
  assert.ok((await second.locator('.t-reason strong').textContent()).length <= 81);
});

test('mountChat exposes isolated public theme get/set/onChange without remounting or restarting confirmation', async (t) => {
  const page = await fixture(t);
  await page.evaluate(async () => {
    window.a = ihtml.mountChat('#a', {theme:{mode:'light',primary:'#0f6b52'}});
    window.b = ihtml.mountChat('#b', {theme:{mode:'light',primary:'#123456'}});
    await Promise.all([a.ready(),b.ready()]);
    window.changes=[]; window.offTheme=a.theme.onChange(value => changes.push(value));
    await a.chat.send('Keep pending');
    sockets[0].emit({type:'turn_start',run_id:'theme',seq:1});
    sockets[0].emit({type:'confirmation_required',run_id:'theme',seq:2,tool_call:{id:'pending',name:'write',confirmation_nonce:'n',confirmation_token:'SIGNED'}});
    sockets[0].emit({type:'assistant_done',run_id:'theme',seq:3,message:'Ready'});
    window.originalChatRoot=a.root;
    a.theme.set({mode:'dark',primary:'#b91c1c',radius:18},{persist:false});
  });
  const state = await page.evaluate(() => ({same:a.root===originalChatRoot, a:a.theme.get(), b:b.theme.get(), events:changes.length, color:getComputedStyle(a.root).getPropertyValue('--ui-primary').trim(), radius:a.root.style.getPropertyValue('--ui-radius'), frames:sockets[0].sent.length}));
  assert.equal(state.same,true); assert.equal(state.a.dark,true);assert.equal(state.a.primary,'#b91c1c');
  assert.equal(state.b.primary,'#123456');assert.equal(state.b.dark,false);assert.equal(state.events,1);
  assert.equal(state.color,'#b91c1c');assert.equal(state.radius,'18px');assert.equal(state.frames,1);
  assert.equal(await page.locator('#a .t-actions button').count(),2);
  await page.evaluate(() => { offTheme();a.theme.set({mode:'auto'},{persist:false}); });
  await page.emulateMedia({colorScheme:'dark'});
  await page.waitForFunction(() => a.theme.get().dark);
  await page.emulateMedia({colorScheme:'light'});
  await page.waitForFunction(() => !a.theme.get().dark);
  assert.equal(await page.evaluate(() => changes.length),1);
  assert.equal(await page.evaluate(() => a.root===originalChatRoot),true);
  await page.evaluate(() => { a.destroy(); a.theme.set({primary:'#ffffff'}); });
  assert.equal(await page.evaluate(() => changes.length),1);
});

test('panel mode selection releases a fixed host palette while public theme.set preserves explicit overrides', async (t) => {
  const page = await fixture(t);
  await page.evaluate(async () => {
    const initial = {mode:'light',primary:'#0f6b52',radius:18,font:'sans-serif',bg:'#ffffff',surface:'#fefefe',text:'#111111',muted:'#444444',border:'#dddddd'};
    window.a = ihtml.mount('#a', {safe:true,chatEntry:false,theme:initial});
    window.b = ihtml.mountChat('#b', {theme:initial});
    await Promise.all([a.ready(),b.ready()]);
    a.theme.onChange(value => b.theme.set(value,{persist:false}));
    window.hostRoots=[a.root,b.root];
    // Programmatic calls must retain the host's deliberately fixed palette.
    a.theme.set({mode:'dark'},{persist:false});
    window.programmatic=a.theme.get();
    a.panel.open();
  });
  assert.equal(await page.evaluate(() => programmatic.bg),'#ffffff');
  await page.locator('#a [data-theme-control="mode"][data-theme-value="dark"]').click();
  const dark = await page.evaluate(() => ({theme:a.theme.get(), roots:hostRoots.map(root => ({dark:root.classList.contains('ihtml-dark'),bg:getComputedStyle(root).getPropertyValue('--ui-bg').trim(),text:getComputedStyle(root).getPropertyValue('--ui-text').trim()})),same:a.root===hostRoots[0]&&b.root===hostRoots[1]}));
  assert.equal(dark.same,true);assert.equal(dark.theme.primary,'#0f6b52');assert.equal(dark.theme.radius,18);assert.equal(dark.theme.font,'sans-serif');
  for (const key of ['bg','surface','text','muted','border']) assert.equal(dark.theme[key],'');
  assert.deepEqual(dark.roots[0],dark.roots[1]);assert.equal(dark.roots[0].dark,true);
  assert.notEqual(dark.roots[0].bg,'#ffffff');assert.notEqual(dark.roots[0].text,'#111111');
  await page.locator('#a [data-theme-control="mode"][data-theme-value="light"]').click();
  const light = await page.evaluate(() => hostRoots.map(root => ({dark:root.classList.contains('ihtml-dark'),bg:getComputedStyle(root).getPropertyValue('--ui-bg').trim(),text:getComputedStyle(root).getPropertyValue('--ui-text').trim()})));
  assert.deepEqual(light[0],light[1]);assert.equal(light[0].dark,false);assert.notEqual(light[0].bg,dark.roots[0].bg);assert.notEqual(light[0].text,dark.roots[0].text);
});

test('compact embedded header omits a duplicate title but keeps new-chat and accessible region',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{
  window.a=ihtml.mountChat('#a',{chatHeader:'compact',strings:{chat_title:'Host assistant'}});await a.ready();await a.chat.send('First');
  sockets[0].emit({type:'assistant_done',message:'Done'});
 });
 assert.equal(await page.locator('#a .ihtml-chat-head .title').count(),0);
 assert.equal(await page.locator('#a [role="region"]').getAttribute('aria-label'),'Host assistant');
 assert.equal(await page.locator('#a .ihtml-chat-head button').count(),1);
 const rect=await page.locator('#a .ihtml-chat-head').boundingBox();assert.ok(rect.height<=46);
 await page.evaluate(()=>a.chat.clear());
 assert.equal(await page.evaluate(()=>sockets[0].readyState),3);
 await page.evaluate(async()=>{await a.chat.send('Second');});
 assert.equal(await page.evaluate(()=>sockets.length),2);
});

test('composer has readable single-line proportions and grows for multiline text',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{window.a=ihtml.mountChat('#a',{chatHeader:'compact'});await a.ready();});
 const input=page.locator('#a textarea');
 const initial=await input.evaluate(n=>({height:n.getBoundingClientRect().height,font:parseFloat(getComputedStyle(n).fontSize)}));
 assert.ok(initial.height>=44 && initial.height<=48);assert.ok(initial.font>=16);
 await input.fill('one\ntwo\nthree');
 const expanded=await input.boundingBox();assert.ok(expanded.height>initial.height);
 await input.fill('one');assert.ok((await input.boundingBox()).height<=48);
});

test('terminal markdown and trace compaction keep the last line visible without overriding a reader',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{window.a=ihtml.mountChat('#a',{chatHeader:'compact'});await a.ready();await a.chat.send('Finish test');
  a.root.querySelector('.ihtml-chat-log').style.scrollBehavior='smooth';
  sockets[0].emit({type:'assistant_delta',delta:'Long paragraph.\n\n'.repeat(90),content_format:'markdown'});
 });
 await page.waitForFunction(()=>{const n=a.root.querySelector('.ihtml-chat-log');return n.scrollHeight-n.clientHeight-n.scrollTop<2;});
 await page.evaluate(()=>sockets[0].emit({type:'assistant_done',message:'Finished paragraph.\n\n'.repeat(70)+'FINAL VISIBLE LINE',content_format:'markdown'}));
 await page.waitForFunction(()=>{const n=a.root.querySelector('.ihtml-chat-log'),last=[...a.root.querySelectorAll('.answer')].at(-1)?.querySelector('p:last-child');return last?.textContent==='FINAL VISIBLE LINE'&&n.scrollHeight-n.clientHeight-n.scrollTop<2&&last.getBoundingClientRect().bottom<=n.getBoundingClientRect().bottom+1;});
 await page.evaluate(async()=>{await a.chat.send('Reader test');sockets[0].emit({type:'assistant_delta',delta:'Second answer.\n\n'.repeat(100),content_format:'markdown'});});
 await page.waitForFunction(()=>{const n=a.root.querySelector('.ihtml-chat-log');return n.scrollHeight-n.clientHeight-n.scrollTop<2;});
 const before=await page.evaluate(()=>{const n=a.root.querySelector('.ihtml-chat-log');n.style.scrollBehavior='auto';n.dispatchEvent(new WheelEvent('wheel',{deltaY:-300}));n.scrollTop-=300;return n.scrollTop;});
 await page.evaluate(()=>sockets[0].emit({type:'assistant_done',message:'Second answer.\n\n'.repeat(100)+'Finished',content_format:'markdown'}));
 await page.waitForTimeout(100);
 assert.ok(Math.abs((await page.locator('#a .ihtml-chat-log').evaluate(n=>n.scrollTop))-before)<2);
});

test('new-chat host notification is explicit and never fires on mount or destroy',async(t)=>{
 const page=await fixture(t);const result=await page.evaluate(async()=>{window.resetEvents=0;window.a=ihtml.mountChat('#a',{onNewChat:()=>resetEvents++});await a.ready();const initial=resetEvents;a.chat.clear();const cleared=resetEvents;a.destroy();return {initial,cleared,destroyed:resetEvents};});assert.deepEqual(result,{initial:0,cleared:1,destroyed:1});
});
