package fsops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

const managedSkillRevisionsDir = ".stella-revisions"

// ManagedSkillTarget reports the digest selected by a managed Skill link. An
// absent name or ordinary directory remains an ordinary POSIX Skill, not an error.
func (r *Root) ManagedSkillTarget(ctx context.Context, name string) (digest string, managed bool, err error) {
	return r.ManagedSkillTargetAt(ctx, ".", name)
}

// ManagedSkillTargetAt inspects a direct entry under a canonical relative
// catalog root. It remains intentionally specific to the managed Skill layout.
func (r *Root) ManagedSkillTargetAt(ctx context.Context, catalogRoot, name string) (digest string, managed bool, err error) {
	if catalogRoot == "" {
		catalogRoot = "."
	}
	if path.Clean(catalogRoot) != catalogRoot || path.IsAbs(catalogRoot) || catalogRoot == ".." || strings.HasPrefix(catalogRoot, "../") {
		return "", false, fmt.Errorf("fsops: invalid managed skill catalog root %q", catalogRoot)
	}
	if err := managedSkillPlatformSupported(); err != nil {
		return "", false, err
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if !validManagedSkillName(name) {
		return "", false, fmt.Errorf("fsops: invalid managed skill name %q", name)
	}
	entry := path.Join(catalogRoot, name)
	info, err := r.root.Lstat(entry)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("fsops: lstat managed skill target: %w", err)
	}
	if info.IsDir() {
		return "", false, nil
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		return "", false, fmt.Errorf("fsops: managed skill target %q is not a directory or symlink", name)
	}
	digest, err = r.validManagedSkillLinkAt(catalogRoot, name)
	if err != nil {
		return "", false, err
	}
	return digest, true, nil
}

