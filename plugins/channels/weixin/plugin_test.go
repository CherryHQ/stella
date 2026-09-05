package weixin

import (
	"context"
	"log/slog"
	"testing"

	pluginhost "github.com/CherryHQ/stella/internal/plugin/host"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestPluginRuntimeFactoryPassesBuildVersionToChannel(t *testing.T) {
	services := pluginhost.NewChannelRuntimeServices()
	notifier := newFakeNotificationRegistry()
	services.Set(context.Background(), fakeChannelHandler{}, notifier, nil)
	services.SetBuildVersion("1.2.3")

	runtime, err := newRuntime(pkgplugins.RuntimeContext{Platform: pluginTestPlatform{channel: services}})
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	if err := runtime.Apply(context.Background(), pkgplugins.PluginState{
		ID:      PluginID,
		Enabled: true,
		Config: map[string]any{
			"bot_token": "wx-token",
			"base_url":  "http://127.0.0.1:1",
		},
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	defer func() { _ = runtime.Stop(context.Background()) }()

	bot, ok := notifier.channel.(*Bot)
	if !ok {
		t.Fatalf("registered channel = %T, want *Bot", notifier.channel)
	}
	if bot.cfg.Version != "1.2.3" {
		t.Fatalf("bot version = %q, want %q", bot.cfg.Version, "1.2.3")
	}
}

type pluginTestPlatform struct {
	channel pkgplugins.ChannelPlatform
}

func (pluginTestPlatform) Logger() *slog.Logger                          { return slog.Default() }
func (pluginTestPlatform) ConfigStore() pkgplugins.ConfigStore           { return nil }
func (pluginTestPlatform) StateStore() pkgplugins.StateStore             { return nil }
func (pluginTestPlatform) Notifier() pkgplugins.Notifier                 { return nil }
func (pluginTestPlatform) Auth() pkgplugins.Auth                         { return nil }
func (pluginTestPlatform) RuntimeLookup() pkgplugins.RuntimeLookup       { return nil }
func (p pluginTestPlatform) ChannelPlatform() pkgplugins.ChannelPlatform { return p.channel }

func (pluginTestPlatform) AccountEnrollment() pkgchannel.AccountEnroller { return nil }
