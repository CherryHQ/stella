// Package webhook owns personal webhook resources, their opaque credentials,
// and the admission boundary from inbound HTTP to a fixed Agent invocation.
package webhook

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/CherryHQ/stella/internal/authz"
)

const (
	TokenPrefix = "stella_whk_"
	etagVersion = "whk1"

	DefaultWaitTimeoutSeconds   = 60
	DefaultMaxRunTimeoutSeconds = 300
	WaitTimeoutCeilingSeconds   = 600
	RunTimeoutCeilingSeconds    = 3600
)

var (
	ErrNotFound           = errors.New("webhook: not found")
	ErrInvalidID          = errors.New("webhook: invalid id")
	ErrInvalidUserID      = errors.New("webhook: invalid user id")
	ErrInvalidName        = errors.New("webhook: name is required")
	ErrInvalidAgentID     = errors.New("webhook: agent id is required")
	ErrInvalidProvider    = errors.New("webhook: invalid provider")
	ErrInvalidTimeout     = errors.New("webhook: timeout is invalid")
	ErrInvalidETag        = errors.New("webhook: invalid etag")
	ErrStaleETag          = errors.New("webhook: changed since the provided etag")
	ErrUserInactive       = errors.New("webhook: user is inactive")
	ErrUserAgentForbidden = errors.New("webhook: user cannot use agent")
	ErrBindingChanged     = errors.New("webhook: binding changed; retry")
)

type Provider string

const ProviderGeneric Provider = "generic"

func (p Provider) Valid() bool { return p == ProviderGeneric }

// Webhook excludes the credential hash by construction. The opaque public ID is
// retained only to derive the compare-only etag and never crosses the API boundary.
type Webhook struct {
	ID                   string
	UserID               string
	AgentID              string
	Name                 string
	Provider             Provider
	IsEnabled            bool
	WaitTimeoutSeconds   int32
	MaxRunTimeoutSeconds int32
	TokenPublicID        string
	TokenLast4           string
	Revision             int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
	RotatedAt            *time.Time
}

func (w Webhook) ETag() string { return EncodeETag(w.TokenPublicID, w.Revision) }

func EncodeETag(publicID string, revision int64) string {
	sum := sha256.Sum256([]byte(etagVersion + "\x00" + publicID + "\x00" + strconv.FormatInt(revision, 10)))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type CreateRequest struct {
	UserID               string
	Name                 string
	AgentID              string
	Provider             Provider
	IsEnabled            bool
	WaitTimeoutSeconds   int32
	MaxRunTimeoutSeconds int32
}

type UpdateRequest struct {
	ID                   string
	UserID               string
	Name                 *string
	AgentID              *string
	IsEnabled            *bool
	WaitTimeoutSeconds   *int32
	MaxRunTimeoutSeconds *int32
}

type IssueResult struct {
	Webhook    Webhook
	Capability string
}

type credentialRecord struct {
	Webhook
	TokenHash string
}

// Candidate holds verifier material privately across the bounded body read.
type Candidate struct {
	WebhookID string
	publicID  string
	secret    string
}

func (c Candidate) String() string       { return "webhook.Candidate{id:" + c.WebhookID + "}" }
func (c Candidate) LogValue() slog.Value { return slog.StringValue(c.String()) }

type AdmittedInvocation struct {
	WebhookID            string
	UserID               string
	AgentID              string
	Provider             Provider
	WaitTimeoutSeconds   int32
	MaxRunTimeoutSeconds int32
	Authority            authz.Authority
}

type AdmitCallback func(context.Context, AdmittedInvocation) error

type Store interface {
	Create(context.Context, credentialRecord) (credentialRecord, error)
	Get(context.Context, string, string) (credentialRecord, error)
	List(context.Context, string, int32, int32) ([]credentialRecord, error)
	// Update atomically re-observes the owner-scoped record and applies only
	// fields present in req. expectedAgent prevents a PEP decision made before
	// the lifecycle fence from writing over a changed durable binding.
	Update(context.Context, UpdateRequest, string) (credentialRecord, error)
	Rotate(context.Context, string, string, string, credentialRecord) (credentialRecord, error)
	Delete(context.Context, string, string) (int64, error)
	ResolveByPublicID(context.Context, string) (credentialRecord, error)
	ResolveAdmitted(context.Context, string) (credentialRecord, error)
}

type UserState interface {
	IsActive(context.Context, string) (bool, error)
}
type UserAgentAccess interface {
	CanUseUser(context.Context, string, string) (bool, error)
}
