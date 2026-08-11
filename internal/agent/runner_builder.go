package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/CherryHQ/stella/internal/agent/prompt"
	"github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/config"
	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/internal/memory"
	skillstool "github.com/CherryHQ/stella/internal/skills"
	"github.com/CherryHQ/stella/internal/vault"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/tools"
)

// MCPToolProvider surfaces external MCP-server tools into an agent's tool
// registry for a (user, agent) context. Implemented by *mcp.ToolProvider; kept
// as an interface here so the agent package need not depend on the MCP client
// internals and tests can stub it.
type MCPToolProvider interface {
	ToolsForContext(ctx context.Context, userID, agentID string) []tools.Tool
}

type BuiltinTool struct {
	Tool      tools.Tool
	Available func(context.Context, RunnerParams) bool
}

// SessionImagePipeline is the complete ordinary-session image boundary.
type SessionImagePipeline interface {
	Enrich(context.Context, string, string, []ai.ContentBlock) ([]ai.ContentBlock, error)
	Load(context.Context, string, string) (ai.ImageContent, error)
}

const runnerScratchDir = "runner-scratch"

// newRunnerScratch creates a disposable runner-owned child outside Home
// authority. Its structural parent is trusted host-owned state. Close and
// construction failure clean best-effort; crashes may leave operator-cleaned
// children. Isolating providers mount only the exact returned child.
func newRunnerScratch(stellaHome string) (string, func() error, error) {
	homeRoot, err := os.OpenRoot(stellaHome)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = homeRoot.Close() }()
	if err := homeRoot.Mkdir(runnerScratchDir, 0o700); err != nil && !os.IsExist(err) {
		return "", nil, err
	}

	root, err := homeRoot.OpenRoot(runnerScratchDir)
	if err != nil {
		return "", nil, err
	}
	info, lstatErr := homeRoot.Lstat(runnerScratchDir)
	openedInfo, statErr := root.Stat(".")
	if lstatErr != nil || statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, openedInfo) {
		_ = root.Close()
		return "", nil, fmt.Errorf("scratch root %q is not a directory", filepath.Join(stellaHome, runnerScratchDir))
	}
	if err := root.Chmod(".", 0o700); err != nil {
		_ = root.Close()
		return "", nil, err
	}

	var name string
	for range 100 {
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			_ = root.Close()
			return "", nil, err
		}
		name = "runner-" + hex.EncodeToString(random[:])
		if err := root.Mkdir(name, 0o700); err == nil {
			break
		} else if !os.IsExist(err) {
			_ = root.Close()
			return "", nil, err
		}
		name = ""
	}
	if name == "" {
		_ = root.Close()
		return "", nil, fmt.Errorf("create runner scratch: too many collisions")
	}
	dir := filepath.Join(stellaHome, runnerScratchDir, name)
	var once sync.Once
	var cleanupErr error
	cleanup := func() error {
		once.Do(func() {
			cleanupErr = errors.Join(root.RemoveAll(name), root.Close())
		})
		return cleanupErr
	}
	return dir, cleanup, nil
}

func BuiltinToolAvailable(_ context.Context, params RunnerParams) bool {
	return params.UserID != "" && params.AgentID != ""
}

// runnerBuilderConfig holds all dependencies needed to assemble a NewRunnerFunc.
type runnerBuilderConfig struct {
	Snap                     *config.Snapshot
	BuiltinTools             []BuiltinTool
	PluginToolsBuilder       PluginToolsBuilder
	ProviderStreamBuilder    ProviderStreamBuilder
	PromptSectionsBuilder    prompt.SectionsBuilder
	SessionPluginViewBuilder SessionPluginViewBuilder
	SkillStore               pkgplugins.SkillStore
	SkillReadAuthorizer      skillstool.SkillReadAuthorizer
	MCPToolProvider          MCPToolProvider
	ToolOverrideFetcher      ToolOverrideFetcher
	ToolLifecycle            *coreagent.ToolLifecycle
	SandboxBackendFn         func(ctx context.Context) string
	VaultEnvLoader           sandbox.VaultEnvLoader
	TokenManager             *oauth.TokenManager
	ProjectResolver          ProjectResolverFunc
	SessionImages            SessionImagePipeline
	WorkspaceViewer          home.WorkspaceViewer
}

