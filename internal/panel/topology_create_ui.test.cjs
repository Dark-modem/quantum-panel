const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const test = require('node:test');

const source = fs.readFileSync(path.join(__dirname, 'topology.js'), 'utf8');
const faSource = fs.readFileSync(path.join(__dirname, 'i18n-fa.js'), 'utf8');
const pageSource = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
const cssSource = fs.readFileSync(path.join(__dirname, 'enterprise.css'), 'utf8');

// enrollment fixtures: two iran agents across two datacenters and one
// foreign agent — the DC lists the wizard shows are this operator data
const AGENTS = [
  { id: 'agt_a1', name: 'tehran-1', role: 'iran', linked: true, server: { id: 1, name: 'tehran-1', datacenter: 'Tehran-Azadi', datacenter_en: 'Tehran-Azadi' } },
  { id: 'agt_a2', name: 'tehran-2', role: 'iran', linked: true, server: { id: 2, name: 'tehran-2', datacenter: 'Tehran-Azadi', datacenter_en: 'Tehran-Azadi' } },
  { id: 'agt_b1', name: 'shiraz-1', role: 'iran', linked: true, server: { id: 3, name: 'shiraz-1', datacenter: 'Shiraz-Hafez', datacenter_en: 'Shiraz-Hafez' } },
  { id: 'agt_f1', name: 'frankfurt-1', role: 'foreign', linked: true, country: 'de', server: { id: 6, name: 'frankfurt-1', datacenter: 'Hetzner', datacenter_en: 'Hetzner' } }
];
const SERVERS = [
  { id: 1, address: '10.0.1.12' }, { id: 2, address: '10.0.1.11' },
  { id: 3, address: '10.0.2.11' }, { id: 6, address: '192.0.2.11' }
];

function harness({ fa = false, topologies = [], tunnels = [], failOn } = {}) {
  const elements = new Map();
  const calls = [], toasts = [];
  let created = null;
  function el(id) {
    if (!elements.has(id)) elements.set(id, {
      hidden: false, disabled: false, value: '', textContent: '', innerHTML: '', dataset: {},
      _attrs: {}, setAttribute(k, v) { this._attrs[k] = String(v); }, getAttribute(k) { return k in this._attrs ? this._attrs[k] : null; },
      parentElement: { hidden: false, querySelector() { return null; } },
      classList: { _s: new Set(), toggle(c, v) { if (v === undefined) this._s.has(c) ? this._s.delete(c) : this._s.add(c); else v ? this._s.add(c) : this._s.delete(c); }, add(c) { this._s.add(c); }, remove(c) { this._s.delete(c); }, contains(c) { return this._s.has(c); } },
      contains() { return false; },
      addEventListener(type, cb) { this[type] = cb; }
    });
    return elements.get(id);
  }
  const sandbox = {
    window: {}, document: { activeElement: null, hidden: false, querySelectorAll() { return []; }, querySelector() { return null; }, addEventListener() {}, createElement() { return el('created'); } },
    $: el, _tunnelDetail: null, setInterval() {}, renderTunnelsList() {},
    skel: n => '<div class="ent-skel"></div>'.repeat(n || 3),
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
      if (url === '/api/ui/agents' && !body) return { agents: AGENTS };
      if (url === '/api/mesh/servers' && !body) return { servers: SERVERS };
      if (url === '/api/topologies' && body) created = { id: 'new1', name: body.name, role: body.role, local_ip: body.local_ip, remote_ip: body.remote_ip, country: body.country, tunnel_ids: [] };
      return created && m === 'POST' ? { topology: created } : {};
    }
  };
  vm.createContext(sandbox);
  vm.runInContext(faSource, sandbox, { filename: 'i18n-fa.js' });
  sandbox.tr = (_k, fb) => fa ? (sandbox.window.FA_TEXT[fb] || fb) : fb;
  vm.runInContext(source, sandbox, { filename: 'topology.js' });
  const wiz = expr => vm.runInContext(expr, sandbox);
  return { sandbox, el, calls, toasts, wiz };
}

