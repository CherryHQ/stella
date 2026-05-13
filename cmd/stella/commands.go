package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"strings"
	"sync"
	"time"

	ucli "github.com/urfave/cli/v2"
	"golang.org/x/oauth2"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/cli"
	"github.com/CherryHQ/stella/internal/config"
	oauth "github.com/CherryHQ/stella/internal/credentials/oauth"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/manifestplugins"
	"github.com/CherryHQ/stella/internal/notify"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/pluginstate"
	"github.com/CherryHQ/stella/internal/scheduler"
	skills "github.com/CherryHQ/stella/internal/skills"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/memory"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/tools"
	pluginhooks "github.com/CherryHQ/stella/plugins/hooks"
	plugintools "github.com/CherryHQ/stella/plugins/tools"
	mcpplugin "github.com/CherryHQ/stella/plugins/tools/mcp"
	"github.com/CherryHQ/stella/resources"
	"github.com/CherryHQ/stella/resources/binaries"
)

func newApp() *ucli.App {
	return &ucli.App{
		Name:    "stella",
		Usage:   "A local AI assistant",
		Version: displayVersion(),
		Flags:   serverFlags(),
		Action:  serverAction,
		Commands: []*ucli.Command{
			modelsCommand(),
			skillsCommand(),
			pluginCommand(),
			versionCommand(),
			upgradeCommand(),
			recallyCommand(),
			schedulerCommand(),
			emailCommand(),
			serviceCommand(),
		},
	}
}

type setupResult struct {
	ctx                      context.Context
	db                       *sql.DB
	mem                      memory.Provider
	snap                     *config.Snapshot
	store                    config.Store
	pluginHost               *pluginhost.Host
	channelRuntimeServices   *pluginhost.ChannelPlatform
	poolManager              *agent.PoolManager
	pool                     *agent.Pool // default agent pool shared with CLI and channel entrypoints
	schedulerSvc             *scheduler.Service
	builtinTools             []tools.Tool
	notifier                 *notify.Dispatcher
	pluginToolsBuilder       agent.PluginToolsBuilder
	promptToolsBuilder       prompt.ToolsBuilder
	promptSectionsBuilder    prompt.SectionsBuilder
	sessionPluginViewBuilder agent.SessionPluginViewBuilder
	toolLifecycle            *coreagent.ToolLifecycle
	skillStore               pkgplugins.SkillStore
	cliUserID                int64 // resolved CLI user for session creation
	oauthRegistry            *oauth.ProviderRegistry
	backgroundTasks          *sync.WaitGroup
}

