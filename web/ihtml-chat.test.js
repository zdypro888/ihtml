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
    window.confirmationScope = 'session'; window.approvalResume = 'manual_tool_only';window.backgroundExecution=false;
    window.fetch = async (url, init) => {
      const pathname = new URL(url).pathname;
      calls.push({ url: String(url), headers: init.headers });
      const data = pathname.endsWith('/api/config')
        ? { user: identity, chat: true, read_only: readOnly, params: {} }
        : pathname.endsWith('/api/chat/models')
          ? { model_ready: true, model_select: false, models: [], agent_modes: agentModes, deep_approval_resume: approvalResume, confirmation_scope: confirmationScope,background_execution:backgroundExecution }
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
    assert.equal(await page.locator('#a [data-agent-mode="deep"]').isVisible(), false);
    assert.equal(await page.locator('#a .ihtml-agent-modebar').evaluate(bar => bar.hidden && bar.getBoundingClientRect().height === 0), true);
    assert.equal(await page.locator('#a .has-agent-modes').count(), 0);
    assert.equal(await page.locator('#a [data-agent-mode="fast"]').evaluate(button => getComputedStyle(button).display), 'none');
  }
  assert.equal(await page.locator('#a [data-agent-mode="deep"]').getAttribute('aria-checked'), 'true');
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
  assert.equal(await page.locator('#a .ihtml-tool[data-status="expired"]').count(), 1);
  await page.locator('#a .t-actions button:not(.ok):not(.t-refresh)').first().click();
  await page.waitForFunction(() => sockets[0].sent.length === 2);
  assert.equal(await page.evaluate(() => sockets[0].sent[1].type), 'cancel_tool');
  assert.equal(await page.evaluate(() => a.chat.busy), true);
  await page.evaluate(() => sockets[0].emit({ type: 'cancel_tool_result', tool_id: 'c1', confirmation_nonce: 'WRONG', tool_status: 'cancelled' }));
  assert.equal(await page.evaluate(() => a.chat.busy), true);
  await page.evaluate(() => sockets[0].emit({ type: 'cancel_tool_result', tool_id: 'c1', confirmation_nonce: 'n1', tool_status: 'cancelled', tool_summary: 'Not executed' }));
  assert.equal(await page.evaluate(() => a.chat.busy), false);
  assert.equal(await page.locator('#a .ihtml-tool[data-status="cancelled"]').count(), 1);
  await page.locator('#a .t-actions button:not(.ok):not(.t-refresh)').first().click();
  await page.waitForFunction(() => sockets[0].sent.length === 3);
  await page.evaluate(() => sockets[0].emit({ type: 'cancel_tool_result', tool_id: 'c2', confirmation_nonce: 'n2', tool_status: 'error', error: 'Authorization: Bearer PRIVATE_SECRET' }));
  assert.equal(await page.evaluate(() => a.chat.busy), false);
  assert.doesNotMatch(await page.locator('#a').innerText(), /PRIVATE_SECRET/);
  await page.evaluate(() => {
    const original = window.setTimeout;
    window.setTimeout = (fn, delay, ...args) => original(fn, delay === 12000 ? 50 : delay, ...args);
  });
  await page.locator('#a .t-actions button:not(.ok):not(.t-refresh)').click();
  await page.waitForFunction(() => !a.chat.busy);
  assert.equal(await page.locator('#a .ihtml-tool[data-status="error"]').count(), 2);
  assert.equal(await page.locator('#a .t-actions:not(:has(.t-refresh)) button:visible').count(), 0);
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
 await page.evaluate(async()=>{window.a=ihtml.mountChat('#a',{chatHeader:'compact',strings:{composer_placeholder:'A very long placeholder that wraps across several lines in a small chat window'}});a.root.style.width='320px';await a.ready();});
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

test('optional history defaults off, pages by host IDs, preserves composer and renders titles as text', async t => {
 const page=await fixture(t,{width:390,height:844});
 await page.evaluate(async()=>{window.a=ihtml.mountChat('#a');await a.ready();});
 assert.equal(await page.locator('.ihtml-chat-history-entry').count(),0);
 await page.evaluate(async()=>{a.destroy();window.historyCalls=[];window.a=ihtml.mountChat('#a',{locale:'en',history:{list:async({cursor})=>{historyCalls.push(cursor);return cursor?{items:[{id:'second',title:'Second'},{id:'first',title:'duplicate'}]}:{items:[{id:'first',title:'<img src=x onerror=alert(1)>',subtitle:'Yesterday'}],cursor:'next'}},select:async({id})=>{window.selectedHistory=id}}});await a.ready();a.chat.draft('draft stays');});
 await page.getByRole('button',{name:'History',exact:true}).click();
 await page.getByRole('button',{name:'Earlier conversations'}).click();
 assert.equal(await page.locator('.ihtml-chat-history-select').count(),2);
 assert.equal(await page.locator('.ihtml-chat-history-list img').count(),0);
 assert.equal(await page.locator('textarea').inputValue(),'draft stays');
 assert.equal(await page.locator('.ihtml-chat-foot').isVisible(),false);
 assert.equal(await page.locator('textarea').boundingBox(),null);
 await page.getByRole('button',{name:'Second',exact:true}).click();
 assert.equal(await page.evaluate(()=>selectedHistory),'second');assert.equal(await page.evaluate(()=>a.chat.history.opened),false);
 assert.equal(await page.locator('textarea').getAttribute('readonly'),null);
 assert.equal(await page.locator('.ihtml-chat-foot').isVisible(),true);
 assert.deepEqual(await page.evaluate(()=>historyCalls),['','next']);
});

test('history failures are bounded, do not leak callback secrets, and deletion requires explicit host receipt',async t=>{
 const page=await fixture(t);await page.evaluate(async()=>{window.removes=0;window.a=ihtml.mountChat('#a',{locale:'en',history:{list:async()=>({items:[{id:'one',title:'One'}]}),select:async()=>{},remove:async()=>{removes++;throw new Error('SECRET_AUTH_TOKEN')}}});await a.ready();a.chat.history.open();});
 await page.getByRole('button',{name:'Delete conversation · One',exact:true}).click();
 await page.getByText('History request did not complete. Check the original record.').waitFor();
 assert.equal(await page.locator('#a').innerText().then(s=>s.includes('SECRET_AUTH_TOKEN')),false);
 assert.ok(await page.getByRole('button',{name:'Delete conversation · One',exact:true}).isDisabled());
 await page.getByRole('button',{name:'Reload',exact:true}).click();assert.equal(await page.evaluate(()=>removes),1);
 assert.ok(await page.getByRole('button',{name:'Delete conversation · One',exact:true}).isDisabled());
 await page.evaluate(async()=>{a.destroy();window.a=ihtml.mountChat('#a',{locale:'en',history:{list:async()=>({items:[{id:'one',title:'One'}]}),select:async()=>{},remove:async()=>false}});await a.ready();a.chat.history.open();});
 await page.getByRole('button',{name:'Delete conversation · One',exact:true}).click();assert.equal(await page.locator('.ihtml-chat-history-select').count(),1);
});

// 导航列表只改变展示，不能清草稿、停后台运行，或被迟到的流事件重新露出输入区。
for(const mode of ['light','dark'])for(const width of [390,1100])test(`history and notifications hide composer without losing draft or active run ${mode} ${width}`,async(t)=>{
 const page=await fixture(t,{width,height:900});await page.evaluate(async({mode})=>{
  backgroundExecution=true;window.selected=[];
  const source={list:async()=>({items:[{id:'saved',title:'Saved dialogue'}]}),select:async({id})=>{selected.push(id)}};
  window.a=ihtml.mountChat('#a',{locale:'en',history:source,notifications:{...source,title:'Inbox'}});await a.ready();a.theme.set({mode},{persist:false});await a.chat.send('Original work');
  sockets[0].emit({type:'turn_start',run_id:'original',seq:1});a.chat.draft('Unsent draft');window.originalInput=a.root.querySelector('textarea');originalInput.setSelectionRange(2,5);
  a.chat.history.open();
 },{mode});
 const foot=page.locator('#a .ihtml-chat-foot');assert.equal(await foot.isVisible(),false);assert.equal(await foot.evaluate(e=>e.getBoundingClientRect().height),0);
 await page.evaluate(()=>{a.chat.notifications.open();sockets[0].emit({type:'assistant_status',run_id:'original',seq:2,status:'Still running'});});
 assert.equal(await foot.isVisible(),false);assert.equal(await page.evaluate(()=>a.chat.busy),true);
 assert.equal(await page.evaluate(()=>sockets[0].sent.length),1,'list navigation never sends cancel or extra messages');
 await page.evaluate(()=>a.chat.notifications.close());assert.equal(await foot.isVisible(),true);assert.equal(await foot.locator('button.stop').isVisible(),true);
 assert.deepEqual(await page.evaluate(()=>({same:originalInput===a.root.querySelector('textarea'),draft:originalInput.value,start:originalInput.selectionStart,end:originalInput.selectionEnd})),{same:true,draft:'Unsent draft',start:2,end:5});
 await page.evaluate(()=>{a.chat.history.open();sockets[0].emit({type:'assistant_done',run_id:'original',seq:3,message:'Original result'});});
 assert.equal(await foot.isVisible(),false);assert.equal(await page.evaluate(()=>a.chat.busy),false);
 await page.getByRole('button',{name:'Saved dialogue',exact:true}).first().click();
 assert.equal(await foot.isVisible(),true);assert.equal(await page.locator('#a textarea').inputValue(),'Unsent draft');assert.equal(await page.evaluate(()=>selected.length),1);
 await page.evaluate(()=>a.chat.notifications.open());assert.equal(await foot.isVisible(),false);await page.getByRole('button',{name:'Saved dialogue',exact:true}).last().click();
 assert.equal(await foot.isVisible(),true);assert.equal(await page.locator('#a textarea').inputValue(),'Unsent draft');assert.match(await page.locator('#a .ihtml-chat-log').innerText(),/Original result/);
 assert.equal(await page.evaluate(()=>sockets[0].sent.length),1);
});

test('late history and selection cannot restore another identity; hidden entry uses public API only',async t=>{
 const page=await fixture(t);await page.evaluate(async()=>{window.a=ihtml.mountChat('#a',{locale:'en',history:{entry:false,list:({signal})=>new Promise(resolve=>{window.resolveOldHistory=resolve;window.historySignal=signal}),select:async()=>{}}});await a.ready();a.chat.history.open();});
 assert.ok(await page.locator('.ihtml-chat-history-entry').isHidden());
 await page.evaluate(async()=>{window.identity='bob';await a.refreshAuth();resolveOldHistory({items:[{id:'private',title:'alice-private-history'}]});});
 assert.equal(await page.evaluate(()=>historySignal.aborted),true);
 assert.equal(await page.locator('#a').innerText().then(s=>s.includes('alice-private-history')),false);
 assert.equal(await page.evaluate(()=>a.chat.history.opened),false);
});

test('history selection blocks parallel send and new-chat until host completes without restoring tool authority',async t=>{
 const page=await fixture(t);await page.evaluate(async()=>{window.a=ihtml.mountChat('#a',{locale:'en',history:{list:async()=>({items:[{id:'one',title:'One'}]}),select:()=>new Promise(resolve=>{window.finishSelect=resolve})}});await a.ready();a.chat.draft('unsent');a.chat.history.open();});
 await page.getByRole('button',{name:'One',exact:true}).click();
 assert.equal(await page.evaluate(()=>a.chat.send('do not send')),false);
 assert.ok(await page.getByRole('button',{name:'New chat',exact:true}).isDisabled());
 await page.evaluate(()=>finishSelect());
 await page.waitForFunction(()=>!a.chat.history.opened);
 assert.equal(await page.evaluate(()=>sockets.length),0);
 assert.equal(await page.locator('textarea').inputValue(),'unsent');
});

test('host clear after authoritative native deletion invalidates the current display without replaying actions',async t=>{
 const page=await fixture(t);await page.evaluate(async()=>{window.nativeDeleted=false;window.a=ihtml.mountChat('#a',{locale:'en',initialMessages:[{role:'user',content:'old transcript'}],history:{list:async()=>({items:[{id:'current',title:'Current'}]}),select:async()=>{},remove:async()=>{nativeDeleted=true;a.chat.clear();return true;}}});await a.ready();a.chat.history.open();});
 await page.getByRole('button',{name:'Delete conversation · Current',exact:true}).click();
 assert.equal(await page.evaluate(()=>nativeDeleted),true);
 assert.equal(await page.evaluate(()=>a.chat.history.opened),false);
 assert.equal(await page.evaluate(()=>a.chat.getMessages().length),0);
 assert.equal(await page.evaluate(()=>sockets.length),0);
});

test('host-owned header and empty-chat starters reclaim space without replaying conversation controls',async(t)=>{
 const page=await fixture(t,{width:390,height:844});
 await page.evaluate(async()=>{window.a=ihtml.mountChat('#a',{chatHeader:'none',starters:['How to begin?','Check status'],history:{entry:false,list:async()=>({items:[]}),select:async()=>{}}});await a.ready();});
 assert.equal(await page.locator('#a .ihtml-chat-head').isVisible(),false);
 assert.equal(await page.locator('#a .ihtml-chat-starters button').count(),2);
 await page.getByRole('button',{name:'How to begin?',exact:true}).click();
 assert.equal(await page.locator('#a .ihtml-chat-starters').count(),0);
 assert.equal(await page.evaluate(()=>sockets[0].sent.find(x=>x.type==='user_message').messages[0].content),'How to begin?');
 await page.evaluate(()=>a.chat.clear());
 assert.equal(await page.locator('#a .ihtml-chat-starters button').count(),2);
 await page.evaluate(()=>a.chat.history.open());assert.equal(await page.locator('#a .ihtml-chat-starters').isVisible(),false);
 await page.evaluate(async()=>{a.destroy();window.a=ihtml.mountChat('#a',{chatHeader:'none',starters:['Not on restored chats'],initialMessages:[{role:'user',content:'Saved question'}]});await a.ready();});
 assert.equal(await page.locator('#a .ihtml-chat-starters').count(),0);
 assert.equal(await page.locator('#a .ihtml-chat').getAttribute('aria-label'),'AI assistant');
});


test('sessionId spans turns and reconnects, and only explicit new chat renews it',async(t)=>{
 const page=await fixture(t);await page.evaluate(async()=>{window.a=ihtml.mountChat('#a');await a.ready();window.firstSession=a.chat.sessionId;await a.chat.send('First');});
 assert.equal(await page.evaluate(()=>new URL(sockets[0].url).searchParams.get('csid')),await page.evaluate(()=>firstSession));
 await page.evaluate(()=>{a.chat.clear();window.nextSession=a.chat.sessionId;});
 assert.notEqual(await page.evaluate(()=>nextSession),await page.evaluate(()=>firstSession));
 await page.evaluate(async()=>{a.destroy();window.a=ihtml.mountChat('#a');await a.ready();});
 assert.equal(await page.evaluate(()=>a.chat.sessionId),await page.evaluate(()=>nextSession));
});


test('empty conversation history uses chat copy instead of revision snapshots',async(t)=>{
 const page=await fixture(t);await page.evaluate(async()=>{window.a=ihtml.mountChat('#a',{locale:'en',history:{list:async()=>({items:[]}),select:async()=>{}}});await a.ready();a.chat.history.open();});
 await page.getByText('No saved conversations',{exact:true}).waitFor({state:'visible'});assert.equal(await page.getByText('No snapshots yet.',{exact:true}).count(),0);
});


