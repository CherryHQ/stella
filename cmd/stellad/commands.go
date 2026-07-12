package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"

	sessionaccess "github.com/CherryHQ/stella/internal/agent/session/access"
	"github.com/CherryHQ/stella/internal/asset"
	"github.com/CherryHQ/stella/internal/blob"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/connections"
	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/embedding"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/internal/mcp"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/notify"
	"github.com/CherryHQ/stella/internal/observability"
	"github.com/CherryHQ/stella/internal/observability/tracehook"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/recally"
	"github.com/CherryHQ/stella/internal/reflect"
	"github.com/CherryHQ/stella/internal/scheduler"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/internal/skills"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/internal/version"
	workflowpkg "github.com/CherryHQ/stella/internal/workflow"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/hooks"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
	pluginhooks "github.com/CherryHQ/stella/plugins/hooks"
	"github.com/CherryHQ/stella/resources"
	"github.com/CherryHQ/stella/resources/binaries"
)

func newApp() *ucli.App {
	return &ucli.App{
		Name:  "stellad",
		Usage: "Stella daemon — server, services, and system management",
		Description: `Stellad is the server component of Stella. Run "stellad server" to start
the server, or use "stellad service" to manage it as a background service.`,
		Version: version.DisplayVersion(),
		Commands: []*ucli.Command{
			serverCommand(),
			versionCommand(),
			upgradeCommand(),
			postgresCommand(),
			vaultCommand(),
			miseCommand(),
			serviceCommand(),
		},
	}
}

type setupResult struct {
	ctx context.Context
	// cfg is the parsed boot-time server config, carried so runServer reads the
	// injected values (base URL, vault key, lifecycle, OIDC) instead of the
	// environment. A secret it holds (Vault.Key) must never be logged.
	cfg                      config.ServerConfig
	db                       *pgxpool.Pool
	embedded                 *appdb.Embedded
	mem                      memory.Provider
	store                    config.Store
	authStore                *appdb.AuthStore
	agentAccess              *agentaccess.Service
	sessionAccess            *sessionaccess.Service
	pluginHost               *pluginhost.Host
	channelRuntimeServices   *pluginhost.ChannelPlatform
	poolManager              *agent.PoolManager
	schedulerSvc             *scheduler.Service
	goalSvc                  *goal.Service
	vaultSvc                 *vault.Service
	mcpSvc                   *mcp.Service
	credSvc                  *connections.Service
	emailSvc                 *email.Service
	shareSvc                 *sharepkg.Service
	recallySvc               *recally.Service
	assetStore               *asset.Store
	workflowSvc              *workflowpkg.Service
	embeddingSvc             *embedding.Service
	riverClient              *river.Client[pgx.Tx]
	builtinTools             []agent.BuiltinTool
	notifier                 *notify.Dispatcher
	pluginToolsBuilder       agent.PluginToolsBuilder
	promptSectionsBuilder    prompt.SectionsBuilder
	sessionPluginViewBuilder agent.SessionPluginViewBuilder
	toolLifecycle            *coreagent.ToolLifecycle
	skillStore               pkgplugins.SkillStore
	cliUserID                int64
	oauthRegistry            *oauth.ProviderRegistry
	backgroundTasks          *sync.WaitGroup
}

