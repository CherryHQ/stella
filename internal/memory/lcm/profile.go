package lcm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/agentrun"
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
	write := memory.FactWrite{
		UserID:  userID,
		AgentID: agentID,
		Subject: subject,
		Content: content,
		Source:  memory.SourceManual,
	}
	if _, err := memorywrite.SetSingletonFact(ctx, p.db, p.q, write); err != nil {
		return fmt.Errorf("write fact %s: %w", subject, err)
	}
	return nil
}

// ApplyFactBatch applies multiple fact mutations in one transaction. Reflect
// uses this capability after reconciliation so fact-line writes can fail closed
// without partially updating profile, soul, or world knowledge.
func (p *Provider) ApplyFactBatch(ctx context.Context, userID string, agentID string, ops []memorywrite.FactBatchOperation) ([]memory.Fact, error) {
	return memorywrite.ApplyFactBatch(ctx, p.db, p.q, userID, agentID, ops)
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
	if version < 0 {
		return "", fmt.Errorf("memory snapshot version must be non-negative: %d", version)
	}
	content, err := p.getIdentityFactAt(ctx, userID, agentID, memory.FactSubjectUser, "profile", version)
	if err != nil {
		return "", fmt.Errorf("get profile at version %d: %w", version, err)
	}
	return content, nil
}

// GetAgentSoulAt implements memory.VersionedProfileStore.
func (p *Provider) GetAgentSoulAt(ctx context.Context, userID string, agentID string, version int64) (string, error) {
	if version < 0 {
		return "", fmt.Errorf("memory snapshot version must be non-negative: %d", version)
	}
	content, err := p.getIdentityFactAt(ctx, userID, agentID, memory.FactSubjectAgent, "soul", version)
	if err != nil {
		return "", fmt.Errorf("get agent soul at version %d: %w", version, err)
	}
	return content, nil
}

func (p *Provider) getIdentityFactAt(ctx context.Context, userID string, agentID string, subject memory.FactSubject, legacyScope string, version int64) (string, error) {
	facts, seenFacts, err := memorywrite.ListActiveFactsAtSnapshot(ctx, p.q, userID, agentID, subject, version)
	if err != nil {
		return "", err
	}
	if seenFacts {
		if len(facts) == 0 {
			return "", nil
		}
		return facts[0].Content, nil
	}
	return p.getLegacyIdentityAt(ctx, userID, agentID, legacyScope, version)
}

