package access

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

var ErrSummaryNotFound = errors.New("session summary not found")

type MessageListInput struct {
	AgentID   string
	SessionID string
	Limit     int
	Skip      int
	After     *string
	Before    *string
	SeqFrom   *int
	SeqTo     *int
}

type TranscriptPageInput struct {
	AgentID   string
	SessionID string
	AnchorSeq int64
	// SnapshotSeq caps the visible transcript before logical turns are formed.
	// Zero keeps the live view used by find cursors.
	SnapshotSeq int64
	Offset      int
	Limit       int
}

type Message struct {
	ID              string
	Seq             int64
	Role            string
	EventType       string
	Content         string
	Parts           []MessagePart
	TokenCount      int64
	CreatedAt       time.Time
	ActorType       string
	ActorID         string
	SourceSessionID string
}

// MessagePart is the transcript-safe projection of an ordered durable part.
// Image parts intentionally expose only an authenticated media reference.
type MessagePart struct {
	Type     string
	Text     string
	MediaID  string
	MimeType string
}

// Media is one authorized immutable media object ready for an HTTP response.
// Its digest is retained solely to form an ETag; no storage path is exposed.
type Media struct {
	ID       string
	MimeType string
	SHA256   [sha256.Size]byte
	Data     []byte
}

type ContextItemListInput struct {
	AgentID   string
	SessionID string
	PageSize  int
	Offset    int
}

type ContextItemPage struct {
	Items         []ContextItem
	Meta          ContextMeta
	NextOffset    int
	HasNextOffset bool
}

type ContextMeta struct {
	MessageCount     int
	SourceTokenCount int
	ActiveTokenCount int
	SummaryDepth     int
}

type ContextItem struct {
	Ordinal   int
	EventType *string
	Message   *ContextMessage
	Summary   *Summary
}

type ContextMessage struct {
	ID              string
	Seq             int
	Role            string
	EventType       *string
	Content         *string
	Timestamp       time.Time
	TokenCount      int
	ActorType       string
	ActorID         string
	SourceSessionID string
}

type Summary struct {
	ID                      string
	Kind                    string
	Depth                   int
	Content                 string
	TokenCount              int
	EarliestAt              *time.Time
	LatestAt                *time.Time
	DescendantCount         int
	DescendantTokenCount    int
	SourceMessageTokenCount int
	CreatedAt               time.Time
}

type SummaryDetail struct {
	Summary        Summary
	Children       []Summary
	MessageSeqFrom int
	MessageSeqTo   int
}

// ListMessages authorizes the session once, then loads transcript rows for it.
func (a *Access) ListMessages(ctx context.Context, in MessageListInput) ([]Message, error) {
	info, err := a.Read(ctx, in.AgentID, in.SessionID)
	if err != nil {
		return nil, err
	}
	conv, err := a.conversation(ctx, info)
	if err != nil {
		return nil, err
	}
	limit := 20
	if in.Limit >= 0 {
		limit = in.Limit
	}
	skip := max(in.Skip, 0)
	var rows []sqlc.CtxMessage
	switch {
	case in.SeqFrom != nil || in.SeqTo != nil:
		if in.SeqFrom == nil || in.SeqTo == nil || *in.SeqFrom <= 0 || *in.SeqTo < *in.SeqFrom {
			return nil, ErrInvalid
		}
		rows, err = a.svc.q.GetMessagesByConversationRange(ctx, sqlc.GetMessagesByConversationRangeParams{ConversationID: conv.ID, Seq: int64(*in.SeqFrom), Seq_2: int64(*in.SeqTo)})
	case limit > 0:
		var pageRows []sqlc.ListMessagesByLogicalPageRow
		pageRows, err = a.svc.q.ListMessagesByLogicalPage(ctx, sqlc.ListMessagesByLogicalPageParams{ConversationID: conv.ID, After: nullTimeFromStringPtr(in.After), Before: nullTimeFromStringPtr(in.Before), Limit: int32(limit), Offset: int32(skip)})
		rows = logicalPageRowsToMessages(pageRows)
	default:
		rows, err = a.svc.q.GetMessagesByConversation(ctx, conv.ID)
		rows = filterMessageRowsByTime(rows, in.After, in.Before)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: list session messages: %w", ErrUnavailable, err)
	}
	partsByMessage, err := a.loadTranscriptParts(ctx, rows)
	if err != nil {
		return nil, err
	}
	return messagesFromRows(rows, partsByMessage), nil
}

