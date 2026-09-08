package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/CherryHQ/stella/internal/plugin/agentpackage"
)

const (
	packageSkillFileLimit  = 32 << 20
	packageSkillTotalLimit = 128 << 20
)

// ContentStore serializes publication and future reachability scans for one
// package CAS root. The pointer is shared by shallow transaction-bound Service
// copies; never embed this mutex in Service itself.
type ContentStore struct {
	root string
	mu   sync.Mutex
}

// ContentOwnerSnapshot is the small cross-package ownership projection needed
// by package cleanup. The owner provider must return immutable IDs and digests
// captured from building/active/closing PluginContexts and active turns.
// Secrets, filesystem paths, and runtime handles never cross this boundary.
type ContentOwnerSnapshot struct {
	PluginIDs []string
	Digests   []string
}

// ContentOwnerSnapshotFunc is called while the ContentStore lock is held. It
// must only take the short admission/lifecycle snapshot lock and must not read
// or mutate package files. This fixed ordering is Store -> admission.
type ContentOwnerSnapshotFunc func(context.Context) (ContentOwnerSnapshot, error)

func NewContentStore(root string) (*ContentStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("plugin: content store root is required")
	}
	return &ContentStore{root: root}, nil
}

// ReadPackageSkill performs the narrow exact read used by runtime Skill
// loading. It holds the content-store lock while verifying and copying the
// selected immutable tree, so cleanup cannot race an active read.
func (s *ContentStore) ReadPackageSkill(ctx context.Context, digest, name string) (map[string][]byte, map[string]fs.FileMode, error) {
	if s == nil || s.root == "" {
		return nil, nil, errors.New("plugin: content store unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if !validStoreComponent(name) || !validStoreDigest(digest) {
		return nil, nil, errors.New("plugin: invalid package Skill reference")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	packagePath := filepath.Join(s.root, digest)
	actual, err := agentpackage.DirectoryDigest(packagePath)
	if err != nil {
		return nil, nil, fmt.Errorf("verify package digest: %w", err)
	}
	if actual != "sha256:"+digest {
		return nil, nil, errors.New("plugin: package digest mismatch")
	}
	storeRoot, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, nil, fmt.Errorf("open package store: %w", err)
	}
	defer func() { _ = storeRoot.Close() }()
	packageRoot, err := storeRoot.OpenRoot(digest)
	if err != nil {
		return nil, nil, fmt.Errorf("open package: %w", err)
	}
	defer func() { _ = packageRoot.Close() }()
	skillRoot, err := packageRoot.OpenRoot(path.Join("skills", name))
	if err != nil {
		return nil, nil, fmt.Errorf("open package Skill: %w", err)
	}
	defer func() { _ = skillRoot.Close() }()
	files := make(map[string][]byte)
	modes := make(map[string]fs.FileMode)
	var total int64
	err = fs.WalkDir(skillRoot.FS(), ".", func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("skill file %q is a symlink", filename)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > packageSkillFileLimit || total+info.Size() > packageSkillTotalLimit {
			return fmt.Errorf("skill file %q exceeds reader limits", filename)
		}
		file, err := skillRoot.Open(filename)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, packageSkillFileLimit+1))
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if int64(len(data)) != info.Size() || int64(len(data)) > packageSkillFileLimit {
			return fmt.Errorf("skill file %q changed while reading", filename)
		}
		total += int64(len(data))
		files[filename] = data
		modes[filename] = info.Mode()
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("read package Skill: %w", err)
	}
	if len(files) == 0 {
		return nil, nil, errors.New("plugin: package Skill is empty")
	}
	return files, modes, nil
}

func validStoreComponent(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value && !strings.ContainsAny(value, `/\\`)
}

func validStoreDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

