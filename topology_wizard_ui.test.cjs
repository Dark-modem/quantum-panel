const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const test = require('node:test');

const source = fs.readFileSync(path.join(__dirname, 'topology.js'), 'utf8');
const faSource = fs.readFileSync(path.join(__dirname, 'i18n-fa.js'), 'utf8');

function harness({ fa = false, topologies, tunnels, failOn, failCode, failMessage, instances = [] } = {}) {
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
  const NEW_BY_URL = {
    '/api/setup': body => ({ id: 'main', name: body.name || 'main', engine: body.engine || 'frp', state: 'healthy' }),
    '/api/sshfwd': body => ({ id: 'bagher-1', name: body.name || 'bagher-1', engine: 'bagher', state: 'healthy' }),
    '/api/matrix/setup': body => ({ id: 'matrix', name: 'Matrix', engine: 'matrix', state: 'healthy', port: body.port || 21007 }),
    '/api/xray': () => ({ id: 'xray:1', name: 'xr', engine: 'xray', state: 'healthy' })
  };
  const sandbox = {
    window: {}, document: { activeElement: null, hidden: false, querySelectorAll() { return []; } },
    $: el, _tunnelDetail: null, setInterval() {}, renderTunnelsList() {},
    esc: v => String(v).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;'),
    toastOk: (...a) => toasts.push(['ok', ...a]), toastErr: (...a) => toasts.push(['err', ...a]), toastWarn: (...a) => toasts.push(['warn', ...a]),
    entConfirm: async () => true,
    api: async (url, body, method) => {
      calls.push({ url, body, method: method || 'POST' });
      if (failOn && failOn(url, body, method)) {
        const err = new Error(failMessage || 'boom');
        if (failCode) err.code = failCode;
        throw err;
      }
      if (url === '/api/matrix/instances') return { instances };
      if (url === '/api/topologies' && !body) {
        if (created && !tunnels.some(t => t.id === created.id)) tunnels = [...tunnels, created];
        return { topologies, tunnels };
      }
      const mk = (!method || method === 'POST') && NEW_BY_URL[url];
      if (mk) { created = mk(body || {}); return created; }
      return {};
    }
  };
  vm.createContext(sandbox);
  vm.runInContext(faSource, sandbox, { filename: 'i18n-fa.js' });
  sandbox.tr = (_k, fb) => fa ? (sandbox.window.FA_TEXT[fb] || fb) : fb;
  vm.runInContext(source, sandbox, { filename: 'topology.js' });
  vm.runInContext(`topologyData = ${JSON.stringify({ topologies, tunnels })};`, sandbox);
  vm.runInContext("topologySelected = " + JSON.stringify(topologies[0] ? topologies[0].id : null) + ";", sandbox);
  // `let` bindings are not sandbox properties: read them inside the context
  const wiz = expr => vm.runInContext(expr, sandbox);
  return { sandbox, el, calls, toasts, wiz };
}
function topo(over = {}) {
  return Object.assign({ id: 'pair', name: 'Pair', role: 'iran', local_ip: '192.0.2.1', remote_ip: '192.0.2.2', tunnel_ids: [] }, over);
}

test('topologyNewTunnel opens the wizard modal instead of navigating to Setup', () => {
  const h = harness({ topologies: [topo()], tunnels: [] });
  h.sandbox.topologyNewTunnel();
  assert.equal(h.el('topoWizModal').hidden, false, 'wizard modal opens in place');
  assert.equal(h.wiz('topoWiz.step'), 1);
  const body = h.el('topoWizBody').innerHTML;
  for (const type of ['frp', 'backhaul', 'bagher', 'matrix', 'xray']) {
    assert.ok(body.includes(`data-wiz-type="${type}"`), `type card ${type} rendered`);
  }
  assert.ok(!source.includes("goTab('setup')"), 'topology.js no longer jumps to the Setup tab');
  // no topology or missing IPs: no wizard, only the old warning toast
  const empty = harness({ topologies: [], tunnels: [] });
  empty.sandbox.topologyNewTunnel();
  assert.equal(empty.wiz('topoWiz'), null, 'wizard stays closed without a topology');
  const half = harness({ topologies: [topo({ local_ip: '' })], tunnels: [] });
  half.sandbox.topologyNewTunnel();
  assert.equal(half.wiz('topoWiz'), null, 'wizard stays closed until both IPs are set');
});

