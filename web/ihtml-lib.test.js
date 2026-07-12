'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

global.window = {};
require('./ihtml-lib.js');

const { fmt, util, makeT, template, chatAgent, chatTrace, itemLayout } = global.window.__ihtmlLib;

test('fmt.num clamps hostile precision values', () => {
  assert.equal(fmt.num(12.6, -8), '13');
  assert.equal(fmt.num(1, 99), '1.00000000000000000000');
  assert.equal(fmt.num(Infinity), '-');
  assert.doesNotThrow(() => fmt.num(1.25, Number.NaN));
});

test('throttle clears an overdue trailing callback before an immediate call', async () => {
  let calls = 0;
  const throttled = util.throttle(() => { calls++; }, 10);
  throttled();
  throttled(); // queues a trailing call
  const until = Date.now() + 20;
  while (Date.now() < until) { /* deliberately block the queued timer */ }
  throttled(); // must replace, not duplicate, the overdue trailing call
  await new Promise((resolve) => setTimeout(resolve, 25));
  assert.equal(calls, 2);
});

test('new accessibility and read-only strings exist in both locales', () => {
  assert.notEqual(makeT('zh')('readonly_empty'), 'readonly_empty');
  assert.notEqual(makeT('en')('previous_page'), 'previous_page');
  assert.equal(makeT('zh')('item_editor_content_min_span', { value: 3 }), '内容至少需要 3 列');
  assert.equal(makeT('en')('layout_resize_width_short'), 'Width');
});

test('chat agent modes default safely and request snapshots do not drift', () => {
  assert.deepEqual(chatAgent.normalizeModes(undefined), ['fast']);
  assert.deepEqual(chatAgent.normalizeModes([' DEEP ', 'unknown', 'deep']), ['fast', 'deep']);
  assert.equal(chatAgent.selectMode(['fast'], 'deep'), 'fast');
  assert.equal(chatAgent.selectMode(['fast', 'deep'], 'deep'), 'deep');
  const snapshot = chatAgent.requestSnapshot(['fast', 'deep'], 'deep', 'model-a', true);
  assert.deepEqual(snapshot, { agentMode: 'deep', model: 'model-a' });
  assert.deepEqual(chatAgent.requestSnapshot(['fast'], 'deep', 'hidden-model', false), { agentMode: 'fast', model: '' });
});

test('deep event projection replaces plans and updates agents and budget in place', () => {
  let state = chatAgent.initialState('deep');
  state = chatAgent.project(state, {
    type: 'assistant_plan',
    plan: [{ id: 'inspect', content: 'Inspect', status: 'in_progress' }, { id: 'fix', content: 'Fix', status: 'pending' }],
  });
  state = chatAgent.project(state, {
    type: 'assistant_agent',
    agent: { mode: 'deep', agent: 'ui_inspector', task_id: 'task-1', parent_task_id: 'root', phase: 'inspect', status: 'running', summary: 'Reading' },
    budget: { model_calls: 2, max_model_calls: 24, subtasks: 1, max_subtasks: 3 },
  });
  state = chatAgent.project(state, {
    type: 'assistant_agent',
    agent: { mode: 'deep', agent: 'ui_inspector', task_id: 'task-1', parent_task_id: 'root', phase: 'inspect', status: 'completed', summary: 'Checked' },
  });
  assert.equal(Object.keys(state.agents).length, 1, 'the same task updates instead of appending a log row');
  assert.equal(state.agents['task-1'].status, 'completed');
  assert.deepEqual(chatAgent.budgetParts(state.budget), { modelCalls: 2, maxModelCalls: 24, subtasks: 1, maxSubtasks: 3 });
  state = chatAgent.project(state, { type: 'assistant_plan', plan: [{ id: 'fix', content: 'Fix', status: 'completed' }] });
  assert.deepEqual(state.plan.map((item) => item.id), ['fix'], 'plan events are complete snapshots');
  state = chatAgent.project(state, {
    type: 'assistant_agent',
    agent: { mode: 'deep', agent: 'root', phase: 'approval', status: 'completed', approval_resume: 'manual_tool_only' },
  });
  assert.equal(state.manualPaused, true, 'executing one confirmation card does not mark the whole plan resumed');
});

