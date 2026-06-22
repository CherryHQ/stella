package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type PGStore struct {
	q *sqlc.Queries
}

func New(db *pgxpool.Pool) *PGStore {
	return &PGStore{q: sqlc.New(db)}
}

func (s *PGStore) Create(ctx context.Context, params CreateParams) (Entry, error) {
	if err := validateCreate(params); err != nil {
		return Entry{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Entry{}, fmt.Errorf("knowledge: new id: %w", err)
	}
	row, err := s.q.CreateKnowledge(ctx, sqlc.CreateKnowledgeParams{
		ID:         id.String(),
		Kind:       string(params.Kind),
		Scope:      params.Scope,
		UserID:     textParam(params.UserID),
		AgentID:    textParam(params.AgentID),
		Name:       params.Name,
		Content:    params.Content,
		Status:     string(defaultStatus(params.Status)),
		Evidence:   defaultJSON(params.Evidence, "[]"),
		Confidence: floatParam(params.Confidence),
		ExpiresAt:  timeParam(params.ExpiresAt),
		Supersedes: stringParam(params.Supersedes),
		Metadata:   defaultJSON(params.Metadata, "{}"),
	})
	if err != nil {
		return Entry{}, fmt.Errorf("knowledge: create %q: %w", params.Name, err)
	}
	return mapRow(row), nil
}

func (s *PGStore) Get(ctx context.Context, id string) (Entry, error) {
	row, err := s.q.GetKnowledge(ctx, id)
	if err != nil {
		return Entry{}, fmt.Errorf("knowledge: get %s: %w", id, err)
	}
	return mapRow(row), nil
}

func (s *PGStore) ListActive(ctx context.Context, vc ViewContext, kinds ...Kind) ([]Entry, error) {
	kind := ""
	if len(kinds) > 1 {
		return nil, fmt.Errorf("knowledge: multiple kind filters are not supported")
	}
	if len(kinds) == 1 {
		if !kinds[0].Valid() {
			return nil, fmt.Errorf("knowledge: invalid kind %q", kinds[0])
		}
		kind = string(kinds[0])
	}
	rows, err := s.q.ListActiveKnowledge(ctx, sqlc.ListActiveKnowledgeParams{
		AgentID: textParam(vc.AgentID),
		UserID:  textParam(vc.UserID),
		Kind:    kind,
	})
	if err != nil {
		return nil, fmt.Errorf("knowledge: list active: %w", err)
	}
	return mapRows(rows), nil
}

func (s *PGStore) ListByScope(ctx context.Context, scope string, userID string, agentID string) ([]Entry, error) {
	if err := validateScopeOwner(scope, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListKnowledgeByScope(ctx, sqlc.ListKnowledgeByScopeParams{
		Scope:   scope,
		UserID:  textParam(userID),
		AgentID: textParam(agentID),
	})
	if err != nil {
		return nil, fmt.Errorf("knowledge: list scope %q: %w", scope, err)
	}
	return mapRows(rows), nil
}

func (s *PGStore) ListByNameAndScope(ctx context.Context, name string, scope string, userID string, agentID string) ([]Entry, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("knowledge: name is required")
	}
	if err := validateScopeOwner(scope, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.q.ListKnowledgeByNameAndScope(ctx, sqlc.ListKnowledgeByNameAndScopeParams{
		Name:    name,
		Scope:   scope,
		UserID:  textParam(userID),
		AgentID: textParam(agentID),
	})
	if err != nil {
		return nil, fmt.Errorf("knowledge: list name %q scope %q: %w", name, scope, err)
	}
	return mapRows(rows), nil
}

func (s *PGStore) Update(ctx context.Context, params UpdateParams) (Entry, error) {
	if err := validateUpdate(params); err != nil {
		return Entry{}, err
	}
	row, err := s.q.UpdateKnowledge(ctx, sqlc.UpdateKnowledgeParams{
		ID:         params.ID,
		Name:       params.Name,
		Content:    params.Content,
		Status:     string(defaultStatus(params.Status)),
		Evidence:   defaultJSON(params.Evidence, "[]"),
		Confidence: floatParam(params.Confidence),
		ExpiresAt:  timeParam(params.ExpiresAt),
		Supersedes: stringParam(params.Supersedes),
		Metadata:   defaultJSON(params.Metadata, "{}"),
	})
	if err != nil {
		return Entry{}, fmt.Errorf("knowledge: update %s: %w", params.ID, err)
	}
	return mapRow(row), nil
}

func (s *PGStore) Deprecate(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("knowledge: id is required")
	}
	if err := s.q.DeprecateKnowledge(ctx, id); err != nil {
		return fmt.Errorf("knowledge: deprecate %s: %w", id, err)
	}
	return nil
}

func (s *PGStore) ExpireDraftsByType(ctx context.Context, kind Kind, before time.Time) error {
	if !kind.Valid() {
		return fmt.Errorf("knowledge: invalid kind %q", kind)
	}
	if err := s.q.ExpireKnowledgeDraftsByType(ctx, sqlc.ExpireKnowledgeDraftsByTypeParams{
		Kind:   string(kind),
		Cutoff: before.UTC(),
	}); err != nil {
		return fmt.Errorf("knowledge: expire drafts by type %q: %w", kind, err)
	}
	return nil
}

func validateCreate(params CreateParams) error {
	if !params.Kind.Valid() {
		return fmt.Errorf("knowledge: invalid kind %q", params.Kind)
	}
	if strings.TrimSpace(params.Name) == "" {
		return fmt.Errorf("knowledge: name is required")
	}
	if strings.TrimSpace(params.Content) == "" {
		return fmt.Errorf("knowledge: content is required")
	}
	if err := validateStatus(defaultStatus(params.Status)); err != nil {
		return err
	}
	return validateScopeOwner(params.Scope, params.UserID, params.AgentID)
}

func validateUpdate(params UpdateParams) error {
	if params.ID == "" {
		return fmt.Errorf("knowledge: id is required")
	}
	if strings.TrimSpace(params.Name) == "" {
		return fmt.Errorf("knowledge: name is required")
	}
	if strings.TrimSpace(params.Content) == "" {
		return fmt.Errorf("knowledge: content is required")
	}
	return validateStatus(defaultStatus(params.Status))
}

func validateStatus(status Status) error {
	if !status.Valid() {
		return fmt.Errorf("knowledge: invalid status %q", status)
	}
	return nil
}

func validateScopeOwner(scope string, userID string, agentID string) error {
	switch scope {
	case "system":
		if userID == "" && agentID == "" {
			return nil
		}
	case "system_agent":
		if userID == "" && agentID != "" {
			return nil
		}
	case "user":
		if userID != "" && agentID == "" {
			return nil
		}
	case "user_agent":
		if userID != "" && agentID != "" {
			return nil
		}
	}
	return fmt.Errorf("knowledge: invalid owner shape for scope %q", scope)
}

func defaultStatus(status Status) Status {
	if status == "" {
		return StatusDraft
	}
	return status
}

func textParam(v string) pgtype.Text {
	return pgtype.Text{String: v, Valid: v != ""}
}

func stringParam(v *string) pgtype.Text {
	if v == nil || *v == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *v, Valid: true}
}

