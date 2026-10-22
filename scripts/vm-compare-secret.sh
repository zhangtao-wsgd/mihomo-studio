#!/usr/bin/env bash
# Compare the secret the running container actually uses against the .env file on disk.
# A mismatch happens when .env is edited without recreating the container.
set -uo pipefail
ENV_FILE="${ENV_FILE:-$HOME/mihomo-studio/app/.env}"

FILE_SECRET=$(grep '^MIHOMO_SECRET=' "$ENV_FILE" | cut -d= -f2-)
CONT_SECRET=$(podman exec app-mihomo-studio-1 printenv MIHOMO_SECRET 2>/dev/null || echo '<unset>')

echo "=== length comparison ==="
printf '  .env on disk          : %s chars\n' "${#FILE_SECRET}"
printf '  container environment : %s chars\n' "${#CONT_SECRET}"

echo
echo "=== identical? ==="
if [ "$FILE_SECRET" = "$CONT_SECRET" ]; then
  echo "  YES: the container uses the secret in .env"
else
  echo "  NO: the container is running with a different secret."
  echo "      Recreate it after editing .env: podman compose up -d --force-recreate mihomo-studio"
fi

echo
echo "=== does the container secret authenticate? ==="
printf '  with container secret -> HTTP %s\n' "$(curl -s -o /dev/null -m 8 -w '%{http_code}' -H "X-Admin-Token: $CONT_SECRET" http://127.0.0.1:8080/api/v1/auth/verify)"
printf '  with .env secret      -> HTTP %s\n' "$(curl -s -o /dev/null -m 8 -w '%{http_code}' -H "X-Admin-Token: $FILE_SECRET" http://127.0.0.1:8080/api/v1/auth/verify)"

echo
echo "=== prefix comparison (first 8 characters) ==="
printf '  .env      : %s...\n' "${FILE_SECRET:0:8}"
printf '  container : %s...\n' "${CONT_SECRET:0:8}"
