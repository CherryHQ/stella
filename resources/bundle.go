package resources

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

const bundleCompleteMarker = ".stella-builtin-bundle.json"

type bundleMarker struct {
	Revision string `json:"revision"`
}

// BundlePath returns the deterministic projection path for this registry under
// stellaHome. The path is derived only from a validated manifest revision.
func (r *Registry) BundlePath(stellaHome string) (string, error) {
	if err := validateBuiltinManifest(r.manifest); err != nil {
		return "", err
	}
	if stellaHome == "" {
		return "", errors.New("empty Stella home")
	}
	return filepath.Join(filepath.Clean(stellaHome), "bundles", r.manifest.Revision), nil
}

// InstallBuiltinBundle materializes the exact embedded bundle below stellaHome.
// It returns only a fully verified revision and leaves an existing verified tree
// untouched so repeat installs do not rewrite operator-visible files.
func (r *Registry) InstallBuiltinBundle(stellaHome string) (string, error) {
	return r.installBuiltinBundle(stellaHome, os.Rename)
}

func (r *Registry) installBuiltinBundle(stellaHome string, rename func(string, string) error) (string, error) {
	final, err := r.BundlePath(stellaHome)
	if err != nil {
		return "", err
	}
	if err := r.VerifyBuiltinBundle(stellaHome); err == nil {
		return final, nil
	}

	bundlesDir := filepath.Dir(final)
	if err := ensureBundlesDir(bundlesDir, true); err != nil {
		return "", err
	}
	unlock, err := lockBundleInstall(filepath.Join(bundlesDir, "."+r.manifest.Revision+".lock"))
	if err != nil {
		return "", fmt.Errorf("lock builtin bundle installation: %w", err)
	}
	defer func() { _ = unlock() }()
	// The first verification is only a fast path. Re-check under the cross-process
	// lock so a repairer cannot act on stale invalid state and delete a verified
	// bundle published by the previous lock owner.
	if err := r.VerifyBuiltinBundle(stellaHome); err == nil {
		return final, nil
	}
	temporary, err := os.MkdirTemp(bundlesDir, "."+r.manifest.Revision+".tmp-")
	if err != nil {
		return "", fmt.Errorf("create builtin bundle temporary directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	if err := r.writeBuiltinBundle(temporary); err != nil {
		return "", err
	}
	if err := verifyBundleAt(temporary, r.manifest); err != nil {
		return "", fmt.Errorf("verify temporary builtin bundle: %w", err)
	}

	_, statErr := os.Lstat(final)
	if errors.Is(statErr, os.ErrNotExist) {
		if err := rename(temporary, final); err != nil {
			if verifyErr := r.VerifyBuiltinBundle(stellaHome); verifyErr == nil {
				return final, nil
			}
			return "", fmt.Errorf("publish builtin bundle: %w", err)
		}
		return r.verifiedPublishedBundle(stellaHome, final)
	}
	if statErr != nil {
		return "", fmt.Errorf("inspect invalid builtin bundle %q: %w", final, statErr)
	}

	quarantine, err := os.MkdirTemp(bundlesDir, "."+r.manifest.Revision+".invalid-")
	if err != nil {
		return "", fmt.Errorf("reserve invalid builtin bundle quarantine: %w", err)
	}
	if err := os.Remove(quarantine); err != nil {
		return "", fmt.Errorf("prepare invalid builtin bundle quarantine %q: %w", quarantine, err)
	}
	if err := rename(final, quarantine); err != nil {
		return "", fmt.Errorf("quarantine invalid builtin bundle %q: %w", final, err)
	}

	if err := rename(temporary, final); err != nil {
		publishErr := fmt.Errorf("publish repaired builtin bundle: %w", err)
		if _, statErr := os.Lstat(final); errors.Is(statErr, os.ErrNotExist) {
			if restoreErr := rename(quarantine, final); restoreErr != nil {
				return "", errors.Join(publishErr, fmt.Errorf("restore quarantined builtin bundle %q: %w", quarantine, restoreErr))
			}
			return "", fmt.Errorf("%w; restored previous invalid bundle", publishErr)
		} else if statErr != nil {
			return "", errors.Join(publishErr, fmt.Errorf("inspect publication destination before restoring quarantine %q: %w", quarantine, statErr))
		}
		return "", errors.Join(publishErr, fmt.Errorf("restore quarantined builtin bundle %q skipped: publication destination exists", quarantine))
	}
	published, err := r.verifiedPublishedBundle(stellaHome, final)
	if err != nil {
		return "", err
	}
	// The old invalid tree is retained until the replacement has been published
	// and verified. Cleanup is best effort because it cannot affect the authority
	// of the verified final pathname.
	_ = os.RemoveAll(quarantine)
	return published, nil
}

// VerifyBuiltinBundle proves that the projected revision contains exactly the
// manifest files and completion marker, with the expected content and modes.
func (r *Registry) VerifyBuiltinBundle(stellaHome string) error {
	final, err := r.BundlePath(stellaHome)
	if err != nil {
		return err
	}
	if err := ensureBundlesDir(filepath.Dir(final), false); err != nil {
		return err
	}
	if err := verifyBundleAt(final, r.manifest); err != nil {
		return fmt.Errorf("verify builtin bundle %q: %w", final, err)
	}
	return nil
}

func (r *Registry) verifiedPublishedBundle(stellaHome, final string) (string, error) {
	if err := r.VerifyBuiltinBundle(stellaHome); err != nil {
		return "", fmt.Errorf("verify published builtin bundle %q: %w", final, err)
	}
	return final, nil
}

// ensureBundlesDir validates only Stella's immediate derived projection. The
// configured home is trusted and may legitimately resolve through symlinks.
func ensureBundlesDir(bundlesDir string, create bool) error {
	info, err := os.Lstat(bundlesDir)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := os.MkdirAll(bundlesDir, 0o755); err != nil {
			return fmt.Errorf("create builtin bundles directory: %w", err)
		}
		info, err = os.Lstat(bundlesDir)
	}
	if err != nil {
		return fmt.Errorf("stat builtin bundles directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("builtin bundles directory %q must be a non-symlink directory", bundlesDir)
	}
	return nil
}

func (r *Registry) writeBuiltinBundle(root string) error {
	for _, skill := range r.BuiltinSkills() {
		for _, file := range skill.Files {
			data, actual, err := r.ReadBuiltinSkillFile(skill.Name, file.Path)
			if err != nil {
				return err
			}
			if actual != file {
				return fmt.Errorf("builtin descriptor changed while installing %q/%q", skill.Name, file.Path)
			}
			target := filepath.Join(root, filepath.FromSlash(skill.Root), filepath.FromSlash(file.Path))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create builtin skill directory for %q: %w", file.Path, err)
			}
			if err := os.WriteFile(target, data, manifestSourceMode(file.Mode)); err != nil {
				return fmt.Errorf("write builtin skill %q/%q: %w", skill.Name, file.Path, err)
			}
			if err := os.Chmod(target, manifestSourceMode(file.Mode)); err != nil {
				return fmt.Errorf("set builtin skill mode %q/%q: %w", skill.Name, file.Path, err)
			}
		}
	}
	marker, err := json.Marshal(bundleMarker{Revision: r.manifest.Revision})
	if err != nil {
		return err
	}
	marker = append(marker, '\n')
	markerPath := filepath.Join(root, bundleCompleteMarker)
	if err := os.WriteFile(markerPath, marker, 0o444); err != nil {
		return fmt.Errorf("write builtin bundle completion marker: %w", err)
	}
	if err := os.Chmod(markerPath, 0o444); err != nil {
		return fmt.Errorf("set builtin bundle completion marker mode: %w", err)
	}
	return nil
}

