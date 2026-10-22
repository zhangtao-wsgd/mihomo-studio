#!/usr/bin/env bash
# Inspect the running Mihomo config for external-ui support and test dashboard image sources.
set -uo pipefail

echo "=== running config (secret redacted) ==="
podman exec app-mihomo-1 sh -c 'sed -e "s/^secret:.*/secret: <redacted>/" /runtime/active/config.yaml' 2>&1 | head -30

echo
echo "=== does the generated config mention external-ui at all? ==="
if podman exec app-mihomo-1 sh -c 'grep -q external-ui /runtime/active/config.yaml' 2>/dev/null; then
  echo "  yes"
else
  echo "  no: Mihomo has no UI directory configured, so /ui/ can only 404"
fi

echo
echo "=== what does the mihomo binary support for external-ui? ==="
podman run --rm --entrypoint /mihomo docker.io/metacubex/mihomo:v1.19.32 -h 2>&1 | grep -iE 'ui|external' || echo "  (no ui flags listed)"

echo
echo "=== dashboard image sources ==="
for image in \
  docker.1panel.live/metacubex/metacubexd:latest \
  ghcr.io/metacubex/metacubexd:latest \
  docker.io/metacubex/metacubexd:latest ; do
  printf '  %-48s ' "$image"
  if timeout 90 podman pull "$image" >/dev/null 2>&1; then
    echo "PULLED"
  else
    echo "unavailable"
  fi
done
