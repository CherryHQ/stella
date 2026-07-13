package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func (s *Server) skillService() *skills.Service {
	return skills.NewService(pluginhost.NewSkillStoreAdapter(s.skillStore()), config.StellaHome())
}

// findSkillByID linear-scans ListAll. The store has no Get(ctx, id) yet —
// see handoff.md "Blockers/Gotchas". Fine at current volumes.
func (s *Server) findSkillByID(ctx context.Context, id string) (*skills.Skill, error) {
	rows, err := s.skillStore().ListAll(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].ID == id {
			return &rows[i], nil
		}
	}
	return nil, pgx.ErrNoRows
}

func (s *Server) requireAgentAccess(ctx context.Context, agentID string) (config.Agent, int, string) {
	info := UserFromContext(ctx)
	if info == nil {
		return config.Agent{}, http.StatusUnauthorized, "unauthorized"
	}
	a, err := s.store.GetAgent(ctx, agentID)
	if err != nil {
		if isNotFound(err) {
			return config.Agent{}, http.StatusNotFound, "agent not found"
		}
		s.log.Error("get agent", "agent_id", agentID, "error", err)
		return config.Agent{}, http.StatusInternalServerError, "internal error"
	}
	if !info.IsAdmin && !s.canAccessAgent(ctx, info, a) {
		return config.Agent{}, http.StatusForbidden, "forbidden"
	}
	return a, 0, ""
}

// requireAgentManage verifies the caller is an admin or the agent creator.
func (s *Server) requireAgentManage(ctx context.Context, agentID string) (config.Agent, int, string) {
	info := UserFromContext(ctx)
	if info == nil {
		return config.Agent{}, http.StatusUnauthorized, "unauthorized"
	}
	a, code, msg := s.requireAgentAccess(ctx, agentID)
	if code != 0 {
		return config.Agent{}, code, msg
	}
	if !info.IsAdmin && a.CreatorID != info.UserID {
		return config.Agent{}, http.StatusForbidden, "forbidden"
	}
	return a, 0, ""
}

// requireSkillScope checks that skill matches the expected scope and owner.
func (s *Server) requireSkillScope(ctx context.Context, id, scope string, userID string, agentID string) (*skills.Skill, int, string) {
	sk, err := s.findSkillByID(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return nil, http.StatusNotFound, "skill not found"
		}
		s.log.Error("find skill", "skill_id", id, "error", err)
		return nil, http.StatusInternalServerError, "internal error"
	}
	if sk.Scope != scope {
		return nil, http.StatusNotFound, "skill not found"
	}
	switch scope {
	case "system_agent":
		if sk.AgentID != agentID {
			return nil, http.StatusNotFound, "skill not found"
		}
	case "user":
		if sk.UserID != userID {
			return nil, http.StatusNotFound, "skill not found"
		}
	case "user_agent":
		if sk.UserID != userID || sk.AgentID != agentID {
			return nil, http.StatusNotFound, "skill not found"
		}
	case "system":
		// no owner check
	}
	return sk, 0, ""
}

// ---- helpers ----------------------------------------------------------------

func (s *Server) projectRootForSession(ctx context.Context, agentID string, sessionID *string) (string, error) {
	if sessionID == nil || *sessionID == "" {
		return "", nil
	}
	sm, ok := s.mem.(memory.SessionManager)
	if !ok {
		return "", nil
	}
	si, err := sm.LoadInfo(ctx, *sessionID)
	if isNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if si.AgentID != agentID || si.ProjectID == "" {
		return "", nil
	}
	p, err := s.q.GetProject(ctx, sqlc.GetProjectParams{ID: si.ProjectID, UserID: si.UserID})
	if isNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return p.BaseDir, nil
}

func dbSkillsToPluginSkills(rows []skills.Skill) []pkgplugins.Skill {
	out := make([]pkgplugins.Skill, len(rows))
	for i, r := range rows {
		out[i] = dbSkillToPluginSkill(r)
	}
	return out
}

