package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/skillaccess"
	"github.com/CherryHQ/stella/internal/skills"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// beginSkillAccess opens one Skill policy evaluation for an authenticated caller.
// The Authority carries the verified session role; request path/body fields never
// contribute to it. Every DB-backed skill decision flows through the returned
// Access, so collection and per-row visibility cannot drift.
func (s *Server) beginSkillAccess(ctx context.Context) (*skillaccess.Access, int, string) {
	if s.skillAccess == nil {
		return nil, http.StatusServiceUnavailable, "skills authorization unavailable"
	}
	info := UserFromContext(ctx)
	if info == nil {
		return nil, http.StatusUnauthorized, "unauthorized"
	}
	authority, err := info.authority()
	if err != nil {
		return nil, http.StatusForbidden, "forbidden"
	}
	acc, err := s.skillAccess.Begin(ctx, authority)
	if err != nil {
		code, msg := skillAccessError(err)
		return nil, code, msg
	}
	return acc, 0, ""
}

// skillAccessError maps a Skill PEP sentinel to an HTTP status and message,
// preserving the accepted 404-not-found / 403-forbidden split.
func skillAccessError(err error) (int, string) {
	switch {
	case err == nil:
		return 0, ""
	case errors.Is(err, skillaccess.ErrNotFound):
		return http.StatusNotFound, "skill not found"
	case errors.Is(err, skillaccess.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, skillaccess.ErrInvalidScope):
		return http.StatusBadRequest, "invalid scope"
	case errors.Is(err, skillaccess.ErrUnavailable):
		return http.StatusServiceUnavailable, "skills authorization unavailable"
	default:
		return http.StatusInternalServerError, "internal error"
	}
}

// beginAgentSkillAccess opens one Skill authorization decision and folds the
// route agent's read gate into it, so both the skill and its path-agent gate
// share a single authorization for every scope (including user/system DB skills).
// It replaces the preliminary requireAgentAccess split decision on the
// agent-scoped skill endpoints. Returns (code, msg) != 0 for the caller to write
// on failure.
func (s *Server) beginAgentSkillAccess(ctx context.Context, agentID string) (*skillaccess.Access, int, string) {
	acc, code, msg := s.beginSkillAccess(ctx)
	if code != 0 {
		return nil, code, msg
	}
	if err := acc.AuthorizeAgent(ctx, agentID); err != nil {
		code, msg := skillAccessError(err)
		return nil, code, msg
	}
	return acc, 0, ""
}

// authorizeReadableDBSkills filters DB skill rows through the Skill read PEP under
// the caller's evaluation (the route agent already gated on the same acc): it
// decides the collection once, then drops each row the caller may not read. The
// FS project/built-in merge is applied by the caller afterward, so filesystem
// skills are never gated here. On an unexpected authorization failure it writes
// the response and returns ok=false.
func (s *Server) authorizeReadableDBSkills(w http.ResponseWriter, r *http.Request, acc *skillaccess.Access, dbSkills []skills.Skill) ([]skills.Skill, bool) {
	if err := acc.AuthorizeList(); err != nil {
		code, msg := skillAccessError(err)
		writeError(w, code, msg)
		return nil, false
	}
	out := make([]skills.Skill, 0, len(dbSkills))
	for _, sk := range dbSkills {
		err := acc.AuthorizeRead(r.Context(), sk)
		switch {
		case err == nil:
			out = append(out, sk)
		case errors.Is(err, skillaccess.ErrNotFound), errors.Is(err, skillaccess.ErrForbidden):
			// filtered
		default:
			code, msg := skillAccessError(err)
			writeError(w, code, msg)
			return nil, false
		}
	}
	return out, true
}

