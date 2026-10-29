const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const test = require('node:test');

const source = fs.readFileSync(path.join(__dirname, 'topology.js'), 'utf8');
const faSource = fs.readFileSync(path.join(__dirname, 'i18n-fa.js'), 'utf8');
const title = 'Delete all tunnels in this topology first';
const notice = '{n} tunnel(s) still exist. Delete all tunnels in this topology first, including stopped tunnels.';
const emptyBody = 'This empty topology will be removed.';

function harness(topologies, tunnels, { fa = false, confirm = true, conflict = false } = {}) {
  const elements = new Map();
  const calls = [], confirms = [], warnings = [], errors = [];
  function el(id) {
    if (!elements.has(id)) elements.set(id, {
      hidden: false, disabled: false, value: '', textContent: '', innerHTML: '',
      classList: { toggle() {} }, contains() { return false; },
      addEventListener(type, callback) { this[type] = callback; }
    });
    return elements.get(id);
  }
  const sandbox = {
    window: {}, document: { activeElement: null, hidden: false, querySelectorAll() { return []; } },
    $: el, _tunnelDetail: null, setInterval() {}, renderTunnelsList() {},
    esc: value => String(value).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;'),
    toastWarn: (message, sub) => warnings.push([message, sub]), toastErr: (...args) => errors.push(args),
    entConfirm: async (...args) => { confirms.push(args); return confirm; },
    api: async (url, body, method) => {
      calls.push({ url, body, method });
      if (conflict && method === 'DELETE') throw new Error('409: topology still contains tunnels');
      return { topologies: [], tunnels: [] };
    }
  };
  vm.createContext(sandbox);
  vm.runInContext(faSource, sandbox, { filename: 'i18n-fa.js' });
  sandbox.tr = (_key, fallback) => fa ? sandbox.window.FA_TEXT[fallback] || fallback : fallback;
  vm.runInContext(source, sandbox, { filename: 'topology.js' });
  vm.runInContext(`topologyData = ${JSON.stringify({ topologies, tunnels })};`, sandbox);
  return { sandbox, el, calls, confirms, warnings, errors };
}

function topo(ids = [], id = 'pair') {
  return { id, name: 'Pair', role: 'iran', local_ip: '192.0.2.1', remote_ip: '192.0.2.2', tunnel_ids: ids };
}
function tunnel(id, state) { return { id, name: id, engine: 'frp', state }; }
function deleteButton(h, id = 'pair') {
  h.sandbox.topologyRender();
  const html = h.el('topologiesList').innerHTML;
  const button = html.match(new RegExp(`<button[^>]*data-topo-delete="${id}"[^>]*>`));
  assert.ok(button, 'delete button rendered');
  return { button: button[0], html };
}
function assertEnabledDanger(button, message) {
  assert.doesNotMatch(button, / disabled(?: |>|$)/, 'delete is never disabled');
  assert.ok(button.includes('hashem-btn-danger'), 'delete is styled as a danger action');
  assert.ok(message === undefined || button.includes(`title="${message}"`), 'button explains the blocked state');
}
function assertCount(html, count, label = `${count} existing tunnel(s)`) {
  const tag = html.match(/<span\b[^>]*class="topo-count"[^>]*>\s*(\d+)\s*<\/span>/);
  assert.ok(tag, 'existing-tunnel counter rendered');
  assert.equal(Number(tag[1]), count);
  assert.ok(tag[0].includes(`aria-label="${label}"`), 'numeric count has a meaningful accessible name');
}
function assertNoticeTooltip(html, message) {
  const wrapper = html.match(/<span\b[^>]*class="topo-delete-wrap"[^>]*>/);
  assert.ok(wrapper, 'delete wrapper retains a hoverable tooltip');
  assert.ok(wrapper[0].includes(`title="${message}"`), 'wrapper explains all existing tunnels, including stopped');
  assert.ok(!html.replace(/<[^>]*>/g, '').includes(message), 'verbose notice is not repeated in visible row content');
}

for (const state of ['healthy', 'up', 'down', 'stopped']) {
  test(`inventory tunnel in ${state} state keeps delete enabled but toasts the guard notice`, async () => {
    const h = harness([topo(['peer:2'])], [tunnel('peer:2', state)]);
    const { button, html } = deleteButton(h);
    assertEnabledDanger(button, title);
    assertCount(html, 1);
    assertNoticeTooltip(html, notice.replace('{n}', '1'));
    await h.sandbox.topologyDelete('pair');
    assert.equal(h.warnings.length, 1, 'a visible toast explains the blocked deletion');
    assert.deepEqual(h.warnings[0], ['Delete topology', notice.replace('{n}', '1')]);
    assert.equal(h.confirms.length, 0, 'guard runs before confirmation');
    assert.deepEqual(h.calls, [], 'blocked deletion sends no API request, including DELETE');
  });
}

test('mixed live/down inventory is counted, while stale and unrelated IDs are ignored', async () => {
  const h = harness([topo(['matrix', 'peer:2', 'deleted'])], [
    tunnel('matrix', 'healthy'), tunnel('peer:2', 'down'), tunnel('unassigned', 'stopped')
  ]);
  const { html } = deleteButton(h);
  assertCount(html, 2);
  assertNoticeTooltip(html, notice.replace('{n}', '2'));
  await h.sandbox.topologyDelete('pair');
  assert.equal(h.warnings.length, 1);
  assert.ok(h.warnings[0][1].includes('2'));
  assert.equal(h.confirms.length, 0);
  assert.equal(h.calls.length, 0);
});

