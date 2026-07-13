'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');

global.window = {};
require('./ihtml-lib.js');

const { fmt, util, makeT, template, chatAgent, chatTrace, chatContent, itemLayout, runtime } = global.window.__ihtmlLib;

test('chat content format is additive, locks explicitly, and unknown values fail closed to text', () => {
  assert.equal(chatContent.maxRenderBytes, 512 * 1024, 'browser hard limit must accept the SDK maximum');
  assert.equal(chatContent.normalizeFormat(''), 'auto');
  assert.equal(chatContent.normalizeFormat('application/x-unknown'), 'text');
  assert.equal(chatContent.mergeFormat('auto', 'markdown'), 'markdown');
  assert.equal(chatContent.mergeFormat('markdown', 'html'), 'markdown', 'the first explicit format locks the stream');
  assert.equal(chatContent.mergeFormat('html', 'text'), 'html', 'an early explicit HTML delta remains authoritative');
  assert.equal(chatContent.mergeFormat('html', '', true), 'auto', 'assistant_reset starts a fresh rendering stream');
  assert.equal(chatContent.finalizeFormat('text', 'html'), 'html', 'assistant_done overrides the stream format');
  assert.equal(chatContent.finalizeFormat('html', 'application/x-unknown'), 'text', 'unknown final formats fail closed');
  assert.equal(chatContent.finalizeFormat('markdown', ''), 'markdown', 'an omitted final format preserves the stream format');
  assert.deepEqual(chatContent.doneSnapshot({ message: '' }), { present: true, message: '' }, 'an explicit empty final snapshot clears the draft');
  assert.deepEqual(chatContent.doneSnapshot({}), { present: false, message: '' }, 'an omitted snapshot preserves legacy streamed content');
  assert.equal(chatContent.resolveFormat('', '<table><tr><td>1</td></tr></table>'), 'html');
  assert.equal(chatContent.resolveFormat('', '```html\n<table></table>\n```'), 'markdown', 'HTML examples in code fences are not executed as HTML');
  assert.equal(chatContent.resolveFormat('unexpected', '<b>not interpreted</b>'), 'text');
});

test('chat answer byte ceiling accepts SDK overrides through 512 KiB and clips above it', () => {
  const within = 'x'.repeat(300 * 1024);
  const accepted = chatContent.clipUTF8(within, chatContent.maxRenderBytes);
  assert.equal(accepted.truncated, false);
  assert.equal(accepted.text.length, within.length);

  const oversized = '😀'.repeat(140 * 1024);
  const clipped = chatContent.clipUTF8(oversized, chatContent.maxRenderBytes);
  assert.equal(clipped.truncated, true);
  assert.ok(clipped.bytes <= 512 * 1024);
  assert.equal(clipped.text.length % 2, 0, 'UTF-16 surrogate pair remains complete');
  assert.equal(clipped.text.codePointAt(clipped.text.length - 2), 0x1F600);
});

test('chat Markdown renders generic rich blocks including GFM tables without business-specific rules', () => {
  const html = chatContent.markdownToHTML([
    '# Summary',
    '',
    '| Name | Amount | Note |',
    '| :--- | ---: | :---: |',
    '| First | **12.50** | `a|b` |',
    '',
    '> A quoted **note**.',
    '',
    '1. Open [documentation](https://example.test/docs)',
    '2. Review `result`',
    '',
    '```json',
    '{"ok": true}',
    '```',
  ].join('\n'));
  assert.match(html, /<h1>Summary<\/h1>/);
  assert.match(html, /<table><thead><tr><th scope="col" style="text-align: left">Name<\/th>/);
  assert.match(html, /<th scope="col" style="text-align: right">Amount<\/th>/);
  assert.match(html, /<td style="text-align: center"><code>a\|b<\/code><\/td>/);
  assert.match(html, /<blockquote><p>A quoted <strong>note<\/strong>\.<\/p><\/blockquote>/);
  assert.match(html, /<ol><li>Open <a href="https:\/\/example\.test\/docs">documentation<\/a><\/li>/);
  assert.match(html, /<pre><code class="language-json">\{&quot;ok&quot;: true\}<\/code><\/pre>/);
});

