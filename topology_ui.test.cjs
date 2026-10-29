const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, 'topology.js'), 'utf8');
const core = source.slice(0, source.indexOf("$('topologyCreateBtn').onclick"));
const elements = new Map();
function el(id) {
  if (id === 'topologyUnassignedCard' || id === 'topologyUnassigned') return null;
  if (!elements.has(id)) elements.set(id, {id, hidden:false, disabled:false, value:'', textContent:'',innerHTML:'',
    classList:{toggle(){},add(){},remove(){}},contains(){return false}, focus(){}});
  return elements.get(id);
}
let calls=[];
const sandbox={
 assert, console, Set, document:{activeElement:null,querySelectorAll(){return []}},
 window:{_lastLocal:{role:'iran (matrix hub)',local_pub:'185.226.119.74'}},
 $:el, tr:(_k,fb)=>fb, esc:s=>String(s).replace(/&/g,'&amp;').replace(/"/g,'&quot;').replace(/</g,'&lt;').replace(/>/g,'&gt;'),
 _tunnelDetail:null, applyTunnelDetail(){},renderTunnelsList(){},toastErr(){},toastOk(){},toastWarn(){},goTab(){},setRole(){},openTunnelDetail(){},
 api:async(path,body,method)=>{calls.push({path,body,method});if(path==='/api/topologies'&&!body)return {topologies:[],tunnels:[]};return {topology:{id:'new'}};},
 entConfirm:async()=>true
};
vm.createContext(sandbox);vm.runInContext(core,sandbox);
vm.runInContext(`
const topo={id:'t',name:'Pair',role:'iran',local_ip:'185.226.119.74',remote_ip:'91.107.179.252',tunnel_ids:['matrix','peer:2']};
const one=[{id:'matrix',name:'Matrix',engine:'matrix',state:'healthy'}];
assert.equal((topologyGraph(topo,one).match(/data-topology-cable=/g)||[]).length,1);
const five=Array.from({length:5},(_,i)=>({id:'peer:'+i,name:'tunnel'+i,engine:'frp',state:i?'down':'healthy'}));
assert.equal((topologyGraph(topo,five).match(/data-topology-cable=/g)||[]).length,5);
assert.equal((topologyGraph(topo,[]).match(/data-topology-cable=/g)||[]).length,0);
const unsafe=topologyGraph(topo,[{id:'matrix',name:'<script>alert(1)</script>',engine:'matrix',state:'healthy'}]);
assert.ok(!unsafe.includes('<script>'));assert.ok(unsafe.includes('&lt;script&gt;'));
assert.equal(topologyTunnelKey(2),'peer:2');assert.equal(topologyTunnelKey('matrix'),'matrix');assert.equal(topologyKind('peer:2'),2);
topologyData={topologies:[topo],tunnels:one};topologySelected='t';
assert.deepEqual(topologyFilterRows([{kind:'main'},{kind:'matrix'},{kind:2}]).map(x=>x.kind),['matrix',2]);
topologyOpen('t');assert.equal($('topologyListCard').hidden,true);assert.equal($('topologyDetailCard').hidden,false);
topologyBack();assert.equal($('topologyListCard').hidden,false);assert.equal($('topologyDetailCard').hidden,true);
topologySelected='t';_tunnelDetail='matrix';topologyRender();assert.equal($('topologyDetailCard').hidden,true);
_tunnelDetail=null;topologyRender();assert.equal($('topologyDetailCard').hidden,false);
const candidates=[
 {id:'candidate',name:'Existing tunnel',engine:'frp',remote_ip:topo.remote_ip},
 {id:'unknown',name:'Unknown endpoint',engine:'frp'},
 {id:'multi',name:'Multiple endpoints',engine:'frp',remote_ip:'192.0.2.1',remote_ips:[topo.remote_ip]},
 {id:'other-pair',name:'Other pair',engine:'frp',remote_ip:'192.0.2.2'},
 {id:'assigned-elsewhere',name:'Already assigned',engine:'frp',remote_ip:topo.remote_ip}
];
topologyData={topologies:[topo,{id:'other',tunnel_ids:['assigned-elsewhere']}],tunnels:[...one,...candidates]};
topologyRender();
const options=$('topologyAttachSelect').innerHTML;
for(const id of ['candidate','unknown','multi']) assert.ok(options.includes('value="'+id+'"'),id+' remains attachable');
for(const id of ['matrix','other-pair','assigned-elsewhere']) assert.ok(!options.includes('value="'+id+'"'),id+' is excluded');
assert.ok($('topologyGraph').innerHTML.includes('data-topology-cable="matrix"'));
assert.ok($('topologyBoundList').innerHTML.includes('data-topo-view-tunnel="matrix"'));
assert.equal($('topologyUnassignedCard'),null);assert.equal($('topologyUnassigned'),null);
topologyData={topologies:[topo],tunnels:one};
`,sandbox);
(async()=>{
 calls=[];el('topologyAttachSelect').value='candidate';await sandbox.topologyAttach();
 assert.equal(calls[0].path,'/api/topologies/t');assert.equal(calls[0].method,'PATCH');
 assert.deepEqual(Array.from(calls[0].body.tunnel_ids),['matrix','peer:2','candidate']);
 vm.runInContext("topologyData={topologies:[topo],tunnels:one};topologySelected='t'",sandbox);
 calls=[];await sandbox.topologyDelete('t');
 assert.equal(calls.length,0,'configured tunnel blocks topology deletion');
 vm.runInContext('topologyData.tunnels=[]',sandbox);
 await sandbox.topologyDelete('t');
 assert.equal(calls[0].path,'/api/topologies/t');assert.equal(calls[0].method,'DELETE');
 assert.ok(!calls.some(c=>/api\/action|matrix\/remove/.test(c.path)));
 console.log('Topology UI: cables, escaping, filters, view/back, absent overview card, existing-tunnel attachment and safe grouping deletion PASS');
})().catch(e=>{console.error(e);process.exitCode=1;});
