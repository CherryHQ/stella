package lcm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/memorywrite"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/memory"
)

// Compile-time checks for snapshot interfaces.
var (
	_ memory.VersionedProfileStore    = (*Provider)(nil)
	_ memory.VersionedConstraintStore = (*Provider)(nil)
	_ memory.SessionSnapshotStore     = (*Provider)(nil)
)

// getMemoryRow fetches the ctx_agent_memory row, returning nil for non-existent rows.
func (p *Provider) getMemoryRow(ctx context.Context, userID string, agentID string) (*sqlc.CtxAgentMemory, error) {
	mem, err := p.q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{
		UserID:  userID,
		AgentID: agentID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &mem, nil
}

func (p *Provider) GetProfile(ctx context.Context, userID string, agentID string) (string, error) {
	row, err := p.getMemoryRow(ctx, userID, agentID)
	if err != nil {
		return "", fmt.Errorf("get profile: %w", err)
	}
	if row == nil {
		return "", nil
	}
	return row.Content, nil
}

func (p *Provider) SetProfile(ctx context.Context, userID string, agentID string, content string) error {
	if err := memorywrite.SetProfile(ctx, p.db, p.q, userID, agentID, content); err != nil {
		return fmt.Errorf("set profile: %w", err)
	}
	return nil
}

func (p *Provider) GetAgentSoul(ctx context.Context, userID string, agentID string) (string, error) {
	row, err := p.getMemoryRow(ctx, userID, agentID)
	if err != nil {
		return "", fmt.Errorf("get agent soul: %w", err)
	}
	if row == nil {
		return "", nil
	}
	return row.Soul, nil
}

func (p *Provider) SetAgentSoul(ctx context.Context, userID string, agentID string, content string) error {
	if err := memorywrite.SetAgentSoul(ctx, p.db, p.q, userID, agentID, content); err != nil {
		return fmt.Errorf("set agent soul: %w", err)
	}
	return nil
}

// GetConstraints implements memory.ConstraintStore.
func (p *Provider) GetConstraints(ctx context.Context, userID string, agentID string) ([]memory.ConstraintEntry, error) {
	return memorywrite.GetConstraints(ctx, p.q, userID, agentID)
}

// AddConstraint implements memory.ConstraintStore.
func (p *Provider) AddConstraint(ctx context.Context, userID string, agentID string, text string) ([]memory.ConstraintEntry, error) {
	return memorywrite.AddConstraint(ctx, p.db, p.q, userID, agentID, text)
}

// RemoveConstraint implements memory.ConstraintStore.
func (p *Provider) RemoveConstraint(ctx context.Context, userID string, agentID string, id string) ([]memory.ConstraintEntry, error) {
	return memorywrite.RemoveConstraint(ctx, p.db, p.q, userID, agentID, id)
}

// GetProfileAt implements memory.VersionedProfileStore.
func (p *Provider) GetProfileAt(ctx context.Context, userID string, agentID string, version int64) (string, error) {
	if version <= 0 {
		return p.GetProfile(ctx, userID, agentID)
	}
	entry, err := p.q.GetMemoryChangelogAtVersion(ctx, sqlc.GetMemoryChangelogAtVersionParams{
		UserID:             userID,
		AgentID:            agentID,
		Scope:              "profile",
		MemoryVersionAfter: sql.NullInt64{Int64: version, Valid: true},
	})
	if err == nil {
		if entry.AfterText.Valid {
			return entry.AfterText.String, nil
		}
		return "", nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return p.GetProfile(ctx, userID, agentID)
	}
	return "", fmt.Errorf("get profile at version %d: %w", version, err)
}

// GetAgentSoulAt implements memory.VersionedProfileStore.
func (p *Provider) GetAgentSoulAt(ctx context.Context, userID string, agentID string, version int64) (string, error) {
	if version <= 0 {
		return p.GetAgentSoul(ctx, userID, agentID)
	}
	entry, err := p.q.GetMemoryChangelogAtVersion(ctx, sqlc.GetMemoryChangelogAtVersionParams{
		UserID:             userID,
		AgentID:            agentID,
		Scope:              "soul",
		MemoryVersionAfter: sql.NullInt64{Int64: version, Valid: true},
	})
	if err == nil {
		if entry.AfterText.Valid {
			return entry.AfterText.String, nil
		}
		return "", nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return p.GetAgentSoul(ctx, userID, agentID)
	}
	return "", fmt.Errorf("get agent soul at version %d: %w", version, err)
}

// GetConstraintsAt implements memory.VersionedConstraintStore.
func (p *Provider) GetConstraintsAt(ctx context.Context, userID string, agentID string, version int64) ([]memory.ConstraintEntry, error) {
	if version <= 0 {
		return p.GetConstraints(ctx, userID, agentID)
	}
	entry, err := p.q.GetMemoryChangelogAtVersion(ctx, sqlc.GetMemoryChangelogAtVersionParams{
		UserID:             userID,
		AgentID:            agentID,
		Scope:              "constraint",
		MemoryVersionAfter: sql.NullInt64{Int64: version, Valid: true},
	})
	if err == nil {
		if entry.AfterText.Valid {
			return memorywrite.ParseConstraintsJSON(entry.AfterText.String)
		}
		return []memory.ConstraintEntry{}, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return p.GetConstraints(ctx, userID, agentID)
	}
	return nil, fmt.Errorf("get constraints at version %d: %w", version, err)
}

// GetOrCreateSessionSnapshot implements memory.SessionSnapshotStore.
func (p *Provider) GetOrCreateSessionSnapshot(ctx context.Context, sessionID string, userID string, agentID string) (memory.SessionSnapshot, error) {
	snap, err := p.q.GetMemorySnapshot(ctx, sqlc.GetMemorySnapshotParams{
		SessionID: sessionID,
		UserID:    userID,
		AgentID:   agentID,
	})
	if err == nil {
		return memory.SessionSnapshot{
			SessionID: snap.SessionID,
			UserID:    snap.UserID,
			AgentID:   snap.AgentID,
			Version:   snap.Version,
		}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return memory.SessionSnapshot{}, fmt.Errorf("get snapshot: %w", err)
	}

	// Create: freeze current version.
	row, err := p.getMemoryRow(ctx, userID, agentID)
	var currentVersion int64
	if err == nil && row != nil {
		currentVersion = row.Version
	}

	created, err := p.q.CreateMemorySnapshot(ctx, sqlc.CreateMemorySnapshotParams{
		SessionID: sessionID,
		UserID:    userID,
		AgentID:   agentID,
		Version:   currentVersion,
	})
	if err != nil {
		return memory.SessionSnapshot{}, fmt.Errorf("create snapshot: %w", err)
	}
	return memory.SessionSnapshot{
		SessionID: created.SessionID,
		UserID:    created.UserID,
		AgentID:   created.AgentID,
		Version:   created.Version,
	}, nil
}

// AdvanceSessionSnapshot implements memory.SessionSnapshotStore.
func (p *Provider) AdvanceSessionSnapshot(ctx context.Context, sessionID string, userID string, agentID string) error {
	row, err := p.getMemoryRow(ctx, userID, agentID)
	if err != nil {
		return fmt.Errorf("advance snapshot: read memory row: %w", err)
	}
	if row == nil {
		return nil
	}
	return p.q.AdvanceMemorySnapshot(ctx, sqlc.AdvanceMemorySnapshotParams{
		Version:   row.Version,
		SessionID: sessionID,
		UserID:    userID,
		AgentID:   agentID,
	})
}

// WriteChangelog implements memory.ChangelogWriter.
func (p *Provider) WriteChangelog(ctx context.Context, entry memory.ChangeEntry) error {
	return p.q.InsertMemoryChangelog(ctx, changeEntryToParams(entry))
}

// ReadChangelog implements memory.ChangelogReader.
func (p *Provider) ReadChangelog(ctx context.Context, userID string, agentID string, scope string, limit int) ([]memory.ChangeEntry, error) {
	rows, err := p.q.ListMemoryChangelog(ctx, sqlc.ListMemoryChangelogParams{
		UserID:  userID,
		AgentID: agentID,
		Scope:   scope,
		Limit:   int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list changelog: %w", err)
	}
	entries := make([]memory.ChangeEntry, len(rows))
	for i, r := range rows {
		entries[i] = changelogRowToEntry(r)
	}
	return entries, nil
}

func changeEntryToParams(e memory.ChangeEntry) sqlc.InsertMemoryChangelogParams {
	p := sqlc.InsertMemoryChangelogParams{
		ID:      uuid.NewString(),
		UserID:  e.UserID,
		AgentID: e.AgentID,
		Scope:   e.Scope,
		Action:  e.Action,
		Source:  string(e.Source),
	}
	if e.SessionID != "" {
		p.SessionID = sql.NullString{String: e.SessionID, Valid: true}
	}
	if e.MemoryVersionBefore != nil {
		p.MemoryVersionBefore = sql.NullInt64{Int64: *e.MemoryVersionBefore, Valid: true}
	}
	if e.MemoryVersionAfter != nil {
		p.MemoryVersionAfter = sql.NullInt64{Int64: *e.MemoryVersionAfter, Valid: true}
	}
	if e.BeforeText != "" {
		p.BeforeText = sql.NullString{String: e.BeforeText, Valid: true}
	}
	if e.AfterText != "" {
		p.AfterText = sql.NullString{String: e.AfterText, Valid: true}
	}
	return p
}

func changelogRowToEntry(r sqlc.MemoryChangelog) memory.ChangeEntry {
	e := memory.ChangeEntry{
		ID:        r.ID,
		UserID:    r.UserID,
		AgentID:   r.AgentID,
		Scope:     r.Scope,
		Action:    r.Action,
		Source:    memory.ChangeSource(r.Source),
		CreatedAt: r.CreatedAt,
	}
	if r.SessionID.Valid {
		e.SessionID = r.SessionID.String
	}
	if r.MemoryVersionBefore.Valid {
		v := r.MemoryVersionBefore.Int64
		e.MemoryVersionBefore = &v
	}
	if r.MemoryVersionAfter.Valid {
		v := r.MemoryVersionAfter.Int64
		e.MemoryVersionAfter = &v
	}
	if r.BeforeText.Valid {
		e.BeforeText = r.BeforeText.String
	}
	if r.AfterText.Valid {
		e.AfterText = r.AfterText.String
	}
	return e
}
