# Security boundaries

This project is a control plane for a live proxy runtime. Read this before changing any
bind address.

## What is protected today

| Surface | Default binding | Protection |
|---|---|---|
| Studio control API (`8080`) | `127.0.0.1` | **None.** Same-origin check only. No authentication. |
| Mihomo controller (`9090`) | `127.0.0.1` | `MIHOMO_SECRET` bearer token. |
| Proxy listener (`7890`) | `127.0.0.1` | None — it is the proxy itself. |

The same-origin check in `internal/api/server.go` rejects requests whose `Origin` header
names a different host. That is a CSRF-oriented guard for the browser UI. It is **not**
authentication: any non-browser client that omits `Origin` is served normally.

## What a LAN binding means

Setting `STUDIO_BIND_IP` or `CONTROLLER_BIND_IP` to a LAN address changes the exposure in a
way that is easy to underestimate:

- **Studio on the LAN** lets anyone who can reach that port read your configuration, read
  subscription URLs, create or delete proxy groups, terminals, and rules, build candidates,
  and **publish them to the running Mihomo instance**. Subscription URLs frequently embed
  access tokens, so this also discloses them.
- **Controller on the LAN** lets anyone holding `MIHOMO_SECRET` rewrite live routing
  directly, bypassing candidate validation, confirmation, audit, and rollback.
- Neither surface rate-limits or logs failed authentication, because there is no
  authentication to fail.

Reaching these ports requires network access to the host. They are not reachable from the
public Internet unless the host is separately forwarded or exposed, but they are reachable
by every device on the same subnet, including guests and internet-of-things devices.

## Deliberate decision on record

An operator may deliberately bind these ports to a specific trusted subnet without adding
authentication. If you do:

- Set the bind address to one specific interface address, not `0.0.0.0`. Binding a single
  address fails closed for unexpected interfaces and is easy to audit with `ss -ltn`.
- Restrict the source range at the host firewall, so the port is not open to the whole
  subnet merely because it is bound to one address.
- Understand that this is a trust decision about every device on the network, not a
  technical control.

The deployment used during development follows that pattern: bind to the host's single
LAN address and allow only the LAN CIDR at the firewall.

## Known gaps, not yet implemented

These are tracked as product work, not as solved problems:

- [ ] Administrator authentication and session handling for the Studio API.
- [ ] CSRF token issuance and validation for state-changing requests.
- [ ] Encryption of subscription URLs at rest; they are currently stored in plaintext in SQLite.
- [ ] Rate limiting and lockout on the control API.
- [ ] Audit of rejected requests, not only of accepted deployments.

Until authentication and CSRF protection are complete, treat any non-loopback binding of
port `8080` as a temporary, trusted-network-only configuration. Do not expose it to the
Internet under any circumstances.

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
