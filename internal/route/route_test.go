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
		route.Route{PathPrefix: "/api/system/", StripPrefix: 2, Service: "admin"},
	)

	rt, path, ok := table.Match("example.com", "/api/system/users")
	if !ok {
		t.Fatal("no match")
	}
	if rt.Service != "admin" {
		t.Errorf("service = %q, want admin", rt.Service)
	}
	if path != "/users" {
		t.Errorf("path = %q, want /users", path)
	}
}

func TestMatchFrozenPrefixes(t *testing.T) {
	table := compile(t,
		route.Route{PathPrefix: "/api/system/", StripPrefix: 2, Service: "admin"},
		route.Route{PathPrefix: "/api/iot/", StripPrefix: 2, Service: "iot"},
		route.Route{PathPrefix: "/api/passport/", StripPrefix: 1, Service: "iam"},
	)

	cases := []struct{ in, service, want string }{
		{"/api/system/users", "admin", "/users"},
		{"/api/iot/devices/page", "iot", "/devices/page"},
		{"/api/passport/login", "iam", "/passport/login"},
		{"/api/system", "admin", "/"},
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
	table := compile(t, route.Route{PathPrefix: "/api/system/", StripPrefix: 2, Service: "admin"})

	if _, _, ok := table.Match("localhost", "/api/systemd/users"); ok {
		t.Error("/api/systemd must not match /api/system/")
	}
	if _, _, ok := table.Match("localhost", "/api/system/users"); !ok {
		t.Error("/api/system/users must match")
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
		{PathPrefix: "api/system", Service: "admin"},
		{PathPrefix: "/api/", StripPrefix: -1, Service: "admin"},
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
		{"/api/system/users", 2, "/users"},
		{"/api/passport/login", 1, "/passport/login"},
		{"/api/system", 2, "/"},
		{"/a/b/c", 0, "/a/b/c"},
		{"/a", 3, "/"},
	}
	for _, tc := range cases {
		if got := route.Strip(tc.path, tc.n); got != tc.want {
			t.Errorf("Strip(%q, %d) = %q, want %q", tc.path, tc.n, got, tc.want)
		}
	}
}
