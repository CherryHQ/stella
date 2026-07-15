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
	UsageCuratorModeShadow UsageCuratorMode = "shadow"
	UsageCuratorModeArmed  UsageCuratorMode = "armed"

	defaultKnowledgeMaxIdle        = 20 * 24 * time.Hour
	defaultSkillMaxIdle            = 60 * 24 * time.Hour
	defaultSkillLowUseIdle         = 20 * 24 * time.Hour
	defaultSkillLowUseMax          = int64(5)
	defaultUsageCuratorRunInterval = 7 * 24 * time.Hour

	usageCuratorSkillRuleUnused usageCuratorSkillRule = "unused"
	usageCuratorSkillRuleLowUse usageCuratorSkillRule = "low_use"
)

type UsageCuratorMode string

type usageCuratorSkillRule string

type UsageCuratorSettings struct {
	Mode               UsageCuratorMode
	KnowledgeMaxIdle   time.Duration
	SkillMaxIdle       time.Duration
	SkillLowUseIdle    time.Duration
	SkillLowUseMaxUses int64
	RunInterval        time.Duration
	Now                func() time.Time
}

const (
	usageCuratorModeShadow = UsageCuratorModeShadow
	usageCuratorModeArmed  = UsageCuratorModeArmed
)

type usageCuratorSettings = UsageCuratorSettings

func (s UsageCuratorSettings) withDefaults() UsageCuratorSettings {
	if s.Mode == "" {
		s.Mode = UsageCuratorModeShadow
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
	Store           UsageCuratorStore
	FactWriter      factBatchWriter
	SkillWriter     usageCuratorSkillWriter
	SkillAuthorizer skillWriteAuthorizer
	Pair            usageCuratorPair
	Settings        usageCuratorSettings
}

type UsageCuratorStore interface {
	ListReflectUsagePairs(ctx context.Context) ([]usageCuratorPair, error)
	ListStaleReflectKnowledge(ctx context.Context, q usageCuratorKnowledgeQuery) ([]usageCuratorKnowledgeCandidate, error)
	ListStaleReflectSkills(ctx context.Context, q usageCuratorSkillQuery) ([]usageCuratorSkillCandidate, error)
}

type usageCuratorPair struct {
	UserID  string
	AgentID string
}

type usageCuratorSkillWriter interface {
	DeprecateReflectOwnedUserAgentSkill(ctx context.Context, in skills.ReflectSkillDeprecate) (skills.Skill, error)
}

type usageCuratorKnowledgeQuery struct {
	UserID      string
	AgentID     string
	StaleBefore time.Time
}

type usageCuratorSkillQuery struct {
	UserID            string
	AgentID           string
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
	Mode                UsageCuratorMode
	Pair                usageCuratorPair
	KnowledgeCandidates int
	KnowledgeDeprecated int
	SkillCandidates     int
	SkillDeprecated     int
	RuleCounts          map[string]int
	Duration            time.Duration
	Errors              int
	Evidence            []usageCuratorEvidence
}

type usageCuratorEvidence struct {
	RecordType           string
	RecordID             string
	UserID               string
	AgentID              string
	Rule                 string
	LastUsedAt           time.Time
	UseCount             int64
	Cutoff               time.Time
	PairLatestActivityAt time.Time
}

func runUsageCurator(ctx context.Context, cfg usageCuratorRunConfig) (report usageCuratorReport, runErr error) {
	started := time.Now()
	defer func() {
		report.Duration = time.Since(started)
		report.Errors = joinedErrorCount(runErr)
	}()
	settings := cfg.Settings.withDefaults()
	now := settings.Now().UTC()
	report = usageCuratorReport{Mode: settings.Mode, Pair: cfg.Pair}
	if cfg.Store == nil {
		return report, nil
	}

	knowledge, knowledgeErr := cfg.Store.ListStaleReflectKnowledge(ctx, usageCuratorKnowledgeQuery{
		UserID:      cfg.Pair.UserID,
		AgentID:     cfg.Pair.AgentID,
		StaleBefore: now.Add(-settings.KnowledgeMaxIdle),
	})
	if knowledgeErr == nil {
		report.KnowledgeCandidates = len(knowledge)
	}
	skillsToDeprecate, skillErr := cfg.Store.ListStaleReflectSkills(ctx, usageCuratorSkillQuery{
		UserID:            cfg.Pair.UserID,
		AgentID:           cfg.Pair.AgentID,
		StaleBefore:       now.Add(-settings.SkillMaxIdle),
		LowUseBefore:      now.Add(-settings.SkillLowUseIdle),
		LowUseMaxUseCount: settings.SkillLowUseMaxUses,
	})
	if skillErr == nil {
		report.SkillCandidates = len(skillsToDeprecate)
	}
	report.Evidence = usageCuratorEvidenceForCandidates(settings, now, knowledge, skillsToDeprecate)
	report.RuleCounts = usageCuratorRuleCounts(report.Evidence)
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
		n, err := deprecateCuratorSkills(ctx, cfg.SkillWriter, cfg.SkillAuthorizer, skillsToDeprecate)
		report.SkillDeprecated = n
		if err != nil {
			writeErrs = append(writeErrs, err)
		}
	}
	return report, errors.Join(writeErrs...)
}

func usageCuratorRuleCounts(evidence []usageCuratorEvidence) map[string]int {
	counts := make(map[string]int)
	for _, item := range evidence {
		counts[item.RecordType+":"+item.Rule]++
	}
	return counts
}

