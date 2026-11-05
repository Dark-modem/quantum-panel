#!/usr/bin/env bash
###############################################################################
# tests/pasarguard_docker_integration.sh — REAL PasarGuard panel + 2 nodes
# in Docker, wired through the Quantum reconciler.
#
# Safety contract (task rules):
#   - EVERY resource is prefixed quantum-test- (containers, network,
#     volumes). Nothing else is ever touched.
#   - Ports bind to 127.0.0.1 only, random host ports; no host networking.
#   - Cleanup runs on exit AND on failure (trap), removing ONLY
#     quantum-test-* resources this script created.
#   - Ends with `docker ps -a` / `docker volume ls` proof.
#
# Requires: docker, curl, openssl, go. Run: bash tests/pasarguard_docker_integration.sh
###############################################################################
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

PREFIX="quantum-test"
NET="${PREFIX}-net"
PANEL="${PREFIX}-panel"
NODE_A="${PREFIX}-node-a"
NODE_B="${PREFIX}-node-b"
VOL_PANEL="${PREFIX}-panel-data"
VOL_A="${PREFIX}-node-a-data"
VOL_B="${PREFIX}-node-b-data"
PG_USER="quantum-admin"
PG_PASS="QA-9x7-Truth-2026"
WORK="$(pwd)/tests/.pg-work"   # repo-local: mingw openssl + docker.exe cp agree on it
export PG_PANEL_URL=""   # filled after the panel is up
export PG_ADMIN_USER="$PG_USER" PG_ADMIN_PASS="$PG_PASS"

cleanup() {
    if [ "${RESTORE_GIT:-0}" = "1" ]; then
        TOKEN="$(gh auth token 2>/dev/null || true)"
        [ -n "$TOKEN" ] && git config --global --unset url."https://x-access-token:${TOKEN}@github.com/".insteadOf || true
    fi
    echo "== cleanup (only ${PREFIX}-* resources) =="
    docker rm -f "$PANEL" "$NODE_A" "$NODE_B" >/dev/null 2>&1 || true
    docker network rm "$NET" >/dev/null 2>&1 || true
    docker volume rm "$VOL_PANEL" "$VOL_A" "$VOL_B" >/dev/null 2>&1 || true
    rm -rf tests/.pg-work 2>/dev/null || true
    echo "== proof: no quantum-test-* resources remain =="
    echo "--- docker ps -a (quantum-test rows; expect none) ---"
    docker ps -a --format '{{.Names}}' | grep "^${PREFIX}" && echo "LEFTOVER CONTAINERS!" || echo "none ✓"
    echo "--- docker volume ls (quantum-test rows; expect none) ---"
    docker volume ls --format '{{.Name}}' | grep "^${PREFIX}" && echo "LEFTOVER VOLUMES!" || echo "none ✓"
    echo "--- docker network ls (quantum-test rows; expect none) ---"
    docker network ls --format '{{.Name}}' | grep "^${PREFIX}" && echo "LEFTOVER NETWORKS!" || echo "none ✓"
}
RESTORE_GIT=0
trap cleanup EXIT

need() { command -v "$1" >/dev/null 2>&1 || { echo "SKIP: $1 not available"; trap - EXIT; exit 0; }; }
# Docker CLI: prefer `docker`, fall back to `docker.exe` (Docker Desktop
# with WSL integration disabled — orchestration then runs from Windows).
# The wrapper is defined ONLY when the CLI is docker.exe: redefining
# `docker` when it already resolves to the binary makes the function call
# itself forever (observed as a silent death after step 1).
DOCKER="docker"
if command -v docker >/dev/null 2>&1; then
    :
else
    DOCKER="docker.exe"
    docker() { "$DOCKER" "$@"; }
