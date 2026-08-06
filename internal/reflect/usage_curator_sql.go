package reflect

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/skills"
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
			SkillID:               row.SkillID,
			UserID:                row.UserID,
			AgentID:               row.AgentID,
			LegacyExpectedVersion: row.Version,
			UseCount:              row.UseCount,
			LastUsedAt:            row.LastUsedAt,
			PairLatestActivityAt:  row.PairLatestActivityAt,
			Rule:                  usageCuratorSkillRule(row.Rule),
		})
	}
	return out, nil
}

// homeUsageCuratorStore keeps PostgreSQL for logical pair/knowledge activity
// while delegating Skill candidates to Home's telemetry-only usage store. It
// deliberately never reads legacy Skill current state or its stale-skill join.
type homeUsageCuratorStore struct {
	q     *sqlc.Queries
	usage *skills.HomeSkillUsageStore
}

func NewHomeUsageCuratorStore(q *sqlc.Queries, usage *skills.HomeSkillUsageStore) (UsageCuratorStore, error) {
	if q == nil || usage == nil {
		return nil, fmt.Errorf("usage curator: Home queries and usage store are required")
	}
	return homeUsageCuratorStore{q: q, usage: usage}, nil
}

// NewHomeUsageCuratorStoreForPool is the production-capable Home curator
// constructor. Wiring it with the Home current-state writer lands separately.
func NewHomeUsageCuratorStoreForPool(pool *pgxpool.Pool) (UsageCuratorStore, error) {
	usage, err := skills.NewHomeSkillUsageStore(pool)
	if err != nil {
		return nil, err
	}
	return NewHomeUsageCuratorStore(sqlc.New(pool), usage)
}

func (s homeUsageCuratorStore) ListReflectUsagePairs(ctx context.Context) ([]usageCuratorPair, error) {
	return sqlUsageCuratorStore{q: s.q}.ListReflectUsagePairs(ctx)
}

func (s homeUsageCuratorStore) ListStaleReflectKnowledge(ctx context.Context, query usageCuratorKnowledgeQuery) ([]usageCuratorKnowledgeCandidate, error) {
	return sqlUsageCuratorStore{q: s.q}.ListStaleReflectKnowledge(ctx, query)
}

func (s homeUsageCuratorStore) ListStaleReflectSkills(ctx context.Context, query usageCuratorSkillQuery) ([]usageCuratorSkillCandidate, error) {
	candidates, err := s.usage.ListStaleReflectCandidates(ctx, query.UserID, query.AgentID, query.StaleBefore, query.LowUseBefore, query.LowUseMaxUseCount)
	if err != nil {
		return nil, err
	}
	out := make([]usageCuratorSkillCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, usageCuratorSkillCandidate{
			SkillID:              candidate.LogicalID,
			UserID:               candidate.UserID,
			AgentID:              candidate.AgentID,
			ContentDigest:        candidate.LastContentDigest,
			UseCount:             candidate.UseCount,
			LastUsedAt:           candidate.LastUsedAt,
			PairLatestActivityAt: candidate.PairLatestActivityAt,
			Rule:                 usageCuratorSkillRule(candidate.Rule),
		})
	}
	return out, nil
}
