package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/cli"
	"github.com/CherryHQ/stella/internal/config"
	oauth "github.com/CherryHQ/stella/internal/credentials/oauth"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/notify"
	"github.com/CherryHQ/stella/internal/orgruntime"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/scheduler"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/internal/tasks"
	"github.com/CherryHQ/stella/internal/tools"
	mcpplugin "github.com/CherryHQ/stella/internal/tools/mcp"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
	"github.com/CherryHQ/stella/pkg/hooks"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
	pluginhooks "github.com/CherryHQ/stella/plugins/hooks"
	"github.com/CherryHQ/stella/resources"
	"github.com/CherryHQ/stella/resources/binaries"
)

// TODO: STELLA_SESSION_ID is not a robust sandbox indicator — add a dedicated STELLA_SANDBOX env var.
func inSandbox() bool {
	return os.Getenv("STELLA_SESSION_ID") != ""
}

func denyInSandbox(cmd *ucli.Command) *ucli.Command {
	prev := cmd.Before
	cmd.Before = func(c *ucli.Context) error {
		if inSandbox() {
			return fmt.Errorf("%q is a system command and cannot be run inside an agent sandbox", cmd.Name)
		}
		if prev != nil {
			return prev(c)
		}
		return nil
	}
	return cmd
}

func newApp() *ucli.App {
	return &ucli.App{
		Name:  "stella",
		Usage: "A local AI assistant",
		Description: `Stella is a self-hosted AI agent that connects to your favourite LLMs,
messaging channels, and local tools. Run "stella server" to start the
server, then use the subcommands below to manage models, content,
schedules, and more.`,
		Version: displayVersion(),
		Commands: []*ucli.Command{
			denyInSandbox(serverCommand()),
			skillsCommand(),
			versionCommand(),
			denyInSandbox(upgradeCommand()),
			recallyCommand(),
			schedulerCommand(),
			emailCommand(),
			vaultCommand(),
			oauthCommand(),
			shareCommand(),
			taskCommand(),
			denyInSandbox(serviceCommand()),
			authCommand(),
		},
	}
}

type setupResult struct {
	ctx                      context.Context
	db                       *sql.DB
	mem                      memory.Provider
	store                    config.Store
	pluginHost               *pluginhost.Host
	channelRuntimeServices   *pluginhost.ChannelPlatform
	poolManager              *agent.PoolManager
	schedulerSvc             *scheduler.Service
	tasksSvc                 *tasks.Service
	builtinTools             []pkgtools.Tool
	notifier                 *notify.Dispatcher
	pluginToolsBuilder       agent.PluginToolsBuilder
	promptToolsBuilder       prompt.ToolsBuilder
	promptSectionsBuilder    prompt.SectionsBuilder
	sessionPluginViewBuilder agent.SessionPluginViewBuilder
	toolLifecycle            *coreagent.ToolLifecycle
	skillStore               pkgplugins.SkillStore
	orgRuntimeManager        *orgruntime.Manager
	cliUserID                int64
	oauthRegistry            *oauth.ProviderRegistry
	backgroundTasks          *sync.WaitGroup
}

