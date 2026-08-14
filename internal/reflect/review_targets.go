package reflect

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/memory"
)

// reviewTarget represents a session that may need review.
type reviewTarget struct {
	session         memory.Session
	lastActive      time.Time // from SessionInfo — used as tiebreaker
	lastReview      time.Time // zero if never reviewed
	privateOneToOne bool
}

// listUnreviewedFromRegistry uses session.Registry.ListForReview to apply the
// default review policy (excludes delegate/task/scheduler sessions) and
// Registry.MemoryScope to build authorised memory.Session values.
func (s *Service) listUnreviewedFromRegistry(ctx context.Context, reg *session.Registry, agentID string) ([]reviewTarget, error) {
	infos, err := reg.ListForReview(ctx, session.ReviewRequest{AgentID: agentID, Policy: session.DefaultReviewPolicy()})
	if err != nil {
		return nil, err
	}

	targets := make([]reviewTarget, 0, len(infos))
	for _, info := range infos {
		target, ok := s.unreviewedTargetFromRegistry(ctx, reg, info)
		if !ok {
			continue
		}
		targets = append(targets, target)
	}

	sortReviewTargets(targets)
	if limit := s.reviewTargetLimit(); limit > 0 && len(targets) > limit {
		targets = targets[:limit]
	}
	return targets, nil
}

func (s *Service) reviewTargetLimit() int {
	if s.maxReviewTargetsPerAgent > 0 {
		return s.maxReviewTargetsPerAgent
	}
	return defaultMaxReviewTargetsPerAgent
}

func (s *Service) unreviewedTargetFromRegistry(ctx context.Context, reg *session.Registry, info session.Info) (reviewTarget, bool) {
	// Reflect currently mines private one-to-one history only. Exclude groups
	// before watermark lookup and target limiting so skipped group sessions cannot
	// permanently occupy the oldest-review slots and starve private sessions.
	if info.UserID == "" || info.GroupID != "" || info.GuestID != "" {
		return reviewTarget{}, false
	}

	wm, pending, err := s.reviewProgress(ctx, info.ID, info.LastActive, info.LatestSeq)
	if err != nil {
		s.log.Warn("reflect: watermark lookup failed, skipping session", "session", info.ID, "error", err)
		return reviewTarget{}, false
	}
	if !pending {
		return reviewTarget{}, false
	}

	scope, err := reg.MemoryScope(info)
	if err != nil {
		s.log.Warn("reflect: invalid session scope, skipping session", "session", info.ID, "error", err)
		return reviewTarget{}, false
	}
	return reviewTarget{
		session:         scope,
		lastActive:      info.LastActive,
		lastReview:      wm,
		privateOneToOne: true,
	}, true
}

func (s *Service) reviewProgress(ctx context.Context, sessionID string, lastActive time.Time, latestSeq int64) (time.Time, bool, error) {
	fact, err := s.wm.getLine(ctx, sessionID, reflectLineFact)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("get fact watermark: %w", err)
	}
	skill, err := s.wm.getLine(ctx, sessionID, reflectLineSkill)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("get skill watermark: %w", err)
	}
	oldest := olderWatermarkTime(fact.At, skill.At)
	return oldest, reviewLinePending(fact, lastActive, latestSeq) || reviewLinePending(skill, lastActive, latestSeq), nil
}

func reviewLinePending(mark reviewWatermark, lastActive time.Time, latestSeq int64) bool {
	if latestSeq > 0 {
		return latestSeq > mark.Seq
	}
	return lastActive.After(mark.At)
}

func sortReviewTargets(targets []reviewTarget) {
	sort.Slice(targets, func(i, j int) bool {
		ri, rj := targets[i].lastReview, targets[j].lastReview
		if ri.Equal(rj) {
			return targets[i].lastActive.Before(targets[j].lastActive)
		}
		return ri.Before(rj)
	})
}