func dbSkillToPluginSkill(sk skills.Skill) pkgplugins.Skill {
	return pkgplugins.Skill{
		ID: sk.ID, Scope: sk.Scope, UserID: sk.UserID, AgentID: sk.AgentID,
		Name: sk.Name, Description: sk.Description, Status: sk.Status,
		DisableModelInvocation: sk.DisableModelInvocation, Metadata: sk.Metadata,
		CreatedAt: sk.CreatedAt, UpdatedAt: sk.UpdatedAt, Version: sk.Version,
	}
}

func resolvedSkillToView(rs skills.ResolvedSkill) skillView {
	var files []string
	if rs.Dir != "" {
		files, _ = skills.ListDirFiles(rs.Dir)
	}
	if files == nil {
		files = []string{}
	}
	return skillView{
		ID:                     rs.ID,
		Scope:                  rs.Scope,
		UserID:                 rs.UserID,
		AgentID:                rs.AgentID,
		Name:                   rs.Name,
		Description:            rs.Description,
		Status:                 rs.Status,
		DisableModelInvocation: rs.DisableModelInvocation,
		Files:                  files,
		Source:                 skillSource(rs.Metadata),
		Version:                skillVersion(rs.Metadata),
		LifecycleVersion:       rs.Version,
		CreatedBy:              skillCreatedBy(rs.Metadata),
		CreatedAt:              rs.CreatedAt.UTC(),
		UpdatedAt:              rs.UpdatedAt.UTC(),
	}
}

// skillSource extracts the install source recorded in a skill's metadata, if any.
func skillSource(metadata json.RawMessage) string {
	return skillMeta(metadata).Source
}

// skillVersion extracts the installed version recorded in a skill's metadata
// (git ref/commit or clawhub version), if any.
func skillVersion(metadata json.RawMessage) string {
	return skillMeta(metadata).Version
}

func skillMeta(metadata json.RawMessage) struct {
	Source  string `json:"source"`
	Version string `json:"version"`
} {
	var m struct {
		Source  string `json:"source"`
		Version string `json:"version"`
	}
	if len(metadata) > 0 {
		_ = json.Unmarshal(metadata, &m)
	}
	return m
}

// defaultAgentSkillScope picks the write scope when the client omits one:
// admins manage the shared system_agent scope, everyone else writes their own
// per-agent (user_agent) skills. This keeps the legacy install/upload entry
// points from defaulting non-admins into the admin-only system_agent scope.
func defaultAgentSkillScope(ctx context.Context) string {
	if info := UserFromContext(ctx); info != nil && info.IsAdmin {
		return "system_agent"
	}
	return "user_agent"
}

// requireAgentSkillWrite checks auth for write operations on DB-backed scopes.
func (s *Server) requireAgentSkillWrite(ctx context.Context, agentID, scope string) (string, skills.ViewContext, int, string) {
	info := UserFromContext(ctx)
	if info == nil {
		return "", skills.ViewContext{}, http.StatusUnauthorized, "unauthorized"
	}
	switch scope {
	case "system_agent":
		// system_agent is an admin-managed scope shared with everyone who uses
		// the agent; being the agent's creator is not enough.
		if !info.IsAdmin {
			return "", skills.ViewContext{}, http.StatusForbidden, "system agent skills are managed by admins"
		}
		if _, code, msg := s.requireAgentManage(ctx, agentID); code != 0 {
			return "", skills.ViewContext{}, code, msg
		}
		return info.UserID, skills.ViewContext{AgentID: agentID}, 0, ""
	case "user":
		if _, code, msg := s.requireAgentAccess(ctx, agentID); code != 0 {
			return "", skills.ViewContext{}, code, msg
		}
		return info.UserID, skills.ViewContext{UserID: info.UserID}, 0, ""
	case "user_agent":
		if _, code, msg := s.requireAgentAccess(ctx, agentID); code != 0 {
			return "", skills.ViewContext{}, code, msg
		}
		return info.UserID, skills.ViewContext{UserID: info.UserID, AgentID: agentID}, 0, ""
	case "project":
		return "", skills.ViewContext{}, http.StatusBadRequest, "project skills are managed via the CLI or filesystem"
	case "system":
		return "", skills.ViewContext{}, http.StatusForbidden, "system skills are managed in Settings → Skills"
	default:
		return "", skills.ViewContext{}, http.StatusBadRequest, "scope must be one of: user, user_agent, system_agent"
	}
}

