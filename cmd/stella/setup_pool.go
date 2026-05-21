package main

import (
	"context"
	"database/sql"
	"maps"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/pluginhost"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

func buildToolLifecycle(phost *pluginhost.Host) *coreagent.ToolLifecycle {
	return &coreagent.ToolLifecycle{
		BeforeCall: func(ctx context.Context, call coreagent.ToolCallContext) (coreagent.ToolCallMutation, error) {
			result, err := phost.BeforeToolCall(ctx, pkgplugins.BeforeToolCallContext{
				SessionID:  call.SessionID,
				Channel:    call.Channel,
				UserID:     call.UserID,
				AgentID:    call.AgentID,
				ToolName:   call.ToolName,
				ToolCallID: call.ToolCallID,
				Arguments:  call.Arguments,
			})
			if err != nil {
				return coreagent.ToolCallMutation{}, err
			}
			return coreagent.ToolCallMutation{
				Arguments:    result.Arguments,
				Block:        result.Block,
				BlockMessage: result.BlockMessage,
			}, nil
		},
		AfterCall: func(ctx context.Context, result coreagent.ToolResultContext) (coreagent.ToolResultMutation, error) {
			mutation, err := phost.AfterToolResult(ctx, pkgplugins.AfterToolResultContext{
				SessionID:  result.SessionID,
				Channel:    result.Channel,
				UserID:     result.UserID,
				AgentID:    result.AgentID,
				ToolName:   result.ToolName,
				ToolCallID: result.ToolCallID,
				Arguments:  result.Arguments,
				Result:     result.Result,
				IsError:    result.IsError,
				Duration:   result.Duration,
			})
			if err != nil {
				return coreagent.ToolResultMutation{}, err
			}
			return coreagent.ToolResultMutation{
				Result:  mutation.Result,
				IsError: mutation.IsError,
			}, nil
		},
	}
}

func buildProjectEnsurer(db *sql.DB, store config.Store) agent.ProjectEnsurerFunc {
	return func(ctx context.Context, agentID, userID string) (string, error) {
		q := sqlc.New(db)
		projects, err := q.ListProjects(ctx, sqlc.ListProjectsParams{AgentID: agentID, UserID: userID})
		if err != nil {
			return "", err
		}
		if len(projects) > 0 {
			return projects[0].ID, nil
		}
		agentName := agentID
		if ag, err := store.GetAgent(ctx, agentID); err == nil && ag.Name != "" {
			agentName = ag.Name
		}
		userRoot, err := agent.SetupUserWorkspace(agentID, config.StellaHome(), userID)
		if err != nil {
			return "", err
		}
		p, err := q.CreateProject(ctx, sqlc.CreateProjectParams{
			ID:      uuid.NewString(),
			AgentID: agentID,
			UserID:  userID,
			Name:    agentName,
			BaseDir: userRoot,
		})
		if err != nil {
			if existing, err2 := q.ListProjects(ctx, sqlc.ListProjectsParams{AgentID: agentID, UserID: userID}); err2 == nil && len(existing) > 0 {
				return existing[0].ID, nil
			}
			return "", err
		}
		return p.ID, nil
	}
}

func modelSwitcher(base *config.Snapshot, store config.Store, pool *agent.Pool, builtinTools []pkgtools.Tool, pluginToolsBuilder agent.PluginToolsBuilder, providerStreamBuilder agent.ProviderStreamBuilder, promptToolsFn prompt.ToolsBuilder, promptSectionsFn prompt.SectionsBuilder, sessionPluginViewFn agent.SessionPluginViewBuilder, toolLifecycle *coreagent.ToolLifecycle) func(string, string) error {
	return func(provider, model string) error {
		snap := *base
		snap.Provider = provider
		snap.Model = provider + "/" + model

		if p, err := store.GetProvider(context.Background(), provider); err == nil {
			providers := make(map[string]config.ProviderCreds, len(base.Providers)+1)
			maps.Copy(providers, base.Providers)
			providers[provider] = config.ProviderCreds{Type: p.Type, APIKey: p.APIKey, BaseURL: p.BaseURL}
			snap.Providers = providers
		}

		factory, err := agent.NewRunnerFactory(agent.RunnerFactoryConfig{
			Snap:                     &snap,
			BuiltinTools:             builtinTools,
			PluginToolsBuilder:       pluginToolsBuilder,
			ProviderStreamBuilder:    providerStreamBuilder,
			PromptToolsBuilder:       promptToolsFn,
			PromptSectionsBuilder:    promptSectionsFn,
			SessionPluginViewBuilder: sessionPluginViewFn,
			ToolLifecycle:            toolLifecycle,
		})
		if err != nil {
			return err
		}
		pool.SetFactory(factory)
		pool.SetDefaultModel(snap.Model)
		return nil
	}
}

func (s *setupResult) modelListFunc(snap *config.Snapshot) func() []pkgchannel.ModelOption {
	return func() []pkgchannel.ModelOption {
		return collectModelsFromStore(s.ctx, s.store, snap)
	}
}

func (s *setupResult) modelSwitchFunc(snap *config.Snapshot, pool *agent.Pool) func(string, string) error {
	return modelSwitcher(
		snap,
		s.store,
		pool,
		s.builtinTools,
		s.pluginToolsBuilder,
		func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
			provider, err := s.store.GetProvider(s.ctx, api)
			if err != nil {
				return nil, err
			}
			providerType := provider.Type
			if providerType == "" {
				providerType = provider.ID
			}
			return s.pluginHost.BuildStreamFunc(providerType, map[string]any{
				"api_key":  apiKey,
				"base_url": baseURL,
			})
		},
		s.promptToolsBuilder,
		s.promptSectionsBuilder,
		s.sessionPluginViewBuilder,
		s.toolLifecycle,
	)
}
