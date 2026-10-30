// Copyright 2026 The gfluxy Authors.
package nacos

import (
	"errors"
	"testing"

	"github.com/nacos-group/nacos-sdk-go/v2/model"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"

	"github.com/maxid/gfluxy/internal/registry"
)

type fakeClient struct {
	instances map[string][]model.Instance
	selectErr error
	callbacks map[string]func([]model.Instance, error)
	closed    bool
}

func (f *fakeClient) SelectInstances(p vo.SelectInstancesParam) ([]model.Instance, error) {
	if f.selectErr != nil {
		return nil, f.selectErr
	}
	return f.instances[p.ServiceName], nil
}

func (f *fakeClient) Subscribe(p *vo.SubscribeParam) error {
	f.callbacks[p.ServiceName] = p.SubscribeCallback
	return nil
}

func (f *fakeClient) CloseClient() { f.closed = true }

func TestNormalizeMetadataToLabels(t *testing.T) {
	fake := &fakeClient{
		callbacks: map[string]func([]model.Instance, error){},
		instances: map[string][]model.Instance{
			"admin": {
				{Ip: "10.0.0.1", Port: 8080, Weight: 2, Healthy: true, Metadata: map[string]string{
					"version": "1.2.0",
					"color":   "canary",
					"zone":    "cn-hangzhou", // registry-specific: must not leak through
				}},
				{Ip: "10.0.0.2", Port: 8080, Healthy: true},
				{Ip: "", Port: 0, Healthy: true},
			},
		},
	}

	reg, err := newWithClient(fake, "DEV_GROUP", []string{"admin"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	endpoints := reg.Resolve("admin")
	if len(endpoints) != 2 {
		t.Fatalf("endpoints = %d, want 2 (invalid instance skipped)", len(endpoints))
	}
	if endpoints[0].Addr != "10.0.0.1:8080" || endpoints[0].Weight != 2 {
		t.Errorf("endpoint = %+v, want 10.0.0.1:8080 weight 2", endpoints[0])
	}
	if endpoints[0].Labels[registry.LabelVersion] != "1.2.0" || endpoints[0].Labels[registry.LabelColor] != "canary" {
		t.Errorf("labels = %v, want version/color only", endpoints[0].Labels)
	}
	if _, ok := endpoints[0].Labels["zone"]; ok {
		t.Error("registry-specific metadata leaked into the label set")
	}
	if endpoints[1].Weight != 1 {
		t.Errorf("default weight = %d, want 1", endpoints[1].Weight)
	}
}

func TestSubscriptionUpdatesAndKeepsLastGoodOnError(t *testing.T) {
	fake := &fakeClient{
		callbacks: map[string]func([]model.Instance, error){},
		instances: map[string][]model.Instance{
			"iot": {{Ip: "10.0.0.1", Port: 8081, Healthy: true}},
		},
	}
	reg, err := newWithClient(fake, "", []string{"iot"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	callback := fake.callbacks["iot"]
	callback([]model.Instance{{Ip: "10.0.0.9", Port: 8081, Healthy: true}}, nil)
	if got := reg.Resolve("iot"); len(got) != 1 || got[0].Addr != "10.0.0.9:8081" {
		t.Fatalf("after update = %+v, want 10.0.0.9:8081", got)
	}

	callback(nil, errors.New("nacos down"))
	if got := reg.Resolve("iot"); len(got) != 1 || got[0].Addr != "10.0.0.9:8081" {
		t.Fatalf("stale list lost on error: %+v", got)
	}
	if reg.Status() == nil {
		t.Fatal("status must report the discovery error")
	}
}

func TestColdStartWithNacosDownStillBoots(t *testing.T) {
	fake := &fakeClient{
		callbacks: map[string]func([]model.Instance, error){},
		selectErr: errors.New("connection refused"),
	}
	reg, err := newWithClient(fake, "", []string{"admin"})
	if err != nil {
		t.Fatalf("cold start must not fail: %v", err)
	}
	if got := reg.Resolve("admin"); len(got) != 0 {
		t.Fatalf("endpoints = %+v, want none", got)
	}
	if reg.Status() == nil {
		t.Fatal("status must report the discovery error")
	}
}

func TestCloseReleasesClient(t *testing.T) {
	fake := &fakeClient{callbacks: map[string]func([]model.Instance, error){}}
	reg, err := newWithClient(fake, "", []string{"admin"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	reg.Close()
	if !fake.closed {
		t.Fatal("close did not release the naming client")
	}
}
