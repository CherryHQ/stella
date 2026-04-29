package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/pkg/db/sqlc"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	mcpplugin "github.com/vaayne/anna/plugins/tools/mcp"
)

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	authInfo := UserFromContext(r.Context())
	if authInfo == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var body struct {
		AgentID string `json:"agent_id"`
	}
	if err := decodeJSON(r, &body); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var pool *agent.Pool
	if body.AgentID != "" {
		pool = s.poolManager.Get(body.AgentID)
	} else {
		pool = s.poolManager.DefaultPool()
	}
	if pool == nil {
		writeError(w, http.StatusBadRequest, "no pool available for the given agent_id")
		return
	}

	info, err := pool.CreateSession("admin", authInfo.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusCreated, toSessionResponse(info))
}

func (s *Server) sendSessionMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}

	authInfo := UserFromContext(r.Context())
	if authInfo == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var body struct {
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}

	sm, ok := s.mem.(memory.SessionManager)
	if !ok {
		writeError(w, http.StatusNotFound, "memory provider does not support sessions")
		return
	}

	si, err := sm.LoadInfo(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	// Ownership is strict: only the session owner may send messages.
	if authInfo.UserID != si.UserID {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	var pool *agent.Pool
	if si.AgentID != "" {
		pool = s.poolManager.Get(si.AgentID)
	} else {
		pool = s.poolManager.DefaultPool()
	}
	if pool == nil {
		writeError(w, http.StatusBadRequest, "no pool available for this session")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	writeSSEEvent := func(event, data string) {
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
	}

	// runningToolID maps tool name to the generated ID for the running invocation.
	runningToolID := make(map[string]string)

	ch := pool.Chat(r.Context(), sessionID, body.Content)
	for {
		select {
		case <-r.Context().Done():
			return
		case evt, open := <-ch:
			if !open {
				return
			}
			if evt.Err != nil {
				data, _ := json.Marshal(map[string]string{"error": evt.Err.Error()})
				writeSSEEvent("error", string(data))
				return
			}
			if evt.Store != nil {
				continue
			}
			if evt.ToolUse != nil {
				tu := evt.ToolUse
				switch tu.Status {
				case "running":
					id := fmt.Sprintf("%s-%d", tu.Tool, time.Now().UnixNano())
					runningToolID[tu.Tool] = id
					payload := map[string]any{
						"type": "tool_call",
						"id":   id,
						"name": tu.Tool,
						"arguments": map[string]string{
							"input": tu.Input,
						},
						"status": "running",
					}
					data, _ := json.Marshal(payload)
					writeSSEEvent("tool_use", string(data))
				case "done", "error":
					id := runningToolID[tu.Tool]
					if id == "" {
						id = fmt.Sprintf("%s-%d", tu.Tool, time.Now().UnixNano())
					}
					delete(runningToolID, tu.Tool)
					payload := map[string]any{
						"type":         "tool_result",
						"tool_call_id": id,
						"content":      tu.Detail,
						"is_error":     tu.Status == "error",
					}
					data, _ := json.Marshal(payload)
					writeSSEEvent("tool_use", string(data))
				}
				continue
			}
			if evt.Text != "" {
				data, _ := json.Marshal(map[string]string{"text": evt.Text})
				writeSSEEvent("text", string(data))
			}
		}
	}
}

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
	var userRoot string
	if info.UserID > 0 && info.AgentID != "" {
		if userDir, err := agent.SetupUserWorkspace(info.AgentID, config.AnnaHome(), info.UserID); err == nil {
			userRoot = agent.UserRoot(userDir)
		}
	}
	homeDir, _ := os.UserHomeDir()
	pluginView, err := s.pluginHost.SessionPluginView(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	promptSections, err := s.pluginHost.SystemPromptSections(r.Context(), pkgplugins.SystemPromptContext{
		AnnaHome:            config.AnnaHome(),
		HomeDir:             homeDir,
		AgentRoot:           agentCfg.Workspace,
		ProjectRoot:         "",
		UserID:              info.UserID,
		AgentID:             info.AgentID,
		UserRoot:            userRoot,
		RegisteredPluginIDs: pluginView.RegisteredPluginIDs,
		EnabledPluginIDs:    pluginView.EnabledPluginIDs,
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

	prompt := agent.BuildSystemPromptFromDB(r.Context(), agent.DBPromptParams{
		SystemPrompt:   agentCfg.SystemPrompt,
		Memory:         s.mem,
		UserID:         info.UserID,
		AgentID:        info.AgentID,
		AnnaHome:       config.AnnaHome(),
		AgentRoot:      agentCfg.Workspace,
		UserRoot:       userRoot,
		PromptTools:    promptTools,
		PluginPrompts:  s.pluginHost.ManifestPluginPrompts(),
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
	return map[string]any{
		"role":        "user",
		"timestamp":   row.CreatedAt,
		"content":     row.Content,
		"token_count": row.TokenCount,
	}
}

func serializeAssistantRows(rows []sqlc.CtxMessage, start int) (map[string]any, int) {
	var blocks []map[string]any
	var totalTokens int64
	consumed := 0

	// Merge ALL consecutive assistant rows into one turn — text and tool_calls alike.
	// A non-assistant row (user, tool) always breaks the run.
	for start+consumed < len(rows) {
		row := rows[start+consumed]
		if row.Role != "assistant" {
			break
		}
		totalTokens += row.TokenCount
		switch row.EventType {
		case "tool_call":
			blocks = append(blocks, decodeToolCallBlock(row.Content))
		default:
			blocks = append(blocks, map[string]any{"type": "text", "text": row.Content})
		}
		consumed++
	}

	return map[string]any{
		"role":        "assistant",
		"blocks":      blocks,
		"timestamp":   rows[start].CreatedAt,
		"token_count": totalTokens,
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
		"role":        "tool",
		"timestamp":   row.CreatedAt,
		"token_count": row.TokenCount,
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
