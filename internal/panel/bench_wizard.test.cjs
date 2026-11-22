'use strict';
// VM tests for the redesigned Pair Benchmark wizard (tab-benchmark): the
// four-step stepper, eligibility gating, the different-datacenter rule on
// real datacenter ids, the core cards with their method counts, and the
// dual-runner start/poll/stop flow. No server or browser required.
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
const consts = section('let coresCache = null;', 'function coresPickerOpen');
const cmp = section('let artList = null;', 'function artSize');
const ctun = section('// ===== Core details drill-down page (\u00a72.22 additive fields)', '(function coresWire()');
const board = section('// ---------- C-Tunnel method board (tab-cboard, \u00a72.23)', '// ---------- Kernel Tuning (tab-netopt)');
const bench = section('// ---------- Pair Benchmark (tab-benchmark)', '// ---------- C-Tunnel method board (tab-cboard, §2.23)');
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
function esc(s) { return String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])); }
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
  window: { __coresAgents: [], __engKeyCfg: true, matchMedia: () => ({ matches: false }) },
  $: id => el(id), el,
  esc,
  tr: (_k, fb) => fb,
  currentLang: 'en',
  BASE: '', at: p => p,
  api: async (p, b, m) => { apiCalls.push({ path: p, body: b, method: m || (b ? 'POST' : 'GET') }); if (!apiHandler) throw new Error('no api handler'); return apiHandler(p, b, m); },
  skel: n => '<div class="skel"></div>'.repeat(n || 1),
  stateError: (msg, actionKey) => '<div class="ent-empty ent-error" data-state-action="' + (actionKey || '') + '">' + esc(msg) + '</div>',
  modalShow() {}, modalHide() {},
  entConfirm: async () => true,
  toastOk: (t, s) => toasts.push(['ok', t, s]),
  toastErr: (t, s) => toasts.push(['err', t, s]),
  toastWarn: (t, s) => toasts.push(['warn', t, s]),
  topologyFlagSvg: () => '<svg class="flag"></svg>',
  fmtSpeedRate: v => Math.round(v / 1024) + ' KB/s',
  getCSRFToken: () => 'tok', setCSRFToken() {}, showLogin() {},
  userActiveAt: Date.now() - 1000,
  loadCores() {}, renderCores() {}, loadArtStore() {}, loadCoresAvailable() {},
  topTitleSync() {}, tabHashWrite() {},
};
vm.createContext(sandbox);
vm.runInContext(consts + '\n' + cmp + '\n' + ctun + '\n' + board + '\n' + bench, sandbox);
const sleep = ms => new Promise(r => setTimeout(r, ms));

// the static hero carries the three dispatched copy blocks
assert.ok(indexHtml.includes('data-i18n="bench_wiz_note"') && indexHtml.includes('data-i18n="bench_wiz_badge"'), 'the hero carries the note and the badge');
assert.ok(indexHtml.includes('bw-hero-badge'), 'the badge renders as its own element');