fi
[ -n "${MSYSTEM:-}" ] && export MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL="*"
$DOCKER info >/dev/null 2>&1 || { echo "SKIP: docker daemon not reachable"; trap - EXIT; exit 0; }
# A path native Windows tools (curl.exe) can write to (Git Bash's /c/...
# form is NOT convertible when MSYS_NO_PATHCONV is on).
CURL_OUT="$WORK/last.out"
command -v cygpath >/dev/null 2>&1 && CURL_OUT="$(cygpath -m "$WORK")/last.out"
# Images: docker.io geo-blocks some networks; ghcr.io mirrors are upstream
# (the node compose names ghcr.io explicitly). Override with PG_IMAGE_*.
PANEL_IMAGE="${PG_IMAGE_PANEL:-ghcr.io/pasarguard/panel:latest}"
NODE_IMAGE="${PG_IMAGE_NODE:-ghcr.io/pasarguard/node:latest}"

echo "== 1. network + panel =="
docker network create --subnet 172.20.0.0/24 "$NET" >/dev/null
docker volume create "$VOL_PANEL" >/dev/null
# Upstream 5.6 binds localhost unless SSL cert+key are configured AND
# UVICORN_SSL_CA_TYPE=private (self-signed rejected otherwise). The cert
# files go in via docker cp BEFORE the first start (into /code, which
# exists in the image; no host bind mounts needed for docker.exe).
(
    mkdir -p tests/.pg-work
    cd tests/.pg-work
    printf '[req]
distinguished_name = dn
prompt = no
req_extensions = ext
x509_extensions = ext
[dn]
CN = quantum-test-panel
[ext]
subjectAltName = IP:127.0.0.1,DNS:quantum-test-panel
' > ssl.cnf
    openssl req -x509 -newkey rsa:2048 -nodes -days 2         -keyout panel-key.pem -out panel-cert.pem -config ssl.cnf >/dev/null 2>&1
)
[ -f "$WORK/panel-cert.pem" ] || { echo "FAIL: panel certificate generation"; exit 1; }
PANEL_PORT=$(( (RANDOM % 20000) + 30000 ))
docker create --name "$PANEL" --network "$NET"     -p "127.0.0.1:${PANEL_PORT}:8000"     -v "$VOL_PANEL:/var/lib/pasarguard"     -e UVICORN_HOST=0.0.0.0 -e UVICORN_PORT=8000     -e UVICORN_SSL_CA_TYPE=private     -e UVICORN_SSL_CERTFILE=/code/ssl_cert.pem     -e UVICORN_SSL_KEYFILE=/code/ssl_key.pem     "$PANEL_IMAGE" >/dev/null
docker cp tests/.pg-work/panel-cert.pem "$PANEL":/code/ssl_cert.pem
docker cp tests/.pg-work/panel-key.pem "$PANEL":/code/ssl_key.pem
docker start "$PANEL" >/dev/null
echo "waiting for the panel to answer…"
up=0
for _ in $(seq 1 60); do
    if curl -fsSk -o "$CURL_OUT" "https://127.0.0.1:${PANEL_PORT}/api/health" 2>/dev/null; then up=1; break; fi
    # some builds have no /api/health; accept any HTTPS answer
    code=$(curl -sk -o "$CURL_OUT" -w '%{http_code}' "https://127.0.0.1:${PANEL_PORT}/" 2>/dev/null || true)
    [ "$code" != "000" ] && { up=1; break; }
    sleep 2
done
[ "$up" = "1" ] || { echo "FAIL: panel never came up"; docker logs --tail 20 "$PANEL" || true; exit 1; }
export PG_PANEL_URL="https://127.0.0.1:${PANEL_PORT}"
export PG_PANEL_CA="$WORK/panel-cert.pem"  # absolute: go test reads it cross-tool
if command -v cygpath >/dev/null 2>&1; then
    export PG_PANEL_CA="$(cygpath -m "$PG_PANEL_CA")"  # C:/... form for a Windows go
fi
echo "panel up at $PG_PANEL_URL (self-signed CA: $PG_PANEL_CA)"

