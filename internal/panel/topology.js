// A topology groups existing tunnels between a pair of servers. Only an empty
// grouping can be deleted; tunnels and engines are never removed automatically.
let topologyData = { topologies: [], tunnels: [] };
// Per-side SERVER lists keyed by topology NAME: /api/ui/agents exposes each
// agent's server (datacenter, datacenter_en) plus the topology names it
// serves; /api/mesh/servers contributes the per-server address. A topology
// side renders every server it has — one line per datacenter group.
let topoServersByTopo = {};
let topoDcLoaded = false;
let topologyDcFilter = 'all';
let topoPage = 1;
const TOPO_PAGE_SIZE = 10;
function topoIsFa() { return typeof currentLang !== 'undefined' && currentLang === 'fa'; }
async function topoDcLoad(topo) {
  if (topoDcLoaded) return;
  topoDcLoaded = true;
  try {
    const [ag, sv] = await Promise.all([api('/api/ui/agents'), api('/api/mesh/servers')]);
    window.__topoAgents = (ag.agents || []);
    const addr = {};
    ((sv && sv.servers) || []).forEach(s => { addr[s.id] = s.address || ''; });
    // the established tunnels live in the agent layer: every member agent of
    // the open topology reports the tunnels it actually runs for it
    window.__topoTunnels = [];
    const memberAgs = window.__topoAgents.filter(a => !topo || (a.topologies || []).includes(topo.name));
    const detailRows = await Promise.all(memberAgs.map(a =>
      api('/api/ui/agents/' + encodeURIComponent(a.id)).then(d2 => ({ agent: a, d: d2 })).catch(() => null)));
    detailRows.filter(Boolean).forEach(({ agent, d }) => {
      (d.tunnels || []).forEach(tn => {
        if (topo && tn.topology && tn.topology !== topo.name) return;
        window.__topoTunnels.push({
          key: tn.tunnel_key || '',
          engine: tn.engine || '',
          state: tn.state || '',
          weightPct: tn.weight_pct,
          latency: tn.latency_ms,
          today: tn.today_bytes,
          localName: (tn.local && tn.local.name) || agent.name || '',
          remoteName: (tn.remote && tn.remote.name) || '',
          agentId: agent.id,
        });
      });
    });
    (ag.agents || []).forEach(a => {
      const srv = a.server || {};
      if (!srv.id) return;
      const entry = {
        dc: srv.datacenter || '',
        dcEn: srv.datacenter_en || '',
        cc: String(a.country || '').toUpperCase(),
        ip: addr[srv.id] || '',
        name: srv.name || a.name || '',
      };
      (a.topologies || []).forEach(name => {
        const slot = topoServersByTopo[name] || (topoServersByTopo[name] = {});
        const side = a.role === 'foreign' ? 'foreign' : 'iran';
        const list = slot[side] || (slot[side] = []);
        if (!list.some(x => x.name === entry.name)) list.push(entry);
      });
    });
  } catch (e) { /* lists stay empty; the cells fall back to country names */ }
}
function topoSideServers(t, side) {
  return (topoServersByTopo[t.name] && topoServersByTopo[t.name][side]) || [];
}
function topoDcName(t, side) {
  const list = topoSideServers(t, side);
  if (!list.length) return '';
  const s = list[0];
  return topoIsFa() ? (s.dc || s.dcEn) : (s.dcEn || s.dc);
}
function topoDcCc(t, side) {
  if (side === 'iran') return 'IR';
  const list = topoSideServers(t, side);
  return (list[0] && list[0].cc) || String(t.country || '').toUpperCase();
}
let topologySelected = null;
let topologySearchQuery = '';
let topologyBusy = false;
let topologyLoading = false;
// First /api/topologies round-trip: until it finishes the list shows skeleton
// rows instead of the "No topology yet" empty state (which would flash).
let topologyLoadedOnce = false;

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
// Pair health for the list's status pill: active while at least one tunnel is
// up, down only when every tunnel is cut, unknown otherwise (or when the pair
// has no tunnels to speak of yet).
function topologyHealthOf(topo, tunnels) {
  if (!tunnels.length) return 'unknown';
  const bucket = s => ['healthy', 'ok', 'up', 'connected'].includes(s) ? 'ok'
    : ['down', 'err', 'stopped'].includes(s) ? 'err' : 'warn';
  const states = (tunnels || []).map(t => bucket(t.state));
  if (states.every(s => s === 'err')) return 'down';
  return states.includes('ok') ? 'ok' : 'unknown';
}
function topoMenusClose() {
  document.querySelectorAll('.topo-menu-pop').forEach(p => p.classList.add('hidden'));
  document.querySelectorAll('.topo-kebab[aria-expanded="true"]').forEach(b => b.setAttribute('aria-expanded', 'false'));
}
async function topologyPower(id, active) {
  // The power switch reconfigures live tunnels — confirm before touching it.
  const topo = topologyData.topologies.find(t => t.id === id);
  const name = topo ? topo.name : '';
  const ok = await entConfirm(
    tr('', active ? 'Activate topology?' : 'Deactivate topology?'),
    (name ? name + ' — ' : '') + tr('', active ? 'The tunnels in this topology will be connected.' : 'The tunnels in this topology will be disconnected.'),
    tr('', active ? 'Activate' : 'Deactivate'),
  );
  if (!ok) return;
  try {
    const r = await api('/api/topologies/' + encodeURIComponent(id) + '/active', { active: !!active }, 'POST');
    const bad = (r.results || []).filter(x => !x.ok);
    if (bad.length) toastErr(tr('', 'Some tunnels did not change state'), bad.map(x => (x.id || '') + (x.error ? ': ' + x.error : '')).join(' · '));
    else toastOk(active ? tr('', 'Tunnels connected') : tr('', 'Tunnels disconnected'));
    await topologyReload();
  } catch(e) { toastErr(tr('', 'Topology action failed'), e.message); }
}
// Foreign endpoint country, chosen by the user (ISO 3166-1 alpha-2). The Iran
// side is always the home server; an empty country falls back to a globe icon.
const TOPO_MESH = '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="5" cy="6" r="2.5"/><circle cx="19" cy="6" r="2.5"/><circle cx="12" cy="19" r="2.5"/><path d="M7.5 6h9M6.3 8l4.7 8.8M17.7 8l-4.7 8.8"/></svg>';
// English label of a topology's country ('' when unknown/Other) — fa mode
// translates it through faText (the same phrase table the agents table uses).
function topoCountryName(cc) {
  const i = TOPO_COUNTRIES.findIndex(c => c[0] === String(cc || '').toLowerCase());
  return i < 0 ? '' : TOPO_COUNTRIES[i][1];
}
const TOPO_COUNTRIES = [
  ['de', 'Germany'], ['fi', 'Finland'], ['nl', 'Netherlands'], ['fr', 'France'],
  ['gb', 'United Kingdom'], ['us', 'USA'], ['tr', 'Turkey'], ['ru', 'Russia'],
  ['ua', 'Ukraine'], ['pl', 'Poland'], ['se', 'Sweden'], ['ch', 'Switzerland'],
  ['at', 'Austria'], ['ca', 'Canada'], ['jp', 'Japan'], ['sg', 'Singapore'],
  ['ae', 'UAE'], ['am', 'Armenia'], ['ge', 'Georgia'], ['az', 'Azerbaijan'],
  ['in', 'India'], ['cn', 'China'], ['', 'Other']
];
// Flags are embedded SVG, not emoji: Windows and older browsers render emoji
// regional-indicator pairs as bare letters ("IR") or boxes. Every flag draws
// inside a 20x14 viewBox with simple, recognizable geometry (bands, crosses,
// crescents, cantons) and stays under ~15 shapes. Elements carry explicit
// fill/stroke because the global `svg { fill:none; stroke:currentColor }`
// rule would otherwise strip them or inherit the text colour.
const TOPO_FLAG_DRAW = {
  ir: '<rect x="0" y="0" width="20" height="4.67" fill="#239f40" stroke="none"/>' +
      '<rect x="0" y="4.67" width="20" height="4.66" fill="#ffffff" stroke="none"/>' +
      '<rect x="0" y="9.33" width="20" height="4.67" fill="#da0000" stroke="none"/>' +
      '<path d="M10 4.8c-1.3 1.4-1.3 3.4 0 4.8 1.3-1.4 1.3-3.4 0-4.8z" fill="#da0000" stroke="none"/>' +
      '<path d="M8.3 5.5c-.7 1.4-.3 3 1 3.8M11.7 5.5c.7 1.4.3 3-1 3.8" fill="none" stroke="#da0000" stroke-width=".6"/>',
  de: '<rect x="0" y="0" width="20" height="4.67" fill="#000000" stroke="none"/>' +
      '<rect x="0" y="4.67" width="20" height="4.66" fill="#dd0000" stroke="none"/>' +
      '<rect x="0" y="9.33" width="20" height="4.67" fill="#ffce00" stroke="none"/>',
  fi: '<rect x="0" y="0" width="20" height="14" fill="#ffffff" stroke="none"/>' +
      '<rect x="5.5" y="0" width="3.4" height="14" fill="#003580" stroke="none"/>' +
      '<rect x="0" y="5.3" width="20" height="3.4" fill="#003580" stroke="none"/>',
  nl: '<rect x="0" y="0" width="20" height="4.67" fill="#ae1c28" stroke="none"/>' +
      '<rect x="0" y="4.67" width="20" height="4.66" fill="#ffffff" stroke="none"/>' +
      '<rect x="0" y="9.33" width="20" height="4.67" fill="#21468b" stroke="none"/>',
  fr: '<rect x="0" y="0" width="6.67" height="14" fill="#0055a4" stroke="none"/>' +
      '<rect x="6.67" y="0" width="6.66" height="14" fill="#ffffff" stroke="none"/>' +
      '<rect x="13.33" y="0" width="6.67" height="14" fill="#ef4135" stroke="none"/>',
  gb: '<rect x="0" y="0" width="20" height="14" fill="#012169" stroke="none"/>' +
      '<path d="M0 0l20 14M20 0L0 14" fill="none" stroke="#ffffff" stroke-width="3"/>' +
      '<path d="M0 0l20 14M20 0L0 14" fill="none" stroke="#c8102e" stroke-width="1.2"/>' +
      '<path d="M10 0v14M0 7h20" fill="none" stroke="#ffffff" stroke-width="5"/>' +
      '<path d="M10 0v14M0 7h20" fill="none" stroke="#c8102e" stroke-width="3"/>',
  us: '<rect x="0" y="0" width="20" height="14" fill="#ffffff" stroke="none"/>' +
      '<rect x="0" y="0" width="20" height="2" fill="#b22234" stroke="none"/>' +
      '<rect x="0" y="4" width="20" height="2" fill="#b22234" stroke="none"/>' +
      '<rect x="0" y="8" width="20" height="2" fill="#b22234" stroke="none"/>' +
      '<rect x="0" y="12" width="20" height="2" fill="#b22234" stroke="none"/>' +
      '<rect x="0" y="0" width="9" height="8" fill="#3c3b6e" stroke="none"/>' +
      '<circle cx="2.2" cy="2" r=".7" fill="#ffffff" stroke="none"/>' +
      '<circle cx="6.2" cy="2" r=".7" fill="#ffffff" stroke="none"/>' +
      '<circle cx="4.2" cy="4.4" r=".7" fill="#ffffff" stroke="none"/>' +
      '<circle cx="2.2" cy="6.4" r=".7" fill="#ffffff" stroke="none"/>' +
      '<circle cx="6.2" cy="6.4" r=".7" fill="#ffffff" stroke="none"/>',
  tr: '<rect x="0" y="0" width="20" height="14" fill="#e30a17" stroke="none"/>' +
      '<circle cx="8" cy="7" r="3.5" fill="#ffffff" stroke="none"/>' +
      '<circle cx="8.9" cy="7" r="2.8" fill="#e30a17" stroke="none"/>' +
      '<polygon points="13,5.6 13.33,6.55 14.33,6.57 13.53,7.17 13.82,8.13 13,7.56 12.18,8.13 12.47,7.17 11.67,6.57 12.67,6.55" fill="#ffffff" stroke="none"/>',
  ru: '<rect x="0" y="0" width="20" height="4.67" fill="#ffffff" stroke="none"/>' +
      '<rect x="0" y="4.67" width="20" height="4.66" fill="#0039a6" stroke="none"/>' +
      '<rect x="0" y="9.33" width="20" height="4.67" fill="#d52b1e" stroke="none"/>',
  ua: '<rect x="0" y="0" width="20" height="7" fill="#005bbb" stroke="none"/>' +
      '<rect x="0" y="7" width="20" height="7" fill="#ffd500" stroke="none"/>',
  pl: '<rect x="0" y="0" width="20" height="7" fill="#ffffff" stroke="none"/>' +
      '<rect x="0" y="7" width="20" height="7" fill="#dc143c" stroke="none"/>',
  se: '<rect x="0" y="0" width="20" height="14" fill="#006aa7" stroke="none"/>' +
      '<rect x="5.5" y="0" width="3" height="14" fill="#fecc00" stroke="none"/>' +
      '<rect x="0" y="5.5" width="20" height="3" fill="#fecc00" stroke="none"/>',
  ch: '<rect x="0" y="0" width="20" height="14" fill="#da291c" stroke="none"/>' +
      '<rect x="8.75" y="3.5" width="2.5" height="7" fill="#ffffff" stroke="none"/>' +
      '<rect x="6.5" y="5.75" width="7" height="2.5" fill="#ffffff" stroke="none"/>',
  at: '<rect x="0" y="0" width="20" height="4.67" fill="#ed2939" stroke="none"/>' +
      '<rect x="0" y="4.67" width="20" height="4.66" fill="#ffffff" stroke="none"/>' +
      '<rect x="0" y="9.33" width="20" height="4.67" fill="#ed2939" stroke="none"/>',
  ca: '<rect x="0" y="0" width="20" height="14" fill="#ffffff" stroke="none"/>' +
      '<rect x="0" y="0" width="5" height="14" fill="#d52b1e" stroke="none"/>' +
      '<rect x="15" y="0" width="5" height="14" fill="#d52b1e" stroke="none"/>' +
      '<path d="M10 3.8l.9 1.9 1.7-.5-.5 1.8 1.6 1.2-1.7.7.3 1.9-2.3-.9-2.3.9.3-1.9-1.7-.7 1.6-1.2-.5-1.8 1.7.5z" fill="#d52b1e" stroke="none"/>',
  jp: '<rect x="0" y="0" width="20" height="14" fill="#ffffff" stroke="none"/>' +
      '<circle cx="10" cy="7" r="3" fill="#bc002d" stroke="none"/>',
  sg: '<rect x="0" y="0" width="20" height="14" fill="#ffffff" stroke="none"/>' +
      '<rect x="0" y="0" width="20" height="7" fill="#ed2939" stroke="none"/>' +
      '<circle cx="5" cy="3.5" r="2.6" fill="#ffffff" stroke="none"/>' +
      '<circle cx="5.9" cy="3.5" r="2.2" fill="#ed2939" stroke="none"/>' +
      '<circle cx="9" cy="2.3" r=".55" fill="#ffffff" stroke="none"/>' +
      '<circle cx="10.4" cy="3.5" r=".55" fill="#ffffff" stroke="none"/>' +
      '<circle cx="9" cy="4.7" r=".55" fill="#ffffff" stroke="none"/>',
  ae: '<rect x="0" y="0" width="20" height="14" fill="#ffffff" stroke="none"/>' +
      '<rect x="0" y="0" width="20" height="4.67" fill="#00732f" stroke="none"/>' +
      '<rect x="0" y="9.33" width="20" height="4.67" fill="#000000" stroke="none"/>' +
      '<rect x="0" y="0" width="5" height="14" fill="#ff0000" stroke="none"/>',
  am: '<rect x="0" y="0" width="20" height="4.67" fill="#d90012" stroke="none"/>' +
      '<rect x="0" y="4.67" width="20" height="4.66" fill="#0033a0" stroke="none"/>' +
      '<rect x="0" y="9.33" width="20" height="4.67" fill="#f2a800" stroke="none"/>',
  ge: '<rect x="0" y="0" width="20" height="14" fill="#ffffff" stroke="none"/>' +
      '<rect x="8.75" y="0" width="2.5" height="14" fill="#ff0000" stroke="none"/>' +
      '<rect x="0" y="5.75" width="20" height="2.5" fill="#ff0000" stroke="none"/>' +
      '<path d="M4.5 2v3M3 3.5h3M15.5 2v3M14 3.5h3M4.5 9v3M3 10.5h3M15.5 9v3M14 10.5h3" fill="none" stroke="#ff0000" stroke-width=".9"/>',
  az: '<rect x="0" y="0" width="20" height="4.67" fill="#00b5e2" stroke="none"/>' +
      '<rect x="0" y="4.67" width="20" height="4.66" fill="#ed2939" stroke="none"/>' +
      '<rect x="0" y="9.33" width="20" height="4.67" fill="#3f9c35" stroke="none"/>' +
      '<circle cx="10" cy="7" r="1.9" fill="#ffffff" stroke="none"/>' +
      '<circle cx="10.55" cy="7" r="1.55" fill="#ed2939" stroke="none"/>' +
      '<circle cx="12.2" cy="7" r=".6" fill="#ffffff" stroke="none"/>',
  in: '<rect x="0" y="0" width="20" height="4.67" fill="#ff9933" stroke="none"/>' +
      '<rect x="0" y="4.67" width="20" height="4.66" fill="#ffffff" stroke="none"/>' +
      '<rect x="0" y="9.33" width="20" height="4.67" fill="#138808" stroke="none"/>' +
      '<circle cx="10" cy="7" r="1.6" fill="none" stroke="#000080" stroke-width=".5"/>' +
      '<circle cx="10" cy="7" r=".35" fill="#000080" stroke="none"/>',
  cn: '<rect x="0" y="0" width="20" height="14" fill="#de2910" stroke="none"/>' +
      '<polygon points="5,2.8 5.52,4.48 7.09,4.32 5.84,5.84 6.29,6.78 5,5.88 3.71,6.78 4.16,5.84 2.91,4.32 4.48,4.48" fill="#ffde00" stroke="none"/>' +
      '<circle cx="9.2" cy="2.6" r=".55" fill="#ffde00" stroke="none"/>' +
      '<circle cx="10.6" cy="4.4" r=".55" fill="#ffde00" stroke="none"/>' +
      '<circle cx="10.6" cy="6.6" r=".55" fill="#ffde00" stroke="none"/>' +
      '<circle cx="9.2" cy="8.4" r=".55" fill="#ffde00" stroke="none"/>'
};
// Generic fallback for an empty/unknown country: a stroked globe in the same
// 20x14 footprint so list rows stay visually aligned.
const TOPO_FLAG_GLOBE =
  '<g class="topo-flag-globe">' +
  '<circle cx="10" cy="7" r="5.5" fill="none" stroke="currentColor" stroke-width="1.2"/>' +
  '<path d="M4.5 7h11M10 1.5v11" fill="none" stroke="currentColor" stroke-width="1.2"/>' +
  '<ellipse cx="10" cy="7" rx="2.8" ry="5.5" fill="none" stroke="currentColor" stroke-width="1.2"/>' +
  '</g>';