// withPublished holds the store lock from staging through the caller's
// database CAS. This gives GC a single ordering rule: Store, then admission.
// The callback must stay short after publication and must not perform unrelated
// file I/O.
func (s *ContentStore) withPublished(source string, fn func(agentpackage.PublishedPackage) error) error {
	if s == nil || fn == nil {
		return errors.New("plugin: content store unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	published, err := agentpackage.PublishDirectory(source, s.root)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidDefinition, err)
	}
	return fn(published)
}

type quarantinedPackage struct {
	digest string
	path   string
}

type contentCleanupSelection struct {
	keep       []string
	candidates []string
}

// cleanup serializes reachability collection with publication and exact reads.
// Collection and quarantine happen under the short Store lock; byte deletion
// and the database finalizer happen after it is released. A failed removal is
// deliberately returned before finalization, leaving the retired row retryable.
func (s *ContentStore) cleanup(ctx context.Context, collect func(context.Context) (contentCleanupSelection, error), finalize func(context.Context, map[string]bool) error) error {
	if s == nil || s.root == "" || collect == nil || finalize == nil {
		return errors.New("plugin: content store unavailable")
	}
	if ctx == nil {
		return errors.New("plugin: cleanup context is nil")
	}
	s.mu.Lock()
	selection, err := collect(ctx)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	keep := make(map[string]struct{}, len(selection.keep))
	for _, digest := range selection.keep {
		digest = strings.TrimPrefix(digest, "sha256:")
		if validStoreDigest(digest) {
			keep[digest] = struct{}{}
		}
	}
	unique := make(map[string]struct{}, len(selection.candidates))
	restored := false
	for _, digest := range selection.candidates {
		digest = strings.TrimPrefix(digest, "sha256:")
		if validStoreDigest(digest) {
			if _, referenced := keep[digest]; referenced {
				continue
			}
			unique[digest] = struct{}{}
		}
	}
	entries, readErr := os.ReadDir(s.root)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		s.mu.Unlock()
		return readErr
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if validStoreDigest(entry.Name()) {
			if _, referenced := keep[entry.Name()]; !referenced {
				unique[entry.Name()] = struct{}{}
			}
			continue
		}
		if digest, ok := quarantineDigest(entry.Name()); ok {
			if _, referenced := keep[digest]; referenced {
				live := filepath.Join(s.root, digest)
				if _, liveErr := os.Stat(live); errors.Is(liveErr, os.ErrNotExist) {
					if err := os.Rename(filepath.Join(s.root, entry.Name()), live); err != nil {
						s.mu.Unlock()
						return fmt.Errorf("restore referenced package %s: %w", digest, err)
					}
					restored = true
				} else if liveErr == nil {
					// A republish won the live name while the old quarantine was
					// pending. The digest identity is the same, so the stale
					// quarantine can be removed after leaving the live root intact.
					unique[digest] = struct{}{}
				}
				continue
			}
			unique[digest] = struct{}{}
		}
	}
	if restored {
		if err := syncContentRoot(s.root); err != nil {
			s.mu.Unlock()
			return fmt.Errorf("sync restored package root: %w", err)
		}
	}
	ordered := make([]string, 0, len(unique))
	for digest := range unique {
		ordered = append(ordered, digest)
	}
	sort.Strings(ordered)
	quarantined := make([]quarantinedPackage, 0, len(ordered))
	removed := make(map[string]bool, len(ordered))
	for _, digest := range ordered {
		if err := ctx.Err(); err != nil {
			s.restoreQuarantine(quarantined)
			_ = syncContentRoot(s.root)
			s.mu.Unlock()
			return err
		}
		quarantinePath, found, err := s.quarantinePath(digest)
		if err != nil {
			s.restoreQuarantine(quarantined)
			_ = syncContentRoot(s.root)
			s.mu.Unlock()
			return err
		}
		if found {
			quarantined = append(quarantined, quarantinedPackage{digest: digest, path: quarantinePath})
		} else {
			// Absence of both the live digest and its quarantine is the only
			// durable evidence available after a restart; treat it as removed.
			removed[digest] = true
		}
	}
	if len(quarantined) != 0 {
		if err := syncContentRoot(s.root); err != nil {
			s.restoreQuarantine(quarantined)
			_ = syncContentRoot(s.root)
			s.mu.Unlock()
			return fmt.Errorf("sync quarantined package root: %w", err)
		}
	}
	s.mu.Unlock()

	for _, item := range quarantined {
		if err := os.RemoveAll(item.path); err != nil {
			return fmt.Errorf("remove quarantined package %s: %w", item.digest, err)
		}
		removed[item.digest] = true
	}
	return finalize(ctx, removed)
}

// quarantinePath moves a digest directory out of the live namespace while the
// Store lock is held. Existing quarantine directories are resumed after a
// previous cleanup failure, so a retry never mistakes pending bytes for a
// successful removal.
func (s *ContentStore) quarantinePath(digest string) (string, bool, error) {
	prefix := ".quarantine-" + digest + "-"
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			return filepath.Join(s.root, entry.Name()), true, nil
		}
	}
	packagePath := filepath.Join(s.root, digest)
	if _, err := os.Stat(packagePath); errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	} else if err != nil {
		return "", false, err
	}
	tmp, err := os.MkdirTemp(s.root, prefix)
	if err != nil {
		return "", false, err
	}
	if err := os.Remove(tmp); err != nil {
		return "", false, err
	}
	if err := os.Rename(packagePath, tmp); err != nil {
		return "", false, err
	}
	return tmp, true, nil
}

func (s *ContentStore) restoreQuarantine(items []quarantinedPackage) {
	for _, item := range items {
		if _, err := os.Stat(item.path); err != nil {
			continue
		}
		live := filepath.Join(s.root, item.digest)
		if _, err := os.Stat(live); err == nil {
			continue
		}
		_ = os.Rename(item.path, live)
	}
}

func quarantineDigest(name string) (string, bool) {
	const prefix = ".quarantine-"
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	value := strings.TrimPrefix(name, prefix)
	if len(value) < 65 || value[64] != '-' || !validStoreDigest(value[:64]) {
		return "", false
	}
	return value[:64], true
}

func syncContentRoot(root string) error {
	file, err := os.Open(root)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return file.Sync()
}
