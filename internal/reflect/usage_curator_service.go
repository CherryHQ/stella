package reflect

import (
	"context"
	"time"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

const usageCuratorStateKey = "usage_curator"

func (s *Service) maybeRunUsageCurator(ctx context.Context) {
	if s.usageCuratorStore == nil {
		return
	}
	if s.stateStore == nil {
		s.log.Warn("reflect usage curator: state store unavailable; skipping scheduled run")
		return
	}
	settings := s.usageCuratorSettings.withDefaults()
	now := settings.Now().UTC()
	due, err := s.usageCuratorDue(ctx, settings, now)
	if err != nil {
		s.log.Error("reflect usage curator: read schedule state", "error", err)
		return
	}
	if !due {
		return
	}

	settings.Now = func() time.Time { return now }
	report, err := s.runUsageCuratorOnce(ctx, settings)
	if err != nil {
		s.log.Error("reflect usage curator: run failed", "error", err, "report", report)
		return
	}
	if err := s.recordUsageCuratorSuccess(ctx, now, report); err != nil {
		s.log.Error("reflect usage curator: record success", "error", err)
		return
	}
	s.log.Info("reflect usage curator: run complete", "report", report)
}

func (s *Service) runUsageCuratorOnce(ctx context.Context, settings usageCuratorSettings) (usageCuratorReport, error) {
	factWriter, _ := s.memory.(factBatchWriter)
	skillWriter, _ := s.skillStore.(usageCuratorSkillWriter)
	return runUsageCurator(ctx, usageCuratorRunConfig{
		Store:       s.usageCuratorStore,
		FactWriter:  factWriter,
		SkillWriter: skillWriter,
		Settings:    settings,
	})
}

func (s *Service) usageCuratorDue(ctx context.Context, settings usageCuratorSettings, now time.Time) (bool, error) {
	value, ok, err := s.stateStore.Get(ctx, pkgplugins.StateScope{Kind: pkgplugins.StateScopeGlobal}, usageCuratorStateKey)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	raw, _ := value["last_success_at"].(string)
	if raw == "" {
		return true, nil
	}
	lastSuccess, parsed := parseUsageCuratorLastSuccess(raw)
	if !parsed {
		return true, nil
	}
	return !now.Before(lastSuccess.Add(settings.RunInterval)), nil
}

func parseUsageCuratorLastSuccess(raw string) (time.Time, bool) {
	lastSuccess, err := time.Parse(time.RFC3339, raw)
	return lastSuccess, err == nil
}

func (s *Service) recordUsageCuratorSuccess(ctx context.Context, at time.Time, report usageCuratorReport) error {
	return s.stateStore.Set(ctx, pkgplugins.StateScope{Kind: pkgplugins.StateScopeGlobal}, usageCuratorStateKey, map[string]any{
		"last_success_at":      at.UTC().Format(time.RFC3339),
		"mode":                 string(report.Mode),
		"knowledge_candidates": report.KnowledgeCandidates,
		"knowledge_deprecated": report.KnowledgeDeprecated,
		"skill_candidates":     report.SkillCandidates,
		"skill_deprecated":     report.SkillDeprecated,
	})
}
