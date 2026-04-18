package docker

import (
	"fmt"
	"strings"

	"github.com/vaayne/anna/plugins/sandbox/docker/dockerclient"
)

// Config holds docker-backend-specific configuration for a factory.
type Config struct {
	// Image is the container image to use. Defaults to "alpine:3.20" if empty.
	Image string

	// User is the "uid:gid" to run container processes as.
	// When empty, the factory uses the current process uid:gid (Unix) or omits
	// the --user flag (Windows, where Docker Desktop handles UID mapping).
	User string

	// AllowPull controls whether Preflight may pull a missing image.
	// When false and the image is absent locally, Preflight returns an error.
	AllowPull bool

	// ExtraMounts is a list of additional bind-mounts in "host:container[:ro]" syntax.
	ExtraMounts []string

	// WorkspaceMount is the in-container path where WorkspaceRoot is bind-mounted.
	// Defaults to "/workspace" if empty.
	WorkspaceMount string
}

// ImageOrDefault returns the configured image or the default "alpine:3.20".
func (c Config) ImageOrDefault() string {
	if c.Image != "" {
		return c.Image
	}
	return "alpine:3.20"
}

// WorkspaceMountOrDefault returns the configured workspace mount path or "/workspace".
func (c Config) WorkspaceMountOrDefault() string {
	if c.WorkspaceMount != "" {
		return c.WorkspaceMount
	}
	return "/workspace"
}

// parseExtraMounts parses a slice of "host:container[:ro]" strings into Mount values.
// Returns an error on any malformed entry.
func parseExtraMounts(entries []string) ([]dockerclient.Mount, error) {
	mounts := make([]dockerclient.Mount, 0, len(entries))
	for _, entry := range entries {
		parts := strings.Split(entry, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return nil, fmt.Errorf("docker: invalid mount %q: expected host:container[:ro]", entry)
		}
		host := parts[0]
		container := parts[1]
		if host == "" || container == "" {
			return nil, fmt.Errorf("docker: invalid mount %q: host and container paths must be non-empty", entry)
		}
		readOnly := false
		if len(parts) == 3 {
			switch strings.ToLower(parts[2]) {
			case "ro", "readonly":
				readOnly = true
			case "rw", "":
				// read-write, default
			default:
				return nil, fmt.Errorf("docker: invalid mount %q: unsupported option %q (use ro or rw)", entry, parts[2])
			}
		}
		mounts = append(mounts, dockerclient.Mount{
			HostPath:      host,
			ContainerPath: container,
			ReadOnly:      readOnly,
		})
	}
	return mounts, nil
}
