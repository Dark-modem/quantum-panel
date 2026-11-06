#!/usr/bin/env bash
###############################################################################
# tests/ui_smoke_container_test.sh — full /mesh UI smoke in a
# quantum-test-ui container: panel with a throwaway config dir, published
# on 127.0.0.1 only, REAL data seeded through the mesh REST API, every tab
# opened, screenshots + JS console errors + 4xx/5xx UI requests captured.
#
# Screenshots land in 00-ADMIN/ui-screens/ on the host.
###############################################################################
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

CT="quantum-test-ui"
SHOTS_HOST="C:/Users/-Mahdi/Downloads/Quantum/00-ADMIN/ui-screens"
WORK="$(pwd)/tests/.ui-work"
rm -rf "$WORK"; mkdir -p "$WORK"

cleanup() {
    docker rm -f "$CT" >/dev/null 2>&1 || true
    echo "== proof: no quantum-test-* resources remain =="
    docker ps -a --format '{{.Names}}' | grep '^quantum-test' && echo "LEFTOVER!" || echo "containers: none ✓"
}
trap cleanup EXIT

DOCKER="docker"
command -v docker >/dev/null 2>&1 || { DOCKER="docker.exe"; docker() { "$DOCKER" "$@"; }; }
$DOCKER info >/dev/null 2>&1 || { echo "SKIP: docker daemon not reachable"; trap - EXIT; exit 0; }

echo "== building the panel binary (linux/amd64, current worktree) =="
if command -v go >/dev/null 2>&1; then
    GOFLAGS=-mod=mod GOPROXY=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
        go build -trimpath -o "$WORK/quantum-panel" ./cmd/quantum-panel
else
    WSLDIR="$(cygpath -u "$(pwd)")"
    wsl.exe -d Ubuntu-24.04 -- bash -lc "export GOFLAGS=-mod=mod GOPROXY=file:///tmp/goproxy,https://proxy.golang.org GOSUMDB=off GOPRIVATE=github.com/Dark-modem/* GOPATH=\$HOME/go; cd '$WSLDIR' && CGO_ENABLED=0 go build -trimpath -o '$WSLDIR/tests/.ui-work/quantum-panel' ./cmd/quantum-panel"
fi
[ -f "$WORK/quantum-panel" ] || { echo "FAIL: panel binary missing"; exit 1; }

WINMOUNT="$(cygpath -m "$(pwd)" 2>/dev/null || pwd)"
PORT=18083   # outside the Windows/Hyper-V excluded ranges
echo "== container up (panel published on 127.0.0.1:${PORT}) =="
docker rm -f "$CT" >/dev/null 2>&1 || true
docker run -d --name "$CT" \
    -p "127.0.0.1:${PORT}:8000" \
    -v "${WINMOUNT}/tests/ui_smoke.py:/smoke.py:ro" \
    -v "${WINMOUNT}/tests/.ui-work:/work" \
    -v "${SHOTS_HOST}:/shots" \
    mcr.microsoft.com/playwright/python:v1.49.0-jammy sleep 1800 >/dev/null

echo "== panel credential + serve =="
docker exec "$CT" bash -c "mkdir -p /cfg"
printf '{"base_path":"ui","port":8000}' > "$WORK/panel.json"
docker cp "$WORK/panel.json" "$CT":/cfg/panel.json
docker exec "$CT" bash -c "export GRE_PANEL_DIR=/cfg; cp /work/quantum-panel /qp && chmod 755 /qp && /qp password ui-secret-123" >/dev/null 2>&1 \
    || echo "NOTE: password CLI output suppressed"
docker exec -d "$CT" bash -c "export GRE_PANEL_DIR=/cfg; cd /work && /qp"
echo "== waiting for the panel (up to 30 s) =="
up=0
for i in $(seq 1 15); do
    code="$(docker exec "$CT" python -c "import urllib.request; print(urllib.request.urlopen('http://127.0.0.1:8000/ui/api/health', timeout=2).status)" 2>/dev/null | tail -1)"
    [ "$code" = "200" ] && { up=1; echo "panel up after ~$((i*2))s"; break; }
    sleep 2
done
if [ "$up" != "1" ]; then
    docker exec "$CT" cat /work/panel.log 2>/dev/null | tail -15
    echo "FAIL: panel never answered"
    exit 1
fi

echo "== playwright package (browsers already in /ms-playwright) =="
docker exec "$CT" pip3 install -q "playwright==1.49.0" 2>&1 | tail -1 || true

echo "== playwright smoke =="
docker exec "$CT" bash -c "cd / && python smoke.py"
SMOKE_RC=$?

echo "== artifacts =="
MSYS_NO_PATHCONV=1 docker.exe exec "$CT" ls /shots 2>/dev/null || docker exec "$CT" ls /shots || true
echo "== smoke rc=$SMOKE_RC (panel was 127.0.0.1:${PORT}) =="
[ "$SMOKE_RC" -eq 0 ]
