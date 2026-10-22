# Security boundaries

This project is a control plane for a live proxy runtime. Read this before changing any
bind address.

## What is protected today

| Surface | Default binding | Protection |
|---|---|---|
| Studio control API (`8080`) | `127.0.0.1` | `MIHOMO_SECRET` via `X-Admin-Token`, when set. See below. |
| Mihomo controller (`9090`) | `127.0.0.1` | `MIHOMO_SECRET` bearer token. |
| Proxy listener (`7890`) | `127.0.0.1` | None — it is the proxy itself. |

### One secret for both surfaces

The panel and the controller share `MIHOMO_SECRET`. That value is what the manager writes
into the generated `config.yaml` as `secret:`, and it is also what the manager presents
when it talks to the controller. Reusing it as the panel credential means an operator
remembers one password, and rotating it in `.env` keeps both sides in step.

Requests present it in the `X-Admin-Token` header. It is **not** accepted in the query
string: a query parameter would leak into browser history, proxy access logs, and `Referer`
headers. Comparison uses `crypto/subtle.ConstantTimeCompare`, so repeated wrong guesses
cannot be distinguished by timing.

The browser stores the token in `sessionStorage`, not `localStorage`, so closing the tab
forgets it rather than leaving it on disk indefinitely. It never appears in the URL.

### Fail-closed by default

There is no built-in fallback secret. A hard-coded default would be public knowledge and
would look like protection while providing none.

| Situation | Behaviour |
|---|---|
| `MIHOMO_SECRET` set | Every `/api/v1/*` request except `/auth/status` must present the header. |
| `MIHOMO_SECRET` unset, listener is loopback | Allowed to start unauthenticated, with a startup warning. |
| `MIHOMO_SECRET` unset, listener is not loopback | **Refuses to start** with an actionable error. |

`0.0.0.0`, `:8080`, and any routable address count as non-loopback. An unparseable
`LISTEN_ADDR` is also treated as non-loopback, so a typo fails closed rather than quietly
exposing the panel.

### What stays reachable without a credential

`/healthz`, `/api.js`, `/`, `/prototype-v6.html`, and `/api/v1/auth/status`. These carry no
desired state: the shell must load so it can prompt for the token, the container health
check must keep working, and the status endpoint only reports whether a credential is
required. `authStatus` never echoes the secret.

### What this does not protect against

This is a trusted-LAN access control, not a multi-user authentication system:

- Anyone who learns `MIHOMO_SECRET` has full control of the panel and the controller.
- There is no user list, no roles, no session expiry, no rate limiting, and no lockout.
- Rejected requests are not audited; only accepted deployments are.
- The token travels in plaintext over HTTP. Put the panel behind TLS if the network is not
  trusted.
- Compromising the browser's `sessionStorage` grants access until the tab closes.

## What a LAN binding means

Setting `STUDIO_BIND_IP` or `CONTROLLER_BIND_IP` to a LAN address changes the exposure in a
way that is easy to underestimate:

- **Studio on the LAN** lets anyone holding `MIHOMO_SECRET` read your configuration, read
  subscription URLs, create or delete proxy groups, terminals, and rules, build candidates,
  and **publish them to the running Mihomo instance**. Subscription URLs frequently embed
  access tokens, so this also discloses them. Without a configured secret the panel refuses
  to start on a non-loopback listener, so this cannot happen by accident.
- **Controller on the LAN** lets anyone holding `MIHOMO_SECRET` rewrite live routing
  directly, bypassing candidate validation, confirmation, audit, and rollback.

Reaching these ports requires network access to the host. They are not reachable from the
public Internet unless the host is separately forwarded or exposed, but they are reachable
by every device on the same subnet, including guests and internet-of-things devices.

## Deliberate decision on record

Binding these ports to a trusted subnet while relying on the shared secret is an accepted
trade-off for this deployment. If you do:

- Set the bind address to one specific interface address, not `0.0.0.0`. Binding a single
  address fails closed for unexpected interfaces and is easy to audit with `ss -ltn`.
- Restrict the source range at the host firewall, so the port is not open to the whole
  subnet merely because it is bound to one address.
- Understand that this is a trust decision about every device on the network, not a
  technical control.

## Known gaps, not yet implemented

- [ ] Per-user accounts, roles, and session expiry.
- [ ] CSRF tokens for state-changing requests. The current same-origin check rejects a
      mismatched `Origin` but permits a request that omits it entirely; the shared-secret
      header is what actually gates those requests today.
- [ ] Encryption of subscription URLs at rest; they are currently stored in plaintext in SQLite.
- [ ] Rate limiting and lockout on the control API.
- [ ] Auditing of rejected requests, not only of accepted deployments.
- [ ] TLS termination for the panel.

Until those exist, treat any non-loopback binding as a trusted-network-only configuration.
Do not expose it to the Internet under any circumstances. See also the
[SSRF guard](#ssrf-guard) below.

## SSRF guard

Subscription URLs are user-supplied and are fetched by the control plane, so the fetcher
resolves the host and refuses to dial private or reserved destinations. A stored URL must
not become a way to reach services on the operator's own network.

Rejected ranges: `0.0.0.0/8`, `10.0.0.0/8`, `100.64.0.0/10`, `127.0.0.0/8`,
`169.254.0.0/16`, `172.16.0.0/12`, `192.0.0.0/24`, `192.0.2.0/24`, `192.88.99.0/24`,
`192.168.0.0/16`, `198.51.100.0/24`, `203.0.113.0/24`, `224.0.0.0/4`, `240.0.0.0/4`, plus
the IPv6 loopback, unspecified, link-local, unique-local, and documentation ranges.

`198.18.0.0/15` (RFC 2544 benchmarking) is **deliberately allowed**. Real subscription
providers publish that range as the A record for their delivery hosts, and Mihomo TUN or
fake-IP DNS setups allocate client addresses from it, so rejecting it refused legitimate
subscriptions. The trade-off is explicit: a subscription URL may now reach a host in that
range. Outbound requests remain bounded by a 15-second timeout, a 5 MiB response cap, a
redirect limit, and HTTPS/HTTP-only schemes.

If you operate in an environment where nothing legitimate lives in `198.18.0.0/15`,
re-adding it to `nonPublicNetworks` in `internal/config/builder.go` restores the stricter
behavior. `TestIsPublicAddress` pins both sides of that boundary.
