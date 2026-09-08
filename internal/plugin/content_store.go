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
