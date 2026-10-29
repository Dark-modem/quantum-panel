const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const test = require('node:test');

const source = fs.readFileSync(path.join(__dirname, 'topology.js'), 'utf8');
const faSource = fs.readFileSync(path.join(__dirname, 'i18n-fa.js'), 'utf8');

function harness({ fa = false, topologies = [], tunnels = [], failOn } = {}) {
  const elements = new Map();
  const calls = [], toasts = [];
  let created = null;
  function el(id) {
    if (!elements.has(id)) elements.set(id, {
      hidden: false, disabled: false, value: '', textContent: '', innerHTML: '', dataset: {},
      classList: { _s: new Set(), toggle(c, v) { if (v === undefined) this._s.has(c) ? this._s.delete(c) : this._s.add(c); else v ? this._s.add(c) : this._s.delete(c); }, add(c) { this._s.add(c); }, remove(c) { this._s.delete(c); }, contains(c) { return this._s.has(c); } },
      contains() { return false; },
      addEventListener(type, cb) { this[type] = cb; }
    });
    return elements.get(id);
  }
  const sandbox = {
    window: {}, document: { activeElement: null, hidden: false, querySelectorAll() { return []; } },
    $: el, _tunnelDetail: null, setInterval() {}, renderTunnelsList() {},
    skel: n => '<div class="ent-skel"></div>'.repeat(n || 3),
    // The draft must live in the standard browser store.
    localStorage: { _d: new Map(), getItem(k) { return this._d.has(k) ? this._d.get(k) : null; }, setItem(k, v) { this._d.set(k, String(v)); }, removeItem(k) { this._d.delete(k); } },
    esc: v => String(v).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;'),
    toastOk: (...a) => toasts.push(['ok', ...a]), toastErr: (...a) => toasts.push(['err', ...a]), toastWarn: (...a) => toasts.push(['warn', ...a]),
    entConfirm: async () => true,
    api: async (url, body, method) => {
      const m = method || (body ? 'POST' : 'GET');
      calls.push({ url, body, method: m });
      if (failOn && failOn(url, body, m)) throw new Error('boom');
      if (url === '/api/topologies' && !body) {
        if (created && !topologies.some(t => t.id === created.id)) topologies = [...topologies, created];
        return { topologies, tunnels };
      }
      if (url === '/api/topologies' && body) created = { id: 'new1', name: body.name, role: body.role, local_ip: body.local_ip, remote_ip: body.remote_ip, country: body.country, tunnel_ids: [] };
      return created && m === 'POST' ? { topology: created } : {};
    }
  };
  vm.createContext(sandbox);
  vm.runInContext(faSource, sandbox, { filename: 'i18n-fa.js' });
  sandbox.tr = (_k, fb) => fa ? (sandbox.window.FA_TEXT[fb] || fb) : fb;
  vm.runInContext(source, sandbox, { filename: 'topology.js' });
  // `let` bindings are not sandbox properties: read them inside the context
  const wiz = expr => vm.runInContext(expr, sandbox);
  return { sandbox, el, calls, toasts, wiz };
}

test('add topology opens the wizard and never POSTs an empty topology', () => {
  const h = harness();
  h.sandbox.topologyCreate();
  assert.equal(h.el('topoCreateModal').hidden, false, 'the wizard modal opens');
  assert.equal(h.calls.length, 0, 'no topology is created until the wizard finishes');
  assert.equal(h.wiz('topoCreate.step'), 1, 'the wizard starts at the role step');
  assert.ok(h.el('topoCreateBody').innerHTML.includes('data-create-role="iran"'), 'the role step offers Iran');
  assert.ok(h.el('topoCreateBody').innerHTML.includes('data-create-role="foreign"'), 'the role step offers Foreign');
});

test('the circular counter shows 1/6 and fills as steps advance', () => {
  const h = harness();
  h.sandbox.topologyCreate();
  assert.ok(h.el('topoCreateRing').innerHTML.includes('>1/6<'), 'ring reads 1/6 at the start');
  assert.ok(h.el('topoCreateRing').innerHTML.includes('stroke-dasharray='), 'the arc carries a dash array');
  h.sandbox.topoCreateNext();
  assert.ok(h.el('topoCreateRing').innerHTML.includes('>2/6<'), 'ring reads 2/6 on step two');
  assert.ok(h.el('topoCreateDots').innerHTML.includes('topo-create-dot on'), 'the current step dot is highlighted');
  assert.equal(h.el('topoCreateBack').hidden, false, 'Back appears after the first step');
});

