// Package profile is the application boundary for per-(user, agent) memory: the
// profile blob, agent soul, hard constraints, reset, and the change history. It
// composes the memory Provider's ProfileStore/ChangelogReader with the
// memorywrite transactional helpers and the raw ctx_agent_memory rows, and owns
// the Agent-access gate and the change-source audit that the HTTP transport used
// to orchestrate itself.
//
// It lives beside memorywrite (not in package memory) because it reuses
// memorywrite, and memorywrite imports memory — a service in package memory could
// not call it without an import cycle. Every use case is Authority-bound: the
// caller passes a trusted authz.Authority. Agent-specific operations authorize
// agent access; cross-agent user lists enforce self-or-admin before any read.
package profile

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// AgentAuthorizer is the narrow Agent PEP port: it authorizes an action on an
// agent for a trusted authority. agentaccess.Service satisfies it. Profile
// operations gate on read access to the agent (the same gate the transport's
// requireAgentAccess applied), so a user may only touch memory for an agent they
// can see.
type AgentAuthorizer interface {
	Authorize(ctx context.Context, authority authz.Authority, agentID string, action authz.Action) error
}

// Service owns the per-(user, agent) memory use cases.
type Service struct {
	db          *pgxpool.Pool
	q           *sqlc.Queries
	profiles    memory.ProfileStore
	changelog   memory.ChangelogReader
	agents      AgentAuthorizer
	defaultSoul func() string
	log         *slog.Logger
}

// NewService builds the profile service. profiles/changelog are the memory
// Provider viewed through its capability interfaces (nil when the Provider does
// not implement them — the matching endpoints then report 503). defaultSoul
// supplies the fallback soul when none is stored. log defaults to slog.Default().
func NewService(db *pgxpool.Pool, profiles memory.ProfileStore, changelog memory.ChangelogReader, agents AgentAuthorizer, defaultSoul func() string, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		db:          db,
		q:           sqlc.New(db),
		profiles:    profiles,
		changelog:   changelog,
		agents:      agents,
		defaultSoul: defaultSoul,
		log:         log,
	}
}

// Typed errors. Agent-gate denials propagate the agentaccess sentinels
// unchanged so the transport maps them to the same 404/403 as everywhere else.
var (
	// ErrConstraintNotFound reports a delete of a constraint that does not exist (404).
	ErrConstraintNotFound = errors.New("constraint not found")
	// ErrProfileStoreUnavailable reports a Provider without ProfileStore (503).
	ErrProfileStoreUnavailable = errors.New("profile memory store not configured")
	// ErrChangelogReaderUnavailable reports a Provider without ChangelogReader (503).
	ErrChangelogReaderUnavailable = errors.New("memory changelog reader not configured")
)

// Memory is the transport-neutral projection of one (user, agent) memory row with
// its fact-backed profile/soul applied. Constraints/ProfileEntries are decoded
// domain values (never raw JSON), and timestamps are UTC.
type Memory struct {
	UserID         string
	AgentID        string
	Content        string
	Soul           string
	Version        int64
	Constraints    []memory.ConstraintEntry
	ProfileEntries []memory.ProfileEntry
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// gate authorizes read access to the agent for the caller. It returns the
// agentaccess sentinel unchanged.
func (s *Service) gate(ctx context.Context, authority authz.Authority, agentID string) error {
	return s.agents.Authorize(ctx, authority, agentID, authz.ActionRead)
}

// changeSource is the audit source for a caller mutating targetUserID's memory:
// an admin acting on another user's memory is a System change; a user acting on
// their own (or a non-admin) is a User change.
func changeSource(authority authz.Authority, targetUserID string) memory.ChangeSource {
	if authority.IsAdmin() && string(authority.UserID()) != targetUserID {
		return memory.SourceSystem
	}
	return memory.SourceUser
}

// Memory returns the caller's own memory for an agent (fact-backed profile/soul,
// constraints, entries). Gated on agent read access.
func (s *Service) Memory(ctx context.Context, authority authz.Authority, agentID string) (Memory, error) {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return Memory{}, err
	}
	return s.loadMemory(ctx, string(authority.UserID()), agentID)
}

