package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/CherryHQ/stella/internal/authz"
)

const fileGrantPrefix = "MCP_FILE_GRANT_"

// fileGrant is the durable per-registration/per-owner fence. It is one Vault
// object so generation, revocation, and OAuth bundle writes share one CAS
// boundary. The bearer token remains in its authored CredentialRef entry.
type fileGrant struct {
	Generation string       `json:"generation"`
	Revoked    bool         `json:"revoked,omitzero"`
	Bundle     *OAuthBundle `json:"bundle,omitempty"`
}

func fileGrantName(reg Registration, owner CredentialOwner) string {
	digest := sha256.Sum256([]byte(fileCredentialKey(reg, owner)))
	return fileGrantPrefix + strings.ToUpper(hex.EncodeToString(digest[:]))
}

func fileAdvisoryKey(reg Registration, owner CredentialOwner) string {
	digest := sha256.Sum256([]byte(fileCredentialKey(reg, owner)))
	return hex.EncodeToString(digest[:])
}

func fileClientStateName(reg Registration) string {
	return "MCP_FILE_CLIENT_" + strings.ToUpper(strings.ReplaceAll(reg.ID, "-", "_"))
}

// withFileVault is the only file-grant mutation/read boundary. PostgreSQL's
// transaction advisory lock serializes refresh, callback, disconnect, and
// explicit bearer replacement for one exact registration owner.
func (s *Service) withFileVault(ctx context.Context, reg Registration, owner CredentialOwner, fn func(Vault) error) error {
	if s == nil || s.vault == nil || s.pool == nil || s.bindVault == nil {
		return errPluginCredentialsUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("mcp: begin file credential transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, fileAdvisoryKey(reg, owner)); err != nil {
		return fmt.Errorf("mcp: lock file credential grant: %w", err)
	}
	vault := s.bindVault(tx)
	if vault == nil {
		return errPluginCredentialsUnavailable
	}
	if err := fn(vault); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("mcp: commit file credential transaction: %w", err)
	}
	return nil
}

func fileVaultGet(ctx context.Context, vault Vault, owner CredentialOwner, name string) (string, error) {
	if IsSystemScope(owner.Scope) {
		return vault.GetScoped(ctx, owner.Scope, "", owner.AgentID, name)
	}
	return vault.GetScoped(ctx, owner.Scope, owner.UserID, owner.AgentID, name)
}

func fileVaultSet(ctx context.Context, vault Vault, owner CredentialOwner, name, value string) error {
	if IsSystemScope(owner.Scope) {
		return vault.SetSystemScoped(ctx, owner.Scope, owner.AgentID, name, value)
	}
	return vault.SetScoped(ctx, owner.Scope, owner.UserID, owner.AgentID, name, value)
}

func readFileGrant(ctx context.Context, vault Vault, owner CredentialOwner, reg Registration) (fileGrant, string, error) {
	raw, err := fileVaultGet(ctx, vault, owner, fileGrantName(reg, owner))
	if errors.Is(err, pgx.ErrNoRows) {
		return fileGrant{}, "", nil
	}
	if err != nil {
		return fileGrant{}, "", err
	}
	if raw == "" {
		return fileGrant{}, "", nil
	}
	var grant fileGrant
	if err := json.Unmarshal([]byte(raw), &grant); err != nil {
		return fileGrant{}, "", fmt.Errorf("mcp: decode file credential grant: %w", err)
	}
	return grant, raw, nil
}

func writeFileGrant(ctx context.Context, vault Vault, owner CredentialOwner, reg Registration, grant fileGrant) (string, error) {
	if grant.Generation == "" {
		grant.Generation = uuid.Must(uuid.NewV7()).String()
	}
	raw, err := json.Marshal(grant)
	if err != nil {
		return "", fmt.Errorf("mcp: encode file credential grant: %w", err)
	}
	return string(raw), fileVaultSet(ctx, vault, owner, fileGrantName(reg, owner), string(raw))
}

func (s *Service) prepareFileOAuthGrant(ctx context.Context, reg Registration, owner CredentialOwner) (string, error) {
	var generation string
	err := s.withFileVault(ctx, reg, owner, func(vault Vault) error {
		generation = uuid.Must(uuid.NewV7()).String()
		old, _, err := readFileGrant(ctx, vault, owner, reg)
		if err != nil {
			return err
		}
		old.Generation, old.Revoked = generation, false
		if old.Bundle != nil {
			old.Bundle.Generation = generation
		}
		_, err = writeFileGrant(ctx, vault, owner, reg, old)
		return err
	})
	return generation, err
}

