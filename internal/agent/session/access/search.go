package access

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/eventlog"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	// Search stays deliberately finite because LCM retrieval is rank/limit based,
	// not cursor based. The model-facing memory search therefore returns a bounded
	// top window instead of pretending an offset is a stable search cursor.
	maxRecallSearchResults = 100
	defaultRecallTokenCap  = 4_000
	maxRecallTokenCap      = 8_000
)

// SearchRecall implements memory.RecallSource through the Session PEP.
func (s *Service) SearchRecall(ctx context.Context, authority authz.Authority, agentID, query string, limit int) ([]memory.RecallSearchResult, error) {
	access, err := s.Begin(ctx, authority)
	if err != nil {
		return nil, err
	}
	return access.searchRecall(ctx, agentID, query, limit)
}

// ReadRecall implements memory.RecallSource through the Session PEP.
func (s *Service) ReadRecall(ctx context.Context, authority authz.Authority, agentID string, ref memory.RecallReference, tokenCap int) (memory.RecallDocument, error) {
	access, err := s.Begin(ctx, authority)
	if err != nil {
		return memory.RecallDocument{}, err
	}
	return access.readRecall(ctx, agentID, ref, tokenCap)
}

func (a *Access) searchRecall(ctx context.Context, agentID, query string, limit int) ([]memory.RecallSearchResult, error) {
	if !a.allowSessionList() {
		return nil, ErrNotFound
	}
	if err := a.allowSessionListAgent(agentID); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" || limit <= 0 {
		return nil, ErrForbidden
	}
	limit = min(limit, maxRecallSearchResults)
	userID := string(a.authority.UserID())
	if userID == "" {
		return nil, ErrForbidden
	}
	// Providers such as Simple intentionally have no transcript search lane.
	// Return an empty authorized lane so unified memory search can still search
	// durable facts and identity; operational Searcher failures remain errors.
	if a.svc.searcher == nil {
		return []memory.RecallSearchResult{}, nil
	}

	hits, err := a.svc.searcher.Search(ctx, memory.Session{UserID: userID, AgentID: agentID}, memory.SearchQuery{
		Text: query, Scope: memory.SearchScopeBoth, Limit: maxRecallSearchResults,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: search conversation memory: %w", ErrUnavailable, err)
	}
	if len(hits) == 0 {
		return []memory.RecallSearchResult{}, nil
	}
	if len(hits) > maxRecallSearchResults {
		hits = hits[:maxRecallSearchResults]
	}
	if err := a.authorizeAgent(ctx, agentID, authz.ActionRead); err != nil {
		return nil, err
	}

	sessionIDs := make([]string, 0, len(hits))
	seenSessions := make(map[string]struct{}, len(hits))
	for _, hit := range hits {
		if hit.SessionID == "" {
			continue
		}
		if _, seen := seenSessions[hit.SessionID]; seen {
			continue
		}
		seenSessions[hit.SessionID] = struct{}{}
		sessionIDs = append(sessionIDs, hit.SessionID)
	}
	conversations, err := a.svc.q.ListConversationsForRecallAccess(ctx, sessionIDs)
	if err != nil {
		return nil, fmt.Errorf("%w: batch session facts for recall: %w", ErrUnavailable, err)
	}
	type authorizedConversation struct {
		info agentsession.Info
		conv sqlc.CtxConversation
	}
	authorized := make(map[string]authorizedConversation, len(conversations))
	for _, conv := range conversations {
		info, ok := a.authorizeRecallConversationRecord(agentID, conv)
		if !ok {
			continue
		}
		authorized[conv.SessionID] = authorizedConversation{info: info, conv: conv}
	}

	type recallCandidate struct {
		hit  memory.SearchResult
		info agentsession.Info
		conv sqlc.CtxConversation
	}
	candidates := make([]recallCandidate, 0, len(hits))
	messageIDs := make([]string, 0, len(hits))
	summaryIDs := make([]string, 0, len(hits))
	for _, hit := range hits {
		authorizedConversation, ok := authorized[hit.SessionID]
		if !ok {
			continue
		}
		switch hit.SourceType {
		case "message":
			messageIDs = appendUniqueID(messageIDs, hit.SourceID)
		case "summary":
			summaryIDs = appendUniqueID(summaryIDs, hit.SourceID)
		default:
			continue
		}
		candidates = append(candidates, recallCandidate{hit: hit, info: authorizedConversation.info, conv: authorizedConversation.conv})
	}
	validResources := make(map[string]struct{}, len(candidates))
	if len(messageIDs) > 0 {
		rows, err := a.svc.q.ListRecallMessageByIDs(ctx, messageIDs)
		if err != nil {
			return nil, fmt.Errorf("%w: batch verify conversation memory messages: %w", ErrUnavailable, err)
		}
		for _, row := range rows {
			validResources[recallResourceKey("message", row.ConversationID, row.ID)] = struct{}{}
		}
	}
	if len(summaryIDs) > 0 {
		rows, err := a.svc.q.ListRecallSummaryByIDs(ctx, summaryIDs)
		if err != nil {
			return nil, fmt.Errorf("%w: batch verify conversation memory summaries: %w", ErrUnavailable, err)
		}
		for _, row := range rows {
			validResources[recallResourceKey("summary", row.ConversationID, row.ID)] = struct{}{}
		}
	}

	out := make([]memory.RecallSearchResult, 0, min(limit, len(hits)))
	for _, candidate := range candidates {
		if len(out) == limit {
			break
		}
		hit, info, conv := candidate.hit, candidate.info, candidate.conv
		ref := memory.RecallReference{Kind: hit.SourceType, ID: hit.SourceID, SessionID: info.ID}
		if _, ok := validResources[recallResourceKey(hit.SourceType, conv.ID, hit.SourceID)]; !ok {
			continue
		}
		title := hit.ConversationTitle
		if title == "" {
			title = info.Title
		}
		out = append(out, memory.RecallSearchResult{
			Reference: ref, Content: hit.Content, Score: hit.Score, OccurredAt: hit.OccurredAt.UTC(),
			SessionID: info.ID, ConversationTitle: title,
		})
	}
	return out, nil
}

