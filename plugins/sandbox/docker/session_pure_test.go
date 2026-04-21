package docker

import (
	"testing"

	sandboxpkg "github.com/vaayne/anna/pkg/sandbox"
	"github.com/vaayne/anna/plugins/sandbox/docker/dockerclient"
)

func TestMergeEnv(t *testing.T) {
	t.Run("nil inputs", func(t *testing.T) {
		got := mergeEnv(nil, nil)
		if len(got) != 0 {
			t.Fatalf("expected empty map, got %v", got)
		}
	})
	t.Run("policy only", func(t *testing.T) {
		got := mergeEnv(map[string]string{"A": "1"}, nil)
		if got["A"] != "1" {
			t.Fatalf("unexpected: %v", got)
		}
	})
	t.Run("opts override policy", func(t *testing.T) {
		got := mergeEnv(map[string]string{"A": "policy"}, map[string]string{"A": "opts"})
		if got["A"] != "opts" {
			t.Fatalf("expected opts to win, got %q", got["A"])
		}
	})
	t.Run("merge both", func(t *testing.T) {
		got := mergeEnv(map[string]string{"A": "1"}, map[string]string{"B": "2"})
		if got["A"] != "1" || got["B"] != "2" {
			t.Fatalf("unexpected: %v", got)
		}
	})
}

func TestBuildMountTable(t *testing.T) {
	t.Run("workspace only", func(t *testing.T) {
		table := buildMountTable("/host/ws", "/container/ws", nil)
		if len(table) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(table))
		}
		if table[0].HostPath != "/host/ws" || table[0].ContainerPath != "/container/ws" {
			t.Fatalf("unexpected mount: %+v", table[0])
		}
		if table[0].ReadOnly {
			t.Fatal("workspace should be read-write")
		}
	})
	t.Run("with readonly mounts", func(t *testing.T) {
		ro := []dockerclient.Mount{{HostPath: "/h/ro", ContainerPath: "/c/ro", ReadOnly: true}}
		table := buildMountTable("/host/ws", "/container/ws", ro)
		if len(table) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(table))
		}
		if !table[1].ReadOnly {
			t.Fatal("ro mount should be ReadOnly")
		}
	})
}

func TestMapNetworkMode(t *testing.T) {
	cases := []struct {
		mode sandboxpkg.NetworkMode
		want dockerclient.NetworkMode
	}{
		{sandboxpkg.NetworkDisabled, dockerclient.NetworkDisabled},
		{sandboxpkg.NetworkAllowAll, dockerclient.NetworkAllowAll},
	}
	for _, c := range cases {
		policy := sandboxpkg.Policy{Network: sandboxpkg.NetworkPolicy{Mode: c.mode}}
		got := mapNetworkMode(policy)
		if got != c.want {
			t.Fatalf("mode %v: got %v, want %v", c.mode, got, c.want)
		}
	}
}

func TestTranslateMountsForDaemon(t *testing.T) {
	t.Run("no prefix returns unchanged", func(t *testing.T) {
		cfg := Config{}
		mounts := []dockerclient.Mount{{HostPath: "/host/foo", ContainerPath: "/c/foo"}}
		got := translateMountsForDaemon(cfg, mounts)
		if got[0].HostPath != "/host/foo" {
			t.Fatalf("unexpected: %v", got[0].HostPath)
		}
	})
	t.Run("translates host paths", func(t *testing.T) {
		// anna runs in a container where its view of ANNA_HOME is /container/anna.
		// The daemon (on the host) sees that same directory as /host/anna.
		// translateMountsForDaemon must rewrite the HostPath from the
		// container-view prefix to the daemon-view prefix.
		cfg := Config{ContainerPathPrefix: "/container", HostPathPrefix: "/host"}
		mounts := []dockerclient.Mount{{HostPath: "/container/foo", ContainerPath: "/c/foo"}}
		got := translateMountsForDaemon(cfg, mounts)
		if got[0].HostPath != "/host/foo" {
			t.Fatalf("expected /host/foo, got %q", got[0].HostPath)
		}
	})
}
