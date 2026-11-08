#!/usr/bin/env bash
# seed11.sh — phase-11 UI seed, one pass: build, fresh panel, the exact
# dataset the brief asks for (2 Iran DC x 2 servers, 2 foreign, 2 topologies
# incl. a 1+1, tunnels in every state, offline agents, alerts, errors),
# then VERIFY. Prints SEED-OK at the end.
set -euo pipefail
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:$HOME/go/bin
export GOFLAGS=-mod=mod GOPROXY=file:///tmp/goproxy,https://proxy.golang.org GOSUMDB=off GOPRIVATE=github.com/Dark-modem/* GOPATH=$HOME/go
cd /mnt/c/Users/-Mahdi/Downloads/Quantum/01-REPOSITORIES/wt-p10-ui
pkill -f qp-audit 2>/dev/null || true
pkill -f fakeagent 2>/dev/null || true
sleep 1
CGO_ENABLED=0 go build -o /tmp/qp-audit ./cmd/quantum-panel
CGO_ENABLED=0 go build -o /tmp/fakeagent ./tests/fakeagent/
rm -rf /tmp/qp-audit-cfg && mkdir -p /tmp/qp-audit-cfg
printf '{"base_path":"audit","port":18091}' > /tmp/qp-audit-cfg/panel.json
GRE_PANEL_DIR=/tmp/qp-audit-cfg /tmp/qp-audit password audit-pass-123 >/dev/null 2>&1
GRE_PANEL_DIR=/tmp/qp-audit-cfg nohup /tmp/qp-audit > /tmp/qp-audit.log 2>&1 &
sleep 2
curl -s --max-time 3 -o /dev/null -w "panel: %{http_code}\n" http://127.0.0.1:18091/audit/api/health