for (const locale of ['zh-CN', 'en']) {
  test('managed continuation hint follows backend capability without adding a single-mode toolbar: '+locale, async (t) => {
    const page = await fixture(t, { width: 390, height: 844 });
    await page.evaluate(async (locale) => {
      window.agentModes = ['deep']; window.approvalResume = 'managed_turn';
      window.a = ihtml.mountChat('#a', { locale }); await a.ready(); await a.chat.send('fixture request');
      sockets[0].emit({ type: 'turn_start', run_id: 'proposal', seq: 1 });
      sockets[0].emit({ type: 'confirmation_required', run_id: 'proposal', seq: 2, tool_call: { id: 'fixture-confirm', name: 'fixture_write', title: 'Fixture write', reason: 'Fixture business impact', approval_resume: 'manual_tool_only', confirmation_nonce: 'nonce', confirmation_token: 'SIGNED', confirmation_expires: Math.floor(Date.now()/1000)+600, body: { count: 1 } } });
      sockets[0].emit({ type: 'assistant_done', run_id: 'proposal', seq: 3, message: 'Confirm fixture', content_format: 'text' });
    }, locale);
    assert.equal(await page.locator('#a .ihtml-agent-modebar').isVisible(), false);
    assert.match(await page.locator('#a .ihtml-tool').innerText(), /Fixture business impact/);
    const notice = await page.locator('#a .ihtml-tool .ihtml-agent-manual').innerText();
    assert.match(notice, locale === 'zh-CN' ? /继续处理原请求.*再次请求确认/ : /continues your request.*asks again/);
    assert.doesNotMatch(await page.locator('#a').innerText(), /不会自动继续|计划不会自动继续|do not resume automatically/);
    // 新能力仅改变说明；仍然只发送当前签名卡，不把续办变成预先批准其他操作。
    await page.locator('#a .t-actions .ok').click();
    const frames = await page.evaluate(() => sockets[0].sent);
    assert.equal(frames.filter(frame => frame.type === 'confirm_tool').length, 1);
    assert.equal(frames.at(-1).tool_call.id, 'fixture-confirm');
    assert.equal(frames.at(-1).tool_call.approval_resume, 'manual_tool_only');
  });
}

test('confirmation shows exact shared deadline, expires without confirming and offers normal preview refresh', async (t) => {
 const page=await fixture(t,{width:390,height:844});
 await page.clock.install({time:new Date('2026-10-28T06:00:00Z')});
 await page.evaluate(async()=>{
  window.a=ihtml.mountChat('#a',{locale:'zh-CN'});await a.ready();await a.chat.send('clean fixture');
  sockets[0].emit({type:'turn_start',run_id:'deadline',seq:1});
  sockets[0].emit({type:'confirmation_required',run_id:'deadline',seq:2,tool_call:{id:'expires',name:'write',title:'清理预览',risk:'high',approval_resume:'manual_tool_only',confirmation_nonce:'n',confirmation_token:'SIGNED',confirmation_expires:Math.floor(Date.now()/1000)+3}});
  sockets[0].emit({type:'assistant_done',run_id:'deadline',seq:3,message:'等待确认'});
 });
 assert.match(await page.locator('#a .t-countdown').textContent(),/0:03/);
 assert.equal(await page.locator('#a .t-deadline').getAttribute('datetime'),'2026-10-28T06:00:03.000Z');
 assert.equal(await page.locator('#a .t-countdown').evaluate(n=>n.classList.contains('is-expiring')),true);
 await page.clock.runFor(350);
 await page.screenshot({path:'/tmp/ihtml-confirmation-deadline-mobile.png',animations:'disabled'});
 await page.clock.fastForward(4000);
 assert.equal(await page.locator('#a .ihtml-tool[data-status="expired"]').count(),1);
 assert.equal(await page.locator('#a .t-actions .ok').count(),0);
 assert.equal(await page.locator('#a .t-refresh').isVisible(),true);
 await page.locator('#a .t-refresh').click();
 await page.waitForFunction(()=>sockets.some(s=>s.sent.some(m=>m.type==='user_message'&&m.messages?.some(x=>x.content.includes('刷新过期预览')))));
 assert.equal(await page.evaluate(()=>sockets.flatMap(s=>s.sent).some(m=>m.type==='confirm_tool')),false);
});

test('confirmation does not submit after a delayed user hook crosses its deadline', async(t)=>{
 const page=await fixture(t);
 await page.clock.install({time:new Date('2026-10-28T06:00:00Z')});
 await page.evaluate(async()=>{
  window.a=ihtml.mountChat('#a',{beforeConfirm:()=>new Promise(resolve=>{window.finishReview=resolve;})});await a.ready();await a.chat.send('fixture');
  sockets[0].emit({type:'turn_start',run_id:'deadline-hook',seq:1});
  sockets[0].emit({type:'confirmation_required',run_id:'deadline-hook',seq:2,tool_call:{id:'hook',name:'write',approval_resume:'manual_tool_only',confirmation_nonce:'n',confirmation_token:'SIGNED',confirmation_expires:Math.floor(Date.now()/1000)+3}});
  sockets[0].emit({type:'assistant_done',run_id:'deadline-hook',seq:3,message:'Review'});
 });
 await page.locator('#a .t-actions .ok').click();
 await page.clock.fastForward(4000);
 await page.evaluate(()=>finishReview(true));
 assert.equal(await page.evaluate(()=>sockets.flatMap(s=>s.sent).some(m=>m.type==='confirm_tool')),false);
 assert.equal(await page.evaluate(()=>a.chat.busy),false);
 assert.equal(await page.locator('#a .t-refresh').isVisible(),true);
});

test('pending cancellation keeps its authoritative wait across the confirmation deadline',async(t)=>{
 const page=await fixture(t);
 await page.clock.install({time:new Date('2026-10-28T06:00:00Z')});
 await page.evaluate(async()=>{
  window.a=ihtml.mountChat('#a',{locale:'zh-CN'});await a.ready();await a.chat.send('fixture');
  sockets[0].emit({type:'turn_start',run_id:'cancel-deadline',seq:1});
  sockets[0].emit({type:'confirmation_required',run_id:'cancel-deadline',seq:2,tool_call:{id:'cancel-expiring',name:'write',title:'取消夹具',approval_resume:'manual_tool_only',confirmation_nonce:'n',confirmation_token:'SIGNED',confirmation_expires:Math.floor(Date.now()/1000)+3}});
  sockets[0].emit({type:'assistant_done',run_id:'cancel-deadline',seq:3,message:'等待确认'});
 });
 await page.locator('#a .t-actions button:not(.ok)').click();
 await page.waitForFunction(()=>sockets[0].sent.some(m=>m.type==='cancel_tool'));
 await page.clock.fastForward(4000);
 assert.equal(await page.locator('#a .ihtml-tool[data-status="pending"]').count(),1,'deadline cannot manufacture a cancellation receipt');
 assert.equal(await page.locator('#a .t-refresh').count(),0,'uncertain cancellation cannot offer automatic preview refresh');
 assert.equal(await page.evaluate(()=>a.chat.busy),true);
 await page.clock.fastForward(30000);
 assert.equal(await page.locator('#a .ihtml-tool[data-status="error"]').count(),1,'missing acknowledgement is a terminal transport error');
 assert.match(await page.locator('#a .ihtml-tool').innerText(),/状态未知/,'transport error must keep execution explicitly unknown');
 assert.equal(await page.locator('#a .t-refresh').count(),0);
 assert.equal(await page.evaluate(()=>sockets.flatMap(s=>s.sent).filter(m=>m.type==='cancel_tool').length),1);
});

test('unexpected disconnect reconnects with a fresh ticket and same conversation without replaying messages or confirmations',async(t)=>{
 const page=await fixture(t);await page.clock.install();
 await page.evaluate(async()=>{
  window.ticketCount=0;window.confirmationScope='connection';
  window.a=ihtml.mountChat('#a',{connectionAuth:()=>Promise.resolve('ticket-'+(++ticketCount))});await a.ready();await a.chat.send('original');
  sockets[0].emit({type:'turn_start',run_id:'interrupted',seq:1});
  sockets[0].emit({type:'confirmation_required',run_id:'interrupted',seq:2,tool_call:{id:'old',name:'write',approval_resume:'manual_tool_only',confirmation_nonce:'nonce',confirmation_token:'SIGNED',confirmation_expires:Math.floor(Date.now()/1000)+600}});
  sockets[0].close();
 });
 await page.clock.runFor(1100);
 await page.waitForFunction(()=>sockets.length===2&&sockets[1].readyState===1);
 const result=await page.evaluate(()=>({tickets:ticketCount,first:new URL(sockets[0].url).searchParams.get('csid'),second:new URL(sockets[1].url).searchParams.get('csid'),sent:sockets[1].sent,busy:a.chat.busy}));
 assert.equal(result.tickets,2);assert.equal(result.first,result.second);assert.deepEqual(result.sent,[]);assert.equal(result.busy,false);
 assert.equal(await page.locator('#a .t-actions .ok').count(),0);
 assert.match(await page.locator('#a').innerText(),/Connection restored|连接已恢复/);
});

test('reconnect backs off after ticket failure and stops on revoked authorization',async(t)=>{
 const page=await fixture(t);await page.clock.install();
 await page.evaluate(async()=>{
  window.ticketCount=0;window.a=ihtml.mountChat('#a',{connectionAuth:async()=>{ticketCount++;if(ticketCount===2)throw new Error('temporary');return 'ticket-'+ticketCount;}});await a.ready();await a.chat.send('original');sockets[0].close();
 });
 await page.clock.runFor(1100);assert.equal(await page.evaluate(()=>ticketCount),2);
 await page.clock.runFor(1900);await page.waitForFunction(()=>sockets.length===2);
 await page.evaluate(()=>{sockets[1].readyState=3;sockets[1].onclose({code:1008});});
 await page.clock.fastForward(60000);
 assert.equal(await page.evaluate(()=>ticketCount),3);
 assert.equal(await page.evaluate(()=>sockets.length),2);
 assert.match(await page.locator('#a').innerText(),/authorization expired|授权已失效/);
});

test('offline and background pauses reconnect; returning online and visible wakes it without sending a request',async(t)=>{
 const page=await fixture(t);await page.clock.install();
 await page.evaluate(async()=>{
  window.isOnline=false;window.isVisible=false;
  Object.defineProperty(navigator,'onLine',{configurable:true,get:()=>isOnline});
  Object.defineProperty(document,'visibilityState',{configurable:true,get:()=>isVisible?'visible':'hidden'});
  window.a=ihtml.mountChat('#a');await a.ready();await a.chat.send('original');sockets[0].close();
 });
 await page.clock.fastForward(30000);assert.equal(await page.evaluate(()=>sockets.length),1);
 await page.evaluate(()=>{isOnline=true;window.dispatchEvent(new Event('online'));});
 await page.clock.runFor(10);assert.equal(await page.evaluate(()=>sockets.length),1);
 await page.evaluate(()=>{isVisible=true;document.dispatchEvent(new Event('visibilitychange'));});
 await page.clock.runFor(10);await page.waitForFunction(()=>sockets.length===2);
 assert.deepEqual(await page.evaluate(()=>sockets[1].sent),[]);
});

test('clear, destroy and authorization failure cancel pending reconnects',async(t)=>{
 const page=await fixture(t);await page.clock.install();
 await page.evaluate(async()=>{window.a=ihtml.mountChat('#a');await a.ready();await a.chat.send('original');sockets[0].close();a.chat.clear();});
 await page.clock.fastForward(60000);assert.equal(await page.evaluate(()=>sockets.length),1);
 await page.evaluate(async()=>{await a.chat.send('second');sockets[1].close();a.destroy();window.dispatchEvent(new Event('online'));});
 await page.clock.fastForward(60000);assert.equal(await page.evaluate(()=>sockets.length),2);
});

test('connection ticket authorization rejection is not retried automatically',async(t)=>{
 const page=await fixture(t);await page.clock.install();
 await page.evaluate(async()=>{window.ticketCount=0;window.a=ihtml.mountChat('#a',{connectionAuth:async()=>{ticketCount++;throw Object.assign(new Error('private auth error'),{status:403});}});await a.ready();await a.chat.send('original');});
 await page.clock.fastForward(60000);
 assert.equal(await page.evaluate(()=>ticketCount),1);
 assert.equal(await page.evaluate(()=>sockets.length),0);
 assert.doesNotMatch(await page.locator('#a').innerText(),/private auth error/);
});

test('a stalled reconnect handshake times out and backs off without replay',async(t)=>{
 const page=await fixture(t);await page.clock.install();
 await page.evaluate(async()=>{
  window.a=ihtml.mountChat('#a');await a.ready();await a.chat.send('original');
  window.WebSocket=class {static OPEN=1;constructor(url){this.url=url;this.sent=[];this.readyState=0;sockets.push(this);}close(){this.readyState=3;this.onclose?.({code:1006});}};
  sockets[0].close();
 });
 await page.clock.runFor(1100);assert.equal(await page.evaluate(()=>sockets.length),2);
 await page.clock.runFor(10000);assert.equal(await page.evaluate(()=>sockets[1].readyState),3);
 await page.clock.runFor(2100);assert.equal(await page.evaluate(()=>sockets.length),3);
 assert.deepEqual(await page.evaluate(()=>sockets.slice(1).flatMap(s=>s.sent)),[]);
});

test('background execution reconnects without ending the active turn and replaces recovered text snapshots',async(t)=>{
 const page=await fixture(t);await page.clock.install();
 await page.evaluate(async()=>{
  backgroundExecution=true;window.a=ihtml.mountChat('#a');await a.ready();await a.chat.send('original');
  sockets[0].emit({type:'turn_start',run_id:'background',seq:1,background_session:'owned',background_seq:2});
  sockets[0].emit({type:'assistant_delta',run_id:'background',seq:2,background_session:'owned',background_seq:3,delta:'part'});
  sockets[0].close();
 });
 assert.equal(await page.evaluate(()=>a.chat.busy),true);
 assert.equal(await page.locator('#a .ihtml-process.failed').count(),0);
 await page.clock.runFor(1100);await page.waitForFunction(()=>sockets.length===2);
 assert.deepEqual(await page.evaluate(()=>sockets[1].sent),[]);
 await page.evaluate(()=>{
  sockets[1].emit({type:'background_user_message',background_session:'owned',background_seq:1,background_replay:true,message:'original'});
  sockets[1].emit({type:'turn_start',run_id:'background',seq:1,background_session:'owned',background_seq:2,background_replay:true});
  sockets[1].emit({type:'assistant_snapshot',run_id:'background',seq:4,background_session:'owned',background_seq:5,background_replay:true,delta:'partial complete'});
  sockets[1].emit({type:'assistant_done',run_id:'background',seq:5,background_session:'owned',background_seq:6,message:'partial complete'});
 });
 assert.equal(await page.evaluate(()=>a.chat.busy),false);
 assert.equal(await page.locator('#a .ihtml-msg.user').count(),1);
 assert.equal(await page.locator('#a .answer').last().textContent(),'partial complete');
});

test('a reopened background view restores the original user request and run without submitting a message',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{
  backgroundExecution=true;window.a=ihtml.mountChat('#a');await a.ready();
 });
 await page.waitForFunction(()=>sockets.length===1);
 await page.evaluate(()=>{
  const socket=sockets[0];socket.emit({type:'background_user_message',background_session:'owned',background_seq:1,background_replay:true,message:'saved request'});
  socket.emit({type:'turn_start',run_id:'restored',seq:1,background_session:'owned',background_seq:2,background_replay:true});
  socket.emit({type:'assistant_snapshot',run_id:'restored',seq:2,background_session:'owned',background_seq:3,background_replay:true,delta:'restored progress'});
  socket.emit({type:'assistant_done',run_id:'restored',seq:3,background_session:'owned',background_seq:4,background_replay:true,message:'saved result'});
 });
 assert.deepEqual(await page.evaluate(()=>sockets[0].sent),[]);
 assert.match(await page.locator('#a').innerText(),/saved request/);
 assert.match(await page.locator('#a').innerText(),/saved result/);
});

test('notification inbox reuses history navigation and requests push permission only on explicit enable',async(t)=>{
 const page=await fixture(t,{width:390,height:844});
 await page.evaluate(async()=>{
  window.permissionCalls=0;window.subscribes=0;window.selectedNotification='';window.unread=-1;
  window.Notification={permission:'default',requestPermission:async()=>{permissionCalls++;return 'granted';}};window.PushManager=class{};
  Object.defineProperty(navigator,'serviceWorker',{configurable:true,value:{getRegistration:async()=>null,register:async()=>({active:{scriptURL:'https://host.test/ihtml-push-sw.js'},pushManager:{getSubscription:async()=>({endpoint:'https://web.push.apple.com/test',toJSON:()=>({endpoint:'https://web.push.apple.com/test'}),unsubscribe:async()=>true})}})}});
  window.a=ihtml.mountChat('#a',{history:{list:async()=>({items:[]}),select:async()=>{}},notifications:{title:'Inbox',emptyMessage:'No notifications',onUnreadChange:n=>{unread=n;},list:async()=>({items:[{id:'owned',title:'Original task',subtitle:'Needs confirmation',removable:false}],unread:1}),select:async({id})=>{selectedNotification=id;},push:{publicKey:'test',subscribe:async()=>{subscribes++;return {enabled:true};},remove:async()=>{}}}});await a.ready();a.chat.notifications.open();
 });
 assert.equal(await page.evaluate(()=>permissionCalls),0);
 assert.match(await page.locator('#a .ihtml-chat-history').last().innerText(),/Original task/);
 await page.locator('#a .ihtml-chat-push-enable').click();
 await page.waitForFunction(()=>subscribes===1);
 assert.equal(await page.evaluate(()=>permissionCalls),1);
 assert.match(await page.locator('#a .ihtml-chat-push-enable').innerText(),/Disable|关闭/);
 await page.locator('#a .ihtml-chat-history').last().getByRole('button',{name:'Original task Needs confirmation'}).click();
 assert.equal(await page.evaluate(()=>selectedNotification),'owned');
 assert.equal(await page.evaluate(()=>unread),1);
});

