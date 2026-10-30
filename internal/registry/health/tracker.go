// Copyright 2026 The gfluxy Authors.
//
// Package health ejects upstreams that keep failing at the transport level and
// lets them back in after a cooldown, one probe at a time.
package health

import (
	"sync"
	"time"
)

// Tracker keeps per-address failure state.
type Tracker struct {
	threshold int
	cooldown  time.Duration
	now       func() time.Time

	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	fails   int
	until   time.Time
	ejected bool
}

// New builds a tracker that ejects after `threshold` consecutive transport
// failures and retries after `cooldown`.
func New(threshold int, cooldown time.Duration) *Tracker {
	return newWithClock(threshold, cooldown, time.Now)
}

func newWithClock(threshold int, cooldown time.Duration, now func() time.Time) *Tracker {
	if threshold < 1 {
		threshold = 1
	}
	return &Tracker{
		threshold: threshold,
		cooldown:  cooldown,
		now:       now,
		entries:   map[string]*entry{},
	}
}

// Available reports whether the address may receive traffic right now.
func (t *Tracker) Available(addr string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	e, ok := t.entries[addr]
	if !ok {
		return true
	}
	return !t.now().Before(e.until)
}

// Fail records one transport failure.
func (t *Tracker) Fail(addr string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	e, ok := t.entries[addr]
	if !ok {
		e = &entry{}
		t.entries[addr] = e
	}
	if now.Before(e.until) {
		return // already ejected; ignore late failures from in-flight requests
	}
	if e.ejected {
		// The cooldown expired and the probe failed: keep it out.
		e.until = now.Add(t.cooldown)
		return
	}
	e.fails++
	if e.fails >= t.threshold {
		e.ejected = true
		e.fails = 0
		e.until = now.Add(t.cooldown)
	}
}

// Succeed clears the failure state.
func (t *Tracker) Succeed(addr string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, addr)
}