// ListTranscriptPage reads whole user turns from newest to oldest. AnchorSeq
// selects the turn containing (or immediately preceding) a find match; zero
// starts from the latest turn.
func (a *Access) ListTranscriptPage(ctx context.Context, in TranscriptPageInput) ([]Message, error) {
	info, err := a.Read(ctx, in.AgentID, in.SessionID)
	if err != nil {
		return nil, err
	}
	conv, err := a.conversation(ctx, info)
	if err != nil {
		return nil, err
	}
	anchor := pgtype.Int8{}
	if in.AnchorSeq > 0 {
		anchor = pgtype.Int8{Int64: in.AnchorSeq, Valid: true}
	}
	snapshot := pgtype.Int8{}
	if in.SnapshotSeq > 0 {
		snapshot = pgtype.Int8{Int64: in.SnapshotSeq, Valid: true}
	}
	rows, err := a.svc.q.ListSessionTranscriptPage(ctx, sqlc.ListSessionTranscriptPageParams{
		ConversationID: conv.ID,
		AnchorSeq:      anchor,
		SnapshotSeq:    snapshot,
		Limit:          int32(in.Limit),
		Offset:         int32(in.Offset),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: list session transcript: %w", ErrUnavailable, err)
	}
	messages := make([]sqlc.CtxMessage, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, sqlc.CtxMessage(row))
	}
	partsByMessage, err := a.loadTranscriptParts(ctx, messages)
	if err != nil {
		return nil, err
	}
	return messagesFromRows(messages, partsByMessage), nil
}