const TOPOS = { topologies: [{ id: 1, name: 'main-mesh' }, { id: 2, name: 'backup-link' }] };
const MEMBERS = {
  1: { iran: [
        { id: 11, name: 'tehran-1', agent: { id: 'ag1', status: 'online' }, datacenter: '\u062a\u0647\u0631\u0627\u0646', datacenter_en: 'Tehran-Azadi' },
        { id: 12, name: 'tehran-2', agent: { id: 'ag2', status: 'offline' }, datacenter: '\u062a\u0647\u0631\u0627\u0646', datacenter_en: 'Tehran-Azadi' },
      ],
      foreign: [
        { id: 21, name: 'frankfurt-1', agent: { id: 'ag3', status: 'online' }, datacenter: '\u0647\u062a\u0632\u0646\u0631', datacenter_en: 'Hetzner' },
        { id: 22, name: 'tehran-copy', agent: { id: 'ag4', status: 'online' }, datacenter: '\u062a\u0647\u0631\u0627\u0646', datacenter_en: 'Tehran-Azadi' },
      ] },
  2: { iran: [{ id: 31, name: 'shiraz-1', agent: { id: 'ag5', status: 'online' }, datacenter_en: 'Shiraz' }], foreign: [] },
};
const DCS = { datacenters: [
  { id: 7, name: '\u062a\u0647\u0631\u0627\u0646', datacenter_en: 'Tehran-Azadi' },
  { id: 9, name: '\u0647\u062a\u0632\u0646\u0631', datacenter_en: 'Hetzner' },
] };
const AGENTS = { agents: [
  { id: 'ag1', name: 'tehran-1', status: 'online', country: 'IR' },
  { id: 'ag2', name: 'tehran-2', status: 'offline', country: 'IR' },
  { id: 'ag3', name: 'frankfurt-1', status: 'online', country: 'DE' },
  { id: 'ag4', name: 'tehran-copy', status: 'online', country: 'IR' },
] };
const AG_DETAIL = {
  ag1: { agent: { id: 'ag1', status: 'online', country: 'IR', effective_public_ip: '10.0.0.1' } },
  ag2: { agent: { id: 'ag2', status: 'offline', country: 'IR', effective_public_ip: '10.0.0.2' } },
  ag3: { agent: { id: 'ag3', status: 'online', country: 'DE', effective_public_ip: '192.0.2.1' } },
  ag4: { agent: { id: 'ag4', status: 'online', country: 'IR', effective_public_ip: '10.0.0.4' } },
};
const ENGINES = {
  ag1: { engines: [{ engine: 'ctunnel', enabled: true, version: '2.0.0' }, { engine: 'frp', enabled: true, version: '0.61.1' }, { engine: 'matrix', enabled: true, version: 'fake-1.0' }] },
  ag3: { engines: [{ engine: 'ctunnel', enabled: true, version: '2.0.0' }, { engine: 'frp', enabled: true, version: '0.61.1' }, { engine: 'matrix', enabled: true, version: 'fake-1.0' }] },
};

function wireApi() {
  let pairStarted = false;
  apiHandler = (p) => {
    if (p.startsWith('/api/mesh/topologies')) return TOPOS;
    if (p.startsWith('/api/ui/engines')) return AGENTS;
    if (p.startsWith('/api/benchmark/pair-run')) { pairStarted = true; return { status: 'started', engines: ['frp'] }; }
    if (p.startsWith('/api/benchmark/pair-stop')) return { status: 'stopped' };
    if (p.startsWith('/api/benchmark/pair-clear')) return { status: 'cleared' };
    if (p.startsWith('/api/benchmark/pair')) return pairStarted
      ? { running: true, phase: 'measure', cur_engine: 'frp', results: [] }
      : { running: false, results: [] };
    if (p.startsWith('/api/ui/topologies/1')) return { members: MEMBERS[1] };
    if (p.startsWith('/api/ui/topologies/2')) return { members: MEMBERS[2] };
    if (p.startsWith('/api/ui/topologies/')) return { members: { iran: [], foreign: [] } };
    if (p.startsWith('/api/mesh/datacenters')) return DCS;
    if (p.startsWith('/api/ui/agents/ag1/engines')) return ENGINES.ag1;
    if (p.startsWith('/api/ui/agents/ag3/engines')) return ENGINES.ag3;
    if (p.startsWith('/api/ui/agents/ag1')) return AG_DETAIL.ag1;
    if (p.startsWith('/api/ui/agents/ag2')) return AG_DETAIL.ag2;
    if (p.startsWith('/api/ui/agents/ag3')) return AG_DETAIL.ag3;
    if (p.startsWith('/api/ui/agents/ag4')) return AG_DETAIL.ag4;
    if (p.startsWith('/api/benchmark/method-board')) return { running: false, rows: [
      { method: '6to4', engine: 'ctunnel', reachability: 'ok', latency_ms: 12.5, throughput: 1048576, pmtu: 1420, score: 88 },
      { method: 'icmp-slip', engine: 'ctunnel', reachability: 'converge-timeout', error: 'no convergence' },
    ], best: '6to4' };
    throw new Error('unmocked path: ' + p);
  };
}