// FileCredentialReady is the session-facing readiness check. It never returns
// secret material and scopes per-user grants by the verified authority.
func (s *Service) FileCredentialReady(ctx context.Context, reg Registration, authority authz.Authority) (bool, error) {
	owner, err := FileCredentialOwner(reg, authority)
	if err != nil {
		return false, err
	}
	if reg.AuthType == AuthTypeNone {
		return true, nil
	}
	snapshot, err := s.loadFileCredentialSnapshot(ctx, reg, owner)
	if err != nil {
		return false, err
	}
	if reg.AuthType == AuthTypeBearer {
		return snapshot.BearerToken != "", nil
	}
	return snapshot.Bundle != nil && snapshot.Bundle.AccessToken != "", nil
}

// DisconnectFile revokes exactly one file grant by rotating its opaque
// generation. A late callback or refresh cannot recreate it, while a bearer
// secret referenced by another declaration remains in Vault.
func (s *Service) DisconnectFile(ctx context.Context, reg Registration, authority authz.Authority) error {
	owner, err := FileCredentialOwner(reg, authority)
	if err != nil {
		return err
	}
	if reg.CredentialMode == CredentialModeShared && !authority.IsAdmin() {
		return authz.ErrForbidden
	}
	if err := s.withFileVault(ctx, reg, owner, func(vault Vault) error {
		generation := uuid.Must(uuid.NewV7()).String()
		_, err := writeFileGrant(ctx, vault, owner, reg, fileGrant{Generation: generation, Revoked: true})
		return err
	}); err != nil {
		return err
	}
	// The durable revoke is already committed. Remote DELETE is best effort,
	// and may require the credential that was just disconnected; local handles
	// are retired before the SDK performs that optional network cleanup.
	_ = s.fileConnections.closeGrant(reg, owner)
	return nil
}

// SetFileBearerCredential writes a replacement bearer token to Vault and
// clears the grant tombstone. The file declaration remains read-only.
func (s *Service) SetFileBearerCredential(ctx context.Context, reg Registration, authority authz.Authority, token string) error {
	if !reg.IsFile() || reg.AuthType != AuthTypeBearer || strings.TrimSpace(token) == "" || reg.CredentialRef == "" {
		return fmt.Errorf("mcp: invalid file bearer credential")
	}
	owner, err := FileCredentialOwner(reg, authority)
	if err != nil {
		return err
	}
	if reg.CredentialMode == CredentialModeShared && !authority.IsAdmin() {
		return authz.ErrForbidden
	}
	return s.withFileVault(ctx, reg, owner, func(vault Vault) error {
		if err := fileVaultSet(ctx, vault, owner, reg.CredentialRef, token); err != nil {
			return err
		}
		grant, _, err := readFileGrant(ctx, vault, owner, reg)
		if err != nil {
			return err
		}
		grant.Revoked = false
		_, err = writeFileGrant(ctx, vault, owner, reg, grant)
		return err
	})
}

type fileOAuthClientState struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
	AuthStyle    int    `json:"auth_style"`
}

func fileClientOwner(reg Registration) CredentialOwner {
	return CredentialOwner{Scope: reg.Scope, UserID: reg.UserID, AgentID: reg.AgentID}
}

func (s *Service) loadFileClientState(ctx context.Context, reg Registration) (fileOAuthClientState, error) {
	owner := fileClientOwner(reg)
	var state fileOAuthClientState
	err := s.withFileVault(ctx, reg, owner, func(vault Vault) error {
		raw, err := fileVaultGet(ctx, vault, owner, fileClientStateName(reg))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if raw == "" {
			return nil
		}
		return json.Unmarshal([]byte(raw), &state)
	})
	return state, err
}

