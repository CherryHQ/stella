package email

import "context"

// ResolveUser is a trusted system adapter. It resolves the already-admitted
// actor from context; it must never read model arguments or request fields.
type ResolveUser func(context.Context) (string, error)

type userAccess struct {
	svc    *Service
	userID string
}

func (s *Service) Access(ctx context.Context) (Access, error) {
	if s == nil {
		return nil, errServiceUnavailable
	}
	if s.resolveUser == nil {
		return nil, errAuthorizationUnavailable
	}
	userID, err := s.resolveUser(ctx)
	if err != nil {
		return nil, err
	}
	if userID == "" {
		return nil, errMissingUser
	}
	return &userAccess{svc: s, userID: userID}, nil
}

func (a *userAccess) Accounts(ctx context.Context) (AccountList, error) {
	cfg, err := a.svc.loadConfig(ctx, a.userID)
	if err != nil {
		return AccountList{}, err
	}
	return AccountList{Accounts: cfg.AccountNames(), Default: cfg.Default}, nil
}

func (a *userAccess) Folders(ctx context.Context, account string) ([]string, error) {
	acct, err := a.svc.loadAccount(ctx, a.userID, account)
	if err != nil {
		return nil, err
	}
	return Folders(acct)
}

func (a *userAccess) List(ctx context.Context, account string, opts ListOptions) ([]Envelope, error) {
	acct, err := a.svc.loadAccount(ctx, a.userID, account)
	if err != nil {
		return nil, err
	}
	return List(acct, opts)
}

func (a *userAccess) Read(ctx context.Context, account, folder string, uid uint32) (*Message, error) {
	acct, err := a.svc.loadAccount(ctx, a.userID, account)
	if err != nil {
		return nil, err
	}
	return Read(acct, folder, uid)
}

func (a *userAccess) MarkSeen(ctx context.Context, account, folder string, uid uint32, seen bool) error {
	acct, err := a.svc.loadAccount(ctx, a.userID, account)
	if err != nil {
		return err
	}
	return MarkSeen(acct, folder, uid, seen)
}

func (a *userAccess) Send(ctx context.Context, account string, opts SendOptions, idempotencyKey string) (SendResult, error) {
	return a.svc.send(ctx, a.userID, account, opts, idempotencyKey)
}
