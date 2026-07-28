package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PathResolver is the shared host-side filesystem boundary for in-process tools.
// It translates agent-visible paths to host paths only when the result stays
// inside a declared mount, and it rejects symlink traversal below mount roots.
type PathResolver struct {
	workingDir string
	mounts     []Mount
}

// PathResolverConfig describes the mount view used by PathResolver.
type PathResolverConfig struct {
	WorkspaceRoot string
	WorkingDir    string
	Mounts        []Mount
	TempDirHost   string
}

// ResolvedPath is a path resolved through a mount.
type ResolvedPath struct {
	HostPath    string
	SandboxPath string
	Mount       Mount
}

// NewPathResolver returns a resolver for the supplied filesystem view. The
// workspace mount is added as a compatibility default when Mounts omits it.
func NewPathResolver(cfg PathResolverConfig) *PathResolver {
	mounts := normalizeMounts(cfg.Mounts)
	workspace := cfg.WorkspaceRoot
	if workspace == "" {
		workspace = cfg.WorkingDir
	}
	if workspace != "" && !hasSandboxMount(mounts, MountWorkspace) && !hasHostMount(mounts, workspace) {
		mounts = append(mounts, Mount{HostPath: workspace, SandboxPath: workspace, Access: MountReadWrite})
	}
	if cfg.TempDirHost != "" && !hasSandboxMount(mounts, "/tmp") {
		mounts = append(mounts, Mount{HostPath: cfg.TempDirHost, SandboxPath: "/tmp", Access: MountReadWrite})
	}
	return &PathResolver{workingDir: cfg.WorkingDir, mounts: mounts}
}

// Mounts returns a copy of the normalized mount table.
func (r *PathResolver) Mounts() []Mount {
	out := make([]Mount, len(r.mounts))
	copy(out, r.mounts)
	return out
}

// ResolvePath resolves an agent path for reading.
func (r *PathResolver) ResolvePath(agentPath string) (ResolvedPath, error) {
	return r.resolve(agentPath, false)
}

// ResolveWritePath resolves an agent path for writing and rejects read-only mounts.
func (r *PathResolver) ResolveWritePath(agentPath string) (ResolvedPath, error) {
	return r.resolve(agentPath, true)
}

// ToHostPath maps a sandbox-space path to its host path using the deepest mount.
func (r *PathResolver) ToHostPath(sandboxPath string) (string, bool) {
	m, rel, ok := deepestSandboxMount(r.mounts, filepath.Clean(sandboxPath))
	if !ok {
		return "", false
	}
	return joinMountRel(m.HostPath, rel), true
}

// ToSandboxPath maps a host path to its sandbox-space path using the deepest mount.
func (r *PathResolver) ToSandboxPath(hostPath string) (string, bool) {
	m, rel, ok := deepestHostMount(r.mounts, filepath.Clean(hostPath))
	if !ok {
		return "", false
	}
	return joinMountRel(m.SandboxPath, rel), true
}

