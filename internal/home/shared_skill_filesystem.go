package home

import (
	"context"
	"errors"
	"fmt"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

// sharedSkillFilesystemStore is intentionally private: a Store may implement
// this trusted server-side capability without adding a general host-path or
// writable attachment contract to Home.
type sharedSkillFilesystemStore interface {
	openSharedSkillFilesystem(Record) (sandbox.Filesystem, error)
}

// UseSharedSkillFilesystem grants one callback a short-lived read-write view
// of exactly one shared Skill root at sandbox.PathWorkspace. It is for trusted
// server-side publishers; ordinary Home attachments remain read-only.
func (r *Registry) UseSharedSkillFilesystem(ctx context.Context, key Key, use func(sandbox.Filesystem) error) (err error) {
	if use == nil {
		return errors.New("home: shared Skill filesystem callback is required")
	}
	if err := validateSharedSkillFilesystemKey(key); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	var unlock func()
	if key.Kind == SystemAgentSkillRoot {
		unlock, err = r.lockOwnerKeys(ctx, []string{ownerLockKey(OwnerAgent, key.AgentID)})
		if err != nil {
			return err
		}
		defer unlock()
	}

	// Ensure may materialize storage, so it and the callback both run outside
	// transactions and advisory locks. The agent owner stripe stays held across
	// the callback to keep an Agent delete from purging its shared root.
	record, err := r.Ensure(ctx, key)
	if err != nil {
		return fmt.Errorf("home: ensure shared Skill root: %w", err)
	}
	store, err := r.sharedSkillFilesystemStore(ctx, record)
	if err != nil {
		return err
	}
	filesystem, err := store.openSharedSkillFilesystem(record)
	if err != nil {
		return fmt.Errorf("home: open shared Skill filesystem: %w", err)
	}
	if filesystem == nil {
		return errors.New("home: Store returned a nil shared Skill filesystem")
	}
	defer func() { err = errors.Join(err, filesystem.Close()) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	return use(filesystem)
}

func validateSharedSkillFilesystemKey(key Key) error {
	if err := key.Validate(); err != nil {
		return err
	}
	if key.Kind != SystemSkillRoot && key.Kind != SystemAgentSkillRoot {
		return errors.New("home: shared Skill filesystem requires a SystemSkills or SystemAgentSkills key")
	}
	return nil
}

// sharedSkillFilesystemStore performs the final durable identity check after
// Ensure and before opening bytes. It deliberately returns only a private
// Store facet, never a Record, locator, attachment, or host coordinate.
func (r *Registry) sharedSkillFilesystemStore(ctx context.Context, record Record) (sharedSkillFilesystemStore, error) {
	row, err := r.q.GetStorageHome(ctx, record.ID)
	if err != nil {
		return nil, fmt.Errorf("home: revalidate shared Skill filesystem: %w", err)
	}
	persisted, err := r.decode(row)
	if err != nil {
		return nil, err
	}
	if persisted.Key != record.Key || persisted.StoreID != record.StoreID || persisted.Locator != record.Locator || persisted.State != StateReady {
		return nil, errors.New("home: stale or unavailable shared Skill filesystem")
	}
	store, ok := r.stores[persisted.StoreID].(sharedSkillFilesystemStore)
	if !ok {
		return nil, fmt.Errorf("home: Store %q does not support shared Skill filesystems", persisted.StoreID)
	}
	return store, nil
}