// Render a country as a small inline SVG flag. `attrs` places the flag inside
// a parent SVG (the topology graph nodes); the frame rect adds the subtle
// rounded border on top of the colours.
function topologyFlagSvg(country, attrs) {
  const cc = String(country || '').toLowerCase();
  const innards = (/^[a-z]{2}$/.test(cc) && TOPO_FLAG_DRAW[cc]) || TOPO_FLAG_GLOBE;
  return `<svg class="topo-flag-svg" viewBox="0 0 20 14" width="20" height="14"${attrs ? ' ' + attrs : ''} aria-hidden="true" focusable="false">` +
    innards +
    '<rect x="0.5" y="0.5" width="19" height="13" rx="2.5" fill="none" stroke="currentColor" stroke-opacity=".3" stroke-width="1"/>' +
    '</svg>';
}
// ---- Custom country dropdown ----
// A native <select> opens as a plain OS popup outside the dialog: it
// overflows the modal and shows no flags. This component replaces it with a
// trigger button (flag + translated name) and an absolutely-positioned
// listbox that stays inside the dialog, using the same embedded SVG flags
// as the rest of the topology UI. The chosen ISO code is written to the
// hidden input `valueId`, so existing readers (`$('topologyCountry').value`
// in the settings PATCH, `$('topoCreateCountry').value` in the create
// wizard, the localStorage draft) keep working unchanged.
const topoDDState = {};
function topoCountryIndex(cc) {
  const i = TOPO_COUNTRIES.findIndex(c => c[0] === String(cc || '').toLowerCase());
  return i < 0 ? TOPO_COUNTRIES.length - 1 : i; // «Other» also covers unknown codes
}
function topoCountryOptionsHtml(hostId, sel, hot) {
  return TOPO_COUNTRIES.map((c, i) =>
    `<div class="topo-dd-opt${i === sel ? ' on' : ''}${i === hot ? ' hot' : ''}" role="option" id="${hostId}Opt${i}" aria-selected="${i === sel}" data-dd-value="${esc(c[0])}">`
    + `<span class="topo-dd-flag" aria-hidden="true">${topologyFlagSvg(c[0])}</span>`
    + `<span class="topo-dd-txt">${esc(tr('', c[1]))}</span></div>`).join('');
}
function topoCountryTriggerHtml(hostId, cc) {
  return `<span class="topo-dd-flag" aria-hidden="true">${topologyFlagSvg(cc)}</span>`
    + `<span class="topo-dd-txt" id="${hostId}Txt">${esc(tr('', TOPO_COUNTRIES[topoCountryIndex(cc)][1]))}</span>`
    + `<svg class="topo-dd-chev" viewBox="0 0 24 24" aria-hidden="true"><path d="M6 9l6 6 6-6"/></svg>`;
}
// Render the dropdown into `hostId` (position:relative, so the listbox can
// never leave the dialog) and wire it. `fieldLabel` names the listbox for
// screen readers; `labelId` names the visible field label so the trigger
// announces «label: value»; `onChange` runs after every pick.
function topoCountryMount(hostId, valueId, fieldLabel, labelId, onChange) {
  const host = $(hostId), val = $(valueId);
  if (!host || !val) return;
  const cc = String(val.value || '').toLowerCase();
  topoDDState[hostId] = { open: false, active: topoCountryIndex(cc), valueId, onChange };
  host.classList.add('topo-dd');
  host.innerHTML =
    `<button type="button" class="quantum-input topo-dd-btn" id="${hostId}Btn" aria-haspopup="listbox" aria-expanded="false" aria-controls="${hostId}List"${labelId ? ` aria-labelledby="${esc(labelId)} ${hostId}Txt"` : ''}>${topoCountryTriggerHtml(hostId, cc)}</button>`
    + `<div class="topo-dd-list hidden" id="${hostId}List" role="listbox" aria-label="${esc(tr('', fieldLabel || ''))}" tabindex="-1">${topoCountryOptionsHtml(hostId, topoCountryIndex(cc), topoCountryIndex(cc))}</div>`;
  const btn = $(hostId + 'Btn');
  if (btn) {
    btn.onclick = () => topoCountrySetOpen(hostId, !topoDDState[hostId].open);
    btn.onkeydown = e => topoCountryKey(hostId, e);
  }
  host.onclick = e => { // one delegated handler covers every option row
    const opt = e && e.target && e.target.closest ? e.target.closest('.topo-dd-opt') : null;
    if (opt) topoCountryPick(hostId, opt.dataset.ddValue);
  };
}
// Repaint the list rows and the trigger's aria-activedescendant: `sel` marks
// the picked option (checkmark), `hot` the keyboard highlight.
function topoCountryPaint(hostId) {
  const st = topoDDState[hostId], list = $(hostId + 'List'), btn = $(hostId + 'Btn');
  if (!st || !list || !btn) return;
  const val = $(st.valueId);
  list.innerHTML = topoCountryOptionsHtml(hostId, topoCountryIndex(val && val.value), st.active);
  btn.setAttribute('aria-activedescendant', `${hostId}Opt${st.active}`);
  if (list.querySelector) {
    const hot = list.querySelector('.topo-dd-opt.hot');
    if (hot && hot.scrollIntoView) hot.scrollIntoView({ block: 'nearest' });
  }
}
function topoCountrySetOpen(hostId, open) {
  const st = topoDDState[hostId], list = $(hostId + 'List'), btn = $(hostId + 'Btn');
  if (!st || !list || !btn || st.open === open) return;
  st.open = open;
  list.classList.toggle('hidden', !open);
  btn.setAttribute('aria-expanded', String(open));
  if (open) topoCountryPaint(hostId);
  else st.active = topoCountryIndex($(st.valueId) && $(st.valueId).value); // reopening highlights the real value again
}
function topoCountryPick(hostId, cc) {
  const st = topoDDState[hostId];
  if (!st) return;
  st.active = topoCountryIndex(cc);
  const val = $(st.valueId);
  if (val) val.value = TOPO_COUNTRIES[st.active][0];
  const btn = $(hostId + 'Btn');
  if (btn) btn.innerHTML = topoCountryTriggerHtml(hostId, TOPO_COUNTRIES[st.active][0]);
  topoCountrySetOpen(hostId, false);
  if (typeof st.onChange === 'function') st.onChange(TOPO_COUNTRIES[st.active][0]);
}
// Keyboard: Enter/Space/Arrows open, arrows move the highlight, Enter/Space
// select, Escape/Tab close. Focus stays on the trigger; the highlight moves
// via aria-activedescendant.
function topoCountryKey(hostId, e) {
  const st = topoDDState[hostId];
  if (!st || !e || !e.key) return;
  const space = e.key === ' ' || e.key === 'Spacebar';
  if (!st.open) {
    if (e.key === 'Enter' || space || e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      topoCountrySetOpen(hostId, true);
    }
    return;
  }
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault();
    st.active = e.key === 'ArrowDown' ? Math.min(TOPO_COUNTRIES.length - 1, st.active + 1) : Math.max(0, st.active - 1);
    topoCountryPaint(hostId);
  } else if (e.key === 'Home' || e.key === 'End') {
    e.preventDefault();
    st.active = e.key === 'Home' ? 0 : TOPO_COUNTRIES.length - 1;
    topoCountryPaint(hostId);
  } else if (e.key === 'Enter' || space) {
    e.preventDefault();
    topoCountryPick(hostId, TOPO_COUNTRIES[st.active][0]);
  } else if (e.key === 'Escape' || e.key === 'Tab') {
    topoCountrySetOpen(hostId, false);
  }
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
  // The tunnels table lives inside the open topology's page: the tunnels
  // assigned to this server pair, plus the unassigned ones still waiting for
  // a topology to attach them. With no topology open the table stays hidden.
  if (!topo) return [];
  const owned = new Set(topologyData.topologies.flatMap(t => t.tunnel_ids || []));
  return rows.filter(row => (topo.tunnel_ids || []).includes(topologyTunnelKey(row.kind)) ||
    !owned.has(topologyTunnelKey(row.kind)));
}
function topologyGraph(topo, tunnels) {
  const text = value => esc(String(value || '—'));
  const iranIp = topo.role === 'foreign' ? topo.remote_ip : topo.local_ip;
  const foreignIp = topo.role === 'foreign' ? topo.local_ip : topo.remote_ip;
  const rtl = typeof currentLang !== 'undefined' && currentLang === 'fa';
  const vert = typeof window !== 'undefined' && window.matchMedia && window.matchMedia('(max-width: 799px)').matches;
  // Per-side server lists (every server the side has, from the agents' own
  // reports), each carrying its datacenter, address and live status.
  const sideServers = side => {
    const list = (topoSideServers(topo, side) || []).slice();
    if (!list.length) {
      list.push({
        name: '', dc: '', cc: side === 'iran' ? 'IR' : String(topo.country || '').toUpperCase(),
        ip: side === 'iran' ? iranIp : foreignIp,
      });
    }
    return list;
  };
  const iranServers = sideServers('iran');
  const foreignServers = sideServers('foreign');
  // Tunnel states map onto the four states the backend really carries:
  // healthy → connected; unknown → unverified/pending; down → stopped; disabled → out of service.
  const stateOf = t => {
    const s = String(t.state || '');
    return ['healthy', 'ok', 'up', 'connected'].includes(s) ? 'ok'
      : s === 'down' ? 'err'
      : s === 'disabled' ? 'off' : 'warn';
  };
  const stateLabel = st => st === 'ok' ? tr('', 'Connected') : st === 'err' ? tr('', 'Stopped') : st === 'off' ? tr('', 'Disabled') : tr('', 'Unverified');
  // The line width follows the tunnel's REAL weight share when the backend
  // provides it (weight_pct 0-100), else the honest default.
  const weightOf = t => {
    const wp = Number(t.weightPct);
    if (isFinite(wp) && wp > 0) return Math.max(2, Math.min(6.5, 2 + wp / 100 * 4.5));
    const w = Number(t.weight);
    if (isFinite(w) && w > 0) return Math.max(2, Math.min(6.5, 1.8 + w / 8));
    return 3;
  };
  const tTxt = (x, y, cls, s2, anchor) => `<text class="${cls}" x="${x}" y="${y}"${anchor ? ` text-anchor="${anchor}"` : ''}>${text(s2)}</text>`;
  const tFlag = (flag, attrs) => topologyFlagSvg(flag, attrs);
  const NW = 236, NH = 96, GAP = 12, HDR = 46;
  const agentTuns = topoAgentTunnels(topo);
  // geometry: two column containers (Iran servers / Foreign servers), the
  // server cards stacked inside, vertically centered against each other
  let W, H;
  let geo = {};
  if (vert) {
    W = 520;
    const iH = HDR + iranServers.length * (NH + GAP) + 10;
    const fH = HDR + foreignServers.length * (NH + GAP) + 10;
    const fY = 14 + iH + (n_() ? 74 : 84);
    H = Math.max(fY + fH + 30, 420);
    geo = {
      iran: { x: 16, y: 14, w: W - 32, list: iranServers },
      foreign: { x: 16, y: fY, w: W - 32, list: foreignServers },
      W, H,
    };
    function n_() { return tunnels.length + agentTuns.length; }
  } else {
    W = 940;
    const rows = Math.max(iranServers.length, foreignServers.length);
    const CW = 306;
    H = Math.max(HDR + rows * (NH + GAP) + 30, 300);
    const fX = rtl ? 16 : W - 16 - CW;
    const iX = rtl ? W - 16 - CW : 16;
    geo = {
      iran: { x: iX, y: 14, w: CW, list: iranServers },
      foreign: { x: fX, y: 14, w: CW, list: foreignServers },
      cols: CW,
      W, H,
    };
  }
  const nodeY = (col, i) => col.y + HDR + i * (NH + GAP);
  // resolve every tunnel to its (iran node, foreign node) pair: the agent
  // rows match by the server name, the inventory rows by the endpoint ip,
  // falling back to the side's first node when unresolved
  function findNodeIdx(list, name, ip) {
    let i = list.findIndex(x => x.name && name && x.name === name);
    if (i < 0 && ip) i = list.findIndex(x => x.ip && x.ip === ip);
    return i >= 0 ? i : 0;
  }
  // the established tunnels from BOTH layers: the agent rows first (what the
  // member agents actually run, resolved by the server name), then any
  // inventory tunnels not covered (resolved by the endpoint ip)
  const resolved = [];
  const seenIds = {};
  agentTuns.forEach(tn => {
    const ii = findNodeIdx(iranServers, tn.localName, null);
    const fi = findNodeIdx(foreignServers, tn.remoteName, null);
    resolved.push({
      id: tn.key, name: tn.key, engine: tn.engine, st: stateOf(tn), weightPct: tn.weightPct,
      ii, fi,
      iranName: (iranServers[ii] || {}).name || '', foreignName: (foreignServers[fi] || {}).name || '',
      agentId: tn.agentId,
    });
    seenIds[tn.key] = true;
  });
  tunnels.forEach(t => {
    if (seenIds[t.id] || seenIds[t.name]) return;
    const localIp = topo.role === 'foreign' ? t.remote_ip : t.local_ip;
    const remoteIp = topo.role === 'foreign' ? t.local_ip : t.remote_ip;
    const ii = findNodeIdx(iranServers, '', localIp), fi = findNodeIdx(foreignServers, '', remoteIp);
    resolved.push({
      id: t.id, name: t.name, engine: t.engine, st: stateOf(t), weightPct: null,
      ii, fi,
      iranName: (iranServers[ii] || {}).name || '', foreignName: (foreignServers[fi] || {}).name || '',
      agentId: null,
    });
  });
  // lanes: the tunnels sharing a node pair spread their bows so parallel
  // cables read separately even though they share the glowing ports
  const byPair = {};
  resolved.forEach(r => {
    const k = r.ii + '>' + r.fi;
    (byPair[k] = byPair[k] || []).push(r);
  });
  Object.values(byPair).forEach(list => list.forEach((r, k) => { r.lane = k - (list.length - 1) / 2; }));
  const crossParity = {};
  resolved.forEach(r => {
    if (r.ii !== r.fi) {
      const k = Math.min(r.ii, r.fi) + '>' + Math.max(r.ii, r.fi);
      crossParity[k] = (crossParity[k] || 0) + 1;
      r.bow = (crossParity[k] % 2 === 1) ? -1 : 1;
    } else {
      r.bow = 0;
    }
  });
  // ports: one glowing connector per server card, on the edge facing the
  // other side; green while ANY of the card's tunnels works (the same
  // optimism the health pill uses), else the worst state it carries
  const portKind = {};
  resolved.forEach(r => {
    const rank = { off: 0, warn: 1, err: 2, ok: 3 };
    const better = (a, b) => rank[a] > rank[b] ? a : b;
    ['iran:' + r.ii, 'foreign:' + r.fi].forEach(k => {
      portKind[k] = portKind[k] ? better(portKind[k], r.st) : r.st;
    });
  });
  const cables = resolved.map(r => {
    const st = r.st;
    const sw = weightOf({ weightPct: r.weightPct });
    let d;
    if (vert) {
      // the vertical stack: ports sit on the card's bottom/top edge, the
      // cables run straight down with a slight lane spread
      const x1 = geo.iran.x + geo.iran.w / 2 + r.lane * 16;
      const y1 = nodeY(geo.iran, r.ii) + NH;
      const x2 = geo.foreign.x + geo.foreign.w / 2 + r.lane * 16;
      const y2 = nodeY(geo.foreign, r.fi);
      const midY = (y1 + y2) / 2;
      d = `M ${x1.toFixed(1)} ${y1} C ${x1.toFixed(1)} ${midY}, ${x2.toFixed(1)} ${midY}, ${x2.toFixed(1)} ${y2}`;
    } else {
      const y1 = nodeY(geo.iran, r.ii) + NH / 2;
      const y2 = nodeY(geo.foreign, r.fi) + NH / 2;
      const x1 = rtl ? geo.iran.x + 12 : geo.iran.x + geo.iran.w - 12;
      const x2 = rtl ? geo.foreign.x + geo.foreign.w - 12 : geo.foreign.x + 12;
      const dx = Math.max(60, (x2 - x1) / 2);
      const sag = Math.max(-90, Math.min(90, r.bow * 30 + r.lane * 9));
      d = `M ${x1},${y1.toFixed(1)} C ${x1 + dx},${(y1 + sag).toFixed(1)} ${x2 - dx},${(y2 + sag).toFixed(1)} ${x2},${y2.toFixed(1)}`;
    }
    return `<g data-topology-cable="${text(r.id)}" data-topo-iran="${text(r.iranName)}" data-topo-foreign="${text(r.foreignName)}" data-topo-agent="${text(r.agentId || '')}" class="cl-e ${st} topo-cable" tabindex="0" role="button" aria-label="${text(r.name)} · ${text(stateLabel(st))}">` +
      `<title>${text(r.name)} · ${text(r.engine)} · ${text(stateLabel(st))}</title>` +
      `<path class="cl-e-hit" d="${d}"/>` +
      `<path class="cl-e-line" style="stroke-width:${sw}" d="${d}"/>` +
      (st === 'ok' ? `<path class="cl-e-flow" d="${d}"/>` : '') +
    `</g>`;
  }).join('');
  const cc = String(topo.country || '').toLowerCase();
  const countryLabel = (TOPO_COUNTRIES.find(c => c[0] === cc) || ['', tr('', 'Other')])[1];
  // server cards, the mockup's way: a big flag tile, the bold name with a
  // status pill (Connected while the agent is online), a pin + datacenter
  // row, a server icon + mono ip row, and ONE glowing port on the edge
  // facing the other side where this card's cables plug in
  const icPin = (ix, iy) => `<g class="topo-n-ic" transform="translate(${ix},${iy})"><path d="M8 1.5a5 5 0 015 5c0 3.6-5 8-5 8s-5-4.4-5-8a5 5 0 015-5z"/><circle cx="8" cy="6.4" r="1.7"/></g>`;
  const icSrv = (ix, iy) => `<g class="topo-n-ic" transform="translate(${ix},${iy})"><rect x="1.5" y="2.5" width="13" height="4.6" rx="1.5"/><rect x="1.5" y="9" width="13" height="4.6" rx="1.5"/><path d="M4.3 4.8h.01M4.3 11.3h.01"/></g>`;
  const node = (col, i, s, side) => {
    const x = col.x + 12, y = nodeY(col, i), w = (col.w || NW) - 24;
    const m = rtl && !vert;
    const agRow = (window.__topoAgents || []).find(a => a.server && a.server.name === s.name);
    const online = !!(agRow && agRow.status === 'online');
    const info = { name: s.name, dc: s.dc, cc: s.cc || (side === 'iran' ? 'IR' : String(topo.country || '').toUpperCase()), ip: s.ip || (side === 'iran' ? iranIp : foreignIp) };
    const title = s.name || (side === 'iran' ? tr('', 'Iran server') : countryLabel);
    const flag = side === 'iran' ? 'ir' : topo.country;
    const pk = portKind[(side === 'iran' ? 'iran:' : 'foreign:') + i] || 'off';
    // status pill: top-right in LTR, top-left in RTL
    let pill = '';
    if (agRow) {
      const lbl = online ? tr('', 'Connected') : tr('', 'Offline');
      const kind = online ? 'ok' : 'err';
      const pw = Math.round(28 + lbl.length * 7);
      const px = m ? x + 12 : x + w - 12 - pw;
      const py = y + 12;
      pill = `<g class="topo-pill"><rect class="topo-pill-bg ${kind}" x="${px}" y="${py}" width="${pw}" height="22" rx="11"/>` +
        `<circle class="topo-pill-dot ${kind}" cx="${px + 11}" cy="${py + 11}" r="3"/>` +
        `<text class="topo-pill-tx ${kind}" x="${px + 19}" y="${py + 15}">${text(lbl)}</text></g>`;
    }
    // the glowing port on the edge facing the other column (top/bottom in
    // the vertical stack)
    let port;
    if (vert) {
      const px = col.x + col.w / 2;
      const py = side === 'iran' ? y + NH : y;
      port = `<circle class="topo-port-halo ${pk}" cx="${px}" cy="${py}" r="9"/><circle class="topo-port ${pk}" cx="${px}" cy="${py}" r="5"/>`;
    } else {
      const px = side === 'iran' ? (rtl ? x : x + w) : (rtl ? x + w : x);
      port = `<circle class="topo-port-halo ${pk}" cx="${px}" cy="${y + NH / 2}" r="9"/><circle class="topo-port ${pk}" cx="${px}" cy="${y + NH / 2}" r="5"/>`;
    }
    // rows: pin + datacenter, then the server icon + ip — mirrored in RTL
    const dcTxt = info.dc ? (info.dc + (info.cc ? ' (' + info.cc + ')' : '')) : (side === 'iran' ? tr('', 'Iran') : topoCountryName(topo.country) || '—');
    const flagX = m ? x + w - 14 - 42 : x + 14;
    const nameTx = m ? tTxt(x + w - 64, y + 34, 'topo-n-name', title, 'end') : tTxt(x + 64, y + 34, 'topo-n-name', title);
    const row = (icon, yy, cls, val) => m
      ? icon(x + w - 14 - 16, yy) + tTxt(x + w - 14 - 22, yy + 13, cls, val, 'end')
      : icon(x + 16, yy) + tTxt(x + 40, yy + 13, cls, val);
    return `<g class="cl-n ${side === 'iran' ? 'topo-node-iran' : 'topo-node-foreign'}" data-agview="${text(agRow ? agRow.id : '')}" data-server-name="${text(s.name || '')}" role="button" tabindex="0" aria-label="${text(title)} · ${text(info.ip)}"><title>${text(title)} · ${text(info.ip)}</title>` +
      `<rect class="cl-n-bg" x="${x}" y="${y}" width="${w}" height="${NH}" rx="12"/>` +
      tFlag(flag, `x="${flagX}" y="${y + 14}" width="42" height="30"`) +
      nameTx +
      pill +
      row(icPin, y + 44, 'topo-n-dc', dcTxt) +
      row(icSrv, y + 66, 'topo-ip', info.ip) +
      port +
      `</g>`;
  };
  const iranNodes = iranServers.map((s, i) => node(geo.iran, i, s, 'iran'));
  const foreignNodes = foreignServers.map((s, i) => node(geo.foreign, i, s, 'foreign'));
  // the column containers: a flag-headed header (flag + bold label + green
  // live count) over the stacked server cards
  const colBox = (col, side, list) => {
    const w = col.w || NW, h = HDR + list.length * (NH + GAP) + 8;
    const lbl = side === 'iran' ? tr('', 'IRAN SERVERS') : tr('', 'FOREIGN SERVERS');
    const m = rtl && !vert;
    const x = col.x - 12, y = col.y - 8;
    const flag = side === 'iran' ? 'ir' : topo.country;
    const fx = m ? x + w + 12 - 14 - 22 : x + 14;
    return `<g class="topo-colbox ${side}"><rect class="topo-col-bg" x="${x}" y="${y}" width="${w + 24}" height="${h}" rx="16"/>` +
      tFlag(flag, `x="${fx}" y="${y + 12}" width="22" height="15"`) +
      (m
        ? `<text class="topo-col-cap" x="${fx - 8}" y="${y + 24}" text-anchor="end">${text(lbl)}<tspan class="topo-col-count"> · ${list.length}</tspan></text>`
        : `<text class="topo-col-cap" x="${x + 42}" y="${y + 24}">${text(lbl)}<tspan class="topo-col-count"> · ${list.length}</tspan></text>`) +
      `</g>`;
  };
  const colBoxes = [];
  colBoxes.push(colBox(geo.iran, 'iran', iranServers));
  colBoxes.push(colBox(geo.foreign, 'foreign', foreignServers));
  // the footer legend: line samples for the four states, then the hint, all
  // in one strip under the map
  const lgLine = (cls, dash) => `<svg class="topo-lg-line" viewBox="0 0 26 8" aria-hidden="true"><line x1="1" y1="4" x2="25" y2="4" class="${cls}"${dash ? ` stroke-dasharray="${dash}"` : ''}/></svg>`;
  const legend = `<div class="topo-map-foot">` +
    `<span class="topo-lg">${lgLine('ok')}${esc(tr('', 'Connected'))}</span>` +
    `<span class="topo-lg">${lgLine('warn', '2 4')}${esc(tr('', 'Unverified'))}</span>` +
    `<span class="topo-lg">${lgLine('err', '2 4')}${esc(tr('', 'Stopped'))}</span>` +
    `<span class="topo-lg">${lgLine('off', '2 4')}${esc(tr('', 'Disabled'))}</span>` +
    `<span class="topo-lg-sep" aria-hidden="true"></span>` +
    `<span class="topo-lg-hint">${esc(tr('', 'Each cable represents one tunnel between the two servers.'))}</span>` +
    `</div>`;
  return `<div class="topo-map"><div class="topo-map-scroll">` +
    `<svg class="topo-graph" viewBox="0 0 ${geo.W} ${Math.round(geo.H)}" role="img" aria-label="${text(tr('', 'Topology connections'))}" dir="ltr">` +
    colBoxes.join('') + cables + iranNodes.join('') + foreignNodes.join('') +
    `</svg></div>` +
    `<div class="topo-tip" hidden></div>` +
    legend +
    `</div>`;
}

