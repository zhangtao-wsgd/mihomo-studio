# Implementation status

## Implemented in this first code slice

- [x] Go modular-monolith skeleton and structured HTTP logging.
- [x] SQLite schema and startup recovery of interrupted publish attempts.
- [x] CRUD endpoints for subscriptions, terminals, proxy groups, and global rules.
- [x] Stable ID references from terminals/rules to proxy groups.
- [x] Manual node membership and dynamic membership filters by subscription, region/name substring, keyword, and regex.
- [x] Deterministic dynamic membership order, with explicitly listed preferred nodes first.
- [x] Source-IP/CIDR terminal routing rules.
- [x] Bounded subscription downloads and private/reserved-address SSRF protection.
- [x] Immutable raw subscription snapshots, candidate snapshots, SHA-256 identities, and source fingerprint.
- [x] Mihomo binary config validation.
- [x] Explicit publish, runtime reload, uncertainty recording, audit event, and guarded rollback.
- [x] Docker Compose bootstrap config and loopback-first binding.
- [x] V6 prototype included and available at `/prototype-v6.html`.
- [x] Official MetaCubeX dashboard image pinned and included as an optional, unmodified diagnostics profile.

## Not yet implemented

- [x] First API-backed UI slice: subscriptions, terminals, proxy groups, and global rules load from and save to the backend; candidate creation and explicit publish call the real API. UI clearly marks static dashboard metrics, node inventory, deployment history, and settings as prototype-only.
- [ ] Full UI integration remains incomplete: node latency data, version history, and settings are not yet fully wired. Subscription refresh and node inventory are now API-backed; runtime verification/recovery controls remain partial.
- [x] Explicit per-subscription refresh for Mihomo YAML with immutable snapshot history and a last-known-good current snapshot. Subscription-provider formats beyond Mihomo YAML are unsupported. Subscription URL encryption at rest is not yet implemented; keep the service local-only.
- [ ] Runtime latency sampling and latency-threshold dynamic membership. Node selection table uses the current successful snapshot; candidate validation remains the authoritative guard against missing node references.
- [ ] Rule conflict analysis for overlapping CIDRs and unreachable/overshadowed rules.
- [ ] Authenticated remote access, user/role management, CSRF token strategy, and secret encryption at rest.
- [ ] End-to-end Docker build and live Mihomo controller tests in this environment.
- [ ] Full UI interaction tests and browser-level acceptance tests.

## Verification performed in the authoring environment

- Go formatting/parser checks passed.
- Type-check and helper tests passed using temporary local API-compatible stubs because this environment cannot resolve `proxy.golang.org`; this is not a substitute for running against the real dependencies.
- Both SQL migration copies were executed successfully by Python SQLite 3.46.1.
- Compose YAML parsed successfully with PyYAML.
- Docker is not installed in this environment, so the actual image build and container startup remain unverified.

### 本轮增量：真实节点快照清单

- 新增 `GET /api/v1/nodes`，读取每个订阅最近保存的 YAML 输入快照，并返回节点派生 ID、原始名/显示名、来源订阅、类型及启发式地区标签。
- 节点矩阵展示 API 返回的快照节点；已接入 Mihomo 控制器真实延迟探测与持久化结果。未探测、探测异常、未加载与可用状态分别展示，不使用演示数字冒充真实结果。
- 仍未完成：订阅主动刷新任务、节点真实探测、节点矩阵筛选器联动、代理组编辑器节点选择完全切换为真实节点 API，已在后续增量中完成：代理组编辑器读取真实节点 API，并以稳定 node ID 保存新成员引用；旧名称字段保留兼容。


## 本轮增量：真实节点与稳定成员引用
- `GET /api/v1/nodes` 返回快照节点及稳定 node ID；ID 由订阅 ID 和源节点名确定。
- 代理组成员新增 `memberNodeIds` 持久化字段，采用 SQLite migration 002；旧 `memberNodeNames` 保留以兼容已有配置。
- 代理组编辑器和节点矩阵使用真实节点快照，地区为名称推断，延迟/可用性明确显示未测。
- 构建候选时优先按 node ID 解析成员；引用的节点已从当前候选消失时，候选构建失败并提示复核，而不是静默替换。
- 后续增量已实现订阅主动刷新、实时探测与基于真实样本的延迟阈值筛选；候选构建也会记录成功获取的输入快照。


