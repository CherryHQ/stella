package mcp

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// McpOauthFlow aliases the generated flow row so the OAuth files do not all
// import sqlc.
type (
	McpOauthFlow             = sqlc.McpOauthFlow
	CreateMCPOAuthFlowParams = sqlc.CreateMCPOAuthFlowParams
)

// DB is the persistence surface the Service needs. *sqlc.Queries satisfies it.
type DB interface {
	CreateMCPOAuthFlow(ctx context.Context, arg CreateMCPOAuthFlowParams) (McpOauthFlow, error)
	ConsumeMCPOAuthFlow(ctx context.Context, id string) (McpOauthFlow, error)
}

// Vault stores and retrieves the per-connection bearer token, age-encrypted at
// rest under the same 4-value scope as the registration. *vault.Service
// satisfies it.
type Vault interface {
	SetScoped(ctx context.Context, scope, userID, agentID, name, plaintext string) error
	SetSystemScoped(ctx context.Context, scope, agentID, name, plaintext string) error
	GetScoped(ctx context.Context, scope, userID, agentID, name string) (string, error)
	DeleteScoped(ctx context.Context, scope, userID, agentID, name string) error
	DeleteSystemScoped(ctx context.Context, scope, agentID, name string) error
}

// Service coordinates file-backed MCP grants, OAuth state, and session-owned
// remote connections. Secrets stay in the Vault and are referenced by the
// immutable file registration projection.
type Service struct {
	db        DB
	vault     Vault
	pool      *pgxpool.Pool
	bindVault func(pgx.Tx) Vault
	// connect opens a client session to a registration; injectable so tests can
	// fake the remote server. The default implementation resolves the
	// credential for owner (bearer from the vault, OAuth via a TokenSource
	// handler) and opens the session. probeTimeout bounds one connect + tools/list.
	connect      func(ctx context.Context, reg Registration, owner CredentialOwner) (RemoteClient, error)
	fileConnect  func(context.Context, Registration, CredentialOwner, func()) (RemoteClient, error)
	probeTimeout time.Duration
	// fileSessionFactory is a package-test seam; production always creates a
	// disposable FileSession for each explicit file probe.
	fileSessionFactory func(*Service) *FileSession
	// endpoints gates file MCP URLs during discovery, OAuth, and every dial; the
	// zero value is public-only.
	endpoints EndpointPolicy
	// refreshLocks serializes token refresh per (registration, owner) so
	// concurrent tool calls share one /token round trip.
	refreshLocks sync.Map
	// Only live handles are indexed; Vault remains the authorization source.
	fileConnections fileConnectionIndex
}

// defaultProbeTimeout bounds one probe (connect + tools/list).
const defaultProbeTimeout = 15 * time.Second

// NewService builds a Service. vault may be nil, in which case bearer auth is
// rejected (there is nowhere to store the secret).
func NewService(db DB, vault Vault) *Service {
	s := &Service{
		db:           db,
		vault:        vault,
		probeTimeout: defaultProbeTimeout,
	}
	s.connect = s.connectSession
	return s
}

// SetEndpointPolicy replaces the endpoint policy. Call it at startup, before
// the service handles requests; it is not synchronized.
func (s *Service) SetEndpointPolicy(policy EndpointPolicy) { s.endpoints = policy }

// NewServiceForPool builds a Service backed by the given connection pool,
// owning construction of its sqlc query set. vault may be nil, in which case
// bearer auth is rejected (there is nowhere to store the secret).
func NewServiceForPool(pool *pgxpool.Pool, vault Vault, bindVault func(pgx.Tx) Vault) *Service {
	svc := NewService(sqlc.New(pool), vault)
	svc.pool = pool
	svc.bindVault = bindVault
	return svc
}

// CredentialOwner resolves the vault tuple holding the credential the caller
// should use: the registration's own scope for shared mode, the calling user's
// user scope for per_user mode.
func (s *Service) CredentialOwner(reg Registration, userID string) CredentialOwner {
	return credentialOwnerFor(reg, userID)
}

func credentialOwnerFor(reg Registration, userID string) CredentialOwner {
	if reg.CredentialMode == CredentialModePerUser {
		return CredentialOwner{Scope: ScopeUser, UserID: userID}
	}
	return CredentialOwner{Scope: reg.Scope, UserID: reg.UserID, AgentID: reg.AgentID}
}

// connectClient resolves the registration's credential for owner and opens a
// session. It is the lazy path tool proxies take on first Execute.
func (s *Service) connectClient(ctx context.Context, reg Registration, owner CredentialOwner) (RemoteClient, error) {
	return s.connect(ctx, reg, owner)
}

// connectSession is the default connect implementation.
func (s *Service) connectSession(ctx context.Context, reg Registration, owner CredentialOwner) (RemoteClient, error) {
	transport, err := s.buildTransport(ctx, reg, owner)
	if err != nil {
		return nil, connectionError(reg, err)
	}
	c := mcpsdk.NewClient(clientImpl, nil)
	session, err := c.Connect(ctx, transport, nil)
	if err != nil {
		return nil, connectionError(reg, err)
	}
	return &Client{session: session}, nil
}

// SetConnectForTesting replaces the transport-level connect function with a
// fake remote. Test-only seam: real endpoints are unreachable in tests because
// the SSRF-safe dialer refuses loopback/private targets.
func (s *Service) SetConnectForTesting(fn func(ctx context.Context, reg Registration, owner CredentialOwner) (RemoteClient, error)) {
	s.connect = fn
}

// SetFileConnectForTesting replaces the session connector used by file-backed
// MCP catalog tests. Production file sessions keep using the endpoint-safe
// connector; the seam lets integration tests exercise the real catalog path
// against a deterministic tools/list client.
func (s *Service) SetFileConnectForTesting(fn func(context.Context, Registration, CredentialOwner, func()) (RemoteClient, error)) {
	s.fileConnect = fn
}
