package local

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
)

// Service owns local username/password authentication.
type Service struct {
	cfg         *Config
	users       auth.UserStore
	credentials auth.CredentialStore
}

type LoginInput struct {
	Email    string
	Password string
}

type RegisterInput struct {
	Name            string
	Email           string
	Password        string
	ConfirmPassword string
}

var (
	ErrRegistrationDisabled = errors.New("registration disabled")
	ErrInvalidInput         = errors.New("invalid input")
	ErrInvalidLogin         = errors.New("invalid login")
	ErrAccountDisabled      = errors.New("account disabled")
	ErrEmailExists          = errors.New("email exists")
	ErrEmailNotAllowed      = errors.New("email domain not allowed")
)

const dummyPasswordHash = "$2a$12$CeeBKQWOCBTpIR6Gg2zU4u/fKqV3QzpEH2aCLVqA0dZ1ZfqOyLvMu"

func verifyDummyPassword(password string) {
	_ = auth.CheckPassword(dummyPasswordHash, password)
}

func NewService(cfg *Config, users auth.UserStore, credentials auth.CredentialStore) *Service {
	return &Service{cfg: cfg, users: users, credentials: credentials}
}

func (s *Service) AllowsRegistration(ctx context.Context) bool {
	ok, err := s.allowsRegistration(ctx, s.users)
	return err == nil && ok
}

func (s *Service) allowsRegistration(ctx context.Context, users auth.UserStore) (bool, error) {
	if s.cfg.AllowRegistration {
		return true, nil
	}
	if !s.cfg.BootstrapRegistration {
		return false, nil
	}
	count, err := users.CountUsers(ctx)
	if err != nil {
		return false, err
	}
	return count == 0, nil
}

func (s *Service) Login(ctx context.Context, in LoginInput) (string, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email == "" || in.Password == "" || len(in.Password) > 72 {
		return "", ErrInvalidInput
	}

	user, err := s.users.GetUserByEmail(ctx, email)
	if err != nil {
		verifyDummyPassword(in.Password)
		return "", ErrInvalidLogin
	}
	credSvc := auth.NewCredentialService(s.credentials)
	if err := credSvc.VerifyPassword(ctx, user.ID, in.Password); err != nil {
		return "", ErrInvalidLogin
	}
	if !user.IsActive {
		return "", ErrAccountDisabled
	}

	return user.ID, nil
}

func (s *Service) Register(ctx context.Context, in RegisterInput) (string, error) {
	allowed, err := s.allowsRegistration(ctx, s.users)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", ErrRegistrationDisabled
	}

	name := strings.TrimSpace(in.Name)
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if name == "" || email == "" || in.Password == "" || in.ConfirmPassword == "" {
		return "", ErrInvalidInput
	}
	if len(name) > 255 || len(email) > 254 {
		return "", ErrInvalidInput
	}
	if len(in.Password) < 8 || len(in.Password) > 72 || in.Password != in.ConfirmPassword {
		return "", ErrInvalidInput
	}
	if !s.cfg.IsEmailAllowed(email) {
		return "", ErrEmailNotAllowed
	}

	userID, err := s.createRegisteredUser(ctx, name, email, in.Password)
	if err != nil {
		return "", err
	}
	return userID, nil
}

func (s *Service) createRegisteredUser(ctx context.Context, name, email, password string) (string, error) {
	if txner, ok := s.users.(auth.Transactioner); ok {
		stores, commit, rollback, err := txner.BeginAuthTx(ctx)
		if err != nil {
			return "", err
		}
		defer rollback()
		userID, err := s.createRegisteredUserNoTx(ctx, stores.Users, stores.Credentials, name, email, password)
		if err != nil {
			return "", err
		}
		if err := commit(); err != nil {
			return "", err
		}
		return userID, nil
	}
	return s.createRegisteredUserNoTx(ctx, s.users, s.credentials, name, email, password)
}

func (s *Service) createRegisteredUserNoTx(ctx context.Context, users auth.UserStore, credentials auth.CredentialStore, name, email, password string) (string, error) {
	if _, err := users.GetUserByEmail(ctx, email); err == nil {
		return "", ErrEmailExists
	} else if !errors.Is(err, auth.ErrNotFound) {
		return "", err
	}

	// The first account ever registered bootstraps the admin; everyone after is
	// a regular user.
	count, err := users.CountUsers(ctx)
	if err != nil {
		return "", err
	}
	if count > 0 && !s.cfg.AllowRegistration {
		return "", ErrRegistrationDisabled
	}
	role := auth.RoleUser
	if count == 0 {
		role = auth.RoleAdmin
	}

	newUser, err := users.CreateUser(ctx, auth.User{
		ID:    uuid.Must(uuid.NewV7()).String(),
		Email: email,
		Name:  name,
		Role:  role,
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