test('push registration failure never claims notifications are enabled or leaks errors',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{
  window.permissionCalls=0;window.Notification={permission:'default',requestPermission:async()=>{permissionCalls++;return 'denied';}};window.PushManager=class{};
  Object.defineProperty(navigator,'serviceWorker',{configurable:true,value:{getRegistration:async()=>null,register:async()=>{throw new Error('PRIVATE_PUSH_KEY');}}});
  window.a=ihtml.mountChat('#a',{notifications:{title:'Inbox',list:async()=>({items:[]}),select:async()=>{},push:{publicKey:'test',subscribe:async()=>({enabled:true}),remove:async()=>{}}}});await a.ready();a.chat.notifications.open();
 });
 await page.locator('#a .ihtml-chat-push-enable').click();
 assert.equal(await page.evaluate(()=>permissionCalls),1);
 assert.doesNotMatch(await page.locator('#a').innerText(),/PRIVATE_PUSH_KEY/);
 assert.match(await page.locator('#a .ihtml-chat-push-enable').innerText(),/Enable|开启/);
});

// 用户确认的是受理预览；中间回执后仍需自动展示等待进度及新的确认卡。
test('managed confirmation receipt keeps one active flow and displays its next run and approval',async(t)=>{
 const page=await fixture(t,{width:390,height:844});
 await page.evaluate(async()=>{
  backgroundExecution=true;approvalResume='managed_turn';window.a=ihtml.mountChat('#a',{locale:'zh-CN'});await a.ready();await a.chat.send('preview then clear then transfer');
  const emit=(type,seq,rest={})=>sockets[0].emit({type,run_id:'confirmed',seq,...rest});
  emit('turn_start',1);emit('tool_result',2,{tool_id:'preview',tool_name:'preview',tool_title:'预览云盘',tool_status:'accepted',tool_summary:'请求已接受，尚未确认业务完成。'});
  emit('assistant_done',3,{finish_reason:'manual_tool_only',continuation_pending:true,message:'请求已接受，尚未确认业务完成。'});
 });
 assert.equal(await page.evaluate(()=>a.chat.busy),true);
 assert.doesNotMatch(await page.locator('#a .ihtml-process-head').innerText(),/本轮处理结束/);
 assert.equal(await page.locator('#a .answer').last().textContent(),'');
 await page.evaluate(()=>{
  sockets[0].emit({type:'turn_start',run_id:'followup',seq:1});
  sockets[0].emit({type:'assistant_status',run_id:'followup',seq:2,status:'正在读取云盘预览：4/10 个身份已就绪'});
 });
 assert.match(await page.locator('#a').innerText(),/4\/10/);
 assert.equal(await page.locator('#a .ihtml-msg.assistant:has(.ihtml-process)').count(),1);
 await page.evaluate(()=>{
  sockets[0].emit({type:'confirmation_required',run_id:'followup',seq:3,tool_call:{id:'delete',name:'delete',title:'确认清空10个身份',method:'POST',path:'/delete',confirmation_token:'SIGNED',confirmation_nonce:'fresh',confirmation_expires:Math.floor(Date.now()/1000)+600}});
  sockets[0].emit({type:'assistant_done',run_id:'followup',seq:4,message:'预览已完成，请核对并确认下一步。'});
 });
 assert.equal(await page.evaluate(()=>a.chat.busy),false);
 assert.match(await page.locator('#a').innerText(),/确认清空10个身份/);
 assert.deepEqual(await page.evaluate(()=>sockets[0].sent.map(e=>e.type)),['user_message']);
});

test('replay of a managed continuation keeps its accepted receipt and resumed run in the same flow',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{
  backgroundExecution=true;window.a=ihtml.mountChat('#a');await a.ready();
 });
 await page.waitForFunction(()=>sockets.length===1);
 await page.evaluate(()=>{
  let seq=0;const emit=(type,run_id,rest={})=>sockets[0].emit({type,run_id,seq:++seq,background_session:'stable',background_seq:seq,background_replay:true,...rest});
  emit('turn_start','receipt');emit('assistant_done','receipt',{finish_reason:'manual_tool_only',continuation_pending:true,message:'Accepted only'});
  emit('turn_start','resumed');emit('assistant_status','resumed',{status:'Waiting for original preview'});
 });
 assert.equal(await page.evaluate(()=>a.chat.busy),true);
 assert.equal(await page.locator('#a .ihtml-msg.assistant:has(.ihtml-process)').count(),1);
 assert.match(await page.locator('#a').innerText(),/Waiting for original preview/);
});

test('notification deletion and clearing reuse optional hooks and refresh unread state',async(t)=>{
 const page=await fixture(t,{width:390,height:844});
 await page.evaluate(async()=>{
  window.notices=[{id:'one',title:'First'},{id:'two',title:'Second'}];window.noticeUnread=-1;window.allowRemoval=false;window.clearCalls=0;
  window.a=ihtml.mountChat('#a',{locale:'zh-CN',notifications:{emptyMessage:'暂无助手通知',list:async()=>({items:notices,unread:notices.length}),select:async()=>{},onUnreadChange:n=>{noticeUnread=n},remove:async({id})=>{if(!allowRemoval)return false;notices=notices.filter(row=>row.id!==id);return true},clear:async()=>{clearCalls++;notices=[{id:'concurrent',title:'New progress'}];return true}}});await a.ready();a.chat.notifications.open();
 });
 const pane=page.locator('.ihtml-chat-history');
 await pane.getByRole('button',{name:'删除通知 · First',exact:true}).click();
 await page.waitForFunction(()=>noticeUnread===2);
 assert.equal(await pane.locator('.ihtml-chat-history-row').count(),2);
 await page.evaluate(()=>{allowRemoval=true});
 await pane.getByRole('button',{name:'删除通知 · First',exact:true}).click();
 await page.waitForFunction(()=>noticeUnread===1);
 assert.equal(await pane.locator('.ihtml-chat-history-row').count(),1);
 await pane.getByRole('button',{name:'清空通知',exact:true}).click();
 await page.waitForFunction(()=>clearCalls===1&&noticeUnread===1&&document.querySelector('.ihtml-chat-history-select')?.textContent==='New progress');
 assert.equal(await pane.locator('.ihtml-chat-history-row').count(),1);
});

// 确认在另一 run 执行；历史回放没有浏览器点击留下的 acceptNextRun 状态。
for(const status of ['accepted','success','error']) test('historical confirmation resolves its original card across runs: '+status,async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{backgroundExecution=true;window.a=ihtml.mountChat('#a',{locale:'zh-CN'});await a.ready();});
 await page.waitForFunction(()=>sockets.length===1);
 await page.evaluate((status)=>{
  let n=0;const emit=(type,run_id,seq,rest={})=>sockets[0].emit({type,run_id,seq,background_session:'history',background_seq:++n,background_replay:true,...rest});
  const card={id:'same-id',name:'preview',title:'原预览确认卡',confirmation_nonce:'original-nonce',confirmation_token:'SIGNED',confirmation_expires:Math.floor(Date.now()/1000)+600};
  emit('turn_start','proposal',1);emit('confirmation_required','proposal',2,{tool_call:card});emit('assistant_done','proposal',3,{message:'等待确认'});
  emit('turn_start','other',1);emit('confirmation_required','other',2,{tool_call:{...card,title:'另一张确认卡',confirmation_nonce:'other-nonce'}});emit('assistant_done','other',3,{message:'另一个操作'});
  emit('turn_start','execution',1);emit('tool_start','execution',2,{tool_call:{id:card.id,confirmation_nonce:card.confirmation_nonce}});
  emit('tool_result','execution',3,{tool_call:{id:card.id,confirmation_nonce:card.confirmation_nonce},tool_status:status,tool_summary:'服务端回执'});
  emit('assistant_done','execution',4,{message:'本操作已处理'});
  // 迟到的同卡恢复事件不能把已完成的回执降级为可再次确认。
  emit('turn_start','late',1);emit('confirmation_required','late',2,{tool_call:card});emit('assistant_done','late',3,{message:'旧恢复事件'});
 },status);
 const original=page.locator('.ihtml-tool').filter({hasText:'原预览确认卡'});
 assert.equal(await original.count(),1);assert.equal(await original.getAttribute('data-status'),status);
 assert.equal(await original.getByRole('button',{name:'确认此操作',exact:true}).count(),0);
 const other=page.locator('.ihtml-tool').filter({hasText:'另一张确认卡'});
 assert.equal(await other.getAttribute('data-status'),'pending');
 assert.equal(await page.locator('.ihtml-tool[data-status="pending"]').count(),1);
 assert.deepEqual(await page.evaluate(()=>sockets.flatMap(s=>s.sent)),[]);
});

test('expired historical card is corrected by its accepted receipt without claiming cancellation or workflow completion',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{backgroundExecution=true;window.a=ihtml.mountChat('#a',{locale:'zh-CN'});await a.ready();});
 await page.waitForFunction(()=>sockets.length===1);
 await page.evaluate(()=>{
  const card={id:'preview',name:'preview',title:'过期历史预览',confirmation_nonce:'already-confirmed',confirmation_expires:Math.floor(Date.now()/1000)-30};
  sockets[0].emit({type:'turn_start',run_id:'old-proposal',seq:1,background_replay:true});
  sockets[0].emit({type:'confirmation_required',run_id:'old-proposal',seq:2,tool_call:card});
  sockets[0].emit({type:'assistant_done',run_id:'old-proposal',seq:3,message:'旧轮次'});
 });
 assert.equal(await page.locator('.ihtml-tool[data-status="expired"]').count(),1);
 assert.doesNotMatch(await page.locator('#a').innerText(),/已取消|本次未执行|本轮处理结束/);
 await page.evaluate(()=>{
  sockets[0].emit({type:'turn_start',run_id:'accepted-receipt',seq:1,background_replay:true});
  sockets[0].emit({type:'tool_result',run_id:'accepted-receipt',seq:2,tool_call:{id:'preview',confirmation_nonce:'already-confirmed'},tool_status:'accepted',tool_summary:'预览请求已受理'});
  sockets[0].emit({type:'assistant_done',run_id:'accepted-receipt',seq:3,continuation_pending:true});
  sockets[0].emit({type:'turn_start',run_id:'followup',seq:1,background_replay:true});
  sockets[0].emit({type:'assistant_status',run_id:'followup',seq:2,status:'正在读取云盘预览：16/30'});
 });
 assert.equal(await page.locator('.ihtml-tool').count(),1);
 assert.equal(await page.locator('.ihtml-tool[data-status="accepted"]').count(),1);
 assert.equal(await page.locator('.t-refresh').count(),0);
 assert.match(await page.locator('#a .p-title').first().innerText(),/处理过程/);
 assert.match(await page.locator('#a').innerText(),/16\/30/);
 assert.doesNotMatch(await page.locator('#a').innerText(),/已取消|本轮处理结束/);
 assert.equal(await page.evaluate(()=>a.chat.busy),true);
});

// 提案 ID 可被模型复用；确认卡必须以 id+nonce 隔离，不能覆盖同轮次的其他授权。
test('same-turn cards with reused ids retain independent nonces and counters',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{
  window.a=ihtml.mountChat('#a',{locale:'zh-CN'});await a.ready();await a.chat.send('two operations');
  const card={id:'reused',name:'write',confirmation_token:'SIGNED',confirmation_expires:Math.floor(Date.now()/1000)+600};
  sockets[0].emit({type:'turn_start',run_id:'proposal',seq:1});
  sockets[0].emit({type:'confirmation_required',run_id:'proposal',seq:2,tool_call:{...card,title:'第一项操作',confirmation_nonce:'one'}});
  sockets[0].emit({type:'confirmation_required',run_id:'proposal',seq:3,tool_call:{...card,title:'第二项操作',confirmation_nonce:'two'}});
  sockets[0].emit({type:'assistant_done',run_id:'proposal',seq:4,message:'Review'});
 });
 assert.equal(await page.locator('.ihtml-tool[data-status="pending"]').count(),2);
 await page.evaluate(()=>{
  sockets[0].emit({type:'turn_start',run_id:'execution',seq:1,background_replay:true});
  sockets[0].emit({type:'tool_result',run_id:'execution',seq:2,tool_call:{id:'reused',confirmation_nonce:'one'},tool_status:'accepted'});
 });
 assert.equal(await page.locator('.ihtml-tool').filter({hasText:'第一项操作'}).getAttribute('data-status'),'accepted');
 assert.equal(await page.locator('.ihtml-tool').filter({hasText:'第二项操作'}).getAttribute('data-status'),'pending');
 assert.match(await page.locator('.ihtml-process-head').first().innerText(),/1 个操作待确认/);
});

for(const lateStatus of ['cancelled','error']) test('late cancellation acknowledgement cannot overwrite accepted execution: '+lateStatus,async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{
  window.a=ihtml.mountChat('#a');await a.ready();await a.chat.send('operation');
  window.review={id:'operation',name:'write',confirmation_nonce:'executed',confirmation_token:'SIGNED',confirmation_expires:Math.floor(Date.now()/1000)+600};
  sockets[0].emit({type:'turn_start',run_id:'proposal',seq:1});
  sockets[0].emit({type:'confirmation_required',run_id:'proposal',seq:2,tool_call:review});
  sockets[0].emit({type:'tool_result',run_id:'proposal',seq:3,tool_call:review,tool_status:'accepted'});
  sockets[0].emit({type:'assistant_done',run_id:'proposal',seq:4,message:'Accepted'});
 });
 await page.evaluate((status)=>sockets[0].emit({type:'cancel_tool_result',tool_id:'operation',confirmation_nonce:'executed',tool_status:status,error:'Nonce already consumed'}),lateStatus);
 assert.equal(await page.locator('.ihtml-tool').getAttribute('data-status'),'accepted');
});

test('late accepted receipt cannot downgrade a completed operation',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{
  window.a=ihtml.mountChat('#a');await a.ready();await a.chat.send('operation');
  const call={id:'operation',name:'write',confirmation_nonce:'done'};
  sockets[0].emit({type:'turn_start',run_id:'completed',seq:1});
  sockets[0].emit({type:'tool_result',run_id:'completed',seq:2,tool_call:call,tool_status:'success'});
  sockets[0].emit({type:'assistant_done',run_id:'completed',seq:3,message:'Done'});
  sockets[0].emit({type:'turn_start',run_id:'late',seq:1,background_replay:true});
  sockets[0].emit({type:'tool_result',run_id:'late',seq:2,tool_call:call,tool_status:'accepted'});
 });
 assert.equal(await page.locator('.ihtml-tool').count(),1);
 assert.equal(await page.locator('.ihtml-tool').getAttribute('data-status'),'success');
});

test('a cancellation failure is not execution evidence and a real receipt corrects it',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{
  window.a=ihtml.mountChat('#a');await a.ready();await a.chat.send('operation');
  window.card={id:'operation',name:'write',confirmation_nonce:'one',confirmation_token:'SIGNED',confirmation_expires:Math.floor(Date.now()/1000)+600};
  sockets[0].emit({type:'turn_start',run_id:'proposal',seq:1});
  sockets[0].emit({type:'confirmation_required',run_id:'proposal',seq:2,tool_call:card});
  sockets[0].emit({type:'assistant_done',run_id:'proposal',seq:3,message:'Review'});
  sockets[0].emit({type:'cancel_tool_result',tool_id:'operation',confirmation_nonce:'one',tool_status:'error',error:'Already consumed'});
 });
 assert.equal(await page.locator('.ihtml-tool').getAttribute('data-status'),'error');
 await page.evaluate(()=>{
  sockets[0].emit({type:'turn_start',run_id:'actual',seq:1,background_replay:true});
  sockets[0].emit({type:'tool_result',run_id:'actual',seq:2,tool_call:card,tool_status:'accepted'});
 });
 assert.equal(await page.locator('.ihtml-tool').getAttribute('data-status'),'accepted');
 assert.doesNotMatch(await page.locator('#a').innerText(),/Already consumed/);
});

