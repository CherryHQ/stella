package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/agent"
	"github.com/vaayne/anna/internal/agent/runner"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/embedded"
	coreagent "github.com/vaayne/anna/pkg/agent"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/providers"
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

type commandTestStore struct{}

func (commandTestStore) ListProviders(context.Context) ([]config.Provider, error) { return nil, nil }
func (commandTestStore) GetProvider(context.Context, string) (config.Provider, error) {
	return config.Provider{}, errors.New("not found")
}
func (commandTestStore) CreateProvider(context.Context, config.Provider) error     { return nil }
func (commandTestStore) UpdateProvider(context.Context, config.Provider) error     { return nil }
func (commandTestStore) DeleteProvider(context.Context, string) error              { return nil }
func (commandTestStore) ListAgents(context.Context) ([]config.Agent, error)        { return nil, nil }
func (commandTestStore) ListEnabledAgents(context.Context) ([]config.Agent, error) { return nil, nil }
func (commandTestStore) GetAgent(context.Context, string) (config.Agent, error) {
	return config.Agent{}, nil
}
func (commandTestStore) CreateAgent(context.Context, config.Agent) error        { return nil }
func (commandTestStore) UpdateAgent(context.Context, config.Agent) error        { return nil }
func (commandTestStore) DeleteAgent(context.Context, string) error              { return nil }
func (commandTestStore) ListChannels(context.Context) ([]config.Channel, error) { return nil, nil }
func (commandTestStore) ListChannelsByType(context.Context, string) ([]config.Channel, error) {
	return nil, nil
}

func (commandTestStore) GetChannel(context.Context, string) (config.Channel, error) {
	return config.Channel{}, nil
}
func (commandTestStore) UpsertChannel(context.Context, config.Channel) error { return nil }
func (commandTestStore) DeleteChannel(context.Context, string) error         { return nil }
func (commandTestStore) ListPlugins(context.Context) ([]config.Plugin, error) {
	return nil, nil
}

func (commandTestStore) ListPluginsByKind(context.Context, string) ([]config.Plugin, error) {
	return nil, nil
}
func (commandTestStore) ListEnabledPlugins(context.Context) ([]config.Plugin, error) { return nil, nil }
func (commandTestStore) GetPlugin(context.Context, string) (config.Plugin, error) {
	return config.Plugin{}, nil
}
func (commandTestStore) UpsertPlugin(context.Context, config.Plugin) error    { return nil }
func (commandTestStore) SetPluginEnabled(context.Context, string, bool) error { return nil }
func (commandTestStore) SetPluginConfig(context.Context, string, map[string]any) error {
	return nil
}
func (commandTestStore) DeletePlugin(context.Context, string) error { return nil }
func (commandTestStore) GetChatAgent(context.Context, string, string, string) (string, error) {
	return "", nil
}

func (commandTestStore) SetChatAgent(context.Context, string, string, string, string) error {
	return nil
}
func (commandTestStore) DeleteChatAgent(context.Context, string, string, string) error { return nil }
func (commandTestStore) GetSetting(context.Context, string) (string, error)            { return "", nil }
func (commandTestStore) SetSetting(context.Context, string, string) error              { return nil }
func (commandTestStore) Snapshot(context.Context, string) (*config.Snapshot, error)    { return nil, nil }
func (commandTestStore) SeedDefaults(context.Context) error                            { return nil }

type commandTestMemory struct{}

func (commandTestMemory) Name() string                                                { return "test" }
func (commandTestMemory) Bootstrap(context.Context, memory.Session) error             { return nil }
func (commandTestMemory) Append(context.Context, memory.Session, ...ai.Message) error { return nil }
func (commandTestMemory) Assemble(context.Context, memory.Session, int, int) ([]ai.Message, error) {
	return nil, nil
}

func (commandTestMemory) Stats(context.Context, memory.Session) (memory.SessionStats, error) {
	return memory.SessionStats{}, nil
}
func (commandTestMemory) Close() error { return nil }

func setupCommandTestAnnaHome(t *testing.T) string {
	t.Helper()
	annaHome := t.TempDir()
	binDir := filepath.Join(annaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	_ = embedded.EnsureTools(annaHome)
	boxshPath := filepath.Join(binDir, "boxsh")
	_ = os.Remove(boxshPath)
	boxshStub := `#!/bin/bash
if [[ "$1" == "--version" ]]; then
	echo boxsh 2.0.1
	exit 0
fi

while read -r line; do
	id=$(echo "$line" | grep -o '"id":[0-9]*' | cut -d: -f2)
	if [[ "$line" == *'"method":"initialize"'* ]]; then
		echo "{\"jsonrpc\":\"2.0\",\"result\":{\"serverInfo\":{\"name\":\"boxsh\",\"version\":\"2.0.1\"},\"protocolVersion\":\"2024-11-05\"},\"id\":$id}"
	elif [[ "$line" == *'"method":"tools/call"'* ]]; then
		echo "{\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}],\"structuredContent\":{\"stdout\":\"ok\",\"stderr\":\"\",\"exit_code\":0}},\"id\":$id}"
	fi
done
`
	if err := os.WriteFile(boxshPath, []byte(boxshStub), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("ANNA_HOME", annaHome)
	config.ResetAnnaHome()
	t.Cleanup(config.ResetAnnaHome)
	return annaHome
}

func TestNewRunnerFactoryGo(t *testing.T) {
	setupCommandTestAnnaHome(t)
	snap := &config.Snapshot{
		Provider: "anthropic",
		Model:    "test-model",
		APIKey:   "test-key",
		Runner:   config.RunnerConfig{Type: "go"},
	}
	snap.Workspace = t.TempDir()

	factory, err := agent.NewRunnerFactory(snap, nil, nil, testProviderRegistryBuilder, nil, nil, nil)
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

	_, err := agent.NewRunnerFactory(snap, nil, nil, testProviderRegistryBuilder, nil, nil, nil)
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

func TestModelSwitcherPreservesPromptBuilders(t *testing.T) {
	setupCommandTestAnnaHome(t)

	snap := &config.Snapshot{
		Provider: "anthropic",
		Model:    "anthropic/old-model",
		APIKey:   "test-key",
		Runner:   config.RunnerConfig{Type: "go"},
	}
	snap.Workspace = t.TempDir()

	initialFactory, err := agent.NewRunnerFactory(snap, nil, nil, testProviderRegistryBuilder, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewRunnerFactory: %v", err)
	}

	pool := agent.NewPool(initialFactory, commandTestMemory{}, agent.WithDefaultModel(snap.Model))

	promptToolsCalls := 0
	promptSectionsCalls := 0
	switchFn := modelSwitcher(
		snap,
		commandTestStore{},
		pool,
		nil,
		nil,
		testProviderRegistryBuilder,
		func(context.Context) ([]pkgplugins.PromptToolInfo, error) {
			promptToolsCalls++
			return nil, nil
		},
		func(context.Context, pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error) {
			promptSectionsCalls++
			return nil, nil
		},
		&coreagent.ToolLifecycle{},
	)

	if err := switchFn("anthropic", "new-model"); err != nil {
		t.Fatalf("switchFn: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for range pool.Chat(ctx, "session-switch", "hello") {
	}

	if promptToolsCalls == 0 {
		t.Fatal("expected prompt tools builder to be preserved after model switch")
	}
	if promptSectionsCalls == 0 {
		t.Fatal("expected prompt sections builder to be preserved after model switch")
	}
}
