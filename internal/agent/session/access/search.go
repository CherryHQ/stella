package access

import (
	"context"
	"errors"
	"fmt"
	"strings"

	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	// Search stays deliberately finite because the existing LCM retrieval API is
	// rank/limit based rather than cursor based. Upgrade to keyset paging if real
	// users routinely need relevant hits beyond the first 100 storage results.
	maxSessionSearchResults = 100
	maxSessionMatchBytes    = 1_000
)

type TranscriptAnchor struct {
	Seq int64
}

type MatchedCard struct {
	Card   Card
	Match  string
	Anchor *TranscriptAnchor
}

type MatchedCardPage struct {
	Sessions   []MatchedCard
	NextOffset int
	HasMore    bool
}

// FindCardPage reuses the memory provider's cross-session BM25/RRF retrieval,
// but turns storage hits into policy-checked Session cards. Neither source type
// nor summary/message identifiers cross this boundary.
func (a *Access) FindCardPage(ctx context.Context, agentID, query string, includeArchived bool, offset, limit int) (MatchedCardPage, error) {
	if a.svc.searcher == nil {
		return MatchedCardPage{}, fmt.Errorf("%w: session transcript search is unavailable", ErrUnavailable)
	}
	if !a.allowSessionList() {
		return MatchedCardPage{}, ErrNotFound
	}
	if err := a.allowSessionListAgent(agentID); err != nil {
		return MatchedCardPage{}, err
	}
	userID := string(a.authority.UserID())
	if userID == "" {
		return MatchedCardPage{}, ErrForbidden
	}

	hits, err := a.svc.searcher.Search(ctx, memory.Session{UserID: userID, AgentID: agentID}, memory.SearchQuery{
		Text: query, Scope: memory.SearchScopeBoth, Limit: maxSessionSearchResults,
	})
	if err != nil {
		return MatchedCardPage{}, fmt.Errorf("%w: search session transcripts: %w", ErrUnavailable, err)
	}

	type visibleHit struct {
		info agentsession.Info
		hit  memory.SearchResult
	}
	visible := make([]visibleHit, 0, len(hits))
	seen := make(map[string]struct{}, len(hits))
	// Deliberate ceiling: LCM caps this policy loop at 100 Read calls, and
	// anchor resolution below at one query per returned card. Replace both with
	// batched scoped loads when query-bearing find latency becomes material or
	// the retrieval ceiling needs to grow beyond 100 hits.
	for _, hit := range hits {
		if hit.SessionID == "" {
			continue
		}
		if _, ok := seen[hit.SessionID]; ok {
			continue
		}
		info, readErr := a.Read(ctx, agentID, hit.SessionID)
		if readErr != nil {
			if errors.Is(readErr, ErrNotFound) || errors.Is(readErr, ErrForbidden) {
				continue
			}
			return MatchedCardPage{}, readErr
		}
		// Search is stricter than generic administrator exact-ID inspection: it is
		// always the current principal's private corpus for this exact Agent.
		if info.UserID != userID || info.AgentID != agentID || info.GroupID != "" || (!includeArchived && info.Archived) {
			continue
		}
		seen[hit.SessionID] = struct{}{}
		visible = append(visible, visibleHit{info: info, hit: hit})
	}
	if offset >= len(visible) {
		return MatchedCardPage{Sessions: []MatchedCard{}}, nil
	}
	end := min(offset+limit, len(visible))
	pageHits := visible[offset:end]
	infos := make([]agentsession.Info, len(pageHits))
	for i := range pageHits {
		infos[i] = pageHits[i].info
	}
	cards, err := a.projectCards(ctx, infos)
	if err != nil {
		return MatchedCardPage{}, err
	}
	items := make([]MatchedCard, 0, len(cards))
	for i, card := range cards {
		// Search and anchor lookup are separate reads. Rotation, compaction, or
		// deletion may invalidate one source between them; the card and excerpt
		// remain useful, so only the optional around-match cursor is dropped.
		var anchor *TranscriptAnchor
		if resolved, anchorErr := a.transcriptAnchorForSearchHit(ctx, pageHits[i].info, pageHits[i].hit); anchorErr == nil {
			anchor = &resolved
		}
		match := strings.ReplaceAll(strings.ReplaceAll(pageHits[i].hit.Content, "<b>", ""), "</b>", "")
		items = append(items, MatchedCard{Card: card, Match: summaryExcerpt(match, maxSessionMatchBytes), Anchor: anchor})
	}
	return MatchedCardPage{Sessions: items, NextOffset: end, HasMore: end < len(visible)}, nil
}

func (a *Access) transcriptAnchorForSearchHit(ctx context.Context, info agentsession.Info, hit memory.SearchResult) (TranscriptAnchor, error) {
	conv, err := a.conversation(ctx, info)
	if err != nil {
		return TranscriptAnchor{}, err
	}
	switch hit.SourceType {
	case "message":
		message, err := a.svc.q.GetMessage(ctx, sqlc.GetMessageParams{ID: hit.SourceID, ConversationID: conv.ID})
		if err != nil {
			return TranscriptAnchor{}, fmt.Errorf("%w: resolve session search match: %w", ErrUnavailable, err)
		}
		return TranscriptAnchor{Seq: message.Seq}, nil
	case "summary":
		if _, err := a.svc.q.GetSummary(ctx, sqlc.GetSummaryParams{ID: hit.SourceID, ConversationID: conv.ID}); err != nil {
			return TranscriptAnchor{}, fmt.Errorf("%w: resolve session search summary: %w", ErrUnavailable, err)
		}
		from, to, err := a.summaryMessageSeqRange(ctx, hit.SourceID)
		if err != nil {
			return TranscriptAnchor{}, err
		}
		if from <= 0 || to < from {
			return TranscriptAnchor{}, fmt.Errorf("%w: session search summary has no transcript range", ErrUnavailable)
		}
		return TranscriptAnchor{Seq: int64(to)}, nil
	default:
		return TranscriptAnchor{}, fmt.Errorf("%w: unsupported session search result", ErrUnavailable)
	}
}
