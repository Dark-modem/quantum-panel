'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');
const code = fs.readFileSync(path.join(__dirname, 'tunnel_overview.js'), 'utf8');
const app = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');
const esc = app.match(/^const esc = .*$/m)[0];
const matrixStart = app.indexOf('function matrixSummary(mx)');
const matrixCode = app.slice(matrixStart, app.indexOf('function dashboardModel(', matrixStart));

function fixture({ selected = null, local = {}, peers = [], matrix, bagher, xray, dash = {} } = {}) {
  const elements = Object.fromEntries(['tunnelOverviewHeader', 'tunnelOverviewDetails', 'roleLine', 'heroTopo', 'topologyGraph'].map(id => [id, {
    hidden: true, innerHTML: id.startsWith('tunnelOverview') ? '' : 'untouched', classes: new Set(),
    classList: { toggle(name, on) { on ? elements[id].classes.add(name) : elements[id].classes.delete(name); } },
  }]));
  const window = { _lastLocal: local, _lastDash: dash, _matrixState: matrix };
  const context = vm.createContext({ window, $: id => elements[id] || null, tr: (_key, value) => value, fmtBytes: n => n + ' B',
    api() { throw new Error('presentation must not call APIs'); }, document: { addEventListener() { throw new Error('no event setup'); } } });
  vm.runInContext(esc + matrixCode + '\nlet _tunnelDetail, tunPeerCache, _sshfwdData, _xray;', context);
  context.input = { selected, peers, bagher, xray };
  vm.runInContext('_tunnelDetail = input.selected; tunPeerCache = input.peers; _sshfwdData = input.bagher; _xray = input.xray;', context);
  delete context.input;
  vm.runInContext(code, context);
  return { context, window, elements, render: kind => window.renderTunnelOverview(kind), html: () => elements.tunnelOverviewHeader.innerHTML + elements.tunnelOverviewDetails.innerHTML,
    select(value) { context.nextSelection = value; vm.runInContext('_tunnelDetail = nextSelection;', context); delete context.nextSelection; } };
}
const main = () => ({ role: 'iran', engine: 'frp', name: 'main-remote', local_pub: '192.0.2.1', remote_pub: '198.51.100.1',
  gre: { exists: true, inner: '10.10.10.1/30' }, frp_up: true, frp_svc: 'frps', frp_port: 25000, health_state: 'healthy', ping_ok: true,
  ping_ms: '20 ms', proxy_ports: [443, 8443], rx: 100, tx: 200 });
const peer = id => ({ id, name: 'peer-' + id, local_pub: '192.0.2.' + id, remote_pub: '198.51.100.' + id, engine: 'backhaul', no_gre: true,
  frp_up: true, linked: true, health_state: 'healthy', frps_svc: 'backhaul-server-' + id, frp_port: 26000 + id,
  raw_ports: '1000-1005, ' + (3000 + id), rx: id * 111, tx: id * 222, latency_ms: id * 10 });
const mx = (role, service, ping) => ({ enabled: true, role, service, family: 'ipv6', port: 2097,
  addr: '2001:db8::1', ip: role === 'hub' ? '10.254.231.1' : '10.254.231.2', ports: [443, 8080],
  peers: role === 'hub' ? [{ name: 'Foreign Matrix', addr: '2001:db8::2', ip: '10.254.231.2', ping_ok: ping }] : [],
  hub_ping_ok: ping, rx: 777, tx: 888, latency_ms: 12 });

test('no selection, explicit null, and mismatched kind hide and clear both cards', () => {
  const f = fixture({ local: main() });
  assert.equal(f.render('main'), null);
  f.select('main'); assert.ok(f.render());
  assert.equal(f.render(2), null);
  for (const id of ['tunnelOverviewHeader', 'tunnelOverviewDetails']) {
    assert.equal(f.elements[id].hidden, true); assert.equal(f.elements[id].innerHTML, '');
  }
  f.render(); f.select(null); assert.equal(f.render(), null);
  assert.equal(f.elements.tunnelOverviewHeader.classes.has('td-keep'), false);
  assert.equal(f.elements.tunnelOverviewHeader.classes.has('td-hide'), true);
});

test('main shows its exact endpoint, ports, inner IP, latency and counters without touching dashboard/topbar', () => {
  const f = fixture({ selected: 'main', local: main() });
  const m = f.render();
  assert.equal(m.remoteIP, '198.51.100.1'); assert.equal(m.engine, 'frp'); assert.equal(m.latency, '20 ms');
  for (const value of ['10.10.10.1/30', '25000', '443', '8443', '100 B', '200 B', 'GRE + FRP']) assert.ok(f.html().includes(value), value);
  assert.equal(f.elements.tunnelOverviewHeader.hidden, false);
  assert.equal((f.elements.tunnelOverviewHeader.innerHTML.match(/class="tv-pipe"/g) || []).length, 1);
  for (const id of ['roleLine', 'heroTopo', 'topologyGraph']) assert.equal(f.elements[id].innerHTML, 'untouched');
});