test('chat trace presents item writes as localized human actions and facts', () => {
  const call = {
    name: 'ui_item_put', title: '写入/更新 Item', method: 'TOOL', path: 'ui://ui_item_put',
    body: {
      items: [
        { id: 'weather-now', type: 'html', page: 'dashboard', content: '<section>weather</section>' },
        { id: 'weather-week', type: 'html', page: 'dashboard', content: '<section>forecast</section>' },
      ],
      note: 'Add a weather view',
    },
  };
  const zh = chatTrace.toolView(call, { type: 'tool_start', tool_status: 'running' }, makeT('zh'));
  const en = chatTrace.toolView(call, { type: 'tool_start', tool_status: 'running' }, makeT('en'));
  assert.equal(zh.title, '创建或更新模块');
  assert.equal(zh.action, '创建或更新 2 个界面模块');
  assert.ok(zh.facts.some((fact) => fact.label === '模块 ID' && fact.value.includes('weather-now')));
  assert.equal(en.title, 'Create or update modules');
  assert.equal(en.action, 'Create or update 2 interface modules');
  assert.ok(en.facts.some((fact) => fact.label === 'Pages' && fact.value === 'dashboard'));
});

test('chat trace summarizes long code and structure instead of exposing raw payloads', () => {
  const longCode = '<script>' + 'window.renderWeather();'.repeat(80) + '</script>';
  const view = chatTrace.toolView({
    name: 'ui_item_put', method: 'TOOL', path: 'ui://ui_item_put',
    body: { items: [{ id: 'weather', type: 'html', content: longCode }], note: 'Weather card' },
  }, { type: 'confirmation_required', tool_status: 'pending' }, makeT('en'));
  assert.match(view.request, /\d+ characters, collapsed/);
  assert.doesNotMatch(view.request, /window\.renderWeather/);
  assert.ok(view.request.length <= 5000);

  const templateView = chatTrace.toolView({
    name: 'ui_template_put_current', method: 'TOOL', path: 'ui://ui_template_put_current',
    body: { expected_revision: 4, template: { nodes: Array.from({ length: 40 }, (_, i) => ({ id: 'node-' + i, kind: 'slot' })) } },
  }, {}, makeT('zh'));
  assert.match(templateView.request, /结构数据（约 \d+ 个字符），已折叠/);
  assert.doesNotMatch(templateView.request, /node-39/);
});

test('chat trace removes credentials from request, result, debug, facts, and summaries', () => {
  const secret = 'do-not-render-this-secret';
  const view = chatTrace.toolView({
    name: 'custom_remote_tool', method: 'POST', path: '/run?access_token=' + secret,
    confirmation_token: secret,
    query: { q: 'safe', authorization: 'Bearer ' + secret },
    body: {
      label: 'safe', confirmation_token: secret,
      authToken: secret, sessionId: secret, privateKey: secret,
      headers: { Authorization: 'Bearer ' + secret, Cookie: 'session=' + secret, clientSecret: secret },
    },
  }, {
    type: 'tool_result', tool_status: 'success', tool_summary: 'authToken=' + secret,
    tool_result: { ok: true, access_token: secret, refreshToken: secret, data: 'safe result' },
    tool_debug: { status: 'ok', secret, tool_args: { api_key: secret, sessionKey: secret, q: 'safe' } },
  }, makeT('en'));
  const rendered = JSON.stringify(view);
  assert.doesNotMatch(rendered, new RegExp(secret));
  assert.doesNotMatch(view.request, /Authorization|Cookie/);
  assert.match(view.summary, /\[redacted\]/);
  assert.match(view.result, /safe result/);
  assert.match(view.debug, /"status": "ok"/);
});

test('chat trace safely falls back for unknown tools and keeps result and diagnostics separate', () => {
  const view = chatTrace.toolView({
    name: 'inventory_lookup', method: 'TOOL', path: 'plugin://inventory_lookup', body: { sku: 'A-42', limit: 5 },
  }, {
    type: 'tool_result', tool_status: 'success', tool_summary: 'Found one item',
    tool_result: { item: { sku: 'A-42', stock: 7 } },
    tool_debug: { status: 'ok', duration_ms: 18 },
  }, makeT('en'));
  assert.equal(view.title, 'inventory_lookup');
  assert.equal(view.action, 'Run inventory_lookup');
  assert.ok(view.facts.some((fact) => fact.label === 'sku' && fact.value === 'A-42'));
  assert.match(view.result, /"stock": 7/);
  assert.doesNotMatch(view.result, /duration_ms/);
  assert.match(view.debug, /"duration_ms": 18/);
  assert.doesNotMatch(view.debug, /"stock"/);
  assert.equal(view.hasTechnical, true);
});

