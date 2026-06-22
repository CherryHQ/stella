package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/knowledge"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// PGStore implements Store against PostgreSQL via sqlc.
type PGStore struct {
	db *pgxpool.Pool
	q  *sqlc.Queries
}

// New returns a new PGStore. Callers may store it as a Store interface.
func New(db *pgxpool.Pool) *PGStore {
	return &PGStore{db: db, q: sqlc.New(db)}
}

func viewSQLParams(vc ViewContext) (pgtype.Text, pgtype.Text) {
	return pgtype.Text{String: vc.AgentID, Valid: vc.AgentID != ""}, pgtype.Text{String: vc.UserID, Valid: vc.UserID != ""}
}

// List returns all visible skills for the given context.
func (s *PGStore) List(ctx context.Context, vc ViewContext) ([]Skill, error) {
	rows, err := s.q.ListSkillsVisible(ctx, sqlc.ListSkillsVisibleParams{
		AgentID: pgtype.Text{String: vc.AgentID, Valid: vc.AgentID != ""},
		UserID:  pgtype.Text{String: vc.UserID, Valid: vc.UserID != ""},
	})
	if err != nil {
		return nil, fmt.Errorf("skills: list: %w", err)
	}
	out := make([]Skill, 0, len(rows))
	for _, r := range rows {
		out = append(out, mapRow(r))
	}
	return out, nil
}

// ListAll returns every skill regardless of status, visibility, or scope filter.
func (s *PGStore) ListAll(ctx context.Context) ([]Skill, error) {
	rows, err := s.q.ListAllSkills(ctx)
	if err != nil {
		return nil, fmt.Errorf("skills: list all: %w", err)
	}
	out := make([]Skill, 0, len(rows))
	for _, r := range rows {
		out = append(out, mapRow(r))
	}
	return out, nil
}

// ListForAgentContext returns system, agent, and current-user skills for one agent.
func (s *PGStore) ListForAgentContext(ctx context.Context, userID string, agentID string) ([]Skill, error) {
	rows, err := s.q.ListSkillsForAgentContext(ctx, sqlc.ListSkillsForAgentContextParams{
		UserID:  pgtype.Text{String: userID, Valid: userID != ""},
		AgentID: pgtype.Text{String: agentID, Valid: agentID != ""},
	})
	if err != nil {
		return nil, fmt.Errorf("skills: list for agent context: %w", err)
	}
	out := make([]Skill, 0, len(rows))
	for _, r := range rows {
		out = append(out, mapRow(r))
	}
	return out, nil
}

// ListByScope returns every skill in exactly one scope/owner bucket, including
// drafts and disabled skills, for management views.
func (s *PGStore) ListByScope(ctx context.Context, scope string, userID string, agentID string) ([]Skill, error) {
	rows, err := s.q.ListSkillsByScope(ctx, sqlc.ListSkillsByScopeParams{
		Scope:   scope,
		UserID:  pgtype.Text{String: userID, Valid: userID != ""},
		AgentID: pgtype.Text{String: agentID, Valid: agentID != ""},
	})
	if err != nil {
		return nil, fmt.Errorf("skills: list by scope %q: %w", scope, err)
	}
	out := make([]Skill, 0, len(rows))
	for _, r := range rows {
		out = append(out, mapRow(r))
	}
	return out, nil
}

// ListForAdmin returns system and agent skills, plus the admin user's own user skills.
func (s *PGStore) ListForAdmin(ctx context.Context, userID string) ([]Skill, error) {
	rows, err := s.q.ListSkillsForAdmin(ctx, pgtype.Text{String: userID, Valid: userID != ""})
	if err != nil {
		return nil, fmt.Errorf("skills: list for admin: %w", err)
	}
	out := make([]Skill, 0, len(rows))
	for _, r := range rows {
		out = append(out, mapRow(r))
	}
	return out, nil
}

