//go:build !darwin && !linux

package fsops

import "errors"

// NewSkillFilesystem fails closed when the platform cannot provide the
// handle-relative no-follow walk required for trusted typed Skill roots.
func NewSkillFilesystem(base, relative string) (*Filesystem, error) {
	return nil, errors.New("fsops: trusted Skill filesystem roots are unsupported on this platform")
}

func OpenExistingSkillFilesystem(base, relative string) (*Filesystem, error) {
	return NewSkillFilesystem(base, relative)
}

func newSkillFilesystem(base, relative string, syncParent func(int) error) (*Filesystem, error) {
	return NewSkillFilesystem(base, relative)
}

func (r *Root) openPinnedManagedSkillCatalog(relative string) (*Root, error) {
	return nil, errors.New("fsops: pinned managed Skill catalogs are unsupported on this platform")
}
