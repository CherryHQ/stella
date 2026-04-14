package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/pkg/db/sqlc"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	mcpplugin "github.com/vaayne/anna/plugins/tools/mcp"
)

// sessionResponse is a JSON-friendly representation of memory.SessionInfo.
type sessionResponse struct {
	ID         string `json:"id"`
	Channel    string `json:"channel"`
	Title      string `json:"title"`
	AgentID    string `json:"agent_id"`
	UserID     int64  `json:"user_id"`
	CreatedAt  string `json:"created_at"`
	LastActive string `json:"last_active"`
	Archived   bool   `json:"archived"`
}

// sessionDetailResponse extends sessionResponse with resolved names.
type sessionDetailResponse struct {
	sessionResponse
	AgentName string `json:"agent_name"`
	UserName  string `json:"user_name"`
}

func toSessionResponse(info memory.SessionInfo) sessionResponse {
	return sessionResponse{
		ID:         info.ID,
		Channel:    info.Channel,
		Title:      info.Title,
		AgentID:    info.AgentID,
		UserID:     info.UserID,
		CreatedAt:  info.CreatedAt.Format(time.RFC3339),
		LastActive: info.LastActive.Format(time.RFC3339),
		Archived:   info.Archived,
	}
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	sm, ok := s.mem.(memory.SessionManager)
	if !ok {
		writeData(w, http.StatusOK, []any{})
		return
	}
	info := UserFromContext(r.Context())
	sessions, err := sm.ListInfo(r.Context(), memory.ListOptions{IncludeArchived: true})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := make([]sessionResponse, 0, len(sessions))
	for _, si := range sessions {
		// Non-admin users only see their own sessions.
		if info != nil && !info.IsAdmin && si.UserID != info.UserID {
			continue
		}
		resp = append(resp, toSessionResponse(si))
	}
	writeData(w, http.StatusOK, resp)
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}
	sm, ok := s.mem.(memory.SessionManager)
	if !ok {
		writeError(w, http.StatusNotFound, "memory provider does not support sessions")
		return
	}

	authInfo := UserFromContext(r.Context())
	si, err := sm.LoadInfo(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	// Non-admin users can only view their own sessions.
	if authInfo != nil && !authInfo.IsAdmin && si.UserID != authInfo.UserID {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	resp := sessionDetailResponse{
		sessionResponse: toSessionResponse(si),
	}
	info := si

	// Resolve agent name.
	if info.AgentID != "" {
		agent, err := s.store.GetAgent(r.Context(), info.AgentID)
		if err == nil {
			resp.AgentName = agent.Name
		}
	}

	// Resolve user name from auth system.
	if info.UserID != 0 {
		authUser, err := s.authStore.GetUser(r.Context(), info.UserID)
		if err == nil {
			resp.UserName = authUser.Username
		}
	}

	writeData(w, http.StatusOK, resp)
}

func (s *Server) getSessionMessages(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}

	// Ownership check for non-admin users.
	if err := s.checkSessionAccess(w, r, sessionID); err != nil {
		return
	}

	// Load raw DB rows to preserve created_at timestamps.
	conv, err := s.q.GetConversationBySessionID(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	rows, err := s.q.GetMessagesByConversation(r.Context(), conv.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusOK, serializeDBMessages(rows))
}

// checkSessionAccess verifies the current user has access to the session.
// Returns a non-nil error (and writes the HTTP response) if access is denied.
func (s *Server) checkSessionAccess(w http.ResponseWriter, r *http.Request, sessionID string) error {
	info := UserFromContext(r.Context())
	sm, ok := s.mem.(memory.SessionManager)
	if info == nil || info.IsAdmin || !ok {
		return nil
	}
	si, err := sm.LoadInfo(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return err
	}
	if si.UserID != info.UserID {
		writeError(w, http.StatusForbidden, "access denied")
		return fmt.Errorf("access denied")
	}
	return nil
}

func (s *Server) getSessionSystemPrompt(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}
	sm, ok := s.mem.(memory.SessionManager)
	if !ok {
		writeError(w, http.StatusNotFound, "memory provider does not support sessions")
		return
	}

	// Ownership check for non-admin users.
	if err := s.checkSessionAccess(w, r, sessionID); err != nil {
		return
	}

	info, err := sm.LoadInfo(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	// Look up agent config.
	var agentCfg config.Agent
	if info.AgentID != "" {
		agentCfg, _ = s.store.GetAgent(r.Context(), info.AgentID)
	}
	cwd, _ := os.Getwd()
	var userDataDir string
	if info.UserID > 0 && info.AgentID != "" {
		if userDir, err := agent.SetupUserWorkspace(info.AgentID, config.AnnaHome(), info.UserID); err == nil {
			userDataDir = agent.UserDataDir(userDir)
		}
	}
	homeDir, _ := os.UserHomeDir()
	promptSections, err := s.pluginHost.SystemPromptSections(r.Context(), pkgplugins.SystemPromptContext{
		AnnaHome:    config.AnnaHome(),
		HomeDir:     homeDir,
		Workspace:   agentCfg.Workspace,
		Cwd:         cwd,
		UserID:      info.UserID,
		AgentID:     info.AgentID,
		UserDataDir: userDataDir,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	promptTools, err := s.pluginHost.PromptTools(r.Context(), mcpplugin.PluginID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	prompt := runner.BuildSystemPromptFromDB(r.Context(), runner.DBPromptParams{
		SystemPrompt:   agentCfg.SystemPrompt,
		Memory:         s.mem,
		UserID:         info.UserID,
		AgentID:        info.AgentID,
		AnnaHome:       config.AnnaHome(),
		Workspace:      agentCfg.Workspace,
		Cwd:            cwd,
		UserDataDir:    userDataDir,
		PromptTools:    promptTools,
		PromptSections: promptSections,
	})

	writeData(w, http.StatusOK, map[string]string{"system_prompt": prompt})
}

// serializeDBMessages converts raw DB message rows to JSON-friendly maps,
// preserving the created_at timestamp from the database.
func serializeDBMessages(rows []sqlc.CtxMessage) []map[string]any {
	result := make([]map[string]any, 0, len(rows))
	i := 0
	for i < len(rows) {
		row := rows[i]
		switch row.Role {
		case "user":
			result = append(result, serializeUserRow(row))
			i++
		case "assistant":
			m, consumed := serializeAssistantRows(rows, i)
			result = append(result, m)
			i += consumed
		case "tool":
			result = append(result, serializeToolRow(row))
			i++
		default:
			i++
		}
	}
	return result
}

func serializeUserRow(row sqlc.CtxMessage) map[string]any {
	m := map[string]any{
		"role":      "user",
		"timestamp": row.CreatedAt,
	}
	m["content"] = row.Content
	return m
}

func serializeAssistantRows(rows []sqlc.CtxMessage, start int) (map[string]any, int) {
	var blocks []map[string]any
	consumed := 0
	row := rows[start]

	switch row.EventType {
	case "text":
		blocks = append(blocks, map[string]any{"type": "text", "text": row.Content})
		consumed++
	case "tool_call":
		blocks = append(blocks, decodeToolCallBlock(row.Content))
		consumed++
	default:
		blocks = append(blocks, map[string]any{"type": "text", "text": row.Content})
		consumed++
		return map[string]any{
			"role":      "assistant",
			"blocks":    blocks,
			"timestamp": row.CreatedAt,
		}, consumed
	}

	// Merge following tool_call rows from the same assistant turn.
	for start+consumed < len(rows) {
		next := rows[start+consumed]
		if next.Role != "assistant" || next.EventType != "tool_call" {
			break
		}
		blocks = append(blocks, decodeToolCallBlock(next.Content))
		consumed++
	}

	return map[string]any{
		"role":      "assistant",
		"blocks":    blocks,
		"timestamp": row.CreatedAt,
	}, consumed
}

func decodeToolCallBlock(content string) map[string]any {
	var env struct {
		ID   string          `json:"id"`
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal([]byte(content), &env); err != nil {
		return map[string]any{"type": "tool_call", "name": "unknown", "arguments": map[string]any{}}
	}
	var args map[string]any
	_ = json.Unmarshal(env.Args, &args)
	return map[string]any{"type": "tool_call", "id": env.ID, "name": env.Tool, "arguments": args}
}

func serializeToolRow(row sqlc.CtxMessage) map[string]any {
	m := map[string]any{
		"role":      "tool",
		"timestamp": row.CreatedAt,
	}
	var env struct {
		ID     string          `json:"id"`
		Tool   string          `json:"tool"`
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error,omitempty"`
	}
	if err := json.Unmarshal([]byte(row.Content), &env); err != nil {
		m["content"] = row.Content
		m["tool_name"] = ""
		m["is_error"] = false
		return m
	}
	var text string
	_ = json.Unmarshal(env.Result, &text)
	m["tool_call_id"] = env.ID
	m["tool_name"] = env.Tool
	m["content"] = text
	m["is_error"] = env.Error != ""
	return m
}
