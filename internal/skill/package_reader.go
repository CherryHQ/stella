package skill

import (
	"context"
	"errors"
	"io/fs"
	"strings"
)

// PackageSkillLoader is the narrow immutable-store seam. The store verifies
// and copies one digest/name tree while holding its own publication/cleanup
// lock; this package only turns the copied bytes into the runtime revision.
type PackageSkillLoader func(context.Context, string, string) (map[string][]byte, map[string]fs.FileMode, error)

// StorePackageSkillReader adapts the plugin content store to the runtime Skill
// reader without exposing its host root or a general filesystem capability.
type StorePackageSkillReader struct{ load PackageSkillLoader }

func NewStorePackageSkillReader(load PackageSkillLoader) (*StorePackageSkillReader, error) {
	if load == nil {
		return nil, errors.New("skills: package store loader is required")
	}
	return &StorePackageSkillReader{load: load}, nil
}

func (r *StorePackageSkillReader) LoadPackageSkill(ctx context.Context, ref PackageSkillRef) (PackageSkillRevision, error) {
	if r == nil || r.load == nil || !validInventoryComponent(ref.PackageID) || !validPackageDigest(ref.PackageDigest) || !validInventoryComponent(ref.Name) {
		return PackageSkillRevision{}, ErrInvalidSkillRevision
	}
	if ref.Path != "" && !validPackageSkillPath(ref.Path, ref.Name) {
		return PackageSkillRevision{}, ErrInvalidSkillRevision
	}
	digest := strings.TrimPrefix(ref.PackageDigest, "sha256:")
	files, modes, err := r.load(ctx, digest, ref.Name)
	if err != nil {
		return PackageSkillRevision{}, err
	}
	if len(files) == 0 {
		return PackageSkillRevision{}, ErrInvalidSkillRevision
	}
	return PackageSkillRevision{Ref: ref, Files: files, Modes: modes}, nil
}
