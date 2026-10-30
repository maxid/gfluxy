// Copyright 2026 The gfluxy Authors.
package server_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/net/http2"

	"github.com/maxid/gfluxy/internal/config"
	"github.com/maxid/gfluxy/internal/server"
)

// TestDataPlane drives the whole request path against a real listener: one
// address serves h1, h2 and h3, `/api/system/*` is stripped before reaching
// the upstream, WebSocket upgrades tunnel, and unknown paths 404.
//
// Caddy keeps process-global state, so everything lives in one test.
func TestDataPlane(t *testing.T) {
	upstream := newUpstream(t)
	defer upstream.Close()

	certFile, keyFile := selfSigned(t)
	addr := freeAddr(t)

	cfg := &config.Config{
		Listen: config.Listen{Addr: addr, TLS: config.TLS{CertFile: certFile, KeyFile: keyFile}},
		Tenant: config.Tenant{Source: "host"},
		Registry: config.Registry{
			Type: "static",
			Static: config.StaticRegistry{Services: map[string][]config.Endpoint{
				"admin": {{Addr: strings.TrimPrefix(upstream.URL, "http://")}},
			}},
		},
		Routes: []config.Route{{
			PathPrefix:  "/api/system/",
			StripPrefix: 2,
			Service:     "admin",
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
	defer func() {
		if err := srv.Stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	}()
	waitReady(t, addr)

	tlsCfg := &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"} //nolint:gosec // self-signed test cert
	url := "https://" + addr + "/api/system/users"

	protocols := []struct {
		name string
		rt   http.RoundTripper
		want string
	}{
		{"h1", &http.Transport{TLSClientConfig: tlsCfg, ForceAttemptHTTP2: false}, "HTTP/1.1"},
		{"h2", &http2.Transport{TLSClientConfig: tlsCfg}, "HTTP/2.0"},
		{"h3", &http3.Transport{TLSClientConfig: tlsCfg}, "HTTP/3.0"},
	}
	for _, tc := range protocols {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: tc.rt, Timeout: 5 * time.Second}
			resp, err := client.Get(url)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, body %s", resp.StatusCode, body)
			}

			var got map[string]string
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("decode %s: %v", body, err)
			}
			// The upstream is HTTP/1.1 by design; the negotiated client
			// protocol is what the client transport reports.
			if resp.Proto != tc.want {
				t.Errorf("client negotiated %q, want %q", resp.Proto, tc.want)
			}
			if got["path"] != "/users" {
				t.Errorf("upstream path = %q, want /users", got["path"])
			}
			if got["route"] != "admin:/api/system/" {
				t.Errorf("route header = %q, want admin:/api/system/", got["route"])
			}
		})
	}

	t.Run("websocket", func(t *testing.T) {
		dialer := websocket.Dialer{
			TLSClientConfig:  tlsCfg,
			HandshakeTimeout: 5 * time.Second,
		}
		conn, _, err := dialer.Dial("wss://"+addr+"/api/system/ws", nil)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()

		if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("deadline: %v", err)
		}
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(msg) != "echo:ping" {
			t.Errorf("message = %q, want echo:ping", msg)
		}
	})

	t.Run("unknown path", func(t *testing.T) {
		resp, err := (&http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}).Get("https://" + addr + "/nope")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
	})
}

func newUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			for {
				mt, msg, err := conn.ReadMessage()
				if err != nil {
					return
				}
				if err := conn.WriteMessage(mt, append([]byte("echo:"), msg...)); err != nil {
					return
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"proto": r.Proto,
			"path":  r.URL.Path,
			"route": r.Header.Get("X-Gfluxy-Route"),
		})
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