// ===== topology members: agents (=servers) join/leave the topology ========
// The established tunnels of the open topology, from the agent layer: every
// member agent reports the tunnels it actually runs for this topology.
// The tunnel set a topology DISPLAYS with: the panel inventory tunnels bound
// to it plus the tunnels its member agents actually report running (deduped
// by id/key). Display paths use this; the attach picker keeps the raw
// inventory (it must only offer truly unassigned tunnels).
function topoAllTunnels(topo) {
  const inv = topologyExistingTunnels(topo);
  const seen = {};
  inv.forEach(t => { seen[t.id || t.name || ''] = true; });
  const extra = topoAgentTunnels(topo).filter(tn => !seen[tn.key]);
  return inv.concat(extra);
}
function topoAgentTunnels(topo) {
  // both endpoints report the same tunnel rows — dedupe by tunnel_key so a
  // tunnel renders once regardless of how many of its agents reported it
  const seen = {};
  return (window.__topoTunnels || []).filter(tn => {
    if (!tn.key || seen[tn.key]) return false;
    seen[tn.key] = true;
    return true;
  });
}
function topoDcReset() { topoDcLoaded = false; topoServersByTopo = {}; }
function renderTopoMembers(topo) {
  const card = $('topoMembersCard');
  if (!card || !topo) { if (card) card.innerHTML = ''; return; }
  const ags = window.__topoAgents || [];
  const members = side => ags.filter(a => a.role === side && (a.topologies || []).includes(topo.name));
  const memRow = (a, side) => '<div class="topo-mem-row">' +
    '<span class="badge ' + (a.status === 'online' ? 'ok' : 'dim') + '">' + esc(a.status || '\\u2014') + '</span>' +
    '<b>' + esc(a.name || '') + '</b>' +
    (a.server && a.server.datacenter ? '<span class="topo-mem-dc">' + esc(topoIsFa() ? (a.server.datacenter || '') : (a.server.datacenter_en || a.server.datacenter || '')) + '</span>' : '') +
    '<button type="button" class="topo-mem-x" data-mem-remove="' + side + '" data-mem-agent="' + esc(a.id) + '" aria-label="' + esc(tr('', 'Remove')) + '" title="' + esc(tr('', 'Remove')) + '">\×</button></div>';
  const sideBlock = (side, lbl) => {
    const list = members(side);
    return '<div class="topo-mem-side"><div class="topo-mem-side-h"><b>' + esc(lbl) + '</b>' +
      '<button type="button" class="quantum-btn quantum-btn-secondary u-btn-xs" data-mem-add="' + side + '">+ ' + esc(tr('', 'Add agent')) + '</button></div>' +
      (list.length ? list.map(a => memRow(a, side)).join('') : '<div class="u-hint-12">' + esc(tr('', 'No agents on this side yet')) + '</div>') + '</div>';
  };
  card.innerHTML = '<div class="tc-header u-flex-wrap-gap8"><p class="tc-title" data-i18n="topo_members_t">' + esc(tr('', 'Agents on this topology')) + '</p></div>' +
    '<div class="topo-mem-grid">' + sideBlock('iran', tr('', 'Iran side')) + sideBlock('foreign', tr('', 'Foreign side')) + '</div>';
}
function topoMembersPickOpen(side) {
  const topo = topologyCurrent();
  if (!topo) return;
  const ags = (window.__topoAgents || []).filter(a => a.role === side && !(a.topologies || []).includes(topo.name));
  const rows = ags.length ? ags.map(a => '<label class="cores-pick-row"><input type="checkbox" value="' + esc(a.id) + '"><b>' + esc(a.name) + '</b>' +
    (a.server && a.server.datacenter ? '<span class="cores-pick-state">' + esc(topoIsFa() ? (a.server.datacenter || '') : (a.server.datacenter_en || a.server.datacenter || '')) + '</span>' : '') + '</label>').join('')
    : '<div class="ent-empty"><b>' + esc(tr('', 'No agents on this side yet')) + '</b></div>';
  let ov = document.getElementById('topoMemPick');
  if (!ov) {
    ov = document.createElement('div');
    ov.id = 'topoMemPick';
    ov.className = 'ent-modal';
    ov.innerHTML = '<div class="ent-card cores-pick-card"><div class="cores-pick-head"><b></b><button type="button" class="mtb-toast-x" data-mem-pick-close>\×</button></div>' +
      '<div class="cores-pick-list"></div><div class="cores-pick-foot"><button type="button" class="quantum-btn quantum-btn-primary" data-mem-pick-go>' + esc(tr('', 'Add')) + '</button>' +
      '<button type="button" class="quantum-btn quantum-btn-secondary" data-mem-pick-close>' + esc(tr('', 'Cancel')) + '</button></div></div>';
    document.body.appendChild(ov);
  }
  ov.dataset.memSide = side;
  ov.querySelector('.cores-pick-head b').textContent = (side === 'iran' ? tr('', 'Iran side') : tr('', 'Foreign side')) + ' \\u2014 ' + tr('', 'Add agent');
  ov.querySelector('.cores-pick-list').innerHTML = rows;
  ov.hidden = false;
}
async function topoMembersPickGo() {
  const ov = document.getElementById('topoMemPick');
  if (!ov) return;
  const topo = topologyCurrent();
  const side = ov.dataset.memSide;
  const ids = Array.from(ov.querySelectorAll('.cores-pick-row input:checked')).map(i => i.value);
  if (!topo || !ids.length) { ov.hidden = true; return; }
  const go = ov.querySelector('[data-mem-pick-go]');
  go.disabled = true;
  const results = await Promise.allSettled(ids.map(id =>
    api('/api/ui/topologies/' + encodeURIComponent(topo.name) + '/members/add', { agent_id: id }, 'POST')));
  const bad = results.filter(r => r.status === 'rejected');
  if (bad.length) toastErr(tr('', 'Agents'), bad.map(r => (r.reason && r.reason.message) || 'HTTP ' + (r.reason && r.reason.status)).join(' · '));
  else toastOk(tr('', 'Agents'), tr('', 'Agent added to the topology'));
  go.disabled = false;
  ov.hidden = true;
  topoDcReset();
  await topologyReload();
}
async function topoMemberRemove(side, agentId) {
  const topo = topologyCurrent();
  if (!topo) return;
  try {
    await api('/api/ui/topologies/' + encodeURIComponent(topo.name) + '/members/remove', { agent_id: agentId }, 'POST');
    toastOk(tr('', 'Agents'), tr('', 'Agent removed'));
  } catch (e) { toastErr(tr('', 'Agents'), e.message || 'HTTP ' + e.status); }
  topoDcReset();
  await topologyReload();
}