- 当前代理组编辑器延迟阈值控件处于禁用状态，因为尚无真实探测数据；不以假延迟参与筛选。节点清单仅列出已启用订阅的最新快照。


### Increment: subscription snapshot lifecycle
- Added migration 003 for `subscription_snapshots`, retaining immutable input history and a single current-successful marker per subscription.
- Added `POST /api/v1/subscriptions/{id}/refresh`; it explicitly fetches one enabled source, validates the Mihomo YAML `proxies` list, persists content-addressed bytes, then promotes the snapshot. Failed refreshes preserve the previous current snapshot.
- Candidate builds now record successful input snapshots as current as well. `GET /api/v1/nodes` reads only the current pointer from SQLite rather than guessing the newest file by filesystem modification time.
- Subscription UI now offers an explicit “立即刷新” action; success message clarifies that refresh does not publish runtime configuration.
- Added `GET /api/v1/subscriptions/{id}/snapshots` and a snapshot-history UI action with added/removed/unchanged node summaries. Same-type one-to-one changes may be shown as low-confidence rename hints; references are never rewritten automatically.
- Added migration 004 for persisted node probe results. Live node delay probes use Mihomo controller `/proxies/{name}/delay`, with explicit `not_loaded` status for nodes absent from the running configuration.
- Added a read-only runtime proxy-group check comparing saved group type with Mihomo `/proxies` data and showing the selected member and live member list for `fallback` / `url-test`. This does not simulate a node outage; controlled failover fault-injection and full end-to-end verification remain unimplemented.

### V6 follow-up hardening
- Fixed node probe URL construction to use the actual source proxy name (`Node.SourceName`) rather than the UI label (`Node.Name`, which includes the subscription prefix). This prevents false `not_loaded` results caused by querying a proxy name that does not exist in Mihomo.
- Extracted probe endpoint construction into a helper and added a regression test for path escaping and probe query parameters.
- Validation remains limited by unavailable Go module downloads in this environment; `go test ./...` must be rerun in an environment with dependencies available.


## Container engineering increment

- Added `.dockerignore`, Studio HTTP health check, Mihomo process health check, a Compose validation target, and an isolated disposable smoke-test script.
- CI now validates the resolved Compose model and attempts a real build/start/health smoke test on GitHub-hosted Linux runners.
- Security boundary: the Studio UI is loopback-bound by default; the Mihomo controller port is published only on host loopback (`127.0.0.1:9090`), never on a LAN interface; proxy ingress is loopback-bound unless the operator deliberately configures a trusted LAN IP.
- This environment does not have Docker CLI/daemon, so Compose image build and runtime smoke test have not been executed locally. Go full-suite status remains dependent on downloading modules.

## Build and first-run correctness increment

All three items below were blocking defects, not feature work. Each was reproduced before the fix and re-verified after it.

- **Compile blocker fixed.** `internal/config/nodes.go` imported `path/filepath` without using it, so `go build ./...` failed with `imported and not used`. Removed the import. `go build ./...` and `go vet ./...` now exit 0.
- **First-run UI failure fixed.** `Builder.Nodes` declared `var nodes []Node`, so an empty inventory serialized as JSON `null` instead of `[]`. The browser client calls `state.nodes.map(...)` inside `refreshAll`, and the resulting `TypeError` was swallowed by the catch block, which then reported "控制平面未连接" against a perfectly healthy backend on a fresh database. Changed to `nodes := []Node{}` and added `TestNodesReturnsEmptySliceNotNullForEmptyInventory`, which opens a real migrated SQLite database and asserts the encoded response is exactly `[]`.
- **Dependency integrity.** Added `go.sum` (47 entries) so module checksums are pinned and verified; it was previously absent, leaving the Docker build and CI to resolve unpinned checksums. No `go.mod` requirement versions changed.
- **Verification script portability.** `scripts/verify.sh` used `path.rsplit('/', 1)[-1]` to compare migration filenames. Under Windows, `glob.glob` returns backslash-separated paths, so the check aborted with `Unexpected migration set` even though the copies were identical. Replaced with `os.path.basename`.

