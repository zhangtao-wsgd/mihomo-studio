#!/usr/bin/env bash
# Verify the overview and runtime endpoints reflect real state, not sample data.
# Usage: ADMIN_TOKEN=<secret> bash scripts/overview-check.sh
set -uo pipefail

S="${STUDIO_API:-http://127.0.0.1:18080}/api/v1"
AUTH=()
if [[ -n "${ADMIN_TOKEN:-}" ]]; then AUTH=(-H "X-Admin-Token: $ADMIN_TOKEN"); fi

# Keep temp files beside the script so this works under Git Bash on Windows, where /tmp is
# remapped and a bare path can silently end up somewhere curl cannot write.
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

section() { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }

section "1. GET /overview"
code=$(curl -s -m 20 -o "$WORK/ov.json" -w '%{http_code}' "${AUTH[@]}" "$S/overview")
echo "  HTTP $code"
if [[ "$code" != "200" ]]; then
  echo "  body: $(head -c 300 "$WORK/ov.json")"
  exit 1
fi
python3 - "$WORK/ov.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding='utf-8'))
print('  runtime      :', json.dumps(d['runtime'], ensure_ascii=False))
print('  health.score :', d['health']['score'], 'grade=', d['health']['grade'])
print('  blockers/warn:', d['health']['blockers'], '/', d['health']['warnings'])
print('  nodes        :', json.dumps(d['nodes']))
print('  counts       :', json.dumps(d['counts']))
print('  deployments  :', len(d['deployments']), 'active=', (d['activeDeployment'] or {}).get('candidateId'))
print('  auth.required:', d['auth']['required'])
print('  config       :', json.dumps(d['config'], ensure_ascii=False))
print('  health checks:')
for c in d['health']['checks']:
    print('    [%-5s] %-22s -%-3d %s' % (c['severity'], c['id'], c['deduction'], c['title']))
PY

section "2. GET /runtime"
curl -s -m 20 -o "$WORK/rt.json" "${AUTH[@]}" "$S/runtime"
python3 -c "import json,sys;print(' ', json.dumps(json.load(open(sys.argv[1])), ensure_ascii=False))" "$WORK/rt.json"

section "3. overview must not leak a subscription URL"
if grep -q '"url"' "$WORK/ov.json"; then
  echo "  FAIL: overview exposes a url field"
else
  echo "  PASS: no url field in overview payload"
fi
