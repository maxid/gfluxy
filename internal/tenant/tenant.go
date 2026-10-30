// Copyright 2026 The gfluxy Authors.
//
// Package tenant derives the tenant code from the request. XDMP resolves the
// tenant from the token; a gateway that does not authenticate cannot, so the
// host name is the only trustworthy source here. The `X-Tenant-Id` header is
// passed through untouched but never used for routing decisions: only root
// operators may send it, and deciding that needs the token.
package tenant

import (
	"net"
	"net/http"
	"strings"
)

// Source values.
const (
	SourceHost   = "host"
	SourceHeader = "header"
)

// Resolver turns a request into a tenant code.
type Resolver struct {
	Source     string
	Header     string
	BaseDomain string
	Exact      map[string]string
}

// Resolve returns the tenant code, or "" when the request cannot be mapped.
func (r Resolver) Resolve(host string, header http.Header) string {
	switch r.Source {
	case SourceHeader:
		if r.Header == "" {
			return ""
		}
		return header.Get(r.Header)
	default:
		return r.fromHost(host)
	}
}

func (r Resolver) fromHost(host string) string {
	host = strings.ToLower(stripPort(host))
	if host == "" {
		return ""
	}
	if code, ok := r.Exact[host]; ok {
		return code
	}
	if r.BaseDomain != "" {
		suffix := "." + strings.ToLower(strings.TrimPrefix(r.BaseDomain, "."))
		if strings.HasSuffix(host, suffix) {
			return strings.TrimSuffix(host, suffix)
		}
		if host == strings.TrimPrefix(suffix, ".") {
			return ""
		}
	}
	return ""
}

func stripPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}
