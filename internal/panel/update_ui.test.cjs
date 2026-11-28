'use strict';
// VM tests for the redesigned Panel update page (tab-update): the honest
// update-state classification (a v1.32-dev build must NOT read as a normal
// update of the older stable v1.29.0), the version card render per state,
// the review-first dialog, the GitHub token states, and the release-notes
// card. No server or browser required.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const dir = __dirname;
const source = fs.readFileSync(path.join(dir, 'app.js'), 'utf8').replace(new RegExp('\\r\\n', 'g'), '\n');
function section(start, end) {
  const a = source.indexOf(start), b = source.indexOf(end, a + start.length);
  assert.ok(a >= 0 && b > a, 'source anchors: ' + start);
  return source.slice(a, b);
}
const upd = section('// ---------- Panel update (tab-update)', '// ---------- interactive terminal');

const elements = new Map();
function el(id) {
  if (!elements.has(id)) elements.set(id, { id, hidden: false, disabled: false, value: '', type: 'password',
    textContent: '', innerHTML: '', className: '', title: '', open: false, focused: false,
    classList: { _on: new Set(), add(c) { this._on.add(c); }, remove(c) { this._on.delete(c); }, toggle(c, on) { if (on === undefined) { this._on.has(c) ? this._on.delete(c) : this._on.add(c); } else if (on) this._on.add(c); else this._on.delete(c); }, contains(c) { return this._on.has(c); } } });
  const e = elements.get(id);
  e._clicked = 0;
  e.querySelectorAll = () => [];
  e.querySelector = () => null;
  e.focus = () => { e.focused = true; };
  e.setAttribute = () => {};
  e.getAttribute = () => null;
  return e;
}
let toasts = [], apiCalls = [], apiHandler = null;
let modalShown = [], modalHidden = [];
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
  $: id => el(id), el,
  esc,
  tr: (_k, fb) => fb,
  currentLang: 'en',
  BASE: '', at: p => p,
  api: async (p, b, m) => { apiCalls.push({ path: p, body: b, method: m || (b ? 'POST' : 'GET') }); if (!apiHandler) throw new Error('no api handler'); return apiHandler(p, b, m); },
  modalShow: (ov, onClose) => { modalShown.push({ ov: ov && ov.id, onClose }); ov._release = onClose; },
  modalHide: ov => { modalHidden.push(ov && ov.id); if (ov && ov._release) { const r = ov._release; ov._release = null; r(); } },
  entConfirm: async () => true,
  toastOk: (t, s) => toasts.push(['ok', t, s]),
  toastErr: (t, s) => toasts.push(['err', t, s]),
  docTpl: (tpl, args) => { let s = tpl || ''; (args || []).forEach((a, i) => { s = s.split('{' + i + '}').join(a); }); return s; },
  fetch: async () => { throw new Error('no network in test'); },
  mdLitePlaceholder: true,
};
// mdLite is defined inside the section itself; nothing else to inject.
vm.createContext(sandbox);
vm.runInContext(upd, sandbox, { filename: 'update-section.js' });
const run = code => vm.runInContext(code, sandbox);

// ---- 1. the classification matrix (the heart of the redesign) ----
// The spec's headline case: a v1.32-dev build over the stable v1.29.0 is a
// CHANNEL SWITCH, never a normal update.
assert.equal(run('updClassify("v1.32-dev", "v1.29.0").state'), 'switch-stable');
// same family, strictly newer = a real update
assert.equal(run('updClassify("v1.30.0", "v1.31.0").state'), 'normal');
assert.equal(run('updClassify("dev-r100", "dev-r120").state'), 'normal');
// same family, not newer = up to date
assert.equal(run('updClassify("v1.31.0", "v1.31.0").state'), 'none');
assert.equal(run('updClassify("v1.31.0", "v1.30.0").state'), 'none');
// dev build offered the current stable = switch-stable review
assert.equal(run('updClassify("dev-r100", "v1.29.0").state'), 'switch-stable');
// stable build, dev channel's prerelease = channel switch the other way
assert.equal(run('updClassify("v1.29.0", "dev-r150").state'), 'switch-dev');
// unknown build against dev-rN: the relationship cannot be verified at all
assert.equal(run('updClassify("v1.32-dev", "dev-r150").state'), 'unverified');
// no latest at all: honest "check failed" shape
assert.equal(run('updClassify("v1.32-dev", "").failed'), true);

