// Copyright 2026 The gfluxy Authors.
package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxid/gfluxy/internal/config"
)

func TestLoadAppliesDefaults(t *testing.T) {
	path := writeConfig(t, "listen:\n  addr: \":8443\"\n")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Tenant.Source != "host" {
		t.Errorf("tenant.source = %q, want host", cfg.Tenant.Source)
	}
	if cfg.Tenant.Header != "X-Tenant-Id" {
		t.Errorf("tenant.header = %q, want X-Tenant-Id", cfg.Tenant.Header)
	}
	if cfg.Registry.Type != "static" {
		t.Errorf("registry.type = %q, want static", cfg.Registry.Type)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "missing listen addr",
			yaml:    "tenant:\n  source: host\n",
			wantErr: "listen.addr",
		},
		{
			name:    "half a certificate pair",
			yaml:    "listen:\n  addr: \":8443\"\n  tls:\n    cert_file: /tmp/a.crt\n",
			wantErr: "both cert_file and key_file",
		},
		{
			name:    "unknown tenant source",
			yaml:    "listen:\n  addr: \":8443\"\ntenant:\n  source: token\n",
			wantErr: "tenant.source",
		},
		{
			name:    "nacos without servers",
			yaml:    "listen:\n  addr: \":8443\"\nregistry:\n  type: nacos\n",
			wantErr: "servers",
		},
		{
			name: "valid nacos",
			yaml: "listen:\n  addr: \":8443\"\nregistry:\n  type: nacos\n  nacos:\n    servers: [\"127.0.0.1:8848\"]\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(writeConfig(t, tc.yaml))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gfluxy.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
