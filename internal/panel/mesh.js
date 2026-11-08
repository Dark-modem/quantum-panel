// mesh.js — the mesh management UI (phase 11): consumes the /api/ui/*
// contract (00-ADMIN/UI-API-CONTRACT.md) exclusively. Persian-first RTL,
// Jalali dates, structured error cards, hash routing so every detail page
// is linkable, no external assets. NOT a port of the legacy app.
(function () {
  'use strict';

  // ---- preferences ---------------------------------------------------------
  var LANG = localStorage.getItem('mesh_lang') || 'fa';
  var CAL = localStorage.getItem('mesh_cal') || 'jalali';

  var T = {
    en: {
      // nav + titles
      brand_sub: 'mesh', nav_dash: 'Dashboard', nav_topo: 'Topologies', nav_agents: 'Agents',
      nav_tunnels: 'Tunnels & weights', nav_engines: 'Engines', nav_alerts: 'Alerts',
      nav_security: 'Security', nav_pg: 'PasarGuard', nav_settings: 'Settings',
      nav_legacy: 'Advanced (legacy)', ng_mesh: 'Infrastructure', ng_ops: 'Operations',
      title_dash: 'Dashboard', title_topologies: 'Topologies', title_agents: 'Agents',
      title_agent: 'Agent', title_tunnels: 'Tunnels & weights', title_tunnel: 'Tunnel',
      title_engines: 'Engines', title_alerts: 'Alerts', title_security: 'Security (safe-apply)',
      title_pg: 'PasarGuard', title_settings: 'Settings', title_notfound: 'Not found',
      refresh_lbl: 'Refresh', logout_lbl: 'Log out', theme_lbl: 'Theme',
      // dashboard
      k_dcs: 'Datacenters', k_servers: 'Servers', k_agents: 'Agents', k_topos: 'Topologies',
      k_tunnels: 'Tunnels', t_up: 'up', t_down: 'down', t_degraded: 'degraded', t_disabled: 'disabled',
      k_res: 'Agent resources', res_cpu: 'CPU', res_mem: 'RAM', res_disk: 'Disk',
      res_sum: 'total', res_avg: 'average', res_max: 'busiest',
      k_traffic: 'Traffic (Iran datacenters)', rng_today: 'Today', rng_7d: '7 days', rng_30d: '30 days',
      th_dc: 'Datacenter', th_billable: 'Billable download', th_upload: 'Upload',
      th_payload: 'Payload (download)', th_coeff: 'Coefficient',
      k_alerts: 'Active alerts', k_pending: 'Pending confirmations', k_cert: 'Certificate',
      cert_valid: 'valid', cert_expiring: 'expiring', cert_expired: 'expired', cert_unknown: 'no certificate',
      cert_days: '{d} days left', cert_expired_d: 'expired {d} days ago',
      cached_note: '7d/30d figures are cached (≤60 s old).',
      checklist_title: 'First-run checklist', checklist_sub: 'Complete these steps to go live.',
      step1: 'Add an Iran datacenter', step2: 'Add servers & connect agents', step3: 'Enable the Matrix engine',
      step4: 'Create a topology', step5: 'Set tunnel weights', step6: 'Connect PasarGuard',
      step7: 'Enable alerts', step8: 'Download a backup',
      done: 'done', todo: 'open',
      // agents
      th_agent: 'Agent', th_state: 'State', th_role: 'Role', th_dc: 'Datacenter',
      th_topos: 'Topologies', th_engines: 'Engines (enabled/running)', th_cpu: 'CPU',
      th_mem: 'RAM', th_seen: 'Last seen', online: 'online', offline: 'offline', revoked: 'revoked',
      agent_info: 'Agent information', agent_res: 'Resources', ver_lbl: 'Version', os_lbl: 'OS',
      host_lbl: 'Hostname', arch_lbl: 'Arch', uptime_lbl: 'Uptime', ip_lbl: 'Public IP',
      ip_ovr: '(manual override)', ip_save: 'Save IP', ip_hint: 'empty = auto-detect',
      rev_lbl: 'Applied revision', mem_use: '{u} of {t} MB used',
      agent_topos: 'Topology memberships', th_myrole: 'Role in topology', th_tunnels: 'Tunnels',
      member_of: 'member', no_topos: 'This agent is not a member of any topology yet.',
      agent_engines: 'Engines on this agent', th_engine: 'Engine', th_installed: 'Installed',
      th_enabled: 'Enabled', th_running: 'Running', th_health: 'Health', th_lasterr: 'Last error',
      installed: 'installed', not_installed: 'not installed', running: '{n} running', stopped: 'stopped',
      healthy: 'healthy', degraded: 'degraded', down: 'down',
      enable: 'Enable', disable: 'Disable', enable_q: 'Enable engine {e} on agent {a}?',
      disable_q: 'Disable engine {e} on agent {a}? Tunnels that still use it will be refused.',
      enabled_ok: 'Engine {e} enabled on {a}', disabled_ok: 'Engine {e} disabled on {a}',
      block_no_artifact: 'No signed artifact for this agent\'s architecture — upload one (Engines page) or use a kernel engine.',
      matrix_default: 'Matrix is enabled by default on every new agent.',
      recent_errors: 'Recent errors', no_errors: 'No recent errors.',
      kernel_note: 'kernel engine — no binary on disk',
      bytes_disk: '{b} on disk',
      // topology
      th_topology: 'Topology', th_members: 'Members', th_enabled: 'Enabled',
      topo_summary: 'Summary', topo_health: 'Health', th_traffic_today: 'Traffic today',
      map_title: 'Iran datacenters → foreign servers',
      map_hint: 'Line thickness = tunnel count · color = health · click a line for its tunnels',
      map_empty: 'No tunnels yet — create a topology with the wizard.',
      iran_side: 'Iran', foreign_side: 'Foreign',
      th_peer: 'Peer', th_engine: 'Engine', th_weight: 'Weight', th_share: 'Share',
      th_today: 'Today (wire ↓/↑)', th_payload: 'Payload (↓/↑)', th_state: 'State', th_latency: 'Latency',
      open_tunnel: 'Open tunnel detail',
      // tunnels & weights
      weights_hint: 'Weights are PER TUNNEL. The share is weight ÷ Σweights of the topology\'s enabled tunnels on the same Iran server; agents enforce exactly these shares after a push.',
      sum_lbl: 'Σ weights', save_all: 'Save all changed', saved_one: 'Saved tunnel {k}',
      enforced: 'Agent view', not_pushed: 'changes not pushed yet',
      search: 'Search…', no_tunnels: 'No tunnels in this topology yet.',
      wizard_link: 'Open the setup wizard →',
      // tunnel detail
      td_now: 'Current state', td_uptime: 'Uptime', td_cpu: 'Engine CPU', td_rss: 'Engine memory',
      td_lat: 'Latency', td_loss: 'Loss', td_rate: 'Current rate',
      td_na: 'not available', td_na_kernel: 'not available for kernel engines',
      td_totals: 'Totals per direction', td_today: 'Today', td_billable: 'billable download',
      td_ratios: 'Ratios', td_share: 'Share of topology traffic (today)', td_overhead: 'Overhead (wire ÷ payload)',
      td_history: 'Traffic history', td_events: 'State changes & errors', td_noevents: 'No events recorded yet.',
      td_nodata: 'No traffic recorded in this range yet.',
      ev_state: 'state', ev_error: 'error', ev_from: 'from', ev_to: 'to',
      wire: 'Wire', payload: 'Payload', down_dir: 'download', up_dir: 'upload',
      rng_24h: '24 hours',
      // engines page
      eng_artifacts: 'Signed engine artifacts', eng_none: 'No artifacts uploaded yet.',
      eng_upload: 'Upload artifact', eng_version: 'Version', eng_arch: 'Arch',
      eng_matrix_note: 'Matrix is the default and recommended engine. Other engines are experimental: they work, but their parameter coverage, overhead behaviour and failure modes are less battle-tested.',
      eng_default: 'default / recommended', eng_experimental: 'experimental',
      // alerts
      al_rules: 'Alert rules', al_open: 'Open alerts', al_history: 'Recent notifications',
      al_none: 'No alerts — all quiet.', al_enable: 'Alerts enabled', al_offline: 'Agent offline after (min)',
      al_cert: 'Cert expiry warn (days)', al_disk: 'Disk max (%)', al_tg: 'Telegram bot token',
      al_chat: 'Telegram chat id', al_hook: 'Webhook URL (http(s))', al_save: 'Save settings',
      al_test: 'Send test notification', al_test_ok: 'Test notification sent to', al_notif: 'notifier(s)',
      al_no_conf: 'No notifier configured — fill Telegram or webhook first.',
      // security
      sec_agents: 'Agents', apply_h: 'Apply hardening profile', apply_n: 'Apply netopt profile',
      push_btn: 'Push desired state', prof_hint: 'The JSON below is the profile the buttons apply to the selected agent. Applying arms the agent\'s automatic 120 s rollback; confirm in the yellow banner to keep it.',
      confirm: 'Confirm (keep)', rollback: 'Rollback now',
      confirm_banner: 'Safe-apply armed on {agent} — confirm within {s}s or the agent rolls back.',
      need_json: 'Profile JSON is invalid', pushed_ok: 'Pushed to agent',
      // pasarguard
      pg_connect: 'Connect', pg_url: 'Panel URL', pg_user: 'Admin user', pg_pass: 'Admin password',
      pg_dry: 'Dry-run diff', pg_apply: 'Apply plan', pg_crosscheck: 'Cross-check usage',
      pg_coeff: 'Suggested coefficient', pg_overhead: 'Engine overhead',
      // settings
      backup_btn: 'Download encrypted backup', backup_ask_pw: 'Panel password (re-auth):',
      backup_ask_pass: 'Backup passphrase (min 12 chars) — STORE IT SAFELY, without it the backup is unreadable:',
      backup_ok: 'Backup downloaded (encrypted).', backup_denied: 'Backup denied: wrong panel password.',
      prefs: 'Preferences', calendar: 'Calendar', cal_jalali: 'Jalali (Persian)', cal_greg: 'Gregorian',
      lang_pref: 'Language', legacy_note: 'Legacy host-operator pages',
      legacy_open: 'Open legacy pages', legacy_gated: 'disabled (enable legacy_enabled in panel.json)',
      reload_note: 'Reloads the page.',
      // errors / misc
      load_fail: 'Could not load this page', net_down: 'The panel is unreachable — check your connection or sign in again.',
      login_redirect: 'Sign in', retry: 'Retry', ok: 'OK', cancel: 'Cancel', copy: 'Copy details',
      copied: 'Copied', del: 'Delete', add: 'Add', saved: 'Saved', deleted: 'Deleted',
      confirm_del: 'Delete "{n}"? This cannot be undone.', err_details: 'Technical details',
      what_happened: 'What happened', likely_cause: 'Likely cause', what_to_do: 'What to do',
      th_created: 'Created'
    },
    fa: {
      brand_sub: 'مش', nav_dash: 'داشبورد', nav_topo: 'توپولوژی‌ها', nav_agents: 'ایجنت‌ها',
      nav_tunnels: 'تونل‌ها و وزن‌ها', nav_engines: 'موتورها', nav_alerts: 'هشدارها',
      nav_security: 'امنیت', nav_pg: 'پاسارگارد', nav_settings: 'تنظیمات',
      nav_legacy: 'پیشرفته (نسخه قدیمی)', ng_mesh: 'زیرساخت', ng_ops: 'عملیات',
      title_dash: 'داشبورد', title_topologies: 'توپولوژی‌ها', title_agents: 'ایجنت‌ها',
      title_agent: 'ایجنت', title_tunnels: 'تونل‌ها و وزن‌ها', title_tunnel: 'تونل',
      title_engines: 'موتورها', title_alerts: 'هشدارها', title_security: 'امنیت (اعمال ایمن)',
      title_pg: 'پاسارگارد', title_settings: 'تنظیمات', title_notfound: 'پیدا نشد',
      refresh_lbl: 'به‌روزرسانی', logout_lbl: 'خروج', theme_lbl: 'پوسته',
      k_dcs: 'دیتاسنترها', k_servers: 'سرورها', k_agents: 'ایجنت‌ها', k_topos: 'توپولوژی‌ها',
      k_tunnels: 'تونل‌ها', t_up: 'برقرار', t_down: 'قطع', t_degraded: 'کاهش‌کیفیت', t_disabled: 'غیرفعال',
      k_res: 'منابع ایجنت‌ها', res_cpu: 'پردازنده', res_mem: 'حافظه', res_disk: 'دیسک',
      res_sum: 'مجموع', res_avg: 'میانگین', res_max: 'پرمصرف‌ترین',
      k_traffic: 'ترافیک (دیتاسنترهای ایران)', rng_today: 'امروز', rng_7d: '۷ روز', rng_30d: '۳۰ روز',
      th_dc: 'دیتاسنتر', th_billable: 'دانلود قابل‌قبض', th_upload: 'آپلود',
      th_payload: 'بار (دانلود)', th_coeff: 'ضریب',
      k_alerts: 'هشدارهای فعال', k_pending: 'تأییدهای در انتظار', k_cert: 'گواهی TLS',
      cert_valid: 'معتبر', cert_expiring: 'نزدیک انقضا', cert_expired: 'منقضی', cert_unknown: 'گواهی ندارد',
      cert_days: '{d} روز مانده', cert_expired_d: '{d} روز پیش منقضی شد',
      cached_note: 'ارقام ۷/۳۰ روزه کش‌شده‌اند (حداکثر ۶۰ ثانیه قدیمی‌تر).',
      checklist_title: 'چک‌لیست راه‌اندازی', checklist_sub: 'این مرحله‌ها را کامل کنید تا سرویس بالا بیاید.',
      step1: 'افزودن دیتاسنتر ایران', step2: 'افزودن سرورها و اتصال ایجنت‌ها', step3: 'فعال‌سازی موتور Matrix',
      step4: 'ساخت توپولوژی', step5: 'تنظیم وزن تونل‌ها', step6: 'اتصال پاسارگارد',
      step7: 'فعال‌سازی هشدارها', step8: 'دانلود پشتیبان',
      done: 'انجام شد', todo: 'باز است',
      th_agent: 'ایجنت', th_state: 'وضعیت', th_role: 'نقش', th_dc: 'دیتاسنتر',
      th_topos: 'توپولوژی‌ها', th_engines: 'موتورها (فعال/در حال اجرا)', th_cpu: 'پردازنده',
      th_mem: 'حافظه', th_seen: 'آخرین حضور', online: 'برخط', offline: 'خارج از خط', revoked: 'باطل‌شده',
      agent_info: 'اطلاعات ایجنت', agent_res: 'منابع', ver_lbl: 'نسخه', os_lbl: 'سیستم‌عامل',
      host_lbl: 'نام میزبان', arch_lbl: 'معماری', uptime_lbl: 'مدت کار', ip_lbl: 'IP عمومی',
      ip_ovr: '(دستی)', ip_save: 'ذخیره IP', ip_hint: 'خالی = تشخیص خودکار',
      rev_lbl: 'revision اعمال‌شده', mem_use: '{u} از {t} مگابایت مصرف شده',
      agent_topos: 'عضویت در توپولوژی‌ها', th_myrole: 'نقش در توپولوژی', th_tunnels: 'تونل‌ها',
      member_of: 'عضو', no_topos: 'این ایجنت هنوز عضو هیچ توپولوژی‌ای نیست.',
      agent_engines: 'موتورهای این ایجنت', th_engine: 'موتور', th_installed: 'نصب‌شده',
      th_enabled: 'فعال', th_running: 'در حال اجرا', th_health: 'سلامت', th_lasterr: 'آخرین خطا',
      installed: 'نصب‌شده', not_installed: 'نصب نشده', running: '{n} در حال اجرا', stopped: 'متوقف',
      healthy: 'سالم', degraded: 'کاهش‌کیفیت', down: 'قطع',
      enable: 'فعال‌سازی', disable: 'غیرفعال‌سازی', enable_q: 'موتور {e} روی ایجنت {a} فعال شود؟',
      disable_q: 'موتور {e} روی ایجنت {a} غیرفعال شود؟ تونل‌هایی که هنوز از آن استفاده می‌کنند رد می‌شوند.',
      enabled_ok: 'موتور {e} روی {a} فعال شد', disabled_ok: 'موتور {e} روی {a} غیرفعال شد',
      block_no_artifact: 'برای معماری این ایجنت فایل امضاشده‌ای نیست — از صفحهٔ موتورها بارگذاری کنید یا از موتورِ هسته‌ای استفاده کنید.',
      matrix_default: 'Matrix روی هر ایجنت تازه به‌صورت پیش‌فرض فعال است.',
      recent_errors: 'خطاهای اخیر', no_errors: 'خطای اخیری نیست.',
      kernel_note: 'موتور هسته‌ای — بدون فایل باینری',
      bytes_disk: '{b} روی دیسک',
      th_topology: 'توپولوژی', th_members: 'اعضا', th_enabled: 'فعال',
      topo_summary: 'خلاصه', topo_health: 'سلامت', th_traffic_today: 'ترافیک امروز',
      map_title: 'دیتاسنترهای ایران → سرورهای خارج',
      map_hint: 'ضخامت خط = تعداد تونل · رنگ = سلامت · کلیک روی خط = تونل‌های آن جفت',
      map_empty: 'هنوز تونلی نیست — با ویزارد توپولوژی بسازید.',
      iran_side: 'ایران', foreign_side: 'خارج',
      th_peer: 'طرف مقابل', th_engine: 'موتور', th_weight: 'وزن', th_share: 'سهم',
      th_today: 'امروز (سیم ↓/↑)', th_payload: 'بار (↓/↑)', th_state: 'وضعیت', th_latency: 'تأخیر',
      open_tunnel: 'جزئیات تونل',
      weights_hint: 'وزن‌ها برای هر تونل جداگانه است. سهم = وزن ÷ مجموع وزن‌های تونل‌های فعال همان سرور ایران؛ ایجنت‌ها دقیقاً همین سهم‌ها را بعد از push اعمال می‌کنند.',
      sum_lbl: 'Σ وزن‌ها', save_all: 'ذخیرهٔ همهٔ تغییرات', saved_one: 'تونل {k} ذخیره شد',
      enforced: 'نمای ایجنت', not_pushed: 'تغییرات هنوز push نشده',
      search: 'جستجو…', no_tunnels: 'در این توپولوژی تونلی نیست.',
      wizard_link: 'باز کردن ویزارد راه‌اندازی →',
      td_now: 'وضعیت فعلی', td_uptime: 'مدت کار', td_cpu: 'پردازندهٔ موتور', td_rss: 'حافظهٔ موتور',
      td_lat: 'تأخیر', td_loss: 'اتلاف', td_rate: 'نرخ لحظه‌ای',
      td_na: 'در دسترس نیست', td_na_kernel: 'برای موتورهای هسته‌ای در دسترس نیست',
      td_totals: 'مجموع هر جهت', td_today: 'امروز', td_billable: 'دانلود قابل‌قبض',
      td_ratios: 'نسبت‌ها', td_share: 'سهم از ترافیک توپولوژی (امروز)', td_overhead: 'سربار (سیم ÷ بار)',
      td_history: 'تاریخچهٔ ترافیک', td_events: 'تغییر وضعیت‌ها و خطاها', td_noevents: 'رویدادی ثبت نشده.',
      td_nodata: 'در این بازه هنوز ترافیکی ثبت نشده.',
      ev_state: 'وضعیت', ev_error: 'خطا', ev_from: 'از', ev_to: 'به',
      wire: 'سیم', payload: 'بار', down_dir: 'دانلود', up_dir: 'آپلود',
      rng_24h: '۲۴ ساعت',
      eng_artifacts: 'موتورهای امضاشده', eng_none: 'هنوز فایلی بارگذاری نشده.',
      eng_upload: 'بارگذاری فایل', eng_version: 'نسخه', eng_arch: 'معماری',
      eng_matrix_note: 'Matrix موتور پیش‌فرض و توصیه‌شده است. سایر موتورها آزمایشی هستند: کار می‌کنند اما پوشش پارامترها، رفتار سربار و حالت‌های خرابی‌شان کمتر آزموده شده است.',
      eng_default: 'پیش‌فرض / توصیه‌شده', eng_experimental: 'آزمایشی',
      al_rules: 'قواعد هشدار', al_open: 'هشدارهای باز', al_history: 'اعلان‌های اخیر',
      al_none: 'هیچ هشداری نیست — همه چیز آرام است.', al_enable: 'هشدارها فعال', al_offline: 'ایجنت آفلاین بعد از (دقیقه)',
      al_cert: 'هشدار انقضای گواهی (روز)', al_disk: 'حداکثر دیسک (%)', al_tg: 'توکن ربات تلگرام',
      al_chat: 'شناسه چت تلگرام', al_hook: 'آدرس وب‌هوک (http(s))', al_save: 'ذخیره تنظیمات',
      al_test: 'ارسال اعلان آزمایشی', al_test_ok: 'اعلان آزمایشی به', al_notif: 'مجری ارسال شد',
      al_no_conf: 'هیچ مجری ارسالی تنظیم نشده — ابتدا تلگرام یا وب‌هوک را پر کنید.',
      sec_agents: 'ایجنت‌ها', apply_h: 'اعمال پروفایل hardening', apply_n: 'اعمال پروفایل netopt',
      push_btn: 'ارسال وضعیت مطلوب', prof_hint: 'JSON زیر، پروفایلی است که دکمه‌ها روی ایجنت انتخاب‌شده اعمال می‌کنند. اعمال، تایمر ۱۲۰ ثانیه‌ای بازگردانی را فعال می‌کند؛ برای ماندگاری از نوار زرد تأیید بگیرید.',
      confirm: 'تأیید (حفظ تغییرات)', rollback: 'بازگردانی فوری',
      confirm_banner: 'حالت apply ایمن روی {agent} فعال شد — تا {s} ثانیه تأیید کنید، وگرنه عامل خودش بازمی‌گرداند.',
      need_json: 'JSON پروفایل نامعتبر است', pushed_ok: 'برای ایجنت ارسال شد',
      pg_connect: 'اتصال', pg_url: 'آدرس پنل', pg_user: 'کاربر ادمین', pg_pass: 'رمز ادمین',
      pg_dry: 'بررسی تغییرات', pg_apply: 'اعمال برنامه', pg_crosscheck: 'مقایسه مصرف',
      pg_coeff: 'ضریب پیشنهادی', pg_overhead: 'سربار موتور',
      backup_btn: 'دانلود پشتیبان رمزنگاری‌شده', backup_ask_pw: 'رمز پنل (تأیید دوباره):',
      backup_ask_pass: 'گذرواژهٔ پشتیبان (حداقل ۱۲ نویسه) — آن را ایمن نگه دارید؛ بدون آن پشتیبان خواندنی نیست:',
      backup_ok: 'پشتیبان رمزنگاری‌شده دانلود شد.', backup_denied: 'پشتیبان رد شد: رمز پنل نادرست است.',
      prefs: 'تنظیمات', calendar: 'تقویم', cal_jalali: 'جلالی (شمسی)', cal_greg: 'میلادی',
      lang_pref: 'زبان', legacy_note: 'صفحات قدیمیِ مدیریتیِ هاست',
      legacy_open: 'باز کردن صفحات قدیمی', legacy_gated: 'غیرفعال (با legacy_enabled در panel.json فعال می‌شود)',
      reload_note: 'صفحه دوباره بارگذاری می‌شود.',
      load_fail: 'بارگذاری این صفحه ناموفق بود', net_down: 'پنل در دسترس نیست — اتصال را بررسی کنید یا دوباره وارد شوید.',
      login_redirect: 'ورود', retry: 'تلاش دوباره', ok: 'تأیید', cancel: 'انصراف', copy: 'کپی جزئیات',
      copied: 'کپی شد', del: 'حذف', add: 'افزودن', saved: 'ذخیره شد', deleted: 'حذف شد',
      confirm_del: '«{n}» حذف شود؟ بازگشت ندارد.', err_details: 'جزئیات فنی',
      what_happened: 'چه اتفاقی افتاد', likely_cause: 'علت احتمالی', what_to_do: 'چه کاری باید بکنید',
      th_created: 'تاریخ ساخت'
    }
  };
  function t(k) { return (T[LANG] && T[LANG][k]) || T.en[k] || k; }

  // ---- API (contract errors carry ui.{title,what_happened,…} in both langs) --
  function base() { return location.pathname.replace(/\/mesh\/?$/, '/'); }
  function csrfToken() {
    var m = document.cookie.match(/(?:^|;\s*)gre_csrf=([^;]+)/);
    return m ? decodeURIComponent(m[1]) : '';
  }
  // apiErr: a normalized error carrying the structured ui block (or a plain
  // message when the response is not contract-shaped, e.g. the network died).
  function api(method, path, body) {
    var opt = { method: method, headers: { 'X-CSRF-Token': csrfToken() } };
    if (body !== undefined) {
      opt.headers['Content-Type'] = 'application/json';
      opt.body = JSON.stringify(body);
    }
    return fetch(base() + 'api/ui/' + path, opt).then(function (r) {
      if (r.status === 401) { location.href = base(); throw mkNetErr(t('login_redirect')); }
      return r.text().then(function (txt) {
        var j = null;
        try { j = txt ? JSON.parse(txt) : null; } catch (e) { /* non-JSON */ }
        if (!r.ok) {
          var err = new Error((j && j.error) || ('HTTP ' + r.status));
          err.ui = j && j.ui;
          err.status = r.status;
          throw err;
        }
        return j;
      });
    }).catch(function (e) {
      if (!e.status && !e.ui && !(e instanceof TypeError) === false) { /* TypeError = network */ }
      if (e instanceof TypeError) {
        throw mkNetErr(t('net_down'));
      }
      throw e;
    });
  }
  function mkNetErr(msg) { var e = new Error(msg); e.net = true; return e; }
  // uiErrText: the best human text for an error in the current language.
  function uiErrText(err) {
    if (err && err.ui) {
      var b = err.ui;
      return (b.title && (b.title[LANG] || b.title.en)) || err.message;
    }
    return (err && err.message) || String(err);
  }
  // uiErrCard: the structured, in-place error card (requirement 7).
  function uiErrCard(err, retryFn) {
    var u = err && err.ui;
    var div = document.createElement('div');
    div.className = 'errcard';
    var head = '<h3><span class="ecode">' + esc((u && u.code) || (err && err.net ? 'E-NET' : 'E-ERR')) + '</span>' +
      esc(u ? ((u.title && (u.title[LANG] || u.title.en)) || '') : t('load_fail')) + '</h3>';
    var body = '';
    if (u) {
      body += '<p><b>' + esc(t('what_happened')) + ':</b> ' + esc((u.what_happened && (u.what_happened[LANG] || u.what_happened.en)) || '') + '</p>';
      body += '<p class="ecause"><b>' + esc(t('likely_cause')) + ':</b> ' + esc((u.likely_cause && (u.likely_cause[LANG] || u.likely_cause.en)) || '') + '</p>';
      body += '<p class="edo"><b>' + esc(t('what_to_do')) + ':</b> ' + esc((u.what_to_do && (u.what_to_do[LANG] || u.what_to_do.en)) || '') + '</p>';
    } else {
      body += '<p>' + esc((err && err.message) || t('load_fail')) + '</p>';
    }
    var det = '';
    if (u && u.details && Object.keys(u.details).length) {
      det = '<details><summary>' + esc(t('err_details')) + '</summary><pre>' +
        esc(JSON.stringify(u.details, null, 2)) + '</pre></details>';
    } else if (!u) {
      det = '<details><summary>' + esc(t('err_details')) + '</summary><pre>' + esc(String(err && err.stack || err)) + '</pre></details>';
    }
    div.innerHTML = head + body + det +
      '<div class="efoot">' +
      (retryFn ? '<button class="act primary" data-retry>' + esc(t('retry')) + '</button>' : '') +
      '<button class="act" data-copy>' + esc(t('copy')) + '</button></div>';
    if (retryFn) div.querySelector('[data-retry]').onclick = retryFn;
    div.querySelector('[data-copy]').onclick = function () {
      var payload = u ? JSON.stringify({ code: u.code, title: u.title, what_happened: u.what_happened,
        likely_cause: u.likely_cause, what_to_do: u.what_to_do, details: u.details }, null, 2)
        : String(err && err.stack || err);
      navigator.clipboard.writeText(payload).then(function () { toast(t('copied'), 'ok'); });
    };
    return div;
  }
  // Show an error in a page container (card) + keep the message area clean.
  function showPageError(el, err, retryFn) {
    el.innerHTML = '';
    el.appendChild(uiErrCard(err, retryFn));
    if (err && err.net) showGlobalBanner(err.message);
  }

  // ---- toasts + confirm modal ------------------------------------------------
  function toast(msg, kind) {
    var box = document.getElementById('toasts');
    var el = document.createElement('div');
    el.className = 'toast ' + (kind || '');
    el.textContent = msg;
    box.appendChild(el);
    setTimeout(function () { el.remove(); }, 4600);
  }
  function toastErr(err) {
    var u = err && err.ui;
    if (u && u.what_to_do) {
      toast((u.title && (u.title[LANG] || u.title.en)) || err.message, 'err');
    } else {
      toast(uiErrText(err), 'err');
    }
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

  // ---- formatting helpers -----------------------------------------------------
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
  function num(v, digits) {
    if (v == null) return '—';
    return Number(v).toFixed(digits == null ? 1 : digits);
  }
  function pct(v) { return v == null ? '—' : num(v, 1) + '%'; }
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
  function dur(sec) {
    if (sec == null) return '—';
    var d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
    if (d > 0) return d + 'd ' + h + 'h';
    if (h > 0) return h + 'h ' + m + 'm';
    return m + 'm';
  }
  function statePill(s) {
    var cls = s === 'healthy' || s === 'up' || s === 'online' ? 'ok'
      : (s === 'degraded' ? 'warn' : (s === 'down' || s === 'offline' ? 'down' : 'dim'));
    var lbl = { healthy: t('healthy'), degraded: t('degraded'), down: t('down'), disabled: t('t_disabled'),
      unknown: t('td_na'), online: t('online'), offline: t('offline'), revoked: t('revoked') }[s] || s;
    return '<span class="pill ' + cls + '">' + esc(lbl) + '</span>';
  }
  function trunc(s, cls) {
    return '<span class="trunc ' + (cls || '') + '" title="' + esc(s) + '">' + esc(s) + '</span>';
  }
  function skeletons(n) {
    var h = '';
    for (var i = 0; i < (n || 3); i++) h += '<div class="skeleton"></div>';
    return h;
  }

  // ---- router (hash-based: #dash, #agents, #agent/{id}, #tunnel/{key}…) -----
  var PAGES = ['dash', 'topologies', 'agents', 'agent', 'tunnels', 'tunnel', 'engines',
    'alerts', 'security', 'pasarguard', 'settings', 'notfound'];
  var NAV_FOR = { dash: 'dash', topologies: 'topologies', agents: 'agents', agent: 'agents',
    tunnels: 'tunnels', tunnel: 'tunnels', engines: 'engines', alerts: 'alerts',
    security: 'security', pasarguard: 'pasarguard', settings: 'settings' };
  var route = { page: 'dash', id: '' };

  function parseHash() {
    var h = location.hash.replace(/^#\/?/, '');
    if (!h) return { page: 'dash', id: '' };
    var parts = h.split('/');
    if (PAGES.indexOf(parts[0]) < 0) return { page: 'notfound', id: h };
    return { page: parts[0], id: parts.slice(1).join('/') || '' };
  }
  function go(hash) { location.hash = hash; }
  function setRoute() {
    route = parseHash();
    var p = route.page;
    PAGES.forEach(function (id) {
      var sec = document.getElementById('page-' + id);
      if (sec) sec.hidden = id !== p;
    });
    var navKey = NAV_FOR[p] || p;
    Array.prototype.forEach.call(document.querySelectorAll('#nav button'), function (b) {
      b.classList.toggle('active', b.dataset.nav === navKey);
    });
    var titleEl = document.getElementById('page-title');
    var title = t('title_' + p);
    if (p === 'agent' || p === 'tunnel') {
      title = t('title_' + p) + ' ' + (route.id || '');
      titleEl.dir = 'ltr';
      titleEl.style.fontSize = 'var(--fs-h3)';
    } else {
      titleEl.dir = '';
      titleEl.style.fontSize = '';
    }
    titleEl.textContent = title;
    document.title = 'Quantum — ' + (p === 'dash' ? t('title_dash') : title);
    closeSidebar();
    render();
  }
  function render() {
    clearGlobalBanner();
    if (route.page === 'dash') renderDash();
    else if (route.page === 'topologies') route.id ? renderTopologyDetail(route.id) : renderTopologies();
    else if (route.page === 'agents') route.id ? renderAgentDetail(route.id) : renderAgents();
    else if (route.page === 'tunnels') renderTunnels();
    else if (route.page === 'tunnel') renderTunnelDetail(route.id);
    else if (route.page === 'engines') renderEngines();
    else if (route.page === 'alerts') renderAlerts();
    else if (route.page === 'security') renderSecurity();
    else if (route.page === 'pasarguard') renderPG();
    else if (route.page === 'settings') renderSettings();
    else if (route.page === 'notfound') {
      document.getElementById('page-notfound').innerHTML = '<div class="empty">' + esc(t('title_notfound')) + ' — ' +
        esc(route.id) + '</div><p><a class="btn primary" href="#dash">' + esc(t('nav_dash')) + '</a></p>';
    }
  }

  // ---- top bar: session (user, version, alerts bell), theme, lang, menu ------
  var session = null;
  function loadSession() {
    api('GET', 'session').then(function (s) {
      session = s;
      document.getElementById('um-user').textContent = s.user || '—';
      document.getElementById('um-version').textContent = s.panel_version || '';
      document.getElementById('side-version').textContent = s.panel_version || '';
      var n = s.alerts_open || 0;
      var bell = document.getElementById('bell-count');
      var navBadge = document.getElementById('nav-alerts');
      if (n > 0) {
        bell.hidden = false; bell.textContent = n;
        navBadge.hidden = false; navBadge.textContent = n;
      } else {
        bell.hidden = true; navBadge.hidden = true;
      }
    }).catch(function () { /* the page-level handlers show errors */ });
  }
  function showGlobalBanner(msg) {
    var ge = document.getElementById('global-err');
    ge.style.display = 'block';
    ge.innerHTML = '<b>' + esc(msg) + '</b> <a href="' + esc(base()) + '">' + esc(t('login_redirect')) + '</a>';
  }
  function clearGlobalBanner() {
    var ge = document.getElementById('global-err');
    ge.style.display = 'none'; ge.innerHTML = '';
  }
  function applyTheme() {
    var th = localStorage.getItem('gre_theme') || 'dark';
    document.documentElement.setAttribute('data-t', th);
    document.body.setAttribute('data-t', th);
    var m = document.querySelector('meta[name=theme-color]');
    if (m) m.setAttribute('content', th === 'light' ? '#f2f2f2' : '#09090b');
  }
  function applyLang() {
    document.documentElement.lang = LANG;
    document.documentElement.dir = LANG === 'fa' ? 'rtl' : 'ltr';
    document.getElementById('lang').textContent = LANG === 'fa' ? 'EN' : 'فا';
    Array.prototype.forEach.call(document.querySelectorAll('[data-i18n]'), function (el) {
      el.textContent = t(el.dataset.i18n);
    });
  }
  function openSidebar() { document.getElementById('side').classList.add('open'); document.getElementById('scrim').hidden = false; }
  function closeSidebar() { document.getElementById('side').classList.remove('open'); document.getElementById('scrim').hidden = true; }

  // ---- dashboard: aggregates only (contract §2.2) -----------------------------
  var overview = null;
  var trafficRange = 'today';
  var loadSeq = 0; // ignore stale loadAll completions

  function loadAll() {
    var seq = ++loadSeq;
    return api('GET', 'overview').then(function (o) {
      if (seq !== loadSeq) return;
      overview = o;
      if (route.page === 'dash') renderDash();
    }).catch(function (e) {
      if (seq !== loadSeq) return;
      if (e.net) showGlobalBanner(e.message);
    });
  }

  function renderDash() {
    var el = document.getElementById('page-dash');
    if (!overview) { el.innerHTML = skeletons(); return; }
    var c = overview.counts;
    var ag = c.agents;
    var tu = c.tunnels;
    el.innerHTML =
      '<div class="kpis">' +
      kpi('agents', num0(ag.online) + '<small>/' + num0(ag.online + ag.offline) + '</small>', t('k_agents'), ag.offline > 0 ? 'err' : 'ok') +
      kpi('topologies', num0(c.topologies.total), t('k_topos'), '') +
      kpi('tunnels', num0(tu.up) + '<small>/' + num0(tu.total) + '</small>', t('k_tunnels') + ' ' + t('t_up'),
        tu.down > 0 ? 'err' : (tu.degraded > 0 ? 'warn' : 'ok'),
        '<div class="ksub">' + esc(t('t_down')) + ': <b>' + num0(tu.down) + '</b> · ' + esc(t('t_degraded')) + ': <b>' + num0(tu.degraded) + '</b> · ' + esc(t('t_disabled')) + ': <b>' + num0(tu.disabled) + '</b></div>') +
      kpi('alerts', num0(overview.alerts.open), t('k_alerts'), overview.alerts.open > 0 ? 'warn' : 'ok') +
      kpi('pending', num0(overview.pending_confirmations), t('k_pending'), overview.pending_confirmations > 0 ? 'warn' : '') +
      kpiCert(overview.certificate) +
      '</div>' +
      '<div class="card"><h3>' + esc(t('k_res')) + '</h3><div class="aggr" id="res-aggr"></div></div>' +
      '<div class="card"><h3>' + esc(t('k_traffic')) + '</h3>' +
      '<div class="row"><div class="rng" id="trng">' +
      '<button data-r="today" class="' + (trafficRange === 'today' ? 'active' : '') + '">' + esc(t('rng_today')) + '</button>' +
      '<button data-r="7d" class="' + (trafficRange === '7d' ? 'active' : '') + '">' + esc(t('rng_7d')) + '</button>' +
      '<button data-r="30d" class="' + (trafficRange === '30d' ? 'active' : '') + '">' + esc(t('rng_30d')) + '</button></div>' +
      '<span class="muted">' + esc(t('cached_note')) + '</span></div>' +
      '<div id="dc-traffic"></div></div>' +
      '<div class="card"><h3>' + esc(t('checklist_title')) + '</h3><p class="muted">' + esc(t('checklist_sub')) + '</p>' +
      '<div class="check" id="checklist"></div></div>';
    el.querySelectorAll('.kpi[data-go]').forEach(function (b) {
      b.onclick = function () { go(b.dataset.go); };
    });
    renderResAggr();
    renderDcTraffic();
    renderChecklist();
    document.querySelectorAll('#trng button').forEach(function (b) {
      b.onclick = function () { trafficRange = b.dataset.r; renderDash(); };
    });
  }
  function num0(v) { return String(v == null ? 0 : v); }
  function kpi(hash, val, lbl, kind, sub) {
    return '<button class="kpi ' + (kind || '') + '" data-go="#' + hash + '">' +
      '<div class="kval">' + val + '</div><div class="klbl">' + esc(lbl) + '</div>' + (sub || '') + '</button>';
  }
  function kpiCert(cert) {
    var kind = cert.state === 'valid' ? 'ok' : (cert.state === 'expiring' ? 'warn' : (cert.state === 'expired' ? 'err' : ''));
    var days = cert.days_left;
    var lbl = cert.state === 'valid' ? t('cert_days').replace('{d}', days)
      : cert.state === 'expired' ? t('cert_expired_d').replace('{d}', Math.abs(days))
      : t('cert_' + (cert.state || 'unknown'));
    return '<button class="kpi ' + kind + '" data-go="#settings">' +
      '<div class="kval">' + esc(lbl) + '</div><div class="klbl">' + esc(t('k_cert')) + '</div></button>';
  }
  function meter(pctv) {
    var cls = pctv >= 90 ? 'err' : (pctv >= 70 ? 'warn' : '');
    return '<div class="meter"><i class="' + cls + '" style="width:' + Math.min(100, Math.max(2, pctv)) + '%"></i></div>';
  }
  function renderResAggr() {
    var box = document.getElementById('res-aggr');
    var r = overview.resources;
    if (!r || !r.agent_count) {
      box.innerHTML = '<div class="empty" style="grid-column:1/-1">' + esc(t('k_res')) + ': 0</div>';
      return;
    }
    var cpuMax = r.cpu_pct.max || {};
    var memMax = r.mem.max || {};
    var diskMax = r.disk.max || {};
    box.innerHTML =
      '<div class="kpi" data-go="#agents"><div class="kval">' + pct(r.cpu_pct.avg) + '</div><div class="klbl">' + esc(t('res_cpu')) + ' — ' + esc(t('res_avg')) + '</div>' +
      '<div class="ksub">' + esc(t('res_sum')) + ': <b>' + num(r.cpu_pct.sum, 0) + '%</b> · ' + esc(t('res_max')) + ': <b title="' + esc(cpuMax.name || '') + '">' + esc(cpuMax.name || '—') + ' ' + pct(cpuMax.value) + '</b></div>' + meter(r.cpu_pct.avg) + '</div>' +
      '<div class="kpi" data-go="#agents"><div class="kval">' + pct(r.mem.avg_pct) + '</div><div class="klbl">' + esc(t('res_mem')) + ' — ' + esc(t('res_avg')) + '</div>' +
      '<div class="ksub">' + esc(t('res_sum')) + ': <b>' + bytes(r.mem.sum_used_mb * 1024 * 1024) + '</b> · ' + esc(t('res_max')) + ': <b title="' + esc(memMax.name || '') + '">' + esc(memMax.name || '—') + ' ' + pct(memMax.value) + '</b></div>' + meter(r.mem.avg_pct) + '</div>' +
      '<div class="kpi" data-go="#agents"><div class="kval">' + pct(r.disk.avg_pct) + '</div><div class="klbl">' + esc(t('res_disk')) + ' — ' + esc(t('res_avg')) + '</div>' +
      '<div class="ksub">' + esc(t('res_sum')) + ': <b>' + bytes(r.disk.sum_used_gb * 1024 * 1024 * 1024) + '</b> · ' + esc(t('res_max')) + ': <b title="' + esc(diskMax.name || '') + '">' + esc(diskMax.name || '—') + ' ' + pct(diskMax.value) + '</b></div>' + meter(r.disk.avg_pct) + '</div>';
    box.querySelectorAll('[data-go]').forEach(function (b) { b.onclick = function () { go(b.dataset.go); }; });
  }
  function renderDcTraffic() {
    var box = document.getElementById('dc-traffic');
    var per = (overview.traffic && overview.traffic.per_datacenter) || [];
    if (!per.length) {
      box.innerHTML = '<div class="empty">' + esc(t('k_traffic')) + ': 0</div>';
      return;
    }
    var blk = overview.traffic[trafficRange] || {};
    var rows = per.map(function (d) {
      return '<tr><td>' + esc(d.name) + '</td>' +
        '<td class="num">' + bytes(d.billable_download_bytes) + '</td>' +
        '<td class="num">' + bytes(d.upload_bytes) + '</td>' +
        '<td class="num">' + bytes(d.payload_download_bytes) + '</td></tr>';
    }).join('');
    box.innerHTML =
      '<div class="chart"><div class="legend"><span><i class="bar-dl"></i>' + esc(t('th_billable')) + '</span>' +
      '<span><i class="bar-ul"></i>' + esc(t('th_upload')) + '</span></div>' +
      dcChart(per) + '</div>' +
      '<div class="tblwrap"><table><thead><tr><th>' + esc(t('th_dc')) + '</th>' +
      '<th class="num">' + esc(t('th_billable')) + '</th><th class="num">' + esc(t('th_upload')) + '</th>' +
      '<th class="num">' + esc(t('th_payload')) + '</th></tr></thead><tbody>' + rows + '</tbody></table></div>';
  }
  // Simple horizontal bar chart: billable download + upload per DC (SVG).
  function dcChart(per) {
    var W = 720, rowH = 34, barH = 9, labelW = 110;
    var H = per.length * rowH + 10;
    var max = 1;
    per.forEach(function (d) { max = Math.max(max, d.billable_download_bytes, d.upload_bytes); });
    var svg = '<svg class="chart" viewBox="0 0 ' + W + ' ' + H + '" role="img" aria-label="traffic">';
    per.forEach(function (d, i) {
      var y = i * rowH + 8;
      var wdl = Math.max(2, (W - labelW - 20) * (d.billable_download_bytes / max));
      var wul = Math.max(2, (W - labelW - 20) * (d.upload_bytes / max));
      svg += '<text x="' + (labelW - 8) + '" y="' + (y + barH) + '" text-anchor="end">' + esc(d.name) + '</text>';
      svg += '<rect class="bar-dl" x="' + labelW + '" y="' + y + '" width="' + wdl + '" height="' + barH + '" rx="4"><title>' + bytes(d.billable_download_bytes) + '</title></rect>';
      svg += '<rect class="bar-ul" x="' + labelW + '" y="' + (y + barH + 3) + '" width="' + wul + '" height="' + barH + '" rx="4"><title>' + bytes(d.upload_bytes) + '</title></rect>';
      svg += '<text x="' + Math.min(labelW + wdl + 6, W - 74) + '" y="' + (y + barH) + '">' + bytes(d.billable_download_bytes) + '</text>';
    });
    svg += '</svg>';
    return svg;
  }
  // First-run checklist: 8 steps with live status (requirement 12).
  function renderChecklist() {
    var box = document.getElementById('checklist');
    if (!box || !overview) return;
    var c = overview.counts;
    // live probes per step (no localStorage guesswork): weights → one boolean,
    // matrix-on → every ONLINE agent has matrix enabled, pg + alerts → status.
    var matrixOn = api('GET', 'agents').then(function (j) {
      var on = (j.agents || []).filter(function (a) { return a.status === 'online'; });
      if (!on.length) return false;
      return Promise.all(on.map(function (a) {
        return api('GET', 'agents/' + a.id + '/engines').then(function (e) {
          var m = (e.engines || []).filter(function (x) { return x.engine === 'matrix'; })[0];
          return !!(m && m.enabled);
        }).catch(function () { return false; });
      })).then(function (flags) { return flags.length > 0 && flags.every(Boolean); });
    }).catch(function () { return false; });
    var pgOn = api('GET', '../mesh/pasarguard/status').then(function (s) { return !!s.configured; })
      .catch(function () { return false; });
    var alertsOn = api('GET', '../mesh/alerts').then(function (a) { return !!a.enabled; })
      .catch(function () { return false; });
    var hasWeights = api('GET', 'weights/check').then(function (w) { return !!w.any_custom; })
      .catch(function () { return false; });
    Promise.all([matrixOn, pgOn, alertsOn, hasWeights]).then(function (r) {
      var steps = [
        { ok: c.datacenters.iran > 0, hash: '#agents', label: t('step1') },
        { ok: c.agents.online > 0, hash: '#agents', label: t('step2') },
        { ok: r[0], hash: '#agents', label: t('step3') },
        { ok: c.topologies.total > 0, hash: '#topologies', label: t('step4') },
        { ok: r[3], hash: '#tunnels', label: t('step5') },
        { ok: r[1], hash: '#pasarguard', label: t('step6') },
        { ok: r[2], hash: '#alerts', label: t('step7') },
        { ok: null, hash: '#settings', label: t('step8') }
      ];
      box.innerHTML = steps.map(function (s, i) {
        return '<button class="step' + (s.ok ? ' done' : '') + '" data-go="' + s.hash + '">' +
          '<span class="st-num">' + (s.ok ? '✓' : (i + 1)) + '</span>' + esc(s.label) +
          '<span class="st-st"><span class="pill ' + (s.ok === true ? 'ok' : (s.ok === false ? 'warn' : 'dim')) + '">' +
          esc(s.ok === true ? t('done') : (s.ok === false ? t('todo') : '—')) + '</span></span></button>';
      }).join('');
      box.querySelectorAll('[data-go]').forEach(function (b) { b.onclick = function () { go(b.dataset.go); }; });
    });
  }

  // ---- agents list (contract §2.3 + per-agent detail enrichment) --------------
  function renderAgents() {
    var el = document.getElementById('page-agents');
    el.innerHTML = skeletons();
    api('GET', 'agents').then(function (list) {
      var ags = list.agents || [];
      if (!ags.length) {
        el.innerHTML = '<div class="empty">' + esc(t('nav_agents')) + ': 0</div>' +
          '<p><a class="btn primary" href="' + esc(base()) + 'mesh-wizard">' + esc(t('wizard_link')) + '</a></p>';
        return;
      }
      // enrich: engines + topologies badges per agent (bounded, ≤ ~20 calls)
      return Promise.all(ags.map(function (a) {
        return Promise.all([
          api('GET', 'agents/' + a.id + '/engines').catch(function () { return { engines: [] }; }),
          api('GET', 'agents/' + a.id + '/topologies').catch(function () { return { topologies: [] }; }),
        ]).then(function (rs) {
          a.__engines = rs[0].engines || [];
          a.__topos = rs[1].topologies || [];
          return a;
        });
      })).then(function () {
        renderAgentsTable(el, ags);
      });
    }).catch(function (e) { showPageError(el, e, renderAgents); });
  }
  function renderAgentsTable(el, ags) {
    var rows = ags.map(function (a) {
      var en = (a.__engines || []).filter(function (e) { return e.enabled; }).length;
      var run = (a.__engines || []).reduce(function (n, e) { return n + (e.instances || 0); }, 0);
      var topos = (a.__topos || []).map(function (tp) {
        return '<a class="pill dim" href="#agent/' + esc(a.id) + '" title="' + esc(tp.name) + '">' + esc(tp.name) + '</a>';
      }).join(' ');
      var sv = a.server || {};
      var cpu = a.cpu ? a.cpu.percent : null;
      var mem = (a.mem && a.mem.total_mb) ? (a.mem.used_mb / a.mem.total_mb * 100) : null;
      return '<tr data-id="' + esc(a.id) + '" class="rowlink">' +
        '<td>' + statePill(a.status) + '</td>' +
        '<td><a href="#agent/' + esc(a.id) + '"><b>' + esc(a.name || a.id) + '</b></a><br><span class="muted mono">' + esc(a.id) + '</span></td>' +
        '<td>' + esc(a.role) + '</td>' +
        '<td>' + esc(sv.datacenter || '—') + '</td>' +
        '<td>' + (topos || '—') + '</td>' +
        '<td class="num">' + en + ' / ' + run + '</td>' +
        '<td class="num">' + pct(cpu) + '</td>' +
        '<td class="num">' + pct(mem) + '</td>' +
        '<td class="muted">' + fmtDate(a.last_seen) + '</td></tr>';
    }).join('');
    el.innerHTML =
      '<div class="row"><span class="muted">' + ags.length + ' × ' + esc(t('nav_agents')) + '</span><span class="spacer"></span>' +
      '<a class="btn primary small" href="' + esc(base()) + 'mesh-wizard">' + esc(t('wizard_link')) + '</a></div>' +
      '<div class="tblwrap"><table id="agents-tbl"><thead><tr>' +
      '<th>' + esc(t('th_state')) + '</th><th>' + esc(t('th_agent')) + '</th><th>' + esc(t('th_role')) + '</th>' +
      '<th>' + esc(t('th_dc')) + '</th><th>' + esc(t('th_topos')) + '</th>' +
      '<th class="num">' + esc(t('th_engines')) + '</th><th class="num">' + esc(t('th_cpu')) + '</th>' +
      '<th class="num">' + esc(t('th_mem')) + '</th><th>' + esc(t('th_seen')) + '</th></tr></thead>' +
      '<tbody>' + rows + '</tbody></table></div>';
    wireTableTools('agents-tbl', el, ['th_agent', 'th_state', 'th_role', 'th_dc', 'th_cpu', 'th_mem', 'th_seen']);
  }

  // ---- agent detail (contract §2.4/2.5/2.6/2.7) --------------------------------
  function renderAgentDetail(id) {
    var el = document.getElementById('page-agent');
    el.innerHTML = skeletons();
    Promise.all([
      api('GET', 'agents/' + id),
      api('GET', 'agents/' + id + '/engines'),
      api('GET', 'agents/' + id + '/topologies'),
    ]).then(function (rs) {
      drawAgentDetail(el, id, rs[0], rs[1], rs[2]);
    }).catch(function (e) { showPageError(el, e, function () { renderAgentDetail(id); }); });
  }
  function drawAgentDetail(el, id, det, eng, topos) {
    var a = det.agent;
    document.getElementById("page-title").textContent = t("title_agent") + " " + (a.name || id);
    document.title = "Quantum — " + (a.name || id);
    var memPct = a.mem && a.mem.total_mb ? (a.mem.used_mb / a.mem.total_mb * 100) : null;
    var diskPct = a.disk && a.disk.total_gb ? (a.disk.used_gb / a.disk.total_gb * 100) : null;
    el.innerHTML =
      '<div class="card"><h3>' + esc(t('agent_info')) + '</h3><div class="aggr">' +
      '<div class="kpi"><div class="kval">' + statePill(a.status) + '</div><div class="klbl">' + esc(t('th_state')) + '</div>' +
      '<div class="ksub">' + esc(t('ver_lbl')) + ': <b>' + esc(a.version || '—') + '</b> · ' + esc(t('arch_lbl')) + ': <b>' + esc(a.arch || '—') + '</b> · ' + esc(t('os_lbl')) + ': <b>' + esc(a.os || '—') + '</b></div></div>' +
      '<div class="kpi"><div class="kval">' + pct(a.cpu ? a.cpu.percent : null) + '</div><div class="klbl">' + esc(t('res_cpu')) + '</div>' +
      '<div class="ksub">' + esc(a.cpu ? a.cpu.model || '' : '') + ' · ' + num0(a.cpu ? a.cpu.cores : null) + ' cores · load ' + num(a.cpu ? a.cpu.load1 : null, 2) + '</div>' + meter(a.cpu ? a.cpu.percent : 0) + '</div>' +
      '<div class="kpi"><div class="kval">' + pct(memPct) + '</div><div class="klbl">' + esc(t('res_mem')) + '</div>' +
      '<div class="ksub">' + esc(t('mem_use').replace('{u}', num0(a.mem ? a.mem.used_mb : null)).replace('{t}', num0(a.mem ? a.mem.total_mb : null))) + '</div>' + meter(memPct || 0) + '</div>' +
      '<div class="kpi"><div class="kval">' + pct(diskPct) + '</div><div class="klbl">' + esc(t('res_disk')) + '</div>' +
      '<div class="ksub">' + num0(a.disk ? a.disk.used_gb : null) + ' / ' + num0(a.disk ? a.disk.total_gb : null) + ' GB</div>' + meter(diskPct || 0) + '</div>' +
      '<div class="kpi"><div class="kval">' + esc(dur(a.uptime_s)) + '</div><div class="klbl">' + esc(t('uptime_lbl')) + '</div>' +
      '<div class="ksub">' + esc(t('host_lbl')) + ': <b>' + esc(a.hostname || '—') + '</b> · virt: ' + esc(a.virt || '—') + '</div></div>' +
      '<div class="kpi"><div class="kval mono" style="font-size:1rem">' + esc(a.effective_public_ip || '—') + '</div><div class="klbl">' + esc(t('ip_lbl')) + ' ' + (a.public_ip_override ? esc(t('ip_ovr')) : '') + '</div>' +
      '<div class="ksub"><input id="ag-ip" value="' + esc(a.public_ip_override || '') + '" placeholder="' + esc(t('ip_hint')) + '" style="width:100%">' +
      '<button class="act small" id="ag-ip-save" style="margin-top:4px">' + esc(t('ip_save')) + '</button></div></div>' +
      '<div class="kpi"><div class="kval mono" style="font-size:.9rem">' + trunc(a.applied_revision || '—') + '</div><div class="klbl">' + esc(t('rev_lbl')) + '</div>' +
      '<div class="ksub">' + esc(t('th_seen')) + ': ' + fmtDate(a.last_seen) + '</div></div>' +
      '</div></div>' +
      '<div class="card"><h3>' + esc(t('agent_topos')) + '</h3><div id="ag-topos"></div></div>' +
      '<div class="card"><h3>' + esc(t('agent_engines')) + '</h3>' +
      '<p class="muted">' + esc(t('matrix_default')) + '</p><div id="ag-engines"></div></div>' +
      '<div class="card"><h3>' + esc(t('recent_errors')) + '</h3><div id="ag-errors"></div></div>';
    // memberships
    var tp = topos.topologies || [];
    document.getElementById('ag-topos').innerHTML = tp.length
      ? '<div class="tblwrap"><table><thead><tr><th>' + esc(t('th_topology')) + '</th><th>' + esc(t('th_myrole')) + '</th>' +
        '<th>' + esc(t('th_tunnels')) + '</th><th>' + esc(t('th_enabled')) + '</th></tr></thead><tbody>' +
        tp.map(function (x) {
          return '<tr><td><a href="#topologies/' + x.id + '"><b>' + esc(x.name) + '</b></a></td>' +
            '<td>' + esc(x.my_role) + '</td><td class="num">' + num0(x.tunnel_count) + '</td><td>' +
            (x.enabled ? '<span class="pill ok">✓</span>' : '<span class="pill dim">—</span>') + '</td></tr>';
        }).join('') + '</tbody></table></div>'
      : '<div class="empty">' + esc(t('no_topos')) + '</div>';
    // engines
    drawAgentEngines(id, eng.engines || []);
    // recent errors
    var errs = det.recent_errors || [];
    document.getElementById('ag-errors').innerHTML = errs.length
      ? errs.map(function (e) {
        return '<div class="row"><span class="pill warn mono">' + esc(e.code) + '</span>' +
          '<span class="muted">' + fmtDate(e.at) + '</span><span>' + esc(e.text) + '</span></div>';
      }).join('')
      : '<div class="empty">' + esc(t('no_errors')) + '</div>';
    // public IP save
    document.getElementById('ag-ip-save').onclick = function () {
      var v = document.getElementById('ag-ip').value.trim();
      api('POST', 'agents/' + id + '/public-ip', { public_ip: v })
        .then(function () { toast(t('saved'), 'ok'); renderAgentDetail(id); })
        .catch(function (e) { toastErr(e); });
    };
  }
  function drawAgentEngines(agentId, engines) {
    var box = document.getElementById('ag-engines');
    if (!box) return;
    box.innerHTML = '<div class="tblwrap"><table><thead><tr>' +
      '<th>' + esc(t('th_engine')) + '</th><th>' + esc(t('th_installed')) + '</th><th>' + esc(t('th_enabled')) + '</th>' +
      '<th>' + esc(t('th_running')) + '</th><th>' + esc(t('th_health')) + '</th><th>' + esc(t('th_lasterr')) + '</th><th></th></tr></thead><tbody>' +
      engines.map(function (e) {
        var tag = e.engine === 'matrix'
          ? ' <span class="pill info">' + esc(t('eng_default')) + '</span>'
          : ' <span class="pill dim">' + esc(t('eng_experimental')) + '</span>';
        var inst = e.installed
          ? '<span class="pill ok">' + esc(t('installed')) + (e.version ? ' · ' + esc(e.version) : '') + '</span>' +
            (e.kernel ? '<br><span class="muted">' + esc(t('kernel_note')) + '</span>'
              : (e.bytes_on_disk != null ? '<br><span class="muted">' + esc(t('bytes_disk').replace('{b}', bytes(e.bytes_on_disk))) + '</span>' : ''))
          : '<span class="pill dim">' + esc(t('not_installed')) + '</span>';
        var run = e.instances > 0 ? esc(t('running').replace('{n}', num0(e.instances))) : esc(t('stopped'));
        var canBtn = e.engine === 'matrix' ? '' :
          '<button class="act small ' + (e.enabled ? 'danger' : 'primary') + '" data-eng="' + esc(e.engine) + '" data-on="' + (e.enabled ? '0' : '1') + '"' +
          (e.enabled ? '' : (e.can_enable === false ? ' disabled title="' + esc(e.block_reason || t('block_no_artifact')) + '"' : '')) + '>' +
          esc(e.enabled ? t('disable') : t('enable')) + '</button>';
        return '<tr><td><b>' + esc(e.engine) + '</b>' + tag + '</td>' +
          '<td>' + inst + '</td>' +
          '<td>' + (e.enabled ? '<span class="pill ok">✓</span>' : '<span class="pill dim">—</span>') + '</td>' +
          '<td class="num">' + run + '</td>' +
          '<td>' + (e.health ? statePill(e.health) : '<span class="pill dim">—</span>') + '</td>' +
          '<td class="trunc" style="max-width:220px" title="' + esc(e.last_error || '') + '">' + esc(e.last_error || '—') + '</td>' +
          '<td>' + canBtn + '</td></tr>';
      }).join('') + '</tbody></table></div>';
    box.querySelectorAll('[data-eng]').forEach(function (b) {
      b.onclick = function () {
        var eng = b.dataset.eng;
        var on = b.dataset.on === '1';
        confirmModal(
          (on ? t('enable') : t('disable')) + ': ' + eng,
          (on ? t('enable_q') : t('disable_q')).replace('{e}', eng).replace('{a}', agentId),
          on ? t('enable') : t('disable'), !on
        ).then(function (yes) {
          if (!yes) return;
          api('POST', 'agents/' + agentId + '/engines/' + eng + '/' + (on ? 'enable' : 'disable'))
            .then(function (r) {
              toast(((on ? t('enabled_ok') : t('disabled_ok')).replace('{e}', eng)).replace('{a}', agentId) +
                (r.queued ? ' (queued)' : ''), 'ok');
              renderAgentDetail(agentId);
            })
            .catch(function (e) {
              // the structured error card, right here where it happened (req 7)
              var box = document.getElementById('ag-engines');
              var card = uiErrCard(e);
              box.insertBefore(card, box.firstChild);
              toastErr(e);
            });
        });
      };
    });
  }

  // ---- topologies list + detail (contract §2.8) ---------------------------------
  function renderTopologies() {
    var el = document.getElementById('page-topologies');
    el.innerHTML = skeletons();
    api('GET', 'overview').then(function (ov) {
      // the overview carries counts only; enumerate topologies via details
      // is N calls — the list view reuses the mesh overview (light).
      return api('GET', '../mesh/topologies').then(function (list) {
        return { list: (list && list.topologies) || [], ov: ov };
      });
    }).catch(function () { return { list: [], ov: overview }; }).then(function (r) {
      var topos = r.list;
      if (!topos.length) {
        el.innerHTML = '<div class="empty">' + esc(t('map_empty')) + '</div>' +
          '<p><a class="btn primary" href="' + esc(base()) + 'mesh-wizard">' + esc(t('wizard_link')) + '</a></p>';
        return;
      }
      // each topology: summary strip (detail call gives totals — do it lazily)
      el.innerHTML = '<div class="row"><span class="muted">' + num0(topos.length) + ' × ' + esc(t('th_topology')) + '</span>' +
        '<span class="spacer"></span><a class="btn primary small" href="' + esc(base()) + 'mesh-wizard">' + esc(t('wizard_link')) + '</a></div>' +
        '<div id="topo-cards">' + topos.map(function (tp) {
          return '<div class="card" data-id="' + tp.id + '"><div class="row"><h3 style="margin:0"><a href="#topologies/' + tp.id + '"><b>' + esc(tp.name) + '</b></a></h3>' +
            '<span class="spacer"></span>' + (tp.enabled ? '<span class="pill ok">✓ ' + esc(t('th_enabled')) + '</span>' : '<span class="pill dim">—</span>') + '</div>' +
            '<div class="muted">' + esc(t('th_members')) + ': ' + (tp.members || []).length + ' · ' + esc(t('th_tunnels')) + ': ' + num0(tp.tunnel_count) + '</div>' +
            '<div class="toposum" data-id="' + tp.id + '">' + skeletons(1) + '</div></div>';
        }).join('') + '</div>';
      topos.forEach(function (tp) {
        api('GET', 'topologies/' + tp.id).then(function (d) {
          var box = el.querySelector('.toposum[data-id="' + tp.id + '"]');
          if (!box) return;
          var tt = d.totals.tunnels;
          var tr = d.totals.traffic_today;
          box.innerHTML = '<div class="row">' +
            '<span class="pill ' + (tt.down > 0 ? 'down' : (tt.degraded > 0 ? 'warn' : 'ok')) + '">' +
            esc(tt.up + ' ' + t('t_up')) + ' · ' + esc(tt.down + ' ' + t('t_down')) + (tt.degraded ? ' · ' + esc(tt.degraded + ' ' + t('t_degraded')) : '') + '</span>' +
            '<span class="muted">' + esc(t('th_traffic_today')) + ': <b class="num">' + bytes(tr.billable_download_bytes) + '</b> ↓ / <b class="num">' + bytes(tr.upload_bytes) + '</b> ↑</span>' +
            '<span class="spacer"></span><a class="act small" href="#topologies/' + tp.id + '">' + esc(t('open_tunnel')) + ' →</a></div>';
        }).catch(function () {});
      });
    });
  }

  function renderTopologyDetail(id) {
    var el = document.getElementById('page-topologies');
    el.innerHTML = skeletons();
    api('GET', 'topologies/' + id).then(function (d) {
      var tt = d.totals.tunnels;
      var tr = d.totals.traffic_today;
      var html =
        '<div class="kpis">' +
        kpi('topologies', num0(tt.up) + '<small>/' + num0(tt.total) + '</small>', t('k_tunnels') + ' — ' + t('t_up'), tt.down > 0 ? 'err' : 'ok',
          '<div class="ksub">' + esc(t('t_down')) + ': <b>' + num0(tt.down) + '</b> · ' + esc(t('t_degraded')) + ': <b>' + num0(tt.degraded) + '</b></div>') +
        kpi('', bytes(tr.billable_download_bytes), t('th_billable') + ' — ' + t('rng_today'), '') +
        kpi('', bytes(tr.upload_bytes), t('th_upload') + ' — ' + t('rng_today'), '') +
        '</div>' +
        '<div class="card"><h3>' + esc(t('map_title')) + '</h3><p class="muted">' + esc(t('map_hint')) + '</p><div id="mapbox"></div></div>' +
        '<div id="per-server"></div>';
      el.innerHTML = html;
      drawMapNew(d);
      renderPerServer(d);
    }).catch(function (e) { showPageError(el, e, function () { renderTopologyDetail(id); }); });
  }

  function renderPerServer(d) {
    var box = document.getElementById('per-server');
    var section = function (title, list) {
      if (!list.length) return '';
      return '<h3 style="margin:14px 0 6px">' + esc(title) + '</h3>' + list.map(function (sv) {
        var rows = (sv.tunnels || []).map(function (tn) {
          var tt = tn.traffic_today || {};
          return '<tr class="rowlink" data-key="' + esc(tn.tunnel_key) + '">' +
            '<td class="trunc mono" style="max-width:210px" title="' + esc(tn.tunnel_key) + '">' + esc(tn.tunnel_key) + '</td>' +
            '<td>' + esc(tn.engine) + (tn.engine === 'matrix' ? ' <span class="pill info">' + esc(t('eng_default')) + '</span>' : '') + '</td>' +
            '<td>' + statePill(tn.state) + '</td>' +
            '<td class="num">' + num0(tn.weight) + '</td>' +
            '<td class="num">' + pct(tn.resolved_percent) + '</td>' +
            '<td class="num">' + bytes(tt.wire_rx_bytes) + ' / ' + bytes(tt.wire_tx_bytes) + '</td>' +
            '<td class="num">' + bytes(tt.payload_rx_bytes) + ' / ' + bytes(tt.payload_tx_bytes) + '</td>' +
            '<td class="num">' + (tn.latency_ms != null ? num(tn.latency_ms, 0) + ' ms' : '—') + '</td></tr>';
        }).join('');
        return '<div class="card"><h3>' + esc(sv.name) + ' ' + (sv.agent ? statePill(sv.agent.status) : '') + '</h3>' +
          '<div class="tblwrap"><table><thead><tr><th>' + esc(t('tunnel')) + '</th><th>' + esc(t('th_engine')) + '</th>' +
          '<th>' + esc(t('th_state')) + '</th><th class="num">' + esc(t('th_weight')) + '</th><th class="num">' + esc(t('th_share')) + '</th>' +
          '<th class="num">' + esc(t('th_today')) + '</th><th class="num">' + esc(t('th_payload')) + '</th>' +
          '<th class="num">' + esc(t('th_latency')) + '</th></tr></thead><tbody>' + rows + '</tbody></table></div></div>';
      }).join('');
    };
    box.innerHTML = section(t('iran_side'), d.members.iran) + section(t('foreign_side'), d.members.foreign);
    box.querySelectorAll('tr.rowlink').forEach(function (tr) {
      tr.onclick = function () { go('#tunnel/' + tr.dataset.key); };
    });
  }

  // The SVG map from the CONTRACT data: members carry the tunnels.
  function drawMapNew(d) {
    var box = document.getElementById('mapbox');
    if (!box) return;
    var allT = d.tunnels || [];
    if (!allT.length) { box.innerHTML = '<div class="empty">' + esc(t('map_empty')) + '</div>'; return; }
    var pairs = {};
    allT.forEach(function (tn) {
      var k = tn.local_server.id + '>' + tn.remote_server.id;
      var p = pairs[k] = pairs[k] || { local: tn.local_server, remote: tn.remote_server, n: 0, up: 0, down: 0, deg: 0 };
      p.n += 1;
      if (tn.state === 'healthy') p.up += 1;
      else if (tn.state === 'degraded') p.deg += 1;
      else if (tn.state !== 'disabled') p.down += 1;
    });
    var iranList = d.members.iran, foreignList = d.members.foreign;
    var rowH = 38, pad = 18, W = 780, leftBoxW = 240, rightBoxW = 170;
    var leftN = iranList.reduce(function (n, s) { return n + (s.tunnels || []).length ? Math.max(1, Math.ceil((s.tunnels || []).length / 3)) : 1; }, 0);
    var H = Math.max(iranList.length, foreignList.length) * rowH + pad * 2 + 30;
    var srvY = {}, fy = pad + 26, fgnY = {};
    var y = pad + 26;
    iranList.forEach(function (s) { srvY[s.id] = y + rowH / 2 - 8; y += rowH; });
    y = pad + 26;
    foreignList.forEach(function (s) { fgnY[s.id] = y + rowH / 2 - 8; y += rowH; });
    var svg = '<svg class="topomap" viewBox="0 0 ' + W + ' ' + H + '" role="img" aria-label="' + esc(t('map_title')) + '">';
    var dcY = {};
    var drawnDC = {};
    y = pad + 26;
    iranList.forEach(function (s) {
      var did = s.datacenter_id || s.id;
      if (!drawnDC[did]) { drawnDC[did] = { y0: y - 8, y1: y + rowH - 8, name: s.datacenter || '' }; }
      else { drawnDC[did].y1 = y + rowH - 8; }
      y += rowH;
    });
    Object.keys(drawnDC).forEach(function (did, i) {
      var b = drawnDC[did];
      svg += '<rect class="dcbox" x="4" y="' + b.y0 + '" width="' + leftBoxW + '" height="' + (b.y1 - b.y0 + 8) + '" rx="10"/>';
      if (b.name) svg += '<text class="dclbl" x="' + (leftBoxW - 8) + '" y="' + (b.y0 + 14) + '" text-anchor="end">' + esc(b.name) + '</text>';
    });
    iranList.forEach(function (s) {
      var cy = srvY[s.id];
      svg += '<rect class="svnode" x="14" y="' + (cy - 12) + '" width="' + (leftBoxW - 36) + '" height="24" rx="7"/>' +
        '<text x="24" y="' + (cy + 4) + '">' + esc(s.name) + '</text>';
    });
    foreignList.forEach(function (s) {
      var cy = fgnY[s.id];
      svg += '<rect class="svnode fgn" x="' + (W - rightBoxW - 14) + '" y="' + (cy - 12) + '" width="' + (rightBoxW - 20) + '" height="24" rx="7"/>' +
        '<text x="' + (W - rightBoxW - 4) + '" y="' + (cy + 4) + '" text-anchor="end">' + esc(s.name) + '</text>';
    });
    var x1 = leftBoxW - 16, x2 = W - rightBoxW - 14;
    var maxN = 1;
    Object.keys(pairs).forEach(function (k) { maxN = Math.max(maxN, pairs[k].n); });
    var i = 0;
    Object.keys(pairs).forEach(function (k) {
      var p = pairs[k];
      var yA = srvY[p.local.id] || H / 2, yB = fgnY[p.remote.id] || H / 2;
      var cls = 'link';
      if (p.down === p.n) cls += ' down-all';
      else if (p.down > 0 || p.deg > 0) cls += ' part';
      var wpx = 1.6 + 3.2 * (p.n / maxN);
      var mx = (x1 + x2) / 2 + ((i % 5) - 2) * 14;
      svg += '<path class="' + cls + '" d="M' + x1 + ' ' + yA + ' C ' + mx + ' ' + yA + ', ' + mx + ' ' + yB + ', ' + x2 + ' ' + yB + '" ' +
        'stroke-width="' + wpx.toFixed(1) + '" fill="none" opacity="0.85" data-pair="' + esc(k) + '">' +
        '<title>' + esc(p.n + ' · ' + p.up + ' up · ' + p.down + ' down') + '</title></path>';
      svg += '<text class="linklbl" x="' + ((x1 + x2) / 2) + '" y="' + ((yA + yB) / 2 - 6) + '" text-anchor="middle">' + p.n + '</text>';
      i++;
    });
    svg += '</svg>';
    box.innerHTML = svg;
    box.querySelectorAll('[data-pair]').forEach(function (path) {
      path.addEventListener('click', function () { showPairTunnelsNew(d, path.dataset.pair); });
    });
  }
  function showPairTunnelsNew(d, pairKey) {
    var parts = pairKey.split('>');
    var li = Number(parts[0]), ri = Number(parts[1]);
    var rows = (d.tunnels || []).filter(function (tn) {
      return tn.local_server.id === li && tn.remote_server.id === ri;
    }).map(function (tn) {
      return '<tr><td class="mono">' + esc(tn.tunnel_key) + '</td><td>' + esc(tn.engine) + '</td><td>' + statePill(tn.state) + '</td>' +
        '<td class="num">' + num0(tn.weight) + '</td><td class="num">' + pct(tn.resolved_percent) + '</td></tr>';
    }).join('');
    var root = document.getElementById('modal-root');
    root.innerHTML = '<div class="modal-scrim"><div class="modal" role="dialog" aria-modal="true"><h3>' + esc(t('pair_tunnels')) + '</h3>' +
      '<table><tr><th>' + esc(t('tunnel')) + '</th><th>' + esc(t('th_engine')) + '</th><th>' + esc(t('th_state')) + '</th>' +
      '<th class="num">' + esc(t('th_weight')) + '</th><th class="num">' + esc(t('th_share')) + '</th></tr>' + rows + '</table>' +
      '<div class="mfoot"><button class="act primary" id="m-ok">' + esc(t('ok')) + '</button></div></div></div>';
    root.querySelector('#m-ok').onclick = function () { root.innerHTML = ''; };
    root.querySelector('.modal-scrim').addEventListener('click', function (e) {
      if (e.target.classList.contains('modal-scrim')) root.innerHTML = '';
    });
  }

  // ---- tunnels & weights (per-tunnel editor on the contract endpoint) ----------
  var weightsTopo = null;
  function renderTunnels() {
    var el = document.getElementById('page-tunnels');
    el.innerHTML = skeletons();
    api('GET', 'overview').then(function (ov) {
      return Promise.resolve(overview ? (overview.topologies || []) : [])
    }).then(function (topos) {
      if (!topos.length) {
        el.innerHTML = '<div class="empty">' + esc(t('no_tunnels')) + '</div>' +
          '<p><a class="btn primary" href="' + esc(base()) + 'mesh-wizard">' + esc(t('wizard_link')) + '</a></p>';
        return;
      }
      if (!weightsTopo) weightsTopo = topos[0].id;
      el.innerHTML = '<div class="card"><div class="row"><label for="wtopo">' + esc(t('th_topology')) + ':</label><select id="wtopo">' +
        topos.map(function (tp) {
          return '<option value="' + tp.id + '"' + (weightsTopo == tp.id ? ' selected' : '') + '>' + esc(tp.name) + '</option>';
        }).join('') + '</select>' +
        '<span class="spacer"></span><span class="muted" id="w-enforced"></span></div>' +
        '<p class="muted">' + esc(t('weights_hint')) + '</p>' +
        '<div id="wbody">' + skeletons() + '</div></div>';
      document.getElementById('wtopo').addEventListener('change', function (e) {
        weightsTopo = Number(e.target.value); loadWeights();
      });
      loadWeights();
    });
  }
  function loadWeights() {
    var body = document.getElementById('wbody');
    if (!body) return;
    api('GET', 'topologies/' + weightsTopo).then(function (d) {
      var rows = (d.tunnels || []).map(function (tn) {
        return '<tr data-key="' + esc(tn.tunnel_key) + '">' +
          '<td class="mono trunc" style="max-width:230px" title="' + esc(tn.tunnel_key) + '">' + esc(tn.tunnel_key) + '</td>' +
          '<td>' + esc(tn.engine) + '</td>' +
          '<td>' + statePill(tn.state) + '</td>' +
          '<td><input type="number" min="0" max="1000" value="' + num0(tn.weight) + '" data-key="' + esc(tn.tunnel_key) + '" style="width:84px" aria-label="' + esc(t('th_weight')) + ' ' + esc(tn.tunnel_key) + '"></td>' +
          '<td class="wshare num">' + pct(tn.resolved_percent) + '</td>' +
          '<td class="num">' + bytes((tn.traffic_today || {}).wire_rx_bytes) + '</td>' +
          '<td><button class="act small" data-save="' + esc(tn.tunnel_key) + '">' + esc(t('save')) + '</button></td></tr>';
      }).join('');
      document.getElementById('w-enforced').innerHTML = '<span class="pill ' + (anyApplied() ? 'ok' : 'warn') + '">' +
        esc(t('enforced')) + (anyApplied() ? ' ✓' : ' — ' + esc(t('not_pushed'))) + '</span>';
      body.innerHTML = '<div class="row"><input id="wsearch" type="search" placeholder="' + esc(t('search')) + '" style="width:220px">' +
        '<span class="spacer"></span><span class="wsum" id="wsum"></span>' +
        '<button class="act primary" id="wsaveall">' + esc(t('save_all')) + '</button></div>' +
        '<div class="tblwrap"><table id="w-tbl"><thead><tr><th>' + esc(t('tunnel')) + '</th><th>' + esc(t('th_engine')) + '</th>' +
        '<th>' + esc(t('th_state')) + '</th><th>' + esc(t('th_weight')) + '</th><th class="num">' + esc(t('th_share')) + '</th>' +
        '<th class="num">' + esc(t('th_today')) + '</th><th></th></tr></thead><tbody>' + rows + '</tbody></table></div>';
      wireTableTools('w-tbl', document.getElementById('page-tunnels'), []);
      updateWSum(d.tunnels || []);
      body.querySelectorAll('input[data-key]').forEach(function (inp) {
        inp.addEventListener('input', function () {
          var current = currentWeights();
          updateWSum(current);
        });
      });
      body.addEventListener('click', function (e) {
        var b = e.target.closest('button[data-save]');
        if (!b) return;
        saveWeight(b.dataset.save);
      });
      document.getElementById('wsaveall').onclick = function () {
        var current = currentWeights();
        var chain = Promise.resolve();
        var n = 0;
        Object.keys(current).forEach(function (key) {
          chain = chain.then(function () {
            return api('POST', 'topologies/' + weightsTopo + '/tunnels/' + encodeURIComponent(key) + '/weight', { weight: current[key] })
              .then(function () { n++; });
          });
        });
        chain.then(function () { toast(t('saved') + ' ×' + n, 'ok'); loadWeights(); })
          .catch(function (e) { toastErr(e); });
      };
    }).catch(function (e) { showPageError(body, e, loadWeights); });
  }
  function anyApplied() { return localStorage.getItem('weights_pushed_' + weightsTopo) === '1'; }
  function currentWeights() {
    var out = {};
    document.querySelectorAll('#wbody input[data-key]').forEach(function (inp) {
      out[inp.dataset.key] = Math.max(0, parseInt(inp.value, 10) || 0);
    });
    return out;
  }
  function updateWSum(tunnels) {
    var cur = currentWeights();
    var sum = 0, total = 0;
    document.querySelectorAll('#wbody input[data-key]').forEach(function (inp) {
      sum += parseInt(inp.value, 10) || 0;
    });
    var ws = document.getElementById('wsum');
    if (ws) {
      ws.textContent = t('sum_lbl') + ': ' + sum;
      ws.className = 'wsum ' + (sum === 100 ? 'good' : 'bad');
    }
    // refresh share cells client-side (the backend formula: weight / Σ per local server)
    var byKey = {};
    (tunnels || []).forEach(function (tn) { byKey[tn.tunnel_key] = tn; });
    document.querySelectorAll('#wbody tr[data-key]').forEach(function (tr) {
      var tn = byKey[tr.dataset.key];
      var inp = tr.querySelector('input[data-key]');
      if (!tn || !inp) return;
      var perLocal = {};
      (tunnels || []).forEach(function (x) {
        if (!x.enabled) return;
        var k = x.local_server ? x.local_server.id : 0;
        perLocal[k] = (perLocal[k] || 0) + (x.tunnel_key === inp.dataset.key ? (parseInt(inp.value, 10) || 0) : (x.weight || 0));
      });
      var my = parseInt(inp.value, 10) || 0;
      var scope = tn.local_server ? perLocal[tn.local_server.id] || 0 : 0;
      var cell = tr.querySelector('.wshare');
      if (cell) cell.textContent = scope > 0 ? pct(my / scope * 100) : '—';
    });
  }
  function saveWeight(key) {
    var inp = document.querySelector('#wbody input[data-key="' + CSS.escape(key) + '"]');
    if (!inp) return;
    api('POST', 'topologies/' + weightsTopo + '/tunnels/' + encodeURIComponent(key) + '/weight', { weight: Math.max(0, parseInt(inp.value, 10) || 0) })
      .then(function (r) {
        toast((t('saved_one').replace('{k}', key)) + ' → ' + pct(r.resolved_percent), 'ok');
        localStorage.setItem('weights_pushed_' + weightsTopo, '0');
        loadWeights();
      })
      .catch(function (e) { toastErr(e); });
  }

  // ---- tunnel detail (contract §2.9) --------------------------------------------
  var tunnelRange = '24h';
  function renderTunnelDetail(key) {
    var el = document.getElementById('page-tunnel');
    el.innerHTML = skeletons();
    api('GET', 'tunnels/' + encodeURIComponent(key) + '?range=' + tunnelRange).then(function (d) {
      drawTunnelDetail(el, key, d);
    }).catch(function (e) { showPageError(el, e, function () { renderTunnelDetail(key); }); });
  }
  function drawTunnelDetail(el, key, d) {
    var tn = d.tunnel, now = d.now, tot = d.totals, rat = d.ratios, hist = d.history;
    document.getElementById("page-title").textContent = t("title_tunnel") + " " + key;
    document.title = "Quantum — " + key;
    var cpu = now.cpu_percent, rss = now.rss_bytes;
    var naNote = cpu == null && rss == null
      ? '<div class="ksub">' + esc(t('td_na_kernel')) + '</div>' : '';
    el.innerHTML =
      '<div class="kpis">' +
      kpi('', statePill(now.state), t('td_now') + ' — ' + esc(t('td_uptime')) + ' ' + dur(now.uptime_s), now.state === 'healthy' ? 'ok' : (now.state === 'degraded' ? 'warn' : 'err'),
        '<div class="ksub">' + esc(t('th_latency')) + ': <b>' + (now.latency_ms != null ? num(now.latency_ms, 0) + ' ms' : '—') + '</b> · ' +
        esc(t('td_loss')) + ': <b>' + (now.loss_pct != null ? pct(now.loss_pct) : '—') + '</b></div>') +
      kpi('', cpu != null ? pct(cpu) : '—', t('td_cpu'), '',
        '<div class="ksub">' + esc(t('td_rss')) + ': <b>' + (rss != null ? bytes(rss) : esc(t('td_na'))) + '</b></div>' + naNote) +
      kpi('', '<bdi dir="ltr">' + bytes(now.rate ? now.rate.wire_rx_bps : null) + '/s</bdi>', t('td_rate') + ' — ' + esc(t('wire')) + ' ' + esc(t('down_dir')), '',
        '<div class="ksub"><bdi dir="ltr">' + bytes(now.rate ? now.rate.wire_tx_bps : null) + '/s</bdi> ' + esc(t('up_dir')) + ' · ' + esc(t('payload')) + ' ' + esc(t('down_dir')) + ' <bdi dir="ltr">' + bytes(now.rate ? now.rate.payload_rx_bps : null) + '/s</bdi> · ' + esc(t('up_dir')) + ' <bdi dir="ltr">' + bytes(now.rate ? now.rate.payload_tx_bps : null) + '/s</bdi></div>') +
      kpi('', pct(rat.topology_share_pct), t('td_share'), '',
        '<div class="ksub">' + esc(t('td_overhead')) + ': <b>' + (rat.overhead_ratio != null ? num(rat.overhead_ratio, 3) : esc(t('td_na'))) + '</b></div>') +
      '</div>' +
      '<div class="card"><h3>' + esc(t('th_topology')) + ': <a href="#topologies/' + tn.topology.id + '">' + esc(tn.topology.name) + '</a></h3>' +
      '<div class="ksub">' + esc(t('th_peer')) + ': <b>' + esc(tn.local_server.name) + '</b> ↔ <b>' + esc(tn.remote_server.name) + '</b> · ' +
      esc(t('th_engine')) + ': <b>' + esc(tn.engine) + '</b> · ' + esc(t('th_created')) + ': ' + fmtDate(tn.created_at) + '</div></div>' +
      '<div class="card"><h3>' + esc(t('td_totals')) + '</h3><div class="tblwrap"><table><thead><tr><th></th>' +
      '<th class="num">' + esc(t('wire')) + ' ↓</th><th class="num">' + esc(t('wire')) + ' ↑</th>' +
      '<th class="num">' + esc(t('payload')) + ' ↓</th><th class="num">' + esc(t('payload')) + ' ↑</th></tr></thead><tbody>' +
      ['today', '7d', '30d'].map(function (r) {
        var x = tot[r] || {};
        return '<tr><td>' + esc(t('rng_' + (r === 'today' ? 'today' : r))) + '</td>' +
          '<td class="num">' + bytes(x.wire_rx_bytes) + '</td><td class="num">' + bytes(x.wire_tx_bytes) + '</td>' +
          '<td class="num">' + bytes(x.payload_rx_bytes) + '</td><td class="num">' + bytes(x.payload_tx_bytes) + '</td></tr>';
      }).join('') + '</tbody></table></div>' +
      '<p class="muted">' + esc(t('td_billable')) + ' (' + esc(t('rng_today')) + '): <b class="num">' + bytes((tot.today || {}).billable_download_bytes) + '</b></p></div>' +
      '<div class="card"><h3>' + esc(t('td_history')) + '</h3>' +
      '<div class="row"><div class="rng" id="thrng">' +
      ['24h', '7d', '30d'].map(function (r) {
        return '<button data-r="' + r + '" class="' + (tunnelRange === r ? 'active' : '') + '">' +
          esc(r === '24h' ? t('rng_24h') : t('rng_' + r)) + '</button>';
      }).join('') + '</div>' +
      '<span class="muted">' + esc(hist.granularity === 'hour' ? 'hour' : 'day') + '</span></div>' +
      '<div id="th-chart"></div></div>' +
      '<div class="card"><h3>' + esc(t('td_events')) + '</h3><div id="td-events"></div></div>';
    renderTunnelChart(hist);
    var evs = d.events || [];
    document.getElementById('td-events').innerHTML = evs.length
      ? '<div class="tblwrap"><table><thead><tr><th></th><th></th><th></th></tr></thead><tbody>' +
        evs.map(function (e) {
          return '<tr><td class="muted">' + fmtDate(e.at) + '</td>' +
            '<td><span class="pill ' + (e.kind === 'error' ? 'down' : 'info') + '">' + esc(t('ev_' + e.kind) || e.kind) + '</span></td>' +
            '<td>' + (e.kind === 'state'
              ? esc(t('ev_from') + ' ' + (e.from || '—') + ' ' + t('ev_to') + ' ' + (e.to || '—'))
              : '<span class="mono">' + esc(e.code || '') + '</span> ' + esc(e.text || '')) + '</td></tr>';
        }).join('') + '</tbody></table></div>'
      : '<div class="empty">' + esc(t('td_noevents')) + '</div>';
    el.querySelectorAll('#thrng button').forEach(function (b) {
      b.onclick = function () { tunnelRange = b.dataset.r; renderTunnelDetail(key); };
    });
  }
  // Grouped bar chart: wire/payload download per bucket (inline SVG).
  function renderTunnelChart(hist) {
    var box = document.getElementById('th-chart');
    if (!box) return;
    var pts = hist.points || [];
    if (!pts.length) {
      box.innerHTML = '<div class="empty">' + esc(t('td_nodata')) + '</div>';
      return;
    }
    var W = 760, H = 190, padL = 56, padB = 22, padT = 8;
    var bw = (W - padL - 10) / pts.length;
    var max = 1;
    pts.forEach(function (p) { max = Math.max(max, p.wire_rx_bytes || 0, p.wire_tx_bytes || 0, p.payload_rx_bytes || 0); });
    var svg = '<svg class="chart" viewBox="0 0 ' + W + ' ' + (H + padB) + '" role="img" aria-label="history">';
    for (var g = 1; g <= 3; g++) {
      var gy = padT + (H - padT - padB) * g / 4;
      svg += '<line class="grid" x1="' + padL + '" y1="' + gy + '" x2="' + (W - 10) + '" y2="' + gy + '"/>';
      svg += '<text x="' + (padL - 6) + '" y="' + (gy + 3) + '" text-anchor="end">' + bytes(max * (4 - g) / 4) + '</text>';
    }
    svg += '<line class="axis" x1="' + padL + '" y1="' + (H - padB) + '" x2="' + (W - 10) + '" y2="' + (H - padB) + '"/>';
    pts.forEach(function (p, i) {
      var x = padL + i * bw;
      var hdl = (H - padT - padB) * ((p.wire_rx_bytes || 0) / max);
      var htx = (H - padT - padB) * ((p.wire_tx_bytes || 0) / max);
      var hpl = (H - padT - padB) * ((p.payload_rx_bytes || 0) / max);
      var w = Math.max(2, bw * 0.28);
      svg += '<rect class="bar-dl" x="' + x.toFixed(1) + '" y="' + (H - padB - hdl).toFixed(1) + '" width="' + w.toFixed(1) + '" height="' + hdl.toFixed(1) + '" rx="2"><title>' + bytes(p.wire_rx_bytes) + '</title></rect>';
      svg += '<rect class="bar-ul" x="' + (x + w + 1).toFixed(1) + '" y="' + (H - padB - htx).toFixed(1) + '" width="' + w.toFixed(1) + '" height="' + htx.toFixed(1) + '" rx="2"><title>' + bytes(p.wire_tx_bytes) + '</title></rect>';
      svg += '<rect x="' + (x + 2 * w + 2).toFixed(1) + '" y="' + (H - padB - hpl).toFixed(1) + '" width="' + w.toFixed(1) + '" height="' + hpl.toFixed(1) + '" rx="2" fill="var(--ok)" opacity="0.55"><title>' + bytes(p.payload_rx_bytes) + '</title></rect>';
      if (pts.length <= 26 || i % Math.ceil(pts.length / 12) === 0) {
        var lbl = new Date(p.t * 1000);
        var s = hist.granularity === 'hour'
          ? lbl.toLocaleString(LANG === 'fa' ? 'fa-IR' : 'en-US', { timeZone: 'Asia/Tehran', hour: '2-digit' })
          : lbl.toLocaleDateString(LANG === 'fa' ? 'fa-IR' : 'en-US', { timeZone: 'Asia/Tehran', day: 'numeric', month: 'short' });
        svg += '<text x="' + (x + bw / 2).toFixed(1) + '" y="' + (H - 6) + '" text-anchor="middle">' + esc(s) + '</text>';
      }
    });
    svg += '</svg>';
    box.innerHTML = '<div class="legend"><span><i class="bar-dl"></i>' + esc(t('wire')) + ' ' + esc(t('down_dir')) + '</span>' +
      '<span><i class="bar-ul"></i>' + esc(t('wire')) + ' ' + esc(t('up_dir')) + '</span>' +
      '<span><i style="background:var(--ok);opacity:.55"></i>' + esc(t('payload')) + ' ' + esc(t('down_dir')) + '</span></div>' + svg;
  }

  // ---- table tools: sorting + search (shared) ------------------------------------
  function wireTableTools(tableId, scopeEl, sortCols) {
    var tbl = document.getElementById(tableId);
    if (!tbl) return;
    var input = scopeEl.querySelector('input[type=search]');
    if (input) {
      input.addEventListener('input', function () {
        var q = input.value.toLowerCase();
        tbl.querySelectorAll('tbody tr').forEach(function (tr) {
          tr.style.display = tr.textContent.toLowerCase().indexOf(q) >= 0 ? '' : 'none';
        });
      });
    }
    if (sortCols && sortCols.length) {
      var headCells = tbl.querySelectorAll('thead th');
      headCells.forEach(function (th, i) {
        if (!sortCols[i]) return;
        th.classList.add('sortable');
        th.onclick = function () {
          var dir = th.classList.contains('asc') ? 'desc' : 'asc';
          headCells.forEach(function (x) { x.classList.remove('asc', 'desc'); });
          th.classList.add(dir);
          var body = tbl.querySelector('tbody');
          var rows = Array.prototype.slice.call(body.querySelectorAll('tr'));
          rows.sort(function (a, b) {
            var av = a.children[i] ? a.children[i].textContent.trim() : '';
            var bv = b.children[i] ? b.children[i].textContent.trim() : '';
            var an = parseFloat(av.replace(/[^\d.-]/g, '')), bn = parseFloat(bv.replace(/[^\d.-]/g, ''));
            if (!isNaN(an) && !isNaN(bn) && /^[\s\d.,%\/\-]+$/.test(av) && /^[\s\d.,%\/\-]+$/.test(bv)) {
              return dir === 'asc' ? an - bn : bn - an;
            }
            return dir === 'asc' ? av.localeCompare(bv) : bv.localeCompare(av);
          });
          rows.forEach(function (r) { body.appendChild(r); });
        };
      });
    }
  }

  // ---- engines page --------------------------------------------------------------
  function renderEngines() {
    var el = document.getElementById('page-engines');
    // artifacts ride the existing mesh endpoint (they are panel-side objects)
    fetch(base() + 'api/mesh/engines', { headers: { 'X-CSRF-Token': csrfToken() } })
      .then(function (r) { return r.json(); }).then(function (d) {
        var arts = (d.artifacts || []).map(function (a) {
          return '<tr><td><b>' + esc(a.engine) + '</b>' + (a.engine === 'matrix' ? ' <span class="pill info">' + esc(t('eng_default')) + '</span>' : ' <span class="pill dim">' + esc(t('eng_experimental')) + '</span>') + '</td>' +
            '<td>' + esc(a.version) + '</td><td>' + esc(a.goos) + '</td><td>' + esc(a.goarch) + '</td>' +
            '<td class="mono trunc" style="max-width:200px" title="' + esc(a.artifact) + '">' + esc(a.artifact) + '</td>' +
            '<td class="num">' + bytes(a.size) + '</td><td>' + fmtDate(a.uploaded_at) + '</td></tr>';
        }).join('');
        el.innerHTML =
          '<div class="card"><p class="note">' + esc(t('eng_matrix_note')) + '</p></div>' +
          '<div class="card"><h3>' + esc(t('eng_artifacts')) + '</h3>' +
          (arts ? '<div class="tblwrap"><table><thead><tr><th>' + esc(t('th_engine')) + '</th><th>' + esc(t('eng_version')) + '</th>' +
            '<th>OS</th><th>' + esc(t('eng_arch')) + '</th><th></th><th class="num">' + esc(t('eng_size')) + '</th><th></th></tr></thead><tbody>' + arts + '</tbody></table></div>'
            : '<div class="empty">' + esc(t('eng_none')) + '</div>') + '</div>' +
          '<div class="card"><h3>' + esc(t('eng_upload')) + '</h3>' +
          '<form id="eng-form"><div class="formgrid">' +
          '<div class="field"><label for="eng-e">' + esc(t('th_engine')) + '</label><input id="eng-e" required pattern="[a-z0-9_-]+"></div>' +
          '<div class="field"><label for="eng-v">' + esc(t('eng_version')) + '</label><input id="eng-v" required></div>' +
          '<div class="field"><label for="eng-a">' + esc(t('eng_arch')) + '</label><select id="eng-a"><option>amd64</option><option>arm64</option></select></div>' +
          '<div class="field"><label for="eng-n">name</label><input id="eng-n"></div>' +
          '<div class="field"><label for="eng-s">signature (base64 ed25519)</label><input id="eng-s" required></div>' +
          '</div><div class="field"><label for="eng-f">file</label><input id="eng-f" type="file" required></div>' +
          '<button class="act primary" type="submit">' + esc(t('eng_upload')) + '</button></form></div>';
        document.getElementById('eng-form').addEventListener('submit', function (ev) {
          ev.preventDefault();
          var fd = new FormData();
          fd.append('engine', document.getElementById('eng-e').value);
          fd.append('version', document.getElementById('eng-v').value);
          fd.append('goarch', document.getElementById('eng-a').value);
          fd.append('goos', 'linux');
          var nm = document.getElementById('eng-n').value;
          if (nm) fd.append('name', nm);
          fd.append('signature', document.getElementById('eng-s').value);
          var f = document.getElementById('eng-f').files[0];
          if (!f) return;
          fd.append('file', f);
          fetch(base() + 'api/mesh/engines/artifacts', { method: 'POST', headers: { 'X-CSRF-Token': csrfToken() }, body: fd })
            .then(function (r) { return r.json().then(function (j) { if (!r.ok) throw mkApiErr(j); }); })
            .then(function () { toast(t('saved'), 'ok'); renderEngines(); })
            .catch(function (e) { toastErr(e); });
        });
      }).catch(function (e) { showPageError(el, e, renderEngines); });
  }
  function mkApiErr(j) {
    var e = new Error((j && j.error) || 'HTTP error');
    e.ui = j && j.ui;
    return e;
  }

  // ---- alerts page ---------------------------------------------------------------
  function renderAlerts() {
    var el = document.getElementById('page-alerts');
    api('GET', '../mesh/alerts').catch(function () {
      return fetch(base() + 'api/mesh/alerts', { headers: { 'X-CSRF-Token': csrfToken() } }).then(function (r) { return r.json(); });
    }).then(function (d) {
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
        (open ? '<div class="tblwrap"><table><thead><tr><th></th><th></th><th></th></tr></thead><tbody>' + open + '</tbody></table></div>'
          : '<div class="empty">' + esc(t('al_none')) + '</div>') + '</div>' +
        '<div class="card"><h3>' + esc(t('al_rules')) + '</h3><form id="al-form" onsubmit="return false">' +
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
        (hist ? '<div class="tblwrap"><table><thead><tr><th></th><th></th><th></th><th></th></tr></thead><tbody>' + hist + '</tbody></table></div>' : '<div class="empty">—</div>') + '</div>';
      document.getElementById('al-save').onclick = function () {
        fetch(base() + 'api/mesh/alerts/settings', {
          method: 'POST', headers: { 'X-CSRF-Token': csrfToken(), 'Content-Type': 'application/json' },
          body: JSON.stringify({
            enabled: document.getElementById('al-en').checked,
            agent_offline_min: parseInt(document.getElementById('al-off').value, 10),
            cert_expire_days: parseInt(document.getElementById('al-cert').value, 10),
            disk_max_percent: parseInt(document.getElementById('al-disk').value, 10),
            telegram_token: document.getElementById('al-tg').value,
            telegram_chat: document.getElementById('al-chat').value,
            webhook_url: document.getElementById('al-hook').value
          })
        }).then(function (r) { return r.json().then(function (j) { if (!r.ok) throw mkApiErr(j); return j; }); })
          .then(function () { localStorage.setItem('alerts_on', '1'); toast(t('saved'), 'ok'); loadSession(); renderAlerts(); })
          .catch(function (e) { toastErr(e); });
      };
      document.getElementById('al-test').onclick = function () {
        fetch(base() + 'api/mesh/alerts/test', { method: 'POST', headers: { 'X-CSRF-Token': csrfToken() } })
          .then(function (r) { return r.json(); })
          .then(function (r) { toast(r.sent ? t('al_test_ok') + ' ' + r.sent + ' ' + t('al_notif') : t('al_no_conf'), r.sent ? 'ok' : 'err'); })
          .catch(function (e) { toastErr(e); });
      };
    }).catch(function (e) { showPageError(el, e, renderAlerts); });
  }
  function alertText(a) {
    var m = /agent (agt_[0-9a-f]+)/.exec(a.text || '');
    if (m && agentNames[m[1]]) return a.text.replace(m[1], agentNames[m[1]] + ' (' + m[1] + ')');
    return a.text;
  }

  // ---- security (safe-apply) -------------------------------------------------------
  var agentNames = {};
  function renderSecurity() {
    var el = document.getElementById('page-security');
    api('GET', 'agents').then(function (j) {
      var rows = (j.agents || []).filter(function (a) { return a.status !== 'revoked'; }).map(function (a) {
        agentNames[a.id] = a.name || a.id;
        return '<tr><td><b>' + esc(a.name || a.id) + '</b> <span class="muted mono">' + esc(a.id) + '</span></td>' +
          '<td>' + statePill(a.status) + '</td>' +
          '<td><button class="act small" data-h="' + a.id + '">' + esc(t('apply_h')) + '</button> ' +
          '<button class="act small" data-n="' + a.id + '">' + esc(t('apply_n')) + '</button> ' +
          '<button class="act small primary" data-push="' + a.id + '">' + esc(t('push_btn')) + '</button></td></tr>';
      }).join('');
      el.innerHTML = '<div class="card"><h3>' + esc(t('sec_agents')) + '</h3>' +
        '<div class="tblwrap"><table><thead><tr><th>' + esc(t('th_agent')) + '</th><th>' + esc(t('th_state')) + '</th><th></th></tr></thead><tbody>' + rows + '</tbody></table></div>' +
        '<div class="field"><label for="prof">profile JSON</label>' +
        '<textarea id="prof" rows="6" class="mono" style="width:100%">{}</textarea></div>' +
        '<p class="muted">' + esc(t('prof_hint')) + '</p></div>';
      el.addEventListener('click', profileClick);
    }).catch(function (e) { showPageError(el, e, renderSecurity); });
  }
  function profileClick(e) {
    var h = e.target.closest('button[data-h]');
    var n = e.target.closest('button[data-n]');
    var p = e.target.closest('button[data-push]');
    if (p) {
      api('POST', '../mesh/agents/' + p.dataset.push + '/push', {}).then(function (r) {
        localStorage.setItem('weights_pushed_' + (weightsTopo || 'x'), '1');
        toast(t('pushed_ok') + (r.revision ? ' rev ' + r.revision : ''), 'ok');
      }).catch(function (er) { toastErr(er); });
      return;
    }
    if (!h && !n) return;
    var kind = h ? 'hardening' : 'netopt';
    var agent = h ? h.dataset.h : n.dataset.n;
    var prof;
    try { prof = JSON.parse(document.getElementById('prof').value); }
    catch (err) { toast(t('need_json'), 'err'); return; }
    fetch(base() + 'api/mesh/agents/' + agent + '/profile/' + kind, {
      method: 'POST', headers: { 'X-CSRF-Token': csrfToken(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ profile: prof, rollback_timer_s: 120 })
    }).then(function (r) { return r.json().then(function (j) { if (!r.ok) throw mkApiErr(j); return j; }); })
      .then(function (r) { toast(t('pushed') + '; ' + r.rollback_timer_s + 's', 'ok'); renderBanner(); })
      .catch(function (er) { toastErr(er); });
  }

  // ---- pasarguard (billing: connect + coefficient view) ------------------------------
  function renderPG() {
    var el = document.getElementById('page-pasarguard');
    fetch(base() + 'api/mesh/pasarguard/status', { headers: { 'X-CSRF-Token': csrfToken() } })
      .then(function (r) { return r.json(); }).then(function (st) {
        el.innerHTML = '<div class="card"><div class="row">' +
          '<input id="pg-url" placeholder="' + esc(t('pg_url')) + ' (https://pg.example:8000)" style="width:260px">' +
          '<input id="pg-user" placeholder="' + esc(t('pg_user')) + '">' +
          '<input id="pg-pass" type="password" autocomplete="off" placeholder="' + esc(t('pg_pass')) + '">' +
          '<button class="act primary" id="pg-connect">' + esc(t('pg_connect')) + '</button>' +
          '<span class="muted" id="pg-status">' + (st.configured ? esc(st.url) + ' ✓' : '') + '</span></div>' +
          '<div class="row"><button class="act" id="pg-dry">' + esc(t('pg_dry')) + '</button>' +
          '<button class="act primary" id="pg-apply">' + esc(t('pg_apply')) + '</button>' +
          '<button class="act" id="pg-cross">' + esc(t('pg_crosscheck')) + '</button></div>' +
          '<div id="pg-out"></div></div>' +
          '<div class="card"><h3>' + esc(t('pg_coeff')) + '</h3><div class="row">' +
          '<select id="pg-dc"></select><button class="act" id="pg-coef">' + esc(t('pg_coeff')) + '</button></div>' +
          '<div id="pg-coefout"></div></div>';
        fetch(base() + 'api/mesh/overview', { headers: { 'X-CSRF-Token': csrfToken() } })
          .then(function (r) { return r.json(); }).then(function (ov) {
            var sel = document.getElementById('pg-dc');
            if (sel) sel.innerHTML = (ov.datacenters || []).map(function (x) {
              return '<option value="' + x.id + '">' + esc(x.name) + '</option>';
            }).join('');
          }).catch(function () {});
        document.getElementById('pg-connect').onclick = function () {
          fetch(base() + 'api/mesh/pasarguard/connect', {
            method: 'POST', headers: { 'X-CSRF-Token': csrfToken(), 'Content-Type': 'application/json' },
            body: JSON.stringify({
              url: document.getElementById('pg-url').value,
              username: document.getElementById('pg-user').value,
              password: document.getElementById('pg-pass').value
            })
          }).then(function (r) { return r.json().then(function (j) { if (!r.ok) throw mkApiErr(j); return j; }); })
            .then(function (r) { localStorage.setItem('pg_on', '1'); toast('connected: ' + r.nodes, 'ok'); loadSession(); renderPG(); })
            .catch(function (e) { toastErr(e); });
        };
        document.getElementById('pg-dry').onclick = function () { pgReconcile(false); };
        document.getElementById('pg-apply').onclick = function () { pgReconcile(true); };
        document.getElementById('pg-cross').onclick = function () { pgCross(); };
        var cb = document.getElementById('pg-coef');
        if (cb) cb.onclick = function () {
          var dc = document.getElementById('pg-dc').value;
          Promise.all([
            fetch(base() + 'api/mesh/coefficient?datacenter_id=' + dc, { headers: { 'X-CSRF-Token': csrfToken() } }).then(function (r) { return r.json(); }),
            fetch(base() + 'api/mesh/overhead', { headers: { 'X-CSRF-Token': csrfToken() } }).then(function (r) { return r.json(); })
          ]).then(function (rs) {
            var r = rs[0], ov = rs[1] || {};
            var ovBy = {};
            (ov.engines || []).forEach(function (x) { ovBy[x.engine] = x; });
            document.getElementById('pg-coefout').innerHTML =
              '<div class="tblwrap"><table><thead><tr><th>' + esc(t('th_engine')) + '</th><th class="num">' + esc(t('pg_overhead')) + ' RX</th>' +
              '<th class="num">' + esc(t('pg_overhead')) + ' TX</th><th class="num">' + esc(t('th_coeff')) + '</th></tr></thead><tbody>' +
              (r.suggestions || []).map(function (sg) {
                var x = ovBy[sg.engine] || {};
                return '<tr><td>' + esc(sg.engine) + '</td><td class="num">' + (x.ratio ? num(x.ratio, 3) : '—') + '</td>' +
                  '<td class="num">' + (x.ratio_tx ? num(x.ratio_tx, 3) : '—') + '</td>' +
                  '<td><b>' + num(sg.suggested_coefficient, 3) + '</b></td></tr>';
              }).join('') + '</tbody></table></div>';
          }).catch(function (e) { toastErr(e); });
        };
      }).catch(function (e) { showPageError(el, e, renderPG); });
  }
  function pgReconcile(apply) {
    fetch(base() + 'api/mesh/pasarguard/reconcile', {
      method: 'POST', headers: { 'X-CSRF-Token': csrfToken(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ apply: apply })
    }).then(function (r) { return r.json().then(function (j) { if (!r.ok) throw mkApiErr(j); return j; }); })
      .then(function (r) {
        var rows = ((r.plan || {}).actions || []).map(function (a) {
          return '<tr><td>' + esc(a.op) + '</td><td>' + esc(a.name || '') + '</td><td>' + esc(a.detail || '') + '</td></tr>';
        }).join('');
        document.getElementById('pg-out').innerHTML =
          (rows ? '<div class="tblwrap"><table><thead><tr><th>op</th><th>name</th><th></th></tr></thead><tbody>' + rows + '</tbody></table></div>' : '<div class="empty">—</div>') +
          (r.warnings || []).map(function (w) { return '<p class="muted">⚠ ' + esc(w) + '</p>'; }).join('');
      }).catch(function (e) { toastErr(e); });
  }
  function pgCross() {
    fetch(base() + 'api/mesh/pasarguard/crosscheck?datacenter_id=' + (document.getElementById('pg-dc') ? document.getElementById('pg-dc').value : ''), { headers: { 'X-CSRF-Token': csrfToken() } })
      .then(function (r) { return r.json(); })
      .then(function (r) {
        var rows = (r.rows || []).map(function (x) {
          return '<tr><td>' + esc(x.node || '') + '</td><td>' + esc(x.server || '') + '</td><td class="num">' + bytes(x.pg_used) + '</td><td class="num">' + bytes(x.panel_used) + '</td></tr>';
        }).join('');
        document.getElementById('pg-out').innerHTML = rows
          ? '<div class="tblwrap"><table><thead><tr><th>node</th><th>server</th><th>PasarGuard</th><th>Panel</th></tr></thead><tbody>' + rows + '</tbody></table></div>'
          : '<div class="empty">—</div>';
      }).catch(function (e) { toastErr(e); });
  }

  // ---- settings ----------------------------------------------------------------------
  function renderSettings() {
    var el = document.getElementById('page-settings');
    el.innerHTML = '<div class="card"><h3>' + esc(t('backup_btn')) + '</h3>' +
      '<div class="formgrid">' +
      '<div class="field"><label for="bk-pw">' + esc(t('backup_ask_pw')) + '</label><input id="bk-pw" type="password" autocomplete="off"></div>' +
      '<div class="field"><label for="bk-pp">' + esc(t('backup_ask_pass')) + '</label><input id="bk-pp" type="password" autocomplete="off"></div>' +
      '</div><button class="act primary" id="bk-go">' + esc(t('backup_btn')) + '</button></div>' +
      '<div class="card"><h3>' + esc(t('prefs')) + '</h3>' +
      '<div class="formgrid">' +
      '<div class="field"><label for="pref-lang">' + esc(t('lang_pref')) + '</label><select id="pref-lang"><option value="fa"' + (LANG === 'fa' ? ' selected' : '') + '>فارسی</option><option value="en"' + (LANG === 'en' ? ' selected' : '') + '>English</option></select></div>' +
      '<div class="field"><label for="pref-cal">' + esc(t('calendar')) + '</label><select id="pref-cal"><option value="jalali"' + (CAL === 'jalali' ? ' selected' : '') + '>' + esc(t('cal_jalali')) + '</option><option value="gregorian"' + (CAL === 'gregorian' ? ' selected' : '') + '>' + esc(t('cal_greg')) + '</option></select></div>' +
      '</div><p class="muted">' + esc(t('reload_note')) + '</p></div>' +
      '<div class="card"><h3>' + esc(t('legacy_note')) + '</h3><p class="muted">' +
      (session && session.legacy_enabled
        ? '<a class="btn" href="' + esc(base()) + './">' + esc(t('legacy_open')) + '</a>'
        : esc(t('legacy_gated'))) + '</p></div>';
    document.getElementById('bk-go').onclick = function () {
      var pw = document.getElementById('bk-pw').value;
      var pass = document.getElementById('bk-pp').value;
      if (!pw) { toast(t('backup_ask_pw'), 'err'); return; }
      if (!pass || pass.length < 12) { toast(t('backup_denied'), 'err'); return; }
      fetch(base() + 'api/mesh/backup', {
        method: 'POST', headers: { 'X-CSRF-Token': csrfToken(), 'Content-Type': 'application/json' },
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
      }).catch(function (e) { toastErr(e); });
    };
    document.getElementById('pref-lang').onchange = function (e) {
      localStorage.setItem('mesh_lang', e.target.value); location.reload();
    };
    document.getElementById('pref-cal').onchange = function (e) {
      localStorage.setItem('mesh_cal', e.target.value); location.reload();
    };
  }

  // ---- safe-apply confirm banner -------------------------------------------------------
  var bannerIv = null;
  function renderBanner() {
    var el = document.getElementById('confirm-banner');
    fetch(base() + 'api/mesh/profiles/pending', { headers: { 'X-CSRF-Token': csrfToken() } })
      .then(function (r) { return r.json(); })
      .then(function (j) {
        var p = (j.pending || [])[0];
        if (!p) {
          el.style.display = 'none';
          if (bannerIv) { clearInterval(bannerIv); bannerIv = null; }
          return;
        }
        var left = Math.max(0, p.deadline - Math.floor(Date.now() / 1000));
        el.style.display = 'block';
        var name = agentNames[p.agent_id] || p.agent_id;
        var draw = function () {
          el.innerHTML = '<b>⏱ ' + esc(t('confirm_banner').replace('{agent}', name).replace('{s}', left)) + '</b> ' +
            '<button class="act primary small" id="btn-confirm">' + esc(t('confirm')) + '</button> ' +
            '<button class="act small" id="btn-rollback">' + esc(t('rollback')) + '</button>';
        };
        draw();
        document.getElementById('btn-confirm').onclick = function () {
          fetch(base() + 'api/mesh/agents/' + p.agent_id + '/profile/confirm', { method: 'POST', headers: { 'X-CSRF-Token': csrfToken() } })
            .then(function () { toast(t('pushed_ok'), 'ok'); renderBanner(); })
            .catch(function (er) { toastErr(er); });
        };
        document.getElementById('btn-rollback').onclick = function () {
          fetch(base() + 'api/mesh/agents/' + p.agent_id + '/profile/rollback?kind=' + p.kind, { method: 'POST', headers: { 'X-CSRF-Token': csrfToken() } })
            .then(function () { toast(t('pushed'), 'ok'); renderBanner(); })
            .catch(function (er) { toastErr(er); });
        };
        if (bannerIv) clearInterval(bannerIv);
        bannerIv = setInterval(function () {
          left -= 1;
          var b = el.querySelector('b');
          if (!b || left < 0) { clearInterval(bannerIv); bannerIv = null; return; }
          b.textContent = '⏱ ' + t('confirm_banner').replace('{agent}', name).replace('{s}', left);
        }, 1000);
      }).catch(function () { el.style.display = 'none'; });
  }

  // ---- boot ------------------------------------------------------------------------------
  function boot() {
    applyTheme();
    applyLang();
    setRoute();
    loadSession();
    loadAll();
    document.getElementById('nav').addEventListener('click', function (e) {
      var b = e.target.closest('button[data-nav]');
      if (b) { location.hash = b.dataset.nav; }
    });
    window.addEventListener('hashchange', setRoute);
    document.getElementById('menubtn').addEventListener('click', openSidebar);
    document.getElementById('scrim').addEventListener('click', closeSidebar);
    document.getElementById('theme').addEventListener('click', function () {
      var th = (localStorage.getItem('gre_theme') || 'dark') === 'dark' ? 'light' : 'dark';
      localStorage.setItem('gre_theme', th);
      applyTheme();
    });
    document.getElementById('lang').addEventListener('click', function () {
      LANG = LANG === 'fa' ? 'en' : 'fa';
      localStorage.setItem('mesh_lang', LANG);
      applyLang(); setRoute();
    });
    document.getElementById('um-refresh').addEventListener('click', function () {
      document.getElementById('usermenu').hidden = true; loadAll(); loadSession();
    });
    document.getElementById('um-logout').addEventListener('click', function () {
      fetch(base() + 'api/logout', { method: 'POST', headers: { 'X-CSRF-Token': csrfToken() } })
        .then(function () { location.href = base(); })
        .catch(function () { location.href = base(); });
    });
    var umb = document.getElementById('usermenu-btn');
    umb.addEventListener('click', function () {
      var pop = document.getElementById('usermenu');
      pop.hidden = !pop.hidden;
      umb.setAttribute('aria-expanded', String(!pop.hidden));
    });
    document.addEventListener('click', function (e) {
      if (!e.target.closest('#usermenu-wrap')) {
        document.getElementById('usermenu').hidden = true;
        umb.setAttribute('aria-expanded', 'false');
      }
    });
    // polling per contract §3: session 60s, overview 10s, detail pages 15s
    setInterval(loadSession, 60000);
    setInterval(function () {
      if (route.page === 'dash') loadAll();
      else if (route.page === 'agent') renderAgentDetail(route.id);
      else if (route.page === 'tunnel') renderTunnelDetail(route.id);
      else if (route.page === 'topologies' && route.id) renderTopologyDetail(route.id);
    }, 15000);
    loadAll().then(function () {
      // refresh agent names for the alert join once at boot
      api('GET', 'agents').then(function (j) {
        (j.agents || []).forEach(function (a) { agentNames[a.id] = a.name || a.id; });
      }).catch(function () {});
    });
  }

  // API paths above use `../mesh/...` style escapes from /api/ui/ — normalize
  // api() so a path starting with `../mesh/` rides the mesh endpoints (glue:
  // profiles/pending, push, alerts, pasarguard, engines artifacts are panel-side
  // objects the contract keeps on /api/mesh).
  (function () {
    var orig = api;
    api = function (method, path, body) {
      if (path.indexOf('../mesh/') === 0) {
        var p = path.replace('../mesh/', 'mesh/');
        var opt = { method: method, headers: { 'X-CSRF-Token': csrfToken() } };
        if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
        return fetch(base() + 'api/' + p, opt).then(function (r) {
          if (r.status === 401) { location.href = base(); throw mkNetErr(t('login_redirect')); }
          return r.text().then(function (txt) {
            var j = null;
            try { j = txt ? JSON.parse(txt) : null; } catch (e2) {}
            if (!r.ok) { var err = new Error((j && j.error) || 'HTTP ' + r.status); err.ui = j && j.ui; err.status = r.status; throw err; }
            return j;
          });
        }).catch(function (e) { if (e instanceof TypeError) throw mkNetErr(t('net_down')); throw e; });
      }
      return orig(method, path, body);
    };
  })();

  boot();
})();