test('accepted execution clears cancellation timers instead of failing later',async(t)=>{
 const page=await fixture(t);await page.clock.install();
 await page.evaluate(async()=>{
  window.a=ihtml.mountChat('#a');await a.ready();await a.chat.send('operation');
  window.card={id:'operation',name:'write',confirmation_nonce:'timer',confirmation_token:'SIGNED',confirmation_expires:Math.floor(Date.now()/1000)+600};
  sockets[0].emit({type:'turn_start',run_id:'proposal',seq:1});
  sockets[0].emit({type:'confirmation_required',run_id:'proposal',seq:2,tool_call:card});
  sockets[0].emit({type:'assistant_done',run_id:'proposal',seq:3,message:'Review'});
 });
 await page.locator('#a .t-actions button:not(.ok)').click();
 await page.waitForFunction(()=>sockets[0].sent.some(e=>e.type==='cancel_tool'));
 await page.evaluate(()=>{
  sockets[0].emit({type:'turn_start',run_id:'actual',seq:1,background_replay:true});
  sockets[0].emit({type:'tool_result',run_id:'actual',seq:2,tool_call:card,tool_status:'accepted'});
  sockets[0].emit({type:'assistant_done',run_id:'actual',seq:3,message:'Accepted'});
 });
 await page.clock.fastForward(60000);
 assert.equal(await page.locator('.ihtml-tool').getAttribute('data-status'),'accepted');
 assert.equal(await page.evaluate(()=>a.chat.busy),false);
});

// 产品历史已有受理正文时，SDK 仍须保留后续 run 的可见进度容器。
test('saved receipt text does not hide replayed continuation progress after reconciling its original card',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{
  backgroundExecution=true;window.a=ihtml.mountChat('#a',{initialMessages:[{role:'assistant',content:'Accepted only'}]});await a.ready();
 });
 await page.waitForFunction(()=>sockets.length===1);
 await page.evaluate(()=>{
  let n=0;const emit=(type,run_id,seq,rest={})=>sockets[0].emit({type,run_id,seq,background_session:'saved',background_seq:++n,background_replay:true,...rest});
  const call={id:'preview',name:'preview',title:'Original preview',confirmation_nonce:'confirmed',confirmation_token:'SIGNED',confirmation_expires:Math.floor(Date.now()/1000)+600};
  emit('turn_start','proposal',1);emit('confirmation_required','proposal',2,{tool_call:call});emit('assistant_done','proposal',3,{message:'Review'});
  emit('turn_start','receipt',1);emit('tool_result','receipt',2,{tool_call:{id:'preview',confirmation_nonce:'confirmed'},tool_status:'accepted'});
  emit('assistant_done','receipt',3,{continuation_pending:true,message:'Accepted only'});
  emit('turn_start','continued',1);emit('assistant_status','continued',2,{status:'LIVE PROGRESS 16/30'});
 });
 assert.match(await page.locator('#a').innerText(),/LIVE PROGRESS 16\/30/);
 assert.equal(await page.locator('.ihtml-tool[data-status="accepted"]').count(),1);
 assert.equal(await page.evaluate(()=>a.chat.busy),true);
 // 原正文去重不能粘住 hidden，让后续真实回答一直不可见，也不能留下空AI作者。
 await page.evaluate(()=>{
  sockets[0].emit({type:'assistant_delta',run_id:'continued',seq:3,background_session:'saved',background_seq:9,delta:'NEW VISIBLE ANSWER'});
  sockets[0].emit({type:'assistant_done',run_id:'continued',seq:4,background_session:'saved',background_seq:10,message:'NEW VISIBLE ANSWER'});
 });
 await page.getByText('NEW VISIBLE ANSWER',{exact:true}).waitFor({state:'visible'});
});

for(const colorScheme of ['dark','light']) test('compact notification inbox marks rendered notices seen without reading background polls: '+colorScheme,async(t)=>{
 const page=await fixture(t,{width:390,height:844});await page.emulateMedia({colorScheme});
 await page.evaluate(async()=>{
  window.seenCalls=0;window.unreadCount=-1;window.listCalls=0;
  window.a=ihtml.mountChat('#a',{locale:'zh-CN',chatHeader:'none',notifications:{title:'助手通知',description:'关闭页面后助手继续处理；需要确认时会等你返回。',push:{publicKey:'test',subscribe:async()=>({enabled:true}),remove:async()=>{}},list:async()=>{listCalls++;return{items:[{id:'notice',title:'清空30个身份的云盘，然后转移给 nbpro',subtitle:'等待确认下一步',meta:'10/29 01:02',unread:true}],unread:1}},seen:async({ids})=>{seenCalls++;return{readIds:ids,unread:0}},onUnreadChange:n=>{unreadCount=n},remove:async()=>false,clear:async()=>false}});await a.ready();
 });
 await page.waitForFunction(()=>listCalls>0);assert.equal(await page.evaluate(()=>seenCalls),0);
 await page.evaluate(()=>a.chat.notifications.open());
 await page.waitForFunction(()=>seenCalls===1&&unreadCount===0);
 assert.equal(await page.locator('.ihtml-chat-history-row.is-unread').count(),0);
 const remove=page.getByRole('button',{name:'删除通知 · 清空30个身份的云盘，然后转移给 nbpro',exact:true});
 assert.equal(await remove.locator('svg').count(),1);
 const pane=page.locator('.ihtml-chat-history').last();
 assert.equal(await pane.evaluate(n=>n.scrollWidth<=n.clientWidth),true);
 await pane.screenshot({path:'/tmp/ihtml-notifications-'+colorScheme+'.png'});
});

test('notification seen failure preserves unread state and does not expose callback errors',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{window.unreadCount=-1;window.a=ihtml.mountChat('#a',{locale:'zh-CN',notifications:{list:async()=>({items:[{id:'unread',title:'原通知',unread:true}],unread:1}),onUnreadChange:n=>{unreadCount=n},seen:async()=>{throw new Error('PRIVATE_READ_ERROR')}}});await a.ready();a.chat.notifications.open();});
 await page.waitForFunction(()=>unreadCount===1);
 await page.getByText('通知已显示，已读状态暂未同步。').waitFor();
 assert.equal(await page.locator('.ihtml-chat-history-row.is-unread').count(),1);
 assert.doesNotMatch(await page.locator('#a').innerText(),/PRIVATE_READ_ERROR/);
});

test('live status remains in collapsed summary across answer deltas and resets at next tool stage',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{window.a=ihtml.mountChat('#a',{locale:'zh-CN'});await a.ready();await a.chat.send('检查任务');const s=sockets[0];s.emit({type:'turn_start',run_id:'progress',seq:1});s.emit({type:'tool_result',run_id:'progress',seq:2,tool_call:{id:'read',title:'读取任务'},tool_status:'success'});s.emit({type:'assistant_delta',run_id:'progress',seq:3,delta:'正在等待原任务。'});s.emit({type:'assistant_status',run_id:'progress',seq:4,status:'已处理 2/30 项，读取 8947 条记录'});s.emit({type:'assistant_delta',run_id:'progress',seq:5,delta:'不会重复提交。'});});
 assert.equal(await page.locator('#a .p-title').textContent(),'已处理 2/30 项，读取 8947 条记录');
 assert.equal(await page.locator('#a .ihtml-process').getAttribute('open'),null);
 await page.evaluate(()=>sockets[0].emit({type:'tool_start',run_id:'progress',seq:6,tool_call:{id:'next',title:'核对结果'}}));
 assert.ok((await page.locator('#a .p-title').textContent()).includes('核对结果'));
 await page.evaluate(()=>sockets[0].emit({type:'assistant_done',run_id:'progress',seq:7,message:'已完成'}));
 assert.ok(!(await page.locator('#a .p-title').textContent()).includes('8947'));
 await page.evaluate(()=>a.destroy());
});

test('readable confirmation metadata renders exact targets and clear action on mobile',async(t)=>{
 const page=await fixture(t,{width:393,height:850});
 await page.evaluate(async()=>{window.a=ihtml.mountChat('#a',{locale:'zh-CN'});await a.ready();await a.chat.send('查看文件');sockets[0].emit({type:'turn_start',run_id:'readable',seq:1});sockets[0].emit({type:'confirmation_required',run_id:'readable',seq:2,tool_call:{id:'preview',title:'查看所选账号的文件清单',method:'POST',path:'/preview',body:{scope:'all',proxySelection:'default'},risk:'medium',reason:'仅查看文件清单和空间占用，不删除文件。',presentation:{confirm_text:'确认查看文件清单',reason_label:'确认后会做什么',facts:[{label:'账号数量',value:'2'},{label:'完整账号名单',value:'one@example.test\ntwo@example.test'},{label:'清理范围',value:'整个 iCloud Drive（包含其他来源文件）'},{label:'网络出口',value:'沿用账号的默认网络设置'}]},confirmation_token:'fixture',confirmation_nonce:'fixture'}});});
 await page.evaluate(()=>sockets[0].emit({type:'assistant_done',run_id:'readable',seq:3,message:'核对名单后可确认查看清单。'}));
 assert.equal(await page.locator('#a .t-actions .ok').textContent(),'确认查看文件清单');
 assert.equal(await page.locator('#a .t-actions .ok').isVisible(),true);
 const facts=await page.locator('#a .t-facts').innerText();assert.ok(facts.includes('two@example.test'));assert.ok(facts.includes('沿用账号的默认网络设置'));assert.ok(!facts.includes('proxySelection'));
 assert.equal(await page.locator('#a .ihtml-tool').evaluate(node=>node.scrollWidth<=node.clientWidth+1),true);
 if(process.env.IHTML_REVIEW_SCREENSHOT) { await page.waitForTimeout(250); await page.screenshot({path:process.env.IHTML_REVIEW_SCREENSHOT}); }
 await page.evaluate(()=>a.destroy());
});

for(const viewport of [{width:393,height:852},{width:1100,height:852}]) test('narrow host prioritizes live progress and keeps accepted receipts compact: '+viewport.width,async(t)=>{
 const page=await fixture(t,viewport);
 await page.evaluate(async()=>{document.querySelector('#a').style.width='min(100%,390px)';window.a=ihtml.mountChat('#a',{locale:'zh-CN'});await a.ready();await a.chat.send('执行原计划');const s=sockets[0];s.emit({type:'turn_start',run_id:'narrow',seq:1});s.emit({type:'confirmation_required',run_id:'narrow',seq:2,tool_call:{id:'accepted',title:'执行所选项目',name:'fixture_execute',method:'POST',path:'/operations',confirmation_nonce:'signed',confirmation_token:'fixture'}});s.emit({type:'tool_result',run_id:'narrow',seq:3,tool_status:'accepted',tool_summary:'本次提交已受理，最新进度见原任务。',tool_call:{id:'accepted',confirmation_nonce:'signed'}});[...document.querySelectorAll('#a .ihtml-msg.assistant')].at(-1)._turn.elapsedMS=3600000;s.emit({type:'assistant_status',run_id:'narrow',seq:4,status:'正在处理：已完成 11/30 项，处理中 1 项，异常 1 项，已确认 48069 条记录'});});
 const box=await page.locator('#a .ihtml-process').boundingBox(),label=await page.locator('#a .p-title').boundingBox(),meta=await page.locator('#a .p-meta').boundingBox();
 assert.ok(label.width>box.width*.75,'progress receives the available width');assert.ok(meta.y>=label.y+label.height,'secondary metadata has its own line');assert.ok((await page.locator('#a .p-meta').textContent()).includes('1小时0分'));
 const receipt=page.locator('#a .ihtml-tool[data-status="accepted"]');assert.equal(await receipt.locator('.t-summary').isVisible(),false);await receipt.locator('summary.t-head').click();assert.ok((await receipt.locator('.tbody').innerText()).includes('本次提交已受理'));assert.equal(await receipt.getAttribute('data-status'),'accepted');
 if(process.env.IHTML_PROGRESS_SCREENSHOT&&viewport.width===393){await receipt.locator('summary.t-head').click();await aScreenshot(page);}
 await page.evaluate(()=>a.destroy());
});
async function aScreenshot(page){await page.waitForTimeout(200);await page.locator('#a').screenshot({path:process.env.IHTML_PROGRESS_SCREENSHOT});}

test('restored progress does not invent elapsed time from the reconnect clock',async(t)=>{
 const page=await fixture(t);await page.evaluate(async()=>{window.a=ihtml.mountChat('#a',{locale:'zh-CN'});await a.ready();await a.chat.send('连接以接收原记录');sockets[0].emit({type:'turn_start',run_id:'old-run',seq:1,background_replay:true});sockets[0].emit({type:'tool_result',run_id:'old-run',seq:2,background_replay:true,tool_call:{id:'query',title:'查询原任务'},tool_status:'success'});sockets[0].emit({type:'assistant_status',run_id:'old-run',seq:3,background_replay:true,status:'已处理 12/30，后台继续执行'});});
 assert.equal(await page.locator('#a .p-title').last().textContent(),'已处理 12/30，后台继续执行');assert.ok(!(await page.locator('#a .p-meta').last().textContent()).includes('秒'));assert.ok((await page.locator('#a .p-meta').last().textContent()).includes('1 条工具记录'));await page.evaluate(()=>a.destroy());
});

// 公共 SDK 将同轮同类操作收成一张卡，批准发送固定签名清单，不触发逐项按钮或扩大后到范围。
test('57 signed operations use one batch confirmation and aggregate independent receipts',async(t)=>{
 const page=await fixture(t,{width:390,height:844});
 await page.evaluate(async()=>{
  const fetchOriginal=window.fetch;window.fetch=async(...args)=>{const response=await fetchOriginal(...args);const read=response.text;response.text=async()=>{const data=JSON.parse(await read());data.batch_confirmations=true;return JSON.stringify(data)};return response};
  window.a=ihtml.mountChat('#a',{locale:'zh-CN'});await a.ready();await a.chat.send('批量设置这些账号');
  const s=sockets[0];s.emit({type:'turn_start',run_id:'batch',seq:1});
  for(let i=0;i<57;i++)s.emit({type:'confirmation_required',run_id:'batch',seq:i+2,tool_call:{id:'item-'+i,name:'update',title:'设置业务用途',method:'PUT',path:'/settings',risk:'medium',reason:'仅更改所列账号的用途',approval_resume:'manual_tool_only',confirmation_nonce:'nonce-'+i,confirmation_token:'signed-'+i,confirmation_expires:Math.floor(Date.now()/1000)+600,presentation:{facts:[{label:'用途',value:'仅直播转播'},{label:'账号',value:'account-'+i+'@example.test'}]}}});
  s.emit({type:'assistant_done',run_id:'batch',seq:60,message:'请确认整批'});
 });
 assert.equal(await page.locator('.ihtml-tool-batch').count(),1);
 assert.equal(await page.locator('.ihtml-trace-actions > .ihtml-tool').count(),0);
 assert.equal(await page.locator('.t-batch-items').isVisible(),false);
 assert.match(await page.locator('.ihtml-tool-batch').innerText(),/操作清单 · 57 项/);
 assert.match(await page.locator('.ihtml-tool-batch').innerText(),/仅直播转播/);
 assert.equal(await page.locator('.ihtml-tool-batch').evaluate(el=>el.scrollWidth<=el.clientWidth),true);
 await page.screenshot({path:'/tmp/ihtml-batch-confirmation-mobile.png',animations:'disabled'});
 await page.locator('.t-batch-list > summary').click();
 assert.match(await page.locator('.t-batch-items').innerText(),/account-56@example.test/);
 await page.getByRole('button',{name:'确认这 57 项操作',exact:true}).click();
 const sent=await page.evaluate(()=>sockets[0].sent.filter(frame=>frame.type==='confirm_tools'));
 assert.equal(sent.length,1);assert.equal(sent[0].tool_calls.length,57);assert.equal(sent[0].tool_calls[56].confirmation_token,'signed-56');
 assert.equal(await page.evaluate(()=>sockets[0].sent.filter(frame=>frame.type==='confirm_tool').length),0);
 await page.evaluate(()=>{
  const s=sockets[0];s.emit({type:'turn_start',run_id:'execute',seq:1});
  for(let i=0;i<57;i++)s.emit({type:'tool_result',run_id:'execute',seq:i+2,tool_call:{id:'item-'+i,confirmation_nonce:'nonce-'+i},tool_status:i===56?'unknown':'success',tool_summary:i===56?'请核对原回执':'已保存'});
  s.emit({type:'assistant_done',run_id:'execute',seq:60,message:'批量处理结束'});
 });
 assert.match(await page.locator('.t-batch-status').innerText(),/56/);
 assert.match(await page.locator('.t-batch-status').innerText(),/1/);
 assert.equal(await page.locator('.ihtml-tool-batch > .t-actions button').isVisible(),false);
});

