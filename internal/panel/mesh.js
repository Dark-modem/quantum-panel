// mesh.js — the minimal mesh management UI (Phase 2B). Persian/English,
// API-driven, no build step: everything is fetched from /api/mesh/*.
// The page lives at <base>/mesh and reuses the panel session cookie.
// Mutations need the CSRF token; the panel exposes it to same-origin JS
// through the existing pattern (double-submit cookie), see meshCSRF().
(function () {
  'use strict';

  var LANG = localStorage.getItem('mesh_lang') || 'en';
  var T = {
    en: {
      tab_topos: 'Topologies', tab_weights: 'Weights', tab_profiles: 'Profiles',
      tab_traffic: 'Traffic', tab_pg: 'PasarGuard', pg_connect: 'Connect', pg_url: 'Panel URL', pg_user: 'Admin user', pg_pass: 'Admin password', pg_dry: 'Dry-run diff', pg_apply: 'Apply plan', pg_cert: 'Store certificate', pg_install: 'Install command', pg_crosscheck: 'Cross-check usage', title: 'Quantum — Mesh Management',
      tab_engines: 'Engines', eng_artifacts: 'Signed engine artifacts', eng_agents: 'Agents: reported engines',
      eng_upload: 'Upload artifact', eng_version: 'Version', eng_arch: 'Arch', eng_goos: 'OS',
      eng_name: 'Download name (optional)', eng_sig: 'Detached signature (base64 ed25519)',
      eng_key_snippet: 'Agent build snippet (pin this public key)', eng_no_key: 'No engine-artifact public key configured',
      eng_none: 'No artifacts uploaded yet.', eng_sha: 'SHA-256', eng_size: 'Size', eng_uploaded: 'Uploaded',
      backup_btn: '⬇ Backup', backup_ask_pw: 'Panel password (re-auth):', backup_ask_pass: 'Backup passphrase (min 12 chars) — STORE IT SAFELY, without it the backup is unreadable:', backup_ok: 'Backup downloaded (encrypted).', backup_denied: 'Backup denied: wrong panel password.',
      eng_del: 'Remove', eng_warning: 'Coverage warning', eng_agent_col: 'Agent', eng_arch_col: 'Arch',
      eng_engines_col: 'Engines', eng_uploading: 'Uploading…', eng_uploaded_ok: 'Artifact stored',
      topo: 'Topology', members: 'Members', tunnels: 'Tunnels', enabled: 'Enabled',
      engine: 'Engine', state: 'State', up: 'up', down: 'down', unknown: 'unknown',
      tunnel: 'Tunnel', weight: 'Weight', set: 'Set', save: 'Save',
      apply_h: 'Apply hardening profile', apply_n: 'Apply netopt profile',
      confirm: 'Confirm (keep)', rollback: 'Rollback now',
      confirm_banner: 'Safe-apply armed on {agent} — confirm within {s}s or the agent rolls back.',
      no_pending: 'No pending profile confirmations.',
      usage: 'Usage (real bytes)', overhead: 'Engine overhead', coefficient: 'Suggested coefficient',
      wire: 'Wire', payload: 'Payload', dc: 'Datacenter', price: 'Price ratio',
      stale: 'stale', online: 'online', offline: 'offline',
      need_json: 'Profile JSON is invalid', pushed: 'Pushed', skipped: 'Skipped'
    },
    fa: {
      tab_topos: 'توپولوژی‌ها', tab_weights: 'وزن‌ها', tab_profiles: 'پروفایل‌ها',
      tab_traffic: 'ترافیک', tab_pg: 'پازارگارد', pg_connect: 'اتصال', pg_url: 'آدرس پنل', pg_user: 'کاربر ادمین', pg_pass: 'رمز ادمین', pg_dry: 'بررسی تغییرات', pg_apply: 'اعمال برنامه', pg_cert: 'ذخیره گواهی', pg_install: 'دستور نصب', pg_crosscheck: 'مقایسه مصرف', title: 'کوانتوم — مدیریت مش',
      tab_engines: 'موتورها', eng_artifacts: 'موتورهای امضاشده', eng_agents: 'عامل‌ها: موتورهای اعلام‌شده',
      eng_upload: 'بارگذاری فایل', eng_version: 'نسخه', eng_arch: 'معماری', eng_goos: 'سیستم‌عامل',
      eng_name: 'نام دانلود (اختیاری)', eng_sig: 'امضای جداشده (base64 ed25519)',
      eng_key_snippet: 'قطعه کد ساخت عامل (این کلید عمومی را پین کنید)', eng_no_key: 'هیچ کلید عمومی موتور تنظیم نشده',
      eng_none: 'هنوز فایلی بارگذاری نشده.', eng_sha: 'SHA-256', eng_size: 'حجم', eng_uploaded: 'بارگذاری',
      backup_btn: '⬇ پشتیبان', backup_ask_pw: 'رمز پنل (تأیید دوباره):', backup_ask_pass: 'گذرواژهٔ پشتیبان (حداقل ۱۲ نویسه) — آن را ایمن نگه دارید؛ بدون آن پشتیبان خواندنی نیست:', backup_ok: 'پشتیبان رمزنگاری‌شده دانلود شد.', backup_denied: 'پشتیبان رد شد: رمز پنل نادرست است.',
      eng_del: 'حذف', eng_warning: 'هشدار پوشش', eng_agent_col: 'عامل', eng_arch_col: 'معماری',
      eng_engines_col: 'موتورها', eng_uploading: 'در حال بارگذاری…', eng_uploaded_ok: 'فایل ذخیره شد',
      topo: 'توپولوژی', members: 'اعضا', tunnels: 'تونل‌ها', enabled: 'فعال',
      engine: 'موتور', state: 'وضعیت', up: 'برقرار', down: 'قطع', unknown: 'نامشخص',
      tunnel: 'تونل', weight: 'وزن', set: 'ثبت', save: 'ذخیره',
      apply_h: 'اعمال پروفایل hardening', apply_n: 'اعمال پروفایل netopt',
      confirm: 'تأیید (حفظ تغییرات)', rollback: 'بازگردانی فوری',
      confirm_banner: 'حالت apply ایمن روی {agent} فعال شد — تا {s} ثانیه تأیید کنید، وگرنه عامل خودش بازمی‌گرداند.',
      no_pending: 'هیچ پروفایلی در انتظار تأیید نیست.',
      usage: 'مصرف (بایت واقعی)', overhead: 'سربار موتور', coefficient: 'ضریب پیشنهادی',
      wire: 'سیم', payload: 'بار', dc: 'دیتاسنتر', price: 'نرخ',
      stale: 'بی‌اثر', online: 'برخط', offline: 'خارج از خط',
      need_json: 'JSON پروفایل نامعتبر است', pushed: 'ارسال شد', skipped: 'رد شد'
    }
  };
  function t(k) { return (T[LANG] && T[LANG][k]) || T.en[k] || k; }

  // ---- API helpers ------------------------------------------------------
  function base() {
    // The panel's secret base path is the location path sans the page name.
    var p = location.pathname.replace(/\/mesh\/?$/, '/');
    return p;
  }
  function csrfToken() {
    var m = document.cookie.match(/(?:^|;\s*)gre_csrf=([^;]+)/);
    return m ? decodeURIComponent(m[1]) : '';
  }
  function api(method, path, body) {
    var opt = { method: method, headers: { 'X-CSRF-Token': csrfToken() } };
    if (body !== undefined) {
      opt.headers['Content-Type'] = 'application/json';
      opt.body = JSON.stringify(body);
    }
    return fetch(base() + 'api/mesh/' + path, opt).then(function (r) {
      return r.json().then(function (j) {
        if (!r.ok) throw new Error(j.error || ('HTTP ' + r.status));
        return j;
      });
    });
  }
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }
  function say(msg, isErr) {
    var el = document.getElementById('msg');
    el.innerHTML = '<span class="' + (isErr ? 'pill down' : 'muted') + '">' + esc(msg) + '</span>';
  }
  function bytes(n) {
    if (n == null) return '—';
    var u = ['B', 'KB', 'MB', 'GB', 'TB'], i = 0, v = Number(n);
    while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
    return v.toFixed(v >= 100 || i === 0 ? 0 : 1) + ' ' + u[i];
  }

  // ---- tabs --------------------------------------------------------------
  var current = 'topos';
  document.getElementById('tabs').addEventListener('click', function (e) {
    var b = e.target.closest('button[data-tab]');
    if (!b) return;
    current = b.dataset.tab;
    Array.prototype.forEach.call(document.querySelectorAll('#tabs button'), function (x) {
      x.classList.toggle('active', x === b);
    });
    ['topos', 'weights', 'profiles', 'traffic', 'pasarguard', 'engines'].forEach(function (id) {
      document.getElementById('tab-' + id).style.display = id === current ? '' : 'none';
    });
    render();
  });
  document.getElementById('lang').addEventListener('click', function () {
    LANG = LANG === 'en' ? 'fa' : 'en';
    localStorage.setItem('mesh_lang', LANG);
    applyLang(); render();
  });
  function applyLang() {
    document.documentElement.lang = LANG;
    document.documentElement.dir = LANG === 'fa' ? 'rtl' : 'ltr';
    document.getElementById('title').textContent = t('title');
    Array.prototype.forEach.call(document.querySelectorAll('[data-i18n]'), function (el) {
      el.textContent = t(el.dataset.i18n);
    });
  }

  // ---- state ---------------------------------------------------------------
  var overview = null;   // /overview result
  var agents = null;     // /agents result
  var weightsTopo = null;

  function render() {
    if (current === 'topos') renderTopos();
    if (current === 'weights') renderWeights();
    if (current === 'profiles') renderProfiles();
    if (current === 'traffic') renderTraffic();
    if (current === 'pasarguard') renderPG();
    if (current === 'engines') renderEngines();
  }

  function loadAll() {
    return Promise.all([api('GET', 'overview'), api('GET', 'agents')]).then(function (rs) {
      overview = rs[0]; agents = rs[1];
      renderBanner(); render();
    }).catch(function (e) { say(e.message, true); });
  }

  // ---- topologies + tunnels -------------------------------------------------
  function renderTopos() {
    var el = document.getElementById('tab-topos');
    if (!overview) { el.innerHTML = '<p class="muted">…</p>'; return; }
    var html = '';
    (overview.topologies || []).forEach(function (tp) {
      html += '<h3>' + esc(tp.name) + (tp.enabled ? '' : ' <span class="pill warn">' + esc(t('enabled')) + ': off</span>') + '</h3>';
      html += '<p class="muted">' + esc(t('members')) + ': ' + (tp.members || []).length +
        ' · ' + esc(t('tunnels')) + ': ' + esc(tp.tunnel_count) + '</p>';
      var to = (overview.topologies || []).indexOf(tp) + 1;
      var tun = topoTunnels(tp);
      if (tun.length) {
        html += '<table><tr><th>' + esc(t('tunnel')) + '</th><th>' + esc(t('engine')) + '</th><th>' + esc(t('state')) + '</th></tr>';
        tun.forEach(function (tn) {
          var rt = runtimeFor(tn.tunnel_key);
          var st = rt == null ? t('unknown')
            : (rt.up ? '<span class="pill ok">' + esc(t('up')) + '</span>'
                     : '<span class="pill down">' + esc(t('down')) + '</span>');
          html += '<tr><td>' + esc(tn.tunnel_key) + '</td><td>' + esc(tn.engine) + '</td><td>' + st + '</td></tr>';
        });
        html += '</table>';
      }
    });
    el.innerHTML = html;
  }
  function topoTunnels(tp) {
    // The overview omits tunnel rows for size; fetch per topology lazily.
    if (tp.__tunnels) return tp.__tunnels;
    api('GET', 'topologies/' + tp.id).then(function (j) {
      tp.__tunnels = j.tunnels || [];
      if (current === 'topos') renderTopos();
    }).catch(function () {});
    return tp.__tunnels || [];
  }
  function runtimeFor(key) {
    if (!agents) return null;
    var found = null;
    (agents.agents || []).forEach(function (a) {
      (a.runtime || []).forEach(function (rt) { if (rt.tunnel_id === key) found = rt; });
    });
    return found;
  }

  // ---- weights ---------------------------------------------------------------
  function renderWeights() {
    var el = document.getElementById('tab-weights');
    if (!overview) { el.innerHTML = '<p class="muted">…</p>'; return; }
    var html = '<div class="row"><span class="muted">' + esc(t('topo')) + ':</span><select id="wtopo">';
    (overview.topologies || []).forEach(function (tp) {
      html += '<option value="' + tp.id + '"' + (weightsTopo == tp.id ? ' selected' : '') + '>' + esc(tp.name) + '</option>';
    });
    html += '</select></div><div id="wbody"></div>';
    el.innerHTML = html;
    document.getElementById('wtopo').addEventListener('change', function (e) {
      weightsTopo = Number(e.target.value); loadWeights();
    });
    if (!weightsTopo && (overview.topologies || []).length) weightsTopo = overview.topologies[0].id;
    loadWeights();
  }
  function loadWeights() {
    api('GET', 'topologies/' + weightsTopo).then(function (j) {
      // Resolution order (same specificity chain as the panel store):
      // tunnel > server pair (local,remote) > topology. The editor prefills
      // the RESOLVED value and shows the scope it came from; saving writes
      // a tunnel-scoped weight (most specific), which then becomes the
      // resolved one.
      var wts = j.weights || [];
      var findW = function (pred) {
        for (var k = 0; k < wts.length; k++) {
          if (wts[k].weight != null && pred(wts[k])) return wts[k];
        }
        return null;
      };
      var rows = (j.tunnels || []).map(function (tn) {
        var hit = findW(function (wp) { return wp.tunnel_id === tn.id; });
        var scope = 'default';
        if (hit) {
          scope = 'tunnel';
        } else {
          hit = findW(function (wp) {
            return !wp.tunnel_id && wp.local_server_id === tn.local_server_id &&
                   wp.remote_server_id === tn.remote_server_id;
          });
          if (hit) scope = 'server pair';
        }
        if (!hit) {
          hit = findW(function (wp) {
            return !wp.tunnel_id && !wp.local_server_id && !wp.remote_server_id;
          });
          if (hit) scope = 'topology';
        }
        if (!hit) hit = { weight: 1 };
        var resolved = hit.weight;
        var scopeTag = '<span class="pill ' + (scope === 'tunnel' ? 'ok' : '') + '">' +
          esc(scope) + '</span> <span class="muted">resolved ' + resolved + '</span>';
        return '<tr><td>' + esc(tn.tunnel_key) + '</td>' +
          '<td><input type="number" min="0" max="1000" value="' + resolved + '" data-tid="' + tn.id + '" style="width:80px"></td>' +
          '<td>' + scopeTag + '</td>' +
          '<td><button class="act primary" data-save="' + tn.id + '">' + esc(t('save')) + '</button></td></tr>';
      }).join('');
      document.getElementById('wbody').innerHTML =
        '<table><tr><th>' + esc(t('tunnel')) + '</th><th>' + esc(t('weight')) + '</th><th>' +
        (LANG === 'fa' ? 'منبع' : 'resolved from') + '</th><th></th></tr>' + rows + '</table>' +
        '<p class="muted">0 = ' + (LANG === 'fa' ? 'خارج از چرخش' : 'excluded from balancing') +
        ' · ' + (LANG === 'fa' ? 'ترتیب حل: تونل ← جفت‌سرور ← توپولوژی' : 'resolution: tunnel > server pair > topology') + '</p>';
      document.getElementById('wbody').addEventListener('click', function (e) {
        var b = e.target.closest('button[data-save]');
        if (!b) return;
        var tid = b.dataset.save;
        var input = document.querySelector('input[data-tid="' + tid + '"]');
        api('POST', 'topologies/' + weightsTopo + '/weights',
          { tunnel_id: Number(tid), weight: Number(input.value) })
          .then(function () { say(t('set') + ' #' + tid); loadWeights(); })
          .catch(function (er) { say(er.message, true); });
      });
    }).catch(function (e) { say(e.message, true); });
  }

  // ---- profiles (safe-apply) ---------------------------------------------------
  function renderProfiles() {
    var el = document.getElementById('tab-profiles');
    api('GET', 'agents').then(function (j) {
      var rows = (j.agents || []).map(function (a) {
        return '<tr><td>' + esc(a.id) + '</td><td>' + esc(a.name || '') + '</td>' +
          '<td>v' + esc(a.proto_version || 1) + '</td><td>' + esc(a.status) + '</td>' +
          '<td>' +
          '<button class="act" data-h="' + a.id + '">' + esc(t('apply_h')) + '</button> ' +
          '<button class="act" data-n="' + a.id + '">' + esc(t('apply_n')) + '</button></td></tr>';
      }).join('');
      el.innerHTML = '<table><tr><th>Agent</th><th>Name</th><th>Proto</th><th>Status</th><th></th></tr>' + rows + '</table>' +
        '<textarea id="prof" rows="6" style="width:100%;background:#11151d;color:inherit;border:1px solid #2a3140;border-radius:8px;padding:8px">{}</textarea>' +
        '<p class="muted">' + (LANG === 'fa'
          ? 'پروفایل JSON برای هر دو دکمه بالا استفاده می‌شود. اعمال، تایمر ۱۲۰ ثانیه‌ای بازگردانی را فعال می‌کند.'
          : 'The JSON above is the profile both buttons apply. Applying arms the agent automatic 120 s rollback.') + '</p>';
      el.addEventListener('click', profileClick);
    }).catch(function (e) { say(e.message, true); });
  }
  function profileClick(e) {
    var h = e.target.closest('button[data-h]');
    var n = e.target.closest('button[data-n]');
    if (!h && !n) return;
    var kind = h ? 'hardening' : 'netopt';
    var agent = h ? h.dataset.h : n.dataset.n;
    var prof;
    try { prof = JSON.parse(document.getElementById('prof').value); }
    catch (err) { say(t('need_json'), true); return; }
    api('POST', 'agents/' + agent + '/profile/' + kind,
      { profile: prof, rollback_timer_s: 120 })
      .then(function (r) { say((LANG === 'fa' ? 'اعمال شد؛ ' : 'applied; ') + r.rollback_timer_s + 's'); renderBanner(); })
      .catch(function (er) { say(er.message, true); });
  }
  function renderBanner() {
    var el = document.getElementById('confirm-banner');
    api('GET', 'profiles/pending').then(function (j) {
      var p = (j.pending || [])[0];
      if (!p) { el.style.display = 'none'; return; }
      var left = Math.max(0, p.deadline - Math.floor(Date.now() / 1000));
      el.style.display = '';
      el.innerHTML = '<b>⏱ ' + esc(t('confirm_banner').replace('{agent}', p.agent_id).replace('{s}', left)) + '</b><br>' +
        '<button class="act primary" id="btn-confirm">' + esc(t('confirm')) + '</button> ' +
        '<button class="act" id="btn-rollback">' + esc(t('rollback')) + '</button>';
      document.getElementById('btn-confirm').onclick = function () {
        api('POST', 'agents/' + p.agent_id + '/profile/confirm', {})
          .then(function () { say('confirmed'); renderBanner(); })
          .catch(function (er) { say(er.message, true); });
      };
      document.getElementById('btn-rollback').onclick = function () {
        api('POST', 'agents/' + p.agent_id + '/profile/rollback?kind=' + p.kind, {})
          .then(function () { say('rolled back'); renderBanner(); })
          .catch(function (er) { say(er.message, true); });
      };
      // live countdown
      var iv = setInterval(function () {
        left -= 1;
        var b = el.querySelector('b');
        if (!b || left < 0) { clearInterval(iv); return; }
        b.textContent = '⏱ ' + t('confirm_banner').replace('{agent}', p.agent_id).replace('{s}', left);
      }, 1000);
    }).catch(function () { el.style.display = 'none'; });
  }

  // ---- traffic + overhead ---------------------------------------------------------
  function renderTraffic() {
    var el = document.getElementById('tab-traffic');
    if (!overview) { el.innerHTML = '<p class="muted">…</p>'; return; }
    el.innerHTML = '<div class="row"><span class="muted">' + esc(t('dc')) + ':</span><select id="udc">' +
      (overview.datacenters || []).map(function (d) {
        return '<option value="' + d.id + '">' + esc(d.name) + '</option>';
      }).join('') +
      '</select><button class="act" id="ubtn">' + esc(t('usage')) + '</button>' +
      '<button class="act" id="cbtn">' + esc(t('coefficient')) + '</button></div><div id="uout"></div>';
    document.getElementById('ubtn').onclick = function () {
      var dc = document.getElementById('udc').value;
      var from = Math.floor(Date.now() / 1000) - 86400;
      api('GET', 'usage?datacenter_id=' + dc + '&from=' + from).then(function (r) {
        var u = r.usage;
        document.getElementById('uout').innerHTML =
          '<table><tr><th></th><th>' + esc(t('wire')) + '</th><th>' + esc(t('payload')) + '</th></tr>' +
          '<tr><td>RX</td><td>' + bytes(u.wire_rx) + '</td><td>' + bytes(u.payload_rx) + '</td></tr>' +
          '<tr><td>TX</td><td>' + bytes(u.wire_tx) + '</td><td>' + bytes(u.payload_tx) + '</td></tr></table>' +
          '<p class="muted">' + esc(r.note) + '</p>';
      }).catch(function (e) { say(e.message, true); });
    };
    document.getElementById('cbtn').onclick = function () {
      var dc = document.getElementById('udc').value;
      api('GET', 'coefficient?datacenter_id=' + dc).then(function (r) {
        var rows = (r.suggestions || []).map(function (sg) {
          return '<tr><td>' + esc(sg.engine) + '</td><td>' + (sg.overhead || 0).toFixed(3) + '</td><td>' +
            (sg.suggested_coefficient || 0).toFixed(3) + '</td></tr>';
        }).join('');
        document.getElementById('uout').innerHTML =
          '<table><tr><th>' + esc(t('engine')) + '</th><th>' + esc(t('overhead')) + '</th><th>' + esc(t('coefficient')) + '</th></tr>' +
          rows + '</table><p class="muted">' + esc(r.note) + '</p>';
      }).catch(function (e) { say(e.message, true); });
    };
  }


  // ---- pasarguard ------------------------------------------------------------
  function renderPG() {
    var el = document.getElementById('tab-pasarguard');
    api('GET', 'pasarguard/status').then(function (st) {
      var html = '<div class="row">' +
        '<input id="pg-url" placeholder="' + esc(t('pg_url')) + ' (https://pg.example:8000)" style="width:280px">' +
        '<input id="pg-user" placeholder="' + esc(t('pg_user')) + '">' +
        '<input id="pg-pass" type="password" placeholder="' + esc(t('pg_pass')) + '">' +
        '<button class="act primary" id="pg-connect">' + esc(t('pg_connect')) + '</button>' +
        '<span class="muted" id="pg-status">' + (st.configured ? esc(st.url) + ' ✓' : '') + '</span></div>' +
        '<div class="row">' +
        '<button class="act" id="pg-dry">' + esc(t('pg_dry')) + '</button>' +
        '<button class="act" id="pg-apply">' + esc(t('pg_apply')) + '</button>' +
        '<button class="act" id="pg-cross">' + esc(t('pg_crosscheck')) + '</button></div>' +
        '<div id="pg-out"></div>';
      el.innerHTML = html;
      document.getElementById('pg-connect').onclick = function () {
        api('POST', 'pasarguard/connect', {
          url: document.getElementById('pg-url').value,
          username: document.getElementById('pg-user').value,
          password: document.getElementById('pg-pass').value
        }).then(function (r) { say('connected: ' + r.nodes + ' nodes'); renderPG(); })
          .catch(function (e) { say(e.message, true); });
      };
      document.getElementById('pg-dry').onclick = function () { pgReconcile(false); };
      document.getElementById('pg-apply').onclick = function () { pgReconcile(true); };
      document.getElementById('pg-cross').onclick = function () { pgCross(); };
      pgNodes();
    }).catch(function (e) { el.innerHTML = '<p class="muted">' + esc(e.message) + '</p>'; });
  }
  function pgReconcile(apply) {
    var out = document.getElementById('pg-out');
    var body = { apply: !!apply };
    if (apply) body.confirm = 'yes';
    api('POST', 'pasarguard/reconcile', body).then(function (r) {
      var rows = (r.plan.actions || []).map(function (a) {
        return '<tr><td>' + esc(a.action) + '</td><td>' + esc(a.name) + '</td><td>' + esc(a.detail) + '</td></tr>';
      }).join('');
      out.innerHTML = '<table><tr><th>Action</th><th>Node</th><th>Detail</th></tr>' + rows + '</table>' +
        '<p class="muted">creates=' + r.plan.creates + ' updates=' + r.plan.updates +
        ' reconnects=' + r.plan.reconnects + ' stale=' + r.plan.stale + ' applied=' + r.applied + '</p>' +
        (r.warnings || []).map(function (w) { return '<p class="muted">⚠ ' + esc(w) + '</p>'; }).join('');
    }).catch(function (e) { say(e.message, true); });
  }
  function pgNodes() {
    api('GET', 'pasarguard/nodes').then(function (r) {
      var rows = (r.nodes || []).map(function (n) {
        var owned = n.name.indexOf('quantum:') === 0;
        return '<tr><td>' + esc(n.name) + (owned ? ' <span class="pill ok">Q</span>' : '') + '</td><td>' + esc(n.address) +
          '</td><td>' + (n.usage_coefficient || 1) + '</td><td><span class="pill ' +
          (n.status === 'connected' ? 'ok' : 'down') + '">' + esc(n.status) + '</span></td></tr>';
      }).join('');
      var box = document.getElementById('pg-out');
      if (!box) return;
      box.insertAdjacentHTML('afterbegin',
        '<table><tr><th>Node</th><th>Address</th><th>Coefficient</th><th>Status</th></tr>' + rows + '</table>');
    }).catch(function () {});
  }
  function pgCross() {
    var dc = (overview && overview.datacenters && overview.datacenters[0]) ? overview.datacenters[0].id : 0;
    if (!dc) { say('no datacenter configured', true); return; }
    api('GET', 'pasarguard/crosscheck?datacenter_id=' + dc).then(function (r) {
      var rows = (r.rows || []).map(function (x) {
        return '<tr><td>' + esc(x.pg_node_name) + '</td><td>' + bytes(x.pg_total) + '</td><td>' + bytes(x.quantum_wire) +
          '</td><td>' + (x.ratio_wire || 0).toFixed(2) + '</td><td>' +
          (x.drift ? '<span class="pill down">drift</span>' : '<span class="pill ok">ok</span>') + '</td></tr>';
      }).join('');
      document.getElementById('pg-out').innerHTML =
        '<table><tr><th>PG node</th><th>PG usage</th><th>Quantum wire</th><th>Ratio</th><th></th></tr>' + rows + '</table>' +
        '<p class="muted">configured=' + (r.configured_coefficient || 1) + ' suggested=' +
        (r.suggested_coefficient || 0).toFixed(3) + ' · ' + esc(r.note) + '</p>';
    }).catch(function (e) { say(e.message, true); });
  }

  // ---- engines (Phase 5 R5: signed engine artifact store) --------------------
  // Multipart upload: fetch with the CSRF header, no Content-Type override
  // (the browser sets the multipart boundary).
  function apiForm(path, form) {
    return fetch(base() + 'api/mesh/' + path, {
      method: 'POST', headers: { 'X-CSRF-Token': csrfToken() }, body: form
    }).then(function (r) {
      return r.json().then(function (j) {
        if (!r.ok) throw new Error(j.error || ('HTTP ' + r.status));
        return j;
      });
    });
  }
  function renderEngines() {
    var el = document.getElementById('tab-engines');
    api('GET', 'engines').then(function (d) {
      var warns = (d.warnings || []).map(function (w) {
        return '<div class="row"><span class="pill warn">⚠ ' + esc(t('eng_warning')) + '</span>' +
          '<span class="muted">' + esc(w.text) + '</span></div>';
      }).join('');
      var arts = (d.artifacts || []).map(function (a) {
        return '<tr><td>' + esc(a.engine) + '</td><td>' + esc(a.version) + '</td><td>' + esc(a.goos) + '/' + esc(a.goarch) +
          '</td><td class="muted" style="max-width:180px;overflow:hidden;text-overflow:ellipsis">' + esc(a.sha256.slice(0, 16)) + '…</td>' +
          '<td>' + bytes(a.size) + '</td><td class="muted">' + esc(a.artifact) + '</td>' +
          '<td><button class="act" data-del-art="' + a.id + '">' + esc(t('eng_del')) + '</button></td></tr>';
      }).join('');
      var ags = (d.agents || []).map(function (ag) {
        var pills = (ag.engines || []).map(function (e) {
          return '<span class="pill ' + (e.version ? 'ok' : 'warn') + '">' + esc(e.id) +
            (e.version ? ' ' + esc(e.version) : '') + '</span>';
        }).join(' ');
        return '<tr><td>' + esc(ag.name || ag.id) + '</td><td>' + esc(ag.arch || '?') + '</td><td>' +
          (pills || '<span class="muted">—</span>') + '</td></tr>';
      }).join('');
      var keyBox = d.key_configured
        ? '<p class="muted" id="eng-key-line">' + esc(t('eng_key_snippet')) + '…</p>'
        : '<p class="muted">⚠ ' + esc(t('eng_no_key')) + '</p>';
      el.innerHTML = warns +
        '<h3 style="font-size:0.98rem">' + esc(t('eng_artifacts')) + '</h3>' +
        (arts
          ? '<table><tr><th>' + esc(t('engine')) + '</th><th>' + esc(t('eng_version')) + '</th><th>' + esc(t('eng_goos')) + '/' + esc(t('eng_arch')) +
            '</th><th>' + esc(t('eng_sha')) + '</th><th>' + esc(t('eng_size')) + '</th><th>' + esc(t('eng_name')) + '</th><th></th></tr>' + arts + '</table>'
          : '<p class="muted">' + esc(t('eng_none')) + '</p>') +
        '<div class="row"><input type="file" id="eng-file">' +
        '<input id="eng-engine" placeholder="' + esc(t('engine')) + ' (frp)" style="width:110px">' +
        '<input id="eng-version" placeholder="' + esc(t('eng_version')) + ' (0.61.1)" style="width:110px">' +
        '<select id="eng-arch"><option value="amd64">amd64</option><option value="arm64">arm64</option></select>' +
        '<input id="eng-name" placeholder="' + esc(t('eng_name')) + '" style="width:220px">' +
        '<input id="eng-sig" placeholder="' + esc(t('eng_sig')) + '" style="width:280px">' +
        '<button class="act primary" id="eng-upload">' + esc(t('eng_upload')) + '</button></div>' +
        '<h3 style="font-size:0.98rem">' + esc(t('eng_agents')) + '</h3>' +
        '<table><tr><th>' + esc(t('eng_agent_col')) + '</th><th>' + esc(t('eng_arch_col')) + '</th><th>' + esc(t('eng_engines_col')) + '</th></tr>' + ags + '</table>' +
        keyBox;
      document.getElementById('eng-upload').onclick = function () {
        var f = document.getElementById('eng-file').files[0];
        var engine = document.getElementById('eng-engine').value.trim();
        if (!f || !engine) { say(t('eng_upload') + ' ?', true); return; }
        say(t('eng_uploading'));
        var form = new FormData();
        form.append('file', f);
        form.append('engine', engine);
        form.append('version', document.getElementById('eng-version').value.trim());
        form.append('goarch', document.getElementById('eng-arch').value);
        form.append('goos', 'linux');
        var nm = document.getElementById('eng-name').value.trim();
        if (nm) form.append('name', nm);
        form.append('signature', document.getElementById('eng-sig').value.trim());
        apiForm('engines/artifacts', form).then(function (r) {
          say(t('eng_uploaded_ok') + ': ' + r.artifact + ' (' + bytes(r.size) + ')');
          renderEngines();
        }).catch(function (e) { say(e.message, true); });
      };
      Array.prototype.forEach.call(el.querySelectorAll('[data-del-art]'), function (b) {
        b.onclick = function () {
          api('DELETE', 'engines/artifacts/' + b.dataset.delArt).then(function () { renderEngines(); })
            .catch(function (e) { say(e.message, true); });
        };
      });
      if (d.key_configured) {
        api('GET', 'engines/keysnippet').then(function (k) {
          var line = document.getElementById('eng-key-line');
          if (line && k.configured) {
            line.innerHTML = esc(t('eng_key_snippet')) + ':<br><code style="font-size:0.78rem;word-break:break-all">' +
              esc('go build ' + k.agent_ldflags) + '</code>';
          } else if (line && k.hint) {
            line.textContent = k.hint;
          }
        }).catch(function () {});
      }
    }).catch(function (e) { el.innerHTML = '<p class="muted">' + esc(e.message) + '</p>'; });
  }

  // ---- backup (TASK 2): encrypted download behind re-auth ---------------
  document.getElementById('backup').addEventListener('click', function () {
    var pw = window.prompt(t('backup_ask_pw'));
    if (!pw) return;
    var pass = window.prompt(t('backup_ask_pass'));
    if (!pass || pass.length < 12) { say(t('backup_denied'), true); return; }
    fetch(base() + 'api/mesh/backup', {
      method: 'POST',
      headers: { 'X-CSRF-Token': csrfToken(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ password: pw, passphrase: pass })
    }).then(function (r) {
      if (!r.ok) { say(t('backup_denied'), true); return null; }
      return r.blob();
    }).then(function (blob) {
      if (!blob) return;
      var a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = 'quantum-panel-backup.qpbak';
      document.body.appendChild(a); a.click(); a.remove();
      say(t('backup_ok'));
    }).catch(function (e) { say(e.message, true); });
  });

  applyLang();
  loadAll();
  setInterval(loadAll, 10000); // live state, ~heartbeat cadence
})();
