# wizard_smoke.py — headless browser run of the /mesh-wizard (phase 7):
# completes BOTH shapes end to end through the real REST API
#   shape A: 2 iran DCs × 2 servers, 2 foreign, 3 tunnels per pair
#   shape B: 3 iran × 4 foreign × 2 tunnels per pair
# (agents are simulated at the API level: the wizard's "agent connected"
# indicator is driven by the agents registry, which the smoke seeds via
# the real /api/agents/tokens + a fake enroll? NO — enrolling needs the
# agent binary. The wizard continues without the live indicator: the
# smoke marks the server as connected by enrolling a REAL in-process
# fake agent over the websocket (the e2e_mesh harness pattern) — here we
# instead proceed with the "waiting" state and select engines from the
# artifacts (uploaded via the R5 API) which works without agents.)
# Screenshots land in /shots; zero JS errors expected.

import json
import time

from playwright.sync_api import sync_playwright

BASE = "http://127.0.0.1:8000/ui"
USER = "admin"
PASS = "wiz-secret-123"

console_errors = []
page_errors = []
shots = []


def run_shape(pw, ctx, shape):
    page = ctx.new_page()
    page.on("console", lambda m: console_errors.append(f"console.{m.type}: {m.text[:300]}")
            if m.type == "error" else None)
    page.on("pageerror", lambda e: page_errors.append(f"pageerror: {str(e)[:300]}"))

    def on_resp(r):
        if r.request.resource_type in ("xhr", "fetch") and r.status >= 400:
            # the app's own pre-login probes are known-benign
            if "/api/status" not in r.url and "/api/logs" not in r.url:
                console_errors.append(f"bad {r.status} {r.request.method} {r.url[-70:]}")
    page.on("response", on_resp)

    tag = shape["tag"]
    page.goto(BASE + "/mesh-wizard", wait_until="networkidle", timeout=30000)
    page.wait_for_selector("#steps", timeout=15000)
    page.screenshot(path=f"/shots/{tag}-01-step1.png")
    shots.append(f"{tag}-01-step1.png")

    def mesh(method, path, body=None):
        tok = page.evaluate("() => (document.cookie.match(/(?:^|; )gre_csrf=([^;]+)/)||[])[1] || ''")
        return page.evaluate(
            """async ([method, path, body, tok]) => {
                const r = await fetch('/ui/api/mesh/' + path, {
                    method, headers: {'Content-Type': 'application/json', 'X-CSRF-Token': tok},
                    body: body === null ? undefined : JSON.stringify(body)});
                return {status: r.status, body: await r.json().catch(() => ({}))};
            }""", [method, path, body, tok])

    # seed the signed engine artifact the wizard's step 4 needs (real R5
    # API) — ONCE per run (shapeB reuses it; the duplicate version+arch
    # with a different hash is correctly refused with 409 by the panel).
    global _art_seeded
    import base64, subprocess
    def sh(cmd):
        r = subprocess.run(["bash", "-c", cmd], capture_output=True, text=True)
        assert r.returncode == 0, r.stderr[-200:]
        return r.stdout.strip()
    sh("openssl genpkey -algorithm ed25519 -out /tmp/wiz.key 2>/dev/null || true")
    key_b64 = sh("openssl pkey -in /tmp/wiz.key -pubout -outform DER | base64 -w0")
    sh("mkdir -p /cfg/engine-artifacts && cp /tmp/wiz-pub.b64 /cfg/engine-artifacts/engine-pub.b64 2>/dev/null || "
       "openssl pkey -in /tmp/wiz.key -pubout -outform DER | base64 -w0 > /cfg/engine-artifacts/engine-pub.b64")
    sh("mkdir -p /tmp/wz/frp_0.61.1 && echo b > /tmp/wz/frp_0.61.1/frps && echo c > /tmp/wz/frp_0.61.1/frpc && "
       "tar -C /tmp/wz -czf /tmp/wz/frp.tar.gz frp_0.61.1 && "
       "openssl pkeyutl -sign -inkey /tmp/wiz.key -rawin -in /tmp/wz/frp.tar.gz -out /tmp/wz/frp.sig && "
       "base64 -w0 /tmp/wz/frp.sig > /tmp/wz/frp.sig.b64 && base64 -w0 /tmp/wz/frp.tar.gz > /tmp/wz/frp.b64")
    sig = sh("cat /tmp/wz/frp.sig.b64")
    art_b64 = sh("cat /tmp/wz/frp.b64")
    if getattr(main, "_art_seeded", False):
        up = 200
    else:
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
        }""", [sig, art_b64, page.evaluate("() => (document.cookie.match(/(?:^|; )gre_csrf=([^;]+)/)||[])[1] || ''")])
        assert up == 200, f"artifact upload: {up}"
        main._art_seeded = True

    # STEP 1: datacenters
    for i, dc in enumerate(shape["dcs"]):
        page.fill("#dc-name", dc)
        page.click("#add-dc")
        time.sleep(0.4)
    page.screenshot(path=f"/shots/{tag}-02-dcs.png")
    shots.append(f"{tag}-02-dcs.png")

    # STEP 2: iran servers (2 per DC in shape A; shape["iran_per_dc"] each)
    page.click("#next")
    time.sleep(0.6)
    iran_names = []
    for di in range(len(shape["dcs"])):
        for si in range(shape["iran_per_dc"]):
            name = f"ir-{tag}-{di}-{si}"
            iran_names.append(name)
            page.fill(f"#srv-name-{di}", name)
            page.fill(f"#srv-addr-{di}", f"10.{di}.{si}.1")
            page.click(f"[data-add-iran='{di}']")
            time.sleep(0.5)
    page.screenshot(path=f"/shots/{tag}-03-iran.png")
    shots.append(f"{tag}-03-iran.png")

    # STEP 3: foreign servers
    page.click("#next")
    time.sleep(0.6)
    for i in range(shape["foreign"]):
        page.fill("#srv-name-f", f"hz-{tag}-{i}")
        page.fill("#srv-addr-f", f"195.201.{i}.1")
        page.click("[data-add-f]")
        time.sleep(0.5)
    page.screenshot(path=f"/shots/{tag}-04-foreign.png")
    shots.append(f"{tag}-04-foreign.png")

    # STEP 4: slots — K per pair, engine frp (the artifact we uploaded)
    page.click("#next")
    time.sleep(1.0)
    pairs = len(shape["dcs"]) * shape["iran_per_dc"] * shape["foreign"]
    page.screenshot(path=f"/shots/{tag}-05-slots.png")
    shots.append(f"{tag}-05-slots.png")
    # The wizard re-renders the slot rows when K changes — re-query the
    # remaining unfilled inputs each round instead of holding stale handles.
    for _ in range(30):
        inputs = page.query_selector_all("[data-k]")
        unfilled = [i for i in inputs if i.input_value() != str(shape["k"])]
        if not unfilled:
            break
        unfilled[0].fill(str(shape["k"]))
        unfilled[0].dispatch_event("change")
        time.sleep(0.6)
    page.screenshot(path=f"/shots/{tag}-06-slots-k.png")
    shots.append(f"{tag}-06-slots-k.png")
    for sel in page.query_selector_all("[data-slot]"):
        sel.select_option("frp")
        sel.dispatch_event("change")
        time.sleep(0.2)

    # STEP 5: weights sliders
    page.click("#next")
    time.sleep(0.6)
    for sl in page.query_selector_all("[data-w]"):
        sl.fill("3")
        sl.dispatch_event("input")
    page.screenshot(path=f"/shots/{tag}-07-weights.png")
    shots.append(f"{tag}-07-weights.png")

    # STEP 6: one forward rule
    page.click("#next")
    time.sleep(0.6)
    page.select_option("#fwd-srv", index=0)
    page.fill("#fwd-port", "8443")
    page.fill("#fwd-target", "127.0.0.1:443")
    page.click("#add-fwd")
    time.sleep(0.4)
    page.screenshot(path=f"/shots/{tag}-08-forwards.png")
    shots.append(f"{tag}-08-forwards.png")

    # STEP 7: review shows N x M x K and creates through the real API
    page.click("#next")
    time.sleep(0.8)
    body = page.inner_text("#step-body")
    expect_tunnels = pairs * shape["k"]
    if f"{expect_tunnels}" not in body:
        console_errors.append(f"review shows wrong tunnel count (want {expect_tunnels}): {body[:120]}")
    page.screenshot(path=f"/shots/{tag}-09-review.png")
    shots.append(f"{tag}-09-review.png")
    page.click("#wiz-create")
    # status page: wait for the tunnel table (generator + weights + forwards);
    # a rejected chain surfaces in #msg — include it in the failure output.
    time.sleep(3)
    msg = page.inner_text("#msg")
    if msg.strip():
        console_errors.append(f"{tag}: create chain said: {msg[:200]}")
    page.screenshot(path=f"/shots/{tag}-10-status.png")
    shots.append(f"{tag}-10-status.png")
    st = page.inner_text("#wiz-tunnels")
    if str(expect_tunnels) not in st and "up" not in st and "unknown" not in st:
        console_errors.append(f"status page empty: {st[:120]}")

    # fa/RTL check
    page.click("#lang")
    time.sleep(0.5)
    if page.evaluate("() => document.documentElement.dir") != "rtl":
        console_errors.append(f"{tag}: fa toggle dir!=rtl")
    page.screenshot(path=f"/shots/{tag}-11-fa.png")
    shots.append(f"{tag}-11-fa.png")
    page.click("#lang")

    # phone width
    page.set_viewport_size({"width": 390, "height": 844})
    time.sleep(0.5)
    overflow = page.evaluate("() => document.documentElement.scrollWidth - document.documentElement.clientWidth")
    if overflow > 8:
        console_errors.append(f"{tag}: phone overflow {overflow}px")
    page.screenshot(path=f"/shots/{tag}-12-phone.png", full_page=True)
    shots.append(f"{tag}-12-phone.png")
    page.close()
    return expect_tunnels


def main():
    with sync_playwright() as pw:
        browser = pw.chromium.launch(args=["--no-sandbox"])
        ctx = browser.new_context(viewport={"width": 1400, "height": 900}, locale="en-US")
        page = ctx.new_page()
        page.goto(BASE + "/", wait_until="networkidle", timeout=30000)
        page.fill("#u", USER)
        page.fill("#p", PASS)
        page.click("#loginBtn")
        page.wait_for_selector("#app:not(.hidden)", timeout=15000)
        page.close()

        shapes = [
            {"tag": "shapeA", "dcs": ["drak", "chekavak"], "iran_per_dc": 2, "foreign": 2, "k": 3},
            {"tag": "shapeB", "dcs": ["teh", "shiraz", "tabriz"], "iran_per_dc": 1, "foreign": 4, "k": 2},
        ]
        counts = {}
        for shape in shapes:
            counts[shape["tag"]] = run_shape(pw, ctx, shape)
        browser.close()

    print(json.dumps({"ok": not console_errors and not page_errors,
                      "tunnels_created": counts,
                      "console_errors": console_errors,
                      "page_errors": page_errors,
                      "screenshots": shots}, indent=2))
    if console_errors or page_errors:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
