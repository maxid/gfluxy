---
id: architecture
type: architecture-design
title: gfluxy architecture and module boundaries
status: active
parent: goal
---

# gfluxy architecture and module boundaries

## Overview

```
              ┌────────────── control plane (admin port, HTTP/1.1) ──────────────┐
  ops/CI ────▶│ PUT /v1/config → validate → build immutable snapshot → atomic apply│
              └───────────────────────────┬───────────────────────────────┘
                                          │ snapshot（atomic.Value）
  clients ──▶ single TCP+UDP port ──▶ Caddy ──▶ gfluxy handler ──▶ ReverseProxy ──▶ upstream
   (h1/h2/h3/WS)   (TLS/ALPN/QUIC)   │  route match/strip/canary/LB selection
                                     │
                                     └── Registry（static / nacos）──▶ instances + LabelSet
```

Three planes:

- **Data plane**: Caddy owns listening, TLS, protocol negotiation and WS upgrade; gfluxy's HTTP handler owns routing, canary and reverse proxying. Data-plane config never changes after startup.
- **Control plane**: reads/writes only the in-memory snapshot; `PUT /v1/config` validates the full document then atomically replaces it; data-plane config is untouched.
- **Observability plane**: access log, `/metrics`, `/healthz`, `/readyz`, `/v1/status`.

## Request lifecycle

1. Caddy accepts the connection and determines h1, h2 or h3 from ALPN/QUIC; WS upgrade happens on h1.
2. It hands the request to the gfluxy handler (the only catch-all handler).
3. The handler takes the routing table from the snapshot and matches host + longest path prefix; no match → `404`.
4. It rewrites the path by `StripPrefix`.
5. It evaluates the canary: hashKey → rules in order → target `LabelSet` (default `color=stable`).
6. The registry returns current instances (unhealthy already removed), filtered by the target LabelSet.
7. The LB policy picks one instance in the candidate group (weighted round robin / least connections / consistent hash).
8. `httputil.ReverseProxy` forwards, injecting `X-Forwarded-*` and `X-Gfluxy-Route`/`X-Gfluxy-Gray` for troubleshooting; the response is written back unchanged.
9. Access log and metrics are recorded; instance failure counts are written back to the registry.

## Package structure

| Package | Responsibility | Allowed dependencies |
|---|---|---|
| `cmd/gfluxy` | flags, assembly, signal handling | all below |
| `internal/config` | static startup config (listen, TLS, admin, registry, logging) | standard library |
| `internal/control` | control-plane API, snapshot validation and atomic apply | `snapshot`, `route`, `canary` |
| `internal/snapshot` | immutable rule snapshot + version | `route`, `canary` |
| `internal/route` | routing table compile/match (host/prefix/StripPrefix) | standard library |
| `internal/registry` | `Registry` interface, `Endpoint`, `LabelSet`, health counters | standard library |
| `internal/registry/static` | inline/file instance table | `registry` |
| `internal/registry/nacos` | Nacos subscription adapter, metadata normalization | `registry`, nacos SDK |
| `internal/lb` | weighted round robin, least connections, consistent hash | `registry` |
| `internal/canary` | canary rule evaluation, deterministic hash | `registry` |
| `internal/server` | Caddy assembly, handler, reverse proxy | `route`, `canary`, `lb`, `registry` |
| `internal/obs` | access log, metrics, health probes | standard library |

Dependencies are one-way: `server → canary/lb/route/registry`, `control → snapshot`; registry adapters depend only on the `registry` interface.

## Core models

```go
type Endpoint struct {
    Addr    string            // host:port
    Weight  int               // default 1
    Labels  LabelSet          // version/color/group, normalized from registry metadata
    Healthy bool
}

type LabelSet map[string]string // only version, color and group are kept

type Route struct {
    Host        string   // empty means any host
    PathPrefix  string   // longest prefix first
    StripPrefix int
    Service     string   // registry service name
    Policy      string   // wrr | least_conn | hash
    Gray        []Rule   // evaluated in order, first match wins
}

type Rule struct {
    MatchHeader string // header name; empty means no header match
    MatchValue  string
    MatchTenant string // tenant equality; empty means no tenant match
    Percent     int    // 0..100; 0 means explicit match only
    Target      LabelSet
}
```

## Control-plane contract

| Method | Path | Semantics |
|---|---|---|
| `GET` | `/v1/config` | current effective config document and version |
| `PUT` | `/v1/config` | full-document validation then atomic apply; failure returns `422` and keeps the old snapshot |
| `POST` | `/v1/validate` | validate only, no apply |
| `GET` | `/v1/status` | version, registry connection state, instance health summary, last apply time |
| `GET` | `/healthz` `/readyz` | process liveness / readiness (snapshot loaded and at least one upstream usable) |