### Verification actually run for this increment

Executed locally with Go 1.23.12 (portable toolchain outside the repository), Node 22.20.0, and Python 3.12.14:

- `go build ./...` — pass.
- `go vet ./...` — pass.
- `go test ./...` — pass (api, config, workflow; 10 tests including the new regression test).
- `gofmt -l .` — no files listed.
- `node --check web/api.js` — pass; the single inline `<script>` in `web/prototype-v6.html` also passed.
- `bash scripts/verify.sh` — full suite pass, end to end.
- Runtime smoke: built `cmd/server`, started it against a fresh database, and confirmed `GET /healthz` returns 200 and `GET /api/v1/nodes` returns `[]` (previously `null`).

### Still not verified in this environment

- Docker CLI/daemon is absent, so `docker compose config`, image build, and `scripts/docker-smoke.sh` were not run locally; CI remains the only place these execute.
- No live Mihomo controller was reachable, so publish, verify, rollback, node probing, and runtime group checks were not exercised against a real runtime. Their status is unchanged by this increment.

### Known defects found but deliberately not fixed in this increment

These were observed during review and are out of the approved scope (1–3). They are recorded so they are not lost:

- Editing an existing subscription resets its `lastStatus` to `never_fetched`, because `SaveSubscription` writes `last_status=excluded.last_status` on conflict while the UI does not send `lastStatus`.
- `web/api.js` reads `row.dataset.sourceName`, which no table row ever sets, so legacy short-name group members are not pre-selected in the group editor.
- Runtime parameters (`mixed-port`, `allow-lan`, `bind-address`, `log-level`, `external-controller`) are hard-coded in `internal/config/builder.go` and are not adjustable through any API.
- There is no `GET /api/v1/deployments` collection endpoint, so the UI cannot list deployment history; the release history panel remains prototype-only.
- The `## Not yet implemented` heading in this file still contains completed `[x]` items from earlier increments.

## Live deployment on rootless Podman

The stack was deployed for the first time on a real host and exercised against a real MetaCubeX Mihomo runtime. This closes the "no live Mihomo controller was reachable" gap recorded above.

### Environment actually used

- Ubuntu 26.04 LTS, x86_64, 4 vCPU / 7.2 GiB, rootless Podman 5.7.0 (invoking uid 1000), `podman compose` backed by the docker-compose 2.40.3 provider.
- Mihomo `v1.19.32` (same tag the Dockerfile pins). Docker Hub is **not** reachable from that network; base images were served from a mirror.

### Production code changes required by that environment

- **Dockerfile base images are now overridable** via `ARG GO_IMAGE`, `ARG RUNTIME_IMAGE`, and `ARG GOPROXY`. Defaults are unchanged, so Docker and CI behavior is identical; the arguments exist so an unreachable registry or module proxy can be substituted without editing the file. `COPY go.mod go.sum ./` now includes `go.sum`, so `go mod download` verifies checksums during the image build.
- **`compose.podman.yaml` added** as an additive overlay. It only redirects images/build args for the mirror and is applied with `-f compose.yaml -f compose.podman.yaml`. `compose.yaml` remains canonical for Docker and CI.
- Rootless uid mapping was measured, not assumed: container uid 0 maps to host uid 1000, and container uid 100 maps to host uid 100099. Because the runtime volume is a **named volume**, the `chown -R 100:100 /runtime` in bootstrap stays correct — the Studio container (uid 100) and the Mihomo container (root) both read it. This would break on a bind mount, where host-side files become owner `100099` and the invoking user can no longer remove them.

### Defects found only by running it

Three defects were invisible to unit tests and to code review, and were found by exercising the deployed stack:

