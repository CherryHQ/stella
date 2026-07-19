package feishu

import (
	"context"
	"fmt"
	"time"

	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

const provisionCacheTTL = time.Hour

// isAutoProvisionMessage restricts group enrollment to users who directly
// address this bot. The later semantic-group decision is asynchronous and must
// not be used as an admission signal.
func (b *Bot) isAutoProvisionMessage(chatType string, mentions []*larkim.MentionEvent) bool {
	if chatType != "group" {
		return true
	}
	botOpenID, _ := b.botOpenID.Load().(string)
	if botOpenID == "" {
		return false
	}
	for _, mention := range mentions {
		if mention != nil && mention.Id != nil && derefStr(mention.Id.OpenId) == botOpenID {
			return true
		}
	}
	return false
}

func (b *Bot) isCachedProvision(key string) bool {
	b.provisionedMu.Lock()
	defer b.provisionedMu.Unlock()
	t, ok := b.provisioned[key]
	return ok && time.Since(t) < provisionCacheTTL
}

// effectiveTenantKey returns the configured tenant key, or the one auto-detected
// at startup from the Feishu tenant API.
func (b *Bot) effectiveTenantKey() string {
	if b.cfg.TenantKey != "" {
		return b.cfg.TenantKey
	}
	b.learnedTenantKeyMu.RLock()
	defer b.learnedTenantKeyMu.RUnlock()
	return b.learnedTenantKey
}

// fetchBotTenantKey queries the Feishu tenant API at startup to auto-detect the
// bot's home tenant key. Called only when auto_provision=true and tenant_key is
// not explicitly configured.
func (b *Bot) fetchBotTenantKey(ctx context.Context) error {
	if b.client == nil {
		return fmt.Errorf("client not initialised")
	}
	resp, err := b.client.Tenant.Tenant.Query(ctx)
	if err != nil {
		return fmt.Errorf("tenant query: %w", err)
	}
	if !resp.Success() || resp.Data == nil || resp.Data.Tenant == nil || resp.Data.Tenant.TenantKey == nil {
		return fmt.Errorf("tenant query: unexpected response (code=%d)", resp.Code)
	}
	key := *resp.Data.Tenant.TenantKey
	b.learnedTenantKeyMu.Lock()
	b.learnedTenantKey = key
	b.learnedTenantKeyMu.Unlock()
	logger().Info("auto-provision: detected tenant_key from Feishu API", "tenant_key", key)
	return nil
}

// TenantProfile holds the information fetched from the Feishu contact API.
type TenantProfile struct {
	UnionID string
	OpenID  string
	Name    string
	Email   string
}

// fetchTenantProfile calls contact.v3.user.get with open_id to get profile info.
// Returns nil if the API call fails or returns no data.
func (b *Bot) fetchTenantProfile(ctx context.Context, openID string) *TenantProfile {
	if b.fetchTenantProfileFn != nil {
		return b.fetchTenantProfileFn(ctx, openID)
	}
	if b.client == nil {
		return nil
	}
	resp, err := b.client.Contact.User.Get(ctx,
		larkcontact.NewGetUserReqBuilder().
			UserId(openID).
			UserIdType(larkcontact.UserIdTypeGetUserOpenId).
			Build())
	if err != nil {
		logger().Debug("contact api: get user failed", "open_id", openID, "error", err)
		return nil
	}
	if !resp.Success() || resp.Data == nil || resp.Data.User == nil {
		logger().Debug("contact api: get user unsuccessful", "open_id", openID, "code", resp.Code)
		return nil
	}

	u := resp.Data.User
	profile := &TenantProfile{
		OpenID: openID,
	}
	if u.UnionId != nil {
		profile.UnionID = *u.UnionId
	}
	if u.Name != nil {
		profile.Name = *u.Name
	}
	if u.Email != nil {
		profile.Email = *u.Email
	}
	return profile
}

// maybeAutoProvision provisions an Stella user for the sender if auto-provisioning
// is enabled and the sender belongs to the configured tenant.
//
// tenantKey must be non-empty evidence from a normal message event and match
// the bot's effective tenant key. Contact API readability is not tenant proof.
// On any failure it logs and returns silently — provisioning failure never
// blocks normal message handling.
func (b *Bot) maybeAutoProvision(ctx context.Context, openID, eventUnionID, tenantKey string) {
	if !b.cfg.AutoProvision {
		return
	}
	effective := b.effectiveTenantKey()
	if effective == "" || tenantKey == "" {
		return
	}
	if tenantKey != effective {
		logger().Debug("auto-provision: skipping external tenant user", "tenant_key", tenantKey)
		return
	}

	provisioner, ok := b.handler.(pkgchannel.Provisioner)
	if !ok {
		return
	}
	// A cached key was recorded only after a Contact-API-derived canonical
	// union_id enrolled successfully. The event union_id is safe to use only to
	// avoid that repeat lookup; it is never persisted without profile evidence.
	if eventUnionID != "" && b.isCachedProvision(eventUnionID) {
		return
	}

	// The Contact API profile supplies the canonical union_id and enrollment
	// attributes. Never trust the event union_id for durable identity writes.
	profile := b.fetchTenantProfile(ctx, openID)
	if profile == nil || profile.UnionID == "" {
		if profile != nil {
			logger().Warn("auto-provision: skipping user with empty union_id", "open_id", openID)
		}
		return
	}
	if b.isCachedProvision(profile.UnionID) {
		return
	}

	if err := provisioner.ProvisionUser(ctx, pkgchannel.ProvisionRequest{
		Platform:   pkgchannel.PlatformFeishu,
		ExternalID: profile.UnionID,
		TenantKey:  tenantKey,
		Email:      profile.Email,
		Name:       profile.Name,
	}); err != nil {
		logger().Debug("auto-provision failed", "open_id", openID, "error", err)
		return
	}

	// Cache only a successful, canonically identified enrollment.
	b.provisionedMu.Lock()
	b.provisioned[profile.UnionID] = time.Now()
	b.provisionedMu.Unlock()
}
