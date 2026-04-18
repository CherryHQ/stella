package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"time"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/embedded"
	"github.com/vaayne/anna/internal/notify"
	"github.com/vaayne/anna/internal/pluginhost"
	"github.com/vaayne/anna/internal/pluginstate"
	"github.com/vaayne/anna/internal/scheduler"
	skills "github.com/vaayne/anna/internal/skills"
	internaltools "github.com/vaayne/anna/internal/tools"
	coreagent "github.com/vaayne/anna/pkg/agent"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	"github.com/vaayne/anna/pkg/hooks"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
	pluginhooks "github.com/vaayne/anna/plugins/hooks"
	plugintools "github.com/vaayne/anna/plugins/tools"
	mcpplugin "github.com/vaayne/anna/plugins/tools/mcp"
	skillsbuiltin "github.com/vaayne/anna/plugins/tools/skills/builtin"
)

func newApp() *ucli.App {
	return &ucli.App{
		Name:    "anna",
		Usage:   "A local AI assistant",
		Version: displayVersion(),
		Flags:   serverFlags(),
		Action:  serverAction,
		Commands: []*ucli.Command{
			chatCommand(),
			modelsCommand(),
			skillsCommand(),
			pluginCommand(),
			toolsCommand(),
			versionCommand(),
			upgradeCommand(),
		},
	}
}

type setupResult struct {
	ctx                    context.Context
	db                     *sql.DB
	mem                    memory.Provider
	snap                   *config.Snapshot
	store                  config.Store
	pluginHost             *pluginhost.Host
	channelRuntimeServices *pluginhost.ChannelPlatform
	poolManager            *agent.PoolManager
	pool                   *agent.Pool // default agent pool shared with CLI and channel entrypoints
	schedulerSvc           *scheduler.Service
	builtinTools           []tools.Tool
	notifier               *notify.Dispatcher
	pluginToolsBuilder     agent.PluginToolsBuilder
	promptToolsBuilder     func(context.Context) ([]pkgplugins.PromptToolInfo, error)
	promptSectionsBuilder  func(context.Context, pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error)
	toolLifecycle          *coreagent.ToolLifecycle
	cliUserID              int64 // resolved CLI user for session creation
}

