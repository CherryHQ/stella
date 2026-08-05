package fsops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
)

func (r *Root) validateManagedSkillOccupantAt(catalogRoot, name string) error {
	entry := path.Join(catalogRoot, name)
	info, err := r.root.Lstat(entry)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		return fmt.Errorf("fsops: managed skill target %q is not a managed symlink", name)
	}
	_, err = r.validManagedSkillLinkAt(catalogRoot, name)
	return err
}

func (r *Root) swapManagedSkillTargetAt(ctx context.Context, catalogRoot, name, digest string) (err error) {
	if err = r.validateManagedSkillOccupantAt(catalogRoot, name); err != nil {
		return err
	}
	if err = r.validManagedSkillRevisionAt(catalogRoot, name, digest); err != nil {
		return err
	}
	var temporary string
	for range 16 {
		candidate, e := r.createManagedSkillTemporaryLinkAt(catalogRoot, name, digest)
		if e == nil {
			temporary = candidate
			break
		}
		if !errors.Is(e, fs.ErrExist) {
			return e
		}
	}
	if temporary == "" {
		return errors.New("fsops: could not allocate managed skill temporary link")
	}
	published := false
	defer func() {
		if !published {
			_ = r.root.Remove(temporary)
		}
	}()
	if err = r.syncDirectory(catalogRoot); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// The destination was proved to be absent or a managed link above. POSIX
	// rename replaces that link atomically; post-publication verification below
	// prevents a same-UID writer from turning this check into false success.
	if err = r.root.Rename(temporary, path.Join(catalogRoot, name)); err != nil {
		return fmt.Errorf("fsops: rename managed skill target: %w", err)
	}
	published = true
	if r.afterManagedSkillRename != nil {
		r.afterManagedSkillRename()
	}
	if got, e := r.validManagedSkillLinkAt(catalogRoot, name); e != nil || got != digest {
		if e == nil {
			e = fmt.Errorf("published digest differs")
		}
		return managedSkillOutcomeUnknown(e)
	}
	if err = ctx.Err(); err != nil {
		return managedSkillOutcomeUnknown(err)
	}
	if err = r.syncDirectory(catalogRoot); err != nil {
		return managedSkillOutcomeUnknown(err)
	}
	return nil
}

func (r *Root) createManagedSkillTemporaryLinkAt(catalogRoot, name, digest string) (string, error) {
	// Names are private control entries in the catalog directory; the target is
	// relative to that directory, so moving a catalog root never leaks authority.
	for range 16 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		candidate := path.Join(catalogRoot, ".stella-skill-target-"+hex.EncodeToString(random[:]))
		if err := r.root.Symlink(managedSkillRevisionPath(name, digest), candidate); err == nil {
			return candidate, nil
		} else if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", fs.ErrExist
}