// 使用 Go ProposedToolCall 的真实零值 JSON，不能只用省略 method/risk 的人工简化回执。
test('serialized receipt references cannot split a signed batch or change its remaining approval',async(t)=>{
 const page=await fixture(t,{width:390,height:844});
 await page.evaluate(async()=>{
  const originalFetch=fetch;window.fetch=async(...args)=>{const r=await originalFetch(...args);const read=r.text;r.text=async()=>JSON.stringify({...JSON.parse(await read()),batch_confirmations:true});return r};
  window.a=ihtml.mountChat('#a',{locale:'zh-CN'});await a.ready();await a.chat.send('原30项');const s=sockets[0];s.emit({type:'turn_start',run_id:'proposal',seq:1});
  for(let i=0;i<30;i++)s.emit({type:'confirmation_required',run_id:'proposal',seq:i+2,tool_call:{id:'item-'+i,name:'any_write',confirmation_group:'original',title:'同类设置',method:'PUT',path:'/settings',reason:'仅执行原清单',risk:'medium',requires_confirmation:true,approval_resume:'manual_tool_only',confirmation_nonce:'nonce-'+i,confirmation_token:'SIGN-'+i,confirmation_expires:Math.floor(Date.now()/1000)+600,presentation:{facts:[{label:'目标',value:'target-'+i},{label:'操作影响',value:'仅执行原清单'}]}}});
  s.emit({type:'assistant_done',run_id:'proposal',seq:32,message:'请核对原清单'});
 });
 await page.locator('.t-batch-list > summary').click();
 await page.locator('.ihtml-tool').nth(0).locator('summary.t-head').click();
 await page.locator('.ihtml-tool').nth(0).locator('.t-actions button.ok').click();
 await page.evaluate(()=>{
  const s=sockets[0];s.emit({type:'turn_start',run_id:'execution',seq:1});
  for(let i=0;i<2;i++)s.emit({type:'tool_start',run_id:'execution',seq:i+2,tool_call:{id:'item-'+i,confirmation_nonce:'nonce-'+i,method:'',path:'',risk:'',reason:'',requires_confirmation:false}});
 });
 assert.equal(await page.locator('.ihtml-tool-batch').count(),1);assert.match(await page.locator('.t-batch-title').innerText(),/30/);
 assert.match(await page.locator('.t-batch-status').innerText(),/执行中 2/);assert.match(await page.locator('.t-batch-status').innerText(),/待确认 28/);
 await page.evaluate(()=>{const s=sockets[0];for(let i=0;i<2;i++)s.emit({type:'tool_result',run_id:'execution',seq:i+4,tool_call:{id:'item-'+i,confirmation_nonce:'nonce-'+i,method:'',path:'',risk:'',reason:'',requires_confirmation:false},tool_status:'success'});s.emit({type:'assistant_done',run_id:'execution',seq:6,message:'2项已返回'});});
 await page.getByRole('button',{name:'确认这 28 项操作',exact:true}).click();
 const frames=await page.evaluate(()=>sockets[0].sent.filter(x=>x.type==='confirm_tools'));
 assert.equal(frames.length,1);assert.equal(frames[0].tool_calls.length,28);assert.equal(frames[0].tool_calls[0].method,'PUT');assert.equal(frames[0].tool_calls[0].reason,'仅执行原清单');
 assert.equal(await page.locator('.ihtml-tool-batch').count(),1);
 await page.locator('.t-batch-list > summary').click();await page.locator('.ihtml-tool-batch').scrollIntoViewIfNeeded();
 await page.locator('#a').screenshot({path:'/tmp/ihtml-stable-batch-after.png'});
});

test('display-only tool history folds receipts without AI authors, action authority or lost snapshot data',async(t)=>{
 const page=await fixture(t,{width:390,height:844});
 await page.evaluate(async()=>{
  window.initial=[{role:'user',content:'原请求'},{role:'assistant',content:'请核对确认范围',contentFormat:'markdown'},...Array.from({length:28},()=>({role:'tool',content:'原操作接口已返回，请核对实际结果。',tool_call:{confirmation_nonce:'old',confirmation_token:'PRIVATE'},contentFormat:'markdown'})),{role:'assistant',content:'已查询原记录，28项已完成。'},{role:'assistant',content:' '}];
  window.a=ihtml.mountChat('#a',{locale:'zh-CN',initialMessages:initial});await a.ready();
 });
 const group=page.locator('.ihtml-receipt-history');assert.equal(await group.count(),1);assert.match(await group.locator('summary').innerText(),/28/);assert.equal(await group.locator('ol').isVisible(),false);
 // welcome 仍是独立引导，不能为28项回执各制造AI作者。
 assert.equal(await page.locator('.ihtml-msg.assistant').count(),2);assert.equal(await page.locator('.ihtml-answer-history').count(),1);assert.equal(await page.locator('.ihtml-tool, .t-actions button').count(),0);
 await group.locator('summary').click();assert.equal(await group.locator('li').count(),1);assert.match(await group.innerText(),/× 28/);
 const saved=await page.evaluate(()=>a.chat.getMessages());assert.equal(saved.filter(m=>m.role==='tool').length,28);assert.ok(saved.every(m=>!m.tool_call));assert.ok(saved.filter(m=>m.role==='tool').every(m=>m.contentFormat==='text'));
 await page.locator('#a').screenshot({path:'/tmp/ihtml-receipt-history-after.png'});
});

test('batch host rejection prevents every member from being sent',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{
  const oldFetch=fetch;window.fetch=async(...args)=>{const r=await oldFetch(...args);const read=r.text;r.text=async()=>JSON.stringify({...JSON.parse(await read()),batch_confirmations:true});return r};
  window.allowBatch=false;window.checks=0;
  window.a=ihtml.mountChat('#a',{beforeConfirm:({toolCall})=>{checks++;toolCall.title='changed local copy';return allowBatch||checks!==2}});await a.ready();await a.chat.send('batch');
  window.emitCard=(i,seq)=>sockets[0].emit({type:'confirmation_required',run_id:'proposal',seq,tool_call:{id:'batch-'+i,name:'write',title:'Original operation',method:'PUT',path:'/write',confirmation_group:'original',confirmation_nonce:'nonce-'+i,confirmation_token:'SIGNED',confirmation_expires:Math.floor(Date.now()/1000)+600,approval_resume:'manual_tool_only',body:{target:i}}});
  sockets[0].emit({type:'turn_start',run_id:'proposal',seq:1});emitCard(1,2);emitCard(2,3);sockets[0].emit({type:'assistant_done',run_id:'proposal',seq:4,message:'Review'});
 });
 await page.locator('.ihtml-tool-batch > .t-actions button').click();
 await page.waitForFunction(()=>checks===2&&!a.chat.busy);
 assert.equal(await page.evaluate(()=>sockets[0].sent.some(f=>f.type==='confirm_tools')),false);

});

// 任意业务工具使用相同汇总机制，不按接口名、HTTP 路径或特定字段识别批量操作。
test('batch grouping is shared by unrelated tools and keeps each tool scope separate',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{
  const original=fetch;window.fetch=async(...args)=>{const r=await original(...args);const read=r.text;r.text=async()=>JSON.stringify({...JSON.parse(await read()),batch_confirmations:true});return r};
  window.a=ihtml.mountChat('#a');await a.ready();await a.chat.send('several batches');
  const s=sockets[0];let seq=1;s.emit({type:'turn_start',run_id:'generic',seq:seq++});
  for(const name of ['rename_folder','adjust_switch','remove_record'])for(let i=0;i<2;i++)s.emit({type:'confirmation_required',run_id:'generic',seq:seq++,tool_call:{id:name+i,name,title:name,method:'TOOL',path:'/arbitrary/'+name+'/'+i,confirmation_group:'same-original-turn',approval_resume:'manual_tool_only',confirmation_nonce:name+i,confirmation_token:'SIGNED',confirmation_expires:Math.floor(Date.now()/1000)+600,body:{arbitraryTarget:i}}});
  s.emit({type:'assistant_done',run_id:'generic',seq:seq++,message:'Review'});
 });
 assert.equal(await page.locator('.ihtml-tool-batch').count(),3);
 await page.locator('.ihtml-tool-batch').first().locator(':scope > .t-actions button').click();
 const frame=await page.evaluate(()=>sockets[0].sent.find(f=>f.type==='confirm_tools'));
 assert.equal(frame.tool_calls.length,2);
 assert.ok(frame.tool_calls.every(call=>call.name==='rename_folder'));
 assert.equal(await page.locator('.ihtml-tool[data-status="pending"]').count(),4);
});

// 会话级完成通知不伪造新轮次、不重发调用，也不把业务受理显示为业务完成。
test('background call completion reaches host notifications without adding a conversation message',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{
  window.a=ihtml.mountChat('#a',{onChatEvent:event=>events.push(event)});await a.ready();await a.chat.send('query');
  sockets[0].emit({type:'turn_start',run_id:'task-owner',seq:1});
  sockets[0].emit({type:'assistant_done',run_id:'task-owner',seq:2,message:'Call continues in the background'});
  window.framesBefore=sockets[0].sent.length;window.cardsBefore=a.root.querySelectorAll(".ihtml-msg.assistant").length;
  sockets[0].emit({type:'task_complete',task_id:'opaque-task',task_state:'completed',tool_name:'arbitrary_tool'});
 });
 assert.equal(await page.evaluate(()=>sockets[0].sent.length),await page.evaluate(()=>framesBefore));
 assert.equal(await page.evaluate(()=>events.filter(event=>event.type==='task_complete').length),1);
 assert.doesNotMatch(await page.locator('#a').innerText(),/opaque-task|后台调用已返回|Background call returned/);
 assert.equal(await page.evaluate(()=>events.find(event=>event.type==='task_complete').task_id),'opaque-task');
 assert.equal(await page.locator('#a .ihtml-msg.assistant').count(),await page.evaluate(()=>cardsBefore));
});

// 提交状态只描述回执，不能挤占业务结论；过程入口、说明和通知在实时与历史保持同一层次。
for(const mode of ['light','dark'])for(const width of [390,1100])test(`submission receipts stay distinct from outcomes in live and restored UI ${mode} ${width}`,async(t)=>{
 const page=await fixture(t,{width,height:900});await page.evaluate(async({mode,width})=>{
  const original=fetch;window.fetch=async(...args)=>{const r=await original(...args),text=r.text;r.text=async()=>JSON.stringify({...JSON.parse(await text()),batch_confirmations:true});return r};
  const call=i=>({id:'op-'+i,name:'arbitrary_operation',title:'Apply selected scope',method:'POST',path:'/objects',body:{target:'item-'+i},confirmation_nonce:'n'+i,confirmation_token:'SIGNED'+i});
  window.a=ihtml.mountChat('#a',{locale:'zh-CN',onChatEvent:e=>events.push(e)});await a.ready();a.theme.set({mode},{persist:false});a.root.style.width=width+'px';await a.chat.send('Apply the selected scope');
  const socket=sockets[0];socket.emit({type:'turn_start',run_id:'r',seq:1,flow_id:'f'});for(let i=0;i<2;i++){socket.emit({type:'confirmation_required',run_id:'r',seq:2+i*2,flow_id:'f',tool_call:call(i)});socket.emit({type:'tool_result',run_id:'r',seq:3+i*2,flow_id:'f',tool_call:call(i),tool_status:'accepted',tool_result:{id:'original-job-'+i}})}socket.emit({type:'assistant_done',run_id:'r',seq:6,flow_id:'f',content_format:'markdown',message:'The original work is complete.'});
  window.b=ihtml.mountChat('#b',{locale:'zh-CN',initialMessages:[{id:'u',role:'user',content:'Apply the selected scope'},{id:'a',role:'assistant',flowId:'f',runId:'r',contentFormat:'markdown',content:'The original work is complete.'}],initialOperations:[0,1].map(i=>({id:'op-'+i,afterMessageId:'u',flowId:'f',runId:'r',sequence:3+i*2,status:'accepted',proposalRecorded:true,call:call(i),result:{id:'original-job-'+i}}))});await b.ready();b.theme.set({mode},{persist:false});b.root.style.width=width+'px';
  socket.emit({type:'task_complete',task_id:'INTERNAL_TASK_ID',task_state:'completed'});
 },{mode,width});
 for(const selector of ['#a','#b']){
  assert.equal(await page.locator(selector+' .p-title').innerText(),'处理过程');
  const group=page.locator(selector+' .ihtml-tool-batch');assert.equal(await group.evaluate(e=>e.open),false);assert.match(await group.locator(':scope > summary').innerText(),/操作清单.*已提交 2/s);
  assert.doesNotMatch(await page.locator(selector).innerText(),/INTERNAL_TASK_ID|后台调用已返回|操作请求已受理/);
  await group.locator(':scope > summary').click();assert.match(await group.locator(':scope > .t-meta').innerText(),/已提交表示请求已受理/);
  assert.match(await group.locator(':scope > summary').getAttribute('aria-label'),/Apply selected scope/);
  await group.locator('.t-batch-list > summary').click();await group.locator('.ihtml-tool > summary').first().click();assert.match(await group.innerText(),/提交回执/);
  assert.equal(await page.locator(selector+' .ihtml-chat-log').evaluate(e=>e.scrollWidth>e.clientWidth),false);
  await group.locator(':scope > summary').click();await page.locator(selector).screenshot({path:`/tmp/ihtml-polish-${selector.slice(1)}-${mode}-${width}.png`});
 }
 assert.equal(await page.evaluate(()=>events.filter(e=>e.type==='task_complete'&&e.task_id==='INTERNAL_TASK_ID').length),1);
});

// 模型挂起后准备完成的确认卡仍汇入原批次；迟到正文不能改写已完成答复。
test('late background confirmations keep the original batch after model suspension',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{
  const original=fetch;window.fetch=async(...args)=>{const r=await original(...args);const read=r.text;r.text=async()=>JSON.stringify({...JSON.parse(await read()),batch_confirmations:true});return r};
  window.a=ihtml.mountChat('#a');await a.ready();await a.chat.send('start in background');
  sockets[0].emit({type:'turn_start',run_id:'suspended-origin',seq:1});
  sockets[0].emit({type:'assistant_done',run_id:'suspended-origin',seq:2,message:'Waiting for events',finish_reason:'task_waiting'});
  sockets[0].emit({type:'assistant_delta',run_id:'suspended-origin',seq:3,delta:'STALE_BODY'});
  for(let i=0;i<2;i++)sockets[0].emit({type:'confirmation_required',run_id:'suspended-origin',seq:4+i,tool_call:{id:'late-'+i,name:'arbitrary_update',title:'Update',method:'PUT',path:'/arbitrary/'+i,confirmation_group:'suspended-origin',approval_resume:'manual_tool_only',confirmation_nonce:'late-nonce-'+i,confirmation_token:'SIGNED',confirmation_expires:Math.floor(Date.now()/1000)+600,body:{target:i}}});
 });
 assert.equal(await page.locator('#a .ihtml-tool-batch').count(),1);
 assert.equal(await page.locator('#a .ihtml-tool-batch .ihtml-tool').count(),2);
 assert.doesNotMatch(await page.locator('#a').innerText(),/STALE_BODY/);
 await page.locator('#a .ihtml-tool-batch > .t-actions button').click();
 const frame=await page.evaluate(()=>sockets[0].sent.find(f=>f.type==='confirm_tools'));
 assert.equal(frame.tool_calls.length,2);
});

