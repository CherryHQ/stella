package server

import (
	"context"
	"fmt"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	lcmmemory "github.com/CherryHQ/stella/internal/memory/lcm"
	"github.com/CherryHQ/stella/internal/notify"
	"github.com/CherryHQ/stella/internal/pluginhost"
	cfgstore "github.com/CherryHQ/stella/internal/store"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	weixinplugin "github.com/CherryHQ/stella/plugins/channels/weixin"
)

type weixinNoopChannel struct{}

func (c *weixinNoopChannel) Name() string                                              { return "weixin" }
func (c *weixinNoopChannel) Start(ctx context.Context) error                           { <-ctx.Done(); return ctx.Err() }
func (c *weixinNoopChannel) Stop()                                                     {}
func (c *weixinNoopChannel) Notify(_ context.Context, _ pkgchannel.Notification) error { return nil }

type testWeixinHandler struct{}

func (testWeixinHandler) HandleIncoming(ctx context.Context, msg pkgchannel.IncomingMessage, command, args string) (string, bool, *pkgchannel.ChatStream, error) {
	return "", false, nil, nil
}
func (testWeixinHandler) ListModels() []pkgchannel.ModelOption     { return nil }
func (testWeixinHandler) SwitchModel(provider, model string) error { return nil }
func (testWeixinHandler) ListAgents(ctx context.Context, msg pkgchannel.IncomingMessage) ([]pkgchannel.AgentInfo, string, error) {
	return nil, "", nil
}

func (testWeixinHandler) SwitchAgent(ctx context.Context, msg pkgchannel.IncomingMessage, agentSlug string) error {
	return nil
}

func TestSaveWeixinCredentialsUsesPluginHost(t *testing.T) {
	db := dbtest.New(t)

	store := cfgstore.NewDBStore(db)
	ctx := context.Background()
	if err := store.Seed(ctx); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	as := appdb.NewAuthStore(db)
	engine, err := auth.NewEngine(ctx, as)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	mem, err := lcmmemory.New(db, nil, nil)
	if err != nil {
		t.Fatalf("Build lcm provider: %v", err)
	}
	dispatcher := notify.NewDispatcher()
	channelRuntimeServices := pluginhost.NewChannelRuntimeServices()
	channelRuntimeServices.Set(context.Background(), testWeixinHandler{}, dispatcher)
	t.Cleanup(auth.SetBcryptCostForTesting(bcrypt.MinCost))
	runtimeCtx, cancelRuntime := context.WithCancel(context.Background())
	t.Cleanup(cancelRuntime)
	resetWeixin := weixinplugin.SetRuntimeFactoryForTesting(func(pkgplugins.Platform) (pkgplugins.Runtime, error) {
		return weixinplugin.NewWeixinManagedRuntime(weixinplugin.WeixinRuntimeDeps{
			Parent:  runtimeCtx,
			Handler: testWeixinHandler{},
			NewChannel: func(_ pkgchannel.WeixinConfig, _ pkgchannel.Handler) (pkgchannel.Channel, error) {
				return &weixinNoopChannel{}, nil
			},
		}), nil
	})
	t.Cleanup(resetWeixin)
	phost := pluginhost.New(store, pluginhost.WithChannelRuntimeServices(channelRuntimeServices))
	if err := phost.LoadDefaultCatalog(); err != nil {
		t.Fatalf("LoadDefaultCatalog: %v", err)
	}
	srv := newTestServer(t, store, as, engine, mem, db, phost)

	status := &weixinplugin.QRCodeStatusResponse{
		Status:      "confirmed",
		BotToken:    "wx-token",
		BaseURL:     "https://wx.example",
		ILinkBotID:  "bot-1",
		ILinkUserID: "user-1",
	}
	if err := srv.saveWeixinCredentials(ctx, status); err != nil {
		t.Fatalf("saveWeixinCredentials: %v", err)
	}

	plugin, err := store.GetPlugin(ctx, config.PluginID(config.PluginKindChannel, pkgchannel.PlatformWeixin))
	if err != nil {
		t.Fatalf("GetPlugin: %v", err)
	}
	if plugin.Config["bot_token"] != "wx-token" {
		t.Fatalf("bot_token = %#v, want %q", plugin.Config["bot_token"], "wx-token")
	}

	runtimeStatus, err := phost.Status(ctx, config.PluginID(config.PluginKindChannel, pkgchannel.PlatformWeixin))
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	payload, ok := runtimeStatus.(map[string]any)
	if !ok {
		t.Fatalf("status payload type = %T, want map[string]any", runtimeStatus)
	}
	if got := fmt.Sprint(payload["state"]); got != "running" {
		t.Fatalf("state = %#v, want %q", payload["state"], "running")
	}
}
