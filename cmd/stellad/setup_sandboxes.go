package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"

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
		kubeClient, err = kubernetesbackend.NewInCluster(initCtx, kubernetesbackend.Config{Namespace: cfg.KubernetesSandbox.Namespace, OwnerName: cfg.KubernetesSandbox.PodName, OwnerUID: types.UID(cfg.KubernetesSandbox.PodUID), NodeName: cfg.KubernetesSandbox.NodeName, Deployment: cfg.KubernetesSandbox.Deployment, PVC: cfg.KubernetesSandbox.PVC, Image: cfg.KubernetesSandbox.Image, StartupTimeout: cfg.KubernetesSandbox.StartupTimeout, ServerURL: cfg.KubernetesSandbox.ServerURL, StellaHome: config.StellaHome(), BundleRevision: registry.BundleRevision()})
		if err != nil {
			return nil, err
		}
	}
	return agentsandbox.NewBackendRegistry(
		agentsandbox.BackendDefinition{Name: config.SandboxBackendKubernetes, Create: func(ctx context.Context, request agentsandbox.BackendRequest) (pkgsandbox.Session, error) {
			if kubeClient == nil {
				return nil, errors.New("kubernetes backend was not configured at startup")
			}
			return kubeClient.Factory(request.MountSources).CreateSession(ctx, request.Policy)
		}},
		agentsandbox.BackendDefinition{Name: config.SandboxBackendDocker, Create: func(ctx context.Context, request agentsandbox.BackendRequest) (session pkgsandbox.Session, err error) {
			request.Policy.InheritEnv = true
			resourceRegistry, err := resources.Default()
			if err != nil {
				return nil, fmt.Errorf("load builtin skill bundle: %w", err)
			}
			factory, err := dockerbackend.NewFactoryWithMountSources(dockerbackend.Config{
				Image:                  sandboxDockerImage(),
				StellaHome:             request.Paths.StellaHome,
				ExpectedBundleRevision: resourceRegistry.BundleRevision(),
			}, request.MountSources)
			if err != nil {
				return nil, err
			}
			session, err = factory.CreateSession(ctx, request.Policy)
			if err == nil {
				return session, nil
			}
			return nil, sandboxDockerSessionError(err)
		}},
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

func sandboxDockerImage() string {
	if version.IsDev() {
		return dockerDevImage
	}
	return dockerImageRepo + ":" + strings.TrimPrefix(version.Version, "v")
}

func sandboxDockerSessionError(err error) error {
	var imageErr *dockerbackend.ImageUnavailableError
	if version.IsDev() && errors.As(err, &imageErr) {
		return fmt.Errorf("%w (run `mise run sandbox:docker:build` to build the local %q image)", err, sandboxDockerImage())
	}
	return fmt.Errorf("create docker session: %w", err)
}
