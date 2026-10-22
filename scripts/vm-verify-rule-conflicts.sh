#!/usr/bin/env bash
# Verify the rule-order analysis against the deployed stack, using real rules that create a
# genuine shadowing relationship and then cleaning them up.
set -uo pipefail
ENV_FILE="${ENV_FILE:-$HOME/mihomo-studio/app/.env}"
SECRET=$(grep '^MIHOMO_SECRET=' "$ENV_FILE" | cut -d= -f2-)
B="${STUDIO_URL:-http://192.168.152.130:8080}/api/v1"
AUTH=(-H "X-Admin-Token: $SECRET" -H 'Content-Type: application/json')
WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT

api() { curl -s -m 60 -X "$1" "${AUTH[@]}" ${3:+-d "$3"} "$B$2"; }

echo "=== before: current analysis ==="
api GET /rules/conflicts > "$WORK/before.json"
python3 - "$WORK/before.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding='utf-8'))
print('  checked=%s conflicts=%s' % (d['checked'], len(d['conflicts'])))
for c in d['conflicts']:
    print('   [%s] %s -- %s' % (c['kind'], c['name'], c['detail'][:80]))
PY

echo
echo "=== create a deliberately shadowed rule pair ==="
# A broad suffix rule first, then an exact domain inside it: the second can never match.
R1=$(api POST /rules '{"name":"验证-宽规则","ruleType":"DOMAIN-SUFFIX","ruleValue":"shadow-test.example","target":"DIRECT","priority":10,"source":"custom","enabled":true}')
R1ID=$(echo "$R1" | python3 -c "import json,sys;print(json.load(sys.stdin).get('id',''))")
R2=$(api POST /rules '{"name":"验证-窄规则","ruleType":"DOMAIN","ruleValue":"api.shadow-test.example","target":"REJECT","priority":20,"source":"custom","enabled":true}')
R2ID=$(echo "$R2" | python3 -c "import json,sys;print(json.load(sys.stdin).get('id',''))")
echo "  created $R1ID (broad) and $R2ID (narrow)"

echo
echo "=== analysis should now report the narrow rule as unreachable ==="
api GET /rules/conflicts > "$WORK/after.json"
python3 - "$WORK/after.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding='utf-8'))
print('  checked=%s conflicts=%s' % (d['checked'], len(d['conflicts'])))
found = False
for c in d['conflicts']:
    print('   [%s] severity=%s' % (c['kind'], c['severity']))
    print('     %s' % c['detail'])
    if c['kind'] == 'shadowed' and '验证-窄规则' in c['name']:
        found = True
print('  narrow rule detected as shadowed:', found)
PY

echo
echo "=== overview health reflects it ==="
api GET /overview > "$WORK/ov.json"
python3 - "$WORK/ov.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding='utf-8'))
print('  score=%s grade=%s' % (d['health']['score'], d['health']['grade']))
for c in d['health']['checks']:
    if c['id'] == 'rule_conflicts':
        print('  rule_conflicts: severity=%s deduction=%s count=%s' % (c['severity'], c['deduction'], c['count']))
print('  overview.ruleConflicts count:', len(d['ruleConflicts']['conflicts']))
PY

echo
echo "=== clean up the verification rules ==="
curl -s -m 30 -X DELETE "${AUTH[@]}" "$B/rules/$R1ID" >/dev/null
curl -s -m 30 -X DELETE "${AUTH[@]}" "$B/rules/$R2ID" >/dev/null
api GET /rules/conflicts > "$WORK/clean.json"
python3 -c "import json,sys;d=json.load(open(sys.argv[1],encoding='utf-8'));print('  after cleanup: checked=%s conflicts=%s' % (d['checked'], len(d['conflicts'])))" "$WORK/clean.json"
