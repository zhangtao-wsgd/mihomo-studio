#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

command -v docker >/dev/null 2>&1 || { echo "Docker CLI is required." >&2; exit 127; }
docker info >/dev/null 2>&1 || { echo "Docker daemon is not reachable." >&2; exit 1; }

if [[ ! -f .env ]]; then
  echo "Create .env from .env.example and set a random MIHOMO_SECRET first." >&2
  exit 1
fi
if grep -Eq '^MIHOMO_SECRET=(replace-with-a-long-random-secret)?$' .env; then
  echo "Replace the example MIHOMO_SECRET before starting." >&2
  exit 1
fi

project="mihomo-studio-smoke"
cleanup() { docker compose -p "$project" down --volumes --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker compose -p "$project" up -d --build
for _ in $(seq 1 40); do
  if docker compose -p "$project" exec -T mihomo-studio wget -q -O /dev/null http://127.0.0.1:8080/healthz; then
    echo "Studio health endpoint is responding."
    exit 0
  fi
  sleep 2
done

docker compose -p "$project" ps
docker compose -p "$project" logs --no-color --tail=200
exit 1