test('header shows step progress and completed steps get a checkmark', () => {
  const h = harness({ topologies: [topo()], tunnels: [] });
  h.sandbox.topologyNewTunnel();
  h.sandbox.topoWizRender();
  assert.equal(h.el('topoWizProgress').textContent, 'Step 1 of 3');
  assert.ok(h.el('topoWizSteps').innerHTML.includes('data-wiz-step="1"'));
  h.sandbox.topoWizPick('frp'); // -> step 2
  assert.equal(h.el('topoWizProgress').textContent, 'Step 2 of 3');
  assert.ok(h.el('topoWizSteps').innerHTML.includes('class="topo-wiz-step done"'), 'step 1 marked done');
  assert.equal(h.el('topoWizBack').hidden, false, 'Back appears from step 2 on');
  h.sandbox.topoWizRead();
  h.sandbox.topoWizNext(); // -> step 3
  assert.equal(h.el('topoWizProgress').textContent, 'Step 3 of 3');
  assert.ok(h.el('topoWizNext').textContent.includes('Create'), 'Create is the step-3 action');
  const done = h.el('topoWizSteps').innerHTML.match(/class="topo-wiz-step done"/g) || [];
  assert.equal(done.length, 2, 'two finished steps carry the checkmark styling');
});

test('type selection changes the step-2 fields', () => {
  const h = harness({ topologies: [topo()], tunnels: [] });
  h.sandbox.topologyNewTunnel();
  h.sandbox.topoWizPick('frp');
  const frp = h.el('topoWizBody').innerHTML;
  assert.ok(frp.includes('id="topoWizFTransport"'), 'frp shows the transport select');
  assert.ok(!frp.includes('topoWizFGreOn'), 'frp has no GRE toggle');
  h.sandbox.topoWizBack();
  h.sandbox.topoWizPick('backhaul');
  assert.ok(h.el('topoWizBody').innerHTML.includes('topoWizFGreOn'), 'backhaul shows the GRE toggle');
  h.sandbox.topoWizBack();
  h.sandbox.topoWizPick('bagher');
  const bagher = h.el('topoWizBody').innerHTML;
  assert.ok(bagher.includes('id="topoWizFSshPort"') && bagher.includes('id="topoWizFPool"'), 'bagher shows SSH port and pool');
  h.sandbox.topoWizBack();
  h.sandbox.topoWizPick('matrix');
  assert.ok(h.el('topoWizBody').innerHTML.includes('id="topoWizFFamily"'), 'matrix shows the family select');
  assert.ok(!h.el('topoWizBody').innerHTML.includes('id="topoWizFRemote"'), 'matrix needs no remote IP');
  h.sandbox.topoWizBack();
  h.sandbox.topoWizPick('xray');
  assert.ok(h.el('topoWizBody').innerHTML.includes('id="topoWizFMode"'), 'xray shows direct/reverse mode');
});

test('Back keeps entered values and Cancel closes the modal', () => {
  const h = harness({ topologies: [topo()], tunnels: [] });
  h.sandbox.topologyNewTunnel();
  h.sandbox.topoWizPick('frp');
  h.el('topoWizFRemote').value = '198.51.100.9';
  h.sandbox.topoWizRead();
  h.sandbox.topoWizNext(); // review
  h.sandbox.topoWizBack(); // back to settings
  assert.equal(h.wiz('topoWiz.step'), 2);
  assert.equal(h.wiz('topoWiz.values.remote'), '198.51.100.9', 'Back does not lose the typed value');
  assert.ok(h.el('topoWizBody').innerHTML.includes('value="198.51.100.9"'), 're-rendered field restores the value');
  h.sandbox.topoWizCancel();
  assert.equal(h.el('topoWizModal').hidden, true, 'Cancel closes the modal');
  assert.equal(h.wiz('topoWiz'), null);
});

