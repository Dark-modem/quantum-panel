// A topology groups existing tunnels between a pair of servers. Only an empty
// grouping can be deleted; tunnels and engines are never removed automatically.
let topologyData = { topologies: [], tunnels: [] };
let topologySelected = null;
let topologyBusy = false;
let topologyLoading = false;
let topologyWasConfigured = false;

function topologyCurrent() {
  return topologyData.topologies.find(t => t.id === topologySelected) || null;
}
function topologyExistingTunnelCount(topo) {
  const bound = new Set(topo ? topo.tunnel_ids || [] : []);
  return topologyData.tunnels.filter(t => bound.has(t.id)).length;
}
function topologyDeleteNotice(count) {
  return tr('', '{n} tunnel(s) still exist. Delete all tunnels in this topology first, including stopped tunnels.').replace('{n}', String(count));
}
function topologyTunnelKey(kind) {
  return typeof kind === 'number' ? 'peer:' + kind : String(kind);
}
function topologyKind(id) {
  if (/^peer:\d+$/.test(id)) return Number(id.slice(5));
  return id;
}
function topologyFilterRows(rows) {
  const topo = topologyCurrent();
  if (!topo) return [];
  return rows.filter(row => (topo.tunnel_ids || []).includes(topologyTunnelKey(row.kind)));
}
function topologyGraph(topo, tunnels) {
  const text = value => esc(String(value || '—'));
  const iran = topo.role === 'foreign' ? topo.remote_ip : topo.local_ip;
  const foreign = topo.role === 'foreign' ? topo.local_ip : topo.remote_ip;
  const height = Math.max(230, 90 + tunnels.length * 42);
  const cy = height / 2;
  const paths = tunnels.map((t, i) => {
    const healthy = ['healthy', 'ok', 'up', 'connected'].includes(t.state);
    const down = ['down', 'err', 'stopped'].includes(t.state);
    const state = healthy ? 'ok' : down ? 'err' : 'warn';
    const y = 65 + i * 42;
    return `<g data-topology-cable="${text(t.id)}" class="topo-cable topo-${state}">
      <path d="M 180 ${cy} C 300 ${y}, 500 ${y}, 620 ${cy}" />
      <rect x="290" y="${y - 14}" width="220" height="28" rx="12" />
      <text x="400" y="${y + 5}" text-anchor="middle">${text(t.name)} · ${text(t.engine)}</text>
      <title>${text(t.name)} · ${text(t.state)}</title>
    </g>`;
  }).join('');
  return `<svg class="topo-graph" viewBox="0 0 800 ${height}" role="img" aria-label="${text(tr('', 'Topology connections'))}" dir="ltr">
    ${paths}
    <g class="topo-node"><rect x="20" y="${cy - 48}" width="160" height="96" rx="18"/><text x="100" y="${cy - 12}" text-anchor="middle">${text(tr('', 'Iran server'))}</text><text class="topo-ip" x="100" y="${cy + 17}" text-anchor="middle">${text(iran)}</text></g>
    <g class="topo-node"><rect x="620" y="${cy - 48}" width="160" height="96" rx="18"/><text x="700" y="${cy - 12}" text-anchor="middle">${text(tr('', 'Foreign server'))}</text><text class="topo-ip" x="700" y="${cy + 17}" text-anchor="middle">${text(foreign)}</text></g>
    ${tunnels.length ? '' : `<text x="400" y="${cy}" text-anchor="middle" class="topo-empty">${text(tr('', 'No tunnel attached yet'))}</text>`}
  </svg>`;
}

