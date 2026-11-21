'use strict';
// VM tests for the C-Tunnel UI trio (contract §2.22 additive fields +
// §2.23 method board + the kernel-tuning card): the core details modal
// (49-method table, version strip, outdated badge), the live board page
// (pair gating, run=1 request, live rows, best/deploy card) and the kernel
// table normalizer. No server or browser required.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const dir = __dirname;
const source = fs.readFileSync(path.join(dir, 'app.js'), 'utf8').replace(new RegExp('\\r\\n', 'g'), '\n');
const indexHtml = fs.readFileSync(path.join(dir, 'index.html'), 'utf8');
function section(start, end) {
  const a = source.indexOf(start), b = end ? source.indexOf(end, a + start.length) : source.length;
  assert.ok(a >= 0 && b > a, 'source anchors: ' + start);
  return source.slice(a, b);
}
const consts = section('const CORES_ENGINE_IMG', 'async function loadCores(force)');
const cmp = section('function verCmp(a, b)', 'function artSize');
const ctun = section('// ===== Core details modal (§2.22 additive fields)', '(function coresWire()');
const board = section('// ---------- C-Tunnel method board (tab-cboard, §2.23)', '// ---------- Kernel Tuning (tab-netopt)');
const kernel = section('// ---------- Kernel Tuning (tab-netopt)', '// ---------- Bagher (SSH pool carrier)');
const elements = new Map();
function el(id) {
  if (!elements.has(id)) elements.set(id, { id, hidden: false, disabled: false, value: '', files: null,
    textContent: '', innerHTML: '', title: '',
    classList: { _on: new Set(), add(c) { this._on.add(c); }, remove(c) { this._on.delete(c); }, toggle(c, on) { if (on) this._on.add(c); else this._on.delete(c); }, contains(c) { return this._on.has(c); } },
    setAttribute() {}, getAttribute() { return null; }, addEventListener() {}, focus() {},
    querySelectorAll() { return []; }, querySelector() { return null; }, appendChild() {} });
  return elements.get(id);
}
let toasts = [], apiCalls = [], apiHandler = null;
const sandbox = {
  assert, console, JSON, Math, Date, String, Number, isNaN, encodeURIComponent, Promise, setTimeout, clearTimeout,
  document: {
    activeElement: null, body: { appendChild() {} },
    getElementById: id => el(id),
    querySelector() { return null; },
    querySelectorAll() { return []; },
    createElement() { return el('created'); },
    addEventListener() {},
  },
  location: { hash: '' },
  window: { __coresAgents: [], __engKeyCfg: true },
  $: id => el(id), el,
  esc: s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])),
  tr: (_k, fb) => fb,
  currentLang: 'en',
  BASE: '', at: p => p,
  api: async (p, b, m) => { apiCalls.push({ path: p, body: b, method: m || 'GET' }); if (!apiHandler) throw new Error('no api handler'); return apiHandler(p, b, m); },
  skel: n => '<div class="skel"></div>'.repeat(n || 1),
  stateError: (msg, actionKey) => '<div class="ent-empty ent-error" data-state-action="' + (actionKey || '') + '">' + esc(msg) + '</div>',
  modalShow() {}, modalHide(ov) { if (ov) ov.hidden = true; },
  entConfirm: async () => true,
  toastOk: (t, s) => toasts.push(['ok', t, s]),
  toastErr: (t, s) => toasts.push(['err', t, s]),
  toastWarn: (t, s) => toasts.push(['warn', t, s]),
  topologyFlagSvg: () => '<svg class="flag"></svg>',
  fmtSpeedRate: v => Math.round(v / 1024) + ' KB/s',
  loadCores() {}, renderCores() {}, loadArtStore() {}, loadCoresAvailable() {},
};
function esc(s) { return String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])); }
vm.createContext(sandbox);
vm.runInContext(consts + '\n' + cmp + '\n' + ctun + '\n' + board + '\n' + kernel, sandbox);
const sleep = ms => new Promise(r => setTimeout(r, ms));

