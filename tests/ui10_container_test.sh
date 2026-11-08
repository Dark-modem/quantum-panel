#!/usr/bin/env bash
# ui10_container_test.sh — the phase-10 UI e2e in a quantum-test-ui
# container (real panel binary + real fake agents + headless Chromium),
# removed afterwards with docker ps -a proof. Screenshots land in
# 00-ADMIN/ui-screens/after/.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
CT="quantum-test-ui"
SHOTS_HOST="C:/Users/-Mahdi/Downloads/Quantum/00-ADMIN/ui-screens"
WORK="$(pwd)/tests/.ui10-work"
rm -rf "$WORK"; mkdir -p "$WORK"
cleanup() {
    docker rm -f "$CT" >/dev/null 2>&1 || true
    echo "== proof: the e2e container is gone =="
    docker ps -a --format '{{.Names}}' | grep -x 'quantum-test-ui' && echo "LEFTOVER!" || echo "quantum-test-ui: none ✓ (other sessions' containers excluded)"
}
trap cleanup EXIT
DOCKER="docker"; command -v docker >/dev/null 2>&1 || DOCKER=docker.exe
$DOCKER info >/dev/null 2>&1 || { echo "SKIP: docker not reachable"; trap - EXIT; exit 0; }

echo "== building the panel + fakeagent (linux/amd64, phase10 worktree) =="
WSLDIR="/mnt/c/Users/-Mahdi/Downloads/Quantum/01-REPOSITORIES/wt-p10-ui"
MSYS_NO_PATHCONV=1 wsl.exe -d Ubuntu-24.04 -- bash -c "export GOFLAGS=-mod=mod GOPROXY=file:///tmp/goproxy,https://proxy.golang.org GOSUMDB=off GOPRIVATE=github.com/Dark-modem/* GOPATH=\$HOME/go; export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:\$HOME/go/bin; cd '$WSLDIR' && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o '$WSLDIR/tests/.ui10-work/quantum-panel' ./cmd/quantum-panel && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o '$WSLDIR/tests/.ui10-work/fakeagent' ./tests/fakeagent/ && head -c 4 '$WSLDIR/tests/.ui10-work/quantum-panel' | od -A n -t x1 | grep -q '7f 45 4c 46' && head -c 4 '$WSLDIR/tests/.ui10-work/fakeagent' | od -A n -t x1 | grep -q '7f 45 4c 46' && echo ELFS-OK"
[ -f "$WORK/quantum-panel" ] && [ -f "$WORK/fakeagent" ] || { echo "FAIL: binaries missing"; exit 1; }

WINMOUNT="$(cygpath -m "$(pwd)")"
PORT=18092
docker rm -f "$CT" >/dev/null 2>&1 || true
docker run -d --name "$CT" -p "127.0.0.1:${PORT}:8000" \
    -v "${WINMOUNT}/tests/ui10_smoke.py:/smoke.py:ro" \
    -v "${WINMOUNT}/tests/.ui10-work:/work" \
    -v "${SHOTS_HOST}:/shots" \
    mcr.microsoft.com/playwright/python:v1.49.0-jammy sleep 3600 >/dev/null
docker exec "$CT" bash -c "mkdir -p /cfg && cp /work/quantum-panel /qp && cp /work/fakeagent /fakeagent && chmod 755 /qp /fakeagent"
printf '{"base_path":"ui","port":8000}' > "$WORK/panel.json"
docker cp "$WORK/panel.json" "$CT":/cfg/panel.json
docker exec "$CT" bash -c "export GRE_PANEL_DIR=/cfg; /qp password ui10-secret-123" >/dev/null 2>&1 || true
docker exec -d "$CT" bash -c "export GRE_PANEL_DIR=/cfg; /qp > /work/panel.log 2>&1"
up=0
for i in $(seq 1 15); do
    code="$(docker exec "$CT" python -c "import urllib.request; print(urllib.request.urlopen('http://127.0.0.1:8000/ui/api/health', timeout=2).status)" 2>/dev/null | tail -1)"
    [ "$code" = "200" ] && { up=1; break; }
    sleep 2
done
[ "$up" = "1" ] || { echo "FAIL: panel never answered"; docker exec "$CT" tail -20 /work/panel.log || true; exit 1; }
echo "panel up; running the smoke (takes a few minutes)…"
docker exec "$CT" pip3 install -q "playwright==1.49.0" 2>&1 | tail -1 || true
docker exec "$CT" bash -c "cd / && python smoke.py"
SMOKE_RC=$?
echo "== ui10 smoke rc=$SMOKE_RC =="
exit $SMOKE_RC
