package vault

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/CherryHQ/stella/internal/authz"
	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	"github.com/CherryHQ/stella/pkg/db/pgnull"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Authorized is the identity-scoped view of the service; all authorization
// checks live on its methods.
type Authorized struct {
	*Service
	ident authz.Identity
}

func (s *Service) As(ident authz.Identity) Authorized { return Authorized{Service: s, ident: ident} }

const (
	ScopeUser        = "user"
	ScopeUserAgent   = "user_agent"
	ScopeSystem      = "system"
	ScopeSystemAgent = "system_agent"
)

// DB is the minimal database interface the vault Service requires.
type DB interface {
	GetVaultUser(ctx context.Context, id string) (sqlc.VaultUser, error)
	GetVaultEntryByScope(ctx context.Context, arg sqlc.GetVaultEntryByScopeParams) (sqlc.VaultEntry, error)
	ListVaultEntriesByScope(ctx context.Context, arg sqlc.ListVaultEntriesByScopeParams) ([]sqlc.VaultEntry, error)
	ListVaultEntriesForRuntime(ctx context.Context, arg sqlc.ListVaultEntriesForRuntimeParams) ([]sqlc.VaultEntry, error)
	UpsertVaultEntryByScope(ctx context.Context, arg sqlc.UpsertVaultEntryByScopeParams) (sqlc.VaultEntry, error)
	DeleteVaultEntryByScope(ctx context.Context, arg sqlc.DeleteVaultEntryByScopeParams) error
}

// Service provides vault operations: storing, retrieving, and decrypting
// secrets using user-level or system-level age encryption.
type Service struct {
	db              DB
	masterIdentity  *age.X25519Identity
	masterRecipient *age.X25519Recipient
}

// NewService creates a vault Service. masterIdentityStr is the raw age secret
// key string (typically from the STELLA_VAULT_KEY environment variable).
func NewService(db DB, masterIdentityStr string) (*Service, error) {
	id, recipient, err := ParseMasterIdentity(masterIdentityStr)
	if err != nil {
		return nil, fmt.Errorf("vault: new service: %w", err)
	}
	return &Service{
		db:              db,
		masterIdentity:  id,
		masterRecipient: recipient,
	}, nil
}

// MasterRecipient returns the master public key recipient.
// It is used when generating new user key pairs.
func (s *Service) MasterRecipient() *age.X25519Recipient {
	return s.masterRecipient
}

// EntryMeta holds non-sensitive metadata for a vault entry.
type EntryMeta struct {
	Scope       string
	UserID      string
	AgentID     string
	Name        string
	Description string
	CreatedAt   string
	UpdatedAt   string
}

type SetOptions struct {
	Description *string
}

type ScopeRequest struct {
	Scope        string
	UserID       string
	AgentID      string
	IsAdmin      bool
	AgentScoped  bool
	BoundAgentID string
}

type ResolvedScope struct {
	Scope   string
	UserID  string
	AgentID string
}

func ResolveScope(req ScopeRequest) (ResolvedScope, error) {
	scope := req.Scope
	if scope == "" {
		scope = ScopeUser
	}
	agentID := req.AgentID
	if req.AgentScoped {
		switch scope {
		case ScopeUser:
		case ScopeUserAgent:
			if agentID != req.BoundAgentID {
				return ResolvedScope{}, fmt.Errorf("vault: agent-scoped identity cannot access another agent's vault: %w", authz.ErrForbidden)
			}
		default:
			return ResolvedScope{}, fmt.Errorf("vault: system-scoped secrets are managed by operators: %w", authz.ErrForbidden)
		}
	}
	out := ResolvedScope{Scope: scope}
	userID := req.UserID
	switch scope {
	case ScopeUser:
		out.UserID = userID
	case ScopeUserAgent:
		if agentID == "" {
			return ResolvedScope{}, fmt.Errorf("vault: agent_id is required for user_agent scope")
		}
		out.UserID = userID
		out.AgentID = agentID
	case ScopeSystem:
		if !req.IsAdmin {
			return ResolvedScope{}, fmt.Errorf("vault: admin access required: %w", authz.ErrForbidden)
		}
	case ScopeSystemAgent:
		if !req.IsAdmin {
			return ResolvedScope{}, fmt.Errorf("vault: admin access required: %w", authz.ErrForbidden)
		}
		if agentID == "" {
			return ResolvedScope{}, fmt.Errorf("vault: agent_id is required for system_agent scope")
		}
		out.AgentID = agentID
	default:
		return ResolvedScope{}, fmt.Errorf("vault: invalid scope %q", scope)
	}
	if err := validateScope(out.Scope, out.UserID, out.AgentID); err != nil {
		return ResolvedScope{}, err
	}
	return out, nil
}

