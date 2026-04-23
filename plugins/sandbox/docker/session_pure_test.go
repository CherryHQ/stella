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
	table := buildMountTable("/host/ws", "/container/ws", "/host/.anna", "/home/anna/.anna")
	if len(table) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(table))
	}
	if table[0].HostPath != "/host/ws" || table[0].ContainerPath != "/container/ws" {
		t.Fatalf("unexpected workspace mount: %+v", table[0])
	}
	if table[0].ReadOnly {
		t.Fatal("workspace should be read-write")
	}
	if table[1].HostPath != "/host/.anna" || table[1].ContainerPath != "/home/anna/.anna" || !table[1].ReadOnly {
		t.Fatalf("unexpected anna home synthetic mount: %+v", table[1])
	}
	if table[2].HostPath != "/host/.anna/bin" || table[2].ContainerPath != "/home/anna/.anna/bin" || !table[2].ReadOnly {
		t.Fatalf("unexpected anna bin mount: %+v", table[2])
	}
	if table[3].HostPath != "/host/.anna/skills" || table[3].ContainerPath != "/home/anna/.anna/skills" || !table[3].ReadOnly {
		t.Fatalf("unexpected anna skills mount: %+v", table[3])
	}
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

func TestInjectAnnaHomeBinPath_PrependedWhenSet(t *testing.T) {
	env := map[string]string{
		"ANNA_HOME": "/home/anna/.anna",
		"PATH":      "/usr/bin:/bin",
	}
	got := injectAnnaHomeBinPath(env)
	want := "/home/anna/.anna/bin:/usr/bin:/bin"
	if got["PATH"] != want {
		t.Errorf("PATH = %q, want %q", got["PATH"], want)
	}
}

func TestInjectAnnaHomeBinPath_UsesDefaultPathWhenPATHAbsent(t *testing.T) {
	env := map[string]string{
		"ANNA_HOME": "/home/anna/.anna",
	}
	got := injectAnnaHomeBinPath(env)
	if got["PATH"] == "" {
		t.Fatal("PATH should not be empty when ANNA_HOME is set")
	}
	if got["PATH"][:len("/home/anna/.anna/bin:")] != "/home/anna/.anna/bin:" {
		t.Errorf("PATH does not start with ANNA_HOME/bin: %q", got["PATH"])
	}
	if len(got["PATH"]) <= len("/home/anna/.anna/bin:") {
		t.Error("PATH should include containerDefaultPATH after ANNA_HOME/bin")
	}
}

func TestInjectAnnaHomeBinPath_NoOpWhenAbsent(t *testing.T) {
	env := map[string]string{
		"PATH": "/usr/bin:/bin",
	}
	got := injectAnnaHomeBinPath(env)
	if got["PATH"] != "/usr/bin:/bin" {
		t.Errorf("PATH changed when ANNA_HOME absent: %q", got["PATH"])
	}
}

func TestInjectAnnaHomeBinPath_NoOpWhenEmpty(t *testing.T) {
	env := map[string]string{
		"ANNA_HOME": "",
		"PATH":      "/usr/bin:/bin",
	}
	got := injectAnnaHomeBinPath(env)
	if got["PATH"] != "/usr/bin:/bin" {
		t.Errorf("PATH changed when ANNA_HOME is empty: %q", got["PATH"])
	}
}
