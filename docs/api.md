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

IDs are stable opaque strings. Rule `target` is `DIRECT`, `REJECT`, or a proxy-group ID闁炽儲鏀簅t a display name. Terminal `proxyGroupId` is a proxy-group ID.

## Configuration lifecycle

- `POST /api/v1/config-candidates` fetches the current enabled subscriptions, builds a candidate, persists source snapshots, and runs Mihomo config validation.
- `GET /api/v1/config-candidates/{id}` reads candidate metadata.
- `GET /api/v1/config-candidates/{id}/inputs` lists the immutable subscription input snapshots and hashes used to build that candidate.
- `GET /api/v1/config-candidates/{id}/content` returns the candidate's exact YAML bytes plus its `sha256`, `status`, and `validationMessage`. The handler recomputes the digest and returns `500` if the stored file no longer matches the recorded hash, rather than serving unverified content.
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

### 闁煎搫鍊婚崑锝呫€掗崨顓炵

`GET /api/v1/nodes` 濞?`subscription_snapshots` 濞戞搩鍘介惁鈩冪▔椤忓浂鍚傞梻鍐ㄦ噹閺侇喗绋夐埀顒勫冀閸ヮ亶鍞跺☉?current 闁汇劌瀚崹姘跺礉閻旈攱褰ラ柣鎾楀棭鍤㈤柛?`proxies`闁挎稑鐭佺换鎴﹀炊閻愯泛螡闁绘劕婀辫彊閻庤纰嶅ǎ鎶芥偨?ID闁靛棔绀佺敮顐ｆ叏鐎ｎ厼螡闁绘劗鎳撻幃鏇㈠Υ娴ｇ顩柡灞炬尭閹洟濡存担绛嬪悅闂傚啫鎳忓闈涒攦閹扳斁鍋撴担钘壩濋柣鎰贡鐞氼偊宕圭€ｎ亝瀚查柛鈺勬〃缁剟宕ュ鍥嗙偤鎯冮崟顐ｅ嬀闁告牜鍎ょ敮褰掑棘椤撴壕鍋撻崒婵單濋柣?ID 闁汇垼绮鹃褰掓⒓?ID 濞戞挸楠哥敮顐ｆ叏鐎ｎ厼螡闁绘劗鎳撻幃鏇㈡偨閻旂鐏囬柨娑欑☉閹捇宕ュ鍫澪濋柣鎰潐濞肩敻鎳涢鍐憹闁告艾鐭侀褰掓⒓閸涱喗顦уù鐘茬С缁绘岸骞愭担椋庣憹闁?ID闁靛棗鍊稿﹢鎾礌閸濆嫮鎽熸繛鍫ユ涧瑜把囧及椤栨瑧顏辩紒澶婄Т閹洜绮旈弶鎸庡剻闁告瑦鍨电槐锟犲箳閵婏附鐒介柨娑樼暛regionInferred` 闁活潿鍔嬬花顒勫及鎼达絺鈧﹢寮介崶顏嗘闁稿繐鐖煎顏堝级閸愩劉鏋嗛柟顑讲鍋撻崒婵單濋柣鎰潐鐢澘霉鐎ｎ剛娉㈤柡瀣矒閳ь剚淇虹换?`node_probe_results` 闁归晲妞掔粻娆撳礌閺嶇數骞㈤柟鎭掑灪缁佸绱掗幘瀵镐函闁告瑯浜欓崬顒傛偘閵婏箑璧存繛鏉戭儐濡炲倹娼婚幇顖ｆ斀濞戞搩鍘惧▓?Mihomo 闂佹澘绉堕悿鍡涙晬鐏炶偐鐟濋柤鍐测偓鐔烘闁哄嫬楠搁惃濠氬嫉椤忓嫬绲洪悽顖氬暟濞堟垹鎷嬮姀銈嗩潐闁煎搫鍊婚崑锝夊矗椤栨粍鏆忛柕?
婵縿鍊栫敮鎾矗閿濆嫮鐟濆ù鍏肩煯鐎靛矂宕濋妸銉ョ厱闁哄倹濯介褰掓⒓閸涱偀鍋撴穱姗甇ST /api/v1/nodes/probe` 濞村吋宀搁埀顒佷亢缁?Mihomo controller 閻庣數鎳撶紞瀣礈瀹ュ牏绠ラ悶娑樼焸閸樸倗绱旈鏄忓幀闁汇劌瀚俊顓㈡倷閻熸澘绲洪悹褔顥撳﹢锛勨偓鍦仜濞嗐垺娼婚悢绋胯荡婵炴潙顑戠槐閬嶅嫉椤忓嫬顫ｉ弶鐐垫櫕濞堟垿鎳為崒婊冧化闁哄秴娲╅鍥ㄧ▔?`not_loaded`闁挎稑鏈敮鍫曞礆鐠虹儤鐝ら柡鍫邯閸樸倗绱旈鑺ヮ槯閺夆晜鏌ㄥú?503闁靛棗鍊归惀鍛村嫉婢跺鐦☉鏂挎噹鐎佃尪绠涢銈呭季闁汇劌瀚褰掓⒓閸涱剛鐟濆ù鍏肩煯妤犲洭鎮介悢璇参濋柣鎰攰椤斿洩銇愰弴顏嗗耿閻忓繑纰嶅﹢顓㈠箳閵忥紕銈撮柣?`latencyMs` 濞?`null`闁靛棔姊梥tatus=unmeasured`闁挎稑濂旂粭澶愭嚄閼恒儱惟闁煎搫鍊婚崑锝囨喆閸℃洝绀嬮柛娆樺灣閺併倝骞嬮弽锔剧憹闁告瑯鍨抽弫銈夊Υ?

### Proxy group node references
`memberNodeIds` is the stable member reference used by new clients. `memberNodeNames` remains for compatibility with existing records and older clients. When both are present, candidate building uses `memberNodeIds` for manual membership. If a referenced node is missing from the candidate built from enabled subscriptions, validation fails and asks the operator to review membership.


### Subscription snapshot semantics

- `subscription_snapshots` stores immutable content hash/path, fetch time, proxy count, and a per-subscription current marker.
- `node_probe_results` stores the latest controller-based delay probe for each stable node ID.
- A refresh promotes a snapshot only after download and Mihomo-YAML validation succeed. Failed refreshes update the subscription status but preserve the prior current snapshot.
- Candidate construction also promotes each successfully fetched and validated source snapshot. A later candidate-build failure does not roll back successful source fetches: the current snapshot represents the latest successful subscription fetch, not the latest successfully published runtime configuration.
- Refreshing a source only updates the node inventory and input snapshot. It never builds or publishes a candidate automatically.
