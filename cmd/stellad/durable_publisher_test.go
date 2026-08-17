package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/jackc/pgx/v5/pgtype"

	internalchannel "github.com/CherryHQ/stella/internal/channel"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	storepkg "github.com/CherryHQ/stella/internal/store"
	"github.com/CherryHQ/stella/internal/vault"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// These tests exercise the stellad composition root directly. In particular,
// no listener is built and no publisher is installed in a process-local
// registry before reconstruction.
func TestDurablePublisherReconstructorBuildsEveryGroupChannel(t *testing.T) {
	tests := []struct {
		name, typ, config string
	}{
		{"telegram", pkgchannel.PlatformTelegram, `{"token":"123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi","allow_group":true}`},
		{"discord", pkgchannel.PlatformDiscord, `{"token":"durable-token","allow_group":true,"allow_all_guilds":true}`},
		{"feishu", pkgchannel.PlatformFeishu, `{"app_id":"cli_test","app_secret":"secret","allow_group":true}`},
		{"qq", pkgchannel.PlatformQQ, `{"app_id":"app","app_secret":"secret"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			publisher, err := newDurablePublisherReconstructor(nil).ReconstructGroupPublisher(context.Background(), config.Channel{
				ID: "channel-" + tt.name, Type: tt.typ, Enabled: true, Config: tt.config,
			}, internalchannel.GroupOutboxEnvelope{})
			if err != nil {
				t.Fatalf("reconstruct %s publisher: %v", tt.name, err)
			}
			if publisher == nil {
				t.Fatalf("reconstruct %s publisher returned nil", tt.name)
			}
		})
	}
}

func TestDurablePublisherReconstructorUsesCurrentConfigFailClosed(t *testing.T) {
	r := newDurablePublisherReconstructor(nil)
	tests := []struct {
		name string
		ch   config.Channel
		want string
	}{
		{"disabled after enqueue", config.Channel{ID: "changed", Type: pkgchannel.PlatformDiscord, Enabled: false, Config: `{"token":"old"}`}, "disabled"},
		{"credentials removed after enqueue", config.Channel{ID: "changed", Type: pkgchannel.PlatformDiscord, Enabled: true, Config: `{}`}, "token is required"},
		{"type changed after enqueue", config.Channel{ID: "changed", Type: "deleted-plugin", Enabled: true, Config: `{}`}, "no durable group publisher"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := r.ReconstructGroupPublisher(context.Background(), tt.ch, internalchannel.GroupOutboxEnvelope{}); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestDingTalkDurableCapabilityEnforcesEncryptedOwnershipKindAndExpiry(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate vault identity: %v", err)
	}
	vaultSvc, err := vault.NewServiceForPool(db, identity.String(), nil)
	if err != nil {
		t.Fatalf("new vault service: %v", err)
	}
	q := sqlc.New(db)
	for _, id := range []string{"dingtalk-owner", "dingtalk-other"} {
		if _, err := q.CreateChannel(ctx, sqlc.CreateChannelParams{ID: id, Name: id, Type: pkgchannel.PlatformDingTalk, AgentID: pgtype.Text{}, Enabled: true, Config: `{}`}); err != nil {
			t.Fatalf("create channel %s: %v", id, err)
		}
	}
	insert := func(id, kind string, expires time.Time) error {
		t.Helper()
		ciphertext, err := vaultSvc.EncryptSystem("https://example.com/dingtalk/session")
		if err != nil {
			t.Fatalf("encrypt capability: %v", err)
		}
		_, err = q.CreateChannelReplyCapability(ctx, sqlc.CreateChannelReplyCapabilityParams{ID: id, ChannelID: "dingtalk-owner", Kind: kind, Ciphertext: ciphertext, ExpiresAt: expires})
		return err
	}
	const (
		liveRef      = "11111111-1111-1111-1111-111111111111"
		wrongKindRef = "22222222-2222-2222-2222-222222222222"
		expiredRef   = "33333333-3333-3333-3333-333333333333"
	)
	if err := insert(liveRef, "dingtalk_session_webhook", time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("create live capability: %v", err)
	}
	if err := insert(wrongKindRef, "another_kind", time.Now().UTC().Add(time.Hour)); err == nil {
		t.Fatal("persisting a capability of an unsupported type succeeded")
	}
	if err := insert(expiredRef, "dingtalk_session_webhook", time.Now().UTC().Add(100*time.Millisecond)); err != nil {
		t.Fatalf("create expired capability: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	r := newDurablePublisherReconstructor(internalchannel.NewDurableReplyCapabilityResolver(db, vaultSvc))
	channel := config.Channel{ID: "dingtalk-owner", Type: pkgchannel.PlatformDingTalk, Enabled: true, Config: `{}`}
	if publisher, err := r.ReconstructGroupPublisher(ctx, channel, internalchannel.GroupOutboxEnvelope{ReplyCapabilityRef: liveRef}); err != nil || publisher == nil {
		t.Fatalf("reconstruct live encrypted capability: publisher=%v err=%v", publisher, err)
	}
	for _, tc := range []struct {
		name, ref, channelID, want string
	}{
		{"channel ownership", liveRef, "dingtalk-other", "resolve DingTalk reply capability"},
		{"expiry", expiredRef, "dingtalk-owner", "resolve DingTalk reply capability"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel.ID = tc.channelID
			if _, err := r.ReconstructGroupPublisher(ctx, channel, internalchannel.GroupOutboxEnvelope{ReplyCapabilityRef: tc.ref}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestNonLeaderDurablePublisherReconstructsFromDatabaseAndPublishesWithoutRegistry(t *testing.T) {
	requestBody := make(chan string, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestBody <- string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer server.Close()
	oldTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })

	ctx := t.Context()
	db := dbtest.New(t)
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	vaultSvc, err := vault.NewServiceForPool(db, identity.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(db)
	const (
		channelID    = "dingtalk-non-leader"
		capabilityID = "44444444-4444-4444-4444-444444444444"
	)
	if _, err := q.CreateChannel(ctx, sqlc.CreateChannelParams{
		ID: channelID, Name: channelID, Type: pkgchannel.PlatformDingTalk,
		Enabled: true, Config: `{}`,
	}); err != nil {
		t.Fatalf("persist channel config: %v", err)
	}
	ciphertext, err := vaultSvc.EncryptSystem(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateChannelReplyCapability(ctx, sqlc.CreateChannelReplyCapabilityParams{
		ID: capabilityID, ChannelID: channelID, Kind: "dingtalk_session_webhook",
		Ciphertext: ciphertext, ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("persist encrypted reply capability: %v", err)
	}
	configured, err := storepkg.NewDBStore(db).GetChannel(ctx, channelID)
	if err != nil {
		t.Fatalf("reload durable channel config: %v", err)
	}
	// This executor has no listener and never creates or populates a
	// PublisherRegistry: all egress state comes from PostgreSQL.
	publisher, err := newDurablePublisherReconstructor(
		internalchannel.NewDurableReplyCapabilityResolver(db, vaultSvc),
	).ReconstructGroupPublisher(ctx, configured, internalchannel.GroupOutboxEnvelope{ReplyCapabilityRef: capabilityID})
	if err != nil {
		t.Fatalf("reconstruct publisher on non-leader: %v", err)
	}
	events := make(chan pkgchannel.Event, 1)
	events <- pkgchannel.Event{Text: "durable hello"}
	close(events)
	if err := publisher.Publish(ctx, internalchannel.GroupPublishRequest{
		Platform: pkgchannel.PlatformDingTalk,
		Stream:   &pkgchannel.ChatStream{Events: events},
	}); err != nil {
		t.Fatalf("publish from reconstructed non-leader client: %v", err)
	}
	select {
	case body := <-requestBody:
		if !strings.Contains(body, "durable hello") {
			t.Fatalf("outbound request body = %s", body)
		}
	default:
		t.Fatal("concrete reconstructed publisher sent no outbound request")
	}
}
