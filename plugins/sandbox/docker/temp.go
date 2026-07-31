package docker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

const staleSessionTempMinimumAge = time.Hour

// cleanupStaleSessionTempDirs removes only old owned fallback directories that
// are not referenced by any scoped Docker container. The age gate closes the
// race with a concurrently starting peer that has made its directory but has
// not yet created its container.
func cleanupStaleSessionTempDirs(ctx context.Context, client *dockerclient.Client, scope, stellaHome string) {
	if stellaHome == "" {
		return
	}
	active, err := client.SessionIDsWithContainers(ctx, scope)
	if err != nil {
		slog.Warn("docker session: skip stale temp cleanup", "error", err)
		return
	}
	root := filepath.Join(stellaHome, "cache", "sandbox-tmp")
	entries, err := os.ReadDir(root)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("docker session: read stale temp directory", "path", root, "error", err)
		}
		return
	}
	cutoff := time.Now().Add(-staleSessionTempMinimumAge)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "sandbox-") {
			continue
		}
		if _, live := active[entry.Name()]; live {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		stalePath := filepath.Join(root, entry.Name())
		if err := os.RemoveAll(stalePath); err != nil {
			slog.Warn("docker session: remove stale temp directory", "path", stalePath, "error", err)
		}
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
