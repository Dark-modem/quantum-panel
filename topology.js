// A topology groups existing tunnels between a pair of servers. Only an empty
// grouping can be deleted; tunnels and engines are never removed automatically.
let topologyData = { topologies: [], tunnels: [] };
let topologySelected = null;
let topologySearchQuery = '';
let topologyViewMode = 'list';
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
function topologyCountryOptions(current) {
  return TOPO_COUNTRIES.map(([cc, label]) =>
    `<option value="${esc(cc)}"${cc === (current || '') ? ' selected' : ''}>${esc(tr('', label))}</option>`
  ).join('');
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
  const iran = topo.role === 'foreign' ? topo.remote_ip : topo.local_ip;
  const foreign = topo.role === 'foreign' ? topo.local_ip : topo.remote_ip;
  // The connection strip from the classic server view: every tunnel is a
  // straight horizontal beam between the two cards — a thin base wire with
  // evenly spaced beacon dots in the tunnel's health color, a marching pulse
  // on live ones, and a halo'd name above each beam. The svg never stretches
  // past its natural width, so cards and beams keep their proportions.
  const W = 840, NW = 260, NH = 104, ix = 40, fx = W - 40 - NW;
  const n = tunnels.length;
  const step = n > 1 ? Math.min(30, 96 / (n - 1)) : 0;
  const H = n ? Math.max(250, 170 + (n - 1) * step + 80) : 210;
  const cy = H / 2;
  const stateOf = t => {
    const s = String(t.state || '');
    return ['healthy', 'ok', 'up', 'connected'].includes(s) ? 'ok'
      : ['down', 'err', 'stopped'].includes(s) ? 'err' : 'warn';
  };
  const kinds = tunnels.map(stateOf);
  const nodeKind = !n ? '' : kinds.every(k => k === 'ok') ? 'ok' : kinds.every(k => k === 'err') ? 'err' : 'warn';
  const cables = tunnels.map((t, i) => {
    const y = (cy + (i - (n - 1) / 2) * step).toFixed(1);
    const d = `M ${ix + NW} ${y} L ${fx} ${y}`;
    const st = stateOf(t);
    return `<g data-topology-cable="${text(t.id)}" class="topo-cable topo-${st}">` +
      `<path class="topo-e-hit" d="${d}"/>` +
      `<path class="topo-e-wire" d="${d}"/>` +
      `<path class="topo-e-dots" d="${d}"/>` +
      (st === 'ok' ? `<path class="topo-e-pulse" d="${d}"/>` : '') +
      `<text class="topo-e-tag" x="${W / 2}" y="${(Number(y) - 12).toFixed(1)}">${text(t.name)} · ${text(t.engine)}</text>` +
      `<title>${text(t.name)} · ${text(t.state)}</title>` +
    `</g>`;
  }).join('');
  const cc = String(topo.country || '').toLowerCase();
  const countryLabel = (TOPO_COUNTRIES.find(c => c[0] === cc) || ['', 'Other'])[1];
  const top = cy - NH / 2;
  // RTL pages read from the right, so the home (Iran) card sits on the right
  // and the foreign card opposite; LTR keeps Iran on the left. The cables are
  // symmetric between the two facing edges, so only the cards trade places.
  const rtl = typeof currentLang !== 'undefined' && currentLang === 'fa';
  const iranX = rtl ? fx : ix, foreignX = rtl ? ix : fx;
  // No tunnels means there is no health to show: the dot stays hidden and a
  // quiet dashed placeholder links the pair instead of an empty void.
  // LTR cards flow from the left edge; RTL cards mirror — the status dot and
  // flag start at the right edge, every text anchors its right edge there and
  // the rack moves to the opposite corner, so Persian reads naturally.
  const dot = (x, m) => nodeKind ? `<circle class="topo-dot ${nodeKind}" cx="${m ? x + NW - 20 : x + 20}" cy="${top + 27}" r="5"/>` : '';
  const node = (x, caption, title, ip, flag) => {
    const m = rtl;
    const head = m
      ? dot(x, true) +
        topologyFlagSvg(flag, `x="${x + NW - 54}" y="${top + 20}"`) +
        `<text class="topo-cap" x="${x + NW - 58}" y="${top + 31}" text-anchor="start" style="direction:rtl">${caption}</text>`
      : dot(x) +
        topologyFlagSvg(flag, `x="${nodeKind ? x + 34 : x + 20}" y="${top + 20}"`) +
        `<text class="topo-cap" x="${nodeKind ? x + 62 : x + 48}" y="${top + 31}">${caption}</text>`;
    return `<g class="topo-node"><title>${text(title)} · ${text(ip)}</title>` +
    `<rect class="topo-n-bg" x="${x}" y="${top}" width="${NW}" height="${NH}" rx="12"/>` +
    head +
    (m
      ? `<text class="topo-t1" x="${x + NW - 18}" y="${top + 60}" text-anchor="start" style="direction:rtl">${text(title)}</text>` +
        `<text class="topo-ip" x="${x + NW - 18}" y="${top + 84}" text-anchor="start" style="direction:rtl">${text(ip)}</text>`
      : `<text class="topo-t1" x="${x + 18}" y="${top + 60}">${text(title)}</text>` +
        `<text class="topo-ip" x="${x + 18}" y="${top + 84}">${text(ip)}</text>`) +
    // A little rack at the card's end; its LEDs mirror the tunnel health.
    `<g class="topo-srv led-${nodeKind || 'off'}" transform="translate(${m ? x + 18 : x + NW - 42} ${top + 34})">` +
    `<rect x="0" y="0" width="24" height="36" rx="6"/>` +
    `<path d="M8 9h11M8 18h11M8 27h11"/>` +
    `<circle cx="4.5" cy="9" r="1.5"/><circle cx="4.5" cy="18" r="1.5"/><circle cx="4.5" cy="27" r="1.5"/>` +
    `</g>` +
    `</g>`;
  };
  return `<div class="topo-map"><div class="topo-map-scroll">` +
    `<svg class="topo-graph" viewBox="0 0 ${W} ${H}" role="img" aria-label="${text(tr('', 'Topology connections'))}" dir="ltr">` +
    (n ? '' : `<g class="topo-cable topo-idle"><path class="topo-e-wire" d="M ${ix + NW} ${cy} L ${fx} ${cy}"/><path class="topo-e-dots" d="M ${ix + NW} ${cy} L ${fx} ${cy}"/></g>` +
      `<text class="topo-empty" x="${W / 2}" y="${cy - 12}" text-anchor="middle">${text(tr('', 'No tunnel attached yet'))}</text>`) +
    cables +
    node(iranX, esc(tr('', 'Iran')), tr('', 'Iran server'), iran, 'ir') +
    node(foreignX, esc(tr('', countryLabel)), tr('', 'Foreign server'), foreign, topo.country) +
    `</svg></div>` +
    `<div class="cl-legend">` +
    `<span><i class="ok"></i>${esc(tr('', 'Connected'))}</span>` +
    `<span><i class="warn"></i>${esc(tr('', 'Unverified'))}</span>` +
    `<span><i class="err"></i>${esc(tr('', 'Stopped'))}</span>` +
    `</div></div>`;
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
  $('topologiesList').innerHTML = visible.length
    ? visible.map(t => {
        const tunnels = topologyExistingTunnels(t);
        const remaining = tunnels.length;
        const endpoints = topologyEndpointPair(t);
        const countLabel = tr('', '{n} existing tunnel(s)').replace('{n}', String(remaining));
        const deleteTitle = remaining ? topologyDeleteNotice(remaining) : tr('', 'Delete topology');
        const serverCell = (flag, ip) => `<span class="topo-server"><span class="topo-flag" aria-hidden="true">${topologyFlagSvg(flag)}</span><bdi class="u-mono" dir="ltr">${esc(ip || '—')}</bdi></span>`;
        const health = topologyHealthOf(t, tunnels);
        const healthLabel = tr('', health === 'ok' ? 'Active' : health === 'down' ? 'Down' : 'Unknown');
        const powering = health === 'down' || !remaining;
        return `<tr class="topo-list-row" role="row" tabindex="0" data-topo-id="${esc(t.id)}" aria-label="${esc(t.name)}">
        <td role="cell" class="topo-cell-name" data-label="${esc(tr('', 'Name'))}"><button type="button" class="topo-name" data-topo-open="${esc(t.id)}"><bdi>${esc(t.name)}</bdi></button></td>
        <td role="cell" class="topo-cell-iran" data-label="${esc(tr('', 'Iran'))}">${serverCell('ir', endpoints.iran)}</td>
        <td role="cell" class="topo-cell-foreign" data-label="${esc(tr('', 'Foreign'))}">${serverCell(t.country, endpoints.foreign)}</td>
        <td role="cell" class="topo-cell-tunnels" data-label="${esc(tr('', 'Tunnels'))}"><span class="topo-count" aria-label="${esc(countLabel)}">${remaining}</span></td>
        <td role="cell" class="topo-cell-state" data-label="${esc(tr('', 'Status'))}"><span class="topo-state-pill st-${health}"><i aria-hidden="true"></i>${esc(healthLabel)}</span></td>
        <td role="cell" class="topo-cell-actions" data-label="${esc(tr('', 'Actions'))}"><div class="topo-actions"><div class="topo-menu"><button type="button" class="topo-kebab" data-topo-menu="${esc(t.id)}" aria-haspopup="menu" aria-expanded="false" aria-label="${esc(tr('', 'Tunnel actions'))}"><svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><circle cx="12" cy="5" r="2"/><circle cx="12" cy="12" r="2"/><circle cx="12" cy="19" r="2"/></svg></button><div class="topo-menu-pop hidden" role="menu" aria-label="${esc(tr('', 'Tunnel actions'))}"><button type="button" class="topo-menu-item" role="menuitem" data-topo-open="${esc(t.id)}">${esc(tr('', 'View'))}</button><button type="button" class="topo-menu-item" role="menuitem" data-topo-power="${esc(t.id)}" data-active="${powering ? 'true' : 'false'}">${esc(powering ? tr('', 'Activate') : tr('', 'Deactivate'))}</button><span class="topo-delete-wrap" title="${esc(deleteTitle)}"><button type="button" class="quantum-btn quantum-btn-danger topo-delete topo-menu-item" role="menuitem" data-topo-delete="${esc(t.id)}" aria-label="${esc(tr('', 'Delete topology'))}"${remaining ? ` title="${esc(tr('', 'Delete all tunnels in this topology first'))}"` : ''}>${esc(tr('', 'Delete'))}</button></span></div></div></div></td>
      </tr>`;
      }).join('')
    : searching
      ? `<tr class="topo-list-empty"><td colspan="6"><div class="ent-empty"><b>${esc(tr('', 'No topology matches your search.'))}</b><span>${esc(tr('', 'Clear the search to see all topologies.'))}</span></div></td></tr>`
      : topologyLoading && !topologyLoadedOnce
        ? `<tr class="topo-list-empty topo-list-skel"><td colspan="6">${skel(2)}</td></tr>`
        : `<tr class="topo-list-empty"><td colspan="6"><div class="ent-empty"><b>${esc(tr('', 'No topology yet'))}</b><span>${esc(tr('', 'Create a topology, set the server roles, then add tunnels between them.'))}</span></div></td></tr>`;
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
    $('topologyCountry').innerHTML = topologyCountryOptions(topo.country);
    topologyChooseRole(topo.role || 'iran');
  }
  const candidates = topologyData.tunnels.filter(t => !assigned.has(t.id) && (!t.remote_ip || t.remote_ip === topo.remote_ip || (t.remote_ips || []).includes(topo.remote_ip)));
  $('topologyAttachSelect').innerHTML = `<option value="">${esc(tr('', 'Select an existing tunnel'))}</option>`
    + candidates.map(t => `<option value="${esc(t.id)}">${esc(t.name)} · ${esc(t.engine)}</option>`).join('');
  $('topologyBoundList').innerHTML = cables.map(t => `<div class="topo-binding"><b>${esc(t.name)}</b><span>${esc(t.state)}</span><button type="button" class="quantum-btn quantum-btn-ghost" data-topo-detach="${esc(t.id)}">${esc(tr('', 'Detach from topology'))}</button><button type="button" class="quantum-btn quantum-btn-secondary" data-topo-view-tunnel="${esc(t.id)}">${esc(tr('', 'View'))}</button></div>`).join('');
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
// The wizard is the only path that creates a topology: entering the flow or
// closing it mid-way must not leave an empty topology behind (the old code
// POSTed one immediately with blank IPs).
function topologyCreate() { topoCreateOpen(); }
async function topologySave() {
  const topo = topologyCurrent(); if (!topo || topologyBusy) return;
  topologyBusy = true; $('topologySaveBtn').disabled = true;
  try {
    await api('/api/topologies/' + encodeURIComponent(topo.id), {name: $('topologyName').value.trim(), role: $('topologyRole').value, local_ip: $('topologyLocalIP').value.trim(), remote_ip: $('topologyRemoteIP').value.trim(), country: $('topologyCountry').value.trim().toLowerCase()}, 'PATCH');
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
  if (remaining) {
    // The delete button is never disabled; existing tunnels block the deletion
    // with a visible warning instead of a silent no-op.
    toastWarn(tr('', 'Delete topology'), topologyDeleteNotice(remaining));
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
// Step list for the current wizard state. The hub side gets one extra final
// step for the Matrix bundle hand-off (type -> settings -> review -> bundle);
// other tunnel types and the foreign (node) side keep the 3-step flow.
function topoWizSteps() {
  const steps = [...TOPO_WIZ_BASE_STEPS];
  if (topoWiz && topoWiz.type === 'matrix' && !topoWizForeignSide()) steps.push('Bundle');
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
    step: 1, type: '', bundle: '',
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
function topoWizRender() {
  if (!topoWiz) return;
  const steps = topoWizSteps();
  const step = topoWiz.step, bundle = topoWizOnBundle();
  $('topoWizProgress').textContent = tr('', 'Step {x} of {n}').replace('{x}', String(step)).replace('{n}', String(steps.length));
  $('topoWizSteps').innerHTML = steps.map((lb, i) =>
    `<span class="topo-wiz-step${i + 1 < step ? ' done' : ''}${i + 1 === step ? ' on' : ''}" data-wiz-step="${i + 1}"${i + 1 === step ? ' aria-current="step"' : ''}>${esc(tr('', lb))}</span>`).join('');
  // The bundle hand-off screen is terminal: Back and Cancel would only confuse
  // after the tunnel already exists; the header × still closes it.
  $('topoWizBack').hidden = step === 1 || bundle;
  $('topoWizCancelBtn').hidden = bundle;
  const next = $('topoWizNext');
  next.textContent = bundle ? tr('', 'Done') : (step === 3 ? tr('', 'Create the tunnel') : tr('', 'Next'));
  next.disabled = step === 1 && !topoWiz.type;
  topoWizError('');
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
  } else if (t.id === 'matrix' && topoWizForeignSide()) {
    // Node side: this server joins an existing hub mesh, so the wizard asks
    // for the hub bundle instead of the hub's mesh settings.
    html = `<p class="qs-hint u-m-0">${esc(tr('', 'On the Iran server, create the Matrix tunnel, copy the bundle it shows, and paste it here.'))}</p>`
      + `<div class="fg"><label class="qs-label" for="topoWizFBundle">${esc(tr('', 'Enter the hub bundle here'))}</label>`
      + `<textarea class="quantum-input u-mono topo-wiz-bundle" id="topoWizFBundle" rows="6" dir="ltr" spellcheck="false" autocapitalize="off" autocomplete="off" placeholder="hmx1_...">${esc(v.bundle || '')}</textarea></div>`;
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
  // A blank Matrix mesh port reads as 0: the backend then picks a free port
  // instead of silently forcing a default that may be occupied.
  const portEl = $('topoWizFPort');
  const wt = topoWizType();
  if (portEl && wt && wt.id === 'matrix' && String(portEl.value).trim() === '') v.port = 0;
  // The hub bundle textarea reads as-is (trimmed): clearing the field must
  // clear the stored bundle too, so validation cannot pass on a stale paste.
  const bundleEl = $('topoWizFBundle');
  if (bundleEl) v.bundle = String(bundleEl.value || '').trim();
}
function topoWizValidate() {
  const t = topoWizType(), v = topoWiz.values;
  if (!t) return tr('', 'Choose the tunnel type');
  if (t.id !== 'matrix' && !v.remote) return tr('', 'Enter the foreign server public IP.');
  if ((t.id === 'bagher' || t.id === 'xray') && !v.ports) return tr('', 'Enter at least one port to forward, for example 443, 8443.');
  if (t.id === 'matrix' && topoWizForeignSide()) {
    // Node side: validate the pasted bundle inline, before any API call.
    const b = String(v.bundle || '').trim();
    if (!b) return tr('', 'Paste the hub bundle from the Iran server.');
    if (!/^hmx1_/.test(b)) return tr('', 'The bundle must start with hmx1_.');
    return '';
  }
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
    if (topoWizForeignSide()) {
      rows.push([tr('', 'Hub bundle'), v.bundle ? String(v.bundle).slice(0, 21) + '…' : '']);
    } else {
      rows.push([tr('', 'Family'), (v.family || 'ipv4').toUpperCase()]);
      if (v.family === 'ipv6' && v.addr) rows.push([tr('', 'This server\'s IPv6 address'), v.addr]);
      rows.push([tr('', 'Mesh port'), v.port ? String(v.port) : tr('', 'Automatic')]);
      if (v.ports) rows.push([tr('', 'Forwarded ports'), v.ports]);
    }
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
      if (topoWizForeignSide()) {
        // Node side: pair with the hub using the pasted bundle. Calling
        // /api/matrix/setup here would wrongly create a second hub mesh.
        mxResult = await api('/api/matrix/foreign-setup', { bundle: String(v.bundle || '').trim() });
      } else {
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
      }
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
    if (t.id === 'matrix' && topoWizForeignSide()) {
      toastOk(tr('', 'Matrix tunnel connected to the hub.'));
      topoWiz = null;
      topoWizHide();
    } else if (t.id === 'matrix' && mxResult && mxResult.bundle) {
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
      `<div class="fg"><label class="qs-label" for="topoCreateCountry">${esc(tr('', 'Foreign server country'))}</label><select class="quantum-input" id="topoCreateCountry">` +
      TOPO_COUNTRIES.map(c => `<option value="${esc(c[0])}"${c[0] === v.country ? ' selected' : ''}>${esc(tr('', c[1]))}</option>`).join('') +
      `</select></div>`;
    $('topoCreateCountry').onchange = () => { topoCreateRead(); topoCreateRender(); };
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
$('topoCreateCancelBtn').onclick = topoCreateCancel;
$('topoCreateX').onclick = topoCreateCancel;
$('topoCreateBack').onclick = topoCreateBack;
$('topoCreateNext').onclick = topoCreateNext;
$('topologySearch').addEventListener('input', e => { topologySearchQuery = (e && e.target ? e.target.value : '') || ''; topologyRender(); });
$('topologyViewListBtn').onclick = () => topologySetView('list');
$('topologyViewGridBtn').onclick = () => topologySetView('grid');
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
      let top = r.bottom + 6;
      if (top + ph > window.innerHeight - 8) top = Math.max(8, r.top - ph - 6);
      pop.style.top = top + 'px';
      pop.style.right = 'auto';
      // end-align the pop with the kebab (kebab sits at the row's end edge),
      // then clamp inside the viewport
      let left = document.documentElement.dir === 'rtl' ? r.left : r.right - pw;
      pop.style.left = Math.min(Math.max(8, left), window.innerWidth - pw - 8) + 'px';
      target.setAttribute('aria-expanded', 'true');
    }
    return;
  }
  if (target.dataset.topoPower !== undefined) { topoMenusClose(); topologyPower(target.dataset.topoPower, target.dataset.active === 'true'); return; }
  if (target.dataset.topoOpen || target.dataset.topoDelete) topoMenusClose();
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
if (document.addEventListener) { // sandbox DOM stubs may not have it
  document.addEventListener('click', e => { if (!e.target.closest('.topo-menu')) topoMenusClose(); });
  document.addEventListener('keydown', e => { if (e.key === 'Escape') topoMenusClose(); });
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