// ListForUser returns skills visible to a non-admin user across accessible agents.
func (s *PGStore) ListForUser(ctx context.Context, userID string, agentIDs []string) ([]Skill, error) {
	rows, err := s.q.ListSkillsForUser(ctx, sqlc.ListSkillsForUserParams{
		UserID:      pgtype.Text{String: userID, Valid: userID != ""},
		AgentIdsCsv: pgtype.Text{String: strings.Join(agentIDs, ","), Valid: len(agentIDs) > 0},
	})
	if err != nil {
		return nil, fmt.Errorf("skills: list for user: %w", err)
	}
	out := make([]Skill, 0, len(rows))
	for _, r := range rows {
		out = append(out, mapRow(r))
	}
	return out, nil
}

// ListFiles returns all file paths for a skill (no content).
func (s *PGStore) ListFiles(ctx context.Context, skillID string) ([]string, error) {
	rows, err := s.q.ListSkillFiles(ctx, skillID)
	if err != nil {
		return nil, fmt.Errorf("skills: list files for %s: %w", skillID, err)
	}
	paths := make([]string, 0, len(rows))
	for _, r := range rows {
		paths = append(paths, r.Path)
	}
	return paths, nil
}

// ListFilesWithContent returns all files for a skill keyed by path.
func (s *PGStore) ListFilesWithContent(ctx context.Context, skillID string) (map[string]string, error) {
	rows, err := s.q.ListSkillFiles(ctx, skillID)
	if err != nil {
		return nil, fmt.Errorf("skills: list files with content for %s: %w", skillID, err)
	}
	files := make(map[string]string, len(rows))
	for _, r := range rows {
		files[r.Path] = r.Content
	}
	return files, nil
}

