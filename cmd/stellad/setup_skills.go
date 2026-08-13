package main

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/home"
	skills "github.com/CherryHQ/stella/internal/skills"
)

func setupSkillStore(db *pgxpool.Pool, roots home.RootOpener) (*skills.POSIXStore, error) {
	return skills.NewPOSIXStore(db, roots)
}
