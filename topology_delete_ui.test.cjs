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
    toastWarn: message => warnings.push(message), toastErr: (...args) => errors.push(args),
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

for (const state of ['healthy', 'up', 'down', 'stopped']) {
  test(`inventory tunnel in ${state} state disables delete and blocks the actual function`, async () => {
    const h = harness([topo(['peer:2'])], [tunnel('peer:2', state)]);
    const { button, html } = deleteButton(h);
    assert.match(button, / disabled /);
    assert.ok(button.includes(`title="${title}"`));
    assert.ok(html.includes('1 tunnels'));
    assert.ok(html.includes(notice.replace('{n}', '1')));
    await h.sandbox.topologyDelete('pair');
    assert.deepEqual(h.warnings, [notice.replace('{n}', '1')]);
    assert.equal(h.confirms.length, 0, 'guard runs before confirmation');
    assert.deepEqual(h.calls, [], 'blocked deletion sends no API request, including DELETE');
  });
}

test('mixed live/down inventory is counted, while stale and unrelated IDs are ignored', async () => {
  const h = harness([topo(['matrix', 'peer:2', 'deleted'])], [
    tunnel('matrix', 'healthy'), tunnel('peer:2', 'down'), tunnel('unassigned', 'stopped')
  ]);
  const { html } = deleteButton(h);
  assert.ok(html.includes('2 tunnels'));
  assert.ok(html.includes(notice.replace('{n}', '2')));
  await h.sandbox.topologyDelete('pair');
  assert.deepEqual(h.warnings, [notice.replace('{n}', '2')]);
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

test('delegated click cannot bypass the function guard on a disabled delete button', async () => {
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
    assert.doesNotMatch(button, / disabled(?: |>|$)/);
    assert.ok(html.includes('0 tunnels'));
    assert.ok(!html.includes(title));
    await h.sandbox.topologyDelete('pair');
    assert.deepEqual(h.confirms, [['Delete topology?', emptyBody, 'Delete topology']]);
    assert.equal(h.warnings.length, 0);
    assert.deepEqual(h.calls.map(c => [c.url, c.method]), [
      ['/api/topologies/pair', 'DELETE'], ['/api/topologies', undefined]
    ]);
    assert.equal(Object.keys(h.calls[0].body).length, 0);
  });
}

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
  assert.ok(html.includes(map[notice].replace('{n}', '1')));
  await h.sandbox.topologyDelete('pair');
  assert.deepEqual(h.warnings, [map[notice].replace('{n}', '1')]);
  assert.equal(h.confirms.length, 0);
  assert.equal(h.calls.length, 0);
  const empty = harness([topo()], [], { fa: true });
  await empty.sandbox.topologyDelete('pair');
  assert.equal(empty.confirms[0][1], map[emptyBody]);
  assert.ok(!source.includes('Its tunnels and services keep running.'));
  assert.ok(!faSource.includes('Only the topology grouping is removed. Its tunnels and services keep running.'));
});