test('validation blocks an empty remote before the review step', () => {
  const h = harness({ topologies: [topo()], tunnels: [] });
  h.sandbox.topologyNewTunnel();
  h.sandbox.topoWizPick('bagher');
  h.sandbox.topoWizNext(); // no ports typed
  assert.equal(h.wiz('topoWiz.step'), 2, 'stays on settings');
  assert.ok(h.el('topoWizErr').textContent.length > 0, 'inline error explains what is missing');
  h.el('topoWizFPorts').value = '443, 8443';
  h.sandbox.topoWizRead();
  h.sandbox.topoWizNext();
  assert.equal(h.wiz('topoWiz.step'), 3, 'valid settings reach the review');
});

for (const [type, expect] of [
  ['frp', ['/api/setup', 'frp']],
  ['backhaul', ['/api/setup', 'gre-backhaul']],
  ['bagher', ['/api/sshfwd', 'bagher']],
  ['matrix', ['/api/matrix/setup', 'ipv4']],
  ['xray', ['/api/xray', 'create']]
]) {
  test(`creating a ${type} tunnel calls the Setup-tab endpoint and attaches it to the topology`, async () => {
    const tunnels = [{ id: 'peer:2', name: 'old', engine: 'frp', state: 'healthy' }];
    // peer:2 is assigned to another topology, so it cannot be picked as "the new tunnel"
    const h = harness({ topologies: [topo({ tunnel_ids: [] }), { id: 'other', name: 'Other', role: 'iran', local_ip: '192.0.2.3', remote_ip: '192.0.2.4', tunnel_ids: ['peer:2'] }], tunnels });
    const NEW_ID = { frp: 'main', backhaul: 'main', bagher: 'bagher-1', matrix: 'matrix', xray: 'xray:1' }[type];
    h.sandbox.topologyNewTunnel();
    h.sandbox.topoWizPick(type);
    h.el('topoWizFRemote').value = '198.51.100.9';
    h.el('topoWizFPorts').value = '443';
    h.el('topoWizFName').value = 'NewLink';
    h.sandbox.topoWizRead();
    h.sandbox.topoWizNext();
    const review = h.el('topoWizBody').innerHTML;
    assert.ok(review.includes('Pair'), 'review shows the topology name');
    if (type === 'matrix') {
      assert.ok(review.includes('Automatic'), 'blank mesh port: the review shows it is picked automatically');
    } else {
      assert.ok(review.includes('198.51.100.9'), 'review shows the remote endpoint');
    }
    await h.sandbox.topoWizNext(); // Create
    const setup = h.calls.find(c => c.url === expect[0]);
    assert.ok(setup, `${expect[0]} called`);
    assert.equal(setup.method, 'POST');
    const body = setup.body;
    if (type === 'frp' || type === 'backhaul') { assert.equal(body.engine, expect[1]); assert.equal(body.role, 'iran'); }
    if (type === 'bagher') assert.equal(body.mode, 'bagher');
    if (type === 'matrix') {
      assert.equal(body.family, 'ipv4');
      assert.equal(body.port, 0, 'a blank mesh port is sent as 0 (auto)');
      assert.equal(body.instance, undefined, 'no existing instances: legacy body without the instance field');
    }
    if (type === 'xray') assert.equal(body.action, 'create');
    const patch = h.calls.find(c => c.method === 'PATCH');
    assert.ok(patch, 'new tunnel is attached via tunnel_ids PATCH');
    assert.equal(patch.url, '/api/topologies/pair');
    assert.deepEqual(Array.from(patch.body.tunnel_ids), [NEW_ID], 'attach payload carries the new tunnel');
    assert.ok(h.toasts.some(t => t[0] === 'ok'), 'success toast shown');
    assert.equal(h.el('topoWizModal').hidden, true, 'modal closes on success');
  });
}