// SetSoul overwrites the caller's agent soul (a User-sourced change) and returns
// the refreshed memory. Gated on agent read access.
func (s *Service) SetSoul(ctx context.Context, authority authz.Authority, agentID, soul string) (Memory, error) {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return Memory{}, err
	}
	if s.profiles == nil {
		return Memory{}, ErrProfileStoreUnavailable
	}
	userID := string(authority.UserID())
	ctx = memory.WithChangeSource(ctx, memory.SourceUser)
	if err := s.profiles.SetAgentSoul(ctx, userID, agentID, soul); err != nil {
		return Memory{}, err
	}
	return s.loadMemory(ctx, userID, agentID)
}

// ListConstraints returns the caller's hard constraints for an agent.
func (s *Service) ListConstraints(ctx context.Context, authority authz.Authority, agentID string) ([]memory.ConstraintEntry, error) {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return nil, err
	}
	return memorywrite.GetConstraints(ctx, s.q, string(authority.UserID()), agentID)
}

// AddConstraint appends a hard constraint (a Manual-sourced change) and returns
// the full updated list.
func (s *Service) AddConstraint(ctx context.Context, authority authz.Authority, agentID, text string) ([]memory.ConstraintEntry, error) {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return nil, err
	}
	ctx = memory.WithChangeSource(ctx, memory.SourceManual)
	return memorywrite.AddConstraint(ctx, s.db, s.q, string(authority.UserID()), agentID, text)
}

// RemoveConstraint deletes a constraint by ID (a Manual-sourced change),
// returning ErrConstraintNotFound when it does not belong to the caller's agent
// memory, and otherwise the updated list.
func (s *Service) RemoveConstraint(ctx context.Context, authority authz.Authority, agentID, constraintID string) ([]memory.ConstraintEntry, error) {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return nil, err
	}
	userID := string(authority.UserID())
	existing, err := memorywrite.GetConstraints(ctx, s.q, userID, agentID)
	if err != nil {
		return nil, err
	}
	found := false
	for _, c := range existing {
		if c.ID == constraintID {
			found = true
			break
		}
	}
	if !found {
		return nil, ErrConstraintNotFound
	}
	ctx = memory.WithChangeSource(ctx, memory.SourceManual)
	return memorywrite.RemoveConstraint(ctx, s.db, s.q, userID, agentID, constraintID)
}

// Changelog returns the merged change history for the caller's agent memory
// across the requested scopes (profile/soul via the Provider's changelog reader,
// constraint from the durable changelog table). It reads at most limit rows per
// scope; the transport merges, sorts, and truncates for presentation.
func (s *Service) Changelog(ctx context.Context, authority authz.Authority, agentID string, scopes []string, limit int) ([]memory.ChangeEntry, error) {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return nil, err
	}
	userID := string(authority.UserID())
	out := make([]memory.ChangeEntry, 0, limit)
	for _, scope := range scopes {
		switch scope {
		case "profile", "soul":
			if s.changelog == nil {
				return nil, ErrChangelogReaderUnavailable
			}
			rows, err := s.changelog.ReadChangelog(ctx, userID, agentID, scope, limit)
			if err != nil {
				return nil, err
			}
			out = append(out, rows...)
		default:
			rows, err := s.q.ListMemoryChangelog(ctx, sqlc.ListMemoryChangelogParams{
				UserID:  userID,
				AgentID: agentID,
				Scope:   scope,
				Limit:   int32(limit),
			})
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				out = append(out, changeEntryFromRow(row))
			}
		}
	}
	return out, nil
}

