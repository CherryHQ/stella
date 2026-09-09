package docker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

// Docker sessions always own a private temp directory. The parent is private to
// stellad, while the mounted directory uses normal /tmp permissions because the
// sandbox image UID need not match the host stellad UID.
func (f *dockerFactory) prepareSessionTempDir(sessionID string) (string, error) {
	var dir string
	if f.cfg.StellaHome != "" {
		root := filepath.Join(f.cfg.StellaHome, "cache", "sandbox-tmp")
		if err := sandboxpkg.EnsurePrivateDir(root); err != nil {
			return "", fmt.Errorf("docker session: create temp root: %w", err)
		}
		dir = filepath.Join(root, sessionID)
		if err := os.Mkdir(dir, 0o700); err != nil {
			return "", fmt.Errorf("docker session: create temp directory: %w", err)
		}
	} else {
		var err error
		dir, err = os.MkdirTemp("", "stella-sandbox-tmp-"+sessionID+"-")
		if err != nil {
			return "", fmt.Errorf("docker session: create temp directory: %w", err)
		}
	}
	if err := os.Chmod(dir, os.ModeSticky|0o777); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("docker session: set temp permissions: %w", err)
	}
	return dir, nil
}

// cleanupStaleSessionTempDirs deliberately retains unreferenced directories.
// Without a durable per-session creation/close marker, absence from Docker is
// ambiguous: it can mean a crashed session, or a peer that is between creating
// its host directory and creating its container. An age cutoff would turn that
// race into data loss, so startup leaves these paths for an explicit recovery
// pass that has stronger ownership evidence.
func cleanupStaleSessionTempDirs(ctx context.Context, client *dockerclient.Client, scope, stellaHome string) {
	if stellaHome == "" {
		return
	}
	active, err := client.SessionIDsWithContainers(ctx, scope)
	if err != nil {
		slog.Warn("docker session: skip stale temp cleanup", "error", err)
		return
	}
	if len(active) == 0 {
		// Keep the read/list above as an ownership check for callers and to make
		// Docker failures fail closed. There is no safe deletion decision without
		// a durable marker tying a directory to a terminal container lifecycle.
		slog.Debug("docker session: retaining unreferenced temp directories pending recovery evidence")
	}
}

// clearContainerTemp lets the image user open and remove trees it owns. Making
// those trees traversable also lets host cleanup reach nested host-tool-owned
// entries after Stop. Clearing nested sticky bits is essential: otherwise host
// cleanup could traverse a container-owned tree but still be unable to unlink a
// container-owned child. The two ownership passes work without container caps.
func (s *dockerSession) clearContainerTemp() {
	if s.ownedTempDir == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := s.client.Exec(ctx, dockerclient.ExecOptions{
		ContainerID: s.containerID,
		Command: []string{
			"/bin/sh", "-c",
			`chmod -R a+rwx,a-t -- /tmp/* /tmp/.[!.]* /tmp/..?* 2>/dev/null || true; rm -rf -- /tmp/* /tmp/.[!.]* /tmp/..?*`,
		},
		Cwd: "/",
	})
	exitCode := -1
	if result != nil {
		exitCode = result.ExitCode
	}
	if err != nil || exitCode != 0 {
		slog.Warn("docker session: container temp pre-clean failed",
			"session_id", s.id,
			"container_id", s.containerID,
			"exit_code", exitCode,
			"error", err,
		)
	}
}

func (s *dockerSession) cleanupOwnedTempDir() error {
	if s.ownedTempDir == "" {
		return nil
	}
	if err := os.RemoveAll(s.ownedTempDir); err != nil {
		return fmt.Errorf("docker session: remove owned temp directory %q: %w", s.ownedTempDir, err)
	}
	return nil
}
