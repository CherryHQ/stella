package skills

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/vaayne/anna/pkg/db/sqlc"
)

// SQLiteStore implements Store against a SQLite database via sqlc.
type SQLiteStore struct {
	db *sql.DB
	q  *sqlc.Queries
}

// New returns a new SQLiteStore. Callers may store it as a Store interface.
func New(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db, q: sqlc.New(db)}
}

// List returns all visible skills for the given context.
func (s *SQLiteStore) List(ctx context.Context, vc ViewContext) ([]Skill, error) {
	rows, err := s.q.ListSkillsVisible(ctx, sqlc.ListSkillsVisibleParams{
		AgentID: sql.NullString{String: vc.AgentID, Valid: vc.AgentID != ""},
		UserID:  sql.NullInt64{Int64: vc.UserID, Valid: vc.UserID != 0},
		Project: sql.NullString{String: vc.Project, Valid: vc.Project != ""},
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

// Resolve finds the highest-priority visible skill by name.
func (s *SQLiteStore) Resolve(ctx context.Context, name string, vc ViewContext) (*Skill, error) {
	row, err := s.q.ResolveSkill(ctx, sqlc.ResolveSkillParams{
		Name:    name,
		AgentID: sql.NullString{String: vc.AgentID, Valid: vc.AgentID != ""},
		UserID:  sql.NullInt64{Int64: vc.UserID, Valid: vc.UserID != 0},
		Project: sql.NullString{String: vc.Project, Valid: vc.Project != ""},
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("skills: resolve %q: %w", name, err)
	}
	sk := mapRow(row)
	return &sk, nil
}

// LoadFile fetches a single file by path.
func (s *SQLiteStore) LoadFile(ctx context.Context, skillID, path string) (string, error) {
	f, err := s.q.GetSkillFile(ctx, sqlc.GetSkillFileParams{
		SkillID: skillID,
		Path:    path,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("skills: file %q not found on skill %s: %w", path, skillID, sql.ErrNoRows)
	}
	if err != nil {
		return "", fmt.Errorf("skills: load file %q on skill %s: %w", path, skillID, err)
	}
	return f.Content, nil
}

// Create inserts the skill row and all its files in a transaction. The files
// map must include MainFile ("SKILL.md"). If s.ID is empty a new ID is generated.
func (s *SQLiteStore) Create(ctx context.Context, sk Skill, files map[string]string) (string, error) {
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

	disabledInt := int64(0)
	if sk.DisableModelInvocation {
		disabledInt = 1
	}

	params := sqlc.CreateSkillParams{
		ID:                     sk.ID,
		Scope:                  sk.Scope,
		Name:                   sk.Name,
		Description:            sk.Description,
		Status:                 sk.Status,
		DisableModelInvocation: disabledInt,
		Metadata:               meta,
	}

	// Set nullable owner fields based on scope.
	switch sk.Scope {
	case "user":
		params.UserID = sql.NullInt64{Int64: sk.UserID, Valid: true}
	case "agent":
		params.AgentID = sql.NullString{String: sk.AgentID, Valid: true}
	case "project":
		params.Project = sql.NullString{String: sk.Project, Valid: true}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("skills: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

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

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("skills: commit create %q: %w", sk.ID, err)
	}
	return sk.ID, nil
}

// Update patches metadata fields using read-modify-write.
func (s *SQLiteStore) Update(ctx context.Context, id string, patch UpdatePatch) error {
	row, err := s.q.GetSkill(ctx, id)
	if err != nil {
		return fmt.Errorf("skills: update get %s: %w", id, err)
	}

	description := row.Description
	if patch.Description != nil {
		description = *patch.Description
	}

	status := row.Status
	if patch.Status != nil {
		status = *patch.Status
	}

	disabled := row.DisableModelInvocation
	if patch.DisableModelInvocation != nil {
		if *patch.DisableModelInvocation {
			disabled = 1
		} else {
			disabled = 0
		}
	}

	meta := row.Metadata
	if len(patch.Metadata) > 0 {
		meta = string(patch.Metadata)
	}

	if err := s.q.UpdateSkillMetadata(ctx, sqlc.UpdateSkillMetadataParams{
		ID:                     id,
		Description:            description,
		Status:                 status,
		DisableModelInvocation: disabled,
		Metadata:               meta,
	}); err != nil {
		return fmt.Errorf("skills: update %s: %w", id, err)
	}
	return nil
}

// UpsertFile creates or replaces a single file under a skill.
func (s *SQLiteStore) UpsertFile(ctx context.Context, skillID, path, content string) error {
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
func (s *SQLiteStore) DeleteFile(ctx context.Context, skillID, path string) error {
	if err := s.q.DeleteSkillFile(ctx, sqlc.DeleteSkillFileParams{
		SkillID: skillID,
		Path:    path,
	}); err != nil {
		return fmt.Errorf("skills: delete file %q on skill %s: %w", path, skillID, err)
	}
	return nil
}

// Delete removes a skill and (via ON DELETE CASCADE) all its files.
func (s *SQLiteStore) Delete(ctx context.Context, id string) error {
	if err := s.q.DeleteSkill(ctx, id); err != nil {
		return fmt.Errorf("skills: delete %s: %w", id, err)
	}
	return nil
}

// ExpireDrafts deprecates all draft skills whose created-at metadata timestamp
// is before the given cutoff.
func (s *SQLiteStore) ExpireDrafts(ctx context.Context, before time.Time) error {
	if err := s.q.DeprecateExpiredDrafts(ctx, before.UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("skills: expire drafts: %w", err)
	}
	return nil
}

// mapRow converts a sqlc Skill to the domain Skill type.
func mapRow(r sqlc.Skill) Skill {
	createdAt, _ := time.Parse("2006-01-02 15:04:05", r.CreatedAt)
	updatedAt, _ := time.Parse("2006-01-02 15:04:05", r.UpdatedAt)

	meta := json.RawMessage("{}")
	if r.Metadata != "" {
		meta = json.RawMessage(r.Metadata)
	}

	return Skill{
		ID:                     r.ID,
		Scope:                  r.Scope,
		UserID:                 r.UserID.Int64,
		AgentID:                r.AgentID.String,
		Project:                r.Project.String,
		Name:                   r.Name,
		Description:            r.Description,
		Status:                 r.Status,
		DisableModelInvocation: r.DisableModelInvocation != 0,
		Metadata:               meta,
		CreatedAt:              createdAt,
		UpdatedAt:              updatedAt,
	}
}
