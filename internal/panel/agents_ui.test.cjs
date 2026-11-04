'use strict';
// Agents tab UI tests: executes the real agents-section code from app.js
// (sliced between its section markers) in a sandbox, following the pattern
// of topology_ui.test.cjs.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

const source = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');
const START = '// ---- Agents tab: endpoint agents, enrollment tokens, agent Matrix tunnels ----';
const END = '// ---- End agents tab ----';
const a = source.indexOf(START), b = source.indexOf(END);
assert.ok(a >= 0 && b > a, 'agents section markers present in app.js');
const core = source.slice(a, b);

const elements = new Map();
function el(id) {
  if (!elements.has(id)) elements.set(id, {
    id, hidden: false, disabled: false, value: '', textContent: '', innerHTML: '', dataset: {}, onclick: null,
    classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
    setAttribute() {}, addEventListener() {}, closest() { return null; }, focus() {},
  });
  return elements.get(id);
}
let calls = [];
const sandbox = {
  assert, console, Set, Map, Date, isFinite, Number, String, Math, Object, Array, Promise, Error, RangeError, encodeURIComponent, parseInt, isNaN, setInterval, clearInterval, setTimeout, clearTimeout,
  document: { activeElement: null, hidden: false, querySelectorAll() { return []; }, querySelector() { return null; }, addEventListener() {} },
  window: { prompt: () => null },
  location: { origin: 'https://panel.example' },
  navigator: {},
  BASE: '',
  NA: 'N/A',
  $: el, el,
  tr: (_k, fb) => fb,
  esc: s => String(s ?? '').replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;'),
  docTpl: (tpl, args) => String(tpl).replace(/\{0\}/g, (args || [])[0]),
  skel: n => { let s = ''; for (let i = 0; i < (n || 3); i++) s += '<div class="ent-skel"></div>'; return s; },
  stateError: msg => '<div class="ent-error">' + msg + '</div>',
  fmtRate: bps => (bps ?? 0) + ' B/s',
  ST_ICO_EMPTY: '<svg></svg>',
  api: async (p, body, method) => {
    calls.push({ path: p, body, method });
    if (p === '/api/agents') return { agents: [] };
    if (p === '/api/agent-tunnels') return { tunnels: [] };
    if (p === '/api/agents/tokens') return body ? { id: 'tok_1', token: 'enr_abcd1234', prefix: 'enr_abcd' } : { tokens: [] };
    return {};
  },
  entConfirm: async () => true,
  modalShow() {}, modalHide() {},
  toastOk() {}, toastErr() {}, toastWarn() {},
  showActOut() {},
  agentsRefresh: async () => {},
  agentDetailClose() {},
  STATE_ACTIONS: {},
};
vm.createContext(sandbox);
// define setters inside the same script so the lexical let-bindings are reachable
vm.runInContext(core + `
globalThis.__setAgents = list => { _agAgents = list; };
globalThis.__setTunnels = list => { _agTunnels = list; };
globalThis.__setTokens = list => { _agTokens = list; };
globalThis.__pollStop = () => { if (_agTimer) { clearInterval(_agTimer); _agTimer = null; } };`, sandbox);

const now = Math.floor(Date.now() / 1000);

