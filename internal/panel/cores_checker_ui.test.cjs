'use strict';
// VM tests for the ui/ux-1 overnight work: the per-core drill-down
// generalization (the dedicated page opens for EVERY core — per-core hero
// text, NOTICE license/upstream strip, honest catalog-pending state) and
// the methods-checker view (GET /api/overnight/checker, documented shape —
// the backend lands it in parallel; until then the honest not-live state).
// No server or browser required.
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
// the grid state + name/alias consts + the drill-down page (which carries
// the NOTICE strip) + the checker section (the file's last block)
const consts = section('let coresCache = null;', 'function coresPickerOpen');
const cmp = section('let artList = null;', 'function artSize');
const ctun = section('// ===== Core details drill-down page (§2.22 additive fields)', '(function coresWire()');
const chk = section('// ===== Overnight methods checker', null);
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
  topTitleSync() {}, tabHashWrite() {},
};
vm.createContext(sandbox);
vm.runInContext(consts + '\n' + cmp + '\n' + ctun + '\n' + chk, sandbox);

// the static markup carries the checker card inside the cores list view
assert.ok(indexHtml.includes('id="chkCard"') && indexHtml.includes('id="chkBody"') && indexHtml.includes('id="chkRefresh"'), 'the checker card exists in the markup');
assert.ok(indexHtml.includes('data-i18n="chk_title"') && indexHtml.includes('data-i18n="chk_sub"'), 'the checker head copy is i18n-keyed');
assert.ok(indexHtml.indexOf('id="chkCard"') > indexHtml.indexOf('id="engArtCard"'), 'the checker card sits after the artifact store');
assert.ok(indexHtml.indexOf('id="chkCard"') < sourceMatchCoresListEnd(), 'the checker lives INSIDE the cores list (hides while a core detail is open)');
function sourceMatchCoresListEnd() {
  const a = indexHtml.indexOf('id="coresList"');
  // the list closes at the detail mount; the card must precede it
  const b = indexHtml.indexOf('id="coresDetail"');
  assert.ok(a >= 0 && b > a, 'markup anchors: coresList / coresDetail');
  return b;
}

