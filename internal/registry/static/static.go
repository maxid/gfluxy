// Copyright 2026 The gfluxy Authors.
//
// Package static serves a fixed upstream table, either from the start-up
// configuration or from a file that is re-read by the control plane.
package static

import (
	"github.com/maxid/gfluxy/internal/registry"
)

// Registry is an immutable service table.
type Registry struct {
	services map[string][]registry.Endpoint
}

// New builds a registry from a service table, defaulting weights to 1 and
// marking every endpoint healthy (the static table has no health signal).
func New(services map[string][]registry.Endpoint) *Registry {
	table := make(map[string][]registry.Endpoint, len(services))
	for name, endpoints := range services {
		copied := make([]registry.Endpoint, 0, len(endpoints))
		for _, ep := range endpoints {
			if ep.Weight <= 0 {
				ep.Weight = 1
			}
			ep.Healthy = true
			copied = append(copied, ep)
		}
		table[name] = copied
	}
	return &Registry{services: table}
}

// Resolve implements registry.Registry.
func (r *Registry) Resolve(service string) []registry.Endpoint {
	endpoints := r.services[service]
	out := make([]registry.Endpoint, len(endpoints))
	copy(out, endpoints)
	return out
}
