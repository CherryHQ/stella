package simple

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func nullAgent(agentID string) sql.NullString {
	return sql.NullString{String: agentID, Valid: agentID != ""}
}

func requireSessionScope(ctx context.Context, userID, agentID string) (string, string, error) {
	if userID == "" {
		userID = memory.UserIDFromContext(ctx)
	}
	if agentID == "" {
		agentID = memory.AgentIDFromContext(ctx)
	}
	if userID == "" {
		return "", "", fmt.Errorf("missing user context")
	}
	if agentID == "" {
		return "", "", fmt.Errorf("missing agent context")
	}
	return userID, agentID, nil
}

func requireMemorySessionScope(ctx context.Context, session memory.Session) (memory.Session, error) {
	userID, agentID, err := requireSessionScope(ctx, session.UserID, session.AgentID)
	if err != nil {
		return memory.Session{}, err
	}
	session.UserID = userID
	session.AgentID = agentID
	return session, nil
}

func conversationScopeParams(session memory.Session) sqlc.GetConversationBySessionIDParams {
	return sqlc.GetConversationBySessionIDParams{
		SessionID: session.ID,
		UserID:    sql.NullString{String: session.UserID, Valid: true},
		AgentID:   nullAgent(session.AgentID),
	}
}

// Compile-time interface checks.
var (
	_ memory.Provider                 = (*Provider)(nil)
	_ memory.ProfileStore             = (*Provider)(nil)
	_ memory.SessionManager           = (*Provider)(nil)
	_ memory.ChangelogWriter          = (*Provider)(nil)
	_ memory.ChangelogReader          = (*Provider)(nil)
	_ memory.ConstraintStore          = (*Provider)(nil)
	_ memory.VersionedProfileStore    = (*Provider)(nil)
	_ memory.VersionedConstraintStore = (*Provider)(nil)
	_ memory.SessionSnapshotStore     = (*Provider)(nil)
)

// Provider implements a minimal sliding-window memory provider.
// It stores messages in the same schema as LCM but does not write
// summaries or context items. Assemble returns the last N messages
// that fit within the token budget.
type Provider struct {
	db        *sql.DB
	q         *sqlc.Queries
	log       *slog.Logger
	sessionMu map[string]*sync.Mutex
	globalMu  sync.Mutex
}

// New creates a new simple provider backed by the given database.
func New(db *sql.DB) *Provider {
	return &Provider{
		db:        db,
		q:         sqlc.New(db),
		log:       slog.Default(),
		sessionMu: make(map[string]*sync.Mutex),
	}
}

// Name implements memory.Provider.
func (p *Provider) Name() string { return "simple" }

// Bootstrap implements memory.Provider.
func (p *Provider) Bootstrap(ctx context.Context, session memory.Session) error {
	_, err := p.getOrCreateConversation(ctx, session)
	return err
}

// Append implements memory.Provider.
func (p *Provider) Append(ctx context.Context, session memory.Session, msgs ...ai.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	return p.withSessionLock(session.ID, func() error {
		convID, err := p.getOrCreateConversation(ctx, session)
		if err != nil {
			return err
		}

		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		qtx := p.q.WithTx(tx)

		seq, err := qtx.GetMaxSeq(ctx, convID)
		if err != nil {
			return fmt.Errorf("get max seq: %w", err)
		}

		for _, msg := range msgs {
			rows := messageToRows(msg)
			for _, row := range rows {
				seq++
				_, err := qtx.CreateMessage(ctx, sqlc.CreateMessageParams{
					ID:             uuid.NewString(),
					ConversationID: convID,
					Seq:            seq,
					Role:           row.role,
					EventType:      row.eventType,
					Content:        row.content,
					TokenCount:     int64(memory.EstimateTokens(row.content)),
				})
				if err != nil {
					return fmt.Errorf("create message: %w", err)
				}
			}
		}

		return tx.Commit()
	})
}

