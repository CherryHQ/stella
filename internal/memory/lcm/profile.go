package lcm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Compile-time checks for snapshot interfaces.
var (
	_ memory.VersionedProfileStore    = (*Provider)(nil)
	_ memory.VersionedConstraintStore = (*Provider)(nil)
	_ memory.SessionSnapshotStore     = (*Provider)(nil)
	_ memory.ProfileEntryStore        = (*Provider)(nil)
	_ memory.GroupMemoryStore         = (*Provider)(nil)
	_ memory.FactStore                = (*Provider)(nil)
	_ memory.VersionedFactStore       = (*Provider)(nil)
)

// getMemoryRow fetches the ctx_agent_memory row, returning nil for non-existent rows.
func (p *Provider) getMemoryRow(ctx context.Context, userID string, agentID string) (*sqlc.CtxAgentMemory, error) {
	mem, err := p.q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{
		UserID:  userID,
		AgentID: agentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &mem, nil
}

func (p *Provider) GetProfile(ctx context.Context, userID string, agentID string) (string, error) {
	return p.getSingletonFactContent(ctx, userID, agentID, memory.FactSubjectUser)
}

func (p *Provider) SetProfile(ctx context.Context, userID string, agentID string, content string) error {
	if err := p.setSingletonFact(ctx, userID, agentID, memory.FactSubjectUser, content); err != nil {
		return fmt.Errorf("set profile: %w", err)
	}
	return nil
}

func (p *Provider) GetAgentSoul(ctx context.Context, userID string, agentID string) (string, error) {
	return p.getSingletonFactContent(ctx, userID, agentID, memory.FactSubjectAgent)
}

func (p *Provider) SetAgentSoul(ctx context.Context, userID string, agentID string, content string) error {
	if err := p.setSingletonFact(ctx, userID, agentID, memory.FactSubjectAgent, content); err != nil {
		return fmt.Errorf("set agent soul: %w", err)
	}
	return nil
}

func (p *Provider) getSingletonFactContent(ctx context.Context, userID string, agentID string, subject memory.FactSubject) (string, error) {
	facts, err := memorywrite.ListActiveFacts(ctx, p.q, userID, agentID, subject)
	if err != nil {
		return "", fmt.Errorf("get fact %s: %w", subject, err)
	}
	if len(facts) == 0 {
		return "", nil
	}
	return facts[0].Content, nil
}

func (p *Provider) setSingletonFact(ctx context.Context, userID string, agentID string, subject memory.FactSubject, content string) error {
	facts, err := memorywrite.ListActiveFacts(ctx, p.q, userID, agentID, subject)
	if err != nil {
		return fmt.Errorf("list existing fact: %w", err)
	}
	write := memory.FactWrite{
		UserID:  userID,
		AgentID: agentID,
		Subject: subject,
		Content: content,
		Source:  memory.SourceManual,
	}
	if len(facts) == 0 {
		_, err = memorywrite.CreateFact(ctx, p.db, p.q, write)
	} else {
		_, err = memorywrite.ReplaceFact(ctx, p.db, p.q, facts[0].ID, write)
	}
	if err != nil {
		return fmt.Errorf("write fact %s: %w", subject, err)
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
	facts, err := p.ListActiveFactsAt(ctx, userID, agentID, memory.FactSubjectUser, version)
	if err != nil {
		return "", fmt.Errorf("get profile at version %d: %w", version, err)
	}
	if len(facts) == 0 {
		return "", nil
	}
	return facts[0].Content, nil
}

// GetAgentSoulAt implements memory.VersionedProfileStore.
func (p *Provider) GetAgentSoulAt(ctx context.Context, userID string, agentID string, version int64) (string, error) {
	if version <= 0 {
		return p.GetAgentSoul(ctx, userID, agentID)
	}
	facts, err := p.ListActiveFactsAt(ctx, userID, agentID, memory.FactSubjectAgent, version)
	if err != nil {
		return "", fmt.Errorf("get agent soul at version %d: %w", version, err)
	}
	if len(facts) == 0 {
		return "", nil
	}
	return facts[0].Content, nil
}

// ListActiveFacts implements memory.FactStore.
func (p *Provider) ListActiveFacts(ctx context.Context, userID string, agentID string, subject memory.FactSubject) ([]memory.Fact, error) {
	return memorywrite.ListActiveFacts(ctx, p.q, userID, agentID, subject)
}

// ListActiveFactsAt implements memory.VersionedFactStore. The snapshot clock is
// ctx_agent_memory.version; fact.version remains local to each fact row.
func (p *Provider) ListActiveFactsAt(ctx context.Context, userID string, agentID string, subject memory.FactSubject, version int64) ([]memory.Fact, error) {
	return memorywrite.ListActiveFactsAt(ctx, p.q, userID, agentID, subject, version)
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
		MemoryVersionAfter: pgtype.Int8{Int64: version, Valid: true},
	})
	if err == nil {
		if entry.AfterText.Valid {
			return memorywrite.ParseConstraintsJSON(entry.AfterText.String)
		}
		return []memory.ConstraintEntry{}, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
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
			UpdatedAt: snap.UpdatedAt.UTC(),
		}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
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
		UpdatedAt: created.UpdatedAt.UTC(),
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
		Limit:   int32(limit),
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
		ID:      uuid.Must(uuid.NewV7()).String(),
		UserID:  e.UserID,
		AgentID: e.AgentID,
		Scope:   e.Scope,
		Action:  e.Action,
		Source:  string(e.Source),
	}
	if e.SessionID != "" {
		p.SessionID = pgtype.Text{String: e.SessionID, Valid: true}
	}
	if e.MemoryVersionBefore != nil {
		p.MemoryVersionBefore = pgtype.Int8{Int64: *e.MemoryVersionBefore, Valid: true}
	}
	if e.MemoryVersionAfter != nil {
		p.MemoryVersionAfter = pgtype.Int8{Int64: *e.MemoryVersionAfter, Valid: true}
	}
	if e.BeforeText != "" {
		p.BeforeText = pgtype.Text{String: e.BeforeText, Valid: true}
	}
	if e.AfterText != "" {
		p.AfterText = pgtype.Text{String: e.AfterText, Valid: true}
	}
	return p
}

func changelogRowToEntry(r sqlc.CtxAgentMemoryChangelog) memory.ChangeEntry {
	e := memory.ChangeEntry{
		ID:        r.ID,
		UserID:    r.UserID,
		AgentID:   r.AgentID,
		Scope:     r.Scope,
		Action:    r.Action,
		Source:    memory.ChangeSource(r.Source),
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
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

// GetProfileEntries implements memory.ProfileEntryStore.
func (p *Provider) GetProfileEntries(ctx context.Context, userID string, agentID string) ([]memory.ProfileEntry, error) {
	return memorywrite.GetProfileEntries(ctx, p.q, userID, agentID)
}

// GetGroupMemory implements memory.GroupMemoryStore.
func (p *Provider) GetGroupMemory(ctx context.Context, groupID string) (string, error) {
	return memorywrite.GetGroupMemory(ctx, p.q, groupID)
}