// EncryptSystem encrypts plaintext with the master key for system-level storage
// (not tied to any user).
func (s *Service) EncryptSystem(plaintext string) (string, error) {
	return encryptArmored(s.masterRecipient, plaintext)
}

// DecryptSystem decrypts ciphertext that was produced by EncryptSystem.
func (s *Service) DecryptSystem(ciphertext string) (string, error) {
	return decryptArmored(s.masterIdentity, ciphertext)
}

// Set validates name, encrypts plaintext with the user's public key, and
// upserts the user-level vault entry. The user must already have age keys provisioned.
func (s *Service) Set(ctx context.Context, userID string, name string, plaintext string) error {
	return s.set(ctx, ScopeUser, userID, "", name, plaintext, true, SetOptions{})
}

// SetScoped stores a user-owned secret in the requested vault scope.
func (s *Service) SetScoped(ctx context.Context, scope string, userID string, agentID string, name string, plaintext string) error {
	return s.SetScopedWithOptions(ctx, scope, userID, agentID, name, plaintext, SetOptions{})
}

func (s *Service) SetScopedWithOptions(ctx context.Context, scope string, userID string, agentID string, name string, plaintext string, opts SetOptions) error {
	if isSystemScope(scope) {
		return fmt.Errorf("vault: system scope requires privileged caller")
	}
	return s.set(ctx, scope, userID, agentID, name, plaintext, true, opts)
}

func (s Authorized) Set(ctx context.Context, scope string, name string, plaintext string) (EntryMeta, error) {
	ident := s.ident
	resolved, err := ownedScope(ident, scope)
	if err != nil {
		return EntryMeta{}, err
	}
	if err := s.SetScoped(ctx, resolved.Scope, resolved.UserID, resolved.AgentID, name, plaintext); err != nil {
		return EntryMeta{}, err
	}
	return s.GetScopedMeta(ctx, resolved.Scope, resolved.UserID, resolved.AgentID, name)
}

// SetSystemScoped stores an admin-managed secret. Call only after an explicit
// admin authorization check at the API boundary.
func (s *Service) SetSystemScoped(ctx context.Context, scope string, agentID string, name string, plaintext string) error {
	return s.SetSystemScopedWithOptions(ctx, scope, agentID, name, plaintext, SetOptions{})
}

func (s *Service) SetSystemScopedWithOptions(ctx context.Context, scope string, agentID string, name string, plaintext string, opts SetOptions) error {
	if !isSystemScope(scope) {
		return fmt.Errorf("vault: scope %q is not system-managed", scope)
	}
	return s.set(ctx, scope, "", agentID, name, plaintext, true, opts)
}

func (s *Service) set(ctx context.Context, scope string, userID string, agentID string, name string, plaintext string, validate bool, opts SetOptions) error {
	if validate {
		if err := ValidateName(name); err != nil {
			return err
		}
	}
	if err := validateScope(scope, userID, agentID); err != nil {
		return err
	}

	ciphertext, err := s.encryptForScope(ctx, scope, userID, plaintext)
	if err != nil {
		return fmt.Errorf("vault: set %q: %w", name, err)
	}

	description := pgtype.Text{}
	if opts.Description != nil {
		description = pgnull.Text(*opts.Description)
	}
	_, err = s.db.UpsertVaultEntryByScope(ctx, sqlc.UpsertVaultEntryByScopeParams{
		ID:          uuid.Must(uuid.NewV7()).String(),
		Scope:       scope,
		UserID:      pgnull.Text(userID),
		AgentID:     pgnull.Text(agentID),
		Name:        name,
		Ciphertext:  ciphertext,
		Description: description,
	})
	if err != nil {
		return fmt.Errorf("vault: set %q: upsert: %w", name, err)
	}
	return nil
}

