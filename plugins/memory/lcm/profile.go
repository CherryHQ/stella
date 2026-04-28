package lcm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/vaayne/anna/internal/memorywrite"
	"github.com/vaayne/anna/pkg/db/sqlc"
	"github.com/vaayne/anna/pkg/memory"
)

// getMemoryRow fetches the ctx_agent_memory row, returning nil for non-existent rows.
func (p *Provider) getMemoryRow(ctx context.Context, userID int64, agentID string) (*sqlc.CtxAgentMemory, error) {
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

func (p *Provider) GetProfile(ctx context.Context, userID int64, agentID string) (string, error) {
	row, err := p.getMemoryRow(ctx, userID, agentID)
	if err != nil {
		return "", fmt.Errorf("get profile: %w", err)
	}
	if row == nil {
		return "", nil
	}
	return row.Content, nil
}

func (p *Provider) SetProfile(ctx context.Context, userID int64, agentID string, content string) error {
	if err := memorywrite.SetProfile(ctx, p.db, p.q, userID, agentID, content); err != nil {
		return fmt.Errorf("set profile: %w", err)
	}
	return nil
}

func (p *Provider) GetAgentSoul(ctx context.Context, userID int64, agentID string) (string, error) {
	row, err := p.getMemoryRow(ctx, userID, agentID)
	if err != nil {
		return "", fmt.Errorf("get agent soul: %w", err)
	}
	if row == nil {
		return "", nil
	}
	return row.Soul, nil
}

func (p *Provider) SetAgentSoul(ctx context.Context, userID int64, agentID string, content string) error {
	if err := memorywrite.SetAgentSoul(ctx, p.db, p.q, userID, agentID, content); err != nil {
		return fmt.Errorf("set agent soul: %w", err)
	}
	return nil
}

// GetConstraints implements memory.ConstraintStore.
func (p *Provider) GetConstraints(ctx context.Context, userID int64, agentID string) ([]memory.ConstraintEntry, error) {
	return memorywrite.GetConstraints(ctx, p.q, userID, agentID)
}

// AddConstraint implements memory.ConstraintStore.
func (p *Provider) AddConstraint(ctx context.Context, userID int64, agentID string, text string) ([]memory.ConstraintEntry, error) {
	return memorywrite.AddConstraint(ctx, p.db, p.q, userID, agentID, text)
}

// RemoveConstraint implements memory.ConstraintStore.
func (p *Provider) RemoveConstraint(ctx context.Context, userID int64, agentID string, id string) ([]memory.ConstraintEntry, error) {
	return memorywrite.RemoveConstraint(ctx, p.db, p.q, userID, agentID, id)
}

// WriteChangelog implements memory.ChangelogWriter.
func (p *Provider) WriteChangelog(ctx context.Context, entry memory.ChangeEntry) error {
	return p.q.InsertMemoryChangelog(ctx, changeEntryToParams(entry))
}

// ReadChangelog implements memory.ChangelogReader.
func (p *Provider) ReadChangelog(ctx context.Context, userID int64, agentID string, scope string, limit int) ([]memory.ChangeEntry, error) {
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
