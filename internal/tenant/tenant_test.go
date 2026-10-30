// Copyright 2026 The gfluxy Authors.
package tenant_test

import (
	"net/http"
	"testing"

	"github.com/maxid/gfluxy/internal/tenant"
)

func TestResolveFromHost(t *testing.T) {
	r := tenant.Resolver{
		Source:     tenant.SourceHost,
		BaseDomain: "example.com",
		Exact:      map[string]string{"admin.example.com": "management"},
	}

	cases := []struct{ host, want string }{
		{"admin.example.com", "management"},
		{"admin.example.com:8443", "management"},
		{"tenant-a.example.com", "tenant-a"},
		{"TENANT-B.EXAMPLE.COM", "tenant-b"},
		{"example.com", ""},
		{"other.org", ""},
	}
	for _, tc := range cases {
		if got := r.Resolve(tc.host, nil); got != tc.want {
			t.Errorf("Resolve(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
}

func TestResolveFromHeader(t *testing.T) {
	r := tenant.Resolver{Source: tenant.SourceHeader, Header: "X-Tenant-Id"}
	h := http.Header{}
	h.Set("X-Tenant-Id", "tenant-a")

	if got := r.Resolve("anything.example.com", h); got != "tenant-a" {
		t.Errorf("tenant = %q, want tenant-a", got)
	}
}

// The pass-through header must not leak into host-based decisions.
func TestHostSourceIgnoresTenantHeader(t *testing.T) {
	r := tenant.Resolver{Source: tenant.SourceHost, BaseDomain: "example.com"}
	h := http.Header{}
	h.Set("X-Tenant-Id", "spoofed")

	if got := r.Resolve("tenant-a.example.com", h); got != "tenant-a" {
		t.Errorf("tenant = %q, want tenant-a (header must be ignored)", got)
	}
}