test('chat Markdown escapes raw HTML while auto and explicit HTML use the DOM sanitizer, and clips UTF-8 safely', () => {
  const html = chatContent.markdownToHTML('Plain <not-a-real-tag data-x="1"> & text\n\n<mark aria-label="note">raw</mark>');
  assert.match(html, /Plain &lt;not-a-real-tag data-x=&quot;1&quot;&gt; &amp; text/);
  assert.match(html, /&lt;mark aria-label=&quot;note&quot;&gt;raw&lt;\/mark&gt;/);

  const clipped = chatContent.clipUTF8('你好ab', 7);
  assert.deepEqual(clipped, { text: '你好a', bytes: 7, truncated: true });
  assert.deepEqual(chatContent.clipUTF8('plain', 32), { text: 'plain', bytes: 5, truncated: false });
  assert.deepEqual(chatContent.splitTableRow('| `a|b` | c\\|d |'), ['`a|b`', 'c|d']);
});

test('chat inline parsing stays bounded for hostile unmatched delimiters', () => {
  const hostile = 'IHTML_CONTENT_TOKEN' + '_'.repeat(100000) + '`'.repeat(100000) + '['.repeat(50000) + '<a '.repeat(10000);
  const started = Date.now();
  const html = chatContent.markdownToHTML(hostile);
  assert.ok(html.length > hostile.length, 'hostile punctuation is escaped as content');
  assert.ok(Date.now() - started < 1000, 'unmatched inline delimiters must not trigger quadratic rescans');
});

test('chat HTML auto detection stays linear for many unterminated tags', () => {
  const hostile = '<div '.repeat(50000);
  const started = Date.now();
  assert.equal(chatContent.resolveFormat('', hostile), 'markdown');
  assert.ok(Date.now() - started < 1500, 'unterminated tags must not trigger a quadratic closing-tag search');
});

test('chat link labels remove invisible Unicode before applying a bounded visible label', () => {
  assert.deepEqual(chatContent.compactLinkLabel('\u2800\u3164\u200b', 320), { text: '', visible: false, truncated: false });
  assert.deepEqual(chatContent.compactLinkLabel('  safe\n\u2800 documentation  ', 320), { text: 'safe documentation', visible: true, truncated: false });
  assert.deepEqual(chatContent.compactLinkLabel('x' + '\u2800'.repeat(10000), 320), { text: 'x', visible: true, truncated: false });
  const bounded = chatContent.compactLinkLabel('a'.repeat(500), 320);
  assert.equal(Array.from(bounded.text).length, 320);
  assert.equal(bounded.text.endsWith('…'), true);
  assert.equal(bounded.visible, true);
  assert.equal(bounded.truncated, true);
});

test('runtime isolates embedded CSS by exact instance while preserving standalone compatibility', () => {
  const css = '.ui-card{display:none}';
  const a = runtime.scopeCSS(css, false, true, 'instance-a');
  const b = runtime.scopeCSS(css, false, true, 'instance-b');
  assert.match(a, /data-ihtml-instance="instance-a"/);
  assert.doesNotMatch(a, /instance-b/);
  assert.notEqual(a, b);
  assert.equal(runtime.scopeCSS(css, true, true, 'standalone'), css);
  assert.equal(runtime.scopeCSS(css, false, false, 'unsafe'), css);
});

test('runtime persistence keys separate host, base, user, and value kind', () => {
  const key = runtime.storageKey('csid', 'https://ui.test/app/', '#left', 'alice');
  assert.equal(key, runtime.storageKey('csid', 'https://ui.test/app/', '#left', 'alice'));
  assert.notEqual(key, runtime.storageKey('csid', 'https://ui.test/app/', '#right', 'alice'));
  assert.notEqual(key, runtime.storageKey('csid', 'https://ui.test/app/', '#left', 'bob'));
  assert.notEqual(key, runtime.storageKey('model', 'https://ui.test/app/', '#left', 'alice'));
});

test('runtime validates mount options before browser-side effects', () => {
  const input = { base: '/sdk', params: { uid: 'alice' }, routing: 'memory' };
  const normalized = runtime.normalizeMountOptions(input, 'https://host.test/app/page', false);
  assert.equal(normalized.base, 'https://host.test/sdk/');
  assert.equal(normalized.routing, 'memory');
  assert.equal(normalized.params.get('uid'), 'alice');
  assert.deepEqual(input, { base: '/sdk', params: { uid: 'alice' }, routing: 'memory' }, 'preflight must not mutate host options');
  assert.equal(
    runtime.normalizeMountOptions({ base: 'https://api.test/root?token=secret#page' }, 'https://host.test/', false).base,
    'https://api.test/root/',
    'page query/hash state must not leak into API path resolution',
  );
  assert.throws(() => runtime.normalizeMountOptions({ base: 'http://[' }, 'https://host.test/', false));
  assert.throws(() => runtime.normalizeMountOptions({ base: 'javascript:alert(1)' }, 'https://host.test/', false), /http/);
  assert.throws(() => runtime.normalizeMountOptions({ base: 'data:text/plain,nope' }, 'https://host.test/', false), /http/);
  assert.throws(() => runtime.normalizeMountOptions({ base: 'https://user:pass@api.test/' }, 'https://host.test/', false), /credentials/);
  assert.throws(() => runtime.normalizeMountOptions({ routing: 'absolute' }, 'https://host.test/', false), /routing/);
  assert.throws(() => runtime.normalizeMountOptions('bad-options', 'https://host.test/', false), /options/);
});