func (s *Service) storeFileClientStateIfEmpty(ctx context.Context, reg Registration, proposed fileOAuthClientState) (fileOAuthClientState, error) {
	owner := fileClientOwner(reg)
	var winning fileOAuthClientState
	err := s.withFileVault(ctx, reg, owner, func(vault Vault) error {
		raw, err := fileVaultGet(ctx, vault, owner, fileClientStateName(reg))
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &winning); err != nil {
				return err
			}
			return nil
		}
		winning = proposed
		encoded, err := json.Marshal(proposed)
		if err != nil {
			return err
		}
		return fileVaultSet(ctx, vault, owner, fileClientStateName(reg), string(encoded))
	})
	return winning, err
}

func (s *Service) loadFileCredentialSnapshot(ctx context.Context, reg Registration, owner CredentialOwner) (credentialSnapshot, error) {
	if err := validateFileOwner(reg, owner); err != nil {
		return credentialSnapshot{}, err
	}
	var snapshot credentialSnapshot
	err := s.withFileVault(ctx, reg, owner, func(vault Vault) error {
		grant, raw, err := readFileGrant(ctx, vault, owner, reg)
		if err != nil {
			return err
		}
		if grant.Revoked {
			return errFileMCPGrantRevoked
		}
		snapshot.BundleRaw = []byte(raw)
		if reg.AuthType == AuthTypeBearer && reg.CredentialRef != "" {
			// A file declaration does not own a bearer ref until an explicit
			// SetFileBearerCredential binds it to a live grant. Never resolve a
			// package-authored name against Vault while the grant is absent, or a
			// changed endpoint could replay the old declaration's token.
			if grant.Generation == "" {
				return nil
			}
			snapshot.BearerToken, err = fileVaultGet(ctx, vault, owner, reg.CredentialRef)
			if errors.Is(err, pgx.ErrNoRows) {
				err = nil
			}
		}
		if reg.AuthType == AuthTypeOAuth {
			snapshot.Bundle = grant.Bundle
			if reg.OAuthClientSecretRef != "" {
				snapshot.ClientSecret, err = fileVaultGet(ctx, vault, fileClientOwner(reg), reg.OAuthClientSecretRef)
			}
		}
		return err
	})
	return snapshot, err
}

func validateFileOwner(reg Registration, owner CredentialOwner) error {
	if !reg.IsFile() {
		return errPluginConfigIdentity
	}
	// Unauthenticated declarations still carry a trusted scope tuple for
	// session identity, but they never read or write Vault. Keep that tuple
	// aligned with FileCredentialOwner instead of applying per-user credential
	// rules to a credentialless registration.
	if reg.AuthType == AuthTypeNone {
		if owner.Scope != reg.Scope || owner.UserID != reg.UserID || owner.AgentID != reg.AgentID {
			return authz.ErrForbidden
		}
		return nil
	}
	if reg.CredentialMode == CredentialModePerUser {
		if owner.Scope != ScopeUser || owner.UserID == "" || owner.AgentID != "" {
			return authz.ErrForbidden
		}
		return nil
	}
	if owner.Scope != reg.Scope || owner.UserID != reg.UserID || owner.AgentID != reg.AgentID {
		return authz.ErrForbidden
	}
	return nil
}

func (s *Service) storeFileBundleCAS(ctx context.Context, reg Registration, owner CredentialOwner, bundle OAuthBundle, expectedRaw []byte) error {
	if err := validateFileOwner(reg, owner); err != nil {
		return err
	}
	return s.withFileVault(ctx, reg, owner, func(vault Vault) error {
		grant, currentRaw, err := readFileGrant(ctx, vault, owner, reg)
		if err != nil {
			return err
		}
		if grant.Revoked || grant.Generation == "" || bundle.Generation != grant.Generation {
			return errFileMCPGrantRevoked
		}
		if expectedRaw != nil && !rawBundleMatches([]byte(currentRaw), expectedRaw) {
			return errOAuthBundleChanged
		}
		bundle.Generation = grant.Generation
		grant.Bundle = &bundle
		_, err = writeFileGrant(ctx, vault, owner, reg, grant)
		return err
	})
}

func fileGenerationIsCurrent(ctx context.Context, s *Service, reg Registration, owner CredentialOwner, generation string) error {
	return s.withFileVault(ctx, reg, owner, func(vault Vault) error {
		grant, _, err := readFileGrant(ctx, vault, owner, reg)
		if err != nil {
			return err
		}
		if generation == "" || grant.Generation != generation || grant.Revoked {
			return errFileMCPGrantRevoked
		}
		return nil
	})
}
