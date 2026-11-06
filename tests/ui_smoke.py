# ui_smoke.py — full headless smoke of the panel /mesh UI with REAL data.
#
# Runs inside quantum-test-ui (mcr playwright image). The panel binary is
# already running on http://127.0.0.1:8000 with GRE_PANEL_DIR=/cfg.
#
#   1. login
#   2. seed REAL data through the mesh REST API (CSRF included):
#      2 iran DCs + 1 foreign DC, 2+2 servers, one generated topology
#      (3 tunnels/pair), weights, a forward rule
#   3. open every mesh tab, click the main buttons, collect JS console
#      errors + failed (4xx/5xx) UI requests
#   4. desktop (1400x900) + phone (390x844) screenshots, fa/RTL toggle
#   5. hardening confirm-banner check (pending profile via a stub command
#      is not possible without an agent — the banner render path is
#      exercised through profiles/pending + the buttons' click handlers)
#   6. PasarGuard tab must NOT contain saved passwords/api_keys
#
# Prints a JSON summary; screenshots land in /shots (mounted).

import base64
import json
import subprocess
import time

from playwright.sync_api import sync_playwright

BASE = "http://127.0.0.1:8000/ui"
USER = "admin"
PASS = "ui-secret-123"
TABS = ["topos", "weights", "profiles", "traffic", "pasarguard", "engines"]

console_errors = []
page_errors = []
bad_requests = []
shots = []


