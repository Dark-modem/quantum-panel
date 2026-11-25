'use strict';
// VM tests for the ui-ux-2 mesh tunnel-creation wizard: the per-core method
// catalogs surface in the creation flow. Runs the meshtabs.js IIFE against
// a stub document (every boot step is try/catch-wrapped, so a minimal DOM
// stub is enough) and drives the window.__tuPure resolver surface plus the
// static markup the wizard renders.
// No server or browser required.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const dir = __dirname;
const source = fs.readFileSync(path.join(dir, 'meshtabs.js'), 'utf8').replace(new RegExp('\\r\\n', 'g'), '\n');

// ---- stub DOM ------------------------------------------------------------
const elements = new Map();
function el(id) {
  if (!elements.has(id)) elements.set(id, {
    id, hidden: false, disabled: false, value: '', textContent: '', innerHTML: '', title: '',
    style: { setProperty() {} },
    classList: { _on: new Set(), add(c) { this._on.add(c); }, remove(c) { this._on.delete(c); }, toggle(c, on) { on ? this._on.add(c) : this._on.delete(c); }, contains(c) { return this._on.has(c); } },
    setAttribute() {}, getAttribute() { return null; }, addEventListener() {}, focus() {},
    insertAdjacentElement() {}, insertAdjacentHTML() {}, appendChild() {}, remove() {},
    querySelector() { return null; }, querySelectorAll() { return []; },
  });
  return elements.get(id);
}
const sandbox = {
  console, JSON, Math, Date, String, Number, Boolean, Array, Object, Promise, Symbol,
  isNaN, isFinite, parseInt, parseFloat, encodeURIComponent, decodeURIComponent,
  setTimeout() { return 0; }, setInterval() { return 0; }, clearTimeout() {}, clearInterval() {},
  document: {
    readyState: 'complete', cookie: '',
    documentElement: { getAttribute() { return null; }, lang: 'en', dir: 'ltr' },
    body: { classList: { contains() { return false; }, add() {}, remove() {} }, appendChild() {} },
    getElementById: id => el(id),
    querySelector() { return null; }, querySelectorAll() { return []; },
    createElement() { return el('created'); }, addEventListener() {},
  },
  location: { hash: '' },
  window: {}, navigator: {}, history: {},
  localStorage: { getItem() { return null; }, setItem() {} },
  fetch() { return Promise.reject(new Error('no net')); },
  crypto: { getRandomValues() {} },
  MutationObserver: function () { this.observe = function () {}; },
};
sandbox.self = sandbox;
vm.createContext(sandbox);
vm.runInContext(source, sandbox, { filename: 'meshtabs.js' });

const P = sandbox.window.__tuPure;
assert.ok(P, 'the pure wizard helpers are exposed for the VM tests');

// ---- fixture: a §2.22-shaped cores feed -----------------------------------
// ctunnel 2 methods (real shapes from the manifest), gost known-but-pending
const CORES = { cores: [
  { core: 'ctunnel', latest_tag: 'ctunnel-v1.1.1', latest_version: '1.1.1', installed_version: '', outdated: null,
    methods: [
      { id: 7, name: 'quic-v1', title: 'QUIC transport', layer: 'l4', transport: 'udp', ipver: 'v4+v6', overhead: 34, needs_port: true, userspace: true, mikrotik: false, family: 'stealth' },
      { id: 8, name: 'dtls-1.3', title: 'DTLS 1.3 transport', layer: 'l4', transport: 'udp', ipver: 'v4', overhead: 40, needs_port: true, userspace: true, mikrotik: false, family: 'stealth' },
    ] },
  { core: 'gost', latest_tag: 'gost-v3.3.3', latest_version: '3.3.3', installed_version: '', outdated: null, methods: null },
] };

// ---- resolver: the cores-feed join ----------------------------------------
assert.equal(P.tuCoreOf('wst'), 'wstunnel', 'wst aliases to the wstunnel core');
assert.equal(P.tuCoreOf('singbox'), 'sing-box', 'singbox aliases to the sing-box core');
assert.equal(P.tuCoreOf('gost'), 'gost', 'identity cores pass through');
const ctRow = P.tuCoreRow(CORES, 'ctunnel');
assert.ok(ctRow && ctRow.latest_version === '1.1.1', 'the ctunnel row resolves');
assert.equal(P.tuCoreRow(CORES, 'matrix'), null, 'panel-internal engines have no core row');
const ctMethods = P.tuCoreMethods(CORES, 'ctunnel');
assert.ok(Array.isArray(ctMethods) && ctMethods.length === 2, 'ctunnel serves its methods[]');
assert.equal(P.tuCoreMethods(CORES, 'gost'), null, 'a core without methods[] honestly resolves null');
assert.equal(P.tuCoreMethods(CORES, 'wst'), null, 'wst maps to the wstunnel row (none in this fixture)');