// 任意工具都使用实时卡片渲染器，历史仅移除授权，不能丢掉范围、差异或公开结果。
test('structured operation history preserves approval scope and per-item results without restoring authority',async(t)=>{
 const page=await fixture(t,{width:390,height:844});
 await page.evaluate(async()=>{
  window.a=ihtml.mountChat('#a',{locale:'zh-CN',initialMessages:[{id:'question',role:'user',content:'通用批量操作'},{id:'reply',role:'assistant',content:'原始回复'}],initialOperations:['success','accepted','unknown','pending'].map((status,i)=>({id:'history-'+i,afterMessageId:'question',status,proposalRecorded:true,summary:'实际回执 '+i,result:{saved:status==='success',jobId:'original-'+i,api_key:'SECRET'},call:{name:'arbitrary_tool',title:'任意资源变更',method:'PUT',path:'/resources',confirmation_group:'original',reason:'只修改原始范围',confirmation_token:'OLD_SECRET',confirmation_nonce:'OLD_NONCE',requires_confirmation:true,presentation:{facts:[{label:'目标',value:'resource-'+i},{label:'用途',value:'原目标值'}]},review:{scope:'原始范围',before:{value:'旧值'},after:{value:'新值'}}}}))});await a.ready();
 });
 assert.equal(await page.locator('.ihtml-tool-batch').count(),1);assert.match(await page.locator('.ihtml-tool-batch').innerText(),/完成 1.*已提交 1.*结果未知 1/);
 assert.equal(await page.locator('.ihtml-operation-history .t-actions button:visible').count(),0);
 assert.equal(await page.locator('.ihtml-tool-batch').evaluate(e=>e.open),false);await page.locator('.t-batch-head').click();
 await page.locator('.t-batch-list > summary').click();await page.locator('.ihtml-tool').first().locator('summary.t-head').click();
 assert.match(await page.locator('.ihtml-tool').first().innerText(),/resource-0/);assert.match(await page.locator('.ihtml-tool').first().innerText(),/旧值/);assert.match(await page.locator('.ihtml-tool').first().innerText(),/新值/);
 await page.locator('.ihtml-tool').first().locator('.t-tech > summary').click();assert.match(await page.locator('.ihtml-tool').first().innerText(),/original-0/);
 const exported=await page.evaluate(()=>JSON.stringify(a.options||{}));assert.ok(!exported.includes('OLD_SECRET'));
 assert.ok(!(await page.locator('#a').innerText()).includes('SECRET'));
 assert.equal(await page.evaluate(()=>sockets.some(s=>s.sent.some(f=>['confirm_tool','confirm_tools','cancel_tool'].includes(f.type)))),false);
 await page.locator('#a').screenshot({path:'/tmp/ihtml-structured-history.png'});
});

test('live evidence updates historical operation in place and old card cannot authorize execution',async(t)=>{
 const page=await fixture(t);await page.evaluate(async()=>{
  window.a=ihtml.mountChat('#a',{initialMessages:[{id:'u',role:'user',content:'original'}],initialOperations:[{id:'same-call',afterMessageId:'u',status:'running',proposalRecorded:true,call:{name:'write',title:'Original target',method:'PUT',path:'/old',presentation:{facts:[{label:'Target',value:'original-resource'}]}}}]});await a.ready();await a.chat.send('status');
  const s=sockets[0];s.emit({type:'turn_start',run_id:'restore',seq:1});s.emit({type:'tool_result',run_id:'restore',seq:2,tool_call:{id:'same-call',confirmation_nonce:'server-reference',method:'',path:'',risk:'',reason:''},tool_status:'success',tool_summary:'authoritative receipt',tool_result:{saved:true}});s.emit({type:'assistant_done',run_id:'restore',seq:3,message:'Updated'});
 });
 assert.equal(await page.locator('.ihtml-tool').count(),1);assert.match(await page.locator('.ihtml-tool').innerText(),/authoritative receipt/);await page.locator('.ihtml-tool > summary').click();assert.match(await page.locator('.ihtml-tool').innerText(),/original-resource/);
 assert.equal(await page.locator('.ihtml-operation-history button').count(),0);
});

test('operation ID links legacy text to structured receipts without merging unrelated identical summaries',async(t)=>{
 const page=await fixture(t);await page.evaluate(async()=>{window.a=ihtml.mountChat('#a',{locale:'zh-CN',initialMessages:[{id:'u',role:'user',content:'original'},{id:'one',operationId:'op-one',role:'tool',content:'identical'},{id:'two',operationId:'op-two',role:'tool',content:'identical'}],initialOperations:['one','two'].map(id=>({id:'op-'+id,afterMessageId:'u',status:'success',receiptOnly:true,proposalRecorded:false,summary:'identical',result:{saved:true},call:{name:'unrelated_'+id,title:'Operation '+id}}))});await a.ready()});
 assert.equal(await page.locator('.ihtml-receipt-history').count(),0);assert.equal(await page.locator('.ihtml-tool').count(),2);assert.equal(await page.locator('.ihtml-tool-batch').count(),0);
 await page.locator('.ihtml-tool > summary').first().click();assert.match(await page.locator('.ihtml-tool').first().innerText(),/未保存原确认范围/);assert.match(await page.locator('.ihtml-tool').first().innerText(),/saved/);
 assert.equal(await page.locator('.ihtml-operation-history button').count(),0);
});

test('operation history pages join the original batch and retain per-item results',async(t)=>{
 const page=await fixture(t);await page.evaluate(async()=>{
  const record=i=>({id:'op-'+i,afterMessageId:'u',status:'success',proposalRecorded:true,summary:'saved '+i,result:{saved:true,target:'resource-'+i},call:{name:'write',title:'Original group',confirmation_group:'group',presentation:{facts:[{label:'Target',value:'resource-'+i}]}}});
  window.a=ihtml.mountChat('#a',{initialMessages:[{id:'u',role:'user',content:'original'}],initialOperations:[record(2)],operationHistory:{cursor:'older',load:async({cursor,signal})=>{if(cursor!=='older'||signal.aborted)throw Error('invalid cursor');return{items:[record(1)],cursor:''}}}});await a.ready();
 });
 await page.locator('.t-history-more').click();await page.waitForFunction(()=>!document.querySelector('.t-history-more'));
 assert.equal(await page.locator('.ihtml-operation-history').count(),1);assert.equal(await page.locator('.ihtml-tool-batch').count(),1);assert.equal(await page.locator('.ihtml-tool-batch').evaluate(e=>e.open),false);await page.locator('.t-batch-head').click();await page.locator('.t-batch-list > summary').click();assert.equal(await page.locator('.ihtml-tool').count(),2);assert.equal(await page.locator('.ihtml-operation-history button:visible').count(),0);
});

test('late proposals and duplicate terminal references preserve verified history scope and result',async(t)=>{
 const page=await fixture(t);await page.evaluate(async()=>{
  window.a=ihtml.mountChat('#a',{initialMessages:[{id:'u',role:'user',content:'original'}],initialOperations:[{id:'known',afterMessageId:'u',status:'success',proposalRecorded:true,summary:'verified original receipt',result:{saved:true},call:{name:'any_write',title:'Original range',method:'PUT',path:'/original',presentation:{facts:[{label:'Target',value:'original-target'}]}}}]});await a.ready();await a.chat.send('read status');
  const s=sockets[0];s.emit({type:'turn_start',run_id:'replay',seq:1});s.emit({type:'confirmation_required',run_id:'replay',seq:2,tool_call:{id:'known',name:'any_write',title:'late proposal',confirmation_nonce:'old',confirmation_token:'OLD',body:{target:'different'}},tool_status:'pending'});s.emit({type:'tool_start',run_id:'replay',seq:3,tool_call:{id:'known'},tool_status:'running'});s.emit({type:'tool_result',run_id:'replay',seq:4,tool_call:{id:'known'},tool_status:'success'});s.emit({type:'assistant_done',run_id:'replay',seq:5,message:'done'});
 });
 assert.equal(await page.locator('.ihtml-tool').count(),1);await page.locator('.ihtml-tool > summary').click();const text=await page.locator('.ihtml-tool').innerText();assert.match(text,/original-target/);assert.match(text,/saved/);assert.match(text,/verified original receipt/);assert.ok(!text.includes('late proposal'));assert.equal(await page.locator('.ihtml-operation-history button:visible').count(),0);
});

// 任意历史批次都收起外层；键盘展开和后续分页不丢失用户选择，逐项回执仍能核对。
test('historical batch summaries collapse compactly and preserve manual expansion across pagination',async(t)=>{
 const page=await fixture(t,{width:390,height:844});await page.evaluate(async()=>{
  const row=i=>({id:'scope-'+i,afterMessageId:'u',status:i===1?'unknown':'success',proposalRecorded:true,result:{receipt:'original-'+i},call:{name:'arbitrary_inspect',title:'An arbitrary operation',confirmation_group:'same'}});
  window.a=ihtml.mountChat('#a',{initialMessages:[{id:'u',role:'user',content:'original'}],initialOperations:[row(2),row(1)],operationHistory:{cursor:'more',load:async()=>({items:[row(0)],cursor:''})}});await a.ready();
 });
 const batch=page.locator('.ihtml-tool-batch');assert.equal(await batch.evaluate(e=>e.open),false);assert.equal(await page.locator('.t-batch-list').isVisible(),false);
 assert.ok(await batch.evaluate(e=>e.getBoundingClientRect().height<105&&e.scrollWidth<=e.clientWidth));
 await batch.locator(':scope > summary').focus();await page.keyboard.press('Enter');assert.equal(await batch.evaluate(e=>e.open),true);
 await page.locator('.t-history-more').click();await page.waitForFunction(()=>!document.querySelector('.t-history-more'));assert.equal(await batch.evaluate(e=>e.open),true);
 await batch.locator('.t-batch-list > summary').click();await batch.locator('.ihtml-tool').first().locator('summary.t-head').click();assert.match(await batch.innerText(),/original-2/);
 await batch.locator(':scope > summary').click();assert.equal(await batch.evaluate(e=>e.open),false);assert.equal(await page.locator('.ihtml-operation-history button:visible').count(),0);
});

// 同一操作状态决定两端展开方式；历史只读不恢复按钮，错误与未知回执在两端都可收起核对。
test('historical cards use the same active and settled disclosure states without restoring authority',async(t)=>{
 const page=await fixture(t);await page.evaluate(async()=>{
  window.a=ihtml.mountChat('#a',{initialOperations:['error','unknown','pending','expired'].map((status,i)=>({id:'attention-'+i,status,proposalRecorded:true,result:{note:'original receipt '+i},call:{name:'different_'+i,title:'Original operation '+i}}))});await a.ready();
 });
 assert.equal(await page.locator('.ihtml-operation-history .ihtml-tool[open]').count(),2);
 assert.equal(await page.locator('.ihtml-operation-history .ihtml-tool[data-status="pending"]').evaluate(e=>e.open),true);
 assert.equal(await page.locator('.ihtml-operation-history .ihtml-tool').count(),4);
 const first=page.locator('.ihtml-operation-history .ihtml-tool').first();await first.locator('summary.t-head').click();assert.match(await first.innerText(),/original receipt 0/);
 assert.equal(await page.locator('.ihtml-operation-history .t-actions button:visible').count(),0);
});

// 历史默认折叠不能牺牲实时确认；确定受理/完成后则自动收起，结果摘要仍保留。
test('live batch confirmation stays open until settled and then collapses automatically',async(t)=>{
 const page=await fixture(t);await page.evaluate(async()=>{
  const original=window.fetch;window.fetch=async(...args)=>{const r=await original(...args),read=r.text;r.text=async()=>JSON.stringify({...JSON.parse(await read()),batch_confirmations:true});return r};
  window.a=ihtml.mountChat('#a',{});await a.ready();await a.chat.send('original');const s=sockets[0];s.emit({type:'turn_start',run_id:'batch',seq:1});
  for(let i=0;i<2;i++)s.emit({type:'confirmation_required',run_id:'batch',seq:i+2,tool_call:{id:'live-'+i,name:'any_operation',confirmation_nonce:'n'+i,confirmation_token:'signed'+i}});
  s.emit({type:'assistant_done',run_id:'batch',seq:4,message:'review'});
 });
 const batch=page.locator('.ihtml-tool-batch');assert.equal(await batch.evaluate(e=>e.open),true);assert.equal(await batch.locator(':scope > .t-actions button').isVisible(),true);await batch.locator(':scope > .t-actions button').click();
 await page.evaluate(()=>{const s=sockets[0];s.emit({type:'turn_start',run_id:'settled',seq:1});for(let i=0;i<2;i++)s.emit({type:'tool_result',run_id:'settled',seq:i+2,tool_call:{id:'live-'+i},tool_status:i?'accepted':'success',tool_result:{saved:true}});s.emit({type:'assistant_done',run_id:'settled',seq:4,message:'receipts saved'});});
 assert.equal(await batch.evaluate(e=>e.open),false);assert.match(await batch.locator('.t-batch-status').innerText(),/1/);
 assert.equal(await batch.locator(':scope > summary').evaluate(e=>e===document.activeElement),true,'settled approval keeps keyboard focus on the original batch');
 await batch.locator(':scope > summary').click();assert.equal(await batch.locator('.t-batch-list').isVisible(),true);assert.equal(await batch.locator(':scope > .t-actions button').isVisible(),false);
});

// 同一份公开操作快照经实时事件或历史输入，都必须产生同一种卡片与分组；只读仅去掉授权控件。
test('live and historical operations share grouping disclosure scope and result placement for every status',async(t)=>{
 const page=await fixture(t);for(const batchEnabled of [true,false]){await page.evaluate(async(batchEnabled)=>{
  window.a?.destroy?.();window.b?.destroy?.();sockets.length=0;
  const original=window.fetch;window.fetch=async(...args)=>{const r=await original(...args),read=r.text;r.text=async()=>JSON.stringify({...JSON.parse(await read()),batch_confirmations:batchEnabled});return r};
  const statuses=['pending','running','success','accepted','error','unknown','cancelled','expired'];
  const call=(status,i)=>({id:status+i,name:'arbitrary_operation',title:'Same operation',method:'PUT',path:'/objects',body:{target:'one'},reason:'Exact original scope',confirmation_group:status,confirmation_nonce:'nonce-'+status+i,confirmation_token:'signed-'+status+i});
  const receipt=status=>['pending','running'].includes(status)?undefined:{state:status,reference:'original'};
  window.a=ihtml.mountChat('#a',{locale:'en'});await a.ready();await a.chat.send('original');const s=sockets[0];s.emit({type:'turn_start',run_id:'same',seq:1});let seq=2;
  for(const status of statuses)for(let i=0;i<2;i++){s.emit({type:'confirmation_required',run_id:'same',seq:seq++,tool_call:call(status,i),tool_summary:'Original receipt',tool_updated_at:'2026-11-06T08:00:00Z'});if(status!=='pending')s.emit({type:status==='running'?'tool_start':'tool_result',run_id:'same',seq:seq++,tool_call:call(status,i),tool_status:status,tool_updated_at:'2026-11-06T08:00:00Z',tool_summary:'Original receipt',tool_result:receipt(status)});}
  s.emit({type:'assistant_done',run_id:'same',seq:seq++,message:'same public records'});
  window.b=ihtml.mountChat('#b',{locale:'en',initialOperations:statuses.flatMap(status=>[0,1].map(i=>({id:status+i,status,runId:'same',sequence:2+i,updatedAt:'2026-11-06T08:00:00Z',proposalRecorded:true,summary:'Original receipt',result:receipt(status),call:call(status,i)})))});await b.ready();
 },batchEnabled);
 const read=selector=>page.locator(selector).evaluate(root=>[...root.querySelectorAll('.ihtml-tool-batch')].map(g=>({open:g.open,summary:g.querySelector('.t-batch-head').textContent,cards:[...g.querySelectorAll('.ihtml-tool')].map(c=>({status:c.dataset.status,open:c.open,colors:[getComputedStyle(c).color,getComputedStyle(c).backgroundColor,getComputedStyle(c).borderColor,getComputedStyle(c.querySelector('.t-status')).color,getComputedStyle(c.querySelector('.t-status')).backgroundColor],time:c.querySelector('time')?.dateTime,title:c.querySelector('.t-title').textContent,scope:[...c.querySelectorAll('.tbody > .t-reason,.tbody > .t-facts')].map(e=>e.textContent),result:[...c.querySelectorAll('.tbody > .t-tech-section pre')].map(e=>e.textContent),technical:[...c.querySelectorAll('.t-tech pre')].map(e=>e.textContent)}))})));
 const live=await read('#a'),history=await read('#b');assert.equal(live.length,batchEnabled?8:7);assert.deepEqual(history,live);
 const singles=selector=>page.locator(selector+' .ihtml-trace-actions > .ihtml-tool').evaluateAll(cards=>cards.map(c=>({status:c.dataset.status,open:c.open,colors:[getComputedStyle(c).color,getComputedStyle(c).backgroundColor,getComputedStyle(c).borderColor,getComputedStyle(c.querySelector('.t-status')).color,getComputedStyle(c.querySelector('.t-status')).backgroundColor],time:c.querySelector('time')?.dateTime,title:c.querySelector('.t-title').textContent})));assert.deepEqual(await singles('#b'),await singles('#a'));
 assert.equal(await page.locator('#a .ihtml-tool-batch > .t-actions button:visible').count(),batchEnabled?1:0);assert.equal(await page.locator('#b .t-actions button:visible').count(),0);
 }});