test('foreign selection puts Iran then foreign server in the hero, unknown role is not invented', () => {
  const s = main(); s.role = 'foreign'; s.frp_svc = 'frpc';
  const f = fixture({ selected: 'main', local: s }); f.render();
  assert.ok(f.elements.tunnelOverviewHeader.innerHTML.indexOf('<em>Iran server') < f.elements.tunnelOverviewHeader.innerHTML.indexOf('<em>Foreign server'));
  s.role = ''; s.frp_svc = 'unknown-service'; f.render();
  assert.equal(f.render().role, ''); assert.match(f.html(), /<em>Other server/);
});

test('numeric peer uses only that peer: never main or another peer endpoints, ports, latency or counters', () => {
  const f = fixture({ selected: 3, local: main(), peers: [peer(2), peer(3)] });
  const m = f.render();
  assert.equal(m.remoteIP, '198.51.100.3'); assert.equal(m.control, 26003); assert.equal(m.rx, 333); assert.equal(m.latency, '30 ms');
  assert.equal(m.inner, ''); assert.match(f.html(), /1000-1005, 3003/);
  for (const value of ['main-remote', '198.51.100.1', '198.51.100.2', '26002', '25000', '10.10.10.1/30', '222 B']) assert.ok(!f.html().includes(value), value);
  f.select(2); assert.equal(f.render().remoteIP, '198.51.100.2');
  f.select(3); f.context.replacement = { id: 3, name: 'missing fields' }; vm.runInContext('tunPeerCache = [replacement];', f.context);
  const missing = f.render(); assert.equal(missing.remoteIP, ''); assert.equal(missing.control, ''); assert.equal(missing.latency, '—');
  assert.ok(!f.html().includes('198.51.100.1')); assert.ok(!f.html().includes('25000')); assert.ok(!f.html().includes('100 B'));
});

test('running service or GRE interface without a verified link is waiting, not connected', () => {
  for (const kind of ['main', 2]) {
    const s = kind === 'main' ? main() : peer(2);
    Object.assign(s, { linked: false, connected: false, health_state: 'degraded', ping_ok: false });
    const f = fixture({ selected: kind, local: s, peers: [s] }); const m = f.render();
    assert.equal(m.service, true); assert.equal(m.connected, false); assert.equal(m.state, 'warn');
    assert.match(f.html(), /Waiting for the other server/); assert.match(f.html(), /Running/);
    assert.ok(!f.elements.tunnelOverviewHeader.innerHTML.includes('tv-on'));
  }
});

test('Matrix agrees with existing matrixSummary for backend public and snapshot fields', () => {
  for (const role of ['hub', 'node']) for (const service of [true, false]) for (const ping of [true, false]) for (const sync of [true, false]) {
    const s = mx(role, service, ping); s.peer_sync_required = sync;
    // A misleading generic connected flag cannot override Matrix's service and mesh fields.
    s.connected = true;
    const f = fixture({ selected: 'matrix', matrix: s, local: main() });
    const m = f.render(), existing = f.context.matrixSummary(s);
    assert.equal(m.connected, existing.connected); assert.equal(m.state, existing.state);
    assert.equal(m.remoteIP, role === 'hub' ? '2001:db8::2' : '2001:db8::1');
    assert.equal(m.family, 'IPv6'); assert.equal(m.control, 2097);
    for (const value of ['Matrix', '10.254.231.', '2097', '443, 8080', '777 B', '888 B']) assert.ok(f.html().includes(value), value);
    for (const value of ['GRE', 'FRP', 'main-remote', '25000']) assert.ok(!f.html().includes(value), value);
  }
});

test('Matrix waiting hub never treats its own addr as remote; all nodes are represented, partial connectivity explicit', () => {
  const s = mx('hub', true, true); s.peers = [];
  const f = fixture({ selected: 'matrix', matrix: s, local: main() });
  assert.equal(f.render().remoteIP, ''); assert.match(f.html(), /Waiting for foreign node/);
  assert.ok(!f.html().includes('2001:db8::1'));
  s.peers = [{ name: 'A', addr: '2001:db8::2', ip: '10.254.231.2', ping_ok: true }, { name: 'B', addr: '2001:db8::3', ip: '10.254.231.3', ping_ok: false }];
  const m = f.render(); assert.equal(m.connected, true); assert.equal(m.state, 'warn');
  assert.equal(m.remoteIP, '2001:db8::2, 2001:db8::3'); assert.match(f.html(), /Partially connected/);
  s.enabled = false; assert.equal(f.render(), null); assert.equal(f.elements.tunnelOverviewDetails.innerHTML, '');
});

