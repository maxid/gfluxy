// Copyright 2026 The gfluxy Authors.
package lb_test

import (
	"testing"

	"github.com/maxid/gfluxy/internal/lb"
	"github.com/maxid/gfluxy/internal/registry"
)

func endpoints(weights ...int) []registry.Endpoint {
	out := make([]registry.Endpoint, 0, len(weights))
	for i, w := range weights {
		out = append(out, registry.Endpoint{Addr: string(rune('a'+i)) + ":80", Weight: w, Healthy: true})
	}
	return out
}

func TestWeightedRoundRobinHonoursWeights(t *testing.T) {
	policy, err := lb.New("wrr")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	eps := endpoints(1, 3)

	counts := map[string]int{}
	for i := 0; i < 4000; i++ {
		ep, ok := policy.Pick(eps, "")
		if !ok {
			t.Fatal("no endpoint picked")
		}
		counts[ep.Addr]++
	}
	if counts["a:80"] != 1000 || counts["b:80"] != 3000 {
		t.Fatalf("counts = %v, want 1000/3000", counts)
	}
}

func TestLeastConnectionsTracksInFlight(t *testing.T) {
	policy, _ := lb.New("least_conn")
	eps := endpoints(1, 1)

	first, _ := policy.Pick(eps, "")
	second, _ := policy.Pick(eps, "")
	if first.Addr == second.Addr {
		t.Fatalf("both picks hit %s; second should avoid the loaded one", first.Addr)
	}

	policy.Release(first)
	third, _ := policy.Pick(eps, "")
	if third.Addr != first.Addr {
		t.Errorf("after release picked %s, want %s", third.Addr, first.Addr)
	}
}

func TestHashIsStickyAndMinimallyDisruptive(t *testing.T) {
	policy, _ := lb.New("hash")
	eps := endpoints(1, 1, 1)

	before := map[string]string{}
	for i := 0; i < 300; i++ {
		key := string(rune('a' + i%26))
		ep, _ := policy.Pick(eps, key)
		if prev, ok := before[key]; ok && prev != ep.Addr {
			t.Fatalf("key %q moved from %s to %s", key, prev, ep.Addr)
		}
		before[key] = ep.Addr
	}

	// Removing one endpoint must only remap the keys that pointed at it.
	removed := eps[1]
	shrunken := []registry.Endpoint{eps[0], eps[2]}
	moved := 0
	for key, addr := range before {
		ep, _ := policy.Pick(shrunken, key)
		if addr != ep.Addr {
			if addr != removed.Addr {
				t.Fatalf("key %q moved although its endpoint stayed: %s -> %s", key, addr, ep.Addr)
			}
			moved++
		}
	}
	if moved == 0 {
		t.Skip("no key hashed to the removed endpoint")
	}
}

func TestUnknownPolicy(t *testing.T) {
	if _, err := lb.New("random"); err == nil {
		t.Fatal("want error for unknown policy")
	}
}