// ReadMedia authorizes the routed session before resolving a media reference
// that is actually attached to it. Blob failures are unavailable, never an
// authorization-shaped success.
func (a *Access) ReadMedia(ctx context.Context, agentID, sessionID, mediaID string) (Media, error) {
	info, err := a.Read(ctx, agentID, sessionID)
	if err != nil {
		return Media{}, err
	}
	id, err := uuid.Parse(mediaID)
	if err != nil {
		return Media{}, ErrNotFound
	}
	userID, err := uuid.Parse(info.UserID)
	if err != nil {
		return Media{}, fmt.Errorf("%w: invalid session media owner", ErrUnavailable)
	}
	row, err := a.svc.q.GetMediaForSession(ctx, sqlc.GetMediaForSessionParams{
		MediaID: id.String(), UserID: userID.String(), SessionID: info.ID,
		AgentID: pgtype.Text{String: info.AgentID, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Media{}, ErrNotFound
	}
	if err != nil {
		return Media{}, fmt.Errorf("%w: get session media: %w", ErrUnavailable, err)
	}
	if len(row.Sha256) != sha256.Size || row.SizeBytes <= 0 || strings.TrimSpace(row.MimeType) == "" {
		return Media{}, fmt.Errorf("%w: invalid session media metadata", ErrUnavailable)
	}
	var digest [sha256.Size]byte
	copy(digest[:], row.Sha256)
	data, err := a.svc.assets.SessionMedia().OpenSessionMedia(ctx, userID, digest, row.SizeBytes)
	if err != nil {
		return Media{}, fmt.Errorf("%w: open session media: %w", ErrUnavailable, err)
	}
	return Media{ID: row.ID, MimeType: row.MimeType, SHA256: digest, Data: data}, nil
}

// ListContextItems authorizes the session once, then loads materialized context
// items. Old conversations without ctx_item rows fall back to raw messages.
func (a *Access) ListContextItems(ctx context.Context, in ContextItemListInput) (ContextItemPage, error) {
	info, err := a.Read(ctx, in.AgentID, in.SessionID)
	if err != nil {
		return ContextItemPage{}, err
	}
	conv, err := a.conversation(ctx, info)
	if err != nil {
		return ContextItemPage{}, err
	}
	pageSize := in.PageSize
	if pageSize <= 0 {
		pageSize = 100
	}
	if pageSize > 500 {
		pageSize = 500
	}
	offset := max(in.Offset, 0)
	rows, err := a.svc.q.ListContextItemsPage(ctx, sqlc.ListContextItemsPageParams{ConversationID: conv.ID, LimitCount: int32(pageSize + 1), OffsetCount: int32(offset)})
	if err != nil {
		return ContextItemPage{}, fmt.Errorf("%w: list context items: %w", ErrUnavailable, err)
	}
	usingMessageFallback := false
	hasNext := len(rows) > pageSize
	if hasNext {
		rows = rows[:pageSize]
	}
	items := contextItemsFromRows(rows)
	if len(rows) == 0 {
		count, err := a.svc.q.GetContextItemCount(ctx, conv.ID)
		if err != nil {
			return ContextItemPage{}, fmt.Errorf("%w: count context items: %w", ErrUnavailable, err)
		}
		if count == 0 {
			messages, err := a.svc.q.GetMessagesByConversation(ctx, conv.ID)
			if err != nil {
				return ContextItemPage{}, fmt.Errorf("%w: list fallback context messages: %w", ErrUnavailable, err)
			}
			items = contextItemsFromMessages(messages)
			if offset < len(items) {
				items = items[offset:]
			} else {
				items = nil
			}
			usingMessageFallback = true
		}
	}
	stats, err := a.svc.q.GetContextStats(ctx, conv.ID)
	if err != nil {
		return ContextItemPage{}, fmt.Errorf("%w: get context stats: %w", ErrUnavailable, err)
	}
	page := ContextItemPage{Items: items, Meta: ContextMeta{MessageCount: int(stats.MessageCount), SourceTokenCount: int(stats.SourceTokenCount), ActiveTokenCount: int(stats.ActiveTokenCount), SummaryDepth: int(stats.SummaryDepth)}}
	if usingMessageFallback {
		page.Meta.ActiveTokenCount = int(stats.SourceTokenCount)
	}
	if usingMessageFallback && len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		hasNext = true
	}
	if hasNext {
		page.NextOffset = offset + pageSize
		page.HasNextOffset = true
	}
	return page, nil
}

// GetSummary authorizes the session once, then loads a summary detail within
// that session's conversation.
func (a *Access) GetSummary(ctx context.Context, agentID, sessionID, summaryID string) (SummaryDetail, error) {
	info, err := a.Read(ctx, agentID, sessionID)
	if err != nil {
		return SummaryDetail{}, err
	}
	conv, err := a.conversation(ctx, info)
	if err != nil {
		return SummaryDetail{}, err
	}
	summary, err := a.svc.q.GetSummary(ctx, sqlc.GetSummaryParams{ID: summaryID, ConversationID: conv.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SummaryDetail{}, ErrSummaryNotFound
		}
		return SummaryDetail{}, fmt.Errorf("%w: get summary: %w", ErrUnavailable, err)
	}
	children, err := a.svc.q.GetSummaryParents(ctx, summaryID)
	if err != nil {
		return SummaryDetail{}, fmt.Errorf("%w: list summary children: %w", ErrUnavailable, err)
	}
	from, to, err := a.summaryMessageSeqRange(ctx, summaryID)
	if err != nil {
		return SummaryDetail{}, err
	}
	out := SummaryDetail{Summary: summaryFromRow(summary), Children: make([]Summary, 0, len(children)), MessageSeqFrom: from, MessageSeqTo: to}
	for _, child := range children {
		out.Children = append(out.Children, summaryFromRow(child))
	}
	return out, nil
}

func (a *Access) conversation(ctx context.Context, info agentsession.Info) (sqlc.CtxConversation, error) {
	conv, err := a.svc.q.GetConversationBySessionID(ctx, sqlc.GetConversationBySessionIDParams{SessionID: info.ID, UserID: pgtype.Text{String: info.UserID, Valid: true}, AgentID: pgtype.Text{String: info.AgentID, Valid: true}})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.CtxConversation{}, ErrNotFound
		}
		return sqlc.CtxConversation{}, fmt.Errorf("%w: get session conversation: %w", ErrUnavailable, err)
	}
	return conv, nil
}