// comparison mirrors the backend exactly (documenting WHY the backend calls
// v1.32-dev older: unknown family ranks below every release)
assert.equal(run('updCompare("v1.29.0", "v1.32-dev")'), 1);
assert.equal(run('updCompare("dev-r120", "dev-r100")'), 1);
assert.equal(run('updCompare("v1.31.0", "v1.30.9")'), 1);
assert.equal(run('updCompare("v1.31.0", "v1.31.0")'), 0);

// build-kind chips: dev suffix is a development build, semver is stable,
// a legacy panel-rN tag shows nothing (never a fabricated channel)
assert.equal(run('updBuildKind("v1.32-dev")'), 'dev');
assert.equal(run('updBuildKind("v1.29.0")'), 'stable');
assert.equal(run('updBuildKind("dev-r150")'), 'dev');
assert.equal(run('updBuildKind("weird-build")'), '');

// reset the fake DOM state without dropping the elements: the section's
// onclick wiring ran once at evaluation time and must survive
function resetUI() {
  for (const [, e] of elements) {
    e.hidden = false; e.disabled = false; e.textContent = ''; e.innerHTML = '';
    e.className = ''; e.value = ''; e.open = false; e.type = 'password';
  }
  toasts = []; apiCalls = []; modalShown = []; modalHidden = [];
}
// ---- 2. the version card renders the switch-stable state honestly ----
async function loadWith(payload, tokenSet) {
  resetUI();
  apiHandler = (p) => {
    if (p.startsWith('/api/version')) return payload;
    if (p.startsWith('/api/update/github-token')) return { set: !!tokenSet };
    if (p.startsWith('/api/releases')) return { channel: 'stable', releases: [] };
    if (p.startsWith('/api/update/peer')) return { configured: false };
    throw new Error('unexpected api path ' + p);
  };
  el('tab-update').hidden = false;
  await run('loadVersion()');
}
(async () => {

// 2a. dev build v1.32-dev vs stable v1.29.0: review-first, amber warning,
// never the old misleading "Update available" action.
await loadWith({ current: 'v1.32-dev', latest: 'v1.29.0', channel: 'stable', update_available: true,
  checked_at: Math.floor(Date.now() / 1000) - 120,
  changes: [{ tag: 'v1.29.0', date: '2026-11-20T10:00:00Z', notes: '## v1.29.0\n\n- Fix update guard\n- Add token states' }] });
assert.equal(el('verCurrent').textContent, 'v1.32-dev');
assert.equal(el('verLatest').textContent, 'v1.29.0');
assert.equal(el('updCurChip').textContent, 'Development');
assert.ok(el('updCurChip').className.includes('dev'), 'installed chip reads Development');
assert.ok(!el('updCurChip').hidden);
assert.equal(el('updAvailChip').textContent, 'Stable');
assert.ok(el('updAvailChip').className.includes('stable'));
assert.equal(el('updAvailLbl').textContent, 'Available stable release');
assert.equal(el('updDotTxt').textContent, 'Stable release available');
assert.equal(el('updateBtn').hidden, false);
assert.equal(el('updateBtn').disabled, false);
assert.equal(el('updateBtn').textContent, 'Review stable switch');
assert.equal(el('updSwitchWarn').hidden, false, 'amber channel-switch warning shown');
assert.ok(el('updSwitchWarnBody').textContent.includes('lower version number'), 'warning names the risk');
assert.ok(el('verRowsUpd').innerHTML.includes('Last successful check'), 'successful check time shown');
assert.ok(!el('updBadge').classList.contains('hidden'), 'nav badge on');
// release notes: version + expandable summary, first list line as the summary
assert.ok(el('updNotes').innerHTML.includes('<details class="upd-rel">'), 'notes collapse by default');
assert.ok(el('updNotes').innerHTML.includes('v1.29.0'));
assert.ok(el('updNotes').innerHTML.includes('Fix update guard'));
assert.equal(el('updNotesEmpty').hidden, true);

// 2b. the review dialog: facts, warning, notes, and the labeled confirm
run('updOpenReview()');
assert.equal(modalShown.length, 1, 'review opens as a dialog');
assert.ok(modalShown[0].ov === 'updReviewModal');
assert.equal(el('updReviewTitle').textContent, 'Review stable switch');
assert.ok(el('updReviewFacts').innerHTML.includes('v1.32-dev'), 'installed version in the review');
assert.ok(el('updReviewFacts').innerHTML.includes('v1.29.0'), 'target version in the review');
assert.ok(el('updReviewFacts').innerHTML.includes('Channel switch — development build to stable release'), 'classification named');
assert.ok(el('updReviewFacts').innerHTML.includes('Automatic rollback'), 'rollback capability stated');
assert.ok(el('updReviewFacts').innerHTML.includes('Tunnel agents'), 'agent impact stated');
assert.equal(el('updReviewWarn').hidden, false, 'review carries the channel warning');
assert.ok(el('updReviewNotes').innerHTML.includes('Fix update guard'), 'release notes in the review');
assert.equal(el('updReviewNotesBox').hidden, false);
assert.equal(el('updReviewGo').textContent, 'Switch to stable');
assert.ok(el('updReviewGo').className.includes('quantum-btn-danger'), 'a channel switch confirms through the danger button');
// confirming runs the update against the REVIEWED tag
apiHandler = (p, b) => { if (p === '/api/update') { throw new Error('stop'); } return {}; };
el('updReviewGo').onclick();
await new Promise(r => setTimeout(r, 0));
assert.ok(apiCalls.some(c => c.path === '/api/update' && c.body && c.body.tag === 'v1.29.0'), 'confirm installs exactly the reviewed tag');

// 2c. normal update: same channel, strictly newer
await loadWith({ current: 'v1.30.0', latest: 'v1.31.0', channel: 'stable', update_available: true, checked_at: 1 });
assert.equal(el('updDotTxt').textContent, 'Update available');
assert.equal(el('updateBtn').textContent, 'Update to v1.31.0');
assert.equal(el('updSwitchWarn').hidden, true, 'no channel warning for a normal update');
assert.ok(el('verRowsUpd').innerHTML.includes('v1.31.0'));

// 2d. up to date
await loadWith({ current: 'v1.29.0', latest: 'v1.29.0', channel: 'stable', update_available: false, checked_at: 1 });
assert.equal(el('updDotTxt').textContent, 'You\'re up to date');
assert.equal(el('updateBtn').hidden, true, 'no update action');
assert.ok(el('updBadge').classList.contains('hidden'), 'nav badge off');
await loadWith({ current: 'v1.31.0', latest: 'v1.30.0', channel: 'stable', update_available: false });
assert.equal(el('updDotTxt').textContent, 'You\'re up to date', 'an older latest release is still "up to date"');

// 2e. unverified path: visible but DISABLED action
await loadWith({ current: 'v1.32-dev', latest: 'dev-r150', channel: 'dev', update_available: true, checked_at: 1 });
assert.equal(el('updDotTxt').textContent, 'Update path unverified');
assert.equal(el('updateBtn').hidden, false);
assert.equal(el('updateBtn').disabled, true, 'unverified path stays disabled');
run('updOpenReview()');
assert.equal(modalShown.length, 0, 'the review dialog refuses to open for an unverified path');

// 2f. failed check: honest absence, attempted-check time, no invented release
await loadWith({ current: 'v1.32-dev', latest: '', channel: 'stable', update_available: false,
  error: 'github api: 404 — the token cannot see Dark-modem/quantum', checked_at: Math.floor(Date.now() / 1000) - 300 }, true);
assert.equal(el('verLatest').textContent, '—');
assert.equal(el('updDotTxt').textContent, 'Check failed');
assert.ok(el('verRowsUpd').innerHTML.includes('Last attempted check'), 'attempted-check time shown when the check failed');
assert.ok(!el('verRowsUpd').innerHTML.includes('Last successful check'), 'no fabricated success time');
assert.equal(el('updNotesEmpty').hidden, false, 'honest empty notes state');
assert.equal(el('ghtokBadge').textContent, 'Permission insufficient', '404 with a stored token = permission problem');

// ---- 3. GitHub token states ----
async function tokenWith(set, v) {
  apiHandler = (p) => {
    if (p.startsWith('/api/update/github-token')) return { set };
    if (p.startsWith('/api/version')) return v;
    if (p.startsWith('/api/releases')) return { channel: 'stable', releases: [] };
    if (p.startsWith('/api/update/peer')) return { configured: false };
    throw new Error('unexpected api path ' + p);
  };
  await run('loadVersion(true)');
}
await tokenWith(false, { current: 'v1.29.0', latest: 'v1.29.0', channel: 'stable', update_available: false });
assert.equal(el('ghtokBadge').textContent, 'Not configured');
assert.ok(el('ghtokBadge').className.includes('warn'));
assert.equal(el('ghtokEntry').hidden, false, 'input visible when not configured');
assert.equal(el('ghtokSet').hidden, true, 'replace/remove hidden when not configured');
await tokenWith(true, { current: 'v1.29.0', latest: 'v1.29.0', channel: 'stable', update_available: false });
assert.equal(el('ghtokBadge').textContent, 'Connected');
assert.ok(el('ghtokBadge').className.includes('ok'));
assert.equal(el('ghtokEntry').hidden, true, 'a saved token is never shown or re-typed by default');
assert.equal(el('ghtokSet').hidden, false, 'Replace/Remove available when configured');
// invalid / insufficient / network-down map from the backend's own wording
assert.equal(run('updTokenState(true, {error: "github api: 401 — the access token is invalid or expired"}).txt'), 'Invalid token');
assert.equal(run('updTokenState(true, {error: "github api: 404 — the token cannot see Dark-modem/quantum"}).txt'), 'Permission insufficient');
assert.equal(run('updTokenState(true, {error: "github api: 403 — the token lacks the permission or the API rate limit was hit"}).txt'), 'Permission insufficient');
assert.equal(run('updTokenState(true, {error: "dial tcp: connection refused"}).txt'), 'Connection failed');
// replace reveals a fresh input, focused and empty
el('ghtokReplaceBtn').onclick();
assert.equal(el('ghtokEntry').hidden, false, 'Replace reveals the input');
assert.equal(el('ghtokInput').value, '', 'Replace starts from an empty input');
// show/hide toggles the password type
assert.equal(el('ghtokInput').type, 'password');
el('ghtokEye').onclick();
assert.equal(el('ghtokInput').type, 'text');
el('ghtokEye').onclick();
assert.equal(el('ghtokInput').type, 'password');

// ---- 4. saving the token never echoes it back ----
resetUI();
apiHandler = (p, b) => {
  if (p === '/api/update/github-token' && b) return { status: 'ok', detail: 'token saved — update checks use it from now on' };
  if (p.startsWith('/api/version')) return { current: 'v1.29.0', latest: 'v1.29.0', channel: 'stable', update_available: false };
  if (p.startsWith('/api/releases')) return { channel: 'stable', releases: [] };
  if (p.startsWith('/api/update/peer')) return { configured: false };
  throw new Error('unexpected api path ' + p);
};
el('ghtokInput').value = 'github_pat_test1234567890ABC';
el('ghtokSaveBtn').onclick();
await new Promise(r => setTimeout(r, 0));
const tokCall = apiCalls.find(c => c.path === '/api/update/github-token');
assert.ok(tokCall && tokCall.body.token === 'github_pat_test1234567890ABC', 'token sent once to the storage endpoint');
assert.equal(el('ghtokInput').value, '', 'input cleared after save');
assert.ok(!el('ghtokMsg').textContent.includes('github_pat_test'), 'the token value never comes back');
assert.equal(el('ghtokMsg').textContent, 'token saved — update checks use it from now on');

// ---- 5. rollback notice + peer row still render ----
await loadWith({ current: 'v1.29.0', latest: 'v1.29.0', channel: 'stable', update_available: false,
  rolled_back: 'v1.31.0', checked_at: 1 });
assert.equal(el('updRollback').hidden, false);
assert.ok(el('updRollback').textContent.includes('v1.31.0'), 'rollback notice names the failed version');

console.log('update_ui: all green');
})().catch(e => { console.error(e); process.exit(1); });