test('a failed create keeps the modal open and surfaces the error inline', async () => {
  const h = harness({ topologies: [topo()], tunnels: [], failOn: url => url === '/api/setup' });
  h.sandbox.topologyNewTunnel();
  h.sandbox.topoWizPick('frp');
  h.sandbox.topoWizRead();
  h.sandbox.topoWizNext();
  await h.sandbox.topoWizNext();
  assert.equal(h.el('topoWizModal').hidden, false, 'modal stays open');
  assert.ok(h.el('topoWizErr').textContent.includes('boom'), 'inline error shows the API message');
});

test('the Matrix mesh port is labelled, hinted, and optional (auto pick)', () => {
  const h = harness({ topologies: [topo()], tunnels: [] });
  h.sandbox.topologyNewTunnel();
  h.sandbox.topoWizPick('matrix');
  const body = h.el('topoWizBody').innerHTML;
  assert.ok(body.includes('Mesh port'), 'the field is labelled Mesh port, not just Port');
  assert.ok(body.includes('A free TCP+UDP port for the tinc mesh'), 'a hint says the mesh port is not the traffic port');
  assert.ok(body.includes('Leave empty/0 to pick automatically'), 'the hint tells how to auto-pick');
  assert.ok(!body.includes('>2097<'), 'no 2097 default is forced into the field');
  assert.ok(body.includes('value=""'), 'the mesh port starts blank');
  h.sandbox.topoWizRead();
  h.sandbox.topoWizNext();
  assert.equal(h.wiz('topoWiz.step'), 3, 'a blank mesh port passes validation');
  assert.ok(h.el('topoWizBody').innerHTML.includes('Automatic'), 'the review announces the automatic pick');
});

test('wizard sends the typed mesh port, and 0 when the field is blank', async () => {
  const typed = harness({ topologies: [topo()], tunnels: [] });
  typed.sandbox.topologyNewTunnel();
  typed.sandbox.topoWizPick('matrix');
  typed.el('topoWizFPort').value = '4430';
  typed.sandbox.topoWizRead();
  typed.sandbox.topoWizNext();
  await typed.sandbox.topoWizNext();
  const call = typed.calls.find(c => c.url === '/api/matrix/setup');
  assert.ok(call, 'matrix setup called');
  assert.equal(call.body.port, 4430, 'the typed mesh port is sent as-is');

  const blank = harness({ topologies: [topo()], tunnels: [] });
  blank.sandbox.topologyNewTunnel();
  blank.sandbox.topoWizPick('matrix');
  blank.sandbox.topoWizRead();
  blank.sandbox.topoWizNext();
  await blank.sandbox.topoWizNext();
  const auto = blank.calls.find(c => c.url === '/api/matrix/setup');
  assert.ok(auto, 'matrix setup called');
  assert.equal(auto.body.port, 0, 'a blank mesh port is sent as 0 so the server picks a free one');
});

test('success reports the mesh port the server chose', async () => {
  const h = harness({ topologies: [topo()], tunnels: [] });
  h.sandbox.topologyNewTunnel();
  h.sandbox.topoWizPick('matrix');
  h.sandbox.topoWizRead();
  h.sandbox.topoWizNext();
  await h.sandbox.topoWizNext();
  const toast = h.toasts.find(t => t[0] === 'ok' && String(t[1]).includes('Matrix mesh port: 21007'));
  assert.ok(toast, 'a success toast names the chosen mesh port, got ' + JSON.stringify(h.toasts));
  // A typed port is echoed back, not replaced by the auto fallback.
  const typed = harness({ topologies: [topo()], tunnels: [] });
  typed.sandbox.topologyNewTunnel();
  typed.sandbox.topoWizPick('matrix');
  typed.el('topoWizFPort').value = '4430';
  typed.sandbox.topoWizRead();
  typed.sandbox.topoWizNext();
  await typed.sandbox.topoWizNext();
  assert.ok(typed.toasts.some(t => t[0] === 'ok' && String(t[1]).includes('Matrix mesh port: 4430')), 'the returned port is shown');
});

