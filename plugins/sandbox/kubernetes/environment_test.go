package kubernetes

import (
	"testing"

	sandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func TestEnvironmentProjectsOnlyDeclaredPaths(t *testing.T) {
	s := &session{client: &Client{cfg: Config{StellaHome: "/data/home"}}, sources: map[string]string{"/workspace": "/data/home/work", "/user": "/data/home/user"}, policy: sandbox.Policy{Env: map[string]string{"STELLA_ASSETS_DIR": "/data/home/user/assets", "TOKEN": "/data/home/user/literal", "PATH": "/host/bin", "K": "old"}}}
	env := s.environment(map[string]string{"K": "new"})
	if env["STELLA_ASSETS_DIR"] != "/user/assets" || env["TOKEN"] != "/data/home/user/literal" || env["K"] != "new" {
		t.Fatalf("environment projection: %#v", env)
	}
	if env["PATH"] == "/host/bin" || env[sandbox.EnvRunnerPath] != env["PATH"] {
		t.Fatal("host PATH survived")
	}
	s.policy.Network.Mode = sandbox.NetworkDisabled
	if _, ok := s.environment(map[string]string{"STELLA_SERVER_URL": "http://callback"})["STELLA_SERVER_URL"]; ok {
		t.Fatal("disabled network exposed callback URL")
	}
}