func setup(parent context.Context, _ bool) (*setupResult, error) {
	db, err := appdb.OpenDB(config.DBPath())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	store := cfgstore.NewDBStore(db)

	if err := ensureEmbeddedAssets(); err != nil {
		return nil, err
	}

	ss := setupSkillStores(db)

	dispatcher := notify.NewDispatcher()
	dispatcher.SetChannelStore(store)

	ps, err := setupPlugins(parent, db, store, ss.diskSync, dispatcher)
	if err != nil {
		return nil, err
	}
	phost := ps.host

	schedulerSvc, err := setupScheduler(db, phost)
	if err != nil {
		return nil, err
	}

	providerStreamBuilder := func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
		return phost.BuildStreamFunc(api, map[string]any{
			"api_key":  apiKey,
			"base_url": baseURL,
		})
	}

	memProvider, err := setupMemoryProvider(parent, db, store, providerStreamBuilder)
	if err != nil {
		return nil, fmt.Errorf("memory provider: %w", err)
	}

	var poolMgr *agent.PoolManager
	memProvider = wrapMemoryWithTracing(memProvider, &poolMgr)

	builtinTools := []pkgtools.Tool{
		memory.BuildTool(memProvider),
		mcpplugin.New(ps.mcpManager),
	}
	if notifyTool := tools.NewNotifyTool(dispatcher); notifyTool != nil {
		builtinTools = append(builtinTools, notifyTool)
	}

	pluginToolsBuilder := func(ctx context.Context, build pkgplugins.ToolBuildContext) []pkgtools.Tool {
		return phost.BuildEnabledTools(ctx, build)
	}
	ps.reflectRuntimeServices.Set(parent, memProvider, store, config.StellaHome(), providerStreamBuilder)

	pluginHooksBuilder := func(ctx context.Context) []hooks.HookPlugin {
		return phost.BuildEnabledHooks(ctx, pluginhooks.BuildContext{ToolsBinDir: binaries.BinDir(config.StellaHome())})
	}

	toolLifecycle := buildToolLifecycle(phost)
	promptToolsBuilder := buildPromptToolsBuilder(ps.mcpManager)
	promptSectionsBuilder := func(ctx context.Context, build pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error) {
		return phost.SystemPromptSections(ctx, build)
	}
	sessionPluginViewBuilder := func(ctx context.Context) (pkgplugins.SessionPluginView, error) {
		return phost.SessionPluginView(ctx)
	}

	// v2 task system (Phase 6 boot wiring). The runner is a noop until the
	// agent.Pool adapter is wired; this still validates the boot path,
	// scheduler registration, dispatcher tick, and DB plumbing. The pool
	// wiring is omitted from BootConfig deliberately — adding it before the
	// adapter exists would silently drop the caller-supplied closure.
	tasksSvc := tasks.New(tasks.BootConfig{
		DB: db,
	})

	skillStoreAdapter := pluginhost.NewSkillStoreAdapter(ss.diskSync)
	poolMgr = agent.NewPoolManager(store, memProvider,
		agent.WithCompactionPM(agent.CompactionConfig{}.WithDefaults()),
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
		agent.WithSkillStore(skillStoreAdapter),
		agent.WithProjectResolver(func(ctx context.Context, projectID, userID string) (string, error) {
			p, err := sqlc.New(db).GetProject(ctx, sqlc.GetProjectParams{ID: projectID, UserID: userID})
			if err != nil {
				return "", err
			}
			return p.BaseDir, nil
		}),
		agent.WithProjectEnsurerPM(buildProjectEnsurer(db, store)),
	)

	if err := poolMgr.StartAll(parent); err != nil {
		return nil, fmt.Errorf("start pool manager: %w", err)
	}

	orgRtMgr := orgruntime.NewManager(orgruntime.ManagerDeps{
		Store:    store,
		Syncer:   poolMgr,
		Channels: nil, // Channels wired later in runServer
		Jobs:     schedulerSvc,
	})

	backgroundTasks := &sync.WaitGroup{}
	if ps.manifestToReconcile != nil {
		reconcileManifestPluginsInBackground(parent, backgroundTasks, ps.manifestToReconcile, config.StellaHome())
	}

	return &setupResult{
		ctx:                      parent,
		db:                       db,
		mem:                      memProvider,
		store:                    store,
		pluginHost:               phost,
		channelRuntimeServices:   ps.channelRuntimeServices,
		poolManager:              poolMgr,
		schedulerSvc:             schedulerSvc,
		tasksSvc:                 tasksSvc,
		builtinTools:             builtinTools,
		notifier:                 dispatcher,
		pluginToolsBuilder:       pluginToolsBuilder,
		promptToolsBuilder:       promptToolsBuilder,
		promptSectionsBuilder:    promptSectionsBuilder,
		sessionPluginViewBuilder: sessionPluginViewBuilder,
		toolLifecycle:            toolLifecycle,
		skillStore:               skillStoreAdapter,
		orgRuntimeManager:        orgRtMgr,
		cliUserID:                0,
		oauthRegistry:            ps.oauthRegistry,
		backgroundTasks:          backgroundTasks,
	}, nil
}

func ensureEmbeddedAssets() error {
	if err := binaries.EnsureTools(config.StellaHome()); err != nil {
		return fmt.Errorf("extract embedded tools: %w", err)
	}
	if err := binaries.VerifyTools(config.StellaHome()); err != nil {
		return err
	}
	if err := cli.EnsureStellaCLIInPath(config.StellaHome()); err != nil {
		return fmt.Errorf("copy stella cli into sandbox path: %w", err)
	}
	if err := resources.EnsureBuiltinSkills(filepath.Join(config.StellaHome(), ".agents", "skills")); err != nil {
		return fmt.Errorf("extract builtin skills: %w", err)
	}
	return nil
}

func setupScheduler(db *sql.DB, phost *pluginhost.Host) (*scheduler.Service, error) {
	svc, err := scheduler.New(db)
	if err != nil {
		return nil, fmt.Errorf("create scheduler service: %w", err)
	}
	phost.SetSchedulerService(newSchedulerServiceAdapter(svc, phost.Runtime()))
	return svc, nil
}

func wireSchedulerCallbacks(svc *scheduler.Service, poolMgr *agent.PoolManager, dispatcher *notify.Dispatcher) {
	if svc == nil {
		return
	}
	svc.SetOnJob(func(ctx context.Context, job scheduler.Job) error {
		if job.OwnerKind == scheduler.JobOwnerPlugin {
			return nil
		}
		pool := poolMgr.Get(job.AgentID)
		if pool == nil {
			pool = poolMgr.DefaultPool()
		}
		if pool == nil {
			slog.Warn("scheduler: no pool available for job", "job_id", job.ID, "agent_id", job.AgentID)
			return fmt.Errorf("no agent pool available for job %s", job.ID)
		}
		sessionID := job.SessionID()
		ch := pool.Chat(schedulerJobContext(ctx, pool, job), sessionID, schedulerJobMessage(job))
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

func (s *setupResult) waitBackgroundTasks() {
	if s != nil && s.backgroundTasks != nil {
		s.backgroundTasks.Wait()
	}
}
