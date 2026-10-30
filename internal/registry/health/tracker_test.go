// Copyright 2026 The gfluxy Authors.
package health

import (
	"testing"
	"time"
)

func TestEjectsAfterThreshold(t *testing.T) {
	now := time.Unix(0, 0)
	tr := newWithClock(3, 30*time.Second, func() time.Time { return now })

	for i := 0; i < 2; i++ {
		tr.Fail("a:1")
		if !tr.Available("a:1") {
			t.Fatalf("ejected after %d failures, want still available", i+1)
		}
	}
	tr.Fail("a:1")
	if tr.Available("a:1") {
		t.Fatal("still available after threshold failures")
	}
}

func TestCooldownThenHalfOpen(t *testing.T) {
	now := time.Unix(0, 0)
	tr := newWithClock(1, 10*time.Second, func() time.Time { return now })

	tr.Fail("a:1")
	if tr.Available("a:1") {
		t.Fatal("available during cooldown")
	}

	now = now.Add(11 * time.Second)
	if !tr.Available("a:1") {
		t.Fatal("should be half-open after cooldown")
	}

	tr.Fail("a:1")
	if tr.Available("a:1") {
		t.Fatal("failed probe must eject again")
	}
}

func TestSuccessRestores(t *testing.T) {
	now := time.Unix(0, 0)
	tr := newWithClock(1, time.Minute, func() time.Time { return now })

	tr.Fail("a:1")
	tr.Succeed("a:1")
	if !tr.Available("a:1") {
		t.Fatal("success must restore the endpoint")
	}
}
