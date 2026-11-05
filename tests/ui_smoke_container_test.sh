#!/usr/bin/env bash
###############################################################################
# tests/ui_smoke_container_test.sh — headless-browser smoke test of the
# panel's /mesh UI. Everything runs in a quantum-test-* container:
#
#   quantum-test-ui  (mcr.microsoft.com/playwright/python) — runs BOTH the
#     panel binary (throwaway config dir, port 8000 INSIDE the container)
#     and the playwright chromium client against http://127.0.0.1:8000.
#     Nothing is exposed beyond the container; screenshots + console
#     errors come out via a mounted directory.
#
#   Flow: set admin password via the panel CLI → serve → login → open
#   /mesh → click every tab (Topologies/Weights/Profiles/Traffic/
#   PasarGuard) → screenshots → fa toggle → dashboard → summary JSON of
#   all JS console errors.
#
# Requires: docker + the panel binary (built from the CURRENT worktree).
# Run: bash tests/ui_smoke_container_test.sh
###############################################################################
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

CT="quantum-test-ui"
WORK="$(pwd)/tests/.ui-work"
SHOTS="$WORK/shots"
# Keep a prebuilt binary across runs (the WSL go build is slow); rm only
# the screenshots.
rm -rf "$SHOTS"; mkdir -p "$SHOTS"

cleanup() {
    docker rm -f "$CT" >/dev/null 2>&1 || true
    echo "== proof: no quantum-test-* resources remain =="
    docker ps -a --format '{{.Names}}' | grep '^quantum-test' && echo "LEFTOVER!" || echo "containers: none ✓"
}
trap cleanup EXIT

# docker.exe directly + MSYS_NO_PATHCONV: the `docker` SHIM breaks under
# that env var, but docker.exe (a Windows binary) is exactly what we want
# so container-side paths like /cfg and /work survive unconverted.
if command -v docker.exe >/dev/null 2>&1; then
    DOCKER="docker.exe"
    export MSYS_NO_PATHCONV=1
else
    DOCKER="docker"
fi
$DOCKER info >/dev/null 2>&1 || { echo "SKIP: docker daemon not reachable"; trap - EXIT; exit 0; }

echo "== building the panel binary (linux/amd64, current worktree) =="
if command -v go >/dev/null 2>&1; then
    GOFLAGS=-mod=mod GOPROXY=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
        go build -trimpath -o "$WORK/quantum-panel" ./cmd/quantum-panel
else
    # No host go: build via WSL into the (fixed-path) work dir.
    wsl.exe -d Ubuntu-24.04 -- bash -lc "export GOFLAGS=-mod=mod GOPROXY=file:///tmp/goproxy,https://proxy.golang.org GOSUMDB=off GOPRIVATE=github.com/Dark-modem/* GOPATH=\$HOME/go; cd /mnt/c/Users/-Mahdi/Downloads/Quantum/01-REPOSITORIES/wt-phase4-ui && CGO_ENABLED=0 go build -trimpath -o /mnt/c/Users/-Mahdi/Downloads/Quantum/01-REPOSITORIES/wt-phase4-ui/tests/.ui-work/quantum-panel ./cmd/quantum-panel"
fi
[ -f "$WORK/quantum-panel" ] || { echo "FAIL: panel binary missing"; exit 1; }

WINMOUNT="$(cygpath -m "$(pwd)" 2>/dev/null || pwd)"
echo "== container up =="
docker rm -f "$CT" >/dev/null 2>&1 || true
docker run -d --name "$CT" \
    -v "${WINMOUNT}/tests/ui_smoke.py:/smoke.py:ro" \
    -v "${WINMOUNT}/tests/.ui-work:/work" \
    mcr.microsoft.com/playwright/python:v1.49.0-jammy sleep 900 >/dev/null

echo "== panel password + serve (inside the container, port 8000) =="
# NOTE: no `-w /work` args — MSYS path conversion mangles them into
# Windows paths ("Cwd must be an absolute path"). Everything runs via
# bash -c with an internal cd instead.
printf '{"base_path":"ui","port":8000}' > "$WORK/panel.json"
docker exec "$CT" mkdir -p /cfg
docker cp "$WORK/panel.json" "$CT":/cfg/panel.json
docker exec -e GRE_PANEL_DIR=/cfg "$CT" bash -c "mkdir -p /cfg && cp /work/panel.json /cfg/panel.json && /work/quantum-panel password ui-secret-123" >/dev/null 2>&1 \
    || echo "NOTE: password CLI output suppressed"
docker exec -d -e GRE_PANEL_DIR=/cfg "$CT" bash -c "cd /work && /work/quantum-panel"
sleep 3
docker exec "$CT" bash -c "command -v curl >/dev/null && curl -s -o /dev/null -w 'health=%{http_code}\n' http://127.0.0.1:8000/ui/api/health || echo 'no curl in image — playwright will prove reachability'"

echo "== playwright smoke =="
docker exec -e PASSWD=ui-secret-123 "$CT" python /smoke.py
SMOKE_RC=$?

echo "== artifacts =="
docker exec "$CT" ls -la /shots || true
echo "== smoke rc=$SMOKE_RC =="
[ "$SMOKE_RC" -eq 0 ]
