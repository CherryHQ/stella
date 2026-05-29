package manifestplugins

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
)

type fakePluginLister struct {
	plugins []config.Plugin
}

func (f *fakePluginLister) ListPluginsByKind(_ context.Context, kind string) ([]config.Plugin, error) {
	if kind != config.PluginKindCLI {
		return nil, nil
	}
	return f.plugins, nil
}

func cliPlugin(name, tool, version string) config.Plugin {
	return config.Plugin{
		Name:    name,
		Kind:    config.PluginKindCLI,
		Enabled: true,
		Config:  map[string]any{"mise_tool": tool, "version": version},
	}
}

// writeFakeMise installs a no-op mise that succeeds for trust/install/reshim so
// SyncOrgCLITools exercises config rendering without touching the network.
func writeFakeMise(t *testing.T, stellaHome string) {
	t.Helper()
	binDir := filepath.Join(stellaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	fake := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "mise"), []byte(fake), 0o755); err != nil {
		t.Fatalf("write fake mise: %v", err)
	}
}

func TestSyncOrgCLITools_SelfContainedConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake mise script uses POSIX shell")
	}
	stellaHome := t.TempDir()
	writeFakeMise(t, stellaHome)

	store := &fakePluginLister{plugins: []config.Plugin{
		cliPlugin("orgtool", "github:acme/orgtool", "9.9.9"),
	}}
	syncer := NewOrgCLISyncer(store, stellaHome)

	if err := syncer.SyncOrgCLITools(context.Background(), "org1"); err != nil {
		t.Fatalf("SyncOrgCLITools: %v", err)
	}

	data, err := os.ReadFile(ScopeConfigPath(stellaHome, "org1"))
	if err != nil {
		t.Fatalf("read org config: %v", err)
	}
	cfg := string(data)

	// Org tool is present.
	if !strings.Contains(cfg, `'github:acme/orgtool' = '9.9.9'`) {
		t.Errorf("org config missing org tool:\n%s", cfg)
	}
	// Builtin base is folded in (self-contained), e.g. gh from plugins.yaml.
	if !strings.Contains(cfg, "github:cli/cli") {
		t.Errorf("org config missing builtin base tools:\n%s", cfg)
	}
}

func TestSyncOrgCLITools_PerOrgVersionsDiffer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake mise script uses POSIX shell")
	}
	stellaHome := t.TempDir()
	writeFakeMise(t, stellaHome)

	store1 := &fakePluginLister{plugins: []config.Plugin{cliPlugin("shared", "ubi:acme/shared", "1.0.0")}}
	store2 := &fakePluginLister{plugins: []config.Plugin{cliPlugin("shared", "ubi:acme/shared", "2.0.0")}}

	if err := NewOrgCLISyncer(store1, stellaHome).SyncOrgCLITools(context.Background(), "orgA"); err != nil {
		t.Fatalf("sync orgA: %v", err)
	}
	if err := NewOrgCLISyncer(store2, stellaHome).SyncOrgCLITools(context.Background(), "orgB"); err != nil {
		t.Fatalf("sync orgB: %v", err)
	}

	a, err := os.ReadFile(ScopeConfigPath(stellaHome, "orgA"))
	if err != nil {
		t.Fatalf("read orgA: %v", err)
	}
	b, err := os.ReadFile(ScopeConfigPath(stellaHome, "orgB"))
	if err != nil {
		t.Fatalf("read orgB: %v", err)
	}
	if !strings.Contains(string(a), `'ubi:acme/shared' = '1.0.0'`) {
		t.Errorf("orgA wrong version:\n%s", a)
	}
	if !strings.Contains(string(b), `'ubi:acme/shared' = '2.0.0'`) {
		t.Errorf("orgB wrong version:\n%s", b)
	}
}

func TestSyncOrgCLITools_RejectsUnsafeBackend(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake mise script uses POSIX shell")
	}
	stellaHome := t.TempDir()
	writeFakeMise(t, stellaHome)

	store := &fakePluginLister{plugins: []config.Plugin{
		cliPlugin("safe", "github:acme/safe", "1.0.0"),
		cliPlugin("evil", "npm:evil", "1.0.0"), // code-executing backend
		cliPlugin("bare", "ripgrep", "1.0.0"),  // no explicit safe backend
	}}

	if err := NewOrgCLISyncer(store, stellaHome).SyncOrgCLITools(context.Background(), "org1"); err != nil {
		t.Fatalf("SyncOrgCLITools: %v", err)
	}

	data, err := os.ReadFile(ScopeConfigPath(stellaHome, "org1"))
	if err != nil {
		t.Fatalf("read org config: %v", err)
	}
	cfg := string(data)

	if !strings.Contains(cfg, "github:acme/safe") {
		t.Errorf("safe tool was dropped:\n%s", cfg)
	}
	if strings.Contains(cfg, "npm:evil") {
		t.Errorf("code-executing backend was not rejected:\n%s", cfg)
	}
	if strings.Contains(cfg, "'ripgrep'") {
		t.Errorf("bare registry name was not rejected:\n%s", cfg)
	}
}