test('runtime migrates legacy storage once without overwriting scoped data or deleting rollback keys', () => {
  const makeStorage = (initial) => {
    const values = new Map(Object.entries(initial || {}));
    return {
      getItem: (key) => values.has(key) ? values.get(key) : null,
      setItem: (key, value) => values.set(String(key), String(value)),
      values,
    };
  };
  const local = makeStorage({
    'ihtml.theme': '{"mode":"dark"}',
    'ihtml.model': 'legacy-model',
    'ihtml.theme.scoped': '{"mode":"light"}',
  });
  assert.equal(runtime.migrateLegacyStorage(local, 'migration.local', [
    { legacy: 'ihtml.theme', scoped: 'ihtml.theme.scoped' },
    { legacy: 'ihtml.model', scoped: 'ihtml.model.scoped' },
  ]), true);
  assert.equal(local.getItem('ihtml.theme.scoped'), '{"mode":"light"}', 'a scoped value wins over the legacy value');
  assert.equal(local.getItem('ihtml.model.scoped'), 'legacy-model');
  assert.equal(local.getItem('ihtml.model'), 'legacy-model', 'legacy keys remain available for rollback');
  local.setItem('ihtml.model', 'changed-after-claim');
  assert.equal(runtime.migrateLegacyStorage(local, 'migration.local', [
    { legacy: 'ihtml.model', scoped: 'ihtml.model.scoped' },
  ]), false);
  assert.equal(local.getItem('ihtml.model.scoped'), 'legacy-model', 'the marker prevents another mount/user from claiming again');

  const a = { id: 'a' };
  const b = { id: 'b' };
  assert.equal(runtime.selectPrimary(a, [a, b]), a);
  assert.equal(runtime.selectPrimary(a, [b]), b, 'destroying the primary promotes the first remaining live instance');
  assert.equal(runtime.selectPrimary(a, []), null);
});

test('runtime inspect range clamps offsets and exposes an explicit next page', () => {
  assert.deepEqual(runtime.inspectRange(512, 0, 99), { total: 512, offset: 0, limit: 20, end: 20, nextOffset: 20, truncated: true });
  assert.deepEqual(runtime.inspectRange(10, 100, 20), { total: 10, offset: 10, limit: 20, end: 10, nextOffset: 0, truncated: false });
  assert.deepEqual(runtime.inspectRange(250, undefined, undefined, true), { total: 250, offset: 0, limit: 200, end: 200, nextOffset: 200, truncated: true });
});

test('runtime module facade captures the exact Item lifecycle instead of a same-ID replacement', () => {
  const oldRecord = { lifecycle: 7 };
  const replacement = { lifecycle: 8 };
  const registry = new Map([['weather', oldRecord]]);
  assert.equal(runtime.itemLifecycleRecord(registry, 'weather', 7), oldRecord);
  registry.set('weather', replacement);
  assert.equal(runtime.itemLifecycleRecord(registry, 'weather', 7), null,
    'a resumed old module cannot attach resources to the replacement record');
  assert.equal(runtime.itemLifecycleRecord(registry, 'weather', 8), replacement);

  const expression = runtime.itemFacadeExpression('window.__instanceAPI', 'weather', 7);
  assert.equal(expression, '(window.__instanceAPI).__itemFacade("weather",7)');
  const source = 'await Promise.resolve();\nihtml.poll(refresh, 1000);';
  assert.equal(runtime.moduleItemScript(source, expression), 'const ihtml = ' + expression + ';\n' + source);
});

