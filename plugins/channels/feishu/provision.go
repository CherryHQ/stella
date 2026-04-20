package feishu

import (
	"context"
	"log/slog"
	"time"

	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
)

const provisionCacheTTL = time.Hour

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

// maybeAutoProvision provisions an Anna user for the sender if auto-provisioning
// is enabled and the sender belongs to the configured tenant.
//
// tenantKey is the sender's tenant key from the event (empty string if unavailable,
// e.g. in reaction events). When non-empty it is checked against cfg.TenantKey
// before any API call; when empty the contact API failure acts as an implicit filter.
//
// It is called after dedup+group-eligibility checks in onMessage/onReaction.
// On any error it logs and returns silently — provisioning failure must never
// block the normal message flow.
//
// Cache key is union_id so users messaging from multiple devices share one entry.
func (b *Bot) maybeAutoProvision(ctx context.Context, openID, unionID, tenantKey string) {
	if !b.cfg.AutoProvision || b.cfg.TenantKey == "" {
		return
	}

	// Explicit tenant check when the event carries a tenant_key.
	if tenantKey != "" && tenantKey != b.cfg.TenantKey {
		logger().Debug("auto-provision: skipping external tenant user", "tenant_key", tenantKey)
		return
	}

	provisioner, ok := b.handler.(pkgchannel.Provisioner)
	if !ok {
		return
	}

	// Check cache using union_id when available, falling back to open_id.
	cacheKey := unionID
	if cacheKey == "" {
		cacheKey = openID
	}

	b.provisionedMu.Lock()
	if t, ok := b.provisioned[cacheKey]; ok && time.Since(t) < provisionCacheTTL {
		b.provisionedMu.Unlock()
		return
	}
	b.provisionedMu.Unlock()

	// Fetch profile from contact API for email hint and authoritative union_id.
	// This also acts as an implicit tenant filter: the API rejects external users.
	profile := b.fetchTenantProfile(ctx, openID)
	if profile == nil {
		return
	}

	// Use union_id from profile as the canonical external ID.
	if profile.UnionID != "" {
		cacheKey = profile.UnionID
		unionID = profile.UnionID
	}

	// Re-check cache with the authoritative union_id.
	b.provisionedMu.Lock()
	if t, ok := b.provisioned[cacheKey]; ok && time.Since(t) < provisionCacheTTL {
		b.provisionedMu.Unlock()
		return
	}
	b.provisionedMu.Unlock()

	if err := provisioner.ProvisionUser(ctx, pkgchannel.ProvisionRequest{
		Platform:   pkgchannel.PlatformFeishu,
		ExternalID: unionID,
		Name:       profile.Name,
		EmailHint:  profile.Email,
	}); err != nil {
		slog.Debug("auto-provision failed", "open_id", openID, "error", err)
		return
	}

	b.provisionedMu.Lock()
	b.provisioned[cacheKey] = time.Now()
	b.provisionedMu.Unlock()
}
