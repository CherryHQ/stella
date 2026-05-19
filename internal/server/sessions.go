package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	apiserver "github.com/CherryHQ/stella/api/server"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	mcpplugin "github.com/CherryHQ/stella/internal/tools/mcp"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func (s *Server) CreateSession(w http.ResponseWriter, r *http.Request) {
	authInfo := UserFromContext(r.Context())
	if authInfo == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var body apiserver.CreateSessionJSONRequestBody
	if err := decodeJSON(r, &body); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var pool *agent.Pool
	if body.AgentId != nil && *body.AgentId != "" {
		pool = s.poolManager.Get(*body.AgentId)
	} else {
		pool = s.poolManager.DefaultPool()
	}
	if pool == nil {
		writeError(w, http.StatusBadRequest, "no pool available for the given agent_id")
		return
	}

	kind := "chat"
	if body.Kind != nil {
		kind = string(*body.Kind)
	}
	projectID := ""
	if body.ProjectId != nil {
		projectID = *body.ProjectId
	}

	info, err := pool.CreateSessionWithKind("admin", kind, projectID, authInfo.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusCreated, toSessionResponse(info))
}

func (s *Server) SendSessionMessage(w http.ResponseWriter, r *http.Request, sessionID string) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}

	authInfo := UserFromContext(r.Context())
	if authInfo == nil {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var body apiserver.SendSessionMessageJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body.Parts) == 0 {
		writeError(w, http.StatusBadRequest, "parts is required")
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

	// Convert AI SDK parts to internal MessageContent.
	msgContent := partsToMessageContent(body.Parts)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Vercel-AI-UI-Message-Stream", "v1")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	writeData := func(v any) {
		data, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
	writeDone := func() {
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}

	messageID := uuid.New().String()
	writeData(map[string]string{"type": "start", "messageId": messageID})

	var (
		inText      bool
		textID      string
		inReasoning bool
		reasoningID string
		stepOpen    bool
	)

	closeText := func() {
		if inText {
			writeData(map[string]string{"type": "text-end", "id": textID})
			inText = false
		}
	}
	closeReasoning := func() {
		if inReasoning {
			writeData(map[string]string{"type": "reasoning-end", "id": reasoningID})
			inReasoning = false
		}
	}

	ch := pool.Chat(r.Context(), sessionID, msgContent)
	for {
		select {
		case <-r.Context().Done():
			closeText()
			closeReasoning()
			if stepOpen {
				writeData(map[string]string{"type": "finish-step"})
			}
			writeData(map[string]string{"type": "finish"})
			writeDone()
			return
		case evt, open := <-ch:
			if !open {
				closeText()
				closeReasoning()
				if stepOpen {
					writeData(map[string]string{"type": "finish-step"})
				}
				writeData(map[string]string{"type": "finish"})
				writeDone()
				return
			}

			if evt.Err != nil {
				closeText()
				closeReasoning()
				writeData(map[string]string{"type": "error", "errorText": evt.Err.Error()})
				if stepOpen {
					writeData(map[string]string{"type": "finish-step"})
				}
				writeData(map[string]string{"type": "finish"})
				writeDone()
				return
			}

			if evt.Store != nil {
				continue
			}

			if evt.Step != nil {
				switch evt.Step.Kind {
				case "start":
					closeText()
					closeReasoning()
					if stepOpen {
						writeData(map[string]string{"type": "finish-step"})
					}
					writeData(map[string]string{"type": "start-step"})
					stepOpen = true
				case "finish":
					closeText()
					closeReasoning()
					if stepOpen {
						writeData(map[string]string{"type": "finish-step"})
						stepOpen = false
					}
				}
				continue
			}

			if evt.Reasoning != "" {
				closeText()
				if !inReasoning {
					reasoningID = uuid.New().String()
					writeData(map[string]string{"type": "reasoning-start", "id": reasoningID})
					inReasoning = true
				}
				writeData(map[string]any{"type": "reasoning-delta", "id": reasoningID, "delta": evt.Reasoning})
				continue
			}

			if evt.Text != "" {
				closeReasoning()
				if !inText {
					textID = uuid.New().String()
					writeData(map[string]string{"type": "text-start", "id": textID})
					inText = true
				}
				writeData(map[string]any{"type": "text-delta", "id": textID, "delta": evt.Text})
				continue
			}

			if evt.ToolUse != nil {
				closeText()
				closeReasoning()
				tu := evt.ToolUse
				switch tu.Status {
				case "running":
					writeData(map[string]any{
						"type":       "tool-input-start",
						"toolCallId": tu.ID,
						"toolName":   tu.Tool,
						"dynamic":    true,
					})
					args := tu.Arguments
					if args == nil {
						args = map[string]any{"input": tu.Input}
					}
					writeData(map[string]any{
						"type":       "tool-input-available",
						"toolCallId": tu.ID,
						"toolName":   tu.Tool,
						"dynamic":    true,
						"input":      args,
					})
				case "done":
					writeData(map[string]any{
						"type":       "tool-output-available",
						"toolCallId": tu.ID,
						"output":     tu.Content,
					})
				case "error":
					writeData(map[string]any{
						"type":       "tool-output-error",
						"toolCallId": tu.ID,
						"errorText":  tu.Content,
					})
				}
				continue
			}

			if evt.Image != nil {
				closeText()
				closeReasoning()
				dataURI := "data:" + evt.Image.MimeType + ";base64," + evt.Image.Data
				writeData(map[string]string{
					"type":      "file",
					"url":       dataURI,
					"mediaType": evt.Image.MimeType,
				})
				continue
			}

			if evt.File != nil {
				closeText()
				closeReasoning()
				fileURL := fmt.Sprintf("/api/sessions/%s/workspace/file-content?path=%s&raw=true",
					sessionID, evt.File.Path)
				mediaType := detectMIME(evt.File.Name)
				writeData(map[string]string{
					"type":      "file",
					"url":       fileURL,
					"mediaType": mediaType,
				})
				continue
			}
		}
	}
}

