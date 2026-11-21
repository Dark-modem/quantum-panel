const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const test = require('node:test');

const source = fs.readFileSync(path.join(__dirname, 'topology.js'), 'utf8');
const faSource = fs.readFileSync(path.join(__dirname, 'i18n-fa.js'), 'utf8');
const pageSource = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
const cssSource = fs.readFileSync(path.join(__dirname, 'enterprise.css'), 'utf8');
// The foreign bundle flow sends the very request app.js's Setup-tab Connect
// builds: extract the real join builder (plus its IP helpers) instead of
// re-implementing it, so the wizard tests prove the shared code path.
const appSource = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');
const joinCode = appSource.slice(appSource.indexOf('const isIPv4'), appSource.indexOf('// jsArg renders'))
  + '\n' + appSource.slice(appSource.indexOf('function parseBundleClient'), appSource.indexOf('function joinBundleSync'));

function harness({ fa = false, topologies, tunnels, failOn, failCode, failMessage, instances = [], localRole = '', foreignPort = 0 } = {}) {
  const elements = new Map();
  const calls = [], toasts = [], clipboard = [];
  let created = null;
  function el(id) {
    if (!elements.has(id)) elements.set(id, {
      hidden: false, disabled: false, value: id === 'jFRPTransport' ? 'tcp' : id === 'jDial' ? 'auto' : '', textContent: '', innerHTML: '', dataset: {},
      getAttribute() { return null; },
      parentElement: { hidden: false, querySelector() { return null; } },
      classList: { _s: new Set(), toggle(c, v) { if (v === undefined) this._s.has(c) ? this._s.delete(c) : this._s.add(c); else v ? this._s.add(c) : this._s.delete(c); }, add(c) { this._s.add(c); }, remove(c) { this._s.delete(c); }, contains(c) { return this._s.has(c); } },
      contains() { return false; },
      addEventListener(type, cb) { this[type] = cb; }
    });
    return elements.get(id);
  }
  const NEW_BY_URL = {
    '/api/setup': body => ({ id: 'main', name: body.name || 'main', engine: body.engine || 'frp', state: 'healthy' }),
    '/api/sshfwd': body => ({ id: 'bagher-1', name: body.name || 'bagher-1', engine: 'bagher', state: 'healthy' }),
    '/api/matrix/setup': body => ({ id: 'matrix', name: 'Matrix', engine: 'matrix', state: 'healthy', port: body.port || 21007, bundle: 'hmx1_QnVuZGxl', instance: 'matrix' }),
    '/api/matrix/foreign-setup': body => Object.assign({ id: 'matrix', name: 'Matrix', engine: 'matrix', state: 'healthy', status: 'ok', detail: 'matrix node registered with the hub and started' }, foreignPort ? { port: foreignPort } : {}),
    '/api/xray': () => ({ id: 'xray:1', name: 'xr', engine: 'xray', state: 'healthy' })
  };
  const sandbox = {
    // addEventListener/querySelector keep the document-level wiring in
    // topology.js (menus, Escape handling) loadable inside the sandbox.
    window: {}, document: { activeElement: null, hidden: false, querySelectorAll() { return []; }, querySelector() { return null; }, addEventListener() {} },
    $: el, _tunnelDetail: null, setInterval() {}, setTimeout() {}, renderTunnelsList() {},
    copyTextSafe: (...a) => { clipboard.push(a); },
    esc: v => String(v).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;'),
    toastOk: (...a) => toasts.push(['ok', ...a]), toastErr: (...a) => toasts.push(['err', ...a]), toastWarn: (...a) => toasts.push(['warn', ...a]),
    entConfirm: async () => true,
    // LS_ENGINE backs the extracted `const JOIN_ENGINE = LS_ENGINE;` line.
    LS_ENGINE: { frp: 'GRE + FRP', backhaul: 'Backhaul (no GRE)', 'gre-backhaul': 'Backhaul (with GRE)', bagher: 'Bagher' },
    api: async (url, body, method) => {
      const m = method || (body ? 'POST' : 'GET'); // mirrors the real api() defaulting
      calls.push({ url, body, method: m });
      if (failOn && failOn(url, body, m)) {
        const err = new Error(failMessage || 'boom');
        if (failCode) err.code = failCode;
        throw err;
      }
      if (url === '/api/matrix/instances') return { instances };
      if (url === '/api/topologies' && !body) {
        if (created && !tunnels.some(t => t.id === created.id)) tunnels = [...tunnels, created];
        return { topologies, tunnels };
      }
      if (!body && !method) return {}; // a bodyless call is a GET: creates nothing
      const mk = NEW_BY_URL[url];
      if (mk) { created = mk(body || {}); return created; }
      return {};
    }
  };
  // app.js stores the local role snapshot on window (e.g. "foreign (matrix node)").
  if (localRole) sandbox.window._lastLocal = { role: localRole };
  vm.createContext(sandbox);
  vm.runInContext(faSource, sandbox, { filename: 'i18n-fa.js' });
  sandbox.tr = (_k, fb) => fa ? (sandbox.window.FA_TEXT[fb] || fb) : fb;
  vm.runInContext(joinCode, sandbox, { filename: 'app.js [join builder]' });
  vm.runInContext(source, sandbox, { filename: 'topology.js' });
  vm.runInContext(`topologyData = ${JSON.stringify({ topologies, tunnels })};`, sandbox);
  vm.runInContext("topologySelected = " + JSON.stringify(topologies[0] ? topologies[0].id : null) + ";", sandbox);
  // `let` bindings are not sandbox properties: read them inside the context
  const wiz = expr => vm.runInContext(expr, sandbox);
  return { sandbox, el, calls, toasts, clipboard, wiz };
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

test('the wizard footer has no Cancel; Back and Next render on opposite ends', () => {
  // Cancel next to Back/Next was redundant: the header × (topoWizX -> topoWizCancel)
  // stays the only cancel affordance.
  assert.ok(!source.includes('topoWizCancelBtn'), 'topology.js no longer wires a footer Cancel button');
  assert.ok(!pageSource.includes('id="topoWizCancelBtn"'), 'index.html drops the wizard Cancel button');
  const foot = pageSource.match(/<div class="ent-modal-f" id="topoWizFoot">([\s\S]*?)<\/div>/);
  assert.ok(foot, 'the wizard footer keeps its own id');
  const back = foot[1].indexOf('id="topoWizBack"'), next = foot[1].indexOf('id="topoWizNext"');
  assert.ok(back >= 0 && next > back, 'Back renders before Next so space-between pulls them to opposite ends');
  assert.ok(!foot[1].includes('Cancel'), 'the footer holds only Back and Next');
  assert.ok(/#topoWizModal \.ent-modal-f[^}]*justify-content\s*:\s*space-between/.test(cssSource),
    'footer CSS spreads Back and Next to opposite ends (flex mirrors with the dir attribute)');
  assert.ok(/#topoWizModal \.ent-modal-f\.next-only[^}]*justify-content\s*:\s*flex-end/.test(cssSource),
    'with Back hidden the single Next keeps its reading-end spot');
  // The render keeps next-only in sync with Back's visibility.
  const h = harness({ topologies: [topo()], tunnels: [] });
  h.sandbox.topologyNewTunnel();
  h.sandbox.topoWizRender();
  assert.ok(h.el('topoWizFoot').classList._s.has('next-only'), 'step 1 has no Back: the footer goes next-only');
  h.sandbox.topoWizPick('frp');
  assert.ok(!h.el('topoWizFoot').classList._s.has('next-only'), 'from step 2 on Back shows and the footer spreads both actions');
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
    if (type === 'matrix') {
      // Hub side: the wizard stays open on the bundle hand-off screen — the
      // tunnel only works once its bundle is pasted on the foreign server.
      assert.equal(h.el('topoWizModal').hidden, false, 'hub matrix: the wizard does not auto-close');
      assert.equal(h.wiz('topoWiz.step'), 4, 'it moves to the bundle step instead');
      assert.equal(h.el('topoWizBundleOut').value, 'hmx1_QnVuZGxl', 'the setup response bundle is shown');
    } else {
      assert.equal(h.el('topoWizModal').hidden, true, 'modal closes on success');
    }
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
    'Matrix mesh port: {port}',
    'Bundle', 'The Matrix tunnel is ready.', 'Matrix bundle', 'Done', 'Copy bundle', 'Copied ✓',
    'Copy this bundle and paste it on the foreign server, in the new-tunnel wizard under Matrix.',
    'Enter the hub bundle here',
    'Hub bundle', 'Paste the hub bundle from the Iran server.',
    'Matrix tunnel connected to the hub.', 'Local mesh port: {port}',
    'Create', 'Matrix',
    'Everything is detected from the bundle: the tunnel type, the server and the ports.',
    'Copy the bundle from the Iran server and paste it here.', 'Detected: {engine}',
    'Unrecognised bundle: it must start with hmx1_, hsh1_, bh1_, gh1_ or BAGHER:.'];
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
  // The foreign side renders its bundle-only paste screen in Persian too.
  const fh = harness({ fa: true, topologies: [topo()], tunnels: [], localRole: 'foreign' });
  fh.sandbox.topologyNewTunnel();
  assert.ok(fh.el('topoWizBody').innerHTML.includes('باندل هاب را اینجا وارد کنید'), 'the paste label renders in Persian');
  assert.ok(fh.el('topoWizBody').innerHTML.includes('همه‌چیز از باندل تشخیص داده می‌شود'), 'the auto-detect hint renders in Persian');
  assert.ok(fh.el('topoWizSteps').innerHTML.includes('ایجاد'), 'the Create step chip renders in Persian');
  fh.el('topoWizFBundle').value = 'hsh1_192.0.2.10_25000_10.10.10.1_10.10.10.2_tok_443';
  fh.el('topoWizFBundle').oninput();
  assert.equal(fh.el('topoWizBundleChip').textContent, 'تشخیص داده شد: GRE + FRP', 'the detection chip renders with the detected engine');
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

test('hub side: the Matrix wizard ends on a bundle hand-off screen with a copy button', async () => {
  const h = harness({ topologies: [topo()], tunnels: [] });
  h.sandbox.topologyNewTunnel();
  h.sandbox.topoWizPick('matrix');
  assert.equal(h.el('topoWizProgress').textContent, 'Step 2 of 4', 'picking Matrix grows the header to the extra bundle step');
  assert.ok(h.el('topoWizSteps').innerHTML.includes('data-wiz-step="4"'), 'the header lists the Bundle step');
  assert.equal(h.el('topoWizNext').textContent, 'Next', 'the create caption is untouched before creation');
  h.sandbox.topoWizRead();
  h.sandbox.topoWizNext(); // review
  await h.sandbox.topoWizNext(); // create
  assert.equal(h.el('topoWizModal').hidden, false, 'the wizard does not auto-close after hub-side matrix creation');
  assert.equal(h.wiz('topoWiz.step'), 4, 'it sits on the bundle step');
  assert.ok(h.el('topoWizSteps').innerHTML.match(/class="topo-wiz-step done"/g).length === 3, 'the three done steps carry checkmarks');
  assert.equal(h.el('topoWizProgress').textContent, 'Step 4 of 4');
  assert.equal(h.el('topoWizNext').textContent, 'Done', 'the primary footer button becomes «Done»');
  assert.equal(h.el('topoWizBack').hidden, true, 'Back is gone after creation');
  assert.ok(h.el('topoWizFoot').classList._s.has('next-only'), 'the bundle screen keeps only «Done» at the reading end');
  const out = h.el('topoWizBundleOut');
  assert.ok(out, 'a readonly bundle textarea is rendered');
  assert.equal(out.value, 'hmx1_QnVuZGxl', 'it carries the bundle returned by /api/matrix/setup');
  const body = h.el('topoWizBody').innerHTML;
  assert.ok(body.includes('Copy bundle'), 'a copy button is offered');
  assert.ok(body.includes('in the new-tunnel wizard under Matrix'), 'the hint says where to paste the bundle');
  // Copy: the shared clipboard helper is called with the bundle, the button
  // flips to «Copied ✓» and back after 2s.
  h.el('topoWizCopyBundle').onclick();
  assert.equal(h.clipboard.length, 1, 'the copy button calls the clipboard helper');
  assert.equal(h.clipboard[0][0], 'hmx1_QnVuZGxl', 'the helper receives the bundle text');
  assert.equal(h.el('topoWizCopyBundle').textContent, 'Copied ✓');
  // «Done» closes the wizard without another API call.
  const before = h.calls.length;
  h.sandbox.topoWizNext();
  assert.equal(h.el('topoWizModal').hidden, true, '«Done» closes the wizard');
  assert.equal(h.wiz('topoWiz'), null);
  assert.equal(h.calls.length, before, 'closing needs no further API calls');
  // The tunnel was still created and attached on the way to the bundle screen.
  assert.ok(h.calls.find(c => c.method === 'PATCH'), 'the mesh tunnel is attached before the hand-off');
});

// ---- Foreign side: bundle-only flow (every engine) ----

test('foreign side: the wizard becomes bundle-only with Bundle → Review → Create steps', () => {
  const h = harness({ topologies: [topo()], tunnels: [], localRole: 'foreign' });
  h.sandbox.topologyNewTunnel();
  assert.equal(h.wiz('topoWiz.step'), 1);
  const body = h.el('topoWizBody').innerHTML;
  assert.ok(body.includes('id="topoWizFBundle"'), 'step 1 is a large paste field for the bundle');
  assert.ok(body.includes('Everything is detected from the bundle'), 'the hint says everything is auto-detected');
  assert.ok(!body.includes('data-wiz-type='), 'no type cards: the bundle decides the engine');
  assert.equal(h.el('topoWizProgress').textContent, 'Step 1 of 3', 'three steps: Bundle, Review, Create');
  const chips = h.el('topoWizSteps').innerHTML;
  for (const label of ['Bundle', 'Review', 'Create']) assert.ok(chips.includes('>' + label + '<'), label + ' step listed');
  assert.equal(h.el('topoWizBack').hidden, true, 'no Back on the paste step');
  assert.equal(h.el('topoWizNext').disabled, false, 'Next is clickable so validation can explain what is missing');
});

test('foreign side: pasting any bundle shows the detected engine chip without an API call', () => {
  for (const [raw, engine] of [
    ['hmx1_QnVuZGxl', 'Matrix'],
    ['hsh1_192.0.2.10_25000_10.10.10.1_10.10.10.2_tok_443', 'GRE + FRP'],
    ['bh1_192.0.2.10_25000_tcpmux_tok_443,8443', 'Backhaul'],
    ['gh1_192.0.2.10_25000_10.10.10.1_10.10.10.2_tcpmux_tok_443', 'Backhaul (with GRE)'],
    ['BAGHER:{"ports":[443]}', 'Bagher']
  ]) {
    const h = harness({ topologies: [topo()], tunnels: [], localRole: 'foreign' });
    h.sandbox.topologyNewTunnel();
    h.el('topoWizFBundle').value = raw;
    h.el('topoWizFBundle').oninput(); // live paste path
    const chip = h.el('topoWizBundleChip');
    assert.ok(!chip.classList._s.has('hidden'), 'chip visible for ' + raw.slice(0, 5));
    assert.equal(chip.textContent, 'Detected: ' + engine, 'the chip names the engine for ' + raw.slice(0, 5));
    assert.equal(h.el('topoWizErr').textContent, '', 'a valid paste shows no error');
    assert.equal(h.calls.length, 0, 'detection makes zero API calls for ' + raw.slice(0, 5));
  }
});

test('foreign side: an unknown, empty or malformed bundle errors inline with zero API calls', () => {
  const h = harness({ topologies: [topo()], tunnels: [], localRole: 'foreign' });
  h.sandbox.topologyNewTunnel();
  // Live path: unknown prefix.
  h.el('topoWizFBundle').value = 'totally-not-a-bundle';
  h.el('topoWizFBundle').oninput();
  assert.ok(h.el('topoWizErr').textContent.includes('hmx1_'), 'the error names the accepted prefixes');
  assert.ok(h.el('topoWizBundleChip').classList._s.has('hidden'), 'no engine chip for junk');
  // Next-click path: empty paste.
  h.el('topoWizFBundle').value = '   ';
  h.sandbox.topoWizRead();
  h.sandbox.topoWizNext();
  assert.equal(h.wiz('topoWiz.step'), 1, 'stays on the paste step');
  assert.ok(h.el('topoWizErr').textContent.length > 0, 'an inline error explains the empty paste');
  // Valid prefix but malformed payload (parsed by the shared app.js parser).
  h.el('topoWizFBundle').value = 'bh1_notanip';
  h.sandbox.topoWizRead();
  h.sandbox.topoWizNext();
  assert.equal(h.wiz('topoWiz.step'), 1, 'a malformed bundle stays on the paste step');
  assert.ok(h.el('topoWizErr').textContent.length > 0, 'the parse error is shown inline');
  assert.equal(h.calls.length, 0, 'zero API calls for invalid input');
});

test('foreign side: a Matrix bundle pairs via foreign-setup, attaches, and closes', async () => {
  const h = harness({ topologies: [topo()], tunnels: [], localRole: 'foreign (matrix node)' });
  h.sandbox.topologyNewTunnel();
  h.el('topoWizFBundle').value = '  hmx1_QnVuZGxl \n';
  h.el('topoWizFBundle').oninput();
  assert.equal(h.el('topoWizBundleChip').textContent, 'Detected: Matrix', 'the chip detects the Matrix bundle');
  h.sandbox.topoWizNext(); // review
  assert.equal(h.wiz('topoWiz.step'), 2, 'a valid bundle reaches the review');
  assert.ok(h.el('topoWizBody').innerHTML.includes('hmx1_QnVuZGxl'), 'the review shows the pasted bundle');
  await h.sandbox.topoWizNext(); // create
  const call = h.calls.find(c => c.url === '/api/matrix/foreign-setup');
  assert.ok(call, '/api/matrix/foreign-setup is called');
  assert.equal(call.body.bundle, 'hmx1_QnVuZGxl', 'the trimmed bundle is sent');
  assert.equal(h.calls.filter(c => c.url === '/api/matrix/setup').length, 0, 'the hub setup endpoint is never used on the node side');
  assert.equal(h.calls.filter(c => c.url === '/api/setup').length, 0, 'the join endpoint is never used for a Matrix bundle');
  const patch = h.calls.find(c => c.method === 'PATCH');
  assert.ok(patch, 'the paired node tunnel is attached to the topology');
  assert.deepEqual(Array.from(patch.body.tunnel_ids), ['matrix'], 'the matrix inventory id attaches like on the hub');
  assert.ok(h.toasts.some(t => t[0] === 'ok' && String(t[1]).includes('Matrix tunnel connected to the hub')), 'the pairing success toast is shown');
  assert.ok(!h.toasts.some(t => String(t[1]).includes('Local mesh port')), 'no local mesh port is reported when the response carries none');
  assert.equal(h.el('topoWizModal').hidden, true, 'the node side closes without a bundle screen');
  assert.equal(h.wiz('topoWiz'), null);
});

test('foreign side: a shifted local mesh port is appended to the pairing toast', async () => {
  // The node may have had to pick its own local mesh port (the bundle's port
  // is the hub's); the pairing toast must say so in the same message.
  const h = harness({ topologies: [topo()], tunnels: [], localRole: 'foreign (matrix node)', foreignPort: 21001 });
  h.sandbox.topologyNewTunnel();
  h.el('topoWizFBundle').value = 'hmx1_QnVuZGxl';
  h.sandbox.topoWizRead();
  h.sandbox.topoWizNext();
  await h.sandbox.topoWizNext();
  const toast = h.toasts.find(t => t[0] === 'ok' && String(t[1]).includes('Matrix tunnel connected to the hub.'));
  assert.ok(toast, 'the pairing success toast is shown');
  assert.ok(String(toast[1]).includes('Local mesh port: 21001'), 'the toast appends the node-local mesh port, got ' + JSON.stringify(h.toasts));
  assert.ok(!h.toasts.some(t => String(t[1]).includes('Matrix mesh port:')), 'no separate hub-style mesh-port toast on the node side');
});

for (const [raw, kind, endpoint, newId] of [
  ['hsh1_192.0.2.10_25000_10.10.10.1_10.10.10.2_tok_443', 'GRE + FRP', '/api/setup', 'main'],
  ['bh1_192.0.2.10_25000_tcpmux_tok_443,8443', 'Backhaul', '/api/setup', 'main'],
  ['gh1_192.0.2.10_25000_10.10.10.1_10.10.10.2_tcpmux_tok_443', 'Backhaul (with GRE)', '/api/setup', 'main'],
  ['BAGHER:{"ports":[443]}', 'Bagher', '/api/sshfwd', 'bagher-1']
]) {
  test(`foreign side: a ${kind} bundle creates via ${endpoint}, attaches and closes`, async () => {
    const h = harness({ topologies: [topo()], tunnels: [], localRole: 'foreign' });
    h.sandbox.topologyNewTunnel();
    h.el('topoWizFBundle').value = raw;
    h.sandbox.topoWizRead();
    h.sandbox.topoWizNext(); // review
    assert.equal(h.wiz('topoWiz.step'), 2, 'a valid bundle reaches the review');
    const review = h.el('topoWizBody').innerHTML;
    assert.ok(review.includes(kind), 'the review names the detected engine');
    if (endpoint === '/api/setup') {
      assert.ok(review.includes('192.0.2.10'), 'the review shows the Iran server from the parsed bundle');
    } else {
      // Bagher bundles are opaque: the review shows the kind and a preview,
      // and the server validates the payload on apply.
      assert.ok(review.includes(raw.slice(0, 7)), 'the review previews the opaque bundle');
    }
    assert.equal(h.el('topoWizNext').textContent, 'Create the tunnel', 'the review step is the create step');
    await h.sandbox.topoWizNext(); // create (header moves to the Create step)
    const call = h.calls.find(c => c.url === endpoint && c.body);
    assert.ok(call, endpoint + ' called with a body');
    assert.equal(call.method, 'POST');
    const body = call.body;
    if (endpoint === '/api/setup') {
      assert.equal(body.role, 'foreign', 'the join body carries the foreign role');
      assert.equal(body.token, raw, 'the raw bundle rides along as the token');
      assert.equal(body.remote_public, '192.0.2.10', 'the Iran server comes from the bundle');
      assert.equal(body.force, true);
      if (kind === 'GRE + FRP') {
        assert.equal(body.engine, 'frp');
        assert.equal(body.frp_port, 25000);
        assert.equal(body.local_gre, '10.10.10.2', 'the foreign GRE IP comes from the bundle');
        assert.equal(body.peer_gre, '10.10.10.1', 'the Iran GRE IP comes from the bundle');
        assert.equal(body.ports, '443', 'the forwarded ports come from the bundle');
      } else if (kind === 'Backhaul') {
        assert.equal(body.engine, 'backhaul');
        assert.equal(body.ports, '443,8443', 'the forwarded ports come from the bundle');
        assert.equal(body.dial, '', 'plain Backhaul dials without GRE');
      } else {
        assert.equal(body.engine, 'gre-backhaul');
        assert.equal(body.local_gre, '10.10.10.2', 'the foreign GRE IP comes from the bundle');
      }
    } else {
      assert.equal(body.action, 'apply_bundle', 'Bagher joins through the bundle apply action');
      assert.equal(body.bundle, raw, 'the BAGHER: bundle is sent as-is');
      assert.equal(body.name, '', 'the wizard sends no join-form name');
    }
    const patch = h.calls.find(c => c.method === 'PATCH');
    assert.ok(patch, 'the new tunnel is attached to the topology');
    assert.equal(patch.url, '/api/topologies/pair');
    assert.deepEqual(Array.from(patch.body.tunnel_ids), [newId], 'the created tunnel attaches');
    assert.ok(h.toasts.some(t => t[0] === 'ok' && String(t[1]).includes('Tunnel created')), 'success toast shown');
    assert.equal(h.el('topoWizModal').hidden, true, 'the wizard closes on success');
    assert.equal(h.wiz('topoWiz'), null);
  });
}

test('foreign side: the bundle transport wins over the hidden join form default', async () => {
  // The kcp transport rides in the bundle (tr-kcp); the wizard sends it even
  // though the Setup tab's transport select still holds its TCP default.
  const h = harness({ topologies: [topo()], tunnels: [], localRole: 'foreign' });
  h.sandbox.topologyNewTunnel();
  h.el('topoWizFBundle').value = 'hsh1_192.0.2.10_25000_10.10.10.1_10.10.10.2_tok_tr-kcp_443';
  h.sandbox.topoWizRead();
  h.sandbox.topoWizNext();
  await h.sandbox.topoWizNext();
  const call = h.calls.find(c => c.url === '/api/setup' && c.body);
  assert.ok(call, '/api/setup called');
  assert.equal(call.body.frp_transport, 'kcp', 'the FRP transport is detected from the bundle');
  assert.equal(h.el('jFRPTransport').value, 'tcp', 'the hidden join form was never touched');
});

test('foreign side: a failed create drops back to the review with the API error inline', async () => {
  const h = harness({
    topologies: [topo()], tunnels: [], localRole: 'foreign',
    failOn: (url, body) => url === '/api/setup' && !!body
  });
  h.sandbox.topologyNewTunnel();
  h.el('topoWizFBundle').value = 'hsh1_192.0.2.10_25000_10.10.10.1_10.10.10.2_tok_443';
  h.sandbox.topoWizRead();
  h.sandbox.topoWizNext();
  await h.sandbox.topoWizNext();
  assert.equal(h.el('topoWizModal').hidden, false, 'modal stays open');
  assert.equal(h.wiz('topoWiz.step'), 2, 'back on the review step');
  assert.ok(h.el('topoWizErr').textContent.includes('boom'), 'inline error shows the API message');
  const patch = h.calls.find(c => c.method === 'PATCH');
  assert.ok(!patch, 'nothing is attached after a failed create');
});

test('the wizard and the Setup join flow send the identical request for the same bundle', async () => {
  // Both paths call the shared joinRequestBody builder; the wizard adds no
  // drift: with a fresh join form its body is byte-equal to the Connect one.
  for (const raw of [
    'hsh1_192.0.2.10_25000_10.10.10.1_10.10.10.2_tok_443,2083',
    'bh1_192.0.2.10_25000_wssmux+udp_tok_443,8443',
    'gh1_192.0.2.10_25000_10.10.10.1_10.10.10.2_tcpmux_tok_443',
    'BAGHER:{"ports":[443]}'
  ]) {
    const h = harness({ topologies: [topo()], tunnels: [], localRole: 'foreign' });
    h.sandbox.topologyNewTunnel();
    h.el('topoWizFBundle').value = raw;
    h.sandbox.topoWizRead();
    h.sandbox.topoWizNext();
    await h.sandbox.topoWizNext();
    const wizard = h.calls.find(c => (c.url === '/api/setup' || c.url === '/api/sshfwd') && c.body);
    assert.ok(wizard, 'the wizard sent its request for ' + raw.slice(0, 5));
    const join = await h.sandbox.joinRequestBody(raw); // what the Connect button sends
    assert.equal(wizard.url, join.path, 'same endpoint for ' + raw.slice(0, 5));
    assert.deepEqual(wizard.body, join.body, 'same body for ' + raw.slice(0, 5));
    if (raw.startsWith('BAGHER:')) assert.equal(join.body.action, 'apply_bundle', 'Bagher joins through apply_bundle on both paths');
  }
});
