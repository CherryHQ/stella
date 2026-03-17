package admin

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/db/sqlc"
	"github.com/vaayne/anna/internal/memory"
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
	if s.mem == nil {
		writeData(w, http.StatusOK, []any{})
		return
	}
	sessions, err := s.mem.ListInfo(r.Context(), true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := make([]sessionResponse, len(sessions))
	for i, info := range sessions {
		resp[i] = toSessionResponse(info)
	}
	writeData(w, http.StatusOK, resp)
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}
	if s.mem == nil {
		writeError(w, http.StatusNotFound, "memory engine not available")
		return
	}

	info, err := s.mem.LoadInfo(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	resp := sessionDetailResponse{
		sessionResponse: toSessionResponse(info),
	}

	// Resolve agent name.
	if info.AgentID != "" {
		agent, err := s.store.GetAgent(r.Context(), info.AgentID)
		if err == nil {
			resp.AgentName = agent.Name
		}
	}

	// Resolve user name.
	if info.UserID != 0 {
		user, err := s.store.GetUser(r.Context(), info.UserID)
		if err == nil {
			resp.UserName = user.Name
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

func (s *Server) getSessionSystemPrompt(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}
	if s.mem == nil {
		writeError(w, http.StatusNotFound, "memory engine not available")
		return
	}

	info, err := s.mem.LoadInfo(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	// Look up agent config.
	var agentCfg config.Agent
	if info.AgentID != "" {
		agentCfg, _ = s.store.GetAgent(r.Context(), info.AgentID)
	}

	// Look up user-agent memory.
	var userMemory string
	if info.UserID != 0 && info.AgentID != "" {
		userMemory, _ = s.store.GetUserAgentMemory(r.Context(), info.UserID, info.AgentID)
	}

	prompt := runner.BuildSystemPromptFromDB(runner.DBPromptParams{
		SystemPrompt: agentCfg.SystemPrompt,
		UserMemory:   userMemory,
		AnnaHome:     config.AnnaHome(),
		Workspace:    agentCfg.Workspace,
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
