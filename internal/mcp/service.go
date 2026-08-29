package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

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
	DeleteMCPServerByScope(ctx context.Context, arg sqlc.DeleteMCPServerByScopeParams) error
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
	db    DB
	vault Vault
}

// NewService builds a Service. vault may be nil, in which case bearer auth is
// rejected (there is nowhere to store the secret).
func NewService(db DB, vault Vault) *Service {
	return &Service{db: db, vault: vault}
}

// NewServiceForPool builds a Service backed by the given connection pool,
// owning construction of its sqlc query set. vault may be nil, in which case
// bearer auth is rejected (there is nowhere to store the secret).
func NewServiceForPool(pool *pgxpool.Pool, vault Vault) *Service {
	return NewService(sqlc.New(pool), vault)
}

// CreateInput describes a new registration. Token is the raw bearer token,
// stored encrypted in the vault and never persisted in mcp_server.
type CreateInput struct {
	Scope     string
	UserID    string
	AgentID   string
	Name      string
	URL       string
	Transport string
	AuthType  string
	Token     string
}

// UpdateInput describes a partial registration update. Nil fields keep the
// current value; Token nil keeps the current bearer token, while Token != nil
// replaces it.
type UpdateInput struct {
	ID         string
	Scope      string
	UserID     string
	AgentID    string
	NewScope   *string
	NewUserID  string
	NewAgentID string
	Name       *string
	URL        *string
	Transport  *string
	AuthType   *string
	Enabled    *bool
	Token      *string
}

// Create validates the input, stores any bearer token in the vault, and inserts
// the registration. Enum validation (scope, transport, auth) happens here so the
// stdio transport and other invalid values are rejected before touching the DB.
func (s *Service) Create(ctx context.Context, in CreateInput) (Registration, error) {
	if in.Transport == "" {
		in.Transport = TransportStreamableHTTP
	}
	if in.AuthType == "" {
		in.AuthType = AuthTypeNone
	}
	if err := validateRegistration(in.Scope, in.Name, in.URL, in.Transport, in.AuthType); err != nil {
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
	if err := validateRegistration(newScope, name, rawURL, transport, authType); err != nil {
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
			tokenToStore, err = s.vault.GetScoped(ctx, old.Scope, old.UserID, old.AgentID, old.CredentialRef)
			if err != nil {
				return Registration{}, fmt.Errorf("mcp: read existing token: %w", err)
			}
			storeToken = true
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

	row, err := s.db.UpdateMCPServerByScope(ctx, sqlc.UpdateMCPServerByScopeParams{
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
	})
	if err != nil {
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
	if err := validateScopeOwner(scope, userID, agentID); err != nil {
		return err
	}
	// Load the row first so we know the credential to purge. A missing row is a
	// no-op delete, not an error.
	if row, err := s.db.GetMCPServerByID(ctx, id); err == nil && row.CredentialRef != "" {
		_ = s.deleteToken(ctx, scope, userID, agentID, row.CredentialRef)
	}
	if err := s.db.DeleteMCPServerByScope(ctx, sqlc.DeleteMCPServerByScopeParams{
		ID:      id,
		Scope:   scope,
		UserID:  pgnull.Text(userID),
		AgentID: pgnull.Text(agentID),
	}); err != nil {
		return fmt.Errorf("mcp: delete registration: %w", err)
	}
	return nil
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
	return Registration{
		ID:            row.ID,
		Scope:         row.Scope,
		UserID:        textOrEmpty(row.UserID),
		AgentID:       textOrEmpty(row.AgentID),
		Name:          row.Name,
		URL:           row.Url,
		Transport:     row.Transport,
		AuthType:      row.AuthType,
		CredentialRef: row.CredentialRef,
		Enabled:       row.Enabled,
		CreatedAt:     row.CreatedAt.UTC(),
		UpdatedAt:     row.UpdatedAt.UTC(),
	}
}

func registrationsFromRows(rows []sqlc.McpServer) []Registration {
	out := make([]Registration, len(rows))
	for i, row := range rows {
		out[i] = registrationFromRow(row)
	}
	return out
}