func safeSkillFilePath(skillDir, filePath string) (string, error) {
	clean := filepath.Clean(filePath)
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fs.ErrPermission
	}
	return filepath.Join(skillDir, clean), nil
}

// resolveSkillAny finds a skill by name across all scopes (highest priority wins).
func (s *Server) resolveSkillAny(ctx context.Context, agentID, skillName string, sessionID *string) (*skills.ResolvedSkill, string, int, string) {
	info := UserFromContext(ctx)
	if info == nil {
		return nil, "", http.StatusUnauthorized, "unauthorized"
	}
	if _, code, msg := s.requireAgentAccess(ctx, agentID); code != 0 {
		return nil, "", code, msg
	}
	projectRoot, _ := s.projectRootForSession(memoryContext2(ctx), agentID, sessionID)
	vc := pkgplugins.SkillViewContext{UserID: info.UserID, AgentID: agentID}
	rs, err := s.skillService().Resolve(ctx, skillName, vc, projectRoot)
	if err != nil {
		s.log.Error("resolve skill", "agent_id", agentID, "skill", skillName, "error", err)
		return nil, "", http.StatusInternalServerError, "internal error"
	}
	if rs == nil {
		return nil, "", http.StatusNotFound, "skill not found"
	}
	return rs, projectRoot, 0, ""
}

// resolveSkill finds a skill by name in a specific scope for the given agent.
func (s *Server) resolveSkill(ctx context.Context, agentID, skillName, scope string, sessionID *string) (*skills.ResolvedSkill, string, int, string) {
	info := UserFromContext(ctx)
	if info == nil {
		return nil, "", http.StatusUnauthorized, "unauthorized"
	}
	if _, code, msg := s.requireAgentAccess(ctx, agentID); code != 0 {
		return nil, "", code, msg
	}
	projectRoot, _ := s.projectRootForSession(memoryContext2(ctx), agentID, sessionID)
	vc := pkgplugins.SkillViewContext{UserID: info.UserID, AgentID: agentID}
	rs, err := s.skillService().ResolveScoped(ctx, skillName, scope, vc, projectRoot)
	if err != nil {
		s.log.Error("resolve scoped skill", "agent_id", agentID, "skill", skillName, "scope", scope, "error", err)
		return nil, "", http.StatusInternalServerError, "internal error"
	}
	if rs == nil {
		return nil, "", http.StatusNotFound, "skill not found"
	}
	return rs, projectRoot, 0, ""
}

// resolveAgentSkillReference treats a retained DB ID as authoritative, then
// falls back to the legacy active-name resolver for filesystem and old clients.
func (s *Server) resolveAgentSkillReference(ctx context.Context, agentID, ref, scope string, exactScope bool, sessionID *string) (*skills.ResolvedSkill, string, int, string) {
	info := UserFromContext(ctx)
	if info == nil {
		return nil, "", http.StatusUnauthorized, "unauthorized"
	}
	if _, code, msg := s.requireAgentAccess(ctx, agentID); code != 0 {
		return nil, "", code, msg
	}

	sk, err := s.findSkillByID(ctx, ref)
	if err == nil {
		wantedScope := sk.Scope
		if exactScope {
			wantedScope = scope
		}
		owned, code, msg := s.requireSkillScope(ctx, sk.ID, wantedScope, info.UserID, agentID)
		if code != 0 {
			return nil, "", code, msg
		}
		if owned.Status == "deprecated" && owned.Scope == "system_agent" && !info.IsAdmin {
			return nil, "", http.StatusForbidden, "system agent skills are managed by admins"
		}
		return &skills.ResolvedSkill{Skill: dbSkillToPluginSkill(*owned)}, "", 0, ""
	}
	if !isNotFound(err) {
		s.log.Error("find skill by stable id", "agent_id", agentID, "skill", ref, "error", err)
		return nil, "", http.StatusInternalServerError, "internal error"
	}

	var rs *skills.ResolvedSkill
	var projectRoot string
	var code int
	var msg string
	if exactScope {
		rs, projectRoot, code, msg = s.resolveSkill(ctx, agentID, ref, scope, sessionID)
	} else {
		rs, projectRoot, code, msg = s.resolveSkillAny(ctx, agentID, ref, sessionID)
	}
	if code == 0 && rs.Status == "deprecated" {
		return nil, "", http.StatusNotFound, "skill not found"
	}
	return rs, projectRoot, code, msg
}

