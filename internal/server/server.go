// Copyright 2026 The gfluxy Authors.
//
// Package server wires the data plane. Caddy owns the listener, TLS,
// ALPN/QUIC negotiation and WebSocket upgrades; gfluxy owns a single
// catch-all handler that resolves routes, gray rules and upstreams from
// an in-memory snapshot.
package server

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp"
	_ "github.com/caddyserver/caddy/v2/modules/caddytls"

	"github.com/maxid/gfluxy/internal/config"
	"github.com/maxid/gfluxy/internal/registry"
	"github.com/maxid/gfluxy/internal/registry/static"
	"github.com/maxid/gfluxy/internal/route"
)

// Server is a runnable data plane.
type Server struct {
	doc []byte
	reg registry.Registry
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
	reg, err := buildRegistry(cfg, routes)
	if err != nil {
		return nil, err
	}

	active.Store(&gateway{table: routes, reg: reg})
	return &Server{doc: doc, reg: reg}, nil
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

// Stop drains the listener and its active connections.
func (s *Server) Stop() error {
	return caddy.Stop()
}

func compileRoutes(in []config.Route) (*route.Table, error) {
	routes := make([]route.Route, 0, len(in))
	for _, rt := range in {
		routes = append(routes, route.Route{
			Host:        rt.Host,
			PathPrefix:  rt.PathPrefix,
			StripPrefix: rt.StripPrefix,
			Service:     rt.Service,
			Policy:      rt.Policy,
		})
	}
	table, err := route.Compile(routes)
	if err != nil {
		return nil, fmt.Errorf("routes: %w", err)
	}
	return table, nil
}

func buildRegistry(cfg *config.Config, table *route.Table) (registry.Registry, error) {
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
		if err := checkServicesServed(cfg.Routes, reg); err != nil {
			return nil, err
		}
		return reg, nil
	case "nacos":
		return nil, errors.New("registry.type nacos: adapter lands with the discovery milestone")
	default:
		return nil, fmt.Errorf("registry.type %q: unsupported", cfg.Registry.Type)
	}
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