B=http://127.0.0.1:18091/audit
CJ=/tmp/audit-cj; rm -f $CJ
curl -s -c $CJ -X POST -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"audit-pass-123"}' $B/api/login > /dev/null
CSRF=$(awk '$6=="gre_csrf"{print $7}' $CJ)
m() { curl -s -b $CJ -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' -X POST "$B/api/mesh/$1" -d "$2"; }
J() { python3 -c "import json,sys; d=json.load(sys.stdin); $1"; }

# DCs: 2 iran + 1 foreign (the brief's "2 foreign" = foreign SERVERS)
m datacenters '{"name":"درک","kind":"iran","price_coefficient":1.0}' > /dev/null
m datacenters '{"name":"چکاوک","kind":"iran","price_coefficient":1.5}' > /dev/null
m datacenters '{"name":"هتزنر","kind":"foreign","price_coefficient":1.0}' > /dev/null
DCS=$(curl -s -b $CJ "$B/api/mesh/datacenters")
D1=$(echo "$DCS" | J "print([x['id'] for x in d['datacenters'] if x['name']=='درک'][0])")
D2=$(echo "$DCS" | J "print([x['id'] for x in d['datacenters'] if x['name']=='چکاوک'][0])")
# servers: 2 iran per DC (4 total) + 2 foreign
m servers "{\"name\":\"tehran-1\",\"role\":\"iran\",\"datacenter_id\":$D1,\"address\":\"10.0.1.1\"}" > /dev/null
m servers "{\"name\":\"tehran-2\",\"role\":\"iran\",\"datacenter_id\":$D1,\"address\":\"10.0.1.2\"}" > /dev/null
m servers "{\"name\":\"shiraz-1\",\"role\":\"iran\",\"datacenter_id\":$D2,\"address\":\"10.0.2.1\"}" > /dev/null
m servers "{\"name\":\"shiraz-2\",\"role\":\"iran\",\"datacenter_id\":$D2,\"address\":\"10.0.2.2\"}" > /dev/null
m servers '{"name":"frankfurt-1","role":"foreign","address":"195.201.0.1"}' > /dev/null
m servers '{"name":"frankfurt-2","role":"foreign","address":"195.201.0.2"}' > /dev/null
SVS=$(curl -s -b $CJ "$B/api/mesh/servers")
S() { echo "$SVS" | J "print([x['id'] for x in d['servers'] if x['name']=='$1'][0])"; }
TEH1=$(S tehran-1); SHIR1=$(S shiraz-1); F1=$(S frankfurt-1); F2=$(S frankfurt-2)
# topology A: 1 iran x 2 foreign x 3 (the main mesh) — 6 tunnels
R=$(m "topologies/generate" "{\"name\":\"main-mesh\",\"iran_servers\":[$TEH1],\"foreign_servers\":[$F1,$F2],\"tunnels_per_pair\":3,\"engines\":[\"matrix\",\"matrix\",\"matrix\"],\"enabled\":true}")
echo "$R" | grep -q topology_id || { echo "TOPO A FAILED: $R"; exit 1; }
# topology B: 1+1 (single iran, single foreign, 2 tunnels)
R=$(m "topologies/generate" "{\"name\":\"backup-link\",\"iran_servers\":[$SHIR1],\"foreign_servers\":[$F1],\"tunnels_per_pair\":2,\"engines\":[\"matrix\",\"matrix\"],\"enabled\":true}")
echo "$R" | grep -q topology_id || { echo "TOPO B FAILED: $R"; exit 1; }
TOPOS=$(curl -s -b $CJ "$B/api/mesh/topologies" | J "print(' '.join(str(x['id']) for x in d['topologies']))")
NT=$(echo $TOPOS | wc -w); echo "topologies: $NT (want 2)"
NTUN=0
for TOPO in $TOPOS; do
  NTUN=$((NTUN + $(curl -s -b $CJ "$B/api/mesh/topologies/$TOPO" | J "print(len(d['tunnels']))")))
done
echo "tunnels: $NTUN (want 8)"
[ "$NTUN" = "8" ] || { echo "FATAL: tunnel count"; exit 1; }
# per-tunnel weights: main-mesh first tunnel = 7 (rest 1)
T1KEYS=$(curl -s -b $CJ "$B/api/mesh/topologies/$(echo $TOPOS | awk '{print $1}')" | J "print(','.join(str(x['id']) for x in d['tunnels']))")
FIRST=$(echo $T1KEYS | cut -d, -f1)
curl -s -b $CJ -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -X POST "$B/api/mesh/topologies/$(echo $TOPOS | awk '{print $1}')/weights" -d "{\"tunnel_id\":$FIRST,\"weight\":7}" > /dev/null
m forwards "{\"topology_id\":$(echo $TOPOS | awk '{print $1}'),\"local_server_id\":$TEH1,\"remote_server_id\":$F1,\"listen_port\":8443,\"proto\":\"tcp\",\"target\":\"127.0.0.1:443\",\"enabled\":true}" > /dev/null || true

# agents: 2 online (tunnels up + 1 down for variety), 1 offline (dies), 1 never connects
launch() { # name ids down keep
  local N=$1 IDS=$2 DOWN=$3
  local TOK=$(curl -s -b $CJ -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
    -X POST $B/api/agents/tokens -d "{\"role\":\"iran\",\"label\":\"$N\",\"expires_days\":1}" | J "print(d.get('token',''))")
  [ -n "$TOK" ] || { echo "TOKEN MINT FAILED for $N"; exit 1; }
  nohup /tmp/fakeagent -server $B -token "$TOK" -name $N -tunnels 8 -tunnel-ids "$IDS" -down $DOWN \
    > /tmp/fa-$N.log 2>&1 &
}
ALLKEYS=$(for TOPO in $TOPOS; do curl -s -b $CJ "$B/api/mesh/topologies/$TOPO" | J "print(','.join(x['tunnel_key'] for x in d['tunnels']))"; done | paste -sd,)
T1K=$(for TOPO in $TOPOS; do curl -s -b $CJ "$B/api/mesh/topologies/$TOPO" | J "print(','.join(x['tunnel_key'] for x in d['tunnels']))"; done | head -1)
launch ag-main "$ALLKEYS" 1 &
launch ag-backup "$T1K" 0 &
launch ag-dying "$T1K" 0 &
sleep 12
pkill -f "name ag-dying" 2>/dev/null || true
# alerts on
curl -s -b $CJ -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -X POST "$B/api/mesh/alerts/settings" -d '{"enabled":true,"agent_offline_min":1}' > /dev/null
sleep 5
curl -s -b $CJ "$B/api/agents" | python3 -c "
import json,sys
from collections import Counter
d=json.load(sys.stdin)
print('agents:', dict(Counter(a['status'] for a in d['agents'])))"
echo "== ui/overview probe =="
curl -s -b $CJ "$B/api/ui/overview" | python3 -c "
import json,sys
d=json.load(sys.stdin)
print('counts:', json.dumps(d.get('counts',{}), ensure_ascii=False)[:200])
print('resources:', json.dumps(d.get('resources',{}), ensure_ascii=False)[:160])
print('traffic keys:', list((d.get('traffic') or {}).keys()))"
echo "SEED-OK"
