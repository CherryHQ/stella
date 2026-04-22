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
	table := buildMountTable("/host/ws", "/container/ws")
	if len(table) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(table))
	}
	if table[0].HostPath != "/host/ws" || table[0].ContainerPath != "/container/ws" {
		t.Fatalf("unexpected mount: %+v", table[0])
	}
	if table[0].ReadOnly {
		t.Fatal("workspace should be read-write")
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

func TestInjectWrapperPath_PrependedWhenSet(t *testing.T) {
	env := map[string]string{
		"ANNA_WRAPPER_DIR": "/home/anna/workspace/.anna/bin",
		"PATH":             "/usr/bin:/bin",
	}
	got := injectWrapperPath(env)
	want := "/home/anna/workspace/.anna/bin:/usr/bin:/bin"
	if got["PATH"] != want {
		t.Errorf("PATH = %q, want %q", got["PATH"], want)
	}
}

func TestInjectWrapperPath_UsesDefaultPathWhenPATHAbsent(t *testing.T) {
	env := map[string]string{
		"ANNA_WRAPPER_DIR": "/home/anna/workspace/.anna/bin",
	}
	got := injectWrapperPath(env)
	if got["PATH"] == "" {
		t.Fatal("PATH should not be empty when ANNA_WRAPPER_DIR is set")
	}
	if got["PATH"][:len("/home/anna/workspace/.anna/bin:")] != "/home/anna/workspace/.anna/bin:" {
		t.Errorf("PATH does not start with wrapper dir: %q", got["PATH"])
	}
	// Should include the container default PATH.
	if len(got["PATH"]) <= len("/home/anna/workspace/.anna/bin:") {
		t.Error("PATH should include containerDefaultPATH after wrapper dir")
	}
}

func TestInjectWrapperPath_NoOpWhenAbsent(t *testing.T) {
	env := map[string]string{
		"PATH": "/usr/bin:/bin",
	}
	got := injectWrapperPath(env)
	if got["PATH"] != "/usr/bin:/bin" {
		t.Errorf("PATH changed when ANNA_WRAPPER_DIR absent: %q", got["PATH"])
	}
}

func TestInjectWrapperPath_NoOpWhenEmpty(t *testing.T) {
	env := map[string]string{
		"ANNA_WRAPPER_DIR": "",
		"PATH":             "/usr/bin:/bin",
	}
	got := injectWrapperPath(env)
	if got["PATH"] != "/usr/bin:/bin" {
		t.Errorf("PATH changed when ANNA_WRAPPER_DIR is empty: %q", got["PATH"])
	}
}

func TestInjectDockerBinPaths_SetsFixedPaths(t *testing.T) {
	env := map[string]string{}
	got := injectDockerBinPaths(env)

	if got["ANNA_GH_BIN"] != "/usr/bin/gh" {
		t.Errorf("ANNA_GH_BIN = %q, want %q", got["ANNA_GH_BIN"], "/usr/bin/gh")
	}
	if got["ANNA_LARK_BIN"] != "/usr/local/bin/lark-cli" {
		t.Errorf("ANNA_LARK_BIN = %q, want %q", got["ANNA_LARK_BIN"], "/usr/local/bin/lark-cli")
	}
}

func TestInjectDockerBinPaths_OverridesHostPaths(t *testing.T) {
	env := map[string]string{
		"ANNA_GH_BIN":   "/host/anna/bin/gh",
		"ANNA_LARK_BIN": "/host/anna/bin/lark-cli",
	}
	got := injectDockerBinPaths(env)

	if got["ANNA_GH_BIN"] != "/usr/bin/gh" {
		t.Errorf("ANNA_GH_BIN = %q, want container path %q", got["ANNA_GH_BIN"], "/usr/bin/gh")
	}
	if got["ANNA_LARK_BIN"] != "/usr/local/bin/lark-cli" {
		t.Errorf("ANNA_LARK_BIN = %q, want container path %q", got["ANNA_LARK_BIN"], "/usr/local/bin/lark-cli")
	}
}