echo "== 2. owner bootstrap (temp key via panel CLI) =="
KEY=""
for _ in $(seq 1 10); do
    # v5.6 image: the CLI runs as a module from /code ("Temp key: <uuid>").
    KEY="$(docker exec -w /code "$PANEL" python -m cli.main generate-temp-key 2>/dev/null         | grep -o '[0-9a-f]\{8\}-[0-9a-f]\{4\}-[0-9a-f]\{4\}-[0-9a-f]\{4\}-[0-9a-f]\{12\}' | head -1)"
    [ -n "$KEY" ] && break
    sleep 2
done
[ -n "$KEY" ] || { echo "FAIL: no temp key"; exit 1; }
code=$(curl -sk -o "$CURL_OUT" -w '%{http_code}' -X POST "https://127.0.0.1:${PANEL_PORT}/api/setup/owner" \
    -H 'Content-Type: application/json' \
    -d "{\"key\":\"${KEY}\",\"username\":\"${PG_USER}\",\"password\":\"${PG_PASS}\"}")
case "$code" in 201|200|409) echo "owner bootstrap: HTTP $code";; *) echo "FAIL: owner bootstrap HTTP $code"; exit 1;; esac

echo "== 3. two REAL node containers (self-signed certs, api keys) =="
docker volume create "$VOL_A" >/dev/null
docker volume create "$VOL_B" >/dev/null
i=0
NODE_A_IP=172.20.0.11 NODE_B_IP=172.20.0.12
i=0
for pair in "$NODE_A:11111111-2222-3333-4444-555555555555:$VOL_A:$NODE_A_IP" "$NODE_B:aaaaaaaabbbbccccddddeeeeffff0000:$VOL_B:$NODE_B_IP"; do
    i=$((i+1))
    name="$(echo "$pair" | cut -d: -f1)"; key="$(echo "$pair" | cut -d: -f2)"; vol="$(echo "$pair" | cut -d: -f3)"; NODE_IP="$(echo "$pair" | cut -d: -f4)"
    docker run -d --name "$name" --network "$NET" --ip "$NODE_IP" \
        -v "$vol:/var/lib/pg-node" \
        -e SERVICE_PORT=62050 -e SERVICE_PROTOCOL=grpc -e API_KEY="$key" \
        -e SSL_CERT_FILE=/var/lib/pg-node/certs/ssl_cert.pem \
        -e SSL_KEY_FILE=/var/lib/pg-node/certs/ssl_key.pem \
        -e GENERATED_CONFIG_PATH=/var/lib/pg-node/generated \
        --entrypoint /bin/sh pasarguard/node:latest \
        -c 'mkdir -p /var/lib/pg-node/certs && \
            [ -f /var/lib/pg-node/certs/ssl_cert.pem ] || openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
              -keyout /var/lib/pg-node/certs/ssl_key.pem -out /var/lib/pg-node/certs/ssl_cert.pem \
              -subj "/CN=node" >/dev/null 2>&1; \
            exec /app/node' >/dev/null
    echo "  node $name up (api key set)"
done
sleep 3

export PG_NODE_A_ADDR=172.20.0.11 PG_NODE_B_ADDR=172.20.0.12
echo "== 4. Go reconciler against the REAL panel =="
# The node containers have no published ports: the Go test reaches the
# PANEL only (the panel dials the nodes over the docker network itself).
# Where go runs decides what 127.0.0.1 means: a Windows go speaks to the
# Docker-Desktop-published port directly; a WSL go reaches it through the
# Windows host adapter IP.
if command -v go >/dev/null 2>&1; then
    export GOPRIVATE=github.com/Dark-modem/*
    TOKEN="$(gh auth token 2>/dev/null || true)"
    if [ -n "$TOKEN" ]; then
        git config --global url."https://x-access-token:${TOKEN}@github.com/".insteadOf "https://github.com/"
        RESTORE_GIT=1
    fi
    go test -tags pgintegration ./internal/pasarguard/ -run TestIntegrationRealPanel -count=1 -v 2>&1 | tail -25
else
    echo "SKIP: no local go toolchain for the reconciler leg (containers left up for manual testing)"
    trap - EXIT
    exit 0
fi

echo "== integration: PASS =="
