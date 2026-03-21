package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/agent/runner"
	agenttool "github.com/vaayne/anna/internal/agent/tool"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/feishutool"
	"github.com/vaayne/anna/internal/memory"
	memorytool "github.com/vaayne/anna/internal/memory/tool"
	pluginmgr "github.com/vaayne/anna/internal/plugin"
	"github.com/vaayne/anna/internal/scheduler"
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
	mem          memory.Engine
	snap         *config.Snapshot
	store        config.Store
	poolManager  *agent.PoolManager
	pool         *agent.Pool // default agent's pool (backward compat)
	schedulerSvc *scheduler.Service
	extraTools   []agenttool.Tool
	notifier     *channel.Dispatcher
	pluginMgr    *pluginmgr.Manager
	fsClient     *feishutool.Client // feishu client for OAuth (nil if not configured)
	cliUserID    int64              // resolved CLI user for session creation
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

	// Create scheduler service and tool before the runner factory so the tool
	// can be injected into the Go runner.
	var schedulerSvc *scheduler.Service
	var sharedTools []agenttool.Tool
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

	// Memory engine: create engine for message persistence and compaction.
	memoryEngine := memory.NewEngineFromDB(db, &memory.StaticSummarizer{Response: "compacted"}, memory.WithLogger(slog.Default()))

	// Unified memory tool (shared across all agents).
	userMemoryStore := memory.NewUserMemoryStore(store)
	sharedTools = append(sharedTools,
		memorytool.NewMemoryTool(memoryEngine, userMemoryStore),
	)

	// Feishu tools: load config early (like scheduler/memory), create client
	// and tools if configured, so all agents have access to Feishu APIs.
	var fsClient *feishutool.Client
	if fsCfg := loadChannelConfig[feishuChannelConfig](store, "feishu"); fsCfg != nil && fsCfg.AppID != "" && fsCfg.AppSecret != "" {
		// Create token store for UAT token management.
		tokenStore, tsErr := feishutool.NewSQLiteTokenStore(db, fsCfg.AppSecret)
		if tsErr != nil {
			slog.Warn("feishu token store creation failed, UAT disabled", "error", tsErr)
		}

		var clientOpts []feishutool.ClientOption
		if tokenStore != nil {
			clientOpts = append(clientOpts, feishutool.WithTokenStore(tokenStore))
		}

		larkClient := lark.NewClient(fsCfg.AppID, fsCfg.AppSecret,
			lark.WithLogLevel(larkcore.LogLevelWarn),
			lark.WithEnableTokenCache(true),
		)
		fsClient = feishutool.NewClient(larkClient, clientOpts...)
		fsClient.SetAppCredentials(fsCfg.AppID, fsCfg.AppSecret)
		sharedTools = append(sharedTools,
			feishutool.NewUserTool(fsClient),
		)
		slog.Info("feishu tools loaded", "uat_enabled", tokenStore != nil)
	}

	// Collect built-in tool names for plugin collision detection.
	builtinReg := agenttool.NewRegistry("")
	builtinNames := builtinReg.BuiltinNames()
	builtinNames = append(builtinNames, "delegate", "skills")
	for _, t := range sharedTools {
		builtinNames = append(builtinNames, t.Definition().Name)
	}

	// Load plugins.
	pm := pluginmgr.NewManager(slog.Default(), builtinNames)
	pm.LoadAll(snap.Plugins)

	// Add plugin tools to the shared set.
	for _, pt := range pm.Registry().Tools() {
		sharedTools = append(sharedTools, pluginmgr.AdaptTool(pt))
	}

	idleTimeout := time.Duration(snap.Runner.IdleTimeout) * time.Minute

	// Create PoolManager with shared tools and options.
	poolMgr := agent.NewPoolManager(store, memoryEngine,
		agent.WithIdleTimeoutPM(idleTimeout),
		agent.WithCompactionPM(agent.CompactionConfig{
			MaxTokens: snap.Runner.Compaction.MaxTokens,
			KeepTail:  snap.Runner.Compaction.KeepTail,
		}.WithDefaults()),
		agent.WithSharedExtraTools(sharedTools),
		agent.WithPluginHooksPM(pm.Registry()),
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
		mem:          memoryEngine,
		snap:         snap,
		store:        store,
		poolManager:  poolMgr,
		pool:         pool,
		schedulerSvc: schedulerSvc,
		extraTools:   sharedTools,
		notifier:     dispatcher,
		pluginMgr:    pm,
		fsClient:     fsClient,
		cliUserID:    cliUserID,
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
