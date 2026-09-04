package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/oauth2"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

const AuthorizationCodeFlow = "authorization_code"

var ErrFlowNotFound = errors.New("oauth flow is unknown, expired, or already used")

// DurableFlowDB is the generated persistence surface used by the shared OAuth
// broker. Both connections and MCP inject their existing sqlc query set.
type DurableFlowDB interface {
	CreateOAuthFlow(ctx context.Context, arg sqlc.CreateOAuthFlowParams) (sqlc.OauthFlow, error)
	GetOAuthFlow(ctx context.Context, id string) (sqlc.OauthFlow, error)
	ClaimOAuthFlow(ctx context.Context, id string) (sqlc.OauthFlow, error)
	UpdateOAuthFlow(ctx context.Context, arg sqlc.UpdateOAuthFlowParams) error
	DeleteOAuthFlow(ctx context.Context, id string) error
}

// AuthCodeConfig is the restart-safe, non-secret configuration snapshot for an
// Authorization Code + PKCE flow.
type AuthCodeConfig struct {
	ClientID          string   `json:"client_id"`
	ClientSecretRef   string   `json:"client_secret_ref,omitempty"`
	AuthorizationURL  string   `json:"authorization_url"`
	TokenURL          string   `json:"token_url"`
	AuthStyle         int      `json:"auth_style,omitzero"`
	Resource          string   `json:"resource,omitempty"`
	Scopes            []string `json:"scopes,omitempty"`
	RedirectURI       string   `json:"redirect_uri"`
	StoreClientSecret bool     `json:"store_client_secret,omitzero"`
	Brand             string   `json:"brand,omitempty"`
}

// AuthCodeStart describes the domain adapter facts bound into one flow row.
type AuthCodeStart struct {
	ProviderKey string
	TargetKind  string
	TargetID    string
	ServerID    string
	UserID      string
	Owner       CredentialOwner
	BundleName  string
	Config      AuthCodeConfig
	TTL         time.Duration
}

// DurableFlow is the identity and state recovered from an untrusted callback's
// opaque state value.
type DurableFlow struct {
	ID          string
	ProviderKey string
	TargetKind  string
	TargetID    string
	ServerID    string
	UserID      string
	Owner       CredentialOwner
	BundleName  string
	FlowType    string
	State       FlowState
	Error       string
	ExpiresAt   time.Time
	Config      AuthCodeConfig
}

// DurableAuthCodeBroker is the sole Authorization Code + PKCE implementation
// for connections and MCP servers.
type DurableAuthCodeBroker struct {
	db     DurableFlowDB
	tokens *TokenManager
}

func NewDurableAuthCodeBroker(db DurableFlowDB, tokens *TokenManager) *DurableAuthCodeBroker {
	return &DurableAuthCodeBroker{db: db, tokens: tokens}
}

// Start persists the flow before returning its authorization URL.
func (b *DurableAuthCodeBroker) Start(ctx context.Context, in AuthCodeStart) (DurableFlow, string, error) {
	if b == nil || b.db == nil {
		return DurableFlow{}, "", fmt.Errorf("oauth: durable flow store not configured")
	}
	if in.TTL <= 0 {
		in.TTL = 10 * time.Minute
	}
	if in.ProviderKey == "" || in.TargetKind == "" || in.TargetID == "" || in.UserID == "" || in.BundleName == "" {
		return DurableFlow{}, "", fmt.Errorf("oauth: incomplete authorization flow identity")
	}
	if in.Config.ClientID == "" || in.Config.AuthorizationURL == "" || in.Config.TokenURL == "" || in.Config.RedirectURI == "" {
		return DurableFlow{}, "", fmt.Errorf("oauth: incomplete authorization flow config")
	}

	flowID := uuid.Must(uuid.NewV7()).String()
	verifier := oauth2.GenerateVerifier()
	expiresAt := time.Now().UTC().Add(in.TTL)
	raw, err := json.Marshal(in.Config)
	if err != nil {
		return DurableFlow{}, "", fmt.Errorf("oauth: encode authorization flow config: %w", err)
	}
	row, err := b.db.CreateOAuthFlow(ctx, sqlc.CreateOAuthFlowParams{
		ID: flowID, ServerID: nullableText(in.ServerID), UserID: in.UserID,
		CredentialScope: in.Owner.Scope, CredentialUserID: nullableText(in.Owner.UserID), CredentialAgentID: nullableText(in.Owner.AgentID),
		PkceVerifier: verifier, OauthConfig: raw, ExpiresAt: expiresAt,
		ProviderKey: in.ProviderKey, TargetKind: in.TargetKind, TargetID: in.TargetID, BundleName: in.BundleName,
		FlowType: AuthorizationCodeFlow, State: string(FlowStatePending), VerificationUri: "", UserCode: "", Error: "",
	})
	if err != nil {
		return DurableFlow{}, "", fmt.Errorf("oauth: persist authorization flow: %w", err)
	}
	flow, err := durableFlowFromRow(row)
	if err != nil {
		return DurableFlow{}, "", err
	}
	options := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	if in.Config.Resource != "" {
		options = append(options, oauth2.SetAuthURLParam("resource", in.Config.Resource))
	}
	authURL := (&oauth2.Config{
		ClientID: in.Config.ClientID, RedirectURL: in.Config.RedirectURI, Scopes: in.Config.Scopes,
		Endpoint: oauth2.Endpoint{AuthURL: in.Config.AuthorizationURL, TokenURL: in.Config.TokenURL, AuthStyle: oauth2.AuthStyle(in.Config.AuthStyle)},
	}).AuthCodeURL(flowID, options...)
	return flow, authURL, nil
}

