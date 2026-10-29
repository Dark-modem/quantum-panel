// Selected-tunnel presentation only. Loaded after app.js; no requests or events.
(function (root) {
  'use strict';
  const text = value => typeof value === 'string' || typeof value === 'number' ? String(value) : '';
  const e = value => typeof esc === 'function' ? esc(text(value)) : text(value).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const t = value => typeof tr === 'function' ? tr('', value) : value;
  const list = value => (Array.isArray(value) ? value.map(text) : text(value).split(/[ ,]+/)).filter(Boolean);
  const knownNumber = value => (typeof value === 'number' && Number.isFinite(value) && value >= 0) ? value : null;
  const ms = value => {
    if (typeof value === 'string' && /^\d+(?:\.\d+)?\s*(?:ms)?$/.test(value.trim())) value = parseFloat(value);
    const n = knownNumber(value);
    return n === null ? '—' : n.toFixed(n < 10 ? 1 : 0) + ' ms';
  };
  const bytes = value => {
    const n = knownNumber(value);
    return n === null ? '—' : typeof fmtBytes === 'function' ? fmtBytes(n) : n + ' B';
  };
  const family = (value, address) => {
    const v = text(value).toLowerCase();
    if (v === 'ipv4' || v === '4') return 'IPv4';
    if (v === 'ipv6' || v === '6') return 'IPv6';
    if (/^(?:\d{1,3}\.){3}\d{1,3}$/.test(text(address))) return 'IPv4';
    if (/^[\da-f:[\].%]+$/i.test(text(address)) && text(address).includes(':')) return 'IPv6';
    return '—';
  };
  const roleOf = (role, svc) => /iran|hub/i.test(text(role)) || /^(frps|backhaul-server)/.test(text(svc)) ? 'iran'
    : /foreign|node/i.test(text(role)) || /^(frpc|backhaul-client)/.test(text(svc)) ? 'foreign' : '';
  const configured = b => !!(b && (b.has_config || b.is_active || b.remote_host || list(b.ports).length));
  const engineOf = (s, peer) => {
    const value = text(s.engine || s.tunnel_engine);
    if (value) return value;
    const svc = text(peer ? s.frps_svc : s.frp_svc);
    if (svc.includes('backhaul')) return s.no_gre || (!peer && !(s.gre || {}).exists) ? 'backhaul' : 'gre-backhaul';
    return svc || (s.gre || {}).exists || s.gre_if ? 'frp' : '';
  };
  const engineLabels = { frp: 'GRE + FRP', backhaul: 'Backhaul', 'gre-backhaul': 'GRE + Backhaul', matrix: 'Matrix', bagher: 'Bagher', xray: 'Xray' };

  function model(kind) {
    const L = root._lastLocal || {}, D = root._lastDash || {};
    const peers = typeof tunPeerCache !== 'undefined' ? tunPeerCache : root.tunPeerCache;
    const B = (typeof _sshfwdData !== 'undefined' && _sshfwdData) || D.bagher || root._lastBagher;
    const X = (typeof _xray !== 'undefined' && _xray) || root._xray;
    // Local server identity is shared. Remote/inner/control/ports/counters are NEVER inherited from the main tunnel.
    const localIP = D.local_pub || L.local_pub || '';
    let s, m = { kind, localIP, localName: D.server_name || D.hostname || t('This server'), remoteName: '', remoteIP: '',
      role: '', engine: '', transport: '', family: '—', service: null, connected: false, state: 'down',
      inner: '', remoteInner: '', control: '', ports: [], rx: null, tx: null, latency: '—', serviceName: '', connections: null };
    if (kind === 'main' || typeof kind === 'number') {
      const peer = typeof kind === 'number';
      s = peer ? (Array.isArray(peers) ? peers.find(p => p.id === kind) : null) : L;
      if (!s || (!peer && !(s.frp_up || s.frp_svc || (s.gre || {}).exists || list(s.proxies).length))) return null;
      m.engine = engineOf(s, peer);
      m.role = peer ? 'iran' : roleOf(s.role, s.frp_svc);
      m.localIP = s.local_pub || localIP;
      m.remoteName = s.name || t(m.role === 'iran' ? 'Foreign server' : m.role === 'foreign' ? 'Iran server' : 'Other server');
      m.remoteIP = s.remote_pub || (!peer && ((s.gre || {}).peer_ip || s.gre_peer)) || '';
      m.transport = text(s.transport);
      m.family = family(s.family || s.tunnel_type, m.remoteIP || m.localIP);
      m.service = typeof s.frp_up === 'boolean' ? s.frp_up : null;
      // A running unit/interface alone is never an established connection.
      m.connected = s.health_state !== 'down' && (!!s.linked || !!s.connected || s.health_state === 'healthy' || (!!s.frp_up && !!s.ping_ok));
      m.state = s.health_state === 'down' ? 'down' : s.health_state === 'degraded' ? 'warn' : m.connected ? 'ok' : m.service || s.gre_up || (s.gre || {}).exists ? 'warn' : 'down';
      m.inner = peer ? s.local_gre || s.gre_inner || '' : (s.gre || {}).inner || '';
      m.remoteInner = peer ? s.peer_gre || '' : '';
      if (m.engine === 'backhaul' || s.no_gre) m.inner = m.remoteInner = '';
      m.control = s.frp_port || (!peer && s.bind_port) || '';
      m.serviceName = peer ? s.frps_svc || '' : s.frp_svc || '';
      m.ports = peer ? list(s.raw_ports || s.ports) : list(list(s.proxy_ports).length ? s.proxy_ports : s.ports || s.proxies);
      m.rx = s.rx; m.tx = s.tx;
      m.latency = s.ping_ok ? ms(s.ping_ms) : ms(s.latency_ms);
    } else if (kind === 'matrix') {
      s = root._matrixState;
      if (!s || !s.enabled || !['hub', 'node'].includes(s.role)) return null;
      const nodes = Array.isArray(s.peers) ? s.peers : [], hub = s.role === 'hub';
      const summary = typeof matrixSummary === 'function' ? matrixSummary(s) : null;
      m.engine = 'matrix'; m.transport = 'tinc · TCP + UDP'; m.role = hub ? 'iran' : 'foreign';
      m.family = family(s.family, ''); m.service = !!s.service;
      m.connected = summary ? summary.connected : !!s.service && !s.peer_sync_required && (hub ? nodes.some(p => p.ping_ok) : !!s.hub_ping_ok);
      m.state = summary ? summary.state : !m.service ? 'down' : !s.peer_sync_required && (hub ? nodes.length > 0 && nodes.every(p => p.ping_ok) : s.hub_ping_ok) ? 'ok' : 'warn';
      m.remoteName = hub ? nodes.length > 1 ? t('Multiple foreign nodes') : (nodes[0] || {}).name || t('Waiting for foreign node') : t('Iran server');
      m.remoteIP = hub ? nodes.map(p => text(p.addr)).filter(Boolean).join(', ') : s.addr || '';
      m.inner = s.inner || s.ip || ''; m.remoteInner = hub ? nodes.map(p => text(p.ip)).filter(Boolean).join(', ') : '10.254.231.1';
      m.control = s.port || ''; m.ports = list(s.ports); m.serviceName = 'tinc';
      m.rx = s.rx; m.tx = s.tx; m.latency = m.connected ? ms(s.latency_ms) : '—';
      m.nodes = nodes.map(p => ({ name: text(p.name), addr: text(p.addr), ip: text(p.ip), connected: !!s.service && !s.peer_sync_required && !!p.ping_ok }));
    } else if (kind === 'bagher' || /^bagher[-:]?\d+$/.test(text(kind))) {
      if (!configured(B)) return null;
      const id = kind === 'bagher' ? 1 : Number(text(kind).match(/\d+$/)[0]);
      s = id === 1 ? B : (Array.isArray(B.links) ? B.links.find(l => l.id === id) : null);
      if (!s) return null;
      m.engine = 'bagher'; m.transport = s.udp ? 'SSH + FRP · TCP + UDP' : 'SSH + FRP · TCP';
      m.role = B.role === 'foreign' || B.role === 'server' ? 'foreign' : B.role === 'iran' || B.role === 'hub' ? 'iran' : '';
      m.remoteName = s.name || t(m.role === 'foreign' ? 'Iran server' : m.role === 'iran' ? 'Foreign server' : 'Other server'); m.remoteIP = s.remote_host || '';
      m.family = family(s.family, m.remoteIP); m.service = !!s.is_active; m.connected = m.service && !!s.connected;
      m.state = m.connected ? 'ok' : m.service ? 'warn' : 'down'; m.control = s.frp_bind_port || '';
      m.sshPort = s.remote_ssh_port || s.ssh_port || ''; m.ports = list(s.ports); m.serviceName = 'Bagher';
      m.rx = s.rx_bytes; m.tx = s.tx_bytes; m.latency = s.latency_ms > 0 ? ms(s.latency_ms) : '—'; m.connections = knownNumber(s.cur_conns);
    } else if (/^xray[-:]?\d+$/.test(text(kind))) {
      const id = Number(text(kind).match(/\d+$/)[0]);
      // The detailed cache is authoritative, including removal; dashboard is compatibility-only.
      const links = X && Array.isArray(X.links) ? X.links : D.xray;
      s = Array.isArray(links) ? links.find(l => l.id === id) : null;
      if (!s) return null;
      const h = s.health || s, link = s.disabled ? 'off' : h.link || 'checking';
      m.engine = 'xray'; m.role = roleOf(s.side, ''); m.localIP = (X && X.public_ip) || localIP;
      m.remoteName = s.name || t(m.role === 'iran' ? 'Foreign server' : m.role === 'foreign' ? 'Iran server' : 'Other server'); m.remoteIP = s.remote || '';
      m.transport = text((s.profile || {}).transport || s.transport);
      m.mode = s.mode === 'reverse' ? t('Reverse') : s.mode === 'direct' ? t('Direct') : '';
      m.family = family(s.family, m.remoteIP || m.localIP);
      m.service = s.disabled || link === 'off' ? false : typeof h.active === 'boolean' ? h.active : null;
      m.connected = link === 'up'; m.state = m.connected ? 'ok' : ['listening', 'checking'].includes(link) ? 'warn' : 'down';
      m.control = s.tunnel_port || ''; m.ports = list(s.ports); m.serviceName = 'Xray';
      m.rx = s.rx; m.tx = s.tx; m.latency = h.probe_ms > 0 ? ms(h.probe_ms) : h.ping_ms > 0 ? ms(h.ping_ms) : '—'; m.connections = knownNumber(h.conns);
    } else return null;
    return m;
  }

  const row = (label, value) => `<div class="det-row"><span class="det-k">${e(t(label))}</span><span class="det-v mono"><bdi data-no-i18n>${e(value || '—')}</bdi></span></div>`;
  const badge = (label, state) => `<span class="badge ${state}"><span class="dot" aria-hidden="true"></span>${e(t(label))}</span>`;
  const serviceText = m => m.service === null ? 'Unknown' : m.service ? 'Running' : 'Stopped';
  const connectionText = m => m.connected ? m.state === 'warn' ? 'Partially connected' : 'Connected' : m.state === 'warn' ? 'Waiting for the other server' : 'Not connected';
  function hero(m) {
    const role = m.role === 'iran' ? 'Iran server' : m.role === 'foreign' ? 'Foreign server' : 'This server';
    const otherRole = m.role === 'iran' ? 'Foreign server' : m.role === 'foreign' ? 'Iran server' : 'Other server';
    const node = (name, ip, label) => `<div class="tv-node"><span class="tv-ico" aria-hidden="true"><svg viewBox="0 0 24 24"><rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/></svg></span><em>${e(t(label))}</em><b data-no-i18n>${e(name)}</b><small data-no-i18n>${e(ip || '—')}</small></div>`;
    const local = node(m.localName, m.localIP, role), remote = node(m.remoteName, m.remoteIP, otherRole);
    const engine = engineLabels[m.engine] || m.engine || '—';
    return `<div class="tc-header u-flex-wrap-gap8"><b>${e(t('Tunnel overview'))}</b>${badge(connectionText(m), m.state === 'ok' ? 'ok' : m.state === 'warn' ? 'warn' : 'err')}</div>
      <div class="tv-wrap"><div class="tv ${m.connected ? 'tv-on' : 'tv-off'}${m.state === 'warn' ? ' tv-warn' : ''}" dir="ltr">
        ${m.role === 'foreign' ? remote : local}<div class="tv-link"><div class="tv-tag"><span class="eng" data-no-i18n>${e(engine)}</span>${m.transport ? `<span data-no-i18n>${e(m.transport)}</span>` : ''}<span data-no-i18n>${e(m.family)}</span>${m.mode ? `<span>${e(m.mode)}</span>` : ''}</div>
          <div class="tv-pipe" role="img" aria-label="${e(t(connectionText(m)))}"><span class="ls-arrow" aria-hidden="true">⇄</span></div>
          <div class="tv-meta"><span>${e(t('Service'))}: ${e(t(serviceText(m)))}</span><span>${e(t('Latency'))}: <bdi data-no-i18n>${e(m.latency)}</bdi></span></div></div>${m.role === 'foreign' ? local : remote}</div>
        <div class="tv-ports"><span class="tv-ports-k">${e(t('Forwarded ports'))}</span>${m.ports.length ? m.ports.slice(0, 8).map(p => `<span class="tv-port" data-no-i18n>${e(p)}</span>`).join('') + (m.ports.length > 8 ? `<span class="tv-port more">+${m.ports.length - 8}</span>` : '') : '<span class="tv-port">—</span>'}</div></div>`;
  }
  function details(m) {
    const role = m.role === 'iran' ? 'Iran' : m.role === 'foreign' ? 'Foreign' : 'Unknown';
    const card = (title, rows) => `<div class="det-card"><div class="det-card-head"><span>${e(t(title))}</span></div>${rows}</div>`;
    const nodes = (m.nodes || []).map(n => row('Foreign node', [n.name, n.addr, n.ip, t(n.connected ? 'Connected' : 'Not connected')].filter(Boolean).join(' · '))).join('');
    return `<div class="det-grid">${card('Selected server', row('Name', m.localName) + row('Public IP', m.localIP) + row('Role', t(role)) + row('Remote endpoint', m.remoteIP) + row('Remote name', m.remoteName) + nodes)}
      ${card('Tunnel details', row('Engine', engineLabels[m.engine] || m.engine) + row('Family', m.family) + (m.transport ? row('Transport', m.transport) : '') + row('Inner IP', m.inner) + row('Remote inner IP', m.remoteInner) + row('Control port', text(m.control)) + (m.sshPort ? row('SSH port', text(m.sshPort)) : '') + row('Forwarded ports', m.ports.join(', ')))}
      ${card('Link status', row('Service', t(serviceText(m)) + (m.serviceName ? ' · ' + m.serviceName : '')) + row('Session', t(connectionText(m))) + row('Latency', m.latency) + row('Received', bytes(m.rx)) + row('Sent', bytes(m.tx)) + (m.connections !== null ? row('Connections', String(m.connections)) : ''))}</div>`;
  }

  root.renderTunnelOverview = function renderTunnelOverview(kind) {
    const selected = typeof _tunnelDetail !== 'undefined' ? _tunnelDetail : root._tunnelDetail;
    if (kind === undefined) kind = selected;
    const m = selected !== null && selected !== undefined && kind === selected ? model(kind) : null;
    const find = id => typeof $ === 'function' ? $(id) : typeof document !== 'undefined' ? document.getElementById(id) : null;
    ['tunnelOverviewHeader', 'tunnelOverviewDetails'].forEach((id, i) => {
      const el = find(id); if (!el) return;
      el.hidden = !m;
      if (el.classList && el.classList.toggle) { el.classList.toggle('td-hide', !m); el.classList.toggle('td-keep', !!m); }
      el.innerHTML = m ? (i ? details(m) : hero(m)) : '';
    });
    return m;
  };
})(typeof window !== 'undefined' ? window : globalThis);
