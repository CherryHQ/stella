package docker

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
	"github.com/CherryHQ/stella/plugins/sandbox/hostlayout"
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

// agentWorkingDir maps the configured host working directory into the
// container view before a session starts. A session has no safe default when
// this fails: exposing a different directory splits process and filesystem
// coordinates.
func agentWorkingDir(mounts []dockerclient.Mount, hostWorkingDir string) (string, error) {
	if hostWorkingDir == "" {
		return "", fmt.Errorf("docker: working directory is required")
	}
	absWorkingDir, err := filepath.Abs(hostWorkingDir)
	if err != nil {
		return "", fmt.Errorf("docker: abs working directory: %w", err)
	}
	workingDir, err := toContainerPath(mounts, absWorkingDir)
	if err != nil {
		return "", fmt.Errorf("docker: map working directory: %w", err)
	}
	return workingDir, nil
}

// ─────────────────────────── dockerHost ──────────────────────────────

// dockerHost maps the provider's container paths to private host coordinates.
// Exec and StartProcess translate host cwd → container cwd via toContainerPath.
type dockerHost struct {
	session *dockerSession
}

// resolvePath turns a relative or absolute path into an absolute host path
// covered by the session's mount set. Paths outside every mount are rejected
// so absolute-path inputs cannot bypass the workspace / read-only policy
// boundary on filesystem operations. Symlinks anywhere in the path are
// rejected — nothing in the codebase creates symlinks in a session
// workspace, so any are agent-planted and following them would let an
// agent escape the mount via a file that passes the string-based check.
func (h *dockerHost) resolvePath(path string) (string, error) {
	resolved, err := h.pathResolver().SourceForRead(path)
	if err != nil {
		return "", fmt.Errorf("docker host: %w", err)
	}
	return resolved, nil
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

// resolveWritePath is like resolvePath but additionally rejects paths that
// fall within a read-only mount.
func (h *dockerHost) resolveWritePath(path string) (string, error) {
	resolved, err := h.pathResolver().SourceForWrite(path)
	if err != nil {
		return "", fmt.Errorf("docker host: %w", err)
	}
	return resolved, nil
}

func (h *dockerHost) pathResolver() *hostlayout.Resolver {
	mounts := make([]hostlayout.Mount, 0, len(h.session.mountTable))
	for _, m := range h.session.mountTable {
		access := hostlayout.ReadWrite
		if m.ReadOnly {
			access = hostlayout.ReadOnly
		}
		mounts = append(mounts, hostlayout.Mount{Source: m.HostPath, Target: m.ContainerPath, Access: access})
	}
	return hostlayout.NewResolver(hostlayout.Layout{WorkspaceSource: h.workspaceRoot(), WorkingDirSource: h.workingDirSource(), Mounts: mounts})
}

func (h *dockerHost) workspaceRoot() string {
	root, _ := toHostPath(h.session.mountTable, workspaceMount)
	return root
}

func (h *dockerHost) workingDirSource() string {
	workingDir, _ := toHostPath(h.session.mountTable, h.session.workingDir)
	return workingDir
}
