package main

import (
	"context"
	"strings"
	"testing"

	"github.com/vaayne/anna/internal/config"
)

func TestHasEnabledNotifyChannel(t *testing.T) {
	falseValue := false
	trueValue := true

	cfg := &config.Config{
		Channels: config.ChannelsConfig{
			Telegram: config.TelegramConfig{
				Enabled:      &falseValue,
				EnableNotify: &trueValue,
				Token:        "test-token",
			},
		},
	}

	if hasEnabledNotifyChannel(cfg) {
		t.Fatal("hasEnabledNotifyChannel() = true, want false for disabled channel")
	}

	cfg.Channels.Telegram.Enabled = &trueValue
	if !hasEnabledNotifyChannel(cfg) {
		t.Fatal("hasEnabledNotifyChannel() = false, want true for enabled notify channel")
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

func TestNewRunnerFactoryGo(t *testing.T) {
	cfg := &config.Config{
		Provider:  "anthropic",
		Model:     "test-model",
		Workspace: t.TempDir(),
		Runner:    config.RunnerConfig{Type: "go"},
		Providers: map[string]config.ProviderConfig{
			"anthropic": {APIKey: "test-key"},
		},
	}

	factory, err := newRunnerFactory(cfg, nil, nil)
	if err != nil {
		t.Fatalf("newRunnerFactory: %v", err)
	}

	r, err := factory(context.Background(), "")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}

	if r == nil {
		t.Fatal("expected non-nil runner")
	}
}

func TestNewRunnerFactoryUnknown(t *testing.T) {
	cfg := &config.Config{
		Runner: config.RunnerConfig{Type: "invalid"},
	}

	_, err := newRunnerFactory(cfg, nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown runner type")
	}
	if !strings.Contains(err.Error(), "unknown runner type") {
		t.Errorf("error = %q, want contains 'unknown runner type'", err.Error())
	}
}

func TestRunGatewayNoServices(t *testing.T) {
	t.Setenv("ANNA_TELEGRAM_TOKEN", "")
	t.Setenv("ANNA_HOME", t.TempDir())
	config.ResetAnnaHome()
	t.Cleanup(config.ResetAnnaHome)
	app := newApp()
	err := app.Run([]string{"anna", "gateway"})
	if err == nil {
		t.Fatal("expected error for no configured services")
	}
	if !strings.Contains(err.Error(), "no gateway services configured") {
		t.Errorf("err = %q, want contains 'no gateway services configured'", err.Error())
	}
}