// partsToMessageContent converts API MessageParts to internal MessageContent.
func partsToMessageContent(parts []apitypes.MessagePart) agent.MessageContent {
	if len(parts) == 1 && parts[0].Type == apitypes.Text && parts[0].Text != nil {
		return *parts[0].Text
	}
	var blocks []ai.ContentBlock
	for _, p := range parts {
		switch p.Type {
		case apitypes.Text:
			if p.Text != nil {
				blocks = append(blocks, ai.TextContent{Text: *p.Text})
			}
		case apitypes.Image:
			if p.Image != nil {
				mime := "image/png"
				if p.MimeType != nil {
					mime = *p.MimeType
				}
				blocks = append(blocks, ai.ImageContent{Data: *p.Image, MimeType: mime})
			}
		}
	}
	if len(blocks) == 0 {
		return ""
	}
	return blocks
}

// detectMIME returns a MIME type based on file extension.
func detectMIME(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".txt":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	default:
		return "application/octet-stream"
	}
}

// sessionResponse is a JSON-friendly representation of memory.SessionInfo.
type sessionResponse struct {
	ID         string `json:"id"`
	Channel    string `json:"channel"`
	Kind       string `json:"kind"`
	ProjectID  string `json:"project_id,omitempty"`
	Title      string `json:"title"`
	AgentID    string `json:"agent_id"`
	UserID     string `json:"user_id"`
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
		Kind:       info.Kind,
		ProjectID:  info.ProjectID,
		Title:      info.Title,
		AgentID:    info.AgentID,
		UserID:     info.UserID,
		CreatedAt:  info.CreatedAt.Format(time.RFC3339),
		LastActive: info.LastActive.Format(time.RFC3339),
		Archived:   info.Archived,
	}
}

