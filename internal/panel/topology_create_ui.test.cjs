const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const test = require('node:test');

const source = fs.readFileSync(path.join(__dirname, 'topology.js'), 'utf8');
const faSource = fs.readFileSync(path.join(__dirname, 'i18n-fa.js'), 'utf8');
const pageSource = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
const cssSource = fs.readFileSync(path.join(__dirname, 'enterprise.css'), 'utf8');

// the §2.21.1 data the wizard builds its steps from: the REAL datacenter
// rows (GET /api/mesh/datacenters) and the agents whose linked servers sit
// in them (matched by datacenter_id from GET /api/mesh/servers)
const DATACENTERS = [
  { id: 1, kind: 'iran', name: 'Tehran-Azadi', datacenter_en: 'Tehran-Azadi' },
  { id: 2, kind: 'iran', name: 'Shiraz-Hafez', datacenter_en: 'Shiraz-Hafez' },
  { id: 3, kind: 'foreign', name: 'Hetzner', datacenter_en: 'Hetzner' }
];
const AGENTS = [
  { id: 'agt_a1', name: 'tehran-1', role: 'iran', status: 'online', linked: true, server: { id: 1, name: 'tehran-1', datacenter: 'Tehran-Azadi', datacenter_en: 'Tehran-Azadi' } },
  { id: 'agt_a2', name: 'tehran-2', role: 'iran', status: 'offline', linked: true, server: { id: 2, name: 'tehran-2', datacenter: 'Tehran-Azadi', datacenter_en: 'Tehran-Azadi' } },
  { id: 'agt_b1', name: 'shiraz-1', role: 'iran', status: 'online', linked: true, server: { id: 3, name: 'shiraz-1', datacenter: 'Shiraz-Hafez', datacenter_en: 'Shiraz-Hafez' } },
  { id: 'agt_f1', name: 'frankfurt-1', role: 'foreign', status: 'online', linked: true, country: 'de', server: { id: 6, name: 'frankfurt-1', datacenter: 'Hetzner', datacenter_en: 'Hetzner' } }
];
const SERVERS = [
  { id: 1, address: '10.0.1.12', datacenter_id: 1 }, { id: 2, address: '10.0.1.11', datacenter_id: 1 },
  { id: 3, address: '10.0.2.11', datacenter_id: 2 }, { id: 6, address: '192.0.2.11', datacenter_id: 3 }
];

// api() attaches the §1 packet to thrown errors (code/ui/status) — the
// failOn hook builds the same shape
function apiError(code, message, ui) {
  const e = new Error(message);
  e.code = code; e.ui = ui || null; e.status = ui && ui.http_status;
  return e;
}

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
    $: el, _tunnelDetail: null, setInterval() {}, renderTunnelsList() {}, apiError,
    skel: n => '<div class="ent-skel"></div>'.repeat(n || 3),
    esc: v => String(v).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;'),
    toastOk: (...a) => toasts.push(['ok', ...a]), toastErr: (...a) => toasts.push(['err', ...a]), toastWarn: (...a) => toasts.push(['warn', ...a]),
    entConfirm: async () => true,
    api: async (url, body, method) => {
      const m = method || (body ? 'POST' : 'GET');
      calls.push({ url, body, method: m });
      if (failOn && failOn(url, body, m)) throw failOn(url, body, m);
      if (url === '/api/topologies' && !body) {
        if (created && !topologies.some(t => t.id === created.id)) topologies = [...topologies, created];
        return { topologies, tunnels };
      }
      if (url === '/api/ui/agents' && !body) return { agents: AGENTS };
      if (url === '/api/mesh/servers' && !body) return { servers: SERVERS };
      if (url === '/api/mesh/datacenters' && !body) return { datacenters: DATACENTERS };
      if (url === '/api/mesh/topologies/dc' && body) {
        const iran = SERVERS.filter(s => s.datacenter_id === body.iran.datacenter_id);
        const foreign = SERVERS.filter(s => s.datacenter_id === body.foreign.datacenter_id);
        return { status: 'ok', topology_id: 42, pairs: iran.length * foreign.length,
          tunnels: body.iran.agent_ids.flatMap(aid => foreign.map((f, i) => ({ tunnel_key: 'tun-x-' + aid + '-' + f.id + '-a', engine: 'matrix', enabled: true, engine_params: '{}' }))),
          pushes: [...body.iran.agent_ids, ...body.foreign.agent_ids].map(aid => ({ agent_id: aid, status: 'ok' })),
          warnings: [] };
      }
      if (url === '/api/topologies' && body) created = { id: 'new1', name: body.name, role: body.role, local_ip: body.local_ip, remote_ip: body.remote_ip, country: body.country, tunnel_ids: [] };
      return created && m === 'POST' ? { topology: created } : {};
    }
  };
  vm.createContext(sandbox);
  if (fa) sandbox.currentLang = 'fa'; // production defines it globally (app.js)
  vm.runInContext(faSource, sandbox, { filename: 'i18n-fa.js' });
  sandbox.tr = (_k, fb) => fa ? (sandbox.window.FA_TEXT[fb] || fb) : fb;
  vm.runInContext(source, sandbox, { filename: 'topology.js' });
  const wiz = expr => vm.runInContext(expr, sandbox);
  return { sandbox, el, calls, toasts, wiz };
}

