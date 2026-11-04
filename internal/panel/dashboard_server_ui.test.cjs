'use strict';
// VM integration tests execute the owned rendering functions with deliberately
// different server and tunnel counters. No server or browser is required.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');
function section(start, end) {
  const a = source.indexOf(start), b = source.indexOf(end, a + start.length);
  assert.ok(a >= 0 && b > a, 'source anchors: ' + start);
  return source.slice(a, b);
}
const code = section('function engLabel(e)', 'function latBadge(ms)')
  + section('function setTraffic(', '// Canvas cannot read CSS variables')
  + section('function linkStrip(s)', '// Engine of the base tunnel:')
  + section('function hydrateCachedTraffic()', 'try { hydrateCachedTraffic();')
  + section('async function refreshDash()', 'function drawActivityChart(')
  + section('function usageCols(d, tot)', 'async function loadUsage()')
  + section('function applyTunnelDetail(scroll)', '// Two-step remove (inline confirm');
function node() {
  const classes = new Set();
  return { textContent: '', innerHTML: '', hidden: false, dataset: {}, title: '',
    style: { setProperty() {} }, querySelector: () => null,
    classList: { add: (...names) => names.forEach(n => classes.add(n)), remove: (...names) => names.forEach(n => classes.delete(n)),
      contains: n => classes.has(n), toggle: (n, on) => { if (on) classes.add(n); else classes.delete(n); } } };
}
function harness(d) {
  const ids = ['kpiDown', 'kpiUpRate', 'kpiTraffic', 'kpiTrafficSub', 'icoTraffic', 'kpiConns', 'kpiConnsSub', 'actNow',
    'actLiveBadge', 'icoConns', 'chartDown', 'chartCard', 'chartLiveBadge', 'roleLine', 'usageBody', 'tab-tunnel', 'dashHero', 'dashDetails'];
  const elements = Object.fromEntries(ids.map(id => [id, node()]));
  const liveText = node(); elements.chartLiveBadge.querySelector = () => liveText;
  const saved = new Map(), calls = [];
  const context = {
    window: { _quantumEmpty: true }, document: { hidden: false, querySelectorAll: () => [] },
    $: id => elements[id] || null, api: async () => d, dashRange: '1h', tunPeerCache: [], _tunnelDetail: null,
    tr: (_key, fallback) => fallback, esc: s => String(s ?? '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/"/g, '&quot;'), NA: '—',
    ICO_SRV: '', ICO_GRE: '', ICO_FRP: '', renderMatrixSetup() {}, renderTunnelsList() {}, renderMatrixDetail() {}, renderDashPeers() {},
    drawChart: (hist, down) => calls.push(['traffic', hist, down]), drawActivityChart: (hist, down) => calls.push(['activity', hist, down]),
    renderTunnelOverview: m => calls.push(['overview', m]), topologyDisplayMode() {},
    fmtBytes: n => n == null ? 'N/A' : n + ' B', fmtRate: n => n + ' B/s', fmtSecs: n => String(n), latBadge: () => '', icmpChip: () => '',
    localStorage: { getItem: k => saved.get(k), setItem: (k, v) => saved.set(k, v), removeItem: k => saved.delete(k) },
    setTimeout: f => f(), reportErr: (_name, e) => { throw e; }
  };
  vm.createContext(context); vm.runInContext(code, context);
  return { context, elements, saved, calls, liveText };
}
const legacy = { online: false, health: 'DOWN', local_pub: '203.0.113.7', role: '', gre: {}, frp: {}, peers: [], system: {},
  traffic: { down: 999, up: 888, total: 1887, speed_down: 77, speed_up: 66 }, conns: 900,
  history: [{t:1,down:999,up:888,conns:900},{t:2,down:1999,up:1888,conns:901}] };
(async () => {
  const history = [{ t: 1, down: 100, up: 50, conns: 2 }, { t: 2, down: 300, up: 80, conns: 5 }];
  const d = { ...legacy, server_traffic: { rx: 300, tx: 80, rx_rate: 200, tx_rate: 30, known: true, interfaces: ['eth0'], counters_at: 2000 },
    server_history: history, server_connections: { active: 4, known: true } };
  const h = harness(d); assert.equal(await h.context.refreshDash(), true);
  assert.equal(h.elements.kpiDown.textContent, '200 B/s'); assert.equal(h.elements.kpiUpRate.textContent, '30 B/s');
  assert.equal(h.elements.kpiTraffic.textContent, '380 B'); assert.equal(h.elements.kpiConns.textContent, '4');
  assert.match(h.elements.kpiConnsSub.textContent, /Peak 5/); assert.equal(h.elements.actNow.textContent, '4 now');
  assert.deepEqual(h.calls.find(c => c[0] === 'traffic'), ['traffic', history, false]);
  assert.deepEqual(h.calls.find(c => c[0] === 'activity'), ['activity', history, false]);
  assert.ok(h.elements.chartDown.classList.contains('hidden')); assert.equal(h.elements.chartLiveBadge.hidden, false);
  assert.equal(h.liveText.textContent, 'Live'); // no tunnels/onboarding must not pause host collection
  assert.match(h.elements.roleLine.innerHTML, /This server.*203\.0\.113\.7/);
  assert.doesNotMatch(h.elements.roleLine.innerHTML, /Iran|Foreign|FRP|⇄/);
  const cache = JSON.parse(h.saved.get('quantum_server_traffic_cache'));
  assert.equal(cache.scope, 'server'); assert.equal(cache.traffic.total, 380);
  assert.equal(h.saved.has('quantum_traffic_cache'), false);
  assert.ok(h.calls.some(c => c[0] === 'overview' && c[1] === null));

  for (const fields of [{}, {server_traffic:{known:false,rx:0,tx:0,rx_rate:0,tx_rate:0},server_connections:{known:false,active:0}},
    {server_traffic:{known:true,rx:null,tx:null,rx_rate:null,tx_rate:null},server_connections:{known:true,active:null}}]) {
    const u = harness({...legacy,...fields}); assert.equal(await u.context.refreshDash(), true);
    for (const id of ['kpiDown','kpiUpRate','kpiTraffic','kpiConns','actNow']) assert.equal(u.elements[id].textContent,'Unknown',id);
    assert.equal(u.elements.actLiveBadge.hidden,true);
    assert.equal(u.calls.find(c => c[0] === 'traffic')[1].length,0);
  }
  const zero = harness({...d,server_traffic:{known:true,rx:0,tx:0,rx_rate:0,tx_rate:0},server_connections:{known:true,active:0}});
  await zero.context.refreshDash(); assert.equal(zero.elements.kpiTraffic.textContent,'0 B'); assert.equal(zero.elements.kpiConns.textContent,'0');
  const oldCache = harness(legacy);
  oldCache.saved.set('quantum_traffic_cache',JSON.stringify({traffic:legacy.traffic,history:legacy.history}));
  oldCache.context.hydrateCachedTraffic(); assert.equal(oldCache.elements.kpiTraffic.textContent,''); assert.equal(oldCache.calls.length,0);
  h.context.hydrateCachedTraffic(); assert.equal(h.elements.kpiTraffic.textContent,'380 B'); assert.equal(h.elements.kpiDown.textContent,'—');
  h.context._tunnelDetail = 'matrix'; h.context.applyTunnelDetail(false);
  assert.ok(h.calls.some(c => c[0] === 'overview' && c[1] === 'matrix'));

  const usage = {range:'1h',server_total:{rx:1000,tx:500,total:1500,known:true},server_window:{rx:30,tx:20,total:50,known:true,
    points:[{t:1,rx:10,tx:5},{t:61,rx:20,tx:15}]}, topologies:[{id:'topo-1',name:'Named topology',rx:7,tx:3,total:10,known:true,tunnel_count:2}],
    unassigned:{rx:null,tx:null,total:null,known:false,tunnel_count:1},links:[{name:'Wrong tunnel total',rx:888888,tx:999999}]};
  h.context.renderUsage(usage); const html = h.elements.usageBody.innerHTML;
  assert.match(html,/Server total.*1500 B/); assert.match(html,/Selected range.*50 B/); assert.match(html,/Named topology/);
  // Stale deleted-entity data (unknown traffic, history-only tunnel count)
  // must never resurrect the unassigned row.
  assert.doesNotMatch(html,/Unassigned tunnels/);
  assert.match(html,/ug-chart/); assert.doesNotMatch(html,/Wrong tunnel total|888888|999999/);
  assert.equal((html.match(/class="ug-col"/g)||[]).length,2);
  usage.topologies[0].name = 'Renamed grouping'; h.context.renderUsage(usage);
  assert.match(h.elements.usageBody.innerHTML,/Renamed grouping/); assert.doesNotMatch(h.elements.usageBody.innerHTML,/Named topology/);
  h.context.renderUsage({links:usage.links,topologies:[{name:'Unmeasured',rx:0,tx:0,known:false}]});
  assert.match(h.elements.usageBody.innerHTML,/Unknown/); assert.doesNotMatch(h.elements.usageBody.innerHTML,/0 B|888888/);
  // The unassigned bucket is a fallback only: hidden when empty, stale-only or
  // unknown, shown only for a live unassigned tunnel with measured nonzero traffic.
  h.context.renderUsage({...usage,unassigned:{rx:0,tx:0,total:0,known:true,tunnel_count:0}});
  assert.doesNotMatch(h.elements.usageBody.innerHTML,/Unassigned tunnels/);
  h.context.renderUsage({...usage,unassigned:{rx:null,tx:null,total:null,known:false,tunnel_count:1}});
  assert.doesNotMatch(h.elements.usageBody.innerHTML,/Unassigned tunnels/);
  h.context.renderUsage({...usage,unassigned:{rx:5,tx:6,total:11,known:true,tunnel_count:0}});
  assert.doesNotMatch(h.elements.usageBody.innerHTML,/Unassigned tunnels/);
  h.context.renderUsage({...usage,unassigned:{rx:5,tx:6,total:11,known:true,tunnel_count:1}});
  assert.match(h.elements.usageBody.innerHTML,/Unassigned tunnels/);
  // Zero topologies plus stale deleted-tunnel data: no row at all.
  h.context.renderUsage({...usage,topologies:[],unassigned:{rx:null,tx:null,total:null,known:false,tunnel_count:1}});
  assert.doesNotMatch(h.elements.usageBody.innerHTML,/Unassigned tunnels/);
  const seven = h.context.usageCols({range:'7d'},usage.server_window);
  assert.equal(seven.length,1); assert.equal(seven[0].rx,30); assert.equal(seven[0].tx,20);

  const faSource = fs.readFileSync(path.join(__dirname,'i18n-fa.js'),'utf8');
  const faJSON = faSource.slice(faSource.indexOf('window.FA_TEXT = ')+'window.FA_TEXT = '.length).trim().replace(/;$/,'');
  const pairs = [...faJSON.matchAll(/^"((?:\\.|[^"\\])+)":/gm)].map(m=>JSON.parse('"'+m[1]+'"'));
  assert.equal(new Set(pairs).size,pairs.length,'no duplicate Persian phrase keys');
  const fa = JSON.parse(faJSON);
  const dict = section('const I18N = {','\n};');
  for (const m of dict.matchAll(/\b(?:dash_server_\w+|dash_this_server|dash_unknown|usage_\w+): "([^"]+)"/g)) assert.ok(fa[m[1]],'Persian phrase '+m[1]);
  console.log('Server dashboard rendering, unknown isolation, cache, detail hooks, topology usage, ranges and FA: passed');
})().catch(e => { console.error(e); process.exitCode = 1; });
