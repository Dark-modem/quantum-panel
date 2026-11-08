// mesh.js — the mesh-first management UI (Phase 10 redesign). Persian-first
// (RTL default), Jalali calendar with Asia/Tehran, light/dark from the shared
// tokens, no build step and no external assets: everything is fetched from
// /api/mesh/*. The page lives at <base>/mesh and reuses the panel session
// cookie; mutations need the CSRF token (double-submit cookie, meshCSRF()).
(function () {
  'use strict';

  // ---- preferences (language, calendar, theme) ---------------------------
  var LANG = localStorage.getItem('mesh_lang') || 'fa';    // Persian first
  var CAL = localStorage.getItem('mesh_cal') || 'jalali';  // jalali | gregorian
  var T = {
    en: {
      title_dash: 'Dashboard', title_topo: 'Topology', title_servers: 'Servers & Agents',
      title_tunnels: 'Tunnels & Weights', title_traffic: 'Traffic & Billing',
      title_engines: 'Engines', title_alerts: 'Alerts', title_security: 'Security (profiles)',
      title_pg: 'PasarGuard', title_settings: 'Backup & Settings',
      nav_dash: 'Dashboard', nav_topo: 'Topology', nav_servers: 'Servers & Agents',
      nav_tunnels: 'Tunnels & Weights', nav_traffic: 'Traffic & Billing',
      nav_engines: 'Engines', nav_alerts: 'Alerts', nav_security: 'Security (profiles)',
      nav_pg: 'PasarGuard', nav_settings: 'Backup & Settings', nav_legacy: 'Advanced (legacy)',
      ng_mesh: 'Infrastructure', ng_ops: 'Operations',
      theme_lbl: 'Light / dark', lang_lbl: 'فارسی', logout_lbl: 'Log out', refresh_lbl: 'Refresh',
      // dashboard
      k_agents: 'Agents online', k_tunnels_up: 'Tunnels up', k_tunnels_down: 'Tunnels down',
      k_tunnels_unknown: 'Tunnels unknown', k_alerts: 'Firing alerts', k_traffic: 'Wire traffic today',
      k_billable: 'Billable download today', k_cert: 'Certificate',
      k_per_dc: 'per Iran DC', cert_days: '{d} days left', cert_none: 'no near expiry',
      checklist_title: 'First-run checklist', checklist_sub: 'Complete these steps to go live.',
      step1: 'Add an Iran datacenter', step2: 'Add servers & connect agents',
      step3: 'Generate a topology', step4: 'Set tunnel weights', step5: 'Connect PasarGuard (billing)',
      step6: 'Enable alerts', step7: 'Download a backup',
      done: 'done', todo: 'open',
      // topology / map
      topo: 'Topology', members: 'Members', tunnels: 'Tunnels', enabled: 'Enabled', tunnel: 'Tunnel',
      engine: 'Engine', state: 'State', up: 'up', down: 'down', unknown: 'unknown',
      map_title: 'Iran datacenters → foreign servers', map_hint: 'Line thickness = weight share · color = health · click a line for its tunnels',
      map_empty: 'Add an Iran datacenter, servers and a topology to see the map.',
      pair_tunnels: 'Tunnels of this pair', latency: 'latency', wire: 'Wire', payload: 'Payload',
      add_dc: 'Add datacenter', dc_name: 'Datacenter name', dc_kind: 'Kind', dc_price: 'Price coefficient',
      kind_iran: 'Iran', kind_foreign: 'Foreign', add: 'Add', del: 'Delete', confirm_del: 'Delete {n}? This cannot be undone.',
      deleted: 'Deleted', saved: 'Saved',
      wizard_link: 'Open the setup wizard →',
      // servers & agents
      servers: 'Servers', agents: 'Agents', dcs: 'Datacenters',
      srv_name: 'Server name', srv_role: 'Role', srv_dc: 'Datacenter', srv_addr: 'Address',
      agent_col: 'Agent', status_col: 'Status', ver_col: 'Version', seen_col: 'Last seen',
      link_col: 'Bound server', link: 'Bind', unbind: '—', tokens: 'Enrollment tokens',
      tok_role: 'Role', tok_label: 'Label', tok_days: 'Valid (days)', tok_create: 'Mint token',
      tok_copy: 'Copy this token now — it is shown once:', tok_none: 'No open tokens.',
      tok_revoked: 'Token revoked', agent_revoked: 'revoke', revoked: 'revoked',
      ip_override: 'Public IP override', ip_save: 'Save IP', ip_hint: 'empty = auto-detect',
      // tunnels & weights
      weight: 'Weight', percent: 'Share', sum: 'Sum', save: 'Save', save_all: 'Save all changed',
      resolved: 'resolved from', wsum_ok: '= 100%', wsum_bad: '≠ 100% — agents enforce the raw weights, the share is informational',
      enforced: 'What agents enforce', applied_rev: 'applied revision', not_pushed: 'not pushed yet',
      search: 'Search…', no_tunnels: 'No tunnels in this topology yet — use the wizard.',
      excluded: '0 = excluded from balancing', resolution: 'resolution: tunnel > server pair > topology',
      // traffic
      usage: 'Usage (real bytes)', overhead: 'Engine overhead', coefficient: 'Suggested coefficient',
      dc: 'Datacenter', price: 'Price ratio', health: 'Tunnel health', from: 'last 24h',
      // engines
      eng_artifacts: 'Signed engine artifacts', eng_agents: 'Agents: reported engines',
      eng_upload: 'Upload artifact', eng_version: 'Version', eng_arch: 'Arch', eng_goos: 'OS',
      eng_name: 'Download name (optional)', eng_sig: 'Detached signature (base64 ed25519)',
      eng_key_snippet: 'Agent build snippet (pin this public key)', eng_no_key: 'No engine-artifact public key configured',
      eng_none: 'No artifacts uploaded yet.', eng_sha: 'SHA-256', eng_size: 'Size', eng_uploaded: 'Uploaded',
      eng_del: 'Remove', eng_warning: 'Coverage warning', eng_agent_col: 'Agent', eng_arch_col: 'Arch',
      eng_engines_col: 'Engines', eng_uploading: 'Uploading…', eng_uploaded_ok: 'Artifact stored',
      eng_matrix_note: 'Matrix is the default and recommended engine. Other engines are experimental: they work, but their parameter coverage, overhead behavior and failure modes are less battle-tested.',
      eng_default: 'default / recommended', eng_experimental: 'experimental',
      // alerts
      al_rules: 'Alert rules', al_open: 'Open alerts', al_history: 'Recent notifications',
      al_none: 'No alerts — all quiet.', al_enable: 'Alerts enabled', al_offline: 'Agent offline after (min)',
      al_cert: 'Cert expiry warn (days)', al_disk: 'Disk max (%)', al_tg: 'Telegram bot token',
      al_chat: 'Telegram chat id', al_hook: 'Webhook URL (http(s))', al_save: 'Save settings',
      al_test: 'Send test notification', al_test_ok: 'Test notification sent to', al_notif: 'notifier(s)',
      al_no_conf: 'No notifier configured — fill Telegram or webhook first.',
      // security
      apply_h: 'Apply hardening profile', apply_n: 'Apply netopt profile',
      confirm: 'Confirm (keep)', rollback: 'Rollback now',
      confirm_banner: 'Safe-apply armed on {agent} — confirm within {s}s or the agent rolls back.',
      no_pending: 'No pending profile confirmations.',
      need_json: 'Profile JSON is invalid', pushed: 'Pushed', skipped: 'Skipped',
      prof_hint: 'The JSON below is the profile the buttons apply to the selected agent. Applying arms the agent automatic 120 s rollback; confirm in the yellow banner to keep it.',
      // pasarguard
      pg_connect: 'Connect', pg_url: 'Panel URL', pg_user: 'Admin user', pg_pass: 'Admin password',
      pg_dry: 'Dry-run diff', pg_apply: 'Apply plan', pg_cert: 'Store certificate', pg_install: 'Install command',
      pg_crosscheck: 'Cross-check usage',
      // settings
      backup_btn: 'Download encrypted backup', backup_ask_pw: 'Panel password (re-auth):',
      backup_ask_pass: 'Backup passphrase (min 12 chars) — STORE IT SAFELY, without it the backup is unreadable:',
      backup_ok: 'Backup downloaded (encrypted).', backup_denied: 'Backup denied: wrong panel password.',
      prefs: 'Preferences', calendar: 'Calendar', cal_jalali: 'Jalali (Persian)', cal_greg: 'Gregorian',
      lang_pref: 'Language', panel_ver: 'Panel version', legacy_note: 'Legacy host-operator pages',
      legacy_open: 'Open legacy pages', legacy_gated: 'disabled (enable legacy_enabled in panel.json)',
      reload_note: 'Reloads the page.',
      pushed_ok: 'Pushed to agent', push_btn: 'Push desired state',
      // errors / misc
      load_fail: 'Failed to load: ', offline_hint: 'Is the panel reachable? Session expired? Log in again.',
      login_redirect: 'Go to login',
      ok: 'OK', cancel: 'Cancel'
    },
    fa: {
      title_dash: 'داشبورد', title_topo: 'توپولوژی', title_servers: 'سرورها و ایجنت‌ها',
      title_tunnels: 'تونل‌ها و وزن‌ها', title_traffic: 'ترافیک و صورت‌حساب',
      title_engines: 'موتورها', title_alerts: 'هشدارها', title_security: 'امنیت (پروفایل‌ها)',
      title_pg: 'پاسارگارد', title_settings: 'پشتیبان‌گیری و تنظیمات',
      nav_dash: 'داشبورد', nav_topo: 'توپولوژی', nav_servers: 'سرورها و ایجنت‌ها',
      nav_tunnels: 'تونل‌ها و وزن‌ها', nav_traffic: 'ترافیک و صورت‌حساب',
      nav_engines: 'موتورها', nav_alerts: 'هشدارها', nav_security: 'امنیت (پروفایل‌ها)',
      nav_pg: 'پاسارگارد', nav_settings: 'پشتیبان‌گیری و تنظیمات', nav_legacy: 'پیشرفته (نسخه قدیمی)',
      ng_mesh: 'زیرساخت', ng_ops: 'عملیات',
      theme_lbl: 'روشن / تاریک', lang_lbl: 'English', logout_lbl: 'خروج', refresh_lbl: 'به‌روزرسانی',
      k_agents: 'ایجنت‌های برخط', k_tunnels_up: 'تونل‌های برقرار', k_tunnels_down: 'تونل‌های قطع',
      k_tunnels_unknown: 'وضعیت نامشخص', k_alerts: 'هشدارهای فعال', k_traffic: 'ترافیک سیم امروز',
      k_billable: 'دانلود قابل‌صورت‌حساب امروز', k_cert: 'گواهی TLS',
      k_per_dc: 'به‌تفکیک دیتاسنتر ایران', cert_days: '{d} روز مانده', cert_none: 'انقضای نزدیک ندارد',
      checklist_title: 'چک‌لیست راه‌اندازی', checklist_sub: 'این مرحله‌ها را کامل کنید تا سرویس بالا بیاید.',
      step1: 'افزودن دیتاسنتر ایران', step2: 'افزودن سرورها و اتصال ایجنت‌ها',
      step3: 'ساخت توپولوژی', step4: 'تنظیم وزن تونل‌ها', step5: 'اتصال پاسارگارد (صورت‌حساب)',
      step6: 'فعال‌سازی هشدارها', step7: 'دانلود پشتیبان',
      done: 'انجام شد', todo: 'باز است',
      topo: 'توپولوژی', members: 'اعضا', tunnels: 'تونل‌ها', enabled: 'فعال', tunnel: 'تونل',
      engine: 'موتور', state: 'وضعیت', up: 'برقرار', down: 'قطع', unknown: 'نامشخص',
      map_title: 'دیتاسنترهای ایران → سرورهای خارج', map_hint: 'ضخامت خط = سهم وزن · رنگ = سلامت · کلیک روی خط = فهرست تونل‌ها',
      map_empty: 'برای دیدن نقشه، دیتاسنتر ایران، سرورها و توپولوژی را بسازید.',
      pair_tunnels: 'تونل‌های این جفت', latency: 'تأخیر', wire: 'سیم', payload: 'بار',
      add_dc: 'افزودن دیتاسنتر', dc_name: 'نام دیتاسنتر', dc_kind: 'نوع', dc_price: 'ضریب قیمت',
      kind_iran: 'ایران', kind_foreign: 'خارج', add: 'افزودن', del: 'حذف', confirm_del: '«{n}» حذف شود؟ بازگشت ندارد.',
      deleted: 'حذف شد', saved: 'ذخیره شد',
      wizard_link: 'باز کردن Wizard راه‌اندازی →',
      servers: 'سرورها', agents: 'ایجنت‌ها', dcs: 'دیتاسنترها',
      srv_name: 'نام سرور', srv_role: 'نقش', srv_dc: 'دیتاسنتر', srv_addr: 'آدرس',
      agent_col: 'ایجنت', status_col: 'وضعیت', ver_col: 'نسخه', seen_col: 'آخرین حضور',
      link_col: 'سرور متصل', link: 'اتصال', unbind: '—', tokens: 'توکن‌های ثبت ایجنت',
      tok_role: 'نقش', tok_label: 'برچسب', tok_days: 'اعتبار (روز)', tok_create: 'ساخت توکن',
      tok_copy: 'این توکن را همین حالا کپی کنید — فقط یک‌بار نشان داده می‌شود:', tok_none: 'توکن بازی باز نیست.',
      tok_revoked: 'توکن باطل شد', agent_revoked: 'باطل‌کردن', revoked: 'باطل‌شده',
      ip_override: 'IP عمومی دستی', ip_save: 'ذخیره IP', ip_hint: 'خالی = تشخیص خودکار',
      weight: 'وزن', percent: 'سهم', sum: 'مجموع', save: 'ذخیره', save_all: 'ذخیره همه تغییرات',
      resolved: 'منبع', wsum_ok: '= ۱۰۰٪', wsum_bad: '≠ ۱۰۰٪ — ایجنت‌ها وزن خام را اعمال می‌کنند؛ درصد فقط اطلاع‌رسانی است',
      enforced: 'آنچه ایجنت‌ها اعمال می‌کنند', applied_rev: 'revision اعمال‌شده', not_pushed: 'هنوز push نشده',
      search: 'جستجو…', no_tunnels: 'در این توپولوژی تونلی نیست — از Wizard استفاده کنید.',
      excluded: '۰ = خارج از چرخش', resolution: 'ترتیب حل: تونل ← جفت‌سرور ← توپولوژی',
      usage: 'مصرف (بایت واقعی)', overhead: 'سربار موتور', coefficient: 'ضریب پیشنهادی',
      dc: 'دیتاسنتر', price: 'نرخ', health: 'سلامت تونل‌ها', from: '۲۴ ساعت گذشته',
      eng_artifacts: 'موتورهای امضاشده', eng_agents: 'ایجنت‌ها: موتورهای اعلام‌شده',
      eng_upload: 'بارگذاری فایل', eng_version: 'نسخه', eng_arch: 'معماری', eng_goos: 'سیستم‌عامل',
      eng_name: 'نام دانلود (اختیاری)', eng_sig: 'امضای جداشده (base64 ed25519)',
      eng_key_snippet: 'قطعه کد ساخت عامل (این کلید عمومی را پین کنید)', eng_no_key: 'هیچ کلید عمومی موتور تنظیم نشده',
      eng_none: 'هنوز فایلی بارگذاری نشده.', eng_sha: 'SHA-256', eng_size: 'حجم', eng_uploaded: 'بارگذاری',
      eng_del: 'حذف', eng_warning: 'هشدار پوشش', eng_agent_col: 'ایجنت', eng_arch_col: 'معماری',
      eng_engines_col: 'موتورها', eng_uploading: 'در حال بارگذاری…', eng_uploaded_ok: 'فایل ذخیره شد',
      eng_matrix_note: 'Matrix موتور پیش‌فرض و توصیه‌شده است. سایر موتورها آزمایشی هستند: کار می‌کنند اما پوشش پارامترها، رفتار سربار و حالت‌های خرابی آن‌ها کمتر آزموده شده است.',
      eng_default: 'پیش‌فرض / توصیه‌شده', eng_experimental: 'آزمایشی',
      al_rules: 'قواعد هشدار', al_open: 'هشدارهای باز', al_history: 'اعلان‌های اخیر',
      al_none: 'هیچ هشداری نیست — همه چیز آرام است.', al_enable: 'هشدارها فعال', al_offline: 'ایجنت آفلاین بعد از (دقیقه)',
      al_cert: 'هشدار انقضای گواهی (روز)', al_disk: 'حداکثر دیسک (%)', al_tg: 'توکن ربات تلگرام',
      al_chat: 'شناسه چت تلگرام', al_hook: 'آدرس وب‌هوک (http(s))', al_save: 'ذخیره تنظیمات',
      al_test: 'ارسال اعلان آزمایشی', al_test_ok: 'اعلان آزمایشی به', al_notif: 'مجری ارسال شد',
      al_no_conf: 'هیچ مجری ارسالی تنظیم نشده — ابتدا تلگرام یا وب‌هوک را پر کنید.',
      apply_h: 'اعمال پروفایل hardening', apply_n: 'اعمال پروفایل netopt',
      confirm: 'تأیید (حفظ تغییرات)', rollback: 'بازگردانی فوری',
      confirm_banner: 'حالت apply ایمن روی {agent} فعال شد — تا {s} ثانیه تأیید کنید، وگرنه عامل خودش بازمی‌گرداند.',
      no_pending: 'هیچ پروفایلی در انتظار تأیید نیست.',
      need_json: 'JSON پروفایل نامعتبر است', pushed: 'ارسال شد', skipped: 'رد شد',
      prof_hint: 'JSON زیر، پروفایلی است که دکمه‌ها روی ایجنت انتخاب‌شده اعمال می‌کنند. اعمال، تایمر ۱۲۰ ثانیه‌ای بازگردانی را فعال می‌کند؛ برای ماندگاری از نوار زرد تأیید بگیرید.',
      pg_connect: 'اتصال', pg_url: 'آدرس پنل', pg_user: 'کاربر ادمین', pg_pass: 'رمز ادمین',
      pg_dry: 'بررسی تغییرات', pg_apply: 'اعمال برنامه', pg_cert: 'ذخیره گواهی', pg_install: 'دستور نصب',
      pg_crosscheck: 'مقایسه مصرف',
      backup_btn: 'دانلود پشتیبان رمزنگاری‌شده', backup_ask_pw: 'رمز پنل (تأیید دوباره):',
      backup_ask_pass: 'گذرواژهٔ پشتیبان (حداقل ۱۲ نویسه) — آن را ایمن نگه دارید؛ بدون آن پشتیبان خواندنی نیست:',
      backup_ok: 'پشتیبان رمزنگاری‌شده دانلود شد.', backup_denied: 'پشتیبان رد شد: رمز پنل نادرست است.',
      prefs: 'تنظیمات', calendar: 'تقویم', cal_jalali: 'جلالی (شمسی)', cal_greg: 'میلادی',
      lang_pref: 'زبان', panel_ver: 'نسخه پنل', legacy_note: 'صفحات قدیمیِ مدیریتیِ هاست',
      legacy_open: 'باز کردن صفحات قدیمی', legacy_gated: 'غیرفعال (با legacy_enabled در panel.json فعال می‌شود)',
      reload_note: 'صفحه دوباره بارگذاری می‌شود.',
      pushed_ok: 'برای ایجنت ارسال شد', push_btn: 'ارسال وضعیت مطلوب (push)',
      load_fail: 'بارگذاری ناموفق: ', offline_hint: 'پنل در دسترس است؟ نشست منقضی شده؟ دوباره وارد شوید.',
      login_redirect: 'رفتن به صفحه ورود',
      ok: 'تأیید', cancel: 'انصراف'
    }
  };
  function t(k) { return (T[LANG] && T[LANG][k]) || T.en[k] || k; }

  // ---- API helpers --------------------------------------------------------
  function base() {
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
      if (r.status === 401) { location.href = base(); throw new Error(t('login_redirect')); }
      return r.json().then(function (j) {
        if (!r.ok) throw new Error(j.error || ('HTTP ' + r.status));
        return j;
      });
    });
  }
  // apiRaw talks to the non-mesh panel APIs (agent token registry, logout…).
  function apiRaw(method, path, body) {
    var opt = { method: method, headers: { 'X-CSRF-Token': csrfToken() } };
    if (body !== undefined) {
      opt.headers['Content-Type'] = 'application/json';
      opt.body = JSON.stringify(body);
    }
    return fetch(base() + path, opt).then(function (r) {
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
  function bytes(n) {
    if (n == null) return '—';
    var u = ['B', 'KB', 'MB', 'GB', 'TB'], i = 0, v = Number(n);
    while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
    return v.toFixed(v >= 100 || i === 0 ? 0 : 1) + ' ' + u[i];
  }

  // ---- Jalali/Gregorian date formatting (Intl — no library) ----------------
  function fmtDate(tsSec) {
    if (!tsSec) return '—';
    var d = new Date(tsSec * 1000);
    try {
      if (CAL === 'gregorian') {
        return d.toLocaleString(LANG === 'fa' ? 'fa-IR-u-ca-gregory' : 'en-US',
          { timeZone: 'Asia/Tehran', dateStyle: 'medium', timeStyle: 'short' });
      }
      return d.toLocaleString(LANG === 'fa' ? 'fa-IR' : 'en-US-u-ca-persian-nu-latn',
        { timeZone: 'Asia/Tehran', dateStyle: 'medium', timeStyle: 'short' });
    } catch (e) { return d.toISOString(); }
  }

  // ---- toasts (result feedback) + confirm modal ----------------------------
  function toast(msg, kind) {
    var box = document.getElementById('toasts');
    var el = document.createElement('div');
    el.className = 'toast ' + (kind || '');
    el.textContent = msg;
    box.appendChild(el);
    setTimeout(function () { el.remove(); }, 4200);
  }
  function confirmModal(title, body, okLabel, danger) {
    return new Promise(function (resolve) {
      var root = document.getElementById('modal-root');
      root.innerHTML = '<div class="modal-scrim"><div class="modal" role="dialog" aria-modal="true" aria-label="' + esc(title) + '">' +
        '<h3>' + esc(title) + '</h3><p>' + esc(body) + '</p>' +
        '<div class="mfoot"><button class="act" id="m-cancel">' + esc(t('cancel')) + '</button>' +
        '<button class="act ' + (danger ? 'danger' : 'primary') + '" id="m-ok">' + esc(okLabel || t('ok')) + '</button></div></div></div>';
      function close(v) { root.innerHTML = ''; resolve(v); }
      root.querySelector('#m-cancel').onclick = function () { close(false); };
      root.querySelector('#m-ok').onclick = function () { close(true); };
      root.querySelector('.modal-scrim').addEventListener('click', function (e) {
        if (e.target.classList.contains('modal-scrim')) close(false);
      });
      root.querySelector('#m-ok').focus();
    });
  }

  // ---- theme (shared with the legacy app via gre_theme) ---------------------
  function applyTheme() {
    var th = localStorage.getItem('gre_theme') || 'dark';
    document.documentElement.setAttribute('data-t', th);
    document.body.setAttribute('data-t', th);
    var m = document.querySelector('meta[name=theme-color]');
    if (m) m.setAttribute('content', th === 'light' ? '#f2f2f2' : '#09090b');
  }

  // ---- state + navigation ---------------------------------------------------
  var overview = null;   // /overview result
  var agents = null;     // /agents result (with runtime)
  var meta = null;       // /meta result (version, legacy_enabled)
  var page = 'dash';
  var alertCount = 0;

  function runtimeFor(key) {
    if (!agents) return null;
    var found = null;
    (agents.agents || []).forEach(function (a) {
      (a.runtime || []).forEach(function (rt) { if (rt.tunnel_id === key) found = rt; });
    });
    return found;
  }
  function agentName(id) {
    if (!agents) return id;
    var hit = (agents.agents || []).filter(function (a) { return a.id === id; })[0];
    return (hit && hit.name) || id;
  }

  var PAGES = ['dash', 'topo', 'servers', 'tunnels', 'traffic', 'engines', 'alerts', 'security', 'pasarguard', 'settings'];
  function setPage(p) {
    page = p;
    PAGES.forEach(function (id) {
      var sec = document.getElementById('page-' + id);
      if (sec) sec.hidden = id !== p;
    });
    Array.prototype.forEach.call(document.querySelectorAll('#nav button'), function (b) {
      b.classList.toggle('active', b.dataset.page === p);
    });
    var title = document.getElementById('title');
    title.textContent = t('title_' + p);
    document.title = 'Quantum — ' + t('title_' + p);
    closeSidebar();
    render();
  }
  function openSidebar() {
    document.getElementById('side').classList.add('open');
    document.getElementById('scrim').hidden = false;
  }
  function closeSidebar() {
    document.getElementById('side').classList.remove('open');
    document.getElementById('scrim').hidden = true;
  }
  function render() {
    if (page === 'dash') renderDash();
    if (page === 'topo') renderTopo();
    if (page === 'servers') renderServers();
    if (page === 'tunnels') renderTunnels();
    if (page === 'traffic') renderTraffic();
    if (page === 'engines') renderEngines();
    if (page === 'alerts') renderAlerts();
    if (page === 'security') renderProfiles();
    if (page === 'pasarguard') renderPG();
    if (page === 'settings') renderSettings();
  }

  function loadAll() {
    return Promise.all([
      api('GET', 'overview'),
      api('GET', 'agents'),
      api('GET', 'alerts')
    ]).then(function (rs) {
      overview = rs[0]; agents = rs[1];
      alertCount = (rs[1] && rs[1].alerts) ? 0 : (rs[2] && (rs[2].open || []).length) || 0;
      var navBadge = document.querySelector('#nav button[data-page="alerts"] .nvbadge');
      if (alertCount > 0) {
        if (!navBadge) {
          navBadge = document.createElement('span');
          navBadge.className = 'nvbadge';
          document.querySelector('#nav button[data-page="alerts"]').appendChild(navBadge);
        }
        navBadge.textContent = alertCount;
      } else if (navBadge) navBadge.remove();
      renderBanner();
      render();
    }).catch(function (e) {
      var ge = document.getElementById('global-err');
      ge.style.display = '';
      ge.innerHTML = '<b>' + esc(t('load_fail') + e.message) + '</b> — ' + esc(t('offline_hint')) +
        ' <a href="' + esc(base()) + '">' + esc(t('login_redirect')) + '</a>';
    });
  }

  // ---- dashboard: one-glance health + first-run checklist --------------------
  function tunnelHealthCounts() {
    var counts = { up: 0, down: 0, unknown: 0, total: 0 };
    (overview ? overview.topologies : []).forEach(function (tp) {
      topoTunnels(tp).forEach(function (tn) {
        counts.total += 1;
        var rt = runtimeFor(tn.tunnel_key);
        if (!rt) counts.unknown += 1;
        else if (rt.up) counts.up += 1;
        else counts.down += 1;
      });
    });
    return counts;
  }

  function renderDash() {
    var el = document.getElementById('page-dash');
    if (!overview) {
      el.innerHTML = '<div class="skeleton" style="width:40%"></div><div class="skeleton"></div><div class="skeleton"></div>';
      return;
    }
    var hc = tunnelHealthCounts();
    var online = (agents ? agents.agents : []).filter(function (a) { return a.status === 'online'; }).length;
    var total = (agents ? agents.agents : []).length;

    // per-Iran-DC wire traffic today (real wire bytes) + billable download.
    var iranDCs = (overview.datacenters || []).filter(function (d) { return d.kind === 'iran'; });
    var dayStart = Math.floor(new Date(new Date().toDateString()).getTime() / 1000);
    var dcCards = iranDCs.map(function (d) {
      return api('GET', 'usage?datacenter_id=' + d.id + '&from=' + dayStart)
        .then(function (r) { return { dc: d, u: r.usage || {} }; })
        .catch(function () { return { dc: d, u: {} }; });
    });
    el.innerHTML =
      '<div class="kpis">' +
      '<button class="kpi ' + (online === total && total > 0 ? 'ok' : (online < total ? 'err' : '')) + '" data-go="servers">' +
      '<div class="kval">' + online + '<small>/' + total + '</small></div><div class="klbl">' + esc(t('k_agents')) + '</div></button>' +
      '<button class="kpi ' + (hc.down > 0 ? 'err' : (hc.up > 0 ? 'ok' : '')) + '" data-go="topo">' +
      '<div class="kval">' + hc.up + '</div><div class="klbl">' + esc(t('k_tunnels_up')) + '</div></button>' +
      '<button class="kpi ' + (hc.down > 0 ? 'err' : '') + '" data-go="topo">' +
      '<div class="kval">' + hc.down + '</div><div class="klbl">' + esc(t('k_tunnels_down')) + '</div></button>' +
      '<button class="kpi' + (hc.unknown ? '' : '') + '" data-go="topo">' +
      '<div class="kval">' + hc.unknown + '</div><div class="klbl">' + esc(t('k_tunnels_unknown')) + '</div></button>' +
      '<button class="kpi ' + (alertCount > 0 ? 'warn' : 'ok') + '" data-go="alerts">' +
      '<div class="kval">' + alertCount + '</div><div class="klbl">' + esc(t('k_alerts')) + '</div></button>' +
      '<div class="kpi" id="k-traffic"><div class="kval" id="k-traffic-val">…</div>' +
      '<div class="klbl">' + esc(t('k_traffic')) + '</div></div>' +
      '<div class="kpi" id="k-bill"><div class="kval" id="k-bill-val">…</div>' +
      '<div class="klbl">' + esc(t('k_billable')) + '</div></div>' +
      '</div>' +
      '<div class="card"><h3>' + esc(t('k_traffic')) + ' — ' + esc(t('k_per_dc')) + '</h3><div id="dc-usage"><div class="skeleton"></div></div></div>' +
      '<div class="card"><h3>' + esc(t('checklist_title')) + '</h3><p class="muted">' + esc(t('checklist_sub')) + '</p>' +
      '<div class="check" id="checklist"></div></div>';
    el.querySelectorAll('[data-go]').forEach(function (b) {
      b.onclick = function () { setPage(b.dataset.go); };
    });

    Promise.all(dcCards).then(function (rows) {
      var wire = 0, bill = 0;
      var html = '<table><tr><th>' + esc(t('dc')) + '</th><th>' + esc(t('wire')) + ' ↓</th><th>' + esc(t('wire')) + ' ↑</th><th>' + esc(t('k_billable')) + ' ↓</th></tr>';
      rows.forEach(function (r) {
        wire += (r.u.wire_rx || 0) + (r.u.wire_tx || 0);
        // billable = payload when the counters are known, else wire (phase-4 rule)
        var b = (r.u.payload_known ? (r.u.payload_rx || 0) : (r.u.wire_rx || 0));
        bill += b;
        html += '<tr><td>' + esc(r.dc.name) + '</td><td>' + bytes(r.u.wire_rx || 0) + '</td><td>' +
          bytes(r.u.wire_tx || 0) + '</td><td>' + bytes(b) + '</td></tr>';
      });
      html += '</table>';
      document.getElementById('dc-usage').innerHTML = html;
      var kv = document.querySelector('#k-traffic-val');
      var bv = document.querySelector('#k-bill-val');
      if (kv) kv.textContent = bytes(wire);
      if (bv) bv.textContent = bytes(bill);
    });

    renderChecklist(el);
  }

  function renderChecklist(el) {
    var box = el.querySelector('#checklist');
    if (!box) return;
    var dcs = (overview ? overview.datacenters : []);
    var servers = (overview ? overview.servers : []);
    var ags = (agents ? agents.agents : []);
    var topos = (overview ? overview.topologies : []);
    var iranDC = dcs.some(function (d) { return d.kind === 'iran'; });
    var linkedAgent = ags.some(function (a) { return a.server_id > 0 && a.status === 'online'; });
    var hasTopo = topos.some(function (tp) { return (tp.tunnel_count || 0) > 0; });
    Promise.all(topos.map(function (tp) {
      return api('GET', 'topologies/' + tp.id + '/weights').then(function (w) {
        return (w.weights || []).some(function (wp) { return wp.weight != null && wp.weight !== 1; });
      }).catch(function () { return false; });
    })).then(function (flags) {
      var hasWeights = flags.some(Boolean);
      api('GET', 'pasarguard/status').then(function (st) {
        return !!st.configured;
      }).catch(function () { return false; }).then(function (pgOK) {
        api('GET', 'alerts').then(function (a) { return !!a.enabled; })
          .catch(function () { return false; }).then(function (alOK) {
            var steps = [
              { ok: iranDC, page: 'servers', label: t('step1') },
              { ok: linkedAgent, page: 'servers', label: t('step2') },
              { ok: hasTopo, page: 'topo', label: t('step3') },
              { ok: hasWeights, page: 'tunnels', label: t('step4') },
              { ok: pgOK, page: 'pasarguard', label: t('step5') },
              { ok: alOK, page: 'alerts', label: t('step6') },
              { ok: null, page: 'settings', label: t('step7') } // manual: cannot be verified
            ];
            box.innerHTML = steps.map(function (s, i) {
              return '<button class="step' + (s.ok ? ' done' : '') + '" data-page="' + s.page + '">' +
                '<span class="st-num">' + (s.ok ? '✓' : (i + 1)) + '</span>' + esc(s.label) +
                '<span class="st-st"><span class="pill ' + (s.ok === true ? 'ok' : (s.ok === false ? 'warn' : 'dim')) + '">' +
                esc(s.ok === true ? t('done') : (s.ok === false ? t('todo') : '—')) + '</span></span></button>';
            }).join('');
            box.querySelectorAll('[data-page]').forEach(function (b) {
              b.onclick = function () { setPage(b.dataset.page); };
            });
          });
      });
    });
  }

  // ---- topology page: inline SVG map + tunnel lists --------------------------
  function topoTunnels(tp) {
    if (tp.__tunnels) return tp.__tunnels;
    api('GET', 'topologies/' + tp.id).then(function (j) {
      tp.__tunnels = j.tunnels || [];
      if (page === 'topo') renderTopo();
      if (page === 'dash') renderDash();
    }).catch(function () { tp.__tunnels = []; });
    return tp.__tunnels || [];
  }

  function topoHealth(tp) {
    var st = { up: 0, down: 0, unknown: 0 };
    topoTunnels(tp).forEach(function (tn) {
      var rt = runtimeFor(tn.tunnel_key);
      if (!rt) st.unknown += 1;
      else if (rt.up) st.up += 1;
      else st.down += 1;
    });
    return st;
  }

  function renderTopo() {
    var el = document.getElementById('page-topo');
    if (!overview) { el.innerHTML = '<div class="skeleton"></div>'; return; }
    el.innerHTML =
      '<div class="card"><h3>' + esc(t('map_title')) + '</h3>' +
      '<p class="muted">' + esc(t('map_hint')) + '</p><div id="mapbox"></div></div>' +
      '<div class="card"><div class="row"><h3 style="margin:0">' + esc(t('topo')) + '</h3><span class="spacer"></span>' +
      '<a class="btn small primary" href="' + esc(base()) + 'mesh-wizard">' + esc(t('wizard_link')) + '</a></div>' +
      '<div id="topolists"></div></div>';
    drawMap();
    renderTopoLists();
  }

  // The map: Iran DC boxes (with their server nodes) on the left, foreign
  // servers on the right, one edge per (local,remote) pair that has tunnels.
  // Pure inline SVG — no libraries. Health color, weight-share thickness.
  function drawMap() {
    var box = document.getElementById('mapbox');
    if (!box) return;
    var dcs = (overview.datacenters || []);
    var servers = (overview.servers || []);
    var topos = (overview.topologies || []);
    var allT = [];
    topos.forEach(function (tp) { topoTunnels(tp).forEach(function (tn) { allT.push({ topo: tp, tn: tn }); }); });
    if (!dcs.length || !allT.length) {
      box.innerHTML = '<div class="empty">' + esc(t('map_empty')) + '</div>';
      return;
    }
    // group iran servers by DC
    var dcMap = {};
    dcs.forEach(function (d) { dcMap[d.id] = d; });
    var iranServers = servers.filter(function (s) { return s.role === 'iran'; });
    var foreign = servers.filter(function (s) { return s.role === 'foreign'; });
    var pairs = {};
    allT.forEach(function (x) {
      var k = x.tn.local_server_id + '>' + x.tn.remote_server_id;
      var p = pairs[k] = pairs[k] || { local: x.tn.local_server_id, remote: x.tn.remote_server_id, n: 0, up: 0, down: 0, unknown: 0, weight: 0, topo: x.topo.id };
      p.n += 1;
      var rt = runtimeFor(x.tn.tunnel_key);
      if (!rt) p.unknown += 1;
      else if (rt.up) { p.up += 1; p.weight += (rt.weight || 1); }
      else p.down += 1;
    });
    // weights for thickness come from the weights API lazily; for now use
    // tunnel counts × resolved default (the weights page shows exact numbers).
    var W = 760, rowH = 34, pad = 16;
    var leftBoxW = 250, rightBoxW = 170;
    var leftH = iranServers.length * rowH + pad * 2 + 22;
    var rightH = Math.max(foreign.length, 1) * rowH + pad * 2 + 22;
    var H = Math.max(leftH, rightH) + 8;
    var srvY = {};
    var y = 6;
    iranServers.forEach(function (s) {
      srvY[s.id] = y + rowH / 2 + 8;
      y += rowH;
    });
    y = 6;
    // one DC box per contiguous run of same-DC servers
    var drawnDCs = {};
    var boxes = [];
    iranServers.forEach(function (s) {
      var dcId = s.datacenter_id || 0;
      if (!drawnDCs[dcId]) { drawnDCs[dcId] = { y0: y, y1: y, count: 0 }; boxes.push(dcId); }
      drawnDCs[dcId].count += 1;
      drawnDCs[dcId].y1 = y + rowH;
      y += rowH;
    });
    var svg = '<svg class="topomap" viewBox="0 0 ' + W + ' ' + H + '" role="img" aria-label="' + esc(t('map_title')) + '">';
    // Iran DC boxes: one per contiguous run of same-DC servers, then nodes.
    boxes.forEach(function (dcId) {
      var b = drawnDCs[dcId];
      var d = dcMap[dcId];
      svg += '<rect class="dcbox" x="4" y="' + (b.y0 - 4) + '" width="' + leftBoxW + '" height="' + (b.y1 - b.y0 + 12) + '" rx="10"/>';
      svg += '<text class="dclbl" x="' + (leftBoxW - 8) + '" y="' + (b.y0 + 10) + '" text-anchor="end">' +
        esc(d ? d.name : ('DC #' + dcId)) + (d && d.price_coefficient ? ' ×' + d.price_coefficient : '') + '</text>';
    });
    iranServers.forEach(function (s) {
      var cy = srvY[s.id];
      svg += '<rect class="svnode" x="14" y="' + (cy - 12) + '" width="' + (leftBoxW - 36) + '" height="24" rx="7"/>' +
        '<text x="24" y="' + (cy + 4) + '">' + esc(s.name) + '</text>';
    });
    // foreign nodes
    var fy = 6 + 22 + pad;
    var fgnY = {};
    foreign.forEach(function (s) {
      fgnY[s.id] = fy + rowH / 2 + 8;
      svg += '<rect class="svnode fgn" x="' + (W - rightBoxW - 14) + '" y="' + (fy + rowH / 2 - 12) + '" width="' + (rightBoxW - 20) + '" height="24" rx="7"/>' +
        '<text x="' + (W - rightBoxW - 4) + '" y="' + (fy + rowH / 2 + 4) + '" text-anchor="end">' + esc(s.name) + '</text>';
      fy += rowH;
    });
    // edges
    var x1 = 14 + leftBoxW - 22, x2 = W - rightBoxW - 14;
    var maxN = 1;
    Object.keys(pairs).forEach(function (k) { if (pairs[k].n > maxN) maxN = pairs[k].n; });
    Object.keys(pairs).forEach(function (k, i) {
      var p = pairs[k];
      var yA = srvY[p.local] || (H / 2);
      var yB = fgnY[p.remote] || (H / 2);
      var cls = 'link';
      if (p.down === p.n) cls += ' down-all';
      else if (p.down > 0 || p.unknown > 0) cls += ' part';
      var wpx = 1.5 + 3.5 * (p.n / maxN);
      var mx = (x1 + x2) / 2 + ((i % 5) - 2) * 14;
      svg += '<path class="' + cls + '" d="M' + x1 + ' ' + yA + ' C ' + mx + ' ' + yA + ', ' + mx + ' ' + yB + ', ' + x2 + ' ' + yB + '" ' +
        'stroke-width="' + wpx.toFixed(1) + '" fill="none" opacity="0.8" data-pair="' + esc(k) + '">' +
        '<title>' + esc(p.n + ' tunnels · ' + p.up + ' up / ' + p.down + ' down / ' + p.unknown + ' unknown') + '</title></path>';
      svg += '<text class="linklbl" x="' + ((x1 + x2) / 2) + '" y="' + ((yA + yB) / 2 - 6) + '" text-anchor="middle">' + p.n + '</text>';
    });
    svg += '</svg>';
    box.innerHTML = svg;
    box.querySelectorAll('[data-pair]').forEach(function (path) {
      path.addEventListener('click', function () { showPairTunnels(path.dataset.pair); });
    });
  }

  function showPairTunnels(pairKey) {
    var parts = pairKey.split('>');
    var li = Number(parts[0]), ri = Number(parts[1]);
    var rows = [];
    (overview.topologies || []).forEach(function (tp) {
      topoTunnels(tp).forEach(function (tn) {
        if (tn.local_server_id === li && tn.remote_server_id === ri) {
          var rt = runtimeFor(tn.tunnel_key);
          rows.push('<tr><td class="mono">' + esc(tn.tunnel_key) + '</td><td>' + esc(tn.engine) + '</td><td>' +
            (rt == null ? '<span class="pill dim">' + esc(t('unknown')) + '</span>'
              : (rt.up ? '<span class="pill ok">' + esc(t('up')) + '</span>' : '<span class="pill down">' + esc(t('down')) + '</span>')) +
            '</td></tr>');
        }
      });
    });
    var root = document.getElementById('modal-root');
    root.innerHTML = '<div class="modal-scrim"><div class="modal" role="dialog" aria-modal="true"><h3>' + esc(t('pair_tunnels')) + '</h3>' +
      '<table><tr><th>' + esc(t('tunnel')) + '</th><th>' + esc(t('engine')) + '</th><th>' + esc(t('state')) + '</th></tr>' + rows.join('') + '</table>' +
      '<div class="mfoot"><button class="act primary" id="m-ok">' + esc(t('ok')) + '</button></div></div></div>';
    root.querySelector('#m-ok').onclick = function () { root.innerHTML = ''; };
    root.querySelector('.modal-scrim').addEventListener('click', function (e) {
      if (e.target.classList.contains('modal-scrim')) root.innerHTML = '';
    });
  }

  function renderTopoLists() {
    var el = document.getElementById('topolists');
    if (!el) return;
    var html = '';
    (overview.topologies || []).forEach(function (tp) {
      var st = topoHealth(tp);
      var tun = topoTunnels(tp);
      html += '<h3 style="margin-top:14px">' + esc(tp.name) +
        ' <span class="pill ' + (st.down > 0 ? 'down' : (st.up > 0 ? 'ok' : 'dim')) + '">' + st.up + '↑ ' + st.down + '↓ ' + st.unknown + '? </span>' +
        (tp.enabled ? '' : ' <span class="pill warn">' + esc(t('enabled')) + ': off</span>') + '</h3>';
      if (tun.length) {
        html += '<table><tr><th>' + esc(t('tunnel')) + '</th><th>' + esc(t('engine')) + '</th><th>' + esc(t('state')) + '</th><th>' + esc(t('latency')) + '</th></tr>';
        tun.forEach(function (tn) {
          var rt = runtimeFor(tn.tunnel_key);
          var pill = rt == null ? '<span class="pill dim">' + esc(t('unknown')) + '</span>'
            : (rt.up ? '<span class="pill ok">' + esc(t('up')) + '</span>' : '<span class="pill down">' + esc(t('down')) + '</span>');
          html += '<tr><td class="mono">' + esc(tn.tunnel_key) + '</td><td>' + esc(tn.engine) + '</td><td>' + pill + '</td><td>' +
            (rt && rt.latency_ms != null ? rt.latency_ms.toFixed(0) + ' ms' : '—') + '</td></tr>';
        });
        html += '</table>';
      }
    });
    el.innerHTML = html || '<div class="empty">' + esc(t('map_empty')) + '</div>';
  }

  // ---- servers & agents page --------------------------------------------------
  function renderServers() {
    var el = document.getElementById('page-servers');
    if (!overview) { el.innerHTML = '<div class="skeleton"></div>'; return; }
    var dcs = (overview.datacenters || []);
    var servers = (overview.servers || []);
    var ags = (agents ? agents.agents : []);
    var dcOpts = function (sel) {
      return dcs.map(function (d) {
        return '<option value="' + d.id + '"' + (sel === d.id ? ' selected' : '') + '>' + esc(d.name) + '</option>';
      }).join('');
    };
    var html = '';
    // datacenters
    html += '<div class="card"><h3>' + esc(t('dcs')) + '</h3><table><tr><th>' + esc(t('dc_name')) + '</th><th>' + esc(t('dc_kind')) + '</th><th>' + esc(t('dc_price')) + '</th><th></th></tr>';
    dcs.forEach(function (d) {
      html += '<tr><td>' + esc(d.name) + '</td><td><span class="pill ' + (d.kind === 'iran' ? 'info' : 'dim') + '">' +
        esc(d.kind === 'iran' ? t('kind_iran') : t('kind_foreign')) + '</span></td><td>×' + esc(d.price_coefficient) + '</td>' +
        '<td><button class="act small danger" data-dcdel="' + d.id + '" data-n="' + esc(d.name) + '">' + esc(t('del')) + '</button></td></tr>';
    });
    html += '</table><div class="row">' +
      '<input id="dc-name" placeholder="' + esc(t('dc_name')) + '" style="width:180px">' +
      '<select id="dc-kind"><option value="iran">' + esc(t('kind_iran')) + '</option><option value="foreign">' + esc(t('kind_foreign')) + '</option></select>' +
      '<input id="dc-price" type="number" step="0.1" min="0.1" value="1.0" style="width:90px" aria-label="' + esc(t('dc_price')) + '">' +
      '<button class="act primary" id="dc-add">' + esc(t('add_dc')) + '</button></div></div>';
    // servers
    html += '<div class="card"><h3>' + esc(t('servers')) + '</h3><table><tr><th>' + esc(t('srv_name')) + '</th><th>' + esc(t('srv_role')) + '</th><th>' + esc(t('srv_dc')) + '</th><th>' + esc(t('srv_addr')) + '</th><th></th></tr>';
    servers.forEach(function (s) {
      var d = dcs.filter(function (x) { return x.id === s.datacenter_id; })[0];
      html += '<tr><td>' + esc(s.name) + '</td><td>' + esc(s.role) + '</td><td>' + esc(d ? d.name : '—') + '</td><td class="mono">' + esc(s.address || '') + '</td>' +
        '<td><button class="act small danger" data-srvdel="' + s.id + '" data-n="' + esc(s.name) + '">' + esc(t('del')) + '</button></td></tr>';
    });
    html += '</table><div class="row">' +
      '<input id="srv-name" placeholder="' + esc(t('srv_name')) + '" style="width:160px">' +
      '<select id="srv-role"><option value="iran">' + esc(t('kind_iran')) + '</option><option value="foreign">' + esc(t('kind_foreign')) + '</option></select>' +
      '<select id="srv-dc"><option value="">' + esc(t('srv_dc')) + ': —</option>' + dcOpts(0) + '</select>' +
      '<input id="srv-addr" placeholder="' + esc(t('srv_addr')) + '" style="width:160px">' +
      '<button class="act primary" id="srv-add">' + esc(t('add')) + '</button></div></div>';
    // agents
    html += '<div class="card"><h3>' + esc(t('agents')) + '</h3><table><tr><th>' + esc(t('agent_col')) + '</th><th>' + esc(t('status_col')) + '</th><th>' + esc(t('ver_col')) + '</th><th>' + esc(t('seen_col')) + '</th><th>' + esc(t('link_col')) + '</th><th>' + esc(t('ip_override')) + '</th><th></th></tr>';
    ags.forEach(function (a) {
      var opts = '<option value="">' + esc(t('unbind')) + '</option>' + servers.map(function (s) {
        return '<option value="' + s.id + '"' + (a.server_id === s.id ? ' selected' : '') + '>' + esc(s.name) + '</option>';
      }).join('');
      html += '<tr><td>' + esc(a.name || a.id) + '<span class="mono muted"> ' + esc(a.id) + '</span></td>' +
        '<td><span class="pill ' + (a.status === 'online' ? 'ok' : (a.status === 'revoked' ? 'dim' : 'down')) + '">' + esc(a.status) + '</span></td>' +
        '<td>' + esc(a.version || '—') + '</td><td>' + fmtDate(a.last_seen) + '</td>' +
        '<td><select data-agent="' + a.id + '" class="agent-link">' + opts + '</select></td>' +
        '<td><input class="agent-ip" data-agent="' + a.id + '" value="' + esc(a.public_ip_override || '') + '" placeholder="' + esc(t('ip_hint')) + '" style="width:130px"></td>' +
        '<td>' + (a.status !== 'revoked' ? '<button class="act small danger" data-agrev="' + a.id + '">' + esc(t('agent_revoked')) + '</button>' : '') + '</td></tr>';
    });
    html += '</table></div>';
    // tokens
    html += '<div class="card"><h3>' + esc(t('tokens')) + '</h3><div id="tokout"><div class="skeleton"></div></div>' +
      '<div class="row"><select id="tok-role"><option value="iran">' + esc(t('kind_iran')) + '</option><option value="foreign">' + esc(t('kind_foreign')) + '</option></select>' +
      '<input id="tok-label" placeholder="' + esc(t('tok_label')) + '" style="width:160px">' +
      '<input id="tok-days" type="number" value="7" min="1" max="365" style="width:90px" aria-label="' + esc(t('tok_days')) + '">' +
      '<button class="act primary" id="tok-add">' + esc(t('tok_create')) + '</button></div></div>';
    el.innerHTML = html;

    // --- wire actions ---
    el.querySelector('#dc-add').onclick = function () {
      var name = el.querySelector('#dc-name').value.trim();
      if (!name) return;
      api('POST', 'datacenters', { name: name, kind: el.querySelector('#dc-kind').value,
        price_coefficient: parseFloat(el.querySelector('#dc-price').value) || 1.0 })
        .then(function () { toast(t('saved'), 'ok'); loadAll(); })
        .catch(function (e) { toast(e.message, 'err'); });
    };
    el.querySelectorAll('[data-dcdel]').forEach(function (b) {
      b.onclick = function () {
        confirmModal(t('del'), t('confirm_del').replace('{n}', b.dataset.n), t('del'), true).then(function (yes) {
          if (yes) api('DELETE', 'datacenters/' + b.dataset.dcdel).then(function () { toast(t('deleted'), 'ok'); loadAll(); })
            .catch(function (e) { toast(e.message, 'err'); });
        });
      };
    });
    el.querySelector('#srv-add').onclick = function () {
      var name = el.querySelector('#srv-name').value.trim();
      if (!name) return;
      var dcv = el.querySelector('#srv-dc').value;
      var body = { name: name, role: el.querySelector('#srv-role').value, address: el.querySelector('#srv-addr').value.trim() };
      if (dcv) body.datacenter_id = Number(dcv);
      api('POST', 'servers', body).then(function () { toast(t('saved'), 'ok'); loadAll(); })
        .catch(function (e) { toast(e.message, 'err'); });
    };
    el.querySelectorAll('[data-srvdel]').forEach(function (b) {
      b.onclick = function () {
        confirmModal(t('del'), t('confirm_del').replace('{n}', b.dataset.n), t('del'), true).then(function (yes) {
          if (yes) api('DELETE', 'servers/' + b.dataset.srvdel).then(function () { toast(t('deleted'), 'ok'); loadAll(); })
            .catch(function (e) { toast(e.message, 'err'); });
        });
      };
    });
    el.querySelectorAll('.agent-link').forEach(function (sel) {
      sel.onchange = function () {
        api('POST', 'agents/' + sel.dataset.agent + '/link', { server_id: Number(sel.value) || 0 })
          .then(function () { toast(t('saved'), 'ok'); loadAll(); })
          .catch(function (e) { toast(e.message, 'err'); });
      };
    });
    el.querySelectorAll('.agent-ip').forEach(function (inp) {
      inp.onchange = function () {
        api('POST', 'agents/' + inp.dataset.agent + '/public-ip', { public_ip: inp.value.trim() })
          .then(function () { toast(t('saved'), 'ok'); })
          .catch(function (e) { toast(e.message, 'err'); });
      };
    });
    el.querySelectorAll('[data-agrev]').forEach(function (b) {
      b.onclick = function () {
        confirmModal(t('agent_revoked'), t('confirm_del').replace('{n}', b.dataset.agrev), t('agent_revoked'), true).then(function (yes) {
          if (yes) apiRaw('POST', 'api/agents/' + b.dataset.agrev + '/revoke', {}).then(function () { toast(t('saved'), 'ok'); loadAll(); })
            .catch(function (e) { toast(e.message, 'err'); });
        });
      };
    });
    el.querySelector('#tok-add').onclick = function () {
      apiRaw('POST', 'api/agents/tokens', { role: el.querySelector('#tok-role').value,
        label: el.querySelector('#tok-label').value.trim() || 'agent',
        expires_days: parseInt(el.querySelector('#tok-days').value, 10) || 7 })
        .then(function (r) {
          renderTokens(el, r.token);
          toast(t('saved'), 'ok');
        }).catch(function (e) { toast(e.message, 'err'); });
    };
    renderTokens(el, '');
  }
  function renderTokens(el, newToken) {
    var out = el.querySelector('#tokout');
    apiRaw('GET', 'api/agents/tokens').then(function (r) {
      var rows = (r.tokens || []).filter(function (x) { return !x.revoked; }).map(function (x) {
        return '<tr><td>' + esc(x.label || x.id) + '</td><td>' + esc(x.role) + '</td><td>' + fmtDate(x.expires_at) + '</td>' +
          '<td><button class="act small danger" data-tokrev="' + x.id + '">' + esc(t('agent_revoked')) + '</button></td></tr>';
      }).join('');
      out.innerHTML = (newToken ? '<p class="pill info" style="display:block;padding:8px;margin-bottom:6px">' + esc(t('tok_copy')) + ' <b class="mono">' + esc(newToken) + '</b></p>' : '') +
        (rows ? '<table><tr><th>' + esc(t('tok_label')) + '</th><th>' + esc(t('tok_role')) + '</th><th></th><th></th></tr>' + rows + '</table>'
              : '<div class="empty">' + esc(t('tok_none')) + '</div>');
      out.querySelectorAll('[data-tokrev]').forEach(function (b) {
        b.onclick = function () {
          apiRaw('POST', 'api/agents/tokens/' + b.dataset.tokrev + '/revoke', {}).then(function () { toast(t('tok_revoked'), 'ok'); renderTokens(el, ''); })
            .catch(function (e) { toast(e.message, 'err'); });
        };
      });
    }).catch(function (e) { out.innerHTML = '<div class="empty">' + esc(e.message) + '</div>'; });
  }

  // ---- tunnels & weights (per-tunnel editor + share + batch) ------------------
  var weightsTopo = null;
  function renderTunnels() {
    var el = document.getElementById('page-tunnels');
    if (!overview) { el.innerHTML = '<div class="skeleton"></div>'; return; }
    var topos = (overview.topologies || []);
    if (!topos.length) {
      el.innerHTML = '<div class="empty">' + esc(t('no_tunnels')) + '</div>' +
        '<p><a class="btn primary" href="' + esc(base()) + 'mesh-wizard">' + esc(t('wizard_link')) + '</a></p>';
      return;
    }
    if (!weightsTopo) weightsTopo = topos[0].id;
    var html = '<div class="card"><div class="row"><label for="wtopo">' + esc(t('topo')) + ':</label><select id="wtopo">' +
      topos.map(function (tp) {
        return '<option value="' + tp.id + '"' + (weightsTopo == tp.id ? ' selected' : '') + '>' + esc(tp.name) + '</option>';
      }).join('') + '</select>' +
      '<span class="spacer"></span><span class="muted" id="w-enforced"></span></div>' +
      '<div id="wbody"><div class="skeleton"></div></div>' +
      '<p class="muted">' + esc(t('excluded')) + ' · ' + esc(t('resolution')) + '</p></div>';
    el.innerHTML = html;
    document.getElementById('wtopo').addEventListener('change', function (e) {
      weightsTopo = Number(e.target.value); loadWeights();
    });
    loadWeights();
  }
  function loadWeights() {
    var body = document.getElementById('wbody');
    if (!body) return;
    api('GET', 'topologies/' + weightsTopo).then(function (j) {
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
        var scopeTag = '<span class="pill ' + (scope === 'tunnel' ? 'ok' : 'dim') + '">' + esc(scope) + '</span>';
        return '<tr><td class="mono">' + esc(tn.tunnel_key) + '</td>' +
          '<td><input type="number" min="0" max="1000" value="' + resolved + '" data-tid="' + tn.id + '" style="width:80px" aria-label="' + esc(t('weight')) + ' ' + esc(tn.tunnel_key) + '"></td>' +
          '<td class="wshare" data-tn="' + tn.id + '">—</td>' +
          '<td>' + scopeTag + '</td>' +
          '<td><button class="act small" data-save="' + tn.id + '">' + esc(t('save')) + '</button></td></tr>';
      }).join('');
      var applied = (agents ? agents.agents : []).map(function (a) { return a.applied_revision; })
        .filter(function (x) { return !!x; }).length;
      document.getElementById('w-enforced').innerHTML = '<span class="pill ' + (applied ? 'ok' : 'warn') + '">' +
        esc(t('enforced')) + ': ' + (applied ? esc(t('applied_rev')) + ' ✓' : esc(t('not_pushed'))) + '</span>';
      body.innerHTML =
        '<table><tr><th>' + esc(t('tunnel')) + '</th><th>' + esc(t('weight')) + '</th><th>' + esc(t('percent')) + '</th><th>' + esc(t('resolved')) + '</th><th></th></tr>' + rows + '</table>' +
        '<div class="row"><span class="wsum" id="wsum"></span><span class="spacer"></span>' +
        '<button class="act primary" id="wsaveall">' + esc(t('save_all')) + '</button></div>';
      updateShares();
      body.addEventListener('input', function (e) {
        if (e.target.matches('input[data-tid]')) updateShares();
      });
      body.addEventListener('click', function (e) {
        var b = e.target.closest('button[data-save]');
        if (b) saveWeight(Number(b.dataset.save));
      });
      document.getElementById('wsaveall').onclick = function () {
        var inputs = Array.prototype.slice.call(body.querySelectorAll('input[data-tid]'));
        var chain = Promise.resolve();
        var n = 0;
        inputs.forEach(function (inp) {
          chain = chain.then(function () {
            return api('POST', 'topologies/' + weightsTopo + '/weights',
              { tunnel_id: Number(inp.dataset.tid), weight: Number(inp.value) })
              .then(function () { n += 1; });
          });
        });
        chain.then(function () { toast(t('saved') + ' ×' + n, 'ok'); loadWeights(); loadAll(); })
          .catch(function (e) { toast(e.message, 'err'); });
      };
    }).catch(function (e) { body.innerHTML = '<div class="empty">' + esc(e.message) + '</div>'; });
  }
  function updateShares() {
    var body = document.getElementById('wbody');
    if (!body) return;
    var inputs = Array.prototype.slice.call(body.querySelectorAll('input[data-tid]'));
    var sum = 0, nonzero = 0;
    inputs.forEach(function (inp) { var v = Number(inp.value) || 0; sum += v; if (v > 0) nonzero += 1; });
    inputs.forEach(function (inp) {
      var v = Number(inp.value) || 0;
      var cell = body.querySelector('.wshare[data-tn="' + inp.dataset.tid + '"]');
      if (cell) {
        cell.innerHTML = (sum > 0)
          ? (v > 0 ? Math.round(v / sum * 100) + '%' : '—') +
            '<div class="wbar"><i style="width:' + (sum > 0 ? (v / sum * 100) : 0) + '%"></i></div>'
          : '—';
      }
    });
    var ws = document.getElementById('wsum');
    if (ws) {
      ws.textContent = t('sum') + ': ' + sum + (sum === 100 ? ' ' + t('wsum_ok') : ' ' + t('wsum_bad'));
      ws.className = 'wsum ' + (sum === 100 ? 'good' : 'bad');
    }
  }
  function saveWeight(tid) {
    var input = document.querySelector('#page-tunnels input[data-tid="' + tid + '"]');
    api('POST', 'topologies/' + weightsTopo + '/weights', { tunnel_id: tid, weight: Number(input.value) })
      .then(function () { toast(t('saved') + ' #' + tid, 'ok'); loadWeights(); loadAll(); })
      .catch(function (e) { toast(e.message, 'err'); });
  }

  // ---- traffic & billing -------------------------------------------------------
  function renderTraffic() {
    var el = document.getElementById('page-traffic');
    if (!overview) { el.innerHTML = '<div class="skeleton"></div>'; return; }
    el.innerHTML = '<div class="card"><div class="row"><label for="udc">' + esc(t('dc')) + ':</label><select id="udc">' +
      (overview.datacenters || []).map(function (d) {
        return '<option value="' + d.id + '">' + esc(d.name) + '</option>';
      }).join('') +
      '</select><button class="act" id="ubtn">' + esc(t('usage')) + '</button>' +
      '<button class="act" id="cbtn">' + esc(t('coefficient')) + '</button>' +
      '<span class="muted">' + esc(t('from')) + '</span></div><div id="uout"></div></div>' +
      '<div class="card"><h3>' + esc(t('health')) + '</h3><div id="thealth"><div class="skeleton"></div></div></div>';
    document.getElementById('ubtn').onclick = function () {
      var dc = document.getElementById('udc').value;
      var from = Math.floor(Date.now() / 1000) - 86400;
      api('GET', 'usage?datacenter_id=' + dc + '&from=' + from).then(function (r) {
        var u = r.usage;
        document.getElementById('uout').innerHTML =
          '<table><tr><th></th><th>' + esc(t('wire')) + '</th><th>' + esc(t('payload')) + '</th></tr>' +
          '<tr><td>↓ RX</td><td>' + bytes(u.wire_rx) + '</td><td>' + bytes(u.payload_rx) + '</td></tr>' +
          '<tr><td>↑ TX</td><td>' + bytes(u.wire_tx) + '</td><td>' + bytes(u.payload_tx) + '</td></tr></table>' +
          '<p class="muted">' + esc(r.note) + '</p>';
      }).catch(function (e) { toast(e.message, 'err'); });
    };
    document.getElementById('cbtn').onclick = function () {
      var dc = document.getElementById('udc').value;
      Promise.all([
        api('GET', 'coefficient?datacenter_id=' + dc),
        api('GET', 'overhead'),
      ]).then(function (rs) {
        var r = rs[0], ov = rs[1] || {};
        var ovByEngine = {};
        (ov.engines || []).forEach(function (e) { ovByEngine[e.engine] = e; });
        var rows = (r.suggestions || []).map(function (sg) {
          var e = ovByEngine[sg.engine] || {};
          var warn = (e.tunnels > 0 && !(e.ratio > 0)) ? ' <span class="pill warn">' + esc(t('overhead')) + '?</span>' : '';
          return '<tr><td>' + esc(sg.engine) + '</td><td>' + (e.ratio ? e.ratio.toFixed(3) : '—') + '</td>' +
            '<td>' + (e.ratio_tx ? e.ratio_tx.toFixed(3) : '—') + '</td>' +
            '<td>' + (sg.price_ratio || 0).toFixed(3) + '</td>' +
            '<td>' + (sg.overhead || 0).toFixed(3) + '</td>' +
            '<td><b>' + (sg.suggested_coefficient || 0).toFixed(3) + '</b> ' + warn + '</td></tr>';
        }).join('');
        document.getElementById('uout').innerHTML =
          '<table><tr><th>' + esc(t('engine')) + '</th><th>' + esc(t('overhead')) + ' RX</th><th>' + esc(t('overhead')) + ' TX</th>' +
          '<th>' + esc(t('price')) + '</th><th>' + esc(t('overhead')) + '</th><th>' + esc(t('coefficient')) + '</th></tr>' + rows + '</table>' +
          '<p class="muted">' + esc(r.note) + ' — <a href="#" id="goto-pg">' + esc(t('nav_pg')) + '</a></p>';
        var g = document.getElementById('goto-pg');
        if (g) g.onclick = function (ev) { ev.preventDefault(); setPage('pasarguard'); };
      }).catch(function (e) { toast(e.message, 'err'); });
    };
    renderTunnelHealth();
  }
  // FIXED (phase-10 audit #4): the health table now fetches the REAL tunnels
  // per topology (the overview payload never had a top-level `tunnels`)
  // and joins the v2 agent runtime by tunnel_key.
  function renderTunnelHealth() {
    var box = document.getElementById('thealth');
    if (!box) return;
    var topos = (overview.topologies || []);
    Promise.all(topos.map(function (tp) {
      return api('GET', 'topologies/' + tp.id).then(function (j) {
        return (j.tunnels || []).map(function (tn) { return { tn: tn, topo: tp }; });
      }).catch(function () { return []; });
    })).then(function (chunks) {
      var all = [].concat.apply([], chunks);
      if (!all.length) {
        box.innerHTML = '<div class="empty">' + esc(t('no_tunnels')) + '</div>';
        return;
      }
      var rows = all.map(function (x) {
        var rt = runtimeFor(x.tn.tunnel_key);
        var st = rt == null ? t('unknown') : (rt.up ? t('up') : t('down'));
        var pill = rt == null ? 'dim' : (rt.up ? 'ok' : 'down');
        var ov = (rt && rt.payload_known && rt.wire_known && rt.payload_rx > 0)
          ? (rt.wire_rx / rt.payload_rx).toFixed(3) : '—';
        return '<tr><td class="mono">' + esc(x.tn.tunnel_key) + '</td><td>' + esc(x.topo.name) + '</td><td>' + esc(x.tn.engine) + '</td>' +
          '<td><span class="pill ' + pill + '">' + esc(st) + '</span></td>' +
          '<td>' + (rt && rt.latency_ms != null ? rt.latency_ms.toFixed(0) + ' ms' : '—') + '</td>' +
          '<td>' + bytes(rt && rt.wire_rx) + '</td><td>' + bytes(rt && rt.wire_tx) + '</td>' +
          '<td>' + bytes(rt && rt.payload_rx) + '</td><td>' + bytes(rt && rt.payload_tx) + '</td><td>' + ov + '</td></tr>';
      }).join('');
      box.innerHTML = '<div class="row"><input id="th-filter" type="search" placeholder="' + esc(t('search')) + '" style="width:220px"></div>' +
        '<table id="th-table"><tr><th>' + esc(t('tunnel')) + '</th><th>' + esc(t('topo')) + '</th><th>' + esc(t('engine')) + '</th><th>' + esc(t('state')) + '</th>' +
        '<th>' + esc(t('latency')) + '</th><th>' + esc(t('wire')) + ' ↓</th><th>' + esc(t('wire')) + ' ↑</th>' +
        '<th>' + esc(t('payload')) + ' ↓</th><th>' + esc(t('payload')) + ' ↑</th><th>' + esc(t('overhead')) + '</th></tr>' + rows + '</table>';
      var f = document.getElementById('th-filter');
      f.addEventListener('input', function () {
        var q = f.value.toLowerCase();
        box.querySelectorAll('#th-table tr').forEach(function (tr, i) {
          if (i === 0) return;
          tr.style.display = tr.textContent.toLowerCase().indexOf(q) >= 0 ? '' : 'none';
        });
      });
    }).catch(function (e) { box.innerHTML = '<div class="empty">' + esc(e.message) + '</div>'; });
  }

  // ---- engines -------------------------------------------------------------------
  function renderEngines() {
    var el = document.getElementById('page-engines');
    api('GET', 'engines').then(function (d) {
      var warns = (d.warnings || []).map(function (w) {
        return '<p class="muted">⚠ ' + esc(w) + '</p>';
      }).join('');
      var arts = (d.artifacts || []).map(function (a) {
        return '<tr><td>' + esc(a.engine) + '</td><td>' + esc(a.version) + '</td><td>' + esc(a.goos) + '</td><td>' + esc(a.goarch) + '</td>' +
          '<td class="mono">' + esc(a.artifact) + '</td><td>' + bytes(a.size) + '</td><td>' + fmtDate(a.uploaded_at) + '</td>' +
          '<td><button class="act small danger" data-del="' + a.id + '">' + esc(t('eng_del')) + '</button></td></tr>';
      }).join('');
      var agentsRows = (d.agent_engines || []).map(function (ae) {
        return '<tr><td>' + esc(ae.agent) + '</td><td>' + esc(ae.arch || '') + '</td><td>' +
          (ae.engines || []).join(', ') + '</td></tr>';
      }).join('');
      el.innerHTML =
        '<div class="card"><h3>' + esc(t('eng_artifacts')) + '</h3>' +
        '<p class="pill info" style="display:block;padding:9px">' + esc(t('eng_matrix_note')) + '</p>' +
        (arts ? '<table><tr><th>' + esc(t('engine')) + '</th><th>' + esc(t('eng_version')) + '</th><th>' + esc(t('eng_goos')) + '</th><th>' + esc(t('eng_arch')) + '</th><th></th><th>' + esc(t('eng_size')) + '</th><th>' + esc(t('eng_uploaded')) + '</th><th></th></tr>' + arts + '</table>'
              : '<div class="empty">' + esc(t('eng_none')) + '</div>') + warns + '</div>' +
        '<div class="card"><h3>' + esc(t('eng_upload')) + '</h3>' +
        '<form id="eng-form"><div class="formgrid">' +
        '<div class="field"><label for="eng-e">' + esc(t('engine')) + '</label><input id="eng-e" required pattern="[a-z0-9_-]+" value="matrix"></div>' +
        '<div class="field"><label for="eng-v">' + esc(t('eng_version')) + '</label><input id="eng-v" required></div>' +
        '<div class="field"><label for="eng-a">' + esc(t('eng_arch')) + '</label><select id="eng-a"><option>amd64</option><option>arm64</option></select></div>' +
        '<div class="field"><label for="eng-g">' + esc(t('eng_goos')) + '</label><input id="eng-g" value="linux" readonly></div>' +
        '<div class="field"><label for="eng-n">' + esc(t('eng_name')) + '</label><input id="eng-n"></div>' +
        '<div class="field"><label for="eng-s">' + esc(t('eng_sig')) + '</label><input id="eng-s" required></div>' +
        '</div><div class="field"><label for="eng-f">file</label><input id="eng-f" type="file" required></div>' +
        '<button class="act primary" type="submit">' + esc(t('eng_upload')) + '</button></form></div>' +
        '<div class="card"><h3>' + esc(t('eng_agents')) + '</h3>' +
        (agentsRows ? '<table><tr><th>' + esc(t('eng_agent_col')) + '</th><th>' + esc(t('eng_arch_col')) + '</th><th>' + esc(t('eng_engines_col')) + '</th></tr>' + agentsRows + '</table>' : '') +
        '</div>' +
        '<div class="card"><h3>' + esc(t('eng_key_snippet')) + '</h3><pre class="mono" id="eng-key"></pre></div>';
      el.querySelectorAll('[data-del]').forEach(function (b) {
        b.onclick = function () {
          confirmModal(t('eng_del'), t('confirm_del').replace('{n}', b.dataset.del), t('eng_del'), true).then(function (yes) {
            if (yes) api('DELETE', 'engines/artifacts/' + b.dataset.del).then(function () { toast(t('deleted'), 'ok'); renderEngines(); })
              .catch(function (e) { toast(e.message, 'err'); });
          });
        };
      });
      document.getElementById('eng-form').addEventListener('submit', function (ev) {
        ev.preventDefault();
        var fd = new FormData();
        fd.append('engine', document.getElementById('eng-e').value);
        fd.append('version', document.getElementById('eng-v').value);
        fd.append('goarch', document.getElementById('eng-a').value);
        fd.append('goos', document.getElementById('eng-g').value);
        var nm = document.getElementById('eng-n').value;
        if (nm) fd.append('name', nm);
        fd.append('signature', document.getElementById('eng-s').value);
        var f = document.getElementById('eng-f').files[0];
        if (!f) return;
        fd.append('file', f);
        toast(t('eng_uploading'));
        fetch(base() + 'api/mesh/engines/artifacts', { method: 'POST', headers: { 'X-CSRF-Token': csrfToken() }, body: fd })
          .then(function (r) { return r.json().then(function (j) { if (!r.ok) throw new Error(j.error || 'HTTP ' + r.status); return j; }); })
          .then(function () { toast(t('eng_uploaded_ok'), 'ok'); renderEngines(); })
          .catch(function (e) { toast(e.message, 'err'); });
      });
      api('GET', 'engines/keysnippet').then(function (k) {
        document.getElementById('eng-key').textContent = k.snippet || t('eng_no_key');
      }).catch(function () { document.getElementById('eng-key').textContent = t('eng_no_key'); });
    }).catch(function (e) { el.innerHTML = '<div class="empty">' + esc(e.message) + '</div>'; });
  }

  // ---- alerts ------------------------------------------------------------------------
  function renderAlerts() {
    var el = document.getElementById('page-alerts');
    api('GET', 'alerts').then(function (d) {
      var open = (d.open || []).map(function (a) {
        return '<tr><td><span class="pill ' + (a.sev === 'crit' ? 'down' : 'warn') + '">' + esc(a.rule) + '</span></td>' +
          '<td>' + esc(alertText(a)) + '</td><td class="muted">' + fmtDate(a.at) + '</td></tr>';
      }).join('');
      var hist = (d.history || []).map(function (a) {
        return '<tr><td>' + (a.recovery ? '<span class="pill ok">OK</span>' : '<span class="pill warn">⚠</span>') + '</td>' +
          '<td>' + esc(a.rule) + '</td><td>' + esc(alertText(a)) + '</td><td class="muted">' + fmtDate(a.at) + '</td></tr>';
      }).join('');
      el.innerHTML =
        '<div class="card"><h3>' + esc(t('al_open')) + '</h3>' +
        (open ? '<table><tr><th></th><th></th><th></th></tr>' + open + '</table>' : '<div class="empty">' + esc(t('al_none')) + '</div>') + '</div>' +
        '<div class="card"><h3>' + esc(t('al_rules')) + '</h3>' +
        '<form id="al-form" onsubmit="return false">' +
        '<div class="row"><label class="row"><input type="checkbox" id="al-en"' + (d.enabled ? ' checked' : '') + '> ' + esc(t('al_enable')) + '</label></div>' +
        '<div class="formgrid">' +
        '<div class="field"><label for="al-off">' + esc(t('al_offline')) + '</label><input id="al-off" type="number" value="' + d.agent_offline_min + '"></div>' +
        '<div class="field"><label for="al-cert">' + esc(t('al_cert')) + '</label><input id="al-cert" type="number" value="' + d.cert_expire_days + '"></div>' +
        '<div class="field"><label for="al-disk">' + esc(t('al_disk')) + '</label><input id="al-disk" type="number" value="' + d.disk_max_percent + '"></div>' +
        '<div class="field"><label for="al-tg">' + esc(t('al_tg')) + '</label><input id="al-tg" type="password" autocomplete="off"></div>' +
        '<div class="field"><label for="al-chat">' + esc(t('al_chat')) + '</label><input id="al-chat"></div>' +
        '<div class="field"><label for="al-hook">' + esc(t('al_hook')) + '</label><input id="al-hook"></div>' +
        '</div><div class="row"><button class="act primary" id="al-save">' + esc(t('al_save')) + '</button>' +
        '<button class="act" id="al-test">' + esc(t('al_test')) + '</button>' +
        '<span class="muted">' + (d.telegram_set ? 'Telegram ✓ ' : '') + (d.webhook_set ? 'Webhook ✓' : '') + '</span></div></form></div>' +
        '<div class="card"><h3>' + esc(t('al_history')) + '</h3>' +
        (hist ? '<table><tr><th></th><th></th><th></th><th></th></tr>' + hist + '</table>' : '<div class="empty">—</div>') + '</div>';
      document.getElementById('al-save').onclick = function () {
        api('POST', 'alerts/settings', {
          enabled: document.getElementById('al-en').checked,
          agent_offline_min: parseInt(document.getElementById('al-off').value, 10),
          cert_expire_days: parseInt(document.getElementById('al-cert').value, 10),
          disk_max_percent: parseInt(document.getElementById('al-disk').value, 10),
          telegram_token: document.getElementById('al-tg').value,
          telegram_chat: document.getElementById('al-chat').value,
          webhook_url: document.getElementById('al-hook').value
        }).then(function () { toast(t('saved'), 'ok'); renderAlerts(); loadAll(); })
          .catch(function (e) { toast(e.message, 'err'); });
      };
      document.getElementById('al-test').onclick = function () {
        api('POST', 'alerts/test').then(function (r) {
          toast(r.sent ? t('al_test_ok') + ' ' + r.sent + ' ' + t('al_notif') : t('al_no_conf'), r.sent ? 'ok' : 'err');
        }).catch(function (e) { toast(e.message, 'err'); });
      };
    }).catch(function (e) { el.innerHTML = '<div class="empty">' + esc(e.message) + '</div>'; });
  }
  // Alerts carry raw agent ids — join the friendly name for the operator.
  function alertText(a) {
    var m = /agent (agt_[0-9a-f]+)/.exec(a.text || '');
    if (m) return a.text.replace(m[1], agentName(m[1]) + ' (' + m[1] + ')');
    return a.text;
  }

  // ---- security (profiles / safe-apply) ---------------------------------------------
  function renderProfiles() {
    var el = document.getElementById('page-security');
    api('GET', 'agents').then(function (j) {
      var rows = (j.agents || []).filter(function (a) { return a.status !== 'revoked'; }).map(function (a) {
        return '<tr><td>' + esc(a.name || a.id) + '</td><td><span class="pill ' + (a.status === 'online' ? 'ok' : 'down') + '">' + esc(a.status) + '</span></td>' +
          '<td>v' + esc(a.proto_version || 1) + '</td>' +
          '<td><button class="act small" data-h="' + a.id + '">' + esc(t('apply_h')) + '</button> ' +
          '<button class="act small" data-n="' + a.id + '">' + esc(t('apply_n')) + '</button> ' +
          '<button class="act small primary" data-push="' + a.id + '">' + esc(t('push_btn')) + '</button></td></tr>';
      }).join('');
      el.innerHTML = '<div class="card"><h3>' + esc(t('agents')) + '</h3>' +
        '<table><tr><th>' + esc(t('agent_col')) + '</th><th>' + esc(t('status_col')) + '</th><th>proto</th><th></th></tr>' + rows + '</table>' +
        '<div class="field"><label for="prof">profile JSON</label>' +
        '<textarea id="prof" rows="6" class="mono" style="width:100%">{}</textarea></div>' +
        '<p class="muted">' + esc(t('prof_hint')) + '</p></div>';
      el.addEventListener('click', profileClick);
    }).catch(function (e) { el.innerHTML = '<div class="empty">' + esc(e.message) + '</div>'; });
  }
  function profileClick(e) {
    var h = e.target.closest('button[data-h]');
    var n = e.target.closest('button[data-n]');
    var p = e.target.closest('button[data-push]');
    if (p) {
      api('POST', 'agents/' + p.dataset.push + '/push', {}).then(function (r) {
        toast(t('pushed_ok') + (r.revision ? ' rev ' + r.revision : ''), 'ok');
      }).catch(function (er) { toast(er.message, 'err'); });
      return;
    }
    if (!h && !n) return;
    var kind = h ? 'hardening' : 'netopt';
    var agent = h ? h.dataset.h : n.dataset.n;
    var prof;
    try { prof = JSON.parse(document.getElementById('prof').value); }
    catch (err) { toast(t('need_json'), 'err'); return; }
    api('POST', 'agents/' + agent + '/profile/' + kind,
      { profile: prof, rollback_timer_s: 120 })
      .then(function (r) { toast(t('pushed') + '; ' + r.rollback_timer_s + 's', 'ok'); renderBanner(); })
      .catch(function (er) { toast(er.message, 'err'); });
  }

  // ---- pasarguard --------------------------------------------------------------------
  function renderPG() {
    var el = document.getElementById('page-pasarguard');
    api('GET', 'pasarguard/status').then(function (st) {
      var html = '<div class="card"><div class="row">' +
        '<input id="pg-url" placeholder="' + esc(t('pg_url')) + ' (https://pg.example:8000)" style="width:280px">' +
        '<input id="pg-user" placeholder="' + esc(t('pg_user')) + '">' +
        '<input id="pg-pass" type="password" autocomplete="off" placeholder="' + esc(t('pg_pass')) + '">' +
        '<button class="act primary" id="pg-connect">' + esc(t('pg_connect')) + '</button>' +
        '<span class="muted" id="pg-status">' + (st.configured ? esc(st.url) + ' ✓' : '') + '</span></div>' +
        '<div class="row">' +
        '<button class="act" id="pg-dry">' + esc(t('pg_dry')) + '</button>' +
        '<button class="act primary" id="pg-apply">' + esc(t('pg_apply')) + '</button>' +
        '<button class="act" id="pg-cross">' + esc(t('pg_crosscheck')) + '</button></div>' +
        '<div id="pg-out"></div></div>';
      el.innerHTML = html;
      document.getElementById('pg-connect').onclick = function () {
        api('POST', 'pasarguard/connect', {
          url: document.getElementById('pg-url').value,
          username: document.getElementById('pg-user').value,
          password: document.getElementById('pg-pass').value
        }).then(function (r) { toast('connected: ' + r.nodes + ' nodes', 'ok'); renderPG(); loadAll(); })
          .catch(function (e) { toast(e.message, 'err'); });
      };
      document.getElementById('pg-dry').onclick = function () { pgReconcile(false); };
      document.getElementById('pg-apply').onclick = function () { pgReconcile(true); };
      document.getElementById('pg-cross').onclick = function () { pgCross(); };
    }).catch(function (e) { el.innerHTML = '<div class="empty">' + esc(e.message) + '</div>'; });
  }
  function pgReconcile(apply) {
    var body = { apply: apply };
    api('POST', 'pasarguard/reconcile', body).then(function (r) {
      var rows = (r.plan.actions || []).map(function (a) {
        return '<tr><td>' + esc(a.op) + '</td><td>' + esc(a.name || '') + '</td><td>' + esc(a.detail || '') + '</td></tr>';
      }).join('');
      document.getElementById('pg-out').innerHTML =
        (rows ? '<table><tr><th>op</th><th>name</th><th></th></tr>' + rows + '</table>' : '<div class="empty">—</div>') +
        (r.warnings || []).map(function (w) { return '<p class="muted">⚠ ' + esc(w) + '</p>'; }).join('');
    }).catch(function (e) { toast(e.message, 'err'); });
  }
  function pgNodes() {
    api('GET', 'pasarguard/nodes').then(function (r) {
      var rows = (r.nodes || []).map(function (n) {
        return '<tr><td>' + esc(n.name || n.id) + '</td><td>' + esc(n.address || '') + '</td><td>' + esc(n.status || '') + '</td></tr>';
      }).join('');
      var el = document.getElementById('pg-out');
      if (el) el.innerHTML = rows ? '<table><tr><th></th><th></th><th></th></tr>' + rows + '</table>' : '<div class="empty">—</div>';
    }).catch(function () {});
  }
  function pgCross() {
    var dc = document.getElementById('udc') ? document.getElementById('udc').value : '';
    api('GET', 'pasarguard/crosscheck?datacenter_id=' + dc).then(function (r) {
      var rows = (r.rows || []).map(function (x) {
        return '<tr><td>' + esc(x.node || '') + '</td><td>' + esc(x.server || '') + '</td><td>' + bytes(x.pg_used) + '</td><td>' + bytes(x.panel_used) + '</td></tr>';
      }).join('');
      document.getElementById('pg-out').innerHTML = rows
        ? '<table><tr><th>node</th><th>server</th><th>PasarGuard</th><th>Panel</th></tr>' + rows + '</table>' : '<div class="empty">—</div>';
    }).catch(function (e) { toast(e.message, 'err'); });
  }

  // ---- settings (backup + preferences + legacy gate) ------------------------------------
  function renderSettings() {
    var el = document.getElementById('page-settings');
    var calV = CAL;
    el.innerHTML = '<div class="card"><h3>' + esc(t('backup_btn')) + '</h3>' +
      '<div class="formgrid">' +
      '<div class="field"><label for="bk-pw">' + esc(t('backup_ask_pw')) + '</label><input id="bk-pw" type="password" autocomplete="off"></div>' +
      '<div class="field"><label for="bk-pp">' + esc(t('backup_ask_pass')) + '</label><input id="bk-pp" type="password" autocomplete="off"></div>' +
      '</div><button class="act primary" id="bk-go">' + esc(t('backup_btn')) + '</button></div>' +
      '<div class="card"><h3>' + esc(t('prefs')) + '</h3>' +
      '<div class="formgrid">' +
      '<div class="field"><label for="pref-lang">' + esc(t('lang_pref')) + '</label><select id="pref-lang"><option value="fa"' + (LANG === 'fa' ? ' selected' : '') + '>فارسی</option><option value="en"' + (LANG === 'en' ? ' selected' : '') + '>English</option></select></div>' +
      '<div class="field"><label for="pref-cal">' + esc(t('calendar')) + '</label><select id="pref-cal"><option value="jalali"' + (calV === 'jalali' ? ' selected' : '') + '>' + esc(t('cal_jalali')) + '</option><option value="gregorian"' + (calV === 'gregorian' ? ' selected' : '') + '>' + esc(t('cal_greg')) + '</option></select></div>' +
      '</div><p class="muted">' + esc(t('reload_note')) + '</p></div>' +
      '<div class="card"><h3>' + esc(t('panel_ver')) + '</h3><p class="muted">' + esc(meta && meta.version ? meta.version : '—') + '</p>' +
      '<h3>' + esc(t('legacy_note')) + '</h3><p class="muted">' +
      (meta && meta.legacy_enabled
        ? '<a class="btn" href="' + esc(base()) + './">' + esc(t('legacy_open')) + '</a>'
        : esc(t('legacy_gated'))) + '</p></div>';
    document.getElementById('bk-go').onclick = function () {
      var pw = document.getElementById('bk-pw').value;
      var pass = document.getElementById('bk-pp').value;
      if (!pw) { toast(t('backup_ask_pw'), 'err'); return; }
      if (!pass || pass.length < 12) { toast(t('backup_denied'), 'err'); return; }
      fetch(base() + 'api/mesh/backup', {
        method: 'POST',
        headers: { 'X-CSRF-Token': csrfToken(), 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: pw, passphrase: pass })
      }).then(function (r) {
        if (!r.ok) { toast(t('backup_denied'), 'err'); return null; }
        return r.blob();
      }).then(function (blob) {
        if (!blob) return;
        var a = document.createElement('a');
        a.href = URL.createObjectURL(blob);
        a.download = 'quantum-panel-backup.qpbak';
        document.body.appendChild(a); a.click(); a.remove();
        toast(t('backup_ok'), 'ok');
      }).catch(function (e) { toast(e.message, 'err'); });
    };
    document.getElementById('pref-lang').onchange = function (e) {
      localStorage.setItem('mesh_lang', e.target.value); location.reload();
    };
    document.getElementById('pref-cal').onchange = function (e) {
      localStorage.setItem('mesh_cal', e.target.value); location.reload();
    };
  }

  // ---- safe-apply confirm banner (global, with live countdown) ---------------------------
  var bannerIv = null;
  function renderBanner() {
    var el = document.getElementById('confirm-banner');
    api('GET', 'profiles/pending').then(function (j) {
      var p = (j.pending || [])[0];
      if (!p) { el.style.display = 'none'; if (bannerIv) { clearInterval(bannerIv); bannerIv = null; } return; }
      var left = Math.max(0, p.deadline - Math.floor(Date.now() / 1000));
      el.style.display = '';
      var render_ = function () {
        el.innerHTML = '<b>⏱ ' + esc(t('confirm_banner').replace('{agent}', agentName(p.agent_id)).replace('{s}', left)) + '</b> ' +
          '<button class="act primary small" id="btn-confirm">' + esc(t('confirm')) + '</button> ' +
          '<button class="act small" id="btn-rollback">' + esc(t('rollback')) + '</button>';
      };
      render_();
      document.getElementById('btn-confirm').onclick = function () {
        api('POST', 'agents/' + p.agent_id + '/profile/confirm', {})
          .then(function () { toast(t('pushed_ok'), 'ok'); renderBanner(); })
          .catch(function (er) { toast(er.message, 'err'); });
      };
      document.getElementById('btn-rollback').onclick = function () {
        api('POST', 'agents/' + p.agent_id + '/profile/rollback?kind=' + p.kind, {})
          .then(function () { toast(t('pushed'), 'ok'); renderBanner(); })
          .catch(function (er) { toast(er.message, 'err'); });
      };
      if (bannerIv) clearInterval(bannerIv);
      bannerIv = setInterval(function () {
        left -= 1;
        var b = el.querySelector('b');
        if (!b || left < 0) { clearInterval(bannerIv); bannerIv = null; return; }
        b.textContent = '⏱ ' + t('confirm_banner').replace('{agent}', agentName(p.agent_id)).replace('{s}', left);
      }, 1000);
    }).catch(function () { el.style.display = 'none'; });
  }

  // ---- applyLang + boot -----------------------------------------------------------------
  function applyLang() {
    document.documentElement.lang = LANG;
    document.documentElement.dir = LANG === 'fa' ? 'rtl' : 'ltr';
    Array.prototype.forEach.call(document.querySelectorAll('[data-i18n]'), function (el) {
      el.textContent = t(el.dataset.i18n);
    });
    document.getElementById('title').textContent = t('title_' + page);
  }

  document.getElementById('nav').addEventListener('click', function (e) {
    var b = e.target.closest('button[data-page]');
    if (b) setPage(b.dataset.page);
  });
  document.getElementById('menubtn').addEventListener('click', openSidebar);
  document.getElementById('scrim').addEventListener('click', closeSidebar);
  document.getElementById('refresh').addEventListener('click', function () { loadAll(); });
  document.getElementById('lang').addEventListener('click', function () {
    LANG = LANG === 'fa' ? 'en' : 'fa';
    localStorage.setItem('mesh_lang', LANG);
    applyLang(); render();
  });
  document.getElementById('theme').addEventListener('click', function () {
    var th = (localStorage.getItem('gre_theme') || 'dark') === 'dark' ? 'light' : 'dark';
    localStorage.setItem('gre_theme', th);
    applyTheme();
  });
  // logout is a POST (CSRF-safe): intercept the link
  document.getElementById('logout').addEventListener('click', function (e) {
    e.preventDefault();
    apiRaw('POST', 'api/logout', {}).then(function () { location.href = base(); })
      .catch(function () { location.href = base(); });
  });

  applyTheme();
  applyLang();
  setPage('dash');
  api('GET', 'meta').then(function (m) {
    meta = m;
    if (m.legacy_enabled) document.getElementById('legacy-link').hidden = false;
  }).catch(function () {});
  loadAll();
  setInterval(loadAll, 10000); // live state, ~heartbeat cadence
})();
