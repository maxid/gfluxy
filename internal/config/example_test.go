// Copyright 2026 The gfluxy Authors.
package config_test

import (
	"testing"

	"github.com/maxid/gfluxy/internal/config"
)

// TestExampleConfigStaysValid keeps the shipped example honest: it is what
// operators copy, so it must parse and pass validation on every commit.
func TestExampleConfigStaysValid(t *testing.T) {
	cfg, err := config.Load("../../examples/gfluxy.yaml")
	if err != nil {
		t.Fatalf("examples/gfluxy.yaml: %v", err)
	}
	if len(cfg.Routes) == 0 {
		t.Fatal("example has no routes")
	}
	if len(cfg.Registry.Static.Services) == 0 {
		t.Fatal("example has no static services")
	}
}
