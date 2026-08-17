// Package provisioning owns the provisioned-user lifecycle. Its API accepts
// only trusted issuer attribution from the HTTP credential gate; handlers never
// reach auth_user or personal_access_token persistence themselves.
package provisioning

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/auth/account"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/credential"
	"github.com/CherryHQ/stella/internal/vault"
	sqlc "github.com/CherryHQ/stella/pkg/db/sqlc"
)

const DefaultTokenName = "enterprise-integration"

var (
	ErrNotFound      = errors.New("provisioned user not found")
	ErrForbidden     = errors.New("provisioned user lifecycle forbidden")
	ErrExternalIDDup = errors.New("provisioned user external_id already exists")
	ErrEmailDup      = errors.New("provisioned user email already exists")
	ErrIdentityDup   = errors.New("channel identity already exists")
)

// ExternalIDConflict carries only the already-managed resource's safe metadata;
// it never contains a hash or plaintext token.
type ExternalIDConflict struct{ Existing User }

func (e *ExternalIDConflict) Error() string { return ErrExternalIDDup.Error() }
func (e *ExternalIDConflict) Unwrap() error { return ErrExternalIDDup }

// AccountLifecycle is deliberately narrow: deactivation remains in the account
// domain so it continues to revoke every session and PAT use consistently.
type AccountLifecycle interface {
	DeactivateUserIfUserRole(context.Context, authz.Authority, string) (account.AccountView, error)
}

type Service struct {
	db             *pgxpool.Pool
	account        AccountLifecycle
	vaultRecipient *age.X25519Recipient
	log            *slog.Logger
}

func New(db *pgxpool.Pool, account AccountLifecycle, vaultRecipient *age.X25519Recipient, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{db: db, account: account, vaultRecipient: vaultRecipient, log: log}
}

type Issuer struct {
	UserID  string
	TokenID string
}

type CreateInput struct {
	ExternalID string
	Email      string
	Name       string
	TokenName  string
	ExpiresAt  time.Time
}

type Cursor struct {
	CreatedAt time.Time
	ID        string
}

type Token struct {
	ID         string
	Name       string
	Last4      string
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	CreatedAt  time.Time
}