test('item layout keys hash non-root page names as UTF-8 FNV-1a', () => {
  assert.equal(itemLayout.key(''), 'item-layout.root');
  assert.equal(itemLayout.key('root'), 'item-layout.root');
  assert.equal(itemLayout.key('hello'), 'item-layout.4f9f2cab');
  assert.equal(itemLayout.key('仪表盘'), 'item-layout.dd4430cf');
  assert.equal(itemLayout.fnv1a('页面/一'), 'e78ab8b5');
});

test('item layout normalization clamps geometry and rejects CSS color injection', () => {
  const doc = itemLayout.normalize({
    version: 99,
    page: 'dashboard',
    mode: 'broken',
    items: {
      hero: {
        slot: 'main',
        flow: { span: 99, height: 2 },
        free: { x: -40, y: 20000, width: 2, height: Infinity, z: 5000 },
        appearance: {
          background: '#ABCDEF', color: 'red;position:fixed', radius: 999, opacity: -3,
        },
      },
      '<unsafe>': { slot: 'main' },
    },
    orderBySlot: { main: ['missing', 'hero', 'hero'] },
  });
  assert.equal(doc.version, 1);
  assert.equal(doc.mode, 'grid');
  assert.deepEqual(Object.keys(doc.items), ['hero']);
  assert.deepEqual(doc.orderBySlot.main, ['hero']);
  assert.deepEqual(doc.items.hero.flow, { span: 12, height: 64 });
  assert.deepEqual(doc.items.hero.free, { x: 0, y: 10000, width: 120, height: 180, z: 999 });
  assert.deepEqual(doc.items.hero.appearance, { background: '#abcdef', radius: 96, opacity: 0 });
  assert.equal(itemLayout.sanitizeColor('url(javascript:alert(1))'), '');
  assert.equal(itemLayout.sanitizeColor('rgb(255, 0, 12)'), 'rgb(255, 0, 12)');
  assert.equal(itemLayout.sanitizeColor('rgba(1, 2, 3, 2)'), '');
  assert.equal(itemLayout.sanitizeColor('var(--card-surface)'), 'var(--card-surface)');
});

test('item layout normalization reconciles slot order without duplicates or stale ids', () => {
  const doc = itemLayout.normalize({
    page: 'work', mode: 'grid',
    items: {
      a: { slot: 'left' }, b: { slot: 'left' }, c: { slot: 'right' }, d: { slot: 'left' },
    },
    orderBySlot: { left: ['b', 'c', 'b', 'missing'], right: ['c'] },
  });
  assert.deepEqual(doc.orderBySlot.left, ['b', 'a', 'd']);
  assert.deepEqual(doc.orderBySlot.right, ['c']);
});

test('item layout reducer edits existing items and reorders within or across slots', () => {
  let state = itemLayout.initial('work', {
    mode: 'grid',
    items: { a: { slot: 'left' }, b: { slot: 'left' }, c: { slot: 'right' } },
    orderBySlot: { left: ['a', 'b'], right: ['c'] },
  });
  state = itemLayout.reduce(state, { type: 'select', id: 'a' });
  state = itemLayout.reduce(state, { type: 'patch', section: 'appearance', patch: { background: '#112233', radius: 18 } });
  state = itemLayout.reduce(state, { type: 'patch', id: 'a', patch: { flow: { span: 6, height: 240 }, free: { x: 44, y: 80, width: 640 } } });
  state = itemLayout.reduce(state, { type: 'mode', mode: 'free' });
  assert.equal(state.doc.mode, 'free');
  assert.deepEqual(state.doc.items.a.appearance, { background: '#112233', radius: 18 });
  assert.deepEqual(state.doc.items.a.flow, { span: 6, height: 240 });
  assert.equal(state.doc.items.a.free.x, 44);
  assert.equal(state.doc.items.a.free.width, 640);
  state = itemLayout.reduce(state, { type: 'reorder', slot: 'left', ids: ['b', 'a'] });
  assert.deepEqual(state.doc.orderBySlot.left, ['b', 'a']);
  state = itemLayout.reduce(state, { type: 'reorder', id: 'a', slot: 'right', index: 0 });
  assert.equal(state.doc.items.a.slot, 'right');
  assert.deepEqual(state.doc.orderBySlot.left, ['b']);
  assert.deepEqual(state.doc.orderBySlot.right, ['a', 'c']);
  assert.equal(state.dirty, true);
});

