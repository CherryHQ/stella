package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
// binary embeds and serves — and they must survive as themselves. A file that
// exists but holds the wrong bytes (a truncated copy, or an error page picked
// up by a broken asset step) is indistinguishable from a healthy build until a
// browser refuses to install.
func TestPWARootFilesAreBuilt(t *testing.T) {
	if _, err := staticFS.ReadFile("static/dist/index.html"); err != nil {
		t.Skipf("SPA not built: %v", err)
	}
	for _, path := range PWARootFiles {
		body, err := staticFS.ReadFile("static/dist" + path)
		if err != nil {
			t.Errorf("%s did not survive the SPA build: %v", path, err)
			continue
		}
		if len(body) == 0 {
			t.Errorf("%s is empty", path)
			continue
		}
		// The dominant failure is a path resolving to the SPA shell.
		if bytes.HasPrefix(bytes.TrimSpace(bytes.ToLower(body)), []byte("<!doctype html")) {
			t.Errorf("%s is the HTML shell, not the file it claims to be", path)
			continue
		}
		switch filepath.Ext(path) {
		case ".png":
			if !bytes.HasPrefix(body, []byte("\x89PNG\r\n\x1a\n")) {
				t.Errorf("%s is not a PNG", path)
			}
		case ".svg":
			if !bytes.Contains(body, []byte("<svg")) {
				t.Errorf("%s is not an SVG", path)
			}
		case ".js":
			if !json.Valid(body) && !bytes.Contains(body, []byte("addEventListener")) {
				t.Errorf("%s does not look like the service worker", path)
			}
		}
	}
}

// Chrome will not offer to install without a name, a start URL, a standalone
// display mode, and an icon of at least 192px. None of that fails at runtime —
// the install prompt simply never appears — so pin the contract here.
func TestWebManifestMeetsInstallCriteria(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("public", "site.webmanifest"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	var manifest struct {
		Name      string `json:"name"`
		ShortName string `json:"short_name"`
		StartURL  string `json:"start_url"`
		Display   string `json:"display"`
		Icons     []struct {
			Src   string `json:"src"`
			Sizes string `json:"sizes"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}

	if manifest.Name == "" || manifest.ShortName == "" {
		t.Error("manifest needs both name and short_name")
	}
	if manifest.StartURL == "" {
		t.Error("manifest needs start_url")
	}
	if manifest.Display != "standalone" && manifest.Display != "fullscreen" {
		t.Errorf("display = %q, want standalone or fullscreen for an installable app", manifest.Display)
	}

	var largest int
	for _, icon := range manifest.Icons {
		var w, h int
		if _, err := fmt.Sscanf(icon.Sizes, "%dx%d", &w, &h); err != nil {
			continue
		}
		if w > largest {
			largest = w
		}
		// Every icon the manifest promises must be one the server will serve
		// without a session, or the browser reads an HTML redirect as an image.
		if !slices.Contains(PWARootFiles, icon.Src) {
			t.Errorf("manifest icon %s is not in PWARootFiles, so it needs a session to fetch", icon.Src)
		}
	}
	if largest < 192 {
		t.Errorf("largest manifest icon is %dpx, want at least 192px", largest)
	}
}
