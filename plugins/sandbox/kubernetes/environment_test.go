package kubernetes

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	sandbox "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/internal/sessionfs"
)

func TestRenderEnvRebuildsTurnAndSelectionPaths(t *testing.T) {
	s := &session{
		client: &Client{cfg: Config{StellaHome: "/data/home", ServerURL: "http://stella:25678"}},
		sources: map[string]string{
			"/workspace":                             "/data/home/work",
			"/opt/stella/bin":                        "/data/home/core",
			"/opt/stella/.mise-tools/public/current": "/data/home/.mise-tools/public/current",
		},
		policy: sandbox.Policy{Env: map[string]string{"OLD_TOKEN": "retained secret"}, Network: sandbox.NetworkPolicy{Mode: sandbox.NetworkAllowAll}, Filesystem: sandbox.FilesystemPolicy{Mounts: []sandbox.Mount{
			{SandboxPath: "/workspace", Access: sandbox.MountReadWrite},
			{SandboxPath: "/opt/stella/bin", Access: sandbox.MountReadOnly},
			{SandboxPath: "/opt/stella/.mise-tools/public/current", Access: sandbox.MountReadOnly},
		}}},
	}
	env, err := s.RenderEnv(t.Context(), map[string]string{
		"CURRENT":                     "yes",
		sandbox.EnvNativeSelectionDir: "/data/home/.mise-tools/public/current",
		sandbox.EnvCoreRuntimeDir:     "/data/home/core",
		"PATH":                        "/host/bin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if env["CURRENT"] != "yes" {
		t.Fatalf("logical env changed: %#v", env)
	}
	if _, ok := env["OLD_TOKEN"]; ok {
		t.Fatal("retained policy env leaked into logical turn")
	}
	if got, want := env[sandbox.EnvNativeSelectionDir], "/opt/stella/.mise-tools/public/current"; got != want {
		t.Fatalf("selection marker = %q, want %q", got, want)
	}
	if got, want := env[sandbox.EnvCoreRuntimeDir], "/opt/stella/bin"; got != want {
		t.Fatalf("core marker = %q, want %q", got, want)
	}
	if env["STELLA_SERVER_URL"] != "http://stella:25678" || env["STELLA_HOME"] != "/opt/stella" {
		t.Fatalf("backend environment = %#v", env)
	}
	if env["PATH"] != "/opt/stella/.mise-tools/public/current:/opt/stella/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" {
		t.Fatalf("PATH = %q", env["PATH"])
	}
}

func TestEnvironmentProjectsOnlyDeclaredPaths(t *testing.T) {
	s := &session{client: &Client{cfg: Config{StellaHome: "/data/home"}}, sources: map[string]string{"/workspace": "/data/home/work", "/user": "/data/home/user"}, policy: sandbox.Policy{Env: map[string]string{"STELLA_ASSETS_DIR": "/data/home/user/assets", "TOKEN": "/data/home/user/literal", "PATH": "/host/bin", "K": "old"}}}
	env := s.environment(map[string]string{"K": "new"}, sandbox.EnvOverlay)
	if env["STELLA_ASSETS_DIR"] != "/user/assets" || env["TOKEN"] != "/data/home/user/literal" || env["K"] != "new" {
		t.Fatalf("environment projection: %#v", env)
	}
	if env["PATH"] == "/host/bin" || env[sandbox.EnvRunnerPath] != env["PATH"] {
		t.Fatal("host PATH survived")
	}
	s.policy.Network.Mode = sandbox.NetworkDisabled
	if _, ok := s.environment(map[string]string{"STELLA_SERVER_URL": "http://callback"}, sandbox.EnvOverlay)["STELLA_SERVER_URL"]; ok {
		t.Fatal("disabled network exposed callback URL")
	}
}

func TestFrameEnvReplaceDropsRetainedPolicy(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "workspace"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolver, err := sessionfs.NewResolver("/workspace", []sessionfs.Mount{{HostPath: filepath.Join(root, "workspace"), SandboxPath: "/workspace"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resolver.Close() }()
	s := &session{
		client:   &Client{cfg: Config{StellaHome: filepath.Join(root, "home")}},
		policy:   sandbox.Policy{Env: map[string]string{"OLD_TOKEN": "retained"}, Filesystem: sandbox.FilesystemPolicy{WorkingDir: "/workspace"}},
		resolver: resolver,
	}
	frame, err := s.frame(sandbox.ProcessRequest{Path: "/bin/true", Env: map[string]string{"CURRENT": "yes"}, EnvMode: sandbox.EnvReplace})
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.BigEndian.Uint32(frame[:4]); int(got) != len(frame[4:]) {
		t.Fatalf("frame length = %d, payload = %d", got, len(frame[4:]))
	}
	var payload struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(frame[4:], &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Env["CURRENT"] != "yes" {
		t.Fatalf("current env = %#v", payload.Env)
	}
	if _, ok := payload.Env["OLD_TOKEN"]; ok {
		t.Fatal("retained policy env crossed EnvReplace frame")
	}
}