func floatParam(v *float64) pgtype.Float8 {
	if v == nil {
		return pgtype.Float8{}
	}
	return pgtype.Float8{Float64: *v, Valid: true}
}

func timeParam(v *time.Time) pgtype.Timestamptz {
	if v == nil || v.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: v.UTC(), Valid: true}
}

func defaultJSON(raw json.RawMessage, fallback string) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(fallback)
	}
	return raw
}

func mapRows(rows []sqlc.AgentKnowledge) []Entry {
	out := make([]Entry, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapRow(row))
	}
	return out
}

func mapRow(row sqlc.AgentKnowledge) Entry {
	var confidence *float64
	if row.Confidence.Valid {
		v := row.Confidence.Float64
		confidence = &v
	}
	var expiresAt *time.Time
	if row.ExpiresAt.Valid {
		v := row.ExpiresAt.Time.UTC()
		expiresAt = &v
	}
	var supersedes *string
	if row.Supersedes.Valid {
		v := row.Supersedes.String
		supersedes = &v
	}
	return Entry{
		ID:         row.ID,
		Kind:       Kind(row.Kind),
		Scope:      row.Scope,
		UserID:     row.UserID.String,
		AgentID:    row.AgentID.String,
		Name:       row.Name,
		Content:    row.Content,
		Status:     Status(row.Status),
		Evidence:   defaultJSON(row.Evidence, "[]"),
		Confidence: confidence,
		ExpiresAt:  expiresAt,
		Supersedes: supersedes,
		Metadata:   defaultJSON(row.Metadata, "{}"),
		CreatedAt:  row.CreatedAt.UTC(),
		UpdatedAt:  row.UpdatedAt.UTC(),
	}
}

var _ Store = (*PGStore)(nil)