**Why full PUT rather than per-resource CRUD**: canary rules have ordering semantics; changing one at a time creates intermediate states. A full document + one atomic swap makes "when the rules became effective" unambiguous. Per-resource PATCH waits until there is a UI.

## Hot-update semantics

- The snapshot is immutable and held in `atomic.Value`; request paths read it lock-free.
- `PUT /v1/config`: parse → validate (route overlap, canary percentages, service existence, valid StripPrefix) → compile route index → store new snapshot → increment version.
- Failure rejects the whole update; the old snapshot and version remain; no partial apply.
- Registry changes do not enter the snapshot; they replace each service's instance table atomically, so instance changes do not change the rule version.

## Key decisions

| Decision | Choice | Rejected | Reason |
|---|---|---|---|
| Data-plane kernel | Caddy as a library, only its listener/TLS/h1-h2-h3/WS | build on `net/http` + `quic-go` | QUIC, TLS hot reload and WS upgrade details are risky; 6–10 person-weeks vs 2–4 |
| Proxy location | gfluxy's own handler with `httputil.ReverseProxy` | Caddy `reverse_proxy` + custom upstream/policy modules | Caddy's upstream list is fixed at config load; rule hot updates would need a reload, conflicting with "no reload" |
| Config apply | full-document atomic replace | per-resource CRUD, Caddy admin reload | canary has ordering semantics; per-resource changes create intermediate/partial states |
| Nacos coupling | only instances and health; metadata normalized to `LabelSet`; release state written by the control plane | read `metadata.version` directly for canary | changing registries or metadata keys must not change canary rules |
| Tenant source | Host inference (exact mapping → `{code}.{base-domain}` → leftmost label), configurable | read `X-Tenant-Id`, parse token | the header is client-forgeable, token parsing needs auth; the gateway does neither, so it trusts only the Host |
| WebSocket | tunnel: byte passthrough after 101 (`ReverseProxy`) | terminate with a third-party WS library | termination loses `permessage-deflate` and arbitrary 101 headers, and needs its own listener; tunneling preserves subprotocol/extension negotiation, pinned by `tunnel_test.go` |
| Canary hash | `hash(route+hashKey) mod 10000`, independent of instances | random, modulo instance list | deterministic; same key never flips, adding instances does not reshuffle |

## Configuration example

```yaml
listen:
  addr: ":8443"
  tls:
    cert_file: /etc/gfluxy/tls.crt
    key_file: /etc/gfluxy/tls.key
admin:
  addr: "127.0.0.1:9000"
registry:
  type: nacos
  nacos:
    servers: ["127.0.0.1:8848"]
    namespace: dev
    group: DEV_GROUP
tenant:
  source: host              # host | header
  base_domain: example.com
  exact:                    # exact domain -> tenant code
    "admin.example.com": "management"
tenant_header: X-Tenant-Id
```

Dynamic part (`PUT /v1/config`):

```yaml
routes:
  - path_prefix: /api/catalog/
    strip_prefix: 2
    service: admin
    policy: wrr
    gray:
      - match_header: X-Gray
        match_value: "1"
        target: {version: "1.1.0"}
      - match_tenant: tenant-a
        target: {version: "1.1.0"}
      - percent: 10
        target: {version: "1.1.0"}
  - path_prefix: /api/orders/
    strip_prefix: 2
    service: iot
  - path_prefix: /api/auth/
    strip_prefix: 1
    service: iam
```

## Test strategy

- **Unit**: longest-prefix routing and StripPrefix, canary hash determinism/distribution, weighted round-robin ratio, instance filtering (LabelSet + health), snapshot validation rejection paths.
- **Integration**: `httptest` upstream + real listener; h3 requests with a `quic-go` client; WS with an echo upstream to verify upgrade and stickiness; Nacos with a fake adapter injecting online/offline events.
- **e2e script**: `make e2e` starts a three-upstream scenario and asserts 404/503/canary ratio/stickiness/hot update without restart.

## Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Caddy h3 + WS + single-port behavior in real networks | entry unavailable | step 3 starts with a minimal listener and h3/WS regression before business logic |
| tenant header forgeable without auth | canary bypass | documented known limit; production edge strips client tenant headers |
| `nacos-sdk-go` compatibility with Nacos 2.x/3.x servers | discovery failure | adapter depends on interfaces only; start with Nacos 2.x gRPC, verify 3.x with the same SDK |
| concurrent full PUTs | later write overwrites earlier | single-writer semantics (ops/CI), version in the response; `If-Match` optimistic locking later |
