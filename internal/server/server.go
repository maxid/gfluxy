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

	"github.com/caddyserver/caddy/v2"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp"
	_ "github.com/caddyserver/caddy/v2/modules/caddytls"

	"github.com/maxid/gfluxy/internal/config"
)

// Server is a runnable data plane.
type Server struct {
	doc []byte
}

// New compiles the static configuration into a Caddy config document.
func New(cfg *config.Config) (*Server, error) {
	doc, err := buildDocument(cfg)
	if err != nil {
		return nil, err
	}
	return &Server{doc: doc}, nil
}

// Start loads the configuration and begins serving. It returns once the
// listener is bound; serving continues in the background (Caddy owns it).
func (s *Server) Start() error {
	// The gateway is configured by the control plane, not by a Caddyfile on
	// disk: leaving an autosave artefact behind would be a second source of
	// truth that never gets applied.
	caddy.ConfigAutosavePath = ""

	if err := caddy.Load(s.doc, true); err != nil {
		return fmt.Errorf("start server: %w", err)
	}
	return nil
}

// Stop drains the listener and its active connections.
func (s *Server) Stop() error {
	return caddy.Stop()
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