func (a *Access) authorizeRecallConversationRecord(agentID string, conv sqlc.CtxConversation) (agentsession.Info, bool) {
	if !conv.UserID.Valid || !conv.AgentID.Valid || conv.AgentID.String != agentID || conv.UserID.String != string(a.authority.UserID()) || conv.GroupID.Valid || conv.GuestID.Valid {
		return agentsession.Info{}, false
	}
	info, err := agentsession.InfoFromRecord(memory.SessionInfo{
		ID: conv.SessionID, AgentID: conv.AgentID.String, UserID: conv.UserID.String,
		Channel: conv.Channel, Kind: conv.Kind, ProjectID: conv.ProjectID.String, Title: conv.Title.String,
		CreatedAt: conv.CreatedAt.UTC(), LastActive: conv.LastActive.UTC(),
		LastTurnStartedAt: conv.LastTurnStartedAt.Time.UTC(), LastTurnCompletedAt: conv.LastTurnCompletedAt.Time.UTC(),
		LastTurnResult: memory.SessionTurnResult(conv.LastTurnResult.String), LastViewedAt: conv.LastViewedAt.Time.UTC(), Archived: conv.Archived,
	})
	if err != nil || !a.allowSession(authz.ActionRead, sessionFactsFor(info, a.authority)) {
		return agentsession.Info{}, false
	}
	return info, true
}

func appendUniqueID(ids []string, id string) []string {
	if id == "" {
		return ids
	}
	if slices.Contains(ids, id) {
		return ids
	}
	return append(ids, id)
}

func recallResourceKey(kind, conversationID, id string) string {
	return kind + "\x00" + conversationID + "\x00" + id
}

func (a *Access) readRecall(ctx context.Context, agentID string, ref memory.RecallReference, tokenCap int) (memory.RecallDocument, error) {
	if ref.ID == "" || ref.SessionID == "" {
		return memory.RecallDocument{}, ErrNotFound
	}
	info, conv, ok, err := a.authorizedRecallConversation(ctx, agentID, ref.SessionID)
	if err != nil {
		return memory.RecallDocument{}, err
	}
	if !ok {
		return memory.RecallDocument{}, ErrNotFound
	}
	title := conv.Title.String
	if title == "" {
		title = info.Title
	}

	switch ref.Kind {
	case "message":
		row, err := a.svc.q.GetMessage(ctx, sqlc.GetMessageParams{ID: ref.ID, ConversationID: conv.ID})
		if errors.Is(err, pgx.ErrNoRows) {
			return memory.RecallDocument{}, ErrNotFound
		}
		if err != nil {
			return memory.RecallDocument{}, fmt.Errorf("%w: read conversation memory message: %w", ErrUnavailable, err)
		}
		return memory.RecallDocument{
			Reference: ref, Content: row.Content, Role: row.Role, Authority: recallAuthority(row.ActorType), OccurredAt: row.CreatedAt.UTC(),
			SessionID: info.ID, ConversationTitle: title,
		}, nil
	case "summary":
		return a.readRecallSummary(ctx, info.ID, conv.ID, title, ref, tokenCap)
	default:
		return memory.RecallDocument{}, ErrNotFound
	}
}

func (a *Access) authorizedRecallConversation(ctx context.Context, agentID, sessionID string) (info agentsession.Info, conv sqlc.CtxConversation, ok bool, err error) {
	if sessionID == "" {
		return info, conv, false, nil
	}
	info, err = a.Read(ctx, agentID, sessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
			return agentsession.Info{}, sqlc.CtxConversation{}, false, nil
		}
		return agentsession.Info{}, sqlc.CtxConversation{}, false, err
	}
	// Recall is deliberately narrower than administrator exact-ID inspection:
	// it is always the current owner's private corpus for this exact Agent.
	if info.UserID != string(a.authority.UserID()) || info.AgentID != agentID || info.GroupID != "" {
		return agentsession.Info{}, sqlc.CtxConversation{}, false, nil
	}
	conv, err = a.conversation(ctx, info)
	if err != nil {
		return agentsession.Info{}, sqlc.CtxConversation{}, false, err
	}
	return info, conv, true, nil
}

