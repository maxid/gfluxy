// Copyright 2026 The gfluxy Authors.
//
// Package obs exposes the two things an operator needs from a gateway:
// Prometheus counters and one structured line per request.
package obs

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// Requests counts proxied requests per route and status code.
	Requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gfluxy_requests_total",
		Help: "Requests proxied, by route, service and status code.",
	}, []string{"route", "service", "code"})

	// UpstreamFailures counts transport failures per upstream instance.
	UpstreamFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gfluxy_upstream_failures_total",
		Help: "Upstream transport failures, by service and instance.",
	}, []string{"service", "upstream"})

	// ConfigVersion tracks the applied routing document.
	ConfigVersion = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "gfluxy_config_version",
		Help: "Version of the routing document currently in force.",
	})

	// GrayHits counts how many requests a gray rule routed away from stable.
	GrayHits = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gfluxy_gray_requests_total",
		Help: "Requests routed to a canary group, by route and labels.",
	}, []string{"route", "gray"})
)

func init() {
	prometheus.MustRegister(Requests, UpstreamFailures, ConfigVersion, GrayHits)
}

// MetricsHandler serves the Prometheus exposition.
func MetricsHandler() http.Handler { return promhttp.Handler() }

// Event is one access-log line.
type Event struct {
	Time     time.Time `json:"ts"`
	Proto    string    `json:"proto"`
	Method   string    `json:"method"`
	Host     string    `json:"host"`
	Path     string    `json:"path"`
	Route    string    `json:"route,omitempty"`
	Service  string    `json:"service,omitempty"`
	Upstream string    `json:"upstream,omitempty"`
	Gray     string    `json:"gray,omitempty"`
	Status   int       `json:"status"`
	Bytes    int64     `json:"bytes"`
	Millis   int64     `json:"ms"`
	Error    string    `json:"error,omitempty"`
}

var accessLog = log.New(os.Stdout, "", 0)

// Log writes one JSON line per request.
func Log(e Event) {
	e.Time = time.Now().UTC()
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	accessLog.Println(string(line))
}
