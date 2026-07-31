// Package webhook owns the capability endpoint that turns an inbound HTTP
// request into a fixed, trusted Agent invocation. In this core layer an
// endpoint is a one-to-one facet of a webhook channel: it owns the owner→Agent
// identity binding, an opaque capability verifier, and a lifecycle revision.
// Token plaintext is disclosed exactly once at issuance/rotation and is never
// persisted; only its hash crosses the storage boundary.
package webhook

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"time"
)

const (
	// TokenPrefix namespaces the opaque capability token family alongside the
	// existing stella_pat_/stella_oat_/stella_ort_ prefixes.
	TokenPrefix = "stella_whk_"

	// etagVersion versions the opaque etag scheme so a future scheme can be
	// distinguished from this one. Clients must treat the etag as opaque.
	etagVersion = "whk1"
)

var (
	ErrNotFound              = errors.New("webhook: endpoint not found")
	ErrEndpointExists        = errors.New("webhook: endpoint already exists")
	ErrInvalidChannelID      = errors.New("webhook: channel id is required")
	ErrInvalidOwnerUserID    = errors.New("webhook: owner user id is invalid")
	ErrInvalidProvider       = errors.New("webhook: invalid provider")
	ErrInvalidETag           = errors.New("webhook: invalid etag")
	ErrStaleETag             = errors.New("webhook: endpoint changed since the provided etag")
	ErrOwnerInactive         = errors.New("webhook: owner is inactive")
	ErrOwnerAgentForbidden   = errors.New("webhook: owner cannot use agent")
	ErrAgentDisabled         = errors.New("webhook: channel agent is disabled")
	ErrChannelNotWebhook     = errors.New("webhook: channel is not a webhook")
	ErrChannelBindingChanged = errors.New("webhook: channel binding changed during endpoint issuance")
)

// Provider fixes the delivery scenario an endpoint serves. This core layer
// accepts only the generic provider; the GitHub provider lands with its own
// migration, secret storage, and policy tables in a later stack layer.
type Provider string

const ProviderGeneric Provider = "generic"

func (p Provider) Valid() bool { return p == ProviderGeneric }

// Endpoint is the secret-safe endpoint metadata callers may retain or display.
// Token verifiers (the secret hash) never cross this boundary, so a response
// built from an Endpoint cannot leak a capability by construction. TokenPublicID
// is the non-secret indexed lookup key; it never appears in an API response but
// binds the opaque etag to this exact credential so a revoke+recreate (or a
// different channel's endpoint) cannot reuse a stale etag.
type Endpoint struct {
	ChannelID     string
	OwnerUserID   string
	Provider      Provider
	TokenPublicID string
	TokenLast4    string
	Revision      int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	RotatedAt     *time.Time
}

// ETag is the opaque optimistic-concurrency token for this endpoint state.
func (e Endpoint) ETag() string { return EncodeETag(e.TokenPublicID, e.Revision) }

// EncodeETag derives an opaque, compare-only concurrency token bound to both the
// current credential (token_public_id) and the lifecycle revision. It is a hash,
// so it exposes neither value and is never parsed back; rotation recomputes it
// from the locked row and compares. Because token_public_id is unique per
// credential and changes on every rotate/recreate, a stale etag from a revoked
// endpoint or another channel cannot match.
func EncodeETag(tokenPublicID string, revision int64) string {
	sum := sha256.Sum256([]byte(etagVersion + "\x00" + tokenPublicID + "\x00" + strconv.FormatInt(revision, 10)))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// IssueRequest carries all endpoint identity input. Provider policy and secrets
// are not part of the generic core contract.
type IssueRequest struct {
	ChannelID   string
	OwnerUserID string
	Provider    Provider
}

// IssueResult returns the persisted endpoint plus the one-time capability. The
// capability is disclosed to the caller exactly once and is never stored.
type IssueResult struct {
	Endpoint   Endpoint
	Capability string
}

// RotationResult mirrors IssueResult: rotation replaces the capability and
// returns the new one-time value.
type RotationResult = IssueResult

// ChannelBinding is the channel state read under the channel-row lock shared by
// endpoint issuance and channel agent/type mutation.
type ChannelBinding struct {
	ChannelID    string
	Type         string
	AgentID      string
	AgentEnabled bool
	Config       string
}

// endpointRecord is the storage-facing endpoint including the secret verifier.
// The non-secret TokenPublicID is promoted from the embedded Endpoint.
type endpointRecord struct {
	Endpoint
	TokenHash string
}

// Store is the webhook domain's persistence port.
type Store interface {
	// BindEndpoint locks the channel row, hands its current binding to build,
	// then persists the returned record in that same transaction. Channel
	// mutation takes this exact lock before checking endpoint absence; neither
	// path may acquire another row lock first.
	BindEndpoint(context.Context, string, func(context.Context, ChannelBinding) (endpointRecord, error)) (endpointRecord, error)
	GetEndpointByChannel(context.Context, string) (endpointRecord, error)
	ResolveByPublicID(context.Context, string) (endpointRecord, error)
	// RotateEndpoint replaces the verifier only if the current row's opaque etag
	// still equals expectedETag (bound to token_public_id + revision), returning
	// ErrStaleETag otherwise. The comparison happens under the row lock.
	RotateEndpoint(ctx context.Context, channelID string, expectedETag string, next endpointRecord) (endpointRecord, error)
	DeleteEndpoint(context.Context, string) (int64, error)
}

// UserState and OwnerAgentAccess keep issuance validation independent of both
// the control plane and the agent access implementation.
type UserState interface {
	IsActive(context.Context, string) (bool, error)
}

type OwnerAgentAccess interface {
	// CanUseOwner returns true only when this active owner may currently use the
	// enabled channel agent as an ordinary non-admin user.
	CanUseOwner(context.Context, string, string) (bool, error)
}