(async () => {
  wireApi();
  // the ctunnel catalog rides the feed: 49 real methods for the step-4 count
  vm.runInContext(`coresAvail = { cores: [{ core: 'ctunnel', latest_tag: 'ctunnel-v2.0.0', latest_version: '2.0.0', methods: Array.from({ length: 49 }, (_, i) => ({ id: i + 1, name: 'm' + (i + 1), title: 'Method ' + (i + 1), layer: 'L3', transport: 'udp', ipver: 'v4', overhead: 20, needs_port: false, userspace: false, mikrotik: false, family: 'fam' })) }] };`, sandbox);
  // ---- step 1: the stepper, the summary and the eligibility gating -----------
  await vm.runInContext('loadBenchWizard()', sandbox);
  await sleep(20);
  let html = el('benchWizard').innerHTML;
  assert.ok(html.includes('pbw-step cur') && html.includes('data-bw-goto="4"'), 'the stepper renders four steps');
  assert.ok((html.match(/data-bw-goto="\d+" disabled/g) || []).length === 3, 'future steps are not navigable');
  assert.ok(html.includes('Selection summary'), 'the selection summary renders from step 1');
  assert.ok(html.includes('Select topology'), 'step 1 names itself');
  assert.ok(el('bwBody').innerHTML.includes('main-mesh') && el('bwBody').innerHTML.includes('backup-link'), 'both topology cards render');
  await sleep(30); // eligibility fetches land
  html = el('bwBody').innerHTML;
  assert.ok(html.includes('Iran: 2') && html.includes('Foreign: 2'), 'topology 1 carries its real member counts');
  assert.ok(/data-bw-topo="2"[^>]*disabled/.test(html), 'topology 2 without a foreign side is not selectable');
  assert.ok(html.includes('No Iran/foreign pair in this topology'), 'the ineligible topology names its reason');
  assert.ok(html.includes('Only agents belonging to the selected topology'), 'the helper line renders');

  // ---- pick topology 1 → iran reset → next to step 2 -------------------------
  await vm.runInContext(`bwPickTopo('1', 'main-mesh')`, sandbox);
  assert.equal(vm.runInContext('bwState.topoName', sandbox), 'main-mesh', 'the topology pick records its name');
  assert.equal(vm.runInContext('bwState.iran', sandbox), null, 'a topology change resets the dependent picks');
  await vm.runInContext('bwNext()', sandbox);
  await sleep(40);
  html = el('benchWizard').innerHTML;
  const body2 = el('bwBody').innerHTML;
  assert.ok(html.includes('Select Iran agent'), 'step 2 names itself');
  assert.ok(body2.includes('data-bw-agent="iran"'), 'the iran cards render');
  assert.ok(body2.includes('data-bw-v="12" disabled'), 'the offline agent is not selectable');
  assert.ok(body2.includes('The agent is offline'), 'the offline reason is explained');
  assert.ok(body2.includes('10.0.0.1'), 'the agent card carries its real IP');
  assert.ok(body2.includes('Tehran-Azadi'), 'the agent card carries its datacenter');

  // ---- pick iran → step 3 → the different-datacenter rule --------------------
  await vm.runInContext(`bwPickAgent('iran', '11')`, sandbox);
  assert.equal(vm.runInContext('bwState.iranAgent', sandbox), 'ag1', 'the iran pick records the agent id');
  assert.equal(vm.runInContext('bwState.iranDC', sandbox), '7', 'the iran datacenter id resolves through the real list');
  await vm.runInContext('bwNext()', sandbox);
  await sleep(40);
  html = el('benchWizard').innerHTML;
  const body3 = el('bwBody').innerHTML;
  assert.ok(html.includes('Select foreign agent'), 'step 3 names itself');
  assert.ok(body3.includes('data-bw-v="22" disabled'), 'the same-datacenter foreign agent is not selectable');
  assert.ok(body3.includes('Same datacenter as the Iran agent'), 'the same-datacenter reason is explained');
  assert.ok(!body3.includes('data-bw-v="21" disabled'), 'the different-datacenter foreign agent stays selectable');

  // ---- pick foreign → step 4 → core cards with real method counts ------------
  await vm.runInContext(`bwPickAgent('foreign', '21')`, sandbox);
  assert.equal(vm.runInContext('bwState.foreignDC', sandbox), '9', 'the foreign datacenter id resolves');
  await vm.runInContext('bwNext()', sandbox);
  await sleep(40);
  html = el('benchWizard').innerHTML;
  const body4 = el('bwBody').innerHTML;
  assert.ok(html.includes('Select cores to benchmark'), 'step 4 names itself');
  assert.ok(body4.includes('All 49 methods will be tested'), 'the catalog core promises its real method count');
  assert.ok(body4.includes('Engine benchmark'), 'the catalog-less core names its honest unit');
  assert.ok(body4.includes('Recommended'), 'matrix-style recommendation badge renders');
  assert.ok(body4.includes('Ready on both agents') && body4.includes('v2.0.0'), 'the readiness line carries the agent version');
  assert.ok(body4.includes('Temporary test tunnels will be created'), 'the pre-run warning renders');
  assert.ok(body4.includes('data-bw-core="ctunnel"') && body4.includes('data-bw-core="frp"'), 'both common cores render as cards');

  // ---- core picks + the summary math ----------------------------------------
  await vm.runInContext(`bwPickCore('ctunnel'); bwPickCore('frp'); bwPickCore('matrix');`, sandbox);
  assert.equal(vm.runInContext('bwState.picked.length', sandbox), 3, 'three cores picked');
  assert.ok(vm.runInContext(`bwState.picked.indexOf('ctunnel') >= 0 && bwState.picked.indexOf('frp') >= 0 && bwState.picked.indexOf('matrix') >= 0`, sandbox), 'the picks carry all three cores');
  const sumHtml = el('benchWizard').innerHTML;
  const sumHtml2 = el('pbwSumWrap').innerHTML;assert.ok(sumHtml2.includes('Selected cores') && sumHtml2.includes('49'), 'the summary counts the real methods');

  // ---- start: the dual runner split ------------------------------------------
  await vm.runInContext('bwNext()', sandbox); // step 4 → bwStart
  await sleep(30);
  const runPost = apiCalls.find(c => c.method === 'POST' && c.path === '/api/benchmark/pair-run');
  assert.ok(runPost, 'the plain engine rides the pair-run runner');
  // the body is cross-realm: field-by-field
  assert.deepEqual(Array.from(runPost.body.engines).sort(), ['frp', 'matrix'], 'only the catalog-less cores ride pair-run');
  assert.equal(String(runPost.body.topology_id), '1', 'pair-run carries the topology');
  const boardCall = apiCalls.find(c => c.path.indexOf('method-board') >= 0 && c.path.indexOf('run=1') >= 0);
  assert.ok(boardCall, 'the catalog core rides the method-board runner');
  assert.ok(boardCall.path.indexOf('iran=ag1') >= 0 && boardCall.path.indexOf('kharej=ag3') >= 0, 'the board runs on the same agent pair');
  assert.ok(vm.runInContext('bwState.running', sandbox) === true, 'the pair runner is running');
  assert.ok(vm.runInContext('bwState.boardRunning', sandbox) === false, 'a finished board poll does not stay running');

  // ---- running view: the unified tables and the statuses ---------------------
  apiHandler = (p) => {
    if (p.startsWith('/api/benchmark/pair')) return { running: false, topo_name: 'main-mesh', phase: 'cleanup', cur_engine: 'frp', results: [
      { engine: 'frp', status: 'ok', converge_s: 1.4, down_bps: 1024, up_bps: 512, rtt_ms: 41, score: 7 },
      { engine: 'ctunnel', status: 'converge-timeout', note: 'did not converge' },
    ] };
    if (p.startsWith('/api/benchmark/method-board')) return { running: false, rows: [
      { method: '6to4', engine: 'ctunnel', reachability: 'ok', latency_ms: 12.5, throughput: 1048576, pmtu: 1420, score: 88 },
      { method: 'icmp-slip', engine: 'ctunnel', reachability: 'converge-timeout', error: 'no convergence' },
    ], best: '6to4' };
    return {};
  };
  await vm.runInContext('bwPoll()', sandbox);
  html = el('benchWizard').innerHTML;
  assert.ok(html.includes('Core benchmarks') && html.includes('Method probes'), 'the unified view renders both runner sections');
  assert.ok(html.includes('Completed') && html.includes('Did not come up'), 'the core statuses map to the honest vocabulary');
  assert.ok(html.includes('Temporary tunnels removed'), 'the cleanup confirmation line renders');
  assert.ok(html.includes('Run again') && html.includes('New benchmark'), 'the finished view offers the next actions');

  // ---- stop: the pair runner stops, the board note stays honest --------------
  vm.runInContext(`bwState.running = true; bwState.boardRunning = true; bwStop();`, sandbox);
  html = el('benchWizard').innerHTML;
  const stopCall = apiCalls.find(c => c.method === 'POST' && c.path === '/api/benchmark/pair-stop');
  assert.ok(stopCall, 'stop fires the pair-stop endpoint');
  assert.ok(html.includes('no stop switch'), 'the board keeps running and the view says so');

  console.log('bench_wizard: all assertions passed');
})().catch(e => { console.error(e && e.stack || e); process.exit(1); });
