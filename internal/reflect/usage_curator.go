package reflect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorywrite"
	"github.com/CherryHQ/stella/internal/skills"
)

const (
	usageCuratorModeShadow usageCuratorMode = "shadow"
	usageCuratorModeArmed  usageCuratorMode = "armed"

	defaultKnowledgeMaxIdle        = 20 * 24 * time.Hour
	defaultSkillMaxIdle            = 60 * 24 * time.Hour
	defaultSkillLowUseIdle         = 20 * 24 * time.Hour
	defaultSkillLowUseMax          = int64(5)
	defaultUsageCuratorRunInterval = 7 * 24 * time.Hour

	usageCuratorSkillRuleUnused usageCuratorSkillRule = "unused"
	usageCuratorSkillRuleLowUse usageCuratorSkillRule = "low_use"
)

type usageCuratorMode string

type usageCuratorSkillRule string

type usageCuratorSettings struct {
	Mode               usageCuratorMode
	KnowledgeMaxIdle   time.Duration
	SkillMaxIdle       time.Duration
	SkillLowUseIdle    time.Duration
	SkillLowUseMaxUses int64
	RunInterval        time.Duration
	Now                func() time.Time
}

func (s usageCuratorSettings) withDefaults() usageCuratorSettings {
	if s.Mode == "" {
		s.Mode = usageCuratorModeShadow
	}
	if s.KnowledgeMaxIdle <= 0 {
		s.KnowledgeMaxIdle = defaultKnowledgeMaxIdle
	}
	if s.SkillMaxIdle <= 0 {
		s.SkillMaxIdle = defaultSkillMaxIdle
	}
	if s.SkillLowUseIdle <= 0 {
		s.SkillLowUseIdle = defaultSkillLowUseIdle
	}
	if s.SkillLowUseMaxUses <= 0 {
		s.SkillLowUseMaxUses = defaultSkillLowUseMax
	}
	if s.RunInterval <= 0 {
		s.RunInterval = defaultUsageCuratorRunInterval
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	return s
}

type usageCuratorRunConfig struct {
	Store       UsageCuratorStore
	FactWriter  factBatchWriter
	SkillWriter usageCuratorSkillWriter
	Settings    usageCuratorSettings
}

type UsageCuratorStore interface {
	ListStaleReflectKnowledge(ctx context.Context, q usageCuratorKnowledgeQuery) ([]usageCuratorKnowledgeCandidate, error)
	ListStaleReflectSkills(ctx context.Context, q usageCuratorSkillQuery) ([]usageCuratorSkillCandidate, error)
}

type usageCuratorSkillWriter interface {
	DeprecateReflectOwnedUserAgentSkill(ctx context.Context, in skills.ReflectSkillDeprecate) (skills.Skill, error)
}

type usageCuratorKnowledgeQuery struct {
	StaleBefore time.Time
}

type usageCuratorSkillQuery struct {
	StaleBefore       time.Time
	LowUseBefore      time.Time
	LowUseMaxUseCount int64
}

type usageCuratorKnowledgeCandidate struct {
	FactID               string
	UserID               string
	AgentID              string
	LastUsedAt           time.Time
	PairLatestActivityAt time.Time
}

type usageCuratorSkillCandidate struct {
	SkillID              string
	UserID               string
	AgentID              string
	Version              int64
	UseCount             int64
	LastUsedAt           time.Time
	PairLatestActivityAt time.Time
	Rule                 usageCuratorSkillRule
}

type usageCuratorReport struct {
	Mode                usageCuratorMode
	KnowledgeCandidates int
	KnowledgeDeprecated int
	SkillCandidates     int
	SkillDeprecated     int
}

func runUsageCurator(ctx context.Context, cfg usageCuratorRunConfig) (usageCuratorReport, error) {
	settings := cfg.Settings.withDefaults()
	now := settings.Now().UTC()
	report := usageCuratorReport{Mode: settings.Mode}
	if cfg.Store == nil {
		return report, nil
	}

	knowledge, knowledgeErr := cfg.Store.ListStaleReflectKnowledge(ctx, usageCuratorKnowledgeQuery{
		StaleBefore: now.Add(-settings.KnowledgeMaxIdle),
	})
	if knowledgeErr == nil {
		report.KnowledgeCandidates = len(knowledge)
	}
	skillsToDeprecate, skillErr := cfg.Store.ListStaleReflectSkills(ctx, usageCuratorSkillQuery{
		StaleBefore:       now.Add(-settings.SkillMaxIdle),
		LowUseBefore:      now.Add(-settings.SkillLowUseIdle),
		LowUseMaxUseCount: settings.SkillLowUseMaxUses,
	})
	if skillErr == nil {
		report.SkillCandidates = len(skillsToDeprecate)
	}
	if settings.Mode == usageCuratorModeShadow {
		return report, errors.Join(knowledgeErr, skillErr)
	}
	if settings.Mode != usageCuratorModeArmed {
		return report, fmt.Errorf("usage curator: unsupported mode %q", settings.Mode)
	}

	var writeErrs []error
	if knowledgeErr != nil {
		writeErrs = append(writeErrs, knowledgeErr)
	} else {
		n, err := deprecateCuratorKnowledge(ctx, cfg.FactWriter, knowledge)
		report.KnowledgeDeprecated = n
		if err != nil {
			writeErrs = append(writeErrs, err)
		}
	}
	if skillErr != nil {
		writeErrs = append(writeErrs, skillErr)
	} else {
		n, err := deprecateCuratorSkills(ctx, cfg.SkillWriter, skillsToDeprecate)
		report.SkillDeprecated = n
		if err != nil {
			writeErrs = append(writeErrs, err)
		}
	}
	return report, errors.Join(writeErrs...)
}

func deprecateCuratorKnowledge(ctx context.Context, writer factBatchWriter, candidates []usageCuratorKnowledgeCandidate) (int, error) {
	if len(candidates) == 0 {
		return 0, nil
	}
	if writer == nil {
		return 0, fmt.Errorf("usage curator: fact writer is required")
	}
	groups := groupKnowledgeCuratorCandidates(candidates)
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var deprecated int
	var errs []error
	writeCtx := memory.WithChangeSource(ctx, memory.SourceReflect)
	for _, key := range keys {
		group := groups[key]
		op := memorywrite.FactBatchOperation{
			Action:        memorywrite.FactBatchDeprecateMany,
			Subject:       memory.FactSubjectWorld,
			TargetFactIDs: group.factIDs,
		}
		if _, err := writer.ApplyFactBatch(writeCtx, group.userID, group.agentID, []memorywrite.FactBatchOperation{op}); err != nil {
			errs = append(errs, fmt.Errorf("usage curator: deprecate knowledge for %s/%s: %w", group.userID, group.agentID, err))
			continue
		}
		deprecated += len(group.factIDs)
	}
	return deprecated, errors.Join(errs...)
}

type usageCuratorKnowledgeGroup struct {
	userID  string
	agentID string
	factIDs []string
}

func groupKnowledgeCuratorCandidates(candidates []usageCuratorKnowledgeCandidate) map[string]usageCuratorKnowledgeGroup {
	groups := make(map[string]usageCuratorKnowledgeGroup)
	for _, candidate := range candidates {
		key := candidate.UserID + "\x00" + candidate.AgentID
		group := groups[key]
		group.userID = candidate.UserID
		group.agentID = candidate.AgentID
		group.factIDs = append(group.factIDs, candidate.FactID)
		groups[key] = group
	}
	for key, group := range groups {
		sort.Strings(group.factIDs)
		groups[key] = group
	}
	return groups
}

func deprecateCuratorSkills(ctx context.Context, writer usageCuratorSkillWriter, candidates []usageCuratorSkillCandidate) (int, error) {
	if len(candidates) == 0 {
		return 0, nil
	}
	if writer == nil {
		return 0, fmt.Errorf("usage curator: skill writer is required")
	}
	var deprecated int
	var errs []error
	for _, candidate := range candidates {
		metadata, err := usageCuratorSkillMetadata(candidate)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		_, err = writer.DeprecateReflectOwnedUserAgentSkill(ctx, skills.ReflectSkillDeprecate{
			ID:              candidate.SkillID,
			UserID:          candidate.UserID,
			AgentID:         candidate.AgentID,
			ExpectedVersion: candidate.Version,
			Metadata:        metadata,
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("usage curator: deprecate skill %s: %w", candidate.SkillID, err))
			continue
		}
		deprecated++
	}
	return deprecated, errors.Join(errs...)
}

func usageCuratorSkillMetadata(candidate usageCuratorSkillCandidate) (json.RawMessage, error) {
	payload := map[string]any{
		"curator":                 "usage",
		"rule":                    string(candidate.Rule),
		"use_count":               candidate.UseCount,
		"last_used_at":            candidate.LastUsedAt.UTC().Format(time.RFC3339),
		"pair_latest_activity_at": candidate.PairLatestActivityAt.UTC().Format(time.RFC3339),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}
