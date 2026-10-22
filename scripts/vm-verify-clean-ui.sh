#!/usr/bin/env bash
# Confirm the deployed shell carries no fabricated demo rows and that the new endpoints work.
set -uo pipefail
ENV_FILE="${ENV_FILE:-$HOME/mihomo-studio/app/.env}"
SECRET=$(grep '^MIHOMO_SECRET=' "$ENV_FILE" | cut -d= -f2-)
B="${STUDIO_URL:-http://192.168.152.130:8080}"
AUTH=(-H "X-Admin-Token: $SECRET")
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT

curl -s -m 15 "$B/" > "$WORK/i.html"

echo "=== placeholder rows present (real data has not loaded yet in the raw HTML) ==="
printf '  "正在读取" occurrences: %s\n' "$(grep -c '正在读取' "$WORK/i.html" || true)"

echo
echo "=== fabricated demo rows removed from the served template ==="
for needle in 'MacBook Pro' 'node_01a8' 'IEPL 01' 'sub-snap-2026' 'mihomo-primary' 'node_0320'; do
  count=$(grep -c "$needle" "$WORK/i.html" || true)
  printf '  %-18s %s\n' "$needle" "$count"
done

echo
echo "=== sidebar runtime is no longer a fixed claim ==="
for needle in 'Mihomo 在线' 'v1.19.12' '未连接真实 Mihomo'; do
  count=$(grep -c "$needle" "$WORK/i.html" || true)
  printf '  %-22s %s\n' "$needle" "$count"
done

echo
echo "=== rule conflicts endpoint ==="
curl -s -m 20 "${AUTH[@]}" "$B/api/v1/rules/conflicts" > "$WORK/rc.json"
python3 - "$WORK/rc.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding='utf-8'))
print('  checked=%s conflicts=%s' % (d['checked'], len(d['conflicts'])))
for c in d['conflicts']:
    print('   [%s] %s' % (c['kind'], c['name']))
PY

echo
echo "=== overview health ==="
curl -s -m 25 "${AUTH[@]}" "$B/api/v1/overview" > "$WORK/ov.json"
python3 - "$WORK/ov.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding='utf-8'))
h = d['health']
print('  score=%s grade=%s blockers=%s warnings=%s' % (h['score'], h['grade'], h['blockers'], h['warnings']))
for c in h['checks']:
    print('   [%-5s] %-22s -%s' % (c['severity'], c['id'], c['deduction']))
print('  runtime:', json.dumps(d['runtime'], ensure_ascii=False))
PY
