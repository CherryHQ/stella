package access

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/authz"
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

	out := make([]memory.RecallSearchResult, 0, min(limit, len(hits)))
	for _, hit := range hits {
		if len(out) == limit {
			break
		}
		info, conv, ok, err := a.authorizedRecallConversation(ctx, agentID, hit.SessionID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		ref := memory.RecallReference{Kind: hit.SourceType, ID: hit.SourceID, SessionID: info.ID}
		switch hit.SourceType {
		case "message":
			if _, err := a.svc.q.GetMessage(ctx, sqlc.GetMessageParams{ID: hit.SourceID, ConversationID: conv.ID}); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				return nil, fmt.Errorf("%w: verify conversation memory message: %w", ErrUnavailable, err)
			}
		case "summary":
			if _, err := a.svc.q.GetSummary(ctx, sqlc.GetSummaryParams{ID: hit.SourceID, ConversationID: conv.ID}); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				return nil, fmt.Errorf("%w: verify conversation memory summary: %w", ErrUnavailable, err)
			}
		default:
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
			Reference: ref, Content: row.Content, Role: row.Role, OccurredAt: row.CreatedAt.UTC(),
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
				Role:      message.Role, Content: message.Content, OccurredAt: message.CreatedAt.UTC(),
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
				Kind:      child.Kind, Depth: &depth, Content: child.Content, OccurredAt: summaryOccurredAt(child),
			})
			tokensUsed += tokens
		}
	}

	return memory.RecallDocument{
		Reference: ref, Content: root.Content, OccurredAt: summaryOccurredAt(root),
		SessionID: sessionID, ConversationTitle: title, Summary: detail,
	}, nil
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
