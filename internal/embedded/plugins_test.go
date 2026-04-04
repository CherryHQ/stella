package embedded

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/vaayne/anna/internal/pluginhost"
)

func TestEnsurePlugins(t *testing.T) {
	ensurePluginsOnce = sync.Once{}
	dest := t.TempDir()

	if err := EnsurePlugins(dest); err != nil {
		t.Fatalf("EnsurePlugins: %v", err)
	}

	bundledDir := filepath.Join(dest, "plugins", "bundled")

	// Verify all 9 manifests are extracted.
	wantPlugins := []struct {
		kind string
		name string
	}{
		{"tool", "read"},
		{"tool", "bash"},
		{"tool", "edit"},
		{"tool", "write"},
		{"tool", "webfetch"},
		{"channel", "telegram"},
		{"channel", "qq"},
		{"channel", "feishu"},
		{"channel", "weixin"},
	}

	for _, p := range wantPlugins {
		path := filepath.Join(bundledDir, p.kind, p.name, "plugin.json")
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("expected manifest at %s: %v", path, err)
			continue
		}
		if info.IsDir() {
			t.Errorf("expected %s to be a file, not directory", path)
		}
		if info.Size() == 0 {
			t.Errorf("expected %s to be non-empty", path)
		}
	}

	// Verify pluginhost.Discover can load them.
	catalog, err := pluginhost.Discover(bundledDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	for _, p := range wantPlugins {
		id := p.kind + "/" + p.name
		def, ok := catalog.Get(id)
		if !ok {
			t.Errorf("catalog missing plugin %s", id)
			continue
		}
		if def.Manifest.Entrypoint != pluginhost.BuiltinEntrypoint {
			t.Errorf("plugin %s: entrypoint = %q, want %q", id, def.Manifest.Entrypoint, pluginhost.BuiltinEntrypoint)
		}
	}

	// Verify total count.
	all := catalog.List()
	if len(all) != len(wantPlugins) {
		t.Errorf("catalog has %d plugins, want %d", len(all), len(wantPlugins))
	}
}

func TestEnsurePluginsIdempotent(t *testing.T) {
	ensurePluginsOnce = sync.Once{}
	dest := t.TempDir()

	if err := EnsurePlugins(dest); err != nil {
		t.Fatal(err)
	}

	// Reset once to allow second call.
	ensurePluginsOnce = sync.Once{}
	if err := EnsurePlugins(dest); err != nil {
		t.Fatal(err)
	}
}
