package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/groupingest"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/scheduler"
	"github.com/CherryHQ/stella/pkg/ai"
)

const (
	groupMemoryModeLegacy     = "legacy"
	groupMemoryModeStructured = "structured"
	groupReflectDefaultCron   = "0 3,9,15,21 * * *"
	minGroupReflectInterval   = time.Minute
)

type groupReflectProviderStore interface {
	ListProviders(ctx context.Context) ([]config.Provider, error)
}

type groupMemorySetup struct {
	structured   bool
	promptLoader agent.GroupFactPromptLoader
}

func setupGroupMemory(
	ctx context.Context,
	schedulerSvc *scheduler.Service,
	db *pgxpool.Pool,
	store groupReflectProviderStore,
	memProvider memory.Provider,
	providerBuilder agent.ProviderStreamBuilder,
	cfg config.GroupMemoryConfig,
) (groupMemorySetup, error) {
	mode, err := resolveGroupMemoryMode(cfg.Mode)
	if err != nil {
		return groupMemorySetup{}, err
	}
	if mode == groupMemoryModeLegacy {
		slog.Info("group memory: legacy mode active; structured Group Reflect is disabled")
		return groupMemorySetup{}, nil
	}
	if schedulerSvc == nil || db == nil || store == nil || memProvider == nil || providerBuilder == nil {
		return groupMemorySetup{}, fmt.Errorf("group memory: structured mode requires scheduler, database, config store, memory provider, and provider builder")
	}

	model, provider, err := resolveGroupReflectModel(ctx, store, cfg.ReflectModel)
	if err != nil {
		return groupMemorySetup{}, err
	}
	stream, err := providerBuilder(model.API, provider.APIKey, provider.BaseURL)
	if err != nil {
		return groupMemorySetup{}, fmt.Errorf("group memory: build Group Reflect provider: %w", err)
	}
	factStore, err := requireStructuredGroupMemoryCapabilities(memProvider)
	if err != nil {
		return groupMemorySetup{}, err
	}
	cache, err := groupingest.NewGroupFactCache(factStore, groupingest.GroupFactCacheOptions{})
	if err != nil {
		return groupMemorySetup{}, err
	}
	injector, err := groupingest.NewRuntimeInjector(cache)
	if err != nil {
		return groupMemorySetup{}, err
	}
	ingester, err := groupingest.NewStructuredForPool(groupingest.StructuredConfig{
		DB:        db,
		FactStore: factStore,
		Reviewer: groupingest.CandidateReviewer{
			Stream: stream,
			Model:  model,
		},
		Reconciler: groupingest.ReconciliationRunner{
			Stream: stream,
			Model:  model,
		},
	})
	if err != nil {
		return groupMemorySetup{}, fmt.Errorf("group memory: build structured Group Reflect: %w", err)
	}
	if err := schedulerSvc.RegisterBuiltin(scheduler.BuiltinJob{
		Name:     groupingest.BuiltinGroupReflectJobName,
		Schedule: resolveGroupReflectSchedule(cfg.ReflectInterval),
		Handler: func(ctx context.Context, _ scheduler.Job) error {
			return ingester.RunOnce(ctx)
		},
	}); err != nil {
		return groupMemorySetup{}, fmt.Errorf("group memory: register Group Reflect builtin: %w", err)
	}
	slog.Info("group memory: structured Group Reflect registered",
		"model", cfg.ReflectModel,
		"schedule", resolveGroupReflectSchedule(cfg.ReflectInterval),
	)
	return groupMemorySetup{
		structured:   true,
		promptLoader: injector.Inject,
	}, nil
}

func requireStructuredGroupMemoryCapabilities(provider memory.Provider) (memory.GroupFactStore, error) {
	if provider == nil {
		return nil, fmt.Errorf("group memory: memory provider is required")
	}
	inner := memory.Unwrap(provider)
	if _, ok := inner.(memory.GroupFactStore); !ok {
		return nil, fmt.Errorf("group memory: memory provider must support Group Fact reads")
	}
	if _, ok := inner.(memory.GroupEventIngestor); !ok {
		return nil, fmt.Errorf("group memory: memory provider must support group event ingestion")
	}
	if _, ok := inner.(memory.GroupCursorCommitter); !ok {
		return nil, fmt.Errorf("group memory: memory provider must support group cursor commits")
	}
	factStore, ok := provider.(memory.GroupFactStore)
	if !ok {
		return nil, fmt.Errorf("group memory: memory provider wrapper does not expose Group Fact reads")
	}
	if _, ok := provider.(memory.GroupEventIngestor); !ok {
		return nil, fmt.Errorf("group memory: memory provider wrapper does not expose group event ingestion")
	}
	if _, ok := provider.(memory.GroupCursorCommitter); !ok {
		return nil, fmt.Errorf("group memory: memory provider wrapper does not expose group cursor commits")
	}
	return factStore, nil
}

