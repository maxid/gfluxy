// Copyright 2026 The gfluxy Authors.
//
// Package control serves the control plane: a full-document PUT that is
// validated and swapped in atomically, plus the health and status endpoints.
//
// Partial updates are deliberately absent. Gray rules are ordered, so editing
// them one by one would create intermediate states in which traffic is routed
// by a rule set nobody wrote.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/maxid/gfluxy/internal/config"
	"github.com/maxid/gfluxy/internal/server"
)

// Document is the body of PUT /v1/config.
type Document struct {
	Routes []config.Route `json:"routes" yaml:"routes"`
}

// Server is the admin listener.
type Server struct {
	addr string
	srv  *http.Server
	ln   net.Listener
}

// New builds the control plane on addr.
func New(addr string) *Server {
	s := &Server{addr: addr}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/config", s.handleConfig)
	mux.HandleFunc("/v1/validate", s.handleValidate)
	mux.HandleFunc("/v1/status", s.handleStatus)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReady)
	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

// Start binds the admin listener. It is deliberately a separate listener from
// the data plane: the control surface must never be reachable from the edge.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("admin listen %s: %w", s.addr, err)
	}
	s.ln = ln
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Printf("gfluxy: admin server: %v\n", err)
		}
	}()
	return nil
}

// Addr returns the bound address (useful when the configured port was 0).
func (s *Server) Addr() string {
	if s.ln == nil {
		return s.addr
	}
	return s.ln.Addr().String()
}

// Stop shuts the control plane down.
func (s *Server) Stop(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"version": server.Version(),
			"routes":  server.CurrentRoutes(),
		})
	case http.MethodPut:
		doc, err := decode(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		version, err := server.ApplyRoutes(doc.Routes)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"version": version})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	doc, err := decode(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := server.ValidateRoutes(doc.Routes); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "valid"})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, server.Snapshot())
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}

func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	if !server.Ready() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ready\n")
}

func decode(r *http.Request) (Document, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return Document{}, fmt.Errorf("read body: %w", err)
	}
	var doc Document
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return Document{}, fmt.Errorf("parse body: %w", err)
	}
	if len(doc.Routes) == 0 {
		return Document{}, errors.New("routes must not be empty")
	}
	return doc, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