// Get resolves callback state without consuming it. The caller uses only the
// persisted target kind/id to select its domain adapter.
func (b *DurableAuthCodeBroker) Get(ctx context.Context, id string) (DurableFlow, error) {
	if b == nil || b.db == nil {
		return DurableFlow{}, fmt.Errorf("oauth: durable flow store not configured")
	}
	row, err := b.db.GetOAuthFlow(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return DurableFlow{}, ErrFlowNotFound
	}
	if err != nil {
		return DurableFlow{}, fmt.Errorf("oauth: get flow: %w", err)
	}
	return durableFlowFromRow(row)
}

// Complete atomically consumes a flow, exchanges its code, and persists the
// resulting bundle through TokenManager.
func (b *DurableAuthCodeBroker) Complete(ctx context.Context, id, code, clientSecret string, client *http.Client) (DurableFlow, *OAuthBundle, error) {
	if b == nil || b.db == nil || b.tokens == nil {
		return DurableFlow{}, nil, fmt.Errorf("oauth: durable broker not configured")
	}
	row, err := b.db.ClaimOAuthFlow(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return DurableFlow{}, nil, ErrFlowNotFound
	}
	if err != nil {
		return DurableFlow{}, nil, fmt.Errorf("oauth: claim flow: %w", err)
	}
	flow, err := durableFlowFromRow(row)
	if err != nil {
		return DurableFlow{}, nil, err
	}
	exchangeCtx := ctx
	if client != nil {
		exchangeCtx = context.WithValue(ctx, oauth2.HTTPClient, client)
	}
	options := []oauth2.AuthCodeOption{oauth2.VerifierOption(row.PkceVerifier)}
	if flow.Config.Resource != "" {
		options = append(options, oauth2.SetAuthURLParam("resource", flow.Config.Resource))
	}
	tok, err := (&oauth2.Config{
		ClientID: flow.Config.ClientID, ClientSecret: clientSecret,
		RedirectURL: flow.Config.RedirectURI, Scopes: flow.Config.Scopes,
		Endpoint: oauth2.Endpoint{TokenURL: flow.Config.TokenURL, AuthStyle: oauth2.AuthStyle(flow.Config.AuthStyle)},
	}).Exchange(exchangeCtx, code, options...)
	if err != nil {
		b.fail(ctx, id, err)
		return flow, nil, fmt.Errorf("oauth: exchange authorization code: %w", err)
	}
	bundle := OAuthBundle{
		Version: 1, ClientID: flow.Config.ClientID, TokenEndpoint: flow.Config.TokenURL,
		AuthStyle: flow.Config.AuthStyle, Resource: flow.Config.Resource,
		AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken,
		AccessExpiresAt: tok.Expiry, GrantedScope: grantedScope(tok), DesiredScopes: append([]string(nil), flow.Config.Scopes...), Brand: flow.Config.Brand,
	}
	if seconds, ok := tok.Extra("refresh_token_expires_in").(float64); ok && seconds > 0 {
		bundle.RefreshExpiresAt = time.Now().UTC().Add(time.Duration(seconds) * time.Second)
	}
	if flow.Config.StoreClientSecret {
		bundle.ClientSecret = clientSecret
	}
	if err := b.tokens.Save(ctx, BundleRef{ProviderKey: flow.ProviderKey, Owner: flow.Owner, Name: flow.BundleName}, bundle); err != nil {
		b.fail(ctx, id, err)
		return flow, nil, err
	}
	if err := b.db.UpdateOAuthFlow(ctx, sqlc.UpdateOAuthFlowParams{ID: id, State: string(FlowStateAuthorized), Error: ""}); err != nil {
		return flow, nil, fmt.Errorf("oauth: mark flow authorized: %w", err)
	}
	flow.State = FlowStateAuthorized
	return flow, &bundle, nil
}

func (b *DurableAuthCodeBroker) fail(ctx context.Context, id string, cause error) {
	_ = b.db.UpdateOAuthFlow(ctx, sqlc.UpdateOAuthFlowParams{ID: id, State: string(FlowStateFailed), Error: cause.Error()})
}

func durableFlowFromRow(row sqlc.OauthFlow) (DurableFlow, error) {
	var cfg AuthCodeConfig
	if err := json.Unmarshal(row.OauthConfig, &cfg); err != nil {
		return DurableFlow{}, fmt.Errorf("oauth: decode authorization flow config: %w", err)
	}
	return DurableFlow{
		ID: row.ID, ProviderKey: row.ProviderKey, TargetKind: row.TargetKind, TargetID: row.TargetID,
		ServerID: textValue(row.ServerID), UserID: row.UserID,
		Owner:      CredentialOwner{Scope: row.CredentialScope, UserID: textValue(row.CredentialUserID), AgentID: textValue(row.CredentialAgentID)},
		BundleName: row.BundleName, FlowType: row.FlowType, State: FlowState(row.State), Error: row.Error, ExpiresAt: row.ExpiresAt, Config: cfg,
	}, nil
}

func nullableText(v string) pgtype.Text { return pgtype.Text{String: v, Valid: v != ""} }

func textValue(v pgtype.Text) string {
	if v.Valid {
		return v.String
	}
	return ""
}

func grantedScope(tok *oauth2.Token) string {
	if raw, ok := tok.Extra("scope").(string); ok {
		return raw
	}
	return ""
}
