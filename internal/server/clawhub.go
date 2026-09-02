package server

import (
	"context"
	"net/http"
	"time"

	apiserver "github.com/CherryHQ/stella/api/server"
	clawhubskills "github.com/CherryHQ/stella/internal/skill"
)

// clawhubSkillView is the JSON representation of a single ClawHub marketplace skill.
type clawhubSkillView struct {
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	Summary      string `json:"summary,omitempty"`
	Version      string `json:"version,omitempty"`
	Downloads    *int   `json:"downloads,omitempty"`
	Installs     *int   `json:"installs,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
	AuthorHandle string `json:"author_handle,omitempty"`
	AuthorImage  string `json:"author_image,omitempty"`
}

// ListClawhubSkills handles GET /api/clawhub/skill.
// When q is absent it browses popular skills (paginated); when q is set it searches.
func (s *Server) ListClawhubSkills(w http.ResponseWriter, r *http.Request, params apiserver.ListClawhubSkillsParams) {
	limit := 20
	if params.PageSize != nil {
		limit = *params.PageSize
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}

	var q string
	if params.Q != nil {
		q = *params.Q
	}
	var pageToken string
	if params.PageToken != nil {
		pageToken = *params.PageToken
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	catalog, nextCursor, err := clawhubskills.BrowseCatalog(ctx, q, limit, pageToken)
	if err != nil {
		s.writeBadGatewayError(w, err)
		return
	}

	views := make([]clawhubSkillView, 0, len(catalog))
	for _, sk := range catalog {
		v := clawhubSkillView{
			Slug:         sk.Slug,
			Name:         sk.Name,
			Summary:      sk.Summary,
			Version:      sk.Version,
			Downloads:    sk.Downloads,
			Installs:     sk.Installs,
			AuthorHandle: sk.AuthorHandle,
			AuthorImage:  sk.AuthorImage,
		}
		if !sk.UpdatedAt.IsZero() {
			v.UpdatedAt = sk.UpdatedAt.UTC().Format(time.RFC3339)
		}
		views = append(views, v)
	}

	var nextPageToken *string
	if nextCursor != "" {
		nextPageToken = &nextCursor
	}

	writeData(w, http.StatusOK, map[string]any{
		"skills":          views,
		"next_page_token": nextPageToken,
	})
}

// GetClawhubSkill handles GET /api/clawhub/skills/{slug}, returning a single skill's
// metadata together with its README and file list (downloaded from ClawHub on demand).
func (s *Server) GetClawhubSkill(w http.ResponseWriter, r *http.Request, slug string) {
	if slug == "" {
		writeError(w, http.StatusNotFound, "skill not found")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	detail, err := clawhubskills.FetchCatalogDetail(ctx, slug)
	if err != nil {
		s.writeBadGatewayError(w, err)
		return
	}

	writeData(w, http.StatusOK, map[string]any{
		"slug":    detail.Slug,
		"name":    detail.Name,
		"summary": detail.Summary,
		"version": detail.Version,
		"readme":  detail.Readme,
		"files":   detail.Files,
	})
}
