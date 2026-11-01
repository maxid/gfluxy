---
id: goal
type: goal-and-requirements
title: gfluxy — northbound traffic gateway (goals and requirements)
status: active
---

# gfluxy — northbound traffic gateway

## Goals

Accept HTTP/1.1, HTTP/2, HTTP/3 and WebSocket on **one port**, route frontend and client traffic from the frozen `/api/*` prefixes to bare backend paths, discover upstream instances through a registry (static file / Nacos), and split traffic **deterministically** by request header, tenant or percentage.

v0 completion criteria:

1. One port (TCP and UDP with the same number) serves h1/h2/h3; WebSocket upgrade succeeds and long-lived connections stay stable.
2. Prefixed paths (`/api/catalog/**`, `/api/orders/**`, `/api/auth/**`) hit bare backend paths after stripping.
3. Nacos instance changes are reflected in routing within seconds; unhealthy instances are evicted.
4. Canary: the same tenant/hash key does not flip versions; percentage deviation over 10k requests is ≤ ±2%.
5. Rule changes take effect hot, without restarting the process or reloading data-plane configuration.

## Problem

Three real constraints hold at once:

- **The client contract is fixed**: existing clients only know prefixed paths (`/api/<domain>/**`), while backends expose bare paths. The edge proxy used to strip the prefix; the gateway must preserve the same semantics byte for byte.
- **Topology splits**: a single process on one machine; in a cluster, iam/admin/iot each have replicas, so the entry must route by service and load-balance by instance.
- **Releases need canaries**: a new version goes to named tenants/test traffic first, then a percentage. A tenant must not flip versions within one session.

Existing deployments split these across an edge proxy, a registry-aware entry gateway and manual weight changes. The entry layer has no business logic and does not authenticate.

## Audience

- **Primary**: operations and private-deployment teams — one entry for single-node and cluster shapes, with zero client changes.
- **Secondary**: any self-built service with instances in a registry that needs canaries and port consolidation.
- **Not** an edge protection device for public tenants (no auth, no WAF).

## Capabilities (v0)

| Capability | Description |
|---|---|
| Single-port convergence | TCP+UDP same numeric port; TCP negotiates h2/h1 via ALPN and supports WS upgrade; h3 over UDP |
| Path routing | Host + path-prefix matching, longest prefix first, `StripPrefix=N` |
| Upstream discovery | `static` (inline config) and `nacos` (instance subscription), normalized to `LabelSet` |
| Load balancing | Weighted round robin, least connections, consistent hash (session key) |
| Canary | Header, tenant, percentage; tenant inferred from Host; deterministic hash, same key never flips |
| Control plane | `GET/PUT /v1/config` atomic apply, `POST /v1/validate`, `GET /v1/status` |
| Observability | Structured access log, `/metrics`, `/healthz`, `/readyz` |
| Health checks | Passive eviction (consecutive failures) + optional active probes; fail fast when a whole group is unhealthy |

## Non-goals (explicitly out of v0)

- **Auth/authorization**: no token parsing or introspection. Authentication stays in each service, as today.
- **Rate limiting/circuit breaking/WAF**: new scope; needs separate requirements and threshold semantics.
- **Automatic TLS certificates (ACME)**: v0 accepts static certificates only, supplied by the edge or operations.
- **L4 proxying** (MySQL/Redis/TCP business protocols): HTTP family and WS only.
- **Config UI, multi-tenant admin plane**: the control plane is an HTTP API only.
- **Session migration**: when a WS instance goes down the connection closes and the client reconnects; no connection migration.
- **gRPC/GraphQL transcoding**: request bodies are not parsed.

## Constraints

- **Pure reverse proxy**: never read, modify or cache business request bodies; only rewrite path, headers and target address.
- **Data-plane kernel is Caddy (Apache-2.0)**: reuse TLS, h1/h2/h3 and WS upgrade; gfluxy owns routing, discovery, canary and the control plane. Product names must not imply Caddy endorsement.
- **Rule hot updates do not reload the data plane**: data-plane config is fixed after startup; business rules come from an in-memory snapshot.
- **Determinism**: the same input (route + hash key + rule version) must select the same target group regardless of instance count.
- **Independent middleware**: no product platform binding — docs, examples, tests and code comments contain no platform module names, paths or contracts; examples use neutral business names.

## Behaviour contracts (user-visible)

### Prefix stripping

| Request path | Strip | Upstream receives |
|---|---|---|
| `/api/catalog/items` | 2 | `/users` |
| `/api/orders/page` | 2 | `/devices/page` |
| `/api/auth/login` | 1 | `/auth/login` |
| Any unmatched path | — | `404` (no guessed default upstream) |

### Canary

- Rules evaluate in order; **first match wins**; no match uses the stable group (`labels.color=stable`).
- Match conditions: header equality, tenant equality, percentage interval.
- **Tenant is inferred from Host**, following the existing platform login order: exact domain mapping → `{code}.{base-domain}` → leftmost Host label. The mapping table is configuration; the gateway does not query business databases.
- **Inbound `X-Tenant-Id` does not participate**: on the existing platform only the root-tenant operator may use it, and it is fail-closed validated. The gateway has no identity to validate. The header is passed through unchanged.
- Percentage uses a deterministic hash: `hash(route + hashKey) mod 10000 < percent*100`; `hashKey` is inferred tenant → configured session cookie → client IP.
- The same hashKey always selects the same branch while the rule version is unchanged; increasing the percentage only grows the hit set and never reshuffles existing users.
- **The stable group is the instances no rule targets**: operations need no extra label on stable instances; a new canary carrying the target label is automatically excluded from stable.
- If the matched target group has no instances, return `503`; never fall back to stable — a silent fallback makes a failed release look successful.
- Responses carry `X-Gfluxy-Gray` (for example `version=1.1.0`) to confirm the selected group in browsers/logs.

### Failure and degradation

- Nacos unavailable: keep the last successful instance list (stale-while-error) and mark `/status` `degraded`; do not blank the whole site.
- An instance reaching the consecutive-failure threshold is evicted; half-open retry after a cooldown.
- A group with no healthy instance returns `503`; never send traffic to a known-bad instance.

### WebSocket

- Upgrade over h1 only (h2 extended CONNECT and h3 WS client support are insufficient in v0).
- After establishment **stick** to one instance; when the instance goes down the connection closes and the client reconnects through the LB.
- Idle/heartbeat timeouts are per-route configurable; no default active disconnect.

### h3 fallback

- A client without QUIC falls back to h2/h1 via ALPN; no extra configuration.
- The data-plane kernel emits `Alt-Svc`; gfluxy does not interfere.

## Known limits

- **No token parsing**: a tenant can only be inferred from Host, so multiple tenants on one domain cannot be distinguished; cross-tenant canaries need separate domains or an authenticating edge.
- Operator tenant switching (`X-Tenant-Id`) is decided by the backend from token permissions; the gateway neither participates nor validates, only passes it through.
- Canary rules only evaluate request attributes, not business sessions; the same tenant across multiple routes needs a rule per route.
