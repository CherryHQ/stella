package docker

import (
	"testing"

	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

func TestApplyReachability(t *testing.T) {
	composeSelf := &dockerclient.SelfContainer{
		Name:     "anna-stella-1",
		Networks: []dockerclient.SelfNetwork{{Name: "anna_default", IP: "192.168.97.3"}},
		BridgeIP: "172.17.0.2",
	}

	t.Run("auto: joins user network, URL by container name (DNS)", func(t *testing.T) {
		got := applyReachability(Config{}, composeSelf)
		if got.SandboxNetwork != "anna_default" {
			t.Fatalf("network = %q", got.SandboxNetwork)
		}
		if got.ServerURL != "http://anna-stella-1:25678" {
			t.Fatalf("url = %q, want name-based", got.ServerURL)
		}
	})

	t.Run("prefers *_default network when several", func(t *testing.T) {
		self := &dockerclient.SelfContainer{Name: "s", Networks: []dockerclient.SelfNetwork{
			{Name: "anna_backend", IP: "10.0.1.2"},
			{Name: "anna_default", IP: "10.0.2.2"},
		}}
		got := applyReachability(Config{}, self)
		if got.SandboxNetwork != "anna_default" {
			t.Fatalf("network = %q, want anna_default preferred", got.SandboxNetwork)
		}
	})

	t.Run("bridge-only: keep default bridge, URL by bridge IP", func(t *testing.T) {
		self := &dockerclient.SelfContainer{Name: "s", BridgeIP: "172.17.0.2"}
		got := applyReachability(Config{}, self)
		if got.SandboxNetwork != "" {
			t.Fatalf("network = %q, want empty (default bridge)", got.SandboxNetwork)
		}
		if got.ServerURL != "http://172.17.0.2:25678" {
			t.Fatalf("url = %q, want bridge IP", got.ServerURL)
		}
	})

	t.Run("explicit network present on self: URL host from that network by name", func(t *testing.T) {
		got := applyReachability(Config{SandboxNetwork: "anna_default"}, composeSelf)
		if got.SandboxNetwork != "anna_default" || got.ServerURL != "http://anna-stella-1:25678" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("explicit network absent on self: do not fabricate URL", func(t *testing.T) {
		got := applyReachability(Config{SandboxNetwork: "custom-net"}, composeSelf)
		if got.SandboxNetwork != "custom-net" {
			t.Fatalf("network = %q", got.SandboxNetwork)
		}
		if got.ServerURL != "" {
			t.Fatalf("url = %q, want empty (stellad not on custom-net)", got.ServerURL)
		}
	})

	t.Run("explicit URL always wins", func(t *testing.T) {
		got := applyReachability(Config{ServerURL: "http://stella:9000"}, composeSelf)
		if got.ServerURL != "http://stella:9000" {
			t.Fatalf("url overridden: %q", got.ServerURL)
		}
	})
}
