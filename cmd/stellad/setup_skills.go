package main

import (
	"database/sql"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/config"
	skills "github.com/CherryHQ/stella/internal/skills"
)

const cliSkillsUserID = "1"

func cliUserSkillsDir(snap *config.Snapshot) (string, error) {
	userDir, err := agent.SetupUserWorkspace(config.StellaHome(), cliSkillsUserID, snap.AgentID)
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
			return agent.UserSkillsDir(agent.AgentWorkspaceDir(base, agentID))
		case "user":
			// User skills are shared across all of a user's agents (#442), so the
			// path no longer depends on agentID.
			if userID == "" {
				return ""
			}
			return agent.UserSkillsDir(agent.UserHomeDir(base, userID))
		case "user_agent":
			if agentID == "" || userID == "" {
				return ""
			}
			return agent.UserSkillsDir(agent.UserAgentDir(base, userID, agentID))
		default:
			// system (global) skills are not tied to a workspace, so they are not
			// mirrored to disk here; their SKILL.md resolves from the DB.
			return ""
		}
	})

	return skillStores{raw: raw, diskSync: diskSync}
}
