package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/vaayne/anna/internal/agent"
	agenttool "github.com/vaayne/anna/internal/agent/tool"
	"github.com/vaayne/anna/internal/auth"
	channelplugin "github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/channel/feishu"
	"github.com/vaayne/anna/internal/channel/qq"
	"github.com/vaayne/anna/internal/channel/telegram"
	"github.com/vaayne/anna/internal/channel/weixin"
	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/memory"
	memorytool "github.com/vaayne/anna/internal/memory/tool"
	pluginmgr "github.com/vaayne/anna/internal/plugin"
	"github.com/vaayne/anna/internal/pluginapi"
	"github.com/vaayne/anna/internal/pluginhost"
)

type channelPluginRuntime struct {
	bot channelplugin.Channel
}

func (r channelPluginRuntime) Start(ctx context.Context) error {
	return r.bot.Start(ctx)
}

func (r channelPluginRuntime) Notify(ctx context.Context, n pluginapi.ChannelNotification) error {
	return r.bot.Notify(ctx, channelplugin.Notification(n))
}

type channelRuntimeDeps struct {
	db          *sql.DB
	store       config.Store
	authStore   auth.AuthStore
	engine      *auth.PolicyEngine
	linkCodes   *auth.LinkCodeStore
	poolManager *agent.PoolManager
	pool        *agent.Pool
	snap        *config.Snapshot
	listFn      channelplugin.ModelListFunc
	switchFn    channelplugin.ModelSwitchFunc
}

func buildChannelPlugin(name, workDir, userDataDir string) (pluginhost.Definition, channelPluginRuntime, error) {
	deps, err := newChannelRuntime(context.Background(), workDir, userDataDir)
	if err != nil {
		return pluginhost.Definition{}, channelPluginRuntime{}, err
	}

	def, err := channelplugin.BuiltinChannelDefinition(name, workDir, userDataDir)
	if err != nil {
		return pluginhost.Definition{}, channelPluginRuntime{}, err
	}

	switch name {
	case "telegram":
		cfg := channelplugin.LoadConfig[channelplugin.TelegramConfig](deps.store, name)
		if cfg == nil || cfg.Token == "" {
			return pluginhost.Definition{}, channelPluginRuntime{}, fmt.Errorf("telegram: missing channel config")
		}
		bot, err := telegram.New(telegram.Config{
			Token:     cfg.Token,
			ChannelID: cfg.ChannelID,
			GroupMode: cfg.GroupMode,
		}, deps.poolManager, deps.store, deps.listFn, deps.switchFn,
			telegram.WithAuth(deps.authStore, deps.engine, deps.linkCodes),
		)
		if err != nil {
			return pluginhost.Definition{}, channelPluginRuntime{}, err
		}
		return def, channelPluginRuntime{bot: bot}, nil

	case "qq":
		cfg := channelplugin.LoadConfig[channelplugin.QQConfig](deps.store, name)
		if cfg == nil || cfg.AppID == "" || cfg.AppSecret == "" {
			return pluginhost.Definition{}, channelPluginRuntime{}, fmt.Errorf("qq: missing channel config")
		}
		bot, err := qq.New(qq.Config{
			AppID:     cfg.AppID,
			AppSecret: cfg.AppSecret,
			GroupMode: cfg.GroupMode,
		}, deps.poolManager, deps.store, deps.listFn, deps.switchFn,
			qq.WithAuth(deps.authStore, deps.engine, deps.linkCodes),
		)
		if err != nil {
			return pluginhost.Definition{}, channelPluginRuntime{}, err
		}
		return def, channelPluginRuntime{bot: bot}, nil

	case "feishu":
		cfg := channelplugin.LoadConfig[channelplugin.FeishuConfig](deps.store, name)
		if cfg == nil || cfg.AppID == "" || cfg.AppSecret == "" {
			return pluginhost.Definition{}, channelPluginRuntime{}, fmt.Errorf("feishu: missing channel config")
		}
		groups := make(map[string]feishu.GroupConfig, len(cfg.Groups))
		for k, v := range cfg.Groups {
			groups[k] = feishu.GroupConfig{
				GroupMode:    v.GroupMode,
				SystemPrompt: v.SystemPrompt,
				ToolAllow:    v.ToolAllow,
				ToolDeny:     v.ToolDeny,
			}
		}
		bot, err := feishu.New(feishu.Config{
			AppID:             cfg.AppID,
			AppSecret:         cfg.AppSecret,
			EncryptKey:        cfg.EncryptKey,
			VerificationToken: cfg.VerificationToken,
			GroupMode:         cfg.GroupMode,
			Groups:            groups,
		}, deps.poolManager, deps.store, deps.listFn, deps.switchFn,
			feishu.WithAuth(deps.authStore, deps.engine, deps.linkCodes),
		)
		if err != nil {
			return pluginhost.Definition{}, channelPluginRuntime{}, err
		}
		return def, channelPluginRuntime{bot: bot}, nil

	case "weixin":
		cfg := channelplugin.LoadConfig[channelplugin.WeixinConfig](deps.store, name)
		if cfg == nil || cfg.BotToken == "" {
			return pluginhost.Definition{}, channelPluginRuntime{}, fmt.Errorf("weixin: missing channel config")
		}
		bot, err := weixin.New(weixin.Config{
			BotToken: cfg.BotToken,
			BaseURL:  cfg.BaseURL,
			BotID:    cfg.BotID,
			UserID:   cfg.UserID,
		}, deps.poolManager, deps.store, deps.listFn, deps.switchFn,
			weixin.WithAuth(deps.authStore, deps.engine, deps.linkCodes),
		)
		if err != nil {
			return pluginhost.Definition{}, channelPluginRuntime{}, err
		}
		return def, channelPluginRuntime{bot: bot}, nil
	}

	return pluginhost.Definition{}, channelPluginRuntime{}, fmt.Errorf("unknown channel plugin: %s", name)
}

