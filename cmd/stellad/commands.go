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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/prompt"

	"github.com/CherryHQ/stella/internal/cli"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/connections"
	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/email"
	"github.com/CherryHQ/stella/internal/embedding"
	"github.com/CherryHQ/stella/internal/goal"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/notify"
	"github.com/CherryHQ/stella/internal/observability"
	"github.com/CherryHQ/stella/internal/observability/tracehook"
	"github.com/CherryHQ/stella/internal/pluginhost"
	"github.com/CherryHQ/stella/internal/recally"
	"github.com/CherryHQ/stella/internal/reflect"
	"github.com/CherryHQ/stella/internal/scheduler"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/internal/version"
	workflowpkg "github.com/CherryHQ/stella/internal/workflow"
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
			serviceCommand(),
			authCommand(),
		},
	}
}

type setupResult struct {
	ctx                      context.Context
	db                       *pgxpool.Pool
	embedded                 *appdb.Embedded
	mem                      memory.Provider
	store                    config.Store
	pluginHost               *pluginhost.Host
	channelRuntimeServices   *pluginhost.ChannelPlatform
	poolManager              *agent.PoolManager
	schedulerSvc             *scheduler.Service
	goalSvc                  *goal.Service
	vaultSvc                 *vault.Service
	credSvc                  *connections.Service
	shareSvc                 *sharepkg.Service
	recallySvc               *recally.Service
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

func setup(parent context.Context, _ bool) (*setupResult, error) {
	dsn := config.DatabaseURL()
	var embedded *appdb.Embedded
	if dsn == "" {
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

	schedulerSvc, err := setupScheduler(db, phost)
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
	if err := registerReflectBuiltin(schedulerSvc, reflect.Config{
		Memory:     memProvider,
		Store:      store,
		SkillStore: skillStoreAdapter,
		Notifier:   dispatcher,
		StateStore: pluginhost.NewScopedStateStore(phost.StateStore(), "reflect"),
		Workspace:  config.StellaHome(),
		Providers:  providerStreamBuilder,
		Services:   &lazyServiceManager{get: func() agent.ServiceManager { return poolMgr }},
	}); err != nil {
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
	coreHooks := []hooks.HookPlugin{tracehook.New(observability.LoadConfig().Enabled)}

	toolLifecycle := buildToolLifecycle(phost)
	promptSectionsBuilder := func(ctx context.Context, build pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error) {
		return phost.SystemPromptSections(ctx, build)
	}
	sessionPluginViewBuilder := func(ctx context.Context) (pkgplugins.SessionPluginView, error) {
		return phost.SessionPluginView(ctx)
	}

	var vaultSvc *vault.Service
	if vaultKey := os.Getenv("STELLA_VAULT_KEY"); vaultKey != "" {
		var err error
		vaultSvc, err = vault.NewService(sqlc.New(db), vaultKey)
		if err != nil {
			slog.Warn("vault service init failed; vault endpoints and vault tool will be unavailable", "error", err)
			vaultSvc = nil
		}
	}

	serviceToolNames := []string{goal.ToolName, scheduler.ToolName, workflowpkg.ToolName, connections.ToolName, email.ToolName, sharepkg.ToolName, recally.ToolName, vault.ToolName}

	goalSvc, err := goal.Boot(goal.BootConfig{
		DB:            db,
		Services:      &lazyServiceManager{get: func() agent.ServiceManager { return poolMgr }},
		ExcludedTools: serviceToolNames,
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

	workflowSvc := workflowpkg.New(db, sqlc.New(db), goalSvc.Goal)
	schedulerSvc.SetWorkflowRunner(schedulerWorkflowAdapter{svc: workflowSvc, q: sqlc.New(db)})

	credSvc := connections.NewService(vaultSvc, sqlc.New(db), oauth.NewFlowStore(), "http://localhost:25678")
	emailSvc := email.NewService(vaultSvc, sqlc.New(db))
	if ps.oauthRegistry != nil {
		credSvc.SetRegistry(ps.oauthRegistry)
	}
	recallyStore := recally.NewStore(db)
	recallyFiles := recally.NewFileManager(config.StellaHome())
	recallySvc := recally.NewService(recallyStore, recallyFiles, config.StellaHome())
	shareSvc := sharepkg.NewService(sqlc.New(db), memProvider, recallyStore, recallyFiles, config.StellaHome(), "http://localhost:25678")

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
		serviceTools = append(serviceTools, agent.BuiltinTool{Tool: vault.NewTool(vaultSvc), Available: agent.BuiltinToolAvailable})
	}
	builtinTools = append(builtinTools, serviceTools...)

	poolMgr = agent.NewPoolManager(store, memProvider,
		agent.WithCompactionPM(agent.CompactionConfig{}.WithDefaults()),
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
		agent.WithToolOverrideFetcher(toolOverrideFetcher(sqlc.New(db))),
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

	// Composition root for River: both the scheduler and goal subsystems are now
	// built, so assemble the single shared working client from their queues and
	// inject it back into each. runServer owns its Start/Stop.
	riverClient, err := buildSharedRiverClient(db, schedulerSvc, goalSvc, embeddingSvc)
	if err != nil {
		return nil, err
	}

	backgroundTasks := &sync.WaitGroup{}
	if ps.manifestToReconcile != nil {
		reconcileManifestPluginsInBackground(parent, backgroundTasks, ps.manifestToReconcile, config.StellaHome())
	}

	result := &setupResult{
		ctx:                      parent,
		db:                       db,
		embedded:                 embedded,
		mem:                      memProvider,
		store:                    store,
		pluginHost:               phost,
		channelRuntimeServices:   ps.channelRuntimeServices,
		poolManager:              poolMgr,
		schedulerSvc:             schedulerSvc,
		goalSvc:                  goalSvc,
		vaultSvc:                 vaultSvc,
		credSvc:                  credSvc,
		shareSvc:                 shareSvc,
		recallySvc:               recallySvc,
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

func setupScheduler(db *pgxpool.Pool, phost *pluginhost.Host) (*scheduler.Service, error) {
	// External-river mode: the scheduler does not build its own River client. The
	// composition root (buildSharedRiverClient) assembles the single process-wide
	// working client from both the scheduler and goal queues and injects it back
	// via SetRiverClient, so there is exactly one electable River client per
	// database (see db.NewWorkingRiverClient).
	svc, err := scheduler.New(db, scheduler.WithExternalRiver())
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
func buildSharedRiverClient(db *pgxpool.Pool, schedulerSvc *scheduler.Service, goalSvc *goal.Service, embeddingSvc *embedding.Service) (*river.Client[pgx.Tx], error) {
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

	client, err := appdb.NewWorkingRiverClient(db, queues, workers, slog.With("component", "river"))
	if err != nil {
		return nil, fmt.Errorf("build shared river client: %w", err)
	}
	schedulerSvc.SetRiverClient(client)
	goalSvc.SetRiverClient(client)
	if embeddingSvc != nil {
		embeddingSvc.SetRiverClient(client)
	}
	return client, nil
}

type schedulerWorkflowAdapter struct {
	svc *workflowpkg.Service
	q   *sqlc.Queries
}

func (a schedulerWorkflowAdapter) ValidateScheduledWorkflow(ctx context.Context, req scheduler.WorkflowValidateRequest) (scheduler.ScheduledWorkflow, error) {
	wf, err := a.svc.Get(ctx, req.UserID, req.AgentID, req.WorkflowID)
	if err != nil {
		return scheduler.ScheduledWorkflow{}, err
	}
	return scheduler.ScheduledWorkflow{ID: wf.ID, FullyFrozen: wf.FullyFrozen}, nil
}

func (a schedulerWorkflowAdapter) LatestWorkflowRun(ctx context.Context, req scheduler.WorkflowLatestRunRequest) (scheduler.WorkflowRunState, error) {
	run, err := a.q.GetLatestWorkflowRun(ctx, req.WorkflowID)
	if errors.Is(err, pgx.ErrNoRows) {
		return scheduler.WorkflowRunState{}, nil
	}
	if err != nil {
		return scheduler.WorkflowRunState{}, err
	}
	state := scheduler.WorkflowRunState{Found: true, Status: run.Status, IdempotencyKey: run.IdempotencyKey}
	if run.RootGoalID.Valid {
		state.RootGoalID = run.RootGoalID.String
		root, err := a.q.GetGoal(ctx, run.RootGoalID.String)
		if err != nil {
			return scheduler.WorkflowRunState{}, err
		}
		state.RootGoalTerminal = goal.IsTerminalLifecycle(root.Lifecycle)
	}
	return state, nil
}

func (a schedulerWorkflowAdapter) InstantiateWorkflow(ctx context.Context, req scheduler.WorkflowInstantiateRequest) (scheduler.WorkflowInstantiateResult, error) {
	run, _, err := a.svc.Instantiate(ctx, workflowpkg.InstantiateInput{UserID: req.UserID, AgentID: req.AgentID, WorkflowID: req.WorkflowID, Inputs: req.Inputs, IdempotencyKey: req.IdempotencyKey})
	if err != nil {
		return scheduler.WorkflowInstantiateResult{}, err
	}
	rootID := ""
	if run.RootGoalID.Valid {
		rootID = run.RootGoalID.String
	}
	return scheduler.WorkflowInstantiateResult{RunID: run.ID, RootGoalID: rootID}, nil
}

func wireSchedulerCallbacks(svc *scheduler.Service, poolMgr *agent.PoolManager, dispatcher *notify.Dispatcher) {
	if svc == nil {
		return
	}
	svc.SetOnJob(func(ctx context.Context, job scheduler.Job) error {
		if job.OwnerKind == scheduler.JobOwnerPlugin {
			return nil
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

func (s *setupResult) waitBackgroundTasks() {
	if s != nil && s.backgroundTasks != nil {
		s.backgroundTasks.Wait()
	}
}