// 迟到分页与实时结果交错时仍是同一记录；补回提案只补范围，不回退状态或增加空历史块。
test('history pagination and live receipts share monotonic merge without duplicate wrappers',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{
  window.pages=[{items:[{id:'op',afterMessageId:'different-anchor',status:'running',runId:'run',sequence:3,updatedAt:'2026-11-06T08:00:03Z',call:{id:'op'}}],cursor:'proposal'},{items:[{id:'op',afterMessageId:'different-anchor',status:'pending',proposalRecorded:true,updatedAt:'2026-11-06T08:00:01Z',call:{id:'op',name:'arbitrary',title:'Frozen scope',body:{target:'original-target'}}}],cursor:''}];
  window.a=ihtml.mountChat('#a',{locale:'en',initialMessages:[{id:'u',role:'user',content:'original'}],initialOperations:[{id:'op',afterMessageId:'u',status:'running',receiptOnly:true,runId:'run',sequence:2,updatedAt:'2026-11-06T08:00:02Z',call:{id:'op',name:'arbitrary'}}],operationHistory:{cursor:'older',load:async()=>pages.shift()}});await a.ready();await a.chat.send('read');
  const s=sockets[0];s.emit({type:'turn_start',run_id:'run',seq:1});s.emit({type:'tool_result',run_id:'run',seq:4,tool_call:{id:'op'},tool_status:'success',tool_result:{saved:true},tool_summary:'verified receipt',tool_updated_at:'2026-11-06T08:00:04Z'});s.emit({type:'assistant_done',run_id:'run',seq:5,message:'done'});
 });
 await page.locator('.t-history-more').click();await page.waitForFunction(()=>pages.length===1&&document.querySelector('.t-history-more')&&!document.querySelector('.t-history-more').disabled);
 assert.equal(await page.locator('.ihtml-operation-history').count(),1);assert.equal(await page.locator('.ihtml-tool[data-status="success"]').count(),1);
 await page.locator('.t-history-more').click();await page.waitForFunction(()=>!document.querySelector('.t-history-more'));
 assert.equal(await page.locator('.ihtml-operation-history').count(),1);assert.equal(await page.locator('.ihtml-tool').count(),1);assert.equal(await page.locator('.ihtml-tool').getAttribute('data-status'),'success');
 await page.locator('.ihtml-tool > summary').click();const text=await page.locator('.ihtml-tool').innerText();assert.match(text,/original-target/);assert.match(text,/saved/);assert.match(text,/verified receipt/);assert.equal(await page.locator('.ihtml-operation-history button').count(),0);
});

// 过程归过程、确认归操作，来源不决定展示层；对照实际主题的颜色、边框和文字样式。
for(const mode of ['light','dark'])for(const width of [390,1100])test(`live and restored turns preserve process/action layout and colors ${mode} ${width}`,async(t)=>{
 const page=await fixture(t,{width,height:900});
 await page.evaluate(async({mode,width})=>{
  const processNames=['tool_search','future_internal','arbitrary_read'];
  const operations=processNames.map((name,i)=>({id:'read-'+i,afterMessageId:'u',runId:'r',sequence:i+2,updatedAt:'2026-11-06T08:00:00Z',status:'success',summary:'internal receipt',call:{id:'read-'+i,name,title:name},result:{checked:true}}));
  const call={id:'change',name:'unrelated_write',title:'Apply original scope',method:'PUT',path:'/objects/one',risk:'high',reason:'Only this original object',confirmation_nonce:'nonce',confirmation_token:'SIGNED',body:{target:'one'}};
  operations.push({id:'change',afterMessageId:'u',runId:'r',sequence:5,updatedAt:'2026-11-06T08:00:00Z',status:'pending',proposalRecorded:true,call});
  window.a=ihtml.mountChat('#a',{locale:'en'});await a.ready();a.theme.set({mode},{persist:false});a.root.style.width=width+'px';await a.chat.send('Original question');
  const s=sockets[0];s.emit({type:'turn_start',run_id:'r',seq:1});let seq=2;
  for(const op of operations.slice(0,-1))s.emit({type:'tool_result',run_id:'r',seq:seq++,tool_call:op.call,tool_status:op.status,tool_summary:op.summary,tool_result:op.result});
  s.emit({type:'confirmation_required',run_id:'r',seq:seq++,tool_call:call});s.emit({type:'assistant_done',run_id:'r',seq:seq++,message:'Original answer'});
  window.b=ihtml.mountChat('#b',{locale:'en',initialMessages:[{id:'u',role:'user',content:'Original question'},{id:'reply',role:'assistant',content:'Original answer'}],initialOperations:operations});await b.ready();b.theme.set({mode},{persist:false});b.root.style.width=width+'px';
 },{mode,width});
 const read=selector=>page.locator(selector).evaluate(root=>{
  const article=[...root.querySelectorAll('.ihtml-msg.assistant')].find(c=>c.querySelector('.answer')?.textContent==='Original answer');
  const styles=e=>e?Object.fromEntries(['color','backgroundColor','borderColor','borderRadius','fontSize','fontWeight','lineHeight','padding','margin','display'].map(k=>[k,getComputedStyle(e)[k]])):null;
  return {author:article.querySelector('.author')?.textContent,processOpen:article.querySelector('.ihtml-process').open,processNames:[...article.querySelectorAll('.pbody > .ihtml-tool')].map(c=>c.querySelector('.t-title').textContent),actionNames:[...article.querySelectorAll('.ihtml-trace-actions > .ihtml-tool')].map(c=>c.querySelector('.t-title').textContent),processStyles:styles(article.querySelector('.ihtml-process')),actionStyles:styles(article.querySelector('.ihtml-trace-actions > .ihtml-tool')),chipStyles:styles(article.querySelector('.ihtml-trace-actions .t-status')),answerStyles:styles(article.querySelector('.answer')),historyLabels:article.querySelectorAll('.t-history-label').length,overflow:article.scrollWidth>article.clientWidth};
 });
 const live=await read('#a'),history=await read('#b');assert.deepEqual(history,live);assert.equal(live.processOpen,false);assert.deepEqual(live.processNames,['tool_search','future_internal','arbitrary_read']);assert.deepEqual(live.actionNames,['Apply original scope']);assert.equal(live.historyLabels,0);assert.equal(live.overflow,false);
 assert.equal(await page.locator('#a .ihtml-trace-actions button:visible').count(),2);assert.equal(await page.locator('#b .ihtml-trace-actions button:visible').count(),0);
 for(const selector of ['#a','#b']) {await page.locator(selector+' .ihtml-process > summary').click();assert.equal(await page.locator(selector+' .pbody > .ihtml-tool').first().isVisible(),true);await page.locator(selector+' .pbody > .ihtml-tool > summary').first().click();assert.match(await page.locator(selector+' .pbody > .ihtml-tool').first().innerText(),/checked/)}
 await page.locator('#b').screenshot({path:`/tmp/ihtml-process-history-${mode}-${width}.png`});
});

// 只保存正文的回合也能由后续分页补齐工具，不能另起历史文章或丢掉已移除的过程容器。
test('late history pages enrich the original answer turn without replacing its layout',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{window.a=ihtml.mountChat('#a',{initialMessages:[{id:'u',role:'user',content:'Question'},{id:'a',role:'assistant',content:'Original answer'}],operationHistory:{cursor:'older',load:async()=>({items:[{id:'read',afterMessageId:'u',status:'success',call:{id:'read',name:'arbitrary_read'}},{id:'change',afterMessageId:'u',status:'pending',proposalRecorded:true,call:{id:'change',name:'arbitrary_change',title:'Original scope',body:{target:'one'}}}],cursor:''})}});await a.ready();window.originalAnswer=a.root.querySelector('.answer').parentElement;});
 await page.locator('.t-history-more').click();await page.waitForFunction(()=>!document.querySelector('.t-history-more'));
 assert.equal(await page.evaluate(()=>originalAnswer===a.root.querySelector('.answer').parentElement),true);assert.equal(await page.locator('.ihtml-msg.assistant').filter({hasText:'Original answer'}).count(),1);
 assert.equal(await page.locator('.pbody > [data-surface="process"]').count(),1);assert.equal(await page.locator('.ihtml-trace-actions > [data-surface="operation"]').count(),1);assert.equal(await page.locator('.ihtml-trace-actions > .ihtml-tool').isVisible(),true);assert.equal(await page.locator('.t-history-label').count(),0);
});

// 观测阶段不得同步改变被观察布局；合帧处理并随销毁回收，避免窄侧栏/手机重排反馈循环。
test('chat geometry changes are deferred and coalesced outside resize delivery',async(t)=>{
 const page=await fixture(t);
 const result=await page.evaluate(async()=>{
  window.resizeWatchers=[];window.ResizeObserver=class {constructor(callback){this.callback=callback;this.targets=new Set();resizeWatchers.push(this)}observe(node){this.targets.add(node)}unobserve(node){this.targets.delete(node)}disconnect(){this.targets.clear()}};
  window.a=ihtml.mountChat('#a');await a.ready();const root=a.root,panel=root.querySelector('.ihtml-chat');Object.defineProperty(panel,'clientHeight',{configurable:true,value:250});root.classList.remove('ihtml-chat-compact');
  const observer=resizeWatchers.find(o=>o.targets.has(panel));observer.callback([]);observer.callback([]);const immediate=root.classList.contains('ihtml-chat-compact');
  await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));const deferred=root.classList.contains('ihtml-chat-compact');
  root.classList.remove('ihtml-chat-compact');observer.callback([]);a.destroy();await new Promise(resolve=>requestAnimationFrame(resolve));return {immediate,deferred,afterDestroy:root.classList.contains('ihtml-chat-compact'),disconnected:observer.targets.size===0};
 });
 assert.deepEqual(result,{immediate:false,deferred:true,afterDestroy:false,disconnected:true});
});

// 同一目标的说明是版本快照；自动续办有新状态时不能继续把上一版说明并列成当前事实。
test('live and restored goal continuations share one flow and archive superseded answers',async(t)=>{
 const page=await fixture(t);
 await page.evaluate(async()=>{backgroundExecution=true;
  window.a=ihtml.mountChat('#a',{locale:'en'});await a.ready();await a.chat.send('Original goal');
  const call={id:'op',name:'arbitrary_change',title:'Original scope',confirmation_nonce:'n',confirmation_token:'SIGNED',body:{target:'one'}};
  const emit=(type,run_id,seq,rest={})=>sockets[0].emit({type,run_id,seq,flow_id:'goal-f',...rest});
  emit('turn_start','r1',1);emit('confirmation_required','r1',2,{tool_call:call});emit('assistant_done','r1',3,{message:'OLD_PENDING'});
  emit('turn_start','r2',1);emit('tool_result','r2',2,{tool_call:{id:'op',confirmation_nonce:'n'},tool_status:'accepted',tool_result:{id:'original-job'}});emit('assistant_done','r2',3,{message:'OLD_ZERO_COMPLETED'});
  window.b=ihtml.mountChat('#b',{locale:'en',initialMessages:[{id:'u',role:'user',content:'Original goal'},{id:'old',flowId:'goal-f',runId:'r1',createdAt:'2026-11-06T08:00:00Z',role:'assistant',content:'OLD_PENDING'},{id:'latest',flowId:'goal-f',runId:'r2',createdAt:'2026-11-06T08:01:00Z',role:'assistant',content:'OLD_ZERO_COMPLETED'}],initialOperations:[{id:'op',afterMessageId:'u',flowId:'goal-f',runId:'r2',sequence:2,status:'accepted',proposalRecorded:true,call,result:{id:'original-job'}}]});await b.ready();while(sockets.length<2)await new Promise(resolve=>setTimeout(resolve,0));
  // 剥离测试新问题，只给历史连接注入可信背景帧，不调用任何实际服务。
  const original=b.root.querySelector('.ihtml-operation-history');
  for(const [socket,run] of [[sockets[0],'r3'],[sockets[1],'r3']]) {socket.emit({type:'turn_start',run_id:run,seq:1,restored_approval:true,task_id:'wake-fixture',background_replay:socket===sockets[1]});socket.emit({type:'turn_context',run_id:run,seq:2,flow_id:'goal-f'});socket.emit({type:'assistant_status',run_id:run,seq:3,flow_id:'goal-f',status:'CURRENT_COMPLETED_1'})}
  window.restoredOriginal=original;
 });
 for(const selector of ['#a','#b']) {
  assert.equal(await page.locator(selector+' .ihtml-msg.assistant').count(),2);const flow=page.locator(selector+' .ihtml-msg.assistant').filter({has:page.locator('.ihtml-tool')});assert.equal(await flow.count(),1);assert.match(await flow.innerText(),/CURRENT_COMPLETED_1/);assert.doesNotMatch(await flow.innerText(),/OLD_PENDING|OLD_ZERO_COMPLETED/);assert.equal(await flow.locator('.ihtml-tool').getAttribute('data-status'),'accepted');assert.equal(await flow.locator('.ihtml-answer-history').count(),1);assert.equal(await flow.locator('.ihtml-answer-history').evaluate(e=>e.open),false);await flow.locator('.ihtml-answer-history > summary').click();assert.match(await flow.innerText(),/OLD_PENDING/);assert.match(await flow.innerText(),/OLD_ZERO_COMPLETED/);
 }
 assert.deepEqual(await page.locator('#a .t-prior-answer').allTextContents(),await page.locator('#b .t-prior-answer').allTextContents());
 assert.equal(await page.evaluate(()=>restoredOriginal.isConnected),true);
});

test('legacy active replay rejoins its unique original question without duplicating the AI flow',async(t)=>{
 const page=await fixture(t);await page.evaluate(async()=>{backgroundExecution=true;window.a=ihtml.mountChat('#a',{initialMessages:[{id:'u',role:'user',content:'Legacy original'},{id:'a',role:'assistant',content:'OLD_SNAPSHOT'}],initialOperations:[{id:'read',afterMessageId:'u',status:'success',call:{id:'read',name:'read'}}]});await a.ready();while(!sockets.length)await new Promise(resolve=>setTimeout(resolve,0));const s=sockets[0];s.emit({type:'background_user_message',message:'Legacy original',background_replay:true});s.emit({type:'turn_start',run_id:'active',seq:1,background_replay:true});s.emit({type:'assistant_status',run_id:'active',seq:2,status:'CURRENT_PROGRESS'});});
 const flow=page.locator('.ihtml-operation-history');assert.equal(await flow.count(),1);assert.match(await flow.innerText(),/CURRENT_PROGRESS/);assert.doesNotMatch(await flow.innerText(),/OLD_SNAPSHOT/);assert.equal(await flow.locator('.ihtml-answer-history').count(),1);
});

test('new progress supersedes an in-flight explanatory draft until its authoritative final snapshot',async(t)=>{
 const page=await fixture(t);await page.evaluate(async()=>{window.a=ihtml.mountChat('#a');await a.ready();await a.chat.send('Track arbitrary work');const s=sockets[0];s.emit({type:'turn_start',run_id:'r',seq:1});s.emit({type:'assistant_delta',run_id:'r',seq:2,delta:'OLD_ZERO_SNAPSHOT'});a.root.querySelector('.ihtml-process').closest('.ihtml-msg').dataset.fixtureFlow='yes';});
 await page.waitForFunction(()=>a.root.querySelector('.ihtml-process')?.closest('.ihtml-msg').querySelector('.answer')?.textContent.includes('OLD_ZERO'));
 await page.evaluate(()=>sockets[0].emit({type:'assistant_status',run_id:'r',seq:3,status:'CURRENT_PROGRESS_1'}));
 assert.equal(await page.locator('.ihtml-msg.assistant[data-fixture-flow] > .answer').isVisible(),false);assert.equal(await page.locator('.ihtml-answer-history').count(),0);assert.match(await page.locator('.p-title').innerText(),/CURRENT_PROGRESS_1/);
 await page.evaluate(()=>sockets[0].emit({type:'assistant_delta',run_id:'r',seq:4,delta:' still a draft'}));assert.equal(await page.locator('.ihtml-msg.assistant[data-fixture-flow] > .answer').isVisible(),false);
 await page.evaluate(()=>sockets[0].emit({type:'assistant_done',run_id:'r',seq:5,message:'FINAL_CONFIRMED_2'}));assert.equal(await page.locator('.ihtml-msg.assistant[data-fixture-flow] > .answer').isVisible(),true);assert.equal(await page.locator('.ihtml-msg.assistant[data-fixture-flow] > .answer').innerText(),'FINAL_CONFIRMED_2');
});

