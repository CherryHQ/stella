package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/agent"
	"github.com/vaayne/anna/agent/runner"
	"github.com/vaayne/anna/agent/tool"
	"github.com/vaayne/anna/channel"
	"github.com/vaayne/anna/config"
	"github.com/vaayne/anna/cron"
	"github.com/vaayne/anna/heartbeat"
	"github.com/vaayne/anna/lcm"
	lcmtool "github.com/vaayne/anna/lcm/tool"
	"github.com/vaayne/anna/skills"
	"github.com/vaayne/anna/store"
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
			onboardCommand(),
			versionCommand(),
			upgradeCommand(),
		},
	}
}

type setupResult struct {
	ctx        context.Context
	cfg        *config.Config
	pool       *agent.Pool
	cronSvc    *cron.Service
	heartbeat  *heartbeat.Service
	memoryDir  string
	extraTools []tool.Tool
	notifier   *channel.Dispatcher
}

func setup(parent context.Context, gateway bool) (*setupResult, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	ctx, cancel := context.WithCancel(parent)
	_ = cancel // cancel is deferred via the caller's lifecycle

	// Create cron service and tool before the runner factory so the tool
	// can be injected into the Go runner.
	var cronSvc *cron.Service
	var extraTools []tool.Tool
	if cfg.Cron.CronEnabled() || (gateway && cfg.Heartbeat.IsEnabled()) {
		cronSvc, err = cron.New(cfg.Cron.DataDir)
		if err != nil {
			return nil, fmt.Errorf("create cron service: %w", err)
		}
		if cfg.Cron.CronEnabled() {
			extraTools = append(extraTools, cron.NewTool(cronSvc))
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

	// LCM: create engine for message persistence and compaction.
	// TODO: wire LLMSummarizer with runner factory for production-quality summaries.
	lcmDBPath := filepath.Join(cfg.Workspace, "lcm.db")
	lcmEngine, err := lcm.NewEngine(lcmDBPath, &lcm.StaticSummarizer{Response: "compacted"}, lcm.WithLogger(slog.Default()))
	if err != nil {
		return nil, fmt.Errorf("create lcm engine: %w", err)
	}

	// LCM retrieval tools — must be appended before runner factory captures extraTools.
	extraTools = append(extraTools,
		lcmtool.NewGrepTool(lcmEngine),
		lcmtool.NewDescribeTool(lcmEngine),
		lcmtool.NewExpandTool(lcmEngine),
	)

	memoryDir := cfg.Workspace

	idleTimeout := time.Duration(cfg.Runner.IdleTimeout) * time.Minute
	factory, err := newRunnerFactory(cfg, memoryDir, extraTools)
	if err != nil {
		return nil, fmt.Errorf("create runner factory: %w", err)
	}

	opts := []agent.PoolOption{
		agent.WithIdleTimeout(idleTimeout),
		agent.WithCompaction(cfg.Runner.Compaction.WithDefaults()),
		agent.WithDefaultModel(cfg.ResolveModelID(config.ModelTierStrong)),
		agent.WithFastModel(cfg.ResolveModelID(config.ModelTierFast)),
	}
	opts = append(opts, agent.WithLCM(lcmEngine))

	sessionsPath := cfg.SessionsPath()
	if sessionsPath != "" {
		s, err := store.NewFileStore(sessionsPath, cwd)
		if err != nil {
			return nil, fmt.Errorf("create session store: %w", err)
		}
		opts = append(opts, agent.WithStore(s))
		slog.Info("session persistence enabled", "dir", sessionsPath)
	}

	pool := agent.NewPool(factory, opts...)
	go pool.StartReaper(ctx)

	var heartbeatSvc *heartbeat.Service
	if cfg.Heartbeat.IsEnabled() {
		heartbeatSvc = heartbeat.New(heartbeat.Config{
			File:      cfg.Heartbeat.FilePath(cfg.Workspace),
			FastModel: cfg.ResolveModelID(config.ModelTierFast),
		}, func(ctx context.Context, sessionID, message, model string) <-chan runner.Event {
			if model != "" {
				return pool.Chat(ctx, sessionID, message, agent.WithModel(model))
			}
			return pool.Chat(ctx, sessionID, message)
		}, dispatcher)
	}

	// Wire the cron callback now that pool exists.
	if cronSvc != nil {
		cronSvc.SetOnJob(func(ctx context.Context, job cron.Job) {
			sessionID := job.SessionID()
			msg := fmt.Sprintf("[Scheduled Task] %s\n\nInstruction: %s", job.Name, job.Message)
			ch := pool.Chat(ctx, sessionID, msg)
			for evt := range ch {
				if evt.Err != nil {
					slog.Error("cron job error", "job_id", job.ID, "error", evt.Err)
				}
			}
		})
	}

	return &setupResult{
		ctx:        ctx,
		cfg:        cfg,
		pool:       pool,
		cronSvc:    cronSvc,
		heartbeat:  heartbeatSvc,
		memoryDir:  memoryDir,
		extraTools: extraTools,
		notifier:   dispatcher,
	}, nil
}

func hasEnabledNotifyChannel(cfg *config.Config) bool {
	return (cfg.Channels.Telegram.IsEnabled() && cfg.Channels.Telegram.IsNotifyEnabled()) ||
		(cfg.Channels.QQ.IsEnabled() && cfg.Channels.QQ.IsNotifyEnabled()) ||
		(cfg.Channels.Feishu.IsEnabled() && cfg.Channels.Feishu.IsNotifyEnabled())
}

func newRunnerFactory(cfg *config.Config, memoryDir string, extraTools []tool.Tool) (runner.NewRunnerFunc, error) {
	switch cfg.Runner.Type {
	case "go":
		providerCfg := cfg.Providers[cfg.Provider]
		return func(ctx context.Context, model string) (runner.Runner, error) {
			if model == "" {
				model = cfg.Model
			}
			return runner.NewGoRunner(ctx, runner.GoRunnerConfig{
				API:        cfg.Provider,
				Model:      model,
				APIKey:     providerCfg.APIKey,
				Workspace:  cfg.Workspace,
				AnnaHome:   config.AnnaHome(),
				MemoryDir:  memoryDir,
				BaseURL:    providerCfg.BaseURL,
				ExtraTools: extraTools,
			})
		}, nil
	default:
		return nil, fmt.Errorf("unknown runner type: %q", cfg.Runner.Type)
	}
}

// modelSwitcher returns a function that switches the pool's runner factory
// to use a different provider/model combination.
func modelSwitcher(cfg *config.Config, pool *agent.Pool, memoryDir string, extraTools []tool.Tool) channel.ModelSwitchFunc {
	return func(provider, model string) error {
		cfg.Provider = provider
		cfg.Model = model
		factory, err := newRunnerFactory(cfg, memoryDir, extraTools)
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
