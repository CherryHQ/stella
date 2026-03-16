package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/agent/runner"
	agenttool "github.com/vaayne/anna/internal/agent/tool"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/memory"
	memorytool "github.com/vaayne/anna/internal/memory/tool"
	pluginmgr "github.com/vaayne/anna/internal/plugin"
	"github.com/vaayne/anna/internal/scheduler"
	"github.com/vaayne/anna/internal/skills"
)

func newApp() *ucli.App {
	return &ucli.App{
		Name:    "anna",
		Usage:   "A local AI assistant",
		Version: displayVersion(),
		Commands: []*ucli.Command{
			chatCommand(),
			gatewayCommand(),
			modelsCommand(),
			skillsCommand(),
			pluginCommand(),
			onboardCommand(),
			versionCommand(),
			upgradeCommand(),
		},
	}
}

type setupResult struct {
	ctx          context.Context
	snap         *config.Snapshot
	store        config.Store
	pool         *agent.Pool
	schedulerSvc *scheduler.Service
	extraTools   []agenttool.Tool
	notifier     *channel.Dispatcher
	pluginMgr    *pluginmgr.Manager
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

	// Get snapshot for the default agent.
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

	// Create scheduler service and tool before the runner factory so the tool
	// can be injected into the Go runner.
	var schedulerSvc *scheduler.Service
	var extraTools []agenttool.Tool
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
			extraTools = append(extraTools, scheduler.NewTool(schedulerSvc))
		}
	}

	// Skills tool — always available.
	cwd, _ := os.Getwd()
	extraTools = append(extraTools, skills.NewTool(config.AnnaHome(), snap.Workspace, cwd))

	// Notification dispatcher + tool.
	dispatcher := channel.NewDispatcher()
	// The notify tool is wired in gateway mode — channels register later.

	// Memory engine: create engine for message persistence and compaction.
	memoryEngine := memory.NewEngineFromDB(db, &memory.StaticSummarizer{Response: "compacted"}, memory.WithLogger(slog.Default()))

	// Memory retrieval tools.
	extraTools = append(extraTools,
		memorytool.NewGrepTool(memoryEngine),
		memorytool.NewDescribeTool(memoryEngine),
		memorytool.NewExpandTool(memoryEngine),
	)

	// Collect built-in tool names for plugin collision detection.
	builtinReg := agenttool.NewRegistry("")
	builtinNames := builtinReg.BuiltinNames()
	builtinNames = append(builtinNames, "delegate")
	for _, t := range extraTools {
		builtinNames = append(builtinNames, t.Definition().Name)
	}

	// Load plugins.
	pm := pluginmgr.NewManager(slog.Default(), builtinNames)
	pm.LoadAll(snap.Plugins)

	for _, pt := range pm.Registry().Tools() {
		extraTools = append(extraTools, pluginmgr.AdaptTool(pt))
	}

	idleTimeout := time.Duration(snap.Runner.IdleTimeout) * time.Minute
	factory, err := agent.NewRunnerFactory(snap, extraTools, pm.Registry())
	if err != nil {
		return nil, fmt.Errorf("create runner factory: %w", err)
	}

	opts := []agent.PoolOption{
		agent.WithIdleTimeout(idleTimeout),
		agent.WithCompaction(agent.CompactionConfig{
			MaxTokens: snap.Runner.Compaction.MaxTokens,
			KeepTail:  snap.Runner.Compaction.KeepTail,
		}.WithDefaults()),
		agent.WithDefaultModel(snap.ResolveModelID(config.ModelTierStrong)),
		agent.WithFastModel(snap.ResolveModelID(config.ModelTierFast)),
		agent.WithPluginHooks(pm.Registry()),
	}

	pool := agent.NewPool(factory, memoryEngine, opts...)
	go pool.StartReaper(ctx)

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
	if schedulerSvc != nil {
		schedulerSvc.SetOnJob(func(ctx context.Context, job scheduler.Job) {
			sessionID := job.SessionID()
			msg := fmt.Sprintf("[Scheduled Task] %s\n\nInstruction: %s", job.Name, job.Message)
			ch := pool.Chat(ctx, sessionID, msg)
			for evt := range ch {
				if evt.Err != nil {
					slog.Error("scheduler job error", "job_id", job.ID, "error", evt.Err)
				}
			}
		})
	}

	return &setupResult{
		ctx:          ctx,
		snap:         snap,
		store:        store,
		pool:         pool,
		schedulerSvc: schedulerSvc,
		extraTools:   extraTools,
		notifier:     dispatcher,
		pluginMgr:    pm,
	}, nil
}

// modelSwitcher returns a function that switches the pool's runner factory
// to use a different provider/model combination.
func modelSwitcher(snap *config.Snapshot, store config.Store, pool *agent.Pool, extraTools []agenttool.Tool, pluginHooks *pluginmgr.Registry) channel.ModelSwitchFunc {
	return func(provider, model string) error {
		snap.Provider = provider
		snap.Model = model

		// Look up provider credentials from the store.
		p, err := store.GetProvider(context.Background(), provider)
		if err == nil {
			snap.APIKey = p.APIKey
			snap.BaseURL = p.BaseURL
		}

		factory, err := agent.NewRunnerFactory(snap, extraTools, pluginHooks)
		if err != nil {
			return err
		}
		pool.SetFactory(factory)
		pool.SetDefaultModel(model)
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