test('runtime Item script binding never rewrites source strings or conflicting module declarations', () => {
  const api = 'window.__instanceAPI';
  const source = 'console.log("const ihtml = window.ihtml")';
  assert.equal(runtime.hasIHTMLBinding(source), false);
  const classic = runtime.classicItemScript(source, api, 'code-card', '__private_api');
  assert.match(classic, /function\(ihtml\)/);
  assert.ok(classic.includes(source), 'valid source text must be embedded byte-for-byte');
  assert.doesNotThrow(() => new Function(classic));
  assert.equal(runtime.hasIHTMLBinding('const sample = `export const ihtml = window.ihtml`;\nihtml.bus.emit("ok")'), false,
    'declaration-looking template text is not a binding');

  for (const moduleSource of [
    'import ihtml from "./runtime.js";\nexport default ihtml;',
    'export const ihtml = {};',
    'const ihtml = window.ihtml;\nexport { ihtml };',
  ]) {
    assert.equal(runtime.hasIHTMLBinding(moduleSource), true);
    assert.equal(runtime.moduleItemScript(moduleSource, api), moduleSource, 'conflicting module source must be untouched');
  }
  assert.equal(runtime.moduleItemScript('export const answer = ihtml.config.answer;', api),
    'const ihtml = ' + api + ';\nexport const answer = ihtml.config.answer;');

  const regexSource = 'const matcher = /export\\s+const\\s+ihtml[\\/]/giu;\nexport const answer = ihtml.config.answer;';
  assert.equal(runtime.hasIHTMLBinding(regexSource), false, 'declaration text inside a RegExp literal is not a binding');
  assert.equal(runtime.moduleItemScript(regexSource, api), 'const ihtml = ' + api + ';\n' + regexSource);
  const leadingRegex = '/export const ihtml/.test(source);\nexport const answer = ihtml.config.answer;';
  assert.equal(runtime.hasIHTMLBinding(leadingRegex), false);
  assert.equal(runtime.hasIHTMLBinding('const ratio = total / count;\nexport const answer = ihtml.config.answer;'), false,
    'division must not be consumed as a RegExp literal');

  const escapedIdentifier = 'const \\u0069html = window.ihtml;\nexport { \\u0069html };';
  assert.equal(runtime.hasIHTMLBinding(escapedIdentifier), true,
    'Unicode escapes that decode to ihtml must prevent a duplicate injected declaration');
  assert.equal(runtime.moduleItemScript(escapedIdentifier, api), escapedIdentifier);
  const escapedTail = 'let ihtm\\u006c = window.ihtml;\nexport { ihtm\\u006c };';
  assert.equal(runtime.hasIHTMLBinding(escapedTail), true);
  assert.equal(runtime.moduleItemScript(escapedTail, api), escapedTail);

  const nestedVar = 'function localOnly() { var ihtml = {}; return ihtml; }\nsetTimeout(() => ihtml.bus.emit("outer"), 0);';
  assert.equal(runtime.hasIHTMLBinding(nestedVar), false, 'function-scoped var does not bind the module scope');
  assert.equal(runtime.moduleItemScript(nestedVar, api), 'const ihtml = ' + api + ';\n' + nestedVar);
  assert.equal(runtime.hasIHTMLBinding('const helper = { run() { var ihtml = {}; return ihtml; } };\nihtml.bus.emit("outer");'), false,
    'var inside an object method remains function-scoped');
  assert.equal(runtime.hasIHTMLBinding('{ var ihtml = {}; }'), true,
    'var in an ordinary module block still hoists and conflicts with the injected binding');
  assert.equal(runtime.hasIHTMLBinding('foo()\n{ var ihtml = {}; }'), true,
    'a call followed by an ASI-separated bare block is not a method body');
  assert.equal(runtime.hasIHTMLBinding('(foo)\n{ var ihtml = {}; }'), true);
  for (const propertyKeywordBeforeBlock of [
    'obj.class()\n{ var ihtml = {}; }',
    'obj.class\n{ var ihtml = {}; }',
    'const o = { class: 1 };\n{ var ihtml = {}; }',
    'obj.function()\n{ var ihtml = {}; }',
  ]) {
    assert.equal(runtime.hasIHTMLBinding(propertyKeywordBeforeBlock), true,
      'property names that happen to be keywords must not hide a later module-scoped var');
  }

  const aliasedImport = 'import { ihtml as localAlias } from "./dep.js";\nsetTimeout(() => ihtml.bus.emit("outer"), 0);';
  assert.equal(runtime.hasIHTMLBinding(aliasedImport), false, 'an imported property name is not the local binding');
  assert.equal(runtime.hasIHTMLBinding('import { localAlias as ihtml } from "./dep.js";'), true);
  assert.equal(runtime.hasIHTMLBinding('import { "x" as ihtml } from "./dep.js";\nexport { ihtml };'), true,
    'a string-named import can still bind the local name ihtml');
  assert.equal(runtime.hasIHTMLBinding('import { "ihtml" as localAlias } from "./dep.js";\nihtml.bus.emit("outer");'), false);
  const aliasedProperty = 'const { ihtml: localAlias } = source;\nsetTimeout(() => ihtml.bus.emit("outer"), 0);';
  assert.equal(runtime.hasIHTMLBinding(aliasedProperty), false, 'a destructured property key is not the local binding');
  assert.equal(runtime.hasIHTMLBinding('const { localAlias: ihtml } = source;'), true);
  assert.equal(runtime.hasIHTMLBinding('const { ihtml } = source;'), true);

  const regexAfterBlock = 'if (ready) {} /export const ihtml/.test(source);\nexport const answer = ihtml.config.answer;';
  assert.equal(runtime.hasIHTMLBinding(regexAfterBlock), false, 'a RegExp statement after a control block is masked');
  assert.equal(runtime.moduleItemScript(regexAfterBlock, api), 'const ihtml = ' + api + ';\n' + regexAfterBlock);
  assert.equal(runtime.hasIHTMLBinding('class C {} /export const ihtml/.test(source);\nexport const answer = ihtml.config.answer;'), false,
    'a RegExp statement after a class body is masked');
  assert.equal(runtime.hasIHTMLBinding('export default class {} /export const ihtml/.test(source);\nexport const answer = ihtml.config.answer;'), false);
  assert.equal(runtime.hasIHTMLBinding('debugger\n/const ihtml/.test(source);\nexport const answer = ihtml.config.answer;'), false);
  assert.equal(runtime.hasIHTMLBinding('while (ready) { continue\n/var ihtml/.test(source); }\nexport const answer = ihtml.config.answer;'), false);
  assert.equal(runtime.hasIHTMLBinding('label: {} /const ihtml/.test(source);\nexport const answer = ihtml.config.answer;'), false,
    'a RegExp statement after a labeled block must stay masked');

  const nestedShadow = [
    'function localOnly() { const ihtml = { local: true }; return ihtml; }',
    '{ let ihtml = { block: true }; void ihtml; }',
    'setTimeout(() => ihtml.bus.emit("outer-instance"), 0);',
  ].join('\n');
  assert.equal(runtime.hasIHTMLBinding(nestedShadow), false,
    'nested lexical shadows do not conflict with a module-level instance binding');
  assert.equal(runtime.moduleItemScript(nestedShadow, api), 'const ihtml = ' + api + ';\n' + nestedShadow);
  const nestedClassic = runtime.classicItemScript(nestedShadow, api, 'nested-shadow', '__private_api');
  assert.match(nestedClassic, /function\(ihtml\)/,
    'a nested shadow must not make the rest of a classic Item fall back to the primary global instance');
  assert.ok(nestedClassic.includes(nestedShadow));
  assert.doesNotThrow(() => new Function(nestedClassic));

  const ownTopLevelBinding = 'const ihtml = { own: true }; void ihtml;';
  assert.equal(runtime.hasIHTMLBinding(ownTopLevelBinding), true);
  const classicWithOwnBinding = runtime.classicItemScript(ownTopLevelBinding, api, 'own-binding', '__private_api');
  assert.match(classicWithOwnBinding, /function\(ihtml\)/);
  assert.ok(classicWithOwnBinding.includes(ownTopLevelBinding));
  assert.doesNotThrow(() => new Function(classicWithOwnBinding));
});

