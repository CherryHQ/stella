package reflect

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/vaayne/anna/pkg/skills"
)

// expireDrafts scans user and agent skill directories, deprecating draft skills
// older than maxAge. It silently skips directories that don't exist.
func expireDrafts(workspace string, maxAge time.Duration, log *slog.Logger) {
	if workspace == "" {
		return
	}

	cutoff := time.Now().Add(-maxAge)

	// Agent-level drafts: {workspace}/skills/
	expireDraftsInDir(filepath.Join(workspace, "skills"), cutoff, log)

	// Per-user drafts: {workspace}/users/*/.agents/skills/
	usersDir := filepath.Join(workspace, "users")
	entries, err := os.ReadDir(usersDir)
	if err != nil {
		return // No users dir — nothing to expire.
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		userSkillsDir := filepath.Join(usersDir, entry.Name(), ".agents", "skills")
		expireDraftsInDir(userSkillsDir, cutoff, log)
	}
}

func expireDraftsInDir(dir string, cutoff time.Time, log *slog.Logger) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return
	}

	loaded := skills.LoadSkills("", "", "", dir)
	for _, s := range loaded {
		if s.Status != skills.SkillStatusDraft {
			continue
		}
		if s.CreatedAt == "" {
			continue
		}

		created, err := time.Parse(time.RFC3339, s.CreatedAt)
		if err != nil {
			log.Warn("reflect: bad created-at in draft skill", "skill", s.Name, "error", err)
			continue
		}

		if created.Before(cutoff) {
			if err := skills.Deprecate(s.Name, dir); err != nil {
				log.Error("reflect: expire draft", "skill", s.Name, "error", err)
				continue
			}
			log.Info("reflect: expired draft skill", "skill", s.Name, "created_at", s.CreatedAt)
		}
	}
}
