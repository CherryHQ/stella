// Package hostlayout describes the host filesystem layout granted to a sandbox provider.
package hostlayout

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// Access controls writes through a mount.
type Access uint8

const (
	ReadOnly Access = iota
	ReadWrite
)

// Mount maps a physical host source into a provider-visible target.
type Mount struct {
	Source string
	Target string
	Access Access
}

// Layout is the immutable physical filesystem authority for a provider.
type Layout struct {
	WorkspaceSource  string
	WorkingDirSource string
	Mounts           []Mount
}

// Clone returns a deep copy suitable for retaining in a factory.
func (l Layout) Clone() Layout {
	l.Mounts = append([]Mount(nil), l.Mounts...)
	return l
}

// Validate rejects incomplete, ambiguous, or unmappable layouts.
func (l Layout) Validate() error {
	if !filepath.IsAbs(l.WorkspaceSource) {
		return fmt.Errorf("host layout: workspace source must be absolute")
	}
	if !filepath.IsAbs(l.WorkingDirSource) {
		return fmt.Errorf("host layout: working directory source must be absolute")
	}
	if len(l.Mounts) == 0 {
		return fmt.Errorf("host layout: mounts are required")
	}
	seenSource := make(map[string]struct{}, len(l.Mounts))
	seenTarget := make(map[string]struct{}, len(l.Mounts))
	for _, mount := range l.Mounts {
		if !filepath.IsAbs(mount.Source) || !path.IsAbs(mount.Target) {
			return fmt.Errorf("host layout: mount source and target must be absolute")
		}
		if strings.Contains(mount.Target, "\\") {
			return fmt.Errorf("host layout: mount target %q must use POSIX separators", mount.Target)
		}
		if mount.Access != ReadOnly && mount.Access != ReadWrite {
			return fmt.Errorf("host layout: invalid mount access")
		}
		source, target := filepath.Clean(mount.Source), path.Clean(mount.Target)
		if mount.Target != target {
			return fmt.Errorf("host layout: mount target %q is not canonical", mount.Target)
		}
		if _, ok := seenSource[source]; ok {
			return fmt.Errorf("host layout: duplicate mount source %q", source)
		}
		if _, ok := seenTarget[target]; ok {
			return fmt.Errorf("host layout: duplicate mount target %q", target)
		}
		seenSource[source] = struct{}{}
		seenTarget[target] = struct{}{}
	}
	workspace, _, ok := l.mountForSource(l.WorkspaceSource)
	if !ok || workspace.Access != ReadWrite {
		return fmt.Errorf("host layout: workspace source is not in a writable mount")
	}
	if _, ok := l.SourceToTarget(l.WorkingDirSource); !ok {
		return fmt.Errorf("host layout: working directory is not mappable")
	}
	return nil
}

// SourceToTarget maps a host path through its deepest mount.
func (l Layout) SourceToTarget(source string) (string, bool) {
	mount, relative, ok := l.mountForSource(source)
	if !ok {
		return "", false
	}
	if relative == "." {
		return path.Clean(mount.Target), true
	}
	return path.Join(mount.Target, filepath.ToSlash(relative)), true
}

func (l Layout) mountForSource(source string) (Mount, string, bool) {
	source = filepath.Clean(source)
	var best Mount
	var bestRelative string
	found := false
	for _, mount := range l.Mounts {
		root := filepath.Clean(mount.Source)
		relative, err := filepath.Rel(root, source)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		if !found || len(root) > len(filepath.Clean(best.Source)) {
			best, bestRelative, found = mount, relative, true
		}
	}
	return best, bestRelative, found
}
