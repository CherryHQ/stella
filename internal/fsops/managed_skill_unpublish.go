package fsops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

// UnpublishManagedSkillAt removes one direct managed-selection link. The
// immutable revision it selected is deliberately retained for later GC.
//
// POSIX has no compare-and-unlink primitive. Managed writers serialize above
// this layer; arbitrary same-UID writers follow ordinary POSIX winner ordering.
func (r *Root) UnpublishManagedSkillAt(ctx context.Context, catalogRoot, name, expectedDigest string) (err error) {
	if err := managedSkillPlatformSupported(); err != nil {
		return err
	}
	if !validCatalogRoot(catalogRoot) || !validManagedSkillName(name) || !validManagedSkillDigest(expectedDigest) {
		return errors.New("fsops: invalid managed skill unpublication")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	catalog, err := r.openPinnedManagedSkillCatalog(catalogRoot)
	if err != nil {
		return err
	}
	unlinked := false
	defer func() {
		closeErr := catalog.Close()
		if r.closeManagedSkillCatalog != nil {
			closeErr = errors.Join(closeErr, r.closeManagedSkillCatalog(catalog))
		}
		if closeErr != nil {
			if unlinked {
				err = managedSkillOutcomeUnknown(errors.Join(err, closeErr))
				return
			}
			err = errors.Join(err, closeErr)
		}
	}()
	if r.afterManagedSkillCatalogPin != nil {
		r.afterManagedSkillCatalogPin()
	}

	got, err := catalog.validManagedSkillLinkAt(".", name)
	if err != nil {
		return managedSkillConflict(fmt.Errorf("inspect direct target %q: %w", name, err))
	}
	if got != expectedDigest {
		return managedSkillConflict(fmt.Errorf("direct target %q selects digest %q, want %q", name, got, expectedDigest))
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	entry := path.Join(".", name)
	// This unlink is the linearization point. Do not use RemoveAll: an ordinary
	// directory that wins a same-UID race must never be recursively deleted.
	if err := catalog.root.Remove(entry); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return managedSkillConflict(fmt.Errorf("direct target %q disappeared: %w", name, err))
		}
		if info, lstatErr := catalog.root.Lstat(entry); lstatErr == nil && info.IsDir() {
			return managedSkillConflict(fmt.Errorf("direct target %q became an ordinary directory: %w", name, err))
		}
		return fmt.Errorf("fsops: unlink managed skill target %q: %w", name, err)
	}
	unlinked = true
	if r.afterManagedSkillUnlink != nil {
		r.afterManagedSkillUnlink()
	}
	if err := ctx.Err(); err != nil {
		return managedSkillOutcomeUnknown(err)
	}
	if _, err := catalog.root.Lstat(entry); !errors.Is(err, fs.ErrNotExist) {
		if err == nil {
			err = errors.New("direct target was replaced after unlink")
		}
		return managedSkillOutcomeUnknown(fmt.Errorf("verify unlinked managed skill target %q: %w", name, err))
	}
	if err := catalog.syncDirectory("."); err != nil {
		return managedSkillOutcomeUnknown(fmt.Errorf("sync unlinked managed skill target %q: %w", name, err))
	}
	return nil
}

func managedSkillConflict(err error) error {
	return fmt.Errorf("fsops: managed skill unpublication conflict: %w: %w", sandbox.ErrManagedSkillConflict, err)
}
