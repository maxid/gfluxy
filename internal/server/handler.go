// Copyright 2026 The gfluxy Authors.
package server

import (
	"encoding/json"
	"net/http"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

const handlerName = "gfluxy"

// Handler is the single catch-all HTTP handler. Until the router lands it
// echoes what the client negotiated, which is what the single-port tests
// assert on.
type Handler struct{}

// CaddyModule registers the handler under http.handlers.gfluxy.
func (Handler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers." + handlerName,
		New: func() caddy.Module { return new(Handler) },
	}
}

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, _ caddyhttp.Handler) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(map[string]string{
		"proto": r.Proto,
		"host":  r.Host,
		"path":  r.URL.Path,
	})
}

func init() { caddy.RegisterModule(Handler{}) }
