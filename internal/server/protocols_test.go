// Copyright 2026 The gfluxy Authors.
package server_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
	"golang.org/x/net/http2"

	"github.com/maxid/gfluxy/internal/config"
	"github.com/maxid/gfluxy/internal/server"
)

// TestSinglePortProtocols asserts the core promise of the product: one
// address serves h1, h2 and h3. Caddy keeps global state, so this runs as a
// single test rather than parallel cases.
func TestSinglePortProtocols(t *testing.T) {
	certFile, keyFile := selfSigned(t)
	addr := freeAddr(t)

	cfg := &config.Config{
		Listen: config.Listen{Addr: addr, TLS: config.TLS{CertFile: certFile, KeyFile: keyFile}},
		Tenant: config.Tenant{Source: "host"},
	}
	cfg.Registry.Type = "static"
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
	defer func() {
		if err := srv.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	}()
	waitReady(t, addr)

	base := "https://" + addr + "/api/system/users"
	tlsCfg := &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"} //nolint:gosec // self-signed test cert

	cases := []struct {
		name string
		rt   http.RoundTripper
		want string
	}{
		{"h1", &http.Transport{TLSClientConfig: tlsCfg, ForceAttemptHTTP2: false}, "HTTP/1.1"},
		{"h2", &http2.Transport{TLSClientConfig: tlsCfg}, "HTTP/2.0"},
		{"h3", &http3.Transport{TLSClientConfig: tlsCfg}, "HTTP/3.0"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: tc.rt, Timeout: 5 * time.Second}
			resp, err := client.Get(base)
			if err != nil {
				t.Fatalf("%s request: %v", tc.name, err)
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("%s read: %v", tc.name, err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s status = %d, body %s", tc.name, resp.StatusCode, body)
			}
			if !strings.Contains(string(body), `"proto":"`+tc.want+`"`) {
				t.Fatalf("%s negotiated %s, want %s", tc.name, body, tc.want)
			}
		})
	}
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

func selfSigned(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"localhost", "gfluxy.test"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	dir := t.TempDir()
	certFile = filepath.Join(dir, "tls.crt")
	keyFile = filepath.Join(dir, "tls.key")
	writePEM(t, certFile, "CERTIFICATE", der)
	writePEM(t, keyFile, "EC PRIVATE KEY", keyDER)
	return certFile, keyFile
}

func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
}
