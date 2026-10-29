// A topology groups existing tunnels between a pair of servers. Only an empty
// grouping can be deleted; tunnels and engines are never removed automatically.
let topologyData = { topologies: [], tunnels: [] };
let topologySelected = null;
let topologySearchQuery = '';
let topologyViewMode = 'list';
let topologyBusy = false;
let topologyLoading = false;

function topologyCurrent() {
  return topologyData.topologies.find(t => t.id === topologySelected) || null;
}
function topologyExistingTunnels(topo) {
  const bound = new Set(topo ? topo.tunnel_ids || [] : []);
  const known = new Map();
  topologyData.tunnels.forEach(t => {
    if (bound.has(t.id) && !known.has(t.id)) known.set(t.id, t);
  });
  return [...known.values()];
}
function topologyExistingTunnelCount(topo) {
  return topologyExistingTunnels(topo).length;
}
function topologyListStatus(tunnels) {
  if (!tunnels.length) return { key: 'empty', label: tr('', 'Empty') };
  const states = tunnels.map(t => String(t.state || '').toLowerCase());
  if (states.every(s => ['healthy', 'ok', 'up', 'connected'].includes(s))) {
    return { key: 'healthy', label: tr('', 'Healthy') };
  }
  if (states.every(s => ['down', 'err', 'error', 'failed', 'stopped', 'off'].includes(s))) {
    return { key: 'down', label: tr('', 'Down') };
  }
  return { key: 'degraded', label: tr('', 'Degraded') };
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
function topologyEndpointPair(topo) {
  return {
    iran: topo.role === 'foreign' ? topo.remote_ip : topo.local_ip,
    foreign: topo.role === 'foreign' ? topo.local_ip : topo.remote_ip
  };
}
function topologySearchMatches(t, tunnels) {
  const query = topologySearchQuery.trim().toLowerCase();
  if (!query) return true;
  const endpoints = topologyEndpointPair(t);
  return [t.name, endpoints.iran, endpoints.foreign, topologyListStatus(tunnels).label]
    .some(value => String(value || '').toLowerCase().includes(query));
}
function topologyRender() {
  const topo = topologyCurrent();
  const inTunnel = typeof _tunnelDetail !== 'undefined' && _tunnelDetail !== null;
  $('topologyListCard').hidden = !!topo || inTunnel;
  $('topologyDetailCard').hidden = !topo || inTunnel;
  $('topologySetupBack').hidden = !topo;
  $('rscBanner').classList.toggle('td-hide', !inTunnel);
  $('dialCard').classList.toggle('td-hide', !inTunnel);
  const error = $('topologyError'); if (error) { error.hidden = true; error.textContent = ''; }
  $('tunnelsAddBtn').disabled = !topo || !topo.local_ip || !topo.remote_ip;
  $('topologyAddTunnelBtn').disabled = !topo || !topo.local_ip || !topo.remote_ip;
  $('tunnelsEmpty').hidden = !topo || inTunnel;
  if (!topo) $('tunnelsListCard').hidden = true;
  $('topologyCount').textContent = String(topologyData.topologies.length);
  const listTable = $('topologyListTable');
  if (listTable && listTable.classList) listTable.classList.toggle('topo-grid', topologyViewMode === 'grid');
  const searching = topologySearchQuery.trim() !== '';
  const visible = topologyData.topologies.filter(t => topologySearchMatches(t, topologyExistingTunnels(t)));
  const swapIcon = '<svg class="topo-icon topo-swap" viewBox="0 0 24 24" aria-hidden="true"><path d="M7 4 3 8l4 4"/><path d="M3 8h17"/><path d="M17 20l4-4-4-4"/><path d="M21 16H4"/></svg>';
  const pairLabel = `${tr('', 'Iran')} – ${tr('', 'Foreign')}`;
  $('topologiesList').innerHTML = visible.length
    ? visible.map(t => {
        const tunnels = topologyExistingTunnels(t);
        const remaining = tunnels.length;
        const status = topologyListStatus(tunnels);
        const endpoints = topologyEndpointPair(t);
        const countLabel = tr('', '{n} existing tunnel(s)').replace('{n}', String(remaining));
        const deleteTitle = remaining ? topologyDeleteNotice(remaining) : tr('', 'Delete topology');
        return `<tr class="topo-list-row" role="row" data-topo-id="${esc(t.id)}">
        <td role="cell" class="topo-cell-name" data-label="${esc(tr('', 'Name'))}"><button type="button" class="topo-name" data-topo-open="${esc(t.id)}"><span class="topo-dot" data-topo-status="${status.key}" aria-hidden="true"></span><bdi>${esc(t.name)}</bdi></button></td>
        <td role="cell" class="topo-cell-servers" data-label="${esc(tr('', 'Server pair'))}"><div class="topo-server-pair"><span class="topo-location">${esc(pairLabel)}</span><span class="topo-ips"><bdi class="u-mono" dir="ltr">${esc(endpoints.iran || '—')}</bdi>${swapIcon}<bdi class="u-mono" dir="ltr">${esc(endpoints.foreign || '—')}</bdi></span></div></td>
        <td role="cell" class="topo-cell-tunnels" data-label="${esc(tr('', 'Tunnels'))}"><span class="topo-count" aria-label="${esc(countLabel)}">${remaining}</span></td>
        <td role="cell" class="topo-cell-status" data-label="${esc(tr('', 'Status'))}"><span class="topo-status" data-topo-status="${status.key}"><i aria-hidden="true"></i>${esc(status.label)}</span></td>
        <td role="cell" class="topo-cell-actions" data-label="${esc(tr('', 'Actions'))}"><div class="topo-actions"><button type="button" class="hashem-btn hashem-btn-primary topo-view" data-topo-open="${esc(t.id)}">${esc(tr('', 'View'))}</button><span class="topo-delete-wrap" title="${esc(deleteTitle)}"><button type="button" class="hashem-btn topo-delete" data-topo-delete="${esc(t.id)}" aria-label="${esc(tr('', 'Delete topology'))}"${remaining ? ` disabled title="${esc(tr('', 'Delete all tunnels in this topology first'))}"` : ''}><svg class="topo-icon" viewBox="0 0 24 24" aria-hidden="true"><path d="M3 6h18M9 6V3h6v3M5 6l1 15h12l1-15M10 10v7M14 10v7"/></svg>${esc(tr('', 'Delete'))}</button></span></div></td>
      </tr>`;
      }).join('')
    : searching
      ? `<tr class="topo-list-empty"><td colspan="5"><div class="ent-empty"><b>${esc(tr('', 'No topology matches your search.'))}</b><span>${esc(tr('', 'Clear the search to see all topologies.'))}</span></div></td></tr>`
      : `<tr class="topo-list-empty"><td colspan="5"><div class="ent-empty"><b>${esc(tr('', 'No topology yet'))}</b><span>${esc(tr('', 'Create a topology, set the server roles, then add tunnels between them.'))}</span></div></td></tr>`;
  const assigned = new Set(topologyData.topologies.flatMap(t => t.tunnel_ids || []));
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
  if (typeof tabHashWrite === 'function') tabHashWrite('tunnel');
}
function topologyBack() {
  topologySelected = null;
  _tunnelDetail = null;
  applyTunnelDetail(false);
  topologyRender();
  renderTunnelsList();
  if (typeof tabHashWrite === 'function') tabHashWrite('tunnel');
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
  // No visible toast: the disabled delete button's title (topologyDeleteNotice)
  // is the only delivery of the "tunnels still exist" message.
  if (remaining) return;
  if (!await entConfirm(tr('', 'Delete topology?'), tr('', 'This empty topology will be removed.'), tr('', 'Delete topology'))) return;
  try { await api('/api/topologies/' + encodeURIComponent(id), {}, 'DELETE'); await topologyReload(); }
  catch(e) { toastErr(tr('', 'Topology save failed'), e.message); }
}
// ---- New-tunnel wizard: a 3-step modal (type -> settings -> review) that
// replaces the old "jump to the Setup tab" behaviour. It reuses the same
// endpoints the Setup tab uses and never duplicates server-side validation.
const TOPO_WIZ_TYPES = [
  { id: 'frp', label: 'GRE + FRP', desc: 'High-stability Linux GRE kernel tunnel paired with FRP reverse proxy.' },
  { id: 'backhaul', label: 'Backhaul', desc: 'Multiplexed Backhaul tunnel: one engine, with or without GRE under it.' },
  { id: 'bagher', label: 'Bagher', desc: 'Stealth tunnel over an SSH connection pool.' },
  { id: 'matrix', label: 'Matrix', desc: 'Mesh tunnel (tinc): the servers join one virtual network.' },
  { id: 'xray', label: 'Xray', desc: 'Forward ports from the Iran server to a foreign server over Xray: Reality, XHTTP, gRPC or mKCP, direct or reverse.' }
];
const TOPO_WIZ_STEPS = ['Type', 'Settings', 'Review'];
let topoWiz = null;
let topoWizBusy = false;
function topoWizType() {
  return (topoWiz && TOPO_WIZ_TYPES.find(t => t.id === topoWiz.type)) || null;
}
function topoWizShow() {
  const ov = $('topoWizModal');
  if (typeof modalShow === 'function') modalShow(ov, topoWizCancel);
  else ov.hidden = false;
}
function topoWizHide() {
  const ov = $('topoWizModal');
  if (typeof modalHide === 'function') modalHide(ov);
  else ov.hidden = true;
}
function topoWizCancel() {
  if (!topoWiz || topoWizBusy) return;
  topoWiz = null;
  topoWizHide();
}
function topoWizOpen() {
  const topo = topologyCurrent();
  if (!topo) return;
  topoWiz = {
    step: 1, type: '',
    values: { name: topo.name || '', local: topo.local_ip || '', remote: topo.remote_ip || '', gre: true,
      transport: '', port: 0, ports: '', ssh_port: 22, pool: 25, udp: false, family: 'ipv4', addr: '', mode: 'direct' }
  };
  topoWizShow();
  topoWizRender();
}
function topoWizPick(id) {
  if (!topoWiz) return;
  topoWiz.type = id;
  // seed the type's default port so the review never shows an empty one
  const dflt = { frp: 7000, backhaul: 7000, bagher: 55400, matrix: 2097, xray: 443 }[id];
  if (dflt && !topoWiz.values.port) topoWiz.values.port = dflt;
  if (topoWiz.step === 1) { topoWiz.step = 2; topoWizRender(); }
  else topoWizRender();
}
function topoWizError(msg) {
  const el = $('topoWizErr');
  el.textContent = msg || '';
  el.classList.toggle('hidden', !msg);
}
function topoWizRender() {
  if (!topoWiz) return;
  const step = topoWiz.step;
  $('topoWizProgress').textContent = tr('', 'Step {x} of {n}').replace('{x}', String(step)).replace('{n}', String(TOPO_WIZ_STEPS.length));
  $('topoWizSteps').innerHTML = TOPO_WIZ_STEPS.map((lb, i) =>
    `<span class="topo-wiz-step${i + 1 < step ? ' done' : ''}${i + 1 === step ? ' on' : ''}" data-wiz-step="${i + 1}"${i + 1 === step ? ' aria-current="step"' : ''}>${esc(tr('', lb))}</span>`).join('');
  $('topoWizBack').hidden = step === 1;
  const next = $('topoWizNext');
  next.textContent = step === 3 ? tr('', 'Create the tunnel') : tr('', 'Next');
  next.disabled = step === 1 && !topoWiz.type;
  topoWizError('');
  if (step === 1) topoWizRenderTypes();
  else if (step === 2) topoWizRenderFields();
  else topoWizRenderReview();
}
function topoWizRenderTypes() {
  $('topoWizBody').innerHTML = `<p class="qs-hint u-m-0">${esc(tr('', 'Choose the tunnel type'))}</p>`
    + `<div class="topo-wiz-types" role="group" aria-label="${esc(tr('', 'Choose the tunnel type'))}">`
    + TOPO_WIZ_TYPES.map(t => `<button type="button" class="topo-wiz-type${topoWiz.type === t.id ? ' on' : ''}" data-wiz-type="${t.id}" aria-pressed="${topoWiz.type === t.id}"><b>${esc(tr('', t.label))}</b><span>${esc(tr('', t.desc))}</span></button>`).join('')
    + '</div>';
  document.querySelectorAll('#topoWizBody [data-wiz-type]').forEach(b => { b.onclick = () => topoWizPick(b.dataset.wizType); });
}
function topoWizInput(id, label, value, extra) {
  return `<div class="fg"><label class="qs-label" for="${id}">${esc(label)}</label><input class="hashem-input" id="${id}" value="${esc(value)}"${extra ? ' ' + extra : ''}></div>`;
}
function topoWizSelect(id, label, cur, opts) {
  return `<div class="fg"><label class="qs-label" for="${id}">${esc(label)}</label><select class="hashem-input" id="${id}">`
    + opts.map(o => `<option value="${esc(o[0])}"${o[0] === cur ? ' selected' : ''}>${esc(o[1])}</option>`).join('') + '</select></div>';
}
function topoWizCheckbox(id, label, on) {
  return `<div class="fg topo-wiz-check"><label class="qs-label" for="${id}">${esc(label)}</label><input type="checkbox" id="${id}"${on ? ' checked' : ''}></div>`;
}
function topoWizRenderFields() {
  const t = topoWizType(), v = topoWiz.values;
  if (!t) { topoWiz.step = 1; topoWizRender(); return; }
  const frpTrans = [['tcp', 'TCP'], ['kcp', 'KCP'], ['quic', 'QUIC'], ['websocket', 'WebSocket'], ['wss', 'WSS']];
  const bhTrans = [['tcpmux', 'TCPMUX'], ['wssmux', 'WSSMUX'], ['tcp', 'TCP'], ['ws', 'WS'], ['wsmux', 'WSMUX'], ['wss', 'WSS'], ['udp', 'UDP']];
  let html = '';
  if (t.id === 'frp') {
    html = topoWizInput('topoWizFName', tr('', 'Tunnel name'), v.name || '')
      + topoWizInput('topoWizFLocal', tr('', 'Local public IP'), v.local || '', 'dir="ltr"')
      + topoWizInput('topoWizFRemote', tr('', 'Remote public IP'), v.remote || '', 'dir="ltr"')
      + topoWizSelect('topoWizFTransport', tr('', 'Transport'), v.transport || 'tcp', frpTrans)
      + topoWizInput('topoWizFPort', tr('', 'Port'), String(v.port || 7000), 'type="number" min="1" inputmode="numeric"');
  } else if (t.id === 'backhaul') {
    html = topoWizInput('topoWizFName', tr('', 'Tunnel name'), v.name || '')
      + topoWizInput('topoWizFLocal', tr('', 'Local public IP'), v.local || '', 'dir="ltr"')
      + topoWizInput('topoWizFRemote', tr('', 'Remote public IP'), v.remote || '', 'dir="ltr"')
      + `<div class="fg"><span class="qs-label">${esc(tr('', 'GRE under the tunnel'))}</span><div class="qs-seg" role="group"><button type="button" id="topoWizFGreOn" aria-pressed="${!!v.gre}">${esc(tr('', 'With GRE'))}</button><button type="button" id="topoWizFGreOff" aria-pressed="${!v.gre}">${esc(tr('', 'Without GRE'))}</button></div></div>`
      + topoWizSelect('topoWizFTransport', tr('', 'Transport'), v.transport || 'tcpmux', bhTrans)
      + topoWizInput('topoWizFPort', tr('', 'Tunnel port'), String(v.port || 7000), 'type="number" min="1" inputmode="numeric"')
      + topoWizInput('topoWizFPorts', tr('', 'Forwarded ports'), v.ports || '', 'dir="ltr" placeholder="443, 2083"');
  } else if (t.id === 'bagher') {
    html = topoWizInput('topoWizFName', tr('', 'Tunnel name'), v.name || '')
      + topoWizInput('topoWizFRemote', tr('', 'Remote public IP'), v.remote || '', 'dir="ltr"')
      + topoWizInput('topoWizFSshPort', tr('', 'SSH port'), String(v.ssh_port || 22), 'type="number" min="1" inputmode="numeric"')
      + topoWizInput('topoWizFPorts', tr('', 'Forwarded ports'), v.ports || '', 'dir="ltr" placeholder="443, 8443"')
      + topoWizInput('topoWizFPort', tr('', 'FRP bind port'), String(v.port || 55400), 'type="number" min="1" inputmode="numeric"')
      + topoWizInput('topoWizFPool', tr('', 'Pool size'), String(v.pool || 25), 'type="number" min="1" inputmode="numeric"')
      + topoWizCheckbox('topoWizFUdp', 'UDP', !!v.udp);
  } else if (t.id === 'matrix') {
    html = topoWizSelect('topoWizFFamily', tr('', 'Family'), v.family || 'ipv4', [['ipv4', 'IPv4'], ['ipv6', 'IPv6']])
      + (v.family === 'ipv6' ? topoWizInput('topoWizFAddr', tr('', 'This server\'s IPv6 address'), v.addr || '', 'dir="ltr"') : '')
      + topoWizInput('topoWizFPort', tr('', 'Port'), String(v.port || 2097), 'type="number" min="1" inputmode="numeric"')
      + topoWizInput('topoWizFPorts', tr('', 'Forwarded ports'), v.ports || '', 'dir="ltr" placeholder="443, 2083"');
  } else {
    html = topoWizInput('topoWizFName', tr('', 'Tunnel name'), v.name || '')
      + topoWizSelect('topoWizFMode', tr('', 'Mode'), v.mode || 'direct', [['direct', tr('', 'Direct')], ['reverse', tr('', 'Reverse')]])
      + topoWizInput('topoWizFRemote', tr('', 'Remote public IP'), v.remote || '', 'dir="ltr"')
      + topoWizInput('topoWizFPorts', tr('', 'Forwarded ports'), v.ports || '', 'dir="ltr" placeholder="443, 2083"')
      + topoWizInput('topoWizFPort', tr('', 'Tunnel port'), String(v.port || 443), 'type="number" min="1" inputmode="numeric"')
      + topoWizCheckbox('topoWizFUdp', 'UDP', !!v.udp);
  }
  $('topoWizBody').innerHTML = html;
  const v2 = topoWiz.values;
  if ($('topoWizFGreOn')) {
    $('topoWizFGreOn').onclick = () => { v2.gre = true; topoWizRenderFields(); };
    $('topoWizFGreOff').onclick = () => { v2.gre = false; topoWizRenderFields(); };
  }
  if ($('topoWizFFamily')) $('topoWizFFamily').onchange = () => { topoWizRead(); topoWizRenderFields(); };
}
function topoWizRead() {
  if (!topoWiz || topoWiz.step !== 2) return;
  const v = topoWiz.values;
  const str = (id, key) => { const el = $(id); if (el && String(el.value).trim() !== '') v[key] = String(el.value).trim(); };
  const num = (id, key, dflt) => { const el = $(id); if (el && String(el.value).trim() !== '') { const n = parseInt(el.value, 10); v[key] = isNaN(n) ? dflt : n; } };
  const chk = (id, key) => { const el = $(id); if (el) v[key] = !!el.checked; };
  str('topoWizFName', 'name');
  str('topoWizFLocal', 'local');
  str('topoWizFRemote', 'remote');
  str('topoWizFPorts', 'ports');
  str('topoWizFAddr', 'addr');
  num('topoWizFPort', 'port', v.port);
  num('topoWizFSshPort', 'ssh_port', v.ssh_port);
  num('topoWizFPool', 'pool', v.pool);
  str('topoWizFTransport', 'transport');
  str('topoWizFMode', 'mode');
  str('topoWizFFamily', 'family');
  chk('topoWizFUdp', 'udp');
}
function topoWizValidate() {
  const t = topoWizType(), v = topoWiz.values;
  if (!t) return tr('', 'Choose the tunnel type');
  if (t.id !== 'matrix' && !v.remote) return tr('', 'Enter the foreign server public IP.');
  if ((t.id === 'bagher' || t.id === 'xray') && !v.ports) return tr('', 'Enter at least one port to forward, for example 443, 8443.');
  if (!v.port || v.port < 1) return tr('', 'Enter a port number.');
  return '';
}
function topoWizRow(k, val) {
  return `<div class="topo-wiz-row"><span>${esc(k)}</span><bdi>${esc(val || '—')}</bdi></div>`;
}
function topoWizRenderReview() {
  const t = topoWizType(), topo = topologyCurrent(), v = topoWiz.values;
  const rows = [[tr('', 'Topology'), topo ? topo.name : ''], [tr('', 'Type'), tr('', t.label)]];
  if (t.id === 'frp' || t.id === 'backhaul') {
    if (v.name) rows.push([tr('', 'Tunnel name'), v.name]);
    rows.push([tr('', 'Remote public IP'), v.remote || '']);
    if (t.id === 'backhaul') rows.push([tr('', 'GRE under the tunnel'), tr('', v.gre ? 'With GRE' : 'Without GRE')]);
    rows.push([tr('', 'Transport'), (v.transport || '').toUpperCase()]);
    rows.push([tr('', 'Port'), String(v.port || '')]);
    if (v.ports) rows.push([tr('', 'Forwarded ports'), v.ports]);
  } else if (t.id === 'bagher') {
    if (v.name) rows.push([tr('', 'Tunnel name'), v.name]);
    rows.push([tr('', 'Remote public IP'), v.remote || '']);
    rows.push([tr('', 'Forwarded ports'), v.ports || '']);
    rows.push([tr('', 'SSH port'), String(v.ssh_port || 22)]);
    rows.push([tr('', 'FRP bind port'), String(v.port || 55400)]);
    rows.push([tr('', 'Pool size'), String(v.pool || 25)]);
    if (v.udp) rows.push(['UDP', tr('', 'On')]);
  } else if (t.id === 'matrix') {
    rows.push([tr('', 'Family'), (v.family || 'ipv4').toUpperCase()]);
    if (v.family === 'ipv6' && v.addr) rows.push([tr('', 'This server\'s IPv6 address'), v.addr]);
    rows.push([tr('', 'Port'), String(v.port || 2097)]);
    if (v.ports) rows.push([tr('', 'Forwarded ports'), v.ports]);
  } else {
    if (v.name) rows.push([tr('', 'Tunnel name'), v.name]);
    rows.push([tr('', 'Mode'), tr('', v.mode === 'reverse' ? 'Reverse' : 'Direct')]);
    rows.push([tr('', 'Remote public IP'), v.remote || '']);
    rows.push([tr('', 'Forwarded ports'), v.ports || '']);
    rows.push([tr('', 'Tunnel port'), String(v.port || 443)]);
    if (v.udp) rows.push(['UDP', tr('', 'On')]);
  }
  $('topoWizBody').innerHTML = `<div class="topo-wiz-review">${rows.map(r => topoWizRow(r[0], r[1])).join('')}</div>`;
}
function topoWizNext() {
  if (!topoWiz || topoWizBusy) return;
  topoWizRead();
  if (topoWiz.step === 1) {
    if (!topoWiz.type) { topoWizError(tr('', 'Choose the tunnel type')); return; }
    topoWiz.step = 2;
  } else if (topoWiz.step === 2) {
    const err = topoWizValidate();
    if (err) { topoWizError(err); return; }
    topoWiz.step = 3;
  } else { return topoWizCreate(); }
  topoWizRender();
}
function topoWizBack() {
  if (!topoWiz || topoWizBusy || topoWiz.step === 1) return;
  topoWizRead(); // Back keeps everything typed so far
  topoWiz.step = Math.max(1, topoWiz.step - 1);
  topoWizRender();
}
function topoWizPortsList() {
  return String(topoWiz.values.ports || '').split(/[\s,]+/).filter(Boolean).map(x => parseInt(x, 10)).filter(n => n > 0);
}
function topoWizFindNewTunnel(type) {
  const assigned = new Set(topologyData.topologies.flatMap(x => x.tunnel_ids || []));
  const match = {
    frp: t => t.id === 'main' || t.engine === 'frp' || t.engine === 'gre-frp',
    backhaul: t => /backhaul/.test(String(t.engine || '')),
    bagher: t => /^bagher/.test(String(t.id)),
    matrix: t => /matrix/.test(String(t.id)) || /matrix/.test(String(t.engine || '')),
    xray: t => /^xray:/.test(String(t.id))
  }[type];
  if (!match) return null;
  const found = topologyData.tunnels.find(t => !assigned.has(t.id) && match(t));
  return found ? found.id : null;
}
async function topoWizCreate() {
  const t = topoWizType(), topo = topologyCurrent();
  if (!t || !topo || topoWizBusy) return;
  topoWizBusy = true;
  topoWizError('');
  const btn = $('topoWizNext');
  btn.disabled = true;
  const oldTxt = btn.textContent;
  btn.innerHTML = '<span class="ent-spinner"></span> ' + esc(tr('', 'Applying…'));
  const v = topoWiz.values;
  try {
    if (t.id === 'frp' || t.id === 'backhaul') {
      const withGre = t.id === 'frp' || v.gre;
      const iran = topo.role !== 'foreign';
      await api('/api/setup', {
        role: iran ? 'iran' : 'foreign',
        engine: t.id === 'frp' ? 'frp' : (v.gre ? 'gre-backhaul' : 'backhaul'),
        transport: v.transport || (t.id === 'frp' ? 'tcp' : 'tcpmux'),
        frp_transport: t.id === 'frp' ? (v.transport || 'tcp') : '',
        local_public: v.local || '', remote_public: v.remote || '',
        name: v.name || '',
        local_gre: withGre ? (iran ? '10.10.10.2' : '10.10.10.1') : '',
        peer_gre: withGre ? (iran ? '10.10.10.1' : '10.10.10.2') : '',
        frp_port: v.port || 7000, ports: v.ports || '', token: '', force: false
      });
    } else if (t.id === 'bagher') {
      await api('/api/sshfwd', {
        action: 'enable', mode: 'bagher', role: 'iran',
        remote_host: v.remote || '', remote_user: 'root', ssh_port: v.ssh_port || 22,
        ports: v.ports || '', pool_count: v.pool || 25, udp: !!v.udp,
        frp_bind_port: v.port || 55400, name: v.name || undefined
      });
    } else if (t.id === 'matrix') {
      // Multiple Matrix instances: with none on this server use the legacy
      // body (no instance field); otherwise ask for a fresh isolated one.
      let instance = '';
      try {
        const st = await api('/api/matrix/instances');
        const list = Array.isArray(st) ? st : ((st && st.instances) || []);
        if (list.length) instance = 'next';
      } catch (_) { /* endpoint unavailable: keep the legacy first-instance path */ }
      const mxBody = {
        family: v.family || 'ipv4', port: v.port || 2097,
        ports: topoWizPortsList(), addr: v.family === 'ipv6' ? (v.addr || '') : ''
      };
      if (instance) mxBody.instance = instance;
      await api('/api/matrix/setup', mxBody);
    } else {
      const profile = typeof xrPresetOf === 'function' ? xrPresetOf('reality') : { security: 'reality' };
      await api('/api/xray', {
        action: 'create', id: 0, mode: v.mode || 'direct', remote: v.remote || '',
        ports: topoWizPortsList(), udp: !!v.udp, tunnel_port: v.port || 443,
        name: v.name || '', listen: '', public_ip: '', profile
      });
    }
    await topologyReload();
    const tid = topoWizFindNewTunnel(t.id);
    const cur = topologyCurrent();
    if (tid && cur && !(cur.tunnel_ids || []).includes(tid)) {
      await api('/api/topologies/' + encodeURIComponent(cur.id), { tunnel_ids: [...(cur.tunnel_ids || []), tid] }, 'PATCH');
    }
    await topologyReload();
    toastOk(tr('', 'Tunnel created'), tr('', 'The new tunnel is attached to this topology.'));
    topoWiz = null;
    topoWizHide();
  } catch(e) {
    topoWizError(e.message);
    toastErr(tr('', 'Setup failed'), e.message);
  } finally {
    topoWizBusy = false;
    btn.disabled = false;
    btn.textContent = oldTxt;
  }
}
function topologyNewTunnel() {
  const topo = topologyCurrent();
  if (!topo || !topo.local_ip || !topo.remote_ip) { toastWarn(tr('', 'Set server roles and IP addresses first')); return; }
  // The wizard keeps the user on the topology view; no more jumping to Setup.
  topoWizOpen();
}
function topologyResume() { goTab('tunnel'); topologyReload(); }
function topologySelectForTunnel(kind) {
  const id = topologyTunnelKey(kind);
  const t = topologyData.topologies.find(t => (t.tunnel_ids || []).includes(id));
  if (t) topologySelected = t.id;
}
function topologyDisplayMode() { topologyRender(); }

function topologySetView(mode) {
  topologyViewMode = mode === 'grid' ? 'grid' : 'list';
  const listBtn = $('topologyViewListBtn'), gridBtn = $('topologyViewGridBtn');
  if (listBtn && typeof listBtn.setAttribute === 'function') listBtn.setAttribute('aria-pressed', String(topologyViewMode === 'list'));
  if (gridBtn && typeof gridBtn.setAttribute === 'function') gridBtn.setAttribute('aria-pressed', String(topologyViewMode === 'grid'));
  topologyRender();
}
$('topologyCreateBtn').onclick = topologyCreate;
$('topologyBackBtn').onclick = topologyBack;
$('topologySaveBtn').onclick = topologySave;
$('topologyAttachBtn').onclick = topologyAttach;
$('topologySetupBack').onclick = topologyResume;
$('tunnelsAddBtn').onclick = topologyNewTunnel;
$('topologyAddTunnelBtn').onclick = topologyNewTunnel;
$('topoWizCancelBtn').onclick = topoWizCancel;
$('topoWizX').onclick = topoWizCancel;
$('topoWizBack').onclick = topoWizBack;
$('topoWizNext').onclick = topoWizNext;
$('topologySearch').addEventListener('input', e => { topologySearchQuery = (e && e.target ? e.target.value : '') || ''; topologyRender(); });
$('topologyViewListBtn').onclick = () => topologySetView('list');
$('topologyViewGridBtn').onclick = () => topologySetView('grid');
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
