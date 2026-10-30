// Copyright 2026 The gfluxy Authors.
//
// Package nacos adapts Nacos service discovery to the gateway registry. The
// adapter is the only place that knows Nacos metadata key names: everything
// downstream sees a normalised LabelSet.
package nacos

import (
	"fmt"
	"log"
	"strconv"
	"sync"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/model"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"

	"github.com/maxid/gfluxy/internal/registry"
)

// Config mirrors the start-up registry block.
type Config struct {
	Servers   []string
	Namespace string
	Group     string
	Username  string
	Password  string
}

// Registry is a Nacos-backed, live instance table. It keeps serving the last
// good list when Nacos reports an error (stale-while-error).
type Registry struct {
	group  string
	client namingClient

	mu       sync.RWMutex
	services map[string][]registry.Endpoint
	lastErr  error
}

type namingClient interface {
	SelectInstances(param vo.SelectInstancesParam) ([]model.Instance, error)
	Subscribe(param *vo.SubscribeParam) error
	CloseClient()
}

// New connects to Nacos and subscribes to every service the routes reference.
func New(cfg Config, services []string) (*Registry, error) {
	sc := make([]constant.ServerConfig, 0, len(cfg.Servers))
	for _, addr := range cfg.Servers {
		host, port, err := splitHostPort(addr)
		if err != nil {
			return nil, err
		}
		sc = append(sc, constant.ServerConfig{IpAddr: host, Port: uint64(port)})
	}

	cc := constant.ClientConfig{
		NamespaceId: cfg.Namespace,
		Username:    cfg.Username,
		Password:    cfg.Password,
		TimeoutMs:   5000,
	}
	client, err := clients.NewNamingClient(vo.NacosClientParam{ClientConfig: &cc, ServerConfigs: sc})
	if err != nil {
		return nil, fmt.Errorf("nacos client: %w", err)
	}
	return newWithClient(client, cfg.Group, services)
}

func newWithClient(client namingClient, group string, services []string) (*Registry, error) {
	if group == "" {
		group = "DEFAULT_GROUP"
	}
	r := &Registry{
		group:    group,
		client:   client,
		services: make(map[string][]registry.Endpoint, len(services)),
	}

	for _, service := range services {
		instances, err := client.SelectInstances(vo.SelectInstancesParam{
			ServiceName: service,
			GroupName:   group,
			HealthyOnly: true,
		})
		if err == nil {
			r.set(service, normalize(instances))
		} else {
			// Cold start with Nacos down: keep going with an empty table so the
			// gateway can report not-ready instead of refusing to boot.
			r.fail(err)
			log.Printf("gfluxy: nacos select %s: %v", service, err)
		}

		if err := client.Subscribe(&vo.SubscribeParam{
			ServiceName: service,
			GroupName:   group,
			SubscribeCallback: func(instances []model.Instance, err error) {
				if err != nil {
					r.fail(err)
					return
				}
				r.set(service, normalize(instances))
			},
		}); err != nil {
			log.Printf("gfluxy: nacos subscribe %s: %v", service, err)
		}
	}
	return r, nil
}

// Resolve implements registry.Registry.
func (r *Registry) Resolve(service string) []registry.Endpoint {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]registry.Endpoint, len(r.services[service]))
	copy(out, r.services[service])
	return out
}

// Status reports whether the last discovery interaction succeeded.
func (r *Registry) Status() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastErr
}

// Close releases the Nacos long-polling subscriptions.
func (r *Registry) Close() { r.client.CloseClient() }

func (r *Registry) set(service string, endpoints []registry.Endpoint) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.services[service] = endpoints
	r.lastErr = nil
}

func (r *Registry) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastErr = err
}

// normalize maps Nacos instances onto the gateway model: metadata keys are
// read here and nowhere else.
func normalize(instances []model.Instance) []registry.Endpoint {
	out := make([]registry.Endpoint, 0, len(instances))
	for _, in := range instances {
		if in.Ip == "" || in.Port == 0 {
			continue
		}
		labels := registry.LabelSet{}
		for _, key := range []string{registry.LabelVersion, registry.LabelColor, registry.LabelGroup} {
			if v, ok := in.Metadata[key]; ok && v != "" {
				labels[key] = v
			}
		}
		out = append(out, registry.Endpoint{
			Addr:    fmt.Sprintf("%s:%d", in.Ip, in.Port),
			Weight:  weight(in.Weight),
			Labels:  labels,
			Healthy: in.Healthy,
		})
	}
	return out
}

func weight(w float64) int {
	if w <= 0 {
		return 1
	}
	return int(w)
}

func splitHostPort(addr string) (string, int, error) {
	host, portStr := addr, ""
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			host, portStr = addr[:i], addr[i+1:]
			break
		}
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || host == "" {
		return "", 0, fmt.Errorf("nacos server %q: want host:port", addr)
	}
	return host, port, nil
}
