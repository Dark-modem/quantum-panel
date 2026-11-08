#!/usr/bin/env bash
B=http://127.0.0.1:18091/audit
CJ=/tmp/audit-cj
echo "== overview =="
curl -s -b $CJ "$B/api/ui/overview" > /tmp/ov.json
python3 - << 'PY'
import json
d = json.load(open('/tmp/ov.json'))
print('counts:', json.dumps(d['counts'], ensure_ascii=False))
r = d['resources']
print('resources: cpu avg', r['cpu_pct']['avg'], 'max', r['cpu_pct']['max']['name'], '| mem avg', r['mem']['avg_pct'])
t = d['traffic']['today']
print('traffic today: dl', t['billable_download_bytes'], 'ul', t['upload_bytes'], 'per-dc:', len(d['traffic']['per_datacenter']))
print('alerts:', d['alerts'], 'pending:', d['pending_confirmations'], 'cert:', d['certificate'])
PY
echo "== agents list =="
curl -s -b $CJ "$B/api/ui/agents" > /tmp/agl.json
python3 - << 'PY'
import json
d = json.load(open('/tmp/agl.json'))
for a in d['agents']:
    print(a['name'], a['status'], (a.get('server') or {}).get('datacenter'))
PY
AG=$(python3 -c "import json; d=json.load(open('/tmp/agl.json')); print([a['id'] for a in d['agents'] if a['status']=='online'][0])")
echo "== agent detail ($AG) =="
curl -s -b $CJ "$B/api/ui/agents/$AG" > /tmp/agd.json
python3 - << 'PY'
import json
d = json.load(open('/tmp/agd.json'))
a = d['agent']
print('cpu:', a['cpu']['percent'], 'mem:', a['mem']['used_mb'], '/', a['mem']['total_mb'], 'uptime:', a['uptime_s'])
print('topologies:', [(t['name'], t['my_role']) for t in d['topologies']])
for e in d['engines']:
    print('engine:', e['engine'], 'installed', e['installed'], 'enabled', e['enabled'], 'instances', e['instances'], 'health', e['health'])
PY
echo "== topology detail =="
curl -s -b $CJ "$B/api/ui/topologies/1" > /tmp/tpd.json
python3 - << 'PY'
import json
d = json.load(open('/tmp/tpd.json'))
print('members iran:', [(s['name'], len(s['tunnels'])) for s in d['members']['iran']])
print('members foreign:', [(s['name'], len(s['tunnels'])) for s in d['members']['foreign']])
t0 = d['tunnels'][0]
print('tunnel0:', t0['tunnel_key'], t0['state'], 'w', t0['weight'], 'pct', t0['resolved_percent'], 'today', t0['traffic_today']['wire_rx_bytes'])
print('totals:', json.dumps(d['totals']['tunnels']))
PY
echo "== tunnel detail =="
TK=$(python3 -c "import json; d=json.load(open('/tmp/tpd.json')); print(d['tunnels'][0]['tunnel_key'])")
curl -s -b $CJ "$B/api/ui/tunnels/$TK?range=24h" > /tmp/tnd.json
python3 - << 'PY'
import json
d = json.load(open('/tmp/tnd.json'))
n = d['now']
print('now:', n['state'], 'cpu', n['cpu_percent'], 'rss', n['rss_bytes'], 'lat', n['latency_ms'], 'loss', n['loss_pct'], 'up', n['uptime_s'])
print('rates:', json.dumps(n['rate']))
print('totals keys:', list(d['totals'].keys()))
h = d['history']
print('history:', h['range'], h['granularity'], 'points', len(h['points']))
print('events:', len(d['events']), 'errors:', len(d['errors_recent']))
print('ratios:', d['ratios'])
PY
