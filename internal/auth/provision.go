package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const maxUsernameAttempts = 20

// ProvisionRequest carries the information needed to provision a new user.
type ProvisionRequest struct {
	Platform   string
	ExternalID string
	Name       string
	EmailHint  string

	// OnUserCreated is invoked after CreateUser succeeds and before the identity
	// is created. Callers can use it to provision optional user-scoped resources
	// such as vault keys without adding auth->resource-package dependencies.
	OnUserCreated func(ctx context.Context, userID int64) error
}

// ProvisionIdentityUser creates a new user + identity pair atomically.
// It is idempotent: if the (platform, externalID) identity already exists,
// the existing user is returned without creating anything new.
//
// On a concurrent race where two callers both miss the initial identity lookup
// and one loses the unique-constraint insert, the loser re-reads the winning
// identity/user and returns it rather than propagating an error.
func ProvisionIdentityUser(ctx context.Context, store AuthStore, req ProvisionRequest) (AuthUser, error) {
	// Fast path: identity already exists.
	existing, err := store.GetIdentityByPlatform(ctx, req.Platform, req.ExternalID)
	if err == nil {
		user, err := store.GetUser(ctx, existing.UserID)
		if err != nil {
			return AuthUser{}, fmt.Errorf("provision: get existing user: %w", err)
		}
		return user, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AuthUser{}, fmt.Errorf("provision: check identity: %w", err)
	}

	username, err := deriveUsername(ctx, store, req.EmailHint, req.ExternalID, req.Platform)
	if err != nil {
		return AuthUser{}, err
	}

	user, err := store.CreateUser(ctx, username, "") // empty hash = no web login
	if err != nil {
		return AuthUser{}, fmt.Errorf("provision: create user: %w", err)
	}

	if req.OnUserCreated != nil {
		if err := req.OnUserCreated(ctx, user.ID); err != nil {
			if derr := store.DeleteUser(ctx, user.ID); derr != nil {
				return AuthUser{}, fmt.Errorf("provision: on user created: %w (cleanup user: %w)", err, derr)
			}
			return AuthUser{}, fmt.Errorf("provision: on user created: %w", err)
		}
	}

	_, identErr := store.CreateIdentity(ctx, Identity{
		UserID:     user.ID,
		Platform:   req.Platform,
		ExternalID: req.ExternalID,
		Name:       req.Name,
	})
	if identErr != nil {
		_ = store.DeleteUser(ctx, user.ID)

		// A concurrent provision may have won the race on the unique constraint.
		if winner, rerr := store.GetIdentityByPlatform(ctx, req.Platform, req.ExternalID); rerr == nil {
			if winUser, rerr := store.GetUser(ctx, winner.UserID); rerr == nil {
				return winUser, nil
			}
		}

		return AuthUser{}, fmt.Errorf("provision: create identity: %w", identErr)
	}

	return user, nil
}

// deriveUsername produces a unique username from an email hint or external ID.
// It tries the base name, then base-2, base-3, … up to maxUsernameAttempts.
// Returns an error if all candidates are taken.
func deriveUsername(ctx context.Context, store AuthStore, emailHint, externalID, platform string) (string, error) {
	base := localPart(emailHint)
	if base == "" {
		id := externalID
		if len(id) > 8 {
			id = id[:8]
		}
		base = platform + "-" + id
	}

	for i := range maxUsernameAttempts {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", base, i+1)
		}
		_, err := store.GetUserByUsername(ctx, candidate)
		if errors.Is(err, sql.ErrNoRows) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("provision: probe username %q: %w", candidate, err)
		}
	}

	return "", fmt.Errorf("provision: no unique username after %d attempts for base %q", maxUsernameAttempts, base)
}

// localPart returns the portion of an email address before the @ sign.
// Returns empty string if there is no @ or the local part is empty.
func localPart(email string) string {
	at := strings.Index(email, "@")
	if at <= 0 {
		return ""
	}
	return email[:at]
}