test('runtime visibility intersects viewport and every clipping ancestor', () => {
  const rect = { left: 50, top: 50, right: 150, bottom: 150 };
  assert.equal(runtime.rectVisible(rect, { width: 200, height: 200 }, []), true);
  assert.equal(runtime.rectVisible(rect, { width: 200, height: 200 }, [
    { left: 0, top: 0, right: 40, bottom: 200, clipX: true, clipY: false },
  ]), false);
  assert.equal(runtime.rectVisible(rect, { width: 200, height: 200 }, [
    { left: 0, top: 75, right: 200, bottom: 125, clipX: true, clipY: true },
  ]), true);
  assert.equal(runtime.rectVisible(rect, { width: 200, height: 200 }, [
    { left: 50, top: 0, right: 50, bottom: 200, clipX: true, clipY: false },
  ]), false, 'a zero-width overflow clip hides all horizontal content');
  assert.equal(runtime.rectVisible({ left: 210, top: 0, right: 250, bottom: 40 }, { width: 200, height: 200 }, []), false);
});

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

test('confirmation dismissal builds cancel_tool without changing the signed card', () => {
  const call = {
    id: 'call-1', name: 'ui_page_delete', body: { name: 'old' },
    confirmation_nonce: 'nonce', confirmation_token: 'signed-token',
  };
  const event = chatAgent.cancelToolEvent(call);
  assert.deepEqual(event, { type: 'cancel_tool', tool_call: call });
  assert.equal(event.tool_call.confirmation_token, 'signed-token');
  assert.deepEqual(chatAgent.cancelToolEvent(null), { type: 'cancel_tool', tool_call: null });
});

