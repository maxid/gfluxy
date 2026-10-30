// Copyright 2026 The gfluxy Authors.
package server_test

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/maxid/gfluxy/internal/config"
	"github.com/maxid/gfluxy/internal/server"
)

// TestWebSocketTunnelKeepsNegotiation pins the decision to tunnel WebSocket
// instead of terminating it. A terminating proxy would have to re-negotiate
// subprotocol and compression on each leg; a tunnel relays the handshake, so
// both must arrive at the upstream exactly as the client sent them.
func TestWebSocketTunnelKeepsNegotiation(t *testing.T) {
	extensions := make(chan string, 1)
	closed := make(chan int, 1)

	upgrader := websocket.Upgrader{EnableCompression: true, Subprotocols: []string{"mqtt"}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		extensions <- r.Header.Get("Sec-WebSocket-Extensions")

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		for {
			mt, msg, err := conn.ReadMessage()
			if err != nil {
				var closeErr *websocket.CloseError
				if errors.As(err, &closeErr) {
					closed <- closeErr.Code
				}
				return
			}
			if err := conn.WriteMessage(mt, msg); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()

	certFile, keyFile := selfSigned(t)
	addr := freeAddr(t)
	cfg := &config.Config{
		Listen: config.Listen{Addr: addr, TLS: config.TLS{CertFile: certFile, KeyFile: keyFile}},
		Admin:  config.Admin{Addr: freeAddr(t)},
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
	defer srv.Stop()
	waitReady(t, addr)

	dialer := websocket.Dialer{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"}, //nolint:gosec // self-signed test cert
		Subprotocols:      []string{"mqtt"},
		EnableCompression: true,
		HandshakeTimeout:  5 * time.Second,
	}
	conn, resp, err := dialer.Dial("wss://"+addr+"/api/system/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if got := conn.Subprotocol(); got != "mqtt" {
		t.Errorf("subprotocol = %q, want mqtt", got)
	}
	if got := resp.Header.Get("Sec-WebSocket-Extensions"); !strings.Contains(got, "permessage-deflate") {
		t.Errorf("extension header = %q, want permessage-deflate", got)
	}
	select {
	case got := <-extensions:
		if !strings.Contains(got, "permessage-deflate") {
			t.Errorf("upstream saw extensions %q, want permessage-deflate", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream never reported the extension header")
	}

	if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	if _, msg, err := conn.ReadMessage(); err != nil || string(msg) != "ping" {
		t.Fatalf("echo = %q, %v", msg, err)
	}

	if err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "bye")); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case code := <-closed:
		if code != 4001 {
			t.Errorf("close code = %d, want 4001", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never saw the close frame")
	}
}
