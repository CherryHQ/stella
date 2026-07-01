package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// DB is the persistence surface the Service needs. *sqlc.Queries satisfies it.
type DB interface {
	CreateMCPServer(ctx context.Context, arg sqlc.CreateMCPServerParams) (sqlc.McpServer, error)
	GetMCPServerByID(ctx context.Context, id string) (sqlc.McpServer, error)
	ListMCPServersByScope(ctx context.Context, arg sqlc.ListMCPServersByScopeParams) ([]sqlc.McpServer, error)
	ListMCPServersForAgentContext(ctx context.Context, arg sqlc.ListMCPServersForAgentContextParams) ([]sqlc.McpServer, error)
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