test('a mesh-port conflict surfaces the API error as-is, unwrapped', async () => {
  const h = harness({
    topologies: [topo()], tunnels: [],
    failOn: url => url === '/api/matrix/setup',
    failCode: 'E-MATRIX-10',
    failMessage: '[E-MATRIX-10] TCP port 443 is occupied'
  });
  h.sandbox.topologyNewTunnel();
  h.sandbox.topoWizPick('matrix');
  h.sandbox.topoWizRead();
  h.sandbox.topoWizNext();
  await h.sandbox.topoWizNext();
  assert.equal(h.el('topoWizModal').hidden, false, 'modal stays open');
  assert.ok(h.el('topoWizErr').textContent.includes('TCP port 443 is occupied'), 'inline error shows the API message');
  const toast = h.toasts.find(t => t[0] === 'err');
  assert.ok(toast, 'error toast shown');
  assert.equal(toast.length, 2, 'the self-explanatory message is the whole toast, not wrapped in a generic title');
  assert.ok(String(toast[1]).includes('TCP port 443 is occupied'), 'the toast carries the raw API message');
});

test('wizard strings carry unique Persian translations', () => {
  const h = harness({ fa: true, topologies: [topo()], tunnels: [] });
  const map = h.sandbox.window.FA_TEXT;
  const keys = ['Step {x} of {n}', 'Choose the tunnel type', 'Tunnel name', 'Local public IP',
    'Remote public IP', 'GRE under the tunnel', 'FRP bind port', 'Pool size', 'Enter a port number.',
    'The new tunnel is attached to this topology.', 'Mesh tunnel (tinc): the servers join one virtual network.',
    "This server's IPv6 address", 'Stealth tunnel over an SSH connection pool.', 'Type',
    'Mesh port', 'A free TCP+UDP port for the tinc mesh — NOT the traffic port. Leave empty/0 to pick automatically.',
    'Matrix mesh port: {port}'];
  for (const key of keys) {
    assert.equal(faSource.split(JSON.stringify(key) + ':').length - 1, 1, `unique translation: ${key}`);
    assert.ok(map[key], `Persian entry exists: ${key}`);
    assert.match(map[key], /[\u0600-\u06ff]/);
  }
  h.sandbox.topologyNewTunnel();
  h.sandbox.topoWizRender();
  assert.equal(h.el('topoWizProgress').textContent, map['Step {x} of {n}'].replace('{x}', '1').replace('{n}', '3'));
  h.sandbox.topoWizPick('matrix');
  assert.ok(h.el('topoWizBody').innerHTML.includes('پورت مش'), 'the mesh-port label renders in Persian');
});

test('with existing Matrix instances the wizard requests a fresh isolated one', async () => {
  const h = harness({ topologies: [topo()], tunnels: [], instances: [{ id: 'matrix', name: 'Matrix', engine: 'matrix', state: 'healthy' }] });
  h.sandbox.topologyNewTunnel();
  h.sandbox.topoWizPick('matrix');
  h.sandbox.topoWizRead();
  h.sandbox.topoWizNext();
  await h.sandbox.topoWizNext();
  const get = h.calls.find(c => c.url === '/api/matrix/instances');
  assert.ok(get, 'instances are listed before submitting');
  const setupCall = h.calls.find(c => c.url === '/api/matrix/setup');
  assert.ok(setupCall, 'matrix setup called');
  assert.equal(setupCall.body.instance, 'next', 'existing instances: the new tunnel gets its own instance');
  const gi = h.calls.findIndex(c => c.url === '/api/matrix/instances');
  const si = h.calls.findIndex(c => c.url === '/api/matrix/setup');
  assert.ok(gi < si, 'the instances check happens before the setup call');
  const patch = h.calls.find(c => c.method === 'PATCH');
  assert.ok(patch, 'the new instance tunnel is attached to the topology');
});
