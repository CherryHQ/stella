package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/embedded"
	annamcp "github.com/vaayne/anna/internal/mcp"
	"github.com/vaayne/anna/internal/scheduler"
	"github.com/vaayne/anna/pkg/hooks"
	"github.com/vaayne/anna/pkg/memory"
	"github.com/vaayne/anna/pkg/tools"
	pluginhooks "github.com/vaayne/anna/plugins/hooks"
	pluginmemory "github.com/vaayne/anna/plugins/memory"
	plugintools "github.com/vaayne/anna/plugins/tools"
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
			versionCommand(),
			upgradeCommand(),
		},
	}
}

type setupResult struct {
	ctx          context.Context
	db           *sql.DB
	mem          memory.Provider
	snap         *config.Snapshot
	store        config.Store
	mcpManager   *annamcp.Manager
	poolManager  *agent.PoolManager
	pool         *agent.Pool // default agent's pool (backward compat)
	schedulerSvc *scheduler.Service
	extraTools   []tools.Tool
	notifier     *channel.Dispatcher
	cliUserID    int64 // resolved CLI user for session creation
}

func setup(parent context.Context, gateway bool) (*setupResult, error) {
	// Open DB.
	dbPath := config.DBPath()
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	store := config.NewDBStore(db)

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

	ctx, cancel := context.WithCancel(parent)
	_ = cancel // cancel is deferred via the caller's lifecycle

	mcpManager := annamcp.NewManager()
	annamcp.SetDefaultManager(mcpManager)
	if mcpCfg, mcpEnabled, err := annamcp.LoadPluginState(parent, store); err == nil {
		mcpManager.Reconcile(ctx, mcpCfg, mcpEnabled)
	} else {
		return nil, fmt.Errorf("load mcp plugin state: %w", err)
	}

	// Create scheduler service and tool before the runner factory so the tool
	// can be injected into the Go runner.
	var schedulerSvc *scheduler.Service
	var sharedTools []tools.Tool
	if snap.Scheduler.IsEnabled() || (gateway && snap.Heartbeat.IsEnabled()) {
		schedulerSvc, err = scheduler.NewFromPath(dbPath)
		if err != nil {
			return nil, fmt.Errorf("create scheduler service: %w", err)
		}
		dataDir := snap.Scheduler.DataDir
		if dataDir == "" {
			dataDir = filepath.Join(snap.Workspace, "scheduler")
		}
		schedulerSvc.SetLegacyDataPath(dataDir)
		if snap.Scheduler.IsEnabled() {
			sharedTools = append(sharedTools, scheduler.NewTool(schedulerSvc))
		}
	}

	// Notification dispatcher + tool.
	dispatcher := channel.NewDispatcher()
	// The notify tool is wired in gateway mode — channels register later.

	// Build memory provider via plugin registry.
	memProvider, err := pluginmemory.Build(parent, "lcm", pluginmemory.BuildContext{
		DB:       db,
		AnnaHome: config.AnnaHome(),
	})
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

	// Unified memory tool (shared across all agents, adapts to provider capabilities).
	sharedTools = append(sharedTools,
		memory.BuildTool(memProvider),
	)

	// Plugin tools builder: auto-discovers registered plugin tools and returns
	// enabled ones. Called at startup and on hot-reload.
	pluginToolsBuilder := func(ctx context.Context) []tools.Tool {
		return plugintools.BuildEnabled(plugintools.BuildContext{}, func(name string) bool {
			p, err := store.GetPlugin(ctx, config.PluginID(config.PluginKindTool, name))
			return err == nil && p.Enabled
		})
	}

	// Plugin hooks builder: auto-discovers registered hook plugins and returns
	// enabled ones. Called at startup and on hot-reload.
	pluginHooksBuilder := func(ctx context.Context) []hooks.HookPlugin {
		return pluginhooks.BuildEnabled(pluginhooks.BuildContext{
			ToolsBinDir: embedded.BinDir(config.AnnaHome()),
		}, func(name string) bool {
			p, err := store.GetPlugin(ctx, config.PluginID(config.PluginKindHook, name))
			return err == nil && p.Enabled
		})
	}

	idleTimeout := time.Duration(snap.Runner.IdleTimeout) * time.Minute

	// Create PoolManager with shared tools, plugin builder, and hooks builder.
	// WithSharedExtraTools sets the always-on core tools (scheduler, memory).
	// WithPluginToolsBuilder provides the function for hot-reloadable plugin tools.
	// WithPluginHooksBuilder provides the function for hot-reloadable hook plugins.
	poolMgr = agent.NewPoolManager(store, memProvider,
		agent.WithIdleTimeoutPM(idleTimeout),
		agent.WithCompactionPM(agent.CompactionConfig{
			MaxTokens: snap.Runner.Compaction.MaxTokens,
			KeepTail:  snap.Runner.Compaction.KeepTail,
		}.WithDefaults()),
		agent.WithSharedExtraTools(sharedTools),
		agent.WithPluginToolsBuilder(pluginToolsBuilder),
		agent.WithPluginHooksBuilder(pluginHooksBuilder),
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
		schedulerSvc.SetOnJob(func(ctx context.Context, job scheduler.Job) {
			targetPool := pool
			if job.AgentID != "" {
				if p := poolMgr.Get(job.AgentID); p != nil {
					targetPool = p
				}
			}
			sessionID := job.SessionID()
			msg := fmt.Sprintf("[Scheduled Task] %s\n\nInstruction: %s", job.Name, job.Message)
			ch := targetPool.Chat(ctx, sessionID, msg)
			for evt := range ch {
				if evt.Err != nil {
					slog.Error("scheduler job error", "job_id", job.ID, "error", evt.Err)
				}
			}
		})
	}

	// CLI sessions don't use auth — userID stays 0.
	var cliUserID int64

	return &setupResult{
		ctx:          ctx,
		db:           db,
		mem:          memProvider,
		snap:         snap,
		store:        store,
		mcpManager:   mcpManager,
		poolManager:  poolMgr,
		pool:         pool,
		schedulerSvc: schedulerSvc,
		extraTools:   sharedTools,
		notifier:     dispatcher,
		cliUserID:    cliUserID,
	}, nil
}

// modelSwitcher returns a function that switches the pool's runner factory
// to use a different provider/model combination.
// Each switch creates a new immutable snapshot so the factory closure captures
// no shared mutable state — eliminating races between concurrent Chat calls and
// model switches. Hooks are stored on the Pool independently and are not affected.
func modelSwitcher(base *config.Snapshot, store config.Store, pool *agent.Pool, extraTools []tools.Tool) func(string, string) error {
	return func(provider, model string) error {
		// Shallow-copy the base snapshot so we never mutate shared state.
		snap := *base
		snap.Provider = provider
		snap.Model = provider + "/" + model

		// Fetch fresh provider credentials and record them in the copy's map.
		if p, err := store.GetProvider(context.Background(), provider); err == nil {
			providers := make(map[string]config.ProviderCreds, len(base.Providers)+1)
			for k, v := range base.Providers {
				providers[k] = v
			}
			providers[provider] = config.ProviderCreds{APIKey: p.APIKey, BaseURL: p.BaseURL}
			snap.Providers = providers
		}

		factory, err := agent.NewRunnerFactory(&snap, extraTools)
		if err != nil {
			return err
		}
		pool.SetFactory(factory)
		pool.SetDefaultModel(snap.Model)
		return nil
	}
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
