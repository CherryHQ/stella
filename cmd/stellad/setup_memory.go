package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/embedding"
	"github.com/CherryHQ/stella/internal/memory"
	memorylcm "github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/providers"
)

func buildMemorySummarizer(store config.Store, providerStreamBuilder agent.ProviderStreamBuilder) func(context.Context, string) (string, error) {
	return func(ctx context.Context, prompt string) (string, error) {
		agentID := authz.AgentIDFromContext(ctx)
		if agentID == "" {
			if agents, err := store.ListEnabledAgents(ctx); err == nil && len(agents) > 0 {
				agentID = agents[0].ID
			}
		}
		if agentID == "" {
			return "", fmt.Errorf("no agent ID in context for memory summarizer")
		}
		currentSnap, err := store.Snapshot(ctx, agentID)
		if err != nil {
			return "", fmt.Errorf("load summarizer snapshot: %w", err)
		}
		model := currentSnap.ResolveModelTier(config.ModelTierFast)
		creds := currentSnap.ResolveProviderCreds(model.Provider)
		apiName := creds.Type
		if apiName == "" {
			apiName = model.API
		}
		if apiName == "" {
			apiName = model.Provider
		}
		model.API = apiName
		model.BaseURL = creds.BaseURL

		stream, err := providerStreamBuilder(apiName, creds.APIKey, creds.BaseURL)
		if err != nil {
			return "", fmt.Errorf("build summarizer provider: %w", err)
		}
		maxTokens := 4096
		temperature := 0.0
		msg, err := providers.Complete(ctx, model, ai.Context{
			Messages: []ai.Message{ai.UserMessage{Content: prompt}},
		}, ai.CompleteOptions{StreamOptions: ai.StreamOptions{
			MaxTokens:   &maxTokens,
			Temperature: &temperature,
		}}, stream)
		if err != nil {
			return "", fmt.Errorf("summarize with model: %w", err)
		}
		text := strings.TrimSpace(ai.FlattenText(msg.Content))
		if text == "" {
			return "", fmt.Errorf("summarize with model: empty response")
		}
		return text, nil
	}
}

func setupMemoryProvider(_ context.Context, memDB *pgxpool.Pool, store config.Store, providerStreamBuilder agent.ProviderStreamBuilder, embeddingSvc *embedding.Service) (memory.Provider, error) {
	summarizer := buildMemorySummarizer(store, providerStreamBuilder)
	// Guard the option on the concrete pointer: passing a typed-nil *Service as the
	// QueryEmbedder interface would make a non-nil interface wrapping a nil pointer,
	// defeating the engine's nil check and panicking on first search.
	var opts []memorylcm.Option
	if embeddingSvc != nil {
		opts = append(opts, memorylcm.WithQueryEmbedder(embeddingSvc))
	}
	mem, err := memorylcm.New(memDB, summarizer, nil, opts...)
	if err != nil {
		return nil, fmt.Errorf("init lcm memory: %w", err)
	}
	return mem, nil
}

func wrapMemoryWithTracing(mem memory.Provider, poolMgr **agent.PoolManager) memory.Provider {
	return memory.WithTracing(mem, func() *hooks.HookSet {
		if *poolMgr == nil {
			return nil
		}
		return hooks.NewHookSet((*poolMgr).HookPlugins())
	})
}