// the static markup must carry the three surfaces and the goTab list must
// make the two new tabs reachable
assert.ok(indexHtml.includes('data-tab="cboard"') && indexHtml.includes('id="tab-cboard"'), 'the method board tab exists in the markup');
assert.ok(indexHtml.includes('id="ktBody"') && indexHtml.includes('id="ktRefresh"'), 'the kernel tuning card exists in the markup');
const goTabSrc = source.slice(source.indexOf('function goTab'), source.indexOf('function tabHashWrite'));
assert.ok(/['"]cboard['"]/.test(goTabSrc), 'goTab reaches the method board tab');
assert.ok(/['"]netopt['"]/.test(goTabSrc), 'goTab reaches the NetOpt tab');
// fmtSpeedRate was referenced but never defined (a latent pair-benchmark bug)
assert.ok(/function fmtSpeedRate\(/.test(source), 'fmtSpeedRate is defined');

// 49 fixture methods: 16 L2 + 15 L3 + 18 L4, mixed nulls and an evil row
function mkMethods() {
  const rows = [];
  for (let i = 0; i < 49; i++) {
    const layer = i < 16 ? 'L2' : i < 31 ? 'L3' : 'L4';
    rows.push({
      id: 'm' + (i + 1), name: 'meth-' + (i + 1), title: 'Method ' + (i + 1),
      layer, transport: ['udp', 'tcp', 'icmp', 'raw-ip'][i % 4], ipver: ['v4', 'v6', 'both'][i % 3],
      overhead: i % 5 === 0 ? null : i % 7 + '%', needs_port: i % 2 === 0, userspace: i % 3 === 0,
      mikrotik: i % 4 === 0 ? null : i % 4 === 1, family: i % 6 === 0 ? null : 'fam-' + (i % 3),
    });
  }
  rows[7].title = '<script>';
  return rows;
}

(async () => {
  // ---- Piece 1: the core details modal --------------------------------------
  vm.runInContext(`
  coresCache = [{ engine: 'ctunnel', latest: '0.3.2', builds: [], installed: {}, detail: { a1: { installed: true, version: '0.3.2' }, a2: { installed: true, version: '0.3.1' } } }];
  coresAvail = { cores: [{ core: 'ctunnel', latest_tag: 'ctunnel-v0.4.0', latest_version: '0.4.0', installed_version: '0.3.2', outdated: true, methods: null }] };
  ctunModalOpen('ctunnel');
  `, sandbox);
  let html = el('ctunBody').innerHTML;
  assert.ok(el('ctunMTitle').textContent.includes('C-Tunnel'), 'the modal titles the core display name');
  assert.ok(el('ctunMTitle').textContent.includes('Method catalog'), 'the modal names the method catalog');
  assert.ok(html.includes('v0.3.2') && html.includes('v0.4.0'), 'the version strip shows installed vs repo latest');
  assert.ok(html.includes('Newer version available'), 'outdated=true carries the newer-version badge');
  vm.runInContext('globalThis.mkMethods = ' + mkMethods.toString(), sandbox);
  vm.runInContext(`
  coresAvail.cores[0].methods = mkMethods();
  renderCtunModal();
  `, sandbox);
  html = el('ctunBody').innerHTML;
  assert.ok((html.match(/ctun-lb-/g) || []).length === 49, 'the full 49-method table renders');
  assert.ok(html.includes('All') && html.includes('L2') && html.includes('L3') && html.includes('L4'), 'layer filter chips render');
  assert.ok(html.includes('16') && html.includes('15') && html.includes('18'), 'the chips carry their per-layer counts');
  assert.ok(html.includes('Overhead') && html.includes('Port needed') && html.includes('Userspace') && html.includes('MikroTik') && html.includes('Family'), 'the table carries every dispatched column');
  assert.ok(html.includes('&lt;script&gt;') && !html.includes('<script>'), 'the method title renders escaped');
  assert.ok(html.includes('49 transport methods'), 'the count line names the 49 methods');

  vm.runInContext(`ctunLayer = 'L3'; renderCtunModal();`, sandbox);
  html = el('ctunBody').innerHTML;
  assert.ok((html.match(/ctun-lb-/g) || []).length === 15, 'the L3 filter keeps only its 15 rows');

  // null fields render —
  vm.runInContext(`
  ctunLayer = 'all';
  coresAvail.cores[0].methods = [{ id: 'x1', name: 'x1', title: 'Null row', layer: 'L4', transport: null, ipver: null, overhead: null, needs_port: null, userspace: null, mikrotik: null, family: null }];
  renderCtunModal();
  `, sandbox);
  html = el('ctunBody').innerHTML;
  assert.ok((html.match(/art-dash/g) || []).length === 5, 'every null field renders the em dash');

  // outdated=false → up to date; absent field + older installed → fallback badge; equal → nothing
  vm.runInContext(`
  coresAvail.cores[0].methods = mkMethods();
  coresAvail.cores[0].outdated = false; coresAvail.cores[0].installed_version = '0.4.0';
  renderCtunModal();
  `, sandbox);
  html = el('ctunBody').innerHTML;
  assert.ok(html.includes('Up to date') && !html.includes('Newer version available'), 'outdated=false shows the up-to-date mark');
  vm.runInContext(`
  delete coresAvail.cores[0].outdated; coresAvail.cores[0].installed_version = '0.3.2';
  renderCtunModal();
  `, sandbox);
  html = el('ctunBody').innerHTML;
  assert.ok(html.includes('Newer version available'), 'the fallback comparison flags an older installed version');
  vm.runInContext(`
  coresAvail.cores[0].installed_version = '0.4.0'; renderCtunModal();
  `, sandbox);
  html = el('ctunBody').innerHTML;
  assert.ok(!html.includes('Newer version available') && html.includes('Up to date'), 'equal versions with no field read up to date via the fallback comparison');
  // installed_version absent → the newest agent-installed version stands in
  vm.runInContext(`
  delete coresAvail.cores[0].installed_version; coresAvail.cores[0].outdated = undefined;
  coresCache[0].detail = { a1: { installed: true, version: '0.3.1' }, a2: { installed: true, version: '0.3.2' } };
  renderCtunModal();
  `, sandbox);
  html = el('ctunBody').innerHTML;
  assert.ok(html.includes('v0.3.2'), 'the computed installed version picks the newest agent one');
  // no methods on the feed → the honest empty state
  vm.runInContext(`
  coresCache[0].detail = {};
  delete coresAvail.cores[0].methods; renderCtunModal();
  `, sandbox);
  html = el('ctunBody').innerHTML;
  assert.ok(html.includes('No method catalog yet'), 'a missing catalog renders its honest empty state');

  // ---- Piece 2: the live method board ---------------------------------------
  vm.runInContext(`
  ctbAgents = [
    { id: 'ag-ir1', name: 'tehran-1', role: 'iran', status: 'online', arch: 'amd64', country: 'IR' },
    { id: 'ag-ir2', name: 'tehran-2', role: 'iran', status: 'offline', arch: 'amd64', country: 'IR' },
    { id: 'ag-fa1', name: 'frankfurt-1', role: 'foreign', status: 'online', arch: 'arm64', country: 'DE' },
  ];
  ctbAgentsErr = null; ctbAgentsLoading = false;
  ctbState = { iran: '', kharej: '', running: false, view: null, timer: null, err: null };
  ctbRender();
  `, sandbox);
  html = el('ctunBoard').innerHTML;
  assert.ok(html.includes('tehran-1') && html.includes('frankfurt-1'), 'both sides list their agents');
  assert.ok(html.includes('Online') && html.includes('Offline'), 'agent cards carry their online state');
  assert.ok(/id="ctbRunBtn"[^>]*disabled/.test(html), 'the run button is disabled until a full pair is picked');
  vm.runInContext(`ctbState.iran = 'ag-ir1'; ctbState.kharej = 'ag-fa1'; ctbRender();`, sandbox);
  html = el('ctunBoard').innerHTML;
  assert.ok(!/id="ctbRunBtn"[^>]*disabled/.test(html), 'a full pair enables the run button');

  // the pick handler: cards select, a second click on a picked card deselects
  vm.runInContext(`ctbState = { iran: '', kharej: '', running: false, view: null, timer: null, err: null };`, sandbox);
  vm.runInContext(`ctbPick('iran', 'ag-ir2')`, sandbox);
  assert.equal(vm.runInContext('ctbState.iran', sandbox), 'ag-ir2', 'picking a new iran card switches the iran side');
  vm.runInContext(`ctbPick('iran', 'ag-ir2')`, sandbox);
  assert.equal(vm.runInContext('ctbState.iran', sandbox), '', 'clicking a picked card toggles it off');
  vm.runInContext(`ctbPick('kharej', 'ag-fa1'); ctbPick('iran', 'ag-ir1');`, sandbox);
  assert.equal(vm.runInContext('ctbState.kharej', sandbox), 'ag-fa1', 'the kharej side picks independently');

  // run=1 request assembly + the running state, then the poll converges
  let resolveRun = null;
  const boardPayload = { running: false, results: [
    { method: 'udp-hammer', layer: 'L4', transport: 'udp', ipver: 'both', reachability: 'pass', latency_ms: 41.5, throughput: 1048576, pmtu: 1420, score: 87 },
    { method: 'icmp-slip', layer: 'L3', transport: 'icmp', ipver: 'v4', reachability: 'blocked', error: 'icmp filtered upstream' },
    { method: 'tcp-weave', layer: 'L4', transport: 'tcp', ipver: null, reachability: null, latency_ms: null, throughput: null, pmtu: null, score: null },
  ], best: { method: 'udp-hammer', layer: 'L4', transport: 'udp', ipver: 'both', score: 87, latency_ms: 41.5, throughput: 1048576 }, pmtu: 1420 };
  apiHandler = (p) => {
    if (p.includes('run=1')) return new Promise(res => { resolveRun = res; });
    return boardPayload;
  };
  const runP = vm.runInContext('ctbRun()', sandbox);
  await sleep(10);
  assert.equal(apiCalls[0].path, '/api/benchmark/method-board?iran=ag-ir1&kharej=ag-fa1&run=1', 'the run fires the §2.23 endpoint with the picked pair and run=1');
  html = el('ctunBoard').innerHTML;
  assert.ok(html.includes('Probing every method'), 'the running state names itself while the run is in flight');
  resolveRun({ running: true, phase: 'probe', cur_method: 'udp-hammer' });
  await runP;
  await sleep(20);
  assert.equal(apiCalls[1].path, '/api/benchmark/method-board?iran=ag-ir1&kharej=ag-fa1', 'the poll re-reads the board without run=1');
  html = el('ctunBoard').innerHTML;
  assert.ok(html.includes('udp-hammer') && html.includes('icmp-slip') && html.includes('tcp-weave'), 'all probed methods render as rows');
  assert.ok(html.includes('Reachable') && html.includes('Blocked'), 'reachability renders its ok/err badges');
  assert.ok(html.includes('41.5 ms'), 'latency renders with its unit');
  assert.ok(html.includes('KB/s'), 'throughput renders through the speed formatter');
  assert.ok(html.includes('1420'), 'PMTU renders');
  assert.ok(html.includes('Best method'), 'the best card renders');
  assert.ok(/id="ctbDeploy"[^>]*disabled/.test(html), 'deploy stays disabled while the backend offers no deploy field');
  assert.ok(html.includes('Deploy wiring arrives with the backend'), 'the disabled deploy carries its honest hint');
  assert.ok(html.includes('—') , 'null fields render the em dash in the live table');
  assert.ok(html.includes('New pair') && html.includes('Run again'), 'the finished board offers run again / new pair');

  // deploy activates only with the backend's additive deploy field
  apiHandler = () => ({ running: false, results: [{ method: 'udp-hammer', reachability: 'pass', score: 9 }], best: { method: 'udp-hammer' }, deploy: {} });
  await vm.runInContext('ctbRun()', sandbox);
  await sleep(20);
  html = el('ctunBoard').innerHTML;
  assert.ok(!/id="ctbDeploy"[^>]*disabled/.test(html), 'the deploy field enables the deploy button');
  await vm.runInContext('ctbDeployBest()', sandbox);
  const post = apiCalls.find(c => c.method === 'POST');
  assert.ok(post && post.path === '/api/benchmark/method-board/deploy', 'deploy posts to the board deploy endpoint');
  // the body is a cross-realm object: compare field-by-field, not by prototype
  assert.equal(post.body.iran, 'ag-ir1', 'deploy posts the iran side');
  assert.equal(post.body.kharej, 'ag-fa1', 'deploy posts the kharej side');
  assert.equal(post.body.method, 'udp-hammer', 'deploy posts the best method');

  // a failing run renders the honest error with a retry
  vm.runInContext(`ctbState = { iran: 'ag-ir1', kharej: 'ag-fa1', running: false, view: null, timer: null, err: null };`, sandbox);
  apiHandler = () => { throw new Error('[E-UPDATE-01] board feed unreachable'); };
  await vm.runInContext('ctbRun()', sandbox);
  html = el('ctunBoard').innerHTML;
  assert.ok(html.includes('ent-error') && html.includes('data-state-action="ctbrun"'), 'a failed run renders the §1 error with a retry');

  // ---- Piece 3: the kernel tuning card --------------------------------------
  vm.runInContext(`
  ktData = ktNorm({
    agents: [
      { agent_id: 'ag-1', agent_name: 'tehran-1', bbr: true, qdisc: 'fq', gso: false, tcp_mtu_probing: 1 },
      { agent: { id: 'ag-2', name: 'frankfurt-1' }, congestion_control: 'cubic', fq: true, gso: null, mtu_probing: null },
      { name: 'shiraz-3', bbr: null, fq: null, gso: null, mtu: null, detail: 'agent offline' },
    ],
  });
  ktErr = null; ktLoading = false;
  renderKernelTuning();
  `, sandbox);
  html = el('ktBody').innerHTML;
  assert.ok((html.match(/<tr>/g) || []).length === 4, 'three agents render as three rows (+header)');
  assert.ok(html.includes('tehran-1') && html.includes('frankfurt-1') && html.includes('shiraz-3'), 'every agent row carries its name');
  assert.ok(html.includes('BBR') && html.includes('Queue (fq)') && html.includes('GSO') && html.includes('MTU probing'), 'the table carries the four dispatched facts');
  assert.ok(html.includes('1 of 3 agents report BBR'), 'the summary line counts true/string-bbr rows correctly');
  assert.ok(html.includes('cubic'), 'a string congestion control renders verbatim');
  assert.ok(html.includes('fq'), 'the qdisc value renders verbatim');
  assert.ok((html.match(/ctun-no/g) || []).length >= 1, 'a false fact renders the off mark');
  assert.ok(html.includes('agent offline'), 'the row note renders');

  vm.runInContext(`ktData = null; ktErr = 'boom'; renderKernelTuning();`, sandbox);
  html = el('ktBody').innerHTML;
  assert.ok(html.includes('ent-error') && html.includes('data-state-action="kt"'), 'a load failure renders the error with a retry');

  vm.runInContext(`ktErr = null; ktData = []; renderKernelTuning();`, sandbox);
  html = el('ktBody').innerHTML;
  assert.ok(html.includes('No kernel state reported yet'), 'an empty fleet renders the honest empty state');

  console.log('ctunnel_ui: all assertions passed');
})().catch(e => { console.error(e && e.stack || e); process.exit(1); });
