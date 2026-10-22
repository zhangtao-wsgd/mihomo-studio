#!/usr/bin/env bash
# Validate a real subscription end-to-end against a locally running Studio.
# The subscription URL is read from the environment so it never lands in a file.
# Usage: SUB_URL='https://...' bash scripts/real-subscription-check.sh
set -uo pipefail

if [[ -z "${SUB_URL:-}" ]]; then
  echo "SUB_URL is required" >&2
  exit 1
fi

S="http://127.0.0.1:18080/api/v1"
OUT="${OUT:-/tmp/realsub}"
mkdir -p "$OUT"

section() { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }
jget() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)" 2>/dev/null; }

section "1. Create subscription"
SUB=$(curl -s -m 30 -X POST "$S/subscriptions" -H 'Content-Type: application/json' \
  -d "{\"name\":\"主力机场\",\"url\":\"$SUB_URL\",\"enabled\":true}")
echo "  $SUB"
SUBID=$(echo "$SUB" | jget "['id']")
[[ -z "$SUBID" ]] && { echo "subscription creation failed" >&2; exit 1; }

section "2. Refresh (real fetch + format parse)"
REF=$(curl -s -m 60 -X POST "$S/subscriptions/$SUBID/refresh" -H 'Content-Type: application/json' -d '{}')
echo "$REF" | head -c 400; echo
COUNT=$(echo "$REF" | jget "['proxyCount']")
echo "  parsed proxy count: ${COUNT:-none}"

section "3. Node inventory"
curl -s -m 30 "$S/nodes" | python3 -c '
import sys, json
nodes = json.load(sys.stdin)
print("  total nodes: %d" % len(nodes))
for n in nodes[:12]:
    print("   - %-34s type=%-8s region=%s id=%s" % (n["name"], n["type"], n.get("region") or "-", n["id"]))
if len(nodes) > 12:
    print("   ... and %d more" % (len(nodes) - 12))
'

section "4. Build candidate (real mihomo -t validation)"
CAND=$(curl -s -m 120 -X POST "$S/config-candidates" -H 'Content-Type: application/json' -d '{}')
echo "$CAND" | head -c 500; echo
STATUS=$(echo "$CAND" | jget "['status']")
CANDID=$(echo "$CAND" | jget "['id']")
echo "  status=$STATUS id=$CANDID"

if [[ "$STATUS" == "validation_failed" ]]; then
  echo "  --- validation message ---"
  echo "$CAND" | jget "['validationMessage']"
fi
