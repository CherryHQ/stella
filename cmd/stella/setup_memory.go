package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	memorylcm "github.com/CherryHQ/stella/internal/memory/lcm"
	memorysimple "github.com/CherryHQ/stella/internal/memory/simple"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/providers"
)

func buildMemorySummarizer(defaultAgentID string, store config.Store, providerStreamBuilder agent.ProviderStreamBuilder) func(context.Context, string) (string, error) {
	return func(ctx context.Context, prompt string) (string, error) {
		agentID := memory.AgentIDFromContext(ctx)
		if agentID == "" {
			agentID = defaultAgentID
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

func setupMemoryProvider(ctx context.Context, db *sql.DB, store config.Store, defaultAgentID string, providerStreamBuilder agent.ProviderStreamBuilder) (memory.Provider, error) {
	summarizer := buildMemorySummarizer(defaultAgentID, store, providerStreamBuilder)

	name := "lcm"
	cfg := map[string]any{}
	if plugins, err := store.ListPluginsByKind(ctx, config.PluginKindMemory); err == nil {
		for _, plugin := range plugins {
			if plugin.Enabled {
				name = plugin.Name
				cfg = plugin.Config
				break
			}
		}
	}
	return buildMemory(name, db, cfg, summarizer)
}

func wrapMemoryWithTracing(mem memory.Provider, poolMgr **agent.PoolManager) memory.Provider {
	return memory.WithTracing(mem, func() *hooks.HookSet {
		if *poolMgr == nil {
			return nil
		}
		return hooks.NewHookSet((*poolMgr).HookPlugins())
	})
}

func buildMemory(name string, db *sql.DB, cfg map[string]any, summarizerFn func(context.Context, string) (string, error)) (memory.Provider, error) {
	switch name {
	case "lcm":
		return memorylcm.New(db, summarizerFn, cfg)
	case "simple":
		return memorysimple.New(db), nil
	default:
		return nil, fmt.Errorf("unknown memory provider: %q", name)
	}
}