// Resolve finds the highest-priority visible skill by name.
func (s *PGStore) Resolve(ctx context.Context, name string, vc ViewContext) (*Skill, error) {
	row, err := s.q.ResolveSkill(ctx, sqlc.ResolveSkillParams{
		Name:    name,
		AgentID: pgtype.Text{String: vc.AgentID, Valid: vc.AgentID != ""},
		UserID:  pgtype.Text{String: vc.UserID, Valid: vc.UserID != ""},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("skills: resolve %q: %w", name, err)
	}
	sk := mapRow(row)
	return &sk, nil
}

// LoadFile fetches a single file by path.
func (s *PGStore) LoadFile(ctx context.Context, skillID, path string) (string, error) {
	f, err := s.q.GetSkillFile(ctx, sqlc.GetSkillFileParams{
		SkillID: skillID,
		Path:    path,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("skills: file %q not found on skill %s: %w", path, skillID, pgx.ErrNoRows)
	}
	if err != nil {
		return "", fmt.Errorf("skills: load file %q on skill %s: %w", path, skillID, err)
	}
	return f.Content, nil
}

// Create inserts the skill row and all its files in a transaction. The files
// map must include MainFile ("SKILL.md"). If s.ID is empty a new ID is generated.
func (s *PGStore) Create(ctx context.Context, sk Skill, files map[string]string) (string, error) {
	if _, ok := files[MainFile]; !ok {
		return "", fmt.Errorf("skills: missing SKILL.md")
	}

	if sk.ID == "" {
		sk.ID = uuid.New().String()[:8]
	}
	if sk.Status == "" {
		sk.Status = "active"
	}

	meta := "{}"
	if len(sk.Metadata) > 0 {
		meta = string(sk.Metadata)
	}

	params := sqlc.CreateSkillParams{
		ID:                     sk.ID,
		Scope:                  sk.Scope,
		Name:                   sk.Name,
		Description:            sk.Description,
		Status:                 sk.Status,
		DisableModelInvocation: sk.DisableModelInvocation,
		Metadata:               json.RawMessage(meta),
	}

	// Set nullable owner fields based on scope.
	switch sk.Scope {
	case "user":
		params.UserID = pgtype.Text{String: sk.UserID, Valid: true}
	case "user_agent":
		params.UserID = pgtype.Text{String: sk.UserID, Valid: true}
		params.AgentID = pgtype.Text{String: sk.AgentID, Valid: true}
	case "system_agent":
		params.AgentID = pgtype.Text{String: sk.AgentID, Valid: true}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("skills: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.q.WithTx(tx)

	if _, err := qtx.CreateSkill(ctx, params); err != nil {
		return "", fmt.Errorf("skills: create skill %q: %w", sk.Name, err)
	}

	for path, content := range files {
		if err := qtx.UpsertSkillFile(ctx, sqlc.UpsertSkillFileParams{
			SkillID: sk.ID,
			Path:    path,
			Content: content,
		}); err != nil {
			return "", fmt.Errorf("skills: insert file %q for skill %q: %w", path, sk.ID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("skills: commit create %q: %w", sk.ID, err)
	}
	return sk.ID, nil
}

type resolvedPatch struct {
	Description            string
	Status                 string
	DisableModelInvocation bool
	Metadata               json.RawMessage
}

func applyPatch(row sqlc.Skill, patch UpdatePatch) resolvedPatch {
	r := resolvedPatch{
		Description:            row.Description,
		Status:                 row.Status,
		DisableModelInvocation: row.DisableModelInvocation,
		Metadata:               row.Metadata,
	}
	if patch.Description != nil {
		r.Description = *patch.Description
	}
	if patch.Status != nil {
		r.Status = *patch.Status
	}
	if patch.DisableModelInvocation != nil {
		r.DisableModelInvocation = *patch.DisableModelInvocation
	}
	if len(patch.Metadata) > 0 {
		r.Metadata = patch.Metadata
	}
	return r
}

// Update patches metadata fields using read-modify-write.
func (s *PGStore) Update(ctx context.Context, id string, vc ViewContext, patch UpdatePatch) error {
	agentID, userID := viewSQLParams(vc)
	row, err := s.q.GetSkill(ctx, sqlc.GetSkillParams{ID: id, AgentID: agentID, UserID: userID})
	if err != nil {
		return fmt.Errorf("skills: update get %s: %w", id, err)
	}
	p := applyPatch(row, patch)
	if err := s.q.UpdateSkillMetadata(ctx, sqlc.UpdateSkillMetadataParams{
		ID:                     id,
		AgentID:                agentID,
		UserID:                 userID,
		Description:            p.Description,
		Status:                 p.Status,
		DisableModelInvocation: p.DisableModelInvocation,
		Metadata:               p.Metadata,
	}); err != nil {
		return fmt.Errorf("skills: update %s: %w", id, err)
	}
	return nil
}

// UpsertFile creates or replaces a single file under a skill.
func (s *PGStore) UpsertFile(ctx context.Context, skillID, path, content string) error {
	if err := s.q.UpsertSkillFile(ctx, sqlc.UpsertSkillFileParams{
		SkillID: skillID,
		Path:    path,
		Content: content,
	}); err != nil {
		return fmt.Errorf("skills: upsert file %q on skill %s: %w", path, skillID, err)
	}
	return nil
}

// DeleteFile removes a single file from a skill.
func (s *PGStore) DeleteFile(ctx context.Context, skillID, path string) error {
	if err := s.q.DeleteSkillFile(ctx, sqlc.DeleteSkillFileParams{
		SkillID: skillID,
		Path:    path,
	}); err != nil {
		return fmt.Errorf("skills: delete file %q on skill %s: %w", path, skillID, err)
	}
	return nil
}

// Delete removes a skill and (via ON DELETE CASCADE) all its files.
func (s *PGStore) Delete(ctx context.Context, id string, vc ViewContext) error {
	agentID, userID := viewSQLParams(vc)
	if err := s.q.DeleteSkill(ctx, sqlc.DeleteSkillParams{ID: id, AgentID: agentID, UserID: userID}); err != nil {
		return fmt.Errorf("skills: delete %s: %w", id, err)
	}
	return nil
}

func (s *PGStore) UpdateSystemSkill(ctx context.Context, id string, patch UpdatePatch) error {
	row, err := s.q.GetSkill(ctx, sqlc.GetSkillParams{ID: id})
	if err != nil {
		return fmt.Errorf("skills: system update get %s: %w", id, err)
	}
	if row.Scope != "system" {
		return fmt.Errorf("skills: %s is not a system skill", id)
	}
	p := applyPatch(row, patch)
	if err := s.q.UpdateSystemSkillMetadata(ctx, sqlc.UpdateSystemSkillMetadataParams{
		ID:                     id,
		Description:            p.Description,
		Status:                 p.Status,
		DisableModelInvocation: p.DisableModelInvocation,
		Metadata:               p.Metadata,
	}); err != nil {
		return fmt.Errorf("skills: system update %s: %w", id, err)
	}
	return nil
}

func (s *PGStore) DeleteSystemSkill(ctx context.Context, id string) error {
	if err := s.q.DeleteSystemSkill(ctx, id); err != nil {
		return fmt.Errorf("skills: system delete %s: %w", id, err)
	}
	return nil
}

// ExpireDrafts deprecates all draft skills (disable_model_invocation=0) whose
// created-at metadata timestamp is before the given cutoff.
func (s *PGStore) ExpireDrafts(ctx context.Context, before time.Time) error {
	if err := s.q.DeprecateExpiredDrafts(ctx, before.UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("skills: expire drafts: %w", err)
	}
	return nil
}

// ListKnowledge returns active first-class knowledge entries for the given view context.
// Pass knowledge types to filter; no types means all.
func (s *PGStore) ListKnowledge(ctx context.Context, vc ViewContext, types ...KnowledgeType) ([]KnowledgeEntry, error) {
	kinds := make([]knowledge.Kind, 0, len(types))
	for _, t := range types {
		kinds = append(kinds, knowledge.Kind(t))
	}
	rows, err := knowledge.New(s.db).ListActive(ctx, knowledge.ViewContext{
		UserID:  vc.UserID,
		AgentID: vc.AgentID,
	}, kinds...)
	if err != nil {
		return nil, fmt.Errorf("skills: list knowledge: %w", err)
	}
	entries := make([]KnowledgeEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, knowledgeEntryFromDomain(row))
	}
	return entries, nil
}

// CreateKnowledge creates a first-class fact/context entry without creating a skill row.
func (s *PGStore) CreateKnowledge(ctx context.Context, params KnowledgeCreateParams) (KnowledgeEntry, error) {
	meta := metadataWithDescription(params.Metadata, params.Description)
	row, err := knowledge.New(s.db).Create(ctx, knowledge.CreateParams{
		Kind:       knowledge.Kind(params.KnowledgeType),
		Scope:      params.Scope,
		UserID:     params.UserID,
		AgentID:    params.AgentID,
		Name:       params.Name,
		Content:    params.Content,
		Status:     knowledge.Status(params.Status),
		Evidence:   params.Evidence,
		Confidence: params.Confidence,
		ExpiresAt:  params.ExpiresAt,
		Supersedes: params.Supersedes,
		Metadata:   meta,
	})
	if err != nil {
		return KnowledgeEntry{}, fmt.Errorf("skills: create knowledge: %w", err)
	}
	return knowledgeEntryFromDomain(row), nil
}

// ListKnowledgeByNameAndScope returns exact writable-scope matches for tool patch/deprecate.
func (s *PGStore) ListKnowledgeByNameAndScope(ctx context.Context, name string, scope string, userID string, agentID string) ([]KnowledgeEntry, error) {
	rows, err := knowledge.New(s.db).ListByNameAndScope(ctx, name, scope, userID, agentID)
	if err != nil {
		return nil, fmt.Errorf("skills: list knowledge by name: %w", err)
	}
	out := make([]KnowledgeEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, knowledgeEntryFromDomain(row))
	}
	return out, nil
}

// UpdateKnowledge replaces mutable fields on an existing knowledge entry.
func (s *PGStore) UpdateKnowledge(ctx context.Context, params KnowledgeUpdateParams) (KnowledgeEntry, error) {
	meta := metadataWithDescription(params.Metadata, params.Description)
	row, err := knowledge.New(s.db).Update(ctx, knowledge.UpdateParams{
		ID:         params.ID,
		Name:       params.Name,
		Content:    params.Content,
		Status:     knowledge.Status(params.Status),
		Evidence:   params.Evidence,
		Confidence: params.Confidence,
		ExpiresAt:  params.ExpiresAt,
		Supersedes: params.Supersedes,
		Metadata:   meta,
	})
	if err != nil {
		return KnowledgeEntry{}, fmt.Errorf("skills: update knowledge: %w", err)
	}
	return knowledgeEntryFromDomain(row), nil
}

// DeprecateKnowledge marks a knowledge entry deprecated.
func (s *PGStore) DeprecateKnowledge(ctx context.Context, id string) error {
	if err := knowledge.New(s.db).Deprecate(ctx, id); err != nil {
		return fmt.Errorf("skills: deprecate knowledge: %w", err)
	}
	return nil
}

// ExpireKnowledgeDraftsByType deprecates draft knowledge entries of the given type
// whose created-at timestamp is before the cutoff.
func (s *PGStore) ExpireKnowledgeDraftsByType(ctx context.Context, knowledgeType KnowledgeType, before time.Time) error {
	if err := knowledge.New(s.db).ExpireDraftsByType(ctx, knowledge.Kind(knowledgeType), before); err != nil {
		return fmt.Errorf("skills: expire knowledge drafts by type %q: %w", knowledgeType, err)
	}
	return nil
}

func knowledgeEntryFromDomain(row knowledge.Entry) KnowledgeEntry {
	return KnowledgeEntry{
		ID:            row.ID,
		Name:          row.Name,
		Description:   descriptionFromMetadata(row.Metadata),
		Content:       row.Content,
		KnowledgeType: KnowledgeType(row.Kind),
		Status:        string(row.Status),
		Evidence:      row.Evidence,
		Confidence:    row.Confidence,
		ExpiresAt:     row.ExpiresAt,
		Supersedes:    row.Supersedes,
		Metadata:      row.Metadata,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
}

func metadataWithDescription(raw json.RawMessage, description string) json.RawMessage {
	meta := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &meta)
	}
	if description != "" {
		meta["description"] = description
	}
	if len(meta) == 0 {
		return json.RawMessage("{}")
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return json.RawMessage("{}")
	}
	return encoded
}

func descriptionFromMetadata(raw json.RawMessage) string {
	var meta map[string]any
	if json.Unmarshal(raw, &meta) != nil {
		return ""
	}
	if desc, _ := meta["description"].(string); desc != "" {
		return desc
	}
	desc, _ := meta["legacy_description"].(string)
	return desc
}

// Compile-time assertions.
var (
	_ Store          = (*PGStore)(nil)
	_ KnowledgeStore = (*PGStore)(nil)
)

// mapRow converts a sqlc Skill to the domain Skill type.
func mapRow(r sqlc.Skill) Skill {
	meta := json.RawMessage("{}")
	if len(r.Metadata) != 0 {
		meta = r.Metadata
	}

	return Skill{
		ID:                     r.ID,
		Scope:                  r.Scope,
		UserID:                 r.UserID.String,
		AgentID:                r.AgentID.String,
		Name:                   r.Name,
		Description:            r.Description,
		Status:                 r.Status,
		DisableModelInvocation: r.DisableModelInvocation,
		Metadata:               meta,
		CreatedAt:              r.CreatedAt.UTC(),
		UpdatedAt:              r.UpdatedAt.UTC(),
	}
}
