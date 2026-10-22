#!/usr/bin/env bash
# Verify authentication and the overview payload against the deployed stack.
set -uo pipefail

ENV_FILE="${ENV_FILE:-$HOME/mihomo-studio/app/.env}"
SECRET=$(grep '^MIHOMO_SECRET=' "$ENV_FILE" | cut -d= -f2-)
B="${STUDIO_URL:-http://192.168.152.130:8080}"

echo "=== anonymous access ==="
for path in /api/v1/rules /api/v1/overview /api/v1/nodes /api/v1/deployments; do
  code=$(curl -s -o /dev/null -m 10 -w '%{http_code}' "$B$path")
  printf '  %-22s -> %s\n' "$path" "$code"
done

echo
echo "=== with admin token ==="
for path in /api/v1/overview /api/v1/nodes /api/v1/deployments; do
  code=$(curl -s -o /dev/null -m 10 -w '%{http_code}' -H "X-Admin-Token: $SECRET" "$B$path")
  printf '  %-22s -> %s\n' "$path" "$code"
done

echo
echo "=== auth status (public) ==="
curl -s -m 10 "$B/api/v1/auth/status"
echo

echo
echo "=== overview payload ==="
curl -s -m 20 -H "X-Admin-Token: $SECRET" "$B/api/v1/overview" > /tmp/ov-vm.json 2>/dev/null || curl -s -m 20 -H "X-Admin-Token: $SECRET" "$B/api/v1/overview" > "$HOME/ov-vm.json"
OV=/tmp/ov-vm.json
[ -f "$OV" ] || OV="$HOME/ov-vm.json"
python3 - "$OV" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding='utf-8'))
print('  runtime :', json.dumps(d['runtime'], ensure_ascii=False))
h = d['health']
print('  health  : score=%s grade=%s blockers=%s warnings=%s' % (h['score'], h['grade'], h['blockers'], h['warnings']))
print('  nodes   :', json.dumps(d['nodes']))
print('  counts  :', json.dumps(d['counts']))
print('  auth    :', json.dumps(d['auth']))
print('  config  :', json.dumps(d['config'], ensure_ascii=False))
print('  checks  :')
for c in h['checks']:
    print('    [%-5s] %-22s -%-3d %s' % (c['severity'], c['id'], c['deduction'], c['title']))
PY
