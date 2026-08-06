package home

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

// SkillRoot is an opaque, typed mutable Skill catalog identity. Its
// constructors deliberately admit only the four supported user/system scopes;
// group catalog scopes do not exist.
type SkillRoot struct{ key Key }

func newSkillRoot(key Key) (*SkillRoot, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	switch key.Kind {
	case SystemSkillRoot, SystemAgentSkillRoot:
	case PrincipalHome:
		if key.PrincipalKind != UserPrincipal {
			return nil, errors.New("home: Skill catalog requires a user principal")
		}
	case AgentHome:
		if key.PrincipalKind != UserPrincipal {
			return nil, errors.New("home: Skill catalog requires a user principal")
		}
	default:
		return nil, errors.New("home: unsupported Skill catalog scope")
	}
	return &SkillRoot{key: key}, nil
}

// SystemSkillCatalog identifies the deployment-wide mutable Skill catalog.
func SystemSkillCatalog() *SkillRoot {
	root, _ := newSkillRoot(SystemSkills())
	return root
}

// SystemAgentSkillCatalog identifies one Agent's deployment-wide Skill catalog.
func SystemAgentSkillCatalog(agentID string) (*SkillRoot, error) {
	return newSkillRoot(SystemAgentSkills(agentID))
}

// UserSkillCatalog identifies one user's shared mutable Skill catalog.
func UserSkillCatalog(userID string) (*SkillRoot, error) {
	return newSkillRoot(Principal(UserPrincipal, userID))
}

// UserAgentSkillCatalog identifies one user's Agent-specific mutable Skill catalog.
func UserAgentSkillCatalog(userID, agentID string) (*SkillRoot, error) {
	return newSkillRoot(Agent(UserPrincipal, userID, agentID))
}

// skillFilesystemStore is intentionally private: Store implementations can
// provide this trusted server-side capability without gaining a general
// writable attachment or host-path API.
type skillFilesystemStore interface {
	openSkillFilesystem(Record, *SkillRoot) (sandbox.Filesystem, error)
}
type existingSkillFilesystemStore interface {
	openExistingSkillFilesystem(Record, *SkillRoot) (sandbox.Filesystem, error)
}

// UseSkillFilesystem grants one callback a short-lived read-write filesystem
// rooted at sandbox.PathWorkspace. /workspace is exactly the selected Skill
// catalog, never the surrounding Home. The filesystem is closed before this
// method returns, including when the callback panics.
func (r *Registry) UseSkillFilesystem(ctx context.Context, root *SkillRoot, use func(sandbox.Filesystem) error) (err error) {
	return r.useSkillFilesystem(ctx, root, use, true)
}

// UseExistingSkillFilesystem is UseSkillFilesystem without creating a Home.
// It returns exists=false when the catalog does not have a ready Home yet.
func (r *Registry) UseExistingSkillFilesystem(ctx context.Context, root *SkillRoot, use func(sandbox.Filesystem) error) (exists bool, err error) {
	if use == nil {
		return false, errors.New("home: Skill filesystem callback is required")
	}
	err = r.useSkillFilesystem(ctx, root, func(filesystem sandbox.Filesystem) error {
		exists = true
		return use(filesystem)
	}, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return exists, err
}

func (r *Registry) useSkillFilesystem(ctx context.Context, root *SkillRoot, use func(sandbox.Filesystem) error, ensure bool) (err error) {
	if root == nil {
		return errors.New("home: Skill filesystem root is required")
	}
	if use == nil {
		return errors.New("home: Skill filesystem callback is required")
	}
	if _, err := newSkillRoot(root.key); err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("home: Skill filesystem context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	unlock, err := r.lockOwnerKeys(ctx, skillRootOwnerKeys(root.key))
	if err != nil {
		return err
	}
	defer unlock()

	// Ensure and all catalog I/O stay outside transactions and advisory locks.
	// The bounded owner stripes span Ensure, open, callback, and close so an
	// owner deletion cannot purge a catalog while it is in use.
	var record Record
	if ensure {
		record, err = r.Ensure(ctx, root.key)
		if err != nil {
			return fmt.Errorf("home: ensure Skill catalog: %w", err)
		}
	} else {
		row, getErr := r.get(ctx, root.key)
		if getErr != nil {
			return getErr
		}
		record, err = r.decode(row)
		if err != nil {
			return err
		}
		if record.Key != root.key || record.State != StateReady {
			return errors.New("home: stale or unavailable Skill filesystem")
		}
	}
	store, err := r.skillFilesystemStore(ctx, record, root)
	if err != nil {
		return err
	}
	var filesystem sandbox.Filesystem
	if ensure {
		filesystem, err = store.openSkillFilesystem(record, root)
	} else {
		existing, ok := store.(existingSkillFilesystemStore)
		if !ok {
			return errors.New("home: Store does not support existing Skill filesystems")
		}
		filesystem, err = existing.openExistingSkillFilesystem(record, root)
	}
	if err != nil {
		return fmt.Errorf("home: open Skill filesystem: %w", err)
	}
	if filesystem == nil {
		return errors.New("home: Store returned a nil Skill filesystem")
	}
	defer func() { err = errors.Join(err, filesystem.Close()) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	return use(filesystem)
}

func skillRootOwnerKeys(key Key) []string {
	switch key.Kind {
	case SystemAgentSkillRoot:
		return []string{ownerLockKey(OwnerAgent, key.AgentID)}
	case PrincipalHome:
		return []string{ownerLockKey(OwnerUser, key.PrincipalID)}
	case AgentHome:
		// Match WorkspaceView's established agent-then-user ownership vocabulary.
		return []string{ownerLockKey(OwnerAgent, key.AgentID), ownerLockKey(OwnerUser, key.PrincipalID)}
	default:
		return nil // singleton system catalog has no deletion path
	}
}

// skillFilesystemStore performs the final durable identity/state/store/locator
// check immediately before a Store opens catalog bytes. It returns only the
// private filesystem facet, never a Record, locator, attachment, or host path.
func (r *Registry) skillFilesystemStore(ctx context.Context, record Record, root *SkillRoot) (skillFilesystemStore, error) {
	row, err := r.q.GetStorageHome(ctx, record.ID)
	if err != nil {
		return nil, fmt.Errorf("home: revalidate Skill filesystem: %w", err)
	}
	persisted, err := r.decode(row)
	if err != nil {
		return nil, err
	}
	if persisted.Key != root.key || persisted.Key != record.Key || persisted.StoreID != record.StoreID || persisted.Locator != record.Locator || persisted.State != StateReady {
		return nil, errors.New("home: stale or unavailable Skill filesystem")
	}
	store, ok := r.stores[persisted.StoreID].(skillFilesystemStore)
	if !ok {
		return nil, fmt.Errorf("home: Store %q does not support Skill filesystems", persisted.StoreID)
	}
	return store, nil
}
