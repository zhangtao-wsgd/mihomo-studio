#!/usr/bin/env bash
# Rebuild one candidate so the new CORS origins reach the running controller, then report
# what each port actually serves.
set -uo pipefail
ENV_FILE="${ENV_FILE:-$HOME/mihomo-studio/app/.env}"
SECRET=$(grep '^MIHOMO_SECRET=' "$ENV_FILE" | cut -d= -f2-)
STUDIO="${STUDIO_URL:-http://192.168.152.130:8080}"
CTRL="${CONTROLLER_URL:-http://192.168.152.130:9090}"
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT

api() { curl -s -m 180 -X "$1" -H "X-Admin-Token: $SECRET" -H 'Content-Type: application/json' ${3:+-d "$3"} "$STUDIO/api/v1$2"; }

echo "=== containers ==="
podman ps --format '  {{.Names}}  {{.Status}}  {{.Ports}}'

echo
echo "=== 9090 is an API, not a web page ==="
for p in / /ui/ /version; do
  code=$(curl -s -o /dev/null -m 8 -w '%{http_code}' "$CTRL$p")
  printf '  %-10s without secret -> HTTP %s\n' "$p" "$code"
done
printf '  %-10s with secret    -> HTTP %s\n' "/version" "$(curl -s -o /dev/null -m 8 -w '%{http_code}' -H "Authorization: Bearer $SECRET" "$CTRL/version")"

echo
echo "=== 8081 serves the official dashboard ==="
printf '  GET / -> HTTP %s\n' "$(curl -s -o /dev/null -m 10 -w '%{http_code}' "http://192.168.152.130:8081/")"
curl -s -m 10 "http://192.168.152.130:8081/" | grep -oiE '<title>[^<]*</title>' | head -1

echo
echo "=== publish a fresh candidate so the new CORS origins are applied ==="
api POST /config-candidates '{}' > "$WORK/cand.json"
python3 - "$WORK/cand.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding='utf-8'))
print('  candidate:', d.get('id') or d.get('error'))
print('  status   :', d.get('status'))
PY
CAND=$(python3 -c "import json,sys;print(json.load(open(sys.argv[1],encoding='utf-8')).get('id',''))" "$WORK/cand.json")
if [ -n "$CAND" ]; then
  api POST "/config-candidates/$CAND/publish" '{"confirm":true}' > "$WORK/dep.json"
  python3 -c "import json,sys;d=json.load(open(sys.argv[1],encoding='utf-8'));print('  deployment:', d.get('status'), '-', (d.get('runtimeMessage') or '')[:90])" "$WORK/dep.json"
fi

echo
echo "=== running config CORS origins ==="
podman exec app-mihomo-1 sh -c 'sed -n "/external-controller-cors/,/allow-private-network/p" /runtime/active/config.yaml' 2>&1 | sed 's/^/  /'