test('grid same-row and cross-row reorders each commit orderBySlot as one history step', () => {
  const input = {
    mode: 'grid',
    items: {
      a: { slot: 'main', flow: { span: 6 } },
      b: { slot: 'main', flow: { span: 6 } },
      c: { slot: 'main', flow: { span: 6 } },
      d: { slot: 'main', flow: { span: 6 } },
    },
    orderBySlot: { main: ['a', 'b', 'c', 'd'] },
  };

  let sameRow = itemLayout.initial('work', input);
  sameRow = itemLayout.reduce(sameRow, { type: 'reorder', slot: 'main', ids: ['b', 'a', 'c', 'd'] });
  assert.deepEqual(sameRow.doc.orderBySlot.main, ['b', 'a', 'c', 'd']);
  assert.equal(sameRow.past.length, 1, 'a same-row drag produces one undo checkpoint');
  sameRow = itemLayout.reduce(sameRow, { type: 'undo' });
  assert.deepEqual(sameRow.doc.orderBySlot.main, ['a', 'b', 'c', 'd']);
  sameRow = itemLayout.reduce(sameRow, { type: 'redo' });
  assert.deepEqual(sameRow.doc.orderBySlot.main, ['b', 'a', 'c', 'd']);

  let crossRow = itemLayout.initial('work', input);
  crossRow = itemLayout.reduce(crossRow, { type: 'reorder', slot: 'main', ids: ['b', 'c', 'a', 'd'] });
  assert.deepEqual(crossRow.doc.orderBySlot.main, ['b', 'c', 'a', 'd']);
  assert.equal(crossRow.past.length, 1, 'a cross-row drag also produces one undo checkpoint');
  assert.deepEqual(Object.fromEntries(Object.entries(crossRow.doc.items).map(([id, item]) => [id, item.flow.span])), {
    a: 6, b: 6, c: 6, d: 6,
  }, 'reordering never rewrites item sizing');
});

test('free move and resize patches undo and redo independently', () => {
  let state = itemLayout.initial('work', {
    mode: 'free',
    items: { card: { slot: 'main', free: { x: 0, y: 0, width: 320, height: 180, z: 0 } } },
    orderBySlot: { main: ['card'] },
  });
  state = itemLayout.reduce(state, { type: 'patch', id: 'card', section: 'free', patch: { x: 64, y: 40 } });
  state = itemLayout.reduce(state, { type: 'patch', id: 'card', section: 'free', patch: { width: 480, height: 260 } });
  assert.equal(state.past.length, 2);
  assert.deepEqual(state.doc.items.card.free, { x: 64, y: 40, width: 480, height: 260, z: 0 });

  state = itemLayout.reduce(state, { type: 'undo' });
  assert.deepEqual(state.doc.items.card.free, { x: 64, y: 40, width: 320, height: 180, z: 0 });
  state = itemLayout.reduce(state, { type: 'undo' });
  assert.deepEqual(state.doc.items.card.free, { x: 0, y: 0, width: 320, height: 180, z: 0 });
  state = itemLayout.reduce(state, { type: 'redo' });
  assert.deepEqual(state.doc.items.card.free, { x: 64, y: 40, width: 320, height: 180, z: 0 });
  state = itemLayout.reduce(state, { type: 'redo' });
  assert.deepEqual(state.doc.items.card.free, { x: 64, y: 40, width: 480, height: 260, z: 0 });
});

test('grid horizontal span patches preserve automatic flow height', () => {
  let state = itemLayout.initial('work', {
    mode: 'grid',
    items: { card: { slot: 'main', flow: { span: 12 }, free: { x: 24, y: 32, width: 480, height: 260, z: 2 } } },
    orderBySlot: { main: ['card'] },
  });
  assert.equal(Object.hasOwn(state.doc.items.card.flow, 'height'), false);
  state = itemLayout.reduce(state, { type: 'patch', id: 'card', section: 'flow', patch: { span: 5 } });
  assert.deepEqual(state.doc.items.card.flow, { span: 5 });
  assert.equal(state.doc.items.card.free.height, 260, 'a grid-only horizontal resize must not copy its rendered height into free geometry');
  assert.equal(itemLayout.styleFor(state.doc, 'card').height, '');
  assert.equal(itemLayout.styleFor(state.doc, 'card').overflow, '');
  assert.equal(state.past.length, 1, 'one completed horizontal resize creates one undo checkpoint');
  state = itemLayout.reduce(state, { type: 'undo' });
  assert.deepEqual(state.doc.items.card.flow, { span: 12 });
});