async function topologyReload() {
  if (topologyLoading) return;
  topologyLoading = true;
  try {
    const r = await api('/api/topologies');
    topologyData = { topologies: r.topologies || [], tunnels: r.tunnels || [] };
    if (topologySelected && !topologyCurrent()) topologySelected = null;
    topologyRender();
    renderTunnelsList();
  } catch(e) {
    $('topologyError').textContent = e.message;
    $('topologyError').hidden = false;
  } finally { topologyLoading = false; }
}
function topologyRender() {
  const topo = topologyCurrent();
  const inTunnel = typeof _tunnelDetail !== 'undefined' && _tunnelDetail !== null;
  $('topologyListCard').hidden = !!topo || inTunnel;
  $('topologyDetailCard').hidden = !topo || inTunnel;
  $('topologyUnassignedCard').hidden = !!topo || inTunnel;
  $('topologySetupBack').hidden = !topo;
  $('rscBanner').classList.toggle('td-hide', !inTunnel);
  $('dialCard').classList.toggle('td-hide', !inTunnel);
  const error = $('topologyError'); if (error) { error.hidden = true; error.textContent = ''; }
  $('tunnelsAddBtn').disabled = !topo || !topo.local_ip || !topo.remote_ip;
  $('topologyAddTunnelBtn').disabled = !topo || !topo.local_ip || !topo.remote_ip;
  $('tunnelsEmpty').hidden = !topo || inTunnel;
  if (!topo) $('tunnelsListCard').hidden = true;
  $('topologyCount').textContent = String(topologyData.topologies.length);
  $('topologiesList').innerHTML = topologyData.topologies.length
    ? topologyData.topologies.map(t => {
        const remaining = topologyExistingTunnelCount(t);
        return `<div class="topo-list-row">
        <button type="button" class="topo-name" data-topo-open="${esc(t.id)}">${esc(t.name)}</button>
        <span class="u-mono" dir="ltr">${esc(t.local_ip || '—')} ↔ ${esc(t.remote_ip || '—')}</span>
        <span class="badge info">${remaining} ${esc(tr('', 'tunnels'))}</span>
        <button type="button" class="hashem-btn hashem-btn-secondary" data-topo-open="${esc(t.id)}">${esc(tr('', 'View'))}</button>
        <button type="button" class="hashem-btn hashem-btn-danger" data-topo-delete="${esc(t.id)}"${remaining ? ` disabled title="${esc(tr('', 'Delete all tunnels in this topology first'))}"` : ''}>${esc(tr('', 'Delete topology'))}</button>
        ${remaining ? `<span class="qs-hint">${esc(topologyDeleteNotice(remaining))}</span>` : ''}
      </div>`;
      }).join('')
    : `<div class="ent-empty"><b>${esc(tr('', 'No topology yet'))}</b><span>${esc(tr('', 'Create a topology, set the server roles, then add tunnels between them.'))}</span></div>`;
  const assigned = new Set(topologyData.topologies.flatMap(t => t.tunnel_ids || []));
  const unassigned = topologyData.tunnels.filter(t => !assigned.has(t.id));
  $('topologyUnassigned').innerHTML = unassigned.map(t => `<div class="topo-list-row"><b>${esc(t.name)}</b><span>${esc(t.engine)}</span><span class="u-mono" dir="ltr">${esc(t.remote_ip || '—')}</span><button type="button" class="hashem-btn hashem-btn-secondary" data-topo-view-tunnel="${esc(t.id)}">${esc(tr('', 'View'))}</button></div>`).join('')
    || `<p class="qs-hint">${esc(tr('', 'All known tunnels are assigned or no tunnel exists.'))}</p>`;
  if (!topo) return;
  const cables = (topo.tunnel_ids || []).map(id => topologyData.tunnels.find(t => t.id === id) || {id, name: id, engine: '—', state: 'down'});
  $('topologyGraph').innerHTML = topologyGraph(topo, cables);
  $('topologyNameHeading').textContent = topo.name;
  // Do not overwrite a form the user is currently editing.
  if (!$('topologySettings').contains(document.activeElement)) {
    $('topologyName').value = topo.name;
    $('topologyLocalIP').value = topo.local_ip || '';
    $('topologyRemoteIP').value = topo.remote_ip || '';
    topologyChooseRole(topo.role || 'iran');
  }
  const candidates = topologyData.tunnels.filter(t => !assigned.has(t.id) && (!t.remote_ip || t.remote_ip === topo.remote_ip || (t.remote_ips || []).includes(topo.remote_ip)));
  $('topologyAttachSelect').innerHTML = `<option value="">${esc(tr('', 'Select an existing tunnel'))}</option>`
    + candidates.map(t => `<option value="${esc(t.id)}">${esc(t.name)} · ${esc(t.engine)}</option>`).join('');
  $('topologyBoundList').innerHTML = cables.map(t => `<div class="topo-binding"><b>${esc(t.name)}</b><span>${esc(t.state)}</span><button type="button" class="hashem-btn hashem-btn-ghost" data-topo-detach="${esc(t.id)}">${esc(tr('', 'Detach from topology'))}</button><button type="button" class="hashem-btn hashem-btn-secondary" data-topo-view-tunnel="${esc(t.id)}">${esc(tr('', 'View'))}</button></div>`).join('');
  $('tunnelsEmpty').innerHTML = cables.length ? '' : `<p class="qs-hint">${esc(tr('', 'Attach an existing tunnel or add a new tunnel for this server pair.'))}</p>`;
}
function topologyChooseRole(role) {
  $('topologyRole').value = role;
  document.querySelectorAll('[data-topo-role]').forEach(b => b.setAttribute('aria-pressed', String(b.dataset.topoRole === role)));
}
function topologyOpen(id) {
  topologySelected = id;
  _tunnelDetail = null;
  applyTunnelDetail(false);
  topologyRender();
  renderTunnelsList();
}
function topologyBack() {
  topologySelected = null;
  _tunnelDetail = null;
  applyTunnelDetail(false);
  topologyRender();
  renderTunnelsList();
}
async function topologyCreate() {
  if (topologyBusy) return;
  topologyBusy = true;
  try {
    const local = window._lastLocal || {};
    const nameBase = tr('', 'New topology');
    let number = 1;
    while (topologyData.topologies.some(t => t.name === nameBase + ' ' + number)) number++;
    const r = await api('/api/topologies', {name: nameBase + ' ' + number, role: /foreign/.test(local.role || '') ? 'foreign' : 'iran', local_ip: local.local_pub || '', remote_ip: '', tunnel_ids: []});
    topologySelected = r.topology.id;
    await topologyReload();
    $('topologyName').focus();
  } catch(e) { toastErr(tr('', 'Topology save failed'), e.message); }
  finally { topologyBusy = false; }
}
async function topologySave() {
  const topo = topologyCurrent(); if (!topo || topologyBusy) return;
  topologyBusy = true; $('topologySaveBtn').disabled = true;
  try {
    await api('/api/topologies/' + encodeURIComponent(topo.id), {name: $('topologyName').value.trim(), role: $('topologyRole').value, local_ip: $('topologyLocalIP').value.trim(), remote_ip: $('topologyRemoteIP').value.trim()}, 'PATCH');
    await topologyReload(); toastOk(tr('', 'Topology saved'));
  } catch(e) { toastErr(tr('', 'Topology save failed'), e.message); }
  finally { topologyBusy = false; $('topologySaveBtn').disabled = false; }
}
async function topologyAttach() {
  const topo = topologyCurrent(), id = $('topologyAttachSelect').value;
  if (!topo || !id) return;
  try {
    await api('/api/topologies/' + encodeURIComponent(topo.id), {tunnel_ids: [...(topo.tunnel_ids || []), id]}, 'PATCH');
    await topologyReload();
  } catch(e) { toastErr(tr('', 'Topology save failed'), e.message); }
}
async function topologyDetach(id) {
  const topo = topologyCurrent(); if (!topo) return;
  try {
    await api('/api/topologies/' + encodeURIComponent(topo.id), {tunnel_ids: (topo.tunnel_ids || []).filter(t => t !== id)}, 'PATCH');
    await topologyReload();
  } catch(e) { toastErr(tr('', 'Topology save failed'), e.message); }
}
async function topologyDelete(id) {
  const topo = topologyData.topologies.find(t => t.id === id);
  const remaining = topologyExistingTunnelCount(topo);
  if (remaining) { toastWarn(topologyDeleteNotice(remaining)); return; }
  if (!await entConfirm(tr('', 'Delete topology?'), tr('', 'This empty topology will be removed.'), tr('', 'Delete topology'))) return;
  try { await api('/api/topologies/' + encodeURIComponent(id), {}, 'DELETE'); await topologyReload(); }
  catch(e) { toastErr(tr('', 'Topology save failed'), e.message); }
}
function topologyNewTunnel() {
  const topo = topologyCurrent();
  if (!topo || !topo.local_ip || !topo.remote_ip) { toastWarn(tr('', 'Set server roles and IP addresses first')); return; }
  topologyWasConfigured = true;
  goTab('setup');
  if (typeof setRole === 'function') setRole(topo.role);
  else if ($('sRole')) $('sRole').value = topo.role;
  if ($('sLocalPub')) $('sLocalPub').value = topo.role === 'iran' ? topo.local_ip : topo.remote_ip;
  if ($('sRemotePub')) $('sRemotePub').value = topo.role === 'iran' ? topo.remote_ip : topo.local_ip;
  if ($('sMainName')) $('sMainName').value = topo.name;
  if ($('fRemotePub')) $('fRemotePub').value = topo.remote_ip;
  $('topologySetupBack').hidden = false;
}
function topologyResume() { goTab('tunnel'); topologyReload(); }
function topologySelectForTunnel(kind) {
  const id = topologyTunnelKey(kind);
  const t = topologyData.topologies.find(t => (t.tunnel_ids || []).includes(id));
  if (t) topologySelected = t.id;
}
function topologyDisplayMode() { topologyRender(); }

