package main

import (
	"context"
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

const (
	// Retained revisions have no Phase-2 GC. Eight largest legal trees consume
	// at most 4,112 scanned entries; three consume 96 MiB. These warning-only
	// limits leave bounded headroom (4,608 entries / 128 MiB) for a complete
	// threshold observation while Phase 3 remains the owner of reclamation.
	productionRevisionWarningCount = 8
	productionRevisionWarningBytes = 96 << 20
	productionRevisionScanEntries  = 4_608
	productionRevisionScanBytes    = 128 << 20
)

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

func setupHomeSkillAuthority(ctx context.Context, db *pgxpool.Pool, homes *home.Registry) (homeSkillAuthority, error) {
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
	telemetry, err := skills.NewRevisionTelemetry(skills.RevisionTelemetryConfig{
		Limits:     skills.RevisionScanLimits{MaxEntries: productionRevisionScanEntries, MaxBytes: productionRevisionScanBytes},
		Thresholds: skills.RevisionThresholds{Count: productionRevisionWarningCount, Bytes: productionRevisionWarningBytes},
	})
	if err != nil {
		return homeSkillAuthority{}, err
	}
	publisher, err := skills.NewHomeSkillPublisherWithRevisionTelemetry(homes, telemetry, func(ctx context.Context, root *home.SkillRoot) (skills.FilesystemCatalogRoot, error) {
		return skills.HomeSkillObservationCatalogRoot(ctx, homes, root)
	})
	if err != nil {
		return homeSkillAuthority{}, err
	}
	// Authority was verified by the caller before this composition root. This is
	// a one-time best-effort snapshot, not a retry loop or a second authority.
	catalog.ObserveRetainedRevisions(ctx, telemetry)
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
