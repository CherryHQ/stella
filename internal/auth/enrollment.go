package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/CherryHQ/stella/internal/vault"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

var (
	ErrEnrollmentInvalidInput     = errors.New("auth: invalid account enrollment input")
	ErrEnrollmentNoActiveAdmin    = errors.New("auth: no active administrator")
	ErrEnrollmentInactiveUser     = errors.New("auth: enrolled user is deactivated")
	ErrEnrollmentIdentityConflict = errors.New("auth: account identities belong to different users")
	ErrEnrollmentEmailConflict    = errors.New("auth: email belongs to another user")
)

// AccountEnrollmentResult describes the resolved account. Created is true
// only when this call created the auth_user row.
type AccountEnrollmentResult struct {
	User    User
	Created bool
}

// AccountEnrollmentInput is the host-bound identity data used by the account
// enrollment transaction. Namespace is supplied by the host registration;
// platform adapters only provide a stable subject and verified claims.
type AccountEnrollmentInput struct {
	Namespace      string
	Subject        string
	Email          string
	EmailSynthetic bool
	Name           string
	AvatarURL      string
	Claims         map[string]string
}

// AccountEnrollmentService creates or completes a regular user with matching
// login and channel identities in the host-bound namespace.
type AccountEnrollmentService struct {
	txner     Transactioner
	recipient *age.X25519Recipient
}

var _ pkgchannel.AccountEnroller = (*AccountEnrollmentService)(nil)

func NewAccountEnrollmentService(txner Transactioner, recipient *age.X25519Recipient) *AccountEnrollmentService {
	return &AccountEnrollmentService{txner: txner, recipient: recipient}
}

func (s *AccountEnrollmentService) EnrollAccount(ctx context.Context, req pkgchannel.EnrollmentRequest) error {
	if strings.TrimSpace(req.Namespace) == "" || strings.TrimSpace(req.Subject) == "" {
		return ErrEnrollmentInvalidInput
	}
	_, err := s.Enroll(ctx, AccountEnrollmentInput{
		Namespace:      req.Namespace,
		Subject:        req.Subject,
		Email:          req.Email,
		EmailSynthetic: req.EmailSynthetic,
		Name:           req.Name,
		AvatarURL:      req.AvatarURL,
		Claims:         req.Claims,
	})
	return err
}

// Enroll resolves an already-normalized account input. It always uses a real transaction;
// compensating deletes are not sufficient for identity admission.
func (s *AccountEnrollmentService) Enroll(ctx context.Context, input AccountEnrollmentInput) (AccountEnrollmentResult, error) {
	if s.txner == nil {
		return AccountEnrollmentResult{}, errors.New("auth: account enrollment requires transactional stores")
	}
	input.Namespace = strings.TrimSpace(input.Namespace)
	input.Subject = strings.TrimSpace(input.Subject)
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	if input.Namespace == "" || input.Subject == "" {
		return AccountEnrollmentResult{}, ErrEnrollmentInvalidInput
	}
	if input.Email == "" {
		return AccountEnrollmentResult{}, ErrEnrollmentInvalidInput
	}

	// Key generation has no durable side effect, so it happens before the
	// transaction while the later user/identity writes remain all-or-nothing.
	var publicKey, privateKey string
	if s.recipient != nil {
		var err error
		publicKey, privateKey, err = vault.GenerateUserKeys(s.recipient)
		if err != nil {
			return AccountEnrollmentResult{}, fmt.Errorf("auth: generate account enrollment vault keys: %w", err)
		}
	}

	for attempt := range 2 {
		result, err := s.enrollOnce(ctx, input, publicKey, privateKey)
		if err == nil {
			return result, nil
		}
		if attempt == 0 && isEnrollmentUniqueConflict(err) {
			continue
		}
		return AccountEnrollmentResult{}, err
	}
	panic("unreachable")
}

