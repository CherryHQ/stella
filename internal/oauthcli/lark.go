package oauthcli

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

const (
	larkBaseFeishu = "https://open.feishu.cn"
	larkBaseLark   = "https://open.larksuite.com"

	larkRedirectURI = "https://anna.app/oauth/lark/callback" // placeholder; caller sets up the real redirect
)

// larkScopes covers common Feishu/Lark daily-use scenarios: IM read/send,
// calendar, all document types (docs, docx, wiki, sheets, slides), tasks,
// and file access. offline_access is required for refresh tokens.
var larkScopes = []string{
	"offline_access",
	// Identity
	"contact:user.base:readonly",
	// IM — read and send messages as user, read chats
	"im:message",
	"im:chat:readonly",
	// Calendar — read events and free/busy
	"calendar:calendar:readonly",
	// Drive — download files and read metadata
	"drive:file:download",
	"drive:drive.metadata:readonly",
	// Docs (legacy doc format) — read content, comments, export
	"docs:document.content:read",
	"docs:document.comment:read",
	"docs:document.comment:create",
	"docs:document:export",
	// Docx (new document format) — full read/write
	"docx:document",
	// Wiki — full read/write
	"wiki:wiki",
	// Sheets — full read/write
	"sheets:spreadsheet",
	// Slides — read and update presentations
	"slides:presentation:read",
	"slides:presentation:update",
	// Tasks — full read/write with comments
	"task:task",
	"task:comment:read",
	"task:comment:write",
}

// LarkConfig holds the OAuth app credentials for Lark/Feishu device-style flow.
type LarkConfig struct {
	AppID     string
	AppSecret string
	Brand     string // "lark" or "feishu"
}

// LarkBroker manages Lark/Feishu OAuth sessions via the authorization-code
// flow adapted for device-like use: StartDeviceFlow returns a URL the user
// visits; Poll checks completion; Complete exchanges the code and saves the
// bundle to vault.
type LarkBroker struct {
	cfg         LarkConfig
	store       *FlowStore
	redirectURI string
}

// NewLarkBroker constructs a LarkBroker. redirectURI is the OAuth callback URL
// that your HTTP handler will receive and then call Complete on.
func NewLarkBroker(cfg LarkConfig, store *FlowStore) *LarkBroker {
	return &LarkBroker{cfg: cfg, store: store, redirectURI: larkRedirectURI}
}

// WithRedirectURI returns a new broker with the same configuration but the
// given redirect URI.
func (b *LarkBroker) WithRedirectURI(uri string) *LarkBroker {
	return &LarkBroker{cfg: b.cfg, store: b.store, redirectURI: uri}
}

func larkBaseURL(brand string) string {
	if brand == "feishu" {
		return larkBaseFeishu
	}
	return larkBaseLark
}

// oauthConfig returns an oauth2.Config for Lark's v2 token endpoint, which
// follows standard OAuth2: form-encoded requests with client credentials in
// the body, flat JSON responses. No app access token pre-fetch is required.
func (b *LarkBroker) oauthConfig() *oauth2.Config {
	base := larkBaseURL(b.cfg.Brand)
	return &oauth2.Config{
		ClientID:     b.cfg.AppID,
		ClientSecret: b.cfg.AppSecret,
		RedirectURL:  b.redirectURI,
		Scopes:       larkScopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:   base + "/open-apis/authen/v1/authorize",
			TokenURL:  base + "/open-apis/authen/v2/oauth/token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

// StartDeviceFlow generates a state token, constructs the authorization URL,
// and stores a pending FlowStatus. The user must navigate to VerificationURI.
func (b *LarkBroker) StartDeviceFlow(ctx context.Context, userID int64) (FlowStatus, error) {
	flowID := uuid.NewString()
	status := FlowStatus{
		Provider:        ProviderLark,
		FlowID:          flowID,
		VerificationURI: b.oauthConfig().AuthCodeURL(flowID),
		ExpiresAt:       time.Now().Add(10 * time.Minute),
		State:           FlowStatePending,
	}
	b.store.Create(status)
	return status, nil
}

// Poll checks whether the flow identified by flowID has been completed.
// Completion is signaled externally via Complete after the OAuth callback is received.
func (b *LarkBroker) Poll(ctx context.Context, flowID string) (FlowStatus, error) {
	status, ok := b.store.Get(flowID)
	if !ok {
		return FlowStatus{}, fmt.Errorf("oauthcli/lark: unknown flow %q", flowID)
	}
	if status.State != FlowStatePending {
		return status, nil
	}
	if time.Now().After(status.ExpiresAt) {
		b.store.Update(flowID, FlowStateExpired, nil)
		status.State = FlowStateExpired
	}
	return status, nil
}

// Complete exchanges an authorization code for tokens, saves the bundle to
// vault, and marks the flow as authorized. The code comes from your OAuth
// callback handler's query parameter.
func (b *LarkBroker) Complete(ctx context.Context, vs VaultStore, userID int64, flowID string, code string) error {
	if _, ok := b.store.Get(flowID); !ok {
		return fmt.Errorf("oauthcli/lark: unknown flow %q", flowID)
	}

	tok, err := b.oauthConfig().Exchange(ctx, code)
	if err != nil {
		return fmt.Errorf("oauthcli/lark: exchange code: %w", err)
	}

	bundle := larkBundleFromToken(tok, b.cfg.AppID, b.cfg.AppSecret, b.cfg.Brand)
	bundle.Version = 1
	if err := SaveLarkBundle(ctx, vs, userID, bundle); err != nil {
		return err
	}

	b.store.Update(flowID, FlowStateAuthorized, nil)
	b.store.Delete(flowID)
	return nil
}

// larkBundleFromToken builds a LarkOAuthBundle from an oauth2.Token returned
// by Exchange or TokenSource. Version is left at zero; callers must set it.
// refresh_token_expires_in is a non-standard Lark field preserved as a token
// extra so callers can read it via tok.Extra("refresh_token_expires_in").
func larkBundleFromToken(tok *oauth2.Token, appID, appSecret, brand string) LarkOAuthBundle {
	bundle := LarkOAuthBundle{
		AppID:           appID,
		AppSecret:       appSecret,
		Brand:           brand,
		AccessToken:     tok.AccessToken,
		RefreshToken:    tok.RefreshToken,
		AccessExpiresAt: tok.Expiry,
	}
	if ri, ok := tok.Extra("refresh_token_expires_in").(float64); ok && ri > 0 {
		bundle.RefreshExpiresAt = time.Now().Add(time.Duration(ri) * time.Second)
	}
	return bundle
}