func resolveGroupMemoryMode(raw string) (string, error) {
	switch mode := strings.ToLower(strings.TrimSpace(raw)); mode {
	case "", groupMemoryModeLegacy:
		return groupMemoryModeLegacy, nil
	case groupMemoryModeStructured:
		return groupMemoryModeStructured, nil
	default:
		return "", fmt.Errorf("group memory: unsupported STELLA_GROUP_MEMORY_MODE %q (want legacy or structured)", raw)
	}
}

func resolveGroupReflectSchedule(rawInterval string) scheduler.Schedule {
	if rawInterval == "" {
		return scheduler.Schedule{Cron: groupReflectDefaultCron}
	}
	interval := resolveGroupReflectInterval(rawInterval)
	return scheduler.Schedule{Every: interval.String()}
}

func resolveGroupReflectInterval(raw string) time.Duration {
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		slog.Warn("group memory: STELLA_GROUP_REFLECT_INTERVAL unparseable, using 6h",
			"value", raw,
			"error", err,
		)
		return 6 * time.Hour
	}
	if parsed < minGroupReflectInterval {
		slog.Warn("group memory: STELLA_GROUP_REFLECT_INTERVAL below minimum, using minimum",
			"value", parsed,
			"min", minGroupReflectInterval,
		)
		return minGroupReflectInterval
	}
	return parsed
}

func resolveGroupReflectModel(
	ctx context.Context,
	store groupReflectProviderStore,
	rawRef string,
) (ai.Model, config.Provider, error) {
	ref := strings.TrimSpace(rawRef)
	providerID, modelID := config.ParseModelRef(ref)
	if providerID == "" || modelID == "" {
		return ai.Model{}, config.Provider{}, fmt.Errorf("group memory: STELLA_GROUP_REFLECT_MODEL must be provider/model")
	}
	providerRows, err := store.ListProviders(ctx)
	if err != nil {
		return ai.Model{}, config.Provider{}, fmt.Errorf("group memory: list providers: %w", err)
	}
	provider, ok, ambiguous := selectGroupReflectProvider(providerRows, providerID)
	if ambiguous {
		return ai.Model{}, config.Provider{}, fmt.Errorf("group memory: provider type %q is ambiguous; use a provider ID", providerID)
	}
	if !ok || !provider.Enabled {
		return ai.Model{}, config.Provider{}, fmt.Errorf("group memory: provider %q is missing or disabled", providerID)
	}
	if strings.TrimSpace(provider.APIKey) == "" {
		return ai.Model{}, config.Provider{}, fmt.Errorf("group memory: provider %q has no API key", provider.ID)
	}
	modelConfig, ok := provider.Models[modelID]
	if !ok || !modelConfig.Enabled {
		return ai.Model{}, config.Provider{}, fmt.Errorf("group memory: model %q is missing or disabled on provider %q", modelID, provider.ID)
	}
	if modelConfig.ContextWindow < config.GroupMemoryMinimumContextWindow {
		return ai.Model{}, config.Provider{}, fmt.Errorf(
			"group memory: model %q context window is %d; structured Group Reflect requires at least %d",
			ref,
			modelConfig.ContextWindow,
			config.GroupMemoryMinimumContextWindow,
		)
	}
	apiName := provider.Type
	if apiName == "" {
		apiName = provider.ID
	}
	model := ai.Model{
		ID:            modelID,
		Name:          modelConfig.Name,
		API:           apiName,
		Provider:      provider.ID,
		BaseURL:       provider.BaseURL,
		Reasoning:     modelConfig.Reasoning,
		Input:         append([]string(nil), modelConfig.Input...),
		ContextWindow: modelConfig.ContextWindow,
		MaxTokens:     modelConfig.MaxTokens,
	}
	if model.Name == "" {
		model.Name = modelID
	}
	return model, provider, nil
}

func selectGroupReflectProvider(providers []config.Provider, ref string) (config.Provider, bool, bool) {
	for _, provider := range providers {
		if provider.ID == ref {
			return provider, true, false
		}
	}
	var selected config.Provider
	matches := 0
	for _, provider := range providers {
		if provider.Type == ref {
			selected = provider
			matches++
		}
	}
	return selected, matches == 1, matches > 1
}