// open + let the agent/server pools land (a microtask after both GETs)
async function openWizard(h) {
  h.sandbox.topologyCreate();
  await new Promise(r => setTimeout(r, 0));
}

test('add topology opens the DC wizard and never POSTs an empty topology', async () => {
  const h = harness();
  h.sandbox.topologyCreate();
  assert.equal(h.el('topoCreateModal').hidden, false, 'the wizard modal opens');
  h.el('topoCreateName').value = 'main-mesh'; // step 1 requires a name before it advances
  // opening the wizard pre-fetches the agent/server pools (two GETs) — what
  // must never happen is a topology creation before the wizard finishes
  assert.equal(h.calls.filter(c => c.url === '/api/topologies' && c.method !== 'GET').length, 0, 'no topology is created until the wizard finishes');
  assert.equal(h.wiz('topoCreate.step'), 1, 'the wizard starts at the name step');
  await openWizard(h);
  h.sandbox.topoCreateNext(); // step 2
  const body = h.el('topoCreateBody').innerHTML;
  assert.ok(body.includes('data-createdc="Tehran-Azadi"'), 'the Iran DC list comes from the enrollment data');
  assert.ok(body.includes('data-createdc="Shiraz-Hafez"'), 'every enrolled Iran DC is offered');
  assert.ok(!body.includes('data-createdc="Hetzner"'), 'foreign DCs stay off the Iran step');
  assert.equal(h.calls.filter(c => c.url === '/api/topologies' && c.body).length, 0, 'still no creation');
});

test('the circular counter shows 1/3 and fills as steps advance', () => {
  const h = harness();
  h.sandbox.topologyCreate();
  assert.ok(h.el('topoCreateRing').innerHTML.includes('>1/3<'), 'ring reads 1/3 at the start');
  assert.ok(h.el('topoCreateRing').innerHTML.includes('stroke-dasharray='), 'the arc carries a dash array');
  h.sandbox.topoCreateNext(); // invalid: empty name blocks
  assert.ok(h.el('topoCreateRing').innerHTML.includes('>1/3<'), 'an empty name blocks the step');
  h.el('topoCreateName').value = 'main-mesh';
  h.sandbox.topoCreateNext();
  assert.ok(h.el('topoCreateRing').innerHTML.includes('>2/3<'), 'ring reads 2/3 on the Iran DC step');
  assert.ok(h.el('topoCreateDots').innerHTML.includes('topo-create-dot on'), 'the current step dot is highlighted');
  assert.equal(h.el('topoCreateBack').hidden, false, 'Back appears after the first step');
});

test('the name step blocks an empty and a non-slug name', () => {
  const h = harness();
  h.sandbox.topologyCreate();
  h.sandbox.topoCreateNext();
  assert.equal(h.wiz('topoCreate.step'), 1, 'an empty name blocks the step');
  assert.ok(h.toasts.some(t => t[0] === 'err' && t.includes('Enter a name for the topology.')), 'the block is announced as a toast');
  h.el('topoCreateName').value = 'DE-NODE';
  h.sandbox.topoCreateNext();
  assert.equal(h.wiz('topoCreate.step'), 2, 'uppercase names are accepted — DE-NODE is a valid topology name');
  h.sandbox.topoCreateBack();
  h.el('topoCreateName').value = 'main-mesh';
  h.sandbox.topoCreateNext();
  assert.equal(h.wiz('topoCreate.step'), 2, 'a normal name advances');
});

