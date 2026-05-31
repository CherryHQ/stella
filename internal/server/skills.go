package server

import (
	"context"
	"database/sql"
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
	Status                 string    `json:"status"`
	DisableModelInvocation bool      `json:"disable_model_invocation"`
	Files                  []string  `json:"files"`
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
	Status                 string            `json:"status"`
	DisableModelInvocation bool              `json:"disable_model_invocation"`
	Files                  map[string]string `json:"files"`
}

type updateSkillRequest struct {
	Description            *string           `json:"description"`
	Status                 *string           `json:"status"`
	DisableModelInvocation *bool             `json:"disable_model_invocation"`
	Files                  map[string]string `json:"files"`
}

type installSkillRequest struct {
	Source  string `json:"source"`
	Scope   string `json:"scope"`
	UserID  string `json:"user_id"`
	AgentID string `json:"agent_id"`
}

// applySkillUpdate is the shared body for PATCH .../skills/{id}.
func (s *Server) applySkillUpdate(w http.ResponseWriter, r *http.Request, id string, vc skills.ViewContext) {
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
		Status:                 req.Status,
		DisableModelInvocation: req.DisableModelInvocation,
	}
	if vc.AgentID == "" && vc.UserID == "" {
		sk, err := s.findSkillByID(r.Context(), id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, "skill not found")
			} else {
				s.writeInternalError(w, err)
			}
			return
		}
		if sk.Scope == "system" {
			if systemStore, ok := store.(interface {
				UpdateSystemSkill(context.Context, string, skills.UpdatePatch) error
			}); ok {
				if err := systemStore.UpdateSystemSkill(r.Context(), id, patch); err != nil {
					s.writeInternalError(w, err)
					return
				}
				s.upsertSkillFiles(w, store, r.Context(), id, req.Files)
				return
			}
		}
		vc = skillOwnerViewContext(*sk)
	}
	if err := store.Update(r.Context(), id, vc, patch); err != nil {
		s.writeInternalError(w, err)
		return
	}
	s.upsertSkillFiles(w, store, r.Context(), id, req.Files)
}

func (s *Server) upsertSkillFiles(w http.ResponseWriter, store skills.Store, ctx context.Context, id string, files map[string]string) {
	for path, content := range files {
		if err := store.UpsertFile(ctx, id, path, content); err != nil {
			s.writeInternalError(w, err)
			return
		}
	}
	writeData(w, http.StatusOK, map[string]string{"id": id})
}

func skillOwnerViewContext(sk skills.Skill) skills.ViewContext {
	switch sk.Scope {
	case "agent":
		return skills.ViewContext{AgentID: sk.AgentID}
	case "user":
		return skills.ViewContext{UserID: sk.UserID}
	default:
		return skills.ViewContext{}
	}
}

// doDeleteSkill is the shared body for DELETE .../skills/{id}.
func (s *Server) doDeleteSkill(w http.ResponseWriter, r *http.Request, id string, vc skills.ViewContext) {
	store := s.skillStore()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "skills store not available")
		return
	}
	if vc.AgentID == "" && vc.UserID == "" {
		sk, err := s.findSkillByID(r.Context(), id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, "skill not found")
			} else {
				s.writeInternalError(w, err)
			}
			return
		}
		if sk.Scope == "system" {
			if systemStore, ok := store.(interface {
				DeleteSystemSkill(context.Context, string) error
			}); ok {
				if err := systemStore.DeleteSystemSkill(r.Context(), id); err != nil {
					s.writeInternalError(w, err)
					return
				}
				writeNoContent(w)
				return
			}
		}
		vc = skillOwnerViewContext(*sk)
	}
	if err := store.Delete(r.Context(), id, vc); err != nil {
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