$('topologyCreateBtn').onclick = topologyCreate;
$('topologyBackBtn').onclick = topologyBack;
$('topologySaveBtn').onclick = topologySave;
$('topologyAttachBtn').onclick = topologyAttach;
$('topologySetupBack').onclick = topologyResume;
$('tunnelsAddBtn').onclick = topologyNewTunnel;
$('topologyAddTunnelBtn').onclick = topologyNewTunnel;
document.querySelectorAll('[data-topo-role]').forEach(b => { b.onclick = () => topologyChooseRole(b.dataset.topoRole); });
$('tab-tunnel').addEventListener('click', e => {
  const target = e.target.closest('button'); if (!target) return;
  if (target.dataset.topoOpen) topologyOpen(target.dataset.topoOpen);
  if (target.dataset.topoDelete) topologyDelete(target.dataset.topoDelete);
  if (target.dataset.topoDetach) topologyDetach(target.dataset.topoDetach);
  if (target.dataset.topoViewTunnel) {
    const kind = topologyKind(target.dataset.topoViewTunnel);
    if (target.dataset.topoViewTunnel.startsWith('xray:')) { goTab('xray'); return; }
    if (target.dataset.topoViewTunnel.startsWith('bagher:')) { openTunnelDetail('bagher'); return; }
    openTunnelDetail(kind);
  }
});
setInterval(() => {
  const tab = $('tab-tunnel');
  if (tab && !tab.hidden && !document.hidden && !topologyBusy && !_tunnelDetail) topologyReload();
}, 15000);
