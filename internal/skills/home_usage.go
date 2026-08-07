package skills

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/sandbox"
)

// HomeSkillUsageIdentity identifies one user_agent Skill selected by a
// canonical filesystem ID and immutable content digest. It is telemetry-only;
// it never reads or writes the Home catalog.
type HomeSkillUsageIdentity struct {
	ID                string
	UserID            string
	AgentID           string
	Name              string
	LastContentDigest string
}

// HomeSkillUsage is the durable usage fact for a Home-authoritative Skill.
type HomeSkillUsage struct {
	LogicalID         string
	UserID            string
	AgentID           string
	Name              string
	LastContentDigest string
	UseCount          int64
	LastUsedAt        time.Time
	CreatedAt         time.Time
}

// HomeSkillUsageCandidate is a curator candidate gated by pair activity.
type HomeSkillUsageCandidate struct {
	HomeSkillUsage
	PairLatestActivityAt time.Time
	Rule                 string
}

// HomeSkillUsageStore owns PostgreSQL telemetry for Home-authoritative Reflect
// Skills. It deliberately depends only on narrow skill_usage queries.
type HomeSkillUsageStore struct{ q *sqlc.Queries }

func NewHomeSkillUsageStore(db *pgxpool.Pool) (*HomeSkillUsageStore, error) {
	if db == nil {
		return nil, errors.New("skills: Home Skill usage store requires database")
	}
	return &HomeSkillUsageStore{q: sqlc.New(db)}, nil
}

// InitializeReflectCreate inserts the first use exactly once. An exact retry
// observes the existing fact and never resets its count or timestamp.
func (s *HomeSkillUsageStore) InitializeReflectCreate(ctx context.Context, identity HomeSkillUsageIdentity) (changed bool, err error) {
	if err := validateHomeSkillUsageIdentity(identity); err != nil {
		return false, err
	}
	rows, err := s.q.InsertLogicalReflectSkillUsage(ctx, logicalUsageInsertParams(identity))
	if err != nil {
		return false, homeUsageMutationOutcome("initialize Home Skill usage", err)
	}
	if rows == 1 {
		return true, nil
	}
	if rows != 0 {
		return false, fmt.Errorf("skills: initialize Home Skill usage affected %d rows", rows)
	}
	if _, err := s.getExact(ctx, identity); err == nil {
		return false, nil
	} else if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrSkillUsageChanged
	} else {
		return false, err
	}
}

// PatchReflectDigest CAS-updates the selected revision without changing the
// use count. Repeating an already-applied patch is a no-op success.
func (s *HomeSkillUsageStore) PatchReflectDigest(ctx context.Context, identity HomeSkillUsageIdentity, newDigest string) (changed bool, err error) {
	if err := validateHomeSkillUsageIdentity(identity); err != nil {
		return false, err
	}
	if !validHomeSkillDigest(newDigest) {
		return false, errors.New("skills: new digest must be a lowercase SHA-256 digest")
	}
	rows, err := s.q.PatchLogicalReflectSkillUsageDigest(ctx, sqlc.PatchLogicalReflectSkillUsageDigestParams{
		NewDigest:      pgtype.Text{String: newDigest, Valid: true},
		UserID:         identity.UserID,
		AgentID:        identity.AgentID,
		Name:           pgtype.Text{String: identity.Name, Valid: true},
		ExpectedDigest: pgtype.Text{String: identity.LastContentDigest, Valid: true},
	})
	if err != nil {
		return false, homeUsageMutationOutcome("patch Home Skill usage digest", err)
	}
	if rows == 1 {
		return true, nil
	}
	if rows != 0 {
		return false, fmt.Errorf("skills: patch Home Skill usage digest affected %d rows", rows)
	}
	identity.LastContentDigest = newDigest
	if _, err := s.getExact(ctx, identity); err == nil {
		return false, nil
	} else if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrSkillUsageChanged
	} else {
		return false, err
	}
}

// TouchReflectRuntimeUse records one successful runtime load at the exact
// selected revision; a missing or stale fact is a CAS conflict.
func (s *HomeSkillUsageStore) TouchReflectRuntimeUse(ctx context.Context, identity HomeSkillUsageIdentity) error {
	if err := validateHomeSkillUsageIdentity(identity); err != nil {
		return err
	}
	rows, err := s.q.TouchLogicalReflectSkillRuntimeUse(ctx, sqlc.TouchLogicalReflectSkillRuntimeUseParams{
		UserID:            identity.UserID,
		AgentID:           identity.AgentID,
		Name:              pgtype.Text{String: identity.Name, Valid: true},
		LastContentDigest: pgtype.Text{String: identity.LastContentDigest, Valid: true},
	})
	if err != nil {
		return homeUsageMutationOutcome("touch Home Skill usage", err)
	}
	if rows == 0 {
		return ErrSkillUsageChanged
	}
	if rows != 1 {
		return fmt.Errorf("skills: touch Home Skill usage affected %d rows", rows)
	}
	return nil
}