// memoryContext2 builds memory context from just ctx (no *http.Request).
func memoryContext2(ctx context.Context) context.Context {
	return ctx
}

// loadSkillFile loads a file from an already-resolved skill.
func (s *Server) loadSkillFile(ctx context.Context, rs *skills.ResolvedSkill, path string) (string, error) {
	if rs.Dir != "" {
		fp, err := safeSkillFilePath(rs.Dir, path)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(fp)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	store := s.skillStore()
	if store == nil {
		return "", errors.New("skills store not available")
	}
	return store.LoadFile(ctx, rs.ID, path)
}

// ---- Agent skills: /api/agents/{id}/skills* ---------------------------------

func (s *Server) ListAgentSkills(w http.ResponseWriter, r *http.Request, id string, params apiserver.ListAgentSkillsParams) {
	agentID := id
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), agentID); code != 0 {
		writeError(w, code, msg)
		return
	}
	if params.Scope != nil && params.ScopeGroup != nil {
		writeError(w, http.StatusBadRequest, "scope and scope_group are mutually exclusive")
		return
	}
	if params.State != nil && !params.State.Valid() {
		writeError(w, http.StatusBadRequest, "state must be active or removed")
		return
	}
	if params.Scope != nil && !params.Scope.Valid() {
		writeError(w, http.StatusBadRequest, "invalid scope")
		return
	}
	if params.ScopeGroup != nil && !params.ScopeGroup.Valid() {
		writeError(w, http.StatusBadRequest, "invalid scope_group")
		return
	}
	if params.CreatedBy != nil && !params.CreatedBy.Valid() {
		writeError(w, http.StatusBadRequest, "invalid created_by")
		return
	}

	state := skills.ManagedSkillStateActive
	if params.State != nil {
		state = skills.ManagedSkillState(*params.State)
	}
	pageSize := defaultPageSize
	if params.PageSize != nil {
		pageSize = *params.PageSize
	}
	if pageSize < 1 || pageSize > 100 {
		writeError(w, http.StatusBadRequest, "page_size must be between 1 and 100")
		return
	}
	var cursor *skills.ManagedSkillCursor
	if params.PageToken != nil {
		var err error
		cursor, err = decodeSkillPageToken(*params.PageToken, state)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	query := ""
	if params.Q != nil {
		query = strings.TrimSpace(*params.Q)
	}
	createdBy := ""
	if params.CreatedBy != nil {
		createdBy = string(*params.CreatedBy)
	}
	if state == skills.ManagedSkillStateRemoved {
		s.listRemovedAgentSkills(w, r, agentID, info, params, query, createdBy, pageSize, cursor)
		return
	}

	projectRoot, err := s.projectRootForSession(memoryContext(r, agentID), agentID, params.SessionId)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	dbSkills, err := s.skillStore().ListForAgentContext(r.Context(), info.UserID, agentID)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	merged := s.skillService().ListMergedWithDB(dbSkillsToPluginSkills(dbSkills), projectRoot)
	filtered := make([]skills.ResolvedSkill, 0, len(merged))
	queryLower := strings.ToLower(query)
	for _, rs := range merged {
		if queryLower != "" && !strings.Contains(strings.ToLower(rs.Name), queryLower) && !strings.Contains(strings.ToLower(rs.Description), queryLower) {
			continue
		}
		if createdBy != "" && skillCreatedBy(rs.Metadata) != createdBy {
			continue
		}
		filtered = append(filtered, rs)
	}

	counts := agentSkillScopeCounts(filtered)
	selected := make([]skills.ResolvedSkill, 0, len(filtered))
	for _, rs := range filtered {
		if agentSkillScopeSelected(rs.Scope, params) {
			selected = append(selected, rs)
		}
	}
	total := len(selected)
	legacyFullList := params.State == nil && params.ScopeGroup == nil && params.CreatedBy == nil && params.Q == nil && params.PageSize == nil && params.PageToken == nil
	if !legacyFullList {
		sort.SliceStable(selected, func(i, j int) bool {
			if selected[i].UpdatedAt.Equal(selected[j].UpdatedAt) {
				return selected[i].ID > selected[j].ID
			}
			return selected[i].UpdatedAt.After(selected[j].UpdatedAt)
		})
		if cursor != nil {
			position := 0
			for position < len(selected) && !skillFollowsCursor(selected[position], *cursor) {
				position++
			}
			selected = selected[position:]
		}
	}

	hasMore := !legacyFullList && len(selected) > pageSize
	if hasMore {
		selected = selected[:pageSize]
	}
	out := make([]skillView, len(selected))
	for i := range selected {
		out[i] = resolvedSkillToView(selected[i])
	}
	response := map[string]any{
		"skills": out, "total_size": total, "scope_counts": counts, "next_page_token": nil,
	}
	if hasMore {
		last := selected[len(selected)-1]
		token, err := encodeSkillPageToken(state, skills.ManagedSkillCursor{Timestamp: last.UpdatedAt, ID: last.ID})
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
		response["next_page_token"] = token
	}
	writeData(w, http.StatusOK, response)
}