test('steps validate before advancing', () => {
  const h = harness();
  h.sandbox.topologyCreate();
  h.sandbox.topoCreateNext(); // role always has a default, so step two opens
  assert.equal(h.wiz('topoCreate.step'), 2);
  h.sandbox.topoCreateNext();
  assert.equal(h.wiz('topoCreate.step'), 2, 'empty this-server IP blocks the step');
  assert.ok(h.el('topoCreateErr').textContent.length > 0, 'an inline error explains the block');
  h.el('topoCreateLocal').value = 'not-an-ip';
  h.sandbox.topoCreateNext();
  assert.equal(h.wiz('topoCreate.step'), 2, 'a non-IP string blocks the step');
  h.el('topoCreateLocal').value = '192.0.2.1';
  h.sandbox.topoCreateNext();
  h.sandbox.topoCreateNext();
  assert.equal(h.wiz('topoCreate.step'), 3, 'empty other-server IP blocks the step');
  h.el('topoCreateRemote').value = '192.0.2.1';
  h.sandbox.topoCreateNext();
  assert.equal(h.wiz('topoCreate.step'), 3, 'identical IPs are rejected');
  assert.ok(h.el('topoCreateErr').textContent.includes('different'), 'the error names the real problem');
  h.el('topoCreateRemote').value = '192.0.2.2';
  h.sandbox.topoCreateNext();
  assert.equal(h.wiz('topoCreate.step'), 4, 'valid distinct IPs advance to the country step');
});

test('closing the wizard keeps the draft and reopening restores it', () => {
  const h = harness();
  h.sandbox.topologyCreate();
  h.sandbox.topoCreateNext();
  h.el('topoCreateLocal').value = '198.51.100.7';
  h.sandbox.topoCreateNext(); // step 3, draft saved on every change
  h.sandbox.topoCreateCancel();
  assert.equal(h.el('topoCreateModal').hidden, true, 'cancel closes the modal');
  assert.ok(h.sandbox.localStorage.getItem('topoCreateDraft'), 'the draft survives the close');
  h.sandbox.topologyCreate();
  assert.equal(h.el('topoCreateModal').hidden, false, 'the wizard reopens');
  assert.equal(h.wiz('topoCreate.step'), 3, 'the draft restores the step');
  assert.equal(h.wiz('topoCreate.values.local'), '198.51.100.7', 'the draft restores the typed IP');
  assert.ok(h.el('topoCreateBody').innerHTML.includes('id="topoCreateRemote"'), 'the restored step renders its own field');
});

test('completing the wizard creates the topology once and clears the draft', async () => {
  const h = harness();
  h.sandbox.topologyCreate();
  h.sandbox.topoCreateNext();
  h.el('topoCreateLocal').value = '192.0.2.1';
  h.sandbox.topoCreateNext();
  h.el('topoCreateRemote').value = '192.0.2.2';
  h.sandbox.topoCreateNext();
  h.el('topoCreateCountry').value = 'DE';
  h.sandbox.topoCreateNext();
  h.el('topoCreateName').value = 'Pair';
  h.sandbox.topoCreateNext();
  assert.equal(h.wiz('topoCreate.step'), 6, 'the review step is reached');
  await h.sandbox.topoCreateNext();
  const post = h.calls.find(c => c.url === '/api/topologies' && c.body);
  assert.ok(post, 'the topology is created exactly at the end');
  assert.deepEqual(JSON.parse(JSON.stringify(post.body)), { name: 'Pair', role: 'iran', local_ip: '192.0.2.1', remote_ip: '192.0.2.2', country: 'de', tunnel_ids: [] });
  assert.equal(h.calls.filter(c => c.url === '/api/topologies' && c.body).length, 1, 'no duplicate creations');
  assert.equal(h.wiz('topologySelected'), 'new1', 'the created topology opens');
  assert.equal(h.el('topoCreateModal').hidden, true, 'the wizard closes after creating');
  assert.equal(h.sandbox.localStorage.getItem('topoCreateDraft'), null, 'the draft is cleared after a successful create');
  assert.ok(h.toasts.some(t => t[0] === 'ok' && t[1] === 'Topology created'), 'creation is confirmed with a toast');
  assert.ok(h.wiz('topoCreate') === null, 'the wizard state is reset');
});

