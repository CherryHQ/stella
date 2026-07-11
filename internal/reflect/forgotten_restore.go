package reflect

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// RestoreForgottenRequest identifies one curator-deprecated record to restore.
type RestoreForgottenRequest struct {
	Kind       string
	ID         string
	UserID     string
	AgentID    string
	RestoredBy string
	Reason     string
}

// RestoreForgottenResult returns the restored item. Exactly one of Knowledge or
// Skill is set when the request succeeds.
type RestoreForgottenResult struct {
	Kind      string
	Restored  bool
	Knowledge *memory.Fact
	Skill     *skills.Skill
}

type forgottenSkillRestorer interface {
	RestoreReflectOwnedUserAgentSkill(context.Context, skills.ReflectSkillRestore) (skills.ReflectSkillRestoreResult, error)
}

// SQLForgottenRestoreService provides the internal/admin restore path for items
// returned by SQLRecentlyForgottenStore.
type SQLForgottenRestoreService struct {
	db         *pgxpool.Pool
	q          *sqlc.Queries
	skillStore forgottenSkillRestorer
}

func NewSQLForgottenRestoreService(db *pgxpool.Pool, q *sqlc.Queries, skillStore forgottenSkillRestorer) SQLForgottenRestoreService {
	return SQLForgottenRestoreService{
		db:         db,
		q:          q,
		skillStore: skillStore,
	}
}

// RestoreForgotten is the internal/admin restore entrypoint for curator-deprecated
// Reflect-owned knowledge and skills. It does not expose a user-facing API by
// itself; callers still decide authentication and authorization.
func (s SQLForgottenRestoreService) RestoreForgotten(ctx context.Context, in RestoreForgottenRequest) (RestoreForgottenResult, error) {
	switch in.Kind {
	case RecentlyForgottenKindKnowledge:
		if s.db == nil || s.q == nil {
			return RestoreForgottenResult{}, fmt.Errorf("forgotten restore: db and sql queries are required")
		}
		result, err := memorywrite.RestoreCuratorDeprecatedKnowledgeFact(ctx, s.db, s.q, memorywrite.RestoreCuratorDeprecatedKnowledgeFactInput{
			FactID:     in.ID,
			UserID:     in.UserID,
			AgentID:    in.AgentID,
			RestoredBy: in.RestoredBy,
			Reason:     in.Reason,
		})
		if err != nil {
			return RestoreForgottenResult{}, err
		}
		fact := result.Fact
		return RestoreForgottenResult{
			Kind:      RecentlyForgottenKindKnowledge,
			Restored:  result.Restored,
			Knowledge: &fact,
		}, nil
	case RecentlyForgottenKindSkill:
		if s.skillStore == nil {
			return RestoreForgottenResult{}, fmt.Errorf("forgotten restore: skill store is required")
		}
		result, err := s.skillStore.RestoreReflectOwnedUserAgentSkill(ctx, skills.ReflectSkillRestore{
			ID:         in.ID,
			UserID:     in.UserID,
			AgentID:    in.AgentID,
			RestoredBy: in.RestoredBy,
			Reason:     in.Reason,
		})
		if err != nil {
			return RestoreForgottenResult{}, err
		}
		skill := result.Skill
		return RestoreForgottenResult{
			Kind:     RecentlyForgottenKindSkill,
			Restored: result.Restored,
			Skill:    &skill,
		}, nil
	default:
		return RestoreForgottenResult{}, fmt.Errorf("forgotten restore: unknown kind %q", in.Kind)
	}
}
