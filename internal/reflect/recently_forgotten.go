package reflect

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const (
	// RecentlyForgottenKindKnowledge identifies Reflect-owned world facts that
	// the usage curator deprecated.
	RecentlyForgottenKindKnowledge = "knowledge"
	defaultRecentlyForgottenLimit  = int32(100)
)

// RecentlyForgottenQuery scopes the internal/admin listing to one user-agent
// pair. Empty Kind returns Knowledge.
type RecentlyForgottenQuery struct {
	UserID  string
	AgentID string
	Kind    string
	Limit   int32
}

// RecentlyForgottenItems contains the restorable curator-deprecated records.
type RecentlyForgottenItems struct {
	Knowledge []RecentlyForgottenKnowledgeItem
}

// RecentlyForgottenKnowledgeItem includes fact content so an admin can inspect
// exactly what would be restored.
type RecentlyForgottenKnowledgeItem struct {
	Kind                        string
	FactID                      string
	Content                     string
	DeprecatedAt                time.Time
	CuratorRule                 string
	LastUsedAt                  string
	MemoryVersionAfterDeprecate int64
	DeprecatedChangelogID       string
}

// SQLRecentlyForgottenStore lists curator-deprecated Reflect records that are
// eligible for internal/admin restore.
type SQLRecentlyForgottenStore struct {
	q *sqlc.Queries
}

func NewSQLRecentlyForgottenStore(q *sqlc.Queries) SQLRecentlyForgottenStore {
	return SQLRecentlyForgottenStore{q: q}
}

// ListRecentlyForgotten returns only records whose latest deprecate changelog
// was written by the usage curator.
func (s SQLRecentlyForgottenStore) ListRecentlyForgotten(ctx context.Context, query RecentlyForgottenQuery) (RecentlyForgottenItems, error) {
	if s.q == nil {
		return RecentlyForgottenItems{}, fmt.Errorf("recently forgotten: sql queries are required")
	}
	limit := query.Limit
	if limit <= 0 {
		limit = defaultRecentlyForgottenLimit
	}
	var out RecentlyForgottenItems
	if query.Kind == "" || query.Kind == RecentlyForgottenKindKnowledge {
		rows, err := s.q.ListRecentlyForgottenReflectKnowledge(ctx, sqlc.ListRecentlyForgottenReflectKnowledgeParams{
			UserID:     query.UserID,
			AgentID:    query.AgentID,
			LimitCount: limit,
		})
		if err != nil {
			return RecentlyForgottenItems{}, err
		}
		for _, row := range rows {
			metadata := decodeCuratorMetadataString(row.DeprecateMetadata.String)
			out.Knowledge = append(out.Knowledge, RecentlyForgottenKnowledgeItem{
				Kind:                        RecentlyForgottenKindKnowledge,
				FactID:                      row.FactID,
				Content:                     row.Content,
				DeprecatedAt:                row.DeprecatedAt.UTC(),
				CuratorRule:                 metadata.Rule,
				LastUsedAt:                  metadata.LastUsedAt,
				MemoryVersionAfterDeprecate: row.MemoryVersionAfter.Int64,
				DeprecatedChangelogID:       row.DeprecatedChangelogID,
			})
		}
	}
	if query.Kind != "" && query.Kind != RecentlyForgottenKindKnowledge {
		return RecentlyForgottenItems{}, fmt.Errorf("recently forgotten: unknown kind %q", query.Kind)
	}
	return out, nil
}

type curatorMetadata struct {
	Rule       string `json:"rule"`
	LastUsedAt string `json:"last_used_at"`
}

func decodeCuratorMetadataString(raw string) curatorMetadata {
	return decodeCuratorMetadataBytes([]byte(raw))
}

func decodeCuratorMetadataBytes(raw []byte) curatorMetadata {
	var metadata curatorMetadata
	_ = json.Unmarshal(raw, &metadata)
	return metadata
}