func setup(parent context.Context, gateway bool) (*setupResult, error) {
	// Open DB.
	dbPath := config.DBPath()
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	store := config.NewDBStore(db)

	if err := embedded.EnsureTools(config.AnnaHome()); err != nil {
		return nil, fmt.Errorf("extract embedded tools: %w", err)
	}
	if err := embedded.VerifyTools(config.AnnaHome()); err != nil {
		return nil, err
	}

	if err := skillsbuiltin.ExtractSkills(filepath.Join(config.AnnaHome(), "skills")); err != nil {
		return nil, fmt.Errorf("extract builtin skills: %w", err)
	}

	skillStore := skills.New(db)
	if err := skills.SyncBuiltin(parent, skillStore, skillsbuiltin.BuiltinSkillFS()); err != nil {
		return nil, fmt.Errorf("sync builtin skills: %w", err)
	}

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
	ensureEnabledPluginBinaries(parent, phost, store, config.AnnaHome())
	if err := phost.ApplyPlugin(ctx, mcpplugin.PluginID); err != nil {
		return nil, fmt.Errorf("apply mcp runtime: %w", err)
	}

	// Create the shared scheduler service before runner construction so both
	// plugin-owned jobs and the user-facing scheduler tool can use it.
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

	builtinTools := []tools.Tool{scheduler.NewTool(schedulerSvc)}

	// Build memory provider through the plugin host so memory plugins use the same registration path.
	memoryName := "lcm"
	if plugins, err := store.ListPluginsByKind(parent, config.PluginKindMemory); err == nil {
		for _, plugin := range plugins {
			if plugin.Enabled {
				memoryName = plugin.Name
				break
			}
		}
	}
	memProvider, err := phost.BuildMemory(parent, memoryName, db, config.AnnaHome(), map[string]any{}, nil)
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
	providerRegistryBuilder := func(api, apiKey, baseURL string) (*providers.Registry, error) {
		return phost.BuildProviderRegistry(api, map[string]any{
			"api_key":  apiKey,
			"base_url": baseURL,
		})
	}
	reflectRuntimeServices.Set(ctx, memProvider, store, snap.Workspace, providerRegistryBuilder)

	// Plugin hooks builder: auto-discovers registered hook plugins and returns
	// enabled ones. Called at startup and on hot-reload.
	pluginHooksBuilder := func(ctx context.Context) []hooks.HookPlugin {
		return phost.BuildEnabledHooks(ctx, pluginhooks.BuildContext{ToolsBinDir: embedded.BinDir(config.AnnaHome())})
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

	poolMgr = agent.NewPoolManager(store, memProvider,
		agent.WithIdleTimeoutPM(idleTimeout),
		agent.WithCompactionPM(agent.CompactionConfig{
			MaxTokens: snap.Runner.Compaction.MaxTokens,
			KeepTail:  snap.Runner.Compaction.KeepTail,
		}.WithDefaults()),
		agent.WithBuiltinTools(builtinTools),
		agent.WithPluginToolsBuilder(pluginToolsBuilder),
		agent.WithPluginHooksBuilder(pluginHooksBuilder),
		agent.WithProviderRegistryBuilder(providerRegistryBuilder),
		agent.WithPromptToolsBuilder(promptToolsBuilder),
		agent.WithPromptSectionsBuilder(promptSectionsBuilder),
		agent.WithBeforeRunBuilderPM(func(ctx context.Context, build pkgplugins.BeforeRunContext) (pkgplugins.BeforeRunResult, error) {
			return phost.BeforeRun(ctx, build)
		}),
		agent.WithToolLifecyclePM(toolLifecycle),
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
		}, func(ctx context.Context, sessionID, message, model string) <-chan runner.Event {
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

	return &setupResult{
		ctx:                    ctx,
		db:                     db,
		mem:                    memProvider,
		snap:                   snap,
		store:                  store,
		pluginHost:             phost,
		channelRuntimeServices: channelRuntimeServices,
		poolManager:            poolMgr,
		pool:                   pool,
		schedulerSvc:           schedulerSvc,
		builtinTools:           builtinTools,
		notifier:               dispatcher,
		pluginToolsBuilder:     pluginToolsBuilder,
		promptToolsBuilder:     promptToolsBuilder,
		promptSectionsBuilder:  promptSectionsBuilder,
		toolLifecycle:          toolLifecycle,
		cliUserID:              cliUserID,
	}, nil
}

// modelSwitcher returns a function that switches the pool's runner factory
// to use a different provider/model combination.
// Each switch creates a new immutable snapshot so the factory closure captures
// no shared mutable state — eliminating races between concurrent Chat calls and
// model switches. Hooks are stored on the Pool independently and are not affected.
func modelSwitcher(base *config.Snapshot, store config.Store, pool *agent.Pool, builtinTools []tools.Tool, pluginToolsBuilder agent.PluginToolsBuilder, providerRegistryBuilder func(api, apiKey, baseURL string) (*providers.Registry, error), promptToolsFn func(context.Context) ([]pkgplugins.PromptToolInfo, error), promptSectionsFn func(context.Context, pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error), toolLifecycle *coreagent.ToolLifecycle) func(string, string) error {
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

		factory, err := agent.NewRunnerFactory(&snap, builtinTools, pluginToolsBuilder, providerRegistryBuilder, promptToolsFn, promptSectionsFn, toolLifecycle)
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
		func(api, apiKey, baseURL string) (*providers.Registry, error) {
			provider, err := s.store.GetProvider(s.ctx, api)
			if err != nil {
				return nil, err
			}
			providerType := provider.Type
			if providerType == "" {
				providerType = provider.ID
			}
			return s.pluginHost.BuildProviderRegistry(providerType, map[string]any{
				"api_key":  apiKey,
				"base_url": baseURL,
			})
		},
		s.promptToolsBuilder,
		s.promptSectionsBuilder,
		s.toolLifecycle,
	)
}

func setupLogFile() error {
	logPath := filepath.Join(config.AnnaHome(), "anna.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	return nil
}

// ensureEnabledPluginBinaries downloads missing or broken binaries for all
// currently-enabled plugins and runs their PostInstall hooks. Called at startup
// so plugins enabled by default (or enabled before the current session) have
// their binaries ready without requiring a UI toggle.
func ensureEnabledPluginBinaries(ctx context.Context, phost *pluginhost.Host, store config.Store, annaHome string) {
	plugins, err := store.ListPlugins(ctx)
	if err != nil {
		slog.Warn("could not list plugins for binary ensure", "error", err)
		return
	}
	for _, p := range plugins {
		if p.Enabled {
			internaltools.EnsurePluginBinaries(ctx, phost.BinarySpecs(p.ID), annaHome, nil)
		}
	}
}
