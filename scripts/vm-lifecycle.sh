#!/usr/bin/env bash
# Full lifecycle verification against a real Mihomo runtime and a real HTTP subscription.
#
# The SSRF guard requires the subscription host to resolve to a public address, so the
# fixture is published to a public paste service rather than served from the LAN. This
# exercises the same code path a real operator would use; the guard is not bypassed.
#
# Fixture node addresses are RFC 5737 documentation ranges, so they can never carry
# traffic — probing them is expected to report `unavailable`.
set -uo pipefail

S="${STUDIO_API:-http://127.0.0.1:8080}/api/v1"
CTRL="${MIHOMO_CONTROLLER:-http://127.0.0.1:9090}"
ENV_FILE="${ENV_FILE:-$HOME/mihomo-studio/app/.env}"
# Override to match the network the operator actually wants to route.
TEST_CIDR="${TEST_CIDR:-10.0.0.0/8}"

if [[ ! -f "$ENV_FILE" ]]; then
  printf 'missing env file %s; set ENV_FILE to the deployment .env\n' "$ENV_FILE" >&2
  exit 1
fi
SECRET=$(grep '^MIHOMO_SECRET=' "$ENV_FILE" | cut -d= -f2-)
if [[ -z "${SECRET:-}" ]]; then
  printf 'MIHOMO_SECRET is empty in %s; the panel would run unauthenticated and these calls would fail with 401\n' "$ENV_FILE" >&2
  exit 1
fi
# The panel and the controller share MIHOMO_SECRET, so every management call must present it.
AUTH_HEADER=(-H "X-Admin-Token: $SECRET")

section() { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }
pass()    { printf '  \033[32mPASS\033[0m  %s\n' "$*"; }
fail()    { printf '  \033[31mFAIL\033[0m  %s\n' "$*"; FAILED=1; }
info()    { printf '        %s\n' "$*"; }
FAILED=0

jq_() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)" 2>/dev/null; }
api() { curl -s -X "$1" "$S$2" "${AUTH_HEADER[@]}" -H 'Content-Type: application/json' ${3:+-d "$3"}; }

