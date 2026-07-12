package email

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
)

// Access is one email use case bound to exactly one Authorizer evaluation. The
// email Service is the sole policy-enforcement point for the ResourceEmail
// capability: transports and the agent tool pass a trusted authz.Authority and
// never a bare identity. Email is user-owned — a delegated AgentActor has the
// same access as its delegating user (the account config lives in that user's
// vault), so the resource is authorized as the acting user's own.
type Access struct {
	svc     *Service
	eval    authz.Evaluation
	userID  string
	agentID string
}

// Begin opens exactly one evaluation for one email use case.
func (s *Service) Begin(ctx context.Context, authority authz.Authority) (*Access, error) {
	if s == nil || s.authz == nil {
		return nil, fmt.Errorf("email authorization unavailable: authorizer not configured")
	}
	if !authority.Valid() {
		return nil, authz.ErrForbidden
	}
	eval, err := s.authz.Begin(ctx, authority)
	if err != nil {
		return nil, fmt.Errorf("email authorization begin: %w", err)
	}
	actor := authority.Actor()
	agentID := ""
	if actor.Kind() == authz.ActorAgent {
		agentID = string(actor.AgentID())
	}
	return &Access{svc: s, eval: eval, userID: string(actor.UserID()), agentID: agentID}, nil
}

func (a *Access) Accounts(ctx context.Context) (AccountList, error) {
	if err := a.authorize(authz.ActionRead); err != nil {
		return AccountList{}, err
	}
	cfg, err := a.svc.loadConfig(ctx, a.userID)
	if err != nil {
		return AccountList{}, err
	}
	return AccountList{Accounts: cfg.AccountNames(), Default: cfg.Default}, nil
}

func (a *Access) Folders(ctx context.Context, account string) ([]string, error) {
	if err := a.authorize(authz.ActionRead); err != nil {
		return nil, err
	}
	acct, err := a.svc.loadAccount(ctx, a.userID, account)
	if err != nil {
		return nil, err
	}
	return Folders(acct)
}

func (a *Access) List(ctx context.Context, account string, opts ListOptions) ([]Envelope, error) {
	if err := a.authorize(authz.ActionRead); err != nil {
		return nil, err
	}
	acct, err := a.svc.loadAccount(ctx, a.userID, account)
	if err != nil {
		return nil, err
	}
	return List(acct, opts)
}

func (a *Access) Read(ctx context.Context, account, folder string, uid uint32) (*Message, error) {
	if err := a.authorize(authz.ActionRead); err != nil {
		return nil, err
	}
	acct, err := a.svc.loadAccount(ctx, a.userID, account)
	if err != nil {
		return nil, err
	}
	return Read(acct, folder, uid)
}

func (a *Access) MarkSeen(ctx context.Context, account, folder string, uid uint32, seen bool) error {
	if err := a.authorize(authz.ActionWrite); err != nil {
		return err
	}
	acct, err := a.svc.loadAccount(ctx, a.userID, account)
	if err != nil {
		return err
	}
	return MarkSeen(acct, folder, uid, seen)
}

func (a *Access) Send(ctx context.Context, account string, opts SendOptions, idempotencyKey string) (SendResult, error) {
	if err := a.authorize(authz.ActionExecute); err != nil {
		return SendResult{}, err
	}
	return a.svc.send(ctx, a.userID, account, opts, idempotencyKey)
}

// authorize decides one email action for the acting user under this Access's
// single revision. is_owner is always true: the resource is the acting user's
// own email capability (their vault-stored account config). A denial is an
// authenticated 403 (ErrForbidden), matching the pre-cutover contract.
func (a *Access) authorize(action authz.Action) error {
	if a.userID == "" {
		return authz.ErrUnauthenticated
	}
	facts := policy.OwnedFacts{Owner: a.userID, Agent: a.agentID, IsOwner: true}
	req, err := policy.EmailRequest(action, a.userID, a.userID, facts)
	if err != nil {
		return authz.ErrForbidden
	}
	dec, err := a.eval.Decide(req)
	if err != nil {
		return fmt.Errorf("email decide: %w", err)
	}
	if !dec.Allowed() {
		return authz.ErrForbidden
	}
	return nil
}
