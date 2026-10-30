// Copyright 2026 The gfluxy Authors.
//
// Package config loads the static, start-up-only configuration. Everything
// that changes at runtime — routes, upstreams, gray rules — arrives through
// the control API instead, so that a rule change never needs a restart.
package config

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the on-disk start-up configuration.
type Config struct {
	Listen   Listen   `yaml:"listen"`
	Tenant   Tenant   `yaml:"tenant"`
	Registry Registry `yaml:"registry"`
}

// Listen describes the single public listener.
type Listen struct {
	Addr string `yaml:"addr"`
	TLS  TLS    `yaml:"tls"`
}

// TLS points at a static certificate pair. Empty fields mean plaintext.
type TLS struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// Tenant controls how the gray-release tenant dimension is resolved.
type Tenant struct {
	Source     string            `yaml:"source"` // host | header
	Header     string            `yaml:"header"`
	BaseDomain string            `yaml:"base_domain"`
	Exact      map[string]string `yaml:"exact"`
	Cookie     string            `yaml:"cookie"`
}

// Registry selects where upstream instances come from.
type Registry struct {
	Type  string `yaml:"type"` // static | nacos
	Path  string `yaml:"path"`
	Nacos Nacos  `yaml:"nacos"`
}

// Nacos holds the Nacos discovery settings.
type Nacos struct {
	Servers   []string `yaml:"servers"`
	Namespace string   `yaml:"namespace"`
	Group     string   `yaml:"group"`
	Username  string   `yaml:"username"`
	Password  string   `yaml:"password"`
}

// Load reads and validates a configuration file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Tenant.Source == "" {
		c.Tenant.Source = "host"
	}
	if c.Tenant.Header == "" {
		c.Tenant.Header = "X-Tenant-Id"
	}
	if c.Registry.Type == "" {
		c.Registry.Type = "static"
	}
}

// Validate rejects configurations the server cannot honour.
func (c *Config) Validate() error {
	if c.Listen.Addr == "" {
		return errors.New("listen.addr is required")
	}
	if (c.Listen.TLS.CertFile == "") != (c.Listen.TLS.KeyFile == "") {
		return errors.New("listen.tls requires both cert_file and key_file")
	}
	switch c.Tenant.Source {
	case "host", "header":
	default:
		return fmt.Errorf("tenant.source %q: want host or header", c.Tenant.Source)
	}
	switch c.Registry.Type {
	case "static":
	case "nacos":
		if len(c.Registry.Nacos.Servers) == 0 {
			return errors.New("registry.nacos.servers is required")
		}
	default:
		return fmt.Errorf("registry.type %q: want static or nacos", c.Registry.Type)
	}
	return nil
}
