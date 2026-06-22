package main

import (
	"github.com/jackc/pgx/v5/pgxpool"

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
	return agent.UserSkillsDir(agent.UserDataDir(userDir)), nil
}

type skillStores struct {
	raw      *skills.PGStore
	diskSync *skills.DiskSyncStore
}

func setupSkillStores(db *pgxpool.Pool) skillStores {
	raw := skills.New(db)
	// The per-scope on-disk layout is owned by agent.WriterSkillDiskLayout (the
	// single authority the skills tool reads back through); this resolver just
	// selects the base dir for the skill's scope.
	diskSync := skills.NewDiskSyncStore(raw, func(scope, agentID string, userID string) string {
		return agent.WriterSkillDiskLayout(config.StellaHome(), userID, agentID).BaseDir(scope)
	})

	return skillStores{raw: raw, diskSync: diskSync}
}