// Assemble implements memory.Provider.
// Returns the last N messages that fit within budget, always honouring freshTail.
func (p *Provider) Assemble(ctx context.Context, session memory.Session, budget, freshTail int) ([]ai.Message, error) {
	convID, err := p.getOrCreateConversation(ctx, session)
	if err != nil {
		return nil, err
	}

	dbMsgs, err := p.q.GetMessagesByConversation(ctx, convID)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}

	allMsgs := rowsToMessages(dbMsgs)
	if len(allMsgs) == 0 {
		return nil, nil
	}

	// Sliding window: walk backwards from the end, collecting messages
	// that fit within budget. Always include at least freshTail messages.
	used := 0
	start := len(allMsgs)
	for i := len(allMsgs) - 1; i >= 0; i-- {
		tokens := estimateMessageTokens(allMsgs[i])
		tailCount := len(allMsgs) - i
		if tailCount > freshTail && used+tokens > budget {
			break
		}
		used += tokens
		start = i
	}

	return allMsgs[start:], nil
}

// Stats implements memory.Provider.
func (p *Provider) Stats(ctx context.Context, session memory.Session) (memory.SessionStats, error) {
	session, err := requireMemorySessionScope(ctx, session)
	if err != nil {
		return memory.SessionStats{}, err
	}
	conv, err := p.q.GetConversationBySessionID(ctx, conversationScopeParams(session))
	if errors.Is(err, sql.ErrNoRows) {
		return memory.SessionStats{}, nil
	}
	if err != nil {
		return memory.SessionStats{}, fmt.Errorf("get conversation: %w", err)
	}

	msgs, err := p.q.GetMessagesByConversation(ctx, conv.ID)
	if err != nil {
		return memory.SessionStats{}, fmt.Errorf("get messages: %w", err)
	}

	var tokenCount int64
	for _, m := range msgs {
		tokenCount += m.TokenCount
	}

	stats := memory.SessionStats{
		MessageCount: len(msgs),
		TokenCount:   int(tokenCount),
		SummaryCount: 0,
	}

	if len(msgs) > 0 {
		stats.OldestAt = parseTime(msgs[0].CreatedAt)
		stats.NewestAt = parseTime(msgs[len(msgs)-1].CreatedAt)
	}

	return stats, nil
}

// Close implements memory.Provider.
func (p *Provider) Close() error { return nil }

// --- ProfileStore ---

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
		return "", nil
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
		return "", nil
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
		return []memory.ConstraintEntry{}, nil
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
			UpdatedAt: parseSnapshotTime(snap.UpdatedAt),
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
		UpdatedAt: parseSnapshotTime(created.UpdatedAt),
	}, nil
}

