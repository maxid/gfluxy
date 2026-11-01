English | [简体中文](README.zh-CN.md)

# gfluxy

Northbound traffic gateway: **one port** accepts HTTP/1.1, HTTP/2, HTTP/3 and WebSocket, routes the frozen `/api/*` prefixes, discovers upstreams through a registry (static file / Nacos), and splits traffic deterministically by request header, tenant or percentage.

> Independent middleware: bound to no product platform; docs and examples use neutral business names. It coexists with nginx / Traefik / Kubernetes Gateway / Ingress: the edge terminates TLS, gfluxy owns discovery-based routing and canary traffic.

## Status

v0 is end-to-end: four protocols on one port, prefix stripping, Nacos discovery and health eviction, three load-balancing policies, three canary dimensions, control-plane hot update, access logs and Prometheus metrics.

## Quick start

```bash
make build
./bin/gfluxy -config examples/gfluxy.yaml     # prepare certificates first, or remove the tls block for plaintext
```

```bash
# Business request (frontend path -> bare backend path)
curl -k https://127.0.0.1:8443/api/catalog/items

# Canary: a header routes to the canary; the response carries X-Gfluxy-Gray
curl -k -H 'X-Gray: 1' https://127.0.0.1:8443/api/catalog/items -i | grep X-Gfluxy-Gray

# Control plane
curl http://127.0.0.1:9000/v1/status
curl -X PUT --data-binary @routes.yaml http://127.0.0.1:9000/v1/config

# Observability
curl http://127.0.0.1:9000/metrics | grep gfluxy_
curl http://127.0.0.1:9000/readyz
```

Every request writes one JSON access-log line to stdout: `route`, `service`, `upstream`, `gray`, `status`, `bytes`, `ms`, `error`.

## Design

- Goals and requirements: [`docs/specs/goal-and-requirements.md`](docs/specs/goal-and-requirements.md)
- Architecture and module boundaries: [`docs/specs/architecture.md`](docs/specs/architecture.md)

Key trade-off: Caddy only owns listening, TLS, ALPN/QUIC and WebSocket upgrade; routing, canary, endpoint selection and reverse proxying live in gfluxy's own handler. A rule update swaps one atomic snapshot — **no data-plane reload, no process restart**.

## Build and test

```bash
make build          # bin/gfluxy
make ci             # fmt-check + vet + test + build
```

Tests cover three protocols on one port (including an h3 client), prefix stripping, WebSocket tunneling, canary hit and stable-group isolation, Nacos metadata normalization with failure retention, and atomic control-plane updates.

## Foundation

The data plane uses [Caddy](https://github.com/caddyserver/caddy) (Apache-2.0) as its kernel: HTTP/1-2-3, WebSocket upgrade, TLS and single-port listener reuse come from its mature implementation; gfluxy provides four parts of its own — routing, registry, canary selection and control plane.

> Caddy is a trademark of the Caddy project (800.com, LLC). This project uses its code under Apache-2.0 only; it is not affiliated with or endorsed by that project.
