package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	agentsandbox "github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/internal/platform/version"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	bridgebackend "github.com/CherryHQ/stella/plugins/sandbox/bridge"
	dockerbackend "github.com/CherryHQ/stella/plugins/sandbox/docker"
	kubernetesbackend "github.com/CherryHQ/stella/plugins/sandbox/kubernetes"
	localbackend "github.com/CherryHQ/stella/plugins/sandbox/local"
	nonebackend "github.com/CherryHQ/stella/plugins/sandbox/none"
	"github.com/CherryHQ/stella/resources"
)

const (
	dockerImageRepo = "ghcr.io/cherryhq/stella-sandbox"
	dockerDevImage  = "stella-sandbox:dev"
)

func setupSandboxBackends(ctx context.Context, cfg config.ServerConfig) (*agentsandbox.BackendRegistry, error) {
	return setupSandboxBackendsWithBoot(ctx, cfg, "")
}

// setupSandboxBackendsWithBoot binds provider labels to the same executor boot
// that owns AgentRun and SessionSandbox rows. Generation-managed resources
// remain under durable reconciliation; only legacy unmanaged resources use
// startup cleanup.
func setupSandboxBackendsWithBoot(ctx context.Context, cfg config.ServerConfig, bootID string) (*agentsandbox.BackendRegistry, error) {
	if err := config.ValidateSandboxBackend(); err != nil {
		return nil, err
	}
	var kubeClient *kubernetesbackend.Client
	if config.ActiveSandboxBackend() == config.SandboxBackendKubernetes {
		registry, err := resources.Default()
		if err != nil {
			return nil, err
		}
		initCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		kubeClient, err = kubernetesbackend.NewInCluster(initCtx, kubernetesBackendConfig(cfg, bootID, registry.BundleRevision()))
		if err != nil {
			return nil, err
		}
	}
	return agentsandbox.NewBackendRegistry(
		agentsandbox.BackendDefinition{Name: config.SandboxBackendKubernetes, Create: func(ctx context.Context, request agentsandbox.BackendRequest) (pkgsandbox.Session, error) {
			if kubeClient == nil {
				return nil, errors.New("kubernetes backend was not configured at startup")
			}
			return kubeClient.FactoryWithGeneration(request.MountSources, request.Generation, request.ExecutorBootID).CreateSession(ctx, request.Policy)
		}, ControllerFactory: func(ctx context.Context) (pkgsandbox.ResourceController, error) {
			if kubeClient == nil {
				return nil, errors.New("kubernetes backend was not configured at startup")
			}
			return kubeClient.Factory(nil).ResourceController(ctx)
		}},
		agentsandbox.BackendDefinition{Name: config.SandboxBackendDocker, Create: func(ctx context.Context, request agentsandbox.BackendRequest) (session pkgsandbox.Session, err error) {
			request.Policy.InheritEnv = true
			resourceRegistry, err := resources.Default()
			if err != nil {
				return nil, fmt.Errorf("load builtin skill bundle: %w", err)
			}
			backendConfig := dockerbackend.Config{
				Image:                    sandboxImage(),
				Generation:               request.Generation,
				ExecutorBootID:           request.ExecutorBootID,
				StellaHome:               request.Paths.StellaHome,
				ExpectedBundleRevision:   resourceRegistry.BundleRevision(),
				SessionEnvRollbacks:      maps.Clone(request.SessionEnvRollbacks),
				StableProjectionID:       request.StableProjectionID,
				StableProjectionHostRoot: request.StableProjectionRoot,
			}
			// Every resolved selection is prepared in the isolated Linux helper.
			// User-scoped installers never execute on the host.
			for _, spec := range request.BinarySpecs {
				backendConfig.SelectionToolBinaries = append(backendConfig.SelectionToolBinaries, dockerbackend.ToolBinary{
					PluginID: spec.PluginID, ConfigID: spec.ConfigID, Scope: spec.Scope, Revision: spec.Revision, PackageDigest: spec.PackageDigest,
					Name: spec.Name, Tool: spec.Tool, Version: spec.Version, Options: maps.Clone(spec.Options),
				})
			}

			factory, err := dockerbackend.NewFactoryWithMountSources(backendConfig, request.MountSources)
			if err != nil {
				return nil, err
			}
			session, err = factory.CreateSession(ctx, request.Policy)
			if err == nil {
				return session, nil
			}
			return nil, sandboxDockerSessionError(err)
		}, ControllerFactory: dockerbackend.NewResourceController},
		agentsandbox.BackendDefinition{Name: config.SandboxBackendLocal, Create: func(ctx context.Context, request agentsandbox.BackendRequest) (pkgsandbox.Session, error) {
			session, err := localbackend.NewFactoryWithMountSources(request.MountSources, localbackend.Config{StellaHome: request.Paths.StellaHome}).CreateSession(ctx, request.Policy)
			if err != nil {
				return nil, fmt.Errorf("create local session: %w", err)
			}
			return session, nil
		}},
		agentsandbox.BackendDefinition{Name: config.SandboxBackendNone, Create: func(ctx context.Context, request agentsandbox.BackendRequest) (pkgsandbox.Session, error) {
			session, err := nonebackend.NewFactoryWithMountSources(request.MountSources, nonebackend.Config{StellaHome: request.Paths.StellaHome}).CreateSession(ctx, request.Policy)
			if err != nil {
				return nil, fmt.Errorf("create host session: %w", err)
			}
			return session, nil
		}},
		agentsandbox.BackendDefinition{Name: config.SandboxBackendBridge, Create: func(ctx context.Context, request agentsandbox.BackendRequest) (pkgsandbox.Session, error) {
			session, err := bridgebackend.NewFactory(bridgebackend.Config{
				BindingDir: config.EvalBridgeBindingDir(),
				UserID:     request.UserID,
				GroupID:    request.GroupID,
			}).CreateSession(ctx, request.Policy)
			if err != nil {
				return nil, fmt.Errorf("create bridge session: %w", err)
			}
			return session, nil
		}},
	)
}

func kubernetesBackendConfig(cfg config.ServerConfig, bootID, bundleRevision string) kubernetesbackend.Config {
	return kubernetesbackend.Config{
		Image:                   cmp.Or(cfg.KubernetesSandbox.Image, sandboxImage()),
		ServerPort:              cfg.KubernetesSandbox.ServerPort,
		StartupTimeout:          cfg.KubernetesSandbox.StartupTimeout,
		ServerURL:               cfg.KubernetesSandbox.ServerURL,
		StellaHome:              config.StellaHome(),
		BundleRevision:          bundleRevision,
		BootID:                  bootID,
		SkipPreviousBootCleanup: bootID == "",
	}
}

func sandboxImage() string {
	if version.IsDev() {
		return dockerDevImage
	}
	return dockerImageRepo + ":" + strings.TrimPrefix(version.Version, "v")
}

func sandboxDockerSessionError(err error) error {
	var imageErr *dockerbackend.ImageUnavailableError
	if version.IsDev() && errors.As(err, &imageErr) {
		return fmt.Errorf("%w (run `mise run sandbox:docker:build` to build the local %q image)", err, sandboxImage())
	}
	return fmt.Errorf("create docker session: %w", err)
}