test('explicit grid height can return to automatic height and undo or redo cleanly', () => {
  let state = itemLayout.initial('work', {
    mode: 'grid', items: { card: { slot: 'main', flow: { span: 8, height: 280 } } }, orderBySlot: { main: ['card'] },
  });
  assert.equal(itemLayout.styleFor(state.doc, 'card').height, '280px');
  assert.equal(itemLayout.styleFor(state.doc, 'card').overflow, 'auto');

  state = itemLayout.reduce(state, { type: 'patch', id: 'card', section: 'flow', patch: { height: '' } });
  assert.deepEqual(state.doc.items.card.flow, { span: 8 }, 'the auto-height control removes the persisted override');
  assert.equal(itemLayout.styleFor(state.doc, 'card').height, '', 'auto height clears the old inline height');
  assert.equal(itemLayout.styleFor(state.doc, 'card').overflow, '', 'auto height also clears fixed-height overflow');

  state = itemLayout.reduce(state, { type: 'undo' });
  assert.deepEqual(state.doc.items.card.flow, { span: 8, height: 280 });
  assert.equal(itemLayout.styleFor(state.doc, 'card').height, '280px');
  state = itemLayout.reduce(state, { type: 'redo' });
  assert.deepEqual(state.doc.items.card.flow, { span: 8 });
  assert.equal(itemLayout.styleFor(state.doc, 'card').height, '');
});

test('grid and free resize geometry stay isolated across layout-mode switches', () => {
  let state = itemLayout.initial('work', {
    mode: 'grid',
    items: {
      card: {
        slot: 'main',
        flow: { span: 12 },
        free: { x: 24, y: 32, width: 480, height: 260, z: 2 },
      },
    },
    orderBySlot: { main: ['card'] },
  });

  state = itemLayout.reduce(state, { type: 'patch', id: 'card', section: 'flow', patch: { span: 5, height: 320 } });
  state = itemLayout.reduce(state, { type: 'mode', mode: 'free' });
  const freeStyle = itemLayout.styleFor(state.doc, 'card');
  assert.deepEqual(state.doc.items.card.free, { x: 24, y: 32, width: 480, height: 260, z: 2 });
  assert.equal(freeStyle.width, '480px');
  assert.equal(freeStyle.height, '260px');
  assert.equal(freeStyle.gridColumn, '', 'grid span never leaks into free-position styles');

  state = itemLayout.reduce(state, { type: 'patch', id: 'card', section: 'free', patch: { width: 720, height: 440 } });
  state = itemLayout.reduce(state, { type: 'mode', mode: 'grid' });
  const gridStyle = itemLayout.styleFor(state.doc, 'card');
  assert.deepEqual(state.doc.items.card.flow, { span: 5, height: 320 });
  assert.equal(gridStyle.gridColumn, 'span 5');
  assert.equal(gridStyle.height, '320px');
  assert.equal(gridStyle.width, '', 'free width never leaks back into grid styles');
  assert.equal(gridStyle.position, '');
});

test('item deletion is pending until confirmed and remains undoable', () => {
  let state = itemLayout.initial('', { items: { a: { slot: '' }, b: { slot: '' } }, orderBySlot: { '': ['a', 'b'] } });
  state = itemLayout.reduce(state, { type: 'select', id: 'a' });
  const pending = itemLayout.reduce(state, { type: 'remove' });
  assert.equal(pending.pendingRemoveId, 'a');
  assert.ok(pending.doc.items.a, 'requesting removal must not eagerly mutate the document');
  state = itemLayout.reduce(pending, { type: 'cancelRemove' });
  assert.equal(state.pendingRemoveId, '');
  state = itemLayout.reduce(state, { type: 'removePending', id: 'a' });
  state = itemLayout.reduce(state, { type: 'confirmRemove' });
  assert.equal(state.doc.items.a, undefined);
  assert.deepEqual(state.doc.orderBySlot[''], ['b']);
  assert.equal(state.selectedId, '');
  state = itemLayout.reduce(state, { type: 'undo' });
  assert.ok(state.doc.items.a);
  assert.equal(state.dirty, false, 'undoing the delete returns exactly to the persisted baseline');
  state = itemLayout.reduce(state, { type: 'redo' });
  assert.equal(state.doc.items.a, undefined);
});