func setup(parent context.Context, gateway bool) (*setupResult, error) {
	// Open DB.
	dbPath := config.DBPath()
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	store := config.NewDBStore(db)

	if err := binaries.EnsureTools(config.StellaHome()); err != nil {
		return nil, fmt.Errorf("extract embedded tools: %w", err)
	}
	if err := binaries.VerifyTools(config.StellaHome()); err != nil {
		return nil, err
	}
	if err := cli.EnsureStellaCLIInPath(config.StellaHome()); err != nil {
		return nil, fmt.Errorf("copy stella cli into sandbox path: %w", err)
	}
	if err := resources.EnsureBuiltinSkills(filepath.Join(config.StellaHome(), ".agents", "skills")); err != nil {
		return nil, fmt.Errorf("extract builtin skills: %w", err)
	}

	rawSkillStore := skills.New(db)
	skillStore := skills.NewDiskSyncStore(rawSkillStore, func(scope, agentID string, userID int64) string {
		base := config.StellaHome()
		switch scope {
		case "system":
			return filepath.Join(base, ".agents", "skills")
		case "agent":
			if agentID == "" {
				return ""
			}
			return filepath.Join(base, "workspaces", agentID, ".agents", "skills")
		case "user":
			if agentID == "" || userID == 0 {
				return ""
			}
			return filepath.Join(base, "workspaces", agentID, "users", fmt.Sprintf("%d", userID), ".agents", "skills")
		default:
			return ""
		}
	})

	builtinSkillsFS, ok := resources.SubFS(resources.KindSkill)
	if !ok {
		return nil, fmt.Errorf("builtin skills FS unavailable")
	}
	if err := skills.SyncBuiltin(parent, skillStore, builtinSkillsFS); err != nil {
		return nil, fmt.Errorf("sync builtin skills: %w", err)
	}
	if err := skillStore.SyncAllToDisk(parent); err != nil {
		return nil, fmt.Errorf("sync skills to disk: %w", err)
	}

	backgroundTasks := &sync.WaitGroup{}

	// OAuth provider registry populated from manifest below.
	var (
		oauthRegistry       *oauth.ProviderRegistry
		manifestToReconcile *manifestplugins.Manifest
	)

	// Seed defaults so there's always at least one agent.
	if err := store.SeedDefaults(parent); err != nil {
		return nil, fmt.Errorf("seed defaults: %w", err)
	}

	// Seed auth policies.
	authStore := appdb.NewAuthStore(db)
	if err := auth.SeedPolicies(parent, authStore); err != nil {
		return nil, fmt.Errorf("seed auth: %w", err)
	}

	// Get snapshot for the default agent (used for global settings).
	agents, err := store.ListEnabledAgents(parent)
	if err != nil || len(agents) == 0 {
		return nil, fmt.Errorf("no enabled agents found")
	}
	defaultAgentID := agents[0].ID

	snap, err := store.Snapshot(parent, defaultAgentID)
	if err != nil {
		return nil, fmt.Errorf("load config snapshot: %w", err)
	}

	ctx := parent

	channelRuntimeServices := pluginhost.NewChannelRuntimeServices()
	reflectRuntimeServices := pluginhost.NewReflectRuntimeServices()
	dispatcher := notify.NewDispatcher()
	dispatcher.SetChannelStore(store)
	stateStore := pluginstate.New(db)
	phost := pluginhost.New(store,
		pluginhost.WithAuthService(pluginhost.NewAuthService(authStore)),
		pluginhost.WithNotificationService(dispatcher),
		pluginhost.WithStateStore(stateStore),
		pluginhost.WithSkillStore(skillStore),
		pluginhost.WithChannelRuntimeServices(channelRuntimeServices),
		pluginhost.WithReflectRuntimeServices(reflectRuntimeServices),
	)
	if err := phost.LoadDefaultCatalog(); err != nil {
		return nil, fmt.Errorf("load plugin catalog: %w", err)
	}

	// Load and reconcile manifest tool plugins.
	{
		builtinManifest, err := manifestplugins.LoadBuiltin()
		if err != nil {
			slog.Warn("manifest plugin: failed to load builtin manifest", "error", err)
		} else {
			userManifestPath := filepath.Join(config.StellaHome(), "plugins.yaml")
			userManifest, err := manifestplugins.LoadUser(userManifestPath)
			if err != nil {
				slog.Warn("manifest plugin: failed to load user manifest", "path", userManifestPath, "error", err)
				userManifest = &manifestplugins.Manifest{}
			}
			merged := manifestplugins.Merge(builtinManifest, userManifest)
			manifestToReconcile = merged
			phost.RegisterManifestPlugins(merged)

			// Build OAuth provider registry from manifest.
			oauthRegistry = oauth.NewProviderRegistry()
			for _, op := range merged.OAuthProviders {
				flows := make([]oauth.ProviderFlowConfig, 0, len(op.Flows))
				for _, f := range op.Flows {
					var authStyle oauth2.AuthStyle
					switch f.AuthStyle {
					case "in_params":
						authStyle = oauth2.AuthStyleInParams
					case "in_header":
						authStyle = oauth2.AuthStyleInHeader
					default:
						authStyle = oauth2.AuthStyleAutoDetect
					}
					flows = append(flows, oauth.ProviderFlowConfig{
						Type:          f.Type,
						AuthURL:       f.AuthURL,
						DeviceAuthURL: f.DeviceAuthURL,
						TokenURL:      f.TokenURL,
						AuthStyle:     authStyle,
						PKCE:          f.PKCE,
					})
				}
				oauthRegistry.Register(oauth.ProviderConfig{
					ID:           op.ID,
					Scopes:       op.Scopes,
					VaultKey:     op.VaultKey,
					Flows:        flows,
					ClientID:     op.ClientID,
					ClientSecret: op.ClientSecret,
				})
			}

		}
	}

	if err := phost.ApplyPlugin(ctx, mcpplugin.PluginID); err != nil {
		return nil, fmt.Errorf("apply mcp runtime: %w", err)
	}

	// Create the shared scheduler service before runner construction so both
	// plugin-owned jobs and the user-facing scheduler skill can use it.
	// Reuse the process-wide DB handle so scheduler persistence and memory writes
	// do not contend through separate SQLite pools.
	schedulerSvc, err := scheduler.New(db)
	if err != nil {
		return nil, fmt.Errorf("create scheduler service: %w", err)
	}
	dataDir := snap.Scheduler.DataDir
	if dataDir == "" {
		dataDir = filepath.Join(snap.Workspace, "scheduler")
	}
	schedulerSvc.SetLegacyDataPath(dataDir)
	schedulerSvc.SetUserJobsEnabled(snap.Scheduler.IsEnabled())
	phost.SetSchedulerService(newSchedulerServiceAdapter(schedulerSvc, phost.Runtime()))

	builtinTools := []tools.Tool{}

	providerStreamBuilder := func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
		return phost.BuildStreamFunc(api, map[string]any{
			"api_key":  apiKey,
			"base_url": baseURL,
		})
	}

	memorySummarizer := func(ctx context.Context, prompt string) (string, error) {
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

	// Build memory provider through the plugin host so memory plugins use the same registration path.
	memoryName := "lcm"
	memoryConfig := map[string]any{}
	if plugins, err := store.ListPluginsByKind(parent, config.PluginKindMemory); err == nil {
		for _, plugin := range plugins {
			if plugin.Enabled {
				memoryName = plugin.Name
				memoryConfig = plugin.Config
				break
			}
		}
	}
	memProvider, err := phost.BuildMemory(parent, memoryName, db, config.StellaHome(), memoryConfig, memorySummarizer)
	if err != nil {
		return nil, fmt.Errorf("memory plugin: %w", err)
	}

	// Wrap with tracing. The hooksFn closure captures poolMgr which is nil now
	// but populated before any memory operations run.
	var poolMgr *agent.PoolManager
	memProvider = memory.WithTracing(memProvider, func() *hooks.HookSet {
		if poolMgr == nil {
			return nil
		}
		return hooks.NewHookSet(poolMgr.HookPlugins())
	})

	// Unified memory tool (always available to every agent, adapts to provider capabilities).
	builtinTools = append(builtinTools,
		memory.BuildTool(memProvider),
	)

	// Plugin tools builder: auto-discovers registered plugin tools and returns
	// enabled ones. Called per runner so builders receive the active sandbox host.
	pluginToolsBuilder := func(ctx context.Context, build plugintools.BuildContext) []tools.Tool {
		return phost.BuildEnabledTools(ctx, build)
	}
	reflectRuntimeServices.Set(ctx, memProvider, store, snap.Workspace, providerStreamBuilder)

	// Plugin hooks builder: auto-discovers registered hook plugins and returns
	// enabled ones. Called at startup and on hot-reload.
	pluginHooksBuilder := func(ctx context.Context) []hooks.HookPlugin {
		return phost.BuildEnabledHooks(ctx, pluginhooks.BuildContext{ToolsBinDir: binaries.BinDir(config.StellaHome())})
	}

	idleTimeout := time.Duration(snap.Runner.IdleTimeout) * time.Minute

	// Create PoolManager with builtin tools and external plugin tools.
	// WithBuiltinTools sets the always-on builtin tools (scheduler, memory).
	// WithPluginToolsBuilder provides the function for hot-reloadable external tools.
	// WithPluginHooksBuilder provides the function for hot-reloadable hook plugins.
	toolLifecycle := &coreagent.ToolLifecycle{
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
	promptToolsBuilder := func(ctx context.Context) ([]pkgplugins.PromptToolInfo, error) {
		return phost.PromptTools(ctx, mcpplugin.PluginID)
	}
	promptSectionsBuilder := func(ctx context.Context, build pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error) {
		return phost.SystemPromptSections(ctx, build)
	}
	sessionPluginViewBuilder := func(ctx context.Context) (pkgplugins.SessionPluginView, error) {
		return phost.SessionPluginView(ctx)
	}

	poolMgr = agent.NewPoolManager(store, memProvider,
		agent.WithIdleTimeoutPM(idleTimeout),
		agent.WithCompactionPM(agent.CompactionConfig{
			MaxTokens: snap.Runner.Compaction.MaxTokens,
			KeepTail:  snap.Runner.Compaction.KeepTail,
		}.WithDefaults()),
		agent.WithBuiltinTools(builtinTools),
		agent.WithPluginToolsBuilder(pluginToolsBuilder),
		agent.WithPluginHooksBuilder(pluginHooksBuilder),
		agent.WithProviderStreamBuilder(providerStreamBuilder),
		agent.WithPromptToolsBuilder(promptToolsBuilder),
		agent.WithPromptSectionsBuilder(promptSectionsBuilder),
		agent.WithSessionPluginViewBuilder(sessionPluginViewBuilder),
		agent.WithBeforeRunBuilderPM(func(ctx context.Context, build pkgplugins.BeforeRunContext) (pkgplugins.BeforeRunResult, error) {
			return phost.BeforeRun(ctx, build)
		}),
		agent.WithToolLifecyclePM(toolLifecycle),
		agent.WithSkillStore(pluginhost.NewSkillStoreAdapter(skillStore)),
	)

	if err := poolMgr.StartAll(ctx); err != nil {
		return nil, fmt.Errorf("start pool manager: %w", err)
	}

	// Default pool for backward compat with CLI/channel code.
	pool := poolMgr.Get(defaultAgentID)
	if pool == nil {
		pool = poolMgr.DefaultPool()
	}

	// Wire heartbeat on the scheduler service if enabled.
	if schedulerSvc != nil && snap.Heartbeat.IsEnabled() {
		schedulerSvc.SetHeartbeat(scheduler.HeartbeatConfig{
			File:      snap.Heartbeat.FilePath(snap.Workspace),
			FastModel: snap.ResolveModelID(config.ModelTierFast),
		}, func(ctx context.Context, sessionID, message, model string) <-chan agent.Event {
			if model != "" {
				return pool.Chat(ctx, sessionID, message, agent.WithModel(model))
			}
			return pool.Chat(ctx, sessionID, message)
		}, dispatcher)
	}

	// Wire the scheduler callback now that pool exists.
	// Route to the correct pool via PoolManager when the job has an AgentID.
	if schedulerSvc != nil {
		schedulerSvc.SetOnJob(func(ctx context.Context, job scheduler.Job) error {
			if scheduler.IsPluginJob(job) {
				return nil
			}
			targetPool := pool
			if job.AgentID != "" {
				if p := poolMgr.Get(job.AgentID); p != nil {
					targetPool = p
				}
			}
			sessionID := job.SessionID()
			ch := targetPool.Chat(schedulerJobContext(ctx, targetPool, job), sessionID, schedulerJobMessage(job))
			var runErr error
			for evt := range ch {
				if evt.Err != nil {
					runErr = evt.Err
					slog.Error("scheduler job error", "job_id", job.ID, "error", evt.Err)
				}
			}
			return runErr
		})
	}

	// CLI sessions don't use auth — userID stays 0.
	var cliUserID int64

	if manifestToReconcile != nil {
		reconcileManifestPluginsInBackground(ctx, backgroundTasks, manifestToReconcile, config.StellaHome())
	}

	return &setupResult{
		ctx:                      ctx,
		db:                       db,
		mem:                      memProvider,
		snap:                     snap,
		store:                    store,
		pluginHost:               phost,
		channelRuntimeServices:   channelRuntimeServices,
		poolManager:              poolMgr,
		pool:                     pool,
		schedulerSvc:             schedulerSvc,
		builtinTools:             builtinTools,
		notifier:                 dispatcher,
		pluginToolsBuilder:       pluginToolsBuilder,
		promptToolsBuilder:       promptToolsBuilder,
		promptSectionsBuilder:    promptSectionsBuilder,
		sessionPluginViewBuilder: sessionPluginViewBuilder,
		toolLifecycle:            toolLifecycle,
		skillStore:               pluginhost.NewSkillStoreAdapter(skillStore),
		cliUserID:                cliUserID,
		oauthRegistry:            oauthRegistry,
		backgroundTasks:          backgroundTasks,
	}, nil
}

func reconcileManifestPluginsInBackground(ctx context.Context, wg *sync.WaitGroup, m *manifestplugins.Manifest, stellaHome string) {
	wg.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("manifest plugin reconcile panic", "panic", r)
			}
		}()
		slog.Info("manifest plugin reconcile queued in background")
		manifestplugins.Reconcile(ctx, m, stellaHome)
	})
}

