// Copyright 2026 The gfluxy Authors.
//
// Package route compiles the routing table and rewrites request paths. The
// gateway must accept the frozen `/api/*` prefixes while backends keep their
// bare paths, so stripping is part of the route, not of a middleware chain.
package route

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
)

// Route maps an incoming request to a service.
type Route struct {
	Host        string
	PathPrefix  string
	StripPrefix int
	Service     string
	Policy      string
}

// Table is an immutable, pre-sorted routing table.
type Table struct {
	byHost map[string][]Route
	any    []Route
}

// Compile validates routes and orders them so that the most specific match
// wins: host-specific before wildcard, then longest prefix.
func Compile(routes []Route) (*Table, error) {
	table := &Table{byHost: map[string][]Route{}}
	for _, rt := range routes {
		if rt.PathPrefix == "" || !strings.HasPrefix(rt.PathPrefix, "/") {
			return nil, fmt.Errorf("route %q: path_prefix must start with /", rt.PathPrefix)
		}
		if rt.StripPrefix < 0 {
			return nil, fmt.Errorf("route %q: strip_prefix must not be negative", rt.PathPrefix)
		}
		if rt.Service == "" {
			return nil, fmt.Errorf("route %q: service is required", rt.PathPrefix)
		}
		if rt.Host != "" {
			host := strings.ToLower(rt.Host)
			table.byHost[host] = append(table.byHost[host], rt)
			continue
		}
		table.any = append(table.any, rt)
	}

	for _, list := range table.byHost {
		sortRoutes(list)
	}
	sortRoutes(table.any)

	for _, rt := range routes {
		if err := table.checkOverlap(rt); err != nil {
			return nil, err
		}
	}
	return table, nil
}

// Match returns the route for a host/path pair and the path to send upstream.
func (t *Table) Match(host, path string) (Route, string, bool) {
	if t == nil {
		return Route{}, "", false
	}
	host = stripPort(strings.ToLower(host))
	if list, ok := t.byHost[host]; ok {
		if rt, ok := firstMatch(list, path); ok {
			return rt, Strip(path, rt.StripPrefix), true
		}
	}
	if rt, ok := firstMatch(t.any, path); ok {
		return rt, Strip(path, rt.StripPrefix), true
	}
	return Route{}, "", false
}

// Strip removes the first n path segments and always returns a rooted path.
func Strip(path string, n int) string {
	if n <= 0 {
		return path
	}
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if n >= len(segments) {
		return "/"
	}
	return "/" + strings.Join(segments[n:], "/")
}

func firstMatch(routes []Route, path string) (Route, bool) {
	for _, rt := range routes {
		if HasPrefix(path, rt.PathPrefix) {
			return rt, true
		}
	}
	return Route{}, false
}

// HasPrefix treats the prefix as a path boundary: /api/system/ matches
// /api/system/users but not /api/systemd, and matches /api/system itself.
func HasPrefix(path, prefix string) bool {
	if prefix == "/" {
		return true
	}
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	if strings.HasSuffix(prefix, "/") || len(path) == len(prefix) {
		return true
	}
	return path[len(prefix)] == '/'
}

func sortRoutes(routes []Route) {
	sort.SliceStable(routes, func(i, j int) bool {
		return len(routes[i].PathPrefix) > len(routes[j].PathPrefix)
	})
}

func (t *Table) checkOverlap(candidate Route) error {
	if candidate.Host == "" {
		return nil
	}
	for _, existing := range t.any {
		if existing.PathPrefix == candidate.PathPrefix {
			return errors.New("host-specific route " + candidate.Host + candidate.PathPrefix +
				" shadows wildcard route " + existing.PathPrefix)
		}
	}
	return nil
}

func stripPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}