test('markSaved establishes a new item layout baseline for dirty tracking', () => {
  let state = itemLayout.initial('', { items: { card: { slot: '' } } });
  state = itemLayout.reduce(state, { type: 'patch', id: 'card', section: 'flow', patch: { span: 5 } });
  assert.equal(state.dirty, true);
  state = itemLayout.reduce(state, { type: 'markSaved' });
  assert.equal(state.dirty, false);
  state = itemLayout.reduce(state, { type: 'patch', id: 'card', section: 'flow', patch: { span: 9 } });
  state = itemLayout.reduce(state, { type: 'undo' });
  assert.equal(state.doc.items.card.flow.span, 5);
  assert.equal(state.dirty, false);
  state = itemLayout.reduce(state, { type: 'undo' });
  assert.equal(state.doc.items.card.flow.span, 12);
  assert.equal(state.dirty, true, 'history before the save point is still undoable and differs from the new baseline');
});

test('item layout styles explicitly clear properties owned by the other mode', () => {
  const item = itemLayout.normalize({
    mode: 'free', items: { card: {
      flow: { span: 5, height: 250 }, free: { x: 30, y: 40, width: 500, height: 220, z: 7 },
      appearance: { background: '#123456', color: '#fff', radius: 14, opacity: 0.75 },
    } },
  }).items.card;
  const free = itemLayout.styleFor(item, 'free');
  assert.equal(free.position, 'absolute');
  assert.equal(free.left, '30px');
  assert.equal(free.gridColumn, '', 'free mode clears stale grid sizing');
  assert.equal(free.gridRow, '', 'free mode clears template row placement');
  assert.equal(free.order, '', 'free mode clears template ordering');
  assert.equal(free.borderRadius, '14px');
  const grid = itemLayout.styleFor(item, 'grid');
  assert.equal(grid.position, '', 'grid mode clears stale absolute positioning');
  assert.equal(grid.left, '');
  assert.equal(grid.width, '');
  assert.equal(grid.gridColumn, 'span 5');
  assert.equal(grid.gridRow, '', 'item grid sizing replaces template row placement');
  assert.equal(grid.order, '', 'DOM order replaces template order while item layout is active');
  assert.equal(grid.height, '250px');
  const cleared = itemLayout.styleFor(null, 'grid');
  assert.ok(Object.values(cleared).every((value) => value === ''), 'missing records return a complete inline-style reset');
});

test('item layout replaceDoc commits a batch as one undo step', () => {
  let state = itemLayout.initial('', { items: { a: { slot: '' }, b: { slot: '' } } });
  const next = itemLayout.normalize(state.doc);
  next.mode = 'free';
  next.items.a.free.x = 80;
  next.items.b.free.y = 96;
  state = itemLayout.reduce(state, { type: 'replaceDoc', doc: next, selectedId: 'a' });
  assert.equal(state.past.length, 1);
  assert.equal(state.doc.mode, 'free');
  state = itemLayout.reduce(state, { type: 'undo' });
  assert.equal(state.doc.mode, 'grid');
  assert.equal(state.doc.items.a.free.x, 0);
});

test('template state starts as a content-free Root and supports structural history', () => {
  let state = template.initial();
  assert.deepEqual(template.flatten(state.doc).map((entry) => entry.id), ['root']);
  state = template.reduce(state, { type: 'add', id: 'container-1', nodeType: 'container', parentId: 'root' });
  assert.equal(state.dirty, true);
  state = template.reduce(state, { type: 'add', id: 'slot-1', nodeType: 'slot', parentId: 'container-1' });
  assert.deepEqual(state.doc.children.root, ['container-1']);
  assert.deepEqual(state.doc.children['container-1'], ['slot-1']);
  assert.equal(state.selectedId, 'slot-1');
  assert.deepEqual(template.toVersion(state.doc).slots[0].accepts, ['html'], 'new structural slots accept visual HTML content by default');
  state = template.reduce(state, { type: 'remove', id: 'container-1' });
  assert.equal(state.doc.nodes['slot-1'], undefined, 'deleting a container removes its subtree');
  state = template.reduce(state, { type: 'undo' });
  assert.equal(state.doc.nodes['slot-1'].type, 'slot');
  state = template.reduce(state, { type: 'redo' });
  assert.equal(state.doc.nodes['container-1'], undefined);
  while (state.past.length) state = template.reduce(state, { type: 'undo' });
  assert.equal(state.dirty, false, 'undoing back to the saved baseline must not show a false unsaved warning');
});