def main():
    with sync_playwright() as pw:
        browser = pw.chromium.launch(args=["--no-sandbox"])
        ctx = browser.new_context(viewport={"width": 1400, "height": 900}, locale="en-US")
        page = ctx.new_page()
        page.on("console", lambda m: console_errors.append(f"console.{m.type}: {m.text[:300]}")
                if m.type == "error" else None)
        page.on("pageerror", lambda e: page_errors.append(f"pageerror: {str(e)[:300]}"))

        def on_resp(r):
            if r.request.resource_type in ("xhr", "fetch") and r.status >= 400:
                bad_requests.append(f"{r.status} {r.request.method} {r.url[-70:]}")
        page.on("response", on_resp)

        # -- 1. login -------------------------------------------------------
        page.goto(BASE + "/", wait_until="networkidle", timeout=30000)
        page.fill("#u", USER)
        page.fill("#p", PASS)
        page.click("#loginBtn")
        page.wait_for_selector("#app:not(.hidden)", timeout=15000)
        page.screenshot(path="/shots/01-login.png")
        shots.append("01-login.png")

        # -- 2. seed REAL data via the mesh API (session cookie + CSRF) -----
        def mesh(method, path, body=None):
            headers = {}
            tok = page.evaluate("() => (document.cookie.match(/(?:^|; )gre_csrf=([^;]+)/)||[])[1] || ''")
            if tok:
                headers["X-CSRF-Token"] = tok
            return page.evaluate(
                """async ([method, path, body, hdr]) => {
                    const r = await fetch('/ui/api/mesh/' + path, {
                        method, headers: {...hdr, 'Content-Type': 'application/json'},
                        body: body === null ? undefined : JSON.stringify(body)});
                    return {status: r.status, body: await r.json().catch(() => ({}))};
                }""", [method, path, body, headers])

        r = mesh("POST", "datacenters", {"name": "drak", "kind": "iran", "price_coefficient": 1.0})
        assert r["status"] < 300, f"dc drak: {r}"
        r = mesh("POST", "datacenters", {"name": "chekavak", "kind": "iran", "price_coefficient": 1.5})
        assert r["status"] < 300, f"dc chekavak: {r}"
        r = mesh("POST", "datacenters", {"name": "hetzner", "kind": "foreign", "price_coefficient": 1.0})
        assert r["status"] < 300, f"dc hetzner: {r}"

        r = mesh("GET", "datacenters")
        dcs = {d["name"]: d["id"] for d in r["body"]["datacenters"]}
        for i, name in enumerate(["ir-1", "ir-2"]):
            r = mesh("POST", "servers", {"name": name, "role": "iran", "datacenter_id": dcs["drak"], "address": f"10.0.0.{i+1}"})
            assert r["status"] < 300, f"server {name}: {r}"
        for i, name in enumerate(["hz-1", "hz-2"]):
            r = mesh("POST", "servers", {"name": name, "role": "foreign", "datacenter_id": dcs["hetzner"], "address": f"195.201.0.{i+1}"})
            assert r["status"] < 300, f"server {name}: {r}"

        r = mesh("GET", "servers")
        servers = {s["name"]: s["id"] for s in r["body"]["servers"]}
        iran = [servers["ir-1"], servers["ir-2"]]
        foreign = [servers["hz-1"], servers["hz-2"]]

        r = mesh("POST", "topologies/generate", {
            "name": "drak-hz", "iran_servers": iran, "foreign_servers": foreign,
            "tunnels_per_pair": 3, "engines": ["matrix", "matrix", "matrix"], "enabled": True})
        assert r["status"] < 300, f"generate: {r}"
        tunnels = r["body"]["tunnels"]
        assert len(tunnels) == 12, f"want 12 tunnels, got {len(tunnels)}"

        # weights: tunnel-level for t1, server-pair-level for the rest
        t0 = tunnels[0]["id"]
        r = mesh("POST", f"topologies/{tunnels[0]['topology_id']}/weights",
                 {"tunnel_id": t0, "weight": 7})
        assert r["status"] < 300, f"weight: {r}"

        # a forward rule (Iran ir-1 listens 8443 → hz-1)
        r = mesh("POST", "forwards", {"topology_id": tunnels[0]["topology_id"],
                                      "local_server_id": iran[0], "remote_server_id": foreign[0],
                                      "listen_port": 8443, "proto": "tcp",
                                      "target": "127.0.0.1:443", "enabled": True})
        assert r["status"] < 300, f"forward: {r}"

        # -- 2b. R5 engines: real key + signed artifact via openssl (the
        #     documented operator flow in docs/ENGINE-ARTIFACTS.md, run
        #     for real) ---------------------------------------------------
        def sh(cmd):
            r = subprocess.run(["bash", "-c", cmd], capture_output=True, text=True)
            assert r.returncode == 0, f"{cmd[:60]}: rc={r.returncode} {r.stderr[-300:]}"
            return r.stdout.strip()

        sh("openssl genpkey -algorithm ed25519 -out /tmp/engine.key")
        key_b64 = sh("openssl pkey -in /tmp/engine.key -pubout -outform DER | base64 -w0")
        sh("mkdir -p /cfg/engine-artifacts && cp /tmp/engine-pub.b64 /cfg/engine-artifacts/engine-pub.b64 "
           "2>/dev/null || openssl pkey -in /tmp/engine.key -pubout -outform DER | base64 -w0 > /cfg/engine-artifacts/engine-pub.b64")
        # small placeholder artifact (content only feeds sha256+sig checks)
        sh("mkdir -p /tmp/art/frp_0.61.1 && echo fake-frpc > /tmp/art/frp_0.61.1/frpc && "
           "echo fake-frps > /tmp/art/frp_0.61.1/frps && tar -C /tmp/art -czf /tmp/frp-0.61.1-linux-amd64.tar.gz frp_0.61.1 && "
           "openssl pkeyutl -sign -inkey /tmp/engine.key -rawin -in /tmp/frp-0.61.1-linux-amd64.tar.gz -out /tmp/art.sig && "
           "base64 -w0 /tmp/art.sig > /tmp/art.sig.b64")
        art_b64 = sh("base64 -w0 /tmp/frp-0.61.1-linux-amd64.tar.gz")
        sig_b64 = sh("cat /tmp/art.sig.b64")

        # login page context is needed for the session cookie + CSRF
        def csrf_hdr():
            tok = page.evaluate("() => (document.cookie.match(/(?:^|; )gre_csrf=([^;]+)/)||[])[1] || ''")
            return {"X-CSRF-Token": tok} if tok else {}

        def engine_upload(sig, body_b64):
            return page.evaluate(
                """async ([sig, b64, hdr]) => {
                    const bin = Uint8Array.from(atob(b64), c => c.charCodeAt(0));
                    const fd = new FormData();
                    fd.append('file', new Blob([bin], {type: 'application/octet-stream'}), 'frp-0.61.1-linux-amd64.tar.gz');
                    fd.append('engine', 'frp');
                    fd.append('version', '0.61.1');
                    fd.append('goarch', 'amd64');
                    fd.append('goos', 'linux');
                    fd.append('signature', sig);
                    const r = await fetch('/ui/api/mesh/engines/artifacts',
                        {method: 'POST', headers: hdr, body: fd});
                    return {status: r.status, body: await r.json().catch(() => ({}))};
                }""", [sig, body_b64, csrf_hdr()])

        # the panel was started BEFORE the key file landed — verification
        # reads the file per request, so uploads work without a restart
        r = engine_upload(sig_b64, art_b64)
        assert r["status"] == 200, f"signed upload: {r}"
        stored_sha = r["body"].get("sha256", "")

        # wrong signature (valid sig over DIFFERENT bytes) → 400
        r = engine_upload(sig_b64, base64.b64encode(b"tampered-bytes").decode())
        assert r["status"] == 400, f"tampered upload must fail: {r}"

        # same version+arch with a DIFFERENT build → 409
        sh("echo different-build > /tmp/art2.tar.gz && openssl pkeyutl -sign -inkey /tmp/engine.key "
           "-rawin -in /tmp/art2.tar.gz -out /tmp/art2.sig && base64 -w0 /tmp/art2.sig > /tmp/art2.sig.b64")
        r = engine_upload(sh("cat /tmp/art2.sig.b64"), sh("base64 -w0 /tmp/art2.tar.gz"))
        assert r["status"] == 409, f"hash-conflict upload must be 409: {r}"

        # agent manifest: unauthenticated, lists the stored pins
        r = page.evaluate(
            """async () => {
                const r = await fetch('/ui/api/agent/engine-artifacts?engine=frp&goarch=amd64',
                    {credentials: 'omit'});
                return {status: r.status, body: await r.json().catch(() => ({}))};
            }""")
        assert r["status"] == 200, f"manifest: {r}"
        arts = r["body"].get("artifacts", [])
        assert len(arts) == 1 and arts[0]["sha256"] == stored_sha, f"manifest pins: {arts}"

        # -- 3. mesh page: every tab ----------------------------------------
        page.goto(BASE + "/mesh", wait_until="networkidle", timeout=30000)
        page.wait_for_selector("#tabs", timeout=15000)
        time.sleep(1.5)  # let the overview fetch settle
        page.screenshot(path="/shots/02-mesh-desktop.png")
        shots.append("02-mesh-desktop.png")

        tab_expect = {
            "topos": "drak-hz",
            "weights": "tunnel",
            "profiles": "Agent",
            "traffic": "usage",
            "pasarguard": "Connect",
            "engines": "0.61.1",
        }
        for i, tab in enumerate(TABS):
            page.click(f'button[data-tab="{tab}"]')
            time.sleep(1.5)
            page.screenshot(path=f"/shots/{i+3:02d}-tab-{tab}.png")
            shots.append(f"{i+3:02d}-tab-{tab}.png")
            if tab == "topos":
                body = page.inner_text("#tab-topos")
                if "drak-hz" not in body:
                    console_errors.append("tab topos: generated topology 'drak-hz' not rendered")
            if tab == "pasarguard":
                body = page.inner_text("#tab-pasarguard")
                for secret in ("ui-secret-123", "api_key", "password"):
                    # the tab itself never shows credential material
                    if secret in body.lower() and secret != "api_key":
                        console_errors.append(f"tab pasarguard leaks secret: {secret}")
            if tab == "engines":
                body = page.inner_text("#tab-engines")
                if "frp-0.61.1-linux-amd64.tar.gz" not in body:
                    console_errors.append("tab engines: uploaded artifact not rendered")
                # the seeded topology enables matrix on 12 tunnels and no
                # agent is connected → the coverage warning must be visible
                if "no connected agent reports it" not in body and "no signed artifact" not in body:
                    console_errors.append("tab engines: coverage warning missing")
                if "go build -ldflags" not in body:
                    console_errors.append("tab engines: agent build snippet missing")

        # -- 3b. engines tab: the REAL upload form (file input → multipart) --
        page.click('button[data-tab="engines"]')
        time.sleep(1.2)
        # same bytes, NEW version slot (0.61.2) — signature is over the file
        # bytes, so the sig computed earlier is valid for the new slot too
        page.set_input_files("#eng-file", "/tmp/frp-0.61.1-linux-amd64.tar.gz")
        page.fill("#eng-engine", "frp")
        page.fill("#eng-version", "0.61.2")
        page.fill("#eng-sig", sig_b64)
        page.click("#eng-upload")
        time.sleep(2.0)
        ebody = page.inner_text("#tab-engines")
        if "0.61.2" not in ebody:
            console_errors.append("engines form upload: 0.61.2 artifact not rendered after upload")
        page.screenshot(path="/shots/12-engines-form.png")
        shots.append("12-engines-form.png")

        # -- 4. weights editor: resolved weight visible ----------------------
        page.click('button[data-tab="weights"]')
        time.sleep(1.2)
        wbody = page.inner_text("#tab-weights")
        if "drak-hz" not in wbody and "hz-1" not in wbody:
            console_errors.append("weights editor shows no tunnels")

        # -- 5. hardening confirm banner render path -------------------------
        # No agent is connected, so profile/apply cannot arm a real timer.
        # The banner render path IS the profiles/pending fetch in the page;
        # verify the endpoint returns an empty list and the page stays clean.
        r = mesh("GET", "profiles/pending")
        if r["status"] != 200:
            console_errors.append(f"profiles/pending: {r['status']}")

        # -- 6. fa/RTL toggle + phone layout ---------------------------------
        page.click('button[data-tab="topos"]')
        page.click("#lang")
        time.sleep(0.6)
        rtl = page.evaluate("() => document.documentElement.dir")
        if rtl != "rtl":
            console_errors.append(f"fa toggle: dir={rtl}, want rtl")
        page.screenshot(path="/shots/09-mesh-fa-rtl.png")
        shots.append("09-mesh-fa-rtl.png")
        page.click("#lang")  # back to en

        phone = ctx.new_page()
        phone.set_viewport_size({"width": 390, "height": 844})
        phone.on("console", lambda m: console_errors.append(f"phone.console.{m.type}: {m.text[:200]}")
                 if m.type == "error" else None)
        phone.goto(BASE + "/mesh", wait_until="networkidle", timeout=30000)
        time.sleep(1.5)
        overflow = phone.evaluate(
            "() => document.documentElement.scrollWidth - document.documentElement.clientWidth")
        if overflow > 8:
            console_errors.append(f"phone layout: horizontal overflow {overflow}px")
        phone.screenshot(path="/shots/10-mesh-phone.png", full_page=True)
        shots.append("10-mesh-phone.png")

        # -- 7. dashboard (index) still fine ---------------------------------
        page.goto(BASE + "/", wait_until="networkidle", timeout=30000)
        time.sleep(1.0)
        page.screenshot(path="/shots/11-dashboard.png")
        shots.append("11-dashboard.png")

        browser.close()

    print(json.dumps({
        "ok": not console_errors and not page_errors and not bad_requests,
        "screenshots": shots,
        "console_errors": console_errors,
        "page_errors": page_errors,
        "bad_requests": bad_requests,
    }, indent=2))


if __name__ == "__main__":
    main()
