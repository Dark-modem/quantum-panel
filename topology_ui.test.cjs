const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync('topology.js', 'utf8');
const core = source.slice(0, source.indexOf("$('topologyCreateBtn').onclick"));
const elements = new Map();
function el(id) {
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
`,sandbox);
(async()=>{
 calls=[];await sandbox.topologyDelete('t');
 assert.equal(calls[0].path,'/api/topologies/t');assert.equal(calls[0].method,'DELETE');
 assert.ok(!calls.some(c=>/api\/action|matrix\/remove/.test(c.path)));
 console.log('Topology UI: 1/5 cables, escaping, filters, view/back and safe grouping deletion PASS');
})().catch(e=>{console.error(e);process.exitCode=1;});