test('Bagher separates enabled/service/session and uses own counters without secrets or main fields', () => {
  const b = { enabled: true, has_config: true, role: 'iran', is_active: false, connected: true, remote_host: '2001:db8::5', name: 'Bagher foreign',
    frp_bind_port: 55400, remote_ssh_port: 2222, ports: [9000], rx_bytes: 12345, tx_bytes: 54321, latency_ms: 4.3,
    bundle: 'SECRET-BUNDLE', auth_line: 'SECRET-AUTH', key_path: 'SECRET-PATH' };
  const f = fixture({ selected: 'bagher', bagher: b, local: main() });
  assert.equal(f.render().connected, false); assert.equal(f.render().service, false); assert.match(f.html(), /Stopped/);
  b.is_active = true; b.connected = false; assert.equal(f.render().state, 'warn');
  b.connected = true; assert.equal(f.render().state, 'ok');
  for (const value of ['55400', '2222', '9000', '12345 B', '54321 B', '4.3 ms']) assert.ok(f.html().includes(value), value);
  for (const value of ['SECRET-', '25000', 'main-remote', '198.51.100.1']) assert.ok(!f.html().includes(value), value);
  b.links = [{ id: 2, name: 'second Bagher', remote_host: '198.51.100.20', is_active: true, connected: true, ports: [9999] }];
  f.select('bagher-2'); assert.equal(f.render().remoteIP, '198.51.100.20'); assert.ok(!f.html().includes('12345 B'));
});

test('Xray exact-id cache, ready/listening not connected, no key/profile/expert disclosure', () => {
  const l = { id: 7, name: 'Xray foreign', side: 'iran', mode: 'reverse', remote: '2001:db8::7', tunnel_port: 9443, ports: [8081], rx: 700, tx: 701,
    profile: { transport: 'xhttp', security: 'reality' }, keys: { uuid: 'SECRET-UUID', private_key: 'SECRET-KEY' }, expert: { secret: 'SECRET-EXPERT' },
    health: { active: true, link: 'listening', ping_ms: 42, conns: 0 } };
  const x = { public_ip: '192.0.2.7', links: [l] };
  const f = fixture({ selected: 'xray-7', xray: x, local: main() });
  const m = f.render(); assert.equal(m.connected, false); assert.equal(m.service, true); assert.equal(m.state, 'warn');
  for (const value of ['Xray', 'xhttp', 'Reverse', '9443', '8081', '700 B', '42 ms']) assert.ok(f.html().includes(value), value);
  assert.ok(!f.html().includes('SECRET-')); l.health.link = 'up'; assert.equal(f.render().connected, true);
  l.disabled = true; assert.equal(f.render().service, false); assert.equal(f.render().connected, false);
  x.links = []; assert.equal(f.render(), null);
  const cached = fixture({ selected: 'xray:8', dash: { xray: [{ id: 8, side: 'foreign', remote: '198.51.100.8', link: 'up', rx: 8 }] }, local: main() });
  assert.equal(cached.render().remoteIP, '198.51.100.8'); assert.equal(cached.render().control, ''); assert.equal(cached.render().service, null);
});

test('unknown, removed, or ambiguous string kinds never silently reuse main', () => {
  const f = fixture({ local: main(), peers: [peer(2)] });
  for (const kind of [99, '2', 'peer-2', 'unknown', 'bagher-55', 'xray', 'matrix']) {
    f.select(kind); assert.equal(f.render(), null, String(kind)); assert.equal(f.html(), '');
  }
});

test('HTML injection in names, endpoint, ports, inner address and transport is escaped using app esc', () => {
  const attack = '<img src=x onerror="alert(1)">\'&';
  const p = peer(2); Object.assign(p, { name: attack, remote_pub: attack, raw_ports: attack, transport: attack, engine: attack, local_pub: attack,
    local_gre: attack, peer_gre: attack, no_gre: false, frps_svc: attack });
  const f = fixture({ selected: 2, peers: [p], dash: { server_name: attack } }); f.render();
  assert.ok(!f.html().includes('<img')); assert.match(f.html(), /&lt;img/); assert.match(f.html(), /&quot;/); assert.match(f.html(), /&#39;/); assert.match(f.html(), /&amp;/);
  const s = mx('hub', true, true); Object.assign(s.peers[0], { name: attack, addr: attack, ip: attack });
  const matrix = fixture({ selected: 'matrix', matrix: s }); matrix.render(); assert.ok(!matrix.html().includes('<img')); assert.match(matrix.html(), /&lt;img/);
});

test('presentation never changes caches, registers events, or requires DOM at load time', () => {
  const l = main(), p = [peer(2)], s = mx('hub', true, true), before = JSON.stringify({ l, p, s });
  const f = fixture({ selected: 2, local: l, peers: p, matrix: s }); f.render(); f.render();
  assert.equal(JSON.stringify({ l, p, s }), before);
  const context = vm.createContext({}); vm.runInContext(code, context); assert.equal(context.renderTunnelOverview(), null);
});