// authorizeDBSkillRead authorizes reading one resolved DB-backed skill (Dir=="")
// through the Skill read PEP, reusing the acc that already gated the route agent
// so the agent and skill decisions share one evaluation. Filesystem project/
// built-in skills pass. On denial it writes the response and returns false.
func (s *Server) authorizeDBSkillRead(w http.ResponseWriter, r *http.Request, acc *skillaccess.Access, rs *skills.ResolvedSkill) bool {
	if rs == nil || rs.Dir != "" {
		return true
	}
	if err := acc.AuthorizeRead(r.Context(), resolvedToDBSkill(rs)); err != nil {
		code, msg := skillAccessError(err)
		writeError(w, code, msg)
		return false
	}
	return true
}

// resolvedToDBSkill projects a resolved (FS-or-DB) skill into the durable row
// facts the Skill PEP authorizes against. Only DB rows reach it.
func resolvedToDBSkill(rs *skills.ResolvedSkill) skills.Skill {
	return skills.Skill{
		ID:       rs.ID,
		Scope:    rs.Scope,
		UserID:   rs.UserID,
		AgentID:  rs.AgentID,
		Metadata: rs.Metadata,
	}
}

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

// requireAgentAccess authorizes read/use access to an agent through the agent
// PEP. It is the chokepoint every agent-scoped sub-resource handler calls.
func (s *Server) requireAgentAccess(ctx context.Context, agentID string) (config.Agent, int, string) {
	info := UserFromContext(ctx)
	if info == nil {
		return config.Agent{}, http.StatusUnauthorized, "unauthorized"
	}
	authority, err := info.authority()
	if err != nil {
		return config.Agent{}, http.StatusForbidden, "forbidden"
	}
	a, err := s.agentAccess.Read(ctx, authority, agentID)
	if err != nil {
		code, msg := agentAccessError(err)
		if code == http.StatusInternalServerError {
			s.log.Error("agent access", "agent_id", agentID, "error", err)
		}
		return config.Agent{}, code, msg
	}
	return a, 0, ""
}

// requireAgentUse authorizes executing a turn against an agent.
func (s *Server) requireAgentUse(ctx context.Context, agentID string) (config.Agent, int, string) {
	return s.requireAgentAction(ctx, agentID, "use", s.agentAccess.Use)
}

// requireAgentManage authorizes managing (updating) an agent through the agent
// PEP (admin, or the agent's creator via the creator-manage policy).
func (s *Server) requireAgentManage(ctx context.Context, agentID string) (config.Agent, int, string) {
	return s.requireAgentAction(ctx, agentID, "manage", s.agentAccess.Manage)
}

// requireAgentDelete deliberately uses ActionDelete rather than Manage: custom
// policies may grant editing while denying destructive operations.
func (s *Server) requireAgentDelete(ctx context.Context, agentID string) (config.Agent, int, string) {
	return s.requireAgentAction(ctx, agentID, "delete", s.agentAccess.Delete)
}

func (s *Server) requireAgentAction(ctx context.Context, agentID, action string, decide func(context.Context, authz.Authority, string) (config.Agent, error)) (config.Agent, int, string) {
	info := UserFromContext(ctx)
	if info == nil {
		return config.Agent{}, http.StatusUnauthorized, "unauthorized"
	}
	authority, err := info.authority()
	if err != nil {
		return config.Agent{}, http.StatusForbidden, "forbidden"
	}
	a, err := decide(ctx, authority, agentID)
	if err != nil {
		code, msg := agentAccessError(err)
		if code == http.StatusInternalServerError {
			s.log.Error("agent "+action, "agent_id", agentID, "error", err)
		}
		return config.Agent{}, code, msg
	}
	return a, 0, ""
}

// ---- helpers ----------------------------------------------------------------

