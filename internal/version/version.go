// Copyright 2026 The gfluxy Authors.
//
// Package version exposes build metadata injected at link time.
package version

import "fmt"

// Injected via -ldflags at build time (see Makefile).
var (
	version   = "0.0.0-dev"
	commit    = "none"
	buildDate = "unknown"
)

// String renders the human-readable build identity.
func String() string {
	return fmt.Sprintf("gfluxy %s (commit %s, built %s)", version, commit, buildDate)
}