func joinedErrorCount(err error) int {
	if err == nil {
		return 0
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		count := 0
		for _, child := range joined.Unwrap() {
			count += joinedErrorCount(child)
		}
		return count
	}
	return 1
}

func usageCuratorEvidenceForCandidates(settings usageCuratorSettings, now time.Time, knowledge []usageCuratorKnowledgeCandidate, skillCandidates []usageCuratorSkillCandidate) []usageCuratorEvidence {
	evidence := make([]usageCuratorEvidence, 0, len(knowledge)+len(skillCandidates))
	for _, candidate := range knowledge {
		evidence = append(evidence, usageCuratorEvidence{
			RecordType: "knowledge", RecordID: candidate.FactID,
			UserID: candidate.UserID, AgentID: candidate.AgentID, Rule: "idle",
			LastUsedAt: candidate.LastUsedAt, Cutoff: now.Add(-settings.KnowledgeMaxIdle),
			PairLatestActivityAt: candidate.PairLatestActivityAt,
		})
	}
	for _, candidate := range skillCandidates {
		cutoff := now.Add(-settings.SkillLowUseIdle)
		if candidate.Rule == usageCuratorSkillRuleUnused {
			cutoff = now.Add(-settings.SkillMaxIdle)
		}
		evidence = append(evidence, usageCuratorEvidence{
			RecordType: "skill", RecordID: candidate.SkillID,
			UserID: candidate.UserID, AgentID: candidate.AgentID, Rule: string(candidate.Rule),
			LastUsedAt: candidate.LastUsedAt, UseCount: candidate.UseCount, Cutoff: cutoff,
			PairLatestActivityAt: candidate.PairLatestActivityAt,
		})
	}
	return evidence
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

writeGroups:
	for _, key := range keys {
		group := groups[key]
		for _, candidate := range group.candidates {
			if err := ctx.Err(); err != nil {
				errs = append(errs, err)
				break writeGroups
			}
			metadata, err := usageCuratorKnowledgeMetadata(candidate)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			op := memorywrite.FactBatchOperation{
				Action:                            memorywrite.FactBatchDeprecateMany,
				Subject:                           memory.FactSubjectWorld,
				TargetFactIDs:                     []string{candidate.FactID},
				Metadata:                          metadata,
				TargetUsageLastUsedAt:             map[string]time.Time{candidate.FactID: candidate.LastUsedAt},
				RequireEligibleActivityAfterUsage: true,
			}
			facts, err := writer.ApplyFactBatch(writeCtx, group.userID, group.agentID, []memorywrite.FactBatchOperation{op})
			if err != nil {
				errs = append(errs, fmt.Errorf("usage curator: deprecate knowledge %s for %s/%s: %w", candidate.FactID, group.userID, group.agentID, err))
				continue
			}
			deprecated += len(facts)
		}
	}
	return deprecated, errors.Join(errs...)
}

func usageCuratorKnowledgeMetadata(candidate usageCuratorKnowledgeCandidate) (json.RawMessage, error) {
	payload := map[string]any{
		"curator":                 "usage",
		"rule":                    "idle",
		"last_used_at":            candidate.LastUsedAt.UTC().Format(time.RFC3339),
		"pair_latest_activity_at": candidate.PairLatestActivityAt.UTC().Format(time.RFC3339),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

type usageCuratorKnowledgeGroup struct {
	userID     string
	agentID    string
	candidates []usageCuratorKnowledgeCandidate
}

func groupKnowledgeCuratorCandidates(candidates []usageCuratorKnowledgeCandidate) map[string]usageCuratorKnowledgeGroup {
	groups := make(map[string]usageCuratorKnowledgeGroup)
	for _, candidate := range candidates {
		key := candidate.UserID + "\x00" + candidate.AgentID
		group := groups[key]
		group.userID = candidate.UserID
		group.agentID = candidate.AgentID
		group.candidates = append(group.candidates, candidate)
		groups[key] = group
	}
	for key, group := range groups {
		sort.Slice(group.candidates, func(i, j int) bool {
			return group.candidates[i].FactID < group.candidates[j].FactID
		})
		groups[key] = group
	}
	return groups
}

func deprecateCuratorSkills(ctx context.Context, writer usageCuratorSkillWriter, authorizer skillWriteAuthorizer, candidates []usageCuratorSkillCandidate) (int, error) {
	if len(candidates) == 0 {
		return 0, nil
	}
	if writer == nil {
		return 0, fmt.Errorf("usage curator: skill writer is required")
	}
	if authorizer == nil {
		return 0, fmt.Errorf("usage curator: skill authorizer is required")
	}
	var deprecated int
	var errs []error
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		metadata, err := usageCuratorSkillMetadata(candidate)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := authorizer.AuthorizeWorkerWrite(ctx, candidate.UserID, candidate.AgentID, candidate.SkillID, false); err != nil {
			errs = append(errs, fmt.Errorf("usage curator: authorize deprecate skill %s: %w", candidate.SkillID, err))
			continue
		}
		expectedLastUsedAt := candidate.LastUsedAt
		_, err = writer.DeprecateReflectOwnedUserAgentSkill(ctx, skills.ReflectSkillDeprecate{
			ID:                                candidate.SkillID,
			UserID:                            candidate.UserID,
			AgentID:                           candidate.AgentID,
			ExpectedVersion:                   candidate.Version,
			ExpectedUsageLastUsedAt:           &expectedLastUsedAt,
			RequireEligibleActivityAfterUsage: true,
			Metadata:                          metadata,
		})
		if errors.Is(err, skills.ErrSkillUsageChanged) {
			continue
		}
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
