package simple

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vaayne/anna/internal/db/sqlc"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/memory"
)

// Compile-time interface checks.
var (
	_ memory.Provider       = (*Provider)(nil)
	_ memory.ProfileStore   = (*Provider)(nil)
	_ memory.SessionManager = (*Provider)(nil)
)

// Provider implements a minimal sliding-window memory provider.
// It stores messages in the same schema as LCM but does not write
// summaries or context items. Assemble returns the last N messages
// that fit within the token budget.
type Provider struct {
	db        *sql.DB
	q         *sqlc.Queries
	log       *slog.Logger
	sessionMu map[string]*sync.Mutex
	convCache map[string]int64
	globalMu  sync.Mutex
}

// New creates a new simple provider backed by the given database.
func New(db *sql.DB) *Provider {
	return &Provider{
		db:        db,
		q:         sqlc.New(db),
		log:       slog.Default(),
		sessionMu: make(map[string]*sync.Mutex),
		convCache: make(map[string]int64),
	}
}

// Name implements memory.Provider.
func (p *Provider) Name() string { return "simple" }

// Bootstrap implements memory.Provider.
func (p *Provider) Bootstrap(ctx context.Context, session memory.Session) error {
	_, err := p.getOrCreateConversation(ctx, session.ID)
	return err
}

// Append implements memory.Provider.
func (p *Provider) Append(ctx context.Context, session memory.Session, msgs ...ai.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	return p.withSessionLock(session.ID, func() error {
		convID, err := p.getOrCreateConversation(ctx, session.ID)
		if err != nil {
			return err
		}

		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		qtx := p.q.WithTx(tx)

		seq, err := qtx.GetMaxSeq(ctx, convID)
		if err != nil {
			return fmt.Errorf("get max seq: %w", err)
		}

		for _, msg := range msgs {
			rows := messageToRows(msg)
			for _, row := range rows {
				seq++
				_, err := qtx.CreateMessage(ctx, sqlc.CreateMessageParams{
					ConversationID: convID,
					Seq:            seq,
					Role:           row.role,
					EventType:      row.eventType,
					Content:        row.content,
					TokenCount:     int64(memory.EstimateTokens(row.content)),
				})
				if err != nil {
					return fmt.Errorf("create message: %w", err)
				}
			}
		}

		return tx.Commit()
	})
}

// Assemble implements memory.Provider.
// Returns the last N messages that fit within budget, always honouring freshTail.
func (p *Provider) Assemble(ctx context.Context, session memory.Session, budget, freshTail int) ([]ai.Message, error) {
	convID, err := p.getOrCreateConversation(ctx, session.ID)
	if err != nil {
		return nil, err
	}

	dbMsgs, err := p.q.GetMessagesByConversation(ctx, convID)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}

	allMsgs := rowsToMessages(dbMsgs)
	if len(allMsgs) == 0 {
		return nil, nil
	}

	// Sliding window: walk backwards from the end, collecting messages
	// that fit within budget. Always include at least freshTail messages.
	used := 0
	start := len(allMsgs)
	for i := len(allMsgs) - 1; i >= 0; i-- {
		tokens := estimateMessageTokens(allMsgs[i])
		tailCount := len(allMsgs) - i
		if tailCount > freshTail && used+tokens > budget {
			break
		}
		used += tokens
		start = i
	}

	return allMsgs[start:], nil
}

// Stats implements memory.Provider.
func (p *Provider) Stats(ctx context.Context, session memory.Session) (memory.SessionStats, error) {
	conv, err := p.q.GetConversationBySessionID(ctx, session.ID)
	if err == sql.ErrNoRows {
		return memory.SessionStats{}, nil
	}
	if err != nil {
		return memory.SessionStats{}, fmt.Errorf("get conversation: %w", err)
	}

	msgs, err := p.q.GetMessagesByConversation(ctx, conv.ID)
	if err != nil {
		return memory.SessionStats{}, fmt.Errorf("get messages: %w", err)
	}

	var tokenCount int64
	for _, m := range msgs {
		tokenCount += m.TokenCount
	}

	stats := memory.SessionStats{
		MessageCount: len(msgs),
		TokenCount:   int(tokenCount),
		SummaryCount: 0,
	}

	if len(msgs) > 0 {
		stats.OldestAt = parseTime(msgs[0].CreatedAt)
		stats.NewestAt = parseTime(msgs[len(msgs)-1].CreatedAt)
	}

	return stats, nil
}

// Close implements memory.Provider.
func (p *Provider) Close() error { return nil }

// --- ProfileStore ---

// GetProfile implements memory.ProfileStore.
func (p *Provider) GetProfile(ctx context.Context, userID int64, agentID string) (string, error) {
	mem, err := p.q.GetUserAgentMemory(ctx, sqlc.GetUserAgentMemoryParams{
		UserID:  userID,
		AgentID: agentID,
	})
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get profile: %w", err)
	}
	return mem.Content, nil
}