func (a *Access) readRecallSummary(ctx context.Context, sessionID, conversationID, title string, ref memory.RecallReference, tokenCap int) (memory.RecallDocument, error) {
	root, err := a.svc.q.GetSummary(ctx, sqlc.GetSummaryParams{ID: ref.ID, ConversationID: conversationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return memory.RecallDocument{}, ErrNotFound
	}
	if err != nil {
		return memory.RecallDocument{}, fmt.Errorf("%w: read conversation memory summary: %w", ErrUnavailable, err)
	}
	// Compaction writes (summary_id=container, parent_summary_id=constituent).
	// The query names predate the conceptual API names and are therefore
	// inverted: GetSummaryChildren returns containers, while GetSummaryParents
	// returns constituents.
	parents, err := a.svc.q.GetSummaryChildren(ctx, root.ID)
	if err != nil {
		return memory.RecallDocument{}, fmt.Errorf("%w: read summary parents: %w", ErrUnavailable, err)
	}
	children, err := a.svc.q.GetSummaryParents(ctx, root.ID)
	if err != nil {
		return memory.RecallDocument{}, fmt.Errorf("%w: read summary children: %w", ErrUnavailable, err)
	}
	if !summariesBelongToConversation(parents, conversationID) || !summariesBelongToConversation(children, conversationID) {
		return memory.RecallDocument{}, fmt.Errorf("%w: summary lineage crosses conversation boundary", ErrUnavailable)
	}

	detail := &memory.RecallSummaryDetail{
		Kind: root.Kind, Depth: int(root.Depth), DescendantCount: int(root.DescendantCount),
		EarliestAt: timePtr(root.EarliestAt), LatestAt: timePtr(root.LatestAt),
		Parents:  make([]memory.RecallReference, 0, len(parents)),
		Children: make([]memory.RecallReference, 0, len(children)),
	}
	for _, parent := range parents {
		detail.Parents = append(detail.Parents, memory.RecallReference{Kind: "summary", ID: parent.ID, SessionID: sessionID})
	}
	for _, child := range children {
		detail.Children = append(detail.Children, memory.RecallReference{Kind: "summary", ID: child.ID, SessionID: sessionID})
	}

	if tokenCap <= 0 {
		tokenCap = defaultRecallTokenCap
	}
	tokenCap = min(tokenCap, maxRecallTokenCap)
	tokensUsed := 0
	if root.Kind == "leaf" {
		messages, err := a.svc.q.GetSummaryMessages(ctx, root.ID)
		if err != nil {
			return memory.RecallDocument{}, fmt.Errorf("%w: expand summary messages: %w", ErrUnavailable, err)
		}
		for _, message := range messages {
			if message.ConversationID != conversationID {
				return memory.RecallDocument{}, fmt.Errorf("%w: summary message crosses conversation boundary", ErrUnavailable)
			}
		}
		for _, message := range messages {
			tokens := memory.EstimateTokens(message.Content)
			if tokensUsed+tokens > tokenCap && len(detail.Expanded) > 0 {
				break
			}
			detail.Expanded = append(detail.Expanded, memory.RecallFragment{
				Reference: memory.RecallReference{Kind: "message", ID: message.ID, SessionID: sessionID},
				Role:      message.Role, Authority: recallAuthority(message.ActorType), Content: message.Content, OccurredAt: message.CreatedAt.UTC(),
			})
			tokensUsed += tokens
		}
	} else {
		for _, child := range children {
			tokens := memory.EstimateTokens(child.Content)
			if tokensUsed+tokens > tokenCap && len(detail.Expanded) > 0 {
				break
			}
			depth := int(child.Depth)
			detail.Expanded = append(detail.Expanded, memory.RecallFragment{
				Reference: memory.RecallReference{Kind: "summary", ID: child.ID, SessionID: sessionID},
				Kind:      child.Kind, Depth: &depth, Authority: summaryAuthority(child), Content: child.Content, OccurredAt: summaryOccurredAt(child),
			})
			tokensUsed += tokens
		}
	}

	return memory.RecallDocument{
		Reference: ref, Content: root.Content, Authority: summaryAuthority(root), OccurredAt: summaryOccurredAt(root),
		SessionID: sessionID, ConversationTitle: title, Summary: detail,
	}, nil
}

func recallAuthority(actorType string) string {
	if eventlog.ActorType(actorType) == eventlog.ActorAgent {
		return "information_only"
	}
	return ""
}

func summaryAuthority(summary sqlc.CtxSummary) string {
	if summary.ContainsNonPrincipalInput {
		return "information_only"
	}
	return ""
}

func summariesBelongToConversation(summaries []sqlc.CtxSummary, conversationID string) bool {
	for _, summary := range summaries {
		if summary.ConversationID != conversationID {
			return false
		}
	}
	return true
}

func summaryOccurredAt(summary sqlc.CtxSummary) time.Time {
	if summary.LatestAt.Valid {
		return summary.LatestAt.Time.UTC()
	}
	return summary.CreatedAt.UTC()
}
