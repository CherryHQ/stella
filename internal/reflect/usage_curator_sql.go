package reflect

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type sqlUsageCuratorStore struct {
	q *sqlc.Queries
}

func NewSQLUsageCuratorStore(q *sqlc.Queries) UsageCuratorStore {
	return sqlUsageCuratorStore{q: q}
}

// NewSQLUsageCuratorStoreForPool builds a UsageCuratorStore backed by the given
// connection pool, owning construction of its sqlc query set.
func NewSQLUsageCuratorStoreForPool(pool *pgxpool.Pool) UsageCuratorStore {
	return NewSQLUsageCuratorStore(sqlc.New(pool))
}

func (s sqlUsageCuratorStore) ListReflectUsagePairs(ctx context.Context) ([]usageCuratorPair, error) {
	if s.q == nil {
		return nil, fmt.Errorf("usage curator: sql queries are required")
	}
	rows, err := s.q.ListReflectUsagePairs(ctx)
	if err != nil {
		return nil, err
	}
	pairs := make([]usageCuratorPair, 0, len(rows))
	for _, row := range rows {
		pairs = append(pairs, usageCuratorPair{UserID: row.UserID, AgentID: row.AgentID})
	}
	return pairs, nil
}

func (s sqlUsageCuratorStore) ListStaleReflectKnowledge(ctx context.Context, query usageCuratorKnowledgeQuery) ([]usageCuratorKnowledgeCandidate, error) {
	if s.q == nil {
		return nil, fmt.Errorf("usage curator: sql queries are required")
	}
	rows, err := s.q.ListStaleReflectKnowledgeForCurator(ctx, sqlc.ListStaleReflectKnowledgeForCuratorParams{
		UserID: query.UserID, AgentID: query.AgentID, StaleBefore: query.StaleBefore,
	})
	if err != nil {
		return nil, err
	}
	out := make([]usageCuratorKnowledgeCandidate, 0, len(rows))
	for _, row := range rows {
		out = append(out, usageCuratorKnowledgeCandidate{
			FactID:               row.FactID,
			UserID:               row.UserID,
			AgentID:              row.AgentID,
			LastUsedAt:           row.LastUsedAt,
			PairLatestActivityAt: row.PairLatestActivityAt,
		})
	}
	return out, nil
}

func (s sqlUsageCuratorStore) ListStaleReflectSkills(ctx context.Context, query usageCuratorSkillQuery) ([]usageCuratorSkillCandidate, error) {
	if s.q == nil {
		return nil, fmt.Errorf("usage curator: sql queries are required")
	}
	rows, err := s.q.ListStaleReflectSkillsForCurator(ctx, sqlc.ListStaleReflectSkillsForCuratorParams{
		UserID:            query.UserID,
		AgentID:           query.AgentID,
		StaleBefore:       query.StaleBefore,
		LowUseBefore:      query.LowUseBefore,
		LowUseMaxUseCount: query.LowUseMaxUseCount,
	})
	if err != nil {
		return nil, err
	}
	out := make([]usageCuratorSkillCandidate, 0, len(rows))
	for _, row := range rows {
		out = append(out, usageCuratorSkillCandidate{
			SkillID:              row.SkillID,
			UserID:               row.UserID,
			AgentID:              row.AgentID,
			ContentDigest:        row.ContentDigest.String,
			UseCount:             row.UseCount,
			LastUsedAt:           row.LastUsedAt,
			PairLatestActivityAt: row.PairLatestActivityAt,
			Rule:                 usageCuratorSkillRule(row.Rule),
		})
	}
	return out, nil
}
