package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/platform/diagnostic"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// DB is the persistence surface the Service needs. *sqlc.Queries satisfies it.
type DB interface {
	CreateMCPServer(ctx context.Context, arg sqlc.CreateMCPServerParams) (sqlc.McpServer, error)
	GetMCPServerByID(ctx context.Context, id string) (sqlc.McpServer, error)
	ListMCPServersByScope(ctx context.Context, arg sqlc.ListMCPServersByScopeParams) ([]sqlc.McpServer, error)
	ListMCPServersForAgentContext(ctx context.Context, arg sqlc.ListMCPServersForAgentContextParams) ([]sqlc.McpServer, error)
	UpdateMCPServerByScope(ctx context.Context, arg sqlc.UpdateMCPServerByScopeParams) (sqlc.McpServer, error)
	UpdateMCPServerByScopeIfVersion(ctx context.Context, arg sqlc.UpdateMCPServerByScopeIfVersionParams) (sqlc.McpServer, error)
	UpdateMCPServerProbeResult(ctx context.Context, arg sqlc.UpdateMCPServerProbeResultParams) (sqlc.McpServer, error)
	UpdateMCPServerStatus(ctx context.Context, arg sqlc.UpdateMCPServerStatusParams) error
	DeleteMCPServerByScope(ctx context.Context, arg sqlc.DeleteMCPServerByScopeParams) error
	DeleteMCPServerByScopeIfVersion(ctx context.Context, arg sqlc.DeleteMCPServerByScopeIfVersionParams) (int64, error)
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

// Service manages MCP server registrations and their encrypted credentials.
// The registration table holds no secret material — the bearer token lives in
// the vault and is referenced by CredentialRef.
type Service struct {
	db        DB
	vault     Vault
	pool      *pgxpool.Pool
	bindVault func(pgx.Tx) Vault

	// connect opens a client session to a registration; injectable so tests can
	// fake the remote server. probeTimeout bounds one connect + tools/list.
	connect      func(ctx context.Context, reg Registration, bearer string) (RemoteClient, error)
	probeTimeout time.Duration
	// endpoints gates registration URLs at write time and every dial; the zero
	// value is public-only.
	endpoints EndpointPolicy
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
	s.connect = func(ctx context.Context, reg Registration, bearer string) (RemoteClient, error) {
		return ConnectWithPolicy(ctx, reg, bearer, s.endpoints)
	}
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

// transaction gives registration and credential lifecycle one commit point.
// Unit-only Services without a pool retain their fake-friendly direct path.
func (s *Service) transaction(ctx context.Context, fn func(*Service) (Registration, error)) (Registration, error) {
	if s.pool == nil {
		return fn(s)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Registration{}, fmt.Errorf("mcp: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Everything but the DB handle is inherited: a transactional child must
	// validate and dial with the same policy as its parent.
	child := &Service{db: sqlc.New(s.pool).WithTx(tx), vault: s.vault, connect: s.connect, probeTimeout: s.probeTimeout, endpoints: s.endpoints}
	if s.bindVault != nil {
		child.vault = s.bindVault(tx)
		if child.vault == nil {
			return Registration{}, fmt.Errorf("mcp: bind vault transaction")
		}
	}
	out, err := fn(child)
	if err != nil {
		return Registration{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Registration{}, fmt.Errorf("mcp: commit transaction: %w", err)
	}
	return out, nil
}

// connectClient resolves the registration's bearer and opens a session. It is
// the lazy path tool proxies take on first Execute.
func (s *Service) connectClient(ctx context.Context, reg Registration) (RemoteClient, error) {
	bearer, err := s.BearerToken(ctx, reg)
	if err != nil {
		return nil, err
	}
	return s.connect(ctx, reg, bearer)
}

// CreateInput describes a new registration. Token is the raw bearer token,
// stored encrypted in the vault and never persisted in mcp_server.
type CreateInput struct {
	Scope          string
	UserID         string
	AgentID        string
	Name           string
	URL            string
	Transport      string
	AuthType       string
	Token          string
	CredentialMode string
}

// UpdateInput describes a partial registration update. Nil fields keep the
// current value; Token nil keeps the current bearer token, while Token != nil
// replaces it.
type UpdateInput struct {
	ID              string
	Scope           string
	UserID          string
	AgentID         string
	NewScope        *string
	NewUserID       string
	NewAgentID      string
	Name            *string
	URL             *string
	Transport       *string
	AuthType        *string
	Enabled         *bool
	Token           *string
	CredentialMode  *string
	ExpectedVersion string // tool-only opaque version; empty preserves HTTP's unconditional contract
}

// Create validates the input, stores any bearer token in the vault, and inserts
// the registration. Enum validation (scope, transport, auth) happens here so the
// stdio transport and other invalid values are rejected before touching the DB.
func (s *Service) Create(ctx context.Context, in CreateInput) (Registration, error) {
	return s.transaction(ctx, func(tx *Service) (Registration, error) {
		return tx.create(ctx, in)
	})
}

func (s *Service) create(ctx context.Context, in CreateInput) (Registration, error) {
	if in.Transport == "" {
		in.Transport = TransportStreamableHTTP
	}
	if in.AuthType == "" {
		in.AuthType = AuthTypeNone
	}
	if err := validateRegistration(in.Scope, in.Name, in.URL, in.Transport, in.AuthType, s.endpoints); err != nil {
		return Registration{}, err
	}
	if err := validateCredentialMode(in.CredentialMode); err != nil {
		return Registration{}, err
	}
	if err := validateScopeOwner(in.Scope, in.UserID, in.AgentID); err != nil {
		return Registration{}, err
	}

	id := uuid.Must(uuid.NewV7()).String()

	credRef := ""
	if in.AuthType == AuthTypeBearer {
		if in.Token == "" {
			return Registration{}, fmt.Errorf("mcp: auth_type %q requires a token", AuthTypeBearer)
		}
		if s.vault == nil {
			return Registration{}, fmt.Errorf("mcp: bearer auth requires the vault, which is not configured")
		}
		credRef = credentialName(id)
		if err := s.storeToken(ctx, in.Scope, in.UserID, in.AgentID, credRef, in.Token); err != nil {
			return Registration{}, fmt.Errorf("mcp: store token: %w", err)
		}
	}

	row, err := s.db.CreateMCPServer(ctx, sqlc.CreateMCPServerParams{
		ID:            id,
		Scope:         in.Scope,
		UserID:        pgnull.Text(in.UserID),
		AgentID:       pgnull.Text(in.AgentID),
		Name:          in.Name,
		Url:           in.URL,
		Transport:     in.Transport,
		AuthType:      in.AuthType,
		CredentialRef: credRef,
		Enabled:       true,
		Metadata:      json.RawMessage(`{}`),
	})
	if err != nil {
		// Best-effort rollback of the orphaned secret so a failed insert does not
		// leave a dangling vault entry.
		if credRef != "" {
			_ = s.deleteToken(ctx, in.Scope, in.UserID, in.AgentID, credRef)
		}
		return Registration{}, fmt.Errorf("mcp: create registration: %w", err)
	}
	return registrationFromRow(row), nil
}

// Get returns one registration only when it belongs to the requested scope and
// owner. Callers can map a mismatch to not-found without leaking another scope.
func (s *Service) Get(ctx context.Context, id, scope, userID, agentID string) (Registration, error) {
	if err := validateScopeOwner(scope, userID, agentID); err != nil {
		return Registration{}, err
	}
	row, err := s.db.GetMCPServerByID(ctx, id)
	if err != nil {
		return Registration{}, fmt.Errorf("mcp: get registration: %w", err)
	}
	if !rowMatchesScopeOwner(row, scope, userID, agentID) {
		return Registration{}, fmt.Errorf("mcp: registration not found in scope")
	}
	return registrationFromRow(row), nil
}

// ListByScope returns every registration in exactly one scope/owner bucket.
func (s *Service) ListByScope(ctx context.Context, scope, userID, agentID string) ([]Registration, error) {
	if err := validateScopeOwner(scope, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.db.ListMCPServersByScope(ctx, sqlc.ListMCPServersByScopeParams{
		Scope:   scope,
		UserID:  pgnull.Text(userID),
		AgentID: pgnull.Text(agentID),
	})
	if err != nil {
		return nil, fmt.Errorf("mcp: list registrations: %w", err)
	}
	return registrationsFromRows(rows), nil
}

// Update modifies a registration in the given current scope/owner bucket and
// returns the updated row. Omitted fields keep their current values. If bearer
// auth stays enabled and Token is omitted, the existing encrypted token is kept;
// moving scopes copies that token to the new vault bucket.
func (s *Service) Update(ctx context.Context, in UpdateInput) (Registration, error) {
	return s.transaction(ctx, func(tx *Service) (Registration, error) {
		return tx.update(ctx, in)
	})
}

// UpdateIfVersion applies a tool mutation only if the durable row still matches
// the opaque version observed by the caller. The SQL predicate closes the gap
// between the read and write inside this transaction.
func (s *Service) UpdateIfVersion(ctx context.Context, in UpdateInput, expectedVersion string) (Registration, error) {
	if strings.TrimSpace(expectedVersion) == "" {
		return Registration{}, ErrVersionConflict
	}
	in.ExpectedVersion = expectedVersion
	return s.Update(ctx, in)
}

func (s *Service) update(ctx context.Context, in UpdateInput) (Registration, error) {
	if in.ID == "" {
		return Registration{}, fmt.Errorf("mcp: id is required")
	}
	if err := validateScopeOwner(in.Scope, in.UserID, in.AgentID); err != nil {
		return Registration{}, err
	}

	current, err := s.db.GetMCPServerByID(ctx, in.ID)
	if err != nil {
		return Registration{}, fmt.Errorf("mcp: get registration: %w", err)
	}
	if !rowMatchesScopeOwner(current, in.Scope, in.UserID, in.AgentID) {
		return Registration{}, fmt.Errorf("mcp: registration not found in scope")
	}
	old := registrationFromRow(current)
	if in.ExpectedVersion != "" && old.Version() != in.ExpectedVersion {
		return Registration{}, ErrVersionConflict
	}

	newScope := old.Scope
	if in.NewScope != nil {
		newScope = *in.NewScope
	}
	newUserID := in.NewUserID
	newAgentID := in.NewAgentID
	if in.NewScope == nil {
		newUserID = old.UserID
		newAgentID = old.AgentID
	}
	if err := validateScopeOwner(newScope, newUserID, newAgentID); err != nil {
		return Registration{}, err
	}

	name := old.Name
	if in.Name != nil {
		name = *in.Name
	}
	rawURL := old.URL
	if in.URL != nil {
		rawURL = *in.URL
	}
	transport := old.Transport
	if in.Transport != nil {
		transport = *in.Transport
	}
	authType := old.AuthType
	if in.AuthType != nil {
		authType = *in.AuthType
	}
	enabled := old.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	// credential_mode cannot be changed this phase (only 'shared' exists), but an
	// explicit per_user request must fail here rather than silently no-op.
	if in.CredentialMode != nil && *in.CredentialMode != old.CredentialMode {
		return Registration{}, validateCredentialMode(*in.CredentialMode)
	}
	if err := validateRegistration(newScope, name, rawURL, transport, authType, s.endpoints); err != nil {
		return Registration{}, err
	}

	credentialRef := old.CredentialRef
	var tokenToStore string
	storeToken := false
	deleteOldToken := false
	oldTokenScope, oldTokenUserID, oldTokenAgentID := old.Scope, old.UserID, old.AgentID

	scopeMoved := newScope != old.Scope || newUserID != old.UserID || newAgentID != old.AgentID
	switch authType {
	case AuthTypeNone:
		credentialRef = ""
		deleteOldToken = old.CredentialRef != ""
	case AuthTypeBearer:
		if s.vault == nil {
			return Registration{}, fmt.Errorf("mcp: bearer auth requires the vault, which is not configured")
		}
		if credentialRef == "" {
			credentialRef = credentialName(in.ID)
		}
		switch {
		case in.Token != nil:
			if *in.Token == "" {
				return Registration{}, fmt.Errorf("mcp: auth_type %q requires a token", AuthTypeBearer)
			}
			tokenToStore = *in.Token
			storeToken = true
		case old.AuthType == AuthTypeBearer && old.CredentialRef != "" && scopeMoved:
			return Registration{}, fmt.Errorf("mcp: moving a bearer registration requires replacement credentials in the Web UI")
		case old.AuthType != AuthTypeBearer || old.CredentialRef == "":
			return Registration{}, fmt.Errorf("mcp: auth_type %q requires a token", AuthTypeBearer)
		}
		deleteOldToken = old.CredentialRef != "" && old.CredentialRef != credentialRef || (old.CredentialRef != "" && scopeMoved)
	}

	if storeToken {
		if err := s.storeToken(ctx, newScope, newUserID, newAgentID, credentialRef, tokenToStore); err != nil {
			return Registration{}, fmt.Errorf("mcp: store token: %w", err)
		}
	}

	params := sqlc.UpdateMCPServerByScopeParams{
		NewScope:      newScope,
		NewUserID:     pgnull.Text(newUserID),
		NewAgentID:    pgnull.Text(newAgentID),
		Name:          name,
		Url:           rawURL,
		Transport:     transport,
		AuthType:      authType,
		CredentialRef: credentialRef,
		Enabled:       enabled,
		ID:            in.ID,
		Scope:         in.Scope,
		UserID:        pgnull.Text(in.UserID),
		AgentID:       pgnull.Text(in.AgentID),
	}
	var row sqlc.McpServer
	if in.ExpectedVersion == "" {
		row, err = s.db.UpdateMCPServerByScope(ctx, params)
	} else {
		row, err = s.db.UpdateMCPServerByScopeIfVersion(ctx, sqlc.UpdateMCPServerByScopeIfVersionParams{
			NewScope: params.NewScope, NewUserID: params.NewUserID, NewAgentID: params.NewAgentID,
			Name: params.Name, Url: params.Url, Transport: params.Transport, AuthType: params.AuthType,
			CredentialRef: params.CredentialRef, Enabled: params.Enabled, ID: params.ID, Scope: params.Scope,
			UserID: params.UserID, AgentID: params.AgentID, ExpectedUpdatedAt: current.UpdatedAt,
		})
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) && in.ExpectedVersion != "" {
			return Registration{}, ErrVersionConflict
		}
		if storeToken && (scopeMoved || old.CredentialRef == "") {
			_ = s.deleteToken(ctx, newScope, newUserID, newAgentID, credentialRef)
		}
		return Registration{}, fmt.Errorf("mcp: update registration: %w", err)
	}
	if deleteOldToken {
		_ = s.deleteToken(ctx, oldTokenScope, oldTokenUserID, oldTokenAgentID, old.CredentialRef)
	}
	return registrationFromRow(row), nil
}

// Delete removes a registration in the given scope and its vault credential.
func (s *Service) Delete(ctx context.Context, id, scope, userID, agentID string) error {
	return s.deleteIfVersion(ctx, id, scope, userID, agentID, "")
}

// DeleteIfVersion is the tool-only CAS delete path. HTTP callers retain the
// existing unconditional contract through Delete.
func (s *Service) DeleteIfVersion(ctx context.Context, id, scope, userID, agentID, expectedVersion string) error {
	if strings.TrimSpace(expectedVersion) == "" {
		return ErrVersionConflict
	}
	return s.deleteIfVersion(ctx, id, scope, userID, agentID, expectedVersion)
}

func (s *Service) deleteIfVersion(ctx context.Context, id, scope, userID, agentID, expectedVersion string) error {
	_, err := s.transaction(ctx, func(tx *Service) (Registration, error) {
		if err := tx.delete(ctx, id, scope, userID, agentID, expectedVersion); err != nil {
			return Registration{}, err
		}
		return Registration{}, nil
	})
	return err
}

func (s *Service) delete(ctx context.Context, id, scope, userID, agentID, expectedVersion string) error {
	if err := validateScopeOwner(scope, userID, agentID); err != nil {
		return err
	}
	row, err := s.db.GetMCPServerByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		if expectedVersion == "" {
			return nil // HTTP DELETE is idempotent for an already-absent registration.
		}
		return ErrVersionConflict
	}
	if err != nil {
		return fmt.Errorf("mcp: get registration: %w", err)
	}
	if !rowMatchesScopeOwner(row, scope, userID, agentID) {
		if expectedVersion == "" {
			return nil // Do not expose a registration outside this authorized scope.
		}
		return ErrVersionConflict
	}
	if expectedVersion != "" && registrationFromRow(row).Version() != expectedVersion {
		return ErrVersionConflict
	}
	if expectedVersion == "" {
		if err := s.db.DeleteMCPServerByScope(ctx, sqlc.DeleteMCPServerByScopeParams{ID: id, Scope: scope, UserID: pgnull.Text(userID), AgentID: pgnull.Text(agentID)}); err != nil {
			return fmt.Errorf("mcp: delete registration: %w", err)
		}
	} else {
		deleted, err := s.db.DeleteMCPServerByScopeIfVersion(ctx, sqlc.DeleteMCPServerByScopeIfVersionParams{ID: id, Scope: scope, UserID: pgnull.Text(userID), AgentID: pgnull.Text(agentID), ExpectedUpdatedAt: row.UpdatedAt})
		if err != nil {
			return fmt.Errorf("mcp: delete registration: %w", err)
		}
		if deleted == 0 {
			return ErrVersionConflict
		}
	}
	if row.CredentialRef != "" {
		if err := s.deleteToken(ctx, scope, userID, agentID, row.CredentialRef); err != nil {
			return fmt.Errorf("mcp: delete token: %w", err)
		}
	}
	return nil
}

// Probe connects to the registration's endpoint, runs tools/list, and
// persists the result: ok + catalog, error + redacted reason, or needs_auth
// when the server rejected the credential with 401/403. A failed probe returns
// the persisted registration (status=error), never an error — callers decide
// whether that counts as a failure.
func (s *Service) Probe(ctx context.Context, reg Registration) (Registration, error) {
	probeCtx, cancel := context.WithTimeout(ctx, s.probeTimeout)
	defer cancel()

	bearer, err := s.BearerToken(probeCtx, reg)
	if err != nil {
		return s.persistProbeFailure(ctx, reg, err)
	}
	client, err := s.connect(probeCtx, reg, bearer)
	if err != nil {
		return s.persistProbeFailure(ctx, reg, err)
	}
	defer func() { _ = client.Close() }()
	remote, err := client.ListTools(probeCtx)
	if err != nil {
		return s.persistProbeFailure(ctx, reg, err)
	}
	tools := make([]CatalogTool, 0, len(remote))
	for _, rt := range remote {
		tools = append(tools, CatalogTool{
			Name:        rt.Name,
			Description: rt.Description,
			InputSchema: cloneSchema(toolInputSchema(rt.InputSchema)),
			Annotations: annotationsSchema(rt.Annotations),
		})
	}
	return s.persistProbeResult(ctx, reg.ID, StatusOK, "", tools)
}

// SetConnectForTesting replaces the transport-level connect function with a
// fake remote. Test-only seam: real endpoints are unreachable in tests because
// the SSRF-safe dialer refuses loopback/private targets.
func (s *Service) SetConnectForTesting(fn func(ctx context.Context, reg Registration, bearer string) (RemoteClient, error)) {
	s.connect = fn
}

// SetStatus persists a status transition without a probe (e.g. a tool call
// rejected a stored credential). It deliberately leaves updated_at alone so a
// status change cannot invalidate a client's If-Match version.
func (s *Service) SetStatus(ctx context.Context, id, status, msg string) error {
	if !ValidStatus(status) {
		return fmt.Errorf("mcp: invalid status %q", status)
	}
	if err := s.db.UpdateMCPServerStatus(ctx, sqlc.UpdateMCPServerStatusParams{Status: status, StatusError: msg, ID: id}); err != nil {
		return fmt.Errorf("mcp: persist status: %w", err)
	}
	return nil
}

// persistProbeFailure keeps the last known catalog (still the best snapshot
// available) and records the redacted reason.
func (s *Service) persistProbeFailure(ctx context.Context, reg Registration, cause error) (Registration, error) {
	status := StatusError
	if isCredentialRejection(cause) {
		status = StatusNeedsAuth
	}
	return s.persistProbeResult(ctx, reg.ID, status, redactProbeError(reg.URL, cause), nil)
}

// persistProbeResult writes the probe outcome; tools nil keeps the existing catalog.
func (s *Service) persistProbeResult(ctx context.Context, id, status, statusErr string, tools []CatalogTool) (Registration, error) {
	catalog := tools
	if catalog == nil {
		if current, err := s.db.GetMCPServerByID(ctx, id); err == nil {
			catalog = decodeCatalog(current.Tools)
		}
	}
	toolsJSON, err := json.Marshal(catalog)
	if err != nil {
		toolsJSON = []byte("[]")
	}
	row, err := s.db.UpdateMCPServerProbeResult(ctx, sqlc.UpdateMCPServerProbeResultParams{
		Status:      status,
		StatusError: statusErr,
		ProbedAt:    pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		Tools:       toolsJSON,
		ID:          id,
	})
	if err != nil {
		return Registration{}, fmt.Errorf("mcp: persist probe result: %w", err)
	}
	return registrationFromRow(row), nil
}

// urlPattern matches any URL in an error message so probe failures can never
// carry userinfo, query secrets, or fragments, wherever the URL came from.
var urlPattern = regexp.MustCompile(`https?://[^\s]+`)

// redactProbeError scrubs every URL from a probe failure message down to its
// scheme/host/path via diagnostic.Endpoint.
func redactProbeError(rawURL string, err error) string {
	return urlPattern.ReplaceAllStringFunc(err.Error(), diagnostic.Endpoint)
}

// ResolveForContext returns the enabled registrations visible to a (user,
// agent), deduplicated by name with precedence user_agent > user > system_agent
// > system (the SQL already orders most-specific-first).
func (s *Service) ResolveForContext(ctx context.Context, userID, agentID string) ([]Registration, error) {
	rows, err := s.db.ListMCPServersForAgentContext(ctx, sqlc.ListMCPServersForAgentContextParams{
		UserID:  pgnull.Text(userID),
		AgentID: pgnull.Text(agentID),
	})
	if err != nil {
		return nil, fmt.Errorf("mcp: resolve registrations: %w", err)
	}
	seen := make(map[string]bool, len(rows))
	out := make([]Registration, 0, len(rows))
	for _, row := range rows {
		if seen[row.Name] {
			continue
		}
		seen[row.Name] = true
		out = append(out, registrationFromRow(row))
	}
	return out, nil
}

// BearerToken returns the decrypted bearer token for a registration, or an empty
// string when the server needs no auth.
func (s *Service) BearerToken(ctx context.Context, reg Registration) (string, error) {
	if reg.AuthType != AuthTypeBearer || reg.CredentialRef == "" {
		return "", nil
	}
	if s.vault == nil {
		return "", fmt.Errorf("mcp: cannot read token: vault not configured")
	}
	tok, err := s.vault.GetScoped(ctx, reg.Scope, reg.UserID, reg.AgentID, reg.CredentialRef)
	if err != nil {
		return "", fmt.Errorf("mcp: read token: %w", err)
	}
	return tok, nil
}

func (s *Service) storeToken(ctx context.Context, scope, userID, agentID, name, token string) error {
	if IsSystemScope(scope) {
		return s.vault.SetSystemScoped(ctx, scope, agentID, name, token)
	}
	return s.vault.SetScoped(ctx, scope, userID, agentID, name, token)
}

func (s *Service) deleteToken(ctx context.Context, scope, userID, agentID, name string) error {
	if IsSystemScope(scope) {
		return s.vault.DeleteSystemScoped(ctx, scope, agentID, name)
	}
	return s.vault.DeleteScoped(ctx, scope, userID, agentID, name)
}

// validateScopeOwner enforces the same scope/owner coupling as the DB CHECK, so
// callers get a clear error before the insert instead of a constraint violation.
func validateScopeOwner(scope, userID, agentID string) error {
	switch scope {
	case ScopeUser:
		if userID == "" || agentID != "" {
			return fmt.Errorf("mcp: user scope requires user_id only")
		}
	case ScopeUserAgent:
		if userID == "" || agentID == "" {
			return fmt.Errorf("mcp: user_agent scope requires user_id and agent_id")
		}
	case ScopeSystem:
		if userID != "" || agentID != "" {
			return fmt.Errorf("mcp: system scope cannot include user_id or agent_id")
		}
	case ScopeSystemAgent:
		if userID != "" || agentID == "" {
			return fmt.Errorf("mcp: system_agent scope requires agent_id only")
		}
	default:
		return fmt.Errorf("mcp: invalid scope %q", scope)
	}
	return nil
}

func textOrEmpty(v pgtype.Text) string {
	if !v.Valid {
		return ""
	}
	return v.String
}

func rowMatchesScopeOwner(row sqlc.McpServer, scope, userID, agentID string) bool {
	return row.Scope == scope && textOrEmpty(row.UserID) == userID && textOrEmpty(row.AgentID) == agentID
}

func registrationFromRow(row sqlc.McpServer) Registration {
	var metadata map[string]any
	if len(row.Metadata) > 0 {
		_ = json.Unmarshal(row.Metadata, &metadata)
	}
	return Registration{
		ID:             row.ID,
		Scope:          row.Scope,
		UserID:         textOrEmpty(row.UserID),
		AgentID:        textOrEmpty(row.AgentID),
		Name:           row.Name,
		URL:            row.Url,
		Transport:      row.Transport,
		AuthType:       row.AuthType,
		CredentialRef:  row.CredentialRef,
		Enabled:        row.Enabled,
		Status:         row.Status,
		StatusError:    row.StatusError,
		ProbedAt:       row.ProbedAt.Time.UTC(),
		Tools:          decodeCatalog(row.Tools),
		CredentialMode: row.CredentialMode,
		Metadata:       metadata,
		CreatedAt:      row.CreatedAt.UTC(),
		UpdatedAt:      row.UpdatedAt.UTC(),
	}
}

// decodeCatalog unmarshals the persisted tool catalog; corrupt or empty JSON
// yields nil so callers treat the server as unprobed.
func decodeCatalog(raw json.RawMessage) []CatalogTool {
	if len(raw) == 0 {
		return nil
	}
	var out []CatalogTool
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func registrationsFromRows(rows []sqlc.McpServer) []Registration {
	out := make([]Registration, len(rows))
	for i, row := range rows {
		out[i] = registrationFromRow(row)
	}
	return out
}