func (s *Server) ListSessions(w http.ResponseWriter, r *http.Request, params apiserver.ListSessionsParams) {
	sm, ok := s.mem.(memory.SessionManager)
	if !ok {
		writeData(w, http.StatusOK, []any{})
		return
	}
	info := UserFromContext(r.Context())

	limit := 10
	if params.Limit != nil && *params.Limit >= 0 {
		limit = *params.Limit
	}
	offset := 0
	if params.Offset != nil && *params.Offset >= 0 {
		offset = *params.Offset
	}

	opts := memory.ListOptions{IncludeArchived: true, Limit: limit, Offset: offset}
	if info != nil {
		opts.UserID = info.UserID
	}
	if params.Kind != nil {
		opts.Kind = string(*params.Kind)
	}
	if params.AgentId != nil {
		opts.AgentID = *params.AgentId
	}
	if params.ProjectId != nil {
		opts.ProjectID = *params.ProjectId
	}
	sessions, err := sm.ListInfo(r.Context(), opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := make([]sessionResponse, 0, len(sessions))
	for _, si := range sessions {
		resp = append(resp, toSessionResponse(si))
	}
	writeData(w, http.StatusOK, resp)
}

func (s *Server) GetSession(w http.ResponseWriter, r *http.Request, sessionID string) {
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

	if authInfo != nil && si.UserID != authInfo.UserID {
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
	if info.UserID != "" {
		authUser, err := s.authStore.GetUser(r.Context(), info.UserID)
		if err == nil {
			resp.UserName = authUser.Username
		}
	}

	writeData(w, http.StatusOK, resp)
}

func (s *Server) GetSessionMessages(w http.ResponseWriter, r *http.Request, sessionID string, params apiserver.GetSessionMessagesParams) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}

	// Ownership check for non-admin users.
	if err := s.checkSessionAccess(w, r, sessionID); err != nil {
		return
	}

	limit := 20
	if params.Limit != nil && *params.Limit >= 0 {
		limit = *params.Limit
	}
	// skip counts serialized messages from the end, enabling backwards pagination.
	// skip=0 → last 20 messages; skip=20 → messages 20–40 from the end, etc.
	skip := 0
	if params.Skip != nil && *params.Skip >= 0 {
		skip = *params.Skip
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

	// Serialize first so that skip/limit count logical (serialized) messages,
	// matching what the frontend tracks in sessionMessagesSkip. Without this,
	// consecutive assistant rows (tool-call turns) collapse into fewer messages
	// than DB rows, causing the frontend skip cursor to drift and pages to
	// overlap or go missing.
	all := serializeDBMessages(rows)
	total := len(all)
	if limit > 0 {
		end := total - skip
		if end <= 0 {
			all = nil
		} else {
			start := max(end-limit, 0)
			all = all[start:end]
		}
	}

	writeData(w, http.StatusOK, all)
}

// checkSessionAccess verifies the current user has access to the session.
// Returns a non-nil error (and writes the HTTP response) if access is denied.
func (s *Server) checkSessionAccess(w http.ResponseWriter, r *http.Request, sessionID string) error {
	info := UserFromContext(r.Context())
	sm, ok := s.mem.(memory.SessionManager)
	if info == nil || !ok {
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

func (s *Server) GetSessionWorkspace(w http.ResponseWriter, r *http.Request, sessionID string, params apiserver.GetSessionWorkspaceParams) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return
	}
	if err := s.checkSessionAccess(w, r, sessionID); err != nil {
		return
	}
	sm, ok := s.mem.(memory.SessionManager)
	if !ok {
		writeError(w, http.StatusNotFound, "memory provider does not support sessions")
		return
	}
	info, err := sm.LoadInfo(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if info.UserID == "" || info.AgentID == "" {
		writeData(w, http.StatusOK, workspaceDiskInfo{Root: "", Paths: []string{}})
		return
	}
	userDir, err := agent.SetupUserWorkspace(info.AgentID, config.StellaHome(), info.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	root := userDir
	showHidden := params.ShowHidden != nil && *params.ShowHidden
	listPath := ""
	if params.Path != nil {
		listPath = *params.Path
	}
	depth := 2
	if params.Depth != nil && *params.Depth > 0 {
		depth = *params.Depth
	}
	diskInfo, err := collectWorkspaceDiskInfo(root, showHidden, listPath, depth)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, diskInfo)
}

type workspaceDiskInfo struct {
	Root       string   `json:"root"`
	Paths      []string `json:"paths"`
	TotalFiles int      `json:"total_files"`
	TotalDirs  int      `json:"total_dirs"`
	TotalBytes int64    `json:"total_bytes"`
}

func pathDepth(path string) int {
	return len(strings.Split(filepath.ToSlash(path), "/"))
}

func collectWorkspaceDiskInfo(root string, showHidden bool, listPath string, depth int) (workspaceDiskInfo, error) {
	info := workspaceDiskInfo{Root: root, Paths: []string{}}
	listRoot, err := safePath(root, strings.TrimSuffix(listPath, "/"))
	if err != nil {
		return workspaceDiskInfo{}, err
	}
	if stat, statErr := os.Stat(listRoot); statErr != nil {
		return workspaceDiskInfo{}, statErr
	} else if !stat.IsDir() {
		return workspaceDiskInfo{}, fmt.Errorf("path is not a directory")
	}
	err = filepath.WalkDir(listRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil //nolint:nilerr // skip unreadable entries
		}
		scopeRel, scopeRelErr := filepath.Rel(listRoot, path)
		if scopeRelErr != nil || scopeRel == "." {
			return nil //nolint:nilerr
		}
		if depth > 0 && pathDepth(scopeRel) > depth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || rel == "." {
			return nil //nolint:nilerr
		}
		name := d.Name()
		isDot := strings.HasPrefix(name, ".") || strings.Contains(rel, string(filepath.Separator)+".")
		if isDot && !showHidden {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			info.TotalDirs++
			info.Paths = append(info.Paths, filepath.ToSlash(rel)+"/")
			return nil
		}
		entryInfo, statErr := d.Info()
		if statErr != nil {
			return nil //nolint:nilerr // skip unreadable entries
		}
		info.TotalFiles++
		if entryInfo.Mode().IsRegular() {
			info.TotalBytes += entryInfo.Size()
		}
		info.Paths = append(info.Paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return workspaceDiskInfo{}, err
	}
	return info, nil
}

// sessionWorkspaceRoot resolves the workspace root for a session, checking
// access and returning (root, nil) or writing an error and returning ("", err).
func (s *Server) sessionWorkspaceRoot(w http.ResponseWriter, r *http.Request, sessionID string) (string, error) {
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing session ID")
		return "", fmt.Errorf("missing session ID")
	}
	if err := s.checkSessionAccess(w, r, sessionID); err != nil {
		return "", err
	}
	sm, ok := s.mem.(memory.SessionManager)
	if !ok {
		writeError(w, http.StatusNotFound, "memory provider does not support sessions")
		return "", fmt.Errorf("unsupported")
	}
	info, err := sm.LoadInfo(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return "", err
	}
	if info.UserID == "" || info.AgentID == "" {
		writeError(w, http.StatusNotFound, "session has no workspace")
		return "", fmt.Errorf("no workspace")
	}
	userDir, err := agent.SetupUserWorkspace(info.AgentID, config.StellaHome(), info.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return "", err
	}
	return userDir, nil
}

// safePath resolves a caller-supplied relative path to an absolute path that
// is guaranteed to stay within root. Returns an error if the result would
// escape root (directory traversal).
func safePath(root, rel string) (string, error) {
	abs := filepath.Join(root, filepath.Clean("/"+rel))
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes workspace root")
	}
	return abs, nil
}

