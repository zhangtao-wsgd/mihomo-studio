.PHONY: deps test verify run compose-check up down logs smoke

deps:
	go mod download

test: deps
	go test ./...

verify:
	bash scripts/verify.sh

run: deps
	go run ./cmd/server

compose-check:
	docker compose config --quiet

up:
	@test -f .env || (echo "Create .env from .env.example and set MIHOMO_SECRET first." && exit 1)
	@if grep -Eq '^MIHOMO_SECRET=(replace-with-a-long-random-secret)?$$' .env; then echo "Replace the example MIHOMO_SECRET before starting."; exit 1; fi
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f --tail=200

smoke:
	bash scripts/docker-smoke.sh
