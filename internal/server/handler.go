// Copyright 2026 The gfluxy Authors.
package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httputil"
	"sync/atomic"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	"github.com/maxid/gfluxy/internal/registry"
	"github.com/maxid/gfluxy/internal/registry/health"
	"github.com/maxid/gfluxy/internal/route"
)

const handlerName = "gfluxy"

// gateway is the immutable state a request reads: the routing table and the
// registry. Replacing the pointer is the whole of a hot update.
type gateway struct {
	table  *route.Table
	reg    registry.Registry
	health *health.Tracker
	rr     atomic.Uint64
}

var active atomic.Pointer[gateway]

type targetKey struct{}

// attempt carries per-request failure state from the proxy back to the
// handler, which is where health accounting happens.
type attempt struct{ failed bool }

type attemptKey struct{}

// Handler is the single catch-all HTTP handler.
type Handler struct{}

// CaddyModule registers the handler under http.handlers.gfluxy.
func (Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers." + handlerName,
		New: func() caddy.Module { return new(Handler) },
	}
}

// ServeHTTP resolves the route, picks an upstream and proxies the request.
// WebSocket upgrades take the same path: ReverseProxy switches to tunnelling
// once the upstream answers 101.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, _ caddyhttp.Handler) error {
	g := active.Load()
	if g == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway not ready")
		return nil
	}

	rt, path, ok := g.table.Match(r.Host, r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "no route")
		return nil
	}

	endpoint, ok := g.pick(rt.Service)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "no healthy upstream for "+rt.Service)
		return nil
	}

	att := &attempt{}
	ctx := context.WithValue(r.Context(), targetKey{}, endpoint)
	ctx = context.WithValue(ctx, attemptKey{}, att)

	r = r.WithContext(ctx)
	r.URL.Path = path
	r.URL.RawPath = ""
	r.Header.Set("X-Gfluxy-Route", rt.Service+":"+rt.PathPrefix)

	proxy().ServeHTTP(w, r)

	if att.failed {
		g.health.Fail(endpoint.Addr)
	} else {
		g.health.Succeed(endpoint.Addr)
	}
	return nil
}

// pick is a round-robin over healthy endpoints. Weighted and sticky policies
// arrive with the load-balancing milestone; keeping one implementation here
// means the proxy path is already exercised end to end.
func (g *gateway) pick(service string) (registry.Endpoint, bool) {
	endpoints := g.reg.Resolve(service)
	if len(endpoints) == 0 {
		return registry.Endpoint{}, false
	}
	healthy := make([]registry.Endpoint, 0, len(endpoints))
	for _, ep := range endpoints {
		if ep.Healthy && g.health.Available(ep.Addr) {
			healthy = append(healthy, ep)
		}
	}
	if len(healthy) == 0 {
		return registry.Endpoint{}, false
	}
	next := g.rr.Add(1) - 1
	return healthy[next%uint64(len(healthy))], true
}

var sharedProxy *httputil.ReverseProxy

func proxy() *httputil.ReverseProxy {
	return sharedProxy
}

func newProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			endpoint, ok := req.Context().Value(targetKey{}).(registry.Endpoint)
			if !ok {
				return
			}
			req.URL.Scheme = "http"
			req.URL.Host = endpoint.Addr

			if proto := req.Header.Get("X-Forwarded-Proto"); proto == "" {
				proto = "http"
				if req.TLS != nil {
					proto = "https"
				}
				req.Header.Set("X-Forwarded-Proto", proto)
			}
			if req.Header.Get("X-Forwarded-Host") == "" {
				req.Header.Set("X-Forwarded-Host", req.Host)
			}
		},
		Transport:     newTransport(),
		ErrorHandler:  proxyError,
		FlushInterval: -1, // stream responses; WebSocket tunnels are unaffected
	}
}

func newTransport() *http.Transport {
	return &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ForceAttemptHTTP2:     false, // upstreams are HTTP/1.1 JSON services
	}
}

func proxyError(w http.ResponseWriter, r *http.Request, err error) {
	if att, ok := r.Context().Value(attemptKey{}).(*attempt); ok {
		att.failed = true
	}
	writeError(w, http.StatusBadGateway, "upstream error: "+err.Error())
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

var _ caddyhttp.MiddlewareHandler = (*Handler)(nil)

func init() { caddy.RegisterModule(Handler{}) }
