package main

import (
	"database/sql"
	"path/filepath"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/config"
	skills "github.com/CherryHQ/stella/internal/skills"
)

const cliSkillsUserID = "1"

func cliUserSkillsDir(snap *config.Snapshot) (string, error) {
	userDir, err := agent.SetupUserWorkspace(snap.AgentID, config.StellaHome(), cliSkillsUserID)
	if err != nil {
		return "", err
	}
	return agent.UserSkillsDir(userDir), nil
}

type skillStores struct {
	raw      *skills.SQLiteStore
	diskSync *skills.DiskSyncStore
}

func setupSkillStores(db *sql.DB) skillStores {
	raw := skills.New(db)
	diskSync := skills.NewDiskSyncStore(raw, func(scope, agentID string, userID string) string {
		base := config.StellaHome()
		switch scope {
		case "system_agent":
			if agentID == "" {
				return ""
			}
			return filepath.Join(base, "workspaces", agentID, ".agents", "skills")
		case "user_agent":
			if agentID == "" || userID == "" {
				return ""
			}
			return filepath.Join(base, "workspaces", agentID, "users", userID, ".agents", "skills")
		default:
			// user (global) and system (global) skills are not tied to a single
			// agent workspace, so they are not mirrored to disk here; their
			// SKILL.md resolves from the DB.
			return ""
		}
	})

	return skillStores{raw: raw, diskSync: diskSync}
}
