// Copyright 2026 The gfluxy Authors.
//
// Package canary decides which label set a request belongs to. Rules are
// evaluated in order and the first match wins. The percentage rule is a pure
// function of (route, key), so raising a percentage only ever adds users to
// the canary — it never reshuffles the ones already there.
package canary

import (
	"net/http"

	"github.com/maxid/gfluxy/internal/registry"
)

// Rule is one gray release step.
type Rule struct {
	Header  string
	Value   string
	Tenant  string
	Percent int
	Target  registry.LabelSet
}

// Decider is the ordered rule set of a single route.
type Decider struct {
	rules []Rule
}

// New builds a decider from rules, dropping rules that cannot match.
func New(rules []Rule) Decider {
	kept := make([]Rule, 0, len(rules))
	for _, r := range rules {
		if r.Header == "" && r.Tenant == "" && r.Percent == 0 {
			continue
		}
		if r.Percent > 100 {
			r.Percent = 100
		}
		kept = append(kept, r)
	}
	return Decider{rules: kept}
}

// Decide returns the target labels for a request, or nil for the stable set.
func (d Decider) Decide(route string, header http.Header, tenantCode, key string) registry.LabelSet {
	for _, rule := range d.rules {
		if !rule.matches(header, tenantCode, route, key) {
			continue
		}
		return rule.Target
	}
	return nil
}

func (r Rule) matches(header http.Header, tenantCode, route, key string) bool {
	if r.Header != "" && header.Get(r.Header) != r.Value {
		return false
	}
	if r.Tenant != "" && tenantCode != r.Tenant {
		return false
	}
	if r.Percent > 0 {
		hashKey := key
		if hashKey == "" {
			hashKey = tenantCode
		}
		if Bucket(route, hashKey) >= r.Percent*100 {
			return false
		}
	}
	return true
}

// Bucket maps a route/key pair into [0, 10000).
func Bucket(route, key string) int {
	return int(fnv64a(route+"|"+key) % 10000)
}

// Keep reports whether an endpoint may serve a request. With a target, the
// endpoint must carry every target label. Without one, the endpoint must not
// belong to any canary group: the stable set is "everything the rules do not
// claim", so operators do not have to label stable instances explicitly.
func (d Decider) Keep(endpoint registry.Endpoint, target registry.LabelSet) bool {
	if target != nil {
		return Matches(endpoint, target)
	}
	for _, rule := range d.rules {
		if len(rule.Target) > 0 && Matches(endpoint, rule.Target) {
			return false
		}
	}
	return true
}

// Matches reports whether an endpoint carries every label the target asks for.
// A nil target matches everything.
func Matches(endpoint registry.Endpoint, target registry.LabelSet) bool {
	for k, v := range target {
		if endpoint.Labels[k] != v {
			return false
		}
	}
	return true
}

const (
	offset64 = 14695981039346656037
	prime64  = 1099511628211
)

func fnv64a(s string) uint64 {
	h := uint64(offset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return h
}
