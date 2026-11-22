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
// the grid state + name/alias consts + the grid render (which now also
// drives the core-detail view sync) the artifact store and the page share
const consts = section('let coresCache = null;', 'function coresPickerOpen');
const cmp = section('let artList = null;', 'function artSize');
const ctun = section('// ===== Core details drill-down page (§2.22 additive fields)', '(function coresWire()');
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
  topTitleSync() {}, tabHashWrite() {},
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
  // ---- Piece 1: the redesigned core details drill-down page ------------------
  // the fleet two agents know so the installed-count stat card is known
  vm.runInContext(`
  window.__coresAgents = [{ id: 'a1', name: 'tehran-1' }, { id: 'a2', name: 'frank-1' }];
  coresCache = [{ engine: 'ctunnel', latest: '0.3.2', builds: [], installed: {}, detail: { a1: { installed: true, version: '0.3.2' }, a2: { installed: true, version: '0.3.1' } } }];
  coresAvail = { cores: [{ core: 'ctunnel', latest_tag: 'ctunnel-v0.4.0', latest_version: '0.4.0', installed_version: '0.3.2', outdated: true, methods: null }] };
  ctunPageOpen('ctunnel');
  `, sandbox);
  let html = el('ctunPage').innerHTML;
  assert.equal(vm.runInContext('ctunOpen', sandbox), 'ctunnel', 'opening a core sets the detail state');
  assert.equal(el('coresList').hidden, true, 'the grid hides while the detail page is open');
  assert.equal(el('coresDetail').hidden, false, 'the detail page shows');
  assert.ok(html.includes('C-Tunnel') && html.includes('Method catalog'), 'the hero heads with the core name and the catalog label');
  assert.ok(html.includes('Browse and manage supported C-Tunnel methods'), 'the hero carries its description');
  assert.ok(html.includes('Manage installations') && html.includes('data-ctun-manage'), 'the primary install action renders');
  assert.ok(html.includes('Back to cores') && html.includes('data-ctun-back'), 'the secondary back action renders');
  // the three stat cards
  assert.ok(html.includes('Total methods') && html.includes('—'), 'the methods card reads honest — while the feed has no catalog');
  assert.ok(html.includes('Latest repo version') && html.includes('v0.4.0'), 'the repo version card shows the repo latest');
  // catalog lands → stats fill, table paginates
  vm.runInContext('globalThis.mkMethods = ' + mkMethods.toString(), sandbox);
  vm.runInContext(`
  coresAvail.cores[0].methods = mkMethods();
  // the first row mirrors the real manifest shape (name + title with the
  // parenthetical technical description)
  coresAvail.cores[0].methods[0] = { id: 1, name: '6to4', title: '6to4 (SIT carrier + IPIP6 inside)', layer: 'L3', transport: 'raw-ip', ipver: 'v4', overhead: 60, needs_port: false, userspace: false, mikrotik: true, family: 'kernel-l3' };
  renderCtunPage();
  `, sandbox);
  html = el('ctunPage').innerHTML;
  const tHtml = el('ctunTableWrap').innerHTML;
  assert.ok(html.includes('Total methods') && html.includes('<bdi dir="ltr">49</bdi>'), 'the methods card counts the real catalog');
  assert.ok(html.includes('of 2 agents') && html.includes('v0.3.2'), 'the install card counts agents and names the installed version');
  assert.ok(html.includes('Newer version available') && html.includes('v0.4.0'), 'outdated=true carries the newer-version mark');
  assert.ok(html.includes('All') && html.includes('L2') && html.includes('L3') && html.includes('L4'), 'layer filter chips render');
  assert.ok(html.includes('16') && html.includes('15') && html.includes('18'), 'the chips carry their per-layer counts');
  assert.ok(tHtml.includes('Overhead') && tHtml.includes('Port required') && tHtml.includes('Userspace') && tHtml.includes('MikroTik') && tHtml.includes('Family'), 'the table carries every dispatched column');
  assert.ok(html.includes('data-ctun-page') === false, 'no leaked test attribute');
  assert.ok((tHtml.match(/ctun-lb-/g) || []).length === 10, 'page 1 renders 10 rows (10 per page)');
  assert.ok(tHtml.includes('Showing 1–10 of 49 methods'), 'the pager names the range');
  assert.ok((tHtml.match(/data-ctun-goto="(\d+)"/g) || []).length === 5, 'five page buttons for 49 methods at 10 per page');
  assert.ok(tHtml.includes('>6to4</b>') && tHtml.includes('SIT carrier + IPIP6 inside'), 'the method cell splits name and technical description');
  assert.ok(tHtml.includes('kernel-l3'), 'the family renders');
  assert.ok(tHtml.includes('Yes') && tHtml.includes('No'), 'the booleans render as yes/no pills');
  assert.ok(tHtml.includes('&lt;script&gt;') && !tHtml.includes('<script>'), 'the method title renders escaped');
  assert.ok(tHtml.includes('data-ctun-detail="0"'), 'rows carry their details action');
  // details panel
  vm.runInContext(`ctunDetailOpen(coresAvail.cores[0].methods[0])`, sandbox);
  const dHtml = el('ctunDetailBody').innerHTML;
  assert.ok(el('ctunDTitle').textContent.includes('Method details') && el('ctunDTitle').textContent.includes('6to4'), 'the details panel titles the method');
  assert.ok(dHtml.includes('SIT carrier + IPIP6 inside') && dHtml.includes('raw-ip') && dHtml.includes('kernel-l3') && dHtml.includes('>60<'), 'the details panel renders the manifest fields');
  assert.ok(dHtml.includes('no unit') || dHtml.includes('\u0648\u0627\u062d\u062f'), 'the overhead row carries the honest unit note');
  vm.runInContext('ctunDetailClose()', sandbox);

  // pagination: page 5 → 9 rows; per-page 50 → one page of 49
  vm.runInContext(`ctunPageNo = 5; renderCtunTable();`, sandbox);
  html = el('ctunTableWrap').innerHTML;
  assert.ok((html.match(/ctun-lb-/g) || []).length === 9, 'page 5 keeps the 9 remaining rows');
  assert.ok(html.includes('Showing 41–49 of 49 methods'), 'the pager names the last range');
  vm.runInContext(`ctunPerPage = 50; ctunPageNo = 1; renderCtunTable();`, sandbox);
  html = el('ctunTableWrap').innerHTML;
  assert.ok((html.match(/ctun-lb-/g) || []).length === 49, 'per-page 50 renders the whole catalog');
  assert.ok(html.includes('Showing 1–49 of 49 methods'), 'the pager names the full range');

  // search: by name, by family; no results state
  vm.runInContext(`ctunSearch = '6to4'; ctunPageNo = 1; renderCtunTable();`, sandbox);
  html = el('ctunTableWrap').innerHTML;
  assert.ok(html.includes('Showing 1–1 of 1 methods'), 'the search narrows to the name match');
  vm.runInContext(`ctunSearch = 'kernel-l3'; renderCtunTable();`, sandbox);
  html = el('ctunTableWrap').innerHTML;
  const famMatch = Number((html.match(/of (\d+) methods/) || [])[1]);
  assert.ok(famMatch > 0 && famMatch < 49, 'the family search matches its subset');
  vm.runInContext(`ctunSearch = 'zzz-nothing'; renderCtunTable();`, sandbox);
  html = el('ctunTableWrap').innerHTML;
  assert.ok(html.includes('No methods match your search.') && html.includes('data-ctun-clear'), 'a no-match search renders its honest empty state with a clear action');

  // value filters: transport + mikrotik combine with the layer filter
  vm.runInContext(`ctunSearch = ''; ctunLayer = 'L3'; renderCtunTable();`, sandbox);
  html = el('ctunTableWrap').innerHTML;
  const l3Total = Number((html.match(/of (\d+) methods/) || [])[1]);
  vm.runInContext(`ctunF.transport = 'raw-ip'; ctunPageNo = 1; renderCtunTable();`, sandbox);
  html = el('ctunTableWrap').innerHTML;
  const l3Raw = Number((html.match(/of (\d+) methods/) || [])[1]);
  assert.ok(l3Raw > 0 && l3Raw <= l3Total, 'the transport filter composes with the layer filter');
  vm.runInContext(`ctunF.mikrotik = 'yes'; ctunPageNo = 1; renderCtunTable();`, sandbox);
  html = el('ctunTableWrap').innerHTML;
  const l3RawMik = Number((html.match(/of (\d+) methods/) || [])[1]);
  assert.ok(l3RawMik <= l3Raw, 'the mikrotik filter composes on top');
  vm.runInContext(`
  ctunSearch = ''; ctunLayer = 'all'; ctunF = { transport: '', ipver: '', port: '', userspace: '', mikrotik: '' }; ctunPageNo = 1; ctunPerPage = 10;
  `, sandbox);
  vm.runInContext(`renderCtunPage()`, sandbox);
  const cHtml = el('ctunTableWrap').innerHTML;

  // null fields render —
  vm.runInContext(`
  coresAvail.cores[0].methods = [{ id: 'x1', name: 'x1', title: 'Null row', layer: 'L4', transport: null, ipver: null, overhead: null, needs_port: null, userspace: null, mikrotik: null, family: null }];
  renderCtunPage();
  `, sandbox);
  html = el('ctunTableWrap').innerHTML;
  assert.ok((html.match(/art-dash/g) || []).length === 5, 'every null field renders the em dash');

  // outdated=false → up to date; absent field + older installed → fallback badge; equal → nothing
  vm.runInContext(`
  coresAvail.cores[0].methods = mkMethods();
  coresAvail.cores[0].outdated = false; coresAvail.cores[0].installed_version = '0.4.0';
  renderCtunPage();
  `, sandbox);
  html = el('ctunPage').innerHTML;
  assert.ok(html.includes('Up to date') && !html.includes('Newer version available'), 'outdated=false shows the up-to-date mark');
  vm.runInContext(`
  delete coresAvail.cores[0].outdated; coresAvail.cores[0].installed_version = '0.3.2';
  renderCtunPage();
  `, sandbox);
  html = el('ctunPage').innerHTML;
  assert.ok(html.includes('Newer version available'), 'the fallback comparison flags an older installed version');
  vm.runInContext(`
  coresAvail.cores[0].installed_version = '0.4.0'; renderCtunPage();
  `, sandbox);
  html = el('ctunPage').innerHTML;
  assert.ok(!html.includes('Newer version available') && html.includes('Up to date'), 'equal versions with no field read up to date via the fallback comparison');
  // installed_version absent → the newest agent-installed version stands in
  vm.runInContext(`
  delete coresAvail.cores[0].installed_version; coresAvail.cores[0].outdated = undefined;
  coresCache[0].detail = { a1: { installed: true, version: '0.3.1' }, a2: { installed: true, version: '0.3.2' } };
  renderCtunPage();
  `, sandbox);
  html = el('ctunPage').innerHTML;
  assert.ok(html.includes('v0.3.2'), 'the computed installed version picks the newest agent one');
  // install states: none installed → نصب نشده; no agent facts → not available
  vm.runInContext(`
  coresCache[0].detail = { a1: { installed: false, version: '' }, a2: { installed: false, version: '' } };
  coresAvail.cores[0].installed_version = ''; renderCtunPage();
  `, sandbox);
  html = el('ctunPage').innerHTML;
  assert.ok(html.includes('Not installed'), 'a zero install count reads نصب نشده, never 0');
  vm.runInContext(`
  coresCache[0].detail = {}; renderCtunPage();
  `, sandbox);
  html = el('ctunPage').innerHTML;
  assert.ok(html.includes('Not available'), 'unknown install facts read اطلاعات در دسترس نیست');
  // no methods on the feed → the honest empty state
  vm.runInContext(`
  coresCache[0].detail = {};
  delete coresAvail.cores[0].methods; renderCtunPage();
  `, sandbox);
  html = el('ctunPage').innerHTML;
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
  assert.ok(html.includes('1.0 Mbps'), 'throughput renders through the wire-bps formatter');
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

  // the REAL BACKEND-42 shape: rows[].recommended + the bare `best` string +
  // iran_name/kharej_name + the wire-bps throughput + the honest unprobed word
  apiHandler = () => ({ running: false, phase: 'done', iran: 'ag-ir1', kharej: 'ag-fa1',
    iran_name: 'tehran-1', kharej_name: 'frankfurt-1',
    rows: [
      { method: 'udp-hammer', layer: 'L4', transport: 'udp', ipver: 'both', reachability: 'ok', latency_ms: 41.5, throughput: 104857600, pmtu: null, score: 87, recommended: true },
      { method: 'icmp-slip', layer: 'L3', transport: 'icmp', ipver: 'v4', reachability: 'unprobed', latency_ms: null, throughput: null, score: null },
      { method: 'tcp-weave', layer: 'L4', transport: 'tcp', ipver: 'v4', reachability: 'ok', latency_ms: 60, throughput: null, score: null, error: 'counters did not move' },
    ],
    best: 'udp-hammer', error: '' });
  vm.runInContext(`ctbState = { iran: 'ag-ir1', kharej: 'ag-fa1', running: false, view: null, timer: null, err: null };`, sandbox);
  await vm.runInContext('ctbRun()', sandbox);
  await sleep(20);
  html = el('ctunBoard').innerHTML;
  assert.ok(html.includes('tehran-1') && html.includes('frankfurt-1'), 'the board header uses the backend pair names');
  assert.ok(html.includes('Best method') && html.includes('udp-hammer'), 'the bare best string resolves to the best card');
  assert.ok(html.includes('104.9 Mbps'), 'the wire-bps throughput formats as Mbps');
  assert.ok(html.includes('unprobed'), 'an unprobed row keeps its honest word in a dim badge');
  assert.ok(html.includes('Recommended by the board'), 'the recommended row carries its best-mark');
  assert.ok(html.includes('counters did not move'), 'a reachable-but-unmeasured row names its error');
  assert.ok(/id="ctbDeploy"[^>]*disabled/.test(html), 'the real shape ships no deploy field — the button stays honest-disabled');

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
      { agent: 'ag-1', name: 'tehran-1', online: true, congestion_control: 'bbr', qdisc: 'fq', gso: 'on', tcp_mtu_probing: 1 },
      { agent: 'ag-2', name: 'frankfurt-1', online: true, congestion_control: 'cubic', qdisc: 'fq_codel', gso: 'off', tcp_mtu_probing: 0 },
      { agent: 'ag-3', name: 'shiraz-3', online: false, congestion_control: null, qdisc: null, gso: null, tcp_mtu_probing: null, error: 'agent unreachable' },
    ],
  });
  ktErr = null; ktLoading = false;
  renderKernelTuning();
  `, sandbox);
  html = el('ktBody').innerHTML;
  assert.ok((html.match(/<tr>/g) || []).length === 4, 'three agents render as three rows (+header)');
  assert.ok(html.includes('tehran-1') && html.includes('frankfurt-1') && html.includes('shiraz-3'), 'every agent row carries its name');
  assert.ok(html.includes('BBR') && html.includes('Queue (fq)') && html.includes('GSO') && html.includes('MTU probing'), 'the table carries the four dispatched facts');
  assert.ok(html.includes('1 of 3 agents report BBR'), 'the summary line counts the congestion-control bbr rows');
  assert.ok(html.includes('cubic'), 'a string congestion control renders verbatim');
  assert.ok(html.includes('fq_codel'), 'the qdisc value renders verbatim');
  assert.ok((html.match(/ctun-yes/g) || []).length >= 5, 'bbr + fq + fq_codel + gso-on + mtu-1 read as on');
  assert.ok(html.includes('<bdi dir="ltr">off</bdi>') && html.includes('<bdi dir="ltr">0</bdi>'), 'off/0 sysctl strings render verbatim in dim badges');
  assert.ok(html.includes('agent unreachable'), 'an offline agent keeps its honest note');

  vm.runInContext(`ktData = null; ktErr = 'boom'; renderKernelTuning();`, sandbox);
  html = el('ktBody').innerHTML;
  assert.ok(html.includes('ent-error') && html.includes('data-state-action="kt"'), 'a load failure renders the error with a retry');

  vm.runInContext(`ktErr = null; ktData = []; renderKernelTuning();`, sandbox);
  html = el('ktBody').innerHTML;
  assert.ok(html.includes('No kernel state reported yet'), 'an empty fleet renders the honest empty state');

  console.log('ctunnel_ui: all assertions passed');
})().catch(e => { console.error(e && e.stack || e); process.exit(1); });
