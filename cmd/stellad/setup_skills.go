package main

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/platform/home"
	"github.com/CherryHQ/stella/internal/skill"
)

func setupSkillStore(db *pgxpool.Pool, roots home.SkillRootOpener) (*skill.POSIXStore, error) {
	return skill.NewPOSIXStore(db, roots)
}