1. **Permanent database deadlock in `Builder.Nodes`** (`internal/config/nodes.go`). The store pins `SetMaxOpenConns(1)`. `Nodes` executed a per-node `node_probe_results` lookup *while the outer snapshot `rows` iterator was still open*, so the second query waited forever for the single connection held by the first. Effect: `GET /api/v1/nodes` never returned, `POST /api/v1/nodes/probe` hung, and every subsequent API call queued behind it. Measured from the container log: `/api/v1/subscriptions` blocked for **180 s** and `/api/v1/nodes` for **10 s**. Fixed by draining and closing the outer iterator into a slice before any per-node lookup. Re-measured after the fix: **2.3 ms**.
   - Proven, not assumed: an isolated program reproduces the deadlock pattern against a one-connection pool and exits with "DEADLOCK REPRODUCED".
   - Guarded by `TestNodesDoesNotDeadlockOnSingleConnection`, which seeds a subscription, a snapshot, and a persisted probe result, then fails after 15 s if `Nodes` blocks.
2. **Node probing reported `not_loaded` for nodes the runtime had loaded** (`internal/config/history_probe.go`). Candidate composition rewrites every proxy to `"<subscription> / <source name>"` before writing the config, so Mihomo keys its proxies by that rendered name. Probing with `Node.SourceName` returned HTTP 404 `{"message":"Resource not found"}`, which the code classified as `not_loaded` — a false "your node is not deployed" signal for correctly deployed nodes. Probing with the rendered name returns a real delay/timeout. Fixed by routing the probe through `runtimeProxyName`, which returns the rendered name; `SourceName` is retained for snapshot provenance. Guarded by `TestRuntimeProxyNameUsesRenderedNameNotSourceName`.
   - This **contradicts the earlier "V6 follow-up hardening" entry above**, which claims switching to `SourceName` *fixed* false `not_loaded` results. That change was in fact the cause. That entry is left in place rather than rewritten, and this note supersedes it.
3. **`scripts/verify.sh` was not portable** to Windows hosts, detailed in the previous increment.

### End-to-end results against the live runtime

`scripts/vm-lifecycle.sh` drives the real HTTP API against the deployed stack. Every check passed:

- Subscription refresh fetched a real remote Mihomo-YAML document and promoted an immutable snapshot (`proxyCount=3`).
- SSRF guard rejected a subscription resolving to a private address with `subscription host resolves only to private or reserved addresses`, leaving `lastStatus=fetch_failed` and the previous successful snapshot intact.
- Candidate build ran the real `mihomo -t -f` binary and returned `status=validated`.
- Publish without `{"confirm":true}` was refused (`explicit confirmation is required`).
- Publish with confirmation returned `verified`; the on-disk `/runtime/active/config.yaml` SHA-256 matched the candidate `sha256` byte for byte.
- The live controller exposed the new group (`E2E-自动选择`, type `URLTest`) and all three proxies under their rendered names.
- `GET /api/v1/proxy-groups/runtime-check` reported `configuredGroups=1 typeMatched=1` with status `verified`.
- Node probing returned `notLoaded=0` for loaded nodes and honestly reported `unavailable` (HTTP 504) for the fixture nodes, which point at RFC 5737 documentation addresses and cannot be reached.
- A second candidate published and then rolled back to the recorded previous candidate, both `verified`.
- Proxy-group deletion correctly refused when terminals still referenced the group, and candidate build correctly refused with `references missing node ID ...; refresh subscriptions and review group members` when a group referenced a node absent from the current candidate.

### Still not verified

- `scripts/docker-smoke.sh` and `docker compose config` remain unexecuted with the real Docker engine; only the Podman/compose-provider path was exercised.
- The `diagnostics` profile (`metacubexd`) was not started.
- Rollback from an `unknown` (ambiguous reload) state was not induced; it needs a controllable controller failure to test.
- No traffic-level behaviour was verified. Publish verification confirms Mihomo accepted the config and the controller answers `/version`; it says nothing about whether proxied traffic flows, because the fixture nodes are unreachable by design. Do not read these results as proof of end-to-end traffic behaviour.

### Deployment helpers added

- `scripts/vm-preflight.sh` — checks rootless port binding, Mihomo image boot, controller auth, and `mihomo -t` before deploying.
- `scripts/vm-deploy.sh` — resolves the compose model, builds, starts, and waits for both the Studio health endpoint and the Mihomo controller.
- `scripts/vm-lifecycle.sh` — drives the full desired-state-to-verified-deployment lifecycle through the public API.

These target a disposable test host. They assume passwordless SSH is configured out of band and contain no credentials, secrets, or host-specific absolute paths.