// SwapManagedSkillTarget atomically selects an already-published revision. It
// never accepts a caller-controlled link target or replaces an ordinary Skill.
func (r *Root) SwapManagedSkillTarget(ctx context.Context, name, digest string) (err error) {
	if err := managedSkillPlatformSupported(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validManagedSkillName(name) {
		return fmt.Errorf("fsops: invalid managed skill name %q", name)
	}
	if !validManagedSkillDigest(digest) {
		return fmt.Errorf("fsops: invalid managed skill digest")
	}
	if err := r.validManagedSkillRevision(name, digest); err != nil {
		return err
	}
	if err := r.validateManagedSkillOccupant(name); err != nil {
		return err
	}

	temporary, err := r.createManagedSkillTemporaryLink(name, digest)
	if err != nil {
		return err
	}
	if r.afterManagedSkillTemporaryLink != nil {
		r.afterManagedSkillTemporaryLink(temporary)
	}
	published := false
	defer func() {
		if !published {
			if removeErr := r.root.Remove(temporary); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) && err == nil {
				err = fmt.Errorf("fsops: remove managed skill temporary link: %w", removeErr)
			}
		}
	}()

	if err := r.syncManagedSkillRoot(); err != nil {
		return fmt.Errorf("fsops: sync managed skill temporary link: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.root.Rename(temporary, name); err != nil {
		return fmt.Errorf("fsops: rename managed skill target: %w", err)
	}
	published = true
	if r.afterManagedSkillRename != nil {
		r.afterManagedSkillRename()
	}
	if finalDigest, err := r.validManagedSkillLink(name); err != nil || finalDigest != digest {
		if err == nil {
			err = fmt.Errorf("published digest %q differs from requested digest %q", finalDigest, digest)
		}
		return managedSkillOutcomeUnknown(fmt.Errorf("verify managed skill target: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return managedSkillOutcomeUnknown(err)
	}
	if err := r.syncManagedSkillRoot(); err != nil {
		return managedSkillOutcomeUnknown(err)
	}
	return nil
}

func (r *Root) validateManagedSkillOccupant(name string) error {
	info, err := r.root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("fsops: lstat managed skill target: %w", err)
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		return fmt.Errorf("fsops: managed skill target %q is not a managed symlink", name)
	}
	_, err = r.validManagedSkillLink(name)
	return err
}

func (r *Root) validManagedSkillLink(name string) (string, error) {
	return r.validManagedSkillLinkAt(".", name)
}

func (r *Root) validManagedSkillLinkAt(catalogRoot, name string) (string, error) {
	entry := path.Join(catalogRoot, name)
	info, err := r.root.Lstat(entry)
	if err != nil {
		return "", fmt.Errorf("fsops: lstat managed skill target: %w", err)
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		return "", fmt.Errorf("fsops: managed skill target %q is not a symlink", name)
	}
	var target string
	err = nil
	for range 4 {
		target, err = r.root.Readlink(entry)
		if !errors.Is(err, syscall.EINVAL) {
			break
		}
	}
	if err != nil {
		return "", fmt.Errorf("fsops: read managed skill target: %w", err)
	}
	prefix := managedSkillRevisionsDir + "/" + name + "/"
	if !strings.HasPrefix(target, prefix) {
		return "", fmt.Errorf("fsops: managed skill target %q has an invalid link", name)
	}
	digest := strings.TrimPrefix(target, prefix)
	if target != managedSkillRevisionPath(name, digest) || !validManagedSkillDigest(digest) {
		return "", fmt.Errorf("fsops: managed skill target %q has an invalid link", name)
	}
	if err := r.validManagedSkillRevisionAt(catalogRoot, name, digest); err != nil {
		return "", err
	}
	return digest, nil
}

func (r *Root) validManagedSkillRevision(name, digest string) error {
	return r.validManagedSkillRevisionAt(".", name, digest)
}

func (r *Root) validManagedSkillRevisionAt(catalogRoot, name, digest string) error {
	for _, component := range []string{managedSkillRevisionsDir, path.Join(managedSkillRevisionsDir, name), managedSkillRevisionPath(name, digest)} {
		component = path.Join(catalogRoot, component)
		info, err := r.root.Lstat(component)
		if err != nil {
			return fmt.Errorf("fsops: lstat managed skill revision: %w", err)
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("fsops: managed skill revision component %q is not a real directory", component)
		}
	}
	return nil
}

func (r *Root) createManagedSkillTemporaryLink(name, digest string) (string, error) {
	target := managedSkillRevisionPath(name, digest)
	for range 16 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", fmt.Errorf("fsops: create managed skill temporary name: %w", err)
		}
		temporary := ".stella-skill-target-" + hex.EncodeToString(random[:])
		if err := r.root.Symlink(target, temporary); err == nil {
			return temporary, nil
		} else if !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("fsops: create managed skill temporary link: %w", err)
		}
	}
	return "", errors.New("fsops: could not allocate managed skill temporary link")
}

func (r *Root) syncManagedSkillRoot() error {
	directory, err := r.root.Open(".")
	if err != nil {
		return fmt.Errorf("open root directory: %w", err)
	}
	sync := r.syncRootDirectory
	if sync == nil {
		sync = func(f *os.File) error { return f.Sync() }
	}
	syncErr := sync(directory)
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func managedSkillRevisionPath(name, digest string) string {
	return managedSkillRevisionsDir + "/" + name + "/" + digest
}

func validManagedSkillName(name string) bool {
	if len(name) == 0 || len(name) > 64 || name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	previousHyphen := false
	for _, c := range name {
		if c == '-' {
			if previousHyphen {
				return false
			}
			previousHyphen = true
			continue
		}
		previousHyphen = false
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func validManagedSkillDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	for _, c := range digest {
		if (c < 'a' || c > 'f') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func managedSkillOutcomeUnknown(err error) error {
	return fmt.Errorf("fsops: managed skill target publication outcome unknown: %w: %w", sandbox.ErrOutcomeUnknown, err)
}
