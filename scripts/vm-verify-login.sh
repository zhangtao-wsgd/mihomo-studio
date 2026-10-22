#!/usr/bin/env bash
# Verify the login gate wiring against the deployed stack.
set -uo pipefail
ENV_FILE="${ENV_FILE:-$HOME/mihomo-studio/app/.env}"
SECRET=$(grep '^MIHOMO_SECRET=' "$ENV_FILE" | cut -d= -f2-)
B="${STUDIO_URL:-http://192.168.152.130:8080}"

echo "=== served shell contains the login gate ==="
curl -s -m 10 "$B/" > "$HOME/index.html"
for id in loginGate loginForm loginPassword loginSubmit loginError logoutButton appRoot; do
  if grep -q "id=\"$id\"" "$HOME/index.html"; then printf '  %-14s present\n' "$id"; else printf '  %-14s MISSING\n' "$id"; fi
done
echo "  api.js referenced: $(grep -c '/api.js' "$HOME/index.html")"
echo "  api.js served    : HTTP $(curl -s -o /dev/null -m 10 -w '%{http_code}' "$B/api.js")"

echo
echo "=== /auth/verify gates the login form ==="
printf '  anonymous -> HTTP %s\n' "$(curl -s -o /dev/null -m 10 -w '%{http_code}' "$B/api/v1/auth/verify")"
printf '  wrong     -> HTTP %s\n' "$(curl -s -o /dev/null -m 10 -w '%{http_code}' -H 'X-Admin-Token: wrong' "$B/api/v1/auth/verify")"
printf '  correct   -> HTTP %s  body=%s\n' \
  "$(curl -s -o /dev/null -m 10 -w '%{http_code}' -H "X-Admin-Token: $SECRET" "$B/api/v1/auth/verify")" \
  "$(curl -s -m 10 -H "X-Admin-Token: $SECRET" "$B/api/v1/auth/verify")"

echo
echo "=== login gate must not block the shell or health ==="
printf '  /          -> HTTP %s\n' "$(curl -s -o /dev/null -m 10 -w '%{http_code}' "$B/")"
printf '  /healthz   -> HTTP %s\n' "$(curl -s -o /dev/null -m 10 -w '%{http_code}' "$B/healthz")"
printf '  /api.js    -> HTTP %s\n' "$(curl -s -o /dev/null -m 10 -w '%{http_code}' "$B/api.js")"

echo
echo "=== authenticated overview still works ==="
curl -s -m 20 -H "X-Admin-Token: $SECRET" "$B/api/v1/overview" > "$HOME/ov2.json"
python3 - "$HOME/ov2.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding='utf-8'))
print('  runtime:', json.dumps(d['runtime'], ensure_ascii=False))
print('  health : score=%s grade=%s' % (d['health']['score'], d['health']['grade']))
print('  auth   :', json.dumps(d['auth']))
PY
rm -f "$HOME/index.html" "$HOME/ov2.json"
