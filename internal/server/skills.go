package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	mcpskills "github.com/vaayne/mcphub/pkg/skills"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/skills"
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
	CreatedBy              string    `json:"created_by"`
	Builtin                *bool     `json:"builtin,omitempty"`
	LogicalRef             string    `json:"logical_ref,omitempty"`
	Enabled                *bool     `json:"enabled,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

func (s *Server) skillStore() skills.Store {
	return s.pluginHost.SkillStore()
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
}

type installSkillRequest struct {
	Source  string `json:"source"`
	Scope   string `json:"scope"`
	UserID  string `json:"user_id"`
	AgentID string `json:"agent_id"`
}

// skillCreatedBy keeps legacy and filesystem records in the manual bucket;
// only the durable Reflect marker may opt a DB record into Reflect ownership.
func skillCreatedBy(metadata json.RawMessage) string {
	createdBy := skills.CreatedBy(skills.Skill{Metadata: metadata})
	if createdBy == skills.ReflectSkillCreatedBy {
		return createdBy
	}
	return skills.ManualSkillCreatedBy
}

func storedSkillToView(sk skills.Skill, files []string) skillView {
	if files == nil {
		files = []string{}
	}
	return skillView{
		ID: sk.ID, Scope: sk.Scope, UserID: sk.UserID, AgentID: sk.AgentID,
		Name: sk.Name, Description: sk.Description,
		DisableModelInvocation: sk.DisableModelInvocation, Files: files,
		Source: skillSource(sk.Metadata), Version: skillVersion(sk.Metadata),
		LifecycleVersion: sk.Version, CreatedBy: skillCreatedBy(sk.Metadata),
		CreatedAt: sk.CreatedAt.UTC(), UpdatedAt: sk.UpdatedAt.UTC(),
	}
}

// applySkillUpdate commits mutable DB metadata, files, and ownership together.
func (s *Server) applySkillUpdate(w http.ResponseWriter, r *http.Request, sk *skills.Skill) {
	store := s.skillStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "skills store not available")
		return
	}
	var req updateSkillRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	patch := skills.UpdatePatch{
		Description:            req.Description,
		DisableModelInvocation: req.DisableModelInvocation,
	}
	if sk.Status == "deprecated" {
		writeError(w, http.StatusConflict, "deprecated skills cannot be edited")
		return
	}
	if req.Version != nil {
		merged, err := mergeMetadataVersion(sk.Metadata, *req.Version)
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
		patch.Metadata = merged
	}
	updated, err := store.UpdateManagedSkill(r.Context(), skills.ManagedSkillUpdate{
		ID: sk.ID, UserID: sk.UserID, AgentID: sk.AgentID, Scope: sk.Scope,
		Patch: patch, Files: req.Files, ConvertToManual: req.ConvertToManual,
	})
	if errors.Is(err, skills.ErrSkillNotMutable) || errors.Is(err, skills.ErrSkillNotReflectOwned) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if errors.Is(err, skills.ErrInvalidSkillFilePath) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, committedSkillView(updated))
}

// mergeMetadataVersion overwrites just the "version" key in a skill's metadata
// JSON, preserving every other field (source, install timestamps). An empty
// version clears the key so the badge disappears.
func mergeMetadataVersion(metadata json.RawMessage, version string) (json.RawMessage, error) {
	m := map[string]any{}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &m); err != nil {
			return nil, err
		}
	}
	if version == "" {
		delete(m, "version")
	} else {
		m["version"] = version
	}
	return json.Marshal(m)
}

// doDeleteSkill is the shared body for DELETE .../skills/{id}.
func (s *Server) doDeleteSkill(w http.ResponseWriter, r *http.Request, id string) {
	store := s.skillStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "skills store not available")
		return
	}
	sk, err := s.findSkillByID(r.Context(), id)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "skill not found")
		} else {
			s.writeInternalError(w, err)
		}
		return
	}
	info := UserFromContext(r.Context())
	if info == nil || info.UserID == "" {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if sk.Scope != "user" && sk.Scope != "user_agent" && sk.Scope != "system" && sk.Scope != "system_agent" {
		// Project skills are deleted by their existing filesystem handler and do
		// not reach this DB-backed lifecycle path.
		writeError(w, http.StatusBadRequest, "skill scope is not lifecycle-managed")
		return
	}
	if err := store.Delete(r.Context(), id, skills.ViewContext{UserID: info.UserID, AgentID: sk.AgentID}); err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeNoContent(w)
}

// doDeleteSkillFile is the shared body of DELETE .../skills/{id}/file?path=...
func (s *Server) doDeleteSkillFile(w http.ResponseWriter, r *http.Request, id, path string) {
	store := s.skillStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "skills store not available")
		return
	}
	if path == "" {
		writeError(w, http.StatusBadRequest, "path query parameter is required")
		return
	}
	if path == skills.MainFile {
		writeError(w, http.StatusBadRequest, "cannot delete SKILL.md")
		return
	}
	if err := store.DeleteFile(r.Context(), id, path); err != nil {
		s.writeInternalError(w, err)
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