// Get returns only the exact selected Home revision.
func (s *HomeSkillUsageStore) Get(ctx context.Context, identity HomeSkillUsageIdentity) (HomeSkillUsage, error) {
	if err := validateHomeSkillUsageIdentity(identity); err != nil {
		return HomeSkillUsage{}, err
	}
	return s.getExact(ctx, identity)
}

func (s *HomeSkillUsageStore) getExact(ctx context.Context, identity HomeSkillUsageIdentity) (HomeSkillUsage, error) {
	row, err := s.q.GetLogicalReflectSkillUsage(ctx, logicalUsageExactParams(identity))
	if err != nil {
		return HomeSkillUsage{}, err
	}
	if !row.Scope.Valid || row.Scope.String != "user_agent" || !row.Name.Valid || row.Name.String != identity.Name || !row.LastContentDigest.Valid || row.LastContentDigest.String != identity.LastContentDigest {
		return HomeSkillUsage{}, errors.New("skills: malformed logical Home Skill usage")
	}
	return HomeSkillUsage{
		LogicalID:         identity.ID,
		UserID:            row.UserID,
		AgentID:           row.AgentID,
		Name:              row.Name.String,
		LastContentDigest: row.LastContentDigest.String,
		UseCount:          row.UseCount,
		LastUsedAt:        row.LastUsedAt.UTC(),
		CreatedAt:         row.CreatedAt.UTC(),
	}, nil
}

// Delete removes only the exact curator decision. A changed timestamp, digest,
// or identity leaves the fact intact and reports a CAS conflict.
func (s *HomeSkillUsageStore) Delete(ctx context.Context, identity HomeSkillUsageIdentity, expectedLastUsedAt time.Time) error {
	if err := validateHomeSkillUsageIdentity(identity); err != nil {
		return err
	}
	if expectedLastUsedAt.IsZero() {
		return errors.New("skills: expected last used at is required")
	}
	rows, err := s.q.DeleteLogicalReflectSkillUsage(ctx, sqlc.DeleteLogicalReflectSkillUsageParams{
		UserID:             identity.UserID,
		AgentID:            identity.AgentID,
		Name:               pgtype.Text{String: identity.Name, Valid: true},
		LastContentDigest:  pgtype.Text{String: identity.LastContentDigest, Valid: true},
		ExpectedLastUsedAt: expectedLastUsedAt.UTC(),
	})
	if err != nil {
		return homeUsageMutationOutcome("delete Home Skill usage", err)
	}
	if rows == 0 {
		return ErrSkillUsageChanged
	}
	if rows != 1 {
		return fmt.Errorf("skills: delete Home Skill usage affected %d rows", rows)
	}
	return nil
}

// DeleteForLifecycle removes the exact logical telemetry fact after Home has
// committed a lifecycle cleanup. A missing fact is already-cleaned success;
// lifecycle cleanup deliberately does not use curator activity or timestamp CAS.
func (s *HomeSkillUsageStore) DeleteForLifecycle(ctx context.Context, identity HomeSkillUsageIdentity) error {
	if err := validateHomeSkillUsageIdentity(identity); err != nil {
		return err
	}
	rows, err := s.q.DeleteLogicalReflectSkillUsageForLifecycle(ctx, sqlc.DeleteLogicalReflectSkillUsageForLifecycleParams{
		UserID:            identity.UserID,
		AgentID:           identity.AgentID,
		Name:              pgtype.Text{String: identity.Name, Valid: true},
		LastContentDigest: pgtype.Text{String: identity.LastContentDigest, Valid: true},
	})
	if err != nil {
		return homeUsageMutationOutcome("delete Home Skill lifecycle usage", err)
	}
	if rows > 1 {
		return fmt.Errorf("skills: delete Home Skill lifecycle usage affected %d rows", rows)
	}
	return nil
}

// DeleteForCurator removes one exact logical usage fact only if the eligible
// pair activity which justified its curation decision still exists. This is a
// single DELETE ... EXISTS CAS: it never reads a Home or legacy Skill row.
func (s *HomeSkillUsageStore) DeleteForCurator(ctx context.Context, identity HomeSkillUsageIdentity, expectedLastUsedAt, expectedPairLatestActivityAt time.Time) error {
	if err := validateHomeSkillUsageIdentity(identity); err != nil {
		return err
	}
	if expectedLastUsedAt.IsZero() {
		return errors.New("skills: expected last used at is required")
	}
	if expectedPairLatestActivityAt.IsZero() {
		return errors.New("skills: expected pair latest activity at is required")
	}
	rows, err := s.q.DeleteLogicalReflectSkillUsageForCurator(ctx, sqlc.DeleteLogicalReflectSkillUsageForCuratorParams{
		UserID:                       identity.UserID,
		AgentID:                      identity.AgentID,
		Name:                         pgtype.Text{String: identity.Name, Valid: true},
		LastContentDigest:            pgtype.Text{String: identity.LastContentDigest, Valid: true},
		ExpectedLastUsedAt:           expectedLastUsedAt.UTC(),
		ExpectedPairLatestActivityAt: expectedPairLatestActivityAt.UTC(),
	})
	if err != nil {
		return homeUsageMutationOutcome("delete Home Skill usage for curator", err)
	}
	if rows == 0 {
		return ErrSkillUsageChanged
	}
	if rows != 1 {
		return fmt.Errorf("skills: delete Home Skill usage for curator affected %d rows", rows)
	}
	return nil
}

