//go:build windows

package resources

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuiltinBundleWindowsFilesystemModes(t *testing.T) {
	registry, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	bundle, err := registry.InstallBuiltinBundle(home)
	if err != nil {
		t.Fatalf("InstallBuiltinBundle: %v", err)
	}
	if err := registry.VerifyBuiltinBundle(home); err != nil {
		t.Fatalf("VerifyBuiltinBundle: %v", err)
	}

	wantModes := map[os.FileMode]os.FileMode{0o644: 0o666, 0o755: 0o666}
	seen := make(map[os.FileMode]bool, len(wantModes))
	for _, skill := range registry.BuiltinSkills() {
		for _, file := range skill.Files {
			want, ok := wantModes[file.Mode.Perm()]
			if !ok || seen[file.Mode.Perm()] {
				continue
			}
			info, err := os.Stat(filepath.Join(bundle, filepath.FromSlash(skill.Root), filepath.FromSlash(file.Path)))
			if err != nil {
				t.Fatal(err)
			}
			if !info.Mode().IsRegular() || info.Mode().Perm() != want {
				t.Fatalf("installed source mode = %s, want regular %04o for manifest %04o", info.Mode(), want, file.Mode.Perm())
			}
			seen[file.Mode.Perm()] = true
		}
	}
	for mode := range wantModes {
		if !seen[mode] {
			t.Fatalf("embedded manifest has no %04o source file", mode)
		}
	}

	marker := filepath.Join(bundle, bundleCompleteMarker)
	markerInfo, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	if !markerInfo.Mode().IsRegular() || markerInfo.Mode().Perm() != 0o444 {
		t.Fatalf("completion marker mode = %s, want regular 0444", markerInfo.Mode())
	}
	if err := os.Chmod(marker, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := registry.VerifyBuiltinBundle(home); err == nil {
		t.Fatal("VerifyBuiltinBundle accepted writable completion marker")
	}
	if _, err := registry.InstallBuiltinBundle(home); err != nil {
		t.Fatalf("repair InstallBuiltinBundle: %v", err)
	}
	if err := registry.VerifyBuiltinBundle(home); err != nil {
		t.Fatalf("VerifyBuiltinBundle after repair: %v", err)
	}
}