// ListUserMemories returns every agent memory for one user (fact-backed). It is
// self-or-admin; a foreign target is opaque and denied before the durable list
// read. This reads across agents and therefore has no single-agent gate.
func (s *Service) ListUserMemories(ctx context.Context, authority authz.Authority, targetUserID string) ([]Memory, error) {
	if !authority.Valid() {
		return nil, authz.ErrForbidden
	}
	if !authority.IsAdmin() && (authority.Kind() != authz.ActorUser || string(authority.UserID()) != targetUserID) {
		return nil, authz.ErrNotFound
	}
	rows, err := s.q.ListUserAgentMemoriesByUser(ctx, targetUserID)
	if err != nil {
		return nil, err
	}
	out := make([]Memory, 0, len(rows))
	for _, row := range rows {
		m, err := s.memoryFromRow(ctx, row)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// SetUserMemory overwrites targetUserID's profile for an agent and returns the
// refreshed memory. The change source reflects whether an admin is acting on
// another user's memory. Gated on the caller's read access to the agent.
func (s *Service) SetUserMemory(ctx context.Context, authority authz.Authority, targetUserID, agentID, content string) (Memory, error) {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return Memory{}, err
	}
	if s.profiles == nil {
		return Memory{}, ErrProfileStoreUnavailable
	}
	ctx = memory.WithChangeSource(ctx, changeSource(authority, targetUserID))
	if err := s.profiles.SetProfile(ctx, targetUserID, agentID, content); err != nil {
		return Memory{}, err
	}
	return s.loadMemory(ctx, targetUserID, agentID)
}

// DeleteUserMemory resets targetUserID's memory for an agent in a single
// transaction. The change source reflects admin-acting-on-other. Gated on the
// caller's read access to the agent.
func (s *Service) DeleteUserMemory(ctx context.Context, authority authz.Authority, targetUserID, agentID string) error {
	if err := s.gate(ctx, authority, agentID); err != nil {
		return err
	}
	ctx = memory.WithChangeSource(ctx, changeSource(authority, targetUserID))
	return memorywrite.ResetUserAgentMemory(ctx, s.db, s.q, targetUserID, agentID)
}

// loadMemory fetches one (user, agent) row (or a fresh empty one) and applies the
// fact-backed profile/soul.
func (s *Service) loadMemory(ctx context.Context, userID, agentID string) (Memory, error) {
	row, err := s.q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{UserID: userID, AgentID: agentID})
	if errors.Is(err, pgx.ErrNoRows) {
		row = sqlc.CtxAgentMemory{UserID: userID, AgentID: agentID, Constraints: []byte("[]"), ProfileEntries: []byte("[]")}
	} else if err != nil {
		return Memory{}, err
	}
	return s.memoryFromRow(ctx, row)
}

// memoryFromRow projects a row into the domain Memory, overriding content/soul
// with the fact-backed values and defaulting an empty soul.
func (s *Service) memoryFromRow(ctx context.Context, row sqlc.CtxAgentMemory) (Memory, error) {
	if s.profiles == nil {
		return Memory{}, ErrProfileStoreUnavailable
	}
	content, err := s.profiles.GetProfile(ctx, row.UserID, row.AgentID)
	if err != nil {
		return Memory{}, err
	}
	soul, err := s.profiles.GetAgentSoul(ctx, row.UserID, row.AgentID)
	if err != nil {
		return Memory{}, err
	}
	if soul == "" && s.defaultSoul != nil {
		soul = s.defaultSoul()
	}
	constraints, err := memorywrite.ParseConstraintsJSON(string(row.Constraints))
	if err != nil {
		return Memory{}, err
	}
	entries, err := memorywrite.ParseProfileEntriesJSON(string(row.ProfileEntries))
	if err != nil {
		return Memory{}, err
	}
	return Memory{
		UserID:         row.UserID,
		AgentID:        row.AgentID,
		Content:        content,
		Soul:           soul,
		Version:        row.Version,
		Constraints:    constraints,
		ProfileEntries: entries,
		CreatedAt:      row.CreatedAt.UTC(),
		UpdatedAt:      row.UpdatedAt.UTC(),
	}, nil
}

// changeEntryFromRow converts a durable changelog row into the neutral
// memory.ChangeEntry the Provider-backed scopes already return, so the transport
// maps one changelog type. CreatedAt is rendered RFC3339Nano and re-parsed by the
// transport, preserving the instant.
func changeEntryFromRow(row sqlc.CtxAgentMemoryChangelog) memory.ChangeEntry {
	return memory.ChangeEntry{
		ID:                  row.ID,
		UserID:              row.UserID,
		AgentID:             row.AgentID,
		SessionID:           textValue(row.SessionID),
		Scope:               row.Scope,
		Action:              row.Action,
		Source:              memory.ChangeSource(row.Source),
		MemoryVersionBefore: int8Ptr(row.MemoryVersionBefore),
		MemoryVersionAfter:  int8Ptr(row.MemoryVersionAfter),
		BeforeText:          textValue(row.BeforeText),
		AfterText:           textValue(row.AfterText),
		CreatedAt:           row.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func int8Ptr(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

func textValue(v pgtype.Text) string {
	if !v.Valid {
		return ""
	}
	return v.String
}