test('template move rejects cycles and preserves deterministic sibling order', () => {
  let state = template.initial();
  state = template.reduce(state, { type: 'add', id: 'a', parentId: 'root' });
  state = template.reduce(state, { type: 'add', id: 'b', parentId: 'root' });
  state = template.reduce(state, { type: 'add', id: 'inside', parentId: 'a' });
  const same = template.reduce(state, { type: 'move', id: 'a', parentId: 'inside' });
  assert.equal(same, state, 'a parent cannot move beneath its descendant');
  state = template.reduce(state, { type: 'move', id: 'b', parentId: 'root', index: 0 });
  assert.deepEqual(state.doc.children.root, ['b', 'a']);
});

test('template breakpoint values inherit desktop then tablet overrides', () => {
  let state = template.initial();
  state = template.reduce(state, { type: 'patchLayout', id: 'root', breakpoint: 'desktop', patch: { mode: 'grid', columns: 12, gap: 16 } });
  state = template.reduce(state, { type: 'patchLayout', id: 'root', breakpoint: 'tablet', patch: { columns: 8 } });
  assert.deepEqual(template.resolveLayout(state.doc.nodes.root, 'mobile'), {
    mode: 'grid', direction: 'column', gap: 16, padding: 16, columns: 8,
  });
  state = template.reduce(state, { type: 'patchLayout', id: 'root', breakpoint: 'mobile', patch: { mode: 'free', minHeight: 320 } });
  assert.equal(template.resolveLayout(state.doc.nodes.root, 'mobile').mode, 'free');
  assert.equal(template.resolveLayout(state.doc.nodes.root, 'desktop').mode, 'grid');
});

test('switching to free layout seeds non-overlapping frames and duplicates preserve layout', () => {
  let state = template.initial();
  state = template.reduce(state, { type: 'add', id: 'a', parentId: 'root' });
  state = template.reduce(state, { type: 'add', id: 'b', parentId: 'root' });
  state = template.reduce(state, { type: 'patchLayout', id: 'root', breakpoint: 'desktop', patch: { mode: 'free' } });
  assert.equal(template.resolveFrame(state.doc.nodes.a, 'desktop').y, 0);
  assert.equal(template.resolveFrame(state.doc.nodes.b, 'desktop').y, 136);
  state = template.reduce(state, { type: 'patchLayout', id: 'a', breakpoint: 'desktop', patch: { mode: 'grid', columns: 6 } });
  state = template.reduce(state, { type: 'duplicate', id: 'a', newId: 'a-copy' });
  assert.equal(template.resolveLayout(state.doc.nodes['a-copy'], 'desktop').mode, 'grid');
  assert.equal(template.resolveLayout(state.doc.nodes['a-copy'], 'desktop').columns, 6);
  assert.equal(template.resolveFrame(state.doc.nodes['a-copy'], 'desktop').x, 16);
});

test('template backend conversion preserves slots and orphans placements for removed slots', () => {
  const version = {
    version: 1,
    root_id: 'canvas',
    nodes: [
      { id: 'canvas', kind: 'canvas', order: 0, props: { layouts: { desktop: { mode: 'flow' } } } },
      { id: 'slot-node', parent_id: 'canvas', kind: 'group', name: 'Hero', order: 0, props: {} },
    ],
    slots: [{ id: 'hero-slot', node_id: 'slot-node', name: 'Hero', accepts: ['html'] }],
    placements: [{ item_id: 'hero', slot_id: 'hero-slot', mode: 'flow' }],
    breakpoints: [{ id: 'desktop', columns: 12 }],
  };
  let state = template.initial(template.fromVersion(version));
  assert.equal(state.doc.nodes['slot-node'].type, 'container');
  assert.equal(state.doc.nodes['hero-slot'].type, 'slot');
  let roundTrip = template.toVersion(state.doc);
  assert.equal(roundTrip.slots[0].id, 'hero-slot');
  assert.equal(roundTrip.slots[0].node_id, 'slot-node');
  assert.equal(roundTrip.placements[0].slot_id, 'hero-slot');
  state = template.reduce(state, { type: 'remove', id: 'hero-slot' });
  roundTrip = template.toVersion(state.doc);
  assert.equal(roundTrip.slots.length, 0);
  assert.equal(roundTrip.placements[0].slot_id, '');
  assert.equal(roundTrip.placements[0].unassigned, true);
});