// setup builds every subsystem. baseURL is the final public URL resolved once at
// the startup boundary; the shared credentials/share services are constructed
// with it directly, so no service is built with a localhost placeholder and
// mutated later.
func setup(parent context.Context, cfg config.ServerConfig, baseURL string) (*setupResult, error) {
	dsn := cfg.Database.URL
	var embedded *appdb.Embedded
	if dsn == "" {
		if cfg.Database.RequireExternalDB {
			// Set by the Docker image (and k8s manifests): in a container the
			// embedded cluster lands on an ephemeral filesystem, and with multiple
			// replicas each process would create its own database — refuse it
			// rather than silently starting one.
			return nil, errors.New("STELLA_REQUIRE_EXTERNAL_DB=1 but STELLA_DATABASE_URL is not set: embedded PostgreSQL is for single-node local use; point STELLA_DATABASE_URL at an external PostgreSQL with pgvector + pg_search (or set STELLA_REQUIRE_EXTERNAL_DB=0 to deliberately run embedded PostgreSQL on a persistent volume)")
		}
		// Zero-config default: no external DSN, so run a managed PostgreSQL whose
		// cluster lives under the stella home and persists across restarts.
		emb, err := appdb.StartEmbedded(filepath.Join(config.StellaHome(), "postgres"), 0)
		if err != nil {
			return nil, fmt.Errorf("start embedded postgres: %w", err)
		}
		embedded = emb
		dsn = emb.DSN()
	}
	// Stop the embedded server if setup fails partway. Ownership transfers to the
	// returned setupResult on the success path (which clears this local), where
	// shutdown stops it instead.
	defer func() {
		if embedded != nil {
			_ = embedded.Stop()
		}
	}()

	db, err := appdb.OpenDB(dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	store := cfgstore.NewDBStore(db)
	// Construct the one Agent PDP/PEP at the composition root before any agent
	// Service is built. HTTP, channels, and durable workers all share it.
	authStore := appdb.NewAuthStore(db)
	// Every #709 PEP shares this one revision-bound policy authorizer. A use
	// case begins it once and may decide its agent/session/workspace resources
	// against that immutable revision without a second snapshot.
	authorizer := policy.New(db)
	agentAccess := agentaccess.NewService(store, authStore, authorizer)

	if err := ensureEmbeddedAssets(); err != nil {
		return nil, err
	}

	ss := setupSkillStores(db)
	if err := ss.diskSync.SyncAllToDisk(parent); err != nil {
		return nil, fmt.Errorf("sync DB skills to disk: %w", err)
	}

	dispatcher := notify.NewDispatcher()
	dispatcher.SetChannelStore(store)

	ps, err := setupPlugins(parent, db, store, ss.diskSync, dispatcher)
	if err != nil {
		return nil, err
	}
	phost := ps.host

	schedulerSvc, err := setupScheduler(db, phost, authorizer, agentAccess)
	if err != nil {
		return nil, err
	}
	for _, tmpl := range []scheduler.JobTemplate{scheduler.RecallyDigestTemplate, scheduler.RecallyRSSTemplate} {
		if err := schedulerSvc.RegisterTemplate(tmpl); err != nil {
			return nil, fmt.Errorf("register template %q: %w", tmpl.Key, err)
		}
	}

	providerStreamBuilder := func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
		return phost.BuildStreamFunc(api, map[string]any{
			"api_key":  apiKey,
			"base_url": baseURL,
		})
	}

	// Semantic-search lane (config-driven via the web settings page). Always built:
	// it reads its config from the DB at runtime and idles when disabled. Built
	// before the memory provider so its query embedder can be injected, and before
	// the shared River client so its backfill worker joins the single electable client.
	embeddingSvc := setupEmbedding(db, store, slog.With("component", "embedding"))

	memProvider, err := setupMemoryProvider(parent, db, store, providerStreamBuilder, embeddingSvc)
	if err != nil {
		return nil, fmt.Errorf("memory provider: %w", err)
	}

	var poolMgr *agent.PoolManager
	memProvider = wrapMemoryWithTracing(memProvider, &poolMgr)

	builtinTools := []agent.BuiltinTool{
		{Tool: memory.BuildTool(memProvider, memory.WithSessionReadOnlyWrites())},
	}
	if notifyTool := notify.NewTool(dispatcher); notifyTool != nil {
		builtinTools = append(builtinTools, agent.BuiltinTool{Tool: notifyTool})
	}

	pluginToolsBuilder := func(ctx context.Context, build pkgplugins.ToolBuildContext) []pkgtools.Tool {
		return phost.BuildEnabledTools(ctx, build)
	}
	skillStoreAdapter := pluginhost.NewSkillStoreAdapter(ss.diskSync)
	// Asset authority is selected by capability: a configured object store is the
	// shared authority; otherwise the local filesystem under STELLA_HOME is the
	// single-node authority. This replaces the former blob process-global
	// (blob.SetDefault/Default) with one service injected into every consumer.
	blobStore, err := blob.NewStoreFromConfig(cfg.Blob)
	if err != nil {
		return nil, err
	}
	assetStore, err := asset.NewStore(config.StellaHome(), blobStore, slog.Default())
	if err != nil {
		return nil, err
	}
	homeDir, _ := os.UserHomeDir()
	systemPromptBuilder, err := sessionaccess.NewSystemPromptBuilder(sessionaccess.SystemPromptDeps{
		StellaHome: config.StellaHome(),
		HomeDir:    homeDir,
		Memory:     memProvider,
		Agents:     sessionaccess.ConfigPromptAgentStore{Store: store},
		Projects:   sessionaccess.NewSQLPromptProjectStore(db),
		Workspace:  sessionaccess.AgentPromptWorkspace{},
		Plugins:    phost,
		SkillStore: skillStoreAdapter,
		Skills:     skills.BuildPromptSection,
	})
	if err != nil {
		return nil, fmt.Errorf("build session prompt service: %w", err)
	}
	sessionAccess, err := sessionaccess.NewService(memProvider, db, store, authStore, assetStore, authorizer, sessionaccess.WithSystemPromptBuilder(systemPromptBuilder))
	if err != nil {
		return nil, fmt.Errorf("build session/workspace service: %w", err)
	}

	if err := registerReflectBuiltin(schedulerSvc, reflect.Config{
		Memory:            memProvider,
		Store:             store,
		SkillStore:        skillStoreAdapter,
		UsageCuratorStore: reflect.NewSQLUsageCuratorStoreForPool(db),
		Notifier:          dispatcher,
		StateStore:        pluginhost.NewScopedStateStore(phost.StateStore(), "reflect"),
		Workspace:         config.StellaHome(),
		Providers:         providerStreamBuilder,
		Services:          &lazyServiceManager{get: func() agent.ServiceManager { return poolMgr }},
	}, cfg.Reflect.Interval, cfg.Reflect.CuratorMode); err != nil {
		return nil, err
	}

	pluginHooksBuilder := func(ctx context.Context) []hooks.HookPlugin {
		return phost.BuildEnabledHooks(ctx, pluginhooks.BuildContext{ToolsBinDir: binaries.BinDir(config.StellaHome())})
	}

	// The trace hook is server-level infrastructure, not a user-managed plugin:
	// it always runs and shares its enabled flag with the global tracer provider
	// (both derive from observability.LoadConfig) so there is a single source of
	// truth for whether OTel export is active. It is registered as a core hook so
	// plugin reloads never rebuild or close it out from under in-flight runners.
	// Its constructor starts no goroutine (#708 D); the composition root starts
	// its idle-session reaper here, bound to the daemon lifecycle context.
	traceHook := tracehook.New(observability.LoadConfig().Enabled, cfg.Observability.RecordToolIO)
	traceHook.Start(parent)
	coreHooks := []hooks.HookPlugin{traceHook}

	toolLifecycle := buildToolLifecycle(phost)
	promptSectionsBuilder := func(ctx context.Context, build pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error) {
		return phost.SystemPromptSections(ctx, build)
	}
	sessionPluginViewBuilder := func(ctx context.Context) (pkgplugins.SessionPluginView, error) {
		return phost.SessionPluginView(ctx)
	}

	var vaultSvc *vault.Service
	if vaultKey := cfg.Vault.Key; vaultKey != "" {
		var err error
		vaultSvc, err = vault.NewServiceForPool(db, vaultKey)
		if err != nil {
			slog.Warn("vault service init failed; vault endpoints and vault tool will be unavailable", "error", err)
			vaultSvc = nil
		}
	}

	workerExcludedTools := []string{goal.ToolName, scheduler.ToolName, workflowpkg.ToolName}
	// The goal River worker is a durable Agent invocation boundary. It gets the
	// same authoritative PEP as HTTP/channel paths, but reconstructs authority
	// exclusively from the persisted attempt owner and executor.
	goalAgentAccess := agentAccess

	goalSvc, err := goal.Boot(goal.BootConfig{
		DB:            db,
		Services:      &lazyServiceManager{get: func() agent.ServiceManager { return poolMgr }},
		ExcludedTools: workerExcludedTools,
		AgentAccess:   goalAgentAccess,
		Capabilities: goal.CapabilityProbeFunc(func() bool {
			plugins, err := store.ListPlugins(context.Background())
			if err != nil {
				return false
			}
			return config.ActiveSandboxBackend(plugins) != config.SandboxBackendNone
		}),
		Chat: func(ctx context.Context, p goal.TaskChatParams) <-chan agent.Event {
			var svc *agent.Service
			if poolMgr != nil {
				svc = poolMgr.GetService(p.AgentID)
				if svc == nil {
					svc = poolMgr.Default()
				}
			}
			if svc == nil {
				out := make(chan agent.Event, 1)
				out <- agent.Event{Err: fmt.Errorf("no agent service for %s", p.AgentID)}
				close(out)
				return out
			}
			chatReq := agent.TaskChatRequest{
				SessionID:        p.SessionID,
				UserID:           p.UserID,
				AgentID:          p.AgentID,
				ProjectID:        p.ProjectID,
				Message:          p.Prompt,
				ExtraTools:       p.ExtraTools,
				ExcludedTools:    p.ExcludedTools,
				OnSandboxSession: p.OnSandboxSession,
				Authority:        p.Authority,
			}
			// Decomposition runs on the goal's KindDelegate planning session;
			// execution on the KindTask worker session. They resolve differently.
			if p.Decompose {
				return svc.ChatForGoalDecomposition(ctx, chatReq)
			}
			return svc.ChatForTask(ctx, chatReq)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("boot goal service: %w", err)
	}

	workflowSvc := workflowpkg.New(db, goalSvc.Goal, authorizer, agentAccess)
	schedulerSvc.SetWorkflowRunner(schedulerWorkflowAdapter{svc: workflowSvc})

	// Build the shared credentials/email/share services once, with the final
	// resolved base URL — no localhost placeholder to mutate later. Each domain
	// owns its own sqlc query set (the *ForPool constructors), so the composition
	// root passes only the pool. These same instances back both the agent tools
	// (below) and the HTTP endpoints (via server.Deps).
	credSvc := connections.NewServiceForPool(vaultSvc, db, oauth.NewFlowStore(), baseURL)
	emailSvc := email.NewServiceForPool(vaultSvc, db)
	if ps.oauthRegistry != nil {
		credSvc.SetRegistry(ps.oauthRegistry)
		if vaultSvc != nil {
			vaultSvc.AddSystemManagedNames(ps.oauthRegistry.VaultKeys()...)
		}
	}
	recallyStore := recally.NewStore(db)
	recallySvc := recally.NewService(recallyStore, config.StellaHome())
	shareSvc := sharepkg.NewServiceForPool(db, memProvider, recallyStore, assetStore, config.StellaHome(), baseURL)

	// MCP registration service: one instance shared by the HTTP API and the agent
	// runtime. Built here (before StartAll) so its tool provider can be bound into
	// the pool as a static capability rather than injected after agents start.
	var mcpVault mcp.Vault
	if vaultSvc != nil {
		mcpVault = vaultSvc
	}
	mcpSvc := mcp.NewServiceForPool(db, mcpVault)

	serviceTools := []agent.BuiltinTool{
		{Tool: goal.NewTool(goalSvc), Available: agent.BuiltinToolAvailable},
		{Tool: scheduler.NewTool(schedulerSvc), Available: agent.BuiltinToolAvailable},
		{Tool: workflowpkg.NewTool(workflowSvc), Available: agent.BuiltinToolAvailable},
		{Tool: connections.NewTool(credSvc), Available: oauthToolAvailable(credSvc)},
		{Tool: email.NewTool(emailSvc), Available: emailToolAvailable(vaultSvc)},
		{Tool: sharepkg.NewTool(shareSvc), Available: agent.BuiltinToolAvailable},
		{Tool: recally.NewTool(recallySvc), Available: agent.BuiltinToolAvailable},
	}
	if vaultSvc != nil {
		serviceTools = append(serviceTools, agent.BuiltinTool{Tool: vault.NewTool(vaultSvc, credSvc), Available: agent.BuiltinToolAvailable})
	}
	builtinTools = append(builtinTools, serviceTools...)

	// The agent domain owns project resolution/ensuring and tool-override
	// fetching; the composition root passes the pool, not raw queries.
	projectStore := agent.NewProjectStore(db, store, assetStore)

	poolMgr = agent.NewPoolManager(store, memProvider,
		agent.WithCompactionPM(agent.CompactionConfig{}.WithDefaults()),
		agent.WithAssetStorePM(assetStore),
		agent.WithBuiltinTools(builtinTools),
		agent.WithPluginToolsBuilder(pluginToolsBuilder),
		agent.WithPluginHooksBuilder(pluginHooksBuilder),
		agent.WithCoreHooks(coreHooks),
		agent.WithProviderStreamBuilder(providerStreamBuilder),
		agent.WithPromptSectionsBuilder(promptSectionsBuilder),
		agent.WithSessionPluginViewBuilder(sessionPluginViewBuilder),
		agent.WithBeforeRunBuilderPM(func(ctx context.Context, build pkgplugins.BeforeRunContext) (pkgplugins.BeforeRunResult, error) {
			return phost.BeforeRun(ctx, build)
		}),
		agent.WithToolLifecyclePM(toolLifecycle),
		agent.WithToolOverrideFetcher(agent.NewToolOverrideStore(db).Fetch),
		agent.WithSkillStore(skillStoreAdapter),
		agent.WithProjectResolver(projectStore.Resolve),
		agent.WithProjectEnsurerPM(projectStore.Ensure),
	)

	// Bind the static Vault/MCP/OAuth capabilities into the pool BEFORE StartAll,
	// as one-shot pre-start binds. Binding them up front means agents are built
	// once, with the full capability set, rather than rebuilt after a late setter.
	if vaultSvc != nil {
		if err := poolMgr.BindVaultEnvLoader(vaultSvc); err != nil {
			return nil, fmt.Errorf("bind vault env loader: %w", err)
		}
	}
	if err := poolMgr.BindSessionAccess(sessionaccess.NewAgentSessionAccess(sessionAccess)); err != nil {
		return nil, fmt.Errorf("bind session access: %w", err)
	}
	if err := sessionAccess.BindRuntimeManager(sessionaccess.NewRuntimeManager(poolMgr)); err != nil {
		return nil, fmt.Errorf("bind session runtime manager: %w", err)
	}
	if err := poolMgr.BindMCPToolProvider(mcp.NewToolProvider(mcpSvc)); err != nil {
		return nil, fmt.Errorf("bind mcp tool provider: %w", err)
	}
	if ps.oauthRegistry != nil {
		if err := poolMgr.BindOAuthRegistry(ps.oauthRegistry); err != nil {
			return nil, fmt.Errorf("bind oauth registry: %w", err)
		}
	}

	if err := poolMgr.StartAll(parent); err != nil {
		return nil, fmt.Errorf("start pool manager: %w", err)
	}

	// The runner invalidator lets credential/token refresh propagate to running
	// pools. It targets the pool but is not a pool capability, so it is wired
	// after StartAll; the shared credSvc is then fully configured before it is
	// handed to the admin server via Deps.
	credSvc.SetInvalidator(poolMgr)

	// Composition root for River: both the scheduler and goal subsystems are now
	// built, so assemble the single shared working client from their queues and
	// inject it back into each. runServer owns its Start/Stop.
	riverClient, err := buildSharedRiverClient(db, schedulerSvc, goalSvc, embeddingSvc, cfg.Lifecycle.RiverSoftStopTimeout)
	if err != nil {
		return nil, err
	}

	// Seal the plugin host: all static plugin registrations and capability
	// bindings are complete. This validates them once and refuses any late
	// static registration; the dynamic desired-state surface (ApplyChannel /
	// RegisterManifestPlugins, used by the background reconcile below and by
	// runtime admin edits) stays available.
	if err := phost.Seal(); err != nil {
		return nil, fmt.Errorf("seal plugin host: %w", err)
	}

	backgroundTasks := &sync.WaitGroup{}
	if ps.manifestToReconcile != nil {
		reconcileManifestPluginsInBackground(parent, backgroundTasks, ps.manifestToReconcile, config.StellaHome())
	}
	backfillRecallyContentInBackground(parent, backgroundTasks, recallySvc)

	result := &setupResult{
		ctx:                      parent,
		cfg:                      cfg,
		db:                       db,
		embedded:                 embedded,
		mem:                      memProvider,
		store:                    store,
		authStore:                authStore,
		agentAccess:              agentAccess,
		sessionAccess:            sessionAccess,
		pluginHost:               phost,
		channelRuntimeServices:   ps.channelRuntimeServices,
		poolManager:              poolMgr,
		schedulerSvc:             schedulerSvc,
		goalSvc:                  goalSvc,
		vaultSvc:                 vaultSvc,
		mcpSvc:                   mcpSvc,
		credSvc:                  credSvc,
		emailSvc:                 emailSvc,
		shareSvc:                 shareSvc,
		recallySvc:               recallySvc,
		assetStore:               assetStore,
		workflowSvc:              workflowSvc,
		embeddingSvc:             embeddingSvc,
		riverClient:              riverClient,
		builtinTools:             builtinTools,
		notifier:                 dispatcher,
		pluginToolsBuilder:       pluginToolsBuilder,
		promptSectionsBuilder:    promptSectionsBuilder,
		sessionPluginViewBuilder: sessionPluginViewBuilder,
		toolLifecycle:            toolLifecycle,
		skillStore:               skillStoreAdapter,
		cliUserID:                0,
		oauthRegistry:            ps.oauthRegistry,
		backgroundTasks:          backgroundTasks,
	}
	// Ownership of the embedded server moves to result; clear the local so the
	// cleanup defer above becomes a no-op on this success path.
	embedded = nil
	return result, nil
}

func ensureEmbeddedAssets() error {
	// Remove the CLI binary older releases copied here so stale copies don't linger on sandbox PATH.
	_ = os.Remove(filepath.Join(config.StellaHome(), "bin", "stella"))
	_ = os.Remove(filepath.Join(config.StellaHome(), "bin", "stella.exe"))
	if err := binaries.EnsureTools(config.StellaHome()); err != nil {
		return fmt.Errorf("extract embedded tools: %w", err)
	}
	if err := binaries.VerifyTools(config.StellaHome()); err != nil {
		return err
	}
	if err := resources.EnsureBuiltinSkills(filepath.Join(config.StellaHome(), ".agents", "skills")); err != nil {
		return fmt.Errorf("extract builtin skills: %w", err)
	}
	return nil
}

func setupScheduler(db *pgxpool.Pool, phost *pluginhost.Host, authorizer authz.Authorizer, agentAccess *agentaccess.Service) (*scheduler.Service, error) {
	// External-river mode: the scheduler does not build its own River client. The
	// composition root (buildSharedRiverClient) assembles the single process-wide
	// working client from both the scheduler and goal queues and injects it back
	// via BindRiverClient, so there is exactly one electable River client per
	// database (see db.NewWorkingRiverClient).
	//
	// WithAuthorization wires the unified Authorizer + agent-access gate so the
	// scheduler Service is the sole PEP for job resources (HTTP + tool).
	svc, err := scheduler.New(db, scheduler.WithExternalRiver(), scheduler.WithAuthorization(authorizer, agentAccess))
	if err != nil {
		return nil, fmt.Errorf("create scheduler service: %w", err)
	}
	phost.SetSchedulerService(newSchedulerServiceAdapter(svc, phost.Runtime()))
	return svc, nil
}

// buildSharedRiverClient is the composition root for River: it assembles the
// single process-wide working client from every subsystem's queue + worker
// (scheduler and goal), then injects it back into each so they enqueue and
// register periodic jobs against the same client. There must be exactly one
// electable River client per database (see db.NewWorkingRiverClient); this is
// where that invariant is enforced. The caller owns the returned client's
// Start/Stop lifecycle (runServer); the subsystems only use it.
func buildSharedRiverClient(db *pgxpool.Pool, schedulerSvc *scheduler.Service, goalSvc *goal.Service, embeddingSvc *embedding.Service, softStopTimeout time.Duration) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	scheduler.RegisterRiverWorker(workers, schedulerSvc)
	goalSvc.RegisterRiverWorker(workers)

	queues := map[string]river.QueueConfig{}
	sn, sc := scheduler.SchedulerQueueConfig()
	queues[sn] = sc
	gn, gc := goalSvc.GoalQueueConfig()
	queues[gn] = gc
	gtn, gtc := goalSvc.GoalTickQueueConfig()
	queues[gtn] = gtc

	// The embedding lane is opt-in: only contribute its backfill worker + queue
	// when an embedding provider is configured.
	if embeddingSvc != nil {
		embeddingSvc.RegisterRiverWorker(workers)
		en, ec := embeddingSvc.BackfillQueueConfig()
		queues[en] = ec
	}

	client, err := appdb.NewWorkingRiverClient(db, queues, workers, slog.With("component", "river"), softStopTimeout)
	if err != nil {
		return nil, fmt.Errorf("build shared river client: %w", err)
	}
	// One-shot pre-start binds: each subsystem rejects a nil/duplicate/late bind.
	if err := schedulerSvc.BindRiverClient(client); err != nil {
		return nil, err
	}
	if err := goalSvc.BindRiverClient(client); err != nil {
		return nil, err
	}
	if embeddingSvc != nil {
		if err := embeddingSvc.BindRiverClient(client); err != nil {
			return nil, err
		}
	}
	return client, nil
}

// schedulerWorkflowAdapter bridges the scheduler's WorkflowRunner port to the
// workflow service. It holds no queries of its own — every read goes through the
// workflow domain (svc), so the composition root carries no application SQL.
type schedulerWorkflowAdapter struct {
	svc *workflowpkg.Service
}

func (a schedulerWorkflowAdapter) ValidateScheduledWorkflow(ctx context.Context, req scheduler.WorkflowValidateRequest) (scheduler.ScheduledWorkflow, error) {
	wf, err := a.svc.Get(ctx, req.UserID, req.AgentID, req.WorkflowID)
	if err != nil {
		return scheduler.ScheduledWorkflow{}, err
	}
	return scheduler.ScheduledWorkflow{ID: wf.ID, FullyFrozen: wf.FullyFrozen}, nil
}

func (a schedulerWorkflowAdapter) LatestWorkflowRun(ctx context.Context, req scheduler.WorkflowLatestRunRequest) (scheduler.WorkflowRunState, error) {
	rs, err := a.svc.LatestRunState(ctx, req.WorkflowID)
	if err != nil {
		return scheduler.WorkflowRunState{}, err
	}
	return scheduler.WorkflowRunState{
		Found:            rs.Found,
		Status:           rs.Status,
		IdempotencyKey:   rs.IdempotencyKey,
		RootGoalID:       rs.RootGoalID,
		RootGoalTerminal: rs.RootGoalTerminal,
	}, nil
}

func (a schedulerWorkflowAdapter) InstantiateWorkflow(ctx context.Context, req scheduler.WorkflowInstantiateRequest) (scheduler.WorkflowInstantiateResult, error) {
	// A fired scheduled workflow is a durable worker action: reconstruct the
	// owner+executor authority from the persisted job and begin a fresh workflow
	// evaluation, so a since-revoked agent assignment stops the run.
	authority, err := agentaccess.WorkerAgentAuthority(req.UserID, req.AgentID)
	if err != nil {
		return scheduler.WorkflowInstantiateResult{}, err
	}
	acc, err := a.svc.Begin(ctx, authority)
	if err != nil {
		return scheduler.WorkflowInstantiateResult{}, err
	}
	run, _, err := acc.Instantiate(ctx, req.WorkflowID, req.Inputs, req.IdempotencyKey)
	if err != nil {
		return scheduler.WorkflowInstantiateResult{}, err
	}
	rootID := ""
	if run.RootGoalID.Valid {
		rootID = run.RootGoalID.String
	}
	return scheduler.WorkflowInstantiateResult{RunID: run.ID, RootGoalID: rootID}, nil
}

func wireSchedulerCallbacks(svc *scheduler.Service, poolMgr *agent.PoolManager, dispatcher *notify.Dispatcher, access *agentaccess.Service) {
	if svc == nil {
		return
	}
	svc.SetOnJob(func(ctx context.Context, job scheduler.Job) error {
		if job.OwnerKind == scheduler.JobOwnerPlugin {
			return nil
		}
		// Every durable execution reconstructs its authority from persisted owner
		// data and performs a fresh PEP use. Ownerless user rows fail closed.
		if access == nil || job.AgentID == "" {
			return fmt.Errorf("scheduler: missing agent authorization data for job %s", job.ID)
		}
		var authorityErr error
		var authority authz.Authority
		switch job.OwnerKind {
		case scheduler.JobOwnerUser:
			if job.UserID == "" {
				return fmt.Errorf("scheduler: user job %s has no persisted owner", job.ID)
			}
			authority, authorityErr = agentaccess.WorkerAgentAuthority(job.UserID, job.AgentID)
		case scheduler.JobOwnerSystem:
			authority, authorityErr = agentaccess.SystemAgentAuthority("scheduler")
		default:
			return fmt.Errorf("scheduler: unsupported durable owner kind %q", job.OwnerKind)
		}
		if authorityErr != nil {
			return authorityErr
		}
		if _, err := access.Use(ctx, authority, job.AgentID); err != nil {
			return fmt.Errorf("scheduler: agent use denied for job %s: %w", job.ID, err)
		}

		agentSvc := poolMgr.GetService(job.AgentID)
		if agentSvc == nil && job.AgentID == "" {
			agentSvc = poolMgr.Default()
		}
		if agentSvc == nil {
			slog.Warn("scheduler: no service available for job", "job_id", job.ID, "agent_id", job.AgentID)
			return fmt.Errorf("no agent service available for job %s (agent %q)", job.ID, job.AgentID)
		}
		agentID := agentSvc.AgentID
		sessionID := scheduler.RunSessionIDFromContext(ctx)
		if sessionID == "" {
			if job.UserID != "" {
				sessionID = job.UserSessionID(job.UserID)
			} else {
				sessionID = job.SessionID()
			}
		}
		ch := agentSvc.ChatForScheduler(schedulerJobContext(ctx, agentID, job), agent.SchedulerChatRequest{
			SessionID: sessionID,
			UserID:    job.UserID,
			AgentID:   agentID,
			Message:   schedulerJobMessage(job),
			Authority: authority,
		})
		// Keep the last step's text — that's the final assistant answer;
		// earlier steps are tool-call narration.
		var runErr error
		var output, step strings.Builder
		for evt := range ch {
			if evt.Err != nil {
				runErr = evt.Err
				slog.Error("scheduler job error", "job_id", job.ID, "error", evt.Err)
			}
			if evt.Text != "" {
				step.WriteString(evt.Text)
			}
			if evt.Step != nil && evt.Step.Kind == "finish" && step.Len() > 0 {
				output.Reset()
				output.WriteString(step.String())
				step.Reset()
			}
		}
		if step.Len() > 0 {
			output.Reset()
			output.WriteString(step.String())
		}
		scheduler.RunOutputSinkFromContext(ctx).Set(strings.TrimSpace(output.String()))
		return runErr
	})
}

// backfillRecallyContentInBackground eager-copies legacy recally article bodies
// from their disk mirrors into PostgreSQL at startup, so bodies survive on hosts
// where the pod-local disk that held the mirror is gone. It runs off the daemon
// lifecycle context in a background goroutine so it never delays startup.
func backfillRecallyContentInBackground(ctx context.Context, wg *sync.WaitGroup, svc *recally.Service) {
	if svc == nil {
		return
	}
	wg.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recally content backfill panic", "panic", r)
			}
		}()
		scanned, backfilled, missing, err := svc.BackfillMissingContent(ctx)
		if err != nil {
			slog.Warn("recally content backfill failed", "scanned", scanned, "backfilled", backfilled, "missing", missing, "error", err)
			return
		}
		if scanned > 0 {
			slog.Info("recally content backfill complete", "scanned", scanned, "backfilled", backfilled, "missing", missing)
		}
	})
}

func (s *setupResult) waitBackgroundTasks() {
	if s != nil && s.backgroundTasks != nil {
		s.backgroundTasks.Wait()
	}
}
