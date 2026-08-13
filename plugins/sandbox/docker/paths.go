package docker

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

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

// dockerHost implements the process surface of Host. Filesystem access is
// mediated separately by the Session's provider-private rooted capability.
type dockerHost struct {
	session *dockerSession
}

func (h *dockerHost) WorkingDir() string {
	return h.session.policy.Filesystem.WorkingDir
}