test('cancel acknowledgements correlate by card id and nonce without accepting ambiguous events', () => {
  const call = { id: 'call-1', confirmation_nonce: 'nonce-1' };
  assert.equal(chatAgent.cancelAckMatches(call, {
    type: 'cancel_tool_result', tool_id: 'call-1', confirmation_nonce: 'nonce-1', tool_status: 'cancelled',
  }), true);
  assert.equal(chatAgent.cancelAckMatches(call, { type: 'cancel_tool_result', tool_id: 'call-1' }), false);
  assert.equal(chatAgent.cancelAckMatches(call, { type: 'cancel_tool_result', confirmation_nonce: 'nonce-1' }), false, 'an id-bearing card requires both dimensions');
  assert.equal(chatAgent.cancelAckMatches(call, { type: 'cancel_tool_result', tool_id: 'other', confirmation_nonce: 'nonce-1' }), false);
  assert.equal(chatAgent.cancelAckMatches(call, { type: 'cancel_tool_result', tool_id: 'call-1', confirmation_nonce: 'other' }), false);
  assert.equal(chatAgent.cancelAckMatches(call, { type: 'cancel_tool_result' }), false);
  assert.equal(chatAgent.cancelAckMatches({}, { type: 'cancel_tool_result', tool_id: 'call-1' }), false);
  assert.equal(chatAgent.cancelAckMatches({ confirmation_nonce: 'nonce-1' }, { type: 'cancel_tool_result', confirmation_nonce: 'nonce-1' }), true, 'id-less provider cards still correlate by unique nonce');
});

test('turn stop and cancellation transport failures preserve honest confirmation states', () => {
  assert.equal(chatAgent.statusAfterTurnStop('running'), 'error', 'a started tool becomes locked and unknown, not falsely cancelled');
  assert.equal(chatAgent.statusAfterTurnStop('pending'), 'pending', 'a normal stop never revokes a signed confirmation card');
  assert.equal(chatAgent.statusAfterTurnStop('success'), 'success');
  assert.equal(chatAgent.cancellationFailureTerminal(false, false), false, 'a request that was never sent may be retried');
  assert.equal(chatAgent.cancellationFailureTerminal(true, false), true, 'disconnect after send must lock the card');
  assert.equal(chatAgent.cancellationFailureTerminal(false, true), true, 'an explicit terminal failure remains terminal');
});

test('streamed rich answers use a bounded render cadence', () => {
  assert.equal(chatAgent.streamRenderDelay(1000, 1000, 80), 80);
  assert.equal(chatAgent.streamRenderDelay(1000, 1030, 80), 50);
  assert.equal(chatAgent.streamRenderDelay(1000, 1090, 80), 0);
  assert.equal(chatAgent.streamRenderDelay(0, 0, 10000), 250, 'untrusted intervals are capped');
  assert.equal(chatAgent.streamRenderDelay(0, 0, 1), 16, 'rendering never busy-loops');
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
    budget: {
      model_calls: 2, max_model_calls: 24, subtasks: 1, max_subtasks: 3,
      tool_calls: 5, max_tool_calls: 32, max_tool_calls_per_tool: 8, max_repeated_tool_calls: 2,
    },
  });
  state = chatAgent.project(state, {
    type: 'assistant_agent',
    agent: { mode: 'deep', agent: 'ui_inspector', task_id: 'task-1', parent_task_id: 'root', phase: 'inspect', status: 'completed', summary: 'Checked' },
  });
  assert.equal(Object.keys(state.agents).length, 1, 'the same task updates instead of appending a log row');
  assert.equal(state.agents['task-1'].status, 'completed');
  assert.deepEqual(chatAgent.budgetParts(state.budget), {
    modelCalls: 2, maxModelCalls: 24, subtasks: 1, maxSubtasks: 3,
    toolCalls: 5, maxToolCalls: 32, maxToolCallsPerTool: 8, maxRepeatedToolCalls: 2,
    toolBudgetCode: '', toolBudgetLimit: '', toolBudgetTool: '',
  });
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

  const pending = chatTrace.toolView({
    name: 'custom_remote_tool', reason: 'authorization=Bearer ' + secret,
  }, { type: 'confirmation_required', tool_status: 'pending' }, makeT('en'));
  assert.doesNotMatch(JSON.stringify(pending), new RegExp(secret));
  assert.match(pending.reason, /\[redacted\]/);
  assert.equal(chatTrace.safeText('authToken=' + secret, 500), 'authToken=[redacted]');
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

