#!/usr/bin/env bash
B=http://127.0.0.1:18091/audit
CJ=/tmp/audit-cj
CSRF=$(awk '$6=="gre_csrf"{print $7}' $CJ)
mint() {
  curl -s -b $CJ -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
    -X POST $B/api/agents/tokens -d "{\"role\":\"iran\",\"label\":\"$1\",\"expires_days\":1}" \
    | grep -o '"token":"[^"]*"' | head -1 | cut -d'"' -f4
}
KEYS_A=$( { curl -s -b $CJ "$B/api/mesh/topologies/1"; echo; } > /tmp/t1.json; grep -o '"tunnel_key":"[^"]*"' /tmp/t1.json | cut -d'"' -f4 | paste -sd,)
KEYS_B=$( { curl -s -b $CJ "$B/api/mesh/topologies/2"; echo; } > /tmp/t2.json; grep -o '"tunnel_key":"[^"]*"' /tmp/t2.json | cut -d'"' -f4 | paste -sd,)
echo "topo1 keys: ${KEYS_A:0:60}..."
ALL="$KEYS_A,$KEYS_B"
TOK1=$(mint ag-main);  TOK2=$(mint ag-backup); TOK3=$(mint ag-dying); TOK4=$(mint ag-quiet)
nohup /tmp/fakeagent -server $B -token "$TOK1" -name ag-main    -tunnels 8 -tunnel-ids "$ALL" -down 1 > /tmp/fa-ag-main.log 2>&1 &
nohup /tmp/fakeagent -server $B -token "$TOK2" -name ag-backup  -tunnels 2 -tunnel-ids "$KEYS_B" -down 0 > /tmp/fa-ag-backup.log 2>&1 &
nohup /tmp/fakeagent -server $B -token "$TOK3" -name ag-dying   -tunnels 2 -tunnel-ids "$KEYS_B" -down 0 > /tmp/fa-ag-dying.log 2>&1 &
TOK4=""; # ag-quiet never starts (the offline one from a minted-but-unused token)
sleep 14
pkill -f "name ag-dying" 2>/dev/null || true
curl -s -b $CJ -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -X POST "$B/api/mesh/alerts/settings" -d '{"enabled":true,"agent_offline_min":1}' > /dev/null
sleep 6
curl -s -b $CJ "$B/api/agents" > /tmp/ags.json
grep -o '"status":"[a-z]*"' /tmp/ags.json | sort | uniq -c