type User struct {
	ID          string
	ExternalID  string
	UserID      string
	Email       string
	Name        string
	Role        string
	IsActive    bool
	ActiveToken *Token
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type CreateResult struct {
	User  User
	Token string // plaintext: caller must return it once and never persist it.
}

type ChannelIdentityInput struct {
	Platform   string
	ExternalID string
	Name       string
}

type ChannelIdentity struct {
	ID         string
	UserID     string
	Platform   string
	ExternalID string
	Name       string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (s *Service) Create(ctx context.Context, issuer Issuer, in CreateInput) (CreateResult, error) {
	if issuer.UserID == "" || issuer.TokenID == "" {
		return CreateResult{}, fmt.Errorf("%w: missing provisioning issuer", ErrForbidden)
	}
	userID := uuid.Must(uuid.NewV7()).String()
	provisionedID := uuid.Must(uuid.NewV7()).String()
	minted, err := credential.MintOpaque(credential.KindPAT)
	if err != nil {
		return CreateResult{}, fmt.Errorf("mint provisioned PAT: %w", err)
	}
	agePublic, agePrivate, err := s.userAgeKeys()
	if err != nil {
		return CreateResult{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return CreateResult{}, fmt.Errorf("begin provisioned user create: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(s.db).WithTx(tx)
	if _, err := q.CreateProvisionedAuthUser(ctx, sqlc.CreateProvisionedAuthUserParams{
		ID: userID, Email: in.Email, Name: in.Name, AgePublicKey: agePublic, AgePrivateKey: agePrivate,
	}); err != nil {
		return CreateResult{}, s.resolveCreateConflict(ctx, tx, classifyCreateError(err), in.ExternalID)
	}
	if _, err := q.CreateProvisionedUser(ctx, sqlc.CreateProvisionedUserParams{
		ID: provisionedID, ExternalID: in.ExternalID, UserID: userID,
		CreatedByUserID:  pgtype.Text{String: issuer.UserID, Valid: true},
		CreatedByTokenID: pgtype.Text{String: issuer.TokenID, Valid: true},
	}); err != nil {
		return CreateResult{}, s.resolveCreateConflict(ctx, tx, classifyCreateError(err), in.ExternalID)
	}
	if _, err := q.CreateProvisionedPersonalAccessToken(ctx, provisionedPATParams(userID, issuer.TokenID, in.TokenName, in.ExpiresAt, minted)); err != nil {
		return CreateResult{}, fmt.Errorf("create provisioned PAT: %w", err)
	}
	row, err := q.GetProvisionedUser(ctx, provisionedID)
	if err != nil {
		return CreateResult{}, fmt.Errorf("load created provisioned user: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return CreateResult{}, fmt.Errorf("commit provisioned user create: %w", err)
	}
	result := CreateResult{User: userFromGet(row), Token: minted.Plaintext}
	s.log.InfoContext(ctx, "provisioned user created", "provisioned_user_id", result.User.ID, "user_id", result.User.UserID, "issuer_user_id", issuer.UserID, "issuer_token_id", issuer.TokenID)
	return result, nil
}

func (s *Service) Get(ctx context.Context, id string) (User, error) {
	row, err := sqlc.New(s.db).GetProvisionedUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("get provisioned user: %w", err)
	}
	return userFromGet(row), nil
}

func (s *Service) getByExternalID(ctx context.Context, externalID string) (User, error) {
	row, err := sqlc.New(s.db).GetProvisionedUserByExternalID(ctx, externalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("get provisioned user by external id: %w", err)
	}
	return userFromExternal(row), nil
}

// resolveCreateConflict lets an idempotent directory retry recover by
// external_id even when auth_user's email uniqueness fires first. It never
// discloses an unmanaged email collision.
func (s *Service) resolveCreateConflict(ctx context.Context, tx pgx.Tx, cause error, externalID string) error {
	if !errors.Is(cause, ErrExternalIDDup) && !errors.Is(cause, ErrEmailDup) {
		return cause
	}
	_ = tx.Rollback(ctx)
	existing, err := s.getByExternalID(ctx, externalID)
	if err == nil {
		return &ExternalIDConflict{Existing: existing}
	}
	return cause
}

// ListAfter fetches limit+1 rows after an immutable (created_at, id) cursor.
// Cursor encoding stays in the transport, but query semantics remain owned here.
func (s *Service) ListAfter(ctx context.Context, limit int, cursor *Cursor) ([]User, error) {
	params := sqlc.ListProvisionedUserAfterParams{PageLimit: int32(limit)}
	if cursor != nil {
		params.CursorCreatedAt = pgtype.Timestamptz{Time: cursor.CreatedAt.UTC(), Valid: true}
		params.CursorID = pgtype.Text{String: cursor.ID, Valid: true}
	}
	rows, err := sqlc.New(s.db).ListProvisionedUserAfter(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list provisioned users: %w", err)
	}
	out := make([]User, 0, len(rows))
	for _, row := range rows {
		out = append(out, userFromList(row))
	}
	return out, nil
}

func (s *Service) Rotate(ctx context.Context, issuer Issuer, id, tokenName string, expiresAt time.Time) (CreateResult, error) {
	if issuer.UserID == "" || issuer.TokenID == "" {
		return CreateResult{}, fmt.Errorf("%w: missing provisioning issuer", ErrForbidden)
	}
	minted, err := credential.MintOpaque(credential.KindPAT)
	if err != nil {
		return CreateResult{}, fmt.Errorf("mint replacement PAT: %w", err)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return CreateResult{}, fmt.Errorf("begin provisioned token rotation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(s.db).WithTx(tx)
	locked, err := q.GetProvisionedUserForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return CreateResult{}, ErrNotFound
	}
	if err != nil {
		return CreateResult{}, fmt.Errorf("lock provisioned user: %w", err)
	}
	if locked.Role != "user" || !locked.IsActive {
		return CreateResult{}, ErrForbidden
	}
	if _, err := q.RevokeProvisionedPersonalAccessTokenByUser(ctx, locked.UserID); err != nil {
		return CreateResult{}, fmt.Errorf("revoke provisioned PAT: %w", err)
	}
	if _, err := q.CreateProvisionedPersonalAccessToken(ctx, provisionedPATParams(locked.UserID, issuer.TokenID, tokenName, expiresAt, minted)); err != nil {
		return CreateResult{}, fmt.Errorf("create replacement provisioned PAT: %w", err)
	}
	row, err := q.GetProvisionedUser(ctx, id)
	if err != nil {
		return CreateResult{}, fmt.Errorf("load rotated provisioned user: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return CreateResult{}, fmt.Errorf("commit provisioned token rotation: %w", err)
	}
	result := CreateResult{User: userFromGet(row), Token: minted.Plaintext}
	s.log.InfoContext(ctx, "provisioned user token rotated", "provisioned_user_id", result.User.ID, "user_id", result.User.UserID, "issuer_user_id", issuer.UserID, "issuer_token_id", issuer.TokenID)
	return result, nil
}

// CreateChannelIdentity attaches a messaging identity without granting an
// interactive login. Ownership follows the administrator that created the
// provisioned user, so that administrator may rotate provisioning tokens
// without orphaning its users.
func (s *Service) CreateChannelIdentity(ctx context.Context, issuer Issuer, id string, in ChannelIdentityInput) (ChannelIdentity, error) {
	if issuer.UserID == "" || issuer.TokenID == "" {
		return ChannelIdentity{}, fmt.Errorf("%w: missing provisioning issuer", ErrForbidden)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ChannelIdentity{}, fmt.Errorf("begin channel identity create: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := sqlc.New(s.db).WithTx(tx)
	locked, err := q.GetOwnedProvisionedUserForUpdate(ctx, sqlc.GetOwnedProvisionedUserForUpdateParams{
		ID:              id,
		CreatedByUserID: pgtype.Text{String: issuer.UserID, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ChannelIdentity{}, ErrNotFound
	}
	if err != nil {
		return ChannelIdentity{}, fmt.Errorf("lock owned provisioned user: %w", err)
	}
	if locked.Role != auth.RoleUser || !locked.IsActive {
		return ChannelIdentity{}, ErrForbidden
	}

	row, err := q.CreateProvisionedUserChannelIdentity(ctx, sqlc.CreateProvisionedUserChannelIdentityParams{
		ID:         uuid.Must(uuid.NewV7()).String(),
		UserID:     locked.UserID,
		Platform:   in.Platform,
		ExternalID: in.ExternalID,
		Name:       in.Name,
	})
	if err != nil {
		return ChannelIdentity{}, classifyChannelIdentityError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ChannelIdentity{}, fmt.Errorf("commit channel identity create: %w", err)
	}
	identity := ChannelIdentity{
		ID: row.ID, UserID: row.UserID, Platform: row.Platform, ExternalID: row.ExternalID,
		Name: row.Name, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
	s.log.InfoContext(ctx, "provisioned user channel identity created",
		"provisioned_user_id", id, "user_id", identity.UserID,
		"platform", identity.Platform, "issuer_user_id", issuer.UserID, "issuer_token_id", issuer.TokenID)
	return identity, nil
}

func (s *Service) Deactivate(ctx context.Context, issuer Issuer, authority authz.Authority, id string) (User, error) {
	if issuer.UserID == "" || issuer.TokenID == "" {
		return User{}, fmt.Errorf("%w: missing provisioning issuer", ErrForbidden)
	}
	user, err := s.Get(ctx, id)
	if err != nil {
		return User{}, err
	}
	if s.account == nil {
		return User{}, fmt.Errorf("deactivate provisioned user: account lifecycle unavailable")
	}
	if !authority.Valid() || !authority.IsAdmin() || string(authority.UserID()) != issuer.UserID {
		return User{}, fmt.Errorf("%w: invalid provisioning issuer authority", ErrForbidden)
	}
	if _, err := s.account.DeactivateUserIfUserRole(ctx, authority, user.UserID); err != nil {
		if errors.Is(err, account.ErrForbidden) {
			return User{}, ErrForbidden
		}
		return User{}, fmt.Errorf("deactivate provisioned user: %w", err)
	}
	updated, err := s.Get(ctx, id)
	if err != nil {
		return User{}, err
	}
	s.log.InfoContext(ctx, "provisioned user deactivated", "provisioned_user_id", updated.ID, "user_id", updated.UserID, "issuer_user_id", issuer.UserID, "issuer_token_id", issuer.TokenID)
	return updated, nil
}

func (s *Service) userAgeKeys() (string, string, error) {
	if s.vaultRecipient == nil {
		return "", "", nil
	}
	public, private, err := vault.GenerateUserKeys(s.vaultRecipient)
	if err != nil {
		return "", "", fmt.Errorf("generate provisioned user age keys: %w", err)
	}
	return public, private, nil
}

func provisionedPATParams(userID, issuerTokenID, name string, expiresAt time.Time, minted credential.Minted) sqlc.CreateProvisionedPersonalAccessTokenParams {
	return sqlc.CreateProvisionedPersonalAccessTokenParams{
		PublicID: minted.PublicID, UserID: userID, Name: name, TokenHash: minted.TokenHash, Last4: minted.Last4,
		Scopes: []string{}, ExpiresAt: pgtype.Timestamptz{Time: expiresAt.UTC(), Valid: true},
		IssuedByTokenID: pgtype.Text{String: issuerTokenID, Valid: true},
	}
}

func sTime(v pgtype.Timestamptz) *time.Time {
	if !v.Valid {
		return nil
	}
	t := v.Time.UTC()
	return &t
}

func token(id, name, last4 string, expiresAt, lastUsedAt pgtype.Timestamptz, createdAt time.Time) *Token {
	if id == "" {
		return nil
	}
	return &Token{ID: id, Name: name, Last4: last4, ExpiresAt: sTime(expiresAt), LastUsedAt: sTime(lastUsedAt), CreatedAt: createdAt.UTC()}
}

func userFromGet(r sqlc.GetProvisionedUserRow) User {
	return User{ID: r.ID, ExternalID: r.ExternalID, UserID: r.UserID, Email: r.Email, Name: r.Name, Role: r.Role, IsActive: r.IsActive, ActiveToken: token(r.TokenID, r.TokenName, r.TokenLast4, r.TokenExpiresAt, r.TokenLastUsedAt, r.TokenCreatedAt), CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
}

func userFromList(r sqlc.ListProvisionedUserAfterRow) User {
	return User{ID: r.ID, ExternalID: r.ExternalID, UserID: r.UserID, Email: r.Email, Name: r.Name, Role: r.Role, IsActive: r.IsActive, ActiveToken: token(r.TokenID, r.TokenName, r.TokenLast4, r.TokenExpiresAt, r.TokenLastUsedAt, r.TokenCreatedAt), CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
}

func userFromExternal(r sqlc.GetProvisionedUserByExternalIDRow) User {
	return User{ID: r.ID, ExternalID: r.ExternalID, UserID: r.UserID, Email: r.Email, Name: r.Name, Role: r.Role, IsActive: r.IsActive, ActiveToken: token(r.TokenID, r.TokenName, r.TokenLast4, r.TokenExpiresAt, r.TokenLastUsedAt, r.TokenCreatedAt), CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
}

func classifyCreateError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return fmt.Errorf("create provisioned user: %w", err)
	}
	if pgErr.ConstraintName == "auth_provisioned_user_external_id_key" {
		return ErrExternalIDDup
	}
	if pgErr.ConstraintName == "auth_user_email_key" {
		return ErrEmailDup
	}
	return fmt.Errorf("create provisioned user: %w", err)
}

func classifyChannelIdentityError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "channel_identity_platform_external_id_key" {
		return ErrIdentityDup
	}
	return fmt.Errorf("create provisioned user channel identity: %w", err)
}