func (s *Service) encryptForScope(ctx context.Context, scope string, userID string, plaintext string) (string, error) {
	if scope == ScopeSystem || scope == ScopeSystemAgent {
		ciphertext, err := s.EncryptSystem(plaintext)
		if err != nil {
			return "", fmt.Errorf("encrypt system: %w", err)
		}
		return ciphertext, nil
	}

	user, err := s.db.GetVaultUser(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("get user: %w", err)
	}
	if user.AgePublicKey == "" {
		return "", fmt.Errorf("user %s has no age public key provisioned", userID)
	}

	ciphertext, err := Encrypt(user.AgePublicKey, plaintext)
	if err != nil {
		return "", fmt.Errorf("encrypt: %w", err)
	}
	return ciphertext, nil
}

// Delete removes a user-level vault entry by name for the given user.
func (s *Service) Delete(ctx context.Context, userID string, name string) error {
	return s.DeleteScoped(ctx, ScopeUser, userID, "", name)
}

// DeleteScoped removes a user-owned vault entry by name and scope.
func (s *Service) DeleteScoped(ctx context.Context, scope string, userID string, agentID string, name string) error {
	if isSystemScope(scope) {
		return fmt.Errorf("vault: system scope requires privileged caller")
	}
	return s.deleteScoped(ctx, scope, userID, agentID, name)
}

func (s Authorized) Delete(ctx context.Context, scope string, name string) error {
	ident := s.ident
	resolved, err := ownedScope(ident, scope)
	if err != nil {
		return err
	}
	return s.DeleteScoped(ctx, resolved.Scope, resolved.UserID, resolved.AgentID, name)
}

// DeleteSystemScoped removes an admin-managed vault entry by name and scope.
func (s *Service) DeleteSystemScoped(ctx context.Context, scope string, agentID string, name string) error {
	if !isSystemScope(scope) {
		return fmt.Errorf("vault: scope %q is not system-managed", scope)
	}
	return s.deleteScoped(ctx, scope, "", agentID, name)
}

func (s *Service) deleteScoped(ctx context.Context, scope string, userID string, agentID string, name string) error {
	if err := validateScope(scope, userID, agentID); err != nil {
		return err
	}
	if err := s.db.DeleteVaultEntryByScope(ctx, sqlc.DeleteVaultEntryByScopeParams{
		Scope:   scope,
		UserID:  pgnull.Text(userID),
		AgentID: pgnull.Text(agentID),
		Name:    name,
	}); err != nil {
		return fmt.Errorf("vault: delete %q: %w", name, err)
	}
	return nil
}

// Get decrypts and returns the plaintext value of a single user-level vault entry by name.
func (s *Service) Get(ctx context.Context, userID string, name string) (string, error) {
	return s.GetScoped(ctx, ScopeUser, userID, "", name)
}

// GetScoped decrypts and returns one scoped vault entry by name.
func (s *Service) GetScoped(ctx context.Context, scope string, userID string, agentID string, name string) (string, error) {
	if err := validateScope(scope, userID, agentID); err != nil {
		return "", err
	}
	entry, err := s.db.GetVaultEntryByScope(ctx, sqlc.GetVaultEntryByScopeParams{
		Scope:   scope,
		UserID:  pgnull.Text(userID),
		AgentID: pgnull.Text(agentID),
		Name:    name,
	})
	if err != nil {
		return "", fmt.Errorf("vault: get %q: %w", name, err)
	}
	return s.decryptEntry(ctx, entry)
}

// GetMeta returns non-sensitive metadata for a single user-level vault entry by name.
func (s *Service) GetMeta(ctx context.Context, userID string, name string) (EntryMeta, error) {
	return s.GetScopedMeta(ctx, ScopeUser, userID, "", name)
}

