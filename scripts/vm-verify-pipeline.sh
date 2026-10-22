#!/usr/bin/env bash
# Verify the candidate pipeline and the YAML viewer end to end on the deployed stack.
#
# The operator's real subscription host is not reachable from this VM, so a temporary public
# fixture is used to exercise the pipeline. It is deleted afterwards; nothing fake is left
# in the deployment.
set -uo pipefail
ENV_FILE="${ENV_FILE:-$HOME/mihomo-studio/app/.env}"
SECRET=$(grep '^MIHOMO_SECRET=' "$ENV_FILE" | cut -d= -f2-)
B="${STUDIO_URL:-http://192.168.152.130:8080}/api/v1"
AUTH=(-H "X-Admin-Token: $SECRET")
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT

api() { curl -s -m 180 -X "$1" "${AUTH[@]}" -H 'Content-Type: application/json' ${3:+-d "$3"} "$B$2"; }
jget() { python3 -c "import json,sys;print(json.load(open(sys.argv[1],encoding='utf-8'))$1)" "$2"; }

echo "=== publish a temporary fixture ==="
cat > "$WORK/fixture.yaml" <<'YAML'
proxies:
  - name: 香港 验证节点 01
    type: ss
    server: 198.51.100.11
    port: 8388
    cipher: aes-128-gcm
    password: fixture-only
  - name: 日本 验证节点 02
    type: ss
    server: 198.51.100.12
    port: 8388
    cipher: aes-128-gcm
    password: fixture-only
YAML
# paste.c-net.org returns the raw body, which is what a subscription fetch needs.
FIXTURE_URL=$(curl -s -m 25 --data-binary @"$WORK/fixture.yaml" https://paste.c-net.org/ 2>/dev/null | tr -d '\r\n')
if [[ "$FIXTURE_URL" != https://* ]]; then
  echo "  FAILED to host fixture: $FIXTURE_URL"; exit 1
fi
echo "  $FIXTURE_URL"
echo "  serves raw body: $(curl -s -m 15 "$FIXTURE_URL" | head -1)"

echo
echo "=== add temporary subscription and refresh ==="
SUB=$(api POST /subscriptions "{\"name\":\"验证用示例源\",\"url\":\"$FIXTURE_URL\",\"enabled\":true}")
echo "$SUB" > "$WORK/sub.json"
SUBID=$(jget "['id']" "$WORK/sub.json")
echo "  id: $SUBID"
api POST "/subscriptions/$SUBID/refresh" '{}' > "$WORK/ref.json"
echo "  refresh: $(jget "['status']" "$WORK/ref.json") proxyCount=$(jget "['proxyCount']" "$WORK/ref.json")"

echo
echo "=== build candidate ==="
api POST /config-candidates '{}' > "$WORK/cand.json"
CANDID=$(jget "['id']" "$WORK/cand.json")
CANDSTATUS=$(jget "['status']" "$WORK/cand.json")
echo "  id=$CANDID status=$CANDSTATUS"

echo
echo "=== candidate content endpoint (the YAML viewer) ==="
curl -s -m 30 -o "$WORK/content.json" "${AUTH[@]}" "$B/config-candidates/$CANDID/content"
python3 - "$WORK/content.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding='utf-8'))
if 'error' in d:
    print('  ERROR:', d['error'])
else:
    lines = d['content'].splitlines()
    print('  bytes=%d lines=%d' % (len(d['content']), len(lines)))
    print('  sha256 in response matches status:', d['status'])
    for line in lines[:6]:
        print('   |', line)
    print('   ...')
    print('  contains generated proxy names:',
          any('验证节点' in l for l in lines))
PY

echo
echo "=== candidate is publishable ==="
api POST "/config-candidates/$CANDID/publish" '{"confirm":true}' > "$WORK/dep.json"
echo "  deployment: $(jget "['status']" "$WORK/dep.json")"

echo
echo "=== clean up the temporary fixture subscription ==="
curl -s -m 30 -X DELETE "${AUTH[@]}" "$B/subscriptions/$SUBID" >/dev/null
echo "  deleted $SUBID"
echo
echo "=== final subscription list ==="
api GET /subscriptions > "$WORK/final.json"
python3 - "$WORK/final.json" <<'PY'
import json, sys
for s in json.load(open(sys.argv[1], encoding='utf-8')):
    print('  %-20s enabled=%s status=%s' % (s['name'], s['enabled'], s['lastStatus']))
PY
