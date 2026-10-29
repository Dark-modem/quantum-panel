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
const unsafeEndpoints=topologyGraph({...topo,role:'foreign',local_ip:'<foreign>&"',remote_ip:'<iran>&"'},[]);
assert.ok(!unsafeEndpoints.includes('<foreign>'));assert.ok(!unsafeEndpoints.includes('<iran>'));
assert.ok(unsafeEndpoints.includes('&lt;foreign&gt;&amp;&quot;'));assert.ok(unsafeEndpoints.includes('&lt;iran&gt;&amp;&quot;'));
assert.ok(unsafeEndpoints.indexOf('&lt;iran&gt;')<unsafeEndpoints.indexOf('&lt;foreign&gt;'),'foreign local role reverses Iran/foreign graph endpoints');
assert.equal(topologyTunnelKey(2),'peer:2');assert.equal(topologyTunnelKey('matrix'),'matrix');assert.equal(topologyKind('peer:2'),2);
assert.equal(topologyListStatus([]).key,'empty','zero existing tunnels is empty, not healthy');
for(const state of ['healthy','ok','up','connected','HEALTHY']) assert.equal(topologyListStatus([{state}]).key,'healthy',state);
for(const state of ['down','err','error','failed','stopped','off']) assert.equal(topologyListStatus([{state}]).key,'down',state);
for(const state of ['unknown','degraded','connecting','',undefined]) assert.equal(topologyListStatus([{state}]).key,'degraded',String(state)+' must not imply healthy');
assert.equal(topologyListStatus([{state:'healthy'},{state:'stopped'}]).key,'degraded','mixed inventory is not healthy');
const unsafePair={id:'pair"<id>&',name:'Human-readable <Pair> & "Name"',role:'foreign',local_ip:'<local>&"',remote_ip:'<remote>&"',tunnel_ids:[]};
topologyData={topologies:[unsafePair],tunnels:one};topologySelected=null;topologyRender();
const listHTML=$('topologiesList').innerHTML;
assert.ok(listHTML.includes(esc(unsafePair.name)),'list renders the semantic topology name');
assert.ok(!listHTML.includes(unsafePair.name),'topology name is escaped');
for(const field of ['local_ip','remote_ip']) {
 assert.ok(listHTML.includes(esc(unsafePair[field])),field+' rendered escaped');
 assert.ok(!listHTML.includes(unsafePair[field]),field+' cannot inject HTML');
}
for(const action of ['data-topo-open','data-topo-delete']) {
 assert.ok(listHTML.includes(action+'="'+esc(unsafePair.id)+'"'),action+' retains the original escaped ID');
 assert.ok(!listHTML.includes(action+'="'+unsafePair.id+'"'),action+' cannot inject attributes');
}
assert.ok(listHTML.includes('data-topo-id="'+esc(unsafePair.id)+'"'),'row ID is escaped');
assert.equal((listHTML.match(/<tr\\b/g)||[]).length,1,'one semantic row per topology');
assert.equal((listHTML.match(/<td\\b/g)||[]).length,5,'name, server pair, count, status and actions cells');
for(const cell of ['name','servers','tunnels','status','actions']) assert.ok(listHTML.includes('class="topo-cell-'+cell+'" data-label='),cell+' supports narrow-screen labels');
assert.equal(listHTML.split('data-topo-open="'+esc(unsafePair.id)+'"').length-1,2,'name and explicit View target the same topology');
const serverCell=listHTML.match(/<td\\b[^>]*class="topo-cell-servers"[^>]*>([\\s\\S]*?)<\\/td>/)[1];
assert.ok(serverCell.includes('>Iran</span>'));assert.ok(serverCell.includes('>Foreign</span>'));
assert.ok(serverCell.indexOf(esc(unsafePair.remote_ip))<serverCell.indexOf(esc(unsafePair.local_ip)),'role labels map remote Iran/local Foreign correctly');
const actionCell=listHTML.match(/<td\\b[^>]*class="topo-cell-actions"[^>]*>([\\s\\S]*?)<\\/td>/)[1];
const actionText=actionCell.replace(/<[^>]*>/g,'').trim();
assert.ok(actionText.includes('View'));assert.ok(actionText.includes('Delete'));
assert.ok(!actionText.includes('Delete topology'),'explicit Delete stays compact while aria-label remains descriptive');
assert.ok(actionCell.includes('aria-label="Delete topology"'));
assert.ok(listHTML.includes('data-topo-status="empty"'),'unrelated inventory does not make this row healthy');
topologyData={topologies:[topo],tunnels:[...one,{...one[0]}, {id:'unrelated',state:'down'}]};topologyRender();
const populated=$('topologiesList').innerHTML;
assert.ok(populated.includes('aria-label="1 existing tunnel(s)">1</span>'),'duplicate inventory and deleted IDs do not inflate count');
assert.ok(populated.includes('data-topo-status="healthy"'),'stale IDs and unrelated down tunnels do not falsify row status');
topologyData={topologies:[],tunnels:one};topologyRender();
assert.ok($('topologiesList').innerHTML.includes('colspan="5"'),'empty state spans the table');
assert.ok(!$('topologiesList').innerHTML.includes('topo-list-row'),'no phantom topology row when empty');
assert.equal($('topologyCount').textContent,'0');
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
 console.log('Topology UI: semantic table/actions, escaping, server roles, truthful status, inventory counts, cables, filters, view/back, absent overview card, attachment and safe deletion PASS');
})().catch(e=>{console.error(e);process.exitCode=1;});
