#!/usr/bin/env bash
# Pre-flight feasibility probe for Mihomo Studio on rootless Podman.
# Verifies: unprivileged port binding, Mihomo image boot, controller API reachability.
set -uo pipefail

STUDIO_IMAGE_BASE="docker.1panel.live/library/alpine:3.21"
MIHOMO_IMAGE="docker.io/metacubex/mihomo:v1.19.32"
WORKDIR="$(mktemp -d)"

cleanup() {
  podman rm -f mihomo-probe porttest >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

echo "=== 1. rootless unprivileged port binding ==="
if podman run --rm -d --name porttest -p 127.0.0.1:18080:8080 "$STUDIO_IMAGE_BASE" sleep 20 >/dev/null 2>&1; then
  if ss -ltn 2>/dev/null | grep -q '127.0.0.1:18080'; then
    echo "PASS: bound 127.0.0.1:18080 (rootless)"
  else
    echo "WARN: container started but 18080 not listening"
  fi
else
  echo "FAIL: could not bind 127.0.0.1:18080"
fi
podman rm -f porttest >/dev/null 2>&1 || true

echo
echo "=== 2. Mihomo boots with our generated config ==="
cat > "$WORKDIR/config.yaml" <<'YAML'
mixed-port: 17890
allow-lan: true
bind-address: "*"
mode: rule
log-level: info
external-controller: 0.0.0.0:19090
external-controller-cors:
  allow-origins:
    - http://127.0.0.1:8081
  allow-private-network: false
secret: "probe-secret-123"
proxies: []
proxy-groups: []
rules:
  - MATCH,DIRECT
YAML

podman run -d --name mihomo-probe \
  -v "$WORKDIR/config.yaml:/root/.config/mihomo/config.yaml:ro" \
  -p 127.0.0.1:19090:19090 \
  "$MIHOMO_IMAGE" >/dev/null 2>&1
if [ $? -ne 0 ]; then
  echo "FAIL: container did not start"
  exit 1
fi
echo "container started: $(podman ps --filter name=mihomo-probe --format '{{.Status}}')"

for i in $(seq 1 20); do
  if curl -sf -H "Authorization: Bearer probe-secret-123" http://127.0.0.1:19090/version >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

echo "--- /version ---"
curl -s -H "Authorization: Bearer probe-secret-123" http://127.0.0.1:19090/version || echo "(failed)"
echo
echo "--- /proxies (truncated) ---"
curl -s -H "Authorization: Bearer probe-secret-123" http://127.0.0.1:19090/proxies | head -c 400
echo
echo "--- auth rejected without secret? ---"
code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:19090/version)
echo "unauthenticated /version HTTP $code (401/403 expected)"

echo
echo "=== 3. Mihomo config test mode (used by candidate validation) ==="
podman run --rm -v "$WORKDIR/config.yaml:/test.yaml:ro" --entrypoint /mihomo "$MIHOMO_IMAGE" -t -f /test.yaml 2>&1 | tail -5
echo "config-test exit: $?"

echo
echo "=== 4. host can reach VM from outside? (LAN 8080 later) ==="
echo "host_ip=$(hostname -I | awk '{print $1}')"