func parseSnapshotTime(s string) time.Time {
	t, _ := time.Parse("2006-01-02 15:04:05", s)
	return t
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
	params := sqlc.InsertMemoryChangelogParams{
		ID:      uuid.NewString(),
		UserID:  e.UserID,
		AgentID: e.AgentID,
		Scope:   e.Scope,
		Action:  e.Action,
		Source:  string(e.Source),
	}
	if e.SessionID != "" {
		params.SessionID = sql.NullString{String: e.SessionID, Valid: true}
	}
	if e.MemoryVersionBefore != nil {
		params.MemoryVersionBefore = sql.NullInt64{Int64: *e.MemoryVersionBefore, Valid: true}
	}
	if e.MemoryVersionAfter != nil {
		params.MemoryVersionAfter = sql.NullInt64{Int64: *e.MemoryVersionAfter, Valid: true}
	}
	if e.BeforeText != "" {
		params.BeforeText = sql.NullString{String: e.BeforeText, Valid: true}
	}
	if e.AfterText != "" {
		params.AfterText = sql.NullString{String: e.AfterText, Valid: true}
	}
	return params
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

// --- SessionManager ---

// SaveInfo implements memory.SessionManager.
func (p *Provider) SaveInfo(ctx context.Context, info memory.SessionInfo) error {
	userID, agentIDValue, err := requireSessionScope(ctx, info.UserID, info.AgentID)
	if err != nil {
		return err
	}
	info.UserID = userID
	info.AgentID = agentIDValue

	conv, err := p.q.GetConversationBySessionID(ctx, sqlc.GetConversationBySessionIDParams{SessionID: info.ID, UserID: sql.NullString{String: info.UserID, Valid: true}, AgentID: nullAgent(info.AgentID)})
	if errors.Is(err, sql.ErrNoRows) {
		lastActive := info.LastActive
		if lastActive.IsZero() {
			lastActive = time.Now().UTC()
		}
		kind := info.Kind
		if kind == "" {
			kind = "chat"
		}
		_, err = p.q.CreateConversation(ctx, sqlc.CreateConversationParams{
			ID:         uuid.NewString(),
			SessionID:  info.ID,
			Title:      sql.NullString{String: info.Title, Valid: info.Title != ""},
			Channel:    info.Channel,
			Kind:       kind,
			ProjectID:  sql.NullString{String: info.ProjectID, Valid: info.ProjectID != ""},
			Archived:   boolToInt(info.Archived),
			LastActive: lastActive.UTC().Format("2006-01-02 15:04:05"),
			AgentID:    sql.NullString{String: info.AgentID, Valid: info.AgentID != ""},
			UserID:     sql.NullString{String: info.UserID, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("create conversation: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get conversation: %w", err)
	}

	agentID := nullAgent(info.AgentID)

	if info.Title != "" {
		if err := p.q.UpdateConversationTitleBySessionID(ctx, sqlc.UpdateConversationTitleBySessionIDParams{
			Title:     sql.NullString{String: info.Title, Valid: true},
			SessionID: info.ID,
			UserID:    sql.NullString{String: info.UserID, Valid: true},
			AgentID:   agentID,
		}); err != nil {
			return fmt.Errorf("update title: %w", err)
		}
	}
	if boolToInt(info.Archived) != conv.Archived {
		if err := p.q.UpdateConversationArchived(ctx, sqlc.UpdateConversationArchivedParams{
			Archived:  boolToInt(info.Archived),
			SessionID: info.ID,
			UserID:    sql.NullString{String: info.UserID, Valid: true},
			AgentID:   agentID,
		}); err != nil {
			return fmt.Errorf("update archived: %w", err)
		}
	}
	if (info.Kind != "" && info.Kind != conv.Kind) || (info.ProjectID != "" && (!conv.ProjectID.Valid || info.ProjectID != conv.ProjectID.String)) {
		kind := info.Kind
		if kind == "" {
			kind = conv.Kind
		}
		projectID := sql.NullString{String: info.ProjectID, Valid: info.ProjectID != ""}
		if info.ProjectID == "" && conv.ProjectID.Valid {
			projectID = conv.ProjectID
		}
		if err := p.q.UpdateConversationKindProject(ctx, sqlc.UpdateConversationKindProjectParams{
			Kind:      kind,
			ProjectID: projectID,
			SessionID: info.ID,
			UserID:    sql.NullString{String: info.UserID, Valid: true},
			AgentID:   agentID,
		}); err != nil {
			return fmt.Errorf("update kind/project: %w", err)
		}
	}

	if err := p.q.UpdateConversationLastActive(ctx, sqlc.UpdateConversationLastActiveParams{SessionID: info.ID, UserID: sql.NullString{String: info.UserID, Valid: true}, AgentID: agentID}); err != nil {
		return fmt.Errorf("update last_active: %w", err)
	}
	return nil
}

// LoadInfo implements memory.SessionManager.
func (p *Provider) LoadInfo(ctx context.Context, sessionID string) (memory.SessionInfo, error) {
	userID, agentID, err := requireSessionScope(ctx, "", "")
	if err != nil {
		return memory.SessionInfo{}, err
	}
	conv, err := p.q.GetConversationBySessionID(ctx, sqlc.GetConversationBySessionIDParams{SessionID: sessionID, UserID: sql.NullString{String: userID, Valid: true}, AgentID: nullAgent(agentID)})
	if err != nil {
		return memory.SessionInfo{}, fmt.Errorf("get conversation: %w", err)
	}
	return convToSessionInfo(conv), nil
}

// ListInfo implements memory.SessionManager.
func (p *Provider) ListInfo(ctx context.Context, opts memory.ListOptions) ([]memory.SessionInfo, error) {
	var convs []sqlc.CtxConversation
	var err error
	userID, agentIDValue, err := requireSessionScope(ctx, opts.UserID, opts.AgentID)
	if err != nil {
		return nil, err
	}
	agentID := sql.NullString{String: agentIDValue, Valid: true}
	if opts.IncludeArchived {
		convs, err = p.q.ListConversationsAll(ctx, sqlc.ListConversationsAllParams{UserID: sql.NullString{String: userID, Valid: true}, AgentID: agentID})
	} else {
		convs, err = p.q.ListConversations(ctx, sqlc.ListConversationsParams{UserID: sql.NullString{String: userID, Valid: true}, AgentID: agentID})
	}
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}

	var result []memory.SessionInfo
	skipped := 0
	for _, c := range convs {
		info := convToSessionInfo(c)
		if opts.AgentID != "" && info.AgentID != opts.AgentID {
			continue
		}
		if opts.UserID != "" && info.UserID != opts.UserID {
			continue
		}
		if opts.Kind != "" && info.Kind != opts.Kind {
			continue
		}
		if opts.ProjectID != "" && info.ProjectID != opts.ProjectID {
			continue
		}
		if skipped < opts.Offset {
			skipped++
			continue
		}
		result = append(result, info)
		if opts.Limit > 0 && len(result) >= opts.Limit {
			break
		}
	}
	return result, nil
}

// LoadHistory implements memory.SessionManager.
func (p *Provider) LoadHistory(ctx context.Context, sessionID string) ([]ai.Message, error) {
	userID, agentID, err := requireSessionScope(ctx, "", "")
	if err != nil {
		return nil, err
	}
	conv, err := p.q.GetConversationBySessionID(ctx, sqlc.GetConversationBySessionIDParams{SessionID: sessionID, UserID: sql.NullString{String: userID, Valid: true}, AgentID: nullAgent(agentID)})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}

	msgs, err := p.q.GetMessagesByConversation(ctx, conv.ID)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}

	return rowsToMessages(msgs), nil
}

// --- Internal helpers ---

func (p *Provider) withSessionLock(sessionID string, fn func() error) error {
	p.globalMu.Lock()
	mu, ok := p.sessionMu[sessionID]
	if !ok {
		mu = &sync.Mutex{}
		p.sessionMu[sessionID] = mu
	}
	p.globalMu.Unlock()

	mu.Lock()
	defer mu.Unlock()
	return fn()
}

func (p *Provider) getOrCreateConversation(ctx context.Context, session memory.Session) (string, error) {
	session, err := requireMemorySessionScope(ctx, session)
	if err != nil {
		return "", err
	}
	sessionID := session.ID

	conv, err := p.q.GetConversationBySessionID(ctx, conversationScopeParams(session))
	if err == nil {
		return conv.ID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("get conversation: %w", err)
	}

	conv, err = p.q.CreateConversation(ctx, sqlc.CreateConversationParams{
		ID:         uuid.NewString(),
		SessionID:  sessionID,
		Channel:    session.Channel,
		Kind:       "chat",
		AgentID:    nullAgent(session.AgentID),
		UserID:     sql.NullString{String: session.UserID, Valid: true},
		LastActive: time.Now().UTC().Format("2006-01-02 15:04:05"),
	})
	if err != nil {
		return "", fmt.Errorf("create conversation: %w", err)
	}
	return conv.ID, nil
}

func convToSessionInfo(conv sqlc.CtxConversation) memory.SessionInfo {
	info := memory.SessionInfo{
		ID:       conv.SessionID,
		Channel:  conv.Channel,
		Kind:     conv.Kind,
		Archived: conv.Archived != 0,
	}
	if conv.Title.Valid {
		info.Title = conv.Title.String
	}
	if conv.AgentID.Valid {
		info.AgentID = conv.AgentID.String
	}
	if conv.UserID.Valid {
		info.UserID = conv.UserID.String
	}
	if conv.ProjectID.Valid {
		info.ProjectID = conv.ProjectID.String
	}
	if t, err := time.Parse("2006-01-02 15:04:05", conv.CreatedAt); err == nil {
		info.CreatedAt = t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", conv.LastActive); err == nil {
		info.LastActive = t
	}
	return info
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func parseTime(s string) time.Time {
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// --- Message conversion (duplicated from LCM, simplified) ---

type storageRow struct {
	role      string
	eventType string
	content   string
}

const (
	roleUser      = "user"
	roleAssistant = "assistant"
	roleTool      = "tool"

	eventTypeText       = "text"
	eventTypeThinking   = "thinking"
	eventTypeMultimodal = "multimodal"
	eventTypeToolCall   = "tool_call"
	eventTypeToolResult = "tool_result"
)

type toolCallEnvelope struct {
	ID   string          `json:"id"`
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

type toolResultEnvelope struct {
	ID     string          `json:"id"`
	Tool   string          `json:"tool"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error,omitempty"`
}

// TODO: messageToRows/rowsToMessages are duplicated between simple/ and lcm/ — extract into a shared helper in internal/memory/.
func messageToRows(msg ai.Message) []storageRow {
	switch m := msg.(type) {
	case ai.UserMessage:
		return userMessageToRows(m)
	case ai.AssistantMessage:
		return assistantMessageToRows(m)
	case ai.ToolResultMessage:
		return toolResultToRows(m)
	default:
		return nil
	}
}

func userMessageToRows(m ai.UserMessage) []storageRow {
	switch c := m.Content.(type) {
	case string:
		if c == "" {
			return nil
		}
		return []storageRow{{role: roleUser, eventType: eventTypeText, content: c}}
	case []ai.ContentBlock:
		data, err := json.Marshal(contentBlocksToJSON(c))
		if err != nil {
			return nil
		}
		return []storageRow{{role: roleUser, eventType: eventTypeMultimodal, content: string(data)}}
	default:
		s := fmt.Sprintf("%v", m.Content)
		if s == "" {
			return nil
		}
		return []storageRow{{role: roleUser, eventType: eventTypeText, content: s}}
	}
}

func assistantMessageToRows(m ai.AssistantMessage) []storageRow {
	var rows []storageRow
	for _, block := range m.Content {
		switch b := block.(type) {
		case ai.ThinkingContent:
			if b.Thinking != "" {
				rows = append(rows, storageRow{role: roleAssistant, eventType: eventTypeThinking, content: b.Thinking})
			}
		case ai.TextContent:
			if b.Text != "" {
				rows = append(rows, storageRow{role: roleAssistant, eventType: eventTypeText, content: b.Text})
			}
		case ai.ToolCall:
			argsJSON, _ := json.Marshal(b.Arguments)
			envelope := toolCallEnvelope{ID: b.ID, Tool: b.Name, Args: argsJSON}
			data, _ := json.Marshal(envelope)
			rows = append(rows, storageRow{role: roleAssistant, eventType: eventTypeToolCall, content: string(data)})
		}
	}
	return rows
}

func toolResultToRows(m ai.ToolResultMessage) []storageRow {
	text := ai.FlattenText(m.Content)
	resultJSON, _ := json.Marshal(text)
	var errStr string
	if m.IsError {
		errStr = text
	}
	envelope := toolResultEnvelope{
		ID:     m.ToolCallID,
		Tool:   m.ToolName,
		Result: resultJSON,
		Error:  errStr,
	}
	data, _ := json.Marshal(envelope)
	return []storageRow{{role: roleTool, eventType: eventTypeToolResult, content: string(data)}}
}

type contentBlockJSON struct {
	Kind     string `json:"kind"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

func contentBlocksToJSON(blocks []ai.ContentBlock) []contentBlockJSON {
	out := make([]contentBlockJSON, 0, len(blocks))
	for _, b := range blocks {
		switch b := b.(type) {
		case ai.TextContent:
			out = append(out, contentBlockJSON{Kind: "text", Text: b.Text})
		case ai.ImageContent:
			out = append(out, contentBlockJSON{Kind: "image", Data: b.Data, MimeType: b.MimeType})
		}
	}
	return out
}

func rowsToMessages(msgs []sqlc.CtxMessage) []ai.Message {
	var result []ai.Message
	i := 0
	for i < len(msgs) {
		msg := msgs[i]
		switch msg.Role {
		case roleUser:
			result = append(result, rowToUserMessage(msg))
			i++
		case roleAssistant:
			am, consumed := mergeAssistantRows(msgs, i)
			result = append(result, am)
			i += consumed
		case roleTool:
			result = append(result, rowToToolResult(msg))
			i++
		default:
			i++
		}
	}
	return result
}

func rowToUserMessage(msg sqlc.CtxMessage) ai.UserMessage {
	ts, _ := time.Parse("2006-01-02 15:04:05", msg.CreatedAt)
	if msg.EventType == eventTypeMultimodal {
		var blocks []contentBlockJSON
		if json.Unmarshal([]byte(msg.Content), &blocks) == nil && len(blocks) > 0 {
			content := make([]ai.ContentBlock, 0, len(blocks))
			for _, b := range blocks {
				switch b.Kind {
				case "text":
					content = append(content, ai.TextContent{Text: b.Text})
				case "image":
					content = append(content, ai.ImageContent{Data: b.Data, MimeType: b.MimeType})
				}
			}
			return ai.UserMessage{Content: content, Timestamp: ts}
		}
	}
	return ai.UserMessage{Content: msg.Content, Timestamp: ts}
}

func mergeAssistantRows(msgs []sqlc.CtxMessage, start int) (ai.AssistantMessage, int) {
	var blocks []ai.ContentBlock
	consumed := 0

	msg := msgs[start]
	switch msg.EventType {
	case eventTypeText:
		blocks = append(blocks, ai.TextContent{Text: msg.Content})
		consumed++
	case eventTypeThinking:
		blocks = append(blocks, ai.ThinkingContent{Thinking: msg.Content})
		consumed++
	case eventTypeToolCall:
		if call, ok := decodeToolCall(msg.Content); ok {
			blocks = append(blocks, call)
		}
		consumed++
	default:
		blocks = append(blocks, ai.TextContent{Text: msg.Content})
		consumed++
		return ai.AssistantMessage{Content: blocks}, consumed
	}

	for start+consumed < len(msgs) {
		next := msgs[start+consumed]
		if next.Role != roleAssistant {
			break
		}
		switch next.EventType {
		case eventTypeThinking:
			blocks = append(blocks, ai.ThinkingContent{Thinking: next.Content})
		case eventTypeText:
			blocks = append(blocks, ai.TextContent{Text: next.Content})
		case eventTypeToolCall:
			if call, ok := decodeToolCall(next.Content); ok {
				blocks = append(blocks, call)
			}
		default:
			return ai.AssistantMessage{Content: blocks}, consumed
		}
		consumed++
	}

	return ai.AssistantMessage{Content: blocks}, consumed
}

func decodeToolCall(content string) (ai.ToolCall, bool) {
	var env toolCallEnvelope
	if err := json.Unmarshal([]byte(content), &env); err != nil {
		return ai.ToolCall{}, false
	}
	var args map[string]any
	_ = json.Unmarshal(env.Args, &args)
	return ai.ToolCall{ID: env.ID, Name: env.Tool, Arguments: args}, true
}

func rowToToolResult(msg sqlc.CtxMessage) ai.ToolResultMessage {
	var env toolResultEnvelope
	if err := json.Unmarshal([]byte(msg.Content), &env); err != nil {
		return ai.ToolResultMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: msg.Content}},
		}
	}
	var text string
	_ = json.Unmarshal(env.Result, &text)
	return ai.ToolResultMessage{
		ToolCallID: env.ID,
		ToolName:   env.Tool,
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
		IsError:    env.Error != "",
	}
}

func estimateMessageTokens(msg ai.Message) int {
	switch m := msg.(type) {
	case ai.AssistantMessage:
		total := 0
		for _, block := range m.Content {
			switch b := block.(type) {
			case ai.TextContent:
				total += memory.EstimateTokens(b.Text)
			case ai.ToolCall:
				total += memory.EstimateTokens(b.Name)
				if b.Arguments != nil {
					data, _ := json.Marshal(b.Arguments)
					total += memory.EstimateTokens(string(data))
				}
			}
		}
		return total
	default:
		return memory.EstimateTokens(memory.MessageText(msg))
	}
}
