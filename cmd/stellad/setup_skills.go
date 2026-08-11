package main

import (
	"github.com/jackc/pgx/v5/pgxpool"

	skills "github.com/CherryHQ/stella/internal/skills"
)

func setupSkillStore(db *pgxpool.Pool) *skills.PGStore {
	return skills.New(db)
}
