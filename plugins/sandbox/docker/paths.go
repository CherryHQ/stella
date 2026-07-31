package docker

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

// toContainerPath maps a host absolute path to its equivalent in-container path
// by finding the deepest mount in the mount table that covers it.
// Returns an error if no mount covers the path (fail closed).
func toContainerPath(mounts []dockerclient.Mount, hostPath string) (string, error) {
	bestRel := ""
	bestMount := dockerclient.Mount{}
	found := false

	for _, m := range mounts {
		rel, err := filepath.Rel(m.HostPath, hostPath)
		if err != nil {
			continue
		}
		// Must not be a parent traversal.
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		// Pick the deepest (longest host path) match.
		if !found || len(m.HostPath) > len(bestMount.HostPath) {
			bestRel = rel
			bestMount = m
			found = true
		}
	}

	if !found {
		return "", fmt.Errorf("docker: host path %q is not covered by any container mount", hostPath)
	}

	containerRoot := cleanContainerPath(bestMount.ContainerPath)
	// Exact match on the mount root — return the container path directly.
	if bestRel == "." {
		return containerRoot, nil
	}

	// The host relative path may use backslashes; containers are always Linux.
	return path.Join(containerRoot, strings.ReplaceAll(bestRel, "\\", "/")), nil
}

// ─────────────────────────── dockerHost ──────────────────────────────

// dockerHost implements the process/path surface of Host.
// File I/O is done directly via os.* on resolved host paths
// (bind-mount makes host paths the source of truth).
// Exec and StartProcess translate host cwd → container cwd via toContainerPath.
type dockerHost struct {
	session *dockerSession
}

func (h *dockerHost) WorkingDir() string {
	return h.session.policy.Filesystem.WorkingDir
}

// ResolvePath turns a relative or absolute path into an absolute host path
// covered by the session's mount set. Paths outside every mount are rejected
// so absolute-path inputs cannot bypass the workspace / read-only policy
// boundary on filesystem operations. Symlinks anywhere in the path are
// rejected — nothing in the codebase creates symlinks in a session
// workspace, so any are agent-planted and following them would let an
// agent escape the mount via a file that passes the string-based check.
func (h *dockerHost) ResolvePath(path string) (string, error) {
	resolved, err := h.pathResolver().ResolvePath(path)
	if err != nil {
		return "", fmt.Errorf("docker host: %w", err)
	}
	return resolved.HostPath, nil
}

// toHostPath maps a container absolute path to its equivalent host path when
// the path is covered by a mount's container path.
func toHostPath(mounts []dockerclient.Mount, containerPath string) (string, bool) {
	containerPath = cleanContainerPath(containerPath)
	bestRel := ""
	bestMount := dockerclient.Mount{}
	found := false
	for _, m := range mounts {
		rel, ok := sandboxpkg.POSIXPathRelative(m.ContainerPath, containerPath)
		if !ok {
			continue
		}
		if !found || len(m.ContainerPath) > len(bestMount.ContainerPath) {
			bestRel = rel
			bestMount = m
			found = true
		}
	}
	if !found {
		return "", false
	}
	if bestRel == "." {
		return bestMount.HostPath, true
	}
	return filepath.Join(bestMount.HostPath, bestRel), true
}

// ResolveWritePath is like ResolvePath but additionally rejects paths that
// fall within a read-only mount.
func (h *dockerHost) ResolveWritePath(path string) (string, error) {
	resolved, err := h.pathResolver().ResolveWritePath(path)
	if err != nil {
		return "", fmt.Errorf("docker host: %w", err)
	}
	return resolved.HostPath, nil
}

func (h *dockerHost) pathResolver() *sandboxpkg.PathResolver {
	mounts := make([]sandboxpkg.Mount, 0, len(h.session.mountTable))
	for _, m := range h.session.mountTable {
		access := sandboxpkg.MountReadWrite
		if m.ReadOnly {
			access = sandboxpkg.MountReadOnly
		}
		mounts = append(mounts, sandboxpkg.Mount{HostPath: m.HostPath, SandboxPath: m.ContainerPath, Access: access})
	}
	return sandboxpkg.NewPathResolver(sandboxpkg.PathResolverConfig{
		WorkspaceRoot: h.session.policy.WorkspaceRootOrDefault(),
		WorkingDir:    h.session.policy.Filesystem.WorkingDir,
		Mounts:        mounts,
	})
}
