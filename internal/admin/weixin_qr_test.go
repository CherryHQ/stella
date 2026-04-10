package admin

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/notify"
	"github.com/vaayne/anna/internal/pluginhost"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	weixinplugin "github.com/vaayne/anna/plugins/channels/weixin"
	lcmmemory "github.com/vaayne/anna/plugins/memory/lcm"
)

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
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	store := config.NewDBStore(db)
	if err := store.SeedDefaults(context.Background()); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}

	as := appdb.NewAuthStore(db)
	engine, err := auth.NewEngine(context.Background(), as)
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
	phost := pluginhost.New(store, pluginhost.WithChannelRuntimeServices(channelRuntimeServices))
	if err := phost.LoadDefaultCatalog(); err != nil {
		t.Fatalf("LoadDefaultCatalog: %v", err)
	}
	srv := New(store, as, engine, mem, db, auth.NewLinkCodeStore(), nil, phost)

	status := &weixinplugin.QRCodeStatusResponse{
		Status:      "confirmed",
		BotToken:    "wx-token",
		BaseURL:     "https://wx.example",
		ILinkBotID:  "bot-1",
		ILinkUserID: "user-1",
	}
	if err := srv.saveWeixinCredentials(context.Background(), status); err != nil {
		t.Fatalf("saveWeixinCredentials: %v", err)
	}

	plugin, err := store.GetPlugin(context.Background(), config.PluginID(config.PluginKindChannel, pkgchannel.PlatformWeixin))
	if err != nil {
		t.Fatalf("GetPlugin: %v", err)
	}
	if plugin.Config["bot_token"] != "wx-token" {
		t.Fatalf("bot_token = %#v, want %q", plugin.Config["bot_token"], "wx-token")
	}

	runtimeStatus, err := phost.Status(context.Background(), config.PluginID(config.PluginKindChannel, pkgchannel.PlatformWeixin))
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