// The topbar breadcrumb's back link: while a topology is open on the tunnel
// tab, clicking it closes the detail and paints the list — the native hash
// jump alone leaves the detail view stuck on screen.
document.addEventListener('click', e => {
  const bcb = e.target.closest && e.target.closest('[data-bc-topo-back]');
  if (!bcb) return;
  if (location.hash.indexOf('#t=tunnel') === 0 && typeof topologySelected !== 'undefined' && topologySelected) {
    e.preventDefault();
    topologyBack();
  }
});

// fillHeadCard paints the detail head card's name/pill/actions — called from
// topologyRender AND directly after topoDcLoad completes, so the health pill
// never lags the agent-layer tunnel data.
function fillHeadCard(topo) {
  const hn = $('topologyHeadName');
  if (hn) hn.textContent = topo ? topo.name : '—';
  const pill = $('topologyHeadPill');
  if (!pill || !topo) return;
  // the health counts BOTH sources: the panel inventory and the tunnels
  // the member agents actually report running for this topology
  const own = topoAllTunnels(topo);
  const health = topologyHealthOf(topo, own);
  const cls = !own.length ? 'st-none' : 'st-' + health;
  pill.className = 'topo-state-pill ' + cls;
  const lbl = !own.length ? tr('', 'No tunnels') : tr('', health === 'ok' ? 'Active' : health === 'down' ? 'Down' : 'Unknown');
  const tx = $('topologyHeadPillTx');
  if (tx) tx.textContent = lbl;
  // The delete guard (owner rule): a topology can only be deleted while it
  // is powered off or holds no tunnels at all — otherwise the button is
  // disabled with the reason on its title.
  const del = $('topologyDeleteBtn');
  if (del) {
    const canDelete = topo.Disabled === true || !own.length;
    del.disabled = !canDelete;
    del.title = canDelete ? '' : tr('', 'The topology must be off or empty before it can be deleted');
  }
}

// fillHeadStats paints the head card's bottom stat strip (the owner's mockup):
// each side's server count and datacenter with its flag, the tunnel total
// split into healthy/faulty, and the topology's overall uptime — the healthy
// share of its tunnels (the one honest percentage the live states can give).
function fillHeadStats(topo) {
  const host = $('topologyHeadStats');
  if (!host) return;
  if (!topo) { host.innerHTML = ''; return; }
  const icServer = '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="2" y="4" width="20" height="7" rx="2"/><rect x="2" y="13" width="20" height="7" rx="2"/><path d="M6 7.5h.01M6 16.5h.01"/></svg>';
  const icLink = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M9.5 13.5l5-5"/><path d="M11 7l1.8-1.8a3.6 3.6 0 0 1 5.1 5.1L16 12.2"/><path d="M13 17l-1.8 1.8a3.6 3.6 0 0 1-5.1-5.1L8 12.2"/></svg>';
  const icPulse = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M22 12h-4l-3 9L9 3l-3 9H2"/></svg>';
  const sideStat = side => {
    const list = topoSideServers(topo, side);
    const cc = topoDcCc(topo, side);
    const dcs = [];
    list.forEach(s => {
      const nm = topoIsFa() ? (s.dc || s.dcEn || '') : (s.dcEn || s.dc || '');
      if (nm && dcs.indexOf(nm) < 0) dcs.push(nm);
    });
    const dcTxt = dcs.length ? dcs[0] + (dcs.length > 1 ? ' +' + (dcs.length - 1) : '') : '';
    return '<div class="topo-stat">' +
      '<span class="topo-stat-ic ' + (side === 'iran' ? 'iran' : 'alt') + '">' + icServer + '</span>' +
      '<div class="topo-stat-tx"><b>' + list.length + '</b><span>' + esc(tr('', side === 'iran' ? 'Iran servers' : 'Foreign servers')) + '</span></div></div>' +
      '<div class="topo-stat"><span class="topo-stat-flag">' + topologyFlagSvg(cc) + '</span>' +
      '<div class="topo-stat-tx"><span class="topo-stat-dc">' + esc(dcTxt ? dcTxt + ' (' + cc + ')' : cc) + '</span>' +
      '<span>' + esc(tr('', 'Datacenter')) + '</span></div></div>';
  };
  const own = topoAllTunnels(topo);
  const isOk = t => ['healthy', 'ok', 'up', 'connected'].includes(String(t.state || ''));
  const good = own.filter(isOk).length;
  const bad = own.length - good;
  const pct = own.length ? (Math.round(good / own.length * 1000) / 10) + '%' : '—';
  // the four logical sections (each side's servers+datacenter, the tunnels,
  // the uptime) each take a fair share of the strip's whole width, so no
  // dead space is left at its end
  host.innerHTML =
    '<div class="topo-stat-group">' + sideStat('iran') + '</div>' +
    '<div class="topo-stat-group">' + sideStat('foreign') + '</div>' +
    '<div class="topo-stat-group">' +
      '<div class="topo-stat"><span class="topo-stat-ic">' + icLink + '</span>' +
        '<div class="topo-stat-tx"><b>' + own.length + '</b><span>' + esc(tr('', 'Tunnels')) + '</span></div>' +
        '<div class="topo-stat-mini">' +
          '<span class="topo-stat-k"><i></i>' + esc(good + ' ' + tr('', 'Healthy')) + '</span>' +
          '<span class="topo-stat-k er"><i></i>' + esc(bad + ' ' + tr('', 'Faulty')) + '</span>' +
        '</div></div>' +
    '</div>' +
    '<div class="topo-stat-group">' +
      '<div class="topo-stat upt"><span class="topo-stat-ic upt">' + icPulse + '</span>' +
        '<div class="topo-stat-tx"><b>' + pct + '</b><span>' + esc(tr('', 'Overall uptime')) + '</span></div></div>' +
    '</div>';
}

