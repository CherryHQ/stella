package hostlayout

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Resolver translates provider-visible paths to physical sources within a Layout.
// It is provider construction plumbing, not a sandbox runtime API.
type Resolver struct{ layout Layout }

// NewResolver returns a resolver for layout.
func NewResolver(layout Layout) *Resolver { return &Resolver{layout: layout} }

// SourceForRead resolves an agent-visible path to its physical source.
func (r *Resolver) SourceForRead(agentPath string) (string, error) {
	return r.sourceFor(agentPath, false)
}

// SourceForWrite resolves an agent-visible path to a writable physical source.
func (r *Resolver) SourceForWrite(agentPath string) (string, error) {
	return r.sourceFor(agentPath, true)
}

// SourceToTarget maps a physical source through the deepest layout mount.
func (r *Resolver) SourceToTarget(source string) (string, bool) {
	return r.layout.SourceToTarget(source)
}

// TargetToSource maps a canonical POSIX target through the deepest layout mount.
func (r *Resolver) TargetToSource(target string) (string, bool) {
	mount, rel, ok := r.mountForTarget(path.Clean(target))
	if !ok {
		return "", false
	}
	if rel == "." || rel == "" {
		return mount.Source, true
	}
	return filepath.Join(mount.Source, filepath.FromSlash(rel)), true
}

func (r *Resolver) sourceFor(agentPath string, write bool) (string, error) {
	if len(r.layout.Mounts) == 0 {
		return "", fmt.Errorf("host layout: path %q is outside mount set: no mounts configured", agentPath)
	}
	target := agentPath
	if !path.IsAbs(target) && !filepath.IsAbs(target) {
		wd, ok := r.layout.SourceToTarget(r.layout.WorkingDirSource)
		if !ok {
			return "", fmt.Errorf("host layout: working directory is not mappable")
		}
		target = path.Join(wd, target)
	}
	// Accept physical absolute paths only when they are contained in a mount.
	candidate, ok := r.TargetToSource(target)
	if !ok {
		candidate = filepath.Clean(agentPath)
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("host layout: resolve path %q: %w", agentPath, err)
		}
		resolved = candidate
	}
	resolved = filepath.Clean(resolved)
	mount, _, ok := r.layout.mountForSource(resolved)
	if !ok {
		mount, _, ok = r.layout.mountForSource(candidate)
	}
	if !ok {
		return "", fmt.Errorf("host layout: path %q resolves outside mount set", agentPath)
	}
	if write && mount.Access == ReadOnly {
		return "", fmt.Errorf("host layout: path %q is in a read-only mount", agentPath)
	}
	if err := rejectSymlinkTraversal(mount.Source, candidate); err != nil {
		return "", err
	}
	return candidate, nil
}

func (r *Resolver) mountForTarget(target string) (Mount, string, bool) {
	var best Mount
	bestRel := ""
	found := false
	for _, mount := range r.layout.Mounts {
		root := path.Clean(mount.Target)
		var rel string
		switch {
		case target == root:
			rel = "."
		case strings.HasPrefix(target, root+"/"):
			rel = strings.TrimPrefix(target, root+"/")
		default:
			continue
		}
		if !found || len(mount.Target) > len(best.Target) {
			best, bestRel, found = mount, rel, true
		}
	}
	return best, bestRel, found
}

func rejectSymlinkTraversal(root, candidate string) error {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("host layout: path %q is outside mount root %q", candidate, root)
	}
	current := filepath.Clean(root)
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("host layout: lstat %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("host layout: path %q traverses symlink at %q", candidate, current)
		}
	}
	return nil
}
