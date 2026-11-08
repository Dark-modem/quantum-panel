#!/usr/bin/env bash
# wizard_container_test.sh — the /mesh-wizard browser smoke in a
# quantum-test-ui container (removed afterwards with docker ps -a proof).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
CT="quantum-test-wiz"
SHOTS_HOST="C:/Users/-Mahdi/Downloads/Quantum/00-ADMIN/ui-screens"
WORK="$(pwd)/tests/.wiz-work"
rm -rf "$WORK"; mkdir -p "$WORK"
cleanup() {
    docker rm -f "$CT" >/dev/null 2>&1 || true
    echo "== proof: the wizard container is gone =="
    docker ps -a --format '{{.Names}}' | grep -x 'quantum-test-wiz' && echo "LEFTOVER!" || echo "quantum-test-wiz: none ✓ (other sessions' containers excluded)"
}
trap cleanup EXIT
DOCKER="docker"; command -v docker >/dev/null 2>&1 || DOCKER=docker.exe
$DOCKER info >/dev/null 2>&1 || { echo "SKIP: docker not reachable"; trap - EXIT; exit 0; }

echo "== building the panel (phase6/wizard worktree) =="
WSLDIR="/mnt/c/Users/-Mahdi/Downloads/Quantum/01-REPOSITORIES/wt-p7-wizard"
MSYS_NO_PATHCONV=1 wsl.exe -d Ubuntu-24.04 -- bash -lc "export GOFLAGS=-mod=mod GOPROXY=file:///tmp/goproxy,https://proxy.golang.org GOSUMDB=off GOPRIVATE=github.com/Dark-modem/* GOPATH=\$HOME/go; export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:\$HOME/go/bin; cd '$WSLDIR' && CGO_ENABLED=0 go build -trimpath -o '$WSLDIR/tests/.wiz-work/quantum-panel' ./cmd/quantum-panel"
[ -f "$WORK/quantum-panel" ] || { echo "FAIL: binary missing"; exit 1; }

WINMOUNT="$(cygpath -m "$(pwd)")"
PORT=18086
docker rm -f "$CT" >/dev/null 2>&1 || true
docker run -d --name "$CT" -p "127.0.0.1:${PORT}:8000" \
    -v "${WINMOUNT}/tests/wizard_smoke.py:/smoke.py:ro" \
    -v "${WINMOUNT}/tests/.wiz-work:/work" \
    -v "${SHOTS_HOST}:/shots" \
    mcr.microsoft.com/playwright/python:v1.49.0-jammy sleep 1800 >/dev/null
docker exec "$CT" bash -c "mkdir -p /cfg"
printf '{"base_path":"ui","port":8000}' > "$WORK/panel.json"
docker cp "$WORK/panel.json" "$CT":/cfg/panel.json
docker exec "$CT" bash -c "export GRE_PANEL_DIR=/cfg; cp /work/quantum-panel /qp && chmod 755 /qp && /qp password wiz-secret-123" >/dev/null 2>&1 || true
docker exec -d "$CT" bash -c "export GRE_PANEL_DIR=/cfg; cd /work && /qp"
up=0
for i in $(seq 1 15); do
    code="$(docker exec "$CT" python -c "import urllib.request; print(urllib.request.urlopen('http://127.0.0.1:8000/ui/api/health', timeout=2).status)" 2>/dev/null | tail -1)"
    [ "$code" = "200" ] && { up=1; break; }
    sleep 2
done
[ "$up" = "1" ] || { echo "FAIL: panel never answered"; exit 1; }
docker exec "$CT" pip3 install -q "playwright==1.49.0" 2>&1 | tail -1 || true
docker exec "$CT" bash -c "cd / && python smoke.py"
SMOKE_RC=$?
echo "== wizard smoke rc=$SMOKE_RC =="
[ "$SMOKE_RC" -eq 0 ]
