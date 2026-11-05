# ui_smoke.py — headless-browser smoke test of the Quantum panel /mesh UI.
#
# Runs INSIDE the quantum-test-ui-* container (mcr.microsoft.com/playwright
# image). The panel binary runs in the same container on port 8000 with a
# throwaway config dir. Screenshots land in /shots; a JSON summary with all
# JS console errors is printed to stdout.
#
# Tabs exercised: Topologies, Weights, Profiles, Traffic, PasarGuard.

import json
import sys
import time

from playwright.sync_api import sync_playwright

BASE = "http://127.0.0.1:8000/ui"
USER = "admin"
PASS = "ui-secret-123"
TABS = ["topos", "weights", "profiles", "traffic", "pasarguard"]

console_errors = []
page_errors = []
shots = []


def main():
    with sync_playwright() as pw:
        browser = pw.chromium.launch(args=["--no-sandbox"])
        page = browser.new_page(viewport={"width": 1400, "height": 900})
        page.on("console", lambda m: console_errors.append(f"console.{m.type}: {m.text}")
                if m.type == "error" else None)
        page.on("pageerror", lambda e: page_errors.append(f"pageerror: {e}"))

        # -- login ---------------------------------------------------------
        page.goto(BASE + "/", wait_until="networkidle", timeout=30000)
        page.fill("#u", USER)
        page.fill("#p", PASS)
        page.click("#loginBtn")
        page.wait_for_selector("#app:not(.hidden)", timeout=15000)
        page.screenshot(path="/shots/01-login-success.png")
        shots.append("01-login-success.png")

        # -- mesh page + every tab ------------------------------------------
        page.goto(BASE + "/mesh", wait_until="networkidle", timeout=30000)
        page.wait_for_selector("#tabs", timeout=15000)
        page.screenshot(path="/shots/02-mesh-initial.png")
        shots.append("02-mesh-initial.png")

        for i, tab in enumerate(TABS):
            page.click(f'button[data-tab="{tab}"]')
            time.sleep(1.2)  # let fetches settle
            page.screenshot(path=f"/shots/{i + 3:02d}-tab-{tab}.png")
            shots.append(f"{i + 3:02d}-tab-{tab}.png")

        # language toggle (fa) on the mesh page
        page.click("#lang")
        time.sleep(0.5)
        page.screenshot(path="/shots/08-mesh-fa.png")
        shots.append("08-mesh-fa.png")
        page.click("#lang")  # back to en

        # the main panel index (dashboard behind the login)
        page.goto(BASE + "/", wait_until="networkidle", timeout=30000)
        time.sleep(1.0)
        page.screenshot(path="/shots/09-dashboard.png")
        shots.append("09-dashboard.png")

        browser.close()

    print(json.dumps({
        "ok": True,
        "screenshots": shots,
        "console_errors": console_errors,
        "page_errors": page_errors,
    }, indent=2))


if __name__ == "__main__":
    sys.exit(main())