test('template conversion preserves multiple root slots, node kinds, and custom breakpoint props', () => {
  const version = {
    version: 4,
    root_id: 'canvas',
    nodes: [
      { id: 'canvas', kind: 'canvas', order: 0, props: { layouts: { desktop: { mode: 'flow' }, wide: { gap: 32 } } } },
      { id: 'grid-node', parent_id: 'canvas', kind: 'grid', order: 0 },
    ],
    slots: [
      { id: 'header', node_id: 'canvas', name: 'Header' },
      { id: 'main', node_id: 'canvas', name: 'Main' },
    ],
    placements: [{ item_id: 'hero', slot_id: 'main', mode: 'flow' }],
    breakpoints: [{ id: 'wide', min_width: 1440, columns: 16 }],
  };
  const doc = template.fromVersion(version);
  assert.equal(doc.nodes.header.type, 'slot');
  assert.equal(doc.nodes.main.type, 'slot');
  assert.equal(template.resolveLayout(doc.nodes['grid-node'], 'desktop').mode, 'grid');
  const output = template.toVersion(doc);
  assert.deepEqual(output.slots.map((slot) => slot.id).sort(), ['header', 'main']);
  assert.equal(output.root_id, 'canvas');
  assert.ok(output.slots.every((slot) => slot.node_id === 'canvas'));
  assert.equal(output.nodes.find((node) => node.id === 'grid-node').kind, 'grid');
  assert.equal(output.nodes.find((node) => node.id === 'canvas').kind, 'canvas');
  assert.equal(output.nodes.find((node) => node.id === 'canvas').props.layouts.wide.gap, 32);
  assert.equal(output.placements[0].slot_id, 'main');
});

test('template conversion keeps opaque node props, long stable ids, and slot geometry', () => {
  const longID = 'n' + 'a'.repeat(110);
  const version = {
    version: 2,
    root_id: longID,
    nodes: [{ id: longID, kind: 'grid', props: { analytics_hint: { owner: 'ops' }, slot_layouts: { hero: { frames: { desktop: { w: 640, h: 180 } } } } } }],
    slots: [{ id: 'hero', node_id: longID }], placements: [], breakpoints: [],
  };
  const doc = template.fromVersion(version);
  assert.equal(template.resolveFrame(doc.nodes.hero, 'desktop').w, 640);
  const output = template.toVersion(doc);
  assert.equal(output.root_id, longID);
  assert.equal(output.nodes[0].id, longID);
  assert.equal(output.nodes[0].kind, 'grid');
  assert.deepEqual(output.nodes[0].props.analytics_hint, { owner: 'ops' });
  assert.equal(output.nodes[0].props.slot_layouts.hero.frames.desktop.h, 180);
});

test('template save request sends only the current Template wrapper and CAS revision', () => {
  const currentResponse = {
    template: { id: 'tpl-root', name: 'Root template', revision: 7, draft: { version: 3 } },
    persisted: true,
    migrated: false,
  };
  const request = template.saveRequest(currentResponse.template, '', template.empty());
  assert.equal(request.page, '');
  assert.equal(request.expected_revision, 7);
  assert.equal(request.template.id, 'tpl-root');
  assert.equal(request.template.revision, 7);
  assert.equal(request.template.persisted, undefined);
  assert.equal(request.template.migrated, undefined);
  assert.equal(request.template.page, undefined);
  assert.ok(Array.isArray(request.template.draft.nodes));
});

test('template runtime breakpoint and placement resolution inherit without hiding new items', () => {
  const version = {
    breakpoints: [
      { id: 'desktop', min_width: 1024 },
      { id: 'tablet', min_width: 768, max_width: 1023 },
      { id: 'mobile', max_width: 767 },
    ],
    placements: [
      { item_id: 'hero', breakpoint: 'desktop', slot_id: 'main', mode: 'flow' },
      { item_id: 'hero', breakpoint: 'mobile', slot_id: '', unassigned: true, mode: 'flow' },
    ],
  };
  assert.equal(template.chooseBreakpoint(version, 390), 'mobile');
  assert.equal(template.chooseBreakpoint(version, 800), 'tablet');
  assert.equal(template.resolvePlacement(version, 'hero', 'tablet').slot_id, 'main');
  assert.equal(template.resolvePlacement(version, 'hero', 'mobile').unassigned, true);
  assert.equal(template.resolvePlacement(version, 'new-item', 'desktop'), null);
});
