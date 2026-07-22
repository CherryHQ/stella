// Package webhook owns the durable capability endpoint domain. It deliberately
// has no HTTP or plugin dependency: transports receive an already-resolved,
// fixed invocation and cannot choose an owner or agent.
package webhook

import (
	"context"
	"errors"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
)

const (
	TokenPrefix       = "stella_whk_"
	deliveryIDMaxLen  = 256
	deliveryTTL       = 30 * 24 * time.Hour
	cleanupBatchLimit = 100 // Global inline cleanup; move to a job if claim latency or table growth is material.
)

var (
	ErrNotFound              = errors.New("webhook: endpoint not found")
	ErrEndpointExists        = errors.New("webhook: endpoint already exists")
	ErrInvalidChannelID      = errors.New("webhook: channel id is required")
	ErrInvalidOwnerUserID    = errors.New("webhook: owner user id is required")
	ErrInvalidProvider       = errors.New("webhook: invalid provider")
	ErrInvalidGitHubPolicy   = errors.New("webhook: invalid github allowlist")
	ErrInvalidGitHubDelivery = errors.New("webhook: invalid github delivery")
	ErrGitHubDeliveryIgnored = errors.New("webhook: github delivery ignored")
	ErrOwnerInactive         = errors.New("webhook: owner is inactive")
	ErrOwnerAgentForbidden   = errors.New("webhook: owner cannot use agent")
)

type Provider string

const (
	ProviderGeneric Provider = "generic"
	ProviderGitHub  Provider = "github"
)

func (p Provider) Valid() bool { return p == ProviderGeneric || p == ProviderGitHub }

// Endpoint is the secret-safe endpoint metadata callers may retain or display.
// Token verifiers and provider ciphertext never cross this boundary.
type Endpoint struct {
	ID          string
	ChannelID   string
	OwnerUserID string
	Provider    Provider
	TokenLast4  string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	RotatedAt   *time.Time
}

// GitHubPolicy is persisted as webhook behavior configuration in the next
// control-plane phase. The domain accepts it here to validate issuance and
// deliveries without depending on a channel plugin implementation.
type GitHubPolicy struct {
	Events       []string
	Repositories []string
}

type IssueRequest struct {
	ChannelID   string
	OwnerUserID string
	Provider    Provider
	GitHub      GitHubPolicy
}

type IssueResult struct {
	Endpoint            Endpoint
	Capability          string // one-time disclosure only
	GitHubWebhookSecret string // one-time disclosure only; empty for generic
}

type RotationResult = IssueResult

// Invocation is a fixed, trusted runtime identity reconstructed only after a
// capability verifier and all durable active-state checks pass.
type Invocation struct {
	Endpoint  Endpoint
	AgentID   string
	Authority authz.Authority

	githubSecret string
}

type endpointRecord struct {
	Endpoint
	TokenPublicID            string
	TokenHash                string
	ProviderSecretCiphertext string
}

type resolvedRecord struct {
	endpointRecord
	AgentID        string
	ChannelEnabled bool
	OwnerActive    bool
	AgentEnabled   bool
}

// Store is the webhook domain's complete persistence port. ClaimDelivery must
// atomically remove this delivery's expired row, perform bounded global expiry,
// and claim the new row in one transaction.
type Store interface {
	// BindEndpoint locks the channel row, supplies its exact current webhook
	// agent to build, then persists the returned record in that same transaction.
	// Future channel rebinds must take this same lock before rejecting active
	// endpoints; neither path may acquire another row lock first.
	BindEndpoint(context.Context, string, func(context.Context, string) (endpointRecord, error)) (endpointRecord, error)
	GetEndpoint(context.Context, string) (endpointRecord, error)
	GetEndpointByChannel(context.Context, string) (endpointRecord, error)
	ResolveEndpoint(context.Context, string) (resolvedRecord, error)
	RotateEndpoint(context.Context, endpointRecord) (endpointRecord, error)
	DeleteEndpoint(context.Context, string) (int64, error)
	ClaimDelivery(context.Context, string, Provider, string) (bool, error)
	ReleaseDelivery(context.Context, string, Provider, string) (int64, error)
}

// UserState and OwnerAgentAccess keep lifecycle validation independent of both
// the control plane and the agent access implementation.
type UserState interface {
	IsActive(context.Context, string) (bool, error)
}

type OwnerAgentAccess interface {
	// CanUseOwner returns true only when this active owner may currently use the
	// enabled channel agent as an ordinary non-admin user.
	CanUseOwner(context.Context, string, string) (bool, error)
}

type SecretCipher interface {
	EncryptSystem(string) (string, error)
	DecryptSystem(string) (string, error)
}
