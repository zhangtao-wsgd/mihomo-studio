# HTTP API (first implementation slice)

The control API is intended for local use and is bound to `127.0.0.1:8080` by Compose. Do not expose it to a network until authentication and authorization are implemented.

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

IDs are stable opaque strings. Rule `target` is `DIRECT`, `REJECT`, or a proxy-group ID—not a display name. Terminal `proxyGroupId` is a proxy-group ID.

## Configuration lifecycle

- `POST /api/v1/config-candidates` fetches the current enabled subscriptions, builds a candidate, persists source snapshots, and runs Mihomo config validation.
- `GET /api/v1/config-candidates/{id}` reads candidate metadata.
- `GET /api/v1/config-candidates/{id}/inputs` lists the immutable subscription input snapshots and hashes used to build that candidate.
- `POST /api/v1/config-candidates/{id}/publish` with `{"confirm":true}` starts an explicitly confirmed deployment.
- `POST /api/v1/deployments/{id}/verify` explicitly retries the exact active candidate reload when a prior result is uncertain.
- `POST /api/v1/deployments/{id}/rollback` with `{"confirm":true}` rolls back a verified deployment to its recorded previous verified candidate.

Candidate build returns a `validation_failed` candidate when Mihomo rejects the YAML, with diagnostic text in `validationMessage`. Only `validated` or already `published` candidates can be published.

## Error semantics

- `400`: malformed input or invalid fields.
- `404`: resource not found.
- `409`: a conflicting operation or reference prevents the request.
- `422`: candidate construction could not complete from current inputs.
- `202`: deployment is accepted or has a persisted non-final/uncertain outcome.

An HTTP request timeout during runtime reload is recorded as `unknown`, not treated as definite failure. Inspect the deployment and explicitly verify before another action.

## Minimal setup examples

Create a subscription (use a URL that serves Mihomo-compatible YAML containing a `proxies` list):

```json
{"name":"Primary","url":"https://example.invalid/mihomo.yaml","enabled":true}
```

Create a fallback group; the returned `id` is the stable reference for terminal and rule targets:

```json
{"name":"OpenAI","mihomoType":"fallback","selectionMode":"manual","memberNodeNames":["LA-01","LA-02"],"memberFilter":{},"healthUrl":"https://www.gstatic.com/generate_204","intervalSeconds":300,"toleranceMs":50}
```

Create a rule using the proxy-group ID returned by the previous request:

```json
{"name":"OpenAI domains","ruleType":"DOMAIN-SUFFIX","ruleValue":"openai.com","target":"<proxy-group-id>","priority":100,"source":"custom","enabled":true}
```

For dynamic group membership, `memberFilter` supports `subscription`, `region`, `keyword`, and `regex`. `subscription` matches the subscription name prefix, while `region` and `keyword` are case-insensitive substrings of the rendered node name. `maxLatencyMs` is rejected until reliable runtime latency samples are available.

### 节点清单

`GET /api/v1/nodes` 从 `subscription_snapshots` 中每个订阅唯一标记为 current 的成功快照读取 `proxies`，返回节点稳定派生 ID、原始节点名、渲染名、订阅来源、节点类型和基于名称的地区推断。节点 ID 由订阅 ID 与原始节点名生成；同名节点来自不同订阅时仍保持不同 ID。地区字段只是一种名称启发式推断，`regionInferred` 用于明确标识其非权威性。节点探测结果通过 `node_probe_results` 持久化；探测结果只代表探测时运行中的 Mihomo 配置，不能证明尚未发布的订阅节点可用。

此接口不会主动刷新订阅。`POST /api/v1/nodes/probe` 会通过 Mihomo controller 对当前运行配置中的节点发起真实延迟探测；未加载的节点标记为 `not_loaded`，控制器未配置时返回 503。没有持久化快照的订阅不会产生节点记录；尚未探测的 `latencyMs` 为 `null`、`status=unmeasured`，不能把节点视为可用或不可用。


### Proxy group node references
`memberNodeIds` is the stable member reference used by new clients. `memberNodeNames` remains for compatibility with existing records and older clients. When both are present, candidate building uses `memberNodeIds` for manual membership. If a referenced node is missing from the candidate built from enabled subscriptions, validation fails and asks the operator to review membership.


### Subscription snapshot semantics

- `subscription_snapshots` stores immutable content hash/path, fetch time, proxy count, and a per-subscription current marker.
- `node_probe_results` stores the latest controller-based delay probe for each stable node ID.
- A refresh promotes a snapshot only after download and Mihomo-YAML validation succeed. Failed refreshes update the subscription status but preserve the prior current snapshot.
- Candidate construction also promotes each successfully fetched and validated source snapshot. A later candidate-build failure does not roll back successful source fetches: the current snapshot represents the latest successful subscription fetch, not the latest successfully published runtime configuration.
- Refreshing a source only updates the node inventory and input snapshot. It never builds or publishes a candidate automatically.