// GetScopedMeta returns non-sensitive metadata for a single scoped vault entry by name.
func (s *Service) GetScopedMeta(ctx context.Context, scope string, userID string, agentID string, name string) (EntryMeta, error) {
	entry, err := s.db.GetVaultEntryByScope(ctx, sqlc.GetVaultEntryByScopeParams{
		Scope:   scope,
		UserID:  pgnull.Text(userID),
		AgentID: pgnull.Text(agentID),
		Name:    name,
	})
	if err != nil {
		return EntryMeta{}, fmt.Errorf("vault: get meta %q: %w", name, err)
	}
	return s.metaFromEntry(ctx, entry), nil
}

func (s *Service) List(ctx context.Context, userID string) ([]EntryMeta, error) {
	return s.ListScoped(ctx, ScopeUser, userID, "")
}

// ListScoped returns metadata for all user-owned vault entries in one effective scope.
func (s *Service) ListScoped(ctx context.Context, scope string, userID string, agentID string) ([]EntryMeta, error) {
	if isSystemScope(scope) {
		return nil, fmt.Errorf("vault: system scope requires privileged caller")
	}
	return s.listScoped(ctx, scope, userID, agentID)
}

func (s Authorized) List(ctx context.Context, scope string) ([]EntryMeta, error) {
	ident := s.ident
	if scope == "" {
		userScope, err := ownedScope(ident, ScopeUser)
		if err != nil {
			return nil, err
		}
		userEntries, err := s.ListScoped(ctx, userScope.Scope, userScope.UserID, userScope.AgentID)
		if err != nil {
			return nil, err
		}
		agentScope, err := ownedScope(ident, ScopeUserAgent)
		if err != nil {
			return nil, err
		}
		agentEntries, err := s.ListScoped(ctx, agentScope.Scope, agentScope.UserID, agentScope.AgentID)
		if err != nil {
			return nil, err
		}
		return append(userEntries, agentEntries...), nil
	}
	resolved, err := ownedScope(ident, scope)
	if err != nil {
		return nil, err
	}
	return s.ListScoped(ctx, resolved.Scope, resolved.UserID, resolved.AgentID)
}

// ListSystemScoped returns metadata for admin-managed vault entries in one effective scope.
func (s *Service) ListSystemScoped(ctx context.Context, scope string, agentID string) ([]EntryMeta, error) {
	if !isSystemScope(scope) {
		return nil, fmt.Errorf("vault: scope %q is not system-managed", scope)
	}
	return s.listScoped(ctx, scope, "", agentID)
}

func (s *Service) listScoped(ctx context.Context, scope string, userID string, agentID string) ([]EntryMeta, error) {
	if err := validateScope(scope, userID, agentID); err != nil {
		return nil, err
	}
	entries, err := s.db.ListVaultEntriesByScope(ctx, sqlc.ListVaultEntriesByScopeParams{
		Scope:   scope,
		UserID:  pgnull.Text(userID),
		AgentID: pgnull.Text(agentID),
	})
	if err != nil {
		return nil, fmt.Errorf("vault: list: %w", err)
	}
	return s.metasFromEntries(ctx, entries), nil
}

