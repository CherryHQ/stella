package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const maxEmailAttempts = 20

// ProvisionRequest carries the information needed to provision a new user.
type ProvisionRequest struct {
	Platform   string
	ExternalID string
	Name       string
	EmailHint  string

	// OnUserCreated is invoked after CreateUser succeeds and before the identity
	// is created. Callers can use it to provision optional user-scoped resources
	// such as vault keys without adding auth->resource-package dependencies.
	OnUserCreated func(ctx context.Context, userID string) error
}

// ProvisionIdentityUser creates a new user + identity pair atomically.
// It is idempotent: if the (platform, externalID) identity already exists,
// the existing user is returned without creating anything new.
//
// On a concurrent race where two callers both miss the initial identity lookup
// and one loses the unique-constraint insert, the loser re-reads the winning
// identity/user and returns it rather than propagating an error.
func ProvisionIdentityUser(ctx context.Context, users UserStore, channelIdents ChannelIdentityStore, req ProvisionRequest) (User, error) {
	// Fast path: identity already exists.
	existing, err := channelIdents.GetChannelIdentityByPlatform(ctx, req.Platform, req.ExternalID)
	if err == nil {
		user, err := users.GetUser(ctx, existing.UserID)
		if err != nil {
			return User{}, fmt.Errorf("provision: get existing user: %w", err)
		}
		return user, nil
	}
	if !isNotFoundErr(err) {
		return User{}, fmt.Errorf("provision: check identity: %w", err)
	}

	email, err := deriveEmail(ctx, users, req.EmailHint, req.ExternalID, req.Platform)
	if err != nil {
		return User{}, err
	}

	user, err := users.CreateUser(ctx, User{
		ID:    uuid.NewString(),
		Email: email,
		Name:  req.Name,
	})
	if err != nil {
		return User{}, fmt.Errorf("provision: create user: %w", err)
	}

	if req.OnUserCreated != nil {
		if err := req.OnUserCreated(ctx, user.ID); err != nil {
			if derr := users.DeleteUser(ctx, user.ID); derr != nil {
				return User{}, fmt.Errorf("provision: on user created: %w (cleanup user: %w)", err, derr)
			}
			return User{}, fmt.Errorf("provision: on user created: %w", err)
		}
	}

	_, identErr := channelIdents.CreateChannelIdentity(ctx, ChannelIdentity{
		ID:         uuid.NewString(),
		UserID:     user.ID,
		Platform:   req.Platform,
		ExternalID: req.ExternalID,
		Name:       req.Name,
	})
	if identErr != nil {
		_ = users.DeleteUser(ctx, user.ID)

		// A concurrent provision may have won the race on the unique constraint.
		if winner, rerr := channelIdents.GetChannelIdentityByPlatform(ctx, req.Platform, req.ExternalID); rerr == nil {
			if winUser, rerr := users.GetUser(ctx, winner.UserID); rerr == nil {
				return winUser, nil
			}
		}

		return User{}, fmt.Errorf("provision: create identity: %w", identErr)
	}

	return user, nil
}

// deriveEmail produces a unique email from an email hint or external ID.
// It tries the base email, then base-2@..., base-3@..., … up to maxEmailAttempts.
// Returns an error if all candidates are taken.
func deriveEmail(ctx context.Context, users UserStore, emailHint, externalID, platform string) (string, error) {
	var base, domain string
	if lp := localPart(emailHint); lp != "" {
		base = lp
		domain = emailHint[len(lp)+1:]
	} else {
		id := externalID
		if len(id) > 9 {
			id = id[:9]
		}
		base = platform + "-" + id
		domain = platform + ".channel"
	}

	for i := range maxEmailAttempts {
		var candidate string
		if i == 0 {
			candidate = base + "@" + domain
		} else {
			candidate = fmt.Sprintf("%s-%d@%s", base, i+1, domain)
		}
		_, err := users.GetUserByEmail(ctx, candidate)
		if isNotFoundErr(err) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("provision: probe email %q: %w", candidate, err)
		}
	}

	return "", fmt.Errorf("provision: no unique email after %d attempts for base %q", maxEmailAttempts, base)
}

// isNotFoundErr reports whether err signals a "not found" condition.
func isNotFoundErr(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, sql.ErrNoRows)
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
