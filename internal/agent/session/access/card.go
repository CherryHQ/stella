package access

import (
	"context"
	"fmt"
	"strings"
	"time"

	agentsession "github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/tools"
)

const (
	SessionStateIdle     = "idle"
	SessionStateRunning  = "running"
	SessionStateArchived = "archived"

	// A card is orientation, not transcript retrieval. Increase this only if
	// real summaries routinely lose their recent tail; Phase 2 owns deeper reads.
	maxSessionCardSummaryBytes = 2_000
	maxSessionCardBackground   = 600
	maxSessionCardUser         = 700
	maxSessionCardAssistant    = 900
)

// Card is the reusable, model-safe Session projection. Kind remains available
// on Info for internal routing but deliberately does not cross this boundary.
type Card struct {
	ID            string
	Title         string
	Summary       string
	State         string
	Sendable      bool
	LastActive    time.Time
	TurnStartedAt time.Time
}

// ReadCard authorizes one Session and projects the same compact shape used by
// list pages.
func (a *Access) ReadCard(ctx context.Context, agentID, sessionID string) (Card, error) {
	info, err := a.Read(ctx, agentID, sessionID)
	if err != nil {
		return Card{}, err
	}
	cards, err := a.projectCards(ctx, []agentsession.Info{info})
	if err != nil {
		return Card{}, err
	}
	return cards[0], nil
}

type CardPage struct {
	Sessions   []Card
	NextOffset int
	HasMore    bool
	infos      []agentsession.Info
}

// ListCardPage preserves ListPage's authorization-aware cursor and performs
// exactly one summary-source query for the sessions returned on that page.
func (a *Access) ListCardPage(ctx context.Context, agentID string, opts agentsession.ListOptions, limit int) (CardPage, error) {
	page, err := a.ListPage(ctx, agentID, opts, limit)
	if err != nil {
		return CardPage{}, err
	}
	cards, err := a.projectCards(ctx, page.Sessions)
	if err != nil {
		return CardPage{}, err
	}
	return CardPage{Sessions: cards, NextOffset: page.NextOffset, HasMore: page.HasMore, infos: page.Sessions}, nil
}

func (a *Access) projectCards(ctx context.Context, infos []agentsession.Info) ([]Card, error) {
	if len(infos) == 0 {
		return []Card{}, nil
	}
	ids := make([]string, len(infos))
	for i := range infos {
		ids[i] = infos[i].ID
	}
	rows, err := a.svc.q.ListConversationSummarySourceBySessionIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("%w: load session summaries: %w", ErrUnavailable, err)
	}
	sources := make(map[string]sqlc.ListConversationSummarySourceBySessionIDsRow, len(rows))
	for _, row := range rows {
		sources[row.SessionID] = row
	}

	cards := make([]Card, len(infos))
	for i, info := range infos {
		source, ok := sources[info.ID]
		summary := summaryExcerpt(info.Title, maxSessionCardSummaryBytes)
		if ok {
			summary = deriveSessionSummary(info.Title, string(info.LastTurnResult), source)
		}
		state := SessionStateIdle
		if info.Archived {
			state = SessionStateArchived
		} else if a.SessionRunning(info) {
			state = SessionStateRunning
		}
		cards[i] = Card{
			ID: info.ID, Title: info.Title,
			Summary: summary,
			State:   state, Sendable: sessionSendable(info), LastActive: info.LastActive.UTC(),
			TurnStartedAt: info.LastTurnStartedAt.UTC(),
		}
	}
	return cards, nil
}

func sessionSendable(info agentsession.Info) bool {
	if info.Archived {
		return false
	}
	// Agent-originated input cannot be stored as a plain user message in a chat
	// Session. Actor provenance arrives in Phase 4, so only the pre-existing
	// managed/delegate path is callable now.
	return agentsession.Kind(info.Kind) == agentsession.KindDelegate
}

func deriveSessionSummary(title, lastTurnResult string, source sqlc.ListConversationSummarySourceBySessionIDsRow) string {
	background := summaryExcerpt(source.Background, maxSessionCardBackground)
	user := summaryExcerpt(source.LastUserMessage, maxSessionCardUser)
	assistant := summaryExcerpt(source.LastAssistantText, maxSessionCardAssistant)

	tail := user
	result := strings.TrimSpace(lastTurnResult)
	if assistant != "" {
		if result != "" {
			assistant = result + ": " + assistant
		}
		if tail != "" {
			tail += " — " + assistant
		} else {
			tail = assistant
		}
	} else if result != "" {
		if tail != "" {
			tail += " — " + result
		} else {
			tail = result
		}
	}

	parts := make([]string, 0, 2)
	if background != "" {
		parts = append(parts, background)
	}
	if tail != "" {
		parts = append(parts, tail)
	}
	summary := strings.Join(parts, " | ")
	if summary == "" && source.HasMessages {
		summary = summaryExcerpt(title, maxSessionCardSummaryBytes)
		if summary == "" {
			// All persisted rows were intentionally non-display events. Do not leak
			// them merely to fill a card, but never return an unexplained ID either.
			summary = "Session activity recorded."
		}
	}
	summary, _ = tools.TruncateText(summary, maxSessionCardSummaryBytes)
	return summary
}

func summaryExcerpt(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	value, _ = tools.TruncateText(value, limit)
	return value
}