func (s *AccountEnrollmentService) enrollOnce(ctx context.Context, input AccountEnrollmentInput, publicKey, privateKey string) (AccountEnrollmentResult, error) {
	stores, commit, rollback, err := s.txner.BeginAuthTx(ctx)
	if err != nil {
		return AccountEnrollmentResult{}, fmt.Errorf("auth: begin account enrollment tx: %w", err)
	}
	defer rollback()

	if err := stores.Admins.LockActiveAdmin(ctx); err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
			return AccountEnrollmentResult{}, ErrEnrollmentNoActiveAdmin
		}
		return AccountEnrollmentResult{}, fmt.Errorf("auth: lock active administrator: %w", err)
	}

	// Read email before identities. Under READ COMMITTED, each statement gets a
	// fresh snapshot: if a concurrent enrollment commits between these reads,
	// the later identity reads must be able to supply the user ID that owns an
	// email observed here. Reading email last can observe the committed user
	// after earlier identity misses and falsely report an email conflict.
	emailUser, emailFound, err := findUserByEmail(ctx, stores.Users, input.Email)
	if err != nil {
		return AccountEnrollmentResult{}, err
	}

	login, loginFound, err := findLoginIdentity(ctx, stores.Logins, input.Namespace, input.Subject)
	if err != nil {
		return AccountEnrollmentResult{}, err
	}
	channel, channelFound, err := findChannelIdentity(ctx, stores.Channels, input.Namespace, input.Subject)
	if err != nil {
		return AccountEnrollmentResult{}, err
	}
	if loginFound && channelFound && login.UserID != channel.UserID {
		return AccountEnrollmentResult{}, ErrEnrollmentIdentityConflict
	}

	userID := ""
	if loginFound {
		userID = login.UserID
	} else if channelFound {
		userID = channel.UserID
	}

	if emailFound && emailUser.ID != userID {
		return AccountEnrollmentResult{}, ErrEnrollmentEmailConflict
	}

	result := AccountEnrollmentResult{}
	if userID != "" {
		user, err := stores.ActiveUsers.GetActiveUserForShare(ctx, userID)
		if errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
			return AccountEnrollmentResult{}, ErrEnrollmentInactiveUser
		}
		if err != nil {
			return AccountEnrollmentResult{}, fmt.Errorf("auth: lock active identity user: %w", err)
		}
		result.User = user
	} else {
		user, err := stores.Users.CreateUser(ctx, User{
			ID:            uuid.Must(uuid.NewV7()).String(),
			Email:         input.Email,
			Name:          input.Name,
			AvatarURL:     input.AvatarURL,
			Role:          RoleUser,
			AgePublicKey:  publicKey,
			AgePrivateKey: privateKey,
		})
		if err != nil {
			return AccountEnrollmentResult{}, fmt.Errorf("auth: create account enrollment user: %w", err)
		}
		result.User = user
		result.Created = true
	}

	claims := make(map[string]any, len(input.Claims)+1)
	for key, value := range input.Claims {
		claims[key] = value
	}
	if input.EmailSynthetic {
		claims["email_synthetic"] = true
	}
	if !loginFound {
		if _, err := stores.Logins.CreateLoginIdentity(ctx, LoginIdentity{
			ID:              uuid.Must(uuid.NewV7()).String(),
			UserID:          result.User.ID,
			Provider:        input.Namespace,
			ProviderSubject: input.Subject,
			Email:           input.Email,
			Name:            input.Name,
			AvatarURL:       input.AvatarURL,
			RawClaims:       claims,
		}); err != nil {
			return AccountEnrollmentResult{}, fmt.Errorf("auth: create login identity: %w", err)
		}
	}
	if !channelFound {
		if _, err := stores.Channels.CreateChannelIdentity(ctx, ChannelIdentity{
			ID:         uuid.Must(uuid.NewV7()).String(),
			UserID:     result.User.ID,
			Platform:   input.Namespace,
			ExternalID: input.Subject,
			Name:       input.Name,
		}); err != nil {
			return AccountEnrollmentResult{}, fmt.Errorf("auth: create channel identity: %w", err)
		}
	}
	if err := commit(); err != nil {
		return AccountEnrollmentResult{}, fmt.Errorf("auth: commit account enrollment: %w", err)
	}
	return result, nil
}

func findLoginIdentity(ctx context.Context, store LoginIdentityStore, namespace, subject string) (LoginIdentity, bool, error) {
	identity, err := store.GetLoginIdentityByProvider(ctx, namespace, subject)
	if errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
		return LoginIdentity{}, false, nil
	}
	if err != nil {
		return LoginIdentity{}, false, fmt.Errorf("auth: get login identity: %w", err)
	}
	return identity, true, nil
}

func findChannelIdentity(ctx context.Context, store ChannelIdentityStore, namespace, subject string) (ChannelIdentity, bool, error) {
	identity, err := store.GetChannelIdentityByPlatform(ctx, namespace, subject)
	if errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
		return ChannelIdentity{}, false, nil
	}
	if err != nil {
		return ChannelIdentity{}, false, fmt.Errorf("auth: get channel identity: %w", err)
	}
	return identity, true, nil
}

func findUserByEmail(ctx context.Context, store UserStore, email string) (User, bool, error) {
	user, err := store.GetUserByEmail(ctx, email)
	if errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("auth: get account enrollment email user: %w", err)
	}
	return user, true, nil
}

func isEnrollmentUniqueConflict(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	switch pgErr.ConstraintName {
	case "auth_identity_provider_provider_subject_key", "channel_identity_platform_external_id_key", "auth_user_email_key":
		return true
	default:
		return false
	}
}
