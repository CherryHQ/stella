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

func setupSkillStore(db *pgxpool.Pool) *skills.PGStore {
	return skills.New(db)
}
