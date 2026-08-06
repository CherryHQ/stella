package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

// EnsureSkillHomeAuthority is the startup gate for the eventual Home-backed
// authority. A fresh empty deployment gets its completed marker from a short
// database-only transaction; it never enters the publishing migration path.
func EnsureSkillHomeAuthority(ctx context.Context, db *pgxpool.Pool, homes *home.Registry) error {
	return ensureSkillHomeAuthority(ctx, db, homes, authorityEnsureHooks{})
}

type authorityEnsureHooks struct {
	// beforeFreshLock is test-only synchronization after observing a missing
	// marker and before the transaction locks legacy Skill writers.
	beforeFreshLock func() error
}

func ensureSkillHomeAuthority(ctx context.Context, db *pgxpool.Pool, homes *home.Registry, hooks authorityEnsureHooks) error {
	migration, err := NewSkillHomeMigrationService(db, homes)
	if err != nil {
		return err
	}
	if err := migration.validateMutableAssetGate(ctx); err != nil {
		return fmt.Errorf("skills: Home authority is unavailable: %w", err)
	}
	q := sqlc.New(db)
	marker, err := q.GetStorageMigration(ctx, SkillHomeAuthorityMigration)
	if errors.Is(err, pgx.ErrNoRows) {
		marker, err = initializeEmptySkillHomeAuthority(ctx, db, q, hooks)
		if err != nil {
			return err
		}
		// A CAS loser reloads the winner below through this same strict path;
		// no recursive startup invocation is needed.
	} else if err != nil {
		return fmt.Errorf("skills: load Home authority marker: %w", err)
	}
	switch marker.State {
	case "pending":
		if err := validateSkillMigrationMarker(marker, []byte(`{}`)); err != nil {
			return fmt.Errorf("skills: malformed pending Skill migration marker: %w", err)
		}
		return errors.New("skills: Skill migration is pending; run `stellad storage migrate-skills` before startup")
	case "completed":
		// This is the established read-only aggregate/Home/usage verifier.
		if _, err := migration.MigrateSkillHomeAuthority(ctx, SkillMigrationOptions{DryRun: true}); err != nil {
			return fmt.Errorf("skills: verify Home authority: %w", err)
		}
		return nil
	default:
		return errors.New("skills: malformed Skill migration marker")
	}
}

func initializeEmptySkillHomeAuthority(ctx context.Context, db *pgxpool.Pool, q *sqlc.Queries, hooks authorityEnsureHooks) (sqlc.StorageMigration, error) {
	if hooks.beforeFreshLock != nil {
		if err := hooks.beforeFreshLock(); err != nil {
			return sqlc.StorageMigration{}, err
		}
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return sqlc.StorageMigration{}, fmt.Errorf("skills: begin empty authority initialization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := q.WithTx(tx)
	if err := qtx.LockSkillMigrationSource(ctx); err != nil {
		return sqlc.StorageMigration{}, fmt.Errorf("skills: lock migration source: %w", err)
	}
	count, err := qtx.CountSkillMigrationSource(ctx)
	if err != nil {
		return sqlc.StorageMigration{}, fmt.Errorf("skills: count migration source: %w", err)
	}
	if count != 0 {
		return sqlc.StorageMigration{}, errors.New("skills: legacy Skills require `stellad storage migrate-skills` before startup")
	}
	expected := emptySkillMigrationMetadata()
	marker, insertErr := qtx.InitializeEmptySkillAuthorityStorageMigration(ctx, sqlc.InitializeEmptySkillAuthorityStorageMigrationParams{Name: SkillHomeAuthorityMigration, Metadata: expected})
	if insertErr != nil && !errors.Is(insertErr, pgx.ErrNoRows) {
		return sqlc.StorageMigration{}, fmt.Errorf("skills: initialize empty authority marker: %w", insertErr)
	}
	if err := tx.Commit(ctx); err != nil {
		// Commit can be ambiguous. A durable exact marker proves the desired
		// outcome; anything else remains explicitly unknown.
		reloaded, reloadErr := q.GetStorageMigration(ctx, SkillHomeAuthorityMigration)
		if reloadErr == nil && validateSkillMigrationMarker(reloaded, expected) == nil {
			return reloaded, nil
		}
		return sqlc.StorageMigration{}, fmt.Errorf("%w: commit empty authority marker: %w", sandbox.ErrOutcomeUnknown, errors.Join(err, reloadErr))
	}
	if insertErr == nil {
		if err := validateSkillMigrationMarker(marker, expected); err != nil {
			return sqlc.StorageMigration{}, fmt.Errorf("%w: initialized empty authority marker: %w", sandbox.ErrOutcomeUnknown, err)
		}
		return marker, nil
	}
	marker, err = q.GetStorageMigration(ctx, SkillHomeAuthorityMigration)
	if err != nil {
		return sqlc.StorageMigration{}, fmt.Errorf("%w: reload competing authority marker: %w", sandbox.ErrOutcomeUnknown, err)
	}
	return marker, nil
}

func emptySkillMigrationMetadata() []byte {
	hash := sha256.Sum256([]byte(skillHomeAuthorityLayout + "\x00"))
	return skillMigrationMetadata(SkillMigrationSummary{SHA256: hex.EncodeToString(hash[:])})
}
