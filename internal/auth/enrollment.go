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
)

const feishuProvider = "feishu"

var (
	ErrEnrollmentInvalidInput     = errors.New("auth: invalid Feishu enrollment input")
	ErrEnrollmentNoActiveAdmin    = errors.New("auth: no active administrator")
	ErrEnrollmentInactiveUser     = errors.New("auth: enrolled user is deactivated")
	ErrEnrollmentIdentityConflict = errors.New("auth: Feishu identities belong to different users")
	ErrEnrollmentEmailConflict    = errors.New("auth: email belongs to another user")
)

// FeishuEnrollmentInput contains a verified Feishu member profile. The channel
// adapter is responsible for proving tenant membership before calling Enroll.
type FeishuEnrollmentInput struct {
	UnionID   string
	TenantKey string
	Email     string
	Name      string
	AvatarURL string
}

// FeishuEnrollmentResult describes the resolved account. Created is true only
// when this call created the auth_user row.
type FeishuEnrollmentResult struct {
	User    User
	Created bool
}

// FeishuEnrollmentService creates or completes the Feishu identity shape used
// by OAuth: one active regular user with matching login and channel identities.
type FeishuEnrollmentService struct {
	txner     Transactioner
	recipient *age.X25519Recipient
}

func NewFeishuEnrollmentService(txner Transactioner, recipient *age.X25519Recipient) *FeishuEnrollmentService {
	return &FeishuEnrollmentService{txner: txner, recipient: recipient}
}

