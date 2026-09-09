package channel

import (
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func TestReplyCapabilityRequiresMatchingChannelKindAndLiveExpiry(t *testing.T) {
	fx := newDispatcherFixture(t, "web", `{}`)
	ctx := t.Context()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.NewService(fx.q, identity.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "synthetic-recipient-context-token"
	ciphertext, err := v.EncryptSystem(secret)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ciphertext, secret) {
		t.Fatal("capability was not encrypted")
	}
	id := uuid.NewString()
	if _, err := fx.q.CreateChannelReplyCapability(ctx, sqlc.CreateChannelReplyCapabilityParams{
		ID: id, ChannelID: "ch-1", Kind: "weixin_context_token", Ciphertext: ciphertext,
		ExpiresAt: time.Now().UTC().Add(time.Hour), FifoItemID: "",
	}); err != nil {
		t.Fatal(err)
	}
	resolver := NewDurableReplyCapabilityResolver(fx.db, v)
	got, err := resolver.Resolve(ctx, id, "ch-1", "weixin_context_token")
	if err != nil || got.Secret != secret {
		t.Fatalf("valid capability lookup failed: %v", err)
	}
	for _, tc := range []struct{ channel, kind string }{
		{"another-channel", "weixin_context_token"},
		{"ch-1", "dingtalk_webhook"},
	} {
		if got, err := resolver.Resolve(ctx, id, tc.channel, tc.kind); err == nil || got.Secret != "" {
			t.Fatal("mismatched capability exposed a secret")
		}
	}
	if _, err := fx.db.Exec(ctx, `UPDATE channel_reply_capability SET created_at=clock_timestamp()-interval '2 seconds', expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if got, err := resolver.Resolve(ctx, id, "ch-1", "weixin_context_token"); err == nil || got.Secret != "" {
		t.Fatal("expired capability exposed a secret")
	}
	if _, err := fx.q.SweepExpiredChannelReplyCapabilities(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := fx.db.QueryRow(ctx, `SELECT count(*) FROM channel_reply_capability WHERE id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired ciphertext remains after sweep: count=%d err=%v", count, err)
	}
}
