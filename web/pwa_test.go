package web

import (
	"os"
	"path/filepath"
	"testing"
)

// PWARootFiles drives the server's auth exemption, so an entry that stops
// shipping does not fail anything at runtime — it just quietly makes the Web UI
// uninstallable, because the browser gets the SPA shell where it expected a
// manifest or a worker. Pin the list to the files on disk instead.
func TestPWARootFilesArePublished(t *testing.T) {
	for _, path := range PWARootFiles {
		source := filepath.Join("public", filepath.FromSlash(path))
		if _, err := os.Stat(source); err != nil {
			t.Errorf("%s is exempt from auth but missing from web/public: %v", path, err)
		}
	}
}

// The published files must also survive the build, since that is what the
// binary embeds and serves.
func TestPWARootFilesAreBuilt(t *testing.T) {
	if _, err := staticFS.ReadFile("static/dist/index.html"); err != nil {
		t.Skipf("SPA not built: %v", err)
	}
	for _, path := range PWARootFiles {
		if _, err := staticFS.ReadFile("static/dist" + path); err != nil {
			t.Errorf("%s did not survive the SPA build: %v", path, err)
		}
	}
}