func agentSkillScopeGroup(scope string) string {
	switch scope {
	case "system":
		return "system"
	case "system_agent", "user_agent":
		return "agent"
	case "user":
		return "user"
	case "project":
		return "project"
	default:
		return ""
	}
}

func agentSkillScopeSelected(scope string, params apiserver.ListAgentSkillsParams) bool {
	if params.Scope != nil {
		return scope == string(*params.Scope)
	}
	if params.ScopeGroup != nil {
		return agentSkillScopeGroup(scope) == string(*params.ScopeGroup)
	}
	return true
}

func agentSkillScopeCounts(items []skills.ResolvedSkill) map[string]int {
	counts := map[string]int{"all": len(items), "system": 0, "agent": 0, "user": 0, "project": 0}
	for i := range items {
		if group := agentSkillScopeGroup(items[i].Scope); group != "" {
			counts[group]++
		}
	}
	return counts
}

func skillFollowsCursor(sk skills.ResolvedSkill, cursor skills.ManagedSkillCursor) bool {
	return sk.UpdatedAt.Before(cursor.Timestamp) || (sk.UpdatedAt.Equal(cursor.Timestamp) && sk.ID < cursor.ID)
}

func (s *Server) listRemovedAgentSkills(w http.ResponseWriter, r *http.Request, agentID string, info *AuthInfo, params apiserver.ListAgentSkillsParams, query, createdBy string, pageSize int, cursor *skills.ManagedSkillCursor) {
	allowed := []string{"user", "user_agent"}
	if info.IsAdmin {
		allowed = append(allowed, "system_agent")
	}
	if params.Scope != nil && string(*params.Scope) == "system_agent" && !info.IsAdmin {
		writeError(w, http.StatusForbidden, "system agent skills are managed by admins")
		return
	}

	selectedScopes := allowed
	if params.Scope != nil {
		switch string(*params.Scope) {
		case "user", "user_agent", "system_agent":
			selectedScopes = []string{string(*params.Scope)}
		default:
			selectedScopes = nil
		}
	} else if params.ScopeGroup != nil {
		switch string(*params.ScopeGroup) {
		case "user":
			selectedScopes = []string{"user"}
		case "agent":
			selectedScopes = []string{"user_agent"}
			if info.IsAdmin {
				selectedScopes = append(selectedScopes, "system_agent")
			}
		default:
			selectedScopes = nil
		}
	}

	now := time.Now().UTC()
	list := func(scopes []string, limit int32, pageCursor *skills.ManagedSkillCursor) (skills.ManagedSkillPage, error) {
		if len(scopes) == 0 {
			return skills.ManagedSkillPage{}, nil
		}
		return s.skillStore().ListManagedSkills(r.Context(), skills.ManagedSkillListQuery{
			UserID: info.UserID, AgentID: agentID, Scopes: scopes, CreatedBy: createdBy,
			Query: query, State: skills.ManagedSkillStateRemoved, Limit: limit,
			Now: now, Cursor: pageCursor,
		})
	}

	page, err := list(selectedScopes, int32(pageSize), cursor)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	allPage, err := list(allowed, 1, nil)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	userPage, err := list([]string{"user"}, 1, nil)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	agentScopes := []string{"user_agent"}
	if info.IsAdmin {
		agentScopes = append(agentScopes, "system_agent")
	}
	agentPage, err := list(agentScopes, 1, nil)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}

	out := make([]skillView, len(page.Items))
	for i, item := range page.Items {
		out[i] = storedSkillToView(item.Skill, nil)
		out[i].IsRestorable = item.IsRestorable
		out[i].DeprecatedAt = item.DeprecatedAt
		out[i].RestoreDeadline = item.RestoreDeadline
		if item.RemovalSource != "" {
			source := item.RemovalSource
			out[i].RemovalSource = &source
		}
	}
	response := map[string]any{
		"skills":     out,
		"total_size": int(page.Total),
		"scope_counts": map[string]int{
			"all": int(allPage.Total), "system": 0, "agent": int(agentPage.Total), "user": int(userPage.Total), "project": 0,
		},
		"next_page_token": nil,
	}
	if page.HasMore && page.NextCursor != nil {
		token, err := encodeSkillPageToken(skills.ManagedSkillStateRemoved, *page.NextCursor)
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
		response["next_page_token"] = token
	}
	writeData(w, http.StatusOK, response)
}

