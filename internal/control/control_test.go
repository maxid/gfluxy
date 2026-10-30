// Copyright 2026 The gfluxy Authors.
package control_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/maxid/gfluxy/internal/config"
	"github.com/maxid/gfluxy/internal/control"
	"github.com/maxid/gfluxy/internal/server"
)

// TestHotUpdateAppliesAtomically proves the promise of the control plane: a
// PUT takes effect on the very next request, a rejected PUT changes nothing,
// and neither case restarts the process.
func TestHotUpdateAppliesAtomically(t *testing.T) {
	stable := newUpstream(t, "stable")
	defer stable.Close()
	canary := newUpstream(t, "canary")
	defer canary.Close()

	dataAddr := freeAddr(t)
	adminAddr := freeAddr(t)

	cfg := &config.Config{
		Listen: config.Listen{Addr: dataAddr},
		Admin:  config.Admin{Addr: adminAddr},
		Tenant: config.Tenant{Source: "host"},
		Registry: config.Registry{
			Type: "static",
			Static: config.StaticRegistry{Services: map[string][]config.Endpoint{
				"catalog": {{Addr: strings.TrimPrefix(stable.URL, "http://")}},
				"orders":  {{Addr: strings.TrimPrefix(canary.URL, "http://")}},
			}},
		},
		Routes: []config.Route{{
			PathPrefix:  "/api/catalog/",
			StripPrefix: 2,
			Service:     "catalog",
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	srv, err := server.New(cfg)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Stop()

	ctrl := control.New(adminAddr)
	if err := ctrl.Start(); err != nil {
		t.Fatalf("control start: %v", err)
	}
	defer ctrl.Stop(context.Background())

	waitReady(t, dataAddr)

	dataURL := "http://" + dataAddr + "/api/catalog/users"
	if got := upstreamOf(t, dataURL); got != "stable" {
		t.Fatalf("initial upstream = %q, want stable", got)
	}

	// Re-point the same prefix at the canary upstream.
	putConfig(t, adminAddr, `routes:
  - path_prefix: /api/catalog/
    strip_prefix: 2
    service: orders
`, http.StatusOK)

	if got := upstreamOf(t, dataURL); got != "canary" {
		t.Fatalf("after update upstream = %q, want canary", got)
	}
	if v := version(t, adminAddr); v != 2 {
		t.Fatalf("version = %d, want 2", v)
	}

	// A route to a service the registry does not know must be rejected.
	putConfig(t, adminAddr, `routes:
  - path_prefix: /api/catalog/
    strip_prefix: 2
    service: does-not-exist
`, http.StatusUnprocessableEntity)

	if got := upstreamOf(t, dataURL); got != "canary" {
		t.Fatalf("rejected update changed routing: upstream = %q", got)
	}
	if v := version(t, adminAddr); v != 2 {
		t.Fatalf("version after rejection = %d, want 2", v)
	}

	// Readiness is served by the control plane, not the data plane.
	resp, err := http.Get("http://" + adminAddr + "/readyz")
	if err != nil {
		t.Fatalf("readyz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz = %d, want 200", resp.StatusCode)
	}
}

func putConfig(t *testing.T, adminAddr, body string, wantStatus int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, "http://"+adminAddr+"/v1/config", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		var got bytes.Buffer
		_, _ = got.ReadFrom(resp.Body)
		t.Fatalf("put status = %d, want %d: %s", resp.StatusCode, wantStatus, got.String())
	}
}

func version(t *testing.T, adminAddr string) int {
	t.Helper()
	resp, err := http.Get("http://" + adminAddr + "/v1/status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	defer resp.Body.Close()

	var status struct {
		Version int `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	return status.Version
}

func upstreamOf(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer resp.Body.Close()

	var body struct {
		Upstream string `json:"upstream"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Upstream
}

func newUpstream(t *testing.T, name string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"upstream": name, "path": r.URL.Path})
	}))
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return addr
}

func waitReady(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("listener %s never became ready", addr)
}
