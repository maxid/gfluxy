// Copyright 2026 The gfluxy Authors.
//
// Package lb selects one endpoint out of a candidate set. Policies never
// mutate the endpoint list: filtering (health, gray labels) happens first, so
// a policy only has to be fair inside an already valid set.
package lb

import (
	"math"
	"sync"
	"sync/atomic"

	"github.com/maxid/gfluxy/internal/registry"
)

// Well-known policy names.
const (
	WeightedRoundRobin = "wrr"
	LeastConnections   = "least_conn"
	Hash               = "hash"
)

// Policy picks an endpoint and reports when the request is finished so that
// stateful policies (least connections) can settle.
type Policy interface {
	Pick(endpoints []registry.Endpoint, key string) (registry.Endpoint, bool)
	Release(endpoint registry.Endpoint)
}

// New builds a policy by name; an empty name means weighted round robin.
func New(name string) (Policy, error) {
	switch name {
	case "", WeightedRoundRobin:
		return &wrr{}, nil
	case LeastConnections:
		return &leastConn{active: map[string]*int64{}}, nil
	case Hash:
		return &rendezvous{}, nil
	default:
		return nil, &UnknownPolicyError{Name: name}
	}
}

// UnknownPolicyError reports a name that is not implemented.
type UnknownPolicyError struct{ Name string }

func (e *UnknownPolicyError) Error() string { return "unknown load-balancing policy " + e.Name }

// wrr is weighted round robin over the weighted slots of the candidate set.
type wrr struct{ next atomic.Uint64 }

func (w *wrr) Pick(endpoints []registry.Endpoint, _ string) (registry.Endpoint, bool) {
	total := 0
	for _, ep := range endpoints {
		total += weightOf(ep)
	}
	if total == 0 {
		return registry.Endpoint{}, false
	}
	slot := int(w.next.Add(1)-1) % total
	for _, ep := range endpoints {
		w := weightOf(ep)
		if slot < w {
			return ep, true
		}
		slot -= w
	}
	return endpoints[len(endpoints)-1], true
}

func (w *wrr) Release(registry.Endpoint) {}

// leastConn prefers the endpoint with the fewest in-flight requests.
type leastConn struct {
	mu     sync.Mutex
	active map[string]*int64
}

func (l *leastConn) Pick(endpoints []registry.Endpoint, _ string) (registry.Endpoint, bool) {
	if len(endpoints) == 0 {
		return registry.Endpoint{}, false
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	best := 0
	bestLoad := int64(math.MaxInt64)
	for i, ep := range endpoints {
		load := *l.counter(ep.Addr)
		// Prefer the heavier endpoint when loads tie.
		if load < bestLoad || (load == bestLoad && weightOf(ep) > weightOf(endpoints[best])) {
			best, bestLoad = i, load
		}
	}

	atomic.AddInt64(l.counter(endpoints[best].Addr), 1)
	return endpoints[best], true
}

func (l *leastConn) Release(ep registry.Endpoint) {
	l.mu.Lock()
	counter, ok := l.active[ep.Addr]
	l.mu.Unlock()
	if !ok {
		return
	}
	if atomic.AddInt64(counter, -1) < 0 {
		atomic.StoreInt64(counter, 0)
	}
}

func (l *leastConn) counter(addr string) *int64 {
	if c, ok := l.active[addr]; ok {
		return c
	}
	c := new(int64)
	l.active[addr] = c
	return c
}

// rendezvous hashes the session key against every endpoint and keeps the
// highest score. Adding or removing an endpoint only remaps the keys that
// pointed at it, which is what keeps a tenant on one instance across scaling.
type rendezvous struct{}

func (r *rendezvous) Pick(endpoints []registry.Endpoint, key string) (registry.Endpoint, bool) {
	if len(endpoints) == 0 {
		return registry.Endpoint{}, false
	}
	if key == "" {
		return endpoints[0], true
	}

	best := endpoints[0]
	bestScore := math.Inf(-1)
	for _, ep := range endpoints {
		h := fnv64a(key + "|" + ep.Addr)
		u := (float64(h) + 1) / float64(math.MaxUint64)
		score := -float64(weightOf(ep)) / math.Log(u)
		if score > bestScore {
			best, bestScore = ep, score
		}
	}
	return best, true
}

func (r *rendezvous) Release(registry.Endpoint) {}

func weightOf(ep registry.Endpoint) int {
	if ep.Weight <= 0 {
		return 1
	}
	return ep.Weight
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
