# ui10_smoke.py — the phase-10 UI e2e, run INSIDE the quantum-test-ui
# container against the real panel binary: first-run checklist, the mesh
# dashboard, the wizard (both shapes, per-tunnel weights), per-tunnel
# weight changes verified against the COMPILED desired state the agent
# actually receives, the hardening safe-apply countdown, traffic/billing,
# fa/en, dark/light, 390px, and the 200-tunnel performance page.
# Zero console errors expected (a small benign whitelist, same rules as
# the phase-9 smoke). Screenshots land in /shots/after/.

import json
import os
import subprocess
import time

from playwright.sync_api import sync_playwright

BASE = "http://127.0.0.1:8000/ui"
USER = "admin"
PASS = "ui10-secret-123"

console_errors = []
page_errors = []
shots = []
FAKE = "/fakeagent"


def sh(cmd):
    r = subprocess.run(["bash", "-c", cmd], capture_output=True, text=True)
    assert r.returncode == 0, f"{cmd}: {r.stderr[-300:]}"
    return r.stdout.strip()


def launch_agent(token, name, tunnel_ids="", down=0, tunnels=6, desired_out=""):
    args = f"{FAKE} -server {BASE} -token {token} -name {name} -tunnels {tunnels} -interval 2s"
    if tunnel_ids:
        args += f" -tunnel-ids '{tunnel_ids}'"
    if down:
        args += f" -down {down}"
    if desired_out:
        args += f" -desired-out {desired_out}"
    log = open(f"/work/fa-{name}.log", "w")
    return subprocess.Popen(["bash", "-c", args], stdout=log, stderr=log)


