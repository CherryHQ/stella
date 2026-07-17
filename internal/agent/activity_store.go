package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	sqlc "github.com/CherryHQ/stella/pkg/db/sqlc"
)

// AgentActivityStore is the conversation-activity read model backing the Agent
// management service: it reports the last-active time per agent for one user's
// own conversations. It owns the sqlc row and nullable-time decoding so the
// transport receives only a domain map.
type AgentActivityStore struct {
	q *sqlc.Queries
}

// NewAgentActivityStore builds the read model over the given pool.
func NewAgentActivityStore(db *pgxpool.Pool) *AgentActivityStore {
	return &AgentActivityStore{q: sqlc.New(db)}
}

// ListAgentLastActive returns agentID -> last-active UTC time for the user's
// conversations. Rows without a valid agent id or a zero/absent timestamp are
// omitted, so a caller can treat a missing key as "no activity".
func (s *AgentActivityStore) ListAgentLastActive(ctx context.Context, userID string) (map[string]time.Time, error) {
	rows, err := s.q.ListAgentConversationLastActive(ctx, pgtype.Text{String: userID, Valid: true})
	if err != nil {
		return nil, err
	}
	out := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		if !row.AgentID.Valid {
			continue
		}
		t := decodeSQLTime(row.LastActive)
		if t.IsZero() {
			continue
		}
		out[row.AgentID.String] = t
	}
	return out, nil
}

// decodeSQLTime coerces the aggregate interface{} timestamp sqlc returns for the
// MAX(...) activity column into a UTC time.Time. Empty or unparseable input
// yields the zero time (treated as "no activity" by the caller).
func decodeSQLTime(value any) time.Time {
	switch v := value.(type) {
	case nil:
		return time.Time{}
	case time.Time:
		return v.UTC()
	case string:
		return parseSQLTimeString(v)
	case []byte:
		return parseSQLTimeString(string(v))
	default:
		return parseSQLTimeString(fmt.Sprint(v))
	}
}

func parseSQLTimeString(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