func (s *Server) CreateAgentSkill(w http.ResponseWriter, r *http.Request, id string) {
	agentID := id
	var req createSkillRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Scope == "" {
		writeError(w, http.StatusBadRequest, "scope is required")
		return
	}
	userID, _, code, msg := s.requireAgentSkillWrite(r.Context(), agentID, req.Scope)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	files := req.Files
	if files == nil {
		files = map[string]string{skills.MainFile: "---\nname: " + req.Name + "\ndescription: " + req.Description + "\n---\n"}
	}
	if files[skills.MainFile] == "" {
		writeError(w, http.StatusBadRequest, "files must include SKILL.md")
		return
	}
	sk := skills.Skill{
		Scope:                  req.Scope,
		Name:                   req.Name,
		Description:            req.Description,
		Status:                 req.Status,
		DisableModelInvocation: req.DisableModelInvocation,
	}
	switch req.Scope {
	case "user":
		sk.UserID = userID
	case "user_agent":
		sk.UserID = userID
		sk.AgentID = agentID
	case "system_agent":
		sk.AgentID = agentID
	}
	createdID, err := s.skillStore().Create(r.Context(), sk, files)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusCreated, map[string]string{"id": createdID, "name": req.Name})
}

func (s *Server) GetAgentSkill(w http.ResponseWriter, r *http.Request, id string, skillId string, params apiserver.GetAgentSkillParams) {
	scope := ""
	exactScope := params.Scope != nil
	if params.Scope != nil {
		scope = string(*params.Scope)
	}
	rs, _, code, msg := s.resolveAgentSkillReference(r.Context(), id, skillId, scope, exactScope, params.SessionId)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	view := resolvedSkillToView(*rs)
	if rs.Dir == "" && s.skillStore() != nil {
		sk, err := s.findSkillByID(r.Context(), rs.ID)
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
		view = s.dbSkillView(r, sk)
	}
	writeData(w, http.StatusOK, view)
}

