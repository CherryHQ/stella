package home

import (
	"context"
	"errors"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

// UseSharedSkillFilesystem preserves the narrow shared-publisher contract.
// Other mutable Skill scopes must use a typed SkillRoot through
// UseSkillFilesystem; ordinary shared attachments remain read-only.
func (r *Registry) UseSharedSkillFilesystem(ctx context.Context, key Key, use func(sandbox.Filesystem) error) error {
	if err := validateSharedSkillFilesystemKey(key); err != nil {
		return err
	}
	root, err := newSkillRoot(key)
	if err != nil {
		return err
	}
	return r.UseSkillFilesystem(ctx, root, use)
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

// sharedSkillFilesystemStore remains the private shared-root revalidation
// helper for the legacy wrapper's focused tests. General callers use the
// typed SkillRoot boundary above.
func (r *Registry) sharedSkillFilesystemStore(ctx context.Context, record Record) (skillFilesystemStore, error) {
	root, err := newSkillRoot(record.Key)
	if err != nil {
		return nil, err
	}
	if record.Key.Kind != SystemSkillRoot && record.Key.Kind != SystemAgentSkillRoot {
		return nil, errors.New("home: shared Skill filesystem requires a shared Skill root")
	}
	return r.skillFilesystemStore(ctx, record, root)
}
