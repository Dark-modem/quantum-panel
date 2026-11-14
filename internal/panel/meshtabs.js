// meshtabs.js — mesh-management tabs for the legacy dashboard SPA (owner
// review round 2). The standalone /mesh UI was rejected and removed; its
// FUNCTIONALITY returns here as native tabs inside the legacy dashboard's
// design language (tunnel-card / tc-header / ent-tbl / badge / quantum-btn).
//
// Self-contained IIFE — no dependence on app.js internals. The integrator
// loads it AFTER app.js: <script src="meshtabs.js"></script>. On load it:
//   1. injects one namespaced <style> (.mtb-*) into <head>;
//   2. appends nav buttons (class "nav-item", data-tab="mtun|magt|
//      malert|mbill") to #sideNav and clones them into #drawerNav;
//   3. appends five <section role="tabpanel" hidden> blocks after the last
//      existing section inside <main class="page">.
// Tab switching is implemented here: capture-phase delegation on both navs
// intercepts clicks on "m*" tabs; a guarded window.goTab wrapper keeps the
// mobile-drawer clones, command palette and keyboard nav working; bubble-phase
// delegation detects legacy switches to hide our sections and restore the
// previously visible legacy panel when needed.
// Data is lazy-loaded on first open and refreshed on every re-open; loading
// states use the native .ent-skel skeletons; failures render an inline error
// card. No console noise, no polling loops. Read-only GETs only — same-origin
// cookies ride along automatically; the CSRF header (gre_csrf cookie) is
// attached the way the legacy code does, for the authenticated CSV download.
//
// Endpoints (all same-origin, prefixed with <html data-base>):
//   GET api/mesh/topologies            topology list (store view)
//   GET api/mesh/topologies/{id}       detail: topology, members (server ids),
//                                      tunnels (store view: key/engine/enabled/
//                                      local_server_id/remote_server_id — no
//                                      state/weight here), weights, forwards
//   GET api/ui/topologies/{id}         detail enrichment: per-tunnel state,
//                                      weight, resolved_percent, local/remote
//                                      server objects, traffic_today, totals
//   GET api/mesh/servers               server id -> name/datacenter map
//   GET api/ui/tunnels/{key}?range=7d  totals for 7d AND 30d in one response
//   GET api/ui/agents                  agents list (no per-agent resources)
//   GET api/mesh/alerts                enabled, open[], history[], thresholds
//   GET api/ui/reports/billing?month=  billing report (+ &format=csv)
(function () {
  'use strict';

  var MY_TABS = ['magt', 'mtun', 'malert', 'mbill'];

  // ---- base path (same rule as app.js: <html data-base="...">) -------------
  var BASE = (document.documentElement.getAttribute('data-base') || '/');
  if (BASE.charAt(BASE.length - 1) !== '/') BASE += '/';
  function at(p) { return BASE + p; }

  function csrfToken() {
    var m = document.cookie.match(/(?:^|;\s*)gre_csrf=([^;]+)/);
    return m ? decodeURIComponent(m[1]) : '';
  }

  // ---- language (fa/en): the legacy dashboard toggles body.is-rtl ----------
  function isFa() {
    if (document.body && document.body.classList.contains('is-rtl')) return true;
    return document.documentElement.lang === 'fa';
  }
  // TXT[k] = [fa, en]
  var TXT = {
    mtun: ['تونل‌ها', 'Tunnels'],
    topoCol: ['توپولوژی', 'Topology'],
    agActs: ['عملیات', 'Actions'], agActView: ['مشاهده', 'View'], agActEdit: ['ویرایش', 'Edit'], agActDel: ['حذف', 'Delete'],
    agBack: ['بازگشت', 'Back'], agInfoT: ['مشخصات ایجنت', 'Agent details'],
    agTopoT: ['توپولوژی‌ها', 'Topologies'], agEngT: ['هسته‌های ایجنت', 'Agent engines'], agNoEng: ['انجینی ثبت نشده', 'No engines'],
    agNoTopo: ['عضو هیچ توپولوژی‌ای نیست', 'Not a member of any topology'], agPeers: ['هم‌تا', 'peers'],
    agEditT: ['ویرایش ایجنت', 'Edit agent'], agNameK: ['نام', 'Name'], agIPK: ['IP عمومی', 'Public IP'],
    agEditSub: ['اطلاعات ایجنت را ویرایش و ذخیره کنید.', 'Edit the agent information and save.'],
    agEdSt1: ['اطلاعات پایه', 'Basic info'], agEdSt2: ['بازبینی و ذخیره', 'Review & save'],
    agEdNext: ['مرحله بعد', 'Next'],
    agSave: ['ذخیره', 'Save'], agDelT: ['حذف ایجنت', 'Delete agent'],
    agDelConfirm: ['برای تأیید حذف، اسم ایجنت را بنویس:', 'To confirm, type the agent name:'],
    agDelPh: ['اسم ایجنت', 'agent name'],
    agDelNo: ['اسم ایجنت را دقیق بنویس تا دکمه حذف فعال شود', 'Type the exact agent name to enable Delete'],
    agDelQ: ['این عملیات قابل بازگشت نیست. برای ادامه، نام ایجنت را دقیقاً وارد کنید.', 'This action cannot be undone. To continue, enter the agent name exactly.'],
    agDelEnable: ['برای فعال شدن حذف، نام ایجنت را دقیق وارد کنید.', 'Enter the exact agent name to enable Delete'],
    agDelWarn2: ['این عملیات غیرقابل بازگشت است و تمام اطلاعات مربوط به این ایجنت حذف خواهد شد.', 'This action is irreversible and everything belonging to this agent will be removed.'],
    agCopied: ['کپی شد!', 'Copied!'],
    agVer: ['نسخه', 'Version'], agOS: ['سیستم‌عامل', 'OS'], agLastSeen: ['آخرین حضور', 'Last seen'],
    agActUpd: ['آپدیت', 'Update'],
    agUpdT: ['آپدیت ایجنت', 'Update agent'], agUpdVer: ['نسخه هدف', 'Target version'],
    agUpdUrl: ['آدرس فایل آپدیت (https)', 'Artifact URL (https)'],
    agUpdHint: ['فایل باید https، امضاشده با کلید آپدیت و برای معماری همین ایجنت باشد. ایجنت دانلود و تأیید می‌کند و پس از تعویض ری‌استارت می‌شود.', 'The artifact must be https, signed with the pinned update key, and built for this agent architecture. The agent downloads, verifies, swaps and restarts.'],
    agUpdStart: ['شروع آپدیت', 'Start update'], agUpdNeed: ['نسخه و آدرس فایل لازم است', 'Version and artifact URL are required'],
    agPhRequested: ['درخواست شد', 'requested'], agPhDownloading: ['دانلود', 'downloading'],
    agPhVerifying: ['بررسی امضا', 'verifying'], agPhSwapped: ['جایگزین شد', 'swapped'],
    agPhRestarting: ['ری‌استارت', 'restarting'], agPhCompleted: ['کامل شد', 'completed'],
    agPhFailed: ['ناموفق', 'failed'],
    agTunT: ['تونل‌ها', 'Tunnels'], agNoTun: ['هیچ تونلی روی این سرور نیست', 'No tunnels on this server'],
    agTunUp: ['متصل', 'up'],
    agStatAll: ['کل', 'Total'], agStatOn: ['آنلاین', 'online'],     agStatOff: ['آفلاین', 'offline'],
    agPage: ['صفحه', 'Page'], agOf: ['از', 'of'],
    agSearchPh: ['جستوجوی ایجنت‌ها...', 'Search agents...'], agAllF: ['همه', 'All'],
    agNoMatch: ['ایجنتی مطابق جستوجو/فیلتر نیست', 'No agents match the search/filters'],
    agRoleH: ['موقعیت سرور', 'Server location'], agRoleF: ['نقش', 'Role'],
    agToposH: ['توپولوژی‌ها', 'Topologies'], agLastConn: ['آخرین ارتباط', 'Last contact'],
    agNoTopoS: ['بدون توپولوژی', 'No topology'],
    agAllRoles: ['همه نقش‌ها', 'All roles'], agAllStates: ['همه وضعیت‌ها', 'All statuses'],
    agAllArchs: ['همه معماری‌ها', 'All architectures'], agAllTopos: ['همه توپولوژی‌ها', 'All topologies'],
    agRefresh: ['تازه‌سازی وضعیت', 'Refresh status'], agUpdBtn: ['به‌روزرسانی ایجنت', 'Update agent'],
    agBackList: ['بازگشت به لیست ایجنت‌ها', 'Back to agents list'],
    agRefreshed: ['وضعیت ایجنت تازه‌سازی شد', 'Agent status refreshed'],
    agDc: ['دیتاسنتر', 'Datacenter'], agResT: ['منابع سرور', 'Server resources'], agCores: ['هسته', 'cores'],
    agMembers: ['تعداد اعضا', 'Members'], agViewTopo: ['مشاهده توپولوژی', 'View topology'],
    topoRelH: ['توپولوژی‌های مرتبط', 'Related topologies'], topoSrv: ['سرور', 'servers'],
    topoMemberN: ['عضو', 'members'], topoViewDtl: ['مشاهده جزئیات', 'View details'],
    agNotInstalled: ['نصب نشده', 'Not installed'], agEnable: ['فعال‌سازی', 'Enable'], agDisable: ['غیرفعال‌سازی', 'Disable'],
    engStateRun: ['در حال اجرا', 'Running'], engStateStop: ['متوقف', 'Stopped'], engStateNot: ['نصب نشده', 'Not installed'],
    engInstall: ['نصب', 'Install'], engStart: ['اجرای', 'Start'], engStop: ['توقف', 'Stop'],
    engAll: ['همه', 'All'], engVersion: ['نسخه', 'Version'], engKernel: ['هسته‌ای — بدون نسخه', 'Kernel — no version'],
    agSumDisk: ['مجموع دیسک', 'Total disk'], agSumRam: ['مجموع رم', 'Total RAM'],
    agSumCores: ['مجموع هسته‌ها', 'Total cores'],
    agTunName: ['تانل', 'Tunnel'],
    agDstSrv: ['سرور مقصد', 'Destination'], agMotor: ['هسته', 'Engine'], agWeight: ['وزن', 'Weight'],
    agToday: ['ترافیک امروز', 'Today traffic'], agLatency: ['تأخیر', 'Latency'], agViewDtl: ['مشاهده جزئیات', 'View details'],
    heroAgT: ['اولین ایجنت را وصل کن', 'Connect your first agent'],
    heroAgD: ['دستور نصب را روی سرور جدید اجرا کن؛ ایجنت خودش ثبت‌نام می‌کند و همین‌جا ظاهر می‌شود.', 'Run the install command on a new server; the agent enrolls itself and appears here.'],
    heroTopoT: ['اولین توپولوژی را بساز', 'Build your first topology'],
    heroTopoD: ['نقش سرورها را تعیین کن و تونل‌ها را بین آن‌ها متصل کن.', 'Set the server roles, then connect tunnels between them.'],
    heroDocAg: ['ایجنت چیست و چه کاربردی دارد؟', 'What is an agent and what is it for?'],
    heroDocTopo: ['توپولوژی چیست و چه کاربردی دارد؟', 'What is a topology and what is it for?'],
    heroDocS: ['مطالعهٔ مقدماتی در مستندات', 'Read the primer in the docs'],
    agPerPage: ['موارد در هر صفحه', 'Items per page'], agPrev: ['قبلی', 'Previous'], agNext: ['بعدی', 'Next'],
    agNewSub: ['یک توکن یک‌بارمصرف می‌سازیم؛ دستور نصب را روی سرور جدید اجرا کنید تا ایجنت همین‌جا ظاهر شود.', 'We mint a one-time enrollment token; run the install command on the new server and the agent appears here.'],
    agRoleIranD: ['سرور داخل — سمت هاب', 'Domestic — hub side'],
    agRoleForeignD: ['سرور خارج — کشور را انتخاب کنید', 'Foreign server — pick the country'],
    agNameHint: ['اسم قابل تشخیص بگذارید؛ بعداً از صفحه مدیریت ایجنت قابل تغییر است.', 'Pick a recognizable name; you can rename it later on the agent page.'],
    agNamePh: ['تهران-۳', 'tehran-3'],
    agStName: ['نام', 'Name'], agStSide: ['نقش', 'Side'], agStTtl: ['زمان توکن', 'Token lifetime'],
    agWizNext: ['بعدی', 'Next'], agWizBack: ['قبلی', 'Back'],
    agNameReq: ['اسم ایجنت را وارد کنید', 'Enter the agent name'],
    agActMore: ['عملیات', 'Actions'],
    agTtlHint: ['توکن بعد از این روزها منقضی می‌شود؛ پیش‌فرض ۳۶۵ روز است.', 'The token expires after this many days — 365 by default.'],
    agDaysU: ['روز', 'days'],
    agTokenOk: ['توکن ساخته شد — فقط یک بار قابل استفاده است', 'Token minted — valid for a single use'],
    agCopyCmd: ['کپی دستور', 'Copy command'],
    magt: ['ایجنت‌ها', 'Agents'], malert: ['هشدارها', 'Alerts'],
    mbill: ['صورت‌حساب', 'Billing'],
    back: ['بازگشت', 'Back'], detailHint: ['برای جزئیات کلیک کنید', 'Click for details'],
    newTopo: ['+ توپولوژی جدید', '+ New topology'], topoName: ['نام', 'Name'],
    newAgent: ['+ افزودن ایجنت', '+ Add agent'], agentRole: ['نقش سرور', 'Server role'],
    agentLabel: ['نام ایجنت', 'Agent name'], agentExpiry: ['اعتبار توکن (روز)', 'Token validity (days)'],
    mint: ['ساخت توکن', 'Mint token'], agentToken: ['توکن ثبت (یک‌بار مصرف)', 'Enrollment token (single-use)'],
    installCmd: ['دستور نصب روی سرور جدید', 'Install command for the new server'],
    copy: ['کپی', 'Copy'], copied: ['کپی شد', 'Copied'],
    agentHint: ['این دستور را روی سرور جدید اجرا کنید؛ ایجنت خودش ثبت‌نام می‌کند و همین‌جا ظاهر می‌شود.', 'Run this on the new server; the agent enrolls itself and appears here.'],
    howTitle: ['ایجنت چطور اضافه می‌شود؟', 'How agents are added'],
    topoNameHint: ['نام انگلیسی (حروف کوچک، رقم، خط تیره)', 'latin slug (a-z, 0-9, dash)'],
    iranSrv: ['سرورهای ایران', 'Iran servers'], forSrv: ['سرورهای خارج', 'Foreign servers'],
    tpp: ['تونل به ازای هر جفت', 'Tunnels per pair'], engines: ['موتورها', 'Engines'],
    create: ['ایجاد', 'Create'], cancel: ['انصراف', 'Cancel'],
    actions: ['عملیات', 'Actions'], del: ['حذف', 'Delete'], delArm: ['تأیید حذف؟', 'Confirm delete?'],
    delDone: ['حذف شد', 'Deleted'], created: ['ایجاد شد', 'Created'],
    enginesCol: ['موتورها', 'Engines'], weightCol2: ['Σ وزن', 'Σ weight'],
    up: ['بالا', 'up'], down: ['قطع', 'down'],
    pickBoth: ['حداقل یک سرور ایران و یک سرور خارج انتخاب کنید', 'Pick at least one Iran and one foreign server'],
    badName: ['نام بایدslug انگلیسی باشد: a-z 0-9 -', 'Name must be a latin slug: a-z 0-9 -'],
    members: ['اعضا', 'Members'], tunnelsN: ['تونل‌ها', 'Tunnels'],
    enabled: ['فعال', 'Enabled'], disabled: ['غیرفعال', 'Disabled'],
    st_healthy: ['سالم', 'healthy'], st_degraded: ['کاهش‌کیفیت', 'degraded'],
    st_down: ['قطع', 'down'], st_disabled: ['غیرفعال', 'disabled'],
    st_unknown: ['نامشخص', 'unknown'], st_online: ['آنلاین', 'online'],
    st_offline: ['آفلاین', 'offline'], st_revoked: ['باطل‌شده', 'revoked'],
    up: ['برقرار', 'up'], down: ['قطع', 'down'],
    trafficToday: ['ترافیک امروز', 'Traffic today'], today: ['امروز', 'Today'],
    d7: ['۷ روز', '7d'], d30: ['۳۰ روز', '30d'],
    tunnelKey: ['تونل', 'Tunnel'], topologyCol: ['توپولوژی', 'Topology'],
    engineCol: ['موتور', 'Engine'], engineDef: ['پیش‌فرض', 'default'],
    weightCol: ['وزن', 'Weight'], shareCol: ['سهم', 'Share'],
    stateCol: ['وضعیت', 'State'], peerCol: ['طرف مقابل', 'Peer'],
    billable: ['دانلود قابل‌قبض', 'Billable download'], upload: ['آپلود', 'Upload'],
    payloadDown: ['دانلود بار', 'Payload download'], overhead: ['سربار', 'Overhead'],
    priceHint: ['ضریب راهنما', 'Price hint'], suggested: ['ضریب پیشنهادی', 'Suggested coefficient'],
    qualityCol: ['کیفیت داده', 'Data quality'], qualityClean: ['کامل', 'complete'],
    qualityGaps: ['{n} شکاف، بلندترین {s}', '{n} gap(s), longest {s}'],
    qualityOffline: ['{n} ایجنت آفلاین', '{n} agent(s) offline'],
    dcs: ['دیتاسنترها', 'Datacenters'],
    serverCol: ['سرور', 'Server'], dcCol: ['دیتاسنتر', 'Datacenter'],
    agentCol: ['ایجنت', 'Agent'], roleCol: ['نقش', 'Role'],
    versionCol: ['نسخه', 'Version'], archCol: ['معماری', 'Arch'],
    lastSeenCol: ['آخرین حضور', 'Last seen'],
    cpuCol: ['پردازنده', 'CPU'], memCol: ['حافظه', 'RAM'], diskCol: ['دیسک', 'Disk'],
    roleIran: ['ایران', 'Iran'], roleForeign: ['خارج', 'Foreign'],
    agentCountry: ['کشور میزبان', 'Hosting country'],
    pickCountry: ['کشور میزبان را انتخاب کنید', 'Pick a hosting country'],
    alertsOpen: ['هشدارهای باز', 'Open alerts'],
    alertsHistory: ['اعلان‌های اخیر', 'Recent notifications'],
    alertsNone: ['هیچ هشداری نیست — همه چیز آرام است.', 'No alerts — all quiet.'],
    sevCrit: ['بحرانی', 'critical'], sevWarn: ['هشدار', 'warning'],
    alertsEnabled: ['هشدارها فعال', 'Alerts enabled'], alertsOff: ['هشدارها خاموش', 'Alerts disabled'],
    ruleOffline: ['ایجنت آفلاین بعد از (دقیقه)', 'Agent offline after (min)'],
    ruleCert: ['هشدار انقضای گواهی (روز)', 'Cert expiry warn (days)'],
    ruleDisk: ['حداکثر دیسک (%)', 'Disk max (%)'],
    ruleCol: ['قاعده', 'Rule'], msgCol: ['پیام', 'Message'], timeCol: ['زمان', 'Time'],
    historyHint: ['اعلان‌های اخیر، شامل بازیابی‌ها', 'Recent notifications, including recoveries'],
    billMonth: ['ماه شمسی', 'Jalali month'], billLoad: ['نمایش گزارش', 'Load report'],
    billCsv: ['دانلود CSV', 'Download CSV'],
    billNone: ['در این بازه ردیفی نیست.', 'No rows in this period.'],
    billNote: ['همهٔ اعداد بایت واقعی‌اند؛ هیچ ضریبی اعمال نمی‌شود.', 'Every number is a real byte; no coefficient is ever applied.'],
    billBillableNote: ['دانلود قابل‌قبض = بایت‌های wire دریافتی در سمت ایران.', 'Billable download = wire bytes received on the Iran side.'],
    csvOk: ['CSV دانلود شد.', 'CSV downloaded.'],
    csvFail: ['دانلود CSV ناموفق بود.', 'CSV download failed.'],
    toposEmpty: ['هنوز توپولوژی‌ای ثبت نشده است.', 'No topologies yet.'],
    tunnelsEmpty: ['در این نما تونلی نیست.', 'No tunnels to show.'],
    agentsEmptyT: ['ایجنتی ثبت نشده است.', 'No agents enrolled yet.'],
    errTitle: ['بارگذاری این بخش ناموفق بود', 'Could not load this page'],
    retry: ['تلاش دوباره', 'Retry'],
    expired: ['نشست منقضی شده است — صفحه را دوباره بارگذاری کنید.', 'Session expired — reload the page.'],
    netDown: ['پنل در دسترس نیست — اتصال را بررسی کنید.', 'The panel is unreachable — check your connection.'],
    justNow: ['همین حالا', 'just now'], minAgo: ['دقیقه پیش', 'min ago'],
    hourAgo: ['ساعت پیش', 'h ago'], dayAgo: ['روز پیش', 'd ago']
  };
  function L(k) { var p = TXT[k] || [k, k]; return isFa() ? p[0] : p[1]; }
  function Lf(k, reps) {
    var s = L(k);
    Object.keys(reps || {}).forEach(function (key) { s = s.replace('{' + key + '}', reps[key]); });
    return s;
  }

  // ---- formatting ------------------------------------------------------------
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }
  function bytes(n) {
    if (n == null || isNaN(Number(n))) return '—';
    var u = ['B', 'KB', 'MB', 'GB', 'TB'], i = 0, v = Number(n);
    while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
    return v.toFixed(v >= 100 || i === 0 ? 0 : 1) + ' ' + u[i];
  }
  function num(v, d) { if (v == null || isNaN(Number(v))) return '—'; return Number(v).toFixed(d == null ? 1 : d); }
  function num0(v) { return String(v == null ? 0 : v); }
  function pct(v) { return v == null ? '—' : num(v, 1) + '%'; }
  function dur(sec) {
    if (sec == null || isNaN(Number(sec))) return '—';
    sec = Math.max(0, Number(sec));
    var d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600), m = Math.floor((sec % 3600) / 60);
    if (d > 0) return d + 'd ' + h + 'h';
    if (h > 0) return h + 'h ' + m + 'm';
    return m + 'm';
  }
  function fmtDate(tsSec) {
    if (!tsSec) return '—';
    var d = new Date(Number(tsSec) * 1000);
    if (isNaN(d.getTime())) return '—';
    try {
      return d.toLocaleString(isFa() ? 'fa-IR' : 'en-US-u-ca-persian-nu-latn',
        { timeZone: 'Asia/Tehran', dateStyle: 'medium', timeStyle: 'short' });
    } catch (e) { return d.toISOString(); }
  }
  function ago(tsSec) {
    if (!tsSec) return '—';
    var d = Math.max(0, Date.now() / 1000 - Number(tsSec));
    if (d < 60) return L('justNow');
    var m = Math.floor(d / 60); if (m < 60) return m + ' ' + L('minAgo');
    var h = Math.floor(m / 60); if (h < 24) return h + ' ' + L('hourAgo');
    return Math.floor(h / 24) + ' ' + L('dayAgo');
  }
  // current Jalali year-month in Asia/Tehran (billing month) — from mesh.js
  function currentJalaliMonth() {
    try {
      var parts = new Intl.DateTimeFormat('en-US-u-ca-persian-nu-latn',
        { timeZone: 'Asia/Tehran', year: 'numeric', month: '2-digit' }).formatToParts(new Date());
      var y = '', m = '';
      parts.forEach(function (p) { if (p.type === 'year') y = p.value; if (p.type === 'month') m = p.value; });
      if (/^\d{4}$/.test(y) && /^\d{2}$/.test(m)) return y + '-' + m;
    } catch (e) { /* fall through */ }
    return '';
  }

  // ---- native badges / states / skeleton / error card ------------------------
  function badge(kind, label, dot) {
    return '<span class="badge ' + kind + '">' + (dot ? '<span class="dot"></span>' : '') + esc(label) + '</span>';
  }
  var STATE_KIND = { healthy: 'ok', up: 'ok', online: 'ok', degraded: 'warn', down: 'err', offline: 'err', revoked: 'err' };
  function stateBadge(s) {
    var key = (s == null || s === '' || !TXT['st_' + s]) ? 'st_unknown' : ('st_' + s);
    return badge(STATE_KIND[s] || 'dim', L(key), true);
  }
  function qualityPill(q) {
    if (!q) return badge('ok', L('qualityClean'));
    var bits = [];
    if (q.gaps) bits.push(Lf('qualityGaps', { n: q.gaps, s: dur(q.longest_gap_s || 0) }));
    if (q.agents_offline) bits.push(Lf('qualityOffline', { n: q.agents_offline }));
    if (q.unknown_counters) bits.push('—');
    return bits.length
      ? '<span class="badge warn" title="' + esc(bits.join(' · ')) + '">' + esc(bits[0]) + '</span>'
      : badge('ok', L('qualityClean'));
  }
  function skel(n) {
    var h = '';
    for (var i = 0; i < (n || 3); i++) h += '<div class="ent-skel u-h-14"></div>';
    return '<div class="u-grid u-gap-8">' + h + '</div>';
  }
  var ICO_EMPTY = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor"><circle cx="12" cy="12" r="9"/><path d="M8 12h8"/></svg>';
  var ICO_ERR = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor"><path d="M12 3l10 18H2z"/><path d="M12 10v5M12 18h.01"/></svg>';
  function entEmpty(msg) {
    return '<div class="ent-empty"><span class="ent-empty-ic" aria-hidden="true">' + ICO_EMPTY + '</span><b>' + esc(msg) + '</b></div>';
  }
  function entError(err, retryFn) {
    var detail;
    if (err && err.status === 401) detail = L('expired');
    else if (err && err.net) detail = L('netDown');
    else if (err && err.ui && err.ui.title) detail = (isFa() ? err.ui.title.fa : err.ui.title.en) || err.message;
    else if (err && err.message) detail = err.message;
    else if (err) detail = String(err);
    return {
      html: '<div class="ent-empty ent-error" role="alert"><span class="ent-empty-ic" aria-hidden="true">' + ICO_ERR + '</span>' +
        '<b>' + esc(L('errTitle')) + '</b>' +
        (detail ? '<span class="ent-err-text">' + esc(String(detail).split('\n')[0].slice(0, 200)) + '</span>' : '') +
        (retryFn ? '<button type="button" class="quantum-btn quantum-btn-secondary u-btn-xs" data-mtb-retry>' + esc(L('retry')) + '</button>' : '') +
        '</div>',
      wire: function (root) {
        if (!retryFn || !root) return;
        var b = root.querySelector('[data-mtb-retry]');
        if (b) b.onclick = retryFn;
      }
    };
  }

  // ---- API (GET only; same-origin cookies; CSRF header like the legacy code)
  function apiSend(method, path, body) {
    return fetch(at(path), {
      method: method, credentials: 'same-origin',
      headers: { 'X-CSRF-Token': csrfToken(), 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body)
    }).then(function (r) {
      return r.text().then(function (txt) {
        var j = null;
        try { j = txt ? JSON.parse(txt) : null; } catch (e) { j = null; }
        if (!r.ok) {
          var err = new Error((j && (j.error || j.message)) || ('HTTP ' + r.status));
          err.ui = j && j.ui;
          err.status = r.status;
          err.body = j;
          throw err;
        }
        return j;
      });
    });
  }
  function apiGet(path) {
    return fetch(at(path), { method: 'GET', credentials: 'same-origin', headers: { 'X-CSRF-Token': csrfToken() } })
      .then(function (r) {
        return r.text().then(function (txt) {
          var j = null;
          try { j = txt ? JSON.parse(txt) : null; } catch (e) { j = null; }
          if (!r.ok) {
            var err = new Error((j && (j.error || j.error_code)) || ('HTTP ' + r.status));
            err.status = r.status;
            err.ui = j && j.ui;
            if (r.status >= 500 || r.status === 0) err.net = true;
            throw err;
          }
          return j;
        });
      })
      .catch(function (e) {
        if (e instanceof TypeError) { var ne = new Error('network'); ne.net = true; throw ne; }
        throw e;
      });
  }
  // bounded-concurrency runner for the progressive per-tunnel enrichment
  function pool(items, n, fn) {
    var i = 0;
    function next() {
      if (i >= items.length) return Promise.resolve();
      var it = items[i++];
      return Promise.resolve().then(function () { return fn(it); }).catch(function () { }).then(next);
    }
    var starters = [];
    for (var k = 0; k < Math.min(n, items.length); k++) starters.push(next());
    return Promise.all(starters);
  }
  function cssEscape(s) {
    try { if (window.CSS && CSS.escape) return CSS.escape(s); } catch (e) { /* fall through */ }
    return String(s).replace(/["\\\]]/g, '\\$&');
  }

  // ---- stylesheet (namespaced; everything else reuses legacy classes) --------
  var CSS = [
    'main.page section[id^="tab-m"] > .mtb-body > .tunnel-card + .tunnel-card { margin-top: 14px; }',
    '.mtb-body { min-width: 0; }',
    '.mtb-stats { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }',
    '.mtb-muted { color: var(--dim, var(--mu)); font-size: var(--fs-small, 12px); }',
    '.mtb-ver { display:inline-block; padding:1px 8px; border:1px solid rgba(148,163,184,.25); border-radius:999px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size:.8em; color:#94a3b8; background:rgba(148,163,184,.08); white-space:nowrap; }',
    '.mtb-card-click { cursor: pointer; }',
    '.mtr-chart text { stroke: none !important; fill: #cbd5e1 !important; font-family: ui-monospace, SFMono-Regular, Menlo, monospace !important; font-weight: 400 !important; text-rendering: geometricPrecision; }',
    '.mtb-actions { text-align: end; }',
    '.mtb-table td { vertical-align: middle; }',
    '.mtb-modal-ov { position:fixed; inset:0; background:rgba(0,0,0,.45); display:flex; align-items:center; justify-content:center; z-index:200; -webkit-backdrop-filter:blur(9px) saturate(120%); backdrop-filter:blur(9px) saturate(120%); animation:mtbOvIn .18s ease; }',
    '.mtb-modal-ov .mtb-modal { animation:mtbModalIn .18s ease; }',
    '@keyframes mtbOvIn { from { opacity:0; } to { opacity:1; } }',
    '@keyframes mtbModalIn { from { opacity:0; transform:scale(.97) translateY(4px); } to { opacity:1; transform:none; } }',
    '.mtb-modal { width:min(520px, 92vw); max-height:86vh; overflow:auto; background:var(--s1, #12161f); color:var(--tx, #e6e9ef); border:1px solid var(--ln, rgba(148,163,184,.25)); border-radius:16px; padding:16px 18px; box-shadow:0 18px 60px rgba(0,0,0,.5); }',
    '.mtb-x { background:color-mix(in srgb, var(--ac) 8%, transparent); border:1px solid color-mix(in srgb, var(--ac) 45%, transparent); color:var(--ac); font-size:17px; cursor:pointer; float:inline-end; width:34px; height:34px; border-radius:10px; display:inline-flex; align-items:center; justify-content:center; -webkit-backdrop-filter:blur(8px); backdrop-filter:blur(8px); transition:background .15s, border-color .15s; }',
    '.mtb-x:hover { background:color-mix(in srgb, var(--ac) 16%, transparent); border-color:var(--ac); }',
    '.mtb-form { display:flex; flex-direction:column; gap:10px; margin-top:8px; }',
    '.mtb-form label { font-size:13px; color:var(--tx2, #cbd5e1); display:block; }',
    '.mtb-checks { display:flex; flex-wrap:wrap; gap:8px 14px; }',
    '.mtb-check { display:inline-flex; align-items:center; gap:6px; font-size:13px; }',
    '.mtb-tokenrow { display:flex; align-items:center; gap:8px; }',
    '.mtb-token { flex:1; background:var(--s2, #161b26); color:var(--tx, #e6e9ef); border:1px solid var(--ln, rgba(148,163,184,.25)); border-radius:8px; padding:8px 10px; font-family:ui-monospace, Menlo, monospace; font-size:12px; overflow-wrap:anywhere; user-select:all; }',
    '.mtb-engines { display:flex; flex-wrap:wrap; gap:8px; }',
    '.mtb-acts { display:flex; gap:8px; justify-content:flex-end; margin-top:6px; }',
    '.quantum-input { background:var(--s2, #161b26); color:var(--tx, #e6e9ef); border:1px solid var(--ln, rgba(148,163,184,.25)); border-radius:8px; padding:6px 10px; }',
    '.mtb-card-click:focus-visible { outline: 2px solid var(--ac, var(--info)); outline-offset: 2px; }',
    '.mtb-key { display: inline-block; max-width: 250px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; vertical-align: bottom; }',
    '.mtb-pending { opacity: .55; }',
    '.mtb-month { max-width: 130px; }',
    '@media (max-width: 640px) { .mtb-key { max-width: 150px; } }',
    '@media (prefers-reduced-motion: reduce) { main.page section[id^="tab-m"] * { animation: none !important; transition: none !important; } }'
  ].join('\n');

  // ---- nav icons (same visual grammar as the legacy nav svgs) -----------------
  var ICO_TUN = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor"><path d="M3 8h13M13 5l3 3-3 3"/><path d="M21 16H8M11 13l-3 3 3 3"/></svg>';
  var ICO_AGT = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor"><rect x="3" y="5" width="18" height="12" rx="2"/><path d="M8 21h8M12 17v4"/><circle cx="8.5" cy="11" r="1.5"/><circle cx="15.5" cy="11" r="1.5"/><path d="M7 8h.01M17 8h.01"/></svg>';
  var ICO_ALERT = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor"><path d="M18 8a6 6 0 0 0-12 0c0 7-3 9-3 9h18s-3-2-3-9"/><path d="M13.7 21a2 2 0 0 1-3.4 0"/></svg>';
  var ICO_BILL = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor"><path d="M6 3h12v18l-2-1.5L14 21l-2-1.5L10 21l-2-1.5L6 21z"/><path d="M9 8h6M9 12h6M9 16h4"/></svg>';

  // ---- state -------------------------------------------------------------------
  var seq = { mtun: 0, magt: 0, malert: 0, mbill: 0 };
  var cache = { mtun: null, magt: null, billMonth: '' };
  var topoTraffic = {};            // tunnel_key -> {d7, d30} kept across re-renders
  var lastLegacy = '';             // id of the legacy panel visible before us

  function isMineId(id) { return MY_TABS.indexOf(id) >= 0; }
  function bodyOf(tab) {
    var sec = document.getElementById('tab-' + tab);
    return sec ? sec.querySelector('.mtb-body') : null;
  }
  function setBody(tab, html) {
    var b = bodyOf(tab);
    if (b) b.innerHTML = html;
  }
  function wireRetry(tab, wire) { var b = bodyOf(tab); if (b && wire) wire(b); }
  function visibleLegacyPanel() {
    var list = document.querySelectorAll('main.page section[role="tabpanel"]:not([hidden])');
    for (var i = 0; i < list.length; i++) {
      if (!isMineId((list[i].id || '').replace(/^tab-/, ''))) return list[i];
    }
    return null;
  }

  // ---- tab switching (own switcher + legacy coordination) ----------------------
  function markNav(name) {
    ['#sideNav', '#drawerNav'].forEach(function (sel) {
      var nav = document.querySelector(sel);
      if (!nav) return;
      nav.querySelectorAll('[data-tab]').forEach(function (b) {
        var tab = b.getAttribute('data-tab');
        if (isMineId(tab)) {
          var on = tab === name;
          b.classList.toggle('on', on);
          b.setAttribute('aria-selected', on ? 'true' : 'false');
          b.setAttribute('tabindex', on ? '0' : '-1');
        } else if (b.classList.contains('on')) {
          b.classList.remove('on');
          b.setAttribute('aria-selected', 'false');
          b.setAttribute('tabindex', '-1');
        }
      });
    });
  }
  function hideMine(keepMemory) {
    MY_TABS.forEach(function (id) {
      var s = document.getElementById('tab-' + id);
      if (s) s.hidden = true;
    });
    ['#sideNav', '#drawerNav'].forEach(function (sel) {
      var nav = document.querySelector(sel);
      if (!nav) return;
      nav.querySelectorAll('[data-tab]').forEach(function (b) {
        if (!isMineId(b.getAttribute('data-tab'))) return;
        b.classList.remove('on');
        b.setAttribute('aria-selected', 'false');
        b.setAttribute('tabindex', '-1');
      });
    });
    if (!keepMemory) lastLegacy = '';
  }
  function activate(name) {
    var sec = document.getElementById('tab-' + name);
    if (!sec) return;
    // hide (and remember) whatever legacy panel is currently visible
    var remembered = '';
    var list = document.querySelectorAll('main.page section[role="tabpanel"]:not([hidden])');
    Array.prototype.forEach.call(list, function (p) {
      if (isMineId((p.id || '').replace(/^tab-/, ''))) return;
      if (!remembered) remembered = p.id || '';
      p.hidden = true;
    });
    if (remembered) lastLegacy = remembered;
    MY_TABS.forEach(function (id) {
      var s = document.getElementById('tab-' + id);
      if (s) s.hidden = id !== name;
    });
    markNav(name);
    try {
      var agm = (location.hash || '').match(/[?&]ag=([^&]+)/);
      history.replaceState(null, '', '#t=' + name + (name === 'magt' && agm ? '&ag=' + agm[1] : ''));
    } catch (e) { /* best effort */ }
    loadTab(name); // lazy-load on first open, refresh on re-open
    if (window.topTitleSync) window.topTitleSync(name);
  }
  function onLegacyNavClick() {
    hideMine(true); // keep memory: goTab may not have restored the panel yet
    setTimeout(function () {
      if (!visibleLegacyPanel() && lastLegacy) {
        var el = document.getElementById(lastLegacy);
        if (el) el.hidden = false;
      }
      lastLegacy = '';
    }, 0);
  }
  // Guarded wrapper: the drawer clones and the command palette call goTab()
  // directly, so route "m*" names through our switcher and hide our sections
  // on every legacy switch. Self-contained: only patched when goTab is global.
  function patchGoTab() {
    try {
      if (typeof window.goTab !== 'function' || window.goTab.__mtb) return;
      var orig = window.goTab;
      var wrapped = function (name) {
        if (name && isMineId(name)) return activate(name);
        hideMine(false);
        return orig.apply(this, arguments);
      };
      wrapped.__mtb = true;
      window.goTab = wrapped;
    } catch (e) { /* stay with delegation only */ }
  }

  // ---- shared table cell helpers ------------------------------------------------
  function tdNum(inner) { return '<td class="num"><bdi dir="ltr">' + inner + '</bdi></td>'; }
  function tdKey(key) {
    return '<td class="mono" dir="ltr"><span class="mtb-key" title="' + esc(key) + '">' + esc(key) + '</span></td>';
  }
  function pair(local, remote) {
    return '<bdi dir="ltr">' + esc(local || '—') + ' ↔ ' + esc(remote || '—') + '</bdi>';
  }

  // ===========================================================================
  // 1) Topologies — list + detail (back button returns to the list)
  // ===========================================================================
  function loadTun() {
    var my = (seq.mtun = seq.mtun + 1);
    setBody('mtun', skel(4));
    apiGet('api/mesh/topologies').then(function (j) {
      if (seq.mtun !== my) return;
      var topos = (j && j.topologies) || [];
      return Promise.all(topos.map(function (tp) {
        return apiGet('api/mesh/topologies/' + tp.id).catch(function () { return null; }).then(function (det) {
          return apiGet('api/ui/topologies/' + tp.id).catch(function () { return null; }).then(function (ui) {
            return { tp: tp, det: det, ui: ui };
          });
        });
      })).then(function (packs) {
        if (seq.mtun !== my) return;
        cache.mtun = packs;
        renderTun(my);
      });
    }).catch(function (e) {
      if (seq.mtun !== my) return;
      var card = entError(e, loadTun);
      setBody('mtun', card.html); wireRetry('mtun', card.wire);
    });
  }
  function renderTun(my) {
    var packs = cache.mtun || [];
    var rows = [];
    packs.forEach(function (pk) {
      if (!pk || !pk.det || !pk.tp) return;
      var weights = {};
      (pk.det.weights || []).forEach(function (w) { weights[w.tunnel_id] = w.weight; });
      var uiByKey = {};
      ((pk.ui && pk.ui.tunnels) || []).forEach(function (t) { uiByKey[t.tunnel_key] = t; });
      (pk.det.tunnels || []).forEach(function (t) {
        var u = uiByKey[t.tunnel_key] || {};
        rows.push({
          key: t.tunnel_key,
          topo: pk.tp.name || ('#' + pk.tp.id),
          engine: t.engine,
          enabled: t.enabled !== false,
          weight: weights[t.id] != null ? weights[t.id] : (u.weight != null ? u.weight : null),
          state: u.state != null ? u.state : null
        });
      });
    });
    rows.sort(function (a, b) { return (a.topo + a.key).localeCompare(b.topo + b.key); });
    if (!rows.length) { setBody('mtun', '<div class="tunnel-card">' + entEmpty(L('tunnelsEmpty')) + '</div>'); return; }
    var trs = rows.map(function (r) {
      var d7 = (topoTraffic[r.key] || {}).d7, d30 = (topoTraffic[r.key] || {}).d30;
      return '<tr data-mtb-row="' + esc(r.key) + '">' +
        tdKey(r.key) +
        '<td>' + esc(r.topo) + '</td>' +
        '<td>' + esc(r.engine || '—') + (r.engine === 'matrix' ? ' ' + badge('info', L('engineDef')) : '') + '</td>' +
        tdNum(r.weight != null ? num0(r.weight) : '—') +
        '<td>' + stateBadge(r.state != null ? r.state : (r.enabled ? 'unknown' : 'disabled')) + '</td>' +
        '<td class="num' + (d7 ? '' : ' mtb-pending') + '" data-mtb-d7>' + (d7 ? '<bdi dir="ltr">' + esc(bytes(d7.wire_rx_bytes) + ' / ' + bytes(d7.wire_tx_bytes)) + '</bdi>' : '…') + '</td>' +
        '<td class="num' + (d30 ? '' : ' mtb-pending') + '" data-mtb-d30>' + (d30 ? '<bdi dir="ltr">' + esc(bytes(d30.wire_rx_bytes) + ' / ' + bytes(d30.wire_tx_bytes)) + '</bdi>' : '…') + '</td>' +
        '</tr>';
    }).join('');
    setBody('mtun',
      '<div class="tunnel-card"><div class="tc-header"><p class="tc-title">' + esc(L('mtun')) + '</p>' +
      '<span class="mtb-muted">' + rows.length + '</span></div>' +
      '<p class="qs-hint">' + esc(L('d7')) + ' / ' + esc(L('d30')) + ' — wire ↓/↑</p>' +
      '<div class="ent-tbl-wrap"><table class="ent-tbl"><thead><tr>' +
      '<th>' + esc(L('tunnelKey')) + '</th><th>' + esc(L('topologyCol')) + '</th>' +
      '<th>' + esc(L('engineCol')) + '</th><th class="num">' + esc(L('weightCol')) + '</th>' +
      '<th>' + esc(L('stateCol')) + '</th>' +
      '<th class="num">' + esc(L('d7')) + ' ↓/↑</th><th class="num">' + esc(L('d30')) + ' ↓/↑</th>' +
      '</tr></thead><tbody>' + trs + '</tbody></table></div></div>');
    void my;
    // progressive 7d/30d fill: ONE GET per tunnel returns both windows
    pool(rows.map(function (r) { return r.key; }), 4, function (key) {
      return apiGet('api/ui/tunnels/' + encodeURIComponent(key) + '?range=7d').then(function (j) {
        var tot = (j && j.totals) || {};
        topoTraffic[key] = { d7: tot['7d'] || null, d30: tot['30d'] || null };
        if (seq.mtun !== my) return;
        var trEl = document.querySelector('tr[data-mtb-row="' + cssEscape(key) + '"]');
        if (!trEl) return;
        var t7 = topoTraffic[key].d7, t30 = topoTraffic[key].d30;
        var c7 = trEl.querySelector('[data-mtb-d7]'), c30 = trEl.querySelector('[data-mtb-d30]');
        if (c7) { c7.classList.remove('mtb-pending'); c7.innerHTML = t7 ? '<bdi dir="ltr">' + esc(bytes(t7.wire_rx_bytes) + ' / ' + bytes(t7.wire_tx_bytes)) + '</bdi>' : '—'; }
        if (c30) { c30.classList.remove('mtb-pending'); c30.innerHTML = t30 ? '<bdi dir="ltr">' + esc(bytes(t30.wire_rx_bytes) + ' / ' + bytes(t30.wire_tx_bytes)) + '</bdi>' : '—'; }
      });
    });
  }

  // ===========================================================================
  // 3) Agents
  // ===========================================================================
  function loadAgt() {
    if (agtView.view === 'detail') return; // the management view owns the tab
    var my = (seq.magt = seq.magt + 1);
    setBody('magt', skel(3));
    apiGet('api/ui/agents').then(function (j) {
      if (seq.magt !== my) return;
      cache.magt = (j && j.agents) || [];
      renderAgt();
    }).catch(function (e) {
      if (seq.magt !== my) return;
      var card = entError(e, loadAgt);
      setBody('magt', card.html); wireRetry('magt', card.wire);
    });
  }
  function agentHeader() {
    var ags = (cache && cache.magt) || [];
    var on = 0;
    ags.forEach(function (a) { if (a.status === 'online') on++; });
    var stats = '';
    if (ags.length) {
      stats = '<div class="mtb-agstatbig">' +
        '<div class="mtb-agstat-i all"><span class="mtb-agico">' + MTB_ICO.stack + '</span><span class="mtb-agtx"><b>' + num0(ags.length) + '</b><span>' + esc(L('agStatAll')) + '</span></span></div>' +
        '<div class="mtb-agstat-i ok"><span class="mtb-agico">' + MTB_ICO.okc + '</span><span class="mtb-agtx"><b>' + num0(on) + '</b><span>' + esc(L('agStatOn')) + '</span></span></div>' +
        '<div class="mtb-agstat-i err"><span class="mtb-agico">' + MTB_ICO.alerc + '</span><span class="mtb-agtx"><b>' + num0(ags.length - on) + '</b><span>' + esc(L('agStatOff')) + '</span></span></div>' +
        '</div>';
    }
    return '<div class="tunnel-card mtb-agintro">' +
      '<div class="mtb-agintro-row">' +
      '<button type="button" class="quantum-btn quantum-btn-primary" id="mtbNewAgent">' + esc(L('newAgent')) + '</button>' +
      '<span class="mtb-agdiv"></span>' +
      '<div class="mtb-agres disk"><span class="mtb-agico">' + MTB_ICO.drive + '</span><span class="mtb-agtx"><b id="agSumDisk">—</b><span>' + esc(L('agSumDisk')) + '</span></span></div>' +
      '<div class="mtb-agres ram"><span class="mtb-agico">' + MTB_ICO.ramstick + '</span><span class="mtb-agtx"><b id="agSumRam">—</b><span>' + esc(L('agSumRam')) + '</span></span></div>' +
      '<div class="mtb-agres cores"><span class="mtb-agico">' + MTB_ICO.cpuchip + '</span><span class="mtb-agtx"><b id="agSumCores">—</b><span>' + esc(L('agSumCores')) + '</span></span></div>' +
      '<span class="mtb-agdiv"></span>' +
      stats +
      '</div></div>';
  }
  // Fleet resource totals for the intro strip: the overview endpoint
  // aggregates every agent (server-cached, cheap) — disk/RAM/cores.
  // Called after every renderAgt so the strip refills on rerenders.
  function agSumFill() {
    fetch('api/ui/overview').then(function (r) { return r.json(); }).then(function (ov) {
      var rr = (ov.resources || {}), mem = rr.mem || {}, dk = rr.disk || {};
      var set = function (id, v) { var el = document.getElementById(id); if (el && v != null) el.textContent = v; };
      set('agSumDisk', dk.sum_total_gb != null ? bytes(dk.sum_total_gb * 1073741824) : null);
      set('agSumRam', mem.sum_total_mb != null ? bytes(mem.sum_total_mb * 1048576) : null);
      set('agSumCores', rr.cores_total != null ? num0(rr.cores_total) : null);
    }).catch(function () {});
  }
  // Embedded 40px flag PNGs (flagcdn, public domain) — Chrome on Windows
  // renders regional-indicator emoji as bare letters, so real images win.
  var FLAGS = {
    IR: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAACgAAAAXCAMAAABODP0nAAAAVFBMVEXaAAAjn0D////wkZHkSUme1KtfunRduHOo2bTjRERYtW7xmpriPT2k17DujIzxl5f+9PT96enxnZ30sLBDrVzfJyeKy5nrd3fsfn7tg4P4y8v0s7NKuoUCAAAAkElEQVQoz+WPRxLDMAhFlSBLVrOae+5/zyCcsrAXrBPKvIH/FyBuzBA/ZRyVtlprq9QbloDdoOyhj2LuuxC6nvKF7xw+8yzu58j5Ynk2pr0+9sQwuhpLLQxjWcGtHCNsw7AB58bsfebciF+nq68XBxAhDggAR4jQgI1FAuqLmKSRHkvKBmMaDEHS1hz6JP4ynkIfCksRzajdAAAAAElFTkSuQmCC",
    DE: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAACgAAAAYAgMAAAD16ldTAAAACVBMVEUAAADdAAD/zgDGIigcAAAAFUlEQVQY02NgoBEIhQPqMlfBAVWZANYST7G6bxb2AAAAAElFTkSuQmCC",
    FR: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAACgAAAAbBAMAAAD8PtBdAAAAD1BMVEUAJlT////OESbvr7aqtsYrjF9NAAAAGUlEQVQoz2NgAAMWQTAwUgIDhlHBUUG8ggBpKyX5efKDZQAAAABJRU5ErkJggg==",
    NL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAACgAAAAbAgMAAABzfiX9AAAACVBMVEX///8hRouuHChfbmbQAAAAFUlEQVQY02NYBQcMtGLSAYTCAa2YAJQLWaccQRfwAAAAAElFTkSuQmCC",
    FI: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAACgAAAAYBAMAAAB6qqLzAAAAElBMVEW/ytoAL2z///8bRHw2W4sBL2zkHzVXAAAAKElEQVQY02NQAgFlQUERJQRgGISCYMAMFGRAAoKYQJR+glidNETCEwBPSDATCt0c6gAAAABJRU5ErkJggg==",
    GB: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAACgAAAAUCAMAAADImI+JAAAANlBMVEX////w8vfyxcx8jbJCWpAOLHFVa5sBIWnIEC7WTGLnv8qAkLTheYpecqHx0digrMfz3+T21dqBS5WQAAAApUlEQVQoz8WTyxpFMAyEpyqOuhTv/7IntImmurAzC59kfsIIhA5wnn6iASMLgzbIT2Fk6joUaAUKBsiJm+kJ0iyuR1mQBam8Cex1dINkZ6EesSZwrR8JfdYWl7AcbCRw5ypytYmPpy6w3X6jL8HXL6PxOODgTPYEcnXmFe94cuBsdJzwpIFrRwK3WPkJLQqL2aUoUVisXjOvLizWWNyMVljrV0gz/wdhEi0lRIbcAAAAAElFTkSuQmCC",
    US: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAACgAAAAVBAMAAADGNLEtAAAALVBMVEX////GU3LZjKGzGUINM2PsxtAYPWotT3hed5enma46WoBPa46zGkMkR3Jrgp+6ntXWAAAAiklEQVQY02Nwc0lJcQMSxghgw+CRVdaWkVXWooQEGFxaPLxKWjxcGJCBn1fJExAWRAJglUuAKlEEPdyXgA1A0e62LTu9bFt2CopF3iCh9LItKE5yX5tVe31tVgmq9pL0NLcy95RQJMAAdiUQo2ifCQUoKrECY0xgw6CEBWDXLogFYBfECrBahM1JAItMTcEiMDpsAAAAAElFTkSuQmCC",
    CA: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAACgAAAAUCAMAAADImI+JAAAASFBMVEX/////ubn/oqL/Z2f/Jib/09P/7e3/AAD/+/v/Gxv/W1v/rq7/eXn/nZ3/DAz/OTn/AwP/Pj7//Pz/QED/PT3/g4P/xsb/xcWY5npoAAAAkUlEQVQoz43TyQ7DIAxFUQbDNZCx4///aTdt00ohsXdPHIFsGce33E79nO5AvRjhGCxQVYs41fMbpUIVw9MzwHwKk4sA0aUj6IdW7iNAGEobfBfmBbgC3IAld6HKllhEuzDwV6HfjJ+2OPmjrlP5pJKOx5PfsuSTOWZZY61xlWxYiufDuD3eG2FrRpiSEe5+hRcblQ+AegtNbgAAAABJRU5ErkJggg==",
    TR: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAACgAAAAbCAMAAAA5zj1cAAAARVBMVEXjChf70dPqRlDnMDvnLDflIi383N7jCxj////kFCHmJjL4v8Pwe4LvbnbtZW3sUlv+8PHlHSn0naL3trrpO0XyjZPyiZBeDlMuAAAAjUlEQVQ4y+WTyxKDIAwAw0NYQETR9v8/tQenehHl3ObE7OxAQhLx0hVeflZctOkQ/egAlf2DODhQU1zKdC/6BHk/xVvxBfWgw9AUPRAOapNYcy0aUCfVzC5ciwXWLwsVKI2nI7iTbrNTjRtl7sxRRnj3VC2SYP/qYB86o6BuIY65r9er7pse+4c7cyd+AOKjBXOHX0j9AAAAAElFTkSuQmCC",
    AE: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAACgAAAAUBAMAAAANaGKIAAAAElBMVEUAAAAAhD1UVFRVrX7IEC7///8VlrW7AAAAJElEQVQY02NwAQFBVMBAX0FjVAARDEUFdBZUQgUQQQY0QC9BAGh6Q2h5ljhMAAAAAElFTkSuQmCC",
    RU: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAACgAAAAbAgMAAABzfiX9AAAACVBMVEUAOabVKx7///95ANL1AAAAFUlEQVQY02NYBQcMtGLSAYTCAa2YAJQLWaccQRfwAAAAAElFTkSuQmCC",
    SG: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAACgAAAAbCAMAAAA5zj1cAAAAPFBMVEXtKTnvRFL5srj3oajvPEryW2fuM0L////2k5vtKjr1hY30doH+6On2lJzzbXjwTlv6wcb7ztL82Nv/9/iW1qa7AAAAe0lEQVQ4y+3RORIDIQxE0Q9IaATMfv+7OnDgyINyu+NXqmo1TijOG3pSicB9DAWZwn6fAi3XCazDNoStPdx0HNRu12OHevRH2OxCSgWaPsJuA/DPD77BZCakkmrRSetimXQKeZtAv6wHl9nXrDUCA1uvSygrFswf/g58AZjBEs/aTMUHAAAAAElFTkSuQmCC",
    JP: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAACgAAAAbCAMAAAA5zj1cAAAAOVBMVEW8AC3dfJPMPF7ILFHQTGvuvcjADzn////55urjkaXyzte9BTHqrbvDHEP++fr77/Hst8PmobHXZ4H4lhdsAAAAcUlEQVQ4y9XTuw6AIAyF4XKVclXf/2FNjDF0OMjgwj9/YWgLbZPRkjA4Zhc+YbB0Z8MYHp6efB7BmOgtRQyroQ6aCmEm0Q6hklBBqCXUEHoJPYRm9kUr4QkhS8gQttK70vBmHM1tRkyS/7me+Xtc+hdeTWcZDIhvaeAAAAAASUVORK5CYII="
  };
  var COUNTRIES_FA = {
    DE: "آلمان",
    FR: "فرانسه",
    NL: "هلند",
    FI: "فنلاند",
    GB: "انگلیس",
    US: "آمریکا",
    CA: "کانادا",
    TR: "ترکیه",
    AE: "امارات",
    RU: "روسیه",
    SG: "سنگاپور",
    JP: "ژاپن"
  };
  var COUNTRIES = {
    DE: "Germany",
    FR: "France",
    NL: "Netherlands",
    FI: "Finland",
    GB: "United Kingdom",
    US: "United States",
    CA: "Canada",
    TR: "Turkey",
    AE: "UAE",
    RU: "Russia",
    SG: "Singapore",
    JP: "Japan"
  };
  function CN(cc) { return (isFa() ? COUNTRIES_FA : COUNTRIES)[cc] || ''; }
  function flagImg(code) {
    var src = FLAGS[code];
    if (!src) return '';
    return '<img src="' + src + '" alt="" width="18" height="13" style="border-radius:2px;vertical-align:-2px;margin:0 2px">';
  }
  function flagBadge(kind, code, text) {
    return '<span class="badge ' + kind + '">' + flagImg(code) + ' ' + esc(text) + '</span>';
  }
  function countryOpts() {
    return '<option value="">—</option>' + Object.keys(COUNTRIES).map(function (c) {
      return '<option value="' + c + '">' + esc(CN(c)) + '</option>';
    }).join('');
  }
  function agentRoleCell(a) {
    var role = a.role || '';
    if (role === 'iran' || role.indexOf('ایران') === 0) {
      return '<span class="badge info">' + topologyFlagSvg('ir') + ' ' + esc(L('roleIran')) + '</span>';
    }
    if (role === 'foreign' || role.indexOf('خارج') === 0) {
      var cc = a.country ? String(a.country).toLowerCase() : '';
      var c = CN(a.country);
      if (c) return '<span class="badge u-pill-violet">' + topologyFlagSvg(cc) + ' ' + esc(c) + '</span>';
      return '<span class="badge u-pill-violet">' + topologyFlagSvg(cc) + ' ' + esc(L('roleForeign')) + '</span>';
    }
    return role ? badge('dim', role) : '—';
  }
  function openAgentModal() {
    var old = document.getElementById('mtbAgentModal');
    if (old) old.remove();
    var GLOBE = '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M3 12h18"/><path d="M12 3c2.7 2.7 4 5.8 4 9s-1.3 6.3-4 9c-2.7-2.7-4-5.8-4-9s1.3-6.3 4-9z"/></svg>';
    var PLUS = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14M5 12h14"/></svg>';
    var USER = '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="8" r="3.2"/><path d="M5.5 19a6.5 6.5 0 0 1 13 0"/></svg>';
    var CHECK = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 6L9 17l-5-5"/></svg>';
    var chips = [30, 90, 180, 365].map(function (d) {
      return '<button type="button" class="mtb-chip' + (d === 365 ? ' on' : '') + '" data-days="' + d + '">' + num0(d) + ' ' + esc(L('agDaysU')) + '</button>';
    }).join('');
    var ov = document.createElement('div');
    ov.id = 'mtbAgentModal';
    ov.className = 'mtb-modal-ov';
    ov.innerHTML = '<div class="mtb-modal" role="dialog" aria-modal="true">' +
      '<div class="tc-header"><div class="mtb-wiz-hero"><span class="mtb-wiz-plus">' + PLUS + '</span><div class="mtb-wiz-hero-tx"><p class="tc-title">' + esc(L('newAgent')) + '</p></div></div>' +
      '<button type="button" class="mtb-x" data-mtb-close>\u00d7</button></div>' +
      '<div class="mtb-wiz" id="mtbWiz">' +
      '<div class="mtb-wstep on" data-st="1"><span class="mtb-wstep-dot">1</span><span class="mtb-wstep-lbl">' + esc(L('agStName')) + '</span></div>' +
      '<span class="mtb-wline"></span>' +
      '<div class="mtb-wstep" data-st="2"><span class="mtb-wstep-dot">2</span><span class="mtb-wstep-lbl">' + esc(L('agStSide')) + '</span></div>' +
      '<span class="mtb-wline"></span>' +
      '<div class="mtb-wstep" data-st="3"><span class="mtb-wstep-dot">3</span><span class="mtb-wstep-lbl">' + esc(L('agStTtl')) + '</span></div>' +
      '</div>' +
      '<div class="mtb-form" id="mtbAgentForm">' +
      '<div class="mtb-wpage" data-pg="1">' +
      '<div class="mtb-field"><span class="mtb-field-ico">' + USER + '</span>' +
      '<input id="mtbAgentLabel" class="quantum-input" placeholder="' + esc(L('agNamePh')) + '" dir="ltr" aria-label="' + esc(L('agentLabel')) + '">' +
      '<span class="mtb-field-ok" id="mtbNameOk" hidden>' + CHECK + '</span>' +
      '<button type="button" class="mtb-tip-i mtb-tip-infield" data-tipopen data-tip="' + esc(L('agNameHint')) + '" aria-label="' + esc(L('agNameHint')) + '">!</button></div>' +
      '<div class="mtb-hint-row"><span class="mtb-hint-ico"><svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 8h.01M12 11.5V16"/></svg></span><span class="mtb-hint-tx">' + esc(L('agNameHint')) + '</span></div>' +
      '</div>' +
      '<div class="mtb-wpage" data-pg="2" hidden>' +
      '<span class="mtb-fl">' + esc(L('agentRole')) + '</span>' +
      '<div class="mtb-agent-cards">' +
      '<label class="mtb-agent-card on"><input type="radio" name="mtbRole" value="iran" checked>' +
      '<span class="mtb-agc-ico">' + topologyFlagSvg('ir') + '</span>' +
      '<span class="mtb-agc-tx"><b>' + esc(L('roleIran')) + '</b><i>' + esc(L('agRoleIranD')) + '</i></span></label>' +
      '<label class="mtb-agent-card"><input type="radio" name="mtbRole" value="foreign">' +
      '<span class="mtb-agc-ico"><span class="mtb-glob">' + GLOBE + '</span></span>' +
      '<span class="mtb-agc-tx"><b>' + esc(L('roleForeign')) + '</b><i>' + esc(L('agRoleForeignD')) + '</i></span></label>' +
      '</div>' +
      '<label id="mtbCountryRow" style="display:none">' + esc(L('agentCountry')) + ' ' +
      '<div class="mtb-cdd" id="mtbCdd">' +
      '<button type="button" class="mtb-cdd-btn" id="mtbCddBtn">' +
      '<span class="mtb-cdd-cur" id="mtbCddCur">\u2014</span>' +
      '<span class="mtb-cdd-arrow"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 9l6 6 6-6"/></svg></span>' +
      '</button>' +
      '<div class="mtb-cdd-pop" id="mtbCddPop" hidden>' +
      '<button type="button" class="mtb-cdd-opt" data-cc="">\u2014</button>' +
      Object.keys(COUNTRIES).map(function (cc) {
        return '<button type="button" class="mtb-cdd-opt" data-cc="' + cc + '">' + flagImg(cc) + ' ' + esc(CN(cc)) + '</button>';
      }).join('') +
      '</div></div>' +
      '<input type="hidden" id="mtbCountry" value=""></label>' +
      '</div>' +
      '<div class="mtb-wpage" data-pg="3" hidden>' +
      '<div class="mtb-tip-row"><button type="button" class="mtb-tip-i" data-tipopen data-tip="' + esc(L('agTtlHint')) + '" aria-label="' + esc(L('agTtlHint')) + '">!</button></div>' +
      '<label>' + esc(L('agentExpiry')) + ' <input id="mtbExpiry" type="number" min="1" max="3650" value="365" class="quantum-input" style="width:120px" dir="ltr"></label>' +
      '<div class="mtb-chips">' + chips + '</div>' +
      '</div>' +
      '<p class="qs-hint mtb-formmsg" data-mtb-msg></p>' +
      '<div class="mtb-acts" id="mtbWizFoot">' +
      '<button type="button" class="quantum-btn quantum-btn-secondary" id="mtbWizBack" hidden>' + esc(L('agWizBack')) + '</button>' +
      '<button type="button" class="quantum-btn quantum-btn-primary mtb-wiz-next" id="mtbWizNext">' + esc(L('agWizNext')) + '</button>' +
      '<button type="button" class="quantum-btn quantum-btn-primary" id="mtbMint" hidden>' + esc(L('mint')) + '</button></div>' +
      '</div>' +
      '<div id="mtbTokenOut" style="display:none">' +
      '<p class="mtb-token-ok">' + CHECK + ' ' + esc(L('agTokenOk')) + '</p>' +
      '<div class="mtb-outbox">' +
      '<div class="mtb-outbox-head"><span>' + esc(L('agentToken')) + '</span>' +
      '<button type="button" class="quantum-btn quantum-btn-secondary u-btn-xs" id="mtbCopy">' + esc(L('copy')) + '</button></div>' +
      '<div class="mtb-outbox-body"><code id="mtbTokenVal" dir="ltr"></code></div>' +
      '</div>' +
      '<div class="mtb-outbox">' +
      '<div class="mtb-outbox-head"><span>' + esc(L('installCmd')) + '</span>' +
      '<button type="button" class="quantum-btn quantum-btn-secondary u-btn-xs" id="mtbCmdCopy">' + esc(L('agCopyCmd')) + '</button></div>' +
      '<div class="mtb-outbox-body"><code id="mtbCmd" dir="ltr"></code></div>' +
      '</div>' +
      '<p class="qs-hint">' + esc(L('agentHint')) + '</p>' +
      '<div class="mtb-acts"><button type="button" class="quantum-btn quantum-btn-secondary" data-mtb-close>' + esc(L('back')) + '</button></div>' +
      '</div>' +
      '</div>';
    document.body.appendChild(ov);
    ov.querySelectorAll('[data-mtb-close]').forEach(function (b) { b.addEventListener('click', function () { ov.remove(); }); });
    ov.addEventListener('click', function (e) {
      if (e.target === ov) { ov.remove(); return; }
      if (cdd && !cdd.contains(e.target) && cddPop && !cddPop.hidden) cddPop.hidden = true;
    });
    function syncRole() {
      var foreign = (ov.querySelector('input[name="mtbRole"]:checked') || {}).value === 'foreign';
      ov.querySelectorAll('.mtb-agent-card').forEach(function (c) {
        c.classList.toggle('on', (c.querySelector('input[name="mtbRole"]') || {}).value !== 'foreign' ? !foreign : foreign);
      });
      var row = document.getElementById('mtbCountryRow');
      if (row) row.style.display = foreign ? '' : 'none';
      var fl = document.getElementById('mtbCountryFlag');
      if (fl) {
        var csel = document.getElementById('mtbCountry');
        fl.innerHTML = (foreign && csel && csel.value) ? flagImg(csel.value) : '';
      }
      if (typeof wizSave === 'function') wizSave();
    }
    ov.querySelectorAll('input[name="mtbRole"]').forEach(function (r) {
      r.addEventListener('change', syncRole);
    });
    var cInp = document.getElementById('mtbCountry');
    var cddBtn = document.getElementById('mtbCddBtn');
    var cddPop = document.getElementById('mtbCddPop');
    var cddCur = document.getElementById('mtbCddCur');
    var cdd = document.getElementById('mtbCdd');
    function setCountry(cc) {
      cInp.value = cc;
      cddCur.innerHTML = cc ? (flagImg(cc) + ' ' + esc(CN(cc) || cc)) : '\u2014';
      cddPop.querySelectorAll('.mtb-cdd-opt').forEach(function (op) { op.classList.toggle('on', op.getAttribute('data-cc') === cc); });
      cddPop.hidden = true;
      if (typeof wizSave === 'function') wizSave();
    }
    cddBtn.addEventListener('click', function (e) { e.stopPropagation(); cddPop.hidden = !cddPop.hidden; });
    cddPop.addEventListener('click', function (e) {
      e.stopPropagation();
      var op = e.target && e.target.closest ? e.target.closest('.mtb-cdd-opt') : null;
      if (op) setCountry(op.getAttribute('data-cc'));
    });
    ov.querySelectorAll('.mtb-chip').forEach(function (ch) {
      ch.addEventListener('click', function () {
        document.getElementById('mtbExpiry').value = ch.getAttribute('data-days');
        ov.querySelectorAll('.mtb-chip').forEach(function (c2) { c2.classList.toggle('on', c2 === ch); });
        if (typeof wizSave === 'function') wizSave();
      });
    });
    document.getElementById('mtbExpiry').addEventListener('input', function () {
      var v = parseInt(this.value, 10);
      ov.querySelectorAll('.mtb-chip').forEach(function (c2) { c2.classList.toggle('on', parseInt(c2.getAttribute('data-days'), 10) === v); });
    });
    if (typeof wizSave === 'function') { /* wired below after wizSave exists */ }
    var wizStep = 1;
    function wizSync() {
      ov.querySelectorAll('.mtb-wpage').forEach(function (p) { p.hidden = parseInt(p.getAttribute('data-pg'), 10) !== wizStep; });
      ov.querySelectorAll('.mtb-wstep').forEach(function (st2) {
        var n2 = parseInt(st2.getAttribute('data-st'), 10);
        st2.classList.toggle('on', n2 === wizStep);
        st2.classList.toggle('done', n2 < wizStep);
        st2.querySelector('.mtb-wstep-dot').innerHTML = n2 < wizStep ? CHECK : String(n2);
      });
      ov.querySelectorAll('.mtb-wline').forEach(function (ln, i2) { ln.classList.toggle('fill', i2 < wizStep - 1); });
      document.getElementById('mtbWizBack').hidden = wizStep === 1;
      document.getElementById('mtbWizNext').hidden = wizStep === 3;
      document.getElementById('mtbMint').hidden = wizStep !== 3;
      var pg = ov.querySelector('.mtb-wpage:not([hidden])');
      if (pg) { var f = pg.querySelector('input:not([type=hidden])'); if (f) f.focus(); }
    }
    function wizNext() {
      if (wizStep === 1 && !document.getElementById('mtbAgentLabel').value.trim()) {
        ov.querySelector('[data-mtb-msg]').textContent = L('agNameReq');
        document.getElementById('mtbAgentLabel').focus();
        return;
      }
      if (wizStep === 2) {
        var foreign2 = (ov.querySelector('input[name="mtbRole"]:checked') || {}).value === 'foreign';
        if (foreign2 && !((document.getElementById('mtbCountry') || {}).value)) {
          ov.querySelector('[data-mtb-msg]').textContent = L('pickCountry');
          return;
        }
      }
      ov.querySelector('[data-mtb-msg]').textContent = '';
      wizStep = Math.min(3, wizStep + 1);
      wizSync();
      if (typeof wizSave === 'function') wizSave();
    }
    document.getElementById('mtbWizNext').addEventListener('click', wizNext);
    document.getElementById('mtbWizBack').addEventListener('click', function () {
      wizStep = Math.max(1, wizStep - 1);
      ov.querySelector('[data-mtb-msg]').textContent = '';
      wizSync();
      if (typeof wizSave === 'function') wizSave();
    });
    document.getElementById('mtbAgentLabel').addEventListener('keydown', function (e) {
      if (e.key === 'Enter') { e.preventDefault(); wizNext(); }
    });
    var WKEY = 'quantum_agwiz';
    function wizSave() {
      try {
        localStorage.setItem(WKEY, JSON.stringify({
          s: wizStep,
          n: document.getElementById('mtbAgentLabel').value,
          r: (ov.querySelector('input[name="mtbRole"]:checked') || {}).value || 'iran',
          c: (document.getElementById('mtbCountry') || {}).value || '',
          d: document.getElementById('mtbExpiry').value
        }));
      } catch (err) { /* storage blocked */ }
    }
    function wizLoad() { try { return JSON.parse(localStorage.getItem(WKEY) || 'null'); } catch (err) { return null; } }
    function wizClear() { try { localStorage.removeItem(WKEY); } catch (err) { /* storage blocked */ } }
    function nameOk() {
      var ok = document.getElementById('mtbNameOk');
      if (ok) ok.hidden = document.getElementById('mtbAgentLabel').value.trim().length < 2;
    }
    document.getElementById('mtbAgentLabel').addEventListener('input', function () { nameOk(); wizSave(); });
    document.getElementById('mtbExpiry').addEventListener('input', wizSave);
    ov.addEventListener('click', function (e) {
      var t2 = e.target && e.target.closest ? e.target.closest('[data-tipopen]') : null;
      if (t2) t2.classList.toggle('tipopen');
      else ov.querySelectorAll('.mtb-tip-i.tipopen').forEach(function (x) { x.classList.remove('tipopen'); });
    });
    wizSync();
    var saved = wizLoad();
    if (saved) {
      if (saved.n) document.getElementById('mtbAgentLabel').value = saved.n;
      if (saved.r === 'foreign' || saved.r === 'iran') {
        var rr = ov.querySelector('input[name="mtbRole"][value="' + saved.r + '"]');
        if (rr) { rr.checked = true; syncRole(); }
      }
      if (saved.c) setCountry(saved.c);
      if (saved.d) {
        document.getElementById('mtbExpiry').value = saved.d;
        ov.querySelectorAll('.mtb-chip').forEach(function (c2) { c2.classList.toggle('on', parseInt(c2.getAttribute('data-days'), 10) === parseInt(saved.d, 10)); });
      }
      if (saved.s >= 1 && saved.s <= 3) wizStep = saved.s;
      nameOk();
      wizSync();
    }
    var origin = location.origin;
    var base = (document.documentElement.getAttribute('data-base') || '/ui');
    var panelUrl = origin + base;
    document.getElementById('mtbMint').addEventListener('click', function () {
      var role = (ov.querySelector('input[name="mtbRole"]:checked') || {}).value || 'iran';
      var country = '';
      if (role === 'foreign') {
        country = (document.getElementById('mtbCountry') || {}).value || '';
        if (!country) { ov.querySelector('[data-mtb-msg]').textContent = L('pickCountry'); return; }
      }
      var label = document.getElementById('mtbAgentLabel').value.trim() || ('agent-' + Date.now() % 10000);
      var days = Math.max(1, Math.min(3650, parseInt(document.getElementById('mtbExpiry').value, 10) || 365));
      apiSend('POST', 'api/agents/tokens', { role: role, label: label, expires_days: days, country: country }).then(function (r) {
        var tok = (r && (r.token || (r.data && r.data.token))) || '';
        if (!tok) { ov.querySelector('[data-mtb-msg]').textContent = 'no token in response'; return; }
        document.getElementById('mtbAgentForm').style.display = 'none';
        ov.querySelectorAll('.mtb-wstep').forEach(function (st2) { st2.classList.remove('on'); st2.classList.add('done'); st2.querySelector('.mtb-wstep-dot').innerHTML = CHECK; });
        ov.querySelectorAll('.mtb-wline').forEach(function (ln) { ln.classList.add('fill'); });
        wizClear();
        var outd = document.getElementById('mtbTokenOut');
        outd.style.display = 'block';
        document.getElementById('mtbTokenVal').textContent = tok;
        var cmd = 'sudo bash install-agent.sh --server ' + panelUrl + ' --token ' + tok + ' --ssh-port 22';
        document.getElementById('mtbCmd').textContent = cmd;
        document.getElementById('mtbCopy').addEventListener('click', function () {
          navigator.clipboard.writeText(tok).then(function () {
            document.getElementById('mtbCopy').textContent = L('copied');
          });
        });
        document.getElementById('mtbCmdCopy').addEventListener('click', function () {
          navigator.clipboard.writeText(cmd).then(function () {
            document.getElementById('mtbCmdCopy').textContent = L('copied');
          });
        });
      }).catch(function (e2) {
        ov.querySelector('[data-mtb-msg]').textContent = (e2.body && (e2.body.error || e2.body.message)) || ('HTTP ' + e2.status);
      });
    });
  }
  var agtView = { view: 'table' };
  var agtActsOpen = '';
  var agtActsUp = false;
  var engFilter = 'all';
  var tunFilter = 'all';
  // the ?v= asset tag app.js was served with (version + content hash) —
  // appended to engine brand icon URLs so icon updates bust browser caches.
  var engImgTag = ((document.querySelector('script[src*="app.js"]') || { src: '' }).src.match(/v=([^&]+)/) || ['', ''])[1];
  var agtPage = 1;
  var agtPerPage = 10;
  var agtQuery = '';
  var agtFilters = { role: '', status: '', arch: '', topo: '' };
  function agtTopoCell(a) {
    var tp = a.topologies || [];
    if (!tp.length) return '<span class="badge dim">' + esc(L('agNoTopoS')) + '</span>';
    if (tp.length === 1) return '<span class="badge info">' + esc(tp[0]) + '</span>';
    return '<div class="mtb-cdd mtb-topocdd"><button type="button" class="mtb-cdd-btn mtb-topopill" data-cdd-toggle>' +
      '<span class="mtb-cdd-cur">' + num0(tp.length) + ' ' + esc(L('topoCol')) + '</span>' +
      '<span class="mtb-cdd-arrow"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 9l6 6 6-6"/></svg></span></button>' +
      '<div class="mtb-cdd-pop" hidden>' + tp.map(function (t2) { return '<div class="mtb-cdd-opt mtb-topoitem">' + esc(t2) + '</div>'; }).join('') + '</div></div>';
  }
  function agtFilter(ags) {
    return ags.filter(function (a) {
      if (agtFilters.role && (a.role || '') !== agtFilters.role) return false;
      if (agtFilters.status && (a.status || '') !== agtFilters.status) return false;
      if (agtFilters.arch && (a.arch || '') !== agtFilters.arch) return false;
      if (agtFilters.topo && (a.topologies || []).indexOf(agtFilters.topo) < 0) return false;
      if (agtQuery) {
        var hay = ((a.name || '') + ' ' + (a.id || '')).toLowerCase();
        if (hay.indexOf(agtQuery.toLowerCase()) < 0) return false;
      }
      return true;
    });
  }
  function agtFilterCdd(key, ags) {
    var cur = agtFilters[key];
    var label = { role: L('agRoleF'), status: L('stateCol'), arch: L('archCol'), topo: L('topoCol') }[key];
    var allLbl = { role: L('agAllRoles'), status: L('agAllStates'), arch: L('agAllArchs'), topo: L('agAllTopos') }[key];
    var fico = { role: MTB_ICO.frole, status: MTB_ICO.act, arch: MTB_ICO.chip, topo: MTB_ICO.mesh }[key];
    function valTxt(v) {
      if (key === 'role') return v === 'iran' ? L('roleIran') : L('roleForeign');
      if (key === 'status') return v === 'online' ? L('agStatOn') : L('agStatOff');
      return v;
    }
    var opts = ['<button type="button" class="mtb-cdd-opt' + (!cur ? ' on' : '') + '" data-cdd-opt="' + key + '" data-cc="">' + esc(allLbl) + '</button>'];
    var vals;
    if (key === 'role') vals = ['iran', 'foreign'];
    else if (key === 'status') vals = ['online', 'offline'];
    else if (key === 'arch') {
      vals = [];
      ags.forEach(function (a) { if (a.arch && vals.indexOf(a.arch) < 0) vals.push(a.arch); });
    } else {
      vals = [];
      ags.forEach(function (a) { (a.topologies || []).forEach(function (t) { if (t && vals.indexOf(t) < 0) vals.push(t); }); });
    }
    vals.forEach(function (v) {
      var ico = '';
      if (key === 'role') ico = (v === 'iran') ? topologyFlagSvg('ir') : '<span class="mtb-optglob">' + MTB_ICO.glob2 + '</span>';
      else if (key === 'status') ico = '<span class="mtb-optico ' + (v === 'online' ? 'ok' : 'er') + '">' + (v === 'online' ? MTB_ICO.okc : MTB_ICO.alerc) + '</span>';
      else if (key === 'arch') ico = '<span class="mtb-optico">' + MTB_ICO.chip + '</span>';
      opts.push('<button type="button" class="mtb-cdd-opt' + (cur === v ? ' on' : '') + '" data-cdd-opt="' + key + '" data-cc="' + esc(v) + '">' + ico + esc(valTxt(v)) + '</button>');
    });
    return '<div class="mtb-cdd mtb-fcdd">' +
      '<button type="button" class="mtb-cdd-btn mtb-fbtn' + (cur ? ' on' : '') + '" data-cdd-toggle>' +
      '<span class="mtb-fico">' + fico + '</span>' +
      '<span class="mtb-cdd-cur">' + esc(cur ? (label + ': ' + valTxt(cur)) : allLbl) + '</span>' +
      '<span class="mtb-cdd-arrow"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 9l6 6 6-6"/></svg></span>' +
      '</button>' +
      '<div class="mtb-cdd-pop" hidden>' + opts.join('') + '</div>' +
      '</div>';
  }
  function renderAgt() {
    if (agtView.view === 'detail') { renderAgtDetail(agtView.id); return; }
    window.mtbAgtDetail = null;
    if (window.topTitleSync) window.topTitleSync('magt');
    try { if (location.hash !== '#t=magt') history.replaceState(null, '', '#t=magt'); } catch (e) { /* non-fatal */ }
    var ags = cache.magt || [];
    if (!ags.length) {
      setBody('magt', mtbHero({
        title: L('heroAgT'), desc: L('heroAgD'), docQ: L('heroDocAg'),
        cta: '<button type="button" class="quantum-btn quantum-btn-primary mtb-hero-cta" id="mtbNewAgent">+ ' + esc(L('newAgent')).replace('+ ', '') + '</button>'
      }));
      return;
    }
    // cpu/mem/disk columns appear only if the payload actually carries resources
    var hasRes = ags.some(function (a) { return a.cpu || a.mem || a.disk || a.resources; });
    var header = agentHeader();
    var filtered = agtFilter(ags);
    var PS = agtPerPage;
    var maxPg = Math.max(1, Math.ceil(filtered.length / PS));
    var pg = Math.min(agtPage, maxPg);
    agtPage = pg;
    var pageAgs = filtered.slice((pg - 1) * PS, pg * PS);
    var anyF = agtQuery || agtFilters.role || agtFilters.status || agtFilters.arch || agtFilters.topo;
    var tcbar = '<div class="tc-header mtb-tcbar">' +
      '<div class="mtb-search">' +
      '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="11" cy="11" r="7"/><path d="M21 21l-4.3-4.3"/></svg>' +
      '<input id="mtbAgSearch" class="quantum-input" placeholder="' + esc(L('agSearchPh')) + '" value="' + esc(agtQuery) + '" aria-label="' + esc(L('agSearchPh')) + '">' +
      '</div>' +
      '<div class="mtb-filters">' +
      agtFilterCdd('role', ags) + agtFilterCdd('status', ags) + agtFilterCdd('arch', ags) + agtFilterCdd('topo', ags) +
      (anyF ? '<button type="button" class="mtb-fclear" data-cdd-clear title="' + esc(L('agAllF')) + '">\u00d7</button>' : '') +
      '</div></div>';
    var trs = pageAgs.map(function (a) {
      var r = a.resources || a;

      var dc = (a.server && a.server.datacenter) || '';
      var row = '<tr>' +
        '<td><b class="mtb-aglink" data-agd="' + esc(a.id) + '">' + esc(a.name || a.id) + '</b>' +
        (dc ? '<br><span class="mtb-muted" style="font-size:.85em">' + esc(dc) + '</span>' : '') + '</td>' +
        '<td>' + stateBadge(a.status) + '</td>' +
        '<td class="mtb-muted">' + (a.last_seen ? esc(ago(a.last_seen)) : '—') + '</td>' +
        '<td>' + agentRoleCell(a) + '</td>' +
        '<td>' + (a.version ? '<span class="mtb-ver"><bdi dir="ltr">' + esc(/^[0-9]/.test(a.version) ? 'v' + a.version : a.version) + '</bdi></span>' : '—') + '</td>' +
        '<td class="mono">' + esc(a.arch || '—') + '</td>' +
        '<td>' + agtTopoCell(a) + '</td>' +
        '<td><div class="mtb-acts-row' + (agtActsOpen === a.id ? ' open' : '') + (agtActsOpen === a.id && agtActsUp ? ' mtb-acts-up' : '') + '">' +
        '<div class="mtb-acts-pop">' +
        '<button type="button" class="mtb-act mtb-act-view" data-agview="' + esc(a.id) + '" title="' + esc(L('agActView')) + '">' + MTB_ICO.eye + '</button>' +
        '<button type="button" class="mtb-act mtb-act-edit" data-agedit="' + esc(a.id) + '" title="' + esc(L('agActEdit')) + '">' + MTB_ICO.pen + '</button>' +
        '<button type="button" class="mtb-act mtb-act-upd" data-agupd="' + esc(a.id) + '" title="' + esc(L('agActUpd')) + '">' + MTB_ICO.upArrow + '</button>' +
        '<button type="button" class="mtb-act mtb-act-ref" data-agrefresh="' + esc(a.id) + '" title="' + esc(L('agRefresh')) + '">' + MTB_ICO.upd + '</button>' +
        '<button type="button" class="mtb-act mtb-act-del" data-agdel="' + esc(a.id) + '" title="' + esc(L('agActDel')) + '">' + MTB_ICO.trash + '</button>' +
        '</div>' +
        '<button type="button" class="mtb-act mtb-acts-kebab" data-actskebab title="' + esc(L('agActMore')) + '" aria-label="' + esc(L('agActMore')) + '"><svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><circle cx="12" cy="5" r="1.7"/><circle cx="12" cy="12" r="1.7"/><circle cx="12" cy="19" r="1.7"/></svg></button>' +
        '</div></td>';
      if (hasRes) {
        row += tdNum(r.cpu && r.cpu.percent != null ? pct(r.cpu.percent) : (r.cpu_pct != null ? pct(r.cpu_pct) : '—'));
        row += tdNum(r.mem ? (r.mem.total_mb ? pct(r.mem.used_mb / r.mem.total_mb * 100) : (r.mem.used_mb != null ? bytes(r.mem.used_mb * 1048576) : '—')) : (r.mem_pct != null ? pct(r.mem_pct) : '—'));
        row += tdNum(r.disk ? (r.disk.total_gb ? pct(r.disk.used_gb / r.disk.total_gb * 100) : (r.disk.used_gb != null ? bytes(r.disk.used_gb * 1073741824) : '—')) : (r.disk_pct != null ? pct(r.disk_pct) : '—'));
      }
      return row + '</tr>';
    }).join('');
    var emptyRow = filtered.length ? '' : '<tr><td colspan="8">' + entEmpty(L('agNoMatch')) + '</td></tr>';
    setBody('magt',
      header +
      '<div class="tunnel-card mtb-agfilter">' + tcbar + '</div>' +
      '<div class="tunnel-card">' +
      '<div class="ent-tbl-wrap"><table class="ent-tbl"><thead><tr>' +
      '<th><span class="mtb-th">' + ICO_AGT + esc(L('agentCol')) + '</span></th>' +
      '<th><span class="mtb-th">' + MTB_ICO.act + esc(L('stateCol')) + '</span></th>' +
      '<th><span class="mtb-th">' + MTB_ICO.clock + esc(L('agLastConn')) + '</span></th>' +
      '<th><span class="mtb-th">' + MTB_ICO.glob2 + esc(L('agRoleH')) + '</span></th>' +
      '<th><span class="mtb-th">' + MTB_ICO.tag + esc(L('versionCol')) + '</span></th>' +
      '<th><span class="mtb-th">' + MTB_ICO.chip + esc(L('archCol')) + '</span></th>' +
      '<th><span class="mtb-th">' + MTB_ICO.mesh + esc(L('agToposH')) + '</span></th>' +
      '<th><span class="mtb-th">' + MTB_ICO.sliders + esc(L('agActs')) + '</span></th>' +
      (hasRes ? '<th class="num">' + esc(L('cpuCol')) + '</th><th class="num">' + esc(L('memCol')) + '</th><th class="num">' + esc(L('diskCol')) + '</th>' : '') +
      '</tr></thead><tbody>' + (emptyRow || trs) + '</tbody></table></div>' +
      (function () {
        var pages;
        if (maxPg <= 7) { pages = []; for (var i3 = 1; i3 <= maxPg; i3++) pages.push(i3); }
        else if (pg <= 4) { pages = [1, 2, 3, 4, 5, '\u2026', maxPg]; }
        else if (pg >= maxPg - 3) { pages = [1, '\u2026', maxPg - 4, maxPg - 3, maxPg - 2, maxPg - 1, maxPg]; }
        else { pages = [1, '\u2026', pg - 1, pg, pg + 1, '\u2026', maxPg]; }
        var btns = pages.map(function (p2) {
          if (p2 === '\u2026') return '<span class="mtb-pg-dots">\u2026</span>';
          return '<button type="button" class="mtb-pg-btn' + (p2 === pg ? ' on' : '') + '" data-agpg="' + p2 + '">' + num0(p2) + '</button>';
        }).join('');
        var sizes = [5, 10, 20, 50].map(function (p2) {
          return '<button type="button" class="mtb-cdd-opt' + (p2 === agtPerPage ? ' on' : '') + '" data-psize="' + p2 + '">' + num0(p2) + '</button>';
        }).join('');
        return '<div class="mtb-pager mtb-pager2">' +
          '<div class="mtb-pg-size">' +
          '<div class="mtb-cdd mtb-fcdd"><button type="button" class="mtb-cdd-btn" data-cdd-toggle><span class="mtb-cdd-cur">' + num0(agtPerPage) + '</span>' +
          '<span class="mtb-cdd-arrow"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 9l6 6 6-6"/></svg></span></button>' +
          '<div class="mtb-cdd-pop" hidden>' + sizes + '</div></div>' +
          '<span class="mtb-pg-szlbl">' + esc(L('agPerPage')) + '</span></div>' +
          '<div class="mtb-pg-nav">' +
          '<button type="button" class="mtb-pg-navbtn" data-agpg="prev"' + (pg <= 1 ? ' disabled' : '') + '><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M9 6l6 6-6 6"/></svg>' + esc(L('agPrev')) + '</button>' +
          btns +
          '<button type="button" class="mtb-pg-navbtn" data-agpg="next"' + (pg >= maxPg ? ' disabled' : '') + '>' + esc(L('agNext')) + '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M15 6l-6 6 6 6"/></svg></button>' +
          '</div></div></div>';
      })());
    var si = document.getElementById('mtbAgSearch');
    if (si) {
      si.addEventListener('input', function () {
        agtQuery = this.value;
        agtPage = 1;
        var pos = this.selectionStart;
        renderAgt();
        var si2 = document.getElementById('mtbAgSearch');
        if (si2) { si2.focus(); try { si2.setSelectionRange(pos, pos); } catch (e2) {} }
      });
      if (agtQuery) { si.focus(); try { si.setSelectionRange(agtQuery.length, agtQuery.length); } catch (e2) {} }
    }
    agSumFill();
  }

  // ===========================================================================
  // 4) Alerts — open alerts (+ thresholds + recent notifications)
  // ===========================================================================
  var MTB_ICO = {
    eye: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M2 12s3.5-6 10-6 10 6 10 6-3.5 6-10 6-10-6-10-6z"/><circle cx="12" cy="12" r="2.5"/></svg>',
    pen: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M17 3l4 4L8 20l-5 1 1-5z"/></svg>',
    trash: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 6h18M8 6V4h8v2M6 6l1 14h10l1-14"/></svg>',
    upd: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M21 12a9 9 0 1 1-2.64-6.36"/><path d="M21 3v6h-6"/></svg>',
    upArrow: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 16V5"/><path d="M7.5 9.5L12 5l4.5 4.5"/><path d="M5 20h14"/></svg>',
    info: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 8h.01M12 11.5V16"/></svg>',
    mesh: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="5" cy="6" r="2.5"/><circle cx="19" cy="6" r="2.5"/><circle cx="12" cy="19" r="2.5"/><path d="M7.5 6h9M6.3 8l4.7 8.8M17.7 8l-4.7 8.8"/></svg>',
    tun: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 4v16"/><path d="M8.5 7.5L12 4l3.5 3.5"/><path d="M8.5 16.5L12 20l3.5-3.5"/></svg>',
    okc: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M8.5 12.2l2.5 2.5 4.7-5"/></svg>',
    alerc: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 7.5V13"/><path d="M12 16.5h.01"/></svg>',
    chevL: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M15 6l-6 6 6 6"/></svg>',
    chain: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M10 13a5 5 0 007.54.54l3-3a5 5 0 00-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 00-7.54-.54l-3 3a5 5 0 007.07 7.07l1.71-1.71"/></svg>',
    okmark: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4.5 12.5l5 5 10-11"/></svg>',
    users: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="9" cy="8" r="3.2"/><path d="M3.5 20c.6-3.3 2.8-5 5.5-5s4.9 1.7 5.5 5"/><circle cx="17" cy="9.5" r="2.6"/><path d="M16.2 15.3c2.2.4 3.8 1.9 4.3 4.7"/></svg>',
    play: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 5.5v13l11-6.5z" fill="currentColor" stroke="none"/></svg>',
    stop: '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="7" y="7" width="10" height="10" rx="1.5" fill="currentColor" stroke="none"/></svg>',
    dl: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3v10"/><path d="M8 9.5l4 4 4-4"/><path d="M5 19h14"/></svg>',
    spoofmark: '<svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M2.8 10.6 C2.8 8.4 4.6 7 6.8 7.2 C8.5 7.35 10.4 8 12 8 C13.6 8 15.5 7.35 17.2 7.2 C19.4 7 21.2 8.4 21.2 10.6 C21.2 14.2 17.8 17.2 14.2 17.2 L9.8 17.2 C6.2 17.2 2.8 14.2 2.8 10.6 Z"/><ellipse cx="8" cy="11.6" rx="1.7" ry="1.15" fill="currentColor" stroke="none"/><ellipse cx="16" cy="11.6" rx="1.7" ry="1.15" fill="currentColor" stroke="none"/></svg>',
    qmark: '<svg viewBox="0 0 100 100" aria-hidden="true"><path d="M50 48 C42 38 26 32 12 42 C4 48 1 56 3 62 C6 60 12 54 20 55 C32 56 42 60 50 52 C58 60 68 56 80 55 C88 54 94 60 97 62 C99 56 96 48 88 42 C74 32 58 38 50 48 Z" fill="#38bdf8"/></svg>',
    frole: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/></svg>',
    act: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M22 12h-4l-3 9L9 3l-3 9H2"/></svg>',
    chip: '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="5" width="14" height="14" rx="2"/><rect x="9.5" y="9.5" width="5" height="5"/></svg>',
    stack: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3l9 5-9 5-9-5z"/><path d="M4.5 12.5L12 16.5l7.5-4"/><path d="M4.5 16.5L12 20.5l7.5-4"/></svg>',
    clock: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 3"/></svg>',
    tag: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20.6 13.4l-7.2 7.2a2 2 0 0 1-2.8 0L3 13V3h10l7.6 7.6a2 2 0 0 1 0 2.8z"/><circle cx="7.5" cy="7.5" r="1.5"/></svg>',
    sliders: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 6h16M4 12h16M4 18h16"/><circle cx="9" cy="6" r="2"/><circle cx="15" cy="12" r="2"/><circle cx="7" cy="18" r="2"/></svg>',
    glob2: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M3 12h18"/><path d="M12 3c2.7 2.7 4 5.8 4 9s-1.3 6.3-4 9c-2.7-2.7-4-5.8-4-9s1.3-6.3 4-9z"/></svg>',
    pin: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 10c0 6-8 11-8 11s-8-5-8-11a8 8 0 0 1 16 0z"/><circle cx="12" cy="10" r="3"/></svg>',
    chart: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 3v18h18"/><path d="M7 14l4-4 3 3 5-6"/></svg>',
    gear: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09a1.65 1.65 0 0 0-1-1.51 1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09a1.65 1.65 0 0 0 1.51-1 1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33h.01a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82v.01a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>',
    cube: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/><path d="M3.3 7l8.7 5 8.7-5"/><path d="M12 22V12"/></svg>',
    osico: '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="2" y="4" width="20" height="16" rx="2"/><path d="M2 9h20"/><path d="M6 6.5h.01M9 6.5h.01"/></svg>',
    file: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6"/><path d="M9 13h6M9 17h6"/></svg>',
    ramstick: '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="3" y="6" width="18" height="9" rx="2"/><path d="M7 15v4M12 15v4M17 15v4"/><circle class="cx-led" cx="8" cy="10.5" r="1"/><circle class="cx-led" cx="12" cy="10.5" r="1"/><circle class="cx-led" cx="16" cy="10.5" r="1"/></svg>',
    cpuchip: '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="6" y="6" width="12" height="12" rx="2"/><rect class="cx-core" x="10" y="10" width="4" height="4"/><path d="M9 2v3M15 2v3M9 19v3M15 19v3M2 9h3M2 15h3M19 9h3M19 15h3"/></svg>',
    drive: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M22 12H2"/><path d="M5.5 5h13L22 12v5a2 2 0 01-2 2H4a2 2 0 01-2-2v-5l3.5-7z"/><circle class="cx-led" cx="6" cy="16" r="1"/><circle class="cx-led" cx="10" cy="16" r="1"/></svg>',
    upgrade: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 16V5"/><path d="M7.5 9.5L12 5l4.5 4.5"/><path d="M5 20h14"/></svg>'
  };
  var MTB_HERO_LOGO = '<svg class="mtb-hero-logo" width="110" height="110" viewBox="0 0 100 100" fill="none" aria-hidden="true"><rect width="100" height="100" rx="22" fill="#090d16" stroke="rgba(56,189,248,0.25)" stroke-width="1.5"/><path d="M50 48 C42 38 26 32 12 42 C4 48 1 56 3 62 C6 60 12 54 20 55 C32 56 42 60 50 52 C58 60 68 56 80 55 C88 54 94 60 97 62 C99 56 96 48 88 42 C74 32 58 38 50 48 Z" fill="#38bdf8"/></svg>';
  var mtbToastBox = null;
  function mtbToast(msg) {
    if (!mtbToastBox) {
      mtbToastBox = document.createElement('div');
      mtbToastBox.className = 'mtb-toasts';
      document.body.appendChild(mtbToastBox);
    }
    var el = document.createElement('div');
    el.className = 'mtb-toast';
    el.innerHTML = '<span class="mtb-toast-ico">' + MTB_ICO.okc + '</span><span class="mtb-toast-tx"></span>' +
      '<button type="button" class="mtb-toast-x" aria-label="\u00d7">\u00d7</button>' +
      '<span class="mtb-toast-bar"></span>';
    el.querySelector('.mtb-toast-tx').textContent = msg;
    mtbToastBox.appendChild(el);
    requestAnimationFrame(function () { el.classList.add('in'); });
    var kill = function () {
      if (!el.parentNode) return;
      el.classList.remove('in');
      el.classList.add('out');
      setTimeout(function () {
        el.remove();
        if (mtbToastBox && !mtbToastBox.childElementCount) { mtbToastBox.remove(); mtbToastBox = null; }
      }, 260);
    };
    el.querySelector('.mtb-toast-x').addEventListener('click', kill);
    setTimeout(kill, 3000);
  }
  function mtbHero(opts) {
    return '<div class="tunnel-card mtb-hero"><div class="mtb-hero-in">' +
      MTB_HERO_LOGO +
      '<h3 class="mtb-hero-title">' + esc(opts.title) + '</h3>' +
      '<p class="mtb-hero-desc">' + esc(opts.desc) + '</p>' +
      (opts.cta || '') +
      '<div class="mtb-hero-doc"><span class="mtb-hero-doc-ico">' + MTB_ICO.file + '</span>' +
      '<span class="mtb-hero-doc-tx"><b>' + esc(opts.docQ) + '</b><span>' + esc(L('heroDocS')) + '</span></span></div>' +
      '</div></div>';
  }
  function openAgtDetail(id) {
    agtView = { view: 'detail', id: id };
    renderAgtDetail(id);
  }
  function mtbOsLogo(osRaw) {
    var os = String(osRaw || '').toLowerCase();
    if (os.indexOf('ubuntu') >= 0) {
      return '<span class="mtb-oslogo"><svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="3.1" fill="none" stroke="currentColor" stroke-width="1.9"/><circle cx="12" cy="3.3" r="2.2" fill="currentColor"/><circle cx="4.4" cy="16.6" r="2.2" fill="currentColor"/><circle cx="19.6" cy="16.6" r="2.2" fill="currentColor"/><path d="M10.9 7.2C7.7 8.1 5.6 10.5 5 14" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M13.1 7.2c3.2.9 5.3 3.3 5.9 6.8" fill="none" stroke="currentColor" stroke-width="1.6"/><path d="M6.5 17.4c1.3 1.8 3.3 2.9 5.5 2.9s4.2-1.1 5.5-2.9" fill="none" stroke="currentColor" stroke-width="1.6"/></svg></span>';
    }
    if (os.indexOf('linux') >= 0 || os.indexOf('debian') >= 0 || os.indexOf('centos') >= 0 || os.indexOf('alpine') >= 0 || os.indexOf('fedora') >= 0) {
      return '<span class="mtb-oslogo"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 1.6c-3.1 0-4.8 2.1-4.8 5.4 0 1.7-.4 3-1.7 4.8-1.3 1.9-1.9 4-1.9 5.9 0 2.8 2.2 4.9 4.9 4.9h7c2.7 0 4.9-2.1 4.9-4.9 0-1.9-.6-4-1.9-5.9-1.3-1.8-1.7-3.1-1.7-4.8 0-3.3-1.7-5.4-4.8-5.4z" fill="#17171b" stroke="#a1a1aa" stroke-width="0.8"/><ellipse cx="12" cy="14.6" rx="4.5" ry="5.5" fill="#ffffff"/><path d="M9 8.7c1 1.2 5 1.2 6 0-.7 1.7-1.5 2.3-3 2.3s-2.3-.6-3-2.3z" fill="#f5b027"/><circle cx="9.7" cy="6.3" r="1" fill="#ffffff"/><circle cx="14.3" cy="6.3" r="1" fill="#ffffff"/><circle cx="9.7" cy="6.4" r="0.55" fill="#17171b"/><circle cx="14.3" cy="6.4" r="0.55" fill="#17171b"/><path d="M7.8 20.4c-.2 1.1-1.1 1.9-2.3 1.9M16.2 20.4c.2 1.1 1.1 1.9 2.3 1.9" fill="none" stroke="#f5b027" stroke-width="1.5"/></svg></span>';
    }
    return '<span class="mtb-oslogo"><svg viewBox="0 0 24 24" aria-hidden="true"><rect x="2" y="4" width="20" height="16" rx="2"/><path d="M2 9h20"/><path d="M6 6.5h.01M9 6.5h.01"/></svg></span>';
  }
  function renderAgtDetail(id) {
    var my = (seq.magt = seq.magt + 1);
    setBody('magt', skel(3));
    apiGet('api/ui/agents/' + encodeURIComponent(id)).then(function (d) {
      if (seq.magt !== my) return;
      var ag = d.agent || {};
      var topos = d.topologies || d.topos || [];
      var tuns = d.tunnels || [];
      var engs = d.engines || [];
      window.mtbAgtDetail = ag.name || id;
      if (window.topTitleSync) window.topTitleSync('magt');
      var lastSeen = ag.last_seen ? ago(ag.last_seen) : '\u2014';

      // head card: name+badge (right) / refresh+update+meta (middle) / pen+trash (left)
      var html = '<div class="tunnel-card mtb-dtl"><div class="mtb-dtl-head">' +
        '<div class="mtb-dtl-idcol">' +
        '<span class="mtb-dtl-id"><b class="mtb-detail-name">' + esc(ag.name || id) + '</b>' + stateBadge(ag.status) + '</span>' +
        '<span class="mtb-dtl-meta">' + esc(L('agLastConn')) + ': ' + esc(lastSeen) + '</span>' +
        '</div>' +
        '<div class="mtb-dtl-manage">' +
        '<button type="button" class="quantum-btn quantum-btn-primary" data-agupd="' + esc(id) + '">' + MTB_ICO.upgrade + ' ' + esc(L('agUpdBtn')) + '</button>' +
        '<button type="button" class="quantum-btn quantum-btn-secondary" data-agrefresh="' + esc(id) + '">' + MTB_ICO.upd + ' ' + esc(L('agRefresh')) + '</button>' +
        '<button type="button" class="quantum-btn quantum-btn-secondary" data-agedit="' + esc(id) + '">' + MTB_ICO.pen + ' ' + esc(L('agActEdit')) + '</button>' +
        '<button type="button" class="quantum-btn quantum-btn-danger" data-agdel="' + esc(id) + '">' + MTB_ICO.trash + ' ' + esc(L('agActDel')) + '</button>' +
        '<button type="button" class="quantum-btn mtb-backbtn" data-agback><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M19 12H5"/><path d="M12 19l-7-7 7-7"/></svg> ' + esc(L('agBackList')) + '</button>' +
        '</div></div></div>';

      // info tiles
      var kvTile = function (ico, k, v) {
        return '<div class="mtb-kv-tile"><span class="mtb-kv-tx"><span class="mtb-kv-k2">' + k + '</span><span class="mtb-kv-v2">' + v + '</span></span><span class="mtb-kv-ico">' + ico + '</span></div>';
      };
      html += '<div class="tunnel-card"><div class="tc-header dcard-head"><p class="tc-title dcard-title">' + MTB_ICO.info + esc(L('agInfoT')) + '</p></div>' +
        '<div class="mtb-kv-grid">' +
        kvTile(MTB_ICO.pin, esc(L('agRoleH')), agentRoleCell(ag)) +
        kvTile(MTB_ICO.stack, esc(L('agDc')), esc(ag.datacenter || '\u2014')) +
        kvTile(MTB_ICO.chip, esc(L('archCol')), '<span class="mono">' + esc(ag.arch || '\u2014') + '</span>') +
        kvTile(mtbOsLogo(ag.os || ag.hostname), esc(L('agOS')), ag.os ? esc(ag.os.charAt(0).toUpperCase() + ag.os.slice(1)) : esc(ag.hostname ? ag.hostname.charAt(0).toUpperCase() + ag.hostname.slice(1) : '\u2014')) +
        '</div><div class="mtb-kv-grid mtb-mt8">' +
        kvTile(MTB_ICO.tag, esc(L('agVer')), ag.version ? '<span class="mono"><bdi dir="ltr">' + esc(/^[0-9]/.test(ag.version) ? 'v' + ag.version : ag.version) + '</bdi></span>' : '\u2014') +
        kvTile(MTB_ICO.cube, esc(L('agIPK')), (function () {
          var ip = ag.effective_public_ip || ag.public_ip || '';
          if (!ip) return '<span class="mono">\u2014</span>';
          return '<span class="mono mtb-copyval" data-copyval="' + esc(ip) + '" title="' + esc(L('copy')) + '"><bdi dir="ltr">' + esc(ip) + '</bdi></span>';
        })()) +
        kvTile(MTB_ICO.clock, esc(L('agLastConn')), esc(lastSeen)) +
        '</div></div>';

      // server resources with rings
      var resTile = function (ico, label, pctv, sub, tone) {
        var tones = { violet: 'var(--dash-fo)', ok: 'var(--ok)', info: 'var(--dash-ram)' };
        var p = Math.max(0, Math.min(100, pctv == null ? 0 : pctv));
        var pctTxt = pctv == null ? '\u2014' : Math.round(pctv) + '%';
        return '<div class="mtb-res-tile"><span class="mtb-ringwrap"><svg class="mtb-ring" viewBox="0 0 42 42" aria-hidden="true"><circle class="mtb-ring-track" cx="21" cy="21" r="15.9155"></circle>' +
          '<circle class="mtb-ring-seg" cx="21" cy="21" r="15.9155" stroke="' + tones[tone] + '" stroke-width="5" stroke-linecap="round" pathLength="100" stroke-dasharray="' + p + ' ' + (100 - p) + '" transform="rotate(-90 21 21)"></circle></svg>' +
          '<span class="mtb-res-pct">' + pctTxt + '</span></span>' +
          '<span class="mtb-res-tx"><b>' + esc(label) + '</b><span>' + sub + '</span></span>' +
          '<span class="mtb-res-ico tone-' + tone + '">' + ico + '</span></div>';
      };
      var cpuP = ag.cpu && ag.cpu.percent != null ? ag.cpu.percent : null;
      var cpuModel = ag.cpu_model || '';
      var mhzM = cpuModel.match(/@\s*([\d.]+)\s*GHz/i);
      var cpuSub = (ag.cpu && ag.cpu.cores ? num0(ag.cpu.cores) + ' ' + esc(L('agCores')) : '\u2014') +
        (mhzM ? ' \u00b7 ' + esc(mhzM[1]) + ' GHz' : '');
      var memP = ag.mem && ag.mem.total_mb ? ag.mem.used_mb / ag.mem.total_mb * 100 : null;
      var memSub = ag.mem && ag.mem.total_mb ? esc(bytes(ag.mem.used_mb * 1048576)) + ' / ' + esc(bytes(ag.mem.total_mb * 1048576)) : '\u2014';
      var diskP = ag.disk && ag.disk.total_gb ? ag.disk.used_gb / ag.disk.total_gb * 100 : null;
      var diskSub = ag.disk && ag.disk.total_gb ? esc(ag.disk.used_gb) + ' GB / ' + esc(ag.disk.total_gb) + ' GB' : '\u2014';
      html += '<div class="tunnel-card"><div class="tc-header dcard-head"><p class="tc-title dcard-title">' + MTB_ICO.chart + esc(L('agResT')) + '</p></div>' +
        '<div class="mtb-res-grid">' +
        resTile(MTB_ICO.cpuchip, 'CPU', cpuP, cpuSub, 'violet') +
        resTile(MTB_ICO.ramstick, 'RAM', memP, memSub, 'ok') +
        resTile(MTB_ICO.drive, 'Disk', diskP, diskSub, 'info') +
        '</div></div>';

      // topology tiles
      var topoTiles = topos.length ? topos.map(function (t2, ti) {
        var members = (t2.members != null ? t2.members : ((t2.peers && t2.peers.length) || 0) + 1);
        var node = function (side, fbServers) {
          var role = (side && side.role) || '';
          var cc = side && side.country ? String(side.country).toLowerCase() : '';
          var lbl = role === 'iran' ? L('roleIran') : (cc && CN(side.country) ? CN(side.country) : L('roleForeign'));
          return '<div class="mtb-topo-node"><span class="mtb-topo-nflag">' + topologyFlagSvg(cc) + '</span><b>' + esc(lbl) + '</b>' +
            '<span class="mtb-topo-srv">' + num0((side && side.servers) || fbServers) + ' ' + esc(L('topoSrv')) + '</span></div>';
        };
        var ordFa = ['اول', 'دوم', 'سوم', 'چهارم', 'پنجم', 'ششم', 'هفتم', 'هشتم'];
        var ordEn = ['1st', '2nd', '3rd', '4th', '5th', '6th', '7th', '8th'];
        var ordTxt = ti < 8 ? (isFa() ? 'توپولوژی ' + ordFa[ti] : ordEn[ti] + ' topology')
          : (isFa() ? 'توپولوژی ' + num0(ti + 1) : 'Topology ' + num0(ti + 1));
        var on = !!t2.enabled;
        return '<div class="mtb-topo-tile">' +
          '<div class="mtb-topo-head"><span class="mtb-topo-name"><bdi dir="ltr">' + esc(t2.name || '') + '</bdi><span class="mtb-topo-chev">' + MTB_ICO.chevL + '</span></span>' +
          '<span class="mtb-topo-ord o' + (ti % 3) + '">' + esc(ordTxt) + '</span></div>' +
          '<div class="mtb-topo-diagram">' +
          node(t2.my_side || { role: t2.my_role, servers: 1 }, 1) +
          '<span class="mtb-topo-wire"><i></i><span class="mtb-topo-chain">' + MTB_ICO.chain + '<span class="mtb-topo-okb">' + MTB_ICO.okmark + '</span></span><i></i></span>' +
          node(t2.peer_side || { role: t2.my_role === 'iran' ? 'foreign' : 'iran', servers: (t2.peers && t2.peers.length) || 0 }, (t2.peers && t2.peers.length) || 0) +
          '</div>' +
          '<div class="mtb-topo-foot"><span class="mtb-topo-mem">' + MTB_ICO.users + '<b>' + num0(members) + '</b> ' + esc(L('topoMemberN')) + '</span>' +
          '<span class="mtb-topo-state ' + (on ? 'on' : 'off') + '"><i></i>' + esc(on ? L('enabled') : L('disabled')) + '</span></div>' +
          '<a class="mtb-topo-view" href="#t=tunnel">' + esc(L('topoViewDtl')) + ' \u2190</a></div>';
      }).join('') : entEmpty(L('agNoTopo'));
      html += '<div class="tunnel-card"><div class="tc-header dcard-head"><p class="tc-title dcard-title">' + MTB_ICO.mesh + esc(L('topoRelH')) +
        '<span class="mtb-topo-count">' + num0(topos.length) + '</span></p></div>' +
        '<div class="mtb-topo-tiles">' + topoTiles + '</div></div>';

      // engine cards: install-first lifecycle
      var engState = function (en) {
        if (!en.installed) return 'notinst';
        if (en.enabled && ((en.instances || 0) > 0 || en.health === 'healthy')) return 'run';
        return 'stopped';
      };
      var engDisp = { matrix: 'Matrix', frp: 'FRP', backhaul: 'Backhaul', gost: 'Gost', xray: 'Xray', wireguard: 'WireGuard', wst: 'wstunnel', wstunnel: 'wstunnel', chisel: 'Chisel', rathole: 'Rathole', tinc: 'Tinc', backpack: 'BackPack', dagger: 'DaggerConnect', easytier: 'EasyTier', hysteria: 'Hysteria', midos: 'Midos', narnia: 'Narnia', paqet: 'Paqet', pengo: 'Pengo', singbox: 'sing-box', spoof: 'Spoof', udp2raw: 'udp2raw', waterwall: 'WaterWall', wattunnel: 'WatTunnel' };
      var ENGINE_IMG = { gost: 'img/engines/gost.png', xray: 'img/engines/xray.svg', wireguard: 'img/engines/wireguard.png', rathole: 'img/engines/rathole.png', wst: 'img/engines/wstunnel.png', wstunnel: 'img/engines/wstunnel.png', tinc: 'img/engines/tinc.svg', hysteria: 'img/engines/hysteria.png', singbox: 'img/engines/singbox.png', midos: 'img/engines/midos.png', dagger: 'img/engines/dagger.png', pengo: 'img/engines/pengo.png', backhaul: 'img/engines/backhaul.png', backpack: 'img/engines/backpack.png', narnia: 'img/engines/narnia.png', waterwall: 'img/engines/waterwall.png', wattunnel: 'img/engines/wattunnel.png', udp2raw: 'img/engines/udp2raw.png', paqet: 'img/engines/paqet.png', frp: 'img/engines/frp.png', easytier: 'img/engines/easytier.png', chisel: 'img/engines/chisel.png' };
      var engImgQ = engImgTag ? '?v=' + engImgTag : '';
      var DSTH = '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="3" y="4" width="18" height="7" rx="1.5"/><rect x="3" y="13" width="18" height="7" rx="1.5"/><path d="M7 7.5h.01M7 16.5h.01"/></svg>';
      var CPUTH = MTB_ICO.gear;
      var engImg = function (eng) { return ENGINE_IMG[eng] ? ENGINE_IMG[eng] + engImgQ : ''; };
      var engLogo = function (en, disp) {
        var img = engImg(en.engine);
        if (img) return '<img src="' + img + '" alt="' + esc(disp) + '" draggable="false">';
        if (en.engine === 'spoof') return MTB_ICO.spoofmark;
        // default: the panel's own mark for engines without brand art
        return MTB_ICO.qmark;
      };
      var engChips = [
        { k: 'all', lbl: L('engAll'), dot: 'var(--ac)' },
        { k: 'run', lbl: L('engStateRun'), dot: 'var(--ok)' },
        { k: 'stopped', lbl: L('engStateStop'), dot: 'var(--er)' },
        { k: 'notinst', lbl: L('engStateNot'), dot: 'var(--mu)' }
      ];
      var tunChips = [
        { k: 'all', lbl: L('engAll'), dot: 'var(--ac)' },
        { k: 'healthy', lbl: L('agTunUp'), dot: 'var(--ok)' },
        { k: 'down', lbl: L('st_down'), dot: 'var(--er)' },
        { k: 'disabled', lbl: L('disabled'), dot: 'var(--mu)' },
        { k: 'unknown', lbl: L('st_unknown'), dot: 'var(--mu)' }
      ];
      var engShown = engs.filter(function (en) { return engFilter === 'all' || engState(en) === engFilter; });
      var engCards = engShown.length ? engShown.map(function (en) {
        var st = engState(en);
        var disp = engDisp[en.engine] || en.engine || '';
        var pill = st === 'run'
          ? '<span class="mtb-eng-state run">' + MTB_ICO.okmark + esc(L('engStateRun')) + '</span>'
          : '<span class="mtb-eng-state ' + (st === 'stopped' ? 'stop' : 'notinst') + '"><i></i>' + esc(st === 'stopped' ? L('engStateStop') : L('engStateNot')) + '</span>';
        var act = st === 'notinst'
          ? '<button type="button" class="quantum-btn quantum-btn-secondary mtb-eng-btn" data-engtoggle="' + esc(en.engine || '') + '" data-engto="enable">' + MTB_ICO.dl + esc(L('engInstall')) + '</button>'
          : (st === 'run'
            ? '<button type="button" class="quantum-btn quantum-btn-secondary mtb-eng-btn" data-engtoggle="' + esc(en.engine || '') + '" data-engto="disable">' + MTB_ICO.stop + esc(L('engStop')) + '</button>'
            : '<button type="button" class="quantum-btn quantum-btn-primary mtb-eng-btn" data-engtoggle="' + esc(en.engine || '') + '" data-engto="enable">' + MTB_ICO.play + esc(L('engStart')) + '</button>');
        return '<div class="mtb-eng-card' + (st === 'run' ? ' run' : '') + '" data-no-i18n>' +
          '<div class="mtb-eng-top"><span class="mtb-eng-logo e-' + esc(en.engine || '') + '">' + engLogo(en, disp) + '</span>' +
          '<span class="mtb-eng-id"><b>' + esc(disp) + '</b>' +
          (en.version ? '<span class="mtb-eng-ver">' + esc(L('engVersion')) + ': <bdi dir="ltr">' + esc(en.version) + '</bdi></span>'
            : (en.kernel ? '<span class="mtb-eng-ver">' + esc(L('engKernel')) + '</span>' : '')) +
          (en.last_error ? '<span class="mtb-eng-err">' + esc(en.last_error) + '</span>' : '') +
          '</span>' + pill + '</div>' + act + '</div>';
      }).join('') : entEmpty(L('agNoEng'));
      var engChipHtml = '<div class="mtb-eng-filters">' + engChips.map(function (c) {
        return '<button type="button" class="mtb-eng-chip' + (engFilter === c.k ? ' on' : '') + '" data-engfilter="' + c.k + '"><i style="background:' + c.dot + '"></i>' + esc(c.lbl) + '</button>';
      }).join('') + '</div>';
      var tunChipHtml = '<div class="mtb-eng-filters">' + tunChips.map(function (c) {
        return '<button type="button" class="mtb-eng-chip' + (tunFilter === c.k ? ' on' : '') + '" data-tunfilter="' + c.k + '"><i style="background:' + c.dot + '"></i>' + esc(c.lbl) + '</button>';
      }).join('') + '</div>';
      html += '<div class="tunnel-card"><div class="tc-header dcard-head"><p class="tc-title dcard-title">' + MTB_ICO.gear + esc(L('agEngT')) + '</p></div>' +
        engChipHtml + '<div class="mtb-eng-tiles">' + engCards + '</div></div>';

      // tunnels table
      var tunShown = tuns.filter(function (tn) { return tunFilter === 'all' || tn.state === tunFilter; });
      var tunRows = tunShown.length ? tunShown.map(function (tn) {
        // Always a word, never a bare dash: up / down / disabled / unknown.
        var b2 = tn.state === 'healthy' ? badge('ok', esc(L('agTunUp')), true) : (tn.state === 'down' ? badge('err', esc(L('st_down')), true) : (tn.state === 'disabled' ? badge('dim', esc(L('disabled')), true) : badge('dim', esc(L('st_unknown')), true)));
        var rem = (tn.remote && tn.remote.name) || '\u2014';
        var remDc = (tn.remote && tn.remote.datacenter) || '';
        var remAg = (cache.magt || []).find(function (a) { return a.server && a.server.name === tn.remote.name; }) || null;
        var remCc = remAg && remAg.country ? String(remAg.country).toLowerCase() : '';
        var engName2 = engDisp[tn.engine] || tn.engine || '\u2014';
        // A down tunnel reports latency 0; that is "no measurement", not a
        // real RTT, so it must read as an em dash instead of "0 ms".
        var latMs = tn.latency_ms;
        var latCls = (latMs == null || latMs <= 0) ? 'na' : (latMs < 80 ? 'ok' : (latMs < 200 ? 'warn' : 'bad'));
        var lat = (latMs != null && latMs > 0) ? '<span class="mtb-lat ' + latCls + '">' + (latMs < 1 ? '&lt;1 ms' : Math.round(latMs) + ' ms') + '</span>' : '\u2014';
        var tkey = tn.tunnel_key || '';
        var tTopo = tn.topology || '';
        return '<tr>' +
          '<td>' + (tkey ? '<b class="mono mtb-tun-key" dir="ltr" title="' + esc(tkey) + '">' + esc(tkey) + '</b>' : '\u2014') +
            (tTopo ? '<br><span class="mtb-muted" style="font-size:.85em">' + esc(tTopo) + '</span>' : '') + '</td>' +
          '<td><div class="mtb-dest"><span class="mtb-dest-flag">' + (remCc ? topologyFlagSvg(remCc) : '') + '</span><div class="mtb-dest-tx"><b>' + esc(rem) + '</b>' +
            ((remDc || remCc) ? '<span class="mtb-muted" style="font-size:.85em">' + esc(remDc) + (remDc && remCc ? ' \u00b7 ' : '') + esc(remCc ? CN(remCc) : '') + '</span>' : '') + '</div></div></td>' +
          '<td><span class="badge info mtb-eng-badge">' + engLogo({ engine: tn.engine }, engName2) + ' ' + esc(engName2) + '</span></td>' +
          '<td>' + b2 + '</td>' +
          '<td>' + (tn.weight_pct != null ? (Math.round(tn.weight_pct * 10) / 10) + '%' : '\u2014') + '</td>' +
          '<td>' + (tn.today_bytes ? esc(bytes(tn.today_bytes)) : '\u2014') + '</td>' +
          '<td>' + lat + '</td>' +
          '<td><button type="button" class="quantum-btn quantum-btn-secondary u-btn-xs" data-agopentun="' + esc(tn.tunnel_key || '') + '">' + esc(L('agViewDtl')) + '</button></td>' +
          '</tr>';
      }).join('') : '';
      html += '<div class="tunnel-card"><div class="tc-header dcard-head"><p class="tc-title dcard-title">' + MTB_ICO.tun + esc(L('agTunT')) + '</p></div>' +
        tunChipHtml +
        '<div class="ent-tbl-wrap"><table class="ent-tbl"><thead><tr>' +
        '<th><span class="mtb-th">' + MTB_ICO.tun + esc(L('agTunName')) + '</span></th>' +
        '<th><span class="mtb-th">' + DSTH + esc(L('agDstSrv')) + '</span></th>' +
        '<th><span class="mtb-th">' + CPUTH + esc(L('agMotor')) + '</span></th><th>' + esc(L('stateCol')) + '</th><th>' + esc(L('agWeight')) + '</th><th>' + esc(L('agToday')) + '</th><th>' + esc(L('agLatency')) + '</th><th>' + esc(L('agActs')) + '</th>' +
        '</tr></thead><tbody>' + (tunRows || '<tr><td colspan="8">' + entEmpty(tunFilter === 'all' ? L('agNoTun') : L('engStateStop') + ' — ' + L('st_unknown')) + '</td></tr>') + '</tbody></table></div></div>';

      setBody('magt', html); agtUpdProgress(id, my);
      try { var nh = '#t=magt&ag=' + encodeURIComponent(id); if (location.hash !== nh) history.replaceState(null, '', nh); } catch (e) { /* non-fatal */ }
    }).catch(function (e) {
      if (seq.magt !== my) return;
      var c = entError(e, function () { renderAgtDetail(id); });
      setBody('magt', c.html); wireRetry('magt', c.wire);
    });
  }
  function agtUpdProgress(id, my) {
    apiGet('api/agents/' + encodeURIComponent(id)).then(function (d) {
      if (seq.magt !== my) return;
      var st = (d && d.last_state) || null;
      var up = st && st.update_progress;
      if (!up) return;
      var kinds = { requested: 'info', downloading: 'info', verifying: 'warn', swapped: 'warn', restarting: 'warn', completed: 'ok', failed: 'err' };
      var labels = { requested: 'agPhRequested', downloading: 'agPhDownloading', verifying: 'agPhVerifying', swapped: 'agPhSwapped', restarting: 'agPhRestarting', completed: 'agPhCompleted', failed: 'agPhFailed' };
      var kind = kinds[up.phase] || 'dim';
      var lbl = labels[up.phase] ? L(labels[up.phase]) : (up.phase || '');
      var head = document.querySelector('.mtb-detail-head');
      if (!head || document.querySelector('.mtb-upd-line')) return;
      head.closest('.tunnel-card').insertAdjacentHTML('afterend',
        '<div class="tunnel-card mtb-upd-line"><span class="mtb-upd-k">' + esc(L('agUpdT')) + '</span>' +
        badge(kind, lbl, true) +
        (up.version ? '<bdi dir="ltr" class="mono">' + esc(up.version) + '</bdi>' : '') +
        (up.error ? '<span class="mtb-upd-err">' + esc(up.error) + '</span>' : '') +
        '</div>');
    }).catch(function () {});
  }
  var agLastUpdUrl = '';
  function openAgtUpd(id) {
    apiGet('api/ui/agents/' + encodeURIComponent(id)).then(function (d) {
      var ag = d.agent || {};
      var curV = ag.version || '';
      var preV = /^v[0-9]/.test(curV) ? curV : '';
      var ov = document.createElement('div');
      ov.className = 'mtb-modal-ov';
      var PENCIL = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M17 3l4 4L8 20l-5 1 1-5z"/></svg>';
    var CUBE = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/></svg>';
    var LINK = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/></svg>';
    var EXT = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/><path d="M15 3h6v6"/><path d="M10 14L21 3"/></svg>';
    var INFO = '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 8h.01M12 11.5V16"/></svg>';
    var REFRESH = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M21 12a9 9 0 1 1-2.64-6.36"/><path d="M21 3v6h-6"/></svg>';
    var SERV = '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="3" y="4" width="18" height="7" rx="1.5"/><rect x="3" y="13" width="18" height="7" rx="1.5"/><path d="M7 7.5h.01M7 16.5h.01"/></svg>';
    ov.innerHTML = '<div class="mtb-modal mtb-ed-modal mtb-upd-modal" role="dialog" aria-modal="true">' +
        '<div class="tc-header"><div class="mtb-wiz-hero"><span class="mtb-modal-badge">' + PENCIL + '</span><div class="mtb-wiz-hero-tx"><p class="tc-title">' + esc(L('agUpdT')) + '</p><p class="qs-hint"><b>' + esc(ag.name || id) + '</b>' + (curV ? ' · <bdi dir="ltr">' + esc(curV) + '</bdi>' : '') + '</p></div></div>' +
        '<button type="button" class="mtb-x" data-mtb-close>×</button></div>' +
        '<div class="mtb-form">' +
        '<span class="mtb-upd-k">' + SERV + esc(L('agUpdVer')) + '</span>' +
        '<div class="mtb-field"><input id="mtbUpdVer" class="quantum-input" value="' + esc(preV) + '" dir="ltr" placeholder="v1.29.1"><span class="mtb-field-end">' + CUBE + '</span></div>' +
        '<span class="mtb-upd-k">' + LINK + esc(L('agUpdUrl')) + '</span>' +
        '<div class="mtb-field"><input id="mtbUpdUrl" class="quantum-input" value="' + esc(agLastUpdUrl) + '" dir="ltr" placeholder="https://.../quantum-agent-linux-' + esc(ag.arch || 'amd64') + '.tar.gz"><span class="mtb-field-end">' + EXT + '</span></div>' +
        '<div class="mtb-upd-info"><span class="mtb-upd-ico">' + INFO + '</span><span class="mtb-upd-info-tx">' + esc(L('agUpdHint')) + '</span></div>' +
        '<p class="qs-hint mtb-formmsg" data-mtb-msg></p>' +
        '<div class="mtb-acts"><button type="button" class="quantum-btn quantum-btn-secondary" data-mtb-close>' + esc(L('cancel')) + '</button>' +
        '<button type="button" class="quantum-btn quantum-btn-primary" id="mtbUpdGo">' + REFRESH + ' ' + esc(L('agUpdStart')) + '</button></div>' +
        '</div></div>';
      document.body.appendChild(ov);
      ov.querySelectorAll('[data-mtb-close]').forEach(function (b) { b.addEventListener('click', function () { ov.remove(); }); });
      ov.addEventListener('click', function (e) { if (e.target === ov) ov.remove(); });
      ov.querySelector('#mtbUpdGo').addEventListener('click', function () {
        var ver = ov.querySelector('#mtbUpdVer').value.trim();
        var url = ov.querySelector('#mtbUpdUrl').value.trim();
        if (!ver || !url) { toastErr(L('agUpdBtn'), L('agUpdNeed')); return; }
        agLastUpdUrl = url;
        apiSend('POST', 'api/agents/' + encodeURIComponent(id) + '/update', { version: ver, url: url }).then(function (res) {
          if (res && res.status === 'started') {
            ov.remove();
            if (agtView.view === 'detail' && agtView.id === id) renderAgtDetail(id);
          } else {
            toastErr(L('agUpdBtn'), (res && (res.error || res.message)) || 'HTTP');
          }
        }).catch(function (e2) {
          toastErr(L('agUpdBtn'), ((e2.body && (e2.body.error || e2.body.message)) || e2.message || ('HTTP ' + e2.status)) + (e2.body && e2.body.hint ? ' \u2014 ' + e2.body.hint : ''));
        });
      });
    }).catch(function () {});
  }
  function openAgtEdit(id) {
    apiGet('api/ui/agents/' + encodeURIComponent(id)).then(function (d) {
      var ag = d.agent || {};
      var PENCIL = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M17 3l4 4L8 20l-5 1 1-5z"/></svg>';
      var USER = '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="8" r="3.2"/><path d="M5.5 19a6.5 6.5 0 0 1 13 0"/></svg>';
      var IPICO = '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="3" y="4" width="18" height="7" rx="1.5"/><rect x="3" y="13" width="18" height="7" rx="1.5"/><path d="M7 7.5h.01M7 16.5h.01"/></svg>';
      var ov = document.createElement('div');
      ov.className = 'mtb-modal-ov';
      ov.innerHTML = '<div class="mtb-modal mtb-ed-modal" role="dialog" aria-modal="true">' +
        '<div class="tc-header"><div class="mtb-wiz-hero"><span class="mtb-modal-badge">' + PENCIL + '</span><div class="mtb-wiz-hero-tx"><p class="tc-title">' + esc(L('agEditT')) + '</p><p class="qs-hint">' + esc(L('agEditSub')) + '</p></div></div>' +
        '<button type="button" class="mtb-x" data-mtb-close>×</button></div>' +
        '<div class="mtb-wiz" id="mtbEdWiz">' +
        '<div class="mtb-wstep on" data-st="1"><span class="mtb-wstep-dot">1</span><span class="mtb-wstep-lbl">' + esc(L('agEdSt1')) + '</span></div>' +
        '<span class="mtb-wline"></span>' +
        '<div class="mtb-wstep" data-st="2"><span class="mtb-wstep-dot">2</span><span class="mtb-wstep-lbl">' + esc(L('agEdSt2')) + '</span></div>' +
        '</div>' +
        '<div class="mtb-form">' +
        '<div class="mtb-wpage" data-pg="1">' +
        '<div class="mtb-ed-panel">' +
        '<div class="mtb-ed-row"><label class="mtb-ed-k"><i class="mtb-ed-req">*</i>' + esc(L('agNameK')) + '</label>' +
        '<div class="mtb-field"><span class="mtb-field-ico">' + USER + '</span><input id="mtbAgName" class="quantum-input" value="' + esc(ag.name || '') + '" dir="ltr"></div></div>' +
        '<div class="mtb-ed-row"><label class="mtb-ed-k"><i class="mtb-ed-req">*</i>' + esc(L('agIPK')) + '</label>' +
        '<div class="mtb-field"><span class="mtb-field-ico">' + IPICO + '</span><input id="mtbAgIP" class="quantum-input" value="' + esc(ag.public_ip || '') + '" dir="ltr" placeholder="—"></div></div>' +
        '</div>' +
        '</div>' +
        '<div class="mtb-wpage" data-pg="2" hidden>' +
        '<div class="mtb-ed-panel" id="mtbEdRev"></div>' +
        '</div>' +
        '<p class="qs-hint mtb-formmsg" data-mtb-msg></p>' +
        '<div class="mtb-acts"><button type="button" class="quantum-btn quantum-btn-secondary mtb-ed-back" id="mtbEdBack" hidden>' + esc(L('agWizBack')) + '</button>' +
        '<button type="button" class="quantum-btn quantum-btn-secondary" data-mtb-close>' + esc(L('cancel')) + '</button>' +
        '<button type="button" class="quantum-btn quantum-btn-primary" id="mtbEdNext">' + esc(L('agEdNext')) + '</button>' +
        '<button type="button" class="quantum-btn quantum-btn-primary" id="mtbAgSave" hidden>' + esc(L('agSave')) + '</button></div>' +
        '</div></div>';
      document.body.appendChild(ov);
      ov.querySelectorAll('[data-mtb-close]').forEach(function (b) { b.addEventListener('click', function () { ov.remove(); }); });
      ov.addEventListener('click', function (e) { if (e.target === ov) ov.remove(); });
      var edStep = 1;
      function edSync() {
        ov.querySelectorAll('.mtb-wpage').forEach(function (p) { p.hidden = parseInt(p.getAttribute('data-pg'), 10) !== edStep; });
        ov.querySelectorAll('.mtb-wstep').forEach(function (st2) {
          var n2 = parseInt(st2.getAttribute('data-st'), 10);
          st2.classList.toggle('on', n2 === edStep);
          st2.classList.toggle('done', n2 < edStep);
          st2.querySelector('.mtb-wstep-dot').innerHTML = n2 < edStep ? '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 6L9 17l-5-5"/></svg>' : String(n2);
        });
        ov.querySelectorAll('.mtb-wline').forEach(function (ln, i2) { ln.classList.toggle('fill', i2 < edStep - 1); });
        document.getElementById('mtbEdBack').hidden = edStep === 1;
        document.getElementById('mtbEdNext').hidden = edStep === 2;
        document.getElementById('mtbAgSave').hidden = edStep !== 2;
      }
      ov.querySelector('#mtbEdNext').addEventListener('click', function () {
        if (edStep === 1 && !ov.querySelector('#mtbAgName').value.trim()) {
          ov.querySelector('[data-mtb-msg]').textContent = L('agNameK') + ' — ' + L('agSave');
          return;
        }
        var nm = ov.querySelector('#mtbAgName').value.trim() || '—';
        var pip = ov.querySelector('#mtbAgIP').value.trim() || '—';
        ov.querySelector('#mtbEdRev').innerHTML =
          '<div class="mtb-ed-rev-r"><span>' + esc(L('agNameK')) + '</span><b dir="ltr">' + esc(nm) + '</b></div>' +
          '<div class="mtb-ed-rev-r"><span>' + esc(L('agIPK')) + '</span><b dir="ltr">' + esc(pip) + '</b></div>';
        ov.querySelector('[data-mtb-msg]').textContent = '';
        edStep = 2;
        edSync();
      });
      document.getElementById('mtbEdBack').addEventListener('click', function () {
        edStep = 1;
        ov.querySelector('[data-mtb-msg]').textContent = '';
        edSync();
      });
      ov.querySelector('#mtbAgSave').addEventListener('click', function () {
        var nm = ov.querySelector('#mtbAgName').value.trim();
        var pip = ov.querySelector('#mtbAgIP').value.trim();
        var msg = ov.querySelector('[data-mtb-msg]');
        if (!nm) { msg.textContent = L('agNameK') + ' — ' + L('agSave'); return; }
        apiSend('POST', 'api/ui/agents/' + encodeURIComponent(id) + '/rename', { name: nm }).then(function () {
          return apiSend('POST', 'api/ui/agents/' + encodeURIComponent(id) + '/public-ip', { public_ip: pip });
        }).then(function () {
          ov.remove(); renderAgtDetail(id);
        }).catch(function (e2) {
          msg.textContent = (e2.body && (e2.body.error || e2.body.message)) || ('HTTP ' + e2.status);
        });
      });
      edSync();
    }).catch(function () {});
  }
  function agtDel(id) {
    var row = (cache.magt || []).find(function (x) { return x.id === id; }) || {};
    var agName = row.name || '';
    var on = row.status === 'online';
    var TRASH = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 6h18M8 6V4h8v2M6 6l1 14h10l1-14"/></svg>';
    var COPY = '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a1 1 0 0 1 1-1h9"/></svg>';
    var XSM = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18"/></svg>';
    var CHK = '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 6L9 17l-5-5"/></svg>';
    var ov = document.createElement('div');
    ov.className = 'mtb-modal-ov';
    ov.innerHTML = '<div class="mtb-modal mtb-del-modal" role="dialog" aria-modal="true">' +
      '<div class="tc-header"><div class="mtb-del-hero"><span class="mtb-del-badge">' + TRASH + '</span><p class="tc-title">' + esc(L('agDelT')) + '</p></div>' +
      '<button type="button" class="mtb-x" data-mtb-close>×</button></div>' +
      '<p class="qs-hint mtb-del-warn"><span class="mtb-del-warn-i"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3L2 20h20L12 3z"/><path d="M12 10v4"/><path d="M12 17h.01"/></svg></span>' + esc(L('agDelQ')) + '</p>' +
      '<div class="mtb-del-panel">' +
        '<div class="mtb-del-idcol"><p class="mtb-del-k">' + esc(L('agentLabel')) + ':</p>' +
        '<div class="mtb-del-nmrow"><b class="mtb-del-name" dir="ltr">' + esc(agName || '—') + '</b>' +
        (row.status ? '<span class="badge ' + (on ? 'ok' : 'err') + '"><span class="dot"></span>' + esc(L(on ? 'agStatOn' : 'agStatOff')) + '</span>' : '') +
        '</div></div>' +
        '<button type="button" class="mtb-del-copy" id="mtbDelCopy" title="' + esc(L('copy')) + '">' + COPY + '<span id="mtbDelCopyTx">' + esc(L('copy')) + '</span></button>' +
      '</div>' +
      '<p class="qs-hint">' + esc(L('agDelConfirm')) + '</p>' +
      '<input id="mtbAgDelName" class="quantum-input" placeholder="' + esc(L('agDelPh')) + '" dir="ltr" autocomplete="off" spellcheck="false" aria-label="' + esc(L('agDelPh')) + '">' +
      '<p class="qs-hint mtb-formmsg" data-mtb-msg></p>' +
      '<div class="mtb-acts"><span class="mtb-del-foot-hint"><span class="mtb-del-foot-hint-ico"><svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 8h.01M12 11.5V16"/></svg></span>' + esc(L('agDelEnable')) + '</span>' +
      '<button type="button" class="quantum-btn quantum-btn-secondary" data-mtb-close>' + esc(L('cancel')) + ' ' + XSM + '</button>' +
      '<button type="button" class="quantum-btn quantum-btn-danger" id="mtbAgDelGo" disabled>' + TRASH + ' ' + esc(L('agActDel')) + '</button></div>' +
      '</div>';
    document.body.appendChild(ov);
    ov.querySelectorAll('[data-mtb-close]').forEach(function (b) { b.addEventListener('click', function () { ov.remove(); }); });
    ov.addEventListener('click', function (e) { if (e.target === ov) ov.remove(); });
    var copyBtn = ov.querySelector('#mtbDelCopy');
    var copyTx = ov.querySelector('#mtbDelCopyTx');
    var delInp = ov.querySelector('#mtbAgDelName');
    var delGo = ov.querySelector('#mtbAgDelGo');
    var delMsg = ov.querySelector('[data-mtb-msg]');
    delInp.addEventListener('input', function () {
      var ok = agName !== '' && delInp.value.trim() === agName;
      delGo.disabled = !ok;
      delMsg.textContent = (!ok && delInp.value.trim()) ? L('agDelNo') : '';
    });
    copyBtn.addEventListener('click', function () {
      navigator.clipboard.writeText(agName).catch(function () { /* clipboard blocked */ });
      copyTx.textContent = L('agCopied');
      copyBtn.classList.add('done');
      clearTimeout(copyBtn._t);
      copyBtn._t = setTimeout(function () { copyTx.textContent = L('copy'); copyBtn.classList.remove('done'); }, 1600);
    });
    ov.querySelector('#mtbAgDelGo').addEventListener('click', function () {
      if (delGo.disabled) return;
      apiSend('DELETE', 'api/ui/agents/' + encodeURIComponent(id)).then(function () {
        ov.remove(); agtView = { view: 'table' }; renderAgt();
      }).catch(function () { ov.remove(); });
    });
  }

  function alertLine(a) {
    // engine/billing alerts carry a localized ui block; prefer its title
    if (a && a.ui && a.ui.title) return (isFa() ? a.ui.title.fa : a.ui.title.en) || a.text || '';
    return (a && a.text) || '';
  }
  function loadAlerts() {
    var my = (seq.malert = seq.malert + 1);
    setBody('malert', skel(3));
    apiGet('api/mesh/alerts').then(function (d) {
      if (seq.malert !== my) return;
      cache.malert = d || {};
      renderAlerts();
    }).catch(function (e) {
      if (seq.malert !== my) return;
      var card = entError(e, loadAlerts);
      setBody('malert', card.html); wireRetry('malert', card.wire);
    });
  }
  function alertRows(list, withRecovery) {
    return (list || []).map(function (a) {
      var first = withRecovery
        ? (a.recovery ? badge('ok', 'OK') : badge('warn', '⚠'))
        : (a.sev === 'crit' ? badge('err', L('sevCrit'), true) : badge('warn', L('sevWarn'), true));
      return '<tr><td>' + first + '</td>' +
        '<td class="mono" dir="ltr">' + esc(a.rule || '—') + '</td>' +
        '<td title="' + esc(a.text || '') + '">' + esc(alertLine(a)) + '</td>' +
        '<td class="mtb-muted">' + esc(fmtDate(a.at)) + '</td></tr>';
    }).join('');
  }
  function renderAlerts() {
    var d = cache.malert || {};
    var open = d.open || [];
    var hist = (d.history || []).slice(0, 30);
    var head = '<div class="tunnel-card"><div class="tc-header"><p class="tc-title">' + esc(L('alertsOpen')) + '</p>' +
      '<span class="mtb-stats">' +
      (d.enabled ? badge('ok', L('alertsEnabled'), true) : badge('dim', L('alertsOff'))) +
      '<span class="mtb-muted">' + open.length + '</span></span></div>';
    var rules = [];
    if (d.agent_offline_min != null) rules.push(esc(L('ruleOffline')) + ': <b>' + num0(d.agent_offline_min) + '</b>');
    if (d.cert_expire_days != null) rules.push(esc(L('ruleCert')) + ': <b>' + num0(d.cert_expire_days) + '</b>');
    if (d.disk_max_percent != null) rules.push(esc(L('ruleDisk')) + ': <b>' + num0(d.disk_max_percent) + '</b>');
    if (rules.length) head += '<p class="qs-hint">' + rules.join(' · ') + '</p>';
    head += '</div>';
    var thead = '<thead><tr><th></th><th>' + esc(L('ruleCol')) + '</th><th>' + esc(L('msgCol')) + '</th><th>' + esc(L('timeCol')) + '</th></tr></thead>';
    var openTbl = open.length
      ? '<div class="ent-tbl-wrap"><table class="ent-tbl">' + thead + '<tbody>' + alertRows(open, false) + '</tbody></table></div>'
      : entEmpty(L('alertsNone'));
    var histTbl = hist.length
      ? '<div class="ent-tbl-wrap"><table class="ent-tbl">' + thead + '<tbody>' + alertRows(hist, true) + '</tbody></table></div>'
      : entEmpty(L('alertsNone'));
    setBody('malert', head +
      '<div class="tunnel-card">' + openTbl + '</div>' +
      '<div class="tunnel-card"><div class="tc-header"><p class="tc-title">' + esc(L('alertsHistory')) + '</p></div>' +
      '<p class="qs-hint">' + esc(L('historyHint')) + '</p>' + histTbl + '</div>');
  }

  // ===========================================================================
  // 5) Billing — three tables, month input + load + CSV download
  // ===========================================================================
  function billTable(title, rows, head) {
    return '<div class="tunnel-card"><div class="tc-header"><p class="tc-title">' + esc(title) + '</p></div>' +
      (rows ? '<div class="ent-tbl-wrap"><table class="ent-tbl"><thead><tr>' + head + '</tr></thead><tbody>' + rows + '</tbody></table></div>'
        : entEmpty(L('billNone'))) + '</div>';
  }
  function billControls() {
    return '<div class="tunnel-card"><div class="tc-header"><p class="tc-title">' + esc(L('mbill')) + '</p></div>' +
      '<div class="mtb-stats">' +
      '<div class="fg"><label class="qs-label" for="mtbMonthInp">' + esc(L('billMonth')) + '</label>' +
      '<input class="quantum-input u-mono mtb-month" id="mtbMonthInp" data-mtb-month value="' + esc(cache.billMonth || '') + '" placeholder="1405-08" inputmode="numeric" autocomplete="off"></div>' +
      '<button type="button" class="quantum-btn quantum-btn-primary" data-mtb-billgo>' + esc(L('billLoad')) + '</button>' +
      '<button type="button" class="quantum-btn quantum-btn-secondary" data-mtb-billcsv>' + esc(L('billCsv')) + '</button>' +
      '<span class="mtb-muted" data-mtb-csvmsg></span></div>' +
      '<p class="qs-hint">' + esc(L('billNote')) + '</p></div>';
  }
  function wireBillControls() {
    var sec = document.getElementById('tab-mbill');
    if (!sec) return;
    var go = sec.querySelector('[data-mtb-billgo]');
    var csv = sec.querySelector('[data-mtb-billcsv]');
    var inp = sec.querySelector('[data-mtb-month]');
    if (go) go.onclick = loadBill;
    if (inp) inp.onkeydown = function (e) { if (e.key === 'Enter') loadBill(); };
    if (csv) csv.onclick = function () { downloadBillCsv(); };
  }
  function downloadBillCsv() {
    var msg = document.querySelector('#tab-mbill [data-mtb-csvmsg]');
    var month = cache.billMonth || currentJalaliMonth();
    var say = function (text) { if (msg) msg.textContent = text; };
    // authenticated download via fetch-blob (standalone version of mesh.js's
    // downloadFile — no dependence on panel helpers)
    fetch(at('api/ui/reports/billing?month=' + encodeURIComponent(month) + '&format=csv'),
      { credentials: 'same-origin', headers: { 'X-CSRF-Token': csrfToken() } })
      .then(function (r) {
        if (!r.ok) throw new Error('HTTP ' + r.status);
        return r.blob();
      })
      .then(function (blob) {
        var url = URL.createObjectURL(blob);
        var a = document.createElement('a');
        a.href = url;
        a.download = 'billing-' + month + '.csv';
        document.body.appendChild(a);
        a.click();
        a.remove();
        setTimeout(function () { URL.revokeObjectURL(url); }, 5000);
        say(L('csvOk'));
      })
      .catch(function () { say(L('csvFail')); });
  }
  function loadBill() {
    var my = (seq.mbill = seq.mbill + 1);
    var monthInput = document.querySelector('#tab-mbill [data-mtb-month]');
    if (monthInput && monthInput.value.trim()) cache.billMonth = monthInput.value.trim();
    if (!cache.billMonth) cache.billMonth = currentJalaliMonth();
    setBody('mbill', billControls() + '<div data-mtb-billout>' + skel(4) + '</div>');
    wireBillControls();
    var out = document.querySelector('#tab-mbill [data-mtb-billout]');
    apiGet('api/ui/reports/billing?month=' + encodeURIComponent(cache.billMonth)).then(function (r) {
      if (seq.mbill !== my || !out) return;
      renderBill(out, r);
    }).catch(function (e) {
      if (seq.mbill !== my || !out) return;
      var card = entError(e, loadBill);
      out.innerHTML = card.html;
      card.wire(out);
    });
  }
  function renderBill(out, r) {
    var dcRows = (r.per_datacenter || []).map(function (d) {
      var u = d.usage || {}, b = d.billable || {};
      return '<tr><td><b>' + esc(d.name) + '</b></td>' +
        tdNum(bytes(b.wire_rx)) + tdNum(bytes(u.wire_tx)) + tdNum(bytes(b.payload_rx)) +
        tdNum(d.overhead_ratio != null ? num(d.overhead_ratio, 3) : '—') +
        tdNum(d.price_coefficient_hint != null ? num(d.price_coefficient_hint, 3) : '—') +
        tdNum(d.suggested_coefficient != null ? num(d.suggested_coefficient, 3) : '—') +
        '<td>' + qualityPill(d.quality) + '</td></tr>';
    }).join('');
    var svRows = (r.per_server || []).map(function (s) {
      var u = s.usage || {}, b = s.billable || {};
      return '<tr><td><b>' + esc(s.name) + '</b></td><td>' + esc(s.datacenter || '—') + '</td>' +
        tdNum(bytes(b.wire_rx)) + tdNum(bytes(u.wire_tx)) + tdNum(bytes(b.payload_rx)) +
        tdNum(s.overhead_ratio != null ? num(s.overhead_ratio, 3) : '—') +
        '<td>' + qualityPill(s.quality) + '</td></tr>';
    }).join('');
    var tnRows = (r.per_tunnel || []).map(function (x) {
      var b = x.billable || {};
      return '<tr>' + tdKey(x.tunnel_key) +
        '<td>' + esc(x.topology || '—') + '</td>' +
        '<td>' + pair(x.local_server, x.remote_server) + '</td>' +
        '<td>' + esc(x.engine || '—') + '</td>' +
        tdNum(bytes(b.wire_rx)) + tdNum(bytes(b.payload_rx)) +
        tdNum(x.overhead_ratio != null ? num(x.overhead_ratio, 3) : '—') +
        '<td>' + qualityPill(x.quality) + '</td></tr>';
    }).join('');
    var none = !(r.per_datacenter || []).length && !(r.per_server || []).length && !(r.per_tunnel || []).length;
    if (none) {
      out.innerHTML = '<div class="tunnel-card"><div class="mtb-stats">' +
        '<span class="badge info"><bdi dir="ltr">' + esc((r.from_jalali || '') + ' → ' + (r.to_jalali || '')) + '</bdi></span></div>' +
        entEmpty(L('billNone')) + '</div>';
      return;
    }
    out.innerHTML =
      '<div class="tunnel-card"><div class="mtb-stats">' +
      '<span class="badge info"><bdi dir="ltr">' + esc((r.from_jalali || '') + ' → ' + (r.to_jalali || '')) + '</bdi></span>' +
      '<span class="mtb-muted">' + esc(L('billBillableNote')) + '</span></div>' +
      (r.note ? '<p class="qs-hint">' + esc(r.note) + '</p>' : '') + '</div>' +
      billTable(L('dcs'), dcRows,
        '<th>' + esc(L('dcCol')) + '</th><th class="num">' + esc(L('billable')) + '</th>' +
        '<th class="num">' + esc(L('upload')) + '</th><th class="num">' + esc(L('payloadDown')) + '</th>' +
        '<th class="num">' + esc(L('overhead')) + '</th><th class="num">' + esc(L('priceHint')) + '</th>' +
        '<th class="num">' + esc(L('suggested')) + '</th><th>' + esc(L('qualityCol')) + '</th>') +
      billTable(L('serverCol'), svRows,
        '<th>' + esc(L('serverCol')) + '</th><th>' + esc(L('dcCol')) + '</th>' +
        '<th class="num">' + esc(L('billable')) + '</th><th class="num">' + esc(L('upload')) + '</th>' +
        '<th class="num">' + esc(L('payloadDown')) + '</th><th class="num">' + esc(L('overhead')) + '</th>' +
        '<th>' + esc(L('qualityCol')) + '</th>') +
      billTable(L('tunnelKey'), tnRows,
        '<th>' + esc(L('tunnelKey')) + '</th><th>' + esc(L('topologyCol')) + '</th>' +
        '<th>' + esc(L('peerCol')) + '</th><th>' + esc(L('engineCol')) + '</th>' +
        '<th class="num">' + esc(L('billable')) + '</th><th class="num">' + esc(L('payloadDown')) + '</th>' +
        '<th class="num">' + esc(L('overhead')) + '</th><th>' + esc(L('qualityCol')) + '</th>');
  }

  // ---- dispatch -----------------------------------------------------------------
  var LOADERS = { mtun: loadTun, magt: loadAgt, malert: loadAlerts, mbill: loadBill };
  function loadTab(name) { var fn = LOADERS[name]; if (fn) fn(); }

  // ---- boot: style, nav buttons, sections, listeners ------------------------------
  function injectStyle() {
    if (document.getElementById('mtb-style')) return;
    var st = document.createElement('style');
    st.id = 'mtb-style';
    st.textContent = CSS;
    document.head.appendChild(st);
  }
  function navButton(tab, icon) {
    var b = document.createElement('button');
    b.type = 'button';
    b.className = 'nav-item';
    b.setAttribute('role', 'tab');
    b.setAttribute('aria-selected', 'false');
    b.setAttribute('aria-controls', 'tab-' + tab);
    b.setAttribute('tabindex', '-1');
    b.setAttribute('data-tab', tab);
    // The data-i18n key is intentionally NOT in the legacy dictionary: it never
    // resolves (applyTranslations skips unknown keys, leaving the text alone)
    // but sidebar-collapse (sbApply) and the command palette (palItems) read
    // the span's text, so our buttons behave exactly like native ones.
    b.innerHTML = icon + '<span data-i18n="mtb_tab_' + tab + '" data-mtb-lbl="' + tab + '">' + esc(L(tab)) + '</span>';
    return b;
  }
  function section(tab) {
    var s = document.createElement('section');
    s.id = 'tab-' + tab;
    s.setAttribute('role', 'tabpanel');
    s.hidden = true;
    s.innerHTML = '<div class="mtb-body"></div>';
    return s;
  }
  function injectNav() {
    var icons = { mtun: ICO_TUN, magt: ICO_AGT, malert: ICO_ALERT, mbill: ICO_BILL };
    var side = document.getElementById('sideNav');
    var last = side ? side.querySelector('[data-tab="dashboard"]') : null; // Dashboard first
    MY_TABS.forEach(function (tab) {
      if (side && side.querySelector('[data-tab="' + tab + '"]')) {
        last = side.querySelector('[data-tab="' + tab + '"]');
        return;
      }
      var btn = navButton(tab, icons[tab]);
      (last || side).insertAdjacentElement(last ? 'afterend' : 'afterbegin', btn);
      last = btn;
    });
    var drawer = document.getElementById('drawerNav');
    if (drawer) {
      var dLast = drawer.querySelector('[data-tab="dashboard"]') || null;
      MY_TABS.forEach(function (tab) {
        if (drawer.querySelector('[data-tab="' + tab + '"]')) {
          dLast = drawer.querySelector('[data-tab="' + tab + '"]');
          return;
        }
        var src = side ? side.querySelector('[data-tab="' + tab + '"]') : null;
        var clone = src ? src.cloneNode(true) : navButton(tab, icons[tab]);
        (dLast || drawer).insertAdjacentElement(dLast ? 'afterend' : 'afterbegin', clone);
        dLast = clone;
      });
    }
  }
  function injectSections() {
    var main = document.querySelector('main.page');
    MY_TABS.forEach(function (tab) {
      if (document.getElementById('tab-' + tab)) return;
      var s = section(tab);
      if (main) main.appendChild(s);
      else document.body.appendChild(s);
    });
  }
  document.addEventListener('click', function (e) {
    var b = e.target && e.target.closest ? e.target.closest('#mtbNewAgent') : null;
    if (b) openAgentModal();
  });
  document.addEventListener('click', function (e) {
    var kb = e.target && e.target.closest ? e.target.closest('[data-actskebab]') : null;
    var keep = kb ? kb.closest('.mtb-acts-row') : null;
    document.querySelectorAll('.mtb-acts-row.open').forEach(function (r) { if (r !== keep) r.classList.remove('open'); });
    if (kb) {
      var arow = kb.closest('.mtb-acts-row');
      arow.classList.toggle('open');
      var idEl = arow.querySelector('[data-agview]');
      agtActsOpen = arow.classList.contains('open') && idEl ? idEl.getAttribute('data-agview') : '';
      // not enough room below inside the wrap? open upward instead
      agtActsUp = false;
      if (agtActsOpen) {
        var wrap = arow.closest('.ent-tbl-wrap') || (arow.closest('table') && arow.closest('table').parentElement) || null;
        if (wrap) {
          var rr2 = arow.getBoundingClientRect(), wr2 = wrap.getBoundingClientRect();
          agtActsUp = (wr2.bottom - rr2.bottom) < 210;
        }
      }
      arow.classList.toggle('mtb-acts-up', agtActsOpen && agtActsUp);
    } else {
      agtActsOpen = '';
    }
  });
  document.addEventListener('click', function (e) {
    var v = e.target && e.target.closest ? e.target.closest('[data-agview],[data-agd]') : null;
    if (v) { openAgtDetail(v.getAttribute('data-agview') || v.getAttribute('data-agd')); return; }
    var ed = e.target && e.target.closest ? e.target.closest('[data-agedit]') : null;
    if (ed) { openAgtEdit(ed.getAttribute('data-agedit')); return; }
    var bk = e.target && e.target.closest ? e.target.closest('[data-agback]') : null;
    if (bk) { agtView = { view: 'table' }; renderAgt(); return; }
    var uu = e.target && e.target.closest ? e.target.closest('[data-agupd]') : null;
    if (uu) { openAgtUpd(uu.getAttribute('data-agupd')); return; }
    var cp = e.target && e.target.closest ? e.target.closest('[data-copyval]') : null;
    if (cp) {
      var cval = cp.getAttribute('data-copyval');
      navigator.clipboard.writeText(cval).then(function () {
        mtbToast(L('copied') + ' \u00b7 ' + cval);
      }).catch(function () { /* clipboard blocked: the value stays selectable */ });
      return;
    }
    var bcb = e.target && e.target.closest ? e.target.closest('[data-bc-back]') : null;
    if (bcb) { agtView = { view: 'table' }; loadAgt(); return; }
    var arf = e.target && e.target.closest ? e.target.closest('[data-agrefresh]') : null;
    if (arf) {
      var rid = arf.getAttribute('data-agrefresh');
      arf.classList.add('mtb-act-busy');
      setTimeout(function () { arf.classList.remove('mtb-act-busy'); }, 1000);
      mtbToast(L('agRefreshed'));
      if (agtView && agtView.view === 'detail') renderAgtDetail(rid); else loadAgt();
      return;
    }
    var ef = e.target && e.target.closest ? e.target.closest('[data-engfilter]') : null;
    if (ef) {
      engFilter = ef.getAttribute('data-engfilter') || 'all';
      renderAgtDetail(agtView.id);
      return;
    }
    var tf = e.target && e.target.closest ? e.target.closest('[data-tunfilter]') : null;
    if (tf) {
      tunFilter = tf.getAttribute('data-tunfilter') || 'all';
      renderAgtDetail(agtView.id);
      return;
    }
    var egt = e.target && e.target.closest ? e.target.closest('[data-engtoggle]') : null;
    if (egt) {
      var eng = egt.getAttribute('data-engtoggle');
      var to = egt.getAttribute('data-engto');
      egt.disabled = true;
      apiSend('POST', 'api/ui/agents/' + encodeURIComponent(agtView.id) + '/engines/' + encodeURIComponent(eng) + '/' + to).then(function () {
        renderAgtDetail(agtView.id);
      }).catch(function (e2) {
        egt.disabled = false;
        toastErr(L('agEngT'), (e2.body && (e2.body.error || e2.body.message)) || (e2.message || ('HTTP ' + e2.status)));
      });
      return;
    }
    var opt = e.target && e.target.closest ? e.target.closest('[data-agopentun]') : null;
    if (opt) { goTab('mtun'); return; }
    var fop = e.target && e.target.closest ? e.target.closest('[data-cdd-opt]') : null;
    if (fop) {
      var fk = fop.getAttribute('data-cdd-opt');
      agtFilters[fk] = fop.getAttribute('data-cc') || '';
      agtPage = 1;
      renderAgt();
      return;
    }
    var psz = e.target && e.target.closest ? e.target.closest('[data-psize]') : null;
    if (psz) {
      agtPerPage = parseInt(psz.getAttribute('data-psize'), 10) || 10;
      agtPage = 1;
      renderAgt();
      return;
    }
    var fcl = e.target && e.target.closest ? e.target.closest('[data-cdd-clear]') : null;
    if (fcl) {
      agtQuery = '';
      agtFilters = { role: '', status: '', arch: '', topo: '' };
      agtPage = 1;
      renderAgt();
      return;
    }
    var ftg = e.target && e.target.closest ? e.target.closest('[data-cdd-toggle]') : null;
    if (ftg) {
      var fcdd = ftg.closest('.mtb-cdd');
      var fpop = fcdd ? fcdd.querySelector('.mtb-cdd-pop') : null;
      document.querySelectorAll('#tab-magt .mtb-cdd-pop').forEach(function (p2) { if (!p2.hidden && p2 !== fpop) p2.hidden = true; });
      if (fpop) {
        fpop.style.top = '';
        fpop.style.bottom = '';
        fpop.hidden = !fpop.hidden;
        if (!fpop.hidden) {
          var fr = fpop.getBoundingClientRect();
          if (fr.bottom > window.innerHeight - 8) {
            fpop.style.top = 'auto';
            fpop.style.bottom = 'calc(100% + 4px)';
          }
        }
      }
      return;
    }
    if (!e.target.closest('.mtb-cdd')) {
      document.querySelectorAll('#tab-magt .mtb-cdd-pop').forEach(function (p2) { if (!p2.hidden) p2.hidden = true; });
    }
    var pg = e.target && e.target.closest ? e.target.closest('[data-agpg]') : null;
    if (pg && !pg.disabled) {
      var v = pg.getAttribute('data-agpg');
      if (v === 'prev') agtPage = Math.max(1, agtPage - 1);
      else if (v === 'next') agtPage = agtPage + 1;
      else agtPage = parseInt(v, 10) || 1;
      renderAgt();
      return;
    }
    var dl = e.target && e.target.closest ? e.target.closest('[data-agdel]') : null;
    if (dl) { agtDel(dl.getAttribute('data-agdel')); }
  });
  function bindNav() {
    ['#sideNav', '#drawerNav'].forEach(function (sel) {
      var nav = document.querySelector(sel);
      if (!nav || nav.__mtb) return;
      nav.__mtb = true;
      // capture: our own buttons never reach the legacy goTab onclick binding
      nav.addEventListener('click', function (e) {
        var b = e.target && e.target.closest ? e.target.closest('button[data-tab]') : null;
        if (!b) return;
        var name = b.getAttribute('data-tab');
        if (isMineId(name)) {
          e.stopPropagation();
          activate(name);
        }
      }, true);
      // bubble: a legacy nav button was activated → hide ours, restore state
      nav.addEventListener('click', function (e) {
        var b = e.target && e.target.closest ? e.target.closest('button[data-tab]') : null;
        if (!b) return;
        if (!isMineId(b.getAttribute('data-tab'))) onLegacyNavClick();
      }, false);
    });
  }
  function relabel() {
    document.querySelectorAll('[data-mtb-lbl]').forEach(function (sp) {
      var tab = sp.getAttribute('data-mtb-lbl');
      if (tab && TXT[tab]) sp.textContent = L(tab);
    });
  }
  function bindLang() {
    ['#langToggleBtn', '#langToggleBtnM'].forEach(function (sel) {
      var btn = document.querySelector(sel);
      if (!btn || btn.__mtb) return;
      btn.__mtb = true;
      btn.addEventListener('click', function () {
        // setLanguage() normally reloads the page; if it doesn't (same-lang
        // click or a blocked reload), re-render labels and the visible tab.
        setTimeout(function () {
          relabel();
          var vis = document.querySelectorAll('main.page section[id^="tab-m"]:not([hidden])');
          if (vis.length) loadTab((vis[0].id || '').replace(/^tab-/, ''));
        }, 60);
      });
    });
  }

  function boot() {
    try { injectStyle(); } catch (e) { /* non-fatal */ }
    try { injectNav(); } catch (e) { /* non-fatal */ }
    try { // place the legacy topology tab right after the agents tab (position 3)
      var sb = document.getElementById('sideNav');
      var tB = sb && sb.querySelector('[data-tab="tunnel"]');
      var aB = sb && sb.querySelector('[data-tab="magt"]');
      if (tB && aB) aB.insertAdjacentElement('afterend', tB);
      var dr = document.getElementById('drawerNav');
      var dT = dr && dr.querySelector('[data-tab="tunnel"]');
      var dA = dr && dr.querySelector('[data-tab="magt"]');
      if (dT && dA) dA.insertAdjacentElement('afterend', dT);
    } catch (e) { /* non-fatal */ }
    try { injectSections(); } catch (e) { /* non-fatal */ }
    try { bindNav(); } catch (e) { /* non-fatal */ }
    try { bindLang(); } catch (e) { /* non-fatal */ }
    try { patchGoTab(); } catch (e) { /* non-fatal */ }
    // deep link: #t=mtun … survives a refresh (app.js's bootRestoreTab also
    // routes such names through goTab → our wrapper — this covers the case
    // where that runs before our sections exist).
    var m = (location.hash || '').match(/^#t=(m[a-z]+)(?:&ag=([^&]+))?$/);
    if (m && isMineId(m[1]) && document.getElementById('tab-' + m[1])) {
      if (m[1] === 'magt' && m[2]) {
        try {
          var agId = decodeURIComponent(m[2]);
          agtView = { view: 'detail', id: agId };
          activate(m[1]);
          renderAgtDetail(agId);
        } catch (e) { /* fall back to the table */ }
        return;
      }
      activate(m[1]);
    }
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
  else boot();
})();
