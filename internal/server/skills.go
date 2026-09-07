package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	mcpskills "github.com/vaayne/mcphub/pkg/skills"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/skill"
)

// skillView is the JSON representation of a skill returned by the API.
type skillView struct {
	ID                     string    `json:"id"`
	Scope                  string    `json:"scope"`
	UserID                 string    `json:"user_id,omitempty"`
	AgentID                string    `json:"agent_id,omitempty"`
	Name                   string    `json:"name"`
	Description            string    `json:"description"`
	DisableModelInvocation bool      `json:"disable_model_invocation"`
	Files                  []string  `json:"files"`
	Source                 string    `json:"source,omitempty"`
	Version                string    `json:"version,omitempty"`
	LifecycleVersion       int64     `json:"lifecycle_version"`
	ContentDigest          string    `json:"content_digest,omitempty"`
	CreatedBy              string    `json:"created_by"`
	Builtin                *bool     `json:"builtin,omitempty"`
	LogicalRef             string    `json:"logical_ref,omitempty"`
	Enabled                *bool     `json:"enabled,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

type createSkillRequest struct {
	Scope                  string            `json:"scope"`
	UserID                 string            `json:"user_id"`
	AgentID                string            `json:"agent_id"`
	Name                   string            `json:"name"`
	Description            string            `json:"description"`
	DisableModelInvocation bool              `json:"disable_model_invocation"`
	Files                  map[string]string `json:"files"`
}

type updateSkillRequest struct {
	Description            *string           `json:"description"`
	DisableModelInvocation *bool             `json:"disable_model_invocation"`
	Version                *string           `json:"version"`
	ConvertToManual        bool              `json:"convert_to_manual"`
	Files                  map[string]string `json:"files"`
	ExpectedDigest         string            `json:"expected_digest"`
}

type installSkillRequest struct {
	Source  string `json:"source"`
	Scope   string `json:"scope"`
	UserID  string `json:"user_id"`
	AgentID string `json:"agent_id"`
}

// skillCreatedBy keeps unmarked and immutable records in the manual bucket;
// only the durable Reflect marker may opt a managed record into Reflect ownership.
func skillCreatedBy(metadata json.RawMessage) string {
	createdBy := skill.CreatedBy(skill.Skill{Metadata: metadata})
	if createdBy == skill.ReflectSkillCreatedBy {
		return createdBy
	}
	return skill.ManualSkillCreatedBy
}

func storedSkillToView(sk skill.Skill, files []string) skillView {
	if files == nil {
		files = []string{}
	}
	return skillView{
		ID: sk.ID, Scope: sk.Scope, UserID: sk.UserID, AgentID: sk.AgentID,
		Name: sk.Name, Description: sk.Description,
		DisableModelInvocation: sk.DisableModelInvocation, Files: files,
		Source: skillSource(sk.Metadata), Version: skillVersion(sk.Metadata),
		LifecycleVersion: sk.Version, ContentDigest: sk.ContentDigest,
		CreatedBy: skillCreatedBy(sk.Metadata),
		CreatedAt: sk.CreatedAt.UTC(), UpdatedAt: sk.UpdatedAt.UTC(),
	}
}

// applySkillUpdate commits mutable DB metadata, files, and ownership together.
func (s *Server) applySkillUpdate(w http.ResponseWriter, r *http.Request, authority authz.Authority, id string) {
	var req updateSkillRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	patch := skill.UpdatePatch{
		Description:            req.Description,
		DisableModelInvocation: req.DisableModelInvocation,
	}
	updated, err := s.skillManagement.Update(r.Context(), authority, skill.ManagedUpdate{
		ID: id, Patch: patch, Version: req.Version, Files: req.Files,
		ConvertToManual: req.ConvertToManual, ExpectedVersion: req.ExpectedDigest,
	})
	if errors.Is(err, skill.ErrInvalidSkillFilePath) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		s.writeSkillMutationError(w, err)
		return
	}
	writeData(w, http.StatusOK, committedSkillView(updated))
}

func (s *Server) writeSkillMutationError(w http.ResponseWriter, err error) {
	if code, msg := skillAccessError(err); code != http.StatusInternalServerError {
		writeError(w, code, msg)
		return
	}
	switch {
	case errors.Is(err, skill.ErrSkillDigestRequired):
		writeError(w, http.StatusBadRequest, "expected_digest is required")
	case errors.Is(err, skill.ErrSkillDigestConflict), errors.Is(err, skill.ErrSkillNotMutable), errors.Is(err, skill.ErrSkillNotReflectOwned):
		writeError(w, http.StatusConflict, err.Error())
	default:
		s.writeInternalError(w, err)
	}
}

// doDeleteSkill is the shared body for DELETE .../skills/{id}.
func (s *Server) doDeleteSkill(w http.ResponseWriter, r *http.Request, authority authz.Authority, id, expectedDigest string) {
	if err := s.skillManagement.Delete(r.Context(), authority, id, expectedDigest); err != nil {
		s.writeSkillMutationError(w, err)
		return
	}
	writeNoContent(w)
}

// doDeleteSkillFile is the shared body of DELETE .../skills/{id}/file?path=...
func (s *Server) doDeleteSkillFile(w http.ResponseWriter, r *http.Request, authority authz.Authority, id, path, expectedDigest string) {
	if path == "" {
		writeError(w, http.StatusBadRequest, "path query parameter is required")
		return
	}
	if path == skill.MainFile {
		writeError(w, http.StatusBadRequest, "cannot delete SKILL.md")
		return
	}
	if _, err := s.skillManagement.DeleteFile(r.Context(), authority, id, path, expectedDigest); err != nil {
		s.writeSkillMutationError(w, err)
		return
	}
	writeNoContent(w)
}

// SearchSkills handles GET /api/skills/search?q=<query>&limit=<n>.
func (s *Server) SearchSkills(w http.ResponseWriter, r *http.Request, params apiserver.SearchSkillsParams) {
	q := params.Q
	limit := 10
	if params.Limit != nil && *params.Limit > 0 {
		limit = *params.Limit
	}
	if limit > 50 {
		limit = 50
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	results, err := mcpskills.Search(ctx, q, limit)
	if err != nil {
		s.writeBadGatewayError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"skills": results})
}