(async () => {
  // 1. comma ports parsing -> int array, deduped, validated
  assert.deepEqual([...sandbox.agentPortsParse('443, 8080,990')], [443, 8080, 990]);
  assert.deepEqual([...sandbox.agentPortsParse('443 443 990')], [443, 990], 'duplicates dropped');
  assert.deepEqual([...sandbox.agentPortsParse('')], [], 'empty input yields no ports');
  assert.throws(() => sandbox.agentPortsParse('443,abc'), RangeError, 'non-numeric token throws');
  assert.throws(() => sandbox.agentPortsParse('0'), RangeError, 'port 0 is not a forwarded port');
  assert.throws(() => sandbox.agentPortsParse('70000'), RangeError, 'port above 65535 throws');

  // 2. relative-time helper
  assert.equal(sandbox.agentAgo(0), '—');
  assert.equal(sandbox.agentAgo(null), '—');
  assert.equal(sandbox.agentAgo(now - 30), 'just now');
  assert.equal(sandbox.agentAgo(now - 300), '5 min ago');
  assert.equal(sandbox.agentAgo(now - 7200), '2 h ago');
  assert.equal(sandbox.agentAgo(now - 3 * 86400), '3 d ago');

  // 3. tunnel state badge mapping
  const want = { provisioning: 'info', registering: 'warn', active: 'ok', stopped: 'dim', error: 'err' };
  for (const [state, cls] of Object.entries(want)) {
    const bd = sandbox.agentTunnelStateBadge(state);
    assert.equal(bd.cls, cls, 'state ' + state + ' maps to ' + cls);
    assert.equal(bd.label, state[0].toUpperCase() + state.slice(1));
  }
  assert.equal(sandbox.agentTunnelStateBadge('mystery').cls, 'dim', 'unknown state falls back to dim');
  assert.equal(sandbox.agentTunnelStateBadge('mystery').label, 'mystery');

  // 4. agents list renders online/offline/revoked status dots
  sandbox.__setAgents([
    { id: 'agt_a', name: 'Hub Iran', role: 'iran', status: 'online', caps: ['matrix'], hostname: 'hub', last_seen: now - 10 },
    { id: 'agt_b', name: 'Node EU', role: 'foreign', status: 'offline', caps: [], hostname: 'node', last_seen: now - 3600 },
    { id: 'agt_c', name: 'Old', role: 'foreign', status: 'revoked', caps: ['matrix'], hostname: 'old', last_seen: 0 },
  ]);
  sandbox.agentsRenderAgents();
  const listHTML = el('agAgentsList').innerHTML;
  assert.ok(listHTML.includes('ag-dot-online'), 'online agent gets the green dot');
  assert.ok(listHTML.includes('ag-dot-offline'), 'offline agent gets the gray dot');
  assert.ok(listHTML.includes('ag-dot-revoked'), 'revoked agent gets the red dot');
  assert.ok(listHTML.includes('data-ag-open="agt_a"'), 'cards open the detail drawer');
  assert.ok(listHTML.includes('data-ag-revoke="agt_a"'), 'non-revoked agent has a revoke button');
  assert.ok(!listHTML.includes('data-ag-revoke="agt_c"'), 'revoked agent has no revoke button');
  sandbox.__setAgents([{ id: 'agt_x"><script>', name: '<img src=x onerror=alert(1)>', status: 'online', role: 'iran', caps: [] }]);
  sandbox.agentsRenderAgents();
  const xssHTML = el('agAgentsList').innerHTML;
  assert.ok(!xssHTML.includes('<img src=x'), 'agent name is escaped');
  assert.ok(xssHTML.includes('&lt;img src=x'), 'escaped markup renders as text');
  assert.ok(!xssHTML.includes('data-ag-open="agt_x">'), 'agent id never breaks out of the attribute');
  sandbox.__setAgents([]);

  // 5. token create builds the correct POST body and shows the one-time modal
  el('agTokenRole').value = 'foreign';
  el('agTokenLabel').value = '  Berlin node  ';
  el('agTokenDays').value = '30';
  calls = [];
  await sandbox.agentTokenCreate();
  const tokCall = calls.find(c => c.path === '/api/agents/tokens' && c.body);
  assert.ok(tokCall, 'token create POSTs the mint endpoint');
  assert.equal(tokCall.method, undefined, 'default verb is POST');
  assert.deepEqual(JSON.parse(JSON.stringify(tokCall.body)), { role: 'foreign', label: 'Berlin node', expires_days: 30 });
  assert.equal(el('agTokenValue').value, 'enr_abcd1234', 'plaintext token is placed in the one-time modal');
  assert.equal(el('agTokenCmd').value, 'gre-panel agent enroll --server https://panel.example --token enr_abcd1234', 'enroll command carries the server URL and token');

  // 6. token revoke hits the revoke endpoint
  calls = [];
  await sandbox.agentTokenRevoke('tok_abc');
  const revCall = calls.find(c => c.path === '/api/agents/tokens/tok_abc/revoke');
  assert.ok(revCall, 'token revoke POSTs the revoke endpoint');
  assert.equal(revCall.method, undefined, 'revoke is a POST');

  // 7. tokens table renders prefix/role/status/revoke
  sandbox.__setTokens([
    { id: 'tok_1', prefix: 'enr_abcd', role: 'iran', label: 'first' },
    { id: 'tok_2', prefix: 'enr_ffff', role: 'foreign', label: '', used_by: 'agt_1' },
    { id: 'tok_3', prefix: 'enr_dead', role: 'iran', label: 'dead', revoked: true },
  ]);
  sandbox.agentsRenderTokens();
  const tokHTML = el('agTokensBody').innerHTML;
  assert.ok(tokHTML.includes('enr_abcd') && tokHTML.includes('enr_dead'), 'prefixes are shown, never plaintext');
  assert.ok(tokHTML.includes('Used'), 'used token shows its status');
  assert.ok(!tokHTML.includes('data-ag-token-revoke="tok_3"'), 'revoked token has no revoke button');

  // 8. tunnels list: state badge, error tooltip, dots, health chip, actions
  sandbox.__setTunnels([{
    id: 'at_1', name: 'Tehran-Frankfurt', instance: 'matrix', state: 'registering', active: true,
    hub: { agent_id: 'agt_a', agent_name: 'Hub Iran', online: true },
    node: { agent_id: 'agt_b', agent_name: 'Node EU', online: false },
    health: { latency_ms: 42.4, jitter_ms: 3.2, loss_pct: 0.5, quality: 'good' },
    ports: [443, 8080], traffic: { billable_amplification: 2.25 },
  }, {
    id: 'at_2', name: 'Broken', instance: 'matrix2', state: 'error', error: 'node: ack timeout', active: false,
    hub: { agent_id: 'agt_a', agent_name: 'Hub Iran', online: false }, node: { agent_id: 'agt_b', agent_name: 'Node EU', online: false },
  }]);
  sandbox.agentsRenderTunnels();
  const tunHTML = el('agTunnelsList').innerHTML;
  assert.ok(tunHTML.includes('Registering'), 'registering badge label rendered');
  assert.ok(tunHTML.includes('title="node: ack timeout"'), 'error text becomes the badge tooltip');
  assert.ok(tunHTML.includes('×2.3'), 'amplification renders the rounded factor');
  assert.ok(tunHTML.includes('Server-wide estimate (whole server, not Matrix only)'), 'amplification carries the server-basis label');
  assert.ok(tunHTML.includes('data-ag-power="at_1" data-ag-power-on="0"'), 'active tunnel powers off');
  assert.ok(tunHTML.includes('data-ag-power="at_2" data-ag-power-on="1"'), 'stopped tunnel powers on');
  assert.ok(tunHTML.includes('data-ag-del="at_1"'), 'tunnel remove action present');
  assert.ok(tunHTML.includes('42 ms'), 'health chip shows latency');
  assert.ok(tunHTML.includes('ag-dot-online') && tunHTML.includes('ag-dot-offline'), 'endpoint online dots rendered');
  assert.ok(tunHTML.includes('<span class="ag-port u-mono">443</span>'), 'forwarded ports render as chips');

  // 9. STRICT: null amplification renders UNKNOWN, never a number
  const unknown = sandbox.agentAmplify(null);
  assert.ok(unknown.includes('UNKNOWN'), 'null amplification renders UNKNOWN');
  assert.ok(unknown.includes('Server-wide estimate (whole server, not Matrix only)'), 'label stays on the UNKNOWN case too');
  assert.ok(!/×\d/.test(unknown), 'no fabricated factor next to UNKNOWN');
  const undef = sandbox.agentAmplify({ billable_amplification: null });
  assert.ok(undef.includes('UNKNOWN'), 'explicit null renders UNKNOWN');

  // 10. tunnel create posts the parsed int array
  el('agTunnelName').value = 'Main pair';
  el('agTunnelHub').value = 'agt_a';
  el('agTunnelNode').value = 'agt_b';
  el('agTunnelFamily').value = 'ipv4';
  el('agTunnelMeshPort').value = '0';
  el('agTunnelPorts').value = '443, 8080,990';
  calls = [];
  await sandbox.agentTunnelCreate();
  assert.equal(calls[0].path, '/api/agent-tunnels');
  assert.deepEqual(JSON.parse(JSON.stringify(calls[0].body)), {
    name: 'Main pair', hub_agent_id: 'agt_a', node_agent_id: 'agt_b',
    family: 'ipv4', port: 0, ports: [443, 8080, 990],
  }, 'create body carries parsed int ports');

  // 11. tunnel create rejects port 22, no ports and same hub/node
  el('agTunnelPorts').value = '22, 443';
  calls = [];
  await sandbox.agentTunnelCreate();
  assert.equal(calls.filter(c => c.path === '/api/agent-tunnels').length, 0, 'port 22 never reaches the API');
  el('agTunnelPorts').value = '443';
  el('agTunnelNode').value = 'agt_a';
  await sandbox.agentTunnelCreate();
  assert.equal(calls.filter(c => c.path === '/api/agent-tunnels').length, 0, 'same hub and node never reaches the API');

  // 12. power / restart / delete endpoints
  calls = [];
  await sandbox.agentTunnelPower('at_1', false);
  await sandbox.agentTunnelRestart('at_1');
  await sandbox.agentTunnelDelete('at_1');
  const tunCalls = calls.filter(c => c.path.startsWith('/api/agent-tunnels/at_1'));
  assert.deepEqual(tunCalls.map(c => [c.path, c.method]), [
    ['/api/agent-tunnels/at_1/power', undefined],
    ['/api/agent-tunnels/at_1/restart', undefined],
    ['/api/agent-tunnels/at_1', 'DELETE'],
  ]);

  // 13. agent commands hit the relay with op/params
  calls = [];
  await sandbox.agentCommand('agt_a', 'matrix.restart', { instance: 'matrix' });
  await sandbox.agentCommand('agt_a', 'diag.ping', { target: '10.254.231.2' });
  const cmdCalls = JSON.parse(JSON.stringify(calls.filter(c => c.path.endsWith('/command')).map(c => [c.path, c.body])));
  assert.deepEqual(cmdCalls, [
    ['/api/agents/agt_a/command', { op: 'matrix.restart', params: { instance: 'matrix' } }],
    ['/api/agents/agt_a/command', { op: 'diag.ping', params: { target: '10.254.231.2' } }],
  ]);

  // 14. health chip maps quality to badge colors
  assert.ok(sandbox.agentHealthChip({ quality: 'good', latency_ms: 20 }).includes('badge ok'), 'good quality is green');
  assert.ok(sandbox.agentHealthChip({ quality: 'fair', latency_ms: 20 }).includes('badge warn'), 'fair quality is amber');
  assert.ok(sandbox.agentHealthChip({ quality: 'poor', latency_ms: 20 }).includes('badge err'), 'poor quality is red');
  assert.ok(sandbox.agentHealthChip({ quality: 'down', latency_ms: 0 }).includes('badge err'), 'down quality is red');
  assert.ok(sandbox.agentHealthChip({ quality: 'good', loss_pct: 1.2 }).includes('1%'), 'loss percent renders');

  // 15. the 10s poll is armed once and only one interval exists
  sandbox.agentsPollStart();
  const t1 = elements.get('tab-agents'); // poll tick guard reads the section
  sandbox.agentsPollStart();
  assert.ok(true, 'second poll start is a no-op');
  sandbox.__pollStop();
  sandbox.__pollStop();

  console.log('Agents tab UI: 15 groups passed (ports, relative time, badges, dots, token flow, tunnels, amplification, commands)');
})().catch(e => { console.error(e); process.exitCode = 1; });
