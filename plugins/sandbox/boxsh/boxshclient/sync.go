package boxshclient

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SyncSessionToSrc copies all files from the session overlay upperdir (dst)
// back to the source workspace (src) using cp -a. Only files written during
// the session exist in dst. Deletions are not propagated.
//
// TODO: propagate deletions. Options: (a) pre-populate DST with a full copy of
// SRC at session start, then use `rsync -a --delete DST/ SRC/` on close;
// (b) track deletions in boxshHost.Remove and replay them on SRC at close.
func SyncSessionToSrc(dst, src string) error {
	entries, err := os.ReadDir(dst)
	if err != nil {
		return fmt.Errorf("read session dir: %w", err)
	}
	if len(entries) == 0 {
		return nil
	}
	// dst/. copies contents (including hidden files) without the dir itself.
	out, err := exec.Command("cp", "-a", dst+"/.", src).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cp -a: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CleanupOrphanedSessions syncs and removes any leftover session directories
// from crashed processes. Sessions with readable metadata are synced back to
// their source workspace; sessions without metadata are removed silently.
func CleanupOrphanedSessions(annaHome string) {
	if annaHome == "" {
		annaHome = DefaultAnnaHome()
	}
	cleanupSessionsInDir(filepath.Join(annaHome, "cache", "sandbox", "sessions"), true)
}

// CleanupLegacySessionDirs removes orphaned session dirs from the old incorrect
// location caused by the filepath.Dir(UserRoot) bug. Legacy dirs have no metadata
// so they are removed without syncing.
func CleanupLegacySessionDirs(dir string) {
	if dir == "" {
		return
	}
	cleanupSessionsInDir(dir, false)
}

func cleanupSessionsInDir(dir string, syncBack bool) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		slog.Warn("sandbox cleanup: read sessions dir", "dir", dir, "error", err)
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, ".anna-boxsh-session-") && !strings.HasPrefix(name, ".boxsh-ovl-work-") {
			continue
		}
		sessionDir := filepath.Join(dir, name)

		if syncBack && strings.HasPrefix(name, ".anna-boxsh-session-") {
			meta, metaErr := ReadSessionMeta(sessionDir)
			if metaErr == nil {
				if err := SyncSessionToSrc(sessionDir, meta.Src); err != nil {
					slog.Warn("sandbox cleanup: sync failed, removing anyway",
						"session_dir", sessionDir, "src", meta.Src, "error", err)
				} else {
					slog.Info("sandbox cleanup: orphaned session synced",
						"session_dir", sessionDir, "src", meta.Src)
				}
			}
		}

		if err := CleanupSessionDir(sessionDir); err != nil {
			slog.Warn("sandbox cleanup: remove session dir", "session_dir", sessionDir, "error", err)
		}
	}
}
