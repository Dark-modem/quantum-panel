'use strict';
// Execute the actual dashboard/onboarding functions, not screenshot assertions.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');
const section = (start, end) => {
  const a = source.indexOf(start), b = source.indexOf(end, a + start.length);
  assert.ok(a >= 0 && b > a, 'dashboard test source anchors present');
  return source.slice(a, b);
};
const code = section('function engLabel(e)', 'function latBadge(ms)')
  + section('async function refreshDash()', 'function drawActivityChart(')
  + section('function dashLinks(d, bObj, iran, foreign)', '// xrWireName:')
  + section('function uiObserve(s)', '{ const _rd = renderDoctor;');

async function run(d, empty = true, fallback) {
  const ids = ['kpiStatus', 'kpiStatusSub', 'kpiLinks', 'kpiLinksSub', 'heroLocalName', 'heroRemoteName', 'heroRemoteIp', 'dashDetails', 'onbCard'];
  const elements = Object.fromEntries(ids.map(id => [id, {textContent:'',innerHTML:'',hidden:false}]));
  const context = {
    window: {_hashemEmpty:empty, _matrixState:fallback},
    document: {querySelectorAll:()=>[], hidden:false},
    $: id => elements[id] || null,
    api: async()=>d, dashRange:'1h', tunPeerCache:[], _statusCountersAt:null,
    _tunnelDetail:null, _sshfwdData:null,
    tr: (_key, fallback)=>fallback, esc: s=>String(s ?? ''), NA:'—',
    ICO_SRV:'server', ICO_GRE:'gre', ICO_FRP:'frp',
    localStorage:{getItem:()=>null,setItem:()=>{}},
    renderMatrixSetup:()=>{},renderTunnelsList:()=>{},renderMatrixDetail:()=>{},
    renderDashPeers:()=>{},setTraffic:()=>{},drawChart:()=>{},drawActivityChart:()=>{},navBadge:()=>{},
    fmtBytes:n=>n+' B',fmtSecs:n=>String(n),fmtRate:n=>n+' B/s',latBadge:()=>'',icmpChip:()=>'',
    linkRate:()=>null,linkPing:()=>({ok:false}),vizState:()=>null,
    reportErr:(_name,e)=>{throw e;},
  };
  vm.createContext(context);vm.runInContext(code,context);
  context.uiObserve({local:{matrix:d.matrix},peers:d.peers||[]});
  const model = context.dashboardModel(d,empty);
  assert.equal(await context.refreshDash(),true);
  const links = context.dashLinks(d,d.bagher||{},/iran/.test(model.role),/foreign/.test(model.role));
  return {elements,model,links,context};
}
const mx = (role='hub',service=true,ping=true)=>({enabled:true,role,family:'ipv6',port:2097,addr:'2001:db8::1',ip:role==='hub'?'10.254.231.1':'10.254.231.2',ports:[443,8080],service,hub_ping_ok:ping,
  peers:role==='hub'?[{name:'foreign',addr:'2001:db8::2',ip:'10.254.231.2',ping_ok:ping}]:[],rx:1234,tx:5678});
const dashboard = matrix=>({online:!!matrix.service,health:!matrix.service?'DOWN':matrix.hub_ping_ok?'HEALTHY':'DEGRADED',matrix,role:'',active_engine:'matrix',gre:{exists:false},frp:{},peers:[],reasons:[],traffic:{},history:[],bagher:{},system:{}});
(async()=>{
  let count=0;
  for (const role of ['hub','node']) for (const [service,ping,state] of [[true,true,'ok'],[true,false,'warn'],[false,false,'err']]) {
    const r=await run(dashboard(mx(role,service,ping)));
    assert.equal(r.model.unset,false);assert.equal(r.model.state,state);
    assert.equal(r.elements.onbCard.hidden,true);
    assert.ok(!r.elements.kpiLinksSub.textContent.includes('Not configured'));
    assert.ok(!r.elements.dashDetails.innerHTML.includes('Not configured'));
    assert.ok(!r.elements.kpiLinks.innerHTML.includes('GRE'));assert.ok(!r.elements.kpiLinks.innerHTML.includes('FRP'));
    assert.match(r.elements.kpiLinks.innerHTML,/Matrix/);
    assert.match(r.elements.dashDetails.innerHTML,/10\.254\.231\.1|10\.254\.231\.2/);
    assert.match(r.elements.dashDetails.innerHTML,/2097/);assert.match(r.elements.dashDetails.innerHTML,/443, 8080/);
    assert.equal(r.elements.heroLocalName.textContent,role==='hub'?'Iran server':'Foreign server');
    assert.equal(r.elements.heroRemoteIp.textContent,role==='hub'?'2001:db8::2':'2001:db8::1');
    assert.equal(r.links.length,1);assert.equal(r.links[0].gre,false);assert.match(r.links[0].eng,/Matrix/);count++;
  }
  const mixed=dashboard(mx());Object.assign(mixed,{active_engine:'frp',health:'HEALTHY',role:'foreign (client)',remote_pub:'198.51.100.1',frp:{up:true,svc:'frpc',proxy_ports:[8443]},gre:{exists:true}});
  const r=await run(mixed);
  assert.equal(r.model.role,'foreign (client)');assert.equal(r.elements.heroRemoteIp.textContent,'198.51.100.1');
  assert.equal(r.links[0].eng,'FRP');assert.equal(r.links[0].key,'main');assert.equal(r.links[1].gre,false);assert.match(r.elements.kpiLinks.innerHTML,/FRP/);assert.match(r.elements.kpiLinks.innerHTML,/Matrix/);count++;
  const waiting=mx();waiting.peers=[];
  const wait=await run({...dashboard(waiting),health:'DEGRADED'});
  assert.equal(wait.model.state,'warn');assert.equal(wait.elements.heroRemoteIp.textContent,'—');assert.notEqual(wait.elements.heroRemoteIp.textContent,waiting.addr);count++;
  const pending=mx();pending.peer_sync_required=true;
  const p=await run({...dashboard(pending),health:'DEGRADED'});assert.equal(p.model.matrix.connected,false);assert.equal(p.model.state,'warn');count++;
  const removed={...dashboard(mx()),matrix:null,online:false,health:'DOWN',active_engine:'unknown'};
  const rem=await run(removed,true,mx());assert.equal(rem.model.matrix.enabled,false);assert.equal(rem.model.unset,true);count++;
  const old={...dashboard(mx()),online:false,health:'DOWN'};delete old.matrix;
  const compat=await run(old,true,mx('node',true,false));assert.equal(compat.model.unset,false);assert.equal(compat.model.state,'warn');count++;
  console.log('Matrix dashboard/onboarding/render integration: '+count+' scenarios passed');
})().catch(e=>{console.error(e);process.exitCode=1;});
