# HTTP API (first implementation slice)

The control API is bound to `127.0.0.1:8080` by Compose.

## Authentication

When `MIHOMO_SECRET` is set, every `/api/v1/*` request except `/api/v1/auth/status` must
carry it in the `X-Admin-Token` header:

```sh
curl -H "X-Admin-Token: $MIHOMO_SECRET" http://127.0.0.1:8080/api/v1/rules
```

The same value protects the Mihomo controller, so there is one password to manage. It is
deliberately **not** accepted as a `?token=` query parameter, and comparison is constant
time. A wrong or missing credential returns `401` with a `WWW-Authenticate: Token` header.

`GET /api/v1/auth/status` reports whether a credential is required and which header to use.
It never returns the secret.

`GET /api/v1/auth/verify` is protected: a `200` means the presented password was accepted.
The login form uses it to validate before loading the console. It never echoes the secret.

There is no user or role model. One password protects the panel, and it is the same
`MIHOMO_SECRET` that protects the Mihomo controller.

These paths are always reachable without a credential because they carry no desired state:
`/healthz`, `/api.js`, `/`, `/prototype-v6.html`, and `/api/v1/auth/status`.

If `MIHOMO_SECRET` is empty, the panel starts only when `LISTEN_ADDR` is loopback; a
listener reachable from the network is refused at startup. See
[security.md](security.md#fail-closed-by-default).

## Resources

- `GET|POST /api/v1/subscriptions`
- `GET|PUT|DELETE /api/v1/subscriptions/{id}`
- `POST /api/v1/subscriptions/{id}/refresh` explicitly fetches and validates one enabled Mihomo-YAML subscription, then atomically marks its immutable input snapshot as current; this does not publish a runtime config.
- `GET /api/v1/subscriptions/{id}/snapshots` returns immutable snapshot history and node-level added/removed/unchanged diffs; possible renames are low-confidence hints only.
- `GET|POST /api/v1/terminals`
- `PUT|DELETE /api/v1/terminals/{id}`
- `GET|POST /api/v1/proxy-groups`
- `GET /api/v1/proxy-groups/runtime-check` compares the live controller group types and, when available, ordered members against the last verified candidate; it does not force an outage.
- `PUT|DELETE /api/v1/proxy-groups/{id}`
- `GET|POST /api/v1/rules`
- `PUT|DELETE /api/v1/rules/{id}`

IDs are stable opaque strings. Rule `target` is `DIRECT`, `REJECT`, or a proxy-group ID閳ユ攺ot a display name. Terminal `proxyGroupId` is a proxy-group ID.

## Configuration lifecycle

- `POST /api/v1/config-candidates` fetches the current enabled subscriptions, builds a candidate, persists source snapshots, and runs Mihomo config validation.
- `GET /api/v1/config-candidates/{id}` reads candidate metadata.
- `GET /api/v1/config-candidates/{id}/inputs` lists the immutable subscription input snapshots and hashes used to build that candidate.
- `POST /api/v1/config-candidates/{id}/publish` with `{"confirm":true}` starts an explicitly confirmed deployment.
- `GET /api/v1/deployments` returns paged history, newest first. Query parameters: `limit` (1-100, default 20), `offset`, `status`, `operation`, `candidateId`. Response shape is `{"items":[...],"total":N,"limit":N,"offset":N}`. An invalid parameter returns `400` rather than an empty page.
- `GET /api/v1/deployments/{id}` returns one deployment joined with its candidate `sha256`.
- `GET /api/v1/deployments/{id}/events` returns that deployment's audit trail, oldest first. An unknown deployment returns `404`, not an empty list.
- `POST /api/v1/deployments/{id}/verify` explicitly retries the exact active candidate reload when a prior result is uncertain.
- `POST /api/v1/deployments/{id}/rollback` with `{"confirm":true}` rolls back a verified deployment to its recorded previous verified candidate. A rollback appends a new deployment record; the original is retained with status `rolled_back`.
- `GET /api/v1/audit-events` returns recent audit events across all resources, newest first, with `limit` and `offset`.
- `GET /api/v1/overview` returns the dashboard payload in one consistent read: `runtime`, `activeDeployment`, `deployments` (recent 5), `nodes` (counts by knowledge state), `counts` (desired-state inventory), `subscriptions` (name and status only, never the URL), `health`, `config` (paths actually in use), and `auth`.
- `GET /api/v1/runtime` returns `configured`, `reachable`, `version`, `checkedAt`, and `error`. An unreachable controller is reported as unreachable, not as down: the running state is then unknown.

### Health score

`/overview` includes a derived `health` object with `score`, `grade`, `blockers`, `warnings`, and a `checks` array. Every deduction corresponds to one entry in `checks` carrying its own `severity`, `title`, `detail`, `count`, and `deduction`, so the number can always be explained from the response alone.

Grades are `healthy` (100), `attention` (70-99), `degraded` (<70), and `blocked` whenever a blocking check fires; a blocker overrides the numeric score.

Node reachability is **not** part of the score. A node pointing at a private or documentation address reports `unavailable` while the configuration is valid, so availability is reported as counts instead of affecting the verdict.

`previousCandidateId` is only populated once a deployment reaches `verified`, because it is
read from the last active candidate, which is recorded only after the runtime confirms the
reload. A deployment that was never verified therefore has no rollback target.

Candidate build returns a `validation_failed` candidate when Mihomo rejects the YAML, with diagnostic text in `validationMessage`. Only `validated` or already `published` candidates can be published.

## Error semantics

- `400`: malformed input or invalid fields.
- `404`: resource not found.
- `409`: a conflicting operation or reference prevents the request.
- `422`: candidate construction could not complete from current inputs.
- `202`: deployment is accepted or has a persisted non-final/uncertain outcome.

An HTTP request timeout during runtime reload is recorded as `unknown`, not treated as definite failure. Inspect the deployment and explicitly verify before another action.

## Minimal setup examples

Create a subscription. The URL may serve any of three payload shapes, detected automatically:

| Payload | Example provider form |
|---|---|
| Mihomo YAML with a `proxies` list | `proxies:\n  - name: HK-01\n    type: ss\n    ...` |
| Base64 of a newline-separated URI list | `dm1lc3M6Ly9leUoySWpvaU1pSXNJ...` |
| Plain newline-separated URI list | `vmess://eyJ2IjoiMiIsInBzIjoi...` |

Recognized URI schemes are `vmess`, `vless`, `ss`, and `trojan`. An unparseable payload is
rejected with an error naming the reason and sets `lastStatus` to `invalid_format`; an empty
`proxies` list is an error, not a silent success. Individual unparseable entries within an
otherwise valid list are counted and reported rather than dropped silently.

```json
{"name":"Primary","url":"https://example.invalid/subscription","enabled":true}
```

The subscription host must resolve to a non-private address. `198.18.0.0/15` is treated as
acceptable because real providers publish it, but every other private, loopback,
link-local, and documentation range is rejected. See
[security.md](security.md#ssrf-guard).

Create a fallback group; the returned `id` is the stable reference for terminal and rule targets:

```json
{"name":"OpenAI","mihomoType":"fallback","selectionMode":"manual","memberNodeNames":["LA-01","LA-02"],"memberFilter":{},"healthUrl":"https://www.gstatic.com/generate_204","intervalSeconds":300,"toleranceMs":50}
```

Create a rule using the proxy-group ID returned by the previous request:

```json
{"name":"OpenAI domains","ruleType":"DOMAIN-SUFFIX","ruleValue":"openai.com","target":"<proxy-group-id>","priority":100,"source":"custom","enabled":true}
```

For dynamic group membership, `memberFilter` supports `subscription`, `region`, `keyword`, and `regex`. `subscription` matches the subscription name prefix, while `region` and `keyword` are case-insensitive substrings of the rendered node name. `maxLatencyMs` is rejected until reliable runtime latency samples are available.

### 閼哄倻鍋ｅ〒鍛礋

`GET /api/v1/nodes` 娴?`subscription_snapshots` 娑擃厽鐦℃稉顏囶吂闂冨懎鏁稉鈧弽鍥唶娑?current 閻ㄥ嫭鍨氶崝鐔锋彥閻撗嗩嚢閸?`proxies`閿涘矁绻戦崶鐐跺Ν閻愬湱菙鐎规碍娣抽悽?ID閵嗕礁甯慨瀣Ν閻愮懓鎮曢妴浣硅閺屾挸鎮曢妴浣筋吂闂冨懏娼靛┃鎰┾偓浣藉Ν閻愬湱琚崹瀣嫲閸╄桨绨崥宥囆為惃鍕勾閸栫儤甯归弬顓溾偓鍌濆Ν閻?ID 閻㈣精顓归梼?ID 娑撳骸甯慨瀣Ν閻愮懓鎮曢悽鐔稿灇閿涙稑鎮撻崥宥堝Ν閻愯娼甸懛顏冪瑝閸氬矁顓归梼鍛娴犲秳绻氶幐浣风瑝閸?ID閵嗗倸婀撮崠鍝勭摟濞堥潧褰ч弰顖欑缁夊秴鎮曠粔鏉挎儙閸欐垵绱￠幒銊︽焽閿涘畭regionInferred` 閻劋绨弰搴ｂ€橀弽鍥槕閸忓爼娼弶鍐ㄢ枆閹佲偓鍌濆Ν閻愯甯板ù瀣波閺嬫粓鈧俺绻?`node_probe_results` 閹镐椒绠欓崠鏍电幢閹恒垺绁寸紒鎾寸亯閸欘亙鍞悰銊﹀赴濞村妞傛潻鎰攽娑擃厾娈?Mihomo 闁板秶鐤嗛敍灞肩瑝閼冲€熺槈閺勫骸鐨婚張顏勫絺鐢啰娈戠拋銏ゆ閼哄倻鍋ｉ崣顖滄暏閵?
濮濄倖甯撮崣锝勭瑝娴兼矮瀵岄崝銊ュ煕閺傛媽顓归梼鍛偓淇橮OST /api/v1/nodes/probe` 娴兼岸鈧俺绻?Mihomo controller 鐎电懓缍嬮崜宥堢箥鐞涘矂鍘ょ純顔昏厬閻ㄥ嫯濡悙鐟板絺鐠ч婀＄€圭偛娆㈡潻鐔稿赴濞村绱遍張顏勫鏉炵晫娈戦懞鍌滃仯閺嶅洩顔囨稉?`not_loaded`閿涘本甯堕崚璺烘珤閺堫亪鍘ょ純顔芥鏉╂柨娲?503閵嗗倹鐥呴張澶嬪瘮娑斿懎瀵茶箛顐ゅ弾閻ㄥ嫯顓归梼鍛瑝娴兼矮楠囬悽鐔诲Ν閻愮顔囪ぐ鏇幢鐏忔碍婀幒銏＄ゴ閻?`latencyMs` 娑?`null`閵嗕梗status=unmeasured`閿涘奔绗夐懗鑺ュΩ閼哄倻鍋ｇ憴鍡曡礋閸欘垳鏁ら幋鏍︾瑝閸欘垳鏁ら妴?

### Proxy group node references
`memberNodeIds` is the stable member reference used by new clients. `memberNodeNames` remains for compatibility with existing records and older clients. When both are present, candidate building uses `memberNodeIds` for manual membership. If a referenced node is missing from the candidate built from enabled subscriptions, validation fails and asks the operator to review membership.


### Subscription snapshot semantics

- `subscription_snapshots` stores immutable content hash/path, fetch time, proxy count, and a per-subscription current marker.
- `node_probe_results` stores the latest controller-based delay probe for each stable node ID.
- A refresh promotes a snapshot only after download and Mihomo-YAML validation succeed. Failed refreshes update the subscription status but preserve the prior current snapshot.
- Candidate construction also promotes each successfully fetched and validated source snapshot. A later candidate-build failure does not roll back successful source fetches: the current snapshot represents the latest successful subscription fetch, not the latest successfully published runtime configuration.
- Refreshing a source only updates the node inventory and input snapshot. It never builds or publishes a candidate automatically.