test('a failed create keeps the wizard open with the API error', async () => {
  const h = harness({ failOn: (url, body) => url === '/api/topologies' && !!body });
  h.sandbox.topologyCreate();
  h.sandbox.topoCreateNext();
  h.el('topoCreateLocal').value = '192.0.2.1';
  h.sandbox.topoCreateNext();
  h.el('topoCreateRemote').value = '192.0.2.2';
  h.sandbox.topoCreateNext();
  h.el('topoCreateCountry').value = 'DE';
  h.sandbox.topoCreateNext();
  h.el('topoCreateName').value = 'Pair';
  h.sandbox.topoCreateNext();
  await h.sandbox.topoCreateNext();
  assert.equal(h.el('topoCreateModal').hidden, false, 'the modal stays open');
  assert.ok(h.el('topoCreateErr').textContent.includes('boom'), 'the inline error shows the API message');
  assert.ok(h.sandbox.localStorage.getItem('topoCreateDraft'), 'the draft survives a failed create');
});

test('the review step escapes user input', () => {
  const h = harness();
  h.sandbox.topologyCreate();
  h.sandbox.topoCreateNext();
  h.el('topoCreateLocal').value = '192.0.2.1';
  h.sandbox.topoCreateNext();
  h.el('topoCreateRemote').value = '192.0.2.2';
  h.sandbox.topoCreateNext();
  h.sandbox.topoCreateNext();
  h.el('topoCreateName').value = 'Pair <script>alert(1)</script>';
  h.sandbox.topoCreateNext();
  const body = h.el('topoCreateBody').innerHTML;
  assert.ok(!body.includes('<script>'), 'the review never injects HTML');
  assert.ok(body.includes('&lt;script&gt;'), 'the review renders the escaped name');
});

test('back preserves typed values across steps', () => {
  const h = harness();
  h.sandbox.topologyCreate();
  h.sandbox.topoCreateNext();
  h.el('topoCreateLocal').value = '192.0.2.1';
  h.sandbox.topoCreateNext();
  h.el('topoCreateRemote').value = '192.0.2.2';
  h.sandbox.topoCreateBack();
  assert.equal(h.wiz('topoCreate.step'), 2, 'back returns to the previous step');
  assert.equal(h.wiz('topoCreate.values.remote'), '192.0.2.2', 'the typed remote IP is kept');
  assert.equal(h.wiz('topoCreate.values.local'), '192.0.2.1', 'the typed local IP is kept');
});

test('wizard strings carry unique Persian translations', () => {
  const h = harness({ fa: true });
  const map = h.sandbox.window.FA_TEXT;
  const keys = ['Choose this server\'s role', 'This server is inside Iran; the other server is abroad.',
    'This server is abroad; the other server is inside Iran.', "Enter this server's IP address.",
    "Enter the other server's IP address.", 'Enter a valid IPv4 or IPv6 address.',
    'The two IP addresses must be different.', 'Enter a name for the topology.', 'Create topology',
    'Creating…', 'A topology becomes active once at least one tunnel is added to it.',
    'Add at least one tunnel to activate it.', 'Country', 'Role', 'Review'];
  for (const key of keys) {
    assert.equal(faSource.split(JSON.stringify(key) + ':').length - 1, 1, `unique translation: ${key}`);
    assert.ok(map[key], `Persian entry exists: ${key}`);
    assert.match(map[key], /[\u0600-\u06ff]/);
  }
  h.sandbox.topologyCreate();
  h.sandbox.topoCreateNext();
  assert.ok(h.el('topoCreateBody').innerHTML.includes(map["This server's IP"]), 'the IP step label is translated');
});
