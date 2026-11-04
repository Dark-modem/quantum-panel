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
    classList:{_toggles:[],toggle(c,v){this._toggles.push([c,v]);},add(){},remove(){}},
    setAttribute(){}, addEventListener(){}, contains(){return false}, focus(){}});
  return elements.get(id);
}
let calls=[];
const sandbox={
 // addEventListener/querySelector keep the document-level wiring in topology.js (menus, Escape) loadable
 assert, console, Set, document:{activeElement:null,querySelectorAll(){return []},querySelector(){return null},addEventListener(){}},
 window:{_lastLocal:{role:'iran (matrix hub)',local_pub:'185.226.119.74'}},
 $:el, el, tr:(_k,fb)=>fb, esc:s=>String(s).replace(/&/g,'&amp;').replace(/"/g,'&quot;').replace(/</g,'&lt;').replace(/>/g,'&gt;'),
 _tunnelDetail:null, applyTunnelDetail(){},renderTunnelsList(){},toastErr(){},toastOk(){},toastWarn(){},goTab(){},setRole(){},openTunnelDetail(){},
 skel:n=>{let s='';for(let i=0;i<(n||3);i++)s+='<div class="ent-skel u-h-14"></div>';return '<div class="u-grid u-gap-8">'+s+'</div>';},
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
const designed=topologyGraph(topo,one);
assert.ok(designed.includes('topo-map-scroll')&&designed.includes('cl-legend'),'graph keeps the cluster-map chrome: sunken scroll well and legend row');
assert.ok(designed.includes('topo-e-dots')&&designed.includes('topo-e-pulse'),'healthy beams carry beacon dots and the marching pulse');
assert.ok(!topologyGraph(topo,five.slice(1)).includes('topo-e-pulse'),'down beams never animate the pulse');
assert.ok(designed.includes('topo-dot ok'),'all-healthy tunnels light the node dot green');
assert.ok(topologyGraph(topo,five).includes('topo-dot warn'),'mixed tunnel health marks the node dot warn');
const empty=topologyGraph(topo,[]);
assert.ok(!empty.includes('topo-dot'),'no tunnels hides the health dot instead of a dead grey one');
assert.ok(empty.includes('topo-idle')&&empty.includes('No tunnel attached yet'),'the empty state draws a quiet placeholder cable with its label');
assert.ok(designed.includes('topo-flag-svg'),'graph nodes embed the flag as inline SVG, not emoji in <text>');
assert.ok(topologyGraph({...topo,country:'de'},one).includes('#000000')&&topologyGraph({...topo,country:'de'},one).includes('#dd0000'),'foreign graph node draws the chosen country flag');
assert.equal(topologyTunnelKey(2),'peer:2');assert.equal(topologyKind('peer:2'),2);
const irFlag=topologyFlagSvg('ir');
assert.ok(irFlag.startsWith('<svg class="topo-flag-svg"')&&irFlag.includes('viewBox="0 0 20 14"'),'flags render as embedded inline SVG, not emoji');
assert.ok(irFlag.includes('#239f40')&&irFlag.includes('#da0000'),'Iran draws its green and red bands');
assert.ok(irFlag.includes('#ffffff'),'Iran keeps its white middle band');
assert.ok(topologyFlagSvg('de').includes('#ffce00')&&topologyFlagSvg('de').includes('#000000'),'ISO country draws its own colours');
assert.ok(topologyFlagSvg('').includes('topo-flag-globe'),'empty country falls back to the embedded globe');
assert.equal(topologyFlagSvg('xyz'),topologyFlagSvg(''),'non-ISO country falls back to the globe');
assert.ok(topologyFlagSvg('ir').includes('rx="2.5"'),'flags carry the rounded frame');
assert.ok(!/[\\u{1F1E6}-\\u{1F1FF}]/u.test(irFlag),'no regional-indicator emoji inside the flag markup');
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
assert.equal((listHTML.match(/<td\\b/g)||[]).length,6,'name, iran, foreign, count, status and actions cells');
for(const cell of ['name','iran','foreign','tunnels','state','actions']) assert.ok(listHTML.includes('class="topo-cell-'+cell+'" data-label='),cell+' supports narrow-screen labels');
assert.ok(!listHTML.includes('topo-dot'),'no status dot next to the topology name');
assert.ok(!listHTML.includes('topo-cell-status'),'status column is gone');
assert.ok(listHTML.includes('tabindex="0"'),'rows are keyboard reachable');
const iranCell=listHTML.match(/<td\\b[^>]*class="topo-cell-iran"[^>]*>([\\s\\S]*?)<\\/td>/)[1];
assert.ok(iranCell.includes('topo-flag-svg')&&iranCell.includes('#239f40'),'Iran column always carries the embedded Iran flag');
assert.ok(!/[\\u{1F1E6}-\\u{1F1FF}]/u.test(listHTML)&&!listHTML.includes('🌐'),'list markup never falls back to emoji flags');
assert.ok(iranCell.includes(esc(unsafePair.remote_ip)),'Iran column shows the Iran-side IP');
const foreignCell=listHTML.match(/<td\\b[^>]*class="topo-cell-foreign"[^>]*>([\\s\\S]*?)<\\/td>/)[1];
assert.ok(foreignCell.includes('topo-flag-globe'),'unknown country falls back to the embedded globe icon');
assert.ok(foreignCell.includes(esc(unsafePair.local_ip)),'Foreign column shows the foreign-side IP');
topologyData={topologies:[{...unsafePair,country:'de'}],tunnels:one};topologyRender();
const germanCell=$('topologiesList').innerHTML.match(/<td\\b[^>]*class="topo-cell-foreign"[^>]*>([\\s\\S]*?)<\\/td>/)[1];
assert.ok(germanCell.includes('topo-flag-svg')&&germanCell.includes('#dd0000'),'user-chosen country renders the embedded German flag');
topologyData={topologies:[unsafePair],tunnels:one};topologyRender();
const countCell=listHTML.match(/<td\\b[^>]*class="topo-cell-tunnels"[^>]*>([\\s\\S]*?)<\\/td>/)[1];
assert.ok(countCell.includes('class="topo-count"'),'tunnel count keeps a semantic class');
assert.ok(!countCell.includes('badge'),'tunnel count is plain text, not a badge');
const actionCell=listHTML.match(/<td\\b[^>]*class="topo-cell-actions"[^>]*>([\\s\\S]*?)<\\/td>/)[1];
const actionText=actionCell.replace(/<[^>]*>/g,'').trim();
assert.ok(actionText.includes('View'));assert.ok(actionText.includes('Delete'));
assert.ok(!actionText.includes('Delete topology'),'explicit Delete stays compact while aria-label remains descriptive');
assert.ok(actionCell.includes('aria-label="Delete topology"'));
const deleteBtn=actionCell.match(/<button[^>]*data-topo-delete[^>]*>/)[0];
assert.ok(!/\\sdisabled(\\s|>)/.test(deleteBtn),'delete button is never disabled');
assert.ok(deleteBtn.includes('quantum-btn-danger'),'delete button is styled as a danger action');
topologyData={topologies:[topo],tunnels:[...one,{...one[0]}, {id:'unrelated',state:'down'}]};topologyRender();
const populated=$('topologiesList').innerHTML;
assert.ok(populated.includes('aria-label="1 existing tunnel(s)">1</span>'),'duplicate inventory and deleted IDs do not inflate count');
topologyData={topologies:[],tunnels:one};topologyRender();
assert.ok($('topologiesList').innerHTML.includes('colspan="6"'),'empty state spans the table');
assert.ok(!$('topologiesList').innerHTML.includes('topo-list-row'),'no phantom topology row when empty');
assert.equal($('topologyCount').textContent,'0');
topologyData={topologies:[topo],tunnels:one};topologySelected='t';
assert.deepEqual(topologyFilterRows([{kind:'main'},{kind:'matrix'},{kind:2}]).map(x=>x.kind),['main','matrix',2],'unassigned tunnels stay visible inside the open topology');
topologyData={topologies:[topo,{...topo,id:'t2',name:'Other pair',tunnel_ids:['main']}],tunnels:one};topologySelected='t';
assert.deepEqual(topologyFilterRows([{kind:'main'},{kind:'matrix'},{kind:2}]).map(x=>x.kind),['matrix',2],'tunnels owned by another topology are not listed here');
topologyOpen('t');assert.equal($('topologyListCard').hidden,true);assert.equal($('topologyDetailCard').hidden,false);
topologyBack();assert.equal($('topologyListCard').hidden,false);assert.equal($('topologyDetailCard').hidden,true);
topologySelected='t';_tunnelDetail='matrix';topologyRender();assert.equal($('topologyDetailCard').hidden,true);
_tunnelDetail=null;topologyRender();assert.equal($('topologyDetailCard').hidden,false);
assert.ok($('topologyCountryDD').innerHTML.includes('role="listbox"'),'the form country field is the custom dropdown');
assert.ok($('topologyCountryDD').innerHTML.includes('data-dd-value="de"'),'country dropdown offers Germany');
assert.ok($('topologyCountryDD').innerHTML.includes('data-dd-value=""'),'country dropdown offers Other');
assert.ok(!$('topologyCountryDD').innerHTML.includes('<select'),'no native select for the country field');
assert.ok(!/[\\u{1F1E6}-\\u{1F1FF}]/u.test($('topologyCountryDD').innerHTML),'country dropdown uses embedded SVG flags, no emoji');
assert.ok($('topologyCountryDD').innerHTML.includes('aria-haspopup="listbox"'),'the dropdown trigger announces the listbox');
assert.ok(topoCountryOptionsHtml('topologyCountryDD', topoCountryIndex('de'), topoCountryIndex('de')).includes('aria-selected="true" data-dd-value="de"'),'current country is preselected');
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
topologySearchQuery='zzz-nomatch';topologyRender();
assert.ok($('topologiesList').innerHTML.includes('No topology matches your search.'),'search miss explains itself');
assert.ok(!$('topologiesList').innerHTML.includes('topo-list-row'),'search miss renders no topology row');
topologySearchQuery=String(topo.local_ip);topologyRender();
assert.ok($('topologiesList').innerHTML.includes('data-topo-id="'+esc(topo.id)+'"'),'search matches the server IP');
topologySearchQuery=topo.name.toLowerCase();topologyRender();
assert.ok($('topologiesList').innerHTML.includes('data-topo-id="'+esc(topo.id)+'"'),'search is case-insensitive on the name');
topologySearchQuery='';topologySetView('grid');
assert.ok(el('topologyListTable').classList._toggles.some(([c,v])=>c==='topo-grid'&&v===true),'grid view toggles the card class on the table');
topologySetView('list');
assert.ok(el('topologyListTable').classList._toggles.some(([c,v])=>c==='topo-grid'&&v===false),'list view restores the row layout');
topologyData={topologies:[topo],tunnels:one};
// First-load skeleton: while the first fetch is in flight the list shows
// skeleton rows and never the "No topology yet" empty state.
assert.equal(topologyLoadedOnce,false,'no load has finished yet');
topologyLoading=true;topologyData={topologies:[],tunnels:[]};topologyRender();
let loadingHTML=$('topologiesList').innerHTML;
assert.ok(loadingHTML.includes('ent-skel'),'first load renders skeleton bars');
assert.ok(loadingHTML.includes('topo-list-skel'),'skeleton row keeps the empty-row slot');
assert.ok(!loadingHTML.includes('No topology yet'),'empty state must not flash during the first load');
topologyLoading=false;topologyLoadedOnce=true;topologyRender();
assert.ok($('topologiesList').innerHTML.includes('No topology yet'),'finished first load with zero topologies shows the empty state');
topologyLoading=false;topologyLoadedOnce=true;topologyData={topologies:[topo],tunnels:one};topologyRender();
assert.ok($('topologiesList').innerHTML.includes('topo-list-row'),'finished first load with data renders rows');
assert.ok(!$('topologiesList').innerHTML.includes('ent-skel'),'no skeleton once data is on screen');
topologySearchQuery='zzz-nomatch';topologyRender();
assert.ok($('topologiesList').innerHTML.includes('No topology matches your search.'),'search miss after load still explains itself');
topologySearchQuery='';
topologyData={topologies:[topo],tunnels:one};
`,sandbox);
// Column/grid wiring in enterprise.css: the shared header/row grid keeps
// Tunnels and Actions on fixed tracks, and the embedded flags get a fixed
// footprint so they line up with the mono IP in both directions.
const entCss=fs.readFileSync(path.join(__dirname,'enterprise.css'),'utf8');
assert.ok(/\.topo-list-head\s*,\s*\.topo-list-row\s*\{[^}]*grid-template-columns\s*:[^;}]*minmax\(0,[^;}]*64px\s+96px\s+56px/.test(entCss),'shared grid strips content minimums and fixes the Tunnels/Actions tracks');
assert.ok(/\.topo-list-head\s+th:nth-child\(4\)\s*\{[^}]*text-align\s*:\s*center/.test(entCss),'Tunnels header is centred over its column');
assert.ok(/\.topo-list-head\s+th:nth-child\(5\)\s*\{[^}]*text-align\s*:\s*center/.test(entCss),'Status header is centred over its column');
assert.ok(/\.topo-list-head\s+th:nth-child\(6\)\s*\{[^}]*text-align\s*:\s*end/.test(entCss),'Actions header is end-aligned like its buttons');
assert.ok(/\.topo-flag-svg\s*\{[^}]*width\s*:\s*20px[^}]*height\s*:\s*14px/.test(entCss),'embedded flags keep a fixed 20x14 footprint');
(async()=>{
 // loadedOnce gate: topologyReload paints the skeleton before the first
 // response and flips the flag once the fetch settles (success or failure).
 calls=[];vm.runInContext('topologyLoadedOnce=false;topologyLoading=false;topologySelected=null;topologySearchQuery="";_tunnelDetail=null;topologyData={topologies:[],tunnels:[]}',sandbox);
 const pending=vm.runInContext('topologyReload()',sandbox);
 let duringLoad=el("topologiesList").innerHTML;
 assert.ok(duringLoad.includes('ent-skel'),'topologyReload paints the skeleton synchronously on the first load');
 assert.ok(!duringLoad.includes('No topology yet'),'empty state never flashes while the first fetch is in flight');
 await pending;
 assert.equal(vm.runInContext('topologyLoadedOnce',sandbox),true,'first response marks the load complete');
 assert.ok(el('topologiesList').innerHTML.includes('No topology yet'),'zero topologies after the first load shows the empty state');
 sandbox.api=async(p,b,m)=>{calls.push({path:p,body:b,method:m});if(p==='/api/topologies'&&!b)throw new Error('boom');return {};};
 calls=[];await vm.runInContext('topologyReload()',sandbox);
 assert.equal(vm.runInContext('topologyLoadedOnce',sandbox),true,'a failed first load also completes the gate');
 assert.equal(el('topologyError').hidden,false,'errors keep the topologyError banner');
 sandbox.api=async(p,b,m)=>{calls.push({path:p,body:b,method:m});if(p==='/api/topologies'&&!b)return {topologies:[],tunnels:[]};return {topology:{id:'new'}};};
 vm.runInContext("topologyData={topologies:[topo],tunnels:one};topologySelected='t'",sandbox);
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
 vm.runInContext("topologyData={topologies:[topo],tunnels:one};topologySelected='t'",sandbox);
 el('topologyName').value='Renamed';el('topologyRole').value='iran';el('topologyLocalIP').value='192.0.2.1';
 el('topologyRemoteIP').value='192.0.2.2';el('topologyCountry').value='FI';
 calls=[];await sandbox.topologySave();
 assert.equal(calls[0].method,'PATCH');
 assert.equal(calls[0].body.country,'fi','save persists the chosen country');
 assert.ok(sandbox.topoCountryOptionsHtml('topologyCountryDD', sandbox.topoCountryIndex('fi'), sandbox.topoCountryIndex('fi')).includes('aria-selected="true" data-dd-value="fi"'),'saved country is preselected in the dropdown');
 // ---- Activate/Deactivate: the real power flow (header button + row menu) ----
 vm.runInContext("topologyData={topologies:[topo],tunnels:one};topologySelected='t'",sandbox);
 // activate=true → POST /active, success toast, and the reload that flips the label
 let toasts=[];sandbox.toastOk=(t,d)=>toasts.push(['ok',t,d]);sandbox.toastErr=(t,d)=>toasts.push(['err',t,d]);sandbox.toastWarn=(t,d)=>toasts.push(['warn',t,d]);
 calls=[];await sandbox.topologyPower('t',true);
 let power=calls.find(c=>c.path==='/api/topologies/t/active');
 assert.ok(power,'the power action POSTs the topology active endpoint');
 assert.equal(power.method,'POST');assert.equal(power.body.active,true);
 assert.ok(toasts.some(t=>t[0]==='ok'&&t[1]==='Tunnels connected'),'success toast names the connected state');
 assert.ok(calls.some(c=>c.path==='/api/topologies'&&!c.body),'the power action reloads the topology state');
 // a tunnel that fails to change state surfaces the per-tunnel error
 toasts=[];calls=[];
 sandbox.api=async(p,b,m)=>{calls.push({path:p,body:b,method:m});if(p==='/api/topologies'&&!b)return {topologies:[topo],tunnels:one};if(p.endsWith('/active'))return {results:[{id:'matrix',ok:false,error:'unit offline'}]};return {};};
 await sandbox.topologyPower('t',false);
 assert.ok(toasts.some(t=>t[0]==='err'&&t[1]==='Some tunnels did not change state'&&String(t[2]).includes('matrix: unit offline')),'partial failures report the offending tunnel');
 // API failure keeps the generic title
 toasts=[];calls=[];
 sandbox.api=async(p,b,m)=>{calls.push({path:p,body:b,method:m});if(p.endsWith('/active'))throw new Error('boom');return {topologies:[topo],tunnels:one};};
 await sandbox.topologyPower('t',true);
 assert.ok(toasts.some(t=>t[0]==='err'&&t[1]==='Topology action failed'),'an API failure keeps the action-failed toast');
 // the header label mirrors the pair health, without wiping the power icon
 sandbox.api=async(p,b,m)=>{calls.push({path:p,body:b,method:m});if(p==='/api/topologies'&&!b)return {topologies:[topo],tunnels:one};return {};};
 vm.runInContext('topologySelected=null;topologyRender()',sandbox);
 vm.runInContext("topologyData={topologies:[topo],tunnels:one};topologySelected='t';_tunnelDetail=null;topologyRender()",sandbox);
 const actBtn=el('topologyActivateBtn');
 // The icon lives in the static markup, the label is the only dynamic part.
 const detailMarkup=fs.readFileSync(path.join(__dirname,'index.html'),'utf8');
 const powerBtn=detailMarkup.slice(detailMarkup.indexOf('id="topologyActivateBtn"'),detailMarkup.indexOf('id="topologyEditBtn"'));
 assert.ok(/<svg[^>]*topo-act-ic/.test(powerBtn)&&powerBtn.includes('id="topologyActivateLbl"'),'the power button pairs its icon with the label span');
 assert.ok(el('topologyActivateLbl').textContent==='Deactivate','a healthy pair offers Deactivate');
 vm.runInContext("topologyData={topologies:[topo],tunnels:[{id:'matrix',name:'Matrix',engine:'matrix',state:'down'}]};topologyRender()",sandbox);
 assert.ok(el('topologyActivateLbl').textContent==='Activate','a down pair offers Activate');
 console.log('Topology UI: semantic table/actions, escaping, embedded SVG flag columns, country selector, aligned columns, plain counts, keyboard rows, view/back, first-load skeleton gate, absent overview card, attachment and safe deletion PASS');
})().catch(e=>{console.error(e);process.exitCode=1;});