func (a *Access) summaryMessageSeqRange(ctx context.Context, summaryID string) (int, int, error) {
	from, to := 0, 0
	queue := []string{summaryID}
	seen := map[string]bool{}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		r, err := a.svc.q.GetSummaryMessageSeqRange(ctx, id)
		if err != nil {
			return 0, 0, fmt.Errorf("%w: get summary message range: %w", ErrUnavailable, err)
		}
		if r.MessageSeqFrom > 0 {
			if from == 0 || int(r.MessageSeqFrom) < from {
				from = int(r.MessageSeqFrom)
			}
			if int(r.MessageSeqTo) > to {
				to = int(r.MessageSeqTo)
			}
		}
		kids, err := a.svc.q.GetSummaryParents(ctx, id)
		if err != nil {
			return 0, 0, fmt.Errorf("%w: list summary descendants: %w", ErrUnavailable, err)
		}
		for _, k := range kids {
			queue = append(queue, k.ID)
		}
	}
	return from, to, nil
}

func nullTimeFromStringPtr(p *string) pgtype.Timestamptz {
	if p == nil {
		return pgtype.Timestamptz{}
	}
	t := parseTime(*p)
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t
	}
	return time.Time{}
}

func logicalPageRowsToMessages(rows []sqlc.ListMessagesByLogicalPageRow) []sqlc.CtxMessage {
	out := make([]sqlc.CtxMessage, 0, len(rows))
	for _, row := range rows {
		out = append(out, sqlc.CtxMessage(row))
	}
	return out
}

func filterMessageRowsByTime(rows []sqlc.CtxMessage, after, before *string) []sqlc.CtxMessage {
	if after == nil && before == nil {
		return rows
	}
	var afterT, beforeT time.Time
	if after != nil {
		afterT = parseTime(*after)
	}
	if before != nil {
		beforeT = parseTime(*before)
	}
	filtered := rows[:0]
	for _, row := range rows {
		if !afterT.IsZero() && row.CreatedAt.Before(afterT) {
			continue
		}
		if !beforeT.IsZero() && row.CreatedAt.After(beforeT) {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}

func (a *Access) loadTranscriptParts(ctx context.Context, rows []sqlc.CtxMessage) (map[string][]MessagePart, error) {
	result := make(map[string][]MessagePart)
	ids := messageIDsThatCanHaveParts(rows)
	if len(ids) == 0 {
		return result, nil
	}
	parts, err := a.svc.q.GetMessagePartsByMessages(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("%w: get session message parts: %w", ErrUnavailable, err)
	}
	mediaRows, err := a.svc.q.ListMessagePartsWithMediaByMessages(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("%w: get session message media: %w", ErrUnavailable, err)
	}
	mediaByPartID := make(map[string]sqlc.CtxMedium, len(mediaRows))
	for _, row := range mediaRows {
		mediaByPartID[row.CtxMessagePart.ID] = row.CtxMedium
	}
	for _, part := range parts {
		switch part.PartType {
		case "text":
			result[part.MessageID] = append(result[part.MessageID], MessagePart{Type: "text", Text: part.TextContent.String})
		case "image":
			projection := part.TextContent.String
			media, ok := mediaByPartID[part.ID]
			if !ok || !part.MediaID.Valid || media.ID != part.MediaID.String || strings.TrimSpace(media.MimeType) == "" {
				result[part.MessageID] = append(result[part.MessageID], MessagePart{Type: "text", Text: projection})
				continue
			}
			result[part.MessageID] = append(result[part.MessageID], MessagePart{Type: "image", MediaID: media.ID, MimeType: media.MimeType})
		}
	}
	return result, nil
}

func messageIDsThatCanHaveParts(rows []sqlc.CtxMessage) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if (row.Role == "user" && row.EventType == "multimodal") || (row.Role == "tool" && row.EventType == "tool_result") {
			ids = append(ids, row.ID)
		}
	}
	return ids
}

func messagesFromRows(rows []sqlc.CtxMessage, partsByMessage map[string][]MessagePart) []Message {
	out := make([]Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, messageFromRow(row, partsByMessage[row.ID]))
	}
	return out
}