// Enroll resolves a Feishu member by canonical union ID. It always uses a real
// transaction; compensating deletes are not sufficient for identity admission.
func (s *FeishuEnrollmentService) Enroll(ctx context.Context, input FeishuEnrollmentInput) (FeishuEnrollmentResult, error) {
	if s.txner == nil {
		return FeishuEnrollmentResult{}, errors.New("auth: Feishu enrollment requires transactional stores")
	}
	input.UnionID = strings.TrimSpace(input.UnionID)
	input.TenantKey = strings.TrimSpace(input.TenantKey)
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	if input.UnionID == "" {
		return FeishuEnrollmentResult{}, ErrEnrollmentInvalidInput
	}
	if input.Email == "" {
		input.Email = SyntheticFeishuEmail(input.UnionID, input.TenantKey)
	}

	// Key generation has no durable side effect, so it happens before the
	// transaction while the later user/identity writes remain all-or-nothing.
	var publicKey, privateKey string
	if s.recipient != nil {
		var err error
		publicKey, privateKey, err = vault.GenerateUserKeys(s.recipient)
		if err != nil {
			return FeishuEnrollmentResult{}, fmt.Errorf("auth: generate Feishu enrollment vault keys: %w", err)
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
		return FeishuEnrollmentResult{}, err
	}
	panic("unreachable")
}

func (s *FeishuEnrollmentService) enrollOnce(ctx context.Context, input FeishuEnrollmentInput, publicKey, privateKey string) (FeishuEnrollmentResult, error) {
	stores, commit, rollback, err := s.txner.BeginAuthTx(ctx)
	if err != nil {
		return FeishuEnrollmentResult{}, fmt.Errorf("auth: begin Feishu enrollment tx: %w", err)
	}
	defer rollback()

	if err := stores.Admins.LockActiveAdmin(ctx); err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
			return FeishuEnrollmentResult{}, ErrEnrollmentNoActiveAdmin
		}
		return FeishuEnrollmentResult{}, fmt.Errorf("auth: lock active administrator: %w", err)
	}

	// Read email before identities. Under READ COMMITTED, each statement gets a
	// fresh snapshot: if a concurrent enrollment commits between these reads,
	// the later identity reads must be able to supply the user ID that owns an
	// email observed here. Reading email last can observe the committed user
	// after earlier identity misses and falsely report an email conflict.
	emailUser, emailFound, err := findUserByEmail(ctx, stores.Users, input.Email)
	if err != nil {
		return FeishuEnrollmentResult{}, err
	}

	login, loginFound, err := findLoginIdentity(ctx, stores.Logins, input.UnionID)
	if err != nil {
		return FeishuEnrollmentResult{}, err
	}
	channel, channelFound, err := findChannelIdentity(ctx, stores.Channels, input.UnionID)
	if err != nil {
		return FeishuEnrollmentResult{}, err
	}
	if loginFound && channelFound && login.UserID != channel.UserID {
		return FeishuEnrollmentResult{}, ErrEnrollmentIdentityConflict
	}

	userID := ""
	if loginFound {
		userID = login.UserID
	} else if channelFound {
		userID = channel.UserID
	}

	if emailFound && emailUser.ID != userID {
		return FeishuEnrollmentResult{}, ErrEnrollmentEmailConflict
	}

	result := FeishuEnrollmentResult{}
	if userID != "" {
		user, err := stores.ActiveUsers.GetActiveUserForShare(ctx, userID)
		if errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
			return FeishuEnrollmentResult{}, ErrEnrollmentInactiveUser
		}
		if err != nil {
			return FeishuEnrollmentResult{}, fmt.Errorf("auth: lock active Feishu identity user: %w", err)
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
			return FeishuEnrollmentResult{}, fmt.Errorf("auth: create Feishu enrollment user: %w", err)
		}
		result.User = user
		result.Created = true
	}

	claims := map[string]any{"tenant_key": input.TenantKey}
	if input.Email == SyntheticFeishuEmail(input.UnionID, input.TenantKey) {
		claims["email_synthetic"] = true
	}
	if !loginFound {
		if _, err := stores.Logins.CreateLoginIdentity(ctx, LoginIdentity{
			ID:              uuid.Must(uuid.NewV7()).String(),
			UserID:          result.User.ID,
			Provider:        feishuProvider,
			ProviderSubject: input.UnionID,
			Email:           input.Email,
			Name:            input.Name,
			AvatarURL:       input.AvatarURL,
			RawClaims:       claims,
		}); err != nil {
			return FeishuEnrollmentResult{}, fmt.Errorf("auth: create Feishu login identity: %w", err)
		}
	}
	if !channelFound {
		if _, err := stores.Channels.CreateChannelIdentity(ctx, ChannelIdentity{
			ID:         uuid.Must(uuid.NewV7()).String(),
			UserID:     result.User.ID,
			Platform:   feishuProvider,
			ExternalID: input.UnionID,
			Name:       input.Name,
		}); err != nil {
			return FeishuEnrollmentResult{}, fmt.Errorf("auth: create Feishu channel identity: %w", err)
		}
	}
	if err := commit(); err != nil {
		return FeishuEnrollmentResult{}, fmt.Errorf("auth: commit Feishu enrollment: %w", err)
	}
	return result, nil
}

func findLoginIdentity(ctx context.Context, store LoginIdentityStore, unionID string) (LoginIdentity, bool, error) {
	identity, err := store.GetLoginIdentityByProvider(ctx, feishuProvider, unionID)
	if errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
		return LoginIdentity{}, false, nil
	}
	if err != nil {
		return LoginIdentity{}, false, fmt.Errorf("auth: get Feishu login identity: %w", err)
	}
	return identity, true, nil
}

func findChannelIdentity(ctx context.Context, store ChannelIdentityStore, unionID string) (ChannelIdentity, bool, error) {
	identity, err := store.GetChannelIdentityByPlatform(ctx, feishuProvider, unionID)
	if errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
		return ChannelIdentity{}, false, nil
	}
	if err != nil {
		return ChannelIdentity{}, false, fmt.Errorf("auth: get Feishu channel identity: %w", err)
	}
	return identity, true, nil
}

func findUserByEmail(ctx context.Context, store UserStore, email string) (User, bool, error) {
	user, err := store.GetUserByEmail(ctx, email)
	if errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("auth: get Feishu enrollment email user: %w", err)
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