test('item layout remote fingerprints distinguish real writes from harmless resyncs', () => {
  const page = 'dashboard';
  const stored = {
    version: 1, page, mode: 'grid',
    orderBySlot: { main: ['card'] },
    items: { card: { slot: 'main', flow: { span: 6 }, free: { x: 0, y: 0, width: 320, height: 180, z: 0 } } },
  };
  const first = itemLayout.remoteSnapshot(stored, page);
  const equivalent = itemLayout.remoteSnapshot(JSON.parse(JSON.stringify(stored)), page);
  const changed = itemLayout.remoteSnapshot(Object.assign({}, stored, { mode: 'free' }), page);
  const wrongPage = itemLayout.remoteSnapshot(stored, 'other');
  assert.equal(first.valid, true);
  assert.equal(first.fingerprint, equivalent.fingerprint);
  assert.notEqual(first.fingerprint, changed.fingerprint);
  assert.equal(wrongPage.valid, false);
  assert.equal(wrongPage.fingerprint, '');
});

test('item layout fingerprints and dirty checks ignore object key insertion order', () => {
  const page = 'dashboard';
  const first = {
    page, mode: 'grid',
    items: { a: { slot: 'main', appearance: { color: '#111111', background: '#ffffff' } }, b: { slot: 'side' }, c: { slot: 'main' } },
    orderBySlot: { main: ['a', 'c'], side: ['b'] },
  };
  const reordered = {
    orderBySlot: { side: ['b'], main: ['a', 'c'] },
    items: { c: { slot: 'main' }, b: { slot: 'side' }, a: { appearance: { background: '#ffffff', color: '#111111' }, slot: 'main' } },
    mode: 'grid', page,
  };
  assert.equal(itemLayout.remoteSnapshot(first, page).fingerprint, itemLayout.remoteSnapshot(reordered, page).fingerprint);
  assert.equal(itemLayout.remoteSnapshot(first, page).sourceFingerprint, itemLayout.remoteSnapshot(reordered, page).sourceFingerprint);
  const state = itemLayout.initial(page, first);
  const equivalent = itemLayout.reduce(state, { type: 'replaceDoc', doc: reordered });
  assert.equal(equivalent.dirty, false);
  assert.notEqual(itemLayout.remoteSnapshot(first, page).fingerprint, itemLayout.remoteSnapshot({ ...first, orderBySlot: { main: ['c', 'a'], side: ['b'] } }, page).fingerprint);
  assert.notEqual(itemLayout.remoteSnapshot(first, page).sourceFingerprint, itemLayout.remoteSnapshot({ ...first, extension: { preserve: true } }, page).sourceFingerprint);
});

test('item layout remote classification separates equivalent and conflicting revisions', () => {
  const base = {
    baselineRevision: 'sha256:base',
    baselineSourceFingerprint: 'json:base',
    remoteRevision: 'sha256:base',
    remoteSourceFingerprint: 'json:base',
    localSourceFingerprint: 'json:local',
    remoteExists: true,
    remoteValid: true,
  };
  assert.equal(itemLayout.classifyRemoteChange(base), 'unchanged');
  assert.equal(itemLayout.classifyRemoteChange({ ...base, remoteRevision: 'sha256:reordered' }), 'equivalent-baseline');
  assert.equal(itemLayout.classifyRemoteChange({ ...base, remoteRevision: 'sha256:peer', remoteSourceFingerprint: 'json:local' }), 'matches-local');
  assert.equal(itemLayout.classifyRemoteChange({ ...base, remoteRevision: 'sha256:unknown-extension', remoteSourceFingerprint: 'json:extended' }), 'conflict');
  assert.equal(itemLayout.classifyRemoteChange({ ...base, remoteRevision: 'missing', remoteSourceFingerprint: 'missing', remoteExists: false, remoteValid: false }), 'conflict');
});

test('item layout history reconciles additions and removals before undo or redo', () => {
  let state = itemLayout.initial('work', {
    mode: 'grid',
    items: { a: { slot: 'main', flow: { span: 12 } }, removed: { slot: 'main', flow: { span: 4 } } },
    orderBySlot: { main: ['a', 'removed'] },
  });
  state = itemLayout.reduce(state, { type: 'select', id: 'removed' });
  state = itemLayout.reduce(state, { type: 'patch', id: 'a', section: 'flow', patch: { span: 7 } });
  state = itemLayout.reduce(state, { type: 'select', id: 'removed' });
  const live = itemLayout.normalize({
    page: 'work', mode: 'grid',
    items: { a: { slot: 'main', flow: { span: 7 } }, added: { slot: 'main', flow: { span: 5 } } },
    orderBySlot: { main: ['a', 'added'] },
  });
  state = itemLayout.reconcileState(state, live);
  for (const snapshot of [state.doc, state.baseline, ...state.past, ...state.future]) {
    assert.ok(snapshot.items.added, 'new live Item is present in every history snapshot');
    assert.equal(snapshot.items.removed, undefined, 'deleted live Item is absent from every history snapshot');
  }
  assert.equal(state.selectedId, '');
  state = itemLayout.reduce(state, { type: 'undo' });
  assert.ok(state.doc.items.added);
  assert.equal(state.doc.items.removed, undefined);
  state = itemLayout.reduce(state, { type: 'redo' });
  assert.ok(state.doc.items.added);
  assert.equal(state.doc.items.removed, undefined);
  assert.equal(state.doc.items.a.flow.span, 7);
});

