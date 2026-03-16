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
	cfg          *config.Config
	pool         *agent.Pool
	schedulerSvc *scheduler.Service
	extraTools   []agenttool.Tool
	notifier     *channel.Dispatcher
	pluginMgr    *pluginmgr.Manager
}

func setup(parent context.Context, gateway bool) (*setupResult, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	ctx, cancel := context.WithCancel(parent)
	_ = cancel // cancel is deferred via the caller's lifecycle

	// Create scheduler service and tool before the runner factory so the tool
	// can be injected into the Go runner.
	var schedulerSvc *scheduler.Service
	var extraTools []agenttool.Tool
	memoryDBPath := filepath.Join(cfg.Workspace, "memory.db")
	if cfg.Scheduler.IsEnabled() || (gateway && cfg.Heartbeat.IsEnabled()) {
		schedulerSvc, err = scheduler.NewFromPath(memoryDBPath)
		if err != nil {
			return nil, fmt.Errorf("create scheduler service: %w", err)
		}
		schedulerSvc.SetLegacyDataPath(cfg.Scheduler.DataDir)
		if cfg.Scheduler.IsEnabled() {
			extraTools = append(extraTools, scheduler.NewTool(schedulerSvc))
		}
	}

	// Skills tool — always available.
	cwd, _ := os.Getwd()
	extraTools = append(extraTools, skills.NewTool(config.AnnaHome(), cfg.Workspace, cwd))

	// Notification dispatcher + tool — backends are registered later in
	// runGateway(). Only expose the tool in gateway mode where at least one
	// enabled channel has enable_notify set to true.
	dispatcher := channel.NewDispatcher()
	if gateway && hasEnabledNotifyChannel(cfg) {
		extraTools = append(extraTools, channel.NewNotifyTool(dispatcher))
	}

	// Memory engine: create engine for message persistence and compaction.
	// TODO: wire LLMSummarizer with runner factory for production-quality summaries.
	memoryEngine, err := memory.NewEngine(memoryDBPath, &memory.StaticSummarizer{Response: "compacted"}, memory.WithLogger(slog.Default()))
	if err != nil {
		return nil, fmt.Errorf("create memory engine: %w", err)
	}

	// Memory retrieval tools — must be appended before runner factory captures extraTools.
	extraTools = append(extraTools,
		memorytool.NewGrepTool(memoryEngine),
		memorytool.NewDescribeTool(memoryEngine),
		memorytool.NewExpandTool(memoryEngine),
	)

	// Collect built-in tool names from the default registry + extra tools
	// so the plugin registry can reject collisions.
	builtinReg := agenttool.NewRegistry("")
	builtinNames := builtinReg.BuiltinNames()
	builtinNames = append(builtinNames, "delegate") // registered separately in GoRunner
	for _, t := range extraTools {
		builtinNames = append(builtinNames, t.Definition().Name)
	}

	// Load plugins (best-effort; failures are logged as warnings).
	pm := pluginmgr.NewManager(slog.Default(), builtinNames)
	pm.LoadAll(cfg.Plugins)

	// Adapt plugin tools into the extra tools list.
	for _, pt := range pm.Registry().Tools() {
		extraTools = append(extraTools, pluginmgr.AdaptTool(pt))
	}

	idleTimeout := time.Duration(cfg.Runner.IdleTimeout) * time.Minute
	factory, err := newRunnerFactory(cfg, extraTools, pm.Registry())
	if err != nil {
		return nil, fmt.Errorf("create runner factory: %w", err)
	}

	opts := []agent.PoolOption{
		agent.WithIdleTimeout(idleTimeout),
		agent.WithCompaction(agent.CompactionConfig{
			MaxTokens: cfg.Runner.Compaction.MaxTokens,
			KeepTail:  cfg.Runner.Compaction.KeepTail,
		}.WithDefaults()),
		agent.WithDefaultModel(cfg.ResolveModelID(config.ModelTierStrong)),
		agent.WithFastModel(cfg.ResolveModelID(config.ModelTierFast)),
		agent.WithPluginHooks(pm.Registry()),
	}

	pool := agent.NewPool(factory, memoryEngine, opts...)
	go pool.StartReaper(ctx)

	// Wire heartbeat on the scheduler service if enabled.
	if schedulerSvc != nil && cfg.Heartbeat.IsEnabled() {
		schedulerSvc.SetHeartbeat(scheduler.HeartbeatConfig{
			File:      cfg.Heartbeat.FilePath(cfg.Workspace),
			FastModel: cfg.ResolveModelID(config.ModelTierFast),
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
		cfg:          cfg,
		pool:         pool,
		schedulerSvc: schedulerSvc,
		extraTools:   extraTools,
		notifier:     dispatcher,
		pluginMgr:    pm,
	}, nil
}

func hasEnabledNotifyChannel(cfg *config.Config) bool {
	return (cfg.Channels.Telegram.IsEnabled() && cfg.Channels.Telegram.IsNotifyEnabled()) ||
		(cfg.Channels.QQ.IsEnabled() && cfg.Channels.QQ.IsNotifyEnabled()) ||
		(cfg.Channels.Feishu.IsEnabled() && cfg.Channels.Feishu.IsNotifyEnabled())
}

func newRunnerFactory(cfg *config.Config, extraTools []agenttool.Tool, pluginHooks *pluginmgr.Registry) (runner.NewRunnerFunc, error) {
	switch cfg.Runner.Type {
	case "go":
		providerCfg := cfg.Providers[cfg.Provider]
		return func(ctx context.Context, model string) (runner.Runner, error) {
			if model == "" {
				model = cfg.Model
			}
			return runner.NewGoRunner(ctx, runner.GoRunnerConfig{
				API:         cfg.Provider,
				Model:       model,
				APIKey:      providerCfg.APIKey,
				Workspace:   cfg.Workspace,
				AnnaHome:    config.AnnaHome(),
				BaseURL:     providerCfg.BaseURL,
				ExtraTools:  extraTools,
				PluginHooks: pluginHooks,
			})
		}, nil
	default:
		return nil, fmt.Errorf("unknown runner type: %q", cfg.Runner.Type)
	}
}

// modelSwitcher returns a function that switches the pool's runner factory
// to use a different provider/model combination.
func modelSwitcher(cfg *config.Config, pool *agent.Pool, extraTools []agenttool.Tool, pluginHooks *pluginmgr.Registry) channel.ModelSwitchFunc {
	return func(provider, model string) error {
		cfg.Provider = provider
		cfg.Model = model
		factory, err := newRunnerFactory(cfg, extraTools, pluginHooks)
		if err != nil {
			return err
		}
		pool.SetFactory(factory)
		pool.SetDefaultModel(model)
		if err := config.SaveModelSelection(cfg.Workspace, provider, model); err != nil {
			slog.Warn("failed to persist model selection", "error", err)
		}
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