// newRunnerFunc assembles a NewRunnerFunc for a given config snapshot.
// The returned func creates runners scoped to one agent's provider, model,
// workspace, and system prompt. Memory provider, user ID, and agent ID are
// injected per-session from RunnerParams. Runner execution is always user-scoped,
// so per-user workspace directories are created for every runner instance.
//
// Hooks are not part of the builder — they are injected via RunnerParams.HooksFn
// by the Pool, keeping hook lifecycle fully decoupled from model/provider config.
func newRunnerFunc(cfg runnerBuilderConfig) NewRunnerFunc {
	return func(ctx context.Context, params RunnerParams) (Runner, error) {
		modelRef := params.Model
		if modelRef == "" {
			modelRef = cfg.Snap.Model
		}

		// Parse provider/model from the ref string.
		provID, modelID := config.ParseModelRef(modelRef)
		if provID == "" {
			provID = cfg.Snap.Provider
		}
		creds := cfg.Snap.ResolveProviderCreds(provID)
		apiName := creds.Type
		if apiName == "" {
			apiName = provID
		}
		providerID := creds.ProviderID
		if providerID == "" {
			providerID = provID
		}

		if params.GuestID != "" {
			return newRunner(ctx, runnerConfig{
				NoCapabilities: true,
				Provider: providerConfig{
					ProviderID: providerID,
					API:        apiName,
					Model:      modelID,
					Input:      cfg.Snap.ModelInput(provID, modelID),
					APIKey:     creds.APIKey,
					BaseURL:    creds.BaseURL,
					Builder:    cfg.ProviderStreamBuilder,
				},
				Thinking: params.Thinking,
				System:   prompt.BuildGuestSystemPrompt(cfg.Snap.SystemPrompt),
			})
		}
		if cfg.WorkspaceViewer == nil {
			return nil, fmt.Errorf("runner: Home workspace resolver is required")
		}

		view, err := cfg.WorkspaceViewer.WorkspaceView(ctx, home.WorkspaceRequest{UserID: params.UserID, GroupID: params.GroupID, AgentID: params.AgentID})
		if err != nil {
			return nil, fmt.Errorf("resolve Home workspace: %w", err)
		}
		var (
			userRoot      string
			workspaceRoot string
			userDataDir   string
			// projectValidateRoot is the per-(principal, agent) dir a project must
			// live under: a project is owned by the agent (see #442), so it stays
			// scoped to the agent's subdir of the shared user/group home.
			projectValidateRoot string
			scratchCleanup      func() error
		)
		if params.UserID != "" || params.GroupID != "" {
			userRoot = view.PrincipalRoot
			workspaceRoot, userDataDir = view.AgentRoot, view.DataRoot
			projectValidateRoot = workspaceRoot
		} else {
			if params.ProjectID != "" {
				return nil, fmt.Errorf("runner: user-less runs cannot use a project")
			}
			userRoot, scratchCleanup, err = newRunnerScratch(config.StellaHome())
			if err != nil {
				return nil, fmt.Errorf("create user-less scratch: %w", err)
			}
			workspaceRoot, projectValidateRoot = userRoot, userRoot
		}

		// Resolve project directory when session has a project.
		var projectRoot string
		if params.ProjectID != "" && cfg.ProjectResolver != nil {
			dir, err := cfg.ProjectResolver(ctx, params.ProjectID, params.UserID)
			if err != nil {
				slog.Warn("project resolution failed", "project_id", params.ProjectID, "error", err)
			} else if dir != "" {
				if err := ValidateProjectDir(dir, projectValidateRoot); err != nil {
					slog.Warn("project dir validation failed", "project_id", params.ProjectID, "base_dir", dir, "error", err)
				} else {
					projectRoot = dir
				}
			}
		}

		// Extract memory provider from params (typed as any to avoid circular imports).
		var memProvider memory.Provider
		if params.Memory != nil {
			memProvider, _ = params.Memory.(memory.Provider)
		}

		homeDir, _ := os.UserHomeDir()
		pluginView := pkgplugins.SessionPluginView{}
		if cfg.SessionPluginViewBuilder != nil {
			pluginView, _ = cfg.SessionPluginViewBuilder(ctx)
		}
		promptBuild := pkgplugins.SystemPromptContext{
			StellaHome:          config.StellaHome(),
			HomeDir:             homeDir,
			AgentRoot:           cfg.Snap.Workspace,
			ProjectRoot:         projectRoot,
			UserID:              params.UserID,
			AgentID:             params.AgentID,
			UserRoot:            userRoot,
			WorkspaceRoot:       projectValidateRoot,
			SkillStore:          cfg.SkillStore,
			RegisteredPluginIDs: append([]string(nil), pluginView.RegisteredPluginIDs...),
			EnabledPluginIDs:    append([]string(nil), pluginView.EnabledPluginIDs...),
			DisabledSkillRefs:   append([]string(nil), cfg.Snap.DisabledSkillRefs...),
		}
		var sections []pkgplugins.SystemPromptSection
		if cfg.PromptSectionsBuilder != nil {
			sections, _ = cfg.PromptSectionsBuilder(ctx, promptBuild)
		}
		skillPromptBuild := promptBuild
		if params.GroupID != "" {
			skillPromptBuild.UserID, skillPromptBuild.UserRoot, skillPromptBuild.WorkspaceRoot = "", "", ""
		}
		if skillsSection, err := skillstool.BuildPromptSection(ctx, skillPromptBuild); err == nil && skillsSection.Title != "" && skillsSection.Content != "" {
			sections = append(sections, skillsSection)
		}
		if params.GroupID == "" && cfg.VaultEnvLoader != nil {
			metas, err := cfg.VaultEnvLoader.ListAmbientSecretMetas(ctx, params.UserID, params.AgentID)
			if err != nil {
				slog.Warn("vault secret metadata unavailable",
					"component", "runner_builder",
					"user_id", params.UserID,
					"agent_id", params.AgentID,
					"project_id", params.ProjectID,
					"error", err,
				)
			} else if len(metas) > 0 {
				sections = append(sections, pkgplugins.SystemPromptSection{
					Title:   "Available Secrets",
					Content: "These vault secret names are already available as environment variables in bash. Values are never shown; use the names exactly as the CLI or tool expects.\n\n" + formatAvailableSecretMetas(metas),
				})
			}
		}

		// Build the full system prompt per-session with profile from memory provider.
		// Group sessions skip private profile injection (D9 isolation); group memory
		// is Phase 3 concern.
		promptUserID := params.UserID
		if params.GroupID != "" {
			promptUserID = ""
		}
		system := prompt.BuildSystemPromptFromDB(ctx, prompt.DBPromptParams{
			SystemPrompt: cfg.Snap.SystemPrompt,
			AgentSoul:    cfg.Snap.Soul,
			Memory:       memProvider,
			UserID:       promptUserID,
			AgentID:      params.AgentID,
			GroupID:      params.GroupID,
			StellaHome:   config.StellaHome(),
			AgentRoot:    cfg.Snap.Workspace,
			ProjectRoot:  projectRoot,
			UserRoot:     userRoot,
			Sections:     sections,
		})

		// Resolve hooks from RunnerParams — injected by Pool, not the builder.
		var hookPlugins []hooks.HookPlugin
		if params.HooksFn != nil {
			hookPlugins = params.HooksFn()
		}

		sessionSecretValues := sandbox.NewSessionSecretValues()
		sandboxCfg := sandbox.Config{
			SandboxConfig:    cfg.Snap.Sandbox,
			SandboxBackendFn: cfg.SandboxBackendFn,
			Paths: sandbox.Paths{
				StellaHome:    config.StellaHome(),
				AgentRoot:     cfg.Snap.Workspace,
				UserRoot:      userRoot,
				WorkspaceRoot: workspaceRoot,
				UserDataDir:   userDataDir,
				ProjectRoot:   projectRoot,
			},
			UserID:              params.UserID,
			GroupID:             params.GroupID,
			AgentID:             params.AgentID,
			SessionID:           params.SessionID,
			ProjectID:           params.ProjectID,
			SessionEnvSpecs:     append([]pkgplugins.SessionEnvSpec(nil), pluginView.SessionEnvSpecs...),
			VaultEnvLoader:      cfg.VaultEnvLoader,
			SessionSecretValues: sessionSecretValues,
			TokenManager:        cfg.TokenManager,
			OAuthEnvBindings:    sandbox.NewOAuthEnvBindings(),
		}

		builtinTools := append([]BuiltinTool(nil), cfg.BuiltinTools...)
		perRunTools := append([]tools.Tool(nil), params.ExtraTools...)

		var canonicalImages *coreagent.CanonicalImageConfig
		if params.GroupID == "" {
			canonicalize := coreagent.ToolImageCanonicalizer(func(_ context.Context, result ai.ToolResultMessage) (ai.ToolResultMessage, error) {
				if ai.HasImage(result.Content) {
					return ai.ToolResultMessage{}, fmt.Errorf("session image enrichment is not configured")
				}
				return result, nil
			})
			if cfg.SessionImages != nil {
				canonicalize = func(ctx context.Context, result ai.ToolResultMessage) (ai.ToolResultMessage, error) {
					if !ai.HasImage(result.Content) {
						return result, nil
					}
					blocks, err := cfg.SessionImages.Enrich(ctx, params.UserID, params.AgentID, result.Content)
					if err != nil {
						return ai.ToolResultMessage{}, err
					}
					result.Content = blocks
					return result, nil
				}
			}
			load := coreagent.MediaLoader(func(context.Context, string) (ai.ImageContent, error) {
				return ai.ImageContent{}, fmt.Errorf("session image loader is not configured")
			})
			if cfg.SessionImages != nil {
				load = func(ctx context.Context, mediaID string) (ai.ImageContent, error) {
					return cfg.SessionImages.Load(ctx, params.UserID, mediaID)
				}
			}
			canonicalImages = &coreagent.CanonicalImageConfig{Load: load, CanonicalizeToolResult: canonicalize}
		}

		runner, err := newRunner(ctx, runnerConfig{
			Provider: providerConfig{
				ProviderID: providerID,
				API:        apiName,
				Model:      modelID,
				Input:      cfg.Snap.ModelInput(provID, modelID),
				APIKey:     creds.APIKey,
				BaseURL:    creds.BaseURL,
				Builder:    cfg.ProviderStreamBuilder,
			},
			Thinking:            params.Thinking,
			Sandbox:             sandboxCfg,
			System:              system,
			Sections:            sections,
			BuiltinTools:        builtinTools,
			BuiltinParams:       params,
			DisabledSkillRefs:   append([]string(nil), cfg.Snap.DisabledSkillRefs...),
			PerRunTools:         perRunTools,
			SkillStore:          cfg.SkillStore,
			SkillReadAuthorizer: cfg.SkillReadAuthorizer,
			PluginView:          pluginView,
			MCPToolProvider:     cfg.MCPToolProvider,
			ToolOverrideFetcher: cfg.ToolOverrideFetcher,
			PluginTools:         cfg.PluginToolsBuilder,
			HookPlugins:         hookPlugins,
			ToolLifecycle:       cfg.ToolLifecycle,
			DelegateRunner:      params.DelegateRunner,
			DelegateTimeout:     cfg.Snap.Runner.DelegateTimeoutDuration(),
			CanonicalImages:     canonicalImages,
			Cleanup:             scratchCleanup,
		})
		if err != nil && scratchCleanup != nil {
			_ = scratchCleanup()
		}
		return runner, err
	}
}

func formatAvailableSecretMetas(metas []vault.AmbientSecretMeta) string {
	lines := make([]string, 0, len(metas))
	for _, meta := range metas {
		line := meta.Name
		if meta.Description != "" {
			line += " — " + meta.Description
		}
		lines = append(lines, line)
	}
	return "- " + strings.Join(lines, "\n- ")
}