func (s *Server) CreateWorkspaceFile(w http.ResponseWriter, r *http.Request, sessionID string) {
	root, err := s.sessionWorkspaceRoot(w, r, sessionID)
	if err != nil {
		return
	}
	var body apiserver.CreateWorkspaceFileJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	abs, err := safePath(root, body.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.IsDir != nil && *body.IsDir {
		if err := os.MkdirAll(abs, 0o755); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		content := ""
		if body.Content != nil {
			content = *body.Content
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	diskInfo, err := collectWorkspaceDiskInfo(root, false, "", 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusCreated, diskInfo)
}

func (s *Server) DeleteWorkspaceFile(w http.ResponseWriter, r *http.Request, sessionID string) {
	root, err := s.sessionWorkspaceRoot(w, r, sessionID)
	if err != nil {
		return
	}
	var body apiserver.DeleteWorkspaceFileJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	abs, err := safePath(root, body.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.RemoveAll(abs); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	diskInfo, err := collectWorkspaceDiskInfo(root, false, "", 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, diskInfo)
}

func (s *Server) MoveWorkspaceFile(w http.ResponseWriter, r *http.Request, sessionID string) {
	root, err := s.sessionWorkspaceRoot(w, r, sessionID)
	if err != nil {
		return
	}
	var body apiserver.MoveWorkspaceFileJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Path == "" || body.NewPath == "" {
		writeError(w, http.StatusBadRequest, "path and new_path are required")
		return
	}
	src, err := safePath(root, body.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	dst, err := safePath(root, body.NewPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.Rename(src, dst); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	diskInfo, err := collectWorkspaceDiskInfo(root, false, "", 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeData(w, http.StatusOK, diskInfo)
}

func (s *Server) GetWorkspaceFileContent(w http.ResponseWriter, r *http.Request, sessionID string, params apiserver.GetWorkspaceFileContentParams) {
	root, err := s.sessionWorkspaceRoot(w, r, sessionID)
	if err != nil {
		return
	}
	if params.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	abs, err := safePath(root, params.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a directory")
		return
	}
	if params.Raw != nil && *params.Raw {
		w.Header().Set("Content-Disposition", "inline")
		http.ServeFile(w, r, abs)
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Refuse binary files: check first 512 bytes for null byte.
	probe := data
	if len(probe) > 512 {
		probe = probe[:512]
	}
	if slices.Contains(probe, 0) {
		writeError(w, http.StatusBadRequest, "file appears to be binary")
		return
	}
	lang := detectLanguage(params.Path)
	writeData(w, http.StatusOK, map[string]any{
		"path":     params.Path,
		"content":  string(data),
		"language": lang,
	})
}

func (s *Server) UpdateWorkspaceFileContent(w http.ResponseWriter, r *http.Request, sessionID string) {
	root, err := s.sessionWorkspaceRoot(w, r, sessionID)
	if err != nil {
		return
	}
	var body apiserver.UpdateWorkspaceFileContentJSONRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	abs, err := safePath(root, body.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.WriteFile(abs, []byte(body.Content), 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	lang := detectLanguage(body.Path)
	writeData(w, http.StatusOK, map[string]any{
		"path":     body.Path,
		"content":  body.Content,
		"language": lang,
	})
}

func (s *Server) UploadWorkspaceFile(w http.ResponseWriter, r *http.Request, sessionID string) {
	root, err := s.sessionWorkspaceRoot(w, r, sessionID)
	if err != nil {
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart form: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing file field: "+err.Error())
		return
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	now := time.Now()
	hash := fmt.Sprintf("%06x", now.UnixNano()&0xFFFFFF)
	dir := filepath.Join(root, ".assets", now.Format("200601"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := fmt.Sprintf("%s-%s-%s", now.Format("20060102"), hash, filepath.Base(header.Filename))
	abs := filepath.Join(dir, name)
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rel, _ := filepath.Rel(root, abs)
	writeData(w, http.StatusCreated, map[string]string{"path": rel})
}

// detectLanguage returns a simple language hint based on file extension.
func detectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".py":
		return "python"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".md", ".mdx":
		return "markdown"
	case ".html":
		return "html"
	case ".css":
		return "css"
	case ".sh", ".bash":
		return "shell"
	case ".sql":
		return "sql"
	case ".toml":
		return "toml"
	case ".xml":
		return "xml"
	case ".rs":
		return "rust"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx":
		return "cpp"
	case ".java":
		return "java"
	case ".rb":
		return "ruby"
	case ".php":
		return "php"
	case ".txt":
		return "text"
	default:
		return ""
	}
}

func (s *Server) GetSessionSystemPrompt(w http.ResponseWriter, r *http.Request, sessionID string) {
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
	if info.UserID != "" && info.AgentID != "" {
		if userDir, err := agent.SetupUserWorkspace(info.AgentID, config.StellaHome(), info.UserID); err == nil {
			userRoot = userDir
		}
	}
	homeDir, _ := os.UserHomeDir()
	pluginView, err := s.pluginHost.SessionPluginView(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	promptSections, err := s.pluginHost.SystemPromptSections(r.Context(), pkgplugins.SystemPromptContext{
		StellaHome:          config.StellaHome(),
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

	systemPrompt := prompt.BuildSystemPromptFromDB(r.Context(), prompt.DBPromptParams{
		SystemPrompt: agentCfg.SystemPrompt,
		Memory:       s.mem,
		UserID:       info.UserID,
		AgentID:      info.AgentID,
		StellaHome:   config.StellaHome(),
		AgentRoot:    agentCfg.Workspace,
		UserRoot:     userRoot,
		PromptTools:  promptTools,
		Sections:     append(promptSections, s.pluginHost.ManifestPluginPrompts()...),
	})

	writeData(w, http.StatusOK, map[string]string{"system_prompt": systemPrompt})
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
		// Malformed envelope: best-effort — show raw content, no ID to match.
		m["content"] = row.Content
		m["tool_name"] = ""
		m["is_error"] = false
		return m
	}
	// Try to decode result as a plain string first; fall back to raw JSON bytes.
	var text string
	if err := json.Unmarshal(env.Result, &text); err != nil {
		text = string(env.Result)
	}
	m["tool_call_id"] = env.ID
	m["tool_name"] = env.Tool
	m["content"] = text
	m["is_error"] = env.Error != ""
	return m
}
