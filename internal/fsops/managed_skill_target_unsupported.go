//go:build !darwin && !linux

package fsops

import "errors"

// Deliberate ceiling: add a handle-relative atomic replacement implementation
// before managed Skill publication is enabled on another platform.
func managedSkillPlatformSupported() error {
	return errors.New("fsops: managed skill target replacement is unsupported on this platform")
}
