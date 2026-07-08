package reflect

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type sqlUsageCuratorStore struct {
	q *sqlc.Queries
}

func NewSQLUsageCuratorStore(q *sqlc.Queries) UsageCuratorStore {
	return sqlUsageCuratorStore{q: q}
}

func (s sqlUsageCuratorStore) ListStaleReflectKnowledge(ctx context.Context, query usageCuratorKnowledgeQuery) ([]usageCuratorKnowledgeCandidate, error) {
	if s.q == nil {
		return nil, fmt.Errorf("usage curator: sql queries are required")
	}
	rows, err := s.q.ListStaleReflectKnowledgeForCurator(ctx, query.StaleBefore)
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
			Version:              row.Version,
			UseCount:             row.UseCount,
			LastUsedAt:           row.LastUsedAt,
			PairLatestActivityAt: row.PairLatestActivityAt,
			Rule:                 usageCuratorSkillRule(row.Rule),
		})
	}
	return out, nil
}