// Lookup decrypts one user-level vault entry by name.
func (s *Service) Lookup(ctx context.Context, userID string, name string) (string, bool, error) {
	entry, err := s.db.GetVaultEntryByScope(ctx, sqlc.GetVaultEntryByScopeParams{
		Scope:   ScopeUser,
		UserID:  pgnull.Text(userID),
		AgentID: pgnull.Text(""),
		Name:    name,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	plaintext, err := s.decryptEntry(ctx, entry)
	if err != nil {
		return "", false, err
	}
	return plaintext, true, nil
}

// LoadEnvForAgentProject resolves runtime env in the SQL precedence order;
// later scopes override earlier scopes. System-managed names stay internal-only.
func (s *Service) LoadEnvForAgentProject(ctx context.Context, userID string, agentID string) (map[string]string, error) {
	entries, err := s.db.ListVaultEntriesForRuntime(ctx, sqlc.ListVaultEntriesForRuntimeParams{
		UserID:         pgnull.Text(userID),
		RuntimeAgentID: pgnull.Text(agentID),
	})
	if err != nil {
		return nil, fmt.Errorf("vault: load env: list entries: %w", err)
	}

	env := make(map[string]string, len(entries))
	for _, e := range entries {
		if !isAmbientSecretName(e.Name) {
			continue
		}
		plaintext, err := s.decryptEntry(ctx, e)
		if err != nil {
			slog.Warn("vault env entry skipped",
				"component", "vault",
				"scope", e.Scope,
				"name", e.Name,
				"error", err,
			)
			continue
		}
		env[e.Name] = plaintext
	}
	return env, nil
}

func (s *Service) decryptEntry(ctx context.Context, entry sqlc.VaultEntry) (string, error) {
	if entry.Scope == ScopeSystem || entry.Scope == ScopeSystemAgent {
		return s.DecryptSystem(entry.Ciphertext)
	}
	if !entry.UserID.Valid || entry.UserID.String == "" {
		return "", fmt.Errorf("user-scoped entry %q has no user", entry.Name)
	}
	user, err := s.db.GetVaultUser(ctx, entry.UserID.String)
	if err != nil {
		return "", fmt.Errorf("get user: %w", err)
	}
	if user.AgePrivateKey == "" {
		return "", fmt.Errorf("user %s has no age private key provisioned", entry.UserID.String)
	}
	plaintext, err := Decrypt(s.masterIdentity, user.AgePrivateKey, entry.Ciphertext)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return plaintext, nil
}

func (s *Service) metaFromEntry(ctx context.Context, e sqlc.VaultEntry) EntryMeta {
	meta := EntryMeta{
		Scope:       e.Scope,
		UserID:      stringFromNull(e.UserID),
		AgentID:     stringFromNull(e.AgentID),
		Name:        e.Name,
		Description: stringFromNull(e.Description),
		CreatedAt:   e.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   e.UpdatedAt.UTC().Format(time.RFC3339),
	}
	return meta
}

func (s *Service) metasFromEntries(ctx context.Context, entries []sqlc.VaultEntry) []EntryMeta {
	meta := make([]EntryMeta, len(entries))
	for i, e := range entries {
		meta[i] = s.metaFromEntry(ctx, e)
	}
	return meta
}

func ownedScope(ident authz.Identity, scope string) (ResolvedScope, error) {
	if err := ident.RequireUser(); err != nil {
		return ResolvedScope{}, err
	}
	if scope == "" {
		scope = ScopeUser
	}
	if ident.AgentScoped {
		if scope == ScopeUserAgent && ident.AgentID == "" {
			return ResolvedScope{}, authz.ErrForbidden
		}
		return ResolveScope(ScopeRequest{
			Scope:        scope,
			UserID:       ident.UserID,
			AgentID:      ident.AgentID,
			AgentScoped:  true,
			BoundAgentID: ident.AgentID,
		})
	}
	return ResolveScope(ScopeRequest{Scope: scope, UserID: ident.UserID, AgentID: ident.AgentID})
}

func isSystemScope(scope string) bool {
	return scope == ScopeSystem || scope == ScopeSystemAgent
}

func IsAgentScope(scope string) bool {
	return scope == ScopeUserAgent || scope == ScopeSystemAgent
}

func validateScope(scope string, userID string, agentID string) error {
	switch scope {
	case ScopeUser:
		if userID == "" || agentID != "" {
			return fmt.Errorf("vault: user scope requires user_id only")
		}
	case ScopeUserAgent:
		if userID == "" || agentID == "" {
			return fmt.Errorf("vault: user_agent scope requires user_id and agent_id")
		}
	case ScopeSystem:
		if userID != "" || agentID != "" {
			return fmt.Errorf("vault: system scope cannot include user_id or agent_id")
		}
	case ScopeSystemAgent:
		if userID != "" || agentID == "" {
			return fmt.Errorf("vault: system_agent scope requires agent_id only")
		}
	default:
		return fmt.Errorf("vault: invalid scope %q", scope)
	}
	return nil
}

func isAmbientSecretName(name string) bool {
	switch name {
	case "STELLA_TOKEN", oauth.VaultKeyGitHub, oauth.VaultKeyLark, oauth.VaultKeyFeishu:
		return false
	}
	return !strings.HasPrefix(name, "OAUTH_")
}

func stringFromNull(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}