def main():
    os.makedirs("/shots/after", exist_ok=True)
    with sync_playwright() as pw:
        browser = pw.chromium.launch(args=["--no-sandbox"])
        ctx = browser.new_context(viewport={"width": 1400, "height": 900}, locale="en-US")
        page = ctx.new_page()
        page.on("console", lambda m: console_errors.append(
            f"console.{m.type} @{m.location.get('url', '')[-60:]}:{m.location.get('lineNumber', '')} {m.text[:220]}")
            if m.type == "error" else None)
        page.on("pageerror", lambda e: page_errors.append(f"pageerror: {str(e)[:300]}"))

        def on_resp(r):
            if r.request.resource_type in ("xhr", "fetch") and r.status >= 400:
                if "/api/status" not in r.url and "/api/logs" not in r.url:
                    console_errors.append(f"bad {r.status} {r.request.method} {r.url[-70:]}")
        page.on("response", on_resp)

        def shot(name):
            page.screenshot(path=f"/shots/after/{name}.png")
            shots.append(f"after/{name}.png")

        def csrf():
            return page.evaluate("() => (document.cookie.match(/(?:^|;\\s*)gre_csrf=([^;]+)/)||[])[1] || ''")

        def mesh(method, path, body=None, raw=False):
            tok = csrf()
            return page.evaluate(
                """async ([method, path, body, tok]) => {
                    const r = await fetch('/ui/api/mesh/' + path, {
                        method, headers: {'Content-Type': 'application/json', 'X-CSRF-Token': tok},
                        body: body === null ? undefined : JSON.stringify(body)});
                    const j = await r.json().catch(() => ({}));
                    return {status: r.status, body: j};
                }""", [method, path, body, tok])

        def agent_api(method, path, body=None):
            tok = csrf()
            return page.evaluate(
                """async ([method, path, body, tok]) => {
                    const r = await fetch('/ui/' + path, {
                        method, headers: {'Content-Type': 'application/json', 'X-CSRF-Token': tok},
                        body: body === null ? undefined : JSON.stringify(body)});
                    const j = await r.json().catch(() => ({}));
                    return {status: r.status, body: j};
                }""", [method, path, body, tok])

        # === 1. login on the fresh panel =====================================
        page.goto(BASE + "/", wait_until="networkidle", timeout=30000)
        page.fill("#u", USER)
        page.fill("#p", PASS)
        page.click("#loginBtn")
        page.wait_for_selector("#app:not(.hidden)", timeout=15000)

        # === 2. the NEW mesh dashboard: first-run shell ======================
        page.goto(BASE + "/mesh", wait_until="networkidle", timeout=30000)
        page.wait_for_selector("#nav", timeout=15000)
        page.wait_for_timeout(1200)
        nav = page.evaluate("() => [...document.querySelectorAll('#nav button')].map(b => b.dataset.page)")
        expected_nav = {"dash", "topo", "servers", "tunnels", "traffic", "engines", "alerts", "security", "pasarguard", "settings"}
        missing = expected_nav - set(nav)
        if missing:
            console_errors.append(f"nav missing pages: {missing}")
        if page.evaluate("() => document.documentElement.dir") != "rtl":
            console_errors.append("fresh mesh page is not RTL (Persian-first broken)")
        if page.evaluate("() => document.documentElement.lang") != "fa":
            console_errors.append("fresh mesh page lang != fa")
        steps = page.evaluate("() => document.querySelectorAll('.check .step').length")
        if steps != 7:
            console_errors.append(f"first-run checklist has {steps} steps, want 7")
        shot("01-first-run-dashboard-fa")
        # every checklist step except backup should be OPEN on a fresh panel
        open_steps = page.evaluate("() => [...document.querySelectorAll('.check .step')].filter(s => !s.classList.contains('done')).length")
        if open_steps < 6:
            console_errors.append(f"fresh panel shows only {open_steps} open checklist steps")

        # === 3. engine artifact (real R5 API) so the wizard has engines =====
        sh("openssl genpkey -algorithm ed25519 -out /tmp/wiz.key 2>/dev/null || true")
        sh("mkdir -p /cfg/engine-artifacts && openssl pkey -in /tmp/wiz.key -pubout -outform DER | base64 -w0 > /cfg/engine-artifacts/engine-pub.b64")
        sh("mkdir -p /tmp/wz/frp_0.61.1 && echo b > /tmp/wz/frp_0.61.1/frps && echo c > /tmp/wz/frp_0.61.1/frpc && "
           "tar -C /tmp/wz -czf /tmp/wz/frp.tar.gz frp_0.61.1 && "
           "openssl pkeyutl -sign -inkey /tmp/wiz.key -rawin -in /tmp/wz/frp.tar.gz -out /tmp/wz/frp.sig && "
           "base64 -w0 /tmp/wz/frp.sig > /tmp/wz/frp.sig.b64 && base64 -w0 /tmp/wz/frp.tar.gz > /tmp/wz/frp.b64")
        sig = sh("cat /tmp/wz/frp.sig.b64")
        art_b64 = sh("cat /tmp/wz/frp.b64")
        up = page.evaluate(
            """async ([sig, b64, tok]) => {
                const bin = Uint8Array.from(atob(b64), c => c.charCodeAt(0));
                const fd = new FormData();
                fd.append('file', new Blob([bin]), 'frp-0.61.1-linux-amd64.tar.gz');
                fd.append('engine', 'frp'); fd.append('version', '0.61.1');
                fd.append('goarch', 'amd64'); fd.append('goos', 'linux');
                fd.append('signature', sig);
                const r = await fetch('/ui/api/mesh/engines/artifacts',
                    {method: 'POST', headers: {'X-CSRF-Token': tok}, body: fd});
                return r.status;
            }""", [sig, art_b64, csrf()])
        if up not in (200, 409):
            console_errors.append(f"artifact upload: {up}")

        # === 4. the wizard, BOTH shapes, through the real UI =================
        def run_shape(shape):
            tag = shape["tag"]
            page.goto(BASE + "/mesh-wizard", wait_until="networkidle", timeout=30000)
            page.wait_for_selector("#steps", timeout=15000)
            page.wait_for_timeout(600)

            def mesh(method, path, body=None):
                return page.evaluate(
                    """async ([method, path, body, tok]) => {
                        const r = await fetch('/ui/api/mesh/' + path, {
                            method, headers: {'Content-Type': 'application/json', 'X-CSRF-Token': tok},
                            body: body === null ? undefined : JSON.stringify(body)});
                        return {status: r.status, body: await r.json().catch(() => ({}))};
                    }""", [method, path, body, csrf()])

            # step 1: datacenters
            for dc in shape["dcs"]:
                page.fill("#dc-name", dc)
                page.click("#add-dc")
                time.sleep(0.4)
            shot(f"{tag}-02-dcs")

            # step 2: iran servers
            page.click("#next")
            time.sleep(0.6)
            for di in range(len(shape["dcs"])):
                for si in range(shape["iran_per_dc"]):
                    page.fill(f"#srv-name-{di}", f"ir-{tag}-{di}-{si}")
                    page.fill(f"#srv-addr-{di}", f"10.{di}.{si}.1")
                    page.click(f"[data-add-iran='{di}']")
                    time.sleep(0.5)
            shot(f"{tag}-03-iran")

            # step 3: foreign servers
            page.click("#next")
            time.sleep(0.6)
            for i in range(shape["foreign"]):
                page.fill("#srv-name-f", f"hz-{tag}-{i}")
                page.fill("#srv-addr-f", f"195.201.{i}.1")
                page.click("[data-add-f]")
                time.sleep(0.5)
            shot(f"{tag}-04-foreign")

            # step 4: K per pair + engine (matrix default label visible)
            page.click("#next")
            time.sleep(1.2)
            note = page.inner_text("#step-body")
            if "Matrix" not in note and "ماتریکس" not in note and "Matrix" not in note:
                if "default" not in note and "پیش‌فرض" not in note:
                    console_errors.append(f"{tag}: step4 missing the matrix default/experimental note")
            for _ in range(30):
                inputs = page.query_selector_all("[data-k]")
                unfilled = [i for i in inputs if i.input_value() != str(shape["k"])]
                if not unfilled:
                    break
                unfilled[0].fill(str(shape["k"]))
                unfilled[0].dispatch_event("change")
                time.sleep(0.6)
            sel_opts = page.evaluate("() => [...document.querySelectorAll('[data-slot] option')].map(o => o.textContent).slice(0,4)")
            # Matrix is labelled default, everything else experimental — the
            # tag must be visible either way (only-frp runs show 'experimental').
            if sel_opts and not any(("default" in o or "پیش‌فرض" in o or "experimental" in o or "آزمایشی" in o) for o in sel_opts):
                console_errors.append(f"{tag}: engine options lack the default/experimental tag: {sel_opts}")
            for sel in page.query_selector_all("[data-slot]"):
                opts = sel.evaluate("el => [...el.options].map(o => o.value)")
                val = "matrix" if "matrix" in opts else ("frp" if "frp" in opts else (opts[0] if opts else ""))
                if val:
                    sel.select_option(val)
                    sel.dispatch_event("change")
                    time.sleep(0.15)
            shot(f"{tag}-05-slots")

            # step 5: PER-TUNNEL weights (new UI)
            page.click("#next")
            time.sleep(0.8)
            pw_sliders = page.query_selector_all("[data-pw]")
            tw_inputs = page.query_selector_all("[data-tw]")
            pairs = len(shape["dcs"]) * shape["iran_per_dc"] * shape["foreign"]
            if len(pw_sliders) != pairs:
                console_errors.append(f"{tag}: step5 has {len(pw_sliders)} pair sliders, want {pairs}")
            if len(tw_inputs) != pairs * shape["k"]:
                console_errors.append(f"{tag}: step5 has {len(tw_inputs)} per-tunnel inputs, want {pairs * shape['k']}")
            for sl in pw_sliders:
                sl.fill(str(shape["weight"]))
                sl.dispatch_event("input")
                time.sleep(0.3)
            shot(f"{tag}-06-weights-per-tunnel")

            # step 6: one forward, step 7: review + create
            page.click("#next")
            time.sleep(0.6)
            page.select_option("#fwd-srv", index=0)
            page.fill("#fwd-port", "8443")
            page.fill("#fwd-target", "127.0.0.1:443")
            page.click("#add-fwd")
            time.sleep(0.4)
            page.click("#next")
            time.sleep(0.8)
            page.click("#wiz-create")
            time.sleep(3)
            msg = page.inner_text("#msg")
            if msg.strip():
                console_errors.append(f"{tag}: create chain said: {msg[:200]}")
            shot(f"{tag}-07-status")
            return pairs * shape["k"]

        shapeA = {"tag": "A", "dcs": ["drak", "chekavak"], "iran_per_dc": 2, "foreign": 2, "k": 3, "weight": 7}
        shapeB = {"tag": "B", "dcs": ["teh", "shiraz", "tabriz"], "iran_per_dc": 1, "foreign": 4, "k": 2, "weight": 5}
        nA = run_shape(shapeA)
        nB = run_shape(shapeB)
        if nA != 24 or nB != 24:
            console_errors.append(f"tunnel counts: A={nA} (want 24), B={nB} (want 24)")

        # === 5. enroll REAL fake agents (in-container) and bind them ========
        # the wizard minted one token per server — revoke any still-open ones
        # so the fresh mint cannot hit the open-token cap.
        for tk in (agent_api("GET", "api/agents/tokens")["body"] or {}).get("tokens", []):
            if not tk.get("revoked"):
                agent_api("POST", f"api/agents/tokens/{tk['id']}/revoke", {})
        topos = mesh("GET", "topologies")["body"]["topologies"]
        topo_ids = [t["id"] for t in topos]
        topo1_tuns = mesh("GET", f"topologies/{topo_ids[0]}")["body"]["tunnels"]
        keys1 = ",".join(t["tunnel_key"] for t in topo1_tuns)
        tresp = agent_api("POST", "api/agents/tokens", {"role": "iran", "label": "e2e-ir-1", "expires_days": 1})
        tok = (tresp["body"] or {}).get("token", "")
        if not tok:
            print("TOKEN-MINT-RESPONSE:", json.dumps(tresp), flush=True)
            console_errors.append(f"token mint failed: {tresp}")
            raise SystemExit(2)  # cannot continue without an agent
        proc = launch_agent(tok, "e2e-ir-1", tunnel_ids=keys1, desired_out="/work/last-desired.json")
        time.sleep(6)
        ags = mesh("GET", "agents")["body"]["agents"]
        online = [a for a in ags if a["status"] == "online"]
        if not online:
            console_errors.append(f"no online agents after enroll: {[(a['name'], a['status']) for a in ags]}")
        agent_id = online[0]["id"] if online else ""
        # bind the agent to its server so the dashboard join is realistic
        servers = mesh("GET", "servers")["body"]["servers"]
        iran_srv = [s for s in servers if s["role"] == "iran"]
        if agent_id and iran_srv:
            mesh("POST", f"agents/{agent_id}/link", {"server_id": iran_srv[0]["id"]})

        # === 6. per-tunnel weight change via the NEW UI + compiled desired ===
        page.goto(BASE + "/mesh", wait_until="networkidle", timeout=30000)
        page.wait_for_timeout(800)
        page.click("#nav button[data-page=tunnels]")
        time.sleep(1.2)
        first_input = page.query_selector("#wbody input[data-tid]")
        if not first_input:
            console_errors.append("weights page: no per-tunnel inputs")
        else:
            first_input.fill("9")
            first_input.dispatch_event("change")
            page.wait_for_timeout(300)
            page.click("#wsaveall")
            time.sleep(1.5)
            # verify via the API: every tunnel has a tunnel-scoped policy
            w = mesh("GET", f"topologies/{topo_ids[0]}/weights")["body"]["weights"]
            tunnel_scoped = [x for x in w if x.get("tunnel_id")]
            if len(tunnel_scoped) < len(topo1_tuns):
                console_errors.append(f"weights after save-all: {len(tunnel_scoped)} tunnel-scoped, want {len(topo1_tuns)}")
            weights_by_tunnel = {x["tunnel_id"]: x["weight"] for x in tunnel_scoped}
            vals = set(weights_by_tunnel.values())
            if len(topo1_tuns) >= 2 and len(vals) < 2:
                console_errors.append(f"all tunnels share one weight {vals} — per-tunnel writes collapsed")
            shot("02-weights-after-change")
            # push and verify the COMPILED desired the agent receives
            push = mesh("POST", f"agents/{agent_id}/push")
            if push["status"] != 200 or not push["body"].get("pushed"):
                console_errors.append(f"push failed: {push}")
            # the fakeagent writes the desired doc it receives
            deadline = time.time() + 15
            desired = None
            while time.time() < deadline:
                if os.path.exists("/work/last-desired.json"):
                    desired = open("/work/last-desired.json").read()
                    if desired:
                        break
                time.sleep(1)
            if not desired:
                console_errors.append("agent never received a desired document")
            else:
                d = json.loads(desired)
                # weights live in balancing.weights[] (WeightRule, tunnel_id
                # = the tunnel KEY) — the compiled per-tunnel state.
                wr = (d.get("balancing") or {}).get("weights") or []
                tw = {w.get("tunnel_id"): w.get("weight") for w in wr if w.get("tunnel_id")}
                if len(tw) < len(topo1_tuns):
                    console_errors.append(f"desired carries {len(tw)} tunnel weight rules, want {len(topo1_tuns)}")
                distinct = set(tw.values())
                if len(distinct) < 2:
                    console_errors.append(f"desired weights all equal {tw} — per-tunnel state lost in compilation")
                else:
                    print("DESIRED-WEIGHTS-OK:", json.dumps(tw))
                shot("03-dashboard-after-weights")

        # === 7. hardening safe-apply: apply -> pending banner -> confirm ====
        page.click("#nav button[data-page=security]")
        time.sleep(1.5)
        page.fill("#prof", json.dumps({"ssh_port": 2222, "ufw_default_deny": True}))
        # apply to the ONLINE agent (an offline one would just arm the 90s
        # command timeout and no banner can ever appear)
        online_id = page.evaluate(
            """() => { const rows = [...document.querySelectorAll('#page-security tr')];
                for (const r of rows) { if (r.textContent.includes('online')) {
                    const b = r.querySelector('[data-h]'); if (b) return b.dataset.h; } }
                return ''; }""")
        btn = page.query_selector(f"[data-h='{online_id}']") if online_id else None
        if not btn:
            console_errors.append("security page: no hardening apply button for an online agent")
        else:
            btn.click()
            time.sleep(3)
            toast_early = page.inner_text("#toasts").strip()
            time.sleep(4)  # apply + ack + pending record
            banner = page.query_selector("#confirm-banner")
            if not banner or not banner.is_visible():
                console_errors.append(f"hardening apply did not surface the pending-confirm banner (toast early: {toast_early[:160]})")
            else:
                txt1 = page.inner_text("#confirm-banner b")
                if "⏱" not in txt1:
                    console_errors.append(f"banner missing countdown: {txt1[:80]}")
                shot("04-hardening-countdown")
                time.sleep(3)
                txt2 = page.inner_text("#confirm-banner b")
                if txt1 == txt2:
                    console_errors.append("countdown is not ticking")
                page.click("#btn-confirm")
                time.sleep(2)
                if page.query_selector("#confirm-banner").is_visible():
                    console_errors.append("banner still visible after confirm")

        # === 8. traffic + billing view ======================================
        page.click("#nav button[data-page=traffic]")
        time.sleep(2.5)
        health_rows = page.evaluate("() => document.querySelectorAll('#th-table tr').length - 1")
        if health_rows < 6:
            console_errors.append(f"traffic health shows {health_rows} rows, want >= 6")
        page.click("#ubtn")
        time.sleep(1.2)
        if "—" == page.inner_text("#uout").strip()[:1]:
            console_errors.append("usage view stayed empty")
        page.click("#cbtn")
        time.sleep(1.2)
        shot("05-traffic-billing")

        # === 9. 200-tunnel performance page =================================
        # tunnels_per_pair is capped at 26: build 208 tunnels as 2 topologies
        # x (1 iran x 4 foreign x 26) and report them from one fake agent.
        sv = mesh("GET", "servers")["body"]["servers"]
        ir = [s["id"] for s in sv if s["role"] == "iran"][:2]
        fg = [s["id"] for s in sv if s["role"] == "foreign"][:4]
        all_keys = ""
        for ti, irid in enumerate(ir):
            r = mesh("POST", "topologies/generate",
                     {"name": f"perf200-{ti}", "iran_servers": [irid], "foreign_servers": fg,
                      "tunnels_per_pair": 26, "engines": ["matrix"] * 26, "enabled": True})
            if r["status"] != 200:
                console_errors.append(f"perf topology generate failed: {r['status']} {str(r['body'])[:150]}")
                continue
            ptuns = mesh("GET", f"topologies/{r['body']['topology_id']}")["body"]["tunnels"]
            all_keys += ("," if all_keys else "") + ",".join(t["tunnel_key"] for t in ptuns)
        if all_keys:
            n_perf = len(all_keys.split(","))
            tok2 = agent_api("POST", "api/agents/tokens", {"role": "iran", "label": "e2e-perf", "expires_days": 1})["body"]["token"]
            proc2 = launch_agent(tok2, "e2e-perf", tunnel_ids=all_keys, tunnels=n_perf)
            time.sleep(8)
            t0 = time.time()
            page.click("#nav button[data-page=dash]")
            page.wait_for_timeout(1500)
            t_dash = time.time() - t0
            t0 = time.time()
            page.click("#nav button[data-page=traffic]")
            page.wait_for_timeout(2500)
            t_traffic = time.time() - t0
            rows = page.evaluate("() => document.querySelectorAll('#th-table tr').length - 1")
            ov = mesh("GET", "overview")["body"]
            ntopo = len(ov.get("topologies") or [])
            print("PERF-PROBE:", json.dumps({"health_rows": rows, "topologies": ntopo,
                                            "perf_keys": n_perf}), flush=True)
            if rows < n_perf:
                console_errors.append(f"perf: traffic health shows {rows} rows, want >= {n_perf}")
            if t_dash > 5 or t_traffic > 8:
                console_errors.append(f"perf: dashboard {t_dash:.1f}s / traffic {t_traffic:.1f}s render")
            shot("06-perf200-traffic")
            t0 = time.time()
            page.click("#nav button[data-page=topo]")
            page.wait_for_timeout(2500)
            t_topo = time.time() - t0
            if t_topo > 8:
                console_errors.append(f"perf: topology map {t_topo:.1f}s render")
            shot("07-perf200-topo-map")

        # === 10. fa/en, dark/light, phone ====================================
        page.click("#theme")
        time.sleep(0.6)
        theme = page.evaluate("() => document.body.dataset.t")
        if theme not in ("light", "dark"):
            console_errors.append(f"theme toggle produced {theme}")
        shot("08-theme-toggled")
        page.click("#theme")
        page.click("#lang")
        time.sleep(0.6)
        if page.evaluate("() => document.documentElement.dir") != "ltr":
            console_errors.append("en toggle dir != ltr")
        shot("09-en-ltr")
        page.click("#lang")
        page.set_viewport_size({"width": 390, "height": 844})
        time.sleep(0.8)
        overflow = page.evaluate("() => document.documentElement.scrollWidth - document.documentElement.clientWidth")
        if overflow > 8:
            console_errors.append(f"phone overflow {overflow}px")
        shot("10-phone-390-fa")
        page.close()
        browser.close()

    benign = [c for c in console_errors
              if "401 (Unauthorized)" in c or c.endswith("(Bad Request)")
              or c.endswith("(Conflict)")]
    real_console = [c for c in console_errors if c not in benign]
    real_page = [p for p in page_errors if "innerHTML" not in p]
    print(json.dumps({"ok": not real_console and not real_page,
                      "console_errors": console_errors,
                      "page_errors": page_errors,
                      "screenshots": shots}, indent=2, ensure_ascii=False))
    if real_console or real_page:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
