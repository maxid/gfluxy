// Copyright 2026 The gfluxy Authors.
package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"

	"github.com/maxid/gfluxy/internal/canary"
	"github.com/maxid/gfluxy/internal/lb"
	"github.com/maxid/gfluxy/internal/obs"
	"github.com/maxid/gfluxy/internal/registry"
	"github.com/maxid/gfluxy/internal/registry/health"
	"github.com/maxid/gfluxy/internal/route"
	"github.com/maxid/gfluxy/internal/tenant"
)

const handlerName = "gfluxy"

// gateway is the immutable state a request reads: the routing table and the
// registry. Replacing the pointer is the whole of a hot update.
type gateway struct {
	table        *route.Table
	reg          registry.Registry
	health       *health.Tracker
	policies     map[string]lb.Policy
	tenant       tenant.Resolver
	registryType string
}

var active atomic.Pointer[gateway]

type targetKey struct{}

// attempt carries per-request failure state from the proxy back to the
// handler, which is where health accounting happens.
type attempt struct {
	failed bool
	err    string
}

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
		obs.Log(obs.Event{Proto: r.Proto, Method: r.Method, Host: r.Host, Path: r.URL.Path, Status: http.StatusNotFound})
		writeError(w, http.StatusNotFound, "no route")
		return nil
	}

	key := stickyKey(r)
	tenantCode := g.tenant.Resolve(r.Host, r.Header)
	target := rt.Gray.Decide(rt.PathPrefix, r.Header, tenantCode, key)
	if target != nil {
		w.Header().Set("X-Gfluxy-Gray", labels(target))
	}

	policy := g.policyFor(rt.Policy)
	endpoint, ok := g.pick(policy, rt.Service, key, rt.Gray, target)
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

	start := time.Now()
	rec := &recorder{ResponseWriter: w}
	proxy().ServeHTTP(rec, r)
	policy.Release(endpoint)

	if att.failed {
		g.health.Fail(endpoint.Addr)
		obs.UpstreamFailures.WithLabelValues(rt.Service, endpoint.Addr).Inc()
	} else {
		g.health.Succeed(endpoint.Addr)
	}

	routeLabel := rt.PathPrefix
	obs.Requests.WithLabelValues(routeLabel, rt.Service, statusLabel(rec.status)).Inc()
	if grayLabel := w.Header().Get("X-Gfluxy-Gray"); grayLabel != "" {
		obs.GrayHits.WithLabelValues(routeLabel, grayLabel).Inc()
	}
	obs.Log(obs.Event{
		Proto:    r.Proto,
		Method:   r.Method,
		Host:     r.Host,
		Path:     r.URL.Path,
		Route:    routeLabel,
		Service:  rt.Service,
		Upstream: endpoint.Addr,
		Gray:     w.Header().Get("X-Gfluxy-Gray"),
		Status:   rec.statusOrOK(),
		Bytes:    rec.bytes,
		Millis:   time.Since(start).Milliseconds(),
		Error:    att.err,
	})
	return nil
}

// policyFor falls back to weighted round robin; unknown names are rejected at
// start-up, so a fallback here only guards against an empty policy field.
func (g *gateway) policyFor(name string) lb.Policy {
	if p, ok := g.policies[name]; ok {
		return p
	}
	return g.policies[lb.WeightedRoundRobin]
}

// pick filters the candidate set (health first) and defers the choice to the
// route's policy.
func (g *gateway) pick(policy lb.Policy, service, key string, gray canary.Decider, target registry.LabelSet) (registry.Endpoint, bool) {
	endpoints := g.reg.Resolve(service)
	if len(endpoints) == 0 {
		return registry.Endpoint{}, false
	}
	candidates := make([]registry.Endpoint, 0, len(endpoints))
	for _, ep := range endpoints {
		if !ep.Healthy || !g.health.Available(ep.Addr) || !gray.Keep(ep, target) {
			continue
		}
		candidates = append(candidates, ep)
	}
	return policy.Pick(candidates, key)
}

// labels renders a target label set for the diagnostic response header.
func labels(target registry.LabelSet) string {
	if len(target) == 0 {
		return ""
	}
	parts := make([]string, 0, len(target))
	for _, key := range []string{registry.LabelVersion, registry.LabelColor, registry.LabelGroup} {
		if v, ok := target[key]; ok {
			parts = append(parts, key+"="+v)
		}
	}
	return strings.Join(parts, ",")
}

// stickyKey keeps a client on the same upstream across reconnects. Without
// authentication the client address is the only stable, non-spoofable value.
func stickyKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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
		att.err = err.Error()
	}
	writeError(w, http.StatusBadGateway, "upstream error: "+err.Error())
}

// recorder captures the status and size for the access log and metrics while
// staying transparent to the proxy: Unwrap keeps http.ResponseController and
// Hijack keep WebSocket tunnelling working.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	// Caddy's writer reaches Hijack through Unwrap, not through a direct type
	// assertion, so go via the controller instead of asserting http.Hijacker.
	conn, brw, err := http.NewResponseController(r.ResponseWriter).Hijack()
	if err == nil && r.status == 0 {
		r.status = http.StatusSwitchingProtocols
	}
	return conn, brw, err
}

func (r *recorder) statusOrOK() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

func statusLabel(status int) string { return strconv.Itoa(status) }

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

var _ caddyhttp.MiddlewareHandler = (*Handler)(nil)

func init() { caddy.RegisterModule(Handler{}) }
