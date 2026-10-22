# Mihomo Studio

Mihomo Studio is a self-hosted control plane for one official MetaCubeX Mihomo instance. This repository is the first implementation slice, not a claim that every prototype interaction is already connected.

## Principles

The repository-level agent rules are in [`AGENTS.md`](AGENTS.md).

- **Design explains:** user-facing language explains what a setting changes and what its risks are.
- **Architecture explains:** package boundaries and persisted contracts express responsibilities.
- **Code explains:** explicit names, small functions, and tests carry intent without relying on comments.
- Existing product capabilities are additive: no approved prototype feature is removed silently.

## First implementation slice

- Go modular monolith and SQLite metadata.
- CRUD APIs for subscriptions, terminals, proxy groups, and global rules.
- Immutable configuration candidates identified by SHA-256.
- Candidate validation, explicit publish confirmation, runtime verification, and guarded rollback workflow.
- Docker Compose deployment with persistent database and shared configuration snapshots.

## Current boundaries

This slice deliberately does not implement subscription-provider-specific parsing, AI-provider risk scoring, IP rotation to evade provider limits, or production-grade auth. Do not expose the control API to an untrusted network. Before Internet-facing use, authentication and CSRF/origin controls must be completed.

## Docker Compose quick start

1. Copy `.env.example` to `.env`.
2. Generate a secret with `openssl rand -hex 32` and replace `MIHOMO_SECRET` in `.env`.
3. Keep `MIHOMO_BIND_IP=127.0.0.1` for local-only proxy access. Only set it to the host LAN IP if trusted terminal devices must connect.
4. Run `docker compose up -d --build`. The Studio UI binds to loopback; the Mihomo controller is published only on host loopback (`127.0.0.1:9090`) for the optional local dashboard and is not exposed to the LAN, while the proxy port is loopback-only unless `MIHOMO_BIND_IP` is changed.
5. Check service state with `docker compose ps` and `docker compose logs -f --tail=200`.
6. Open `http://127.0.0.1:8080` and sign in with `MIHOMO_SECRET`. Subscriptions, terminals, proxy groups, rules, node inventory, deployment history, overview metrics, and the generated YAML all come from the backend. Settings are read-only and show the values the running process actually uses. Runtime status is reported honestly: an unreachable controller shows as unknown, never as healthy.
7. Optional official runtime dashboard: `docker compose --profile diagnostics up -d`, then open `http://127.0.0.1:8081`. This is the way to browse the runtime; `http://<host>:9090/` is a JSON API that answers `401` to a browser, and `http://<host>:9090/ui/` answers `404` unless `external-ui` is configured. To serve the dashboard on a LAN address, set both `DASHBOARD_BIND_IP` and a matching `DASHBOARD_ORIGINS`, then publish a new candidate — see [docs/security.md](docs/security.md#port-9090-is-an-api-not-a-web-page).

## Rootless Podman deployment

`compose.yaml` stays canonical for Docker and CI. On a rootless Podman host, apply the additive overlay instead:

```sh
podman compose -f compose.yaml -f compose.podman.yaml up -d --build
```

The overlay only redirects base images and build arguments, which matters when Docker Hub or `proxy.golang.org` is unreachable. Set these in `.env` as needed:

| Variable | Purpose | Default |
|---|---|---|
| `GO_IMAGE` | Go build-stage image | `golang:1.23-alpine` |
| `RUNTIME_IMAGE` | final runtime image | `alpine:3.21` |
| `ALPINE_IMAGE` | bootstrap image | `alpine:3.21` |
| `GOPROXY` | module proxy used inside the build | `https://proxy.golang.org,direct` |

Note that `podman compose` drives the Docker daemon socket, so the Podman API socket must be running:

```sh
systemctl --user enable --now podman.socket
export DOCKER_HOST=unix:///run/user/1000/podman/podman.sock
```

Deployment helpers live in [`scripts/`](scripts): `vm-preflight.sh` checks the host before deploying, `vm-deploy.sh` builds and starts the stack, and `vm-lifecycle.sh` drives subscription, candidate, publish, verify, and rollback through the public API. They target a disposable test host and contain no credentials.

`MIHOMO_BIND_IP` matters more under rootless Podman: the default `127.0.0.1` binds the proxy listener to the container's own loopback, which the host cannot use. Set it to the host's LAN address only when trusted terminal devices must connect.

## Local development

```sh
go mod download
go test ./...
go run ./cmd/server
```

Requires Go 1.23+. `go.sum` is committed, so `go mod download` verifies module checksums without contacting a checksum service beyond the initial fetch. If `proxy.golang.org` is unreachable, set `GOPROXY` to a reachable mirror (for example `https://goproxy.cn,direct`); no dependency versions change.

Default listen address is `:8080`; database and snapshot locations can be set with `DATABASE_PATH` and `SNAPSHOT_DIR`. Create `.env` from `.env.example`; use `openssl rand -hex 32` for `MIHOMO_SECRET`. The web control plane binds to loopback by default. To permit terminal devices on a trusted LAN, set `MIHOMO_BIND_IP` to the host LAN IP and understand that source-IP routing trusts the LAN network.

## API

- `GET /healthz`
- `GET|POST /api/v1/subscriptions`
- `GET|PUT|DELETE /api/v1/subscriptions/{id}`
- `GET|POST /api/v1/terminals`
- `GET|PUT|DELETE /api/v1/terminals/{id}`
- `GET|POST /api/v1/proxy-groups`
- `GET|PUT|DELETE /api/v1/proxy-groups/{id}`
- `GET|POST /api/v1/rules`
- `GET|PUT|DELETE /api/v1/rules/{id}`
- `POST /api/v1/config-candidates` 鈥?build and validate an immutable candidate from the current desired configuration
- `GET /api/v1/config-candidates/{id}`
- `GET /api/v1/config-candidates/{id}/inputs`
- `POST /api/v1/config-candidates/{id}/publish` 鈥?requires `{"confirm":true}`
- `POST /api/v1/deployments/{id}/verify`
- `POST /api/v1/deployments/{id}/rollback` 鈥?requires `{"confirm":true}`

The root page serves the V6 interface with a partial API integration. Persisted CRUD, node inventory from saved subscription snapshots, and candidate/publish flows call the Go API; deployment history and settings are still not fully wired. Node latency probes now use the live Mihomo controller API and are only meaningful for nodes already loaded by the running configuration; subscription snapshot history includes node-level diffs, and runtime proxy-group checks inspect the controller-reported group types/current selections without injecting outages. Proxy-group membership now stores stable node IDs while retaining member-name fields for backward compatibility. The unmodified official MetaCubeX dashboard is available as an optional diagnostics profile at `http://127.0.0.1:8081` after `docker compose --profile diagnostics up -d`. The first iteration validates candidates with the Mihomo binary copied from the same pinned official image, promotes snapshots atomically, reloads through the controller API, and records runtime uncertainty rather than claiming success. Terminal routing is generated for source-IP/CIDR identities; MAC/manual identities remain inventory-only because Mihomo rules cannot enforce them. Subscription input accepts Mihomo-compatible YAML with a `proxies` list, a Base64-encoded proxy URI list, and a plain proxy URI list; `vmess`, `vless`, `ss`, and `trojan` schemes are parsed. See [docs/implementation-status.md](docs/implementation-status.md#subscription-format-support-increment) for the exact coverage, including which schemes have only unit-test coverage rather than live verification. 


节点矩阵通过 `GET /api/v1/nodes` 读取已持久化的订阅输入快照。订阅可单独刷新；节点延迟探测通过 Mihomo 控制器执行，只对已加载到运行配置中的节点返回真实样本。快照历史展示新增/删除节点与低置信度改名候选。

### Subscription refresh and snapshot behavior

The subscription list provides an explicit refresh action. A refresh downloads and validates a single enabled Mihomo-YAML source, persists an immutable content-addressed snapshot, and marks it as the current successful snapshot. If refresh fails, the previous successful snapshot remains current. Refreshing a subscription does not generate or publish a runtime configuration; publication remains a separate explicit operation.

#### Node-probe implementation note
Node probing addresses Mihomo with the exact source proxy name from the subscription snapshot. The UI label includes the subscription name and is not a valid controller proxy identifier. Full Go tests require the modules in `go.mod` to be downloadable or present in the local module cache.

### Automated verification

Run `bash scripts/verify.sh` in an environment with Go module network access, Node.js 22+, and Python 3. The script resolves dependencies (including generating/updating `go.sum`), runs all Go tests and `go vet`, checks Go/JavaScript syntax, and applies both copies of the SQLite migrations to in-memory databases.

The repository includes a GitHub Actions workflow at `.github/workflows/ci.yml` to run the verification suite on pushes and pull requests. In network-restricted environments, dependency resolution may fail before Go tests start; this is an environment limitation, not a passing test result. Use a network-enabled build runner or provide a complete Go module cache/vendor bundle for offline builds.

### Engineering checks

- `make compose-check` validates the resolved Compose model (requires Docker Compose v2).
- `make verify` runs Go tests/vet, frontend syntax checks, and SQLite migration checks (requires downloadable Go modules).
- `make smoke` builds and starts an isolated Compose project, waits for the Studio health endpoint, prints logs on failure, and removes only the smoke-test project's containers and volumes. Do not run it against valuable data; it uses a dedicated Compose project and disposable volumes.