test('the Iran step needs a DC and at least one of its agents', async () => {
  const h = harness();
  await openWizard(h);
  h.el('topoCreateName').value = 'main-mesh';
  h.sandbox.topoCreateNext();
  h.sandbox.topoCreateNext(); // no DC picked yet
  assert.equal(h.wiz('topoCreate.step'), 2, 'a missing DC blocks the step');
  h.wiz('topoCreate.values.dcIran = "Tehran-Azadi"; topoCreateRender();');
  const ags = h.wiz('topoCreateAgentsOf("iran", "Tehran-Azadi").map(a => a.id)');
  assert.deepEqual(ags, ['agt_a1', 'agt_a2'], 'the DC hosts exactly its own agents');
  h.sandbox.topoCreateNext();
  assert.equal(h.wiz('topoCreate.step'), 2, 'zero picked agents blocks the step');
  h.wiz('topoCreate.values.agentsIran = ["agt_a1"];');
  h.sandbox.topoCreateNext();
  assert.equal(h.wiz('topoCreate.step'), 3, 'one picked agent advances to the foreign step');
});

test('completing the wizard creates the topology and adds the chosen agents', async () => {
  const h = harness();
  await openWizard(h);
  h.el('topoCreateName').value = 'main-mesh';
  h.sandbox.topoCreateNext();
  h.wiz('topoCreate.values.dcIran = "Tehran-Azadi"; topoCreate.values.agentsIran = ["agt_a1", "agt_a2"];');
  h.sandbox.topoCreateNext();
  h.wiz('topoCreate.values.dcForeign = "Hetzner"; topoCreate.values.agentsForeign = ["agt_f1"];');
  assert.equal(h.wiz('topoCreate.step'), 3, 'the review-free flow reaches Create on step 3');
  // the final next IS the create: finish is async (create + member-adds), await it
  await h.sandbox.topoCreateNext();
  const post = h.calls.find(c => c.url === '/api/topologies' && c.body);
  assert.ok(post, 'the topology is created exactly at the end');
  assert.equal(post.body.name, 'main-mesh');
  assert.equal(post.body.local_ip, '10.0.1.12', 'the first Iran agent resolves the local address');
  assert.equal(post.body.remote_ip, '192.0.2.11', 'the first foreign agent resolves the remote address');
  const adds = h.calls.filter(c => c.url.includes('/members/add') && c.method === 'POST');
  assert.equal(adds.length, 3, 'every picked agent joins through the member-add route');
  assert.deepEqual(adds.map(c => c.body.agent_id), ['agt_a1', 'agt_a2', 'agt_f1']);
  assert.equal(h.wiz('topologySelected'), 'new1', 'the created topology opens');
  assert.equal(h.el('topoCreateModal').hidden, true, 'the wizard closes after creating');
  assert.ok(h.toasts.some(t => t[0] === 'ok' && t[1] === 'Topology created'), 'creation is confirmed with a toast');
  assert.ok(h.wiz('topoCreate') === null, 'the wizard state is reset');
});

test('a failed create keeps the wizard open with the API error', async () => {
  const h = harness({ failOn: (url, body) => url === '/api/topologies' && !!body });
  await openWizard(h);
  h.el('topoCreateName').value = 'main-mesh';
  h.sandbox.topoCreateNext();
  h.wiz('topoCreate.values.dcIran = "Tehran-Azadi"; topoCreate.values.agentsIran = ["agt_a1"];');
  h.sandbox.topoCreateNext();
  h.wiz('topoCreate.values.dcForeign = "Hetzner"; topoCreate.values.agentsForeign = ["agt_f1"];');
  // the final next IS the create: finish is async, await it (a second next
  // would just no-op against the busy gate)
  await h.sandbox.topoCreateNext();
  assert.equal(h.el('topoCreateModal').hidden, false, 'the modal stays open');
  assert.ok(h.toasts.some(t => t[0] === 'err' && t.join(' ').includes('boom')), 'the API failure is announced as a toast');
  assert.equal(h.el('topoCreateErr').textContent, '', 'the inline alert strip stays empty');
});

