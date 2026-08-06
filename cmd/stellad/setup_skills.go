package main

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/home"
	skills "github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const cliSkillsUserID = "1"

func cliUserSkillsDir(snap *config.Snapshot) (string, error) {
	userDir, err := agent.SetupUserWorkspace(config.StellaHome(), cliSkillsUserID, snap.AgentID)
	if err != nil {
		return "", err
	}
	return agent.UserSkillsDir(agent.UserDataDir(userDir)), nil
}

// homeSkillAuthority is the complete production Skill authority. PostgreSQL is
// retained only for Home identity inventory and logical Reflect usage facts;
// Home owns every current-state Skill byte and mutation.
type homeSkillAuthority struct {
	store *skills.HomeAuthorityStore
	usage *skills.HomeSkillUsageStore
}

func setupHomeSkillAuthority(db *pgxpool.Pool, homes *home.Registry) (homeSkillAuthority, error) {
	if db == nil || homes == nil {
		return homeSkillAuthority{}, errors.New("home skill authority requires database and registry")
	}
	queries := sqlc.New(db)
	inventory, err := skills.NewStorageHomeCatalogInventory(queries)
	if err != nil {
		return homeSkillAuthority{}, err
	}
	catalog, err := skills.NewHomeCatalog(homes, inventory)
	if err != nil {
		return homeSkillAuthority{}, err
	}
	publisher, err := skills.NewHomeSkillPublisher(homes)
	if err != nil {
		return homeSkillAuthority{}, err
	}
	manager, err := skills.NewHomeSkillManager(catalog, publisher, func() time.Time { return time.Now().UTC() })
	if err != nil {
		return homeSkillAuthority{}, err
	}
	homeStore, err := skills.NewHomeStore(catalog, manager)
	if err != nil {
		return homeSkillAuthority{}, err
	}
	usage, err := skills.NewHomeSkillUsageStore(db)
	if err != nil {
		return homeSkillAuthority{}, err
	}
	reflectStore, err := skills.NewHomeReflectStore(homeStore, usage)
	if err != nil {
		return homeSkillAuthority{}, err
	}
	store, err := skills.NewHomeAuthorityStore(homeStore, reflectStore)
	if err != nil {
		return homeSkillAuthority{}, err
	}
	return homeSkillAuthority{store: store, usage: usage}, nil
}
