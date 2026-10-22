#!/usr/bin/env bash
# Deploy Mihomo Studio on a rootless Podman host and exercise the full lifecycle.
# Idempotent: safe to re-run. Removes only its own resources on --reset.
set -uo pipefail

DEPLOY_DIR="${DEPLOY_DIR:-$HOME/mihomo-studio}"
APP_DIR="$DEPLOY_DIR/app"

step() { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }
ok()   { printf '  PASS  %s\n' "$*"; }
bad()  { printf '  FAIL  %s\n' "$*"; }
info() { printf '        %s\n' "$*"; }

step "Environment"
info "podman:  $(podman --version)"
info "rootless: $(podman info --format '{{.Host.Security.Rootless}}')"
info "deploy:  $DEPLOY_DIR"

if [[ ! -f "$APP_DIR/.env" ]]; then
  bad "missing $APP_DIR/.env"
  exit 1
fi
set -a; . "$APP_DIR/.env"; set +a
info "secret len: ${#MIHOMO_SECRET}"

cd "$APP_DIR" || exit 1

step "Validate compose model"
if podman compose -f compose.yaml -f compose.podman.yaml config --quiet 2>&1; then
  ok "compose model resolves"
else
  bad "compose model invalid"
  exit 1
fi

step "Build Studio image"
if podman compose -f compose.yaml -f compose.podman.yaml build mihomo-studio 2>&1 | tail -20; then
  ok "image built"
else
  bad "build failed"
  exit 1
fi

step "Start stack"
podman compose -f compose.yaml -f compose.podman.yaml up -d 2>&1 | tail -20
sleep 8

step "Container state"
podman compose -f compose.yaml -f compose.podman.yaml ps 2>&1

step "Wait for Studio health"
health=""
for _ in $(seq 1 40); do
  health=$(curl -sf http://127.0.0.1:8080/healthz 2>/dev/null) && break
  sleep 2
done
if [[ -n "$health" ]]; then ok "healthz: $health"; else bad "healthz never responded"; podman compose -f compose.yaml -f compose.podman.yaml logs --no-color --tail=60 mihomo-studio; exit 1; fi

step "Wait for Mihomo controller"
ctrl=""
for _ in $(seq 1 30); do
  ctrl=$(curl -sf -H "Authorization: Bearer $MIHOMO_SECRET" http://127.0.0.1:9090/version 2>/dev/null) && break
  sleep 2
done
if [[ -n "$ctrl" ]]; then ok "controller: $ctrl"; else bad "controller never responded"; fi

step "Stack logs (studio)"
podman compose -f compose.yaml -f compose.podman.yaml logs --no-color --tail=25 mihomo-studio 2>&1
