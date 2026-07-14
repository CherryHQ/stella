package email

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/authz"
)

// Access is one email use case bound to one trusted Authority. Email is a
// user-owned capability: the account config lives in the acting user's vault, so
// ownership is simply the captured userID — every operation loads that user's
// config and nothing else. A delegated AgentActor acts as its delegating user
// (an agent shares its user's mail), so it reaches the same accounts. There is no
// policy evaluation: transports and the agent tool pass a trusted authz.Authority
// and never a bare identity, and the per-user vault namespace is the boundary.
type Access struct {
	svc    *Service
	userID string
}

// Access binds one email use case to a trusted Authority. It rejects an invalid
// Authority (403) and one carrying no user (401) up front, so every method can
// assume a non-empty acting user.
func (s *Service) Access(authority authz.Authority) (*Access, error) {
	if s == nil {
		return nil, fmt.Errorf("email service is unavailable — try again later")
	}
	if !authority.Valid() {
		return nil, authz.ErrForbidden
	}
	userID := string(authority.UserID())
	if userID == "" {
		return nil, authz.ErrUnauthenticated
	}
	return &Access{svc: s, userID: userID}, nil
}

func (a *Access) Accounts(ctx context.Context) (AccountList, error) {
	cfg, err := a.svc.loadConfig(ctx, a.userID)
	if err != nil {
		return AccountList{}, err
	}
	return AccountList{Accounts: cfg.AccountNames(), Default: cfg.Default}, nil
}

func (a *Access) Folders(ctx context.Context, account string) ([]string, error) {
	acct, err := a.svc.loadAccount(ctx, a.userID, account)
	if err != nil {
		return nil, err
	}
	return Folders(acct)
}

func (a *Access) List(ctx context.Context, account string, opts ListOptions) ([]Envelope, error) {
	acct, err := a.svc.loadAccount(ctx, a.userID, account)
	if err != nil {
		return nil, err
	}
	return List(acct, opts)
}

func (a *Access) Read(ctx context.Context, account, folder string, uid uint32) (*Message, error) {
	acct, err := a.svc.loadAccount(ctx, a.userID, account)
	if err != nil {
		return nil, err
	}
	return Read(acct, folder, uid)
}

func (a *Access) MarkSeen(ctx context.Context, account, folder string, uid uint32, seen bool) error {
	acct, err := a.svc.loadAccount(ctx, a.userID, account)
	if err != nil {
		return err
	}
	return MarkSeen(acct, folder, uid, seen)
}

func (a *Access) Send(ctx context.Context, account string, opts SendOptions, idempotencyKey string) (SendResult, error) {
	return a.svc.send(ctx, a.userID, account, opts, idempotencyKey)
}
