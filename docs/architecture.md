# Architecture

## Runtime topology

```text
Browser (loopback by default)
        |
        v
Go modular monolith ---- SQLite metadata
        |                 immutable input/config snapshots
        |
        +---- Mihomo Controller API (private Compose network)
        |
        v
Official MetaCubeX Mihomo container
```

The Go service owns desired state and deployment coordination. Mihomo remains the proxy runtime; this project does not fork or patch Mihomo. The official MetaCubeX dashboard is included unchanged as an optional diagnostics service, not as a replacement for the Mihomo Studio management workflow. Runtime reload uses the documented [`PUT /configs?force=true` controller API](https://wiki.metacubex.one/en/api/); the active config path is included in `SAFE_PATHS` because it lives outside Mihomo’s home directory.

## Module responsibilities

- `internal/api`: HTTP contracts, request validation, same-origin protection, response mapping.
- `internal/domain`: API/domain models.
- `internal/store`: SQLite persistence, constraints, and startup recovery of interrupted deployment records.
- `internal/config`: subscription input acquisition, config composition, semantic reference checks, immutable source/config snapshots, and validation with the Mihomo binary.
- `internal/workflow`: candidate promotion, runtime reload, explicit verification, audit recording, and guarded rollback.

## Candidate and deployment identity

- `sha256` identifies the exact generated configuration bytes.
- `candidate_id` also incorporates the input fingerprint, so equal output bytes generated from different input revisions remain distinguishable.
- `source_fingerprint` summarizes subscription content hashes plus terminal/group/rule desired state.
- `config_candidate_inputs` records each subscription ID, content SHA-256, and immutable raw input snapshot path.
- `deployments` records each attempt independently from candidate content.

## Publish lifecycle

1. Fetch enabled subscriptions with bounded HTTP timeouts and size limits.
2. Reject subscription hosts that resolve only to private/reserved addresses.
3. Store immutable raw subscription snapshots.
4. Compose a candidate and validate references.
5. Run the official Mihomo binary in config-test mode.
6. Persist the immutable candidate and its input manifest.
7. Require explicit publish confirmation.
8. Verify the snapshot hash, atomically promote the active file, and ask Mihomo to reload it.
9. Record `verified`, `failed`, `unknown`, or `published_unverified` truthfully. A transport timeout is not treated as definite failure.
10. If Mihomo definitively rejects a candidate, restore the previous active file; if restoration is uncertain, preserve the `unknown` state for explicit verification.
11. Roll back only from a verified deployment to a previously verified candidate.

A process restart while status is `publishing` converts that record to `unknown`; the service does not blindly retry a live reload.

## Terminal routing boundary

`source_ip` and `cidr` identities produce `SRC-IP-CIDR` rules ahead of global rules. More-specific rules should precede broader ranges; overlapping networks are ordered from most-specific to broadest, with warnings when overlapping ranges target different groups. MAC and manual identities are inventory-only because Mihomo rule matching cannot enforce those identity types directly. Terminal source-IP matching trusts the local network and is not an authentication mechanism.

## Deliberate first-slice limitations

- Subscription input supports Mihomo-compatible YAML with a `proxies` list. Base64 and URI subscription formats are not yet implemented.
- The V6 high-fidelity prototype is served at `/prototype-v6.html` as a reference; its interactions are not wired to the API yet.
- Latency-based dynamic node filtering needs a reliable runtime latency source; the API rejects that filter instead of pretending it works.
- The service binds to loopback in Compose by default. No Internet-facing deployment should be made before full authentication, authorization, and operational hardening are added.


## Container lifecycle and exposure

- `mihomo-bootstrap` initializes the shared runtime volume once and exits successfully; it does not run as a long-lived service.
- `mihomo-studio` is published on `127.0.0.1:8080` by default. Its container health check verifies the HTTP process endpoint, not the entire upstream control plane.
- `mihomo` publishes only the proxy port, bound to `MIHOMO_BIND_IP` (loopback by default). The controller remains reachable on the Compose network at `mihomo:9090`; its optional host mapping is bound strictly to `127.0.0.1:9090` for local diagnostics and is not exposed to the LAN.
- `app-data`, `mihomo-runtime`, and `mihomo-state` are separate named volumes. `docker compose down` preserves them; `docker compose down -v` deletes them and must be treated as destructive.
- `scripts/docker-smoke.sh` uses a separate Compose project name and removes its own volumes on exit. It must not be used with the production project name.

## Storage access and connection concurrency

- The SQLite handle is opened with `SetMaxOpenConns(1)`. Every query is therefore serialized on one connection, and **no code may issue a second statement while a `Rows` iterator is still open** — doing so blocks forever rather than merely contending. Read what you need into a slice, close the iterator, then query again. This constraint has already caused one production-blocking deadlock in `Builder.Nodes`.
- Cross-container file handoff relies on the shared `mihomo-runtime` named volume. Bootstrap runs as container root and `chown`s it to uid 100; the Studio image runs as uid 100 and writes the active config; Mihomo runs as root and reads it. Mihomo additionally requires `SAFE_PATHS` to include the active config path, because it refuses to load a config from outside its home directory otherwise.
- Under rootless Podman, container uid 0 maps to the invoking host user and container uid 100 maps to a high subuid (100099 in the observed environment). **Named volumes keep this consistent across containers; bind mounts do not** — a bind-mounted tree chowned inside the container becomes host-owned by an unmapped id that the invoking user cannot clean up. Keep runtime handoff on named volumes.

## Image provenance

- The Mihomo binary used for candidate validation is copied from the same image tag that serves traffic, so `mihomo -t` checks run against the exact runtime version.
- Base images, the Go module proxy, and the Mihomo image are all build arguments with production defaults. A deployment on a network that cannot reach Docker Hub or `proxy.golang.org` overrides them through `compose.podman.yaml` instead of editing `Dockerfile`, so the canonical definition stays single-sourced.