func (s *Server) UpdateAgentSkill(w http.ResponseWriter, r *http.Request, id string, skillId string, params apiserver.UpdateAgentSkillParams) {
	rs, _, code, msg := s.resolveAgentSkillReference(r.Context(), id, skillId, string(params.Scope), true, params.SessionId)
	if code != 0 {
		writeError(w, code, msg)
		return
	}

	// Project/system skills live on the filesystem.
	if rs.Dir != "" {
		if rs.Scope == "system" {
			writeError(w, http.StatusForbidden, "system skills are read-only")
			return
		}
		var req updateSkillRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		for p, content := range req.Files {
			file, err := safeSkillFilePath(rs.Dir, p)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid path")
				return
			}
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				s.writeInternalError(w, err)
				return
			}
			if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
				s.writeInternalError(w, err)
				return
			}
		}
		writeData(w, http.StatusOK, map[string]string{"id": skillId})
		return
	}

	// DB-backed skill.
	_, vc, code, msg := s.requireAgentSkillWrite(r.Context(), id, rs.Scope)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	sk, code, msg := s.requireSkillScope(r.Context(), rs.ID, rs.Scope, vc.UserID, id)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	s.applySkillUpdate(w, r, sk)
}

// RestoreAgentSkill restores one retained mutable DB row in its exact owner
// scope. Stable IDs are required so a same-name replacement is never targeted.
func (s *Server) RestoreAgentSkill(w http.ResponseWriter, r *http.Request, id string, skillId string, params apiserver.RestoreAgentSkillParams) {
	scope := string(params.Scope)
	info := UserFromContext(r.Context())
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if _, code, msg := s.requireAgentAccess(r.Context(), id); code != 0 {
		writeError(w, code, msg)
		return
	}
	// Check the stable row identity before scope-specific write authorization so
	// mismatched scopes and owners fail closed without revealing another bucket.
	sk, code, msg := s.requireSkillScope(r.Context(), skillId, scope, info.UserID, id)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	actingUserID, _, code, msg := s.requireAgentSkillWrite(r.Context(), id, scope)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	result, err := s.skillStore().RestoreManagedSkill(r.Context(), skills.ManagedSkillRestore{
		ID: sk.ID, UserID: sk.UserID, AgentID: sk.AgentID, Scope: sk.Scope,
		RestoredBy: actingUserID, Now: time.Now().UTC(),
	})
	switch {
	case errors.Is(err, skills.ErrSkillNameConflict):
		writeError(w, http.StatusConflict, "an active skill already has this name in the same scope")
		return
	case errors.Is(err, skills.ErrSkillRestoreExpired):
		writeError(w, http.StatusGone, "skill restore window expired")
		return
	case errors.Is(err, skills.ErrSkillNotRestorable), errors.Is(err, skills.ErrSkillNotMutable):
		writeError(w, http.StatusConflict, "skill is not restorable")
		return
	case err != nil:
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, s.dbSkillView(r, &result.Skill))
}

// UpgradeAgentSkill re-fetches a DB-backed skill from its recorded install source
// and updates it in place when the source has a newer version. It is the
// check-and-update behind the inspector's "check for updates" button.
func (s *Server) UpgradeAgentSkill(w http.ResponseWriter, r *http.Request, id string, skillId string, params apiserver.UpgradeAgentSkillParams) {
	var rs *skills.ResolvedSkill
	var code int
	var msg string
	if params.Scope != nil {
		rs, _, code, msg = s.resolveSkill(r.Context(), id, skillId, *params.Scope, nil)
	} else {
		rs, _, code, msg = s.resolveSkillAny(r.Context(), id, skillId, nil)
	}
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	// Project/system skills live on disk and are managed via the filesystem/CLI.
	if rs.Dir != "" {
		writeError(w, http.StatusBadRequest, "only installed skills can be upgraded")
		return
	}

	actingUserID, vc, code, msg := s.requireAgentSkillWrite(r.Context(), id, rs.Scope)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	if _, code, msg := s.requireSkillScope(r.Context(), rs.ID, rs.Scope, vc.UserID, id); code != 0 {
		writeError(w, code, msg)
		return
	}

	ctx := r.Context()
	if skills.GitHubSource(skillSource(rs.Metadata)) {
		if token := s.credSvc.GitHubAccessToken(ctx, actingUserID); token != "" {
			ctx = skills.WithGitHubToken(ctx, token)
		}
	}

	res, err := skills.UpgradeInStore(ctx, pluginhost.NewSkillStoreAdapter(s.skillStore()), rs.ID, rs.Metadata)
	if err != nil {
		if errors.Is(err, skills.ErrNoUpgradeSource) {
			writeError(w, http.StatusBadRequest, "skill was not installed from an upgradable source")
			return
		}
		s.writeBadGatewayError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"updated":          res.Updated,
		"version":          res.Version,
		"previous_version": res.PreviousVersion,
	})
}