// ---- resolver: the params chain -------------------------------------------
const schema = { service: '|tap|redirect-tcp|redirect-udp|sni|masque', tap_mtu: 1420, tap_gw: '', insecure: false, forwards: ['host:port'] };
const sf = P.tuSchemaFields(schema);
const byK = Object.fromEntries(sf.map(f => [f.json, f]));
assert.equal(byK.service.kind, 'enum', 'a |pipe| hint converts to an enum');
assert.equal(JSON.stringify(byK.service.options), JSON.stringify(['tap', 'redirect-tcp', 'redirect-udp', 'sni', 'masque']), 'the gost service enums stay verbatim');
assert.equal(byK.tap_mtu.kind, 'number', 'numbers convert');
assert.equal(byK.insecure.kind, 'bool', 'booleans convert');
assert.equal(byK.tap_gw.kind, 'string', 'empty hints stay strings');
assert.ok(byK.forwards.hint && /JSON array/.test(byK.forwards.hint), 'arrays declare their JSON shape honestly');
assert.equal(P.tuSchemaFields(null).length, 0, 'no schema → no fields');
assert.equal(P.tuSchemaFields('nonsense').length, 0, 'a non-object schema yields nothing');
assert.equal(P.tuSchemaFields({ ok: '' }).length, 1, 'empty-string hints are plain strings');

// the union: the panel field wins on collision (labels + secret discipline)
const tplFields = [
  { json: 'service', label_en: 'Service', label_fa: 'سرویس', kind: 'string' },
  { json: 'token', label_en: 'Token', kind: 'secret', generator: 'secret32', required: true },
];
const union = P.tuUnionFields(sf, tplFields);
const uSvc = union.find(f => f.json === 'service');
assert.equal(uSvc.kind, 'string', 'the panel field wins a key collision (its kind stands)');
assert.equal(uSvc.label_en, 'Service', 'the panel field keeps its label');
assert.ok(union.find(f => f.json === 'tap_mtu'), 'schema-only keys append');

// the full resolver: per-method catalog beats everything
const res1 = P.tuMethodParamFields({ name: 'quic-v1', params: [{ json: 'mtu', kind: 'number' }] }, { schema, fields: tplFields });
assert.equal(res1.src, 'method-catalog', 'the method params[] contract wins');
assert.equal(JSON.stringify(res1.fields.map(f => f.json)), JSON.stringify(['mtu']), 'nothing else leaks in');
// template per-method entry
const res2 = P.tuMethodParamFields({ name: 'quic-v1' }, { methods: { 'quic-v1': [{ json: 'congestion', kind: 'enum', options: ['bbr', 'cubic'] }] }, schema, fields: tplFields });
assert.equal(res2.src, 'method-catalog', 'the template methods[methodId] contract counts as the catalog');
// schema + engine fields
const res3 = P.tuMethodParamFields(null, { schema, fields: tplFields });
assert.equal(res3.src, 'catalog', 'the agent schema rides when served');
assert.ok(res3.fields.length > tplFields.length, 'the union carries both sources');
// engine fallback only
const res4 = P.tuMethodParamFields(null, { fields: tplFields });
assert.equal(res4.src, 'engine', 'the live templates are the engine fallback');
// nothing available → honest null
assert.equal(P.tuMethodParamFields(null, null), null, 'no source → null, never invented fields');
// malformed fields filtered
const res5 = P.tuMethodParamFields(null, { fields: [{ json: '', kind: 'string' }, { json: 'weird', kind: 'blob' }, { json: 'fine', kind: 'number' }] });
assert.equal(res5.src, 'engine', 'the live templates are the engine fallback (filtered)');
assert.equal(JSON.stringify(res5.fields.map(f => f.json)), JSON.stringify(['fine']), 'malformed fields never render');
// the pinned method replaces the engine's own free-text method field
assert.equal(JSON.stringify(P.tuDropMethodField([{ json: 'method' }, { json: 'carrier' }]).map(f => f.json)), JSON.stringify(['carrier']), 'the method pin replaces the free-text field');