// 后台旧目标恢复不能覆盖后来提出的新目标；关联依赖可信 ID，不按说明内容或当前焦点猜测。
test('explicit flow identities isolate interleaved goals during background recovery',async(t)=>{
 const page=await fixture(t);await page.evaluate(async()=>{backgroundExecution=true;window.a=ihtml.mountChat('#a',{initialMessages:[{id:'u1',role:'user',content:'First goal'},{id:'a1',role:'assistant',flowId:'f1',runId:'old1',content:'First snapshot'},{id:'u2',role:'user',content:'Second goal'},{id:'a2',role:'assistant',flowId:'f2',runId:'old2',content:'Second snapshot'}]});await a.ready();while(!sockets.length)await new Promise(r=>setTimeout(r,0));sockets[0].emit({type:'turn_start',flow_id:'f1',run_id:'next1',seq:1,background_replay:true});sockets[0].emit({type:'assistant_status',flow_id:'f1',run_id:'next1',seq:2,status:'FIRST_GOAL_PROGRESS'});});
 const first=page.locator('.ihtml-msg.assistant').filter({hasText:'FIRST_GOAL_PROGRESS'});assert.equal(await first.count(),1);assert.equal(await page.locator('.ihtml-msg.assistant > .answer').filter({hasText:'Second snapshot'}).isVisible(),true);assert.doesNotMatch(await first.innerText(),/Second snapshot/);assert.equal(await first.locator('.ihtml-answer-history').count(),1);
});

// 已用结构化卡替代的文本回执仍是合法锚点，不能让其后的状态和说明另起一个历史 AI 块。
test('receipt message anchors keep subsequent operations and latest explanation in the original flow',async(t)=>{
 const page=await fixture(t);await page.evaluate(async()=>{window.a=ihtml.mountChat('#a',{initialMessages:[{id:'u',role:'user',content:'Original goal'},{id:'before',role:'assistant',content:'OLD_PENDING'},{id:'receipt',role:'tool',operationId:'change',content:'accepted'},{id:'after',role:'assistant',content:'LATEST_EXPLANATION'}],initialOperations:[{id:'change',afterMessageId:'u',status:'accepted',proposalRecorded:true,call:{id:'change',name:'arbitrary_change',title:'Original scope'},result:{accepted:true}},{id:'read',afterMessageId:'receipt',status:'success',call:{id:'read',name:'arbitrary_read'},result:{completed:1}}]});await a.ready()});
 assert.equal(await page.locator('.ihtml-operation-history').count(),1);const flow=page.locator('.ihtml-operation-history');assert.equal(await flow.locator('.ihtml-tool').count(),2);assert.equal(await flow.locator('.ihtml-trace-actions > .ihtml-tool').count(),1);assert.equal(await flow.locator('.pbody > .ihtml-tool').count(),1);assert.match(await flow.innerText(),/LATEST_EXPLANATION/);assert.doesNotMatch(await flow.innerText(),/OLD_PENDING/);assert.equal(await flow.locator('.ihtml-answer-history').count(),1);
});

// 两个相同问题仍是两个请求；旧协议的活动恢复使用公开运行 ID 关联，而不是按文本合并。
test('legacy repeated questions use recorded run identity to rejoin the correct active flow',async(t)=>{
 const page=await fixture(t);await page.evaluate(async()=>{backgroundExecution=true;window.a=ihtml.mountChat('#a',{initialMessages:[{id:'u1',role:'user',content:'Same question'},{id:'a1',role:'assistant',content:'First snapshot'},{id:'u2',role:'user',content:'Same question'},{id:'a2',role:'assistant',content:'Second snapshot'}],initialOperations:[{id:'one',afterMessageId:'u1',runId:'r1',sequence:2,status:'success',call:{id:'one',name:'read'}},{id:'two',afterMessageId:'u2',runId:'r2',sequence:2,status:'success',call:{id:'two',name:'read'}}]});await a.ready();while(!sockets.length)await new Promise(r=>setTimeout(r,0));const s=sockets[0];s.emit({type:'background_user_message',background_replay:true,message:'Same question'});s.emit({type:'turn_start',run_id:'r2',seq:1,background_replay:true});s.emit({type:'assistant_status',run_id:'r2',seq:3,status:'SECOND_PROGRESS'});});
 const flows=page.locator('.ihtml-operation-history');assert.equal(await flows.count(),2);assert.match(await flows.nth(0).innerText(),/First snapshot/);assert.doesNotMatch(await flows.nth(0).innerText(),/SECOND_PROGRESS/);assert.match(await flows.nth(1).innerText(),/SECOND_PROGRESS/);assert.doesNotMatch(await flows.nth(1).innerText(),/Second snapshot/);
});

test('completed historical runs suppress old answers while preserving late authoritative approvals',async(t)=>{
 const page=await fixture(t);await page.evaluate(async()=>{backgroundExecution=true;window.a=ihtml.mountChat('#a',{initialMessages:[{id:'u',role:'user',content:'Original goal'},{id:'a',role:'assistant',flowId:'f',runId:'r',content:'Saved final snapshot'}],initialOperations:[{id:'start',afterMessageId:'u',flowId:'f',runId:'r',sequence:3,status:'success',call:{id:'start',name:'task_start'}}]});await a.ready();while(!sockets.length)await new Promise(r=>setTimeout(r,0));const s=sockets[0];s.emit({type:'turn_start',flow_id:'f',run_id:'r',seq:1,background_replay:true});s.emit({type:'assistant_done',flow_id:'f',run_id:'r',seq:4,background_replay:true,message:'STALE_REPLAY'});s.emit({type:'confirmation_required',flow_id:'f',run_id:'r',seq:10,background_replay:true,tool_call:{id:'late',name:'arbitrary_change',title:'New authoritative scope',confirmation_nonce:'fresh',confirmation_token:'SIGNED',body:{target:'one'}}});});
 assert.equal(await page.locator('.ihtml-operation-history').count(),1);assert.doesNotMatch(await page.locator('.ihtml-operation-history').innerText(),/STALE_REPLAY/);assert.equal(await page.locator('.ihtml-tool[data-status="pending"]').isVisible(),true);assert.equal(await page.locator('.ihtml-tool[data-status="pending"] .t-actions button.ok').isVisible(),true);
});

// 回放的完成边界覆盖旧工具轮次，但不吞掉快照之后的活动/已完成新轮次。
test('replay completion boundaries preserve saved final answers across intermediate tool runs',async(t)=>{
 const page=await fixture(t);await page.evaluate(async()=>{
  backgroundExecution=true;window.a=ihtml.mountChat('#a',{initialMessages:[{id:'u',role:'user',content:'Original goal'},{id:'final',role:'assistant',flowId:'f',runId:'final',content:'SAVED_FINAL'}],initialOperations:[{id:'write',afterMessageId:'u',flowId:'f',runId:'receipt',sequence:2,status:'accepted',proposalRecorded:true,call:{id:'write',name:'arbitrary_write'},result:{id:'job'}}]});await a.ready();while(!sockets.length)await new Promise(r=>setTimeout(r,0));
  window.emit=(type,run_id,seq,background_seq,rest={})=>sockets[0].emit({type,run_id,seq,background_seq,flow_id:'f',background_replay:true,...rest});
  emit('turn_start','receipt',1,2,{replay_completions:[{run_id:'receipt',flow_id:'f',sequence:4},{run_id:'final',flow_id:'f',sequence:7},{run_id:'newer',flow_id:'f',sequence:10}]});
  emit('tool_result','receipt',2,3,{tool_call:{id:'write'},tool_status:'accepted'});emit('assistant_done','receipt',3,4,{continuation_pending:true,message:'OLD_RECEIPT'});emit('turn_start','final',1,5);emit('assistant_done','final',2,7,{message:'SAVED_FINAL'});
 });
 const flow=page.locator('.ihtml-operation-history');assert.equal(await flow.locator(':scope > .answer').isVisible(),true);assert.equal(await flow.locator(':scope > .answer').innerText(),'SAVED_FINAL');assert.equal(await flow.locator('.ihtml-answer-history').count(),0);assert.equal(await flow.locator('.ihtml-process').evaluate(e=>e.open),false);
 await page.evaluate(()=>{emit('turn_start','newer',1,8);emit('assistant_done','newer',2,10,{message:'NEW_FINAL_NOT_IN_HISTORY'})});
 assert.equal(await flow.locator(':scope > .answer').innerText(),'NEW_FINAL_NOT_IN_HISTORY');assert.equal(await flow.locator('.t-prior-answer').textContent(),'SAVED_FINAL');
});

// 历史提案与可信重放为同一版本：不重复展示状态，但必须恢复原批准入口。
test('equal revision signed proposal restores historical approval without regressing settled operations',async(t)=>{
 const page=await fixture(t);await page.evaluate(async()=>{
  backgroundExecution=true;
  const call={id:'pending',name:'arbitrary_write',title:'Original scope',body:{target:'one'},confirmation_nonce:'n',confirmation_token:'SIGNED',confirmation_expires:Math.floor(Date.now()/1000)+600};window.call=call;
  window.a=ihtml.mountChat('#a',{initialMessages:[{id:'u',role:'user',content:'Original request'},{id:'a',role:'assistant',flowId:'f',runId:'r',content:'Review'}],initialOperations:['pending','success','running'].map((status,i)=>({id:i?'other-'+i:'pending',afterMessageId:'u',flowId:'f',runId:'r',sequence:3+i,status,proposalRecorded:true,call:{...call,id:i?'other-'+i:'pending'}}))});await a.ready();while(!sockets.length)await new Promise(r=>setTimeout(r,0));
 });
 assert.equal(await page.locator('.t-actions button.ok:visible').count(),0,'public history alone grants no authority');
 await page.evaluate(()=>{for(let i=0;i<3;i++)sockets[0].emit({type:'confirmation_required',run_id:'r',flow_id:'f',seq:3+i,background_replay:true,tool_call:{...call,id:i?'other-'+i:'pending'}})});
 assert.equal(await page.locator('.ihtml-tool[data-status="pending"] .t-actions button.ok').isVisible(),true);
 assert.equal(await page.locator('.ihtml-tool[data-status="success"] .t-actions button.ok:visible').count(),0);
 assert.equal(await page.locator('.ihtml-tool[data-status="running"] .t-actions button.ok:visible').count(),0);
 await page.locator('.ihtml-tool[data-status="pending"] .t-actions button.ok').click();
 assert.equal(await page.evaluate(()=>sockets[0].sent.filter(e=>e.type==='confirm_tool'&&e.tool_call.id==='pending'&&e.tool_call.confirmation_token==='SIGNED').length),1);
});

// 对照真实确认、自动续办和重新挂载的显示状态；历史不能凭正文相同隐藏新的权威答复。
for(const mode of ['light','dark'])for(const width of [390,1100])test(`answer versions keep live and restored presentation identical ${mode} ${width}`,async(t)=>{
 const page=await fixture(t,{width,height:900});
 await page.evaluate(async({mode,width})=>{
  backgroundExecution=true;
  window.call={id:'change',name:'arbitrary_write',title:'Apply selected scope',method:'POST',path:'/objects',body:{target:'one'},confirmation_nonce:'n',confirmation_token:'SIGNED'};
  window.savedMessages=[{id:'u',role:'user',content:'Original goal'},{id:'a1',role:'assistant',contentFormat:'markdown',flowId:'f',runId:'r1',content:'Review the selected scope.'}];
  window.savedOperation={id:'change',afterMessageId:'u',flowId:'f',runId:'r1',sequence:3,status:'pending',proposalRecorded:true,call};
  window.restore=async()=>{window.b?.destroy?.();const count=sockets.length;window.b=ihtml.mountChat('#b',{locale:'en',initialMessages:savedMessages,initialOperations:[savedOperation]});await b.ready();b.theme.set({mode},{persist:false});b.root.style.width=width+'px';while(sockets.length<=count||sockets.at(-1).readyState!==1)await new Promise(r=>setTimeout(r,0));window.restoredSocket=sockets.at(-1)};
  window.a=ihtml.mountChat('#a',{locale:'en'});await a.ready();a.theme.set({mode},{persist:false});a.root.style.width=width+'px';await a.chat.send('Original goal');window.liveSocket=sockets[0];
  liveSocket.emit({type:'turn_start',run_id:'r1',seq:1});liveSocket.emit({type:'turn_context',run_id:'r1',seq:2,flow_id:'f'});liveSocket.emit({type:'confirmation_required',run_id:'r1',seq:3,flow_id:'f',tool_call:call});liveSocket.emit({type:'assistant_done',run_id:'r1',seq:4,flow_id:'f',message:savedMessages[1].content});await restore();
 },{mode,width});
 assert.equal(await page.locator('.ihtml-answer-history').count(),0,'one explanation has no version archive in either view');
 // 点击实际确认按钮走前端状态转换；所有网络均为 fixture，无业务副作用。
 await page.locator('#a .t-actions button.ok').click();
 await page.evaluate(async()=>{
  liveSocket.emit({type:'turn_start',run_id:'r2',seq:1});liveSocket.emit({type:'turn_context',run_id:'r2',seq:2,flow_id:'f'});liveSocket.emit({type:'tool_result',run_id:'r2',seq:3,flow_id:'f',tool_call:{id:'change'},tool_status:'accepted',tool_result:{id:'job'}});liveSocket.emit({type:'assistant_done',run_id:'r2',seq:4,flow_id:'f',message:'The original work is still running.'});
  savedMessages.push({id:'a2',role:'assistant',contentFormat:'markdown',flowId:'f',runId:'r2',content:'The original work is still running.'});savedOperation={...savedOperation,runId:'r2',status:'accepted',result:{id:'job'}};await restore();
 });
 const snapshot=selector=>page.locator(selector+' .ihtml-msg.assistant').filter({has:page.locator('.ihtml-tool')}).evaluate(card=>{
  const styles=e=>Object.fromEntries(['color','backgroundColor','borderColor','borderRadius','fontSize','fontWeight','lineHeight','padding','margin','display'].map(k=>[k,getComputedStyle(e)[k]]));
  const answer=card.querySelector(':scope > .answer'),archive=card.querySelector('.ihtml-answer-history');
  const selectors=[':scope > .author','.ihtml-process','.ihtml-trace-actions > .ihtml-tool','.ihtml-answer-history','.ihtml-answer-history > summary',':scope > .answer'];
  return {authorVisible:!card.querySelector(':scope > .author').hidden,answerVisible:!answer.hidden&&getComputedStyle(answer).display!=='none',answer:answer.innerHTML,archiveOpen:archive.open,archive:archive.innerHTML,processOpen:card.querySelector('.ihtml-process').open,status:card.querySelector('.p-title').textContent,styles:selectors.map(s=>styles(card.querySelector(s))),order:[...card.children].map(e=>e.className),overflow:card.scrollWidth>card.clientWidth};
 });
 const compare=async()=>{await page.evaluate(()=>Promise.all(document.getAnimations().map(a=>a.finished.catch(()=>{}))));const live=await snapshot('#a'),restored=await snapshot('#b');assert.deepEqual(restored,live);assert.equal(live.answerVisible,true);assert.equal(live.authorVisible,true);assert.equal(live.archiveOpen,false);assert.equal(live.overflow,false)};
 await compare();
 assert.equal(await page.locator('#a .t-prior-answer').count(),1);
 await page.evaluate(()=>{
  // 新一轮的结论可以与上一轮相同；身份由 flow/run 决定，文字不是去重键。
  for(const socket of [liveSocket,restoredSocket]){const base={run_id:'r3',flow_id:'f',background_replay:socket===restoredSocket};socket.emit({...base,type:'turn_start',seq:1});socket.emit({...base,type:'assistant_status',seq:2,status:'Checking the original work'});socket.emit({...base,type:'assistant_done',seq:3,message:'The original work is still running.'})}
 });
 await compare();
 assert.equal(await page.locator('#a .t-prior-answer').count(),2);
 await page.evaluate(async()=>{savedMessages.push({id:'a3',role:'assistant',contentFormat:'markdown',flowId:'f',runId:'r3',content:'The original work is still running.'});await restore()});
 await compare();
 for(const selector of ['#a','#b'])await page.locator(selector+' .ihtml-answer-history > summary').click();
 assert.deepEqual(await snapshot('#b'),await snapshot('#a'));
 assert.equal(await page.locator('#b .t-prior-answer').first().isVisible(),true);
 await page.locator('#b').screenshot({path:`/tmp/ihtml-answer-versions-${mode}-${width}.png`});
});
