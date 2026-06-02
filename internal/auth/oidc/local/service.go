package local

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
)

// Service owns local username/password authentication while the issuer owns
// the OIDC protocol endpoints. Keeping credential submission here prevents
// /authorize from becoming a JSON API with content-negotiation tricks.
type Service struct {
	cfg         *Config
	codes       auth.OIDCCodeStore
	users       auth.UserStore
	credentials auth.CredentialStore
}

type LoginInput struct {
	Email       string
	Password    string
	State       auth.AuthState
	RedirectURI string
}

type RegisterInput struct {
	Name            string
	Email           string
	Password        string
	ConfirmPassword string
	State           auth.AuthState
	RedirectURI     string
}

var (
	ErrRegistrationDisabled = errors.New("registration disabled")
	ErrInvalidInput         = errors.New("invalid input")
	ErrInvalidLogin         = errors.New("invalid login")
	ErrAccountDisabled      = errors.New("account disabled")
	ErrEmailExists          = errors.New("email exists")
)

func NewService(cfg *Config, codes auth.OIDCCodeStore, users auth.UserStore, credentials auth.CredentialStore) *Service {
	return &Service{cfg: cfg, codes: codes, users: users, credentials: credentials}
}

func (s *Service) AllowsRegistration(ctx context.Context) bool {
	if !s.cfg.AllowRegistration {
		return false
	}
	count, err := s.users.CountUsers(ctx)
	return err == nil && count == 0
}

func (s *Service) Login(ctx context.Context, in LoginInput) (string, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email == "" || in.Password == "" {
		return "", ErrInvalidInput
	}

	user, err := s.users.GetUserByEmail(ctx, email)
	if err != nil {
		return "", ErrInvalidLogin
	}
	if !user.IsActive {
		return "", ErrAccountDisabled
	}

	credSvc := auth.NewCredentialService(s.credentials)
	if err := credSvc.VerifyPassword(ctx, user.ID, in.Password); err != nil {
		return "", ErrInvalidLogin
	}

	return s.IssueCode(ctx, user.ID, s.authorizeParams(in.State, in.RedirectURI))
}

func (s *Service) Register(ctx context.Context, in RegisterInput) (string, error) {
	if !s.cfg.AllowRegistration {
		return "", ErrRegistrationDisabled
	}

	name := strings.TrimSpace(in.Name)
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if name == "" || email == "" || in.Password == "" || in.ConfirmPassword == "" {
		return "", ErrInvalidInput
	}
	if len(in.Password) < 8 || in.Password != in.ConfirmPassword {
		return "", ErrInvalidInput
	}

	userID, err := s.createBootstrapUser(ctx, name, email, in.Password)
	if err != nil {
		return "", err
	}
	return s.IssueCode(ctx, userID, s.authorizeParams(in.State, in.RedirectURI))
}

func (s *Service) createBootstrapUser(ctx context.Context, name, email, password string) (string, error) {
	if txner, ok := s.users.(auth.Transactioner); ok {
		stores, commit, rollback, err := txner.BeginAuthTx(ctx)
		if err != nil {
			return "", err
		}
		defer rollback()
		userID, err := createBootstrapUserNoTx(ctx, stores.Users, stores.Credentials, name, email, password)
		if err != nil {
			return "", err
		}
		if err := commit(); err != nil {
			return "", err
		}
		return userID, nil
	}
	return createBootstrapUserNoTx(ctx, s.users, s.credentials, name, email, password)
}

func createBootstrapUserNoTx(ctx context.Context, users auth.UserStore, credentials auth.CredentialStore, name, email, password string) (string, error) {
	if _, err := users.GetUserByEmail(ctx, email); err == nil {
		return "", ErrEmailExists
	} else if !errors.Is(err, auth.ErrNotFound) {
		return "", err
	}

	count, err := users.CountUsers(ctx)
	if err != nil {
		return "", err
	}
	if count > 0 {
		return "", ErrRegistrationDisabled
	}

	newUser, err := users.CreateUser(ctx, auth.User{
		ID:    uuid.NewString(),
		Email: email,
		Name:  name,
		Role:  auth.RoleAdmin,
	})
	if err != nil {
		return "", err
	}

	credSvc := auth.NewCredentialService(credentials)
	if err := credSvc.SetPassword(ctx, newUser.ID, password); err != nil {
		_ = users.DeleteUser(ctx, newUser.ID)
		return "", err
	}
	return newUser.ID, nil
}

func (s *Service) authorizeParams(state auth.AuthState, redirectURI string) *authorizeParams {
	if redirectURI == "" && len(s.cfg.RedirectURIs) > 0 {
		redirectURI = s.cfg.RedirectURIs[0]
	}
	return &authorizeParams{
		clientID:            s.cfg.ClientID,
		redirectURI:         redirectURI,
		state:               state.State,
		scopes:              []string{"openid", "email", "profile"},
		pkceChallenge:       pkceChallengeFromVerifier(state.CodeVerifier),
		pkceChallengeMethod: "S256",
	}
}

func (s *Service) IssueCode(ctx context.Context, userID string, params *authorizeParams) (string, error) {
	rawCode := generateOpaqueToken()
	codeHash := hashToken(rawCode)

	_, err := s.codes.CreateOIDCCode(ctx, auth.OIDCCode{
		ID:            uuid.NewString(),
		CodeHash:      codeHash,
		UserID:        userID,
		ClientID:      params.clientID,
		RedirectURI:   params.redirectURI,
		Scopes:        params.scopes,
		Nonce:         params.nonce,
		PKCEChallenge: params.pkceChallenge,
		PKCEMethod:    params.pkceChallengeMethod,
		ExpiresAt:     time.Now().Add(time.Duration(s.cfg.AuthCodeTTL) * time.Second),
	})
	if err != nil {
		return "", fmt.Errorf("local auth: create authorization code: %w", err)
	}

	q := url.Values{"code": {rawCode}}
	if params.state != "" {
		q.Set("state", params.state)
	}
	return params.redirectURI + "?" + q.Encode(), nil
}
