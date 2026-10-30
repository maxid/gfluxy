// Copyright 2026 The gfluxy Authors.
package canary_test

import (
	"net/http"
	"testing"

	"github.com/maxid/gfluxy/internal/canary"
	"github.com/maxid/gfluxy/internal/registry"
)

func TestHeaderRuleWins(t *testing.T) {
	d := canary.New([]canary.Rule{
		{Header: "X-Gray", Value: "1", Target: registry.LabelSet{registry.LabelVersion: "1.1.0"}},
		{Percent: 100, Target: registry.LabelSet{registry.LabelVersion: "0.9.0"}},
	})

	h := http.Header{}
	h.Set("X-Gray", "1")
	if got := d.Decide("/api/catalog/", h, "", "1.2.3.4"); got[registry.LabelVersion] != "1.1.0" {
		t.Fatalf("version = %q, want 1.1.0", got[registry.LabelVersion])
	}
	if got := d.Decide("/api/catalog/", http.Header{}, "", "1.2.3.4"); got[registry.LabelVersion] != "0.9.0" {
		t.Fatalf("version = %q, want fallthrough 0.9.0", got[registry.LabelVersion])
	}
}

func TestTenantRule(t *testing.T) {
	d := canary.New([]canary.Rule{
		{Tenant: "tenant-a", Target: registry.LabelSet{registry.LabelColor: "canary"}},
	})

	if got := d.Decide("/api/orders/", nil, "tenant-a", ""); got[registry.LabelColor] != "canary" {
		t.Fatalf("labels = %v, want canary", got)
	}
	if got := d.Decide("/api/orders/", nil, "tenant-b", ""); got != nil {
		t.Fatalf("labels = %v, want stable", got)
	}
}

func TestPercentIsDeterministicAndMonotonic(t *testing.T) {
	ten := canary.New([]canary.Rule{{Percent: 10, Target: registry.LabelSet{registry.LabelVersion: "1.1.0"}}})
	thirty := canary.New([]canary.Rule{{Percent: 30, Target: registry.LabelSet{registry.LabelVersion: "1.1.0"}}})

	inTen := 0
	for i := 0; i < 5000; i++ {
		key := string(rune('a'+i%26)) + string(rune('0'+i%10)) + string(rune('a'+i/26%26))
		first := ten.Decide("/api/catalog/", nil, "", key)
		second := ten.Decide("/api/catalog/", nil, "", key)
		if (first == nil) != (second == nil) {
			t.Fatalf("key %q is not deterministic", key)
		}
		if first == nil {
			continue
		}
		inTen++
		if thirty.Decide("/api/catalog/", nil, "", key) == nil {
			t.Fatalf("key %q is in 10%% but not in 30%% — raising a percentage must only add users", key)
		}
	}
	if inTen == 0 {
		t.Fatal("10%% never matched")
	}
}

func TestPercentDistribution(t *testing.T) {
	d := canary.New([]canary.Rule{{Percent: 20, Target: registry.LabelSet{registry.LabelVersion: "1.1.0"}}})

	const total = 20000
	hit := 0
	for i := 0; i < total; i++ {
		key := itoa(i)
		if d.Decide("/api/catalog/", nil, "", key) != nil {
			hit++
		}
	}
	ratio := float64(hit) / total
	if ratio < 0.18 || ratio > 0.22 {
		t.Fatalf("ratio = %.3f, want 0.20±0.02", ratio)
	}
}

func TestMatchesFiltersByLabel(t *testing.T) {
	ep := registry.Endpoint{
		Addr:   "10.0.0.1:8080",
		Labels: registry.LabelSet{registry.LabelVersion: "1.1.0", registry.LabelColor: "canary"},
	}
	if !canary.Matches(ep, registry.LabelSet{registry.LabelVersion: "1.1.0"}) {
		t.Error("version match should hold")
	}
	if canary.Matches(ep, registry.LabelSet{registry.LabelVersion: "1.0.0"}) {
		t.Error("version mismatch must not match")
	}
	if !canary.Matches(ep, nil) {
		t.Error("nil target matches everything")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [8]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
