package mcp

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
)

var (
	errPluginConfigIdentity         = errors.New("mcp: plugin config identity mismatch")
	errPluginCredentialsUnavailable = errors.New("mcp: plugin credential transaction unavailable")
	errOAuthBundleChanged           = errors.New("mcp: oauth bundle changed during refresh")
)

type credentialSnapshot struct {
	BearerToken  string
	Bundle       *OAuthBundle
	BundleRaw    []byte
	ClientSecret string
}

// loadCredentialSnapshot reads the exact common config and its vault entries
// from one repeatable-read transaction. There is no legacy fallback: a
// registration without a common identity is rejected before any secret read.
func (s *Service) loadCredentialSnapshot(ctx context.Context, reg Registration, owner CredentialOwner) (credentialSnapshot, error) {
	if !reg.IsFile() {
		return credentialSnapshot{}, errPluginConfigIdentity
	}
	return s.loadFileCredentialSnapshot(ctx, reg, owner)
}

func (s *Service) loadOAuthClientSecret(ctx context.Context, reg Registration) (string, error) {
	if !reg.IsFile() {
		return "", errPluginConfigIdentity
	}
	state, err := s.loadFileClientState(ctx, reg)
	if err != nil {
		return "", err
	}
	if reg.OAuthClientSecretRef == "" {
		return state.ClientSecret, nil
	}
	owner := fileClientOwner(reg)
	var secret string
	err = s.withFileVault(ctx, reg, owner, func(vault Vault) error {
		secret, err = fileVaultGet(ctx, vault, owner, reg.OAuthClientSecretRef)
		return err
	})
	return secret, err
}

// File observations are response-scoped. A credential rejection only marks
// the live grant dirty; the next turn rechecks Vault and reconnects.
func (s *Service) setStatusForRegistration(_ context.Context, reg Registration, _ CredentialOwner, status, _ string) error {
	if !ValidStatus(status) {
		return fmt.Errorf("mcp: invalid status %q", status)
	}
	if !reg.IsFile() {
		return errPluginConfigIdentity
	}
	return nil
}

func bundleDigest(raw []byte) [32]byte { return sha256.Sum256(raw) }

func rawBundleMatches(got, expected []byte) bool {
	return bundleDigest(got) == bundleDigest(expected)
}