// SetProfile implements memory.ProfileStore.
func (p *Provider) SetProfile(ctx context.Context, userID int64, agentID string, content string) error {
	if err := p.q.UpsertUserAgentMemory(ctx, sqlc.UpsertUserAgentMemoryParams{
		UserID:  userID,
		AgentID: agentID,
		Content: content,
	}); err != nil {
		return fmt.Errorf("set profile: %w", err)
	}
	return nil
}

// --- SessionManager ---

// SaveInfo implements memory.SessionManager.
func (p *Provider) SaveInfo(ctx context.Context, info memory.SessionInfo) error {
	conv, err := p.q.GetConversationBySessionID(ctx, info.ID)
	if err == sql.ErrNoRows {
		lastActive := info.LastActive
		if lastActive.IsZero() {
			lastActive = time.Now().UTC()
		}
		_, err = p.q.CreateConversationFull(ctx, sqlc.CreateConversationFullParams{
			SessionID:  info.ID,
			Title:      sql.NullString{String: info.Title, Valid: info.Title != ""},
			Channel:    info.Channel,
			Archived:   boolToInt(info.Archived),
			LastActive: lastActive.UTC().Format("2006-01-02 15:04:05"),
			AgentID:    sql.NullString{String: info.AgentID, Valid: info.AgentID != ""},
			UserID:     sql.NullInt64{Int64: info.UserID, Valid: info.UserID != 0},
		})
		if err != nil {
			return fmt.Errorf("create conversation: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get conversation: %w", err)
	}

	if info.Title != "" {
		if err := p.q.UpdateConversationTitleBySessionID(ctx, sqlc.UpdateConversationTitleBySessionIDParams{
			Title:     sql.NullString{String: info.Title, Valid: true},
			SessionID: info.ID,
		}); err != nil {
			return fmt.Errorf("update title: %w", err)
		}
	}
	if boolToInt(info.Archived) != conv.Archived {
		if err := p.q.UpdateConversationArchived(ctx, sqlc.UpdateConversationArchivedParams{
			Archived:  boolToInt(info.Archived),
			SessionID: info.ID,
		}); err != nil {
			return fmt.Errorf("update archived: %w", err)
		}
	}
	if info.AgentID != "" || info.UserID != 0 {
		agentMatch := conv.AgentID.Valid && conv.AgentID.String == info.AgentID
		userMatch := conv.UserID.Valid && conv.UserID.Int64 == info.UserID
		if !agentMatch || !userMatch {
			if err := p.q.UpdateConversationAgentUser(ctx, sqlc.UpdateConversationAgentUserParams{
				AgentID:   sql.NullString{String: info.AgentID, Valid: info.AgentID != ""},
				UserID:    sql.NullInt64{Int64: info.UserID, Valid: info.UserID != 0},
				SessionID: info.ID,
			}); err != nil {
				return fmt.Errorf("update agent/user: %w", err)
			}
		}
	}

	if err := p.q.UpdateConversationLastActive(ctx, info.ID); err != nil {
		return fmt.Errorf("update last_active: %w", err)
	}
	return nil
}

// LoadInfo implements memory.SessionManager.
func (p *Provider) LoadInfo(ctx context.Context, sessionID string) (memory.SessionInfo, error) {
	conv, err := p.q.GetConversationBySessionID(ctx, sessionID)
	if err != nil {
		return memory.SessionInfo{}, fmt.Errorf("get conversation: %w", err)
	}
	return convToSessionInfo(conv), nil
}

// ListInfo implements memory.SessionManager.
func (p *Provider) ListInfo(ctx context.Context, opts memory.ListOptions) ([]memory.SessionInfo, error) {
	var convs []sqlc.CtxConversation
	var err error
	if opts.IncludeArchived {
		convs, err = p.q.ListConversationsAll(ctx)
	} else {
		convs, err = p.q.ListConversations(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}

	var result []memory.SessionInfo
	for _, c := range convs {
		info := convToSessionInfo(c)
		if opts.AgentID != "" && info.AgentID != opts.AgentID {
			continue
		}
		if opts.UserID != 0 && info.UserID != opts.UserID {
			continue
		}
		result = append(result, info)
		if opts.Limit > 0 && len(result) >= opts.Limit {
			break
		}
	}
	return result, nil
}

// LoadHistory implements memory.SessionManager.
func (p *Provider) LoadHistory(ctx context.Context, sessionID string) ([]ai.Message, error) {
	conv, err := p.q.GetConversationBySessionID(ctx, sessionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}

	msgs, err := p.q.GetMessagesByConversation(ctx, conv.ID)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}

	return rowsToMessages(msgs), nil
}

// --- Internal helpers ---

func (p *Provider) withSessionLock(sessionID string, fn func() error) error {
	p.globalMu.Lock()
	mu, ok := p.sessionMu[sessionID]
	if !ok {
		mu = &sync.Mutex{}
		p.sessionMu[sessionID] = mu
	}
	p.globalMu.Unlock()

	mu.Lock()
	defer mu.Unlock()
	return fn()
}

func (p *Provider) getOrCreateConversation(ctx context.Context, sessionID string) (int64, error) {
	p.globalMu.Lock()
	if id, ok := p.convCache[sessionID]; ok {
		p.globalMu.Unlock()
		return id, nil
	}
	p.globalMu.Unlock()

	conv, err := p.q.GetConversationBySessionID(ctx, sessionID)
	if err == nil {
		p.cacheConvID(sessionID, conv.ID)
		return conv.ID, nil
	}
	if err != sql.ErrNoRows {
		return 0, fmt.Errorf("get conversation: %w", err)
	}

	conv, err = p.q.CreateConversation(ctx, sqlc.CreateConversationParams{
		SessionID: sessionID,
	})
	if err != nil {
		return 0, fmt.Errorf("create conversation: %w", err)
	}
	p.cacheConvID(sessionID, conv.ID)
	return conv.ID, nil
}

func (p *Provider) cacheConvID(sessionID string, convID int64) {
	p.globalMu.Lock()
	p.convCache[sessionID] = convID
	p.globalMu.Unlock()
}

func convToSessionInfo(conv sqlc.CtxConversation) memory.SessionInfo {
	info := memory.SessionInfo{
		ID:       conv.SessionID,
		Channel:  conv.Channel,
		Archived: conv.Archived != 0,
	}
	if conv.Title.Valid {
		info.Title = conv.Title.String
	}
	if conv.AgentID.Valid {
		info.AgentID = conv.AgentID.String
	}
	if conv.UserID.Valid {
		info.UserID = conv.UserID.Int64
	}
	if t, err := time.Parse("2006-01-02 15:04:05", conv.CreatedAt); err == nil {
		info.CreatedAt = t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", conv.LastActive); err == nil {
		info.LastActive = t
	}
	return info
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func parseTime(s string) time.Time {
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// --- Message conversion (duplicated from LCM, simplified) ---

type storageRow struct {
	role      string
	eventType string
	content   string
}

const (
	roleUser      = "user"
	roleAssistant = "assistant"
	roleTool      = "tool"

	eventTypeText       = "text"
	eventTypeMultimodal = "multimodal"
	eventTypeToolCall   = "tool_call"
	eventTypeToolResult = "tool_result"
)

type toolCallEnvelope struct {
	ID   string          `json:"id"`
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

type toolResultEnvelope struct {
	ID     string          `json:"id"`
	Tool   string          `json:"tool"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error,omitempty"`
}

func messageToRows(msg ai.Message) []storageRow {
	switch m := msg.(type) {
	case ai.UserMessage:
		return userMessageToRows(m)
	case ai.AssistantMessage:
		return assistantMessageToRows(m)
	case ai.ToolResultMessage:
		return toolResultToRows(m)
	default:
		return nil
	}
}

func userMessageToRows(m ai.UserMessage) []storageRow {
	switch c := m.Content.(type) {
	case string:
		if c == "" {
			return nil
		}
		return []storageRow{{role: roleUser, eventType: eventTypeText, content: c}}
	case []ai.ContentBlock:
		data, err := json.Marshal(contentBlocksToJSON(c))
		if err != nil {
			return nil
		}
		return []storageRow{{role: roleUser, eventType: eventTypeMultimodal, content: string(data)}}
	default:
		s := fmt.Sprintf("%v", m.Content)
		if s == "" {
			return nil
		}
		return []storageRow{{role: roleUser, eventType: eventTypeText, content: s}}
	}
}

func assistantMessageToRows(m ai.AssistantMessage) []storageRow {
	var rows []storageRow
	var text string
	for _, block := range m.Content {
		switch b := block.(type) {
		case ai.TextContent:
			text += b.Text
		case ai.ToolCall:
			argsJSON, _ := json.Marshal(b.Arguments)
			envelope := toolCallEnvelope{ID: b.ID, Tool: b.Name, Args: argsJSON}
			data, _ := json.Marshal(envelope)
			rows = append(rows, storageRow{role: roleAssistant, eventType: eventTypeToolCall, content: string(data)})
		}
	}
	if text != "" {
		rows = append([]storageRow{{role: roleAssistant, eventType: eventTypeText, content: text}}, rows...)
	}
	return rows
}

func toolResultToRows(m ai.ToolResultMessage) []storageRow {
	text := ai.FlattenText(m.Content)
	resultJSON, _ := json.Marshal(text)
	var errStr string
	if m.IsError {
		errStr = text
	}
	envelope := toolResultEnvelope{
		ID:     m.ToolCallID,
		Tool:   m.ToolName,
		Result: resultJSON,
		Error:  errStr,
	}
	data, _ := json.Marshal(envelope)
	return []storageRow{{role: roleTool, eventType: eventTypeToolResult, content: string(data)}}
}

type contentBlockJSON struct {
	Kind     string `json:"kind"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

func contentBlocksToJSON(blocks []ai.ContentBlock) []contentBlockJSON {
	out := make([]contentBlockJSON, 0, len(blocks))
	for _, b := range blocks {
		switch b := b.(type) {
		case ai.TextContent:
			out = append(out, contentBlockJSON{Kind: "text", Text: b.Text})
		case ai.ImageContent:
			out = append(out, contentBlockJSON{Kind: "image", Data: b.Data, MimeType: b.MimeType})
		}
	}
	return out
}

func rowsToMessages(msgs []sqlc.CtxMessage) []ai.Message {
	var result []ai.Message
	i := 0
	for i < len(msgs) {
		msg := msgs[i]
		switch msg.Role {
		case roleUser:
			result = append(result, rowToUserMessage(msg))
			i++
		case roleAssistant:
			am, consumed := mergeAssistantRows(msgs, i)
			result = append(result, am)
			i += consumed
		case roleTool:
			result = append(result, rowToToolResult(msg))
			i++
		default:
			i++
		}
	}
	return result
}

func rowToUserMessage(msg sqlc.CtxMessage) ai.UserMessage {
	if msg.EventType == eventTypeMultimodal {
		var blocks []contentBlockJSON
		if json.Unmarshal([]byte(msg.Content), &blocks) == nil && len(blocks) > 0 {
			content := make([]ai.ContentBlock, 0, len(blocks))
			for _, b := range blocks {
				switch b.Kind {
				case "text":
					content = append(content, ai.TextContent{Text: b.Text})
				case "image":
					content = append(content, ai.ImageContent{Data: b.Data, MimeType: b.MimeType})
				}
			}
			return ai.UserMessage{Content: content}
		}
	}
	return ai.UserMessage{Content: msg.Content}
}

func mergeAssistantRows(msgs []sqlc.CtxMessage, start int) (ai.AssistantMessage, int) {
	var blocks []ai.ContentBlock
	consumed := 0

	msg := msgs[start]
	switch msg.EventType {
	case eventTypeText:
		blocks = append(blocks, ai.TextContent{Text: msg.Content})
		consumed++
	case eventTypeToolCall:
		if call, ok := decodeToolCall(msg.Content); ok {
			blocks = append(blocks, call)
		}
		consumed++
	default:
		blocks = append(blocks, ai.TextContent{Text: msg.Content})
		consumed++
		return ai.AssistantMessage{Content: blocks}, consumed
	}

	for start+consumed < len(msgs) {
		next := msgs[start+consumed]
		if next.Role != roleAssistant || next.EventType != eventTypeToolCall {
			break
		}
		if call, ok := decodeToolCall(next.Content); ok {
			blocks = append(blocks, call)
		}
		consumed++
	}

	return ai.AssistantMessage{Content: blocks}, consumed
}

func decodeToolCall(content string) (ai.ToolCall, bool) {
	var env toolCallEnvelope
	if err := json.Unmarshal([]byte(content), &env); err != nil {
		return ai.ToolCall{}, false
	}
	var args map[string]any
	_ = json.Unmarshal(env.Args, &args)
	return ai.ToolCall{ID: env.ID, Name: env.Tool, Arguments: args}, true
}

func rowToToolResult(msg sqlc.CtxMessage) ai.ToolResultMessage {
	var env toolResultEnvelope
	if err := json.Unmarshal([]byte(msg.Content), &env); err != nil {
		return ai.ToolResultMessage{
			Content: []ai.ContentBlock{ai.TextContent{Text: msg.Content}},
		}
	}
	var text string
	_ = json.Unmarshal(env.Result, &text)
	return ai.ToolResultMessage{
		ToolCallID: env.ID,
		ToolName:   env.Tool,
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
		IsError:    env.Error != "",
	}
}

func estimateMessageTokens(msg ai.Message) int {
	switch m := msg.(type) {
	case ai.AssistantMessage:
		total := 0
		for _, block := range m.Content {
			switch b := block.(type) {
			case ai.TextContent:
				total += memory.EstimateTokens(b.Text)
			case ai.ToolCall:
				total += memory.EstimateTokens(b.Name)
				if b.Arguments != nil {
					data, _ := json.Marshal(b.Arguments)
					total += memory.EstimateTokens(string(data))
				}
			}
		}
		return total
	default:
		return memory.EstimateTokens(memory.MessageText(msg))
	}
}
