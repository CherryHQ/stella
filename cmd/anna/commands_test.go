package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

type commandTestProvider struct{}

func (commandTestProvider) API() string { return "anthropic" }
func (commandTestProvider) Stream(context.Context, ai.Model, ai.Context, ai.StreamOptions) (providers.AssistantEventStream, error) {
	return nil, errors.New("not implemented")
}
func (commandTestProvider) StreamSimple(context.Context, ai.Model, ai.Context, ai.SimpleStreamOptions) (providers.AssistantEventStream, error) {
	return nil, errors.New("not implemented")
}

func testProviderRegistryBuilder(api, apiKey, baseURL string) (*providers.Registry, error) {
	reg := providers.NewRegistry()
	reg.Register(commandTestProvider{})
	return reg, nil
}

func testCoreToolsBuilder(plugintools.BuildContext) []tools.Tool { return nil }

func TestNewRunnerFactoryGo(t *testing.T) {
	snap := &config.Snapshot{
		Provider: "anthropic",
		Model:    "test-model",
		APIKey:   "test-key",
		Runner:   config.RunnerConfig{Type: "go"},
	}
	snap.Workspace = t.TempDir()

	factory, err := agent.NewRunnerFactory(snap, nil, testCoreToolsBuilder, testProviderRegistryBuilder, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewRunnerFactory: %v", err)
	}

	r, err := factory(context.Background(), runner.RunnerParams{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}

	if r == nil {
		t.Fatal("expected non-nil runner")
	}
}

func TestNewRunnerFactoryUnknown(t *testing.T) {
	snap := &config.Snapshot{
		Runner: config.RunnerConfig{Type: "invalid"},
	}

	_, err := agent.NewRunnerFactory(snap, nil, testCoreToolsBuilder, testProviderRegistryBuilder, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown runner type")
	}
	if !strings.Contains(err.Error(), "unknown runner type") {
		t.Errorf("error = %q, want contains 'unknown runner type'", err.Error())
	}
}

func TestRunHelp(t *testing.T) {
	app := newApp()
	err := app.Run([]string{"anna", "--help"})
	if err != nil {
		t.Fatalf("run --help: %v", err)
	}
}

func TestRunHelpShort(t *testing.T) {
	app := newApp()
	err := app.Run([]string{"anna", "-h"})
	if err != nil {
		t.Fatalf("run -h: %v", err)
	}
}

func TestRunGatewayNoServices(t *testing.T) {
	t.Setenv("ANNA_HOME", t.TempDir())
	config.ResetAnnaHome()
	t.Cleanup(config.ResetAnnaHome)
	app := newApp()
	err := app.Run([]string{"anna", "--admin-port", "0", "gateway"})
	if err == nil {
		t.Fatal("expected error for no configured services")
	}
	if !strings.Contains(err.Error(), "no services to run") {
		t.Errorf("err = %q, want contains 'no services to run'", err.Error())
	}
}
