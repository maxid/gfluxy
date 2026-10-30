// Copyright 2026 The gfluxy Authors.
//
// Package server wires the data plane. Caddy owns the listener, TLS,
// ALPN/QUIC negotiation and WebSocket upgrades; gfluxy owns a single
// catch-all handler that resolves routes, gray rules and upstreams from
// an in-memory snapshot.
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp"
	_ "github.com/caddyserver/caddy/v2/modules/caddytls"

	"github.com/maxid/gfluxy/internal/canary"
	"github.com/maxid/gfluxy/internal/config"
	"github.com/maxid/gfluxy/internal/lb"
	"github.com/maxid/gfluxy/internal/registry"
	"github.com/maxid/gfluxy/internal/registry/health"
	"github.com/maxid/gfluxy/internal/registry/nacos"
	"github.com/maxid/gfluxy/internal/registry/static"
	"github.com/maxid/gfluxy/internal/route"
	"github.com/maxid/gfluxy/internal/tenant"
)

// Server is a runnable data plane.
type Server struct {
	doc     []byte
	reg     registry.Registry
	closers []func()
}

// New validates the routing table against the registry and compiles both into
// a Caddy config document. A route pointing at a service the static registry
// does not know is a start-up error, not a runtime 503.
func New(cfg *config.Config) (*Server, error) {
	doc, err := buildDocument(cfg)
	if err != nil {
		return nil, err
	}

	routes, err := compileRoutes(cfg.Routes)
	if err != nil {
		return nil, err
	}
	reg, closers, err := buildRegistry(cfg, cfg.Routes)
	if err != nil {
		return nil, err
	}

	policies, err := buildPolicies()
	if err != nil {
		return nil, err
	}

	active.Store(&gateway{
		table:    routes,
		reg:      reg,
		health:   health.New(3, 30*time.Second),
		policies: policies,
		tenant: tenant.Resolver{
			Source:     cfg.Tenant.Source,
			Header:     cfg.Tenant.Header,
			BaseDomain: cfg.Tenant.BaseDomain,
			Exact:      cfg.Tenant.Exact,
		},
		registryType: cfg.Registry.Type,
	})
	stored := make([]config.Route, len(cfg.Routes))
	copy(stored, cfg.Routes)
	appliedRoutes.Store(&stored)
	configVersion.Store(1) // the start-up document is version 1
	return &Server{doc: doc, reg: reg, closers: closers}, nil
}

// Status is the control plane's view of the data plane.
type Status struct {
	Version       uint64 `json:"version"`
	Routes        int    `json:"routes"`
	RegistryOK    bool   `json:"registry_ok"`
	RegistryError string `json:"registry_error,omitempty"`
}

var (
	configVersion atomic.Uint64
	appliedRoutes atomic.Pointer[[]config.Route]
)

// ValidateRoutes runs every check ApplyRoutes would run, without touching the
// running table.
func ValidateRoutes(routes []config.Route) error {
	current := active.Load()
	if current == nil {
		return fmt.Errorf("gateway is not initialised")
	}
	if _, err := compileRoutes(routes); err != nil {
		return err
	}
	if _, err := buildPolicies(); err != nil {
		return err
	}
	if current.registryType == "static" {
		return checkServicesServed(routes, current.reg)
	}
	return nil
}

// ApplyRoutes validates a new routing document and swaps it in as one atomic
// step. A rejected document leaves the running table untouched.
func ApplyRoutes(routes []config.Route) (uint64, error) {
	current := active.Load()
	if current == nil {
		return configVersion.Load(), fmt.Errorf("gateway is not initialised")
	}
	if err := ValidateRoutes(routes); err != nil {
		return configVersion.Load(), err
	}

	table, err := compileRoutes(routes)
	if err != nil {
		return configVersion.Load(), err
	}
	policies, err := buildPolicies()
	if err != nil {
		return configVersion.Load(), err
	}

	active.Store(&gateway{
		table:        table,
		reg:          current.reg,
		health:       current.health,
		policies:     policies,
		tenant:       current.tenant,
		registryType: current.registryType,
	})
	stored := make([]config.Route, len(routes))
	copy(stored, routes)
	appliedRoutes.Store(&stored)

	return configVersion.Add(1), nil
}

// CurrentRoutes returns the document currently in force.
func CurrentRoutes() []config.Route {
	stored := appliedRoutes.Load()
	if stored == nil {
		return nil
	}
	out := make([]config.Route, len(*stored))
	copy(out, *stored)
	return out
}

// Version returns the number of successfully applied documents.
func Version() uint64 { return configVersion.Load() }

// Ready reports whether the data plane can serve traffic.
func Ready() bool {
	current := active.Load()
	if current == nil {
		return false
	}
	if reporter, ok := current.reg.(registry.StatusReporter); ok && reporter.Status() != nil {
		return false
	}
	return true
}

// Snapshot reports the running state.
func Snapshot() Status {
	status := Status{Version: configVersion.Load(), Routes: len(CurrentRoutes()), RegistryOK: true}
	current := active.Load()
	if current == nil {
		return status
	}
	if reporter, ok := current.reg.(registry.StatusReporter); ok {
		if err := reporter.Status(); err != nil {
			status.RegistryOK = false
			status.RegistryError = err.Error()
		}
	}
	return status
}

// Start loads the configuration and begins serving. It returns once the
// listener is bound; serving continues in the background (Caddy owns it).
func (s *Server) Start() error {
	// The gateway is configured by the control plane, not by a Caddyfile on
	// disk: leaving an autosave artefact behind would be a second source of
	// truth that never gets applied.
	caddy.ConfigAutosavePath = ""
	sharedProxy = newProxy()

	if err := caddy.Load(s.doc, true); err != nil {
		return fmt.Errorf("start server: %w", err)
	}
	return nil
}