func homeUsageMutationOutcome(action string, err error) error {
	return fmt.Errorf("%w: %s: %w", sandbox.ErrOutcomeUnknown, action, err)
}

// ListStaleReflectCandidates returns only logical Home Skill telemetry and its
// eligible pair activity; it never consults the legacy skill table.
func (s *HomeSkillUsageStore) ListStaleReflectCandidates(ctx context.Context, userID, agentID string, staleBefore, lowUseBefore time.Time, lowUseMaxUseCount int64) ([]HomeSkillUsageCandidate, error) {
	if userID == "" || agentID == "" || staleBefore.IsZero() || lowUseBefore.IsZero() || lowUseMaxUseCount < 0 {
		return nil, errors.New("skills: invalid Home Skill usage curator filter")
	}
	rows, err := s.q.ListStaleLogicalReflectSkillUsageForCurator(ctx, sqlc.ListStaleLogicalReflectSkillUsageForCuratorParams{
		UserID:            userID,
		AgentID:           agentID,
		StaleBefore:       staleBefore.UTC(),
		LowUseBefore:      lowUseBefore.UTC(),
		LowUseMaxUseCount: lowUseMaxUseCount,
	})
	if err != nil {
		return nil, fmt.Errorf("skills: list stale Home Skill usage: %w", err)
	}
	out := make([]HomeSkillUsageCandidate, 0, len(rows))
	for _, row := range rows {
		candidate, err := homeSkillUsageCandidate(row)
		if err != nil {
			return nil, err
		}
		out = append(out, candidate)
	}
	return out, nil
}

func homeSkillUsageCandidate(row sqlc.ListStaleLogicalReflectSkillUsageForCuratorRow) (HomeSkillUsageCandidate, error) {
	logicalID, err := encodeFilesystemSkillID("user_agent", row.UserID, row.AgentID, row.Name)
	if err != nil {
		return HomeSkillUsageCandidate{}, fmt.Errorf("skills: encode logical Home Skill candidate: %w", err)
	}
	return HomeSkillUsageCandidate{
		HomeSkillUsage: HomeSkillUsage{
			LogicalID:         logicalID,
			UserID:            row.UserID,
			AgentID:           row.AgentID,
			Name:              row.Name,
			LastContentDigest: row.LastContentDigest,
			UseCount:          row.UseCount,
			LastUsedAt:        row.LastUsedAt.UTC(),
		},
		PairLatestActivityAt: row.PairLatestActivityAt.UTC(),
		Rule:                 row.Rule,
	}, nil
}

func validateHomeSkillUsageIdentity(identity HomeSkillUsageIdentity) error {
	scope, userID, agentID, name, err := decodeFilesystemSkillID(identity.ID)
	if err != nil {
		return err
	}
	if scope != "user_agent" || userID != identity.UserID || agentID != identity.AgentID || name != identity.Name {
		return errors.New("skills: filesystem Skill ID does not match logical usage identity")
	}
	if !validHomeSkillDigest(identity.LastContentDigest) {
		return errors.New("skills: digest must be a lowercase SHA-256 digest")
	}
	return nil
}

func logicalUsageInsertParams(identity HomeSkillUsageIdentity) sqlc.InsertLogicalReflectSkillUsageParams {
	return sqlc.InsertLogicalReflectSkillUsageParams{
		SkillID:           identity.ID,
		UserID:            identity.UserID,
		AgentID:           identity.AgentID,
		Name:              pgtype.Text{String: identity.Name, Valid: true},
		LastContentDigest: pgtype.Text{String: identity.LastContentDigest, Valid: true},
	}
}

func logicalUsageExactParams(identity HomeSkillUsageIdentity) sqlc.GetLogicalReflectSkillUsageParams {
	return sqlc.GetLogicalReflectSkillUsageParams{
		UserID:            identity.UserID,
		AgentID:           identity.AgentID,
		Name:              pgtype.Text{String: identity.Name, Valid: true},
		LastContentDigest: pgtype.Text{String: identity.LastContentDigest, Valid: true},
	}
}
