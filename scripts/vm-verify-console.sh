#!/usr/bin/env bash
# Final check that the served console is the cleaned build and that the dashboard link is
# host-relative rather than hard-coded to loopback.
set -uo pipefail
ENV_FILE="${ENV_FILE:-$HOME/mihomo-studio/app/.env}"
SECRET=$(grep '^MIHOMO_SECRET=' "$ENV_FILE" | cut -d= -f2-)
B="${STUDIO_URL:-http://192.168.152.130:8080}"
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT

curl -s -m 15 "$B/" > "$WORK/i.html"

echo "=== title and prototype wording ==="
grep -oE '<title>[^<]*</title>' "$WORK/i.html" | sed 's/^/  /'
printf '  "原型" occurrences: %s\n' "$(grep -c '原型' "$WORK/i.html" || true)"
printf '  "演示" occurrences: %s\n' "$(grep -c '演示' "$WORK/i.html" || true)"

echo
echo "=== dashboard link uses the current host, not loopback ==="
if grep -q "window.location.hostname + ':8081'" "$WORK/i.html"; then
  echo "  yes: opens the same host the console was reached on"
else
  echo "  NO: still hard-coded"
fi
grep -c "127.0.0.1:8081" "$WORK/i.html" | sed 's/^/  127.0.0.1:8081 literal occurrences: /'

echo
echo "=== dead prototype handlers removed ==="
for name in previewMade saveGroupDraft openGroupEditor doPublish rollback; do
  printf '  %-18s %s\n' "$name" "$(grep -c "function $name" "$WORK/i.html" || true)"
done

echo
echo "=== login and data still work ==="
printf '  auth/status  -> HTTP %s\n' "$(curl -s -o /dev/null -m 8 -w '%{http_code}' "$B/api/v1/auth/status")"
printf '  verify       -> HTTP %s\n' "$(curl -s -o /dev/null -m 8 -w '%{http_code}' -H "X-Admin-Token: $SECRET" "$B/api/v1/auth/verify")"
curl -s -m 20 -H "X-Admin-Token: $SECRET" "$B/api/v1/overview" > "$WORK/ov.json"
python3 - "$WORK/ov.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding='utf-8'))
print('  runtime      :', d['runtime'].get('version') or 'unknown')
print('  health       : score=%s grade=%s' % (d['health']['score'], d['health']['grade']))
print('  nodes        :', d['nodes']['total'])
PY