func messageFromRow(row sqlc.CtxMessage, parts []MessagePart) Message {
	return Message{
		ID: row.ID, Seq: row.Seq, Role: row.Role, EventType: row.EventType, Content: row.Content,
		Parts: parts, TokenCount: row.TokenCount, CreatedAt: row.CreatedAt.UTC(), ActorType: row.ActorType,
		ActorID: row.ActorID.String, SourceSessionID: row.SourceSessionID.String,
	}
}

func contextItemsFromMessages(messages []sqlc.CtxMessage) []ContextItem {
	items := make([]ContextItem, 0, len(messages))
	for i, msg := range messages {
		content := msg.Content
		eventType := msg.EventType
		items = append(items, ContextItem{Ordinal: i + 1, EventType: stringPtr(msg.EventType), Message: &ContextMessage{
			ID: msg.ID, Seq: int(msg.Seq), Role: msg.Role, EventType: &eventType, Content: &content,
			Timestamp: msg.CreatedAt.UTC(), TokenCount: int(msg.TokenCount), ActorType: msg.ActorType,
			ActorID: msg.ActorID.String, SourceSessionID: msg.SourceSessionID.String,
		}})
	}
	return items
}

func contextItemsFromRows(rows []sqlc.ListContextItemsPageRow) []ContextItem {
	items := make([]ContextItem, 0, len(rows))
	for _, row := range rows {
		if item, ok := contextItemFromRow(row); ok {
			items = append(items, item)
		}
	}
	return items
}

func contextItemFromRow(row sqlc.ListContextItemsPageRow) (ContextItem, bool) {
	item := ContextItem{Ordinal: int(row.Ordinal), EventType: stringPtr(row.EventType)}
	switch row.ItemType {
	case "message":
		if !row.MessageID.Valid || !row.MessageSeq.Valid || !row.MessageRole.Valid {
			return item, false
		}
		item.Message = &ContextMessage{
			ID: row.MessageID.String, Seq: int(row.MessageSeq.Int64), Role: row.MessageRole.String,
			EventType: textPtr(row.MessageEventType), Content: textPtr(row.MessageContent), Timestamp: row.MessageCreatedAt.Time.UTC(),
			TokenCount: int(row.MessageTokenCount.Int64), ActorType: row.MessageActorType.String,
			ActorID: row.MessageActorID.String, SourceSessionID: row.MessageSourceSessionID.String,
		}
		return item, true
	case "summary":
		if !row.SummaryID.Valid {
			return item, false
		}
		item.Summary = &Summary{ID: row.SummaryID.String, Kind: row.SummaryKind.String, Depth: int(row.SummaryDepth.Int64), Content: row.SummaryContent.String, TokenCount: int(row.SummaryTokenCount.Int64), EarliestAt: timePtr(row.SummaryEarliestAt), LatestAt: timePtr(row.SummaryLatestAt), DescendantCount: int(row.SummaryDescendantCount.Int64), DescendantTokenCount: int(row.SummaryDescendantTokenCount.Int64), SourceMessageTokenCount: int(row.SummarySourceMessageTokenCount.Int64), CreatedAt: row.SummaryCreatedAt.Time.UTC()}
		return item, true
	default:
		return item, false
	}
}

func summaryFromRow(s sqlc.CtxSummary) Summary {
	return Summary{ID: s.ID, Kind: s.Kind, Depth: int(s.Depth), Content: s.Content, TokenCount: int(s.TokenCount), EarliestAt: timePtr(s.EarliestAt), LatestAt: timePtr(s.LatestAt), DescendantCount: int(s.DescendantCount), DescendantTokenCount: int(s.DescendantTokenCount), SourceMessageTokenCount: int(s.SourceMessageTokenCount), CreatedAt: s.CreatedAt.UTC()}
}

func textPtr(ns pgtype.Text) *string {
	if !ns.Valid {
		return nil
	}
	return &ns.String
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func timePtr(nt pgtype.Timestamptz) *time.Time {
	if !nt.Valid {
		return nil
	}
	t := nt.Time.UTC()
	return &t
}
