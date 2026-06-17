package dockerclient

import (
	"context"
	"net/netip"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	mobyclient "github.com/moby/moby/client"
)

// inspectAPI overrides only ContainerInspect; other methods are never called by
// InspectSelf, so the embedded nil interface is fine.
type inspectAPI struct {
	API
	res mobyclient.ContainerInspectResult
	err error
}

func (f inspectAPI) ContainerInspect(context.Context, string, mobyclient.ContainerInspectOptions) (mobyclient.ContainerInspectResult, error) {
	return f.res, f.err
}

func inspectResult(name string, nets map[string]string) mobyclient.ContainerInspectResult {
	networks := make(map[string]*network.EndpointSettings, len(nets))
	for n, ip := range nets {
		ep := &network.EndpointSettings{}
		if ip != "" {
			ep.IPAddress = netip.MustParseAddr(ip)
		}
		networks[n] = ep
	}
	return mobyclient.ContainerInspectResult{Container: container.InspectResponse{
		ID:              "abc123def456",
		Name:            name,
		NetworkSettings: &container.NetworkSettings{Networks: networks},
	}}
}

func TestInspectSelf(t *testing.T) {
	t.Run("collects user networks sorted, bridge separate", func(t *testing.T) {
		c := NewWithAPI(inspectAPI{res: inspectResult("/anna-stella-1", map[string]string{
			"bridge":       "172.17.0.2",
			"anna_default": "192.168.97.3",
		})})
		self, err := c.InspectSelf(context.Background(), "abc123")
		if err != nil {
			t.Fatalf("InspectSelf: %v", err)
		}
		if self.Name != "anna-stella-1" {
			t.Fatalf("Name = %q, want anna-stella-1 (leading slash trimmed)", self.Name)
		}
		if len(self.Networks) != 1 || self.Networks[0].Name != "anna_default" || self.Networks[0].IP != "192.168.97.3" {
			t.Fatalf("Networks = %+v, want [anna_default 192.168.97.3]", self.Networks)
		}
		if self.BridgeIP != "172.17.0.2" {
			t.Fatalf("BridgeIP = %q, want 172.17.0.2", self.BridgeIP)
		}
	})

	t.Run("user networks sorted by name", func(t *testing.T) {
		c := NewWithAPI(inspectAPI{res: inspectResult("/s", map[string]string{
			"net-b": "10.0.2.2",
			"net-a": "10.0.1.2",
		})})
		self, _ := c.InspectSelf(context.Background(), "s")
		if len(self.Networks) != 2 || self.Networks[0].Name != "net-a" || self.Networks[1].Name != "net-b" {
			t.Fatalf("Networks = %+v, want sorted net-a, net-b", self.Networks)
		}
	})

	t.Run("only default networks yields no user network but keeps bridge", func(t *testing.T) {
		c := NewWithAPI(inspectAPI{res: inspectResult("/s", map[string]string{
			"bridge": "172.17.0.2",
			"host":   "",
			"none":   "",
		})})
		self, _ := c.InspectSelf(context.Background(), "s")
		if len(self.Networks) != 0 {
			t.Fatalf("expected no user network, got %+v", self.Networks)
		}
		if self.BridgeIP != "172.17.0.2" {
			t.Fatalf("BridgeIP = %q, want 172.17.0.2", self.BridgeIP)
		}
		if self.ID != "abc123def456" {
			t.Fatalf("ID = %q, want abc123def456", self.ID)
		}
	})

	t.Run("not found returns nil", func(t *testing.T) {
		c := NewWithAPI(inspectAPI{err: errdefs.ErrNotFound})
		self, err := c.InspectSelf(context.Background(), "ghost")
		if err != nil || self != nil {
			t.Fatalf("got self=%v err=%v, want nil,nil for not-found", self, err)
		}
	})
}