test('guard checks the requested topology, not the currently selected empty topology', async () => {
  const h = harness([topo([], 'empty'), topo(['matrix'])], [tunnel('matrix', 'healthy')]);
  vm.runInContext("topologySelected = 'empty';", h.sandbox);
  await h.sandbox.topologyDelete('pair');
  assert.equal(h.warnings.length, 1);
  assert.equal(h.confirms.length, 0);
  assert.equal(h.calls.length, 0);
});

test('delegated click cannot bypass the function guard when tunnels remain', async () => {
  const h = harness([topo(['peer:2'])], [tunnel('peer:2', 'stopped')]);
  h.el('tab-tunnel').click({ target: { closest() { return { dataset: { topoDelete: 'pair' } }; } } });
  await Promise.resolve();
  assert.equal(h.warnings.length, 1);
  assert.equal(h.confirms.length, 0);
  assert.equal(h.calls.length, 0);
});

for (const ids of [[], ['deleted-matrix', 'peer:99']]) {
  test(`${ids.length ? 'stale deleted IDs' : 'empty group'} allow confirmed deletion without removing any engine`, async () => {
    const h = harness([topo(ids)], [tunnel('unassigned', 'healthy')]);
    const { button, html } = deleteButton(h);
    assertEnabledDanger(button);
    assertCount(html, 0);
    assert.ok(!html.includes(title));
    await h.sandbox.topologyDelete('pair');
    assert.deepEqual(h.confirms, [['Delete topology?', emptyBody, 'Delete topology']]);
    assert.equal(h.warnings.length, 0, 'empty topologies delete without a warning toast');
    assert.deepEqual(h.calls.map(c => [c.url, c.method]), [
      ['/api/topologies/pair', 'DELETE'], ['/api/topologies', undefined]
    ]);
    assert.equal(Object.keys(h.calls[0].body).length, 0);
  });
}

test('guard rechecks current inventory when a tunnel appears after an enabled row was rendered', async () => {
  const h = harness([topo(['peer:2'])], []);
  const { button } = deleteButton(h);
  assertEnabledDanger(button);
  vm.runInContext(`topologyData.tunnels = ${JSON.stringify([tunnel('peer:2', 'stopped')])}`, h.sandbox);
  await h.sandbox.topologyDelete('pair');
  assert.equal(h.warnings.length, 1, 'late inventory guard toasts instead of deleting');
  assert.equal(h.confirms.length, 0, 'late inventory guard runs before confirmation');
  assert.deepEqual(h.calls, [], 'a stale enabled row cannot send a DELETE');
});

test('duplicate and stale bindings do not inflate the existing inventory count', async () => {
  const h = harness([topo(['matrix', 'matrix', 'deleted'])], [tunnel('matrix', 'stopped')]);
  assert.equal(h.sandbox.topologyExistingTunnelCount(topo(['matrix', 'matrix', 'deleted'])), 1);
  await h.sandbox.topologyDelete('pair');
  assert.equal(h.warnings.length, 1);
  assert.equal(h.confirms.length, 0);
  assert.deepEqual(h.calls, []);
});

test('cancelled empty-group confirmation sends no DELETE request', async () => {
  const h = harness([topo()], [], { confirm: false });
  await h.sandbox.topologyDelete('pair');
  assert.equal(h.confirms.length, 1);
  assert.equal(h.calls.length, 0);
});

test('backend conflict remains handled without retrying or removing engines', async () => {
  const h = harness([topo()], [], { conflict: true });
  await h.sandbox.topologyDelete('pair');
  assert.deepEqual(h.calls.map(c => [c.url, c.method]), [['/api/topologies/pair', 'DELETE']]);
  assert.deepEqual(h.errors, [['Topology save failed', '409: topology still contains tunnels']]);
});

test('Persian title, count notice and empty confirmation are translated with unique keys', async () => {
  const h = harness([topo(['matrix'])], [tunnel('matrix', 'down')], { fa: true });
  const map = h.sandbox.window.FA_TEXT;
  for (const key of [title, notice, emptyBody]) {
    assert.equal(faSource.split(JSON.stringify(key) + ':').length - 1, 1, `unique translation: ${key}`);
    assert.match(map[key], /[\u0600-\u06ff]/);
  }
  const { button, html } = deleteButton(h);
  assert.ok(button.includes(`title="${map[title]}"`));
  assertNoticeTooltip(html, map[notice].replace('{n}', '1'));
  assertCount(html, 1, (map['{n} existing tunnel(s)'] || '{n} existing tunnel(s)').replace('{n}', '1'));
  await h.sandbox.topologyDelete('pair');
  assert.equal(h.warnings.length, 1, 'the blocked deletion warns in Persian');
  assert.deepEqual(h.warnings[0], [map['Delete topology'], map[notice].replace('{n}', '1')]);
  assert.equal(h.confirms.length, 0);
  assert.equal(h.calls.length, 0);
  const empty = harness([topo()], [], { fa: true });
  await empty.sandbox.topologyDelete('pair');
  assert.equal(empty.confirms[0][1], map[emptyBody]);
  assert.ok(!source.includes('Its tunnels and services keep running.'));
  assert.ok(!faSource.includes('Only the topology grouping is removed. Its tunnels and services keep running.'));
});
