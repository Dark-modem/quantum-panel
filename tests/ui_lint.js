async page => {
  const PAGES = ["#dash", "#topologies", "#agents", "#tunnels", "#engines", "#alerts", "#security", "#pasarguard", "#settings"];
  const WIDTHS = [[1440, 900], [1024, 800], [390, 844]];
  const BASE = 'http://127.0.0.1:18091/audit/mesh';
  const problems = [];
  const consoleErrors = [];
  const failedRequests = [];
  page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(m.text().slice(0, 140)); });
  page.on('response', (r) => {
    if (r.status() >= 400 && r.request().resourceType() !== 'document') {
      if (r.url().indexOf('/api/status') < 0 && r.url().indexOf('/api/logs') < 0) {
        failedRequests.push(r.status() + ' ' + r.request().method() + ' ' + r.url().slice(-60));
      }
    }
  });
  const add = (p, w, theme, lang, kind, what) =>
    problems.push(p + ' ' + w + 'px ' + theme + ' ' + lang + ' — ' + kind + ': ' + String(what).slice(0, 150));

  // ---- in-page audit ---------------------------------------------------------
  const audit = () => {
    const out = { overflow: [], clipped: [], textclip: [], i18n: [], contrast: [], taps: [] };
    const vw = document.documentElement.clientWidth;
    const parseColor = (c) => {
      const m = c && c.match(/rgba?\(([\d.]+),\s*([\d.]+),\s*([\d.]+)(?:,\s*([\d.]+))?\)/);
      if (!m) return null;
      return { r: +m[1], g: +m[2], b: +m[3], a: m[4] === undefined ? 1 : +m[4] };
    };
    const blend = (fg, bg) => ({
      r: fg.r * fg.a + bg.r * bg.a * (1 - fg.a),
      g: fg.g * fg.a + bg.g * bg.a * (1 - fg.a),
      b: fg.b * fg.a + bg.b * bg.a * (1 - fg.a), a: 1
    });
    const lum = (c) => {
      const f = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); };
      return 0.2126 * f(c.r) + 0.7152 * f(c.g) + 0.0722 * f(c.b);
    };
    const bgOf = (el) => {
      let n = el;
      while (n && n !== document.documentElement) {
        const c = parseColor(getComputedStyle(n).backgroundColor);
        if (c && c.a > 0.85) return c;
        n = n.parentElement;
      }
      return parseColor(getComputedStyle(document.body).backgroundColor) || { r: 9, g: 9, b: 9, a: 1 };
    };
    // is el horizontally cut by a clip-ancestor (overflow hidden, not page scroll)?
    const clippedByAncestor = (el) => {
      let n = el.parentElement;
      const r = el.getBoundingClientRect();
      while (n && n !== document.body) {
        const cs = getComputedStyle(n);
        if (/(hidden|clip)/.test(cs.overflowX)) {
          const b = n.getBoundingClientRect();
          if (r.right > b.right + 3 || r.left < b.left - 3) return (n.className || n.id || n.tagName).toString().slice(0, 40);
        }
        n = n.parentElement;
      }
      return null;
    };
    document.querySelectorAll('body *').forEach((el) => {
      const cs = getComputedStyle(el);
      if (cs.display === 'none' || cs.visibility === 'hidden' || +cs.opacity === 0) return;
      const r = el.getBoundingClientRect();
      if (r.width < 4 || r.height < 4) return;
      if (el.closest('svg')) return;
      const closedSide = el.closest('#side') && !document.getElementById('side').classList.contains('open');
      if (closedSide) return;
      // inside a scrollable container (overflow auto/scroll) the element's
      // box legitimately extends beyond the viewport — scrollable, not clipped
      let sc = el.parentElement;
      let inScroll = false;
      while (sc && sc !== document.body) {
        const scs = getComputedStyle(sc);
        if (/(auto|scroll)/.test(scs.overflowX + scs.overflowY)) { inScroll = true; break; }
        sc = sc.parentElement;
      }
      // 1. horizontal overflow vs the viewport (vertical is scroll — fine)
      if (!inScroll && (r.left < -3 || r.right > vw + 3)) {
        out.overflow.push((el.id || el.className || el.tagName).toString().slice(0, 40) + ' L' + Math.round(r.left) + ' R' + Math.round(r.right));
      }
      // 2. clipped by an overflow-hidden ancestor
      const clipper = clippedByAncestor(el);
      if (clipper) out.clipped.push((el.id || el.className || el.tagName).toString().slice(0, 40) + ' inside ' + clipper);
      // 3. single-line text overflow without ellipsis
      const isText = el.children.length === 0 && el.textContent.trim().length > 0;
      if (isText && el.scrollWidth > el.clientWidth + 3 && cs.overflowX !== 'auto' && cs.overflowX !== 'scroll'
          && cs.textOverflow !== 'ellipsis' && el.tagName !== 'INPUT' && el.tagName !== 'TEXTAREA') {
        out.textclip.push((el.className || el.tagName).toString().slice(0, 30) + ' "' + el.textContent.trim().slice(0, 30) + '"');
      }
      // 6. contrast AA on body text
      if (isText && !el.closest('button') && el.tagName !== 'INPUT') {
        const fg = parseColor(cs.color);
        if (fg) {
          const eff = fg.a < 1 ? blend(fg, bgOf(el)) : fg;
          const bg = bgOf(el);
          const L1 = lum(eff), L2 = lum(bg);
          const ratio = (Math.max(L1, L2) + 0.05) / (Math.min(L1, L2) + 0.05);
          const big = parseFloat(cs.fontSize) >= 18.66 || (parseFloat(cs.fontSize) >= 14 && parseInt(cs.fontWeight, 10) >= 700);
          if (ratio < (big ? 3 : 4.5)) {
            out.contrast.push((el.className || el.tagName).toString().slice(0, 30) + ' "' + el.textContent.trim().slice(0, 24) + '" ' + ratio.toFixed(2));
          }
        }
      }
      // 7. tap targets on mobile widths
      if (vw < 500 && (el.tagName === 'BUTTON' || (el.tagName === 'A' && el.textContent.trim()))) {
        if (r.width < 36 || r.height < 36) {
          out.taps.push((el.id || el.className || el.tagName).toString().slice(0, 30) + ' ' + Math.round(r.width) + 'x' + Math.round(r.height));
        }
      }
    });
    // 5. i18n: data-i18n elements must resolve in the CURRENT language
    document.querySelectorAll('[data-i18n]').forEach((el) => {
      const key = el.dataset.i18n;
      const txt = el.textContent.trim();
      if (!txt || txt === key || /^(nav_|th_|title_|k_|step)/.test(txt)) out.i18n.push(key + ' -> "' + txt.slice(0, 24) + '"');
    });
    Object.keys(out).forEach((k) => { out[k] = [...new Set(out[k])].slice(0, 10); });
    return out;
  };

  const themes = ['dark', 'light'];
  const langs = ['fa', 'en'];
  for (const [w, h] of WIDTHS) {
    await page.setViewportSize({ width: w, height: h });
    for (const theme of themes) {
      for (const lang of langs) {
        await page.evaluate(([t, l]) => { localStorage.setItem('gre_theme', t); localStorage.setItem('mesh_lang', l); }, [theme, lang]);
        await page.goto(BASE, { waitUntil: 'networkidle' }).catch(() => {});
        await page.waitForTimeout(1300);
        for (const p of PAGES) {
          await page.evaluate((hh) => { location.hash = hh; }, p);
          await page.waitForTimeout(p === '#dash' ? 1200 : 900);
          const res = await page.evaluate(audit);
          for (const k of ['overflow', 'clipped', 'textclip', 'i18n', 'contrast', 'taps']) {
            res[k].forEach((x) => add(p, w, theme, lang, k, x));
          }
        }
      }
    }
  }
  consoleErrors.forEach((x) => problems.push('console.error: ' + x));
  failedRequests.forEach((x) => problems.push('failed request: ' + x));
  const uniq = [...new Set(problems)];
  return JSON.stringify({ violations: uniq.length, problems: uniq.slice(0, 80) }, null, 1);
}