func verifyBundleAt(root string, manifest BuiltinManifest) error {
	if err := validateBuiltinManifest(manifest); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("bundle root is not a directory")
	}

	want := make(map[string]BuiltinSkillFile)
	for _, skill := range manifest.Skills {
		for _, file := range skill.Files {
			name := pathForBundle(skill.Root, file.Path)
			want[name] = file
		}
	}
	seen := make(map[string]struct{}, len(want))
	markerSeen := false
	err = filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !canonicalBuiltinPath(rel) {
			return fmt.Errorf("non-canonical bundle path %q", rel)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("bundle path %q is a symlink", rel)
		}
		if rel == bundleCompleteMarker {
			markerSeen = true
			return verifyBundleMarker(filename, manifest.Revision)
		}
		if entry.IsDir() {
			return nil
		}
		file, ok := want[rel]
		if !ok {
			return fmt.Errorf("unexpected bundle file %q", rel)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != manifestSourceMode(file.Mode).Perm() {
			return fmt.Errorf("bundle file %q has unexpected mode %s", rel, info.Mode())
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		if int64(len(data)) != file.Size || sha256Hex(data) != file.Digest {
			return fmt.Errorf("bundle file %q does not match manifest", rel)
		}
		seen[rel] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(want) {
		var missing []string
		for file := range want {
			if _, ok := seen[file]; !ok {
				missing = append(missing, file)
			}
		}
		sort.Strings(missing)
		return fmt.Errorf("bundle is missing files: %v", missing)
	}
	if !markerSeen {
		return errors.New("bundle completion marker is missing")
	}
	return nil
}

func verifyBundleMarker(filename, revision string) error {
	info, err := os.Lstat(filename)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o444 {
		return fmt.Errorf("bundle completion marker has unexpected mode %s", info.Mode())
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	var marker bundleMarker
	if err := json.Unmarshal(data, &marker); err != nil || marker.Revision != revision {
		return errors.New("bundle completion marker does not match revision")
	}
	return nil
}

func pathForBundle(root, file string) string { return root + "/" + file }
