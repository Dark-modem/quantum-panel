// wizard.js — the /mesh setup wizard (phase 7): a step-by-step fa/en flow
// for a non-expert. EVERYTHING rides the existing mesh REST API — the
// wizard holds no state of its own beyond the in-progress form; the
// server validates every call (the same endpoints the API docs describe).
//
// Steps: 1 datacenters → 2 iran servers (+enroll) → 3 foreign servers
// (+enroll) → 4 tunnel slots per pair (engines limited to signed
// artifacts for the agent's arch) → 5 weights per foreign server
// (slider, resolved preview) → 6 forward rules → 7 review+create
// (generator API, then weights+forwards applied to the new topology,
// then a live ack/state page).
(function () {
  'use strict';

  var LANG = localStorage.getItem('wiz_lang') || 'en';
  var S = { dcs: [], iran: [], foreign: [], pairs: [], weights: {}, forwards: [],
            params: {}, topoId: 0, step: 1 };
  var T = {
    en: {
      title: 'Quantum — Setup Wizard',
      ep_title: 'Parameters', ep_gen: 'generate', ep_set: 'set', ep_unset: 'not set',
      ep_secret_note: 'secrets are write-only: they never come back after saving',
      ep_no_tpl: 'no parameter template for this engine — raw JSON only',
      s1: 'Datacenters', s2: 'Iran servers', s3: 'Foreign servers', s4: 'Tunnel slots',
      s5: 'Weights', s6: 'Forwards', s7: 'Review & create',
      dc_name: 'Datacenter name', coeff: 'Price coefficient', add_dc: 'Add datacenter',
      srv_name: 'Server name', srv_addr: 'Address (IP or host)', add_srv: 'Add server',
      install_cmd: 'Install command (run on the server):', copy: 'Copy',
      waiting: 'waiting for the agent to connect…', connected: 'agent connected ✓',
      next: 'Next ›', back: '‹ Back', create: 'Create topology',
      per_pair: 'tunnels per pair', engine: 'engine', k_hint: 'K tunnels per pair',
      no_artifact: 'no signed artifact for this agent arch — upload one in the Engines tab',
      weight: 'Weight', resolved: 'resolved', forward_port: 'Listen port', proto: 'Proto',
      target: 'Target (host:port)', add_fwd: 'Add forward rule',
      review: 'Review', created: 'Topology created — waiting for acks…',
      tunnels: 'tunnels', pairs: 'pairs', state: 'State',
      need_dc: 'Add at least one Iran datacenter first.',
      need_srv: 'Add at least one server here first.',
      need_engines: 'No engine has a signed artifact for the agents — upload one in the Engines tab first.'
    },
    fa: {
      title: 'کوانتوم — راهنمای راه‌اندازی',
      ep_title: 'پارامترها', ep_gen: 'تولید', ep_set: 'ثبت شده', ep_unset: 'ثبت نشده',
      ep_secret_note: 'رمزها فقط یک‌بار نوشته می‌شوند و پس از ذخیره هرگز نمایش داده نمی‌شوند',
      ep_no_tpl: 'برای این موتور قالبی نیست — JSON خام',
      s1: 'دیتاسنترها', s2: 'سرورهای ایران', s3: 'سرورهای خارج', s4: 'اسلات تونل‌ها',
      s5: 'وزن‌ها', s6: 'فورواردها', s7: 'بازبینی و ساخت',
      dc_name: 'نام دیتاسنتر', coeff: 'ضریب قیمت', add_dc: 'افزودن دیتاسنتر',
      srv_name: 'نام سرور', srv_addr: 'آدرس (IP یا هاست)', add_srv: 'افزودن سرور',
      install_cmd: 'دستور نصب (روی سرور اجرا کنید):', copy: 'کپی',
      waiting: 'در انتظار اتصال عامل…', connected: 'عامل متصل شد ✓',
      next: 'بعدی ›', back: '‹ قبلی', create: 'ساخت توپولوژی',
      per_pair: 'تونل به ازای هر جفت', engine: 'موتور', k_hint: 'K تونل برای هر جفت',
      no_artifact: 'فایل امضاشده برای معماری این عامل نیست — از تب Engines بارگذاری کنید',
      weight: 'وزن', resolved: 'نهایی', forward_port: 'پورت شنونده', proto: 'پروتکل',
      target: 'مقصد (هاست:پورت)', add_fwd: 'افزودن فوروارد',
      review: 'بازبینی', created: 'توپولوژی ساخته شد — در انتظار تأیید عامل‌ها…',
      tunnels: 'تونل', pairs: 'جفت', state: 'وضعیت',
      need_dc: 'ابتدا حداقل یک دیتاسنتر ایران اضافه کنید.',
      need_srv: 'ابتدا حداقل یک سرور اینجا اضافه کنید.',
      need_engines: 'هیچ موتوری فایل امضاشده ندارد — ابتدا از تب Engines بارگذاری کنید.'
    }
  };
  function t(k) { return (T[LANG] && T[LANG][k]) || T.en[k] || k; }

  function csrfToken() {
    var m = document.cookie.match(/(?:^|;\s*)gre_csrf=([^;]+)/);
    return m ? decodeURIComponent(m[1]) : '';
  }
  function base() { return location.pathname.replace(/\/mesh-wizard\/?$/, '/'); }
  function api(method, path, body) {
    var opt = { method: method, headers: { 'X-CSRF-Token': csrfToken() } };
    if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
    return fetch(base() + 'api/mesh/' + path, opt).then(function (r) {
      return r.json().then(function (j) {
        if (!r.ok) throw new Error(j.error || ('HTTP ' + r.status));
        return j;
      });
    });
  }
  function agentAPI(method, path, body) {
    var opt = { method: method, headers: { 'X-CSRF-Token': csrfToken() } };
    if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
    return fetch(base() + path, opt).then(function (r) {
      return r.json().then(function (j) {
        if (!r.ok) throw new Error(j.error || j.message || ('HTTP ' + r.status));
        return j;
      });
    });
  }
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }
  function say(m, isErr) {
    var el = document.getElementById('msg');
    el.innerHTML = '<span class="' + (isErr ? 'pill down' : 'muted') + '">' + esc(m) + '</span>';
  }

  // ---- step renderers ----------------------------------------------------
  var STEPS = ['s1', 's2', 's3', 's4', 's5', 's6', 's7'];

  function renderSteps() {
    var el = document.getElementById('steps');
    el.innerHTML = STEPS.map(function (s, i) {
      var cls = (i + 1 === S.step ? 'on' : (i + 1 < S.step ? 'done' : ''));
      return '<span class="' + cls + '">' + (i + 1) + '. ' + esc(t(s)) + '</span>';
    }).join('');
  }

  function step1() {
    var el = document.getElementById('step-body');
    el.innerHTML = '<div class="card"><div class="row">' +
      '<input id="dc-name" placeholder="' + esc(t('dc_name')) + '">' +
      '<input id="dc-coeff" type="number" step="0.1" min="0.1" value="1.0" style="width:110px" title="' + esc(t('coeff')) + '">' +
      '<button class="act primary" id="add-dc">' + esc(t('add_dc')) + '</button></div>' +
      '<table id="dc-table"><tr><th>' + esc(t('dc_name')) + '</th><th>' + esc(t('coeff')) + '</th></tr></table></div>';
    document.getElementById('add-dc').onclick = function () {
      var name = document.getElementById('dc-name').value.trim();
      var coeff = parseFloat(document.getElementById('dc-coeff').value) || 1.0;
      if (!name) { say(t('dc_name'), true); return; }
      api('POST', 'datacenters', { name: name, kind: 'iran', price_coefficient: coeff })
        .then(function () { S.dcs.push({ name: name, coeff: coeff }); say(''); step1(); })
        .catch(function (e) { say(e.message, true); });
    };
    var rows = S.dcs.map(function (d) {
      return '<tr><td>' + esc(d.name) + '</td><td>' + d.coeff + '</td></tr>';
    }).join('');
    document.getElementById('dc-table').insertAdjacentHTML('beforeend', rows);
  }

  // Servers: iran (per datacenter) or foreign. Each added server gets a
  // fresh enroll token + the exact install command + a live indicator.
  function stepServers(kind) {
    var el = document.getElementById('step-body');
    var html = '';
    if (kind === 'iran') {
      S.dcs.forEach(function (d, di) {
        html += '<div class="card"><b>' + esc(d.name) + '</b><div class="row">' +
          '<input id="srv-name-' + di + '" placeholder="' + esc(t('srv_name')) + '">' +
          '<input id="srv-addr-' + di + '" placeholder="' + esc(t('srv_addr')) + '">' +
          '<button class="act primary" data-add-iran="' + di + '">' + esc(t('add_srv')) + '</button></div>' +
          '<div data-srvlist="' + di + '"></div></div>';
      });
    } else {
      html += '<div class="card"><div class="row">' +
        '<input id="srv-name-f" placeholder="' + esc(t('srv_name')) + '">' +
        '<input id="srv-addr-f" placeholder="' + esc(t('srv_addr')) + '">' +
        '<button class="act primary" data-add-f="1">' + esc(t('add_srv')) + '</button></div>' +
        '<div data-srvlist="f"></div></div>';
    }
    el.innerHTML = html;
    var listEl = function (k) { return document.querySelector('[data-srvlist="' + k + '"]'); };
    var renderList = function (k, list) {
      listEl(k).innerHTML = list.map(function (s) {
        var state = s.online ? '<span class="pill ok">' + esc(t('connected')) + '</span>'
                             : '<span class="pill warn">' + esc(t('waiting')) + '</span>';
        return '<div class="row"><b>' + esc(s.name) + '</b> ' + state +
          (s.online ? '' : '<pre class="cmd">' + esc(s.installCmd) + '</pre>' +
           '<button class="act" data-copy="' + esc(s.name) + '">' + esc(t('copy')) + '</button>') + '</div>';
      }).join('');
      listEl(k).querySelectorAll('[data-copy]').forEach(function (b) {
        b.onclick = function () {
          var s = list.filter(function (x) { return x.name === b.dataset.copy; })[0];
          if (s) navigator.clipboard.writeText(s.installCmd).then(function () { say('copied ✓'); });
        };
      });
    };
    var addServer = function (name, addr, dcId, role, listKey, list) {
      if (!name || !addr) { say(t('srv_name') + ' / ' + t('srv_addr'), true); return; }
      api('POST', 'servers', { name: name, role: role, datacenter_id: dcId, address: addr })
        .then(function (sv) {
          return agentAPI('POST', 'api/agents/tokens', { role: role, label: name, expires_days: 7 })
            .then(function (tok) {
              var url = location.origin + base().replace(/\/$/, '');
              var cmd = 'sudo bash install-agent.sh --server ' + url + ' --token ' + tok.token +
                        ' --name ' + name + ' --role ' + role;
              list.push({ name: name, id: sv.id, online: false, installCmd: cmd });
              renderList(listKey, list);
              pollAgent(listKey, list, name);
              say('');
            });
        })
        .catch(function (e) { say(e.message, true); });
    };
    var pollAgent = function (listKey, list, name) {
      var iv = setInterval(function () {
        agentAPI('GET', 'api/agents').then(function (r) {
          var recs = r.agents || r || [];
          var rec = (Array.isArray(recs) ? recs : []).filter(function (a) { return a.name === name; })[0];
          if (rec && rec.status === 'online') {
            clearInterval(iv);
            list.forEach(function (x) { if (x.name === name) x.online = true; });
            renderList(listKey, list);
          }
        }).catch(function () {});
      }, 3000);
    };
    if (kind === 'iran') {
      S.dcs.forEach(function (d, di) {
        S['iranDc' + di] = S['iranDc' + di] || [];
        renderList(di, S['iranDc' + di]);
        var btn = document.querySelector('[data-add-iran="' + di + '"]');
        if (btn) btn.onclick = function () {
          if (!S.dcs.length) { say(t('need_dc'), true); return; }
          addServer(document.getElementById('srv-name-' + di).value.trim(),
                    document.getElementById('srv-addr-' + di).value.trim(),
                    d.id, 'iran', di, S['iranDc' + di]);
        };
      });
    } else {
      S.foreign = S.foreign || [];
      renderList('f', S.foreign);
      var fb = document.querySelector('[data-add-f]');
      if (fb) fb.onclick = function () {
        addServer(document.getElementById('srv-name-f').value.trim(),
                  document.getElementById('srv-addr-f').value.trim(),
                  0, 'foreign', 'f', S.foreign);
      };
    }
  }

  // ---- engine parameter templates (phase 9 2a) ------------------------------
  var TPL = null; // cached GET engines/templates
  function templates() {
    if (TPL) return Promise.resolve(TPL);
    return api('GET', 'engines/templates').then(function (r) {
      TPL = {};
      (r.templates || []).forEach(function (t) { TPL[t.engine] = t.fields; });
      return TPL;
    });
  }
  // Render one typed param form; returns the inner HTML. Secret fields
  // render as password inputs with a generate button; the value lives
  // only in the DOM until the form is submitted.
  function paramFormHTML(engine, key, current) {
    var fields = (TPL && TPL[engine]) || null;
    if (!fields) return '<p class="muted">' + esc(t('ep_no_tpl')) + '</p>';
    var cur = {};
    try { cur = JSON.parse(current || '{}'); } catch (e) {}
    var html = '<p class="muted">' + esc(t('ep_secret_note')) + '</p>';
    fields.forEach(function (f, i) {
      var id = 'ep-' + key + '-' + i;
      var label = LANG === 'fa' ? f.label_fa : f.label_en;
      var v = cur[f.json];
      var maskedSet = f.kind === 'secret' && typeof v === 'string' && v !== '';
      html += '<div class="row"><label for="' + id + '">' + esc(label) + '</label>';
      if (f.kind === 'number') {
        html += '<input type="number" id="' + id + '" data-ep="' + f.json + '" data-kind="number" value="' + esc(v != null ? v : (f.default || '')) + '">';
      } else if (f.kind === 'secret') {
        html += '<input type="password" id="' + id + '" data-ep="' + f.json + '" data-kind="secret" data-gen="' + esc(f.generator || '') + '" placeholder="' + (maskedSet ? esc(t('ep_set')) : '') + '" value="">';
        html += '<span class="muted">' + (maskedSet ? esc(t('ep_set')) : esc(t('ep_unset'))) + '</span>';
        if (f.generator) html += ' <button class="act" data-genbtn="' + id + '">' + esc(t('ep_gen')) + '</button>';
        if (f.hint) html += '<span class="muted">' + esc(f.hint) + '</span>';
      } else {
        html += '<input type="text" id="' + id + '" data-ep="' + f.json + '" data-kind="string" value="' + esc(v != null ? v : (f.default || '')) + '">';
      }
      if (f.required) html += ' <span class="muted">*</span>';
      html += '</div>';
    });
    return html;
  }
  // Wire generate buttons inside a container: crypto.getRandomValues —
  // the random secret is born in the browser.
  function wireGenerators(container) {
    container.querySelectorAll('[data-genbtn]').forEach(function (b) {
      b.onclick = function () {
        var inp = document.getElementById(b.dataset.genbtn);
        if (!inp) return;
        var bytes = new Uint8Array(24);
        crypto.getRandomValues(bytes);
        var val = btoa(String.fromCharCode.apply(null, bytes));
        if (inp.dataset.gen === 'uuid') {
          var h = '';
          bytes.forEach(function (x) { h += ('0' + x.toString(16)).slice(-2); });
          val = h.slice(0, 8) + '-' + h.slice(8, 12) + '-' + h.slice(12, 16) + '-' + h.slice(16, 20) + '-' + h.slice(20);
        }
        inp.value = val;
        inp.type = 'text';
      };
    });
  }
  // Collect the form into engine_params JSON; masked (empty) secrets are
  // OMITTED so the server merge keeps the stored values.
  function collectParams(key) {
    var out = {};
    document.querySelectorAll('[data-ep][id^="ep-' + key + '-"]').forEach(function (inp) {
      var v = inp.value.trim();
      if (inp.dataset.kind === 'number') {
        if (v !== '') out[inp.dataset.ep] = parseInt(v, 10);
      } else if (inp.dataset.kind === 'secret') {
        if (v !== '') out[inp.dataset.ep] = v;
      } else if (v !== '') {
        out[inp.dataset.ep] = v;
      }
    });
    return JSON.stringify(out);
  }

  // Step 4: for each (iran × foreign) pair choose K and the engine per slot.
  function step4() {
    var el = document.getElementById('step-body');
    var iran = collectIran();
    var html = '<div class="card"><p class="muted">' + esc(t('k_hint')) + '</p>';
    iran.forEach(function (ir) {
      S.foreign.forEach(function (fr) {
        var key = ir.id + '-' + fr.id;
        html += '<div class="card"><b>' + esc(ir.name) + ' ↔ ' + esc(fr.name) + '</b>' +
          '<div class="row"><label>' + esc(t('per_pair')) + ': ' +
          '<input type="number" min="1" max="26" value="' + (S.pairs[key] ? S.pairs[key].k : 1) +
          '" data-k="' + key + '" style="width:70px"></label></div><div data-engines="' + key + '"></div></div>';
      });
    });
    html += '</div>';
    el.innerHTML = html;
    templates().then(function () {
      return fetch(base() + 'api/mesh/engines').then(function (r) { return r.json(); });
    }).then(function (d) {
      var have = {};
      (d.artifacts || []).forEach(function (a) { have[a.engine + '|' + a.goarch] = true; });
      iran.forEach(function (ir) {
        S.foreign.forEach(function (fr) {
          var key = ir.id + '-' + fr.id;
          var box = document.querySelector('[data-engines="' + key + '"]');
          if (!box) return;
          // Agent arch: prefer the iran agent's arch (the carrying end).
          var ag = (d.agents || []).filter(function (a) { return a.id === ir.agentId || a.name === ir.name; })[0];
          var arch = (ag && ag.arch) || 'amd64';
          var slot = S.pairs[key] || { k: 1, engines: ['frp'] };
          var opts = '';
          (d.artifacts || []).forEach(function (a) {
            if (a.goarch !== arch) return;
            var ok = have[a.engine + '|' + arch];
            opts += '<option value="' + esc(a.engine) + '"' + (slot.engines[0] === a.engine ? ' selected' : '') + '>' +
              esc(a.engine) + ' ' + esc(a.version) + (ok ? '' : ' ⚠') + '</option>';
          });
          var inner = '';
          for (var i = 0; i < slot.k; i++) {
            var eng = slot.engines[i] || (slot.engines[0] || 'frp');
            inner += '<div class="row"><span class="muted">#' + (i + 1) + '</span><select data-slot="' + key + '" data-slotidx="' + i + '" data-eng="' + esc(eng) + '">' + opts + '</select></div>';
            inner += '<div data-params="' + key + '-' + i + '">' + paramFormHTML(eng, key + '-' + i, S.params[key + '-' + i]) + '</div>';
          }
          box.innerHTML = inner;
          box.querySelectorAll('[data-params]').forEach(function (pc) { wireGenerators(pc); });
        });
      });
      el.querySelectorAll('[data-k]').forEach(function (inp) {
        inp.onchange = function () {
          var key = inp.dataset.k;
          var k = Math.max(1, Math.min(26, parseInt(inp.value, 10) || 1));
          var cur = S.pairs[key] || { engines: ['frp'] };
          var engines = [];
          for (var i = 0; i < k; i++) engines.push(cur.engines[i] || (cur.engines[0] || 'frp'));
          S.pairs[key] = { k: k, engines: engines };
          inp.value = k;
          step4(); // re-render slots
        };
      });
      el.querySelectorAll('[data-slot]').forEach(function (sel) {
        sel.onchange = function () {
          var key = sel.dataset.slot;
          var idx = parseInt(sel.dataset.slotidx, 10);
          var cur = S.pairs[key] || { k: 1, engines: [] };
          cur.engines[idx] = sel.value;
          S.pairs[key] = cur;
          // Re-render the params form for this slot with the new engine.
          var pc = document.querySelector('[data-params="' + key + '-' + idx + '"]');
          if (pc) {
            pc.innerHTML = paramFormHTML(sel.value, key + '-' + idx, S.params[key + '-' + idx]);
            wireGenerators(pc);
          }
        };
      });
      el.querySelectorAll('[data-ep]').forEach(function (inp) {
        inp.onchange = function () {
          var m = inp.id.match(/^ep-(.+-\d+)-/);
          if (m) S.params[m[1]] = collectParams(m[1]);
        };
      });
    }).catch(function (e) { say(e.message, true); });
  }

  // Step 5: weight slider per foreign server (pair-level policy), with the
  // resolved preview (topology default 1 overridden per pair).
  function step5() {
    var el = document.getElementById('step-body');
    var iran = collectIran();
    var html = '<div class="card">';
    S.foreign.forEach(function (fr) {
      S.weights[fr.id] = S.weights[fr.id] || 1;
      html += '<div class="row"><b>' + esc(fr.name) + '</b>' +
        '<input type="range" class="slider" min="1" max="10" value="' + S.weights[fr.id] + '" data-w="' + fr.id + '">' +
        '<span class="pill ok" data-wval="' + fr.id + '">' + esc(t('weight')) + ' ' + S.weights[fr.id] + ' · ' +
        esc(t('resolved')) + ' ' + S.weights[fr.id] + '</span></div>';
    });
    html += '</div>';
    el.innerHTML = html;
    el.querySelectorAll('[data-w]').forEach(function (sl) {
      sl.oninput = function () {
        var id = sl.dataset.w;
        S.weights[id] = parseInt(sl.value, 10);
        document.querySelector('[data-wval="' + id + '"]').textContent =
          t('weight') + ' ' + S.weights[id] + ' · ' + t('resolved') + ' ' + S.weights[id];
      };
    });
  }

  // Step 6: forward rules.
  function step6() {
    var el = document.getElementById('step-body');
    var iran = collectIran();
    var opts = iran.map(function (ir) {
      return '<option value="' + ir.id + '">' + esc(ir.name) + '</option>';
    }).join('');
    el.innerHTML = '<div class="card"><div class="row">' +
      '<select id="fwd-srv">' + opts + '</select>' +
      '<input id="fwd-port" type="number" min="1" max="65535" placeholder="' + esc(t('forward_port')) + '" style="width:120px">' +
      '<select id="fwd-proto"><option value="tcp">tcp</option><option value="udp">udp</option></select>' +
      '<input id="fwd-target" placeholder="' + esc(t('target')) + ' (127.0.0.1:443)">' +
      '<button class="act primary" id="add-fwd">' + esc(t('add_fwd')) + '</button></div>' +
      '<table id="fwd-table"><tr><th>' + esc(t('forward_port')) + '</th><th>' + esc(t('proto')) + '</th><th>' + esc(t('target')) + '</th></tr></table></div>';
    var render = function () {
      var rows = S.forwards.map(function (f) {
        var iran = collectIran().filter(function (x) { return x.id === f.local; })[0];
        return '<tr><td>' + f.port + '</td><td>' + f.proto + '</td><td>' + f.target + '</td><td class="muted">' + esc(iran ? iran.name : '') + '</td></tr>';
      }).join('');
      document.getElementById('fwd-table').insertAdjacentHTML('beforeend', rows);
    };
    document.getElementById('add-fwd').onclick = function () {
      var port = parseInt(document.getElementById('fwd-port').value, 10);
      var target = document.getElementById('fwd-target').value.trim();
      if (!port || port < 1 || port > 65535 || !target) { say(t('forward_port') + ' / ' + t('target'), true); return; }
      S.forwards.push({ local: parseInt(document.getElementById('fwd-srv').value, 10),
                        port: port, proto: document.getElementById('fwd-proto').value, target: target });
      render();
      say('');
    };
  }

  // Step 7: review + create. Create = generate → weights → forwards → status.
  function step7() {
    var el = document.getElementById('step-body');
    var iran = collectIran();
    var pairs = 0, tunnels = 0;
    var plan = '';
    iran.forEach(function (ir) {
      S.foreign.forEach(function (fr) {
        var key = ir.id + '-' + fr.id;
        var p = S.pairs[key];
        if (!p) return;
        pairs++; tunnels += p.k;
        plan += '<tr><td>' + esc(ir.name) + ' ↔ ' + esc(fr.name) + '</td><td>' + p.k + '</td><td>' +
          p.engines.join(', ') + '</td></tr>';
      });
    });
    var fwdRows = S.forwards.map(function (f) {
      var iran = collectIran().filter(function (x) { return x.id === f.local; })[0];
      return '<tr><td>' + esc(iran ? iran.name : '') + '</td><td>' + f.port + '</td><td>' + f.proto + '</td><td>' + esc(f.target) + '</td></tr>';
    }).join('');
    el.innerHTML = '<div class="card"><h3>' + esc(t('review')) + '</h3>' +
      '<p><b>' + iran.length + ' × ' + S.foreign.length + ' × K</b> = <b>' + tunnels + '</b> ' + esc(t('tunnels')) +
      ' (' + pairs + ' ' + esc(t('pairs')) + ')</p>' +
      '<table><tr><th>pair</th><th>K</th><th>engines</th></tr>' + plan + '</table>' +
      (fwdRows ? '<table><tr><th>iran</th><th>port</th><th>proto</th><th>target</th></tr>' + fwdRows + '</table>' : '') +
      '<button class="act primary" id="wiz-create">' + esc(t('create')) + '</button>' +
      '<div id="wiz-tunnels"></div></div>';
    document.getElementById('wiz-create').onclick = function () {
      createAll();
    };
  }

  function collectIran() {
    var out = [];
    S.dcs.forEach(function (d, di) {
      (S['iranDc' + di] || []).forEach(function (s) {
        out.push({ id: s.id, name: s.name, online: s.online, dc: d.name });
      });
    });
    return out;
  }

  function createAll() {
    var iran = collectIran();
    var topoName = 'wizard-' + new Date().toISOString().slice(0, 10) + '-' + Math.floor(Math.random() * 900 + 100);
    var firstTopo = null;
    var chain = Promise.resolve();
    var created = 0;
    // One topology per (iran server, foreign server) pair carries the
    // pair's K tunnels with the chosen engines; weights + forwards apply
    // to the pair's topology after generation.
    iran.forEach(function (ir) {
      S.foreign.forEach(function (fr) {
        var key = ir.id + '-' + fr.id;
        var p = S.pairs[key];
        if (!p) return;
        chain = chain.then(function () {
          var eparams = [];
          for (var i = 0; i < p.k; i++) {
            var pj = S.params[key + '-' + i];
            eparams.push(pj ? JSON.parse(pj) : {});
          }
          return api('POST', 'topologies/generate', {
            name: topoName + '-' + ir.id + '-' + fr.id,
            iran_servers: [ir.id], foreign_servers: [fr.id],
            tunnels_per_pair: p.k, engines: p.engines, engine_params: eparams, enabled: true
          }).then(function (r) {
            created++;
            // The generator answers {topology_id: N, tunnels: [...]}.
            if (!firstTopo) firstTopo = r.body.topology_id;
            var topoId = r.body.topology_id;
            // Pair-level weight (this iran server ↔ this foreign server).
            var w = S.weights[fr.id] || 1;
            return api('POST', 'topologies/' + topoId + '/weights',
                       { local_server_id: ir.id, remote_server_id: fr.id, weight: w })
              .then(function () {
                // Forwards pointing at this iran server land on this topo.
                var myFwd = S.forwards.filter(function (f) { return f.local === ir.id; });
                var chain2 = Promise.resolve();
                myFwd.forEach(function (f) {
                  chain2 = chain2.then(function () {
                    return api('POST', 'forwards', {
                      topology_id: topoId, local_server_id: ir.id,
                      remote_server_id: fr.id, listen_port: f.port,
                      proto: f.proto, target: f.target, enabled: true
                    });
                  });
                });
                return chain2;
              });
          });
        });
      });
    });
    chain.then(function () {
      S.topoId = firstTopo;
      statusPage();
    }).catch(function (e) { say(e.message, true); });
  }

  // Status page: poll overview until acks land / states show up.
  function statusPage() {
    var el = document.getElementById('step-body');
    el.innerHTML = '<div class="card"><b>' + esc(t('created')) + '</b><div id="wiz-tunnels"></div></div>';
    var tick = function () {
      api('GET', 'overview').then(function (d) {
        var rows = (d.tunnels || []).map(function (tn) {
          var st = tn.state || 'unknown';
          var pill = st === 'up' ? 'ok' : (st === 'down' ? 'down' : 'warn');
          return '<tr><td>' + esc(tn.tunnel_key || tn.id) + '</td><td>' + esc(tn.engine || '') + '</td>' +
            '<td><span class="pill ' + pill + '">' + esc(st) + '</span></td></tr>';
        }).join('');
        document.getElementById('wiz-tunnels').innerHTML =
          '<table><tr><th>tunnel</th><th>' + esc(t('engine')) + '</th><th>' + esc(t('state')) + '</th></tr>' + rows + '</table>';
      }).catch(function (e) { say(e.message, true); });
    };
    tick();
    var iv = setInterval(tick, 3000);
    setTimeout(function () { clearInterval(iv); }, 120000);
  }

  // ---- navigation ---------------------------------------------------------
  function render() {
    renderSteps();
    if (S.step === 1) step1();
    if (S.step === 2) stepServers('iran');
    if (S.step === 3) stepServers('foreign');
    if (S.step === 4) step4();
    if (S.step === 5) step5();
    if (S.step === 6) step6();
    if (S.step === 7) step7();
    document.getElementById('back').disabled = S.step === 1;
  }
  document.getElementById('next').onclick = function () { if (S.step < 7) { S.step++; render(); } };
  document.getElementById('back').onclick = function () { if (S.step > 1) { S.step--; render(); } };
  document.getElementById('lang').onclick = function () {
    LANG = LANG === 'en' ? 'fa' : 'en';
    localStorage.setItem('wiz_lang', LANG);
    document.documentElement.lang = LANG;
    document.documentElement.dir = LANG === 'fa' ? 'rtl' : 'ltr';
    document.getElementById('title').textContent = t('title');
    render();
  };
  document.documentElement.lang = LANG;
  document.documentElement.dir = LANG === 'fa' ? 'rtl' : 'ltr';
  document.getElementById('title').textContent = t('title');
  render();
})();