// ---- value coercion + collection ------------------------------------------
assert.equal(P.tuParamValue({ kind: 'number' }, ' 1420 '), 1420, 'numbers parse');
assert.equal(P.tuParamValue({ kind: 'number' }, ''), undefined, 'an empty number drops out');
assert.equal(P.tuParamValue({ kind: 'number' }, 'abc'), undefined, 'a non-number drops out');
assert.equal(P.tuParamValue({ kind: 'bool' }, 'true'), true, 'bools coerce');
assert.equal(P.tuParamValue({ kind: 'secret' }, ''), undefined, 'a masked-empty secret keeps the stored value (never an empty overwrite)');
assert.equal(P.tuParamValue({ kind: 'secret' }, 's3cret'), 's3cret', 'a typed secret rides');
assert.equal(P.tuParamValue({ kind: 'enum', options: ['tap'] }, 'tap'), 'tap', 'an enum option rides');
assert.equal(P.tuParamValue({ kind: 'enum', options: ['tap'] }, 'evil'), undefined, 'a non-option is dropped');
assert.equal(JSON.stringify(P.tuParamCollect(
  [{ json: 'tap_mtu', kind: 'number' }, { json: 'service', kind: 'enum', options: ['tap'] }, { json: 'pw', kind: 'secret' }],
  { tap_mtu: '1420', service: 'tap', pw: '' })), JSON.stringify({ tap_mtu: 1420, service: 'tap' }), 'the collected params omit the empty secret');
assert.equal(JSON.stringify(P.tuParamCollect([{ json: 'pw', kind: 'secret' }], {})), '{}', 'an empty form collects nothing');

// ---- static markup the wizard renders --------------------------------------
assert.ok(source.includes("data-tupick=\"method\""), 'the wizard renders method pick cards');
assert.ok(source.includes("tuMethodDefault"), 'the engine-default card exists');
assert.ok(source.includes("tuMethodsPending"), 'the catalog-pending mark exists');
assert.ok(source.includes("tuMethodsCount"), 'the {n} methods sub-line exists');
assert.ok(source.includes("tuParamsSchemaNote"), 'the honest no-schema note exists');
assert.ok(source.includes("tuCoresFeedErr") && source.includes("data-tucores-retry"), 'the feed error state carries a retry');
assert.ok(source.includes("tuMethodCatHint"), 'the catalog hint names the cores feed');
assert.ok(source.includes("tuMcatHint") || source.includes("tu-mcat-hint"), 'the hint class renders');
assert.ok(source.includes("engine_params"), 'the create payload carries engine_params');
assert.ok(source.includes("api/cores/available"), 'the wizard reads the §2.22 feed');
assert.ok(source.includes("api/mesh/engines/templates"), 'the wizard reads the engines-catalog templates');
assert.ok(source.includes("TOTAL = 6"), 'the wizard runs six steps (method & parameters is step 5)');
assert.ok(source.includes("tuStMethod"), 'the method step carries its label');
assert.ok(source.includes("method: sel.method"), 'the pinned method rides the payload');
assert.ok(source.includes("data-pgen") && source.includes("crypto.getRandomValues"), 'secret generation stays in the browser');
assert.ok(source.includes("autocomplete=\"new-password\""), 'secret inputs never autocomplete');
assert.ok(source.includes("ctunnel: 'C-Tunnel'") && source.includes("vortexl2: 'VortexL2'") && source.includes("naive: 'Naive'"), 'the engine list carries every cores catalog entry');
assert.ok(source.includes("tuCoreAlias") || source.includes("TU_CORE_ALIAS"), 'the wizard mirrors the engine→core alias map');
assert.ok(source.includes("singbox: 'sing-box'"), 'the singbox→sing-box alias rides in the wizard too');
// XSS discipline: the method card builder escapes every echoed field
assert.ok(source.includes("esc(name)") && source.includes("esc(sub)"), 'method names/titles are escaped');
assert.ok(source.includes("esc(v)") , 'param values are escaped');
// the __tuPure hook does not leak test-only strings into user markup
assert.ok(!source.includes('__tuPure') || source.indexOf('__tuPure') > source.indexOf('function boot'), 'the test hook sits at the tail');

console.log('meshtabs_wizard_ui: all assertions passed');
