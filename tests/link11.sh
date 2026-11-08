#!/usr/bin/env bash
B=http://127.0.0.1:18091/audit
CJ=/tmp/audit-cj
CSRF=$(awk '$6=="gre_csrf"{print $7}' $CJ)
link() { # agent_id server_id
  curl -s -b $CJ -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
    -X POST "$B/api/mesh/agents/$1/link" -d "{\"server_id\":$2}"
  echo " -> $2"
}
curl -s -b $CJ "$B/api/ui/agents" > /tmp/agl.json
curl -s -b $CJ "$B/api/mesh/servers" > /tmp/svs.json
python3 - << 'PY' > /tmp/links.txt
import json
ags = json.load(open('/tmp/agl.json'))['agents']
svs = json.load(open('/tmp/svs.json'))['servers']
iran = {s['name']: s['id'] for s in svs if s['role'] == 'iran'}
assign = {'ag-main': 'tehran-1', 'ag-backup': 'shiraz-1', 'ag-dying': 'shiraz-1'}
for a in ags:
    t = assign.get(a['name'])
    if t:
        print(a['id'], iran[t])
PY
while read -r AID SID; do link "$AID" "$SID"; done < /tmp/links.txt
sleep 1
curl -s -b $CJ "$B/api/ui/overview" > /tmp/ov.json
python3 -c "
import json
d=json.load(open('/tmp/ov.json'))
print('agents counts:', d['counts']['agents'])
print('per-dc:', [(x['name'], x['billable_download_bytes']) for x in d['traffic']['per_datacenter']])"
curl -s -b $CJ "$B/api/ui/topologies/1" > /tmp/tpd.json
python3 -c "
import json
d=json.load(open('/tmp/tpd.json'))
print('topo1 tunnels:', d['totals']['tunnels'], 'rows:', len(d['tunnels']))
print('pct:', [round(t['resolved_percent'],1) for t in d['tunnels']])"
