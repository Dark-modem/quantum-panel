'use strict';
// VM tests for the Cores artifact store (contract §2.22): repo-vs-upload
// request assembly, the repo-newer indicator, null → — rendering and the
// honest feed states. No server or browser required.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8').replace(new RegExp('\\r\\n', 'g'), '\n');
function section(start, end) {
  const a = source.indexOf(start), b = source.indexOf(end, a + start.length);
  assert.ok(a >= 0 && b > a, 'source anchors: ' + start);
  return source.slice(a, b);
}
// the engine name/alias consts the artifact store shares with the grid
const consts = section('const CORES_ENGINE_IMG', 'async function loadCores(force)');
// the whole artifact store block, from its banner to end of file
const art = source.slice(source.indexOf('// ===== Cores artifact store (§2.22)'));
assert.ok(art.length > 1000, 'artifact store section anchors');
const elements = new Map();
function el(id) {
  if (!elements.has(id)) elements.set(id, { id, hidden: false, disabled: false, value: '', files: null,
    textContent: '', innerHTML: '', title: '',
    classList: { _on: new Set(), add(c) { this._on.add(c); }, remove(c) { this._on.delete(c); }, toggle(c, on) { if (on) this._on.add(c); else this._on.delete(c); }, contains(c) { return this._on.has(c); } },
    setAttribute() {}, getAttribute() { return null; }, addEventListener() {}, focus() {},
    querySelectorAll() { return []; }, querySelector() { return null; }, appendChild() {} });
  return elements.get(id);
}
let toasts = [], coreLoads = [], formPosts = [];
class FormData {
  constructor() { this._ = []; }
  append(k, v) { this._.push([k, (v && typeof v === 'object') ? '{file:' + (v.name || 'bin') + '}' : String(v)]); }
  get(k) { const f = this._.find(x => x[0] === k); return f ? f[1] : null; }
  has(k) { return this._.some(x => x[0] === k); }
}
let fetchBody = { status: 'ok', id: 1, artifact: 'frp-0.61.2-linux-amd64.tar.gz', source: 'repo', tag: 'frp-v0.61.2' };
const sandbox = {
  assert, console, FormData, JSON, Math, Date, String, Number, isNaN, encodeURIComponent,
  document: {
    activeElement: null, body: { appendChild() {} },
    getElementById: id => el(id),
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
  api: async () => { throw new Error('api() is not used on the artifact paths'); },
  fetch: async (url, opts) => { formPosts.push({ path: url, body: opts && opts.body }); return { status: 200, ok: true, text: async () => JSON.stringify(fetchBody), json: async () => fetchBody }; },
  getCSRFToken: () => 'tok', setCSRFToken() {}, showLogin() {},
  userActiveAt: Date.now() - 1000,
  toastOk: (t, s) => toasts.push(['ok', t, s]),
  toastErr: (t, s) => toasts.push(['err', t, s]),
  loadCores: force => coreLoads.push(force),
  renderCores() {},
  loadArtStore() {}, loadCoresAvailable() {},
  modalShow() {}, modalHide(ov) { if (ov) ov.hidden = true; },
  entConfirm: async () => true,
};
vm.createContext(sandbox);
vm.runInContext(consts + '\n' + art, sandbox);

(async () => {
  // ---- version comparison + the repo-newer indicator -----------------------
  vm.runInContext(`
  assert.equal(verCmp('0.61.2', '0.61.2'), 0, 'equal versions compare zero');
  assert.ok(verCmp('0.61.10', '0.61.2') > 0, 'numeric compare puts 0.61.10 above 0.61.2');
  assert.ok(verCmp('20230206.0', '0.61.2') > 0, 'calver rows order above old semver');
  assert.ok(verCmp('0.60.0', '0.61.2') < 0, 'older semver orders below');
  coresAvail = { cores: [
    { core: 'frp', latest_tag: 'frp-v0.61.2', latest_version: '0.61.2', assets: [] },
    { core: 'wstunnel', latest_tag: 'wstunnel-v1.2.0', latest_version: '1.2.0', assets: [] },
  ] };
  assert.equal(artRepoRow('frp').latest_version, '0.61.2', 'repo row found by engine');
  assert.equal(artRepoRow('wst').latest_version, '1.2.0', 'the wst alias resolves to the wstunnel repo row');
  assert.equal(artRepoRow('matrix'), null, 'matrix is in-process and never appears in the repo feed');
  assert.equal(artRepoNewer({ engine: 'frp', version: '0.60.0' }), '0.61.2', 'older stored version flags the repo latest');
  assert.equal(artRepoNewer({ engine: 'frp', version: '0.61.2' }), '', 'matching version shows no indicator');
  assert.equal(artRepoNewer({ engine: 'frp', version: '0.62.0' }), '', 'newer stored version shows no indicator');
  assert.equal(artRepoNewer({ engine: 'frp', version: '' }), '', 'missing version never flags');
  assert.equal(artRepoNewer({ engine: 'nowhere', version: '1.0.0' }), '', 'unknown engine never flags');

  assert.equal(artAutoArch(['arm64', 'amd64']), 'amd64', 'no agent hints fall back to amd64');
  assert.equal(artAutoArch(['arm64']), 'arm64', 'a single listed arch is taken as-is');
  window.__coresAgents = [{ arch: 'arm64' }, { arch: 'arm64' }, { arch: 'amd64' }];
  assert.equal(artAutoArch(['amd64', 'arm64']), 'arm64', 'the enrolled agents majority arch wins');
  window.__coresAgents = [{ arch: 'amd64' }];
  assert.equal(artAutoArch(['arm64', 'amd64']), 'amd64', 'exactly one agent picks its own arch');
  assert.equal(artAutoArch([]), '', 'no listed arch yields no pick');

  assert.equal(artSize(null), '—', 'null size renders the em dash');
  assert.equal(artSize(0), '—', 'zero size renders the em dash');
  assert.ok(artSize(5 * 1048576).endsWith('MB'), 'bytes render as MB');
  assert.equal(artDate(0), '—', 'zero upload time renders the em dash');
  assert.equal(artDateISO(''), '—', 'missing published_at renders the em dash');
  `, sandbox);

  // ---- row rendering --------------------------------------------------------
  vm.runInContext(`
  const newer = artRow({ id: 3, engine: 'frp', version: '0.60.0', goos: 'linux', goarch: 'amd64', size: 10485760, source: 'repo', uploaded_at: 1700000000 });
  assert.ok(newer.includes('Newer in repo'), 'repo-ahead row carries the newer chip');
  assert.ok(newer.includes('v0.61.2'), 'the chip names the repo version');
  assert.ok(newer.includes('eng-badge-repo'), 'repo provenance wears the repo badge');
  assert.ok(newer.includes('data-art-replace="3"') && newer.includes('data-art-del="3"'), 'row actions carry the escaped id');
  const older = artRow({ id: 4, engine: 'frp', version: '0.61.2', goos: 'linux', goarch: 'amd64', size: 10485760, source: 'upload', uploaded_at: 0 });
  assert.ok(!older.includes('eng-repo-chip'), 'up-to-date row shows no chip');
  assert.ok(older.includes('art-dash'), 'up-to-date row renders the em dash placeholder');
  assert.ok(older.includes('eng-badge-upl'), 'upload provenance wears the upload badge');
  assert.ok(older.includes('—'), 'null data renders as —');
  const bare = artRow({ id: 5, engine: 'xray', version: null, goos: null, goarch: null, size: null, source: 'upload', uploaded_at: null });
  assert.ok(bare.includes('<bdi dir="ltr">—</bdi>'), 'null version renders the em dash inside bdi');
  assert.ok(bare.includes('— · —'), 'null os/arch render as — · —');
  const evil = artRow({ id: 6, engine: '<script>', version: '<x>', goos: 'linux', goarch: 'amd64', source: 'repo' });
  assert.ok(!evil.includes('<script>'), 'engine id is escaped');
  assert.ok(evil.includes('&lt;script&gt;'), 'engine id renders escaped');
  `, sandbox);

  // ---- list states ----------------------------------------------------------
  vm.runInContext(`
  artLoading = false; artError = null; artList = [];
  renderArtStore();
  assert.ok(el('engArtList').innerHTML.includes('No artifacts stored yet.'), 'empty store shows its honest empty state');
  artError = 'boom'; artList = null;
  renderArtStore();
  assert.ok(el('engArtList').innerHTML.includes('Artifact store error') && el('engArtList').innerHTML.includes('data-art-retry'), 'load failure shows the error box with a retry');
  artError = null; artList = [
    { id: 3, engine: 'frp', version: '0.60.0', goos: 'linux', goarch: 'amd64', size: 10485760, source: 'repo', uploaded_at: 1700000000 },
    { id: 4, engine: 'xray', version: '1.0.0', goos: 'linux', goarch: 'arm64', size: null, source: 'upload', uploaded_at: 0 },
  ];
  window.__engKeyCfg = false;
  renderArtStore();
  let html = el('engArtList').innerHTML;
  assert.ok(html.includes('E-ENG-03'), 'unconfigured signing key surfaces its honest warning');
  assert.ok(html.includes('<table'), 'rows render as a table');
  assert.ok(html.includes('eng-badge-repo') && html.includes('eng-badge-upl'), 'both provenance badges render');
  assert.ok(html.includes('Source') && html.includes('Repo'), 'the header row names the provenance and repo columns');
  window.__engKeyCfg = true;
  renderArtStore();
  assert.ok(!el('engArtList').innerHTML.includes('E-ENG-03'), 'configured key hides the warning');

  coresAvailBusy = true; renderArtStore();
  assert.ok(el('engArtFeed').innerHTML.includes('Checking repo'), 'busy feed says it is checking');
  coresAvailBusy = false; coresAvailErr = 'feed down'; renderArtStore();
  assert.ok(el('engArtFeed').classList.contains('bad'), 'feed error marks the line bad');
  coresAvailErr = null; coresAvailAt = 1700000000; renderArtStore();
  assert.ok(el('engArtFeed').innerHTML.includes('Repo checked'), 'successful check stamps the feed line');
  `, sandbox);

  // ---- request assembly (the §2.22 multipart contract) -----------------------
  vm.runInContext(`
  artEdit = null; artBusy = false; artMode = 'repo';
  artPick = { core: 'frp', version: '', arch: 'amd64' };
  `, sandbox);
  await vm.runInContext('artModalSubmit()', sandbox);
  let post = formPosts[formPosts.length - 1];
  let fd = post && post.body;
  assert.equal(post.path, '/api/ui/engines/artifacts', 'repo submit posts the artifact endpoint');
  assert.equal(fd.get('source'), 'repo', 'repo path sets source=repo');
  assert.equal(fd.get('engine'), 'frp', 'repo path names the core');
  assert.equal(fd.get('goos'), 'linux', 'repo path pins linux');
  assert.equal(fd.get('goarch'), 'amd64', 'repo path carries the picked arch');
  assert.equal(fd.get('version'), null, 'empty version = the core latest, field omitted');
  assert.equal(fd.has('name'), false, 'repo path never sends a download name (E-IN-06 guard)');
  assert.equal(fd.has('signature'), false, 'repo path never sends a signature (E-IN-06 guard)');
  assert.equal(fd.has('file'), false, 'repo path never sends a file field (E-IN-06 guard)');
  assert.ok(toasts.some(t => t[0] === 'ok' && t[2].includes('frp-0.61.2-linux-amd64.tar.gz') && t[2].includes('frp-v0.61.2')), 'success toast names the artifact and its tag');
  assert.ok(coreLoads[coreLoads.length - 1] === true, 'the grid reloads after a successful save');
  assert.equal(el('artStoreModal').hidden, true, 'the modal closes after success');

  vm.runInContext(`
  artBusy = false; artMode = 'repo';
  artPick = { core: 'frp', version: '0.60.1', arch: 'arm64' };
  `, sandbox);
  await vm.runInContext('artModalSubmit()', sandbox);
  fd = formPosts[formPosts.length - 1].body;
  assert.equal(fd.get('version'), '0.60.1', 'an exact older version is sent verbatim');
  assert.equal(fd.get('goarch'), 'arm64', 'exact version keeps the picked arch');

  vm.runInContext(`
  artBusy = false; artMode = 'repo';
  artPick = { core: '', version: '', arch: 'amd64' };
  el('artErr').textContent = ''; el('artErr').hidden = true;
  `, sandbox);
  const postsBefore = formPosts.length;
  await vm.runInContext('artModalSubmit()', sandbox);
  assert.equal(formPosts.length, postsBefore, 'no core selected → nothing posts');
  assert.equal(el('artErr').hidden, false, 'validation error becomes visible');
  assert.ok(el('artErr').textContent.includes('core'), 'validation error names the missing core');

  vm.runInContext(`
  artBusy = false; artMode = 'upload'; artEdit = null;
  el('artUpEngine').value = 'chisel';
  el('artUpVersion').value = '1.10.1';
  el('artUpArch').value = 'amd64';
  el('artUpName').value = '';
  el('artUpSig').value = 'MEUCIQ==';
  el('artUpFile').files = [{ name: 'chisel.tar.gz' }];
  el('artErr').hidden = true;
  `, sandbox);
  await vm.runInContext('artModalSubmit()', sandbox);
  fd = formPosts[formPosts.length - 1].body;
  assert.equal(fd.get('source'), 'upload', 'upload path sets source=upload');
  assert.equal(fd.get('engine'), 'chisel', 'upload path names the core');
  assert.equal(fd.get('version'), '1.10.1', 'upload path requires the version');
  assert.equal(fd.get('goos'), 'linux', 'upload path pins linux');
  assert.equal(fd.get('signature'), 'MEUCIQ==', 'upload path carries the signature');
  assert.ok(fd.get('file').includes('chisel.tar.gz'), 'upload path attaches the file');
  assert.equal(fd.has('name'), false, 'empty name stays omitted');

  vm.runInContext(`
  artBusy = false; artMode = 'upload';
  el('artUpEngine').value = 'Chisel';
  el('artUpSig').value = '';
  el('artUpFile').files = [];
  el('artErr').hidden = true;
  `, sandbox);
  const postsBefore2 = formPosts.length;
  await vm.runInContext('artModalSubmit()', sandbox);
  assert.equal(formPosts.length, postsBefore2, 'invalid engine id + missing signature + no file → nothing posts');
  assert.equal(el('artErr').hidden, false, 'upload validation error becomes visible');
  assert.ok(el('artErr').textContent.includes('lowercase'), 'the engine id rule is explained to the operator');
  console.log('cores_art_ui: all assertions passed');
})().catch(e => { console.error(e && e.stack || e); process.exit(1); });