func (s *Server) projectRootForSession(ctx context.Context, agentID string, sessionID *string) (string, error) {
	if sessionID == nil || *sessionID == "" {
		return "", nil
	}
	info := UserFromContext(ctx)
	if info == nil {
		return "", fmt.Errorf("unauthorized")
	}
	authority, err := info.authority()
	if err != nil {
		return "", err
	}
	access, err := s.sessionAccess.Begin(ctx, authority)
	if err != nil {
		return "", err
	}
	return access.ProjectRoot(ctx, agentID, sessionID)
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
		Name: sk.Name, Description: sk.Description,
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

// agentSkillWriteScope validates a DB-backed write scope reached through an
// agent-scoped path and authorizes it through the Skill PEP, returning the acting
// user id used for owner columns, install attribution, and token lookup. The
// agent path always carries an agent, so user/user_agent/system_agent are the
// only writable scopes here; bare system and project are managed elsewhere. The
// PEP folds the agent-read gate for the path agent into its single evaluation.
func (s *Server) agentSkillWriteScope(ctx context.Context, agentID, scope string) (string, int, string) {
	info := UserFromContext(ctx)
	if info == nil {
		return "", http.StatusUnauthorized, "unauthorized"
	}
	switch scope {
	case "user", "user_agent", "system_agent":
		// authorized through the PEP below
	case "project":
		return "", http.StatusBadRequest, "project skills are managed via the CLI or filesystem"
	case "system":
		return "", http.StatusForbidden, "system skills are managed in Settings → Skills"
	default:
		return "", http.StatusBadRequest, "scope must be one of: user, user_agent, system_agent"
	}
	acc, code, msg := s.beginSkillAccess(ctx)
	if code != 0 {
		return "", code, msg
	}
	if _, _, err := acc.AuthorizeManageScope(ctx, scope, agentID); err != nil {
		code, msg := skillAccessError(err)
		return "", code, msg
	}
	return info.UserID, 0, ""
}

func safeSkillFilePath(skillDir, filePath string) (string, error) {
	clean := filepath.Clean(filePath)
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fs.ErrPermission
	}
	return filepath.Join(skillDir, clean), nil
}