async function topologyReload() {
  if (topologyLoading) return;
  topologyLoading = true;
  // First load: paint skeleton rows now so the list never flashes the
  // empty state while /api/topologies is in flight.
  if (!topologyLoadedOnce) topologyRender();
  try {
    const r = await api('/api/topologies');
    topologyData = { topologies: r.topologies || [], tunnels: r.tunnels || [] };
    if (topologySelected && !topologyCurrent()) topologySelected = null;
    topologyLoadedOnce = true;
    await topoDcLoad(topologyCurrent());
    fillHeadCard(topologyCurrent());
    fillHeadStats(topologyCurrent());
    topologyRender();
    renderTunnelsList();
  } catch(e) {
    topologyLoadedOnce = true;
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
  return [t.name, endpoints.iran, endpoints.foreign]
    .some(value => String(value || '').toLowerCase().includes(query));
}
// Status filter (All statuses / With tunnels / Without tunnels) and the
// country filter (All countries / per-country) narrow the list rows.
let topologyStatusFilter = 'all';
let topologyCountryFilter = 'all';
function topologyStatusMatches(t, tunnels) {
  if (topologyStatusFilter === 'all') return true;
  const has = tunnels.length > 0;
  return topologyStatusFilter === 'with' ? has : !has;
}
function topologyCountryMatches(t) {
  if (topologyCountryFilter === 'all') return true;
  return String(t.country || '').toLowerCase() === topologyCountryFilter;
}
function topologyRender() {
  const topo = topologyCurrent();
  const inTunnel = typeof _tunnelDetail !== 'undefined' && _tunnelDetail !== null;
  $('topologyListCard').hidden = !!topo || inTunnel;
  $('topologyDetailCard').hidden = !topo || inTunnel;
  // the established-tunnels card: shown while a topology is open and it
  // actually carries tunnels (the merged panel + agent-layer set)
  const tunsCard = $('topologyTunnelsCard');
  if (tunsCard) tunsCard.hidden = !topo || inTunnel || !topoAllTunnels(topo).length;
  const tunsCount = $('topologyTunnelsCount');
  if (tunsCount) tunsCount.textContent = topo ? String(topoAllTunnels(topo).length) : '—';
  // the head card: the topology's own name + health pill + the actions
  const headCard = $('topologyHeadCard');
  if (headCard) headCard.hidden = !topo || inTunnel;
  fillHeadCard(topo);
  fillHeadStats(topo);
  $('topologySetupBack').hidden = !topo;
  $('rscBanner').classList.toggle('td-hide', !inTunnel);
  $('dialCard').classList.toggle('td-hide', !inTunnel);
  const error = $('topologyError'); if (error) { error.hidden = true; error.textContent = ''; }
  $('tunnelsAddBtn').disabled = !topo || !topo.local_ip || !topo.remote_ip;
  $('topologyAddTunnelBtn').disabled = !topo || !topo.local_ip || !topo.remote_ip;
  if (!topo) $('tunnelsListCard').hidden = true;
  // KPI strip: three numbers the page can actually prove from its own data —
  // the server pairs on this panel and how many of them carry tunnels —
  // the two numbers the mockup KPI strip shows beside the total.
  if ($('topoStatPairs')) {
    let tunnelTotal = 0, withT = 0;
    topologyData.topologies.forEach(t => {
      const own = topoAllTunnels(t);
      tunnelTotal += own.length;
      if (own.length) withT++;
    });
    $('topoStatPairs').textContent = String(topologyData.topologies.length);
    $('topoStatWith').textContent = String(withT);
    const wo = $('topoStatWithout');
    if (wo) wo.textContent = String(topologyData.topologies.length - withT);
  }
  const countrySel = $('topologyCountryFilter');
  if (countrySel) {
    const seen = {}, opts = ['<option value="all">' + esc(tr('', 'All countries')) + '</option>'];
    topologyData.topologies.forEach(t => {
      const cc = String(t.country || '').toLowerCase();
      if (cc && !seen[cc]) { seen[cc] = 1; opts.push('<option value="' + esc(cc) + '">' + esc(topoCountryName(cc)) + '</option>'); }
    });
    countrySel.innerHTML = opts.join('');
    countrySel.value = topologyCountryFilter;
  }
  const dcSel = $('topologyDcFilter');
  if (dcSel) {
    const seen = {}, opts = ['<option value="all">' + esc(tr('', 'All datacenters')) + '</option>'];
    topologyData.topologies.forEach(t => {
      ['iran', 'foreign'].forEach(side => {
        topoSideServers(t, side).forEach(s => {
          const dc = topoIsFa() ? (s.dc || s.dcEn) : (s.dcEn || s.dc);
          if (dc && !seen[dc]) { seen[dc] = 1; opts.push('<option value="' + esc(dc) + '">' + esc(dc) + '</option>'); }
        });
      });
    });
    dcSel.innerHTML = opts.join('');
    dcSel.value = topologyDcFilter;
    dcSel.parentElement.hidden = opts.length <= 1; // no dc data resolved: hide the empty filter
  }
  const searching = topologySearchQuery.trim() !== '';
  const all = topologyData.topologies
    .filter(t => topologySearchMatches(t, topologyExistingTunnels(t)))
    .filter(t => topologyStatusMatches(t, topologyExistingTunnels(t)))
    .filter(t => topologyCountryMatches(t))
    .filter(t => topologyDcMatches(t));
  const pages = Math.max(1, Math.ceil(all.length / TOPO_PAGE_SIZE));
  if (topoPage > pages) topoPage = pages;
  const pageFrom = (topoPage - 1) * TOPO_PAGE_SIZE;
  const visible = all.slice(pageFrom, pageFrom + TOPO_PAGE_SIZE);
  const foot = $('topologyFootCount');
  if (foot) foot.textContent = all.length
    ? tr('', 'Showing {a} to {b} of {c} topologies')
        .replace('{a}', String(pageFrom + 1))
        .replace('{b}', String(Math.min(pageFrom + visible.length, all.length)))
        .replace('{c}', String(all.length))
    : tr('', '{n} item(s)').replace('{n}', '0');
  const prev = $('topoPagePrev'), next = $('topoPageNext'), cur = $('topoPageCur');
  if (prev && next && cur) {
    cur.textContent = String(topoPage);
    prev.disabled = topoPage <= 1;
    next.disabled = topoPage >= pages;
    cur.disabled = pages <= 1;
  }
  if (typeof topTitleSync === 'function') { try { topTitleSync(); } catch (e) {} }
  renderTopoMembers(topo);
  $('topologiesList').innerHTML = visible.length
    ? visible.map((t, i) => {
        const tunnels = topoAllTunnels(t);
        const remaining = tunnels.length;
        const endpoints = topologyEndpointPair(t);
        const deleteTitle = remaining ? topologyDeleteNotice(remaining) : tr('', 'Delete topology');
        const health = topologyHealthOf(t, tunnels);
        // No attached tunnel is a state of its own: calling it "Unknown" reads
        // like a failed probe when the pair simply has nothing to probe yet.
        const healthLabel = !remaining
          ? tr('', 'No tunnels')
          : tr('', health === 'ok' ? 'Active' : health === 'down' ? 'Down' : 'Unknown');
        const pillClass = !remaining ? 'st-none' : 'st-' + health;
        const powering = health === 'down' || !remaining;
        // The mockup's server cell, extended for multi-server sides: one block
        // per datacenter — flag, "Datacenter (CC)", the server count, and one
        // mono address line per server. A single-server side keeps the exact
        // mockup line ("1 server • IP").
        const serverCell = (side, flag, ip) => {
          const list = topoSideServers(t, side);
          const ccUp = topoDcCc(t, side);
          const dcLabel = s0 => {
            const dc = s0 ? (topoIsFa() ? (s0.dc || s0.dcEn) : (s0.dcEn || s0.dc)) : '';
            const base = dc || (side === 'iran' ? tr('', 'Iran') : topoCountryName(flag));
            return base && ccUp ? base + ' (' + ccUp + ')' : (base || '—');
          };
          if (list.length <= 1) {
            const line = '1 ' + tr('', 'server') + ' • ' + ((list[0] && list[0].ip) || ip || '—');
            return `<span class="topo-server"><span class="topo-flag" aria-hidden="true">${topologyFlagSvg(flag)}</span><span class="topo-server-tx"><b>${esc(dcLabel(list[0]))}</b><span class="topo-server-sub u-mono" dir="ltr">${esc(line)}</span></span></span>`;
          }
          // group by datacenter: the label line carries the count, the IPs stack below
          const groups = [];
          list.forEach(s => {
            const key = (topoIsFa() ? (s.dc || s.dcEn) : (s.dcEn || s.dc)) || (side === 'iran' ? tr('', 'Iran') : topoCountryName(flag));
            const g = groups.find(x => x.key === key);
            if (g) g.servers.push(s); else groups.push({ key, servers: [s] });
          });
          const blocks = groups.map(g => `<div class="topo-server"><span class="topo-flag" aria-hidden="true">${topologyFlagSvg(flag)}</span><span class="topo-server-tx"><b>${esc(g.key)}<span class="topo-server-count">${esc(' · ' + String(g.servers.length) + ' ' + (g.servers.length === 1 ? tr('', 'server') : tr('', 'servers')))}</span></b>${g.servers.map(s => `<span class="topo-server-sub u-mono" dir="ltr">${esc(s.ip || '—')}</span>`).join('')}</span></div>`).join('');
          return `<div class="topo-server-stack">${blocks}</div>`;
        };
        return `<tr class="topo-list-row" role="row" tabindex="0" data-topo-id="${esc(t.id)}" aria-label="${esc(t.name)}">
        <td role="cell" class="topo-cell-name" data-label="${esc(tr('', 'Topology name'))}"><button type="button" class="topo-name" data-topo-open="${esc(t.id)}"><bdi>${esc(t.name)}</bdi></button></td>
        <td role="cell" class="topo-cell-state" data-label="${esc(tr('', 'Status'))}"><span class="topo-state-pill ${pillClass}"><i aria-hidden="true"></i>${esc(healthLabel)}</span></td>
        <td role="cell" class="topo-cell-tunnels" data-label="${esc(tr('', 'Tunnels'))}">${remaining}</td>
        <td role="cell" class="topo-cell-foreign" data-label="${esc(tr('', 'Foreign server'))}">${serverCell('foreign', t.country, endpoints.foreign)}</td>
        <td role="cell" class="topo-cell-iran" data-label="${esc(tr('', 'Iran servers'))}">${serverCell('iran', 'ir', endpoints.iran)}</td>
        <td role="cell" class="topo-cell-no">${pageFrom + i + 1}</td>
        <td role="cell" class="topo-cell-actions" data-label="${esc(tr('', 'Actions'))}"><div class="topo-actions"><div class="topo-actions-inline"><button type="button" class="topo-inline-btn topo-view" data-topo-open="${esc(t.id)}" aria-label="${esc(tr('', 'View'))}" title="${esc(tr('', 'View'))}"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M2 12s3.5-6 10-6 10 6 10 6-3.5 6-10 6-10-6-10-6z"/><circle cx="12" cy="12" r="2.5"/></svg></button><button type="button" class="topo-inline-btn topo-edit" data-topo-edit="${esc(t.id)}" aria-label="${esc(tr('', 'Edit topology'))}" title="${esc(tr('', 'Edit topology'))}"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 20h4L19.5 8.5a2.1 2.1 0 0 0-3-3L5 17v3z"/><path d="M13.5 6.5l3 3"/></svg></button><span class="topo-delete-wrap" title="${esc(deleteTitle)}"><button type="button" class="topo-inline-btn topo-delete" data-topo-delete="${esc(t.id)}" aria-label="${esc(tr('', 'Delete topology'))}"${remaining ? ` title="${esc(tr('', 'Delete all tunnels in this topology first'))}"` : ''}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 6h18M9 6V3h6v3M5 6l1 15h12l1-15M10 10v7M14 10v7"/></svg></button></span></div><div class="topo-menu"><button type="button" class="topo-kebab" data-topo-menu="${esc(t.id)}" aria-haspopup="menu" aria-expanded="false" aria-label="${esc(tr('', 'Tunnel actions'))}"><svg viewBox="0 0 24 24" width="18" height="18" fill="currentColor" aria-hidden="true"><circle cx="12" cy="5" r="2"/><circle cx="12" cy="12" r="2"/><circle cx="12" cy="19" r="2"/></svg></button><div class="topo-menu-pop hidden" role="menu" aria-label="${esc(tr('', 'Tunnel actions'))}"><button type="button" class="topo-menu-item" role="menuitem" data-topo-open="${esc(t.id)}"><svg class="topo-menu-ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M2 12s3.5-6 10-6 10 6 10 6-3.5 6-10 6-10-6-10-6z"/><circle cx="12" cy="12" r="2.5"/></svg>${esc(tr('', 'View'))}</button><button type="button" class="topo-menu-item" role="menuitem" data-topo-power="${esc(t.id)}" data-active="${powering ? 'true' : 'false'}"><svg class="topo-menu-ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3v8"/><path d="M6.3 6.3a8.5 8.5 0 1 0 11.4 0"/></svg>${esc(powering ? tr('', 'Activate') : tr('', 'Deactivate'))}</button><span class="topo-delete-wrap" title="${esc(deleteTitle)}"><button type="button" class="quantum-btn quantum-btn-danger topo-delete topo-menu-item" role="menuitem" data-topo-delete="${esc(t.id)}" aria-label="${esc(tr('', 'Delete topology'))}"${remaining ? ` title="${esc(tr('', 'Delete all tunnels in this topology first'))}"` : ''}><svg class="topo-menu-ic" viewBox="0 0 24 24" aria-hidden="true"><path d="M3 6h18M9 6V3h6v3M5 6l1 15h12l1-15M10 10v7M14 10v7"/></svg>${esc(tr('', 'Delete'))}</button></span></div></div></div></td>
      </tr>`;
      }).join('')
    : searching
      ? `<tr class="topo-list-empty"><td colspan="7"><div class="ent-empty"><b>${esc(tr('', 'No topology matches your search.'))}</b><span>${esc(tr('', 'Clear the search to see all topologies.'))}</span></div></td></tr>`
      : topologyLoading && !topologyLoadedOnce
        ? `<tr class="topo-list-empty topo-list-skel"><td colspan="7">${skel(2)}</td></tr>`
        : `<tr class="topo-list-empty"><td colspan="7"><div class="mtb-hero"><div class="mtb-hero-in">
            <svg class="mtb-hero-logo" width="110" height="110" viewBox="0 0 100 100" fill="none" aria-hidden="true"><rect width="100" height="100" rx="22" fill="#090d16" stroke="rgba(56,189,248,0.25)" stroke-width="1.5"/><path d="M50 48 C42 38 26 32 12 42 C4 48 1 56 3 62 C6 60 12 54 20 55 C32 56 42 60 50 52 C58 60 68 56 80 55 C88 54 94 60 97 62 C99 56 96 48 88 42 C74 32 58 38 50 48 Z" fill="#38bdf8"/></svg>
            <h3 class="mtb-hero-title">${esc(tr('', 'Build your first topology'))}</h3>
            <p class="mtb-hero-desc">${esc(tr('', 'Set the server roles, then connect tunnels between them.'))}</p>
            <button type="button" class="quantum-btn quantum-btn-primary mtb-hero-cta" data-topo-new="1">+ ${esc(tr('', 'Add topology'))}</button>
            <div class="mtb-hero-doc"><span class="mtb-hero-doc-ico"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6"/><path d="M9 13h6M9 17h6"/></svg></span><span class="mtb-hero-doc-tx"><b>${esc(tr('', 'What is a topology and what is it for?'))}</b><span>${esc(tr('', 'Read the primer in the docs'))}</span></span></div>
          </div></div></td></tr>`;
  // One clear action in the empty state: the same wizard the header button opens.
  const emptyNew = document.querySelector('[data-topo-new]');
  if (emptyNew) emptyNew.onclick = topologyCreate;
  const assigned = new Set(topologyData.topologies.flatMap(t => t.tunnel_ids || []));
  if (!topo) return;
  const cables = (topo.tunnel_ids || []).map(id => topologyData.tunnels.find(t => t.id === id) || {id, name: id, engine: '—', state: 'down'});
  if (topo) $('topologyGraph').innerHTML = topologyGraph(topo, cables); else $('topologyGraph').innerHTML = '';
  // the established tunnels between this pair, listed under the map
  const tl = $('topologyTunnelList');
  if (tl) {
    const agentRows = topo ? topoAgentTunnels(topo) : [];
    const label = st => st === 'ok' ? tr('', 'Connected') : st === 'err' ? tr('', 'Stopped') : st === 'off' ? tr('', 'Disabled') : tr('', 'Unverified');
    const stOf = s => ['healthy', 'ok', 'up', 'connected'].includes(String(s || '')) ? 'ok'
      : String(s || '') === 'down' ? 'err' : String(s || '') === 'disabled' ? 'off' : 'warn';
    tl.innerHTML = agentRows.length
      ? '<div class="topo-tl">' + agentRows.map(tn => {
          const st = stOf(tn.state);
          const lat = tn.latency != null && tn.latency >= 0 ? Math.round(tn.latency) + ' ms' : '\u2014';
          return '<button type="button" class="topo-tl-row" data-topo-agent="' + esc(tn.agentId) + '">' +
            '<span class="topo-tl-dot ' + st + '"></span>' +
            '<b>' + esc(tn.key || '') + '</b>' +
            '<span class="topo-tl-eng">' + esc(tn.engine || '') + '</span>' +
            '<span class="topo-tl-lat u-mono">' + esc(lat) + '</span>' +
            '<span class="topo-tl-st">' + esc(label(st)) + '</span>' +
            '<span class="topo-tl-go">' + esc(tr('', 'View')) + ' \u2190</span>' +
            '</button>';
        }).join('') + '</div>'
      : '';
  }
  // The header's power toggle mirrors the row menu: a pair whose tunnels are
  // not up offers Activate, a live pair offers Deactivate.
  const act = $('topologyActivateBtn'), deact = $('topologyDeactivateBtn');
  if (act && deact) {
    // The header's power toggle mirrors the row menu: a pair whose tunnels are
    // not up offers the green Activate; a live pair offers the red Deactivate.
    const powering = topologyHealthOf(topo, topoAllTunnels(topo)) !== 'ok';
    act.hidden = !powering;
    deact.hidden = powering;
  }
  // Do not overwrite a form the user is currently editing.
  if (!$('topologySettings').contains(document.activeElement)) {
    $('topologyName').value = topo.name;
    $('topologyLocalIP').value = topo.local_ip || '';
    $('topologyRemoteIP').value = topo.remote_ip || '';
    $('topologyCountry').value = topo.country || '';
    topoCountryMount('topologyCountryDD', 'topologyCountry', tr('', 'Foreign server country'), 'topologyCountryLbl');
    topologyChooseRole(topo.role || 'iran');
  }
  $('topologyBoundList').innerHTML = cables.map(t => `<div class="topo-binding"><b>${esc(t.name)}</b><span>${esc(t.state)}</span><button type="button" class="quantum-btn quantum-btn-ghost" data-topo-detach="${esc(t.id)}">${esc(tr('', 'Detach from topology'))}</button><button type="button" class="quantum-btn quantum-btn-secondary" data-topo-view-tunnel="${esc(t.id)}">${esc(tr('', 'View'))}</button></div>`).join('');
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
  topoEditClose();
  applyTunnelDetail(false);
  topologyRender();
  renderTunnelsList();
  if (typeof tabHashWrite === 'function') tabHashWrite('tunnel');
}
// The wizard is the only path that creates a topology: entering the flow or
// closing it mid-way must not leave an empty topology behind (the old code
// POSTed one immediately with blank IPs).
function topologyCreate() { topoCreateOpen(); }
async function topologySave() {
  const topo = topologyCurrent(); if (!topo || topologyBusy) return;
  topologyBusy = true; $('topologySaveBtn').disabled = true;
  try {
    await api('/api/topologies/' + encodeURIComponent(topo.id), {name: $('topologyName').value.trim(), role: $('topologyRole').value, local_ip: $('topologyLocalIP').value.trim(), remote_ip: $('topologyRemoteIP').value.trim(), country: $('topologyCountry').value.trim().toLowerCase()}, 'PATCH');
    await topologyReload(); toastOk(tr('', 'Topology saved')); topoEditClose();
  } catch(e) { toastErr(tr('', 'Topology save failed'), e.message); }
  finally { topologyBusy = false; $('topologySaveBtn').disabled = false; }
}
// Edit dialog: the old inline settings form, now opened from the header's
// Edit action. It reuses the same field ids the save PATCH has always read.
function topoEditShow() {
  const ov = $('topoEditModal');
  if (typeof modalShow === 'function') modalShow(ov, topoEditClose);
  else ov.hidden = false;
}
function topoEditClose() {
  const ov = $('topoEditModal');
  if (typeof modalHide === 'function') modalHide(ov);
  else ov.hidden = true;
}
function topoEditOpen() {
  const topo = topologyCurrent(); if (!topo) return;
  $('topologyName').value = topo.name || '';
  $('topologyLocalIP').value = topo.local_ip || '';
  $('topologyRemoteIP').value = topo.remote_ip || '';
  $('topologyCountry').value = topo.country || '';
  topoCountryMount('topologyCountryDD', 'topologyCountry', tr('', 'Foreign server country'), 'topologyCountryLbl');
  topologyChooseRole(topo.role || 'iran');
  topoEditShow();
}
function topologyEditRow(id) {
  topologySelected = id;
  topoEditOpen();
}
function topologyDcMatches(t) {
  if (topologyDcFilter === 'all') return true;
  return ['iran', 'foreign'].some(side => topoSideServers(t, side).some(s => {
    const dc = topoIsFa() ? (s.dc || s.dcEn) : (s.dcEn || s.dc);
    return dc === topologyDcFilter;
  }));
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
  const tunnelsNow = topo ? topoAllTunnels(topo) : [];
  const off = !topo || topo.Disabled === true;
  if (tunnelsNow.length && !off) {
    // The owner rule: delete needs the topology powered off or empty. A live
    // topology answers with a visible warning instead of a silent no-op.
    toastWarn(tr('', 'Delete topology'), tr('', 'The topology must be off or empty before it can be deleted'));
    return;
  }
  if (!await entConfirm(tr('', 'Delete topology?'), tr('', 'This empty topology will be removed.'), tr('', 'Delete topology'))) return;
  try {
    await api('/api/topologies/' + encodeURIComponent(id), {}, 'DELETE');
    await topologyReload();
    toastOk(tr('', 'Topology deleted'), topo ? topo.name : '');
  }
  catch(e) { toastErr(tr('', 'Topology delete failed'), e.message); }
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
const TOPO_WIZ_BASE_STEPS = ['Type', 'Settings', 'Review'];
// Foreign side: bundle-only. The pasted bundle decides the engine, server and
// ports, so the type step disappears: paste -> review -> create.
const TOPO_WIZ_FOREIGN_STEPS = ['Bundle', 'Review', 'Create'];
let topoWiz = null;
let topoWizBusy = false;
// Role of THIS server, from the dashboard snapshot app.js keeps in
// window._lastLocal: a role containing "foreign" (plain "foreign" or
// "foreign (matrix node)") is the foreign/node side. Everywhere else —
// including a missing snapshot — counts as the Iran/hub side.
function topoWizForeignSide() {
  const local = (typeof window !== 'undefined' && window._lastLocal) || {};
  return /foreign/.test(String(local.role || ''));
}
// Step list for the current wizard state. The foreign side gets the 3-step
// bundle-only flow; the hub side gets one extra final step for the Matrix
// bundle hand-off (type -> settings -> review -> bundle).
function topoWizSteps() {
  if (topoWizForeignSide()) return TOPO_WIZ_FOREIGN_STEPS;
  const steps = [...TOPO_WIZ_BASE_STEPS];
  if (topoWiz && topoWiz.type === 'matrix') steps.push('Bundle');
  return steps;
}
// True once a hub-side Matrix setup succeeded and the wizard shows the bundle
// hand-off screen instead of a step body.
function topoWizOnBundle() {
  return !!(topoWiz && topoWiz.bundle);
}
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
    step: 1, type: '', bundle: '', detect: null,
    values: { name: topo.name || '', local: topo.local_ip || '', remote: topo.remote_ip || '', gre: true,
      transport: '', port: 0, ports: '', ssh_port: 22, pool: 25, udp: false, family: 'ipv4', addr: '', mode: 'direct', bundle: '' }
  };
  topoWizShow();
  topoWizRender();
}
function topoWizPick(id) {
  if (!topoWiz) return;
  topoWiz.type = id;
  // seed the type's default port so the review never shows an empty one
  // (Matrix seeds none: a blank mesh port means "pick a free one automatically")
  const dflt = { frp: 7000, backhaul: 7000, bagher: 55400, xray: 443 }[id];
  if (dflt && !topoWiz.values.port) topoWiz.values.port = dflt;
  if (topoWiz.step === 1) { topoWiz.step = 2; topoWizRender(); }
  else topoWizRender();
}
function topoWizError(msg) {
  const el = $('topoWizErr');
  el.textContent = msg || '';
  el.classList.toggle('hidden', !msg);
}
// ---- Foreign side: bundle-only flow ----
// The pasted text decides everything. Recognised prefixes: hmx1_ (Matrix),
// hsh1_ (main GRE+FRP), bh1_ (Backhaul), gh1_ (GRE-Backhaul) and BAGHER:.
// Returns { kind, parsed } for a known prefix — parsed carries the app.js
// parse (which can still fail: parsed.error then says why) — or null when the
// prefix matches no bundle at all. Parsing only, no API call.
function topoWizDetect(raw) {
  const s = String(raw || '').trim();
  if (!s) return null;
  if (/^hmx1_/.test(s)) return { kind: 'matrix', parsed: null };
  if (typeof isBagherBundle === 'function' && isBagherBundle(s)) return { kind: 'bagher', parsed: null };
  if (/^(hsh1_|bh1_|gh1_)/.test(s)) {
    const parsed = typeof parseBundleClient === 'function' ? parseBundleClient(s) : null;
    return { kind: parsed && !parsed.error ? parsed.engine : '', parsed };
  }
  return null;
}
// Translated engine name for the detection chip and the review.
function topoWizEngineLabel(kind) {
  return tr('', { matrix: 'Matrix', frp: 'GRE + FRP', backhaul: 'Backhaul', 'gre-backhaul': 'Backhaul (with GRE)', bagher: 'Bagher' }[kind] || kind);
}
// Inline error for the pasted bundle: empty paste, unknown prefix, or a
// malformed payload (the parse error text comes from app.js, like the Setup
// tab's join flow). Empty string means the bundle is ready to use.
function topoWizBundleError(raw, det) {
  const s = String(raw || '').trim();
  if (!s) return tr('', 'Paste the hub bundle from the Iran server.');
  if (!det || !det.kind) return tr('', 'Unrecognised bundle: it must start with hmx1_, hsh1_, bh1_, gh1_ or BAGHER:.');
  if (det.parsed && det.parsed.error) return tr('', det.parsed.error);
  return '';
}
// Refresh the auto-detected chip and the inline error under the paste field.
function topoWizPaintBundle() {
  const chip = $('topoWizBundleChip');
  const det = topoWiz ? topoWiz.detect : null;
  const raw = topoWiz ? topoWiz.values.bundle : '';
  if (chip) {
    if (det && det.kind) {
      chip.textContent = tr('', 'Detected: {engine}').replace('{engine}', topoWizEngineLabel(det.kind));
      chip.classList.remove('hidden');
    } else {
      chip.textContent = '';
      chip.classList.add('hidden');
    }
  }
  topoWizError(topoWizBundleError(raw, det));
}
// Live paste handling: reads the textarea, re-detects and repaints the chip.
function topoWizForeignBundleInput() {
  if (!topoWiz) return;
  topoWizRead();
  topoWizPaintBundle();
}
// Step-1 body: a large paste field plus the detection chip. The wizard's hint
// says it all: the bundle carries the type, the server and the ports.
function topoWizRenderForeignPaste() {
  const v = topoWiz.values;
  $('topoWizBody').innerHTML = `<p class="qs-hint u-m-0">${esc(tr('', 'Everything is detected from the bundle: the tunnel type, the server and the ports.'))}</p>`
    + `<div class="fg"><label class="qs-label" for="topoWizFBundle">${esc(tr('', 'Enter the hub bundle here'))}</label>`
    + `<textarea class="quantum-input u-mono topo-wiz-bundle" id="topoWizFBundle" rows="6" dir="ltr" spellcheck="false" autocapitalize="off" autocomplete="off" placeholder="hmx1_... / hsh1_... / bh1_... / gh1_... / BAGHER:...">${esc(v.bundle || '')}</textarea></div>`
    + `<p class="qs-hint u-m-0">${esc(tr('', 'Copy the bundle from the Iran server and paste it here.'))}</p>`
    + `<span class="badge ok hidden" id="topoWizBundleChip" aria-live="polite"></span>`;
  const ta = $('topoWizFBundle');
  if (ta) ta.oninput = topoWizForeignBundleInput;
  topoWizPaintBundle();
}
// Step-2 body: everything the bundle carried, so the paste can be checked
// before creating. Matrix and Bagher bundles are opaque (the server validates
// them), so only the bundle kind and a preview are shown.
function topoWizRenderForeignReview() {
  const topo = topologyCurrent(), v = topoWiz.values, det = topoWiz.detect || {};
  const rows = [[tr('', 'Topology'), topo ? topo.name : '']];
  if (det.kind) rows.push([tr('', 'Type'), topoWizEngineLabel(det.kind)]);
  const p = det.parsed && !det.parsed.error ? det.parsed : null;
  if (p) {
    rows.push([tr('', 'Iran server'), p.iranPub]);
    rows.push([tr('', 'Port'), String(p.port || '')]);
    rows.push([tr('', 'Transport'), String(p.transport || '').toUpperCase()]);
    if (p.iranGre) rows.push([tr('', 'Iran GRE IP'), p.iranGre]);
    if (p.foreignGre) rows.push([tr('', 'Foreign GRE IP'), p.foreignGre]);
    const ports = p.rawPorts || (p.ports || []).join(', ');
    if (ports) rows.push([tr('', 'Forwarded ports'), ports]);
    if (p.udp) rows.push(['UDP', tr('', 'On')]);
  } else {
    rows.push([tr('', 'Hub bundle'), v.bundle ? String(v.bundle).slice(0, 21) + '…' : '']);
  }
  $('topoWizBody').innerHTML = `<div class="topo-wiz-review">${rows.map(r => topoWizRow(r[0], r[1])).join('')}</div>`;
}
// Fields the wizard pins instead of reading the hidden Setup-tab join form:
// the bundle carries the ports, and the FRP transport rides along exactly as
// the Setup tab's own paste-sync would have set it.
function topoWizBundleOverrides(det) {
  const p = det && det.parsed;
  if (!p || p.error) return { name: '' };
  return {
    ports: p.rawPorts || (p.ports && p.ports.length ? p.ports.join(', ') : undefined),
    frp_transport: p.engine === 'frp' ? (p.transport || 'tcp') : undefined,
    name: ''
  };
}
function topoWizRender() {
  if (!topoWiz) return;
  const steps = topoWizSteps();
  const step = topoWiz.step, bundle = topoWizOnBundle();
  const foreign = topoWizForeignSide();
  $('topoWizProgress').textContent = tr('', 'Step {x} of {n}').replace('{x}', String(step)).replace('{n}', String(steps.length));
  $('topoWizSteps').innerHTML = steps.map((lb, i) =>
    `<span class="topo-wiz-step${i + 1 < step ? ' done' : ''}${i + 1 === step ? ' on' : ''}" data-wiz-step="${i + 1}"${i + 1 === step ? ' aria-current="step"' : ''}>${esc(tr('', lb))}</span>`).join('');
  // The bundle hand-off screen is terminal: Back would only confuse after
  // the tunnel already exists; the header × still closes it.
  $('topoWizBack').hidden = step === 1 || bundle;
  $('topoWizFoot').classList.toggle('next-only', step === 1 || bundle);
  const next = $('topoWizNext');
  // The create caption sits on the step whose Next launches creation: the
  // review (step 2) on the foreign bundle flow, the review (step 3) on the hub.
  const createAt = foreign ? 2 : 3;
  next.textContent = bundle ? tr('', 'Done') : (step >= createAt ? tr('', 'Create the tunnel') : tr('', 'Next'));
  next.disabled = step === 1 && !topoWiz.type && !foreign;
  topoWizError('');
  if (foreign) { // bundle-only flow: paste -> review -> create
    if (step === 1) topoWizRenderForeignPaste();
    else topoWizRenderForeignReview();
    return;
  }
  if (step === 1) topoWizRenderTypes();
  else if (step === 2) topoWizRenderFields();
  else if (bundle) topoWizRenderBundle();
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
  return `<div class="fg"><label class="qs-label" for="${id}">${esc(label)}</label><input class="quantum-input" id="${id}" value="${esc(value)}"${extra ? ' ' + extra : ''}></div>`;
}
function topoWizSelect(id, label, cur, opts) {
  return `<div class="fg"><label class="qs-label" for="${id}">${esc(label)}</label><select class="quantum-input" id="${id}">`
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
      + topoWizInput('topoWizFPort', tr('', 'Mesh port'), String(v.port || ''), 'type="number" min="0" inputmode="numeric" placeholder="0"')
      + `<p class="qs-hint u-m-0">${esc(tr('', 'A free TCP+UDP port for the tinc mesh — NOT the traffic port. Leave empty/0 to pick automatically.'))}</p>`
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
  if (!topoWiz) return;
  // Foreign side: the only field is the bundle textarea on step 1; its paste
  // is re-detected here too, so a programmatic value change (no input event)
  // still lands in the wizard state.
  if (topoWizForeignSide()) {
    if (topoWiz.step !== 1) return;
    const bundleEl = $('topoWizFBundle');
    if (bundleEl) {
      topoWiz.values.bundle = String(bundleEl.value || '').trim();
      topoWiz.detect = topoWizDetect(topoWiz.values.bundle);
    }
    return;
  }
  if (topoWiz.step !== 2) return;
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
  // A blank Matrix mesh port reads as 0: the backend then picks a free port
  // instead of silently forcing a default that may be occupied.
  const portEl = $('topoWizFPort');
  const wt = topoWizType();
  if (portEl && wt && wt.id === 'matrix' && String(portEl.value).trim() === '') v.port = 0;
}
function topoWizValidate() {
  const t = topoWizType(), v = topoWiz.values;
  // Foreign side: the bundle is the only input — an empty paste, an unknown
  // prefix or a malformed payload is rejected inline, before any API call.
  if (topoWizForeignSide()) return topoWizBundleError(v.bundle, topoWiz.detect);
  if (!t) return tr('', 'Choose the tunnel type');
  if (t.id !== 'matrix' && !v.remote) return tr('', 'Enter the foreign server public IP.');
  if ((t.id === 'bagher' || t.id === 'xray') && !v.ports) return tr('', 'Enter at least one port to forward, for example 443, 8443.');
  // The Matrix mesh port is optional: blank/0 asks the server to pick a free one.
  if (t.id === 'matrix') {
    if (v.port && (v.port < 0 || v.port > 65535)) return tr('', 'Enter a port number.');
  } else if (!v.port || v.port < 1) {
    return tr('', 'Enter a port number.');
  }
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
    rows.push([tr('', 'Mesh port'), v.port ? String(v.port) : tr('', 'Automatic')]);
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
// Final hub-side screen: the bundle /api/matrix/setup returned, ready to copy
// to the foreign server. Readonly and monospace so a triple-click selects the
// whole bundle even without the copy button.
function topoWizRenderBundle() {
  if (!topoWiz) return;
  const bundle = String(topoWiz.bundle || '');
  $('topoWizBody').innerHTML = `<p class="qs-hint u-m-0">${esc(tr('', 'The Matrix tunnel is ready.'))}</p>`
    + `<div class="fg"><label class="qs-label" for="topoWizBundleOut">${esc(tr('', 'Matrix bundle'))}</label>`
    + `<textarea class="quantum-input u-mono topo-wiz-bundle" id="topoWizBundleOut" rows="6" readonly dir="ltr" spellcheck="false" autocapitalize="off" autocomplete="off">${esc(bundle)}</textarea></div>`
    + `<p class="qs-hint u-m-0">${esc(tr('', 'Copy this bundle and paste it on the foreign server, in the new-tunnel wizard under Matrix.'))}</p>`
    + `<div class="topo-wiz-bundle-actions"><button type="button" class="quantum-btn quantum-btn-primary" id="topoWizCopyBundle">${esc(tr('', 'Copy bundle'))}</button></div>`;
  const out = $('topoWizBundleOut');
  if (out) out.value = bundle;
  const btn = $('topoWizCopyBundle');
  if (btn) btn.onclick = () => {
    let copied = false;
    // app.js ships copyTextSafe (clipboard API plus fallbacks); the direct
    // navigator.clipboard branch is only a safety net for bare embeds.
    if (typeof copyTextSafe === 'function') {
      copyTextSafe(bundle, tr('', 'Bundle copied'), tr('', 'Paste it on the foreign server.'));
      copied = true;
    } else if (typeof navigator !== 'undefined' && navigator.clipboard) {
      navigator.clipboard.writeText(bundle).then(() => {}, () => {});
      copied = true;
    }
    if (!copied) return;
    btn.textContent = tr('', 'Copied ✓');
    setTimeout(() => { btn.textContent = tr('', 'Copy bundle'); }, 2000);
  };
}
function topoWizNext() {
  if (!topoWiz || topoWizBusy) return;
  if (topoWizOnBundle()) { // «Done» closes the wizard from the bundle screen
    topoWiz = null;
    topoWizHide();
    return;
  }
  topoWizRead();
  const foreign = topoWizForeignSide();
  if (topoWiz.step === 1) {
    const err = topoWizValidate();
    if (err) { topoWizError(err); return; }
    topoWiz.step = 2;
  } else if (topoWiz.step === 2 && !foreign) {
    const err = topoWizValidate();
    if (err) { topoWizError(err); return; }
    topoWiz.step = 3;
  } else { return topoWizCreate(); } // hub review (3) and foreign review (2) both create
  topoWizRender();
}
function topoWizBack() {
  if (!topoWiz || topoWizBusy || topoWiz.step === 1 || topoWizOnBundle()) return;
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
    // /api/setup always keys the single tunnel it configures "main"; the
    // engine string distinguishes plain Backhaul from GRE-Backhaul.
    backhaul: t => /backhaul/.test(String(t.engine || '')),
    'gre-backhaul': t => /backhaul/.test(String(t.engine || '')),
    bagher: t => /^bagher/.test(String(t.id)),
    matrix: t => /matrix/.test(String(t.id)) || /matrix/.test(String(t.engine || '')),
    xray: t => /^xray:/.test(String(t.id))
  }[type];
  if (!match) return null;
  const found = topologyData.tunnels.find(t => !assigned.has(t.id) && match(t));
  return found ? found.id : null;
}
async function topoWizCreate() {
  const topo = topologyCurrent();
  if (!topo || topoWizBusy) return;
  if (topoWizForeignSide()) return topoWizCreateForeign(topo);
  const t = topoWizType();
  if (!t) return;
  topoWizBusy = true;
  topoWizError('');
  const btn = $('topoWizNext');
  btn.disabled = true;
  const oldTxt = btn.textContent;
  btn.innerHTML = '<span class="ent-spinner"></span> ' + esc(tr('', 'Applying…'));
  const v = topoWiz.values;
  let mxResult = null;
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
      // port 0 = "pick a free mesh port automatically": 443-style defaults
      // made setups fail wherever the default port was already taken.
      const mxBody = {
        family: v.family || 'ipv4', port: Number(v.port) || 0,
        ports: topoWizPortsList(), addr: v.family === 'ipv6' ? (v.addr || '') : ''
      };
      if (instance) mxBody.instance = instance;
      mxResult = await api('/api/matrix/setup', mxBody);
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
    // The server answers with the mesh port it picked (or accepted); showing
    // it spares the user a round-trip to the Matrix card to look it up.
    if (t.id === 'matrix' && mxResult && mxResult.port) {
      toastOk(tr('', 'Matrix mesh port: {port}').replace('{port}', String(mxResult.port)));
    }
    if (t.id === 'matrix' && mxResult && mxResult.bundle) {
      // Hub side: the tunnel exists but is useless until its bundle is pasted
      // on the foreign server, so the wizard ends on a bundle hand-off screen
      // instead of closing.
      topoWiz.bundle = String(mxResult.bundle);
      topoWiz.step = topoWizSteps().length;
      topoWizRender();
    } else {
      topoWiz = null;
      topoWizHide();
    }
  } catch(e) {
    topoWizError(e.message);
    // Self-explanatory API errors (the mesh-port code already names the port
    // and the fix) surface as-is instead of wrapped in a generic toast title.
    if (e && e.code === 'E-MATRIX-10') toastErr(e.message);
    else toastErr(tr('', 'Setup failed'), e.message);
  } finally {
    topoWizBusy = false;
    btn.disabled = false;
    // The bundle hand-off screen renames the primary button to «Done»; keep
    // that label instead of restoring the pre-create caption over it.
    btn.textContent = (topoWiz && topoWiz.bundle) ? tr('', 'Done') : oldTxt;
  }
}
// Foreign side, bundle-only creation: the detected bundle kind picks the
// endpoint. hmx1_ keeps its dedicated pairing call; everything else sends the
// exact request the Setup tab's Connect builds (joinRequestBody), so the two
// foreign flows cannot drift. Creation lands on the «Create» header step and
// drops back to the review with an inline error when it fails.
async function topoWizCreateForeign(topo) {
  const det = topoWiz.detect || {};
  const raw = String(topoWiz.values.bundle || '').trim();
  topoWizBusy = true;
  topoWizError('');
  topoWiz.step = 3; // the «Create» header step shows while the request runs
  topoWizRender();
  const btn = $('topoWizNext');
  btn.disabled = true;
  const oldTxt = btn.textContent;
  btn.innerHTML = '<span class="ent-spinner"></span> ' + esc(tr('', 'Applying…'));
  let mxResult = null;
  try {
    if (det.kind === 'matrix') {
      // Node side: pair with the hub using the pasted bundle. Calling
      // /api/matrix/setup here would wrongly create a second hub mesh.
      mxResult = await api('/api/matrix/foreign-setup', { bundle: raw });
    } else {
      const req = await joinRequestBody(raw, topoWizBundleOverrides(det));
      if (req.error) throw new Error(req.error);
      await api(req.path, req.body);
    }
    await topologyReload();
    const tid = topoWizFindNewTunnel(det.kind);
    const cur = topologyCurrent();
    if (tid && cur && !(cur.tunnel_ids || []).includes(tid)) {
      await api('/api/topologies/' + encodeURIComponent(cur.id), { tunnel_ids: [...(cur.tunnel_ids || []), tid] }, 'PATCH');
    }
    await topologyReload();
    toastOk(tr('', 'Tunnel created'), tr('', 'The new tunnel is attached to this topology.'));
    if (det.kind === 'matrix') {
      // The node may have shifted its own local mesh port away from the
      // bundle's (that port only binds the hub); say so in the same toast.
      const localPort = mxResult && mxResult.port
        ? ' ' + tr('', 'Local mesh port: {port}').replace('{port}', String(mxResult.port))
        : '';
      toastOk(tr('', 'Matrix tunnel connected to the hub.') + localPort);
    }
    topoWiz = null;
    topoWizHide();
  } catch (e) {
    if (topoWiz) { topoWiz.step = 2; topoWizRender(); }
    topoWizError(e.message);
    // Self-explanatory API errors surface as-is instead of wrapped in a
    // generic toast title (same rule as the hub side).
    if (e && e.code === 'E-MATRIX-10') toastErr(e.message);
    else toastErr(tr('', 'Setup failed'), e.message);
  } finally {
    topoWizBusy = false;
    btn.disabled = false;
    btn.textContent = topoWiz ? tr('', 'Create the tunnel') : oldTxt;
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

// ---- Add-topology wizard: a 6-step modal (Role -> This server's IP ->
// Other server's IP -> Country -> Name -> Review) that replaces the old
// "instantly POST an empty topology" behaviour. The draft lives in
// localStorage under topoCreateDraft so closing the modal keeps everything
// typed; only a successful create clears it.
const TOPO_CREATE_STEPS = ['Role', 'This server', 'Other server', 'Country', 'Name', 'Review'];
const TOPO_CREATE_DRAFT_KEY = 'topoCreateDraft';
const TOPO_CREATE_FIELDS = { 2: ['topoCreateLocal', 'local'], 3: ['topoCreateRemote', 'remote'], 4: ['topoCreateCountry', 'country'], 5: ['topoCreateName', 'name'] };
let topoCreate = null;
let topoCreateBusy = false;
function topoCreateDraftStore() {
  return (typeof localStorage !== 'undefined') ? localStorage : null;
}
function topoCreateDraftSave() {
  const store = topoCreateDraftStore();
  if (!store || !topoCreate) return;
  try { store.setItem(TOPO_CREATE_DRAFT_KEY, JSON.stringify({ step: topoCreate.step, values: topoCreate.values })); } catch (_) { /* storage unavailable: the wizard still works, just without a draft */ }
}
function topoCreateDraftClear() {
  const store = topoCreateDraftStore();
  if (!store) return;
  try { store.removeItem(TOPO_CREATE_DRAFT_KEY); } catch (_) {}
}
function topoCreateDraftLoad() {
  const store = topoCreateDraftStore();
  if (!store) return null;
  try {
    const d = JSON.parse(store.getItem(TOPO_CREATE_DRAFT_KEY) || 'null');
    if (!d || !/^[1-6]$/.test(String(d.step)) || !d.values || typeof d.values !== 'object') return null;
    const v = d.values;
    return { step: Number(d.step), values: {
      role: v.role === 'foreign' ? 'foreign' : 'iran',
      local: String(v.local || ''), remote: String(v.remote || ''),
      country: String(v.country || ''), name: String(v.name || '')
    } };
  } catch (_) { return null; }
}
function topoCreateShow() {
  const ov = $('topoCreateModal');
  if (typeof modalShow === 'function') modalShow(ov, topoCreateCancel);
  else ov.hidden = false;
}
function topoCreateHide() {
  const ov = $('topoCreateModal');
  if (typeof modalHide === 'function') modalHide(ov);
  else ov.hidden = true;
}
function topoCreateCancel() {
  if (!topoCreate || topoCreateBusy) return;
  topoCreateDraftSave(); // closing keeps the draft for the next visit
  topoCreate = null;
  topoCreateHide();
}
function topoCreateDefaultName() {
  const nameBase = tr('', 'New topology');
  let number = 1;
  while (topologyData.topologies.some(t => t.name === nameBase + ' ' + number)) number++;
  return nameBase + ' ' + number;
}
function topoCreateOpen() {
  if (topoCreate) { topoCreateShow(); topoCreateRender(); return; }
  const draft = topoCreateDraftLoad();
  if (draft) topoCreate = draft;
  else {
    const local = (typeof window !== 'undefined' && window._lastLocal) || {};
    topoCreate = { step: 1, values: {
      role: /foreign/.test(local.role || '') ? 'foreign' : 'iran',
      local: local.local_pub || '', remote: '', country: '', name: topoCreateDefaultName()
    } };
  }
  topoCreateShow();
  topoCreateRender();
}
function topoCreateError(msg) {
  const el = $('topoCreateErr');
  el.textContent = msg || '';
  el.classList.toggle('hidden', !msg);
}
// Circular step counter: a ring whose arc fills with progress, "1/6" inside.
// The text carries its size and stroke:none inline: tokens.css strokes every
// svg at 1.9px, and inherited values beat presentation attributes, so a
// class alone left the number fat and outlined inside the ring.
function topoCreateRing(step, n) {
  const r = 18, c = 2 * Math.PI * r;
  const label = tr('', 'Step {x} of {n}').replace('{x}', String(step)).replace('{n}', String(n));
  return `<svg viewBox="0 0 44 44" width="44" height="44" role="img" aria-label="${esc(label)}">` +
    `<circle class="topo-create-ring-track" cx="22" cy="22" r="${r}"/>` +
    `<circle class="topo-create-ring-arc" cx="22" cy="22" r="${r}" stroke-dasharray="${(c * step / n).toFixed(2)} ${c.toFixed(2)}"/>` +
    `<text class="topo-create-ring-txt" x="22" y="22" text-anchor="middle" dominant-baseline="central" style="font-size:12px;font-weight:700;stroke:none;fill:var(--tx, #f4f4f5)">${step}/${n}</text></svg>`;
}
function topoCreateRender() {
  if (!topoCreate) return;
  const step = topoCreate.step, n = TOPO_CREATE_STEPS.length, v = topoCreate.values;
  $('topoCreateProgress').textContent = tr('', 'Step {x} of {n}').replace('{x}', String(step)).replace('{n}', String(n));
  $('topoCreateRing').innerHTML = topoCreateRing(step, n);
  $('topoCreateDots').innerHTML = TOPO_CREATE_STEPS.map((lb, i) =>
    `<i class="topo-create-dot${i + 1 < step ? ' done' : ''}${i + 1 === step ? ' on' : ''}" title="${esc(tr('', lb))}"></i>`).join('');
  $('topoCreateBack').hidden = step === 1;
  $('topoCreateFoot').classList.toggle('next-only', step === 1);
  const next = $('topoCreateNext');
  next.textContent = step === n ? tr('', 'Create topology') : tr('', 'Next');
  next.disabled = topoCreateBusy;
  topoCreateError('');
  if (step === 1) {
    $('topoCreateBody').innerHTML = `<p class="qs-hint u-m-0">${esc(tr('', 'Choose this server\'s role'))}</p>` +
      `<div class="topo-wiz-types" role="group" aria-label="${esc(tr('', 'Choose this server\'s role'))}">` +
      `<button type="button" class="topo-wiz-type topo-create-role${v.role === 'iran' ? ' on' : ''}" data-create-role="iran" aria-pressed="${v.role === 'iran'}">${topologyFlagSvg('ir')}<b>${esc(tr('', 'Iran'))}</b><span>${esc(tr('', 'This server is inside Iran; the other server is abroad.'))}</span></button>` +
      `<button type="button" class="topo-wiz-type topo-create-role${v.role === 'foreign' ? ' on' : ''}" data-create-role="foreign" aria-pressed="${v.role === 'foreign'}">${topologyFlagSvg('')}<b>${esc(tr('', 'Foreign'))}</b><span>${esc(tr('', 'This server is abroad; the other server is inside Iran.'))}</span></button>` +
      `</div>`;
    document.querySelectorAll('#topoCreateBody [data-create-role]').forEach(b => { b.onclick = () => { topoCreate.values.role = b.dataset.createRole; topoCreateDraftSave(); topoCreateRender(); }; });
  } else if (step === 4) {
    $('topoCreateBody').innerHTML = `<p class="qs-hint u-m-0">${esc(tr('', 'Used for the flag next to the foreign server.'))}</p>` +
      `<div class="topo-create-flag">${topologyFlagSvg(v.country)}</div>` +
      `<div class="fg"><label class="qs-label" id="topoCreateCountryLbl">${esc(tr('', 'Foreign server country'))}</label>` +
      `<input type="hidden" id="topoCreateCountry" value="${esc(v.country || '')}">` +
      `<div id="topoCreateCountryDD"></div></div>`;
    // Custom dropdown: the flag list stays inside the modal (a native
    // <select> popup would overflow it). The hidden input above still
    // carries the code for topoCreateRead() and the draft.
    topoCountryMount('topoCreateCountryDD', 'topoCreateCountry', tr('', 'Foreign server country'), 'topoCreateCountryLbl',
      () => { topoCreateRead(); topoCreateRender(); });
  } else if (step === 6) {
    topoCreateRenderReview();
  } else {
    const [id, key] = TOPO_CREATE_FIELDS[step];
    const label = { local: tr('', "This server's IP"), remote: tr('', "Other server's IP"), name: tr('', 'Topology name') }[key];
    const hint = {
      local: tr('', 'The address this server is reached at, IPv4 or IPv6.'),
      remote: tr('', 'The public address of the other server.'),
      name: tr('', 'Shown in the topology list and on the dashboard.')
    }[key];
    $('topoCreateBody').innerHTML = `<p class="qs-hint u-m-0">${esc(hint)}</p>` +
      `<div class="fg"><label class="qs-label" for="${id}">${esc(label)}</label><input class="quantum-input u-mono" id="${id}" dir="ltr" value="${esc(v[key])}"${key === 'name' ? ' maxlength="120"' : ''}></div>`;
    $(id).oninput = topoCreateRead; // every keystroke lands in the draft
  }
}
function topoCreateRead() {
  if (!topoCreate) return;
  const f = TOPO_CREATE_FIELDS[topoCreate.step];
  if (f) { const el = $(f[0]); if (el) topoCreate.values[f[1]] = String(el.value || '').trim(); }
  topoCreateDraftSave();
}
function topoCreateRow(k, val) {
  return `<div class="topo-wiz-row"><span>${esc(k)}</span><bdi>${esc(val || '—')}</bdi></div>`;
}
function topoCreateRenderReview() {
  const v = topoCreate.values;
  const rows = [
    [tr('', 'Role'), tr('', v.role === 'foreign' ? 'Foreign' : 'Iran')],
    [tr('', "This server's IP"), v.local],
    [tr('', "Other server's IP"), v.remote],
    [tr('', 'Foreign server country'), tr('', (TOPO_COUNTRIES.find(c => c[0] === v.country) || ['', 'Other'])[1])],
    [tr('', 'Topology name'), v.name]
  ];
  $('topoCreateBody').innerHTML = `<div class="topo-wiz-review">${rows.map(r => topoCreateRow(r[0], r[1])).join('')}</div>` +
    `<p class="qs-hint u-m-0">${esc(tr('', 'A topology becomes active once at least one tunnel is added to it.'))}</p>`;
}
function topoCreateNext() {
  if (!topoCreate || topoCreateBusy) return;
  topoCreateRead();
  const v = topoCreate.values, n = TOPO_CREATE_STEPS.length;
  const validIp = s => /^[0-9A-Fa-f.:]{3,45}$/.test(s) && /[.:]/.test(s);
  if (topoCreate.step === 1) {
    if (!v.role) { topoCreateError(tr('', 'Choose this server\'s role')); return; }
  } else if (topoCreate.step === 2) {
    if (!v.local) { topoCreateError(tr('', "Enter this server's IP address.")); return; }
    if (!validIp(v.local)) { topoCreateError(tr('', 'Enter a valid IPv4 or IPv6 address.')); return; }
  } else if (topoCreate.step === 3) {
    if (!v.remote) { topoCreateError(tr('', "Enter the other server's IP address.")); return; }
    if (!validIp(v.remote)) { topoCreateError(tr('', 'Enter a valid IPv4 or IPv6 address.')); return; }
    if (v.remote === v.local) { topoCreateError(tr('', 'The two IP addresses must be different.')); return; }
  } else if (topoCreate.step === 5) {
    if (!v.name) { topoCreateError(tr('', 'Enter a name for the topology.')); return; }
  } else if (topoCreate.step === n) { return topoCreateFinish(); }
  topoCreate.step = Math.min(n, topoCreate.step + 1);
  topoCreateDraftSave();
  topoCreateRender();
}
function topoCreateBack() {
  if (!topoCreate || topoCreateBusy || topoCreate.step === 1) return;
  topoCreateRead(); // Back keeps everything typed so far
  topoCreate.step--;
  topoCreateDraftSave();
  topoCreateRender();
}
async function topoCreateFinish() {
  if (!topoCreate || topoCreateBusy) return;
  topoCreateBusy = true;
  topoCreateError('');
  const btn = $('topoCreateNext');
  btn.disabled = true;
  const oldTxt = btn.textContent;
  btn.innerHTML = '<span class="ent-spinner"></span> ' + esc(tr('', 'Creating…'));
  const v = topoCreate.values;
  try {
    const r = await api('/api/topologies', { name: v.name, role: v.role, local_ip: v.local, remote_ip: v.remote, country: (v.country || '').toLowerCase(), tunnel_ids: [] });
    topoCreateDraftClear();
    topoCreate = null;
    topologySelected = r.topology.id;
    await topologyReload();
    topoCreateHide();
    toastOk(tr('', 'Topology created'), r.topology.name || '');
    const add = $('topologyAddTunnelBtn');
    if (add && add.focus) add.focus();
  } catch (e) {
    topoCreateError(e.message);
    toastErr(tr('', 'Topology create failed'), e.message);
  } finally {
    topoCreateBusy = false;
    btn.disabled = false;
    btn.textContent = oldTxt;
  }
}

$('topologyCreateBtn').onclick = topologyCreate;
$('topologyBackBtn').onclick = topologyBack;
$('topologySaveBtn').onclick = topologySave;
$('topologyEditBtn').onclick = topoEditOpen;
$('topoEditCancel').onclick = topoEditClose;
$('topoEditX').onclick = topoEditClose;
$('topologyActivateBtn').onclick = () => {
  const t = topologyCurrent(); if (!t) return;
  topologyPower(t.id, true);
};
$('topologyDeactivateBtn').onclick = () => {
  const t = topologyCurrent(); if (!t) return;
  topologyPower(t.id, false);
};
$('topologyDeleteBtn').onclick = () => { const t = topologyCurrent(); if (t) topologyDelete(t.id); };
$('topologySetupBack').onclick = topologyResume;
$('tunnelsAddBtn').onclick = topologyNewTunnel;
$('topologyAddTunnelBtn').onclick = topologyNewTunnel;
// The live map: a hover tooltip with the tunnel's real facts, and the
// inline add-tunnel button for the no-tunnel state.
(function () {
  const box = $('topologyGraph');
  if (!box) return;
  box.addEventListener('mousemove', e => {
    const pairEl = e.target.closest && e.target.closest('[data-topology-pair]');
    const g = pairEl || (e.target.closest && e.target.closest('[data-topology-cable]'));
    const tip = box.querySelector('.topo-tip');
    if (!tip) return;
    const topo = topologyCurrent();
    if (!g || !topo) { tip.hidden = true; return; }
    const label = st => st === 'ok' ? tr('', 'Connected') : st === 'err' ? tr('', 'Stopped') : st === 'off' ? tr('', 'Disabled') : tr('', 'Unverified');
    const stOf = s => ['healthy', 'ok', 'up', 'connected'].includes(String(s || '')) ? 'ok'
      : String(s || '') === 'down' ? 'err' : String(s || '') === 'disabled' ? 'off' : 'warn';
    const rows = topoAgentTunnels(topo);
    let html = '';
    const pillOf = st => '<span class="topo-tip-pill ' + stOf(st) + '"><i></i>' + esc(label(st)) + '</span>';
    const shortKey = k => String(k || '').replace('tun-' + topo.name + '-', '');
    if (pairEl) {
      // a pair wire: every tunnel between the two servers, each in its own row
      const pair = (g.getAttribute('data-topology-pair') || '').split('|');
      const mine = rows.filter(tn => tn.localName === pair[0] && tn.remoteName === pair[1]);
      html = '<div class="topo-tip-h"><b>' + esc(pair[0] + ' → ' + pair[1]) + '</b>' +
        '<span class="topo-tip-count">' + esc(mine.length + ' ' + tr('', mine.length === 1 ? 'tunnel' : 'tunnels')) + '</span></div>' +
        '<div class="topo-tip-rows">' +
        (mine.length ? mine.map(tn => '<div class="topo-tip-row">' +
            '<span class="topo-tip-dot ' + stOf(tn.state) + '"></span>' +
            '<span class="topo-tip-key"><bdi dir="ltr">' + esc(shortKey(tn.key)) + '</bdi></span>' +
            '<span class="topo-tip-eng">' + esc(tn.engine || '') + '</span>' +
            pillOf(tn.state) +
            (tn.latency != null && tn.latency >= 0 ? '<span class="topo-tip-lat">' + esc(Math.round(tn.latency) + ' ms') + '</span>' : '') +
            '</div>').join('')
          : '<div class="topo-tip-none">' + esc(tr('', 'No tunnels')) + '</div>') +
        '</div>';
    } else {
      const attr = g.getAttribute('data-topology-cable');
      const t = (topologyData.tunnels || []).find(x => String(x.id) === attr || String(x.name) === attr);
      const ar = rows.find(tn => tn.key === attr) || null;
      if (!t && !ar) { tip.hidden = true; return; }
      const src = t || ar;
      const st = String(src.state || '');
      const key = t ? t.id : ar.key;
      const eng = src.engine || '';
      const lat = t ? t.latency_ms : (ar ? ar.latency : null);
      const iranName = g.getAttribute('data-topo-iran') || (topoSideServers(topo, 'iran')[0] || {}).name || topo.local_ip || '';
      const frnName = g.getAttribute('data-topo-foreign') || (topoSideServers(topo, 'foreign')[0] || {}).name || topo.remote_ip || '';
      html = '<div class="topo-tip-h"><b>' + esc(shortKey(key)) + '</b></div>' +
        '<div class="topo-tip-rows"><div class="topo-tip-row">' +
        '<span class="topo-tip-dot ' + stOf(st) + '"></span>' +
        '<span class="topo-tip-eng">' + esc(eng) + '</span>' +
        pillOf(st) +
        '<span class="topo-tip-lat">' + esc(lat != null && lat >= 0 ? Math.round(lat) + ' ms' : '—') + '</span>' +
        '</div><div class="topo-tip-path u-mono">' + esc(iranName) + ' → ' + esc(frnName) + '</div></div>';
    }
    tip.innerHTML = html;
    const host = box.querySelector('.topo-map') || box;
    const r = host.getBoundingClientRect();
    const x = e.clientX - r.left, y = e.clientY - r.top;
    tip.style.left = x + 'px';
    // near the top edge the window flips below the cursor instead of leaving the card
    const below = y < 190;
    tip.classList.toggle('below', below);
    tip.style.top = below ? (y + 14) + 'px' : y + 'px';
    tip.hidden = false;
  });
  box.addEventListener('mouseleave', () => { const tip = box.querySelector('.topo-tip'); if (tip) tip.hidden = true; });
  box.addEventListener('click', e => {
  });
})();
$('topoWizX').onclick = topoWizCancel;
$('topoWizBack').onclick = topoWizBack;
$('topoWizNext').onclick = topoWizNext;
$('topoCreateX').onclick = topoCreateCancel;
$('topoCreateBack').onclick = topoCreateBack;
$('topoCreateNext').onclick = topoCreateNext;
$('topologySearch').addEventListener('input', e => { topologySearchQuery = (e && e.target ? e.target.value : '') || ''; topoPage = 1; topologyRender(); });
$('topologyStatusFilter').addEventListener('change', e => { topologyStatusFilter = (e && e.target ? e.target.value : 'all') || 'all'; topoPage = 1; topologyRender(); });
$('topologyCountryFilter').addEventListener('change', e => { topologyCountryFilter = (e && e.target ? e.target.value : 'all') || 'all'; topoPage = 1; topologyRender(); });
$('topologyDcFilter').addEventListener('change', e => { topologyDcFilter = (e && e.target ? e.target.value : 'all') || 'all'; topoPage = 1; topologyRender(); });
$('topoPagePrev').addEventListener('click', () => { if (topoPage > 1) { topoPage--; topologyRender(); } });
$('topoPageNext').addEventListener('click', () => { topoPage++; topologyRender(); });
document.querySelectorAll('[data-topo-role]').forEach(b => { b.onclick = () => topologyChooseRole(b.dataset.topoRole); });
$('tab-tunnel').addEventListener('click', e => {
  const target = e.target.closest('button'); if (!target) return;
  if (target.dataset.topoMenu !== undefined) { // kebab: toggle this menu, close the others
    const pop = target.parentElement.querySelector('.topo-menu-pop');
    const wasOpen = pop && !pop.classList.contains('hidden');
    topoMenusClose();
    if (pop && !wasOpen) {
      pop.classList.remove('hidden');
      const r = target.getBoundingClientRect();
      const ph = pop.offsetHeight || 130, pw = pop.offsetWidth || 172;
      // The pop is position:fixed, but the glassy .tunnel-card ancestor has a
      // backdrop-filter — that makes the CARD the containing block, so fixed
      // coordinates are card-relative. Clamp inside the real containing block
      // (offsetParent; the viewport when there is none), never the window.
      const host = pop.offsetParent;
      const hr = host ? host.getBoundingClientRect()
        : { left: 0, top: 0, right: window.innerWidth, bottom: window.innerHeight };
      let top = r.bottom + 6;
      if (top + ph > hr.bottom - 8) top = Math.max(hr.top + 8, r.top - ph - 6);
      let left = document.documentElement.dir === 'rtl' ? r.left : r.right - pw;
      left = Math.min(Math.max(hr.left + 8, left), hr.right - pw - 8);
      pop.style.top = (host ? top - hr.top : top) + 'px';
      pop.style.left = (host ? left - hr.left : left) + 'px';
      target.setAttribute('aria-expanded', 'true');
    }
    return;
  }
  if (target.dataset.topoPower !== undefined) { topoMenusClose(); topologyPower(target.dataset.topoPower, target.dataset.active === 'true'); return; }
  if (target.dataset.topoOpen || target.dataset.topoDelete) topoMenusClose();
  if (target.dataset.topoEdit) { topoMenusClose(); topologyEditRow(target.dataset.topoEdit); return; }
  if (target.dataset.topoOpen) topologyOpen(target.dataset.topoOpen);
  if (target.dataset.topoDelete) topologyDelete(target.dataset.topoDelete);
  if (target.dataset.topoDetach) topologyDetach(target.dataset.topoDetach);
  const memAdd = target.closest ? target.closest('[data-mem-add]') : null;
  if (memAdd) { topoMembersPickOpen(memAdd.getAttribute('data-mem-add')); return; }
  const memRm = target.closest ? target.closest('[data-mem-remove]') : null;
  if (memRm) { topoMemberRemove(memRm.getAttribute('data-mem-remove'), memRm.getAttribute('data-mem-agent')); return; }
  const memGo = target.closest ? target.closest('[data-mem-pick-go]') : null;
  if (memGo) { topoMembersPickGo(); return; }
  const memX = target.closest ? target.closest('[data-mem-pick-close]') : null;
  if (memX) { const ov = document.getElementById('topoMemPick'); if (ov) ov.hidden = true; return; }
  const agJump = target.closest ? target.closest('[data-topo-agent]') : null;
  if (agJump) {
    const id = agJump.getAttribute('data-topo-agent');
    if (typeof openAgtDetail === 'function') { openAgtDetail(id); if (typeof goTab === 'function') goTab('magt'); }
    return;
  }
  if (target.dataset.topoViewTunnel) {
    const kind = topologyKind(target.dataset.topoViewTunnel);
    if (target.dataset.topoViewTunnel.startsWith('xray:')) { goTab('xray'); return; }
    if (target.dataset.topoViewTunnel.startsWith('bagher:')) { openTunnelDetail('bagher'); return; }
    openTunnelDetail(kind);
  }
});
if (document.addEventListener) { // sandbox DOM stubs may not have it
  document.addEventListener('click', e => { if (!e.target.closest('.topo-menu')) topoMenusClose(); });
  document.addEventListener('keydown', e => { if (e.key === 'Escape') topoMenusClose(); });
  // A click outside an open country dropdown closes it; clicks inside are
  // handled by the trigger and the delegated option handler.
  document.addEventListener('click', e => {
    for (const id of Object.keys(topoDDState)) {
      if (topoDDState[id].open && !(e.target.closest && e.target.closest('.topo-dd'))) topoCountrySetOpen(id, false);
    }
  });
  // Escape closes an open country dropdown first: this capture listener was
  // registered before the modal trap's, and stopImmediatePropagation keeps
  // the same keystroke from closing the whole wizard.
  document.addEventListener('keydown', e => {
    if (e.key !== 'Escape') return;
    for (const id of Object.keys(topoDDState)) {
      if (topoDDState[id].open) {
        topoCountrySetOpen(id, false);
        if (e.stopImmediatePropagation) e.stopImmediatePropagation();
        break;
      }
    }
  }, true);
  if (window.addEventListener) {
    window.addEventListener('scroll', topoMenusClose, { passive: true });
    window.addEventListener('resize', topoMenusClose);
  }
}
// The whole row opens the topology: clicks anywhere except the action buttons.
$('topologiesList').addEventListener('click', e => {
  if (e.target.closest('.topo-actions')) return; // View/Delete keep their own behaviour
  const row = e.target.closest('.topo-list-row');
  if (row && row.dataset.topoId) topologyOpen(row.dataset.topoId);
});
$('topologiesList').addEventListener('keydown', e => {
  if (e.key !== 'Enter' && e.key !== ' ') return;
  const row = e.target.closest ? e.target.closest('.topo-list-row') : null;
  if (row && row.dataset.topoId) { e.preventDefault(); topologyOpen(row.dataset.topoId); }
});
setInterval(() => {
  const tab = $('tab-tunnel');
  if (document.querySelector('.topo-menu-pop:not(.hidden)')) return; // an open menu would be wiped
  if (tab && !tab.hidden && !document.hidden && !topologyBusy && !_tunnelDetail) topologyReload();
}, 15000);