test('back preserves the DC and agent picks across steps', async () => {
  const h = harness();
  await openWizard(h);
  h.el('topoCreateName').value = 'main-mesh';
  h.sandbox.topoCreateNext();
  h.wiz('topoCreate.values.dcIran = "Tehran-Azadi"; topoCreate.values.agentsIran = ["agt_a1"];');
  h.sandbox.topoCreateNext();
  h.wiz('topoCreate.values.dcForeign = "Hetzner"; topoCreate.values.agentsForeign = ["agt_f1"];');
  h.sandbox.topoCreateBack();
  assert.equal(h.wiz('topoCreate.step'), 2, 'back returns to the Iran step');
  assert.equal(h.wiz('topoCreate.values.dcIran'), 'Tehran-Azadi', 'the picked Iran DC is kept');
  // Array.from bridges the VM realm so deepEqual compares host arrays
  assert.deepEqual(Array.from(h.wiz('topoCreate.values.agentsIran')), ['agt_a1'], 'the picked agents are kept');
  assert.deepEqual(Array.from(h.wiz('topoCreate.values.agentsForeign')), ['agt_f1'], 'the foreign picks are kept too');
});

test('wizard strings carry unique Persian translations', () => {
  const h = harness({ fa: true });
  const map = h.sandbox.window.FA_TEXT;
  const keys = ['Topology name', 'Iran datacenter', 'Foreign datacenter',
    'A name for this topology — it names the tunnels too.',
    'Choose the Iran datacenter', 'Choose the foreign datacenter',
    'Pick the agents joining from this datacenter', 'No agents in this datacenter yet.',
    'No agents enrolled yet — add one first.', 'Enter a name for the topology.',
    'Creating…', 'Topology created',
    'Pick at least one agent from this datacenter', 'members added'];
  for (const key of keys) {
    assert.equal(faSource.split(JSON.stringify(key) + ':').length - 1, 1, `unique translation: ${key}`);
    assert.ok(map[key], `Persian entry exists: ${key}`);
    assert.match(map[key], /[\u0600-\u06ff]/);
  }
  h.sandbox.topologyCreate();
  h.el('topoCreateName').value = 'main-mesh';
  h.sandbox.topoCreateNext();
  assert.ok(h.el('topoCreateBody').innerHTML.includes(map['Choose the Iran datacenter']), 'the DC step hint is translated');
});

test('the wizard footer has no Cancel; Back and Next render on opposite ends', () => {
  assert.ok(!source.includes('topoCreateCancelBtn'), 'topology.js no longer wires a footer Cancel button');
  assert.ok(!pageSource.includes('id="topoCreateCancelBtn"'), 'index.html drops the create-wizard Cancel button');
  const foot = pageSource.match(/<div class="ent-modal-f" id="topoCreateFoot">([\s\S]*?)<\/div>/);
  assert.ok(foot, 'the create wizard footer keeps its own id');
  assert.ok(foot[1].indexOf('id="topoCreateBack"') >= 0 && foot[1].indexOf('id="topoCreateBack"') < foot[1].indexOf('id="topoCreateNext"'),
    'Back renders before Next so space-between pulls them to opposite ends');
  assert.ok(!foot[1].includes('Cancel'), 'the footer holds only Back and Next');
  assert.ok(/#topoCreateModal \.ent-modal-f[^}]*justify-content\s*:\s*space-between/.test(cssSource),
    'footer CSS spreads Back and Next to opposite ends (flex mirrors with the dir attribute)');
  const h = harness();
  h.sandbox.topologyCreate();
  h.sandbox.topoCreateRender();
  assert.ok(h.el('topoCreateFoot').classList._s.has('next-only'), 'step 1 has no Back: the footer goes next-only');
  h.el('topoCreateName').value = 'main-mesh'; // topoCreateRead reads the input, not the state
  h.sandbox.topoCreateNext();
  assert.ok(!h.el('topoCreateFoot').classList._s.has('next-only'), 'from step 2 on the footer spreads Back and Next');
});