// resolveAgentSkillReference treats a retained DB ID as authoritative, then
// falls back to the legacy active-name resolver for filesystem and old clients.
func (s *Server) resolveAgentSkillReference(ctx context.Context, agentID, ref, scope string, exactScope bool, sessionID *string) (*skills.ResolvedSkill, *skillaccess.Access, string, int, string) {
	info := UserFromContext(ctx)
	if info == nil {
		return nil, nil, "", http.StatusUnauthorized, "unauthorized"
	}
	acc, code, msg := s.beginAgentSkillAccess(ctx, agentID)
	if code != 0 {
		return nil, nil, "", code, msg
	}

	sk, err := s.findSkillByID(ctx, ref)
	if err == nil {
		applicable := (!exactScope || sk.Scope == scope) && ((sk.Scope != "user_agent" && sk.Scope != "system_agent") || sk.AgentID == agentID)
		if applicable {
			if sk.Status == "deprecated" {
				return nil, nil, "", http.StatusNotFound, "skill not found"
			}
			return &skills.ResolvedSkill{Skill: dbSkillToPluginSkill(*sk)}, acc, "", 0, ""
		}
		if !exactScope {
			return nil, nil, "", http.StatusNotFound, "skill not found"
		}
		// In an exact-scope request, an ID collision outside the requested
		// scope/agent is not authoritative. Continue with legacy scoped-name
		// resolution so a legal hexadecimal Skill name remains reachable.
		err = pgx.ErrNoRows
	}
	if !isNotFound(err) {
		s.log.Error("find skill by stable id", "agent_id", agentID, "skill", ref, "error", err)
		return nil, nil, "", http.StatusInternalServerError, "internal error"
	}

	projectRoot, _ := s.projectRootForSession(ctx, agentID, sessionID)
	vc := pkgplugins.SkillViewContext{UserID: info.UserID, AgentID: agentID}
	var rs *skills.ResolvedSkill
	if exactScope {
		rs, err = s.skillService().ResolveScoped(ctx, ref, scope, vc, projectRoot)
	} else {
		rs, err = s.skillService().Resolve(ctx, ref, vc, projectRoot)
	}
	if err != nil {
		s.log.Error("resolve skill reference", "agent_id", agentID, "skill", ref, "error", err)
		return nil, nil, "", http.StatusInternalServerError, "internal error"
	}
	if rs == nil || rs.Status == "deprecated" {
		return nil, nil, "", http.StatusNotFound, "skill not found"
	}
	return rs, acc, projectRoot, 0, ""
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
	// One Skill evaluation gates the route agent and every DB row: the agent-read
	// gate is folded in (no separate requireAgentAccess evaluation).
	acc, code, msg := s.beginAgentSkillAccess(r.Context(), agentID)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	if params.Scope != nil && params.ScopeGroup != nil {
		writeError(w, http.StatusBadRequest, "scope and scope_group are mutually exclusive")
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
	pageSize := defaultPageSize
	if params.PageSize != nil {
		pageSize = *params.PageSize
	}
	if pageSize < 1 || pageSize > 100 {
		writeError(w, http.StatusBadRequest, "page_size must be between 1 and 100")
		return
	}
	pageQuery := normalizedSkillPageQuery(info.UserID, agentID, params)
	var cursor *skills.ManagedSkillCursor
	if params.PageToken != nil {
		var err error
		cursor, err = decodeSkillPageToken(*params.PageToken, pageQuery)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	query := ""
	if params.Q != nil {
		query = strings.TrimSpace(*params.Q)
	}
	projectRoot, err := s.projectRootForSession(r.Context(), agentID, params.SessionId)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	dbSkills, err := s.skillStore().ListForAgentContext(r.Context(), info.UserID, agentID)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	// Every DB row is authorized under the same evaluation before it is merged with
	// the (ungated) filesystem project/built-in skills.
	dbSkills, ok := s.authorizeReadableDBSkills(w, r, acc, dbSkills)
	if !ok {
		return
	}
	merged := s.skillService().ListMergedWithDB(dbSkillsToPluginSkills(dbSkills), projectRoot)
	filtered := make([]skills.ResolvedSkill, 0, len(merged))
	queryLower := strings.ToLower(query)
	for _, rs := range merged {
		if queryLower != "" && !strings.Contains(strings.ToLower(rs.Name), queryLower) && !strings.Contains(strings.ToLower(rs.Description), queryLower) {
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
	legacyFullList := params.ScopeGroup == nil && params.Q == nil && params.PageSize == nil && params.PageToken == nil
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
		token, err := encodeSkillPageToken(skills.ManagedSkillCursor{Timestamp: last.UpdatedAt, ID: last.ID}, pageQuery)
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
		response["next_page_token"] = token
	}
	writeData(w, http.StatusOK, response)
}

func normalizedSkillPageQuery(userID, agentID string, params apiserver.ListAgentSkillsParams) skillPageQuery {
	query := skillPageQuery{UserID: userID, AgentID: agentID}
	if params.Scope != nil {
		query.Scope = string(*params.Scope)
	}
	if params.ScopeGroup != nil {
		query.ScopeGroup = string(*params.ScopeGroup)
	}
	if params.Q != nil {
		query.Query = strings.ToLower(strings.TrimSpace(*params.Q))
	}
	if params.SessionId != nil {
		query.SessionID = *params.SessionId
	}
	return query
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
	userID, code, msg := s.agentSkillWriteScope(r.Context(), agentID, req.Scope)
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
	snapshot, err := s.skillStore().CreateManagedSkill(r.Context(), sk, files)
	if err != nil {
		if errors.Is(err, skills.ErrInvalidSkillFilePath) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusCreated, committedSkillView(snapshot))
}

func (s *Server) GetAgentSkill(w http.ResponseWriter, r *http.Request, id string, skillId string, params apiserver.GetAgentSkillParams) {
	scope := ""
	exactScope := params.Scope != nil
	if params.Scope != nil {
		scope = string(*params.Scope)
	}
	rs, acc, _, code, msg := s.resolveAgentSkillReference(r.Context(), id, skillId, scope, exactScope, params.SessionId)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	if !s.authorizeDBSkillRead(w, r, acc, rs) {
		return
	}
	view := resolvedSkillToView(*rs)
	if rs.Dir == "" && s.skillStore() != nil {
		sk, err := s.findSkillByID(r.Context(), rs.ID)
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
		view, err = s.dbSkillView(r, sk)
		if err != nil {
			s.writeInternalError(w, err)
			return
		}
	}
	writeData(w, http.StatusOK, view)
}

func (s *Server) UpdateAgentSkill(w http.ResponseWriter, r *http.Request, id string, skillId string, params apiserver.UpdateAgentSkillParams) {
	rs, acc, _, code, msg := s.resolveAgentSkillReference(r.Context(), id, skillId, string(params.Scope), true, params.SessionId)
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
		writeData(w, http.StatusOK, resolvedSkillToView(*rs))
		return
	}

	// Load and authorize the durable row by stable ID before applying lifecycle-aware updates.
	sk, err := acc.AuthorizeManageByID(r.Context(), rs.ID, authz.ActionWrite)
	if err != nil {
		code, msg := skillAccessError(err)
		writeError(w, code, msg)
		return
	}
	s.applySkillUpdate(w, r, &sk)
}

// UpgradeAgentSkill re-fetches a DB-backed skill from its recorded install source
// and updates it in place when the source has a newer version. It is the
// check-and-update behind the inspector's "check for updates" button.
func (s *Server) UpgradeAgentSkill(w http.ResponseWriter, r *http.Request, id string, skillId string, params apiserver.UpgradeAgentSkillParams) {
	scope := ""
	exactScope := params.Scope != nil
	if params.Scope != nil {
		scope = *params.Scope
	}
	rs, acc, _, code, msg := s.resolveAgentSkillReference(r.Context(), id, skillId, scope, exactScope, nil)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	// Project/system skills live on disk and are managed via the filesystem/CLI.
	if rs.Dir != "" {
		writeError(w, http.StatusBadRequest, "only installed skills can be upgraded")
		return
	}

	if _, err := acc.AuthorizeManageByID(r.Context(), rs.ID, authz.ActionWrite); err != nil {
		code, msg := skillAccessError(err)
		writeError(w, code, msg)
		return
	}
	actingUserID := ""
	if info := UserFromContext(r.Context()); info != nil {
		actingUserID = info.UserID
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
	rs, acc, _, code, msg := s.resolveAgentSkillReference(r.Context(), id, skillId, string(params.Scope), true, params.SessionId)
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

	if _, err := acc.AuthorizeManageByID(r.Context(), rs.ID, authz.ActionDelete); err != nil {
		code, msg := skillAccessError(err)
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
	rs, acc, _, code, msg := s.resolveAgentSkillReference(r.Context(), id, skillId, scope, exactScope, params.SessionId)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	if !s.authorizeDBSkillRead(w, r, acc, rs) {
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
	rs, acc, _, code, msg := s.resolveAgentSkillReference(r.Context(), id, skillId, string(params.Scope), true, params.SessionId)
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

	if err := acc.AuthorizeManage(r.Context(), resolvedToDBSkill(rs), authz.ActionWrite); err != nil {
		code, msg := skillAccessError(err)
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
	userID, code, msg := s.agentSkillWriteScope(r.Context(), agentID, scope)
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
	snapshot, err := skills.InstallToStore(ctx, s.skillStore(), req.Source, scope, storeUserID, agentID)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a skill with this name is already installed in this scope")
			return
		}
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusCreated, committedSkillView(snapshot))
}

func (s *Server) UploadAgentSkill(w http.ResponseWriter, r *http.Request, id string) {
	s.uploadAgentSkill(w, r, id) //nolint:contextcheck
}