(async () => {
  // ---- Piece 1: the drill-down generalizes to EVERY core --------------------
  vm.runInContext(`
  window.__coresAgents = [{ id: 'a1', name: 'tehran-1' }];
  coresCache = [{ engine: 'frp', latest: '0.61.1', builds: [], installed: {}, detail: { a1: { installed: true, version: '0.61.1' } } }];
  coresAvail = { cores: [{ core: 'frp', latest_tag: 'frp-v0.61.2', latest_version: '0.61.2', installed_version: '0.61.1', outdated: true, methods: null, license: 'Apache-2.0', upstream: 'https://github.com/fatedier/frp' }] };
  ctunPageOpen('frp');
  `, sandbox);
  let html = el('ctunPage').innerHTML;
  assert.equal(vm.runInContext('ctunOpen', sandbox), 'frp', 'opening frp sets the detail state');
  assert.ok(html.includes('FRP — Method catalog'), 'the hero heads with the OPEN core, not a hardcoded one');
  assert.ok(html.includes('Browse and manage the FRP methods'), 'the hero sub-line names the open core');
  assert.ok(html.includes('License: <bdi dir="ltr">Apache-2.0</bdi>'), 'the NOTICE strip carries the manifest license');
  assert.ok(html.includes('href="https://github.com/fatedier/frp"') && html.includes('rel="noopener noreferrer"'), 'the upstream renders as a safe external link');
  assert.ok(html.includes('No method catalog yet'), 'the honest catalog-pending state on a methods-less core');
  // a catalog-less core with NO license/upstream shows neither (never guessed)
  vm.runInContext(`
  coresCache.push({ engine: 'gost', latest: '', builds: [], installed: {}, detail: {} });
  coresAvail = { cores: [{ core: 'gost', latest_tag: 'gost-v3.0.0', latest_version: '3.0.0', installed_version: '', outdated: null, methods: null, license: '', upstream: '' }] };
  ctunPageOpen('gost');
  `, sandbox);
  html = el('ctunPage').innerHTML;
  assert.ok(html.includes('Gost — Method catalog'), 'the page opens for gost too');
  assert.ok(!html.includes('ctun-notice'), 'no license/upstream published → no NOTICE strip');
  // the upstream protocol guard: an odd URL stays inert text, never a link
  vm.runInContext(`
  coresAvail.cores[0].upstream = 'javascript:alert(1)';
  coresAvail.cores[0].license = 'NO LICENSE';
  renderCtunPage();
  `, sandbox);
  html = el('ctunPage').innerHTML;
  assert.ok(!html.includes('href="javascript:'), 'a non-http(s) upstream never becomes a link');
  assert.ok(html.includes('<bdi dir="ltr">javascript:alert(1)</bdi>'), 'the odd upstream still renders verbatim as text');
  assert.ok(html.includes('License: <bdi dir="ltr">NO LICENSE</bdi>'), 'the license renders verbatim');
  vm.runInContext('ctunPageClose()', sandbox);

  // ---- Piece 2: the checker — honest not-live state -------------------------
  apiHandler = () => { throw new Error('not found'); };
  await vm.runInContext('loadChecker(true)', sandbox);
  let chkHtml = el('chkBody').innerHTML;
  assert.ok(chkHtml.includes('Checker not live yet') && chkHtml.includes('data-chk-retry'), 'the endpoint being absent renders the honest not-live state');
  assert.ok(el('chkFeed').innerHTML.includes('Not live yet'), 'the feed line says not live');
  assert.ok(chkHtml.includes('GET /api/overnight/checker'), 'the not-live note names the endpoint');
  // a §1-envelope 501 reads as not-live too
  apiHandler = () => { const e = new Error('[E-X] unavailable'); e.status = 501; throw e; };
  await vm.runInContext('loadChecker(true)', sandbox);
  assert.ok(el('chkBody').innerHTML.includes('Checker not live yet'), 'a 501 also reads as not live');
  // a real failure is NOT the not-live state
  apiHandler = () => { throw new Error('boom'); };
  await vm.runInContext('loadChecker(true)', sandbox);
  chkHtml = el('chkBody').innerHTML;
  assert.ok(chkHtml.includes('Checker error') && chkHtml.includes('boom'), 'a real failure renders the error state with its message');
  assert.ok(!chkHtml.includes('Checker not live yet'), 'a real failure is not confused with not-live');

  // ---- Piece 3: the checker — live payload rendering ------------------------
  apiHandler = p => {
    assert.equal(p, '/api/overnight/checker', 'the checker reads its documented endpoint');
    return { projects: [
      { name: 'ctunnel', methods_found: 49, methods_added: 49, missing: [], status: 'done' },
      { name: 'hysteria', methods_found: 7, methods_added: 3, missing: ['udp-443', 'masque "<evil>"'], status: 'running' },
      { name: 'sing-box', methods_found: null, methods_added: null, missing: null, status: null },
    ] };
  };
  await vm.runInContext('loadChecker(true)', sandbox);
  chkHtml = el('chkBody').innerHTML;
  assert.ok(chkHtml.includes('<bdi dir="ltr">ctunnel</bdi>') && chkHtml.includes('<bdi dir="ltr">sing-box</bdi>'), 'every project renders a row');
  assert.ok(chkHtml.includes('56'), 'found totals sum the numeric rows (49+7)');
  assert.ok(chkHtml.includes('52'), 'added totals sum the numeric rows (49+3)');
  assert.ok(chkHtml.includes('2 missing') && chkHtml.includes('&lt;evil&gt;'), 'the missing list renders its count, items escaped');
  assert.ok(chkHtml.includes('chk-none'), 'an empty missing list renders 0, not —');
  assert.ok(chkHtml.includes('<bdi dir="ltr">running</bdi>'), 'the status renders verbatim');
  assert.ok(chkHtml.includes('art-dash'), 'null counts render as —');
  const sumPart = chkHtml.slice(chkHtml.indexOf('class="chk-sum"'), chkHtml.indexOf('chk-tblwrap'));
  assert.ok(sumPart.includes('<b>3</b>') && sumPart.includes('56') && sumPart.includes('52'), 'the summary carries the project count and both totals');
  // an ALL-null payload sums honestly to dashes, never to zeros
  apiHandler = () => ({ projects: [{ name: 'a', methods_found: null, methods_added: null, missing: null, status: 'queued' }] });
  await vm.runInContext('loadChecker(true)', sandbox);
  chkHtml = el('chkBody').innerHTML;
  const sumPart2 = chkHtml.slice(chkHtml.indexOf('class="chk-sum"'), chkHtml.indexOf('chk-tblwrap'));
  assert.ok(sumPart2.includes('<b>1</b>') && (sumPart2.match(/—/g) || []).length >= 2, 'an all-null payload sums to dashes — unknown is not zero');
  // a loaded checker does not refetch without force
  apiCalls.length = 0;
  await vm.runInContext('loadChecker()', sandbox);
  assert.equal(apiCalls.length, 0, 'the loaded payload serves without a refetch');
  // XSS discipline on every echoed field
  apiHandler = () => ({ projects: [{ name: '<script>alert(1)</script>', methods_found: 1, methods_added: 1, missing: [], status: '"><b>x</b>' }] });
  await vm.runInContext('loadChecker(true)', sandbox);
  chkHtml = el('chkBody').innerHTML;
  assert.ok(!chkHtml.includes('<script>alert'), 'project names are escaped');
  assert.ok(chkHtml.includes('&lt;script&gt;'), '…rendered as text');
  assert.ok(!chkHtml.includes('status=""><b>'), 'status badges are escaped');
  // the honest empty state
  apiHandler = () => ({ projects: [] });
  await vm.runInContext('loadChecker(true)', sandbox);
  assert.ok(el('chkBody').innerHTML.includes('lists no projects yet'), 'an empty project list says so honestly');
  // an out-of-shape payload is an error, never rendered as data
  apiHandler = () => ({ unexpected: true });
  await vm.runInContext('loadChecker(true)', sandbox);
  chkHtml = el('chkBody').innerHTML;
  assert.ok(chkHtml.includes('Checker error'), 'an out-of-shape payload renders the error state');
  assert.ok(!chkHtml.includes('chk-tbl'), 'no table renders from it');
  assert.ok(el('chkFeed').innerHTML.includes('Checker error'), 'the feed line flags the error');
  console.log('cores_checker_ui: all assertions passed');
})().catch(e => { console.error(e); process.exit(1); });