func (s *setupResult) waitBackgroundTasks() {
	if s != nil && s.backgroundTasks != nil {
		s.backgroundTasks.Wait()
	}
}

// modelSwitcher returns a function that switches the pool's runner factory
// to use a different provider/model combination.
// Each switch creates a new immutable snapshot so the factory closure captures
// no shared mutable state — eliminating races between concurrent Chat calls and
// model switches. Hooks are stored on the Pool independently and are not affected.
func modelSwitcher(base *config.Snapshot, store config.Store, pool *agent.Pool, builtinTools []tools.Tool, pluginToolsBuilder agent.PluginToolsBuilder, providerStreamBuilder agent.ProviderStreamBuilder, promptToolsFn prompt.ToolsBuilder, promptSectionsFn prompt.SectionsBuilder, sessionPluginViewFn agent.SessionPluginViewBuilder, toolLifecycle *coreagent.ToolLifecycle, skillStore pkgplugins.SkillStore) func(string, string) error {
	return func(provider, model string) error {
		// Shallow-copy the base snapshot so we never mutate shared state.
		snap := *base
		snap.Provider = provider
		snap.Model = provider + "/" + model

		// Fetch fresh provider credentials and record them in the copy's map.
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
			SkillStore:               skillStore,
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
		s.skillStore,
	)
}