section "1. Publish a real subscription fixture to a public host"
FIXTURE=$(cat <<'YAML'
proxies:
  - name: 香港 IEPL 01
    type: ss
    server: 198.51.100.11
    port: 8388
    cipher: aes-128-gcm
    password: fixture-pass-1
  - name: 日本东京 01
    type: ss
    server: 198.51.100.12
    port: 8388
    cipher: aes-128-gcm
    password: fixture-pass-2
  - name: 美国洛杉矶 01
    type: ss
    server: 198.51.100.13
    port: 8388
    cipher: aes-128-gcm
    password: fixture-pass-3
YAML
)
SUBURL=$(curl -s -m 15 -X POST https://paste.rs --data-binary "$FIXTURE" 2>&1)
if [[ "$SUBURL" != https://* ]]; then fail "could not publish fixture: $SUBURL"; exit 1; fi
pass "fixture published at $SUBURL"
info "verify it is served: HTTP $(curl -s -o /dev/null -m 10 -w '%{http_code}' "$SUBURL")"

section "2. Clean slate"
# Order matters: groups are referenced by terminals and rules, and deletion is refused
# while references exist. Rules and terminals must go first.
del_all() {
  local path="$1" label="$2"
  curl -s -m 15 "${AUTH_HEADER[@]}" "$S$path" | python3 -c "
import sys, json
for item in json.load(sys.stdin):
    print(item['id'])
" | while read -r id; do
    [[ -z "$id" ]] && continue
    api DELETE "$path/$id" >/dev/null
    info "deleted old $label $id"
  done
}
del_all /rules rule
del_all /terminals terminal
del_all /proxy-groups group
del_all /subscriptions subscription
info "remaining groups: $(api GET /proxy-groups)"

section "3. Create subscription and refresh (real fetch + snapshot)"
SUB=$(api POST /subscriptions "{\"name\":\"E2E 测试源\",\"url\":\"$SUBURL\",\"enabled\":true}")
SUBID=$(echo "$SUB" | jq_ "['id']")
info "subscription id: $SUBID"
REF=$(api POST "/subscriptions/$SUBID/refresh" '{}')
echo "  $REF"
# Go emits compact JSON, so match on the parsed value rather than a spaced literal.
REFSTATUS=$(echo "$REF" | jq_ "['status']")
[[ "$REFSTATUS" == "current" ]] && pass "refresh promoted snapshot (proxyCount=$(echo "$REF" | jq_ "['proxyCount']"))" || fail "refresh failed: $REF"
NODES=$(api GET /nodes)
COUNT=$(echo "$NODES" | python3 -c "import sys,json;print(len(json.load(sys.stdin)))")
info "nodes in inventory: $COUNT"
[[ "$COUNT" == "3" ]] && pass "node inventory has 3 nodes" || fail "expected 3 nodes, got $COUNT"

section "4. Verify stable node IDs are subscription-scoped"
echo "$NODES" | python3 -c "
import sys,json
for n in json.load(sys.stdin):
    print('  %-28s id=%s sub=%s region=%s status=%s' % (n['name'], n['id'], n['subscription'], n.get('region','-'), n['status']))
"
NODEIDS=$(echo "$NODES" | python3 -c "import sys,json;print(' '.join(n['id'] for n in json.load(sys.stdin)))")
FIRSTID=$(echo "$NODEIDS" | awk '{print $1}')

section "5. Create proxy group referencing stable node IDs"
GRP=$(api POST /proxy-groups "{\"name\":\"E2E-自动选择\",\"mihomoType\":\"url-test\",\"selectionMode\":\"manual\",\"memberNodeIds\":[\"$FIRSTID\"],\"memberNodeNames\":[],\"memberFilter\":{},\"healthUrl\":\"https://www.gstatic.com/generate_204\",\"intervalSeconds\":300,\"toleranceMs\":50}")
GRPID=$(echo "$GRP" | jq_ "['id']")
info "group id: $GRPID"
[[ -n "$GRPID" ]] && pass "proxy group created" || fail "proxy group creation failed"

section "6. Create terminal bound to that group"
TERM=$(api POST /terminals "{\"name\":\"E2E 终端\",\"identityType\":\"cidr\",\"identityValue\":\"$TEST_CIDR\",\"proxyGroupId\":\"$GRPID\",\"enabled\":true,\"notes\":\"e2e\"}")
TERMID=$(echo "$TERM" | jq_ "['id']")
[[ -n "$TERMID" ]] && pass "terminal created (cidr -> group)" || fail "terminal creation failed: $TERM"

section "7. Create a rule targeting the group ID"
RULE=$(api POST /rules "{\"name\":\"E2E 规则\",\"ruleType\":\"DOMAIN-SUFFIX\",\"ruleValue\":\"openai.com\",\"target\":\"$GRPID\",\"priority\":100,\"source\":\"custom\",\"enabled\":true}")
RULEID=$(echo "$RULE" | jq_ "['id']")
[[ -n "$RULEID" ]] && pass "rule created targeting group id" || fail "rule creation failed: $RULE"

section "8. Build candidate (real mihomo -t validation)"
CAND=$(api POST /config-candidates '{}')
echo "  $CAND"
CANDID=$(echo "$CAND" | jq_ "['id']")
CANDSTATUS=$(echo "$CAND" | jq_ "['status']")
info "candidate id=$CANDID status=$CANDSTATUS"
if [[ "$CANDSTATUS" == "validated" ]]; then
  pass "candidate validated by the Mihomo binary"
else
  fail "candidate not validated: $(echo "$CAND" | jq_ "['validationMessage']")"
fi

section "9. Inspect generated YAML"
podman exec app-mihomo-studio-1 sh -c "cat /data/snapshots/$CANDID.yaml" 2>&1 | sed 's/^/    /'

section "10. Publish requires explicit confirmation"
NOCONFIRM=$(api POST "/config-candidates/$CANDID/publish" '{"confirm":false}')
info "publish without confirm -> $NOCONFIRM"
echo "$NOCONFIRM" | grep -qi 'confirmation is required' && pass "publish refused without confirm" || fail "publish was not gated"

section "11. Publish with confirmation (real reload)"
DEP=$(api POST "/config-candidates/$CANDID/publish" '{"confirm":true}')
echo "  $DEP"
DEPID=$(echo "$DEP" | jq_ "['id']")
DEPSTATUS=$(echo "$DEP" | jq_ "['status']")
info "deployment id=$DEPID status=$DEPSTATUS"
[[ "$DEPSTATUS" == "verified" ]] && pass "deployment verified against live runtime" || fail "deployment status $DEPSTATUS"

section "12. Confirm Mihomo actually loaded the candidate"
ACTIVE_HASH_STUDIO=$(podman exec app-mihomo-studio-1 sha256sum /runtime/active/config.yaml | awk '{print $1}')
CAND_HASH=$(echo "$CAND" | jq_ "['sha256']")
info "active file hash:  $ACTIVE_HASH_STUDIO"
info "candidate  sha256: $CAND_HASH"
[[ "$ACTIVE_HASH_STUDIO" == "$CAND_HASH" ]] && pass "active config file matches candidate hash" || fail "active file hash mismatch"
LIVE=$(curl -s -H "Authorization: Bearer $SECRET" "$CTRL/proxies")
echo "$LIVE" | python3 -c "
import sys,json
d=json.load(sys.stdin)['proxies']
g=d.get('E2E-自动选择')
print('  live group:', json.dumps(g, ensure_ascii=False)[:300] if g else 'MISSING')
names=[n for n in d if '香港' in n or '日本' in n or '美国' in n]
print('  live nodes:', names)
import sys as s
s.exit(0 if g else 1)
" && pass "runtime exposes the new group" || fail "runtime does not expose the group"

section "13. Runtime proxy-group verification endpoint"
RC=$(api GET /proxy-groups/runtime-check)
echo "$RC" | python3 -c "
import sys,json
d=json.load(sys.stdin)
print('  configuredGroups=%s runtimeGroups=%s typeMatched=%s' % (d['configuredGroups'],d['runtimeGroups'],d['typeMatched']))
for g in d['groups']:
    print('   - %s: status=%s type=%s current=%s members=%d' % (g['name'],g['status'],g.get('runtimeType'),g.get('current'),len(g.get('members') or [])))
" 2>&1

section "14. Node probing against live runtime"
PROBE=$(api POST /nodes/probe '{}')
echo "$PROBE" | python3 -c "
import sys,json
d=json.load(sys.stdin)
print('  total=%s available=%s unavailable=%s notLoaded=%s errors=%s' % (d['total'],d['available'],d['unavailable'],d['notLoaded'],d['errors']))
for r in d['results']:
    print('   - %s: %s %s' % (r['node']['name'], r['status'], r.get('message') or ''))
" 2>&1

section "15. Publish a second candidate and roll back"
# Mutate desired state so a distinct candidate exists, then roll back to the first.
api POST /rules "{\"name\":\"E2E 直连规则\",\"ruleType\":\"DOMAIN-SUFFIX\",\"ruleValue\":\"example.org\",\"target\":\"DIRECT\",\"priority\":50,\"source\":\"custom\",\"enabled\":true}" >/dev/null
CAND2=$(api POST /config-candidates '{}')
CAND2ID=$(echo "$CAND2" | jq_ "['id']")
CAND2STATUS=$(echo "$CAND2" | jq_ "['status']")
info "second candidate $CAND2ID status=$CAND2STATUS"
if [[ "$CAND2STATUS" == "validated" ]]; then
  DEP2=$(api POST "/config-candidates/$CAND2ID/publish" '{"confirm":true}')
  DEP2ID=$(echo "$DEP2" | jq_ "['id']")
  DEP2STATUS=$(echo "$DEP2" | jq_ "['status']")
  info "second deployment $DEP2ID status=$DEP2STATUS"
  [[ "$DEP2STATUS" == "verified" ]] && pass "second publish verified" || fail "second publish $DEP2STATUS"
  RB=$(api POST "/deployments/$DEP2ID/rollback" '{"confirm":true}')
  RBSTATUS=$(echo "$RB" | jq_ "['status']")
  info "rollback deployment status=$RBSTATUS"
  [[ "$RBSTATUS" == "verified" ]] && pass "rollback verified back to previous candidate" || fail "rollback $RBSTATUS"
  ROLLED=$(api GET "/config-candidates/$CAND2ID" | jq_ "['status']")
  info "second candidate status after rollback: $(api GET "/config-candidates/$CAND2ID" | jq_ "['status']")"
else
  fail "second candidate not validated"
fi

section "RESULT"
[[ "$FAILED" == "0" ]] && printf '\033[32mAll lifecycle checks passed.\033[0m\n' || printf '\033[31mSome checks failed.\033[0m\n'
exit "$FAILED"
