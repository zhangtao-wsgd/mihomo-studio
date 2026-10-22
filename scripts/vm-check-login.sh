#!/usr/bin/env bash
# Confirm the panel password works, and show where an operator finds it.
set -uo pipefail
ENV_FILE="${ENV_FILE:-$HOME/mihomo-studio/app/.env}"
SECRET=$(grep '^MIHOMO_SECRET=' "$ENV_FILE" | cut -d= -f2-)
B="${STUDIO_URL:-http://192.168.152.130:8080}"

echo "=== password location ==="
echo "  file: $ENV_FILE"
echo "  key : MIHOMO_SECRET"
echo "  length: ${#SECRET} characters"

echo
echo "=== login flow (as the browser performs it) ==="
printf '  GET  /api/v1/auth/status  -> HTTP %s\n' "$(curl -s -o /dev/null -m 8 -w '%{http_code}' "$B/api/v1/auth/status")"
printf '  POST verify without password -> HTTP %s\n' "$(curl -s -o /dev/null -m 8 -w '%{http_code}' "$B/api/v1/auth/verify")"
printf '  POST verify with password    -> HTTP %s\n' "$(curl -s -o /dev/null -m 8 -w '%{http_code}' -H "X-Admin-Token: $SECRET" "$B/api/v1/auth/verify")"

echo
echo "=== what the console shows after login ==="
curl -s -m 20 -H "X-Admin-Token: $SECRET" "$B/api/v1/overview" > /tmp/ov-login.json 2>/dev/null || true
OV=/tmp/ov-login.json
if [ -s "$OV" ]; then
  python3 - "$OV" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding='utf-8'))
print('  runtime        :', d['runtime'].get('version') or 'unknown', '(reachable=%s)' % d['runtime']['reachable'])
print('  health         : score=%s grade=%s' % (d['health']['score'], d['health']['grade']))
print('  subscriptions  :', d['counts']['subscriptions'])
print('  nodes          :', d['nodes']['total'])
print('  deployments    :', len(d['deployments']))
print('  auth required  :', d['auth']['required'])
PY
fi
