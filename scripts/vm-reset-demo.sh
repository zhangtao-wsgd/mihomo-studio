#!/usr/bin/env bash
# Reset the demo state on the VM: drop stale test records, import the operator's real
# subscription, and build one candidate so every UI surface has real data to render.
set -uo pipefail
ENV_FILE="${ENV_FILE:-$HOME/mihomo-studio/app/.env}"
SECRET=$(grep '^MIHOMO_SECRET=' "$ENV_FILE" | cut -d= -f2-)
B="${STUDIO_URL:-http://192.168.152.130:8080}/api/v1"
AUTH=(-H "X-Admin-Token: $SECRET")
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT

api() { curl -s -m 120 -X "$1" "${AUTH[@]}" -H 'Content-Type: application/json' ${3:+-d "$3"} "$B$2"; }
ids() { python3 -c "import json,sys;print('\n'.join(i['id'] for i in json.load(open(sys.argv[1],encoding='utf-8'))))" "$1"; }

echo "=== current subscriptions ==="
api GET /subscriptions > "$WORK/subs.json"
python3 - "$WORK/subs.json" <<'PY'
import json, sys
for s in json.load(open(sys.argv[1], encoding='utf-8')):
    print('  %-22s status=%-14s %s' % (s['name'], s['lastStatus'], s['url'][:55]))
PY

echo
echo "=== clearing stale demo records ==="
# Order matters: rules and terminals reference groups, groups reference nothing, but the
# API refuses to delete a group that is still referenced.
for path in /rules /terminals /proxy-groups /subscriptions; do
  curl -s -m 30 "${AUTH[@]}" "$B$path" > "$WORK/list.json"
  for id in $(ids "$WORK/list.json"); do
    [[ -z "$id" ]] && continue
    curl -s -m 30 -X DELETE "${AUTH[@]}" "$B$path/$id" >/dev/null
    echo "  deleted $path/$id"
  done
done

echo
echo "=== adding the real subscription ==="
if [[ -z "${SUB_URL:-}" ]]; then
  echo "  SUB_URL not set; skipping import"
else
  SUB=$(api POST /subscriptions "{\"name\":\"主力机场\",\"url\":\"$SUB_URL\",\"enabled\":true}")
  SUBID=$(echo "$SUB" | python3 -c "import json,sys;print(json.load(sys.stdin).get('id',''))")
  echo "  subscription: $SUBID"
  REF=$(api POST "/subscriptions/$SUBID/refresh" '{}')
  echo "  refresh: $(echo "$REF" | python3 -c "import json,sys;d=json.load(sys.stdin);print('proxyCount=%s status=%s' % (d.get('proxyCount'), d.get('status') or d.get('error')))")"
fi

echo
echo "=== building a candidate ==="
api POST /config-candidates '{}' > "$WORK/cand.json"
python3 - "$WORK/cand.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding='utf-8'))
if 'error' in d:
    print('  ERROR:', d['error'])
else:
    print('  id=%s status=%s' % (d['id'], d['status']))
    print('  message:', d.get('validationMessage', '')[:160])
PY