// open + let the agent/server/DC pools land (a microtask after the three GETs)
async function openWizard(h) {
  h.sandbox.topologyCreate();
  await new Promise(r => setTimeout(r, 0));
}

test('add topology opens the DC wizard and never creates a topology before the last step', async () => {
  const h = harness();
  h.sandbox.topologyCreate();
  assert.equal(h.el('topoCreateModal').hidden, false, 'the wizard modal opens');
  h.el('topoCreateName').value = 'main-mesh'; // step 1 requires a name before it advances
  // opening the wizard pre-fetches the agent/server/DC pools (three GETs) —
  // what must never happen is a creation before the wizard finishes
  assert.equal(h.calls.filter(c => c.url === '/api/mesh/topologies/dc').length, 0, 'no §2.21.2 create until the wizard finishes');
  assert.equal(h.calls.filter(c => c.url === '/api/topologies' && c.method !== 'GET').length, 0, 'no display row until the wizard finishes');
  assert.equal(h.wiz('topoCreate.step'), 1, 'the wizard starts at the name step');
  await openWizard(h);
  h.sandbox.topoCreateNext(); // step 2
  const body = h.el('topoCreateBody').innerHTML;
  assert.ok(body.includes('data-createdc="1"'), 'the Iran DC list is the real datacenter rows (ids, not agent-derived names)');
  assert.ok(body.includes('Shiraz-Hafez'), 'every enrolled Iran DC is offered');
  assert.ok(!body.includes('data-createdc="3"'), 'foreign DCs stay off the Iran step');
  assert.equal(h.calls.filter(c => c.url === '/api/mesh/topologies/dc').length, 0, 'still no creation');
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

test('the name step slugifies live: DE-NODE becomes de-node, junk cleans to nothing', () => {
  const h = harness();
  h.sandbox.topologyCreate();
  h.sandbox.topoCreateNext();
  assert.equal(h.wiz('topoCreate.step'), 1, 'an empty name blocks the step');
  assert.ok(h.toasts.some(t => t[0] === 'err' && t.includes('Enter a name for the topology.')), 'the empty block is announced as a toast');
  h.el('topoCreateName').value = 'DE-NODE';
  h.sandbox.topoCreateNext();
  assert.equal(h.wiz('topoCreate.values.name'), 'de-node', 'uppercase input slugifies — the endpoint only takes [a-z0-9-_]');
  assert.equal(h.wiz('topoCreate.step'), 2, 'the cleaned name advances');
  h.sandbox.topoCreateBack();
  h.el('topoCreateName').value = '!!!';
  h.sandbox.topoCreateNext();
  assert.equal(h.wiz('topoCreate.step'), 1, 'a name that cleans to nothing blocks the step');
  assert.ok(h.toasts.some(t => t[0] === 'err' && t.join(' ').includes('1-64 characters')), 'the slug-rule block names the rule');
  h.el('topoCreateName').value = 'main-mesh';
  h.sandbox.topoCreateNext();
  assert.equal(h.wiz('topoCreate.step'), 2, 'a normal name advances');
});

test('the Iran step lists real datacenter rows and its own agents, online highlighted', async () => {
  const h = harness();
  await openWizard(h);
  h.el('topoCreateName').value = 'main-mesh';
  h.sandbox.topoCreateNext();
  h.sandbox.topoCreateNext(); // no DC picked yet
  assert.equal(h.wiz('topoCreate.step'), 2, 'a missing DC blocks the step');
  h.wiz('topoCreate.values.dcIran = 1; topoCreateRender();');
  const ags = h.wiz('topoCreateAgentsOf("iran", 1).map(a => a.id)');
  assert.deepEqual(ags, ['agt_a1', 'agt_a2'], 'the DC hosts exactly the agents whose linked server sits in it (datacenter_id match)');
  const body = h.el('topoCreateBody').innerHTML;
  assert.ok(body.includes('topo-create-st'), 'every agent card carries the online/offline chip');
  assert.ok(body.includes('badge ok'), 'online agents wear the ok badge');
  assert.ok(body.includes('badge dim'), 'offline agents wear the dim badge');
  h.sandbox.topoCreateNext();
  assert.equal(h.wiz('topoCreate.step'), 2, 'zero picked agents blocks the step');
  h.wiz('topoCreate.values.agentsIran = ["agt_a1"];');
  h.sandbox.topoCreateNext();
  assert.equal(h.wiz('topoCreate.step'), 3, 'one picked agent advances to the foreign step');
});

test('a datacenter without eligible agents says so instead of listing strangers', async () => {
  const h = harness();
  await openWizard(h);
  h.el('topoCreateName').value = 'main-mesh';
  h.sandbox.topoCreateNext();
  h.wiz('topoCreate.values.dcIran = 1; topoCreate.values.agentsIran = ["agt_a1"]; topoCreateRender();');
  h.sandbox.topoCreateNext(); // foreign step
  // a foreign DC where no agent's server sits (kind matches, members do not)
  h.sandbox.window.FA_TEXT; // no-op: keep the realm alive
  const foreignCards = h.wiz('topoCreateDCs("foreign").map(d => d.dc.id)');
  assert.deepEqual(foreignCards, [3], 'the foreign side lists its own kind only');
  h.wiz('topoCreate.values.dcForeign = 3; topoCreateRender();');
  assert.ok(h.el('topoCreateBody').innerHTML.includes('frankfurt-1'), 'the picked foreign DC lists its agent');
});

test('completing the wizard makes ONE §2.21.2 call and records the display row', async () => {
  const h = harness();
  await openWizard(h);
  h.el('topoCreateName').value = 'main-mesh';
  h.sandbox.topoCreateNext();
  h.wiz('topoCreate.values.dcIran = 1; topoCreate.values.agentsIran = ["agt_a1", "agt_a2"];');
  h.sandbox.topoCreateNext();
  h.wiz('topoCreate.values.dcForeign = 3; topoCreate.values.agentsForeign = ["agt_f1"];');
  assert.equal(h.wiz('topoCreate.step'), 3, 'the review-free flow reaches Create on step 3');
  // the final next IS the create: finish is async (§2.21.2 call + anchor row), await it
  await h.sandbox.topoCreateNext();
  const dc = h.calls.find(c => c.url === '/api/mesh/topologies/dc');
  assert.ok(dc, 'the §2.21.2 create fires exactly at the end');
  assert.deepEqual(JSON.parse(JSON.stringify({ name: dc.body.name, iran: dc.body.iran, foreign: dc.body.foreign })), {
    name: 'main-mesh',
    iran: { datacenter_id: 1, agent_ids: ['agt_a1', 'agt_a2'] },
    foreign: { datacenter_id: 3, agent_ids: ['agt_f1'] }
  }, 'the body carries the two DCs and the agent subsets (engine/tpp/push stay the server defaults)');
  const post = h.calls.find(c => c.url === '/api/topologies' && c.body);
  assert.ok(post, 'the display anchor row is recorded after the create succeeds');
  assert.equal(post.body.name, 'main-mesh');
  assert.equal(post.body.local_ip, '10.0.1.12', 'the first Iran agent resolves the local address');
  assert.equal(post.body.remote_ip, '192.0.2.11', 'the first foreign agent resolves the remote address');
  assert.equal(h.calls.filter(c => c.url.includes('/members/add')).length, 0, 'no per-agent member-adds: the one call carries the membership');
  assert.equal(h.wiz('topologySelected'), 'new1', 'the created topology opens');
  assert.equal(h.el('topoCreateModal').hidden, true, 'the wizard closes after creating');
  assert.ok(h.toasts.some(t => t[0] === 'ok' && t[1] === 'Topology created'), 'creation is confirmed with a toast');
  assert.ok(h.wiz('topoCreate') === null, 'the wizard state is reset');
});

test('a §2.21.2 failure keeps the wizard open and renders the structured packet', async () => {
  const h = harness({
    failOn: (url) => url === '/api/mesh/topologies/dc'
      ? apiError('E-TOPOLOGY-DC-CONFLICT', '[E-TOPOLOGY-DC-CONFLICT] One datacenter per side', {
        http_status: 409,
        code: 'E-TOPOLOGY-DC-CONFLICT',
        title: { en: 'One datacenter per side', fa: 'یک دیتاسنتر برای هر طرف' },
        details: { side: 'iran', datacenters: ['Tehran-Azadi', 'Hetzner'], agents: ['agt_a1'] }
      })
      : null
  });
  await openWizard(h);
  h.el('topoCreateName').value = 'main-mesh';
  h.sandbox.topoCreateNext();
  h.wiz('topoCreate.values.dcIran = 1; topoCreate.values.agentsIran = ["agt_a1"];');
  h.sandbox.topoCreateNext();
  h.wiz('topoCreate.values.dcForeign = 3; topoCreate.values.agentsForeign = ["agt_f1"];');
  await h.sandbox.topoCreateNext();
  assert.equal(h.el('topoCreateModal').hidden, false, 'the modal stays open');
  const strip = h.el('topoCreateErr').innerHTML;
  assert.ok(strip.includes('One datacenter per side'), 'the packet title renders in the inline strip');
  assert.ok(strip.includes('The Iran side spans several datacenters (Tehran-Azadi، Hetzner)'), 'the conflicting side and DCs render with details');
  assert.ok(strip.includes('Offending agents: tehran-1.'), 'the offending agents render, resolved to names through the pool');
  assert.ok(h.toasts.some(t => t[0] === 'err' && t.includes('One datacenter per side')), 'the failure is announced as a toast too');
  assert.equal(h.calls.filter(c => c.url === '/api/topologies' && c.body).length, 0, 'a failed create records no display row');
});

test('a per-agent validation 400 names the field and the offending agent', async () => {
  const h = harness({
    failOn: (url) => url === '/api/mesh/topologies/dc'
      ? apiError('E-VALIDATION', '[E-VALIDATION] Validation error', {
        http_status: 400,
        code: 'E-VALIDATION',
        title: { en: 'Validation error', fa: 'خطای اعتبارسنجی' },
        details: { field: 'foreign.agent_ids', agent: 'agt_x9', agent_name: 'probe edge', reason: 'agent has no linked server' }
      })
      : null
  });
  await openWizard(h);
  h.el('topoCreateName').value = 'main-mesh';
  h.sandbox.topoCreateNext();
  h.wiz('topoCreate.values.dcIran = 1; topoCreate.values.agentsIran = ["agt_a1"];');
  h.sandbox.topoCreateNext();
  h.wiz('topoCreate.values.dcForeign = 3; topoCreate.values.agentsForeign = ["agt_f1"];');
  await h.sandbox.topoCreateNext();
  const strip = h.el('topoCreateErr').innerHTML;
  assert.ok(strip.includes('Foreign agents'), 'the offending field renders translated');
  assert.ok(strip.includes('agent has no linked server'), 'the reason renders');
  assert.ok(strip.includes('probe edge'), 'the offending agent is named');
  assert.equal(h.el('topoCreateModal').hidden, false, 'the modal stays open');
});

test('back preserves the DC ids and agent picks across steps', async () => {
  const h = harness();
  await openWizard(h);
  h.el('topoCreateName').value = 'main-mesh';
  h.sandbox.topoCreateNext();
  h.wiz('topoCreate.values.dcIran = 1; topoCreate.values.agentsIran = ["agt_a1"];');
  h.sandbox.topoCreateNext();
  h.wiz('topoCreate.values.dcForeign = 3; topoCreate.values.agentsForeign = ["agt_f1"];');
  h.sandbox.topoCreateBack();
  assert.equal(h.wiz('topoCreate.step'), 2, 'back returns to the Iran step');
  assert.equal(h.wiz('topoCreate.values.dcIran'), 1, 'the picked Iran DC id is kept');
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
    'Pick at least one agent from this datacenter',
    'Only lowercase a-z, digits, - and _ — up to 64 characters.',
    'The name must be 1-64 characters of a-z, 0-9, - and _.',
    'No Iran datacenter is defined yet — add one under Datacenters first.',
    'No foreign datacenter is defined yet — add one under Datacenters first.',
    'The engine is Matrix with 1 tunnel per pair; the desired state pushes to every selected agent right after creation.',
    'The {side} side spans several datacenters ({dcs}) — a topology takes exactly one datacenter per side.',
    'Offending agents: {agents}.',
    'One datacenter per side', 'Validation error', 'Storage error', 'Request',
    'Tunnels per pair', 'Iran agents', 'Foreign agents',
    'pushed', 'Some pushes failed', 'Creation notes',
    'The topology list could not record the new entry — the topology itself exists.'];
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

test('the §1 error view speaks Persian in FA mode — side, DCs and agents included', () => {
  const h = harness({ fa: true });
  const view = h.wiz(`topoCreateErrView(apiError('E-TOPOLOGY-DC-CONFLICT', '[E-TOPOLOGY-DC-CONFLICT] x', {
    title: { en: 'One datacenter per side', fa: 'یک دیتاسنتر برای هر طرف' },
    details: { side: 'iran', datacenters: ['تهران-آزادی', 'هتزنر'], agents: ['agt_a1'] }
  }))`);
  assert.equal(view.title, 'یک دیتاسنتر برای هر طرف', 'the packet\'s own Persian title wins in FA mode');
  assert.ok(view.detail.includes('سمت ایران'), 'the side renders in Persian');
  assert.ok(view.detail.includes('تهران-آزادی، هتزنر'), 'the conflicting datacenters ride through');
  assert.ok(view.detail.includes('ایجنت‌های متضارب'), 'the offending-agents line renders in Persian');
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
