// Copyright 2026 The gfluxy Authors.
package route_test

import (
	"testing"

	"github.com/maxid/gfluxy/internal/route"
)

func compile(t *testing.T, routes ...route.Route) *route.Table {
	t.Helper()
	table, err := route.Compile(routes)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return table
}

func TestMatchLongestPrefixWins(t *testing.T) {
	table := compile(t,
		route.Route{PathPrefix: "/api/", StripPrefix: 1, Service: "legacy"},
		route.Route{PathPrefix: "/api/catalog/", StripPrefix: 2, Service: "catalog"},
	)

	rt, path, ok := table.Match("example.com", "/api/catalog/items")
	if !ok {
		t.Fatal("no match")
	}
	if rt.Service != "catalog" {
		t.Errorf("service = %q, want catalog", rt.Service)
	}
	if path != "/items" {
		t.Errorf("path = %q, want /items", path)
	}
}

func TestMatchAPIPrefixes(t *testing.T) {
	table := compile(t,
		route.Route{PathPrefix: "/api/catalog/", StripPrefix: 2, Service: "catalog"},
		route.Route{PathPrefix: "/api/orders/", StripPrefix: 2, Service: "orders"},
		route.Route{PathPrefix: "/api/auth/", StripPrefix: 1, Service: "auth"},
	)

	cases := []struct{ in, service, want string }{
		{"/api/catalog/items", "catalog", "/items"},
		{"/api/orders/page", "orders", "/page"},
		{"/api/auth/login", "auth", "/auth/login"},
		{"/api/catalog", "catalog", "/"},
	}
	for _, tc := range cases {
		rt, path, ok := table.Match("localhost:8443", tc.in)
		if !ok {
			t.Fatalf("%s: no match", tc.in)
		}
		if rt.Service != tc.service || path != tc.want {
			t.Errorf("%s -> (%s, %s), want (%s, %s)", tc.in, rt.Service, path, tc.service, tc.want)
		}
	}
}

func TestPrefixIsPathBoundary(t *testing.T) {
	table := compile(t, route.Route{PathPrefix: "/api/catalog/", StripPrefix: 2, Service: "catalog"})

	if _, _, ok := table.Match("localhost", "/api/catalogd/users"); ok {
		t.Error("/api/catalogd must not match /api/catalog/")
	}
	if _, _, ok := table.Match("localhost", "/api/catalog/items"); !ok {
		t.Error("/api/catalog/users must match")
	}
}

func TestHostSpecificBeatsWildcard(t *testing.T) {
	table := compile(t,
		route.Route{PathPrefix: "/api/", StripPrefix: 1, Service: "shared"},
		route.Route{Host: "tenant-a.example.com", PathPrefix: "/api/", StripPrefix: 1, Service: "tenant-a"},
	)

	if rt, _, _ := table.Match("tenant-a.example.com:8443", "/api/x"); rt.Service != "tenant-a" {
		t.Errorf("service = %q, want tenant-a", rt.Service)
	}
	if rt, _, _ := table.Match("other.example.com", "/api/x"); rt.Service != "shared" {
		t.Errorf("service = %q, want shared", rt.Service)
	}
}

func TestCompileRejectsBadRoutes(t *testing.T) {
	cases := []route.Route{
		{PathPrefix: "api/system", Service: "catalog"},
		{PathPrefix: "/api/", StripPrefix: -1, Service: "catalog"},
		{PathPrefix: "/api/"},
	}
	for _, rt := range cases {
		if _, err := route.Compile([]route.Route{rt}); err == nil {
			t.Errorf("route %+v: want error, got nil", rt)
		}
	}
}

func TestStrip(t *testing.T) {
	cases := []struct {
		path string
		n    int
		want string
	}{
		{"/api/catalog/items", 2, "/items"},
		{"/api/auth/login", 1, "/auth/login"},
		{"/api/catalog", 2, "/"},
		{"/a/b/c", 0, "/a/b/c"},
		{"/a", 3, "/"},
	}
	for _, tc := range cases {
		if got := route.Strip(tc.path, tc.n); got != tc.want {
			t.Errorf("Strip(%q, %d) = %q, want %q", tc.path, tc.n, got, tc.want)
		}
	}
}