func (r *PathResolver) resolve(agentPath string, write bool) (ResolvedPath, error) {
	if len(r.mounts) == 0 {
		return ResolvedPath{}, fmt.Errorf("sandbox: path %q is outside workspace root: no mounts configured", agentPath)
	}

	candidateSandbox := agentPath
	if !filepath.IsAbs(candidateSandbox) {
		candidateSandbox = filepath.Join(r.sandboxWorkingDir(), agentPath)
	}
	candidateSandbox = filepath.Clean(candidateSandbox)

	// An absolute path already inside a host mount is unambiguously host-space
	// input, even when its prefix also names another sandbox mount. This matters
	// for test/development workspaces below host /tmp: interpreting
	// /tmp/workspace/file as sandbox /tmp would redirect it into the session's
	// private temporary directory.
	candidateHost := ""
	if filepath.IsAbs(agentPath) {
		if _, _, isHostPath := deepestHostMount(r.mounts, candidateSandbox); isHostPath {
			candidateHost = candidateSandbox
		}
	}
	if candidateHost == "" {
		mapped, ok := r.ToHostPath(candidateSandbox)
		if ok {
			candidateHost = mapped
		} else {
			// Backwards compatibility for an absolute path that no sandbox mount
			// recognizes; the host-boundary check below will still reject it
			// unless it belongs to a declared host mount.
			candidateHost = candidateSandbox
		}
	}

	resolved, err := filepath.EvalSymlinks(candidateHost)
	if err != nil {
		if !os.IsNotExist(err) {
			return ResolvedPath{}, fmt.Errorf("sandbox: resolve path %q: %w", agentPath, err)
		}
		resolved = candidateHost
	}
	resolved = filepath.Clean(resolved)

	mount, _, ok := deepestHostMount(r.mounts, resolved)
	if !ok {
		mount, _, ok = deepestHostMount(r.mounts, candidateHost)
	}
	if !ok {
		return ResolvedPath{}, fmt.Errorf("sandbox: path %q resolves to %q which is outside workspace root/mount set", agentPath, resolved)
	}
	if write && mount.Access == MountReadOnly {
		return ResolvedPath{}, fmt.Errorf("sandbox: path %q is in a read-only mount", agentPath)
	}
	if err := rejectMountSymlinkTraversal(mount.HostPath, candidateHost); err != nil {
		return ResolvedPath{}, fmt.Errorf("sandbox: %w", err)
	}
	sandboxPath, _ := r.ToSandboxPath(candidateHost)
	return ResolvedPath{HostPath: candidateHost, SandboxPath: sandboxPath, Mount: mount}, nil
}

func (r *PathResolver) sandboxWorkingDir() string {
	wd := r.workingDir
	if wd == "" {
		if len(r.mounts) == 0 {
			return ""
		}
		return r.mounts[0].SandboxPath
	}
	if sandboxPath, ok := r.ToSandboxPath(wd); ok {
		return sandboxPath
	}
	return wd
}

func normalizeMounts(mounts []Mount) []Mount {
	out := make([]Mount, 0, len(mounts))
	for _, m := range mounts {
		if m.HostPath == "" || m.SandboxPath == "" {
			continue
		}
		m.HostPath = filepath.Clean(m.HostPath)
		m.SandboxPath = filepath.Clean(m.SandboxPath)
		out = append(out, m)
	}
	return out
}

func hasSandboxMount(mounts []Mount, sandboxPath string) bool {
	clean := filepath.Clean(sandboxPath)
	for _, m := range mounts {
		if m.SandboxPath == clean {
			return true
		}
	}
	return false
}

func hasHostMount(mounts []Mount, hostPath string) bool {
	clean := filepath.Clean(hostPath)
	for _, m := range mounts {
		if m.HostPath == clean {
			return true
		}
	}
	return false
}

func deepestSandboxMount(mounts []Mount, path string) (Mount, string, bool) {
	return deepestMountBy(path, mounts, func(m Mount) string { return m.SandboxPath })
}

func deepestHostMount(mounts []Mount, path string) (Mount, string, bool) {
	return deepestMountBy(path, mounts, func(m Mount) string { return m.HostPath })
}

func deepestMountBy(path string, mounts []Mount, rootOf func(Mount) string) (Mount, string, bool) {
	var best Mount
	bestRel := ""
	found := false
	for _, m := range mounts {
		root := rootOf(m)
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if !found || len(root) > len(rootOf(best)) {
			best = m
			bestRel = rel
			found = true
		}
	}
	return best, bestRel, found
}

func joinMountRel(root, rel string) string {
	if rel == "." || rel == "" {
		return root
	}
	return filepath.Join(root, rel)
}

func rejectMountSymlinkTraversal(root, path string) error {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q is outside mount root %q", path, root)
	}
	if rel == "." {
		return nil
	}
	current := root
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
			return fmt.Errorf("lstat %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q traverses symlink at %q", path, current)
		}
	}
	return nil
}