// Stop drains the listener and its active connections, then releases the
// discovery subscriptions.
func (s *Server) Stop() error {
	err := caddy.Stop()
	for _, closeFn := range s.closers {
		closeFn()
	}
	return err
}

func compileRoutes(in []config.Route) (*route.Table, error) {
	routes := make([]route.Route, 0, len(in))
	for _, rt := range in {
		if _, err := lb.New(rt.Policy); err != nil {
			return nil, fmt.Errorf("route %s: %w", rt.PathPrefix, err)
		}
		gray, err := compileGray(rt)
		if err != nil {
			return nil, err
		}
		routes = append(routes, route.Route{
			Host:        rt.Host,
			PathPrefix:  rt.PathPrefix,
			StripPrefix: rt.StripPrefix,
			Service:     rt.Service,
			Policy:      rt.Policy,
			Gray:        gray,
		})
	}
	table, err := route.Compile(routes)
	if err != nil {
		return nil, fmt.Errorf("routes: %w", err)
	}
	return table, nil
}

func buildRegistry(cfg *config.Config, routes []config.Route) (registry.Registry, []func(), error) {
	switch cfg.Registry.Type {
	case "static":
		services := make(map[string][]registry.Endpoint, len(cfg.Registry.Static.Services))
		for name, endpoints := range cfg.Registry.Static.Services {
			for _, ep := range endpoints {
				services[name] = append(services[name], registry.Endpoint{
					Addr:   ep.Addr,
					Weight: ep.Weight,
					Labels: registry.LabelSet(ep.Labels),
				})
			}
		}
		reg := static.New(services)
		if err := checkServicesServed(routes, reg); err != nil {
			return nil, nil, err
		}
		return reg, nil, nil
	case "nacos":
		reg, err := nacos.New(nacos.Config{
			Servers:   cfg.Registry.Nacos.Servers,
			Namespace: cfg.Registry.Nacos.Namespace,
			Group:     cfg.Registry.Nacos.Group,
			Username:  cfg.Registry.Nacos.Username,
			Password:  cfg.Registry.Nacos.Password,
		}, referencedServices(routes))
		if err != nil {
			return nil, nil, fmt.Errorf("registry nacos: %w", err)
		}
		log.Printf("gfluxy: nacos discovery ready for %d services", len(referencedServices(routes)))
		return reg, []func(){reg.Close}, nil
	default:
		return nil, nil, fmt.Errorf("registry.type %q: unsupported", cfg.Registry.Type)
	}
}

func compileGray(rt config.Route) (canary.Decider, error) {
	rules := make([]canary.Rule, 0, len(rt.Gray))
	for _, gr := range rt.Gray {
		if gr.Percent < 0 || gr.Percent > 100 {
			return canary.Decider{}, fmt.Errorf("route %s: percent %d outside 0..100", rt.PathPrefix, gr.Percent)
		}
		rules = append(rules, canary.Rule{
			Header:  gr.MatchHeader,
			Value:   gr.MatchValue,
			Tenant:  gr.MatchTenant,
			Percent: gr.Percent,
			Target:  registry.LabelSet(gr.Target),
		})
	}
	return canary.New(rules), nil
}

// buildPolicies instantiates one policy per configured name and rejects
// unknown names at start-up rather than silently falling back.
func buildPolicies() (map[string]lb.Policy, error) {
	policies := make(map[string]lb.Policy, 4)
	for _, name := range []string{"", lb.WeightedRoundRobin, lb.LeastConnections, lb.Hash} {
		policy, err := lb.New(name)
		if err != nil {
			return nil, err
		}
		policies[name] = policy
	}
	return policies, nil
}

func referencedServices(routes []config.Route) []string {
	seen := map[string]bool{}
	var services []string
	for _, rt := range routes {
		if rt.Service == "" || seen[rt.Service] {
			continue
		}
		seen[rt.Service] = true
		services = append(services, rt.Service)
	}
	return services
}

func checkServicesServed(routes []config.Route, reg registry.Registry) error {
	for _, rt := range routes {
		if len(reg.Resolve(rt.Service)) == 0 {
			return fmt.Errorf("route %s references service %q, which the static registry does not define",
				rt.PathPrefix, rt.Service)
		}
	}
	return nil
}

func buildDocument(cfg *config.Config) ([]byte, error) {
	server := map[string]any{
		"listen": []string{cfg.Listen.Addr},
		"routes": []any{
			map[string]any{
				"handle": []any{map[string]any{"handler": handlerName}},
			},
		},
		// The gateway is the single edge of its process: no redirect
		// listener, no ACME, no implicit hosts.
		"automatic_https": map[string]any{"disable": true},
	}

	apps := map[string]any{
		"http": map[string]any{
			"servers": map[string]any{"gfluxy": server},
		},
	}

	if cfg.Listen.TLS.CertFile != "" {
		server["protocols"] = []string{"h1", "h2", "h3"}
		server["tls_connection_policies"] = []any{map[string]any{}}
		apps["tls"] = map[string]any{
			"certificates": map[string]any{
				"load_files": []any{
					map[string]any{
						"certificate": cfg.Listen.TLS.CertFile,
						"key":         cfg.Listen.TLS.KeyFile,
					},
				},
			},
		}
	} else {
		// HTTP/3 requires TLS; plaintext listeners serve h1 and h2 only.
		server["protocols"] = []string{"h1", "h2"}
	}

	return json.Marshal(map[string]any{
		"admin": map[string]any{"disabled": true},
		"apps":  apps,
	})
}