func newChannelRuntime(ctx context.Context, workDir, userDataDir string) (*channelRuntimeDeps, error) {
	dbPath := config.DBPath()
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	store := config.NewDBStore(db)
	if err := store.SeedDefaults(ctx); err != nil {
		return nil, fmt.Errorf("seed defaults: %w", err)
	}

	authStore := appdb.NewAuthStore(db)
	if err := auth.SeedPolicies(ctx, authStore); err != nil {
		return nil, fmt.Errorf("seed auth: %w", err)
	}
	engine, err := auth.NewEngine(ctx, authStore)
	if err != nil {
		return nil, fmt.Errorf("create auth engine: %w", err)
	}

	agents, err := store.ListEnabledAgents(ctx)
	if err != nil || len(agents) == 0 {
		return nil, fmt.Errorf("no enabled agents found")
	}
	defaultAgentID := agents[0].ID

	snap, err := store.Snapshot(ctx, defaultAgentID)
	if err != nil {
		return nil, fmt.Errorf("load config snapshot: %w", err)
	}

	memoryEngine := memory.NewEngineFromDB(db, &memory.StaticSummarizer{Response: "compacted"}, memory.WithLogger(slog.Default()))
	userMemoryStore := memory.NewUserMemoryStore(store)
	sharedTools := []agenttool.Tool{
		memorytool.NewMemoryTool(memoryEngine, userMemoryStore),
	}

	builtinNames := agenttool.BuiltinToolNames()
	builtinNames = append(builtinNames, "delegate", "skills")
	for _, t := range sharedTools {
		builtinNames = append(builtinNames, t.Definition().Name)
	}

	pm := pluginmgr.NewManager(slog.Default(), builtinNames)
	pm.LoadAll(snap.Plugins)
	for _, pt := range pm.Registry().Tools() {
		sharedTools = append(sharedTools, pluginmgr.AdaptTool(pt))
	}

	idleTimeout := time.Duration(snap.Runner.IdleTimeout) * time.Minute
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

	pool := poolMgr.Get(defaultAgentID)
	if pool == nil {
		pool = poolMgr.DefaultPool()
	}

	listFn := func() []channelplugin.ModelOption {
		return collectChannelModels(ctx, store, snap)
	}
	switchFn := func(provider, model string) error {
		snap.Provider = provider
		snap.Model = model

		if p, err := store.GetProvider(context.Background(), provider); err == nil {
			snap.APIKey = p.APIKey
			snap.BaseURL = p.BaseURL
		}

		factory, err := agent.NewRunnerFactory(snap, sharedTools, pm.Registry())
		if err != nil {
			return err
		}
		pool.SetFactory(factory)
		pool.SetDefaultModel(model)
		return nil
	}

	linkCodes, err := auth.NewSharedLinkCodeStore(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("create link code store: %w", err)
	}

	return &channelRuntimeDeps{
		db:          db,
		store:       store,
		authStore:   authStore,
		engine:      engine,
		linkCodes:   linkCodes,
		poolManager: poolMgr,
		pool:        pool,
		snap:        snap,
		listFn:      listFn,
		switchFn:    switchFn,
	}, nil
}

func collectChannelModels(ctx context.Context, store config.Store, snap *config.Snapshot) []channelplugin.ModelOption {
	seen := make(map[string]bool)
	var models []channelplugin.ModelOption

	add := func(provider, model string) {
		key := provider + "/" + model
		if seen[key] {
			return
		}
		seen[key] = true
		models = append(models, channelplugin.ModelOption{Provider: provider, Model: model})
	}

	add(snap.Provider, snap.Model)

	if cache, err := config.LoadModelsCache(); err == nil {
		for _, m := range cache.Models {
			add(m.Provider, m.Model)
		}
		return models
	}

	providers, err := store.ListProviders(ctx)
	if err == nil {
		for _, prov := range providers {
			add(prov.ID, snap.Model)
		}
	}

	return models
}