func (p *Provider) getLegacyIdentityAt(ctx context.Context, userID string, agentID string, scope string, version int64) (string, error) {
	entry, err := p.q.GetMemoryChangelogAtVersion(ctx, sqlc.GetMemoryChangelogAtVersionParams{
		UserID:             userID,
		AgentID:            agentID,
		Scope:              scope,
		MemoryVersionAfter: pgtype.Int8{Int64: version, Valid: true},
	})
	if err == nil {
		if entry.AfterText.Valid {
			return entry.AfterText.String, nil
		}
		return "", nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("get legacy %s at version %d: %w", scope, version, err)
	}

	// Pre-facts snapshots may only have the legacy row, not a full changelog.
	row, err := p.getMemoryRow(ctx, userID, agentID)
	if err != nil || row == nil {
		return "", err
	}
	// The mutable legacy row cannot represent a snapshot older than its version.
	if row.Version > version {
		return "", nil
	}
	switch scope {
	case "profile":
		return row.Content, nil
	case "soul":
		return row.Soul, nil
	default:
		return "", nil
	}
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
	if version < 0 {
		return nil, fmt.Errorf("memory snapshot version must be non-negative: %d", version)
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
		row, rowErr := p.getMemoryRow(ctx, userID, agentID)
		if rowErr != nil {
			return nil, fmt.Errorf("get constraints at version %d: %w", version, rowErr)
		}
		// Legacy constraints have no changelog, so only use a row frozen no later
		// than the requested snapshot.
		if row == nil || row.Version > version {
			return []memory.ConstraintEntry{}, nil
		}
		return memorywrite.ParseConstraintsJSON(string(row.Constraints))
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

	created, err := agentrun.WriteTxValue(ctx, p.db, func(q *sqlc.Queries) (sqlc.CtxAgentMemorySnapshot, error) {
		return q.CreateMemorySnapshot(ctx, sqlc.CreateMemorySnapshotParams{
			SessionID: sessionID,
			UserID:    userID,
			AgentID:   agentID,
			Version:   currentVersion,
		})
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
	return agentrun.WriteTx(ctx, p.db, func(q *sqlc.Queries) error {
		return q.AdvanceMemorySnapshot(ctx, sqlc.AdvanceMemorySnapshotParams{
			Version:   row.Version,
			SessionID: sessionID,
			UserID:    userID,
			AgentID:   agentID,
		})
	})
}

// WriteChangelog implements memory.ChangelogWriter.
func (p *Provider) WriteChangelog(ctx context.Context, entry memory.ChangeEntry) error {
	return agentrun.WriteTx(ctx, p.db, func(q *sqlc.Queries) error {
		return q.InsertMemoryChangelog(ctx, changeEntryToParams(entry))
	})
}

// ReadChangelog implements memory.ChangelogReader.
func (p *Provider) ReadChangelog(ctx context.Context, userID string, agentID string, scope string, limit int) ([]memory.ChangeEntry, error) {
	if subject, ok := identitySubjectForHistoryScope(scope); ok {
		return p.readIdentityFactChangelog(ctx, userID, agentID, scope, subject, limit)
	}
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

// ReadChangelogPage implements memory.ChangelogPageReader without changing the
// recent-only ChangelogReader contract used by memory tools.
func (p *Provider) ReadChangelogPage(ctx context.Context, userID string, agentID string, scope string, cursor *memory.ChangelogCursor, limit int) ([]memory.ChangeEntry, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("list changelog page: limit must be positive")
	}
	cursorCreatedAt, cursorID, err := changelogCursorParams(cursor)
	if err != nil {
		return nil, err
	}
	if subject, ok := identitySubjectForHistoryScope(scope); ok {
		return p.readIdentityFactChangelogPage(ctx, userID, agentID, scope, subject, cursorCreatedAt, cursorID, limit)
	}
	rows, err := p.q.ListMemoryChangelogPage(ctx, sqlc.ListMemoryChangelogPageParams{
		UserID: userID, AgentID: agentID, Scope: scope,
		CursorCreatedAt: cursorCreatedAt, CursorID: cursorID, LimitCount: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list changelog page: %w", err)
	}
	entries := make([]memory.ChangeEntry, len(rows))
	for i, row := range rows {
		entries[i] = changelogRowToEntry(row)
	}
	return entries, nil
}

func (p *Provider) readIdentityFactChangelogPage(
	ctx context.Context,
	userID string,
	agentID string,
	scope string,
	subject memory.FactSubject,
	cursorCreatedAt pgtype.Timestamptz,
	cursorID pgtype.Text,
	limit int,
) ([]memory.ChangeEntry, error) {
	pageRows, err := p.q.ListFactChangelogBySubjectPage(ctx, sqlc.ListFactChangelogBySubjectPageParams{
		UserID: userID, AgentID: agentID, Subject: pgtype.Text{String: string(subject), Valid: true},
		CursorCreatedAt: cursorCreatedAt, CursorID: cursorID, LimitCount: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list fact changelog page: %w", err)
	}

	groups := make([][]sqlc.CtxAgentMemoryChangelog, 0, limit)
	var currentVersion int64
	for _, pageRow := range pageRows {
		row := factChangelogPageRow(pageRow)
		if len(groups) == 0 || row.MemoryVersionAfter.Int64 != currentVersion {
			currentVersion = row.MemoryVersionAfter.Int64
			groups = append(groups, []sqlc.CtxAgentMemoryChangelog{})
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], row)
	}

	entries := make([]memory.ChangeEntry, 0, len(groups))
	for _, rows := range groups {
		entry, ok, err := projectIdentityFactChangelogGroup(scope, subject, rows)
		if err != nil {
			return nil, err
		}
		if ok {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func changelogCursorParams(cursor *memory.ChangelogCursor) (pgtype.Timestamptz, pgtype.Text, error) {
	if cursor == nil {
		return pgtype.Timestamptz{}, pgtype.Text{}, nil
	}
	if cursor.CreatedAt.IsZero() || cursor.ID == "" {
		return pgtype.Timestamptz{}, pgtype.Text{}, fmt.Errorf("changelog cursor created_at and id are required")
	}
	return pgtype.Timestamptz{Time: cursor.CreatedAt.UTC(), Valid: true}, pgtype.Text{String: cursor.ID, Valid: true}, nil
}

func factChangelogPageRow(row sqlc.ListFactChangelogBySubjectPageRow) sqlc.CtxAgentMemoryChangelog {
	return sqlc.CtxAgentMemoryChangelog(row)
}

func identitySubjectForHistoryScope(scope string) (memory.FactSubject, bool) {
	switch scope {
	case "profile":
		return memory.FactSubjectUser, true
	case "soul":
		return memory.FactSubjectAgent, true
	default:
		return "", false
	}
}

func (p *Provider) readIdentityFactChangelog(ctx context.Context, userID string, agentID string, scope string, subject memory.FactSubject, limit int) ([]memory.ChangeEntry, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := p.q.ListFactChangelogBySubject(ctx, sqlc.ListFactChangelogBySubjectParams{
		UserID:     userID,
		AgentID:    agentID,
		Subject:    pgtype.Text{String: string(subject), Valid: true},
		LimitCount: int32(limit * 3),
	})
	if err != nil {
		return nil, fmt.Errorf("list fact changelog: %w", err)
	}

	type group struct {
		version int64
		rows    []sqlc.CtxAgentMemoryChangelog
	}
	var groups []group
	for _, row := range rows {
		if !row.MemoryVersionAfter.Valid {
			continue
		}
		version := row.MemoryVersionAfter.Int64
		if len(groups) == 0 || groups[len(groups)-1].version != version {
			groups = append(groups, group{version: version})
		}
		groups[len(groups)-1].rows = append(groups[len(groups)-1].rows, row)
	}

	entries := make([]memory.ChangeEntry, 0, limit)
	for _, group := range groups {
		entry, ok, err := projectIdentityFactChangelogGroup(scope, subject, group.rows)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		entries = append(entries, entry)
		if len(entries) >= limit {
			break
		}
	}
	return entries, nil
}

type changelogFactState struct {
	row    sqlc.CtxAgentMemoryChangelog
	before *memory.Fact
	after  *memory.Fact
}

func projectIdentityFactChangelogGroup(scope string, subject memory.FactSubject, rows []sqlc.CtxAgentMemoryChangelog) (memory.ChangeEntry, bool, error) {
	var active *changelogFactState
	var deprecated *changelogFactState
	for _, row := range rows {
		before, err := parseChangelogFact(row.BeforeText)
		if err != nil {
			return memory.ChangeEntry{}, false, err
		}
		after, err := parseChangelogFact(row.AfterText)
		if err != nil {
			return memory.ChangeEntry{}, false, err
		}
		if after == nil || after.Subject != subject {
			continue
		}
		state := changelogFactState{row: row, before: before, after: after}
		switch after.Status {
		case memory.FactStatusActive:
			active = &state
		case memory.FactStatusDeprecated:
			if deprecated == nil {
				deprecated = &state
			}
		}
	}

	if active != nil {
		entry := changelogRowToEntry(active.row)
		entry.Scope = scope
		entry.AfterText = active.after.Content
		if active.before != nil && active.before.Subject == subject {
			entry.BeforeText = active.before.Content
		} else if deprecated != nil && deprecated.before != nil && deprecated.before.Subject == subject {
			entry.BeforeText = deprecated.before.Content
		}
		return entry, true, nil
	}
	if deprecated != nil {
		entry := changelogRowToEntry(deprecated.row)
		entry.Scope = scope
		entry.Action = "deprecate"
		entry.AfterText = ""
		if deprecated.before != nil && deprecated.before.Subject == subject {
			entry.BeforeText = deprecated.before.Content
		}
		return entry, true, nil
	}
	return memory.ChangeEntry{}, false, nil
}

func parseChangelogFact(text pgtype.Text) (*memory.Fact, error) {
	if !text.Valid || text.String == "" {
		return nil, nil
	}
	var fact memory.Fact
	if err := json.Unmarshal([]byte(text.String), &fact); err != nil {
		return nil, fmt.Errorf("parse fact changelog state: %w", err)
	}
	return &fact, nil
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
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339Nano),
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