func (s *Server) DeleteAgentSkill(w http.ResponseWriter, r *http.Request, id string, skillId string, params apiserver.DeleteAgentSkillParams) {
	rs, _, code, msg := s.resolveSkill(r.Context(), id, skillId, string(params.Scope), params.SessionId)
	if code != 0 {
		writeError(w, code, msg)
		return
	}

	if rs.Dir != "" {
		if rs.Scope == "system" {
			writeError(w, http.StatusForbidden, "system skills are read-only")
			return
		}
		if err := os.RemoveAll(rs.Dir); err != nil {
			s.writeInternalError(w, err)
			return
		}
		writeNoContent(w)
		return
	}

	_, vc, code, msg := s.requireAgentSkillWrite(r.Context(), id, rs.Scope)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	if _, code, msg := s.requireSkillScope(r.Context(), rs.ID, rs.Scope, vc.UserID, id); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.doDeleteSkill(w, r, rs.ID)
}

func (s *Server) GetAgentSkillFile(w http.ResponseWriter, r *http.Request, id string, skillId string, params apiserver.GetAgentSkillFileParams) {
	scope := ""
	exactScope := params.Scope != nil
	if params.Scope != nil {
		scope = string(*params.Scope)
	}
	rs, _, code, msg := s.resolveAgentSkillReference(r.Context(), id, skillId, scope, exactScope, params.SessionId)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	content, err := s.loadSkillFile(r.Context(), rs, params.Path)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeData(w, http.StatusOK, map[string]string{"path": params.Path, "content": content})
}

func (s *Server) DeleteAgentSkillFile(w http.ResponseWriter, r *http.Request, id string, skillId string, params apiserver.DeleteAgentSkillFileParams) {
	rs, _, code, msg := s.resolveSkill(r.Context(), id, skillId, string(params.Scope), params.SessionId)
	if code != 0 {
		writeError(w, code, msg)
		return
	}

	if rs.Dir != "" {
		if rs.Scope == "system" {
			writeError(w, http.StatusForbidden, "system skills are read-only")
			return
		}
		if params.Path == skills.MainFile {
			writeError(w, http.StatusBadRequest, "cannot delete SKILL.md")
			return
		}
		file, err := safeSkillFilePath(rs.Dir, params.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid path")
			return
		}
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.writeInternalError(w, err)
			return
		}
		writeNoContent(w)
		return
	}

	_, vc, code, msg := s.requireAgentSkillWrite(r.Context(), id, rs.Scope)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	if _, code, msg := s.requireSkillScope(r.Context(), rs.ID, rs.Scope, vc.UserID, id); code != 0 {
		writeError(w, code, msg)
		return
	}
	s.doDeleteSkillFile(w, r, rs.ID, params.Path)
}

func (s *Server) InstallAgentSkill(w http.ResponseWriter, r *http.Request, id string) {
	agentID := id
	var req installSkillRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Source == "" {
		writeError(w, http.StatusBadRequest, "source is required")
		return
	}
	scope := req.Scope
	if scope == "" {
		scope = defaultAgentSkillScope(r.Context())
	}
	userID, _, code, msg := s.requireAgentSkillWrite(r.Context(), agentID, scope)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	storeUserID := ""
	if scope == "user" || scope == "user_agent" {
		storeUserID = userID
	}
	ctx := r.Context()
	if skills.GitHubSource(req.Source) {
		if token := s.credSvc.GitHubAccessToken(ctx, userID); token != "" {
			ctx = skills.WithGitHubToken(ctx, token)
		}
	}
	name, err := skills.InstallToStore(ctx, pluginhost.NewSkillStoreAdapter(s.skillStore()), req.Source, scope, storeUserID, agentID)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a skill with this name is already installed in this scope")
			return
		}
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusCreated, map[string]string{"name": name})
}

func (s *Server) UploadAgentSkill(w http.ResponseWriter, r *http.Request, id string) {
	s.uploadAgentSkill(w, r, id) //nolint:contextcheck
}
