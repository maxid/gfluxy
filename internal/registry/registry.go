// Copyright 2026 The gfluxy Authors.
//
// Package registry is the upstream index: it answers "which instances serve
// this service right now". Registries only provide instances and health; the
// release state (which label is the canary) belongs to the control plane.
package registry

// LabelSet carries the only upstream metadata the gateway reasons about.
// Registration metadata from a concrete registry is normalised into these
// keys by its adapter, so gray rules never mention a registry-specific field.
type LabelSet map[string]string

// Well-known label keys.
const (
	LabelVersion = "version"
	LabelColor   = "color"
	LabelGroup   = "group"
)

// Endpoint is one instance of a service.
type Endpoint struct {
	Addr    string
	Weight  int
	Labels  LabelSet
	Healthy bool
}

// Registry resolves service names to instances.
type Registry interface {
	Resolve(service string) []Endpoint
}

// StatusReporter is implemented by registries that can lose contact with their
// backend while still serving the last good instance list.
type StatusReporter interface {
	Status() error
}