test('item layout save acknowledgement can reconcile Item topology changed in flight', () => {
  let state = itemLayout.initial('work', {
    mode: 'grid',
    items: { a: { slot: 'main', flow: { span: 12 } } },
    orderBySlot: { main: ['a'] },
  });
  state = itemLayout.reduce(state, { type: 'patch', id: 'a', section: 'flow', patch: { span: 8 } });
  const requestSnapshot = state.doc;
  const live = itemLayout.normalize({
    page: 'work', mode: 'grid',
    items: {
      a: { slot: 'side', flow: { span: 8 } },
      added: { slot: 'main', flow: { span: 4 } },
    },
    orderBySlot: { side: ['a'], main: ['added'] },
  });

  // An Item event lands while the old layout request is still in flight.
  state = itemLayout.reconcileState(state, live);
  assert.equal(state.doc.items.a.slot, 'side');
  assert.ok(state.doc.items.added);

  // The editor first acknowledges the exact request snapshot, then immediately
  // reconciles against the current live Item topology.
  state = itemLayout.reduce(state, { type: 'markSaved', doc: requestSnapshot });
  state = itemLayout.reconcileState(state, live);
  assert.equal(state.doc.items.a.slot, 'side');
  assert.ok(state.doc.items.added);
  assert.deepEqual(state.doc.orderBySlot.side, ['a']);
  assert.deepEqual(state.doc.orderBySlot.main, ['added']);
  assert.equal(state.dirty, false);
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

test('template pointer geometry is thresholded and grid sizing accounts for padding and gaps', () => {
  assert.equal(template.pointerMoved({ x: 10, y: 10 }, { x: 13, y: 12 }, 4), false);
  assert.equal(template.pointerMoved({ x: 10, y: 10 }, { x: 14, y: 10 }, 4), true);
  assert.deepEqual(template.moveFrame({ x: 16, y: 24 }, { x: 11, y: -9 }, 8), { x: 24, y: 16 });

  const geometry = { clientWidth: 664, paddingLeft: 16, paddingRight: 16, gap: 8, columns: 4 };
  assert.equal(template.gridSpanForWidth(312, geometry), 2, 'two tracks plus one gap resolve to two columns');
  assert.deepEqual(template.resizeFrame('grid', { width: 152, height: 120 }, { x: 160, y: 200 }, geometry), { span: 2 }, 'grid pointer resize never persists a rendered height');
  assert.deepEqual(template.resizeFrame('free', { width: 240, height: 120 }, { x: 19, y: 13 }, geometry), { w: 256, h: 136 });
});

test('template touch reorder actions match reducer move semantics', () => {
  let state = template.initial();
  state = template.reduce(state, { type: 'add', id: 'a', parentId: 'root' });
  state = template.reduce(state, { type: 'add', id: 'a-child', parentId: 'a' });
  state = template.reduce(state, { type: 'add', id: 'b', parentId: 'root' });
  state = template.reduce(state, { type: 'add', id: 'slot', nodeType: 'slot', parentId: 'root' });
  state = template.reduce(state, { type: 'add', id: 'after-slot', parentId: 'root' });

  const b = template.reorderActions(state.doc, 'b');
  assert.deepEqual(b.up, { type: 'move', id: 'b', parentId: 'root', index: 0 });
  assert.deepEqual(b.indent, { type: 'move', id: 'b', parentId: 'a' });
  assert.equal(b.outdent, null);
  assert.equal(template.reorderActions(state.doc, 'after-slot').indent, null, 'a layer cannot indent into a slot sibling');

  const nested = template.reorderActions(state.doc, 'a-child');
  assert.deepEqual(nested.outdent, { type: 'move', id: 'a-child', parentId: 'root', index: 1 });
  const moved = template.reduce(state, nested.outdent);
  assert.deepEqual(moved.doc.children.root, ['a', 'a-child', 'b', 'slot', 'after-slot']);
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
